package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/piforge/piforge/backend/internal/audit"
)

// Chain verification (docs/AUDIT.md).
//
// Each audit_log row stores prev_hash (the previous row's hash in the same
// session) and hash (SHA-256 over prev_hash and the row's own content). A walk
// in (session_id, seq) order detects three things:
//
//	edited   a row's stored hash no longer matches its content
//	gap      a sequence number is missing in the middle of a session
//	link     a row's prev_hash does not name the row before it
//
// It also reports the anchor of every session whose visible range starts
// mid-chain, so a retention prefix (audit_pruned) is not mistaken for a break.
// What it does not prove is written down in docs/AUDIT.md: there is no secret
// key, so an operator with write access to the database can rewrite a row and
// every hash after it.

// AuditVerifyFilter narrows a chain walk. Zero means every row.
type AuditVerifyFilter struct {
	SessionID string
	FromSeq   int64
	ToSeq     int64
	Since     time.Time
	Until     time.Time
}

// AuditChainBreak is the first row that failed verification.
type AuditChainBreak struct {
	SessionID  string `json:"session_id"`
	ID         string `json:"id"`
	ToolCallID string `json:"tool_call_id"`
	Seq        int64  `json:"seq"`
	// Reason is edited|gap|link|head.
	Reason string `json:"reason"`
	Detail string `json:"detail"`
	// Expected and Actual are the two values the check compared, when the
	// check has two (a hash or a sequence).
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
}

// AuditChainAnchor explains why a session's first visible row is not seq 1.
type AuditChainAnchor struct {
	SessionID string `json:"session_id"`
	Seq       int64  `json:"seq"`
	PrevHash  string `json:"prev_hash"`
	// Reason is retention|range|genesis.
	Reason string `json:"reason"`
}

// AuditVerifyResult is the answer to "is this range intact".
type AuditVerifyResult struct {
	OK         bool               `json:"ok"`
	Checked    int                `json:"checked"`
	Sessions   int                `json:"sessions"`
	FirstBreak *AuditChainBreak   `json:"first_break,omitempty"`
	Anchors    []AuditChainAnchor `json:"anchors,omitempty"`
}

// VerifyAuditChain walks the rows the filter selects and reports the first
// break. It keeps one row per session in memory (the chain tail), not the
// range: an operator can verify a year of history without a year of heap.
func (s *Store) VerifyAuditChain(ctx context.Context, filter AuditVerifyFilter) (AuditVerifyResult, error) {
	result := AuditVerifyResult{OK: true}
	if filter.FromSeq < 0 || filter.ToSeq < 0 {
		return result, fmt.Errorf("store: verify audit: sequence bounds must not be negative: %w", ErrInvalid)
	}
	if filter.FromSeq > 0 && filter.ToSeq > 0 && filter.FromSeq > filter.ToSeq {
		return result, fmt.Errorf("store: verify audit: from_seq is after to_seq: %w", ErrInvalid)
	}

	where := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if v := strings.TrimSpace(filter.SessionID); v != "" {
		where = append(where, "session_id = ?")
		args = append(args, v)
	}
	if filter.FromSeq > 0 {
		where = append(where, "seq >= ?")
		args = append(args, filter.FromSeq)
	}
	if filter.ToSeq > 0 {
		where = append(where, "seq <= ?")
		args = append(args, filter.ToSeq)
	}
	if !filter.Since.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, formatTime(filter.Since))
	}
	if !filter.Until.IsZero() {
		where = append(where, "started_at <= ?")
		args = append(args, formatTime(filter.Until))
	}
	query := `SELECT id, seq, session_id, tool_call_id, phase, recorded_at, started_at, ended_at,
	                 user_id, project_id, sandbox_id, run_id, user_email, project_name, session_title,
	                 tool, namespace, args_hash, args_preview, preview_bytes, args_truncated,
	                 status, is_error, exit_code, duration_ms, result_bytes, model_ref, commit_sha,
	                 prev_hash, hash
	            FROM audit_log`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY session_id ASC, seq ASC"

	rows, err := s.r.QueryContext(ctx, query, args...)
	if err != nil {
		return result, fmt.Errorf("store: verify audit: %w", err)
	}
	defer rows.Close()

	// A bounded head anchor is legitimate when a range filter selected it, or
	// when retention wrote a watermark. Anything else is a deleted head.
	rangeStart := filter.FromSeq > 0 || !filter.Since.IsZero()
	watermarks := map[string]int64{}
	current := ""
	var (
		prevHash string
		prevSeq  int64
	)
	for rows.Next() {
		rec, err := scanAuditRecord(rows)
		if err != nil {
			return result, fmt.Errorf("store: verify audit: %w", err)
		}
		result.Checked++
		if rec.SessionID != current {
			current = rec.SessionID
			result.Sessions++
			prevHash, prevSeq = "", 0
			reason := "genesis"
			switch {
			case rec.Seq == 1 && rec.PrevHash != "":
				result.OK = false
				result.FirstBreak = &AuditChainBreak{
					SessionID: rec.SessionID, ID: rec.ID, ToolCallID: rec.ToolCallID,
					Seq: rec.Seq, Reason: "head",
					Detail:   "the first row of the session names a predecessor, but seq 1 exists",
					Expected: "", Actual: rec.PrevHash,
				}
				return result, nil
			case rec.Seq == 1:
				// genesis: prev_hash must be empty (checked above).
			case rangeStart:
				reason = "range"
				result.Anchors = append(result.Anchors, AuditChainAnchor{
					SessionID: rec.SessionID, Seq: rec.Seq, PrevHash: rec.PrevHash, Reason: reason,
				})
			default:
				watermark, covered, err := s.prunedThrough(ctx, watermarks, rec.SessionID)
				if err != nil {
					return result, err
				}
				if !covered || rec.Seq > watermark+1 {
					result.OK = false
					result.FirstBreak = &AuditChainBreak{
						SessionID: rec.SessionID, ID: rec.ID, ToolCallID: rec.ToolCallID,
						Seq: rec.Seq, Reason: "head",
						Detail:   "rows were removed from the start of the session and no retention watermark covers them",
						Expected: fmt.Sprintf("seq 1 or a watermark through %d", rec.Seq-1),
						Actual:   fmt.Sprintf("seq %d", rec.Seq),
					}
					return result, nil
				}
				reason = "retention"
				result.Anchors = append(result.Anchors, AuditChainAnchor{
					SessionID: rec.SessionID, Seq: rec.Seq, PrevHash: rec.PrevHash, Reason: reason,
				})
			}
			// The first visible row is the anchor; its own hash is still checked
			// against its content below, but its prev_hash cannot be compared.
			if want := audit.ChainHash(rec.PrevHash, rec.chainFields()); want != rec.Hash {
				result.OK = false
				result.FirstBreak = &AuditChainBreak{
					SessionID: rec.SessionID, ID: rec.ID, ToolCallID: rec.ToolCallID,
					Seq: rec.Seq, Reason: "edited",
					Detail:   "row content does not match the hash stored with it",
					Expected: want, Actual: rec.Hash,
				}
				return result, nil
			}
			prevHash, prevSeq = rec.Hash, rec.Seq
			continue
		}

		if rec.Seq != prevSeq+1 {
			result.OK = false
			result.FirstBreak = &AuditChainBreak{
				SessionID: rec.SessionID, ID: rec.ID, ToolCallID: rec.ToolCallID,
				Seq: rec.Seq, Reason: "gap",
				Detail:   "a sequence number is missing between two rows",
				Expected: fmt.Sprintf("seq %d", prevSeq+1), Actual: fmt.Sprintf("seq %d", rec.Seq),
			}
			return result, nil
		}
		if rec.PrevHash != prevHash {
			result.OK = false
			result.FirstBreak = &AuditChainBreak{
				SessionID: rec.SessionID, ID: rec.ID, ToolCallID: rec.ToolCallID,
				Seq: rec.Seq, Reason: "link",
				Detail:   "prev_hash does not name the hash of the previous row",
				Expected: prevHash, Actual: rec.PrevHash,
			}
			return result, nil
		}
		if want := audit.ChainHash(rec.PrevHash, rec.chainFields()); want != rec.Hash {
			result.OK = false
			result.FirstBreak = &AuditChainBreak{
				SessionID: rec.SessionID, ID: rec.ID, ToolCallID: rec.ToolCallID,
				Seq: rec.Seq, Reason: "edited",
				Detail:   "row content does not match the hash stored with it",
				Expected: want, Actual: rec.Hash,
			}
			return result, nil
		}
		prevHash, prevSeq = rec.Hash, rec.Seq
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("store: verify audit: %w", err)
	}
	if result.Checked == 0 {
		// An empty range is intact by definition, but saying "verified" over
		// nothing would be a lie of omission. The caller reports checked=0.
		result.OK = true
	}
	return result, nil
}

func (s *Store) prunedThrough(ctx context.Context, cache map[string]int64, sessionID string) (int64, bool, error) {
	if v, ok := cache[sessionID]; ok {
		return v, v > 0, nil
	}
	var through int64
	err := s.r.QueryRowContext(ctx,
		`SELECT pruned_through_seq FROM audit_pruned WHERE session_id = ?`, sessionID).Scan(&through)
	if errors.Is(err, sql.ErrNoRows) {
		cache[sessionID] = 0
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: audit retention watermark for %q: %w", sessionID, err)
	}
	cache[sessionID] = through
	return through, true, nil
}
