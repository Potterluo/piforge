package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// TestProviderTestButton exercises the "does my key work" probe against a real
// upstream, because the whole point of the endpoint is to report what that
// upstream actually said.
func TestProviderTestButton(t *testing.T) {
	var (
		gotKey   string
		gotPath  string
		gotModel string
		status   = http.StatusOK
		respBody = `{"id":"msg_1","content":[{"type":"text","text":"pong"}]}`
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(upstream.Close)

	env := newTestEnv(t)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")

	created := env.doAuthed(http.MethodPost, "/v1/providers", map[string]any{
		"name": "probe", "api": "anthropic-messages", "base_url": upstream.URL,
		"api_key": "sk-ant-secret", "models": []map[string]any{{"id": "claude-sonnet-4-5"}},
	}, c)
	if created.Code != http.StatusCreated {
		t.Fatalf("create provider = %d body %s", created.Code, created.Body.String())
	}
	var provider struct {
		Provider *contracts.Provider `json:"provider"`
	}
	env.mustJSON(created, &provider)

	rec := env.doAuthed(http.MethodPost, "/v1/providers/"+provider.Provider.ID+"/test", map[string]any{}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("test = %d body %s", rec.Code, rec.Body.String())
	}
	var result providerTestResult
	env.mustJSON(rec, &result)
	if !result.OK || result.Status != http.StatusOK {
		t.Fatalf("result = %+v", result)
	}
	if gotKey != "sk-ant-secret" {
		t.Fatalf("upstream saw key %q, want the real one", gotKey)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	// The first configured model is what the user pinned; testing a different
	// one would report success for a model they are not going to use.
	if gotModel != "claude-sonnet-4-5" {
		t.Fatalf("model = %q, want the configured one", gotModel)
	}
	if strings.Contains(rec.Body.String(), "sk-ant-secret") {
		t.Fatal("the response echoed the key")
	}

	// A rejected key reports the upstream's own words, which is what makes the
	// button useful.
	status = http.StatusUnauthorized
	respBody = `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	rec = env.doAuthed(http.MethodPost, "/v1/providers/"+provider.Provider.ID+"/test", map[string]any{}, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("a failed probe should still answer 200 with ok:false, got %d", rec.Code)
	}
	var failed providerTestResult
	env.mustJSON(rec, &failed)
	if failed.OK || failed.Status != http.StatusUnauthorized {
		t.Fatalf("result = %+v", failed)
	}
	if !strings.Contains(failed.Message, "invalid x-api-key") {
		t.Fatalf("message = %q, want the provider's own words", failed.Message)
	}
}

func TestProviderTestRefusesAKeylessProvider(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")

	created := env.doAuthed(http.MethodPost, "/v1/providers", map[string]any{
		"name": "nokey", "api": "openai-compat", "base_url": "https://example.invalid/v1",
	}, c)
	var provider struct {
		Provider *contracts.Provider `json:"provider"`
	}
	env.mustJSON(created, &provider)

	rec := env.doAuthed(http.MethodPost, "/v1/providers/"+provider.Provider.ID+"/test", map[string]any{}, c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body %s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no API key") {
		t.Fatalf("error does not say what is missing: %s", rec.Body.String())
	}
}

func TestProviderTestIsScopedToTheCaller(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("a@example.test", "pw", contracts.RoleUser)
	env.mkUser("b@example.test", "pw", contracts.RoleUser)
	alice := env.login("a@example.test", "pw")
	bob := env.login("b@example.test", "pw")

	created := env.doAuthed(http.MethodPost, "/v1/providers", map[string]any{
		"name": "mine", "api": "openai-compat", "base_url": "https://example.invalid/v1",
		"api_key": "sk-a",
	}, alice)
	var provider struct {
		Provider *contracts.Provider `json:"provider"`
	}
	env.mustJSON(created, &provider)

	// Bob must not be able to probe Alice's provider, and must not learn it
	// exists.
	rec := env.doAuthed(http.MethodPost, "/v1/providers/"+provider.Provider.ID+"/test", map[string]any{}, bob)
	env.mustErr(rec, http.StatusNotFound, contracts.ErrNotFound)
}

// TestProbeURLNeverDoubleV1 pins the bug the QA pass found: a base that already
// ends in /v1 was probed at /v1/v1/chat/completions, so a healthy endpoint could
// report failure and the connectivity test could not be trusted either way.
func TestProbeURLNeverDoubleV1(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"https://api.example.com", "https://api.example.com/v1/chat/completions"},
		{"https://api.example.com/", "https://api.example.com/v1/chat/completions"},
		{"https://api.example.com/v1", "https://api.example.com/v1/chat/completions"},
		{"https://api.example.com/v1/", "https://api.example.com/v1/chat/completions"},
	}
	for _, tc := range cases {
		provider := &contracts.Provider{
			API:     "openai-compat",
			BaseURL: tc.base,
			Models:  []contracts.ModelSpec{{ID: "m"}},
		}
		target, _, _, err := probeRequest(provider, "k")
		if err != nil {
			t.Fatalf("%s: %v", tc.base, err)
		}
		if target != tc.want {
			t.Fatalf("base %q -> %q, want %q", tc.base, target, tc.want)
		}
	}
}
