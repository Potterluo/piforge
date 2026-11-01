package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/pimodel"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/urlguard"
)

// The MCP broker: how a sandbox gets an MCP server without getting its key.
//
// Stage 1 chose not to stage `mcp.json` at all, because that file carries
// decrypted credentials and writing it into a tenant's sandbox is exactly what
// the execution boundary exists to stop (docs/EXECUTION-BOUNDARY.md §4). The
// consequence was that a session with MCP servers simply had none under the
// flag. Stage 3 replaces the file's *contents*:
//
//	sandbox mcp.json:  {"mcpServers":{"docs":{"url":"http://control:7700/gw/mcp/<token>"}}}
//	control plane:     POST /gw/mcp/<token>  →  the user's real server, with the
//	                    decrypted credential added as headers on the way out
//
// The token is random, scoped to (session, server), stored hashed, and
// revocable — `DELETE /v1/sessions/{id}/mcp-token` stamps every live token of
// the session and the proxy refuses it on the next request. What the sandbox
// holds is therefore a credential the deployment can take away, not a copy of
// the user's.
//
// What this does not cover: a **stdio** MCP server is a process the agent starts
// itself, with its credentials in its environment. There is no egress point to
// inject anything at, so under the flag a stdio server is not staged and the
// launch says so. Pretending otherwise (staging the env into the sandbox) would
// undo stage 1 for the sake of a feature.

// MCPGatewayPrefix is where the broker listens. It is outside the cookie auth
// chain on purpose: the caller is an agent in a sandbox, and the token in the
// path is its credential.
const MCPGatewayPrefix = "/gw/mcp/"

// SessionBinder is the address half of the sandbox binding
// (docs/EXECUTION-BOUNDARY.md §16.3) for a credential that names a session
// rather than a sandbox.
//
// The broker token is minted by this control plane, lives in the sandbox's
// mcp.json and is therefore exactly as portable as a model token was: copied out,
// it answered from the host too. It has no proof half — there is nowhere in Pi's
// mcp.json to interpolate a per-request value that is not in the file itself — so
// what it gets is the strong half: it is only accepted from an address the
// session's sandbox owns.
type SessionBinder interface {
	AuthorizeSession(ctx context.Context, sessionID, remoteAddr string) error
}

// mcpBrokerTimeout bounds one brokered call's *connection* setup, not its body:
// MCP responses can stream (SSE), so the useful bound is on the dial.
const mcpBrokerTimeout = 30 * time.Second

// handleMCPGateway forwards one MCP request to the user's server.
func (s *server) handleMCPGateway(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.URL.Path, MCPGatewayPrefix)
	token, rest, _ := strings.Cut(raw, "/")
	if strings.TrimSpace(token) == "" {
		mcpUnauthorized(w, "a broker token is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), mcpBrokerTimeout)
	defer cancel()
	record, err := s.deps.Store.MCPTokenByHash(ctx, store.MCPTokenHash(token))
	if err != nil {
		// The token is never logged: it is the one secret in this request that
		// belongs to the agent, and a log line is the easiest way to leak it.
		switch {
		case errors.Is(err, store.ErrMCPTokenRevoked):
			s.log.WarnContext(r.Context(), "a revoked MCP broker token was used", "path", MCPGatewayPrefix+"<token>")
		case errors.Is(err, store.ErrNotFound):
			s.log.WarnContext(r.Context(), "an unknown MCP broker token was used", "path", MCPGatewayPrefix+"<token>")
		default:
			s.log.WarnContext(r.Context(), "could not resolve an MCP broker token", "error", err.Error())
		}
		mcpUnauthorized(w, "this broker token is not valid")
		return
	}
	// The credential is real; now the question a portable token cannot answer:
	// did this request come from the sandbox the session runs in? A refusal looks
	// like every other refusal to the caller (401, same message) and is named in
	// the log, which is where an operator needs the reason.
	if s.deps.SandboxBinding != nil {
		if err := s.deps.SandboxBinding.AuthorizeSession(ctx, record.SessionID, r.RemoteAddr); err != nil {
			s.log.WarnContext(r.Context(), "an MCP broker token was used from outside its sandbox",
				"session", record.SessionID, "remote", r.RemoteAddr, "error", err.Error())
			mcpUnauthorized(w, "this broker token is not valid")
			return
		}
	}
	server, err := s.deps.Store.MCPServerByID(ctx, record.ServerID)
	if err != nil {
		mcpUnauthorized(w, "the MCP server behind this token is gone")
		return
	}
	if !server.Enabled {
		mcpUnauthorized(w, "the MCP server behind this token is disabled")
		return
	}
	if !strings.EqualFold(server.Transport, string(contracts.MCPHTTP)) || strings.TrimSpace(server.URL) == "" {
		mcpUnauthorized(w, "this MCP server is not an HTTP server; it cannot be brokered")
		return
	}
	target, err := url.Parse(server.URL)
	if err != nil {
		s.fail(w, r, apiErr(contracts.ErrInternal, "the MCP server URL is not a URL: %v", err))
		return
	}
	if rest != "" {
		target.Path = strings.TrimRight(target.Path, "/") + "/" + rest
	}
	target.RawQuery = r.URL.RawQuery

	// The address and proof checks above say *who* may use the broker; this says
	// *what it may reach*. Without it the broker was an authenticated open
	// reverse proxy: adversarial acceptance #4 sent a sandbox request to
	// http://piforge:7700/healthz and read the control plane's own loopback, and
	// to a marker container only the control plane's network could reach
	// (REPORT.md attack 18). The check runs on every call, not only at save time,
	// so a name whose DNS answer changed is refused rather than dialled.
	if err := s.refuseOutboundURL(ctx, target.String(), "MCP server URL"); err != nil {
		s.fail(w, r, err)
		return
	}

	headers, err := s.mcpBrokerHeaders(ctx, record)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	proxy := &httputil.ReverseProxy{
		// The dial goes through the same policy: resolve, verify, dial the
		// verified address. The check above is the sentence; this is the fence
		// that survives a DNS answer changing between the two.
		Transport: s.outboundPolicy().Transport(),
		Director: func(req *http.Request) {
			req.URL = target
			req.Host = target.Host
			// The sandbox's token is not the upstream's credential: drop it before
			// the request leaves, whatever the agent sent.
			req.Header.Del("Authorization")
			req.Header.Del("X-Aio-Api-Key")
			for name, value := range headers {
				req.Header.Set(name, value)
			}
			if _, ok := req.Header["User-Agent"]; !ok {
				req.Header.Set("User-Agent", "piforge-mcp-broker")
			}
		},
		// MCP over HTTP can stream (text/event-stream); flush every write so the
		// agent sees a tool result when it is produced rather than when the
		// connection closes.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// A refusal that only shows up at dial time (the name resolved
			// somewhere private after the check above, or it stopped resolving)
			// is the same 400 as a save-time refusal, not a 502: the tenant
			// configured a target this deployment will not fetch.
			var refusal *urlguard.Refusal
			if errors.As(err, &refusal) {
				s.fail(w, r, s.urlRefusal(r.Context(), "MCP server URL", err))
				return
			}
			s.log.WarnContext(r.Context(), "the MCP broker could not reach the server",
				"server", server.Name, "error", err.Error())
			http.Error(w, "the MCP server is unreachable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// mcpBrokerHeaders turns the server's stored secret blob into headers.
//
// The rule is the UI's existing one — `KEY=value` per line, which is what a
// person pastes from a secret manager — read as an HTTP header rather than an
// environment variable. It is a small rule and it is written down: for a brokered
// HTTP server, every line of the credential blob is one header, e.g.
//
//	Authorization=Bearer sk-…
//
// CR/LF are refused rather than sanitised: a header value is not a place to be
// clever about a user's input.
func (s *server) mcpBrokerHeaders(ctx context.Context, record *store.MCPToken) (map[string]string, error) {
	secrets, err := s.deps.Store.MCPServerSecrets(ctx, record.UserID, record.ServerID)
	if err != nil {
		// A server with no stored secret is a public one; that is not an error.
		// The store reports sql.ErrNoRows for "no row", and any other failure is
		// a real problem worth surfacing.
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, apiErr(contracts.ErrInternal, "could not read the MCP credential: %v", err)
	}
	parsed := pimodel.ParseMCPSecrets(secrets)
	out := make(map[string]string, len(parsed))
	for name, value := range parsed {
		if strings.ContainsAny(name, " \t\r\n:") {
			return nil, apiErr(contracts.ErrInvalidArgument, "the MCP credential names %q, which is not a header name", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, apiErr(contracts.ErrInvalidArgument, "the MCP credential for %q contains a line break", name)
		}
		out[name] = value
	}
	return out, nil
}

// mcpUnauthorized answers a broker request that is not allowed, without saying
// anything an attacker could use to tell a revoked token from a guessed one.
func mcpUnauthorized(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error":   "unauthorized",
		"message": message,
	})
}

// handleRevokeMCPTokens revokes every live broker token of one session.
//
// This is the "revocable" half of the promise: after it, the URL in the sandbox
// is worth nothing, and the agent's next MCP call fails with 401 until a new
// token is minted (which needs a new session start, by design — a revoked
// session must not silently regain access on its next respawn).
func (s *server) handleRevokeMCPTokens(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	sess, err := s.deps.Store.Session(r.Context(), user.ID, pathID(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	n, err := s.deps.Store.RevokeMCPTokens(r.Context(), user.ID, sess.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.InfoContext(r.Context(), "revoked a session's MCP broker tokens",
		"session", sess.ID, "tokens", n)
	w.WriteHeader(http.StatusNoContent)
}
