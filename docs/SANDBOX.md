# 沙箱：本地目录 与 Docker 容器

控制面**从不执行不可信代码**：它只决定"这个 (project, user) 的沙箱在哪里"，然后把
agent、浏览器、终端指过去。这条不变量在任何 provider 上都成立。

## 两个 provider

| | `local`（默认） | `docker` |
|---|---|---|
| 隔离边界 | 一个目录 | 一个容器 |
| 浏览器 | 宿主 Chrome，按沙箱复用 profile | 容器内 Chromium（默认 `remote`） |
| 终端 | 宿主进程 + PTY | 容器内 shell 会话（默认 `remote`） |
| 面板文件读写 | 宿主目录（`fsx`，仅 `-sandbox-files host` 显式指定） | 容器文件 API（默认 `remote`） |
| 适合 | 单机自托管、开发 | 多租户、需要真实边界 |
| 启用 | 默认 | `-sandbox docker -sandbox-image <image>` |

`PIFORGE_SANDBOX=docker`、`PIFORGE_SANDBOX_IMAGE`、`PIFORGE_SANDBOX_NETWORK`、
`PIFORGE_SANDBOX_CPUS`、`PIFORGE_SANDBOX_MEMORY` 是等价的环境变量。

## 三个功能开关：默认跟随 provider

浏览器、终端、面板文件读写各自还有一个 `host` / `remote` 开关。它们的默认值是 `auto`，
意思是"由 provider 决定"，而不是写死 `host`：

| flag | 环境变量 | 取值 | `-sandbox local` | `-sandbox docker` |
|---|---|---|---|---|
| `-sandbox` | `PIFORGE_SANDBOX` | `local`（默认）/ `docker` | 目录沙箱 | 容器沙箱 |
| `-sandbox-browser` | `PIFORGE_SANDBOX_BROWSER` | `auto`（默认）/ `host` / `remote` | `auto` → `host` | `auto` → `remote` |
| `-sandbox-terminal` | `PIFORGE_SANDBOX_TERMINAL` | `auto`（默认）/ `host` / `remote` | `auto` → `host` | `auto` → `remote` |
| `-sandbox-files` | `PIFORGE_SANDBOX_FILES` | `auto`（默认）/ `host` / `remote` | `auto` → `host` | `auto` → `remote` |

其余 docker 参数不变：`-sandbox-image`、`-sandbox-network`、`-sandbox-cpus`、
`-sandbox-memory`、`-sandbox-api-key`。

三个开关在启动时一次解析完，日志里直接给出结果，所以看日志就知道这个部署长什么样：

```
level=INFO msg="sandbox placement resolved" sandbox=docker browser=remote terminal=remote files=remote
```

`-sandbox local` 下写 `remote` 是**启动错误**，不是运行时降级：没有容器可连，"远程"就不是
一个可选项。报错会点名那个 flag 并说明原因，而不是等到第一次请求才失败。

## 为什么默认是"全在容器里"

三个开关只开一部分，是最难诊断的那种部署：浏览器在容器里、文件面板在宿主机上，面板看到的是
绑定目录里的另一份视图——同一个工作区、两个读者，谁也不知道对方看见了什么。所以
`-sandbox docker` 默认把三个都放进容器：agent、浏览器、终端、文件面板是同一台机器、同一个
`/workspace`。默认值不该产生一个没人会主动挑选的组合。

混合部署仍然允许——确实有人想让宿主机的 Chrome 连容器里的工作区来调镜像——但它必须是
显式写出来的（`-sandbox-files host`），而且启动时会有一条 WARN，把留在宿主机上的功能全部
列出来，日志本身就能说明这个部署到底是什么：

```
level=WARN msg="sandbox placement is mixed: these features still run on the host" sandbox=docker host=files remote="browser, terminal" detail="the agent works inside the container, so a host-side panel is a second view of one workspace; pass remote for every feature, or use -sandbox local for all of them"
```

`cmd/piforged/sandbox_placement_test.go` 钉住三件事：docker 默认全 `remote`、显式 `host` 被
遵守且被报告、`local` + `remote` 启动报错。

## docker provider 现在做了什么

- `Ensure`：按 `(user, project)` 派生容器名 `piforge-<hash8>` 和工作区宿主目录
  `<data>/sandboxes/<user>/<project>`，不存在就 `docker run -d`，存在就复用；**只把这个项目的
  目录**挂到 `/workspace`，暂停/重启不丢工作区；
- `Ensure` 还在容器起来之后、返回 READY 之前把 `/workspace/repo` 变成 checkout，见下一节；
- `RootDir`：**宿主侧**那个 per-project 绑定目录，不是容器里的 `/workspace`。控制面自己的
  git/文件读写跑在宿主上，写的位置必须正好是容器看到的位置；`ContainerPath` 负责把
  `<data>/sandboxes/<user>/<project>/...` 翻译成 `/workspace/...`；
- 复用已有容器前先 `docker inspect` 它的 `/workspace` 挂载源：与这个 pair 的目录不一致
  （例如旧版本把共享的 `<data>/sandboxes` 挂进来的容器）就**删容器重建**，宿主目录和里面的
  文件一律保留。旧容器共享的目录不会被任何新项目"继承"；
- 沙箱记录用容器名当 id：后续的 pause/resume/destroy/endpoint/exec 都拿这个 id 去找容器，
  记录里另造一个 uuid 会让它们全都找不到人；
- `Get`：`docker inspect` 的 `.State` 映射到 `READY / PAUSED / MISSING`；
- `Pause`：`docker stop`（停容器，不是删）；
- `Resume`：`docker start`；
- `Destroy`：`docker rm -f`，**不删数据卷**——删掉别人未提交的工作不该由"销毁沙箱"来代决；
- `Exec`：`docker exec`。

### 为什么工作区必须按项目分目录

这是租户边界，不是目录偏好。旧实现把整个 `<data>/sandboxes` 挂进每个容器，于是所有项目都
解析到 `<data>/sandboxes/repo`：第二个项目会直接看到第一个项目的 checkout——未提交的改动、
`.env` 里的密钥、提交历史都在里面；两个用户各自的项目也会互相看见。现在宿主目录和容器名
由同一对 `(user, project)` 派生，同一项目重启后仍落到同一目录，不同项目则一定不同。

`internal/sandbox/docker_test.go` 里钉住这条边界（需要 AIO 镜像，没有镜像时跳过并说明）：
`TestDockerWorkspacePathIsPerPair`（纯函数，无 Docker）、
`TestDockerProviderSeparatesProjectWorkspaces`（两个项目互不可见）、
`TestDockerProviderReusesTheProjectWorkspace`（同一项目幂等，同目录同容器）、
`TestDockerProviderRecreatesAContainerWithTheWrongMount`（挂载不匹配就重建，旧目录保留）。

## 工作区：checkout 由沙箱用户创建，且只在缺失时创建

`-sandbox docker` 此前只 `docker run`，`/workspace/repo` 里永远没有 `.git`：新建项目后
`GET /git/status`、`POST /git/stage`、`POST /git/commit` 全是 500，容器里 git 的原话是

```
fatal: not a git repository (or any parent up to mount point /)
```

第二层同样重要：宿主（或容器里的 root）建出来的 `.git` 在容器看来是别人的，面板 git 以 `gem`
（uid 1000）运行时失败：

```
fatal: Unable to create '/workspace/repo/.git/index.lock': Permission denied
```

所以 `Ensure` 在容器 up 之后、返回 READY 之前，通过沙箱自己的 HTTP API
（`POST /v1/shell/exec`，`internal/aio`）把 checkout 建出来。命令由镜像自己的 shell 执行，
它已经是 `gem`（uid 1000），不需要再指定用户，也不需要控制面有 docker CLI 或本机 daemon
（`planCheckout` 仍是纯函数，argv 逐条被单元测试钉住）：

| 情况 | 执行 |
|---|---|
| 已有 `.git` 且 `HEAD` 可解析 | 什么都不做：不 re-init、不 reset、不补第二个空提交 |
| 无 `repo_url` | `git init -q -b <branch>`（默认 `main`）+ 仓库内身份 + 一个空提交 |
| 有 `repo_url` | `git clone --quiet -- <url> .`，再 `git checkout -q <branch>`；分支只存在于远端时用 `git checkout -q -b <branch>`（对齐 Local 的 `CreateAndCheckout`） |

- 身份写在 checkout 里（`PiForge Sandbox <sandbox@piforge.local>`），不是用 `-c` 临时传：
  面板后续的提交会沿用同一个作者（`gitx.identityArgs` 先读 config），`git log` 因此能证明
  这条历史是容器里的 git 写的；
- 绑定挂载的布局没变：控制面仍然往同一个宿主目录写文件，容器看到的是同一批字节，这一步只决定
  `.git` 归谁；
- 目录是命令本身显式进入的：镜像会忽略请求里的 `cwd`，所以 `aio.Client.Exec` 把
  `cd -- <dir> &&` 拼进真正发给 API 的命令行；
- 每条 git 都带 `-c safe.directory=/workspace/repo`：绑定挂载在 Docker Desktop 等宿主上可能
  显示成别的 owner，git 2.34 会以 "detected dubious ownership" 拒绝它；
- `docker run -d` 返回时容器的服务还没开始监听，第一次探针会重试到 45s，超时报错点名 checkout，
  **不退回 `docker exec`**：一个只能在有 daemon 的机器上工作的 checkout 正是这次要修的东西；
- 失败即失败：`Ensure` 报错会点名步骤（`init` / `clone` / `checkout` / `commit`），不会返回一个
  不能提交的"READY"沙箱；刚 create 出来的容器会被删掉，下一次从干净状态重试；

2026-10-29 实测（`piforged -sandbox docker`，容器 `piforge-81042b4b785a90cf`，仓库
`prj_01M633SAZ9E7C21X519XEB8AEB`）：

| 步骤 | 证据 |
|---|---|
| 建会话（provision 触发 `Ensure`） | `SESSION 201`，`worktree_id=wt_...`；容器内 `stat -c %U /workspace/repo/.git` → `gem` |
| `git status`（容器内，gem） | 退出码 0；`git log` 已有 `f158c62 piforge: initial commit` |
| `PUT /files?path=notes.txt` | `200 {"ok":true}` |
| `GET /git/status` | `200 {"branch":"main","clean":false,"changes":[{"path":"notes.txt","index":"?","worktree":"?"}]}` |
| `POST /git/stage` | `200 {"ok":true}` |
| `POST /git/commit` | `200 {"commit":"78f0be3ebf1cfc84d3d46996074ea36ac838e541"}` |
| `docker exec -u gem <c> git -C /workspace/repo log -1 --format='%an\|%ae\|%s'` | `PiForge Sandbox\|sandbox@piforge.local\|feat: prove docker commits work` |
| 同一个 `Ensure` 再来一次（provider 测试） | `HEAD` 不变、提交数不变（`TestDockerProviderInitialisesTheCheckout`） |

checkout 改走 `/v1/shell/exec` 之后再跑同一条链路（容器 `piforge-c90d42c4509bae67`，
`127.0.0.1:44885`）：`PUT /files?path=x.txt` → `200 {"ok":true}`，`GET /git/status` → 200
（`x.txt` 未跟踪），`POST /git/stage` → `200 {"ok":true}`，`POST /git/commit` →
`200 {"commit":"0418758…"}`；`docker exec -u gem <c> git -C /workspace/repo log --oneline -3`
给出 `0418758 feat: prove the api checkout` / `20f67bc piforge: initial commit`，`.git` 仍归
`gem`。checkout 的探针、mkdir 和四条 git 都由 AIO 客户端执行，
`TestDockerProviderCheckoutUsesTheSandboxAPI` 用只有 API 的假客户端钉住了这条路径，
`TestDockerProviderCheckoutCrossesTheSandboxAPI` 在真镜像上记录实际发出的命令。

`internal/sandbox/docker_test.go` 会真的起一个容器跑完整生命周期；没有 docker 或
daemon 不可达时**跳过并说明原因**，不会假装通过。

## 浏览器：已能连上容器里的 Chromium

`browser.Manager` 加了 `Remote` 解析器：给出 CDP 地址就直接**连**过去，不再本机起 Chrome。
对着真镜像验证过：

- 握手带 `X-AIO-API-Key`（它的 `/cdp/*` 反代和 HTTP API 共用一个 key，不带就是 401）；
- **attach + navigate 已经通了**；
- **截图也通了**：`Page.captureScreenshot` 需要先在该 session 上 `Page.enable`，
  否则 Chrome 回的是 "Not attached to an active page"——听起来像 attach 失败，其实不是。
  实测 28498 字节 PNG。

容器里的 Chromium 还要一个启动条件：Docker 默认 seccomp 下它连自己的命名空间都建不起来，
浏览器在 CDP 打开之前就死了——

```
Failed to move to new namespace: PID namespaces supported, Network namespace supported,
but failed: errno = Operation not permitted
FATAL:content/browser/zygote_host/zygote_host_impl_linux.cc:207] Check failed
```

`docker provider` 因此给开了服务端口的容器传 `BROWSER_NO_SANDBOX=--no-sandbox`（镜像自己的开关，
`gem.sh` 把它折进 `BROWSER_EXTRA_ARGS`）。为什么不是 `--security-opt seccomp=unconfined`：后者放宽的是
**容器**的系统调用面，前者只关掉浏览器进程内部的沙箱，渲染器仍然在容器边界之内——这条边界才是
这个 provider 承诺的那条。

启用方式（`-sandbox docker` 现在默认就是 `remote`；显式写出仍然有效，也仍然被推荐给
要固定配置的部署）：

```bash
piforged -sandbox docker \
         -sandbox-image <all-in-one-sandbox:1.11.0> \
         -sandbox-api-key <key> \
         -sandbox-browser remote
```

## 终端：已经能跑在容器里

AIO 的 shell 不是 PTY，是**命令会话**：`sessions/create` → `write`（input + press_enter）→
`view`（拿 output）。`internal/term/remote.go` 把它接成了和宿主 PTY 相同的字节流：
同一个 ring buffer、同样的 `Write/Read/Info/Close`，所以 WebSocket 那一层不用改。

对着真镜像（1.11.0）验证过：

```
write  echo remote-terminal-ok
view   "echo remote-terminal-ok\r\nremote-terminal-ok\n"
```

终端桥做的几件具体事：

- **按行缓冲输入**：`write` 每次调用都会立即执行，`press_enter:false` 也不会只“输入不回车”，
  所以半行绝不能发出去。退格键（`\x7f`）只改本地缓冲，并且回显 `\b \b`；
- **本地回显**：容器不回显用户输入，不回显的话终端看起来像死了。输入、换行、退格都由
  桥自己写进 ring；
- **轮询增量**：每 175ms 调一次 `view`，只把 `output` 比上次多出来的后缀写进 ring；
  `view` 返回的是累计全文，不能整段重放；
- **补换行**：镜像把多条命令的输出直接粘在一起（`foo` + `bar` = `foobar`），所以一条命令
  从 `running` 变成 `completed` 时补一个 `\n`，且只补一次；
- **resize 只记录不转发**：它没有 PTY，也没有窗口大小这个概念；
- **Close 只停轮询**：不发送 `exit`。实测镜像在 `exit` 之后会把同一个 session id 卡住
  （后续 write 返回成功但永远不执行），而每个 tab 用的是新 id，容器生命周期负责回收 shell。

启用方式（`PIFORGE_SANDBOX_TERMINAL=remote` 等价）：

```bash
piforged -sandbox docker \
         -sandbox-image <all-in-one-sandbox:1.11.0> \
         -sandbox-api-key <key> \
         -sandbox-terminal remote
```

只有 `-sandbox docker` 下才有意义，并且 docker 下默认就是它（`-sandbox local` 下写 `remote`
会启动失败，见上面的开关表）。docker provider 会因此
发布容器的 8080 端口（`Endpoint` 从 `docker port` 读回实际端口），并把
`-sandbox-api-key` 作为 `SANDBOX_API_KEY` 传给容器。工作区的宿主路径由
`Docker.ContainerPath` 映射到容器里的 `/workspace`，shell 就从该 worktree 启动。

测试分两层：`internal/term/remote_test.go` 用 httptest 钉住“只追加新后缀”和“退格不发出去”；
`internal/term/remote_live_test.go` 用 `PIFORGE_AIO_URL` / `PIFORGE_AIO_KEY` 对着真容器跑，
没有环境变量时干净跳过。

## 端到端：走 piforged 自己起的容器

前面的验证都是"直接 `docker run` 一个镜像"。真正会出问题的是 `piforged` 创建的那个容器，
所以现在按下面的方式验证过一遍：

```
piforged -addr 127.0.0.1:8090 -data <tmp> -dev -fake-pi \
         -sandbox docker -sandbox-image <all-in-one-sandbox:1.11.0> \
         -sandbox-api-key <key> -sandbox-browser remote -sandbox-terminal remote
```

全程只走 HTTP API（cookie 登录 + `X-PF-CSRF`）：建项目 → 建会话 → 发一条消息让 provision 跑。

```
docker ps   piforge-c23e36153abed8ee   127.0.0.1:46249->8080/tcp

GET  /v1/sessions/<id>/browser          ready 帧 urls = ws://127.0.0.1:46249/cdp/...
                                        首个二进制帧 14434 字节 JPEG（ff d8 ff e0），1280x885
POST /v1/sessions/<id>/browser/screenshot
                                        201 {"bytes":30230,"path":"screenshots/....png"}
                                        文件头 89 50 4e 47 0d 0a 1a 0a
WS   /v1/sessions/<id>/pty               "echo e2e-docker-ok\r\n" + "e2e-docker-ok\n"
                                         "uname -sr; hostname" → Linux ...-microsoft-standard-WSL2 / <容器 id>
```

容器里 `ls /workspace` 能看到 provision 写下的 `repo`，截图也落在容器的同一路径下：宿主目录和
容器目录现在是同一个目录，不是两份各写各的。

## 端到端：容器由 piforged 自己创建，浏览器与终端都在里面

2026-10-29 实测（提交 `24b0703`），容器 `piforge-<hash8>`，`127.0.0.1:46249->8080`：

| 项目 | 证据 |
|---|---|
| 截图 | `201 {"bytes":30230}`，首字节 `89 50 4e 47`；容器内同一文件在 `/workspace/repo/screenshots/…` |
| 浏览器 WS | ready 帧带容器 CDP 地址，随后 14434 字节 JPEG 帧 |
| 终端 | 发 `echo e2e-docker-ok\n` 收到 `e2e-docker-ok\n`；身份探测 `Linux 6.18.54.1-microsoft-standard-WSL2 / 9dfb57b19fb5 / /workspace/repo`（容器，不是宿主） |
| 工作区 | 容器内 `ls /workspace` 有 provisioned 的 repo |

这条链路此前有三处断线，都已修：数据库存的沙箱 id 与容器名不一致、`RootDir` 报的是容器内路径（导致往宿主 `C:\workspace` 写）、浏览器门禁看的是"本机有没有 Chrome"。另外容器里的 Chromium 需要镜像自带的 `BROWSER_NO_SANDBOX=--no-sandbox`（比放开容器 seccomp 更窄）。

## 文件：面板的文件树、预览和编辑走容器

`-sandbox-files remote`（等价 `PIFORGE_SANDBOX_FILES=remote`，只在 `-sandbox docker` 下可用，
并且现在是 docker 的默认值）把 `contracts.Workspace` 换成 `internal/workspaceremote`：

- 文件树、预览、编辑、搜索走容器的 `/v1/file/*`（list / read / write / glob / grep）。
  没有 delete 端点，删除是唯一一条 shell 命令（`rm -rf`，并区分"本来就不存在"）；
- 大文件按本地同样的阈值分档：≤2 MiB 全文，≤20 MiB 用 `head`/`tail` 取头尾并标记截断，
  更大的只给元数据；
- git 不另写一套解析：`internal/gitx` 的 porcelain v2 解析、路径校验、"nothing to commit"
  分类、hunk 应用全部保留。`workspaceremote.ShellExecutor` 只是把同一份 argv 送进容器执行，
  stdout 与 stderr 分开取回（私有临时目录 + base64，stdin 写成容器里的临时文件），
  所以面板对 git 的判断和宿主模式一字不差；
- 有一处必须特判：bind mount 在 Docker Desktop/drvfs 上把宿主建出来的 checkout 显示成
  root 所有，而沙箱 shell 是 `gem`，git 2.34 因此拒绝这个仓库
  （`fatal: detected dubious ownership`）。执行器给每条 git 命令加
  `-c safe.directory=<容器内根>`：只信任这个工作区，而不是 `safe.directory=*`；
- 显式写 `-sandbox-files host` 时行为与之前逐字节一致（这是混合部署，启动时会有一条
  WARN 点名 files）；`-sandbox-files remote` 配 `-sandbox local` 直接报错——没有容器可连，
  而不是悄悄退回宿主目录。

2026-10-29 实测（piforged 自己起的容器 `piforge-750aee4547df7564`，`127.0.0.1:46635->8080`）：

| 步骤 | 证据 |
|---|---|
| `PUT /v1/sessions/<id>/files?path=notes.txt`，body `hello` | `200 {"ok":true}`；`docker exec cat /workspace/repo/notes.txt` → `hello`，`od -An -tx1` → `68 65 6c 6c 6f` |
| `GET /files/tree` | `notes.txt dir=False size=5` |
| 容器内写 `agent.txt` | `GET /files?path=agent.txt` → `size=19 body="from the container\n"` |
| 面板 git | `POST /git/stage` → `notes.txt:A.`；`POST /git/commit` → `c3b0f6fc…`；`GET /git/log` 作者 `Container Git`（该身份只在容器里配置过，证明命令真的在容器里执行） |
| 搜索 | `/search?q=hello&type=content` → `notes.txt:1 hello`；`type=name` → `notes.txt` |
| 删除 | `DELETE /files?path=agent.txt` → 200；`docker exec test -e` → `1` |
| 反向对照 | `docker stop` 容器后同一个 `GET /files` 返回 500：如果面板还在读宿主目录，它会照常成功 |

## 还没做的（这一节是重点）

浏览器、终端和**文件读写**都已经能连容器。剩下的边界：

- `GET /files?download=1` 仍然用宿主路径流文件（bind mount 下是同一批文件）。当控制面与
  Docker daemon 不在同一台机器、宿主路径不存在时，它回退到预览 body，大文件下载会被截断；
- 容器内的文件 API 没有 rename/move 端点，面板也没有这个功能，所以没有补；
- `-sandbox-files remote` 只影响面板这一侧；主 checkout 现在由容器里的 `gem` 建（见上一节），
  但**分支会话的 linked worktree 仍由宿主 git 创建**（`provision` 走 `git.worktree add`）。那份
  `.git/worktrees/<slug>/` 元数据和工作树目录在容器里是 root 所有，`gem` 往里面 stage 仍会遇到
  同样的 `index.lock: Permission denied`。主会话（`kind=main`）不受影响；

终端本身也还有边界，应该明确：

1. 它是**行式终端**，不是 TTY：vim/htop、作业控制、窗口 resize 都不可用；
2. 一条命令的输出如果跨了多次轮询且错过了 `running` 状态，命令之间的分隔只能尽力而为；
3. 容器端点解析失败时，`Manager` 会记录 warning 并回退到宿主 PTY——如果终端看起来在本机，
   先看日志里的 `falling back to a host PTY`；
4. 关闭终端不会删除容器里的 shell 会话，容器停掉/删除时才一起消失。

## 为什么不是一个共享容器

沙箱粒度是 `(project, user)`，容器边界必须和它一致。一个共享容器等于把多租户隔离交给
进程权限和运气；一个租户把自己的容器跑挂，不会影响别人，这是这个设计唯一的意义。
