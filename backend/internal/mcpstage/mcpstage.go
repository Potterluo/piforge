// Package mcpstage answers one question, in one place, for the four callers that
// need the same answer: can this MCP server be handed to the agent, and if not,
// why not?
//
// It exists because the answer was previously only in a log line. Stage 3 of
// docs/EXECUTION-BOUNDARY.md brokered HTTP MCP servers — the sandbox gets
// <gateway>/gw/mcp/<token> and the control plane injects the real credential on
// egress — but a stdio server cannot be brokered: Pi starts it *inside* the
// sandbox, and it authenticates with credentials in its own environment. There
// is no egress point where this process could add them, so under an isolated
// placement the server is not staged at all.
//
// "Not staged" used to be a Warn on the server's own log. A user who configured a
// stdio server then saw it in the MCP panel, fully enabled, and never learned
// that the agent had no such tool. Stage 4 makes the refusal part of the product:
// the MCP list says it, the session's mounted manifest records it, and the
// panel renders it (docs/EXECUTION-BOUNDARY.md §10.1 item 8).
//
// The decision is deliberately a pure function of (server, isolated): the same
// call produces the panel's sentence, the manifest's record and the launch-time
// decision, so they cannot drift apart.
package mcpstage

import (
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// CodeStdioNeedsCredentials is the machine-readable reason a stdio server is not
// staged into an isolated sandbox. The panel switches on it; the reason string
// below is what a human reads.
const CodeStdioNeedsCredentials = "stdio_needs_credentials_in_the_sandbox"

// ReasonStdio is the sentence the panel and the manifest show. It names the
// transport, what would have to be true for it to work, and the way out, because
// "unavailable" without a fix is a dead end for the user who configured it.
const ReasonStdio = "a stdio MCP server is a process Pi starts inside the sandbox, and it authenticates with " +
	"credentials in its own environment; the sandbox has no egress point where the control plane could inject " +
	"them, so staging it would copy the user's credential into the container. Use an HTTP MCP server instead: it " +
	"is proxied through /gw/mcp/<token> with a revocable per-session token and the real credential never leaves " +
	"the control plane."

// Omission is one server that will not reach the agent, with both the code a
// client can switch on and the sentence a person can act on.
//
// It is the wire shape of the tooling panel's mcp_unavailable map and of the
// same map in a checkout's mount manifest, so the live answer and the recorded
// one are the same type as well as the same value.
type Omission struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// Unavailable reports whether this server cannot be staged, and why.
//
// isolated is the agent placement: true when the agent runs inside a sandbox
// container (every docker/runner deployment since stage 4), false for
// -sandbox local, where the agent is this process's child and a stdio server is
// staged exactly as it always was.
//
// A disabled server is not "unavailable": the user turned it off, and reporting
// it as blocked would be a second, contradictory answer to the same question.
func Unavailable(server *contracts.MCPServer, isolated bool) (Omission, bool) {
	if server == nil || !server.Enabled || !isolated {
		return Omission{}, false
	}
	if strings.EqualFold(strings.TrimSpace(string(server.Transport)), string(contracts.MCPHTTP)) {
		// Brokered: the sandbox holds a URL with a revocable token, not a
		// credential. This is the transport the boundary is built for.
		return Omission{}, false
	}
	return Omission{Code: CodeStdioNeedsCredentials, Reason: ReasonStdio}, true
}

// Omissions renders Unavailable for a whole configuration, keyed by server name
// — the same key the mount manifest uses for what it did stage (MCP servers are
// named, not numbered, in Pi's config). An empty result means everything this
// user configured can be handed over.
func Omissions(servers []*contracts.MCPServer, isolated bool) map[string]Omission {
	out := map[string]Omission{}
	for _, server := range servers {
		if omission, blocked := Unavailable(server, isolated); blocked {
			out[server.Name] = omission
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
