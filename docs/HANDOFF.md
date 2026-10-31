# PiForge — handoff

Written 2026-10-31 against a running deployment (`pf-local`, image `piforge:local`,
`sha256:e22828…`, built 12:31:10Z), plus `README.md`, `docs/SANDBOX-DEPLOY.md` and
`docs/DELIVERY-DRILL.md`. Numbers marked **measured** are from that instance; everything
else is what those three documents claim, quoted where it matters.

## What it is

PiForge is a multi-user, browser-based workbench for a coding agent: one user, one agent,
one workspace, with a file tree, a git view, a follow-along terminal, an interactive
terminal, a built-in browser and conversations that can fork and run in parallel. A Go
control plane owns everything the agent loop (`pi --mode rpc`) deliberately does not:
sandboxes, worktrees, credentials, persistence, SSE fan-out and the file/git/terminal/
browser services. The UI is Next.js + shadcn/ui, and all model traffic leaves through a
gateway that injects the real provider key, so a sandbox only ever holds a per-session
token.

Shape of the system:

- **Control plane** (`backend/cmd/piforged`, port 7700): HTTP/SSE/WebSocket, single-file
  SQLite, the LLM gateway (`/gw/llm/<token>`), PTY relay, file/git services. It never runs
  agent output itself — except in the `local` placement, see below.
- **Sandbox providers** (`contracts.SandboxProvider`: `Ensure`, `Get`, `Pause`,
  `Resume`, `Destroy`, `Exec`, plus `Endpoint`): `local` (a directory on the control
  plane's host — no isolation), `docker` (needs the docker CLI), `runner` (an HTTP
  client for `sandbox-runner`, the only process that mounts `/var/run/docker.sock`).
  A Kubernetes/E2B provider is a design in `docs/SANDBOX-DEPLOY.md`, not code.
- **Runners**: `backend/cmd/sandbox-runner` exposes exactly six actions
  (`POST /v1/sandboxes`, `GET /v1/sandboxes/{id}`, `…/pause`, `…/resume`,
  `DELETE /v1/sandboxes/{id}`, `POST …/worktrees`), bearer-token authenticated, with no
  exec and no file endpoint. It reuses `sandbox.Docker` rather than reimplementing it,
  and is stateless: container names derive from `sha256(user/project)[:8]`.
- **Workspaces**: one `(user, project)` → one directory → (docker/runner) one container.
  Branch worktrees live under it; `.pi/skills` and `.mcp.json` are materialised into the
  checkout on every provisioning pass.

## Run it

Build (the image is all-in-one: `piforged` plus the built Next app on 127.0.0.1:3100,
proxied by the control plane — you do not run `pnpm dev` in this shape):

```powershell
docker build --build-arg INSTALL_CHROMIUM=true -t piforge:local .
```

Run — reconstructed from `docker inspect pf-local`, so it is what is actually running
here; only the key is yours:

```powershell
docker run -d --name pf-local -p 7722:7700 -v piforge-data:/data `
  --cap-drop DAC_OVERRIDE `
  -e PIFORGE_PORT=7700 -e PIFORGE_DATA=/data `
  -e PIFORGE_SEED_EMAIL=admin@piforge.local -e PIFORGE_SEED_PASSWORD=piforge-admin-123 `
  -e PIFORGE_SEED_USER_EMAIL=dev@piforge.local -e PIFORGE_SEED_USER_PASSWORD=piforge-dev-123 `
  -e PIFORGE_DEFAULT_PROVIDER=volc `
  -e 'PIFORGE_BOOTSTRAP_PROVIDER=volc|<base_url>|deepseek-v4-flash' `
  -e "PIFORGE_BOOTSTRAP_PROVIDER_KEY=$env:PIFORGE_DRILL_KEY" `
  piforge:local
```

- **Ports**: 7700 inside, published here as 7722. One port serves the API *and* the UI
  (`http://127.0.0.1:7722/login`). The README's two-process flow (8080 + `pnpm dev` on
  3000) is a different, also-supported shape; the image does not need it.
- **Seeded accounts**: `admin@piforge.local / piforge-admin-123` (admin) and
  `dev@piforge.local / piforge-dev-123` (user). They exist only on a first boot against
  an empty data volume, and the log names them without printing the passwords. Unset
  passwords means generated ones, not logged, not recoverable without another admin.
- **Provider env vars**: `PIFORGE_DEFAULT_PROVIDER` (here `volc`),
  `PIFORGE_BOOTSTRAP_PROVIDER='name|base_url|model'`,
  `PIFORGE_BOOTSTRAP_PROVIDER_KEY`. Bootstrap creates the shared provider only when none
  usable exists, so a restart never replaces an operator's provider or its key. Also
  relevant: `PIFORGE_MASTER_KEY` (seals provider keys; without it `master.key` is generated
  in the data dir — back it up, or every sealed key dies with the volume),
  `PIFORGE_ALLOWED_ORIGINS`, `PIFORGE_SANDBOX*`, `PIFORGE_BROWSER_IDLE_TIMEOUT`.
- **Verify it is healthy** (all measured):

```
GET /healthz   -> 200 in 20 ms   {"status":"ok"}
GET /readyz    -> 200            {"status":"ok"}
GET /v1/setup  -> 401 unauthenticated
GET /v1/setup  -> 200 (as admin) {"needs_provider":false,"is_admin":true,
                                  "shared_provider_available":true,"default_provider":"volc"}
GET /v1/setup  -> 200 (as user)  {"needs_provider":false,"is_admin":false,
                                  "shared_provider_available":true,"default_provider":"volc"}
```

`/healthz` is a liveness probe for the Go process; it does not mean the Next app is
served. `/v1/admin/status` (admin only) is the operational truth — see below.

## Operate it

### Data directory (`/data` inside the container)

```
/data/master.key            32 bytes, 0600, generated on first boot
/data/piforge.db            SQLite; 5.2 MB + 4.2 MB -wal + 32 KB -shm at 7 sessions / 6 projects
/data/artifacts/            screenshots and other session artifacts (<session>/screenshots/…)
/data/pi-agents/<session>/  per-session agent config: auth.json, mcp.json, models.json, models-store.json
/data/sandboxes/<user>/<project>/repo          the checkout the agent writes to
/data/sandboxes/_state/                        placement bookkeeping (empty here)
/data/skills/<skill-id>/SKILL.md               skill bundles as authored
/data/skill-snapshots/<session>/               frozen skill tree per session (bind-ro/copy-ro/copy)
/data/snapshots/<sandbox>/<timestamp>/         sandbox snapshots kept by the idle policy
```

`GET /v1/admin/status` → `storage` is the source of truth for what is actually on disk.
Pasted verbatim from the deployment after the probe projects were deleted
(2026-10-31T13:0x UTC, `storage_total_bytes: 376888`):

```json
"storage": [
  { "name": "artifacts",       "entries": 0, "files": 0,  "bytes": 0 },
  { "name": "pi-agents",       "entries": 5, "files": 20, "bytes": 368199 },
  { "name": "sandboxes",       "entries": 1, "files": 0,  "bytes": 0 },
  { "name": "skill-snapshots","entries": 0, "files": 0,  "bytes": 0 },
  { "name": "skills",          "entries": 3, "files": 3,  "bytes": 1698 },
  { "name": "snapshots",       "entries": 5, "files": 5,  "bytes": 6991 }
]
```

Read it as: `entries` counts immediate children, `files`/`bytes` are recursive. Note
`sandboxes: entries 1, files 0, bytes 0` while `sandboxes: 5` — five sandbox *rows*
exist but their workspace directories are gone (deleted with their projects). Stale
rows survive their directories; do not use `sandboxes` count as a proxy for disk.

### Janitor, idle policy, reconciliation

Startup log of this deployment, verbatim (abridged to the policy lines):

```
msg="sandbox disk quota" limit="2.0 GiB" bytes=2147483648 skill_import_max="32.0 MiB" skill_import_bytes=33554432
msg="skill mounts use read-only copies" mechanism=copy-ro reason="no CAP_SYS_ADMIN and no CAP_DAC_OVERRIDE: file modes are enforced by the platform"
msg="sandbox browsers are stopped when idle" idle_timeout=1m0s
msg="session liveness: the in-process agent registry" sandbox=local probe=process-registry
msg="session reconciliation" phase=startup scanned=0 interrupted=0 runs=0 kept_live=0 unknown=0 reason=服务重启，本次运行被中断
msg="session reconciliation at startup" scanned=0 interrupted=0 runs=0 kept_live=0 watchdog=30s
msg="sandbox idle policy enabled" pause_after=30m0s reclaim_after=336h0m0s interval=1m0s snapshots=/data/snapshots
msg="orphaned runtime directories are swept" sandbox_root=/data/sandboxes skill_snapshot_root=/data/skill-snapshots agents_root=/data/pi-agents artifacts_root=/data/artifacts sandbox_snapshot_root=/data/snapshots skills_root=/data/skills interval=10m0s
```

- **Janitor** (every 10 min): removes runtime directories no database row points at —
  sandbox roots, skill snapshots, agent configs, artifacts, sandbox snapshots, skill
  bundles. It is what cleans up after a crash; a directory it deletes is gone, so a
  database that loses rows also loses the files.
- **Idle policy** (every 1 min): pause a sandbox after 30 min idle, reclaim (delete
  snapshots / workspace) after 336 h = 14 days. Observed pausing two sandboxes at boot
  that had been idle ~30m30s.
- **Browser idle**: 60 s without a panel viewer → Chromium stopped. Per sandbox; only
  the `host` placement is covered (DELIVERY-DRILL §6.10 says the remote path is not).
- **Reconciliation**: at startup and every 30 s. Because liveness is the in-process
  agent registry, a restart means every agent is gone, and any run that was live is
  marked interrupted (`interrupted`, `reason=服务重启，本次运行被中断`). It marks; it does
  not resume, and it does not re-send a queued message.

### Disk quota

`sandbox_disk_quota: 2147483648` (2.0 GiB) is reported per sandbox in `sandbox_usage[]`
as `{bytes, files, quota_bytes, over, measured_at}`, sampled on the admin-status call.
Skill *imports* have their own cap: 32 MiB (`skill_import_max`). I did not test a write
that crosses the quota — `over: true` is observable, but I never saw a write refused.

### Capability requirements

- The container runs as root. `copy-ro` skill materialisation relies on file modes
  (0444/0555), which root ignores through `CAP_DAC_OVERRIDE`; the deployment therefore
  starts with `--cap-drop DAC_OVERRIDE` (`CapDrop: ["CAP_DAC_OVERRIDE"]`, `CapAdd:
  null`, `Privileged: false`). Without the drop the product still starts but reports
  `copy` (frozen, writable) instead of pretending.
- `CAP_SYS_ADMIN` is optional: it upgrades the mechanism to `bind-ro` (kernel-enforced,
  and many sessions can share one snapshot). It is not required for correctness.
- No docker CLI in the image, so `-sandbox docker` cannot start here; `-sandbox runner`
  moves that dependency into the runner container (which needs Engine ≥ 25 for
  `volume-subpath`).

### Restart behaviour

`docker inspect pf-local`: `RestartPolicy {"Name":"no"}`. This deployment **does not come
back by itself** after a daemon restart or host reboot; with `docker restart` it does. On
restart containers/sandboxes are re-found by name (docker/runner) or reused (local) and
workspaces survive, but every agent process, PTY and Chromium is gone. Terminals reconnect
to a *new* shell (scrollback replayed from the PTY ring buffer, not restored); sessions
show the interrupted-run marker; `healthz` answers in 20 ms.

## Use it

Measured on this deployment, warm, single user, one browser tab:

| Step | Measured |
|---|---|
| Workbench first paint (Playwright, same host) | **168–188 ms** |
| Login `POST /v1/auth/login` | **44 ms** (200, returns `csrf` in the body + cookies) |
| `GET /v1/setup` | **1 ms** |
| `POST /v1/projects` | **2 ms** (201) |
| `POST /v1/projects/{id}/sessions` | **23 ms** (201) |
| Send a message (`POST …/messages`) | **186 ms** to `{accepted:true, disposition:"started"}` |
| First contact: `run.started` on the SSE stream | **+600 ms** from stream open |
| A short run (2-word answer, no tools) | **805 ms** started → settled |
| `GET …/entries` (5 entries) | **2 ms** |
| Interrupt (`POST …/interrupt`) | **7 ms**, answered `{cleared_steering:[],cleared_follow_up:[]}`; idle 1 ms later |
| `GET …/fork-points` | **4 ms** (3 points) |
| Fork (`POST …/fork`) | **227 ms** (201, `kind: branch`, own `worktree_id`) |
| Adopt (`POST …/adopt` on a fresh branch) | **409 in 10 ms** — "这个分支没有可采纳的改动：既没有领先基准的提交，也没有未提交的文件" |
| Inline review (message with `review_comment`) | **191 ms** accepted; that run **30,528 ms** |
| `GET …/git/pr` (no forge configured) | **12 ms**, 200, `remote:false, ahead:0, dirty:true` + warnings |
| Terminal typing | 40 chars in **34–37 ms** (41 frames, all on an open socket) |

Flow notes:

- **First contact** is not where the time goes: the message is accepted in ~200 ms and
  provisioning happens on that first send. A trivial answer settles in 0.8 s; the review
  turn took 30.5 s. Both are the model, not the product.
- **A run** is observed through `run.settled`, never `agent_end` — README rule 2, and the
  only signal that survives retries, compaction and steering. `GET …/status` is
  `{running, streaming, compacting, queue_depth, last_seq, process_id, session_file,
  resolved_model_ref}`; `process_id` is the agent process, absent/0 when idle.
- **Stop/continue**: interrupt stops the run in ~7 ms and clears steering/follow-ups.
  Then it gets interesting — see the second Known limit.
- **Fork/adopt**: fork is cheap and real (branch session, its own worktree). Adopt takes a
  *branch's* changes back, so it refuses when the branch has nothing ahead of its base —
  409 with a readable reason, not a silent no-op.
- **Inline review** is server-assembled: the browser posts
  `{path, line, side, hunk, quote, body}` and the server builds the `行内评论 · notes.txt:2`
  turn with the code quoted and a "位置：…" note. Measured 191 ms to accept, 30.5 s round.
- **PR with a forge configured** (from DELIVERY-DRILL, not reproducible here — no forge on
  this deployment): `PUT /v1/projects/{id}/forge {kind:"github", api_base, repo,
  base_branch, token}`, `GET …/git/pr` previews, `POST …/git/pr` pushes
  (`git push -u origin <branch>`) then creates; a duplicate returns `created:false` with
  the same URL, and a forge 422 surfaces verbatim as `code=forge_error,
  details.step=create, forge_status=422`. With no remote, preview answers 200 with
  `remote:false` and a warning to set `repo_url` — which is what it did here.

## Extend it

Seams, in the order they will bite:

- **Sandbox provider contract** (`backend/internal/contracts`): `Ensure/Get/Pause/
  Resume/Destroy/Exec` + `Endpoint`. `Exec` is debug-only — production paths use the
  sandbox's own `/v1/shell/exec` (files, git, terminal), which is why the runner does
  not implement exec. Two gaps a new provider must fill: `Capabilities()` (can this
  provider pause? is the endpoint proxied?) and `Purge()` (delete the workspace; today
  nothing deletes data). `Sandbox.RootDir` is a *host path* and `provision`/`materialize`
  depend on it — any provider without a host-visible workspace (K8s, E2B) has to move
  `AddWorktree` and `materialize.Mount` onto a workspace client first. Container/Pod
  names should keep the `sha256(user+"/"+project)[:8]` derivation so all three providers
  agree.
- **Forge dialects**: `PUT /v1/projects/{id}/forge` + `/git/pr` are the whole surface;
  the GitHub-shaped calls are `GET/POST /repos/{owner}/{repo}/pulls`, token only in
  `Authorization: Bearer`, filtered by `head`/`base`. A second dialect (GitLab/Gitea)
  means another `kind` and another create/find pair, not a new flow.
- **Skills materialisation**: three mechanisms chosen at runtime and named in the UI —
  `bind-ro` (needs `CAP_SYS_ADMIN`), `copy-ro` (0444/0555; needs `DAC_OVERRIDE` dropped),
  `copy` (frozen but writable). The output is `.pi/skills/<slug>/SKILL.md` +
  `.mcp.json` in the checkout, idempotent, removed when the config shrinks, and always
  written into `.git/info/exclude` — never `.gitignore`, or tooling shows up in the
  user's diff. Snapshots live under `<data>/skill-snapshots/<session>` and die with the
  session.
- **Test detection** (`internal/testrun`): recognises `npm test`, `pnpm test`, jest,
  vitest, `node --test`, `tsx --test`, parses TAP when present, and deliberately keeps
  counts unknown when the output only has `# pass`/`# fail`. Adding a runner means a
  detector plus a parser; anything unrecognised shows as `unrecognized` with counts 0
  rather than invented numbers.

## Known limits

Ordered roughly by how much they will hurt. Nothing here is softened.

1. **This deployment has no isolation at all.** The startup line is
   `sandbox=local browser=host terminal=host files=host`: there is one container, and
   every project is a plain directory under `/data/sandboxes/<user>/<project>`. A
   terminal opened in one project had hostname `0fe80fa91b45` — the control-plane
   container itself — and could read `/data` (I read `/data/skills` and other projects'
   paths from it). `docs/SANDBOX-DEPLOY.md` §3 says this plainly for `local`: it is not
   a tenant boundary, it protects against typos only. If a customer is told "code runs in
   an isolated sandbox", this deployment is not what that sentence describes; you need
   `-sandbox runner` (same host, real container boundary) or K8s + gVisor.
2. **Interrupt, then continue immediately: the answer came back about the wrong
   prompt.** Reproduced once, on purpose: send "Say alpha" (settles), send "Count from 1
   to 200" and interrupt after 2 s, then immediately send "Say beta." The message is
   accepted (200), the run settles in 3.9 s — and the assistant turn that follows
   "Say beta." reads *"The user wants me to count from 1 to 200… Let me just output the
   numbers"*. The next message after a pause ("Say gamma") answered correctly. In an
   earlier run the same sequence settled in **29 ms**, far too fast for a model round. I
   did not isolate whether the reply is the interrupted turn's tail being finalised or a
   new turn fed the stale context, and I deleted the probe project before capturing the
   raw entries for the 29 ms case. **Treat "interrupt then immediately send" as
   unverified and reproduce it before trusting it in a demo.**
3. **No microVM / no Kubernetes.** Firecracker, gVisor, Kata, E2B: design only. The K8s
   section of `SANDBOX-DEPLOY.md` has never been applied to a cluster (its own §8 says
   so), and the cold-start numbers there (42–50 s) are the upstream project's, not ours.
4. **Single replica only.** SQLite on a local volume, SSE fan-out in-process: "events are
   persisted before broadcast" makes a reconnect correct, but nothing wakes a subscriber
   on another node. Two replicas behind a load balancer drop live updates.
5. **The image cannot create containers.** No docker CLI, so `-sandbox docker` fails at
   startup inside this image. Runner mode needs a second service, a shared volume, the
   same path on both sides, and Engine ≥ 25 for `volume-subpath`. Two of the five failure
   modes in `SANDBOX-DEPLOY.md` §1.4.1 are *silent* when the volume mapping is wrong.
6. **No restart policy here.** `RestartPolicy: "no"` — a daemon restart or host reboot
   leaves the product down until someone starts it, and when it does come back every
   agent, PTY and Chromium is gone (reconciliation marks the runs interrupted; it does
   not resume them, and a queued message is lost with the process).
7. **The interactive terminal is 120 columns wide, always.** `stty size` says `32 120`
   inside the terminal and still `32 120` after the browser window is resized from
   2400 px to 1400 px — the PTY does not follow the panel. The product's own prompt is
   ~100 characters, so almost every command line is longer than the row, and the
   rendered screen contains spliced fragments (raw examples in
   `e2e/.artifacts/handoff/spotcheck.md` §3). Input is not lost — everything typed ran —
   but what the user sees while typing is scrambled. I did not chase the cause.
8. **Interrupted runs are marked, not recovered.** There is no "resume where it stopped",
   no partial-answer replay, and the transcript keeps whatever streamed before the
   interrupt. After a crash the same: the run is `interrupted` with a reason string.
9. **Adopt is narrow.** It only accepts a branch that is ahead of its base or has
   uncommitted files (409 otherwise, with a Chinese-language reason). There is no UI path
   I exercised; it is API-only in my testing.
10. **PR needs three things at once**: a `repo_url` reachable from the sandbox, a branch
    that is not the base branch, and a token. With none configured the preview still
    answers 200 and lists what is missing — good — but nothing warns you before you try.
    The forge token is project-level, write-only in the API, and (per the drill) appears
    in neither logs nor artifacts; it is still readable via `docker inspect` if it ever
    lands in an env var, which is how the bootstrap provider key gets there.
11. **Disk quota is measured, not proven enforced.** `over: true` is reported per
    sandbox; I never saw a write refused. The 2 GiB is per sandbox, not per user or per
    deployment, and the volume itself is whatever Docker gave you (`df` showed a 1007 GB
    filesystem).
12. **Stale rows outlive their data.** `sandboxes: 5` with `sandboxes_up: 0` and
    `sandboxes` storage at `entries 1, bytes 0`: five records whose workspaces were
    deleted. `sessions_live: 7` with `agents_live: 0` is the same class of thing — `live`
    is a database count, not a process count. `runs_running`/`agents_running` are the only
    process-truth fields in that response.
13. **Skill bundles are text-only from the UI.** `PUT /v1/skills/{id}/bundle` accepts more,
    but the browser picker sends flat text files only: no binaries, no nested directories.
14. **Test detection is a list, not a general parser.** `node --test`/`tsx --test` are
    recognised now; anything else shows `unrecognized` with zeros — honest, and useless
    to a user who just watched the tests pass.
15. **Browser idle reclamation is untested off the host placement.** 60 s here, and it did
    stop Chromium in the drill; the remote/runner path is explicitly not covered.
16. **SQLite at 5.2 MB + a 4.2 MB WAL after six projects and seven sessions.** Fine now;
    no compaction/vacuum policy in the docs, and the WAL was 80 % of the database after a
    few hours of probe traffic. Expect growth with event volume (9604 events already) and
    back up the WAL too.
17. **Documentation drift you will trip over** (not fixed, per instructions):
    - `README.md` "Status" claims 256 backend / 108 web / 81 e2e tests; `DELIVERY-DRILL.md`
      §7 reports 223 web tests and 164 e2e passes. One of them is stale.
    - `DELIVERY-DRILL.md` §6.4 says first-boot logs print the seed passwords; §6.10 says
      that was fixed and they no longer do. §6.4 is stale, and it is the paragraph an
      operator reads first.
    - `SANDBOX-DEPLOY.md` §8 says `deploy/Dockerfile.runner` "was never actually built"
      and the runner compose was only config-validated, while §1.4.1 records building and
      running it on Docker Desktop. Contradiction in the same file.
    - `SANDBOX-DEPLOY.md` §1.4.1 builds the control plane with
      `scripts/build-app-image.ps1 -Tag piforge:verify`; `DELIVERY-DRILL.md` §5.1 builds
      it with `docker build --build-arg INSTALL_CHROMIUM=true -t piforge:local .`. Both
      work, neither says which is canonical, and they produce differently-tagged images.
    - The image running here (`sha256:e22828…`, built 2026-10-31T12:31:10Z) is **not**
      the image the delivery drill documents (`sha256:2cdba2cd…`, 2026-10-30 16:58), so
      the drill's 119 green assertions were not re-proven on this build.
    - `README.md`'s local-run section (8080 + `pnpm dev` on 3000) never mentions that the
      image serves everything on one port; someone following only the README will not
      reproduce this deployment.
