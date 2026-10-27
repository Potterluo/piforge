package pi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/pitest"
)

// testTimeout bounds every wait in the suite so a deadlock fails fast instead
// of hanging the run.
const testTimeout = 10 * time.Second

// peerConfig is the bridge configuration used against an in-process pitest
// peer: small buffers keep the drop policy exercised and a short close timeout
// keeps failures quick.
func peerConfig() Config {
	return Config{
		CloseTimeout:     time.Second,
		SubscriberBuffer: 64,
		MaxBacklog:       256,
	}
}

// startPeerBridge wires a Bridge to a pitest peer over io.Pipe. Unless the
// caller supplied a translator, a fresh Normalizer is installed so the test
// sees real contracts.Event types. The caller is responsible for closing the
// bridge.
func startPeerBridge(t *testing.T, script string, delay time.Duration, sessionFile string, cfg Config) *Bridge {
	t.Helper()
	if cfg.Translator == nil {
		norm := NewNormalizer("ses_test", contracts.KindMain)
		cfg.Translator = norm.Translate
	}
	ours, theirs := pitest.NewPipe()
	b := NewStream(ours, cfg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer theirs.Close()
		if err := pitest.Serve(script, delay, sessionFile, theirs, theirs); err != nil {
			// The peer reports write failures after the bridge closes; that is
			// expected teardown noise, not a test failure.
			t.Logf("pitest.Serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		_ = b.Close()
		select {
		case <-done:
		case <-time.After(testTimeout):
			t.Errorf("pitest peer did not shut down")
		}
	})
	return b
}

// collectUntil drains events until match reports true, returning everything it
// saw. The matched event is included.
func collectUntil(t *testing.T, ch <-chan contracts.Event, match func(contracts.Event) bool) []contracts.Event {
	t.Helper()
	var out []contracts.Event
	deadline := time.After(testTimeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("event channel closed before the expected event arrived; got %d events", len(out))
			}
			out = append(out, ev)
			if match(ev) {
				return out
			}
		case <-deadline:
			t.Fatalf("timed out after %d events waiting for the expected one", len(out))
		}
	}
}

// typesOf projects the event types in order.
func typesOf(events []contracts.Event) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = ev.Type
	}
	return out
}

// indexOfType returns the index of the first event of the given type, or -1.
func indexOfType(events []contracts.Event, want string) int {
	for i, ev := range events {
		if ev.Type == want {
			return i
		}
	}
	return -1
}

// assertSubsequence fails when want does not appear in order inside have.
func assertSubsequence(t *testing.T, have []contracts.Event, want ...string) {
	t.Helper()
	i := 0
	for _, ev := range have {
		if i < len(want) && ev.Type == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("event order %v does not contain subsequence %v", typesOf(have), want)
	}
}

// assertSeqContiguous fails when seq is not 1,2,3,… across the slice.
func assertSeqContiguous(t *testing.T, events []contracts.Event) {
	t.Helper()
	for i, ev := range events {
		want := int64(i + 1)
		if ev.Seq != want {
			t.Fatalf("event %d (%s) has seq %d, want %d (gaps break SSE resume)", i, ev.Type, ev.Seq, want)
		}
	}
}

// decodePayload unmarshals an event payload into out.
func decodePayload(t *testing.T, ev contracts.Event, out any) {
	t.Helper()
	if len(ev.Payload) == 0 {
		t.Fatalf("%s has no payload", ev.Type)
	}
	if err := json.Unmarshal(ev.Payload, out); err != nil {
		t.Fatalf("%s payload: %v", ev.Type, err)
	}
}

// decodePayloadValue is decodePayload without the *testing.T dependency.
func decodePayloadValue(ev contracts.Event, out any) error {
	return json.Unmarshal(ev.Payload, out)
}

// lastFinalOfRole returns the last message.final for the given role.
func lastFinalOfRole(t *testing.T, events []contracts.Event, role string) (MessageFinalPayload, bool) {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != contracts.EvMsgFinal {
			continue
		}
		var payload MessageFinalPayload
		if err := decodePayloadValue(events[i], &payload); err != nil {
			t.Fatalf("decode message.final: %v", err)
		}
		if payload.Role == role {
			return payload, true
		}
	}
	return MessageFinalPayload{}, false
}
