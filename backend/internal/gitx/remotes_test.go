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
