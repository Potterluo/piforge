package httpapi

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// The operator's audit trail: GET /v1/admin/audit and its verify sibling.
//
// The data is audit_log/audit_runs, not tool_calls: the trail survives the
// session, project and user it describes, records a bounded redacted preview
// instead of a hash alone, and carries a chained hash an operator can verify.
// See docs/AUDIT.md for what is stored, what is redacted, and what the chain
// does and does not prove.
//
// It is not the tenant's transcript: the full arguments stay in the session.
// An operator gets "who ran what, when, with which model and revision, how it
// ended, and how much the run cost", plus enough of the arguments to tell two
// calls apart.

// auditResponse is the JSON shape of one page.
type auditResponse struct {
	Calls []store.AuditRow `json:"calls"`
	// Total is how many calls the filter matches, not how many were returned:
	// the page needs it to say "1–50 of 412".
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	// Filters echoes what was applied, so an empty result is not ambiguous.
	Filters map[string]string `json:"filters"`
}

// handleAdminAudit answers one page of the audit trail, or streams the whole
// filtered set as CSV or JSON-lines when a format is named.
//
// Query parameters, all optional:
//
//	user_id (alias user), project_id (alias project), session_id (alias session)
//	tool                             exact tool name ("bash", "write")
//	outcome                          ok | error | abandoned | running
//	since, until                     RFC 3339 timestamps on started_at
//	min_duration_ms                  only calls at least this slow
//	q (alias search)                 literal substring over the stored preview
//	limit, offset                    page (limit is capped at MaxAuditLimit)
//	format                           json (default) | csv | jsonl
func (s *server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if user.Role != contracts.RoleAdmin {
		s.fail(w, r, apiErr(contracts.ErrForbidden, "admin role required"))
		return
	}
	filter, err := auditFilterFrom(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))) {
	case "csv":
		s.writeAuditCSV(r.Context(), w, filter)
		return
	case "jsonl", "ndjson":
		s.writeAuditJSONL(r.Context(), w, filter)
		return
	}

	rows, total, err := s.deps.Store.AuditToolCalls(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, auditResponse{
		Calls:   rows,
		Total:   total,
		Limit:   filter.Normalised().Limit,
		Offset:  filter.Normalised().Offset,
		Filters: auditFiltersEcho(r),
	})
}

// handleAdminAuditVerify walks the chained hashes and reports the first break.
//
// GET /v1/admin/audit/verify?session_id=…&from_seq=…&to_seq=…&since=…&until=…
// An empty result (checked=0) is not a pass; the caller decides what "nothing
// in this range" means.
func (s *server) handleAdminAuditVerify(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if user.Role != contracts.RoleAdmin {
		s.fail(w, r, apiErr(contracts.ErrForbidden, "admin role required"))
		return
	}
	q := r.URL.Query()
	filter := store.AuditVerifyFilter{
		SessionID: strings.TrimSpace(q.Get("session_id")),
	}
	for _, bound := range []struct {
		name string
		dst  *time.Time
	}{{"since", &filter.Since}, {"until", &filter.Until}} {
		raw := strings.TrimSpace(q.Get(bound.name))
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			s.fail(w, r, apiErr(contracts.ErrInvalidArgument,
				"%s must be an RFC 3339 timestamp, got %q", bound.name, raw))
			return
		}
		*bound.dst = parsed
	}
	for _, bound := range []struct {
		name string
		dst  *int64
	}{{"from_seq", &filter.FromSeq}, {"to_seq", &filter.ToSeq}} {
		raw := strings.TrimSpace(q.Get(bound.name))
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			s.fail(w, r, apiErr(contracts.ErrInvalidArgument,
				"%s must be a non-negative integer, got %q", bound.name, raw))
			return
		}
		*bound.dst = value
	}
	result, err := s.deps.Store.VerifyAuditChain(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// auditFilterFrom parses and validates the query string. A bad timestamp or an
// unknown outcome is a 400 naming the parameter: an operator who typed a filter
// has to be able to tell "no results" from "the filter was ignored".
func auditFilterFrom(r *http.Request) (store.AuditFilter, error) {
	q := r.URL.Query()
	filter := store.AuditFilter{
		UserID:    firstNonEmpty(q.Get("user_id"), q.Get("user")),
		ProjectID: firstNonEmpty(q.Get("project_id"), q.Get("project")),
		SessionID: firstNonEmpty(q.Get("session_id"), q.Get("session")),
		Tool:      strings.TrimSpace(q.Get("tool")),
		Search:    firstNonEmpty(q.Get("q"), q.Get("search")),
	}
	switch strings.ToLower(strings.TrimSpace(q.Get("outcome"))) {
	case "":
	case "ok":
		filter.Status = "ok"
	case "error", "failed":
		filter.Status = "error"
	case "running":
		filter.Running = true
		filter.Status = "running"
	case "abandoned":
		filter.Status = "abandoned"
	default:
		return filter, apiErr(contracts.ErrInvalidArgument,
			"outcome must be ok, error, abandoned or running")
	}
	for _, bound := range []struct {
		name string
		dst  *time.Time
	}{{"since", &filter.Since}, {"until", &filter.Until}} {
		raw := strings.TrimSpace(q.Get(bound.name))
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return filter, apiErr(contracts.ErrInvalidArgument,
				"%s must be an RFC 3339 timestamp, got %q", bound.name, raw)
		}
		*bound.dst = parsed
	}
	if raw := strings.TrimSpace(q.Get("min_duration_ms")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return filter, apiErr(contracts.ErrInvalidArgument,
				"min_duration_ms must be a non-negative integer, got %q", raw)
		}
		filter.MinDurationMS = value
	}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			return filter, apiErr(contracts.ErrInvalidArgument, "limit must be a positive integer, got %q", raw)
		}
		filter.Limit = limit
	}
	if raw := strings.TrimSpace(q.Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return filter, apiErr(contracts.ErrInvalidArgument, "offset must not be negative, got %q", raw)
		}
		filter.Offset = offset
	}
	return filter, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// auditFiltersEcho reports the filters that were understood, so the UI can show
// "filtered by user X" instead of silently showing an empty table.
func auditFiltersEcho(r *http.Request) map[string]string {
	out := map[string]string{}
	for _, key := range []string{
		"user_id", "user", "project_id", "project", "session_id", "session",
		"tool", "outcome", "since", "until", "min_duration_ms", "q", "search",
	} {
		if value := strings.TrimSpace(r.URL.Query().Get(key)); value != "" {
			out[key] = value
		}
	}
	return out
}

// auditCSVHeader is the exported column order. The first 17 columns are the
// order the earlier release shipped: a spreadsheet someone built on it must not
// break because a field moved. New facts are appended.
var auditCSVHeader = []string{
	"started_at", "ended_at", "duration_ms", "outcome", "tool", "namespace",
	"user_id", "user_email", "project_id", "project", "session_id", "session",
	"run_id", "exit_code", "result_bytes", "args_hash", "id",
	"seq", "sandbox_id", "model_ref", "commit_sha", "run_model_ref",
	"run_tokens_in", "run_tokens_out", "run_tokens_cache_read", "run_tokens_cache_write",
	"run_cost_usd", "preview_bytes", "preview_truncated", "args_preview",
	"prev_hash", "hash",
}

// auditCSVStringColumns is every column whose value is text. The numeric
// columns (duration, exit code, bytes, seq, tokens, cost) are excluded: a
// negative exit code is a number and prefixing it would turn it into a string.
var auditCSVStringColumns = []int{
	0, 1, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 15, 16,
	18, 19, 20, 21, 28, 29, 30, 31,
}

// csvFormulaSafe makes one cell inert in a spreadsheet.
//
// The audit export is opened by an operator in Excel, LibreOffice or Sheets,
// and several columns carry text a tenant wrote: the tool name comes from the
// agent's run, the project and session labels from the account, and the preview
// is argument text. A cell whose first character is `=`, `+`, `-`, `@`, TAB or
// CR (OWASP's CSV-injection set) is a formula to those programs, so
// `=cmd|' /C calc'!A0` as a tool name would execute on open — a tenant's text
// running in the operator's spreadsheet.
//
// The mitigation is the one OWASP names first: prefix the cell with a single
// quote. Every one of those readers treats a leading quote as "this cell is
// text" and does not show it, so the value a person reads is unchanged.
func csvFormulaSafe(cell string) string {
	if cell == "" {
		return cell
	}
	switch cell[0] {
	case '=', '+', '-', '@', '\t', '\r', '\n':
		return "'" + cell
	}
	return cell
}

// auditExportFlushRows is how many rows the streaming exporters write between
// flushes. It is the only buffer the export keeps: the rows themselves are
// pulled one at a time from SQLite by StreamAuditCalls.
const auditExportFlushRows = 256

// writeAuditCSV streams every call the filter matches as CSV.
//
// The download is the whole filtered set, not one page, and it is never
// materialised: StreamAuditCalls hands over one row at a time and the writer
// flushes every auditExportFlushRows. A 100k-row export costs a 100k-row
// download, not a 100k-row slice in the process.
func (s *server) writeAuditCSV(ctx context.Context, w http.ResponseWriter, filter store.AuditFilter) {
	filename := "piforge-audit-" + time.Now().UTC().Format("20060102T150405Z") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("X-Audit-Export", "streaming")
	w.WriteHeader(http.StatusOK)

	buffered := bufio.NewWriterSize(w, 32*1024)
	out := csv.NewWriter(buffered)
	_ = out.Write(auditCSVHeader)
	written := 0
	err := s.deps.Store.StreamAuditCalls(ctx, filter, func(row store.AuditRow) error {
		_ = out.Write(auditCSVRecord(row))
		written++
		if written%auditExportFlushRows == 0 {
			out.Flush()
			if err := out.Error(); err != nil {
				return err
			}
			if err := buffered.Flush(); err != nil {
				return err
			}
		}
		return nil
	})
	out.Flush()
	_ = buffered.Flush()
	if err != nil {
		// The status line is already on the wire. The honest thing is to log
		// the truncated export and leave the partial file: a client cannot
		// mistake a 200 for a complete download if the row count is short, and
		// the server log names the cause.
		s.log.WarnContext(ctx, "audit CSV export stopped early",
			"rows_written", written, "error", err.Error())
	}
}

// writeAuditJSONL streams the same rows as JSON-lines, one object per call.
// It is the format a log pipeline ingests without a CSV parser, and it carries
// the preview and the chain fields unchanged.
func (s *server) writeAuditJSONL(ctx context.Context, w http.ResponseWriter, filter store.AuditFilter) {
	filename := "piforge-audit-" + time.Now().UTC().Format("20060102T150405Z") + ".jsonl"
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("X-Audit-Export", "streaming")
	w.WriteHeader(http.StatusOK)

	buffered := bufio.NewWriterSize(w, 32*1024)
	written := 0
	err := s.deps.Store.StreamAuditCalls(ctx, filter, func(row store.AuditRow) error {
		encoded, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if _, err := buffered.Write(append(encoded, '\n')); err != nil {
			return err
		}
		written++
		if written%auditExportFlushRows == 0 {
			if err := buffered.Flush(); err != nil {
				return err
			}
		}
		return nil
	})
	_ = buffered.Flush()
	if err != nil {
		s.log.WarnContext(ctx, "audit JSONL export stopped early",
			"rows_written", written, "error", err.Error())
	}
}

func auditCSVRecord(row store.AuditRow) []string {
	record := make([]string, len(auditCSVHeader))
	record[0] = row.StartedAt.UTC().Format(time.RFC3339)
	record[3] = row.Outcome()
	record[4] = row.Tool
	record[5] = row.Namespace
	record[6] = row.UserID
	record[7] = row.UserEmail
	record[8] = row.ProjectID
	record[9] = row.Project
	record[10] = row.SessionID
	record[11] = row.Session
	record[12] = row.RunID
	record[15] = row.ArgsHash
	record[16] = row.ID
	record[17] = strconv.FormatInt(row.Seq, 10)
	record[18] = row.SandboxID
	record[19] = row.ModelRef
	record[20] = row.CommitSHA
	record[21] = row.RunModelRef
	record[22] = strconv.FormatInt(row.RunTokensIn, 10)
	record[23] = strconv.FormatInt(row.RunTokensOut, 10)
	record[24] = strconv.FormatInt(row.RunTokensCacheR, 10)
	record[25] = strconv.FormatInt(row.RunTokensCacheW, 10)
	record[26] = strconv.FormatFloat(row.RunCostUSD, 'f', -1, 64)
	record[27] = strconv.Itoa(row.PreviewBytes)
	record[28] = strconv.FormatBool(row.PreviewTruncated)
	record[29] = string(row.ArgsPreview)
	record[30] = row.PrevHash
	record[31] = row.Hash
	if row.EndedAt != nil {
		record[1] = row.EndedAt.UTC().Format(time.RFC3339)
	}
	if row.DurationMS != nil {
		record[2] = strconv.Itoa(*row.DurationMS)
	}
	if row.ExitCode != nil {
		record[13] = strconv.Itoa(*row.ExitCode)
	}
	if row.ResultBytes != nil {
		record[14] = strconv.Itoa(*row.ResultBytes)
	}
	// Every text column goes through the spreadsheet rule, including the ones
	// a tenant wrote (tool, project, session, preview). Numeric columns were
	// just formatted and are skipped.
	for _, i := range auditCSVStringColumns {
		record[i] = csvFormulaSafe(record[i])
	}
	return record
}
