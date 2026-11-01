# Data-flow audit: tenant input at every read, fetch, exec and id

Status: first pass, 2026-11-01. 79 edges enumerated, 8 GAPs found, 4 fixed
here, 4 recorded open.

Three adversarial acceptance rounds each found the same class of defect in a
different place: a value a tenant supplies reaching an action the control plane
performs with its own authority — a file the process can read, a URL dialled from
its network position, a process it starts, a row it trusts.

| round | attack | what it reached |
|---|---|---|
| adversarial3 | symlink in a skill repository | `os.ReadFile` followed it: `/proc/1/environ` came back as a skill bundle |
| adversarial4 | MCP server URL | the broker reverse-proxied a sandbox request to the control plane's own loopback |
| adversarial5 | provider `base_url` | the probe POSTed to whatever a tenant saved |

Each was fixed at its site. The pattern was never "which site", so this audit
enumerates the sites: for every endpoint, what a tenant controls, what the
control plane does with it, and which guard (if any) stands between them.

**The rule this audit applies.** A tenant value may reach an action only through
one of four guards:

| guard | question it answers | package |
|---|---|---|
| `tenantfs` | is this path a real file inside a tree the tenant controls? (no links, no escape) | `internal/tenantfs` |
| `urlguard` | may this URL be fetched from this process's network position? | `internal/urlguard` |
| id + ownership | is this row the caller's? (`Store.*` filters on `user_id`; provider ids go through `sandbox.NormalizeSandboxID`) | `internal/store`, `internal/sandbox` |
| argv separation | is a tenant value an *argument*, never part of a command line, and is the binary allow-listed? | `internal/gitx`, `internal/fsx`, call sites |

**Verdicts.** `VERIFIED GUARDED` names the guard. `GAP` gives the concrete
exploit shape. `N/A` says why a tenant cannot reach the edge. `GAP (fixed)`
means this pass added the guard; `GAP (open)` means it is recorded below and was
not changed.

Method: every row was read in the handler and the code it calls. The ambiguous
rows were probed against a throwaway stack (`pwsh scripts/up-isolated.ps1`, port
7723, teardown `-Down`) as the seeded **tenant** account (`dev@`), not as an
admin — `scripts/probe-data-flow.ps1` is that probe, 17 checks, all green after
this pass. Line numbers are as of this commit; the guard test
(`backend/internal/httpapi/guard_registry_test.go`) fails if a guard moves or a
new sink appears unregistered.

---

## 1. Skills

| entry point | tenant value | control plane does | guard | verdict | residual |
|---|---|---|---|---|---|
| `POST /v1/skills/import` — `handleImportSkills` — `skill_import.go:65` | `url` (http/https/git@) | `git clone --depth 1 --single-branch --no-tags` in this process | scheme allow-list; `urlguard` at call (`refuseOutboundURL`, save-time rules too); `gitResolveArgs` pins the name to the verified address (`http.curloptResolve`); clone watchdog kills git at `-skill-clone-max-bytes` | **VERIFIED GUARDED** (`urlguard` + `gitx` argv + cap) | `git@host:path` is judged by host only: the ssh client's keys/known_hosts are the deployment's. A public host is allowed by design. |
| same | `url` (`file://`) | `git clone file:///…` reads a **path on this machine** | `confineLocalImport` → `tenantfs.Contains` against `-skill-import-roots` (default empty = refuse every `file://`) | **GAP (fixed)** | Exploit was: `{"url":"file:///tmp/leakrepro"}` → 200, files returned by `GET /v1/skills/{id}/bundle` (proved on 7723 with marker `LEAK-MARKER-9f3a2b` from a repo only the control plane could see). |
| same | `ref` | `git clone --branch <ref>` | argv element after a value-taking flag, no shell | **VERIFIED GUARDED** (argv separation) | a malformed ref is a git error, not an injection. |
| `POST /v1/skills/import/zip` — `handleImportSkillsZip` — `skill_import.go:269` | archive bytes | `zip.NewReader`, entries written under a temp dir, then walked | `extractSkillZipWithin`: absolute/`..`/non-regular entries refused, declared **and** measured expansion caps, `filepath.Rel` re-check | **VERIFIED GUARDED** | zip members are materialised as regular files only. |
| `GET /v1/skills/{id}/bundle` — `handleReadSkillBundle` — `skill_bundle_handlers.go:214` | `id`, bundle tree | walks and reads the bundle directory | `Store.Skill(user,id)` ownership; `tenantfs.Open` + `WalkFiles` + per-file cap; root is `filepath.Base(bundle_key)` | **VERIFIED GUARDED** (`tenantfs`) | — |
| `PUT /v1/skills/{id}/bundle` — `handleUploadSkillBundle` — `skill_bundle_handlers.go:173` | `id`, file paths | writes `<SkillsDir>/<skill id>/…` | ownership + `scope != admin` check; `bundleFiles` cleans paths, refuses `..`/absolute, **and `.git`** (added); staging dir + rename | **VERIFIED GUARDED**; one GAP fixed | `.git` in a bundle let a tenant turn their own skill directory into a repository, which is the other half of the `file://` read. |
| `POST /v1/skills` — `handleCreateSkill` — `settings_handlers.go:59` | name, body, `scope` | creates a row, then writes the bundle under the generated id | `scope=admin` requires admin; `bundleFiles`; id is generated (`skl_…`), never taken from the body | **VERIFIED GUARDED** | — |
| `PATCH /v1/skills/{id}` — `handlePatchSkill` — `settings_handlers.go:147` | `id` | flips `enabled` | ownership; shared rows need admin | **VERIFIED GUARDED** | — |
| `DELETE /v1/skills/{id}` — `handleDeleteSkill` — `settings_handlers.go:186` | `id` | deletes the row and `os.RemoveAll(SkillsDir/<id>)` | ownership; shared rows need admin; the id is the stored, generated one | **VERIFIED GUARDED** | the removal path relies on the id being generated upstream (it is); no `NormalizeID` call on the way to `RemoveAll`. |
| skill **mount** (provisioning) — `materialize` — `materialize.go:265` | skill name → slug | writes `<worktree>/.pi/skills/<slug>` | `safeJoin` + `slugSkill` (name is not a path) | **VERIFIED GUARDED** (not a route) | — |
| skill **digest** — `materialize.Digest` — `materialize.go:802` | bundle tree | hashes the mounted copy | `walkDigest` refuses symlinks; digest is taken from the mount, not the source | **VERIFIED GUARDED** | — |

## 2. Providers

| entry point | tenant value | control plane does | guard | verdict | residual |
|---|---|---|---|---|---|
| `POST /v1/providers` — `handleCreateProvider` — `provider_handlers.go:82` | `base_url`, `api_key` | stores; probe/gateway later POST to it | `saveOutboundURL` (`CheckSave`); key sealed with `crypto.Box` | **VERIFIED GUARDED** (`urlguard`) | `CheckSave` accepts a name that does not resolve here — deliberate; the call-time check is the boundary. |
| `PATCH /v1/providers/{id}` — `handleUpdateProvider` — `provider_handlers.go:175` | `id`, `base_url` | same | ownership + `saveOutboundURL` | **VERIFIED GUARDED** | — |
| `POST /v1/providers/{id}/test` — `handleTestProvider` — `provider_probe.go:38` | `id` | POSTs a probe to `base_url` | ownership; `refuseOutboundURL` before the probe; `policy.Client` (custom dial + redirect re-validation) | **VERIFIED GUARDED** | — |
| `DELETE /v1/providers/{id}` — `handleDeleteProvider` — `provider_handlers.go:148` | `id` | delete | ownership (404 for a stranger's row) | **VERIFIED GUARDED** | — |
| `/gw/llm/<token>/…` — `llmgw.Handler.ServeHTTP` — `gateway.go:112` | token (held by the agent), path | forwards the model call to `base_url` | token hash + sandbox binding; `urlguard` transport; `upstreamURL` refuses a non-http base | **VERIFIED GUARDED** | the path after the token is forwarded to the provider's own host. |
| `POST/PATCH /v1/admin/providers` — `provider_handlers.go:243,312` | `base_url` | shared provider | handler role check + store `requireAdmin` + `saveOutboundURL` | **VERIFIED GUARDED** | — |

## 3. MCP servers

| entry point | tenant value | control plane does | guard | verdict | residual |
|---|---|---|---|---|---|
| `POST /v1/mcp/servers` — `handleCreateMCPServer` — `settings_handlers.go:263` | `url`, `headers`/`secrets`, `command`/`args`/`cwd` | stores; broker proxies the sandbox's calls to `url` | `saveOutboundURL`; scope forced to `user`; secrets encrypted at rest; stdio refused under isolation (`mcpstage`) | **VERIFIED GUARDED** (`urlguard`) | a stdio command is not executed by the control plane: under isolation it is not staged at all; in host placement the agent is this process's child and the tenant already has a host shell. |
| `/gw/mcp/<token>/…` — `handleMCPGateway` — `mcp_broker.go:67` | token, sub-path, query | reverse-proxies to the stored `url` with the decrypted credential added | token hash; `SessionBinder.AuthorizeSession` (came from the sandbox); `refuseOutboundURL` **per call**; policy transport; `Authorization`/`X-Aio-Api-Key` dropped; `ErrorHandler` maps a dial refusal to 400 | **VERIFIED GUARDED** (`urlguard`) | `rest` is appended to the target's path: a `..` segment stays on the tenant's own MCP host — no control-plane host change, recorded as residual rather than a gap. |
| same | credential blob → header names/values | injects headers upstream | `mcpBrokerHeaders` refuses CR/LF and names with separators | **VERIFIED GUARDED** | — |
| `DELETE /v1/sessions/{id}/mcp-token` — `handleRevokeMCPTokens` — `mcp_broker.go:236` | `id` | revokes the session's broker tokens | ownership | **VERIFIED GUARDED** | — |
| token **staging** — `materialize` writes `.pi/mcp.json` | — | writes only the broker URL + token | no credential is staged; `mcpstage` records what is not staged | **VERIFIED GUARDED** | — |

## 4. Projects

| entry point | tenant value | control plane does | guard | verdict | residual |
|---|---|---|---|---|---|
| `POST /v1/projects` — `handleCreateProject` — `project_handlers.go:169` | `repo_url` | stores; on session creation `git clone` — **on the host** under `-sandbox local`, inside the sandbox under docker/runner | `validateRepoURL` (shape only); **added**: `Local.refuseCloneTarget` — `urlguard` for http(s)/`git@`, and a local path inside another account's sandbox is refused | **GAP (fixed)** | Exploit was: `repo_url: https://127.0.0.1:7700/…` / `git@169.254.169.254:…` accepted (201) and dialled by the control plane in host placement; `repo_url: /data/sandboxes/<other>/repo` cloned another tenant's checkout. Container placements clone inside the tenant's own sandbox, so the value never gets the control plane's network position there — the save stays permissive so the row remains editable, like the no-worktree recovery flow expects. |
| `PATCH /v1/projects/{id}` — `handlePatchProject` — `project_handlers.go:266` | `id`, `repo_url` | same | ownership + `validateRepoURL` + the same clone-time guard | **GAP (fixed)** (same fix) | — |
| `PUT /v1/projects/{id}/forge` — `handleSetProjectForge` — `pr.go:392` | `api_base`, `repo`, token | PR calls fetch `api_base` | `forge.New` shape check; `saveOutboundURL`; `outboundHTTPClient` (dial + redirects) | **VERIFIED GUARDED** (`urlguard`) | `repo` is interpolated into the forge URL path on the tenant's own host — not a cross-host move. |
| project `name` — `handleCreateProject`/`handlePatchProject` | name | stored, rendered in messages | uniqueness only | **N/A** | not a path, URL or argument anywhere. |
| `GET /v1/projects/{id}` — `handleGetProject` — `project_handlers.go:527` | `id` | read | ownership (404) | **VERIFIED GUARDED** | — |
| `DELETE /v1/projects/{id}` — `handleDeleteProject` — `project_handlers.go:324` | `id` | deletes rows and generated directories | ownership; directory names come from stored, generated ids | **VERIFIED GUARDED** | — |

## 5. Sessions

| entry point | tenant value | control plane does | guard | verdict | residual |
|---|---|---|---|---|---|
| `POST /v1/projects/{id}/sessions` — `handleCreateSession` — `project_handlers.go:538` | project `id` | creates the session, then provisions a checkout | ownership before the body is parsed | **VERIFIED GUARDED** | — |
| same | `worktree_id`, `parent_id` | inserts them as foreign keys | **added**: `Store.Worktree(user,id)` / `Store.Session(user,id)` before the insert | **GAP (fixed)** | Exploit was: 201 with another account's `parent_id`/`worktree_id` stored on the row. Every read is scoped by user, so no data followed — the impact was a lying row. Now 404 before the insert. |
| `GET/PATCH/DELETE /v1/sessions/{id}` — `session_handlers.go:104,230,130` | `id` | read/rename/delete | ownership; forks counted before delete | **VERIFIED GUARDED** | — |
| `GET /v1/sessions/{id}/export` — `handleExportSession` — `session_export.go:22` | `id`, `format` | renders the event log into a download | ownership; `format` allow-listed; filename strips CR/LF/quotes | **VERIFIED GUARDED** | — |
| `GET /v1/sessions/{id}/logs` — `handleSessionLogs` — `session_handlers.go:1000` | `id` | returns the runtime's stderr tail | ownership; the log is looked up by session id | **VERIFIED GUARDED** | — |
| `POST /v1/sessions/{id}/messages` — `handlePostMessage` — `message_handlers.go:48` | `text`, `images[]` (base64), `review_comment`, `client_msg_id` | forwards to Pi over RPC; a review comment reads the file it quotes | ownership; images are base64 (no path or URL); the quoted file goes through `Workspace.ReadFile` (`fsx.Resolve`) | **VERIFIED GUARDED** | images are limited by the request size, not by a filesystem cap (no path involved). |
| session **attachments** | — | — | — | **N/A** | there is no attachment endpoint: images are inline base64 and are never opened from disk. |
| `POST /v1/sessions/{id}/fork` / `clone` / `adopt` / `sidecar` / `promote` / `compact` — `session_handlers.go:427,553,636`, `session_clone.go:21`, `branch_adopt.go:23`, `sidecar_promote.go:22` | `id`, `fork_entry_id`, labels | forks Pi's session, copies events, merges a branch | ownership on both ends; `fork_entry_id` is an opaque Pi entry id; adopt uses stored worktree paths | **VERIFIED GUARDED** | a forged `fork_entry_id` is a Pi error, not a path. |
| `GET /v1/sessions/compare` — `handleCompareSessions` — `compare.go:64` | `ids` | loads 2–3 sessions | ownership per id (a stranger's id is 404) | **VERIFIED GUARDED** | — |
| `GET …/entries`, `…/tree`, `…/fork-points` — `session_handlers.go:304,339,369` | `id`, `since` cursor | asks the agent runtime | ownership; the cursor is opaque to Pi | **VERIFIED GUARDED** | — |
| `GET /v1/sessions/{id}/test-runs` — `handleTestRuns` — `test_run_handlers.go:27` | `id` | derived from the event log | `Store.ListTestRuns(user, session)` | **VERIFIED GUARDED** | — |

## 6. Files, artifacts and screenshots

| entry point | tenant value | control plane does | guard | verdict | residual |
|---|---|---|---|---|---|
| `GET /v1/sessions/{id}/files` (+`download`, `inline`) — `handleReadFile` — `workspace_handlers.go:53` | `id`, `path` | reads the file | `rootOrFail` (session→worktree, both scoped); `fsx.Resolve`/`CleanRel` (absolute, `..`, symlink-resolved escape) — probe: 400 `path escapes the workspace` | **VERIFIED GUARDED** | in a detached workspace the read is performed by the sandbox for the sandbox; the confinement is the container. |
| `PUT /v1/sessions/{id}/files` — `handleWriteFile` — `workspace_handlers.go:128` | `id`, `path`, body | writes | same; body capped at 8 MiB | **VERIFIED GUARDED** | `.git` writes are refused by `guardWrite`. |
| `DELETE /v1/sessions/{id}/files` — `handleDeleteFile` — `workspace_handlers.go:156` | `id`, `path` | deletes | same | **VERIFIED GUARDED** | — |
| `GET …/files/tree` — `handleFileTree` — `workspace_handlers.go:27` | `path`, `depth` | lists a directory | `fsx.Resolve`; entries are metadata only, and a link is not followed when read | **VERIFIED GUARDED** | — |
| `GET …/search` — `handleSearch` — `workspace_handlers.go:174` | `q`, `type` | spawns `rg` | `q` is passed as `--regexp=<q>` (one argv element, cannot become an option); `--no-config`; symlinks not followed; result/file caps | **VERIFIED GUARDED** (argv separation) | a pathological regex is bounded by `--max-count`/limits, not refused. |
| `GET /v1/sessions/{id}/artifacts` (+`download`) — `handleArtifactFile` — `artifacts.go:82` | `id`, `path` | serves under `<ArtifactsDir>/<session>` | ownership; `artifactSessionRoot` refuses a separator in the id; `fsx.Resolve` — probe: 400 | **VERIFIED GUARDED** | — |
| `POST …/browser/screenshot` — `handleBrowserScreenshot` — `browser_screenshot.go:35` | session `id`, `tab` | captures and writes a PNG | ownership; the file name is generated (`<ts>-<ms>.png`); the directory is the session's artifact root; `featureDenied` | **VERIFIED GUARDED** | `tab` is passed to CDP, not to a path. |
| `GET …/logs` (same handler as above) | — | — | — | **VERIFIED GUARDED** (see sessions) | — |

## 7. Git

| entry point | tenant value | control plane does | guard | verdict | residual |
|---|---|---|---|---|---|
| `GET …/git/status` — `handleGitStatus` — `workspace_handlers.go:206` | `id` | runs `git status` | `rootOrFail`; `gitx` argv + scrubbed env | **VERIFIED GUARDED** | — |
| `GET …/git/diff` — `handleGitDiff` — `workspace_handlers.go:220` | `scope`, `path`, `commit`, `base`, `from`, `to` | runs `git diff` | `parseDiffScope` allow-list; `cleanRels`; `safeRevArg` | **VERIFIED GUARDED** | a bad revision answers 500 (observed) instead of 400 — see open list. |
| `GET …/git/log` — `handleGitLog` — `workspace_handlers.go:255` | `limit`, `cursor` | runs `git log` | bounds; `safeRevArg` on the cursor | **VERIFIED GUARDED** | — |
| `POST …/git/stage` / `unstage` — `workspace_handlers.go:270,277` | `paths[]` | `git add/reset -- <paths>` | `cleanRels` + `--` separator + argv | **VERIFIED GUARDED** (argv separation) | — |
| `POST …/git/commit` — `handleGitCommit` — `workspace_handlers.go:308` | `message`, `paths[]` | `git commit -m <msg>` | message is a single argv element; paths cleaned | **VERIFIED GUARDED** | — |
| `POST …/git/branch` / `checkout` — `workspace_handlers.go:343,356` | `name` | `git branch -- <name>`, `git checkout <name>` | `safeRevArg` (no leading `-`, no `..`, no whitespace) + `check-ref-format`; probe: `-D` and `../../x` refused | **VERIFIED GUARDED** | the refusal is a 500, not a 400 (open list). |
| `POST …/git/hunk`, `…/discard` — `git_hunks.go:18,70` | `path`, `index` | applies/discards one hunk | `rootOrFail`; `gitx` path cleaning | **VERIFIED GUARDED** | — |
| `GET/POST …/git/remote`, `…/push`, `…/pull` — `git_remote.go:86,42,63` | `id` | `git fetch/push/pull` against the checkout's `origin` | `remoteRoot` ownership; argv; scrubbed env (`GIT_DIR`, `GIT_WORK_TREE`, …) | **VERIFIED GUARDED** for the endpoint | the *origin URL* is repository config: an agent can edit it in-sandbox, and in host placement the push then dials it — open list (same class as `repo_url`, but the tenant has a host shell where it would run). |
| `GET/POST …/git/pr` — `handlePRPreview`/`handleCreatePR` — `pr.go:84,186` | `title`, `body`, `base_branch` | pushes, then calls the forge | ownership; `forgeClient` uses `outboundHTTPClient`; push is the repo's own remote | **VERIFIED GUARDED** | same origin-URL residual as push/pull. |
| branch **worktree** (provisioning) — `provision` / `sandbox.AddWorktree` | branch name | `git worktree add` inside the sandbox | `validateBranchName` + `check-ref-format`; argv | **VERIFIED GUARDED** | — |

## 8. Admin

| entry point | tenant value | control plane does | guard | verdict | residual |
|---|---|---|---|---|---|
| `GET/POST /v1/admin/users`, `PATCH /v1/admin/users/{id}` — `admin_handlers.go:25,46,106` | `id`, role, status, password | reads/writes accounts | handler `RoleAdmin` check **and** `store.requireAdmin`; cannot disable yourself; last admin cannot be demoted | **VERIFIED GUARDED** (role check) | — |
| `GET /v1/admin/users/{id}/features`, `POST …/features` — `feature_handlers.go:29,45` | `id`, `flag` | toggles a flag | role check + `store.requireAdmin` + `KnownFeature` allow-list | **VERIFIED GUARDED** | — |
| `GET /v1/admin/status` — `handleAdminStatus` — `admin_handlers.go:175` | — | counts, walks `DataDir` | role check | **VERIFIED GUARDED** | the walk is over deployment-owned directories only. |
| `GET /v1/admin/audit` — `handleAdminAudit` — `audit_handlers.go:52` | filters (`user_id`, `session_id`, `tool`, `since`, …), `format=csv` | queries `tool_calls`, exports CSV | role check; filters are bound parameters; limit capped | **VERIFIED GUARDED** | a tool name or command beginning with `=`/`+`/`@` lands in a CSV cell an admin may open in a spreadsheet (formula injection). Low, recorded open. |
| `POST /v1/admin/support-access`, `GET …/{id}/transcript` — `support_handlers.go:20,58` | `session_id`, `reason` | reads a session the admin does not own | `store.requireAdmin` inside `CreateSupportAccess`/`SupportAccessGrant`; a reason is mandatory; the grant is (admin, session) and expires; the owner is notified | **VERIFIED GUARDED** (role + grant) | — |
| `GET /v1/support-notices`, `POST …/{id}/seen` — `support_handlers.go:99,116` | `id` | marks a receipt read | user-scoped | **VERIFIED GUARDED** | — |
| admin **seeds** (`PIFORGE_SEED_*`) — `cmd/piforged` | — | creates the first accounts at boot | deployment environment, no request reaches it | **N/A** | — |

## 9. Setup and bootstrap

| entry point | tenant value | control plane does | guard | verdict | residual |
|---|---|---|---|---|---|
| `GET /v1/setup` — `handleSetup` — `setup_handlers.go:45` | — | reports first-run state and the bootstrap prefill | authenticated; the prefill is non-secret by construction (`bootstrap.Defaults`) | **N/A** | — |
| bootstrap provider (`PIFORGE_BOOTSTRAP_PROVIDER*`) — `store.EnsureBootstrapProvider` | — | inserts a shared provider at boot | deployment environment; an existing row is never replaced | **N/A** | — |
| `POST /v1/auth/login`, `GET /v1/auth/me`, `POST /v1/auth/logout` — `auth_handlers.go:21,46,58` | email, password | session + CSRF cookies | parameterised lookup, bcrypt, token digests at rest | **N/A** | credentials are authenticated, not used as paths/URLs/arguments. |

## 10. Sandbox lifecycle

| entry point | tenant value | control plane does | guard | verdict | residual |
|---|---|---|---|---|---|
| `GET /v1/sessions/{id}/sandbox` — `handleSandboxState` — `sandbox_handlers.go:54` | session `id` | reads the sandbox row | `sandboxFor`: session→worktree→sandbox, each `WHERE user_id = ?` | **VERIFIED GUARDED** (ownership) | — |
| `POST …/sandbox/pause` — `handleSandboxPause` — `sandbox_handlers.go:84` | session `id` | pauses the container, stops its agents | same; the provider id comes from the stored row (`sb.ID`), which the provider normalises (`sandbox.NormalizeSandboxID`) | **VERIFIED GUARDED** (id + ownership) | — |
| `POST …/sandbox/resume` — `handleSandboxResume` — `sandbox_handlers.go:123` | session `id` | starts the container, waits for its API | same | **VERIFIED GUARDED** | — |
| `GET …/sandbox/snapshots` — `handleSandboxSnapshots` — `sandbox_handlers.go:182` | session `id` | lists snapshot rows | same; `blob_key` is returned, never dereferenced by the request | **VERIFIED GUARDED** | — |
| `POST …/sandbox/recover` — `handleSandboxRecover` — `sandbox_watch.go:476` | session `id` | repairs a wedged sandbox | same `sandboxFor` ownership; recovery is idempotent per sandbox | **VERIFIED GUARDED** | — |
| sandbox **create/attach/reattach/destroy** — `provision`, `Docker.Ensure`, `reattach`, `reaper`/`janitor` | — | creates containers, starts agents, reclaims disk | internal: no request reaches these with a tenant-shaped id; `NormalizeSandboxID` on every lifecycle call; the boot pass reattaches from stored rows | **N/A** (not reachable) | — |
| `GET /v1/sessions/{id}/pty` — `handleSessionPTY` — `pty_handlers.go:52` | `tab`, `from_offset` | opens a terminal in the sandbox | ownership; `sanitizeTab` (alnum, `-`, `_`, ≤32) + session prefix; origin check | **VERIFIED GUARDED** | — |
| `GET /v1/sessions/{id}/browser` — `handleSessionBrowser` — `browser_handlers.go:41` | `tab` | screencast + input relay | ownership; `featureDenied`; `browserFor` (one Chrome per sandbox) | **VERIFIED GUARDED** | — |
| `POST …/browser/tabs` — `handleOpenBrowserTab` — `browser_tab_handlers.go:79` | `url` | asks Chrome to open a page | ownership; `browser.ValidateNavigationURL` — **scheme only** (`http(s)`/about:blank, no `file:`/`javascript:`/`data:`) | **GAP (open)** | in **host** placement (`-sandbox-browser host`, the default under `-sandbox local`) Chrome is a control-plane process, so navigating it to `http://127.0.0.1:<control port>` or a private address renders that page to the tenant — the same confused-deputy read as the MCP broker. In container placement Chrome is inside the sandbox and the target is the sandbox's own network. Not fixed here: "a dev server on localhost" is the panel's main use, so the fix is a placement/policy decision, not a URL rule. |
| `POST …/browser/screenshot` | see files | — | — | **VERIFIED GUARDED** | — |
| `GET/POST …/browser/mode` — `browser_mode.go:152` | `mode` | observe/takeover | ownership + lease (a second viewer cannot type) | **VERIFIED GUARDED** | — |
| `GET …/browser/tabs`, `DELETE …/tabs/{tab}` — `browser_tab_handlers.go:47,117` | `tab` | CDP calls | ownership; the tab id is matched/compared, never spliced into a URL | **VERIFIED GUARDED** | — |

## 11. GAPs found

| # | edge | exploit shape | status |
|---|---|---|---|
| G1 | `file://` skill import (`skill_import.go:65`) | `POST /v1/skills/import {"url":"file:///tmp/x"}` → 200; the repository's files are returned by `GET /v1/skills/{id}/bundle`. Any git repository this process can read is readable by the tenant; the error text is also a path oracle. Proved on 7723 (marker file). | **fixed** — `confineLocalImport` + `tenantfs.Contains` + `-skill-import-roots` (default: refuse) |
| G2 | `.git` inside a skill bundle (`bundleFiles`) | upload `.git/config` + objects as bundle files, then point G1 at `<SkillsDir>/<id>` — turns the tenant's own bundle directory into a repository, which removes G1's remaining excuse | **fixed** — `.git` segments refused |
| G3 | `worktree_id` / `parent_id` on session create (`project_handlers.go:538`) | 201 with another account's fiducial stored on the row; no read followed (every read is user-scoped), so the impact was a lying row rather than a leak | **fixed** — ownership before the insert |
| G4 | `repo_url` clone target (`project_handlers.go:169`, `sandbox/sandbox.go`) | `https://127.0.0.1:7700/…`, `git@169.254.169.254:…` or `/data/sandboxes/<other>/repo` accepted (201); under `-sandbox local` this process clones it — an SSRF and a cross-tenant read. Container placements clone inside the tenant's sandbox. | **fixed** — `Local.refuseCloneTarget` (urlguard + cross-tenant path refusal) |
| G5 | browser navigation in **host** placement (`browser_tab_handlers.go:79`) | screenshot of `http://127.0.0.1:7700/v1/admin/…` (or any private host) rendered by the control plane's own Chrome | **open** — needs a placement/policy decision (allow-list for host-mode navigation, or run the panel in the sandbox) |
| G6 | `push`/`pull` dial the checkout's `origin` (`git_remote.go:42,63`) | the agent edits `origin` in-sandbox; in host placement the panel's push then dials it | **open** — same placement caveat as G4; the tenant has a host shell there already |
| G7 | git refusals answer 500 (`workspace_handlers.go:343`) | `git/branch {"name":"-D"}` → 500 with `gitx: revision "-D" may not start with '-'` instead of 400 | **open** — cosmetic/API hygiene, the guard holds |
| G8 | audit CSV (`audit_handlers.go`) | a tool name starting with `=` becomes a formula in an admin's spreadsheet | **open** — admin-only, low |

## 12. The guard test

`backend/internal/httpapi/guard_registry_test.go` holds the table above as code:

- `guardedEdges` — for each row, the route pattern, the handler, the file and a
  literal of the guard's use. The test asserts the route is still registered,
  the handler is still declared in that file, and the guard is still called
  there. A deleted route, a moved handler or a removed guard fails.
- `TestEverySinkIsRegistered` — walks the package for the calls that make an
  edge: `os.ReadFile/Open/OpenFile/WriteFile/RemoveAll`, `exec.Command*`,
  `http.NewRequestWithContext`, `.Do(req`, `filepath.Join(s.deps.…)`. A file
  containing one must be a guarded edge or be listed in `declaredSinks` **with a
  reason** (the map is empty today: every sink file is a registered edge). A new
  fetch/read site fails the build before it becomes the next report.

`scripts/probe-data-flow.ps1` is the running-system half: 17 checks that send
what a tenant can send and assert the status the guard produces. It passes on
the throwaway stack. It is not part of `go test` (it needs a deployment) and not
part of `deliver-check.ps1` (that suite is the delivery gate, not this one).

**Limits — what this does not prove.**

1. It checks that the guard is *used in the file*, not that it dominates the
   sink. A handler could call `saveOutboundURL` and then fetch a different URL
   and the test would still pass. Data flow is not analysed.
2. It covers `internal/httpapi`. The guards themselves (`tenantfs`, `urlguard`,
   `gitx`) have their own tests; a sink added in another package that the
   control plane calls (say a new `internal/fetch` helper) is not caught.
3. It assumes routes stay in `router.go`. A handler registered elsewhere would
   only fail the route half if it also left `router.go`.
4. The sink list is a string list. A call reached through a helper
   (`s.someHelper(...)` that reads a file) is invisible unless the helper's own
   file has a sink.
5. It cannot tell a correct guard argument from a wrong one: `tenantfs.Contains`
   called with the wrong root still counts as "uses the guard".

## 13. Reproducing

```powershell
# the running-system half (throwaway stack on 7723; never echoes the drill key)
. .\scripts\drill-key.ps1; $env:PIFORGE_DRILL_KEY = Get-DrillKeyValue
pwsh scripts/up-isolated.ps1 -Port 7723
pwsh scripts/probe-data-flow.ps1
pwsh scripts/up-isolated.ps1 -Down

# the code half
cd backend; go test ./internal/httpapi/ -run 'TestGuardedEdges|TestEverySink' -count=1
go test ./internal/tenantfs/ ./internal/sandbox/ -count=1
```

Two harnesses import over `file://` and therefore declare a root, the way an
operator would: `e2e/playwright.config.ts` passes `-skill-import-roots <tmpdir>`
(that spec stages its repository under the OS temp directory) and
`scripts/deliver-check.ps1` passes `PIFORGE_SKILL_IMPORT_ROOTS=/tmp/drill-imports`
and stages its clone-cap repository there. Everywhere else the empty default
refuses `file://` outright.

## 14. Open list

| # | edge | why it is still open | what a fix looks like |
|---|---|---|---|
| G5 | browser navigation, host placement | refusing loopback/private hosts would remove the panel's main use (a dev server on localhost); the honest fix is placement | either an operator allow-list for host-mode navigation (urlguard-shaped), or make host placement refuse the panel and say so |
| G6 | push/pull to a tenant-set `origin` | same placement caveat; in-sandbox the tenant can run `git push` themselves | route `gitx` fetches through the same policy when the runner is the host |
| G7 | 500 on a refused revision | the guard holds; only the status is wrong | sentinel error from `gitx.safeRevArg`, mapped to 400 in the handlers |
| G8 | CSV formula injection in the audit export | admin-only data, and the cell is a tool name the admin already sees | prefix a cell that starts with `= + - @` with `'` |
| — | `rest` in `/gw/mcp/<token>/…` | stays on the tenant's own MCP host | normalise the sub-path before proxying (belt and braces) |
| — | `repo` in the forge config | interpolation into the tenant's own forge URL path | validate `owner/name` shape in `forge.New` |
| — | residual from the earlier rounds | recorded in `docs/SANDBOX-DEPLOY.md` §1.9 | — |
