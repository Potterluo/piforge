package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// registry is the in-process authority the reconciler asks about. Tests flip it
// by hand to model a boot (nothing alive), a live turn, or a child that died
// while the control plane stayed up.
type registry struct {
	mu    sync.Mutex
	alive map[string]bool
}

func newRegistry() *registry { return &registry{alive: map[string]bool{}} }

func (r *registry) Alive(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.alive[sessionID]
}

func (r *registry) set(sessionID string, alive bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive[sessionID] = alive
}

type fixture struct {
	store    *store.Store
	userID   string
	project  *contracts.Project
	session  *contracts.Session
	runID    string
	logs     *bytes.Buffer
	logger   *slog.Logger
	registry *registry
	rec      *Reconciler
}

// newFixture creates the exact state a process killed mid-turn leaves: the
// session row says running and one run row says started, with nothing serving
// either.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	user, err := st.CreateUser(ctx, store.NewUser{Email: "reconcile@example.test", DisplayName: "R"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	project, err := st.CreateProject(ctx, user.ID, store.NewProject{Name: "p", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	session, err := st.CreateSession(ctx, user.ID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindMain, Title: "run that never came back",
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

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	reg := newRegistry()
	return &fixture{
		store: st, userID: user.ID, project: project, session: session, runID: run.ID,
		logs: logs, logger: logger, registry: reg,
		rec: New(Config{Store: st, Runtime: reg, Logger: logger}),
	}
}

func (f *fixture) sessionStatus(t *testing.T) contracts.SessionStatus {
	t.Helper()
	sess, err := f.store.Session(context.Background(), f.userID, f.session.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	return sess.Status
}

func (f *fixture) runs(t *testing.T) []*contracts.Run {
	t.Helper()
	runs, err := f.store.ListRuns(context.Background(), f.userID, f.session.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	return runs
}

func (f *fixture) events(t *testing.T) []*contracts.Event {
	t.Helper()
	events, err := f.store.ListEvents(context.Background(), f.userID, f.session.ID, 0, 1000)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	return events
}

// TestBootReconcileInterruptsAnOrphanedRun is the restart case: the registry is
// empty, the row says running, and one pass turns it into an interrupted run
// with a transcript that says why. The second pass must write nothing.
func TestBootReconcileInterruptsAnOrphanedRun(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	report, err := f.rec.Run(ctx, ReasonRestart, "startup")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Scanned != 1 || report.Interrupted != 1 || report.Runs != 1 {
		t.Fatalf("report = %+v, want scanned=1 interrupted=1 runs=1", report)
	}
	if got := f.sessionStatus(t); got != contracts.StatusInterrupted {
		t.Fatalf("session status = %q, want %q", got, contracts.StatusInterrupted)
	}
	runs := f.runs(t)
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	if runs[0].Status != "interrupted" || runs[0].InterruptedBy != "reconcile" ||
		runs[0].CompletionNote != ReasonRestart || runs[0].SettledAt == nil {
		t.Fatalf("run after boot = %+v", runs[0])
	}

	events := f.events(t)
	var settled, platformErr *contracts.Event
	for _, ev := range events {
		switch ev.Type {
		case contracts.EvRunSettled:
			settled = ev
		case contracts.EvPlatformErr:
			platformErr = ev
		}
	}
	if settled == nil {
		t.Fatal("no run.settled event was appended")
	}
	var settledPayload struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(settled.Payload, &settledPayload); err != nil {
		t.Fatalf("run.settled payload: %v", err)
	}
	if settled.RunID != f.runID || settledPayload.Status != "interrupted" || settledPayload.Reason != ReasonRestart {
		t.Fatalf("run.settled = %+v payload=%+v", settled, settledPayload)
	}
	if platformErr == nil {
		t.Fatal("no platform.error event was appended")
	}
	var errPayload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(platformErr.Payload, &errPayload); err != nil {
		t.Fatalf("platform.error payload: %v", err)
	}
	if errPayload.Code != "run_interrupted" || errPayload.Message != ReasonRestart {
		t.Fatalf("platform.error = %+v", errPayload)
	}

	// The summary count an operator reads after a restart.
	if !strings.Contains(f.logs.String(), "session reconciliation") ||
		!strings.Contains(f.logs.String(), "interrupted=1") ||
		!strings.Contains(f.logs.String(), ReasonRestart) {
		t.Fatalf("the pass did not log its summary count:\n%s", f.logs.String())
	}

	// Idempotent: the row no longer claims a run, so a second boot pass is a
	// no-op and appends no second transcript entry.
	before := len(events)
	report, err = f.rec.Run(ctx, ReasonRestart, "startup")
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if report.Scanned != 0 || report.Interrupted != 0 || report.Runs != 0 {
		t.Fatalf("second pass = %+v, want a no-op", report)
	}
	if got := len(f.events(t)); got != before {
		t.Fatalf("second pass appended events: %d -> %d", before, got)
	}
}

// TestReconcileLeavesALiveRunAlone is the drill's invariant: reconciliation
// must never disturb a run an agent is actually serving. It runs twice so a
// watchdog tick beside an active turn is covered, not just boot.
func TestReconcileLeavesALiveRunAlone(t *testing.T) {
	f := newFixture(t)
	f.registry.set(f.session.ID, true)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		report, err := f.rec.Run(ctx, ReasonProcessGone, "watchdog")
		if err != nil {
			t.Fatalf("Run #%d: %v", i+1, err)
		}
		if report.Scanned != 1 || report.Live != 1 || report.Interrupted != 0 {
			t.Fatalf("pass #%d = %+v, want the live session kept", i+1, report)
		}
	}
	if got := f.sessionStatus(t); got != contracts.StatusRunning {
		t.Fatalf("a live session was rewritten to %q", got)
	}
	if runs := f.runs(t); len(runs) != 1 || runs[0].Status != "started" {
		t.Fatalf("a live run was settled: %+v", runs)
	}
	if got := len(f.events(t)); got != 0 {
		t.Fatalf("a live run got %d recovery events", got)
	}
}

// TestWatchdogInterruptsARunWhoseProcessDied is the no-restart crash: the same
// control plane stays up, the child disappears, and the periodic pass repairs
// the row. It also proves the pass only fires once the registry says the
// process is gone.
func TestWatchdogInterruptsARunWhoseProcessDied(t *testing.T) {
	f := newFixture(t)
	f.registry.set(f.session.ID, true)
	ctx := context.Background()

	report, err := f.rec.Run(ctx, ReasonProcessGone, "watchdog")
	if err != nil {
		t.Fatalf("Run (alive): %v", err)
	}
	if report.Interrupted != 0 {
		t.Fatalf("interrupted a live run: %+v", report)
	}

	// The agent child crashed; nothing restarted it.
	f.registry.set(f.session.ID, false)
	report, err = f.rec.Run(ctx, ReasonProcessGone, "watchdog")
	if err != nil {
		t.Fatalf("Run (dead): %v", err)
	}
	if report.Interrupted != 1 || report.Runs != 1 {
		t.Fatalf("watchdog pass = %+v, want one interrupted run", report)
	}
	if got := f.sessionStatus(t); got != contracts.StatusInterrupted {
		t.Fatalf("session status = %q, want interrupted", got)
	}
	if runs := f.runs(t); runs[0].Status != "interrupted" || runs[0].CompletionNote != ReasonProcessGone {
		t.Fatalf("run after watchdog = %+v", runs[0])
	}
}

// TestReconcileWithoutARegistryInterruptsNothing is the conservative rule: if
// the process cannot prove a run is dead, it leaves it running and says why.
func TestReconcileWithoutARegistryInterruptsNothing(t *testing.T) {
	f := newFixture(t)
	rec := New(Config{Store: f.store, Logger: f.logger})

	report, err := rec.Run(context.Background(), ReasonRestart, "startup")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Scanned != 1 || report.Interrupted != 0 || report.Live != 1 {
		t.Fatalf("report = %+v, want nothing interrupted without a registry", report)
	}
	if got := f.sessionStatus(t); got != contracts.StatusRunning {
		t.Fatalf("session status = %q, want running", got)
	}
	if !strings.Contains(f.logs.String(), "registry is unavailable") {
		t.Fatalf("the pass did not explain why it left the row alone:\n%s", f.logs.String())
	}
}

// TestProjectReconcileTouchesOnlyItsOwnProject keeps the delete guard from
// repairing somebody else's (or another project's) rows while it looks at one.
func TestProjectReconcileTouchesOnlyItsOwnProject(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	otherProject, err := f.store.CreateProject(ctx, f.userID, store.NewProject{Name: "other", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	otherSession, err := f.store.CreateSession(ctx, f.userID, store.NewSession{
		ProjectID: otherProject.ID, Kind: contracts.KindMain, Title: "other run",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	running := contracts.StatusRunning
	if _, err := f.store.UpdateSession(ctx, f.userID, otherSession.ID, store.SessionPatch{Status: &running}); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	report, err := f.rec.Project(ctx, f.userID, f.project.ID, ReasonProcessGone, "delete")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if report.Scanned != 1 || report.Interrupted != 1 {
		t.Fatalf("report = %+v, want only the target project", report)
	}
	other, err := f.store.Session(ctx, f.userID, otherSession.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if other.Status != contracts.StatusRunning {
		t.Fatalf("another project's session was rewritten to %q", other.Status)
	}
}
