package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// TestSendToAKeylessExplicitModelFailsBeforePrompting pins the acceptance
// finding: a keyless provider was selectable, and sending to it cost three
// automatic retries (~30s) before a raw 502 reached the transcript.
//
// The path matters. The existing model-switch test covers a *live* agent whose
// stored model changed; restartForModelSwitch explains that before stopping it.
// The acceptance case was a session created with the keyless reference, so
// there was no live process and nothing checked the provider. The turn was
// accepted, Pi prompted, the gateway answered 502 provider_unavailable, and Pi
// retried it three times.
//
// A configuration error is not a run. The handler must return the typed
// no_model error before the runtime is touched, so the retry count is zero.
func TestSendToAKeylessExplicitModelFailsBeforePrompting(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("keyless-send@example.com", "password-123", contracts.RoleUser)
	c := env.login("keyless-send@example.com", "password-123")
	project, _ := env.mkSessionFor(user.ID, "keyless-send")

	// The same shape a fresh deployment ships: a provider with models but no
	// key. (mkSessionFor created a keyed `shared/gpt` fixture; this one is the
	// keyless provider the picker used to offer.)
	if _, err := env.store.CreateProvider(context.Background(), user.ID, store.ProviderInput{
		Name:    "shared-anthropic",
		API:     "anthropic-messages",
		BaseURL: "https://example.invalid",
		Models:  []contracts.ModelSpec{{ID: "claude-sonnet-4-5"}},
	}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	// Created with the keyless reference — the new-session dialog's shape, not a
	// PATCH after a live agent exists.
	sess, err := env.store.CreateSession(context.Background(), user.ID, store.NewSession{
		ProjectID: project.ID,
		Kind:      contracts.KindMain,
		Title:     "pinned to a keyless provider",
		ModelRef:  "shared-anthropic/claude-sonnet-4-5",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id": "m-keyless",
		"text":          "hello",
	}, c)
	env.mustErr(rec, http.StatusBadRequest, contracts.ErrNoModel)

	body := rec.Body.String()
	// The UI maps this pair to 服务商「shared-anthropic」还没有 API Key.
	if !strings.Contains(body, "shared-anthropic") || !strings.Contains(body, "no API key") {
		t.Fatalf("error does not explain the missing key: %s", body)
	}
	// Zero retries: nothing ever prompted the agent.
	if got := env.runtime.promptCount(); got != 0 {
		t.Fatalf("prompts = %d, want 0: the turn ran instead of failing before the agent", got)
	}
	if got := env.runtime.startCount(); got != 0 {
		t.Fatalf("agent starts = %d, want 0: a configuration error must not spawn a process", got)
	}
}
