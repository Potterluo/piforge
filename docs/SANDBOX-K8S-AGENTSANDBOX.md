# 沙箱对照：kubernetes-sigs/agent-sandbox（本地源码）与我们

参考源码：`C:\Users\<user>\Downloads\agent-sandbox-main`（下称本项目，Go + controller-runtime，
`module sigs.k8s.io/agent-sandbox`，`go.mod:1-5`）。README 里给的安装版本是 `v1.0.2`
（`README.md:98`），Helm chart 自己是 `0.1.4`（`helm/Chart.yaml:5`）。

本文只回答一个问题：**这个控制器值不值得当成 PiForge 的 K8s provider 底座。** 所有结论都指到
`路径:行`；本地源码里拿不到一手证据的一律写 **未核实**，不引用上游网页、也不沿用
`docs/SANDBOX-DEPLOY.md` §2.3 的二手口径。§10 专门列出那份文档里被这份源码推翻的条目。

一句话结论：**它是一个真的、成熟度超出预期的"单 Pod 有状态工作负载"控制器，warm pool 也是真的；
但它给的是"编排样板"，不是隔离，也不是凭据。** 它替我们写掉的那部分（Pod/PVC/headless Service/
到期清理/预热池）恰好是我们 `K8s provider` 要写的第一层；它替不掉的那部分（每沙箱一个 key、
Pause 必须先快照、控制面直连沙箱）恰好是我们这一层的全部难点。

---

## 1. 它到底提供了什么

### 1.1 四个 CRD，两个 API 组，都是 v1beta1

`v1alpha1` 已经不是"另一个说法"：本快照里只有 `v1beta1`，路线图把"Alpha to Beta API Versioning"
标成已完成，并明确写了 v1alpha1 类型、conversion webhook 都已删除（`roadmap.md:83`）。

| 资源 | 组 / 版本 | 谁装的 | 作用 |
|---|---|---|---|
| `Sandbox` | `agents.x-k8s.io/v1beta1`（`api/v1beta1/groupversion_info.go:35`） | 核心，默认装 | 一个单实例、有状态、Pod 支撑的沙箱 |
| `SandboxTemplate` | `extensions.agents.x-k8s.io/v1beta1`（`extensions/api/v1beta1/groupversion_info.go:34`） | 可选，`--extensions=true`（`docs/configuration.md:54`） | 可复用的 blueprint + 网络策略 + claim 注入策略 |
| `SandboxWarmPool` | 同上 | 同上 | 预热 N 个已 Ready 的 Sandbox |
| `SandboxClaim` | 同上 | 同上 | 从池子里领一个，领不到就冷建 |

### 1.2 一个 `Sandbox` 对象是什么

`spec` 里的 blueprint 只有四样东西：一个完整的 `podTemplate`（`corev1.PodSpec`，`api/v1beta1/sandbox_types.go:209-217`）、
`volumeClaimTemplates`（`:289-304`）、`service`（`*bool`，`:306-313`）、`failedPodPolicy`（`:315-327`），
外加 `operatingMode`（`:347-359`）和 `lifecycle`（`:343-345`、`:374-396`）。

`status` 是我们真正会读的四个字段：`serviceFQDN`（`:403`）、`service`（`:407-410`）、
`podIPs`（`:420-426`）、`nodeName`（`:428-431`）。`podIPs`/`nodeName` 在 Pod 不存在时会被清空
（`controllers/sandbox_controller.go:547-559`），这一点决定了 `Get` 能不能分清"暂停"和"被删"。

它在概念上就是 **StatefulSet(replicas=1) + 稳定名字 + 可选 PVC + 可选 headless Service**，
README 自己也是这么定位的（`README.md:229`）。

### 1.3 控制器做什么

一次 reconcile 的顺序固定：PVC → Pod → Service（`controllers/sandbox_controller.go:534-568`）。

- **Pod 名 = Sandbox 名**（`:2012`），spec 是 `podTemplate` 的深拷贝（`:1994`、`:2017`），
  PVC 由模板生成卷后合并进去（`:1996-2009`）。控制器不注入 `runtimeClassName`、不注入
  `securityContext`、不注入 `resources` —— 模板写什么就是什么（见 §3）。
- **PVC 名 = `<template.metadata.name>-<sandbox.metadata.name>`**（`:2203`），带 ownerRef
  （`:2263`）。所以 Sandbox 被删时 PVC 会被 GC —— 除非用户先手动解绑。
- **Service 只在 `spec.service: true` 时才建**（`:1467`），类型是 headless（`ClusterIP: None`，
  `:1478`），名字 = Sandbox 名，selector 是 `agents.x-k8s.io/sandbox-name-hash`。
  **端口不是 8080，而是模板里所有 `containerPort` 的镜像**（`:1358-1406`）：不写 `ports:` 就没有
  端口。`spec.service` 不写（nil）时控制器"保留已有 Service 但不新建"（`sandbox_types.go:306-313`），
  这是向后兼容的默认值，也是我们最容易踩的一处：**默认没有 Service**。
- 到期由 `lifecycle.shutdownTime` 驱动：删 Pod（和 Service），然后按 `shutdownPolicy`
  决定留不留 Sandbox 对象（`sandbox_types.go:374-396`）。`shutdownPolicy` 默认 `Retain`；
  选 `Delete` 时 Sandbox 对象没了，PVC 跟着 ownerRef 一起没（`:289-295`）。

### 1.4 warm pool 与 claim：池子是真的，时延也是真的

池子不是"文档里的概念"，是一条独立的 reconcile 链：

- `SandboxWarmPool{replicas, sandboxTemplateRef, updateStrategy}`（`extensions/api/v1beta1/sandboxwarmpool_types.go:40-63`），
  `replicas` 默认 1，带 scale 子资源（`:119`）。
- 池子里的 Sandbox 是 `GenerateName: "<pool>-"` 造出来的（`extensions/controllers/sandboxwarmpool_controller.go:1162`），
  名字是**随机的**，不是 claim 名。blueprint 整体深拷贝自模板（`:1167-1170`），并打
  `agents.x-k8s.io/launch-type=warm`（`:1149`）、`agents.x-k8s.io/warm-pool-sandbox=<hash>`（`:1147`）。
  默认还会打 `cluster-autoscaler.kubernetes.io/safe-to-evict=true`（`:1184-1191`）。
- claim 的 adoption 是**乐观锁**：把候选名写进 claim 的
  `agents.x-k8s.io/sandbox-name` annotation，`Update` 成功即抢到，409 就换候选重试
  （`extensions/controllers/sandboxclaim_controller.go:1402-1428`）。抢到后候选从池子的所有权
  转给 claim，池子不再管它（`sandboxwarmpool_types.go:56-59`）。

时延数字在仓库里，是可引用的（`docs/performance-tuning.md`）：

- 纯 warm adoption（池子已经 Ready，45 claim/s 的 Poisson 到达，4 个池、113 副本/ns，kops）：
  `p50 92ms / p90 182ms / p99 376ms`（`docs/performance-tuning.md:168-178`）。这张表的配置是
  `replenish-delay=0` + `max-refill-rate=100`；旁边 burst-tuned 那一行前 10 秒 ~57ms，之后因为池子
  抽干掉到 "~1.4s cold"（`:172`）。
- 冷启动：GKE 上"Pod cold-start 约 42–50s p50"（`:102`），75 claim 突发的整表 p50 都在
  41–55s（`:112-117`、`:137-146`）—— 因为 150 的池子接不住 75 并发，剩下的都是冷建。

**这两组数字要一起读**：92ms 的前提是"池子里有货，且这条 claim 不带任何会强制冷启的字段"；
42–50s 是"池子没覆盖到"的代价。池子不是优化项，它就是产品形态本身。

**什么会打穿池子**（这是本地源码里最硬的一条约束）：

```go
// extensions/controllers/sandboxclaim_controller.go:2224-2228
func claimForcesColdStart(claim *extensionsv1beta1.SandboxClaim) bool {
	return len(claim.Spec.Env) > 0 || len(claim.Spec.VolumeClaimTemplates) > 0
}
```

`spec.env` 和 `spec.volumeClaimTemplates` 任一非空就强制冷启（字段注释也写了：
`sandboxclaim_types.go:127-132`、`:134-135`；`docs/performance-tuning.md:23-31`）。
更要紧的是默认值那条：模板的 `envVarsInjectionPolicy` 默认是 `Disallowed`
（`extensions/api/v1beta1/sandboxtemplate_types.go:150`），**默认配置下带 `spec.env` 的 claim
不是"静默冷启"，而是被拒绝**（`:137-152`；执行点在
`extensions/controllers/sandboxclaim_controller.go:2004-2005`，错误是
`ErrEnvVarsInjectionRejected`）。`additionalPodMetadata`（label/annotation）不影响
adoption，是唯一能在 claim 上安全带的东西（`docs/performance-tuning.md:29-30`）。

---

## 2. 生命周期：create / delete / pause / resume

| 动作 | 它怎么做 | 依据 |
|---|---|---|
| create | 建 Sandbox（或 claim），控制器建 PVC/Pod/(Service) | `controllers/sandbox_controller.go:534-568` |
| pause | **`spec.operatingMode: Suspended`**：删 Pod，留 Sandbox 对象、PVC、Service | `:1736-1766` |
| resume | 改回 `Running`：按**当前** `podTemplate` 新建 Pod，重新挂回 PVC | `sandbox_types.go:347-352` |
| delete | 删 Sandbox 对象 → ownerRef 级联删 Pod/Service/PVC | `sandbox_types.go:289-295` |
| 到期 | `shutdownTime` + `shutdownPolicy`（默认 `Retain`） | `sandbox_types.go:374-396` |

几点必须说清楚：

1. **没有 `spec.replicas`。** Sandbox 的"0/1"是 `operatingMode` 枚举 `Running|Suspended`
   （`sandbox_types.go:230-250`、`:356-359`），默认 `Running`。`replicas` 只存在于
   `SandboxWarmPool`（`sandboxwarmpool_types.go:46`）。
2. **Pause 保留的是"对象 + 存储"，不是进程。** Pod 被删，内存态、终端会话、Chromium、
   正在跑的 agent 全丢。重新 Resume 起来的是同一个 Sandbox、同一块 PVC、**同一个名字**
   （Pod 名 = Sandbox 名，`:2012`），所以 Endpoint 和 key 都不用重新分配 —— 这是 Pause/Resume
   里最省事的一条。
3. **没有快照。** 全仓库没有 Sandbox 级的 snapshot 字段；仓库里出现的 `PodSnapshotPolicy`
   是 GKE 自己的 API 组 `podsnapshot.gke.io/v1`（`examples/podsnapshot-golden-warmpool/05-podsnapshotpolicy.yaml:1-20`），
   是"在 GKE 上给池子做 golden image"的示例，不是本项目的 CRD。
4. **能不能换节点 Resume：机制上可以，实测未核实。** 新 Pod 由调度器挑节点（控制器不写
   `nodeName`），数据在 PVC 里；但 `accessModes: ReadWriteOnce` 的卷跟着节点/可用区走，
   跨区能否挂上取决于 StorageClass，本轮**未核实**。
5. **工作区是 PVC，不是 emptyDir，也不是 bind mount。** 模板里写 `emptyDir` 当然也能跑，
   但那样 pause 就真的丢数据（Pod 删了 emptyDir 就没了）。我们的 bind-mount 假设在这里没有对应物。
6. **没有空闲自动暂停。** `roadmap.md:17`（Auto Suspend/Resume）和 `:49`（Scale to Zero）都还是
   `📅 Planned`。空闲判定仍然是我们的事。

对照我们自己的契约，这里有一处**语义冲突**，不是实现细节：

```
backend/internal/sandbox/sandbox.go:40-43
//   - Pause/Resume: Pause must capture a snapshot before releasing resources;
//     Resume restores it and returns State READY. With a memory snapshot the
//     PTY/tmux processes survive, which is what keeps `npm run dev` alive
//     across a pause (PRD §7.5).
```

agent-sandbox 的 Suspended **没有** snapshot 这一步，而且它给不了。接它 = 把这条契约改成
"Pause 只释放计算、不保进程"，和 `docs/SANDBOX-DEPLOY.md:670-687` 给 docker 写的那段妥协一样，
但这次是**产品契约级**的改动，不是 provider 内部实现。

---

## 3. 运行时：不要求 gVisor，默认就是 runc

**控制器完全不碰 `runtimeClassName`。** 在 `controllers/sandbox_controller.go` 全文里搜
`RuntimeClassName|SecurityContext|seccomp` 零命中；它只做 `podTemplate.Spec.DeepCopy()`
（`:1994`）再补 PVC 卷（`:1996-2009`）。所以 Pod 的运行时边界**完全由模板决定**，
不写就是集群默认的 runc。

官方口径也是这个：本项目是 *sandbox orchestrator*，把底层隔离委托给 Sandbox Runtime
（gVisor / Kata），自己不实现边界（`README.md:20`）。威胁模型里 gVisor 是 **Recommendation**
而不是 requirement（`docs/security/threat_model.md:64`）。

模板里 `runtimeClassName` 的写法在示例中是**注释掉的**：

```yaml
# examples/warmpool-quickstart/sandboxtemplate.yaml:9-10
      # This pod uses a non-root user and gVisor for runtime sandboxing.
      #runtimeClassName: gvisor
```

而 AIO 的示例只写了一条 `allowPrivilegeEscalation: false`：没有 `runtimeClassName`、没有
seccomp、没有 `runAsNonRoot`：

```yaml
# examples/aio-sandbox/aio-sandbox.yaml:10-22
        - name: aio-sandbox
          image: ghcr.io/agent-infra/sandbox:1.0.0.152
          securityContext:
            allowPrivilegeEscalation: false
          ports:
          - containerPort: 8080
          resources:
            limits: { memory: "4Gi", cpu: "2000m" }
```

**它"默认"给的安全配置只发生在模板路径上，而且只有两条**，都在
`extensions/controllers/utils.go`：

```go
// utils.go:28-31 — automountServiceAccountToken 未指定时置 false
// utils.go:42-47 — "secure by default" 模式下把 Pod DNS 改成 DNSNone + 8.8.8.8/1.1.1.1
```

第二条很关键，容易漏：`NetworkPolicyManagement` 为 `Managed`（默认）且模板没写
`networkPolicy` 时，控制器会把 Pod 的 `dnsPolicy` 改成 `None`、nameserver 换成公网 DNS
（`utils.go:33-47`）。**这样一来沙箱解析不了 `*.svc.cluster.local`**，也就解析不了自己的
Service、CoreDNS 后面的任何内部地址。

配套的默认 NetworkPolicy 更硬（`extensions/controllers/sandboxtemplate_controller.go:192-255`）：
入站只允许 `agent-sandbox-system` 里 `app=sandbox-router` 的 Pod（`:193-206`），出站只放行
公网，显式 `Except` 掉 `10/8`、`172.16/12`、`192.168/16`、`169.254/16`（`:230-252`），
注释里写得很直白：**连集群内 DNS 都是故意挡掉的**（`:228-229`）。

结论：**默认模板 = 只有 router 能进、只能出公网、解析不了内部名字。** 我们要让控制面
（`piforge` namespace，内网 IP）直连沙箱的 8080，就必须在模板里显式写 `networkPolicy.ingress`
或者 `networkPolicyManagement: Unmanaged`，并按 `NetworkPolicy` 自己补一条。这不是可选项，
是"接了它就必须同时决定网络拓扑"。

裸 `Sandbox`（不走模板）没有这些：没有 NetworkPolicy、没有 DNS 改写、`automountServiceAccountToken`
也不会被默认成 false —— 威胁模型里明说裸 CRD 要靠准入控制自己管
（`docs/security/threat_model.md:66`）。**安全默认值只属于模板路径**，这一点决定了
"只用 Sandbox 不用扩展"是一个陷阱。

---

## 4. 我们能用的接口

### 4.1 创建与寻址：只有 Kubernetes API，没有生命周期网关

项目没有"沙箱 REST API"。创建只有这几条路，本质都是写 CRD：

| 方式 | 证据 | 适合谁 |
|---|---|---|
| client-go / controller-runtime 直接写 CR | `api/v1beta1/sandbox_types.go:434-458`（`+genclient`） | 我们的控制面 |
| 生成的 typed clientset / informer | `clients/k8s/`（生成物，AGENTS.md 表格） | 需要 watch 的服务 |
| Go SDK（封装 claim 生命周期 + 三种连通方式） | `clients/go/README.md:8-40` | 想要现成封装 |
| Python / TypeScript SDK | `clients/python/...`、`clients/typescript/...`（AGENTS.md 表格） | 不是我们的路径 |

Go SDK 的三种连通模式值得记：Gateway（走 Gateway API 拿外部地址）、Port-Forward（SPDY）、
Direct URL（`clients/go/README.md:14-20`）。它默认打的是 `sandbox-router-svc`
（`sandbox-router/README.md:23`）。

### 4.2 数据面怎么到沙箱的 8080

三条路，各有前提：

1. **每沙箱 headless Service**：`spec.service: true` + 模板里写 `containerPort: 8080`。
   DNS 是 `status.serviceFQDN`（`sandbox_types.go:400-403`，域名由控制器 `--cluster-domain`
   决定，`docs/configuration.md:60`）。名字 = Sandbox 名，所以只有**我们自己是创建者**时才可预测；
   warm pool 的 Sandbox 是 `<pool>-<随机>`（`sandboxwarmpool_controller.go:1162`），claim 名不是它。
2. **Pod IP**：`status.podIPs`（`sandbox_types.go:420-426`）。控制面直连，最省事，但要求
   NetworkPolicy 放行，且 Pod 重启就换 IP —— 我们得每次重读。
3. **sandbox-router**：一个无状态反向代理，按 `X-Sandbox-ID` / `X-Sandbox-Namespace` /
   `X-Sandbox-Port`（默认 8888）转发（`sandbox-router/README.md:31-44`）。解析顺序是
   Pod-IP 头 → informer 缓存 → DNS（`:44-52`）。**它自己明确指出 warm pool 的 sandbox 没有
   per-sandbox Service，所以 DNS 那一步必然 NXDOMAIN**（`:43`），必须开 `--cache-enabled`
   才可达。

对我们最有用的是它的**路径式路由**（默认关闭）：`--path-routing-prefix` 打开后，
`<prefix>/<namespace>/<id>/<port>/...` 就能直接路由，不需要任何 header —— 这正是浏览器
`<iframe>` 和 `new WebSocket()` 能给的东西（`sandbox-router/README.md:151-170`），而且
WebSocket 升级和 `Origin` 剥离都处理了（`:140-149`）。我们现在浏览器/终端是控制面代持 key
再转发；这一层如果换成 router，路径式路由是最接近的替代。

认证侧它有三种 `--authz-mode`：`allow-all`（默认）、`tokenreview`、`scoped-token`。
scoped-token v2 是 Ed25519，claims 绑定 `ns/name/uid/port/method/path/exp`
（`sandbox-router/authz/scopedtoken_v2.go:35-43`）——**这就是"每沙箱 capability token"的现成形状**，
比我们现在的 `X-AIO-API-Key` 更细（能锁到 method/path/过期）。

一条内部不一致，顺手记下来（不影响结论）：router README 说"upstream 用 h2c"
（`sandbox-router/README.md:96`），但 HTTP 代理的 transport 明确 `ForceAttemptHTTP2: false`
且注释写"sandboxes are h1 today"（`sandbox-router/proxy/proxy.go:499-503`、`:520`）；
真正开 h2c 的是 gRPC 那条独立 transport（`sandbox-router/proxy/grpc.go:176-179`）。
所以 `packages/sandboxd/USER_GUIDE.md:24-27` 那句"router 不能承载 gRPC ProcessService"
是按旧实现写的，现在只剩下"默认端口不同"这一层区别。

### 4.3 exec / 终端：核心不管，它只是给了个参考运行时

- **核心 CRD 里没有任何 exec/terminal 概念。** 没有 `exec` 子资源、没有会话对象，
  能力边界就是"Pod 跑起来"。
- 它另外交付了一个 **`sandboxd`**（`packages/sandboxd/`）：沙箱内的守护进程，
  gRPC `:9090` 跑 `ProcessService`（流式进程 I/O，含 `ResizeTTY`），HTTP `:8080` 跑
  `FilesystemService`（`packages/sandboxd/USER_GUIDE.md:8-20`）。协议是 KEP-539.2 定义的
  "portable runtime"（`:10-11`），文档还列了实现一个兼容运行时需要满足的 5 条（`:47-80`）。
- **我们的 AIO 镜像不实现这套协议**，它有自己的 `/v1/shell/*`、`/v1/file/*`、浏览器接口
  （`backend/internal/aio/client.go`）。所以我们接 agent-sandbox 时，终端/浏览器**仍然是
  AIO 的 HTTP API**，只是 base URL 从 `http://piforge-<hash8>:8080` 换成
  `http://<sandbox>.<ns>.svc...:8080` 或 router 的路径 URL。sandboxd 是"另一条 runtime 路线"，
  换过去等于重写 `aio` 客户端和终端（行式 → gRPC 流式），不在本次评估范围内。

---

## 5. 多租户

| 维度 | 它有什么 | 依据 |
|---|---|---|
| 隔离单位 | namespace + 每沙箱一个 Pod；四个 CRD 都是 Namespaced | `sandbox_types.go:437`、`sandboxclaim_types.go:181`、`sandboxtemplate_types.go:163`、`sandboxwarmpool_types.go:120` |
| 跨 namespace 领养 | 明确禁止（`ErrCrossNamespaceAdoption`） | `extensions/controllers/sandboxclaim_controller.go:183` |
| 配额 | **没有**。仓库里没有 `ResourceQuota`/`LimitRange` 清单；威胁模型说这是管理员的事 | `docs/security/threat_model.md:67`（"System administrators should enforce LimitsRanges"） |
| 网络 | 模板级共享一条 NetworkPolicy（`Managed` 默认 / `Unmanaged` 交给 Cilium 等） | `sandboxtemplate_types.go:106-135`、`sandboxtemplate_controller.go:76-165` |
| 每沙箱凭据 | **没有**。API 里没有任何 Secret / token / key 字段 | `api/v1beta1/*.go`、`extensions/api/v1beta1/*.go` 全文无 `Secret` 命中 |
| 控制器权限 | cluster-scoped ClusterRole，能 create/delete 所有 namespace 的 Pod/PVC/Service | `k8s/rbac.generated.yaml:1-20`、`k8s/controller.yaml:1-30` |

"每沙箱凭据没有位置"需要展开，因为它直接决定 §8 的结论。它给的两个口子都不合用：

1. **模板里的 `env` + `secretKeyRef`**：这是"模板级共享"，池子里所有 Sandbox 拿到同一个 key。
   仓库自己的示例就是这么用的 —— `examples/pi-code-agent/sandbox.yaml:19-23` 把一个
   `ANTHROPIC_API_KEY` 从 Secret 注进 Pod。那是**单沙箱**示例，不是池子。
2. **claim 上的 `spec.env`**：能带 per-claim 的值，但强制冷启（§1.4），而且默认模板
   `envVarsInjectionPolicy: Disallowed` 会直接拒绝（`sandboxtemplate_types.go:150`）。

所以"warm pool 的毫秒级"和"每沙箱一个 key"在它的模型里是**互斥**的，除非 key 不放在沙箱里。
这条是我们评估里最贵的一处，比版本依赖贵。

---

## 6. 成熟度

比 `docs/SANDBOX-DEPLOY.md` §2.3 描述的"2025 年 KubeCon 新项目"要成熟得多，但也远没到能当
基础设施底座的程度。

**支持它的一面**

- Kubernetes SIG Apps 子项目，`kubernetes-sigs` 组织下，Prow CI + Tide 合并
  （`AGENTS.md:165-182`），Apache-2.0（`LICENSE:1-3`）。
- OWNERS 里 4 个 approver：`janetkuo`、`justinsb`、`soltysh`、`barney-s`（`OWNERS:1-7`）——
  都是 Kubernetes 里有长期记录的维护者。
- 测试量真的很大：本快照里 **170 个 `_test.go`**（`controllers/sandbox_controller_test.go`
  单文件 297KB），`test/e2e` 下 clients/extensions/framework 三个套件，另有
  `test/benchmarks`。
- 文档质量在同类项目里属于上乘：`docs/configuration.md`（全部 flag 的语义）、
  `docs/performance-tuning.md`（带 A/B 表和集群规格）、`docs/security/threat_model.md`、
  `docs/keps/` 下 8 个 KEP（含 suspend/resume、claim 字段互斥、多集群）。
- 交付形态齐全：kustomize（`k8s/`）、Helm（`helm/`，含 CI values）、OLM（`olm/`）、
  三种语言的 SDK、67 个 example 目录。
- 版本不是 0.x：README 指向 `v1.0.2`（`README.md:98`），且 `v1beta1` 是唯一 served 版本
  （`roadmap.md:83`）。

**不支持它的一面**

- **API 还是 beta。** `v1beta1` 意味着字段可以变；`docs/api-migration-guide.md` 的存在本身
  就说明刚经历过一次 `v1alpha1 → v1beta1` 的迁移。而且本快照里**没有**一份书面的 API 弃用 /
  支持窗口政策：`docs/` 全文搜 `deprecat|compat|API stability` 只命中字段注释
  （如 `api/v1beta1/sandbox_types.go:137-138`），没有承诺。
- **几个我们要用的能力还在路上**：claim 时带存储（`roadmap.md:66`）、claim 时挂网络策略
  （`:64`）、sandbox/pod 身份绑定（`:62`）、自动挂起/恢复（`:17`）、claim 时延目标
  "200ms → 100ms → 50ms"（`:47`）。
- **文档漂移已经出现**：router 的 h2c 说法与代码相反（§4.2）；`packages/sandboxd/USER_GUIDE.md`
  对 router 的判断也已过时。
- **一个 cluster-scoped 控制器**要我们运维：Deployment 1 副本 + leader election
  （`k8s/controller.yaml:52-74`），能删任何 namespace 的 Pod/PVC/Service（`k8s/rbac.generated.yaml`）。
  它停更、或有 CVE、或 CRD 升级要迁移，都是我们的值班。
- 和 PiForge 的**发布节奏**不同步：它按 Kubernetes SIG 的节拍走（stale PR 15 天自动关、
  CLA、CodeRabbit 首轮），我们按产品迭代走。

---

## 7. 对 PiForge 的适配度

### 7.1 四条路的横向对比

| | (a) 自己写 Pod+PVC+Service | (b) 现在的 docker + runner | (c) Genet 的形状 | (d) agent-sandbox |
|---|---|---|---|---|
| 隔离边界 | 我们选的 RuntimeClass | 容器（runc） | 容器 + hostNetwork | 我们选的 RuntimeClass（它不提供） |
| 预热 | 自己写 | 不需要（容器秒起） | 无 | **有，且是唯一真差异** |
| 生命周期样板 | 全写 | `docker run/stop/start` | Pod + ConfigMap 状态 | CRD + 控制器代写 |
| 工作区 | PVC（我们定） | bind mount | **不是 PVC**（NFS 卷 + commit 出来的本地镜像） | PVC（模板定） |
| 每沙箱密钥 | 我们定 | 已有（`api_key_cipher`） | 部署级共享（缺陷） | **没有位置** |
| Pause 语义 | "删 Pod 保 PVC" | `docker stop`（留文件系统，杀进程） | commit 成镜像再删 Pod | 删 Pod 保 PVC |
| 运维成本 | 只有我们的控制面 | 一个 runner 容器 | 无 | 一个 cluster-scoped 控制器 |
| 依赖风险 | 无 | Docker | 无 | CRD + beta API + 上游节奏 |

`docs/SANDBOX-GENET.md` 的结论在这里仍然成立：Genet 的"暂停"是 **commit 成镜像再删 Pod**
（`docs/SANDBOX-GENET.md:9-11`），那是另一种机制，我们只能借顺序。agent-sandbox 的"暂停"是
**删 Pod 保 PVC**，比 Genet 诚实，但同样没有内存态 —— 两者的差别是"要不要在暂停前固化工作区"，
而 PiForge 的契约要求固化（`backend/internal/sandbox/sandbox.go:37-43`）。

### 7.2 我们得到什么

1. **warm pool**：92ms 级的 claim，且它是"池子 + 引用计数 + 补充策略"的完整实现
   （含 `max-refill-rate`、expectations gate、per-pool 串行化）。自己写要几个月。
2. **调度与容量**：Pod 交给 kube-scheduler，池子交给 HPA（`sandboxwarmpool_types.go:41-42`
   明确说 `replicas` 由 HPA 控制，`examples/hpa-swp-scaling/`）。
3. **生命周期样板**：PVC 命名/ownerRef/GC、headless Service 端口镜像、到期清理、
   `Ready/Suspended/PodScheduled/Finished` 四组 condition 的语义（`sandbox_types.go:45-135`）。
   这些正是 `docs/SANDBOX-DEPLOY.md:754-759` 里"第三件要先做的事"要写的代码。
4. **可观测性**：控制器自带 metrics（`internal/metrics/`，含 warm pool collector），
   claim 的启动延迟指标现成。
5. **node 级失败的对账**：`PodScheduled` condition 会被镜像到 Sandbox 上
   （`sandbox_types.go:109-122`），`Get` 不用自己读 Pod 就能解释"为什么没起来"。

### 7.3 我们承担什么

1. **CRD 依赖 + 一个 cluster-scoped 控制器**，且是 beta API。它挂了，所有沙箱都停。
2. **每沙箱密钥没有位置**：要么放弃池子（带 `spec.env` 冷启 40s），要么把认证上移到 router
   的 scoped token，要么池子按租户切分（成本爆炸）。这是产品级决策，不是代码级。
3. **Pause 契约改写**：从"先快照再释放"降级成"释放计算、保住工作区"。
4. **网络默认值是反的**：默认只有 router 能进、DNS 指向公网（§3）。我们要显式写策略，
   否则控制面根本连不上。
5. **配额仍然要自己加**：`ResourceQuota`/`LimitRange` 它不管（§5），fork bomb 的防线还是我们的。
6. **`RootDir` 假设继续不成立**：我们的 `provision`/`materialize` 仍然要改走沙箱内客户端
   （`docs/SANDBOX-DEPLOY.md:702-714`），这一条换任何 K8s 方案都一样。

---

## 8. 如果真接：六个动词怎么落（外加两个旁路接口）

前提：`contracts.SandboxProvider` 不动签名（`contracts/contracts.go:552-560`），
`Remote`/`WorktreeSandbox` 也不动（`backend/internal/sandbox/runner.go:56-59`、
`contracts/contracts.go:575-580`）。下面是逐条映射与成本（S ≈ 一两天，M ≈ 一周，
L ≈ 需要产品决策或多周）。

| 动词 | 落到哪 | 关键细节 | 成本 |
|---|---|---|---|
| `Ensure` | 建 `Sandbox`（每个 `(user, project)` 一个，名字沿用 `piforge-<hash8>`）或 `SandboxClaim`（走池子） | 等 `Ready=True` 且 `status.podIPs` 非空；checkout 仍然走 `aio.Client.Exec`（`sandbox/docker.go` 已有），不新增逻辑 | **M** |
| `Get` | 读 `status.conditions[Ready]` + `operatingMode` | `PAUSED` = 对象在但 `operatingMode=Suspended`；`MISSING` = 对象 404；`ERROR` = `Ready=False` 且 reason 是 `ReconcilerError`/`InvalidConfiguration`（`sandbox_types.go:88-104`） | **S** |
| `Pause` | patch `operatingMode: Suspended`，等 `Suspended=True` | 只删 Pod；PVC/Service 在。**丢了内存态**，`sandbox.go:40-43` 的契约要改 | **S**（代码）/ **L**（契约） |
| `Resume` | patch `operatingMode: Running`，poll 到 Ready | 冷启 40s 量级（`performance-tuning.md:102`），调用方 `provision.go` 要能等；现有 `runnerEnsureTimeout`（15min）够用 | **S** |
| `Destroy` | 删 `Sandbox`（或删 claim） | ownerRef 会把 PVC 一起删 —— 与我们"Destroy 不删数据卷"（`docs/SANDBOX.md:116`）冲突。要保数据就得先 `patch` 掉 PVC 的 ownerRef，或改用 `shutdownPolicy: Retain` | **M** |
| `Exec` | 无对应物 | 要么 `pods/exec`（client-go remotecommand，无 PTY、每命令一次往返），要么复用 AIO 的 `/v1/shell/exec`。**保持"调试接口"定位**（`docs/SANDBOX-DEPLOY.md:689-700`） | **S** |
| `AddWorktree` | 不变：走 `aio.Client.Exec` 在沙箱里 `git worktree add` | 只是 base URL 变了 | **S** |
| `Endpoint` | `http://<sandbox>.<ns>.svc.<cluster-domain>:8080`（需 `spec.service: true` + `containerPort: 8080`）或 router 路径 URL | 后者要 `--cache-enabled`，否则 warm 沙箱 NXDOMAIN（§4.2） | **S**（Service）/ **M**（router） |

**per-sandbox key 放哪** —— 三个选项，按推荐度：

1. **key 留在沙箱里，但池子不共享它**：每个 `(user, project)` 建**独立** Sandbox（带自己的
   `secretKeyRef`），不用池子。代价：40s 冷启，收益：现有不变量全部不变。**这是唯一不改产品
   语义的做法，也是"先不接"的理由。**
2. **认证上移到 router**：池子共享沙箱 key（或干脆不设），控制面为每个沙箱签一张 scoped token
   （Ed25519，绑定 ns/name/uid/port/method/path/exp，`scopedtoken_v2.go:35-43`），NetworkPolicy
   只放行 router。收益：拿到 92ms；代价：`docs/SANDBOX.md:44-69` 那套"每沙箱一个 key、
   A 的 key 打 B 是 401"要重写成"A 的 token 打 B 被 router 403"，且要自己管签名密钥轮换。
3. **池子按租户切分**：`SandboxWarmPool` 每用户一个。配额与成本线性增长，只在"大客户专池"
   场景成立。

**终端/浏览器路径**不变：还是 AIO 的 `/v1/shell/*`、`/v1/file/*`、浏览器截图，
只是 base URL 从容器名换成 Service DNS 或 router URL。`X-AIO-API-Key` 由控制面继续代持
（key 在沙箱里时），只有选方案 2 才换成 `Authorization` + scoped token。

**六动词之和**：代码量大概 M～L（`Ensure`/`Destroy`/`Endpoint` 是主要工作），
但真正的成本在三个决策上（key 放哪、Pause 契约、谁来保证配额），这三个都不是写代码能解决的。

---

## 9. 推荐：**pilot**，不是 adopt，也不是 ignore

**最强的一条理由（为什么值得试）**：warm pool 是它唯一真正独有的东西，而且它是真的 ——
池子、adoption、补充策略、per-pool 串行化、expectations gate 都有实现和压测数据
（`docs/performance-tuning.md:42-88`、`:168-178`）。我们自己写 Pod+PVC+Service 拿不到 92ms，
只能拿到 40s，而"新建会话等 40 秒"是产品问题，不是工程问题。

**最强的一条风险（为什么不能直接 adopt）**：**它没有"每沙箱凭据"的位置，而拿池子就必须放弃
per-sandbox key。** `spec.env` 强制冷启（`sandboxclaim_controller.go:2226-2228`）且默认被模板
拒绝（`sandboxtemplate_types.go:150`）；模板 `env` 是池内共享。也就是说，
**92ms 和"一个 key 只开一个租户"在它的模型里无法同时成立**，除非把认证搬到 router
（方案 2）—— 那是一次产品级改动，必须先被明确接受，不能顺手做。

**最小的决定性实验**（一个下午，kind 上就能做完；四件事各自是否定的也要记录）：

```sh
# k8s/controller.yaml 的镜像是 ko:// 占位符，直接 apply 起不来；
# 上游给的两条可用路径是 make deploy-kind 或 release 里的 sandbox-with-extensions.yaml
EXTENSIONS=true make deploy-kind      # Makefile:109-128，AGENTS.md 的 `make deploy-kind`
export KUBECONFIG=bin/KUBECONFIG
kubectl get crd sandboxes.agents.x-k8s.io   # 核心；extensions 的三个也应在
```

1. **建一个、连上、量一次冷启**：用 `examples/aio-sandbox/aio-sandbox.yaml`（镜像换成本地
   已缓存的 AIO；它已经写了 `containerPort: 8080`，只需补 `spec.service: true`），
   `kubectl wait sandbox/aio-sandbox-example --for=condition=Ready`，记下耗时，
   然后从一个**普通 Pod**（不是 router）`curl http://aio-sandbox-example:8080/v1/health`。
   → 验证"控制面能不能直连"以及冷启到底是不是 40s。
2. **Pause/Resume 保不保数据**：往 `/workspace` 写文件 → `operatingMode: Suspended` →
   确认 Pod 没了、PVC 还在 → 改回 `Running` → 文件还在不在、Pod 名变没变。
   → 验证 §2 的三条。
3. **池子 + claim，并确认 key 放不进去**：`examples/warmpool-quickstart/` 起模板 + 池子 + claim，
   量 claim→Ready；再给 claim 加 `spec.env`，看它是被拒（默认模板）还是冷启。
   → 这一步同时验证 92ms 能不能复现，以及**方案 1 和方案 2 的岔路口**。
4. **默认模板的网络是不是真的挡控制面**：用模板建出来的沙箱，从非 router 的 Pod 打 8080，
   应当超时；再从沙箱里 `getent hosts kubernetes.default`，应当失败（DNS 被改成公网）。
   → 验证 §3 的两条默认值。

四项都过（或都有明确替代方案）才谈 adopt；只要第 3 项证明"key 必须搬去 router"，
那就先做"方案 1 + 自写 provider"，把池子留到有第二个租户需要秒开时再回来。

**不该做的事**：不要因为"它替我们写了 Pod/PVC/Service"就接 —— 那部分代码我们本来就要写，
而且写了才知道 `RootDir`、worktree、快照这几处怎么改；把一个 beta CRD 放进唯一的沙箱创建
路径，换来的只是几百行样板，风险却是"上游改了字段、我们的控制面起不来"。

---

## 10. 与 `docs/SANDBOX-DEPLOY.md` 不一致的地方

那份文档是按检索摘要写的，并自己标了"未核实"。本文按本地源码逐条核对，下面是**被推翻**或
**需要改写**的条目（只列，不改那份文档）：

| # | 那份文档说 | 本地源码是什么 |
|---|---|---|
| 1 | §2.3 表格：`Sandbox` 的 `spec.replicas`(0/1) | **没有这个字段**。是 `spec.operatingMode: Running\|Suspended`（`sandbox_types.go:347-359`）；`replicas` 只在 `SandboxWarmPool`（`sandboxwarmpool_types.go:46`） |
| 2 | §2.3：`spec.replicas: 0` 会删 Pod、保留 Service 和 PVC | 机制对，字段名错（同上）。删 Pod 的是 `operatingMode: Suspended`（`sandbox_controller.go:1736-1766`），Service/PVC 确实保留 |
| 3 | §5 表格 `Pause`：**删 Pod，保 PVC（= agent-sandbox 的 `replicas: 0`）** | 同上，字段名错，语义对 |
| 4 | §2.3：控制器建 Pod + headless Service + PVC | **Service 只在 `spec.service: true` 时建**，默认不建（`sandbox_types.go:306-313`、`sandbox_controller.go:1467`）；端口来自模板的 `containerPort`（`:1358-1406`） |
| 5 | §2.3：`shutdownTime` 到期删 Pod/Service，PVC 保留供恢复 | 只在 `shutdownPolicy: Retain`（默认）下成立；选 `Delete` 时 Sandbox 对象被删，PVC 随 ownerRef 一起 GC（`sandbox_types.go:289-295`、`:384-395`） |
| 6 | §2.3 / §8：API 版本 `v1alpha1` 还是 `v1beta1`，**未核实** | **已核实为 `v1beta1`，且是唯一 served 版本**；v1alpha1 类型与 conversion webhook 已删除（`api/v1beta1/groupversion_info.go:35`、`roadmap.md:83`） |
| 7 | §2.3：读起来像"2025 年 KubeCon 启动的新项目" | 它已经有 `v1.0.2` release（`README.md:98`）、`v1beta1` served version、4 个 Kubernetes approver、170 个测试文件、Helm/OLM/三语言 SDK |
| 8 | §2.3：`SandboxClaim` 带 `spec.env` 会**绕过池子冷启**，"这条会静默失效" | 冷启是真的（`sandboxclaim_controller.go:2224-2228`），但模板的 `envVarsInjectionPolicy` **默认 `Disallowed`，带 env 的 claim 直接被拒**（`sandboxtemplate_types.go:150`、`:137-152`）。不是静默，是显式报错 |
| 9 | §2.3：`sandbox-router` 按沙箱 id 转发到 Pod IP，"省掉每个沙箱一个 Service" | router 是纯数据面；它还支持路径式路由（`--path-routing-prefix`）与三种 authz（`sandbox-router/README.md:151-170`、`:244-260`）。另外 warm pool 的沙箱**本来就没有 per-sandbox Service**，DNS 那一步 NXDOMAIN，必须开 `--cache-enabled`（`sandbox-router/README.md:43`） |
| 10 | §2.3：隔离委托给 RuntimeClass，**没提默认值** | 补充：控制器不注入 `runtimeClassName`，默认 runc（§3）；而模板路径会**默认关掉 SA token、把 DNS 改成 8.8.8.8、只让 router 入站**（`utils.go:28-47`、`sandboxtemplate_controller.go:192-255`）——这三条会直接决定我们连不连得上 |
| 11 | §2.3：冷启动 ~42–50s p50，warm adoption p50 92ms（转引 briefing） | **数字本身对**，本地源码可查（`performance-tuning.md:102`、`:168-178`）。但 92ms 的前提是 `replenish-delay=0` + `max-refill-rate=100` + 池子已 Ready；池子不够时同一份基准是 41–55s（`:112-117`） |
| 12 | §5 表格 `Resume`：重建 Pod + 等 Ready（~40s 冷启动） | 对；但要补一句：**Pod 名不变、PVC 不变、key 不变**，所以 Endpoint 不需要重新分配（`sandbox_controller.go:2012`） |

---

## 11. 未核实

- **kind 上 8080 到底通不通、冷启到底多少秒**：本轮没有集群，全部是源码阅读。上游给的是
  GKE/kops 数字，不是我们的镜像、不是我们的 StorageClass。
- **`ReadWriteOnce` PVC 跨节点 Resume**：原理上由调度器与 CSI 决定，控制器不 pin 节点
  （`sandbox_controller.go:1994-2018` 不写 `nodeName`），但跨可用区能否挂上**未实测**。
- **claim 在"池子刚好抽干"时的行为**：`--sandbox-claim-warm-candidate-grace-period` 默认 2s
  （`docs/configuration.md:14`），超时就冷建；我们这种"用户点开就等"的场景是否可接受**未测**。
- **92ms 在单池、低并发下是否还成立**：上游那张表是 45 claim/s、4 个池（`performance-tuning.md:168`），
  不是我们"一个用户开一个会话"的形态。
- **把 AIO 镜像放进池子是否可行**：`docs/SANDBOX-DEPLOY.md:789-790` 记录过 AIO 的 entrypoint
  要 `useradd`、`runAsNonRoot` 下会挂；那一条是 PiForge 自己的结论，本轮没有在 K8s 池子里复现，
  **未核实**。
- **`agent-sandbox-config` ConfigMap 的 label-domain allowlist 与我们的 label 是否冲突**：
  只读到描述（`docs/configuration.md:319-332`），没有逐行读实现。
- **它和我们的 `sandbox.api_key_cipher` 存储路径能否共存**：没有验证过 store 层，纯设计推演。
- **release tag 的完整列表**：这份快照没有 `.git`，我只能读到 README 举例的 `v1.0.2`
  （`README.md:98`、`:179`）和 chart 的 `0.1.4`；发过几个版本、有没有 `v0.x` 到 `v1.0` 的
  破坏性变更，**未核实**。
- **上游 release 节奏与 EOL 政策**：`docs/release.md` 只写了镜像提升流程，没有支持窗口承诺。

---

## 12. 实际读过的东西

agent-sandbox（路径相对仓库根）：

- `README.md`（全文）— 定位、scope note、安装、Router 的可选性
- `AGENTS.md`（项目结构、CI、约定）、`OWNERS`、`LICENSE`、`go.mod`、`helm/Chart.yaml`
- `api/v1beta1/sandbox_types.go`（全文，474 行）— 全部字段、condition 语义、label 常量
- `api/v1beta1/groupversion_info.go`、`extensions/api/v1beta1/groupversion_info.go`
- `extensions/api/v1beta1/sandboxclaim_types.go`（全文）、`sandboxwarmpool_types.go`（全文）、
  `sandboxtemplate_types.go:40-159`
- `controllers/sandbox_controller.go`：`507-576`（reconcile 顺序）、`1358-1574`（Service/PVC 与端口）、
  `1666-1767`（Suspend 删 Pod）、`1960-2052`（Pod 构造）
- `extensions/controllers/sandboxclaim_controller.go`：`1377-1446`（adoption）、`2224-2283`（冷启判定）
- `extensions/controllers/sandboxwarmpool_controller.go`：`1128-1207`（buildSandboxCR、GenerateName、evict）
- `extensions/controllers/sandboxtemplate_controller.go`：`76-165`、`191-256`（NetworkPolicy）
- `extensions/controllers/utils.go`（全文，53 行）— secure defaults 与 DNS 改写
- `docs/configuration.md`（flag 参考、`:288-332` claim 身份与 ConfigMap）、
  `docs/performance-tuning.md:1-200`、`docs/security/threat_model.md:1-100`、`docs/release.md`
- `roadmap.md`、`k8s/controller.yaml`、`k8s/rbac.generated.yaml`
- `examples/aio-sandbox/aio-sandbox.yaml`、`examples/pi-code-agent/sandbox.yaml`、
  `examples/warmpool-quickstart/*`、`examples/claimed-sandbox-service/{service,claims,template}.yaml`、
  `examples/podsnapshot-golden-warmpool/05-podsnapshotpolicy.yaml`、`examples/` 目录清单
- `sandbox-router/README.md:1-200`、`sandbox-router/proxy/proxy.go:495-539`、
  `sandbox-router/proxy/grpc.go:170-185`、`sandbox-router/authz/scopedtoken_v2.go:1-70`
- `packages/sandboxd/USER_GUIDE.md:1-110`、`clients/go/README.md:1-120`

PiForge（路径相对仓库根）：

- `docs/SANDBOX.md`（全文）、`docs/SANDBOX-DEPLOY.md:296-440`、`:640-837`、`docs/SANDBOX-GENET.md:1-60`、`:441-511`
- `backend/internal/contracts/contracts.go:518-580`
- `backend/internal/sandbox/sandbox.go:24-73`、`backend/internal/sandbox/runner.go:40-130`、`:438-511`
- `backend/internal/sandboxrunner/server.go:1-80`
- `backend/internal/aio/client.go:1-90` 与 function 索引
- `scripts/verify-*.ps1`、`deploy/` 目录清单（确认当前没有 K8s provider）
