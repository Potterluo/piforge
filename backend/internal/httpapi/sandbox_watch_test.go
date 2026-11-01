package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// wedgedSandbox is a provider whose container answers the probe only while the
// test says so: `Stop` (Pause) models the fork storm's processes dying with the
// container, which is exactly why stop+start is the repair.
type wedgedSandbox struct {
	contracts.SandboxProvider
	wedged  atomic.Bool
	paused  atomic.Int32
	resumed atomic.Int32
	probes  atomic.Int32
	// flaky makes the next n probes fail and then succeed: a loaded host that
	// misses one probe and answers the confirmation.
	flaky atomic.Int32
}

func (w *wedgedSandbox) Probe(context.Context, string) error { return nil }

func (w *wedgedSandbox) Pause(_ context.Context, sandboxID string) error {
	w.paused.Add(1)
	return nil
}

func (w *wedgedSandbox) Resume(_ context.Context, sandboxID string) (*contracts.Sandbox, error) {
	w.resumed.Add(1)
	// The storm's processes were in the container; a stop removes them all, so
	// the restarted container answers again (measured: 0.9 s to `docker exec`
	// success on this host, ~10-20 s for the image's own API).
	w.wedged.Store(false)
	return &contracts.Sandbox{ID: sandboxID, State: contracts.SandboxReady}, nil
}

// probeFunc is the liveness hook the watchdog calls.
func (w *wedgedSandbox) probeHook() func(context.Context, string) error {
	return func(context.Context, string) error {
		w.probes.Add(1)
		if n := w.flaky.Load(); n > 0 {
			w.flaky.Add(-1)
			return errors.New("probe timed out")
		}
		if w.wedged.Load() {
			return errors.New("unable to spawn stage-1: Resource temporarily unavailable")
		}
		return nil
	}
}

// newWatchEnv builds a server whose watchdog can be driven by hand: the pass is a
// method, so the test controls the clock instead of waiting for a ticker.
func newWatchEnv(t *testing.T) (*testEnv, *Server, *wedgedSandbox, string, string, string) {
	t.Helper()
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	project, session := env.mkSessionFor(user.ID, "watchable")
	containerID := attachPauseSandbox(t, env, user.ID, project, session)

	provider := &wedgedSandbox{}
	env.deps.Sandbox = provider
	env.deps.ProbeSandbox = provider.probeHook()
	env.deps.SandboxProbeTimeout = 2 * time.Second
	// The confirmation is a real pause in production; the test only needs it not
	// to be a second and a half.
	env.deps.SandboxConfirmDelay = 5 * time.Millisecond
	srv := NewServer(env.deps)
	// The handlers the assertions use must be the same server the watchdog
	// belongs to, or the status endpoint reads a different probe cache.
	env.handler = srv.Handler()
	return env, srv, provider, user.ID, session.ID, containerID
}

// TestSandboxWatchRecoversAWedgedSandbox is the operational defect from
// adversarial acceptance #2: a fork storm left the sandbox unable to fork, the
// agent could not start, and the only recovery was a manual pause/resume. The
// watchdog must notice and do that pause/resume itself, and the session must say
// so before and after.
//
// The rule since acceptance #3 is "one missed probe, confirmed 1.5 s later": the
// first half of this test is the loaded host (the confirmation answers, nothing is
// restarted) and the second half is the wedge (both probes fail, the repair runs
// inside the same pass instead of two 10 s intervals later).
func TestSandboxWatchRecoversAWedgedSandbox(t *testing.T) {
	env, srv, provider, userID, sessionID, containerID := newWatchEnv(t)

	// A single missed probe is a suspicion: the confirmation answers, so the
	// container is left alone.
	provider.flaky.Store(1)
	srv.s.sandboxWatchPass(context.Background())
	if got := provider.paused.Load(); got != 0 {
		t.Fatalf("one missed probe restarted the sandbox (%d pauses)", got)
	}
	if got := provider.probes.Load(); got < 2 {
		t.Fatalf("the missed probe was not confirmed (probes = %d)", got)
	}

	// A failure the confirmation repeats is a wedge, in this pass.
	provider.wedged.Store(true)
	srv.s.sandboxWatchPass(context.Background())

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if provider.paused.Load() == 1 && provider.resumed.Load() == 1 && !srv.s.isRecovering(containerID) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if provider.paused.Load() != 1 || provider.resumed.Load() != 1 {
		t.Fatalf("recovery = %d pauses / %d resumes, want exactly one stop+start",
			provider.paused.Load(), provider.resumed.Load())
	}

	// The row says it is back, and the verdict is cached for the panel.
	sb, err := env.store.Sandbox(context.Background(), userID, containerID)
	if err != nil {
		t.Fatalf("read the sandbox row: %v", err)
	}
	if sb.State != contracts.SandboxReady || !strings.Contains(sb.Message, "recovered") {
		t.Fatalf("sandbox row after recovery = state %q message %q", sb.State, sb.Message)
	}
	verdict, ok := srv.s.probeSnapshot(containerID)
	if !ok || !verdict.OK || verdict.Restarts != 1 || verdict.RecoveredAt.IsZero() {
		t.Fatalf("probe verdict = %+v, want ok with one restart", verdict)
	}

	// The transcript carries both halves: the warning the user sees while it is
	// happening, and the confirmation that it is over.
	events, err := env.store.ListMergedEvents(context.Background(), userID, sessionID, 0, 100)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var codes []string
	for _, event := range events {
		if event.Type != contracts.EvPlatformErr {
			continue
		}
		var payload struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil {
			continue
		}
		codes = append(codes, payload.Code)
		if payload.Code == contracts.ErrSandboxUnresponsive && !strings.Contains(payload.Message, "沙箱无响应，正在恢复") {
			t.Fatalf("the warning does not say what the requirement names: %q", payload.Message)
		}
		if payload.Code == contracts.ErrSandboxRecovered && !strings.Contains(payload.Message, "沙箱已恢复") {
			t.Fatalf("the confirmation is unclear: %q", payload.Message)
		}
	}
	if len(codes) != 2 || codes[0] != contracts.ErrSandboxUnresponsive || codes[1] != contracts.ErrSandboxRecovered {
		t.Fatalf("platform errors = %v, want [%s %s]", codes,
			contracts.ErrSandboxUnresponsive, contracts.ErrSandboxRecovered)
	}
}

// A sandbox that keeps answering is never touched: restarting it would throw
// away its /tmp and whatever the agent is doing.
func TestSandboxWatchLeavesAHealthySandboxAlone(t *testing.T) {
	env, srv, provider, _, _, containerID := newWatchEnv(t)
	_ = env
	for i := 0; i < 5; i++ {
		srv.s.sandboxWatchPass(context.Background())
	}
	time.Sleep(100 * time.Millisecond)
	if provider.paused.Load() != 0 || provider.resumed.Load() != 0 {
		t.Fatalf("a healthy sandbox was restarted: %d/%d", provider.paused.Load(), provider.resumed.Load())
	}
	verdict, ok := srv.s.probeSnapshot(containerID)
	if !ok || !verdict.OK || verdict.Error != "" {
		t.Fatalf("verdict = %+v, want healthy", verdict)
	}
}

// The one-click 恢复 button: it probes first, so pressing it on a healthy sandbox
// is a no-op that says so, and on a wedged one it performs the repair and
// answers with the timing and the sentence the user needs.
func TestSandboxRecoverEndpoint(t *testing.T) {
	env, _, provider, _, sessionID, _ := newWatchEnv(t)
	_ = env
	client := env.login("dev@piforge.local", "pw")

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sessionID+"/sandbox/recover", nil, client)
	if rec.Code != http.StatusOK {
		t.Fatalf("recover (healthy) = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		State     string `json:"state"`
		Recovered bool   `json:"recovered"`
		Restarted bool   `json:"restarted"`
		Message   string `json:"message"`
	}
	env.mustJSON(rec, &body)
	if !body.Recovered || body.Restarted {
		t.Fatalf("healthy sandbox: recovered=%v restarted=%v, want a no-op", body.Recovered, body.Restarted)
	}
	if !strings.Contains(body.Message, "无需恢复") {
		t.Fatalf("a no-op must say why: %q", body.Message)
	}

	provider.wedged.Store(true)
	rec = env.doAuthed(http.MethodPost, "/v1/sessions/"+sessionID+"/sandbox/recover", nil, client)
	if rec.Code != http.StatusOK {
		t.Fatalf("recover (wedged) = %d: %s", rec.Code, rec.Body.String())
	}
	body = struct {
		State     string `json:"state"`
		Recovered bool   `json:"recovered"`
		Restarted bool   `json:"restarted"`
		Message   string `json:"message"`
	}{}
	env.mustJSON(rec, &body)
	if !body.Recovered || !body.Restarted || body.State != string(contracts.SandboxReady) {
		t.Fatalf("wedged sandbox: %+v, want a restart that recovered", body)
	}
	if !strings.Contains(body.Message, "沙箱已恢复") {
		t.Fatalf("message = %q", body.Message)
	}
	if provider.paused.Load() != 1 || provider.resumed.Load() != 1 {
		t.Fatalf("recovery calls = %d/%d, want 1/1", provider.paused.Load(), provider.resumed.Load())
	}

	// And the panel's own endpoint reports the cached verdict, so the banner can
	// be shown without the page probing anything itself.
	rec = env.doAuthed(http.MethodGet, "/v1/sessions/"+sessionID+"/sandbox", nil, client)
	if rec.Code != http.StatusOK {
		t.Fatalf("sandbox state = %d: %s", rec.Code, rec.Body.String())
	}
	var state struct {
		State      string `json:"state"`
		Responsive *bool  `json:"responsive"`
		CanRecover bool   `json:"can_recover"`
	}
	env.mustJSON(rec, &state)
	if state.Responsive == nil || !*state.Responsive {
		t.Fatalf("responsive = %v, want true", state.Responsive)
	}
	if !state.CanRecover {
		t.Fatal("can_recover = false on a deployment that probes")
	}
}

// A deployment that cannot probe says so instead of pretending: no watchdog, and
// the recover endpoint refuses with a typed error rather than doing nothing.
func TestSandboxWatchIsOffWhenTheDeploymentCannotProbe(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	project, session := env.mkSessionFor(user.ID, "no-probe")
	attachPauseSandbox(t, env, user.ID, project, session)
	env.deps.Sandbox = &recordPauseResume{}
	env.rebuild()
	client := env.login("dev@piforge.local", "pw")

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+session.ID+"/sandbox/recover", nil, client)
	env.mustErr(rec, http.StatusUnprocessableEntity, contracts.ErrSandboxUnavail)

	// The state endpoint still answers, and says the repair is not available
	// here rather than leaving the panel to guess.
	rec = env.doAuthed(http.MethodGet, "/v1/sessions/"+session.ID+"/sandbox", nil, client)
	if rec.Code != http.StatusOK {
		t.Fatalf("sandbox state = %d: %s", rec.Code, rec.Body.String())
	}
	var state struct {
		Responsive *bool `json:"responsive"`
		CanRecover bool  `json:"can_recover"`
	}
	env.mustJSON(rec, &state)
	if state.Responsive != nil {
		t.Fatalf("responsive = %v on a deployment that cannot probe, want null", *state.Responsive)
	}
	if state.CanRecover {
		t.Fatal("can_recover = true without a probe")
	}
}
