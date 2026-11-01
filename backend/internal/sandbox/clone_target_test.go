package sandbox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/piforge/piforge/backend/internal/urlguard"
)

// The local provider runs `git clone` on this machine, so a project's repo_url
// is a URL this process dials from its own network position — the same thing the
// MCP broker and the provider probe do, and the same rule has to apply. Every
// other fetch site refuses a loopback / link-local / private target; this one
// used to dial whatever the tenant stored.
//
// The container providers clone *inside* the sandbox, so there the value never
// gets the control plane's network position and this check is not what protects
// it — but the local placement is a real deployment shape (-sandbox local), and
// it is the one where the dial is ours.
func TestLocalRefusesACloneTargetThePolicyRefuses(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	provider := newLocal(t)
	provider.cfg.URLPolicy = urlguard.Default()
	ctx := context.Background()

	if _, err := provider.Ensure(ctx, "usr_a", "prj_a", upstream.URL+"/acme/repo.git", "main"); err == nil {
		t.Fatal("Ensure cloned a loopback repository")
	} else if !strings.Contains(err.Error(), "refus") {
		t.Fatalf("the failure does not name the refusal: %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the refused clone still dialled the target %d time(s)", n)
	}

	// The scp form is the same host with a different transport (ssh): the policy
	// is asked about the name, not the scheme.
	if _, err := provider.Ensure(ctx, "usr_a", "prj_a", "git@127.0.0.1:acme/repo.git", "main"); err == nil {
		t.Fatal("Ensure cloned from a loopback ssh address")
	}
}

// A local path is not a URL, but it is still a name a tenant wrote, and the
// control plane hands it to git. Another account's sandbox is the case that
// matters: cloning it would copy that account's checkout into this one, which
// the files API then serves.
func TestLocalRefusesAnotherTenantsCheckout(t *testing.T) {
	provider := newLocal(t)
	ctx := context.Background()
	provider.cfg.URLPolicy = urlguard.Default()

	// A real repository where another tenant's checkout would live.
	victim := filepath.Join(provider.Root(), "usr_victim", "prj_victim", "repo")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatalf("mkdir victim: %v", err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = victim
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", ".")
	git("-c", "user.email=v@example.com", "-c", "user.name=v", "commit", "-q", "--allow-empty", "-m", "secret")

	if _, err := provider.Ensure(ctx, "usr_attacker", "prj_attacker", victim, "main"); err == nil {
		t.Fatal("Ensure cloned another tenant's checkout")
	} else if !strings.Contains(err.Error(), "another tenant") {
		t.Fatalf("the refusal does not say what it refused: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(provider.Root(), "usr_attacker", "prj_attacker", "repo", ".git")); statErr == nil {
		// The clone directory is created before the clone; what must not exist is
		// a populated checkout.
		if _, headErr := os.Stat(filepath.Join(provider.Root(), "usr_attacker", "prj_attacker", "repo", "HEAD")); headErr == nil {
			t.Fatal("the refused clone left a populated checkout behind")
		}
	}

	// A repository outside the sandbox root is a development fixture (the e2e
	// suite drives push/pull with a bare repo in its own directory): not a
	// tenant's sandbox, and still allowed.
	outside := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", "-q", filepath.Join(outside, "origin.git"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	seed := filepath.Join(outside, "seed")
	for _, args := range [][]string{
		{"clone", "-q", filepath.Join(outside, "origin.git"), seed},
		{"-C", seed, "-c", "user.email=e@example.com", "-c", "user.name=e", "commit", "-q", "--allow-empty", "-m", "seed"},
		{"-C", seed, "push", "-q", "origin", "HEAD:main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if _, err := provider.Ensure(ctx, "usr_dev", "prj_dev", filepath.Join(outside, "origin.git"), "main"); err != nil {
		t.Fatalf("a repository outside the sandbox root was refused: %v", err)
	}
}
