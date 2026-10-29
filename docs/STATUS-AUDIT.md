# STATUS.md 复核报告：55 条 DONE 还剩几条是真的

- 审计对象：`docs/STATUS.md`（台账自称基线 HEAD `2e5f206`，但表里已经写到 `fr145`，实际 HEAD 是 `4f924e7`；`2e5f206..HEAD` 有 79 个提交）。
- 运行环境：`http://127.0.0.1:7722`（Docker 容器 `pf-local`，镜像 `piforge:local`，`-sandbox local`，真 `pi 1.4.0`，provider `volc` 可跑真 turn，容器内 `/usr/bin/chromium`）。
- 方法：先读每行点名的文件/测试，再尽量在运行中的产品上走一遍（API 或 Chrome）。探针都放在 `e2e/status-audit/`，审计后已删除；截图留在 `e2e/.artifacts/status-audit/`。只有能对应到用户可见行为的单测才算证据；只断言 handler 返回 200、或断言的东西永远为真的测试，不算。
- 计数：**DONE 55 条：CONFIRMED 48 / FALSE 4 / UNTESTED 2 / STALE 1**。

判定口径（比台账更严）：CONFIRMED = 我在运行中的产品上复现了行里承诺的行为；UNTESTED = 代码在，但没有任何测试断言那个行为，我也没能复现；FALSE = 代码不做行里说的事，或只在一个窄路径上成立；STALE = 行描述的是已经改掉的设计。

| 行 | 判定 | 证据 | 若为 FALSE/UNTESTED，缺什么 |
|---|---|---|---|
| FR-1.2 会话流就地折叠 | CONFIRMED | [e2e] `02-conversation.spec.ts` 点 `tool-group` 后断言 `tool-trace` 数量；[实测] 工作台里工具调用折成「1 个工具调用」，无弹窗。 | — |
| FR-1.3 双队列输入 | CONFIRMED | [单测] `npx vitest run components/session/composer.test.tsx` 8/8 通过，`follow_up`/`steer` 分别断言；[e2e] `02-conversation` 第二条断言 `disposition` 为 `queued`/`started`。 | — |
| FR-1.4 Esc 清队列 + abort + 回填 | CONFIRMED | [单测] 同文件 "interrupts on Esc and restores the cleared queue text" 通过；[实测] `POST /interrupt` 返回 `cleared_steering`/`cleared_follow_up`。 | — |
| FR-1.5 会话列表搜索/排序/徽章 | CONFIRMED | [实测] `GET /v1/sessions?q=audit%20parent` 命中 2 条；[e2e] `60-session-search` 跨项目 40 条会话、Enter 打开。 | — |
| FR-1.6 断线续传 | CONFIRMED | [实测] `GET /events?after_seq=5` + `Last-Event-ID: 12`，第一帧 `id: 13`；[单测] `sse_test.go` 含 409 `need_full_resync` 用例。 | 409 分支只有单测，未在真实超窗会话上触发。 |
| FR-1.9 压缩可见 | UNTESTED | [e2e] `05-branches.spec.ts` 只 `GET /entries` 断言存在 `type=compaction`，**从未断言 `CompactionMarker`（「上下文已压缩」）出现在 DOM**；[实测] 对一个小会话 `POST /compact` 返回 **500**，容器日志 `pi: command compact failed: Nothing to compact (session too small)`。 | 一条断言「上下文已压缩」可见的测试；真实 pi 的压缩路径；小会话应回 4xx 带原因而不是 500。 |
| FR-1.10 临时会话 | CONFIRMED | [实测] `ephemeral:true` → 201，`GET /sessions?project=` 不含它，按 id 可达；`main.go` 每 30 分钟扫一次、6 小时过期。 | — |
| FR-2.1 任意用户消息处「从这里分叉」 | CONFIRMED | [实测] 两条用户消息产生 2 个 fork 点（只有 user 消息）；在第二条处 fork，子会话保留第一轮 PINEAPPLE、不含第二轮 BLUE。 | fork 点的 `ts` 全是 `0001-01-01T00:00:00Z`，UI 无法显示时间（行里没承诺，但注释说会借 event 时间戳，实际没借到）。 |
| FR-2.2 分叉建新 session + 新 worktree | CONFIRMED | [实测] 子会话 `worktree_id` 与父不同且可读；[e2e] `05-branches` 第一条正是此断言。 | — |
| FR-2.5 对比视图 | **FALSE** | [实测] 父/克隆并排存在（2 列），但克隆列显示「改动 0 个文件 / +0 -0」，而该克隆的 `GET /git/log` 有 2 个自己的提交（`14da0d2`、`b986b2d`）；刚 clone 出来时该列轮次/token 从 0 起算，继承来的 2 轮不计。[e2e] `29-compare` 只断言出现「个文件」「token」这两个词，从不断言数字。 | 「改动规模」只看未提交改动，提交过的分支永远显示 0；clone 的继承用量不显示。需要按「分支 vs 基准」算 diff（或明确写「未提交」），测试必须断言一个非零数字。 |
| FR-2.6 采纳（cherry-pick / 整树覆盖） | **FALSE** | [实测] 分支有未提交文件时 `POST /adopt` 200；同一分支 `git commit` 后再 adopt 返回 **409「这个分支没有可采纳的改动」**（`gitx/adopt.go` 先要求 source 脏）。所谓「整树覆盖」不存在，实现是 `git apply --3way` 一份补丁。[e2e] `28-adopt` 只测未提交文件。 | 已提交的分支无法采纳，而产品自己的提交按钮就会制造这种分支；需要支持「相对基准的提交」，并为此写一条测试。 |
| FR-2.7 `clone` 复制当前分支 | CONFIRMED | [实测] clone 201，`entries` 5 条与父一致，独立 worktree；侧栏有按钮。[单测] `session_clone_test.go`。 | 无 e2e（行里已自认）。 |
| FR-3.1 同一主会话只有一个分支在执行 | CONFIRMED | [实测] 分支运行中给父发消息：分支 `running=false`、`stop_reason=aborted`，事件流出现 `session.paused {reason:"another branch started running", by:<父>}`。 | [e2e] `30-concurrency` 是空的：只断言 `features.parallel===false`，从不断言暂停；这条 e2e 不能当证据（单测 + 我的实测才是）。 |
| FR-3.2 显式并行运行 | CONFIRMED | [实测] 管理端打开 `parallel` 后，父与分支 **同时** `running=true`（轮询序列 `11`）；再关掉恢复 `false`；[代码] 管理员「功能开关」有 `feature-parallel`。 | 没有 e2e 跑两条并行 run；我是用真 turn 实测的。 |
| FR-3.3 并行上限 3→5 + 拒绝原因 | **FALSE** | [代码] `Deps.ParallelLimit` 在整个仓库只有 `router.go`/`concurrency.go`/`concurrency_test.go` 出现，**`main.go` 从未赋值，也没有 flag/env/管理界面**；所以「上限 5」永远到不了，默认 3。[单测] 直接把 `env.deps.ParallelLimit=1` 改成 1 来测 429。 | 一个能配置上限的入口（flag/env/admin UI）；一条从产品配置路径走到 429 的 e2e。 |
| FR-4.2 sidecar 只读 | CONFIRMED | [实测] 真 volc 侧车被要求 `write SIDECAR-WROTE.txt`，模型回「I don't have a write tool」，文件 404；[单测] `TestSidecarSpawnDropsWriteTools`。 | 无 e2e（行里已自认，且说明合理）。 |
| FR-4.7 提升为分支 | CONFIRMED | [实测] `POST /promote` 201，新分支有独立 worktree，7 条 entries 含侧车对话原文，原侧车 `archived`；[e2e] `21-promote` 断言同样内容。 | — |
| FR-5.2 打开会话自动唤醒 | **STALE** | [代码] `wakeSandbox` 只在 `message_handlers.go` 的 `handlePostMessage` 调用；[实测/代码] 前端没有任何 open→resume 的调用（`sandbox/resume` 只存在于手动按钮的 `client.ts`）。[e2e] `40-sandbox-lifecycle` 的注释写「opening the session again wakes it」，实际是 `POST /messages` 才唤醒。 | 行标题应改成「发消息时自动唤醒」。若按 PRD 字面要「打开即唤醒」，则降 PARTIAL（打开一个 PAUSED 会话，终端仍是死的，直到你发一句话）。 |
| FR-5.3 空闲暂停/14d 回收/先快照后销毁 | CONFIRMED | [实测] 用 HEAD 二进制另起 local 实例（`-sandbox-idle-pause 3s -sandbox-reclaim-after 8s -sandbox-reap-interval 1s`）：READY→3s PAUSED→8s MISSING；manifest 列出 `reap-me.txt`（未提交）并记录 branch/HEAD；`GET …/sandbox/snapshots` 有行；工作区目录被删、文件 404。部署日志也看到真实 30m 暂停（`sandbox idle: pausing … idle=30m27s`）。 | 只在 local provider 上跑过；docker 的销毁/回收未跑（行里已自认）；默认暂停 30m 与 FR 标题的 15min 不符（行里也自认）；快照只是清单，无 restore。 |
| FR-5.7 空闲回收（agent） | CONFIRMED | [实测] 同一二进制 `-agent-idle-ttl 3s`：run 结束后 pid 112860 存活；约 70s 后（reaper 间隔固定 1 分钟）进程消失，会话仍在，下一次 prompt 起新 pid 38796。 | 没有 e2e；e2e 里写的「45s TTL」实际首轮清扫在 60s，不能证明 45s 生效。 |
| FR-6.3 尊重 .gitignore + 开关 | CONFIRMED | [实测] 写 `.gitignore` + `ignored.txt`：默认 tree 不含它，`include_ignored=1` 后包含；[e2e] `11-files`。 | — |
| FR-6.4 ⌘P 快速打开 | CONFIRMED | [实测] Ctrl+P 打开 `quick-open-input`；[e2e] `15-quick-open` 建一个树里没加载过的文件、搜到并打开预览。 | — |
| FR-7.7 二进制下载卡 | CONFIRMED | [实测] 写 16 字节 PNG → `binary:true`、`mime_type:image/png`、body 空；[e2e] `19-preview`。 | — |
| FR-7.8 大文件保护 | CONFIRMED | [实测] 3 MiB 文件：`size=3145728`、`truncated:true`、预览 body 524307 字节；`download=1` 返回完整 3145728 字节。 | — |
| FR-7.10 网页内改文件并落盘 | CONFIRMED | [实测] `PUT /files` 后 `GET` 读到新字节；[e2e] `31-file-edit` 断言服务端字节、取消不写。 | — |
| FR-8.1 分支/ahead-behind/干净状态 | CONFIRMED | [实测] `git/status` 返回 `branch:"main"`、`ahead/behind`、`clean:false`；[e2e] `04-git`。 | — |
| FR-8.2 变更列表分组 | CONFIRMED | [实测] 三个未跟踪文件都出现在 `changes`；分组在 `git-changes.tsx`；[e2e] `04-git`。 | API 只有 `index/worktree` 码，staged/unstaged 分组是前端算的（行里没承诺 API 分组）。 |
| FR-8.7 push / pull | CONFIRMED | [实测] 容器内建 bare 仓库作 `repo_url`：写文件→commit→push 200，bare 的 `git log` 出现 `audit push …`；上游再提交后 `POST /git/pull` 200，`upstream.txt` 读得到，status 回到 `ahead=0 behind=0 clean=true`。 | — |
| FR-9.1 跟随终端数据源 | CONFIRMED | [实测] 真 turn 里 agent 跑 `echo FOLLOW-MARKER-…`，跟随面板拿到该输出；[单测] `normalize_test.go` 覆盖 `tool_execution_update`。 | — |
| FR-9.2 跟随终端 | CONFIRMED | [实测] 面板里出现两条 FOLLOW-MARKER（见 `follow-terminal.png`）；[e2e] `33-follow-terminal` **不能当证据**：它断言 `[data-testid=follow-terminal]` 的 `textContent.length != 0`，而这个容器里包含工具栏文字「跟随终端」，永远非空。 | 行为是真的，但测试是假的；应断言 xterm `.xterm-rows` 里出现那段输出。 |
| FR-10.1 完整 PTY + xterm | CONFIRMED | [实测] 在 xterm 里输入 `pwd; echo TERM-MARKER-$((1+1))`，拿到 `/data/sandboxes/…/repo` 与 `TERM-MARKER-2`；[e2e] `03-workspace` 另有协议层用例。 | 「真 PTY」那条 e2e 只断言 `data-pty-status=open`，不够；我实际敲了命令。 |
| FR-10.3 断线重连（offset） | CONFIRMED | [实测] 浏览器内开 WS：首次 234 字节；用 `from_offset=234` 重连只收到 126 字节新输出（没有整段重放）；用 `from_offset=0` 重连收到 236 字节（回放）。[单测] `manager_test.go` 偏移、`ring_test.go`。 | 行里的「60s 不丢」没有时间语义可测；只证明了 ring 回放。 |
| FR-10.4 addon-fit + resize | CONFIRMED | [代码] `terminal-surface.tsx` AddonFit + `use-pty.ts` 发 `{type:"resize"}` + `pty_handlers.go` `Term.Resize`；[单测] `TestResize` 断言真实 PTY 尺寸。 | 浏览器侧 fit 没单独断言；我看到的终端确实铺满面板。 |
| FR-10.8 与 agent 共享文件系统 | CONFIRMED | [实测] 终端 `pwd` = `/data/sandboxes/<user>/<project>/repo`，与 session 的 worktree 路径一致；[e2e] `03-workspace`。 | — |
| FR-11.1 画面投屏 | CONFIRMED | [实测] 浏览器面板 canvas 采样 2500/2500 非白，截图里能看到 PiForge 登录页；[e2e] `08-browser`。 | 初次启动可能失败（日志 `devtools never became ready`），重试后可用。 |
| FR-11.2 观察模式（默认不可输入） | UNTESTED | [实测] 默认 `mode=observe`；[代码] `browser_handlers.go` 在非 takeover 时 `break` 丢弃输入。但 [单测] `browser_mode_test.go` 只读写 mode，**没有断言输入被丢弃**；e2e 也没有。 | 一条真正证明「observe 下键入/点击不生效、takeover 下生效」的测试。代码在，但我没有在页面上实测输入被丢。 |
| FR-11.4 模式互斥 + 确认 + 中断 | CONFIRMED | [实测] 点「观察」弹「接管浏览器？」；确认 takeover 后一个正在跑的 run `running=false`（`browser_mode.go` 里 Interrupt）；按钮显示「观察/控制中」。 | [e2e] `43` 只断言弹窗和按钮文字，不断言 run 被中断；中断这半是我实测的。 |
| FR-11.7 多 tab | CONFIRMED | [实测] `POST /browser/tabs` 201，2 个 tab，UI tab 条可切换/关闭；[e2e] `26-browser-tabs`。 | — |
| FR-11.8 截图入 workspace | **FALSE** | [实测] 面板里正显示 PiForge 登录页时，`POST /browser/screenshot` 返回 201，写出的却是 **661×750 全白 PNG（3008 字节）**，即启动时那个 `about:blank` 页；随后 4 次连拍全部 **422「浏览器正忙，稍后再试一次」**，tabs 接口接着变成「浏览器暂时不可用」。原因是截图抓的是 CDP 启动时 attach 的默认页，不是用户正在看的 tab。[e2e] `20-screenshot` 从不导航，只断言 `bytes>1000`——空白 PNG 也满足。 | 截图要抓当前激活的 tab；要有一条先导航到真实页面、再断言像素/内容不是空白的测试。 |
| FR-11.9 profile 持久化 | CONFIRMED | [实测] 容器内 `find /data/sandboxes -name .piforge-browser` 命中多个沙箱，下面有 `Default/`；[单测] `profile_test.go`。 | 只验证了目录存在/复用，未验证 cookie 在两次会话间真的生效。 |
| FR-13.1 admin 共享 provider + Inherited 徽章 | CONFIRMED | [实测] 管理员建共享 provider 后，dev 的列表里 `source:"inherited"`；[e2e] `12-shared-providers` 断言「继承」徽章、无编辑按钮。 | — |
| FR-13.2 私有 provider + 同名 shadow | CONFIRMED | [实测] 同名（admin 共享 + 用户私有）时，dev 只看到 `source:"custom"` 的私有那条（`base_url` 也是私有的），共享条目被覆盖；[单测] `TestProvidersMergeAndShadow`。 | — |
| FR-13.3 key AES-GCM + 沙箱内无明文 | CONFIRMED | [实测] 读容器内运行中的 `pi` 进程 `/proc/<pid>/environ`：`ANTHROPIC_API_KEY` / `OPENAI_API_KEY` 是带 `ses_…`、`exp` 的网关 JWT，不是 provider key；[单测] `TestProviderKeysAreSealedAtRest`、`TestGatewayInjectsAnthropicKeyAndStripsTheEphemeralOne`；真 turn 走网关成功。 | — |
| FR-13.6 会话级模型覆盖 | CONFIRMED | [实测] `PATCH /sessions/{id} {model_ref:"volc/deepseek-v4-flash"}` 后 `GET` 保留该值；[e2e] `10-models` 断言 session 的 model_ref 变为自定义 id。 | 测试只证明「存下来了 / 传给 Pi 的 spec」，没有证明上游真的用了这个模型。 |
| FR-13.7 连通性测试 | CONFIRMED | [实测] `POST /providers/{volc}/test` → `{ok:true,status:200,latency_ms:1185..1922}`（第一次曾 10s 超时 `context deadline exceeded`）；[单测] `provider_probe_test.go` 4 例。 | 10s 固定超时会偶发把可用 provider 报成失败（我碰到 1 次）。 |
| FR-13.9 用户 API Key | CONFIRMED | [实测] 创建 201 返回一次性 `token:"pfk_…"`；用它 `Authorization: Bearer` 调 `GET /v1/projects` 200；撤销后同 token 401；[e2e] `24-api-keys`。 | — |
| FR-14.5 会话记录 skill@version | CONFIRMED | [实测] 上传 `audit-skill-…@9.9.9` 后新会话：`GET /sessions/{id}/skills` 记录 version/hash/mounted_path；挂载文件内容可读；pi 的 session jsonl 系统提示词里出现 `<skills><skill><name>audit-skill…`；[e2e] `63-skill-version`。 | — |
| FR-14.6 `/skill:name` + `/` 面板 | CONFIRMED | [实测] 输入 `/` 弹出 5 个 skill，带版本与 hash（`v1.0.0 · ba97509f`）；[单测] `skill-palette.test.tsx` 6/6 通过；[e2e] `64-skill-palette`。 | — |
| FR-14.7 内置 skill | CONFIRMED | [实测] `GET /v1/skills` 返回 `code-review@1.0.0`、`explain-codebase@1.0.0`、`test-gap@1.0.0`，scope 均为 admin；[e2e] `39-builtin-skills`。 | — |
| FR-15.1 MCP server 配置 | CONFIRMED | [实测] 建 `echo` server 后开会话：`/data/pi-agents/<sid>/mcp.json` 含该 server；容器内 `PI_CODING_AGENT_DIR=… pi mcp list --json` 认出它（`source` 指向该文件）。 | 「汇总用户与共享 server」只有读路径（`ListMCPServers` 含 `scope='admin'`），没有创建共享 MCP 的 admin 路由。 |
| FR-15.2 MCP 凭据加密 + 注入 | CONFIRMED | [实测] 创建时带 `AUDIT_TOKEN=audit-secret-12345`：`mcp.json` 的 `env` 有明文（给进程用），而 `piforge.db` 与 `-wal` 里 `grep` 不到该串；[单测] `secrets_test.go`、`mcp_test.go`。 | — |
| FR-16.1 用户管理 | CONFIRMED | [实测] 管理员建用户 201；禁用后该用户登录 403 `account is disabled`；重置密码 200 后新密码可登录；改角色 200。 | 响应包是 `{users:[…]}` 而不是 `{user:…}`，容易让脚本写错，但不是行里的承诺。 |
| FR-16.2 共享 provider 管理 | CONFIRMED | [实测] 建共享 provider 200、dev 侧 `inherited`、删除 200；[e2e] `12-shared-providers`。 | — |
| FR-16.7 功能开关 | CONFIRMED | [实测] 管理员开 `parallel` 后 dev `GET /v1/features` 为 true，关掉恢复 false；[e2e] `17-feature-flags` 证明 export 关闭后服务端 403（不是只藏按钮）。 | — |
| FR-16.8 支持模式（只读 + 留痕） | CONFIRMED | [实测] 管理员开支持访问 200，转写含对话原文，`expires_at` 30 分钟；同一管理员 `PATCH /sessions/{id}`、`POST /messages`、`GET /files` 全 404；所有者 `GET /support-notices` 看到原因与管理员邮箱，可标记已读；UI 有「只读」徽章（`support-mode.png`）。 | — |

## 附：这些都「通过」了，但不能当证据

按台账自己的口径（有实现 + 至少一条测试）它们算 DONE，但测试抓不住回归：

- `33-follow-terminal.spec.ts`：断言 `[data-testid=follow-terminal]` 的 `textContent` 非空，而该容器包含工具栏「跟随终端」，**永远通过**。跟随面板真坏掉它也绿。
- `20-screenshot.spec.ts`：只断言 PNG 字节数 `>1000`。661×750 的纯白 PNG 是 3008 字节，**空白截图也能通过**；测试从不导航。
- `30-concurrency.spec.ts`：发两条消息后只断言 `features.parallel === false`，**没有任何暂停/中断/`session.paused` 断言**；注释说的「says so in the sibling's own transcript」不存在。
- `05-branches.spec.ts` 压缩用例：只查 `/entries` 有 `compaction`，**不查「上下文已压缩」是否渲染**。
- `29-compare.spec.ts`：只断言出现「个文件」「token」两个词，**不校验数字**（0 个文件/0 token 也通过）。
- `03-workspace.spec.ts`「the terminal opens a real PTY」：只断言 `data-pty-status=open`，不断言字节进出；真正有效的是同文件下一条协议用例。
- `10-models.spec.ts`：只断言 session 的 `model_ref` 被存成自定义 id，没有断言上游调用真的用了它。

## 最严重的五条

1. **FR-2.6 采纳：一提交就永远采纳不了。** 用户在分支里点「提交」（产品自己的按钮），再点「采纳回主干」，得到 409「这个分支没有可采纳的改动」。实测用同一个分支：未提交时 200，提交后立刻 409；实现只肯搬未提交的工作树，所谓「整树覆盖」不存在。这是 JS-2「试几种改法再择优」的最后一步，也是用户最会截图发群的一步。

2. **FR-2.5 对比：提交过的分支显示「0 个文件 / +0 -0」。** 对比页只统计未提交改动；分支一旦提交（agent 很爱提交），它在对比表里就是零改动。clone 出来的分支在跑第一轮之前还显示 0 轮次/0 token，因为继承来的对话不算进用量。用户会照着这张表挑那个「什么都没改」的分支。

3. **FR-11.8 截图：截到的是 about:blank，不是用户眼前的页面。** 面板正显示 PiForge 登录页时，截图 API 写进 workspace 的是一张 661×750 全白 PNG（3008 字节）；随后连拍 4 次全部 422「浏览器正忙」，tabs 接口也挂了。agent 拿到的是空白图；测试只验「大于 1000 字节」，所以一直是绿的。

4. **FR-3.3 并行上限：文档承诺 3→5，产品里根本没有把上限改成 5 的入口。** `Deps.ParallelLimit` 在 `main.go` 从未赋值，没有 flag/env/管理页；「5」只是 `concurrency.go` 里的一个 clamp 常量。单测靠直接改 deps 把它设成 1 来测 429，绕过了产品配置路径。运营者买了「最多 5 个分支并行」的承诺，实际永远是 3。

5. **FR-1.9 压缩可见：真实压缩 500，所谓「可见」没人验过。** 对小会话点压缩，服务端回 500 internal（日志：`Nothing to compact (session too small)`），应该是一个带原因的 4xx。唯一的测试是 fake peer 下查 `/entries` 有没有 `compaction`，从不看 `CompactionMarker` 是否出现；真机压缩阈值以上的会话我没法造，所以这条只能算「没验过」。

（第六名：FR-11.2「观察模式不可输入」的丢弃逻辑没有任何测试断言，只有代码；一旦回归，人和 agent 会互相抢鼠标。我把它算 UNTESTED，不算 FALSE，因为代码里确实有服务端 `break`。）

## 需要改台账的清单

| 行 | 现在 | 应改为 | 为什么 | 最小修法 |
|---|---|---|---|---|
| FR-2.5 对比视图 | DONE | **PARTIAL** | 视图在，但「改动规模」对提交过的分支恒为 0，clone 的继承用量从 0 起算；测试只查词不查数。 | 用「分支 vs 基准」算 diff；测试断言一个非零的已提交改动。 |
| FR-2.6 采纳 | DONE | **PARTIAL** | 只支持未提交工作树；已提交分支 409；无「整树覆盖」。 | 支持相对基准的提交；补一条「提交后仍可采纳」的测试。 |
| FR-3.3 并行上限 3→5 | DONE | **PARTIAL** | 上限 5 不可配置，产品永远 3；429 只有单测。 | 加 flag/env + 管理页；补 429 的产品级测试。 |
| FR-11.8 截图 | DONE | **PARTIAL** | 能写 PNG，但抓默认 tab 而不是当前页；面板打开时高频 422。 | 截图绑定当前激活 tab；测试先导航再断言像素。 |
| FR-1.9 压缩可见 | DONE | **PARTIAL** | 「可见」没有任何 DOM 断言；真实小会话压缩 500。 | UI 断言 + 把 pi 的「太小」映射成 4xx。 |
| FR-11.2 观察模式 | DONE | **PARTIAL**（若严格按「不可输入」算） | 默认 observe 实测成立；「输入被丢弃」只有代码，没有测试也没有实测。 | 加一条 observe 下键入不生效、takeover 下生效的测试。 |
| FR-5.2 打开会话自动唤醒 | DONE | **DONE（改标题）** | 设计已改成「发消息时唤醒」；标题仍写「打开会话」。 | 标题改为「发消息自动唤醒」；若坚持 PRD 字面，则降 PARTIAL 并实现 open→resume。 |
| FR-9.2 跟随终端 | DONE | **保留 DONE** | 功能实测有效；但 `33-follow-terminal` 是永真断言。 | 换掉测试，断言 `.xterm-rows` 含 bash 输出。 |
| FR-3.1 串行 | 保留 DONE | — | 单测 + 我实测都成立；`30-concurrency` 是空测试。 | 把暂停/`session.paused` 断言补进 e2e。 |

**反向陈旧（不是 DONE 行，但台账同样不准）**：FR-12.5「测试结果面板」仍写 MISSING，但 HEAD 已有 `internal/testrun` + `GET /sessions/{id}/test-runs` + 测试面板 + `62-test-panel.spec.ts`（断言失败/通过两种状态），最低应记 PARTIAL/DONE。台账基线 `2e5f206` 落后 HEAD 79 个提交，这类行不止一条。

## 无法核实

- **FR-5.3 的 docker provider 回收**：文档自己承认没跑过容器回收；我只复现了 local provider。docker 的 `Destroy` 是否保留宿主目录、reclaim 后 `MISSING` 行是否一致，需要一台有 docker 的机器单独跑。
- **FR-5.3 的默认 30m/336h**：我用 3s/8s 跑同一条状态机；部署日志只等到 30m 的暂停，336h 的回收没等（也不可能等）。
- **FR-1.9 的 UI marker**：无法把真实会话压到 pi 的压缩阈值（小会话直接 500），所以没看到「上下文已压缩」在页面上出现。
- **FR-11.2 的「输入被丢弃」**：拿到默认 observe、看到服务端 `break`，但没有在真实页面上证明键入无效；需要一条能读回输入框内容/页面状态的测试。
- **FR-2.1 的 UI 点击分叉**：我走的是 API（`POST /fork`），`18-branching` 覆盖了 UI 但没有运行整条 spec；分叉对话框本身的点击链未实测。
- **FR-11.9 的 cookie 复用**：只证明 profile 目录在多个沙箱里存在/复用，没有验证登录态跨会话保留。
- **FR-13.6 的「真的用了这个模型」**：只证明存进 session 并进了 pi 的 spec；上游实际模型只能从网关 token 的 claim 推断，没有做一次可区分的模型输出实验。
- **配额 / 审计留存**：`STATUS.md` 里没有「配额」或「审计留存」行，无法按行核。最接近的是支持模式：`support_access` 授权 30 分钟、owner 通知保留最近 50 条，表里没有清理/留存期，也没有配额限制。

## 证据文件

- 截图（`e2e/.artifacts/status-audit/`）：`compare.png`、`support-mode.png`、`support-notice-owner.png`、`skill-palette.png`、`follow-terminal.png`、`terminal.png`、`browser-login-tab.png`、`browser-screenshot-visible-tab.png`（空白截图本体）、`browser-panel.png`、`browser-takeover-confirm.png`、`workbench.png`。
- 探针（审计后删除）：`e2e/status-audit/`，含 fork/clone/compare/adopt、sandbox 生命周期、agent 回收、MCP、skill/sidecar、support、浏览器、PTY 重连、push/pull 等脚本。
- 另起实例只用于两个时间敏感实验：local provider 的 3s/8s 回收、agent 的 3s TTL；其他结论都来自 `127.0.0.1:7722` 上的部署。
