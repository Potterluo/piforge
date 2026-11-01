package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/urlguard"
)

// G5 of docs/DATA-FLOW-AUDIT.md, at the route: in host placement the sandbox
// browser is this process's Chrome, so navigating it to a loopback page renders
// the control plane's own services to the tenant. The rule allows a loopback
// page only on a port the deployment published (a dev server, the UI it
// serves), refuses the control plane's own listener and link-local/metadata,
// and refuses every other loopback port. A container placement runs no rule:
// there 127.0.0.1 is the sandbox's own loopback.
//
// The handler is exercised with no Chrome configured, so "allowed" is the 422
// from the missing browser and "refused" is the 400 from the rule — the same
// distinction the blank-page test uses.
func TestBrowserNavigationIsRestrictedInHostPlacement(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "browser-nav")
	env.withWorktree(user.ID, project.ID, sess.ID)
	env.deps.BrowserNavigation = BrowserNavigationPolicy{
		HostPlacement: true,
		Published:     []int{3000, 3100},
		Control:       []int{8080},
	}
	// The deployment default policy, not the test server's loopback allowance:
	// this test is about the rule a deployment actually runs.
	env.deps.URLPolicy = urlguard.Default()
	env.rebuild()

	base := "/v1/sessions/" + sess.ID + "/browser/tabs"
	open := func(url string) (int, string) {
		t.Helper()
		rec := env.doAuthed(http.MethodPost, base, map[string]any{"url": url}, c)
		return rec.Code, rec.Body.String()
	}

	// Allowed in host placement: the published dev-server ports, a blank page,
	// and a public page (public is not this process's authority to read).
	for _, url := range []string{
		"http://127.0.0.1:3000/",
		"http://localhost:3100/login",
		"about:blank",
		"",
		"https://93.184.216.34/",
	} {
		if code, body := open(url); code != http.StatusUnprocessableEntity {
			t.Fatalf("open %q = %d, want 422 (URL accepted, no Chrome): %s", url, code, body)
		}
	}

	// Refused: the control plane's own port (even though 8080 is the control
	// port and not in Published), an unpublished loopback port, metadata and a
	// private address.
	refused := []struct {
		url    string
		reason string
	}{
		{"http://127.0.0.1:8080/v1/admin/audit", "control-port"},
		{"http://localhost:8080/v1/admin/audit", "control-port"},
		{"http://127.0.0.1:3001/", "loopback"},
		{"http://localhost:9999/", "loopback"},
		{"http://169.254.169.254/latest/meta-data/", "link-local"},
		{"http://10.1.2.3:3000/", "private"},
	}
	for _, tc := range refused {
		code, body := open(tc.url)
		if code != http.StatusBadRequest {
			t.Fatalf("open %q = %d, want 400: %s", tc.url, code, body)
		}
		if !strings.Contains(body, tc.reason) {
			t.Fatalf("the refusal for %q does not name reason %q: %s", tc.url, tc.reason, body)
		}
	}

	// Container placement: Chrome is inside the sandbox, so the same loopback
	// port is the sandbox's own dev server and must keep working.
	env.deps.BrowserNavigation = BrowserNavigationPolicy{HostPlacement: false}
	env.rebuild()
	for _, url := range []string{"http://127.0.0.1:3001/", "http://localhost:9999/"} {
		if code, body := open(url); code != http.StatusUnprocessableEntity {
			t.Fatalf("container placement refused %q with %d: %s", url, code, body)
		}
	}
}
