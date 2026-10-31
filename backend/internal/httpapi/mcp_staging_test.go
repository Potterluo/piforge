package httpapi

import (
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/mcpstage"
)

// Stage 4's stdio decision, as the product states it rather than as a log line.
//
// Two surfaces have to agree, because they answer the same question at different
// times: the MCP panel (before a session exists) and the session's tooling
// (what this deployment can hand to this agent). Both are covered here for the
// isolated placement and for the local one, since only one of them can stage a
// stdio server.
func TestMCPPanelAndToolingSayWhenAStdioServerCannotBeStaged(t *testing.T) {
	for _, isolated := range []bool{true, false} {
		name := "local placement"
		if isolated {
			name = "isolated placement"
		}
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t)
			owner := env.mkUser("mcp-owner@piforge.local", "pw", contracts.RoleUser)
			c := env.login("mcp-owner@piforge.local", "pw")
			env.deps.AgentIsolated = isolated
			env.rebuild()

			stdio := env.mustMCP(t, c, map[string]any{
				"scope": "user", "name": "fs", "transport": "stdio", "command": "npx",
			})
			httpServer := env.mustMCP(t, c, map[string]any{
				"scope": "user", "name": "docs", "transport": "http", "url": "https://example.test/mcp",
			})
			_ = stdio
			_ = httpServer

			rec := env.doAuthed(http.MethodGet, "/v1/mcp/servers", nil, c)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET mcp servers = %d body %s", rec.Code, rec.Body.String())
			}
			var list struct {
				Servers       []*contracts.MCPServer       `json:"servers"`
				AgentIsolated bool                         `json:"agent_isolated"`
				Unavailable   map[string]mcpstage.Omission `json:"unavailable"`
			}
			env.mustJSON(rec, &list)
			if list.AgentIsolated != isolated {
				t.Fatalf("agent_isolated = %v, want %v", list.AgentIsolated, isolated)
			}
			// The panel's sentence, per server: present exactly when the
			// placement cannot stage it, and always with a reason a user can act on.
			if isolated {
				omission, ok := list.Unavailable["fs"]
				if !ok {
					t.Fatalf("unavailable = %v, want the stdio server listed", list.Unavailable)
				}
				if omission.Code != mcpstage.CodeStdioNeedsCredentials || omission.Reason == "" {
					t.Fatalf("omission = %+v, want a code and a reason", omission)
				}
				if _, ok := list.Unavailable["docs"]; ok {
					t.Fatalf("the HTTP server is reported as unavailable: %v", list.Unavailable)
				}
			} else if len(list.Unavailable) != 0 {
				t.Fatalf("a local placement reported unavailable servers: %v", list.Unavailable)
			}

			// The same answer on the session's tooling, because a session that has
			// not been provisioned has no manifest to read it from.
			_, sess := env.mkSessionFor(owner.ID, "mcp staging")
			rec = env.doAuthed(http.MethodGet, "/v1/sessions/"+sess.ID+"/tooling", nil, c)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET tooling = %d body %s", rec.Code, rec.Body.String())
			}
			var tooling struct {
				MCPUnavailable map[string]mcpstage.Omission `json:"mcp_unavailable"`
			}
			env.mustJSON(rec, &tooling)
			_, listed := tooling.MCPUnavailable["fs"]
			if listed != isolated {
				t.Fatalf("tooling mcp_unavailable = %v (isolated=%v)", tooling.MCPUnavailable, isolated)
			}
		})
	}
}

// mustMCP creates one MCP server through the public route and returns its row.
func (e *testEnv) mustMCP(t *testing.T, c *creds, body map[string]any) *contracts.MCPServer {
	t.Helper()
	rec := e.doAuthed(http.MethodPost, "/v1/mcp/servers", body, c)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create mcp %v = %d body %s", body["name"], rec.Code, rec.Body.String())
	}
	var out struct {
		Server *contracts.MCPServer `json:"server"`
	}
	e.mustJSON(rec, &out)
	return out.Server
}
