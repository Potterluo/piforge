package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/liveness"
	"github.com/piforge/piforge/backend/internal/pi"
	"github.com/piforge/piforge/backend/internal/reconcile"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
)

// The wiring test: a real store, the real runner client, the real probe this
// binary builds, a fake runner and a fake sandbox speaking the AIO API. It is
// the restart case with -sandbox runner, end to end minus Docker:
//
//   - the "restarted" process has an empty in-process registry (nothing spawned),
//   - the fake runner reports a live sandbox whose heartbeat is fresh,
//   - boot reconciliation must leave the session and its started run alone.
//
// And the same wiring must still repair the row once the sandbox says the
// heartbeat is gone.
type livenessFixture struct {
	store      *store.Store
	userID     string
	session    *contracts.Session
	runID      string
	runner     *sandbox.Runner
	fakeRunner *fakeRunner
	sandbox    *fakeAIO
	logs       *strings.Builder
	rec        *reconcile.Reconciler
	sandboxID  string
}

// fakeAIO answers /v1/shell/exec the way an AIO sandbox does, with a switch for
// the three answers the probe distinguishes.
type fakeAIO struct {
	mode  atomic.Value // "alive" | "nomark" | "broken"
	calls atomic.Int64
	key   atomic.Value
	url   string
}

func newFakeAIO(t *testing.T) *fakeAIO {
	t.Helper()
	a := &fakeAIO{}
	a.mode.Store("alive")
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/shell/exec", func(w http.ResponseWriter, r *http.Request) {
		a.calls.Add(1)
		a.key.Store(r.Header.Get("X-AIO-API-Key"))
		var body struct {
			Command string `json:"command"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if a.mode.Load() == "broken" {
			http.Error(w, "sandbox is on fire", http.StatusInternalServerError)
			return
		}
		output := "PF_LIVE_NOMARK"
		if a.mode.Load() == "alive" {
			output = strconv.FormatInt(time.Now().Unix(), 10)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"output": output, "exit_code": 0},
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	a.url = server.URL
	return a
}

// fakeRunner answers GET /v1/sandboxes/{id} with the record shape the runner
// client expects, pointing at the fake sandbox endpoint.
type fakeRunner struct {
	url       string
	sandboxID string
	rootDir   string
	sandbox   string
	gets      atomic.Int64
}

func newFakeRunner(t *testing.T, sandboxID, rootDir, sandboxURL string) *fakeRunner {
	t.Helper()
	fr := &fakeRunner{sandboxID: sandboxID, rootDir: rootDir, sandbox: sandboxURL}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sandboxes/{id}", func(w http.ResponseWriter, r *http.Request) {
		fr.gets.Add(1)
		if r.PathValue("id") != fr.sandboxID {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sandbox.RunnerRecord{
			ID:                fr.sandboxID,
			State:             contracts.SandboxReady,
			RootDir:           fr.rootDir,
			ContainerPathRoot: sandbox.ContainerWorkspace,
			Endpoint:          fr.sandbox,
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	fr.url = server.URL
	return fr
}

func newLivenessFixture(t *testing.T) *livenessFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "liveness.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	user, err := st.CreateUser(ctx, store.NewUser{
		Email: "liveness@example.test", DisplayName: "L", PasswordHash: "x",
		Role: contracts.RoleUser, Status: "active",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, err := st.CreateProject(ctx, user.ID, store.NewProject{Name: "p", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	rootDir := filepath.Join(t.TempDir(), "sandboxes", user.ID, project.ID)
	sb, err := st.CreateSandbox(ctx, user.ID, store.NewSandbox{
		ProjectID: project.ID, Provider: "runner", RootDir: rootDir,
	})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	session, err := st.CreateSession(ctx, user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindMain, Title: "remote run",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	running := contracts.StatusRunning
	if _, err := st.UpdateSession(ctx, user.ID, session.ID, store.SessionPatch{Status: &running}); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	run, err := st.CreateRun(ctx, user.ID, store.NewRun{SessionID: session.ID, Trigger: "user"})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	sandboxAPI := newFakeAIO(t)
	runner := newFakeRunner(t, sb.ID, rootDir, sandboxAPI.url)
	provider, err := sandbox.NewRunner(sandbox.RunnerConfig{
		BaseURL:  runner.url,
		Token:    "runner-token",
		DataRoot: filepath.Join(t.TempDir(), "sandboxes"),
		Image:    "piforge-sandbox:test",
	})
	if err != nil {
		t.Fatalf("sandbox.NewRunner: %v", err)
	}

	// A restarted control plane: the registry has nothing in it.
	rt := pi.NewRuntime(pi.RuntimeConfig{Config: pi.Config{BinaryPath: "pi"}})
	t.Cleanup(func() { _ = rt.Close() })

	logs := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	probe := sessionLivenessProbe(
		options{sandboxKind: "runner"},
		st, rt, provider,
		sandboxKeys{store: st, fixed: "fixed-sandbox-key"},
		logger,
	)
	return &livenessFixture{
		store: st, userID: user.ID, session: session, runID: run.ID,
		runner: provider, fakeRunner: runner, sandbox: sandboxAPI, logs: logs,
		rec: reconcile.New(reconcile.Config{
			Store: st, Probe: probe, Runtime: rt, Logger: logger, Now: time.Now,
		}),
		sandboxID: sb.ID,
	}
}

func (f *livenessFixture) sessionStatus(t *testing.T) contracts.SessionStatus {
	t.Helper()
	sess, err := f.store.Session(context.Background(), f.userID, f.session.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	return sess.Status
}

func (f *livenessFixture) runStatus(t *testing.T) string {
	t.Helper()
	runs, err := f.store.ListRuns(context.Background(), f.userID, f.session.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	return runs[0].Status
}

// TestRestartWithARunnerSandboxKeepsALiveSession is requirement (b) without a
// real runner: the runner provider's probe asks the sandbox, the sandbox reports
// a fresh heartbeat, and a restart does not write "interrupted" over the run.
func TestRestartWithARunnerSandboxKeepsALiveSession(t *testing.T) {
	f := newLivenessFixture(t)
	ctx := context.Background()

	report, err := f.rec.Run(ctx, reconcile.ReasonRestart, "startup")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Scanned != 1 || report.Live != 1 || report.Interrupted != 0 || report.Unknown != 0 {
		t.Fatalf("report = %+v, want the live remote session kept", report)
	}
	if got := f.sessionStatus(t); got != contracts.StatusRunning {
		t.Fatalf("session status = %q, want running", got)
	}
	if got := f.runStatus(t); got != "started" {
		t.Fatalf("run status = %q, want started", got)
	}
	if f.sandbox.calls.Load() == 0 {
		t.Fatal("the sandbox was never asked: the probe is not wired")
	}
	if f.fakeRunner.gets.Load() == 0 {
		t.Fatal("the runner was never asked for the endpoint")
	}
	events, err := f.store.ListEvents(ctx, f.userID, f.session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("a kept session got %d recovery events", len(events))
	}
	if !strings.Contains(f.logs.String(), "sandbox-marker(sandbox-api)") {
		t.Fatalf("the startup log does not name the probe it wired:\n%s", f.logs.String())
	}

	// The same wiring still repairs the row when the sandbox says the heartbeat
	// is gone: only a definitive dead interrupts.
	f.sandbox.mode.Store("nomark")
	report, err = f.rec.Run(ctx, reconcile.ReasonRestart, "startup")
	if err != nil {
		t.Fatalf("Run (no marker): %v", err)
	}
	if report.Interrupted != 1 || report.Runs != 1 {
		t.Fatalf("report = %+v, want one interrupted run", report)
	}
	if got := f.sessionStatus(t); got != contracts.StatusInterrupted {
		t.Fatalf("session status = %q, want interrupted", got)
	}
	if got := f.runStatus(t); got != "interrupted" {
		t.Fatalf("run status = %q, want interrupted", got)
	}
}

// TestRestartWithABrokenSandboxLeavesTheSessionRunning is the conservative half
// of (b): the sandbox cannot answer, so the run is not killed by bookkeeping.
func TestRestartWithABrokenSandboxLeavesTheSessionRunning(t *testing.T) {
	f := newLivenessFixture(t)
	f.sandbox.mode.Store("broken")

	report, err := f.rec.Run(context.Background(), reconcile.ReasonRestart, "startup")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Unknown != 1 || report.Interrupted != 0 {
		t.Fatalf("report = %+v, want one unknown and no interruption", report)
	}
	if got := f.sessionStatus(t); got != contracts.StatusRunning {
		t.Fatalf("session status = %q, want running", got)
	}
	if got := f.runStatus(t); got != "started" {
		t.Fatalf("run status = %q, want started", got)
	}
	if !strings.Contains(f.logs.String(), "session liveness is unknown") ||
		!strings.Contains(f.logs.String(), f.session.ID) {
		t.Fatalf("the WARN does not name the session:\n%s", f.logs.String())
	}
}

// TestLocalProviderUsesTheRegistryOnly keeps the local deployment's rule: no
// sandbox probe, no marker, and the in-process registry is what answers.
func TestLocalProviderUsesTheRegistryOnly(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	rt := pi.NewRuntime(pi.RuntimeConfig{Config: pi.Config{BinaryPath: "pi"}})
	defer func() { _ = rt.Close() }()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	probe := sessionLivenessProbe(options{sandboxKind: "local"}, st, rt, nil, sandboxKeys{}, logger)
	if got := liveness.Describe(probe); got != "process-registry" {
		t.Fatalf("the local provider wired %q, want the registry only", got)
	}
	if alive, err := probe.Alive(ctx, "ses_local"); err != nil || alive {
		t.Fatalf("Alive = (%v, %v), want a definitive dead from an empty registry", alive, err)
	}
}
