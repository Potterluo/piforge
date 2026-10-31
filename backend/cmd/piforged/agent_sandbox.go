package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/agentsupervisor"
	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/liveness"
	"github.com/piforge/piforge/backend/internal/pi"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
)

// Running the agent inside the sandbox instead of beside it.
//
// docs/EXECUTION-BOUNDARY.md is the design; this file is stage 1 of it. What it
// changes, in one sentence: instead of `piforged` spawning `pi` as its own child
// with the control plane's view of the shared volume as the cwd, the control
// plane asks the sandbox to start the same argv and bridges the RPC stream over
// that connection.
//
// Everything the bridge does with the stream is unchanged (internal/pi): the
// same JSONL framing, the same command/response correlation, the same events.
// Only the file descriptors' origin moves. That is why this is a Dial and not a
// second agent loop.
//
// Three things have to be true for the agent to work over there, and each one is
// a function below:
//
//   - paths (host → container): the cwd, the session file, the skills dir;
//   - configuration (models.json): written through the sandbox file API, never
//     through the shared volume, and never carrying the provider key — only the
//     per-session gateway token pimodel already writes;
//   - liveness: the existing agent-launcher runs *inside* the sandbox, so the
//     marker at /workspace/.piforge/live/<session> is written by the process
//     that is actually hosting the agent, and the existing probe reads it
//     unchanged.
//
// It does not run for -sandbox local: there is no container, no Attacher and
// nothing to bridge. The flag is rejected at startup instead of silently
// degrading.

// agentSandboxUser is the account the agent runs as inside the container.
//
// Not root, and not an accident: the checkout belongs to the sandbox user (the
// image's gem.sh creates it and chowns /workspace), the panel writes as that
// user, and running the agent as anybody else is what produced the ownership
// defects recorded in docs/SANDBOX-DEPLOY.md §1.4.2 (the panel could not edit a
// file the root agent wrote, and the agent's git refused the checkout as
// "dubious ownership"). One writer, one account.
const agentSandboxUser = "gem"

// agentSandboxHome is that account's home, because `docker exec -u gem` does
// not change HOME and Pi writes caches and telemetry under it.
const agentSandboxHome = "/home/gem"

// agentStartTimeout bounds the handshake of one sandbox launch: resolving the
// sandbox, writing models.json through the file API and opening the stream. It
// does not bound the stream itself — the agent runs for as long as the session
// does, and Bridge.Close is what ends it.
const agentStartTimeout = 2 * time.Minute

// sandboxAgent launches agent processes inside a container-backed sandbox.
type sandboxAgent struct {
	provider sandbox.Remote
	attacher sandbox.Attacher
	keys     sandboxKeys
	store    *store.Store
	// setup renders the per-session model configuration and the environment that
	// points Pi at the gateway. It is the same function the local spawn uses, so
	// the two placements cannot drift on what a session's agent is told.
	setup func(contracts.SessionSpec) agentSetup
	// dataDir is where the control plane keeps per-session agent config
	// (<dataDir>/pi-agents/<session>); the file is read from there and written
	// into the sandbox.
	dataDir string
	// launcher is the path of the control-plane binary *inside the sandbox*: the
	// copy that provides `agent-launcher`. It must exist; a launch without it
	// would run the agent unmarked, which is the failure the marker exists to
	// prevent.
	launcher string
	// piBin is the agent executable inside the sandbox, on PATH.
	piBin  string
	logger *slog.Logger
	// bootedAt is when this control-plane process started. It is how a handshake
	// is classified: an agent whose supervisor started before this instant was
	// hosting a run that survived a restart, one that started after it is new.
	// The distinction matters in a log line a human reads after an incident.
	bootedAt time.Time
}

// newSandboxAgent builds the launcher, or explains why it cannot exist.
func newSandboxAgent(provider sandbox.Remote, attacher sandbox.Attacher, keys sandboxKeys, st *store.Store, setup func(contracts.SessionSpec) agentSetup, opt options, logger *slog.Logger) (*sandboxAgent, error) {
	if provider == nil || attacher == nil {
		return nil, fmt.Errorf("agent-in-sandbox needs a container provider that can attach (docker or runner)")
	}
	if strings.TrimSpace(opt.sandboxAgentLauncher) == "" || strings.TrimSpace(opt.sandboxAgentPi) == "" {
		return nil, fmt.Errorf("agent-in-sandbox needs -sandbox-agent-launcher and -sandbox-agent-pi")
	}
	return &sandboxAgent{
		provider: provider,
		attacher: attacher,
		keys:     keys,
		store:    st,
		setup:    setup,
		dataDir:  opt.dataDir,
		launcher: opt.sandboxAgentLauncher,
		piBin:    opt.sandboxAgentPi,
		logger:   logger,
		bootedAt: time.Now(),
	}, nil
}

// SessionFile places a session's JSONL file where both sides can reach it:
// <pair>/.piforge/sessions/<session>.jsonl, the sandbox's own workspace.
//
// Not inside the checkout: `<repo>/.piforge` would show up as untracked noise in
// the changes panel. Not under /tmp either: the control plane still reads this
// file for CloneSession and reports it in Status, and two /tmp directories in two
// containers are two different files.
//
// It is pi.RuntimeConfig.SessionFileFor. Stage 2 makes the workspace a volume only
// the sandbox mounts, so this path is the *container* path and nothing here can
// open it; Dial maps it one step further (unchanged) and the file is written by Pi
// inside the sandbox.
func (a *sandboxAgent) SessionFile(sessionID string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target, err := a.store.SessionSandboxFor(ctx, sessionID)
	if err != nil || strings.TrimSpace(target.RootDir) == "" {
		// No answer: fall back to the runtime's own default. A session that
		// cannot be resolved cannot be launched either, and Dial says so.
		return ""
	}
	return workspacePath(target.RootDir, ".piforge", "sessions", sessionID+".jsonl")
}

// alignSessionFile fixes the session file's cwd header inside the sandbox.
//
// It is the same rule the local spawn applies (pi.AlignSessionBytes), done by the
// account that owns the file: the control plane's rewrite would leave a
// root-owned file under a gem-owned workspace, and Pi could no longer append to
// its own conversation. Only the first line is involved, and only when it is a
// session header.
func (a *sandboxAgent) alignSessionFile(ctx context.Context, client *aio.Client, hostFile, containerFile, worktree string) error {
	raw, err := os.ReadFile(hostFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", hostFile, err)
	}
	next, changed, err := pi.AlignSessionBytes(raw, worktree)
	if err != nil {
		return fmt.Errorf("align %s: %w", containerFile, err)
	}
	if !changed {
		return nil
	}
	if err := client.WriteBytes(ctx, containerFile, next); err != nil {
		return fmt.Errorf("write %s: %w", containerFile, err)
	}
	a.logger.Info("session file cwd aligned inside the sandbox",
		"file", containerFile, "cwd", worktree)
	return nil
}

// ensureSharedDirs makes the workspace directories the agent writes to exist,
// through the sandbox's own API.
//
// It used to create them on the host and chmod them 0777, because the control
// plane created them as root and the agent runs as gem. With a detached workspace
// (stage 2) there is no host directory to create, and there is no second owner to
// open the tree for: the sandbox user creates these directories over its own API,
// owns them, and writes the session file and the model configuration into them.
// One writer, one account — the same rule the checkout follows.
func ensureSharedDirs(ctx context.Context, client *aio.Client, containerRoot string) error {
	dirs := []string{
		path.Join(containerRoot, ".piforge"),
		path.Join(containerRoot, ".piforge", "sessions"),
	}
	quoted := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		quoted = append(quoted, aio.Quote(dir))
	}
	res, err := client.Exec(ctx, "mkdir -p -- "+strings.Join(quoted, " "), containerRoot)
	if err != nil {
		return fmt.Errorf("agent-in-sandbox: prepare the workspace directories: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("agent-in-sandbox: prepare the workspace directories: exit %d: %s",
			res.ExitCode, strings.TrimSpace(res.Output))
	}
	return nil
}

// workspacePath joins elements under a sandbox workspace root in the root's own
// syntax. A container root is POSIX whatever this process runs on, and
// filepath.Join would turn "/workspace/.piforge" into "\workspace\.piforge" on a
// Windows control plane — a string no other part of the deployment would match.
func workspacePath(root string, parts ...string) string {
	if strings.HasPrefix(root, "/") {
		return path.Join(append([]string{root}, parts...)...)
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

// ContainerWorktree reports the path the agent process itself uses for a
// session's worktree, or "" when the session has none (then the caller keeps the
// host path and the session file lands where it always did).
//
// It is pi.RuntimeConfig.AgentWorktree. The translation has to happen before the
// session file's cwd header is aligned: Pi takes its tools' working directory
// from that header, and a header naming /data/sandboxes/... inside a container
// that has no /data is a session whose tools run in a directory that does not
// exist.
func (a *sandboxAgent) ContainerWorktree(ctx context.Context, spec contracts.SessionSpec) string {
	if strings.TrimSpace(spec.Worktree) == "" {
		return ""
	}
	target, err := a.store.SessionSandboxFor(ctx, spec.SessionID)
	if err != nil {
		return ""
	}
	root, err := a.provider.ContainerPath(target.RootDir)
	if err != nil {
		return ""
	}
	rel, ok := relUnder(target.RootDir, spec.Worktree)
	if !ok {
		return ""
	}
	return path.Join(root, rel)
}

// Dial starts the agent inside the sandbox and returns its stdio.
//
// The signature is pi.RuntimeConfig.Dial: the bridge treats the result exactly
// as it treats a child process's pipes, so every RPC semantic — framing, ids,
// settle detection, subscriptions — is the unchanged code path.
//
// Two steps, in this order, and the order is the stage-3 promise:
//
//  1. **Reattach.** If a supervisor is already listening on
//     /workspace/.piforge/run/<session>.sock, connect to it and use the agent it
//     is hosting. This is what makes a control-plane restart invisible: the same
//     `pi` pid, the bytes that accumulated while nobody was reading, and the run
//     still settling into the same database row.
//  2. **Launch.** Otherwise ask the sandbox to start
//     `piforged agent-supervisor-start`, which starts a detached supervisor and
//     then attaches to it. The supervisor — not this process — owns the agent's
//     file descriptors from that moment on, so a later restart of this process
//     cannot close them.
//
// Both steps speak the same handshake (one JSON line, then raw agent bytes), so
// everything downstream of the handshake is the same stream either way. The only
// difference the runtime is told about is `Reattached()`, which stops it from
// sending `set_model` to a process in the middle of a session.
func (a *sandboxAgent) Dial(spec contracts.SessionSpec, sessionFile string) (io.ReadWriteCloser, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentStartTimeout)
	defer cancel()
	if strings.TrimSpace(spec.Worktree) == "" {
		return nil, fmt.Errorf("agent-in-sandbox: session %s has no worktree, so there is nowhere inside the sandbox for its session file", spec.SessionID)
	}
	target, err := a.store.SessionSandboxFor(ctx, spec.SessionID)
	if err != nil {
		return nil, fmt.Errorf("agent-in-sandbox: resolve the sandbox for session %s: %w", spec.SessionID, err)
	}
	if strings.TrimSpace(target.RootDir) == "" {
		return nil, fmt.Errorf("agent-in-sandbox: sandbox %s reports no workspace", target.SandboxID)
	}
	root, err := a.provider.ContainerPath(target.RootDir)
	if err != nil {
		return nil, fmt.Errorf("agent-in-sandbox: map the workspace of %s: %w", target.SandboxID, err)
	}
	worktree, ok := containerUnder(target.RootDir, root, spec.Worktree)
	if !ok {
		return nil, fmt.Errorf("agent-in-sandbox: worktree %q is outside the sandbox workspace %q", spec.Worktree, target.RootDir)
	}
	containerSession, ok := containerUnder(target.RootDir, root, sessionFile)
	if !ok {
		// A fork adopts the path Pi reported, and Pi now reports the *container*
		// path. Accepting it as-is keeps the branch's file where Pi already wrote
		// it; there is no host-side path to translate in that case, and no host
		// file to align either.
		if !strings.HasPrefix(sessionFile, root+"/") {
			return nil, fmt.Errorf("agent-in-sandbox: session file %q is outside the sandbox workspace %q", sessionFile, target.RootDir)
		}
		containerSession = sessionFile
	}

	// Stage 3: the agent's stdio belongs to a supervisor inside the sandbox.
	// These three paths are the whole contract between the two sides.
	socket := agentsupervisor.SocketPath(root, spec.SessionID)
	stderrLog := agentsupervisor.LogPath(root, spec.SessionID)

	if stream, err := a.reattach(ctx, target.SandboxID, socket, spec.SessionID, worktree); err == nil {
		a.logAgentHandshake(fmt.Sprintf("the agent already in %s", target.SandboxID), spec, stream)
		return stream, nil
	} else {
		// Not an error: the ordinary case is "no supervisor yet". Debug, not warn,
		// because every first launch takes this path.
		a.logger.Debug("no agent supervisor to reattach to; starting one",
			"session", spec.SessionID, "sandbox", target.SandboxID, "reason", err.Error())
	}

	setup := a.setup(spec)
	client, err := a.client(ctx, target.SandboxID)
	if err != nil {
		return nil, err
	}
	if err := ensureSharedDirs(ctx, client, root); err != nil {
		return nil, err
	}
	agentDir, err := a.stageModelConfig(ctx, target.SandboxID, spec, root, client)
	if err != nil {
		return nil, err
	}
	if err := a.alignSessionFile(ctx, client, sessionFile, containerSession, worktree); err != nil {
		// Not fatal: a header the control plane cannot rewrite is a session whose
		// tools may start in the wrong directory, which the agent will report
		// itself. Refusing to launch would turn a degraded session into no session.
		a.logger.Warn("could not align the session file's cwd inside the sandbox",
			"session", spec.SessionID, "error", err.Error())
	}

	agentArgv := []string{
		a.launcher, liveness.LauncherCommand,
		"-root", root,
		"-session", spec.SessionID,
		"-interval", liveness.DefaultBeat.String(),
		"--",
		a.piBin, "--mode", "rpc",
	}
	for _, arg := range setup.Args {
		// Only paths change; flags, model ids and tool lists are identical on both
		// sides. Anything outside the workspace is passed through untouched so an
		// unexpected argument fails at the agent with its own message instead of
		// being silently rewritten here.
		if translated, ok := containerUnder(target.RootDir, root, arg); ok {
			agentArgv = append(agentArgv, translated)
			continue
		}
		agentArgv = append(agentArgv, arg)
	}
	// The bridge appends --session itself when it spawns a child. Over an attach
	// it does not, so the sandbox-side path is added here.
	agentArgv = append(agentArgv, "--session", containerSession)

	// The supervisor start is the outer argv: it is what the runner starts, and
	// it is what makes the agent's stdio the supervisor's pipes instead of the
	// exec stream's.
	argv := []string{
		a.launcher, agentsupervisor.StartCommand,
		"-root", root,
		"-session", spec.SessionID,
		"-socket", socket,
		"-stderr-log", stderrLog,
		"--",
	}
	argv = append(argv, agentArgv...)

	env := sandboxAgentEnv(setup.Env, agentDir)
	// The launch branch can still *attach* to a supervisor that was already
	// there: `agent-supervisor-start` attaches when the socket answers. So the
	// decision of what to log is made from the handshake, not from which code
	// path ran.
	a.logger.Debug("asking the sandbox for an agent supervisor",
		"session", spec.SessionID, "sandbox", target.SandboxID,
		"dir", worktree, "user", agentSandboxUser,
		"session_file", containerSession,
		"socket", socket, "log", stderrLog)

	raw, err := a.attacher.Attach(ctx, target.SandboxID, sandbox.AttachSpec{
		Argv: argv,
		Dir:  worktree,
		Env:  env,
		User: agentSandboxUser,
	})
	if err != nil {
		return nil, fmt.Errorf("agent-in-sandbox: start the agent in %s: %w", target.SandboxID, err)
	}
	stream, err := handshake(raw, spec.SessionID, agentsupervisorLaunchTimeout)
	if err != nil {
		return nil, fmt.Errorf("agent-in-sandbox: the agent supervisor in %s did not come up: %w", target.SandboxID, err)
	}
	a.logAgentHandshake(fmt.Sprintf("the sandbox %s", target.SandboxID), spec, stream)
	return stream, nil
}

// logAgentHandshake is the one line an operator reads to know whether an agent
// survived a restart or was started now.
//
// The classification is the supervisor's own start time against this process's:
// an agent older than this control plane is a survivor, and saying "reattached"
// for a fresh process (or the other way round) would make the restart proof
// meaningless in exactly the case it exists for.
func (a *sandboxAgent) logAgentHandshake(where string, spec contracts.SessionSpec, stream *agentStream) {
	attrs := []any{
		"session", spec.SessionID,
		"target", where,
		"agent_pid", stream.PID,
		"agent_started", stream.AgentStartedAt.UTC().Format(time.RFC3339),
		"replayed_bytes", stream.ReplayedBytes,
		"gap_bytes", stream.GapBytes,
	}
	if !stream.AgentStartedAt.IsZero() && stream.AgentStartedAt.Before(a.bootedAt) {
		attrs = append(attrs, "detail", "this agent process predates the control plane: its supervisor kept it alive and the run continues instead of being interrupted")
		a.logger.Info("reattached to an agent that outlived the control plane", attrs...)
		return
	}
	attrs = append(attrs, "detail", "a new supervisor owns this agent's stdio; the provider key never enters the container")
	a.logger.Info("agent runs inside the sandbox", attrs...)
}

// reattach connects to an existing supervisor, if there is one.
//
// The error is deliberately returned rather than logged as a warning by every
// caller: "no supervisor" is the normal state of a session that has not started
// yet, and it must be cheap. Nothing about it is fatal.
func (a *sandboxAgent) reattach(ctx context.Context, sandboxID, socket, sessionID, worktree string) (*agentStream, error) {
	reattacher, ok := a.attacher.(sandbox.Reattacher)
	if !ok {
		return nil, fmt.Errorf("this provider has no reattach verb")
	}
	spec := sandbox.ReattachSpec{
		Socket: socket,
		// The helper is the sandbox's copy of this binary, in its attach mode:
		// the runner appends `-socket <socket>` to it.
		Helper: []string{a.launcher, agentsupervisor.AttachCommand, "-session", sessionID},
		Dir:    worktree,
		User:   agentSandboxUser,
	}
	// Bounded: a reattach is a local dial inside the container, and a control
	// plane that waits two minutes for it would turn every session start into a
	// stall.
	actx, cancel := context.WithTimeout(ctx, agentsupervisorReattachTimeout)
	defer cancel()
	raw, err := reattacher.Reattach(actx, sandboxID, spec)
	if err != nil {
		return nil, err
	}
	stream, err := handshake(raw, sessionID, agentsupervisorReattachTimeout)
	if err != nil {
		return nil, err
	}
	stream.reattached = true
	return stream, nil
}

// Timeouts for the two handshakes. A reattach is a dial; a launch has to start a
// detached process inside a container.
const (
	agentsupervisorReattachTimeout = 20 * time.Second
	agentsupervisorLaunchTimeout   = 90 * time.Second
)

// agentStream is the supervisor's stream as the bridge sees it: the handshake
// line has already been consumed (and verified), so Read returns agent bytes
// only.
type agentStream struct {
	stream     io.ReadWriteCloser
	reader     *bufio.Reader
	reattached bool
	// PID is the process the supervisor is hosting (the heartbeat launcher, whose
	// child is `pi`), ReplayedBytes is how much of the stream this client missed,
	// GapBytes is how much of it the backlog had already dropped, and
	// AgentStartedAt is when that supervisor started — which is how "survived a
	// restart" is told apart from "started now".
	PID            int
	ReplayedBytes  int64
	GapBytes       int64
	AgentStartedAt time.Time
}

func (s *agentStream) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *agentStream) Write(p []byte) (int, error) { return s.stream.Write(p) }
func (s *agentStream) Close() error                { return s.stream.Close() }

// CloseWrite half-closes the stream so the supervisor's client loop sees EOF —
// which ends the *client*, not the agent.
func (s *agentStream) CloseWrite() error {
	if cw, ok := s.stream.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// Reattached implements pi.Reattached.
func (s *agentStream) Reattached() bool { return s.reattached }

// ProcessID implements the bridge's optional transport-pid interface: the API's
// process_id is the agent's pid inside the sandbox, not this process's child.
func (s *agentStream) ProcessID() int { return s.PID }

// handshake consumes exactly one frame from the stream and verifies it.
//
// The read is bounded: the stream is a live connection to another container, and
// a handshake that never arrives must fail the launch instead of hanging the
// control plane's spawn path.
func handshake(raw io.ReadWriteCloser, sessionID string, timeout time.Duration) (*agentStream, error) {
	reader := bufio.NewReaderSize(raw, 64<<10)
	type result struct {
		frame agentsupervisor.Frame
		err   error
	}
	done := make(chan result, 1)
	go func() {
		frame, err := agentsupervisor.ReadFrame(reader)
		done <- result{frame: frame, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			_ = raw.Close()
			return nil, got.err
		}
		if got.frame.Type != "ready" {
			_ = raw.Close()
			msg := got.frame.Message
			if msg == "" {
				msg = got.frame.Type
			}
			return nil, fmt.Errorf("supervisor answered %q", msg)
		}
		if got.frame.Session != sessionID {
			_ = raw.Close()
			return nil, fmt.Errorf("supervisor hosts session %q, not %q", got.frame.Session, sessionID)
		}
		if got.frame.PID <= 0 {
			_ = raw.Close()
			return nil, fmt.Errorf("supervisor reported no agent process")
		}
		started, _ := time.Parse(time.RFC3339Nano, got.frame.Started)
		return &agentStream{
			stream:         raw,
			reader:         reader,
			PID:            got.frame.PID,
			ReplayedBytes:  got.frame.ReplayBytes,
			GapBytes:       got.frame.Gap,
			AgentStartedAt: started,
		}, nil
	case <-time.After(timeout):
		_ = raw.Close()
		return nil, fmt.Errorf("no handshake within %s", timeout)
	}
}

// stageModelConfig writes the session's models.json into the sandbox and returns
// the container path of the directory Pi must use.
//
// Through the file API, not the shared volume: the whole point of stage 1 is to
// prove the agent's inputs can cross on the sandbox's own door. The host copy
// under <dataDir>/pi-agents is still written by setup(), so -sandbox local and
// the remote placement share one renderer; this only copies the result.
//
// Nothing here carries a provider key. models.json holds the gateway URL and the
// per-session HMAC token, which is scoped to (session, user, provider, model)
// and is the credential the sandbox is supposed to hold.
func (a *sandboxAgent) stageModelConfig(ctx context.Context, sandboxID string, spec contracts.SessionSpec, containerRoot string, client *aio.Client) (string, error) {
	hostFile := filepath.Join(a.dataDir, "pi-agents", spec.SessionID, "models.json")
	raw, err := os.ReadFile(hostFile)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("agent-in-sandbox: read the session's model configuration: %w", err)
		}
		// A session whose provider does not exist yet has no model configuration.
		// The local spawn still starts Pi in that state and lets it answer "No API
		// key found", which the UI shows as a readable failure; refusing to launch
		// here would turn that into a 500. Same behavior, same message.
		a.logger.Warn("the agent starts without a model configuration: this session's provider has no models",
			"session", spec.SessionID)
		raw = nil
	}
	agentDir := path.Join(containerRoot, ".piforge", "agents", spec.SessionID)

	// The directory is created by the sandbox user, so both Pi's own files and
	// the control plane's write end up with one owner.
	if res, err := client.Exec(ctx, "mkdir -p -- "+aio.Quote(agentDir)+" && chmod -R a+rwX -- "+aio.Quote(agentDir), containerRoot); err != nil {
		return "", fmt.Errorf("agent-in-sandbox: prepare %s: %w", agentDir, err)
	} else if res.ExitCode != 0 {
		return "", fmt.Errorf("agent-in-sandbox: prepare %s: exit %d: %s", agentDir, res.ExitCode, strings.TrimSpace(res.Output))
	}
	if len(raw) > 0 {
		if err := client.WriteBytes(ctx, path.Join(agentDir, "models.json"), raw); err != nil {
			return "", fmt.Errorf("agent-in-sandbox: write %s/models.json: %w", agentDir, err)
		}
	}
	// mcp.json follows the same rule as models.json, and it has to be staged for
	// a different reason: without it an agent in the sandbox has no MCP servers
	// at all. What it contains is decided in main.go — under this flag it is a
	// broker URL and a revocable per-session token, never a credential
	// (docs/EXECUTION-BOUNDARY.md §4, stage 3).
	if mcpRaw, merr := os.ReadFile(filepath.Join(a.dataDir, "pi-agents", spec.SessionID, "mcp.json")); merr == nil {
		if err := client.WriteBytes(ctx, path.Join(agentDir, "mcp.json"), mcpRaw); err != nil {
			return "", fmt.Errorf("agent-in-sandbox: write %s/mcp.json: %w", agentDir, err)
		}
	} else if !os.IsNotExist(merr) {
		return "", fmt.Errorf("agent-in-sandbox: read the session's MCP configuration: %w", merr)
	}
	return agentDir, nil
}

// Logs returns the tail of the agent's stderr log, read from inside the sandbox.
//
// Stage 3 item 3 of docs/EXECUTION-BOUNDARY.md. The agent's stderr used to land
// only in the runner's log: the bridge's tail buffer is fed by a locally spawned
// child, and the sandbox agent is not one. The supervisor writes that stream to
// /workspace/.piforge/logs/<session>.log, so the Logs panel can show the same
// bytes the operator would find with `docker exec cat`.
func (a *sandboxAgent) Logs(ctx context.Context, sessionID string) (*contracts.SessionLogs, error) {
	target, err := a.store.SessionSandboxFor(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("agent-in-sandbox: resolve the sandbox for session %s: %w", sessionID, err)
	}
	if strings.TrimSpace(target.RootDir) == "" {
		return nil, fmt.Errorf("agent-in-sandbox: sandbox %s reports no workspace", target.SandboxID)
	}
	root, err := a.provider.ContainerPath(target.RootDir)
	if err != nil {
		return nil, fmt.Errorf("agent-in-sandbox: map the workspace of %s: %w", target.SandboxID, err)
	}
	client, err := a.client(ctx, target.SandboxID)
	if err != nil {
		return nil, err
	}
	logPath := agentsupervisor.LogPath(root, sessionID)
	file, err := client.ReadFile(ctx, logPath)
	if err != nil {
		// No file yet means the supervisor has not started this agent, which is
		// an empty log rather than a broken panel. Say which file was expected so
		// an operator can look for it themselves.
		return &contracts.SessionLogs{
			SessionID: sessionID,
			Lines:     []string{},
			UpdatedAt: time.Now(),
		}, nil
	}
	text := strings.TrimRight(file.Content, "\n")
	lines := []string{}
	truncated := false
	if text != "" {
		lines = strings.Split(text, "\n")
		// The panel is a tail viewer, and the file is bounded at 1 MiB by the
		// supervisor's rotation; a few hundred lines is what a person reads.
		if len(lines) > sessionLogTailLines {
			lines = lines[len(lines)-sessionLogTailLines:]
			truncated = true
		}
	}
	return &contracts.SessionLogs{
		SessionID: sessionID,
		Lines:     lines,
		Truncated: truncated,
		UpdatedAt: time.Now(),
	}, nil
}

// sessionLogTailLines is how much of the agent log the Logs panel shows.
const sessionLogTailLines = 200

// client builds a sandbox API client for one sandbox, with that sandbox's own
// key — the same resolution the panels use.
func (a *sandboxAgent) client(ctx context.Context, sandboxID string) (*aio.Client, error) {
	endpoint, err := a.provider.Endpoint(ctx, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("agent-in-sandbox: endpoint of %s: %w", sandboxID, err)
	}
	key, err := a.keys.For(ctx, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("agent-in-sandbox: api key of %s: %w", sandboxID, err)
	}
	return aio.New(aio.Config{BaseURL: endpoint, APIKey: key, Root: sandbox.ContainerWorkspace})
}

// sandboxAgentEnv adapts the session environment to the sandbox.
//
// Two changes, each with a reason:
//
//   - PI_CODING_AGENT_DIR points at the copy written through the file API, not
//     at the control plane's /data path (which does not exist in the container).
//   - HOME is the sandbox user's, because `docker exec -u` does not set it.
//
// It used to drop GIT_CONFIG_* entries as well: those told a *host-side* agent's
// git that a container-owned checkout belonged to another account. Stage 4 deleted
// that execution, and with it the entries, so there is nothing to drop.
func sandboxAgentEnv(hostEnv []string, agentDir string) []string {
	out := make([]string, 0, len(hostEnv)+1)
	for _, entry := range hostEnv {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if name == "PI_CODING_AGENT_DIR" {
			out = append(out, "PI_CODING_AGENT_DIR="+agentDir)
			continue
		}
		out = append(out, entry)
	}
	return append(out, "HOME="+agentSandboxHome)
}

// relUnder reports hostPath relative to root, using forward slashes, or false
// when it is not inside it.
func relUnder(root, hostPath string) (string, bool) {
	rel, err := filepath.Rel(root, hostPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// containerUnder maps a host path under root to the same path under
// containerRoot, reporting whether it is inside the workspace at all.
func containerUnder(root, containerRoot, hostPath string) (string, bool) {
	rel, ok := relUnder(root, hostPath)
	if !ok {
		return "", false
	}
	return path.Join(containerRoot, rel), true
}
