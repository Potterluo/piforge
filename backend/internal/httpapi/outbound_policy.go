package httpapi

// The outbound URL policy, in the HTTP surface.
//
// Adversarial acceptance #4 measured two fetchers on the control plane's own
// network position (e2e/.artifacts/adversarial4/REPORT.md attacks 18 and 21):
// the MCP broker reverse-proxied a sandbox request to whatever URL the tenant's
// server row named, and the provider probe POSTed to whatever `base_url` a
// tenant saved. `internal/urlguard` is the rule; this file is where the rule
// becomes a 400 on each of those paths, with one log line per refusal, and
// where the same rule is handed to the transports that do the dialling.
//
// Every fetch site that uses a tenant-supplied URL must call one of these:
//
//	MCP server URL          POST /v1/mcp/servers (save) + /gw/mcp/<token> (call)
//	provider base_url       POST/PATCH /v1/providers, /v1/admin/providers (save)
//	                        POST /v1/providers/{id}/test (call)
//	                        /gw/llm/<token>/... (the real model call, in llmgw)
//	forge api_base          POST /v1/projects/{id}/forge (save) + the PR calls
//	skill import URL        POST /v1/skills/import when the URL is http(s)
//
// A path that fetches a tenant URL and does not appear above is a bug: the
// audit table in docs/SANDBOX-DEPLOY.md §1.9 is the list this one mirrors.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/urlguard"
)

// outboundPolicy is the policy this server enforces. Deps.URLPolicy is nil in
// hand-built test servers and unset deployments, and the default is the
// deployment rule itself (public http(s) only), never "no policy".
func (s *server) outboundPolicy() *urlguard.Policy {
	if s.deps.URLPolicy != nil {
		return s.deps.URLPolicy
	}
	return urlguard.Default()
}

// refuseOutboundURL is the request-time question: may this URL be dialled now?
// It returns a typed 400 naming the reason and the host, and logs the refusal
// once (the caller reports it through the normal error envelope).
func (s *server) refuseOutboundURL(ctx context.Context, raw, what string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if _, err := s.outboundPolicy().Check(ctx, raw); err != nil {
		return s.urlRefusal(ctx, what, err)
	}
	return nil
}

// saveOutboundURL is the save-time question: it refuses what the policy can
// decide without a live network (scheme, credentials, localhost / docker /
// compose names, and any address that resolves now), and accepts a name this
// process cannot resolve yet — the call-time check is the boundary.
func (s *server) saveOutboundURL(ctx context.Context, raw, what string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if _, err := s.outboundPolicy().CheckSave(ctx, raw); err != nil {
		return s.urlRefusal(ctx, what, err)
	}
	return nil
}

// urlRefusal turns a policy error into the wire refusal: 400, the reason code,
// the host, and a sentence that names both.
func (s *server) urlRefusal(ctx context.Context, what string, err error) error {
	var refusal *urlguard.Refusal
	if !errors.As(err, &refusal) {
		return apiErr(contracts.ErrInternal, "url policy: %v", err)
	}
	// One line per refusal. The URL itself is not logged: it can carry a query
	// a tenant pasted a token into, and the host plus the reason is what an
	// operator acts on.
	s.log.WarnContext(ctx, "refused to fetch a tenant-supplied URL",
		"what", what, "host", refusal.Host, "reason", refusal.Reason)
	return withStatusDetails(http.StatusBadRequest, contracts.ErrInvalidArgument,
		fmt.Sprintf("the %s is refused by the outbound URL policy: %s", what, refusal.Detail),
		map[string]any{"reason": refusal.Reason, "host": refusal.Host})
}

// outboundHTTPClient is a client whose every dial goes through the policy and
// whose redirects are re-validated hop by hop.
func (s *server) outboundHTTPClient(timeout time.Duration) *http.Client {
	return s.outboundPolicy().Client(timeout)
}

