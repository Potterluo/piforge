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
	"github.com/piforge/piforge/backend/internal/testrun"
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
	// workspace resolves the checkout's HEAD for the commit sha written onto
	// each audit row. Nil is a hand-built test hub: the sha is then empty, which
	// the trail records as "unknown" rather than inventing one.
	workspace contracts.Workspace
	// revision caches one session's HEAD for a couple of seconds. A tool-heavy
	// turn would otherwise run `git rev-parse` (or a sandbox round trip) for
	// every call, and a commit the agent makes is still visible almost
	// immediately.
	revisionMu  sync.Mutex
	revisionAt  map[string]time.Time
	revisionSHA map[string]string
}

// subscription is the in-flight Start for one session. Ready is closed once
// Start returns so that a second caller waits instead of prompting a runtime
// that is not listening yet.
type subscription struct {
	ready chan struct{}
	err   error
}

func newHub(st *store.Store, rt contracts.AgentRuntime, log *slog.Logger) *hub {
	return &hub{
		subs:        map[string]*subscription{},
		store:       st,
		rt:          rt,
		log:         log,
		revisionAt:  map[string]time.Time{},
		revisionSHA: map[string]string{},
	}
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
		// A cached subscription says a pump exists, not that the process does. An
		// agent killed by the kernel, the OOM reaper or a crash leaves the subscription
		// behind, and returning it meant every later prompt failed with "bridge closed"
		// until the daemon restarted — a session that could not be recovered from the
		// UI at all.
		if h.live(ctx, spec.SessionID) {
			h.mu.Unlock()
			select {
			case <-sub.ready:
				return sub.err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		delete(h.subs, spec.SessionID)
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
	go h.pump(userID, spec.SessionID, sub, events)
	return nil
}

// pump persists one session's event stream until it closes. It is the only
// writer of runtime events, so `seq` ordering follows arrival order.
//
// The subscription is passed in rather than looked up by session id: a session
// can be stopped and restarted (a model switch does exactly that), and the old
// pump exiting late would otherwise delete the *new* subscription and leave the
// next Start spawning a second process for the same session.
func (h *hub) pump(userID, sessionID string, sub *subscription, events <-chan contracts.Event) {
	defer func() {
		h.mu.Lock()
		if h.subs[sessionID] == sub {
			delete(h.subs, sessionID)
		}
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
			// The run is the unit token usage and cost are reported for. Record
			// its context now; the settle event fills the usage in.
			if err := h.store.BeginAuditRun(ctx, store.AuditRun{
				RunID: ev.RunID, SessionID: ev.SessionID, UserID: userID,
				ModelRef:  h.modelRef(ctx, ev.SessionID),
				CommitSHA: h.commitSHA(ctx, ev.SessionID),
				StartedAt: ev.TS,
			}); err != nil {
				h.log.Debug("audit run not recorded", slog.String("error", err.Error()))
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
			// The final usage the provider reported, on the row the audit query
			// joins. A provider that reports nothing leaves zeroes, which the
			// UI renders as "not reported".
			if err := h.store.SettleAuditRun(ctx, store.AuditRunSettle{
				RunID: ev.RunID, SessionID: ev.SessionID, Status: settle.Status,
				TokensIn: settle.TokensIn, TokensOut: settle.TokensOut,
				TokensCacheR: settle.TokensCacheR, TokensCacheW: settle.TokensCacheW,
				CostUSD: settle.CostUSD, SettledAt: &ev.TS,
			}); err != nil {
				h.log.Debug("audit run not settled", slog.String("error", err.Error()))
			}
			// A call that never reported an outcome is closed as abandoned
			// rather than left "running" forever. Appending the end row is the
			// same write the tool.ended path makes; a late real end row is a
			// no-op because the (call, end) id already exists.
			if n, err := h.store.CloseAbandonedToolCalls(ctx, ev.SessionID, ev.RunID); err != nil {
				h.log.Debug("abandoned tool calls not closed", slog.String("error", err.Error()))
			} else if n > 0 {
				h.log.Info("closed tool calls with no outcome",
					slog.String("session", ev.SessionID), slog.Int("count", n))
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
		// Tool arguments can contain secrets and file content, so the row keeps
		// a hash and a bounded, redacted preview rather than the raw text
		// (docs/AUDIT.md). The store builds that preview so every writer gets
		// the same rule; the full arguments stay in the session.
		sum := sha256.Sum256(append([]byte(p.Name+"\x00"), p.Args...))
		if err := h.store.CreateToolCall(ctx, userID, store.ToolCall{
			ID:          p.ToolCallID,
			SessionID:   ev.SessionID,
			RunID:       ev.RunID,
			Name:        p.Name,
			Namespace:   toolNamespace(p.Name),
			ArgsHash:    hex.EncodeToString(sum[:]),
			ArgsPreview: p.Args,
			StartedAt:   ev.TS,
			ModelRef:    h.modelRef(ctx, ev.SessionID),
			CommitSHA:   h.commitSHA(ctx, ev.SessionID),
		}); err != nil {
			h.log.Debug("tool call not recorded", slog.String("error", err.Error()))
		}
		// A test command is the one tool call whose output the panel reads as
		// structured data. The row starts here so the panel can say 运行中.
		if command, ok := testrun.ToolCommand(p.Name, p.Args); ok {
			h.startTestRun(ctx, userID, ev, p.ToolCallID, command)
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
		if p.Text != "" {
			h.finishTestRun(ctx, userID, ev, p.ToolCallID, p.Text, p.DurationMS)
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

// startedModelReporter is implemented by the real runtime (internal/pi). A
// session row says what the user picked; this says what the live process was
// actually launched with, which is the difference a model switch creates. The
// hub asserts for it rather than widening contracts.AgentRuntime, so a test
// runtime that does not care keeps compiling.
type startedModelReporter interface {
	StartedModelRef(sessionID string) (string, bool)
}

// servedModelReporter is implemented by the real runtime too. StartedModelRef
// is the platform's reference ("volc/deepseek-v4-flash"); this is the one Pi was
// configured with ("piforge/deepseek-v4-flash"), which is the honest answer to
// "which model actually served the call" when the session pinned none.
type servedModelReporter interface {
	ServedModelRef(sessionID string) (string, bool)
}

// modelRef is the model reference written onto an audit row.
func (h *hub) modelRef(ctx context.Context, sessionID string) string {
	if reporter, ok := h.rt.(servedModelReporter); ok {
		if ref, live := reporter.ServedModelRef(sessionID); live && strings.TrimSpace(ref) != "" {
			return ref
		}
	}
	if reporter, ok := h.rt.(startedModelReporter); ok {
		if ref, live := reporter.StartedModelRef(sessionID); live && strings.TrimSpace(ref) != "" {
			return ref
		}
	}
	sess, err := h.store.SessionAny(ctx, sessionID)
	if err != nil {
		return ""
	}
	return sess.ModelRef
}

// commitSHA is the workspace revision at the moment of the call. An unknown
// revision is recorded as empty: guessing one would make the trail lie.
func (h *hub) commitSHA(ctx context.Context, sessionID string) string {
	if h.workspace == nil {
		return ""
	}
	const ttl = 2 * time.Second
	h.revisionMu.Lock()
	if at, ok := h.revisionAt[sessionID]; ok && time.Since(at) < ttl {
		sha := h.revisionSHA[sessionID]
		h.revisionMu.Unlock()
		return sha
	}
	h.revisionMu.Unlock()

	sess, err := h.store.SessionAny(ctx, sessionID)
	if err != nil || sess.WorktreeID == "" {
		return ""
	}
	path, err := h.store.WorktreePathByID(ctx, sess.WorktreeID)
	if err != nil || strings.TrimSpace(path) == "" {
		return ""
	}
	graph, err := h.workspace.Log(ctx, path, 1, "")
	if err != nil || graph == nil {
		return ""
	}
	sha := graph.Head
	h.revisionMu.Lock()
	h.revisionAt[sessionID] = time.Now()
	h.revisionSHA[sessionID] = sha
	h.revisionMu.Unlock()
	return sha
}

// liveCount is how many sessions currently have a subscription, which is the
// closest thing to "agents running" the control plane knows: a subscription
// exists from the moment the child is started until it is stopped.
func (h *hub) liveCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// runsInFlight counts the sessions whose agent is in the middle of a run.
//
// A live agent process is not a run: Pi keeps the child warm between turns, and
// opening an idle fork starts one. The admin page used to report the process
// count as 「运行中的 agent」, so a reviewer opening an idle fork moved the
// number from 1 to 2 with nobody working. The question "is a turn in flight" is
// answered by the runtime's own state, per session, so that is what this asks.
// The ids are copied under the lock and queried outside it: a status call can
// talk to the child, and holding the hub lock while it does would stall every
// other session's start.
func (h *hub) runsInFlight(ctx context.Context) int {
	if h.rt == nil {
		return 0
	}
	h.mu.Lock()
	ids := make([]string, 0, len(h.subs))
	for id := range h.subs {
		ids = append(ids, id)
	}
	h.mu.Unlock()

	count := 0
	for _, id := range ids {
		status, err := h.rt.Status(ctx, id)
		if err != nil {
			continue
		}
		if status.Running || status.Streaming {
			count++
		}
	}
	return count
}

// live reports whether the runtime still has a working process for a session.
//
// Status answers from the normalizer, so a dead process reads as ProcessID 0. That is
// the cheapest honest test, and it is what makes a crashed session self-healing instead
// of a page that looks fine and fails every action.
func (h *hub) live(_ context.Context, sessionID string) bool {
	type aliver interface{ Alive(string) bool }
	if a, ok := h.rt.(aliver); ok {
		return a.Alive(sessionID)
	}
	// A runtime that cannot answer keeps the old behaviour, which is what it had
	// before: trust the subscription.
	return true
}
