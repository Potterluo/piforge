package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The MCP broker is the stage-3 answer to "the sandbox must not hold an MCP
// credential" (docs/EXECUTION-BOUNDARY.md §4). These tests pin the three things
// that make it an answer rather than a proxy:
//
//   - the sandbox's token is not the upstream's credential: the real one is added
//     on egress and never appears in the request the sandbox could see;
//   - an unknown token is refused;
//   - revoking the session's tokens stops the same URL working immediately.
func TestMCPBrokerInjectsTheCredentialAndStopsAfterRevoke(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("mcp@piforge.local", "pw", contracts.RoleUser)
	c := env.login("mcp@piforge.local", "pw")
	_, sess := env.mkSessionFor(user.ID, "repo")

	// The upstream MCP server: it answers only with the credential, which is what
	// makes "the header was injected" observable instead of assumed.
	var sawAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthorization = r.Header.Get("Authorization")
		if sawAuthorization != "Bearer real-mcp-credential" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"no credential"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`))
	}))
	defer upstream.Close()

	server, err := env.store.CreateMCPServer(t.Context(), user.ID, contracts.MCPServer{
		Name:      "docs",
		Transport: contracts.MCPHTTP,
		URL:       upstream.URL + "/mcp",
		Enabled:   true,
		Secrets:   "Authorization=Bearer real-mcp-credential",
	})
	if err != nil {
		t.Fatalf("create mcp server: %v", err)
	}

	token, err := env.store.EnsureMCPToken(t.Context(), user.ID, sess.ID, server.ID)
	if err != nil {
		t.Fatalf("mint broker token: %v", err)
	}
	if strings.Contains(token, "real-mcp-credential") {
		t.Fatal("the broker token is the credential")
	}

	// The sandbox's request: the broker URL, with the token as the path — no
	// Authorization header at all.
	rec := env.do(http.MethodPost, MCPGatewayPrefix+token, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
	}, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("brokered call: %d %s", rec.Code, rec.Body.String())
	}
	if sawAuthorization != "Bearer real-mcp-credential" {
		t.Fatalf("the upstream saw %q, not the injected credential", sawAuthorization)
	}
	if !strings.Contains(rec.Body.String(), "protocolVersion") {
		t.Fatalf("the upstream answer did not come back: %s", rec.Body.String())
	}

	// An unknown token is 401 and never reaches the upstream.
	sawAuthorization = ""
	if rec := env.do(http.MethodPost, MCPGatewayPrefix+"not-a-token", map[string]any{}, nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token = %d, want 401", rec.Code)
	}
	if sawAuthorization != "" {
		t.Fatal("an unknown token reached the upstream")
	}

	// Revoke, then the same URL is worthless.
	rec = env.doAuthed(http.MethodDelete, "/v1/sessions/"+sess.ID+"/mcp-token", nil, c)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.do(http.MethodPost, MCPGatewayPrefix+token, map[string]any{}, nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token = %d, want 401", rec.Code)
	}
	// And it cannot be re-minted by accident: a respawned agent asks again and
	// gets a refusal, so revocation is durable rather than per-process.
	if _, err := env.store.EnsureMCPToken(t.Context(), user.ID, sess.ID, server.ID); err == nil {
		t.Fatal("a revoked session's MCP token was minted again")
	}
}

// A stdio MCP server has no egress point to inject a credential at, so the
// broker refuses to be one for it.
func TestMCPBrokerRefusesAStdioServer(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("stdio@piforge.local", "pw", contracts.RoleUser)
	_, sess := env.mkSessionFor(user.ID, "repo")
	server, err := env.store.CreateMCPServer(t.Context(), user.ID, contracts.MCPServer{
		Name:      "local",
		Transport: contracts.MCPStdio,
		Command:   "npx",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create mcp server: %v", err)
	}
	token, err := env.store.EnsureMCPToken(t.Context(), user.ID, sess.ID, server.ID)
	if err != nil {
		t.Fatalf("mint broker token: %v", err)
	}
	if rec := env.do(http.MethodPost, MCPGatewayPrefix+token, map[string]any{}, nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("stdio server through the broker = %d, want 401", rec.Code)
	}
}
