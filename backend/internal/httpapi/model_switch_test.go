package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// patchModel points the session row at another model ref, the way the composer's
// picker does (a PATCH, not a send).
func patchModel(t *testing.T, e *testEnv, c *creds, sessionID, ref string) {
	t.Helper()
	rec := e.doAuthed(http.MethodPatch, "/v1/sessions/"+sessionID, map[string]string{"model_ref": ref}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch model_ref: %d %s", rec.Code, rec.Body.String())
	}
}

// TestModelSwitchRestartsTheAgent is QA-14's "cosmetic picker": the row said
// `volc/no-such-model` while the agent kept answering from the model it spawned
// with. A switch needs a new process — models.json and the gateway token are
// minted per spawn — so the next turn must restart it with the new reference.
func TestModelSwitchRestartsTheAgent(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("switch@example.com", "password-123", contracts.RoleUser)
	c := env.login("switch@example.com", "password-123")
	_, sess := env.mkSessionFor(user.ID, "switch")

	// The session's live process was launched with the row's original model, and
	// it is idle: a switch during a run is refused, which has its own test below.
	env.runtime.mu.Lock()
	env.runtime.startedModel = sess.ModelRef
	env.runtime.modelLive = true
	env.runtime.status = contracts.RuntimeStatus{Running: false, Streaming: false}
	env.runtime.mu.Unlock()

	patchModel(t, env, c, sess.ID, "shared/gpt2")
	if got := env.runtime.stops; len(got) != 0 {
		t.Fatalf("the picker alone stopped the agent: %v", got)
	}

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id": "m-switch",
		"text":          "hello",
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("send after a model switch = %d %s", rec.Code, rec.Body.String())
	}
	if stops := env.runtime.stops; len(stops) != 1 || stops[0] != sess.ID {
		t.Fatalf("stops = %v, want exactly one for %s", stops, sess.ID)
	}
	if got := env.runtime.promptCount(); got != 1 {
		t.Fatalf("prompts = %d, want 1", got)
	}
	last := env.runtime.starts[len(env.runtime.starts)-1]
	if last.ModelRef != "shared/gpt2" {
		t.Fatalf("restarted with model_ref %q, want shared/gpt2", last.ModelRef)
	}
}

// TestModelSwitchWithoutAKeyFailsClearly: picking a provider with no key must
// fail in the user's language before anything runs, instead of falling back to
// the old model and looking like it worked.
func TestModelSwitchWithoutAKeyFailsClearly(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("nokey@example.com", "password-123", contracts.RoleUser)
	c := env.login("nokey@example.com", "password-123")
	_, sess := env.mkSessionFor(user.ID, "nokey")

	if _, err := env.store.CreateProvider(context.Background(), user.ID, store.ProviderInput{
		Name:    "keyless",
		API:     "openai-compat",
		BaseURL: "https://example.invalid/v1",
		Models:  []contracts.ModelSpec{{ID: "some-model"}},
		// no KeyCipher: this is the provider the QA pass picked.
	}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	env.runtime.mu.Lock()
	env.runtime.startedModel = sess.ModelRef
	env.runtime.modelLive = true
	env.runtime.mu.Unlock()

	patchModel(t, env, c, sess.ID, "keyless/some-model")

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id": "m-nokey",
		"text":          "hello",
	}, c)
	env.mustErr(rec, http.StatusBadRequest, contracts.ErrNoModel)
	// The message must name the provider, so the UI can render
	// 服务商「keyless」还没有 API Key.
	body := rec.Body.String()
	if !strings.Contains(body, "keyless") || !strings.Contains(body, "no API key") {
		t.Fatalf("error does not explain the missing key: %s", body)
	}
	if got := env.runtime.promptCount(); got != 0 {
		t.Fatalf("prompts = %d: the turn ran on the old model instead of failing", got)
	}
	if stops := env.runtime.stops; len(stops) != 0 {
		t.Fatalf("stops = %v: a broken target must not kill the working agent", stops)
	}
}
