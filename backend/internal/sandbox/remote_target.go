package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/piforge/piforge/backend/internal/tenantfs"
	"github.com/piforge/piforge/backend/internal/urlguard"
)

// ErrRefusedRemoteTarget is returned when a checkout's configured remote names
// something this deployment will not dial from the control plane's network
// position (docs/DATA-FLOW-AUDIT.md G6).
var ErrRefusedRemoteTarget = errors.New("sandbox: refused remote target")

// RefuseRemoteTarget is the G6 rule: may the control plane dial this checkout's
// remote?
//
// It mirrors refuseCloneTarget, because it is the same question one step later.
// A project's `repo_url` arrives through the API; a remote arrives through the
// agent, which runs inside the sandbox and can rewrite `origin` (or add a
// pushurl, or a url.<base>.insteadOf rewrite) at any time. In host placement
// this process is the one that runs `git push`, so an unchecked value means the
// control plane dials a URL the tenant chose from the control plane's own
// network position — an SSRF and a credential-leak path.
//
// The rules, in the order they are tried:
//
//   - http(s), ssh and git:// are network transports: the host and port go
//     through the same urlguard policy as every other tenant fetch, which
//     refuses loopback, link-local, private and metadata addresses, and which
//     the operator's allow-list can widen for a legitimate internal remote.
//   - `ext::` and any other transport is refused outright. Some of them execute
//     a command on this machine, which is the class of bug this guard exists
//     for.
//   - anything else is a path git reads here: `file://`, an absolute path, or a
//     relative one (relative to the checkout, so `../../<other>/repo` reaches a
//     sibling tenant's sandbox). A path inside another account's sandbox is
//     refused; a fixture outside the sandboxes root is allowed, exactly as the
//     clone rule allows it.
//
// A nil policy keeps the local-path half and skips the network half, which is
// what a caller with no deployment policy gets; cmd/piforged always passes one.
func RefuseRemoteTarget(ctx context.Context, policy *urlguard.Policy, sandboxesRoot, userID, checkoutRoot, raw string) error {
	target := strings.TrimSpace(raw)
	if target == "" {
		return nil
	}

	if host, port, kind := networkRemote(target); kind != remoteNotNetwork {
		if kind == remoteBadURL {
			return fmt.Errorf("%w: %q is not a remote this deployment can read", ErrRefusedRemoteTarget, target)
		}
		if policy == nil {
			return nil
		}
		if _, err := policy.CheckHostPort(ctx, host, port); err != nil {
			return fmt.Errorf("%w: %v", ErrRefusedRemoteTarget, err)
		}
		return nil
	}

	if transport := refusedTransport(target); transport != "" {
		return fmt.Errorf("%w: %q uses the %q transport; this deployment dials http(s) and ssh remotes, or reads a local fixture path",
			ErrRefusedRemoteTarget, target, transport)
	}

	local := localRemotePath(target)
	if !filepath.IsAbs(local) && checkoutRoot != "" {
		local = filepath.Join(checkoutRoot, local)
	}
	return refuseCrossTenantPath(sandboxesRoot, userID, local)
}

type remoteSource int

const (
	remoteNotNetwork remoteSource = iota
	remoteNetwork
	remoteBadURL
)

// networkRemote recognises the transports git reaches over the network and
// answers with the host and port the policy should judge.
func networkRemote(raw string) (host string, port int, kind remoteSource) {
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"),
		strings.HasPrefix(lower, "ssh://"), strings.HasPrefix(lower, "git+ssh://"),
		strings.HasPrefix(lower, "git://"):
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "", 0, remoteBadURL
		}
		port = 443
		switch lower[:strings.Index(lower, "://")] {
		case "http":
			port = 80
		case "ssh", "git+ssh":
			port = 22
		case "git":
			port = 9418
		}
		if text := u.Port(); text != "" {
			n, perr := strconv.Atoi(text)
			if perr != nil {
				return "", 0, remoteBadURL
			}
			port = n
		}
		return strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), port, remoteNetwork
	}
	// The scp-like form git@host:path (and host:path) is ssh with a different
	// spelling. A Windows drive path also contains a colon, so the form only
	// counts when a user@ prefix is present.
	if host, ok := scpHost(raw); ok {
		return host, 22, remoteNetwork
	}
	return "", 0, remoteNotNetwork
}

// scpHost extracts the host of `[user@]host:path`, and reports false for
// anything else (a URL, an absolute path, a drive letter).
func scpHost(raw string) (string, bool) {
	if strings.Contains(raw, "://") {
		return "", false
	}
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return "", false
	}
	host, _, ok := strings.Cut(raw[at+1:], ":")
	if !ok || host == "" || strings.ContainsAny(host, `/\`) {
		return "", false
	}
	return strings.TrimSuffix(strings.ToLower(host), "."), true
}

// refusedTransport names a transport this deployment does not dial, or "" for
// a value that is a URL/path it may handle. `ext::sh -c …` is the one that
// matters: git-remote-ext runs the command on this machine.
func refusedTransport(raw string) string {
	if i := strings.Index(raw, "::"); i > 0 {
		return raw[:i]
	}
	if lower := strings.ToLower(raw); strings.Contains(lower, "://") {
		if i := strings.Index(lower, "://"); i > 0 {
			scheme := lower[:i]
			// file:// names a path on this machine, not a transport; the local
			// half below judges it.
			if scheme == "file" {
				return ""
			}
			return scheme
		}
	}
	return ""
}

// localRemotePath turns a file:// URL or a bare path into a filesystem path.
func localRemotePath(raw string) string {
	if strings.HasPrefix(strings.ToLower(raw), "file://") {
		if u, err := url.Parse(raw); err == nil && u.Path != "" {
			p := u.Path
			// file:///C:/repo -> /C:/repo
			if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
				p = p[1:]
			}
			if decoded, derr := url.PathUnescape(p); derr == nil {
				p = decoded
			}
			return filepath.FromSlash(p)
		}
	}
	return filepath.FromSlash(raw)
}

// refuseCrossTenantPath is the local half of the rule, shared with the clone
// path: inside the sandboxes root but not under this account's directory is
// another tenant's checkout.
func refuseCrossTenantPath(sandboxesRoot, userID, target string) error {
	if strings.TrimSpace(sandboxesRoot) == "" {
		return nil
	}
	within, err := tenantfs.Contains(sandboxesRoot, target)
	if err != nil || !within {
		return nil
	}
	own, ownErr := tenantfs.Contains(filepath.Join(sandboxesRoot, userID), target)
	if ownErr == nil && own {
		return nil
	}
	return fmt.Errorf("%w: %q is inside another tenant's sandbox", ErrRefusedRemoteTarget, target)
}
