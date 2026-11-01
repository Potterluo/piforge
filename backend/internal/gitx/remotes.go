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
// `git remote get-url origin` would answer the common case and miss the one
// that matters: `remote.<name>.pushurl` overrides `url` for a push, so a tenant
// who leaves `origin` alone and adds a pushurl points the control plane at a
// host the guard never looked at. `url.<base>.insteadOf` is the other rewrite:
// git replaces a matching prefix of the URL with <base> before it dials, so a
// base naming a private address turns an ordinary `origin` into that address.
// Both are read here.
//
// A <base> that is an opaque prefix ("gh:") names no destination and is not
// returned: there is nothing to judge. Values are returned as written; the
// caller applies the policy (docs/DATA-FLOW-AUDIT.md G6).
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
	if rewrites, rerr := r.Run(ctx, dir, "config", "--get-regexp", `^url\..*\.insteadof$`); rerr == nil {
		for _, entry := range configEntries(rewrites.Stdout) {
			if base := insteadOfBase(entry.key); base != "" && looksLikeDestination(base) {
				targets = append(targets, base)
			}
		}
	}
	return dedupeStrings(targets), nil
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

// insteadOfBase recovers <base> from a `url.<base>.insteadOf` key.
func insteadOfBase(key string) string {
	lower := strings.ToLower(key)
	if !strings.HasPrefix(lower, "url.") || !strings.HasSuffix(lower, ".insteadof") {
		return ""
	}
	// Both ends are fixed-width ASCII, so slicing the original keeps a base
	// with mixed case intact.
	return key[len("url.") : len(key)-len(".insteadof")]
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
	return false
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
