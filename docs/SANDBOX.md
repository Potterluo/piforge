# 沙箱：本地目录 与 Docker 容器

控制面**从不执行不可信代码**：它只决定"这个 (project, user) 的沙箱在哪里"，然后把
agent、浏览器、终端指过去。这条不变量在任何 provider 上都成立。

## 两个 provider

| | `local`（默认） | `docker` |
|---|---|---|
| 隔离边界 | 一个目录 | 一个容器 |
| 浏览器 | 宿主 Chrome，按沙箱复用 profile | 容器内 Chromium（`-sandbox-browser remote`） |
| 终端 | 宿主进程 + PTY | 容器内 shell 会话（`-sandbox-terminal remote`） |
| 适合 | 单机自托管、开发 | 多租户、需要真实边界 |
| 启用 | 默认 | `-sandbox docker -sandbox-image <image>` |

`PIFORGE_SANDBOX=docker`、`PIFORGE_SANDBOX_IMAGE`、`PIFORGE_SANDBOX_NETWORK`、
`PIFORGE_SANDBOX_CPUS`、`PIFORGE_SANDBOX_MEMORY` 是等价的环境变量。

## docker provider 现在做了什么

- `Ensure`：按 `(user, project)` 派生容器名 `piforge-<hash8>`，不存在就 `docker run -d`，
  存在就复用；`/workspace` 绑定到数据目录，暂停/重启不丢工作区；
- `Get`：`docker inspect` 的 `.State` 映射到 `READY / PAUSED / MISSING`；
- `Pause`：`docker stop`（停容器，不是删）；
- `Resume`：`docker start`；
- `Destroy`：`docker rm -f`，**不删数据卷**——删掉别人未提交的工作不该由"销毁沙箱"来代决；
- `Exec`：`docker exec`。

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

启用方式：

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

只有 `-sandbox docker` 下才有意义；不设时仍是宿主 PTY，行为不变。docker provider 会因此
发布容器的 8080 端口（`Endpoint` 从 `docker port` 读回实际端口），并把
`-sandbox-api-key` 作为 `SANDBOX_API_KEY` 传给容器。工作区的宿主路径由
`Docker.ContainerPath` 映射到容器里的 `/workspace`，shell 就从该 worktree 启动。

测试分两层：`internal/term/remote_test.go` 用 httptest 钉住“只追加新后缀”和“退格不发出去”；
`internal/term/remote_live_test.go` 用 `PIFORGE_AIO_URL` / `PIFORGE_AIO_KEY` 对着真容器跑，
没有环境变量时干净跳过。

## 还没做的（这一节是重点）

浏览器和终端已经能连容器，**但工作区的文件读写与 git 仍然走宿主目录**。单机部署下 bind
mount 让两边看到同一批文件；控制面部署在别处、只有 Docker daemon 可达时，这条还不成立。
下一步是把 `Workspace` 的读写切到容器的文件服务。

终端本身也还有边界，应该明确：

1. 它是**行式终端**，不是 TTY：vim/htop、作业控制、窗口 resize 都不可用；
2. 一条命令的输出如果跨了多次轮询且错过了 `running` 状态，命令之间的分隔只能尽力而为；
3. 容器端点解析失败时，`Manager` 会记录 warning 并回退到宿主 PTY——如果终端看起来在本机，
   先看日志里的 `falling back to a host PTY`；
4. 关闭终端不会删除容器里的 shell 会话，容器停掉/删除时才一起消失。

## 为什么不是一个共享容器

沙箱粒度是 `(project, user)`，容器边界必须和它一致。一个共享容器等于把多租户隔离交给
进程权限和运气；一个租户把自己的容器跑挂，不会影响别人，这是这个设计唯一的意义。
