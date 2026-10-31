package httpapi

import (
	"net/http"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/sandbox"
	"github.com/piforge/piforge/backend/internal/tenantegress"
)

// "Who can reach the internet" is a per-tenant question, and the operator's one
// screen has to answer it next to the quota: a mode that is only in a config file
// is a mode nobody can act on.
func TestAdminStatusReportsPerTenantEgress(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	devUser := env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	openUser := env.mkUser("open@piforge.local", "openpw", contracts.RoleUser)

	policy, err := tenantegress.Parse(`{
		"default": {"mode": "none"},
		"tenants": {"open@piforge.local": {"mode": "open"}}
	}`, tenantegress.Egress{Mode: sandbox.EgressNone})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	env.deps.TenantEgress = policy
	env.deps.SandboxEgressApplied = true
	env.deps.SandboxNetworkMode = "tenant"
	env.rebuild()

	rec := env.doAuthed(http.MethodGet, "/v1/admin/status", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		TenantLimits struct {
			NetworkMode      string `json:"network_mode"`
			EgressMode       string `json:"egress_mode"`
			EgressSource     string `json:"egress_source"`
			EgressConfigured bool   `json:"egress_configured"`
			EgressApplied    bool   `json:"egress_applied"`
			Tenants          []struct {
				UserID         string `json:"user_id"`
				Email          string `json:"email"`
				Egress         string `json:"egress"`
				EgressOverride bool   `json:"egress_override"`
			} `json:"tenants"`
		} `json:"tenant_limits"`
	}
	env.mustJSON(rec, &body)
	block := body.TenantLimits
	if !block.EgressConfigured || !block.EgressApplied {
		t.Fatalf("egress flags = configured:%v applied:%v", block.EgressConfigured, block.EgressApplied)
	}
	if block.EgressMode != "none" {
		t.Fatalf("default egress mode = %q, want none", block.EgressMode)
	}
	if block.EgressSource != "inline" {
		t.Fatalf("egress source = %q", block.EgressSource)
	}
	// Every account is listed, so a tenant with the default is as visible as one
	// with an override.
	var dev, open *struct {
		UserID         string `json:"user_id"`
		Email          string `json:"email"`
		Egress         string `json:"egress"`
		EgressOverride bool   `json:"egress_override"`
	}
	for i := range block.Tenants {
		switch block.Tenants[i].UserID {
		case devUser.ID:
			dev = &block.Tenants[i]
		case openUser.ID:
			open = &block.Tenants[i]
		}
	}
	if dev == nil || open == nil {
		t.Fatalf("tenants missing from %+v", block.Tenants)
	}
	if dev.Egress != "none" || dev.EgressOverride {
		t.Fatalf("dev row = %+v, want the default none with no override", dev)
	}
	if open.Egress != "open" || !open.EgressOverride {
		t.Fatalf("open row = %+v, want open and flagged as an override", open)
	}
}

// A deployment with no egress policy is open, and the page has to say that rather
// than showing an empty string that reads like "closed".
func TestAdminStatusSaysWhenNoEgressPolicyIsConfigured(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	env.rebuild()

	rec := env.doAuthed(http.MethodGet, "/v1/admin/status", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		TenantLimits struct {
			EgressMode       string `json:"egress_mode"`
			EgressSource     string `json:"egress_source"`
			EgressConfigured bool   `json:"egress_configured"`
		} `json:"tenant_limits"`
	}
	env.mustJSON(rec, &body)
	if body.TenantLimits.EgressConfigured {
		t.Fatal("egress reported as configured with no policy")
	}
	if body.TenantLimits.EgressMode != "open" || body.TenantLimits.EgressSource != "none" {
		t.Fatalf("unconfigured egress = %q/%q, want open/none", body.TenantLimits.EgressMode, body.TenantLimits.EgressSource)
	}
}
