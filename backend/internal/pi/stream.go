package pi

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// BlockKind is the kind of one assistant content block.
type BlockKind string

const (
	// BlockThinking is a reasoning block (Pi: "thinking").
	BlockThinking BlockKind = "thinking"
	// BlockText is ordinary visible output.
	BlockText BlockKind = "text"
	// BlockToolCall is a tool invocation the assistant asked for.
	BlockToolCall BlockKind = "toolCall"
)

// ToolCall is one assistant tool invocation.
//
// Arguments is only populated from an authoritative source: a toolcall_end
// record or the final message content. Valid is false when no authoritative
// arguments arrived, which is how a call truncated by the model's output limit
// is recognised — such a call must never be executed or handed downstream as a
// working invocation (PRD §7.4.3).
type ToolCall struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// ArgsText is the raw argument text accumulated from toolcall_delta. It is
	// for display only ("the model is still writing the arguments") and is
	// never parsed, because it may be incomplete JSON.
	ArgsText string `json:"args_text,omitempty"`
	Valid    bool   `json:"valid"`
	Index    int    `json:"index"`
}

// Block is one assembled content block of an assistant message.
type Block struct {
	Kind  BlockKind `json:"kind"`
	Index int       `json:"index"`
	Text  string    `json:"text,omitempty"`
	Tool  *ToolCall `json:"tool_call,omitempty"`
	// Truncated marks a tool call that never received authoritative arguments.
	Truncated bool `json:"truncated,omitempty"`
}

// ToolResult is one tool's reported outcome, positioned by the index of the
// assistant block that requested it so results can be emitted in call order
// even when the calls complete out of order.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
	Index      int    `json:"index"`
	IsError    bool   `json:"is_error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Text       string `json:"text,omitempty"`
	// Valid is false when the originating call was truncated, so a consumer
	// knows the result cannot be attributed to a well-formed invocation.
	Valid bool `json:"valid"`
}

// blockBuf is the mutable accumulator behind one Block.
type blockBuf struct {
	kind      BlockKind
	index     int
	text      strings.Builder
	args      strings.Builder
	tool      *ToolCall
	ended     bool
	truncated bool
}

// block materialises the accumulator into its immutable form.
func (b *blockBuf) block() Block {
	blk := Block{Kind: b.kind, Index: b.index, Text: b.text.String(), Truncated: b.truncated}
	if b.kind == BlockToolCall {
		if b.tool == nil {
			// toolcall_start never arrived: nothing at all to hand downstream.
			blk.Truncated = true
			return blk
		}
		tc := *b.tool
		if tc.ID == "" {
			tc.ID = "call_truncated_" + strconv.Itoa(b.index)
		}
		blk.Tool = &tc
		if !tc.Valid {
			blk.Truncated = true
		}
	}
	return blk
}

// StreamAssembler rebuilds an assistant message from Pi's delta-only
// message_update stream.
//
// message_update carries no partial snapshot, so blocks are accumulated per
// contentIndex; text_end, thinking_end and toolcall_end replace the block with
// the authoritative content, and message_end replaces the whole message. The
// assembler is the only place that knows which tool calls are safe to act on.
//
// It is safe for concurrent use, but Pi delivers records in order, so in
// practice only the bridge's read loop touches it.
type StreamAssembler struct {
	mu      sync.Mutex
	blocks  map[int]*blockBuf
	usage   Usage
	results map[string]ToolResult
	// callIndex maps a toolCallId to the assistant block index that requested
	// it, which survives across messages so a result can be ordered.
	callIndex map[string]int
	arrival   int
}

// NewStreamAssembler returns an empty assembler.
func NewStreamAssembler() *StreamAssembler {
	return &StreamAssembler{
		blocks:    map[int]*blockBuf{},
		results:   map[string]ToolResult{},
		callIndex: map[string]int{},
	}
}

// Reset drops the current message. Tool results are kept, because results can
// arrive after the message that requested them ends.
func (s *StreamAssembler) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocks = map[int]*blockBuf{}
	s.usage = Usage{}
}

// OnMessageStart begins a new message: any partially assembled state belongs to
// the previous one and can be discarded.
func (s *StreamAssembler) OnMessageStart(m messageWire) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocks = map[int]*blockBuf{}
	s.usage = Usage{}
}

// OnUpdate folds one message_update into the accumulator. usage may be nil;
// when present it is cumulative and may stay zero until the message ends, so it
// is only a hint.
func (s *StreamAssembler) OnUpdate(ev assistantEvent, u *Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u != nil && (u.Input != 0 || u.Output != 0 || u.CacheRead != 0 || u.CacheWrite != 0 || u.Total != 0) {
		s.usage = *u
	}
	switch ev.Type {
	case "thinking_start":
		b := s.bufferLocked(ev.ContentIndex, BlockThinking)
		b.text.Reset()
	case "thinking_delta":
		s.bufferLocked(ev.ContentIndex, BlockThinking).text.WriteString(ev.Delta)
	case "thinking_end":
		b := s.bufferLocked(ev.ContentIndex, BlockThinking)
		b.text.Reset()
		b.text.WriteString(ev.Content)
		b.ended = true

	case "text_start":
		b := s.bufferLocked(ev.ContentIndex, BlockText)
		b.text.Reset()
	case "text_delta":
		s.bufferLocked(ev.ContentIndex, BlockText).text.WriteString(ev.Delta)
	case "text_end":
		b := s.bufferLocked(ev.ContentIndex, BlockText)
		b.text.Reset()
		b.text.WriteString(ev.Content)
		b.ended = true

	case "toolcall_start":
		b := s.bufferLocked(ev.ContentIndex, BlockToolCall)
		b.tool = &ToolCall{ID: ev.ID, Name: ev.ToolName, Index: ev.ContentIndex}
		b.args.Reset()
		s.noteCallLocked(ev.ID, ev.ContentIndex)
	case "toolcall_delta":
		s.bufferLocked(ev.ContentIndex, BlockToolCall).args.WriteString(ev.Delta)
	case "toolcall_end":
		b := s.bufferLocked(ev.ContentIndex, BlockToolCall)
		tc := &ToolCall{
			ID:       ev.ToolCall.ID,
			Name:     ev.ToolCall.Name,
			Index:    ev.ContentIndex,
			ArgsText: b.args.String(),
		}
		if tc.ID == "" && b.tool != nil {
			tc.ID = b.tool.ID
		}
		if tc.Name == "" && b.tool != nil {
			tc.Name = b.tool.Name
		}
		tc.Arguments = rawJSON(ev.ToolCall.Arguments)
		if tc.Arguments == nil && strings.TrimSpace(b.args.String()) == "" {
			// toolcall_end is Pi's authoritative "this call is complete" signal,
			// so an argument-less call is a valid no-argument invocation.
			tc.Arguments = json.RawMessage("{}")
		}
		tc.Valid = tc.Arguments != nil && json.Valid(tc.Arguments)
		b.tool = tc
		b.ended = true
		s.noteCallLocked(tc.ID, ev.ContentIndex)
	}
}

// OnMessageEnd finishes the message. When the record carries a content array it
// is authoritative and replaces everything assembled from deltas; otherwise the
// accumulated blocks stand but every unfinished tool call is marked truncated.
//
// The returned slice is a snapshot ordered by content index.
func (s *StreamAssembler) OnMessageEnd(m messageWire) []Block {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.Usage != nil {
		s.usage = *m.Usage
	}
	if auth := contentBlocks(m.Content); auth != nil {
		s.replaceLocked(auth)
	}
	s.finalizeLocked()
	return s.blocksLocked()
}

// replaceLocked rebuilds the block map from an authoritative content array.
// Tool calls that vanished from the array are retained as truncated so the UI
// can show that a call was cut off instead of silently losing it.
func (s *StreamAssembler) replaceLocked(auth []contentBlock) {
	next := make(map[int]*blockBuf, len(auth))
	seenCalls := make(map[string]bool, len(auth))
	for i, blk := range auth {
		b := &blockBuf{index: i, ended: true}
		switch blk.Type {
		case "thinking":
			b.kind = BlockThinking
			b.text.WriteString(blk.Thinking)
		case "text":
			b.kind = BlockText
			b.text.WriteString(blk.Text)
		case "toolCall":
			b.kind = BlockToolCall
			tc := &ToolCall{ID: blk.ID, Name: blk.Name, Index: i}
			tc.Arguments = rawJSON(blk.Arguments)
			if tc.Arguments == nil {
				tc.Arguments = json.RawMessage("{}")
			}
			tc.Valid = json.Valid(tc.Arguments)
			b.tool = tc
			if prev, ok := s.blocks[i]; ok && prev.args.Len() > 0 {
				tc.ArgsText = prev.args.String()
			}
			if tc.ID != "" {
				seenCalls[tc.ID] = true
			}
			s.noteCallLocked(tc.ID, i)
		default:
			// Images and unknown block kinds are not rendered as text blocks.
			continue
		}
		next[i] = b
	}
	for i, prev := range s.blocks {
		if prev.kind != BlockToolCall {
			continue
		}
		if prev.tool != nil && prev.tool.ID != "" && seenCalls[prev.tool.ID] {
			continue
		}
		if _, exists := next[i]; exists {
			continue
		}
		prev.truncated = true
		if prev.tool != nil {
			prev.tool.Valid = false
		}
		next[i] = prev
	}
	s.blocks = next
}

// finalizeLocked flags every tool call that never received authoritative
// arguments. Such a call is never executable.
func (s *StreamAssembler) finalizeLocked() {
	for _, b := range s.blocks {
		if b.kind != BlockToolCall {
			continue
		}
		if b.tool == nil || !b.ended || !b.tool.Valid {
			b.truncated = true
		}
	}
}

// bufferLocked returns the accumulator for one content index, creating it with
// the given kind when absent. Callers must hold mu.
func (s *StreamAssembler) bufferLocked(index int, kind BlockKind) *blockBuf {
	if b, ok := s.blocks[index]; ok {
		return b
	}
	b := &blockBuf{kind: kind, index: index}
	s.blocks[index] = b
	return b
}

// noteCallLocked records where a tool call sits so its result can be ordered.
func (s *StreamAssembler) noteCallLocked(id string, index int) {
	if id == "" {
		return
	}
	s.callIndex[id] = index
}

// Blocks returns the assembled blocks ordered by content index.
func (s *StreamAssembler) Blocks() []Block {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blocksLocked()
}

func (s *StreamAssembler) blocksLocked() []Block {
	out := make([]Block, 0, len(s.blocks))
	for _, b := range s.blocks {
		out = append(out, b.block())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	if out == nil {
		return []Block{}
	}
	return out
}

// Usage returns the last usage Pi reported, zeroed at the start of a message.
func (s *StreamAssembler) Usage() Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage
}

// Text concatenates the visible text blocks.
func (s *StreamAssembler) Text() string {
	var b strings.Builder
	for _, blk := range s.Blocks() {
		if blk.Kind == BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// Thinking concatenates the reasoning blocks.
func (s *StreamAssembler) Thinking() string {
	var b strings.Builder
	for _, blk := range s.Blocks() {
		if blk.Kind == BlockThinking {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// ToolCalls returns only the calls that are safe to act on, in message order. A
// truncated call never appears here.
func (s *StreamAssembler) ToolCalls() []ToolCall {
	var out []ToolCall
	for _, blk := range s.Blocks() {
		if blk.Kind != BlockToolCall || blk.Tool == nil || blk.Truncated || !blk.Tool.Valid {
			continue
		}
		out = append(out, *blk.Tool)
	}
	return out
}

// OnToolStart notes that a tool began executing.
func (s *StreamAssembler) OnToolStart(ev toolStartWire) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.callIndex[ev.ToolCallID]; !ok && ev.ToolCallID != "" {
		s.arrival++
		s.callIndex[ev.ToolCallID] = (1 << 20) + s.arrival
	}
}

// OnToolUpdate records a progress tick. The partial result is not stored on the
// result entry: progress is cosmetic and may be dropped, so it is carried by
// the event payload instead.
func (s *StreamAssembler) OnToolUpdate(ev toolUpdateWire) {}

// OnToolEnd records a tool's outcome. Results may arrive in any order, so the
// ordering is applied by OrderedResults rather than here.
func (s *StreamAssembler) OnToolEnd(ev toolEndWire) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := ToolResult{
		ToolCallID: ev.ToolCallID,
		Name:       ev.ToolName,
		IsError:    ev.IsError,
		DurationMS: ev.DurationMS,
		Valid:      true,
	}
	if ev.Result != nil {
		res.Text = flattenContent(ev.Result.Content)
	}
	idx, ok := s.callIndex[ev.ToolCallID]
	if !ok {
		s.arrival++
		idx = (1 << 20) + s.arrival
		s.callIndex[ev.ToolCallID] = idx
	}
	res.Index = idx
	if b, ok := s.blocks[idx]; ok && b.kind == BlockToolCall && (b.truncated || b.tool == nil || !b.tool.Valid) {
		res.Valid = false
	}
	s.results[ev.ToolCallID] = res
}

// Result returns one tool result by call id.
func (s *StreamAssembler) Result(id string) (ToolResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.results[id]
	return r, ok
}

// OrderedResults returns every recorded tool result in the assistant's original
// call order, which is what the UI renders — not the order the tools happened
// to finish in.
func (s *StreamAssembler) OrderedResults() []ToolResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ToolResult, 0, len(s.results))
	for _, r := range s.results {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Index != out[j].Index {
			return out[i].Index < out[j].Index
		}
		return out[i].ToolCallID < out[j].ToolCallID
	})
	if out == nil {
		return []ToolResult{}
	}
	return out
}

// HasTruncatedToolCall reports whether the last message contained a tool call
// that never became executable.
func (s *StreamAssembler) HasTruncatedToolCall() bool {
	for _, blk := range s.Blocks() {
		if blk.Kind == BlockToolCall && blk.Truncated {
			return true
		}
	}
	return false
}
