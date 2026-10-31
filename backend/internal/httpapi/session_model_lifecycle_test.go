package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// deleteProviderNamed removes one of the user's providers, which is how both the
// acceptance pass and an operator break a session: the row keeps naming it, and
// nothing else about the session changes.
func deleteProviderNamed(t *testing.T, e *testEnv, userID, name string) {
	t.Helper()
	providers, err := e.store.ListProviders(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	for _, provider := range providers {
		if provider.Name == name {
			if err := e.store.DeleteProvider(context.Background(), userID, provider.ID); err != nil {
				t.Fatalf("DeleteProvider(%s): %v", name, err)
			}
			return
		}
	}
	t.Fatalf("provider %q not found", name)
}

// TestSendReResolvesAModelWhoseProviderIsGone is the acceptance pass's dead
// session (HIGH): a session created before a provider change answered 500 on
// every send forever. The stored reference named a provider that had been
// deleted and re-created under a working default, and nothing ever looked again.
//
// The next send must resolve again, persist the working reference and say in the
// response which model the turn actually used — a conversation has to survive an
// operator rotating configuration.
func TestSendReResolvesAModelWhoseProviderIsGone(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("revive@example.com", "password-123", contracts.RoleUser)
	c := env.login("revive@example.com", "password-123")
	_, sess := env.mkSessionFor(user.ID, "revive")

	// The account's default is a provider that exists and has a key; the one the
	// session pinned is deleted under it.
	env.ensureProvider(user.ID, "volc", "deepseek-v4-flash")
	env.deps.DefaultProvider = "volc"
	env.rebuild()
	deleteProviderNamed(t, env, user.ID, "shared")

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id": "m-revive",
		"text":          "still there?",
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("send to a session whose provider was deleted = %d %s", rec.Code, rec.Body.String())
	}
	var res contracts.PromptResult
	env.mustJSON(rec, &res)
	if !res.Accepted {
		t.Fatalf("result = %+v", res)
	}
	// The run used the working model, and said where it came from.
	if res.ModelRef != "volc/deepseek-v4-flash" {
		t.Fatalf("model_ref = %q, want the working default", res.ModelRef)
	}
	if res.ModelFallbackFrom != "shared/gpt" {
		t.Fatalf("model_fallback_from = %q, want the deleted reference", res.ModelFallbackFrom)
	}
	if got := env.runtime.promptCount(); got != 1 {
		t.Fatalf("prompts = %d, want 1", got)
	}
	env.runtime.mu.Lock()
	prompted := env.runtime.prompts[0].ModelRef
	started := env.runtime.starts[len(env.runtime.starts)-1].ModelRef
	env.runtime.mu.Unlock()
	if prompted != "volc/deepseek-v4-flash" {
		t.Fatalf("the agent was prompted with %q, want the re-resolved model", prompted)
	}
	if started != "volc/deepseek-v4-flash" {
		t.Fatalf("the agent was started with %q, want the re-resolved model", started)
	}

	// The session row carries the new reference: the next send, the picker and
	// the header all agree with what actually ran.
	rec = env.do(http.MethodGet, "/v1/sessions/"+sess.ID, nil, c, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("session detail = %d", rec.Code)
	}
	var detail struct {
		Session *contracts.Session `json:"session"`
	}
	env.mustJSON(rec, &detail)
	if detail.Session.ModelRef != "volc/deepseek-v4-flash" {
		t.Fatalf("stored model_ref = %q, want the re-resolved one", detail.Session.ModelRef)
	}
}

// TestSendToADeletedProviderWithoutFallbackIsTyped: when there is nothing to
// fall back to, the answer must name the provider and the page that fixes it —
// not a bare 500, and not a silent old model.
func TestSendToADeletedProviderWithoutFallbackIsTyped(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("nofallback@example.com", "password-123", contracts.RoleUser)
	c := env.login("nofallback@example.com", "password-123")
	_, sess := env.mkSessionFor(user.ID, "nofallback")

	deleteProviderNamed(t, env, user.ID, "shared")

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id": "m-nofallback",
		"text":          "hello",
	}, c)
	env.mustErr(rec, http.StatusBadRequest, contracts.ErrNoModel)
	body := rec.Body.String()
	if !strings.Contains(body, "shared") || !strings.Contains(body, "not configured") {
		t.Fatalf("error does not name the deleted provider: %s", body)
	}
	// The UI maps this pair to 服务商「shared」尚未配置，请到「设置 → 模型与凭据」添加.
	if got := env.runtime.promptCount(); got != 0 {
		t.Fatalf("prompts = %d: a configuration error must not reach the agent", got)
	}
}

// TestSendRebuildsAnAgentSpawnedWithoutAModel is the other half of the same
// acceptance finding, and the one that produced the 500: Pi's models.json and
// the gateway token are minted per spawn, so a process started while the
// provider did not exist kept answering "No API key found for the selected
// model" long after the provider was back. Nothing could reach it — retrying
// reuses the same process.
//
// Once a model resolves again, the old process must be replaced before the
// prompt, and the replacement must be spawned with the resolved reference.
func TestSendRebuildsAnAgentSpawnedWithoutAModel(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("rebuild@example.com", "password-123", contracts.RoleUser)
	c := env.login("rebuild@example.com", "password-123")
	_, sess := env.mkSessionFor(user.ID, "rebuild")

	// A live agent that was started while no model could be configured. Its
	// identity and its missing configuration are what the runtime reports.
	env.runtime.mu.Lock()
	env.runtime.modelLive = true
	env.runtime.modelConfigMissing = true
	env.runtime.startedModel = ""
	env.runtime.status = contracts.RuntimeStatus{Running: false, Streaming: false}
	env.runtime.mu.Unlock()

	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/messages", map[string]any{
		"client_msg_id": "m-rebuild",
		"text":          "hello again",
	}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("send after the provider came back = %d %s", rec.Code, rec.Body.String())
	}
	env.runtime.mu.Lock()
	stops := append([]string(nil), env.runtime.stops...)
	starts := append([]contracts.SessionSpec(nil), env.runtime.starts...)
	env.runtime.mu.Unlock()
	if len(stops) != 1 || stops[0] != sess.ID {
		t.Fatalf("stops = %v, want exactly one for %s: the unusable process must be replaced", stops, sess.ID)
	}
	if len(starts) == 0 || starts[len(starts)-1].ModelRef != "shared/gpt" {
		t.Fatalf("starts = %+v, want a spawn with the resolved model", starts)
	}
	if got := env.runtime.promptCount(); got != 1 {
		t.Fatalf("prompts = %d, want 1", got)
	}
}

// TestSessionStatusNamesTheResolvedModel: the header reads this, so it must never
// be a model that does not exist and never the word "unknown". A session whose
// provider was deleted names what the next send would use; an account with no
// provider at all answers empty, which the UI renders as 未配置.
func TestSessionStatusNamesTheResolvedModel(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("status-model@example.com", "password-123", contracts.RoleUser)
	c := env.login("status-model@example.com", "password-123")
	_, sess := env.mkSessionFor(user.ID, "status-model")

	rec := env.do(http.MethodGet, "/v1/sessions/"+sess.ID+"/status", nil, c, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var status contracts.RuntimeStatus
	env.mustJSON(rec, &status)
	if status.ResolvedModelRef != "shared/gpt" {
		t.Fatalf("resolved_model_ref = %q, want shared/gpt", status.ResolvedModelRef)
	}
	if status.ModelUnavailable {
		t.Fatal("a session with a keyed provider reported no model")
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if strings.Contains(string(raw), "unknown") {
		t.Fatalf("status carries an internal model name: %s", raw)
	}

	// The provider disappears: the status names the fallback instead of going
	// blank, because that is what the next send will use.
	env.ensureProvider(user.ID, "volc", "deepseek-v4-flash")
	env.deps.DefaultProvider = "volc"
	env.rebuild()
	deleteProviderNamed(t, env, user.ID, "shared")

	rec = env.do(http.MethodGet, "/v1/sessions/"+sess.ID+"/status", nil, c, "")
	env.mustJSON(rec, &status)
	if status.ResolvedModelRef != "volc/deepseek-v4-flash" {
		t.Fatalf("resolved_model_ref = %q, want the fallback", status.ResolvedModelRef)
	}

	// An account with no provider at all: empty, never "unknown".
	bare := env.mkUser("status-bare@example.com", "password-123", contracts.RoleUser)
	bareCreds := env.login("status-bare@example.com", "password-123")
	bareProject, err := env.store.CreateProject(context.Background(), bare.ID,
		store.NewProject{Name: "bare", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	bareSession, err := env.store.CreateSession(context.Background(), bare.ID,
		store.NewSession{ProjectID: bareProject.ID, Kind: contracts.KindMain, Title: "bare"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	status = contracts.RuntimeStatus{}
	rec = env.do(http.MethodGet, "/v1/sessions/"+bareSession.ID+"/status", nil, bareCreds, "")
	env.mustJSON(rec, &status)
	if status.ResolvedModelRef != "" {
		t.Fatalf("resolved_model_ref = %q, want empty for an unconfigured account", status.ResolvedModelRef)
	}
	if !status.ModelUnavailable {
		t.Fatal("an account with no provider was not reported as unavailable")
	}
}
