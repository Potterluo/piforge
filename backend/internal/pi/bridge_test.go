package pi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/pitest"
)

// recordingTranslator emits one event per raw record so framing can be asserted
// on the exact bytes the read loop handed over.
func recordingTranslator() Translator {
	return func(raw []byte) ([]contracts.Event, error) {
		return []contracts.Event{{
			Type:    "test.record",
			Payload: append(json.RawMessage(nil), raw...),
		}}, nil
	}
}

// TestReadLoopFraming pins the framing rules. Every case feeds raw bytes at the
// peer end and asserts which records the bridge saw, because getting this wrong
// silently corrupts message text.
func TestReadLoopFraming(t *testing.T) {
	// U+2028 and U+2029 are legal inside JSON strings; a reader that treats
	// them as line terminators (Node's readline does) splits a record in two.
	const u2028 = "\u2028"
	const u2029 = "\u2029"

	tests := []struct {
		name       string
		input      string
		want       []string
		wantErrors int64
	}{
		{
			name:  "splits on LF only",
			input: "{\"type\":\"a\"}\n{\"type\":\"b\"}\n",
			want:  []string{"{\"type\":\"a\"}", "{\"type\":\"b\"}"},
		},
		{
			name:  "trims one carriage return",
			input: "{\"type\":\"a\"}\r\n",
			want:  []string{"{\"type\":\"a\"}"},
		},
		{
			name:  "keeps U+2028 inside a JSON string",
			input: `{"type":"a","text":"x` + u2028 + `y"}` + "\n",
			want:  []string{`{"type":"a","text":"x` + u2028 + `y"}`},
		},
		{
			name:  "keeps U+2029 inside a JSON string",
			input: `{"type":"a","text":"x` + u2029 + `y"}` + "\n",
			want:  []string{`{"type":"a","text":"x` + u2029 + `y"}`},
		},
		{
			name:  "skips empty lines",
			input: "\n\n{\"type\":\"a\"}\n\n\n",
			want:  []string{"{\"type\":\"a\"}"},
		},
		{
			name:       "counts a malformed record and keeps going",
			input:      "not json\n{\"type\":\"a\"}\n",
			want:       []string{"{\"type\":\"a\"}"},
			wantErrors: 1,
		},
		{
			name:  "accepts a final record without a newline",
			input: "{\"type\":\"a\"}",
			want:  []string{"{\"type\":\"a\"}"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ours, theirs := pitest.NewPipe()
			cfg := peerConfig()
			cfg.Translator = recordingTranslator()
			b := NewStream(ours, cfg)
			defer func() { _ = b.Close() }()
			sub := b.Subscribe()

			if _, err := io.WriteString(theirs, tc.input); err != nil {
				t.Fatalf("write to peer end: %v", err)
			}
			// Closing the peer end EOFs the bridge's read loop, which is the
			// real "the process exited" trigger.
			_ = theirs.Close()

			var got []string
			deadline := time.After(testTimeout)
			for len(got) < len(tc.want) {
				select {
				case ev, ok := <-sub.C:
					if !ok {
						t.Fatalf("channel closed after %d records, want %d", len(got), len(tc.want))
					}
					got = append(got, string(ev.Payload))
				case <-deadline:
					t.Fatalf("got %d records, want %d: %q", len(got), len(tc.want), got)
				}
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("record %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
			if n := b.MalformedRecords(); n != tc.wantErrors {
				t.Errorf("malformed records = %d, want %d", n, tc.wantErrors)
			}
		})
	}
}

// TestCallCorrelation sends two commands at once and answers them in reverse
// order: Pi is asynchronous, so correlation must be by id, never by order.
func TestCallCorrelation(t *testing.T) {
	ours, theirs := pitest.NewPipe()
	b := NewStream(ours, peerConfig())
	defer func() { _ = b.Close() }()

	const peers = 2
	go func() {
		br := bufio.NewReader(theirs)
		type wire struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			N    int    `json:"n"`
		}
		got := make([]wire, 0, peers)
		for len(got) < peers {
			line, err := br.ReadBytes('\n')
			if err != nil {
				return
			}
			var w wire
			if json.Unmarshal(bytes.TrimSpace(line), &w) != nil {
				continue
			}
			got = append(got, w)
		}
		for i := len(got) - 1; i >= 0; i-- {
			resp, _ := json.Marshal(map[string]any{
				"type": "response", "id": got[i].ID, "success": true,
				"data": map[string]any{"echo": got[i].N * 10},
			})
			if _, err := theirs.Write(append(resp, '\n')); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	type result struct {
		echo int
		err  error
	}
	results := make(chan result, peers)
	for i := 0; i < peers; i++ {
		go func(n int) {
			var out struct {
				Echo int `json:"echo"`
			}
			err := b.CallInto(ctx, "probe", map[string]any{"n": n}, &out)
			if err != nil {
				results <- result{err: err}
				return
			}
			results <- result{echo: out.Echo}
		}(i)
	}
	seen := map[int]bool{}
	for i := 0; i < peers; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("call: %v", r.err)
			}
			seen[r.echo] = true
		case <-ctx.Done():
			t.Fatalf("calls did not complete: %v", ctx.Err())
		}
	}
	for _, want := range []int{0, 10} {
		if !seen[want] {
			t.Errorf("missing response %d; got %v", want, seen)
		}
	}
}

// TestCallFailedResponse checks that success:false surfaces as a CommandError
// carrying Pi's own message.
func TestCallFailedResponse(t *testing.T) {
	ours, theirs := pitest.NewPipe()
	b := NewStream(ours, peerConfig())
	defer func() { _ = b.Close() }()

	go func() {
		br := bufio.NewReader(theirs)
		line, err := br.ReadBytes('\n')
		if err != nil {
			return
		}
		var w struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(bytes.TrimSpace(line), &w)
		resp, _ := json.Marshal(map[string]any{
			"type": "response", "id": w.ID, "success": false,
			"error": "agent is streaming",
		})
		_, _ = theirs.Write(append(resp, '\n'))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	_, err := b.Call(ctx, "prompt", map[string]any{"message": "hi"})
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("error = %v, want *CommandError", err)
	}
	if !strings.Contains(cmdErr.Message, "agent is streaming") {
		t.Errorf("CommandError message = %q", cmdErr.Message)
	}
}

// TestCallAfterClose verifies a call on a dead bridge returns ErrClosed instead
// of hanging: an interrupted run must surface, not stall the HTTP handler.
func TestCallAfterClose(t *testing.T) {
	ours, theirs := pitest.NewPipe()
	b := NewStream(ours, peerConfig())
	_ = theirs.Close()
	<-b.Done()
	if err := b.Close(); err != nil {
		t.Logf("Close: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := b.Call(ctx, "get_state", nil)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("Call after close error = %v, want ErrClosed", err)
	}
}

// TestBackpressureSlowSubscriber proves the read loop never blocks on a
// consumer: one subscriber never reads, cosmetic events are dropped, and
// run.settled still arrives.
func TestBackpressureSlowSubscriber(t *testing.T) {
	cfg := peerConfig()
	cfg.SubscriberBuffer = 2
	cfg.MaxBacklog = 8
	b := startPeerBridge(t, pitest.ScriptOK, time.Millisecond, "", cfg)

	fast := b.Subscribe()
	slow := b.Subscribe() // deliberately never read until the run is over

	var mu sync.Mutex
	var fastEvents []contracts.Event
	fastDone := make(chan struct{})
	go func() {
		defer close(fastDone)
		for ev := range fast.C {
			mu.Lock()
			fastEvents = append(fastEvents, ev)
			mu.Unlock()
			if ev.Type == contracts.EvRunSettled {
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if _, err := b.Call(ctx, "prompt", map[string]any{"message": "go"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	select {
	case <-fastDone:
	case <-time.After(testTimeout):
		t.Fatalf("the reading subscriber never saw run.settled")
	}
	mu.Lock()
	reading := len(fastEvents)
	mu.Unlock()
	if reading == 0 {
		t.Fatal("reading subscriber saw no events")
	}

	// Now drain the slow subscriber; the run must already be finished, and the
	// terminal event must have survived the backlog squeeze.
	var slowEvents []contracts.Event
	timeout := time.After(testTimeout)
	for {
		select {
		case ev, ok := <-slow.C:
			if !ok {
				t.Fatal("slow subscriber channel closed unexpectedly")
			}
			slowEvents = append(slowEvents, ev)
			if ev.Type == contracts.EvRunSettled {
				goto done
			}
		case <-timeout:
			t.Fatalf("slow subscriber never received run.settled; got %v", typesOf(slowEvents))
		}
	}
done:
	if slow.Dropped() == 0 {
		t.Errorf("expected cosmetic events to be dropped for a stalled subscriber")
	}
	if indexOfType(slowEvents, contracts.EvMsgFinal) < 0 {
		t.Errorf("message.final was dropped, but it must be retained: %v", typesOf(slowEvents))
	}
}

// TestSubscriptionClosesChannel checks consumers can range over the channel and
// stop without a sentinel value once the bridge is closed.
func TestSubscriptionClosesChannel(t *testing.T) {
	ours, theirs := pitest.NewPipe()
	b := NewStream(ours, peerConfig())
	sub := b.Subscribe()
	_ = theirs.Close()
	select {
	case <-b.Done():
	case <-time.After(testTimeout):
		t.Fatal("bridge did not notice the peer going away")
	}
	_ = b.Close()

	deadline := time.After(testTimeout)
	for {
		select {
		case _, ok := <-sub.C:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("subscription did not close after Close")
		}
	}
}

// TestCloseIsIdempotent guards against double-close panics in shutdown paths.
func TestCloseIsIdempotent(t *testing.T) {
	ours, theirs := pitest.NewPipe()
	b := NewStream(ours, peerConfig())
	_ = theirs.Close()
	for i := 0; i < 3; i++ {
		if err := b.Close(); err != nil {
			t.Logf("Close %d: %v", i, err)
		}
	}
}

// TestStartRequiresBinary keeps a misconfiguration from surfacing as a nil
// pointer panic deeper in the spawn path.
func TestStartRequiresBinary(t *testing.T) {
	b := NewBridge(Config{})
	if err := b.Start(context.Background()); err == nil {
		t.Fatal("Start with no BinaryPath must fail")
	}
}
