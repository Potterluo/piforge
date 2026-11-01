# 沙箱怎么部署：docker-compose 与 Kubernetes

`docs/SANDBOX.md` 回答的是"沙箱是什么、三种功能放在哪"。本文回答下一层问题：**控制面
凭什么创建沙箱，创建完之后浏览器、终端、文件面板怎么找到它**。

两个目标部署：单机 docker-compose，和（预期的）多租户 Kubernetes。两者都不是"把
`docker run` 换成 `kubectl run`"那么简单，因为现在的实现依赖六件只有"控制面与 Docker
daemon 同机、控制面是宿主进程"时才成立的事。先把这六件事摆出来，后面每一节都在拆它们。

## 0. 先固定事实：当前实现依赖的六件事

| # | 依赖 | 代码位置 | 换部署会怎样 |
|---|---|---|---|
| 1 | 控制面 PATH 上有 `docker` CLI | `sandbox/docker.go:120`（`exec.LookPath("docker")`） | `-sandbox docker` 仍然如此，而这个镜像里**没有 docker CLI**（`Dockerfile` runtime 阶段只装 `ca-certificates curl tini` + 可选 chromium），所以 compose 里开 `-sandbox docker` 会启动即失败。**已解决**：`-sandbox runner` 把这条依赖移到 runner 容器（§1.4），控制面既不需要 CLI 也不需要 daemon |
| 2 | 工作区是控制面本机的 bind mount 源 `<DataRoot>/<userID>/<projectID>` | `docker.go:152`、`docker.go:242` | daemon 在另一台机器时，源路径在**那台**机器上解析，控制面写自己磁盘，两边从此不是同一棵树。runner 与 daemon 同机、与控制面共享同一个数据卷、同一个路径（§1.4） |
| 3 | `RootDir` 是宿主路径，控制面用它跑宿主 git | `docker.go:519`、`provision.go:200`（`AddWorktree(RepoDir(sb))`）、`provision.go:243`（`materialize.Mount`） | Pod 里没有"宿主路径"这回事，`provision` 立刻失效。runner 模式下 `RootDir` 仍是共享卷上的路径，所以 compose 成立；K8s 仍要改 |
| 4 | 端点 = `docker port` 读回的随机端口，拼成 `http://127.0.0.1:<port>` | `docker.go:257`（`-p 127.0.0.1::8080`）、`docker.go:661` | 控制面在容器里时 `127.0.0.1` 是它自己的 loopback；K8s 里没有"发布端口"。**已解决**：`DockerConfig.ContainerEndpoint`（compose 里 `SANDBOX_CONTAINER_ENDPOINT=1`）改为容器名 `http://piforge-<hash8>:8080`，compose 与 K8s 形状一致 |
| 5 | 每个沙箱一个 `SANDBOX_API_KEY`，随沙箱行加密落库；`-sandbox-api-key` 只剩调试用的固定覆盖 | `sandbox/key.go`、`sandbox/docker.go`（`runContainer`/`containerAPIKey`）、`provision.go`（`ensureSandbox`） | **已解决**。runner 把端点变成容器名之后，端点不再是秘密，key 才是：每个容器 32 字节随机 key，项目 A 的 key 打项目 B 的容器是 401（§1.4 末与 `scripts/verify-runner-sandbox.ps1`） |
| 6 | 生产代码从不调用 `SandboxProvider.Exec`；沙箱内的命令走 AIO 的 `/v1/shell/exec` | `workspaceremote/exec.go:103`、`workspaceremote/files.go:362`；`Exec` 只被测试调用 | K8s 不需要 `pods/exec` 也能跑面板 git —— 这条是好消息，别把它当坏消息一起搬。runner 因此**不提供** exec 端点，`sandbox.Runner.Exec` 直接报错而不是假装有 |

还有一条容易被忽略、但决定了整套端点设计：

| 7 | 浏览器与终端的 WebSocket 是**用户 ↔ 控制面**，控制面自己连 CDP / AIO | `httpapi/browser_handlers.go:98-131`（`Browser.Ensure` 后用 `TabScreencast` 往 socket 写帧）、`term/remote.go` | 用户浏览器**不需要**直连沙箱。只有控制面需要能连到 8080 —— 这让 K8s 的 ClusterIP 方案成立，也让"给每个租户发一个 Ingress"变成纯粹的多余风险 |

## 与 `docs/SANDBOX.md` 的对齐情况

本文起草时，`docs/SANDBOX.md` 还写着 `/workspace` 绑定到数据目录（所有项目共享一棵树），
而代码已经按 `<DataRoot>/<userID>/<projectID>` 挂载。那处不一致在本文写完之前就落地了
（`c9c9dfb fix(sandbox): one docker workspace per project, not one shared tree`，同一个提交
也更新了 `docs/SANDBOX.md`），所以现在两份文档说的是同一件事：**一个 (user, project)
一个宿主目录、一个容器**。

本文不去重复那份文档已经讲清楚的东西（checkout 由 `gem` 创建、`ContainerPath` 的映射、
三个功能开关），只加它没讲的那一层：部署形态。K8s 下对应的粒度是 **一个 PVC + 一个 Pod**。

---

## 1. docker-compose：控制面要创建容器，就得有 daemon 的入口

先分清两个独立的问题，它们经常被混成一句"把 socket 挂进去"：

1. **二进制**：控制面需要 `docker` CLI（或改为直接调 Docker HTTP API）。镜像里没有。
2. **权限**：创建容器这件事本身，在 Docker 的模型里就是 daemon 的特权。

### 1.1 方案（i）：把 `/var/run/docker.sock` 挂进控制面

```yaml
services:
  piforge:
    image: piforge:dev
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock   # ← 这一行
      - ./data:/data
    environment:
      - PIFORGE_SANDBOX=docker
      - PIFORGE_SANDBOX_IMAGE=ghcr.io/agent-infra/sandbox:1.11.0
```

**安全后果，说清楚**：这不是"给了控制面一点容器权限"，而是**把宿主机的 root 交给了
控制面进程**。具体能做到的事，不需要任何内核漏洞：

- `docker run --privileged -v /:/host ...` 起一个挂了宿主根的容器，然后 `chroot /host`
  —— 宿主上读写任意文件；
- `docker inspect` 读**所有**容器的环境变量，包括 `PIFORGE_MASTER_KEY`、模型 provider 的
  key、数据库口令（同一个 daemon 上的别的服务也一样）；
- 停止、删除这台机器上任何容器（包括不是 PiForge 的）；
- 改 iptables、挂 `/etc`、动 systemd 单元（都可以通过一个特权容器完成）。

这条权限是**控制面进程**的，不是沙箱的。沙箱隔离做得再好也没用：攻击者不需要逃出沙箱，
他只要让控制面执行一次它本来就会执行的动作（`docker run`/`docker rm`），或者直接拿下
控制面（它是个 HTTP 服务，前面还挂着一个 LLM 网关）。

`socket-proxy` 自己的 README 第一句就是这个意思：给它 socket 等于给它宿主 root，代理只能
把能调的 API 收窄（[tecnativa/docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy)）。

**适用**：单机、单人、你自己就是唯一租户，且你接受"这台机器上没有别人"。

### 1.2 方案（ii）：socket 代理（tecnativa/docker-socket-proxy）

```yaml
services:
  docker-proxy:
    image: tecnativa/docker-socket-proxy
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    environment:
      CONTAINERS: 1     # 允许 /containers/*
      POST: 1           # 允许 POST（create/start/stop/rm 都要它）
      EXEC: 1           # 允许 exec
      IMAGES: 0
      VOLUMES: 0
      NETWORKS: 0
      SWARM: 0
    networks: [sandbox-control]

  piforge:
    environment:
      - DOCKER_HOST=tcp://docker-proxy:2375
    networks: [sandbox-control]
```

默认全拒，只有显式打开的路径前缀放行。它确实收窄了**API 面**：不能 build 镜像、不能动
网络和卷、不能读 swarm 密钥。

**但要说清楚它挡不住什么**：代理按"HTTP 方法 + 路径前缀"过滤，**不看请求体**。`POST
/containers/create` 一旦放行，请求体里的 `HostConfig.Privileged`、`Binds: ["/:/host"]`
一样会通过。也就是说：它能防"控制面被诱导去做了不该做的操作类型"，防不住"控制面被完全
控制"——后者仍然等价于宿主 root。

**适用**：你想让控制面只做"容器生命周期"这一件事，并且把它当作**减少误操作面**的措施，
而不是安全边界。别在文档里把它写成"我们已经解决了 socket 风险"。

### 1.3 方案（iii）：DinD sidecar（私有 daemon）

```yaml
services:
  dockerd:
    image: docker:dind
    privileged: true
    environment: [DOCKER_TLS_CERTDIR=]
    volumes: [dind-data:/var/lib/docker]
  piforge:
    environment: [DOCKER_HOST=tcp://dockerd:2375]
    volumes: [dind-data:/data/sandboxes]   # 必须和控制面共享，见下
```

控制面拿到一个**私有** daemon，沙箱是嵌套容器（容器里的容器）。好处是沙箱创建/删除不会
碰到宿主上的其它容器；坏处是：

- 外层容器必须 `--privileged`，而外层特权容器是比 runc 更宽的逃逸面 —— 你把"一个特权
  容器"换成了"每个沙箱外面套一个特权容器"；
- **bind mount 的源路径必须在这个私有 daemon 里存在**。控制面看到的 `/data/sandboxes`
  和 daemon 看到的是两个挂载命名空间，所以要挂同一个卷并在两边用同一个路径；否则就是
  "控制面写本地、沙箱看空目录"，而且不会有任何报错；
- `dockerd` 重启（或 DinD 的 `--live-restore` 没开）→ 所有沙箱容器消失，工作区只剩卷里
  那份；
- 镜像要拉两遍（宿主的和 daemon 的），GB 级镜像下这不是小钱。

**适用**：CI、演示、e2e —— 需要"干净、可丢弃"的 docker，而不是"多租户生产"。

### 1.4 方案（iv）：sandbox-runner 兄弟容器（**已交付**）

控制面完全不碰 Docker。`backend/cmd/sandbox-runner` 是唯一挂 socket 的进程，它对控制面
暴露**六个动作**，别的什么都不给。交付物：

- 服务：`backend/cmd/sandbox-runner` + `backend/internal/sandboxrunner`（HTTP 面与
  `sandbox.Docker` 适配器）；
- 控制面这一侧：`backend/internal/sandbox/runner.go`（`sandbox.Runner`，实现
  `contracts.SandboxProvider` 与 `contracts.WorktreeSandbox`）；
- compose：`docker-compose.runner.yml`（叠加在 `docker-compose.yml` 上），镜像
  `deploy/Dockerfile.runner`；
- 验证：`scripts/verify-runner-sandbox.ps1`。

契约（这就是全部）：

| 方法 | 路径 | 请求体 | 响应 | 对应 `SandboxProvider` |
|---|---|---|---|---|
| POST | `/v1/sandboxes` | `{user_id, project_id, image, repo_url, branch, api_port, api_key, cpus, memory, network, detached_workspace}` | `200` + record；同名容器已存在则复用 | `Ensure` |
| GET | `/v1/sandboxes/{id}` | — | `200` + record | `Get` |
| POST | `/v1/sandboxes/{id}/pause` | — | `204` | `Pause` |
| POST | `/v1/sandboxes/{id}/resume` | — | `200` + record | `Resume` |
| DELETE | `/v1/sandboxes/{id}` | — | `204`（删容器，**不删工作区**） | `Destroy` |
| POST | `/v1/sandboxes/{id}/worktrees` | `{repo_dir, path, branch}`（可选 `api_key`） | `204` | `AddWorktree` |

record 是 `{id, state, root_dir, container_path_root, workspace, endpoint, api_key?}`：`id` 仍然是
`piforge-<sha256(user/project) 前 8 字节>`，`root_dir` 是共享卷上
`<SANDBOX_DATA_ROOT>/<user>/<project>`（`detached_workspace=1` 时是容器路径 `/workspace`），
`container_path_root` 是 `/workspace`（控制面据此校验两侧对挂载点的一致理解），
`workspace` 是 `host` 或 `api`（阶段二：`api` 表示控制面只能经文件 API 访问工作区），
`endpoint` 是控制面可达的沙箱 API 地址，`api_key` **只在 create 的响应里出现一次**（见下）。

**没有第七个端点？2026-10-31 起有一个**：`POST /v1/sandboxes/{id}/attach`
（`{argv, dir, env, user}` → 一条 hijack 的双向字节流）。它不是给用户用的 shell：它是
`-agent-in-sandbox` 把 `pi --mode rpc` 的 stdio 接给控制面的通道，用同一个 bearer token，
参数是 argv 而不是 shell 字符串。仍然**没有** health、没有文件读写——这些继续走沙箱自己的
API。为什么不能用 `/v1/shell/exec` 代替它、以及这个端点的代价，见 §1.4.3 与
`docs/EXECUTION-BOUNDARY.md` §2/§9。

### 每个沙箱一个 key（替代部署级 `SANDBOX_API_KEY`）

端点从 `127.0.0.1:<随机端口>` 变成 `http://piforge-<hash8>:8080` 之后，"端点本身是秘密"
这层遮挡就没有了：同一个网络上的任何进程都能访问每个沙箱的 8080。所以 key 必须每沙箱一个：

1. **生成**：容器创建点（`sandbox.Docker.runContainer`）用 `crypto/rand` 取 32 字节、hex
   编码（64 字符）作为该容器的 `SANDBOX_API_KEY`；本地 provider 没有 API，不涉及。
   设置 `-sandbox-api-key` 时它作为**固定覆盖**原样使用（调试一个部署用，不是租户用）。
2. **上报**：runner 把生成结果放进 create 的 record（`api_key`），经
   `Authorization: Bearer <RUNNER_TOKEN>` 的内部通道**只返回一次**。选"在 runner 生成、
   上报一次"而不是"控制面生成、每次下发"，原因有二：控制面生成意味着密钥在每次 create
   时都上线，且 runner 仍无法回答"它接管的那个容器用的是哪个 key"（runner 无状态）；
   在创建点生成则密钥只向上走一次。
3. **落库**：控制面把 key 交给 `store.CreateSandbox`，用部署已有的 AES-GCM secret box
   （`store/secrets.go`，与 MCP 凭据同一套密钥来源 `PIFORGE_SECRET_KEY` 或库旁 0600 文件）
   加密进 `sandboxes.api_key_cipher`。它**不在** `sandboxCols` 里，只能经
   `store.SandboxAPIKey` 读出；`contracts.Sandbox.SandboxAPIKey` 是 `json:"-"`，任何
   HTTP 响应、日志行、错误串都拿不到它。
4. **使用**：文件面板、终端、浏览器、分支 worktree 每次调用都按 sandbox id 解析 key
   （`cmd/piforged/sandbox_keys.go`），放进 `X-AIO-API-Key` 头，**从不放进 URL**。
   `POST …/worktrees` 的可选 `api_key` 仍然接受固定覆盖；为空时 runner 自己从容器环境
   读回（`docker inspect .Config.Env`），这正是它保持无状态的方式。
5. **旧行**：没有 key 的行（升级前创建）在 `-sandbox-api-key` 存在时回退到它，否则明确
   报错并让调用失败——**不会**退回"不带 key 直接调"。旧的、没有 `SANDBOX_API_KEY` 的
   容器在 `Ensure` 时会被重建（工作区是 bind mount，文件不丢）。
6. **仍未做**：resume 时不轮换 key（容器重启用的还是创建时那个）；没有 key 轮换接口，
   没有按会话分发/撤销。要轮换只能重建沙箱。

**鉴权**：`Authorization: Bearer <RUNNER_TOKEN>`，控制面与 runner 两侧都必需
（runner 没有 token 直接拒绝启动）。比较在 SHA-256 摘要上做
`subtle.ConstantTimeCompare`——先摘要再比较，长度不泄漏；缺失、错 scheme、错 token 一律
`401`，且在解析请求体之前拒掉。状态映射是契约的一部分：`sandbox.ErrNotFound → 404`、
`ErrInvalidID`/坏 body → `400`、handler 超时 → `504`、其余 → `500`。

**它复用的是同一份实现，不是抄了一份**：`internal/sandboxrunner` 的 `DockerProvider`
直接调用 `sandbox.Docker`（`NewDocker` 建容器，`NewDockerAPI` 只访问已有容器的 API）。
每个请求按"请求值 > runner 默认值"合并出一个 `DockerConfig` 再调用，所以 per-pair 目录、
挂载校验、走沙箱 API 的 checkout、`-u gem`、`safe.directory`、不发 `exit` 这些规则只有
一份。runner 因此是无状态的：容器名由 (user, project) 派生，重启 runner 不影响已有沙箱。

compose 形状（`docker-compose.runner.yml` 的核心，完整文件带注释）：

```yaml
services:
  piforge:
    environment:
      PIFORGE_SANDBOX: runner
      PIFORGE_SANDBOX_RUNNER_URL: http://sandbox-runner:7801
      PIFORGE_SANDBOX_RUNNER_TOKEN: ${SANDBOX_RUNNER_TOKEN:?}
    volumes: [piforge-data:/data]
    networks: [default, sandbox-control, piforge-sandboxes]

  sandbox-runner:
    build: {context: ., dockerfile: deploy/Dockerfile.runner}
    environment:
      RUNNER_TOKEN: ${SANDBOX_RUNNER_TOKEN:?}
      SANDBOX_RUNNER_ADDR: 0.0.0.0:7801
      SANDBOX_DATA_ROOT: /data/sandboxes
      SANDBOX_NETWORK: piforge-sandboxes
      SANDBOX_CONTAINER_ENDPOINT: "1"
      SANDBOX_WORKSPACE_VOLUME: piforge-data        # ← 见下面第 2 条
      SANDBOX_WORKSPACE_VOLUME_ROOT: /data          # ← 见下面第 2 条
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - piforge-data:/data
    networks: [sandbox-control, piforge-sandboxes]

volumes:
  piforge-data:
    name: piforge-data
```

四个必须一起成立的细节：

1. **socket 只给 runner，而且不写 `:ro`**。socket 的只读挂载不限制通过它发起的 API
   调用，写成 `:ro` 只会制造"已经限制住了"的错觉。（早先这一节写的是 `:ro`，那是错的：
   它既没挡住什么，又让人以为挡住了。）
2. **数据卷两边同一个路径，而且沙箱从"卷"里挂，不从路径里挂**。两个服务都
   `piforge-data:/data`，`SANDBOX_DATA_ROOT=/data/sandboxes`，控制面 `-data /data`
   推出的 sandbox 根也是 `/data/sandboxes` —— 这只解决了三边命名一致。**bind mount 的源
   路径是 daemon 在它自己的宿主上解析的**，而 `/data` 只在这两个容器的挂载命名空间里存在：
   `-v /data/sandboxes/<u>/<p>` 会让 daemon 在它自己的根上静默建一个空目录（Docker Desktop
   上是它的 VM），沙箱于是看到一个空 `/workspace`，控制面继续往卷里写。两边**名字相同**，
   所以控制面那条 `root_dir` 比对也发现不了。
   所以沙箱容器挂的是**卷本身**加子路径：
   `--mount type=volume,source=piforge-data,target=/workspace,volume-subpath=sandboxes/<u>/<p>`
   （`SANDBOX_WORKSPACE_VOLUME` = 卷名，`SANDBOX_WORKSPACE_VOLUME_ROOT` = 这个卷在本进程里
   的挂载点，用来把 `/data/sandboxes/<u>/<p>` 换算成卷内相对路径）。daemon 自己解析卷，
   宿主路径完全不出现在这条链路上；Linux 宿主和 Docker Desktop 上是同一份配置。
   代价是一个硬性版本要求：**Engine ≥ 25（API 1.45）才有 `volume-subpath`**，更老的引擎
   会在 `docker run` 时直接报错（不是静默降级）。
   **阶段二之后这一条不是唯一形状**：`-agent-in-sandbox` 打开时沙箱挂的是它**自己的**卷
   `piforge-ws-<pair hash>`（无 subpath），控制面根本不挂工作区，于是
   `SANDBOX_WORKSPACE_VOLUME` / `SANDBOX_WORKSPACE_VOLUME_ROOT` 都不再需要（§1.4.4）。
   两个形状不能同时要求：`DetachedWorkspace` 与 `WorkspaceVolume` 在构造时互斥。
3. **不发布沙箱端口**。`SANDBOX_CONTAINER_ENDPOINT=1` 让 runner 返回
   `http://piforge-<hash8>:8080`（容器名 + Docker DNS），控制面加入 `piforge-sandboxes`
   就能连。runner 也必须在这个网络上：它自己做 checkout 时要访问这个地址。这一条同时
   去掉了宿主上的 8080 暴露面。
4. **7801 不发布**，只放在 `sandbox-control` 上。控制面与 runner 之间没有 TLS——token 是
   唯一的认证，别把这个端口发到宿主或跨主机直连（§7 的"还没做"）。

还有一条是镜像的事，但在部署里必须显式给：**容器里的 `/workspace` 要让沙箱用户能写**。
AIO 镜像的 `gem.sh` 对它自己的 `$WORKSPACE` 做 `mkdir -p` + `chown $USER:$USER`，而
`$WORKSPACE` 默认是 `/home/gem`；PiForge 把项目挂在 `/workspace`，不告诉镜像的话那个
目录保持 root:root 0755，第一次 `mkdir /workspace/repo` 就是
`Permission denied`。所以 `containerArgs` 固定带 `-e WORKSPACE=/workspace`，让镜像按它自己
的契约去 chown。Windows 上的宿主 bind mount 会忽略属主，所以这条在单机 bind 形态下一直是
"看不见的 bug"；换成卷（或任何 Linux 宿主）就当场暴露 —— 这一节下面有实测记录。

旧的 `-sandbox docker` 逐字节不变：不设 network 时仍然 `-p 127.0.0.1:<随机端口>` 并从
`docker port` 读回（额外多了 `-e WORKSPACE=/workspace`）。`-sandbox runner` 与它共用
`resolveSandboxPlacement`（三个面板默认 `remote`，显式 `host` 仍然允许并告警），而
`-sandbox runner` 缺 URL/token、或 `-sandbox local`/`docker` 却给了 runner 参数，都是启动
错误。

### 1.4.1 实测：这套 compose 在 Docker Desktop 上真的跑起来了吗

**2026-10-29 之前，这一节只有 `docker compose config` 的验证**：镜像没 build 过，
`compose up` 没跑过。下面是实际跑一遍的记录（Windows 11 + Docker Desktop 4.97.0，
Engine 29.9.2，Linux 容器），包括第一次跑时坏掉的三处。

**跑的命令**（仓库根目录；`.env` 见下）：

```powershell
docker build -f deploy/Dockerfile.runner -t piforge/sandbox-runner:dev .
pwsh scripts/build-app-image.ps1 -Tag piforge:verify      # 控制面镜像：规范命令就是脚本（约 90s，比完整 Dockerfile 快）
docker compose -f docker-compose.yml -f docker-compose.runner.yml up -d
docker compose -f docker-compose.yml -f docker-compose.runner.yml ps
```

控制面镜像只有一个规范构建方式：`scripts/build-app-image.ps1`（`-Tag` 决定 tag）。
底层的完整 `docker build --build-arg INSTALL_CHROMIUM=true -t <tag> .` 只在基础镜像
不可用时才用；`DELIVERY-DRILL.md` §5.1 与 `deploy/README.md` 指向的是同一个脚本。
注意 `-Tag` 会决定 compose 里的 `PIFORGE_IMAGE`：上面这次是 `piforge:verify`，
而 `pf-local` 用的是 `piforge:local`，两者是同一脚本的不同 tag，不是两套构建。

`.env`（compose 自动读取，已在 `.gitignore` 里；两个 `:?` 变量必须有值）：

```dotenv
SANDBOX_RUNNER_TOKEN=<64 位随机串>
PIFORGE_SANDBOX_IMAGE=enterprise-public-cn-beijing.cr.volces.com/vefaas-public/all-in-one-sandbox:1.11.0
PIFORGE_IMAGE=piforge:verify
PIFORGE_RUNNER_IMAGE=piforge/sandbox-runner:dev
```

`docker compose ps` 起来之后：

```
NAME                     IMAGE                        SERVICE          STATUS                    PORTS
piforge                  piforge:verify               piforge           Up (healthy)              0.0.0.0:7700->7700/tcp
piforge-sandbox-runner   piforge/sandbox-runner:dev   sandbox-runner    Up
```

**第一次按原样跑，坏在这些地方**（每条都是实测输出，不是猜的）：

| # | 现象 | 原因 | 修法 |
|---|---|---|---|
| 1 | `POST /v1/sandboxes` 500：`sandbox: docker: prepare /workspace/repo: exit 1: mkdir: cannot create directory '/workspace/repo': Permission denied` | `/workspace` 挂进去是 root:root，沙箱用户是 `gem`（uid 1000）。镜像只 chown 它自己的 `$WORKSPACE`（默认 `/home/gem`） | `containerArgs` 加 `-e WORKSPACE=/workspace` |
| 2 | 修完 1 之后：checkout 成功但沙箱 `/workspace` 是**空**的，控制面写的文件沙箱看不见 | bind mount 源路径由 daemon 在**它的**宿主上解析；`/data/...` 只在这两个容器里存在，daemon 静默建了个空目录 | 沙箱改用 `--mount type=volume,...,volume-subpath=...`（见上第 2 条） |
| 3 | `docker run` 报 `cannot access path /var/lib/docker/volumes/piforge-data/_data/usr_.../prj_...: lstat ...: no such file or directory` | `volume-subpath` 是**相对卷根**的，不是相对 `SANDBOX_DATA_ROOT`；我第一版写成 `usr_.../prj_...`，少了 `sandboxes/` | `SANDBOX_WORKSPACE_VOLUME_ROOT=/data`，provider 用 `filepath.Rel` 算出 `sandboxes/<u>/<p>` |
| 4 | `docker compose down -v` 报 `Volume piforge-data Resource is still in use`，卷删不掉 | 第 3 条那次失败的 `docker run` 留下一个 `Created` 状态的容器，下次 `Ensure` 会认领它并重试 `docker start`，永远失败，还一直占着卷 | `Ensure` 在 `runContainer` 失败时 `docker rm -f` 掉这次半创建的对象 |
| 5 | 每次启动都有 `[WARN tini] Tini is not running as PID 1 ...` | 镜像 `ENTRYPOINT` 已经是 tini，`docker-compose.yml` 的 `init: true` 又在它前面加了一个 init，所以里面的 tini 不是 PID 1 | 删掉 `docker-compose.yml` 的 `init: true`（只留镜像里那个） |

**验证方式**：全部走部署出来的栈（`http://127.0.0.1:7700`，不跑本地二进制），
`PIFORGE_SEED_*` 用 base compose 的默认值（`admin@piforge.local` / `piforge-admin-123`）。
脚本化的一遍在 `.real/verify.ps1`（不进仓库），核心步骤与结果：

> **种子密码从哪里来。** 首次启动只创建账号，日志会写
> `created the first admin account email=…` / `created the demo account email=…`，
> **不会打印密码**（容器的 `docker logs` 经常被贴进工单/CI，打印一次就是泄露一次）。
> 密码由 `PIFORGE_SEED_PASSWORD` / `PIFORGE_SEED_USER_PASSWORD` 决定；base compose 给了
> 默认值，所以本地栈仍是 `admin@piforge.local` / `piforge-admin-123` 与
> `dev@piforge.local` / `piforge-dev-123`。如果部署时把这两个变量留空，store 会**生成**
> 随机密码且不写日志。若部署里还有另一个管理员，可从「用户管理 → 重置密码」拿回访问权；
> 若这是唯一的管理员，恢复方式只有删掉数据卷、设好变量后重新首次启动（会话与项目会一起丢失）。
> 生产不要依赖默认值，也不要在首次启动后才补变量。

```powershell
# 登录 → 建项目 → 建会话 → 发一条消息（provisioning 在这一步发生）
$r = Invoke-RestMethod -Method Post -Uri http://127.0.0.1:7700/v1/auth/login -SessionVariable s `
      -ContentType application/json -Body '{"email":"admin@piforge.local","password":"piforge-admin-123"}'
```

| 验证 | 结果 |
|---|---|
| runner 日志说容器是它建的 | `msg="sandbox container is up" container=piforge-f986b66f3f575804 ... root_dir=/data/sandboxes/<user>/<proj> endpoint=http://piforge-f986b66f3f575804:8080` |
| 容器在，无宿主端口 | `docker ps` 有 `piforge-<hash16>`；`docker port` 输出为空；`ports=[8080/tcp]` 只是 EXPOSE |
| 两边同一个数据目录 | piforged 里 `/data/sandboxes/<u>/<p>/repo/payload.txt` 与沙箱里 `/workspace/repo/payload.txt` inode 相同（`331248 == 331248`） |
| `PUT /files` 后沙箱里字节一致 | `PUT /files?path=payload.txt -> 200`；沙箱里 `od -An -tx1` 的十六进制与写入完全相同（48 字节） |
| git status/stage/commit | 全部 `200`；`git log -1 --format=%ae` = `sandbox@piforge.local`（提交身份是容器用户 `PiForge Sandbox <sandbox@piforge.local>`） |
| 浏览器截图是 PNG | `POST /browser/screenshot -> 201 {"bytes":29598,"path":"screenshots/20261029-085333.png"}`；沙箱里 `head -c 8` = `89 50 4e 47 0d 0a 1a 0a` |
| 终端 WS 的 hostname | 用仓库自己的工具（见下）：`CTRL: {..."shell":"aio","type":"ready"}`，`OUTPUT: "hostname\r\na59aa7b1df2c\n"`；`docker exec <container> hostname` 也是 `a59aa7b1df2c` |
| 控制面没有 socket | `docker inspect piforge` 的 `.Mounts` 只有 `piforge-data -> /data`；容器里 `ls /var/run/docker.sock` → `No such file or directory`；`command -v docker` → 空（`exit 127`） |
| socket 只在 runner | `docker inspect piforge-sandbox-runner` 有 `/var/run/docker.sock`；runner 里 `docker version` 能拿到 Server 29.9.2 |
| 按容器名可达（`SANDBOX_CONTAINER_ENDPOINT` 这条路） | 在 `piforge` 容器里：`getent hosts piforge-f986b66f3f575804` → `172.24.0.4`；`curl http://piforge-<hash>:8080/v1/browser/info` 不带 key → `401`，带该沙箱的 key → `200` |

终端这一步的原始命令（`pf_session` 从登录响应里取，见下面的 `Invoke-RestMethod` 那行）：

```powershell
cd backend
go run ./tools/wsprobe -base ws://127.0.0.1:7700 -session <session id> `
    -cookie <pf_session> -cmd "hostname" -grace 8s
# CTRL: {"end":0,"pid":0,"shell":"aio","start":0,"tab":"...:probe","type":"ready"}
# BYTES: 23
# OUTPUT: "hostname\r\na59aa7b1df2c\n"
```

这个工具在这次验证里也修了两处：`-cmd` 之后是等一个 `-grace` 窗口再把输出打出来（原来
"输出里出现命令的最后一个词就停"，而终端会把命令本身回显一遍，于是永远停在回显上、看不到
答案）；发给沙箱的行是 `\n` 而不是 `\r`（AIO 的行式会话认 `\n`，`\r` 只会被回显、不执行）。

**Docker Desktop / Windows 的注意事项**（都是这台机器上的实测结论）：

1. **socket 能用，但它是 Desktop 的代理**。compose 里写的是
   `/var/run/docker.sock:/var/run/docker.sock`，Desktop 会把它接到自己的
   `docker.proxy.sock` 上；容器里 `docker version` 正常（Server 29.9.2），`docker run`
   也真的在 Desktop 的 Linux VM 里起容器。**不需要** `DOCKER_HOST=tcp://host.docker.internal:2375`，
   也不该悄悄换成它：那会绕过 Desktop 的 socket 代理，并把一个没有 TLS 的 daemon 端口暴露出去。
2. **不需要、也不应该尝试"把宿主路径传给沙箱"**。`/run/desktop/mnt/host/c/...` 在这台机器上
   确实能被 daemon 解析（实测可行），但那是 Docker Desktop 的内部路径，换一台机器/一个
   版本就可能变；卷 + `volume-subpath` 在 Linux 宿主和 Desktop 上是同一条路径，所以选它。
3. **`docker compose down -v` 不一定能删掉卷**。沙箱容器不归 compose 管（§1.5），只要还有
   一个 `piforge-*` 容器（哪怕是 `Created`/`Exited`）挂着这个卷，compose 就只会打印
   `Volume piforge-data Resource is still in use` 然后继续，退出码仍是 0。清理顺序是
   `docker rm -f piforge-*`（只删你认识的）再 `docker compose ... down -v`。
4. **`container_name` 会撞车**。两份 compose 都写死 `container_name: piforge`：先起过单容器
   部署、再起叠加部署（或反过来），第二个 `up` 会直接
   `Conflict. The container name "/piforge" is already in use`。切换前先 `down`。
5. **镜像 tag 要自己给**。`PIFORGE_IMAGE`/`PIFORGE_RUNNER_IMAGE` 不给就会用
   `piforge:dev` / `piforge/sandbox-runner:dev`，而 `docker compose up -d` 在没有
   `--build` 时**不会**替你 build。第一次跑记得先 build（上面的命令），或者加 `--build`。

**这台机器上仍然没有验证的**（不要当成已验证）：

- **Linux 宿主机**。这份文档里的命令只在 Docker Desktop（Windows）上跑过；卷 + subpath 的
  行为在 Linux 上应当一致（daemon 就在本机，`-v` 那条也一样成立），但没有第二台机器实测。
  特别地，单机 bind 形态（不带 `SANDBOX_WORKSPACE_VOLUME`）在 Linux 宿主上会因为第 1 条
  的属主问题需要 `-e WORKSPACE=/workspace`——这条已经加到 `containerArgs`，但同样只在
  Windows 上跑过。
- **K8s**：§2 全是设计，没有任何一个对象在这台机器上 apply 过。
- **升级路径**：从没有 `SANDBOX_API_KEY` 的旧容器升级（`Ensure` 会重建）没有实测，
  只有单元测试覆盖。

### 1.4.2 已验证：runner 提供方的端到端（2026-10-31）

上面 §1.4.1 记录的是"compose 起得来"。这一节记录的是**控制面 + runner + 沙箱跑完一次真实
的 agent 任务**：Windows 11 / Docker Desktop / Engine 29.9.2，`pf-local`（7722，`-sandbox local`）
全程没有被触碰，另起一套一次性实例在 **7723**，`-sandbox runner`，`--cap-drop DAC_OVERRIDE`
（与文档默认一致），工作区是**卷 + 子路径**（§1.4 第 2 条），沙箱不发布宿主端口。

**形状**（`docker inspect` 实测）：

```
pf-drill-control   CapDrop=["CAP_DAC_OVERRIDE"] CapAdd=null  7723->7700
                   /var/run/docker.sock: No such file or directory; command -v docker -> 无
pf-drill-runner    /run/host-services/docker.proxy.sock->/var/run/docker.sock, pf-drill-data:/data
piforge-b04d0a79…  8080/tcp（EXPOSE，无宿主端口）  Up (healthy)
```

启动日志（控制面）：

```
msg="sandbox placement resolved" sandbox=runner browser=remote terminal=remote files=remote
msg="skill mounts use read-only copies" mechanism=copy-ro reason="no CAP_SYS_ADMIN and no CAP_DAC_OVERRIDE: ..."
msg="sandbox lifecycle runs through the runner" url=http://pf-drill-runner:7801
msg="workspace files are read and written through the sandbox" sandbox=runner
msg="session liveness: container agents run under a heartbeat launcher" sandbox=runner beat=15s
msg="session liveness: the in-process registry and the sandbox heartbeat marker" ttl=2m0s
msg="sandbox API keys are per sandbox" sandbox=runner
```

#### 命令（按顺序，可整段执行）

```powershell
# 0. provider key：从凭据文件读入进程变量，绝不打印、绝不落盘
$cred = Get-Content (Join-Path $env:USERPROFILE '.dsh\.credentials.yaml') -Raw
$env:PIFORGE_DRILL_KEY = [regex]::Match($cred, '(?m)^\s*VOLCE_API_KEY:\s*(\S+)\s*$').Groups[1].Value
$volc  = 'https://api.example.com/v1'   # 本机 pf-local 里存的同一个 volc 端点
$image = 'enterprise-public-cn-beijing.cr.volces.com/vefaas-public/all-in-one-sandbox:1.11.0'
$tok   = -join (1..64 | ForEach-Object { '{0:x}' -f (Get-Random -Maximum 16) })   # runner 的 bearer token

# 1. 两个镜像（控制面用规范脚本；runner 用它的 Dockerfile）
pwsh scripts/build-app-image.ps1 -Tag piforge:runner-drill
docker build -f deploy/Dockerfile.runner -t piforge/sandbox-runner:drill .

# 2. 一次性网络 + 卷（沙箱从"卷 + 子路径"挂工作区，宿主路径不进这条链路）
docker network create pf-drill-net
docker volume create pf-drill-data

# 3. runner：唯一持有 socket 的进程，六个动作，无 exec/无文件端点
docker run -d --name pf-drill-runner --network pf-drill-net `
  -e "RUNNER_TOKEN=$tok" -e SANDBOX_RUNNER_ADDR=0.0.0.0:7801 `
  -e SANDBOX_DATA_ROOT=/data/sandboxes -e SANDBOX_NETWORK=pf-drill-net `
  -e SANDBOX_CONTAINER_ENDPOINT=1 `
  -e SANDBOX_WORKSPACE_VOLUME=pf-drill-data -e SANDBOX_WORKSPACE_VOLUME_ROOT=/data `
  -e "SANDBOX_IMAGE=$image" `
  -v /var/run/docker.sock:/var/run/docker.sock -v pf-drill-data:/data `
  piforge/sandbox-runner:drill

# 4. 第二个 PiForge 实例（7723），不动 7722 上的 pf-local
docker run -d --name pf-drill-control --network pf-drill-net -p 7723:7700 `
  -v pf-drill-data:/data --cap-drop DAC_OVERRIDE `
  -e PIFORGE_PORT=7700 -e PIFORGE_DATA=/data `
  -e PIFORGE_GATEWAY_URL=http://pf-drill-control:7700 `
  -e PIFORGE_SEED_EMAIL=admin@piforge.local -e PIFORGE_SEED_PASSWORD=piforge-admin-123 `
  -e PIFORGE_SEED_USER_EMAIL=dev@piforge.local -e PIFORGE_SEED_USER_PASSWORD=piforge-dev-123 `
  -e PIFORGE_DEFAULT_PROVIDER=volc `
  -e "PIFORGE_BOOTSTRAP_PROVIDER=volc|$volc|deepseek-v4-flash" `
  -e "PIFORGE_BOOTSTRAP_PROVIDER_KEY=$env:PIFORGE_DRILL_KEY" `
  -e PIFORGE_SANDBOX=runner `
  -e PIFORGE_SANDBOX_RUNNER_URL=http://pf-drill-runner:7801 `
  -e "PIFORGE_SANDBOX_RUNNER_TOKEN=$tok" -e "PIFORGE_SANDBOX_IMAGE=$image" `
  piforge:runner-drill

# 5. 登录 → 建项目 → 建会话（会话创建时 Ensure 才真的建容器与 checkout）
$base = 'http://127.0.0.1:7723'
$web  = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
$login = Invoke-WebRequest "$base/v1/auth/login" -Method Post -SessionVariable web `
  -ContentType application/json -NoProxy `
  -Body ([Text.Encoding]::UTF8.GetBytes('{"email":"admin@piforge.local","password":"piforge-admin-123"}'))
$csrf = ($login.Content | ConvertFrom-Json).csrf
$proj = Invoke-WebRequest "$base/v1/projects" -Method Post -WebSession $web -NoProxy `
  -Headers @{'X-PF-CSRF'=$csrf} -ContentType application/json `
  -Body ([Text.Encoding]::UTF8.GetBytes('{"name":"runner-drill","default_branch":"main"}'))
$pid2 = ($proj.Content | ConvertFrom-Json).project.id
$sess = Invoke-WebRequest "$base/v1/projects/$pid2/sessions" -Method Post -WebSession $web -NoProxy `
  -Headers @{'X-PF-CSRF'=$csrf} -ContentType application/json `
  -Body ([Text.Encoding]::UTF8.GetBytes('{"kind":"main","title":"prime drill"}'))
$sid  = ($sess.Content | ConvertFrom-Json).session.id

# 6. 发那条任务，等 run.settled（status.running 由 true 变 false）
$body = @{ text = '写一个 Node 脚本输出前 10 个素数，配一个测试并运行，然后提交'; client_msg_id = [guid]::NewGuid().ToString() }
Invoke-WebRequest "$base/v1/sessions/$sid/messages" -Method Post -WebSession $web -NoProxy `
  -Headers @{'X-PF-CSRF'=$csrf} -ContentType application/json `
  -Body ([Text.Encoding]::UTF8.GetBytes(($body | ConvertTo-Json -Compress)))
# 之后每 1s GET /v1/sessions/$sid/status，直到 running 变成 false

# 7. 终止：只删这套一次性实例（不要动 pf-local / piforge / piforge-data）
docker rm -f pf-drill-control pf-drill-runner
# 沙箱容器名是 piforge-<sha256(user/project) 前 8 字节>，只删这一种名字的
docker ps -aq --filter 'name=piforge-' | ForEach-Object {
  $n = (docker inspect --format '{{.Name}}' $_).TrimStart('/')
  if ($n -match '^piforge-[0-9a-f]+$') { docker rm -f $n }
}
docker volume rm pf-drill-data
docker network rm pf-drill-net
docker rmi piforge:runner-drill piforge/sandbox-runner:drill   # 镜像可留，留着就跳过这一行
```

停止 runner 而保留沙箱（容器由 runner 创建、不归 compose 管）：
`docker stop pf-drill-runner`；之后重启 `docker start pf-drill-runner` 不影响已有容器（runner 无状态），
端点由容器名 + `SANDBOX_CONTAINER_ENDPOINT=1` 决定，不需要重新发布端口。

#### 时间（实测，热镜像）

| 步骤 | 耗时 |
|---|---|
| 登录 `POST /v1/auth/login` | **74 ms** |
| 建项目 `POST /v1/projects` | **4 ms** |
| 建会话（含 `Ensure`：建容器 3960 ms + 容器内 checkout） | **4052 ms** |
| 发消息 → `{accepted:true,disposition:"started"}` | **255 ms** |
| 该 run `run.started` → `run.settled`（写脚本 + 写测试 + 跑 + 提交） | **18 452 ms** |
| 面板文件树 / 读 / 写 | 1569 ms（第一次） / 8 ms / 7 ms |
| 变更面板 status / diff / stage / commit | 876 / 958 / 309 / 1305 ms |
| 行内评论被接受 | **61 ms** |
| 只重启控制面（`docker restart`，不碰容器）→ `/healthz` 200 | **1021 ms** |

#### 隔离与真实执行的证据（每条都是命令 + 原始输出）

1. **三台不同的机器**，宿主没有这些路径：

   ```
   host=<host-name>  control=aa5c3b01f5b9  sandbox=eb386c352f77
   docker exec <sandbox> hostname -> eb386c352f77
   Test-Path C:\data\sandboxes -> False
   ```

2. **同一棵树、两个容器、同一个 inode**（工作区是卷，不是宿主目录）：

   ```
   sandbox   /workspace/repo            -> 2096:456006
   control   /data/sandboxes/<u>/<p>/repo -> 2096:456006
   ```

3. **agent 进程在哪**（这一条是这一轮最重要的诚实结论）：

   ```
   docker exec pf-drill-control ps -ef
   root  11  8  /app/bin/piforged -addr 0.0.0.0:7700 ...
   root  37 11  /app/bin/piforged agent-launcher -root /data/sandboxes/<u>/<p> \
                 -session ses_… -interval 15s -- pi --mode rpc --model piforge/deepseek-v4-flash \
                 --skill /data/sandboxes/<u>/<p>/repo/.pi/skills --session /tmp/.piforge/sessions/ses_….jsonl
   root  45 37  pi
   docker exec <sandbox> ps -ef   -> gem.sh / supervisord / python-server / code-server / jupyter，**没有 pi**
   ```

   也就是说：**agent 进程跑在控制面容器里，和沙箱共用同一个工作区卷**。pi 的 cwd 是
   `/data/sandboxes/<u>/<p>/repo`（控制面的视图），不是 `/workspace/repo`。这一点在下面
   "还没修"一节里说清后果。

4. **提交在沙箱里，作者是沙箱 checkout 的身份**：

   ```
   docker exec <sandbox> sh -c 'cd /workspace/repo && git -c safe.directory=/workspace/repo log --format="%h %an|%ae %s" -5'
   6c87b48 PiForge Sandbox|sandbox@piforge.local drill: a second commit from the changes panel
   1431a7b PiForge Sandbox|sandbox@piforge.local docs: 说明 2 为何在偶数检查前特判
   af50057 PiForge Sandbox|sandbox@piforge.local drill: a commit from the changes panel
   453c7c5 PiForge Sandbox|sandbox@piforge.local feat: 输出前 10 个素数的 Node 脚本及测试
   c8eb55f PiForge Sandbox|sandbox@piforge.local piforge: initial commit
   ```

5. **agent 写的文件**：Files API 与容器一致，宿主项目树里没有它的字节。

   ```
   GET /v1/sessions/<sid>/files?path=primes.js -> 200, size=800
   docker exec <sandbox> sha256sum /workspace/repo/primes.js -> 758f11c13fa7…
   C:\Users\<user>\Downloads\piforge 下 12 个 800 字节的 .js，sha256 与它相同的：0 个
   ```

6. **沙箱里没有 provider key**（key 只在进程变量里，输出只给了命中数）：

   ```
   docker exec <sandbox> env | Select-String <key>         -> 0 hits
   find / -name models.json  (in the sandbox)              -> 无（沙箱里根本没有 models.json）
   控制面 /data/pi-agents/*/models.json 里的 apiKey        -> "apiKey": "<per-session token>"
   grep -c <key> /data/pi-agents/*/models.json             -> 0
   docker inspect pf-drill-control env                     -> 1 hit（PIFORGE_BOOTSTRAP_PROVIDER_KEY，控制面持有，按设计）
   ```

7. **每沙箱一把 key，跨租户 401**（直接打容器，不经过控制面）：

   ```
   A ...1051d5d5  B ...cb79648a  (两把不同的 64 位 hex)
   GET http://B:8080/v1/browser/info  X-AIO-API-Key: <A 的 key> -> 401
   同一条请求用 B 自己的 key                                      -> 200
   不带 key                                                       -> 401
   ```

8. **终端在 UI 里就是容器 shell**（`backend/tools/wsprobe`，与浏览器面板同一个 WebSocket）：

   ```
   go run ./tools/wsprobe -base ws://127.0.0.1:7723 -session <sid> -cookie <pf_session> `
       -cmd "hostname; pwd; stat -c '%U %a' /workspace/repo" -grace 10s
   CTRL: {"shell":"aio","type":"ready",...}
   OUTPUT: "hostname; pwd; stat -c '%U %a' /workspace/repo\r\neb386c352f77\n/workspace/repo\ngem 777\n"
   ```

9. **测试面板与行内评论**：agent 用 `npm test` 跑了 3 个用例（`status=passed 3/3, 34 ms,
   source=agent`），记录在 `GET /v1/sessions/<sid>/test-runs`；行内评论提交
   `{path:"primes.js",line:12,side:"new",hunk:"@@ -10,6 +10,7 @@",quote:[...],body:"..."}`
   被接受（61 ms），服务端组装的那一轮原文：

   ```
   行内评论 · primes.js:12 | diff 片段：@@ -10,6 +10,7 @@ | 引用的代码： … | 评论：… |
   位置：这段代码现在在第 9 行（写评论后文件改过，行号有偏移），以当前文件为准。
   ```

   agent 据此改代码并提交 `1431a7b`，测试仍 3/3。

#### 这一轮修掉的两个缺陷（都带测试，都在主线上）

**D1 — 文档默认的 compose 根本建不出 checkout。** 沙箱里的 checkout 由 `gem`（uid 1000）创建，
而控制面以 root 运行、`--cap-drop DAC_OVERRIDE`（这是让 `copy-ro` 技能只读成立的那一条）。
两个身份写同一棵树，于是每个新会话的 provisioning 都死在第一次写：

```
provision: mount tooling: materialize: create skills root:
mkdir /data/sandboxes/<u>/<p>/repo/.pi: permission denied
liveness: create the marker directory ... mkdir /data/sandboxes/<u>/<p>/.piforge: permission denied
```

（同一路径用 `docker run --cap-add DAC_OVERRIDE` 可以写：我用这条对照实验确认了原因是
capability，不是挂载。）修法：`planCheckout` / `planBranchWorktree` 在**创建 checkout 的那一次**
结尾多一步 `chmod -R a+rwX /workspace`（分支会话是对 worktree 目录），**只在创建时**跑——
放到 ready 路径上会连 `materialize` 写的 0444/0555 技能副本一起放宽，那正是只读保证本身。
`chmod` 不是 git，所以 `checkoutCommand` 只对 `git` 注入 `-c safe.directory=<dir>`：
否则那串会被 chmod 当成 mode（`chmod: invalid mode: 'safe.directory=…'`）。
测试：`TestPlanCheckout`、`TestPlanBranchWorktree`、`TestDockerProviderCheckoutUsesTheSandboxAPI`
（钉住 `'chmod' '-R' 'a+rwX' '/workspace'` 这条命令与 `cwd`）。

**D2 — agent 的 `git commit` 被 "dubious ownership" 挡住。** checkout 归 `gem`，agent 进程是
root，git 2.39 在跑任何命令之前就拒绝这个仓库：

```
fatal: detected dubious ownership in repository at '/data/sandboxes/<u>/<p>/repo'
```

这是**属主**判断，改文件模式没有用。修法：容器提供方给每个会话的 agent 注入
`GIT_CONFIG_COUNT/KEY_0/VALUE_0=safe.directory=<本会话 worktree>`（分支会话再加
`<pair>/repo`，因为 linked worktree 的 gitdir 在那里），**不是** `safe.directory=*`；
`-sandbox local` 不注入（那里没有第二个账号）。
测试：`cmd/piforged/agent_git_env_test.go`（含"绝不写成通配符"的断言）。

#### 重启：只重启控制面，不碰容器

`docker restart pf-drill-control`（1021 ms 回来后），沙箱容器 id / `StartedAt` / `RestartCount`
都没变（`eb386c352f77… 14:31:05 0`）。启动对账：

```
msg="session reconciliation at startup" scanned=1 interrupted=0 runs=0 kept_live=1 unknown=0
```

**没有把它标成 interrupted**——这正是设计要求：心跳 marker 还在 TTL（2 分钟）内，探针的
三值语义是"没证据 ≠ 死"。但同一时刻：

```
pi process in the control plane after restart: False          # 它本来就是控制面的子进程，随容器一起死
/workspace/.piforge/live/<session> 内容: 2026-10-31T14:40:52.092262032Z pid=45
```

marker 是**死掉的 launcher 最后一次心跳**，所以 2 分钟 TTL 到期后看门狗补了一刀：

```
14:42:59 msg="interrupted an orphaned run" phase=watchdog session=ses_… runs=1
        reason=运行进程已退出，本次运行被中断
```

结论要说清：**探针本身没有在重启时误杀**（任务关心的是这条），但它也拦不住"agent 其实已经
死了"——它只能证明心跳文件还新。真要让它有意义，agent 得活在沙箱里（见下）。另外重启后那一小段
时间里 `GET /status` 是 `running=false`，而库里那行仍是 running（kept_live），UI 会短暂显示"空闲"。

#### 还没修、但仍然为真（不要当成已验证）

1. **agent 在控制面容器里执行，不在沙箱里。**（**2026-10-31 部分成立**：这一节描述的是默认
   行为；`-agent-in-sandbox` 下 `pi` 已经在沙箱里跑，`/data` 也读不到了，证据与代价见 §1.4.3。
   **2026-11-01 起更进一步**：同一个 flag 下控制面也不再挂工作区，技能投影与分支 worktree 全部经
   沙箱 API，见 §1.4.4。默认仍然是"控制面里执行"，因为存活（阶段三）还没做，flag 默认关闭。）
   上面的 `ps`、pi 的 cwd
   （`/data/sandboxes/<u>/<p>/repo`）、以及 marker 位置（`<pair>/.piforge/live/…`，由控制面
   写、经沙箱 API 读）都是同一条事实。后果：
   - **"agent 代码在沙箱里跑"这句话，对本地实现不成立**——沙箱是**工作区**边界（文件、终端、
     浏览器、git 面板都在里面），不是**执行**边界。agent 的 bash/node/git 与控制面同容器，
     因此它读得到 `/data`（SQLite、`master.key`、别的租户的 workspace）。
   - 控制面重启会杀掉 agent（`docker restart` 之后 pi 进程没了），所以上面那条
     `kept_live=1` 只是"心跳还没过期"。
   - 要真正修，得把 pi 放进沙箱（镜像里有 node，路径是 `aio` 的 shell API 或一个 stdio
     shim），并把 `materialize` 改走沙箱文件 API；那是接口层的改造，不是几行。
2. **面板改不了 agent 创建的文件**（反向的同一类属主问题）。agent 以 root 写出的文件是 0644，
   面板以 `gem` 写：

   ```
   PUT /v1/sessions/<sid>/files?path=primes.js -> 500
   error="workspaceremote: write \"primes.js\": aio: write /workspace/repo/primes.js:
          Permission denied: [Errno 13]"
   stat: root 644 /workspace/repo/primes.js   gem 644 /workspace/repo/panel-note.md
   ```

   反方向可用（面板提交发生在 agent 提交之后、agent 再提交也成功，因为 git 用 lock+rename，
   目录是 0777）。彻底修法与第 1 条是同一件：**一个写者**。过渡办法是把 agent 进程树的
   umask 设成 0（它新建的文件就是 0666），那也只是让"agent 写、用户改"这一半成立。
3. **沙箱镜像的 `gem` 与宿主属主仍可能对不上**（bind 形态在 Linux 宿主上），这一轮只在
   Docker Desktop + 卷上验证过。
4. **多主机**：控制面、runner、daemon 三者在三个地方时没验证。`SANDBOX_CONTAINER_ENDPOINT=1`
   的容器名寻址依赖"控制面与沙箱同一个 Docker 网络"，跨机器要换成可路由的名字或 router；
   runner 的 7801 没有 TLS，只应在内网。
5. **密钥轮换**：resume 不换 key，也没有轮换接口（§1.4 第 6 条）；这一轮没有改变它。

#### 本轮的验收（同一棵树）

| 门 | 结果 |
|---|---|
| `go build ./...` / `go vet ./...` | 干净（含 `gofmt -l` 无输出） |
| `go test ./... -count=1` | 全部包 ok（含 `internal/sandbox` 的真镜像生命周期测试，12.6 s） |
| `pnpm --dir web test -- --run` | **272 / 272**（34 个文件） |
| `pnpm --dir e2e test` | **182 / 182 passed (8.7m)** |
| `pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 来自 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY`，未打印未落盘） | run `20261031-225546`：**119 PASS / 0 FAIL**，`failure` 为空，111.5 s |

注意最后一条的适用范围：drill 用的是 `piforge:local`（本轮修复没打进这个 tag），它走的是
`-sandbox local` 放置，**不覆盖**本轮改动的 docker/runner 路径；runner 路径的覆盖是本节前面
那些手工证据 + `internal/sandbox` 的单元/真镜像测试。`pf-local` 在 drill 前后同一个 id
（`7dc14e4a7525…`），drill 自己的一次性容器与卷已删。

清理（本轮的实例已按这段删掉）：`docker rm -f pf-drill-control pf-drill-runner`、删掉本轮
`piforge-*` 沙箱、`docker volume rm pf-drill-data`、`docker network rm pf-drill-net`。
`pf-local` 的前后指纹：容器 `7dc14e4a7525…`、`/healthz` 200，未变。


### 1.4.3 阶段一：agent 进程搬进沙箱（`-agent-in-sandbox`，2026-10-31）

§1.4.2 最后一条"还没修"是这一轮唯一的目标：**`pi` 跑在控制面容器里，读得到 `/data`**。
设计写在 `docs/EXECUTION-BOUNDARY.md`（为什么走容器的 exec 字节流、`materialize` 怎么
离开共享卷、`models.json` 与 provider key 的边界、远端心跳、四个阶段的验收）。这里是
**已经落地的那一步**和它的原始证据。

**做了什么**（只此一件）：控制面不再 `exec.Command("pi")`，而是让沙箱启动同一个 argv，
把 RPC 流接过来。`internal/pi` 的 `RuntimeConfig.Dial` 本来就是为这件事留的缝（它的注释
写着"sandbox shim's stdio channel"），所以 JSONL 分帧、命令/响应按 id 关联、事件扇出、
settle 判定全部是原来那份代码；变的只有文件描述符的来源。

- **通道**：runner 新增第七个端点 `POST /v1/sandboxes/{id}/attach`，把连接 hijack 掉之后
  直接接 `docker exec -i -u gem -w /workspace …` 的 stdio：响应体是 stdout，请求的写端是
  stdin，半关闭就是 EOF。同一个 bearer token。**没有**用沙箱自己的 `/v1/shell/exec`：那是
  终端面板的行式持久 shell，没有 EOF、不保证二进制安全、和用户的终端混在一个会话里，
  JSONL 不能从那儿过（理由见设计 §2）。
- **agent 从哪来**：镜像里。`deploy/Dockerfile.sandbox-agent` 在 AIO 镜像上叠两样东西——
  `pi`（与 `piforge:local` 同一版本 1.4.0）和 `piforged`（只当 `agent-launcher` 用）。
  不按会话上传 150 MB 的 npm 树，也不在沙箱里联网装。
- **以谁的身份跑**：`gem`（uid 1000），也就是 checkout 和面板的属主。§1.4.2 的两个属主
  缺陷（面板改不了 root 写的文件、agent 的 git 报 dubious ownership）在这个摆法下不成立，
  因为只有一个写者。`GIT_CONFIG_*` 因此不再注入。
- **配置从哪进**：`models.json` 走沙箱**文件 API** 写进
  `/workspace/.piforge/agents/<session>/`，`PI_CODING_AGENT_DIR` 指到那里。里面只有
  gateway URL 和**每会话 HMAC token**，没有 provider key（见下面的 grep）。**MCP 配置
  没有跟着进**：`mcp.json` 里是解密后的凭据，进沙箱就等于把这条路重新打开；带 MCP 的
  会话在这个 flag 下暂时没有 MCP（设计 §4 写明这是 stage 3 的凭据代理）。
- **心跳**：`piforged agent-launcher` 在**沙箱里**跑，marker 仍然是
  `/workspace/.piforge/live/<session>`，探针（`internal/liveness` 的 AIO 读法）一行没改。
- **会话文件**：从 `/tmp/.piforge/sessions/…` 移到 `<pair>/.piforge/sessions/…`（沙箱里是
  `/workspace/.piforge/sessions/…`），一边是共享卷，两边看到同一份字节；cwd header 的
  对齐由**沙箱用户**经文件 API 做（控制面重写会把文件变成 root 所有，gem 就写不回去了）。

#### 实测（Windows 11 / Docker Desktop，7723，一次性实例）

形状与 §1.4.2 相同，只多一个环境变量；`pf-local`（7722）全程未动：

```powershell
# 控制面镜像：pwsh scripts/build-app-image.ps1 -Tag piforge:agent-drill
# runner：docker build -f deploy/Dockerfile.runner -t piforge/sandbox-runner:agentdrill .
# 沙箱：docker build -f deploy/Dockerfile.sandbox-agent --build-arg AGENT_IMAGE=piforge:agent-drill -t piforge/sandbox-agent:agentdrill .
docker run -d --name pf-drill-control --network pf-drill-net -p 7723:7700 \
  -v pf-drill-data:/data --cap-drop DAC_OVERRIDE \
  -e PIFORGE_PORT=7700 -e PIFORGE_DATA=/data \
  -e PIFORGE_GATEWAY_URL=http://pf-drill-control:7700 \
  -e PIFORGE_SANDBOX=runner -e PIFORGE_AGENT_IN_SANDBOX=1 \
  -e PIFORGE_SANDBOX_RUNNER_URL=http://pf-drill-runner:7801 \
  -e PIFORGE_SANDBOX_RUNNER_TOKEN=$tok -e PIFORGE_SANDBOX_IMAGE=piforge/sandbox-agent:agentdrill \
  … piforge:agent-drill
```

启动日志（新增的一行是 `the agent runs inside the sandbox`，旧的
`container agents run under a heartbeat launcher` 不再出现——它描述的是本地 spawn）：

```
msg="the agent runs inside the sandbox" sandbox=runner user=gem pi=pi launcher=/opt/piforge/piforged
     detail="RPC over the sandbox exec stream; the provider key never enters the container"
```

**1. agent 在沙箱里，控制面里没有**（同一次任务，两个 `ps`）：

```
docker exec piforge-79f3b298a4d9d366 ps -ef
gem  1048  0  /opt/piforge/piforged agent-launcher -root /workspace -session ses_01M698J1… \
              -interval 15s -- pi --mode rpc --model piforge/deepseek-v4-flash \
              --skill /workspace/repo/.pi/skills --session /workspace/.piforge/sessions/ses_01M698J1….jsonl
gem  1061  1048  pi

docker exec pf-drill-control ps -ef      # 只有 piforged / next-server / tini
docker exec pf-drill-control sh -c 'ps -ef | grep "p[i] --mode rpc" || echo NO_PI'  -> NO_PI
```

**2. 沙箱里没有 `/data`**（这就是这一轮的全部意义）：

```
docker exec -u gem <sandbox> sh -lc 'ls /data; cat /data/piforge.db; ls /data/sandboxes'
ls: cannot access '/data': No such file or directory
cat: /data/piforge.db: No such file or directory
ls: cannot access '/data/sandboxes': No such file or directory

docker exec pf-drill-control sh -lc 'ls /data'
master.key  pi-agents  piforge.db  piforge.db-shm  piforge.db-wal  sandboxes  secret.key  skills
```

**3. 一个真实任务跑完，提交在沙箱的 worktree 里**（"写一个 Node 脚本输出前 10 个素数，
配一个测试并运行，然后提交"）：

```
run.started -> run.settled          12 s（首条），随后一条行内评论 12 s
docker exec -u gem <sandbox> sh -lc 'cd /workspace/repo && git log --format="%h %an|%ae %s" -5'
de5061b PiForge Sandbox|sandbox@piforge.local 新增输出前 10 个素数的脚本及其测试
a90414c PiForge Sandbox|sandbox@piforge.local piforge: initial commit
git status --porcelain -> 空
```

**4. 面板 / 测试面板 / 行内评论照常**（它们本来就只走沙箱 API）：

```
GET  /files/tree                -> primes.js、test/primes.test.js
GET  /files?path=primes.js      -> 200，body 559 字节
PUT  /files?path=panel-note.md  -> 200；git status 显示 1 insertion
POST /git/stage + /git/commit   -> 200，e30cbb5 "panel: a note from the changes panel"
GET  …/test-runs                -> node --test，passed 2/2，source=agent，
                                   command="cd /workspace/repo && node primes.js && node --test"
POST /messages {review_comment: primes.js:3}  -> 21 ms 接受；agent 改名并提交 3661297，
                                   测试仍 2/2
```

**5. 属主只有一个写者**（§1.4.2 缺陷 2 在这个摆法下不存在）：

```
-rw-r--r-- 1 gem gem 577 /workspace/repo/primes.js        # agent 写的，gem 所有
-rw-r--r-- 1 gem gem 524 /workspace/repo/test/primes.test.js
stat /workspace/repo            -> 2096:437653 gem 777
stat（控制面侧同一目录）          -> 2096:437653      # 同一个 inode
```

**6. 沙箱里没有 provider key**（只给命中数，key 从未打印）：

```
docker exec -u gem <sandbox> sh -c 'env | grep -c VOLCE'                 -> 0
docker exec <sandbox> sh -c 'grep -c VOLCE /workspace/.piforge/agents/*/models.json' -> 0
models.json 的 baseUrl = http://pf-drill-control:7700/gw/llm/eyJzIjoic2VzXzAx…  # 每会话 token
docker inspect pf-drill-control 的 env 里有 PIFORGE_BOOTSTRAP_PROVIDER_KEY（控制面持有，按设计）
```

**7. 重启控制面：agent 会死，而且死得诚实。**（这一条是这一轮最重要的"没做到"）

```
before:  sandbox 71935c04271f StartedAt=15:16:56Z RestartCount=0
         gem 1048 agent-launcher / gem 1061 pi；marker pid=1061
docker restart pf-drill-control        # 沙箱容器 id / StartedAt / RestartCount 全不变
after :  ps 里没有 agent-launcher，也没有 pi；/workspace/.piforge/live 为空（marker 被
         launcher 删掉，不是等 TTL 过期）
         之后发一条消息 -> 4 s 内新的 pi（pid 3110）在沙箱里起来，marker 重新变成 pid=3110
```

原因是**通道的所有权**：阶段一的 RPC 是一条由控制面持有的 TCP 连接，控制面一重启，连接
断开，`docker exec` 看到 stdin EOF，`pi` 退出，launcher 删 marker，于是对账立刻答"死"
（而不是像 §1.4.2 那样先 `kept_live=1` 两分钟）。**要让它活着，得让沙箱里有一个不随控制面
存亡的 supervisor**，控制面重连它——那是设计里的阶段三，本轮没做。诚实地说：这一轮把
**执行边界**换掉了（agent 读不到 `/data`），**没有**换掉"agent 与控制面同生共死"。

`pf-local`（7722）前后同一个容器 id `7dc14e4a7525…`、`/healthz` 200。本轮的一次性
实例已删：`pf-stage3-control` / `pf-stage3-runner`、compose 的 `piforge` 三件
（`docker compose -f docker-compose.runner.yml down -v`）、`^piforge-[0-9a-f]+$` 沙箱、
卷 `pf-stage3-data` / `pf-stage3-compose-data` / `piforge-ws-*`、网络 `pf-stage3-net` /
`piforge-sandboxes` / `piforge_default` / `piforge_sandbox-control`、三个 `*:stage3` 镜像。
（顺带删掉了一个 3 天前 Exited 的 `docker run --name piforge` 残留容器，它占着 compose 的容器名。）
#### 本轮的门禁与清理

| 门 | 结果 |
|---|---|
| `go build ./...` / `go vet ./...` | 干净 |
| `go test ./... -count=1` | 全部包 ok（`internal/sandbox` 含真镜像生命周期，12.7 s；新增 attach 的 4 个用例） |
| `pnpm --dir web test -- --run` | **272 / 272**（34 文件） |
| `pnpm --dir e2e test` | **182 / 182 passed（8.6m）**，exit 0 |
| `pwsh scripts/deliver-check.ps1 -Image piforge:agent-drill` | run `20261031-235815`：**119 PASS / 0 FAIL**，`failure` 为空，105 s，镜像 `sha256:30cbba72…`（本轮构建；`-agent-in-sandbox` 关，local 路径不变） |

`pf-local` 前后同一个容器 id `7dc14e4a7525…`、`/healthz` 200。本轮的一次性实例已删：
`pf-drill-control`、`pf-drill-runner`、四个 `piforge-*` 沙箱（只按 `^piforge-[0-9a-f]+$`
删）、卷 `pf-drill-data`、网络 `pf-drill-net`、三个 `*:agentdrill` 镜像。

**仍然不是默认**：`-agent-in-sandbox` 默认关闭。它要求沙箱镜像里有 `pi` 和
`/opt/piforge/piforged`（否则启动失败，不会静默退回控制面），要求控制面能按名字访问
gateway，而且上面第 7 条的代价还在。阶段三/四（常驻 supervisor、控制面重连、MCP 凭据代理、
K8s 的 `pods/exec`）见 `docs/EXECUTION-BOUNDARY.md` §10。阶段二（`materialize` 走文件 API、
脱离共享卷）已经做完，见下面的 §1.4.4。

上面第 1–6 条的证据来自第一次演练（`de5061b` / `e30cbb5` / `3661297`）。此后代码又加了两处
防御性改动（fork 把 Pi 报回的**容器路径**原样接受；`models.json` 不存在时按本地同样的
"让 Pi 自己报错"处理，而不是 500），于是把整套一次性实例**重建并重跑了一遍**最终树：
`ps` 里仍是沙箱内 `gem … agent-launcher -root /workspace …` + `gem … pi`、控制面 `NO_PI`，
`cat /data/piforge.db` 仍是 `No such file or directory`，一条真实任务 10 s 完成并提交
`34f92a6`（作者 `PiForge Sandbox|sandbox@piforge.local`）。写完这段后实例再次删除。

### 1.4.4 阶段二：工作区不再经过共享卷（`-agent-in-sandbox`，2026-11-01）

§1.4.3 的"仍然为真"第一条是：`materialize`/`provision` 仍写共享卷，所以控制面仍必须挂载
工作区。这一轮把那一半搬走了。设计仍是 `docs/EXECUTION-BOUNDARY.md`，这里是**落地的那一步**
与原始证据。

**形状变了一件事**：沙箱不再从控制面的数据卷里取工作区，而是挂**自己的**卷
`piforge-ws-<pair hash>`；控制面既没有 `--mount` 也没有 `-v`，`/data` 里连 `sandboxes/`
目录都不再有（`Ensure` 在 detached 模式下不再 `MkdirAll`）。控制面能做的只有三件事：
经沙箱 API 写文件、经 runner 建/删容器、看自己的状态卷。

**代码上的三个接缝**（每个都能单独测）：

1. `contracts.WorkspaceMode`（`host` | `api`）落在 `sandboxes.workspace` 列上。它由建挂载的
   provider 写、由之后所有碰这棵树的人读，重启后仍然成立 —— 比"内存里比一个 flag"可靠。
2. `materialize.FileSystem`（`internal/materialize/fs.go`）：`Join/IsAbs/Abs/MkdirAll/
   WriteFile/ReadFile/Lookup/List/RemoveAll/MakeReadOnly/MakeWritable/CanBind`。主机实现就是
   原来的 `os.*`；沙箱实现（`materialize.SandboxFS`）是文件 API。`Mount` 与 `MountFS` 共用
   全部规则（冻结、manifest、git exclude、digest 的框架），所以只有一份投影逻辑。
3. `provision.Config.WorkspaceFiles` + `httpapi` 经 `contracts.Workspace` 读 manifest：
   打不开的树就问沙箱，不问宿主。

**挂载形状从容器自己的记录里认**，不是从 flag 认：`/workspace` 是名为
`piforge-ws-<容器名 hash>` 且没有 subpath 的卷，就是 detached。所以重启后的控制面、无状态的
runner 对一个自己不记得创建过的容器，得到同一个答案。

#### 实测（Windows 11 / Docker Desktop，7723，一次性实例）

与 §1.4.3 的区别只有一个：**不设 `SANDBOX_WORKSPACE_VOLUME`**，runner 不挂 `/data`：

```powershell
docker network create pf-drill-net; docker volume create pf-drill-data   # 只为控制面状态
docker run -d --name pf-drill-runner --network pf-drill-net `
  -e "RUNNER_TOKEN=$tok" -e SANDBOX_RUNNER_ADDR=0.0.0.0:7801 `
  -e SANDBOX_DATA_ROOT=/data/sandboxes -e SANDBOX_NETWORK=pf-drill-net `
  -e SANDBOX_CONTAINER_ENDPOINT=1 -e "SANDBOX_IMAGE=piforge/sandbox-agent:agentdrill" `
  -v /var/run/docker.sock:/var/run/docker.sock piforge/sandbox-runner:agentdrill
  # ↑ 没有 -e SANDBOX_WORKSPACE_VOLUME / -v pf-drill-data:/data
docker run -d --name pf-drill-control --network pf-drill-net -p 7723:7700 `
  -v pf-drill-data:/data --cap-drop DAC_OVERRIDE -e PIFORGE_AGENT_IN_SANDBOX=1 `
  -e PIFORGE_SANDBOX=runner `
  … piforge:agent-drill
```

**1. 控制面不挂工作区（`docker inspect`，本轮的核心证据）：**

```
docker inspect pf-drill-control  ->  mounts: [volume pf-drill-data -> /data]
docker inspect pf-drill-runner   ->  mounts: [bind /var/run/docker.sock -> /var/run/docker.sock]
docker inspect piforge-ba8b2e998f171d81
                                 ->  mounts: [volume piforge-ws-ba8b2e998f171d81 -> /workspace]
docker volume ls | grep piforge-ws  ->  piforge-ws-ba8b2e998f171d81
# 谁挂了这块工作区卷：
docker ps | foreach { mounts -match 'piforge-ws' }  ->  只有 piforge-ba8b2e998f171d81
```

控制面的 `/data` 里**没有** `sandboxes/`：`ls /data` → `master.key pi-agents piforge.db
piforge.db-shm piforge.db-wal secret.key skills`。也就是说整个部署里没有任何进程还在
"往工作区的宿主路径写"，因为那个路径不存在了。

**2. 同一条 prompt 仍然跑完并提交（提交在沙箱里）：**

```
POST /v1/sessions/<sid>/messages -> accepted（1010 ms）
run.started -> run.settled       -> 17 s
docker exec -u gem <sandbox> sh -lc 'cd /workspace/repo && git log --format="%h %an|%ae %s" -2'
ae151cc PiForge Sandbox|sandbox@piforge.local Add script printing first 10 primes with tests
bd2f398 PiForge Sandbox|sandbox@piforge.local piforge: initial commit
git status --porcelain -> 空
ls -la /workspace/repo -> primes.js、package.json、test/、.pi/（全部 gem:gem）
```

**3. 技能树是只读的，机制说真话：**

```
docker exec -u gem <sandbox> ls -la /workspace/repo/.pi/skills
dr-xr-xr-x 2 gem gem code-review / explain-codebase / test-gap
-r--r--r-- 1 gem gem …/code-review/SKILL.md 等
cat /workspace/repo/.pi/mounted.json
  "mechanisms": {"code-review":"copy-ro","explain-codebase":"copy-ro","test-gap":"copy-ro"}

# 以 agent 的身份尝试写入：
echo tampered >> …/code-review/SKILL.md  -> sh: cannot create …: Permission denied (exit 2)
touch …/code-review/extra.md             -> touch: cannot touch …: Permission denied (exit 1)
head -2 …/code-review/SKILL.md           -> 内容未变（--- / name: code-review）
GET /v1/sessions/<sid>/skills           -> mechanisms 与 manifest 一致：三份 copy-ro
```

机制为什么是 `copy-ro` 而不是 `bind-ro`：**bind mount 的命名空间在容器里**，控制面能碰到的
只有文件 API，没有地方可挂。只读的保证是模式位（0444/0555），而这正是 `copy-ro` 在宿主上
本来就表示的东西；探针也搬到了沙箱里跑（`test -w`，以 agent 的账号回答），所以徽章说的是
那个账号的事实，不是控制面进程的事实。前端徽章的文案也按这个事实改了：旧文案说"可新建
文件"，而这次连新建都被拒绝，说少了也是不准确。

**4. 分支会话的 worktree 整个经沙箱 API 建成：**

```
docker exec -u gem <sandbox> git -C /workspace/repo worktree list
/workspace/repo                ae151cc [main]
/workspace/worktrees/asnbxt4n  ae151cc [piforge/asnbxt4n]
cat /workspace/worktrees/asnbxt4n/.git
gitdir: /workspace/repo/.git/worktrees/asnbxt4n      # 容器路径，没有宿主路径
ls /workspace/worktrees/asnbxt4n/.pi/skills          # 技能同样投影进了分支 worktree
```

`provision` 这一侧把 `wt.Path` 记成 `/workspace/worktrees/<slug>`（容器路径），面板、终端、
git 都按同一条路径问沙箱；面板侧不再需要 `ContainerPath` 做翻译，因为两边命名一致。

**5. 面板 / 测试面板 / 行内评论照常（同一实例，同一个会话）：**

```
GET  /files/tree                     -> test/primes.test.js、package.json、primes.js、panel-note.md
GET  /files?path=primes.js           -> 200，620 字节，binary=false
PUT  /files?path=panel-note.md       -> 200
POST /git/stage + /git/commit        -> 200，1dd4ba8 "panel: a note from the changes panel"
GET  …/test-runs                     -> npm test，passed 3/3，source=agent，
                                        command="cd /workspace/repo && node primes.js && npm test 2>&1"
POST /messages {review_comment:{path,line,body}} -> 19 ms 接受；agent 改注释并提交
                                        8bf6b63 "Clarify primes.js doc comment (质数) and keep it brief"
```

**6. 沙箱里仍然没有 `/data`，也没有 provider key：**

```
docker exec -u gem <sandbox> sh -lc 'ls /data; cat /data/piforge.db; ls /data/sandboxes'
ls: cannot access '/data': No such file or directory
cat: /data/piforge.db: No such file or directory
ls: cannot access '/data/sandboxes': No such file or directory
docker exec -u gem <sandbox> env | grep -c VOLCE                                   -> 0
docker exec -u gem <sandbox> grep -c VOLCE /workspace/.piforge/agents/*/models.json -> 0
```

**7. 谁写、谁所有（这一轮"要不要保留 chmod a+rwX"的答案）：**

```
/workspace/repo/.pi           drwxr-xr-x gem gem
/workspace/repo/primes.js     -rw-r--r-- gem gem
/workspace/worktrees/<slug>   drwxrwxrwx gem gem      # git 建目录时的 umask，gem 所有
```

只有一个写者（gem，经沙箱 API），所以 `planCheckout` 的 `chmod -R a+rwX` 与 git 的
`safe.directory` 在 detached 形状里**都不再注入**：前者本来是给"控制面以 root 写进同一棵树"
用的，后者本来是给"bind mount 让树看起来属于别人"用的。两者都按 `DetachedWorkspace` 这一个
位判断，而不是删掉 —— 共享卷形状（`-sandbox local`、不加 flag 的 docker/runner）仍然需要它们，
那里确实还有第二个写者。

**8. 重启控制面：agent 仍会死**（阶段二没有改这个，见 §1.4.3 第 7 条和
`docs/EXECUTION-BOUNDARY.md` §6；存活是阶段三）。这一轮没有重测这一条，因为它与阶段一
同一条通道，代码路径未变。

#### 本轮的门禁与清理

| 门 | 结果 |
|---|---|
| `go build ./...` / `go vet ./...` | 干净 |
| `go test ./... -count=1` | 全部包 ok（新增：`materialize` 的 `MountFS`/`SandboxFS`/`DigestFS` 用例、`provision` 的 detached 两例、`sandbox` 的 volume/safe.directory/mode 用例、`cmd/piforged` 的 `validateAgentWorkspace`） |
| `pnpm --dir web test -- --run` | **272 / 272**（34 文件） |
| `pnpm --dir e2e test` | **182 / 182 passed（8.5m）**，exit 0 |
| `pwsh scripts/deliver-check.ps1 -Image piforge:agent-drill` | run `20261101-005703`：**119 PASS / 0 FAIL**，`failure` 为空，96 s，镜像 `sha256:ebd8c664…`（本仓库这一棵树的构建；`-agent-in-sandbox` 关，local 路径不变） |

`pf-local`（7722）前后同一个容器 id `7dc14e4a7525…`、`/healthz` 200。一次性实例已删：
`pf-drill-control`、`pf-drill-runner`、`piforge-ba8b2e998f171d81`（只按
`^piforge-[0-9a-f]+$` 删）、卷 `piforge-ws-ba8b2e998f171d81` 与 `pf-drill-data`、网络
`pf-drill-net`、三个 `*:agentdrill` 镜像。

**当时仍然是默认关闭**（**阶段三已翻转**：docker/runner 下默认开，见 §1.4.5）：`-agent-in-sandbox` 当时默认关；打开时它现在也要求 `-sandbox-files remote`
（工作区只剩容器里那一份，宿主面板没有东西可读），这是启动错误，不是静默耦合。
阶段二明确**没有**做的部分（`worktrees.base_commit`、agent stderr 日志、marker 的 boot id、
按容器 stage 运行时、回收快照、配额测量、容器内 `CloneSession`）都列在
`docs/EXECUTION-BOUNDARY.md` §10.1，写明了原因和修法。

为什么选它（对照 §1.1–§1.3 的另外三条路）：

- 控制面不再需要 root，runner 是唯一持有 socket 的进程，而它的接口面是**六个动作**，
  不是"整个 Docker API"。审一个几百行的 runner 比审"控制面里每一处 shell 调用"容易；
- runner 是唯一"靠近 daemon"的一方：单机形态下它和控制面跑在同一台机器上，路径语义直接成立；容器形态下它按卷名挂（§1.4 第 2 条），宿主路径根本不进这条链路。两种形态 provider 是同一份代码；
- runner 无状态，重启不影响已存在的沙箱；
- 以后换 K8s，runner 换成一个 K8s 控制器，控制面这一侧的调用形状不变 —— 这正是
  `SandboxProvider` 应该长的样子。

代价：多一个服务、一份要维护的 HTTP 契约、一个要保护的内网端点（用 token +
只在内网网络里可达）。这个代价比"控制面 = 宿主 root"便宜得多。

**如果只想先跑起来**：先用（ii）proxy + 给镜像加 docker CLI，把它明确标注成"单机过渡
方案"，并在 README 里写清楚它等价于宿主 root。不要用它承载多租户。

### 1.4.5 阶段三：agent 活过控制面重启，凭据不出控制面（默认开启，2026-11-01）

阶段一留下了"控制面重启 → agent 立刻死"，阶段二留下了"agent stderr 只进 runner 日志"和
"MCP 根本没有"（`docs/EXECUTION-BOUNDARY.md` §6）。这一轮把三件事一起做完，并且**把默认翻过来**：
`-sandbox docker`/`runner` 下 agent 默认在沙箱里跑，`-sandbox local` 不动。

**形状**（控制面只持有"到 supervisor 的连接"，不再持有 agent 的 fd）：

```
控制面 ──runner attach/reattach──▶ piforged agent-attach ──unix socket──▶ supervisor ──pipes──▶ agent-launcher ──▶ pi
```

- **沙箱里的 supervisor**：`piforged agent-supervisor`（`internal/agentsupervisor`）由
  `agent-supervisor-start` 以**独立会话**（setsid、stdio 指向日志）拉起，监听
  `/workspace/.piforge/run/<session>.sock`，自己持有 agent 的 stdin/stdout/stderr 管道。
  控制面死掉时它只看到"一个客户端没了"，agent 继续跑。
- **重连**：控制面启动时（`Server.ReattachLiveSessions`，在端口打开之前）问存活探针"哪些会话
  真有 agent"，再走 `hub.ensure` 重新订阅；`Dial` 先试 runner 的**第八个端点**
  `POST /v1/sandboxes/{id}/reattach`（body 是 socket + helper argv，**不接受** agent argv，
  所以"重连时再起一个 agent"这件事无法表达），失败才走 attach+`agent-supervisor-start`。
- **握手**：supervisor 连上后先写一行 JSON（`{"type":"ready","session":…,"pid":<pi 的 pid>,
  "started":…,"replay_from":0,"replay_bytes":N}`），之后就是 agent 的原始字节；控制面读掉这一行，
  后面仍是原来那套 JSONL 分帧。
- **补发**：supervisor 保留最近 8 MiB 的 stdout（按整条记录裁剪），重连时从"上一个客户端最后
  接收到的字节"开始补发；超出窗口的部分丢弃并报告 `gap`（PRD §7.4.5：不重放已经死掉的 run）。
- **签名密钥**：`/data/gateway.key`（0600，首次启动生成）。agent 活着而控制面重启时，它手里的
  per-session token 必须仍然有效，否则"活下来"没有意义。
- **MCP 凭据代理**：HTTP MCP server 不再把凭据写进沙箱。控制面给每个 `(session, server)` 生成
  一个随机 token（库里存 hash + 封存的副本），沙箱 `mcp.json` 里是
  `<gateway>/gw/mcp/<token>`；`POST /gw/mcp/<token>` 在出口处解密真实凭据并按 `KEY=value` 的每一行
  注入成一个 HTTP 头；`DELETE /v1/sessions/{id}/mcp-token` 立即吊销，且**不会**在下次重启 agent 时
  重新签发。stdio MCP server 仍然不进沙箱（没有出口可注入），启动日志会说明。
- **stderr 进 Logs 面板**：supervisor 把 agent 的 stderr 写到
  `/workspace/.piforge/logs/<session>.log`，`sandboxAgent.Logs` 经沙箱文件 API 读回，
  `GET /v1/sessions/{id}/logs` 返回的就是这个文件。

#### 实测（Windows 11 / Docker Desktop，7723，一次性实例；镜像 `piforge:stage3`）

三个镜像都是这一棵树：

```powershell
pwsh scripts/build-app-image.ps1 -Tag piforge:stage3
docker build -f deploy/Dockerfile.runner -t piforge/sandbox-runner:stage3 .
docker build -f deploy/Dockerfile.sandbox-agent --build-arg AGENT_IMAGE=piforge:stage3 -t piforge/sandbox-agent:stage3 .
```

**1. 重启只重启控制面：同一个 `pi` pid，同一容器，run 继续并结算**

一次"写 fizzbuzz + 测试 + 提交"的长任务（`send` 1080 ms 接受），`run.started` 之后立即
`docker restart pf-stage3-control`（520 ms 返回），实测：

```
before: sandbox=9f095f00a2a9… 2026-10-31T17:50:51.653Z 0      # Id / StartedAt / RestartCount
before: marker=[2026-10-31T17:51:04.735Z pid=2139] pi_pids=[2139] seq=8
docker restart pf-stage3-control: 520 ms
after:  sandbox=9f095f00a2a9… 2026-10-31T17:50:51.653Z 0      # 沙箱没动
after:  marker=[2026-10-31T17:51:04.735Z pid=2139] pi_pids=[2139]
SAME PI PID: 2139
control plane healthy again: True
after restart: running=True streaming=True seq=5
status: {"running":true,"streaming":true,"process_id":2139,"session_file":"/workspace/.piforge/sessions/ses_…jsonl"}
run settled after the restart: True (saw running: True) seq=902
git log: 6aa7f5b PiForge Sandbox|sandbox@piforge.local add fizzbuzz script and tests
```

控制面这一侧的三行（重启后的进程）：

```
session reconciliation at startup scanned=1 interrupted=0 runs=0 kept_live=1
reattached to surviving agents count=1 detail="stage 3: an agent that outlived this process keeps its run"
（每个会话一行，命中 Supervisor 的 ready 帧后）agent_started=<重启前> agent_pid=2139 replayed_bytes=10955 gap_bytes=0
```

supervisor 这一侧：

```
control plane attached to the agent stream session=ses_… agent_pid=2139 replayed_bytes=10955 gap_bytes=0
```

**读法**：`kept_live=1` 这次是真的（同一 `pi` 进程）；`replayed_bytes=10955 gap_bytes=0` 说明
控制面缺席期间 agent 的输出被补发了，没有洞；`run.settled` 落在**同一个 run 行**上，
`git log` 里的提交就是那个 run 的结果。`process_id` 现在是沙箱里的 `pi` pid（不是 0）：
`Bridge.PID()` 会问传输层要 pid，supervisor 报的是它从 marker 里读到的那个 pid。

**2. 状态在重启后不会短暂显示"空闲"**

第一次演练里重启后 `/status` 有一两秒 `running=false`：补发的字节从 turn 中间开始，未必含
`agent_start`。修法是重连后问一次 `get_state`，进程说 `isStreaming:true` 就把会话标成运行中
（`Normalizer.NoteRunning`，settle 事件照常清除）。上面 `after restart: running=True` 就是它。

**3. MCP：沙箱里只有可吊销的 token，agent 真的能用**

配置一个 HTTP MCP server（`host.docker.internal:7731/mcp`，要求
`Authorization: Bearer <real>`），沙箱里的 `mcp.json`：

```json
{"mcpServers":{"stage3docs":{"url":"http://pf-stage3-control:7700/gw/mcp/T7rbK7jexqb…"}}}
```

agent 通过代理调用工具，拿到 `note: stage 3 broker`；从沙箱里直接打代理 URL 是 200，
直接打上游（不带凭据）是 401：

```
=== files containing the real MCP credential (workspace/tmp/home) ===
file hits: 0
=== MCP credential in the environment: 0 ===
=== provider key anywhere under /workspace: 0 ===
=== models.json: "baseUrl": "http://pf-stage3-control:7700/gw/llm/eyJzIjoic2Vz…" ===   # 只有每会话 token
=== before revoke: HTTP 200 ===
DELETE /v1/sessions/{id}/mcp-token -> 204（日志 tokens=1）
=== after revoke: HTTP 401 / HTTP 401 ===
日志：msg="a revoked MCP broker token was used" path=/gw/mcp/<token>
```

**4. stderr 进 Logs 面板**

把某个会话的 marker 文件改成 0444（真实故障：工作区只读/满），launcher 下一拍写不进去，
它的 WARN 走的就是 agent 的 stderr 管道（`pi` 继承同一个）:
 
```
/workspace/.piforge/logs/<session>.log（沙箱内）
2026/11/01 01:53:41 WARN liveness: could not refresh the agent heartbeat session=ses_… error="… permission denied"
GET /v1/sessions/{id}/logs -> 200
  "lines": ["2026/11/01 01:53:41 WARN liveness: could not refresh the agent heartbeat … permission denied", …]
```

正常运行时这个文件是 0 字节：`pi` 健康时什么都不写 stderr。写入路径另有一个 Go 测试
（`TestSupervisorWritesTheAgentStderrToItsLog`）钉住，读取路径就是上面这条命令。

**5. 默认翻转：`docker compose -f docker-compose.runner.yml up` 不加任何 flag 就是隔离实例**

compose 文件现在是**自包含**的（以前是 overlay，还要手动加 `PIFORGE_AGENT_IN_SANDBOX=1` 和
`SANDBOX_WORKSPACE_VOLUME`）。实测（7724，独立数据卷，未设 `PIFORGE_AGENT_IN_SANDBOX`）：

```
docker compose -f docker-compose.runner.yml up -d
piforge / piforge-sandbox-runner / piforge-sandbox-image: Up
startup: sandbox placement resolved sandbox=runner browser=remote terminal=remote files=remote
startup: the agent runs inside the sandbox sandbox=runner user=gem pi=pi
一次真实 prompt: send 1025 ms -> settled，transcript 里是 bash 的 compose-default-ok

docker inspect piforge                -> volume pf-stage3-compose-data -> /data
docker inspect piforge-sandbox-runner -> bind /var/run/docker.sock
docker inspect piforge-bdcd52f14ac1718c -> volume piforge-ws-bdcd52f14ac1718c -> /workspace
docker exec piforge ps -ef | grep "p[i] --mode rpc" -> NO_PI
docker exec <sandbox> ps -ef -> gem agent-supervisor-start / agent-supervisor / agent-launcher / pi
docker exec piforge ls /data -> gateway.key master.key pi-agents piforge.db … skills   # 没有 sandboxes/
```

`-sandbox local` 仍然是默认的 `-sandbox`：`pf-local`（7722）全程未动，drill 也仍然走 local 路径。

#### 本轮修掉的两个缺陷（都有测试）

1. **supervisor 的读缓冲被复用，导致记录被截断。** `pump` 把 `buf[:n]` 直接排队给写
   goroutine，下一次 `Read` 会覆盖它：一条响应记录的开头会被抹掉，控制面于是等一个永远不来的
   响应（表现为 `POST /messages` 卡 58 s 后 499）。`TestSupervisorCarriesSeveralRPCRoundTrips`
   一轮就复现（第一轮响应就缺头），修法是入队前复制。
2. **runner 的 `DockerProvider` 没有 `Reattach` 方法**，第八个端点在 runner 下回 501，于是重启后
   走的是"attach + supervisor-start"（虽然也接上了同一个 supervisor，但不是 reattach 语义）。

#### 本轮的门禁与清理

| 门 | 结果 |
|---|---|
| `go build ./...` / `go vet ./...` | 干净 |
| `go test ./... -count=1` | 全部包 ok（新增 `internal/agentsupervisor` 的 6 个用例：两轮以上 RPC 往返、客户端离开后 agent 继续、补发窗口按整条记录裁剪、stderr 进日志；`internal/httpapi` 的 MCP broker 两例；`internal/sandboxrunner` 的 reattach 三例；`cmd/piforged` 的默认判定） |
| `pnpm --dir web test -- --run` | **272 / 272**（34 文件） |
| `pnpm --dir web exec next build` | 通过 |
| `pnpm --dir e2e test` | **182 / 182 passed（8.7m）**，exit 0 |
| `pwsh scripts/deliver-check.ps1 -Image piforge:stage3` | run `20261101-021859`：**119 PASS / 0 FAIL**，`failure` 为空，102.3 s，镜像 `sha256:06fc3a61…`（本仓库这一棵树的构建；drill 用 local 放置，默认翻转不影响它） |

**仍然是明确没做的**（`docs/EXECUTION-BOUNDARY.md` §10.1 逐条写明原因）：

- `worktrees.base_commit`、回收快照、配额测量、容器内 `CloneSession`（阶段二遗留，未变）；
- stdio MCP server 在 flag 下仍然不进沙箱（没有出口可注入凭据）；
- 补发窗口内的字节会重复投递一次（有界：一个 socket 缓冲，不是整个 run）；
- 阶段四（删掉 flag、删掉 host spawn、K8s `pods/exec` provider）没有开始。

### 1.4.6 阶段四：只留下一条执行路径，stdio 的拒绝变成产品行为（2026-11-01）

阶段三把默认翻过来之后，`-agent-in-sandbox=false` 仍然能选回"agent 跑在控制面旁边"那条路。
阶段四把这条路**删掉**，把唯一还靠日志说明的凭据缺口（stdio MCP）变成产品里的可见状态，
并且把三把密钥的丢失后果实测了一遍。设计取舍写在 `docs/EXECUTION-BOUNDARY.md` §11。

**形状（现在只有两种执行，且都由 provider 决定）**：

```
-sandbox docker|runner  →  agent 永远在沙箱里（supervisor + agent-launcher + pi）
-sandbox local          →  agent 是控制面的子进程（开发/演练路径，未改动）
```

- **flag 退休，不是被忽略**：`-agent-in-sandbox=false`（或 `PIFORGE_AGENT_IN_SANDBOX=0`，
  老 compose 文件里最常见的就是它）在 docker/runner 下是**启动错误**，报文直接写清楚原因
  （agent 在控制面旁边就能读 `/data`）和出路（`-sandbox local`）。`true` 是 no-op，
  所以已有 compose 文件继续可用；local 下的残留 `=1` 只警告一次。
- **删掉的代码**（逐条 file + symbol 见 `docs/EXECUTION-BOUNDARY.md` §11.3）：
  `sessionAgentLauncher`、`pi.RuntimeConfig.WrapArgv`、`liveness.LauncherArgv`、
  `agentGitEnv`/`agentCheckoutDirs`、`resolveAgentInSandbox`，以及只用来在两条路之间
  切换的接线。`-sandbox local` 需要的路径一条没动（本地子进程、无 marker、in-process registry）。
- **stdio MCP 的诚实答案**：不搬进沙箱（凭据就得进容器），也不搬到控制面（那是把租户代码
  放到数据库旁边），而是**拒绝并把拒绝显示出来**：`GET /v1/mcp/servers` 返回
  `agent_isolated` 与 `unavailable{name:{code,reason}}`，MCP 面板显示
  「stdio MCP 服务器在隔离模式下不可用：<原因>」，会话的 `.pi/mounted.json` 用
  `mcp_unavailable` 记录它，并且不把它写进 `mcp_servers`。判定只有一个地方
  （`internal/mcpstage`），面板、manifest、启动日志用同一句话。

#### 实测（Windows 11 / Docker Desktop，7724，compose 默认隔离实例；镜像 `piforge:stage4`）

三个镜像都是这一棵树（控制面 `sha256:9e44d0b207a1…`）：

```powershell
pwsh scripts/build-app-image.ps1 -Tag piforge:stage4
docker build -f deploy/Dockerfile.runner -t piforge/sandbox-runner:stage4 .
docker build -f deploy/Dockerfile.sandbox-agent --build-arg AGENT_IMAGE=piforge:stage4 -t piforge/sandbox-agent:stage4 .
```

启动就是 `docker compose -f docker-compose.runner.yml up -d`，只额外给了镜像 tag、端口
7724、独立数据卷 `pf-stage4-data`、种子账号、runner token 和 volc provider（密钥从
`~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读进进程环境，从未打印）：

```
startup: sandbox placement resolved sandbox=runner browser=remote terminal=remote files=remote
startup: the agent runs inside the sandbox sandbox=runner user=gem pi=pi launcher=/opt/piforge/piforged
startup: reattached to surviving agents count=0
docker inspect piforge -> 只有 pf-stage4-data -> /data
```

**1. 一条真实 prompt：写入、跑测试、提交，全在沙箱里**

`写一个 Node 脚本 primes.js 打印前 10 个素数，配一个 test.js 并运行它，然后 git commit`
（`send` 1069 ms 接受）：

```
settled=True after 17s  process_id=6533
session_file=/workspace/.piforge/sessions/ses_01M69MNMJZV2QNP1M520N5DBF9.jsonl
transcript: write primes.js -> edit test.js -> bash(node test.js) -> bash(git commit)
toolResult: [main bba9ff9] Add primes.js and prime tests / 2 files changed, 43 insertions(+)
docker exec -u gem <sandbox> git -C /workspace/repo log --oneline -1
  bba9ff9 Add primes.js and prime tests
```

`process_id` 是沙箱里的 `pi` pid；`session_file` 是容器路径（工作区只有沙箱看得到）。

**2. 进程与文件系统边界（同一实例）**

```
docker exec piforge ps -ef | grep -c 'pi --mode'                       -> 0
docker exec -u gem <sandbox> ps -o pid,ppid,args                      -> agent-supervisor(2099)
                                                                          agent-launcher(2107)
                                                                          pi(2116)
docker exec -u gem <sandbox> ls /data                                 -> No such file or directory
docker exec -u gem <sandbox> head -c 8 /data/piforge.db               -> cannot open
```

**3. stdio MCP：产品里说得出来，沙箱里什么都没有**

配了一个 http（`stage4docs`，假凭据 `sk-stage4-http-fake…`）和一个 stdio
（`stage4fs`，假凭据 `sk-stage4-stdio-fake…`）。`GET /v1/mcp/servers`：

```json
{"agent_isolated": true,
 "unavailable": {"stage4fs": {"code": "stdio_needs_credentials_in_the_sandbox",
   "reason": "a stdio MCP server is a process Pi starts inside the sandbox, and it
              authenticates with credentials in its own environment; the sandbox has no
              egress point where the control plane could inject them … Use an HTTP MCP
              server instead …"}}}
```

沙箱里那个会话的 `mcp.json`（只有被代理的 http server，token 每会话一个）：

```json
{"mcpServers":{"stage4docs":{"url":"http://piforge:7700/gw/mcp/dmNX2AFc5dqspuMychXeFWkNhABXsQfPEAptaTQu5L8"}}}
```

会话的挂载记录（`docker exec -u gem <sandbox> cat /workspace/repo/.pi/mounted.json`）：

```json
{"skills":["code-review","explain-codebase","test-gap"],
 "mcp_servers":["stage4docs"],
 "mcp_written":true,
 "mcp_unavailable":{"stage4fs":{"code":"stdio_needs_credentials_in_the_sandbox","reason":"…"}}}
```

同一个会话的 `GET /v1/sessions/{id}/tooling` 返回同样的 `mcp_unavailable`（未 provision 的
会话也能回答，因为它是算出来的，不是只读 manifest）。

**凭据没有进沙箱**（每条都是命令 + 计数）：

```
文件里出现真实 provider key（pattern 走 stdin，不出现在 argv）：
  grep -rlF -f - --exclude-dir=proc --exclude-dir=sys /   (as root) -> 0 files
沙箱环境里出现 provider key：
  env | grep -c -E '^(VOLCE|PIFORGE_BOOTSTRAP_PROVIDER_KEY)='           -> 0
两个假 MCP 凭据（http/stdio）：
  grep -rl -e sk-stage4-http-fake -e sk-stage4-stdio-fake /workspace /home/gem /tmp /opt -> 0 files
stdio server 的启动命令（server-filesystem）在 /workspace 里                  -> 0 files
```

**4. 阶段四的拒绝就在启动时（`docker run --entrypoint /app/bin/piforged piforge:stage4 …`）**

```
-sandbox runner -agent-in-sandbox=false
  ERROR piforged exited with an error err="-agent-in-sandbox=false is not available for
  -sandbox runner: the agent always runs inside the sandbox … run -sandbox local for an
  unsandboxed development deployment"
PIFORGE_AGENT_IN_SANDBOX=0（同上）
PIFORGE_AGENT_IN_SANDBOX=maybe
  ERROR … err="PIFORGE_AGENT_IN_SANDBOX must be 0 or 1, got \"maybe\""
-sandbox local -agent-in-sandbox            -> 启动，日志一行 WARN 说这个开关对 local 无效
-sandbox local（不做任何事）                -> 正常启动，agent 是子进程
```

**5. 三把密钥：丢一个会发生什么（每次都是 mv 走 → 重启 → 发一条 prompt → 恢复 → 再发）**

| 密钥（`/data`，0600，32 字节） | 谁用它 | 实测丢失后的错误 |
|---|---|---|
| `master.key` | `crypto.Box`：provider API key（`providers.key_cipher`）与 PR token | `400 {"code":"provider_unavailable","message":"stored key cannot be decrypted"}`（run 的 `stopReason=error`） |
| `gateway.key` | `llmgw` 的 HMAC：每个会话的模型网关 token | 活着的 agent 用它启动时拿到的 token：`401 {"code":"invalid_token","message":"invalid or expired token"}`；**新会话来同样一条 prompt 正常** |
| `secret.key` | store 的 `secretBoxFor`：沙箱 API key（`sandboxes.api_key_cipher`）、MCP 凭据（`mcp_servers.secrets_cipher`）、MCP broker token | `POST /messages` → 500，日志 `store: open sandbox api key: cipher: message authentication failed`；同时活着的 agent 打 broker 得到 `could not read the MCP credential: cipher: message authentication failed` |

三把都恢复（`mv` 回去、重启）之后：`tooling` 又能读到 manifest
（`mounted.skills=code-review,explain-codebase,test-gap`、`mounted.mcp_servers=stage4docs`、
`mcp_unavailable=stage4fs`），同一个会话再发一条 prompt 正常返回 `ok`。

`gateway.key` 那一轮里有两个值得记住的事实：`docker restart` 只花 1260 ms，沙箱容器和
里面 7 个 `pi` 进程一个没动；以及"新会话正常、仅旧 token 失效"——这就是为什么轮换
`gateway.key` 之后必须重启（或等它自然重启）持有旧 token 的 agent。

**6. 备份与恢复（同一条命令，实测走通一遍）**

```powershell
# 备份：一条命令，整个状态卷（db + 三把密钥 + 配置）
docker run --rm -v pf-stage4-data:/data -v ${PWD}:/backup alpine:3.20 `
  tar czf /backup/pf-stage4-backup.tar.gz -C /data .
# 230694 bytes；tar tzf 里有 ./piforge.db ./master.key ./gateway.key ./secret.key

# 恢复：解到一个全新卷，用同一棵树起第二个实例（7723）
docker volume create pf-stage4-restore
docker run --rm -v ${PWD}:/backup -v pf-stage4-restore:/data alpine:3.20 `
  sh -c "cd /data && tar xzf /backup/pf-stage4-backup.tar.gz"
docker run -d --name pf-stage4-restore --network piforge_sandbox-control -p 7723:7700 `
  -v pf-stage4-restore:/data --cap-drop DAC_OVERRIDE `
  -e PIFORGE_DATA=/data -e PIFORGE_GATEWAY_URL=http://pf-stage4-restore:7700 `
  -e PIFORGE_SANDBOX=runner -e PIFORGE_SANDBOX_RUNNER_URL=http://sandbox-runner:7801 `
  -e "PIFORGE_SANDBOX_RUNNER_TOKEN=$tok" -e PIFORGE_SANDBOX_IMAGE=piforge/sandbox-agent:stage4 `
  -e PIFORGE_SANDBOX_NETWORK=piforge-sandboxes -e PIFORGE_DEFAULT_PROVIDER=volc piforge:stage4
docker network connect piforge-sandboxes pf-stage4-restore
```

恢复实例的实测：`/healthz` 200，**没有**重新 seed（`created the first admin account` 没有出现，
说明 DB 是从备份来的），登录后项目列表里就是 `stage4-isolated`，在同一项目里新建会话、发
`只回复 ok`，`stopReason=stop`、正文 `ok` —— 即恢复出来的 `master.key` 还能解开 provider key，
`gateway.key` 也还能签发/校验 token。

**7. `-sandbox local` 与演练（这一轮没有被阶段四影响）**

`pwsh scripts/deliver-check.ps1 -Image piforge:stage4` 走的就是 local 放置，
`-sandbox local` 的代码路径一条都没删：本地子进程、无 heartbeat marker、in-process registry。
`pf-local`（7722）全程没有被碰。

#### 本轮门禁与清理（阶段四）

| 门 | 结果 |
|---|---|
| `go build ./...` / `go vet ./...` | 干净 |
| `go test ./... -count=1` | 全部包 ok（新增 `internal/mcpstage` 5 例、`materialize` 的 manifest 一例、`httpapi` 的 panel/tooling 两例（隔离/本地各一遍）、`cmd/piforged` 的 `TestRejectDisabledIsolation` 13 条；删除 `pi/wrap_test.go` 与 `agent_git_env_test.go`） |
| `pnpm --dir web test -- --run` | **276 / 276**（35 文件；新增 `web/lib/mcp.test.ts` 4 例） |
| `pnpm --dir web exec next build` | 通过 |
| `pnpm --dir e2e test` | **182 / 182 passed（8.6m，零重试）**，exit 0，跑在 `c685e37`（HEAD；这一提交只改文档和一句面板文案）。同一棵树跑过三次：`01f681e` 前后各一次（8.7m 零重试 / 11.9m 有 2 条 flaky 重试后通过），HEAD 一次 |
| `pwsh scripts/deliver-check.ps1 -Image piforge:stage4` | 见本节末的最后一跑 |

**关于演练的三次记录，写清楚免得被误读**：这一轮第一次跑演练时，唯一的失败项是
`the source checkout is clean after the drill`——因为阶段四自己的改动还没提交，脚本看到
`git status` 非空就觉得是它弄脏了仓库（`118 PASS / 1 FAIL`，run `20261101-030116`，
其余 118 条全过，包括 `pf-local` 前后同 id、`/healthz` 200、临时容器/卷都清掉、密钥在所有
文本 artifact 里 0 次）。提交之后跑的那次是 **119 PASS / 0 FAIL**（run `20261101-030532`）。
最后一次提交（面板文案）之后又跑了一次，仍然是 **119 PASS / 0 FAIL**，就是下表这一次。

中间还有两次 `the Playwright UI check exited 0` 失败（run `20261101-032252`、`20261101-032443`），
原因值得记下来：这两次之间我用 `pnpm --dir web exec next build` **手工**重建了
`web/.next`，而 `web/.env.local` 当时存在（`NEXT_PUBLIC_PIFORGE_WS_BASE=ws://127.0.0.1:8080`），
Next 会把这个值内联进 bundle——镜像里的 UI 于是把实时通道指向容器的 8080，文件树收不到推送。
`scripts/build-app-image.ps1` 就是为此把那两个文件临时挪走的（脚本注释里写着）。用规范脚本重建
之后同一棵树立刻是 119/119。结论：**构建镜像只用脚本，不要手工 `next build` 进去**。

| 演练 | 结果 |
|---|---|
| `pwsh scripts/deliver-check.ps1 -Image piforge:stage4` | run `20261101-032706`：**119 PASS / 0 FAIL**，`failure` 为空，101.1 s，镜像 `sha256:4d5448c2aa45…`（`scripts/build-app-image.ps1` 构建；演练跑在 `92c933a`，其代码与 HEAD `c685e37` 相同，差别只有文档和那句面板文案）。演练用 local 放置，`the source checkout is clean after the drill` 为 `<clean>` |

`pf-local`（7722）前后同一个容器 id `7dc14e4a7525…`、`/healthz` 200。一次性实例已删：
compose 栈（`piforge` / `piforge-sandbox-runner` / `piforge-sandbox-image`）、恢复实例
`pf-stage4-restore`、沙箱容器（只按 `^piforge-[0-9a-f]+$` 删）、数据卷 `pf-stage4-data` /
`pf-stage4-restore` / `piforge-ws-e88ac70dc907f95d`、网络 `piforge_default` /
`piforge_sandbox-control` / `piforge-sandboxes`。宿主机上的备份 tar（含三把密钥）用后即删。


### 1.4.7 对抗验收之后的四个修复（2026-11-01）

一次对抗验收（`e2e/.artifacts/adversarial/VERDICT.md`）以租户身份打了一套全新的
isolated 栈。租户边界本身守住了（读不到 `/data`、没有出网、够不到别的租户），
但边界**周围**有四个真缺陷。机制与取舍的完整叙述在 `docs/EXECUTION-BOUNDARY.md` §14，
这里记的是部署侧的事实与实测数字。

#### (1) 租户在沙箱里是 root —— 已经拿掉

沙箱容器现在带：

```
--cap-drop ALL
--cap-add CHOWN --cap-add DAC_OVERRIDE --cap-add FOWNER --cap-add SETUID --cap-add SETGID
--security-opt no-new-privileges
```

五个 capability 不是猜的：镜像以 `--cap-drop ALL` 启动，逐个加回，因为镜像自己的
启动日志点名了缺少它时失败的那一步（`backend/internal/sandbox/docker.go` 的
`sandboxCapAdd` 里每个都写了一行原因）。agent 账号（`gem`）一个都没有
（`CapEff 0000000000000000`）；容器整体是 `00000000000000cb`，Docker 默认的
`NET_RAW` / `SYS_CHROOT` / `MKNOD` / `SETFCAP` / `KILL` / `AUDIT_WRITE` /
`NET_BIND_SERVICE` 全部不在。

`no-new-privileges` 是真正结束 sudo 的那一层：镜像是 setuid-root 的
`/usr/bin/sudo`，而在 `no_new_privs` 下 execve 不再改变 euid，所以 `sudo -n id`
以 `gem` 的身份失败。`deploy/Dockerfile.sandbox-agent` 另外删掉了 sudo 二进制与
`/etc/sudoers.d/gem`，并把 `/opt/piforge` 与 `/opt/piforge/piforged` 设成
root 所有 `0555`——因为没有 `CAP_DAC_OVERRIDE`，连 root 也写不了它；构建时若缺任何
一项会直接失败。

**明确留下的**：工作区里的 skill 树（`.pi/skills/**`）属于租户账号（沙箱文件 API 以
`gem` 写入），0444/0555 挡住的是误写和旧的 sudo 路径，挡不住先 `chmod u+w` 的租户。
要真正冻结需要一个 root 侧的写入口（bind mount 或 root 文件 API），这个镜像没有。

#### (2) 磁盘不限量而 UI 显示配额 —— 两层强制 + 真实数字

- 每个沙箱容器带 `--ulimit fsize=<budget>:<budget>`（`sandbox.Limits.DiskBytes`，
  从 `-sandbox-disk-quota` 经 runner 的 create 请求传下去）。软硬上限相同，租户的
  shell 无法用 `ulimit -f unlimited` 抬高。实测：2 GiB 预算下
  `fallocate -l 3G` → `File size limit exceeded`（SIGXFSZ），文件 0 字节。
- 每 5 秒的沙箱内检查（`httpapi.StartDiskWatch`）：通过沙箱自己的 shell API 跑
  `du -sb /workspace`；超预算 → 停掉该沙箱的 agent、往会话写一条与 507 相同的
  `platform.error`、如果停掉 agent 之后还在长就暂停沙箱。
- 同一个测量喂三个读侧：admin 的 `sandbox_usage[]`、运行边界的 507 拒绝、看门狗。
  测不出来时是 `unmeasurable: true`（页面显示「未知」），不是 `0 B / over=false`
  ——后者正是缺陷：detached 工作区的 `root_dir` 是 `/workspace`，宿主上根本没有这个
  目录，原来的 `du` 什么都测不到。

**这台 daemon 上试过但不能用的**：`--storage-opt size=` 被接受且被静默忽略
（Docker Desktop overlayfs：1 GiB 的 rootfs 写进 1.5 GiB，`df` 仍显示 1007 GB），
而且它也管不到工作区卷；ext4 project quota 需要 `prjquota` 挂载；size 受限的 tmpfs
不持久；loopback 文件系统需要 `CAP_SYS_ADMIN`，而 (1) 已经把它拿掉了。

#### (3) 网关 token 是可携带的 12 小时凭证 —— 1 小时 + 随会话结束

- TTL 从 12 小时降到 **1 小时**（`llmgw.DefaultTTL`；`-gateway-token-ttl` /
  `PIFORGE_GATEWAY_TOKEN_TTL` 可显式调大）。token 每次 agent 启动时新签，所以一小时
  是**泄漏上界**，不是会话长度。
- `Signer.Revoke(sessionID)` 挂在 `pi.Runtime.Stop` 这个唯一收口上：手动暂停、空闲
  暂停、归档、删除、删项目、切模型、空闲回收、进程关闭都会立即作废该会话的 token。
- `.piforge/agents/<session>` 是 `0700`，`models.json`/`mcp.json` 是 `0600`，由沙箱内
  拥有该目录的账号在文件 API 写完（它写 0644）之后 chmod。实测 `ls -ld` 是
  `drwx------`，另一个账号 `cat` 是 Permission denied。

**明确留下的**：撤销集合在内存里。控制面在「agent 停止」与「撤销」之间崩溃、或重启，
撤销集合就没了；那一小时 TTL 是这个窗口的兜底（所以不能再长）。

#### (4) pause/resume 422 —— 边界上做一次 id 归一

`POST /v1/sessions/{id}/sandbox/pause` 在沙箱 READY 时回 422
`sandbox: not found: piforge-sbx_<hash>`：handler 传的是
`sandbox.ID(user, project)` = `sbx_<hash>`，容器叫 `piforge-<hash>`，而 provider 的
`resolve()` 只认 `piforge-` 前缀，于是去找 `piforge-sbx_<hash>`。resume 同样坏，
所以空闲暂停根本停不下任何沙箱——也就是 (3) 里限制 token 花费的那个机制。

现在只有一个归一化：`sandbox.NormalizeSandboxID` 接受 `piforge-<hash>`、
`sbx_<hash>`、裸 `<hash>`，一律给出容器名；所有生命周期方法都走它，所以进程内的
Docker provider 和 runner 的 HTTP API 都接受两种形式。handler 改为传行里记的
provider id，而不是重新推导 pair id。

回归测试：`TestNormalizeSandboxID`（两种形式 + 空格 + 「不认识就诚实地 not found」）、
`TestDockerPauseAndResumeAcceptBothIDForms`（真容器，按 `sbx_<hash>` 暂停、按容器名
读回、两种形式都能唤醒）、`TestSandboxPauseAndResumeUseTheRowID`（HTTP 边界：provider
必须收到行里的 id；在旧代码上失败信息是
`pause reached the provider with [sbx_…], want [piforge-…]`）。



### 1.4.8 第三轮：磁盘突发的上界与可落库的撤销（2026-11-01）

第二轮对抗验收（`e2e/.artifacts/adversarial2/VERDICT.md`）留下三条「运维会担心」的事，
这一轮关掉其中两条，第三条（token 本身是可携带的一小时凭证）如实写明、不假装关掉。
环境：`pwsh scripts/up-isolated.ps1`（7723，`piforge:isolated` /
`piforge/sandbox-agent:isolated`），`pf-local`（7722）全程未动。

#### (1) 撤销落库：重启不再复活 token

缺陷：`Signer.Revoke` 只改内存里的 `map[string]int64`。暂停当时是对的，控制面在这一小时
TTL 内重启一次，计数器从 0 重新开始，**重启前签发的 token 全部重新可用**。旧代码的注释
还把这写成了设计（「状态在内存里是故意的：它描述的是活进程」）——但 token 描述的不是
活进程，它是一张一小时内有效的现金券。

机制：

- 新表 `gateway_revocations(session_id PRIMARY KEY, generation, revoked_at, expires_at)`
  （`store/gateway_revocations.go`）。`RevokeGatewayToken` 在一个事务里读+加+写（写池是
  单连接，所以两个并发 stop 不会都写成 1），`expires_at = now + TTL`，也就是这次撤销
  能影响的最后一个 token 的到期时刻。
- 写就发生在让 token 失效的那次调用里（`Signer.Revoke`，来自 `pi.Runtime.Stop`），
  不是队列：`200 PAUSED` 意味着行已经在盘上。写失败时内存里仍然生效，并且把「这次只有
  内存在挡、重启会复活」写进日志。
- `Signer.Verify` 每次 `/gw/llm` 都读这个 generation（主键读一行，且在 HMAC 校验之后，
  所以未认证的请求打不到数据库），与内存快路取 `max`；读失败才退回内存 —— 永远不比旧
  行为更松。
- `Signer.Issue` 读同一个数，所以**恢复后新签的 token 带着落库的 generation**，立刻可用；
  同时 `ExtendGatewayRevocation` 把行的 `expires_at` 推到新 token 的到期时刻，清扫不会
  在 token 还有效时把它的依据删掉。
- 清扫：`SweepGatewayRevocations` 删 `expires_at` 已过的行，`cmd/piforged` 每 10 分钟跑
  一次。**这张表故意没有指向 `sessions` 的外键**：删会话时如果级联删掉墓碑行，那个会话
  在撤销前签发（generation 0）的 token 会重新变得有效。
- **关机是唯一不落库的 stop。** 容器里的 agent 按设计要活过控制面（§1.4.5 阶段三），而它
  没有新 prompt 就签不出新 token；关机时撤销等于把阶段三推翻。钩子在
  `cmd/piforged/gateway_revocations.go` 的 `revocationHook`，`drain()` 注册在
  `rt.Close()` 之前（defer 后进先出），所以先 drain 再 Close。用户/平台造成的每一种 stop
  （暂停、归档、删除、切模型、空闲回收）都照常落库。

证据（`scripts/probe-gateway-revocation.ps1`，产物在
`e2e/.artifacts/gateway-revocation/`；token 只打印长度、sha256 前缀和 claims，不打印值）：

```
token A: len=204 sha256=0e8984… session=ses_… provider=volc g=0 ttl_s=3600 baseUrl=…/gw/llm/<token>/v1
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

**这段里有两行现在不成立了**：`token A/B works from the host` —— §1.4.9 之后 token 绑到它自己的
沙箱，从宿主是 `403`。`scripts/probe-gateway-revocation.ps1` 已经把这两次调用改到沙箱里
（`Get-ModelsStatusInSandbox`），「是否已死」的判断仍在宿主上做（容器暂停时跑不了命令，那里
死 token 是 `401`）。其余各行（generation、落库的 `on_record`、各个 `401`）不变，仍由同一个
脚本复测。

测试：`TestRevocationSurvivesAControlPlaneRestart`（真 store：撤销 → 关库 → 开库 → 新
signer 拒绝旧 token，且它内存里的集合是空的，避免「用内存碰巧过了」）、
`TestResumedGenerationIsValidAfterARestart`、`TestSweepKeepsAResumedTokenAlive`、
`TestSignerWithoutAStoreStillRevokes`；store 侧
`TestGatewayRevocationSurvivesReopen` / `…SweepRemovesOnlyExpiredRows` /
`…ExtendKeepsResumedTokenAlive` / `…HaveNoCascadingForeignKey` / `…OutlivesTheSession`；
关机例外由 `TestRevocationHookSkipsOnlyTheShutdownStop` 钉住。

#### (2) 磁盘突发：实测 3.27 倍，之后 1.05 倍

先把「今天到底多重」量出来。`scripts/probe-quota-overshoot.ps1` 建租户/项目/会话，然后在
沙箱里以 `gem`（PTY 和 agent 用的是同一个身份）突发写，同时从宿主每 200 ms 采一次
`du -sbx /workspace`。改动之前（`piforge:before`，HEAD `0592c27`）：

```
sample    194 ms      58845315 bytes  (0.05 GiB)
sample   4118 ms    2122442883 bytes  (1.98 GiB)   <- 到预算
sample   8104 ms    4605479043 bytes  (4.29 GiB)
sample  10085 ms    6874605699 bytes  (6.40 GiB)   <- 探针自己的 6 GiB 上限
RESULT mode=files quota_bytes=2147483648 max_bytes=7029794947 ratio=3.27 paused=false enforcer=absent state=READY
```

十秒写进 2 GiB 预算 7.03 GB，写进程还在跑、沙箱还是 READY，控制面只在 4.90 GB 时打过一条
WARN。第二轮验收在同一机制上量到 3.95 GB 和 5.41 GB —— 所以诚实的说法是「取决于写速，
约 2–3.3 倍预算」，不是一个固定数。

四个选项都在**这台 daemon 上量过**，不是纸面比较：

| 选项 | 在这台 daemon 上的实测 | 结论 |
|---|---|---|
| (a) 更短且自适应的看门狗间隔 | 固定 5 s → 有 run 在跑时 2 s、空闲 10 s；每沙箱各自计时，驱动 tick 500 ms | **采用**：它只是把窗口变窄，仍然是 `间隔 × 写速` |
| (b) size 受限的 tmpfs 工作区 | `--tmpfs /workspace:size=2g`：第二次写正好在 2147483648 B 处 `No space left on device` —— 真的是硬上限；但 `docker restart` 之后 `total 0`，整棵树没了 | **否决**：pause/resume、文件面板、agent 的产出都要工作区持久；tmpfs 还和 `--memory 2g` 抢内存 |
| (c) `--storage-opt size` | 被接受（`HostConfig.StorageOpt={"size":"1g"}`）且被静默忽略：`df /` 仍报 1006.9G，而且它管不到工作区卷 | **否决**：实测是空操作 |
| (d) 沙箱内的配额执行器 | `piforged quota-enforcer`：每 100 ms `du -sbx`，对"持有可写文件描述符的进程 + 它所在的进程树"发 SIGSTOP；只有出现新的写进程才升级 SIGKILL；marker 在 `/workspace/.piforge/quota-brake.json`，日志在 `/tmp/quota-enforcer.log` | **采用**：四个里唯一能在突发进行中当场刹住的 |

**(a)+(d) 一起，代价写清楚：**

- **峰值从"一个窗口"变成"一次在途写"。** 同一探针、同一栈，改动之后：
  **2 265 053 457 B = 1.05x**（8 MiB 文件流；提交后的镜像上复跑为 2 172 778 769 B = 1.01x）、**2 156 001 554 B = 1.00x**（一个 6 GiB
  文件被 RLIMIT 截断后再续小文件）、**2 171 730 202 B = 1.01x**（两者并发）。
  写进程被找到时是 `T` 状态，brake marker 里点名了它们，沙箱保持 READY —— 平台看门狗
  这次没被迫暂停。
- **它仍然不是文件系统配额。** 上界是 `du` 间隔 + 反应时间（在实测约 1 GB/s 的突发下约
  0.2 s 的写入）加上一次 `write()` 的在途量。能跑赢 `/proc` 扫描的写者，或者干脆
  `kill` 掉执行器（同 uid，做得到）的租户，剩下的就是平台看门狗；下一次容器启动会重新
  跑 hook，因为脚本和二进制在镜像里是 root 所有 `0555`。
- **CPU**：2 GiB / 256 文件上 `du -sbx` < 1 ms，每秒 10 次是微秒级；树很大时
  `NextInterval` 退避到 3 倍 walk 时间（上限 5 s），所以 20 万文件的 checkout 不会被
  每秒走十遍。
- **冻结不是杀死**：被刹住的是 `SIGSTOP`，可恢复；升级到 `SIGKILL` 需要冻结之后又长了
  **1 MiB**（`growthFloor`），也就是真的出现了新写者，而不是 `du` 的字节噪声。
- **租户看到什么**：它的 shell 停住（进程 `T`），平台看门狗把与 507 相同的那条消息写进
  会话，§10.5 引了原文；产品里没有「解冻」按钮 —— 恢复方式就是暂停/恢复，和超配额树一样。
- **为什么不由控制面启动**：本仓交付的隔离部署用 runner 提供方，它刻意没有 exec 端点
  （§1.4）。所以执行器做成镜像的一部分：基础镜像的 `RUN_HOOK_PRE_SERVICES` 在沙箱用户
  建好之后、服务启动之前跑 `deploy/quota-enforcer.sh`，用 `su` 降到 `gem`（剥掉
  `CAP_KILL` 之后只有同 uid 能发信号），预算读 `/proc/self/limits` 的 `Max file size`
  —— 和每文件 RLIMIT 是同一个数，不引入第二份配置。

测试：`TestDecide`（杀死决策的表驱动：未超、刚超、超且已冻结、超且仍在长、显式阈值、
无上限、计数为 0）、`TestNextInterval`（基线/慢 walk 退避/下限/上限）、
`TestDefaultsAreTheDocumentedOnes`、`TestWatchIntervalIsFastWhileActiveAndSlowWhenIdle`
（2 s / 10 s）、`TestDiskWatchScheduling`（due/schedule/reset，间隔不是注释）、
`TestPlatformWatchIsSlowerThanTheEnforcer`、
`TestRunningSandboxIDsFollowsTheStatusAndTheWorktree`（哪块沙箱有 run 在跑，决定用哪个
间隔）。

#### 本轮仍然留着的一条 → 下一节关掉了

（这一小节当时写的是：token 仍是一小时内可携带的 bearer 凭证，在宿主上用 `/gw/llm`
能答，不绑定沙箱/容器/地址；落库的撤销界定的是它的**寿命**，不是**持有者**。）

**它已经不再是那样了**：第四轮（§1.4.9）把 token 绑到了它自己的沙箱 —— 源地址 + per-sandbox
证明。上面这段保留下来，是因为它是当时诚实的说法；现在成立的说法在 §1.4.9 和
`docs/EXECUTION-BOUNDARY.md` §16.3。还没被覆盖的（agent 在自己沙箱里当然能用、`-sandbox
local` 不绑定、mTLS 没做）也在那两处逐条写明。

### 1.4.9 第四轮：token 只在自己的沙箱里能用（2026-11-01）

第三轮结束时留下的最后一条风险是「token 是**一小时内可携带的 bearer 凭证**」：验收时
它就是**从宿主**用成功的。这一轮把它关掉，而且没有把 pause→resume / 控制面重启弄坏。

**选型：同时做 (b) 源地址绑定 与 (a) per-sandbox 证明；(c) mTLS 明确不做。** mTLS 是唯一
不依赖「网络视图正确」的方案，但它要 CA、要 per-sandbox 证书签发与轮换、要第二个监听、
还要沙箱里的 SDK 信任那个 CA —— 这一轮做不完，所以不做，写在这里而不是假装考虑过。

两半各管什么，以及**单独一半会漏什么**：

| 一半 | 是什么 | 单独一半会漏掉 |
|---|---|---|
| (b) 源地址 | 请求的 TCP 源地址必须是该沙箱在租户桥上的地址 | 「两条沙箱看起来一样」的网络视图：SNAT、网关前面的代理、解析器答错。它还是**这套部署形状**的性质（控制面加入了每条租户桥），不是协议的性质 |
| (a) 证明 | `X-PF-Sandbox-Proof: HMAC-SHA256(per-sandbox secret, token)` | 「沙箱里面是谁」：agent 就跑在那儿，任何以该账号运行的进程都能读 agent 的环境，就像能读 `models.json` 一样。证明和 token 一样可被复制 —— 只是多一个文件，不是对 agent 的第二因子 |

组合起来才是结论：**地址这一半复制不走**，所以从容器里拿出去的 token（文件，或 `env`
转储）在宿主、同租户的兄弟沙箱、别的租户的沙箱里都不可用；秘密是给「网络视图错了」
准备的皮带，因为两条沙箱就算源地址看起来一样，秘密也不同。

**机制**（细节与代码位置见 `docs/EXECUTION-BOUNDARY.md` §16.3）：

- token 的签名载荷里多一个 `b`（沙箱 id = 容器名），复制 token 改不掉它；没有 `b` 的
  token 不绑定 —— 那是 `-sandbox local`（agent 就跑在宿主上），启动日志用 WARN 说清楚。
- 地址：网关用**本进程的解析器**查沙箱名（隔离部署里就是 Docker 内建 DNS，控制面在每条
  租户桥上，内建 DNS 对容器名是权威），和 `r.RemoteAddr` 比，**不看** `X-Forwarded-For`。
  缓存 2 s；不一致时**强制重新解析一次**再拒绝，所以容器换地址会被跟上；解析失败退回
  上一次已知地址（DNS 抖一下不该把 agent 锁在自己网关外面）；从来解析不到 = 拒绝。
  **没有回绑钩子，也没有会过期的表** —— 这正是它能在 pause→resume 和控制面重启后活下来
  的原因。
- 证明：32 字节 `crypto/rand`，用部署的 AES-GCM box 封在 `sandboxes.proof_cipher`，在给
  这块沙箱签 token 的那一刻惰性生成（`EnsureSandboxProofSecret`）。**它从不进入容器**；
  过界的只有 `HMAC(secret, token)`（只值一个 token），而且放在 **agent 进程的环境变量**
  `PIFORGE_SANDBOX_PROOF` 里 —— 不是容器 env，也不在任何命令行上。`models.json` 里写的是
  引用，Pi 按请求解析（它自己的 schema：headers "Values resolve like apiKey"，apiKey 支持
  `$NAME` 环境插值）：

  ```json
  "headers": { "X-PF-Sandbox-Proof": "$PIFORGE_SANDBOX_PROOF" }
  ```

- 顺序：签名（401）→ 撤销 generation（401）→ 地址（403 `sandbox_address_mismatch` /
  `sandbox_address_unresolved`）→ 证明（403 `sandbox_proof_missing` / `_invalid` /
  `_unavailable`）→ 取 provider。所以**死 token 从哪儿都是 401**（包括它自己的沙箱），
  **活 token 只有在自己沙箱里才是 200**。每次拒绝一行 WARN，带 session、sandbox、源地址、
  原因。
- **MCP broker token 同样绑**（它也是控制面签的、也躺在沙箱的 `mcp.json` 里）：只有地址
  这一半 —— Pi 的 `mcp.json` 没有地方为每个请求插一个「文件里没有的值」；拒绝仍是 broker
  惯用的 `401`（body 和别的拒绝一样），原因在日志里。

**证据**（`scripts/probe-sandbox-binding.ps1`，隔离栈 7723，产物
`e2e/.artifacts/sandbox-binding/20261101-112610`；token 只打印长度/sha256 前缀/claims；
沙箱内的调用通过 stdin 喂给 `sh -s`，所以 token 和证明都不出现在任何 argv 里）：

```
PASS  the first prompt settles with a model answer            (running=False answer=READY)
token A: len=246 sha256=400441adbf2e … sandbox_claim=piforge-b4e0a17c5c0fbc35 g=0
PASS  models.json names the proof as a reference and does not contain it  (header=$PIFORGE_SANDBOX_PROOF)
PASS  GET <baseUrl>/models from INSIDE the sandbox -> 200     (status=200)
PASS  the same call without the proof is refused              (403 sandbox_proof_missing)
PASS  the same token from the HOST is refused -- even with the proof copied along
      (403 sandbox_address_mismatch: … 172.27.0.4; the request arrived from 172.25.0.1)
PASS  token A from a SIBLING sandbox of the same tenant is refused  (403 … arrived from 172.27.0.5)
PASS  token A from ANOTHER TENANT's sandbox is refused             (403 … arrived from 172.28.0.4)
PASS  token A still works from its sandbox after the restart (same token, no re-mint)  (200)
PASS  the session's agent still works after the restart       (accepted=True token_unchanged=True)
PASS  token A is refused after the pause / still after the restart  (401 / 401, on_record=4)
token B: len=254 sha256=e76a23f9af98 g=1
PASS  token B works from the resumed sandbox                  (200)
PASS  the broker token answers from its own sandbox           (200 {"status":"ok"})
PASS  the broker token is refused from the host / a sibling   (401 / 401)
RESULT in_sandbox=200 host_with_proof=403 host_without_proof=403 sibling=403 other_tenant=403
       same_token_after_restart=200 revoked_after_pause=401 revoked_after_restart=401
       resumed_token=200 mcp_in_sandbox=200 mcp_from_host=401 on_record=4
failures=0
```

**现在成立 / 现在仍不成立**（完整版在 §16.3）：

- 成立：复制出容器的 token 对它整个生命周期内、在任何别的地方都无效（宿主、同租户兄弟
  沙箱、别的租户，连证明一起复制也不行）；同一个 token 能活过控制面重启；pause 之后
  401，恢复后的新 token 立刻可用。
- 仍不成立：**沙箱里的 agent 自己当然能用**（它本来就该用；这一轮界定的是「在哪儿」，
  不是「谁」）；`-sandbox local` 不绑定（启动日志 WARN 说明）；地址这一半信任这套部署的
  网络形状，将来若在网关前面加一层改写源地址的东西，就退化到只剩证明 —— mTLS 仍是补上
  这一点的下一步。

**门禁**：`go build ./...`、`go vet ./...`、`go test ./... -count=1`、`pnpm --dir web test
-- --run`（292）、`pnpm --dir web exec next build`、e2e（182）、`pwsh
scripts/deliver-check.ps1`（119/119，`PIFORGE_DRILL_KEY` 由 `scripts/drill-key.ps1` 读入
子进程，只打印长度和 sha256 前缀）。

### 1.4.10 第五轮：租户树上的每一个读取（FIX 1），以及磁盘预算的三层（FIX 2/3，2026-11-01）

第三轮对抗验收（`e2e/.artifacts/adversarial3/REPORT.md` 攻击 3）给出的不是"某个校验漏了"，而是一类
原语：**控制面用 root 读了一棵租户能控制的树，读的时候跟着符号链接走。** 具体路径是
`POST /v1/skills/import` → `git clone` → `findSkills` 用 `filepath.WalkDir` 找 `SKILL.md`，再用
`os.ReadFile` 读目录下的每个文件；`WalkDir` 不跟链接，`os.ReadFile` 跟。于是一个正常的租户账号，
用一个仓库里的 `leakskill/key.txt -> /proc/1/environ`，就拿到了控制面自己的环境——**真实的 provider
key（36 字符精确匹配）**、`PIFORGE_MASTER_KEY`，以及 `/data/master.key`、`/data/secret.key`、
`/data/piforge.db`、别人的 `/data/pi-agents/<session>/models.json`。报告里的测量是
`import STATUS=200`、`provider_key_occurrences=1`。

这一轮把这一类读取收拢成一条规则，并在每条读路径上落实（规则本身写在 §1.8，那是一节给后来人看的
规范）。三处改动：

**FIX 1：租户可控的树只按"每个路径分量都是真目录/真文件"来读**

- 新增 `backend/internal/tenantfs`：`Tree.ReadFile` / `Tree.Walk` / `Tree.WalkFiles`。
  读一个路径前，逐分量 `os.Lstat`，任何分量是符号链接就拒绝；打开后 `os.SameFile` 比对（防止检查与
  打开之间被换成链接），再用 `filepath.EvalSymlinks` + `filepath.Rel` 确认实体落在导入根内。
  `Walk` 在整个树上拒绝**任何**链接——包括没有任何 `SKILL.md` 的目录里的链接——因为拒绝必须是
  "整次导入"，不是"这一个文件"：链接是敌意，不是倒霉。拒绝是类型化的 `*tenantfs.Refusal`，带
  `Path`（仓库内路径，租户认得）/ `Absolute`（日志用）/ `Kind`。
- git 这一侧两道：`git -c core.symlinks=false clone --depth 1`（链接落盘就是**内容为目标路径的普通
  文本文件**，不在控制面磁盘上产生活链接），并且克隆后读 `git ls-files -s -z`，任何 `120000` 条目都
  拒绝整次导入（`refuseDeclaredSymlinks`）。两道都留着：第一道让链接不可能变成活对象，第二道让"仓库
  声明了链接"这件事本身被拒绝并点名，而不是悄悄导入一个 key 文件里写着 `/proc/1/environ` 的 Skill。
- zip 这一侧本来就有绝对路径 / `..` / 非普通文件（含符号链接条目）的拒绝；现在外面那层 `findSkills`
  也走 `tenantfs`，所以解压之后即使出现链接也一样被拒。
- 读回（`GET /v1/skills/{id}/bundle`）也走 `tenantfs`：这个目录是控制面自己写的，但它被服务给租户，
  一次恢复/一次手工拷贝就可能带进一个链接，读回没有理由比导入更宽松。

**实测（隔离栈 7723，镜像从本轮 HEAD 重建；仓库用 smart HTTP 提供，和报告里的
`http://host.docker.internal:9011` 同一个形状）**

```
ATTACK 1  POST /v1/skills/import {"url": ".../leakrepo.git"}        -> 400
          {"code":"invalid_argument","message":"导入被拒绝：仓库里的 \"leakskill/key.txt\"
           是符号链接。…整次导入都不会进行，也没有创建任何 Skill。"}
ATTACK 2  leakskill/key.txt -> /data/master.key                     -> 400，同样点名 leakskill/key.txt
skills    before=[code-review,explain-codebase,test-gap] after=[code-review,explain-codebase,test-gap]
          没有 leakskill 行、没有 bundle 目录
clone     naive_clone_is_symlink=yes          （仓库确实是报告里那个形状）
          fixed_clone_is_symlink=no           （confined clone 不在磁盘上留活链接）
          fixed_clone_content=/proc/1/environ （链接落盘为文本，就是目标路径本身）
          fixed_clone_has_master_key_name=0
legit     POST import -> 200 {"name":"pdf-tools","files":2} ；bundle 里只有 SKILL.md + scripts/split.py
leak-scan 36 字符 drill key 在全部 1016 字符的响应/工件里出现 0 次；PIFORGE_MASTER_KEY 出现 0 次
```

**FIX 2：磁盘预算覆盖所有可写层，执行者不再是"租户能杀掉的那个进程"**

- 以前只量 `/workspace`：报告测到 `/tmp` 4.84 GB、workspace 125 KB、没有任何事件，因为 `/tmp`、
  `/var/tmp`、`/home/gem` 是容器的可写层，不在那个数里。现在两个数相加：workspace 一棵树，加上
  **容器根文件系统减去开机基线**（`du -sbx /`，`-x` 让卷不被重复计，基线由镜像的 hook 在任何租户
  进程之前写进 `/run/piforge/disk-baseline`，root 所有 0644）。sandbox 内的 enforcer 100 ms 量
  workspace、1 s 量第二项；平台侧的同一次测量用 `find /workspace` + `find / -xdev` 做同样的减法，
  所以管理页和 enforcer 看到的是同一个"磁盘"。
- 执行者本来必须以 `gem` 运行（它要 signal 租户的进程，而容器 `CapEff=0`，root 反而没有 CAP_KILL），
  所以旧的 `kill -9 $(pgrep -f quota-enforcer)` 一次就把它拿掉。现在镜像的 hook 起一个**root 所有的
  监督循环**（`deploy/quota-enforcer.sh`，`nohup setsid /bin/bash -c 'while :; do su …; sleep 0.2; done'`）：
  gem 不能 signal 另一个 uid 的进程，所以租户杀掉 enforcer 只是 ~0.2 s 的空档；平台侧的磁盘看门狗
  在每次测量里 `pgrep -f '[q]uota-enforcer'`，缺失就用同一个命令行把它重新拉起来（hook 没跑过的
  沙箱也能自愈，不需要重启容器）。

```
baseline  /run/piforge/disk-baseline = 9 044 327 151 B（root:root 0644）
process   pid 275 root  /bin/bash -c while :; do su -s /bin/sh gem -c 'exec … quota-enforcer …'
          pid 277 root  su -s /bin/sh gem -c exec … quota-enforcer …
          pid 278 gem   /opt/piforge/piforged quota-enforcer -limit 2147483648 -root /workspace
                        -sysroot / -baseline /run/piforge/disk-baseline -interval 100ms
tenant    docker exec -u gem … pkill -9 -f '[q]uota-enforcer'
          -> pkill: killing pid 275 failed: Operation not permitted
             pkill: killing pid 277 failed: Operation not permitted      （root 的杀不掉）
          -> gem enforcer 278 被杀；3 s 后 pid=1405                        （监督循环拉起来了）
          -> docker exec -u gem … kill -9 275 -> Operation not permitted
measure   1.5 GiB 一个文件写进 /tmp，workspace 129 306 B
          admin sandbox_usage bytes = 1 539 211 060                         （/tmp 进了预算）
          再写 1.5 GiB：over=1，容器 running，控制面有事件                 （以前是"没有事件"）
```

**FIX 3：pids 的余量与检测时间**

- `deploy/tenant-limits.json` 的 `pids` 从 256 回到 512：镜像自己的运行时实测占到 216/256，256 只剩
  ~40 个 fork 的余量，一行 `for i in $(seq 80); do sleep 600 & done` 就把沙箱顶死。512 留 ~296 个
  槽位（本轮实测 `pids.max=512 pids.current=196`），wedge 需要一次真正的风暴而不是一次手滑。
- 检测从"每 10 s 探一次、连续失败两次"改成"每 5 s 探一次、失败立刻在 1.5 s 后确认一次"：
  `defaultSandboxWatchInterval = 5s`、`sandboxConfirmDelay = 1.5s`、`sandboxWedgedStrikes = 2`
  （第一次失败 + 确认失败）。"加载中的宿主机错过一次探针"仍然不会重启任何健康容器，因为确认那一次会成功。
  测试相应改成两半：`flaky` 一次（第一次失败、确认成功）不重启；持续失败在**同一趟**里重启。
- 实测（同一套隔离栈，`for i in $(seq 441); do sleep 900 & done` 把 512 的 cgroup 顶死）：

```
pids       max/current = 512 / 191（顶死前）
first miss 05:01:00.810  strike=1  "[Errno 11] Resource temporarily unavailable"
repair     05:01:02.374  "stopping and starting the container" reason="…(confirmed)"   （1.56 s 后）
answered   05:01:20.122  "the sandbox answers again" elapsed=17.7s                    （端到端 19.3 s）
```

  第三轮报告测到的是 DETECTED t+76 s、RECOVERED t+87 s；现在是"第一次探测失败 → 1.56 s 确认 → 修复"，
  端到端 19.3 s。

**这一轮没有做的事，写在明处**

- `file://` 仓库地址仍然被接受（自托管场景），它能把控制面自己能读到的**本地 git 仓库**当导入源。
  带链接的仓库会被拒绝，但"能指向本机仓库"这件事本身没有收窄——它需要部署级的白名单，不是一次
  路径校验。
- `git clone` 仍会把仓库的工作树完整写到控制面的临时目录：4 MB 的读取上限是**读**的上限，不是
  **落盘**的上限。一个 10 GB 的仓库能填控制面的磁盘（不是沙箱的预算管得到的地方）。
- 硬链接不在 `tenantfs` 的检测范围：git 和 zip 解压都造不出硬链接，所以这两种传输不会产生，但
  "以后加一个 tar 导入"就必须重新想这件事。
- 磁盘第二项是"根文件系统减基线"，它包含沙箱自己的服务写的东西（浏览器 profile、node 缓存）。
  这是诚实的记法（那确实是这个沙箱占的盘），但它意味着一个"什么都没干"的沙箱也会缓慢增长。
- 检测再快也还是"检测 + 修复"，不是资源策略：租户可以循环触发它，一次一条 WARN。
### 1.5 推荐方案下仍然会坏的地方（这一节是重点）

| 事件 | 会发生什么 | 缓解 |
|---|---|---|
| **控制面重启** | 沙箱容器还在（`--restart unless-stopped`）。容器名由 `(user, project)` 派生，`docker inspect` 能找回；端口由 `docker port` 重读，`sandbox_files.go` 的客户端缓存 30s 过期后自愈。runner 模式下端点由 runner 的 `GET /v1/sandboxes/{id}` 重问，同样自愈 | 已经是现状，没问题。但**沙箱里的进程**（Chromium、AIO 的 shell 会话）在 daemon 重启时全丢，终端会断 |
| **daemon 在远程机器** | `-v` 的源路径在 daemon 主机解析（Docker 文档原话：bind mount "created to the Docker daemon host"，[Bind mounts](https://docs.docker.com/engine/storage/bind-mounts/)）。`-v` 会**静默创建一个空目录**，于是容器看到空 `/workspace`，控制面往自己磁盘写，两边都不报错。`workspaceMount` 比较的是字符串，两台机器上的 `/data/sandboxes/u/p` 完全相同，比较通过 —— 检测不到 | runner 与 daemon 同机；compose 叠加层让沙箱直接挂**卷+子路径**（`SANDBOX_WORKSPACE_VOLUME`，§1.4 第 2 条），宿主路径根本不进这条链路，daemon 在哪台机器上都不影响。控制面仍在 `Ensure` 时把 runner 报的 `root_dir` 与自己算的路径比一次并 WARN —— 但注意它比的是**字符串**：bind 形态下"两边名字相同、实际是两棵树"这件事它看不出来（§1.4.1 第 2 条就是这么坏的）。跨机器时不要靠 SSH 建目录。**阶段二之后这条缺陷类型不再适用于 detached 形状**：那里根本没有宿主路径可解析错（`docker inspect` 只看到沙箱自己的卷） |
| **控制面在容器里，沙箱也在容器里** | `127.0.0.1:<published>` 是控制面自己的 loopback，连不上沙箱。`-sandbox docker` 仍然发布 `127.0.0.1:<port>` 并从 `docker port` 读回 | **已解决**：`SANDBOX_CONTAINER_ENDPOINT=1` 让 runner 不发布端口、返回 `http://piforge-<hash8>:8080`（Docker 内建 DNS），控制面与 runner 都加入 `piforge-sandboxes`。**这一步同时去掉了宿主上的 8080 暴露面** |
| **`docker compose down`** | 沙箱容器不是 compose 管的，不会跟着删；runner 停掉也不删容器（无状态）。`down -v` 会尝试删卷，但只要还有**任意**一个 `piforge-*` 容器（包括 `Created`/`Exited`）挂着它，就只打印 `Volume piforge-data Resource is still in use` 并继续，退出码仍是 0（§1.4.1 实测） | 清理顺序：先 `docker rm -f piforge-*`（只删你认识的），再 `docker compose ... down -v`，然后确认 `docker volume ls` 里没有 `piforge-data`。runner 尚未实现按 label 对账（`POST` 只会建/复用，不会回收孤儿）；`Ensure` 现在会在 `docker run` 失败时删掉自己半创建的对象，但已经存在的孤儿仍要手工清。**阶段二多了一种要清的对象**：`docker volume ls --filter name=piforge-ws-`，每个 detached 沙箱一块，`Destroy` 同样刻意不删它（工作区里是别人未提交的工作） |
| **网络回连** | 沙箱里的 agent 要访问模型网关（控制面），`127.0.0.1` 在沙箱里不是控制面 | 沙箱加入 `sandbox-control` 网络，网关地址用服务名；`gateway-url` 已经是可配置项。现在的 compose 把沙箱放在 `piforge-sandboxes`，**还没有**把它接到网关；多服务部署时要把 `-gateway-url` 指到控制面的服务名并把两个网络连通 |
| **镜像里没有 docker CLI** | `NewDocker` 直接返回 `sandbox: docker: the docker CLI is not on PATH` | 要么加 CLI，要么走 runner。`-sandbox runner` 已经让这条路在 compose 里跑起来：CLI 只在 runner 镜像里（`deploy/Dockerfile.runner`） |

### 1.6 compose 小结

- 单机、单人：`-sandbox local` 就够，别引入 daemon 权限；
- 单机、要多租户边界：**runner**（`docker compose -f docker-compose.yml -f docker-compose.runner.yml up`）；
  过渡期可以用 proxy，但要写明它仍是宿主 root；
- DinD 留给 CI/e2e；
- 无论哪种：**不要发布宿主 8080**，用 docker 网络的容器名当端点（runner 的
  `SANDBOX_CONTAINER_ENDPOINT=1` 就是这个形状）。

---

### 1.7 租户边界：每租户一个网络、每租户一份配额、一条可查的审计

验收报告（`e2e/.artifacts/final-isolated/VERDICT.md` §8 第 3 条）把这条留作最后一个缺口：执行和文件都已经在沙箱里，但**租户之间只隔着认证**。这一节是补上的部分、代价、以及仍然不宣称的东西。设计与取舍写在 `docs/EXECUTION-BOUNDARY.md` §12，这里是运维视角。

#### 1.7.1 网络：一个租户一条桥

每个租户（账号）在第一次建沙箱时由 runner 建一条桥 `piforge-t-<用户 id 的 12 位哈希>`，带标签 `piforge.tenant=<name>` / `piforge.role=sandbox`；沙箱**只**加入这条桥。控制面和 runner 由 `-sandbox-join-containers` 指定，挂在每条租户桥上（否则面板、checkout 和网关都到不了沙箱）。共享的 `piforge-sandboxes` 不再是沙箱的成员。

为什么是"每租户"而不是"每沙箱"：产品规则是"一个 (user, project) 一个沙箱"，而授权边界本身就是**用户**（跨用户读一律 404）。每沙箱一条网络会额外隔开同一账号自己的项目，代价是**每个沙箱**一条网络 + 控制面/runner 各一次 `docker network connect` + 一段地址；而地址池是硬墙——本机 Docker 29.9.2 上第 17 条网络就报 `all predefined address pools have been fully subnetted`，也就是不先写 `daemon.json` 的 `default-address-pools`，每沙箱模式十来个沙箱就撞墙，每租户模式同样的上限换来十几个**账号**。

**不提的部分**：同一租户的两个沙箱共享这条桥，能互相访问 8080，那里仍由镜像自带的每沙箱 API key 挡成 401。这是同一账号自己的项目，但它不是"sandbox 之间不能互通"，文档不这么说。

从租户 1 的沙箱里量（`e2e/.artifacts/tenancy/isolation-probes.txt`）：

```
getent hosts piforge-874760ba7dddd0e5   -> no such host
curl -m 4 http://172.28.0.4:8080/       -> curl: (28) Connection timed out after 4003 ms
nc -z -w 3 172.28.0.4 8080              -> no route / timed out
curl <自己的 gateway baseUrl>/models    -> http_code=200
curl http://piforge:7700/v1/projects    -> http_code=401
curl http://piforge:7700/healthz        -> http_code=200
```

按名字解不出邻居（不同桥、不同 DNS 作用域），按地址没有路由（Docker 在桥之间丢弃）。网关 200 与 401 是"还能用、但仍不被信任"的两半。

运维命令：

```powershell
docker network ls --filter label=piforge.tenant          # 每条租户桥
docker inspect <sandbox> --format '{{range $k,$v := .NetworkSettings.Networks}}{{println $k}}{{end}}'
docker network inspect piforge-t-<hash> --format '{{range .Containers}}{{.Name}} {{end}}'
pwsh scripts/up-isolated.ps1 -Project <p> -Down          # 连租户桥一起删（按 label）
```

#### 1.7.2 配额：一份策略文件，三个执行点

`deploy/tenant-limits.json`（`PIFORGE_TENANT_LIMITS_FILE`，只读挂到 `/etc/piforge/tenant-limits.json`）给出**默认**与**按账号覆盖**；部署级 flag（`-sandbox-cpus/-sandbox-memory/-sandbox-pids`）是文件缺省时的兜底。启动时校验：未知字段、非数字 CPU、Docker 不认的内存写法、负数，都会**让启动失败并指出是哪个租户**，而不是让某个租户悄悄不限量。

```json
{
  "default": {"cpus": "1", "memory": "2g", "pids": 512, "max_sandboxes": 3},
  "tenants": {"bob@example.com": {"cpus": "2", "memory": "3g", "pids": 1024}}
}
```

三个执行点：

| 限制 | 谁执行 | 在哪 |
|---|---|---|
| CPU / 内存 / 进程数 | `docker run --cpus/--memory/--pids-limit` | `sandbox.Docker.containerArgs`（租户值由控制面算好，runner 在 create 请求里收到） |
| 并发沙箱数 | 建之前数该租户的 `sandboxes` 行 | `provision.checkSandboxQuota`，返回 `tenant_sandbox_limit`（409）+ `{limit,current,tenant}` |
| 磁盘 | 每沙箱 `du` 测量（FR-5.8，未变） | `httpapi.enforceSandboxQuota` |

`--pids-limit` 是新的，也是 fork bomb 的唯一闸门。拒绝是**typed**的并且活到了 HTTP 层：改动之前 provisioning 失败只写一行 WARN，租户在配额上限时看到的是三层之后的 500；现在是 409 + 数字，而且为这次被拒的请求建出来的 session 行会被删掉，不留一个客户端不知道 id 的会话。

实际生效的限制会被记录：provider 从容器自己的 `HostConfig` 读回来，行里存 `cpu_milli`/`mem_mb`，`GET /v1/admin/status` 把它们和租户策略并排报出来（`deployment_status.tenant_limits`），管理页「租户配额与网络」就是这一块。

启动日志一行说明策略，拒绝时引用同一组数字：

```
msg="tenant quota policy" source=/etc/piforge/tenant-limits.json default_cpus=1 default_memory=2g default_pids=512 default_max_sandboxes=2 overrides=1 network_mode=tenant
```

#### 1.7.3 审计：`tool_calls` 的跨租户查询

`GET /v1/admin/audit`（仅管理员）按页返回每一次工具调用：开始/结束时间、耗时、租户（id + 邮箱）、项目（id + 名称）、会话、run、工具名、结果（`ok`/`error`/`running`）。过滤：`user_id`、`project_id`、`session_id`、`tool`、`outcome`、`since`、`until`、`limit`（上限 500）、`offset`；`format=csv` 给同一页的 CSV 下载。

```powershell
# 最近 50 条，某个账号
curl.exe -sS -b cookies.txt "http://127.0.0.1:7723/v1/admin/audit?user_id=usr_...&limit=50"
# 失败的那些，导出 CSV
curl.exe -sS -b cookies.txt -o audit.csv "http://127.0.0.1:7723/v1/admin/audit?outcome=error&limit=500&format=csv"
```

它**不是**取证日志，页面和文档都这么写：`tool_calls` 只存参数哈希与用户在自己转录里已经看到的短预览，不存参数原文。它回答的是"谁在什么时候跑了什么、成没成"，这正是验收报告查不了的那个问题。

#### 1.7.4 一次跑完的证据

`pwsh scripts/soak-tenancy.ps1 -Port 7723` 在一次性栈上建 4 个租户、各建一个项目并**同时**发一条会调用工具的 prompt，然后探测：跨租户不可达（名字 + 地址）、自己网关可达、控制面 `/v1/*` 仍 401、每个沙箱的 CPU/内存/进程数与策略一致、租户到达上限被 409 拒绝并带数字、审计里能查到这四条 run 的工具调用并可导出 CSV。产物在 `e2e/.artifacts/tenancy/`（`soak.txt`、`isolation-probes.txt`、`admin-status-tenant-limits.json`、`audit.csv`）。最近一次：**0 failures，exit 0**。

#### 1.7.5 出口：一个租户能不能上公网（2026-11-01）

§1.7.1 解决的是"租户之间能不能互通"，这一节解决的是它的另一面：沙箱**能不能上公网**。补之前它是能的——`npm install`、`curl`、被投毒的依赖都拿到宿主的全部出网能力，而部署里没有任何一处表达"这是不是故意的"。设计与取舍写在 `docs/EXECUTION-BOUNDARY.md` §13。

策略文件是 `deploy/tenant-egress.json`（只读挂进**控制面和 runner 两个容器**，`/etc/piforge/tenant-egress.json`，`PIFORGE_TENANT_EGRESS_FILE` / `SANDBOX_EGRESS_FILE`）。为什么不在 `tenant-limits.json` 里加一节：配额只由控制面读，出口策略必须由 **runner** 读（桥是它建的），而两个文件的解析器都刻意拒绝未知字段；合成一份会让一个配额笔误能拖垮 runner 的启动。

```json
{
  "default": {"mode": "none"},
  "tenants": {"dev@piforge.local": {"mode": "open"}}
}
```

三种取值，各自的执行方式与覆盖范围：

| 模式 | 怎么执行 | 覆盖 | 不覆盖 |
|---|---|---|---|
| `none`（默认） | 该租户的桥用 `docker network create --internal` 创建：没有默认路由、没有 NAT，Docker 内建 DNS 也不再转发外部名字 | 公网（TCP/UDP）完全不可达；`example.com` 连解析都失败；沙箱里的浏览器、`npm`、`git`、`curl` 全在同一网络命名空间里，同样规则；控制面与 runner 也在这条桥上，所以 `/gw/llm`、`/gw/mcp` 仍然可达 | 同一租户桥上的其它容器（控制面、runner、**同租户的另一个沙箱**，见 §1.7.1）；控制面自己的出网（网关代它去调模型，这是产品本身）；`-sandbox-network-mode shared` 的单机形状；`-sandbox local` |
| `allowlist` | **不执行**：启动时拒绝 | —— | 全部。理由见下 |
| `open` | 普通桥，Docker 做 NAT | 和宿主一样 | —— |

`allowlist` 是"想按域名放行"时最自然的写法，也是这个功能里最容易撒谎的地方：Docker 只能按**桥**过滤，不能按 hostname。诚实的做法只有两种——在租户的内网桥上跑一个过滤代理（`--internal` 下它是唯一出口，但要多一个常驻进程、一张源地址→租户的表、并把 `HTTP(S)_PROXY` 注入每个沙箱），或用宿主上的 `DOCKER-USER` iptables 规则（runner 需要 `--net=host`/`NET_ADMIN`，比已经讨论过的 socket 挂载是更大的信任变更）。两个都没做，所以 `allowlist` **在启动时被拒绝并指出租户**，而不是"接受了但其实是开放的"：

```
tenant egress: tenant "bob@piforge.local": egress mode "allowlist" is not implemented and is
refused rather than accepted: Docker filters a bridge, not a hostname, ...
```

同一道启动闸门还拒绝另外三种说不通的配置：`none` + 共享网络（一条桥装不下两种答案）、共享网络 + 任何按租户的覆盖、`-sandbox local` + 任何 `none`/`allowlist`（宿主上与宿主同网络，没有桥可关）。没有配置文件、又没给 `-tenant-egress-mode` 时：**每租户桥的形状默认关闭**，共享桥的形状保持 open 并在启动日志 WARN、管理页写明"未配置"。方向是"有边界的地方默认关，没有边界的地方说清楚"，不是"沉默即开放"。

改模式要重建桥（`Internal` 不能原地改）：

```powershell
# 当前谁被关着（标签在桥上，不在配置文件里）
docker network ls --filter label=piforge.egress=none
docker network inspect piforge-t-<hash> --format '{{.Internal}} {{index .Labels "piforge.egress"}}'
# 改：编辑 deploy/tenant-egress.json（或 -tenant-egress-mode）→ 重启栈 → 删掉旧桥
docker network rm piforge-t-<hash>
```

不删旧桥就新建沙箱，会得到一条**指名道姓的拒绝**（而不是一条"看起来建好了、其实还开着"的桥）：

```
sandbox: tenant network piforge-t-… was created "open" but this tenant's egress policy is
"none": … Remove it with `docker network rm piforge-t-…` (its sandboxes have to be
recreated) and the next create will build it "none"
```

**排查：沙箱说它没网。** 先分清是策略还是坏了。在 `none` 下 agent 看到的是这几种，都**不是**故障：

```
$ docker exec -u gem <sandbox> curl -m 5 -sS -o /dev/null -w '%{http_code}\n' https://example.com
curl: (6) Could not resolve host: example.com          # 内网桥不转发外部 DNS
$ docker exec -u gem <sandbox> curl -m 5 -sS -o /dev/null -w '%{http_code}\n' https://1.1.1.1
curl: (28) Connection timed out after 5001 milliseconds   # 字面地址：没有路由（也可能直接 Network is unreachable）
$ docker exec -u gem <sandbox> npm install
npm error code ENOTFOUND / EAI_AGAIN …
```

三条自检，全都要成立才算"策略生效、产品正常"：

```powershell
docker network inspect piforge-t-<hash> --format '{{.Internal}}'          # true
docker exec -u gem <sandbox> curl -m 5 -sS -o /dev/null -w '%{http_code}\n' http://piforge:7700/healthz   # 200
docker exec -u gem <sandbox> curl -m 5 -sS -o /dev/null -w '%{http_code}\n' http://piforge:7700/v1/projects # 401
```

（沙箱自己的模型网关地址在 `/workspace/.piforge/agents/*/models.json` 的 `baseUrl`，`<baseUrl>/models` 在 `none` 下也是 200 —— 这条链路是 agent 唯一的"外面"，也是"关掉公网不等于关掉产品"的证据。）

要放行某个租户：把它的 `mode` 改成 `open`、重启栈、`docker network rm` 旧桥，下一次建沙箱就是普通桥。**别用 `--internal` 的桥去试白名单**：那等于 `none`，白名单条目不会生效（也不会被读取，因为 `allowlist` 启动就失败了）。管理页 `/admin/status` 的「租户配额与网络」里，`沙箱出口` 一行给出默认模式、来源，以及每个账号的 `出口：关闭 / 开放`，不用登录宿主去看配置文件。

证据：`pwsh scripts/probe-egress.ps1 -Port 7723`（自带 down/up，结束自动拆栈）在同一个栈上把默认租户设成 `none`、把 `dev@piforge.local` 设成 `open`，从**沙箱内部**量 DNS、按名字与按字面地址的公网请求、控制面 `/healthz` 与 `/v1/*`、自己的网关 URL、桥的 `Internal` 与标签、管理页报告，并在**被关闭的租户**里跑一条真实 prompt。产物在 `e2e/.artifacts/egress/`。最近一次（0 failures，exit 0）：

```
closed: network=piforge-t-29ec2c7a2fa8 Internal=true  label piforge.egress=none
opened: network=piforge-t-6a0e6d79cf7c Internal=false label piforge.egress=open
closed: getent hosts example.com -> DNS-FAILED
        curl -m 5 http://example.com -> (6) Could not resolve host (000)
        curl -m 5 https://1.1.1.1    -> (7) Couldn't connect to server (000)
opened: getent hosts example.com -> 172.66.147.243 / 104.20.23.154
        curl -m 5 http://example.com -> 200 ; https://1.1.1.1 -> 301
both  : http://piforge:7700/healthz -> 200 ; /v1/projects -> 401
admin : egress_mode=none source=/etc/piforge/tenant-egress.json configured=true applied=true
        admin=none / dev=open (override)
closed tenant prompt: settled，助手回复 DONE，
        /gw/llm/<token>/v1/chat/completions -> 200
```

公网目标用明文 HTTP + 一个字面 IP，是因为本机到部分公网站的 TLS 被中间干扰（`curl: (35) unexpected eof`）——那是网络的性质，不是策略的性质；钉死 `https://example.com` 会让探针测防火墙而不是测桥。

---

### 1.8 租户可控目录：读取的收拢规则（containment）

这一节是规范，不是历史。任何一个**新**的"控制面读一个路径"的入口，都要先回答这一节的问题。

**规则**

1. **谁写的这棵树？** 一条路径如果有一部分是租户能写或能影响的（skill 仓库、skill zip、workspace
   卷、agent 自己的目录），这条路径就是"租户可控的树"，读它必须走 `internal/tenantfs`，或者有
   等价的检查写在同一个函数里。
2. **不跟链接。** 读之前逐分量 `os.Lstat`；任何分量是符号链接 → 拒绝。打开之后再
   `os.SameFile(lstat, fstat)` 比一次（检查与打开之间被换成链接的窗口），再
   `filepath.EvalSymlinks` + `filepath.Rel` 确认实体在根内。`WalkDir` 不够：它不跟目录链接，但
   `os.ReadFile` 跟文件链接，这正是本轮那个洞。
3. **只读普通文件。** 目录、设备、fifo、socket 都不是"内容"；一个 fifo 会让 `os.ReadFile` 挂住。
4. **拒绝整次操作，不是跳过那一个文件。** 拒绝要是类型化错误（`*tenantfs.Refusal`，带 offending
   path），并且要在写任何一行数据库 / 任何一个文件之前发生：半个导入比一次拒绝更难解释。
5. **传输层先减一次。** `git clone` 用 `-c core.symlinks=false`（链接落盘为文本），zip 解压拒绝
   绝对路径、`..`、非普通文件条目；然后再用规则 2 做第二道。两道都要：第一道让链接不能变成活对象，
   第二道让"你带了链接"这件事被拒绝并点名。
6. **测量要覆盖租户能写的全部地方。** 磁盘预算不是"某个目录的预算"：workspace + 容器可写层
   （`du -sbx /` 减开机基线）。同理，任何"这个租户占了多少"的指标，先问"他还能写哪里"。

**这一轮审计过的每一个读取点**

| 入口 | 路径来源 | 结论 / 改动 |
|---|---|---|
| `POST /v1/skills/import`（git） | 租户仓库 | **就是那个洞**。`core.symlinks=false` + `refuseDeclaredSymlinks`（`ls-files -s` 的 120000）+ `findSkills` 全树 `tenantfs` 走查 |
| `POST /v1/skills/import/zip` | 租户压缩包 | 绝对路径 / `..` / 非普通文件（含链接条目）已拒绝；解压后同样走 `tenantfs` |
| `GET /v1/skills/{id}/bundle` | 控制面写的目录，但服务给租户 | 改走 `tenantfs`（恢复/手工拷贝可能带进链接） |
| 工作区预览 / 下载 / 树（`/files`、`/files?download`） | 租户 workspace | **已经是对的**：`fsx.Resolve` 做 `EvalSymlinks` + `Rel`，越界即 `ErrEscape` |
| 工作区搜索（`/search`，无 rg 时的回退） | 租户 workspace | **修了**：`walkSearch` 跳过 `ModeSymlink`（rg 默认不跟链接，回退路径以前会 `os.Open` 跟） |
| `.gitignore` 解析 | 租户 workspace | **修了**：`Lstat` + 只读普通文件（链接的 `.gitignore` 以前会被跟） |
| `git status` 未跟踪文件行数 | 租户 workspace | **修了**：`Lstat`，链接不打开（以前 `os.Stat` 跟进去，只泄漏行数，仍然是读） |
| skill 挂载 / 快照拷贝 / digest | skill bundle 目录 | **已经是对的**：`materialize.copyTree` / `copyTreeFS` / `walkDigest` 都拒绝链接 |
| agent 日志（`ReadLog`） | agent 自己的目录 | **修了**：`Lstat` + `SameFile`，非普通文件直接报错 |
| artifacts / 截图（`/artifacts?path=`） | 服务端生成的名字 | **已经是对的**：走 `fsx.Resolve`；会话 id 有分隔符/`..` 直接拒 |
| MCP 配置（`mcp.json`） | 租户配置，控制面路径 | 路径是控制面自己的；isolated 下 stdio server 直接不 stage（`internal/mcpstage`） |
| 项目 `repo_url` 导入 | 租户 URL，在**沙箱里** clone | 不在控制面读；`validateRepoURL` 只放 https / `git@` |
| 会话导出（`/export`） | 只导出事件/消息 | 不读文件 |
| workspace 快照（`internal/snapshot` 的 `listFiles`） | 租户 workspace | 只记大小、链接标记 `Symlink=true`，不读内容 |
| `file://` 导入源 | 租户 URL | **残余风险**：能指向控制面能读的本地仓库；本轮只保证带链接的仓库被拒，没有做部署级白名单 |

**代码位置**

- `backend/internal/tenantfs`：`Tree.Open/ReadFile/Walk/WalkFiles`，`Refusal{Path, Absolute, Kind}`。
- `backend/internal/httpapi/skill_import.go`：`cloneCheckout`、`refuseDeclaredSymlinks`、`findSkills`、
  `importRefusalMessage`。
- `backend/internal/httpapi/skill_bundle_handlers.go`：读回。
- `backend/internal/fsx/search.go`、`ignore.go`、`backend/internal/gitx/status.go`、
  `backend/internal/agentsupervisor/supervisor.go`：上面表里的四处"修了"。

**怎么证明它还在**

Go 测试（都在 `go test ./...` 里）：

```
tenantfs   TestReadFileRefusesALinkToAFileOutsideTheTree / …DanglingLink / …LinkInAParentDirectory
           TestReadFileRefusesADirectory / …OversizedFile / …TraversingOrAbsolutePath
           TestWalkRefusesALinkAnywhereInTheTree / TestWalkSkipsGitStorage
           TestRefusesTheReportsOwnTargets（Linux：/proc/1/environ、/data/master.key）
httpapi    TestImportRefusesASymlinkToTheEnvironment          （报告的攻击，端到端，无行、无 bundle 目录）
           TestImportRefusesASymlinkToTheMasterKey            （/data 变体，整次导入被拒，旁边那个正常 skill 也没进）
           TestFindSkillsRefusesALinkOnDisk                   （磁盘上真链接：类型化 Refusal 点名路径）
           TestCloneMaterialisesADeclaredSymlinkAsAFile       （clone 半：落盘是文本，不是活链接）
           TestImportRefusesARepositoryThatDeclaresASymlink   （索引声明也拒）
           TestZipImportRefusesAnAbsoluteOrTraversingEntry    （绝对路径 / .. / 链接条目，三例都无行）
           TestLegitimateRepositoryStillImportsAndMounts      （正常仓库 200，会话真的挂上）
           TestSkillBundleReadRefusesALink
```

**残余**（同 §1.4.10 末段）：`file://` 没有白名单；硬链接不在检测范围；"根文件系统减基线"包含沙箱服务自己写的东西。
clone 的工作树落盘在第四轮之后有上限（`-skill-clone-max-bytes`，默认 16 MiB，见 §1.9.5）。
### 1.9 出站 URL 策略：控制面替租户取一个 URL 的唯一规则

这一节和 §1.8 一样是**规范**，不是历史。任何一个**新**的"控制面去 fetch 一个租户给的 URL"的入口，
都要先回答这一节的问题；答不上来就不该合入。

#### 1.9.1 这一轮测到的事

第四轮对抗验收（`e2e/.artifacts/adversarial4/REPORT.md` 的攻击 18 与 21）证明了控制面有两个**开放的
反向代理**，而沙箱"没有出网"这句话对 HTTP 不成立：

```
# 攻击 18：MCP broker。POST /v1/mcp/servers 存 URL 时没有任何 scheme/host 检查，
# 会话开始后沙箱拿到 http://piforge:7700/gw/mcp/<token>，broker 把沙箱的
# 方法、body、响应原样转发到那一行里的 URL —— 从控制面自己的网络位置。
# 在沙箱里（没有 DNS、没有路由）：
GET http://piforge:7700/gw/mcp/<tok>/healthz   -> 200 {"status":"ok"}      # 控制面自己的 loopback
GET http://piforge:7700/gw/mcp/<tok>/anything  -> 200 {"you_are":"172.26.0.2",
                                                      "marker":"A4-ONLY-THE-CONTROL-PLANE-CAN-READ-THIS"}
# 172.26.0.2 = 控制面容器；marker 容器只挂在 pf-isolated_default 上，沙箱解析不了它。
# 控制面能上外网（wget https://example.com -> rc=0）→ 双向通道：数据可以出去。

# 攻击 21：provider base_url。任何用户都能建一个 base_url，再让控制面 POST 过去，
# 非 2xx 的响应还会回显 ≤300 字符的上游 body。
POST /v1/providers {"base_url":"http://a4marker:9021"} -> 201
POST /v1/providers/<id>/test                          -> 200 {"ok":true}
# marker 看到的：client=172.26.0.2  Authorization: Bearer <租户自己的 key>
```

结论不是"broker 的鉴权有问题"（token 有、地址绑定有、凭据不转发），而是**缺少一条关于"去哪儿"的规则**。
所以修复不在两个 handler 里各加一个判断，而是一条共享策略，在每个 fetch 点各生效一次、而且生效两次
（存的时候、拨号的时候）。

#### 1.9.2 规则（`internal/urlguard`）

1. **scheme 只能是 `http` / `https`。** `file:`、`gopher:`、`unix:`、`ftp:`、空 scheme 一律类型化拒绝，
   理由码 `scheme`。`http://user:pw@host/` 这种把凭据塞进 URL 的也拒绝（`credentials`）。
2. **名字先判。** `localhost` / `*.localhost`、`host.docker.internal`、`gateway.docker.internal`、
   `*.local`、`*.internal`、`host.containers.internal`、compose 服务名（`piforge`、`sandbox-runner`）、
   本容器自己的 hostname（`os.Hostname()`，每个部署都不同，所以运行时取）、`metadata.google.internal`
   等，**不问 DNS 直接拒**（`host-name` / `loopback`）。一个只能指向"这台机器"的名字，不是租户的 MCP 服务器。
3. **每一个解析结果都要是公网地址。** 逐地址分类，任何一个命中就拒绝：loopback（127/8、::1）、
   link-local（169.254/16、fe80::/10 —— 云元数据就在 169.254.169.254）、private（10/8、172.16/12、
   192.168/16）、unique-local（fc00::/7）、unspecified（0.0.0.0、::）、multicast、CGNAT（100.64/10）、
   documentation（192.0.2/24 等）、benchmark（198.18/15）、reserved（240/4、0/8）。IPv4-mapped IPv6
   先 unmap 再判，否则 `::ffff:127.0.0.1` 会绕过。
4. **请求时再查一次，并且把连接钉在验证过的 IP 上。** `urlguard.Transport` 的 `DialContext` 自己解析、
   自己验证、然后 `dial(验证过的 IP)`；TLS 的 SNI 仍然取自 URL 的 host。所以"存的时候解析到公网、
   一分钟后再解析到私网"（DNS rebinding）依然失败，而且连接去的是被检查过的那个地址，不是第二次解析的答案。
   transport 显式 `Proxy: nil`：环境变量里的 `HTTP_PROXY` 是第二个未经检查的拨号器。
5. **重定向逐跳重查。** `http.Client.CheckRedirect` 对每一跳重新走规则 1–3，最多 5 跳；一个允许的
   base_url 回 302 到 `169.254.169.254` 会变成拒绝，而不是被跟随。
6. **拒绝是类型化的 400，点名理由和 host，一次拒绝一行日志。** body 是平台统一的
   `{"error":{"code":"invalid_argument","message":"…","details":{"reason":"loopback","host":"127.0.0.1"}}}`；
   日志只有一行 `refused to fetch a tenant-supplied URL what=… host=… reason=…`，**不记完整 URL**
   （query 里可能是租户粘进去的 token）。
7. **存的时候宽松一点，只宽松"解析不了"。** `CheckSave` 允许一个**此刻解析不了**的名字被保存（DNS 临时
   坏掉时不能连 provider 都改不了），但 scheme / 凭据 / 本机名字 / 能解析到的私网地址仍然当场拒绝；
   真正到达网络的永远是请求时那次检查，所以这一点不放宽边界。
8. **只有显式 allow-list 能放宽。** 默认空；一个 operator 写的文件在启动时解析（未知字段、坏 CIDR、
   缺 ports 都会让启动失败），条目按 **host 或 CIDR + 端口** 精确匹配，只能把"可拨"扩到那一条，
   改不了别的。这是部署规则，不是租户设置。
9. **git 是第二个 HTTP 客户端。** 每一次 clone/fetch/push 都带 `-c http.followRedirects=false` 且环境里有 `GIT_HTTP_FOLLOW_REDIRECTS=0`：重定向是拒绝，不是抓取。理由是策略只看见第一个 URL（第五轮对抗验收用 `https://postman-echo.com/redirect-to?url=http://127.0.0.1:7700/healthz` 让控制面的 git 取了自己的回环）。逐跳重验是可选做法，这一轮选择拒绝：跟随一个租户能控制的 302，等于把已经验过的判断作废。规则同样适用于 `repo_url` 的 clone（host 放置）。

#### 1.9.3 配置

```
-url-allow-file / PIFORGE_URL_ALLOW_FILE      # 默认空：只有公网 http(s)
-skill-clone-max-bytes / PIFORGE_SKILL_CLONE_MAX_BYTES   # 默认 16 MiB，见 1.9.5
```

allow-list 文件（compose 把它只读挂到 `/etc/piforge/url-policy.json`，默认是 `deploy/url-policy.json`，空的）：

```json
{
  "comment": "the one internal MCP server this operator runs",
  "allow": [
    { "host": "mcp.internal.example.com", "ports": [443] },
    { "cidr": "10.20.0.0/16", "ports": [8443] }
  ]
}
```

启动日志会打一行 `outbound URL policy policy="public http(s) only" | "… + N allow entries"`，
以及它覆盖哪些路径；`allow_file` 永远是文件路径或空。

交付演练（`scripts/deliver-check.ps1`）就是这条规则的一个使用者：它的假 forge 是容器里的
loopback 地址，所以演练启动容器时**写下一个只含 `127.0.0.1:<forge 端口>` 的 allow 文件**并挂进去
（`-v <artifact>/url-policy.json:/etc/piforge/url-policy.json:ro` +
`-e PIFORGE_URL_ALLOW_FILE=/etc/piforge/url-policy.json`）。启动日志里因此是
`policy="public http(s) only + 1 allow entries"`。

**这不是绕过，而且默认拒绝是被自动化守着的**：同一个演练里还有 5 条检查跑在同一个容器上——
`http://127.0.0.1:7700/healthz`（**换了端口**，所以不在那一条 allow 里）→ 400 `loopback`、
`http://host.docker.internal:7700` → 400 `host-name`、`http://169.254.169.254/` → 400
`link-local`、被 allow 文件点名的那个 URL → 201（同一个端点，唯一的差别是文件里有没有它）、
以及一个 20 MiB 仓库的 `POST /v1/skills/import` → 400 且消息里同时有 16.0 MiB 和实测字节数。
谁把默认策略改松了，这四条拒绝会立刻变红。

**不要照抄演练的 allow 文件当作默认配置**：它存在的唯一理由是"演练自己的假 forge 在 loopback 上"。
真正的部署里 `deploy/url-policy.json` 是空的；要放开某个内网目标，是 operator 为那一个目标写一条
带端口的条目，而不是复制一份演练文件。

#### 1.9.4 这一轮审计过的每一个"控制面 fetch 租户 URL"的点

| 入口 | 谁给的 URL | 控制面拿它做什么 | 现在 |
|---|---|---|---|
| `POST /v1/mcp/servers` | 租户 | 存进 `mcp_servers.url` | 存时 `saveOutboundURL` |
| `GET/POST /gw/mcp/<token>/…` | 上面那一行 | `httputil.ReverseProxy` 全方法全 body 转发 | 每次调用 `refuseOutboundURL` + 策略 transport（拨号钉 IP）+ `ErrorHandler` 把拨号期拒绝也变成 400 |
| `POST /v1/providers`、`PATCH /v1/providers/{id}` | 租户 | 存 `base_url` | 存时 |
| `POST/PATCH /v1/admin/providers` | 管理员 | 同上 | 存时 |
| `POST /v1/providers/{id}/test` | 上面那一行 | `POST <base>/v1/…`，并把上游错误回显 300 字符 | 调用前 + 策略 client（重定向逐跳） |
| `/gw/llm/<token>/…`（真模型流量） | provider 行 | 把 agent 的请求转发到 `base_url` | 调用前 400 `url_refused` + 策略 transport |
| `PUT /v1/projects/{id}/forge` + PR 调用 | 租户 | `POST <api_base>/repos/…/pulls` | 存时 + `forge.Config.HTTP` 走策略 client |
| `POST /v1/skills/import`（http/https、`git@`） | 租户 | `git clone` 从控制面出网 | 调用前（`git@` 按 `https://<host>/` 校验同一套主机规则）+ `http.curloptResolve` 钉 IP |
| 项目 `repo_url` | 租户 | 容器部署**在沙箱里** clone；`-sandbox local` 时由控制面 clone | 容器：不在控制面位置；local：克隆前 `Local.refuseCloneTarget`（`urlguard` 判 http(s)/`git@`，本地路径拒绝另一个账号的沙箱目录） |
| 截图 / 浏览器导航 | 租户 | 容器部署：**沙箱里的** Chrome；`host` 放置：控制面自己的 Chrome | 容器：不在控制面位置；`host`：只校验 scheme，**仍是残余**（docs/DATA-FLOW-AUDIT.md G5） |
| `/preview` 反代（web upstream） | 部署 | Next.js | 部署配置，不是租户 URL，不在本策略范围 |
| `file://` skill 导入 | 租户 | git 读控制面自己的本地仓库 | 读前 `confineLocalImport`：路径必须落在 `-skill-import-roots` 里（默认空 = 全部拒绝，`tenantfs.Contains` 解析链接） |

#### 1.9.5 skill 克隆的落盘上界（同一份报告的攻击 10）

4 MB 的读上限是在 clone **之后**才生效的：报告里 32 MiB 的仓库在 `/tmp` 留下 38 829 857 B、
268 MiB 的留下 268 800 878 B，然后才 400。现在：

- `git clone --depth 1 --single-branch --no-tags`（一个提交、一条分支、不要 tag）；
- 一个看门狗每 25 ms 量一次目标目录（含 `.git` 的 pack），超过 `-skill-clone-max-bytes`（默认 16 MiB）
  就 `Kill` git；git 退出后再量一次（覆盖"25 ms 内就跑完"的情况）；
- 任何一种超限：`os.RemoveAll` + 类型化 400，消息里写上限和**实际量到的字节**；
- 最坏情况 = 上限 + 一个采样间隔内 git 写下的字节：实测——64 MiB 仓库停在 16 690 724 B（低于
  16 MiB 上限）；同一个 20 MiB 仓库（本地 `file://` 克隆，读盘快）两次分别停在 18.4 MiB 和
  16.8 MiB，即超出上限 2.4 MiB / 0.8 MiB（写盘越快，一个 25 ms 窗口里塞进去的越多）。网络克隆
  受链路速率限制，超出的量只会更小。

#### 1.9.6 残余（写清楚，不藏）

- **`file://` skill 导入源**不再是"接受任意路径"：它按 `-skill-import-roots` 白名单读
  （默认空 = 拒绝每一个 `file://`；`tenantfs.Contains` 解析符号链接，链接不能绕回根外）。
  自托管安装把仓库放在同一块盘上时，由运维写出允许的目录。这一条与整个数据流的边表
  一起记在 `docs/DATA-FLOW-AUDIT.md`（G1）。
- **`git@host:path`（ssh）** 只按同一套主机规则校验 host；SSH 本身不是 HTTP，`http.curloptResolve` 管不到它
  （`ssh` 的 `HostKeyAlias`/`ProxyCommand` 是另一件事）。公网主机放行，内网主机被拒。
- **allow-list 是 host/CIDR + 端口**，不是路径；一个被允许的 host 上的任何路径都可拨。
- 策略不改变"控制面能上外网"这个事实：它只是不让**租户**借控制面的位置去取。

#### 1.9.7 怎么证明它还在

Go 测试（都在 `go test ./...` 里）：

```
urlguard    TestRefusesEveryAddressTheReportNames        （report 的两个探针 + 每一个地址族，逐个断言 reason）
            TestOwnContainerNameIsRefused / TestRefusesSchemesAndShapes
            TestPublicAddressesAreAllowed                 （anthropic / openai / volc 的主机仍然可拨）
            TestDNSFailureIsARefusalAtCallTimeAndNotAtSaveTime
            TestAllowListIsTheOnlyWayToWiden / TestAllowEntriesAreValidated
            TestDialRefusesAndPins                        （拨号器自己拒绝 + 钉住验证过的 IP）
            TestRedirectToARefusedTargetIsRefused / TestClientDoesNotUseAnEnvironmentProxy
httpapi     TestTheMCPBrokerRefusesTheReportsTargets      （存时 400 × 7，调用时 400，上游 0 次命中）
            TestAnAllowedInternalMCPServerStillWorks      （allow-list 的那一个仍然 200 且真的到上游）
            TestTheProviderBaseURLIsRefusedTheSameWay     （存 / patch / /test 三条）
            TestTheProviderProbeRefusesARedirectToTheMetadataService
            TestTheForgeAPIBaseIsRefused
            TestTheDeploymentDefaultIsWhatRefuses         （默认策略不是 test harness 的宽松策略）
            TestSkillImportRefusesACloneOverTheCap / TestSkillImportStillWorksUnderTheDefaultCap
llmgw       TestGatewayRefusesAPrivateProviderBaseURL     （/gw/llm 对私网 base_url 回 400 url_refused）
```

隔离栈（7723，镜像从本轮 HEAD 重建）的实测输出见 `docs/STATUS.md` 本批那一节和
`e2e/.artifacts/adversarial5/REPORT.md`。

### 1.10 第二轮数据流审计：G5–G8（2026-11-01）

`docs/DATA-FLOW-AUDIT.md` 第二轮关掉了四个 GAP。这一节写运维要做的两件事（发布端口、放行内网远端），以及两个纯 API 修复。

#### 1.10.1 宿主放置的内置浏览器只开被发布的回环端口（G5）

宿主放置（`-sandbox-browser host`；`-sandbox local` 下 `auto` 解析成它）里 Chrome 是控制面进程的子进程：它渲染的页面是从控制面的网络位置读出来的，截图再回给租户。旧规则只看 scheme，所以
`POST /v1/sessions/{id}/browser/tabs {"url":"http://127.0.0.1:7700/v1/admin/…"}` 就能让控制面把自己的管理页拍给租户。

规则（`urlguard.CheckBrowserNavigation`，由 `handleOpenBrowserTab` 经 `Deps.BrowserNavigation` 调用）：

| 目标 | 判定 |
|---|---|
| 公网 http(s) | 放行（公网页面不借控制面的权限） |
| 回环（`127.0.0.0/8`、`::1`、`localhost`、`*.localhost`）+ 已发布端口 | 放行（dev server / 自己服务的 UI） |
| 回环 + 其它端口 | 拒绝（`loopback`，消息里列出已发布端口） |
| 控制面自己的监听端口 | 拒绝（`control-port`），**即使出现在已发布列表里也一样** |
| 169.254/16（云 metadata）、私网、ULA、CGNAT、保留段 | 拒绝，并点名地址段 |
| `host.docker.internal`、compose 服务名、本容器主机名、`*.internal`/`*.local` | 拒绝 |
| `file:`/`javascript:`/`data:` 等非 http(s) | 拒绝 |

```bash
# dev server 在 3000、同机 UI 在 3100
piforged -sandbox local -browser-localhost-ports 3000,3100 …
# 或 PIFORGE_BROWSER_LOCALHOST_PORTS=3000,3100
```

默认空 = 除"本部署自己服务的 UI"之外，宿主放置下不允许任何回环导航。`-web-upstream` 是回环地址时，它的端口自动进入已发布集合（镜像里的 Next 在 `127.0.0.1:3100`，控制面代理它），因为那本来就是平台发布的端口；控制面自己的监听端口仍然优先拒绝。容器放置（`browser=remote`）不跑这条规则：那里 Chrome 在沙箱里，`127.0.0.1` 就是沙箱自己的回环。

**代价（明说）**：agent 自己起在未声明端口上的 dev server，在宿主放置下看不了；拒绝消息会列出已发布端口，运维要么补端口，要么用容器放置。反过来放行全部回环，等于把控制面的截图交给租户——比一个被拒的标签页糟得多。

e2e 跑宿主放置，所以 `e2e/playwright.config.ts` 显式声明 `-browser-localhost-ports 3100,3101`（3100 是它起的 UI，3101 是 `72-observe-input` 绑的固定端口；控制面自己的 8080 永远拒绝）。drill 在容器里开浏览器（`remote` 放置），不受影响。

#### 1.10.2 push/pull/fetch 之前先读远端并过同一套策略（G6）

`origin` 是仓库配置，agent 在沙箱里可以随便改（`git remote set-url`、加 `pushurl`、`url.<base>.insteadOf`）。宿主放置下是控制面在跑 `git push`，所以旧行为等于"按租户写的 URL 从控制面拨号"。

现在（只接在宿主放置，`cmd/piforged`）在每次 push/pull/fetch（包括 PR 的那次 push）之前：

1. `gitx.RemoteTargets` 读出 `remote.*.url`、`remote.*.pushurl`、`url.<base>.insteadOf` 里的目标；
2. `sandbox.RefuseRemoteTarget` 按 `refuseCloneTarget` 的同一套规则判：http(s)/`git@`/ssh/git:// 过 `urlguard`（公网地址或运维 allow-list）；`ext::` 和未知 transport 直接拒；本地路径在别的租户沙箱里拒；
3. 拒绝是类型化 400（`details.reason` / host），且 0 次拨号。

合法内网远端用同一个 `-url-allow-file`（`deploy/url-policy.json` 的形状）：

```json
{"allow": [{"host": "git.internal.example", "ports": [9418]}]}
```

容器放置不接这个检查：git 在沙箱里跑，`origin` 是租户自己拨得到的地址，边界是沙箱的网络出口策略（§1.7）。

#### 1.10.3 git 语法拒绝统一 400（G7）

`gitx` 的每个语法拒绝（revision、分支名、cursor、diff range、路径）现在都是类型化错误（`gitx.ErrInvalidRequest` / `ErrPathEscape`），`httpapi.classify` 统一映射成 400。`POST /git/branch {"name":"-D"}` 从 500 变成 400；branch / checkout / commit / diff（commit、branch base、range）/ log / worktree 一致。

#### 1.10.4 审计 CSV 不再执行租户文本（G8）

`GET /v1/admin/audit?format=csv` 的每个字符串列都过 `csvFormulaSafe`：以 `=`、`+`、`-`、`@`、TAB、CR 开头的单元格前面加一个 `'`（OWASP）。数字列（`duration_ms`、`exit_code`、`result_bytes`）不加：负的 exit code 是数字，不是公式。测试用 GAP 里的原 payload `=cmd|' /C calc'!A0`，并逐个触发字符断言。

#### 1.10.5 怎么证明它还在

```
urlguard  TestCheckBrowserNavigation                （规则：已发布端口 / 控制端口 / metadata / 私网 / docker 名 / allow-list）
httpapi   TestBrowserNavigationIsRestrictedInHostPlacement （路由层；容器放置仍放行回环）
          TestGitRemoteGuardRefusesARewrittenOrigin （origin 被改成 127.0.0.1:7700 或别的租户路径 → 400 且 0 次 push/pull/fetch）
          TestGitRemoteGuardHonoursTheOperatorAllowList
          TestGitValidationRefusalsAre400           （每个 git 路由的真实拒绝都是 400）
          TestAuditCSVNeutralisesFormulaCells / TestCSVFormulaSafe
gitx      TestRemoteTargetsReadsEveryPushRewrite    （url / pushurl / insteadOf 都读）
```


#### 1.11 第三轮数据流审计：G9–G13（2026-11-01）

第五轮对抗验收（`e2e/.artifacts/adversarial5/REPORT-adversarial5-git-and-paths.md`）打的是 §1.10 的修复，
结论是那些守卫描述的都是 **argv**：git 还是一个**命令解释器**（`.git/config` 租户可写，`core.fsmonitor`
经一次普通的 `GET /git/status` 执行了命令）和一个**第二个 HTTP 客户端**（`git clone` 跟随公网 URL 的 302
去取控制面自己的回环）。五条 GAP 的判定与测试见 `docs/DATA-FLOW-AUDIT.md` §11（G9–G13）。运维要看的只有这些：

| 现在会被拒绝的仓库/配置 | 拒绝长什么样 | 怎么办 |
|---|---|---|
| `.git/config` 里 `core.sshCommand`、`core.fsmonitor`、`core.hooksPath`、`credential.helper`、`remote.*.proxy`、`remote.*.uploadpack/receivepack`、`filter.*`、`diff.*.textconv`、`merge.*.driver`、`include.path` | push/pull/fetch 之前 400，消息点名那个 key | 删掉那个 key。这些 key 让 git 执行一个程序，而执行什么不该由仓库决定 |
| `url.<base>.pushInsteadOf` / `remote.*.pushurl` / `[user@]host:path` 指向回环、私网、link-local | 400，`urlguard: host "127.0.0.1" is refused: loopback` | 合法内网远端写进 `-url-allow-file`（host/CIDR + 端口）；ssh 按 **22** 判，不是 443 |
| 一个会 302 的 clone/fetch URL | 400，git 原文 `The requested URL returned error: 302` | 直接用最终地址（`http.followRedirects=false` 是故意的：跟随就要逐跳重验，而拒绝更便宜也更安全） |
| 导入根里的 **junction**、`.git.`/`.git `/`.git..` 这样的 bundle 路径 | 400，点名路径 | 这不是"配置错了"，是有人试过。junction 在 Windows 上不是符号链接：`EvalSymlinks` 看不见它，`Lstat` 把它当普通文件，所以 `tenantfs` 改用 `FILE_ATTRIBUTE_REPARSE_POINT` 与 `GetFinalPathNameByHandle` |

**机制**（`backend/internal/gitx/harden.go`）：每一次控制面发起的 git 都带
`-c core.fsmonitor=false -c core.hooksPath=<空目录，0700，随机名> -c core.sshCommand=<启动时解析出的绝对
ssh> -c core.pager= -c core.askPass= -c core.gitProxy= -c credential.helper= -c commit.gpgSign=false
-c tag.gpgSign=false -c protocol.ext.allow=never -c protocol.file.allow=user -c http.followRedirects=false`，
并且环境里每个 `GIT_*`（包括 `GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_n`、`GIT_SSH_COMMAND`）在 git 启动前被删掉。
**命令行 scope 高于任何配置文件**，所以仓库里的同名 key 不再生效。沙箱里跑的 git 用另一份：`core.sshCommand=ssh`
（容器自己的 ssh 与 PATH），因为把宿主的绝对路径写进容器会破坏合法的 ssh clone；`core.hooksPath` 在那里是一个
不存在的路径，等于没有 hook。

**还成立的事**：ssh 远端照样能 push/pull——用的是**部署自己的** ssh 与密钥，主机与端口仍过策略；`repo_url`
的 `[user@]host:path` 现在按 ssh（22 端口）判；`file://` 导入仍要求落在 `-skill-import-roots` 里，且用的是
**解析后**的路径（把 junction 在检查与 clone 之间翻掉不再有用：演练里 `hits=14/87` → `0/116`）。

**尚未关闭**（写在 `docs/DATA-FLOW-AUDIT.md` §14）：POSIX 的 bind mount 不是 reparse point；解析后的路径
自身分量在"解析"与"打开"之间仍可被换掉（能写这台机器磁盘的人）；容器放置里 git 在沙箱内跑，边界是沙箱出口策略。

**验收工具本身也修了两处**（重要，因为上一轮就是被它们骗了）：`scripts/up-isolated.ps1` 不再"只建缺失的镜像"，
也不再拿 mtime 当判据——每个镜像都带 `piforge.source-digest` 标签，下一轮用**工作树内容的摘要**与它比，
相等才复用（见 §1.12）；`scripts/probe-data-flow.ps1` 自己建夹具，
在干净栈上跑满 **18/18** 条边（旧版会静默跳过 4 条、打印 15），打印计数，测不到就 exit 2。

#### 1.12 第六轮的三个收尾（2026-11-01）

第六轮（`e2e/.artifacts/adversarial6/EVIDENCE.md`）关掉了 G9–G13，同时留下三条：两条在验收工具里，
一条在文件面板的语义里。三条都在这一节，数字见 `docs/STATUS.md`。

**(1) 镜像新鲜度：按内容判，不按时间判。** 旧规则是"镜像创建时间 vs 最新的源文件 mtime"。它两头都错：
`touch` 一个没改的文件会白重建；把一个**改过内容**的源文件的 mtime 设到过去，脚本会打印
`piforge:isolated is current (…)` 而工作树其实不同——也就是说，任何"验证是绿的"结论都可能是在量旧镜像。

现在的机制：构建时给每个镜像打一个标签 `--label piforge.source-digest=<sha256>`，摘要取自工作树里
`backend/`、`web/`、`deploy/`、`Dockerfile`、`docker-compose.runner.yml` 下**每一个**已跟踪（或未跟踪但未被
忽略）文件的**内容**，再把 Dockerfile 名与 build args 折进去。每次运行重新算一遍并只与标签比：相等就复用，
镜像缺失或没有标签（旧镜像、手工构建）就重建；沙箱镜像的摘要里含它 `FROM` 的那个控制面镜像 **id**，所以
控制面一重建，沙箱按同一条规则跟着重建。mtime 只在"算不出摘要"（没有 git）时作兜底，日志会明确说这是兜底。
`-Rebuild` 照旧强制全部重建，新增的 `-BuildOnly` 只构建、不启动（`scripts/verify-image-freshness.ps1` 用它跑
四个用例：改内容且回填旧 mtime → 重建；恢复时间戳 → 不重建；只 touch → 不重建；`-Rebuild` → 重建）。

**(2) 端口是这台栈的身份的一部分。** 旧脚本对 `-Port 7724` 什么都不说：同一个 compose 项目原地重建，
7723 悄悄不再应答。现在启动前先读这个项目**实际发布**的端口（`docker ps` 按
`com.docker.compose.project=<p>` 过滤，再 `docker inspect` 取 `7700/tcp` 的 `HostPort`）：项目已经在别的端口上
就**拒绝**，并点名它现在在哪个端口；确实要搬，`-Force`（并明确说旧端口会停止应答）。`-Down` 在检查之前执行，
所以**任何端口都能拆**——拆栈不该取决于上一次敲的是哪个端口。生效端口在构建前打印一次，最终摘要里再打印一次。

**(3) 文件面板分得清"工作区里的文件"和"顺着符号链接读到的"。** 容器放置下文件面板读的是沙箱自己的文件
API，而那个 API 跟随符号链接：`GET /v1/sessions/{id}/files?path=<指向 /etc/passwd 的符号链接>` → **200**，
内容是容器的 `/etc/passwd`。`../` 与绝对路径仍被拒（400），所以这没有越出租户自己的容器——问题在**陈述**：
面板把它摆在"工作区文件"的位置上，审查者会这么以为，审计者也会（第六轮，FIX 3）。

`contracts.FileContent` 因此多了两个字段：`outside_workspace`（经链接离开了工作区根）与 `resolved_path`
（容器里真正解析到的路径）。判断由沙箱 shell 回答，不靠猜：同一次 exec 里对根和目标各跑一次 `readlink -f`，
目标不在根下就置位，内容照样返回。宿主放置更严：`fsx.Resolve` 对越界链接直接 400（在那里"工作区"就是
这台机器，越界就是读到宿主的文件）。解析不出来时不加标记、也不让读取失败——这不是一个新的失败模式。
面板在头部显示徽章「经符号链接指向工作区外」，正文上方写出实际路径，`file-preview.test.tsx` 覆盖两个方向
（带标记 → 徽章 + 路径；不带 → 什么都没有）。下载端点仍是原始字节流，不带头部标记：预览是声明来源的地方。

## 2. Kubernetes：四种做法，各自什么时候是对的

### 2.1 方案（i）：一个 Pod 一个沙箱，控制面用 client-go 管

这是最直接、依赖最少的做法：控制面本身就是控制器。

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: pf-81042b4b785a90cf
  namespace: piforge-sbx
  labels:
    piforge.io/sandbox: pf-81042b4b785a90cf
    piforge.io/user: usr_01...
    piforge.io/project: prj_01...
spec:
  runtimeClassName: gvisor                 # 隔离交给 RuntimeClass，见 2.4
  automountServiceAccountToken: false      # 沙箱里不需要集群身份
  securityContext:
    seccompProfile: { type: RuntimeDefault }
  containers:
    - name: sandbox
      image: <aio-sandbox:1.11.0>
      env:
        # 每沙箱一个 key，由创建 Pod 的那一方生成并写进这个 Secret（不是部署级共用）。
        # 现在是 runner 在 create 时生成、经内部 API 回报一次；K8s 下同样必须由"创建者"
        # 注入，而不是从一份所有沙箱共享的 Secret 里读。
        - name: SANDBOX_API_KEY
          valueFrom: { secretKeyRef: { name: pf-81042b4b, key: api-key } }
        - { name: BROWSER_NO_SANDBOX, value: "--no-sandbox" }   # 见 2.5(f)
      ports: [{ containerPort: 8080 }]
      resources:
        requests: { cpu: 500m, memory: 1Gi }
        limits:   { cpu: "2",  memory: 4Gi }
      volumeMounts: [{ name: workspace, mountPath: /workspace }]
  volumes:
    - name: workspace
      persistentVolumeClaim: { claimName: pf-81042b4b785a90cf-ws }
---
apiVersion: v1
kind: Service            # 只给控制面用，不要 Ingress
metadata: { name: pf-81042b4b785a90cf, namespace: piforge-sbx }
spec:
  selector: { piforge.io/sandbox: pf-81042b4b785a90cf }
  ports: [{ port: 8080, targetPort: 8080 }]
```

配套的、不能省的三个对象：

- `ResourceQuota` + `LimitRange`（每个租户命名空间，或一个沙箱命名空间加默认 limit）：
  没有配额，一个 fork bomb 就能把节点上的邻居一起带走；
- `NetworkPolicy`：默认拒绝，只允许 `piforge` 命名空间进 8080，出站按需（模型网关、git
  remote、包仓库），并显式挡掉 `169.254.169.254`；
- `PodDisruptionBudget` 对沙箱意义不大（它本来就允许随时被删，工作区在 PVC 里），
  但对控制面有意义。

**什么时候是对的**：你已经有多租户集群、不想引入 CRD 和第三方控制器、能接受自己写
"孤儿 Pod 清理 / PVC 清理 / 驱逐后状态对账"。**代价**：你就是那个控制器 —— Pod 被节点
驱逐、被 OOM kill、被 `kubectl delete pod` 之后，`Get` 要能分辨"被暂停"和"被删了"。

client-go 的具体调用点很窄：`Pods(ns).Create/Get/Delete/List(LabelSelector)`，
`PersistentVolumeClaims(ns).Create/Get/Delete`，`Services(ns).Create/Delete`，
`Watch` Pod phase 映射到 `SandboxState`（Pending→STARTING，Running+Ready→READY，
Succeeded/Failed→PAUSED/ERROR）。

### 2.2 方案（ii）：Job —— 沙箱是一个"跑完就结束"的任务

**什么时候是对的**：沙箱确实是任务 —— e2e 里"起一个容器跑一遍然后扔掉"、一次仓库索引、
一次批量迁移。

**为什么它不是主路径**：Job 的成功条件是"Pod 退出且退出码 0"，而沙箱的成功状态是"还活着"。
把长期沙箱写成 Job 会得到：`restartPolicy: Never` 下的失败重试语义、`backoffLimit`、
`kubectl logs` 面向批处理的假设，以及"用户打开会话时 Job 已经 Completed"这种没法解释的
状态。文件面板、终端、浏览器都需要一个**持续存在**的进程，Job 抽象正好相反。

可以用它做**补充**：比如"给这个项目跑一次 `git gc`/索引"，用完即删，不占 PVC。

### 2.3 方案（iii）：`kubernetes-sigs/agent-sandbox`（已核实存在）

**它是什么**：Kubernetes SIG Apps 下的项目，提供 `Sandbox` CRD + 控制器，定位是"给
单实例、有状态、需要稳定身份的工作负载一个声明式 API"，隔离本身**委托**给 RuntimeClass
（gVisor / Kata），它不自己实现边界
（[项目概览](https://agent-sandbox.sigs.k8s.io/docs/getting_started/overview/)）。

核心与扩展（扩展是一个单独的 CRD 包）：

| 资源 | 作用 |
|---|---|
| `Sandbox` | `spec.podTemplate` + `spec.volumeClaimTemplates` + `spec.shutdownTime` + `spec.replicas`(0/1)；控制器建 Pod + headless Service + PVC，`status.serviceFQDN` 给出稳定 DNS |
| `SandboxTemplate` | 可复用的 pod 模板（`runtimeClassName`、资源、存储、网络策略） |
| `SandboxWarmPool` | 预热一批已启动的 Sandbox |
| `SandboxClaim` | 从池子里"领"一个（adoption），把创建时延压到毫秒级 |
| `sandbox-router`（可选） | 一个反向代理，把请求按沙箱 id 转发到 Pod IP，省掉每个沙箱一个 Service |

`spec.replicas: 0` 会**删掉 Pod、保留 Service 和 PVC**，置回 1 再重建 —— 这就是它提供的
"休眠"。`spec.shutdownTime` 到期删 Pod/Service，PVC 保留供恢复（同上，[架构与字段说明](https://deepwiki.com/kubernetes-sigs/agent-sandbox)）。

**什么时候是对的**：你愿意引入 CRD + 一个集群级控制器，并且需要 warm pool / claim 这套
抽象。它把"Pod + Service + PVC + 到期清理"的样板从你的代码里拿走了。

**要注意的**：

- **冷启动仍然很慢**。项目自己的性能文档给的数字是冷启 **~42–50s p50**，warm pool
  adoption **p50 92ms / p90 182ms / p99 376ms**（转引自 `docs/research/agent-sandbox-isolation-briefing.md`
  §3.2，其原始来源是项目的 `docs/performance-tuning.md`）。**warm pool 不是优化，是产品**：
  没有它，"新建会话"就是 40 秒。
- **陷阱**：`SandboxClaim` 一旦带 `spec.env` 或 `spec.volumeClaimTemplates`，就会
  **绕过 warm pool 直接冷启动**（同一份 briefing §3.2）。我们这个产品每个沙箱都要一个
  自己的 `SANDBOX_API_KEY` —— 如果照搬"每个 claim 带 spec.env"，池子等于白建，必须把
  "分配 key" 做进 claim/adoption 那一步（现在由 runner 在创建容器时生成）。
- 版本：文档示例写 `agents.x-k8s.io/v1beta1`，源码分析文档写 `v1alpha1`，
  **以你要装的那个 release 为准（未核实）**。它是 2025 年 KubeCon 上启动的新项目，
  不是 GA 级依赖，引入前先评估"控制器停更了怎么办"。

### 2.4 方案（iv）：microVM 隔离层（gVisor / Kata / Firecracker）

先说结论：**这是隔离等级的选择，不是部署方式的选择**。三种都能挂在上面的 Pod 方案下面，
切换成本是 pod spec 里的一个 `runtimeClassName`。

| 方案 | 边界 | 什么时候值得 | 代价 |
|---|---|---|---|
| **gVisor**（`runsc`，RuntimeClass） | 用户态内核（Sentry）拦截系统调用，宿主内核暴露面显著缩小 | 默认推荐。租户跑的是 agent 生成的代码，不需要 `sudo`、不需要 docker-in-sandbox | 系统调用开销（第三方口径 I/O 密集 10–30%，[对比文章](https://www.softwareseni.com/ai-agent-sandboxing-explained-why-docker-is-not-enough-and-what-actually-works/)）；`docker build`/testcontainers/FUSE 基本不可用；GPU 受限 |
| **Kata Containers** | 每个 Pod 一个真内核（轻量 VM） | 租户需要真内核行为：`sudo`、dockerd、FUSE、VPN 客户端 | 每 Pod 100–300MB 额外内存、启动更慢（同上的第三方数字，未核实到官方口径） |
| **Firecracker**（E2B/Daytona/Fly/Vercel/Cloudflare 的做法） | 独立内核 + 最小 VMM | 你要做平台，不只是在平台上层做产品 | 自建 Firecracker = 自己做调度、镜像分发、网络、快照 —— 这是另一个产品，不是一个 provider |

在我们这个场景里，沙箱里跑的是：agent 生成的任意命令、一个 Chromium、一个 shell 会话。
共享宿主内核的 runc 边界提供的是**资源分区**，不是**对抗性边界**。多租户生产的第一版，
把 `runtimeClassName` 从 `runc` 换成 `gvisor` 是性价比最高的一步；等有租户真的需要
`docker build`，再给那一类沙箱单独一个 Kata 的 RuntimeClass。

### 2.5 五个横向问题（每个都决定设计）

**(a) 工作区怎么活过重新调度**

| 做法 | 存活范围 | 问题 |
|---|---|---|
| `emptyDir` | Pod 生命周期 | Pod 一删就没。只适合"一次会话、结果不重要" |
| **PVC（RWO）** | Pod 删除、节点漂移 | 跨节点要 detach/attach（时长取决于 CSI，具体秒级**未核实**）；RWO 不能被两个 Pod 同时挂 —— 这正好和"一个 (user, project) 一个沙箱"一致；但**每节点可挂载的卷数有上限**，它会变成集群密度的上限 |
| CSI VolumeSnapshot | 备份/回滚/克隆 | 不是运行态。别拿它当 pause（见 §5） |

推荐：**每个 (user, project) 一个 PVC，挂在 `/workspace`**。这样 `Pause` = 删 Pod、
工作区原地不动；`Destroy` = 删 Pod + Service，PVC 默认保留（对齐现在 docker provider
"不删数据卷"的决定）。

**(b) 8080 怎么被控制面访问到**

| 做法 | 何时用 | 代价 |
|---|---|---|
| 每沙箱一个 ClusterIP Service | 默认 | Service/EndpointSlice 数量随沙箱数增长（etcd 与 kube-proxy 都有成本）；用 NetworkPolicy 限定只有控制面能进 |
| 共享 router（`sandbox-router` 那类） | 沙箱数上千 | 少建 Service，但 router 变成数据面单点，且它自己必须做好 SSRF 防护（只允许转发到 Pod IP 段） |
| `pods/portforward`（client-go） | **只用于调试** | 每条连接都过 API server；Pod 重启即断（[port-forward 走 apiserver→kubelet](https://tasrieit.com/blog/kubectl-port-forward-2026)）；截图流 30KB/帧，不该走这里 |
| Ingress | **不要** | 用户浏览器不需要直连沙箱（见 §0 第 7 条）。给每个租户开一个 Ingress 等于给每个租户开一个公网入口，还共用一个 API key |

代码后果：`Endpoint` 必须从"读 `docker port` 拼 `http://127.0.0.1:<port>`"改成"返回一个
控制面可达的 base URL"。K8s 里是 `http://pf-<hash8>.piforge-sbx.svc:8080`；compose 里是
`http://piforge-<hash8>:8080`。**两者形状一样，都是"名字"，不是"宿主端口"** —— 这是这次
改造里唯一一个能让两种部署共用一套代码的地方。

**(c) 镜像拉取成本**

AIO 镜像是 GB 级（一篇实测记录里 `agent-infra/sandbox` 在本地是 5.61GB，
[来源](https://jianghushinian.cn/2025/11/09/aio-sandbox/)；我们的 1.11.0 镜像确切体积
**未核实**）。含义：

- 镜像按**节点**缓存，不是按沙箱。同一个节点上第 2 个沙箱不重新拉；
- 但调度到新节点 = 重新拉一次 GB 级。节点自动扩缩容时，这会和"40 秒冷启动"叠加；
- 对策：`imagePullPolicy: IfNotPresent`、节点预热（DaemonSet 或预装进节点镜像）、
  内网 registry mirror、以及 warm pool（预热的是**已启动的 Pod**，不只是层）。

**(d) 启动时延**

冷路径 ~42–50s（见 2.3）。这不是一个"优化项"，它决定产品行为：

- `SandboxStarting` 状态已经存在（`contracts.go:65`），UI 必须真的显示它，而不是转圈；
- 第一次打开会话就在冷路径上，所以"新建项目 → 立刻发一条消息"要能排队而不是超时；
- warm pool 把这条路径压到 ~100ms，但它只在"模板固定、不带 per-session env"时成立。

**(e) 什么替代 `docker exec`**

两种，别混：

1. **`pods/exec`**（client-go `remotecommand`，SPDY/WebSocket，经 API server 到 kubelet，
   [调用链](https://phoenixnap.com/kb/kubectl-exec)）：需要 `pods/exec` RBAC；每次一个
   往返；没有 PTY 复用、没有会话概念；适合低频的"跑一条 git 命令"。
2. **沙箱内自己的 HTTP API**（AIO 的 `/v1/shell/exec`）：**这才是我现在真正在用的那条**。
   面板的 git、文件读写、行式终端全部走它（`workspaceremote/exec.go`、`term/remote.go`）。

结论：K8s 迁移**不需要**先把 `Exec` 搬到 `pods/exec`。`ensureCheckout` 已经从
`docker exec -u gem` 改走 AIO 的 `/v1/shell/exec`，provider 只剩"生命周期 + 端点"。

**(f) Pod 安全策略与 seccomp：两个会当场炸的细节**

- **PSA `restricted` 与 AIO 镜像冲突**：镜像的 entrypoint 用 `useradd` 创建 `gem` 账号
  （`docs/SANDBOX.md` 的 checkout 一节记录了这个行为），这需要 root 和可写的
  `/etc/passwd`。`restricted` 要求 `runAsNonRoot: true`，于是 `gem` 不存在，镜像里的
  shell 也起不来，`ensureCheckout` 的 `/v1/shell/exec` 不可能成功，`Ensure` 失败。**要么用 `baseline`、
  自己收紧其余项，要么把 `gem` 烘进镜像**（更干净，但那是镜像的事，不是部署的事）。
- **seccomp**：`SeccompDefault` 在 1.27 GA，但**是否真的套 `RuntimeDefault` 取决于
  kubelet 的 `--seccomp-default`** 和托管集群的默认值（[seccomp 教程](https://kubernetes.io/docs/tutorials/security/seccomp/)）。
  一旦套上，就是 Docker 默认 profile 的同款过滤 → Chromium 的 zygote 起不来
  （`Failed to move to new namespace ... Operation not permitted`）。
  `BROWSER_NO_SANDBOX=--no-sandbox` 必须继续传（`docker.go:266` 已经在做，条件是
  `APIPort > 0`）。这比放开 Pod 的 seccomp 更窄，理由和那里一样。

---

## 3. 能不能一个容器放多个项目、用不同 Linux 用户区分？

直接回答：**能跑，但不能当租户边界。** 它防的是手滑，不是恶意。

先把"一个容器"实际意味着什么说清楚。同一个容器里的两个 Linux 用户共享：

- **一个内核**（宿主的内核）—— 没有内核边界；
- **一个 PID namespace**（除非你另外分）—— 互相看得见 `/proc` 里的进程，能 `kill` 同
  uid 的进程；
- **一个网络 namespace** —— 同一个 loopback，两个项目抢同一个端口；
- **一套 cgroup 配额** —— 一个项目吃满内存，另一个跟着 OOM；
- **一套 capability 与 seccomp** —— 不是按用户分的。

用户能提供的只有**文件属主与权限位**。而沙箱里跑的是 agent 生成的任意代码：它可以
`chmod` 自己的文件、写 `/tmp`、占满磁盘、起一万个进程、监听端口、读任何 `world-readable`
的东西（包括另一个项目里没设权限的 `.env`）。任何 **setuid 二进制**、任何**内核漏洞**、
任何**资源耗尽**，都能跨过这条"用户边界"。

更要紧的是：**镜像的 entrypoint 本身以 root 起**（我们的 AIO 镜像就是 —— `gem.sh` 要
`useradd`）。也就是说容器里存在一个 root。agent 只要拿到 root（配错的 sudo、setuid 位、
内核面），所有用户边界**同时消失**，因为 root 可以 `su` 成任何用户。

所以这个模型的定位是：

| 想防的 | 这套做法管用吗 |
|---|---|
| 两个项目的文件互相覆盖、git 串到别人的 worktree | **管用** |
| 一个项目的 `npm install` 把磁盘写满，另一个项目还能干活 | 不管用（同一套 cgroup / 同一个卷） |
| 一个项目读到另一个项目的密钥 | 不管用（同 uid 组之外还有 world-readable、还有 `/proc`） |
| 故意逃逸到宿主 | 完全不管用（共享内核，且容器里有 root） |
| 作为"多租户"对客户承诺隔离 | **不要承诺** |

它仍然有价值的场景，说实话就三类：

1. **单租户多项目**：一个人/一个团队，想省掉 N 个 Chromium、N 份镜像层、N 个 Pod；
2. **内部工具**：用户之间本来就互相信任，只是想避免误操作；
3. **CI/评测**：按 uid 分目录，跑完即删，不涉及对抗。

如果一定要做，必须**同时**加上这些，缺一条这个模型就退化成"没有边界"：

- **rootless 容器**（容器里没有 uid 0），或者至少 userns-remap 把容器 uid 映射到宿主
  非特权区间；
- `no-new-privileges: true`，堵掉 setuid 提权；
- **只读 rootfs** + 每个项目一个可写卷（工作区、`/tmp`、`/run` 各自独立）；
- `capabilities: drop: [ALL]`，需要什么单独加；
- 每项目独立的 cgroup 配额 + 磁盘配额（XFS project quota 或独立卷大小上限）；
- 每项目独立的网络 namespace（至少端口规划隔离），或者干脆每个项目一个 Pod；
- 明确写进文档：**这不是安全边界**。

具体到本仓库：`-sandbox local` 就是"一个宿主上的多个目录"，是这个模型的极端版本，它被
明确定位为单机/开发用途（`docs/SANDBOX.md` 的表）。现在的 `(user, project) → 一个容器`
不是随手选的，而是这个产品唯一一条真边界（同一份文档最后一节）。

---

## 4. 接入 E2B 这类外部沙箱服务

这是第四条路：不自己运维容器/集群，把"执行"外包给一个微虚拟机服务。E2B 是最典型的
一家：Firecracker 微虚拟机、按沙箱计费、SDK 覆盖文件系统 + 进程执行 + 端口
（[SDK 概述](https://e2b.mintlify.app/agents/vercel-ai-sdk)：`Sandbox.create({template})`、
`files.write/read`、`commands.run/spawn`、`getHost(port)`、`pause()`、`kill()`、
`setNetworkPolicy({allowedHosts, deniedCIDRs})`、默认 30 分钟沙箱超时、命令默认 60 秒超时、
工作目录 `/home/user`）。

### 4.1 映射到现有 `SandboxProvider`

| 接口方法 | E2B 上的对应物 | 诚实评价 |
|---|---|---|
| `Ensure(user, project, repo, branch)` | `Sandbox.create({ template, timeoutMs, metadata })` + 一次 `commands.run("git clone …")` | 模板必须在 E2B 侧**预构建**；"建沙箱"和"准备 checkout"变成两次远端调用，中间失败要能重试 |
| `Get(id)` | 连接/查询沙箱信息 | 状态机由对方定义，`PAUSED`/`MISSING` 要重新映射 |
| `Pause(id)` | `pause()`（文件系统 + 内存；也可只保文件系统） | 语义比 `docker stop` **强**：恢复后进程还在。但"暂停是否计费、能存多久"是商业条款，不是技术条款 |
| `Resume(id)` | 重连同一个沙箱 id（自动 resume） | 有一个真实失败模式：暂停可能被拒（对方节点正在做快照），持续失败会降级成"只保文件系统"，内存丢失 |
| `Destroy(id)` | `kill()` | 干净 |
| `Exec(id, argv, dir)` | `commands.run(cmd)` / `commands.spawn`（流式） | **每次一条 HTTP**；默认 60s 超时；没有 PTY。行式终端（`term/remote.go` 每 175ms 轮询 `view`）的时延会被放大成肉眼可见的卡顿 |
| `Endpoint(id)` | `getHost(port)` → 一个公网域名 | 不是 127.0.0.1，也不是集群 DNS；**必须带鉴权**（见 4.3） |
| `RootDir` | 不存在；工作目录 `/home/user` | 这个字段的语义要改，见 4.2 |

### 4.2 工作区：最贵的一块

现在的模型是"工作区 = 宿主目录，控制面直接读写，容器 bind mount 同一份目录"。E2B 里
**没有宿主目录**：控制面碰不到沙箱的文件系统，只能通过 API 读/写/执行。

于是 `provision` 的两处宿主操作立刻失效：

- `git worktree add`（`provision.go:200`）→ 必须在沙箱内执行，或者放弃 worktree 模型；
- `materialize.Mount`（写 skills / MCP 配置，`provision.go:243`）→ 必须变成"通过文件 API
  写入沙箱内的路径"。

三条可行路线：

1. **每次从远端 clone**：`Ensure` 时 `git clone` + `checkout`；结束/空闲时把分支 push
   回 origin。简单，但"本地分支/未提交的工作"活不下来，且每次进入沙箱都要拉一遍仓库。
2. **模板 + 快照**：把"依赖装好的基线"做成模板（`Template.build`），工作区放在模板里的
   固定路径，靠沙箱暂停保留。快，但工作区与沙箱实例绑定，实例没了就没了。
3. **对象存储同步**：进入时从 S3/OSS 拉一份工作区，退出时推回去（或反过来，把工作区当
   "真相在对象存储"）。最可控，也最慢，且要为每个租户做前缀隔离与加密。

无论哪条，**`RootDir` 这个"宿主路径"字段都得消失**，换成一个"沙箱内路径 + 一个能在里面
读写/执行的客户端"。这一步和 K8s 要做的改造**是同一件事**（见 §5），所以值得先做。

### 4.3 网络出口与工具链

- **出口**：E2B 支持按 hostname / CIDR 限制出站（如上）。我们会需要的出站是：模型网关
  （控制面的地址，通常在内网 → **要么把网关暴露到公网并用强 token，要么放弃 E2B**）、
  git remote、包仓库。默认应挡掉 `169.254.169.254` 等云元数据地址。
- **工具链**：agent 需要的东西（git、node、python、playwright 依赖、CJK 字体）必须在
  **模板里预装**。每到一个新沙箱再 `apt install` 一遍，等于把冷启动从秒级推到分钟级，也
  把外部仓库当成了启动依赖。
- **key 已经不是问题了，端点仍是**：`getHost(port)` 给的是**公网域名**。本仓库现在每个
  沙箱一个 key（§1.4），所以公网可达不再等于"一个口令开所有沙箱"；但把 8080 发布到公网
  依然把爆破面、DoS 面和镜像自身的漏洞一起送出去了。E2B 这条路仍应只让控制面访问。

### 4.4 时延、数据落地、成本

- **时延**：跨地域 RTT 是基础成本。命令执行、文件读写、截图帧（30KB/帧）全部走公网
  HTTPS。国内部署 + 海外沙箱服务，这一条通常直接否掉方案。
- **数据落地**：代码、模型 key（如果进了沙箱）、截图、终端历史全部离开你的机器，进入
  第三方。等保、客户合同、私有化交付往往是硬约束 —— 这不是技术权衡，是合规前提。
- **成本模型**：按沙箱-秒（CPU/内存）计费。长期挂着的沙箱比自建贵一个量级；空闲回收
  必须做，否则账单跟着"用户忘了关的会话数"走。自建的固定成本（节点）反而在低利用率时
  更贵、高利用率时更便宜 —— 这是一条曲线，不是一个结论。
- **同类一句话**：Daytona（Firecracker 类，环境创建 90ms 是厂商口径，快照基于 OCI 镜像）；
  Modal（默认 gVisor，Python 生态强，快照有 TTL 且恢复后是新的沙箱身份）；Fly / Cloudflare
  （microVM，快照与网络语义各不同，Cloudflare 明确说重启可能落到另一个位置）；
  Vercel Sandbox（Firecracker + 每沙箱代理 CA）。细节见
  `docs/research/agent-sandbox-isolation-briefing.md` §1.3，本文不重复。

**结论**：E2B 适合"我们要强隔离但不想运维集群，且能接受代码离开自己的机器"。它是
`SandboxProvider` 的第四个实现，**不是一种部署方式**；它逼你先把工作区模型从"宿主目录"
改成"远端 + 快照/对象存储"，而这正是 K8s 也要做的改造。

---

## 5. Provider 接口需要变成什么

现在的接口（`contracts/contracts.go:538`）：

```go
type SandboxProvider interface {
    Ensure(ctx, userID, projectID, repoURL, branch) (*Sandbox, error)
    Get(ctx, sandboxID) (*Sandbox, error)
    Pause(ctx, sandboxID) error
    Resume(ctx, sandboxID) (*Sandbox, error)
    Destroy(ctx, sandboxID) error
    Exec(ctx, sandboxID, argv, dir) (string, error)
}
```

它成立的前提是"有一个本地容器运行时，能从宿主路径看到工作区"。K8s 和 E2B 都不满足。
逐条说该改什么、该留什么。

| 方法 | compose（runner） | Kubernetes | 要改吗 |
|---|---|---|---|
| `Ensure` | 不变 | 建 Pod + PVC + Service，等 Ready，再做幂等 checkout | **语义要加一条**：返回的 `Sandbox` 必须带一个"可用的工作区句柄"，不只是状态 |
| `Get` | 不变 | Pod phase → `SandboxState`；"PVC 在、Pod 不在"要有明确状态 | 需要**新增状态语义**，否则 PAUSED 与 MISSING 分不清 |
| `Pause` | `docker stop` | **删 Pod，保 PVC**（= agent-sandbox 的 `replicas: 0`） | 签名不变，**文档要写明内存态丢失** |
| `Resume` | `docker start` | 重建 Pod + 等 Ready（~40s 冷启动） | 同上；调用方（`provision.go:265`）必须能等 |
| `Destroy` | 不变 | 删 Pod + Service，**PVC 保留** | 不变（对齐"不删数据卷"），另加 `Purge` 删 PVC |
| `Exec` | **runner 不提供**（`Runner.Exec` 返回错误） | `pods/exec`（client-go remotecommand） | **降级为调试接口**：生产路径不该依赖它，见下 |
| `Endpoint` | 容器名 `http://piforge-<hash8>:8080` | Service DNS 或 router | **已完成一半**：`sandbox.Remote`（界面见 runner.go）已经被 `main.go`/`sandbox_files.go` 依赖，不再是具体类型 |
| —（缺） | — | — | `Capabilities()`：能不能 pause、能不能 exec、端点是否需代理 |
| —（缺） | — | — | `Purge()`：显式删工作区（现在没有任何入口能删数据，这是对的，但需要一个"我确定"的入口） |

### 5.1 Pause/Resume 没有 Pod 对应物，诚实的等价物是什么

Pod 没有"暂停"。三种候选，逐个排除：

1. **`kubectl scale` 到 0**（Deployment/StatefulSet）：对单 Pod 的工作负载就是"删 Pod"，
   和工作区无关 —— PVC 在，所以工作区活着。**这就是等价物**，代价是内存态全丢：终端会话、
   Chromium、正在跑的 agent 进程。resume 是"重新调度 + 重新起容器"，40 秒。
2. **快照 PVC**：Snapshot 是备份/克隆语义，恢复要新建 PVC，耗时分钟级且依赖 CSI。
   **不要用它实现 pause** —— 用户点"暂停"，不该触发一次存储快照。
3. **`docker pause`（cgroup freezer）**：K8s 没有对应 API。别去 node 上 `crictl`，那是
   绕过控制面。

所以：`Pause` 的诚实定义是 **"释放计算，保住工作区"**，不是"挂起进程"。产品文案和状态机
都要按这个写：暂停后再打开，浏览器是新开的、终端是新的、agent 进程要重启（对话记录在
控制面，所以用户看到的是"页面还在，正在恢复"，不是"什么都没发生"）。

`Resume` 相应地要能**等**：现在是 `docker start` 后立刻 `Get`，K8s 下要 poll 到 Ready
（或返回 `STARTING` 并让上层重试）。

### 5.2 `Exec` 不是容器内 shell API 的替代品

这一条要写死在接口注释里：

- `pods/exec` 没有 PTY、没有会话、每次一个 API server 往返、输出要自己分帧；
- 现在的终端是**行式会话**：`sessions/create` → `write` → `view` 轮询（`term/remote.go`），
  依赖 AIO 自己的会话状态（`Close` 只停轮询，不发送 `exit`，因为镜像在 `exit` 后会把
  session id 卡住）。这套语义在 `exec` 上没有对应物；
- 面板的 git 走的是 `/v1/shell/exec`（`workspaceremote/exec.go:103`），不是 provider 的
  `Exec`。

所以 `Exec` 保留，但定位是"运维/调试"，不是"终端"。

### 5.3 最大的一处改动：`RootDir` 的语义

`Sandbox.RootDir` 现在是"宿主路径"（`json:"-"`，只在本进程用）。`provision`、`materialize`、
浏览器 profile 目录都拿它当本地目录。K8s/E2B 下没有这个目录。

两条路：

- **短期（推荐先做）**：`RootDir` 改成"沙箱内路径"，并把 `provision` 的 `AddWorktree`、
  `materialize.Mount` 换成"通过沙箱客户端执行/写文件"。`workspaceremote` 已经实现了
  这条通路（它把 `gitx` 的每个 argv 送进 `/v1/shell/exec`），所以工作量在"把调用点接过去"，
  不在"重写 git 逻辑"。
- **长期**：provider 暴露 `Workspace(ctx, sandboxID) (WorkspaceClient, error)`，上层只认
  这个客户端，不认路径。这样 compose/K8s/E2B 三种实现共用同一套 provision。

### 5.4 该保留的

- `Ensure` 的**幂等**：`planCheckout` 是纯函数、被逐条测试钉住，它不关心"在哪个机器上
  执行"，只关心"执行什么"—— 换 provider 时这段逻辑原样保留（执行器换掉）；
- 容器名派生：`sha256(userID + "/" + projectID)` 前 8 字节（`docker.go:139`）。K8s 里
  它就是 Pod 名/Service 名/PVC 名的同一个后缀，别重新发明；
- `Destroy` 不删数据；
- `ContainerPath` 的"只映射本项目目录"：它挡住的就是"两个项目解析到同一棵树"这个缺陷。

---

## 6. 推荐

| 场景 | 选 | 为什么 | 代价 |
|---|---|---|---|
| 单机自托管 / 演示 | compose + **sandbox-runner**（§1.4，已交付） | 一个 daemon、一个数据目录，控制面不需要 root、不需要 docker CLI | 多一个服务、一份 HTTP 契约 |
| 单机、只有一个人用 | `-sandbox local`（或单容器里全包） | 没有多租户，就不需要边界 | 没有边界 |
| 单机、已经被 socket 方案绑住 | socket proxy，**明确标注为过渡** | 改动最小 | 仍然是宿主 root，别对外说成隔离 |
| 多租户生产（预期目标） | **K8s：Pod per (user, project) + PVC + Service + gVisor** | 调度/配额/网络策略是现成的；隔离靠 RuntimeClass | client-go + 端点改造 + 40s 冷启动 |
| 多租户 + 需要 `docker build`/`sudo` | K8s + **Kata**（或按租户分 RuntimeClass） | 真内核边界 | 每 Pod 100–300MB、启动更慢 |
| 沙箱数上千、要秒开 | K8s + **agent-sandbox** warm pool | claim 时延 p50 ~92ms | CRD/控制器依赖，且 claim 不能带 per-session env |
| 不想运维集群、能接受数据出境 | **E2B** 作为 provider（§4） | 强隔离外包 | 工作区模型重写、公网端点、按秒计费 |

### 第一条要走的路（按可验收拆，每步能单独合）

1. **（已完成）把"在沙箱里跑命令"从 `docker exec` 换成 AIO 客户端**：
   `Docker.ensureCheckout` 现在把 `planCheckout` 的每一步交给 `aio.Client.Exec`，
   就绪探针跑在同一个 API 上，`checkoutReady` / `execAsSandboxUser` 已删除。
   验收：`planCheckout` 的用例一条不改；`docker_test.go` 的生命周期测试仍然过；provider
   里不再有 `exec -u gem`。
   为什么先做这条：它同时是 K8s 和 E2B 的前置条件，且不改变任何对外行为。

2. **把 `Endpoint` 提进 `contracts.SandboxProvider`**，`main.go` 不再持有
   `*sandbox.Docker`（`sandbox_files.go:22` 现在依赖具体类型）。
   验收：`-sandbox local` 下 `-sandbox-files remote` 仍报"需要容器"；compose 里端点变成
   容器名而不是 `127.0.0.1:<port>`。
   为什么第二条：端点形状（"名字"而不是"宿主端口"）是 compose 与 K8s 唯一能共用的部分。

3. **写 `sandbox/k8s` 骨架**：`Ensure`（Pod+PVC+Service）、`Get`（phase）、`Endpoint`
   （Service DNS）、`Destroy`（保 PVC）。先只在 kind/envtest 里跑，不进主分支的默认路径。
   验收：`kubectl apply` 一个 Pod 之后，`Get` 返回 READY，`Endpoint` 可被 `curl` 到
   `/v1/sandbox`。
   为什么最后：它需要前两步的结果，否则会写成一个"只能在 K8s 里跑、和别的 provider
   不兼容"的实现。

---

## 7. 不要做什么

- **一个共享沙箱给所有租户**。沙箱粒度是 `(project, user)`，容器边界必须和它一致。一个
  共享容器等于把多租户隔离交给进程权限和运气 —— 这正是 `docs/SANDBOX.md` 最后一节在说的
  事，K8s 不会让它变得更对。
- **一个容器里用 Linux 用户当租户边界**（§3）。共享内核 + 容器内有 root + agent 跑任意
  代码，三条加起来就不是边界。
- **bind mount + 远程 daemon**。路径在 daemon 主机解析，`-v` 会静默建一个空目录，控制面
  和沙箱从此各写各的，而且不报错。要跨机器就换 named volume，或者让 runner 和 daemon 同机。
- **在多租户集群里发布 8080 / 给每个沙箱开 Ingress**。用户浏览器不需要直连沙箱（§0 第 7
  条），所以每一个"方便调试"的暴露面，都是纯风险、没有收益。key 虽然已经是每沙箱一个，
  发布端口仍然把爆破面、DoS 面和镜像漏洞送出去，收益为零。
- **再用一个共享的 `SANDBOX_API_KEY`**（`-sandbox-api-key` 的固定覆盖）配一个跨租户可达
  的端点。这条曾经是这套设计里最危险的一处，现在默认已经不成立：每沙箱一个 key，A 的 key
  打 B 是 401（`scripts/verify-runner-sandbox.ps1` 会直接打这两个请求）。固定覆盖只应出现
  在调试一个部署的时候。
- **用 `pods/portforward` 当生产数据面**。每条连接过 API server，Pod 重启即断，截图流
  走这里会把控制面拖垮。它只用于调试。
- **用 Job 表达长期沙箱**。Job 的成功条件是"退出"，沙箱的成功条件是"活着"。
- **用 PVC 快照实现 pause**。用户点"暂停"不该触发一次存储快照+新建 PVC。
- **把 socket 挂给控制面然后声称"我们有多租户隔离"**。控制面被拿下 = 宿主 root，沙箱
  边界再好也没用。
- **给 runner 的 7801 发布端口或跨主机直连**。它没有 TLS，token 是唯一的认证，而且它
  能创建任意容器。只放在内网（`sandbox-control`），要跨机器就先补 mTLS 或前置代理。
- **给 socket 加 `:ro` 当作安全措施**。它不限制通过 socket 发起的 API 调用，只是让人以为
  限制住了（§1.4 第 1 条）。
- **在 PSA `restricted` 下硬跑 AIO 镜像**。`runAsNonRoot` 会让 entrypoint 的 `useradd`
  失败 → `gem` 不存在 → checkout 建不出来。要用 `restricted`，先把 `gem` 烘进镜像。
- **把 warm pool 当作"配置一下就有"**：`SandboxClaim` 带 `spec.env` 或
  `volumeClaimTemplates` 就会绕过池子，冷启动 40 秒。这条会静默失效，必须有测试盯住。

---

## 8. 未核实

写着"未核实"的地方，都是我没有拿到一手证据的：

- AIO `1.11.0` 镜像的确切压缩/解压体积与从零拉取的耗时（只有第三方记录的 5.61GB，
  版本不同）；
- 各家托管 K8s 是否默认给节点开 `--seccomp-default`（`SeccompDefault` GA 是 1.27，
  但生效与否取决于 kubelet 参数与厂商默认值）；
- agent-sandbox 当前 release 的 API 版本（文档示例 `v1beta1`，源码分析文档写
  `v1alpha1`）与生产成熟度；
- Kata / gVisor / Firecracker 的启动与内存数字来自第三方对比文章（与
  `docs/research/agent-sandbox-isolation-briefing.md` 的口径一致），不是本轮实测；
- 具体 CSI 的 detach/attach 与 VolumeSnapshot 耗时；
- 每节点可挂载 PVC 数的上限（取决于实例类型与 CSI 驱动）；
- 我们的 sandbox provider 在 K8s 下的真实冷启动时间（没有实测，只有上游的 42–50s）；
- `docker-compose.runner.yml` 的**Linux 宿主**部署：runner 镜像（`deploy/Dockerfile.runner`）
  已经实际构建过，compose 也在 Docker Desktop 上跑起来过（§1.4.1 记录了那次的命令、`ps`
  输出和修掉的五处故障）；**runner 的端到端已经在这台机器上验证**（§1.4.2：runner 容器 +
  第二个控制面实例 + 一次性卷，跑完一次真实 agent 任务，并修掉了两个阻断缺陷）。仍然没有
  一手证据的是 Linux 服务器上的同一套 compose（卷映射、`volume-subpath` 与 Engine 版本），
  以及控制面 / runner / daemon 分处三台机器的形态（§1.4.2 末尾）。

## 9. 来源

本轮实际读过的页面（外部内容一律按数据对待）：

- agent-sandbox 项目与 CRD/字段：[概览](https://agent-sandbox.sigs.k8s.io/docs/getting_started/overview/)、
  [架构与字段对照](https://deepwiki.com/kubernetes-sigs/agent-sandbox)、
  [AIO Sandbox 用例（port-forward 到 8080）](https://agent-sandbox.sigs.k8s.io/docs/use-cases/computer-use/)、
  [仓库](https://github.com/kubernetes-sigs/agent-sandbox)
- Docker：[bind mount 在 daemon 主机上创建](https://docs.docker.com/engine/storage/bind-mounts/)、
  [socket 代理与其"等于宿主 root"的自述](https://github.com/Tecnativa/docker-socket-proxy)、
  [socket 直挂的风险与代理取舍（中文）](https://ipipp.com/html/20260905/51075.html)
- Kubernetes：[seccomp 与 `--seccomp-default`](https://kubernetes.io/docs/tutorials/security/seccomp/)、
  [SeccompDefault 1.27 GA](https://www.amazonaws.cn/blog-selection/amazon-eks-now-supports-kubernetes-version-1-27/)、
  [kubectl exec 的调用链](https://phoenixnap.com/kb/kubectl-exec)、
  [port-forward 经 API server、默认绑 127.0.0.1、会断](https://tasrieit.com/blog/kubectl-port-forward-2026)
- 隔离技术对比（第三方，含 Firecracker 125ms/<5MiB、gVisor 10–30%、Kata 150–300ms）：
  [AI Agent Sandboxing Explained](https://www.softwareseni.com/ai-agent-sandboxing-explained-why-docker-is-not-enough-and-what-actually-works/)
- AIO Sandbox：[一条命令与 seccomp 说明](https://jianghushinian.cn/2025/11/09/aio-sandbox/)、
  [部署方式、`SANDBOX_API_KEY`、端点清单](https://home.wonlab.top/blog/open-source-projects/226-aio-sandbox)
- E2B：[SDK 表面（create/run/files/getHost/pause/kill、网络策略、超时与工作目录）](https://e2b.mintlify.app/agents/vercel-ai-sdk)
- 本仓库内的二次资料：`docs/research/agent-sandbox-isolation-briefing.md`（§3.2 的冷启动/
  warm pool 数字、§1.3 各平台定位，原始来源见该文索引）

## 10. 排错：租户会看到什么，原文是什么，谁该做什么

这一节是给运维和值班用的：**租户侧看到的字**（不是产品想说的话），它对应的内核/平台
行为，以及产品里有没有对应的信号。写它的直接原因是第二轮对抗验收
（`e2e/.artifacts/adversarial2/VERDICT.md` §5）：租户写爆单文件上限时看到的是 shell
自己的一句话，产品里没有任何一处解释它。

### 10.1 单文件写爆：`File size limit exceeded (core dumped)`

容器的 `--ulimit fsize=<配额>:<配额>` 让内核用 `SIGXFSZ` 终止越界的写。租户在终端里
看到的**就是这一行**（Linux 的 shell 对信号的固定描述），产品无法改写它：

```
$ fallocate -l 3G /workspace/ballast.bin
File size limit exceeded (core dumped)
$ echo $?
153                       # 128 + 25 (SIGXFSZ)
$ ls -l /workspace/ballast.bin
-rw-r--r-- 1 gem gem 0 ...   # 0 字节：写被内核拒绝，不是产品拒绝
```

`dd` 的表现是**截断在正好配额处**（2 GiB 配额 → 2147483648 字节）后同样收到 `SIGXFSZ`：

```
$ dd if=/dev/zero of=/workspace/big bs=1M count=3072
dd: error writing '/workspace/big': File size limit exceeded
2147483649 bytes (2.1 GB, 2.0 GiB) copied, ...    # 实际落在盘上 2147483648 字节
```

**这不是配额系统在说话，是内核在说话。** 区别要记住，因为它们指向不同的处置：

| 现象 | 谁说的 | 含义 | 处置 |
|---|---|---|---|
| `File size limit exceeded (core dumped)`，exit 153 | 内核（`SIGXFSZ`） | **单个文件**超过每文件上限（`--ulimit fsize`，默认等于 `-sandbox-disk-quota`，2 GiB） | 拆文件、改用流式/临时文件；这不是 bug，也不是"配额用完" |
| 终端里没有任何提示，下一次 agent 运行被拒：HTTP 507 `sandbox_disk_quota`，会话里一条 `platform.error` | 控制面 | **整个工作区**超过总预算（`du` 走到的字节数 > 配额） | 删东西；沙箱本身仍可进（终端可用），只是 agent 不再启动 |
| `over=true` 后沙箱被暂停（`state=PAUSED`，`message` 里写 "disk quota exceeded: paused by the platform"） | 控制面 watchdog | 停了 agent 之后**还在长**（终端里接着写） | 下一条消息会自动唤醒；先清理再继续 |
| admin 行 `bytes=0, over=false, unmeasurable=true`，`error="…context deadline exceeded"` | 控制面 | 量不出来（沙箱无响应，例如 pids 被风暴打满） | 页面显示「未知」而不是 0；见 10.2 |

**没有做的事（明确写下）**：不改写 `SIGXFSZ` 的文本（内核信号无法被产品改写，包一层
shell 只会让 `exit 153` 变成别的数，更容易误导）；不给终端写者发消息（终端是
`gem` 的 pty，控制面不在那条管道的写路径上）；所以这条只能靠文档 + 那张表。

### 10.2 沙箱"在跑但什么都不做"：pids 被打满

租户跑进程风暴（`bash -c ':(){ :|:& };:'` 或其变体）时，容器的 `pids` cgroup 会被打
满。容器仍显示 `running`，但**任何新进程都起不来**：

```
$ docker exec <sandbox> sh -c 'echo ok'
OCI runtime exec failed: exec failed: unable to start container process:
  error executing setns process: exit status 1; runc init error(s):
  nsexec-0[15297]: unable to spawn stage-1: Resource temporarily unavailable   # exit 128
$ docker exec <sandbox> cat /sys/fs/cgroup/pids.current
sh: 1: Cannot fork
```

产品的处置是自动的（`backend/internal/httpapi/sandbox_watch.go`）：每 10 s 通过沙箱
自己的 API 跑一次 `true`（3 s 上限），连续 2 次失败判定为无响应 → 停容器再启（工作区
在卷上，对话和文件都在）→ 再探一次确认 → 会话里写 `platform.error`。租户看到的是：

```
沙箱无响应，正在恢复：<原因>。工作区、对话和文件都在卷上，恢复不会丢数据。
…（约 5 秒，实测 stop 4.99 s + start 0.91 s）
沙箱已恢复（用时 5.9 秒）。对话和工作区都还在，可以继续。
```

会话视图顶部同时出现「沙箱无响应，正在恢复」和**「恢复」按钮**（同一个动作的手动入口，
`POST /v1/sessions/{id}/sandbox/recover`）；`GET /v1/sessions/{id}/sandbox` 的
`responsive` 字段是缓存的探测结果（`null` = 还没探过，不是"坏了"）。

`pids` 默认值本批从 512 降到 **256**（`deploy/tenant-limits.json`、
`docker-compose.runner.yml` 的 `${PIFORGE_SANDBOX_PIDS:-256}`）。实测依据：镜像自身
运行时（`gem.sh` → supervisord → Jupyter/Chrome/node）空载 **83–85 个 pid（线程）/ 25
个进程**，`pids=64` 时有一个 node 服务起不来，`pids>=128` 完整；正常重活（40 个并发
进程 + 8 个 node worker 线程）在 256 下是 125 个 pid。但**任何上限都挡不住风暴**（64 到
1024 实测一样被打满），所以真正的保证是上面的探测 + 恢复，不是一个更小的数字。

### 10.3 恢复之后对话是空的（已修）

`pause` → `resume` 之后、下一条 prompt 之前，`GET /v1/sessions/{id}/entries` 曾经返回
`{"entries":[],"leaf_id":""}`，而 `/stats` 仍是 2 条用户 / 2 条助手、沙箱里的
`/workspace/.piforge/sessions/<id>.jsonl` 完整。现在这个端点在没有活进程时**读沙箱里
的那个文件**（`pi.RuntimeConfig.TranscriptFor` → `cmd/piforged` 经沙箱 API 读取），
`?since=<entry>` 也在文件上生效。排错时看两处即可：`/stats` 的条数、以及
`docker exec <sandbox> wc -c /workspace/.piforge/sessions/<id>.jsonl`。

### 10.4 admin 的 `files` 计数（已修）

`GET /v1/admin/status` 的 `sandbox_usage[].files` 以前恒为 0（测量只取了 `du -sb` 的
字节数）。现在同一次遍历里用 `find -printf '%y %s\n' | awk` 同时给出字节数与文件数，
字节数与 `du -sb` 逐字节一致（400 个文件的树：`412288 400`）；量不出来时仍是
`unmeasurable: true`，不会伪装成 0。

### 10.5 写进程被"冻住"（`T` 状态）：总预算突发的当场刹车

沙箱内的执行器（`piforged quota-enforcer`，§1.4.8 (2)）每 100 ms 走一次工作区；一旦总和
超过预算，它给**持有可写文件描述符的进程以及它们所在的进程树**发 `SIGSTOP`。租户那边
看到的就是冻结，没有任何提示：

```
$ dd if=/dev/zero of=/workspace/big bs=1M count=4000 &
[1] 1234
$ ps -o pid,stat,args -p 1234
    PID STAT COMMAND
   1234 T    dd if=/dev/zero of=/workspace/big bs=1M count=4000
$ echo $?          # 什么都没发生；进程被停住，不是被杀
$ kill -CONT 1234  # 同 uid 可以自己解冻——但总量还在预算之上，它会立刻被再冻一次
```

配套的两个文件（都在工作区里，租户可读）：

```
/workspace/.piforge/quota-brake.json   上一次动作：action、used_bytes、limit_bytes、writers、readings
/tmp/quota-enforcer.log                执行器自己的日志（容器内 /tmp，随容器删除）
```

| 现象 | 谁做的 | 含义 | 处置 |
|---|---|---|---|
| 写进程 `STAT=T`，工作区大小停住 | 沙箱内执行器 | 总和超过预算，写者被暂停 | 删文件让总量回到预算下；执行器会 `clear` 掉记忆，下一条消息唤醒沙箱即可 |
| 进程直接消失（无 exit code） | 沙箱内执行器 | 冻结之后又长了 ≥ 1 MiB（出现了新的写者），升级为 `SIGKILL` | 同上；如果是 agent 的工具被杀，会话里会看到那次工具失败 |
| `quota-brake.json` 里 `readings` 一直涨但 `used_bytes` 不动 | 执行器 | 写者已经停住，平台看门狗在等"还长不长" | 正常；看门狗不会再暂停，因为不再增长 |
| 租户 `pkill -f quota-enforcer` 之后继续写 | 租户 | 执行器是同 uid，杀得掉 | 平台看门狗是剩下的那层（2 s/10 s），并且下次容器启动 hook 会把它拉起来 |

**没有做的事（明确写下）**：不在产品里提供"解冻"按钮（恢复路径就是暂停/恢复，和超配额
的树一样）；不把 `SIGSTOP` 换成可读的拒绝（信号没有消息通道，写进程不是通过产品 API
写的）；也不声称这是文件系统配额——上界是 `du` 间隔 + 反应时间，实测数字见 §1.4.8。