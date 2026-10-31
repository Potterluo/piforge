package httpapi

import (
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// seedToolCalls writes what a run writes around a tool: the intent before, the
// observation after. It is done through the store, not the runtime, because this
// endpoint reads the table and nothing else.
func seedToolCalls(t *testing.T, env *testEnv, userID, sessionID string) {
	t.Helper()
	ctx := t.Context()
	started := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	for i, call := range []struct {
		id      string
		name    string
		failed  bool
		endedAt time.Time
	}{
		{"tc_ok", "bash", false, started.Add(2 * time.Second)},
		{"tc_err", "write", true, started.Add(3 * time.Second)},
	} {
		if err := env.store.CreateToolCall(ctx, userID, store.ToolCall{
			ID:        call.id,
			SessionID: sessionID,
			RunID:     "run_1",
			Name:      call.name,
			ArgsHash:  "hash-" + call.id,
			StartedAt: started.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("create tool call %s: %v", call.id, err)
		}
		ended := call.endedAt
		duration := 1500
		exit := 0
		if call.failed {
			exit = 1
		}
		if err := env.store.FinishToolCall(ctx, userID, call.id, store.ToolCallFinish{
			IsError:    call.failed,
			ExitCode:   &exit,
			DurationMS: &duration,
			EndedAt:    &ended,
		}); err != nil {
			t.Fatalf("finish tool call %s: %v", call.id, err)
		}
	}
}

// The audit trail is the operator's cross-tenant read, and it has to name who,
// what, when and how it ended — the four things a transcript cannot answer for a
// tenant you are not signed in as.
func TestAdminAuditListsToolCallsWithContext(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	dev := env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	project, sess := env.mkSessionFor(dev.ID, "audited")
	seedToolCalls(t, env, dev.ID, sess.ID)

	rec := env.doAuthed(http.MethodGet, "/v1/admin/audit", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Calls []store.AuditRow `json:"calls"`
		Total int              `json:"total"`
		Limit int              `json:"limit"`
	}
	env.mustJSON(rec, &body)
	if body.Total != 2 || len(body.Calls) != 2 {
		t.Fatalf("total/calls = %d/%d, want 2", body.Total, len(body.Calls))
	}
	// Newest first: the failed write started second.
	first := body.Calls[0]
	if first.Tool != "write" || first.Outcome() != "error" {
		t.Fatalf("first row = %+v (%s)", first, first.Outcome())
	}
	if first.UserEmail != "dev@piforge.local" || first.ProjectID != project.ID || first.SessionID != sess.ID {
		t.Fatalf("row context = %+v", first)
	}
	if first.Project != "audited" {
		t.Fatalf("project name = %q", first.Project)
	}
	if first.DurationMS == nil || *first.DurationMS != 1500 {
		t.Fatalf("duration = %v", first.DurationMS)
	}

	// A filter narrows the query without changing what "total" means.
	rec = env.doAuthed(http.MethodGet, "/v1/admin/audit?tool=bash", nil, admin)
	env.mustJSON(rec, &body)
	if body.Total != 1 || body.Calls[0].Tool != "bash" {
		t.Fatalf("tool filter = %+v", body)
	}
	// outcome=error is the "what failed" question.
	rec = env.doAuthed(http.MethodGet, "/v1/admin/audit?outcome=error", nil, admin)
	env.mustJSON(rec, &body)
	if body.Total != 1 || body.Calls[0].Tool != "write" {
		t.Fatalf("outcome filter = %+v", body)
	}
	// An unknown outcome is a client error, not an empty result set.
	if rec := env.doAuthed(http.MethodGet, "/v1/admin/audit?outcome=maybe", nil, admin); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad outcome = %d, want 400", rec.Code)
	}

	// The export is a real CSV with a stable header and the same rows.
	rec = env.doAuthed(http.MethodGet, "/v1/admin/audit?format=csv", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("csv: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if disposition := rec.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("%d CSV rows (header + 2 expected): %v", len(records), records)
	}
	if records[0][0] != "started_at" || records[0][3] != "outcome" || records[0][5] != "namespace" {
		t.Fatalf("CSV header = %v", records[0])
	}
	if records[1][3] != "error" || records[1][4] != "write" {
		t.Fatalf("CSV first row = %v", records[1])
	}
}

// It is a cross-tenant read: a normal account must not see another's tool calls,
// and must not see its own through this route either (the session trace is the
// owner's view).
func TestAdminAuditIsAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	dev := env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	devCookie := env.login("dev@piforge.local", "devpw")
	_, sess := env.mkSessionFor(dev.ID, "own")
	seedToolCalls(t, env, dev.ID, sess.ID)

	if rec := env.doAuthed(http.MethodGet, "/v1/admin/audit", nil, devCookie); rec.Code != http.StatusForbidden {
		t.Fatalf("a normal user read the audit trail: %d %s", rec.Code, rec.Body.String())
	}
}

// Paging has to be stable and bounded: an operator scrolling a busy deployment
// must not get duplicates or the whole table in one response.
func TestAdminAuditPagesAndCapsTheLimit(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	dev := env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	_, sess := env.mkSessionFor(dev.ID, "many")
	seedToolCalls(t, env, dev.ID, sess.ID)

	rec := env.doAuthed(http.MethodGet, "/v1/admin/audit?limit=1", nil, admin)
	var body struct {
		Calls  []store.AuditRow `json:"calls"`
		Total  int              `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	env.mustJSON(rec, &body)
	if body.Limit != 1 || len(body.Calls) != 1 || body.Total != 2 {
		t.Fatalf("limit=1: %+v", body)
	}
	first := body.Calls[0].ID

	rec = env.doAuthed(http.MethodGet, "/v1/admin/audit?limit=1&offset=1", nil, admin)
	env.mustJSON(rec, &body)
	if len(body.Calls) != 1 || body.Calls[0].ID == first {
		t.Fatalf("page 2 repeated page 1: %+v", body)
	}

	// A limit above the cap is clamped rather than honoured: the response says
	// what was applied, so a client can tell it did not get what it asked for.
	rec = env.doAuthed(http.MethodGet, "/v1/admin/audit?limit=100000", nil, admin)
	env.mustJSON(rec, &body)
	if body.Limit != store.MaxAuditLimit {
		t.Fatalf("limit = %d, want the cap %d", body.Limit, store.MaxAuditLimit)
	}
}
