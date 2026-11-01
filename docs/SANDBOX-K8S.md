# 沙箱：Kubernetes provider（`-sandbox k8s`）

`docs/SANDBOX.md` 讲的是"沙箱是什么、三种功能放在哪"，`docs/SANDBOX-DEPLOY.md`
讲的是 docker-compose 与 runner。本文回答第三种交付形状：**沙箱是集群里的一个 Pod，
工作区是它自己的 PVC，控制面只通过 Kubernetes API 和沙箱自己的 HTTP API 说话。**

设计依据是 `docs/SANDBOX-K8S-AGENTSANDBOX.md`。那份文档评估的是
`kubernetes-sigs/agent-sandbox`，结论是 §9 的 **pilot，不是 adopt**、§7.1 的
**(a) 自己写 Pod+PVC+Service**。本文实现的正是 (a)：不引入 CRD、不引入
cluster-scoped 控制器，`Ensure` 直接写 Pod、PVC、Service、Secret。

```
                    ┌──────────────────────────── piforge namespace ────────────────────────────┐
  piforged ──API──▶ │  Pod      piforge-<hash8>          （runtimeClassName: gvisor）          │
  (控制面)          │  PVC      piforge-ws-<hash8>       （唯一挂载者是这个 Pod）              │
      │             │  Service  piforge-<hash8>          （headless，DNS 名字稳定）              │
      │  HTTP/8080  │  Secret   piforge-key-<hash8>      （每沙箱一个 API key，pause 也不丢）      │
      └────────────▶└──────────────────────────────────────────────────────────────────────────┘
                    一个 (user, project) 一套；hash8 = sha256(user + "/" + project) 前 8 字节
```

四个对象、四件事，一个不多：Pod 是运行时，PVC 是工作区，Service 是名字，
Secret 是那个沙箱的凭据。控制面**从不挂载**这个 PVC——`RootDir` 是 `/workspace`，
沙箱记录里 `workspace=api`，文件面板、技能投影、git 全部走沙箱的
`/v1/file/*`、`/v1/shell/exec`（`docs/EXECUTION-BOUNDARY.md` stage 2）。

---

## 1. 落地：先 apply 什么

四个清单，按顺序。它们不是"可选的美化"，每一条都对应启动 preflight 检查的一件事实。

| 文件 | 内容 | 不 apply 会怎样 |
|---|---|---|
| `deploy/k8s/00-namespace.yaml` | namespace `piforge`、ResourceQuota、LimitRange、`pod-security.kubernetes.io/enforce: restricted` | preflight 拒绝启动：`namespace "piforge" does not exist` |
| `deploy/k8s/10-serviceaccount-rbac.yaml` | 两个 ServiceAccount（控制面用的 `piforge-control-plane`、沙箱用的 `piforge-sandbox`）+ 一个 Role + 一个 ClusterRole | preflight 拒绝启动，逐条列出缺哪个 `verb "resource"` |
| `deploy/k8s/20-control-plane.yaml` | 参考清单：控制面自己的 Deployment（不 apply 也行，但那段 env 是权威的） | 控制面起不来或没有权限 |
| `deploy/k8s/30-runtimeclass.yaml` | RuntimeClass `gvisor`（`handler: runsc`） | preflight 拒绝启动，并说明后果 |
| `deploy/k8s/40-networkpolicy.yaml` | 默认拒绝 + 三条放行（控制面入站、DNS、公网） | 能跑，但 egress 没有任何边界（§5） |

```sh
kubectl apply -f deploy/k8s/00-namespace.yaml
kubectl apply -f deploy/k8s/10-serviceaccount-rbac.yaml
kubectl apply -f deploy/k8s/30-runtimeclass.yaml
kubectl apply -f deploy/k8s/40-networkpolicy.yaml
```

`deploy/k8s/50-workspace-pvc-example.yaml` 是**模板**，默认不用 apply：PVC 由
provider 在第一次 `Ensure` 时创建。它存在是为了三种"卷必须先于沙箱存在"的场景
（从 VolumeSnapshot 恢复、集群禁止动态供给、按项目钉死 StorageClass），文件里写了
adopt 需要的名字与 `app.kubernetes.io/managed-by: piforge` 标签。

**gVisor 不是在集群里 `kubectl apply` 出来的**：`runsc` 必须在节点镜像里，并在
kubelet 的 containerd 配置里注册成 runtime handler（`30-runtimeclass.yaml` 顶部写了
那段配置和 GKE/EKS 的做法）。RuntimeClass 对象只是"允许 pod 请求它"。

---

## 2. 启动：确切的 flag 与环境变量

最小启动（控制面跑在集群里，用 service account）：

```sh
piforged \
  -sandbox k8s \
  -sandbox-image ghcr.example/piforge/sandbox-agent:dev \
  -sandbox-k8s-namespace piforge \
  -sandbox-k8s-runtime-class gvisor
```

控制面在集群外（笔记本对着 kind、CI 对着远程集群）：

```sh
piforged \
  -sandbox k8s \
  -sandbox-image ghcr.example/piforge/sandbox-agent:dev \
  -sandbox-k8s-kubeconfig ~/.kube/config \
  -sandbox-k8s-context kind-piforge \
  -sandbox-k8s-namespace piforge \
  -sandbox-k8s-endpoint 'http://127.0.0.1:{sandbox}-fw:8080'
```

`{sandbox}` 会被替换成沙箱名（`piforge-<hash8>`）。这个 flag 存在的原因是：默认
endpoint 是 headless Service 的集群 DNS 名字，只有控制面**在集群里**才解析得到；在
集群外必须自己提供一条路（`kubectl port-forward` 一条、ingress 一条，都行）。

| flag | 环境变量 | 默认 | 作用 |
|---|---|---|---|
| `-sandbox k8s` | `PIFORGE_SANDBOX=k8s` | 无（必须显式） | 选 provider |
| `-sandbox-image` | `PIFORGE_SANDBOX_IMAGE` | 无（必须） | 沙箱镜像，与 docker/runner 同一个 |
| `-sandbox-k8s-kubeconfig` | `PIFORGE_SANDBOX_K8S_KUBECONFIG` | 空 | 空 = in-cluster SA → `$KUBECONFIG` → `~/.kube/config`（client-go 的加载顺序） |
| `-sandbox-k8s-context` | `PIFORGE_SANDBOX_K8S_CONTEXT` | 空 | 空 = 当前 context。两个集群都在 kubeconfig 里时，这是唯一能确保打对地方的办法 |
| `-sandbox-k8s-namespace` | `PIFORGE_SANDBOX_K8S_NAMESPACE` | `piforge` | 对象所在 namespace；**不创建**，不存在就拒绝启动 |
| `-sandbox-k8s-runtime-class` | `PIFORGE_SANDBOX_K8S_RUNTIME_CLASS` | `gvisor` | pod 的 `runtimeClassName` |
| `-sandbox-k8s-allow-runc` | `PIFORGE_SANDBOX_K8S_ALLOW_RUNC=1` | false | 接受"没有这个 RuntimeClass"，并允许 `runtimeClassName` 为空；后果写在 §4 |
| `-sandbox-k8s-storage-class` | `PIFORGE_SANDBOX_K8S_STORAGE_CLASS` | 空 = 集群默认 | PVC 的 StorageClass |
| `-sandbox-k8s-disk` | `PIFORGE_SANDBOX_K8S_DISK` | `10Gi` | PVC 的 `requests.storage`（Kubernetes quantity） |
| `-sandbox-k8s-service-account` | `PIFORGE_SANDBOX_K8S_SERVICE_ACCOUNT` | 空 = default | pod 的 SA；无论哪个，token 都不挂载 |
| `-sandbox-k8s-run-as-user` | `PIFORGE_SANDBOX_K8S_RUN_AS_USER` | `1000` | pod 的 uid/gid，同时是 PVC 的 `fsGroup` |
| `-sandbox-k8s-allow-root-user` | `PIFORGE_SANDBOX_K8S_ALLOW_ROOT_USER=1` | false | 省略 `runAsNonRoot`（镜像 entrypoint 需要 root 做 bootstrap 时用，见 §4） |
| `-sandbox-k8s-read-only-rootfs` | `PIFORGE_SANDBOX_K8S_READ_ONLY_ROOTFS=0` | true | 容器根文件系统只读 |
| `-sandbox-k8s-writable-dirs` | `PIFORGE_SANDBOX_K8S_WRITABLE_DIRS` | `/tmp,/run` | 只读根文件系统下挂 emptyDir 的路径 |
| `-sandbox-k8s-cluster-domain` | `PIFORGE_SANDBOX_K8S_CLUSTER_DOMAIN` | `cluster.local` | 拼 Service FQDN 用 |
| `-sandbox-k8s-endpoint` | `PIFORGE_SANDBOX_K8S_ENDPOINT` | 空 = Service DNS | 控制面在集群外时的地址模板 |
| `-sandbox-k8s-grace-stop` | `PIFORGE_SANDBOX_K8S_GRACE_STOP` | `30s` | Pause/Destroy 给 pod 的停止时间 |

**这些 flag 属于 `-sandbox k8s`**：在 `local`/`docker`/`runner` 下写任何一个都会被
拒绝启动，并指出 flag、环境变量和当前 provider（和 runner 凭据是同一条规则的反向）。
理由很直白：一行"看起来配置好了"的命令行，比没有这行更麻烦。

### preflight：四件必须先成立的事

启动时（`newK8sSandbox`，30 秒上限）向集群要四个答案，**任何一个不成立就退出**，
一次把所有问题列出来：

1. **API server 可达**——kubeconfig/context/service account 对不对；
2. **namespace 存在**——不存在时说 `apply deploy/k8s/00-namespace.yaml`；
3. **RuntimeClass 存在**——不存在时说的不是"资源缺失"，而是后果（§4）；
4. **RBAC 够用**——对每个 `(verb, resource)` 发一次 `SelfSubjectAccessReview`
   （`internal/sandbox/k8s_preflight.go` 的 `k8sRequiredAccess` 是完整清单：pods 的
   create/get/delete、**pods/exec 的 create**、PVC 的 create/get/update/delete、
   Service 的 create/get/delete、Secret 的 create/get/delete，外加 cluster-scoped 的
   `get namespaces`、`get runtimeclasses`、`create selfsubjectaccessreviews`）。
   缺哪个就写哪个，并指向 `deploy/k8s/10-serviceaccount-rbac.yaml`。

最关键的一条是 `pods/exec`：它是 agent 的字节通道（Attach/Reattach），只给 `pods`
不够。这个漏配在运行时表现为"agent 起不来"，而不是权限错误——preflight 把它提前到
启动。

---

## 3. 六个动词，逐条

| 动词 | 做了什么 | 关键点 |
|---|---|---|
| `Ensure` | `PVC → Service → Secret → Pod`，等 `Ready`，再通过沙箱自己的 `/v1/shell/exec` 建 checkout | 幂等（名字来自 pair hash）；已有的 pod 若没有正确的 API key 引用，**替换**而不是收养（与 `Docker.Ensure` 同一条规则） |
| `Get` | 读 pod 的 phase/Ready | 四种答案：pod 在 → READY/STARTING/PAUSED/ERROR；pod 不在但 PVC 带 `piforge.io/paused=true` → **PAUSED**；pod 不在且没有这个标记 → **MISSING**（被驱逐/被删，不是我们暂停的）；连 PVC 都没有 → `ErrNotFound` |
| `Pause` | 带 grace 删 pod，保留 PVC / Service / Secret，并给 PVC 打 paused 标记 | **丢进程**：PTY、tmux、`npm run dev` 全没（§6）。数据在 |
| `Resume` | 清标记、重建 pod、等 Ready、等沙箱 API 再答一次 | 返回 READY 才算成功；API key 不变（在 Secret 里） |
| `Destroy` | pod → service → PVC → secret | 先 pod 再 PVC（挂载中的 PVC 删不掉）；任何一个对象不带 `app.kubernetes.io/managed-by: piforge` 就拒绝删——名字是哈希，哈希可能解析到别的集群 |
| `Exec` | `pods/exec`（runtime 自己的 exec 原语），argv 直接执行 | 不是 shell 字符串；`dir` 用 `sh -c 'cd -- "$0" && exec "$@"'` 传位置参数，没有拼接；cwd 是 `/workspace` 时可以省掉这层 |
| `Attach` / `Reattach` | 同一条 `pods/exec` 流的 `io.ReadWriteCloser`，带 `CloseWrite` | agent 的 stdin/stdout；ctx 只约束"建立"，流本身不被请求的取消打断（`context.WithoutCancel`） |
| `AddWorktree` | 通过沙箱 API 跑 `git worktree add`（`contracts.WorktreeSandbox`） | 与 docker 共用 `internal/sandbox/checkout.go` 的同一份计划与执行 |
| `Endpoint` | `http://<name>.<ns>.svc.<cluster-domain>:8080`，或 `-sandbox-k8s-endpoint` 的模板 | 先 GET Service：没有 Service 就是 `ErrNotFound`，不是一个稍后才失败的域名 |
| `ContainerPath` | `/workspace` 下的路径恒等映射，其它一律报错 | 这个 provider 的进程**永远不挂载**工作区，所以"宿主路径"只可能指向别的树 |

### 为什么"暂停"是这个语义

`docs/SANDBOX-K8S-AGENTSANDBOX.md` §2 已经把这条说清楚了：删 pod 保 PVC 没有内存态，
而 `backend/internal/sandbox/sandbox.go:40-43` 的契约要求 Pause 先快照。Kubernetes 这
一层拿不到内存快照（CRIU 需要节点特权，gVisor 下还要另说），所以本 provider **明确**
把 Pause 定义成"释放计算、保住工作区"，和 `docs/SANDBOX-DEPLOY.md:670-687` 给 docker
写的妥协一致：

- 保住：`/workspace` 上的每个文件、git 历史、未提交的改动、`.piforge/` 里的会话文件；
- 丢：进程、终端、浏览器、正在跑的 agent。

`-sandbox-idle-pause` 仍然生效，所以在 k8s 上"空闲 30 分钟自动暂停"意味着**空闲 30
分钟会杀掉正在跑的 dev server**。要么把它设成 0（永远不自动暂停），要么接受这一点。
这是产品决策，不是实现细节，所以写在这里而不是藏在代码注释里。

---

## 4. 安全姿态：实际下发的东西

`internal/sandbox/k8s_objects.go` 是唯一的构造点，`k8s_test.go` 对每一个字段都有断言。

| 位置 | 字段 | 值 | 为什么 |
|---|---|---|---|
| pod | `runtimeClassName` | `gvisor`（可配） | 唯一的 **syscall 边界**。没有它，沙箱就是节点内核上的一个普通容器 |
| pod | `automountServiceAccountToken` | `false` | prompt injection 拿不到集群凭据 |
| pod | `enableServiceLinks` | `false` | 不把 namespace 里其它 service 的地址灌进租户环境 |
| pod | `securityContext.runAsNonRoot` / `runAsUser` / `runAsGroup` | `true` / `1000` / `1000` | 沙箱镜像自己的账户；文件、shell、浏览器服务都降到它 |
| pod | `securityContext.fsGroup` | `1000` | 让 PVC 对 uid 1000 可写（CSI 支持 fsGroup 时） |
| pod | `securityContext.seccompProfile` | `RuntimeDefault` | 即使 runtime 是 runc，也还有一层系统调用过滤 |
| pod | `terminationGracePeriodSeconds` | `30`（`-sandbox-k8s-grace-stop`） | pod 停止时给 agent 一个 flush 的窗口 |
| container | `allowPrivilegeEscalation` | `false` | 对应 `--security-opt no-new-privileges`：setuid 的 sudo 不再变成 root |
| container | `capabilities.drop` | `[ALL]` | 对应 `--cap-drop ALL` |
| container | `capabilities.add` | `[CHOWN, DAC_OVERRIDE, FOWNER, SETUID, SETGID]` | **复用 `sandboxCapAdd`**，不是抄一份：这五个是容器 provider 实测出来的（`docker.go` 里每个都写了"没有它哪一步启动失败"）。uid 1000 下它们是惰性的（非 root 进程没有 effective capability），保留它们是为了 `-sandbox-k8s-allow-root-user` 那一形态——那时它们才是真正的约束 |
| container | `readOnlyRootFilesystem` | `true`（可关） | 镜像的二进制在一个 pod 生命周期内不可改 |
| container | emptyDir 挂载 | `/tmp`、`/run`（可配） | 只读根文件系统下，任何镜像都要这两个可写路径；其它路径一律**不猜** |
| pod | `volumes` | 只有 PVC + 上述 emptyDir | **没有 hostPath、没有 hostNetwork、没有 hostPID、没有 privileged**：集群里没有一条能把节点文件系统带进沙箱的路 |
| 整个对象 | label/annotation | `app.kubernetes.io/managed-by=piforge`、`piforge.io/sandbox=<hash8>`、`piforge.io/user`、`piforge.io/project`（原值在 annotation） | 标签用于选择器与清理；annotation 是审计读的原文（label value 有字符集限制，必须 sanitize） |

`00-namespace.yaml` 还开了 `pod-security.kubernetes.io/enforce: restricted`：即使有人
改了代码把 pod 放宽，API server 也会拒绝，而不是让它悄悄上线。

### 镜像的一个已知前提

`runAsNonRoot` + uid 1000 是**默认**，而仓库现在用的 AIO 镜像的 entrypoint 需要 root
做一次 `useradd`/`chown`（`docs/SANDBOX-DEPLOY.md:789-790` 记过这条）。两种处理：

1. 推荐：沙箱镜像的 entrypoint 以 uid 1000 运行（`USER 1000` 或一个不需要 root 的
   启动脚本），然后什么都不用改；
2. 过渡：`-sandbox-k8s-allow-root-user`，pod 以镜像默认用户启动、省略 `runAsNonRoot`。
   后果要说明白：**bootstrap 以 root 跑**，此时边界是"被 drop 到五个 capability 的
   root + runtimeClassName 的 syscall 边界"，比 uid 1000 弱一档。

provider 不知道镜像里是什么（它不探测镜像内容），所以这个前提写在文档里，而不是让
pod 在 `CreateContainerConfigError` 里挂着。

### 凭据：每沙箱一个 key，放在 Secret 里

`SANDBOX_API_KEY` 不再是 pod env 里的字面量，而是
`secretKeyRef: piforge-key-<hash8>/api-key`。原因是 Pause：pod 被删掉之后，key 必须还
在，Resume 必须交回**同一个** key，否则控制面存的那一行立刻失效。Secret 随沙箱一起
创建、一起删除，`Destory` 最后删它。读它的权限只有控制面的 Role（`get secrets`），
沙箱 pod 没有 token，也没有 list 权限。

---

## 5. 网络与 egress：谁在管

**本 provider 不创建 NetworkPolicy。** 它创建 pod、PVC、Service、Secret——网络策略是
集群 CNI 的事。所以：

- `-sandbox-network-mode tenant`（每租户一个 Docker bridge）在 `-sandbox k8s` 下是
  **启动错误**：Kubernetes 里没有这个对象，接受它就是承诺一个没人实现的边界；
- `-tenant-egress-file` / `-tenant-egress-mode` 同样是**启动错误**，错误信息指向
  `deploy/k8s/40-networkpolicy.yaml`。管理页的 `SandboxEgressApplied` 在 k8s 下是
  false——"这个租户能不能上外网"在这一层由集群回答，不由控制面回答。

`40-networkpolicy.yaml` 给的形状是：

1. 对 `app.kubernetes.io/name=piforge-sandbox` 默认拒绝入站与出站；
2. 只放行控制面 pod → 8080；
3. 只放行 DNS（`kube-system/kube-dns`，53/UDP+TCP）；
4. 只放行控制面 pod → 网关端口（模型调用从这里走）；
5. 可选的"上公网"：`0.0.0.0/0` 但 `except` 掉 `10/8`、`172.16/12`、`192.168/16`、
   `169.254/16`。最后一条是重点：**云上 169.254.169.254 会发节点凭据**，而它是
   link-local，不在私网段里。

没有这组策略时集群**允许一切**：pod 之间互通，sandbox 能连 kubelet、能连云元数据。
这不是"默认安全"的形状，`docs/SANDBOX-K8S-AGENTSANDBOX.md` §3 对 agent-sandbox 的模板
路径也是同一个结论（默认只让 router 进、只让公网出，连集群内 DNS 都挡掉——那份默认值
同样不能直接用）。

---

## 6. 谁证明了什么

这张表是本 provider 最重要的一页：**哪些话有测试，哪些话只有集群能说。**

### 假客户端（`k8s.io/client-go/kubernetes/fake`）证明的

`backend/internal/sandbox/k8s_test.go`，`go test ./internal/sandbox -run TestK8s`。
每个 case 都跑真实的 provider 代码，只有两个 seam 被替换（原因见文件头）：

- **对象图**：pod/PVC/Service/Secret 的全部字段——labels、annotations、`runtimeClassName`、
  两个 securityContext、capabilities（与 `sandboxCapAdd` 逐项相等）、volumes 与
  mount 路径、env（`WORKSPACE`、`BROWSER_NO_SANDBOX`、`SANDBOX_API_KEY` 是 Secret 引用
  而不是字面量）、resources（2 → `2`、`2g` → `2Gi`）、headless Service 的
  `clusterIP: None` 与 selector、PVC 的 `ReadWriteOnce`/StorageClass/5Gi/无 ownerRef；
- **状态机**：ready / pending / running-but-unready / failed / paused / vanished /
  not-found 七种 `Get` 答案；
- **六个动词**：Ensure 幂等（第二次不重建、不换 key）、Ensure 替换无 key 的 pod、
  Pause（pod 没了、PVC/Service/Secret 都在、标记写上、Get=PAUSED）、Resume（pod 回来、
  标记清掉、key 不变、不存在的沙箱不回 READY）、Destroy（四个对象都没了、第二次仍然
  成功、外来 PVC 拒绝删除）；
- **Exec / Attach**：argv（含 `cd` 包装与 `env` 包装）、容器名、namespace、非零退出码
  的报错与输出、双向流（写进去的字节真的到了 stdin，读出来的字节就是 stdout）、
  `CloseWrite` 存在、`User: "root"` 被拒并说明 pods/exec 没有 `-u`、非法 env 条目被拒；
- **preflight**：全通过、缺 namespace、**缺 gvisor 且消息里有后果**、allow-runc 接受、
  RBAC 全拒时逐条列 verb；
- **Endpoint / ContainerPath / 命名**：Service FQDN、override、不存在的沙箱、
  `/workspace` 下恒等、宿主路径被拒、pod 名等于 docker 的容器名、PVC 名等于
  detached volume 名、三种 id 形式（`piforge-xxx`、`sbx_xxx`、`xxx`）解析到同一组名字；
- **对象构造的拒绝**：没有 image、CPU 值不可解析。

### 只有真集群能证明的

`backend/internal/sandbox/k8s_cluster_test.go`，默认 **SKIP**，跑法就写在跳过信息里：

```sh
PIFORGE_K8S_TEST=1 \
PIFORGE_K8S_TEST_KUBECONFIG=$HOME/.kube/config \
PIFORGE_K8S_TEST_IMAGE=piforge/sandbox-agent:dev \
PIFORGE_K8S_TEST_NAMESPACE=piforge \
go test ./internal/sandbox -run TestK8sClusterLifecycle -count=1 -v
```

（kind 上没有 gVisor 时加 `PIFORGE_K8S_TEST_RUNTIME_CLASS="" PIFORGE_K8S_TEST_ALLOW_RUNC=1`，
测试会自己打一行"这次证的是生命周期，不是 syscall 边界"。）

它证的是假客户端结构上做不到的七件事：

1. **API server 接受这些对象**：字段名、label/annotation 字符集、Secret 引用、PodSpec、
   headless Service（fake 什么都存）；
2. **Pod Security Admission 不拒绝**：`restricted` 是服务端策略，客户端只能猜；
3. **调度器真的按 runtimeClassName 起 pod**：fake 里没有 kubelet；
4. **`pods/exec` 是真的 SPDY 流**：字节进出、退出码、`sh -c` 的 cwd 包装；
5. **pause/resume 与真 GC**：pod 真的消失、PVC 真的留下、重建的 pod 真的挂回同一块卷；
6. **`Exec` 真的以 uid 1000 跑**（`id -u` 的输出）；
7. **label selector 真的能选出这一对沙箱**（pod 与 PVC 各一条）。

仍然**不在**这张清单里、只有完整部署（e2e drill / `scripts/deliver-check.ps1` 那条路，
换成 k8s 后端时）能证明的：

- 沙箱镜像**真的以 uid 1000 + 只读根文件系统启动**并让 `/v1/health` 答 200（AIO 镜像的
  entrypoint 前提，§4）；
- 控制面**真的解析得到** Service 的集群 DNS 名字（集群内）；
- checkout/worktree 走 `/v1/shell/exec` 的那条路在 pod 里成立（集成测试为了能从集群外
  跑，把它换成了 pods/exec，见 `k8s_cluster_test.go` 顶部的原因）；
- 冷启动时延（`docs/SANDBOX-K8S-AGENTSANDBOX.md` §1.4 引的上游数字是 GKE/kops 的，
  不是我们的镜像和我们的 StorageClass）；
- `ReadWriteOnce` 卷跨可用区/跨节点 Resume 能不能挂上（取决于 CSI 与 StorageClass）。

---

## 7. 没做的，以及为什么

| 项 | 状态 | 原因 |
|---|---|---|
| **warm pool**（预热的沙箱，92ms 领取） | **没做，故意** | 它是 `agent-sandbox` 唯一真正独有的东西，而要它就得引入 CRD + cluster-scoped 控制器（`docs/SANDBOX-K8S-AGENTSANDBOX.md` §7.3、§9）。那份文档的结论是先把"自己写 Pod+PVC+Service"做对；池子是"有第二个租户需要秒开时再回来"的事 |
| 每租户 egress 的模式 | 交给 NetworkPolicy | §5。要在进程内实现等于实现一个 CNI |
| PID 上限（`--pids-limit` 的对应物） | 没有 | PodSpec 没有这个字段。防线留在集群侧（节点 PID 上限、`LimitRange`、或者一个 pod 级的策略控制器），文档不假装有 |
| 内存快照（Pause 保进程） | 没有 | CRIU + 节点特权；gVisor 下另说。§3 写清了 Pause 的实际语义 |
| 工作区快照（Pause 前固化） | 没有 | 卷是 PVC，快照是 VolumeSnapshot API 的事；`50-workspace-pvc-example.yaml` 给了从快照恢复的形状 |
| 控制面在集群外时的 port-forward | 只提供 `-sandbox-k8s-endpoint` 模板 | 每条沙箱一条 forward 的生命周期管理是另一个组件；模板让外部脚本能接管 |
| `ResourceQuota` 的 per-tenant 版本 | 只能整 namespace | Kubernetes 的 quota 是 namespace 级的；per-tenant 上限仍是 `-tenant-max-sandboxes` + `internal/tenantlimits` |
| ImagePullPolicy | `IfNotPresent` | 一个节点只拉一次沙箱镜像；换镜像靠换 tag（和 compose 一样） |

---

## 8. 排错：错误消息 → 含义

| 消息 | 含义 / 怎么办 |
|---|---|
| `namespace "x" does not exist` | 没 apply `00-namespace.yaml`，或者 `-sandbox-k8s-namespace` 写错 |
| `runtime class "gvisor" does not exist ... no syscall boundary between a tenant and the host` | 节点上没有 runsc，或 RuntimeClass 名字不同。装 shim / 改名；真的不需要就 `-sandbox-k8s-allow-runc` |
| `create "pods/exec" is not allowed in piforge` | Role 少了子资源那一行。`10-serviceaccount-rbac.yaml` 有 |
| `cannot read runtime classes` | 缺 `10-...yaml` 里的 ClusterRole |
| `the sandbox API did not answer within 45s` | pod 起来了但镜像里的服务没起（镜像不对 / entrypoint 需要 root，§4） |
| `pod x was not ready within the deadline: ...` | 后面跟的是容器的 waiting/terminated 原因，或调度失败的原因 |
| `refusing to manage piforge-ws-xxx` | 名字撞上了一个不是本部署创建的 PVC。换 namespace，或确认那块卷是不是别人 |
| `sandbox: not found: piforge-xxx` | `Get`/`Pause` 的 id 解析到的对象不存在（pod 和 PVC 都没有） |
| `pod is gone and the sandbox was not paused` | 被驱逐/被 drain/被手删。数据还在 PVC 上，`Resume` 会重建 |
| `pods/exec has no per-command user` | 有代码路径想用 docker 的 `-u`；pods/exec 没有这个能力，身份是 pod 级的 |
