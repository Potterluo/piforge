package pitest

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Prompt handling and the scripted run
// ---------------------------------------------------------------------------

// HoldMarker in a prompt asks the peer to pace that one run at
// HoldChunkDelay instead of its configured delay.
//
// Interrupting a run is a product behaviour that can only be tested against a run
// that is still in flight, and the default pacing finishes a scripted run in
// about a second — faster than a browser can find the stop button. The marker is
// how the end-to-end suite asks for a run it can stop, without making every other
// test slow.
const HoldMarker = "[[hold-run]]"

// HoldChunkDelay is the per-chunk pacing a held run uses: slow enough to click
// through, fast enough that a run that is never stopped still settles.
const HoldChunkDelay = 250 * time.Millisecond

// FailMarker in a prompt asks the peer to emit the failing variant of its
// scripted test output for that one run, while the process keeps its normal
// script. The test-results panel (FR-12.5) has to show a failing run and then a
// passing one in the same session, and a single fake process would otherwise
// always produce the same outcome.
const FailMarker = "[[failing-tests]]"

// NodeTestFileMarker in a prompt asks the peer to run its scripted test the way
// the acceptance pass's agent did: by executing the test file directly, with no
// `--test` flag. The panel has to recognise the file naming convention on its
// own; the marker exists so the end-to-end suite exercises that shape instead
// of the flag.
const NodeTestFileMarker = "[[node-test-file]]"

// SlowLinesMarker in a prompt asks the peer to run a bash call that prints one
// line every SlowLineDelay for about five seconds.
//
// FR-9.2 is about the follow view being *live*: the only honest assertion is one
// made while the command is still running, so the peer has to produce a command
// that runs long enough for a browser to look at it mid-flight. Like the other
// markers this changes only the script, never the wire protocol.
const SlowLinesMarker = "[[slow-lines]]"

// SlowLineCount and SlowLineDelay pace that run: seven lines 700ms apart is the
// "prints a line every 700ms for ~5s" the follow-view test needs.
const (
	SlowLineCount = 7
	SlowLineDelay = 700 * time.Millisecond
)

// BigContextMarker in a prompt asks the peer to report a nearly full context so
// a compaction has something visible to shrink. Without it the scripted session
// uses a few hundred tokens, and a compaction's "after" figure would be larger
// than its "before" — which would make a usage-drop assertion meaningless.
const BigContextMarker = "[[big-context]]"

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
	if strings.Contains(c.Message, BigContextMarker) {
		s.bigContext = true
	}
	cancel := make(chan struct{})
	s.runCancel = cancel
	text := c.Message
	delay := s.delay
	if strings.Contains(text, HoldMarker) {
		delay = HoldChunkDelay
	}
	s.mu.Unlock()

	s.ok(c, map[string]any{"disposition": "started"})

	s.runs.Add(1)
	go func() {
		defer s.runs.Done()
		s.run(text, delay, cancel)
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
func (s *server) run(text string, delay time.Duration, cancel chan struct{}) {
	defer func() {
		s.mu.Lock()
		s.streaming = false
		s.running = false
		s.runCancel = nil
		s.mu.Unlock()
	}()

	// The fail marker overrides the process-wide script for this one run. It is
	// a test affordance, like HoldMarker, and never changes the wire protocol.
	fail := s.script == ScriptError || strings.Contains(text, FailMarker)
	slow := strings.Contains(text, SlowLinesMarker)
	bigContext := strings.Contains(text, BigContextMarker)
	// The node file shape: same node:test harness as the default script, run by
	// executing the file rather than with `--test`.
	nodeFile := strings.Contains(text, NodeTestFileMarker)

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
		if !s.sleep(delay, cancel) {
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
		if !s.sleep(delay, cancel) {
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
	if slow {
		// A real shell command, so the timeline and the follow view show the same
		// thing a user would see: output that takes seconds to finish.
		toolArgs = map[string]any{
			"command": "for i in 1 2 3 4 5 6 7; do echo SLOW-LINE-$i; sleep 0.7; done",
		}
	}
	if nodeFile {
		toolArgs = map[string]any{"command": "node primes.test.js"}
	}
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
	if fail {
		output = "=== RUN   TestCacheEvict\n--- FAIL: TestCacheEvict (0.02s)\n\tcache_test.go:42: race detected\nFAIL\nFAIL\tinternal/cache\t0.021s\n"
	}
	if nodeFile {
		// The bytes node v22 actually prints for a directly executed node:test
		// file (piped stdout), so the parser is tested against the real reporter
		// and not a hand-written summary.
		output = nodeTestTAP(false)
		if fail {
			output = nodeTestTAP(true)
		}
	}
	var partial strings.Builder
	if slow {
		// One snapshot per line, 700ms apart: cumulative, exactly like Pi's
		// partialResult, so a consumer that appends snapshots instead of diffing
		// them duplicates the output and one that only reads the first snapshot
		// freezes after line one (both were real bugs).
		var loop strings.Builder
		for line := 1; line <= SlowLineCount; line++ {
			if cancelled(cancel) {
				s.finishAborted()
				return
			}
			fmt.Fprintf(&loop, "SLOW-LINE-%d\n", line)
			s.emit(map[string]any{"type": "tool_execution_update",
				"toolCallId": toolCallID, "toolName": "bash", "args": toolArgs,
				"partialResult": map[string]any{
					"content": []any{map[string]any{"type": "text", "text": loop.String()}},
					"details": map[string]any{}}})
			if !s.sleep(SlowLineDelay, cancel) {
				s.finishAborted()
				return
			}
		}
		output = loop.String()
	} else {
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
			if !s.sleep(delay, cancel) {
				s.finishAborted()
				return
			}
		}
	}
	isErr := fail
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
	// A real session's context grows with every turn; the `[[big-context]]`
	// marker fakes the "nearly full" reading a compaction is supposed to shrink.
	finalInput, finalOutput := int64(240), int64(24)
	if bigContext {
		finalInput = 150000
	}
	finalUsage := map[string]any{"input": finalInput, "output": finalOutput,
		"cacheRead": 0, "cacheWrite": 0, "totalTokens": finalInput + finalOutput}
	finalMsg := map[string]any{
		"role":     "assistant",
		"content":  []any{map[string]any{"type": "text", "text": final}},
		"provider": s.provider, "model": s.modelID,
		"usage":      finalUsage,
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
		if !s.sleep(delay, cancel) {
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
		s.run(next, delay, cancel)
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

// nodeTestTAP is the console output of `node primes.test.js` on node 22: TAP
// v13 with the summary lines node:test prints. The failing variant adds a
// `not ok` with a location, which is what the panel's failure row reads.
func nodeTestTAP(fail bool) string {
	if fail {
		return "TAP version 13\n" +
			"# Subtest: firstPrimes(10)\n" +
			"not ok 1 - firstPrimes(10)\n" +
			"  ---\n" +
			"  duration_ms: 0.95\n" +
			"  location: '/workspace/primes.test.js:8:3'\n" +
			"  failureType: 'testCodeFailure'\n" +
			"  error: 'expected 10 primes'\n" +
			"  ...\n" +
			"1..1\n" +
			"# tests 1\n" +
			"# suites 0\n" +
			"# pass 0\n" +
			"# fail 1\n" +
			"# cancelled 0\n" +
			"# skipped 0\n" +
			"# todo 0\n" +
			"# duration_ms 6.44\n"
	}
	return "TAP version 13\n" +
		"# Subtest: firstPrimes(10)\n" +
		"ok 1 - firstPrimes(10)\n" +
		"  ---\n" +
		"  duration_ms: 0.95619\n" +
		"  type: 'test'\n" +
		"  ...\n" +
		"# Subtest: firstPrimes(0)\n" +
		"ok 2 - firstPrimes(0)\n" +
		"  ---\n" +
		"  duration_ms: 0.077841\n" +
		"  type: 'test'\n" +
		"  ...\n" +
		"1..2\n" +
		"# tests 2\n" +
		"# suites 0\n" +
		"# pass 2\n" +
		"# fail 0\n" +
		"# cancelled 0\n" +
		"# skipped 0\n" +
		"# todo 0\n" +
		"# duration_ms 6.447074\n"
}

func (s *server) handleCompact(c command) {
	s.mu.Lock()
	// Pi's own rule: a session whose context is still small has nothing worth
	// compacting, and the command fails with that sentence. Reproducing it here
	// is what lets the no-op path (200 + reason, not a 500) be tested end to end.
	if !s.bigContext {
		s.mu.Unlock()
		// The real agent still announces the attempt: compaction_start and a
		// compaction_end with no result, then the command fails. That pair is
		// exactly what the transcript used to turn into a 「上下文已压缩」 marker
		// while the API said compacted:false, so the fake has to emit it for the
		// end-to-end regression to be about the product and not the fake.
		s.emit(map[string]any{"type": "compaction_start", "reason": "manual"})
		s.emit(map[string]any{"type": "compaction_end", "reason": "manual"})
		s.fail(c, "Nothing to compact (session too small)")
		return
	}
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
			// The usage after a compaction is the size of the summary plus what
			// was kept: smaller than tokensBefore, which is what makes the
			// context meter visibly drop in FR-1.9's test.
			"usage": map[string]any{"input": 32000, "output": 200,
				"cacheRead": 0, "cacheWrite": 0, "totalTokens": 32200},
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
