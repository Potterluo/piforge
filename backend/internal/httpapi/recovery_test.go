package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/recovery"
	"github.com/piforge/piforge/backend/internal/store"
)

// interruptedSession builds the shape a real interruption leaves behind: a
// worktree attached to a sandbox, a user request in the event log, and an
// interruption record naming the cause. It returns the session id and the
// sandbox, so a test can make the sandbox disappear afterwards.
func interruptedSession(t *testing.T, env *testEnv, userID, projectID, sessionID, cause, reason, request string) *contracts.Sandbox {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	sb, err := env.store.CreateSandbox(ctx, userID, store.NewSandbox{
		ProjectID: projectID, State: contracts.SandboxReady, Provider: "local", RootDir: dir,
	})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	wt, err := env.store.CreateWorktree(ctx, userID, store.NewWorktree{
		SandboxID: sb.ID, Path: dir, Branch: "main",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := env.store.UpdateSession(ctx, userID, sessionID, store.SessionPatch{WorktreeID: &wt.ID}); err != nil {
		t.Fatalf("attach worktree: %v", err)
	}

	message, _ := json.Marshal(map[string]any{"role": "user", "text": request, "blocks": []any{}})
	if _, err := env.store.AppendEvent(ctx, userID, &contracts.Event{
		SessionID: sessionID, RunID: "run_interrupted_1", Type: contracts.EvMsgFinal,
		Payload: message, TS: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	marker, _ := json.Marshal(map[string]any{"cause": cause, "reason": reason})
	if _, err := env.store.AppendEvent(ctx, userID, &contracts.Event{
		SessionID: sessionID, RunID: "run_interrupted_1", Type: contracts.EvRunInterrupted,
		Payload: marker, TS: time.Now(),
	}); err != nil {
		t.Fatalf("append interruption: %v", err)
	}
	return sb
}

// TestRecoveryEndpointAndContinuePrompt drives the two halves of the recovery
// path through the real router: the marker's facts (cause, dirty count, last
// commit) and the message the agent receives when 继续 is clicked. The message
// is composed server-side, so this asserts the exact text the runtime got.
func TestRecoveryEndpointAndContinuePrompt(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("recovery@piforge.local", "recovery-pass-123", "user")
	project, sess := env.mkSessionFor(user.ID, "recovery")
	const request = "修一下缓存淘汰的竞态，并补一个测试"
	interruptedSession(t, env, user.ID, project.ID, sess.ID,
		recovery.CauseRestart, recovery.ReasonRestart, request)

	env.workspace.mu.Lock()
	env.workspace.status = &contracts.GitStatus{
		Branch: "main", Clean: false,
		Changes: []contracts.GitChange{{Path: "a.go", Worktree: "M"}, {Path: "b.go", Index: "A"}},
	}
	env.workspace.graph = &contracts.GitGraph{Head: "e3f1a2b9999", Commits: []contracts.GitCommit{{Hash: "e3f1a2b9999", Short: "e3f1a2b"}}}
	env.workspace.mu.Unlock()

	c := env.login("recovery@piforge.local", "recovery-pass-123")
	rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/recovery", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET recovery: %d %s", rec.Code, rec.Body.String())
	}
	var view recoveryView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode recovery: %v", err)
	}
	if !view.Interrupted || view.Cause != recovery.CauseRestart || view.CauseLabel != "服务重启" {
		t.Fatalf("recovery view = %+v", view)
	}
	if view.LastRequest != request {
		t.Fatalf("last_request = %q, want %q", view.LastRequest, request)
	}
	if !view.CanContinue || view.BlockedReason != "" {
		t.Fatalf("can_continue = %v blocked=%q", view.CanContinue, view.BlockedReason)
	}
	if view.Survival.DirtyCount != 2 || view.Survival.LastCommit != "e3f1a2b" {
		t.Fatalf("survival = %+v", view.Survival)
	}
	for _, want := range []string{"工作区有 2 个未提交改动", "最后一次提交 e3f1a2b"} {
		if !strings.Contains(view.Survival.Summary, want) {
			t.Fatalf("survival summary %q is missing %q", view.Survival.Summary, want)
		}
	}

	rec = env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id":        "continue-1",
		"continue_interrupted": true,
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST continue: %d %s", rec.Code, rec.Body.String())
	}
	env.runtime.mu.Lock()
	prompts := append([]contracts.PromptRequest(nil), env.runtime.prompts...)
	env.runtime.mu.Unlock()
	if len(prompts) != 1 {
		t.Fatalf("runtime received %d prompts, want 1", len(prompts))
	}
	prompt := prompts[0].Text
	for _, want := range []string{
		request,
		recovery.ReasonRestart,
		"来源：服务重启",
		recovery.ContinueInstruction,
		"当前工作区：工作区有 2 个未提交改动 · 最后一次提交 e3f1a2b",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("continue prompt is missing %q:\n%s", want, prompt)
		}
	}
}

// TestRecoveryOfAGoneSandboxOffersNothing is the "don't lie" rule: when the
// sandbox was reclaimed, the view says so, the action is not offered, and the
// server refuses the same request if a stale client still sends it.
func TestRecoveryOfAGoneSandboxOffersNothing(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("gone@piforge.local", "gone-pass-123", "user")
	project, sess := env.mkSessionFor(user.ID, "gone")
	sb := interruptedSession(t, env, user.ID, project.ID, sess.ID,
		recovery.CauseProcessExit, recovery.ReasonProcessGone, "run the tests")
	if err := env.store.UpdateSandboxState(context.Background(), user.ID, sb.ID, contracts.SandboxMissing, "reclaimed"); err != nil {
		t.Fatalf("mark sandbox missing: %v", err)
	}

	c := env.login("gone@piforge.local", "gone-pass-123")
	rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/recovery", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET recovery: %d %s", rec.Code, rec.Body.String())
	}
	var view recoveryView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode recovery: %v", err)
	}
	if !view.Interrupted || view.Cause != recovery.CauseProcessExit {
		t.Fatalf("recovery view = %+v", view)
	}
	if view.CanContinue {
		t.Fatalf("a gone sandbox still offered 继续: %+v", view)
	}
	if !strings.Contains(view.BlockedReason, "沙箱已不存在") {
		t.Fatalf("blocked reason = %q", view.BlockedReason)
	}
	if !strings.Contains(view.Survival.Summary, "无法确认") ||
		strings.Contains(view.Survival.Summary, "工作区干净") {
		t.Fatalf("survival summary lies about a gone sandbox: %q", view.Survival.Summary)
	}

	rec = env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id":        "continue-gone",
		"continue_interrupted": true,
	}, c)
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST continue on a gone sandbox = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	env.runtime.mu.Lock()
	defer env.runtime.mu.Unlock()
	if len(env.runtime.prompts) != 0 {
		t.Fatalf("a refused continue still reached the agent: %+v", env.runtime.prompts)
	}
}

// TestInterruptRecordsTheStopCause: a user stop is not an error, so only this
// marker says the run ended because someone pressed 停止. Without it the next
// person sees a run that simply stopped and cannot tell it from a crash.
func TestInterruptRecordsTheStopCause(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("stop@piforge.local", "stop-pass-123", "user")
	_, sess := env.mkSessionFor(user.ID, "stop")
	running := contracts.StatusRunning
	if _, err := env.store.UpdateSession(context.Background(), user.ID, sess.ID,
		store.SessionPatch{Status: &running}); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	// The stop is only recorded for a run that is actually in flight; the run row
	// is the durable half of that fact.
	if _, err := env.store.CreateRun(context.Background(), user.ID, store.NewRun{
		SessionID: sess.ID, Trigger: "user",
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	c := env.login("stop@piforge.local", "stop-pass-123")
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/interrupt", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("interrupt: %d %s", rec.Code, rec.Body.String())
	}
	events, err := env.store.ListEvents(context.Background(), user.ID, sess.ID, 0, 50)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var marker *contracts.Event
	for _, ev := range events {
		if ev.Type == contracts.EvRunInterrupted {
			marker = ev
		}
	}
	if marker == nil {
		t.Fatalf("no run.interrupted event after a stop: %+v", events)
	}
	var body struct {
		Cause  string `json:"cause"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(marker.Payload, &body); err != nil {
		t.Fatalf("decode stop marker: %v", err)
	}
	if body.Cause != recovery.CauseStop || body.Reason != recovery.ReasonStop {
		t.Fatalf("stop marker = %+v", body)
	}
}

// TestInterruptWithoutAStartedRunWritesNoMarker: a click that lands after the
// run completed must not paint an interruption over a successful turn.
func TestInterruptWithoutAStartedRunWritesNoMarker(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("stale@piforge.local", "stale-pass-123", "user")
	_, sess := env.mkSessionFor(user.ID, "stale")
	running := contracts.StatusRunning
	if _, err := env.store.UpdateSession(context.Background(), user.ID, sess.ID,
		store.SessionPatch{Status: &running}); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	c := env.login("stale@piforge.local", "stale-pass-123")
	if rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/interrupt", nil, c); rec.Code != http.StatusOK {
		t.Fatalf("interrupt: %d %s", rec.Code, rec.Body.String())
	}
	events, err := env.store.ListEvents(context.Background(), user.ID, sess.ID, 0, 50)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, ev := range events {
		if ev.Type == contracts.EvRunInterrupted {
			t.Fatalf("a stop with no started run wrote an interruption marker: %+v", ev)
		}
	}
}

// TestContinueRefusesAfterTheUserAlreadyContinued: the event log records that a
// run started after the interruption, so the marker must not offer to continue
// it again.
func TestContinueRefusesAfterTheUserAlreadyContinued(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("again@piforge.local", "again-pass-123", "user")
	project, sess := env.mkSessionFor(user.ID, "again")
	interruptedSession(t, env, user.ID, project.ID, sess.ID,
		recovery.CauseStop, recovery.ReasonStop, "fix it")
	if _, err := env.store.AppendEvent(context.Background(), user.ID, &contracts.Event{
		SessionID: sess.ID, RunID: "run_after", Type: contracts.EvRunStarted,
		Payload: json.RawMessage(`{}`), TS: time.Now(),
	}); err != nil {
		t.Fatalf("append later run: %v", err)
	}

	c := env.login("again@piforge.local", "again-pass-123")
	rec := env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/recovery", nil, c)
	var view recoveryView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode recovery: %v", err)
	}
	if view.CanContinue || !strings.Contains(view.BlockedReason, "已经继续过") {
		t.Fatalf("view = %+v", view)
	}
}
