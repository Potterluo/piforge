package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/liveness"
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
	var settled, platformErr, interrupted *contracts.Event
	for _, ev := range events {
		switch ev.Type {
		case contracts.EvRunSettled:
			settled = ev
		case contracts.EvPlatformErr:
			platformErr = ev
		case contracts.EvRunInterrupted:
			interrupted = ev
		}
	}
	if settled == nil {
		t.Fatal("no run.settled event was appended")
	}
	var settledPayload struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
		Cause  string `json:"cause"`
	}
	if err := json.Unmarshal(settled.Payload, &settledPayload); err != nil {
		t.Fatalf("run.settled payload: %v", err)
	}
	if settled.RunID != f.runID || settledPayload.Status != "interrupted" || settledPayload.Reason != ReasonRestart {
		t.Fatalf("run.settled = %+v payload=%+v", settled, settledPayload)
	}
	if settledPayload.Cause != "restart" {
		t.Fatalf("run.settled cause = %q, want restart", settledPayload.Cause)
	}
	if platformErr == nil {
		t.Fatal("no platform.error event was appended")
	}
	var errPayload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Cause   string `json:"cause"`
	}
	if err := json.Unmarshal(platformErr.Payload, &errPayload); err != nil {
		t.Fatalf("platform.error payload: %v", err)
	}
	if errPayload.Code != "run_interrupted" || errPayload.Message != ReasonRestart {
		t.Fatalf("platform.error = %+v", errPayload)
	}
	if errPayload.Cause != "restart" {
		t.Fatalf("platform.error cause = %q, want restart", errPayload.Cause)
	}
	// The recovery marker the transcript renders in place. Without it a restart
	// reads as "something was interrupted"; with it the marker can say 重启 and
	// offer 继续 without parsing the error sentence.
	if interrupted == nil {
		t.Fatal("no run.interrupted event was appended")
	}
	var interruptedPayload struct {
		Cause  string `json:"cause"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(interrupted.Payload, &interruptedPayload); err != nil {
		t.Fatalf("run.interrupted payload: %v", err)
	}
	if interruptedPayload.Cause != "restart" || interruptedPayload.Reason != ReasonRestart {
		t.Fatalf("run.interrupted = %+v", interruptedPayload)
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

// TestReconcileInterruptsARunningSessionWithNoRunRow covers the older shape a
// crash can leave: the session says running but no run row survived. It must
// still be repaired, and the person opening it must still see why — there is no
// run to attach a run.settled event to.
func TestReconcileInterruptsARunningSessionWithNoRunRow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	project, err := f.store.CreateProject(ctx, f.userID, store.NewProject{Name: "no-run", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	session, err := f.store.CreateSession(ctx, f.userID, store.NewSession{
		ProjectID: project.ID, Kind: contracts.KindMain, Title: "no run row",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	running := contracts.StatusRunning
	if _, err := f.store.UpdateSession(ctx, f.userID, session.ID, store.SessionPatch{Status: &running}); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	report, err := f.rec.Project(ctx, f.userID, project.ID, ReasonRestart, "startup")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if report.Scanned != 1 || report.Interrupted != 1 || report.Runs != 0 {
		t.Fatalf("report = %+v, want one session and no run rows", report)
	}
	sess, err := f.store.Session(ctx, f.userID, session.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if sess.Status != contracts.StatusInterrupted {
		t.Fatalf("session status = %q, want interrupted", sess.Status)
	}
	events, err := f.store.ListEvents(ctx, f.userID, session.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want run.interrupted and platform.error", events)
	}
	byType := map[string]*contracts.Event{}
	for _, ev := range events {
		byType[ev.Type] = ev
	}
	if byType[contracts.EvRunInterrupted] == nil || byType[contracts.EvPlatformErr] == nil {
		t.Fatalf("events = %+v, want run.interrupted and platform.error", events)
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

// ---------------------------------------------------------------------------
// The remote-placement gap: reconcile must ask a probe, not only the registry
// ---------------------------------------------------------------------------

// probeRecorder is the fake "remote" liveness a container deployment configures:
// it answers for sessions whose agent lives outside this process.
type probeRecorder struct {
	mu     sync.Mutex
	answer map[string]bool
	err    error
	calls  int
}

func newProbeRecorder() *probeRecorder { return &probeRecorder{answer: map[string]bool{}} }

func (p *probeRecorder) Alive(ctx context.Context, sessionID string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return false, p.err
	}
	return p.answer[sessionID], nil
}

func (p *probeRecorder) set(sessionID string, alive bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.answer[sessionID] = alive
}

// withProbe rebuilds the fixture's reconciler around a probe.
func (f *fixture) withProbe(t *testing.T, probe contracts.LivenessProbe) *Reconciler {
	t.Helper()
	return New(Config{Store: f.store, Probe: probe, Runtime: f.registry, Logger: f.logger})
}

// TestReconcileLeavesALiveRemoteSessionAlone is the defect this batch fixes,
// stated as the invariant the drill needs: after a control-plane restart the
// in-process registry is empty, the agent inside the sandbox is still running,
// and two watchdog passes must not touch the row.
func TestReconcileLeavesALiveRemoteSessionAlone(t *testing.T) {
	f := newFixture(t)
	probe := newProbeRecorder()
	probe.set(f.session.ID, true)
	rec := f.withProbe(t, probe)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		report, err := rec.Run(ctx, ReasonProcessGone, "watchdog")
		if err != nil {
			t.Fatalf("Run #%d: %v", i+1, err)
		}
		if report.Scanned != 1 || report.Live != 1 || report.Interrupted != 0 || report.Unknown != 0 {
			t.Fatalf("pass #%d = %+v, want the live remote session kept", i+1, report)
		}
	}
	if got := f.sessionStatus(t); got != contracts.StatusRunning {
		t.Fatalf("a live remote session was rewritten to %q", got)
	}
	if runs := f.runs(t); len(runs) != 1 || runs[0].Status != "started" {
		t.Fatalf("a live remote run was settled: %+v", runs)
	}
	if got := len(f.events(t)); got != 0 {
		t.Fatalf("a live remote session got %d recovery events", got)
	}
	if probe.calls != 2 {
		t.Fatalf("the probe was asked %d times, want 2", probe.calls)
	}
}

// TestRegistryOnlyWiringInterruptsALiveRemoteSession is the bug itself, frozen
// as a test: a reconciler wired the way cmd/piforged wired it before this batch
// (registry only, no probe) cannot see a remote agent, so it writes
// "interrupted" over a live run. The next test proves the fixed wiring does not.
func TestRegistryOnlyWiringInterruptsALiveRemoteSession(t *testing.T) {
	f := newFixture(t)
	remote := newProbeRecorder()
	remote.set(f.session.ID, true) // the sandbox says the agent is alive
	registryOnly := New(Config{Store: f.store, Runtime: f.registry, Logger: f.logger})

	report, err := registryOnly.Run(context.Background(), ReasonRestart, "startup")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Interrupted != 1 {
		t.Fatalf("report = %+v, want the registry-only wiring to interrupt the live remote run (that is the bug)", report)
	}
	if got := f.sessionStatus(t); got != contracts.StatusInterrupted {
		t.Fatalf("session status = %q, want interrupted (the bug)", got)
	}
	if runs := f.runs(t); runs[0].Status != "interrupted" {
		t.Fatalf("run = %+v, want interrupted (the bug)", runs[0])
	}
}

// TestReconcileInterruptsOnlyOnADefinitiveDeadAnswer: with the same live remote
// agent, the probe-aware wiring leaves it alone — and once the authority says
// dead, the very same reconciler repairs the row.
func TestReconcileInterruptsOnlyOnADefinitiveDeadAnswer(t *testing.T) {
	f := newFixture(t)
	probe := newProbeRecorder()
	probe.set(f.session.ID, true)
	rec := f.withProbe(t, probe)
	ctx := context.Background()

	report, err := rec.Run(ctx, ReasonRestart, "startup")
	if err != nil {
		t.Fatalf("Run (alive): %v", err)
	}
	if report.Interrupted != 0 || report.Live != 1 {
		t.Fatalf("report = %+v, want the live session kept", report)
	}

	probe.set(f.session.ID, false)
	report, err = rec.Run(ctx, ReasonRestart, "startup")
	if err != nil {
		t.Fatalf("Run (dead): %v", err)
	}
	if report.Interrupted != 1 || report.Runs != 1 || report.Live != 0 {
		t.Fatalf("report = %+v, want one interrupted run after a definitive dead", report)
	}
	if got := f.sessionStatus(t); got != contracts.StatusInterrupted {
		t.Fatalf("session status = %q, want interrupted", got)
	}
}

// TestReconcileTreatsAProbeErrorAsUnknown is the conservative rule with teeth:
// an unreachable sandbox leaves the session running, counts it as unknown, names
// it in a WARN, and appends nothing to the transcript.
func TestReconcileTreatsAProbeErrorAsUnknown(t *testing.T) {
	f := newFixture(t)
	probe := newProbeRecorder()
	probe.err = errors.New("sandbox sbx_1: unreachable")
	rec := f.withProbe(t, probe)

	report, err := rec.Run(context.Background(), ReasonRestart, "startup")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Scanned != 1 || report.Unknown != 1 || report.Interrupted != 0 || report.Runs != 0 {
		t.Fatalf("report = %+v, want one unknown and no interruption", report)
	}
	if got := f.sessionStatus(t); got != contracts.StatusRunning {
		t.Fatalf("an unknown liveness rewrote the session to %q", got)
	}
	if runs := f.runs(t); runs[0].Status != "started" {
		t.Fatalf("an unknown liveness settled the run: %+v", runs[0])
	}
	if got := len(f.events(t)); got != 0 {
		t.Fatalf("an unknown liveness appended %d events", got)
	}
	logs := f.logs.String()
	if !strings.Contains(logs, "session liveness is unknown") ||
		!strings.Contains(logs, f.session.ID) ||
		!strings.Contains(logs, "unreachable") {
		t.Fatalf("the WARN does not name the session and the reason:\n%s", logs)
	}
	if !strings.Contains(logs, "unknown=1") {
		t.Fatalf("the summary does not count the unknown session:\n%s", logs)
	}
}

// TestReconcileWithACompositeProbeIsUnknownWhenTheSandboxCannotAnswer is the
// composite fallback: the in-process registry says dead (a fresh process) and
// the sandbox source errors, so the composite errors and reconciliation leaves
// the row alone. This is the "restart plus a broken sandbox" case.
func TestReconcileWithACompositeProbeIsUnknownWhenTheSandboxCannotAnswer(t *testing.T) {
	f := newFixture(t)
	brokenSandbox := liveness.ProbeFunc(func(context.Context, string) (bool, error) {
		return false, errors.New("sandbox API returned HTTP 502")
	})
	probe := liveness.NewComposite(liveness.NewProcess(f.registry), brokenSandbox)
	rec := f.withProbe(t, probe)

	report, err := rec.Run(context.Background(), ReasonRestart, "startup")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Unknown != 1 || report.Interrupted != 0 {
		t.Fatalf("report = %+v, want the unknown session kept", report)
	}
	if got := f.sessionStatus(t); got != contracts.StatusRunning {
		t.Fatalf("session status = %q, want running", got)
	}
}

// TestReconcileProbeTimeoutIsUnknown proves a hung sandbox cannot stall the boot
// pass into writing: the per-candidate deadline turns it into unknown, and the
// row is kept.
func TestReconcileProbeTimeoutIsUnknown(t *testing.T) {
	f := newFixture(t)
	blocked := liveness.ProbeFunc(func(ctx context.Context, _ string) (bool, error) {
		<-ctx.Done()
		return false, ctx.Err()
	})
	rec := New(Config{
		Store:        f.store,
		Probe:        blocked,
		Logger:       f.logger,
		ProbeTimeout: 20 * time.Millisecond,
	})

	report, err := rec.Run(context.Background(), ReasonRestart, "startup")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Unknown != 1 || report.Interrupted != 0 {
		t.Fatalf("report = %+v, want one unknown", report)
	}
	if got := f.sessionStatus(t); got != contracts.StatusRunning {
		t.Fatalf("session status = %q, want running", got)
	}
}
