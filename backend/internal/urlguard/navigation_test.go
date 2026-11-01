package urlguard

import (
	"context"
	"errors"
	"testing"
)

// G5 of docs/DATA-FLOW-AUDIT.md: in host placement the panel's Chrome is the
// control plane's own browser, so "which page may it render" is a question
// about this process's network position. The rule: a loopback page is allowed
// only on a port the platform published (a dev server, the UI it serves), the
// control plane's own listener is never allowed, and everything else the
// outbound policy already refuses (link-local/metadata, private, docker-host
// names) stays refused.
func TestCheckBrowserNavigation(t *testing.T) {
	ctx := context.Background()
	published := Navigation{Published: []int{3000, 3100}, Control: []int{8080}}

	allowed := []struct {
		name string
		url  string
		nav  Navigation
	}{
		{"published loopback port", "http://127.0.0.1:3000/", published},
		{"published localhost port", "http://localhost:3100/login", published},
		{"published ipv6 loopback port", "http://[::1]:3000/", published},
		{"public address is not this process's authority", "https://93.184.216.34/", published},
		{"an allow entry widens the rule for internal hosts",
			"http://dev.internal.example:3000/", Navigation{Published: []int{3000}}},
	}
	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			var policy *Policy
			if tc.url == "http://dev.internal.example:3000/" {
				var err error
				policy, err = New([]Allow{{Host: "dev.internal.example", Ports: []int{3000}}})
				if err != nil {
					t.Fatalf("New: %v", err)
				}
			} else {
				policy = Default()
			}
			if _, err := policy.CheckBrowserNavigation(ctx, tc.url, tc.nav); err != nil {
				t.Fatalf("CheckBrowserNavigation(%q) = %v, want allowed", tc.url, err)
			}
		})
	}

	refused := []struct {
		name   string
		url    string
		nav    Navigation
		reason string
	}{
		{"the control plane's own port", "http://127.0.0.1:8080/v1/admin/audit", published, ReasonControlPort},
		{"the control port is refused even when published",
			"http://127.0.0.1:8080/", Navigation{Published: []int{8080}, Control: []int{8080}}, ReasonControlPort},
		{"an unpublished loopback port", "http://127.0.0.1:3001/", published, ReasonLoopback},
		{"an unpublished localhost port", "http://localhost:3001/", published, ReasonLoopback},
		{"loopback with nothing published", "http://127.0.0.1:3000/", Navigation{}, ReasonLoopback},
		{"link-local metadata", "http://169.254.169.254/latest/meta-data/", published, ReasonLinkLocal},
		{"private address", "http://10.1.2.3:3000/", published, ReasonPrivate},
		{"the docker host name", "http://host.docker.internal:3000/", published, ReasonName},
		{"a file url", "file:///etc/passwd", published, ReasonScheme},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Default().CheckBrowserNavigation(ctx, tc.url, tc.nav)
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("CheckBrowserNavigation(%q) = %v, want a refusal", tc.url, err)
			}
			if refusal.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q (%s)", refusal.Reason, tc.reason, refusal.Detail)
			}
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("a refusal must match ErrRefused: %v", err)
			}
		})
	}
}
