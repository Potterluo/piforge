package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// hub starts at most one agent subscription per session and persists the
// normalized events it produces.
//
// The runtime hands events out as a channel; the SSE endpoint reads the
// persisted log instead of the channel. That split is what makes reconnect
// work: a client that drops its SSE stream loses nothing, because the durable
// log keeps filling up in the pump and Last-Event-ID resumes from it.
type hub struct {
	mu    sync.Mutex
	subs  map[string]*subscription
	store *store.Store
	rt    contracts.AgentRuntime
	log   *slog.Logger
}

// subscription is the in-flight Start for one session. Ready is closed once
// Start returns so that a second caller waits instead of prompting a runtime
// that is not listening yet.
type subscription struct {
	ready chan struct{}
	err   error
}

func newHub(st *store.Store, rt contracts.AgentRuntime, log *slog.Logger) *hub {
	return &hub{subs: map[string]*subscription{}, store: st, rt: rt, log: log}
}

// ensure starts the session's agent process if it is not already running and
// keeps a pump goroutine draining its events into the store.
//
// The lock is not held while Start runs: starting a process can take a while,
// and holding it would serialise every other session's first message. Instead
// the second caller waits on sub.ready.
func (h *hub) ensure(ctx context.Context, userID string, spec contracts.SessionSpec) error {
	if h.rt == nil {
		return withStatus(503, contracts.ErrInternal, "agent runtime is not configured")
	}

	h.mu.Lock()
	if sub, ok := h.subs[spec.SessionID]; ok {
		h.mu.Unlock()
		select {
		case <-sub.ready:
			return sub.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	sub := &subscription{ready: make(chan struct{})}
	h.subs[spec.SessionID] = sub
	h.mu.Unlock()

	events, err := h.rt.Start(ctx, spec)

	h.mu.Lock()
	sub.err = err
	if err != nil {
		// Let the next attempt try again rather than caching a failure.
		delete(h.subs, spec.SessionID)
	}
	h.mu.Unlock()
	close(sub.ready)

	if err != nil {
		return err
	}
	go h.pump(userID, spec.SessionID, events)
	return nil
}

// pump persists one session's event stream until it closes. It is the only
// writer of runtime events, so `seq` ordering follows arrival order.
func (h *hub) pump(userID, sessionID string, events <-chan contracts.Event) {
	defer func() {
		h.mu.Lock()
		delete(h.subs, sessionID)
		h.mu.Unlock()
	}()

	for ev := range events {
		ev.SessionID = sessionID
		if ev.TS.IsZero() {
			ev.TS = time.Now()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := h.store.AppendEvent(ctx, userID, &ev)
		cancel()
		if err != nil {
			h.log.Warn("event not persisted",
				slog.String("session", sessionID),
				slog.String("type", ev.Type),
				slog.String("error", err.Error()))
			continue
		}
		h.applyDerivedState(userID, ev)
	}
}

// applyDerivedState mirrors the two things the UI reads without replaying the
// whole log: the session's running flag and the run's token totals.
//
// It is best-effort: a failure here must not stop the transcript from being
// written, so every branch only logs.
func (h *hub) applyDerivedState(userID string, ev contracts.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	switch ev.Type {
	case contracts.EvRunStarted:
		status := contracts.StatusRunning
		if _, err := h.store.UpdateSession(ctx, userID, ev.SessionID, store.SessionPatch{Status: &status}); err != nil {
			h.log.Debug("session status not updated", slog.String("error", err.Error()))
		}
		if ev.RunID != "" {
			if _, err := h.store.CreateRun(ctx, userID, store.NewRun{
				ID: ev.RunID, SessionID: ev.SessionID, Trigger: "user",
			}); err != nil {
				h.log.Debug("run not created", slog.String("error", err.Error()))
			}
		}
	case contracts.EvRunSettled, contracts.EvRunAborted:
		status := contracts.StatusIdle
		if _, err := h.store.UpdateSession(ctx, userID, ev.SessionID, store.SessionPatch{Status: &status}); err != nil {
			h.log.Debug("session status not updated", slog.String("error", err.Error()))
		}
		if ev.RunID != "" {
			settle := store.RunSettle{Status: "settled"}
			if ev.Type == contracts.EvRunAborted {
				settle.Status = "aborted"
			}
			var usage struct {
				TokensIn     int64   `json:"tokens_in"`
				TokensOut    int64   `json:"tokens_out"`
				TokensCacheR int64   `json:"tokens_cache_read"`
				TokensCacheW int64   `json:"tokens_cache_write"`
				CostUSD      float64 `json:"cost_usd"`
			}
			if len(ev.Payload) > 0 && json.Unmarshal(ev.Payload, &usage) == nil {
				settle.TokensIn = usage.TokensIn
				settle.TokensOut = usage.TokensOut
				settle.TokensCacheR = usage.TokensCacheR
				settle.TokensCacheW = usage.TokensCacheW
				settle.CostUSD = usage.CostUSD
			}
			// The run may have been created by a different path; a missing run
			// is not worth an error.
			if err := h.store.SettleRun(ctx, userID, ev.RunID, settle); err != nil {
				h.log.Debug("run not settled", slog.String("error", err.Error()))
			}
		}

	case contracts.EvToolStarted:
		var p struct {
			ToolCallID string          `json:"tool_call_id"`
			Name       string          `json:"name"`
			Args       json.RawMessage `json:"args"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || p.ToolCallID == "" {
			return
		}
		// Tool arguments can contain secrets, so the row keeps a hash and a
		// bounded preview rather than the raw text (PRD §8.2).
		sum := sha256.Sum256(append([]byte(p.Name+"\x00"), p.Args...))
		preview := p.Args
		if len(preview) > 2048 {
			preview = nil
		}
		if err := h.store.CreateToolCall(ctx, userID, store.ToolCall{
			ID:          p.ToolCallID,
			SessionID:   ev.SessionID,
			RunID:       ev.RunID,
			Name:        p.Name,
			Namespace:   toolNamespace(p.Name),
			ArgsHash:    hex.EncodeToString(sum[:]),
			ArgsPreview: preview,
			StartedAt:   ev.TS,
		}); err != nil {
			h.log.Debug("tool call not recorded", slog.String("error", err.Error()))
		}

	case contracts.EvToolEnded:
		var p struct {
			ToolCallID string `json:"tool_call_id"`
			IsError    bool   `json:"is_error"`
			DurationMS int64  `json:"duration_ms"`
			Text       string `json:"text"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || p.ToolCallID == "" {
			return
		}
		ended := ev.TS
		dur := int(p.DurationMS)
		size := len(p.Text)
		if err := h.store.FinishToolCall(ctx, userID, p.ToolCallID, store.ToolCallFinish{
			IsError:     p.IsError,
			DurationMS:  &dur,
			ResultBytes: &size,
			EndedAt:     &ended,
		}); err != nil {
			h.log.Debug("tool call not finished", slog.String("error", err.Error()))
		}
	}
}

// toolNamespace derives the grouping key for a tool. MCP tools are named
// `mcp__<server>__<tool>`, everything else is a built-in with no namespace.
func toolNamespace(name string) string {
	const prefix = "mcp__"
	if !strings.HasPrefix(name, prefix) {
		return ""
	}
	rest := name[len(prefix):]
	for i := 0; i+1 < len(rest); i++ {
		if rest[i] == '_' && rest[i+1] == '_' {
			return name[:len(prefix)+i]
		}
	}
	return ""
}

// liveCount is how many sessions currently have a subscription, which is the
// closest thing to "agents running" the control plane knows: a subscription
// exists from the moment the child is started until it is stopped.
func (h *hub) liveCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
