# Data-flow audit: tenant input at every read, fetch, exec and id

Status: 2026-11-01 (fourth pass). 79 edges enumerated, 13 GAPs found, all 13
fixed — G1–G4 in the first pass, G5–G8 in the second, G9–G13 in this one. G5 is a
policy with a stated tradeoff rather than a blanket refusal; the rule and what it
costs are in §10.1.

The fourth pass exists because a fifth adversarial round attacked the fourth's
fixes and found that every one of them described *argv*. Git is also a command
interpreter (`.git/config` is tenant-writable and `core.fsmonitor` ran through a
`GET /git/status`), and a second HTTP client (`git clone` of a public URL that
302s to `127.0.0.1` fetched the control plane's own loopback). G9–G13 are that
round: the same class of defect, one layer below the code this audit had read.

Three adversarial acceptance rounds each found the same class of defect in a
different place: a value a tenant supplies reaching an action the control plane
performs with its own authority — a file the process can read, a URL dialled from
its network position, a process it starts, a row it trusts.

| round | attack | what it reached |
|---|---|---|
| adversarial3 | symlink in a skill repository | `os.ReadFile` followed it: `/proc/1/environ` came back as a skill bundle |
| adversarial4 | MCP server URL | the broker reverse-proxied a sandbox request to the control plane's own loopback |
| adversarial5 | provider `base_url` | the probe POSTed to whatever a tenant saved |
| adversarial5 (git/paths) | `core.fsmonitor`, `core.sshCommand`, a 302 in `git clone`, a junction, `.git.`, a `user@host:path` | it ran a command, dialled loopback twice, and read a repository outside an import root |

Each was fixed at its site. The pattern was never "which site", so this audit
enumerates the sites: for every endpoint, what a tenant controls, what the
control plane does with it, and which guard (if any) stands between them.

**The rule this audit applies.** A tenant value may reach an action only through
one of four guards:

| guard | question it answers | package |
|---|---|---|
| `tenantfs` | is this path a real file inside a tree the tenant controls? (no links, no junctions, no escape) | `internal/tenantfs` |
| `urlguard` | may this URL be fetched from this process's network position? | `internal/urlguard` |
| id + ownership | is this row the caller's? (`Store.*` filters on `user_id`; provider ids go through `sandbox.NormalizeSandboxID`) | `internal/store`, `internal/sandbox` |
| argv separation | is a tenant value an *argument*, never part of a command line, and is the binary allow-listed? | `internal/gitx`, `internal/fsx`, call sites |
| command-scope config | when the argument *is* git, what will git execute or dial on its own? | `internal/gitx` harden.go |

**Verdicts.** `VERIFIED GUARDED` names the guard. `GAP` gives the concrete
exploit shape. `N/A` says why a tenant cannot reach the edge. `GAP (fixed)`
means a pass added the guard; `GAP (open)` means it is recorded below and was
not changed.

Method: every row was read in the handler and the code it calls. The ambiguous
rows were probed against a throwaway stack (`pwsh scripts/up-isolated.ps1
-Rebuild`, port 7723, teardown `-Down`) as the seeded **tenant** account
(`dev@`), not as an admin — `scripts/probe-data-flow.ps1` is that probe, 18
checks, all green after this pass, and it prints "N of M" and exits 2 if it could
not measure an edge (the previous version skipped four checks silently on a
fresh stack and a round of this audit reported 15 of the 17 it claimed). Line
numbers are as of this commit; the guard test
(`backend/internal/httpapi/guard_registry_test.go`) fails if a guard moves or a
new sink appears unregistered.

---

## 1. Skills

| entry point | tenant value | control plane does | guard | verdict | residual |
|---|---|---|---|---|---|
| `POST /v1/skills/import` — `handleImportSkills` — `skill_import.go:65` | `url` (http/https/git@) | `git clone --depth 1 --single-branch --no-tags` in this process | scheme allow-list; `urlguard` at call (`refuseOutboundURL`, save-time rules too); `gitResolveArgs` pins the name to the verified address (`http.curloptResolve`); clone watchdog kills git at `-skill-clone-max-bytes`; **added**: the clone runs with the deployment's command-scope config, including `http.followRedirects=false` and `GIT_HTTP_FOLLOW_REDIRECTS=0`, so a 302 is a refusal (G10) | **GAP (fixed)** (`urlguard` + command-scope config + cap) | `git@host:path` is judged by host only: the ssh client's keys/known_hosts are the deployment's. A public host is allowed by design, and an ssh remote keeps working with the deployment's own `ssh` (G9). |
| same | `url` (`file://`) | `git clone file:///…` reads a **path on this machine** | `importURL` → `tenantfs.Resolve` against `-skill-import-roots` (default empty = refuse every `file://`); the **resolved** path is what git clones, and every component's reparse point is refused | **GAP (fixed)** | Exploit was: `{"url":"file:///tmp/leakrepro"}` → 200 (G1); then a junction inside an allowed root → the outside repository came back (G11), and a junction flipped during the import leaked 2 times in 6, 14 times in 87 on the drill's machine. Now: a junction is outside the root (platform handle, not `EvalSymlinks`), and the clone names the resolved directory, so a later flip changes nothing. Residual: a bind mount is not a reparse point (POSIX), and the resolved path's own components are only as stable as the deployment's disk. |
| same | `ref` | `git clone --branch <ref>` | argv element after a value-taking flag, no shell | **VERIFIED GUARDED** (argv separation) | a malformed ref is a git error, not an injection. |
| `POST /v1/skills/import/zip` — `handleImportSkillsZip` — `skill_import.go:269` | archive bytes | `zip.NewReader`, entries written under a temp dir, then walked | `extractSkillZipWithin`: absolute/`..`/non-regular entries refused, declared **and** measured expansion caps, `filepath.Rel` re-check | **VERIFIED GUARDED** | zip members are materialised as regular files only. |
| `GET /v1/skills/{id}/bundle` — `handleReadSkillBundle` — `skill_bundle_handlers.go:214` | `id`, bundle tree | walks and reads the bundle directory | `Store.Skill(user,id)` ownership; `tenantfs.Open` + `WalkFiles` + per-file cap; root is `filepath.Base(bundle_key)` | **VERIFIED GUARDED** (`tenantfs`) | — |
| `PUT /v1/skills/{id}/bundle` — `handleUploadSkillBundle` — `skill_bundle_handlers.go:173` | `id`, file paths | writes `<SkillsDir>/<skill id>/…` | ownership + `scope != admin` check; `bundleFiles` cleans paths, refuses `..`/absolute, refuses `.git` and every segment Windows would rewrite (`TrailingDot`, trailing space), staging dir + rename | **VERIFIED GUARDED**; two GAPs fixed | `.git` in a bundle let a tenant turn their own skill directory into a repository, which is the other half of the `file://` read (G2); `.git./config` was the same directory after Win32 stripped the dot, and it landed as `.git\config` (G11). |
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
| `POST /v1/projects` — `handleCreateProject` — `project_handlers.go:169` | `repo_url` | stores; on session creation `git clone` — **on the host** under `-sandbox local`, inside the sandbox under docker/runner | `validateRepoURL` (shape only); **added**: `Local.refuseCloneTarget` → the same `refuseTarget` the push guard uses — `urlguard` for http(s), ssh, git:// **and every scp-like `[user@]host:path`**, judged on the transport's own port (22 for ssh), plus the cross-tenant local path rule | **GAP (fixed)** | Exploit was: `repo_url: https://127.0.0.1:7700/…` / `git@169.254.169.254:…` accepted (201) and dialled by the control plane in host placement; `repo_url: /data/sandboxes/<other>/repo` cloned another tenant's checkout (G4); `root@127.0.0.1:acme/repo.git` was treated as a *local path* and dialled `127.0.0.1:22` (G12). Container placements clone inside the tenant's own sandbox, so the value never gets the control plane's network position there — the save stays permissive so the row remains editable, like the no-worktree recovery flow expects. |
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
| `GET …/git/status` — `handleGitStatus` — `workspace_handlers.go:206` | `id` | runs `git status` | `rootOrFail`; `gitx` argv **and the deployment's command-scope config** (`core.fsmonitor=false`, `core.hooksPath=<empty dir>`, `core.sshCommand=<resolved ssh>`, `credential.helper=`, `core.pager=`, `commit.gpgSign=false`, `protocol.ext.allow=never`, `protocol.file.allow=user`, `http.followRedirects=false`) with every inherited `GIT_*` variable dropped | **GAP (fixed)** (G9) | This row used to say "VERIFIED GUARDED (`gitx` argv + scrubbed env)" and that was an overclaim: argv separation describes how the arguments are passed, not what git does with the repository. `core.fsmonitor` in the checkout's `.git/config` ran a command through this route (marker `FSMONAPI`). Command scope outranks every config file, and the marker no longer appears. |
| `GET …/git/diff` — `handleGitDiff` — `workspace_handlers.go:220` | `scope`, `path`, `commit`, `base`, `from`, `to` | runs `git diff` | `parseDiffScope` allow-list; `cleanRels`; `safeRevArg` | **VERIFIED GUARDED** | a refused revision used to answer 500; it is a typed 400 now (G7). |
| `GET …/git/log` — `handleGitLog` — `workspace_handlers.go:255` | `limit`, `cursor` | runs `git log` | bounds; `safeRevArg` on the cursor | **VERIFIED GUARDED** | — |
| `POST …/git/stage` / `unstage` — `workspace_handlers.go:270,277` | `paths[]` | `git add/reset -- <paths>` | `cleanRels` + `--` separator + argv | **VERIFIED GUARDED** (argv separation) | — |
| `POST …/git/commit` — `handleGitCommit` — `workspace_handlers.go:308` | `message`, `paths[]` | `git commit -m <msg>` | message is a single argv element; paths cleaned | **VERIFIED GUARDED** | — |
| `POST …/git/branch` / `checkout` — `workspace_handlers.go:343,356` | `name` | `git branch -- <name>`, `git checkout <name>` | `safeRevArg` (no leading `-`, no `..`, no whitespace) + `check-ref-format`; probe: `-D` and `../../x` refused | **VERIFIED GUARDED** | the refusal is a typed 400, not a 500 (G7), and every gitx syntactic refusal maps the same way. |
| `POST …/git/hunk`, `…/discard` — `git_hunks.go:18,70` | `path`, `index` | applies/discards one hunk | `rootOrFail`; `gitx` path cleaning | **VERIFIED GUARDED** | — |
| `GET/POST …/git/remote`, `…/push`, `…/pull` — `git_remote.go:86,42,63` | `id` | `git fetch/push/pull` against the checkout's `origin` | `remoteRoot` ownership; argv; the command-scope config above; **added**: before any push/pull/fetch, `Deps.RemoteCheck` reads every `remote.*.url`, `remote.*.pushurl`, `url.<base>.insteadOf` **and `url.<base>.pushInsteadOf`** (`gitx.RemoteTargets`), applies `urlguard` (http(s), ssh, git://, and the scp-like `[user@]host:path` form with or without a user) plus the cross-tenant local-path rule, **and refuses repository config keys that name a program** (`core.sshCommand`, `core.fsmonitor`, `remote.*.proxy|uploadpack|receivepack`, `credential.helper`, `filter.*`, `diff.*.textconv`, …) | **GAP (fixed)** (G13) | The guard is wired only for a *host* workspace (`cmd/piforged`): a container placement runs git inside the sandbox, where the tenant already has the network position. The operator's `-url-allow-file` is the documented way to keep a legitimate internal remote. `pushInsteadOf` and the scp form without a user were unread and reached loopback (findings 7 and 9); `remote.*.proxy` and `core.sshCommand` were unread and dialled or executed (findings 8 and 5c). |
| `GET/POST …/git/pr` — `handlePRPreview`/`handleCreatePR` — `pr.go:84,186` | `title`, `body`, `base_branch` | pushes, then calls the forge | ownership; `forgeClient` uses `outboundHTTPClient`; push is the repo's own remote; **added**: the same remote check before the push | **VERIFIED GUARDED** | the PR push dials the same origin the push button does, so it passes the same boundary (G6). |
| branch **worktree** (provisioning) — `provision` / `sandbox.AddWorktree` | branch name | `git worktree add` inside the sandbox | `validateBranchName` + `check-ref-format`; argv | **VERIFIED GUARDED** | — |

## 8. Admin

| entry point | tenant value | control plane does | guard | verdict | residual |
|---|---|---|---|---|---|
| `GET/POST /v1/admin/users`, `PATCH /v1/admin/users/{id}` — `admin_handlers.go:25,46,106` | `id`, role, status, password | reads/writes accounts | handler `RoleAdmin` check **and** `store.requireAdmin`; cannot disable yourself; last admin cannot be demoted | **VERIFIED GUARDED** (role check) | — |
| `GET /v1/admin/users/{id}/features`, `POST …/features` — `feature_handlers.go:29,45` | `id`, `flag` | toggles a flag | role check + `store.requireAdmin` + `KnownFeature` allow-list | **VERIFIED GUARDED** | — |
| `GET /v1/admin/status` — `handleAdminStatus` — `admin_handlers.go:175` | — | counts, walks `DataDir` | role check | **VERIFIED GUARDED** | the walk is over deployment-owned directories only. |
| `GET /v1/admin/audit` — `handleAdminAudit` — `audit_handlers.go:52` | filters (`user_id`, `session_id`, `tool`, `since`, …), `format=csv` | queries `tool_calls`, exports CSV | role check; filters are bound parameters; limit capped; **added**: every string column passes `csvFormulaSafe`, which prefixes a cell beginning with `=`, `+`, `-`, `@`, TAB or CR with a single quote (OWASP) | **VERIFIED GUARDED** (G8) | the numeric columns (`duration_ms`, `exit_code`, `result_bytes`) are deliberately not prefixed: a negative exit code is a number, not a formula. |
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
| `POST …/browser/tabs` — `handleOpenBrowserTab` — `browser_tab_handlers.go:79` | `url` | asks Chrome to open a page | ownership; `browser.ValidateNavigationURL` (scheme: http(s)/about:blank only); **added**: in host placement, `urlguard.CheckBrowserNavigation` — loopback only on a published port, never the control port, link-local/metadata/private/docker names refused | **GAP (fixed)** (G5) | container placement relies on the sandbox's own loopback and runs no rule. The rule and its cost are in §10.1. |
| `POST …/browser/screenshot` | see files | — | — | **VERIFIED GUARDED** | — |
| `GET/POST …/browser/mode` — `browser_mode.go:152` | `mode` | observe/takeover | ownership + lease (a second viewer cannot type) | **VERIFIED GUARDED** | — |
| `GET …/browser/tabs`, `DELETE …/tabs/{tab}` — `browser_tab_handlers.go:47,117` | `tab` | CDP calls | ownership; the tab id is matched/compared, never spliced into a URL | **VERIFIED GUARDED** | — |

### 10.1 The browser navigation rule (G5)

In **host** placement (`-sandbox-browser host`, the default under `-sandbox
local`) Chrome is a child of this process: a page it renders is fetched from the
control plane's network position and painted back to the tenant. Before G5 was
fixed the only rule was the scheme, so
`POST …/browser/tabs {"url":"http://127.0.0.1:7700/v1/admin/…"}` put the control
plane's own UI in front of a tenant's screenshot.

The rule, implemented in `urlguard.CheckBrowserNavigation` and called from
`handleOpenBrowserTab` through `Deps.BrowserNavigation`:

| target | verdict |
|---|---|
| `http(s)` public address | allowed — a public page is not this process's authority |
| loopback (`127.0.0.0/8`, `::1`, `localhost`, `*.localhost`) on a port the deployment published | allowed — the panel's dev-server / UI use |
| loopback on any other port | refused (`loopback`): "not one this deployment published for the session's sandbox or dev server" |
| the control plane's own listener port | refused (`control-port`), **even when it appears in the published list** |
| link-local (169.254/16, where cloud metadata lives), private (10/8, 172.16/12, 192.168/16), unique-local, CGNAT, documentation, reserved | refused, naming the range |
| `host.docker.internal`, the compose service, this container's hostname, `*.internal` / `*.local` | refused |
| `file:`, `javascript:`, `data:`, anything not http(s) | refused (scheme) |
| an operator `-url-allow-file` entry for the host and port | allowed — a deployment rule, not a tenant setting |

The published set is `-browser-localhost-ports` /
`PIFORGE_BROWSER_LOCALHOST_PORTS` (comma-separated), plus the loopback port of
`-web-upstream` when the deployment serves its own UI (the image starts Next on
`127.0.0.1:3100` and proxies it). The flag is **empty by default**, which means no
loopback navigation at all beyond the UI the deployment itself serves. Container placement (`browser=remote`)
runs no rule: there Chrome is inside the sandbox, `127.0.0.1` is the sandbox's
own loopback, and the tenant already has it.

**The tradeoff, stated.** A dev server on localhost is the panel's main use, and
naming *ports* instead of hosts means the deployment has to say which ports it
published. An agent-started dev server on an undeclared port is therefore not
viewable in host placement: the refusal names the published set, and the two
honest answers are "declare the port" (`-browser-localhost-ports`) or "run a
container placement", where the whole question disappears. The alternative —
allowing loopback because "the panel needs a dev server" — hands a tenant a
screenshot of the control plane, which is worse than a refused tab.

Two harnesses would otherwise have broken, and both now declare what they
publish instead of the rule being weakened:

- `e2e/playwright.config.ts` runs host placement and passes
  `-browser-localhost-ports 3100,3101`: the UI it serves on 3100, and the
  fixture page `72-observe-input` binds on 3101 (it used an ephemeral port
  before, which no deployment can publish in advance). The control plane's own
  8080 stays refused.
- The delivery drill drives the browser inside the container
  (`docs/DELIVERY-DRILL.md`), so it is `remote` placement and unaffected.

`26-browser-tabs` opens `https://93.184.216.34/second` rather than
`https://example.com/second`: the rule resolves a name, and this suite runs
offline, where a name is an unrelated DNS refusal. A public literal is still a
public page.

## 11. GAPs found

| # | edge | exploit shape | status |
|---|---|---|---|
| G1 | `file://` skill import (`skill_import.go:65`) | `POST /v1/skills/import {"url":"file:///tmp/x"}` → 200; the repository's files are returned by `GET /v1/skills/{id}/bundle`. Any git repository this process can read is readable by the tenant; the error text is also a path oracle. Proved on 7723 (marker file). | **fixed** — `confineLocalImport` + `tenantfs.Contains` + `-skill-import-roots` (default: refuse) |
| G2 | `.git` inside a skill bundle (`bundleFiles`) | upload `.git/config` + objects as bundle files, then point G1 at `<SkillsDir>/<id>` — turns the tenant's own bundle directory into a repository, which removes G1's remaining excuse | **fixed** — `.git` segments refused |
| G3 | `worktree_id` / `parent_id` on session create (`project_handlers.go:538`) | 201 with another account's fiducial stored on the row; no read followed (every read is user-scoped), so the impact was a lying row rather than a leak | **fixed** — ownership before the insert |
| G4 | `repo_url` clone target (`project_handlers.go:169`, `sandbox/sandbox.go`) | `https://127.0.0.1:7700/…`, `git@169.254.169.254:…` or `/data/sandboxes/<other>/repo` accepted (201); under `-sandbox local` this process clones it — an SSRF and a cross-tenant read. Container placements clone inside the tenant's sandbox. | **fixed** — `Local.refuseCloneTarget` (urlguard + cross-tenant path refusal) |
| G5 | browser navigation in **host** placement (`browser_tab_handlers.go:79`) | screenshot of `http://127.0.0.1:7700/v1/admin/…` (or any private host) rendered by the control plane's own Chrome | **fixed** — `urlguard.CheckBrowserNavigation`: loopback only on a published port, the control port never, link-local/metadata/private/docker names refused. Rule and tradeoff in §10.1. |
| G6 | `push`/`pull` dial the checkout's `origin` (`git_remote.go:42,63`) | the agent edits `origin` in-sandbox (or adds a `pushurl`, or a `url.<base>.insteadOf` rewrite); in host placement the panel's push then dials it | **fixed** — `gitx.RemoteTargets` + `sandbox.RefuseRemoteTarget` + `Deps.RemoteCheck`, wired for a host workspace; the operator allow-list keeps a legitimate internal remote reachable |
| G7 | git refusals answer 500 (`workspace_handlers.go:343`) | `git/branch {"name":"-D"}` → 500 with `gitx: revision "-D" may not start with '-'` instead of 400 | **fixed** — `gitx.ErrInvalidRequest` / `InvalidRequestError` at every validation site, mapped once in `httpapi.classify` (branch, checkout, commit, diff range/base/commit, log cursor, worktree) |
| G8 | audit CSV (`audit_handlers.go`) | a tool name starting with `=` becomes a formula in an admin's spreadsheet | **fixed** — `csvFormulaSafe` on every string column (OWASP prefix), tested with `=cmd|' /C calc'!A0` |
| G9 | git executes repository configuration (`gitx`, every route) | `core.fsmonitor = sh -c 'printf FSMONAPI > marker; exit 1'` in the checkout's `.git/config`; `GET /v1/sessions/{id}/git/status` → 200 and the marker written. Same class: `core.sshCommand` through `POST /git/push` (marker `SSHEXEC`), hooks through a commit | **fixed** — `internal/gitx/harden.go`: every invocation carries `-c core.fsmonitor=false -c core.hooksPath=<empty dir> -c core.sshCommand=<resolved ssh> -c credential.helper= -c core.pager= -c core.askPass= -c core.gitProxy= -c commit.gpgSign=false -c tag.gpgSign=false -c protocol.ext.allow=never -c protocol.file.allow=user`, and every inherited `GIT_*` variable (including `GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_n` and `GIT_SSH_COMMAND`) is dropped before git starts. The importer's own `git clone`/`ls-files` and `fsx`'s `check-ignore` use the same list. The sandbox gets a variant whose ssh path is the container's `ssh` (a host path there would break a legitimate clone in a container placement) |
| G10 | git follows HTTP redirects (`skill_import.go`, and any http(s) clone/fetch) | `{"url":"https://postman-echo.com/redirect-to?url=http://127.0.0.1:7700/healthz"}` → the importer's git fetched the control plane's own `/healthz`; the same 302 to a closed port said `Failed to connect to 127.0.0.1 port 7999`. `urlguard` saw only the first URL | **fixed** — `-c http.followRedirects=false` on every invocation plus `GIT_HTTP_FOLLOW_REDIRECTS=0` in the environment: a redirect is a refusal, not a fetch. A legitimate redirect is not followed at all (no per-hop re-validation: refusing is the cheap correct answer) |
| G11 | Windows path semantics (`tenantfs`, `bundleFiles`) | (a) a **junction** inside an import root pointing outside leaked the target repository's files: `filepath.EvalSymlinks` returns a junction unchanged and `os.Lstat` reports it as a regular file, while opening it reaches the target; (b) `.git./config` uploaded as a bundle file → 200 and `.git\config` on the volume, because Win32 strips the trailing dot; (c) a junction flipped during an import leaked 14 times in 87 tries (2 in 6 in the report) | **fixed** — `tenantfs` uses `FILE_ATTRIBUTE_REPARSE_POINT` (a junction is not a symlink to Lstat) and `GetFinalPathNameByHandle` (the filesystem's own answer on an open handle), on every component and for containment; `bundleFiles` refuses any segment that ends in a dot or a space and compares the trimmed segment with `.git`; `importURL` returns the **resolved** path and git clones that, so a flip after the check changes a name nothing reads. Residual: a bind mount is not a reparse point (POSIX), and the resolved path's own components are only as stable as the deployment's disk |
| G12 | `repo_url` scp form with any user (`sandbox.refuseCloneTarget`) | `repo_url: root@127.0.0.1:acme/repo.git` accepted 201; the provision log shows `git clone --quiet -- root@127.0.0.1:acme/repo.git …: exit 128: ssh: connect to host 127.0.0.1 port 22`. `cloneFetchURL` mapped only the literal `git@` prefix, so every other user was a "local path" | **fixed** — `splitSCPLike` parses git's own rule (`[user@]host:path`, no slash before the colon, not a drive letter) and the host goes through `urlguard` on the transport's port (22), through the same `refuseTarget` the push guard uses |
| G13 | `pushInsteadOf`, `remote.*.proxy`, scp form without a user (`gitx.RemoteTargets`) | `url.git://127.0.0.1:9912/.pushInsteadOf https://93.184.216.34/` + a public `origin`: the guard passed and the push dialled loopback (`listener accept`, body `git push … 'git://127.0.0.1:9912/acme/repo.git'`). `remote.origin.proxy = http://127.0.0.1:9912` made a *fetch* CONNECT to it. `origin = 127.0.0.1:9912/x.git` (no `user@`) ran ssh to port 22 | **fixed** — `RemoteTargets` reads `url.*.(insteadOf\|pushInsteadOf)` as well as `remote.*.(url\|pushurl)`, and `networkRemote`/`scpHost` accept the scp form without a user; `RemoteConfigIssues` refuses the keys that name a program (`core.sshCommand`, `core.fsmonitor`, `core.hooksPath`, `remote.*.proxy`, `remote.*.uploadpack/receivepack`, `credential.helper`, `filter.*`, `diff.*.textconv`, `merge.*.driver`, `include.*`) in the local and worktree scopes only, so a deployment's own `~/.gitconfig` is not the tenant's doing |

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

`scripts/probe-data-flow.ps1` is the running-system half: 18 checks that send
what a tenant can send and assert the status the guard produces. It creates its
own fixtures (a project, a session, a skill) so no edge is skipped, prints
"N of M", and exits 2 when it could not measure an edge — that is a different
answer from 1 (wrong status). It passes on the throwaway stack. It is not part of
`go test` (it needs a deployment) and not part of `deliver-check.ps1` (that suite
is the delivery gate, not this one).

The fourth pass (G9–G13) is proved by Go tests *and* by a drill, because each
one's shape is "a command ran" or "a dial happened", which a status code alone
does not show:

| GAP | test | drill |
|---|---|---|
| G9 | `gitx/harden_test.go` (the fsmonitor and sshCommand markers are not written through `Status`/`Push`; the `-c` list is on the command line; every inherited `GIT_*` is gone) | `e2e/.artifacts/adversarial6/attack-round6.ps1` cases 1a/1b: `marker=FSMONAPI` and `marker=SSHEXEC` before, `absent` after |
| G10 | `httpapi/skill_import_*_test.go` (the clone runs with `http.followRedirects=false`) | the 302 to `127.0.0.1:7700` and to a closed port: before, git said `not found` / `Failed to connect`; after, `redirects are not followed` |
| G11 | `tenantfs/reparse_test.go` (a junction is outside the root, the walk refuses it, `Resolve` answers the target), `httpapi` (`.git.`/`.git `/`.git..` bundle paths are 400; `importURL` hands git the resolved directory) | cases 3a/3c: `leak=LEAKED` and `hits=14 tries=87` before; `400` and `hits=0` after |
| G12 | `sandbox/clone_target_test.go` (every scp spelling of a refused host, in both directions) | case 4: `ssh: connect to host 127.0.0.1 port 22` before; refusal naming the policy after |
| G13 | `gitx/remotes_test.go` (pushInsteadOf is read, the scp form without a user is a destination, the executing keys are named) | cases 5a/5b/5c: `listener accepts before=0 after=1`, ssh port 22, `before=1 after=2` before; 400 and no accepts after |

The second pass (G5–G8) is proven by go tests, because the shape of each fix is
a status, a cell or a dial decision rather than a route a probe can reach:

| GAP | test |
|---|---|
| G5 | `urlguard/navigation_test.go` (the rule), `httpapi/browser_navigation_test.go` (the route: control port, unpublished port, metadata, private, and the container-placement case that must keep working) |
| G6 | `httpapi/git_remote_guard_test.go` (a rewritten origin is refused with 0 pushes/pulls/fetches; a fixture remote and a public https remote still push; an allow-listed internal remote still pushes), `gitx/remotes_test.go` (url, pushurl and insteadOf are all read) |
| G7 | `httpapi/git_validation_status_test.go` (a real gitx refusal on every route answers 400) |
| G8 | `httpapi/audit_csv_injection_test.go` (the `=cmd|' /C calc'!A0` payload and every trigger character) |

G5 and G6 cannot be probed on the isolated stack: it runs the runner placement,
where Chrome and git are inside the sandbox and the host-placement rules are
deliberately not wired. The e2e suite runs host placement and covers the
allowance end to end (`-browser-localhost-ports 3100,3101`).

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
pwsh scripts/up-isolated.ps1 -Port 7723 -Rebuild   # -Rebuild: a tag that exists is not this tree
pwsh scripts/probe-data-flow.ps1                   # 18 checks; "18 of 18" or exit 2
pwsh scripts/up-isolated.ps1 -Down

# the code half
cd backend; go test ./internal/httpapi/ -run 'TestGuardedEdges|TestEverySink' -count=1
go test ./internal/tenantfs/ ./internal/sandbox/ -count=1
# the second pass
go test ./internal/httpapi/ -run 'TestGitValidationRefusalsAre400|TestAuditCSV|TestGitRemoteGuard|TestBrowserNavigation' -count=1
go test ./internal/urlguard/ ./internal/gitx/ -count=1
# the fourth pass (git as a command interpreter and as a second HTTP client)
go test ./internal/gitx/ -run 'TestRepoConfig|TestRunnerDrops|TestHardenedConfig|TestRemoteTargets|TestRemoteConfig' -count=1
go test ./internal/tenantfs/ -run 'TestALink|TestResolve' -count=1
go test ./internal/sandbox/ -run 'TestLocalRefuses' -count=1
go test ./internal/httpapi/ -run 'TestSkillBundleRefusesGitStoragePaths|TestImportURLHandsGitTheResolvedPath' -count=1
```

`up-isolated.ps1` no longer builds only what is missing: it compares each
image's creation time with the newest tracked or untracked source file under
`backend/`, `web/` and `deploy/` and rebuilds what is behind, then prints every
image's id and build time. A tag that exists is not a tag built from this tree —
an audit measured the previous image's behaviour for exactly that reason.

Two harnesses import over `file://` and therefore declare a root, the way an
operator would: `e2e/playwright.config.ts` passes `-skill-import-roots <tmpdir>`
(that spec stages its repository under the OS temp directory) and
`scripts/deliver-check.ps1` passes `PIFORGE_SKILL_IMPORT_ROOTS=/tmp/drill-imports`
and stages its clone-cap repository there. Everywhere else the empty default
refuses `file://` outright.

## 14. Open list

The thirteen GAPs are closed. What remains are residuals that are stated rather
than fixed, because each is either outside the boundary (the tenant already has
the authority in question) or a hardening step whose absence is not an exploit:

| # | edge | why it is still open | what a fix looks like |
|---|---|---|---|
| — | G5's published-port set is a deployment decision | an agent-started dev server on a port the operator did not list is not viewable in host placement; the refusal says so and names the set (§10.1) | an operator tool that publishes a session's listening ports into `-browser-localhost-ports` dynamically |
| — | G6/G9 in a *container* placement | git runs inside the sandbox, where `origin` is a URL the tenant can dial themselves and `.git/config` is a file they can write; the host check is deliberately not wired there | nothing to fix at this boundary; the sandbox's egress policy is the control. The command-scope config is still sent (harmless, and it keeps a container placement from being the weak one if the sandbox is ever shared) |
| — | ssh remotes in host placement | git runs the deployment's own `ssh` (resolved to an absolute path at start-up), with the deployment's keys and `known_hosts`. A tenant who can point `origin` at a public host can therefore use that key to reach it (not any other host, and not a private address) | this is the product's job: the operator opts in by making a key available. Refusing ssh outright would remove push-to-github; the guard's part (host, port, redirect) is closed. `-url-allow-file` remains the way to keep an internal remote |
| — | a *bind mount* inside an import root (POSIX) | it is not a reparse point, so `tenantfs` resolves it to itself and treats it as inside the tree | mount-namespace inspection (`/proc/self/mountinfo` + device/inode comparison) on POSIX; the deployment's import roots are the control until then |
| — | the resolved path is only as stable as its own components | `tenantfs.Resolve` closes the check-then-use window for the *name the tenant wrote*; a component inside the resolved directory can still be replaced between resolution and the open, by a process that can write the deployment's disk | `openat`/`O_PATH` traversal with `openat2(RESOLVE_BENEATH)` on Linux; on Windows, opening each component relative to the previous handle |
| — | `rest` in `/gw/mcp/<token>/…` | stays on the tenant's own MCP host | normalise the sub-path before proxying (belt and braces) |
| — | `repo` in the forge config | interpolation into the tenant's own forge URL path | validate `owner/name` shape in `forge.New` |
| — | residual from the earlier rounds | recorded in `docs/SANDBOX-DEPLOY.md` §1.9 | — |
