package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

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
// There is deliberately no context parameter: the stream outlives the request
// that asked for it (Bridge.Start ignores ctx for the same reason), so the
// handshake gets its own bounded context and the connection is closed by
// Bridge.Close, not by a cancelled HTTP request.
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

	argv := []string{
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
			argv = append(argv, translated)
			continue
		}
		argv = append(argv, arg)
	}
	// The bridge appends --session itself when it spawns a child. Over an attach
	// it does not, so the sandbox-side path is added here.
	argv = append(argv, "--session", containerSession)

	env := sandboxAgentEnv(setup.Env, agentDir)
	a.logger.Info("agent runs inside the sandbox",
		"session", spec.SessionID, "sandbox", target.SandboxID,
		"dir", worktree, "user", agentSandboxUser,
		"session_file", containerSession)

	stream, err := a.attacher.Attach(ctx, target.SandboxID, sandbox.AttachSpec{
		Argv: argv,
		Dir:  worktree,
		Env:  env,
		User: agentSandboxUser,
	})
	if err != nil {
		return nil, fmt.Errorf("agent-in-sandbox: start the agent in %s: %w", target.SandboxID, err)
	}
	return stream, nil
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
	return agentDir, nil
}

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
// Three changes, each with a reason:
//
//   - PI_CODING_AGENT_DIR points at the copy written through the file API, not
//     at the control plane's /data path (which does not exist in the container).
//   - GIT_CONFIG_* entries are dropped. They exist to tell the agent's git that
//     the checkout belongs to another account; inside the sandbox the agent runs
//     as the account that owns it, so there is no "dubious ownership" to lift,
//     and the host paths in them would be lies.
//   - HOME is the sandbox user's, because `docker exec -u` does not set it.
func sandboxAgentEnv(hostEnv []string, agentDir string) []string {
	out := make([]string, 0, len(hostEnv)+1)
	for _, entry := range hostEnv {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(name, "GIT_CONFIG_") {
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
