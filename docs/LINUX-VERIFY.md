# Linux verification

Status: **first pass, 2026-11-01.** Every isolation proof in this repository up
to now came from Windows plus Docker Desktop's Linux container VM. The product,
however, only ever ships into Linux: the control-plane image is Debian, the
sandbox image is the AIO image, and every `chmod`, `chown`, `/proc` read and
`mount` call in the code is a Linux call. This is the separate accounting of what
that means: which Linux assumptions are **proven**, which are **assumed**, and
where the proof was wrong.

The instrument is `scripts/verify-linux.sh`, which an operator runs on a Linux
machine. It is the Linux twin of `scripts/verify-docker-sandbox.ps1` and
`scripts/deliver-check.ps1`: the same assertions where Linux can make them, and
an explicit `SKIP` plus the exact command where it cannot.

## Method

Docker Desktop's containers **are** Linux — `uname -r` reports the WSL2 kernel —
so a container is real evidence for kernel behaviour. It is not evidence for host
behaviour: on Docker Desktop, file ownership is synthesised, `/var/lib/docker`
lives in a VM, and cgroup delegation belongs to that VM. So each assumption is
classified:

* **(a) provable here** — a command inside a Linux container answers it. Run on
  this machine, output pasted below.
* **(b) only a native Linux host can prove it** — ownership of host bind mounts,
  the host's storage driver, cgroup delegation, `bwrap`/`firejail`, a real PID
  namespace. `verify-linux.sh` prints `SKIP` and the command.
* **(c) irrelevant on Linux** — a Windows-only workaround. Named so that nobody
  "ports" it later.

## The assumption table

| id | assumption | where it lives | class | evidence | verdict |
|----|------------|----------------|-------|----------|---------|
| L1 | The sandbox is uid 1000, the control plane is root | `backend/internal/sandbox/docker.go:579`, `backend/internal/capabilities/linux.go:29`, `deploy/Dockerfile.sandbox-agent` | (a) | in the sandbox: `id` → `uid=1000(gem) gid=1000(gem)`; control plane: `uid=0(root)` | holds |
| L2 | The control plane drops `DAC_OVERRIDE`, so 0444 binds it | `docker-compose.yml` `cap_drop`, `backend/cmd/piforged/main.go:604-616` | (a) | running control plane's boot log: `mechanism=copy-ro reason="no CAP_SYS_ADMIN and no CAP_DAC_OVERRIDE: file modes are enforced by the platform"`; `CapDrop=["CAP_DAC_OVERRIDE"]`; its `CapEff=00000000a80425f9` — bit 1 clear, where Docker's default set (`…fb`) has it | holds |
| L3 | The sandbox keeps five capabilities and `gem` keeps none | `sandbox/docker.go:579` `sandboxCapAdd` | (a) | container `CapEff=00000000000000cb` (CHOWN, DAC_OVERRIDE, FOWNER, SETGID, SETUID); `su gem -c 'grep CapEff /proc/self/status'` → `0000000000000000` | holds |
| L4 | `du -sbx /` is a usable measurement for the enforcer's uid | `quotaenforcer/platform_linux.go:78` | (a) | as root `9163716049`; as gem `8576239753` **exit 1**, 17 unreadable directories (`/tmp/*_kernels`, `/var/lib/aio-sandbox/nginx/*`) → 587,476,296 bytes invisible to the uid that runs the enforcer | **was wrong, fixed (F3)** |
| L5 | The boot baseline is comparable with the enforcer's walk | `deploy/quota-enforcer.sh:96` | (a) | baseline written as root `9044482902`; the enforcer's walk as gem `8576239753` → delta **−468,243,149**, clamped to 0, so the whole-container term read 0 on every tick | **was wrong, fixed (F1)** |
| L6 | The baseline describes *this layer before any tenant write* | `deploy/quota-enforcer.sh:88-108` | (a) | `/run` is on the overlay (`findmnt /run` → `overlay`), so the file survives a restart; `docker stop` + `docker start` with 200 MB in `/tmp` moved the baseline `9044482902` → **`10014794913`** (+970 MB), and all of it stopped counting | **was wrong, fixed (F2)** |
| L7 | A root-owned 0555 launcher is unwritable, root included | `deploy/Dockerfile.sandbox-agent:61`, `docs/EXECUTION-BOUNDARY.md:882` | (a) | `gem` → `Permission denied`; root with the provider's caps (`0xcb`) **wrote** a root-owned 0555 file and created a new one inside `/opt/piforge`; the same container without DAC_OVERRIDE (`0xc9`) → `install: cannot create regular file: Permission denied` | claim was **too strong**, corrected (F4); the tenant half holds |
| L8 | Model and MCP config are 0700/0600 | `pimodel/pimodel.go:134,141`, `pimodel/mcp.go:72,75` | (a) host; (b) sandbox | host path is `MkdirAll(0o700)` + `WriteFile(0o600)`; the sandbox path goes through `SandboxFS.WriteFile`, whose mode argument is ignored by design (`materialize/sandboxfs.go:93`), so the file lands 0644 as `gem`. The directory and the per-session gateway token are the control there, not the mode | holds on the host, **not claimed** in the sandbox |
| L9 | 0444/0555 refuse writes and new files | `materialize/materialize.go:645`, `skillsnap/skillsnap.go:225` | (a) | `gem` writing a 0444 file it owns → `Permission denied` (rc 2); `touch` in a 0555 dir → `Permission denied`; `chmod u+w` first → the write succeeds (the residual `EXECUTION-BOUNDARY.md:894` already states) | holds; residual documented |
| L10 | cgroup v2 is where the sandbox limits are | `sandbox/limits.go:186-207` | (a) | `stat -fc %T /sys/fs/cgroup` → `cgroup2fs`; in the container `pids.max=512`, `memory.max=2147483648` — exactly the `--pids-limit 512 --memory 2g` the provider passed | holds |
| L11 | `--ulimit fsize` is inherited and cannot be raised | `sandbox/limits.go:198-207`, `deploy/quota-enforcer.sh:41` | (a) | `/proc/self/limits` → `Max file size 2147483648 2147483648` (bytes — bash's `ulimit -f` reports 2097152 *1024-byte* blocks, dash 4194304 *512-byte* ones, which is why the hook reads `/proc`); `gem` `ulimit -f unlimited` → `Operation not permitted`; a 2.5 GiB `dd` as gem stopped at exactly **2147483648** bytes with `File size limit exceeded` | holds |
| L12 | `git` needs `safe.directory` only for a checkout it does not own | `sandbox/docker.go:1097-1121`, `workspaceremote/exec.go:43-82` | (a) | a root-owned repo read by `gem`: `fatal: detected dubious ownership`, rc 128; the same command with `-c safe.directory=<path>`: rc 0; a gem-owned repo: rc 0 without the flag | holds, and the narrowness is right |
| L13 | Containment resolves symlinks, not only names | `tenantfs/realpath_other.go` | (a) | the non-Windows twin really is `os.Lstat` + `filepath.EvalSymlinks` (read, not assumed); `go test ./internal/tenantfs` passes on Linux; a **bind mount** is invisible to it, which is the residual `DATA-FLOW-AUDIT.md §11` states | holds; bind-mount residual stands |
| L14 | `mount --bind` + `MS_RDONLY` freezes the skill snapshot | `materialize/bind_linux.go`, `skillsnap/unmount_linux.go` | (a) with SYS_ADMIN | `mount --bind`, then `mount -o remount,bind,ro`: write → `Read-only file system`, `touch` → `Read-only file system`, `umount -l` → OK. The shipped sandbox has no SYS_ADMIN and uses `copy-ro` by design | holds |
| L15 | `Pdeathsig` and `Setsid` keep the agent tied to its supervisor | `liveness/proc_linux.go`, `agentsupervisor/proc_linux.go` | (a) | `go test ./internal/liveness ./internal/agentsupervisor` on Linux: **ok** (1.44 s / 0.47 s). The Windows branch is not a silent skip: `proc_other.go` returns nil and both platforms close the agent's stdin on exit | holds; caveat R3 |
| L16 | A real agent turn works on Linux | the whole stack | gated | `verify-linux.sh` logs in, creates a project and session, prompts, and reads `verify-linux.txt` back — only when `PIFORGE_VERIFY_KEY` (or `_KEY_FILE`) is set; otherwise `SKIP` | not run in this pass (no key in this shell) |
| L17 | The image under test **is** the image this tree builds | `deploy/Dockerfile.sandbox-agent` (`LABEL piforge.source-digest`), `backend/cmd/sourcedigest`, `scripts/up-isolated.ps1`, `scripts/verify-linux.sh` | (a) precondition, not a product assertion | `docker image inspect -f '{{index .Config.Labels "piforge.source-digest"}}'` equals the digest `backend/cmd/sourcedigest` computes from this tree and the parent image's id. A mismatch, or no label at all, fails L17 and **refuses** the container section, naming the rebuild command | holds: the stale `:isolated` label `5ce0d3fd2bee` is refused; an image built from this tree is labelled `954f1b873432` and passes |

### (c) Windows-only workarounds that Linux does not need

| id | workaround | why it is not a Linux assumption |
|----|-----------|----------------------------------|
| C1 | `copy-ro` implemented with `FILE_ATTRIBUTE_READONLY` | On Linux the same mechanism is 0444/0555; `mount_plan.go` already notes that on Windows a new file in the directory is still creatable and on Unix it is not |
| C2 | `GetFinalPathNameByHandle` to catch junctions | Junctions do not exist on Linux; the POSIX twin is the path L13 checks |
| C3 | `terminationSignals()` omits `SIGTERM` off Linux | Windows cannot catch `SIGTERM`; the Linux branch adds it |
| C4 | `cmd /c` in the terminal, e2e and provision tests | replaced by `/bin/sh` on the Linux branch |
| C5 | `chmod -R a+rwX` between two writers | needed only where root and `gem` both write the same tree; dropped for a detached workspace |

## What was wrong, and what changed

**F1 — the baseline and the walk were measured as different uids.**
`deploy/quota-enforcer.sh` wrote `du -sbx /` as root; `internal/quotaenforcer`
walks `/` as `gem`. Measured on the shipped sandbox image: root `9163716049`,
gem `8576239753`. The subtraction always produced a **negative** number, which
`Run` clamped to 0 — so the whole-container term, the fix for the "4.84 GB in
`/tmp`" acceptance finding, read zero on every tick of the deployment that ships.
Now the hook measures the baseline as `gem` (same uid, same tree), logs both
numbers so the difference is visible, and `Run` logs a negative delta once a
minute instead of silently clamping. Rebuilt image, first boot:

```
quota-enforcer.sh: writable-layer baseline: 8457008016 bytes as gem
  (/run/piforge/disk-baseline); root sees 9044484420 bytes, and the difference is
  the root-only part of the image the enforcer cannot read
```

and the enforcer's own log, running as `gem` (this is F3's shape too):

```
quota-enforcer: watching root=/workspace sys_root=/ baseline_bytes=8457008016 limit_bytes=2147483648 ...
quota-enforcer: the container walk could not read part of the tree; the budget counts what it could
  sys_root=/ unreadable_entries=17 measured_bytes=8660475317
```

`measured_bytes` > `baseline_bytes`, so the term is `+203 MB` and live; before
F1/F3 it was an error once a second and a clamp to 0.

**F2 — a reused container layer re-measured its baseline.**
`/run` is on the overlay, so `disk-baseline` survives `docker stop`/`start`. The
hook rewrote it on every start, folding whatever the tenant had written outside
`/workspace` into the baseline: measured `9044482902` → `10014794913` after a
200 MB `/tmp` burst and a restart, i.e. the burst became free. The hook now
writes the baseline only when it is absent or empty, and logs on a reused layer:

```
quota-enforcer.sh: keeping the boot baseline already recorded: 8457008016 bytes
  (/run/piforge/disk-baseline); this container layer is reused, so re-measuring
  would fold the tenant's earlier writes into it
```

with 20 MB still in `/tmp` and the baseline unchanged. A fresh container (a new
layer) still measures.

A related measurement, because it explains a false negative I hit first:
`docker restart` on Docker Desktop **did not re-run the image's entrypoint hook**
(restart count 0, no new boot line); `docker stop` + `docker start` did. That is
also the product's pause/resume path, so `verify-linux.sh` uses stop/start and
looks for the "keeping" line rather than merely comparing the number.

**The image in this checkout was stale, and the script said so — but only after
failing on quota assertions.** The prebuilt `piforge/sandbox-agent:isolated` was
built before F1/F2, so a first run of `verify-linux.sh` against it produced two
`FAIL`s —

```
FAIL L5 the uid-1000 walk (8465783972) is below the baseline (9044482902): the whole-container term reads 0
FAIL L6 the baseline moved from 9044482902 to 9078516168 across a restart; the 20 MB burst stopped counting
```

— which is exactly the bug, reported by the check rather than hidden. After
`docker build -f deploy/Dockerfile.sandbox-agent --build-arg AGENT_IMAGE=piforge:local -t piforge/sandbox-agent:linux-verify .`
the same run is **28 passed, 0 failed, 9 skipped** (29 passed once L17 exists).

That run cost a round: two red lines three hundred lines into the drill looked
like a regression, and the only thing wrong was an image from an older tree. So
the staleness is now a **precondition** (L17) instead of a surprise. The sandbox
image carries `piforge.source-digest` — stamped by `deploy/Dockerfile.sandbox-agent`
itself, not only by the build script — and `verify-linux.sh` compares it with the
digest `backend/cmd/sourcedigest` computes from this tree before it asserts
anything. A mismatch refuses the container section and prints what to run:

```
FAIL  L17  piforge/sandbox-agent:isolated was built from another tree: label 5ce0d3fd2bee, this tree 954f1b873432 (parent piforge:isolated); refusing to run the container assertions against it
INFO  rebuild it with:
INFO  docker build -f deploy/Dockerfile.sandbox-agent \
                  --build-arg AGENT_IMAGE=piforge:isolated \
                  --build-arg PIFORGE_SOURCE_DIGEST=954f1b873432c59bf957b6d9c2488d82fadce84e7689a91a40586150ce61e490 \
                  -t piforge/sandbox-agent:isolated .
INFO  or, where pwsh is available: pwsh scripts/up-isolated.ps1 -Rebuild (it rebuilds all three images)
SKIP  L3  the container assertions did not run: piforge/sandbox-agent:isolated is not the image this tree builds
```

### Rebuilding the sandbox image

The one command that refreshes everything, and the one the refusal names first:

```
pwsh scripts/up-isolated.ps1 -Rebuild          # all three images, then start the stack
pwsh scripts/up-isolated.ps1 -BuildOnly -Rebuild   # build only, start nothing
```

`up-isolated.ps1` now refuses too: with `-NoBuild` it compares the labels before
starting and exits with the rebuild command instead of starting a stale stack.
To rebuild the sandbox image by hand — the same command `verify-linux.sh` prints,
with *your* digest:

```
digest=$(cd backend && go run ./cmd/sourcedigest -root .. \
  -dockerfile deploy/Dockerfile.sandbox-agent -agent-image piforge:isolated)
docker build -f deploy/Dockerfile.sandbox-agent \
  --build-arg AGENT_IMAGE=piforge:isolated \
  --build-arg PIFORGE_SOURCE_DIGEST="$digest" \
  -t piforge/sandbox-agent:isolated .
```

The `PIFORGE_SOURCE_DIGEST` build arg is what makes the image carry the label: a
build without it produces an unlabelled image, which both scripts refuse (nothing
then says which tree it came from). The digest includes the id of the
control-plane image the sandbox was built `FROM`, so rebuild `piforge:isolated`
first if its sources changed — `-Rebuild` does both in order.


**F3 — `du`'s exit 1 was treated as "no measurement".**
`du -sbx /` exits 1 whenever *any* entry was unreadable, and as `gem` that is
always (17 directories). `duBytes` returned an error, `MeasureRoot` failed, and
`sysUsed` stayed 0 for the life of the container — the second term was dead in
exactly the place it mattered. `platform_linux.go` now classifies the result:
exit 0 is exact, exit 1 with a total is a `*WalkError` carrying how many entries
were unreadable, anything else is an error. `MeasureRoot` (the container) uses
the partial number and logs it; `Measure` (the workspace) **refuses** a partial
walk, because `gem` owns the workspace and `chmod 000` would otherwise let the
tenant choose its own measurement.

**F4 — the 0555 launcher claim was stronger than the container.**
`deploy/Dockerfile.sandbox-agent` and `EXECUTION-BOUNDARY.md` said root could not
write the launcher "because the container has no capability that bypasses it".
The provider adds `DAC_OVERRIDE` back (`sandboxCapAdd`), so root **can**: measured
`CapEff=00000000000000cb` wrote a root-owned 0555 file, `0xc9` (without
DAC_OVERRIDE) could not. Both comments now say what is true — the mode binds the
tenant (`gem`, CapEff 0, no sudo, no-new-privileges) and not the image's own
bootstrap — and `verify-linux.sh` asserts the write result against `CapEff` so a
future capability change fails loudly instead of silently weakening the story.

**F5 — a refused bind mount deleted its own warning.**
`materialize.installSkillFS` returned only "the mounted copy is writable …" when
the copy fell back, dropping the "bind mount refused (…)" sentence that explains
*why*. Both are kept now, joined.

**F6 — the Go suite could not pass on a Linux host running as root.**
`go test ./...` on a stock Linux root host failed 13 tests in five packages, all for the same
honest reason, none of which the Windows host could show:

* `materialize`, `provision`: expected `copy-ro` on a host where 0444 does not
  bind root. They now ask the host (`modeBitsBind`) and expect `copy` when the
  bits do not bind, which is what the product reports and what the docs claim.
* `gitx` adopt tests: the setup commit had no identity, so on a host with no
  `~/.gitconfig` it died with `Author identity unknown`. The setup commit now
  carries `-c user.name/-c user.email` (command scope only — the repository
  still has no identity, so what the test proves about adopt is unchanged).
* `gitx` `TestPathGuards`, `fsx` `TestCleanRelRules`: a Windows drive path is
  absolute on Windows and a legal relative name on POSIX. The refusal is
  asserted where the platform can honour it; on POSIX the containment invariant
  is asserted instead.
* `store` `TestGatewayRevocationSweepRemovesOnlyExpiredRows`: a past expiry is
  clamped to the revocation instant, so sweeping at the same `now` the test
  started with is a race the coarse Windows clock hid and the nanosecond Linux
  clock always loses. The test now sweeps at an instant after the revoke.

After the fixes: `go test ./... -count=1` on Linux — **42 packages ok, 0
failures** (was 13 failures in five packages).

## What `verify-linux.sh` covers

Run as `./scripts/verify-linux.sh` (`--skip-docker` for the structural half only,
`--keep` to keep the probe container). `PASS`/`FAIL`/`SKIP` per check id, exit
non-zero on any failure, no provider key required.

* **structural** — compose still drops `DAC_OVERRIDE`; `sandboxCapAdd` still adds
  it; the hook writes the baseline once and measures it as `gem`; the enforcer
  keeps the partial-walk classifier; the API still demands the CSRF header.
* **kernel surface** — `uname`; `/proc/self/status` and `CapEff`; whether 0444
  binds this process *and* whether `CAP_DAC_OVERRIDE` explains the answer;
  cgroup v2 presence.
* **freshness** — L17, the precondition for everything below: the sandbox image's
  `piforge.source-digest` label must equal the digest of this tree (and of the
  control-plane image it was built `FROM`). A stale or unlabelled image is
  **refused** before any assertion runs, with the rebuild command printed — see
  "Rebuilding the sandbox image" below. Without Go the check `SKIP`s and says so;
  it never guesses.
* **sandbox container** — boots the real image with the provider's flags
  (`--cap-drop ALL` + the five, `no-new-privileges`, `--pids-limit`, `--memory`,
  `--ulimit fsize`) and asserts: uid 1000 / `gem` CapEff 0; the enforcer running
  as `gem` under a root supervisor; launcher `root:root 555` refused to `gem`
  and root-writable **iff** `DAC_OVERRIDE` is set; cgroup `pids.max`/`memory.max`
  equal to the flags; `RLIMIT_FSIZE` in bytes, unraisable, and a `dd` killed at
  exactly the budget; `du` exit 1 with a total; the baseline root-owned 0644 and
  **not above** the uid-1000 walk; the baseline **unchanged** across a restart
  with 20 MB already in `/tmp`; `safe.directory` semantics; 0444 refuses `gem` and
  the `chmod u+w` residual; `bind-ro` provenance (`SKIP` without SYS_ADMIN, with
  the command).
* **code and daemon** — `go test` on the Linux-only packages; the docker storage
  driver.
* **native host** — the four (b) rows as `SKIP` with their commands when run
  inside a container, executed when run on the host.
* **agent turn** — gated on a key; `SKIP` states how to supply one; the key is
  never echoed.

Measured run against the rebuilt image (2026-11-01, Docker Desktop's Linux VM,
`docker:cli` shell so the container section could reach the daemon):

```
28 passed, 0 failed, 9 skipped
```

The nine skips are the honest ones: no `CAP_SYS_ADMIN` in the shipped sandbox
(bind-ro, with the command to prove it elsewhere), the five native-host rows
(this run was itself in a container), the host `pids.max` (a container limit was
checked instead), and the agent turn (no key).

## Gates (this machine, 2026-11-01)

| gate | result |
|------|--------|
| `go build ./...` | pass |
| `go test ./... -count=1` (Windows host) | pass. The one known flake, `agentsupervisor` `TestSupervisorKeepsTheAgentAliveWhenTheClientLeaves` ("the reattaching client was given no backlog"), was a real attach-order race — the client was published as current before its handshake was queued, so a record the agent wrote in that window reached the wire first and was read as the frame. Fixed in `supervisor.attach`; 50/50 green afterwards, whole package 20/20, and `TestEveryReattachSeesTheHandshakeFirst` guards the ordering |
| `go test ./... -count=1` **on Linux** | 42 packages ok, 0 failures (golang:1.25-bookworm container); 13 tests in five packages failed before this pass's fixes |
| `pnpm --dir web lint` | pass |
| `pnpm --dir web test -- --run` | pass |
| full e2e | 182 passed (8.6 m) |
| `pwsh scripts/deliver-check.ps1` | 125 checks, 0 failures. The first attempt failed exactly one check — "the source checkout is clean after the drill" — because this pass's changes were still uncommitted; after committing only its own paths it passed. No key was echoed, written to an artifact or left in a file, and `pf-local` stayed up and healthy throughout (that container was only ever read) |


## What only a native Linux host can prove

These are the honest gaps. None of them is a claim the product makes today; they
are the points where "it worked on Docker Desktop" is not evidence.

1. **Ownership of a host bind mount** (N1). Docker Desktop synthesises one owner
   for every file behind a Windows bind mount, so every uid-based containment
   claim in the *host* placement was never tested. Command:
   `d=$(mktemp -d); printf x > $d/as-root; stat -c %u:%g $d/as-root; su -s /bin/sh nobody -c "printf x > $d/as-nobody"; stat -c %u:%g $d/as-nobody`
   — expect `0:0` and `65534:65534`.
2. **The host's storage driver** (N2). `docker info --format '{{.Driver}}'` must be
   `overlay2`; the `du -sbx /` arithmetic describes roundup/overlay behaviour, not
   a `vfs` or network filesystem.
3. **`bwrap`/`firejail`** (N3). `-isolation bubblewrap|firejail` needs the binary;
   the shipped image has neither, so `PIFORGE_ISOLATION=none` is the only working
   value there, and the host path (`isolate.go`) has no Linux evidence at all.
4. **cgroup delegation** (N4). `pids.max`/`memory.max` are the kernel's numbers
   inside a container; that the *host* systemd delegates the same controllers is a
   host fact (and the failure mode — `pids.max: max` — is silent).
5. **Pdeathsig under a real PID namespace** (N5). The tests pass on Linux, but the
   Go caveat remains: `Pdeathsig` is delivered when the *thread* that forked the
   child exits, and a Go runtime can retire threads. On a native host with the
   real supervisor (`agentsupervisor`), a long soak is the only way to see it.

## Residuals (stated, not closed)

* **R1 — a bind mount is not a reparse point.** On POSIX a bind mount inside an
  import root resolves to the outside tree and `EvalSymlinks` cannot see it
  (`tenantfs/realpath_other.go`, `DATA-FLOW-AUDIT.md §11`). The deployment's
  import roots are the control.
* **R2 — a tenant that can `chmod` can lift `copy-ro`.** The workspace is owned by
  `gem`; `chmod u+w` then write is a two-step bypass (`EXECUTION-BOUNDARY.md:894`).
  `verify-linux.sh` prints it as `INFO`, because closing it needs a root-owned
  writer inside the sandbox.
* **R3 — the Pdeathsig thread caveat.** See N5.
* **R4 — a tenant can hide bytes from its own uid.** The workspace walk is refused
  when it is partial, but a `chmod 000` directory is not readable at all, so a
  determined tenant can under-report by *not being measurable*; the per-file
  `RLIMIT_FSIZE` and the volume size remain the bounds.
