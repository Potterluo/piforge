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

// G8 of docs/DATA-FLOW-AUDIT.md: the audit export is a spreadsheet an admin
// opens, and a tool name is written by the agent that ran it. A name beginning
// with `=`, `+`, `-`, `@`, TAB or CR is a formula in Excel, LibreOffice and
// Sheets, so the tenant's text executes in the operator's spreadsheet.
//
// The OWASP mitigation is to prefix the cell with a single quote, which every
// one of those readers treats as "this cell is text" and does not display.
// This test seeds the exact payload the GAP names and proves it leaves the
// export inert, on every string column.

// seedToolCallNamed writes one finished tool call with a caller-chosen name.
func seedToolCallNamed(t *testing.T, env *testEnv, userID, sessionID, id, name string) {
	t.Helper()
	ctx := t.Context()
	started := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	if err := env.store.CreateToolCall(ctx, userID, store.ToolCall{
		ID:        id,
		SessionID: sessionID,
		RunID:     "run_csv",
		Name:      name,
		ArgsHash:  "hash-" + id,
		StartedAt: started,
	}); err != nil {
		t.Fatalf("create tool call %s: %v", id, err)
	}
	exit, duration := 0, 1500
	ended := started.Add(time.Second)
	if err := env.store.FinishToolCall(ctx, userID, id, store.ToolCallFinish{
		ExitCode: &exit, DurationMS: &duration, EndedAt: &ended,
	}); err != nil {
		t.Fatalf("finish tool call %s: %v", id, err)
	}
}

// spark is the one bit of a spreadsheet cell that matters here: a leading
// character its evaluator treats as the start of a formula.
func spark(cell string) bool {
	if cell == "" {
		return false
	}
	switch cell[0] {
	case '=', '+', '-', '@', '\t', '\r', '\n':
		return true
	}
	return false
}

func TestAuditCSVNeutralisesFormulaCells(t *testing.T) {
	env := newTestEnv(t)
	env.mkUser("admin@piforge.local", "adminpw", contracts.RoleAdmin)
	admin := env.login("admin@piforge.local", "adminpw")
	dev := env.mkUser("dev@piforge.local", "devpw", contracts.RoleUser)
	_, sess := env.mkSessionFor(dev.ID, "csv")

	// The payload from the GAP: a DDE formula that runs calc.exe on open.
	const payload = `=cmd|' /C calc'!A0`
	seedToolCallNamed(t, env, dev.ID, sess.ID, "tc_calc", payload)
	// One per remaining trigger character, so a fix that special-cases '=' is
	// not enough.
	for i, name := range []string{"+1+1", "-1+1", "@SUM(A1)", "\tcmd", "\rcmd"} {
		seedToolCallNamed(t, env, dev.ID, sess.ID, "tc_trig"+string(rune('a'+i)), name)
	}

	rec := env.doAuthed(http.MethodGet, "/v1/admin/audit?format=csv", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("csv: %d %s", rec.Code, rec.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v", err)
	}
	if len(records) != 7 {
		t.Fatalf("%d CSV rows (header + 6 expected): %v", len(records), records)
	}

	toolColumn := -1
	for i, header := range records[0] {
		if header == "tool" {
			toolColumn = i
		}
	}
	if toolColumn < 0 {
		t.Fatalf("no tool column in %v", records[0])
	}

	seen := map[string]bool{}
	for _, row := range records[1:] {
		if len(row) != len(records[0]) {
			t.Fatalf("row width %d != header %d: %v", len(row), len(records[0]), row)
		}
		for i, cell := range row {
			if spark(cell) {
				t.Fatalf("column %q still begins a formula: %q (row %v)", records[0][i], cell, row)
			}
		}
		seen[row[toolColumn]] = true
	}
	if !seen["'"+payload] {
		t.Fatalf("the tool name was not prefixed with a quote: %q in %v", "'"+payload, seen)
	}
}

// The helper is the whole rule, so its edges are tested directly: every
// trigger character, a value that is already text, and an empty cell.
func TestCSVFormulaSafe(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"bash":             "bash",
		"'=already-quoted": "'=already-quoted",
		"=1+1":             "'=1+1",
		"+1+1":             "'+1+1",
		"-1+1":             "'-1+1",
		"@SUM(A1)":         "'@SUM(A1)",
		"\tcmd":            "'\tcmd",
		"\rcmd":            "'\rcmd",
		"\ncmd":            "'\ncmd",
	}
	for in, want := range cases {
		if got := csvFormulaSafe(in); got != want {
			t.Fatalf("csvFormulaSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
