# HANDOFF-FINDINGS — from-zero acceptance of the isolated stack

Written 2026-11-01, 18:06–18:22 (+08:00), on Windows 11 / Docker Desktop Engine 29.9.2,
following **only** `docs/HANDOFF.md` — no source, no other docs. Everything below is what
happened, with the exact line from the document and the replacement text I would commit.

Environment: `pf-local` (`7dc14e4a7525…`) stayed up on 7722 the whole time; healthz
`200 {"status":"ok"}` before (18:07:03) and after (18:16:59, 18:21:41) — untouched.

## Headline numbers

| | |
|---|---|
| Time from clean shell to a healthy stack on 7723 | **5.3 s** (`up-isolated.ps1`, images already current) |
| Same, second run, no `PIFORGE_DRILL_KEY` | 4.2 s (exit 0 — see finding 2) |
| Time to a *working* developer loop after that | ~2 min (login → project → session → first prompt) |
| Full session: prompt → commit → panels → terminal → review → fork → stop/continue | ~10 min wall clock |
| Teardown | 3.2 s, `docker ps -a` / `volume ls` / `network ls` clean apart from `pf-local` |

The product worked. The document is what cost time.

---

## Findings, ordered by how much time they cost

### 1. The "Run it" block's key is not defined anywhere (~5 min, the single biggest cost)

Quoted (lines 49–52):

```powershell
$env:PIFORGE_DRILL_KEY='...'
pwsh scripts/up-isolated.ps1
```

There is no sentence in the document that says where that value comes from, what shape it
has, or that it is reused by `pf-local` too. `scripts/drill-key.ps1` exists in the checkout
and is not mentioned once in `HANDOFF.md`. Worse, running it does **not** print the key —
it prints only a fingerprint, so it cannot be the mechanism the quoted line implies:

```
drill key: len=36 sha256=xxxxxxxx...
OK: the key is present and long enough
```

I had to (a) discover the helper's parameters (`Path`, `Name`, `Check`) through
`Get-Command`, (b) discover that dot-sourcing and `-File` both set no variable, (c) take
line 21 of `~/.dsh/.credentials.yaml` (a 36-character token after `refs:`), and (d) confirm
it was the right token by comparing its SHA-256 prefix to the helper's output. Nothing in
the document told me to do any of that, and a wrong key would have failed much later (see
finding 2) rather than at start-up.

Replace the block with:

```powershell
# The provider key is the 36-char token on line 21 of ~/.dsh/.credentials.yaml (under `refs:`).
# Verify it first: this prints only length and SHA-256 prefix, never the key.
pwsh scripts/drill-key.ps1 -Check        # -> drill key: len=36 sha256=xxxxxxxx...
$env:PIFORGE_DRILL_KEY = ((Get-Content "$HOME/.dsh/.credentials.yaml")[20] -split ':',2)[1].Trim()
pwsh scripts/up-isolated.ps1
```

### 2. A stack started without a key comes up "healthy" and answers prompts with silence (~2 min, and it is the trap the doc says it removed)

`up-isolated.ps1` exits **0** with no key and still prints a provider line:

```
provider : volc (volc|https://…/v1|deepseek-v4-flash)
note     : no provider key was supplied; set -ProviderKey (or PIFORGE_DRILL_KEY) for a real prompt,
           or open Settings -> Providers once the stack is up.
```

The stack then accepts `POST …/messages` with `200 {"accepted":true,…}`, runs, and settles
with an **empty** assistant message; only `/status` carries the truth:
`"model_unavailable": true`. `/v1/setup` is honest (`needs_provider:true,
shared_provider_available:false`) but the summary line above says `provider : volc`, which
reads as "configured".

Add after the "Run it" block:

> Without `PIFORGE_DRILL_KEY` (or `-ProviderKey`) the stack still starts and prints
> `provider : volc (…)`. That line names the *bootstrap configuration*, not a usable
> provider. `/v1/setup` then answers `needs_provider:true`, and a prompt is accepted with
> `200` but produces an empty assistant message (`/status` → `"model_unavailable": true`).
> Check `/v1/setup` before blaming the agent.

### 3. No write request in "Use it" documents its CSRF header (~2.5 min)

Quoted (line 439):

```
| Login `POST /v1/auth/login` | **44 ms** (200, returns `csrf` in the body + cookies) |
```

`csrf` is returned but the document never names the header that consumes it. Every write
(`POST /v1/projects`, `/messages`, `/interrupt`, `/fork`, `/adopt`) answers
`403 {"code":"forbidden","message":"missing or invalid CSRF token"}` without it, and
`X-CSRF-Token` — the obvious guess — is not it.

Replace that row with:

```
| Login `POST /v1/auth/login` | **44 ms** (200, body `{user, csrf}`, sets cookies `pf_session` and `pf_csrf`; send the same value back as header **`X-Pf-Csrf`** on every write) |
```

### 4. The document never shows a request body for the three endpoints that need one (client_msg_id, fork, review_comment) (~4 min total)

Quoted (line 443):

```
| Send a message (`POST …/messages`) | **186 ms** to `{accepted:true, disposition:"started"}` |
```

`{"text": "…"}` alone is rejected:

```
400 {"code":"invalid_argument","message":"client_msg_id is required"}
```

Add the schema: `{"text": "<prompt>", "client_msg_id": "<uuid>"}`.

Quoted (line 449):

```
| Fork (`POST …/fork`) | **227 ms** (201, `kind: branch`, own `worktree_id`) |
```

The body is `{"entry_id": "<id from /fork-points>"}` **only**; any extra field is a hard
failure, so the obvious `{"entry_id": …, "title": "…"}` returns
`400 invalid JSON body: json: unknown field "title"`. Say so, and say the body is exactly
one field.

Quoted (lines 469–471):

```
- **Inline review** is server-assembled: the browser posts
  `{path, line, side, hunk, quote, body}` and the server builds the
```

`quote` is an **array of strings**, not a string; a scalar is refused with
`400 … cannot unmarshal string into Go struct field ReviewComment.review_comment.quote of type []string`.
Correct to `{path, line, side, hunk, quote: ["<line text>"], body}`.

### 5. The quoted adopt refusal is not the one a fresh branch produces, and the timing is off by 180× (~1.5 min of "is my fork broken?")

Quoted (line 450):

```
| Adopt (`POST …/adopt` on a fresh branch) | **409 in 10 ms** — "这个分支没有可采纳的改动：既没有领先基准的提交，也没有未提交的文件" |
```

On a freshly forked branch of a session whose worktree still had my uncommitted edit, the
response was 409 in **1 784 ms** with a different reason:

```
目标会话的工作区有未提交的改动：先在目标会话里提交或撤销这些改动，再重试采纳（避免覆盖你正在写的内容）
```

Correct to: "409, message depends on the target's state — either
`这个分支没有可采纳的改动…` (nothing ahead of base, nothing uncommitted) or
`目标会话的工作区有未提交的改动…` (dirty target); measured 1.8 s here, not 10 ms."

### 6. The "Use it" timing table does not describe a first run, and one row is not a user-visible number

The table is introduced as "Measured on this deployment, warm, single user, one browser
tab" (line 434), but nothing tells the operator that the first call in each class pays for
provisioning. Measured here on the same host, same image:

| doc | measured here |
|---|---|
| send `186 ms` | **2 443 ms** (first send: sandbox created) |
| `Fork 227 ms` | **6 314 ms** (branch worktree created) |
| `Adopt 409 in 10 ms` | **1 784 ms** |
| `Terminal typing 40 chars in 34–37 ms` | **37 chars in 1 232 ms** typed in a real browser (per-character echo round trip ≈ 33 ms) |

Add one sentence: "Warm figures; the first message in a session (sandbox provisioning) and
the first fork (worktree creation) take 2–6 s, and 'terminal typing' measures the PTY
round trip, not keystrokes in the browser."

### 7. Two "measured" payloads in the document have fields the server no longer returns (or omit ones it does) (~0 min, but it makes the doc un-greppable)

Quoted (lines 167–168):

```
GET /v1/setup  -> 200 (as admin) {"needs_provider":false,"is_admin":true,
                                  "shared_provider_available":true,"default_provider":"volc"}
```

Actual body also carries a `bootstrap` object
(`{"name":"volc","base_url":"https://…","model":"deepseek-v4-flash"}`).

Quoted (lines 462–463):

```
`{running, streaming, compacting, queue_depth, last_seq, process_id, session_file,
  resolved_model_ref}`
```

Actual `/status` also returns `model_ref` and, on failure, `model_unavailable: true`
(the field that explains finding 2). Add both.

### 8. The login page tells the operator the opposite of the document about seeded passwords (~1 min of hunting)

Quoted (lines 151–153):

```
- **Seeded accounts**: `admin@piforge.local / piforge-admin-123` (admin) and
  `dev@piforge.local / piforge-dev-123` (user). They exist only on a first boot against
  an empty data volume, and the log names them without printing the passwords.
```

The login page itself says (verbatim, 18:08): 「初始密码在首次启动后端时打印到它的日志里
（只打印一次），请向部署同学索取。」 — i.e. "the initial password is printed to the backend
log on first boot". One of the two is wrong, and the operator reads the login page first.
Either fix the banner or add: "the login page still claims the password is in the boot log;
it is not — use the values above."

### 9. The endpoints the panels actually use are undocumented, so none of step 2 can be scripted from the doc

Not wrong, missing. Observed while using the product:

- terminal: `ws://…/v1/sessions/{id}/pty?tab=t1` (binary/UTF-8 frames, char echo round trip)
- diff: `GET /v1/sessions/{id}/git/diff?path=…&scope=unstaged` — `scope` accepts
  `unstaged|staged|branch|commit|range` (`worktree` is a 400)
- tests: `GET /v1/sessions/{id}/test-runs` (not `/tests`), returns
  `{count, runs:[{runner:"node --test", status:"passed", total, passed, …}]}`
- transcript: `GET …/entries` returns `{entries, leaf_id}`; each entry has `preview`,
  `role`, `type`, `extra` — not `text`

Add these five lines to "Use it"; without them the only way to check a panel is a browser.

---

## Did the product do what the document claims?

| Step | Verdict | Evidence |
|---|---|---|
| 1. One command to a stack | **yes** | `up-isolated.ps1` → preflight 6/6 PASS, 3 containers, healthy on 7723 in 5.3 s; key sourcing in the doc is the only failure |
| 2a. Login (seeded accounts) | **yes** | `POST /v1/auth/login` 200 with `csrf`; both accounts exist |
| 2b. "Add a model key if the doc says to" | **n/a** | bootstrap created the shared provider; nothing to add *when the key is exported* (see finding 2 when it is not) |
| 2c. Project + session | **yes** | 201/201, ids `prj_01M6B9AH…`, `ses_01M6B9AH…` |
| 2d. The prompt | **yes** | settled in ~15 s; `primes.js` + `primes.test.js`, commit `14955b6`, output `2 3 5 7 11 13 17 19 23 29` |
| 2e. Test panel | **yes** | `/test-runs`: runner `node --test`, `status:"passed"`, 2/2, 94 ms; UI badge `测试 2` |
| 2f. Changes panel | **yes** | clean right after the commit; after `echo … >> primes.js` from the terminal it showed `1 个改动 +1 -0`, file `primes.js 未暂存` |
| 2g. Terminal in the container | **yes** | `hostname` → `8dee4a3f80ef` = the sandbox container id (`docker inspect` agrees), not the control plane |
| 2h. Inline comment | **yes** | server composed `行内评论 · primes.js:12 … 引用的代码：12 \| … 评论：… 位置：…` and the agent answered it |
| 2i. Fork | **yes** | 201, `kind:"branch"`, own `worktree_id wt_01M6B9QT…` |
| 2j. Adopt | **yes, different message** | 409 as claimed, but a different reason string and 1.8 s (finding 5) |
| 2k. Stop/continue | **yes** | `POST …/interrupt` **7 ms**, `{cleared_steering:[],cleared_follow_up:[]}`; the next message carried `[上一轮已被用户中断并作废…]` and the answer was exactly `beta` |
| 3. Disk quota | **yes, exactly as documented** | in the sandbox terminal: `ulimit -f` → `2097152`; `fallocate -l 3G /workspace/big.bin` → `File size limit exceeded (core dumped)`, `rc=153` (128+SIGXFSZ), file `0` bytes; `/v1/admin/status` → `quota_bytes 2147483648, over false` |
| 4. Teardown | **yes** | `-Down` 3.2 s, exit 0; afterwards no `piforge*`/`pf-isolated*` container, volume or network — only the pre-existing `pf-local` + `pf-local-data` |
| 5. `pf-local` untouched | **yes** | id `7dc14e4a7525…` and `healthz 200 {"status":"ok"}` identical before and after |

Overall: **partly** — the product's claims held in every step I could measure; the
*document* was wrong or silent in the nine places above, one of which (the key) is on the
critical path and two of which (CSRF, request bodies) block anyone driving the API instead
of the UI.

## Verdict for the next operator (read this before you start)

The stack is real and it does what the document says it does: one PowerShell command brings
up control plane + runner + sandbox on 7723, a seeded dev account logs in, the agent writes
and tests and commits inside a container whose hostname is *not* the control plane's,
`fallocate` really dies at 2 GiB, `interrupt` really returns in 7 ms, and `-Down` leaves
nothing behind except the images. Budget one minute for the stack and half an hour for a
full first pass. But do not trust the document as an executable spec: it never says where
`PIFORGE_DRILL_KEY` comes from (line 21 of `~/.dsh/.credentials.yaml`, verified with
`scripts/drill-key.ps1 -Check`, never echoed), it never names the `X-Pf-Csrf` header that
every write needs, it omits `client_msg_id`, the fork body and the fact that
`review_comment.quote` is an array, its "Use it" timings are warm-only and one row measures
the PTY rather than the user, and a stack started without a key looks configured and then
answers prompts with an empty message (`/status` → `model_unavailable:true`). Treat the
document as a map with the street names missing, not as a wrong map: every landmark it
describes exists where it says it is; you will just have to ask for directions at the
first three corners.

## How this was measured (reproducible)

```powershell
# baseline
docker inspect -f '{{.Id}}' pf-local                 # 7dc14e4a7525…
(Invoke-WebRequest http://127.0.0.1:7722/healthz).Content   # {"status":"ok"}

# up (documented command, key from ~/.dsh/.credentials.yaml line 21, fingerprint-checked)
$env:PIFORGE_DRILL_KEY = ((Get-Content "$HOME/.dsh/.credentials.yaml")[20] -split ':',2)[1].Trim()
pwsh scripts/up-isolated.ps1                          # 5.3 s, exit 0, http://127.0.0.1:7723

# API, from a clean shell: login -> X-Pf-Csrf -> projects -> sessions -> messages
# UI: Playwright/Chromium against the same URL for the panels and the PTY

# quota, typed in the sessions PTY (not docker exec)
#   ulimit -f; fallocate -l 3G /workspace/big.bin; echo rc=$?; ls -l /workspace/big.bin

# down
pwsh scripts/up-isolated.ps1 -Down                     # 3.2 s
docker ps -a; docker volume ls; docker network ls      # filtered: only pf-local / pf-local-data
```
