package pi

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// ---------------------------------------------------------------------------
// Event payloads
// ---------------------------------------------------------------------------

// RunStartedPayload opens a run: one prompt through to agent_settled.
type RunStartedPayload struct {
	Trigger string `json:"trigger,omitempty"`
}

// TurnPayload numbers one agent turn inside a run, starting at 1.
type TurnPayload struct {
	Index int `json:"index"`
}

// MessageStartedPayload announces a message skeleton before its first delta.
type MessageStartedPayload struct {
	Role     string `json:"role"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// MessageDeltaPayload is one incremental chunk. Kind is "thinking", "text" or
// "toolArgs"; toolArgs is display-only and must never be parsed as the tool's
// real arguments.
type MessageDeltaPayload struct {
	Kind       string `json:"kind"`
	Index      int    `json:"index"`
	Delta      string `json:"delta"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
}

// MessageFinalPayload is the authoritative message. Blocks replaces anything
// the client built from deltas; Truncated reports that a tool call never became
// executable, and ToolResults are ordered by the assistant's original call
// order rather than by completion time.
type MessageFinalPayload struct {
	Role        string       `json:"role"`
	Text        string       `json:"text,omitempty"`
	Thinking    string       `json:"thinking,omitempty"`
	Blocks      []Block      `json:"blocks"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
	Usage       *Usage       `json:"usage,omitempty"`
	StopReason  string       `json:"stop_reason,omitempty"`
	Provider    string       `json:"provider,omitempty"`
	Model       string       `json:"model,omitempty"`
	Truncated   bool         `json:"truncated,omitempty"`
	// Error carries the provider failure verbatim. A failed call still produces
	// an assistant message with no content, so this is the only thing that can
	// tell the user what happened.
	Error string `json:"error,omitempty"`
}

// ToolStartedPayload announces a tool invocation. Args is the authoritative
// argument object, so the UI can render "what is about to run" before any
// output exists.
type ToolStartedPayload struct {
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	Args       json.RawMessage `json:"args,omitempty"`
}

// ToolProgressPayload is one output increment. It drives the follow terminal
// (PRD §5.4.1) and is cosmetic: a slow client may miss these.
type ToolProgressPayload struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
	Text       string `json:"text,omitempty"`
}

// ToolEndedPayload closes a tool invocation.
type ToolEndedPayload struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Text       string `json:"text,omitempty"`
	Valid      bool   `json:"valid"`
}

// QueuePayload mirrors the composer's steering and follow-up queues.
type QueuePayload struct {
	Steering []string `json:"steering"`
	FollowUp []string `json:"follow_up"`
}

// CompactionPayload is one end of a context compaction. Phase is "start" or
// "end"; the summary and token counts are only present on "end".
type CompactionPayload struct {
	Phase                string `json:"phase"`
	Reason               string `json:"reason,omitempty"`
	Aborted              bool   `json:"aborted,omitempty"`
	Summary              string `json:"summary,omitempty"`
	FirstKeptEntryID     string `json:"first_kept_entry_id,omitempty"`
	TokensBefore         int64  `json:"tokens_before,omitempty"`
	EstimatedTokensAfter int64  `json:"estimated_tokens_after,omitempty"`
}

// ContextUsagePayload is what the context meter renders (PRD §5.1.1 FR-1.8).
// ContextWindow is filled from the session's model when known.
type ContextUsagePayload struct {
	Usage         Usage   `json:"usage"`
	ContextWindow int64   `json:"context_window,omitempty"`
	Percent       float64 `json:"percent,omitempty"`
}

// RetryPayload reports an upstream retry. Phase is "start" or "end".
type RetryPayload struct {
	Phase       string `json:"phase"`
	Attempt     int    `json:"attempt,omitempty"`
	MaxAttempts int    `json:"max_attempts,omitempty"`
	DelayMS     int64  `json:"delay_ms,omitempty"`
	Error       string `json:"error,omitempty"`
	Success     bool   `json:"success,omitempty"`
}

// AbortedPayload explains why a run was cancelled. Pi does not say who
// cancelled it, so Reason is always "aborted" unless a caller enriches it.
type AbortedPayload struct {
	Reason string `json:"reason"`
}

// SettledPayload is the run terminal. Aborted distinguishes a user interrupt
// from a normal completion.
//
// The token totals are the run's own consumption, accumulated from the
// authoritative usage on each assistant message_end — not a session total. The
// control plane stores them on the run row so the context meter and the cost
// display never have to replay the transcript.
type SettledPayload struct {
	Aborted      bool   `json:"aborted"`
	StopReason   string `json:"stop_reason,omitempty"`
	TokensIn     int64  `json:"tokens_in"`
	TokensOut    int64  `json:"tokens_out"`
	TokensCacheR int64  `json:"tokens_cache_read"`
	TokensCacheW int64  `json:"tokens_cache_write"`
}

// PlatformErrorPayload is a bridge- or extension-level failure that is not part
// of the conversation itself.
type PlatformErrorPayload struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}

// PtyStreamPayload carries raw terminal bytes from a direct bash execution.
type PtyStreamPayload struct {
	Text string `json:"text"`
}

// ---------------------------------------------------------------------------
// Normalizer
// ---------------------------------------------------------------------------

// State is the normalizer's view of the agent, derived purely from the event
// stream so no extra RPC round-trip is needed to answer "is it running?".
type State struct {
	SessionID  string
	RunID      string
	Running    bool
	Streaming  bool
	Compacting bool
	QueueDepth int
	Turn       int
	Provider   string
	Model      string
	LastSeq    int64
}

// Normalizer turns Pi records into contracts.Event values.
//
// Seq is monotonic per session and starts at 1; it doubles as the SSE event id,
// so a reconnect can replay from the last one it saw. TS is always control-plane
// time, because wall clocks inside a resumed sandbox jump (PRD §7.5). RunID is
// set for every event emitted while a run is in flight.
//
// agent_end maps to nothing at all: it fires for retries, compaction and
// steering too, so only agent_settled may be treated as completion.
//
// Event.EntryID and Event.ParentID are deliberately left empty. Pi's event
// stream does not report the session-tree id it assigned to a message; the
// control plane learns those ids from get_entries/get_tree, which is also what
// a reconnecting client uses (PRD §7.4.5).
type Normalizer struct {
	mu        sync.Mutex
	sessionID string
	kind      contracts.SessionKind
	seq       int64
	state     State
	pending   string
	asm       *StreamAssembler
	now       func() time.Time
	window    int64
	// Per-run token accumulators. Reset on agent_start and reported in the
	// run.settled payload so the control plane can store cost without replaying
	// the whole transcript (PRD §5.1.1 FR-1.8).
	runIn     int64
	runOut    int64
	runCacheR int64
	runCacheW int64
	// interrupted is set when the user stops a run (Runtime.Interrupt) and
	// cleared when the next run starts. Pi reports the abort as a normal provider
	// failure on the assistant message ("This operation was aborted", stopReason
	// "error"), and only agent_settled.aborted says it was a stop; without this
	// flag the transcript kept a 模型调用失败 box for pressing Stop.
	interrupted bool
}

var runCounter atomic.Uint64

// NoteInterrupt records that the user asked to stop the current run.
//
// The abort reaches Pi as a normal RPC call, so the only evidence in the event
// stream is the provider failure Pi fabricates while unwinding. Marking the run
// here is what lets the transcript distinguish "the user stopped this" from "the
// model call failed" — the two are identical in the wire records.
func (n *Normalizer) NoteInterrupt() {
	n.mu.Lock()
	n.interrupted = true
	n.mu.Unlock()
}

// errorForMessage reports the provider failure a message carries, or "" when
// there is none.
//
// Two shapes mean "the user stopped this", not "the call failed":
//   - a message with stopReason "aborted";
//   - an error reported after Interrupt was called. Real Pi surfaces the stop as
//     stopReason "error" with "This operation was aborted", which used to render
//     as 模型调用失败 with advice to check the provider's key (QA-14, MAJOR).
//
// `run.aborted` is the signal for a stop; the transcript stays clean.
func (n *Normalizer) errorForMessage(msg messageWire) string {
	if msg.StopReason == "aborted" {
		return ""
	}
	if n.interrupted {
		return ""
	}
	return msg.ErrorMessage
}

// NewNormalizer returns a normalizer for one session.
func NewNormalizer(sessionID string, kind contracts.SessionKind) *Normalizer {
	if kind == "" {
		kind = contracts.KindMain
	}
	return &Normalizer{
		sessionID: sessionID,
		kind:      kind,
		asm:       NewStreamAssembler(),
		now:       time.Now,
		state:     State{SessionID: sessionID},
	}
}

// Assembler exposes the stream assembler, which holds the blocks and tool
// results of the message currently being streamed.
func (n *Normalizer) Assembler() *StreamAssembler { return n.asm }

// SetContextWindow records the model's context window so context.usage can
// report a percentage.
func (n *Normalizer) SetContextWindow(tokens int64) {
	n.mu.Lock()
	n.window = tokens
	n.mu.Unlock()
}

// ReserveRun pre-allocates the id for the run a prompt is about to start, so
// the HTTP layer can answer with a run id before the first event arrives.
func (n *Normalizer) ReserveRun() string {
	id := fmt.Sprintf("run_%x_%d", time.Now().UnixNano(), runCounter.Add(1))
	n.mu.Lock()
	n.pending = id
	n.mu.Unlock()
	return id
}

// ClearReservedRun withdraws a reservation that did not turn into a run, so it
// cannot be attached to some later agent_start.
func (n *Normalizer) ClearReservedRun(id string) {
	if id == "" {
		return
	}
	n.mu.Lock()
	if n.pending == id {
		n.pending = ""
	}
	n.mu.Unlock()
}

// State returns a snapshot of the derived agent state.
func (n *Normalizer) State() State {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state
}

// LastSeq returns the seq of the newest emitted event.
func (n *Normalizer) LastSeq() int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.seq
}

// Translate implements Translator. An unparsable record yields an error and no
// events; an unknown event type yields no events and no error, so a newer Pi
// cannot break the bridge.
func (n *Normalizer) Translate(raw []byte) ([]contracts.Event, error) {
	var env record
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("pi: normalize: %w", err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()

	switch env.Type {
	case "agent_start":
		if n.state.RunID == "" {
			n.state.RunID = n.pending
			if n.state.RunID == "" {
				n.state.RunID = fmt.Sprintf("run_%x_%d", time.Now().UnixNano(), runCounter.Add(1))
			}
			n.pending = ""
		}
		n.state.Running = true
		n.state.Streaming = true
		n.state.Turn = 0
		// A new run starts with a clean accumulator: the settle payload reports
		// what this run cost, not what the session has cost so far. The pending
		// stop is cleared too: it only explains the failures of the run it
		// interrupted.
		n.runIn, n.runOut, n.runCacheR, n.runCacheW = 0, 0, 0, 0
		n.interrupted = false
		n.asm.Reset()
		return n.emit(contracts.EvRunStarted, RunStartedPayload{}), nil

	case "turn_start":
		n.state.Turn++
		return n.emit(contracts.EvTurnStarted, TurnPayload{Index: n.state.Turn}), nil

	case "turn_end":
		return n.emit(contracts.EvTurnEnded, TurnPayload{Index: n.state.Turn}), nil

	case "message_start":
		var w messageStartWire
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pi: normalize message_start: %w", err)
		}
		n.asm.OnMessageStart(w.Message)
		if w.Message.Provider != "" {
			n.state.Provider = w.Message.Provider
		}
		if w.Message.Model != "" {
			n.state.Model = w.Message.Model
		}
		return n.emit(contracts.EvMsgStarted, MessageStartedPayload{
			Role:     w.Message.Role,
			Provider: w.Message.Provider,
			Model:    w.Message.Model,
		}), nil

	case "message_update":
		var w messageUpdateWire
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pi: normalize message_update: %w", err)
		}
		n.asm.OnUpdate(w.Event, w.Usage)
		return n.deltaEvents(w.Event), nil

	case "message_end":
		var w messageEndWire
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pi: normalize message_end: %w", err)
		}
		blocks := n.asm.OnMessageEnd(w.Message)
		if w.Message.Provider != "" {
			n.state.Provider = w.Message.Provider
		}
		if w.Message.Model != "" {
			n.state.Model = w.Message.Model
		}
		payload := MessageFinalPayload{
			Role:        w.Message.Role,
			Text:        joinTextBlocks(blocks),
			Thinking:    joinThinkingBlocks(blocks),
			Blocks:      blocks,
			ToolResults: n.asm.OrderedResults(),
			StopReason:  w.Message.StopReason,
			Provider:    w.Message.Provider,
			Model:       w.Message.Model,
			Truncated:   n.asm.HasTruncatedToolCall(),
			Error:       n.errorForMessage(w.Message),
		}
		usage := w.Message.Usage
		if usage == nil {
			u := n.asm.Usage()
			usage = &u
		}
		payload.Usage = usage
		events := n.emit(contracts.EvMsgFinal, payload)
		if usage != nil {
			// Only assistant turns spend tokens; adding a tool-result message's
			// (absent) usage would be harmless but pointless.
			if w.Message.Role == "assistant" {
				n.runIn += usage.Input
				n.runOut += usage.Output
				n.runCacheR += usage.CacheRead
				n.runCacheW += usage.CacheWrite
			}
			events = append(events, n.usageEvent(*usage)...)
		}
		return events, nil

	case "tool_execution_start":
		var w toolStartWire
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pi: normalize tool_execution_start: %w", err)
		}
		n.asm.OnToolStart(w)
		return n.emit(contracts.EvToolStarted, ToolStartedPayload{
			ToolCallID: w.ToolCallID,
			Name:       w.ToolName,
			Args:       rawJSON(w.Args),
		}), nil

	case "tool_execution_update":
		var w toolUpdateWire
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pi: normalize tool_execution_update: %w", err)
		}
		text := ""
		if w.PartialResult != nil {
			text = flattenContent(w.PartialResult.Content)
		}
		return n.emit(contracts.EvToolProgress, ToolProgressPayload{
			ToolCallID: w.ToolCallID,
			Name:       w.ToolName,
			Text:       text,
		}), nil

	case "tool_execution_end":
		var w toolEndWire
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pi: normalize tool_execution_end: %w", err)
		}
		n.asm.OnToolEnd(w)
		res, _ := n.asm.Result(w.ToolCallID)
		return n.emit(contracts.EvToolEnded, ToolEndedPayload{
			ToolCallID: w.ToolCallID,
			Name:       w.ToolName,
			IsError:    w.IsError,
			DurationMS: w.DurationMS,
			Text:       res.Text,
			Valid:      res.Valid,
		}), nil

	case "queue_update":
		var w queueWire
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pi: normalize queue_update: %w", err)
		}
		n.state.QueueDepth = len(w.Steering) + len(w.FollowUp)
		return n.emit(contracts.EvQueueChanged, QueuePayload{
			Steering: w.Steering,
			FollowUp: w.FollowUp,
		}), nil

	case "compaction_start":
		var w compactionWire
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pi: normalize compaction_start: %w", err)
		}
		n.state.Compacting = true
		return n.emit(contracts.EvCompaction, CompactionPayload{
			Phase:  "start",
			Reason: w.Reason,
		}), nil

	case "compaction_end":
		var w compactionWire
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pi: normalize compaction_end: %w", err)
		}
		n.state.Compacting = false
		payload := CompactionPayload{
			Phase:   "end",
			Reason:  w.Reason,
			Aborted: w.Aborted,
		}
		var events []contracts.Event
		if w.Result != nil {
			payload.Summary = w.Result.Summary
			payload.FirstKeptEntryID = w.Result.FirstKeptEntryID
			payload.TokensBefore = w.Result.TokensBefore
			payload.EstimatedTokensAfter = w.Result.EstimatedTokensAfter
		}
		events = n.emit(contracts.EvCompaction, payload)
		if w.Result != nil && w.Result.Usage != nil {
			events = append(events, n.usageEvent(*w.Result.Usage)...)
		}
		return events, nil

	case "auto_retry_start", "auto_retry_end":
		var w retryWire
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pi: normalize %s: %w", env.Type, err)
		}
		phase := "start"
		if env.Type == "auto_retry_end" {
			phase = "end"
		}
		return n.emit(contracts.EvRunRetry, RetryPayload{
			Phase:       phase,
			Attempt:     w.Attempt,
			MaxAttempts: w.MaxAttempts,
			DelayMS:     w.DelayMS,
			Error:       w.Error,
			Success:     w.Success,
		}), nil

	case "agent_settled":
		var w settledWire
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pi: normalize agent_settled: %w", err)
		}
		var events []contracts.Event
		if w.Aborted {
			// Pi reports the abort only through agent_settled, so the
			// run.aborted marker is synthesised here, before the terminal.
			events = append(events, n.emit(contracts.EvRunAborted, AbortedPayload{Reason: "aborted"})...)
		}
		events = append(events, n.emit(contracts.EvRunSettled, SettledPayload{
			Aborted:      w.Aborted,
			TokensIn:     n.runIn,
			TokensOut:    n.runOut,
			TokensCacheR: n.runCacheR,
			TokensCacheW: n.runCacheW,
		})...)
		n.state.Running = false
		n.state.Streaming = false
		n.state.Compacting = false
		n.state.QueueDepth = 0
		n.state.RunID = ""
		n.pending = ""
		return events, nil

	case "agent_end":
		// Deliberately silent: see the type doc comment.
		return nil, nil

	case "extension_error":
		var w extensionErrorWire
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pi: normalize extension_error: %w", err)
		}
		msg := w.Message
		if msg == "" {
			msg = "extension error"
		}
		source := "extension"
		if w.Extension != "" {
			source = "extension:" + w.Extension
		}
		return n.emit(contracts.EvPlatformErr, PlatformErrorPayload{
			Source:  source,
			Message: msg,
		}), nil

	case "bash_execution_update":
		var w struct {
			Chunk string `json:"chunk"`
		}
		_ = json.Unmarshal(raw, &w)
		return n.emit(contracts.EvPtyStream, PtyStreamPayload{Text: w.Chunk}), nil

	default:
		return nil, nil
	}
}

// deltaEvents maps one assistant delta onto message.delta. Only *_delta records
// carry a payload worth forwarding; *_start and *_end are folded into the
// assembler and surface through message.final.
func (n *Normalizer) deltaEvents(ev assistantEvent) []contracts.Event {
	var kind string
	switch ev.Type {
	case "thinking_delta":
		kind = "thinking"
	case "text_delta":
		kind = "text"
	case "toolcall_delta":
		kind = "toolArgs"
	default:
		return nil
	}
	if ev.Delta == "" {
		return nil
	}
	return n.emit(contracts.EvMsgDelta, MessageDeltaPayload{
		Kind:       kind,
		Index:      ev.ContentIndex,
		Delta:      ev.Delta,
		ToolCallID: ev.ID,
		ToolName:   ev.ToolName,
	})
}

// usageEvent reports the context meter reading.
func (n *Normalizer) usageEvent(u Usage) []contracts.Event {
	payload := ContextUsagePayload{Usage: u, ContextWindow: n.window}
	if n.window > 0 {
		payload.Percent = float64(u.Input+u.Output) / float64(n.window) * 100
	}
	return n.emit(contracts.EvContextUsage, payload)
}

// emit stamps one event with the session envelope. Callers hold mu.
func (n *Normalizer) emit(eventType string, payload any) []contracts.Event {
	n.seq++
	ev := contracts.Event{
		Seq:       n.seq,
		SessionID: n.sessionID,
		BranchID:  n.sessionID,
		Kind:      n.kind,
		Type:      eventType,
		TS:        n.now().UTC(),
		RunID:     n.state.RunID,
	}
	if payload != nil {
		if encoded, err := json.Marshal(payload); err == nil {
			ev.Payload = encoded
		}
	}
	n.state.LastSeq = n.seq
	return []contracts.Event{ev}
}

func joinTextBlocks(blocks []Block) string {
	var out string
	for _, b := range blocks {
		if b.Kind == BlockText {
			out += b.Text
		}
	}
	return out
}

func joinThinkingBlocks(blocks []Block) string {
	var out string
	for _, b := range blocks {
		if b.Kind == BlockThinking {
			out += b.Text
		}
	}
	return out
}
