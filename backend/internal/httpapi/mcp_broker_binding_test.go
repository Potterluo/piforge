package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The MCP broker token is minted by this control plane, stored in the sandbox's
// mcp.json and therefore exactly as portable as a model token was: copied out, it
// answered /gw/mcp from anywhere. These tests pin the binding that stops that
// (docs/EXECUTION-BOUNDARY.md §16.3): the broker asks "did this arrive from the
// session's sandbox" before it spends the user's credential.

// addressBinder is the smallest thing that behaves like llmgw.Binder's session
// half: it answers "allowed" only for the address it was told.
type addressBinder struct {
	sandboxAddr string
	// seen records the sessions it was asked about, so a test can prove the
	// broker consulted it rather than passing by luck.
	seen []string
}

func (a *addressBinder) AuthorizeSession(_ context.Context, sessionID, remoteAddr string) error {
	a.seen = append(a.seen, sessionID+"@"+remoteAddr)
	host, _, _ := strings.Cut(remoteAddr, ":")
	if a.sandboxAddr == "" || host == a.sandboxAddr {
		return nil
	}
	return fmt.Errorf("sandbox_address_mismatch: this session's sandbox is %s, the request came from %s", a.sandboxAddr, host)
}

// doFrom is env.do with a chosen source address, which is the whole subject here.
func (e *testEnv) doFrom(method, path, remoteAddr string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = remoteAddr
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func TestMCPBrokerRefusesATokenFromOutsideTheSandbox(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("bound-mcp@piforge.local", "pw", contracts.RoleUser)
	_, sess := env.mkSessionFor(user.ID, "repo")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`))
	}))
	defer upstream.Close()

	server, err := env.store.CreateMCPServer(t.Context(), user.ID, contracts.MCPServer{
		Name:      "docs",
		Transport: contracts.MCPHTTP,
		URL:       upstream.URL + "/mcp",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create mcp server: %v", err)
	}
	token, err := env.store.EnsureMCPToken(t.Context(), user.ID, sess.ID, server.ID)
	if err != nil {
		t.Fatalf("mint broker token: %v", err)
	}

	binder := &addressBinder{sandboxAddr: "172.20.0.5"}
	env.deps.SandboxBinding = binder
	env.rebuild()

	// From inside the sandbox: the brokered call still works.
	if rec := env.doFrom(http.MethodPost, MCPGatewayPrefix+token, "172.20.0.5:41234", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
	}); rec.Code != http.StatusOK {
		t.Fatalf("from inside the sandbox: %d %s", rec.Code, rec.Body.String())
	}

	// From the host with the same token: refused, and nothing was proxied.
	if rec := env.doFrom(http.MethodPost, MCPGatewayPrefix+token, "192.0.2.1:41234", map[string]any{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("from the host: %d, want 401 (%s)", rec.Code, rec.Body.String())
	}

	// From a second sandbox of the same tenant: refused too.
	if rec := env.doFrom(http.MethodPost, MCPGatewayPrefix+token, "172.20.0.6:41234", map[string]any{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("from a sibling sandbox: %d, want 401 (%s)", rec.Code, rec.Body.String())
	}

	if len(binder.seen) != 3 {
		t.Fatalf("the binder was consulted %d times, want 3 (%v)", len(binder.seen), binder.seen)
	}
	// The refusal is the same shape as every other broker refusal: a caller
	// cannot learn from it whether the token was wrong or the address was.
	if !strings.Contains(binder.seen[1], sess.ID) {
		t.Fatalf("the binder was not told the session: %q", binder.seen[1])
	}
}

// A deployment that wires no binder (a hand-built test server) keeps the old
// behaviour rather than panicking on a nil interface.
func TestMCPBrokerWithoutABinderStillAnswers(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("unbound-mcp@piforge.local", "pw", contracts.RoleUser)
	_, sess := env.mkSessionFor(user.ID, "repo")
	server, err := env.store.CreateMCPServer(t.Context(), user.ID, contracts.MCPServer{
		Name: "local", Transport: contracts.MCPStdio, Command: "npx", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create mcp server: %v", err)
	}
	token, err := env.store.EnsureMCPToken(t.Context(), user.ID, sess.ID, server.ID)
	if err != nil {
		t.Fatalf("mint broker token: %v", err)
	}
	// A stdio server is refused by the broker, which is a different 401 — the
	// point is that the nil binder did not panic before reaching it.
	if rec := env.do(http.MethodPost, MCPGatewayPrefix+token, map[string]any{}, nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("stdio server through the broker = %d, want 401", rec.Code)
	}
}
