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
)

// The first message after an interruption must tell the agent that the previous
// turn was cut short.
//
// Measured on the deployment (HANDOFF §2, e2e/.artifacts/fix-interrupt-context):
// after 停止, "Say beta." reached the model and started its own run, but the answer
// was the interrupted 1–200 list. The agent's conversation still held the
// half-written answer, and Pi keeps it there: nothing in the control plane can
// edit a persisted entry. So the control plane marks the interruption in the
// prompt it composes — the same server-side composition the review-comment and
// 继续 paths use — and the transcript shows that exact sentence, because that is
// what the agent received.
func TestFirstMessageAfterAnInterruptMarksTheInterruption(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("after-stop@piforge.local", "after-stop-123", "user")
	project, sess := env.mkSessionFor(user.ID, "after-stop")
	interruptedSession(t, env, user.ID, project.ID, sess.ID,
		recovery.CauseStop, recovery.ReasonStop, "count to 200")

	c := env.login("after-stop@piforge.local", "after-stop-123")
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id": "after-stop-1",
		"text":          "Say beta.",
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST message after a stop: %d %s", rec.Code, rec.Body.String())
	}

	env.runtime.mu.Lock()
	prompts := append([]contracts.PromptRequest(nil), env.runtime.prompts...)
	env.runtime.mu.Unlock()
	if len(prompts) != 1 {
		t.Fatalf("runtime received %d prompts, want 1", len(prompts))
	}
	got := prompts[0].Text
	if !strings.HasPrefix(got, interruptedTurnNote) {
		t.Fatalf("the prompt does not mark the interruption:\n%s", got)
	}
	if !strings.Contains(got, "Say beta.") {
		t.Fatalf("the prompt lost the user's message:\n%s", got)
	}

	// The stored message is the same string, which is what makes the transcript
	// and the agent's prompt one thing instead of two that can disagree.
	stored, err := env.store.Message(context.Background(), user.ID, sess.ID, "after-stop-1")
	if err != nil {
		t.Fatalf("read the stored message: %v", err)
	}
	if stored.Text != got {
		t.Fatalf("stored message = %q, prompt = %q", stored.Text, got)
	}
}

// The note belongs to the first message only: a run has started since, so the
// interruption is history, and repeating the sentence would be noise nobody can
// remove from their own message.
func TestTheInterruptionNoteIsNotRepeatedOnceARunStarted(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("after-again@piforge.local", "after-again-123", "user")
	project, sess := env.mkSessionFor(user.ID, "after-again")
	interruptedSession(t, env, user.ID, project.ID, sess.ID,
		recovery.CauseStop, recovery.ReasonStop, "count to 200")

	// A run after the interruption: exactly the shape the runtime leaves behind.
	if _, err := env.store.AppendEvent(context.Background(), user.ID, &contracts.Event{
		SessionID: sess.ID, RunID: "run_after", Type: contracts.EvRunStarted,
		Payload: json.RawMessage(`{}`), TS: time.Now(),
	}); err != nil {
		t.Fatalf("append run.started: %v", err)
	}

	c := env.login("after-again@piforge.local", "after-again-123")
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id": "after-again-1",
		"text":          "Say gamma.",
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST message: %d %s", rec.Code, rec.Body.String())
	}
	env.runtime.mu.Lock()
	prompts := append([]contracts.PromptRequest(nil), env.runtime.prompts...)
	env.runtime.mu.Unlock()
	if len(prompts) != 1 {
		t.Fatalf("runtime received %d prompts, want 1", len(prompts))
	}
	if prompts[0].Text != "Say gamma." {
		t.Fatalf("a message after a continued interruption was rewritten: %q", prompts[0].Text)
	}
}

// 继续 has its own composed text, which already says the run was interrupted;
// prefixing it would say the same thing twice.
func TestContinueAfterAnInterruptKeepsItsOwnText(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("continue-once@piforge.local", "continue-once-123", "user")
	project, sess := env.mkSessionFor(user.ID, "continue-once")
	interruptedSession(t, env, user.ID, project.ID, sess.ID,
		recovery.CauseStop, recovery.ReasonStop, "count to 200")

	c := env.login("continue-once@piforge.local", "continue-once-123")
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id":        "continue-once-1",
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
	if strings.Contains(prompts[0].Text, interruptedTurnNote) {
		t.Fatalf("the continue prompt was prefixed twice:\n%s", prompts[0].Text)
	}
	if !strings.Contains(prompts[0].Text, recovery.ContinueInstruction) {
		t.Fatalf("the continue prompt lost its instruction:\n%s", prompts[0].Text)
	}
}
