package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/gitx"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/urlguard"
)

// G6 of docs/DATA-FLOW-AUDIT.md: push, pull and fetch dial the checkout's
// `origin`, and `origin` is repository configuration the agent can rewrite
// inside the sandbox (`git remote set-url origin …`, a `pushurl`, a
// `url.<base>.insteadOf` rewrite). In host placement the control plane runs the
// push, so an unchecked value means this process dials a URL the tenant chose —
// from its own network position, with its own credentials.
//
// The test wires the real guard (gitx reads the remotes, sandbox applies the
// urlguard + cross-tenant rule) and proves two things per shape: the route
// answers a typed 400, and the workspace was never called, which is what "0
// dials" means at this layer. A normal origin still reaches the push.

// initRepoAt makes dir a repository with one commit.
func initRepoAt(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	runGitIn(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	runGitIn(t, dir, "add", "-A")
	runGitIn(t, dir, "commit", "-m", "seed")
}

// attachWorktreeAt gives a session a sandbox + worktree row pointing at a
// directory the test owns (withWorktree uses a /srv path, which no git can
// read).
func attachWorktreeAt(t *testing.T, env *testEnv, userID, projectID, sessionID, path string) {
	t.Helper()
	ctx := context.Background()
	sb, err := env.store.CreateSandbox(ctx, userID, store.NewSandbox{ProjectID: projectID})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	wt, err := env.store.CreateWorktree(ctx, userID, store.NewWorktree{
		SandboxID: sb.ID, Path: path, Branch: "main",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := env.store.UpdateSession(ctx, userID, sessionID,
		store.SessionPatch{WorktreeID: &wt.ID}); err != nil {
		t.Fatalf("attach worktree: %v", err)
	}
}

func TestGitRemoteGuardRefusesARewrittenOrigin(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "g6")

	// The deployment's sandboxes root: this account's checkout and another
	// account's, which is what a relative/absolute path can reach.
	sandboxes := t.TempDir()
	checkout := filepath.Join(sandboxes, user.ID, "repo")
	otherTenant := filepath.Join(sandboxes, "usr_other", "repo")
	initRepoAt(t, checkout)
	initRepoAt(t, otherTenant)
	attachWorktreeAt(t, env, user.ID, project.ID, sess.ID, checkout)

	// The guard exactly as cmd/piforged wires it for a host workspace.
	runner := gitx.NewRunner()
	guard := func(ctx context.Context, userID, root string) error {
		targets, err := runner.RemoteTargets(ctx, root)
		if err != nil {
			return err
		}
		for _, target := range targets {
			if rerr := sandbox.RefuseRemoteTarget(ctx, urlguard.Default(), sandboxes, userID, root, target); rerr != nil {
				return rerr
			}
		}
		return nil
	}
	env.deps.RemoteCheck = guard
	env.rebuild()

	base := "/v1/sessions/" + sess.ID
	setOrigin := func(url string) {
		t.Helper()
		// `git config` sets it whether or not origin exists, which is what an
		// agent rewriting a remote does inside the sandbox.
		runGitIn(t, checkout, "config", "remote.origin.url", url)
	}

	malicious := []struct {
		name   string
		origin string
	}{
		{"loopback control port", "http://127.0.0.1:7700/v1/admin/audit"},
		{"link-local metadata", "http://169.254.169.254/latest/meta-data/"},
		{"private address", "http://10.1.2.3:8080/acme/repo.git"},
		{"another tenant's checkout", otherTenant},
		{"scp-like ssh to loopback", "git@127.0.0.1:acme/repo.git"},
		{"ssh url to loopback", "ssh://git@127.0.0.1:2222/acme/repo.git"},
		{"ext transport executes a command", "ext::sh -c touch% /tmp/pwn"},
	}

	for _, tc := range malicious {
		t.Run(tc.name, func(t *testing.T) {
			setOrigin(tc.origin)
			pushes, pulls, fetches := env.workspace.remoteCalls()

			for _, call := range []struct {
				method string
				path   string
				body   any
			}{
				{http.MethodPost, base + "/git/push", nil},
				{http.MethodPost, base + "/git/pull", nil},
				// The Git tab's ahead/behind fetch is the same dial.
				{http.MethodGet, base + "/git/remote", nil},
				// A PR pushes before it opens anything.
				{http.MethodPost, base + "/git/pr", map[string]any{"title": "x"}},
			} {
				rec := env.doAuthed(call.method, call.path, call.body, c)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("%s %s with origin %q = %d, want 400: %s",
						call.method, call.path, tc.origin, rec.Code, rec.Body.String())
				}
				env.mustErr(rec, http.StatusBadRequest, contracts.ErrInvalidArgument)
				if !strings.Contains(rec.Body.String(), "refused remote target") &&
					!strings.Contains(rec.Body.String(), "another tenant") {
					t.Fatalf("the refusal does not name the rule: %s", rec.Body.String())
				}
			}

			afterPushes, afterPulls, afterFetches := env.workspace.remoteCalls()
			if afterPushes != pushes || afterPulls != pulls || afterFetches != fetches {
				t.Fatalf("the guard refused but git was still asked to dial: pushes %d->%d pulls %d->%d fetches %d->%d",
					pushes, afterPushes, pulls, afterPulls, fetches, afterFetches)
			}
		})
	}

	// A legitimate origin still pushes: a fixture remote outside the sandboxes
	// root (a bare repository the deployment owns, the shape a self-hosted
	// install uses) and a public https remote. The guard's job is to refuse the
	// tenant's private targets, not to break git.
	fixture := t.TempDir()
	runGitIn(t, fixture, "init", "--bare")
	for _, origin := range []string{fixture, "https://93.184.216.34/acme/repo.git"} {
		setOrigin(origin)
		pushes, _, _ := env.workspace.remoteCalls()
		rec := env.doAuthed(http.MethodPost, base+"/git/push", nil, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("push to a legitimate origin %q = %d, want 200: %s", origin, rec.Code, rec.Body.String())
		}
		if after, _, _ := env.workspace.remoteCalls(); after != pushes+1 {
			t.Fatalf("push to %q did not reach the workspace: %d -> %d", origin, pushes, after)
		}
	}
}

// A remote the operator wrote down stays reachable: the allow-list is the same
// mechanism the deployment uses for an internal MCP server, and it is the
// documented way to keep a legitimate internal remote working.
func TestGitRemoteGuardHonoursTheOperatorAllowList(t *testing.T) {
	env := newTestEnv(t)
	user := env.mkUser("dev@piforge.local", "pw", contracts.RoleUser)
	c := env.login("dev@piforge.local", "pw")
	project, sess := env.mkSessionFor(user.ID, "g6-allow")

	sandboxes := t.TempDir()
	checkout := filepath.Join(sandboxes, user.ID, "repo")
	initRepoAt(t, checkout)
	attachWorktreeAt(t, env, user.ID, project.ID, sess.ID, checkout)
	// A git server on the operator's internal network, named in the allow-list
	// with the port it listens on.
	runGitIn(t, checkout, "config", "remote.origin.url", "http://git.internal.example:9418/acme/repo.git")

	policy, err := urlguard.New([]urlguard.Allow{{Host: "git.internal.example", Ports: []int{9418}}})
	if err != nil {
		t.Fatalf("urlguard.New: %v", err)
	}
	runner := gitx.NewRunner()
	env.deps.RemoteCheck = func(ctx context.Context, userID, root string) error {
		targets, terr := runner.RemoteTargets(ctx, root)
		if terr != nil {
			return terr
		}
		for _, target := range targets {
			if rerr := sandbox.RefuseRemoteTarget(ctx, policy, sandboxes, userID, root, target); rerr != nil {
				return rerr
			}
		}
		return nil
	}
	env.rebuild()

	pushes, _, _ := env.workspace.remoteCalls()
	rec := env.doAuthed(http.MethodPost, "/v1/sessions/"+sess.ID+"/git/push", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("push to an allow-listed remote = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if after, _, _ := env.workspace.remoteCalls(); after != pushes+1 {
		t.Fatalf("the allow-listed push did not reach the workspace")
	}
}
