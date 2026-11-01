package store

// The operator's audit trail (docs/AUDIT.md).
//
// The rows live in `audit_log` and `audit_runs`, not in `tool_calls`. The
// transcript's Trace is a different thing: it cascades away with its session
// and it is what the user reads. The audit trail must survive the subject it
// describes, so it has no foreign keys and is append-only.
//
// The implementation is split by concern:
//
//   - audit_record.go  writes the two rows of one call, the run facts, the
//     abandoned-call settlement and the retention pass.
//   - audit_trail.go   folds a call's rows into one AuditRow and answers the
//     admin query (count, page, stream).
//   - audit_verify.go  walks the chained hashes and reports the first break.
//
// The preview/redaction rules and the chain encoding live in internal/audit so
// the writer and the verifier cannot drift apart.
