package urlguard

import (
	"context"
	"strconv"
	"strings"
)

// CheckHostPort is Check for a transport that is not HTTP.
//
// A git remote can be reached over ssh (port 22, or whatever the URL names) or
// git:// (9418), and those are dialled from the same network position — and so
// borrow the same authority — as an https fetch. urlguard speaks URLs because
// that is what its callers used to hand it; this is the same question asked
// with a host and a port, so the name rules, the address classes and the
// operator's allow-list apply unchanged. It is what docs/DATA-FLOW-AUDIT.md G6
// uses to judge a checkout's `origin` before `git push` dials it.
//
// The returned Target has no URL: the caller already has one, and this function
// did not parse it.
func (p *Policy) CheckHostPort(ctx context.Context, host string, port int) (*Target, error) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return nil, refuse("", ReasonNoHost, "the remote names no host")
	}
	if port < 1 || port > 65535 {
		return nil, refuse(host, ReasonPort, "port %d is outside 1-65535", port)
	}
	portText := strconv.Itoa(port)
	addrs, err := p.verify(ctx, host, portText)
	if err != nil {
		return nil, err
	}
	return &Target{Host: host, Port: portText, Addrs: addrs}, nil
}
