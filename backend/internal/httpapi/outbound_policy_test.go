package httpapi

// The outbound URL policy, proven on the two paths adversarial acceptance #4
// measured (e2e/.artifacts/adversarial4/REPORT.md attacks 18 and 21), plus the
// forge api_base.
//
// Two layers are deliberately tested apart:
//
//   - the *deployment default* (Deps.URLPolicy nil, which is what cmd/piforged
//     wires when no allow file is configured) refuses the report's targets. This
//     is the fence an attacker meets.
//   - the *allow-list* (what an operator or the test harness writes) is the only
//     way an internal server stays reachable, and it is scoped to the host and
//     the port.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/urlguard"
)

// deploymentDefault rebuilds the env with the policy a deployment gets when no
// allow file is configured: public http(s) only.
func (e *testEnv) deploymentDefault() {
	e.t.Helper()
	e.deps.URLPolicy = nil
	e.rebuild()
}

// refusalOf decodes a 400 URL refusal and returns its reason, host and message.
func refusalOf(t *testing.T, rec *httptest.ResponseRecorder) (reason, host, message string) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error *contracts.APIError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if body.Error == nil || body.Error.Code != contracts.ErrInvalidArgument {
		t.Fatalf("body = %s, want invalid_argument", rec.Body.String())
	}
	reason, _ = body.Error.Details["reason"].(string)
	host, _ = body.Error.Details["host"].(string)
	return reason, host, body.Error.Message
}

// TestTheMCPBrokerRefusesTheReportsTargets is the first finding, at the two
// points the report used: saving the server, and calling it.
func TestTheMCPBrokerRefusesTheReportsTargets(t *testing.T) {
	env := newTestEnv(t)
	env.deploymentDefault()
	user := env.mkUser("mcp-policy@piforge.local", "pw", contracts.RoleUser)
	c := env.login("mcp-policy@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	// (a) Save time: the report's own URLs, through the real endpoint.
	saves := []struct {
		url    string
		reason string
	}{
		{"http://piforge:7700", "host-name"}, // the control plane's own loopback by service name
		{"http://127.0.0.1:7700/healthz", "loopback"},
		{"http://169.254.169.254/latest/meta-data/", "link-local"},
		{"http://host.docker.internal:9021/mcp", "host-name"},
		{"http://10.20.30.40:9021/mcp", "private"}, // a sibling container by address
		{"http://[fd00::1]:9021/mcp", "unique-local"},
		{"file:///data/master.key", "scheme"},
	}
	for _, tc := range saves {
		rec := env.doAuthed(http.MethodPost, "/v1/mcp/servers", map[string]any{
			"name": "probe", "transport": "http", "url": tc.url,
		}, c)
		reason, host, message := refusalOf(t, rec)
		if reason != tc.reason {
			t.Fatalf("%s: reason = %q, want %q (body %s)", tc.url, reason, tc.reason, rec.Body.String())
		}
		if tc.reason == "scheme" {
			// A URL with no usable scheme has no host to name; the reason code
			// and the scheme are the whole answer.
			continue
		}
		if host == "" {
			t.Fatalf("%s: the refusal does not name the host: %s", tc.url, rec.Body.String())
		}
		if !strings.Contains(message, host) {
			t.Fatalf("%s: message %q does not name the host %q", tc.url, message, host)
		}
	}

	// Nothing was stored by any refused save.
	list := env.doAuthed(http.MethodGet, "/v1/mcp/servers", nil, c)
	var listed mcpServersResponse
	env.mustJSON(list, &listed)
	if len(listed.Servers) != 0 {
		t.Fatalf("a refused save stored a row: %+v", listed.Servers)
	}

	// (b) Call time: a row that exists anyway (saved before the policy, or by a
	// future bug) is refused by the broker, and the upstream is never dialled.
	var reached int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		_, _ = w.Write([]byte(`{"marker":"A4-ONLY-THE-CONTROL-PLANE-CAN-READ-THIS"}`))
	}))
	defer upstream.Close()

	server, err := env.store.CreateMCPServer(t.Context(), user.ID, contracts.MCPServer{
		Name: "legacy", Transport: contracts.MCPHTTP, URL: upstream.URL + "/mcp", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create mcp server: %v", err)
	}
	token, err := env.store.EnsureMCPToken(t.Context(), user.ID, sess.ID, server.ID)
	if err != nil {
		t.Fatalf("mint broker token: %v", err)
	}
	// The sandbox's shape: the broker URL plus a sub-path, exactly what the
	// report sent (`/gw/mcp/<token>/healthz`).
	rec := env.do(http.MethodGet, MCPGatewayPrefix+token+"/healthz", nil, nil, "")
	reason, host, message := refusalOf(t, rec)
	if reason != "loopback" {
		t.Fatalf("broker refusal reason = %q, want loopback (body %s)", reason, rec.Body.String())
	}
	if host != "127.0.0.1" || !strings.Contains(message, "127.0.0.1") {
		t.Fatalf("broker refusal names %q / %q, want the host", host, message)
	}
	if reached != 0 {
		t.Fatalf("the broker dialled the refused target %d time(s)", reached)
	}
}

// TestAnAllowedInternalMCPServerStillWorks is the other half: the allow-list is
// the supported way to keep an internal server.
func TestAnAllowedInternalMCPServerStillWorks(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("mcp-allowed@piforge.local", "pw", contracts.RoleUser)
	_, sess := env.mkSessionFor(user.ID, "repo")

	var reached int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tool":"search"}}`))
	}))
	defer upstream.Close()

	server, err := env.store.CreateMCPServer(t.Context(), user.ID, contracts.MCPServer{
		Name: "internal", Transport: contracts.MCPHTTP, URL: upstream.URL + "/mcp", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create mcp server: %v", err)
	}
	token, err := env.store.EnsureMCPToken(t.Context(), user.ID, sess.ID, server.ID)
	if err != nil {
		t.Fatalf("mint broker token: %v", err)
	}
	// The harness policy allows 127.0.0.1, the way an operator's file would name
	// the internal host; the call goes through and the upstream answers.
	rec := env.do(http.MethodPost, MCPGatewayPrefix+token, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
	}, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("allowed broker call = %d %s", rec.Code, rec.Body.String())
	}
	if reached != 1 || !strings.Contains(rec.Body.String(), "search") {
		t.Fatalf("the allowed server was not reached: reached=%d body=%s", reached, rec.Body.String())
	}
}

// TestTheProviderBaseURLIsRefusedTheSameWay is the second finding: save, patch,
// and the probe.
func TestTheProviderBaseURLIsRefusedTheSameWay(t *testing.T) {
	env := newTestEnv(t)
	env.deploymentDefault()
	user := env.mkUser("provider-policy@piforge.local", "pw", contracts.RoleUser)
	c := env.login("provider-policy@piforge.local", "pw")

	for _, tc := range []struct {
		url    string
		reason string
	}{
		{"http://127.0.0.1:7700", "loopback"},
		{"http://169.254.169.254", "link-local"},
		{"http://piforge:7700", "host-name"},
		{"http://192.168.0.10:8080/v1", "private"},
	} {
		rec := env.doAuthed(http.MethodPost, "/v1/providers", map[string]any{
			"name": "evil", "api": "openai-compat", "base_url": tc.url,
		}, c)
		reason, host, message := refusalOf(t, rec)
		if reason != tc.reason || host == "" || !strings.Contains(message, host) {
			t.Fatalf("%s: reason=%q host=%q message=%q", tc.url, reason, host, message)
		}
	}

	// A row that exists anyway: /test must refuse it, and the upstream must not
	// see a request. This is the report's measured attack (the marker answered
	// 200 and saw the control plane's address).
	var reached int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	sealed, err := env.deps.Secrets.SealString("sk-a4-dummy")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	legacy, err := env.store.CreateProvider(t.Context(), user.ID, store.ProviderInput{
		Name: "legacy", API: "openai-compat", BaseURL: upstream.URL, KeyCipher: sealed,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	rec := env.doAuthed(http.MethodPost, "/v1/providers/"+legacy.ID+"/test", nil, c)
	reason, _, message := refusalOf(t, rec)
	if reason != "loopback" || !strings.Contains(message, "127.0.0.1") {
		t.Fatalf("probe refusal = %q / %q", reason, message)
	}
	if reached != 0 {
		t.Fatalf("the probe dialled the refused base_url %d time(s)", reached)
	}

	// And patching the same row's base_url is refused.
	rec = env.doAuthed(http.MethodPatch, "/v1/providers/"+legacy.ID, map[string]any{
		"base_url": "http://169.254.169.254/v1",
	}, c)
	if reason, _, _ := refusalOf(t, rec); reason != "link-local" {
		t.Fatalf("patch refusal reason = %q, want link-local", reason)
	}
}

// TestTheProviderProbeRefusesARedirectToTheMetadataService: the base URL is
// allowed, the redirect is not. The probe must report a refusal, not follow it.
func TestTheProviderProbeRefusesARedirectToTheMetadataService(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("redirect@piforge.local", "pw", contracts.RoleUser)
	c := env.login("redirect@piforge.local", "pw")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer upstream.Close()

	sealed, err := env.deps.Secrets.SealString("sk-a4-dummy")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	provider, err := env.store.CreateProvider(t.Context(), user.ID, store.ProviderInput{
		Name: "redirector", API: "openai-compat", BaseURL: upstream.URL, KeyCipher: sealed,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	rec := env.doAuthed(http.MethodPost, "/v1/providers/"+provider.ID+"/test", nil, c)
	reason, _, _ := refusalOf(t, rec)
	if reason != "link-local" {
		t.Fatalf("redirect refusal reason = %q, want link-local (body %s)", reason, rec.Body.String())
	}
}

// TestTheForgeAPIBaseIsRefused: the PR client fetches api_base from this
// process, so it is the same class as the MCP server URL.
func TestTheForgeAPIBaseIsRefused(t *testing.T) {
	env := newTestEnv(t)
	env.deploymentDefault()
	user := env.mkUser("forge-policy@piforge.local", "pw", contracts.RoleUser)
	c := env.login("forge-policy@piforge.local", "pw")
	project, _ := env.mkSessionFor(user.ID, "repo")

	rec := env.doAuthed(http.MethodPut, "/v1/projects/"+project.ID+"/forge", map[string]any{
		"kind": "github", "api_base": "http://169.254.169.254", "repo": "acme/app", "token": "t",
	}, c)
	reason, host, _ := refusalOf(t, rec)
	if reason != "link-local" || host != "169.254.169.254" {
		t.Fatalf("forge refusal reason=%q host=%q (body %s)", reason, host, rec.Body.String())
	}
}

// TestTheDeploymentDefaultIsWhatRefuses is the guard the harness comment points
// at: the permissive test policy is an explicit allow-list, not the default.
func TestTheDeploymentDefaultIsWhatRefuses(t *testing.T) {
	policy := urlguard.Default()
	for _, raw := range []string{
		"http://piforge:7700/healthz",
		"http://127.0.0.1:7700/",
		"http://169.254.169.254/",
	} {
		if _, err := policy.CheckSave(t.Context(), raw); err == nil {
			t.Fatalf("the deployment default allowed %q", raw)
		}
	}
	// A name this environment cannot resolve is accepted at save time and
	// refused at call time; that is the documented tradeoff, not a widening.
	if _, err := policy.CheckSave(t.Context(), "https://example.invalid/v1"); err != nil {
		t.Fatalf("CheckSave refused an unresolvable name: %v", err)
	}
}
