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
- **Runners**: `backend/cmd/sandbox-runner` exposes six JSON actions
  (`POST /v1/sandboxes`, `GET /v1/sandboxes/{id}`, `…/pause`, `…/resume`,
  `DELETE /v1/sandboxes/{id}`, `POST …/worktrees`) plus one **stream**,
  `POST /v1/sandboxes/{id}/attach` (argv, dir, env, user → a hijacked duplex byte
  stream; it is how `-agent-in-sandbox` reaches the agent's stdio and it is not a
  user-facing shell). Bearer-token authenticated, with no file endpoint: files still go
  through the sandbox's own API. It reuses `sandbox.Docker` rather than reimplementing
  it, and is stateless: container names derive from `sha256(user/project)[:8]`.
- **Workspaces**: one `(user, project)` → one directory → (docker/runner) one container.
  Branch worktrees live under it; `.pi/skills` and `.mcp.json` are materialised into the
  checkout on every provisioning pass.

## Run it

### The isolated shape: one command

The shape this document's isolation claims are about (control plane container, sandbox
runner holding `/var/run/docker.sock`, one sandbox container and one workspace volume per
project) is one command from a clean checkout:

```powershell
$env:PIFORGE_DRILL_KEY='...'
pwsh scripts/up-isolated.ps1
```

It builds the three images in the only order that works — control plane, then the runner,
then the sandbox image **from** the control plane so `pi` and `/opt/piforge/piforged` are
inside it at the same version — runs `backend/cmd/preflight` (Docker Engine ≥ 25, all
three images, and that the sandbox image really contains `/opt/piforge/piforged` and
`pi`), then starts the stack on 7723 with a fresh volume and waits for `/healthz`. It
prints the URL and the seeded accounts at the end, with each image's id and build time.
`-Check` runs only the preflight; `-Down` removes the stack, its sandboxes and their
volumes; `-Rebuild` forces all three images. Since 2026-11-01 it does not wait to be told:
it compares each image's creation time with the newest tracked or untracked source file
under `backend/`, `web/` and `deploy/`, rebuilds what is behind, and rebuilds the sandbox
image when the control plane image it is built from changed. (Before that, a source change
kept the previous image and an audit measured the old binary.) `-EgressFile` points at
another egress policy than `deploy/tenant-egress.json` (the probe below uses that).

The stack's sandboxes have **no route to the public internet** by default: each tenant's
bridge is created `--internal` from the policy in `deploy/tenant-egress.json`, while the
control plane and the runner are joined to that bridge, so `/gw/llm` (the model gateway)
and the checkout keep working. `-tenant-egress-mode open` (or a tenant entry with
`"mode": "open"` in the policy file) is the only way out, and it is explicit. The
allowlist mode is refused at startup on purpose — see `docs/EXECUTION-BOUNDARY.md` §13.
`pwsh scripts/probe-egress.ps1` starts a throwaway stack that is closed for one tenant and
open for another and measures both from inside the sandboxes.

`.env.example` is the same configuration for the manual
`docker compose -f docker-compose.runner.yml up` path. It names
`piforge/sandbox-agent`, **not** the published AIO base — that image has no
`/opt/piforge` and no `pi`, so a sandbox running it cannot start the agent. That was the
onboarding trap the acceptance pass hit (it had to override every variable); the
preflight now fails at minute one and names the missing piece instead.

### The local shape (`pf-local`, 7722)

Build (the image is all-in-one: `piforged` plus the built Next app on 127.0.0.1:3100,
proxied by the control plane — you do not run `pnpm dev` in this shape). The canonical
command is the script; it cross-compiles the control plane, builds the web app and layers
both on the published base image (~90 s, Chromium included):

```powershell
pwsh scripts/build-app-image.ps1            # -Tag defaults to piforge:local
```

**Build the image only through this script.** It stashes `web/.env.local` (and
`web/.env`) for the build on purpose: Next inlines `NEXT_PUBLIC_*` at build time, so a
bundle built with that file present sends the terminal and browser sockets to
`ws://127.0.0.1:8080` — a port the container never listens on. The delivery drill's UI
check catches it as `the Playwright UI check exited 0 — exit=1` (measured twice this
round: `SANDBOX-DEPLOY.md` §1.4.6).

The underlying from-scratch build (installs node/pi/Chromium, ~20 minutes; only needed
when the base image is unavailable) is:

```powershell
docker build --build-arg INSTALL_CHROMIUM=true -t piforge:local .
```

Run — reconstructed from `docker inspect pf-local`, so it is what is actually running
here; only the key is yours:

```powershell
docker run -d --name pf-local -p 7722:7700 -v piforge-data:/data `
  --cap-drop DAC_OVERRIDE `
  -e PIFORGE_SANDBOX=local `
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
- **`-sandbox local` is explicit**: there is no default provider any more (adversarial
  round 6), so this `docker run` says `PIFORGE_SANDBOX=local`. The boot log then carries
  a WARN that the agent, terminal, browser and files run on this host with no container
  boundary; that is the development shape, not the tenant one.
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

### Keys: what each one protects, and what breaks when it is lost

`/data` holds **three** 32-byte keys, all `0600`, all created on first use. They are not
interchangeable, and losing one breaks a different part of the product (measured on a
throwaway isolated instance, `docs/SANDBOX-DEPLOY.md` §1.4.6 — every row below is an
error that was actually served, not a reading of the code):

| File | What it is | Losing it (measured) | Rotating it |
|---|---|---|---|
| `master.key` | `crypto.Box`: seals provider API keys (`providers.key_cipher`) and PR tokens | The next run fails at the model call: `400 {"code":"provider_unavailable","message":"stored key cannot be decrypted"}`. Every provider key must be re-entered. | Same as losing it — the file *is* the key. Re-enter the keys after. |
| `gateway.key` | HMAC for the per-session model-gateway tokens (`<gateway>/gw/llm/<token>`) | An agent that is still running (it holds the token it started with) gets `401 {"code":"invalid_token","message":"invalid or expired token"}`. **New sessions work**, because they get a token signed with the new key. | Same: every token issued before the rotation is invalid. Restart the affected agents (or wait for their idle TTL) so they get a fresh one. |
| `secret.key` | store `secretBoxFor`: sandbox API keys (`sandboxes.api_key_cipher`), MCP credentials (`mcp_servers.secrets_cipher`), MCP broker tokens | `POST /messages` → 500 with `store: open sandbox api key: cipher: message authentication failed`; the MCP broker answers `could not read the MCP credential: cipher: message authentication failed`. Existing sandboxes cannot be reached by the panels; the file is re-created on the next seal, so the deployment *looks* healthy while the old sandboxes are dead to it. | Same, and it is the nastiest of the three: recreate the sandboxes and re-enter the MCP credentials. |

Consequences worth stating in one place:

- **Nothing else in `/data` can rebuild them.** They are random, generated once, never
  derived from the database or from each other.
- **`gateway.key` is the only one whose loss is survivable without re-entering
  anything** — the cost is that every live agent's model calls fail until it restarts
  with a new token. That is exactly why it lives on the data volume (stage 3) and why a
  restarted control plane keeps its survivors working.
- **A backup is the database *and* all three keys, taken together.** A database without
  them restores into a deployment whose provider keys, sandbox keys and MCP credentials
  are all unreadable.

Backup (stop the control plane first — the archive must not capture a WAL in flight; the
sandbox agents keep running while it is down, which is what stage 3 bought):

```powershell
docker stop piforge
docker run --rm -v piforge-data:/data -v ${PWD}:/backup alpine:3.20 `
  tar czf /backup/piforge-$(Get-Date -Format yyyyMMdd-HHmm).tar.gz -C /data .
docker start piforge
# contains: piforge.db, master.key, gateway.key, secret.key, skills/, pi-agents/, snapshots/
```

Restore, verified end to end this round (a fresh volume, a second instance on 7723, the
same project list, and a real prompt that answers — §1.4.6 item 6):

```powershell
docker volume create piforge-restored
docker run --rm -v ${PWD}:/backup -v piforge-restored:/data alpine:3.20 `
  sh -c "cd /data && tar xzf /backup/piforge-<stamp>.tar.gz"
# then start the control plane against that volume with the SAME environment
```

Rotation, in the order that keeps the downtime honest:

1. **`gateway.key`** — stop the control plane, `mv /data/gateway.key
   /data/gateway.key.old`, start it. The new key is created on boot; every live agent's
   next model call fails with 401 until its process is restarted (`DELETE` the session's
   agent by pausing the sandbox, or wait for `-agent-idle-ttl`). Delete the old file once
   no agent from that era is running.
2. **`master.key`** — rotating it invalidates every sealed provider key: stop, move the
   file aside, start, then re-enter each provider key in Settings → Providers (and any PR
   token). There is no re-encryption path; the old blobs are unreadable by design.
3. **`secret.key`** — rotating it invalidates every sandbox API key and every MCP
   credential: stop, move it aside, start, then recreate sandboxes (the panels cannot
   talk to the old containers) and re-enter MCP credentials. Cheaper alternative: keep the
   file and rotate only the MCP credentials that matter.

### Data directory (`/data` inside the container)

```
/data/master.key            32 bytes, 0600, generated on first boot: seals provider keys
                            and PR tokens (crypto.Box)
/data/gateway.key           32 bytes, 0600, generated on first boot: the HMAC key for the
                            per-session model-gateway tokens. It lives on the data volume
                            so an agent that outlives a control-plane restart keeps using
                            the token it was started with (stage 3). Losing it invalidates
                            every outstanding token — but only those: new sessions work.
/data/secret.key            32 bytes, 0600, created when the first sandbox (or MCP server)
                            is stored: seals sandbox API keys, MCP credentials and MCP
                            broker tokens. Losing it makes every existing sandbox
                            unreachable and every MCP credential unreadable; it is then
                            re-created silently on the next seal.
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

Enforced in three places since the third pass (2026-11-01,
`docs/EXECUTION-BOUNDARY.md` §16.2, `docs/SANDBOX-DEPLOY.md` §1.4.8):

- **Kernel, per file.** Every sandbox container is created with
  `--ulimit fsize=<quota>:<quota>` (`sandbox.Limits.DiskBytes`), so every process in
  it — the agent, its children, the terminal shell — inherits `RLIMIT_FSIZE` at the
  budget and cannot raise the soft limit from a shell (`ulimit -f unlimited` needs
  no privilege; setting a soft limit *above* the hard one is refused). Measured:
  `fallocate -l 3G` against a 2 GiB budget dies with `File size limit exceeded`
  (SIGXFSZ) and the file is 0 bytes.
- **In the sandbox, per burst.** `piforged quota-enforcer` (package
  `internal/quotaenforcer`) runs as `gem` inside the sandbox — started by the image's own
  `RUN_HOOK_PRE_SERVICES` (`deploy/quota-enforcer.sh`), because the shipped runner
  provider deliberately has no exec endpoint. It walks the workspace with `du -sbx` every
  100 ms, reads the budget from `RLIMIT_FSIZE` (`/proc/self/limits`), and when the total
  is over it `SIGSTOP`s the processes holding a writable file below the workspace plus the
  chain that could start another one (stopping at a listening socket, so a sandbox service
  is never frozen). The writer is frozen, not killed; `SIGKILL` needs a mebibyte of growth
  after the freeze, i.e. new writers. Measured on the isolated stack: **2 265 053 457 B
  into a 2 GiB budget (1.05x; 2 172 778 769 B = 1.01x on a repeat run)** for a flood of 8 MiB
  files, 1.01x for a mixed
  one-big-file-plus-many-small burst — against **7 029 794 947 B (3.27x)** on the same
  probe before the change.
- **Periodic, per total.** `RLIMIT_FSIZE` bounds one file, not a tree. A watchdog
  (`httpapi.StartDiskWatch`, started by `cmd/piforged`) measures each
  READY sandbox's workspace *inside the sandbox* (`du -sb /workspace` through its own
  shell API) and, over budget, stops the agents, appends a `platform.error` with the
  same 507 message the run boundary uses, and — if usage keeps growing after the
  agents stopped — pauses the sandbox. The same measurement feeds
  `GET /v1/admin/status`'s `sandbox_usage[]` and the run-boundary refusal, so the
  admin figure and the refusal cannot disagree.

`sandbox_usage[]` reports `{bytes, files, quota_bytes, over, unmeasurable, measured_at}`.
A sandbox the deployment cannot measure is `unmeasurable: true` and the page shows
`未知`, not `0 B / over=false` — that zero was the defect: with a detached workspace
the row's `root_dir` is `/workspace`, a path that exists only *inside* the container,
so the host walk measured nothing while the sandbox held 3 GiB.

**Measured alternatives that do not work here** (re-measured on this daemon in the third
pass): `--storage-opt size=` is *accepted* and silently ignored — `HostConfig.StorageOpt =
{"size":"1g"}` while `df /` still reports 1006.9G — and it would not cover the workspace
volume anyway; a size-capped tmpfs really does enforce a hard cap (the write dies with `No
space left on device` at exactly 2147483648 B) but `docker restart` leaves the workspace
empty (`total 0`), so pause/resume, the file panel and the agent's work all break, and it
competes with `--memory`; ext4 project quotas need a `prjquota` mount this volume does not
have; a loopback ext4 needs `CAP_SYS_ADMIN`, which the sandbox no longer holds. So the
honest bound is "one file cannot exceed the budget, and a burst is stopped within ~0.2 s of
writes (~1.05x measured), with the platform watchdog as the second line", not "the
filesystem cannot be over quota at all".

### Capability requirements

Measured, not guessed (`docs/EXECUTION-BOUNDARY.md` §14.1): a sandbox container runs
with `--cap-drop ALL --security-opt no-new-privileges` plus exactly five
capabilities, each added back because the AIO image's own bootstrap failed without
it:

| capability | what needs it |
|---|---|
| `CHOWN` | `gem.sh` chowns `/home/gem`, `/workspace`, the log/run dirs and the X11 socket to `gem` |
| `DAC_OVERRIDE` | the same bootstrap writes into directories the image already owns as `gem` (`/var/lib/aio-sandbox`, `/var/run/gem`) |
| `FOWNER` | it `chmod`s directories `gem` owns (`/run/user/1000`, `/tmp/.X11-unix`) |
| `SETUID`, `SETGID` | root → `gem` for the services it starts (nginx master stays root; file/shell/terminal/code-server/Jupyter run as `gem`) |

Everything Docker grants by default and this list omits is gone: `NET_RAW`
(raw sockets on the tenant bridge), `SYS_CHROOT`, `MKNOD`, `SETFCAP`, `KILL`,
`AUDIT_WRITE`, `NET_BIND_SERVICE`. The agent account holds **none** of the five
(`CapEff 0000000000000000`); they belong to the container's own bootstrap.

`no-new-privileges` is what ends the passwordless-root leak: the image's
`/usr/bin/sudo` is setuid-root, and with no_new_privs the setuid bit no longer
changes the effective uid, so `sudo -n id` fails as `gem`. The hardened sandbox
image (`deploy/Dockerfile.sandbox-agent`) additionally deletes the sudo binary and
its sudoers entry, and makes `/opt/piforge` + `/opt/piforge/piforged` root-owned
`0555` — a mode root cannot write either, because the container has no
`CAP_DAC_OVERRIDE`-equivalent left to bypass it with.

- `CAP_SYS_ADMIN` is optional and **no longer granted**: it used to upgrade
  `copy-ro` skill materialisation to `bind-ro`. It is not required for correctness.
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

**That paragraph describes `-sandbox local` (this instance) and still holds for it.** For
the `docker`/`runner` placements stage 3 changed the answer: the agent is a process inside
the sandbox owned by a detached supervisor (`piforged agent-supervisor`, Unix socket in
`/workspace/.piforge/run/`), so a control-plane restart no longer kills it. The restarted
control plane reattaches **before it opens its port** (`Server.ReattachLiveSessions`, the
runner's `…/reattach` verb), the run keeps going in the same `pi` process, and it settles
into the same row. Measured on a throwaway runner deployment: same `pi` pid before/after
`docker restart`, `scanned=1 interrupted=0 kept_live=1`, `replayed_bytes=10955 gap_bytes=0`
on the supervisor's side, and the run's commit in the sandbox (`docs/SANDBOX-DEPLOY.md`
§1.4.5). What still ends with the control plane: PTYs, Chromium, and any agent whose
supervisor was killed with it (the agent dies when its supervisor dies, by design).

To make it come back, change the policy on the running container (the container keeps its
name, port mapping and the `piforge-data` volume):

```powershell
docker update --restart unless-stopped pf-local
docker inspect -f '{{.HostConfig.RestartPolicy.Name}}' pf-local   # -> unless-stopped
```

`docker update` rewrites the container's HostConfig; it does not need a restart, and
`RestartPolicy: "no"` is what `docker run` defaults to, so nothing else sets it. The same
value has to be passed as `--restart` on the `docker run` that recreates the container
(see "Run it" above), or `docker update` after it.

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
  nothing deletes data). `Sandbox.RootDir` is where the workspace *is* and
  `Sandbox.Workspace` says how this process can reach it (`host` = a directory it can
  open, `api` = only through the sandbox, stage 2). A provider whose workspace is not a
  host path (K8s, E2B) has to implement `WorkspaceFiles` (provision/materialize) or the
  projection refuses to run — that is the seam that used to be missing and is now the
  contract. Container/Pod names should keep the `sha256(user+"/"+project)[:8]`
  derivation so all three providers agree.
- **Forge dialects**: `PUT /v1/projects/{id}/forge` + `/git/pr` are the whole surface;
  the GitHub-shaped calls are `GET/POST /repos/{owner}/{repo}/pulls`, token only in
  `Authorization: Bearer`, filtered by `head`/`base`. A second dialect (GitLab/Gitea)
  means another `kind` and another create/find pair, not a new flow.
- **Skills materialisation**: three mechanisms chosen at runtime and named in the UI —
  `bind-ro` (needs `CAP_SYS_ADMIN`), `copy-ro` (0444/0555; needs `DAC_OVERRIDE` dropped),
  `copy` (frozen but writable). The projection writes through `materialize.FileSystem`:
  the control plane's disk when the workspace is a directory it can open, and the
  sandbox's file API when it is not (`SandboxFS`, stage 2 — the only mechanism there is
  `copy-ro`, because a bind mount lives in the container's namespace). The output is
  `.pi/skills/<slug>/SKILL.md` in the checkout, idempotent, removed when the config
  shrinks, and always written into `.git/info/exclude` — never `.gitignore`, or tooling
  shows up in the user's diff. Snapshots live under `<data>/skill-snapshots/<session>` and
  die with the session (host-only: they exist to be a bind source).
- **Test detection** (`internal/testrun`): recognises `npm test`, `pnpm test`, jest,
  vitest, `node --test`, `tsx --test`, parses TAP when present, and deliberately keeps
  counts unknown when the output only has `# pass`/`# fail`. Adding a runner means a
  detector plus a parser; anything unrecognised shows as `unrecognized` with counts 0
  rather than invented numbers.

## Known limits

Ordered roughly by how much they will hurt. Nothing here is softened.

0. **Tenancy is now an operating property on the isolated shape — with four named
   exceptions.**
   Since 2026-11-01 (`docs/EXECUTION-BOUNDARY.md` §12, `docs/SANDBOX-DEPLOY.md` §1.7):

   - **Network:** sandboxes no longer share `piforge-sandboxes`. Each tenant gets a
     bridge of its own (`piforge-t-<hash>`, created by the runner, labelled
     `piforge.tenant`), and only that tenant's sandboxes plus the control plane and the
     runner are on it. Measured from inside a sandbox on the isolated stack: the
     neighbour's name does not resolve (`no such host`), its address times out
     (`curl` exit 28 after 4.0 s, `nc` no route), while the sandbox still reaches its
     own model gateway (`200`) and the control plane's `/v1/*` still answers `401`.
   - **Quota:** CPU / memory / process limits and a concurrent-sandbox cap come from a
     per-tenant policy (`deploy/tenant-limits.json`, `PIFORGE_TENANT_LIMITS_FILE`), not
     from the deployment-wide flags. They are applied with
     `docker run --cpus/--memory/--pids-limit`, recorded on the sandbox row from the
     container's own `HostConfig`, and reported per tenant in `/v1/admin/status`
     ("租户配额与网络" on the admin page). A tenant at its cap is refused with
     `409 tenant_sandbox_limit` and the numbers (`{limit, current, tenant}`) — before
     this, that refusal was logged and the caller saw a generic 500.
   - **Audit:** `GET /v1/admin/audit` (admin, paginated, filterable, `format=csv`)
     reads `tool_calls` across tenants: who / what / when / outcome with session,
     project and user. There is an `/admin/audit` page with the same filters and an
     export link.
   - **Egress:** the same batch's last piece (2026-11-01, `docs/EXECUTION-BOUNDARY.md`
     §13, `docs/SANDBOX-DEPLOY.md` §1.7.5). A tenant's sandbox has **no route to the
     public internet** unless its policy says `open`: the tenant bridge is created with
     `docker network create --internal`, so `npm install`, `curl` and a poisoned
     dependency cannot leave, while `/gw/llm`, `/gw/mcp` and the checkout still work
     because the control plane and the runner are on that same bridge. The policy is
     `deploy/tenant-egress.json` (`PIFORGE_TENANT_EGRESS_FILE`, read by the control plane
     *and* the runner), default `none`, per-tenant overrides by user id or email. `open`
     is explicit; `allowlist` is modelled and **refused at startup** because Docker
     cannot filter a bridge by hostname and a half-enforced allowlist is exactly the lie
     this feature exists to prevent; `none` on a shared bridge or under `-sandbox local`
     is refused too. `GET /v1/admin/status` reports each tenant's effective mode and the
     admin page shows it (`沙箱出口`). Probe: `pwsh scripts/probe-egress.ps1 -Port 7723`
     — one closed tenant and one open tenant on the same stack, measured from inside the
     sandboxes, plus a real prompt in the closed one; artifacts in
     `e2e/.artifacts/egress/`.
   - **Soak:** `pwsh scripts/soak-tenancy.ps1 -Port 7723` — four tenants, four
     projects, four concurrent runs, then the probes above plus the quota refusal and
     the audit query. Last run: 0 failures, exit 0, artifacts in
     `e2e/.artifacts/tenancy/`.

   **What it still does not claim, and must not:** two sandboxes of the *same* tenant
   share their tenant bridge and can reach each other's 8080 (the image's per-sandbox
   key still says 401) — per-sandbox networks were rejected because each one costs a
   network and this daemon refuses the 17th (`all predefined address pools have been
   fully subnetted`); egress is a **switch, not a filter** (no host allowlist: that mode
   is refused at boot), it does not exist on the shared single-host shape or under
   `-sandbox local`, and it does not stop a sandbox from asking the model gateway to send
   whatever the prompt contains; disk is per sandbox, not per tenant; and the number is
   four tenants on one Docker Desktop host, not fifty.

   **What the 2026-11-01 adversarial fixes still do not claim** (`docs/EXECUTION-BOUNDARY.md`
   §14 has the mechanisms and the measurements):

   - **The skill tree is read-only by mode, not by ownership.** `.pi/skills/**` is
     0444/0555 and the tenant account *owns* it (the sandbox file API writes as `gem`,
     which is also the account the agent runs as). A shell redirect into a frozen skill
     fails, and the pre-fix `sudo` route to it is gone, but an agent that runs
     `chmod u+w` first can still edit one. Making that impossible needs a root-owned
     writer inside the sandbox (a bind mount or a root-side file API), which this image
     does not offer; the honest statement is "frozen against accident and against the
     old sudo path, not against a determined tenant".
   - **`/opt/piforge/piforged` is unwritable by the tenant, not integrity-checked.** It
     is root-owned 0555 and the container has no capability that bypasses mode bits, so
     the tenant cannot rewrite it; nothing verifies its hash at spawn. A launcher that
     was tampered with would run *inside* that tenant's own sandbox (the control plane
     starts it there, as `gem`), so the blast radius is the sandbox, not the host.
   - **A model-gateway token only works from its own sandbox.** It used to be a portable
     bearer credential for an hour: it answered `/gw/llm` from the host (that is how the
     acceptance pass tested it), from a sibling sandbox and from another tenant, and it
     was readable in `models.json`. Now the token carries the sandbox it belongs to
     (`b` in the signed claims) and the gateway requires two things it did not before: the
     request must arrive **from an address that sandbox has** (Docker's embedded DNS,
     re-read on a mismatch, so pause/resume and a control-plane restart are followed rather
     than re-bound), and it must carry **`X-PF-Sandbox-Proof: HMAC(per-sandbox secret,
     token)`**. The secret is sealed in the sandbox row and never enters the container; the
     value that does is a per-token HMAC in the *agent's environment*, referenced from
     `models.json` as `$PIFORGE_SANDBOX_PROOF`, so the file an attacker copies no longer
     carries everything. Measured on the isolated stack: inside `200`, from the host `403
     sandbox_address_mismatch` (even with the proof copied along), from a sibling sandbox
     `403`, from another tenant `403`, same token still `200` after `docker restart
     piforge`, `401` after pause and after the second restart, resumed token `200`; the MCP
     broker token is bound by address too (`200` inside, `401` from the host). What is
     still true: the agent inside its own sandbox can of course use it, `-sandbox local`
     mints unbound tokens (the boot log says so), and the address half trusts this
     deployment's network shape — mTLS is the answer that does not, and it is not done.
     See `docs/EXECUTION-BOUNDARY.md` §16.3.
   - **Disk is bounded per file, per burst and per total.** See "Disk quota" above:
     `--storage-opt size=` is ignored by this daemon, and a size-capped tmpfs enforces a
     hard cap but loses the workspace on restart. So the bound is: one file cannot exceed
     the budget (kernel), a burst is stopped within ~0.2 s of writes by the in-sandbox
     enforcer (**measured 3.27x → 1.05x** of a 2 GiB budget), and the platform watchdog
     (2 s active / 10 s idle) stops the agents and pauses a sandbox that keeps growing.
     The host volume is 1007 GB; the point is that one command can no longer fill it, that
     a burst cannot run for seconds, and that the admin figure matches reality.
   - **`sudo` removal is image-level; `no-new-privileges` is the runtime guarantee.**
     A deployment that keeps the stock AIO sandbox image still has `/usr/bin/sudo` on
     disk — it just cannot gain privileges. The hardened image deletes it.



1. **This instance has no isolation at all — but the product path that does has been
   proven here.** The startup line is
   `sandbox=local browser=host terminal=host files=host`: there is one container, and
   every project is a plain directory under `/data/sandboxes/<user>/<project>`. A
   terminal opened in one project had hostname `0fe80fa91b45` — the control-plane
   container itself — and could read `/data` (I read `/data/skills` and other projects'
   paths from it). `docs/SANDBOX-DEPLOY.md` §3 says this plainly for `local`: it is not
   a tenant boundary, it protects against typos only.

   **What changed on 2026-10-31:** `-sandbox runner` was brought up on this same machine
   (Docker Desktop, Engine 29.9.2) as a second instance on 7723, with the runner holding
   the only docker socket and a throwaway volume, and a real agent task ran inside it
   ("write a Node script for the first 10 primes, test it, run it, commit"): one commit
   `453c7c5` in the sandbox, tests 3/3, panel/changes/inline-review all working,
   61 ms–18.5 s timings, `docs/SANDBOX-DEPLOY.md` §1.4.2 has the commands and the raw
   evidence. So "code runs in an isolated sandbox" is now a demonstrated mode of this
   codebase, not a design claim — and two blocking defects had to be fixed to get there
   (the checkout was not writable by the root control plane; the agent's git refused a
   checkout owned by `gem`).

   **What is still not true, and must not be said:** the agent *process* (`pi` and the
   bash/node/git it spawns) runs in the **control-plane container**, not in the sandbox
   — `ps` shows `piforged agent-launcher … -- pi` there, and pi's cwd is
   `/data/sandboxes/<u>/<p>/repo` (the control plane's view of the shared volume), while
   the sandbox sees the same inode at `/workspace/repo`. The sandbox is the **workspace**
   boundary (files, terminal, browser, panel git), not an *execution* boundary: the agent
   can read `/data` (SQLite, `master.key`, other tenants' workspaces), and a
   control-plane restart kills it. Two user-visible consequences are recorded in
   §1.4.2: a control-plane restart keeps the run "live" for up to the 2-minute heartbeat
   TTL before the watchdog marks it interrupted (the probe does **not** mis-kill at
   startup — `scanned=1 interrupted=0 kept_live=1`), and the panel cannot edit a file the
   agent created (`PUT /files` -> 500, `Permission denied`, because root wrote 0644 and
   the panel writes as `gem`). Making the sentence "agent code runs in the sandbox"
   literally true needs pi inside the sandbox plus `materialize` through the sandbox file
   API; that is not done.

   **Stage 1 of that is now done, behind a flag (`-agent-in-sandbox`, default off).**
   With the flag, the control plane does not spawn `pi`; it asks the sandbox to run
   `piforged agent-launcher … -- pi --mode rpc` and bridges the JSONL stream over the
   runner's new `attach` endpoint (`docker exec -i` stdio, hijacked). Proven on a
   throwaway 7723 runner deployment: `ps -ef` **inside** the sandbox shows
   `gem … agent-launcher -root /workspace … -- pi` and `gem … pi`, the control plane shows
   neither, and `cat /data/piforge.db` inside the sandbox is `No such file or directory`.
   One real prompt ("write a Node script for the first 10 primes, test it, run it,
   commit") settled in 12 s with commit `de5061b` in the sandbox worktree, tests 2/2 via
   the tests panel, panel write/stage/commit `e30cbb5`, and an inline review comment
   accepted in 21 ms that the agent answered with commit `3661297`. `models.json` reaches
   the sandbox through its file API and holds only the gateway URL and a per-session
   token (0 hits for the provider key in the sandbox, in its env and in that file).
   Two limits are unchanged and stated in `SANDBOX-DEPLOY.md` §1.4.3: **the agent still
   dies with a control-plane restart** (stage 1's RPC pipe belongs to the control plane;
   the detached supervisor is stage 3 — what improves is that the marker is now removed,
   so reconciliation says "dead" immediately instead of keeping it live for the TTL), and
   **`materialize`/`provision` still write through the shared volume**, so the workspace
   mount is still a hard requirement for the control plane (stage 2). MCP configuration is
   deliberately not staged into the sandbox until there is a credential broker.

   **Stage 2 is now done too, behind the same flag (2026-11-01).** The control plane no
   longer mounts the workspace at all: each sandbox gets a volume of its own
   (`piforge-ws-<pair hash>`), the sandbox row records `workspace=api` with
   `root_dir=/workspace`, and the skill projection, the branch worktree and the file panel
   all go through the sandbox's own API (`materialize.MountFS` + `materialize.SandboxFS`).
   Proven on a throwaway 7723 runner deployment with `SANDBOX_WORKSPACE_VOLUME` unset:
   `docker inspect` shows the control plane with only its state volume
   (`pf-drill-data -> /data`), the runner with only the docker socket, and the sandbox
   with `piforge-ws-… -> /workspace` — nobody else mounts it, and `/data/sandboxes` does
   not exist in the control plane any more. The same prompt settled in 17 s with commit
   `ae151cc` inside the sandbox; the skill tree is `dr-xr-xr-x`/`-r--r--r--` and a write as
   `gem` is `Permission denied`; a branch session's `git worktree list` inside the sandbox
   shows `/workspace/worktrees/<slug>`; file panel, tests panel (3/3) and inline review
   still work; `cat /data/piforge.db` inside the sandbox still fails. `docs/SANDBOX-DEPLOY.md`
   §1.4.4 has the commands and the raw output. Two workarounds were **removed** rather than
   kept "just in case": the checkout no longer runs `chmod -R a+rwX` and git is no longer
   told `safe.directory`, because a detached workspace has exactly one writer (the sandbox
   user). The shared-volume shape still injects both, because it still has two writers.
   Still not true after stage 2: the agent dies with a control-plane restart (stage 3), its
   stderr goes to the runner's log rather than the Logs panel, the marker carries no boot
   id, the idle-reclaim snapshot and the disk-quota measurement cannot read a workspace
   they cannot open (both fail safe and are named in `EXECUTION-BOUNDARY.md` §10.1), and
   MCP is still deliberately absent under the flag.

   **Stage 3 is done, and the flag is on by default for docker/runner (2026-11-01).** The
   three things stage 2 left open are closed, each proven on a throwaway 7723 runner
   deployment (`docs/SANDBOX-DEPLOY.md` §1.4.5):

   - **the agent survives a control-plane restart.** A detached supervisor inside the
     sandbox owns the agent's stdio; a restarted control plane reattaches through the
     runner's new `…/reattach` verb, before it opens its port. Measured: same `pi` pid
     before and after `docker restart`, sandbox container untouched,
     `scanned=1 interrupted=0 kept_live=1`, `replayed_bytes=10955 gap_bytes=0`, the run
     settling into the same row and its commit in the sandbox. `/status` reports
     `running:true` and `process_id` = that pid right after the restart.
   - **MCP credentials do not enter the sandbox.** An HTTP MCP server is brokered: the
     sandbox holds `<gateway>/gw/mcp/<per-session-token>`; the control plane injects the
     real credential on egress and `DELETE /v1/sessions/{id}/mcp-token` revokes it. The
     measured grep for the real credential inside the sandbox is **0 hits** (files and
     environment), the agent used the tool through the broker (`note: stage 3 broker`),
     and the same URL went 200 → revoke 204 → 401. A stdio server is still not staged,
     with a log line saying why.
   - **the agent's stderr reaches the Logs panel.** The supervisor writes
     `/workspace/.piforge/logs/<session>.log`; `GET /v1/sessions/{id}/logs` reads it
     through the file API. Proven with a real failure (an unwritable heartbeat marker):
     the launcher's WARN appears in the panel verbatim.

   The default flip means a fresh `docker compose -f docker-compose.runner.yml up` gives
   the isolated instance with no extra flags (the file is self-contained now; measured on
   7724: control plane mounts only its data volume, the runner only the docker socket, and
   the sandbox only its own `piforge-ws-…`). `-sandbox local` is unchanged and is still
   what the `-sandbox` default selects. Stage 4 (removing the flag, the host spawn and the
   K8s `pods/exec` provider) has not started; the remaining limits are listed in
   `EXECUTION-BOUNDARY.md` §10.1.

   **Stage 4 is done (2026-11-01), and the sandboxed execution is now the only one.**
   Three things changed, and one thing is still missing:

   - **the switch is gone.** `-agent-in-sandbox` no longer selects anything: a container
     placement always runs the agent inside its sandbox, `-sandbox local` never does.
     `-agent-in-sandbox=false` (or `PIFORGE_AGENT_IN_SANDBOX=0`, which is what an older
     compose file carries) is now a **startup error** that names the flag, the reason
     (an agent beside the control plane can read `/data`) and the way to run unsandboxed
     on purpose. `true` is a no-op; on `-sandbox local` a stale `=1` is one warning.
     Measured on 7724 (`SANDBOX-DEPLOY.md` §1.4.6 item 4).
   - **the dead path is deleted**, file by file in `EXECUTION-BOUNDARY.md` §11.3:
     `sessionAgentLauncher`, `pi.RuntimeConfig.WrapArgv`, `liveness.LauncherArgv`,
     `agentGitEnv`/`agentCheckoutDirs`, `resolveAgentInSandbox`, plus the wiring that only
     existed to choose between the two executions. `-sandbox local` — the development and
     delivery-drill path — lost nothing: child process, no marker, in-process registry,
     and the full e2e + drill still pass (§1.4.6 has the gates).
   - **stdio MCP is refused in the product, not just in the log.** The panel says
     「stdio MCP 服务器在隔离模式下不可用」 with the reason, the tooling answer carries the same
     map, and the checkout's `.pi/mounted.json` records it in `mcp_unavailable` while
     keeping the server out of `mcp_servers`. The reasoning for refusing rather than
     running it in the sandbox (or on the control plane) is written out in
     `EXECUTION-BOUNDARY.md` §11.1; the evidence — API output, the manifest inside the
     sandbox, and 0 grep hits for a fake stdio credential and for the real provider key —
     is in `SANDBOX-DEPLOY.md` §1.4.6 item 3.
   - **what is still missing for a production claim:** the `ps`/`/data` proofs ran on
     Windows + Docker Desktop, not a Linux host; there is no K8s provider (`pods/exec`);
     and the stage-2 gaps (`worktrees.base_commit`, the idle-reclaim snapshot, the
     disk-quota measurement, in-container `CloneSession`) are unchanged. The keys'
     failure modes are now measured and documented (`Operate it` above and §1.4.6 item 5),
     including the third key, `secret.key`, which this document had not named before.
2. **Interrupt, then send another message: the agent resumes the interrupted request.**
   Verified on 2026-10-31, deterministically (6/6 runs; `e2e/scripts/fix-interrupt-context-probe.mjs`,
   raw runs in `e2e/.artifacts/fix-interrupt-context/`). Send "Count from 1 to 200" and stop it
   while prose is streaming, then send "Say beta.": the message is accepted (200), a *new*
   run starts, and its answer is the 1–200 list (or prose about counting) because Pi keeps
   the truncated assistant message in the conversation and the model continues it.
   What the probe proved, layer by layer:
   - the follow-up reached the model — the agent's own session file
     (`/tmp/.piforge/sessions/<id>.jsonl`) contains it, followed by the answer that run
     generated;
   - the control plane injected no stale context and stamped no stale run id: `run.started`
     is a fresh id after `run.settled{aborted}`, and the POST answered `disposition:
     "started"`;
   - the transcript attributed the answer to the new turn (event order, not UI confusion).
   So the stale answer was a *conversation* failure, not a delivery or reducer one: the agent
   was told "Say beta." with a half-written answer right above it and answered the old task.
   The 29 ms settle did **not** reproduce: the shortest post-interrupt run in 13 runs was
   2.5 s (new run) with the interrupted run settling before the follow-up was even sent; the
   only sub-second run in the log is a provider failure that renders a visible error.
   **Fixed in the control plane:** the first message after an interruption is composed with
   `[上一轮已被用户中断并作废：不要继续它，也不要重新执行上一轮的请求。只回答下面这条消息。]` in
   front, the same server-side composition the review-comment and 继续 paths use (the
   transcript shows the composed text because that is what the agent got). With that
   sentence the same sequence answers "beta" **5/5** on the rebuilt deployment; a weaker
   wording scored 4/5. It is a prompt-level mitigation, not a guarantee — the model can
   still choose to finish the interrupted task, and the sentence then makes that visible
   instead of mysterious. The sentence is only on the first message after the stop; tests:
   `httpapi/interrupt_note_test.go`, `e2e/tests/91-interrupt-then-message.spec.ts`.
   Residual limit: a *second* message in the same run can still be answered against the
   first one's context — that is ordinary conversation, not a lost or misattributed turn.
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
   A container placement adds two requirements of its own: the sandbox image must carry `pi`
   and `/opt/piforge/piforged` (`deploy/Dockerfile.sandbox-agent` builds that image from
   the control-plane image, so the two cannot drift on the pi version), and the control
   plane must reach the sandbox's gateway by name. A sandbox image without them fails the
   first prompt with a clear error; it never falls back to running beside the control
   plane (stage 4 deleted that fallback).
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
17. **Documentation drift** — the four contradictions below were corrected on 2026-10-31;
    what remains is listed after them:
    - ~~`README.md` "Status" claims 256 backend / 108 web / 81 e2e tests; `DELIVERY-DRILL.md`
      §7 reports 223 web tests and 164 e2e passes.~~ Fixed: README now records the counts
      measured on 2026-10-31 (734 Go test functions, 272 web tests in 34 files, 181 e2e
      tests) and says that §7's 223/164 belong to the 2026-10-30 run it describes.
    - ~~`DELIVERY-DRILL.md` §6.4 says first-boot logs print the seed passwords; §6.10 says
      that was fixed.~~ Fixed: §6.4 now says the log names the accounts and never the
      passwords, names `PIFORGE_SEED_PASSWORD`/`PIFORGE_SEED_USER_PASSWORD`, and points at
      `cmd/piforged/seed_log_test.go`.
    - ~~`SANDBOX-DEPLOY.md` §8 says `deploy/Dockerfile.runner` "was never actually built",
      while §1.4.1 records building and running it on Docker Desktop.~~ Fixed: §8 now says
      the runner image *was* built and the compose *did* run on Docker Desktop, and that
      what is unverified is the Linux host (volume mapping, `volume-subpath`, Engine) and
      the runner's end-to-end path.
    - ~~Two control-plane build commands, neither canonical.~~ Fixed: the canonical command
      is `pwsh scripts/build-app-image.ps1` (`-Tag` decides the tag) in `DELIVERY-DRILL.md`
      §5.1, `SANDBOX-DEPLOY.md` §1.4.1, `HANDOFF.md` "Run it" and `deploy/README.md`; the
      full `docker build --build-arg INSTALL_CHROMIUM=true -t <tag> .` is named only as the
      underlying from-scratch build.
    - Still true: the image running here (`sha256:15eb2f9c14ad…`, tag `piforge:local`,
      rebuilt 2026-10-31 after the interrupt fix) is **not** the image the delivery drill
      documents (`sha256:2cdba2cd…`, 2026-10-30 16:58); the drill's 119 assertions *were*
      re-run on the current build (`pwsh scripts/deliver-check.ps1`, run `20261031-215046`: 119 PASS / 0 FAIL, exit 0, on image `sha256:15eb2f9c14ad…`) but the drill
      document itself still describes the older one.
    - Still true: `README.md`'s local-run section (8080 + `pnpm dev` on 3000) never mentions
      that the image serves everything on one port; someone following only the README will
      not reproduce this deployment.


### Sandbox liveness (the fork-storm wedge)

Since 2026-11-01 (second adversarial acceptance, `docs/EXECUTION-BOUNDARY.md` §15.1): a
process storm inside a sandbox saturates its `pids` cgroup, and from that moment the
container cannot spawn a single process — `docker exec` fails with `unable to spawn
stage-1: Resource temporarily unavailable`, the disk measurement above times out, and
the agent cannot start — while the container still reports `running`. The only recovery
used to be a manual pause + resume.

Two things now happen, and the second is the guarantee:

- **The default budget dropped from 512 to 256** (`deploy/tenant-limits.json`,
  `PIFORGE_SANDBOX_PIDS`), because the image's own runtime idles at 83–85 pids
  (25 processes) and 256 leaves ~170 slots for real work while halving what a storm can
  park in one cgroup. Measured with `pwsh scripts/measure-pids.ps1`: the runtime is
  complete at ≥128 and degraded at 64; a storm fills *every* limit from 64 to 1024, so
  a smaller number alone never prevents the wedge.
- **A watchdog detects and repairs it.** `httpapi.StartSandboxWatch` (started by
  `cmd/piforged`, 10 s interval) runs `true` through each READY/BUSY/IDLE sandbox's own
  API with a 3 s bound. Two consecutive failures = wedged; the repair is `Pause` +
  `Resume` (measured ~5 s: `docker stop` 4.99 s + `docker start` → command works in
  0.91 s), verified by probing again (up to 45 s), and the workspace volume is untouched,
  so the checkout, the transcript and the agent's files survive while every process of
  the storm dies. The session gets `platform.error` `sandbox_unresponsive`
  「沙箱无响应，正在恢复」 before the stop and `sandbox_recovered` 「沙箱已恢复（用时 5.9 秒）…」
  after it — or `sandbox_recovery_failed` with what to do next.
- **The same repair, by hand:** `POST /v1/sessions/{id}/sandbox/recover` (probes first,
  so a healthy sandbox is a no-op that says so), and the 「恢复」 button in the session
  view next to 「沙箱无响应，正在恢复」. `GET /v1/sessions/{id}/sandbox` exposes
  `responsive` (`null` = never probed, *not* "down"), `recovering` and `can_recover`.

**Still open:** this is a repair, not a resource policy — a tenant that wedges its own
sandbox in a loop will be repaired in a loop (one WARN per episode). A `ulimit nproc`
reserve was measured and rejected (`nproc=192` breaks the image's own bootstrap), and a
value high enough to boot reserves too few pids to matter. A container that is *gone*
(not wedged) is `reconcile`'s job, not the watchdog's.

## The third adversarial acceptance (2026-11-01)

The headline of that pass was not a sandbox escape: it was the **control plane reading a tree a
tenant controls**. `POST /v1/skills/import` cloned a tenant-supplied repository and read every file
under a `SKILL.md` with `os.ReadFile`, which follows symlinks — so `leakskill/key.txt ->
/proc/1/environ` returned the deployment's own environment to a normal tenant as a skill bundle, with
the real provider key in it (measured: `import 200`, `provider_key_occurrences=1`). `/data/master.key`
and another session's `models.json` were the same primitive.

**What to know if you are operating this build**

- **A repository or archive containing a symlink is refused, whole.** The refusal is a 400 naming the
  path (`导入被拒绝：仓库里的 "leakskill/key.txt" 是符号链接…`), and it happens before any skill row or
  bundle is written. That is deliberate: a tenant shipping a link is hostile, not unlucky, and a
  partial import would hide the attempt. `git clone` is also told `-c core.symlinks=false`, so even a
  repository git itself materialises cannot put a live link on the control plane's disk.
- **The rule now applies to every read of a tenant tree**, not just the importer: the skill bundle
  read-back, the workspace search fallback, `.gitignore` parsing, `git status`'s untracked line
  count and the agent log all go through the same containment (`internal/tenantfs`). The audit table
  — every entry point, what it read before, what it does now — is `docs/SANDBOX-DEPLOY.md` §1.8. If
  you add an endpoint that reads a path a tenant can shape, that table is the question you have to
  answer.
- **The disk budget now measures disk.** It is the workspace *plus* the container's writable layers
  (`/tmp`, `/var/tmp`, `/home/gem`) minus a boot baseline the image writes to
  `/run/piforge/disk-baseline`. 4.84 GB in `/tmp` with a 125 KB workspace used to produce no event;
  measured now: 1.5 GiB in `/tmp` reads as 1 539 211 060 bytes, and a second 1.5 GiB trips the quota
  with the sandbox still running.
- **The enforcer is no longer tenant-killable in one step.** It still runs as `gem` (it must, to signal
  the tenant's processes), but a root-owned supervisor loop in the image restarts it within ~0.2 s,
  and the platform's disk watchdog restarts it too when a measurement finds it missing. Measured:
  `pkill -9` as `gem` cannot signal the root supervisor or its `su` ("Operation not permitted") and
  the gem enforcer is back with a new pid; `kill -9` on the supervisor from `gem` fails the same way.
- **`pids` is 512 again and detection is faster.** The image idles at 216/256, so 40 forks used to
  wedge a sandbox; 512 leaves ~300 slots (measured `pids.max=512 pids.current=191`). The liveness
  watchdog probes every 5 s and confirms a miss 1.5 s later. Measured with that pass's own storm: the
  first failed probe at 05:01:00.810, the repair at 05:01:02.374 (1.56 s, the confirmation), the
  sandbox answering again at 05:01:20.122 (17.7 s of repair, 19.3 s end to end) — against the 76 s
  detection and 87 s recovery measured before. A loaded host that misses one probe still restarts
  nothing.

**What is still open** (unchanged by this round): `file://` import URLs have no allow-list; a clone
still writes the whole working tree before the 4 MB read cap applies; hard links are outside the
containment check (neither transport can create one today); and "root filesystem minus baseline"
counts what the sandbox's own services write, which is honest but means an idle sandbox grows slowly.
The full list is at the end of `docs/EXECUTION-BOUNDARY.md` §17.

## The fourth adversarial acceptance (2026-11-01)

The finding was not a file read: the **control plane would fetch any URL a tenant named, from its own
network position**. `POST /v1/mcp/servers` accepted `http://piforge:7700` (the control plane's own
loopback) and `http://169.254.169.254` with no check, and the broker then reverse-proxied the sandbox's
requests there; `POST /v1/providers` + `/test` was the same fetcher without the broker. Measured from
inside a sandbox with no DNS and no route: `GET /gw/mcp/<tok>/healthz -> 200 {"status":"ok"}` and
`GET /gw/mcp/<tok>/anything -> 200 {"you_are":"172.26.0.2","marker":"A4-ONLY-…"}`. The control plane has
internet, so the sandbox had an HTTP channel out. No key, file or database row was read.

**What to know if you are operating this build**

- **Every tenant-supplied fetch target now goes through one policy** (`internal/urlguard`,
  `docs/SANDBOX-DEPLOY.md` §1.9): http(s) only, no localhost / docker host / compose name / this
  container, and every resolved address must be public (loopback, 169.254/16, 10/8, 172.16/12,
  192.168/16, fc00::/7, unspecified, multicast are refused). It is applied when the URL is saved **and**
  again when it is dialled — the dial is pinned to the verified address, so a DNS answer that changes
  between the two still fails — and redirects are re-validated hop by hop. A refusal is a 400 that names
  the reason and the host, at both points.
- **It covers**: MCP server URL (save + `/gw/mcp/<token>`), provider `base_url` (save, `/test`, and the
  real `/gw/llm/<token>` traffic), project forge `api_base` (save + the PR call), and skill import over
  http(s)/`git@`. The table is §18.2 of `docs/EXECUTION-BOUNDARY.md`; a new endpoint that fetches a
  tenant URL and is not on it is a bug.
- **An internal MCP server or provider still works, but only if you write it down.** Create
  `deploy/url-policy.json` (mounted read-only at `/etc/piforge/url-policy.json`) with
  `{"allow":[{"host":"mcp.internal.example.com","ports":[443]}]}`, or pass `-url-allow-file`. Default is
  empty, which is "public http(s) only". The file is parsed at boot and a typo fails the start; the boot
  log prints `outbound URL policy policy=… covers=…`. The allow-list can only *widen* — it cannot make a
  public target refuse — and it is scoped to the host (or CIDR) **and** the ports.
- **The skill clone is bounded now.** It used to write the whole checkout before the 4 MB read cap: a
  268 MiB repository left 268 800 878 B in the control plane's `/tmp`. It clones with
  `--depth 1 --single-branch --no-tags`, a watchdog measures the directory every 25 ms and kills git at
  `-skill-clone-max-bytes` (16 MiB default), the directory is deleted, and the import is refused with a
  400 naming the cap and the bytes measured.

**What is still open**: `git@host:path` is host-checked only (the HTTP address pinning does not apply to
ssh); the allow-list is host/CIDR + port, not path; a host checkout's `remote.<name>.proxy` and
credential helpers are not read by the G6 guard. `file://` skill imports are confined to
`-skill-import-roots` now (empty means refuse). The full list is §1.9.6 / §1.10 of
`docs/SANDBOX-DEPLOY.md`, §14 of `docs/DATA-FLOW-AUDIT.md` and §18.3 of `docs/EXECUTION-BOUNDARY.md`.

## The data-flow audit, second pass: G5–G8 (2026-11-01)

The first pass (`0d3a2bc`, `25dfe8e`, `e303c82`) closed four edges and left four open. This pass closes
them; `docs/DATA-FLOW-AUDIT.md` carries the verdicts, the test names and the remaining residuals, and
`docs/SANDBOX-DEPLOY.md` §1.10 is the operational half.

- **G7 — the status was the bug.** `POST /git/branch {"name":"-D"}` answered 500 with
  `gitx: revision "-D" may not start with '-'`; the guard held, the status lied. Every syntactic refusal
  from `gitx` is now a typed error (`ErrInvalidRequest` / `ErrPathEscape`) mapped once in
  `httpapi.classify`, so branch, checkout, commit, diff (commit / branch base / range), log and the
  provisioning worktree path all answer 400.
- **G8 — the export is a spreadsheet.** A tool name beginning `=`, `+`, `-`, `@`, TAB or CR was a live
  formula in the admin's CSV. Every string column is prefixed with a quote (`csvFormulaSafe`), tested
  with `=cmd|' /C calc'!A0`.
- **G6 — the remote was the tenant's to rewrite.** In host placement `git push` dialled whatever `origin`
  the agent had rewritten in the sandbox. Before any push/pull/fetch (and the PR's push) the control
  plane now reads every `remote.*.url`, `remote.*.pushurl` and `url.<base>.insteadOf`
  (`gitx.RemoteTargets`), runs it through the same `urlguard` policy as every other tenant fetch,
  refuses `ext::` and unknown transports, refuses a local path inside another tenant's sandbox, and
  answers a typed 400 with zero dials. A legitimate internal remote is allowed with `-url-allow-file`
  (host + ports). Not wired under a container placement: git runs in the sandbox there.
- **G5 — a policy with a stated cost.** With `-sandbox-browser host` the panel's Chrome is this
  process's child, so a page it rendered came from the control plane's network position. A loopback page
  is now allowed only on a port the deployment published — `-browser-localhost-ports`
  (comma-separated) plus the port of `-web-upstream` when it is loopback (the image's own UI on 3100) —
  and never on the control plane's own listener; 169.254/16 (metadata), private and docker-host names
  stay refused. Default is "no loopback navigation"; an agent-started dev server on an undeclared port
  is not viewable in host placement, which is the cost. Container placement runs no rule, and the drill
  (browser inside the container) is unaffected; the e2e suite declares
  `-browser-localhost-ports 3100,3101`.

**If you operate this build**: publish the ports you want viewable
(`PIFORGE_BROWSER_LOCALHOST_PORTS=3000,3100`), and if you push to an internal git server from a host
workspace, name it in the URL allow-list with its port. Both refusals name the rule and answer 400, not
500.

## The sixth adversarial round: git is an interpreter and a second HTTP client (2026-11-01)

Adversarial round 5 (`e2e/.artifacts/adversarial5/REPORT-adversarial5-git-and-paths.md`) attacked the
G1–G8 fixes and showed that all of them described *argv*. Git is also a command interpreter and a second
HTTP client, and it runs on repositories the tenant's agent can write. Five gaps (`G9`–`G13` in
`docs/DATA-FLOW-AUDIT.md`), each proved fail-first on a host-placement instance (`-sandbox local`, port
8091) and then re-proved as a refusal:

| # | attack (before) | after |
|---|---|---|
| G9 | `core.fsmonitor = sh -c 'printf FSMONAPI > marker; exit 1'` + `GET /git/status` → **200, marker written**; `core.sshCommand = printf SSHEXEC > marker; false` + `POST /git/push` → **500, marker written** | `marker=absent` from both routes. Every control-plane git invocation carries `-c core.fsmonitor=false`, `core.hooksPath=<empty 0700 dir>`, `core.sshCommand=<absolute ssh resolved at start-up>`, `core.pager=`, `core.askPass=`, `core.gitProxy=`, `credential.helper=`, `commit.gpgSign=false`, `tag.gpgSign=false`, `protocol.ext.allow=never`, `protocol.file.allow=user`, `http.followRedirects=false`; every inherited `GIT_*` (including `GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_n`) is dropped before git starts (`internal/gitx/harden.go`). Command scope outranks `.git/config` |
| G10 | `POST /skills/import {"url":"https://postman-echo.com/redirect-to?url=http://127.0.0.1:7700/healthz"}` → the importer's git **fetched the control plane's own loopback** (`fatal: repository … not found`), and the same 302 to a closed port said `Failed to connect to 127.0.0.1 port 7999` | 400 with `The requested URL returned error: 302` — a redirect is a refusal, not a fetch. No `/healthz/info/refs` line in the control plane's log |
| G11 | a **junction** inside an import root returned the outside repository (`leak=LEAKED`); `.git./config` uploaded as a bundle → **200** and `.git\config` on the volume; a junction flipped during imports leaked **14/87** | junction → 400 (the platform's `FILE_ATTRIBUTE_REPARSE_POINT` + `GetFinalPathNameByHandle`, because `EvalSymlinks` and `os.Lstat` do not see a junction); `.git.`/`.git `/`.git..` → 400; the clone is handed the *resolved* directory and a name that does not exist at check time is refused, so the flip ended at **0/116** |
| G12 | `repo_url: root@127.0.0.1:acme/repo.git` → 201, `ssh: connect to host 127.0.0.1 port 22` | 400 naming `urlguard: host "127.0.0.1" is refused: loopback`, 0 ssh dials. Any `[user@]host:path` is parsed as ssh and judged on port 22 |
| G13 | `url.git://127.0.0.1:9912/.pushInsteadOf https://93.184.216.34/` → the push **dialled loopback** (listener accept); `remote.origin.proxy = http://127.0.0.1:9912` → the fetch **CONNECTed to it**; `origin = 127.0.0.1:9912/x.git` (no `user@`) → ssh to port 22 | 400 on all three, 0 listener accepts. `RemoteTargets` reads `pushInsteadOf` and the scp form without a user; `RemoteConfigIssues` refuses repository config keys that name a program (`core.sshCommand`, `core.fsmonitor`, `remote.*.proxy|uploadpack|receivepack`, `credential.helper`, `filter.*`, `diff.*.textconv`, …) in the local and worktree scopes only |

**Operator-visible consequences, stated.** (1) A tenant repository that sets `core.sshCommand`,
`core.fsmonitor`, an `include.path`, a credential helper or a `remote.*.proxy` is **refused** before a
push/pull/fetch, with a 400 naming the key — remove the key. (2) An ssh remote still pushes: the
deployment's own `ssh`, resolved to an absolute path at start-up, with the deployment's keys
(`-url-allow-file` is still how a private/internal *host* is allowed; there is no flag for a key).
(3) `git` no longer follows an HTTP redirect for a clone or a fetch; a mirror that answers a 302 must be
named by its final URL. (4) In host placement `ssh` must be on `PATH` at start-up; if it is not, an ssh
remote fails closed with git's "cannot run" message instead of inheriting the repository's command.

**Build/acceptance change.** `scripts/up-isolated.ps1` used to build only what was *missing*, so a
source change kept the old image and an audit probe measured the previous binary. It now compares each
image's creation time with the newest tracked or untracked source file under `backend/`, `web/` and
`deploy/`, rebuilds what is behind (and the sandbox image when the control plane it is built from
changed), and prints every image's id and build time. `-Rebuild` still forces all three.
`scripts/probe-data-flow.ps1` now creates its own fixtures, so it runs **18 of 18** checks on a fresh
stack (it used to run 15 of the 17 this document claimed), prints the count, and exits 2 when an edge
could not be measured.

**Gates for this round** (`go build`, `go vet`, `go test ./... -count=1`, `pnpm --dir web test -- --run`,
`pnpm --dir web exec next build`, full e2e 182, `deliver-check.ps1 -Image` with 125 checks) — the numbers
are in `docs/STATUS.md`.
