package pi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// translateRaw feeds one JSONL record to a normalizer and returns the events.
func translateRaw(t *testing.T, n *Normalizer, raw string) []contracts.Event {
	t.Helper()
	events, err := n.Translate([]byte(raw))
	if err != nil {
		t.Fatalf("Translate(%s): %v", raw, err)
	}
	return events
}

// TestNormalizeAgentEndIsNotCompletion is the single most expensive mistake in
// this bridge: agent_end also fires before retries, compaction and steering, so
// only agent_settled may be treated as "the run is over".
func TestNormalizeAgentEndIsNotCompletion(t *testing.T) {
	n := NewNormalizer("ses_1", contracts.KindMain)
	translateRaw(t, n, `{"type":"agent_start"}`)

	events := translateRaw(t, n, `{"type":"agent_end","messages":[],"willRetry":true}`)
	for _, ev := range events {
		if ev.Type == contracts.EvRunSettled {
			t.Fatalf("agent_end produced run.settled; the agent may still be retrying")
		}
	}
	if state := n.State(); !state.Running {
		t.Fatalf("agent_end cleared the running flag; only agent_settled may do that")
	}

	events = translateRaw(t, n, `{"type":"agent_settled","aborted":false}`)
	if got := typesOf(events); len(got) != 1 || got[0] != contracts.EvRunSettled {
		t.Fatalf("agent_settled produced %v, want [run.settled]", got)
	}
	if state := n.State(); state.Running || state.Streaming || state.RunID != "" {
		t.Fatalf("state after settle = %+v", state)
	}
}

// TestNormalizeSeqAndEnvelope pins the envelope: seq starts at 1 and never
// skips (it is the SSE id), branch_id is the session id, kind comes from the
// spec, and ts is control-plane time in UTC.
func TestNormalizeSeqAndEnvelope(t *testing.T) {
	n := NewNormalizer("ses_abc", contracts.KindSidecar)
	records := []string{
		`{"type":"agent_start"}`,
		`{"type":"turn_start"}`,
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"hi"}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"turn_end"}`,
		`{"type":"agent_settled","aborted":false}`,
	}
	var all []contracts.Event
	for _, raw := range records {
		all = append(all, translateRaw(t, n, raw)...)
	}
	assertSeqContiguous(t, all)

	before := time.Now().Add(-time.Minute)
	for i, ev := range all {
		if ev.SessionID != "ses_abc" {
			t.Errorf("event %d session_id = %q", i, ev.SessionID)
		}
		if ev.BranchID != "ses_abc" {
			t.Errorf("event %d branch_id = %q, want the session id", i, ev.BranchID)
		}
		if ev.Kind != contracts.KindSidecar {
			t.Errorf("event %d kind = %q", i, ev.Kind)
		}
		if ev.TS.Before(before) || ev.TS.After(time.Now().Add(time.Minute)) {
			t.Errorf("event %d ts = %v, want control-plane time", i, ev.TS)
		}
		if ev.TS.Location() != time.UTC {
			t.Errorf("event %d ts location = %v, want UTC", i, ev.TS.Location())
		}
	}
	// RunID is present for everything inside the run and cleared afterwards.
	for _, ev := range all[:len(all)-1] {
		if ev.RunID == "" {
			t.Errorf("%s has no run_id", ev.Type)
		}
	}
	if all[len(all)-1].Type != contracts.EvRunSettled {
		t.Fatalf("last event = %s", all[len(all)-1].Type)
	}
	if last := all[len(all)-1]; last.RunID == "" {
		t.Errorf("run.settled must carry the run id it terminates")
	}
}

// TestNormalizeEventMapping is the mapping table from PRD §7.4.4.
func TestNormalizeEventMapping(t *testing.T) {
	tests := []struct {
		name  string
		setup []string
		raw   string
		want  []string
	}{
		{
			name: "agent_start",
			raw:  `{"type":"agent_start"}`,
			want: []string{contracts.EvRunStarted},
		},
		{
			name: "turn_start",
			raw:  `{"type":"turn_start"}`,
			want: []string{contracts.EvTurnStarted},
		},
		{
			name: "message_start",
			raw:  `{"type":"message_start","message":{"role":"assistant","content":[]}}`,
			want: []string{contracts.EvMsgStarted},
		},
		{
			name:  "text delta",
			setup: []string{`{"type":"agent_start"}`},
			raw:   `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"a"}}`,
			want:  []string{contracts.EvMsgDelta},
		},
		{
			name: "message_end",
			raw:  `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"x"}],"usage":{"input":1,"output":2}}}`,
			want: []string{contracts.EvMsgFinal, contracts.EvContextUsage},
		},
		{
			name: "tool start",
			raw:  `{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":"ls"}}`,
			want: []string{contracts.EvToolStarted},
		},
		{
			name: "tool update",
			raw:  `{"type":"tool_execution_update","toolCallId":"c1","toolName":"bash","partialResult":{"content":[{"type":"text","text":"half"}]}}`,
			want: []string{contracts.EvToolProgress},
		},
		{
			name: "tool end",
			raw:  `{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":{"content":[{"type":"text","text":"done"}]},"durationMs":3}`,
			want: []string{contracts.EvToolEnded},
		},
		{
			name: "queue update",
			raw:  `{"type":"queue_update","steering":["a"],"followUp":["b","c"]}`,
			want: []string{contracts.EvQueueChanged},
		},
		{
			name: "compaction start",
			raw:  `{"type":"compaction_start","reason":"manual"}`,
			want: []string{contracts.EvCompaction},
		},
		{
			name: "compaction end",
			raw:  `{"type":"compaction_end","reason":"manual","result":{"summary":"s","tokensBefore":10,"estimatedTokensAfter":2,"usage":{"input":1}}}`,
			want: []string{contracts.EvCompaction, contracts.EvContextUsage},
		},
		{
			name: "retry",
			raw:  `{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"overloaded"}`,
			want: []string{contracts.EvRunRetry},
		},
		{
			name: "extension error",
			raw:  `{"type":"extension_error","extensionPath":"/x.js","error":"boom"}`,
			want: []string{contracts.EvPlatformErr},
		},
		{
			name: "unknown type is ignored",
			raw:  `{"type":"something_new_from_a_future_pi"}`,
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := NewNormalizer("ses", contracts.KindMain)
			for _, raw := range tc.setup {
				translateRaw(t, n, raw)
			}
			got := typesOf(translateRaw(t, n, tc.raw))
			if len(got) != len(tc.want) {
				t.Fatalf("events = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("events = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestNormalizeAbortedRun checks the interrupt shape the UI depends on:
// run.aborted first, then run.settled{aborted:true}.
func TestNormalizeAbortedRun(t *testing.T) {
	n := NewNormalizer("ses", contracts.KindMain)
	translateRaw(t, n, `{"type":"agent_start"}`)
	events := translateRaw(t, n, `{"type":"agent_settled","aborted":true}`)
	got := typesOf(events)
	if len(got) != 2 || got[0] != contracts.EvRunAborted || got[1] != contracts.EvRunSettled {
		t.Fatalf("events = %v, want [run.aborted run.settled]", got)
	}
	var payload SettledPayload
	decodePayload(t, events[1], &payload)
	if !payload.Aborted {
		t.Errorf("run.settled aborted = false, want true")
	}
}

// TestNormalizeMessageFinalPayload checks that the final payload carries the
// assembled blocks, the ordered tool results and the truncation flag.
func TestNormalizeMessageFinalPayload(t *testing.T) {
	n := NewNormalizer("ses", contracts.KindMain)
	records := []string{
		`{"type":"agent_start"}`,
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_start","contentIndex":0}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"hmm"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_end","contentIndex":0,"content":"hmm"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":1}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"ok"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_end","contentIndex":1,"content":"ok"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_start","contentIndex":2,"id":"c1","toolName":"bash"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_delta","contentIndex":2,"delta":"{\"command\":\"ls\"}"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":2,"toolCall":{"id":"c1","name":"bash","arguments":{"command":"ls"}}}}`,
		`{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":"ls"}}`,
		`{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":{"content":[{"type":"text","text":"out"}]},"durationMs":7}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"ok"},{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"ls"}}],"stopReason":"toolUse","usage":{"input":5,"output":6}}}`,
	}
	var final contracts.Event
	for _, raw := range records {
		for _, ev := range translateRaw(t, n, raw) {
			if ev.Type == contracts.EvMsgFinal {
				final = ev
			}
		}
	}
	var payload MessageFinalPayload
	decodePayload(t, final, &payload)
	if payload.Role != "assistant" {
		t.Errorf("role = %q", payload.Role)
	}
	if payload.Text != "ok" || payload.Thinking != "hmm" {
		t.Errorf("text = %q thinking = %q", payload.Text, payload.Thinking)
	}
	if payload.StopReason != "toolUse" {
		t.Errorf("stop_reason = %q", payload.StopReason)
	}
	if len(payload.Blocks) != 3 {
		t.Fatalf("blocks = %+v", payload.Blocks)
	}
	if payload.Blocks[2].Tool == nil || !payload.Blocks[2].Tool.Valid {
		t.Errorf("tool block = %+v", payload.Blocks[2])
	}
	if len(payload.ToolResults) != 1 || payload.ToolResults[0].Text != "out" {
		t.Errorf("tool_results = %+v", payload.ToolResults)
	}
	if payload.Truncated {
		t.Errorf("a complete message must not be flagged truncated")
	}
	if payload.Usage == nil || payload.Usage.Input != 5 {
		t.Errorf("usage = %+v", payload.Usage)
	}
}

// TestNormalizeMalformedRecord keeps one bad line from taking the session down.
func TestNormalizeMalformedRecord(t *testing.T) {
	n := NewNormalizer("ses", contracts.KindMain)
	if _, err := n.Translate([]byte("}{")); err == nil {
		t.Fatal("want an error for unparsable JSON")
	}
	if _, err := n.Translate([]byte(`{"type":"message_end","message":"not an object"}`)); err == nil {
		t.Fatal("want an error for a malformed message_end")
	}
}

// TestNormalizeReservedRun checks the run id handshake between Prompt and the
// event stream.
func TestNormalizeReservedRun(t *testing.T) {
	n := NewNormalizer("ses", contracts.KindMain)
	reserved := n.ReserveRun()
	if reserved == "" {
		t.Fatal("ReserveRun returned an empty id")
	}
	events := translateRaw(t, n, `{"type":"agent_start"}`)
	if events[0].RunID != reserved {
		t.Fatalf("run.started run_id = %q, want the reserved %q", events[0].RunID, reserved)
	}

	// A withdrawn reservation must not leak into a later run.
	n2 := NewNormalizer("ses2", contracts.KindMain)
	stale := n2.ReserveRun()
	n2.ClearReservedRun(stale)
	events = translateRaw(t, n2, `{"type":"agent_start"}`)
	if events[0].RunID == stale {
		t.Fatalf("a cleared reservation was reused")
	}
}

// TestNormalizeContextUsagePercent checks the meter maths when the window is
// known.
func TestNormalizeContextUsagePercent(t *testing.T) {
	n := NewNormalizer("ses", contracts.KindMain)
	n.SetContextWindow(1000)
	events := translateRaw(t, n, `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"x"}],"usage":{"input":400,"output":100}}}`)
	var usage contracts.Event
	for _, ev := range events {
		if ev.Type == contracts.EvContextUsage {
			usage = ev
		}
	}
	var payload ContextUsagePayload
	decodePayload(t, usage, &payload)
	if payload.ContextWindow != 1000 {
		t.Errorf("context_window = %d", payload.ContextWindow)
	}
	if payload.Percent != 50 {
		t.Errorf("percent = %v, want 50", payload.Percent)
	}
}

// TestNormalizeNoPanicOnWeirdPayloads is the "no panics on malformed input"
// guarantee, exercised with shapes Pi could plausibly send.
func TestNormalizeNoPanicOnWeirdPayloads(t *testing.T) {
	cases := []string{
		`{"type":"message_update"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":9}}`,
		`{"type":"message_end","message":null}`,
		`{"type":"message_end","message":{"role":"assistant","content":null}}`,
		`{"type":"tool_execution_end"}`,
		`{"type":"tool_execution_update","partialResult":null}`,
		`{"type":"compaction_end"}`,
		`{"type":"agent_settled"}`,
		`{"type":"queue_update","steering":null,"followUp":null}`,
		`{"type":"extension_error"}`,
		`{"type":""}`,
	}
	for _, raw := range cases {
		n := NewNormalizer("ses", contracts.KindMain)
		if _, err := n.Translate([]byte(raw)); err != nil {
			t.Errorf("Translate(%s) returned an unexpected error: %v", raw, err)
		}
	}
}

// TestNormalizeQueueDepth checks that the composer's queue depth tracks
// queue_update.
func TestNormalizeQueueDepth(t *testing.T) {
	n := NewNormalizer("ses", contracts.KindMain)
	translateRaw(t, n, `{"type":"queue_update","steering":["a"],"followUp":["b","c"]}`)
	if got := n.State().QueueDepth; got != 3 {
		t.Fatalf("queue depth = %d, want 3", got)
	}
}

// TestNormalizeTurnCounter checks turn numbering restarts on each run.
func TestNormalizeTurnCounter(t *testing.T) {
	n := NewNormalizer("ses", contracts.KindMain)
	translateRaw(t, n, `{"type":"agent_start"}`)
	translateRaw(t, n, `{"type":"turn_start"}`)
	translateRaw(t, n, `{"type":"turn_start"}`)
	if got := n.State().Turn; got != 2 {
		t.Fatalf("turn = %d, want 2", got)
	}
	translateRaw(t, n, `{"type":"agent_settled","aborted":false}`)
	translateRaw(t, n, `{"type":"agent_start"}`)
	if got := n.State().Turn; got != 0 {
		t.Fatalf("turn after a new run = %d, want 0", got)
	}
}

// TestTranslatePayloadIsValidJSON keeps the payload contract honest for the
// frontend, which parses every payload.
func TestTranslatePayloadIsValidJSON(t *testing.T) {
	n := NewNormalizer("ses", contracts.KindMain)
	records := []string{
		`{"type":"agent_start"}`,
		`{"type":"message_start","message":{"role":"user","content":"hi"}}`,
		`{"type":"message_end","message":{"role":"user","content":"hi"}}`,
	}
	for _, raw := range records {
		for _, ev := range translateRaw(t, n, raw) {
			if len(ev.Payload) == 0 {
				continue
			}
			if !json.Valid(ev.Payload) {
				t.Errorf("%s payload is not valid JSON: %s", ev.Type, ev.Payload)
			}
		}
	}
}

// TestMessageEndCarriesTheProviderError pins the difference between "the model
// said nothing" and "the model could not be reached": both arrive as an assistant
// message with no content, and only the error explains which one happened.
func TestMessageEndCarriesTheProviderError(t *testing.T) {
	n := NewNormalizer("ses_err", contracts.KindMain)
	raw := []byte(`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"502: provider \"volc\" is not available","provider":"piforge","model":"m"}}`)

	events, err := n.Translate(raw)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	// A failed call closes the turn as well as reporting the failure, so the
	// final message is the last event rather than the only one.
	var payload MessageFinalPayload
	found := false
	for _, event := range events {
		if event.Type != contracts.EvMsgFinal {
			continue
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		found = true
	}
	if !found {
		t.Fatalf("no message.final in %d events", len(events))
	}
	if payload.StopReason != "error" {
		t.Fatalf("stop_reason = %q", payload.StopReason)
	}
	if payload.Error == "" {
		t.Fatalf("the provider error was dropped: %+v", payload)
	}
	if !strings.Contains(payload.Error, "not available") {
		t.Fatalf("error = %q", payload.Error)
	}
}

// TestInterruptedRunReportsNoFailure pins the shape real Pi produced when a user
// stopped a run: the assistant message is a *provider error* ("This operation was
// aborted", stopReason "error"), and only agent_settled.aborted says it was a
// stop. Forwarding the error made every Stop render as 模型调用失败 with advice to
// check the provider's key (QA-14, MAJOR).
func TestInterruptedRunReportsNoFailure(t *testing.T) {
	n := NewNormalizer("ses_stop", contracts.KindMain)
	if _, err := n.Translate([]byte(`{"type":"agent_start"}`)); err != nil {
		t.Fatalf("agent_start: %v", err)
	}
	// The user presses Stop while a tool runs.
	n.NoteInterrupt()

	events, err := n.Translate([]byte(`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"This operation was aborted","provider":"piforge","model":"m"}}`))
	if err != nil {
		t.Fatalf("message_end: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type != contracts.EvMsgFinal {
			continue
		}
		var payload MessageFinalPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		found = true
		if payload.Error != "" {
			t.Fatalf("a stopped run carried a failure: %q", payload.Error)
		}
	}
	if !found {
		t.Fatal("no message.final")
	}

	// The next run is a fresh one: a genuine failure in it is still reported.
	if _, err := n.Translate([]byte(`{"type":"agent_settled","aborted":true}`)); err != nil {
		t.Fatalf("agent_settled: %v", err)
	}
	if _, err := n.Translate([]byte(`{"type":"agent_start"}`)); err != nil {
		t.Fatalf("agent_start 2: %v", err)
	}
	events, err = n.Translate([]byte(`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"401: invalid api key"}}`))
	if err != nil {
		t.Fatalf("message_end 2: %v", err)
	}
	for _, event := range events {
		if event.Type != contracts.EvMsgFinal {
			continue
		}
		var payload MessageFinalPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if !strings.Contains(payload.Error, "invalid api key") {
			t.Fatalf("a real credential failure was swallowed: %+v", payload)
		}
	}

	// And a message that says stopReason "aborted" is not a failure either.
	n2 := NewNormalizer("ses_stop2", contracts.KindMain)
	events, err = n2.Translate([]byte(`{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"aborted","errorMessage":"aborted"}}`))
	if err != nil {
		t.Fatalf("aborted message_end: %v", err)
	}
	for _, event := range events {
		if event.Type != contracts.EvMsgFinal {
			continue
		}
		var payload MessageFinalPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.Error != "" {
			t.Fatalf("stopReason aborted carried a failure: %q", payload.Error)
		}
	}
}

// The audit trail stores the process status when Pi reports one, and NULL when
// it does not. "Exited 0" and "not reported" must not collapse into the same
// value, which is why the payload field is a pointer.
func TestToolEndedCarriesExitCodeFromDetails(t *testing.T) {
	n := NewNormalizer("ses_exit", contracts.KindMain)
	translateRaw(t, n, `{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":"exit 7"}}`)

	events := translateRaw(t, n, `{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","isError":true,"durationMs":9,"result":{"content":[{"type":"text","text":"boom"}],"details":{"exitCode":7}}}`)
	var ended *contracts.Event
	for i := range events {
		if events[i].Type == contracts.EvToolEnded {
			ended = &events[i]
		}
	}
	if ended == nil {
		t.Fatalf("no tool.ended in %v", typesOf(events))
	}
	var payload ToolEndedPayload
	if err := json.Unmarshal(ended.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.ExitCode == nil || *payload.ExitCode != 7 {
		t.Fatalf("exit_code = %v, want 7", payload.ExitCode)
	}

	// A tool with no details reports no code; the pointer stays nil.
	n2 := NewNormalizer("ses_exit2", contracts.KindMain)
	translateRaw(t, n2, `{"type":"tool_execution_start","toolCallId":"c2","toolName":"read","args":{"path":"a"}}`)
	events = translateRaw(t, n2, `{"type":"tool_execution_end","toolCallId":"c2","toolName":"read","result":{"content":[{"type":"text","text":"ok"}]}}`)
	for _, ev := range events {
		if ev.Type != contracts.EvToolEnded {
			continue
		}
		payload = ToolEndedPayload{}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if payload.ExitCode != nil {
			t.Fatalf("a tool with no details reported exit_code=%d", *payload.ExitCode)
		}
	}
}
