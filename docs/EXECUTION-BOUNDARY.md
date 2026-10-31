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
- **Not egress control.** A sandbox can still reach the internet if the host allows it;
  what changed is that it can no longer reach another tenant. Per-tenant egress policy
  (an allowlist, a proxy) is a separate piece of work and is not here.
- **Not a per-tenant disk budget.** Disk is still per sandbox, measured and refused per
  sandbox; a tenant's total disk is the sum of its sandboxes' budgets, which is a
  consequence, not a policy.
- **Not billing or usage accounting.** The audit trail records attempts and outcomes,
  not tokens or cost; `runs` already carries the token counters and this endpoint does
  not join them.
- **Not a proof at fifty tenants.** Four tenants, concurrently, on one Docker Desktop
  host is the measured number (`scripts/soak-tenancy.ps1`); the address-pool ceiling in
  §12.1 is the first thing a larger deployment will hit.
