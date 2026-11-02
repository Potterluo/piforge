# 沙箱评估：BoxLite（每 box 一个 microVM，自有内核，可嵌入）

参考源码：`%USERPROFILE%\Downloads\boxlite-main`（下称 BoxLite，Rust 工作区 + Python/Node/Go/C SDK +
一个云控制面 `apps/`）。版本 `0.10.8`（`Cargo.toml:40`、`sdks/python/pyproject.toml:3`、
`sdks/node/package.json:3`）。这份快照**不是 git 仓库**（无 `.git`），所以"最后一次发布是哪天、
谁在维护"这类问题只能从文件里间接读。

**后来补齐了这块**：本轮另做了一次 `git clone --depth 1`（HEAD
`5cca8f13864fc30ea0b75f3801eb26aa39c010c3`，2026-10-29T07:38:11Z，3860 个文件），
发版日期、star、提交节奏、最后提交时间都从那里取，见 §7。两个副本不是同一份东西：快照可能落后于
HEAD，两边说法不一的以 HEAD 为准并标出来。

**怎么拿到它的，值得单独记一笔**（它决定上面"本机到 github 不通"这句该不该原样保留）：

| 路线 | 结果 |
|---|---|
| `web_fetch`（仓库页 / raw README） | **不通**：`data.normalized_url must be a string`、`fetch failed` |
| `curl.exe` | **不通**：`github.com`、`raw.githubusercontent.com` 都是 `http_code=000` |
| `Invoke-WebRequest` → `raw.githubusercontent.com` / `api.github.com` / `cdn.jsdelivr.net` / `raw.githack.com` | **不通**：`SSL connection could not be established` |
| `Invoke-WebRequest` → `github.com` 的 HTML | **通**：`200`、401994 字节（带不带 `http://127.0.0.1:7892` 代理一样） |
| `git ls-remote` / `git clone`（HTTPS） | **通**——这才是决定性的那条 |

所以"本机到 github 不通"要收窄成：**到 raw / api / CDN 不通，到 `github.com` 的 git 与 HTML 通**。
`git fetch --unshallow` 会 `RPC failed; curl 28` 失败，所以完整提交历史没有，只有浅克隆 + atom feed。

本文只回答一个问题：**它能不能当我们面板背后那层沙箱，或者比现在的 docker / K8s / E2B 更好。**
所有结论都指到具体文件与行；拿不到一手证据的一律写"未核实"。

一句话结论：**BoxLite 的隔离是真东西，但它不是"面板沙箱"，而是一台"能跑 OCI 镜像的、可编程的
单租户虚拟机宿主"。** 我们面板要的三件事里它原生只有一件（终端），浏览器只有 SDK 侧的便利方法
（服务面没有，§4.5）、文件只有 tar，
而把 AIO 镜像跑进 box 之后**没有任何受支持的路径能让控制面连到箱子里的 8080**——本地
`boxlite serve` 既不发布端口也不实现 tunnel。所以它现在的身份是"候选隔离层"，不是"候选 provider"。

---

## 1. 先纠正 README 的三句话

README 的段位很高（`README.md:26-34`），逐条对代码：

| README 的说法 | 代码里的事实 | 判定 |
|---|---|---|
| "its own kernel"（`README.md:30`） | 每个 box 有自己的内核，但内核是 **libkrunfw 预编译产物**（`v5.4.0` + sha256，`src/deps/libkrun-sys/build.rs:16-31`），不是 BoxLite 写的内核；也可以换（`docs/guides/custom-kernel.md:1-20`，RC 特性、`BOXLITE_EXPERIMENTAL` 白名单控制） | 准确，但"自有"指"每箱一份"，不是"自研" |
| "micro-VM … KVM / Hypervisor.framework"（`README.md:163`） | VMM 是 **libkrun**（BoxLite 的 fork，`.gitmodules:1-7`），shim 进程里直接调它的 FFI（`src/shim/src/main.rs:214`、`:235` 的 `krun_start_enter`）；`docs/concepts/security.md:36` 的图里写的就是 `VM (libkrun/KVM)` | 准确。**不是 Firecracker，也不是它自己的 VMM** |
| "daemonless, embed as a library"（`README.md:31`） | 库模式是真的：`BoxliteRuntime` 在进程内，每 box 一个 shim 子进程（`docs/concepts/README.md:43-49`）；不需要 root，但**要 `/dev/kvm` 的读写**（`src/boxlite/src/system_check.rs:122-145`） | 准确，但"no root"的实际门槛是 kvm 组 |

还有一处必须点明：仓库里**同时有**一个正在写的原生 VMM（`src/vmm/`，自带 KVM/HVF/WHP 后端）。
它不是现在跑 box 的东西：`src/vmm/README.md:42-46` 说"x86_64 KVM 启动与生命周期就位，arm64 与 HVF
随 M1，virtio 与引擎适配随 M2"；WHP（Windows）"整个后端计划在 M10，现在只保留了模块"
（`docs/contributing/architecture/vmm/whp.md:4-6`），并明说"没有任何 CI 任务编译 Windows target"
（`src/vmm/README.md:444-446`）。**所以 Windows 原生这条路在代码里是空的，不是"实验性"。**

---

## 2. 隔离与运行时

### 2.1 一个 box 从创建到执行，谁在跑

```
控制面/SDK → BoxliteRuntime（进程内库）
            → ShimController 起 boxlite-shim 子进程，过 jailer（bwrap / seccomp / landlock / 降权）
              → libkrun 建 VM（KVM 或 HVF），加载 libkrunfw 的内核
                → 客户机里跑 boxlite-guest（vsock 上的 agent）
                  → 客户机里用 libcontainer（youki）+ oci-spec 起 OCI 容器
```

证据：`docs/concepts/README.md:52-79`（jailer 边界 → shim → vsock → guest agent → OCI runtime）、
`src/guest/Cargo.toml`（`libcontainer = "0.6"`、`oci-spec`、`tokio-vsock`）、
`src/guest/src/container/`（`zygote.rs`、`console_socket.rs`、`capabilities.rs`、`lifecycle.rs`）、
`docs/concepts/security.md:15-24`（Linux：namespace / chroot / seccomp / 降权 / cgroups v2）。

jailer 默认是开的（`src/boxlite/src/runtime/advanced_options.rs:106-110`："Default: on for Linux
and macOS"），CLI 有 `--security=enable|disable` 的开关（`src/cli/src/cli.rs:1665-1667`）。REST 不接受
客户端指定隔离级别（`openapi/box.openapi.yaml:2267-2278`：CreateBoxRequest 是
`additionalProperties: false`，且明写 "Sandbox isolation is likewise absent, and deliberately so:
it is the operator's policy"）。

### 2.2 宿主要求（这一段决定它能不能进我们的部署）

| 平台 | 要求 | 出处 |
|---|---|---|
| Linux | `/dev/kvm` **存在且可读写**，并且要过一次 HLT 冒烟测试 | `src/boxlite/src/system_check.rs:122-145`、`:157-181` |
| macOS | Apple Silicon + `kern.hv_support=1`；Intel Mac 明确不支持 | `system_check.rs:335-341`、`:373-383` |
| Windows | **没有原生路径**；非 Linux/macOS 直接 `Unsupported("BoxLite only supports Linux and macOS")` | `system_check.rs:61-66`、`:409-423` |
| WSL2 | 支持，但要 Windows 11 / build 21390+ 且 `.wslconfig` 里 `nestedVirtualization=true` | `system_check.rs:196-203`、`README.md:239` |

**对本机（Windows + Docker Desktop）意味着什么：** 唯一能跑的是 WSL2 里那台 Linux，且 `/dev/kvm`
必须可读写。实测（本轮，`wsl -d Ubuntu-24.04`）：`/dev/kvm` 存在、CPU 有 `vmx`，但 WSL 里的普通
用户**既不能读也不能写**（`test -r/-w /dev/kvm` 均失败，且不在 `kvm` 组）。也就是说在本机能做的
第一件事是 `sudo usermod -aG kvm $USER` 再重进会话——正是 `system_check.rs:209-216` 给出的那句
`sudo usermod -aG kvm $USER && newgrp kvm`。**这不是 BoxLite 的缺陷，但它是"本机能不能试"的第一道闸。**

**对 Kubernetes 意味着什么：** 官方给的就是特权 pod + 宿主 `/dev/kvm`（`docs/guides/deployment-patterns.md:71-103`
的 Pod 片段：`privileged: true`（`:85`）+ `hostPath: /dev/kvm`、`type: CharDevice`（`:94-97`）），并注明
"Only works on KVM-enabled nodes"（`:102`）。Docker 里则是 `--privileged --device /dev/kvm`（`:63`），
官方自己说"Not recommended for multi-tenant environments"（`:68`）。云上要 nested virtualization（`system_check.rs:229-233` 直接点了
"cloud instances need this explicitly"；`apps/runner/README.md:10` 说它在 GCE/EC2 上跑 nested KVM，
监听 `:3003`）。

含义：它和我们现在"控制面不持 root、runner 持 docker CLI"的形状是同构的——**特权只发生在 runner 那一层**。
差别是 docker provider 只要 daemon，BoxLite 要**节点的 KVM**，所以它不是随便一个 K8s 集群能落的，
得是裸金属或显式开了嵌套虚拟化的节点池。

### 2.3 隔离强度，实事求是

比 docker-per-project 强是确定的：容器边界之上还有一层硬件虚拟化，容器逃逸到宿主要多穿一层
hypervisor。但**别把"VM"读成"更强就免费"**：客户机里仍然是 libcontainer 起的 OCI 容器，容器内的
Linux capability 基线是 14 项（`docs/contributing/architecture/container-capabilities.md:28-30`），
`cap_add` 能把它放大（同文件 `:37-40` 自己写了"加 capability 会削弱容器边界"）。所以它给的是
"**租户之间**的强边界"，箱内仍是普通容器语义——这正好是我们要的形状。

### 2.4 它已经出过一次 10.0，而且坏掉的正是"只读挂载"

这一条不在源码里，在公告库。既然我们是拿它当**对抗性边界**用的，就必须写在隔离这一节：

| 项 | 事实 |
|---|---|
| 编号 | **CVE-2026-46695**（GHSA-g6ww-w5j2-r7x3），CVSS **10.0**，`Critical`，GitHub Reviewed |
| 标题 | *BoxLite: Permission Bypass Allows Modification of Read-Only Files* |
| 机制 | 以 `read_only=True` 通过 **virtiofs** 挂进 VM 的宿主目录，**箱内代码可以把它重新挂成可写** |
| 影响 / 修复 | `< 0.9.0` 受影响；`0.9.0` 修复（npm / pip / cargo `boxlite`、`boxlite-cli`、Go 模块同一版本线） |
| 公布 | 2026-05-16，更新 2026-06-23 |

对我们有三层含义：

1. **它不是"理论上可能"，是已经发生过**——而且是"恶意代码绕过一条被当作安全特性宣传的边界"这个类别。
   我们要的正是"隔离对恶意代码成立"，所以这条不是"安全债"，是**准入条件**。
2. 它坏掉的机制（virtiofs 只读）**恰好是"让控制面看见同一批文件"最自然的那条路**（§6、§2.2）。
   换句话说：这个方案里最诱人的那个设计，曾经是它唯一的 10.0。
3. 已修版本远低于我们评估的 `0.10.8`，所以**不是"它有个洞"而是"它修过一个洞、且这个洞的性质值得列入试点验收"**。
   §10 的判定里因此加了一条：用盒内代码去 remount 只读 virtiofs 挂载，作为回归用例跑一遍。

顺带一个只有读代码才知道的坑：`/files` 会**拒绝**落在挂载点内的路径（`400`，理由是
"such a write would not be visible to processes in the box"）。也就是说，一旦工作区做成 virtiofs 挂载，
那个 tar 文件 API 反过来不肯碰它了——文件面板只剩 exec 或箱内 agent 两条路。

---

## 3. 生命周期与持久化

### 3.1 状态机

`configured / running / stopping / stopped / paused / failed / unknown`（`openapi/box.openapi.yaml:2247-2266`）。
其中 **`paused` 不是给用户用的暂停**，定义是"VM frozen via SIGSTOP (during snapshot/export)"
（同处 `:2253-2254`，实现见 `src/boxlite/src/litebox/box_impl.rs:1390-1458`：SIGSTOP → 做事 → SIGCONT）。
对外只有 `POST /start`（`configured|stopped → running`，`:408-431`）和 `POST /stop`
（优雅关机，超时强杀，默认 30s，`:433-461`）。

### 3.2 "persists" 具体指什么

**指磁盘，不指内存。** 一个 box 的持久态是两块 qcow2：

- `~/.boxlite/boxes/{id}/disks/disk.qcow2`（容器盘）、`guest-rootfs.qcow2`（客户机根盘）
  （`docs/reference/file-formats.md:11-13`、`:52-58`）；
- 快照目录里只有 `disk.qcow2`（`file-formats.md:12`），快照对象的字段也只有
  `guest_disk_bytes` / `container_disk_bytes` / `size_bytes`（`openapi/box.openapi.yaml:2641-2673`）——
  **没有内存镜像、没有 vCPU 状态**。创建时对 VM 做 quiesce 拿一致性（`src/boxlite/src/litebox/local_snapshot.rs:60-64`），
  本质是"停机拍盘"，不是 CRIU。

所以"never from cold"（`README.md:26`）是营销话术。真实语义是：**`stop` 关机、`start` 冷启动**，
装过的包、写过的文件都在盘上，进程和内存没了。冷启动实测 ~2.1s（带 jailer，macOS ARM64，
拆解见 `docs/contributing/investigations/boot-latency-analysis.md:10-45`；不带 jailer ~0.7s）。Linux 上的
数字**未核实**（同一份分析只覆盖 macOS）。

一个重要的对照：这和我们 docker provider 的 `Pause/Resume`（`docker stop` / `docker start`，
`docs/SANDBOX.md:114-116`）是**同一档语义**——进程都结束、磁盘都在。顺带说明一处需要订正的表述：
`docs/SANDBOX-GENET.md:241` 写我们"容器里的 Chromium、shell 会话都在，内存也还在"，按 `docker stop`
的语义这句是过强的；BoxLite 的 stop/start 也不保留内存。两者在这一维度等价，差别只在恢复耗时。

### 3.3 装过的东西能活多久

只要 box 存在，盘就在（`docs/concepts/storage.md:28-37`："Box files, kept while the Box exists"）。
`DELETE /v1/boxes/{id}` 才连盘一起删（`openapi:382-403`，`?force=true` 可强行删运行中的）。
过期是两条独立的钟：`auto_stop`（空闲秒数，默认 900，0 关闭）与 `auto_delete`（stop 后多久删，
默认 0 即不删）（`openapi:2372-2386`）。这两条钟**只有 server/cloud 有 sweeper 才生效**
（`src/cli/src/cli.rs:1644-1652`、`:1689-1706`：本地运行时"记录偏好但不执行"）。
`detach: true` 让 box 活过父进程退出（`openapi:2359-2361`）。

---

## 4. 接口

### 4.1 SDK 面

Python 3.10+ / Node 18+ / Rust / C，外加 **Go**（`README.md:42-104`）。Go 和其它四个不一样，必须写清楚：

- Go SDK **全量 CGO**，连"连远端服务器"这条也是 CGO：`sdks/go/rest.go:1-11` 里 `import "C"`，
  `NewRest` 只是把 URL 传给 C 层（`rest.go:52-96`）；
- 链接的是 `libboxlite.a`，由 `go run .../cmd/setup` 从 GitHub releases 下载
  （`sdks/go/README.md:15`、`sdks/go/bridge_cgo_prebuilt.go` 的注释与 `#cgo linux/darwin LDFLAGS`）——
  **只有 linux/darwin 的链接指令，没有 Windows**；
- 这就是"Go 有 SDK"的全部含义：**能在 Go 里用，但用的是 C 库，且只能在那两个平台上编**。

### 4.2 server 模式：`boxlite serve`

`boxlite serve` 是一个 axum 服务，进程内持一个 `BoxliteRuntime`，把 HTTP 翻译成运行时调用，把
WebSocket 翻译成 attach 会话（`src/cli/src/commands/serve/README.md:3-6`）。默认 `127.0.0.1:8100`，
`--api-key` 之外没有别的鉴权（`ServeArgs`，`src/cli/src/commands/serve/mod.rs:53-80`）。

**这里有一个必须分开看的事实：`openapi/box.openapi.yaml` 是给云控制面写的契约，本地 `serve`
只实现了其中一个子集。** 我把两边都列出来。

openapi 里 48 个操作（方法 + 路径，行号是 `openapi/box.openapi.yaml` 的行）：

| 组 | 操作 |
|---|---|
| 基础 | `GET /config`(160) `GET /me`(181) |
| 卷 | `POST/GET /volumes`(207,232) `GET/DELETE /volumes/{id}`(249,263) |
| box 生命周期 | `POST/GET /boxes`(289,328) `GET/HEAD/DELETE /boxes/{id}`(356,371,382) `POST /boxes/{id}/start`(412) `POST /boxes/{id}/stop`(437) |
| SSH | `POST/GET/DELETE /boxes/{id}/ssh`(467,501,522) |
| 快照 | `POST/GET /boxes/{id}/snapshots`(548,572) `GET/DELETE …/{name}`(595,608) `POST …/{name}/restore`(629) |
| clone/export/import | `POST /boxes/{id}/clone`(646) `POST /boxes/{id}/export`(675) `POST /boxes/import`(698) |
| 执行 | `POST /boxes/{id}/exec`(739) `GET/DELETE /boxes/{id}/executions/{eid}`(785,799) `GET /boxes/{id}/attach`(823) `GET …/executions/{eid}/attach`(879) `POST …/signal`(963) `POST …/resize`(991) |
| 文件 | `PUT/GET /boxes/{id}/files`(1020,1065) |
| 网络 | `POST/DELETE /boxes/{id}/network/tunnel`(1118,1178) `PUT …/network/inbound`(1212) `PUT …/network/outbound`(1250) |
| 观测 | `GET /metrics`(1299) `GET /boxes/{id}/metrics`(1318) |
| 镜像 | `GET /images`(1341) `GET /images/usage`(1365) `GET/DELETE /images/{name}`(1387,1402) `POST/DELETE …/aliases`(1424,1459) `GET/POST /images/registries`(1482,1497) `DELETE …/{id}`(1544) |

本地 `serve` 实际注册的路由（`src/cli/src/commands/serve/mod.rs:1810-1930`）：`/v1/me`、`/v1/config`、
`/v1/metrics`、`/v1/volumes*`、`/v1/images*`、`/v1/boxes`、`/v1/boxes/import`、`/v1/boxes/{id}`、
`/v1/boxes/{id}/{start,stop,ssh,metrics,attach,exec,snapshots,clone,export}`、
`/v1/boxes/{id}/executions/{eid}/{attach,signal,resize}`、`/v1/boxes/{id}/files`、
`/v1/boxes/{id}/network/{inbound,outbound}`。

**差集才是重点：本地 `serve` 没有 `/network/tunnel`（也没有任何端口发布）。** 不是漏了路由——它连
handler 都没有，`handlers/network.rs` 只有 `set_inbound` / `set_outbound` 两个函数。
`openapi` 的 CreateBoxRequest 也明说"Host port publication is intentionally absent; use the box
network tunnel endpoint"（`:2269-2276`），而 tunnel 在本地运行时"什么都不做"（`docs/concepts/networking.md:51-67`
"On a local runtime nothing is published, so it does nothing"）；真正的宿主端口发布 `WithPort` 是
**本地运行时 SDK 侧**的能力（`sdks/go/README.md:220`、`:236-238`"Port publication is local-only.
Remote runtimes reject it"），HTTP 面上没有对应路由。

### 4.3 exec / 文件 / 端口 / 流 / WebSocket

| 能力 | 有没有 | 证据 |
|---|---|---|
| exec（异步启动 + 取状态 + 杀） | 有 | `POST /boxes/{id}/exec` 收 `command/args/env/timeout_seconds/working_dir/tty/user`（`openapi:2731-2760`），返回 `execution_id`；`DELETE` 是 SIGKILL+evict（`openapi:799-817`） |
| 流式 stdout/stderr | 有，但是 **WebSocket attach**，不是 SSE/chunked | `GET …/executions/{eid}/attach`，二进制帧首字节 `0x01`=stdout / `0x02`=stderr，文本帧是 `resize` / `signal` / `stdin_eof` / `{"type":"exit","exit_code":N}`（`serve/README.md:310-326`） |
| 真 PTY | 有 | `ExecRequest.tty`（`openapi:2758-2761`）+ `POST …/resize`（`:991`）+ 单 attach 槽位（第二个客户端 409，`serve/README.md:179-180`） |
| 独立 exec 会话 | 有 | 一个 execution 一条 attach，`run` 的 init 另走 `/boxes/{id}/attach`（docker `attach` 语义，`openapi:819-834`） |
| 文件读写 | **只有 tar 进出** | `PUT /boxes/{id}/files?path=&overwrite=`，`Content-Type: application/x-tar`，落到服务端临时目录再 `copy_into`（`openapi:1020-1063`、`serve/handlers/files.rs:26-60`）；`GET` 反向打 tar（`:1065-1109`） |
| 按路径的文件 API（list/stat/read/write/glob/grep/rename/delete） | **没有** | 48 个操作里没有任何 `/file/*`；`handlers/files.rs` 只有 upload/download 两个函数 |
| Range 下载 | 没有 | tar 一次性生成再整包回（`handlers/files.rs:99-124`） |
| 端口转发（本地） | 只有内嵌 SDK 的 `WithPort`，HTTP 面上没有 | 见 §4.2 差集 |
| tunnel（公网/预览 URL） | 契约里有，云里有；本地 serve 没有 | `openapi:1118-1207` vs `serve/mod.rs:1810-1930` |
| 浏览器 / CDP / 截图 | **运行时与 HTTP 面：没有；SDK 面：有（`BrowserBox`）** | `src/` 与 `openapi/` 里 `browser` 的命中只在 OIDC 登录里（`src/cli/src/commands/auth/oidc/*`），`chromium/CDP` 只在 jailer 里抄 Chrome 的沙箱代码（`src/boxlite/src/jailer/credentials.rs:3-10`）；但 `sdks/{node,python}/` 里有 `BrowserBox`，见 §4.5 |
| 指标 | 有 | `GET /metrics`、`GET /boxes/{id}/metrics`（`:1299-1336`） |

### 4.4 箱子里的出口与密钥

- `allow_net` 是**出站白名单**，语义是"IP/CIDR 匹配目的地址；主机名规则匹配 peek 到的 TLS SNI /
  HTTP Host，由网关按名字拨号；**DNS 不过滤**"（`src/boxlite/src/net/gvproxy/config.rs:87-92`）。
  读法：纯 TCP/UDP 只能靠 IP/CIDR 规则，域名规则只对 TLS/HTTP 生效。
- 网络后端是 **gvproxy**（gVisor netstack 的用户态 NAT，`docs/concepts/networking.md:11-31`），
  另有 libslirp 备选（`:43-47`）。这意味着不需要给 box 配 bridge/tap，也不需要节点上的额外网络权限。
- 宿主 loopback 从箱内可达：`host.boxlite.internal` → `192.168.127.254`，且这个地址**受 `allow_net` 管**
  （`src/boxlite/src/net/constants.rs:21-34`）。
- 密钥替换：箱内只看到占位符 `<BOXLITE_SECRET:{name}>`，真值在宿主侧替换，**永不进入客户机**
  （`openapi:2619-2639`、`docs/concepts/networking.md:33-41`）。机制是每 box 一份 10 年期 MITM CA、
  私钥留宿主、叶子证书 ≤24h 自动续（同上）。这是真本事，也是一个真实的信任决定：箱内的 HTTPS
  信任链被宿主插了一级。

### 4.5 浏览器：SDK 里有，服务面没有——而且它给的是宿主回环地址

这一条要更正上面"完全没有"的说法，只说代码事实：

`sdks/node/lib/browserbox.ts`（以及 `sdks/python/boxlite/browserbox.py`）里的 `BrowserBox` 干的是：

1. `find /ms-playwright -name chrome` —— 从 **Playwright 镜像**里找浏览器二进制，
   找不到就报 "Make sure you're using the Playwright Docker image"；
2. `exec` 起 `chrome --headless --no-sandbox --remote-debugging-port=<guest port>`（firefox 走 WebDriver BiDi）；
3. 起一个 **Python TCP forwarder**，"route traffic through port 3000 (which has working port forwarding)"；
4. `endpoint()` 取 `/json/version` 的 `webSocketDebuggerUrl`，**把地址改写成
   `ws://localhost:<hostPort>`** 再返回。

所以准确的结论是三条，不是一条：

- **"一个 box 里能同时有浏览器 + shell + 文件"这件事，机制上是成立的**——因为它就是一个 Linux 容器，
  装什么都行，`exec` 什么都能起。§5 的"必须把 AIO 跑进 box"这条路没有被证伪。
- **但它没有 CDP 的代理层**：没有 `/cdp/*` 这种反代，没有 per-box 的 WS 端点，REST 契约里
  一个 browser 相关的 operation 都没有。`BrowserBox` 是 SDK 侧的**便利方法**，不是产品能力。
- **它交回来的地址是宿主回环**（`ws://localhost:<port>`，走的是"本地运行时的端口发布"那条路，
  见 §4.2 的差集）。这对我们是**明确否掉的形态**：`docs/SANDBOX.md:52-56` 里把端点从
  `127.0.0.1:<随机端口>` 改成 `http://piforge-<hash8>:8080` 就是为了这个。

对我们最要命的一点：**`BrowserBox` 只有 Python/Node 版**。我们的 runner 是 Go、说 HTTP
（`backend/internal/sandboxrunner/server.go`）。也就是说这条能力**不在我们的调用面上**，
要我们自己用 `WithPort` + exec 重写一遍，然后把 `localhost:port` 再翻译成一个内网名字。
工作量不大，但它是"我们补的"，不是"它给的"。

---

## 5. 面板要的三件事

| 需要 | BoxLite 原生 | 结论 |
|---|---|---|
| (a) 带 CDP 的浏览器 | 运行时/HTTP 面：无；SDK 面：`BrowserBox`（返回 `ws://localhost:<port>`，只有 Python/Node） | **必须把 AIO 镜像整个跑进 box**，并且由我们补一层寻址（§4.5） |
| (b) shell | 有，而且是真 PTY + WebSocket + resize | 比我们现在的行式轮询强（`docs/SANDBOX.md:451-457` 自己列的三条缺陷，BoxLite 一条都没有） |
| (c) 文件 API | 只有 tar，没有按路径读写 | 不够。面板的 `contracts.Workspace`（`backend/internal/contracts/contracts.go:462-501`）要 list/read/write/glob/grep/download(Range)/delete，tar 一个都对不上 |

所以只剩一条路：**把 AIO 镜像当 box 的 image 跑起来**。技术上成立——

- 镜像是任意 OCI 引用（`openapi:2290`），私有 registry 也支持（`GET/POST /images/registries`，`:1482-1541`）；
- `entrypoint` / `cmd` / `env` / `user` / `working_dir` / `tty` 全都能覆盖（`openapi:2315-2339`、
  `:2363-2370`），AIO 的 `gem.sh` 那套启动方式不受影响；
- 资源：`cpus 1..16`、`memory_mib ≥ 128`、`disk_size_gb`（`openapi:2300-2315`），默认 `1 vCPU / 1024 MiB /
  10 GiB`（`src/boxlite/src/runtime/constants.rs:63-74`）——AIO + Chromium 至少要把内存抬到 2–4 GiB。

**但"跑起来"和"能用"之间隔着 §4.2 的那个差集**：控制面连不上箱子里的 8080。三种绕法都不好看：

1. 用 `exec` 在箱内 `curl localhost:8080`，把 HTTP 语义翻译成 exec（能跑单次调用，CDP 的 WebSocket
   流、截图二进制、大文件下载全部退化）；
2. 改走内嵌 CGO（`WithPort` 发布宿主端口），见 §8 路线 B；
3. 等上游把 tunnel/ports 做进本地 serve（契约里有、实现没有，且是 pre-1.0 的云优先项目）。

还有一件**未核实**的事，我认为它是这个方案最大的技术未知：**AIO 镜像里的 Chromium 能不能在
"容器里的容器"里起来**。我们已经知道它在 Docker 默认 seccomp 下起不来，得靠镜像自己的
`BROWSER_NO_SANDBOX=--no-sandbox`（`docs/SANDBOX.md:285-297`）；BoxLite 的箱内是 libcontainer + 
它自己的 seccomp 配置（`src/guest/src/container/spec.rs`、`capabilities.rs`），Chromium 的
zygote/namespace 需求在这里是否满足，源码里读不出来，只能跑。

---

## 6. 多租户：它给的和我们要的

| 我们的模型 | BoxLite 给什么 | 差在哪 |
|---|---|---|
| 一个 box 对应一个 `(user, project)` | box 就是 box，没有 owner 概念；`GET /boxes` 列全部（`openapi:328-349`） | 租户归属得我们自己建（名字里编码 / 自己存映射），和 docker provider 用容器名编码 `(user, project)` 一样笨，但可行 |
| 每沙箱一个 API key（`docs/SANDBOX.md:44-69` 用一次真实事故换来的） | **一个部署级 Bearer**，`--api-key` 一把钥匙开所有 box（`serve/README.md:121-126`） | **没有 per-box 凭据**。箱内 AIO 的 `X-AIO-API-Key` 还是我们自己的事（可以 `env` 注入），但"箱子的控制面 API"这一层是共用的 |
| 身份 / 租户隔离 | 本地 serve 是**单租户**：`GET /v1/me` 恒返回 `local-anonymous`，`prefix: null`（`serve/handlers/me.rs:1-14`、`:31-50`） | 多租户身份只有云控制面（`apps/api`）有；那是 AGPL（见 §7） |
| 工作区 = 宿主目录 bind 到 `/workspace`（`docs/SANDBOX.md:100-131`） | **REST 不接受 host bind**：`VolumeSpec` 只有 `managed_volume`，注释写"Host bind mounts have no representation here"（`openapi:2424-2450`）；只读卷还没实现（`:2444-2448`） | REST 面上做不出"控制面写宿主目录、箱子看同一批字节"。**库模式可以**（`VolumeSpec::bind_mount`，`src/boxlite/src/runtime/options.rs:828-848`） |
| 资源上限 | 每 box `cpus` / `memory_mib` / `disk_size_gb`（`openapi:2300-2315`）；cgroup v2 在 jailer 里（`docs/concepts/security.md:20`） | 这一项它比我们强：我们现在 `cpu_milli/mem_mb/disk_gb` 三列是**只写不读**（`docs/SANDBOX-GENET.md:244`） |
| 出口控制 | `allow_net`（IP/CIDR + SNI/Host）+ 独立 `inbound` 策略，默认 inbound disabled（`openapi:2530-2568`） | 比我们现在"整个容器一个 network"细，这可以直接用 |
| 密钥注入 | 占位符 + 宿主侧替换 + MITM CA（`openapi:2619-2639`） | 我们没有对应物；它是加分项，但要接受"宿主能解开箱内 TLS"这个前提 |

一句话：**它给的隔离边界是对的，它给的租户模型是"一个部署＝一个租户"。** 我们要的那层（每 (user, project)
一个 box、每 box 一把钥匙、一个宿主目录）得自己在它上面搭，其中"宿主目录"这一条在 REST 面上搭不出来。

---

## 7. 成熟度（仓库 + 在线元数据）

| 项 | 事实 | 出处 |
|---|---|---|
| 版本 | `0.10.8`，Rust edition 2024，MSRV 1.88 | `Cargo.toml:40-44` |
| 发布节奏 / 最后发布日期 | **有**：`v0.10.5` 2026-09-30 → `v0.10.6` 2026-10-13 → `v0.10.7` 2026-10-18 → `v0.10.8` 2026-10-23，**约每周一个**；tag 共 68 个 | `releases.atom`、`git ls-remote --tags` |
| 最后提交 / 节奏 | `5cca8f1` 2026-10-29T07:38:11Z（`feat(onboarding): guide first Box creation and persist user intent (#2169)`）；`commits/main.atom` 最近 6 条里 5 个不同作者，PR 号已到 `#2191`，**每天多次** | clone + atom feed |
| star | **2642**（2026-10-29 取自仓库页内嵌 JSON 的 `"stargazerCount"`） | `github.com/boxlite-ai/boxlite` |
| 维护者 | `authors = ["Dorian Zheng <https://github.com/dorianzheng>"]`（单一署名），组织 `boxlite-ai`；但提交面上是多人（`DorianZheng` / `Mandalorian-Wang` / `lfld287` / `zombee0`） | `Cargo.toml:42`、`.gitmodules`、atom feed |
| 贡献者总数 / 仓库创建日 | **未核实**：侧栏是 client 渲染的骨架屏，`api.github.com` 本机 SSL 不通，`git fetch --unshallow` 失败（`RPC failed; curl 28`） | — |
| 许可 | 核心运行时与 SDK：**Apache-2.0** | `LICENSE:1-3`、`Cargo.toml:43` |
| 许可（重点） | `apps/` 是**混合许可，含 AGPL-3.0**，源头是 Daytona | `NOTICE:5-20`（"apps/ ... contains AGPL-3.0 and Apache-2.0 components"、"derived from Daytona"） |
| 安全 | `SECURITY.md` 有，走 GitHub private advisory；自己写"pre-1.0，只修 main 和最新 minor，不回补"；支持表停在 `0.9.x`（落后于 0.10.8） | `SECURITY.md:52-66` |
| 安全（公告） | **CVE-2026-46695**，CVSS 10.0，`< 0.9.0` 受影响、`0.9.0` 修，公布 2026-05-16（详见 §2.4） | GitHub Advisory / GitLab Advisory DB |
| 测试面 | `src/` 下 **2946** 处 `#[test]`/`#[tokio::test]`（本机统计）；`tests/` 只有 6 个 Python 文件（插件/脚本级） | 统计于本地副本；`tests/` 见目录 |
| 覆盖率门禁 | codecov：patch 目标 90%，project 只 `informational` | `codecov.yml:1-20` |
| CI | 24 个 workflow，含 `test.yml`（KVM 硬测：`sudo chmod a+rw /dev/kvm` 后跑 `make coverage:vmm:kvm`）和 `e2e-local.yml`（先验 `/dev/kvm` 可读写） | `.github/workflows/test.yml:410-417`、`e2e-local.yml:316-319` |
| 原生 VMM 成熟度 | M1/M2 阶段；WHP 是 M10；Windows target 无 CI | `src/vmm/README.md:42-46`、`:444-446`、`vmm/whp.md:4-6` |
| 生态 | 有第三方在接（AgentScope、deer-flow、Databricks），但那是 README 自述，**未核实** | `README.md:170-177` |

判定：**能读的代码质量是高的**（facade、错误分类、测试密度、威胁模型文档都在），但**产品成熟度是 pre-1.0**：
API 已经有过 breaking change（`openapi:31-45` 自己写了 image API 的破坏性迁移）、安全支持表还没跟上
版本号、有一个正在重写的核心组件（`src/vmm`）、云那一半是 AGPL 的派生代码。补上在线数据之后，
量化的判断是：**非常活跃**（2642 star、68 个 tag、约每周一个 release、每天多次提交、多人维护），
**但"活跃"不等于"稳定"**——我们要是接它，接的是一个每 5–7 天动一次、版本号还没到 1.0 的运行时；
而且它已经有过一个 10.0 的隔离绕过（§2.4）。

---

## 8. 映射到我们的 `SandboxProvider`

我们的接口（`backend/internal/contracts/contracts.go:552-560`）：`Ensure / Get / Pause / Resume / Destroy /
Exec`，可选 `WorktreeSandbox.AddWorktree`（`:575-580`），docker/runner 还各自有 `Endpoint()` 和
`ContainerPath()`（`backend/internal/sandbox/runner.go:57`、`:300`）。runner 的 HTTP 契约一共 6 条路由
（`backend/internal/sandboxrunner/server.go:101-106`）。逐项对：

| 接口 | BoxLite 对应物 | 可行性 |
|---|---|---|
| `Ensure` | `POST /v1/boxes`（`openapi:289`），幂等键 `Idempotency-Key`（`:297-298`）；镜像/资源/env/cmd/entrypoint 全能传 | 直接对应，S |
| `Get` | `GET /v1/boxes/{id}`（`:356`）；状态映射 `running→READY`、`stopped→PAUSED`、404→`MISSING` | 直接对应，S |
| `Pause` | `POST /stop`（`:437`） | 语义等价于 `docker stop`，S |
| `Resume` | `POST /start`（`:412`）；冷启动 ~2s（macOS 实测） | S，接受冷启动 |
| `Destroy` | `DELETE /v1/boxes/{id}`（`:382`）——**会连盘一起删** | 注意：我们 docker provider 刻意"删容器不删数据卷"（`docs/SANDBOX.md:116`），这里行为不同，得在 provider 里绕开 |
| `Exec` | `POST /exec` + `GET /executions/{id}` 轮询，或 WS attach 拿流 | 可用；但没有"一次性拿 combined output"的同步语义，要自己拼，M |
| `Endpoint` | **无解（REST 面）**：本地 serve 不发布端口、不实现 tunnel（§4.2） | 这是整份评估里最硬的一条 |
| `AddWorktree` | 用 `Exec` 在箱内跑 `git worktree add`（和 docker provider 走 AIO API 的做法同构，`docs/SANDBOX.md:199-233`） | 可行，M；但箱内的 git 身份/`safe.directory` 规则要重新踩一遍 |
| 文件面板（`Workspace`） | 只有 tar + exec | 要在 tar 之上实现 list/read/write/glob/grep，或者用 exec 拼 `head`/`grep`，**M–L 且比现在差** |
| 浏览器（CDP） | 无 | 必须把 AIO 跑进 box，而连通性又回到 `Endpoint` 那条死结 |

### 三条实现路线，代价 S/M/L

**路线 A：只当 `boxlite serve` 的 HTTP 客户端（纯 Go，不 CGO）。**
用 `net/http` + `gorilla/websocket` 手写一个客户端（不要用它的 Go SDK——那是 CGO，见 §4.1），实现
Ensure/Get/Pause/Resume/Destroy/Exec，`AddWorktree` 走 exec，文件走 tar。
**能兑现**：一个真实的内核级隔离执行沙箱。
**兑现不了**：`Endpoint()`、浏览器、面板文件 API。
**代价：S–M**（一个 provider 文件 + 一个 HTTP 客户端，形状和 `sandbox/runner.go` 几乎一样）。
**结论：这不是"面板沙箱"，是"另一种执行沙箱"。**

**路线 B：自己写一个 CGO runner（照 `apps/runner` 的形状，不抄它的代码）。**
Go 进程内嵌 `BoxliteRuntime`（`libboxlite.a`），用它自己的 REST+WS 对外（也就是我们现有的
`/v1/sandboxes` 那 6 条路由），内部：
`WithPort(box, 8080)` 发布宿主端口 → `Endpoint()` 成立；
`VolumeSpec::bind_mount` 挂我们的 `<data>/sandboxes/<user>/<project>` → 工作区模型成立；
每 box 的 `SANDBOX_API_KEY` 用 `env` 注入 → 我们"每沙箱一个 key"的模型保住（钥匙由我们发，不经
BoxLite 的鉴权层）。
**代价：L。** 具体是四笔账：(1) CGO + `libboxlite.a` 从 GitHub releases 下（`sdks/go/README.md:15`），
本机到 github 不通，得先解决供应链；(2) 只有 linux/darwin 能编（`bridge_cgo_prebuilt.go` 的 LDFLAGS）；
(3) CGO 把 Rust 的 panic/abort 带进 Go 进程，崩溃域从"一个容器"变成"整个 runner"；(4) 跟一个
pre-1.0 的 Rust API 走，破坏性变更已经发生过一次（`openapi:31-45`）。

**路线 C：把 HTTP 语义翻译成 exec（在箱内 `curl`）。**
**代价 M，但流式/CDP/大文件全废**，而且把每次面板操作变成一次 exec —— 比我们现在走 AIO API
（`backend/internal/aio/client.go:143`、`:178`、`:282`）差一个数量级。**否。**

**现实结论：如果只是"多一个隔离更强的 provider"，走 A 就够，一天能出原型；如果目标是"面板的沙箱也换成
它"，只有 B 成立，而 B 的前提是先证明 Chrome 能在箱里跑（§5 的未核实项）。**

---

## 9. 对照

| 维度 | docker per project（现在） | K8s Pod + PVC + gVisor（计划） | 外部 E2B（`docs/SANDBOX-DEPLOY.md` §4） | Genet（参考） | **BoxLite** |
|---|---|---|---|---|---|
| 隔离边界 | 容器（共享内核） | 容器 + 用户态内核 | Firecracker microVM（§4 开头） | 容器 | **每 box 一个 microVM + jailer** |
| 宿主前提 | docker daemon | 集群 + RuntimeClass | 无（外部服务） | 集群 + 节点 root | **`/dev/kvm` 读写；非 Linux/macOS 不支持** |
| 冷启动 | ~1s（镜像热） | 冷路径 ~42–50s（§2.5(d)） | 未核实（远端） | 十秒级 | **~2.1s（macOS 带 jailer 实测）；Linux 未核实** |
| Pause 语义 | 停容器，盘留 | 无对应物（要么删 Pod 要么留 Pod） | `pause()` 连**内存**一起保，但可能被拒、降级成只保文件系统（§4.1 表） | commit 成镜像再删 Pod | **VM 关机，盘留；无内存快照** |
| 终端 | 容器 shell（AIO 行式） | `pods/exec` 真 TTY | `commands.run/spawn`，**没有 PTY**（§4.1 表） | `pods/exec` 真 TTY | **真 PTY + WS + resize（原生）** |
| 浏览器 / CDP | AIO 镜像里 | AIO 镜像里 | 无托管面，要自己在模板里跑 | 无 | **服务面没有；SDK 的 `BrowserBox` 给回环地址，必须跑 AIO 镜像** |
| 文件 API | AIO `/v1/file/*` | AIO `/v1/file/*` | `files.write/read`（按路径，§4 开头引的 SDK 面） | 无（走 exec） | **只有 tar** |
| 每沙箱凭据 | 有（`api_key_cipher`） | 有 | 有 API key（§4.1 的 Endpoint 一行要求带鉴权） | 无（单一部署级） | **无（一个部署级 Bearer）** |
| 工作区模型 | 宿主目录 bind | PVC | 无宿主目录，`/home/user`（§4.1/§4.2） | 用户级 NFS | **库模式可 bind；REST 不行** |
| 资源上限 | 有（未落地） | ResourceQuota | 模板定义（未核实） | 有 | **有，且落地（cpus/mem/disk + cgroup v2）** |
| 出口控制 | 整容器一个 network | NetworkPolicy | `setNetworkPolicy`（allowedHosts/deniedCIDRs） | 无 | **`allow_net`（IP/CIDR + SNI/Host）** |
| 许可 | — | — | 商业 | — | **核心 Apache-2.0，`apps/` 含 AGPL-3.0** |

**它赢在哪（按重要性）：**
1. **隔离是真的，而且单机可跑。** 我们现在的租户边界是一个容器 + 目录约定（`docs/SANDBOX.md:500-503`），
   它给的是一条硬件边界，且不需要 daemon、不需要 root。
2. **终端是原生的真 PTY。** 我们行式轮询那三条缺陷（无 TTY、无 resize、无作业控制，`docs/SANDBOX.md:451-457`）
   在它这儿根本不存在；这一点和 Genet 那篇 §3.1 的判断一致，但 BoxLite 不需要借 `pods/exec` 的 RBAC。
3. **快照/clone/export 是磁盘级的免费副产品。** 比 Genet 的"commit 成镜像 + 钉节点"便宜得多，
   而且不特权。
4. **资源上限和出口白名单是真落地的**，不像我们是只写不读的列。

**它输在哪（按严重程度）：**
1. **服务面没有浏览器。** SDK 侧的 `BrowserBox` 是真的能连上 CDP，但它交回
   `ws://localhost:<port>`、只有 Python/Node、并且不在 REST 契约里（§4.5）。对我们是硬伤：
   面板的浏览器是我们产品的一部分，不是可选能力。
2. **本地 server 不发布端口、不实现 tunnel**，于是"把 AIO 跑进 box"之后控制面够不着它。
3. **文件面只有 tar**，面板的 `Workspace` 接口一条都对不上。
4. **单租户 server + 单把钥匙**，比我们现在的"每沙箱一个 key"退一步。
5. **Windows 无原生路径**；我们的开发机就是 Windows，实验只能在 WSL2 里做，而 WSL2 里 KVM 权限
   默认没有（本机实测）。
6. **pre-1.0 + `apps/` 是 AGPL 派生**：能用的只有核心运行时和 SDK，云那一半不能碰。

---

## 10. 结论：**试点（pilot）——但只试点"隔离层"，不试点"面板沙箱"**

**单条最强理由（支持它）：** 它是这份候选名单里唯一一个"真硬件边界 + 无需 daemon/root + 原生真 PTY +
资源与出口控制都已落地"的组合，而且单机就能跑起来——也就是说，我们可以在不改变产品形状的前提下，
把"租户之间只隔一个容器"换成"隔一个内核"。

**单条最强风险（反对它）：** 我们面板的三件事里它只原生给一件；另外两件要靠"把 AIO 镜像整个跑进
box"，而**跑进去之后没有任何受支持的路径让控制面连到 8080**（本地 serve 无端口发布、无 tunnel，
REST 无 host bind），唯一解是自写 CGO runner（L），并且 Chrome 在"容器里的容器"里能不能起来
**未核实**。换句话说：**它可能根本不解决我们的问题，而我们会在第 4 周才发现。**

**紧随其后的第二条风险（也是我最想加进验收的一条）：** 这条边界**已经漏过一次**，而且漏的正是
"只读挂载"——CVE-2026-46695，CVSS 10.0，箱内代码能把 `read_only=True` 的 virtiofs 挂载重新挂成可写
（§2.4）。我们要的不是"资源分区"，是"对恶意代码成立的边界"；一个曾经在**这条具体机制**上吃过 10.0
的运行时，可以当候选，但不能不打回归就上。所以试点加一道闸：**用箱内代码去 remount 只读挂载，
在 `0.10.8` 上跑一遍，必须挡住**（见下面第 7 步）。

**最小决定性实验（一天，能真跑）：**

1. WSL2（Ubuntu-24.04）里 `sudo usermod -aG kvm $USER` + `newgrp kvm`，确认
   `test -r /dev/kvm && test -w /dev/kvm` 通过（本机现在是**不通过**的，这是必须的第一步）；
2. 装/构建 boxlite。**注意：这份本地副本不能离线构建**——`src/deps/libkrun-sys/vendor/libkrun`
   与 `vendor/libkrunfw` 两个 submodule 目录是空的（`Get-ChildItem` 里 0 个文件），
   `libkrun-sys/build.rs` 还要从 GitHub releases 下 `libkrunfw v5.4.0`；加上 crates.io，
   这一步要么换一台能联外的 Linux，要么先解决代理。最省事的是反向验证：在能联网的机器上
   `curl -fsSL https://sh.boxlite.ai | sh` 拿预编译 CLI（`README.md:111-116`），只把这台机器
   当作"跑服务的盒子"；
3. `boxlite serve --api-key dev` 起服务，`POST /v1/boxes {"image":"alpine:latest"}`，
   等 `running` 后 `POST /v1/boxes/{id}/exec {"command":"sh","args":["-c","uname -sr; id; cat /etc/os-release | head -2"]}`
   —— **判定 1：`uname -r` 是不是客户机内核（≠ 宿主），即"这是不是真的 VM"**；
4. 同一个服务上 `POST /v1/boxes` 用 **AIO 镜像**、`cmd: ["/bin/sh","-c","sleep 3600"]`、
   `memory_mib: 4096`、`cpus: 2`；起来后 exec
   `sh -c 'curl -sS -m 5 localhost:8080/v1/browser/info || echo NO_API'`
   —— **判定 2：AIO 的 API 在箱内能不能起（Chromium 有没有活着）**；
5. 在**宿主**上 `curl http://127.0.0.1:8100/v1/boxes` 找不到任何端口转发入口，再试
   `curl` 箱子的 8080 —— **判定 3：确认"控制面够不着"这条死结是真的**；
6. 收尾：把 box 里 `/workspace` 写一个文件、`stop`、`start`、再读 —— **判定 4：装过的东西和文件真的还在**；
7. 用**只读** virtiofs 挂一个宿主目录（库模式下 `VolumeSpec::bind_mount` ro，或 REST 下对应写法），
   在盒内 `mount -o remount,rw <挂载点>` 并试着写 —— **判定 5：CVE-2026-46695 的机制在 `0.10.8`
   上确实被挡住了**。

**这五条判定的意思：** 1 和 4 决定"它是不是比 docker 更好的隔离层"（大概率是）；2 和 3 决定
"它能不能替掉 AIO 面板沙箱"（大概是否）。**如果 2 或 3 是否，就只走路线 A，把它当"执行沙箱"，
不要动面板。** 第 5 条是另一码事：**它不过，就什么都不走**——我们要拿它当租户边界，不是当沙盒。

**不要做的：** 不要为了它去改 `SandboxProvider` 的形状（`Endpoint`/`Workspace` 是我们产品的地基，
不是可选项）；不要碰 `apps/`（AGPL）；不要在没有 Linux+KVM 节点的地方承诺它能部署。

---

## 11. 未核实

- **Linux 上的冷启动时间**：`boot-latency-analysis.md` 只测 macOS ARM64（`:1-6`）。
- **AIO 1.11.0 镜像能否在 box 里成功启动 Chromium**（`--no-sandbox` 是否够、箱内 seccomp 是否放行
  zygote/namespace）：源码里读不出来，必须跑。
- **一个 box 的常驻内存开销**（guest 内核 + agent 的基线）：文档没有，只能测。
- **平台数量上限**：没有文档化的"一个 runtime 最多几个 box / 多少 vCPU 总量"。
- **issue 响应速度、贡献者总数、仓库创建日**：发布节奏与最后提交时间已经用 clone + atom feed 补上（§7），
  但这三项仍然拿不到（侧栏是骨架屏、`api.github.com` SSL 不通、`--unshallow` 失败）。
- **CVE-2026-46695 在 `0.10.8` 上的实际行为**：只读到公告的"影响 `< 0.9.0`、`0.9.0` 修复"，
  没有自己复现过 remount（§10 第 7 步就是补这个）。
- **`allow_net` 的绕过面**（域名规则只匹配 SNI/Host，IP 直连与非 TLS 协议的覆盖程度）：只读到
  注释（`config.rs:87-92`），没有读到策略评测。
- **`apps/` 与核心运行时的边界在打包上是否真的干净**（我们只用 core，但 `apps/api`、`apps/runner`
  和它是同一个 workspace）：`NOTICE:5-20` 声明了混合许可，具体到每个 crate 的许可要逐个核。
- **本机 WSL2 的嵌套虚拟化是否真的能跑 guest**：`/dev/kvm` 存在、CPU 有 `vmx`、但当前用户无权限，
  所以"有设备"和"能跑 VM"之间还差一步（`system_check.rs:157-181` 的 HLT 冒烟测试）。
- **这份本地副本能不能构建**：不能（见 §10 第 2 步）。所以实验的最小形态是"能联网的 Linux 机器 +
  预编译 CLI"，不是"在这台 Windows 上从源码构建"。

---

## 12. 实际读过的东西

BoxLite（`%USERPROFILE%\Downloads\boxlite-main`）：

- `README.md`、`NOTICE`、`LICENSE`、`SECURITY.md`、`codecov.yml`、`Cargo.toml`、`.gitmodules`
- `openapi/box.openapi.yaml`（info、48 个 operation、`BoxStatus`、`CreateBoxRequest`、`NetworkSpec`/
  `Outbound`/`Inbound`、`SecretSpec`、`VolumeSpec`、`Snapshot`、`ExecRequest`、files 两节）
- `docs/concepts/{README,storage,security,networking}.md`、`docs/reference/file-formats.md`、
  `docs/contributing/architecture/container-capabilities.md`、
  `docs/contributing/architecture/vmm/whp.md`、`docs/contributing/investigations/boot-latency-analysis.md`、
  `docs/guides/{deployment-patterns,custom-kernel}.md`
- `src/boxlite/src/system_check.rs`、`src/boxlite/src/runtime/{options,constants,advanced_options}.rs`、
  `src/boxlite/src/litebox/{state,local_snapshot,snapshot,box_impl}.rs`、`src/boxlite/src/vmm/{engine,registry,factory}.rs`、
  `src/boxlite/src/net/{constants,mod}.rs`、`src/boxlite/src/net/gvproxy/config.rs`
- `src/shim/src/main.rs`、`src/vmm/README.md`、`src/deps/libkrun-sys/{Cargo.toml,build.rs}`、
  `src/guest/Cargo.toml` 与 `src/guest/src/container/*`
- `src/cli/src/cli.rs`（serve/security 相关段）、`src/cli/src/commands/serve/{mod.rs,types.rs,README.md}`、
  `src/cli/src/commands/serve/handlers/{files,network,me,boxes}.rs`
- `sdks/go/{README.md,rest.go,bridge_cgo_prebuilt.go,go.mod}`、`sdks/python/pyproject.toml`、
  `sdks/node/package.json`、`apps/runner/README.md`、`.github/workflows/{test,e2e-local}.yml`

**在线部分（2026-10-29 实际取到正文的）**：

- [github.com/boxlite-ai/boxlite](https://github.com/boxlite-ai/boxlite)（HTML `200`，401994 字节；
  star 取自页内 `"stargazerCount":2642`）
- [github.com/boxlite-ai/boxlite/releases.atom](https://github.com/boxlite-ai/boxlite/releases.atom)
  （v0.10.5 / 09-30、v0.10.6 / 10-13、v0.10.7 / 10-18、v0.10.8 / 10-23）
- [github.com/boxlite-ai/boxlite/commits/main.atom](https://github.com/boxlite-ai/boxlite/commits/main.atom)
  （最近 6 条提交、5 个作者、PR 号到 `#2191`）
- `git clone --depth 1`（HEAD `5cca8f13`，2026-10-29T07:38:11Z）+ `git ls-remote --tags`（68 个 tag）
- [CVE-2026-46695 / GHSA-g6ww-w5j2-r7x3](https://github.com/advisories/GHSA-g6ww-w5j2-r7x3)
  （CVSS 10.0，`< 0.9.0`，`0.9.0` 修复，公布 2026-05-16）
- [GitLab Advisory Database 同一公告](https://advisories.gitlab.com/pkg/npm/@boxlite-ai/boxlite/)
- 克隆副本里另读了：`sdks/node/lib/browserbox.ts`（`_startChromiumCdp`、`endpoint()`）、
  `examples/node/browserbox_puppeteer.js`、`docs/concepts/{README,storage,networking,security}.md`、
  `docs/contributing/investigations/boot-latency-analysis.md`、`NOTICE`、`apps/NOTICE`、
  `apps/runner/README.md`、`Cargo.toml`

PiForge：

- `docs/SANDBOX.md`、`docs/SANDBOX-GENET.md`（结构、§2 对照表、§3.1、§3.3）、
  `docs/SANDBOX-DEPLOY.md`（§2.5 端点与启动时延、§4 E2B 那一节的对照表）
- `backend/internal/contracts/contracts.go`（`Workspace`、`Sandbox`、`SandboxProvider`、`WorktreeSandbox`）
- `backend/internal/sandbox/runner.go`、`backend/internal/sandboxrunner/server.go`、
  `backend/internal/aio/client.go`
