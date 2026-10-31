package httpapi

import (
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
	"github.com/piforge/piforge/backend/internal/tenantlimits"
)

// The tenant block is the operator's answer to "what is this account allowed to
// use, and how close is it": both numbers have to come from the deployment's
// policy and the sandbox rows, not from a guess.
func TestAdminStatusReportsTenantLimitsAndUsage(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	devUser := env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	// One sandbox for the dev account, so its row shows a non-zero count.
	project, err := env.store.CreateProject(t.Context(), devUser.ID, store.NewProject{Name: "limits", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := env.store.CreateSandbox(t.Context(), devUser.ID, store.NewSandbox{
		ProjectID: project.ID, State: contracts.SandboxReady, Provider: "local",
	}); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	policy, err := tenantlimits.Parse(`{
		"default": {"cpus": "1", "memory": "2g", "pids": 256, "max_sandboxes": 2},
		"tenants": {"dev@piforge.local": {"cpus": "4"}}
	}`, tenantlimits.Limits{})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	env.deps.TenantLimits = policy
	env.deps.SandboxNetworkMode = "tenant"
	env.rebuild()

	rec := env.doAuthed(http.MethodGet, "/v1/admin/status", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		TenantLimits struct {
			CPUs         string `json:"cpus"`
			Memory       string `json:"memory"`
			Pids         int    `json:"pids"`
			MaxSandboxes int    `json:"max_sandboxes"`
			Source       string `json:"source"`
			NetworkMode  string `json:"network_mode"`
			Enforced     bool   `json:"enforced"`
			Tenants      []struct {
				UserID       string `json:"user_id"`
				Email        string `json:"email"`
				CPUs         string `json:"cpus"`
				Memory       string `json:"memory"`
				Pids         int    `json:"pids"`
				MaxSandboxes int    `json:"max_sandboxes"`
				Sandboxes    int    `json:"sandboxes"`
				Override     bool   `json:"override"`
			} `json:"tenants"`
		} `json:"tenant_limits"`
	}
	env.mustJSON(rec, &body)
	got := body.TenantLimits
	if !got.Enforced || got.CPUs != "1" || got.Memory != "2g" || got.Pids != 256 || got.MaxSandboxes != 2 {
		t.Fatalf("default block = %+v", got)
	}
	if got.NetworkMode != "tenant" {
		t.Fatalf("network_mode = %q, want the deployment's mode", got.NetworkMode)
	}
	if len(got.Tenants) < 2 {
		t.Fatalf("every account should be listed, got %+v", got.Tenants)
	}
	var dev *struct {
		UserID       string `json:"user_id"`
		Email        string `json:"email"`
		CPUs         string `json:"cpus"`
		Memory       string `json:"memory"`
		Pids         int    `json:"pids"`
		MaxSandboxes int    `json:"max_sandboxes"`
		Sandboxes    int    `json:"sandboxes"`
		Override     bool   `json:"override"`
	}
	for i := range got.Tenants {
		if got.Tenants[i].UserID == devUser.ID {
			dev = &got.Tenants[i]
		}
	}
	if dev == nil {
		t.Fatalf("the dev account is missing from %+v", got.Tenants)
	}
	if dev.CPUs != "4" || dev.Memory != "2g" || !dev.Override {
		t.Fatalf("dev row = %+v, want the override's 4 CPUs and the default's memory", dev)
	}
	if dev.Sandboxes != 1 {
		t.Fatalf("dev sandboxes = %d, want the one this fixture created", dev.Sandboxes)
	}
}
