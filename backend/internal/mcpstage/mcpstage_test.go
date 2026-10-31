package mcpstage

import (
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// The whole point of stage 4's stdio decision: an isolated placement refuses a
// stdio server *and says why*, in words a user can act on.
func TestStdioIsRefusedInAnIsolatedSandbox(t *testing.T) {
	server := &contracts.MCPServer{Name: "fs", Enabled: true, Transport: contracts.MCPStdio, Command: "npx"}
	omission, blocked := Unavailable(server, true)
	if !blocked {
		t.Fatal("a stdio server was staged into an isolated sandbox")
	}
	if omission.Code != CodeStdioNeedsCredentials {
		t.Fatalf("code = %q, want %q", omission.Code, CodeStdioNeedsCredentials)
	}
	// The reason has to name the transport, the fix and the credential, because
	// the panel shows this sentence verbatim instead of a generic "disabled".
	for _, want := range []string{"stdio", "HTTP MCP server", "credential"} {
		if !strings.Contains(omission.Reason, want) {
			t.Fatalf("reason %q does not mention %q", omission.Reason, want)
		}
	}
}

// An HTTP server is the transport the broker was built for: staging it is the
// normal case, in both placements.
func TestHTTPServersAreNeverRefused(t *testing.T) {
	server := &contracts.MCPServer{Name: "docs", Enabled: true, Transport: contracts.MCPHTTP, URL: "https://example.test/mcp"}
	for _, isolated := range []bool{true, false} {
		if _, blocked := Unavailable(server, isolated); blocked {
			t.Fatalf("an HTTP server was refused with isolated=%v", isolated)
		}
	}
	// The transport is compared case-insensitively: a row written by a client
	// that upper-cased it is the same transport, and treating it as stdio would
	// silently drop a server that works.
	shouty := &contracts.MCPServer{Name: "docs", Enabled: true, Transport: contracts.MCPTransport("HTTP")}
	if _, blocked := Unavailable(shouty, true); blocked {
		t.Fatal("an upper-cased http transport was treated as stdio")
	}
}

// -sandbox local has no container to protect: the agent is this process's child,
// so a stdio server is staged exactly as it always was.
func TestLocalPlacementStagesStdio(t *testing.T) {
	server := &contracts.MCPServer{Name: "fs", Enabled: true, Transport: contracts.MCPStdio, Command: "npx"}
	if _, blocked := Unavailable(server, false); blocked {
		t.Fatal("a stdio server was refused for the local placement")
	}
}

// A server the user turned off is not "unavailable": reporting it as blocked
// would be a second, contradictory answer to the same question.
func TestDisabledServersAreNotReportedAsBlocked(t *testing.T) {
	server := &contracts.MCPServer{Name: "fs", Enabled: false, Transport: contracts.MCPStdio, Command: "npx"}
	if _, blocked := Unavailable(server, true); blocked {
		t.Fatal("a disabled server was reported as blocked by the placement")
	}
}

// Omissions is what the panel and the manifest are built from; it must key by
// name (the manifest's key) and stay nil when nothing is blocked, so the JSON
// omits the field instead of carrying an empty object.
func TestOmissionsKeysByNameAndOmitsTheEmptyCase(t *testing.T) {
	servers := []*contracts.MCPServer{
		{Name: "fs", Enabled: true, Transport: contracts.MCPStdio, Command: "npx"},
		{Name: "docs", Enabled: true, Transport: contracts.MCPHTTP, URL: "https://example.test/mcp"},
		{Name: "off", Enabled: false, Transport: contracts.MCPStdio, Command: "npx"},
	}
	got := Omissions(servers, true)
	if len(got) != 1 {
		t.Fatalf("omissions = %v, want exactly the stdio server", got)
	}
	if _, ok := got["fs"]; !ok {
		t.Fatalf("omissions = %v, want the stdio server keyed by name", got)
	}
	if none := Omissions(servers, false); none != nil {
		t.Fatalf("omissions for the local placement = %v, want nil", none)
	}
}
