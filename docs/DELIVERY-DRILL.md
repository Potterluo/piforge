# PiForge 从零交付演练（Delivery Drill）

> 演练日期：2026-10-30（Asia/Shanghai）
> 演练脚本：[`scripts/deliver-check.ps1`](../scripts/deliver-check.ps1)
> 本次成功运行的证据目录：[`e2e/.artifacts/delivery-drill/20261030-172337/`](../e2e/.artifacts/delivery-drill/20261030-172337/)
> 演练镜像：`piforge:local`（`sha256:2cdba2cd65859dff62fc0c514e4a589d99be3c65e44e449dae0e2ec2cc63ecac`）
> 结论：**新操作员可以从零走到一次可用的 agent 回合，开出第一个 PR，并把一行评论变成 agent 的输入；无需手工配置服务商。** 本次 119 项断言全部通过，脚本 exit 0，总耗时 102.1 s，产品代码未改动。

---

## 0. 一句话结论

在一个**全新的空 volume** 上，只给端口、种子账号和 bootstrap 服务商环境变量启动
`piforge:local`，`/v1/setup` 即返回 `needs_provider:false`、`shared_provider_available:true`；
一个通过 API **新建的普通用户**可以登录、建项目（无 repo）、建会话，并让 agent
真实地写脚本、运行脚本、跑 `npm test`、用容器身份提交 commit。此后同一会话还能：
在一条新分支上提交、把项目指向一个**容器内的假 forge**、先推送再 `POST /git/pr`
开出 PR（重复调用返回 `created=false` 与同一个 URL，forge 的 422 原样上浮），
以及把 `notes.txt:2` 的行内评论变成 agent 的下一轮输入（`行内评论 · notes.txt:2`
+ 被引用的代码 + 评论正文 + 当前位置说明）。浏览器面板截图、文件树自动刷新、空闲
Chromium 回收也都成立。演练最后断言**源 checkout 仍是干净的**、Docker 里没有留下
任何 `pf-drill*` 容器或 volume。

---

## 1. 本次实测数字

下表全部来自本次成功运行，不是估算。原始值见
[`summary.json`](../e2e/.artifacts/delivery-drill/20261030-172337/summary.json) 的
`metrics` 与 `checks`，以及各命令输出文件。

| 指标 | 实测值 | 证据 |
|---|---:|---|
| `docker run` → `GET /healthz` 200 | **0.55 s** | `summary.json` → `healthz_seconds` |
| 发送 prompt → `run.settled` 事件时间戳 | **12.48 s** | `events.sse` 的 `run.settled.ts`；`summary.json` → `prompt_to_settle_seconds` |
| 发送 prompt → 轮询首次看到 `running:false` | **12.57 s** | `summary.json` → `prompt_to_first_idle_seconds` |
| 本次回合 token | 输入 **9,826**，输出 **1,032** | `events.sse` → `run.settled.payload` |
| 截图后 Chromium 进程数 | **10** | `summary.json` → `chromium_processes_active` |
| 空闲超时（60 s）后 Chromium 进程数 | **0**（wait 61.00 s） | `summary.json` → `chromium_processes_after_idle` / `chromium_idle_seconds` |
| 演练结束前容器内存 | **173.8 MiB / 15.43 GiB** | `docker-stats.txt` |
| 演练结束前容器 CPU / PIDs | **0.05 % / 51** | `docker-stats.txt` |
| 浏览器面板截图 | **35,042 bytes，1280×713，132 个采样颜色** | `browser-panel.png`、`browser-panel.json` |
| 测试面板记录 | **1 条**，命令 `npm test`，状态 `passed`，2/2 | `test-runs.json` |
| commit | `a2833c35d4e8530db6ecc116408c484837046fd0` | `commit.txt` |
| commit 身份 | `PiForge\|piforge@localhost\|Add primes script with tests` | `commit.txt` |
| 工作区文件 vs 沙箱文件 SHA-256 | 两边均为 `226a2684df2bf7cc71c0384bb0c1f2e3dd78bebf64988c62f629e7ae20bf6a42` | `summary.json` |
| 会话 transcript | 16 条 entry；5 条 assistant 回复；7 条 toolResult；7 个 toolCall | `entries.json`、`summary.json` |
| PR 分支 / 提交 | `drill/pull-request` / `3357b72aa6157fa4ba35dd982f117fc2eac71d8d` | `pr-create.json`、`pr-preview.json` |
| PR URL | `http://127.0.0.1:7799/acme/widgets/pull/7` | `pr-create.json` |
| 假 forge 收到的请求 | **9 条**；1 次成功创建；1 次被强制的 422 | `forge-requests.jsonl` |
| 行内评论这一轮 | **4.09 s** 内 started → settled | `summary.json` → `review_comment_turn_seconds` |
| 行内评论锚点 | `行内评论 · notes.txt:2` + `2 \| theme: dark` + 评论正文 + `位置：…第 2 行。` | `entries-comment.json`、`review-comment-message.txt` |
| 断言 | **119 PASS / 0 FAIL**，脚本 exit 0，总耗时 **102.1 s** | `summary.json`、`console.log` |

本次运行的标识：

- 新用户：`drill-20261030-172337@piforge.local`
- 项目：`prj_01M661YEEBYNMZP59722ND070N`
- 会话：`ses_01M661YEEFZMHKFSXPYCRMTKKX`
- 容器 / volume：`pf-drill` / `pf-drill-data`（已在 `finally` 中删除）
- 既有部署 `pf-local`：演练前后容器 ID 相同（`9f0de87b...`），`/healthz` 始终 200

---

## 2. 演练到底做了什么

### 2.1 前置约束

- 不碰现有 `pf-local` 容器和它的 volume。
- 只创建一个全新空 volume（`pf-drill-data`）和一个一次性容器（`pf-drill`）。
- 容器环境变量只给：端口映射、种子账号、`PIFORGE_DEFAULT_PROVIDER=volc`、
  `PIFORGE_BOOTSTRAP_PROVIDER=volc|<base_url>|deepseek-v4-flash`、
  `PIFORGE_BOOTSTRAP_PROVIDER_KEY=<key>`。
- 不手工进设置页添加服务商；所有验证走 API 和容器内命令。
- 服务商密钥只从 `PIFORGE_DRILL_KEY` 读取，脚本从不打印、不写进 artifact、不提交。
  本次运行前，密钥是用下面这段**不打印值**的代码从 `~/.dsh/.credentials.yaml`
  的 `VOLCE_API_KEY` 取出的：

```powershell
$cred = "$env:USERPROFILE\.dsh\.credentials.yaml"
$env:PIFORGE_DRILL_KEY = (
  [regex]::Match((Get-Content $cred -Raw), '(?m)^\s*VOLCE_API_KEY\s*:\s*(\S+)\s*$')
).Groups[1].Value
```

### 2.2 启动容器

脚本实际执行的等价命令（种子密码每次随机生成，这里用占位符）：

```powershell
docker volume create pf-drill-data

docker run -d --name pf-drill -p 7731:7700 -v pf-drill-data:/data `
  -v <artifact>/url-policy.json:/etc/piforge/url-policy.json:ro `
  -e PIFORGE_URL_ALLOW_FILE=/etc/piforge/url-policy.json `
  -e PIFORGE_SEED_EMAIL=admin@piforge.local `
  -e PIFORGE_SEED_PASSWORD=<seed-admin-password> `
  -e PIFORGE_SEED_USER_EMAIL=dev@piforge.local `
  -e PIFORGE_SEED_USER_PASSWORD=<seed-user-password> `
  -e PIFORGE_DEFAULT_PROVIDER=volc `
  -e 'PIFORGE_BOOTSTRAP_PROVIDER=volc|https://api.example.com/v1|deepseek-v4-flash' `
  -e "PIFORGE_BOOTSTRAP_PROVIDER_KEY=$env:PIFORGE_DRILL_KEY" `
  piforge:local
```

启动后轮询：

```powershell
curl.exe -fsS http://127.0.0.1:7731/healthz
# 0.55 s 后返回 200（脚本计时，见 summary.json）
```

### 2.2.1 出站 URL 策略：演练用一条 allow 条目，默认仍然拒绝

演练的假 forge 是容器里的 loopback 地址（`http://127.0.0.1:7799`），而出站 URL 策略
（`docs/SANDBOX-DEPLOY.md` §1.9）默认拒绝 loopback。所以演练在启动容器前写一个**只有一条**的
allow 文件：

```json
{"allow":[{"host":"127.0.0.1","ports":[7799]}]}
```

这不是绕过，而是任何一个用内网 forge 的 operator 都必须做的事；容器启动日志里因此出现
`policy="public http(s) only + 1 allow entries"`。**除这一条之外，演练全程跑在默认策略下**，
并且同一个容器上还会跑 5 条检查，专门盯住"默认拒绝"这件事：

| 检查 | 期望 |
|---|---|
| `POST /v1/mcp/servers {"url":"http://127.0.0.1:7700/healthz"}`（同一地址、**不同端口**） | 400，`details.reason=loopback` |
| `POST /v1/mcp/servers {"url":"http://host.docker.internal:7700/healthz"}` | 400，`details.reason=host-name` |
| `POST /v1/mcp/servers {"url":"http://169.254.169.254/latest/meta-data/"}` | 400，`details.reason=link-local` |
| `POST /v1/mcp/servers {"url":"http://127.0.0.1:7799/mcp"}`（allow 文件点名的那个） | 201 |
| `POST /v1/skills/import {"url":"file:///tmp/drill-clone-cap.git"}`（20 MiB 仓库） | 400，消息含 `16.0 MiB` 与实测字节数 |

第四条是前面三条的对照：同一个端点、同样的形状，唯一的差别是 allow 文件里有没有它。谁把默认策略
改松了，前三条（以及 clone 上限那条）会立刻失败。

**不要照抄这个 allow 文件当默认配置**：`deploy/url-policy.json` 出厂是空的。

### 2.3 证明 bootstrap 生效（不手工配置）

```powershell
# 管理员登录，拿 cookie + csrf
curl.exe -sS -c cookies.txt -H 'Content-Type: application/json' `
  -d '{"email":"admin@piforge.local","password":"<seed-admin-password>"}' `
  http://127.0.0.1:7731/v1/auth/login

# 认证后的 first-run 状态
curl.exe -sS -b cookies.txt http://127.0.0.1:7731/v1/setup
```

返回（节选，完整见 `setup-admin.json`）：

```json
{
  "needs_provider": false,
  "is_admin": true,
  "shared_provider_available": true,
  "bootstrap": {
    "name": "volc",
    "base_url": "https://api.example.com/v1",
    "model": "deepseek-v4-flash"
  }
}
```

容器日志（完整、已脱敏，见 `container.log`）：

```text
time=2026-10-29T23:31:53.695Z level=INFO msg="bootstrap: created the shared model provider"
  provider=volc base_url=https://api.example.com/v1
  model=deepseek-v4-flash has_key=true
```

脚本对原始日志做的断言：

- `bootstrap: created the shared model provider` 存在；
- 同一行 `has_key=true`；
- 服务商密钥字符串在日志中出现 **0 次**（`occurrences=0`）。

### 2.4 新建用户 → 项目 → 会话

```powershell
# 管理员创建普通用户（X-PF-CSRF 来自登录响应或 pf_csrf cookie）
curl.exe -sS -b cookies.txt -H "X-PF-CSRF: $csrf" -H 'Content-Type: application/json' `
  -d '{"email":"drill-20261030-172337@piforge.local","display_name":"Delivery Drill","password":"<drill-user-password>","role":"user"}' `
  http://127.0.0.1:7731/v1/admin/users

# 新用户登录后：项目不关联 repo
curl.exe -sS -b new-cookies.txt -H "X-PF-CSRF: $newCsrf" -H 'Content-Type: application/json' `
  -d '{"name":"drill-20261030-172337","repo_url":"","default_branch":"main"}' `
  http://127.0.0.1:7731/v1/projects

# 在项目下建会话
curl.exe -sS -b new-cookies.txt -H "X-PF-CSRF: $newCsrf" -H 'Content-Type: application/json' `
  -d '{"title":"delivery drill"}' `
  http://127.0.0.1:7731/v1/projects/prj_01M661YEEBYNMZP59722ND070N/sessions
```

脚本确认新用户的 `/v1/setup` 也是 `needs_provider:false, shared_provider_available:true,
is_admin:false`（见 `setup-user.json`），说明共享服务商确实被继承，而不是管理员独有。

### 2.5 一次真实 agent 回合

发送的 prompt（原样，注意其中明确要求了文件名和 `npm test`，便于客观断言）：

```text
Create a small Node script named primes.js that prints the first 10 primes, one per line.
Run it with node and show the output. Add a simple test: a package.json whose test script
is "node --test" and a node:test file that checks the primes, then run `npm test`.
Finally commit everything with git.
```

```powershell
curl.exe -sS -b new-cookies.txt -H "X-PF-CSRF: $newCsrf" -H 'Content-Type: application/json' `
  -d '{"client_msg_id":"drill-20261030-172337","text":"...上面那段..."}' `
  http://127.0.0.1:7731/v1/sessions/ses_01M661YEEFZMHKFSXPYCRMTKKX/messages
```

返回 `accepted:true, disposition:"started"`，随后脚本轮询
`GET /v1/sessions/{id}/status`，直到 `running:false` 且 `last_seq` 连续 2 s 不变。
为了拿到真正的完成信号，脚本在结束后用有界 `curl` 回放 SSE：

```powershell
curl.exe -sN --max-time 10 -b "pf_session=<cookie>" `
  "http://127.0.0.1:7731/v1/sessions/ses_01M661YEEFZMHKFSXPYCRMTKKX/events?after_seq=0" `
  -o events.sse
```

`events.sse` 中同时存在 `event: run.started` 和 `event: run.settled`；本次
`run.started.ts=2026-10-30T09:23:39.358061056Z`、
`run.settled.ts=2026-10-30T09:23:51.586786328Z`，相对发送 prompt 的时间为
**12.48 s**。用产品自己的完成信号（`run.settled`，即 `agent_settled` 的规范化事件）
计时，而不是用轮询到的第一个 `running:false` 估算。

### 2.6 验 transcript、文件、commit、测试面板

```powershell
# transcript（16 条：user 1、assistant 5、toolResult 7、toolCall 7）
curl.exe -sS -b new-cookies.txt http://127.0.0.1:7731/v1/sessions/<id>/entries

# 文件树
curl.exe -sS -b new-cookies.txt "http://127.0.0.1:7731/v1/sessions/<id>/files/tree?path=&depth=3"

# 测试面板
curl.exe -sS -b new-cookies.txt "http://127.0.0.1:7731/v1/sessions/<id>/test-runs?limit=20"
```

容器内验证（这是“commit 确实在容器里、作者确实是容器身份”的直接证据）：

```powershell
docker exec pf-drill sh -c "cd /data/sandboxes/<user>/<project>/repo && git log -1 --format='%an|%ae|%s'"
# PiForge|piforge@localhost|Add primes script with tests

docker exec pf-drill sh -c "cd /data/sandboxes/<user>/<project>/repo && git status --porcelain"
# 空输出（clean）

docker exec pf-drill sh -c "cd /data/sandboxes/<user>/<project>/repo && sha256sum primes.js"
# 226a2684df2bf7cc71c0384bb0c1f2e3dd78bebf64988c62f629e7ae20bf6a42  primes.js
```

同一文件通过 API 下载（`/files?path=primes.js&download=1`）后算出的 SHA-256
与容器内 `sha256sum` **完全一致**。commit 内容是：

```text
package.json
primes.js
primes.test.js
```

这次 agent 把测试文件放在仓库根目录，而不是 `test/` 子目录——脚本按扩展名
（`*.test.js`）断言，不假定目录，所以两种布局都算通过。

测试面板这次把同一份 TAP 解析成了通过（`test-runs.json`）：

```json
{"runner":"npm test","status":"passed","total":2,"passed":2,"failed":0,"skipped":0,"duration_ms":35}
```

早期演练里同一份输出是 `unrecognized`，见 §6.2 与 §6.10 的修复记录。

### 2.7 浏览器面板截图

```powershell
# 打开容器内自己的真实页面（Next 在 127.0.0.1:3100 监听）
curl.exe -sS -b new-cookies.txt -H "X-PF-CSRF: $newCsrf" -H 'Content-Type: application/json' `
  -d '{"url":"http://127.0.0.1:3100/login"}' `
  http://127.0.0.1:7731/v1/sessions/<id>/browser/tabs

# 截图（写入数据目录的 artifacts/<session>/screenshots/，不是 checkout）
curl.exe -sS -b new-cookies.txt -X POST -H "X-PF-CSRF: $newCsrf" `
  http://127.0.0.1:7731/v1/sessions/<id>/browser/screenshot
# {"path":"artifacts/ses_.../screenshots/20261030-092408-559.png","bytes":35042}
```

脚本用 `GET /v1/sessions/{id}/artifacts?path=screenshots/...` 把 PNG 下载回来，
解码后统计采样颜色：**1280×713，132 种颜色**，不是空白页。截图内容为 PiForge
登录页（带种子账号的预置提示），见 `browser-panel.png`。

截图落在数据目录而不是工作区，所以截完图后容器内的 `git status` 仍是 clean——
脚本专门断言了这一点（`the checkout is still clean after the capture`），
早期版本会把 `screenshots/` 留在 checkout 里，见 §6.5/§6.10。

说明：这里调用的是浏览器面板“相机”按钮背后的同一个
`POST /v1/sessions/{id}/browser/screenshot` 接口；本次没有用 Playwright 去点那个按钮。
按钮本身的 UI 路径由 `e2e/tests/20-screenshot.spec.ts` 覆盖，该 spec 在本轮完整
e2e 套件中执行（见 §7）。

### 2.8 文件树“无需手动刷新”

这一项不是 API 能证明的，脚本用 Playwright 打开演练容器的真实会话页：

1. 用新用户登录；
2. 打开会话页，在页面上打一个 `window.__drillNoReload` 标记；
3. 打开“文件”面板，确认 agent 的 `primes.js` 已经出现在树里；
4. 打开“终端”面板，输入 `echo ui-live > ui-live.txt`；
5. 切回“文件”面板，不点刷新、不 reload，等待 `ui-live.txt` 出现；
6. 再回终端 `rm -f ui-live.txt`，并把页面截图存为 `ui-file-tree.png`。

结果（`ui-check.json`）：

```json
{"ok": true, "appeared": true, "noReload": true, "primesVisible": true}
```

### 2.9 空闲 Chromium 与内存

```powershell
docker exec pf-drill sh -c "pgrep -c chromium || true"
# 截图后：10
# 等待 61 s（产品默认 idle timeout 60 s）后：0

docker stats --no-stream --format "{{.Name}}|{{.MemUsage}}|{{.CPUPerc}}|{{.PIDs}}" pf-drill
# pf-drill|173.8MiB / 15.43GiB|0.05%|51
```

### 2.10 拆除、不干扰与回归守卫

```powershell
docker rm -f pf-drill
docker volume rm pf-drill-data

docker ps -a --format "{{.Names}}" | Select-String '^pf-drill$'   # 无输出
docker volume ls --format "{{.Name}}" | Select-String '^pf-drill-data$' # 无输出

docker inspect pf-local --format '{{.Id}}|{{.State.Running}}'
curl.exe -fsS http://127.0.0.1:7722/healthz   # 200
```

`pf-local` 在演练前后容器 ID 相同（`9f0de87b...`），`/healthz` 始终 200。
它本身没有 Docker healthcheck（`docker inspect` 没有 `.State.Health`），所以脚本用
**容器 running + 端口 `/healthz` 200** 作为“仍然健康”的证据。

拆除之后，`finally` 里还会做三件守卫（§2.13）：源 checkout `git status` 为空、
Docker 里没有 `pf-drill*` 容器、没有 `pf-drill*` volume。这三项和演练本身的断言
一样是 PASS/FAIL，不是提示。

### 2.11 开 PR：先推送，再创建（FR-12.6）

这一段用一个**跑在演练容器内部**的假 forge（`/tmp/drill-forge/server.js`，Node 标准库，
无依赖）来证明「开 PR」这条链路。放在容器内有两个好处：`127.0.0.1` 就是控制面的
loopback（不需要 host.docker.internal、防火墙规则或额外宿主进程），容器删除时它
和服务端日志一起消失。假 forge 只实现产品会打的两个调用：

- `GET /repos/{owner}/{repo}/pulls`（按 `head`/`base` 过滤已存在的 PR）；
- `POST /repos/{owner}/{repo}/pulls`（创建，或按控制文件强制返回 422）。

每个请求（方法、路径、query、`Authorization`、请求体、响应码）都追加到
`/tmp/drill-forge/requests.jsonl`；脚本把它复制成 `forge-requests.jsonl`
（复制前把 token 替换成 `<redacted>`）。提示：token 只在 `Authorization: Bearer`
头里出现，不在 URL/query 里。

演练按 UI 的路径准备内容，不直接改 git 配置：

```powershell
# 1) 一条新分支 + 一个提交，全部走产品 API（分支选择器 / 编辑器 / 提交框背后的接口）
POST /v1/sessions/<id>/git/branch     {"name":"drill/pull-request"}
POST /v1/sessions/<id>/git/checkout   {"name":"drill/pull-request"}
PUT  /v1/sessions/<id>/files?path=pr-drill.txt     # 原始 text/plain body
POST /v1/sessions/<id>/git/commit     {"message":"drill: a file for the pull request","paths":["pr-drill.txt"]}

# 2) 项目的 forge 配置（token 只写不回显）
PUT /v1/projects/<id>/forge
  {"kind":"github","api_base":"http://127.0.0.1:7799","repo":"acme/widgets",
   "base_branch":"main","token":"<per-run fake token>"}

# 3) 预览（不写任何东西）→ 创建（先 push 再 POST）
GET  /v1/sessions/<id>/git/pr
POST /v1/sessions/<id>/git/pr  {"title":"Drill: open a pull request","body":"..."}
```

`git push` 的远端是容器内的一个空 bare 仓库（`/data/drill-forge-origin.git`），
脚本用 `docker exec` 把它加成 `origin`——项目是通过 API 建的无 repo 项目，
`repo_url` 只接受 https/git@，所以这是唯一不改产品代码又能真实推送的办法。
推送本身仍是产品做的 `git push -u origin <branch>`。

断言（全部来自 `pr-create.json` / `forge-requests.jsonl`，不是产品自说自话）：

| 断言 | 本次值 |
|---|---|
| 状态码 201、`created=true` | `status=201 created=True` |
| 响应带 PR URL | `http://127.0.0.1:7799/acme/widgets/pull/7` |
| `pushed.branch` 是会话分支、`pushed.commit` 是 40 位 SHA | `drill/pull-request` / `3357b72…` |
| 第二次调用：200、`created=false`、同一 URL、`existing=true` | 见 `pr-create-again.json` |
| forge 只收到 **1 次**成功 create | `creates=1` |
| create 的 `Authorization` 是 `Bearer <token>` | `Bearer <redacted>` |
| create 的 title/head/base 精确匹配 | `Drill: open a pull request` / `acme:drill/pull-request` / `main` |
| token 不在 URL/query | `occurrences=0` |
| 强制 422：HTTP 422、`code=forge_error`、`details.step=create`、`forge_status=422` | 见 `pr-rejected.json` |
| 422 的正文**原样**包含 forge 的话 | `…HTTP 422：drill forge refused: base branch is protected (drill-422)` |
| 被拒分支只被 create 一次，且第二次成功调用没有变成第二个 PR | `refused=1 successfulCreates=1` |
| forge token 在容器日志里 0 次 | `occurrences=0` |

422 的分支不能复用已成功的分支：产品的幂等逻辑会先 `FindOpen` 找到已有 PR，
直接返回 `created=false`，不会把 422 暴露出来。因此强制 422 用的是第二条分支
（`drill/rejected-base`），并让假 forge 的 `GET` 对它返回空列表。

### 2.12 行内评论 → agent 的下一轮输入（FR-12.3）

```powershell
# 1) 用编辑器的同一个接口写一个文件（内容三行，评论锚在第 2 行）
PUT /v1/sessions/<id>/files?path=notes.txt
# 2) 用 composer 的同一个 POST 发评论（服务端拼消息，浏览器只发锚点）
POST /v1/sessions/<id>/messages
{
  "client_msg_id": "drill-comment-<run>",
  "review_comment": {
    "path": "notes.txt", "line": 2, "side": "new", "hunk": "@@ -1,3 +1,3 @@",
    "quote": ["theme: dark"], "body": "这一行是演练用的行内评论：请只回复你看到了它，不要修改任何文件。"
  }
}
```

脚本等这一轮 `run.settled`（本次 4.09 s），再从 `GET /v1/sessions/{id}/entries`
（**agent 自己的 transcript**，不是浏览器以为发了什么）里断言：

```text
行内评论 · notes.txt:2
diff 片段：@@ -1,3 +1,3 @@
引用的代码：
   2 | theme: dark
评论：这一行是演练用的行内评论：请只回复你看到了它，不要修改任何文件。
位置：这段代码现在仍在第 2 行。
```

`entries-comment.json` 是原始 transcript；`review-comment-message.txt` 是那条
user turn 的完整 preview。最后脚本用产品的 `POST /git/discard` 撤销这一轮可能
留下的改动（本次删掉了未跟踪的 `notes.txt`），并断言 checkout 回到 clean。

### 2.13 回归守卫：checkout 与 Docker 数据

演练最后（`finally`，拆除之后）记录三项，任何一项失败都会让脚本 exit 1：

1. `git -C <repo> status --porcelain` 为空——截图、端口、假 forge、日志都不能把
   **源 checkout** 弄脏（证据：`source-git-status.txt`）。
2. `docker ps -a` 里没有 `pf-drill*` 容器（证据：`leftover-containers.txt`）。
3. `docker volume ls` 里没有 `pf-drill*` volume（证据：`leftover-volumes.txt`）。

这就是「演练绿了、但把证据留在工作区里」这一类回归的拦截点：本轮里
`e2e/.artifacts/` 已被 `.gitignore` 忽略、截图已移入数据目录，所以守卫是通过的；
如果以后有人把截图、临时仓库或日志写回 checkout，这一项会红。

---

## 3. 证据文件（都在 `e2e/.artifacts/delivery-drill/20261030-172337/`）

| 文件 | 证明了什么 |
|---|---|
| `summary.json` | 119 项检查的 PASS/FAIL、所有 metrics、运行起止时间 |
| `console.log` | 本次运行的完整控制台输出（119 项 PASS，exit 0） |
| `container.log` | bootstrap 创建了共享服务商、`has_key=true`；种子密码已脱敏；密钥 0 次出现 |
| `container-after-forge.log` | 开 PR 之后的完整容器日志（同样脱敏）；forge token 0 次 |
| `setup-admin.json` / `setup-user.json` | 管理员和新用户的 first-run 状态；新用户继承共享服务商 |
| `context.json` | 用户、项目、会话、worktree ID |
| `events.sse` | `run.started` / `run.settled` 原始帧，settle 时间戳（274,795 bytes） |
| `entries.json` | 16 条 transcript：user、assistant、toolResult、toolCall |
| `entries-comment.json` | 行内评论那一轮的 transcript：`行内评论 · notes.txt:2` + 引用 + 评论 |
| `review-comment-message.txt` | 该 user turn 的完整消息文本（含 `位置：…`） |
| `files-tree.json` | 文件树包含 `primes.js`、`package.json`、`primes.test.js` |
| `primes.js` | 从 API 下载的工作区副本，用于和沙箱文件比对 SHA-256 |
| `commit.txt` | `git log -1`、HEAD、`git status --porcelain`、commit 文件列表 |
| `test-runs.json` | 测试面板的唯一一条 run（`npm test`，`passed`，2/2） |
| `fake-forge.js` | 容器内假 forge 的完整源码（本次实际运行的那一份） |
| `forge-control-initial.json` / `forge-control-422.json` | 假 forge 的控制文件（正常 / 强制 422） |
| `forge-config.json` | `PUT /v1/projects/{id}/forge` 的响应（`has_token=true`，无 token 明文） |
| `pr-preview.json` | PR 预览：分支、基准、提交数、三点 diff 的文件、标题草稿 |
| `pr-create.json` / `pr-create-again.json` | 首次创建（201/created）与重复调用（200/existing） |
| `pr-rejected.json` | 强制 422：`step=create` 与 forge 原文 |
| `forge-requests.jsonl` | 假 forge 收到的每个请求（token 已替换为 `<redacted>`） |
| `browser-tab.json` | 打开的真实页面 URL 和 tab id |
| `browser-panel.png` | 浏览器面板截图原图（登录页） |
| `browser-panel.json` | 截图的尺寸、字节数、采样颜色数 |
| `ui-file-tree.png` | 真实会话页截图：`ui-live.txt` 出现在文件树中 |
| `ui-check.json` / `ui-check.log` | Playwright 断言：appeared/noReload/primesVisible |
| `git-status-after-comment.json` | 行内评论轮之后、discard 之后的沙箱 checkout 状态 |
| `docker-stats.txt` | 演练结束前的内存、CPU、PIDs |
| `source-git-status.txt` | 源 checkout 的 `git status --porcelain`（空 = clean） |
| `leftover-containers.txt` / `leftover-volumes.txt` | 拆除后的 Docker 残留扫描（空 = 无残留） |

门禁日志与本次运行放在同一层：
`e2e/.artifacts/delivery-drill/gate-go.log`、`gate-web-test.log`、`gate-next-build.log`、
`gate-e2e.log`。

注意：`e2e/.artifacts/` 被 `.gitignore` 忽略（第 31 行）。因此**提交的是可复跑的脚本和
本报告，原始证据保留在本机该目录**；需要留档时请把整个 `20261030-172337/` 目录另行归档。
`20261030-073102/` 是首轮的失败尝试，原因见 §6.1；`20261030-073152/` 是首轮的绿色
运行，保留作对照。

---

## 4. 如何复跑

```powershell
# 1. 取密钥（不打印）
$cred = "$env:USERPROFILE\.dsh\.credentials.yaml"
$env:PIFORGE_DRILL_KEY = (
  [regex]::Match((Get-Content $cred -Raw), '(?m)^\s*VOLCE_API_KEY\s*:\s*(\S+)\s*$')
).Groups[1].Value

# 2. 跑
cd C:\Users\<user>\Downloads\piforge
pwsh -NoProfile -File .\scripts\deliver-check.ps1
```

常用参数：

```powershell
.\scripts\deliver-check.ps1 `
  -Image piforge:local `
  -Port 7731 `
  -ProviderName volc `
  -ProviderBaseURL 'https://api.example.com/v1' `
  -ProviderModel deepseek-v4-flash `
  -FakeForgePort 7799 `
  -BrowserIdleWaitSeconds 75
```

行为约定：

- `PIFORGE_DRILL_KEY` 缺失、镜像不存在、端口被占、`pf-drill*` 名称已存在、或
  `-ContainerName`/`-VolumeName` 指向 `pf-local` 时，**在创建任何东西之前失败**。
- 只删除本次创建的容器和 volume；`finally` 里执行，断言失败也会清理。假 forge 是容器内
  的一个 `node` 进程，随容器一起消失，不需要单独的清理逻辑。
- 任一断言失败 → exit 1；全部通过 → exit 0。
- 控制台打印 Summary 表和 Measured 表；完整结果在 `summary.json`。
- `-SkipUi` 跳过 Playwright 文件树检查（没有 `e2e/node_modules` 或 Chrome 时使用）。
- 需要 **PowerShell 7**：脚本用 `-SkipHttpErrorCheck` 读 4xx/5xx 响应体（强制 422 的
  forge 原文就是从这里来的）。Windows PowerShell 5.1 没有这个参数。
- `-FakeForgePort` 只在容器内部监听（不发布到宿主），换端口不影响 `pf-local`；
  容器内该端口被占用时脚本会在“fake forge is listening”这一项失败并给出原因。
- 本次总耗时 **102.1 s**（首轮绿色运行 96 s，最近一次 75 项运行 105 s；差异主要来自
  模型回合本身的 10–20 s）。新增两项检查大约多花 **6–8 s**：PR 链路是本地 git push +
  容器内 HTTP，行内评论那一轮实测 4.09 s。

---

## 5. 新操作员仍需自己做的事（脚本不覆盖）

1. **准备镜像。** `piforge:local` 必须已经存在。**规范命令是脚本**（默认 `-Tag piforge:local`，
   约 90 s，把控制面 + web 叠到已发布的基础镜像上，Chromium 在基础镜像里）：

   ```powershell
   pwsh scripts/build-app-image.ps1
   ```

   底层才是仓库根目录的完整构建（从源码装 node/pi/chromium，约 20 分钟；只有在基础镜像
   不可用时才需要它，且必须显式开 Chromium）：

   ```powershell
   docker build --build-arg INSTALL_CHROMIUM=true -t piforge:local .
   ```

   没有 Chromium 时，服务本身可用，但浏览器面板会报 “unavailable”，本演练的截图断言会失败。
   `docs/SANDBOX-DEPLOY.md` §1.4.1 与 `deploy/README.md` 指的是同一个脚本，只是 `-Tag` 不同。

2. **选择服务商和模型。** 脚本把 `volc`、base URL、`deepseek-v4-flash` 作为参数默认值；
   换部署时要确认该 endpoint 真的提供这个 model id，并确认容器能直连（需要代理时设置
   `PIFORGE_HTTP_PROXY` / `HTTPS_PROXY`；浏览器面板还需要 `PIFORGE_BROWSER_PROXY`）。

3. **决定密钥的生命周期。** 演练把 key 放进容器环境变量；`docker inspect` 能看到它
   （日志里没有，但环境里有）。生产应使用 Docker secret / 编排 secret，并在首次启动后
   移除 `PIFORGE_BOOTSTRAP_PROVIDER*`；产品只在“没有可用共享服务商”时创建，重启不会覆盖，
   但环境变量仍会留在容器配置里。

4. **持久化 master key。** 演练用全新 volume，产品会在 `/data/master.key` 自动生成；
   生产必须显式设置并备份 `PIFORGE_MASTER_KEY`，否则 volume 丢失 = 所有服务商密钥不可解。

5. **种子账号与权限。** 脚本用随机密码创建 admin/dev，演练结束即随容器删除；生产要换掉
   默认邮箱/密码，规划管理员数量、禁用/轮换策略。脚本通过 admin API 建普通用户，
   真实 onboarding（邀请、SSO、角色）不在覆盖范围。

6. **TLS 与来源。** 生产要终止 TLS、开 `-secure-cookies`，并设置
   `PIFORGE_ALLOWED_ORIGINS`，否则终端/浏览器 WebSocket 在反向代理后会拒绝连接。

7. **生产形态。** 演练是“一次性容器 + 一次性 volume”；生产需要 restart policy、
   volume 备份、日志采集、磁盘/内存限额、监控（`/healthz`、`/readyz`、`/v1/admin/status`）。

8. **隔离级别。** 演练镜像默认 `PIFORGE_ISOLATION=none`，agent/终端/浏览器都在同一个
   容器里；需要更强隔离时按 `docs/SANDBOX*.md` 配置 bubblewrap/firejail 或远端 runner。

9. **UI 检查依赖。** Playwright 检查需要 `e2e/node_modules` 和本机 Chrome/Chromium；
   CI 上没有时用 `-SkipUi`，但这会少一项“文件树无需刷新”的证据。

10. **成本与配额。** 脚本会真实调用模型**两次**：主回合（本次输入 9,826 / 输出 1,032
    tokens）和行内评论轮（短回复）。重跑就是再花一次。CI 里应使用专用 key 和限额。

---

## 6. 失败与易碎点（历史观察与修复记录）

> §6.1–§6.9 是**首轮**（2026-10-30 `073152`）的真实观察，保留作追溯；§6.10 记录了
> 随后对这些点的修复。本轮扩展（PR / 行内评论 / 回归守卫）没有改动产品代码。

### 6.1 脚本自身的第一次失败：CRLF 比较

第一次运行（`20261030-073102`）在产品层全部通过，但在脚本断言
“commit 包含 primes.js”时失败：`docker exec ... git show --name-only` 在 Windows
上返回 CRLF，PowerShell 的 `-match '(?m)^primes\.js$'` 因行尾 `\r` 不匹配。
这是**脚本 bug，不是产品问题**；已改为按行 `Trim()` 后比较，第二次运行通过。
保留失败目录是为了让这次修正可追溯。

### 6.2 测试面板把 Node TAP 标成 `unrecognized`

`test-runs.json` 里 `npm test` 被正确识别为一次 run，但 `status:"unrecognized"`，
`total/passed/failed` 都是 0，`output_tail` 里其实写着 `# tests 2` / `# pass 2`。
原因：`internal/testrun` 的 JS 解析器只认 jest/vitest 风格摘要，不认 Node 内置
`node --test` 的 TAP。面板仍会显示一条“未识别格式”的 run（满足“有测试运行”），
但用户看不到 2 passed。这是展示精度问题，不是交付阻塞，本次**没有改产品代码**。
**现状**：已在 §6.10 修掉；本轮实测 `status=passed, total=2, passed=2`。

### 6.3 直接运行 `node --test` 不会被记录

`internal/testrun.Detect` 的识别列表里没有 `node`；只有 `npm test` / `pnpm test` /
jest / vitest 等会建 run。演练因此在 prompt 里要求 agent 用 `npm test` 包一层。
如果真实用户只让 agent 跑 `node --test`，测试面板会是空的——这是产品能力缺口，
但不在本次“从零交付”阻塞范围内。

### 6.4 种子账号只写邮箱，不打印密码（首轮观察已修，见 §6.10）

首轮（2026-10-30）的日志里出现过 admin/dev 的初始密码。**这一条已经修掉**：现在
`logSeededAccounts` 只写 `created the first admin account email=…` /
`created the demo account email=…`，密码来自 `PIFORGE_SEED_PASSWORD` /
`PIFORGE_SEED_USER_PASSWORD`（留空则生成随机密码，同样不写日志）。断言在
`cmd/piforged/seed_log_test.go`：日志含邮箱、不含两个密码值。
所以 `docker logs` 可以直接贴进工单，不会泄露密码；生产仍应在首次登录后立即改密，
因为默认值写在 compose/`docker run` 的环境里（`docker inspect` 可见）。

### 6.5 截图会弄脏 checkout

`POST /browser/screenshot` 把 PNG 写进工作区的 `screenshots/`（这是产品设计，
方便 agent 读取截图）。演练在 commit 校验之后才截图，所以 commit 时是 clean 的；
但真实用户提交后再截图，`git status` 会多出 `screenshots/` 未跟踪目录。
如果团队要求 clean tree，需要把 `screenshots/` 加进 `.git/info/exclude` 或
在截图后自行清理——脚本没有替产品做这个决定。
**现状**：已在 §6.10 修掉，截图写到数据目录；本轮在截图后直接断言 sandbox
`git status` 仍 clean，并且 `finally` 的回归守卫再查一次源 checkout。

### 6.6 健康检查的语义

`/healthz` 0.55 s 就返回 200，但它是 Go 控制平面的存活探针，不代表 Next 页面已可访问
（本次 Next 在 76 ms 内 ready，所以没问题）。脚本在截图前固定等 3 s；在更慢的机器上
应依赖页面加载/重试，而不是这个固定等待。

### 6.7 浏览器空闲回收的边界

本次 60 s 超时后 61 s 归零；如果宿主机负载高，Chromium 退出可能更慢。脚本最多等到
`BrowserIdleWaitSeconds + 60` 秒，超时即失败。这是有意的严格断言，但在慢 CI 上可能
需要调大 `-BrowserIdleWaitSeconds`。

### 6.8 `pf-local` 没有 Docker healthcheck

`docker inspect pf-local` 没有 `.State.Health`，所以不能写
`{{.State.Health.Status}}`。脚本改成“容器 running + `http://127.0.0.1:7722/healthz` 200”，
并额外比对演练前后的容器 ID。

### 6.9 Go 测试套件的一个时序 flake

本次在**同时**跑 Web 测试时执行 `go test ./... -count=1`，
`internal/pi` 的 `TestRuntimeRunEventOrder` 失败：

```text
status after settle = {... Running:false Streaming:true ...}
```

单独重跑 `go test ./internal/pi -run TestRuntimeRunEventOrder -count=1` 通过；
随后单独重跑完整 Go 套件也通过（见 §7）。这是 `run.settled` 与内部 status 字段清零
之间的时序竞争，属于测试/实现的 flake，不是本次演练改动引入的（本次没有改产品代码）。
它不影响演练结论，但说明这套 Go 测试对并发负载敏感，CI 上不宜与重负载任务并行跑。

### 6.10 修复后（本轮）

§6.2–§6.5 已作为三个易碎点修掉，原观察保留在上面作为演练记录。注意：**那次修复动了
产品代码**；本轮（PR / 行内评论 / 回归守卫）没有再改产品代码。

- **Node TAP（§6.2/§6.3）**：`internal/testrun` 新增 `RunnerNode`（`node --test`）/ `RunnerTSX`
  （`tsx --test`）识别与 `parse_tap.go`。它读 `# pass` / `# fail` / `# skipped` / `# tests`
  / `# duration_ms`，也读 TAP v13 的 `1..N` plan 与顶层 `ok` / `not ok`；失败名后接
  `location: '…:line:col'` 时给出可点击的 `file:line`。只有 `# pass`/`# fail` 时，
  total/skipped 保持未知（0），不会用 pass+fail 编一个总数；完全不成形则仍是
  `unrecognized` 且不发明计数。测试：`parse_test.go` 的表驱动 fixture（full / failing+location
  / plan-only / counts-only / malformed / tsx）与 `TestParseRealNodeTestRun`（真的跑
  `node --test`）；部署实测见 `e2e/.artifacts/fix-drill/`。
- **种子密码（§6.4）**：`cmd/piforged` 的 `logSeededAccounts` 只写
  `created the first admin account email=…` / `created the demo account email=…`，
  不再打印 `admin: email / password`。密码由 `PIFORGE_SEED_PASSWORD` /
  `PIFORGE_SEED_USER_PASSWORD` 决定（见 `docs/SANDBOX-DEPLOY.md`「种子密码从哪里来」）。
  测试：`cmd/piforged/seed_log_test.go` 断言日志含邮箱、不含两个密码。
- **截图（§6.5）**：截图改写到 `<dataDir>/artifacts/<session>/screenshots/`，客户端拿到虚拟路径
  `artifacts/<session>/screenshots/…`，经新路由 `GET /v1/sessions/{id}/artifacts?path=…`
  预览/下载；checkout 里不再出现 `screenshots/`。旧会话里已经在 checkout 内的截图仍走
  `/files` 预览。测试：`httpapi/artifacts_test.go`、`e2e/tests/20-screenshot.spec.ts`
  （两次抓图后 `git/status.clean=true`，相机按钮的预览用 artifacts 路由）。
- **仍未覆盖**：远端 / AIO 沙箱浏览器的空闲停止（idle 策略只在 host placement 生效），
  见 `docs/STATUS.md`「交付能力」的边界说明。

---

## 7. 门禁与变更声明

**产品代码：未改动。** 本轮只改了两个文件：

- `scripts/deliver-check.ps1`（新增 PR、行内评论、回归守卫；既有 75 项断言原样保留）
- `docs/DELIVERY-DRILL.md`（本报告）

断言从 **75 增至 119**（+44）：PR 路径 31 项（假 forge 安装/监听、远端、分支、提交、
forge 配置、预览、创建、幂等、wire 断言、强制 422、token 不入日志）、行内评论 9 项
（文件、发送、settle、transcript 锚点/引用/正文/位置说明、清理）、回归守卫 3 项
（源 checkout、残留容器、残留 volume），外加 1 项 forge token 不得出现在 artifact 里。

门禁（本轮全部通过）：

| 套件 | 命令 | 结果 |
|---|---|---|
| Go build | `cd backend; go build ./...` | exit 0 |
| Go vet | `cd backend; go vet ./...` | exit 0 |
| Go test | `cd backend; go test ./... -count=1` | exit 0，全部包 ok（`gate-go.log`） |
| Web 单测 | `pnpm --dir web test -- --run` | **28 文件 / 223 测试全部通过，exit 0**（`gate-web-test.log`） |
| Web 构建 | `pnpm --dir web exec next build` | exit 0（Next 16.4.3，17 条路由）（`gate-next-build.log`） |
| e2e | `pnpm --dir e2e test` | **164 passed**，1 worker（`gate-e2e.log`） |
| 演练 | `pwsh -NoProfile -File ./scripts/deliver-check.ps1` | **119 PASS / 0 FAIL，exit 0，102.1 s** |

未重建镜像：本轮没有改后端代码，演练用的仍是 `piforge:local`
（`sha256:2cdba2cd…`，构建于 2026-10-30 16:58）；`scripts/build-app-image.ps1` 未运行。

---

## 8. 交付判断

- **新操作员能不能从零走到可用 agent 回合？** 能。本次在空 volume 上只给端口、
  种子账号和 bootstrap 三件套，未进设置页，新用户即完成一次真实写码/运行/测试/提交。
- **能不能开出第一个 PR？** 能，而且是产品自己推的分支：`POST /git/pr` 先
  `git push -u origin drill/pull-request`，再向假 forge 发一次 create；重复调用返回
  `created=false` 和同一个 URL，forge 的 422 以 `step=create` + 原文上浮。token 只在
  `Authorization` 头里，容器日志 0 次、artifact 0 次。
- **行内评论能不能变成 agent 的输入？** 能。`notes.txt:2` 的评论经过服务器拼装后，
  agent 的 transcript 里出现 `行内评论 · notes.txt:2`、`2 | theme: dark`、评论正文和
  `位置：这段代码现在仍在第 2 行。`——引用是服务端拿当前文件核对过的，不是浏览器抄的。
- **演练会不会自己弄脏仓库？** 不会。源 checkout 在演练后 `git status` 为空；
  Docker 里没有 `pf-drill*` 容器或 volume。截图/日志都在数据卷或 ignored 的
  `e2e/.artifacts` 下。
- **这次扩展有没有发现产品 bug？** 没有。44 项新检查在第一次集成运行里就全绿，
  产品代码未改；唯一一次红是回归守卫对「脚本还没提交」的预期失败（脚本正确性，
  不是产品问题），提交脚本后复跑全绿。
- **最值得操作员注意的风险？** 密钥在容器环境里（`docker inspect` 可见，日志不可见）；
  种子密码只打印一次（现已不含密码，见 §6.10）；PR 的 forge token 是项目级、
  只写不回显，但 `docker inspect` 仍能看到 bootstrap 密钥。生产 runbook 要写清这三件事。
