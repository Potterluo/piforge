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
providers. It is written before the code, and it is deliberately staged: stages 1 and 2
are implemented, stages 3–4 are not, and each stage names the test that accepts it.

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
| Newline/JSON frames over a TCP socket the sandbox dials back | control plane listens, a shim inside the sandbox connects out | Kept for stage 3. It is the only shape that survives a control-plane restart (the socket reconnects), but it needs a shim *inside* the sandbox plus a reconnect protocol. |
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

**MCP is different and is deliberately not in stage 1.** `pimodel.WriteMCP` decrypts MCP
credentials and writes them as environment variables into `mcp.json`. That file is a
provider-equivalent secret; writing it into a tenant's sandbox is exactly the thing this
document is trying to stop. Until there is a credential broker (stage 3: the sandbox
holds a placeholder, the gateway injects the value on egress), a session with MCP
servers runs without them under the flag, and says so in the log.

## 5. Liveness and reconciliation with a remote agent

The marker (`internal/liveness`) already exists and already points the right way: a
writer inside the workspace, a reader through the sandbox API. Stage 1 changes who
writes it — the supervisor now runs in the sandbox — and nothing else:

- the marker path and beat/TTL are unchanged (`/workspace/.piforge/live/<sid>`, 15 s,
  2 min);
- `sessionLivenessProbe` is unchanged, so a restarted control plane can still answer
  "alive" from a sandbox it did not start;
- what changes is that the answer becomes **true**. Today `kept_live=1` after a restart
  means "the marker is 40 s old but the process it described died with the control
  plane". With the agent in the sandbox the process really can outlive the restart —
  provided the RPC channel outlives it too, which stage 1's direct pipe does **not**
  (§6, and the measured restart test in `docs/SANDBOX-DEPLOY.md` §1.4.3).
- stage 2 shifted the marker's *home*: it stayed at `/workspace/.piforge/live/<sid>`,
  but the workspace is now a volume only the sandbox mounts, so the writer and the reader
  are the only two parties that touch it. The marker still carries a pid and not a boot
  id — §10.1 item 3.

## 6. What still breaks, and the honest answer

Stage 1's channel is a TCP connection owned by the control plane. When the control
plane is restarted:

- the connection closes → `docker exec` sees EOF on stdin → `pi` exits → the launcher
  removes the marker → the next reconciliation answers "dead" and marks the run
  interrupted;

so **the agent does not survive a control-plane restart in stages 1 and 2**, and the
restart test asserts exactly that (plus: it dies for a stated reason, not silently).
Survival is stage 3: the sandbox-side supervisor owns a Unix socket in
`/workspace/.piforge/run/`, survives the control plane, and the control plane
reattaches to it by writing a `connect` frame. Buffering a disconnected run is
explicitly *not* promised — PRD §7.4.5 forbids replaying a run whose control plane died.

The other stage-1 gap is still open in stage 2 (it is `§10.1` item 2): the agent's
stderr goes to the runner's log, not to the session's Logs panel
(`Bridge.StderrTail` is fed only by a locally spawned child). The plan is unchanged —
write `/workspace/.piforge/logs/<session>.log` and tail it through the file API.

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
wraps the local spawn, and every existing test keeps running that path. This is also why
the flag is off by default: the default deployment must not change behavior until the
docker/runner path has been proven on a throwaway instance.

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
| 3 | Detached supervisor: agent survives a control-plane restart, control plane reattaches; MCP credential broker; `pods/exec`-shaped provider for K8s | flag **on by default** for docker/runner | Restart the control plane mid-run: the same `pi` pid answers `get_state` after reattach; the run settles instead of interrupted; a run whose control plane is gone for longer than TTL is honestly interrupted (no replay) |
| 4 | Remove the flag; delete the in-process host spawn for container providers; `sandbox.Docker.Attach` becomes the only path | — | Full gates (Go, web, e2e, delivery drill) green with the flag gone, and `ps` proofs hold on a Linux host, not only Docker Desktop |

Stages 1 and 2 are implemented; 3 and 4 are not. `docs/SANDBOX-DEPLOY.md` §1.4.3 and
§1.4.4 record them with raw `ps`/`mount`/`chmod`/commit output.

### 10.1 Stage 2's remaining work (named, not hidden)

These are the pieces stage 2 did **not** move off the host path. Each one is a
deliberate exception, not an oversight:

1. **`worktrees.base_commit` is empty for a detached workspace.** Reading `HEAD` is a
   `git rev-parse` on a path the control plane cannot open, and the runner exposes no
   exec for it. The column is informational (nothing branches on it); the panel's diffs
   come from the container's own git. Fix: return `HEAD` in the `…/worktrees` response,
   or let the provider answer a `head` question through the API it already uses.
2. **Agent stderr still goes to the runner's log**, not to the session's Logs panel
   (`Bridge.StderrTail` is fed only by a locally spawned child). §6's plan — write
   `/workspace/.piforge/logs/<session>.log` and tail it through the file API — is
   unchanged and still open.
3. **The marker carries a pid, not a boot id.** §5's stage-2 item. A probe on a
   restarted container can still read a marker written by a previous boot as proof of
   life. The window is the marker TTL (2 min).
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
7. **`models.json` staging still reads the host copy** under `<dataDir>/pi-agents` and
   writes it into the sandbox. That file is deployment state (it holds only the gateway
   URL and a per-session token), not workspace, so it stays on the state volume by
   design — but it does mean a control plane that loses its data volume cannot
   reconstruct a running session's model configuration.
8. **`-agent-in-sandbox` is still off by default, and it now also requires
   `-sandbox-files remote`** (the file panel has no host tree to read). That is a
   startup check, not a silent coupling.

