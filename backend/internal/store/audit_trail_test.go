package store

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// mkAuditCall writes the two rows of one tool call the same way a run does:
// intent through CreateToolCall, observation through FinishToolCall.
func mkAuditCall(t *testing.T, s *Store, userID, sessionID, id, name string, args string, started time.Time, duration int, failed bool) {
	t.Helper()
	mkAuditCallWithContext(t, s, userID, sessionID, id, name, args, started, duration, failed, "", "")
}

func mkAuditCallWithContext(t *testing.T, s *Store, userID, sessionID, id, name string, args string, started time.Time, duration int, failed bool, modelRef, commitSHA string) {
	t.Helper()
	ctx := context.Background()
	raw := json.RawMessage(args)
	if err := s.CreateToolCall(ctx, userID, ToolCall{
		ID: id, SessionID: sessionID, RunID: "run_" + id, Name: name,
		ArgsHash: "hash-" + id, ArgsPreview: raw, StartedAt: started,
		ModelRef: modelRef, CommitSHA: commitSHA,
	}); err != nil {
		t.Fatalf("CreateToolCall(%s): %v", id, err)
	}
	exit := 0
	if failed {
		exit = 1
	}
	ended := started.Add(time.Duration(duration) * time.Millisecond)
	if err := s.FinishToolCall(ctx, userID, id, ToolCallFinish{
		IsError: failed, ExitCode: &exit, DurationMS: &duration, ResultBytes: intp(64), EndedAt: &ended,
	}); err != nil {
		t.Fatalf("FinishToolCall(%s): %v", id, err)
	}
}

func intp(v int) *int { return &v }

// The audit trail is not the transcript. Deleting the session, then the
// project, then the account must leave the record of what the agent did —
// otherwise the first thing an incident responder does destroys the evidence.
func TestAuditRowsSurviveSessionAndProjectDeletion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "gone@piforge.local", "user")
	project := mkProject(t, s, u.ID, "incident")
	sess := mkSession(t, s, u.ID, project.ID, "main")
	started := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	mkAuditCallWithContext(t, s, u.ID, sess.ID, "tc_survives", "bash",
		`{"command":"rm -rf build","api_key":"sk-live-abcdef0123456789"}`, started, 1234, false,
		"volc/deepseek-v4-flash", "0123456789abcdef")

	if err := s.BeginAuditRun(ctx, AuditRun{
		RunID: "run_tc_survives", SessionID: sess.ID, UserID: u.ID,
		ModelRef: "volc/deepseek-v4-flash", CommitSHA: "0123456789abcdef",
		StartedAt: started,
	}); err != nil {
		t.Fatalf("BeginAuditRun: %v", err)
	}
	if err := s.SettleAuditRun(ctx, AuditRunSettle{
		RunID: "run_tc_survives", SessionID: sess.ID, Status: "settled",
		TokensIn: 1200, TokensOut: 345, CostUSD: 0.0123,
	}); err != nil {
		t.Fatalf("SettleAuditRun: %v", err)
	}

	// The transcript goes; the audit row must not.
	if err := s.DeleteSession(ctx, u.ID, sess.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	assertSurvived := func(step string) {
		t.Helper()
		rows, total, err := s.AuditToolCalls(ctx, AuditFilter{UserID: u.ID})
		if err != nil {
			t.Fatalf("%s: AuditToolCalls: %v", step, err)
		}
		if total != 1 || len(rows) != 1 {
			t.Fatalf("%s: total/rows = %d/%d, want the audit row to survive", step, total, len(rows))
		}
		row := rows[0]
		if row.SessionID != sess.ID || row.ProjectID != project.ID || row.UserID != u.ID {
			t.Fatalf("%s: denormalised ids = %+v", step, row)
		}
		if row.UserEmail != "gone@piforge.local" || row.Project != "incident" {
			t.Fatalf("%s: labels were lost: email=%q project=%q", step, row.UserEmail, row.Project)
		}
		if row.ModelRef != "volc/deepseek-v4-flash" || row.CommitSHA != "0123456789abcdef" {
			t.Fatalf("%s: model/commit = %q/%q", step, row.ModelRef, row.CommitSHA)
		}
		if row.RunTokensIn != 1200 || row.RunTokensOut != 345 || row.RunCostUSD != 0.0123 {
			t.Fatalf("%s: run facts = %+v", step, row)
		}
		if !strings.Contains(string(row.ArgsPreview), "<redacted>") {
			t.Fatalf("%s: the preview lost its redaction: %s", step, row.ArgsPreview)
		}
	}
	assertSurvived("after session delete")

	if err := s.DeleteProject(ctx, u.ID, project.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	assertSurvived("after project delete")

	// The account itself is soft-deleted in this product; the audit query is
	// not allowed to depend on the users row either.
	if err := s.ExecForTest(ctx, `UPDATE users SET deleted_at = ? WHERE id = ?`, FormatTimeForTest(time.Now()), u.ID); err != nil {
		t.Fatalf("soft delete user: %v", err)
	}
	assertSurvived("after account deletion")
}

// Every filter has to answer the question it names, and the total has to
// describe the same set the page returns.
func TestAuditFilters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	alice := mkUser(t, s, "alice@piforge.local", "user")
	bob := mkUser(t, s, "bob@piforge.local", "user")
	projA := mkProject(t, s, alice.ID, "alpha")
	projB := mkProject(t, s, bob.ID, "beta")
	sessA := mkSession(t, s, alice.ID, projA.ID, "main")
	sessB := mkSession(t, s, bob.ID, projB.ID, "main")

	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	mkAuditCall(t, s, alice.ID, sessA.ID, "tc_ls", "bash", `{"command":"ls -la"}`, base, 100, false)
	mkAuditCall(t, s, alice.ID, sessA.ID, "tc_write", "write", `{"path":"notes/secret-plan.txt","content":"shh"}`, base.Add(time.Hour), 9000, true)
	mkAuditCall(t, s, bob.ID, sessB.ID, "tc_read", "read", `{"path":"README.md"}`, base.Add(2*time.Hour), 300, false)

	cases := []struct {
		name   string
		filter AuditFilter
		want   []string
	}{
		{"tool", AuditFilter{Tool: "write"}, []string{"tc_write"}},
		{"outcome ok", AuditFilter{Status: "ok"}, []string{"tc_read", "tc_ls"}},
		{"outcome error", AuditFilter{Status: "error"}, []string{"tc_write"}},
		{"user", AuditFilter{UserID: bob.ID}, []string{"tc_read"}},
		{"project", AuditFilter{ProjectID: projA.ID}, []string{"tc_write", "tc_ls"}},
		{"session", AuditFilter{SessionID: sessA.ID}, []string{"tc_write", "tc_ls"}},
		{"since", AuditFilter{Since: base.Add(90 * time.Minute)}, []string{"tc_read"}},
		{"until", AuditFilter{Until: base.Add(30 * time.Minute)}, []string{"tc_ls"}},
		{"min duration", AuditFilter{MinDurationMS: 1000}, []string{"tc_write"}},
		{"preview search", AuditFilter{Search: "secret-plan"}, []string{"tc_write"}},
		{"search is case-insensitive", AuditFilter{Search: "README"}, []string{"tc_read"}},
		{"combined", AuditFilter{UserID: alice.ID, Status: "ok"}, []string{"tc_ls"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, total, err := s.AuditToolCalls(ctx, tc.filter)
			if err != nil {
				t.Fatalf("AuditToolCalls: %v", err)
			}
			if total != len(tc.want) || len(rows) != len(tc.want) {
				t.Fatalf("total/rows = %d/%d, want %d", total, len(rows), len(tc.want))
			}
			got := make([]string, 0, len(rows))
			for _, row := range rows {
				got = append(got, row.ID)
			}
			for _, want := range tc.want {
				if !contains(got, want) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}

	// An outcome with no rows is an empty result, not an error, but the total
	// still has to agree with the page.
	rows, total, err := s.AuditToolCalls(ctx, AuditFilter{Status: "abandoned"})
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("abandoned: rows=%d total=%d err=%v", len(rows), total, err)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// The export path must not hold the set it exports. 50k rows go in through one
// statement (the point is the read path), then StreamAuditCalls hands them out
// one at a time. The assertion is on the reader: a batch-shaped API would show
// up as a large heap growth here.
func TestStreamAuditCallsIsBoundedForFiftyThousandRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const n = 50000
	// One recursive INSERT: 50k audit rows without 50k round trips. Only the
	// read path is under test, so the hashes are empty on purpose.
	err := s.ExecForTest(ctx, `
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
		  FROM seq`, n)
	if err != nil {
		t.Fatalf("seed 50k audit rows: %v", err)
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	count := 0
	longestPreview := 0
	if err := s.StreamAuditCalls(ctx, AuditFilter{}, func(row AuditRow) error {
		count++
		if len(row.ArgsPreview) > longestPreview {
			longestPreview = len(row.ArgsPreview)
		}
		if count > n {
			return fmt.Errorf("stream returned more rows than were seeded")
		}
		return nil
	}); err != nil {
		t.Fatalf("StreamAuditCalls: %v", err)
	}
	if count != n {
		t.Fatalf("streamed %d rows, want %d", count, n)
	}
	if longestPreview == 0 {
		t.Fatal("the stream did not carry previews")
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	// The rows themselves are ~5 KB each once decoded; collecting them would be
	// hundreds of megabytes. A bounded reader stays in the low tens.
	if grew := after.HeapAlloc - before.HeapAlloc; grew > 32<<20 {
		t.Fatalf("heap grew by %d MiB streaming %d rows; the reader is buffering the set", grew>>20, n)
	}
}

// Retention deletes only rows older than the policy, deletes a call's two rows
// together, and leaves a chain that still verifies (anchored at the watermark).
func TestAuditRetentionDeletesOnlyOldRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u := mkUser(t, s, "retain@piforge.local", "user")
	p := mkProject(t, s, u.ID, "retention")
	sess := mkSession(t, s, u.ID, p.ID, "main")
	now := time.Now().UTC().Truncate(time.Second)

	mkAuditCall(t, s, u.ID, sess.ID, "tc_old", "bash", `{"command":"old"}`, now.Add(-40*24*time.Hour), 10, false)
	mkAuditCall(t, s, u.ID, sess.ID, "tc_new", "bash", `{"command":"new"}`, now.Add(-time.Hour), 10, false)
	if err := s.BeginAuditRun(ctx, AuditRun{
		RunID: "run_old", SessionID: sess.ID, UserID: u.ID,
		StartedAt: now.Add(-40 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("BeginAuditRun: %v", err)
	}

	rows, runs, err := s.PruneAuditBefore(ctx, now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatalf("PruneAuditBefore: %v", err)
	}
	if rows != 2 {
		t.Fatalf("pruned %d rows, want the old call's two rows", rows)
	}
	if runs != 1 {
		t.Fatalf("pruned %d run rows, want 1", runs)
	}

	call, total, err := s.AuditToolCalls(ctx, AuditFilter{})
	if err != nil || total != 1 || call[0].ID != "tc_new" {
		t.Fatalf("after retention: total=%d rows=%v err=%v", total, call, err)
	}
	// The remaining rows keep their sequence numbers; the chain starts at seq 3.
	if call[0].Seq != 3 {
		t.Fatalf("remaining call seq = %d, want 3 (the sequence is not renumbered)", call[0].Seq)
	}
	verify, err := s.VerifyAuditChain(ctx, AuditVerifyFilter{SessionID: sess.ID})
	if err != nil {
		t.Fatalf("VerifyAuditChain: %v", err)
	}
	if !verify.OK {
		t.Fatalf("retention made the chain fail: %+v", verify.FirstBreak)
	}
	if len(verify.Anchors) != 1 || verify.Anchors[0].Reason != "retention" {
		t.Fatalf("anchors = %+v, want one retention anchor", verify.Anchors)
	}

	// A second pass with a cutoff that predates everything deletes the rest and
	// reports it, rather than silently doing nothing.
	rows, _, err = s.PruneAuditBefore(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if rows != 2 {
		t.Fatalf("second prune removed %d rows, want 2", rows)
	}
}

// The chain detects the two things an incident responder cares about: a row
// that was edited in place, and a row that was deleted from the middle.
func TestAuditChainDetectsEditsAndDeletions(t *testing.T) {
	newChain := func(t *testing.T) (*Store, string, string) {
		t.Helper()
		s := newTestStore(t)
		u := mkUser(t, s, "chain@piforge.local", "user")
		p := mkProject(t, s, u.ID, "chain")
		sess := mkSession(t, s, u.ID, p.ID, "main")
		base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
		for i, id := range []string{"tc_1", "tc_2", "tc_3"} {
			mkAuditCall(t, s, u.ID, sess.ID, id, "bash",
				fmt.Sprintf(`{"command":"step %d"}`, i), base.Add(time.Duration(i)*time.Minute), 10+i, false)
		}
		return s, u.ID, sess.ID
	}

	t.Run("intact", func(t *testing.T) {
		s, _, sess := newChain(t)
		result, err := s.VerifyAuditChain(context.Background(), AuditVerifyFilter{SessionID: sess})
		if err != nil {
			t.Fatalf("VerifyAuditChain: %v", err)
		}
		if !result.OK || result.Checked != 6 || result.Sessions != 1 {
			t.Fatalf("verify = %+v, want 6 intact rows", result)
		}
	})

	t.Run("edited row", func(t *testing.T) {
		s, _, sess := newChain(t)
		ctx := context.Background()
		// An attacker who can update one cell but not recompute the chain: the
		// stored preview no longer matches the hash the row was written with.
		if err := s.ExecForTest(ctx,
			`UPDATE audit_log SET args_preview = '{"command":"rewritten"}' WHERE tool_call_id = 'tc_2' AND phase = 'start'`); err != nil {
			t.Fatalf("tamper: %v", err)
		}
		result, err := s.VerifyAuditChain(ctx, AuditVerifyFilter{SessionID: sess})
		if err != nil {
			t.Fatalf("VerifyAuditChain: %v", err)
		}
		if result.OK || result.FirstBreak == nil {
			t.Fatalf("an edited row was not detected: %+v", result)
		}
		if result.FirstBreak.Reason != "edited" || result.FirstBreak.ToolCallID != "tc_2" {
			t.Fatalf("break = %+v, want edited at tc_2", result.FirstBreak)
		}
	})

	t.Run("deleted middle row", func(t *testing.T) {
		s, _, sess := newChain(t)
		ctx := context.Background()
		if err := s.ExecForTest(ctx, `DELETE FROM audit_log WHERE tool_call_id = 'tc_2'`); err != nil {
			t.Fatalf("delete middle call: %v", err)
		}
		result, err := s.VerifyAuditChain(ctx, AuditVerifyFilter{SessionID: sess})
		if err != nil {
			t.Fatalf("VerifyAuditChain: %v", err)
		}
		if result.OK || result.FirstBreak == nil {
			t.Fatalf("a deleted middle row was not detected: %+v", result)
		}
		if result.FirstBreak.Reason != "gap" {
			t.Fatalf("break = %+v, want a gap", result.FirstBreak)
		}
	})

	t.Run("deleted head row without a watermark", func(t *testing.T) {
		s, _, sess := newChain(t)
		ctx := context.Background()
		if err := s.ExecForTest(ctx, `DELETE FROM audit_log WHERE tool_call_id = 'tc_1'`); err != nil {
			t.Fatalf("delete head call: %v", err)
		}
		result, err := s.VerifyAuditChain(ctx, AuditVerifyFilter{SessionID: sess})
		if err != nil {
			t.Fatalf("VerifyAuditChain: %v", err)
		}
		if result.OK || result.FirstBreak == nil || result.FirstBreak.Reason != "head" {
			t.Fatalf("a deleted head row was not detected: %+v", result)
		}
	})
}
