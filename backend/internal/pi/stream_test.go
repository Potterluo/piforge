package pi

import (
	"encoding/json"
	"testing"
)

// feed applies a sequence of assistant events to a fresh assembler.
func feed(script ...assistantEvent) *StreamAssembler {
	s := NewStreamAssembler()
	s.OnMessageStart(messageWire{})
	for _, ev := range script {
		s.OnUpdate(ev, nil)
	}
	return s
}

func textStart(i int) assistantEvent {
	return assistantEvent{Type: "text_start", ContentIndex: i}
}
func textDelta(i int, d string) assistantEvent {
	return assistantEvent{Type: "text_delta", ContentIndex: i, Delta: d}
}
func textEnd(i int, content string) assistantEvent {
	return assistantEvent{Type: "text_end", ContentIndex: i, Content: content}
}
func thinkStart(i int) assistantEvent {
	return assistantEvent{Type: "thinking_start", ContentIndex: i}
}
func thinkDelta(i int, d string) assistantEvent {
	return assistantEvent{Type: "thinking_delta", ContentIndex: i, Delta: d}
}
func thinkEnd(i int, content string) assistantEvent {
	return assistantEvent{Type: "thinking_end", ContentIndex: i, Content: content}
}
func callStart(i int, id, name string) assistantEvent {
	return assistantEvent{Type: "toolcall_start", ContentIndex: i, ID: id, ToolName: name}
}
func callDelta(i int, d string) assistantEvent {
	return assistantEvent{Type: "toolcall_delta", ContentIndex: i, Delta: d}
}
func callEnd(i int, id, name, args string) assistantEvent {
	return assistantEvent{
		Type: "toolcall_end", ContentIndex: i,
		ToolCall: toolCall{ID: id, Name: name, Arguments: json.RawMessage(args)},
	}
}

// TestStreamInterleavedThinkingAndText rebuilds a message whose blocks end and
// start out of order, which is what a real turn looks like.
func TestStreamInterleavedThinkingAndText(t *testing.T) {
	s := feed(
		thinkStart(0), thinkDelta(0, "Let me "), thinkDelta(0, "look."),
		thinkEnd(0, "Let me look."),
		textStart(1), textDelta(1, "I'll "), textDelta(1, "read."),
		textEnd(1, "I'll read."),
	)
	blocks := s.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2: %+v", len(blocks), blocks)
	}
	if blocks[0].Kind != BlockThinking || blocks[0].Text != "Let me look." {
		t.Errorf("block 0 = %+v", blocks[0])
	}
	if blocks[1].Kind != BlockText || blocks[1].Text != "I'll read." {
		t.Errorf("block 1 = %+v", blocks[1])
	}
	if got := s.Text(); got != "I'll read." {
		t.Errorf("Text() = %q", got)
	}
	if got := s.Thinking(); got != "Let me look." {
		t.Errorf("Thinking() = %q", got)
	}
}

// TestStreamEndReplacesAccumulatedText pins the rule from PRD §7.4.3: the
// authoritative content replaces whatever the deltas produced, it is never
// appended to them.
func TestStreamEndReplacesAccumulatedText(t *testing.T) {
	s := feed(
		textStart(0), textDelta(0, "WRONG"), textDelta(0, "WRONG"),
		textEnd(0, "right"),
	)
	blocks := s.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if blocks[0].Text != "right" {
		t.Fatalf("text = %q, want the authoritative %q", blocks[0].Text, "right")
	}
}

// TestStreamMessageEndReplacesWholeMessage checks that message_end overrides
// even a complete-looking delta assembly.
func TestStreamMessageEndReplacesWholeMessage(t *testing.T) {
	s := feed(textStart(0), textDelta(0, "draft"), textEnd(0, "draft"))
	blocks := s.OnMessageEnd(messageWire{
		Role:    "assistant",
		Content: json.RawMessage(`[{"type":"text","text":"final answer"}]`),
		Usage:   &Usage{Input: 10, Output: 3},
	})
	if len(blocks) != 1 || blocks[0].Text != "final answer" {
		t.Fatalf("blocks after message_end = %+v", blocks)
	}
	if got := s.Usage(); got.Input != 10 || got.Output != 3 {
		t.Errorf("usage = %+v, want the message_end values", got)
	}
}

// TestStreamTruncatedToolCall covers the three ways a tool call can fail to
// become executable. None of them may be handed downstream as valid.
func TestStreamTruncatedToolCall(t *testing.T) {
	tests := []struct {
		name   string
		script []assistantEvent
		end    messageWire
	}{
		{
			name: "no toolcall_end at all",
			script: []assistantEvent{
				textStart(0), textDelta(0, "running"), textEnd(0, "running"),
				callStart(1, "call_x", "bash"), callDelta(1, `{"command":`),
			},
			end: messageWire{Content: json.RawMessage(`[{"type":"text","text":"running"}]`)},
		},
		{
			name: "toolcall_end with malformed arguments",
			script: []assistantEvent{
				callStart(0, "call_y", "bash"),
				callDelta(0, `{"command":"go test`),
				callEnd(0, "call_y", "bash", `{"command":"go test`),
			},
			end: messageWire{},
		},
		{
			name: "toolcall_end without arguments after partial deltas",
			script: []assistantEvent{
				callStart(0, "call_z", "bash"),
				callDelta(0, `{"command":"go`),
				callEnd(0, "call_z", "bash", ``),
			},
			end: messageWire{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := feed(tc.script...)
			blocks := s.OnMessageEnd(tc.end)

			if !s.HasTruncatedToolCall() {
				t.Fatalf("HasTruncatedToolCall() = false, blocks = %+v", blocks)
			}
			if calls := s.ToolCalls(); len(calls) != 0 {
				t.Fatalf("ToolCalls() returned %+v; a truncated call must never be executable", calls)
			}
			var found bool
			for _, b := range blocks {
				if b.Kind != BlockToolCall {
					continue
				}
				found = true
				if b.Tool != nil && b.Tool.Valid {
					t.Errorf("truncated block reported as valid: %+v", b)
				}
			}
			if !found {
				t.Errorf("the truncated call disappeared from the blocks: %+v", blocks)
			}
		})
	}
}

// TestStreamValidToolCall is the happy path: authoritative arguments make the
// call executable.
func TestStreamValidToolCall(t *testing.T) {
	s := feed(
		callStart(0, "call_ok", "bash"),
		callDelta(0, `{"command":"ls"}`),
		callEnd(0, "call_ok", "bash", `{"command":"ls"}`),
	)
	blocks := s.OnMessageEnd(messageWire{})
	if s.HasTruncatedToolCall() {
		t.Fatalf("valid call flagged truncated: %+v", blocks)
	}
	calls := s.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("ToolCalls() = %+v, want one", calls)
	}
	if calls[0].ID != "call_ok" || calls[0].Name != "bash" {
		t.Errorf("call = %+v", calls[0])
	}
	if string(calls[0].Arguments) != `{"command":"ls"}` {
		t.Errorf("arguments = %s", calls[0].Arguments)
	}
	if calls[0].ArgsText != `{"command":"ls"}` {
		t.Errorf("display args = %q", calls[0].ArgsText)
	}
}

// TestStreamEmptyAssistantMessage makes sure an empty turn is not a nil-pointer
// panic or a phantom block.
func TestStreamEmptyAssistantMessage(t *testing.T) {
	s := NewStreamAssembler()
	s.OnMessageStart(messageWire{Role: "assistant"})
	blocks := s.OnMessageEnd(messageWire{
		Role:    "assistant",
		Content: json.RawMessage(`[]`),
	})
	if len(blocks) != 0 {
		t.Fatalf("blocks = %+v, want none", blocks)
	}
	if s.Text() != "" || s.Thinking() != "" {
		t.Errorf("text/thinking should be empty")
	}
	if s.HasTruncatedToolCall() {
		t.Errorf("an empty message has no truncated call")
	}
	if len(s.OrderedResults()) != 0 {
		t.Errorf("an empty message has no results")
	}
}

// TestStreamOutOfOrderToolResults is the ordering rule: results are emitted in
// the assistant's original call order, not the order the tools finished in.
func TestStreamOutOfOrderToolResults(t *testing.T) {
	s := feed(
		textStart(0), textEnd(0, "checking"),
		callStart(1, "call_first", "bash"), callEnd(1, "call_first", "bash", `{"command":"ls"}`),
		callStart(2, "call_second", "read"), callEnd(2, "call_second", "read", `{"path":"x"}`),
	)
	s.OnMessageEnd(messageWire{})

	// The second tool finishes first, as happens with parallel tool calls.
	s.OnToolEnd(toolEndWire{
		ToolCallID: "call_second", ToolName: "read", DurationMS: 5,
		Result: &toolResultWire{Content: json.RawMessage(`[{"type":"text","text":"second"}]`)},
	})
	s.OnToolEnd(toolEndWire{
		ToolCallID: "call_first", ToolName: "bash", DurationMS: 9, IsError: true,
		Result: &toolResultWire{Content: json.RawMessage(`[{"type":"text","text":"first"}]`)},
	})

	results := s.OrderedResults()
	if len(results) != 2 {
		t.Fatalf("results = %+v, want 2", results)
	}
	if results[0].ToolCallID != "call_first" {
		t.Fatalf("results out of order: %+v", results)
	}
	if results[0].Text != "first" || !results[0].IsError || results[0].DurationMS != 9 {
		t.Errorf("first result = %+v", results[0])
	}
	if results[1].ToolCallID != "call_second" || results[1].Text != "second" {
		t.Errorf("second result = %+v", results[1])
	}
}

// TestStreamUnknownToolResultIsLast keeps an unannounced tool result from
// displacing the announced ones.
func TestStreamUnknownToolResultIsLast(t *testing.T) {
	s := feed(
		callStart(0, "call_a", "bash"), callEnd(0, "call_a", "bash", `{}`),
	)
	s.OnToolEnd(toolEndWire{ToolCallID: "call_stranger", ToolName: "bash"})
	s.OnToolEnd(toolEndWire{ToolCallID: "call_a", ToolName: "bash"})
	results := s.OrderedResults()
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].ToolCallID != "call_a" {
		t.Errorf("announced call should sort first: %+v", results)
	}
}

// TestStreamResetClearsBlocks checks reuse across messages.
func TestStreamResetClearsBlocks(t *testing.T) {
	s := feed(textStart(0), textEnd(0, "one"))
	if len(s.Blocks()) != 1 {
		t.Fatal("setup failed")
	}
	s.Reset()
	if len(s.Blocks()) != 0 || s.Text() != "" {
		t.Fatalf("Reset left state behind: %+v", s.Blocks())
	}
}
