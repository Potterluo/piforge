# The forensic audit trail

This document describes what PiForge records about a tool call, why each field
exists, what is deliberately **not** recorded, how the record is queried and
exported, how retention works, and exactly what the chained hash proves — and
what it does not.

It is the reference for `backend/internal/audit`, `backend/internal/store`
(`audit_*.go`, `schema.sql`) and `GET /v1/admin/audit` /
`GET /v1/admin/audit/verify`.

## 1. Why this is not `tool_calls`

`tool_calls` is the transcript's Trace. It is written around the side effect,
shown to the user in their own session, and **cascade-deleted with the session**.
That is the right lifecycle for a transcript and the wrong one for evidence: the
first thing an incident responder does (delete a compromised session or project)
would destroy the record of what happened.

The audit trail is a second, independent record:

| Property | `tool_calls` (transcript) | `audit_log` (forensic) |
| --- | --- | --- |
| Owner | session | operator / deployment |
| Foreign keys | cascade from session, project, user | **none** |
| Lifetime | as long as the session | retention policy (default 90 days) |
| Mutability | updated when the call finishes | append-only: two rows, never updated |
| Arguments | hash + preview | hash + bounded, redacted preview |
| Context | joined at read time | denormalised ids **and** labels at write time |
| Integrity | none | per-row `prev_hash` / `hash` chain |

Both are written by the same call: `Store.CreateToolCall` writes the intent row
and `Store.FinishToolCall` writes the observation row, in the same transaction
as the transcript row. There is no second write site that can forget.

## 2. Tables and fields

### `audit_log` — two append-only rows per tool call

`phase='start'` (intent) and `phase='end'` (observation). The pair is joined by
`(session_id, tool_call_id)`; the primary key is `<tool_call_id>:<phase>`, so a
replayed event is a no-op and cannot fork the chain.

| Column | Meaning |
| --- | --- |
| `id` | `<tool_call_id>:start` or `:end`; deterministic. |
| `seq` | Monotonic per session (`COALESCE(MAX(seq),0)+1` on the single writer connection). Ordering by `(session_id, seq)` reconstructs the order facts were recorded. It is not the same number as `events.seq`. |
| `session_id`, `tool_call_id` | The subject. Kept as values, not foreign keys. |
| `phase` | `start` \| `end`. |
| `recorded_at` | When this row was written (the writer's clock). |
| `started_at` | When the tool call started; identical on both rows. |
| `ended_at` | End row only. |
| `user_id`, `project_id`, `sandbox_id`, `run_id` | Denormalised ids. |
| `user_email`, `project_name`, `session_title` | Denormalised labels **as they were at the call**, so a later rename does not rewrite history and a later delete does not blank it. |
| `tool`, `namespace` | Tool name and MCP namespace (`mcp__server`). |
| `args_hash` | SHA-256 of `tool \x00 raw-args` (kept for correlation with the event stream). |
| `args_preview` | Bounded, redacted JSON object; never the raw arguments (§3). |
| `preview_bytes` | `len(args_preview)`. |
| `args_truncated` | A field, array, object or the total budget was cut. |
| `status` | `running` (start), `ok`, `error`, `abandoned` (end). |
| `is_error`, `exit_code`, `duration_ms`, `result_bytes` | Observation. `exit_code` is the process status Pi reports in the tool result's `details` (`exitCode`/`exit_code`); NULL means "not reported", and `0` is a real value. |
| `model_ref` | The model the agent process was actually configured with (the runtime's `ServedModelRef`, e.g. `piforge/deepseek-v4-flash` — this fills in the deployment default when the session pinned none), falling back to the platform reference and then the session's stored reference. |
| `commit_sha` | The checkout's `HEAD` at the call (`git rev-parse HEAD`, host or sandbox). Empty means unknown — never a guess. |
| `prev_hash`, `hash` | The chain (§6). |

`abandoned` is written by the run-settle path for a call that never reported an
outcome (crash, interrupt, process death). Leaving it `running` forever would
make "still working" indistinguishable from "the process died".

### `audit_runs` — the run-level facts a tool row points at

Token usage and cost are only final when the run settles, after its tool calls.
They live here, keyed by `(run_id, session_id)`, and the admin query joins them
in. The tool rows stay append-only, so their chain does not change when a run
ends.

| Column | Meaning |
| --- | --- |
| `run_id`, `session_id` | The subject; no foreign key for the same reason. |
| `user_id`, `project_id` | Denormalised ids. |
| `model_ref` | The model recorded at run start. |
| `commit_sha` | `HEAD` at run start. |
| `status` | `running` while in flight, then the run's terminal status. |
| `tokens_in`, `tokens_out`, `tokens_cache_r`, `tokens_cache_w`, `cost_usd` | What the provider reported on the settle event. Zero means **not reported**, not "free". |
| `started_at`, `settled_at` | The run's window. |

### `audit_pruned` — why a chain starts mid-sequence

Retention deletes a prefix of a session's rows. Without a marker, "the first
visible row is seq 41" is indistinguishable from "rows 1–40 were deleted by
someone". This table records, per session, the highest sequence retention
removed and when. Verification accepts a mid-chain head only when the watermark
covers the missing prefix (§7).

## 3. The preview and the redaction rule

The preview is an **excerpt**, not the arguments. It exists so an operator can
tell two calls apart ("which file", "which command") without the trail becoming
a second copy of tenant data.

Caps (`backend/internal/audit`):

| Cap | Value |
| --- | --- |
| Whole preview | 2048 bytes |
| One string field | 256 runes, then 128, then 64 before the total cap wins |
| Fields per object (top level and nested) | 16 |
| Array items | 8 |
| Nesting depth | 3 |

Redaction, in order:

1. **Key-shaped names.** The field name is split into lowercase words
   (`api_key`, `apiKey`, `X-Api-Key`, `API-KEY` → `api`, `key`) and matched
   against credential words: `password`, `passwd`, `passphrase`, `secret(s)`,
   `token(s)`, `apikey`, `accesskey`, `secretkey`, `privatekey`, `publickey`,
   `signingkey`, `encryptionkey`, `masterkey`, `credential(s)`,
   `authorization`, `auth`, `cookie(s)`, `bearer`, `jwt`, `clientsecret`,
   `subscriptionkey`, `connectionstring`, `dsn`, `cvv`, `ssn`, `pin`, `key`.
   The whole value becomes `<redacted>` — not a prefix, not a hash.
2. **Key-shaped values.** Any string is scanned for credentials by shape and the
   matching substring becomes `<redacted>`: `sk-…`, `sk-ant-…`, `ghp_`/
   `gho_`/… , `github_pat_…`, `AKIA…`, `xox?-…`, a JWT, `Bearer …`, a PEM private
   key, `SANDBOX_API_KEY=…` (the name is kept; the value is not), and
   `password|secret|token|api_key = <value>` assignments.
3. **File content.** A field whose name is content-shaped (`content`,
   `file_content`, `new_string`, `old_string`, `new_text`, `old_text`, `patch`,
   `diff`, `blob`, `data`, `base64`, `image`, `body`, `text`, `source`) is
   replaced by `{"sha256":"…","bytes":N}`. The path field, if present, stays in
   the preview. **Full file bytes are never stored in the audit trail**; the
   path + size + SHA-256 are the evidence, and the file itself is in the
   checkout/snapshot.

The marker is the same `<redacted>` the sandbox provider already uses for
`SANDBOX_API_KEY` in its logs, so an operator reads one convention across the
product.

What the preview does **not** contain: a token's prefix or suffix, a file's
bytes, a full command line beyond 256 runes, more than 16 argument fields, or
anything from a nested structure deeper than three levels. The full arguments
remain in the session transcript, subject to the session's own lifecycle.

## 4. Query and filters

`GET /v1/admin/audit` (admin only). Every parameter is optional; the filters
compose with AND.

| Parameter | Meaning |
| --- | --- |
| `user_id` (alias `user`) | Exact user id. |
| `project_id` (alias `project`) | Exact project id. |
| `session_id` (alias `session`) | Exact session id. |
| `tool` | Exact tool name (not a substring: "who used shell" has one answer). |
| `outcome` | `ok`, `error`, `abandoned`, `running`. |
| `since`, `until` | RFC 3339 bounds on `started_at` (inclusive). |
| `min_duration_ms` | Calls at least this slow. A running call has no duration and is excluded. |
| `q` (alias `search`) | Case-insensitive **literal** substring over the stored preview. `%` and `_` are characters, not wildcards. It cannot search the full arguments, because they are not here. |
| `limit`, `offset` | Page. `limit` is capped at 500. |
| `format` | `json` (default), `csv`, `jsonl`/`ndjson`. |

The response is `{calls, total, limit, offset, filters}`. `total` counts the
calls the filter matches, not the page, so "1–50 of 412" cannot lie. Each call
is one object assembled from its start and end rows plus the run facts:
`seq`, ids, labels, tool, preview, `status`, `exit_code`, `duration_ms`,
`result_bytes`, `model_ref`, `commit_sha`, `run_tokens_*`, `run_cost_usd`,
`run_model_ref`, `prev_hash`, `hash`.

## 5. Exports and streaming

* `format=csv` — the whole filtered set, comma-separated, header first. The
  first 17 columns keep the order the earlier release shipped; new columns
  (sequence, sandbox, model, commit, usage, preview, chain) are appended so an
  existing spreadsheet keeps working.
* `format=jsonl` — one JSON object per line, same fields, including the preview
  and the chain fields. This is the format a log pipeline ingests.

Both are **streamed**: `Store.StreamAuditCalls` walks SQLite's result set one row
at a time and the handler flushes every 256 rows through a 32 KiB buffer. The
export never materialises the result set; `TestAdminAuditCSVStreamsFiftyThousandRows`
exports 50 000 rows through a bounded writer and asserts both that no single
write exceeds 64 KiB and that the heap does not grow with the row count.

CSV cells that begin with `=`, `+`, `-`, `@`, TAB, CR or LF are prefixed with a
single quote (OWASP CSV injection). The preview is tenant-written text and goes
through the same rule.

If the stream fails mid-export the status line is already `200`; the server logs
`audit CSV/JSONL export stopped early` with the rows written and the error. A
short file is the honest outcome; buffering the whole set to avoid it is not.

## 6. The chained hash

Each `audit_log` row stores:

* `prev_hash` — the `hash` of the previous row **in the same session** (empty for
  the first row);
* `hash` — `SHA-256(prev_hash + "\n" + canonical-json(row-fields) + version)`.

The canonical fields are `audit.ChainFields`: every stored column except
`hash`/`prev_hash`, plus presence flags for the nullable observation columns and
an encoding version. `TestAuditChainCoversEveryStoredColumn` compares the struct
to `PRAGMA table_info(audit_log)` and fails when a column is added without being
covered — an unprotected column would be an edit the chain cannot see.

Order is `(session_id, seq)`. Appending is the only write; a row is never
updated after its hash is computed. That is why a call is two rows instead of
one mutable row.

### What verification reports

`GET /v1/admin/audit/verify?session_id=…&from_seq=…&to_seq=…&since=…&until=…`
(admin only) returns:

```json
{
  "ok": false,
  "checked": 128,
  "sessions": 2,
  "first_break": {
    "session_id": "ses_…", "id": "tc_…:start", "tool_call_id": "tc_…",
    "seq": 41, "reason": "edited",
    "detail": "row content does not match the hash stored with it",
    "expected": "<recomputed>", "actual": "<stored>"
  },
  "anchors": [{"session_id": "ses_…", "seq": 3, "prev_hash": "…", "reason": "retention"}]
}
```

`reason` is one of:

* `edited` — a row's content no longer hashes to its stored `hash`.
* `gap` — a sequence number is missing in the middle of a session.
* `link` — `prev_hash` does not name the previous row's hash.
* `head` — a session's visible range starts mid-chain and no retention
  watermark (or range filter) explains the missing prefix.

`anchors` lists every session whose visible range legitimately starts
mid-chain: `retention` (covered by `audit_pruned`), `range` (the caller chose
`from_seq`/`since`), or `genesis` (seq 1). An empty range returns `checked: 0`
and `ok: true`; the caller must not read that as "the whole trail is intact".

### What the chain proves

1. **A naive edit is detected.** Changing a stored value without recomputing
   the hash breaks `edited` at that row.
2. **A middle-row deletion is detected.** The next row's `seq` jumps (`gap`) and
   its `prev_hash` names a row that is no longer there (`link`).
3. **Reordering or splicing sessions is detected.** Rows are linked within a
   session and ordered by a per-session sequence.
4. **A truncation is explained, not hidden.** Retention leaves a watermark; a
   head deletion without one is reported as `head`.

### What it does not prove

* **It is not a signature.** There is no secret key. Anyone who can write the
  database can rewrite a row, recompute its hash, and rewrite every later
  `prev_hash`/`hash` in that session. The chain detects inconsistency, not an
  adversary with write access to the store. (An HMAC keyed by the deployment's
  secret box would raise that bar; it is not implemented, and pretending the
  current chain has that property would be worse than saying so.)
* **It does not make the trail true.** A writer that lies at insert time
  produces a perfectly valid chain. The chain proves "these are the rows this
  process wrote, in this order", not "these events happened".
* **It does not cover `audit_runs`.** Token/cost rows are joined facts, not
  chained. They are protected by the same access control and the same no-update
  discipline, but an edit to a token count is not detected by `verify`.
* **It does not cover the `tool_calls` transcript**, the session's event log, or
  anything outside `audit_log`.
* **Deleting the first row is only distinguishable because of retention's
  watermark.** A deployment with retention disabled cannot tell "row 1 was
  deleted" from "row 1 never existed" if the rest of the chain is intact.
* **The preview is an excerpt.** It cannot reconstruct the full argument text,
  and redaction intentionally loses information (a key is not recoverable from
  `<redacted>`).

## 7. Retention and immutability

* **Retention** is `-audit-retention-days` / `PIFORGE_AUDIT_RETENTION_DAYS`
  (default **90**; `0` keeps the trail forever; negative is refused at startup).
  The janitor runs the pass at startup and on its interval. It deletes a whole
  tool call — both rows — when the call **started** before the cutoff, and run
  rows that started before it. It never deletes a start row without its end row
  or vice versa. It logs
  `janitor: deleted audit rows older than the retention policy` with
  `days`, `cutoff`, `audit_rows` and `audit_runs`.
* **Immutability** is enforced by the write path, not by a trigger: no code path
  updates or deletes an `audit_log` row except retention (which deletes whole
  calls by policy) and the test-only `ExecForTest`. A deployment that wants a
  hard guarantee should make the database file append-only at the OS level or
  ship rows to an external store; this code does not claim that guarantee.
* **The transcript is separate.** Deleting a session/project/user leaves the
  audit rows, their labels and the run facts. That is tested by
  `TestAuditRowsSurviveSessionAndProjectDeletion`.

## 8. Limits worth knowing

* `commit_sha` is best-effort: with a workspace this process cannot reach (an
  API-only deployment or a detached sandbox whose `git` call fails), it is
  empty. The row still records model and ids.
* `model_ref` is the model Pi was configured with when the runtime reports it
  (`ServedModelRef`, which fills in the deployment default); otherwise the
  session's configured reference. It is recorded per call, so a model switched
  mid-run is reflected by the next call, not retroactively.
* Per-call token counts are **run totals joined at read time**, not the usage of
  that one call. A provider that reports nothing leaves zeroes.
* `sandbox_id` is empty for a session with no sandbox (an ephemeral session).
* A tool call that is still in flight has a `start` row and `status=running`; a
  crash leaves it that way until the run settles, at which point it becomes
  `abandoned`.
* The audit query folds the two rows of every call. On a very large table the
  fold is a full scan of `audit_log` after the indexes narrow it; the exports
  stream it, but the admin page's `total` is a count over the same fold. This is
  the price of keeping rows append-only and complete rather than maintaining a
  mutable projection.
