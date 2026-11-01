package httpapi

// The audit's edge list, in code.
//
// docs/DATA-FLOW-AUDIT.md enumerates every place a tenant-supplied path, URL, id
// or name reaches something the control plane does. A document cannot fail a
// build, so the same list lives here with the guard each edge is supposed to go
// through, and TestGuardedEdgesStillGoThroughTheirGuard asserts three things:
//
//   - the route is still registered in router.go (a moved or deleted endpoint
//     fails, so the table cannot outlive the code it describes);
//   - the handler is still declared in the file the table names (a handler that
//     moved takes its guard with it — and this notices);
//   - the guard's name still appears in that file.
//
// The second half is the registry: TestEverySinkIsRegistered walks the package
// for the calls that read a path, fetch a URL, execute a process or delete a
// directory. A file that contains one and is not in the table (or in the
// explicitly-declared list below, with a reason) fails the test — which is what
// "the next attack is a new fetch site" needs, because a new fetch site is a new
// file or a new call in one.
//
// What this deliberately does not do is prove data flow. It checks that the
// guard is used in the file, not that it dominates the sink: a handler could
// call saveOutboundURL and then fetch something else, and this would not notice.
// See the "Limits" section of the audit for the rest.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardEdge is one audited edge: the route, the handler, the file it lives in,
// and the guard that has to be there.
type guardEdge struct {
	Feature string
	// Route is the exact pattern passed to mux.Handle/HandleFunc, e.g.
	// "POST /v1/projects/{id}/sessions".
	Route string
	// Handler is the method name on *server, without the receiver.
	Handler string
	// File is the file name in this package.
	File string
	// Guard is a literal substring of the guard's use in that file.
	Guard string
}

// guardedEdges is the audit table. Add a row when an endpoint gains a
// tenant-shaped input; the registry half fails if you add a sink instead.
var guardedEdges = []guardEdge{
	// --- skills -------------------------------------------------------------
	{"skill import (git)", "POST /v1/skills/import", "handleImportSkills", "skill_import.go", "refuseOutboundURL"},
	{"skill import (git, checkouts)", "POST /v1/skills/import", "handleImportSkills", "skill_import.go", "tenantfs.Open"},
	{"skill import (file:// path)", "POST /v1/skills/import", "handleImportSkills", "skill_import.go", "tenantfs.Resolve"},
	{"skill import (zip)", "POST /v1/skills/import/zip", "handleImportSkillsZip", "skill_import.go", "extractSkillZipWithin"},
	{"skill bundle read", "GET /v1/skills/{id}/bundle", "handleReadSkillBundle", "skill_bundle_handlers.go", "tenantfs.Open"},
	{"skill bundle upload", "PUT /v1/skills/{id}/bundle", "handleUploadSkillBundle", "skill_bundle_handlers.go", "bundleFiles"},
	{"skill delete (bundle dir)", "DELETE /v1/skills/{id}", "handleDeleteSkill", "settings_handlers.go", "os.RemoveAll(filepath.Join(s.deps.SkillsDir"},
	{"skill patch", "PATCH /v1/skills/{id}", "handlePatchSkill", "settings_handlers.go", "Store.Skill(r.Context(), user.ID"},

	// --- providers ----------------------------------------------------------
	{"provider create (base_url)", "POST /v1/providers", "handleCreateProvider", "provider_handlers.go", "saveOutboundURL"},
	{"provider update (base_url)", "PATCH /v1/providers/{id}", "handleUpdateProvider", "provider_handlers.go", "saveOutboundURL"},
	{"provider test (base_url)", "POST /v1/providers/{id}/test", "handleTestProvider", "provider_probe.go", "refuseOutboundURL"},
	{"admin provider upsert", "POST /v1/admin/providers", "handleAdminUpsertProvider", "provider_handlers.go", "saveOutboundURL"},
	{"admin provider update", "PATCH /v1/admin/providers/{id}", "handleAdminUpdateProvider", "provider_handlers.go", "saveOutboundURL"},

	// --- MCP ----------------------------------------------------------------
	{"mcp create (url)", "POST /v1/mcp/servers", "handleCreateMCPServer", "settings_handlers.go", "saveOutboundURL"},
	{"mcp broker call", " /gw/mcp/", "handleMCPGateway", "mcp_broker.go", "refuseOutboundURL"},
	{"mcp broker headers", " /gw/mcp/", "handleMCPGateway", "mcp_broker.go", "mcpBrokerHeaders"},
	{"mcp token revoke", "DELETE /v1/sessions/{id}/mcp-token", "handleRevokeMCPTokens", "mcp_broker.go", "Store.Session(r.Context(), user.ID"},

	// --- projects / sessions ------------------------------------------------
	{"project create", "POST /v1/projects", "handleCreateProject", "project_handlers.go", "validateRepoURL"},
	{"project patch", "PATCH /v1/projects/{id}", "handlePatchProject", "project_handlers.go", "validateRepoURL"},
	{"project forge (api_base)", "PUT /v1/projects/{id}/forge", "handleSetProjectForge", "pr.go", "saveOutboundURL"},
	{"project delete", "DELETE /v1/projects/{id}", "handleDeleteProject", "project_handlers.go", "Store.Project(r.Context(), user.ID"},
	{"session create (project)", "POST /v1/projects/{id}/sessions", "handleCreateSession", "project_handlers.go", "Store.Project(r.Context(), user.ID"},
	{"session create (worktree/parent id)", "POST /v1/projects/{id}/sessions", "handleCreateSession", "project_handlers.go", "Store.Worktree(r.Context(), user.ID"},
	{"session export", "GET /v1/sessions/{id}/export", "handleExportSession", "session_export.go", "exportFilename"},
	{"session fork", "POST /v1/sessions/{id}/fork", "handleFork", "session_handlers.go", "Store.Session(r.Context(), user.ID"},
	{"session clone", "POST /v1/sessions/{id}/clone", "handleCloneSession", "session_clone.go", "Store.Session(r.Context(), user.ID"},
	{"branch adopt", "POST /v1/sessions/{id}/adopt", "handleAdoptBranch", "branch_adopt.go", "Store.Session(r.Context(), user.ID"},
	{"sidecar promote", "POST /v1/sessions/{id}/promote", "handlePromoteSidecar", "sidecar_promote.go", "Store.Session(r.Context(), user.ID"},

	// --- files --------------------------------------------------------------
	{"file read", "GET /v1/sessions/{id}/files", "handleReadFile", "workspace_handlers.go", "rootOrFail"},
	{"file write", "PUT /v1/sessions/{id}/files", "handleWriteFile", "workspace_handlers.go", "rootOrFail"},
	{"file delete", "DELETE /v1/sessions/{id}/files", "handleDeleteFile", "workspace_handlers.go", "rootOrFail"},
	{"file tree", "GET /v1/sessions/{id}/files/tree", "handleFileTree", "workspace_handlers.go", "rootOrFail"},
	{"search", "GET /v1/sessions/{id}/search", "handleSearch", "workspace_handlers.go", "rootOrFail"},
	{"artifact read", "GET /v1/sessions/{id}/artifacts", "handleArtifactFile", "artifacts.go", "artifactSessionRoot"},
	{"artifact download header", "GET /v1/sessions/{id}/artifacts", "handleArtifactFile", "artifacts.go", "dispositionName"},
	{"workspace root resolution", "GET /v1/sessions/{id}/files", "rootOrFail", "workspace_handlers.go", "s.worktreeRoot(r.Context(), user.ID, sess)"},

	// --- git ----------------------------------------------------------------
	{"git diff scope", "GET /v1/sessions/{id}/git/diff", "handleGitDiff", "workspace_handlers.go", "parseDiffScope"},
	{"git branch", "POST /v1/sessions/{id}/git/branch", "handleGitBranch", "workspace_handlers.go", "branchRequest"},
	{"git push", "POST /v1/sessions/{id}/git/push", "handleGitPush", "git_remote.go", "remoteRoot"},
	{"git pull", "POST /v1/sessions/{id}/git/pull", "handleGitPull", "git_remote.go", "remoteRoot"},
	{"git pr preview", "GET /v1/sessions/{id}/git/pr", "handlePRPreview", "pr.go", "remoteRoot"},
	{"git pr create", "POST /v1/sessions/{id}/git/pr", "handleCreatePR", "pr.go", "forgeClient"},
	{"git hunk", "POST /v1/sessions/{id}/git/hunk", "handleGitHunk", "git_hunks.go", "rootOrFail"},

	// --- browser / terminal -------------------------------------------------
	{"browser open tab (url)", "POST /v1/sessions/{id}/browser/tabs", "handleOpenBrowserTab", "browser_tab_handlers.go", "browser.ValidateNavigationURL"},
	{"browser close tab (id)", "DELETE /v1/sessions/{id}/browser/tabs/{tab}", "handleCloseBrowserTab", "browser_tab_handlers.go", "browserFor"},
	{"browser screenshot", "POST /v1/sessions/{id}/browser/screenshot", "handleBrowserScreenshot", "browser_screenshot.go", "saveScreenshot"},
	{"terminal tab id", "GET /v1/sessions/{id}/pty", "handleSessionPTY", "pty_handlers.go", "sanitizeTab"},

	// --- sandbox lifecycle --------------------------------------------------
	{"sandbox state", "GET /v1/sessions/{id}/sandbox", "handleSandboxState", "sandbox_handlers.go", "sandboxFor"},
	{"sandbox pause", "POST /v1/sessions/{id}/sandbox/pause", "handleSandboxPause", "sandbox_handlers.go", "sandboxFor"},
	{"sandbox resume", "POST /v1/sessions/{id}/sandbox/resume", "handleSandboxResume", "sandbox_handlers.go", "sandboxFor"},
	{"sandbox snapshots", "GET /v1/sessions/{id}/sandbox/snapshots", "handleSandboxSnapshots", "sandbox_handlers.go", "sandboxFor"},

	// --- admin --------------------------------------------------------------
	{"admin users", "GET /v1/admin/users", "handleAdminUsers", "admin_handlers.go", "contracts.RoleAdmin"},
	{"admin create user", "POST /v1/admin/users", "handleAdminCreateUser", "admin_handlers.go", "contracts.RoleAdmin"},
	{"admin update user", "PATCH /v1/admin/users/{id}", "handleAdminUpdateUser", "admin_handlers.go", "contracts.RoleAdmin"},
	{"admin status", "GET /v1/admin/status", "handleAdminStatus", "admin_handlers.go", "contracts.RoleAdmin"},
	{"admin audit", "GET /v1/admin/audit", "handleAdminAudit", "audit_handlers.go", "contracts.RoleAdmin"},
	{"admin audit verify", "GET /v1/admin/audit/verify", "handleAdminAuditVerify", "audit_handlers.go", "contracts.RoleAdmin"},
	{"admin feature", "POST /v1/admin/users/{id}/features", "handleAdminSetUserFeature", "feature_handlers.go", "KnownFeature"},
	{"support access", "POST /v1/admin/support-access", "handleCreateSupportAccess", "support_handlers.go", "CreateSupportAccess"},
	{"support transcript", "GET /v1/admin/support-access/{id}/transcript", "handleSupportTranscript", "support_handlers.go", "SupportAccessGrant"},
}

// declaredSinks is the escape hatch, and it is empty on purpose: every file in
// this package that contains a sink today is a guarded edge below. An entry here
// needs a reason — "the reader is behind deps.Workspace" is one, "it was easier"
// is not — and the entry is the thing a reviewer reads when the sink changes.
var declaredSinks = map[string]string{}

// sinkCalls are the calls that make a file worth registering: a read of a path,
// a fetch of a URL, an execution of a process, or a removal of a directory.
var sinkCalls = []string{
	"os.ReadFile(",
	"os.Open(",
	"os.OpenFile(",
	"os.WriteFile(",
	"os.RemoveAll(",
	"exec.Command(",
	"exec.CommandContext(",
	"http.NewRequestWithContext(",
	".Do(req",
	"filepath.Join(s.deps.",
}

// TestGuardedEdgesStillGoThroughTheirGuard is the half that fails when a guard
// is removed or a handler moves.
func TestGuardedEdgesStillGoThroughTheirGuard(t *testing.T) {
	dir := packageDir(t)
	router := readSource(t, filepath.Join(dir, "router.go"))
	if len(guardedEdges) < 40 {
		t.Fatalf("the audit table has %d rows; it is supposed to describe every feature", len(guardedEdges))
	}

	seen := map[string]bool{}
	for _, edge := range guardedEdges {
		edge := edge
		key := edge.Feature
		if seen[key] {
			t.Fatalf("duplicate audit row for %s", key)
		}
		seen[key] = true

		t.Run(edge.Feature, func(t *testing.T) {
			route := strings.TrimSpace(edge.Route)
			if !strings.Contains(router, route) {
				t.Fatalf("route %q is not registered in router.go any more: "+
					"the audit describes an endpoint that does not exist", route)
			}
			src := readSource(t, filepath.Join(dir, edge.File))
			if !strings.Contains(src, "func (s *server) "+edge.Handler+"(") {
				t.Fatalf("%s moved out of %s: the audit's guard claim cannot be verified", edge.Handler, edge.File)
			}
			if !strings.Contains(src, edge.Guard) {
				t.Fatalf("%s no longer uses %q: either the guard was removed or the "+
					"edge needs a new row in guardedEdges (docs/DATA-FLOW-AUDIT.md)", edge.Handler, edge.Guard)
			}
		})
	}
}

// TestEverySinkIsRegistered is the registry half: a file that reads a path,
// fetches a URL, runs a process or deletes a tree has to be in the table or in
// declaredSinks. A new fetch site fails here before it becomes the next report.
func TestEverySinkIsRegistered(t *testing.T) {
	dir := packageDir(t)
	registered := map[string]bool{}
	for _, edge := range guardedEdges {
		registered[edge.File] = true
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src := readSource(t, filepath.Join(dir, name))
		var found []string
		for _, sink := range sinkCalls {
			if strings.Contains(src, sink) {
				found = append(found, sink)
			}
		}
		if len(found) == 0 {
			continue
		}
		if registered[name] {
			continue
		}
		if reason, ok := declaredSinks[name]; ok && strings.TrimSpace(reason) != "" {
			continue
		}
		t.Errorf("%s contains %v but is neither a guarded edge nor a declared sink.\n"+
			"Choose one: add the edge (route, handler, file, guard) to guardedEdges and to "+
			"docs/DATA-FLOW-AUDIT.md, or add the file to declaredSinks with the reason it is safe.",
			name, found)
	}
}

// packageDir locates this source directory from the test binary's own location.
func packageDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

func readSource(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}
