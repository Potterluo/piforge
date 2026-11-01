# The execution boundary: moving the agent process into the sandbox

`docs/SANDBOX-DEPLOY.md` §1.4.2 and `docs/HANDOFF.md` Known limit #1 record one measured
fact: with `-sandbox runner`, `ps` in the control-plane container shows

```
root  37  11  /app/bin/piforged agent-launcher … -- pi --mode rpc …
root  45  37  pi
```

while `ps` inside the sandbox shows no `pi` at all. The sandbox is a **workspace**
boundary (files, terminal, browser, panel git all live there); it is not an **execution**
boundary. The agent — and every `bash`/`node`/`git` it spawns — runs beside the control
plane, with the control plane's view of the shared volume as its cwd, and can therefore
read `/data`: the SQLite database, `master.key`, `skills/`, and every other tenant's
`/data/sandboxes/<user>/<project>`.

This document is the design for closing that gap for the `docker` and `runner`
providers. It is written before the code, and it is deliberately staged: stages
1–3 are implemented, stage 4 is not, and each stage names the test that accepts
it.

## 1. The two things that have to move

1. **The process.** `pi --mode rpc` must be a child of the sandbox container, not of
   `piforged`.
2. **The bytes it reads and writes.** The RPC channel (JSONL on stdin/stdout), its
   session file, its model configuration, its skills, its MCP configuration. If the
   process moves but the channel still terminates in the control plane's filesystem,
   nothing is gained.

The panels are *not* in this list. File tree, terminal, browser and panel git already
call the sandbox's own HTTP API (`internal/workspaceremote`, `internal/term/remote.go`,
`internal/browser`, `sandbox.Docker`/`Runner.Endpoint`). They keep working unchanged,
which is the strongest evidence that the sandbox API is the right seam — §7.

## 2. The RPC channel: what replaces an inherited stdio

Today the bridge (`internal/pi`) spawns `pi` with `cmd.StdinPipe()/StdoutPipe()` and
frames stdout on `'\n'`. When the agent is remote, the control plane needs a
bidirectional **byte** channel to a process it does not own.

Three candidates, and why the third wins for stage 1:

| Channel | Shape | Verdict |
|---|---|---|
| Framed protocol over the sandbox's `/v1/shell/exec` | JSON `{seq,channel,data}` written to a persistent shell session, output polled from `/v1/shell/view` | Rejected. That session is line-oriented and shared with the user's terminal; it has no EOF, no binary safety and no backpressure. JSONL records contain newlines inside strings; encoding them again is a second framing layer over a transport that already mangles them. |
| Newline/JSON frames over a TCP socket the sandbox dials back | control plane listens, a shim inside the sandbox connects out | **Stage 3, with one change.** The shim is real and it is in the sandbox — but it is a *supervisor* with a Unix socket, not a client that dials back: the control plane must be able to attach whenever it likes, including after a restart, and a connection the sandbox established would have to be re-established on a schedule it cannot know. See §6. |
| **The container runtime's own exec as a byte stream** | `docker exec -i -u <user> -w <dir> <container> <argv>` with stdin/stdout spliced to one hijacked HTTP response | **Stage 1.** The runtime already has a correct, binary-safe, EOF-correct stdio primitive. The runner already holds the Docker CLI; this exposes exactly that primitive and nothing else. |

Stage 1 therefore adds **one** endpoint to the runner:
`POST /v1/sandboxes/{id}/attach` with `{argv, dir, env, user}`. The server hijacks the
connection, answers `200` with no `Content-Length`, then splices the socket to
`docker exec -i`. Auth is the existing bearer token; there is no new secret. The
tradeoff this buys and costs is stated in §9.

The client side is `sandbox.Attacher` (`internal/sandbox/attach.go`). `pi.Runtime`
already has the seam for it: `RuntimeConfig.Dial` — *"a production deployment can use it
to reach Pi through a sandbox shim's stdio channel"* — so the bridge, its framing,
command correlation and event fan-out are reused verbatim. What changes is only *where
the file descriptors come from*.

**What the agent is launched as.** The argv the control plane sends is

```
/opt/piforge/piforged agent-launcher -root /workspace -session <sid> -interval 15s \
  -- pi --mode rpc --model piforge/<model> --skill /workspace/repo/.pi/skills \
     --session /workspace/.piforge/sessions/<sid>.jsonl
```

`agent-launcher` is the existing liveness supervisor (`internal/liveness`), running
*inside* the sandbox from a copy of the same binary. Its marker therefore lands at
`/workspace/.piforge/live/<sid>` — the exact path the existing probe already reads
through the sandbox API — and the supervisor becomes the agent's direct parent again,
which is what the marker's semantics require.

## 3. `materialize` and `provision` leave the shared volume

`provision` used to do two host-side things that assumed the control plane's
filesystem *is* the sandbox's: `git worktree add` and `materialize.Mount` (skills,
`.mcp.json`). Both are now sandbox API calls (stage 2, implemented):

- **checkout / worktree**: already partly true — `sandbox.Docker.ensureCheckout` and
  `AddWorktree` run `git` through the sandbox's `/v1/shell/exec` and the runner's
  `…/worktrees`. The remaining host-side `gitx` calls in `provision.go` moved behind
  the same interface (`contracts.WorktreeSandbox`), so no branch logic reads the host.
- **materialize**: `materialize.Mount` writes a skill tree into the checkout and then
  makes it read-only (`bind-ro`/`copy-ro`). Over the API that is: `PUT /v1/file/write`
  per file (or one tar stream, see below), then one `chmod -R a-w` through
  `/v1/shell/exec`. The read-only guarantee is the *mode bits*, which is what `copy-ro`
  already produces on the host; a bind mount is not available from outside the
  container.
- **file payloads**: the sandbox `PUT /v1/file/write` takes a JSON string or base64. A
  skill bundle is text; the agent runtime is not. Binary trees (the `pi` package) ship
  as one gzip, uploaded once per container, unpacked by one `tar xzf` — stage 2's
  `stageAgentRuntime`, with a version marker so it runs once, not per session.

The acceptance test for stage 2 is not "the files appeared": it is **the host
workspace is not required at all** — run with the runner's data root on a volume the
control plane never mounts (`SANDBOX_WORKSPACE_VOLUME` unset, `docker run --volumes-from`
the sandbox only) and complete one prompt + one commit.

### 3.1 What stage 2 actually did (and how)

The shape that made this tractable: **the projection is the same code, and only the
tree it writes into differs.**

| Piece | Before (stage 1) | After (stage 2) |
|---|---|---|
| Where the workspace lives | shared volume mounted by the control plane, the runner and the sandbox | a `piforge-ws-<pair hash>` volume that **only** the sandbox mounts |
| What the sandbox row says | `root_dir` = the host path `/data/sandboxes/<u>/<p>` | `workspace=api`, `root_dir` = `/workspace` (the container path) |
| Skill projection | `materialize.Mount` on the host path | `materialize.MountFS(materialize.SandboxFS…)` through `PUT /v1/file/write` + `chmod` |
| Read-only mechanism | `copy-ro` on the host (`bind-ro` where SYS_ADMIN exists) | `copy-ro` via the API; the probe runs *in the sandbox* as the agent's account |
| Branch worktree | `git worktree add` inside the container, host path recorded | same command, and the path it is given is the container path (`/workspace/worktrees/<slug>`) |
| `chmod -R a+rwX` | needed (two writers: root here, gem there) | **dropped for a detached workspace** (one writer: gem, through its own API) |
| `safe.directory` | needed (bind mount owned by another account) | **dropped for a detached workspace** (the tree is gem's) |
| `SANDBOX_WORKSPACE_VOLUME` | required by the compose overlay | **not needed with `-agent-in-sandbox`**; still the right setting for a deployment that shares one volume |

Three seams carry it, each small and independently testable:

1. `contracts.WorkspaceMode` on the sandbox row (`host` | `api`). It is written by the
   provider that created the mount and read by everything that later touches the tree.
   It survives a restart, which a flag comparison in memory would not.
2. `materialize.FileSystem` (`internal/materialize/fs.go`): `Join/IsAbs/Abs/MkdirAll/
   WriteFile/ReadFile/Lookup/List/RemoveAll/MakeReadOnly/MakeWritable/CanBind`. The host
   implementation is the old `os.*` code; the sandbox implementation
   (`materialize.SandboxFS`) is the file API. `Mount` and `MountFS` share every rule —
   freeze, manifest, git exclude, digest framing — so there is one projection, not two.
3. `provision.Config.WorkspaceFiles` + `httpapi` reading the manifest through
   `contracts.Workspace`: the provisioner and the tooling panel ask the sandbox, not the
   host, for a workspace they cannot open.

The container mount is verified from the container's own record, not from a flag: if
`/workspace` is a volume named `piforge-ws-<hash of the container>` with no subpath, the
workspace is detached. That is why a restarted control plane and the stateless runner
reach the same answer about a container neither of them remembers creating.


## 4. `models.json` — the provider key stays out, absolutely

Two different secrets are easy to confuse:

| Secret | Where it lives today | Where it may live in the sandbox |
|---|---|---|
| Provider API key (`VOLCE_API_KEY`, sealed in SQLite) | control plane only; injected by the gateway (`/gw/llm/<token>`) | **never** |
| Per-session gateway token (`signer.Issue`, HMAC, session-scoped, expires with the deployment) | `models.json` + env of the agent process | yes — it is the credential the sandbox is *supposed* to hold |

`models.json` never contained the provider key: `pimodel.Write` writes a generated
provider whose `baseUrl` is the gateway and whose `apiKey` is the per-session token.
Moving the agent into the sandbox does not change that; it changes the **transfer**:
the file is written into `<pair>/.piforge/agents/<session>/models.json` through the
sandbox file API instead of into `/data/pi-agents/<session>`, and `PI_CODING_AGENT_DIR`
points at the sandbox path. The gateway URL must be reachable *from the sandbox*
(`-gateway-url`, service name — `docs/SANDBOX-DEPLOY.md` §1.5 "网络回连"), and the token
is bound to `(session, user, provider, model)`, so a sandbox that keeps a copy keeps a
credential that is useless for another session.

**MCP was the case that needed a broker, and stage 3 built one.** `pimodel.WriteMCP`
decrypts MCP credentials and writes them as environment variables into `mcp.json`;
writing that file into a tenant's sandbox is exactly what this document exists to
stop. Stage 3 therefore brokers **HTTP** MCP servers:

- the control plane mints a random token per `(session, server)`, stores its hash and a
  sealed copy in `mcp_tokens`, and writes `<gateway>/gw/mcp/<token>` into the sandbox's
  `mcp.json`;
- `POST /gw/mcp/<token>` resolves that token, loads the server, decrypts the credential
  and injects it as headers on the way out (`KEY=value` per line of the secret blob =
  one header, e.g. `Authorization=Bearer sk-…`), streaming the answer back;
- `DELETE /v1/sessions/{id}/mcp-token` revokes every live token of the session, and a
  revoked token is refused on the next request **and not re-minted** by a respawn.

What the sandbox holds is therefore a revocable, per-session, per-server credential
rather than a copy of the user's.

**A stdio MCP server is refused, and stage 4 made the refusal part of the product.**
The transport is the reason, and it is worth stating precisely because the obvious
alternatives were considered and rejected (§11.1): Pi starts a stdio server *inside*
the sandbox, and such a server authenticates with credentials in its own environment.
There is no point on that path where the control plane could inject anything — the
process does not call the gateway at all, it calls its upstream directly. So "stage it
and brokered" is not expressible; the choices are "put the user's credential in the
container" (what this document exists to stop), "run it on the control plane" (worse:
tenant-configured code beside the database), or "do not stage it". Stage 3 chose the
third and only logged it, which left a user staring at a fully-enabled server the agent
never had. Stage 4 keeps the refusal and makes it visible: `GET /v1/mcp/servers` and
`GET /v1/sessions/{id}/tooling` carry the omission with a code and a reason
(`internal/mcpstage`), the checkout's mount manifest records it in
`mcp_unavailable`, and the MCP panel shows 「stdio MCP 服务器在隔离模式下不可用」 with that
reason.

## 5. Liveness and reconciliation with a remote agent

The marker (`internal/liveness`) already exists and already points the right way: a
writer inside the workspace, a reader through the sandbox API. Stage 1 changes who
writes it — the supervisor now runs in the sandbox — and nothing else:

- the marker path and beat/TTL are unchanged (`/workspace/.piforge/live/<sid>`, 15 s,
  2 min);
- `sessionLivenessProbe` is unchanged, so a restarted control plane can still answer
  "alive" from a sandbox it did not start;
- what changes is that the answer becomes **true**. Before stage 3, `kept_live=1` after a
  restart meant "the marker is 40 s old but the process it described died with the
  control plane". Since stage 3 the process really does outlive the restart, and the
  measured restart test (`docs/SANDBOX-DEPLOY.md` §1.4.5) shows the same `pi` pid, a
  fresh marker and a run that settles instead of being interrupted.
- stage 2 shifted the marker's *home*: it stayed at `/workspace/.piforge/live/<sid>`,
  but the workspace is now a volume only the sandbox mounts, so the writer and the reader
  are the only two parties that touch it. The marker still carries a pid and not a boot
  id — §10.1 item 3.

## 6. Surviving a restart: the sandbox-side supervisor

Stage 1's channel was owned by the control plane: restart it and `docker exec` sees EOF
on stdin, `pi` exits, the launcher removes the marker, and reconciliation answers "dead".
Stage 3 interposes a process that owns the agent's descriptors instead:

```
control plane ──runner attach/reattach──▶ piforged agent-attach ──unix socket──▶ supervisor ──pipes──▶ agent-launcher ──▶ pi
```

`piforged agent-supervisor` (inside the sandbox, `internal/agentsupervisor`) is started
detached — its own session, no controlling terminal, stdio on
`/workspace/.piforge/logs/<session>.supervisor.log` — and it listens on
`/workspace/.piforge/run/<session>.sock`. It owns the agent's stdin/stdout/stderr as
pipes, so nothing the control plane does to its own connection can reach the agent. On
connect it writes one JSON line and then the agent's bytes:

```
{"type":"ready","session":"…","pid":812,"started":"…","replay_from":0,"replay_bytes":2579}
…the agent's stdout, exactly as written…
```

The control plane consumes that one line in `sandboxAgent.Dial` (`handshake`), so the
RPC bridge, its framing and its id correlation are the unchanged code. There are two
ways to get such a stream, and they are deliberately different verbs:

- **reattach** (`POST /v1/sandboxes/{id}/reattach`, the eighth runner endpoint): connect
  to the supervisor that is already there. Its body is a socket and a helper argv, never
  an agent argv, so "start a second agent on reattach" is not expressible;
- **attach** (stage 1's verb): start `piforged agent-supervisor-start`, which starts a
  detached supervisor *if none is listening* and then attaches. A first launch and a
  reattach therefore reach the same handshake.

After the handshake, `main` runs `Server.ReattachLiveSessions` **before the port opens**:
it asks the liveness probe which sessions really have an agent, and calls the same
`hub.ensure` a message uses. The subscription, the event pump and the derived run state
resume exactly as they do for a fresh process, so the run settles into the same row
instead of being marked interrupted.

Three honest costs, all measured:

- **The replayed bytes are not free.** The supervisor keeps the last 8 MiB of agent
  stdout in a record-aligned window and resumes a reattaching client at the last byte it
  handed to the previous one. Bytes that the previous control plane had already persisted
  are therefore delivered again — bounded by what was in flight (one socket buffer plus
  whatever it had not yet processed), not by the run. The measured drill replayed 2579
  bytes with `gap_bytes=0`.
- **A longer outage loses bytes, not the agent.** Beyond the window the supervisor drops
  whole records and reports the count as `gap`. The conversation itself is Pi's session
  file inside the sandbox, and PRD §7.4.5 still forbids replaying a *run* on the control
  plane's side: what resumes is the stream, not the turn.
- **The gateway signing key is now on the data volume** (`/data/gateway.key`). An agent
  that outlives the control plane keeps using the token it was started with, so a
  per-process random key would make every survivor fail its model calls after a restart.
  The file is 0600, created on first boot, and must be backed up with the database.

The agent's stderr no longer goes only to the runner's log: the supervisor writes it to
`/workspace/.piforge/logs/<session>.log` and `sandboxAgent.Logs` reads it through the
file API, so the Logs panel shows the same bytes `docker exec cat` would.

Two smaller ones, both consequences of the session file living in the workspace:

- **Fork works, clone does not.** A fork adopts the path Pi reports (a container path)
  and `Dial` accepts it as-is; the control plane does no host-side `mkdir` on it.
  `CloneSession` copies the JSONL *on the host*, from a container path that does not
  exist there — it fails with a readable error instead of corrupting anything. Stage 2
  did **not** move this through the file API; it is remaining work (§10.1).
- **`Status.session_file` reports the container path** (`/workspace/.piforge/…`). With a
  detached workspace that is now the only path there is: it is informational and correct
  for the process that owns it, but it is not a path the operator can `ls` from the host.

## 7. What does not change: the panels

Terminal, browser, file tree, panel git, tests panel, inline review — all of them talk
to the sandbox's HTTP API and already run inside the container. They are the control for
this change: they must behave identically before and after, and the drills exercise the
same panel surface as §1.4.2 (branch/status/diff/stage/commit, inline comment, test run)
to prove it. Stage 2 exercised the same surface again with no workspace mount at all —
that is what makes the panel evidence meaningful rather than incidental.

## 8. `-sandbox local` is untouched

`local` has one filesystem, one user and no container: the agent is this process's
child, the in-process registry is the truth, and there is nothing to attach to. The flag
is rejected unless the provider is `docker`/`runner`, `Dial` stays nil, `WrapArgv` still
wraps the local spawn, and every existing test keeps running that path. Stages 1 and 2
kept the flag off by default for exactly that reason — the default deployment must not
change behavior until the docker/runner path has been proven on a throwaway instance.
**Stage 3 flips it, and only for the container providers**: `resolveAgentInSandbox` makes
it on for `docker`/`runner` and off for `local`, so the local shape is not a special case
that had to be re-tested — it is the one that never changed. `-sandbox` itself still
defaults to `local`, which is what development uses.

Stage 2 keeps that promise in the code, not only in the deployment: `DetachedWorkspace`
is a `DockerConfig`/`RunnerConfig` field that only `-agent-in-sandbox` sets, the local
provider never sees it, and `materialize.Mount` (the host path) is still what runs for
`local` and for a shared volume. The flag also gained one requirement — with it on, the
file panel must be remote, because there is no host tree left to read (a startup error,
not a silent coupling).

## 9. What it costs, stated plainly

- **A seventh runner endpoint.** §1.4 says "no exec, no file endpoint" and this adds
  exec. The narrowness that mattered was *authority over the daemon*, which the runner
  already has; attach is not a new privilege, it is a new verb on a service that can
  already create arbitrary containers. It is still an increase in attack surface: with
  the runner token you get a shell in every sandbox. The mitigation is deployment
  (7801 is internal-only, bearer token), not the endpoint's shape.
- **The agent's cwd is a different string.** `spec.Worktree` is a host path; the agent's
  is `/workspace/…`. `pi.RuntimeConfig.AgentWorktree` makes the translation explicit in
  one place instead of leaking `/data/...` into the session file header. Stage 2 makes
  the container string the *only* one for a detached sandbox — the worktree row says
  `/workspace/...` too — so the translation is now an identity in that case, which is
  why `workspaceMode` is stored rather than re-derived.
- **Custom sandbox image.** The agent runtime must be *in* the sandbox image
  (`deploy/Dockerfile.sandbox-agent`), not uploaded per session. An image without `pi`
  fails loudly at launch, never falls back to running beside the control plane.
- **Stage 2 costs an HTTP request per projected file.** A skill bundle is a handful of
  small files written once per provisioning pass; the alternative (a tar upload) is one
  request but needs a `tar` round trip in the sandbox and a version marker to be safe
  against a partial extract. Per-file was chosen because a half-written skill tree is
  visible (`ReadFS` reports what is there) while a half-extracted tar is not.
- **The workspace volume is now unmanaged by the control plane.** `Destroy` still keeps
  it (that was already true for the shared volume), but nothing lists it, and a
  deployment that deletes sandboxes by hand has one more object to look for:
  `docker volume ls --filter name=piforge-ws-`.

## 10. Staged migration

| Stage | Content | Flag | Acceptance test |
|---|---|---|---|
| 1 | `pi` runs inside the sandbox via `docker exec` over the runner's attach endpoint; model config through the file API; heartbeat inside; provider key still only in the control plane | `-agent-in-sandbox` (env `PIFORGE_AGENT_IN_SANDBOX`), default **off** | On a throwaway deployment: `ps -ef` **inside** the sandbox contains `pi` and `ps -ef` in the control plane does not; `cat /data/piforge.db` fails inside the sandbox; one real prompt commits in the sandbox worktree; panels/tests/inline review work; restart kills the agent for the stated reason. **Done** (`docs/SANDBOX-DEPLOY.md` §1.4.3). |
| 2 | `materialize` + `provision` through the sandbox file API; the workspace is a volume of the sandbox's own; `chmod a+rwX` and `safe.directory` dropped where one writer owns the tree | same flag | The same prompt completes with **no host mount of the workspace** in the control plane; a skill bundle is read-only inside the sandbox; `git worktree add` for a branch session happens entirely through the API. **Done** (`docs/SANDBOX-DEPLOY.md` §1.4.4). |
| 3 | Detached supervisor: agent survives a control-plane restart, control plane reattaches; MCP credential broker; agent stderr through the file API | flag **on by default** for docker/runner | Restart the control plane mid-run: the same `pi` pid answers `get_state` after reattach; the run settles instead of interrupted; a run whose control plane is gone for longer than TTL is honestly interrupted (no replay). **Done** (`docs/SANDBOX-DEPLOY.md` §1.4.5). |
| 4 | Remove the flag; delete the in-process host spawn for container providers; `sandbox.Docker.Attach` becomes the only path; a K8s `pods/exec`-shaped provider | — | Full gates (Go, web, e2e, delivery drill) green with the flag gone, and `ps` proofs hold on a Linux host, not only Docker Desktop. **Done for the flag and the host spawn (2026-11-01, `docs/SANDBOX-DEPLOY.md` §1.4.6); the K8s provider is not started.** |

Stages 1–4 are implemented. Stage 3 made the sandboxed placement the default for
`docker`/`runner`; stage 4 deleted the switch, the execution it selected and the
plumbing that only existed to choose between them (§11). `docs/SANDBOX-DEPLOY.md`
§1.4.3, §1.4.4, §1.4.5 and §1.4.6 record them with raw
`ps`/`mount`/`chmod`/commit/restart/`grep` output.

### 10.1 What is still not moved (named, not hidden)

These are the pieces stages 1–3 did **not** close. Each one is a deliberate exception,
an accepted cost, or work that needs a K8s provider to exist first:

1. **`worktrees.base_commit` is empty for a detached workspace.** Reading `HEAD` is a
   `git rev-parse` on a path the control plane cannot open, and the runner exposes no
   exec for it. The column is informational (nothing branches on it); the panel's diffs
   come from the container's own git. Fix: return `HEAD` in the `…/worktrees` response,
   or let the provider answer a `head` question through the API it already uses.
2. ~~Agent stderr goes to the runner's log.~~ **Done in stage 3**: the supervisor writes
   `/workspace/.piforge/logs/<session>.log` and `sandboxAgent.Logs` reads it through the
   file API (`RuntimeConfig.LogsFor`). `pi` itself prints nothing to stderr on a healthy
   run, so the panel is legitimately empty until the agent fails — which is what it is
   for.
3. **The marker carries a pid, not a boot id.** A probe on a restarted container can
   still read a marker written by a previous boot as proof of life. The window is the
   marker TTL (2 min). Not fixed by stage 3; the supervisor's socket makes the lie much
   less likely (a dead supervisor removes both the marker and the socket).
4. **The agent runtime is in the image, not staged per container** (`deploy/Dockerfile.sandbox-agent`).
   §3's `stageAgentRuntime` tar upload is unnecessary while the image carries `pi`, and an
   image without it fails loudly at launch. If a deployment ever needs an image it does
   not control, that upload is still the way.
5. **The idle-reclaim snapshot cannot index a detached workspace.** `snapshot.Take`
   walks `RootDir` on the host; a container path is not walkable, so the take fails and
   the reaper leaves both the row and the container alone (its documented rule). Reclaim
   therefore does not run for these sandboxes until the index is built through the API.
6. **The disk-quota measurement reads 0 for a detached workspace** for the same reason
   (`runtimedirs.Measure` on a host path). Enforcement never refuses a run it cannot
   measure, which is the safe direction, but the number is not true. Fix: a `du`-shaped
   question through the sandbox API.
7. **`models.json` / `mcp.json` staging still reads the host copy** under
   `<dataDir>/pi-agents` and writes it into the sandbox. Those files hold deployment
   state (a gateway URL, a per-session gateway token, a per-session MCP broker token),
   not workspace, so they stay on the state volume by design. For `mcp.json` that is now
   a *requirement* as well as a choice: the host copy must be rendered without the real
   credential, and it is.
8. ~~A stdio MCP server is still not staged under the flag.~~ **Decided in stage 4, and
   made visible instead of logged.** A stdio server is not staged into an isolated
   sandbox — its credential would have to be an environment variable in the container
   and the transport offers no egress point to inject it at (§11.1 records why running
   it on the control plane instead was rejected). What changed is where the refusal
   appears: the MCP panel says 「stdio MCP 服务器在隔离模式下不可用」 with the reason
   (`GET /v1/mcp/servers` → `unavailable`, `agent_isolated`), the session's tooling
   answer carries the same map, and the checkout's `.pi/mounted.json` records it in
   `mcp_unavailable` while keeping the server out of `mcp_servers`. Evidence in
   `docs/SANDBOX-DEPLOY.md` §1.4.6 (API output, the manifest inside the sandbox, and
   0 grep hits for both a fake stdio credential and the real provider key).
9. **The replay window duplicates bytes, and it is bounded.** A reattaching control plane
   is resumed at the last byte the previous one accepted, so the in-flight tail is
   delivered twice (one socket buffer's worth, not the run). This is a documented cost of
   a real reconnect, not a promise of exactly-once delivery.
10. **The gateway signing key moves to the data volume** (`/data/gateway.key`). A
    deployment that loses it invalidates every token it ever issued — which is the same
    consequence as losing `master.key`, and the reason both belong in the backup.
11. ~~`-agent-in-sandbox` is on by default for docker/runner and still off for local~~;
    **stage 4 deleted it.** The placement follows the provider: a container sandbox
    always runs the agent inside it, `-sandbox local` never does. The retired knob is
    still *recognised* so that `false` on a container placement is a startup error
    naming the flag and the reason instead of a silent downgrade (§11.2).


## 11. Stage 4: what was deleted, and the two decisions

Stage 4 is the cleanup stage: stages 1-3 built a second execution and proved it, and
this stage removes the first one so there is nothing left to switch between. The rules
it followed: `-sandbox local` is the development path and the delivery drill's path, so
nothing it needs was touched; and every deletion below is paired with a gate run
(`docs/SANDBOX-DEPLOY.md` §1.4.6).

### 11.1 stdio MCP servers: refused, visibly

Three designs were on the table.

| Design | What it means | Verdict |
|---|---|---|
| Run the server **inside the sandbox** with a scoped credential minted per session | Pi already starts it there; "scoped" would mean a sandbox-side wrapper that fetches the real credential from the control plane and puts it in the child's environment | **Rejected.** The wrapper has to hand the credential to the process it starts — a stdio MCP server authenticates with `KEY=value` in its own environment and talks to its upstream directly. So the real credential still lands in the sandbox; the token would only be a hop, not a boundary. Keeping the credential out would mean proxying the server's *upstream traffic*, i.e. TLS interception with a CA inside the sandbox, which is a much larger and more fragile thing than the HTTP broker. |
| Run the server **on the control plane** and bridge its stdio over the gateway | The credential stays here; the sandbox holds a per-session token and a byte pipe | **Rejected.** It executes tenant-configured code (`command`, `args`, `cwd` are user input) beside the database — exactly the authority this whole document removes from the control plane, and worse than the pre-stage-4 shape because the control plane would be the *parent* of that process. It also needs a second in-sandbox binary, a new endpoint, a process manager and its own cleanup path. |
| Do not stage it, and say so **in the product** | The server is configured and enabled, and the agent does not get it | **Chosen.** It is the only option that keeps the credential out of the sandbox *and* keeps tenant code out of the control plane, and stage 4's job is to stop hiding it in a log line. |

How it is surfaced, in one place (`internal/mcpstage`):

- `GET /v1/mcp/servers` returns `agent_isolated` and `unavailable: {<name>: {code,
  reason}}`; the MCP panel renders 「stdio MCP 服务器在隔离模式下不可用：<reason>」 on the row
  and one explanation above the list.
- `GET /v1/sessions/{id}/tooling` returns the same map as `mcp_unavailable`, computed
  rather than only read, so a session that has not been provisioned still answers.
- The checkout's `.pi/mounted.json` records it in `mcp_unavailable`, and the server is
  deliberately **not** in `mcp_servers`/`mcp_written` — that list is what the agent was
  given.
- The launch still logs the skip, with the same code, because an operator reading the
  container log should not have to open the UI.

### 11.2 The escape hatch: refused, not warned

`-agent-in-sandbox=false` on a container placement is a **startup error**, not a
warning-plus-degraded-mode:

```
-agent-in-sandbox=false is not available for -sandbox runner: the agent always runs
inside the sandbox, because running it beside the control plane lets it read /data
(docs/EXECUTION-BOUNDARY.md §10.1). Remove the flag to keep the isolated placement, or
run -sandbox local for an unsandboxed development deployment
```

`PIFORGE_AGENT_IN_SANDBOX=0` gets the same refusal (an older compose file is the most
likely source of it), a nonsense value is still a configuration error, and `true` is a
no-op so an existing file keeps working. On `-sandbox local` the retired switch cannot
mean anything — there is no container to put the agent in — so a stale `true` is
reported once and ignored rather than failing a development start.

The alternative (an escape hatch that boots with a warning) was rejected for the reason
this document exists: a warning in a log is not a boundary, and the resulting instance
is the multi-tenant one whose agent can read `/data`, `master.key` and every other
tenant's workspace.

### 11.3 Deleted: file + symbol

Every item was reachable only from the in-control-plane agent spawn for a container
placement, or only configured it. `-sandbox local`'s path (child process, `WrapArgv`
nil, no marker, in-process registry) is untouched.

| File | Symbol | Why it could go |
|---|---|---|
| `cmd/piforged/session_liveness.go` | `sessionAgentLauncher` | The only writer of the heartbeat marker for a locally spawned agent of a container placement. It returns nil for local, and containers now always use the in-sandbox supervisor. Deleted with its test (`TestAgentLauncherIsOnlyWiredForContainerProviders`). |
| `backend/internal/pi/runtime.go` | `RuntimeConfig.WrapArgv` + its use in `Start` | The outermost argv rewrite that existed so the liveness launcher could be the agent's parent. Nothing set it after the deletion above; `pi/wrap_test.go` deleted with it. |
| `backend/internal/liveness/launch.go` | `LauncherArgv` | Built that wrapper's argv. `LauncherCommand`, `Supervise` and `MaybeLaunch` stay — the sandbox-side launcher is still `piforged agent-launcher … -- pi`, built in `agent_sandbox.go`. `TestLauncherArgvRefusesIncompleteConfiguration` became `TestSuperviseRefusesIncompleteConfiguration` (the same validation, one layer down). |
| `cmd/piforged/main.go` | `agentGitEnv`, `agentCheckoutDirs` (+ `agent_git_env_test.go`) | Told a *host-side* agent's git that a container-owned checkout belonged to another account. The agent now runs as the account that owns the tree, so there is no "dubious ownership" to lift; `sandboxAgentEnv` stopped stripping `GIT_CONFIG_*` because nothing produces it any more. |
| `cmd/piforged/sandbox_placement.go` | `resolveAgentInSandbox` | The tri-state (flag → environment → provider default). Replaced by `rejectDisabledIsolation`, which only decides whether the start is allowed. |
| `cmd/piforged/main.go` | the `-agent-in-sandbox` switch's plumbing: the tri-state call, the `local` contradiction check, `sessionAgentLauncher` wiring, `wrapArgv` argument to `newRuntime` | The switch no longer selects anything (§11.2). |
| `cmd/piforged/sandbox_placement.go` | `validateAgentWorkspace` → `validateContainerWorkspace` | It keyed on the flag; the rule is now a property of the provider (a container placement always has a detached workspace). |
| `cmd/piforged/main.go` | `DetachedWorkspace: opt.agentInSandbox` → `true` (docker and runner), `ShellExecutor.OwnedTree: opt.agentInSandbox` → `true` | The shared-volume deployment for a container provider no longer exists, so the values are constants of the placement. The provider fields stay: they describe a shape, not a switch, and the runner's request still carries them. |

What deliberately stayed: `-sandbox-files` (it still refuses a host file panel on a
container placement, and `-sandbox local` still defaults to host), the browser/terminal
`host` placements (a mixed deployment is still allowed and still warned about), and
`DetachedWorkspace` on the providers.

### 11.4 What stage 4 does *not* claim

- **Docker Desktop is not a Linux host.** The `ps`/`/data` proofs in §1.4.6 ran on
  Windows 11 + Docker Desktop. The kernel boundary is real (the sandbox is a container
  with no `/data`), but "on a Linux host, with a Linux daemon" is still unmeasured and
  is the first thing the next stage should do.
- **No K8s provider.** `pods/exec` instead of `docker exec` is a provider, not a
  boundary change; it has not been written.
- **The idle-reclaim snapshot, the disk-quota measurement, `worktrees.base_commit` and
  in-container `CloneSession`** are still the stage-2 gaps listed in §10.1.
- **A lost `secret.key` is silently a new era.** Losing it invalidates every sealed
  sandbox API key and MCP credential; the file is re-created on the next seal, so the
  deployment looks healthy while the old sandboxes cannot be reached
  (`docs/SANDBOX-DEPLOY.md` §1.4.6 measures it). That belongs in a backup, and the
  handoff says so.

## 12. Tenancy as an operating property: networks, quotas, audit

The acceptance pass (`e2e/.artifacts/final-isolated/VERDICT.md` §8 item 3) named the
last gap: "isolation" was true for *execution* and true for *files*, but a tenant was
still only separated from another tenant by **authentication**. Every sandbox sat on
one bridge (`piforge-sandboxes`, shared with the control plane), so sandbox A could
open a TCP connection to sandbox B's port 8080 and be turned away only by the image's
own nginx and its per-sandbox key; the resource limits existed only deployment-wide
(`-sandbox-cpus` / `-sandbox-memory`), so one tenant's agent could use the host; and
`tool_calls` recorded what an agent ran but nothing in the product could read it
across tenants.

This section is what changed, what it costs, and what it still does not claim.

### 12.1 The network boundary: one bridge per tenant

**Chosen: a Docker bridge per tenant** (`piforge-t-<12 hex of the user id>`), created
by the runner when the tenant's first sandbox is created, labelled
`piforge.tenant=<name>` / `piforge.role=sandbox`, and joined by exactly two other
containers: the control plane and the runner. A sandbox joins **only** its tenant's
bridge — the shared `piforge-sandboxes` network is not part of a sandbox's
membership any more (`docker inspect` shows one network per sandbox, `soak.txt`).

**Why per tenant and not per sandbox.** The product's tenancy rule is "one sandbox per
(user, project)", so a tenant owns as many sandboxes as it has projects; the
authorization boundary the product already enforces is the *user* (a cross-user read
is 404 on every route, `15-two-tenant.txt`). Drawing the network boundary where the
authorization boundary already is keeps the two from disagreeing. A bridge per
*sandbox* would additionally separate one tenant's own projects — and would cost, per
sandbox, a network, a `docker network connect` for the control plane, another for the
runner, and its own address block. On stock Docker the address pool is the wall: this
daemon refused the 17th network it was asked for
(`all predefined address pools have been fully subnetted` — measured on Docker
29.9.2 while writing this section), so per-sandbox networks stop at roughly a dozen
sandboxes unless every deployment first writes `default-address-pools` into
`daemon.json`. Per tenant, the same ceiling buys a dozen *accounts*, which is a
number an operator can plan around, and 50 tenants cost 50 networks + 50 address
blocks — a deployment that must widen the pool either way, but at a scale where the
cost is visible before it bites.

**What is deliberately not claimed.** Two sandboxes of the *same* tenant share their
tenant's bridge and can reach each other's port 8080, where the image's per-sandbox
key still answers 401. That is a same-tenant, same-owner relationship — the projects
belong to one account — but it is not "sandboxes cannot reach each other", and this
document does not say it is. Closing it is the per-sandbox variant above, with the
address-pool cost stated here rather than discovered later.

**How the two services keep reach.** The sandbox must still reach the control plane
(it calls the model gateway `/gw/llm/<token>/…` and, for an HTTP MCP server, the
broker `/gw/mcp/<token>`), and the control plane and runner must still reach the
sandbox (browser, files, shell panels; the checkout and branch worktrees). That is why
`-sandbox-join-containers` exists: the runner joins the named containers to each
tenant bridge it creates, on every `Ensure`, so a recreated control plane is repaired
instead of silently losing the path. It is an explicit, auditable membership list,
not an accident of sharing a default network.

Measured from inside tenant 1's sandbox on the isolated stack (`isolation-probes.txt`):

```
getent hosts piforge-874760ba7dddd0e5   -> no such host
curl -m 4 http://172.28.0.4:8080/       -> curl: (28) Connection timed out after 4003 ms
nc -z -w 3 172.28.0.4 8080              -> no route / timed out
curl <own gateway baseUrl>/models       -> http_code=200
curl http://piforge:7700/v1/projects    -> http_code=401
curl http://piforge:7700/healthz        -> http_code=200
```

By name the sandbox cannot even resolve its neighbour (different bridge, different
DNS scope); by address the packets have no route (Docker drops between bridges). The
gateway path and the 401 are the two halves of "still works, still not trusted".

### 12.2 The quota boundary: a per-tenant policy, enforced where containers are made

**Chosen: a policy with a default and per-tenant overrides, in a file the deployment
mounts** (`deploy/tenant-limits.json`, `PIFORGE_TENANT_LIMITS_FILE`), plus the
deployment-wide flags as its fallback. Not a `tenant_limits` table.

The quota is part of the deployment's configuration, not its data: it is reviewed
with the compose file, versioned with the repository, and validated at startup —
`internal/tenantlimits` rejects an unknown field, a non-numeric CPU count, a memory
string Docker would refuse, or a negative limit, naming the tenant that is wrong, so a
typo fails the boot instead of leaving a tenant unlimited. A table would need an admin
UI to change safely, would not appear in a code review, and would make "the limit this
sandbox was created with" and "the limit the page shows" two numbers that can drift.
The honest cost is that changing a quota is a restart.

Three enforcement points, each stated where it happens:

| Limit | Enforced by | Where |
|---|---|---|
| CPU / memory / processes | `docker run --cpus/--memory/--pids-limit` on the container | `sandbox.Docker.containerArgs` (per-tenant via `DockerConfig.LimitsFor`; the runner sends the tenant's values in the create request) |
| concurrent sandboxes | a count of the tenant's `sandboxes` rows before a create | `provision.checkSandboxQuota`, typed `tenant_sandbox_limit` (409) with `{limit, current, tenant}` |
| disk | the existing per-sandbox `du` measurement (FR-5.8) | `httpapi.enforceSandboxQuota` (unchanged) |

`--pids-limit` is new and is the fork-bomb guard: without it one agent can fill the
host's PID table and take every tenant with it.

The refusal is typed and it *survives* the request: before this change a provisioning
failure was logged and swallowed ("provision: no worktree for session"), so a tenant at
its cap saw a generic 500 three layers later. Now `POST /v1/projects/{id}/sessions` and
`POST /v1/sessions/{id}/messages` answer 409 `tenant_sandbox_limit` with the numbers,
and the session row created for the refused request is deleted rather than left behind
(`httpapi/quota_refusal_test.go`).

What the limits actually applied are recorded: the provider reads them back out of the
container's own `HostConfig` and the row keeps `cpu_milli`/`mem_mb`, so
`GET /v1/admin/status` reports the sandbox's real limits next to the tenant's policy
(`admin-status-tenant-limits.json`), and the two agreeing is a check the soak makes.

### 12.3 The audit trail: `tool_calls`, read across tenants

`GET /v1/admin/audit` (admin only) returns one page of every tool call: when it
started and ended, how long it took, the tenant (id + email), the project (id + name),
the session, the run, the tool and the outcome (`ok` / `error` / `running`). Filters:
`user_id`, `project_id`, `session_id`, `tool`, `outcome`, `since`, `until`, `limit`,
`offset`. `format=csv` returns the same page as a CSV download, so an operator can
attach it to a ticket.

It is not a forensic log and the page says so: `tool_calls` keeps a hash plus the same
short argument preview the user sees in their own transcript, never the arguments
themselves. What it answers is "who ran what, when, and did it fail" across tenants —
which is the question the acceptance pass could not answer at all.

### 12.4 What this section does not claim

- **Not sandbox-vs-sandbox within a tenant** (§12.1), stated with its cost.
- **Not egress control beyond what §12.5 states.** §12.5 closed the default: on the
  isolated shape a sandbox has no route to the public internet unless its tenant's policy
  says `open`. Still absent: a *filtered* allowlist, an egress policy on the shared
  single-host shape, and any control over what a sandbox does through the control plane's
  gateway. Read §12.5 before claiming anything about a tenant's reach.
- **Not a per-tenant disk budget.** Disk is still per sandbox, measured and refused per
  sandbox; a tenant's total disk is the sum of its sandboxes' budgets, which is a
  consequence, not a policy.
- **Not billing or usage accounting.** The audit trail records attempts and outcomes,
  not tokens or cost; `runs` already carries the token counters and this endpoint does
  not join them.
- **Not a proof at fifty tenants.** Four tenants, concurrently, on one Docker Desktop
  host is the measured number (`scripts/soak-tenancy.ps1`); the address-pool ceiling in
  §12.1 is the first thing a larger deployment will hit.

## 13. Tenant egress: what a sandbox can reach

§12 closed the *inward* hole and named this one: a sandbox on its tenant bridge could
still reach the public internet, so `npm install`, a `curl` to an exfiltration endpoint, a
compromised dependency, or a build script had exactly the reach the host has — and nothing
in the deployment said whether that was intended. This section is what now decides it,
what each mode covers, what it does not, and why one of the three shapes is refused
instead of half-implemented.

### 13.1 The policy: three shapes, a file, and a default that is closed

`internal/tenantegress` holds a policy with a default and per-tenant overrides, mounted
read-only into both services from `deploy/tenant-egress.json`
(`PIFORGE_TENANT_EGRESS_FILE`), validated at startup:

```json
{
  "default": {"mode": "none"},
  "tenants": {"dev@piforge.local": {"mode": "open"}}
}
```

- **`none`** — no egress beyond the containers on the tenant's own bridge: the control
  plane (so `/gw/llm` and `/gw/mcp` keep working) and the runner. The shipped default.
- **`allowlist`** — "these hosts/CIDRs/ports". Modelled and validated (host **or** CIDR,
  ports 1–65535, no wildcards, no scheme), and **refused at startup**. See §13.3.
- **`open`** — the historical behaviour: an ordinary bridge, Docker NATs it, the sandbox
  reaches whatever the host reaches. It is now a value somebody wrote down.

**Why a second file** rather than a section of `deploy/tenant-limits.json`: the two
policies are read by different processes. The quota is read only by the control plane,
which sends the resolved numbers with each create. Egress must be read by the *runner*
too, because the runner is the process that creates the bridge. Both files reject unknown
fields on purpose, so one document would force each process to parse the other's schema,
and a quota typo would be able to fail the runner's boot for no reason. Two files, mounted
side by side, validated separately, reviewed together.

**Where the default comes from**, in order:

| Situation | Effective mode | Where it is said |
|---|---|---|
| a file is mounted | the file's `default` | boot log (`tenant egress policy source=…`), admin page |
| `-tenant-egress-mode none\|open` | that value | boot log, admin page (`flags`) |
| per-tenant bridges, nothing configured | **none** | boot log (`network-mode`), admin page |
| one shared bridge, nothing configured | open | boot log WARN, admin page (`未配置`) |

The third row is deliberate: where the boundary *exists*, it starts closed. The fourth is
the single-host shape, which has no per-tenant anything, so a "none" there would be a
promise the deployment cannot keep — it is reported as open, at boot and on the page,
instead of being accepted and ignored.

### 13.2 Enforcement: `--internal` on the tenant bridge, and nothing else

**Chosen: `docker network create --internal`** for the tenant's bridge when its mode is
closed, and an ordinary bridge when it is open. That is the whole mechanism, and it is a
good one for this platform: an internal bridge has no default route, no NAT, and Docker's
embedded DNS does not forward external names on it. A container on it can reach the other
containers on that bridge and nothing else — which is exactly the set §12.1 put there:

- the **control plane**, so `http://piforge:7700/gw/llm/<token>/v1` and `/gw/mcp/<token>`
  keep answering (the sandbox holds a per-session gateway token, never a provider key);
- the **runner**, which provisions the checkout through the sandbox's own API.

The mode is labelled onto the bridge (`piforge.egress=none|open`) as well as configured, so
`docker network ls --filter label=piforge.egress=none` answers "who is closed" from the
daemon — the side that actually enforces it — without reading a config file.

**Changing a mode is a bridge rebuild, and the code refuses rather than improvises.** A
network's `Internal` flag cannot be changed in place; recreating it under running sandboxes
would cut them off mid-turn and would leave the containers that are already up with the
route they were created with. So a create against an existing bridge whose flag disagrees
with the policy fails and names the command:

```
sandbox: tenant network piforge-t-… was created "open" but this tenant's egress policy is
"none": … Remove it with `docker network rm piforge-t-…` (its sandboxes have to be
recreated) and the next create will build it "none"
```

**The policy travels with the create** (`sandbox.RunnerRequest.egress_mode`), the same way
the quota does: the control plane owns the file, the runner owns the Docker CLI and parses
and refuses the mode per request. A request that carries no mode gets the runner's own
answer, which is closed when it runs with per-tenant bridges.

### 13.3 `allowlist`: modelled, validated, and refused at boot

Docker cannot filter a bridge by hostname. There is no `--allow-host`; the bridge is
either routed or not. The two honest ways to get a per-host allowlist on this platform
are:

1. **a filtering proxy on the tenant's internal bridge** — with `--internal` the proxy is
   the *only* egress path, so it can enforce a host:port allowlist (CONNECT + absolute-URI)
   and everything that does not use `HTTP(S)_PROXY` fails closed. It costs a long-running
   process per tenant bridge, a source-address→tenant map, and the proxy configuration
   injected into every sandbox;
2. **host iptables rules** (`DOCKER-USER`) installed by a process with host privileges —
   the runner would need `--net=host`/`NET_ADMIN` on the host, which is a larger trust
   change than the socket mount §1 of SANDBOX-DEPLOY.md already argues about.

Neither is in this release, so `allowlist` is **refused at startup**, naming the tenant and
the reason:

```
tenant egress: tenant "bob@piforge.local": egress mode "allowlist" is not implemented and is
refused rather than accepted: Docker filters a bridge, not a hostname, so a per-host
allowlist needs either a filtering proxy on the tenant's internal bridge (the only egress
path) or iptables rules installed on the host by a privileged process, and this deployment
does neither. Use "none" (closed: the gateway stays reachable) or "open" (explicit, the
historical behaviour)
```

The refusal is the point. A policy that accepts `allowlist` and then lets the sandbox out
anyway is the worst outcome this feature exists to prevent: the operator reads "allowlist",
the agent gets the internet.

### 13.4 What each mode covers, and what it does not

Covered by `none`:

- TCP/UDP to any address outside the tenant bridge — no route, so a connection attempt
  times out or reports "network unreachable" rather than being quietly refused;
- external DNS — the embedded DNS on an internal network resolves container names on that
  network and forwards nothing, so `example.com` does not even resolve (this is what an
  agent sees first: `curl: (6) Could not resolve host`);
- the browser inside the sandbox — same network namespace, same rules;
- the agent, `npm`, `pip`, `git`, `curl` — anything the sandbox process runs.

Not covered by `none`, stated because a reader will ask:

- **containers on the same tenant bridge.** The control plane and the runner are reachable
  by design; so is another sandbox of the *same* tenant (§12.1). Egress policy is about the
  outside world, not about intra-bridge reach.
- **the control plane's own egress.** The sandbox reaches the gateway, and the gateway
  makes the model call with the deployment's provider key. A sandbox that can prompt a
  model can therefore cause *content* to leave; that is the product, not a hole in this
  policy.
- **host paths that are not the bridge.** `--internal` says nothing about the Docker
  socket (no sandbox mounts it), about a `--network host` container (this deployment never
  creates one), or about the host's own processes.
- **disk, CPU, PIDs** — §12.2, a different boundary.
- **the shared single-host shape** (`-sandbox-network-mode shared`). One bridge for every
  sandbox cannot hold a per-tenant answer; a policy that asks for one is a boot error, and
  an unconfigured deployment reports open.
- **`-sandbox local`.** The agent runs on the host and shares its network namespace; a
  policy that asks for `none` there is refused at boot rather than reported as applied.
- **`-sandbox docker` with per-tenant bridges.** There the control plane is a host
  process, so `-sandbox-join-containers` has nothing to join: the sandbox reaches it only
  at an address the bridge routes to (`-gateway-url`), and nothing about this policy adds
  that. The isolated deployment ships the runner shape, where both services are
  containers and the join is real.

### 13.5 Visibility: the operator's screen answers "who can reach the internet"

`GET /v1/admin/status` → `tenant_limits` now carries `egress_mode`, `egress_source`,
`egress_configured`, `egress_applied`, and each tenant row carries its effective `egress`
plus `egress_override`. `/admin/status` renders it in the tenant card (沙箱出口), with the
per-tenant badge, so the answer does not require reading the mounted file on the host.

`egress_applied: false` is not a failure: it is the honest flag for a placement with no
container (`-sandbox local`), where the mode is reported but is not a boundary.

### 13.6 Evidence

`pwsh scripts/probe-egress.ps1` (throwaway stack on 7723, its own project and volume,
torn down at the end) starts the isolated stack with a policy that leaves the default
closed and opens one account, then measures, from inside each sandbox, DNS resolution, a
public HTTPS fetch by name and by literal address, the control plane's `/healthz` and
`/v1/*`, the sandbox's own gateway URL, the bridge's `Internal` flag and label, the admin
report, and a real prompt in the *closed* tenant. Last run: **0 failures, exit 0**,
artifacts in `e2e/.artifacts/egress/`:

```
closed: network=piforge-t-29ec2c7a2fa8 Internal=true  label piforge.egress=none
opened: network=piforge-t-6a0e6d79cf7c Internal=false label piforge.egress=open

closed: getent hosts example.com -> DNS-FAILED
        curl -m 5 http://example.com -> curl: (6) Could not resolve host: example.com   (000)
        curl -m 5 https://1.1.1.1    -> curl: (7) Couldn't connect to server             (000)
opened: getent hosts example.com -> 172.66.147.243 / 104.20.23.154
        curl -m 5 http://example.com -> 200
        curl -m 5 https://1.1.1.1    -> 301

both tenants:   http://piforge:7700/healthz -> 200 ; /v1/projects -> 401
admin status:   egress_mode=none source=/etc/piforge/tenant-egress.json
                configured=true applied=true ; admin=none, dev=open (override)
closed tenant:  a real prompt settled, the assistant answered DONE, and
                http://piforge:7700/gw/llm/<token>/v1/chat/completions -> 200
pf-local (7722): same container id 7dc14e4a7525…, same StartedAt, healthz 200 before/after
```

The public target is plain HTTP plus a literal IP because this host's TLS to some public
sites is intercepted (`curl: (35) unexpected eof`) — a property of the network, not of
the policy, and pinning `https://example.com` would have made the probe measure the
firewall instead of the bridge. The closed tenant's `https://1.1.1.1` failure is a
*transport* failure (`curl: (7)`), and its DNS failure is the one an agent sees first.



## 14. What the adversarial acceptance changed (2026-11-01)

An adversarial run (`e2e/.artifacts/adversarial/VERDICT.md`) attacked a fresh isolated
stack as a tenant and found four real defects. The tenancy boundary itself held — no
cross-tenant read, no egress, no route into the control plane's data — and every one of
the four was *around* that boundary. This section records the mechanisms, what was
measured, and what is still not claimed.

### 14.1 The tenant owned root inside its sandbox

`sudo -n id` answered `uid=0(root)`, `sudo -n -l` answered `(ALL) NOPASSWD: ALL`, and
`CapEff` was `00000000a80425fb` — Docker's default set. The image ships a setuid-root
`/usr/bin/sudo`, and its runtime bootstrap (`gem.sh`, running as root) appends
`gem ALL=(ALL) NOPASSWD:ALL` to `/etc/sudoers.d/gem` on every start. From that root a
tenant could rewrite `/opt/piforge/piforged` (the launcher the control plane starts) and
the "frozen" skill tree, and it was one container escape away from the host.

**Mechanism, in three independent layers:**

1. `--security-opt no-new-privileges` on the sandbox container. This is the layer that
   ends the leak even against the *stock* image: with `no_new_privs` set, execve() of a
   setuid binary does not change the effective uid, so sudo runs as `gem` and cannot
   `setuid(0)`. Measured: `sudo -n id` → `sudo: no new privileges flag is set` /
   permission failure, `id` → `uid=1000(gem)`.
2. `--cap-drop ALL` plus only the capabilities the image measurably needs. Each of the
   five was added back because the image's own boot log named the operation that failed
   without it (see `sandboxCapAdd` in `backend/internal/sandbox/docker.go`):
   `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETUID`, `SETGID`. Everything Docker grants by
   default and this list omits is gone — `NET_RAW`, `SYS_CHROOT`, `MKNOD`, `SETFCAP`,
   `KILL`, `AUDIT_WRITE`, `NET_BIND_SERVICE` — and the agent account holds none of the
   five (`CapEff 0000000000000000`). Measured `CapEff` for the container: `00000000000000cb`.
3. `deploy/Dockerfile.sandbox-agent` deletes `/usr/bin/sudo`, `/usr/sbin/sudo` and
   `/etc/sudoers.d/gem`, makes `/opt/piforge` and `/opt/piforge/piforged` root-owned
   `0555`, and fails the build if any of that is missing. The mode is real because the
   container has no capability that bypasses it: a root process in this container is
   subject to file modes like any other account. Writing `/opt/piforge/piforged` fails
   as `gem` (EACCES) and would fail for a root that appeared some other way.

**What it cost:** five capabilities remain (listed above, each with the operation that
needs it). Nothing in the product path needed root for itself: `pi`, git, node, the
terminal, code-server and Jupyter all run as `gem`, and the only root processes are the
image's own bootstrap and nginx master.

**Still open:** the skill tree inside the workspace is owned by the tenant account (the
sandbox file API writes as `gem`), so its 0444/0555 bits are a guard against accident
and against the old sudo path, not against a tenant that runs `chmod u+w` first. Closing
that needs a root-owned writer inside the sandbox, which this image does not offer.

### 14.2 Disk was uncapped while the UI showed a quota

`fallocate -l 3G /workspace/ballast.bin` succeeded (`used_delta_bytes=3221229568`) on a
1007 GB volume with 784 GB free, while the admin row said `quota_bytes=2 GiB, over=false`.
The run-boundary 507 check only ran between turns, and — worse — it could not see the
sandbox at all: with a detached workspace the row's `root_dir` is `/workspace`, a path
that exists only inside the container, so the host walk measured 0 bytes.

**Mechanism:**

1. `--ulimit fsize=<budget>:<budget>` (`sandbox.Limits.DiskBytes`, carried from
   `-sandbox-disk-quota` through the runner's create request). Every process in the
   container inherits `RLIMIT_FSIZE`; soft = hard so a shell cannot raise it. Measured:
   `fallocate -l 3G` → `File size limit exceeded` (SIGXFSZ), 0-byte file.
2. A periodic in-sandbox check (`httpapi.StartDiskWatch`, 5 s) that measures
   `du -sb /workspace` through the sandbox's own shell API. Over budget it stops the
   agents, appends a `platform.error` with the run boundary's 507 message, and pauses
   the sandbox if usage keeps growing after the agents are gone.

   Stopping an agent there is not just `Runtime.Stop`: a container placement's agent is
   owned by a detached supervisor inside the sandbox that deliberately survives a closed
   control-plane stream, so the watchdog also sends SIGTERM to that supervisor through
   the shell API (matched on `agent-supervisor … -session <id>`). Without it the agent
   kept running — the runtime stop only detached the reader. Measured: a live run with
   `pi` inside the sandbox, then 2 GiB + 200 MiB written from the terminal; the
   supervisor was gone 2 s after the writes, `pi` processes 0, the run's status flipped
   to not-running, and the session carried `platform.error(code=sandbox_disk_quota)`.

3. One measurement feeds three readers — the admin report, the run-boundary refusal and
   the watchdog — so `over` cannot disagree with itself. A measurement that fails is
   reported as `unmeasurable: true` (page: `未知`), never as `0 B`.

4. A paused sandbox is woken by the next message (`wakeSandbox`), and the agent's
   dial waits up to 25 s for the sandbox API, because a resume is a container start whose
   services are not listening yet. That race was invisible while pause returned 422
   (§14.4); making pause work made it reachable, and the first prompt after a resume
   died with `connection refused` until the wait was added.

**What did not work on this daemon, measured:** `--storage-opt size=1G` is accepted and
ignored (overlayfs on Docker Desktop): 1.5 GiB written into the 1 GiB rootfs, `df` still
1007 GB. It would not cover the workspace volume in any case. ext4 project quotas need a
`prjquota` mount; a size-limited tmpfs is not persistent; a loopback filesystem needs
`CAP_SYS_ADMIN`, which §14.1 removed.

**Still open:** many files just under the per-file cap can overshoot by a few GiB before
the 5 s check acts (worst case ≈ one writer's throughput × 5 s ≈ a few GiB here). A
per-write quota needs a filesystem that supports it on this volume.

### 14.3 The gateway token was a portable 12-hour credential

`/workspace/.piforge/agents/<session>/models.json` sat in a `drwxrwxrwx` directory and
held a gateway token that answered `200` from the host, outside the sandbox, for twelve
hours. It could not reach `/v1` or MCP, but it spent the operator's provider credit.

**Mechanism:**

1. TTL is one hour (`llmgw.DefaultTTL`; `-gateway-token-ttl` /
   `PIFORGE_GATEWAY_TOKEN_TTL` to raise it deliberately). It is minted at every agent
   spawn, so an hour is a ceiling on a leak, not a session length.
2. Revocation on session end: `Signer.Revoke(sessionID)` is called from
   `pi.Runtime.Stop`, the single choke point every stop path goes through — manual pause,
   idle pause, archive, delete, project delete, model switch, idle reaper and shutdown.
   `Verify` refuses a revoked session's token immediately, so "pause the sandbox" also
   stops the spend.
3. `0700` on `.piforge/agents/<session>` and `0600` on `models.json`/`mcp.json`, set in
   the sandbox as the owning account right after the file API writes them (it writes
   0644). Measured: `ls -ld` shows `drwx------`, `ls -l` shows `-rw-------`, and another
   account in the container cannot read the token.

**Still open, and now closed by §16.1:** the revocation state was in memory. A control-plane
crash between the agent stop and the revoke, or a restart, forgot it; the one-hour TTL was
the backstop for that window. §16.1 persists the counter and proves it survives
`docker restart piforge`.

**A defect in the first version of this fix, found by its own re-check:** Revoke was keyed
by session id alone, so after a pause every token for that session was rejected —
including the one minted for the next spawn. Measured as "a new spawn minted a different
token: True; host GET with it -> 401": a sandbox that had ever been paused could not use
the gateway again until the TTL ran out, which also meant pause → resume → prompt was
broken. Revocation is now a per-session generation counter: Revoke increments it, Issue
stamps it into the claims (`g`), and Verify requires equality. Tokens from before the stop
die exactly, the resume's token works immediately, and no clock granularity is involved
(a pause and its resume can happen inside one second).

### 14.4 Pause and resume could not find the sandbox

`POST /v1/sessions/{id}/sandbox/pause` answered `422 sandbox: not found:
piforge-sbx_<hash>` while the sandbox was READY. The handler passed
`sandbox.ID(user, project)` = `sbx_<hash>`, the container is `piforge-<hash>`, and the
provider's `resolve()` only understood the `piforge-` prefix, so it looked for
`piforge-sbx_<hash>`. Resume failed identically, which also meant an idle pause could
never park a sandbox — the mechanism that bounds the token spend in §14.3.

**Mechanism:** one normalization on the boundary, `sandbox.NormalizeSandboxID`: it
accepts `piforge-<hash>` (unchanged), `sbx_<hash>` and a bare `<hash>`, and answers the
container name. Every lifecycle method resolves through it, so the in-process Docker
provider *and* the runner's HTTP API accept either form. The handlers now pass the id the
sandbox row carries (the provider's own id) instead of re-deriving the pair id.

**Regression tests:** `TestNormalizeSandboxID` (both forms, the whitespace case, the
"not found is honest" case), `TestDockerPauseAndResumeAcceptBothIDForms` (real container,
pause by `sbx_<hash>`, read back by container name, resume both ways) and
`TestSandboxPauseAndResumeUseTheRowID` (the HTTP boundary: the provider must receive the
row's id, and the test fails on the old code with `pause reached the provider with
[sbx_…], want [piforge-…]`).


## 15. The second adversarial acceptance (2026-11-01): a fork storm, an empty transcript, two honest numbers

A second adversarial run (`e2e/.artifacts/adversarial2/VERDICT.md`) attacked a fresh
isolated stack as a tenant. Cross-tenant isolation held again, and the four fixes of §14
held under re-attack (no root, a per-file kernel cap, a one-hour revoked token, working
pause/resume). What it found instead was the boundary's *edges*: one way to take a
project offline with no in-product recovery, one display/API gap after resume, and two
numbers that were not what the product said they were.

### 15.1 A fork storm wedges the sandbox, and the product had no way back

The measured behaviour, reproduced here: a process storm inside a sandbox saturates its
`pids` cgroup. At that point the container cannot spawn **anything** — `docker exec`
fails with `unable to spawn stage-1: Resource temporarily unavailable` (exit 128, ~50 ms,
intermittently, because a slot frees as the storm thrashes), the platform's own `du`
times out (`unmeasurable: true`), and the agent cannot start. The container still reports
`running`. The only recovery was an operator pressing pause and then resume.

`scripts/measure-pids.ps1` was written to answer the question the fix depends on, on this
host, with the shipped image (`piforge/sandbox-agent:isolated`, `--cpus 1 --memory 2g`):

```
boot floor (no storm)                     idle footprint
  pids=64   one node service missing       83-85 pids (threads), 25 processes
            (pids 45, 22 procs)            supervisord 1, jupyter-lab 1, Xvnc 1,
  pids=96/128/192/256/512/1024  complete    nginx 2, node 4, python-server 1

bounded storm (timeout 8 bash -c ':(){ :|:& };:')
  pids=64/96/128/192/256/512/1024   the cgroup is pinned to its maximum in ~1 s;
                                   `cat /sys/fs/cgroup/pids.current` itself cannot fork
                                   while it lasts; `docker exec true` fails at *every*
                                   limit, intermittently, in ~50 ms
  recovery: docker stop -t5 4.99 s + docker start -> exec ok in 0.91 s = 5.9 s

sane tenant at pids=256
  40 concurrent processes + 8 node worker threads: 85 -> 125 pids, exec and du fine
```

Two conclusions, and the second is the one that shaped the fix:

1. The report's "512 is largely consumed by the AIO image" was not true here: the runtime
   idles at ~83 threads. `deploy/tenant-limits.json` still went from 512 to **256**,
   which leaves ~170 slots above the idle footprint and halves what a storm can park in
   one cgroup on a shared daemon.
2. **No ceiling prevents the wedge.** A storm fills whatever it is given, at 64 and at
   1024 alike; the failure is saturation, not the number. A `ulimit nproc` reserve was
   measured too and rejected: at `nproc=192` the image's own bootstrap dies
   (`su: failed to execute /bin/bash: Resource temporarily unavailable`), and a value
   high enough to boot (`>~384`) reserves too few pids to be a real reserve.

So the guarantee is detection and repair (`backend/internal/httpapi/sandbox_watch.go`):

* a liveness probe runs `true` **through the sandbox's own API** with a 3 s bound, every
  10 s, for every READY/BUSY/IDLE sandbox — the same door the agent uses, as the same
  user, so "the API cannot run a command" is exactly the condition the user experiences;
* two consecutive failures are a wedge (one is a loaded host), which is ≤ ~25 s;
* the repair is the pause+resume the operator used to do by hand: `docker stop` +
  `docker start`, ~5 s of container work measured (`stop -t5` 4.99 s + `start` to a
  working command 0.91 s), and the workspace is a volume, so the checkout, the
  transcript and the agent's files survive while every process of the storm dies with
  the container. End to end on the live stack the repair took **7.4 s** and **12.0 s**
  in two runs (`elapsed_ms` on the `sandbox_recovered` event; the second includes the
  image's own services coming back), i.e. 14.1 s from the storm to a sandbox that runs
  commands again;
* the repair is verified by probing again (up to 45 s), and the row's state is written
  only after that — "we restarted it" is not "it works";
* the session is told in its own transcript, with `platform.error`:
  `sandbox_unresponsive` 「沙箱无响应，正在恢复」 before anything is touched, then
  `sandbox_recovered` 「沙箱已恢复（用时 7.4 秒）…」 or `sandbox_recovery_failed` with what
  to do next. Live, the probe's own failure is the tenant's: `500: Internal server
  error: [Errno 11] Resource temporarily unavailable` from `/v1/shell/sessions/create`,
  and the recovery's WARN lines name the same sandbox, strike and elapsed time;
* the same repair is one HTTP call (`POST /v1/sessions/{id}/sandbox/recover`) and one
  button (「恢复」 in the session view), and `GET /v1/sessions/{id}/sandbox` reports the
  cached verdict as `responsive` (`null` = not probed yet, never "down") plus
  `recovering` and `can_recover`.

**What this does not claim.** It is not a resource policy: a determined tenant can wedge
its own sandbox again a second later, and the watchdog will repair it again (one WARN per
episode). A tenant that does it in a loop is a tenancy problem (`max_sandboxes`, project
deletion), not something a watchdog decides. And a sandbox whose *container* is gone is
not recovered here — that is `reconcile`'s job.

### 15.2 After pause → resume, the transcript endpoint answered empty

Measured: after `pause` then `resume` and **no new prompt**,
`GET /v1/sessions/{id}/entries` returned `{"entries":[],"leaf_id":""}` and
`/status` said `last_seq=0`, while `/stats` still counted 2 user + 2 assistant messages
and the sandbox's `/workspace/.piforge/sessions/<id>.jsonl` held 8 093 bytes of both
turns. Nothing was lost; the endpoint was answering from a process that resume does not
start.

**Mechanism:** `pi.Runtime.Entries` now falls back to the session file when there is no
live agent (`RuntimeConfig.TranscriptFor`, `internal/pi/transcript.go`). The deployment
supplies that hook by reading the file through the sandbox's own API
(`cmd/piforged/agent_sandbox.go: Transcript`), which is where the conversation actually
lives for a container placement. `?since=<entry>` is honoured against the file; a cursor
the file no longer contains returns the whole transcript (showing it twice is
recoverable, showing it empty is not). An unreadable file is still reported as the
process-less answer, never as a successful empty conversation.

**Tests:** `TestEntriesReadTheTranscriptWhenNoAgentIsRunning`,
`TestEntriesFromTranscriptHonourSince`,
`TestEntriesFromTranscriptToleratesAColdOrTornFile`,
`TestEntriesWithoutATranscriptHookStillReportNoProcess` (pi), and
`TestEntriesSurvivePauseAndResumeWithoutAPrompt` (httpapi, real `pi.Runtime` + a real
session file: pause → resume → GET entries returns all four entries and `leaf_id=a2`
without sending a prompt).

### 15.3 Two numbers that were not what the product said

**(a) The file-size refusal is the shell's, and the tenant was never told.** A write past
the per-file limit is killed by the kernel with `SIGXFSZ`; the tenant sees the shell's
own line — `File size limit exceeded (core dumped)`, exit 153, a 0-byte file — and
nothing in the product explains it. A kernel signal cannot be rewritten, so the fix is
the documentation: `docs/SANDBOX-DEPLOY.md` §10 quotes that exact text, with the limit,
the exit code, and the difference between it and the 507 total-quota refusal.

**(b) `files` in the admin `sandbox_usage[]` was always 0.** The in-sandbox measurement
ran `du -sb` and deliberately skipped the count ("a second `find` would double the walk").
The second walk was never needed — only the second column: one GNU `find -printf '%y %s\n'`
pass feeds an `awk` that sums every entry's size **and** counts the non-directories.
Measured on a 400-file tree: `412288 400` — bytes identical to `du -sb` (412288), files
400 (was 0).

## 16. The third pass (2026-11-01): the two risks the second acceptance left open

`e2e/.artifacts/adversarial2/VERDICT.md` closed its three "operator would worry" items
with three that were still true: the durable-revocation gap in §14.3, the ~2x disk
overshoot in §14.2/§15, and the one-hour portable token itself. This section fixes the
first two and measures the third, on the same isolated stack
(`pwsh scripts/up-isolated.ps1`, 7723, `piforge:isolated` / `piforge/sandbox-agent:isolated`).

### 16.1 A revoked token now stays revoked across a restart

**Defect.** `Signer.Revoke` bumped a `map[string]int64` in the control plane's memory.
Pause revoked correctly *in that process*; a restart inside the one-hour TTL started the
counter at zero again, so every token minted before it verified again. The old comment
even said so ("the state is in memory on purpose: it describes live processes").

**Mechanism.**

* `gateway_revocations(session_id PRIMARY KEY, generation, revoked_at, expires_at)`
  (`store/gateway_revocations.go`). `RevokeGatewayToken` reads and bumps the generation in
  one transaction — the write pool is a single connection, so two concurrent stops cannot
  both write generation 1 — and sets `expires_at = now + TTL`, the moment the last token
  that revocation could affect expires.
* The write happens in the *same call* that ends the token (`Signer.Revoke`, from
  `pi.Runtime.Stop`), not on a queue: a pause that answered `200 PAUSED` means the row is
  on disk. If the write fails, the revocation is still enforced in memory and the failure
  is logged with what it means.
* `Signer.Verify` reads the durable generation on every `/gw/llm` call (one indexed
  primary-key read, after the HMAC check, so an unauthenticated caller cannot make the
  gateway touch the database) and takes the maximum of it and the in-memory fast path. A
  failed read falls back to memory: never weaker than the old behaviour.
* `Signer.Issue` reads the same number, so the token minted for the resumed session
  carries the persisted generation and verifies immediately. Issue also extends the row's
  `expires_at` (`ExtendGatewayRevocation`) so the sweep can never delete the generation a
  live resumed token still relies on.
* `SweepGatewayRevocations` deletes rows past `expires_at`; `cmd/piforged` runs it every
  ten minutes. The row has **no** foreign key to `sessions` on purpose: a revoked session
  that is later deleted must not take its tombstone with it, or its pre-revoke tokens would
  become valid again.
* **Shutdown is the one stop that does not persist.** A container placement's agent
  outlives the control plane by design (§6), and it cannot mint a new token without a new
  prompt, so revoking at shutdown would break exactly the property stage 3 built. The hook
  is `revocationHook` in `cmd/piforged/gateway_revocations.go`; `drain()` is registered
  before `rt.Close()` so it runs first. Every stop a user or the platform causes — pause,
  archive, delete, model switch, idle reap — still writes the row.

**Proof (isolated stack, `scripts/probe-gateway-revocation.ps1`, artifacts under
`e2e/.artifacts/gateway-revocation/`):**

```
token A: len=204 sha256=0e8984… session=ses_… provider=volc g=0 ttl_s=3600 baseUrl=http://piforge:7700/gw/llm/<token>/v1
PASS  token A works from the host before the pause        (GET /gw/llm/<A>/v1/models -> 200)
PASS  pause                                               (state=PAUSED)
PASS  token A is refused after the pause                  (GET -> 401)
boot log before the restart: … "model gateway revocations are durable" … on_record=0
restarting piforge ...
boot log after the restart:  … "model gateway revocations are durable" … on_record=1
PASS  the restarted process loaded a revocation from disk (on_record=1)
PASS  token A is STILL refused after the restart          (GET -> 401)
PASS  resume                                              (state=READY)
PASS  token B is a different token, one generation later  (g: 0 -> 1)
PASS  token B works from the host (the resumed generation is valid) (GET -> 200)
PASS  token A is still refused while token B works        (GET -> 401)
RESULT durable_revocation=401 old_token_after_restart=401 resumed_token=200 generations=0->1 on_record=1
```

**Tests:** `TestRevocationSurvivesAControlPlaneRestart` (llmgw + a real store: revoke,
close the store, reopen it, a *new* signer refuses the old token and its in-memory set is
empty), `TestResumedGenerationIsValidAfterARestart`, `TestSweepKeepsAResumedTokenAlive`,
`TestSignerWithoutAStoreStillRevokes`, the store-level
`TestGatewayRevocationSurvivesReopen` / `…SweepRemovesOnlyExpiredRows` /
`…ExtendKeepsResumedTokenAlive` / `…HaveNoCascadingForeignKey` / `…OutlivesTheSession`,
and `TestRevocationHookSkipsOnlyTheShutdownStop` for the shutdown exception.

### 16.2 The disk overshoot: measured at 3.27x, then bounded to 1.05x

**Defect, measured on this host instead of assumed.** `scripts/probe-quota-overshoot.ps1`
creates a tenant, a project and a session, then bursts inside the sandbox as `gem` (the
identity the PTY and the agent get) while sampling `du -sbx /workspace` from the host every
200 ms. On the build this section starts from (`piforge:before`, HEAD `0592c27`):

```
sample    194 ms      58845315 bytes  (0.05 GiB)
sample   4118 ms    2122442883 bytes  (1.98 GiB)   <- the budget, reached here
sample   8104 ms    4605479043 bytes  (4.29 GiB)
sample  10085 ms    6874605699 bytes  (6.40 GiB)   <- the probe's own 6 GiB safety cap
RESULT mode=files quota_bytes=2147483648 max_bytes=7029794947 ratio=3.27 paused=false enforcer=absent elapsed_ms=10086 state=READY
```

7.03 GB into a 2 GiB budget in ten seconds, the writer still running and the sandbox still
`READY`; the control plane had logged one warning at 4.90 GB. The second acceptance run
measured 3.95 GB and 5.41 GB on the same mechanism; this run is the same defect with a
faster writer, so the honest statement is "~2x–3.3x of the budget, depending on the write
rate".

**Options, evaluated on this daemon** (all four measured, not argued):

| Option | Measured on this daemon | Verdict |
|---|---|---|
| (a) shorter, adaptive watchdog interval | 5 s fixed → 2 s while a run is in flight, 10 s idle; the interval is per sandbox, ticked at 500 ms | **taken**: it narrows the window, but by itself it is still `interval x write rate` |
| (b) size-capped tmpfs workspace | `--tmpfs /workspace:size=2g`: the second write dies at exactly 2147483648 B (`No space left on device`) — a real hard cap. But `docker restart` leaves `total 0`: the whole workspace is gone | **rejected**: pause/resume, the file panel and the agent's work all require a persistent workspace; the tmpfs also competes with `--memory 2g` |
| (c) `--storage-opt size` | accepted (`HostConfig.StorageOpt = {"size":"1g"}`) and silently ignored: `df /` still reports 1006.9G, and it would not cover the workspace volume anyway | **rejected**: measured no-op |
| (d) in-sandbox enforcer | `piforged quota-enforcer`: `du -sbx` every 100 ms, SIGSTOP the writers and the tree they run in, SIGKILL only when new writers keep appearing; marker at `/workspace/.piforge/quota-brake.json`, log at `/tmp/quota-enforcer.log` | **taken**: it is the only one of the four that bounds the burst in-band |

**(a)+(d) together, and what they cost.** The enforcer is a mode of the binary that is
already in the sandbox image, started by the base image's own bootstrap
(`RUN_HOOK_PRE_SERVICES=/opt/piforge/quota-enforcer.sh`, baked into
`deploy/Dockerfile.sandbox-agent`). It reads the budget from `/proc/self/limits`
(`Max file size`), not from a second configuration source: that is the same
`--ulimit fsize` the per-file cap uses. Two measured costs, stated as costs:

* **The peak is now one in-flight write, not a window.** After the change, the same probe
  on the same stack: **2 265 053 457 B = 1.05x** (files), **2 156 001 554 B = 1.00x**
  (one 6 GiB file capped by RLIMIT, then the flood), **2 171 730 202 B = 1.01x** (mixed). A\n  repeat of the first shape against the committed image measured 2 172 778 769 B = 1.01x, so the\n  range for the file-flood case is 1.01x-1.05x across runs.
  The writers were found in `T` state with the brake marker naming them, and the sandbox
  stayed `READY` — the platform watchdog did not have to pause it.
* **It is still not a filesystem quota.** The bound is `du` interval + reaction (~0.2 s of
  writes at the measured ~1 GB/s burst rate) plus whatever a single `write()` is holding.
  A writer that outruns `/proc` scanning, or a tenant that disables the loop (same uid, so
  it can `kill` it) gets the platform watchdog instead. The next container start runs the
  hook again because the script and the binary are root-owned `0555` in the image.
* **CPU**: `du -sbx` on a 2 GiB / 256-file tree measured < 1 ms, so 10 walks/s is
  micro-seconds of a core; `NextInterval` backs off to 3x the walk time when the tree is
  large (cap 5 s), so a 200k-file checkout is not walked ten times a second.
* **Frozen, not killed.** A bolted writer is stopped with `SIGSTOP`, which is reversible;
  the escalation to `SIGKILL` needed a *mebibyte* of growth after the freeze (the
  `growthFloor`), i.e. new writers, not `du` noise.
* **What the tenant sees**: its shell freezes (a stopped process), the platform's watchdog
  records the same 507 message a refused run carries, and `docs/SANDBOX-DEPLOY.md` §10.5
  quotes it. There is no in-product "unfreeze": the pause/resume cycle is the recovery,
  exactly as for an over-quota tree.

**Tests:** `TestDecide` (the kill decision as a table: under, first over, over-and-frozen,
still-growing past the threshold, explicit threshold, no limit, zero count),
`TestNextInterval` (base, slow-walk back-off, floor, cap), `TestDefaultsAreTheDocumentedOnes`,
`TestWatchIntervalIsFastWhileActiveAndSlowWhenIdle` (2 s / 10 s),
`TestDiskWatchScheduling` (due/schedule/reset, so the interval is not just a comment),
`TestPlatformWatchIsSlowerThanTheEnforcer`, and
`TestRunningSandboxIDsFollowsTheStatusAndTheWorktree` for the "is a run in flight" query
that picks the interval.

### 16.3 What is still open

The token is still a **portable bearer credential for up to an hour**: it answers
`/gw/llm` from the host, with no binding to the sandbox, container or address, and it is
readable by any process in the sandbox that can read `models.json` (owned by `gem`, `0600`
— i.e. the agent and anything it runs). Revocation now survives a restart, which bounds
the *life* of a leaked token; it does not bound the *holder*. Binding the token to the
sandbox (a per-container secret in a header the gateway checks, or a mTLS identity) is the
next step, and it is a bigger change than this pass: the SDK in the sandbox would have to
send the proof, and the gateway would need a per-sandbox secret store that survives
container replacement.