// Package pitest implements a Pi-compatible RPC peer that tests and local
// development can drive in-process or as a subprocess.
//
// The wire protocol is the one documented in docs/PRD.md §2.1: commands arrive
// on stdin as JSONL, responses and session events leave on stdout as JSONL, and
// framing is strictly LF. Records are separated by '\n' only — never by
// U+2028/U+2029, which are legal inside JSON strings.
//
// The peer is deliberately not a model. Assistant output is scripted so a test
// can exercise a full run (thinking, text, one tool call, abort, compaction,
// forks) without network access or API keys.
package pitest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Script names understood by Serve. They select the canned assistant output;
// they never change the protocol.
const (
	// ScriptOK reports a passing test run.
	ScriptOK = "ok"
	// ScriptError reports a failing test run, so tool results carry
	// isError:true and the assistant explains the failure.
	ScriptError = "error"
	// ScriptSlow paces output at a minimum of SlowChunkDelay per chunk so a
	// caller can interrupt a run mid-flight deterministically.
	ScriptSlow = "slow"
)

// SlowChunkDelay is the per-chunk floor used by ScriptSlow.
const SlowChunkDelay = 40 * time.Millisecond

// ---------------------------------------------------------------------------
// Wire types (only the fields the peer needs)
// ---------------------------------------------------------------------------

// command is one inbound JSONL record. Unknown fields are ignored, which keeps
// the peer working when Pi grows commands it does not model.
type command struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	Message            string `json:"message"`
	StreamingBehavior  string `json:"streamingBehavior"`
	Images             []any  `json:"images"`
	Provider           string `json:"provider"`
	ModelID            string `json:"modelId"`
	Level              string `json:"level"`
	EntryID            string `json:"entryId"`
	SessionPath        string `json:"sessionPath"`
	Since              string `json:"since"`
	Name               string `json:"name"`
	CustomInstructions string `json:"customInstructions"`
	Command            string `json:"command"`
}

// response is the reply to exactly one command. Callers correlate on ID; the
// peer may interleave responses with session events freely.
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

// entry is one node of the append-only session tree. The shape mirrors Pi's
// JSONL session file so the same bytes can be replayed by loadEntries.
type entry struct {
	Type       string    `json:"type"`
	ID         string    `json:"id,omitempty"`
	ParentID   *string   `json:"parentId"`
	Timestamp  time.Time `json:"timestamp,omitempty"`
	Message    any       `json:"message,omitempty"`
	Label      string    `json:"label,omitempty"`
	TargetID   string    `json:"targetId,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	CustomType string    `json:"customType,omitempty"`
	Data       any       `json:"data,omitempty"`
}

type message struct {
	Role       string `json:"role"`
	Content    any    `json:"content"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	Usage      *usage `json:"usage,omitempty"`
	StopReason string `json:"stopReason,omitempty"`
	Timestamp  int64  `json:"timestamp"`
}

type usage struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheWrite  int64 `json:"cacheWrite"`
	TotalTokens int64 `json:"totalTokens"`
}

// ---------------------------------------------------------------------------
// Peer state
// ---------------------------------------------------------------------------

// server owns the peer's session state. Every field below mu is guarded by it;
// outMu only serialises writes to the output stream so a run and a response can
// interleave without splitting a record.
type server struct {
	mu      sync.Mutex
	entries []entry
	leaf    string
	nextID  int
	file    string
	cwd     string

	running    bool
	streaming  bool
	compacting bool
	// bigContext records that a run reported a nearly full context, which is
	// what makes the scripted session worth compacting. Pi refuses to compact a
	// session that is still small ("Nothing to compact (session too small)"), so
	// the peer refuses too unless this is set.
	bigContext bool
	// runCancel is closed by abort to signal the in-flight run. It is cleared by
	// the run goroutine when it finishes, never by the abort handler: a cleared
	// channel would make the run believe it was never cancelled. abortSignalled
	// keeps a second abort from closing it twice.
	runCancel      chan struct{}
	abortSignalled bool
	steering       []string
	followUp       []string
	runs           sync.WaitGroup

	provider string
	modelID  string
	level    string
	name     string
	script   string
	delay    time.Duration
	step     int

	outMu    sync.Mutex
	out      *bufio.Writer
	writeErr error
}

func newServer(script string, delay time.Duration, sessionFile string, w io.Writer) (*server, error) {
	if w == nil {
		return nil, errors.New("pitest: nil writer")
	}
	s := &server{
		out:      bufio.NewWriter(w),
		file:     sessionFile,
		cwd:      cwdOf(),
		provider: "fake",
		modelID:  "fake-1",
		level:    "medium",
		script:   script,
		delay:    delay,
	}
	if script == ScriptSlow && delay < SlowChunkDelay {
		s.delay = SlowChunkDelay
	}
	entries, leaf, err := loadEntries(sessionFile)
	if err != nil {
		return nil, err
	}
	s.entries, s.leaf = entries, leaf
	if n := len(entries); n > 0 {
		s.nextID = n
	}
	return s, nil
}

// Serve runs the peer until r reaches EOF, then waits for any in-flight run to
// finish so a caller that closes stdin still receives the tail of the event
// stream (Pi disposes the active runtime before exiting, and so do we).
//
// sessionFile, when non-empty, is both the file the peer appends its tree to
// and, if it already exists, the tree it resumes from. script selects the
// canned output; delay paces each emitted chunk.
//
// Serve never panics on malformed input: an unparsable command is answered with
// a failed "parse" response and the loop continues.
func Serve(script string, delay time.Duration, sessionFile string, r io.Reader, w io.Writer) error {
	if r == nil {
		return errors.New("pitest: nil reader")
	}
	srv, err := newServer(script, delay, sessionFile, w)
	if err != nil {
		return err
	}

	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
			if len(trimmed) > 0 {
				srv.dispatch(trimmed)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			srv.runs.Wait()
			return fmt.Errorf("pitest: read command: %w", readErr)
		}
	}
	// Closing stdin asks for an orderly shutdown.
	srv.runs.Wait()
	if err := srv.writeError(); err != nil {
		return fmt.Errorf("pitest: write event: %w", err)
	}
	return nil
}

// EmitHeader writes the session header line that Pi puts at the top of a
// session file. It is only useful with the fake CLI's -json-header flag.
func EmitHeader(w io.Writer, cwd string) error {
	if cwd == "" {
		cwd = cwdOf()
	}
	b, err := json.Marshal(map[string]any{
		"type": "session", "version": 3, "id": "fake",
		"timestamp": time.Now().UTC(), "cwd": cwd,
	})
	if err != nil {
		return fmt.Errorf("pitest: marshal header: %w", err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("pitest: write header: %w", err)
	}
	return nil
}

func (s *server) dispatch(line []byte) {
	var c command
	if err := json.Unmarshal(line, &c); err != nil {
		s.respond(response{Command: "parse", Success: false,
			Error: "Failed to parse command: " + err.Error()})
		return
	}
	s.handle(c)
}

func (s *server) emit(v any) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if s.writeErr != nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if _, err := s.out.Write(append(b, '\n')); err != nil {
		s.writeErr = err
		return
	}
	if err := s.out.Flush(); err != nil {
		s.writeErr = err
	}
}

func (s *server) writeError() error {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	return s.writeErr
}

func (s *server) respond(r response) {
	if r.Type == "" {
		r.Type = "response"
	}
	s.emit(r)
}

func (s *server) fail(c command, msg string) {
	s.respond(response{ID: c.ID, Command: c.Type, Success: false, Error: msg})
}

func (s *server) ok(c command, data any) {
	s.respond(response{ID: c.ID, Command: c.Type, Success: true, Data: data})
}

// cwdOf reports the process working directory, or "" if it cannot be resolved.
func cwdOf() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// ---------------------------------------------------------------------------
// Session tree
// ---------------------------------------------------------------------------

func (s *server) newID() string {
	s.nextID++
	return fmt.Sprintf("%08x", 0x1000+s.nextID)
}

// appendEntry adds one node; callers must hold mu.
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
	// Best effort, exactly like Pi: a failed write must not kill the agent loop.
	_ = writeSessionFile(s.file, s.entries, s.cwd)
}

// writeSessionFile writes a session header plus every entry as JSONL. It is
// shared by persist and fork so a branched file is byte-compatible with the
// file Pi would have written.
func writeSessionFile(path string, entries []entry, cwd string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("pitest: create session dir: %w", err)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("pitest: create session file: %w", err)
	}
	w := bufio.NewWriter(f)
	header, err := json.Marshal(map[string]any{
		"type": "session", "version": 3,
		"id": filepath.Base(path), "timestamp": time.Now().UTC(), "cwd": cwd,
	})
	if err != nil {
		f.Close()
		return fmt.Errorf("pitest: marshal session header: %w", err)
	}
	if _, err := w.Write(append(header, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("pitest: write session header: %w", err)
	}
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			f.Close()
			return fmt.Errorf("pitest: write session entry: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return fmt.Errorf("pitest: flush session file: %w", err)
	}
	return f.Close()
}

// loadEntries replays a session file written by writeSessionFile. A missing
// file yields an empty tree; malformed lines are skipped rather than fatal.
func loadEntries(path string) ([]entry, string, error) {
	if path == "" {
		return nil, "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("pitest: open session file: %w", err)
	}
	defer f.Close()

	var out []entry
	br := bufio.NewReader(f)
	for {
		line, readErr := br.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var e entry
			if err := json.Unmarshal(trimmed, &e); err == nil && e.Type != "session" && e.ID != "" {
				out = append(out, e)
			}
		}
		if readErr != nil {
			break
		}
	}
	leaf := ""
	if n := len(out); n > 0 {
		leaf = out[n-1].ID
	}
	return out, leaf, nil
}

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
		steering := append([]string{}, s.steering...)
		followUp := append([]string{}, s.followUp...)
		s.mu.Unlock()
		s.emit(map[string]any{"type": "queue_update", "steering": steering, "followUp": followUp})
		s.ok(c, map[string]any{"disposition": "queued"})
	case "follow_up":
		s.mu.Lock()
		s.followUp = append(s.followUp, c.Message)
		steering := append([]string{}, s.steering...)
		followUp := append([]string{}, s.followUp...)
		s.mu.Unlock()
		s.emit(map[string]any{"type": "queue_update", "steering": steering, "followUp": followUp})
		s.ok(c, map[string]any{"disposition": "queued"})
	case "abort":
		s.mu.Lock()
		if s.runCancel != nil && !s.abortSignalled {
			close(s.runCancel)
			s.abortSignalled = true
		}
		s.running = false
		s.streaming = false
		s.steering = nil
		s.followUp = nil
		s.mu.Unlock()
		s.ok(c, nil)
	case "clear_queue":
		s.mu.Lock()
		steering, followUp := s.steering, s.followUp
		s.steering, s.followUp = nil, nil
		s.mu.Unlock()
		s.ok(c, map[string]any{"steering": steering, "followUp": followUp})
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
		s.ok(c, st)
	case "get_messages":
		s.mu.Lock()
		msgs := make([]any, 0, len(s.entries))
		for _, e := range s.entries {
			if e.Type == "message" {
				msgs = append(msgs, e.Message)
			}
		}
		s.mu.Unlock()
		s.ok(c, map[string]any{"messages": msgs})
	case "get_tree":
		s.mu.Lock()
		tree := s.buildTree()
		leaf := s.leaf
		s.mu.Unlock()
		s.ok(c, map[string]any{"tree": tree, "leafId": leaf})
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
			s.fail(c, "unknown cursor")
			return
		}
		s.ok(c, map[string]any{"entries": out, "leafId": leaf})
	case "get_fork_messages":
		s.mu.Lock()
		type forkPoint struct {
			EntryID string `json:"entryId"`
			Text    string `json:"text"`
		}
		points := []forkPoint{}
		for _, e := range s.entries {
			if e.Type != "message" {
				continue
			}
			m, ok := e.Message.(map[string]any)
			if !ok || m["role"] != "user" {
				continue
			}
			points = append(points, forkPoint{EntryID: e.ID, Text: textOf(m["content"])})
		}
		s.mu.Unlock()
		s.ok(c, map[string]any{"messages": points})
	case "fork":
		s.handleFork(c)
	case "clone":
		s.ok(c, map[string]any{"cancelled": false})
	case "switch_session":
		s.ok(c, map[string]any{"cancelled": false})
	case "set_model":
		s.mu.Lock()
		s.provider, s.modelID = c.Provider, c.ModelID
		s.mu.Unlock()
		s.ok(c, map[string]any{"id": c.ModelID, "provider": c.Provider})
	case "get_available_models":
		s.ok(c, map[string]any{"models": []any{
			map[string]any{"id": "fake-1", "provider": "fake", "contextWindow": 200000,
				"maxTokens": 16384, "reasoning": true},
		}})
	case "set_thinking_level":
		s.mu.Lock()
		s.level = c.Level
		s.mu.Unlock()
		s.emit(map[string]any{"type": "thinking_level_changed", "level": c.Level})
		s.ok(c, nil)
	case "get_available_thinking_levels":
		s.ok(c, map[string]any{"levels": []string{"off", "medium", "high"}})
	case "compact":
		s.handleCompact(c)
	case "get_session_stats":
		s.ok(c, s.sessionStats())
	case "set_session_name":
		s.mu.Lock()
		s.name = c.Name
		s.mu.Unlock()
		s.emit(map[string]any{"type": "session_info_changed", "name": c.Name})
		s.ok(c, nil)
	case "set_auto_compaction", "set_auto_retry", "set_steering_mode", "set_follow_up_mode":
		s.ok(c, nil)
	case "abort_retry", "abort_bash":
		s.ok(c, nil)
	case "bash":
		s.ok(c, map[string]any{"output": "fake-bash: " + c.Command, "exitCode": 0,
			"cancelled": false, "truncated": false})
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
		s.ok(c, map[string]any{"text": text})
	case "get_commands":
		s.ok(c, map[string]any{"commands": []any{}})
	default:
		s.fail(c, "unsupported command: "+c.Type)
	}
}

// handleFork branches the session at one user entry, the way real Pi does.
//
// Real Pi forks *inside its own process*: it writes a new session file of its own
// naming and rebinds to it, and it ignores any target path the caller suggests.
// Modelling that here matters — the bridge used to pass a path, real Pi ignored it,
// and the result was a branch with no history while the fake peer's version (which
// honoured the path) made the tests look green.
func (s *server) handleFork(c command) {
	s.mu.Lock()
	idx := -1
	for i, e := range s.entries {
		if e.ID == c.EntryID {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		s.fail(c, "unknown entry")
		return
	}
	kept := append([]entry(nil), s.entries[:idx+1]...)
	var text string
	if m, ok := kept[idx].Message.(map[string]any); ok {
		text = textOf(m["content"])
	}

	// Pi truncates at the *parent* of the chosen user message, so forking at the
	// first message has nothing behind it and Pi starts an empty session. Modelling
	// that matters: it is the case where a branch honestly has no history, and the
	// control plane must not seed a transcript for it.
	parentID := ""
	if s.entries[idx].ParentID != nil {
		parentID = *s.entries[idx].ParentID
	}
	if parentID == "" {
		kept = nil
	} else {
		for i, e := range kept {
			if e.ID == parentID {
				kept = append([]entry(nil), kept[:i+1]...)
				break
			}
		}
	}

	// Pi names the branched file itself and moves the process onto it.
	branched := s.file
	if branched != "" {
		branched = strings.TrimSuffix(branched, ".jsonl") + "-fork-" + strconv.Itoa(len(kept)) + ".jsonl"
	}
	if branched != "" {
		if err := writeSessionFile(branched, kept, s.cwd); err != nil {
			s.mu.Unlock()
			s.fail(c, err.Error())
			return
		}
		s.file = branched
	}
	s.entries = kept
	s.leaf = ""
	if len(kept) > 0 {
		s.leaf = kept[len(kept)-1].ID
	}
	s.persist()
	s.mu.Unlock()
	s.ok(c, map[string]any{"text": text, "cancelled": false, "sessionFile": branched})
}

func (s *server) sessionStats() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	return map[string]any{
		"sessionFile":  s.file,
		"userMessages": 1, "assistantMessages": 1, "toolCalls": tools,
		"totalMessages": len(s.entries),
		"tokens": map[string]any{"input": in, "output": out,
			"cacheRead": 0, "cacheWrite": 0, "total": in + out},
		"cost": 0.01,
		"contextUsage": map[string]any{
			"tokens": in + out, "contextWindow": 200000, "percent": float64(in+out) / 2000.0,
		},
	}
}

// buildTree assembles the parent/child structure. Callers must hold mu.
func (s *server) buildTree() []map[string]any {
	type node struct {
		Entry    entry            `json:"entry"`
		Children []map[string]any `json:"children"`
	}
	nodes := map[string]*node{}
	order := []string{}
	for _, e := range s.entries {
		nodes[e.ID] = &node{Entry: e, Children: []map[string]any{}}
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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

// textOf flattens Pi content (a bare string or an array of text parts).
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

// EnvOr returns the environment value of k or def when it is unset.
func EnvOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// EnvInt returns the integer environment value of k or def when it is unset or
// not an integer.
func EnvInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
