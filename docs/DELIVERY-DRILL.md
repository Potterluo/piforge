# PiForge 从零交付演练（Delivery Drill）

> 演练日期：2026-10-30（Asia/Shanghai）
> 演练脚本：[`scripts/deliver-check.ps1`](../scripts/deliver-check.ps1)
> 本次成功运行的证据目录：[`e2e/.artifacts/delivery-drill/20261030-073152/`](../e2e/.artifacts/delivery-drill/20261030-073152/)
> 演练镜像：`piforge:local`（`sha256:6a6f1fdedd5e8ebec659bcc419ec8d8340d079560883a48184454cea9eeb5312`）
> 结论：**新操作员可以从零走到一次可用的 agent 回合；无需手工配置服务商。** 本次 74 项断言全部通过，产品代码未改动。

---

## 0. 一句话结论

在一个**全新的空 volume** 上，只给端口、种子账号和 bootstrap 服务商环境变量启动
`piforge:local`，`/v1/setup` 即返回 `needs_provider:false`、`shared_provider_available:true`；
一个通过 API **新建的普通用户**可以登录、建项目（无 repo）、建会话，并让 agent
真实地写脚本、运行脚本、跑 `npm test`、用容器身份提交 commit。浏览器面板截图、文件树
自动刷新、空闲 Chromium 回收也都成立。唯一的产品层瑕疵是 **Node 内置测试器（TAP）的
结果显示为 `unrecognized`**，属于展示精度问题，不阻塞交付。

---

## 1. 本次实测数字

下表全部来自本次成功运行，不是估算。原始值见
[`summary.json`](../e2e/.artifacts/delivery-drill/20261030-073152/summary.json) 的
`metrics` 与 `checks`，以及各命令输出文件。

| 指标 | 实测值 | 证据 |
|---|---:|---|
| `docker run` → `GET /healthz` 200 | **0.55 s** | `summary.json` → `healthz_seconds` |
| 发送 prompt → `run.settled` 事件时间戳 | **11.54 s** | `events.sse` 的 `run.settled.ts`；`summary.json` → `prompt_to_settle_seconds` |
| 发送 prompt → 轮询首次看到 `running:false` | **12.02 s** | `summary.json` → `prompt_to_first_idle_seconds` |
| 本次回合 token | 输入 **5,419**，输出 **991** | `events.sse` → `run.settled.payload` |
| 截图后 Chromium 进程数 | **10** | `summary.json` → `chromium_processes_active` |
| 空闲超时（60 s）后 Chromium 进程数 | **0**（wait 61.00 s） | `summary.json` → `chromium_processes_after_idle` / `chromium_idle_seconds` |
| 演练结束前容器内存 | **170.5 MiB / 15.43 GiB** | `docker-stats.txt` |
| 演练结束前容器 CPU / PIDs | **0.00 % / 45** | `docker-stats.txt` |
| 浏览器面板截图 | **35,042 bytes，1280×713，132 个采样颜色** | `browser-panel.png`、`browser-panel.json` |
| 测试面板记录 | **1 条**，命令 `npm test`，状态 `unrecognized` | `test-runs.json` |
| commit | `fca98a551280b81d969b148bfd2474b8f5af8a61` | `commit.txt` |
| commit 身份 | `PiForge\|piforge@localhost\|Add script to print first 10 primes with tests` | `commit.txt` |
| 工作区文件 vs 沙箱文件 SHA-256 | 两边均为 `94a64d08f744a9e54f7c13eb1614a2c8367b979204580075cf82514526277c21` | `summary.json` |
| 会话 transcript | 16 条 entry；4 条 assistant 回复；7 条 toolResult；7 个 toolCall | `entries.json`、`summary.json` |
| 断言 | **74 PASS / 0 FAIL**，脚本 exit 0，总耗时 96 s | `summary.json`、`official-console-2.log` |

本次运行的标识：

- 新用户：`drill-20261030-073152@piforge.local`
- 项目：`prj_01M6502XJRSE36M1EQQP2ZMHMJ`
- 会话：`ses_01M6502XJTWH0K33M9HKN2BB9R`
- 容器 / volume：`pf-drill` / `pf-drill-data`（已在 `finally` 中删除）
- 既有部署 `pf-local`：演练前后容器 ID 相同，`/healthz` 始终 200

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
  -d '{"email":"drill-20261030-073152@piforge.local","display_name":"Delivery Drill","password":"<drill-user-password>","role":"user"}' `
  http://127.0.0.1:7731/v1/admin/users

# 新用户登录后：项目不关联 repo
curl.exe -sS -b new-cookies.txt -H "X-PF-CSRF: $newCsrf" -H 'Content-Type: application/json' `
  -d '{"name":"drill-20261030-073152","repo_url":"","default_branch":"main"}' `
  http://127.0.0.1:7731/v1/projects

# 在项目下建会话
curl.exe -sS -b new-cookies.txt -H "X-PF-CSRF: $newCsrf" -H 'Content-Type: application/json' `
  -d '{"title":"delivery drill"}' `
  http://127.0.0.1:7731/v1/projects/prj_01M6502XJRSE36M1EQQP2ZMHMJ/sessions
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
  -d '{"client_msg_id":"drill-20261030-073152","text":"...上面那段..."}' `
  http://127.0.0.1:7731/v1/sessions/ses_01M6502XJTWH0K33M9HKN2BB9R/messages
```

返回 `accepted:true, disposition:"started"`，随后脚本轮询
`GET /v1/sessions/{id}/status`，直到 `running:false` 且 `last_seq` 连续 2 s 不变。
为了拿到真正的完成信号，脚本在结束后用有界 `curl` 回放 SSE：

```powershell
curl.exe -sN --max-time 10 -b "pf_session=<cookie>" `
  "http://127.0.0.1:7731/v1/sessions/ses_01M6502XJTWH0K33M9HKN2BB9R/events?after_seq=0" `
  -o events.sse
```

`events.sse` 中同时存在 `event: run.started` 和 `event: run.settled`；本次
`run.started.ts=2026-10-29T23:31:54.35817348Z`、
`run.settled.ts=2026-10-29T23:32:05.643808848Z`，相对发送 prompt 的时间为
**11.54 s**。用产品自己的完成信号（`run.settled`，即 `agent_settled` 的规范化事件）
计时，而不是用轮询到的第一个 `running:false` 估算。

### 2.6 验 transcript、文件、commit、测试面板

```powershell
# transcript（16 条：user 1、assistant 4、toolResult 7、system/model_change 等）
curl.exe -sS -b new-cookies.txt http://127.0.0.1:7731/v1/sessions/<id>/entries

# 文件树
curl.exe -sS -b new-cookies.txt "http://127.0.0.1:7731/v1/sessions/<id>/files/tree?path=&depth=3"

# 测试面板
curl.exe -sS -b new-cookies.txt "http://127.0.0.1:7731/v1/sessions/<id>/test-runs?limit=20"
```

容器内验证（这是“commit 确实在容器里、作者确实是容器身份”的直接证据）：

```powershell
docker exec pf-drill sh -c "cd /data/sandboxes/<user>/<project>/repo && git log -1 --format='%an|%ae|%s'"
# PiForge|piforge@localhost|Add script to print first 10 primes with tests

docker exec pf-drill sh -c "cd /data/sandboxes/<user>/<project>/repo && git status --porcelain"
# 空输出（clean）

docker exec pf-drill sh -c "cd /data/sandboxes/<user>/<project>/repo && sha256sum primes.js"
# 94a64d08f744a9e54f7c13eb1614a2c8367b979204580075cf82514526277c21  primes.js
```

同一文件通过 API 下载（`/files?path=primes.js&download=1`）后算出的 SHA-256
与容器内 `sha256sum` **完全一致**。commit 内容是：

```text
package.json
primes.js
test/primes.test.js
```

`npm test` 的输出（`test-runs.json` 的 `output_tail`）显示 TAP：

```text
TAP version 13
# Subtest: first 10 primes are correct
ok 1 - first 10 primes are correct
# Subtest: isPrime rejects composites and non-primes
ok 2 - isPrime rejects composites and non-primes
1..2
# tests 2
# pass 2
# fail 0
```

但面板状态是 `unrecognized`、`total/passed/failed` 都是 0——见 §6。

### 2.7 浏览器面板截图

```powershell
# 打开容器内自己的真实页面（Next 在 127.0.0.1:3100 监听）
curl.exe -sS -b new-cookies.txt -H "X-PF-CSRF: $newCsrf" -H 'Content-Type: application/json' `
  -d '{"url":"http://127.0.0.1:3100/login"}' `
  http://127.0.0.1:7731/v1/sessions/<id>/browser/tabs

# 截图（写入 checkout 的 screenshots/ 目录）
curl.exe -sS -b new-cookies.txt -X POST -H "X-PF-CSRF: $newCsrf" `
  http://127.0.0.1:7731/v1/sessions/<id>/browser/screenshot
# {"path":"screenshots/20261029-233222-110.png","bytes":35042}
```

脚本把 PNG 通过文件 API 下载回来，解码后统计采样颜色：**1280×713，132 种颜色**，
不是空白页。截图内容为 PiForge 登录页（带种子账号的预置提示），见
`browser-panel.png`。

说明：这里调用的是浏览器面板“相机”按钮背后的同一个
`POST /v1/sessions/{id}/browser/screenshot` 接口；本次没有用 Playwright 去点那个按钮。
按钮本身的 UI 路径由 `e2e/tests/20-screenshot.spec.ts` 覆盖，但那个 spec 不在本次
演练的执行范围内（本次没有改产品代码，未跑完整 e2e）。

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
# pf-drill|170.5MiB / 15.43GiB|0.00%|45
```

### 2.10 拆除与不干扰确认

```powershell
docker rm -f pf-drill
docker volume rm pf-drill-data

docker ps -a --format "{{.Names}}" | Select-String '^pf-drill$'   # 无输出
docker volume ls --format "{{.Name}}" | Select-String '^pf-drill-data$' # 无输出

docker inspect pf-local --format '{{.Id}}|{{.State.Running}}'
curl.exe -fsS http://127.0.0.1:7722/healthz   # 200
```

`pf-local` 在演练前后容器 ID 相同（`7eaa287f...`），`/healthz` 始终 200。
它本身没有 Docker healthcheck（`docker inspect` 没有 `.State.Health`），所以脚本用
**容器 running + 端口 `/healthz` 200** 作为“仍然健康”的证据。

---

## 3. 证据文件（都在 `e2e/.artifacts/delivery-drill/20261030-073152/`）

| 文件 | 证明了什么 |
|---|---|
| `summary.json` | 74 项检查的 PASS/FAIL、所有 metrics、运行起止时间 |
| `container.log` | bootstrap 创建了共享服务商、`has_key=true`；种子密码已脱敏；密钥 0 次出现 |
| `setup-admin.json` / `setup-user.json` | 管理员和新用户的 first-run 状态；新用户继承共享服务商 |
| `context.json` | 用户、项目、会话、worktree ID |
| `events.sse` | `run.started` / `run.settled` 原始帧，settle 时间戳（270,643 bytes） |
| `entries.json` | 16 条 transcript：user、assistant、toolResult、toolCall |
| `files-tree.json` | 文件树包含 `primes.js`、`package.json`、`test/primes.test.js` |
| `primes.js` | 从 API 下载的工作区副本，用于和沙箱文件比对 SHA-256 |
| `commit.txt` | `git log -1`、HEAD、`git status --porcelain`、commit 文件列表 |
| `test-runs.json` | 测试面板的唯一一条 run（`npm test`，TAP 输出，状态 `unrecognized`） |
| `browser-tab.json` | 打开的真实页面 URL 和 tab id |
| `browser-panel.png` | 浏览器面板截图原图（登录页） |
| `browser-panel.json` | 截图的尺寸、字节数、采样颜色数 |
| `ui-file-tree.png` | 真实会话页截图：`ui-live.txt` 出现在文件树中 |
| `ui-check.json` / `ui-check.log` | Playwright 断言：appeared/noReload/primesVisible |
| `docker-stats.txt` | 演练结束前的内存、CPU、PIDs |
| `e2e/.artifacts/delivery-drill/official-console-2.log` | 本次运行的完整控制台输出（74 项 PASS） |
| `e2e/.artifacts/delivery-drill/go-test-2.log` | Go 测试套件输出 |
| `e2e/.artifacts/delivery-drill/web-test.log` | Web 单元测试输出（22 文件 / 186 测试） |

注意：`e2e/.artifacts/` 被 `.gitignore` 忽略（第 31 行）。因此**提交的是可复跑的脚本和
本报告，原始证据保留在本机该目录**；需要留档时请把整个 `20261030-073152/` 目录另行归档。
本次还保留了一个失败尝试目录 `20261030-073102/`，原因见 §6.1。

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
  -BrowserIdleWaitSeconds 75
```

行为约定：

- `PIFORGE_DRILL_KEY` 缺失、镜像不存在、端口被占、`pf-drill*` 名称已存在、或
  `-ContainerName`/`-VolumeName` 指向 `pf-local` 时，**在创建任何东西之前失败**。
- 只删除本次创建的容器和 volume；`finally` 里执行，断言失败也会清理。
- 任一断言失败 → exit 1；全部通过 → exit 0。
- 控制台打印 Summary 表和 Measured 表；完整结果在 `summary.json`。
- `-SkipUi` 跳过 Playwright 文件树检查（没有 `e2e/node_modules` 或 Chrome 时使用）。

---

## 5. 新操作员仍需自己做的事（脚本不覆盖）

1. **准备镜像。** `piforge:local` 必须已经存在。若自己构建，浏览器面板依赖
   Chromium，而 Dockerfile 的 `INSTALL_CHROMIUM` 默认是 `false`：

   ```powershell
   docker build --build-arg INSTALL_CHROMIUM=true -t piforge:local .
   ```

   没有 Chromium 时，服务本身可用，但浏览器面板会报 “unavailable”，本演练的截图断言会失败。

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

10. **成本与配额。** 脚本会真实调用一次模型（本次输入 5,419 / 输出 991 tokens）；
    重跑就是再花一次。CI 里应使用专用 key 和限额。

---

## 6. 失败与易碎点（本次真实观察）

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

### 6.3 直接运行 `node --test` 不会被记录

`internal/testrun.Detect` 的识别列表里没有 `node`；只有 `npm test` / `pnpm test` /
jest / vitest 等会建 run。演练因此在 prompt 里要求 agent 用 `npm test` 包一层。
如果真实用户只让 agent 跑 `node --test`，测试面板会是空的——这是产品能力缺口，
但不在本次“从零交付”阻塞范围内。

### 6.4 种子密码会打印在容器日志里

首次启动的日志会打印 admin/dev 的初始密码（只打印一次，这是产品设计）。
`container.log` 里这些值已被脚本替换为 `<redacted>`；但操作员如果直接把
`docker logs` 贴到工单/CI，会泄露初始密码。生产应在首次登录后立即改密。

### 6.5 截图会弄脏 checkout

`POST /browser/screenshot` 把 PNG 写进工作区的 `screenshots/`（这是产品设计，
方便 agent 读取截图）。演练在 commit 校验之后才截图，所以 commit 时是 clean 的；
但真实用户提交后再截图，`git status` 会多出 `screenshots/` 未跟踪目录。
如果团队要求 clean tree，需要把 `screenshots/` 加进 `.git/info/exclude` 或
在截图后自行清理——脚本没有替产品做这个决定。

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

§6.2–§6.5 已作为三个易碎点修掉，原观察保留在上面作为演练记录。注意：§7 的
「产品代码：未改动」只属于发起这次演练的那一轮，本轮为修这三个点动了产品代码。

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

**产品代码：未改动。** 本次只新增：

- `scripts/deliver-check.ps1`（演练脚本）
- `docs/DELIVERY-DRILL.md`（本报告）

按任务约定，未改产品代码时只跑两套既有测试：

| 套件 | 命令 | 结果 |
|---|---|---|
| Go | `cd backend; go test ./... -count=1` | 第一次与 Web 并行运行时 `internal/pi` 的 `TestRuntimeRunEventOrder` 时序失败；单测重跑通过；**单独重跑完整套件通过，exit 0**（`go-test-2.log`） |
| Web | `pnpm --dir web test -- --run` | **22 个测试文件 / 186 个测试全部通过，exit 0**（`web-test.log`） |

未运行 `go build ./...`、`go vet ./...`、`next build` 和完整 e2e（148），因为本次没有
产品代码变更；如果后续要改 §6.2/§6.3 的测试解析器，必须补跑这些门禁。

---

## 8. 交付判断

- **新操作员能不能从零走到可用 agent 回合？** 能。本次在空 volume 上只给端口、
  种子账号和 bootstrap 三件套，未进设置页，新用户即完成一次真实写码/运行/测试/提交。
- **最值得先修的产品问题？** Node 内置测试器的 TAP 解析（§6.2/§6.3）。它不阻塞交付，
  但会让 Node 项目在测试面板上显示“未识别”，与用户看到的 `# pass 2` 不一致。
- **最值得操作员注意的风险？** 密钥在容器环境里（`docker inspect` 可见，日志不可见）；
  种子密码会打印一次；截图会弄脏 checkout。这三点都应在生产 runbook 里写明。
