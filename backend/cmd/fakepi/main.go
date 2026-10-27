// Command fakepi is a Pi-compatible RPC peer used by tests and by the local
// development sandbox. It speaks the same JSONL protocol documented in
// packages/coding-agent/docs/rpc.md: commands on stdin, responses and session
// events on stdout, newline framing only.
//
// It implements just enough of the protocol to exercise PiForge end to end:
// prompting (including steering and follow-up queues), a scripted tool call,
// abort, compaction, and the append-only session tree that powers forks.
//
// It is NOT a model. The "assistant" output is scripted so that tests never
// need network access or API keys.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Wire types (only the fields fakepi needs)
// ---------------------------------------------------------------------------

type command struct {
	ID                string  `json:"id"`
	Type              string  `json:"type"`
	Message           string  `json:"message"`
	StreamingBehavior string  `json:"streamingBehavior"`
	Images            []any   `json:"images"`
	Provider          string  `json:"provider"`
	ModelID           string  `json:"modelId"`
	Level             string  `json:"level"`
	Enabled           *bool   `json:"enabled"`
	EntryID           string  `json:"entryId"`
	SessionPath       string  `json:"sessionPath"`
	Since             string  `json:"since"`
	Name              string  `json:"name"`
	CustomInstructions string `json:"customInstructions"`
	Command           string  `json:"command"`
	ExcludeFromContext bool   `json:"excludeFromContext"`
}

type response struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Command string `json:"command"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// ---------------------------------------------------------------------------
// Session tree
// ---------------------------------------------------------------------------

type entry struct {
	Type      string    `json:"type"`
	ID        string    `json:"id,omitempty"`
	ParentID  *string   `json:"parentId"`
	Timestamp time.Time `json:"timestamp,omitempty"`
	Message   any       `json:"message,omitempty"`
	Label     string    `json:"label,omitempty"`
	TargetID  string    `json:"targetId,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	CustomType string   `json:"customType,omitempty"`
	Data      any       `json:"data,omitempty"`
}

type message struct {
	Role     string     `json:"role"`
	Content  any        `json:"content"`
	Provider string     `json:"provider,omitempty"`
	Model    string     `json:"model,omitempty"`
	Usage    *usage     `json:"usage,omitempty"`
	StopReason string   `json:"stopReason,omitempty"`
	Timestamp int64     `json:"timestamp"`
}

type usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	TotalTokens int64 `json:"totalTokens"`
}

// ---------------------------------------------------------------------------
// Agent
// ---------------------------------------------------------------------------

type server struct {
	mu     sync.Mutex
	out    *bufio.Writer
	outMu  sync.Mutex
	seq    int
	leaf   string
	entries []entry
	nextID int
	file   string

	running   bool
	streaming bool
	compacting bool
	abort     chan struct{}
	steering  []string
	followUp  []string
	runs      sync.WaitGroup

	provider string
	modelID  string
	level    string
	name     string
	script   string
	delay    time.Duration
	step     int
}

func newServer(sessionFile, script string, delay time.Duration) *server {
	return &server{
		out:      bufio.NewWriter(os.Stdout),
		file:     sessionFile,
		provider: "fake",
		modelID:  "fake-1",
		level:    "medium",
		script:   script,
		delay:    delay,
		abort:    make(chan struct{}),
	}
}

func (s *server) emit(v any) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.out.Write(b)
	s.out.WriteByte('\n')
	s.out.Flush()
}

func (s *server) respond(r response) {
	if r.Type == "" {
		r.Type = "response"
	}
	s.emit(r)
}

func (s *server) newID() string {
	s.nextID++
	return fmt.Sprintf("%08x", 0x1000+s.nextID)
}

func (s *server) appendEntry(e entry) string {
	if e.ID == "" {
		e.ID = s.newID()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if e.ParentID == nil && s.leaf != "" {
		leaf := s.leaf
		e.ParentID = &leaf
	}
	s.entries = append(s.entries, e)
	s.leaf = e.ID
	return e.ID
}

func (s *server) persist() {
	if s.file == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return
	}
	f, err := os.Create(s.file)
	if err != nil {
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	header := map[string]any{
		"type": "session", "version": 3,
		"id": filepath.Base(s.file), "timestamp": time.Now().UTC(),
		"cwd": must(os.Getwd()),
	}
	b, _ := json.Marshal(header)
	w.Write(b)
	w.WriteByte('\n')
	for _, e := range s.entries {
		b, _ := json.Marshal(e)
		w.Write(b)
		w.WriteByte('\n')
	}
	w.Flush()
}

func must[T any](v T, _ error) T { return v }

// ---------------------------------------------------------------------------
// Command loop
// ---------------------------------------------------------------------------

func (s *server) handle(c command) {
	switch c.Type {
	case "prompt":
		s.handlePrompt(c)
	case "steer":
		s.mu.Lock()
		s.steering = append(s.steering, c.Message)
		q := append([]string{}, s.steering...)
		s.mu.Unlock()
		s.emit(map[string]any{"type": "queue_update", "steering": q, "followUp": s.followSnapshot()})
		s.respond(response{ID: c.ID, Command: "steer", Success: true,
			Data: map[string]any{"disposition": "queued"}})
	case "follow_up":
		s.mu.Lock()
		s.followUp = append(s.followUp, c.Message)
		q := append([]string{}, s.followUp...)
		s.mu.Unlock()
		s.emit(map[string]any{"type": "queue_update", "steering": []string{}, "followUp": q})
		s.respond(response{ID: c.ID, Command: "follow_up", Success: true,
			Data: map[string]any{"disposition": "queued"}})
	case "abort":
		s.mu.Lock()
		if s.abort != nil {
			close(s.abort)
			s.abort = nil
		}
		s.running = false
		s.streaming = false
		s.steering = nil
		s.followUp = nil
		s.mu.Unlock()
		s.respond(response{ID: c.ID, Command: "abort", Success: true})
	case "clear_queue":
		s.mu.Lock()
		st, fu := s.steering, s.followUp
		s.steering, s.followUp = nil, nil
		s.mu.Unlock()
		s.respond(response{ID: c.ID, Command: "clear_queue", Success: true,
			Data: map[string]any{"steering": st, "followUp": fu}})
	case "get_state":
		s.mu.Lock()
		st := map[string]any{
			"model": map[string]any{
				"id": s.modelID, "provider": s.provider, "name": s.modelID,
				"api": "openai-compat", "contextWindow": 200000, "maxTokens": 16384,
				"reasoning": true, "input": []string{"text", "image"},
			},
			"thinkingLevel":         s.level,
			"isStreaming":           s.streaming,
			"isCompacting":          s.compacting,
			"steeringMode":          "one-at-a-time",
			"followUpMode":          "one-at-a-time",
			"sessionFile":           s.file,
			"sessionId":             filepath.Base(s.file),
			"autoCompactionEnabled": true,
			"messageCount":          len(s.entries),
			"pendingMessageCount":   len(s.steering) + len(s.followUp),
		}
		if s.name != "" {
			st["sessionName"] = s.name
		}
		s.mu.Unlock()
		s.respond(response{ID: c.ID, Command: "get_state", Success: true, Data: st})
	case "get_messages":
		s.mu.Lock()
		msgs := make([]any, 0, len(s.entries))
		for _, e := range s.entries {
			if e.Type == "message" {
				msgs = append(msgs, e.Message)
			}
		}
		s.mu.Unlock()
		s.respond(response{ID: c.ID, Command: "get_messages", Success: true,
			Data: map[string]any{"messages": msgs}})
	case "get_tree":
		s.mu.Lock()
		tree := s.buildTree()
		leaf := s.leaf
		s.mu.Unlock()
		s.respond(response{ID: c.ID, Command: "get_tree", Success: true,
			Data: map[string]any{"tree": tree, "leafId": leaf}})
	case "get_entries":
		s.mu.Lock()
		out := make([]entry, 0, len(s.entries))
		started := c.Since == ""
		for _, e := range s.entries {
			if !started {
				if e.ID == c.Since {
					started = true
				}
				continue
			}
			out = append(out, e)
		}
		leaf := s.leaf
		s.mu.Unlock()
		if !started {
			s.respond(response{ID: c.ID, Command: "get_entries", Success: false,
				Error: "unknown cursor"})
			return
		}
		s.respond(response{ID: c.ID, Command: "get_entries", Success: true,
			Data: map[string]any{"entries": out, "leafId": leaf}})
	case "get_fork_messages":
		s.mu.Lock()
		type fp struct {
			EntryID string `json:"entryId"`
			Text    string `json:"text"`
		}
		pts := []fp{}
		for _, e := range s.entries {
			if e.Type != "message" {
				continue
			}
			m, ok := e.Message.(map[string]any)
			if !ok || m["role"] != "user" {
				continue
			}
			pts = append(pts, fp{EntryID: e.ID, Text: textOf(m["content"])})
		}
		s.mu.Unlock()
		s.respond(response{ID: c.ID, Command: "get_fork_messages", Success: true,
			Data: map[string]any{"messages": pts}})
	case "fork":
		s.mu.Lock()
		idx := -1
		for i, e := range s.entries {
			if e.ID == c.EntryID {
				idx = i
				break
			}
		}
		var text string
		if idx >= 0 && idx < len(s.entries) {
			if m, ok := s.entries[idx].Message.(map[string]any); ok {
				text = textOf(m["content"])
			}
			s.entries = s.entries[:idx+1]
			s.leaf = s.entries[idx].ID
		}
		s.persist()
		s.mu.Unlock()
		if idx < 0 {
			s.respond(response{ID: c.ID, Command: "fork", Success: false, Error: "unknown entry"})
			return
		}
		s.respond(response{ID: c.ID, Command: "fork", Success: true,
			Data: map[string]any{"text": text, "cancelled": false}})
	case "clone":
		s.respond(response{ID: c.ID, Command: "clone", Success: true,
			Data: map[string]any{"cancelled": false}})
	case "switch_session":
		s.respond(response{ID: c.ID, Command: "switch_session", Success: true,
			Data: map[string]any{"cancelled": false}})
	case "set_model":
		s.mu.Lock()
		s.provider, s.modelID = c.Provider, c.ModelID
		s.mu.Unlock()
		s.respond(response{ID: c.ID, Command: "set_model", Success: true,
			Data: map[string]any{"id": c.ModelID, "provider": c.Provider}})
	case "get_available_models":
		s.respond(response{ID: c.ID, Command: "get_available_models", Success: true,
			Data: map[string]any{"models": []any{
				map[string]any{"id": "fake-1", "provider": "fake", "contextWindow": 200000,
					"maxTokens": 16384, "reasoning": true},
			}}})
	case "set_thinking_level":
		s.mu.Lock()
		s.level = c.Level
		s.mu.Unlock()
		s.emit(map[string]any{"type": "thinking_level_changed", "level": c.Level})
		s.respond(response{ID: c.ID, Command: "set_thinking_level", Success: true})
	case "get_available_thinking_levels":
		s.respond(response{ID: c.ID, Command: "get_available_thinking_levels", Success: true,
			Data: map[string]any{"levels": []string{"off", "medium", "high"}}})
	case "compact":
		s.handleCompact(c)
	case "get_session_stats":
		s.mu.Lock()
		var in, out int64
		var tools int
		for _, e := range s.entries {
			if e.Type == "message" {
				if m, ok := e.Message.(map[string]any); ok {
					if u, ok := m["usage"].(map[string]any); ok {
						in += asInt(u["input"])
						out += asInt(u["output"])
					}
				}
			}
			if e.Type == "tool" {
				tools++
			}
		}
		s.mu.Unlock()
		s.respond(response{ID: c.ID, Command: "get_session_stats", Success: true,
			Data: map[string]any{
				"sessionFile": s.file,
				"userMessages": 1, "assistantMessages": 1, "toolCalls": tools,
				"totalMessages": len(s.entries),
				"tokens": map[string]any{"input": in, "output": out,
					"cacheRead": 0, "cacheWrite": 0, "total": in + out},
				"cost": 0.01,
				"contextUsage": map[string]any{
					"tokens": in + out, "contextWindow": 200000, "percent": float64(in+out) / 2000.0,
				},
			}})
	case "set_session_name":
		s.mu.Lock()
		s.name = c.Name
		s.mu.Unlock()
		s.emit(map[string]any{"type": "session_info_changed", "name": c.Name})
		s.respond(response{ID: c.ID, Command: "set_session_name", Success: true})
	case "set_auto_compaction", "set_auto_retry", "set_steering_mode", "set_follow_up_mode":
		s.respond(response{ID: c.ID, Command: c.Type, Success: true})
	case "abort_retry", "abort_bash":
		s.respond(response{ID: c.ID, Command: c.Type, Success: true})
	case "bash":
		s.respond(response{ID: c.ID, Command: "bash", Success: true,
			Data: map[string]any{"output": "fake-bash: " + c.Command, "exitCode": 0,
				"cancelled": false, "truncated": false}})
	case "get_last_assistant_text":
		s.mu.Lock()
		var text string
		for _, e := range s.entries {
			if e.Type == "message" {
				if m, ok := e.Message.(map[string]any); ok && m["role"] == "assistant" {
					text = textOf(m["content"])
				}
			}
		}
		s.mu.Unlock()
		s.respond(response{ID: c.ID, Command: "get_last_assistant_text", Success: true,
			Data: map[string]any{"text": text}})
	case "get_commands":
		s.respond(response{ID: c.ID, Command: "get_commands", Success: true,
			Data: map[string]any{"commands": []any{}}})
	default:
		s.respond(response{ID: c.ID, Command: c.Type, Success: false,
			Error: "unsupported command: " + c.Type})
	}
}

func (s *server) followSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.followUp...)
}

func (s *server) buildTree() []map[string]any {
	nodes := map[string]*struct {
		Entry    entry            `json:"entry"`
		Children []map[string]any `json:"children"`
	}{}
	order := []string{}
	for _, e := range s.entries {
		nodes[e.ID] = &struct {
			Entry    entry            `json:"entry"`
			Children []map[string]any `json:"children"`
		}{Entry: e, Children: []map[string]any{}}
		order = append(order, e.ID)
	}
	roots := []map[string]any{}
	for _, id := range order {
		n := nodes[id]
		parent := ""
		if n.Entry.ParentID != nil {
			parent = *n.Entry.ParentID
		}
		child := map[string]any{"entry": n.Entry, "children": []map[string]any{}}
		if p, ok := nodes[parent]; ok {
			p.Children = append(p.Children, child)
		} else {
			roots = append(roots, child)
		}
	}
	return roots
}

func asInt(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	}
	return 0
}

func textOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var b strings.Builder
		for _, part := range t {
			if m, ok := part.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

// ---------------------------------------------------------------------------
// The scripted run
// ---------------------------------------------------------------------------

func (s *server) handlePrompt(c command) {
	s.mu.Lock()
	if s.running {
		if c.StreamingBehavior == "" {
			s.mu.Unlock()
			s.respond(response{ID: c.ID, Command: "prompt", Success: false,
				Error: "agent is streaming; streamingBehavior is required"})
			return
		}
		// Queue while running, exactly like Pi.
		switch c.StreamingBehavior {
		case "steer":
			s.steering = append(s.steering, c.Message)
		case "followUp":
			s.followUp = append(s.followUp, c.Message)
		}
		disp := "queued"
		s.mu.Unlock()
		s.respond(response{ID: c.ID, Command: "prompt", Success: true,
			Data: map[string]any{"disposition": disp}})
		return
	}
	s.running = true
	s.streaming = true
	s.abort = make(chan struct{})
	abortCh := s.abort
	text := c.Message
	s.mu.Unlock()

	s.respond(response{ID: c.ID, Command: "prompt", Success: true,
		Data: map[string]any{"disposition": "started"}})

	s.runs.Add(1)
	go func() {
		defer s.runs.Done()
		s.run(text, abortCh)
	}()
}

func (s *server) sleep(d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-time.After(d):
		return true
	default:
	}
	t := time.NewTimer(d)
	defer t.Stop()
	s.mu.Lock()
	ch := s.abort
	s.mu.Unlock()
	if ch == nil {
		<-t.C
		return true
	}
	select {
	case <-t.C:
		return true
	case <-ch:
		return false
	}
}

func (s *server) pipedOut() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.abort:
		return false
	default:
		return true
	}
}

func (s *server) run(text string, abortCh chan struct{}) {
	defer func() {
		s.mu.Lock()
		s.streaming = false
		s.running = false
		s.mu.Unlock()
	}()

	ts := time.Now().UnixMilli()

	s.emit(map[string]any{"type": "agent_start"})

	// ---- turn 1: user message -------------------------------------------------
	s.emit(map[string]any{"type": "turn_start"})
	userMsg := message{Role: "user", Content: text, Timestamp: ts}
	s.emit(map[string]any{"type": "message_start", "message": userMsg})
	s.emit(map[string]any{"type": "message_end", "message": userMsg})
	s.mu.Lock()
	s.appendEntry(entry{Type: "message", Message: map[string]any{
		"role": "user", "content": text, "timestamp": ts}})
	s.mu.Unlock()

	// ---- assistant turn with thinking, text and one tool call -----------------
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
		if !s.pipedOut() { s.finishAborted(); return }
		s.emit(map[string]any{"type": "message_update", "usage": usage{},
			"assistantMessageEvent": map[string]any{
				"type": "thinking_delta", "contentIndex": 0, "delta": chunk}})
		if !s.sleep(s.delay) { s.finishAborted(); return }
	}
	s.emit(map[string]any{"type": "message_update", "usage": usage{},
		"assistantMessageEvent": map[string]any{
			"type": "thinking_end", "contentIndex": 0, "content": thinking}})

	body := "I'll read the cache package and then run the tests."
	s.emit(map[string]any{"type": "message_update", "usage": usage{},
		"assistantMessageEvent": map[string]any{
			"type": "text_start", "contentIndex": 1}})
	for _, chunk := range chunks(body, 10) {
		if !s.pipedOut() { s.finishAborted(); return }
		s.emit(map[string]any{"type": "message_update",
			"usage": usage{Input: 120, Output: int64(len(body))},
			"assistantMessageEvent": map[string]any{
				"type": "text_delta", "contentIndex": 1, "delta": chunk}})
		if !s.sleep(s.delay) { s.finishAborted(); return }
	}
	s.emit(map[string]any{"type": "message_update", "usage": usage{},
		"assistantMessageEvent": map[string]any{
			"type": "text_end", "contentIndex": 1, "content": body}})

	// tool call block
	toolCallID := "call_" + s.newID()
	toolArgs := map[string]any{"command": "go test ./internal/cache/..."}
	argsJSON, _ := json.Marshal(toolArgs)
	s.emit(map[string]any{"type": "message_update", "usage": usage{},
		"assistantMessageEvent": map[string]any{
			"type": "toolcall_start", "contentIndex": 2,
			"id": toolCallID, "toolName": "bash"}})
	for _, chunk := range chunks(string(argsJSON), 12) {
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

	// ---- tool execution -------------------------------------------------------
	s.emit(map[string]any{"type": "tool_execution_start",
		"toolCallId": toolCallID, "toolName": "bash", "args": toolArgs})

	output := "=== RUN   TestCacheEvict\n--- PASS: TestCacheEvict (0.02s)\nPASS\nok  \tinternal/cache\t0.031s\n"
	if s.script == "error" {
		output = "=== RUN   TestCacheEvict\n--- FAIL: TestCacheEvict (0.02s)\n\tcache_test.go:42: race detected\nFAIL\n"
	}
	var partial strings.Builder
	for _, chunk := range chunks(output, 24) {
		if !s.pipedOut() { s.finishAborted(); return }
		partial.WriteString(chunk)
		s.emit(map[string]any{"type": "tool_execution_update",
			"toolCallId": toolCallID, "toolName": "bash", "args": toolArgs,
			"partialResult": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": partial.String()}},
				"details": map[string]any{}}})
		if !s.sleep(s.delay) { s.finishAborted(); return }
	}
	isErr := s.script == "error"
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

	// ---- final turn ----------------------------------------------------------
	s.emit(map[string]any{"type": "turn_start"})
	final := "Tests pass. The eviction race is in `lru.go:88`."
	if isErr {
		final = "Tests fail with a data race at `cache_test.go:42`."
	}
	finalMsg := map[string]any{
		"role": "assistant",
		"content": []any{map[string]any{"type": "text", "text": final}},
		"provider": s.provider, "model": s.modelID,
		"usage": map[string]any{"input": 240, "output": 24,
			"cacheRead": 128, "cacheWrite": 0, "totalTokens": 264},
		"stopReason": "stop", "timestamp": ts + 4,
	}
	s.emit(map[string]any{"type": "message_start", "message": map[string]any{
		"role": "assistant", "content": []any{}, "stopReason": "pending",
		"timestamp": ts + 4}})
	for _, chunk := range chunks(final, 8) {
		s.emit(map[string]any{"type": "message_update",
			"usage": usage{Input: 240, Output: 24},
			"assistantMessageEvent": map[string]any{
				"type": "text_delta", "contentIndex": 0, "delta": chunk}})
		if !s.sleep(s.delay) { s.finishAborted(); return }
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

	// Drain follow-ups exactly once, like Pi does when the run ends.
	s.mu.Lock()
	var next string
	hasNext := false
	if len(s.followUp) > 0 {
		next = s.followUp[0]
		s.followUp = s.followUp[1:]
		hasNext = true
	}
	s.mu.Unlock()

	if hasNext {
		s.run(next, abortCh)
		return
	}
	s.emit(map[string]any{"type": "agent_settled", "aborted": false})
}

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
	s.respond(response{ID: c.ID, Command: "compact", Success: true,
		Data: map[string]any{"summary": summary, "firstKeptEntryId": id,
			"tokensBefore": tokensBefore, "estimatedTokensAfter": 32000}})
}

func chunks(s string, n int) []string {
	if n <= 0 {
		n = 8
	}
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	sessionFile := flag.String("session-file", "", "path of the JSONL session file")
	script := flag.String("script", envOr("FAKEPI_SCRIPT", "ok"), "script: ok|error|slow")
	delayMs := flag.Int("delay", envInt("FAKEPI_DELAY_MS", 5), "per-chunk delay in ms")
	emitHeader := flag.Bool("json-header", false, "emit the session header first (JSON mode)")
	flag.Parse()

	delay := time.Duration(*delayMs) * time.Millisecond

	srv := newServer(*sessionFile, *script, delay)
	if *emitHeader {
		srv.emit(map[string]any{"type": "session", "version": 3, "id": "fake",
			"timestamp": time.Now().UTC(), "cwd": must(os.Getwd())})
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c command
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			srv.respond(response{Command: "parse", Success: false,
				Error: "Failed to parse command: " + err.Error()})
			continue
		}
		srv.handle(c)
	}
	// Closing stdin asks for an orderly shutdown. Pi disposes the active
	// runtime before exiting, so piped usage still gets the full event stream.
	srv.runs.Wait()
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
