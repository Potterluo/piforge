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
	"strings"
	"syscall"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/browser"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/crypto"
	"github.com/piforge/piforge/backend/internal/fsx"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/httpapi"
	"github.com/piforge/piforge/backend/internal/llmgw"
	"github.com/piforge/piforge/backend/internal/pi"
	"github.com/piforge/piforge/backend/internal/provision"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/term"
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
	flag.StringVar(&opt.gatewayURL, "gateway-url", envOr("PIFORGE_GATEWAY_URL", ""),
		"public base URL of the model gateway handed to agents (default http://<addr>)")
	flag.StringVar(&opt.defaultProvider, "default-provider", envOr("PIFORGE_DEFAULT_PROVIDER", "shared-anthropic"),
		"provider used when a session has no model_ref")
	flag.StringVar(&opt.chrome, "chrome", envOr("PIFORGE_CHROME", ""),
		"chrome/chromium binary for the browser panel (empty disables it)")
	flag.BoolVar(&opt.headlessBrowser, "headless-browser", envOr("PIFORGE_HEADLESS_BROWSER", "1") != "0",
		"run the sandbox browser headless")
	flag.Parse()

	if err := os.MkdirAll(opt.dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	logger := newLogger(opt)
	logger.Info("starting piforged",
		"addr", opt.addr, "data", abs(opt.dataDir), "dev", opt.dev, "fake_pi", opt.useFakePi)

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

	// 3. Sandboxes. The local provider runs on the host; swapping in the
	// Firecracker provider is a one-line change here and nowhere else.
	sb, err := sandbox.NewLocal(sandbox.LocalConfig{
		Root:          filepath.Join(opt.dataDir, "sandboxes"),
		InitialBranch: "main",
	})
	if err != nil {
		return fmt.Errorf("init sandbox provider: %w", err)
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

	// 4. The agent runtime.
	rt, err := newRuntime(opt, logger, signer, gatewayBase)
	if err != nil {
		return err
	}
	defer rt.Close()

	// 5. Terminals. The manager owns the PTY processes; the HTTP layer only
	// relays bytes (PRD §7.6.1).
	browsers := browser.NewManager(browser.Config{
		Binary:   opt.chrome,
		Headless: opt.headlessBrowser,
	})
	defer browsers.CloseAll()

	terminals := term.New(term.Config{
		RingBytes: 4 << 20,
		// The key is a worktree id: the HTTP layer already proved that the
		// caller owns the session which owns this worktree.
		WorkDir: func(ctx context.Context, worktreeID string) (string, error) {
			return st.WorktreePathByID(ctx, worktreeID)
		},
	})
	defer terminals.CloseAll()

	// 6. Working directories.
	prov := provision.New(provision.Config{Store: st, Sandbox: sb, Git: git})

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
		Store:       st,
		Auth:        authn,
		Runtime:     rt,
		Workspace:   fs,
		Sandbox:     sb,
		Secrets:     box,
		LLMGateway:  gateway,
		Browser:     browsers,
		Term:        termAdapter{m: terminals},
		Provisioner: prov,
		Now:         time.Now,
		Logger:      logger,
	})

	srv := &http.Server{
		Addr:              opt.addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: the SSE endpoint is a long-lived stream.
		IdleTimeout: 120 * time.Second,
	}

	errCh := make(chan error, 1)
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

// newRuntime builds the agent runtime. With -fake-pi it drives the fake peer
// that ships with the repo, which is how development and the e2e suite run
// without a model account.
func newRuntime(opt options, logger *slog.Logger, signer *llmgw.Signer, gatewayBase string) (*pi.Runtime, error) {
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
		EnvFor: func(spec contracts.SessionSpec) []string {
			providerName, modelID := splitModelRef(spec.ModelRef, opt.defaultProvider)
			token, terr := signer.Issue(spec.SessionID, spec.UserID, providerName, modelID, time.Now())
			if terr != nil {
				logger.Warn("cannot mint a model gateway token", "session", spec.SessionID, "error", terr.Error())
				return nil
			}
			base := gatewayBase + "/gw/llm/" + token
			// Point every supported SDK shape at the gateway. A client that
			// honours none of them simply never reaches a model, which is a
			// visible failure rather than a silent one.
			return []string{
				"ANTHROPIC_BASE_URL=" + base,
				"ANTHROPIC_API_KEY=" + token,
				"OPENAI_BASE_URL=" + base + "/v1",
				"OPENAI_API_KEY=" + token,
				"PIFORGE_GATEWAY_TOKEN=" + token,
			}
		},
	}), nil
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
	return nil
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

func abs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
