# Isolation, Snapshotting and Multi-Tenancy for a Go-based Web Coding-Agent Platform

Reference briefing. All numbers below are from primary docs or vendor benchmarks, cited inline. Numbers marked **[verify]** are order-of-magnitude engineering estimates, not published figures.

---

## 0. Bottom line for an architect

1. **Two-tier execution model, not one.** Run the *agent harness* (Go control plane, transcript, tool router, LLM gateway) in normal pods. Run *only untrusted command execution* in a sandbox. Nothing that holds a credential lives in the sandbox.
2. **Pick microVM (Firecracker) as the default isolation boundary** if you must support `docker build`, `testcontainers`, FUSE, VPN clients, or `sudo`. Vercel explicitly supports those *because* it is a microVM with `sudo` ([docs](https://vercel.com/docs/sandbox/concepts)). gVisor is the cheaper default *only* if your tenants never need privileged/Docker-in-sandbox and you can absorb syscall overhead.
3. **Never cold-start on the request path.** Measured Kubernetes pod cold start for sandbox pods is **~42–50 s p50** on GKE; warm-pool adoption from a pre-created `Sandbox` is **p50 92 ms / p90 182 ms / p99 376 ms** ([agent-sandbox benchmark](https://github.com/kubernetes-sigs/agent-sandbox/blob/main/docs/performance-tuning.md)). Warm pools are not an optimization, they are the product.
4. **Snapshot is a filesystem+memory contract with sharp edges.** Assume: connections break, background processes may not survive, restore may need the identical instance type, and identity may change. Design the transcript as the source of truth, the sandbox as disposable.
5. **Secrets never enter the sandbox.** Broker them at an egress proxy or a git proxy. Anthropic does exactly this for Claude Code on the web; so do Fly Connectors and Codex "network secrets".

---

## 1. Sandbox / isolation technologies

### 1.1 Comparison table (coding-agent workload)

| Tech | Cold start | Per-sandbox overhead | Kernel / syscall compat | Snapshot / fork | GPU | Density per node | Fit for coding agents |
|---|---|---|---|---|---|---|---|
| **Docker (runc)** | ~100–300 ms **[verify]** | ~few MB | Host kernel; full syscalls | None native (CRIU only) | Yes (nvidia runtime) | High | Only for trusted code. Escape = host. |
| **gVisor (runsc, systrap)** | **~300 ms measured** full `runsc run` lifecycle, 1 core/2 GB; maintainer: "a reasonable approximation of the current state"; the widely-quoted 50–100 ms is "not something gVisor ever hit" ([issue 13182](https://github.com/google/gvisor/issues/13182)) | Tens of MB (sentry+gofer) **[verify]** | User-space kernel. `go build`/`npm install`/`rustc` work (GKE/Cloud Run run on it). No KVM, no modules, limited GPU. | **Yes, first-class**: `runsc checkpoint/restore`, background restore, `--leave-running`, app-driven via `/proc/gvisor/checkpoint` ([docs](https://gvisor.dev/docs/user_guide/checkpoint_restore/)) | Via `cuda-checkpoint`, not arm64 | Highest (shared kernel) | Best cost/isolation ratio when no privileged ops needed. Startup is the weak point. |
| **Kata Containers** | **~150–300 ms** quoted ([via gVisor issue](https://github.com/google/gvisor/issues/13182)) — but that figure excludes OCI bundle prep + `kata-agent` init; real pod path is worse | Guest kernel ~30–60 MB **[verify]** | Real kernel → everything works, incl. `sudo`, dockerd, FUSE | VM snapshot depending on VMM (Firecracker/CH/QEMU) | Yes (VFIO) | Medium | Good middle path if you want real-kernel behaviour with k8s-native control. |
| **Firecracker** | **<125 ms boot**, ≤150 microVMs/s/host, **<5 MiB** VMM overhead, 5 emulated devices ([firecracker-microvm.github.io](https://firecracker-microvm.github.io/)) | ~5 MiB + guest kernel | Real kernel, full compat, incl. PTY, inotify, dockerd, FUSE | Full VM snapshot/restore (memory+device state), UFFD lazy restore | **No** (no PCIe passthrough by design) | Very high | **Default recommendation.** What E2B, Fly, Vercel, Cloudflare use. |
| **Cloud Hypervisor** | ~150 ms+ boot **[verify]** | ~10s of MB; PCI-based | Real kernel; broader device model than FC (hotplug, vhost-user, GPU via VFIO) | Snapshot/restore + live migration | Yes (VFIO) | Medium-high | Choose when you need GPU or richer devices but still want microVM. Rust, from the same crosvm lineage. |
| **QEMU `microvm` machine type** | Slower than FC on the same kernel; it's a machine type, not a product | Larger; full QEMU | Real kernel, widest device support | `savevm`/migration (heavy) | Yes | Low-medium | Only if you need exotic devices/arch and accept the footprint. |
| **Kubernetes + RuntimeClass** | Pod schedule+bind dominates, not the runtime | `overhead` field is *scheduler-accounted* | Depends on handler | Depends on runtime | Depends | N/A (orchestration) | The control plane for the above. Use `runtimeClassName` + `scheduling.nodeSelector` + `overhead` ([docs](https://kubernetes.io/docs/concepts/containers/runtime-class/)). |
| **nsjail / bubblewrap** | ~10–30 ms | ~1–5 MB | Namespaces + seccomp + (bwrap) no root needed | None | N/A | Very high | In-process dev sandbox (this is what Claude Code ships), **not** a multi-tenant boundary on its own. |
| **seccomp-bpf** | ~0 | ~0 | Allowlist; brittle for arbitrary toolchains | N/A | — | — | Defense-in-depth only. A filter that permits `go build`+`npm`+`rustc`+`cc` is nearly open. |
| **Landlock LSM** | ~0 | ~0 | FS + TCP/UDP port rules, stackable, unprivileged; **cannot** restrict `chdir/stat/flock/chmod/chown/setxattr/utime/fcntl/access` ([kernel docs](https://docs.kernel.org/userspace-api/landlock.html)) | N/A | — | — | Excellent *inside* a VM/container to bound an already-running agent. Not a tenancy boundary alone. |
| **User namespaces** | ~5 ms | ~0 | Mapping only; shared kernel, large CVE history | N/A | — | — | A building block, not a boundary. Note: Ubuntu 23.10+ restricts unprivileged userns via AppArmor — bwrap-based designs break on hardened hosts. |

### 1.2 "Does the toolchain work?" (concrete)

- `go build`: fine everywhere incl. gVisor. Watch thread counts (GOMAXPROCS) and `mmap` pressure; gVisor's syscall path is slower under heavy link steps → expect build-time inflation **[verify, measure]**.
- `npm install` / `pip install`: fine. The risk is **postinstall scripts**, not syscalls (§4).
- `rustc`: fine, but LLVM is thread- and mmap-heavy; it is the workload most sensitive to gVisor overhead.
- PTY: needs `/dev/pts` in the sandbox. Fine in microVM/Kata; gVisor supports PTYs; bwrap needs `--dev-bind /dev`. **Architectural rule: the PTY master must live *inside* the sandbox** or snapshots will sever it (§6).
- `inotify`: works in VM guests and gVisor. It is weakest over `virtiofs`/overlay lower layers — test file-watcher-based dev servers before committing.
- `docker build` / testcontainers / FUSE / VPN: **microVM or Kata only.** Requires `sudo` inside.

### 1.3 Who uses what (published)

| Platform | Isolation | Published cold start / behaviour |
|---|---|---|
| **Firecracker (baseline)** | KVM microVM | <125 ms boot, <5 MiB, ≤150/s/host ([site](https://firecracker-microvm.github.io/)) |
| **OpenAI Codex Cloud** | Not documented as a runtime choice; exposes *environment* controls instead: "Internet access preset: Package managers", additional allowed domains, **network secrets** that also grant access to their domains, OIDC, VPN ([docs](https://learn.chatgpt.com/codex/environments/cloud-environments)) | Per-task workspace; setup script + "start skill" cached per environment |
| **Anthropic Claude Code (local sandbox)** | bubblewrap (Linux) / Seatbelt (macOS), FS + network isolation, network only via a unix socket to an out-of-sandbox proxy with a domain allowlist; 84% fewer permission prompts ([engineering post](https://www.anthropic.com/engineering/claude-code-sandboxing), [OSS runtime](https://github.com/anthropic-experimental/sandbox-runtime)) | No container; ~ms |
| **Anthropic Claude Code on the web** | Sandboxed VM; **credentials never inside**; a git proxy validates a scoped credential and attaches the real token, enforcing branch/repo ([same post](https://www.anthropic.com/engineering/claude-code-sandboxing)) | — |
| **E2B** | Firecracker | <200 ms class; pause ≈ **4 s/GiB RAM**, resume ≈ **1 s** ([persistence](https://docs.e2b.dev/sandbox/persistence)) |
| **Daytona** | Firecracker-class; snapshots are OCI/Docker-image based | **90 ms** environment creation (vendor claim, [daytona.io](https://www.daytona.io/)); memory preserved across pause/resume + forks per [docs](https://www.daytona.io/docs/en/persistence/) |
| **Modal** | **gVisor by default**; VM sandboxes in beta (`vm_runtime`) | Sandbox default max lifetime 5 min, ceiling 24 h; memory snapshots experimental, 7-day TTL ([snapshots](https://modal.com/docs/guide/sandbox-snapshots)) |
| **Fly.io Machines / Sprites** | Firecracker, no shared-kernel tier | Idle suspend → resume "about a second"; checkpoints restore **in place, same identity**; 100 GB ext4 on S3-backed block (SBD) with CoW checkpoints ([Fly vs Modal](https://fly.io/learn/fly-vs-modal/)) |
| **Cloudflare Containers** | Firecracker microVM per instance, container inside, own kernel | **1–3 s** cold start; ephemeral disk; snapshot/restore only under `durable_object` policy; a restarted instance may land in a *different location* ([architecture](https://developers.cloudflare.com/containers/concepts/architecture/)) |
| **Vercel Sandbox** | Firecracker microVM, dedicated kernel, `sudo` allowed | "Milliseconds" (vendor); resume-from-snapshot faster than fresh create; per-sandbox proxy CA + firewall ([concepts](https://vercel.com/docs/sandbox/concepts)) |
| **Runloop** | MicroVM devboxes with disk snapshots | No published ms figure; snapshot-based warm start |
| **Blaxel** | Persistent microVM per agent | **25 ms resume from standby**, suspend-to-zero, memory+FS intact ([platform](https://blaxel.ai/platform/sandboxes), [docs](https://docs.blaxel.ai/Sandboxes/Overview)) |
| **Northflank** | Kata/Firecracker-capable (listed Firecracker integrator) | — |
| **Docker Sandboxes** | microVM per agent **with its own Docker daemon** ([Docker blog](https://www.docker.com/blog/why-microvms-the-architecture-behind-docker-sandboxes/)) | — |

**Reading the numbers:** "cold start" means different things per vendor — VMM boot only (Firecracker's 125 ms), vs. full `runsc run` (300 ms), vs. container entrypoint (Cloudflare 1–3 s), vs. pod scheduling (k8s 42–50 s). The gVisor maintainers make exactly this point: hypervisor-only benchmarks omit OCI bundle prep, block-device creation, and `kata-agent` init ([issue](https://github.com/google/gvisor/issues/13182)). Compare end-to-end "first byte of shell output".

---

## 2. Snapshot & fork semantics

### 2.1 What each layer actually gives you

- **Firecracker snapshot/restore**: full memory + device state to disk, restore with UFFD lazy paging. Preserves guest processes. Does **not** preserve established TCP/vsock sessions; guest clock jumps forward after restore (must be handled in-guest); restoring on a different CPU model requires masking CPU features.
- **gVisor checkpoint/restore**: the most ergonomic for a Go control plane. `runsc checkpoint --image-path`, `runsc restore`, `--leave-running` (implemented as immediate restore, so the container keeps its ID but **may get a new PID**), `--compression=none` enables parallel kernel/memory restore + background restore + `O_DIRECT`, `--exclude-committed-zero-pages` shrinks large zero-filled heaps. CPU-feature pinning via `dev.gvisor.internal.cpufeatures` annotation so snapshots move across heterogeneous hosts. **App-driven checkpoint** via `/proc/gvisor/checkpoint`: write `1` to snapshot, read back `resume` or `restore` — this lets the agent distinguish "I am the original" from "I am the clone", which is exactly what you need to avoid double-pushing a git branch. Restore-time env injection via `/proc/gvisor/spec_environ`. GPU restore via `cuda-checkpoint` (x86 only). ([docs](https://gvisor.dev/docs/user_guide/checkpoint_restore/))
- **CRIU**: process-tree checkpoint/restore for plain containers. Kernel-version dependent, needs `CAP_SYS_ADMIN`/`CAP_CHECKPOINT_RESTORE`, brittle with exotic fds (io_uring, some GPUs, in-flight AIO). In a multi-tenant platform it is an escape-surface multiplier — you are restoring into the *host* kernel. Prefer VM/gVisor-level snapshots. ([criu](https://github.com/checkpoint-restore/criu))
- **E2B fork**: snapshot-in-place then N boot-from-snapshot, one snapshot cost regardless of fork count, up to 20 forks/call, each fork independently succeeding/failing. **The original is paused during the fork, and all active connections (WebSocket, PTY, command streams) are dropped.** ([fork docs](https://docs.e2b.dev/sandbox/fork))
- **E2B pause**: FS + memory; *filesystem-only* mode available. Paused sandboxes kept indefinitely. Notable failure mode: a pause can be **refused with 503** while the node finishes the previous snapshot; if refusal persists ~2 min past the timeout, it degrades to a **filesystem-only** pause and memory is lost ([persistence](https://docs.e2b.dev/sandbox/persistence)).
- **Modal memory snapshot**: alpha; requires `_experimental_enable_snapshot`; **the sandbox terminates when snapshotted**; cannot snapshot while an `exec` is running; **background processes launched by `exec` are not reliably restored**; open TCP connections close; same *exact instance type* required; no GPU; 7-day TTL, not extendable; restore creates a **new sandbox identity** (so any stored ID/URL/webhook pointing at the original is dead). Filesystem snapshots are diff-from-base-image and default 30-day TTL. Directory snapshots can be `mount_image`'d into a *running* sandbox, which is the cleanest "attach a workspace" primitive I've seen. ([snapshots](https://modal.com/docs/guide/sandbox-snapshots))
- **Cloudflare**: snapshots only under the `durable_object` scheduling policy, immutable, restore gives point-in-time FS; disk otherwise ephemeral ([architecture](https://developers.cloudflare.com/containers/concepts/architecture/)).
- **Vercel**: persistent sandboxes auto-snapshot the filesystem on stop and restore on next call; a sandbox spans many sessions (one per resume) ([concepts](https://vercel.com/docs/sandbox/concepts)).
- **Fly Sprites**: checkpoints are **block-level CoW snapshots** and restore the *same* Sprite in place — no new identity, no TTL ([Fly vs Modal](https://fly.io/learn/fly-vs-modal/)). For "rewind my agent's workspace", in-place is materially simpler than clone-with-new-identity.

### 2.2 The consistency problems (this is where platforms get burned)

1. **Open files / half-written builds.** A snapshot is not a filesystem quiesce. `npm install` mid-write yields a `node_modules` that is *structurally* present but has a truncated tarball extraction; `go build` leaves partial object files under `$GOCACHE`. Mitigation: write to a staging dir + `rename(2)` (atomic), or take snapshots only at tool-call boundaries where you control the FS (agent loop is idle), or `fsfreeze`-style quiesce in-guest before checkpoint.
2. **DB connections.** Any pooled TCP connection is dead on restore. Restored processes will throw `ECONNRESET` on first use. gVisor explicitly documents this for `--network=host` (connected sockets return `ECONNRESET`; `epoll_wait` returns `EPOLLERR|EPOLLHUP`); Cloudflare/Modal/E2B all state connections close. Mitigation: (a) never let the *sandbox* hold DB connections — the transcript lives in the control plane; (b) if you must, wrap clients in reconnect-and-retry with an idempotency key.
3. **Double side effects.** A forked sandbox is a *clone of intent*: two processes may both `git push`, both `npm publish`, both POST to a webhook. Mitigation: the `/proc/gvisor/checkpoint` `resume` vs `restore` signal (or an injected nonce in `spec_environ`) so the clone knows to suppress external effects; plus proxy-side enforcement (Anthropic's git proxy checks the branch/repo, so a rogue clone cannot push elsewhere).
4. **Latency of snapshot ≠ 0.** E2B: ~4 s per GiB of RAM, and the pause scales with dirty disk since the last snapshot. A 16 GB tenant costs ~64 s of unavailability per fork. Budget it, and prefer filesystem-only forks for "parallel attempts" where you can re-run the build.
5. **Restore placement constraints.** Modal: same instance type, no region pin. Cloudflare: may land in another PoP. Firecracker: CPU-feature compatible host (or masked CPUID). gVisor: pin features via annotation. If you bin-pack aggressively, snapshot restore becomes a scheduling problem with hard constraints.
6. **Identity drift.** Modal's restore = new sandbox ID; Fly's = same Sprite. Decide up front whether your product's "rewind" is a *fork* or an *in-place rollback*; it changes your URL scheme, your webhook contracts, and your audit model.

---

## 3. Warm pools, image caching, scheduling

### 3.1 The k8s-native pattern: sandbox as a CRD

[`kubernetes-sigs/agent-sandbox`](https://github.com/kubernetes-sigs/agent-sandbox) (SIG Apps, 4.4k★) is the reference design and worth reading end-to-end:

- `Sandbox` CRD = one stateful singleton pod with stable hostname + optional PVC, with pause/resume and scheduled deletion.
- Extensions: `SandboxTemplate` (reusable pod spec, pre-configured `runtimeClassName: gvisor|kata`), `SandboxWarmPool` (pre-created ready Sandboxes), `SandboxClaim` (adopt one from the pool).
- Isolation is **delegated** to RuntimeClass — the project is explicit that it is an orchestrator, not a boundary.
- `sandbox-router` is an optional HTTP reverse proxy for runtimes where port-forward doesn't work.

### 3.2 The numbers you must design against

From the project's own [performance-tuning doc](https://github.com/kubernetes-sigs/agent-sandbox/blob/main/docs/performance-tuning.md) (kops + GKE, k8s 1.35/1.36):

- **Cold pod start: ~42–50 s p50.** No amount of controller tuning fixes this; it is scheduling + image pull + container start.
- **Burst warm adoption: ~300 claims/s at p90 ≤ 200 ms** draining a pre-warmed pool (3,600 claims in 12 s from 3,700 replicas), because no pod is created during the burst.
- **Sustained warm adoption + refill: p50 92 ms / p90 182 ms / p99 376 ms** at 45 claims/s Poisson across 4 pools.
- **Per-pool refill ceiling ~70–85 sandboxes/s**, bounded by per-pool workqueue serialization + `kube-scheduler --kube-api-qps=50` (default).
- **Shard warm pools** above ~30 claims/s or ~500–1,000 replicas (4×450 beats 1×1,800). Pool refill must be rate-shaped (`--sandbox-warm-pool-max-refill-rate`), which alone buys ~15% p50/p90.
- **Trap:** `SandboxClaim` with `spec.env` or `spec.volumeClaimTemplates` **always cold-starts**, silently bypassing the warm pool. If your product lets users pass env vars per session, your warm pool is decorative. This is the single most expensive mis-design in this space.
- Second trap: `--sandbox-warm-pool-replenish-delay` is neutral alone but a *single* transient API timeout during claim creation drained the pool and pushed p90 from 74 s → 205 s in their A/B. Don't enable it.
- Control-plane ceiling is real: each `Sandbox` object is ~10 KB in etcd (inlined `podTemplate` + `managedFields`); at 30+ claims/s the API server/etcd becomes the bottleneck, not the runtime. Mitigate with APF (`FlowSchema`/`PriorityLevelConfiguration`) so refill writes can't starve claim adoptions.

### 3.3 Image / layer caching

- **OCI layer cache + overlayfs** is the baseline: pull once per node, `overlay` upper per sandbox. Cost: overlayfs copy-up on first write, and Landlock cannot restrict through an overlay merge (each layer/merge is a separate hierarchy — [kernel docs](https://docs.kernel.org/userspace-api/landlock.html)), so don't build your FS policy *on* overlay semantics.
- **Warm base + per-tenant delta**: snapshot the *dependency-installed* state once per repo (`npm ci` done, `go mod download` done), then per-session only clone the worktree. This is what Codex's "environment" and E2B's "template + snapshot" both are, and what Modal's directory snapshots formalize.
- **Copy-on-write block snapshots** (Fly's SBD: S3 bucket presented as a block device, ext4 on top, block-level CoW checkpoints) avoid a metadata DB entirely and give O(1) clones. Worth copying if you're building your own storage layer.
- **Content-addressed caches (Bazel/Nix-style)** are the right model for *build artifacts* (`$GOCACHE`, `~/.cargo`, `~/.npm/_cacache`): mount a per-tenant, content-addressed, read-mostly cache volume; key by toolchain hash. Do not share a writable cache across tenants (poisoning → §4).
- **Prewarm the toolchain, not just the OS.** Firecracker's 125 ms is irrelevant if `npm install` takes 90 s. Prewarm = page cache warm for `node_modules` + `$GOCACHE` + a JIT-warmed language server.

### 3.4 Bin-packing and autoscaling for bursty agents

- **Node autoscaling**: Cluster Autoscaler is node-group-bound and retries in minutes; **Karpenter** provisions instances directly from `NodePool` constraints and "retries in milliseconds instead of minutes", with consolidation, drift, and expiration ([concepts](https://karpenter.sh/docs/concepts/)). For bursty agent traffic Karpenter also gives you **CapacityBuffers** — pre-provisioned spare capacity specifically to cut pod scheduling latency ([capacitybuffers](https://karpenter.sh/docs/concepts/capacitybuffers/)).
- **But autoscaling cannot fix a 45 s pod start.** The correct stack is: warm pool (absorb the burst) → Karpenter (replace the pool at rate, minutes) → cold start only for genuinely new images.
- **PodOverhead** (`RuntimeClass.overhead`) is how you stop overcommitting: declare the gVisor sentry/gofer or Kata guest-kernel cost so the scheduler accounts for it ([RuntimeClass](https://kubernetes.io/docs/concepts/containers/runtime-class/)). Without it you will pack nodes to OOM.
- **Bin-packing levers**: `topologySpreadConstraints` to keep a tenant's sandboxes apart (blast radius), `podAntiAffinity` per tenant, taints per RuntimeClass (`sandbox=gvisor:NoSchedule`) so warm pools don't land on GPU nodes. Note agent-sandbox strips `cluster-autoscaler.kubernetes.io/safe-to-evict` on adoption — mirror that if you use CA.
- **Tenant-aware scheduling**: reserve capacity per tier (fair-share across tenants) rather than global FIFO; otherwise one tenant's 400-sandbox RL sweep evicts everyone's interactive sessions.

---

## 4. Multi-tenant security boundaries

### 4.1 Failure modes, concretely

**(a) Container escape → host.** Real and recent: **CVE-2024-21626 ("Leaky Vessels")**, an internal fd leak in runc ≤1.1.11 that let a spawned process obtain a file descriptor to the host filesystem ([NVD](https://nvd.nist.gov/vuln/detail/cve-2024-21626), [Wiz](https://www.wiz.io/blog/leaky-vessels-container-escape-vulnerabilities)). Same class: CVE-2019-5736 (runc overwrite). Any design where untrusted code shares the host kernel inherits this entire CVE stream. Mitigation ordering: microVM > Kata > gVisor > runc+seccomp.

**(b) SSRF into cloud metadata.** The classic is Capital One 2019 via `169.254.169.254`. An agent with a `web_fetch` tool *is* an SSRF primitive, and an agent with a shell is an SSRF primitive with retries. Mitigations, layered:
- No route to link-local at the network policy level, not just in the sandbox's own iptables (the agent has root in its VM and can flush its rules).
- agent-sandbox's default managed NetworkPolicy does exactly this: ingress only from the router, egress only to the public internet, **blocking RFC1918 and cloud metadata endpoints** ([threat model](https://github.com/kubernetes-sigs/agent-sandbox/blob/main/docs/security/threat_model.md)).
- Force IMDSv2 with `http-put-response-hop-limit=1` on the node role ([AWS docs](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/configuring-instance-metadata-options.html)) so a proxied PUT can't cross a hop.
- Deny `fd00:ec2::254` and all ULA/link-local/CGNAT, and enforce on *resolved IPs* per request to defeat DNS rebinding.
- Their router also validates `X-Sandbox-Pod-IP` is an IP literal outside local classes — and warns the default authorizer is `AllowAll`, so that IP-class check is the *only* SSRF defense there. Don't ship that.

**(c) Exfiltration of other tenants' secrets.** Two sub-cases: (i) secrets in the sandbox's env/files — Modal injects secrets as env vars, which is the common pattern and the weakest one ([Fly vs Modal](https://fly.io/learn/fly-vs-modal/)); (ii) lateral reads via a shared metadata/config service. Mitigation: secrets brokered at the proxy (below), never env-injected; the sandbox holds at most a short-lived, audience-scoped token that is useless outside the proxy.

**(d) Prompt-injection-driven lateral movement.** The agent reads a malicious `README`/issue/PR comment and then uses *its own legitimate tools* to exfiltrate. Filesystem+network isolation are both required — Anthropic's post is explicit that network isolation without FS isolation lets a compromised agent read SSH keys, and FS isolation without network isolation lets it exfiltrate: "effective sandboxing requires *both*". Their proxy also asks for user confirmation on newly requested domains, which converts silent exfil into a visible prompt.

**(e) Supply chain via `npm install` / `pip install` postinstall.** Two concrete precedents:
- **Shai-Hulud npm worm** (2025): self-replicating, 200+ packages, harvested tokens/keys from developer and CI environments ([Unit 42](https://unit42.paloaltonetworks.com/npm-supply-chain-attack/), [Zscaler](https://www.zscaler.com/blogs/security-research/mitigating-risks-shai-hulud-npm-worm)).
- **Ultralytics PyPI compromise** (Dec 2024): compromised build env shipped wheels that dropped an XMRIG crypto miner ([ReversingLabs](https://www.reversinglabs.com/blog/compromised-ultralytics-pypi-package-delivers-crypto-coinminer), [Wiz](https://www.wiz.io/blog/ultralytics-ai-library-hacked-via-github-for-cryptomining)).
- Design consequence: `npm install` is *arbitrary code execution by a third party* inside your tenant's sandbox, on every build. If the sandbox has a GitHub token, the worm takes it. If the sandbox has unrestricted egress, the miner runs. Both are policy problems, not sandbox problems.

**(f) Crypto miners / resource abuse.** Detect on CPU-time-per-wall-clock, sustained >90% for >N minutes, plus egress to mining pools (block by default). Enforce with cgroup CPU quota *and* wall-clock caps; a miner that is throttled to 10% is still a cost leak.

### 4.2 The mitigation stack that actually holds

1. **Egress proxy with an allowlist, and nothing else gets out.** Two variants:
   - *TLS-terminating proxy with a per-sandbox CA injected into the trust store* — Vercel does this, mounting `/etc/pki/ca-trust/source/anchors/vercel-proxy-ca.pem` and setting `NODE_EXTRA_CA_CERTS`/`PIP_CERT`/`GIT_SSL_CAINFO`/`SSL_CERT_FILE`/… ([concepts](https://vercel.com/docs/sandbox/concepts)). Caveat they document: containers *inside* the sandbox don't inherit the CA, so HTTPS breaks inside `docker build` unless you mount it.
   - *SNI/domain allowlist without MITM* — cheaper, no CA, but you can't inspect paths or inject credentials.
2. **Credential injection at the proxy (never in the sandbox).** Three working precedents: Anthropic's git proxy (sandbox→proxy with a scoped credential, proxy validates repo/branch then attaches the real token), Fly Connectors (credential in control plane, gateway attaches it, OAuth flow, default-deny by label/name-prefix, per-path scoping, SSRF guard on self-targeting + private/link-local), and Codex "network secrets" (saving a secret also allows its domains). Adopt this pattern; it is the difference between "a compromised agent leaks a repo" and "a compromised agent leaks a customer's GitHub org".
3. **No ambient cloud identity in the sandbox.** `automountServiceAccountToken: false` by default (agent-sandbox defaults this for template-created sandboxes). No node instance role reachable. If the agent needs cloud APIs, broker them like GitHub.
4. **Per-tool capability tokens.** The tool router mints a token per tool invocation, audience = the specific upstream, TTL = seconds, bound to `(tenant, session, tool_call_id)`. The proxy validates audience+scope; the sandbox never sees a long-lived credential. This also gives you per-tool audit for free.
5. **Read-only rootfs + writable workspace.** `verity`/`erofs` or a read-only lower + tmpfs upper, with only `/workspace` (and toolchain caches) writable. Kills implant persistence across sessions. Remember Landlock's warning: rules on an overlay layer don't restrict the merged hierarchy, so apply policy to the *merged* paths you actually care about.
6. **Syscall filtering as defense-in-depth, not as the boundary.** seccomp allowlists that still permit `go build` + `npm` + `rustc` + `cc` are close to permissive; Landlock is strictly better for FS intent (path-scoped, inherited, unprivileged) and now covers TCP/UDP ports (ABI 4/10) and IPC scoping (`LANDLOCK_SCOPE_SIGNAL`, `ABSTRACT_UNIX_SOCKET`, ABI 6). Note Landlock cannot restrict `chdir/stat/flock/chmod/chown/setxattr/utime/fcntl/access` — so it bounds *writes*, not *reads metadata*.
7. **Git credential brokering = branch-scoped.** Anthropic's proxy enforces "only pushing to the configured branch". Copy that: a fork-bomb of agent attempts should not be able to force-push `main`.
8. **Audit every tool call**, not every HTTP request. You need `(tenant, user, session, sandbox, tool, args_hash, decision, egress_domain)` in an append-only log; that's also your incident-response dataset.

---

## 5. Multi-tenant data plane

### 5.1 Tenancy model

| Model | Isolation | Cost | When |
|---|---|---|---|
| **Shared tables + `tenant_id` + RLS** | Logical; one bug = cross-tenant leak | Cheapest ops | Default for SMB/self-serve |
| **Schema-per-tenant** | Medium; per-tenant migrations | Higher (catalog bloat, migration fan-out) | Mid-market; per-tenant extensions |
| **DB-per-tenant** | Strongest; per-tenant backup/restore + KMS | Highest | Enterprise / regulated / BYO-region |

**Postgres RLS specifics that bite** (from the [docs](https://www.postgresql.org/docs/current/ddl-rowsecurity.html)):
- Table **owners bypass RLS** unless you `ALTER TABLE … FORCE ROW LEVEL SECURITY`. Your app role must not be the owner.
- `BYPASSRLS` roles and superusers bypass silently.
- **Referential integrity checks always bypass RLS** — "care must be taken … to avoid covert channel leaks of information through such referential integrity checks." i.e. FK/unique violations can leak existence of another tenant's rows.
- Sub-`SELECT`s in policies create **TOCTOU races** (their `mallory` example reads the policy subquery from an older snapshot and sees a row it shouldn't). Keep policy predicates on the row's own columns when possible.
- Set `tenant_id` with `SET LOCAL app.tenant_id = $1` inside the transaction, never a pooled-connection session variable that can leak to the next request. Enforce with a `current_setting('app.tenant_id')` policy, and make the driver *fail closed* if unset.
- Test with a "negative" suite: for every table, assert tenant B cannot `SELECT/UPDATE/DELETE` tenant A's rows. This is exactly the class of bug that hit **Lovable/Supabase (CVE-2025-48757)** — missing RLS in generated projects exposed 170+ apps ([Superblocks](https://www.superblocks.com/blog/lovable-vulnerabilities), [NVD](https://nvd.nist.gov/vuln/detail/CVE-2025-48757)).

### 5.2 Storage & crypto

- **Object storage: prefix-per-tenant** (`s3://ws/{tenant_id}/{workspace_id}/…`) with an IAM/STS policy conditioned on the prefix, plus per-tenant KMS key. Never hand the sandbox a long-lived key — mint STS creds scoped to the prefix and TTL, or better, proxy the IO.
- **Envelope encryption**: per-tenant data key wrapped by a KMS CMK; store wrapped key + ciphertext; rotate CMK without re-encrypting data ([AWS KMS envelope encryption](https://docs.aws.amazon.com/kms/latest/developerguide/concepts.html#enveloping)). Destroy the wrapped key = crypto-shred for tenant deletion (GDPR).
- Snapshot blobs must be encrypted with the *tenant's* key too, otherwise your snapshot store becomes the cross-tenant read path.
- **Audit logs**: append-only (WORM bucket or a log store with immutability), hash-chained per tenant for tamper evidence, containing actor/tenant/tool/decision/egress. Emit from the control plane, not the sandbox (the sandbox is untrusted).

### 5.3 Quotas, fairness, metering

- **Three separate limiters**, don't conflate them: (i) *admission* — max concurrent sandboxes per tenant; (ii) *runtime* — cgroup CPU/mem/disk + wall-clock; (iii) *throughput* — LLM tokens/min and tool-calls/min.
- **Fair scheduling, not FIFO.** Use weighted fair queueing / deficit round-robin across tenants so one tenant's 8-hour build cannot starve interactive sessions. Practically: per-tenant queues + a global scheduler that admits by weight; preempt by **snapshot/pause** (resumable) rather than kill. k8s analogue: **API Priority and Fairness** (`FlowSchema`, `PriorityLevelConfiguration`) for control-plane requests, and separate priority classes for interactive vs batch sandboxes ([APF](https://kubernetes.io/docs/concepts/cluster-administration/flow-control/)).
- **Metering**: emit usage events from the supervisor (CPU-ms, GiB-ms, egress bytes, tokens in/out, snapshot GiB-months) keyed by `(session_id, seq, idempotency_key)`. Aggregate to `(tenant, user, session, sandbox)` so you can bill *and* attribute a runaway agent to a human. This data is also your abuse signal.
- **Backpressure on LLM providers**: per-provider token buckets, per-tenant sub-buckets, circuit breaker on 429/5xx with jittered retry, and a queue that degrades to "agent paused, resumable" rather than failing the run. Never let provider 429s propagate as user-visible errors when you can park the run durably (§6).

---

## 6. Long-running & durable execution

### 6.1 Keeping a run alive across pod restarts

- **Event-sourced transcript is the source of truth.** Append `(run_id, seq, type, payload)` for every model delta, tool call, tool result, and FS diff. The sandbox is a cache; the transcript is the database. On restart, replay the transcript, rehydrate or re-create the sandbox, continue.
- **Durable execution engines** — pick one and put your agent loop *in* it:
  - **Temporal**: Workflow Executions with an Event History; "if a failure occurs, the Workflow Execution can resume from the last recorded event" ([docs](https://docs.temporal.io/encyclopedia/durable-execution)). Go SDK is first-class and the server itself is Go. Best fit for a Go shop.
  - **Restate** ([docs](https://docs.restate.dev/)): durable execution as a library + single binary; lighter ops than Temporal; good for per-tenant isolation.
  - **DBOS** ([docs](https://docs.dbos.dev/)): durable workflows on top of Postgres — attractive if you already run Postgres and want one fewer system, at the cost of DB-coupled scaling.
- **Idempotency keys**: derive `key = H(run_id, step_index, tool_call_id)`. Every activity (LLM call, tool exec, git push) takes the key; the effect layer dedupes.
- **At-least-once tool execution is the hard part.** Shell commands are *not* idempotent. Rules:
  - Classify tools `pure` (read file, grep) vs `effectful` (write, exec, push, network POST).
  - `pure` → retry freely.
  - `effectful` → on ambiguous failure (worker died mid-exec), do **not** blind-retry. Instead: re-attach to the sandbox if it survived; else inspect state (sentinel file, exit-code file written by your shim) and either adopt the result or surface an explicit `unknown_outcome` to the model so it can decide. This is the single most important correctness decision in the design.
  - Wrap every exec in a shim that writes `{exit_code, stdout_hash, started_at, finished_at}` to a durable path *in the sandbox* before returning — that's your reconciliation record.
- **Snapshot as restart vehicle**: if the runtime supports it, pause-on-drain (E2B pause, Fly checkpoint, Blaxel standby) and resume on the new replica. This preserves *in-flight build state*, which transcript replay cannot. It is why platforms advertise 25 ms–1 s resume.

### 6.2 Streaming fan-out (SSE / WebSocket)

- **Stateless API replicas + a fan-out bus.** Redis pub/sub ([docs](https://redis.io/docs/latest/develop/interact/pubsub/)) is fine for ephemeral deltas; it has **no persistence or replay**, so a client that reconnects mid-turn loses tokens unless you also write to the transcript. **NATS JetStream** ([docs](https://docs.nats.io/)) gives you at-least-once + replay-by-sequence, which maps cleanly to SSE `Last-Event-ID`. If volume is low, Postgres `LISTEN/NOTIFY` + the transcript table is the least-moving-parts option.
- **Pattern**: every token/event is appended to the transcript with a per-run monotonic `seq`; SSE emits `id: {seq}`; a reconnect sends `Last-Event-ID` and the replicas replay from the transcript (bus is only a latency optimization, never the source of truth). This survives pod restarts *and* lets you attach a second viewer (the "review" use case).
- **WebSocket for bidirectional** (PTY input, interrupts). Terminate WS at the API tier, not at the sandbox: sandboxes come and go, and Cloudflare/Vercel both warn that sandbox-hosted WS connections drop on stop/snapshot ([Vercel](https://vercel.com/docs/sandbox/concepts)).
- **Rate-limit and coalesce**: a `go build` can emit 100k lines/s. Buffer per (run, stream) with a bounded ring, coalesce to ≤N msgs/s, and drop-with-marker rather than OOM.

### 6.3 PTY session reattachment

- **Put the PTY master inside the sandbox.** Run `tmux`/`dtach` (or your own supervisor) in the guest; the host only relays bytes. If the *relay* owns the pty master, any snapshot/restore severs it and the session is unrecoverable. If the guest owns it, a VM snapshot preserves the whole pty + child process tree.
- **Address PTYs by `(sandbox_id, pty_id)` with a byte offset**, not by connection. On reconnect, reattach to the same tmux socket and replay from the ring buffer's offset. E2B's fork docs are explicit that active PTY/command streams are dropped and clients must reconnect — assume the same.
- **Control-plane side**: a `pty_sessions` table with `(session_id, sandbox_id, pty_id, last_offset, state)`, so a drain/restart can re-establish attachment on another replica without the user noticing.

---

## 7. Go-specific implementation notes

- **Clients you'll actually use**: `firecracker-go-sdk` (github.com/firecracker-microvm/firecracker-go-sdk), `containerd/client` for RuntimeClass-backed pods, `sigs.k8s.io/agent-sandbox/clients/go/sandbox` for the Sandbox CRD, Modal ships a Go client (`github.com/modal-labs/modal-client/go`, including `SnapshotFilesystem`, `ExperimentalFromSnapshot`), E2B/Daytona are HTTP+gRPC.
- **Never put the agent runtime in the sandbox.** The sandbox runs a ~2 MB Go shim: exec, pty, fs, health, and a vsock/HTTP listener. Everything stateful is in the control plane. This also makes the shim auditable.
- **PID 1 / zombies**: your shim is PID 1 in the sandbox; reap children (`signal.Ignore(syscall.SIGCHLD)` is not enough — use a `wait4` loop) or you leak zombies across a long build.
- **Process groups**: every exec gets its own process group + `Setpgid`, and you kill by `-pgid` on timeout. Otherwise `npm install`'s grandchildren survive the kill.
- **Graceful drain**: SIGTERM → stop accepting claims → snapshot/pause active sandboxes → exit. Cloudflare waits up to 15 min for SIGTERM before SIGKILL ([docs](https://developers.cloudflare.com/containers/concepts/architecture/)); size your `terminationGracePeriodSeconds` to your slowest snapshot.
- **Time**: after any restore, guest `CLOCK_MONOTONIC` continues but wall-clock jumps. Any `time.Since()` across a snapshot is wrong. Stamp events with control-plane time.

---

## 8. What to measure before you commit

1. End-to-end "first shell output" for your three top images (Go, Node, Rust) on gVisor vs Firecracker. Ignore vendor boot numbers.
2. `npm ci` on your largest real repo inside each runtime (postinstall-heavy repos are the worst case).
3. Snapshot+pause wall time at 2 GB / 8 GB / 16 GB RAM, and restore-to-first-exec.
4. gVisor syscall overhead on `go build ./...` and `cargo build` vs bare metal (same host, same cache) **[expect 10–40%, verify]**.
5. Warm-pool adoption p50/p99 at your target burst, and whether any per-session env var silently forces cold start.
6. Cost per agent-hour at 30% CPU utilization — the number that decides your pricing.

---

### Source index

Sandbox/runtime: [Firecracker](https://firecracker-microvm.github.io/) · [gVisor](https://gvisor.dev/docs/) · [gVisor C/R](https://gvisor.dev/docs/user_guide/checkpoint_restore/) · [gVisor startup thread](https://github.com/google/gvisor/issues/13182) · [gVisor perf](https://gvisor.dev/docs/architecture_guide/performance) · [RuntimeClass](https://kubernetes.io/docs/concepts/containers/runtime-class/) · [PodOverhead](https://kubernetes.io/docs/concepts/scheduling-eviction/pod-overhead/) · [Landlock](https://docs.kernel.org/userspace-api/landlock.html) · [seccomp](https://docs.kernel.org/userspace-api/seccomp_filter.html) · [CRIU](https://github.com/checkpoint-restore/criu) · [nsjail](https://github.com/google/nsjail) · [bubblewrap](https://github.com/containers/bubblewrap) · [Cloud Hypervisor](https://cloudhypervisor.org/) · [QEMU microvm](https://www.qemu.org/docs/master/system/i386/microvm.html) · [Docker Sandboxes](https://www.docker.com/blog/why-microvms-the-architecture-behind-docker-sandboxes/)

Platforms: [agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) · [perf tuning](https://github.com/kubernetes-sigs/agent-sandbox/blob/main/docs/performance-tuning.md) · [threat model](https://github.com/kubernetes-sigs/agent-sandbox/blob/main/docs/security/threat_model.md) · [E2B fork](https://docs.e2b.dev/sandbox/fork) · [E2B persistence](https://docs.e2b.dev/sandbox/persistence) · [Modal snapshots](https://modal.com/docs/guide/sandbox-snapshots) · [Vercel Sandbox](https://vercel.com/docs/sandbox/concepts) · [Cloudflare Containers](https://developers.cloudflare.com/containers/concepts/architecture/) · [Fly vs Modal](https://fly.io/learn/fly-vs-modal/) · [Blaxel](https://blaxel.ai/platform/sandboxes) · [Daytona](https://www.daytona.io/docs/en/persistence/) · [Codex Cloud envs](https://learn.chatgpt.com/codex/environments/cloud-environments) · [Anthropic sandboxing](https://www.anthropic.com/engineering/claude-code-sandboxing) · [sandbox-runtime](https://github.com/anthropic-experimental/sandbox-runtime)

Scheduling/durability: [Karpenter](https://karpenter.sh/docs/concepts/) · [CapacityBuffers](https://karpenter.sh/docs/concepts/capacitybuffers/) · [APF](https://kubernetes.io/docs/concepts/cluster-administration/flow-control/) · [Temporal](https://docs.temporal.io/encyclopedia/durable-execution) · [Restate](https://docs.restate.dev/) · [DBOS](https://docs.dbos.dev/) · [NATS](https://docs.nats.io/) · [Redis pub/sub](https://redis.io/docs/latest/develop/interact/pubsub/)

Data/security: [Postgres RLS](https://www.postgresql.org/docs/current/ddl-rowsecurity.html) · [KMS envelope](https://docs.aws.amazon.com/kms/latest/developerguide/concepts.html#enveloping) · [IMDS options](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/configuring-instance-metadata-options.html) · [CVE-2024-21626](https://nvd.nist.gov/vuln/detail/cve-2024-21626) · [CVE-2025-48757](https://nvd.nist.gov/vuln/detail/CVE-2025-48757) · [Shai-Hulud](https://unit42.paloaltonetworks.com/npm-supply-chain-attack/) · [Ultralytics](https://www.reversinglabs.com/blog/compromised-ultralytics-pypi-package-delivers-crypto-coinminer)
