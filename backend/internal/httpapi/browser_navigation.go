package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/urlguard"
)

// BrowserNavigationPolicy is the deployment's answer to one question (G5 of
// docs/DATA-FLOW-AUDIT.md): may the sandbox browser, which in *host* placement
// is this process's own Chrome, render this page?
//
// The panel's main use is a dev server on localhost, so refusing loopback
// outright would remove the feature. The rule instead names the loopback ports
// the platform published (the UI it serves, a dev server it started, a port a
// harness declared), refuses every other loopback port, and always refuses the
// control plane's own listener. Link-local (169.254/16, where cloud metadata
// lives), private and docker-host names stay refused by the outbound policy.
//
// In container placement Chrome lives inside the sandbox, so 127.0.0.1 is the
// sandbox's own loopback and the tenant already owns it: HostPlacement is false
// and the rule does not run.
type BrowserNavigationPolicy struct {
	// HostPlacement reports that Chrome is a child of this process.
	HostPlacement bool
	// Published is the loopback port set this deployment published for a
	// session's sandbox or dev server (from -browser-localhost-ports /
	// PIFORGE_BROWSER_LOCALHOST_PORTS).
	Published []int
	// Control is this process's own listener port(s). They are refused even
	// when listed as published: no operator typo may turn the panel into a
	// reader of the control plane.
	Control []int
}

// refuseBrowserNavigation is the G5 boundary for one navigation.
//
// It returns a typed 400 that names the reason (the control plane's own port, a
// loopback port the deployment did not publish, link-local/metadata, a private
// address), or nil when the page is allowed. A blank page is always allowed:
// it is the panel's 新标签页, not a destination.
func (s *server) refuseBrowserNavigation(ctx context.Context, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "about:blank") {
		return nil
	}
	nav := s.deps.BrowserNavigation
	if !nav.HostPlacement {
		// Chrome is in the sandbox: its loopback is the sandbox's own, and the
		// target is on the tenant's side of the boundary.
		return nil
	}
	_, err := s.outboundPolicy().CheckBrowserNavigation(ctx, raw, urlguard.Navigation{
		Published: nav.Published,
		Control:   nav.Control,
	})
	if err == nil {
		return nil
	}
	var refusal *urlguard.Refusal
	if !errors.As(err, &refusal) {
		return apiErr(contracts.ErrInternal, "browser navigation policy: %v", err)
	}
	s.log.WarnContext(ctx, "refused to navigate the sandbox browser",
		"what", "browser navigation", "host", refusal.Host, "reason", refusal.Reason)
	return withStatusDetails(http.StatusBadRequest, contracts.ErrInvalidArgument,
		"the browser may not open that page: "+refusal.Detail,
		map[string]any{"reason": refusal.Reason, "host": refusal.Host})
}
