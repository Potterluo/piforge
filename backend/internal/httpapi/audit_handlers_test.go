package httpapi

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
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
			ArgsPreview: func() json.RawMessage {
				if call.name == "bash" {
					return json.RawMessage(`{"command":"ls -la"}`)
				}
				return json.RawMessage(`{"path":"notes.txt"}`)
			}(),
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

// A response writer that records the largest single Write. It is how the
// export test proves the handler streams instead of formatting one huge body:
// a buffered implementation would hand the recorder a multi-megabyte slice.
type boundedResponseWriter struct {
	header   http.Header
	status   int
	maxWrite int
	total    int
	writes   int
	lines    int
}

func (w *boundedResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *boundedResponseWriter) WriteHeader(status int) { w.status = status }

func (w *boundedResponseWriter) Write(p []byte) (int, error) {
	if len(p) > w.maxWrite {
		w.maxWrite = len(p)
	}
	w.total += len(p)
	w.writes++
	for _, b := range p {
		if b == '\n' {
			w.lines++
		}
	}
	return len(p), nil
}

// The export is the whole filtered set, streamed. 50k rows through the real
// handler must not build a 50k-row slice: the writer sees bounded chunks and
// the heap does not grow with the number of rows.
func TestAdminAuditCSVStreamsFiftyThousandRows(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	const n = 50000
	if err := env.store.ExecForTest(t.Context(), `
		WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM seq WHERE i < ?)
		INSERT INTO audit_log (
			id, seq, session_id, tool_call_id, phase, recorded_at, started_at,
			user_id, project_id, sandbox_id, run_id, user_email, project_name, session_title,
			tool, namespace, args_hash, args_preview, preview_bytes, args_truncated,
			status, model_ref, commit_sha, prev_hash, hash)
		SELECT 'bulk_' || i || ':start', i, 'ses_bulk', 'bulk_' || i, 'start',
		       '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z',
		       'usr_bulk', 'prj_bulk', 'sbx_bulk', 'run_bulk', 'bulk@piforge.local', 'bulk', 'bulk',
		       'bash', '', 'hash', '{"command":"echo row ' || i || '"}', 24, 0,
		       'running', 'volc/deepseek-v4-flash', 'abc123', '', ''
		  FROM seq`, n); err != nil {
		t.Fatalf("seed 50k audit rows: %v", err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/audit?format=csv", nil)
	for _, ck := range admin.cookies {
		req.AddCookie(ck)
	}
	writer := &boundedResponseWriter{}
	env.handler.ServeHTTP(writer, req)

	if writer.status != http.StatusOK {
		t.Fatalf("export status = %d", writer.status)
	}
	if ct := writer.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if writer.Header().Get("X-Audit-Export") != "streaming" {
		t.Fatalf("the export did not announce that it streams: %v", writer.Header())
	}
	if writer.lines != n+1 {
		t.Fatalf("exported %d CSV lines (header + %d expected)", writer.lines, n)
	}
	if writer.maxWrite > 64*1024 {
		t.Fatalf("one Write was %d bytes; the export is buffering the whole body", writer.maxWrite)
	}
	// 50k CSV lines: about 12 MB. A collect-then-write implementation would
	// have that on the heap before the first byte.
	if writer.total < n*100 {
		t.Fatalf("export wrote %d bytes, want at least %d", writer.total, n*100)
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if grew := after.HeapAlloc - before.HeapAlloc; grew > 32<<20 {
		t.Fatalf("heap grew by %d MiB exporting %d rows", grew>>20, n)
	}
}

// Every filter the UI offers is one the endpoint understands, including the
// short aliases, and jsonl is a real export rather than a JSON array.
func TestAdminAuditFiltersAndJSONL(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	dev := env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	project, sess := env.mkSessionFor(dev.ID, "filtered")
	seedToolCalls(t, env, dev.ID, sess.ID)

	cases := []struct {
		query string
		want  int
	}{
		{"tool=bash", 1},
		{"outcome=ok", 1},
		{"outcome=error", 1},
		{"outcome=running", 0},
		{"user=" + dev.ID, 2},
		{"project=" + project.ID, 2},
		{"session=" + sess.ID, 2},
		{"q=notes", 1},
		{"min_duration_ms=2000", 0},
		{"min_duration_ms=1000", 2},
		{"since=2026-10-31T19:00:00Z", 2},
		{"until=2026-10-31T19:00:00Z", 0},
	}
	for _, tc := range cases {
		rec := env.doAuthed(http.MethodGet, "/v1/admin/audit?"+tc.query, nil, admin)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.query, rec.Code, rec.Body.String())
		}
		var body struct {
			Calls []store.AuditRow `json:"calls"`
			Total int              `json:"total"`
		}
		env.mustJSON(rec, &body)
		if body.Total != tc.want || len(body.Calls) != tc.want {
			t.Fatalf("%s: total/rows = %d/%d, want %d", tc.query, body.Total, len(body.Calls), tc.want)
		}
	}
	if rec := env.doAuthed(http.MethodGet, "/v1/admin/audit?min_duration_ms=x", nil, admin); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad min_duration_ms = %d, want 400", rec.Code)
	}

	rec := env.doAuthed(http.MethodGet, "/v1/admin/audit?format=jsonl&tool=bash", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("jsonl: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Fatalf("jsonl Content-Type = %q", ct)
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("jsonl lines = %d, want 1: %s", len(lines), rec.Body.String())
	}
	var row store.AuditRow
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("jsonl line: %v: %s", err, lines[0])
	}
	if row.Tool != "bash" || len(row.ArgsPreview) == 0 {
		t.Fatalf("jsonl row = %+v", row)
	}
}

// The verify endpoint is the operator's one-request answer to "is this trail
// intact", and it names the first break rather than a boolean.
func TestAdminAuditVerifyReportsFirstBreak(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	dev := env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	_, sess := env.mkSessionFor(dev.ID, "chain")
	seedToolCalls(t, env, dev.ID, sess.ID)

	rec := env.doAuthed(http.MethodGet, "/v1/admin/audit/verify?session_id="+sess.ID, nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body.String())
	}
	var result store.AuditVerifyResult
	env.mustJSON(rec, &result)
	if !result.OK || result.Checked != 4 {
		t.Fatalf("verify = %+v, want 4 intact rows", result)
	}

	if err := env.store.ExecForTest(t.Context(),
		`UPDATE audit_log SET args_preview = '{"command":"tampered"}' WHERE tool_call_id = 'tc_ok' AND phase = 'start'`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	rec = env.doAuthed(http.MethodGet, "/v1/admin/audit/verify?session_id="+sess.ID, nil, admin)
	env.mustJSON(rec, &result)
	if result.OK || result.FirstBreak == nil || result.FirstBreak.Reason != "edited" {
		t.Fatalf("tampered verify = %+v", result)
	}

	// A normal account does not get to look.
	if rec := env.doAuthed(http.MethodGet, "/v1/admin/audit/verify", nil, env.login("dev@piforge.local", "devpw")); rec.Code != http.StatusForbidden {
		t.Fatalf("verify as a tenant = %d, want 403", rec.Code)
	}
}

// auditCSVRecord is exercised by the export tests already; this pins the
// spreadsheet rule on the two new tenant-written columns (preview, model).
func TestAuditCSVRecordNeutralisesTheNewColumns(t *testing.T) {
	row := store.AuditRow{
		StartedAt: time.Now(), ID: "tc", Tool: "=cmd|' /C calc'!A0", Status: "ok",
		ModelRef: "@SUM(1)",
	}
	record := auditCSVRecord(row)
	if tool := record[4]; !strings.HasPrefix(tool, "'") {
		t.Fatalf("tool cell = %q, want a leading quote", tool)
	}
	model := record[19]
	if !strings.HasPrefix(model, "'") {
		t.Fatalf("model cell = %q, want a leading quote", model)
	}
}
