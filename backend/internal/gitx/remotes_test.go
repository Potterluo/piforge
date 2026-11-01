package gitx

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// G6 of docs/DATA-FLOW-AUDIT.md: a guard that reads only `origin` is fooled by
// the two rewrites git itself applies before it dials. `remote.<name>.pushurl`
// overrides `url` for a push, and `url.<base>.insteadOf` rewrites a URL before
// the transport sees it. Both are the tenant's to set inside the sandbox, so
// RemoteTargets has to return them; an opaque insteadOf prefix names no address
// and is not a target.
func TestRemoteTargetsReadsEveryPushRewrite(t *testing.T) {
	runner, dir := newRepo(t)
	ctx := context.Background()

	if targets, err := runner.RemoteTargets(ctx, dir); err != nil {
		t.Fatalf("RemoteTargets on a repository with no remote: %v", err)
	} else if len(targets) != 0 {
		t.Fatalf("a repository with no remote reported targets: %v", targets)
	}

	configure := func(args ...string) {
		t.Helper()
		full := append([]string{"config"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config %v: %v\n%s", args, err, out)
		}
	}
	const (
		origin    = "https://example.com/acme/repo.git"
		pushURL   = "http://127.0.0.1:7700/acme/repo.git"
		rewrite   = "/data/sandboxes/usr_other/repo"
		shorthand = "gh:"
	)
	configure("remote.origin.url", origin)
	configure("remote.origin.pushurl", pushURL)
	configure(`url.`+rewrite+`.insteadOf`, "origin")
	configure(`url.`+shorthand+`.insteadOf`, "github:")

	targets, err := runner.RemoteTargets(ctx, dir)
	if err != nil {
		t.Fatalf("RemoteTargets: %v", err)
	}
	joined := strings.Join(targets, "\n")
	for _, want := range []string{origin, pushURL, rewrite} {
		if !strings.Contains(joined, want) {
			t.Fatalf("RemoteTargets missed %q: %v", want, targets)
		}
	}
	if strings.Contains(joined, shorthand) {
		t.Fatalf("an opaque insteadOf prefix was reported as a destination: %v", targets)
	}
}

// Adversarial round 5, finding 7: `url.<base>.pushInsteadOf` is the push-only
// rewrite, and git applies it before the transport sees the URL. The guard read
// only `.insteadOf`, so a public `origin` plus one pushInsteadOf made the push
// dial loopback. Both suffixes are read now, and a `host:path` base with no user
// is a destination rather than an opaque prefix.
func TestRemoteTargetsReadsPushInsteadOf(t *testing.T) {
	runner, dir := newRepo(t)
	ctx := context.Background()

	configure := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "config"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config %v: %v\n%s", args, err, out)
		}
	}
	const (
		origin       = "https://93.184.216.34/acme/repo.git"
		pushRewrite  = "git://127.0.0.1:9912/"
		scpRewrite   = "127.0.0.1:9913/x.git"
		shorthandOut = "gh:"
	)
	configure("remote.origin.url", origin)
	configure("url."+pushRewrite+".pushInsteadOf", "https://93.184.216.34/")
	configure("url."+scpRewrite+".pushInsteadOf", "https://example.com/")
	configure("url."+shorthandOut+".pushInsteadOf", "github:")

	targets, err := runner.RemoteTargets(ctx, dir)
	if err != nil {
		t.Fatalf("RemoteTargets: %v", err)
	}
	joined := strings.Join(targets, "\n")
	for _, want := range []string{origin, pushRewrite, scpRewrite} {
		if !strings.Contains(joined, want) {
			t.Fatalf("RemoteTargets missed %q: %v", want, targets)
		}
	}
	if strings.Contains(joined, shorthandOut) {
		t.Fatalf("an opaque pushInsteadOf prefix was reported as a destination: %v", targets)
	}
}

// The other half of finding 7 and 8: a remote is not the only way repository
// configuration reaches a process. These keys name a program git runs, and the
// guard names them instead of letting command-scope config silently win — a
// tenant whose push fails should be told why.
func TestRemoteConfigIssuesNamesExecutingKeys(t *testing.T) {
	runner, dir := newRepo(t)
	ctx := context.Background()

	issues, err := runner.RemoteConfigIssues(ctx, dir)
	if err != nil {
		t.Fatalf("RemoteConfigIssues on a clean checkout: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("a clean checkout reported issues: %v", issues)
	}

	for key, value := range map[string]string{
		"core.sshCommand":     "printf x",
		"core.fsmonitor":      "printf x",
		"remote.origin.proxy": "http://127.0.0.1:9912",
		"credential.helper":   "!printf x",
	} {
		cmd := exec.Command("git", "-C", dir, "config", key, value)
		if out, cerr := cmd.CombinedOutput(); cerr != nil {
			t.Fatalf("git config %s: %v\n%s", key, cerr, out)
		}
	}

	issues, err = runner.RemoteConfigIssues(ctx, dir)
	if err != nil {
		t.Fatalf("RemoteConfigIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	for _, want := range []string{"core.sshcommand", "core.fsmonitor", "remote.origin.proxy", "credential.helper"} {
		if !strings.Contains(strings.ToLower(joined), want) {
			t.Fatalf("RemoteConfigIssues missed %q: %v", want, issues)
		}
	}
}
