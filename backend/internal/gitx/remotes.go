package gitx

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// RemoteTargets returns every address this checkout's configuration would dial
// for a push, pull or fetch.
//
// `git remote get-url origin` would answer the common case and miss the ones
// that matter:
//
//   - `remote.<name>.pushurl` overrides `url` for a push;
//   - `url.<base>.insteadOf` rewrites a URL before the transport sees it;
//   - `url.<base>.pushInsteadOf` is the same rewrite for a push only — the one
//     adversarial round 5 (finding 7) used to turn a public `origin` into
//     `git://127.0.0.1:9912/` while the guard saw only the public URL.
//
// All three are read here. A <base> that is an opaque prefix ("gh:") names no
// destination and is not returned: there is nothing to judge. Values are
// returned as written; the caller applies the policy (docs/DATA-FLOW-AUDIT.md
// G6).
//
// Scopes: every scope is read, because git applies every scope when it dials.
// The cost is that an operator's own global rewrite is judged too, and an
// allow-list entry is the documented way to keep an internal mirror reachable.
func (r *Runner) RemoteTargets(ctx context.Context, root string) ([]string, error) {
	dir, err := repoPath(root)
	if err != nil {
		return nil, err
	}

	var targets []string
	// exit code 1 from `git config --get-regexp` means "no match", which is a
	// checkout with no remote — an answer, not a failure.
	res, err := r.Run(ctx, dir, "config", "--get-regexp", `^remote\..*\.(url|pushurl)$`)
	if err != nil {
		if ExitCode(err) != 1 {
			return nil, fmt.Errorf("gitx: read remotes %s: %w", dir, err)
		}
	} else {
		for _, entry := range configEntries(res.Stdout) {
			targets = append(targets, entry.value)
		}
	}

	// `url.<base>.insteadOf <prefix>` keeps the destination in the *key*: the
	// value is the prefix git rewrites, and <base> is what it dials instead.
	// pushInsteadOf is the same shape with a narrower trigger, so both suffixes
	// are read in one call.
	if rewrites, rerr := r.Run(ctx, dir, "config", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`); rerr == nil {
		for _, entry := range configEntries(rewrites.Stdout) {
			if base := insteadOfBase(entry.key); base != "" && looksLikeDestination(base) {
				targets = append(targets, base)
			}
		}
	}
	return dedupeStrings(targets), nil
}

// RemoteConfigIssues returns the repository's own configuration keys that make
// git run a program the control plane did not choose.
//
// RemoteTargets answers "where would git dial?". This answers the other half of
// the same question — "what would git execute?" — because a remote is not the
// only way repository configuration reaches a process: `core.sshCommand` ran a
// command through `POST /git/push` and `core.fsmonitor` through
// `GET /git/status` (adversarial round 5, findings 8 and 11). Every key here is
// now also overridden by harden.go, so this is the belt to that braces: a
// tenant who plants one gets a refusal that names the key instead of a silent
// override, and a key this file does not know about is still outranked by `-c`.
//
// Only the local and worktree scopes are read: those are the ones the agent can
// write. The deployment's own `~/.gitconfig` is not the tenant's doing and
// refusing it would break every push on a machine whose operator configured a
// credential helper.
func (r *Runner) RemoteConfigIssues(ctx context.Context, root string) ([]string, error) {
	dir, err := repoPath(root)
	if err != nil {
		return nil, err
	}
	var issues []string
	seen := map[string]bool{}
	for _, scope := range []string{"--local", "--worktree"} {
		res, rerr := r.Run(ctx, dir, "config", scope, "--get-regexp", `.*`)
		if rerr != nil {
			// --worktree fails when extensions.worktreeConfig is unset, and 1
			// means "no match": both are "nothing there", not a broken checkout.
			continue
		}
		for _, entry := range configEntries(res.Stdout) {
			if reason, bad := configKeyExecutes(entry.key); bad && !seen[entry.key] {
				seen[entry.key] = true
				issues = append(issues, fmt.Sprintf("%s (%s)", entry.key, reason))
			}
		}
	}
	return issues, nil
}

// configKeyExecutes names the configuration keys git turns into a process.
//
// The list is by *effect*, not by transport: a pager, an editor, a credential
// helper, a proxy, a textconv filter and a merge driver are all "run this
// program", and none of them is something a repository needs in order to be
// read.
func configKeyExecutes(key string) (string, bool) {
	lower := strings.ToLower(key)
	switch lower {
	case "core.fsmonitor", "core.hookspath", "core.sshcommand", "core.gitproxy",
		"core.pager", "core.askpass", "core.editor", "core.alternateRefsCommand",
		"credential.helper", "gpg.program", "sequence.editor",
		"protocol.ext.allow", "protocol.allow":
		return "git runs the program this key names", true
	}
	if strings.HasPrefix(lower, "include.") || strings.HasPrefix(lower, "includeif.") {
		return "it loads another file into this configuration", true
	}
	// The per-remote and per-driver forms: section.<name>.<key>.
	if strings.HasPrefix(lower, "remote.") {
		for _, suffix := range []string{".proxy", ".uploadpack", ".receivepack"} {
			if strings.HasSuffix(lower, suffix) {
				return "git runs the program this key names", true
			}
		}
	}
	if strings.HasPrefix(lower, "diff.") {
		for _, suffix := range []string{".textconv", ".command"} {
			if strings.HasSuffix(lower, suffix) {
				return "git runs the program this key names", true
			}
		}
	}
	if strings.HasPrefix(lower, "filter.") {
		for _, suffix := range []string{".clean", ".smudge", ".process"} {
			if strings.HasSuffix(lower, suffix) {
				return "git runs the program this key names", true
			}
		}
	}
	if strings.HasPrefix(lower, "merge.") && strings.HasSuffix(lower, ".driver") {
		return "git runs the program this key names", true
	}
	return "", false
}

// configEntry is one "key value" line of `git config --get-regexp` output.
type configEntry struct {
	key   string
	value string
}

// configEntries parses the output. The value is kept whole so a URL containing
// a space survives.
func configEntries(out string) []configEntry {
	var entries []configEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		entries = append(entries, configEntry{key: key, value: value})
	}
	return entries
}

// insteadOfBase recovers <base> from a `url.<base>.insteadOf` (or
// `.pushInsteadOf`) key.
func insteadOfBase(key string) string {
	lower := strings.ToLower(key)
	if !strings.HasPrefix(lower, "url.") {
		return ""
	}
	for _, suffix := range []string{".insteadof", ".pushinsteadof"} {
		if strings.HasSuffix(lower, suffix) {
			// Both ends are fixed-width ASCII, so slicing the original keeps a
			// base with mixed case intact.
			return key[len("url.") : len(key)-len(suffix)]
		}
	}
	return ""
}

// looksLikeDestination separates a rewrite base that names an address from one
// that is only a shorthand trigger.
func looksLikeDestination(value string) bool {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "://") || strings.Contains(value, "::") {
		return true
	}
	if strings.HasPrefix(value, "git@") || strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, `\`) || filepath.IsAbs(value) {
		return true
	}
	// `[user@]host:path` with no scheme names a destination too; anything else
	// (a bare word, "gh:") is a shorthand prefix and names nothing.
	if _, _, ok := splitSCPLikeKey(value); ok {
		return true
	}
	return false
}

// splitSCPLikeKey is the local copy of the scp-form rule: the part before the
// first colon is a host when it contains no slash and is not a drive letter.
func splitSCPLikeKey(value string) (host, path string, ok bool) {
	if value == "" || strings.Contains(value, "://") {
		return "", "", false
	}
	colon := strings.IndexByte(value, ':')
	if colon <= 0 {
		return "", "", false
	}
	before := value[:colon]
	if strings.ContainsAny(before, `/\`) {
		return "", "", false
	}
	if len(before) == 1 {
		c := before[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return "", "", false
		}
	}
	hostPart := before
	if at := strings.LastIndex(before, "@"); at >= 0 {
		if at == 0 || at == len(before)-1 {
			return "", "", false
		}
		hostPart = before[at+1:]
	}
	rest := value[colon+1:]
	if hostPart == "" || strings.ContainsAny(hostPart, `@\`) || strings.TrimSpace(rest) == "" {
		return "", "", false
	}
	return hostPart, rest, true
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := values[:0]
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
