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
| POST | `/v1/sandboxes` | `{user_id, project_id, image, repo_url, branch, api_port, api_key, cpus, memory, network}` | `200` + record；同名容器已存在则复用 | `Ensure` |
| GET | `/v1/sandboxes/{id}` | — | `200` + record | `Get` |
| POST | `/v1/sandboxes/{id}/pause` | — | `204` | `Pause` |
| POST | `/v1/sandboxes/{id}/resume` | — | `200` + record | `Resume` |
| DELETE | `/v1/sandboxes/{id}` | — | `204`（删容器，**不删工作区**） | `Destroy` |
| POST | `/v1/sandboxes/{id}/worktrees` | `{repo_dir, path, branch}`（可选 `api_key`） | `204` | `AddWorktree` |

record 是 `{id, state, root_dir, container_path_root, endpoint, api_key?}`：`id` 仍然是
`piforge-<sha256(user/project) 前 8 字节>`，`root_dir` 是共享卷上
`<SANDBOX_DATA_ROOT>/<user>/<project>`，`container_path_root` 是 `/workspace`（控制面据此
校验两侧对挂载点的一致理解），`endpoint` 是控制面可达的沙箱 API 地址，`api_key` **只在
create 的响应里出现一次**（见下）。

**没有第七个端点**：没有 health、没有 exec、没有文件读写。"在沙箱里跑命令"继续走沙箱
自己的 `/v1/shell/exec` 与文件 API（§0 第 6 条），runner 不复制它。

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

1. **agent 在控制面容器里执行，不在沙箱里。** 上面的 `ps`、pi 的 cwd
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


为什么是它：

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

### 1.5 推荐方案下仍然会坏的地方（这一节是重点）

| 事件 | 会发生什么 | 缓解 |
|---|---|---|
| **控制面重启** | 沙箱容器还在（`--restart unless-stopped`）。容器名由 `(user, project)` 派生，`docker inspect` 能找回；端口由 `docker port` 重读，`sandbox_files.go` 的客户端缓存 30s 过期后自愈。runner 模式下端点由 runner 的 `GET /v1/sandboxes/{id}` 重问，同样自愈 | 已经是现状，没问题。但**沙箱里的进程**（Chromium、AIO 的 shell 会话）在 daemon 重启时全丢，终端会断 |
| **daemon 在远程机器** | `-v` 的源路径在 daemon 主机解析（Docker 文档原话：bind mount "created to the Docker daemon host"，[Bind mounts](https://docs.docker.com/engine/storage/bind-mounts/)）。`-v` 会**静默创建一个空目录**，于是容器看到空 `/workspace`，控制面往自己磁盘写，两边都不报错。`workspaceMount` 比较的是字符串，两台机器上的 `/data/sandboxes/u/p` 完全相同，比较通过 —— 检测不到 | runner 与 daemon 同机；compose 叠加层让沙箱直接挂**卷+子路径**（`SANDBOX_WORKSPACE_VOLUME`，§1.4 第 2 条），宿主路径根本不进这条链路，daemon 在哪台机器上都不影响。控制面仍在 `Ensure` 时把 runner 报的 `root_dir` 与自己算的路径比一次并 WARN —— 但注意它比的是**字符串**：bind 形态下"两边名字相同、实际是两棵树"这件事它看不出来（§1.4.1 第 2 条就是这么坏的）。跨机器时不要靠 SSH 建目录 |
| **控制面在容器里，沙箱也在容器里** | `127.0.0.1:<published>` 是控制面自己的 loopback，连不上沙箱。`-sandbox docker` 仍然发布 `127.0.0.1:<port>` 并从 `docker port` 读回 | **已解决**：`SANDBOX_CONTAINER_ENDPOINT=1` 让 runner 不发布端口、返回 `http://piforge-<hash8>:8080`（Docker 内建 DNS），控制面与 runner 都加入 `piforge-sandboxes`。**这一步同时去掉了宿主上的 8080 暴露面** |
| **`docker compose down`** | 沙箱容器不是 compose 管的，不会跟着删；runner 停掉也不删容器（无状态）。`down -v` 会尝试删卷，但只要还有**任意**一个 `piforge-*` 容器（包括 `Created`/`Exited`）挂着它，就只打印 `Volume piforge-data Resource is still in use` 并继续，退出码仍是 0（§1.4.1 实测） | 清理顺序：先 `docker rm -f piforge-*`（只删你认识的），再 `docker compose ... down -v`，然后确认 `docker volume ls` 里没有 `piforge-data`。runner 尚未实现按 label 对账（`POST` 只会建/复用，不会回收孤儿）；`Ensure` 现在会在 `docker run` 失败时删掉自己半创建的对象，但已经存在的孤儿仍要手工清 |
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
