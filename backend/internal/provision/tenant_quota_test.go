package provision

import (
	"context"
	"errors"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/tenantlimits"
)

// The per-tenant sandbox cap is enforced where sandboxes are created, and the
// refusal is typed: a client has to be able to tell "your quota is full" from
// "the sandbox failed to start", because only one of those is the user's to fix.
func TestTenantSandboxLimitRefusesTheNextSandbox(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.svc = New(Config{
		Store:     e.store,
		Sandbox:   e.svc.cfg.Sandbox,
		Git:       e.svc.cfg.Git,
		SkillsDir: e.svc.cfg.SkillsDir,
		LimitsFor: func(_ context.Context, userID string) tenantlimits.Limits {
			return tenantlimits.Limits{CPUs: "2", Memory: "4g", Pids: 512, MaxSandboxes: 1}
		},
	})

	first := e.projectNamed("one")
	sess := e.session(first, contracts.KindMain, "", "")
	if _, err := e.svc.EnsureWorktree(ctx, e.user.ID, first, sess); err != nil {
		t.Fatalf("the first sandbox was refused: %v", err)
	}

	second := e.projectNamed("two")
	other := e.session(second, contracts.KindMain, "", "")
	_, err := e.svc.EnsureWorktree(ctx, e.user.ID, second, other)
	if err == nil {
		t.Fatal("a second sandbox was created over the tenant's limit")
	}
	var api *contracts.APIError
	if !errors.As(err, &api) || api.Code != contracts.ErrTenantSandboxLimit {
		t.Fatalf("error = %v, want the typed %s", err, contracts.ErrTenantSandboxLimit)
	}
	// The message has to name the limit and the current use: "quota exceeded" with
	// no numbers is not something a user can act on.
	if api.Details["limit"] != 1 || api.Details["current"] != 1 {
		t.Fatalf("details = %v, want limit 1 and current 1", api.Details)
	}
	if len(api.Message) == 0 || !contains(api.Message, "1") {
		t.Fatalf("message %q does not name the limit", api.Message)
	}

	// A tenant with a larger policy is not affected: the guard is per tenant, not
	// a deployment-wide count.
	e.svc.cfg.LimitsFor = func(_ context.Context, userID string) tenantlimits.Limits {
		return tenantlimits.Limits{MaxSandboxes: 5}
	}
	third := e.projectNamed("three")
	thirdSession := e.session(third, contracts.KindMain, "", "")
	if _, err := e.svc.EnsureWorktree(ctx, e.user.ID, third, thirdSession); err != nil {
		t.Fatalf("a raised limit still refused: %v", err)
	}
}

// No policy means no guard: a single-user deployment that configured nothing must
// keep working exactly as before.
func TestNoTenantPolicyMeansNoSandboxGuard(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if e.svc.cfg.LimitsFor != nil {
		t.Fatal("the test fixture unexpectedly configured a policy")
	}
	for _, name := range []string{"a", "b", "c"} {
		p := e.projectNamed(name)
		sess := e.session(p, contracts.KindMain, "", "")
		if _, err := e.svc.EnsureWorktree(ctx, e.user.ID, p, sess); err != nil {
			t.Fatalf("project %q: %v", name, err)
		}
	}
}

// projectNamed creates a project with a distinct name, so one test can hold more
// than one sandbox.
func (e *env) projectNamed(name string) *contracts.Project {
	e.t.Helper()
	p, err := e.store.CreateProject(context.Background(), e.user.ID, store.NewProject{
		Name: name, DefaultBranch: "main",
	})
	if err != nil {
		e.t.Fatalf("create project %q: %v", name, err)
	}
	return p
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
