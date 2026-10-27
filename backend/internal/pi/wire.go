// Package pi is the bridge between PiForge and a Pi coding-agent process.
//
// One Bridge owns exactly one `pi --mode rpc` child process. The process speaks
// JSONL over stdin/stdout with strict LF framing (PRD §2.1): a record ends at
// '\n' and nowhere else, because U+2028 and U+2029 are legal inside JSON
// strings and Node's readline would wrongly split on them.
//
// The package is split so each concern can be tested on its own:
//
//   - bridge.go   framing, command/response correlation, fan-out, lifecycle
//   - wire.go     the subset of Pi's wire format this bridge models
//   - stream.go   reconstruction of streamed assistant messages
//   - normalize.go mapping Pi events onto contracts.Event
//   - runtime.go  contracts.AgentRuntime, one process per session
//
// Two invariants are load-bearing and easy to get wrong:
//
//  1. agent_settled is the only completion signal. agent_end also fires for
//     retries, compaction and steering, so treating it as "done" reports a
//     finished run while the agent is still working.
//  2. The stdout read loop must never block. A slow consumer may lose cosmetic
//     progress events but must never lose message.final or run.settled.
package pi

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Usage mirrors Pi's token accounting for one message or one run. During
// streaming these numbers are cumulative and may stay zero until the message
// ends, so they must not be used as a live counter.
type Usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Total      int64 `json:"totalTokens"`
}

// Response is the reply to exactly one Command. Pi processes commands
// asynchronously, so replies are matched by ID and never by order.
type Response struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// record is the envelope every stdout line shares: enough to tell a response
// from a session event and to route a response to its waiter.
type record struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// messageWire is a Pi message: a user turn, an assistant turn or a tool result.
// Content is either a bare string (user turns) or an array of blocks, so it is
// kept raw and flattened by the helpers below.
type messageWire struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Provider   string          `json:"provider"`
	Model      string          `json:"model"`
	Usage      *Usage          `json:"usage"`
	StopReason string          `json:"stopReason"`
	Timestamp  int64           `json:"timestamp"`
}

// contentBlock is one element of a message's content array.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// messageStartWire is the payload of a message_start record.
type messageStartWire struct {
	Message messageWire `json:"message"`
}

// messageEndWire is the payload of a message_end record.
type messageEndWire struct {
	Message messageWire `json:"message"`
}

// assistantEvent is one delta of a streamed assistant message. message_update
// carries no partial snapshot (PRD §2.1), so every field that matters arrives
// here and the caller has to accumulate.
type assistantEvent struct {
	Type         string   `json:"type"`
	ContentIndex int      `json:"contentIndex"`
	Delta        string   `json:"delta"`
	Content      string   `json:"content"`
	ID           string   `json:"id"`
	ToolName     string   `json:"toolName"`
	ToolCall     toolCall `json:"toolCall"`
}

// messageUpdateWire is the payload of a message_update record.
type messageUpdateWire struct {
	Usage *Usage         `json:"usage"`
	Event assistantEvent `json:"assistantMessageEvent"`
}

// toolCall is the authoritative tool invocation attached to toolcall_end and to
// the final message content.
type toolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// toolResultWire is a tool's reported output. Content is either a string or an
// array of blocks depending on the tool, so it stays raw.
type toolResultWire struct {
	Content json.RawMessage `json:"content"`
	Details json.RawMessage `json:"details"`
}

// toolStartWire is the payload of tool_execution_start.
type toolStartWire struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
}

// toolUpdateWire is the payload of tool_execution_update. partialResult holds
// the output so far, which is what the follow terminal renders (PRD §5.4.1).
type toolUpdateWire struct {
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	PartialResult *toolResultWire `json:"partialResult"`
}

// toolEndWire is the payload of tool_execution_end.
type toolEndWire struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Result     *toolResultWire `json:"result"`
	IsError    bool            `json:"isError"`
	DurationMS int64           `json:"durationMs"`
}

// queueWire is the payload of queue_update: the texts waiting to be steered in
// or run as the next turn.
type queueWire struct {
	Steering []string `json:"steering"`
	FollowUp []string `json:"followUp"`
}

// compactionWire is the payload of compaction_start and compaction_end.
type compactionWire struct {
	Reason  string `json:"reason"`
	Aborted bool   `json:"aborted"`
	Result  *struct {
		Summary              string `json:"summary"`
		FirstKeptEntryID     string `json:"firstKeptEntryId"`
		TokensBefore         int64  `json:"tokensBefore"`
		EstimatedTokensAfter int64  `json:"estimatedTokensAfter"`
		Usage                *Usage `json:"usage"`
	} `json:"result"`
}

// retryWire is the payload of auto_retry_start and auto_retry_end.
type retryWire struct {
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"maxAttempts"`
	DelayMS     int64  `json:"delayMs"`
	Error       string `json:"errorMessage"`
	Success     bool   `json:"success"`
}

// settledWire is the payload of agent_settled.
type settledWire struct {
	Aborted bool `json:"aborted"`
}

// extensionErrorWire is the payload of extension_error.
type extensionErrorWire struct {
	Extension string `json:"extensionPath"`
	Message   string `json:"error"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// flattenContent renders Pi content (a bare string or an array of blocks) as
// plain text. Unknown block kinds are skipped rather than stringified, so a
// preview never turns into Go struct noise.
func flattenContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
		return ""
	case '[':
		var blocks []contentBlock
		if err := json.Unmarshal(trimmed, &blocks); err != nil {
			return ""
		}
		var b strings.Builder
		for _, blk := range blocks {
			switch blk.Type {
			case "text":
				b.WriteString(blk.Text)
			case "thinking":
				b.WriteString(blk.Thinking)
			}
		}
		return b.String()
	default:
		return ""
	}
}

// contentBlocks decodes a message's content array. A bare string becomes a
// single text block so user and assistant turns can share one renderer.
func contentBlocks(raw json.RawMessage) []contentBlock {
	if len(raw) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil
		}
		return []contentBlock{{Type: "text", Text: s}}
	}
	var blocks []contentBlock
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return nil
	}
	return blocks
}

// rawJSON normalises an arbitrary decoded value into JSON. Pi sends tool
// arguments either as an object or as a JSON-encoded string depending on the
// provider, so both are accepted; anything else is marshalled as-is.
func rawJSON(v json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(v)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			if json.Valid([]byte(s)) {
				return json.RawMessage(s)
			}
		}
		return trimmed
	}
	if !json.Valid(trimmed) {
		return nil
	}
	return json.RawMessage(append([]byte(nil), trimmed...))
}

// previewOf builds a short single-line preview for tree nodes. Truncation is
// rune-aware so it never splits a multi-byte character.
func previewOf(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if limit <= 0 || len(s) <= limit {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}
