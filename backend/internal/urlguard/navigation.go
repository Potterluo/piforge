package urlguard

import (
	"context"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// ReasonControlPort is the refusal for "that is the control plane's own
// listener": it is the one loopback port a host-placement browser must never
// render, even when an operator listed it as published.
const ReasonControlPort = "control-port"

// Navigation is what a host-placement browser is allowed to reach on loopback.
//
// The panel's main use is a dev server on localhost, so refusing loopback
// outright would remove the feature. The rule instead names the loopback ports
// the platform itself published — a dev server the deployment started, the UI
// it serves, the port a test harness declared — and refuses every other one,
// including the control plane's own listener (docs/DATA-FLOW-AUDIT.md G5).
type Navigation struct {
	// Published is the loopback port set the platform published for this
	// session's sandbox or dev server.
	Published []int
	// Control is this process's own listener ports. They are refused even when
	// they appear in Published: an overlay that lists the control port by
	// accident must not turn the panel into a reader of the control plane.
	Control []int
}

// CheckBrowserNavigation is the navigation rule for a browser this process
// launched (host placement).
//
// It is Check plus one exception: a loopback destination is allowed when its
// port is in Navigation.Published, and refused otherwise. Everything else is
// the outbound rule unchanged — public http(s) is allowed, link-local
// (169.254/16, where cloud metadata lives), private, unique-local, CGNAT and
// the "this machine" names are refused, and an operator allow entry still
// widens the set (that is the deployment's documented escape hatch for a
// legitimate internal host).
//
// Container placement must not call this: there Chrome is inside the sandbox,
// so 127.0.0.1 is the sandbox's own loopback and the tenant already owns it.
func (p *Policy) CheckBrowserNavigation(ctx context.Context, raw string, nav Navigation) (*Target, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, refuse("", ReasonParse, "%q is not a URL: %v", redact(raw), err)
	}
	if u.Opaque != "" {
		return nil, refuse("", ReasonScheme, "%q is not a hierarchical URL", redact(raw))
	}
	switch scheme := strings.ToLower(u.Scheme); scheme {
	case "http", "https":
	case "":
		return nil, refuse("", ReasonScheme, "%q has no scheme", redact(raw))
	default:
		return nil, refuse("", ReasonScheme,
			"scheme %q is refused: the browser opens http and https pages only", u.Scheme)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return nil, refuse("", ReasonNoHost, "%q has no host", redact(raw))
	}
	if u.User != nil {
		return nil, refuse(host, ReasonCredentials,
			"the URL embeds credentials for %q; a navigation URL may not carry userinfo", host)
	}
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
		return nil, refuse(host, ReasonPort, "port %q is outside 1-65535", port)
	}

	// An operator's entry is a deployment decision and wins over the classifier,
	// on the ports it names.
	if p.allowedHost(host, port) {
		if addr, aerr := netip.ParseAddr(host); aerr == nil {
			return &Target{URL: u, Host: host, Port: port, Addrs: []netip.Addr{addr.Unmap()}}, nil
		}
		addrs, lerr := p.lookupAddrs(ctx, host)
		if lerr != nil {
			addrs = nil
		}
		return &Target{URL: u, Host: host, Port: port, Addrs: addrs}, nil
	}

	// A literal address needs no DNS.
	if addr, aerr := netip.ParseAddr(host); aerr == nil {
		addr = addr.Unmap()
		if p.allowedAddr(addr, port) {
			return &Target{URL: u, Host: host, Port: port, Addrs: []netip.Addr{addr}}, nil
		}
		reason, phrase := classify(addr)
		switch reason {
		case "":
			return &Target{URL: u, Host: host, Port: port, Addrs: []netip.Addr{addr}}, nil
		case ReasonLoopback:
			return p.navigationLoopback(u, host, port, nav)
		default:
			return nil, refuse(host, reason,
				"host %q is refused: %s is %s, which is not a page a host-placement browser may render",
				host, addr.String(), phrase)
		}
	}

	// `localhost` and `*.localhost` are the panel's own spelling for a dev
	// server; they are loopback by definition, so they take the port rule
	// without a lookup.
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return p.navigationLoopback(u, host, port, nav)
	}
	if reason := p.refuseName(host); reason != "" {
		return nil, refuse(host, reason,
			"host %q is refused: it is a name that only means this machine or the docker host "+
				"(host.docker.internal / the compose service / this container), and the browser must not "+
				"render the control plane's own network position", host)
	}
	addrs, lerr := p.lookupAddrs(ctx, host)
	if lerr != nil || len(addrs) == 0 {
		return nil, refuse(host, ReasonDNS, "host %q could not be resolved", host)
	}
	for _, addr := range addrs {
		addr = addr.Unmap()
		if p.allowedAddr(addr, port) {
			continue
		}
		reason, phrase := classify(addr)
		switch reason {
		case "":
			continue
		case ReasonLoopback:
			return p.navigationLoopback(u, host, port, nav)
		default:
			return nil, refuse(host, reason,
				"host %q is refused: %s is %s, which is not a page a host-placement browser may render",
				host, addr.String(), phrase)
		}
	}
	return &Target{URL: u, Host: host, Port: port, Addrs: addrs}, nil
}

// navigationLoopback applies the published-port rule to one loopback target.
func (p *Policy) navigationLoopback(u *url.URL, host, port string, nav Navigation) (*Target, error) {
	if containsPort(nav.Control, port) {
		return nil, refuse(host, ReasonControlPort,
			"host %q is the control plane's own listener (port %s); a host-placement browser must not "+
				"render the control plane's endpoints to a tenant", host, port)
	}
	if !containsPort(nav.Published, port) {
		return nil, refuse(host, ReasonLoopback,
			"host %q is loopback and port %s is not one this deployment published for the session's "+
				"sandbox or dev server (published: %s); in host placement the browser is the control "+
				"plane's own process, so its loopback is not the tenant's to read",
			host, port, portsText(nav.Published))
	}
	return &Target{URL: u, Host: host, Port: port}, nil
}

// containsPort is the strict membership test the navigation rule needs: unlike
// portListed (where an empty list means "any port", for a programmatic allow
// entry), an empty Published or Control set means exactly none.
func containsPort(ports []int, port string) bool {
	want, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	for _, candidate := range ports {
		if candidate == want {
			return true
		}
	}
	return false
}

// portsText renders the published set for a refusal message.
func portsText(ports []int) string {
	if len(ports) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, strconv.Itoa(port))
	}
	return strings.Join(parts, ", ")
}
