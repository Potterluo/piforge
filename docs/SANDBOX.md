# 沙箱：本地目录 与 Docker 容器

控制面**从不执行不可信代码**：它只决定"这个 (project, user) 的沙箱在哪里"，然后把
agent、浏览器、终端指过去。这条不变量在任何 provider 上都成立。

## 两个 provider

| | `local`（默认） | `docker` |
|---|---|---|
| 隔离边界 | 一个目录 | 一个容器 |
| 浏览器 | 宿主 Chrome，按沙箱复用 profile | 仍在宿主（见下） |
| 终端 | 宿主进程 + PTY | 仍在宿主（见下） |
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

## 还没做的（这一节是重点）

**浏览器和终端仍然跑在宿主上。** 也就是说：换成 docker provider 之后，agent 改的文件
在容器里，而你看的页面、你敲的终端在宿主上——两边没有真正对齐。

这需要下一步：

1. 用 all-in-one 镜像（浏览器 + shell + 文件服务都在容器里），
2. 让 `browser.Manager` 连容器内 Chrome 的 CDP 端口（不是 `exec` 一个宿主 Chrome），
3. 让 `term.Manager` 通过容器里的 shell 服务或 `docker exec -it` 起 PTY，
4. 把 `Workspace` 的读写切到容器内的文件服务（现在是宿主目录的直接 IO）。

在此之前，docker provider 的价值是：**边界先立起来**，以及验证"容器生命周期能对上我们的
状态机"。这不是完成，是第一步。

## 为什么不是一个共享容器

沙箱粒度是 `(project, user)`，容器边界必须和它一致。一个共享容器等于把多租户隔离交给
进程权限和运气；一个租户把自己的容器跑挂，不会影响别人，这是这个设计唯一的意义。
