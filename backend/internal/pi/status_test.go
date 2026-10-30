package pi

import (
	"bufio"
	"encoding/json"
	"io"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/pitest"
)

// laggingPeer rewrites the peer's get_state reply so it keeps claiming that the
// agent is streaming, even after agent_settled has been emitted.
//
// This is not a contrivance: a process clears its own isStreaming flag after it
// announces the settle, and get_state is answered by whichever goroutine is
// reading commands. The interval is short but real, and a status call that lands
// in it used to copy the stale value over the settled event-derived state.
type laggingPeer struct {
	io.ReadWriteCloser
	out *io.PipeReader
}

func (p *laggingPeer) Read(b []byte) (int, error) { return p.out.Read(b) }

// laggingStreamingFlag wraps the bridge end of the pipe and marks every
// get_state response as still streaming.
func laggingStreamingFlag(conn io.ReadWriteCloser) io.ReadWriteCloser {
	out, writer := io.Pipe()
	go func() {
		defer writer.Close()
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			var record map[string]any
			if json.Unmarshal(line, &record) == nil && record["type"] == "response" {
				if data, ok := record["data"].(map[string]any); ok {
					if _, has := data["isStreaming"]; has {
						data["isStreaming"] = true
						if encoded, err := json.Marshal(record); err == nil {
							line = encoded
						}
					}
				}
			}
			if _, err := writer.Write(append(line, '\n')); err != nil {
				return
			}
		}
	}()
	return &laggingPeer{ReadWriteCloser: conn, out: out}
}

// TestRuntimeStatusAfterSettleDoesNotTrustALaggingPeer pins the API contract the
// UI depends on: run.settled means the run is over, so a status read taken after
// it must not say Streaming just because the peer has not cleared its own flag
// yet. The old code copied get_state over the event-derived flags and answered
// Running:false/Streaming:true, which is both self-contradictory and enough to
// leave the composer thinking the agent is busy.
func TestRuntimeStatusAfterSettleDoesNotTrustALaggingPeer(t *testing.T) {
	rt := newFakeRuntimeWith(t, pitest.ScriptOK, 0, laggingStreamingFlag)
	ctx, cancel := ctxFor(t)
	defer cancel()

	spec := sessionSpec(t, "ses_lag", contracts.KindMain)
	ch, err := rt.Start(ctx, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := rt.Prompt(ctx, contracts.PromptRequest{SessionID: "ses_lag", Text: "why?"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	collectUntil(t, ch, settledMatcher)

	status, err := rt.Status(ctx, "ses_lag")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Running || status.Streaming {
		t.Fatalf("status after settle = %+v, want the settled state even though the peer's "+
			"get_state still says isStreaming", status)
	}
}
