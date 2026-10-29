// Command piforged is the PiForge control plane.
//
// It owns every piece of state: users, projects, sessions, the append-only
// event log and the SSE fan-out. It also owns the only two things Pi refuses to
// do — sandboxes and credentials — but it never executes agent output itself;
// that happens inside a sandbox process.
//
// Wiring lives here rather than in an init graph so the dependency order is
// readable top to bottom: store → sandbox → git/fs → runtime → auth → router.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/piforge/piforge/backend/internal/aio"
	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/browser"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/crypto"
	"github.com/piforge/piforge/backend/internal/fsx"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/httpapi"
	"github.com/piforge/piforge/backend/internal/isolate"
	"github.com/piforge/piforge/backend/internal/llmgw"
	"github.com/piforge/piforge/backend/internal/pi"
	"github.com/piforge/piforge/backend/internal/pimodel"
	"github.com/piforge/piforge/backend/internal/provision"
	"github.com/piforge/piforge/backend/internal/reaper"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/snapshot"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/term"
	"github.com/piforge/piforge/backend/internal/workspaceremote"
)

func main() {
	if err := run(); err != nil {
		slog.Error("piforged exited with an error", "err", err)
		os.Exit(1)
	}
}

type options struct {
	addr       string
	dataDir    string
	dev        bool
	useFakePi  bool
	fakePiBin  string
	piBin      string
	logLevel   string
	seedAdmin  string
	seedDemo   string
	secureCook bool
	// gatewayURL is the base URL the agent is told to use for model traffic.
	// It must be reachable from inside the sandbox, which is why it is
	// configurable rather than derived from the listen address.
	gatewayURL      string
	defaultProvider string
	// chrome is the browser panel's executable. Empty disables the feature,
	// which the API reports as a typed 422 instead of a broken socket.
	chrome          string
	headlessBrowser bool
	// isolation names the process jail for agent processes.
	isolation       string
	isolationBinary string
	// browserProxy is handed to Chrome via --proxy-server.
	browserProxy string
	// agentIdleTTL is how long an agent process may sit unused before it is stopped.
	agentIdleTTL time.Duration
	// sandboxIdlePause / sandboxReclaimAfter are the two FR-5.3 thresholds.
	// Zero disables each stage.
	sandboxIdlePause    time.Duration
	sandboxReclaimAfter time.Duration
	// sandboxReapInterval is how often the sandbox reaper looks for candidates.
	sandboxReapInterval time.Duration
	// sandboxBrowser is "auto" (the flag's default), "host" or "remote". It holds
	// the resolved placement by the time anything reads it: auto follows the
	// provider, which is remote under docker and host under local.
	sandboxBrowser string
	// sandboxTerminal behaves like sandboxBrowser: remote runs the shell inside the
	// docker sandbox instead of on the host.
	sandboxTerminal string
	// sandboxFiles behaves like sandboxBrowser: remote reads and writes the
	// workspace through the sandbox's file API instead of the host directory.
	sandboxFiles  string
	sandboxAPIKey string
	// sandboxKind picks the sandbox provider: "local" (directories), "docker"
	// (this process runs the Docker CLI) or "runner" (a sandbox-runner service
	// runs it; this process speaks HTTP).
	sandboxKind    string
	sandboxImage   string
	sandboxNetwork string
	sandboxCPUs    string
	sandboxMemory  string
	// sandboxRunnerURL and sandboxRunnerToken address the sandbox-runner service
	// under -sandbox runner; they are rejected under any other provider.
	sandboxRunnerURL   string
	sandboxRunnerToken string
	// webUpstream makes this process serve the UI as well as the API, which is
	// how a container exposes a single port.
	webUpstream string
}

func run() error {
	var opt options
	flag.StringVar(&opt.addr, "addr", envOr("PIFORGE_ADDR", "127.0.0.1:8080"), "listen address")
	flag.StringVar(&opt.dataDir, "data", envOr("PIFORGE_DATA", "./data"), "data directory")
	flag.BoolVar(&opt.dev, "dev", false, "development mode: verbose logging, insecure cookies over http")
	flag.BoolVar(&opt.useFakePi, "fake-pi", false, "drive the built-in fake Pi peer instead of a real pi binary")
	flag.StringVar(&opt.fakePiBin, "fake-pi-bin", "", "path to the fake Pi binary (optional)")
	flag.StringVar(&opt.piBin, "pi", envOr("PIFORGE_PI", "pi"), "path to the real pi binary")
	flag.StringVar(&opt.logLevel, "log", envOr("PIFORGE_LOG", "info"), "log level: debug|info|warn|error")
	flag.StringVar(&opt.seedAdmin, "seed-admin-email", envOr("PIFORGE_SEED_EMAIL", ""), "seed admin email")
	flag.StringVar(&opt.seedDemo, "seed-demo-email", envOr("PIFORGE_SEED_USER_EMAIL", ""), "seed demo user email")
	flag.BoolVar(&opt.secureCook, "secure-cookies", false, "mark session cookies Secure (required behind TLS)")
	flag.StringVar(&opt.webUpstream, "web-upstream", envOr("PIFORGE_WEB_UPSTREAM", ""),
		"Next.js server to proxy page requests to (single-port deployment)")
	flag.StringVar(&opt.gatewayURL, "gateway-url", envOr("PIFORGE_GATEWAY_URL", ""),
		"public base URL of the model gateway handed to agents (default http://<addr>)")
	flag.StringVar(&opt.defaultProvider, "default-provider", envOr("PIFORGE_DEFAULT_PROVIDER", "shared-anthropic"),
		"provider used when a session has no model_ref")
	flag.DurationVar(&opt.agentIdleTTL, "agent-idle-ttl", envDuration("PIFORGE_AGENT_IDLE_TTL", 30*time.Minute),
		"stop an agent process after it has been idle this long")
	flag.DurationVar(&opt.sandboxIdlePause, "sandbox-idle-pause", envDuration("PIFORGE_SANDBOX_IDLE_PAUSE", 30*time.Minute),
		"pause a READY sandbox after it has been idle this long (0 disables)")
	flag.DurationVar(&opt.sandboxReclaimAfter, "sandbox-reclaim-after", envDuration("PIFORGE_SANDBOX_RECLAIM_AFTER", 336*time.Hour),
		"reclaim a PAUSED sandbox after it has been idle this long, snapshotting first (0 disables)")
	flag.DurationVar(&opt.sandboxReapInterval, "sandbox-reap-interval", envDuration("PIFORGE_SANDBOX_REAP_INTERVAL", time.Minute),
		"how often the sandbox idle reaper checks for candidates")
	flag.StringVar(&opt.sandboxKind, "sandbox", envOr("PIFORGE_SANDBOX", "local"),
		"sandbox provider: local (directories), docker (one container per project, CLI here) or runner (a sandbox-runner service runs the CLI)")
	flag.StringVar(&opt.sandboxBrowser, "sandbox-browser", envOr("PIFORGE_SANDBOX_BROWSER", "auto"),
		"browser placement: auto (container providers: remote, local: host), host, or remote (inside the sandbox container)")
	flag.StringVar(&opt.sandboxTerminal, "sandbox-terminal", envOr("PIFORGE_SANDBOX_TERMINAL", "auto"),
		"terminal placement: auto (container providers: remote, local: host), host, or remote (inside the sandbox container)")
	flag.StringVar(&opt.sandboxFiles, "sandbox-files", envOr("PIFORGE_SANDBOX_FILES", "auto"),
		"workspace files: auto (container providers: remote, local: host), host, or remote (read and written through the sandbox)")
	flag.StringVar(&opt.sandboxAPIKey, "sandbox-api-key", envOr("PIFORGE_SANDBOX_API_KEY", ""),
		"fixed API key for every sandbox (debugging override; empty gives each sandbox its own key)")
	flag.StringVar(&opt.sandboxImage, "sandbox-image", envOr("PIFORGE_SANDBOX_IMAGE", ""),
		"image for the docker sandbox provider")
	flag.StringVar(&opt.sandboxNetwork, "sandbox-network", envOr("PIFORGE_SANDBOX_NETWORK", ""),
		"docker network for sandboxes (empty: bridge)")
	flag.StringVar(&opt.sandboxCPUs, "sandbox-cpus", envOr("PIFORGE_SANDBOX_CPUS", ""),
		"CPU limit per sandbox")
	flag.StringVar(&opt.sandboxMemory, "sandbox-memory", envOr("PIFORGE_SANDBOX_MEMORY", ""),
		"memory limit per sandbox, e.g. 2g")
	flag.StringVar(&opt.sandboxRunnerURL, "sandbox-runner-url", envOr("PIFORGE_SANDBOX_RUNNER_URL", ""),
		"base URL of the sandbox-runner service, with -sandbox runner (e.g. http://sandbox-runner:7801)")
	flag.StringVar(&opt.sandboxRunnerToken, "sandbox-runner-token", envOr("PIFORGE_SANDBOX_RUNNER_TOKEN", ""),
		"shared bearer token of the sandbox-runner service, with -sandbox runner")
	flag.StringVar(&opt.chrome, "chrome", envOr("PIFORGE_CHROME", ""),
		"chrome/chromium binary for the browser panel (empty disables it)")
	flag.BoolVar(&opt.headlessBrowser, "headless-browser", envOr("PIFORGE_HEADLESS_BROWSER", "1") != "0",
		"run the sandbox browser headless")
	flag.StringVar(&opt.browserProxy, "browser-proxy", envOr("PIFORGE_BROWSER_PROXY", ""),
		"proxy the sandbox browser should use (host:port)")
	flag.StringVar(&opt.isolation, "isolation", envOr("PIFORGE_ISOLATION", "none"),
		"process jail for agent processes: none|bubblewrap|firejail")
	flag.StringVar(&opt.isolationBinary, "isolation-binary", envOr("PIFORGE_ISOLATION_BINARY", ""),
		"explicit path to the jail launcher")
	flag.Parse()

	// Normalise the provider name before anything compares it: placement resolution
	// ignores case, and the wiring below does not, so "-sandbox Docker" must mean
	// the same thing in both places.
	opt.sandboxKind = normalizedSandboxKind(opt.sandboxKind)
	// The runner credentials and the provider have to agree before anything is
	// created: a typo here would otherwise start a process that looks healthy and
	// fails on the first session.
	if err := validateSandboxFlags(opt.sandboxKind, opt.sandboxRunnerURL, opt.sandboxRunnerToken); err != nil {
		return err
	}

	// Where the browser, the terminal and the file panel look is one decision, not
	// three. Resolve it before anything is created: a contradictory combination
	// must fail here, not surface later as a panel that disagrees with the agent
	// about what is in the workspace.
	resolved, err := resolveSandboxPlacement(opt.sandboxKind, opt.sandboxBrowser, opt.sandboxTerminal, opt.sandboxFiles)
	if err != nil {
		return err
	}
	// From here on the three fields hold resolved values, so every later check is
	// about where a feature actually is, never about how it was requested.
	opt.sandboxBrowser, opt.sandboxTerminal, opt.sandboxFiles =
		string(resolved.Browser), string(resolved.Terminal), string(resolved.Files)

	if err := os.MkdirAll(opt.dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	logger := newLogger(opt)
	logger.Info("starting piforged",
		"addr", opt.addr, "data", abs(opt.dataDir), "dev", opt.dev, "fake_pi", opt.useFakePi)
	logger.Info("sandbox placement resolved",
		"sandbox", opt.sandboxKind,
		"browser", opt.sandboxBrowser,
		"terminal", opt.sandboxTerminal,
		"files", opt.sandboxFiles)
	// A mixed deployment is allowed, never accidental. Saying it out loud is the
	// difference between a bug report about "the panel shows the wrong file" and a
	// log line that described the deployment before it did any work.
	if resolved.Mixed() {
		logger.Warn("sandbox placement is mixed: these features still run on the host",
			"sandbox", opt.sandboxKind,
			"host", strings.Join(resolved.HostFeatures(), ", "),
			"remote", strings.Join(resolved.RemoteFeatures(), ", "),
			"detail", "the agent works inside the container, so a host-side panel is a second view of one workspace; pass remote for every feature, or use -sandbox local for all of them")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. Persistence.
	st, err := store.Open(ctx, filepath.Join(opt.dataDir, "piforge.db"))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	if err := seed(ctx, st, opt, logger); err != nil {
		return err
	}

	// 2. Secrets and the model gateway.
	//
	// Keys are sealed at rest and only opened here, on the egress path. The
	// agent process never sees a real key: it gets a short-lived signed token
	// and a base URL pointing at this process (PRD §5.7.2).
	box, err := crypto.NewFromEnv(opt.dataDir)
	if err != nil {
		return fmt.Errorf("init secret box: %w", err)
	}
	signingKey := make([]byte, 32)
	if _, rerr := rand.Read(signingKey); rerr != nil {
		return fmt.Errorf("init gateway signer: %w", rerr)
	}
	signer, err := llmgw.NewSigner(signingKey, llmgw.DefaultTTL)
	if err != nil {
		return fmt.Errorf("init gateway signer: %w", err)
	}
	gateway, err := llmgw.New(llmgw.Config{
		Store:  st,
		Box:    box,
		Signer: signer,
		Logger: func(format string, args ...any) { logger.Debug(fmt.Sprintf(format, args...)) },
	})
	if err != nil {
		return fmt.Errorf("init model gateway: %w", err)
	}
	gatewayBase := strings.TrimRight(firstNonEmpty(opt.gatewayURL, "http://"+opt.addr), "/")

	// 3. Sandboxes. The local provider runs on the host; the docker provider runs
	// the Docker CLI here; the runner provider asks a service that owns the CLI.
	// Swapping between them is a flag and one branch here, nowhere else.
	var sb contracts.SandboxProvider
	// remoteSB is the lifecycle-independent half the panels use (the sandbox's API
	// endpoint and the host→container path map). It is nil under -sandbox local,
	// which is how the checks below know there is no container.
	var remoteSB sandbox.Remote
	switch opt.sandboxKind {
	case "docker":
		// One container per (user, project): the isolation boundary matches the tenancy
		// boundary the rest of the product already uses. The AIO image publishes one
		// HTTP port, and the checkout is provisioned through that API (as the sandbox
		// user), so the port is published even when every panel feature stays on the
		// host: without it Ensure could not create /workspace/repo. The browser and
		// terminal reach the same endpoint when they are remote.
		dockerProvider, derr := sandbox.NewDocker(sandbox.DockerConfig{
			Image:    opt.sandboxImage,
			Network:  opt.sandboxNetwork,
			CPUs:     opt.sandboxCPUs,
			Memory:   opt.sandboxMemory,
			DataRoot: filepath.Join(opt.dataDir, "sandboxes"),
			APIPort:  8080,
			APIKey:   opt.sandboxAPIKey,
		})
		if derr != nil {
			return fmt.Errorf("init docker sandbox provider: %w", derr)
		}
		sb = dockerProvider
		remoteSB = dockerProvider
	case "runner":
		// The same container rules, performed by a process that owns the Docker
		// socket. This process needs neither the CLI nor a daemon: it has one HTTP
		// contract, a token, and no way to run anything on the host.
		//
		// The DataRoot is this process's view of the shared sandbox directory. The
		// runner reports the same path in RootDir, which is what makes RootDir,
		// ContainerPath and provision's host git keep working unchanged.
		runnerProvider, rerr := sandbox.NewRunner(sandbox.RunnerConfig{
			BaseURL:  opt.sandboxRunnerURL,
			Token:    opt.sandboxRunnerToken,
			DataRoot: filepath.Join(opt.dataDir, "sandboxes"),
			Image:    opt.sandboxImage,
			APIKey:   opt.sandboxAPIKey,
			CPUs:     opt.sandboxCPUs,
			Memory:   opt.sandboxMemory,
			Network:  opt.sandboxNetwork,
			APIPort:  8080,
		})
		if rerr != nil {
			return fmt.Errorf("init runner sandbox provider: %w", rerr)
		}
		sb = runnerProvider
		remoteSB = runnerProvider
		logger.Info("sandbox lifecycle runs through the runner",
			"url", opt.sandboxRunnerURL,
			"detail", "no docker CLI and no daemon are needed in this process")
	default:
		local, lerr := sandbox.NewLocal(sandbox.LocalConfig{
			Root:          filepath.Join(opt.dataDir, "sandboxes"),
			InitialBranch: "main",
		})
		if lerr != nil {
			return fmt.Errorf("init sandbox provider: %w", lerr)
		}
		sb = local
	}

	// 3. Git and the filesystem. fsx takes gitx as its git backend so file
	// listings and diffs agree about what is modified.
	git := gitx.New(gitx.Config{})
	fs := fsx.New(fsx.Config{
		MaxEntriesPerDir: 2000,
		SearchLimit:      500,
		Git:              git,
		GitState:         gitLetters(git),
		Audit: func(ev fsx.AuditEvent) {
			logger.Debug("file mutation", "path", ev.Path, "action", ev.Action, "bytes", ev.Size)
		},
	})

	// The panel's file tree, preview and editor read the host directory unless
	// asked otherwise. With a docker sandbox, "otherwise" is the default: the agent
	// edits files inside the container, so the panel must ask the container too
	// or the two views drift. Git goes through the same bridge, because the two
	// sides disagreeing about what is staged is the same bug one layer up.
	//
	// resolveSandboxPlacement already rejected remote without a container, so the
	// only thing left to guard is the provider itself.
	var files contracts.Workspace = fs
	if opt.sandboxFiles == "remote" {
		if remoteSB == nil {
			return fmt.Errorf("remote workspace files need a container sandbox provider (-sandbox docker or -sandbox runner)")
		}
		fileBackend := &sandboxFileBackend{provider: remoteSB, store: st, keys: sandboxKeys{store: st, fixed: opt.sandboxAPIKey}}
		executor := workspaceremote.ShellExecutor{Backend: fileBackend}
		remoteFiles, rerr := workspaceremote.New(workspaceremote.Config{
			Backend: fileBackend,
			// Same runner, same parsing: only the machine executing git changes.
			Git: gitx.New(gitx.Config{Exec: executor.Run}),
			Audit: func(ev fsx.AuditEvent) {
				logger.Debug("file mutation via sandbox", "path", ev.Path, "action", ev.Action, "bytes", ev.Size)
			},
		})
		if rerr != nil {
			return fmt.Errorf("init remote workspace: %w", rerr)
		}
		files = remoteFiles
		logger.Info("workspace files are read and written through the sandbox",
			"sandbox", opt.sandboxKind)
	}

	// 4. The agent runtime.
	launcher, err := isolate.New(isolate.Options{
		Kind:   opt.isolation,
		Binary: opt.isolationBinary,
		// The agent writes its worktree and its session file; everything else
		// on the host is read-only inside the jail.
		WritableDirs: []string{opt.dataDir},
		ShareNetwork: true,
	})
	if err != nil {
		return err
	}
	if launcher.Name() != "none" {
		logger.Info("agent processes run inside a jail",
			"launcher", launcher.Name(),
			"covers", "agent process, terminal shells, sandbox browser")
	}

	// setupFor renders each session's model configuration and the environment
	// that points Pi at the gateway. It runs at spawn time, so a provider change
	// applies to the next session without touching a running sandbox.
	resolvedModels := struct {
		sync.Mutex
		bySession map[string]string
	}{bySession: map[string]string{}}

	setupFor := func(spec contracts.SessionSpec) agentSetup {
		providerName, modelID := splitModelRef(spec.ModelRef, opt.defaultProvider)

		models := []string{}
		api := "openai-completions"
		if provider, _, perr := st.ResolveProvider(ctx, spec.UserID, providerName); perr == nil {
			api = piAPI(provider.API)
			for _, m := range provider.Models {
				if strings.TrimSpace(m.ID) != "" {
					models = append(models, m.ID)
				}
			}
		}
		if modelID != "" {
			// Prepend the pinned model unless the provider already lists it, so
			// models.json does not carry the same id twice.
			dupe := false
			for _, existing := range models {
				if existing == modelID {
					dupe = true
					break
				}
			}
			if !dupe {
				models = append([]string{modelID}, models...)
			}
		}
		if len(models) == 0 {
			logger.Warn("no models are configured for this session's provider",
				"session", spec.SessionID, "provider", providerName)
			return agentSetup{}
		}

		token, terr := signer.Issue(spec.SessionID, spec.UserID, providerName, modelID, time.Now())
		if terr != nil {
			logger.Warn("cannot mint a model gateway token", "session", spec.SessionID, "error", terr.Error())
			return agentSetup{}
		}
		gatewayURL := gatewayBase + "/gw/llm/" + token

		res, werr := pimodel.Write(filepath.Join(opt.dataDir, "pi-agents", spec.SessionID), pimodel.Spec{
			GatewayURL:   gatewayURL,
			Token:        token,
			Models:       models,
			APIKind:      api,
			ProviderName: providerName,
		})
		if werr != nil {
			logger.Warn("cannot write the pi model configuration",
				"session", spec.SessionID, "error", werr.Error())
			return agentSetup{}
		}

		resolvedModels.Lock()
		resolvedModels.bySession[spec.SessionID] = res.Model
		resolvedModels.Unlock()

		// MCP servers reach the agent the only way Pi accepts them: a file in the
		// session's agent directory. Credentials are decrypted here, per session, and
		// written as environment variables — never on a command line where `ps` would
		// show them.
		if entries, merr := mcpEntriesFor(ctx, st, spec.UserID); merr != nil {
			logger.Warn("cannot prepare MCP servers for this session",
				"session", spec.SessionID, "error", merr.Error())
		} else if _, werr := pimodel.WriteMCP(res.Dir, entries); werr != nil {
			logger.Warn("cannot write the MCP configuration",
				"session", spec.SessionID, "error", werr.Error())
		}

		return agentSetup{
			// A sidecar shares its parent's checkout, so it must not be able to write
			// to it (FR-4.2). The restriction is passed to Pi as a tool denylist for
			// this run, not merely recorded as an intention.
			Args: pi.ArgsForKind(res.Args, spec.Kind),
			// PI_CODING_AGENT_DIR is the authoritative pointer; the *_BASE_URL
			// variables stay for clients that read them directly.
			Env: []string{
				"PI_CODING_AGENT_DIR=" + res.Dir,
				"ANTHROPIC_BASE_URL=" + gatewayURL,
				"ANTHROPIC_API_KEY=" + token,
				"OPENAI_BASE_URL=" + gatewayURL + "/v1",
				"OPENAI_API_KEY=" + token,
				"PIFORGE_GATEWAY_TOKEN=" + token,
			},
		}
	}

	// Pi knows the model under the generated gateway provider, not under the
	// user's provider name, so the reference has to be translated before it is
	// sent over RPC.
	modelFor := func(spec contracts.SessionSpec, ref string) string {
		resolvedModels.Lock()
		defer resolvedModels.Unlock()
		if model, ok := resolvedModels.bySession[spec.SessionID]; ok && model != "" {
			return model
		}
		return ref
	}

	rt, err := newRuntime(opt, logger, setupFor, modelFor, launcher)
	if err != nil {
		return err
	}
	defer rt.Close()

	// 5. Terminals. The manager owns the PTY processes; the HTTP layer only
	// relays bytes (PRD §7.6.1).
	browserCfg := browser.Config{
		Binary:   opt.chrome,
		Headless: opt.headlessBrowser,
		Proxy:    opt.browserProxy,
		Launcher: launcher,
	}
	// One resolver for every sandbox API call this process makes: the file panel,
	// the terminal, the browser and the worktree request all present the same key
	// the container was created with.
	keys := sandboxKeys{store: st, fixed: opt.sandboxAPIKey}
	if remoteSB != nil {
		if strings.TrimSpace(opt.sandboxAPIKey) != "" {
			logger.Warn("sandbox API keys are fixed by -sandbox-api-key for every sandbox; this is for debugging one deployment, not for tenants",
				"sandbox", opt.sandboxKind,
				"detail", "leave -sandbox-api-key empty to let each sandbox get its own key, which is what keeps one leaked key from opening every tenant")
		} else {
			logger.Info("sandbox API keys are per sandbox",
				"sandbox", opt.sandboxKind,
				"detail", "each container gets a 32-byte random key at creation, sealed in the sandboxes row")
		}
	}

	// With a docker sandbox that runs an AIO image, the browser lives in that container:
	// the panel then shows the same machine the agent edits, which is the whole point of
	// moving the sandbox into a container. This is the resolved placement, so it is
	// remote by default under docker — not only when the flag is spelled out.
	if opt.sandboxBrowser == "remote" {
		browserCfg.RemoteAPIKey = opt.sandboxAPIKey
		browserCfg.RemoteKey = keys.For
		browserCfg.Remote = func(ctx context.Context, sandboxID string) (string, string, error) {
			if remoteSB == nil {
				return "", "", fmt.Errorf("remote browser needs a container sandbox provider")
			}
			endpoint, err := remoteSB.Endpoint(ctx, sandboxID)
			if err != nil {
				return "", "", err
			}
			apiKey, kerr := keys.For(ctx, sandboxID)
			if kerr != nil {
				return "", "", kerr
			}
			client, cerr := aio.New(aio.Config{BaseURL: endpoint, APIKey: apiKey})
			if cerr != nil {
				return "", "", cerr
			}
			info, ierr := client.BrowserInfo(ctx)
			if ierr != nil {
				return "", "", ierr
			}
			return info.CDPURL, endpoint, nil
		}
	}
	browsers := browser.NewManager(browserCfg)
	defer browsers.CloseAll()

	// With a docker sandbox that runs an AIO image, the terminal lives in that
	// container too: the shell sees the same filesystem the agent edits. Like the
	// browser, this is the resolved placement, so docker gets it by default.
	termCfg := term.Config{
		RingBytes: 4 << 20,
		Launcher:  launcher,
		// The key is a worktree id: the HTTP layer already proved that the
		// caller owns the session which owns this worktree.
		WorkDir: func(ctx context.Context, worktreeID string) (string, error) {
			return st.WorktreePathByID(ctx, worktreeID)
		},
	}
	if opt.sandboxTerminal == "remote" {
		termCfg.Remote = func(ctx context.Context, worktreeID string) (*aio.Client, string, string, error) {
			if remoteSB == nil {
				return nil, "", "", fmt.Errorf("remote terminal needs a container sandbox provider")
			}
			worktree, werr := st.WorktreeByID(ctx, worktreeID)
			if werr != nil {
				logger.Warn("cannot resolve the worktree for a remote terminal",
					"worktree", worktreeID, "error", werr.Error())
				return nil, "", "", werr
			}
			endpoint, eerr := remoteSB.Endpoint(ctx, worktree.SandboxID)
			if eerr != nil {
				logger.Warn("cannot reach the sandbox for a remote terminal; falling back to a host PTY",
					"sandbox", worktree.SandboxID, "error", eerr.Error())
				return nil, "", "", eerr
			}
			cwd, cerr := remoteSB.ContainerPath(worktree.Path)
			if cerr != nil {
				logger.Warn("cannot map the worktree into the sandbox; falling back to a host PTY",
					"worktree", worktree.Path, "error", cerr.Error())
				return nil, "", "", cerr
			}
			apiKey, kerr := keys.For(ctx, worktree.SandboxID)
			if kerr != nil {
				logger.Warn("cannot resolve the sandbox's API key; falling back to a host PTY",
					"sandbox", worktree.SandboxID, "error", kerr.Error())
				return nil, "", "", kerr
			}
			client, aerr := aio.New(aio.Config{
				BaseURL: endpoint,
				APIKey:  apiKey,
				Root:    "/workspace",
			})
			if aerr != nil {
				logger.Warn("cannot build the sandbox terminal client", "error", aerr.Error())
				return nil, "", "", aerr
			}
			// A fresh id per tab: the image treats create as idempotent and keeps
			// an exited session wedged, so reusing a tab's old id would replay
			// stale output or a dead shell.
			suffix := make([]byte, 8)
			if _, rerr := rand.Read(suffix); rerr != nil {
				return nil, "", "", fmt.Errorf("remote terminal session id: %w", rerr)
			}
			return client, "pf-term-" + hex.EncodeToString(suffix), cwd, nil
		}
	}
	terminals := term.New(termCfg)
	defer terminals.CloseAll()

	// 6. Working directories.
	prov := provision.New(provision.Config{
		Store:   st,
		Sandbox: sb,
		Git:     git,
		// Uploaded bundles live here; without it provisioning would mount
		// nothing and every skill would silently be metadata only.
		SkillsDir: filepath.Join(opt.dataDir, "skills"),
	})

	// 6. Authentication.
	authn, err := auth.New(st, st, auth.Config{
		SessionTTL: 24 * time.Hour,
		Secure:     opt.secureCook,
	})
	if err != nil {
		return fmt.Errorf("init auth: %w", err)
	}

	// 7. HTTP.
	handler := httpapi.NewRouter(httpapi.Deps{
		Store:           st,
		Auth:            authn,
		Runtime:         rt,
		Workspace:       files,
		DefaultProvider: opt.defaultProvider,
		Sandbox:         sb,
		Secrets:         box,
		WebUpstream:     opt.webUpstream,
		SkillsDir:       filepath.Join(opt.dataDir, "skills"),
		LLMGateway:      gateway,
		Browser:         browsers,
		Term:            termAdapter{m: terminals},
		Provisioner:     prov,
		Now:             time.Now,
		Logger:          logger,
	})

	srv := &http.Server{
		Addr:              opt.addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: the SSE endpoint is a long-lived stream.
		IdleTimeout: 120 * time.Second,
	}

	errCh := make(chan error, 1)
	// Idle agents are stopped on their own (FR-5.x): a session keeps its process warm
	// for speed, but nothing else bounded how many of those a long-lived deployment
	// accumulates. The default is generous; the e2e harness sets it low so a suite of
	// eighty sessions does not leave eighty children behind.
	go rt.StartIdleReaper(ctx, opt.agentIdleTTL, time.Minute)

	// Sandboxes idle-pause and are reclaimed on their own (FR-5.3). The two
	// thresholds are independent so an operator can pause without ever deleting,
	// or leave a long PAUSED window. Reclaim always writes the manifest first;
	// the reaper refuses to destroy if that step fails.
	if opt.sandboxIdlePause > 0 || opt.sandboxReclaimAfter > 0 {
		snapshots, serr := snapshot.New(snapshot.Config{
			Root:  filepath.Join(opt.dataDir, "snapshots"),
			Store: st,
		})
		if serr != nil {
			return fmt.Errorf("init sandbox snapshot writer: %w", serr)
		}
		rp := reaper.New(reaper.Config{
			Store:        st,
			Sandbox:      sb,
			Snapshots:    snapshots,
			Logger:       logger,
			Now:          time.Now,
			PauseAfter:   opt.sandboxIdlePause,
			ReclaimAfter: opt.sandboxReclaimAfter,
			Interval:     opt.sandboxReapInterval,
			// Pausing is not just a label. The manual pause handler already
			// stops the agents on a sandbox; the background pause must do the
			// same or a "PAUSED" sandbox keeps executing work.
			OnPaused: func(pctx context.Context, s *contracts.Sandbox) {
				stopAgentsOnSandbox(pctx, logger, st, rt, s)
			},
		})
		logger.Info("sandbox idle policy enabled",
			"pause_after", opt.sandboxIdlePause.String(),
			"reclaim_after", opt.sandboxReclaimAfter.String(),
			"interval", opt.sandboxReapInterval.String(),
			"snapshots", abs(filepath.Join(opt.dataDir, "snapshots")))
		go rp.Start(ctx)
	} else {
		logger.Info("sandbox idle policy disabled (both thresholds are 0)")
	}

	// Scratch sessions (PRD FR-1.10) expire on their own: a quick question should
	// not need a cleanup step from the user.
	go func() {
		sweep := func() {
			n, err := st.SweepEphemeral(ctx, time.Now().Add(-6*time.Hour))
			if err != nil {
				logger.Warn("could not sweep ephemeral sessions", "error", err.Error())
				return
			}
			if n > 0 {
				logger.Info("swept ephemeral sessions", "count", n)
			}
		}
		sweep()
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()

	go func() {
		logger.Info("listening", "url", "http://"+opt.addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// termAdapter narrows *term.Manager to the interface httpapi declares, so the
// HTTP package never imports the terminal implementation.
type termAdapter struct{ m *term.Manager }

func (a termAdapter) Open(ctx context.Context, sandboxID, tabID, shell string) (*httpapi.TermSession, error) {
	info, err := a.m.Open(ctx, sandboxID, tabID, shell)
	if err != nil {
		return nil, err
	}
	return &httpapi.TermSession{
		TabID: info.TabID, Shell: info.Shell, PID: info.PID,
		Start: info.Start, End: info.End,
	}, nil
}

func (a termAdapter) Write(tabID string, p []byte) error { return a.m.Write(tabID, p) }
func (a termAdapter) Resize(tabID string, cols, rows int) error {
	return a.m.Resize(tabID, cols, rows)
}
func (a termAdapter) Close(tabID string) error { return a.m.Close(tabID) }
func (a termAdapter) Read(tabID string, from int64) ([]byte, int64, bool) {
	return a.m.Read(tabID, from)
}

// stopAgentsOnSandbox stops every agent process whose worktree belongs to one
// sandbox. It mirrors the manual pause handler: "paused" should describe the
// machine, not only the row. Errors are logged and do not fail the transition;
// a process that will not die is the agent reaper's problem next.
func stopAgentsOnSandbox(ctx context.Context, logger *slog.Logger, st *store.Store, rt *pi.Runtime, sb *contracts.Sandbox) {
	sessions, err := st.ListSessions(ctx, sb.UserID, store.SessionFilter{Limit: 500})
	if err != nil {
		logger.Warn("sandbox idle: cannot list sessions to stop agents",
			"sandbox", sb.ID, "error", err.Error())
		return
	}
	for _, sess := range sessions {
		if sess.WorktreeID == "" {
			continue
		}
		wt, werr := st.WorktreeByID(ctx, sess.WorktreeID)
		if werr != nil || wt.SandboxID != sb.ID {
			continue
		}
		if serr := rt.Stop(ctx, sess.ID); serr != nil {
			logger.Warn("sandbox idle: could not stop an agent on the paused sandbox",
				"sandbox", sb.ID, "session", sess.ID, "error", serr.Error())
		}
	}
}

// newRuntime builds the agent runtime. With -fake-pi it drives the fake peer
// that ships with the repo, which is how development and the e2e suite run
// without a model account.
func newRuntime(opt options, logger *slog.Logger, setup func(contracts.SessionSpec) agentSetup, modelFor func(contracts.SessionSpec, string) string, launcher isolate.Launcher) (*pi.Runtime, error) {
	bin := opt.piBin
	args := []string{"--mode", "rpc"}

	if opt.useFakePi {
		resolved, err := resolveFakePi(opt)
		if err != nil {
			return nil, err
		}
		bin = resolved
		// The fake peer has no CLI of its own beyond these flags.
		args = []string{"-delay", envOr("PIFORGE_FAKE_PI_DELAY_MS", "5")}
		logger.Info("using the fake pi peer", "binary", resolved)
	}

	return pi.NewRuntime(pi.RuntimeConfig{
		Config: pi.Config{
			BinaryPath: bin,
			Args:       args,
			Stderr:     os.Stderr,
		},
		Launcher: launcher,
		ModelFor: modelFor,
		ArgsFor: func(spec contracts.SessionSpec) []string {
			return setup(spec).Args
		},
		EnvFor: func(spec contracts.SessionSpec) []string {
			return setup(spec).Env
		},
	}), nil
}

// agentSetup is per-session launch configuration: the model selection Pi needs
// and the environment that points it at the gateway.
type agentSetup struct {
	Env  []string
	Args []string
}

// piAPI maps a stored provider api onto the name Pi uses in models.json.
func piAPI(api string) string {
	switch api {
	case "anthropic-messages":
		return "anthropic-messages"
	case "openai-responses":
		return "openai-responses"
	default:
		return "openai-completions"
	}
}

// splitModelRef splits "provider/model" into its parts, falling back to the
// configured default provider when a session has no explicit model.
func splitModelRef(ref, fallback string) (string, string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fallback, ""
	}
	if provider, model, ok := strings.Cut(ref, "/"); ok {
		if provider == "" {
			return fallback, model
		}
		return provider, model
	}
	return ref, ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// resolveFakePi finds (or builds) the fake peer binary.
//
// Build order: explicit flag → explicit env → a sibling binary next to this
// executable → `go build` into the data dir. The last step needs a Go toolchain
// and only matters for `go run ./cmd/piforged` during development.
func resolveFakePi(opt options) (string, error) {
	if opt.fakePiBin != "" {
		return opt.fakePiBin, nil
	}
	if v := os.Getenv("PIFORGE_FAKE_PI"); v != "" {
		return v, nil
	}

	exe := "fakepi"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if self, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(self), exe)
		if _, err := os.Stat(sibling); err == nil {
			return sibling, nil
		}
	}

	out := filepath.Join(opt.dataDir, exe)
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}

	// Fall back to building it. This only works when the source tree is
	// reachable from the working directory, which is the `go run` case.
	cmd := exec.Command("go", "build", "-o", out, "./cmd/fakepi")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build the fake pi peer (%s): %w — pass -fake-pi-bin to point at a prebuilt one", out, err)
	}
	return out, nil
}

// seed creates the first accounts. It is idempotent: an existing admin is left
// alone, so restarting the server never resets a password.
func seed(ctx context.Context, st *store.Store, opt options, logger *slog.Logger) error {
	res, err := st.EnsureAdminSeed(ctx, store.SeedOptions{
		AdminEmail:    opt.seedAdmin,
		DemoEmail:     opt.seedDemo,
		AdminPassword: envOr("PIFORGE_SEED_PASSWORD", ""),
		DemoPassword:  envOr("PIFORGE_SEED_USER_PASSWORD", ""),
		HashPassword:  auth.HashPassword,
		SharedProvider: &store.ProviderInput{
			Name:    "shared-anthropic",
			API:     "anthropic-messages",
			BaseURL: "",
			Models: []contracts.ModelSpec{{
				ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5",
				ContextWindow: 200000, MaxTokens: 16384, Reasoning: true,
			}},
		},
	})
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	if !res.Created {
		return nil
	}

	// Printed once, on first boot, and never again.
	fmt.Println()
	fmt.Println("──────────────────────────────────────────────────────")
	fmt.Println(" PiForge first boot — these credentials are shown once")
	fmt.Println("──────────────────────────────────────────────────────")
	if res.Admin != nil {
		fmt.Printf("  admin: %s / %s\n", res.Admin.Email, res.AdminPassword)
	}
	if res.Demo != nil {
		fmt.Printf("  user : %s / %s\n", res.Demo.Email, res.DemoPassword)
	}
	fmt.Println("──────────────────────────────────────────────────────")
	fmt.Println()
	logger.Info("seeded the first accounts")

	// A deployment with no skills makes the feature look broken: the page says "write
	// one" and there is nothing to try. Three shared skills ship with the binary
	// (FR-14.7), installed once and left alone afterwards so an operator's edits survive.
	if res.Created {
		skillsDir := filepath.Join(opt.dataDir, "skills")
		if err := os.MkdirAll(skillsDir, 0o700); err != nil {
			return fmt.Errorf("prepare the skills directory: %w", err)
		}
		created, serr := st.EnsureBuiltinSkills(ctx, res.Admin.ID, func(skillID string, files map[string]string) (string, string, error) {
			return writeBundle(skillsDir, skillID, files)
		})
		if serr != nil {
			logger.Warn("could not install the built-in skills", "error", serr.Error())
		} else if created > 0 {
			logger.Info("installed the built-in skills", "count", created)
		}
	}
	return nil
}

// writeBundle materialises one skill's files under the skills root and returns the bundle
// key and content hash, using the same layout the HTTP handler writes.
func writeBundle(skillsRoot, skillID string, files map[string]string) (string, string, error) {
	root := filepath.Join(skillsRoot, skillID)
	staging := root + ".tmp"
	if err := os.RemoveAll(staging); err != nil {
		return "", "", err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	hasher := sha256.New()
	for _, rel := range names {
		target := filepath.Join(staging, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", "", err
		}
		if err := os.WriteFile(target, []byte(files[rel]), 0o644); err != nil {
			return "", "", err
		}
		fmt.Fprintf(hasher, "%s\x00%s\x00", rel, files[rel])
	}
	if err := os.Rename(staging, root); err != nil {
		return "", "", err
	}
	return skillID, hex.EncodeToString(hasher.Sum(nil)), nil
}

// gitLetters adapts git status into the per-path letters the file tree shows.
func gitLetters(git *gitx.Runner) func(ctx context.Context, root string) (map[string]string, error) {
	return func(ctx context.Context, root string) (map[string]string, error) {
		st, err := git.Status(ctx, root)
		if err != nil || st == nil {
			// A sandbox that is not a repo (yet) simply has no letters.
			return nil, nil
		}
		out := make(map[string]string, len(st.Changes))
		for _, c := range st.Changes {
			out[c.Path] = letter(c.Index, c.Worktree)
		}
		for _, c := range st.Conflicts {
			out[c] = "!"
		}
		return out, nil
	}
}

func letter(index, worktree string) string {
	pick := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" || s == "." {
			return ""
		}
		return string(s[0])
	}
	if i := pick(index); i != "" && i != "?" {
		return i
	}
	if w := pick(worktree); w != "" {
		if w == "?" {
			return "U"
		}
		return w
	}
	return ""
}

func newLogger(opt options) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(opt.logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	if opt.dev && opt.logLevel == "" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envDuration reads a duration from the environment, falling back to def when it is
// missing or unparseable — a typo in an env var should not stop the server.
func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func abs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// mcpEntriesFor turns a user's stored MCP servers into what Pi reads, decrypting each
// credential for this launch only.
func mcpEntriesFor(ctx context.Context, st *store.Store, userID string) ([]pimodel.MCPEntry, error) {
	servers, err := st.ListMCPServers(ctx, userID)
	if err != nil {
		return nil, err
	}
	entries := make([]pimodel.MCPEntry, 0, len(servers))
	for _, server := range servers {
		if !server.Enabled {
			continue
		}
		entry := pimodel.MCPEntry{
			Name:      server.Name,
			Transport: string(server.Transport),
			Command:   server.Command,
			Args:      server.Args,
			URL:       server.URL,
		}
		if server.HasSecrets {
			secret, serr := st.MCPServerSecrets(ctx, userID, server.ID)
			if serr != nil {
				_ = serr // logged by the caller; a missing credential is not fatal to the session
			} else if env := pimodel.ParseMCPSecrets(secret); len(env) > 0 {
				entry.Env = env
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
