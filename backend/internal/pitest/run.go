package pitest

import (
	"encoding/json"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Prompt handling and the scripted run
// ---------------------------------------------------------------------------

func (s *server) handlePrompt(c command) {
	s.mu.Lock()
	if s.running {
		if c.StreamingBehavior == "" {
			s.mu.Unlock()
			s.fail(c, "agent is streaming; streamingBehavior is required")
			return
		}
		switch c.StreamingBehavior {
		case "steer":
			s.steering = append(s.steering, c.Message)
		case "followUp":
			s.followUp = append(s.followUp, c.Message)
		default:
			s.mu.Unlock()
			s.fail(c, "unknown streamingBehavior: "+c.StreamingBehavior)
			return
		}
		s.mu.Unlock()
		s.ok(c, map[string]any{"disposition": "queued"})
		return
	}
	s.running = true
	s.streaming = true
	s.abortSignalled = false
	cancel := make(chan struct{})
	s.runCancel = cancel
	text := c.Message
	s.mu.Unlock()

	s.ok(c, map[string]any{"disposition": "started"})

	s.runs.Add(1)
	go func() {
		defer s.runs.Done()
		s.run(text, cancel)
	}()
}

// cancelled reports whether abort has fired for this run. It reads only the
// channel captured when the run started, so a later run cannot be cancelled by
// a stale abort.
func cancelled(cancel chan struct{}) bool {
	select {
	case <-cancel:
		return true
	default:
		return false
	}
}

// sleep waits d, returning false when the run was cancelled first.
func (s *server) sleep(d time.Duration, cancel chan struct{}) bool {
	if d <= 0 {
		return !cancelled(cancel)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-cancel:
		return false
	}
}

// run emits one full scripted agent cycle: the user echo, an assistant turn
// with thinking, text and one tool call, the tool execution, and a closing
// assistant turn. It ends with agent_end and agent_settled; only the latter is
// a completion signal, exactly as documented for Pi.
func (s *server) run(text string, cancel chan struct{}) {
	defer func() {
		s.mu.Lock()
		s.streaming = false
		s.running = false
		s.runCancel = nil
		s.mu.Unlock()
	}()

	ts := time.Now().UnixMilli()

	s.emit(map[string]any{"type": "agent_start"})

	// ---- turn 1: user message ---------------------------------------------
	s.emit(map[string]any{"type": "turn_start"})
	userMsg := message{Role: "user", Content: text, Timestamp: ts}
	s.emit(map[string]any{"type": "message_start", "message": userMsg})
	s.emit(map[string]any{"type": "message_end", "message": userMsg})
	s.mu.Lock()
	s.appendEntry(entry{Type: "message", Message: map[string]any{
		"role": "user", "content": text, "timestamp": ts}})
	s.mu.Unlock()

	// ---- assistant turn with thinking, text and one tool call --------------
	s.emit(map[string]any{"type": "turn_start"})

	asstStart := map[string]any{
		"role": "assistant", "content": []any{}, "stopReason": "pending",
		"provider": s.provider, "model": s.modelID, "timestamp": ts + 1,
	}
	s.emit(map[string]any{"type": "message_start", "message": asstStart})

	thinking := "Let me look at the repository first."
	s.emit(map[string]any{"type": "message_update", "usage": usage{},
		"assistantMessageEvent": map[string]any{
			"type": "thinking_start", "contentIndex": 0}})
	for _, chunk := range chunks(thinking, 8) {
		if cancelled(cancel) {
			s.finishAborted()
			return
		}
		s.emit(map[string]any{"type": "message_update", "usage": usage{},
			"assistantMessageEvent": map[string]any{
				"type": "thinking_delta", "contentIndex": 0, "delta": chunk}})
		if !s.sleep(s.delay, cancel) {
			s.finishAborted()
			return
		}
	}
	s.emit(map[string]any{"type": "message_update", "usage": usage{},
		"assistantMessageEvent": map[string]any{
			"type": "thinking_end", "contentIndex": 0, "content": thinking}})

	body := "I'll read the cache package and then run the tests."
	s.emit(map[string]any{"type": "message_update", "usage": usage{},
		"assistantMessageEvent": map[string]any{
			"type": "text_start", "contentIndex": 1}})
	for _, chunk := range chunks(body, 10) {
		if cancelled(cancel) {
			s.finishAborted()
			return
		}
		s.emit(map[string]any{"type": "message_update",
			"usage": usage{Input: 120, Output: int64(len(body))},
			"assistantMessageEvent": map[string]any{
				"type": "text_delta", "contentIndex": 1, "delta": chunk}})
		if !s.sleep(s.delay, cancel) {
			s.finishAborted()
			return
		}
	}
	s.emit(map[string]any{"type": "message_update", "usage": usage{},
		"assistantMessageEvent": map[string]any{
			"type": "text_end", "contentIndex": 1, "content": body}})

	// tool call block
	s.mu.Lock()
	toolCallID := "call_" + s.newID()
	s.mu.Unlock()
	toolArgs := map[string]any{"command": "go test ./internal/cache/..."}
	argsJSON, _ := json.Marshal(toolArgs)
	s.emit(map[string]any{"type": "message_update", "usage": usage{},
		"assistantMessageEvent": map[string]any{
			"type": "toolcall_start", "contentIndex": 2,
			"id": toolCallID, "toolName": "bash"}})
	for _, chunk := range chunks(string(argsJSON), 12) {
		if cancelled(cancel) {
			s.finishAborted()
			return
		}
		s.emit(map[string]any{"type": "message_update", "usage": usage{},
			"assistantMessageEvent": map[string]any{
				"type": "toolcall_delta", "contentIndex": 2, "delta": chunk}})
	}
	s.emit(map[string]any{"type": "message_update", "usage": usage{},
		"assistantMessageEvent": map[string]any{
			"type": "toolcall_end", "contentIndex": 2,
			"toolCall": map[string]any{
				"id": toolCallID, "name": "bash", "arguments": toolArgs}}})

	asstMsg := map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": thinking},
			map[string]any{"type": "text", "text": body},
			map[string]any{"type": "toolCall", "id": toolCallID,
				"name": "bash", "arguments": toolArgs},
		},
		"provider": s.provider, "model": s.modelID,
		"usage": map[string]any{"input": 120, "output": int64(len(body)),
			"cacheRead": 64, "cacheWrite": 0, "totalTokens": 120 + len(body)},
		"stopReason": "toolUse", "timestamp": ts + 2,
	}
	s.emit(map[string]any{"type": "message_end", "message": asstMsg})
	s.mu.Lock()
	s.appendEntry(entry{Type: "message", Message: asstMsg})
	s.mu.Unlock()

	// ---- tool execution ---------------------------------------------------
	s.emit(map[string]any{"type": "tool_execution_start",
		"toolCallId": toolCallID, "toolName": "bash", "args": toolArgs})

	output := "=== RUN   TestCacheEvict\n--- PASS: TestCacheEvict (0.02s)\nPASS\nok  \tinternal/cache\t0.031s\n"
	if s.script == ScriptError {
		output = "=== RUN   TestCacheEvict\n--- FAIL: TestCacheEvict (0.02s)\n\tcache_test.go:42: race detected\nFAIL\n"
	}
	var partial strings.Builder
	for _, chunk := range chunks(output, 24) {
		if cancelled(cancel) {
			s.finishAborted()
			return
		}
		partial.WriteString(chunk)
		s.emit(map[string]any{"type": "tool_execution_update",
			"toolCallId": toolCallID, "toolName": "bash", "args": toolArgs,
			"partialResult": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": partial.String()}},
				"details": map[string]any{}}})
		if !s.sleep(s.delay, cancel) {
			s.finishAborted()
			return
		}
	}
	isErr := s.script == ScriptError
	s.emit(map[string]any{"type": "tool_execution_end",
		"toolCallId": toolCallID, "toolName": "bash",
		"result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": output}},
			"details": map[string]any{}},
		"isError": isErr, "durationMs": 1240})

	s.emit(map[string]any{"type": "turn_end", "message": asstMsg,
		"toolResults": []any{map[string]any{
			"role": "toolResult", "toolCallId": toolCallID, "toolName": "bash",
			"content": []any{map[string]any{"type": "text", "text": output}},
			"isError": isErr, "timestamp": ts + 3}}})

	// ---- final turn -------------------------------------------------------
	s.emit(map[string]any{"type": "turn_start"})
	final := "Tests pass. The eviction race is in `lru.go:88`."
	if isErr {
		final = "Tests fail with a data race at `cache_test.go:42`."
	}
	finalMsg := map[string]any{
		"role":     "assistant",
		"content":  []any{map[string]any{"type": "text", "text": final}},
		"provider": s.provider, "model": s.modelID,
		"usage": map[string]any{"input": 240, "output": 24,
			"cacheRead": 128, "cacheWrite": 0, "totalTokens": 264},
		"stopReason": "stop", "timestamp": ts + 4,
	}
	s.emit(map[string]any{"type": "message_start", "message": map[string]any{
		"role": "assistant", "content": []any{}, "stopReason": "pending",
		"timestamp": ts + 4}})
	for _, chunk := range chunks(final, 8) {
		if cancelled(cancel) {
			s.finishAborted()
			return
		}
		s.emit(map[string]any{"type": "message_update",
			"usage": usage{Input: 240, Output: 24},
			"assistantMessageEvent": map[string]any{
				"type": "text_delta", "contentIndex": 0, "delta": chunk}})
		if !s.sleep(s.delay, cancel) {
			s.finishAborted()
			return
		}
	}
	s.emit(map[string]any{"type": "message_update", "usage": usage{},
		"assistantMessageEvent": map[string]any{
			"type": "text_end", "contentIndex": 0, "content": final}})
	s.emit(map[string]any{"type": "message_end", "message": finalMsg})
	s.mu.Lock()
	s.appendEntry(entry{Type: "message", Message: finalMsg})
	s.persist()
	s.mu.Unlock()
	s.emit(map[string]any{"type": "turn_end", "message": finalMsg, "toolResults": []any{}})

	s.emit(map[string]any{"type": "agent_end", "messages": []any{}, "willRetry": false})

	// Drain one queued message, like Pi does when a run ends.
	//
	// Steering is drained before follow-ups because a steering message is the
	// one the user wanted applied sooner: real Pi injects it right after the
	// current turn's tool calls, so it must not sit behind a follow-up that was
	// only meant as a postscript. Re-running with it keeps the transcript
	// honest without pretending to implement mid-turn injection.
	s.mu.Lock()
	var next string
	hasNext := false
	if len(s.steering) > 0 {
		next = s.steering[0]
		s.steering = s.steering[1:]
		hasNext = true
	} else if len(s.followUp) > 0 {
		next = s.followUp[0]
		s.followUp = s.followUp[1:]
		hasNext = true
	}
	steeringLeft := append([]string{}, s.steering...)
	followUpLeft := append([]string{}, s.followUp...)
	s.mu.Unlock()

	if hasNext {
		if len(steeringLeft)+len(followUpLeft) > 0 {
			s.emit(map[string]any{
				"type": "queue_update", "steering": steeringLeft, "followUp": followUpLeft,
			})
		}
		s.run(next, cancel)
		return
	}
	s.emit(map[string]any{"type": "agent_settled", "aborted": false})
}

// finishAborted emits the terminal pair for a cancelled run. agent_end comes
// first because that is what Pi emits while unwinding; agent_settled with
// aborted:true is the signal consumers must wait for.
func (s *server) finishAborted() {
	s.emit(map[string]any{"type": "agent_end", "messages": []any{}, "willRetry": false})
	s.emit(map[string]any{"type": "agent_settled", "aborted": true})
}

func (s *server) handleCompact(c command) {
	s.mu.Lock()
	s.compacting = true
	tokensBefore := int64(150000)
	s.mu.Unlock()

	s.emit(map[string]any{"type": "compaction_start", "reason": "manual"})
	s.mu.Lock()
	summary := "Earlier the user asked to fix the cache eviction race."
	if c.CustomInstructions != "" {
		summary = c.CustomInstructions
	}
	id := s.appendEntry(entry{Type: "compaction", Summary: summary})
	s.persist()
	s.mu.Unlock()
	s.emit(map[string]any{"type": "compaction_end", "reason": "manual",
		"result": map[string]any{
			"summary": summary, "firstKeptEntryId": id,
			"tokensBefore": tokensBefore, "estimatedTokensAfter": 32000,
			"usage": map[string]any{"input": 1000, "output": 200,
				"cacheRead": 0, "cacheWrite": 0, "totalTokens": 1200},
		},
		"aborted": false, "willRetry": false})
	s.mu.Lock()
	s.compacting = false
	s.mu.Unlock()
	s.ok(c, map[string]any{"summary": summary, "firstKeptEntryId": id,
		"tokensBefore": tokensBefore, "estimatedTokensAfter": 32000})
}

// chunks splits s into pieces of at most n bytes without breaking a UTF-8 rune,
// so deltas stay valid text even for multi-byte content. A single rune longer
// than n is emitted whole rather than corrupted.
func chunks(s string, n int) []string {
	if n <= 0 {
		n = 8
	}
	var out []string
	for len(s) > n {
		cut := n
		for cut > 0 && !utf8Start(s[cut]) {
			cut--
		}
		if cut == 0 {
			// The rune at the start is longer than the budget: keep it whole.
			cut = 1
			for cut < len(s) && !utf8Start(s[cut]) {
				cut++
			}
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// utf8Start reports whether b can begin a UTF-8 encoded rune.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
