package httpapi

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/auth"
	"github.com/piforge/piforge/backend/internal/contracts"
	"github.com/piforge/piforge/backend/internal/store"
)

// The operator's audit trail: GET /v1/admin/audit.
//
// The data is `tool_calls`, which every run already writes around each tool
// ("intent before the side effect, observation after"): who ran what, in which
// session and project, when, and whether it failed. This endpoint is the query an
// admin could not make before — cross-tenant, paginated, filterable and
// exportable — without reading the SQLite file by hand.
//
// It is not a forensic log and says so in the UI: the arguments are a hash plus
// the same short preview the user already sees in their own transcript, so an
// operator can tell *which* command ran but not recover a secret that was never
// stored.

// auditResponse is the JSON shape of one page.
type auditResponse struct {
	Calls []store.AuditRow `json:"calls"`
	// Total is how many rows the filter matches, not how many were returned: the
	// page needs it to say "1–50 of 412".
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	// Filters echoes what was applied, so an empty result is not ambiguous.
	Filters map[string]string `json:"filters"`
}

// handleAdminAudit answers one page of the audit trail, or the same rows as a CSV
// download when format=csv.
//
// Query parameters, all optional:
//
//	user_id, project_id, session_id  exact ids
//	tool                             exact tool name ("bash", "write")
//	outcome                          ok | error | running
//	since, until                     RFC 3339 timestamps on started_at
//	limit, offset                    page (limit is capped at MaxAuditLimit)
//	format                           json (default) | csv
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
	rows, total, err := s.deps.Store.AuditToolCalls(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "csv") {
		s.writeAuditCSV(w, rows)
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

// auditFilterFrom parses and validates the query string. A bad timestamp or an
// unknown outcome is a 400 naming the parameter: an operator who typed a filter
// has to be able to tell "no results" from "the filter was ignored".
func auditFilterFrom(r *http.Request) (store.AuditFilter, error) {
	q := r.URL.Query()
	filter := store.AuditFilter{
		UserID:    strings.TrimSpace(q.Get("user_id")),
		ProjectID: strings.TrimSpace(q.Get("project_id")),
		SessionID: strings.TrimSpace(q.Get("session_id")),
		Tool:      strings.TrimSpace(q.Get("tool")),
	}
	switch strings.ToLower(strings.TrimSpace(q.Get("outcome"))) {
	case "":
	case "ok":
		no := false
		filter.Failed = &no
	case "error", "failed":
		yes := true
		filter.Failed = &yes
	case "running":
		// "running" is "no outcome yet", which the store expresses as an unset
		// ended_at rather than as a boolean.
		filter.Running = true
	default:
		return filter, apiErr(contracts.ErrInvalidArgument, "outcome must be ok, error or running")
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
			return filter, apiErr(contracts.ErrInvalidArgument, "%s must be an RFC 3339 timestamp, got %q", bound.name, raw)
		}
		*bound.dst = parsed
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

// auditFiltersEcho reports the filters that were understood, so the UI can show
// "filtered by user X" instead of silently showing an empty table.
func auditFiltersEcho(r *http.Request) map[string]string {
	out := map[string]string{}
	for _, key := range []string{"user_id", "project_id", "session_id", "tool", "outcome", "since", "until"} {
		if value := strings.TrimSpace(r.URL.Query().Get(key)); value != "" {
			out[key] = value
		}
	}
	return out
}

// auditCSVHeader is the exported column order. It is stable across releases: a
// spreadsheet someone built on it must not break because a field moved.
var auditCSVHeader = []string{
	"started_at", "ended_at", "duration_ms", "outcome", "tool", "namespace",
	"user_id", "user_email", "project_id", "project", "session_id", "session",
	"run_id", "exit_code", "result_bytes", "args_hash", "id",
}

// writeAuditCSV streams one page as CSV.
//
// The download is the *current page*, and the response says how large the total
// is via headers rather than pretending to export the whole table: a filter that
// matches 200k rows is a query problem, and silently materialising it is how an
// admin request takes down a deployment. Raising -limit (up to MaxAuditLimit) or
// narrowing the range is the documented way to get more.
func (s *server) writeAuditCSV(w http.ResponseWriter, rows []store.AuditRow) {
	filename := "piforge-audit-" + time.Now().UTC().Format("20060102T150405Z") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.WriteHeader(http.StatusOK)

	out := csv.NewWriter(w)
	defer out.Flush()
	_ = out.Write(auditCSVHeader)
	for _, row := range rows {
		record := []string{
			row.StartedAt.UTC().Format(time.RFC3339),
			"",
			"",
			row.Outcome(),
			row.Tool,
			row.Namespace,
			row.UserID,
			row.UserEmail,
			row.ProjectID,
			row.Project,
			row.SessionID,
			row.Session,
			row.RunID,
			"",
			"",
			row.ArgsHash,
			row.ID,
		}
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
		_ = out.Write(record)
	}
}
