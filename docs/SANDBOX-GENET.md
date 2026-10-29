# 沙箱对照：Genet（K8s 个人 Pod 平台）与我们

参考仓库：`D:\refs\Uni_Test\_ref\ucm-system-genet-33127d96216b754a303f348f4e6cb47de88b4960`
（下称 Genet，Go + client-go + React + Helm，自述"个人 Pod 申请平台"）。

本文只回答一个问题：**它把"沙箱"这件事做成了什么形状，哪些形状值得我们在 K8s provider 里
复用，哪些不能碰。** 所有结论都指到具体文件与行；拿不到一手证据的一律写"未核实"。

一句话结论：Genet 的"暂停"不是停容器，而是**先把容器 commit 成镜像、把重建所需的 spec 存进
ConfigMap、再删掉 Pod**。这是 K8s 里唯一诚实的一版"释放计算"，但它换来的每一项都绑在它自己的
前提下（单人开发环境、节点上跑 nerdctl、hostNetwork 默认开）。我们只能借顺序，不能借机制。

---

## 1. 它到底怎么做的

### 1.1 先纠正一个说法：没有"会话"

README 第一句是"会话即 Pod"（`README.md:16`），但代码里**没有任何业务 session 对象**：没有会话表、
没有对话记录、没有工作区历史。全仓库唯一的 `Session` 类型是 `handlers.WebShellSession`——终端会话，
存在 Redis 里、5 分钟过期（`backend/internal/handlers/webshell_session.go:57`、`167-180`）。
所谓"会话"就是一次 Pod 申请，产物是：

- Namespace：`user-{userIdentifier}`，`userIdentifier = {username}-{邮箱前缀}`（`backend/internal/k8s/naming.go:15-46`、`naming.go:122-125`）
- Pod 名：`pod-{userIdentifier}-{自定义名|Unix 时间戳}`（`naming.go:80-86`）
- 一个 `genet.io/managed=true` 标签（`backend/internal/k8s/pod.go:457-461`），后面所有列表/清理
  都按它筛（`pod.go:693-701`、`backend/internal/cleanup/lifecycle.go:126-131`）

也就是说 Genet 的"会话"是**一次性计算申请**，不是"一段持续的工作"。我们的 session 有 Pi 的
transcript、worktree、run 历史，两者不是同一个东西。这条差别后面每一条对照都会再出现一次。

### 1.2 状态存在哪：annotations 是索引，真正的状态在两个 ConfigMap 里

Pod 创建时写入的 annotations（`backend/internal/k8s/pod.go:462-478`）确实是"小型状态的全部"：

| annotation | 用途 |
|---|---|
| `genet.io/created-at` | 创建时间（恢复时用来还原 `CreatedAt`，`k8s/suspended_pods.go:158-163`） |
| `genet.io/image` | **源镜像**，commit 时用它补 content store（`k8s/commit.go:514-524`） |
| `genet.io/gpu-type` / `gpu-count` / `cpu` / `memory` | 重建 Pod 时唯一可信的参数来源（`k8s/suspended_pods.go:194-198`） |
| `genet.io/shm-size` / `huge-pages-2mi` | 同上 |
| `genet.io/storage-type` | 卷类型 |
| `genet.io/protected-until` | TTL：清理器唯一读的字段（`cleanup/lifecycle.go:42-60`） |
| `genet.io/suspended` / `suspend-error` / `suspend-failed-*` | 挂起流程的阶段与失败现场（`k8s/pod_suspend_markers.go:16-70`） |

但"无数据库"这个说法要打折。**真正的状态存储是两个 ConfigMap**：

1. `genet-suspended-pods`：每个用户 namespace 一个，`pods.json` 里一整个 JSON 数组，装的是
   被挂起的 Pod 的**重建 spec**（`backend/internal/k8s/suspended_pods.go:19-22`、`148-179`）。
   写入方式是**读出整个数组 → 改 → 整份 Update**（`suspended_pods.go:64-85`、`103-146`），
   没有 `resourceVersion` 前置条件。两个副本同时挂起两个 Pod 就会互相覆盖。
2. Access Window 的停止状态：文档明说"停止状态按工作负载保存为受管 ConfigMap，并绑定主体类型、
   主体 ID、namespace、工作负载 UID 和策略 revision……更新使用版本和 UID CAS"
   （`docs/SCHEDULING_POLICY_ACCESS_WINDOW.md:96`）。这一处做了 CAS，上面那一处没做。

结论：Genet 的事实是"**没有关系数据库**"，不是"没有状态存储"。它只是把状态放在 K8s 对象里，
并且其中最关键的一份（挂起记录）用的是最容易丢更新的写法。

### 1.3 TTL 与 reaper：删之前先 commit

清理器是一个 CronJob（`helm/genet/templates/cronjob-gc.yaml:7-22`，`concurrencyPolicy: Forbid`，
`activeDeadlineSeconds: 7200`，`backoffLimit: 0`），它跑 `backend/internal/cleanup` 的
`CleanupAllPods`（`cleanup/lifecycle.go:119-214`）。每一步：

1. 列出所有 `genet.io/managed=true` 的 namespace，只处理 `user-` 前缀（`lifecycle.go:126-146`）
2. **独占池的用户整个 namespace 跳过**——"用户独享节点，无自动清理压力"（`lifecycle.go:150-156`、`216-234`）
3. 每个 Pod：`isPodProtected` 看 `genet.io/protected-until` 是否还没到期（`lifecycle.go:42-60`），
   到期或无此注解 → `suspendStandalonePod`（`lifecycle.go:236-292`）：
   - `commitWorkloadImage` → 建一个 commit Job 并**同步等它跑完**（`lifecycle.go:490-524`、`526-553`，
     超时取 `Pod.CommitTimeoutSeconds`，默认 1800s）
   - `BuildSuspendedPodRecord` + `SaveSuspendedPod` 把重建 spec 写进 ConfigMap（`suspended_pods.go:148-179`）
   - `DeletePod`——**删除失败时回滚掉挂起记录**（`lifecycle.go:268-276`）

注意第 3 步的顺序："先固化、后释放"，失败就什么都不做（commit 失败直接 return，Pod 不动）。
这一点是 Genet 全文最值得抄的东西，理由见 §3.2。

还有两个 TTL 细节：新建 Pod **不带**保护（`backend/internal/handlers/pod.go:1013-1015`，
"新建 Pod 不带保护：下次 cleanup 即按常规规则处理"），只有用户手动点"延长保护"才会写
`protected-until`（`pod.go:1242-1257`，按 cleanup 的 cron 表达式算下一次清理时刻）。
所以 README 和用户指南里的"生命周期：1-24 小时"（`README.md:172`、`docs/USER_GUIDE.md:127-140`）
在代码里没有对应物——创建表单里也没有这个字段（`frontend/src/pages/Dashboard/CreatePodModal.tsx`
里没有小时/生命周期相关命中）。**README 这一条是过时的。**

### 1.4 commit 是怎么做的：一个特权 Job + 节点上的 containerd socket

`CreateCommitJob`（`backend/internal/k8s/commit.go:47-189`）建的 Job：

- 钉在源 Pod 所在节点（`Affinity: buildPodAffinity(baseAffinity, spec.NodeName)`，`commit.go:116`）
- `SecurityContext: Privileged: true`（`commit.go:124-126`）
- hostPath 挂 `/run/containerd/containerd.sock`（`commit.go:137-154`）
- 镜像来自配置 `config.Images.Nerdctl`（`commit.go:121`，默认值见 `helm/genet/values.yaml:751`）
- 没有 `ContainerID` 就直接拒绝执行，因为"所有 Genet pod 主容器都叫 workspace，按容器名瞎猜会偷走
  同节点其他用户的 workspace 容器"（`commit.go:48-53`）
- 脚本先查 containerd 的 CRI label `io.kubernetes.pod.name` / `.namespace` 校准归属，不匹配就
  `exit 1` 并打 "Refusing to commit"（`commit.go:375-410`）

真正的命令是（`commit.go:470-478`）：

```
nerdctl -n k8s.io commit --compression=zstd --pause=false "$CONTAINER_ID" <target>
```

`--pause=false` 是因为"部分节点 OCI runtime cgroup freeze 失败"（`commit.go:471-473`）。
挂起场景传 `Push: false`，于是**镜像只留在本节点**（`commit.go:444-452`，日志原话
"Keeping local-only suspend image on node"）。

### 1.5 工作区活不活：活，但不是靠 commit，也不是靠 PVC

这是最容易看错的一处。恢复链（`backend/internal/handlers/pod_suspended.go:523-597`）说明得很清楚：

- 新 Pod 的 `Image` = 挂起镜像（`pod_suspended.go:532`）
- 本地镜像要 `CheckLocalImageExists` 验一遍（`pod_suspended.go:593`）
- 然后 `req.NodeName = sourceNode`——**钉回原节点**（`pod_suspended.go:585-597`），因为镜像是本地
  的，换个节点就没有这一层
- 恢复前还要过配额（`pod_suspended.go:641`）

所以"工作区"分成三层，各自有不同的存活方式：

| 层 | 存在哪 | 活得过删除吗 |
|---|---|---|
| 用户的代码/数据 | Helm 配的 `storage.volumes`，默认是 NFS `/private/{username}` 挂到 `/workspace-genet`（`helm/genet/values.yaml:369-377`） | 活。NFS 目录与 Pod 无关 |
| 容器可写层（装好的包、`/root` 下的东西） | commit 出来的本地镜像 | 活，但只在**原节点**上 |
| 进程、终端会话、内存 | 无 | 死。这是删 Pod，不是冻进程 |

补充两条"absence 也是结论"：

- **没有"每沙箱 PVC"这个概念。** PVC 只在两种情况下创建：`storage.volumes` 里显式写
  `type: "pvc"` 的卷（`backend/internal/k8s/pvc.go:16-35`、`63-123`），或用户自己挂 local-path
  （`k8s/suspended_pods.go:453-460`）。默认 workspace 是 NFS 路径，不是 PVC。
- `DeletePVC` 存在但注释写着"通常不使用，保留用户数据"（`pvc.go:147-154`）。

### 1.6 Web Shell：浏览器 → 独立网关 → apiserver `pods/exec`

这条路是**真 TTY**，不是轮询：

1. 前端 `POST` 建会话。后端检查 `Pod.Terminal.Enabled`、Pod 必须 `Running`，然后
   `CreateWebShellToken(cfg.OAuth.JWTSecret, session)` 签一个带会话声明的 token，TTL 取
   `webshellGateway.attachmentTTLSeconds`（默认 300s）（`backend/internal/handlers/webshell.go:112-186`，
   ttl 在 `150-153`）
2. 返回的 URL 是 `{websocketPathPrefix}/ws?token=...`（`webshell.go:175-185`），前缀在
   `helm/genet/values.yaml:810` 是 `/api/webshell/attachments`
3. 独立进程 `genet-webshell-gateway`（`helm/genet/templates/deployment-webshell-gateway.yaml`，
   独立 ServiceAccount、`/health` 探针、端口 8081）验 token 后升级 WS
   （`backend/internal/webshellgateway/server.go:46-77`）
4. 网关用 `remotecommand.NewSPDYExecutor` 调 `pods/exec`，`TTY: true`，带
   `TerminalSizeQueue`（`webshellgateway/streamer.go:180-206`）；shell 是
   `/bin/sh -lc 'if [ -x /bin/bash ]; then exec /bin/bash; ...'`（`streamer.go:208-210`）
5. 前端是 `@xterm/xterm` + fit addon，把窗口变化当 `{"type":"resize",cols,rows}` 文本帧发回去
   （`frontend/src/pages/WebShell/index.tsx:4`、`39-41`）；开发期 `setupProxy.js` 把
   `/api/webshell` 单独代理到 `:8081`（`frontend/src/setupProxy.js:5-14`）

`handlers/webshell.go:304-329` 里还有一份**同样的** SPDY 实现（后端进程内），
`WebShellWebSocket` 是它的入口。两条路并存，线上用的是网关那条（`deployment-webshell-gateway.yaml`
+ `setupProxy` 都指向它）。这属于"没删干净的旧路径"，不值得学。

另外 Pod 启动脚本只做了两件与终端有关的事：装/链 tmux 到固定目录
（`backend/internal/models/default_scripts/terminal_install.sh:12-54`），然后 `tail -f /dev/null`
（`pod_startup.sh:55`）。**没有 ttyd、没有 sshd、没有 web 终端进程**——终端完全靠 apiserver exec。

### 1.7 SSH / VSCode：README 说的那套在代码里没有落地

按 README（`README.md:23`）和用户指南（`docs/USER_GUIDE.md:186-200`），应该能
`ssh -p <端口> root@<节点IP>`。但：

- `models.PodConnections.SSH` 在唯一的构造点被写成**零值** `models.PodSSHConnections{}`
  （`backend/internal/handlers/pod.go:369-376`），全仓库没有任何地方给它赋值
  （`backend/internal/models/pod.go:151-155` 定义，除此之外只有上面那一处引用）
- 没有分配 SSH 端口的代码；没有 `sshd` 启动；Pod 里没有 `containerPort` / `hostPort`
  （`k8s/pod.go:480-489` 的 PodSpec 只有 HostNetwork/HostIPC/RestartPolicy/Containers/Volumes）
- Pod 启动脚本起完 `tail -f /dev/null` 就结束（`pod_startup.sh:55`）
- Helm 里 Pod 侧没有任何 NodePort/hostPort（只有 exporter 的 hostPort 和 diagnoser 的 nodePort）

**所以"hostNetwork 暴露 SSH"在这份快照里是空壳。** hostNetwork 确实默认开着
（`helm/genet/values.yaml:244`、`402`，`pod.hostNetwork: true`），`hostIPC` 也在，但端口怎么分、
sshd 谁来起，仓库里找不到实现。

VSCode 有两个真实存在的东西，都不是 SSH：

1. **指南**：Pod 卡片上的"VSCode"弹窗教你装 `ms-kubernetes-tools.vscode-kubernetes-tools` /
   `ms-vscode-remote.remote-containers` 然后 attach 到 Pod
   （`frontend/src/pages/Dashboard/PodCard.tsx:236-316`）
2. **code-server（可选）**：后端用 `httputil.ReverseProxy` **直连 Pod IP**，不建 Service、不建
   Ingress（`backend/internal/handlers/code_server.go:117-180`、`173-180`）

`pod_startup.sh:16-35` 只是把 `/root/.vscode-server` 链到持久目录，为 Remote-SSH 预置缓存 ——
预置缓存不等于提供入口。

### 1.8 端点暴露：没有"发布端口"，只有反向代理和 API server

三条路径，都不经过宿主端口：

| 用途 | 路径 |
|---|---|
| Web Shell | 浏览器 → 网关 → apiserver `pods/exec`（§1.6） |
| code-server | 浏览器 → 后端 → `http://<PodIP>:<port>`（`code_server.go:173-180`） |
| 日志/describe/events | 后端 → apiserver |

这正好是我们的 `Endpoint` 未来该长的样子：**一个名字，不是一个宿主端口**。

### 1.9 配额：预检 + ResourceQuota 双闸门

1. 预检：`checkQuotaWithLimits` 数该 namespace 里非 Terminating、非 Succeeded/Failed 的 Pod 数，
   累加 GPU，超了返回 403 和一句人话（`backend/internal/handlers/pod.go:2066-2113`）
2. 硬闸门：每个用户 namespace 一个 `ResourceQuota`，名 `genet-user-quota`，hard 是
   `count/pods` + `requests.nvidia.com/gpu`（以及 A800/Ascend 的 `requests.<res>`），
   **scope 是 `NotTerminating`**，好让 commit/清理这些系统 Job 不占用户配额
   （`backend/internal/k8s/namespace.go:556-600`，scope 在 `571`，注释在 `569-570`）
3. 上限来源：全局 `podLimitPerUser` / `gpuLimitPerUser`，管理型 API Key 可以有 override
   （`namespace.go:539-553`、`admin_users.go:456-482`）

独占池用户整个跳过清理（§1.3），等于"配额与回收都按池分类"。

### 1.10 Helm 参数化了什么

`helm/genet` 一个 chart 里塞了控制面 + 两个 CronJob + 可选 webhook + ingress：

| 段 | 参数 | 位置 |
|---|---|---|
| 配额 | `backend.config.podLimitPerUser`(5) / `gpuLimitPerUser`(20) | `values.yaml:66-67` |
| 调度 | `pod.hostNetwork`/`hostIPC`/`dnsPolicy`/`securityContext` + `pod.poolPolicies` 按池覆盖 | `values.yaml:244`、`441-495` |
| 存储 | `storage.volumes[]`（name/mountPath/type/storageClass/size/scope） | `values.yaml:310-377` |
| 个人目录初始化 | `userWorkspaceInit.{enabled,server,path,mountPath}`（登录后建 `/private/<id>`） | `values.yaml:756-761` |
| 终端网关 | `webshellGateway.{image,replicas,port,attachmentTTLSeconds,webSocketPathPrefix}` | `values.yaml:802-811` |
| 清理 | `cleanup.{schedule="0 23 * * *",timezone,concurrencyPolicy,activeDeadlineSeconds}` | `values.yaml:876-895` |
| 镜像清理 | `imageCleanup.{enabled,schedule="30 23 1 * *",retention="168h",includeImagePrefixes,includeDangling}` | `values.yaml:926-948` |
| 入口 | `ingress.{enabled,className,host,tls,certManager}`、`webhook.*` | `values.yaml:980-1005`、`1028-1062` |

镜像清理也是 CronJob：主 Pod 给每个纳管节点起一个子 Job，跑
`nerdctl -n k8s.io image prune` 风格的脚本，带 keep-list（挂起镜像不能删）与 in-use 保护
（`backend/internal/cleanup/image_cleanup.go:106-120`、`backend/internal/k8s/image_cleanup.go:133-235`）。

---

## 2. 和我们的形状对照

| 概念 | Genet | PiForge 现在 | 差在哪 |
|---|---|---|---|
| **租户/项目粒度** | 用户 namespace `user-{id}` + 每 workload 一个 Pod；没有"项目"这一层，同一个人的 Pod 之间不共享目录（除非都挂同一个 NFS 子路径） | `(user, project)` 一个沙箱，一个项目内所有 session 共享一份 checkout，分支 session 各一个 linked worktree（`backend/internal/sandbox/docker.go:155-180`、`docs/SANDBOX.md:163-201`） | 它把"谁"当边界，我们把"谁 + 哪个仓库"当边界。我们的粒度更细，也更适合 agent（一个项目 = 一份可复现的 checkout） |
| **沙箱生命周期** | 建 Pod → 用 → 当晚 23:00 被 cron 挂起（commit + 删）；`protected-until` 是唯一豁免 | `CREATING → STARTING → READY → BUSY → IDLE → PAUSED → TERMINATED` 九态（`contracts/contracts.go:59-72`），但只有手动暂停/唤醒有调用者（`httpapi/sandbox_handlers.go:12-17`），FR-5.3 的 TTL 是 MISSING（`docs/STATUS.md:59`） | 我们有状态机没有看门狗；它有看门狗没有状态机（状态全从 K8s 对象临时算）。两边各缺一半 |
| **工作区持久化** | 用户级 NFS 目录（`values.yaml:369-377`）+ 容器层 commit 成节点本地镜像；**没有每沙箱 PVC** | 宿主目录 `<DataRoot>/<userID>/<projectID>` 绑定挂载到 `/workspace`（`docker.go:168-180`、`251-260`），暂停/重建都不丢（`docs/SANDBOX.md:64-68`） | 形态其实一致（都是"沙箱外的一份目录"）。差别在**我们的目录不是卷**：控制面与 daemon 分机就失效（`docs/SANDBOX-DEPLOY.md:14-17`） |
| **暂停/恢复的真实含义** | 删 Pod；恢复 = 用快照镜像在新 Pod 里重建，**钉回原节点**（`pod_suspended.go:585-597`）。内存态、进程、终端一律丢 | `docker stop` / `docker start`（`docker.go:739-754`），容器里的 Chromium、shell 会话都在，内存也还在 | 我们的 Pause 语义**更强**（进程活着），代价是它只能在"能 stop"的运行时成立。K8s 里没有这个动作，这就是缺口 |
| **端点暴露方式** | 不发布任何宿主端口：终端走 apiserver exec，code-server 走控制面反向代理到 Pod IP（`code_server.go:173-180`） | `-p 127.0.0.1::8080` 随机宿主端口，`Endpoint` 从 `docker port` 读回再拼 `http://127.0.0.1:<port>`（`docker.go:270-273`、`947-964`） | 我们硬编码了"控制面是宿主进程"。它的形状（名字而非端口）才是 compose 与 K8s 能共用的那一半 |
| **终端的实现路径** | xterm ↔ WS ↔ 独立网关 ↔ `pods/exec` **TTY**，支持 resize（§1.6） | 行式会话：`sessions/create → write → view` 每 175ms 轮询，无 PTY，resize 只记录不转发（`docs/SANDBOX.md:273-306`） | 这是产品级差距：vim/htop/作业控制在我们这儿不可用（`docs/SANDBOX.md:405-411` 自己写了） |
| **配额** | 应用层预检 + 每 namespace `ResourceQuota`（`count/pods` + GPU，`NotTerminating` scope）（`pod.go:2066-2113`、`namespace.go:556-600`） | `sandboxes` 表有 `cpu_milli` / `mem_mb` / `disk_gb` 三列（`store/schema.sql:70-72`），但**只写不读**：除 `store/projects.go:155-181` 的插入外全仓库无引用；FR-5.8 是 PARTIAL（`docs/STATUS.md:64`） | 我们有字段没有闸门。它闸门在集群层，应用层只是给好错误信息 |
| **镜像/工具链** | 用户在创建时自己选镜像；平台只管 commit 出快照镜像并可选推 registry（`commit.go:425-452`） | 一个部署级 `-sandbox-image`（AIO 1.11.0）承载浏览器/终端/文件 API（`docker.go:126-134`、`docs/SANDBOX-DEPLOY.md:370-380`） | 它把"装环境"外包给用户；我们把"沙箱里有 Chromium 和 shell"做成产品能力。所以我们的镜像必须固定，不能像它那样让用户传 |
| **认证** | OAuth/OIDC → JWT cookie `genet_session`（`backend/internal/auth/oauth.go:24-33`），失败可回退代理头/开发模式；K8s 身份由 namespace 承载；另有管理型 API Key（绑定独立 namespace + 配额 override） | 自有账号表 + cookie + `X-PF-CSRF`（`docs/SANDBOX.md:330`），角色 admin/user；沙箱内 token 是**一个部署级** `SANDBOX_API_KEY`（`docker.go:284-286`） | 它是"一次登录换一个 K8s 身份"；我们是"一次登录换一个沙箱"。它那套更贴 K8s，我们那套更好审 |

---

## 3. 值得抄的：五件，按性价比排

### 3.1 终端走 `pods/exec` 的真 TTY，而不是我们现在的行式轮询（最值钱）

**为什么**：它不是"更好的终端"，它是**另一种东西**。我们现在能跑 `echo`，跑不了 `vim`、
`htop`、`npm run dev` 的前台交互，也没有作业控制；`docs/SANDBOX.md:405-411` 已经把这三条
当已知缺陷写着。Genet 用 233 行（`webshellgateway/streamer.go`）证明了这条路在 K8s 里是通的：
`remotecommand.NewSPDYExecutor` + `TTY: true` + `TerminalSizeQueue`，resize 是结构化的通道，
不是"记录但不转发"。

**代价（M）**：我们的 WebSocket 层、ring buffer、前端 xterm 都不用改——要换的只是"字节从哪来"。
需要一个 `term` 实现把 SPDY 流的 `io.Reader/Writer` 接到现有 ring（对应现在
`backend/internal/term/remote.go` 的位置）。真风险是 `pods/exec` 的 RBAC（`pods/exec` 权限等于
"能进任何沙箱"）和每次 exec 一条连接过 apiserver——Genet 用一个独立网关把这份权限从控制面里
摘出去，这一点也要一起抄（§3.4）。

### 3.2 "先固化、再释放"的顺序——借顺序，不借机制

**为什么**：Genet 的挂起顺序是 commit → 写记录 → 删 Pod，任一步失败就什么都不做
（`cleanup/lifecycle.go:236-292`，commit 失败直接 return，Pod 不动）。这个顺序正好是我们
`docs/PRD.md:384` 里 FR-5.3 的原话——"**先快照后销毁，永不先杀**"——而我们现在一条都没实现
（FR-5.3 MISSING）。它给了我们一个已经被生产用过的**形态**：cron 驱动的、"到期 → 固化 → 释放"
的状态机，带上失败不推进的语义。

**代价（M）**：借形态很便宜（一个 ticker + `expires_at` 列已经有了，`store/schema.sql:75`），
难的是"固化"这一步在我们这儿是什么（见 §3.3 的评估）。可以先落地"到期 → Pause（保工作区）→
标记"，把"固化"留成第二步。

### 3.3 `nerdctl commit` 能不能当我们的 Pause？——不能，但它逼我们把话说清楚

这是本次评估里唯一需要写长的判断。

**它解决的问题**：K8s 里没有 `docker pause`。删 Pod 会丢掉"这个环境装好了什么"。
`nerdctl commit` 把容器可写层拍成镜像，于是恢复时"包还在、`/root` 下的配置还在"。
这确实是"释放计算 + 保住环境"的一种答案。

**为什么对我们不成立**，四条，按严重程度：

1. **它保的不是我们的工作区。** 我们的工作区是 `<DataRoot>/<userID>/<projectID>` 里的
   git checkout + 未提交改动，**在 bind mount 上，不在容器的可写层里**（`docker.go:251-260`）。
   删 Pod 再建、挂同一个卷，未提交改动一个字节都不会丢。commit 只额外保住"容器里装过的东西"。
   换句话说：它对准的是 Genet 的痛点（用户 `pip install` 在容器层），不是我们的。
2. **它和我们的"工作区在哪"这条不变量冲突。** 我们的 `/workspace` 是**挂载点**，立足点是
   "控制面写宿主目录、容器看同一批字节"（`docs/SANDBOX.md:126-127`），以及"`.git` 归容器内的
   `gem`"（`docs/SANDBOX.md:112-131`）。commit 能拍到的只有可写层——而按这条不变量，工作区
   **本来就不在可写层里**。要让它有意义，就得把 checkout 搬进可写层，那正好把"谁建 `.git`、
   `.git` 归谁"这条用两次真实故障换来的规则推翻（`docs/SANDBOX.md:105-110`、`174-176`）。
3. **它需要特权。** commit Job 是 `Privileged: true` + hostPath `/run/containerd/containerd.sock`
   （`commit.go:118-155`）。这是"节点 root"，和 `docs/SANDBOX-DEPLOY.md:59-71` 里我们拒绝
   "把 socket 挂给控制面"是同一条理由，只是换了个执行者。
4. **它把沙箱钉在节点上。** 镜像是节点本地的（`commit.go:444-452`），所以恢复必须
   `req.NodeName = sourceNode`（`pod_suspended.go:585-597`）。我们做的是**调度**（把沙箱放到有
   空间的节点），这一条正好把它取消掉。要摆脱就得 `Push: true` 推 registry，于是每次暂停都变成
   一次 GB 级上传（AIO 镜像体积见 `docs/SANDBOX-DEPLOY.md:370-374`，确切数字未核实）。

**那我们抄什么**：抄它的**顺序**（§3.2）和它的**记录**——"挂起时把重建所需的 spec 完整存下来，
恢复只依赖这份记录 + 一份镜像"。这一条对我们成立，而且我们已经有更好的载体（SQLite 的
`sandboxes` / `snapshots` 两张表，`store/schema.sql:59-77`、`207-219`），不需要 JSON blob。
**不抄它的机制。** 我们的 Pause 应该是"删 Pod，保 PVC"，我们的"固化"应该是工作区快照
（`snapshots.blob_key` 那一层，现在只有数据层、没有生产者，`docs/STATUS.md:62` FR-5.6 PARTIAL）。

### 3.4 终端跑在一个独立进程 + 短期 token，而不是控制面自己持有 `pods/exec`

**为什么**：`pods/exec` 的权限语义是"能进任何沙箱的 shell"。Genet 把它从控制面摘出来放进
`genet-webshell-gateway`（独立 Deployment/ServiceAccount，`deployment-webshell-gateway.yaml`），
控制面只签一个 5 分钟、带会话声明的 token（`webshell.go:150-173`），网关验完才升级 WS
（`webshellgateway/server.go:46-57`）。这和我们 `docs/SANDBOX-DEPLOY.md:180-190` 想要的
"runner 是唯一持有危险权限的进程"是同一个模式，只是它在终端这一侧。

**代价（S/M）**：多一个二进制、多一个 Service、一次 token 设计。收益是：控制面被拿下不等于
所有沙箱的 shell 被拿下；也顺手解决"WebSocket 长连接挂在 HTTP 服务上"的运维问题
（我们现在的 PTY WS 就在 `httpapi` 里）。

### 3.5 每用户 namespace 作为配额与清理的分片

**为什么**：`user-{id}` 让 `ResourceQuota`、`NetworkPolicy`、清理扫描都有一个天然的 key，
`cleanup` 只要按 label 列 namespace 再按 namespace 列 Pod（`lifecycle.go:126-146`）。
我们的 `sandboxes` 表里 `user_id` 已经有索引，但集群侧没有对应物，所有沙箱挤在一个 namespace
里，配额只能靠应用层数数（而我们现在连数都没数，§2 的配额行）。

**代价（S）**：一个 namespace 模板 + 一个 `ResourceQuota`（`count/pods` + `requests.cpu/memory`）。
真正的成本是运维复杂度：namespace 数量随用户增长，`kubectl get pods -A` 会变长。

---

## 4. 不值得抄的

### 4.1 "无数据库"——它自己已经破了，而且破得比数据库更难查

`genet-suspended-pods` 是**每 namespace 一个 JSON 数组的整份读改写**
（`k8s/suspended_pods.go:64-85`、`103-146`），没有 CAS。两个挂起同时发生就丢一条记录，
表现是"这个 Pod 挂起了但恢复不了"，日志里只有一次 Update 成功。
Access Window 那份做得对（CAS + 分片 + GC，`docs/SCHEDULING_POLICY_ACCESS_WINDOW.md:96`），
正说明**同一个仓库里两种写法并存**：一处把它当配置，一处把它当数据库。

annotations 只能存小状态：容量有上限、没有 schema、没有查询能力。审计（谁在什么时候暂停了谁的
沙箱）、权限（这个用户能不能看那个沙箱）、跨对象查询（某项目的所有历史沙箱）这些需求，
关系数据是唯一不别扭的答案。我们已经有 SQLite（`store/schema.sql`），正确的用法是让它做
**索引与审计**，K8s 对象做**事实**——而不是像 Genet 那样把两者混在 ConfigMap 里各写一半。

### 4.2 hostNetwork 在多租户集群里的爆炸半径

`pod.hostNetwork: true` 是**默认值**（`helm/genet/values.yaml:244`、`402`），`hostIPC` 也在，
`allowHostNetwork: true` 还允许用户在请求体里再打开（`values.yaml:697`）。
后果不是"少一层隔离"，是这几件具体的事：

- Pod 的 IP 就是节点 IP，Pod 直接看见节点的网络命名空间；`NetworkPolicy` 对 hostNetwork Pod
  **不生效**（K8s 的已知行为，不是 Genet 的 bug）
- 端口冲突变成宿主机上的冲突：两个 Pod 都想 listen 同一个端口，第二个起不来，而且报错在
  Pod 里、看起来像镜像问题
- 节点上的其他进程（kubelet、exporter、其它租户的 Pod）与沙箱之间没有网络边界
- 结合 `hostIPC`，还能看见宿主的共享内存段

对 Genet 这是合理取舍：**它假设这些是"人的开发环境"，跑的是自己写的训练脚本**。
对我们的威胁模型不成立：我们跑的是**agent 生成的任意代码**，而
`docs/SANDBOX-DEPLOY.md:337-340` 已经把"多租户生产第一版必须换 gVisor"写成结论。
一条 hostNetwork 就能把这层结论作废。

### 4.3 特权 commit Job 挂 containerd socket

见 §3.3 第 3 条。这是"给了一个 Job 节点 root"，和我们拒绝把 docker socket 挂给控制面
（`docs/SANDBOX-DEPLOY.md:59-71`）是同一条理由。它挡不住的东西也一样：Job 能被诱导去
commit 任何容器，脚本里的 label 校验（`commit.go:375-410`）是**缓解**，不是边界。

### 4.4 "所有未保护的 Pod 一律挂起"的清理策略

`CleanupAllPods` 的判据只有一条：`protected-until` 没到期就跳过，否则挂起
（`cleanup/lifecycle.go:180-202`）。这是给"个人开发机"设计的：反正第二天早上要用的时候
点一下恢复。对我们不成立——一个 agent 跑了 6 小时的迁移任务，不该因为跨过了 23:00 就被
commit + 删除。我们的回收必须按**沙箱自己的 `last_active_at`** 判（`sandboxes` 表已有该列，
`store/schema.sql:73`），而不是按全局时刻表。

### 4.5 单一部署级凭据 + 跨租户可达的端点

我们现在的 `SANDBOX_API_KEY` 是部署级的（`docker.go:284-286`），今天之所以没炸，是因为端点
发布在 `127.0.0.1`（`docker.go:270-273`）。`docs/SANDBOX-DEPLOY.md:686-688` 已经写明这条
"遮挡"一旦换成 Service/DNS 就消失。Genet 那边形态不同（无 endpoint key，靠 apiserver 鉴权），
结论一样：**端点一旦是"名字"，就必须是每沙箱一个 token**。这一条不是"抄不抄"，是搬 K8s 之前
必须先补的洞。

### 4.6 威胁模型不同，这句话要写在最前面

Genet 是**给人的开发环境**：用户是自己人，镜像是自己选的，Pod 里有一个 root（`pod_startup.sh`
全程 root，装包、`ulimit`、写 `/root`），目标是"别让我排队等 GPU、别让我的环境第二天没了"。
PiForge 是**给 agent 跑任意代码的沙箱**：代码不是人写的，没人 review，出问题的时候攻击者
已经在你机器上了，目标是"它炸了别带走别人"。

所以 Genet 里"够用"的每一处（hostNetwork、privileged、无 per-sandbox 配额、
清理即删除），在我们这儿都要重新论证一次。反过来也一样：我们那些"为 agent 设计"的东西
（每 (user,project) 一个容器、worktree 即会话、三个功能开关）在它那儿是多余的。

---

## 5. 对我们的具体动作：如果 docker provider 换成 K8s provider

五件，按顺序。每一项都写清"为什么是这个顺序"，因为前一项不做，后一项会写成另一个 Genet。

1. **（S）端点先变成"名字"，再谈 Pod。** `Endpoint` 提进 `contracts.SandboxProvider`
   （现在只在 `*Docker` 上，`docker.go:947-964`），返回值从
   `http://127.0.0.1:<port>` 变成 `http://pf-<hash8>.<ns>.svc:8080`。
   名字仍用 `sha256(userID + "/" + projectID)` 前 8 字节（`docker.go:155-158`）——它已经是
   Pod/Service/PVC 名的同一个后缀，不要重新发明。
   **为什么第一**：这是 compose 与 K8s 唯一能共用的一层；没有它，K8s provider 会写成
   "只能在 K8s 里跑"的第二套代码。
2. **（S）配额先上闸门，再上调度。** 照 Genet 的双闸门：应用层预检给好错误信息，集群层
   `ResourceQuota`（`count/pods` + `requests.cpu/memory`）兜底。我们现在
   `cpu_milli`/`mem_mb`/`disk_gb` 三列只写不读（`store/schema.sql:70-72`），FR-5.8 名义上是
   PARTIAL、实际是零闸门。
   **为什么第二**：没有配额的多租户集群，一个 fork bomb 就能把邻居带走；这跟 provider 是谁无关。
3. **（M）Pod per `(user, project)` + 每沙箱一个 PVC 挂 `/workspace`。**
   `Pause` = 删 Pod（保 PVC），`Destroy` = 删 Pod + Service（PVC 默认保留，对齐现在
   `docker.go:756-764` 的"不删数据卷"），`Resume` = 重建 Pod 并轮询到 Ready。
   工作区从"宿主目录"变成"卷"，`RootDir` 的语义必须同时改成"沙箱内路径"
   （`docs/SANDBOX-DEPLOY.md:613-625` 给了短期/长期两条路，走它推荐的短期那条）。
   **为什么第三**：它同时是"工作区能活过重新调度"和"`provision` 不再需要宿主 git"的前提；
   而 `AddWorktree`（`contracts.go:561-566`）已经把"在沙箱里跑命令"这条能力单独抽出来了，
   换 provider 时它是现成的接缝。
4. **（M）终端换真 TTY：`pods/exec` + 现有 WS 层，网关可选。**
   先在后端进程内做（`handlers/webshell.go:304-329` 那份代码就是模板），把
   `backend/internal/term` 的 ring buffer 接上 SPDY 的 stdin/stdout；resize 从"只记录"改成
   `TerminalSizeQueue`。等这套稳了，再考虑把它拆成独立 gateway + 5 分钟 token（§3.4）。
   **为什么第四**：它不改任何数据模型，是纯增量，但它是**用户能立刻感觉到**的那一件；
   放在 provider 之后，是因为它需要 provider 先能给出"沙箱在哪"。
5. **（M）TTL + reaper：`last_active_at` + 一个 cron，顺序是"先固化、后释放"。**
   15 分钟空闲 → `Pause`；PAUSED 14 天 → 回收（保留快照）。判据必须是沙箱自己的
   `last_active_at`，不是全局时刻表（§4.4）。"固化"这一步先落成"工作区快照"
   （`snapshots` 表已有 `blob_key`/`content_hash`，没有生产者），**不是** commit 镜像。
   **为什么最后**：它需要 1–4 都成立才有意义（没有 Endpoint 就不知道怎么唤醒、没有配额就
   不知道回收谁、没有 Pause 就没有中间态）。

**明确不取**（写在这里，免得下一个人的 PR 又捡回来）：

- commit-to-image 作为 Pause 机制（§3.3 四条理由），以及它依赖的特权 Job + containerd socket
- annotations / ConfigMap 当状态源（§4.1）。SQLite 继续做索引与审计
- hostNetwork / hostIPC 的默认值（§4.2）
- "所有未保护的 Pod 一律挂起"（§4.4）
- 一份共享的 `SANDBOX_API_KEY` 配一个跨租户可达的端点（§4.5）——这一条是前置条件，不是选项
- 用 `pods/portforward` 当数据面（`docs/SANDBOX-DEPLOY.md:356-368` 已排除，Genet 也没这么做）

---

## 6. 未核实

- **"会话"到底有没有别处定义**：我在 `backend/**`（219 个非测试 `.go` 文件）里搜过
  `type Session` / `SessionID` / `SessionStore`，只命中 WebShellSession 与 CLI token 两类；
  但不排除它在更早的版本里存在、这份快照删干净了。
- **SSH 是不是被删掉了**：`models.PodSSHConnections` 是空结构、没有任何赋值点，我据此判断
  "SSH 暴露在代码里没有落地"。但也可能是"这个部署形态不需要 SSH、代码在别处"——我只有这份快照。
- **README 的"生命周期 1-24 小时"**：创建路径明确写"新建 Pod 不带保护"（`pod.go:1013-1015`），
  创建表单里也没有时长字段。我只能说**这份快照里没有**，不能说 Genet 从来没有。
- **commit 出来的镜像有多大、恢复要多久**：完全没测。`--compression=zstd`（`commit.go:475`）
  说明作者在意体积，但没有数字。
- **`CleanupAllPods` 在 2000 个 Pod 规模下的表现**：每个 Pod 一次 commit Job + 同步等待
  （`lifecycle.go:519` 的 `waitForCommitJob`），串行。这是 O(n) 次 Job 创建，实际耗时未核实。
- **`webshellGateway` 的 HA 行为**：`replicas: 2`（`values.yaml:807`），但 token 是无状态的
  JWT，理论上任意副本都能接；没找到会话粘滞配置，也没验证过多副本下的 WS 重连。
- **access window 的 ConfigMap 分片/GC 细节**：我只读了文档描述
  （`docs/SCHEDULING_POLICY_ACCESS_WINDOW.md:96`），没有逐行读实现。

## 7. 实际读过的东西

Genet（路径相对仓库根）：

- `README.md`（全文，628 行）— 功能清单、配额/加解密/清理的 values 示例
- `docs/ARCHITECTURE.md`（全文，763 行）— 模块边界、创建/commit/清理时序图、"无数据库"的自述
- `docs/SCHEDULING_POLICY_ACCESS_WINDOW.md:1-110` — 策略契约、约束合成、硬停止与自动恢复
- `docs/USER_GUIDE.md`（grep 命中段）— SSH/VSCode/Web Shell 的用户视角
- `helm/genet/values.yaml`（结构 + 逐段读 `300-380`、`750-805`，其余按注释行 grep）
- `helm/genet/templates/cronjob-gc.yaml`、`cronjob-image-cleanup.yaml`、`deployment-webshell-gateway.yaml`
- `shared/labels/labels.go`（全文）
- `backend/internal/k8s/pod.go:440-560`（+ function/annotation 索引与 `:693-701` 的 grep 命中）
- `backend/internal/k8s/naming.go`（全文）
- `backend/internal/k8s/pod_suspend_markers.go:1-88`
- `backend/internal/k8s/suspended_pods.go`（全文，594 行）
- `backend/internal/k8s/commit.go`（全文，613 行）
- `backend/internal/k8s/namespace.go:556-600`（+ function 索引）
- `backend/internal/k8s/pvc.go`（function/comment 索引）
- `backend/internal/k8s/image_cleanup.go:120-235`
- `backend/internal/k8s/workload_startup.go`（grep 命中段）
- `backend/internal/cleanup/lifecycle.go`（全文，587 行）
- `backend/internal/cleanup/image_cleanup.go:1-120`
- `backend/internal/handlers/pod.go:300-380`、`:1008-1057`、`:1235-1279`、`:2060-2114`
- `backend/internal/handlers/pod_suspended.go:386-476`、`:523-597`
- `backend/internal/handlers/webshell.go`（全文，346 行）
- `backend/internal/handlers/code_server.go`（grep 命中段）
- `backend/internal/webshellgateway/server.go`（全文）、`streamer.go`（全文）
- `backend/internal/auth/oauth.go`（grep 命中段）
- `backend/internal/models/pod.go:145-160`（grep 命中）、`default_scripts/pod_startup.sh`（全文）、`default_scripts/terminal_install.sh`（全文）
- `frontend/src/pages/WebShell/index.tsx`（grep 命中段）、`pages/Dashboard/PodCard.tsx`（grep 命中段）、`setupProxy.js`（全文）

PiForge（路径相对仓库根）：

- `docs/SANDBOX.md`（全文）、`docs/SANDBOX-DEPLOY.md`（全文）、`docs/STATUS.md`（FR-5.x 行）、`docs/PRD.md:382-391`
- `backend/internal/contracts/contracts.go:55-72`、`:510-566`
- `backend/internal/sandbox/docker.go:100-294`、`:715-1011`
- `backend/internal/sandbox/sandbox.go:1-70`（内含 provider 契约：TTL / Pause 必须快照）
- `backend/internal/store/schema.sql:55-90`、`:205-219`
- `backend/internal/httpapi/sandbox_handlers.go`（全文）
- `backend/internal/pi/reap.go`（全文）
- `backend/internal/provision/provision.go`（grep 命中段）
- `deploy/k8s.yaml`（全文）
- `git log --oneline -5`、`git status --short`（工作树干净，HEAD `129fb2e`）
