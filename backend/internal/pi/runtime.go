package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/isolate"
)

// RuntimeConfig configures a Runtime.
//
// Config supplies the launch parameters shared by every session; the fields
// below adapt them to a multi-session runtime.
type RuntimeConfig struct {
	// Config is the template for each per-session process. Dir is overridden by
	// the session's worktree and SessionFile is derived per session.
	Config
	// Launcher optionally wraps the agent's argv so it runs inside a jail
	// (bubblewrap/firejail). Nil means run it directly.
	Launcher isolate.Launcher
	// ModelFor translates the platform's model reference into the one Pi uses.
	// A session's ref names the user's provider ("volc/deepseek-v4-flash"), while
	// the process only knows the gateway provider we generated for it, so the two
	// are not interchangeable. Nil means use the reference as-is.
	ModelFor func(spec contracts.SessionSpec, ref string) string
	// ArgsFor returns extra argv entries for one session's process (the model
	// selection Pi needs). Nil means the configured Args are used as-is.
	ArgsFor func(spec contracts.SessionSpec) []string
	// EnvFor returns extra environment entries for one session's process. It is
	// how the control plane hands the agent a short-lived model gateway token
	// instead of a real provider key (PRD §5.7.2).
	EnvFor func(spec contracts.SessionSpec) []string
	// SessionFileFor overrides where a session's JSONL file lives. When nil the
	// runtime uses Config.SessionFile as a template ("{session}" is replaced
	// with the session id) or falls back to
	// <Dir>/.piforge/sessions/<session-id>.jsonl.
	SessionFileFor func(sessionID string) string
	// Dial replaces the subprocess spawn with an arbitrary transport. Tests use
	// it to run the fake peer in-process over io.Pipe; a production deployment
	// can use it to reach Pi through a sandbox shim's stdio channel.
	Dial func(spec contracts.SessionSpec, sessionFile string) (io.ReadWriteCloser, error)
}

// Runtime implements contracts.AgentRuntime with one Pi child process per
// session.
//
// One process per session is a hard constraint, not a simplification: Pi keeps
// its session state inside the process, so two branches of the same
// conversation cannot share one process (PRD §5.1.3, §7.4.1).
type Runtime struct {
	cfg RuntimeConfig

	mu        sync.Mutex
	sessions  map[string]*session
	overrides map[string]string
	closed    bool

	// spawnMu serialises process creation so two concurrent Start calls for the
	// same session cannot race into spawning two children.
	spawnMu sync.Mutex
}

var _ contracts.AgentRuntime = (*Runtime)(nil)

// session is one live agent process plus the per-session translation state.
type session struct {
	id   string
	spec contracts.SessionSpec
	// startedRef is the platform model reference ("volc/deepseek-v4-flash") this
	// process was launched with. Pi's models.json and the gateway token are
	// minted per spawn from it, so a different reference cannot be applied to a
	// live process — it needs a new one. s.spec.ModelRef is *not* a substitute:
	// ensure() refreshes the spec from the session row even while the process
	// that was spawned with the old model is still running.
	startedRef string
	// modelConfigured records whether this process was given the platform's
	// model configuration (models.json via PI_CODING_AGENT_DIR). A process
	// spawned while the session's provider did not exist has none, and that
	// cannot be repaired in place: the control plane has to spawn a new one.
	modelConfigured bool
	file            string
	bridge          *Bridge
	norm       *Normalizer
	sub        *Subscription
	// lastUsed is when this process was last asked to do anything, in unix nanos.
	// The idle reaper reads it; nothing else should.
	lastUsed atomic.Int64
}

// NewRuntime returns a runtime that spawns processes from cfg.
func NewRuntime(cfg RuntimeConfig) *Runtime {
	return &Runtime{
		cfg:       cfg,
		sessions:  map[string]*session{},
		overrides: map[string]string{},
	}
}

// Start ensures a live agent process exists for the session and returns a
// subscription of normalized events. The subscription is created before the
// process is spawned, so no event emitted during startup can be missed.
func (r *Runtime) Start(ctx context.Context, spec contracts.SessionSpec) (<-chan contracts.Event, error) {
	if strings.TrimSpace(spec.SessionID) == "" {
		return nil, contracts.NewError(contracts.ErrInvalidArgument, "session_id is required")
	}
	s, err := r.ensure(ctx, spec)
	if err != nil {
		return nil, err
	}
	primeContextWindow(ctx, s)
	return s.sub.C, nil
}

// primeContextWindow asks the just-spawned process for its model's context
// window so the very first run reports a real percentage instead of 0%.
//
// It is best-effort and bounded: a peer that does not answer must not delay the
// first prompt, so the call shares the caller's deadline and its failure is
// silent.
func primeContextWindow(ctx context.Context, s *session) {
	if s == nil || s.bridge == nil || !s.bridge.Alive() {
		return
	}
	var state struct {
		Model struct {
			ContextWindow int64 `json:"contextWindow"`
		} `json:"model"`
	}
	if err := s.bridge.CallInto(ctx, "get_state", nil, &state); err == nil && state.Model.ContextWindow > 0 {
		s.norm.SetContextWindow(state.Model.ContextWindow)
	}
}

// ensure returns the live session, spawning a process if there is none or if
// the previous one died.
func (r *Runtime) ensure(ctx context.Context, spec contracts.SessionSpec) (*session, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("pi: runtime: %w", ErrClosed)
	}
	if s, ok := r.sessions[spec.SessionID]; ok && s.bridge.Alive() {
		if spec.Kind != "" {
			s.spec = spec
		}
		r.mu.Unlock()
		return s, nil
	}
	r.mu.Unlock()

	r.spawnMu.Lock()
	defer r.spawnMu.Unlock()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("pi: runtime: %w", ErrClosed)
	}
	if s, ok := r.sessions[spec.SessionID]; ok && s.bridge.Alive() {
		r.mu.Unlock()
		return s, nil
	}
	stale := r.sessions[spec.SessionID]
	delete(r.sessions, spec.SessionID)
	r.mu.Unlock()
	if stale != nil {
		stale.sub.Close()
		_ = stale.bridge.Close()
	}

	s, err := r.spawn(ctx, spec)
	if err != nil {
		return nil, err
	}
	// A brand-new process is not idle: without this the reaper's first tick would see
	// a zero timestamp and stop it immediately.
	s.touch()
	r.mu.Lock()
	r.sessions[spec.SessionID] = s
	r.mu.Unlock()
	return s, nil
}

// spawn launches one process (or opens one transport) and wires it to a
// normalizer. The caller holds spawnMu.
func (r *Runtime) spawn(ctx context.Context, spec contracts.SessionSpec) (*session, error) {
	file := r.sessionFileFor(spec.SessionID)
	cfg := r.cfg.Config
	if spec.Worktree != "" {
		cfg.Dir = spec.Worktree
	}
	cfg.SessionFile = file
	// Pi takes its working directory from the session file, not from the process:
	// a forked or cloned session inherits the *parent's* cwd in that header and
	// would run its tools in the parent's checkout. Aligning it here covers every
	// way a process is created — a fresh start, a fork, a clone, a crash restart
	// and a resume after the control plane or the sandbox restarted.
	if _, err := alignSessionCwd(file, spec.Worktree); err != nil {
		return nil, fmt.Errorf("pi: session %s: %w", spec.SessionID, err)
	}
	if r.cfg.ArgsFor != nil {
		if extra := r.cfg.ArgsFor(spec); len(extra) > 0 {
			cfg.Args = append(append([]string{}, cfg.Args...), extra...)
		}
	}
	if r.cfg.EnvFor != nil {
		if extra := r.cfg.EnvFor(spec); len(extra) > 0 {
			cfg.Env = append(append([]string{}, cfg.Env...), extra...)
		}
	}
	if r.cfg.Launcher != nil {
		// The agent is the process that runs model-driven commands, so it is
		// the one that must be jailed. Wrapping argv here covers everything it
		// spawns in turn.
		wrapped := r.cfg.Launcher.Wrap(append([]string{cfg.BinaryPath}, cfg.Args...))
		if len(wrapped) > 0 {
			cfg.BinaryPath = wrapped[0]
			cfg.Args = wrapped[1:]
		}
	}
	norm := NewNormalizer(spec.SessionID, spec.Kind)
	cfg.Translator = norm.Translate

	var bridge *Bridge
	if r.cfg.Dial != nil {
		t, err := r.cfg.Dial(spec, file)
		if err != nil {
			return nil, fmt.Errorf("pi: dial session %s: %w", spec.SessionID, err)
		}
		bridge = NewStream(t, cfg)
	} else {
		bridge = NewBridge(cfg)
	}
	sub := bridge.Subscribe()
	if r.cfg.Dial == nil {
		if err := bridge.Start(ctx); err != nil {
			sub.Close()
			_ = bridge.Close()
			return nil, err
		}
	}
	s := &session{
		id:              spec.SessionID,
		spec:            spec,
		startedRef:      spec.ModelRef,
		modelConfigured: hasPlatformModelConfig(cfg.Env),
		file:            file,
		bridge:          bridge,
		norm:            norm,
		sub:             sub,
	}
	if spec.ModelRef != "" {
		modelRef := spec.ModelRef
		if r.cfg.ModelFor != nil {
			modelRef = r.cfg.ModelFor(spec, modelRef)
		}
		if err := r.applyModel(ctx, s, modelRef); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// sessionFileFor resolves the JSONL file backing one session.
func (r *Runtime) sessionFileFor(sessionID string) string {
	r.mu.Lock()
	if f, ok := r.overrides[sessionID]; ok {
		r.mu.Unlock()
		return f
	}
	r.mu.Unlock()
	if r.cfg.SessionFileFor != nil {
		return r.cfg.SessionFileFor(sessionID)
	}
	if template := r.cfg.SessionFile; template != "" {
		if strings.Contains(template, "{session}") {
			return strings.ReplaceAll(template, "{session}", sessionID)
		}
		return template
	}
	dir := r.cfg.Dir
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, ".piforge", "sessions", sessionID+".jsonl")
}

// get returns the live session or a not_found error.
func (r *Runtime) get(sessionID string) (*session, error) {
	r.mu.Lock()
	s, ok := r.sessions[sessionID]
	r.mu.Unlock()
	if !ok {
		return nil, contracts.NewError(contracts.ErrNotFound, "no agent process for session "+sessionID)
	}
	return s, nil
}

// Stop tears the process down. It is idempotent: stopping a session that has no
// live process succeeds, so archiving a session twice is harmless.
func (r *Runtime) Stop(ctx context.Context, sessionID string) error {
	r.mu.Lock()
	s := r.sessions[sessionID]
	delete(r.sessions, sessionID)
	r.mu.Unlock()
	if s == nil {
		return nil
	}
	s.sub.Close()
	if err := s.bridge.Close(); err != nil {
		return fmt.Errorf("pi: stop session %s: %w", sessionID, err)
	}
	return nil
}

// Close stops every session. It is what a daemon calls on shutdown.
func (r *Runtime) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	sessions := make([]*session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.sessions = map[string]*session{}
	r.mu.Unlock()

	var firstErr error
	for _, s := range sessions {
		s.sub.Close()
		if err := s.bridge.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("pi: close session %s: %w", s.id, err)
		}
	}
	return firstErr
}

// Prompt submits a user message.
//
// When the agent is already streaming the request must say how to deliver the
// text; without that, the call fails with ErrSessionBusy rather than silently
// interleaving two prompts. "steer" injects the text into the running turn,
// "follow_up" queues it as the next turn (PRD §5.1.1 FR-1.3).
//
// If the session's process has died, the call fails with ErrClosed: the caller
// re-runs Start, which respawns it from the session file (PRD §7.4.5 — a crash
// is reported, never replayed automatically).
func (r *Runtime) Prompt(ctx context.Context, req contracts.PromptRequest) (contracts.PromptResult, error) {
	// A prompt is the strongest signal that this session is in use (FR-5.x reaping),
	// and it is also where a dead process is noticed: an agent killed by the kernel or
	// the OOM reaper leaves no trace until the next call, which used to fail with
	// "bridge closed" and no way back from the UI.
	if s, err := r.get(req.SessionID); err == nil {
		if !s.bridge.Alive() {
			if _, rerr := r.ensure(ctx, s.spec); rerr != nil {
				return contracts.PromptResult{}, fmt.Errorf("pi: respawn after a dead agent: %w", rerr)
			}
		} else {
			s.touch()
		}
	}
	s, err := r.get(req.SessionID)
	if err != nil {
		return contracts.PromptResult{}, err
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Images) == 0 {
		return contracts.PromptResult{}, contracts.NewError(contracts.ErrInvalidArgument,
			"message text is empty")
	}
	behavior, err := wireStreamingBehavior(req.StreamingBehavior)
	if err != nil {
		return contracts.PromptResult{}, err
	}
	state := s.norm.State()
	if state.Streaming && behavior == "" {
		return contracts.PromptResult{}, contracts.NewError(contracts.ErrSessionBusy,
			"the agent is already running; set streaming_behavior to steer or follow_up")
	}
	if req.ModelRef != "" {
		modelRef := req.ModelRef
		if r.cfg.ModelFor != nil {
			modelRef = r.cfg.ModelFor(s.spec, modelRef)
		}
		if err := r.applyModel(ctx, s, modelRef); err != nil {
			return contracts.PromptResult{}, err
		}
	}

	// Reserve the run id before sending, because the peer may emit agent_start
	// before its response is processed.
	reserved := ""
	if !state.Streaming {
		reserved = s.norm.ReserveRun()
	}

	fields := map[string]any{"message": req.Text}
	if behavior != "" {
		fields["streamingBehavior"] = behavior
	}
	if len(req.Images) > 0 {
		fields["images"] = req.Images
	}
	resp, err := s.bridge.Call(ctx, "prompt", fields)
	if err != nil {
		if reserved != "" {
			s.norm.ClearReservedRun(reserved)
		}
		return contracts.PromptResult{}, err
	}
	var data struct {
		Disposition string `json:"disposition"`
	}
	_ = json.Unmarshal(resp.Data, &data)
	disp := data.Disposition
	if disp == "" {
		disp = "started"
	}
	result := contracts.PromptResult{Accepted: true, Disposition: disp}
	switch disp {
	case "started":
		result.RunID = reserved
		if result.RunID == "" {
			result.RunID = s.norm.State().RunID
		}
	default:
		// Queued behind the running turn: report that run, not a new one.
		result.RunID = s.norm.State().RunID
		result.QueuePosition = s.norm.State().QueueDepth
		if reserved != "" {
			s.norm.ClearReservedRun(reserved)
		}
	}
	return result, nil
}

// wireStreamingBehavior maps the API's spelling onto Pi's. Pi calls the
// follow-up queue "followUp"; the API calls it "follow_up".
func wireStreamingBehavior(behavior string) (string, error) {
	switch behavior {
	case "":
		return "", nil
	case "steer":
		return "steer", nil
	case "follow_up", "followUp":
		return "followUp", nil
	default:
		return "", contracts.NewError(contracts.ErrInvalidArgument,
			fmt.Sprintf("unknown streaming_behavior %q", behavior))
	}
}

// applyModel switches the session's model. A ref is "provider/model".
func (r *Runtime) applyModel(ctx context.Context, s *session, ref string) error {
	slash := strings.Index(ref, "/")
	if slash <= 0 || slash == len(ref)-1 {
		return contracts.NewError(contracts.ErrInvalidArgument,
			fmt.Sprintf("model_ref %q must be provider/model", ref))
	}
	_, err := s.bridge.Call(ctx, "set_model", map[string]any{
		"provider": ref[:slash],
		"modelId":  ref[slash+1:],
	})
	if err != nil {
		return fmt.Errorf("pi: set model %q: %w", ref, err)
	}
	return nil
}

// StartedModelRef reports the platform model reference ("volc/deepseek-v4-flash")
// the session's live process was launched with, and whether it is still alive.
//
// The control plane needs it to notice that the session row names a different
// model than the process: the picker writes the row, and a live process keeps the
// model it spawned with, so the next turn must either restart it or fail instead
// of silently answering from the old one.
func (r *Runtime) StartedModelRef(sessionID string) (string, bool) {
	r.mu.Lock()
	s, ok := r.sessions[sessionID]
	r.mu.Unlock()
	if !ok || s == nil || !s.bridge.Alive() {
		return "", false
	}
	return s.startedRef, true
}

// StartedModelConfig reports whether the live process was spawned with the
// platform's model configuration, and whether it is still alive.
//
// The control plane uses it to recognise the one broken state it cannot explain
// away: an agent started while the session's provider did not exist. Pi's
// models.json and the gateway token are written per spawn, so that process will
// keep answering "No API key found for the selected model" until a new one
// replaces it — no retry and no provider change reaches it.
func (r *Runtime) StartedModelConfig(sessionID string) (bool, bool) {
	r.mu.Lock()
	s, ok := r.sessions[sessionID]
	r.mu.Unlock()
	if !ok || s == nil || !s.bridge.Alive() {
		return false, false
	}
	return s.modelConfigured, true
}

// hasPlatformModelConfig reports whether an environment carries the pointer to
// the agent directory this control plane wrote. Without it Pi falls back to its
// own configuration and has no gateway token.
func hasPlatformModelConfig(env []string) bool {
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok && name == "PI_CODING_AGENT_DIR" && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// Interrupt clears the queue and aborts the running turn.
//
// The cleared texts are returned so the composer can put them back in the input
// box (PRD §5.1.1 FR-1.4, which mirrors Pi's clear_queue → abort sequence). The
// abort is attempted even when clear_queue fails: leaving the agent running is
// worse than losing the queue text.
func (r *Runtime) Interrupt(ctx context.Context, sessionID string) (contracts.InterruptResult, error) {
	s, err := r.get(sessionID)
	if err != nil {
		return contracts.InterruptResult{}, err
	}
	// Tell the normalizer a stop is coming *before* Pi unwinds: the abort reaches
	// Pi as an error on the assistant message, and without this flag the
	// transcript kept a 模型调用失败 box — with advice about the provider's key —
	// for a run the user simply stopped.
	s.norm.NoteInterrupt()
	var result contracts.InterruptResult
	if resp, clearErr := s.bridge.Call(ctx, "clear_queue", nil); clearErr == nil {
		var data struct {
			Steering []string `json:"steering"`
			FollowUp []string `json:"followUp"`
		}
		if json.Unmarshal(resp.Data, &data) == nil {
			result.ClearedSteering = nonNilStrings(data.Steering)
			result.ClearedFollowUp = nonNilStrings(data.FollowUp)
		}
	}
	if _, err := s.bridge.Call(ctx, "abort", nil); err != nil {
		return result, err
	}
	return result, nil
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// Status reports the live process state.
//
// The running/streaming flags come from the event stream, so they stay correct
// even when the RPC probe fails; get_state is consulted only to enrich them. A
// session whose process died reports Running:false rather than an error, so the
// UI can render "interrupted" instead of a blank panel.
func (r *Runtime) Status(ctx context.Context, sessionID string) (contracts.RuntimeStatus, error) {
	s, err := r.get(sessionID)
	if err != nil {
		return contracts.RuntimeStatus{}, err
	}
	st := s.norm.State()
	out := contracts.RuntimeStatus{
		SessionID:   sessionID,
		Running:     st.Running,
		Streaming:   st.Streaming,
		Compacting:  st.Compacting,
		QueueDepth:  st.QueueDepth,
		ModelRef:    modelRefOf(st.Provider, st.Model),
		LastSeq:     st.LastSeq,
		ProcessID:   s.bridge.PID(),
		SessionFile: s.file,
	}
	if !s.bridge.Alive() {
		out.Running = false
		out.Streaming = false
		return out, nil
	}
	var state struct {
		IsStreaming         bool   `json:"isStreaming"`
		IsCompacting        bool   `json:"isCompacting"`
		SessionFile         string `json:"sessionFile"`
		PendingMessageCount int    `json:"pendingMessageCount"`
		Model               struct {
			ID            string `json:"id"`
			Provider      string `json:"provider"`
			ContextWindow int64  `json:"contextWindow"`
		} `json:"model"`
	}
	if err := s.bridge.CallInto(ctx, "get_state", nil, &state); err == nil {
		// The event stream is authoritative for the run flags. agent_settled is
		// emitted when the run is over, but the process can still report
		// isStreaming:true for a moment — it clears its own flag after announcing
		// the settle, and get_state is answered by a different goroutine. Copying
		// that reply over the derived state made Status answer
		// Running:false/Streaming:true immediately after a settle, so the UI
		// flipped back to "running" and the next run was refused as busy.
		//
		// Only fall back to the process when no event has been observed yet: a
		// runtime that has just attached has nothing else to go on.
		if st.LastSeq == 0 {
			out.Running = state.IsStreaming
			out.Streaming = state.IsStreaming
			out.Compacting = state.IsCompacting
			out.QueueDepth = state.PendingMessageCount
		}
		if state.SessionFile != "" {
			out.SessionFile = state.SessionFile
		}
		if state.Model.Provider != "" || state.Model.ID != "" {
			out.ModelRef = modelRefOf(state.Model.Provider, state.Model.ID)
		}
		s.norm.SetContextWindow(state.Model.ContextWindow)
	}
	return out, nil
}

func modelRefOf(provider, model string) string {
	// "unknown" is Pi's placeholder for a model it never resolved (a process
	// spawned with no model configuration). It is not a model name and must not
	// reach the UI: the header printed `跟随默认（unknown）` because of it.
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if strings.EqualFold(provider, "unknown") {
		provider = ""
	}
	if strings.EqualFold(model, "unknown") {
		model = ""
	}
	switch {
	case provider == "" && model == "":
		return ""
	case provider == "":
		return model
	case model == "":
		return provider
	default:
		return contracts.ModelRef(provider, model)
	}
}

// Tree returns the append-only session tree.
func (r *Runtime) Tree(ctx context.Context, sessionID string) ([]*contracts.TreeEntry, error) {
	s, err := r.get(sessionID)
	if err != nil {
		return nil, err
	}
	var data struct {
		Tree   []treeNodeWire `json:"tree"`
		LeafID string         `json:"leafId"`
	}
	if err := s.bridge.CallInto(ctx, "get_tree", nil, &data); err != nil {
		return nil, err
	}
	out := make([]*contracts.TreeEntry, 0, len(data.Tree))
	for _, node := range data.Tree {
		out = append(out, convertTreeNode(node))
	}
	return out, nil
}

// Entries returns entries appended after `since` plus the current leaf id.
func (r *Runtime) Entries(ctx context.Context, sessionID, since string) ([]*contracts.TreeEntry, string, error) {
	s, err := r.get(sessionID)
	if err != nil {
		return nil, "", err
	}
	fields := map[string]any{}
	if since != "" {
		fields["since"] = since
	}
	var data struct {
		Entries []entryWire `json:"entries"`
		LeafID  string      `json:"leafId"`
	}
	if err := s.bridge.CallInto(ctx, "get_entries", fields, &data); err != nil {
		return nil, "", err
	}
	out := make([]*contracts.TreeEntry, 0, len(data.Entries))
	for _, e := range data.Entries {
		out = append(out, convertEntry(e))
	}
	return out, data.LeafID, nil
}

// ForkPoints lists the user messages a session can be forked from. Pi only
// accepts user messages as fork origins, so nothing else is offered.
func (r *Runtime) ForkPoints(ctx context.Context, sessionID string) ([]contracts.ForkPoint, error) {
	s, err := r.get(sessionID)
	if err != nil {
		return nil, err
	}
	var data struct {
		Messages []struct {
			EntryID string `json:"entryId"`
			Text    string `json:"text"`
		} `json:"messages"`
	}
	if err := s.bridge.CallInto(ctx, "get_fork_messages", nil, &data); err != nil {
		return nil, err
	}
	out := make([]contracts.ForkPoint, 0, len(data.Messages))
	for _, m := range data.Messages {
		out = append(out, contracts.ForkPoint{
			EntryID: m.EntryID,
			Preview: previewOf(m.Text, 120),
		})
	}
	return out, nil
}

// Compact asks the agent to rewrite its model view (PRD §5.1.1 FR-1.9). The
// history itself is never modified.
//
// Pi refuses to compact a session that has nothing worth compacting yet, and
// says so with a command failure ("Nothing to compact (session too small)").
// That is an answer, not a crash: it is reported as contracts.ErrNothingToCompact
// so the HTTP layer can return a readable no-op instead of a 500.
func (r *Runtime) Compact(ctx context.Context, sessionID, instructions string) error {
	s, err := r.get(sessionID)
	if err != nil {
		return err
	}
	fields := map[string]any{}
	if instructions != "" {
		fields["customInstructions"] = instructions
	}
	if _, err := s.bridge.Call(ctx, "compact", fields); err != nil {
		var cmdErr *CommandError
		if errors.As(err, &cmdErr) && isNothingToCompact(cmdErr.Message) {
			return contracts.ErrNothingToCompact
		}
		return err
	}
	return nil
}

// isNothingToCompact matches Pi's wording for "the session is too small to be
// worth compacting". Compared case-insensitively and without the parenthetical
// so a wording tweak on Pi's side does not turn the no-op back into a 500.
func isNothingToCompact(message string) bool {
	return strings.Contains(strings.ToLower(message), "nothing to compact")
}

// Fork creates a new session file branching at a user message, then starts the
// branch's own process.
//
// Pi's fork writes the new branch to its own file and leaves the source session
// intact, so the parent keeps running untouched while the branch gets a fresh
// process — the architecture PRD §5.1.2 depends on.
func (r *Runtime) Fork(ctx context.Context, req contracts.ForkRequest) error {
	if strings.TrimSpace(req.EntryID) == "" {
		return contracts.NewError(contracts.ErrInvalidArgument, "entry_id is required")
	}
	if strings.TrimSpace(req.NewSession) == "" {
		return contracts.NewError(contracts.ErrInvalidArgument, "new_session is required")
	}
	if req.NewSession == req.SessionID {
		return contracts.NewError(contracts.ErrInvalidArgument, "new_session must differ from session_id")
	}
	s, err := r.get(req.SessionID)
	if err != nil {
		return err
	}
	file := r.sessionFileFor(req.NewSession)

	// Pi's fork happens *inside its own process*: it writes a branched session
	// file of its own choosing and rebinds to it. It does not accept a target
	// path, so passing one (which this used to do) silently produced an empty
	// branch — the model had no history even though the transcript claimed it did.
	if _, err := s.bridge.Call(ctx, "fork", map[string]any{"entryId": req.EntryID}); err != nil {
		return fmt.Errorf("pi: fork session %s at %s: %w", req.SessionID, req.EntryID, err)
	}

	// Ask where Pi put it, then adopt that file as the branch's session file so a
	// fresh process for the branch opens the real branched history.
	var state struct {
		SessionFile string `json:"sessionFile"`
	}
	if err := s.bridge.CallInto(ctx, "get_state", nil, &state); err != nil {
		return fmt.Errorf("pi: fork: read forked session path: %w", err)
	}
	// The parent process is now running the branch: stop it so a later prompt to
	// the parent restarts on its own transcript.
	if err := r.Stop(ctx, req.SessionID); err != nil {
		return fmt.Errorf("pi: fork: stop parent: %w", err)
	}
	if state.SessionFile != "" {
		// Adopt Pi's file where it is. It is written lazily, so moving it here
		// fails for a branch that has not spoken yet; an override is enough for
		// the branch process to open the real branched history.
		file = state.SessionFile
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return fmt.Errorf("pi: fork: prepare session dir: %w", err)
	}

	r.mu.Lock()
	r.overrides[req.NewSession] = file
	r.mu.Unlock()

	// The branch process is *not* started here. A runtime session's spec carries
	// the translated model reference (`piforge/...`, the gateway's name), and
	// reusing it minted a gateway token for a provider that does not exist — the
	// branch's first call came back 502 provider_unavailable. The caller starts the
	// branch from the database row, which still says `volc/...`.
	if req.Label != "" {
		if branch, err := r.get(req.NewSession); err == nil {
			// Best effort: a session without a label is still a valid branch.
			_, _ = branch.bridge.Call(ctx, "set_session_name", map[string]any{"name": req.Label})
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tree conversion
// ---------------------------------------------------------------------------

// entryWire is one session-tree entry as Pi reports it.
type entryWire struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	ParentID   *string         `json:"parentId"`
	Timestamp  json.RawMessage `json:"timestamp"`
	Message    json.RawMessage `json:"message"`
	Label      string          `json:"label"`
	Summary    string          `json:"summary"`
	CustomType string          `json:"customType"`
}

// treeNodeWire is one recursive node of get_tree.
type treeNodeWire struct {
	Entry    entryWire      `json:"entry"`
	Children []treeNodeWire `json:"children"`
}

func convertTreeNode(node treeNodeWire) *contracts.TreeEntry {
	out := convertEntry(node.Entry)
	out.Children = make([]*contracts.TreeEntry, 0, len(node.Children))
	for _, child := range node.Children {
		out.Children = append(out.Children, convertTreeNode(child))
	}
	return out
}

func convertEntry(e entryWire) *contracts.TreeEntry {
	out := &contracts.TreeEntry{
		EntryID: e.ID,
		Type:    e.Type,
		Label:   e.Label,
		TS:      parseTimestamp(e.Timestamp),
	}
	if e.ParentID != nil {
		out.ParentID = *e.ParentID
	}
	if len(e.Message) > 0 && string(e.Message) != "null" {
		var msg messageWire
		if json.Unmarshal(e.Message, &msg) == nil {
			out.Role = msg.Role
			out.Preview = previewOf(flattenContent(msg.Content), 120)
		}
		out.Extra = append(json.RawMessage(nil), e.Message...)
	}
	if out.Preview == "" && e.Summary != "" {
		out.Preview = previewOf(e.Summary, 120)
	}
	return out
}

// parseTimestamp accepts the RFC3339 string Pi writes and the millisecond epoch
// some builds use, defaulting to the zero time rather than failing the tree.
func parseTimestamp(raw json.RawMessage) (out time.Time) {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		if t, err := time.Parse(time.RFC3339Nano, asString); err == nil {
			return t.UTC()
		}
		return time.Time{}
	}
	var asNumber float64
	if json.Unmarshal(raw, &asNumber) == nil && asNumber > 0 {
		return time.UnixMilli(int64(asNumber)).UTC()
	}
	return time.Time{}
}

// IsClosed reports whether the runtime has been shut down.
func (r *Runtime) IsClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// Logs returns the tail of the agent's stderr for one session.
//
// It is the first thing to read when a run dies without saying why: Pi writes
// startup failures (a missing model, a bad flag, a crashed extension) to stderr,
// and without this they were only visible on the host's console (PRD FR-5.10).
func (r *Runtime) Logs(ctx context.Context, sessionID string) (*contracts.SessionLogs, error) {
	r.mu.Lock()
	s := r.sessions[sessionID]
	r.mu.Unlock()
	if s == nil {
		return &contracts.SessionLogs{SessionID: sessionID, Lines: []string{}}, nil
	}
	tail := strings.TrimRight(s.bridge.StderrTail(), "\n")
	lines := []string{}
	if tail != "" {
		lines = strings.Split(tail, "\n")
	}
	// The buffer is a byte window, not a line window: say so rather than letting
	// the caller wonder why the first line is cut in half.
	truncated := len(lines) > 0 && len(s.bridge.StderrTail()) >= 8<<10
	return &contracts.SessionLogs{
		SessionID: sessionID,
		Lines:     lines,
		Truncated: truncated,
		UpdatedAt: time.Now(),
	}, nil
}

// CloneSession copies one session's transcript file to another's.
//
// Promoting a sidecar needs it: the control plane can copy the *events* it stores,
// but the model reads Pi's own file, so a branch that only had events showed the
// conversation while the agent denied ever seeing it. Copying the file gives both
// sides the same history.
func (r *Runtime) CloneSession(ctx context.Context, fromSession, toSession string) error {
	if fromSession == toSession {
		return nil
	}
	source := r.sessionFileFor(fromSession)
	target := r.sessionFileFor(toSession)
	if source == target {
		return nil
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("pi: clone session: read %s: %w", source, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("pi: clone session: prepare %s: %w", target, err)
	}
	if err := os.WriteFile(target, raw, 0o600); err != nil {
		return fmt.Errorf("pi: clone session: write %s: %w", target, err)
	}
	// The target keeps its own path even if Pi would name one: the branch resumes
	// this file, not a fresh session.
	r.mu.Lock()
	r.overrides[toSession] = target
	r.mu.Unlock()
	return nil
}

// WriteToolArgs is the flag pair that makes one agent session unable to change files
// or run commands (PRD FR-4.2).
//
// A sidecar shares its parent's checkout, so "read-only" has to be enforced by the
// agent's own tool list; assigning a boolean and never using it was a promise the code
// did not keep. Pi accepts an allowlist/denylist per run, which is exactly this.
func WriteToolArgs() []string {
	return []string{"--exclude-tools", strings.Join(writeTools, ",")}
}

// writeTools are the built-in tools that change the checkout or run commands.
var writeTools = []string{"bash", "edit", "write"}

// ArgsForKind appends the per-kind restrictions to the base arguments.
func ArgsForKind(base []string, kind contracts.SessionKind) []string {
	out := append([]string{}, base...)
	if kind == contracts.KindSidecar {
		out = append(out, WriteToolArgs()...)
	}
	return out
}

// Alive reports whether a session has a working agent process.
//
// It is the question the hub has to ask before trusting a cached subscription: a
// process killed from outside leaves the subscription behind, and answering from it
// turned every later prompt into "bridge closed". Status is not a substitute — it
// reports the *normalizer's* view, which a fake peer (and a just-spawned process)
// legitimately fills with zeros.
func (r *Runtime) Alive(sessionID string) bool {
	s, err := r.get(sessionID)
	if err != nil {
		return false
	}
	return s.bridge.Alive()
}
