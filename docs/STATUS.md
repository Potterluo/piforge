# 实现状态（对照 PRD）

这份表是**逐条核对代码后**的结果，不是设计意图。判据（比上一版更严）：

- **DONE**：实现 + 至少一条断言**用户可见承诺**的测试或端到端路径。只断言 handler 返回 200、或断言永远为真的测试，不算。
- **PARTIAL**：实现了但缺这样一条测试，或只做了 API / 只做了界面，或只在某一条路径上成立；行内写明缺什么。
- **MISSING**：仓库里没有对应实现。

保留它的原因：我此前两次在汇报里把“做完”说早了。第一次是“P2 全落地”（真），第二次暗示 PRD 全落地（假）。
一张带证据的表比一段自我评价可靠。

基线：HEAD `d0f3f7a`（"fix(fr92,fr19): a live follow view, and a no-op compaction with a reason"）；共 126 条 FR：DONE 54 / PARTIAL 52 / MISSING 19 / 不做 1。此后 FR-3.3 接上真实配置与可解释拒绝、FR-1.9 补上浏览器入口（见文末「本次修复（FR-3.3）」与「本次修复（FR-1.9 浏览器入口）」），该轮结束时为 **DONE 55 / PARTIAL 52 / MISSING 18 / 不做 1**；当前值见下一段。
本轮把 `15b995c`（惰性浏览器面板 + 空闲停止）与 `ba532a2`（首次运行 setup）记入「交付能力（非 FR）」两行，均为 DONE；此后 FR-14.4（zip 导入 + 可禁用）、FR-6.6（⌘⇧F 内容搜索）与 FR-10.2（终端多 tab）从 PARTIAL 移入 DONE，126 条 FR 为 DONE 58 / PARTIAL 49 / MISSING 18 / 不做 1；加上这两行后**整表 128 行：DONE 60 / PARTIAL 49 / MISSING 18 / 不做 1**。

本批（flake 修复 + FR-11.2 + FR-12.4）之后，FR-11.2 与 FR-12.4 从 PARTIAL 移入 DONE：**126 条 FR 为 DONE 60 / PARTIAL 47 / MISSING 18 / 不做 1**；加上两条交付能力后**整表 128 行 = DONE 62 / PARTIAL 47 / MISSING 18 / 不做 1**。

本批（四个缺陷）之后，FR-16.3（共享 Skill 管理）从 MISSING 移入 DONE；FR-14.1 仍 PARTIAL（两种作用域都能创建、共享可见、非管理员不可改，但「同名覆盖」语义仍缺）；FR-14.4 补上「导入重启用」。非 FR 交付能力新增「项目删除不遗留沙箱目录」一条：**126 条 FR 为 DONE 61 / PARTIAL 47 / MISSING 17 / 不做 1**；**整表 129 行 = DONE 64 / PARTIAL 47 / MISSING 17 / 不做 1**。

本批（命令面板 + 同名覆盖）之后，FR-14.1 从 PARTIAL 移入 DONE（规则、挂载、会话记录、UI 与测试齐了）；非 FR 交付能力新增「⌘K 命令面板默认选中」一条（PRD §10.1-5 / §13 有要求，但没有 FR 行）：**126 条 FR 为 DONE 62 / PARTIAL 46 / MISSING 17 / 不做 1**；**整表 130 行 = DONE 66 / PARTIAL 46 / MISSING 17 / 不做 1**。

本批（diff 作用域与逐 hunk UI + e2e 定向清理）之后，FR-12.1 与 FR-12.2 从 PARTIAL 移入 DONE：**126 条 FR 为 DONE 64 / PARTIAL 44 / MISSING 17 / 不做 1**；**整表 130 行 = DONE 68 / PARTIAL 44 / MISSING 17 / 不做 1**。`FR-12.1` 的 DONE 只覆盖 工作区/已暂存/分支 vs 基准/提交范围 四个作用域——PRD 原文里的「本轮」仍没有入口，行首标题已按实际覆盖改写，不拿 DONE 冒充。另一个不是 FR 的改动在 `e2e/scripts/`：e2e 数据目录不再无限增长（见文末「e2e 数据目录的定向清理」），它不改变任何 FR 状态。

本批（FR-12.3 行内评论 + 读连接旧快照的触发条件）之后，FR-12.3 从 MISSING 移入 DONE：**126 条 FR 为 DONE 65 / PARTIAL 44 / MISSING 16 / 不做 1**；**整表 130 行 = DONE 69 / PARTIAL 44 / MISSING 16 / 不做 1**。同批的另一半不是 FR：读连接为什么会停在旧快照被量出来了（触发条件是「语句被中断」，即客户端 abort 掉的请求），修法是把被中断过的连接退役、不再发给下一个请求，证据与仍未回答的问题见文末「读连接旧快照：触发条件、直方图与修复」。

本批（FR-12.6 一键开 PR）之后，FR-12.6 从 MISSING 移入 DONE，FR-8.8（fetch/pull/push + 开 PR）也随之从 PARTIAL 移入 DONE：**126 条 FR 为 DONE 67 / PARTIAL 43 / MISSING 15 / 不做 1**；**整表 130 行 = DONE 71 / PARTIAL 43 / MISSING 15 / 不做 1**。这两个数是本批逐行重数表格得到的；上一行记录是 65 / 44 / 16，而旧表头写的是 64 / 44 / 17（早已与表格不一致），本批以实际行为准，并把这个差异写在这里而不是继续抄旧数。

本批（中断恢复）之后，FR 状态不变：**126 条 FR 仍为 DONE 68 / PARTIAL 42 / MISSING 15 / 不做 1**；非 FR 交付能力新增「中断恢复：原因 + 存活状态 + 继续」一条（均 DONE）：**整表 135 行 = DONE 77 / PARTIAL 42 / MISSING 15 / 不做 1**。PRD 没有「运行被中断后怎么恢复」的 FR 行，所以它不是 FR；但它改的是三个真实结局（用户停止 / 服务重启 / agent 进程退出）共用的一条用户可见路径，证据见文末「本批：中断恢复」。

本批（最终验收的三处「界面说了后端不知道的事」）之后，FR 状态不变：**126 条 FR 仍为 DONE 68 / PARTIAL 42 / MISSING 15 / 不做 1**；**整表 135 行 = DONE 77 / PARTIAL 42 / MISSING 15 / 不做 1**（按表格行重数）。三处都是「界面承诺了后端并不知道的事」，所以改的是来源而不是文案：①**测试面板**从 agent 真实跑过的 `node primes.test.js`（没有 `--test` 标志）里一个字都记不下来——检测只认 `node --test`，现在按 node 自己的测试文件命名约定识别，并用 node 22 的真实 TAP 输出做夹具；②**状态说谎**——fork 复制过来的转录可能停在一次 run 中间，回放它会让会话头声称「运行中 · 3 个工具」而 `/v1/sessions/{id}/status` 说 `running:false`，现在 running 只由状态 API 与真正实时的 run 事件决定，回放只能结束 run 不能开始 run；同时「变更」面板订阅终端的输出（复用文件树已有的那条去抖信号），不再需要手点刷新；③**管理页的数字**把「运行中的 agent」（其实是活进程数，打开一个空闲 fork 就会 +1）拆成「进行中的运行」与「agent 进程」，行内评论入口从 `opacity-0` 改成常驻低对比度，新会话对话框说出「默认模型」到底解析成哪个模型、并说明缺 key 的 provider 不影响它。证据见文末「本批：最终验收的三处真相修复」。

本批（fork 转录的截断 + Node 24 spec reporter）之后，FR 状态不变：**126 条 FR 仍为 DONE 68 / PARTIAL 42 / MISSING 15 / 不做 1**；**整表 135 行 = DONE 77 / PARTIAL 42 / MISSING 15 / 不做 1**（按表格行重数）。fork 不再复制父会话的事件，而是记录 `sessions.forked_at_seq`，读取时把父日志（cut 之前）与分支自己的事件合并成一条单调转录；那条把长会话截断在 1000 条的 `ListEvents` 复制路径已经删除。测试面板补上 Node 24 默认的 spec reporter（`ℹ pass N`），直接 `node x.test.js` 的输出不再记为未识别格式。证据见文末「本批：fork 转录的截断（lineage）+ Node 24 spec reporter」。

本批（终端输入的三个缺陷）同样不改变任何 FR 状态：**126 条 FR 仍为 DONE 68 / PARTIAL 42 / MISSING 15 / 不做 1**；**整表 135 行 = DONE 77 / PARTIAL 42 / MISSING 15 / 不做 1**。三处都是「实现声称支持、实际会丢用户输入」，不是缺功能：①**快速输入丢字**——`use-pty.ts` 在 socket 未 OPEN 时直接把按键丢掉（不是客户端合并、不是 PTY 读循环、也没有 debounce），量到「输入开始 → socket open」的窗口后按 `send` 逐条排队并在 open 时按序逐帧 flush；②**`git log` 进分页器**——PTY 环境没有 `GIT_PAGER`/`PAGER`，`less` 吃掉后续按键，现在 PTY 环境固定 `PAGER=cat`/`GIT_PAGER=cat`/`GIT_TERMINAL_PROMPT=0`；③**分叉对话框的「创建分支」无故禁用**——补上「分支名称必填」说明与按钮 `title`。根因测量、fail-first 与门禁见文末「本批：终端输入的三个缺陷」。
本批（中断后下一条消息）不改变任何 FR 状态：**126 条 FR 仍为 DONE 68 / PARTIAL 42 / MISSING 15 / 不做 1**；**整表 135 行 = DONE 77 / PARTIAL 42 / MISSING 15 / 不做 1**。它修的是 HANDOFF §2 那条「未核实」的报告，而且**不是**我们这一侧的交付/归属缺陷：探针证明 follow-up 以自己的 run 送达了模型、run id 是新的、transcript 归属正确；Pi 把被中断的半截回答留在会话里，模型于是继续/重做上一条请求。修法是在「中断后的第一条消息」前由服务端拼一句「上一轮已作废，只回答这条」——与行内评论、继续同一条 compose 原则，transcript 显示的就是 agent 收到的。实测 0/6 → 5/5（最终措辞两批共 10/10；旧措辞 4/5），是强缓解而非保证。报告里的 29 ms 没有复现（13 次中断里新 run 最短 1.4 s），证据与边界见文末「本批：中断后下一条消息被拿去回答上一条请求」。

本批（agent 进程进入沙箱，stage 1）同样不改变任何 FR 状态：**126 条 FR 仍为 DONE 68 / PARTIAL 42 / MISSING 15 / 不做 1**；**整表 135 行 = DONE 77 / PARTIAL 42 / MISSING 15 / 不做 1**。它改的是 HANDOFF Known limit #1 里那句"控制面不是执行边界"：`-agent-in-sandbox`（默认关闭）下 `pi` 由沙箱容器启动、控制面通过 runner 的新 `attach` 端点桥接它的 stdio，沙箱里 `cat /data/piforge.db` 变成 `No such file or directory`，属主缺陷（面板改不了 agent 写的文件）在这个摆法下消失。代价与没做的部分同样明确：agent 仍随控制面重启而死（阶段三才换常驻 supervisor），`materialize`/`provision` 仍写共享卷，MCP 配置故意不进沙箱。证据见 `docs/SANDBOX-DEPLOY.md` §1.4.3，设计与四阶段见 `docs/EXECUTION-BOUNDARY.md`。

本批（多租户成为可运营属性）不改变 FR 状态：**126 条 FR 仍为 DONE 68 / PARTIAL 42 / MISSING 15 / 不做 1**；**整表 135 行 = DONE 77 / PARTIAL 42 / MISSING 15 / 不做 1**。它补的是验收报告 `e2e/.artifacts/final-isolated/VERDICT.md` §8 第 3 条——"隔离"在执行与文件上成立，租户之间却只隔着认证。三件事，各有命令与证据（设计与取舍见 `docs/EXECUTION-BOUNDARY.md` §12，运维见 `docs/SANDBOX-DEPLOY.md` §1.7）：①**网络**：沙箱不再共享 `piforge-sandboxes`，每个租户一条桥 `piforge-t-<hash>`（runner 建、带 `piforge.tenant` 标签），只有该租户的沙箱 + 控制面 + runner 在上面；从沙箱 A 到 B 按名字 `no such host`、按地址 `curl` 4.0 s 超时/`nc` 无路由，同时 A 到自己的模型网关 200、到控制面 `/v1/*` 仍 401（`e2e/.artifacts/tenancy/isolation-probes.txt`）。选"每租户"而不是"每沙箱"的理由是产品规则（一个 (user, project) 一个沙箱、授权边界是用户）加地址池硬墙：本机 Docker 29.9.2 第 17 条网络就报 `all predefined address pools have been fully subnetted`。**同租户的两个沙箱仍互通 8080**（镜像的每沙箱 key 仍是 401），文档明说不宣称这一点。②**配额**：CPU/内存/进程数来自按租户的策略文件（`deploy/tenant-limits.json`，`PIFORGE_TENANT_LIMITS_FILE`，启动时校验、出错指出租户），经 `docker run --cpus/--memory/--pids-limit` 生效，从容器 `HostConfig` 读回并存进沙箱行，`/v1/admin/status` 的管理页「租户配额与网络」按租户并排显示策略与用量；并发沙箱上限在建之前数行，拒绝是 typed 的 `409 tenant_sandbox_limit` + `{limit,current,tenant}`（此前只写一行 WARN，用户看到的是三层之后的 500）。③**审计**：`GET /v1/admin/audit`（仅管理员、分页、可过滤、`format=csv`）跨租户读 `tool_calls`：谁/什么/何时/成败 + 会话 + 项目 + 用户，另有 `/admin/audit` 页面。`scripts/soak-tenancy.ps1` 用 4 个租户同时各跑一条会调用工具的 prompt，一次跑完隔离、配额、审计与磁盘/网络探测：**0 failures，exit 0**。

本批（租户出口策略，tenancy 的最后一块）不改变 FR 状态：**126 条 FR 仍为 DONE 68 / PARTIAL 42 / MISSING 15 / 不做 1**；**整表 135 行 = DONE 77 / PARTIAL 42 / MISSING 15 / 不做 1**。它补的是上一批 §12.4 自己写下的那条"没有出口控制"：沙箱在租户桥上仍能上公网，`npm install`、`curl`、被投毒的依赖都拿到宿主的出网能力，而部署里没有一处表达这是不是故意的。现在有 `deploy/tenant-egress.json`（`PIFORGE_TENANT_EGRESS_FILE`，控制面与 runner 都读，启动校验），三种取值：`none`（默认，桥用 `docker network create --internal` 创建，没有默认路由与 NAT，外部 DNS 也不再解析；控制面与 runner 在这条桥上，所以 `/gw/llm`、`/gw/mcp` 与 checkout 照常）、`open`（显式的今天行为）、`allowlist`（**建模但不执行，启动时拒绝并指出租户**——Docker 只能按桥过滤，按 hostname 需要内网桥上的过滤代理或宿主 iptables，两个都没做，所以不假装支持）。同一道闸门还拒绝 `none` + 共享网络、共享网络 + 按租户覆盖、`-sandbox local` + 任何关闭模式；改模式要重建桥，代码宁可拒绝建沙箱并给出 `docker network rm`，也不让"看着建好了、其实还开着"的桥存在。管理页 `/admin/status` 的「租户配额与网络」多了一行「沙箱出口」并按账号显示 `出口：关闭/开放`（`GET /v1/admin/status` 的 `tenant_limits.egress_*`）。证据：`scripts/probe-egress.ps1` 在同一个一次性栈上把默认租户设成 `none`、另一个设成 `open`，从沙箱内部量 DNS、按名字与按字面地址的公网请求、控制面 `/healthz` 200、`/v1/*` 401、自己的网关 200，并在**被关闭的租户**里跑一条真实 prompt（能跑通，因为 agent 需要的是网关不是公网）。设计与不宣称的东西见 `docs/EXECUTION-BOUNDARY.md` §13，运维与排查见 `docs/SANDBOX-DEPLOY.md` §1.7.5。

---

---
# PiForge 对照 PRD 的实现审计

- 审计对象：`C:\Users\<user>\Downloads\piforge`，HEAD `be01575`（"fix(browser): serialise launches per sandbox, and recover a dead profile lock"），工作树干净（本文件除外）。
- 审计方法：先读 `docs/STATUS-AUDIT.md`（`06b61a3`）的每条判定，再逐条重读 `backend/internal/httpapi/router.go` 的路由表、各 handler、`web/components/**` 与 `e2e/tests/*.spec.ts`；以「实现 + 断言了用户可见结果的测试名」为准，不采信命名与注释。
- 状态口径：**DONE** 必须点名一条断言用户可见承诺的测试或 e2e 路径；**PARTIAL** 必须写清缺什么；**MISSING** 是无实现。
- 本版把 `06b61a3` 之后的修复接进台账：`f766651`（FR-2.5 / FR-2.6）、`bd85ade`（FR-2.7）、`9c4ab0b`（FR-11.8）、`717f7cb`（FR-12.5），以及浏览器启动按沙箱单飞与 profile 僵尸锁恢复（FR-11.1）。本批补上 FR-12.6（一键开 PR）并把 FR-8.8 从 PARTIAL 移到 DONE——开 PR 是那一条里唯一缺的能力。
- 计数（按本文件表格行重数，不是抄旧表头）：**126 条 FR = DONE 68 / PARTIAL 42 / MISSING 15 / 不做 1**（`be01575` 时是 52 / 54；此后 FR-1.9、FR-9.2、FR-3.3、FR-14.4、FR-6.6、FR-10.2、FR-11.2、FR-12.4、FR-14.1、FR-12.1、FR-12.2、FR-16.3、FR-12.3、FR-12.6、FR-8.8、FR-11.3 逐一移入 DONE）。另有 9 条非 FR 交付能力（见「交付能力（非 FR，单独计）」），均 DONE；**整表 135 行 = DONE 77 / PARTIAL 42 / MISSING 15 / 不做 1**。重启对账那一批不改变任何 FR 状态，只新增一条非 FR 行。
- 本批（远端存活探测）同样不改任何 FR 状态、不加表格行：它补的是 `internal/reconcile` 权威来源那一块（上一批自己在「边界」里写明没做），所以只改「重启对账」那一行的证据，行数与计数仍是 **134 行 = DONE 76 / PARTIAL 42 / MISSING 15 / 不做 1**。PRD 没有「谁来证明控制面之外的 agent 还活着」的 FR 行，把它塞进 FR 表会把「产品需求覆盖」和「交付可靠性」混成一个数字；理由与边界写在「本批：远端存活探测」。

> 说明：PRD 中没有「部署」类 FR（§12 是里程碑，`deploy/` 是 Dockerfile.app / Dockerfile.offline / Dockerfile.runner / k8s.yaml / entrypoint.sh，属交付物而非需求行），因此下面没有部署行。`sandbox-runner`（把 Docker CLI 从控制面移出去）同样是交付物而非 FR：它没有改变任何一条 FR 的状态，证据见 `docs/SANDBOX-DEPLOY.md` §1.4 与 `scripts/verify-runner-sandbox.ps1`。
>
> 后续交付（同样不改 FR 状态）：每沙箱一个 `SANDBOX_API_KEY`（`crypto/rand` 生成、创建时经内部 API 上报一次、AES-GCM 加密存 `sandboxes.api_key_cipher`；`-sandbox-api-key` 降为调试用固定覆盖）。它修的是「runner 让端点跨租户可达之后，一个部署级 key 等于所有沙箱的 key」这个问题，证据见 `docs/SANDBOX-DEPLOY.md` §1.4、`docs/SANDBOX.md`「每个沙箱一个 key」，以及 `TestDockerProviderPerSandboxAPIKey`（真实容器：A 的 key 打 B 是 401）与 `scripts/verify-runner-sandbox.ps1` 的 cross-tenant 检查。

| FR | 状态 | 证据 |
|---|---|---|
| FR-1.1 新建会话（仓库/基准分支/模型/推理强度） | PARTIAL | 会话只选模型/推理/临时（标题框已删，首条消息自动命名）：`web/components/shell/new-session-dialog.tsx`（`session-model`、`session-thinking`）；仓库与基准分支只在建项目时选（`new-project-dialog.tsx` 的 `repo_url`/`default_branch`，`handleCreateProject`）。测试：`e2e/tests/00-shell.spec.ts` "creates a project and a session through the dialogs"（未覆盖分支选择）、`59-auto-naming.spec.ts` |
| FR-1.2 会话流就地折叠 | DONE | `web/components/session/message-item.tsx` + `thinking-trace.tsx` + `tool-trace.tsx`（Trace/Collapsible，无弹窗）；`e2e/tests/02-conversation.spec.ts` "a full run streams a reply, a tool call and settles"（点 `tool-trace` 展开 `tool-output`） |
| FR-1.3 双队列输入（follow-up / steer） | DONE | `composer.tsx` `onKeyDown` → `submit("follow_up")` 或 `submit("steer")`；后端 `handlePostMessage` 的 `streaming_behavior` 校验；`composer.test.tsx` 三个用例 + `02-conversation.spec.ts` "Enter queues a follow-up while running, Ctrl+Enter steers" |
| FR-1.4 Esc 清队列 + abort + 回填 | DONE | `composer.tsx` `interrupt()` 把 `cleared_steering`/`cleared_follow_up` 合并回输入框；后端 `handleInterrupt` → `rt.Interrupt`（`clear_queue`）；`composer.test.tsx` "interrupts on Esc and restores the cleared queue text" |
| FR-1.5 会话列表：搜索/排序/状态徽章 | DONE | `app-sidebar.tsx` `groupSessions` + `StatusDot` + 归档切换 + 按 `last_active_at` 排序，顶部 `session-search-input` 跨项目搜索（`GET /v1/sessions?q=`，客户端 `sessions.list` 透传 `q`/`limit`）+ 键盘上下/Enter；命令面板的「最近会话/会话」组同样接 `?q=`；`e2e/tests/60-session-search.spec.ts` 2 例 |
| FR-1.6 断线续传（SSE Last-Event-ID + entries） | DONE | `sse.go` `handleEvents`（读 `Last-Event-ID`/`after_seq`，窗口超限回 409 `need_full_resync`）+ `handleEntries`；`sse_test.go` 5 个用例，含 `TestSSEReplayFromLastEventID`、`TestSSEGapReturnsNeedFullResync` |
| FR-1.7 重命名/归档/删除/导出（MD / HTML） | PARTIAL | 重命名/归档/删除：`handlePatchSession`/`handleDeleteSession` + `app-sidebar.tsx` `SessionActions`，`14-session-actions.spec.ts`；导出：`session_export.go` 同时有 `renderMarkdown` 与 `renderHTML`，但 UI 只有 `api.sessions.exportUrl(id,"md")`，HTML 无入口 |
| FR-1.8 上下文用量（按轮 in/out/cache + 窗口占用） | PARTIAL | `handleSessionStats` + `Store.SessionStats`/`LastContextUsage`；`context-marker.tsx` `ContextMeter`（tokens/percent/cost）。无单测或 e2e 断言用量数值 |
| FR-1.9 压缩可见 | DONE | `context-marker.tsx` `CompactionMarker`（原因、tokens 前后、`firstKeptEntryId`、摘要可展开）+ `handleCompact` + **`compact-context.tsx` 的「压缩上下文」按钮（会话头用量条旁）**。修复后：压缩成功返回 200 `{"compacted":true}`；**没有可压缩的上下文时返回 200 `{"compacted":false,"reason":"没有可压缩的上下文"}`**（`pi` 的 `CommandError` "Nothing to compact (session too small)" 在 `Runtime.Compact` 里映射成 `contracts.ErrNothingToCompact`，handler 翻译成 no-op，不再 500）。测试：`handlers_test.go`（成功与 no-op 两条，后者断言 no-op 不写 marker）、`runtime_test.go` `TestNothingToCompactIsRecognized`、`pitest` `TestServeRefusesToCompactASmallSession`；e2e `05-branches.spec.ts`「a compaction shows a marker in the transcript and drops the context meter」断言 DOM 里出现「上下文已压缩（手动触发）150k → 32k tokens」且 `context-meter` 从 75% 掉到 16%，「compacting a small session is a no-op with a reason, not a 500」断言 200 + reason 且页面没有 marker。旧代码跑这两条：分别失败在 `200 vs 202`、`Expected 200 Received 500`（见文末「本次修复」）。**本轮补上的浏览器入口**：`GET …/status` 的 `process_id` 为空时按钮禁用并显示 `compact-disabled-reason`「没有运行中的 agent」；no-op 时把 API 的 `reason` 原样写进 `compact-status` 并弹 toast（不是只放在响应里）；成功时显示「上下文已压缩，用量已更新」并 `invalidateQueries(qk.stats)`，用量条不刷新页面就下降（SSE `context.usage` 通常先到，这里是兜底）。测试：`compact-context.test.tsx` 3 例（禁用+说明、reason 上屏、meter 75%→16%）、e2e `66-compact-ui.spec.ts` 3 例（无 agent 禁用、no-op reason 上屏且无 marker、成功后 meter 下降）；旧代码跑 `66` 三例全部失败在 `getByTestId('compact-context')` 不存在（见文末）。 |
| FR-1.10 临时会话 | DONE | `project_handlers.go` `ephemeral` 字段 + `store.SweepEphemeral` + `piforged/main.go` 6 小时清理循环；`ephemeral_test.go` `TestEphemeralSessionsAreHiddenAndSwept` 与 `23-ephemeral.spec.ts` |
| FR-2.1 任意用户消息处「从这里分叉」 | DONE | `GET /v1/sessions/{id}/fork-points` → `handleForkPoints`（只列 user message）、`POST …/fork` → `handleFork`；UI `branch-tree.tsx`（"可分叉的用户消息"）与 `fork-dialog.tsx`；`18-branching.spec.ts` 3 例 |
| FR-2.2 分叉建新 session + 新 worktree | DONE | `handleFork` → `provisionSession` → `provision.EnsureWorktree` → `gitx.AddWorktree`（`worktree.go`）；`05-branches.spec.ts` "forking a user message creates a branch session with its own worktree"、`fork_history_test.go` |
| FR-2.3 分支树视图（父子/时间/状态/diff 规模） | PARTIAL | `branch-tree.tsx` 画的是 **Pi entry 树**（`TreeNodeRow`，按 `children` 缩进）+ 分叉点列表；主干↔分支的父子只在侧栏 `app-sidebar.tsx` `groupSessions` 里体现（`Collapsible` + `StatusDot`）。没有「创建时间/diff 规模」列，也没有分支级图形。无测试 |
| FR-2.4 分支命名 / 归档 / 删除（清理 worktree） | PARTIAL | 命名与归档：`handleFork` 的 `label`、`handlePatchSession`；删除：`handleDeleteSession` **明确保留 worktree 在磁盘上**（注释 "The worktree stays on disk"），`gitx.RemoveWorktree`/`PruneWorktrees` 全仓库零调用；且从未写 Pi 的 `label` entry（`pi/runtime.go` 无 `set_label`/`label`）。`18-branching.spec.ts` 只覆盖父子关系 |
| FR-2.5 对比视图（2–3 分支并排） | DONE | `GET /v1/sessions/compare?ids=` + `compare-page.tsx` + `/p/{project}/compare`；`f766651` 让「改动规模」按「分支 vs 基准」同时算已提交与未提交，clone 的继承轮次/token 也计入。`compare_test.go` `TestCompareCountsCommittedAndUncommittedWork`、`TestCompareCountsInheritedUsage`；`e2e/tests/29-compare.spec.ts` 断言真实数字（已提交 1 个文件 / +2 行、未提交 1 个 / +1 行、合计 2 个 / +3 −0、未动分支为 0、`含继承 1`），并把 `2 个文件` / `+3 / -0` 读回页面。缺 checkout 时报 `diff_error` 而非 0。**本批补一句解释**：`compare-explainer` 写明「改动」相对分支基准计算、主会话的分支就是基准所以它对自己永远是 0 个文件——验收 pass 3 的「主会话 0 个文件」是正确但不可发现；`29-compare.spec.ts` 断言这句话在页面上（旧前端没有该 testid） |
| FR-2.6 采纳（cherry-pick / 整树覆盖） | DONE | `gitx.Runner.Adopt` + `POST /v1/sessions/{id}/adopt` + 侧栏「采纳回主干」；`f766651` 让分支**已提交**的工作也能被采纳（fast-forward / merge commit，再把脏工作树叠上去）。测试：`adopt_test.go` `TestAdoptFastForwardsCommittedBranch`、`TestAdoptMergesCommitsThenAppliesTheDirtyRemainder`、`TestAdoptRefusesConflictsAndLeavesTheTargetUntouched`；`e2e/tests/28-adopt.spec.ts` 的 "a branch that committed its work can still be adopted" 断言父会话拿到文件、且分支 commit 确实在 `main` 的 HEAD 可达（不是只复制了字节） |
| FR-2.7 `clone` 复制当前分支 | DONE | `session_clone.go` `handleCloneSession` + `POST /v1/sessions/{id}/clone`（router.go:203）+ `api.sessions.clone` + 侧栏 `data-testid="session-clone"`；`bd85ade` 让 clone 的分支按会话唯一（此前按标题，两次 clone 会共用一个 checkout）。测试：`session_clone_test.go` `TestCloneKeepsTheWholeConversation`、`provision_test.go` `TestTwoClonesOfOneSessionGetDistinctWorktrees`；`e2e/tests/27-clone.spec.ts` 断言整段对话、独立 `worktree_id`、两条不同的 `piforge/*` 分支、agent 的 cwd 分属两个 checkout |
| FR-3.1 同一主会话下只有一个分支在执行 | DONE | `httpapi/concurrency.go` `enforceConcurrency`：发消息前中断同 root 的其它运行中分支，并在对方事件流写 `session.paused`；`concurrency_test.go` 断言「分支被中断 + 对方事件流出现 `session.paused`」。注意 `30-concurrency.spec.ts` **不能当证据**：它只断言 `features.parallel === false`，从不检查暂停事件 |
| FR-3.2 显式并行运行 2–3 分支 | DONE（每个分支仍是自己的会话与 PTY） | 每用户开关 `parallel`（`store.FeatureParallel`，默认关）；管理员「功能开关」里可打开 |
| FR-3.3 并行上限 3→5 + 拒绝原因 | DONE | 上限是真配置：`cmd/piforged` 的 `-parallel-limit` / `PIFORGE_PARALLEL_LIMIT`（默认 3，允许 1–10）经 `httpapi.ResolveParallelLimit` 解析，**启动时校验**（0、11、非整数都是启动错误，不是静默默认），再写入 `Deps.ParallelLimit`；启动日志为 `parallel branch limit branches=1 max=10`。可见：`GET /v1/admin/status` 返回 `parallel_limit` / `parallel_limit_max`，管理页「配置」显示为 `stat-parallel-limit` / `parallel-limit-value`（「并行分支上限（1–10，来自 -parallel-limit）」）。拒绝可解释：`concurrency.go` 的 429 文案为「并行上限是 N 个分支，现在有 M 个在运行（分支名…），先让其中一个跑完」，`details` 带 `limit` / `running` / `running_branches`；`composer.tsx` 把 429 渲染成内联 `parallel-limit-refusal`（`parallel-limit-reason` 是原句），不再落进「发送失败」toast。测试：`cmd/piforged/parallel_limit_test.go`（flag/env：默认 3、0→错误、11→错误、5→5、flag 优先于 env、非整数→错误）；`concurrency_test.go` `TestParallelLimitRefusesTheThirdRun`（经 resolver 设 2，两次 `POST /messages` 真的起两个 run，第三次 429 且文案含上限与两个在跑的分支，`details.running` 两条）；`composer.test.tsx` 断言 429 原句上屏且草稿保留；e2e `65-parallel-limit.spec.ts` 用 `PIFORGE_PARALLEL_LIMIT=2` 启动的实例验证管理页=2、两条 `[[hold-run]]` 在跑、第三条在浏览器里被拒并显示原句，API 同时 429+details。旧代码跑 `65`：失败在 `parallel-limit-value` 不存在。部署实例以 `PIFORGE_PARALLEL_LIMIT=1` 重启后实测（截图 `e2e/.artifacts/fix-limit-compact/40-deployed-admin-limit.png`、`42-deployed-refusal.png`）。 |
| FR-3.4 多窗格 Workbench（2–4 个分支实时流） | MISSING | `workspace-panels.tsx` 的 `split` 是**检查器第二个面板**（`PANELS = changes/git/browser/terminal`），不是多个分支的会话流；`25-split.spec.ts` 只验证两个面板同时显示。`docs/STATUS.md` 把这条记为「完成」，属误记 |
| FR-3.5 分支级终端 tab / 浏览器 tab | PARTIAL | 终端天然按会话隔离（`pty_handlers.go` 的 key 为 `sess.ID+":"+tab`）；浏览器是**每 sandbox 一个**（`browser_handlers.go` 用 `worktree.SandboxID`），分支间共享，且无 cookie 共享/分离选项 |
| FR-3.6 冲突预警（同一文件被两分支改） | MISSING | 无任何文件级冲突检测；`GitStatus.conflicts` 只透传工作区未合并项，与分支间比较无关 |
| FR-4.1 Sidecar 面板（可拖宽 / 可全屏 / 窄屏 Sheet） | PARTIAL | `sidecar-dock.tsx` 用右侧 `Sheet`（`sm:max-w-lg`），不可拖宽、无全屏；`05-branches.spec.ts` "opening a sidecar shows the dock and accepts a prompt" |
| FR-4.2 sidecar 只读（写工具被拦住） | DONE（单测 + 容器实测；无 e2e） | `pi.ArgsForKind` 给 sidecar 追加 `--exclude-tools bash,edit,write`；`TestSidecarSpawnDropsWriteTools`；容器内真实模型回答 `NO_WRITE_TOOL` 且文件未创建。**没有 e2e**：e2e 跑的是 fake peer，它不模拟工具集，写一条只会断言假实现 |
| FR-4.3 显式引用（@main:msg_xxx 拖入 sidecar） | MISSING | `sidecar-dock.tsx` 无引用卡、无附件；`@main` 全仓库零命中；后端也没有注入 `custom_message` 的路径 |
| FR-4.4 Sidecar 独立模型 | PARTIAL | `handleSidecar` 接受 `model_ref` 并落到 session；`SidecarDock` 调 `api.sessions.sidecar(sessionId)` 不传模型，面板里也没有模型选择器 |
| FR-4.5 提权（sidecar 变可写） | 不做，由 FR-4.7 取代 | 让旁路在同一个 checkout 里写等于和主会话抢文件；写权限的正确路径是「提升为分支」 |
| FR-4.6 多个 sidecar（最多 3，tab 切换） | PARTIAL | 上限 3 有后端实现并有单测（`handlers_test.go` `TestSidecarSharesWorktreeAndIsCapped`）；UI 只有一个 dock，无 3 个 tab 切换，也无「已有 sidecar 列表」 |
| FR-4.7 提升为分支（带上上下文） | DONE | `sidecar_promote.go` `handlePromoteSidecar` + `contracts.SessionCloner` `CloneSession` + `copyEvents`；`21-promote.spec.ts` "promoting keeps the conversation and gives it its own worktree"、`sidecar_promote_test.go` |
| FR-5.1 沙箱状态机 + UI 状态与倒计时 | PARTIAL | `contracts.SandboxState`（9 态）+ `sandbox.go` `setState`/`Ensure`（STARTING→READY）；UI 只在侧栏显示 `sandbox_state`（`app-sidebar.tsx` "沙箱 · READY"），**无倒计时/expires 展示**；`sandbox_test.go` 覆盖状态转换 |
| FR-5.2 发消息时唤醒 | DONE | 发消息时若沙箱是 PAUSED 自动 `Resume`（`message_handlers.go` `wakeSandbox`）；`40-sandbox-lifecycle.spec.ts` 暂停后 `POST /messages`，`GET /sandbox` 变回 `READY`。UI **没有**「打开即唤醒」：打开一个 PAUSED 会话不会恢复，直到发第一条消息——标题按实际行为写成「发消息时唤醒」 |
| FR-5.3 空闲 15min 暂停 / 14d 回收 / 先快照后销毁 | DONE（本地 provider 实测；快照是清单索引，不是工作区副本） | `internal/reaper` 两段状态机：READY→`Pause`→PAUSED，PAUSED→snapshot→`Destroy`→MISSING；`piforged/main.go` 的 `-sandbox-idle-pause`（默认 30m，0 关闭）/`-sandbox-reclaim-after`（默认 336h，0 关闭）/`-sandbox-reap-interval`（默认 1m），启动即跑一轮。**顺序固定为快照→销毁**：快照失败只记日志、provider 与 store 行都不动，下一 tick 重试（`TestReaperSnapshotFailureLeavesSandboxUntouched`、`TestReaperDestroyFailureKeepsPausedForRetry`）。`internal/snapshot` 把每个 checkout 的路径/大小/branch/HEAD/未提交清单写进 `<dataDir>/snapshots/<sandbox-id>/<timestamp>/manifest.json`，并在 `snapshots` 表记一行；`GET /v1/sessions/{id}/sandbox/snapshots` 只读列出。测试：`TestReaperPausesOnlyReadyPastThreshold`、`TestReaperReclaimsOnlyPausedPastThreshold`、`TestReaperIsIdempotentWhenRunTwice`、`TestReaperDisabledChangesNothing`、`TestTakeIndexesFilesGitAndUncommittedWork`。**局限**：完整回收只在 `-sandbox local` 上实跑过；docker 的 `Destroy` 会保留宿主数据目录（`docker.go` 注释），但未跑容器回收；没有 restore-from-snapshot；本地 provider 回收会删掉工作区目录，清单只记录「当时有什么」，文件内容不保留 |
| FR-5.4 多 worktree 创建/列出/清理 | PARTIAL | 创建：`provision.EnsureWorktree`（`provision_test.go`、`routes_test.go` `TestCreateSessionProvisionsWorktree`）；列出/清理：无 HTTP 端点，`gitx.ListWorktrees`/`RemoveWorktree`/`PruneWorktrees` 无调用者 |
| FR-5.5 依赖缓存共享 | MISSING | `GOMODCACHE`/`_cacache`/`CARGO_HOME` 全仓库零命中 |
| FR-5.6 快照（fs / fs+mem，仅工具边界） | PARTIAL | 只有数据层：`store/resources.go` `CreateSnapshot`/`ListSnapshots` + `store_test.go`；没有任何生产者、没有工具边界钩子、无 HTTP、`sandbox.Pause` 不快照 |
| FR-5.7 空闲回收 | DONE | `Runtime.ReapIdle` + `StartIdleReaper`（`PIFORGE_AGENT_IDLE_TTL`，默认 30 分钟）；有 run 在跑时不回收；`TestReapIdleStopsUnusedAgentsOnly`；e2e 用 45s TTL |
| FR-5.8 资源护栏（并发/CPU/内存/磁盘） | PARTIAL（磁盘预算已按沙箱落地；CPU/内存仍只有容器 provider 的启动参数） | **磁盘**：`-sandbox-disk-quota` / `PIFORGE_SANDBOX_DISK_QUOTA`（字节，默认 2 GiB，0 关闭）是每个沙箱 checkout 的预算，`internal/diskquota` 做 du 式遍历并缓存给管理页；发送消息前测量，超预算则**停止这次运行**（507 `sandbox_disk_quota`），中文文案写明上限、当前用量和该删什么（node_modules / 构建产物 / 大文件），并往会话事件里写一条 `platform.error`（转录与通知路径可见）；`GET /v1/admin/status` 新增 `sandbox_usage`（每个沙箱的 bytes / quota / over）与 `sandbox_disk_quota`，admin 页面在「运行时目录」卡片里逐个沙箱显示用量与百分比。**技能展开**：`-skill-max-bytes` / `PIFORGE_SKILL_MAX_BYTES`（默认 32 MiB）在一次导入的**实际解压字节**上生效，超过即整包拒绝（临时目录删掉，不写任何 skill 行）。**仍是 PARTIAL 的原因**：配额是「每次运行前」测量（周期性的、不是每次写入），所以单个 turn 可以超一次再被下一轮拦住；本地 provider 没有 CPU/内存限制，也没有把配额做成容器级 limit；`sandbox_usage` 的遍历有文件数上限（截断时 bytes 是下界，仍按「下界超了就是超了」判定）。见文末「本批：快照行生命周期 + 沙箱磁盘配额」。 |
| FR-5.9 手动暂停/唤醒/快照/重建 | PARTIAL（暂停/唤醒/状态有，快照与重建没有） | `GET …/sandbox`、`POST …/sandbox/pause`、`POST …/sandbox/resume`；暂停会停掉该沙箱上的 agent；会话页有状态 chip 与「唤醒」 |
| FR-5.10 沙箱日志（启动 / 镜像构建） | PARTIAL | `handleSessionLogs`（代码注释标 FR-5.10）返回的是 **agent stderr 尾巴**（`contracts.LogSource`），不是容器启动或镜像构建日志；`22-logs.spec.ts` 通过 |
| FR-6.1 虚拟化文件树（展开/折叠/搜索/新建/改名/删除） | PARTIAL | `file-tree.tsx` + `file-tree-canvas.tsx`（`react-arborist` `Tree`，懒加载 `files/tree?depth=`，客户端按名过滤，下载/删除）；**新建文件有两个入口**：空态按钮 `file-create`，以及树头部始终可用的 `file-create-header`（验收 pass 3：树非空时找不到新建入口，只能改用终端）；**无重命名**；测试：`03-workspace.spec.ts`、`smoke.test.tsx`、`e2e/tests/11-files.spec.ts` 的「the tree header creates a file when the tree already has files」（树非空时点头部按钮 → 填路径 → 文件真的出现在 Files API 的 tree 里；旧前端没有 `file-create-header`，见 `e2e/.artifacts/fix-provider-lifecycle/fail-first-fix3.log`） |
| FR-6.2 Git 状态色 M/A/D/U/! | PARTIAL | `contracts.FileNode.GitState` + `file-tree.tsx` `gitLetterMap` / `file-tree-canvas.tsx` 渲染字母与色点；无单独测试（`04-git.spec.ts` 只断言 status 接口） |
| FR-6.3 尊重 .gitignore + 显示忽略开关 | DONE | `fsx/ignore.go`、`handleFileTree` 的 `include_ignored=1`、`file-tree.tsx` 开关；`11-files.spec.ts` "an ignored file appears only when the switch is on"、`handlers_test.go` `TestFileTreeSwitchReachesTheServer` |
| FR-6.4 ⌘P 快速打开 | DONE | `quick-open.tsx`（`Ctrl/Meta+P`，debounce，`api.files.search(byContent=false)`）；`15-quick-open-status.spec.ts` 2 例 |
| FR-6.5 右键菜单（终端/复制路径/下载/对比 HEAD/删除） | PARTIAL | `file-tree-canvas.tsx` 的 `DropdownMenu`：在终端打开（`onRevealInTerminal`）、下载、删除；**缺复制路径、缺与 HEAD 对比** |
| FR-6.6 内容搜索 ⌘⇧F | DONE | 后端 `handleSearch`（`?type=content` + `fsx.Search`）本来就有；缺的是入口。`quick-open.tsx` 现在一个面板两种模式：⌘P 文件名、⌘⇧F 内容（`quick-open-mode` 的 `data-mode` 标明当前模式），内容模式走 `api.files.search(..., true)`，结果行显示 `path:line` 与匹配文本并带 `data-path`/`data-line`；点击经 `handleOpenLocation` 把行号交给 `FilePreview` → `CodePreview`（`data-active-line`、`第 N 行` 徽章、`scrollIntoView`）。Markdown 命中时改渲染源码视图——渲染视图无法指向第 N 行。测试：`e2e/tests/70-content-search.spec.ts` 两例（写 `docs/guide.md` 后按 Ctrl+Shift+F，模式为 content，结果 `docs/guide.md:3` 且含 token，点击后 `code-preview[data-active-line="3"]` 且屏幕上是该行；Ctrl+P 仍是文件名搜索，同一 token 查不到）。旧代码：面板里没有 `quick-open-mode`，⌘⇧F 不打开任何东西 |
| FR-7.1 文件预览与编辑 | PARTIAL（可编辑，无 CodeMirror） | `file-preview.tsx` 的 textarea 编辑模式（`preview-edit` / `file-editor-save`）；`31-file-edit.spec.ts`。没有语法高亮编辑器与大文件折叠 |
| FR-7.2 Markdown 渲染 + 源码切换 | PARTIAL | `preview/markdown-preview.tsx`（`Tabs` 渲染/源码 + `markdown-lite.tsx`）；无该组件的测试 |
| FR-7.3 图片预览（缩放/平移） | PARTIAL | `preview/image-preview.tsx`（按钮缩放、Ctrl/⌘+滚轮、拖拽平移）；无测试，AC-7.3 未验 |
| FR-7.4 PDF 预览 | PARTIAL | `file-preview.tsx` 用 `iframe src=inlineUrl`（注释明说不用 pdfjs），**无 canvas 渲染、无翻页/页码**；`19-preview.spec.ts` 只验 iframe 可见 |
| FR-7.5 CSV/TSV 表格 + 原文切换 | PARTIAL | `preview/table-preview.tsx`（`Tabs` table/raw）+ `lib/fs/csv.ts`；只有解析器单测 `csv.test.ts`，无组件测试 |
| FR-7.6 HTML iframe sandbox 预览 | PARTIAL | `preview/html-preview.tsx`（`sandbox=""`，脚本被全禁，与 PRD「若含 JS 走预览域名」不符）；无测试 |
| FR-7.7 二进制下载卡 | DONE | `preview/binary-card.tsx`（大小/类型/下载）+ `file-preview.tsx` 的 `preview-binary` 分支；`file-preview.test.tsx` "shows size and a download for a binary file, not an error"、`19-preview.spec.ts` "a binary file shows a download card instead of an error"；远程预览的二进制分类见 `workspace_preview_remote_test.go` |
| FR-7.8 大文件保护（2MB / 20MB） | DONE | `fsx.go` `DefaultMaxInline = 2<<20`、`DefaultMaxPreview = 20<<20`、首尾截断（`truncationMarker`）；`19-preview.spec.ts` "a download streams the whole file, not the preview window"、`fsx_test.go` |
| FR-7.9 Diff 视图 unified / split | PARTIAL | `git-diff.tsx` + `git-diff-impl.tsx`（`@git-diff-view/react`，`DiffModeEnum.Split/Unified`）；`04-git.spec.ts` 只验 patch 内容，切换未测 |
| FR-7.10 网页内改文件并落盘 | DONE | `PUT /v1/sessions/{id}/files` 有调用者了（`api.files.write`），保存后重新读取；`31-file-edit.spec.ts` 断言服务端字节已更新、取消不写 |
| FR-8.1 当前分支 / ahead-behind / 干净状态 | DONE | `gitx/status.go` + `handleGitStatus` + `git-data.tsx`/`git-panel.tsx` 状态头；`04-git.spec.ts` "the branch is reported…" |
| FR-8.2 变更列表（staged/unstaged/untracked 分组） | DONE | `git-changes.tsx`（三组 `ChangeGroup`）；`04-git.spec.ts` 断言 untracked 出现在 status |
| FR-8.3 提交（消息 + 选中文件 + amend） | PARTIAL | `git-panel.tsx` 提交框 + `amend` `Switch` + `stagedPaths`；`handleGitCommit` 支持 `amend`；`04-git.spec.ts` 覆盖提交与路径，**amend 无测试** |
| FR-8.4 分支操作（新建/切换/删除/合并/rebase） | PARTIAL | 只有 `POST …/git/branch`（`handleGitBranch`）与 `…/git/checkout`（`handleGitCheckout`）；`gitx/ops.go` 无 Merge/Rebase，无删除分支，无冲突指引 |
| FR-8.5 历史列表 + 点击看该 commit diff | PARTIAL | `git-history.tsx` 列表 + `git-commit-sheet.tsx`，但 sheet 注释明说「无法按 commit 取 diff」，只展示元数据；`api.git.diff` 不接受 commit 参数 |
| FR-8.6 Git 树（SVG DAG + ref 标签 + merge 线 + 点击详情） | PARTIAL | `git-graph.tsx`（`assignLanes`、28/18/4.5、ref 最多 3 个 + `+N`）+ `lib/git/lanes.test.ts` + `04-git.spec.ts`（`git-graph-row` 渲染）；无 HoverCard，点击只有元数据 sheet（无 diff） |
| FR-8.7 push / pull | DONE | `gitx/remote.go`（fast-forward only，分叉返回 `ErrDiverged` 并说明）；`GET /git/remote` 报 ahead/behind；`git-panel.tsx` 的推送/拉取按钮；`remote_test.go`（真实 bare 仓库）、`git_remote_test.go`、`34-push-pull.spec.ts` |
| FR-8.8 fetch/pull/push + 开 PR | DONE | fetch/pull/push：`gitx/remote.go` + `GET /git/remote`（`remote_test.go`、`git_remote_test.go`、`34-push-pull.spec.ts`）；开 PR：项目级 forge 配置 + `POST /v1/sessions/{id}/git/pr` 先推分支再创建，完整证据见 FR-12.6 行 |
| FR-9.1 跟随终端数据源（tool_execution_* partialResult） | DONE | `pi/normalize.go` `ToolProgressPayload` → `EvToolProgress`（tool_execution_update）、`PtyStreamPayload` → `EvPtyStream`；`normalize_test.go`、`web/lib/session/reduce.ts` 消费 `tool.progress` |
| FR-9.2 跟随终端 | DONE | `FollowTerminal` 在面板条「跟随」（`workspace-panels.tsx`），数据来自 `collectBashOutput` 的**累计文本**，按「已写入字符数」只追加新增部分；xterm 挂载前到达的字节先缓冲（命令开始后才打开 tab 也能补齐整段），超过 `capFollowText`（2000 行，超出 500 行才批量裁剪）重建尾部。数据源是事件流（`tool.progress`），`mergeToolChunk` 把 `partialResult` 的累计快照识别成替换而不是再次追加。测试：`follow.test.ts`（cap）、`reduce.test.ts`（累计快照与文本收集）、e2e `33-follow-terminal.spec.ts` 两例：命令**运行中**（`run-state=running`）跟随面板出现 ≥3 行、结束后 7 行且每行只出现一次；另一例在命令开始后打开跟随（第一个 tab）并由第二个浏览器 tab 跟随同一会话，两边最后都是同样 7 行。旧代码跑新断言：`Received: 1`（只显示第一行，永不前进）。 |
| FR-9.3 只读渲染（ansi / 只读 xterm） | PARTIAL | 工具输出走 `tool-trace.tsx` 的 `Trace` + `CodeBlock`（只读）；`FollowTerminal` 的 `readOnly` 终端从未挂载 |
| FR-9.4 命令重放（历史命令列表） | MISSING | 无命令历史面板/端点 |
| FR-9.5 长输出 >5000 行折叠 + 完整输出路径 | PARTIAL | `tool-trace.tsx` 可折叠输出；无 5000 行阈值，无 `fullOutputPath` 概念 |
| FR-9.6 多路跟随（每分支一个跟随 tab） | MISSING | 无跟随 tab 体系 |
| FR-10.1 完整 PTY + xterm（支持 TUI） | DONE | `term/manager.go`（真 PTY，`pty_unix.go`/`pty_windows.go`）+ `terminal-surface.tsx`（`@xterm/xterm`）；`03-workspace.spec.ts` "the terminal opens a real PTY in the sandbox"、"keystrokes reach the shell and output comes back"。**本批修掉「快速输入丢字」**：`use-pty.ts` 的 `send` 过去在 `readyState !== OPEN` 时直接 `return`（socket 还在握手，或断线退避中），于是这段时间的按键被静默丢弃；现在按 `send` 调用逐条排队、`onopen` 时按原顺序逐帧 flush（不合并、不延迟已打开路径），`terminate`/干净关闭/协议错误时清空队列，连接期间的 resize 也只保留最新一次并在打开后发送。同批让 PTY 环境无分页器：`process.go` 的 `terminalEnv()` 覆盖 `PAGER=cat`、`GIT_PAGER=cat`、`GIT_TERMINAL_PROMPT=0`、`TERM=xterm-256color`（替换而非追加：libc 的 getenv 先命中先生效的重复项）。测试：`web/lib/terminal/use-pty.test.ts` 4 例（连接中排队/按序逐帧、已打开零排队、重连退避中排队并 flush、`terminate` 丢弃）、e2e `90-terminal-input.spec.ts` 2 例（0ms 连发完整且 `EXIT=0`、CDP 把握手延迟 1.5s 时抢在 open 前输入仍完整执行）、`term/process_test.go` 3 例（覆盖而非重复、保留无关变量、TERM）、`manager_test.go` `TestGitLogReturnsToThePrompt`（真 PTY 里 `git log` 打印后下一条命令能跑，且没有 `(END)`） |
| FR-10.2 多 tab（≥5，重命名/关闭） | DONE | `terminal-tabs.tsx`：可新增/切换/关闭（≥5，上限 8），每个 tab 一个 `TerminalPane`，`tab` id 就是服务端的 PTY key（`data-pty-tab`）；切走的 tab 只隐藏不卸载，shell 与滚动缓冲都留着。关闭时先发 `{type:"close"}` 让服务端 `Term.Close`（掉 socket 只是重连语义，见 FR-10.3），再移除标签；双击标签改名只改 `label`，id 不变，所以改名不会换 shell；关掉最后一个会新开一个。`workspace-panels.tsx` 的「在终端中打开」跟随当前标签（`paneRef` 指向 active）。测试：`e2e/tests/71-terminal-tabs.spec.ts`（加到 5 个 tab、5 个 pane 的 `data-pty-tab` 各不相同且 `data-pty-status` 全为 `open`；在第 5 个 tab 键入 `echo five > tab-five.txt`、第 1 个键入 `echo one > tab-one.txt`，两个文件都能从 Files API 读到；改名「构建」后 `t1` 仍 `open`；关闭 `t1` 后剩 4 个 tab/pane，文件仍在）。**实现坑**：`nextNumber.current++` 被 React Compiler 编译成前置自增，第一个 tab 的编号从 3 开始，改成显式 `const n = ref.current; ref.current = n + 1` |
| FR-10.3 断线重连（offset + 60s 不丢） | DONE | `pty_handlers.go` `from_offset` + `pumpPTYToWS`；`term/ring.go` 2MB ring；`manager_test.go` `TestReadOffsetsAreStable`、`ring_test.go` 5 例。**输出**的重放本来就按 offset 续传；本批补上**输入**在重连期间不丢：`use-pty.ts` 在 socket 非 OPEN 时排队，下一次 `onopen` 按序 flush（见 FR-10.1 行），单测覆盖「退避 500ms 后新 socket 收到重连期间键入的 4 个字符」。 |
| FR-10.4 addon-fit + resize 同步 TIOCSWINSZ | DONE | `terminal-surface.tsx`（`AddonFit`）+ `use-pty.ts` 发 `{type:"resize"}` + `pty_handlers.go` `Term.Resize`；`manager_test.go` `TestResize` |
| FR-10.5 复制粘贴/搜索/清屏/字号 | PARTIAL | `terminal-pane.tsx` `Toolbar`：搜索（`addon-search`）、清屏、字号 ±；无复制/粘贴按钮（仅 xterm 选择） |
| FR-10.6 布局（右侧/底部/全屏/拖拽分割） | PARTIAL | 只有右侧面板内的 tab；`ResizablePanelGroup` 分的是会话流/检查器，不是把终端放到下方或全屏 |
| FR-10.7 沙箱暂停 → 终端变灰只读 + 唤醒后回放 | MISSING | 无暂停态 UI；`terminal-pane.tsx` 只有 socket 断开覆盖层，不是沙箱暂停 |
| FR-10.8 与 agent 共享文件系统 | DONE | 终端 cwd 取自 `worktreeRoot(sess)`，与 agent 的 `spec.Worktree` 同目录（`pty_handlers.go` vs `runtime.go spawn` `cfg.Dir`）；`03-workspace.spec.ts` |
| FR-11.1 画面投屏（screencast → WS → canvas） | DONE | `browser.go` `Screencast`/`Page.startScreencast` + `browser_handlers.go` + `browser-panel.tsx` canvas；`08-browser.spec.ts`、`browser_test.go` `TestScreencastForwardsFramesAndAcks`。并发打开面板的启动竞态由 `be01575` 修掉（每 sandbox 单飞、失败返回 typed `LaunchError`、devtools 断线视为死会话）：`TestConcurrentEnsureLaunchesASandboxOnce`、`TestTwoSandboxesLaunchInParallel`、`TestRealChromeConcurrentEnsureSharesOneBrowser`；部署实测同 tick 打开两个面板 socket 都拿到同一个 pid。**面板生命周期（`15b995c`）**：会话页不再挂载浏览器面板，只有选中「浏览器」面板（或把它放进第二窗格）才连接 screencast，服务端因此不会在打开会话时启动 Chrome；最后一个 viewer 离开后，`Manager.StartIdleReaper` 在 `PIFORGE_BROWSER_IDLE_TIMEOUT`（默认 60s）后停止该 sandbox 的浏览器，`Len()` / `SandboxIDs()` 归零，管理页 `browser_running` / `browser_idle_timeout` 可读。测试：`idle_test.go` `TestIdlePolicyStopsOnlyUnwatchedBrowsers`、`TestIdlePolicyStopsABrowserThatWasNeverWatched`、`TestConcurrentAcquireLaunchesOneBrowserPerSandbox`，e2e `67-browser-lazy.spec.ts` 断言「打开会话 20s 内没有浏览器进程，选中面板后才启动，切走后 60s（套件里 3s）内停止」。**边界**：这条 idle 策略只覆盖 host placement；远端 / AIO 沙箱里的 Chrome 仍不会因空闲被停止（见文末「交付能力」）。 |
| FR-11.2 观察模式（默认不可输入） | DONE | 默认 `observe` 成立；`browser_handlers.go` 在非 takeover 时对 `input` 命令直接 `break`（服务端判定，不靠面板自觉）。**本批补上断言**：e2e `72-observe-input.spec.ts` 起一个页面，把「最后收到的事件」写进 `document.title`，再从 `GET …/browser/tabs` 读回标题——不是读面板自己的模式按钮。observe 下点 canvas、按键后标题仍为 `observe-target`（等 1.5s 再断言，给错误转发的输入足够时间到达）；切到 takeover 后同一套点击/按键分别变成 `clicked` / `key-b`。旧代码没有行为差异可失败（守卫比测试早），所以用变异证明测试有效：删掉服务端 `browserModeOf(...) != takeover` 守卫后，同一条 spec 失败在 `Expected "observe-target" / Received "key-z"`（`e2e/.artifacts/partial-batch2/fr-11.2-mutation-fail.log`）。部署截图 `fr-11.2-observe-click-dropped.png`（观察，标题未变）与 `fr-11.2-takeover-click-delivered.png`（控制中，标题 `clicked`） |
| FR-11.3 接管模式（输入 + 暂停 agent 浏览器工具） | DONE | 接管现在是**独占租约**（`internal/browserlease`）：作用域是**沙箱**（一个沙箱一个 Chrome，两个会话看的是同一页），owner = 用户 + 会话，TTL 45s（`-browser-takeover-ttl` / `PIFORGE_BROWSER_TAKEOVER_TTL`），面板在接管期间按 TTL/3 发 `{"type":"heartbeat"}` 续租。第二个接管返回 **409 `browser_busy`**，消息与 details 写明持有人与剩余秒数（`holder` / `holder_session_id` / `remaining_seconds`）；`GET …/browser/mode` 在旧字段 `mode` 之外返回 `holder{owned_by_me, remaining_seconds}` 与 `ttl_seconds`。释放三条路：切回 observe 立即 release；断线后 TTL 过期（`Manager.Holder` 惰性清理，`effectiveBrowserMode` 把过期接管读成 observe）；「观察」按钮本身就是显式释放。**输入门禁同时查 mode 与租约**：过期或别人的租约即使还留着 takeover 字样，输入也照丢。UI：别人的租约显示「正在被 X 控制」（`browser-holder`，title 带剩余秒数）、接管按钮禁用；失败 toast 用服务端中文消息。测试：`browserlease/lease_test.go` 5 例（grant/refuse/expire/heartbeat/release，fake clock）、`httpapi/browser_takeover_test.go` 2 例（同沙箱两个会话：第一个 200、第二个 409 + 持有人 + 释放后成功；fake clock 过期后旧会话读回 observe 且第二个可接管）、e2e `83-browser-takeover.spec.ts`（第二个浏览器上下文看到「正在被 …」、API 409 `browser_busy`、释放后成功接管）。截图 `fr-11.3-controlling.png`、`fr-11.3-busy.png`、`fr-11.3-swapped.png`。**边界**：仓库里没有 Playwright MCP（FR-11.6 MISSING），没有 `mcp__playwright__*` 可拦；接管时仍照旧中断当前 run，锁覆盖的是「人和人」以及「人 vs 已经在跑的工具」。 |
| FR-11.4 模式互斥 + 「谁在控制」标识 + 确认 | DONE | `GET/POST …/browser/mode`；进入 takeover 前弹确认（说明会中断 agent），确认后中断当前运行；面板按钮显示「观察 / 控制中」；`browser_mode_test.go`、`43-browser-mode.spec.ts` |
| FR-11.5 地址栏 + 前进/后退/刷新 + 端口快捷入口 | PARTIAL | `browser-panel.tsx` 有 URL `Input` 与刷新/导航；无前进/后退，无端口列表下拉 |
| FR-11.6 agent 侧通过 Playwright MCP 驱动同一 chromium | MISSING | 无 Playwright MCP 预设或 CDP 端点下发（`materialize.mcpEntry` 只写用户自己填的 command/url） |
| FR-11.7 多 tab（≥3，tab 条 + 缩略图） | DONE | `browser/tabs.go` + `handleListBrowserTabs`/`handleOpenBrowserTab`/`handleCloseBrowserTab` + `browser-panel.tsx` tab 条；`26-browser-tabs.spec.ts`、`tabs_test.go` |
| FR-11.8 截图入会话附件（不再弄脏 checkout） | DONE | `9c4ab0b`：截图跟随面板当前激活的 tab（`Session.SetActiveTab` / `ScreenshotTab`），空图被拒并给出原因（`capture.go` `ErrBlankCapture`）。**本轮把落点移出仓库**：截图写 `<dataDir>/artifacts/<session>/screenshots/<ts>-<ms>.png`，响应虚拟路径 `artifacts/<session>/screenshots/…`，面板经新路由 `GET /v1/sessions/{id}/artifacts?path=…` 预览/下载；`git status` 不再出现 `screenshots/`，而旧会话里已在 checkout 内的 `screenshots/…` 仍走 `/files` 可预览（不迁移、不删除用户文件）。测试：`screenshot_test.go`（about:blank、纯色图、非 PNG、连续两次）、`real_chrome_test.go` `TestRealChromeScreenshotFollowsTheActiveTab`、`httpapi/artifacts_test.go` `TestScreenshotWritesOutsideTheCheckoutAndIsServable`（POST 后 checkout 零新增文件、artifacts 路由 200、legacy workspace 路径仍 200）；`e2e/tests/20-screenshot.spec.ts` 先导航到真实页面，再解码 PNG 断言 >8 种颜色、两次 API 截图后 `git/status.clean=true`、面板相机按钮预览 `src` 为 `/artifacts?...&inline=1`、空白 tab 返回 422 且不落文件 |
| FR-11.9 profile 持久化（sandbox 内 .piforge-browser） | DONE | `browser.go` `profile := filepath.Join(profileRoot, ".piforge-browser")` + `--user-data-dir`；`profile_test.go` `TestRealChromeProfileIsStableAcrossSessions`（边界已注明） |
| FR-12.1 Diff 作用域（工作区/已暂存/分支 vs 基准/提交范围） | DONE | 作用域是真的换 git 命令，不是换标签：`contracts.ScopeRange` + `DiffRequest.From/To`，`gitx.Diff`/`DiffPatch` 走 `git diff <from>..<to>`（两端各自过 `safeRevArg`，`..`、`-` 开头、空白一律拒绝，所以范围不会被拼成一个 revision），`GET /v1/sessions/{id}/git/diff?scope=range&from=HEAD~1&to=HEAD`，缺 `from` 由 handler 返回 400 `invalid_argument`。前端 `git-panel.tsx` 的 `SCOPES` 现在是 工作区/已暂存/分支/范围，`lib/git/diff-scope.ts` 负责拼请求；范围有 `git-range-from`/`git-range-to`/`git-range-apply` 两个输入，当前作用域用 `git-diff-scope` 徽章（带 `data-scope`）写在 diff 头上。历史作用域从自己的 diff 列文件（`git-scope-files`/`git-scope-file`），否则「某个提交改了但工作区干净」的文件根本点不开——而那正是 review 要看的。测试：`gitx/diff_range_test.go`（HEAD~1..HEAD 只有最后一次提交、HEAD~2..HEAD 两条都在、main..HEAD 在 main 上为空、路径过滤、两端校验）；`httpapi/git_diff_scopes_test.go` 走真实路由 + 真实 git 仓库断言同一批内容；`web/lib/git/diff-scope.test.ts` 断言作用域改变请求本身；e2e `76-diff-scopes.spec.ts` 断言 工作区/已暂存/HEAD~1..HEAD/HEAD~2..HEAD 四种内容互不相同。旧代码确实过不了：旧前端没有 `git-diff-scope`（`fr-12.1-fail-first-ui.log`），旧后端对 `scope=range` 返回 400、范围文件列表停在 0（`fr-12.1-fail-first-backend.log`）。剩余：PRD 里的「本轮」作用域仍没有入口（见文末），提交范围靠手输 from/to，没有提交选择器 |
| FR-12.2 逐文件 + 逐 hunk stage/unstage/revert | DONE | `gitx/hunks.go`（客户端只传 path+index，补丁由服务端重建）；`POST …/git/hunk`（stage/unstage/discard）；diff 视图里每个 hunk 一行（`git-hunk-row`，`data-index` 就是发给服务端的 index），工作区给「暂存」「丢弃」，已暂存给「取消暂存」，历史范围显示 `git-hunk-readonly` 且不画按钮——提交里已有的 hunk 不能暂存，按钮能点却必然失败比没有更糟。逐文件仍是每行「丢弃」（带确认）。测试：`hunks_test.go`；`httpapi/git_diff_scopes_test.go` 从 diff 视图的路径做完整一轮（工作区两 hunk → 暂存 index 1 → 索引里只剩第二条、工作区只剩第一条 → 取消暂存后索引为空、两条都回到工作区）；e2e `76-diff-scopes.spec.ts` 在页面上点 `git-hunk-stage`/`git-hunk-unstage` 断言同样的结果。**顺带修掉的坑**：提交原来把 `stagedPaths` 再交给后端 `git add` 一遍，于是「只暂存一个 hunk 再提交」会把另一个未暂存的 hunk 一起带走；现在 `git-panel.tsx` 用 `paths: []` 提交索引原样内容（`gitx.Commit` 的 paths 语义没动，避免影响其它调用方），76 在第一次提交后断言仍有一行未暂存且内容是另一条改动 |
| FR-12.3 行内评论 → 作为 steer 发给 agent | DONE | 在 diff 的某一行（以及文件预览的某一行）上写评论：`patch-view.tsx` 每行一个 `diff-line-comment`（行号来自 `@@` 头，见 `lib/git/patch-lines.ts`），`code-preview.tsx` 每行一个 `file-line-comment`。评论先存成草稿（`lib/review/comments.ts`：localStorage、按会话分区、键 `piforge.review.drafts.v1`，重载后仍在），diff 里有标记与计数（`review-comment-row` / `diff-line-marker` / `diff-comment-count`）；发送走**同一个** `POST /v1/sessions/{id}/messages`，带 `review_comment{path,line,side,hunk,quote,body}`，run 进行中时 `streaming_behavior:"steer"`。**消息正文由服务端拼**（`internal/httpapi/review_comment.go`）：`行内评论 · <path>:<line>` + diff 片段 + 逐行引用（` 42 \| 代码`）+ `评论：…` + 位置说明；服务端拿工作区里**当前**的文件核对引用，所以「行挪了」有明确语义（见文末「FR-12.3 的设计与证据」）。测试：`review_comment_test.go`（引用/路径行号/挪行/删除行/读不到文件/校验，以及两条走真实路由断言 agent 收到的 prompt 含 path:line 与引用）、`lib/review/comments.test.ts` 9 例、`lib/git/patch-lines.test.ts` 8 例、e2e `77-inline-comment.spec.ts`（行上评论 → 保存 → 标记/计数 → 重载草稿仍在 → 发送 → 草稿清除 → transcript 的 `turn-user` 含 `行内评论 · f.txt:3`、被引用的代码与评论正文）。**渲染器换了**：`@git-diff-view/react` 在这个构建里 `DiffUnifiedView` 抛错、错误边界一直回落到原始 patch 文本（一行都点不了），所以 diff 现在是自己的逐行渲染器，保留原 testid（`git-diff-view`/`git-diff-patch`）与 `+`/`-` 标记；split 用同 hunk 内顺序配对。**本批补上可发现性（LOW–MEDIUM）**：入口原来是 `invisible … opacity-0`、只有 hover 才出现（16×16），验收方只能靠翻 DOM 找到；现在是常驻 `opacity-60`（hover/focus 全亮、图标 14px），e2e `77-inline-comment.spec.ts` 在 hover 之前断言 `toHaveCount` 可见、`getComputedStyle().opacity > 0` 且 `visibility !== hidden`，然后才 hover 点击；旧样式跑该 spec 失败在 `visibility: hidden`（`e2e/.artifacts/fix-truth/fix3-failfirst.log`） |
| FR-12.4 变更概览（增删行数 + 风险标记） | DONE | `GitStatus` 新增 `additions`/`deletions`，每个 `GitChange` 新增 `additions`/`deletions`/`risk`（`lockfile` / `ci`）。统计相对 HEAD 合并已暂存与未暂存：`git diff --numstat -z HEAD`（无 HEAD 时分别数 index 与工作区），重命名取新路径，未跟踪文件按行数计入（二进制/超过 4MB 记 0）。风险在 `gitx` 判定：`.github/workflows/`、`.circleci/`、`.buildkite/` 与常见 CI 文件为 `ci`，`go.sum`、`package-lock.json`、`pnpm-lock.yaml` 等为 `lockfile`。`git status` 改用 `--untracked-files=all`：用 `normal` 时新 `.github/workflows/ci.yml` 只显示成 `.github/` 一行，行数与风险都挂不到文件上。UI 每行显示 `+N/−N`（`git-line-counts`）与「锁文件」/「CI」徽章（`git-risk`），表头 `git-change-totals`。测试：`TestStatusCountsLinesAndFlagsRisk`（a.txt +1、b.txt −2、new.txt +2、package-lock +2/锁文件、ci.yml +1/CI，合计 +6 −2）与未跟踪分支计数；e2e `73-git-overview.spec.ts` 先断言 API 的 `+6 −1` 与逐行字段，再断言页面上的同样数字与徽章。旧代码：后端没有这些字段，spec 失败在 `expect(status.additions).toBe(6)`；只换回旧前端则失败在 `getByTestId('git-change-totals')` element not found（`fr-12.4-fail-first.log`、`fr-12.4-fail-first-ui.log`）。部署截图 `fr-12.4-change-overview.png` |
| FR-12.5 测试结果面板 | DONE | `717f7cb`：`internal/testrun` 解析 go test / vitest / jest / pytest 输出（含 `race detected`、`file:line`），`GET /v1/sessions/{id}/test-runs` 从工具事件与终端两条来源入库，`test-panel.tsx` 有失败/通过徽章与逐条失败展开。测试：`testrun/parse_test.go`、`store/testruns_test.go`、`httpapi/test_run_handlers_test.go`；`e2e/tests/62-test-panel.spec.ts` 断言空态、失败徽章与 `TestCacheEvict` / `cache_test.go:42`、随后同一会话变成通过徽章、刷新后仍有两条且 API 与面板一致。**本批修掉验收的真实漏检（MEDIUM）**：agent 写 `primes.test.js` 后跑的是 `node primes.js && node primes.test.js`，没有 `--test` 标志，`Detect` 只认标志，于是面板一直空着。工具事件这条链路本身是通的（同一个 agent 跑 `node --test` 就被记成 4/4 通过，部署实测），所以补的是检测：`testrun/runner.go` 的 `nodeTestScript` 按 node 自己发现测试文件的命名约定（`*.test.*`、`*.spec.*`、`*-test.*`、`*_test.*`、`test-*.*`、`test.*`，脚本扩展名限定）识别 `node x.test.js`，`tsx` 同理；`node primes.js`、`node scripts/check.js`、`node --version` 仍然不算。空态文案同时改成「只识别这些命令」并点名 `node --test` / `node x.test.js`，以及「其他脚本不会被识别，也不会编造通过或失败」。测试：`testrun/parse_test.go` 的 `TestDetect` 新增 9 条（含 `node primes.js && node primes.test.js`、带 `--require/--env-file` 前缀、反例）与 `TestParseTAP` 的「agent 直接跑测试文件」用例（node 22 在沙箱里打印的真实 TAP 字节）；`test-panel.test.tsx` 断言空态点名 `node x.test.js` 与 `./run-tests.sh` 的边界；fake peer 新增 `[[node-test-file]]`（命令与 TAP 输出就是 node 22 对 `node primes.test.js` 的真实形状），e2e `86-node-test-file.spec.ts` 断言面板出现 `data-runner="node --test"`、命令 `node primes.test.js`、`✅ 2`，且 API 同一行。旧检测跑该 spec 失败在 `test-tab-badge` element not found（`e2e/.artifacts/fix-truth/fix1-failfirst.log`） |
| FR-12.6 一键开 PR | DONE | `internal/forge` 是通用「创建 PR」客户端，不是 GitHub 专用：**github 方言**（GitHub / GitHub Enterprise / Gitea / Forgejo 共用 `POST {api}/repos/{owner}/{repo}/pulls` + `Authorization: Bearer`）与 **gitlab 方言**（`PRIVATE-TOKEN` + `merge_requests`）。**配置是项目级**（`PUT /v1/projects/{id}/forge`：kind / API base / repo / 基准分支），token 用与 provider key 同一个 `crypto.Box` AES-GCM 封存进 `projects.forge_token_cipher`，只写不回显（API 只给 `has_token`）。**为什么不是全局 token**：token 同时代表一个人和「他能写哪些仓库」，全局 token 会让所有项目的 PR 都以同一身份开出，包括 token 主人没打算碰的仓库；代价是每个项目多填一次。流程：变更面板「创建 Pull Request」→ `GET …/git/pr` 预览（分支、基准、精确提交数 + 最近 50 条提交、**三点 diff** 的文件与增删行、脏工作区/落后/无远端警告、标题取会话第一条用户消息并回退到会话标题、正文取最新提交 subject、已有 PR 从本地回执或 forge 查询得到）→ `POST …/git/pr` 先 `git push -u origin <branch>` 再创建。失败按步返回 `details.step=config|push|create`：无远端 400、非快进 409（`ErrPushRejected`/`ErrNoCredentials` 由 git stderr 分类，原始 stderr 保留）、forge 4xx 原状态+原文、5xx→502；不会把 forge 的 422 说成成功。幂等：先 `FindOpen(head,base)`，POST 撞 422/409 再查一次，已有就返回 existing 且 `created=false`；成功写 `pull_requests` 回执（刷新后仍显示链接）与 `pr.created` 事件。UI 里也能首次配置 forge（token 只写一次）。测试：`forge/forge_test.go`（fake forge 断言 Authorization、title/body/head/base、query 无 token、已有返回、422/409 恢复、GitLab 路径/头/字段、配置校验）、`gitx/pr_test.go`（真实仓库：2 提交、改名、三点 diff 不把基准后来新增的提交算成反向改动、缺基准）、`gitx/push_reject_test.go`（真实非快进 + stderr 分类表）、`httpapi/pr_test.go`（真实 bare 仓库 push 后创建、forge 收到的请求形状、无远端、非快进、forge 422 原文 + step、配置缺失、token 封存/不回显/不进日志、5xx→502）、`web/lib/git/pr.test.ts`（步骤/汇总文案）；部署实测 `e2e/scripts/fr126-verify.mjs`（fake forge 经 `host.docker.internal` 指向容器，bare 仓库在 `/data` 里，走真实 UI）：截图 `e2e/.artifacts/fr126/fr126-preview.png`、`fr126-created.png`、`fr126-existing.png`、`fr126-error.png`，日志 `e2e/.artifacts/fr126/probe-log.json` |
| FR-12.7 产物交付（present 为下载/预览卡） | PARTIAL | 预览区与 `binary-card.tsx` 提供下载/预览；没有「present 任意产物」的语义（截图现在写入 `<dataDir>/artifacts/<session>/screenshots` 并由 artifacts 路由预览，不再写入 worktree） |
| FR-13.1 admin 共享 provider + Inherited 徽章 | DONE | `handleAdminUpsertProvider`/`handleAdminListProviders` + `handleListProviders` 合并标 `inherited`；`12-shared-providers.spec.ts`、`admin_management_test.go` `TestAdminSharesAProviderThatUsersInherit` |
| FR-13.2 用户私有 provider + 同名 shadow | DONE | `handleCreateProvider`/`handleUpdateProvider` + 按名合并；`handlers_test.go` `TestProvidersMergeAndShadow` |
| FR-13.3 key AES-GCM 存库 + 沙箱内无明文 | DONE | `crypto.Box` 封存（`handleCreateProvider` 拒绝无 Box 的写入）+ `llmgw` 短期 token 注入环境（`piforged/main.go` `setupFor` 的 `ANTHROPIC_API_KEY=token`）；`TestProviderKeysAreSealedAtRest`、`gateway_test.go` `TestGatewayInjectsAnthropicKeyAndStripsTheEphemeralOne` |
| FR-13.4 模型选择器（按 provider 分组 + 窗口/推理/成本） | PARTIAL | `workbench.tsx` 构造扁平 `provider/model` 选项，`composer.tsx` 单层 `Select`；不显示 context window / reasoning / 成本（这些字段在 settings 页有） |
| FR-13.5 推理强度随模型能力动态显示 | PARTIAL | 前端写死 `THINKING_OPTIONS`（5 档），后端只有 `thinking_level` 列与 PATCH；`pi/runtime.go` 从不发 `set_thinking_level`，也没有 `get_available_thinking_levels`（只有 `pitest/serve.go` 的假实现） |
| FR-13.6 会话级模型覆盖 | DONE | `composer.tsx` `persistSetting({model_ref})` → `PATCH` + `handlePostMessage` 的 `ModelRef`；`10-models.spec.ts` "a custom model id typed in the composer is what the session uses"。**本批补上失效引用的生命周期**：`session_models.go` 在每次发送前重解析 `model_ref`，provider 被删除/停用时改用账号当前的默认或第一个可用 provider 并把新引用写回 session，`PromptResult.model_ref` / `model_fallback_from` 告诉前端本轮实际用了哪个；provider 存在但没有 key 仍然按原规则报 `no_model`（不静默换模型）。测试：`session_model_lifecycle_test.go` 三例 + `81-provider-lifecycle.spec.ts` 两例；旧后端实测 `400 no_model` 且 session 行仍指着已删 provider（`e2e/.artifacts/fix-provider-lifecycle/lifecycle-api-OLD.json`） |
| FR-13.7 连通性测试 | DONE | `provider_probe.go` + `POST /v1/providers/{id}/test`；`provider_probe_test.go` 4 例、`06-providers.spec.ts` |
| FR-13.8 用量显示（token + 估算成本） | PARTIAL | `SessionStats.TokensIn/Out/CostUSD` + `ContextMeter`；无测试断言成本显示 |
| FR-13.9 用户 API Key（pf_…） | DONE | `api_key_handlers.go` + `api_keys.go` + Bearer 分支（`router.go` `authed`/`mutatingBearer`）；`24-api-keys.spec.ts`、`api_keys_test.go` |
| FR-14.1 两层作用域（admin 共享 + 用户私有，同名覆盖） | DONE | 两种作用域都能创建（`POST /v1/skills` 的 `scope`；非管理员建共享 403），共享行对每个用户可见且不可改。**本批补齐「同名覆盖」并把它定义成规则**（`backend/internal/skills/resolve.go`）：① 停用的行既不挂载也不遮蔽（停用 = 没配置）；② 同名时 `user` 作用域覆盖 `admin`；③ 同作用域多版本取最高版本（数字比较），再按创建时间、id 兜底；④ 名字 trim + 小写比较，因为挂载目录是 `materialize` 的小写 slug。结果：`provision.mountTooling` 只把生效行交给 `materialize.Mount`，停用私有行后共享行重新生效（回退，不是消失）；`product` 侧 `materialize.Mount` 也保留「同名目录 private 优先」的兜底，防调用方绕过解析器。API `GET /v1/skills` 给每行加 `effective`（每调用者计算，不落库）。会话记录：`session_skills` 由生效行生成，绝不因 SQL 排序/遍历顺序而记错（FR-14.5）。UI：设置页同名两行不再无法区分——生效行「生效中」，被遮蔽的共享行「已被同名私有 Skill 覆盖」并解释「停用会回退」，停用的私有行显示「使用共享版本 vX」；`/` 面板按名字只列生效行（一条 `/skill:name`），私有覆盖时标注「覆盖同名共享 Skill」。测试：`skills/resolve_test.go` 5 例（private 覆盖 shared 且与顺序无关、停用回退、两个用户各自解析、2.10.0 > 2.9.0、大小写同名）、`provision_test.go` `TestMountUsesThePrivateSkillAndFallsBackWhenDisabled`（真的读 `.pi/skills/review/SKILL.md` 与 `session_skills`，再停用私有行后读回共享内容）、`materialize_test.go` `TestMountPrefersThePrivateBundleWhenTwoScopesShareAName`（两种传入顺序）、`e2e/tests/75-skill-shadow.spec.ts`（fresh 用户建同名私有 → 设置页徽章、`/` 面板只有生效行、新会话挂载私有 bundle 且记录 private id；停用后新会话挂载共享 bundle 且记录 shared id、列表说明回退）。**旧行为**：挂载选私有只是 `ListSkills` 的 `ORDER BY scope` + map 覆盖的副作用，没有规则、没有 `effective`、UI 两行完全相同、`/` 面板同时列两行且版本对不上；部署实例实测 `effective` 缺失、两行无任何提示（`e2e/.artifacts/fix-palette-shadow/probe-log.json` 旧构建 8/18 失败）。 |
| FR-14.2 挂载到 worktree（只读 bind mount + content_hash） | PARTIAL | **机制按运行时能力显式选择并记录**（`materialize/mount_plan.go` 的 `PlanFor`，判定表在 `mount_plan_test.go`）：① 非 Linux（Windows/macOS）→ `copy-ro`；② Linux 且进程 `CapEff` 有 `CAP_SYS_ADMIN` → `bind-ro`（先把 bundle 复制成**该会话的快照**，再把快照 bind 到 `.pi/skills/<slug>` 并 remount `MS_RDONLY`）；③ Linux 没有 `CAP_SYS_ADMIN` → `copy-ro`（文件 0444、目录 0555；Windows 上映射为 `FILE_ATTRIBUTE_READONLY`）；④ 只有 copy 之后「以写方式打开 `SKILL.md`」探测成功时才记 `copy`（冻结但非只读）。`copy` 不是计划而是探测结果，所以无能力的默认部署不会再落到它。**默认部署不需要 SYS_ADMIN**：`copy-ro` 不需要任何 capability；`docker-compose.yml` 不 `cap_add`，而是 `cap_drop: [DAC_OVERRIDE]`（控制面以 root 运行，root 带该 capability 会忽略 0444；丢它是减权限，控制面写的每个文件都是自己的）。**为什么必须由容器丢**：Linux 只允许线程改自己的 capability 集（对别的 tid 调 `capset` 返回 `EPERM`），Go 的 goroutine 跑在多线程上，进程内丢只能覆盖一条线程——实测过，不采用。忘了丢时产品照常启动，但启动日志与徽章会诚实报 `copy`（并提示加 `--cap-drop`）。徽章 tooltip/`aria-label` 是一句可访问说明：「只读挂载：agent 无法改写技能」/「只读副本：agent 无法覆盖，但可新建文件」。**快照生命周期**（`internal/skillsnap` + janitor）：快照目录名就是会话 id（`ses_…`；旧布局 `ses-…` 也认，升级时不会误删在跑会话的目录）；删会话 → 删该目录；删项目 → 项目**所有**会话（含分支会话，因为分支有自己的 worktree 和快照）的目录，id 在级联删除前读出；janitor 在启动与每个 `-sandbox-sweep` 周期扫「名字像会话、数据库里没有这个会话」的目录，删除前对目录及其子目录 `umount(MNT_DETACH)`（还在挂载会 EBUSY），单个失败只 warn 并等下一轮，不中断整轮；一行日志给出 `count` / `bytes` / `freed`。**顺带修掉的两个同源 bug**：`sandbox.removeTreeWritable` 的 `unmountSkills` 现在递归到 `worktrees/<slug>/.pi/skills`（只认主 checkout 时，带分支会话的项目删除会因为分支 worktree 的挂载返回 409）；`provision.EnsureWorktree` 对已 attach 的 worktree 也重跑投影（容器重启后挂载点只剩空目录，否则该会话永远没有 skill；投影本身幂等，仍在冻结语义内）。测试：`materialize/mount_plan_test.go`（**决策顺序表**：有/无 capability、各平台；无能力时不尝试 bind、不建快照、机制为 copy-ro；copy-ro 写入被拒且源不变；bind 源是快照；bind 被拒 → copy-ro + warning）、`skillsnap/skillsnap_test.go`（命名/边界/先 detach 后删/清只读位/两种布局/拒删外来目录）、`janitor/janitor_test.go`（只删无会话的快照、旧布局算活着、先 detach、单个失败不中断且下一轮重试、会话索引失败时拒绝清扫、外来目录不动、日志含 count/bytes、dry-run）、`httpapi/skill_snapshot_test.go`（删会话删自己的、不碰别人的；删项目删主+分支；被拒的项目删除保留快照）、`provision/skill_mount_test.go`（挂载路径 ≠ 源路径且只读；编辑后同会话不变、新会话变；sandbox Exec 写被拒；**投影丢失后下一次 provision 修回来**）、e2e `84-skill-mount.spec.ts`（API 报机制、写被拒、UI 徽章、冻结）。**部署实测（无 SYS_ADMIN）**：`pf-local` 的 `CapAdd=null`、`CapDrop=["CAP_DAC_OVERRIDE"]`、`CapEff=0xa80425f9` 无 bit21/bit1；机制 `copy-ro`、API 写入 500 且内容不变、该会话没有快照目录、UI 徽章「只读副本」+ tooltip 原文；同一镜像加 `--cap-add SYS_ADMIN` 的临时容器报 `bind-ro`，删会话/删项目（含分支）都在 10s 内清掉快照目录。**仍为 PARTIAL 的原因**：Windows 上 copy-ro 只拒绝覆盖/删除已有文件，同一账号仍可在目录里新建文件（没有目录级不可变，不假装）；`content_hash` 仍在 `session_skills` 记录里，没有写进挂载树。 |
| FR-14.3 Skill 中心（浏览/搜索/详情/SKILL.md 渲染） | PARTIAL | `web/app/settings/skills/page.tsx`：列表/新建/删除/上传（含 `SKILL.md` 文本域）；无搜索框、无详情页（不渲染文件树与脚本预览） |
| FR-14.4 一键安装（Git URL / zip）+ 可禁用 | DONE | `POST /v1/skills/import` 克隆仓库、按含 SKILL.md 的目录逐个导入、只取该目录下文件、4 MB 上限；`skill_import_test.go`、`38-skill-import.spec.ts`。**zip 与「可禁用」本轮补齐**：`POST /v1/skills/import/zip`（multipart `file`，8 MB 请求上限，`archive/zip` 解到临时目录后走同一个 `findSkills`；`../`、绝对路径、符号链接条目整包拒绝，解压总量 4 MB）；`PATCH /v1/skills/{id}` 改 `enabled`（共享 Skill 仅管理员）；`provision.mountTooling` 只投影 `Enabled` 的 Skill，停用后新建会话真的不挂载。UI：设置 → Skill 中心「导入 Skill」对话框有 zip 选择与「上传 zip」，每行有启用开关。测试：`e2e/tests/69-skill-zip.spec.ts` 三例（设置页上传 zip → 列表出现 pdf-tools/review 且描述正确、bundle 只含 `SKILL.md`+`scripts/split.py`、启用时新会话挂载两者、停用后新会话只挂 review；`../escape` 整包 400 且不留半成品；无 SKILL.md 的包 404）；`skill_zip_test.go` 5 例（解压+findSkills、路径穿越、绝对路径/符号链接、超大条目、非 zip）。旧代码：该路由返回 `no route for POST /v1/skills/import/zip`，设置页没有 `skill-import-zip`。**本次修复（导入重启用）**：替换一个**已停用** Skill 的 bundle 时同时把它重新启用，并在响应里回报 `enabled` / `reenabled`，UI 的 toast 写明「之前是停用状态，已重新启用」——旧行为是内容被替换、行仍是 `enabled=0`、界面报成功而新会话什么都不挂载（导出 `e2e/.artifacts/fix-orphans/fix2-fail-first.log`：旧代码 `Enabled:false Reenabled:false`）。同一次改动让导入**不覆盖**同名共享 Skill（`SkillEditable` 先判权限再写文件，返回 `skipped` + 原因），此前普通用户导入同名共享 Skill 会先把磁盘上的共享 bundle 换掉、再因 `UpdateSkillBundle` 失败被标记跳过。测试：`skill_import_reenable_test.go`（停用 → 导入同名 → 响应 `enabled=true, reenabled=true` → 行一致 → 新会话真的挂载新内容）、`69-skill-zip.spec.ts` 补了「再次导入后重新启用并在新会话里挂载」；`e2e/tests/38-skill-import.spec.ts` 与 69 的旧注释（「导入不会重新启用」）已改 |
| FR-14.5 会话记录所用 skill@version | DONE | 挂载时把 `skill_id/name/version + 内容 sha256 + mounted_path` 写进 `session_skills`（`store/session_skills.go`，`ON CONFLICT DO NOTHING` ⇒ 写一次即事实，之后编辑 Skill 不改写）；hash 由 `materialize.Digest` 对挂载目录「路径+内容」排序计算，与上传路径的 hash 同算法；`GET /v1/sessions/{id}/skills`；日志/信息抽屉新增只读「本会话使用的 Skill」；侧车复制父会话记录；`TestDigestIsContentAddressed`、`TestSessionSkillRecordIsWriteOnce`、`SessionSkillsEndpoint`、`63-skill-version.spec.ts`。**额外修复**：Pi 只在信任项目后才加载 `.pi/skills`，此前 agent 实际从未被告知挂载的 skill；现在启动参数显式带 `--skill <worktree>/.pi/skills`（`sessionSpec.SkillsDir`），容器实测系统提示词含 `<skills>` 列表且模型按 skill 作答 |
| FR-14.6 `/skill:name` + `/` 命令面板 | DONE | 输入框开头 `/` 打开 skill 面板（`skill-palette.tsx`，`role=listbox` + `aria-activedescendant`）；输入过滤、↑↓ 选择、Enter 插入 `/skill:name `、Esc 关闭且不中断 run；无 skill 时空态链接 Skill 中心；插入的文本走既有消息路径（不是第二套调用机制），Pi 把 `/skill:name` 展开成 `<skill name … location …>` 并加载 SKILL.md；`skill-palette.test.tsx`（6 例）、`64-skill-palette.spec.ts` |
| FR-14.7 内置开箱可用 skill | DONE | `store/skill_seed.go` 三个内置技能（code-review / test-gap / explain-codebase），admin 共享、首次启动安装、已存在则不动；`skill_seed_test.go`、`39-builtin-skills.spec.ts` |
| FR-15.1 MCP server 配置 | DONE | `pimodel.WriteMCP` 在启动时写 `<agent dir>/mcp.json`；`mcpEntriesFor` 汇总用户与共享 server；`mcp_test.go`（形状取自镜像里的 `pi mcp add`）；容器实测 `pi mcp list` 认出了写入的 server |
| FR-15.2 MCP 凭据加密 + 注入 | DONE | AES-GCM 落库（`store/secrets.go`）；启动时解密并经 `ParseMCPSecrets` 写成 `env`（不进命令行）；`secrets_test.go`、`mcp_test.go`；容器实测 mcp.json 内含 env |
| FR-15.3 OAuth 代办 | MISSING | 无 `POST /v1/mcp/{id}/oauth/start`，无回调路由 |
| FR-15.4 曝光配置（server + 按工具通配符） | PARTIAL | `MCPServer.Exposure` 会写进 `.mcp.json`（`mcpEntry`）；无按工具 `tool_overrides`，UI 只有自由文本 |
| FR-15.5 状态徽章（Connected/Needs auth/Failed/cached） | MISSING | 契约里没有健康字段，`settings/mcp` 不显示状态，也没有探测 |
| FR-15.6 一键导入 mcpServers JSON | MISSING | 无 `POST /v1/mcp/import`，无粘贴导入 UI |
| FR-15.7 内置预设（Playwright/Filesystem/GitHub/Context7） | MISSING | 无预设表/预设按钮 |
| FR-15.8 工具列表（名称/描述/注解/逐个禁用） | MISSING | 无 MCP 工具枚举端点与界面 |
| FR-16.1 用户管理（创建/禁用/重置密码/角色） | DONE | `admin_handlers.go` `handleAdminCreateUser`/`handleAdminUpdateUser`；`09-admin-users.spec.ts` 5 例、`admin_management_test.go` |
| FR-16.2 共享 provider 管理 | DONE | `handleAdminUpsertProvider`/`handleAdminDeleteProvider` + `admin/providers` 页；`12-shared-providers.spec.ts` |
| FR-16.3 共享 skill 管理 | DONE | 三件事管理员都能做：**建**（`POST /v1/skills` 带 `scope=admin`，非管理员 403；UI 新建对话框对管理员显示「共享给所有用户」开关）、**删**（`DELETE /v1/skills/{id}` → `DeleteSharedSkill`，非管理员 403）、**开关**（`PATCH` → `SetSharedSkillEnabled`，任意管理员，不只创建者）。共享 Skill 对每个用户可见（列表与 `/skill:` 面板），普通用户只能看。路由仍挂在 `/v1/skills` 上按角色区分，没有单独的 `/v1/admin/skills`，也没有单独的「共享 Skill」管理页——权限在服务端，界面是同一个 Skill 中心。测试：`skill_scope_test.go` 两例、`74-shared-skill-create.spec.ts`。**边界**：本次没有开放「管理员事后改共享 Skill 的正文」——`PUT /v1/skills/{id}/bundle` 仍只允许私有 Skill，正文在创建（或创建者本人导入）时写入；需要改内容目前只能删了重建 |
| FR-16.4 镜像管理 | MISSING | 无镜像版本/构建状态端点或页面 |
| FR-16.5 运行状态（在线用户/活跃沙箱/节点水位/镜像告警） | PARTIAL | `handleAdminStatus` 返回 users/sessions/sandboxes/providers/skills 计数（`admin_status_test.go`、`15-quick-open-status.spec.ts`）；无节点水位、无镜像拉取失败告警。**本批修掉一个误导数字（LOW–MEDIUM）**：`agents_running` 数的是活进程（`hub.liveCount`），所以打开一个空闲 fork 就从 1 变 2，而页面上写的却是「运行中的 agent」。现在分成两个数：`runs_running`（`hub.runsInFlight`，逐会话问运行时的 Running/Streaming，即真正在跑的一轮）与 `agents_live`（活进程，空闲也算；`agents_running` 保留为同值别名）。页面用 `lib/admin/status-metrics.ts` 的 `agentMetrics` 渲染「进行中的运行」与「agent 进程」两张卡，各自的 hint 写明含义；`status-metrics.test.ts` 断言两个 label/值与「空闲的 agent 进程不算」，e2e 15 断言两张卡都在、旧 `stat-agents` 已不存在 |
| FR-16.6 资源护栏配置 | MISSING | 无配置端点/页面（与 FR-5.8 同源缺口） |
| FR-16.7 功能开关（按用户灰度） | DONE | `feature_handlers.go` + `feature_flags` 表 + 服务端强制（`featureDenied`）；`17-feature-flags.spec.ts`、`feature_test.go` |
| FR-16.8 支持模式（只读 + 通知留痕） | DONE | `support_handlers.go` + `support_access` 表 + `support-notices`；`16-support-mode.spec.ts`、`support_test.go` 2 例 |

## 交付能力（非 FR，单独计）

PRD 没有这些条目的 FR 行，但它们是演练或真实事故暴露、已修复、且有用户可见证据的交付能力。放在这里而不是塞进 FR 表，是为了不把「产品需求覆盖」和「交付流程」混成同一个数字。

| 能力 | 状态 | 证据 |
|---|---|---|
| 首次运行 setup：`GET /v1/setup` + 首次运行面板 + `PIFORGE_BOOTSTRAP_PROVIDER` 预填（`ba532a2`） | DONE | `httpapi/setup_handlers.go` 的 `GET /v1/setup` 返回 `needs_provider` / `is_admin` / `shared_provider_available` / 非密 `bootstrap`（name/base_url/model，没有 key 字段）；`web/components/setup/first-run-setup.tsx` 在首页与会话输入框上方显示「先配置一个模型服务」，按角色提供「添加共享服务商」或设置页链接；`bootstrap.Ensure` 只在「没有可用共享服务商」时创建，重启不覆盖；启动环境持有 key，日志不打印。**本批补上默认服务商守卫（验收 pass 3，MEDIUM）**：`PIFORGE_DEFAULT_PROVIDER=volc` 指向不存在的 provider 时，启动日志有一条 WARN（`cmd/piforged/default_provider.go`，非致命，写明后果是「跟随默认的会话会 400 no_model」），`GET /v1/setup` 返回 `default_provider` 与 `default_provider_missing`，面板据此显示「默认服务商「volc」不存在，请添加或更改默认值」。测试：`setup_test.go` `TestSetupReportsAMissingDefaultProvider`（缺失→true；加回→false；停用→true）、`default_provider_test.go`；旧代码跑该测试失败在 `default_provider = ""`（`e2e/.artifacts/fix-provider-lifecycle/fail-first.log`） |
| 会话在 provider 变更后自动恢复（验收 pass 3，HIGH；PRD 没有单独 FR 行，属 FR-13.6 的生命周期面） | DONE | 旧行为：会话的 `model_ref` 指向已删除的 provider 时，每次发送都是 500（server log `pi: command prompt failed: No API key found for the selected model`），UI 只说「服务端内部错误」，重试无用，只能新建会话；并且一个在 provider 缺失期间启动的 agent 进程会带着空的模型配置活到进程结束——models.json 与网关 token 是每次 spawn 生成的，改 provider 到不了它。现在：① `session_models.go` 在发送前重解析失效引用，改用部署默认/第一个可用 provider 并写回 session；② `pi.Runtime.StartedModelConfig` 让控制面识别「启动时没有模型配置」的活进程，解析出可用模型后先 `Stop` 再 spawn；③ `RuntimeStatus.resolved_model_ref` / `model_unavailable` 让 header 显示真正会用的模型或「未配置」，`modelRefOf` 把 Pi 的 `unknown` 占位符归一成空，`跟随默认（unknown）` 不可能再出现。测试：`session_model_lifecycle_test.go` 四例（失效引用→换到默认并持久化、无可用 provider→带 provider 名的 400、无配置进程→重建后再 prompt、status 字段）、`pi/runtime_test.go` 两例（`unknown` 归一、`StartedModelConfig` 跟随 spawn env）、e2e `81-provider-lifecycle.spec.ts` 两例（删 provider 后发送成功并弹出「已自动切换模型」+ header/会话行都更新；无 fallback 时 400 文案含 provider 与「模型与凭据」）。旧后端实测：同一会话 `400 no_model`、session 行仍是死引用（`e2e/.artifacts/fix-provider-lifecycle/lifecycle-api-OLD.json`） |
| 惰性浏览器面板 + 空闲停止（host placement，`15b995c`） | DONE | `workspace-panels.tsx` 只在浏览器面板可见时挂载它；`browser.Manager` 的 idle reaper 在无 viewer 附着 `PIFORGE_BROWSER_IDLE_TIMEOUT` 后 stop 该 sandbox 浏览器；`GET /v1/admin/status` 暴露 `browser_running` / `browser_idle_timeout`。测试：`browser/idle_test.go` 4 例、`67-browser-lazy.spec.ts` 断言「打开会话不启动、选中面板才启动、切走后停止」；部署实测选中前 `docker exec pf-local pgrep chrome` 为空，选中后才出现 |
| 项目删除：先停进程、重试销毁，失败则拒绝并保留项目；清扫历史孤儿目录（本次修复） | DONE | `handleDeleteProject` 的顺序改为「停进程 → 重试销毁 → 删行」：`stopProjectProcesses` 先关该沙箱的浏览器、`Runtime.Stop` 每个会话的 agent、`Term.CloseSandbox` 每个 worktree 的终端（`TermManager` 新增该方法），再 `destroySandbox` 以 150ms 递增退避重试 6 次；仍失败返回 409 并**保留项目**（文案「沙箱目录删不掉，项目已保留…」），不再像旧代码那样记一条 warn 后照删项目行。历史遗留由 `internal/janitor` 处理：启动即扫一轮、之后每 `-sandbox-sweep`（默认 10m，0 关闭，仅本地 provider）扫一次，删除 `sandboxes/<user>/<project>` 中没有 projects 行的目录及其 `_state/<user>/<project>.json`，只认 `usr_*`/`prj_*` 形状的目录名。测试：`project_delete_test.go`（注入 destroy 失败 → 409 + 项目仍在 + 至少重试 2 次；释放后重试 → 行与目录都消失；终端先关后销毁的顺序）、`janitor_test.go` 5 例、`61-project-lifecycle.spec.ts`（UI 删除后**真实路径**消失；用 PowerShell `FileShare.None` 真锁住 checkout → UI 报「沙箱目录删不掉 / 项目已保留」且项目仍在，释放后重试两者都消失）、`e2e/scripts/fix-orphans-repro.ps1`（Windows 复现/验证脚本）。旧代码实测：删除返回 200、项目行消失、`sandboxes/<user>/<project>` 留盘，日志 `delete project: sandbox destroy failed ... unlinkat ...\repo\held-by-a-process.txt: The process cannot access the file...`（`e2e/.artifacts/fix-orphans/fail-first.log`） |
| ⌘K 命令面板的默认选中 / Enter（PRD §10.1-5、§13「全部交互可键盘到达」；PRD 没有对应 FR 行） | DONE | 旧实现把选择交给 cmdk 1.1.1，它按行的 `value` 字符串认选中行。会话行的 label 在 projects 查询返回后才从「项目」变成真实项目名，`value` 随之变化，存储的选中值不再匹配任何行：`[cmdk-item][aria-selected="true"]` 实测 0，而 cmdk 的 Enter 只会向选中行派发，于是「输入 → 回车」什么都不发生。现在面板自己管理选择：行有稳定 id（`session:<id>` 等），active 行由可见列表**推导**（当前 id 不在了就选第一行），`aria-selected`、`aria-activedescendant`、`role=listbox/option`、`aria-controls/expanded` 同源；↑↓/Home/End/Enter 由面板处理。列表仍在加载时按 Enter **忽略而不是排队**：结果随时可能变化，排队的 Enter 会执行用户没看见的行；面板保持打开，行出现后下一次 Enter 生效。测试：`web/components/shell/command-palette.test.tsx` 4 例（异步落定后第一行默认选中且 Enter 进入会话、label 变化不丢选择、加载中 Enter 无效、↑↓ 仍可用）、`e2e/tests/60-session-search.spec.ts` 的「Ctrl+K → 输入 → Enter 直接打开」（旧代码在保留新 spec、去掉 ArrowDown 后必失败）与「列表仍在加载时 Enter 被忽略，随后生效」；部署实例探针 `e2e/scripts/fix-palette-shadow-probe.mjs` 旧构建 `selected=0`、URL 未变。 |
| 运行时目录生命周期：`<dataDir>` 下每个目录都有 owner、删除路径与 janitor 兜底 | DONE | 见「本批：运行时目录生命周期」。`backend/internal/runtimedirs` 是唯一清单（sandboxes / pi-agents / skill-snapshots / snapshots / artifacts / skills，外加 piforge.db、master.key、secret.key 三个部署文件）；`TestEveryInventoryEntryHasALifecycle` 逐行要求 creator/owner/delete path/fallback，`TestEveryDataDirLiteralIsInTheInventory` 扫描 `filepath.Join(dataDir, …)`，新目录没有生命周期会失败。删会话级联 pi-agents + artifacts + skill-snapshots；删项目级联所有子会话的同三类目录与每个 sandbox 的 `snapshots/<sbx>`；janitor 每类一条 count/bytes 日志，单个失败不中断、索引读不到拒扫。`GET /v1/admin/status` 的 `storage` 块与 admin 页面按目录报告 entries/bytes。测试：`runtime_dirs_test.go` 4 例、`janitor_test.go` 新增 4 例（含 live-owner fixture 与索引失败）、`runtimedirs` 4 例；部署探针 `e2e/scripts/dirlifecycle-probe.mjs` 旧镜像留下两个目录、新镜像 10s 内清零，设计见 `e2e/.artifacts/fix-dirlifecycle/`。 |
| 快照记录（`snapshots` 表行）的生命周期：外键级联 + janitor 行清扫 + 迁移 | DONE | 见「本批：快照行生命周期 + 沙箱磁盘配额」。`snapshots.sandbox_id` 现在是 `REFERENCES sandboxes(id) ON DELETE CASCADE` 的真外键（删项目 → 级联 sandboxes → 行随目录一起消失）；老库在 `store.Open` 时幂等重建（一个事务里建新表、原样拷贝每一行、改名，外键校验在拷贝期间关闭，所以「sandbox 已经没了」的历史行留给 janitor 而不是迁移删掉）；janitor 每轮读 `SnapshotIndex`（一条查询、covering index `snapshots_sweep`），只删「manifest 目录不存在」或「sandbox 行不存在」的行，目录和 sandbox 都活着的一行不碰、没有 blob_key 的行不猜也不删；`DeleteSnapshots` 分批删。另外修掉让删除根本走不到级联的那个缺陷：sandbox 被空闲回收后再删项目会 409（provider 已经不认这个 id），现在 `sandbox.ErrNotFound` 视为「已经销毁」。 |
| 重启对账：声称 running 但没有进程的会话/运行被标记 `interrupted`（本次修复 + 远端存活探测） | DONE | 见「本批：重启对账」与「本批：远端存活探测」。`internal/reconcile` 在**启动时（端口打开前）**、看门狗每 `-session-reconcile-interval`（默认 30s，0 关闭周期）以及**删项目守卫里**核对 `sessions.status='running'` 或 `runs.status='started'` 的行，把没有进程服务的行在**一个事务**里改成 `interrupted`：session 置 `interrupted`、started run 落成 `interrupted`（`interrupted_by=reconcile`、`completion_note` 写原因），追加 `run.settled`（payload 带 `status`/`reason`/`cause`）、`run.interrupted`（恢复标记，带 `cause`）与一条 `platform.error`（`code=run_interrupted`，同样带 `cause`）。**权威是一个可替换的探针，不是写死的内置注册表**：`contracts.LivenessProbe.Alive(ctx, sessionID) (bool, error)` 是唯一问题；`-sandbox local` 用 `liveness.Process`（进程内注册表 `pi.Runtime.Alive`），`docker`/`runner` 用 `liveness.Composite`（注册表 **+** 沙箱工作区里的心跳 marker，经沙箱自己的 shell API 读取；agent 在沙箱里活过控制面重启，所以新的控制面必须问沙箱）。**三值语义**：`true` → 保留（`kept_live`）；只有 `false` → 写 interrupted；`error` → **unknown，保留并 WARN**（写明 session/project/原因，`unknown=` 计数，单次探针 10s 超时同样算 unknown）。没有探针（Probe 与 Runtime 都 nil）→ 保持旧行为（只问注册表）并 WARN；只配 Runtime → 启动日志写明「no liveness probe is configured」。启动日志一行 `session reconciliation at startup scanned=… interrupted=… runs=… kept_live=… unknown=…`；看门狗只在真修复或有 unknown 时打 summary。幂等：第二遍看不到 running/started 行，不追加事件。删项目不再被谎言永久钉住：守卫先对项目做同一轮对账再数 running，仍有活进程才 409。测试：`reconcile/reconcile_test.go` 12 例（含 6 个探针用例：活着的远端 run 不动、探针 error → unknown 且不改行、只有明确 false 才中断、composite 在沙箱答不上来时保守、超时算 unknown、旧接线把活着的远端 run 写成 interrupted 的 bug 冻结）、`liveness/` 19 例（marker 租约与路径、launcher 写/刷新/删除 marker 与退出码、fake 沙箱的 HTTP 路径、composite 规则）、`cmd/piforged/session_liveness_test.go` 4 例（真 store + 真 runner 客户端 + fake runner + fake 沙箱的重启接线；无探针时 local 只问注册表；launcher argv）、`store/session_sandbox_test.go`、`pi/wrap_test.go`。**fail-first（旧二进制）**：`e2e/scripts/fix-liveness-restart.mjs` 用 `51ff223` 的二进制 + `-sandbox runner` 指向一个报告「心跳新鲜」的 fake 沙箱 → 活着的远端 session 被写成 `interrupted`、`interrupted=1`、沙箱一次都没被问；同一脚本的新二进制 `kept_live=1 unknown=0`、session 仍 `running`、沙箱被问 1 次（`e2e/.artifacts/fix-liveness/restart-old.json` / `restart-new.json`）。部署实测：local 部署（`pf-local`，:7722）上真实 run 跨 15 次采样/76s（≥2 个 30s 看门狗周期）仍 `running`，DB 无恢复事件，容器日志无 `interrupted an orphaned run`（`live-live-local.json` + 截图 `live-run-mid-watchdog.png`）。 |

| 中断恢复：标记写明原因与存活状态，一键「继续」重新建立上下文（本次修复） | DONE | 见「本批：中断恢复」。三种结局共用 `run.interrupted` 事件（payload 的 `cause` = `stop` / `restart` / `process_exit` 加原因句）：用户停止由 `handleInterrupt` 写，重启与进程退出由 `store.InterruptRunningSession` 在同一个事务里随 `run.settled` 写（`platform.error` 也补上 `cause`）。`GET /v1/sessions/{id}/recovery` 返回原因、来源、上次请求、`survival.summary`（未提交文件数与 HEAD 由 `Workspace.Status` + `Workspace.Log` 实测；沙箱 `MISSING`/`TERMINATED`、worktree 行不存在或读不到时写「沙箱/工作区已不存在…无法确认」，不冒充干净）以及 `can_continue` / `blocked_reason`。`POST /messages` 的 `continue_interrupted` 由服务端 `internal/recovery.ComposeContinue` 拼出「上次请求 + 中断原因 + 先检查当前状态」，与行内评论一样在转录里显示的就是 agent 收到的；`run.started` 之后同一标记不再提供继续，绝不自动发送。测试：`internal/recovery/recovery_test.go`（三种原因的消息都含上次请求、原因与核对指令；存活摘要覆盖 dirty/HEAD、无提交、读不到、沙箱不存在）、`httpapi/recovery_test.go` 四例（真实路由 GET + 继续消息真的进 runtime、沙箱 MISSING 时拒绝且不发送、已继续过不再提供、停止写入 cause）、`reconcile_test.go` 断言 `run.interrupted` 与 `cause`；e2e `85-recovery.spec.ts` 点停止 → 标记显示「停止」与「工作区有 1 个未提交改动 / 最后一次提交」→ 点继续 → 转录里的消息含上次请求与核对指令。旧前端跑新 spec 失败在 `recovery-cause` 不存在（`e2e/.artifacts/fix-recovery/fail-first-e2e.log`）。 |

计数：上面 126 条 FR 是 **DONE 68 / PARTIAL 42 / MISSING 15 / 不做 1**（按表格行逐行重数）；加上九条非 FR 交付能力（均 DONE）后整表 **135 行 = DONE 77 / PARTIAL 42 / MISSING 15 / 不做 1**。本批（中断恢复）不改变任何 FR 状态（FR-5.8 仍是 PARTIAL：配额按运行边界周期性测量，且本地 provider 仍无 CPU/内存限制）；FR-14.2 仍是 PARTIAL（Windows 仍没有目录级不可变，且 copy-ro 不阻止在目录里新建文件）。

**明确边界：远端 / AIO 沙箱里的浏览器仍不会在空闲时被停止。** `15b995c` 的 idle 策略只在 host placement 生效：控制面直接持有 Chrome 进程，`browser.Manager` 能 stop 它。远端 / AIO 模式下 Chrome 跑在沙箱里，控制面断开 CDP 只意味着没人看了，进程仍在沙箱里；`PIFORGE_BROWSER_IDLE_TIMEOUT` 不会杀它，也没有 runner 侧的空闲回收。要覆盖这条路径，需要沙箱 provider 按「无 viewer」自行 stop 浏览器（或 runner 提供 idle 端点），目前没有实现，也没有测试。

## 最影响用户的缺口（按「用户多久会撞上」排序）

1. **FR-11.6 agent 侧浏览器工具**——接管锁已经有了（FR-11.3 DONE：租约 + `browser_busy` + 「正在被 X 控制」），但仍没有 Playwright MCP，agent 没有浏览器工具可被挂起；要做的是 MCP 预设（FR-15.7）与接管时的工具拦截。
2. **FR-15.x MCP 只到一半**——没有 OAuth 代办（15.3）、没有连接状态徽章（15.5）、没有一键导入 mcpServers JSON（15.6）、没有内置预设（15.7）、没有工具列表/逐个禁用（15.8）。
3. **FR-5.8 / 16.4 / 16.6 运维面缺失**——资源护栏只有 docker provider 的启动参数，没有管理入口；镜像管理、护栏配置都没有端点或页面。
4. **FR-9.6 多路跟随**——跟随终端没有「每分支一个跟随 tab」（单路跟随已修，见文末）；终端本体已有多 tab（FR-10.2 本轮补齐）。
5. **FR-12.3 / 12.6**——diff 里不能写行内评论转成 steer；没有一键开 PR。（FR-12.1 的四个作用域与 FR-12.2 的逐 hunk UI 本批补齐；「本轮」仍没有入口，见文末。）
6. **FR-2.4 / 8.8 / 16.3**——删除分支不清理 worktree（`RemoveWorktree` 无调用者）；没有开 PR；skill 已按平台只读挂载或只读副本（bind-ro 只在有权限的 Linux 上），但没有 admin 共享 skill 的写入口。
7. **FR-5.3 快照只能看不能恢复**——清单索引在，但没有 restore-from-snapshot；docker 的回收未实跑；状态条也没有倒计时（FR-5.1）。

## 本次复核改了什么

本版以 `docs/STATUS-AUDIT.md`（`06b61a3`）为输入，逐行重读测试，并把 06b61a3 之后的修复接进台账：

- **补上 DONE 的证据**：FR-2.5 / FR-2.6（`f766651`）、FR-2.7（`bd85ade`）、FR-11.8（`9c4ab0b`）、FR-12.5（`717f7cb`，从 MISSING 移到 DONE）、FR-12.6（从 MISSING 移到 DONE，并把 FR-8.8 一并补完）——每一行都指向断言用户可见结果的测试（并排对比的真实数字、已提交分支的采纳、截图的颜色数、测试面板的失败/通过徽章、部署实例上从对话框创建出来的 PR 链接），而不是「handler 返回 200」。
- **下调**：FR-3.3 DONE → MISSING（上限没有配置入口）；FR-1.9 / FR-11.2 / FR-9.2 DONE → PARTIAL（代码在，但缺一条断言用户可见承诺的测试，具体缺什么写在行内）。
- **标题按实际行为**：FR-5.2 由「打开会话自动唤醒」改为「发消息时唤醒」——打开一个 PAUSED 会话不会恢复，发消息才会。
- **去掉不能当证据的引用**：`30-concurrency`、`33-follow-terminal` 这两个永真/不相关断言在对应行里注明了，不再算作 DONE 的依据。
- **计数**：旧表头写 37 / 50 / 39（`2e5f206` 时代，且 FR-12.5 等行早已过期）；本版按 126 行实际状态重算；本批（FR-12.6 / FR-8.8）后为 67 / 43 / 15 / 1。

## 无法核实

- **FR-5.3 的 docker provider 回收**：只在 local provider 上跑过；docker 的 `Destroy` 是否保留宿主目录、reclaim 后 `MISSING` 行是否一致，需要一台有 docker 的机器单独跑。
- **FR-1.9 的真实阈值压缩**：e2e 里用 fake peer 走通了「压缩真的发生 → marker 出现 → 用量下降」；真机没把会话压到阈值（pi 的 `keepRecentTokens=20000` + `reserveTokens=16384`，见文末），因此真实压缩本身仍未跑过一次；小会话的 no-op 已在部署实例上实测。
- **FR-11.9 的 cookie 复用**：只证明 profile 目录在多个沙箱里存在/复用，没有验证登录态跨浏览器重启保留。
- **FR-13.6 的「真的用了这个模型」**：只证明存进 session 并进了 pi 的 spec；上游实际模型只能从网关 token 的 claim 推断。

## 本次修复（FR-9.2 / FR-1.9）

两条都不是「补个测试」：测试之所以假，是因为行为本身有真实缺陷。

- **FR-9.2 跟随终端只显示第一行**：`FollowTerminal` 按 `chunks` 数组下标追加，而 `collectBashOutput` 每个 bash 工具块只产出**一个** `chunk`——数组长度永远是 1，内容却在增长，于是第一段之后再也不写。第二层问题：handler 把 `block.chunk + chunk` 当成增量追加，而 Pi 的 `partialResult` 是累计快照，真实输出会逐行重复。修法：`collectBashOutput` 返回整段文本，组件按「已写入字符数」追加差额；xterm 挂载前的字节进缓冲；`mergeToolChunk` 用前缀判定把累计快照当替换；`capFollowText` 保留最新 2000 行（超出 500 行才批量重建），xterm scrollback 与之一致。
- **FR-1.9 小会话压缩 500**：`Runtime.Compact` 把 pi 的 `CommandError`（"Nothing to compact (session too small)"）原样上抛，`classify` 只能落到 500。修法：`isNothingToCompact` 识别它并返回 `contracts.ErrNothingToCompact`，`handleCompact` 翻成 200 `{"compacted":false,"reason":"没有可压缩的上下文"}`；成功改为 200 `{"compacted":true}`。fake peer 现在也复现 pi 的「太小就拒绝」，因此 no-op 路径是端到端跑通的。
- **旧代码确实会失败**（本轮实测）：
  - 用 `HEAD b87e5e6` 的前端构建跑新 `33-follow-terminal`：两例都失败在 `Expected: >= 3 / Received: 1`——跟随面板只显示第一行。
  - 用 `HEAD b87e5e6` 的后端二进制跑新压缩用例：marker 用例 `Expected: 200 / Received: 202`，no-op 用例 `Expected: 200 / Received: 500`。
- 证据截图：`e2e/.artifacts/fix-follow-compact/`（`follow-live-mid-run.png` 运行中、`follow-live-final.png`、`follow-second-tab-mid-run.png`、`compaction-marker-and-meter.png`、`old-code-follow-frozen.png`）。

## 本次修复（FR-3.3）

- **上限从常量变成配置**：`cmd/piforged/main.go` 新增 `-parallel-limit`（默认 3）/ `PIFORGE_PARALLEL_LIMIT`，经 `httpapi.ResolveParallelLimit` 解析（flag 显式传入时优先，否则读 env，缺省为 3），并在启动时校验 1–10：0、11、非整数都是启动错误（`invalid parallel limit: ...`），不再静默回落到默认值。解析结果写入 `Deps.ParallelLimit`，启动日志输出 `parallel branch limit branches=1 max=10`。上限常量从 `UnlockableParallelLimit = 5` 改为 `MaxParallelLimit = 10`。
- **管理员能看到实际值**：`handleAdminStatus` 返回 `parallel_limit` / `parallel_limit_max`，管理页「配置」新增 `stat-parallel-limit`（值 `parallel-limit-value`），文案「并行分支上限（1–10，来自 -parallel-limit）」。
- **拒绝能解释**：`enforceConcurrency` 的 429 文案改为「并行上限是 N 个分支，现在有 M 个在运行（分支名…），先让其中一个跑完」，`error.details` 带 `limit` / `running`（会话 id）/ `running_branches`（人读的名字）；error code 为 `rate_limited`。`composer.tsx` 对 429 不再弹「发送失败」，而是渲染内联 `parallel-limit-refusal`（`parallel-limit-reason` 为服务端原句），草稿保留。
- **测试**：`cmd/piforged/parallel_limit_test.go`（默认 3；`-parallel-limit=5`→5；0→错误；11→错误；env 5→5；flag 4 覆盖 env 7；env 0/11/`three`→错误）；`internal/httpapi/concurrency_test.go` 的 `TestParallelLimitRefusesTheThirdRun`（经 resolver 设 2，主会话与分支各 `POST /messages` 真的起两个 run，第三次 429，文案含 `并行上限是 2 个分支`/`现在有 2 个在运行`/两个分支名，`details.running` 两条，且被拒分支没有 prompt）；`admin_status_test.go` 断言 `parallel_limit = 5`；`composer.test.tsx` 断言 429 原句上屏且草稿保留。
- **e2e 用的是哪种方式**：`e2e/playwright.config.ts` 给被测实例加了 `PIFORGE_PARALLEL_LIMIT: "2"`（env 方式，不是「测试自己改 Deps」），`65-parallel-limit.spec.ts` 再通过管理端打开 dev 的 `parallel` 开关、在管理页读回 2、用两条 `[[hold-run]]` 制造两个在跑的 run，最后在浏览器里发第三个 prompt，断言内联原句，并用 API 断言 429 + `details.limit=2`。
- **旧代码确实会失败**：`git stash`（保留新 spec）后用 `d0f3f7a` 的前后端重建，跑 `65-parallel-limit.spec.ts` → 失败在 `expect(getByTestId('parallel-limit-value')).toHaveText('2')`：`element(s) not found`（那一版既没有这个字段也没有这个卡片）；同时旧文案只有「并行上限是 N 个分支，先让其中一个跑完」，不包含任何「谁在跑」的信息。
- **部署实测**：以 `PIFORGE_PARALLEL_LIMIT=1` 重启 `pf-local`（同数据卷、同 seed、`PIFORGE_DEFAULT_PROVIDER=volc`），启动日志 `parallel branch limit branches=1 max=10`；`GET /v1/admin/status` 返回 `"parallel_limit":1,"parallel_limit_max":10`，管理页显示 1（截图 `40-deployed-admin-limit.png`）；真实 run 在跑时从分支页面发送，内联显示「并行上限是 1 个分支，现在有 1 个在运行（fr33 verify root），先让其中一个跑完」（截图 `42-deployed-refusal.png`），同一请求 API 返回 429 且 `details.limit=1`、`details.running` 一条。

## 本次修复（FR-1.9 浏览器入口）

- **入口**：`web/components/session/compact-context.tsx` 新增 `CompactContextButton`，放在会话头用量条旁（即 context-usage 区域）。`agentAlive` 来自 `GET /v1/sessions/{id}/status` 的 `process_id`（Pi 进程只要还在就非 0，run 结束不等于进程结束）；为空时按钮 `disabled` 并直接显示「没有运行中的 agent」，不是只写在 title 里。run settle 时会失效 `session-status`，另加 15s 轮询，首个 run 结束后无需刷新即可点击。
- **点击**：调 `api.sessions.compact`；`compacted:false` 时把 `reason`（「没有可压缩的上下文」）原样写进 `compact-status` 并弹 toast，不画 marker；`compacted:true` 时显示「上下文已压缩，用量已更新」，并 `invalidateQueries(qk.stats)` 作为兜底，配合 SSE `context.usage` 让用量条无需 reload 就下降。
- **测试**：`compact-context.test.tsx` 3 例——无 agent 时禁用且有可见说明；no-op 的 reason 在屏幕上；成功后 meter 从 75% 到 16%（mock stats 第二次返回 16%）。e2e `66-compact-ui.spec.ts` 3 例——禁用说明、no-op 原句上屏且页面上没有「上下文已压缩」、`[[big-context]]` 会话点按钮后 `compact-status` 为「上下文已压缩，用量已更新」且 `context-meter` 从 >50% 掉 20 个百分点以上。
- **旧代码确实会失败**：`git stash`（保留新 spec）后用 `d0f3f7a` 的前后端重建并跑 `66-compact-ui.spec.ts`：3 例全部失败在 `getByTestId('compact-context')`（`element(s) not found`），因为那一版浏览器里没有任何压缩入口。
- **部署实测**：`pf-local`（真实 pi + volc）新会话上按钮禁用并显示「没有运行中的 agent」（`41-deployed-compact-disabled.png`）；跑完一个真实小 turn 后按钮可用，点击后屏幕上出现「没有可压缩的上下文」且没有 marker（`43-deployed-compact-noop.png`）。成功压缩（`150k → 32k`、meter 下降）由 fake peer 的 e2e 覆盖，真机没有把会话压到阈值。
- **门禁**：`go build ./...`、`go vet ./...`、`go test ./... -count=1`、`pnpm --dir web test -- --run`（22 文件 / 183 例）、`pnpm --dir web exec next build` 全绿；完整 e2e **143 passed**（139 + 本轮 4 条新用例，6.4m）。
## 本次修复（FR-14.4 zip 与「可禁用」）

- **台账原文**：「PARTIAL（Git 有，zip 没有）……UI 在设置页「从仓库导入」。**zip 上传未实现**」。复核时还发现 PRD 的「可禁用」也只有一列 `enabled`：没有切换入口，`provision.mountTooling` 也不看它，所以「停用」只会改一个徽章。
- **补的 zip**：`POST /v1/skills/import/zip` 接 multipart `file`（8 MB 请求上限），用 `archive/zip` 解到临时目录后调用与仓库导入**同一个** `findSkills`，因此「每个含 SKILL.md 的目录各成一个 Skill、只取该目录下文件、4 MB 总量」的规则只有一份。条目名是攻击者输入：`../`、绝对路径、符号链接、超过 1 MB 的单文件、超过 4 MB 的解压总量都会让**整包**被拒绝，而不是跳过坏条目继续导入。设置页的「导入 Skill」对话框现在有 zip 选择与「上传 zip」按钮，结果与仓库导入共用一条提示与列表刷新。
- **补的可禁用**：`PATCH /v1/skills/{id}` 改 `enabled`（共享 Skill 与删除同一条权限规则：仅管理员）；`store.SetSkillEnabled` 在写连接上回读；`provision.mountTooling` 只把 `Enabled` 的 Skill 交给 `materialize.Mount`，所以停用后**新建**会话不再有它的目录与 `session_skills` 记录，已挂载的副本不动。Skill 行上有开关和「已停用」徽章。
- **测试（断言用户可见结果）**：`e2e/tests/69-skill-zip.spec.ts` 三例——在设置页真的选 zip 并点上传后，列表出现 `pdf-tools`（描述来自 SKILL.md 正文）与 `review`，bundle 只有 `SKILL.md` 和 `scripts/split.py`（README 没混进来）；启用时新会话的 `tooling.mounted.skills` 含两者，点开关后徽章变「已停用」、`GET /v1/skills` 的 `enabled=false`，再建一个会话只剩 `review`；`../escape/SKILL.md` 的包返回 400 且库里既没有 `escape` 也没有同包的 `fine`；没有 SKILL.md 的包返回 404。Go 侧 `skill_zip_test.go` 5 例覆盖解压+findSkills、穿越、绝对路径/符号链接、超大条目、非 zip。
- **旧代码确实会失败**：`0722790` 的前后端（本轮改动前）跑同一条 spec——UI 用例失败在 `waiting for getByTestId('skill-import-zip')`（设置页没有这个输入），API 用例收到 `{"error":{"code":"not_found","message":"no route for POST /v1/skills/import/zip"}}`；停用开关同理不存在。日志：`e2e/.artifacts/partial-batch1/fr-14.4-fail-first.log`。

## 本次修复（FR-6.6）

- **台账原文**：「后端 `handleSearch`（`?type=content` + `fsx.Search`）完整；前端无 ⌘⇧F 入口，`quick-open.tsx` 只做文件名搜索」。也就是说这条缺的不是搜索能力，是用户按不到它。
- **补的**：`quick-open.tsx` 的一个面板里加第二种模式。⌘P 打开/关闭文件名搜索（Shift 被排除，⌘⇧P 不会误触发），⌘⇧F 打开内容搜索；模式用一个 `quick-open-mode` 徽章明示（`data-mode="name|content"`），不靠用户猜。内容结果每条显示 `path:line` 和匹配到的整行文本，`data-path` / `data-line` 供点击使用；点击走既有的 `handleOpenLocation`，把行号传给 `FilePreview`，最终由 `CodePreview` 高亮并滚动到 `data-active-line`（这条管线是 FR-12.5 测试失败跳转时就有的，本轮只是让内容搜索接上它）。
- **一处必须改的行为**：Markdown 命中时 `MarkdownPreview` 的渲染视图无法指向第 N 行，所以带行号打开时改用源码视图（`file-preview.tsx` 的分支）。不这么做，`.md` 里的内容命中会点开文件却停在第 1 行——表面完成，实际没兑现。
- **测试**：`e2e/tests/70-content-search.spec.ts` 两例。第一例通过真实 Files API 写 `docs/guide.md`，在 token 所在第 3 行，按 Ctrl+Shift+F 后断言模式为 `content`、结果 `docs/guide.md:3` 且文本含 token，点击后 `code-preview[data-active-line="3"]` 且预览头部是 `docs/guide.md`、面板关闭。第二例证明两种模式没有混：同一 token 只存在于文件内容，Ctrl+P（模式 `name`）查不到任何结果。
- **旧代码确实会失败**：`git stash` 保留新 spec，用 `f015189` 的前端构建跑这两例——两例都失败在 `getByTestId('quick-open-mode')` 的 `element(s) not found`（旧面板没有模式，⌘⇧F 不打开任何东西）。日志：`e2e/.artifacts/partial-batch1/fr-6.6-fail-first.log`。

## 本次修复（FR-10.2）

- **台账原文**：「后端按 `?tab=` 支持任意 tab 名（`pty_handlers.go` `sanitizeTab`）；前端 `workspace-panels.tsx` 只挂一个 `TerminalPane`（tab 默认 `main`），无 tab 条、无重命名/关闭」。传输层早就有多终端，界面只用了其中一个。
- **补的**：`terminal-tabs.tsx`。tab 条可新增/切换/关闭（上限 8，测试用到 5）；每个 tab 是一个 `TerminalPane`，`tab` id（`t1`…）就是服务端的 PTY key，`data-pty-tab` 可核对；未选中的 tab 只加 `hidden`，不卸载，所以切回去 shell 与滚动缓冲都还在。关闭先发 `{type:"close"}`（`use-pty.ts` 的 `terminate`）让服务端 `Term.Close`，再移除标签——只断 socket 是「断线重连」语义（FR-10.3），不能用来关 tab。重命名只改 `label`，id 不变，所以改名不会换掉 shell；关掉最后一个会开一个新的，而不是留一个空面板。「在终端中打开」的 `cd` 跟随当前标签：`paneRef` 在 active 变化时重新指向对应 pane。
- **实现坑（真 bug，不是测试问题）**：`nextNumber.current++` 被本仓库启用的 React Compiler 编译成前置自增（编译产物是 `ref.current = ref.current + 1` 后再取值），于是第一个新 tab 编号是 3、标签显示「终端 3」。改成显式的 `const n = ref.current; ref.current = n + 1`，`terminal-tabs.tsx` 里两处都注明了原因。
- **测试**：`e2e/tests/71-terminal-tabs.spec.ts` 一例。加到 5 个 tab 后断言 5 个 pane 的 `data-pty-tab` 与 tab 的 `data-tab-id` 一一对应、`data-pty-status` 全为 `open`（不是只画了个条）；在第 5 个 tab 里键入 `echo five > tab-five.txt`、在第 1 个里键入 `echo one > tab-one.txt`，两个文件都能从 Files API 读到——这是两个独立 shell 落在同一个 worktree 上的证据，而不是两个空壳；双击改名「构建」后 `t1` 仍 `open`；关闭 `t1` 后剩 4 个 tab/pane，`tab-one.txt` 还在。
- **旧代码确实会失败**：把新组件挪开、`git stash` 保留新 spec，用 `c26d670` 的前端构建跑——失败在 `getByTestId('terminal-tab')` 的 `expected 1 / received 0`（旧面板只有一个 `TerminalPane`，没有 tab 条）。日志：`e2e/.artifacts/partial-batch1/fr-10.2-fail-first.log`。

## 本轮门禁（FR-14.4 / FR-6.6 / FR-10.2）

- `go build ./...`：0；`go vet ./...`：0。`go test ./... -count=1` 第一次在全量门禁并发跑时挂在 `internal/pi` 的 `TestRuntimeRunEventOrder`（`status after settle = {Streaming:true}`，`runtime_test.go:127`）。该包本轮零改动（`internal/pi` 不在三次提交的文件里），单测单跑通过，隔离后再跑一遍全量 `go test ./... -count=1` 全绿（见下）。这是一个已存在的「收到 settled 事件后立刻读 Status」的计时竞态，不是本轮引入；没有改它，也没有把它当成本轮的门禁通过。（**本批已修**：`Status` 不再让 peer 的 `get_state` 覆盖事件流已宣布的 settled 状态，见文末「本次修复（任务 0）」。）
- `pnpm --dir web test -- --run`：**22 个文件 / 186 例通过**。`pnpm --dir web exec next build`：通过（路由表含 `/settings/skills`）。
- 完整 e2e：**154 passed（6.2m，72 个 spec 文件）**，即原有 148 条加本轮 6 条（`69-skill-zip` 3、`70-content-search` 2、`71-terminal-tabs` 1）。日志：`e2e/.artifacts/partial-batch1/full-e2e.log`。
- `pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入，未打印、未落盘）：run `20261030-090820`，**75/75 PASS，failure=null**；全新 volume `pf-drill-data` + 一次性容器 `pf-drill`，结束后容器与 volume 都已删除（`docker ps -a` 与 `docker volume ls` 无残留），`pf-local` 全程未被触碰。日志：`e2e/.artifacts/partial-batch1/deliver-check.log`，summary：`e2e/.artifacts/delivery-drill/20261030-090820/summary.json`。
- 镜像与部署：`pwsh scripts/build-app-image.ps1` 重建 `piforge:local`（sha256 `26b57a9b69f3…`，drill 断言的是同一 id）；`pf-local` 用**同一个数据卷** `368d6186adb8…` 重启，环境仍为 `PIFORGE_DEFAULT_PROVIDER=volc` 与四个 seed 账号；重启后 `GET /v1/providers` 里 `volc` 仍 `enabled=true`、`has_key=true`（`shared-anthropic` 无 key 是部署里原有的，未改）。
- 部署上的截图（不是 fake peer，是 `pf-local` 真实实例）：`e2e/.artifacts/partial-batch1/fr-14.4-zip-dialog.png`（导入对话框的 zip 选择与「上传 zip」）、`fr-14.4-imported-and-disabled.png`（`pdf-tools`/`review` 导入成功，`pdf-tools` 已停用）、`fr-6.6-content-results.png`（⌘⇧F，模式「内容」，结果 `docs/guide.md:3`）、`fr-6.6-line-jump.png`（点开后第 3 行高亮、「第 3 行」徽章）、`fr-10.2-terminal-tabs.png`（5 个终端 tab，其中一个改名为「构建」，当前 tab 已连接）。截图脚本：`e2e/scripts/shots-partial-batch1.mjs`（自建一个临时项目/会话与两个 Skill，结束时删除；不打印任何密钥）。

## 本次修复（任务 0：settled 之后仍报 Streaming 的竞态）

- **根因在产品，不在测试**。`Normalizer.Translate` 在同一个 mutex 里先 `emit(run.settled)`、再把 `Streaming` 清掉，而 `Translate` 返回前一直持有该锁；`Status` 读 `State()` 也要拿这把锁，所以订阅者看到 `run.settled` 后读到的**事件派生状态一定是 settled**。测试的读法没有竞态。
- 真正的缺陷在 `Runtime.Status`：它拿到 `Running:false / Streaming:false` 之后，又调 peer 的 `get_state`，并用返回的 `isStreaming` / `isCompacting` / `pendingMessageCount` **覆盖**事件流已经宣布的结果。peer 会先发出 `agent_settled`、再清自己的 `isStreaming`（fake peer 的 `defer` 就是这样；`get_state` 由另一条 goroutine 应答），落在这个窗口里的 status 于是返回 `Running:false Streaming:true` —— 自相矛盾，而且足以让 UI 从 settled 翻回 running、把下一次 run 当成 busy。`Status` 的注释本来就写着「run flags 来自事件流，get_state 只做补充」，代码没有做到。
- **修法**：事件流是权威；`get_state` 只在 `LastSeq == 0`（还没有任何事件可依据，例如刚 attach）时提供 run flags。其余时候它只补充 model、session file 与 context window。`internal/pi/runtime.go` `Status`。
- **回归测试**：`status_test.go` 的 `TestRuntimeStatusAfterSettleDoesNotTrustALaggingPeer` 用一个 peer 包装器把每个 `get_state` 应答都改写成 `isStreaming:true`，settle 之后 `Status` 仍不得报 Streaming。旧代码失败在 `Running:false Streaming:true`。
- **50× 证据**：单跑 `-count=50 -race` 在两种代码上都可能全绿（窗口很小），所以用 40 个并发实例各跑 50 次（=2000 次）复现：旧代码 **36 次失败**（20/40 个实例红）；修复后 **0/2000**。日志：`e2e/.artifacts/task0/stress-before.log`、`stress-after.log`。提交 `11fdea5`。

## 本次修复（FR-11.2 观察模式）

- **台账原文**：「默认 `observe` 成立；`browser_handlers.go` 在非 takeover 时对 `input` 命令直接 `break`（服务端判定，不靠面板自觉）。但**没有任何测试断言「输入被丢弃」**……缺：一条 observe 下键入/点击不生效、takeover 下生效的测试（单测或 e2e 均可）」。
- **补的是一条 e2e，行为未改**：`e2e/tests/72-observe-input.spec.ts`。页面把最后收到的事件写进 `document.title`，测试从 `GET …/browser/tabs` 读 title（读面板自己的按钮只能证明按钮状态）。observe 下点 canvas、按 `z`，等 1.5s 后 title 仍是 `observe-target`；从 UI 接管（按钮 → 确认）后同一套点击/按键让 title 变成 `clicked` / `key-b`。
- **为什么没有「旧代码失败」**：这一行缺的只是断言，守卫比测试早存在，所以上一提交必然通过——这不是可以伪造的 fail-first。改用变异证明断言有效：删掉服务端 `browserModeOf(sess.ID) != takeover` 守卫后，同一条 spec 失败在 `Expected "observe-target" / Received "key-z"`（`e2e/.artifacts/partial-batch2/fr-11.2-mutation-fail.log`）。恢复守卫后重新构建，spec 通过。
- **部署截图**：`fr-11.2-observe-click-dropped.png`（模式「观察」，点击后 tab 标题仍是 `observe-target`）、`fr-11.2-takeover-click-delivered.png`（模式「控制中」+ toast，标题 `clicked`）。

## 本次修复（FR-12.4 变更概览）

- **台账原文**：「`git-changes.tsx` 分组 + 文件列表；**无 +/- 行数汇总，无 lockfile/CI 风险标记**（`GitStatus` 也没有这些字段）」。
- **数据层**：`contracts.GitStatus` 加 `additions`/`deletions`；`contracts.GitChange` 加 `additions`/`deletions`/`risk`。`gitx.Status` 用 `git diff --numstat -z HEAD --` 统计（无 HEAD 时分别统计 index 与工作区），重命名取新路径；未跟踪文件从磁盘数行（二进制或 >4MB 记 0）。风险由 `gitx.riskOf` 判定，API 与任何客户端因此一致：`.github/workflows/`、`.circleci/`、`.buildkite/` 与 `.gitlab-ci.yml`、`Jenkinsfile` 等是 `ci`；`go.sum`、`package-lock.json`、`pnpm-lock.yaml`、`Cargo.lock` 等是 `lockfile`。
- **一处必须改的行为**：`git status` 从 `--untracked-files=normal` 改为 `all`。`normal` 把新的 `.github/workflows/ci.yml` 报成一行 `.github/`，行数（目录）为 0、风险也匹配不到文件——两个承诺都无法兑现。
- **UI**：每行 `+N/−N`（`git-line-counts`）与「锁文件」/「CI」徽章（`git-risk`），表头 `git-change-totals`；合计优先用服务端字段，旧服务端则按行求和。
- **测试**：`TestStatusCountsLinesAndFlagsRisk`（a.txt +1、b.txt −2、new.txt +2、package-lock +2/锁文件、ci.yml +1/CI，合计 +6 −2）与未跟踪分支的计数；e2e `73-git-overview.spec.ts` 先断言 API 的 `+6 −1` 和逐行字段，再断言页面上的同一组数字与徽章。
- **旧代码确实会失败**（分层证明）：只回退后端 → spec 失败在 `expect(status.additions).toBe(6)`（旧 API 无此字段，`fr-12.4-fail-first.log`）；只回退前端（后端已新）→ 失败在 `getByTestId('git-change-totals')` element not found（`fr-12.4-fail-first-ui.log`）。
- **部署截图**：`fr-12.4-change-overview.png`（表头 `4 个改动 +6 −1`，base.txt `+1 −1`，ci.yml `+1` + 「CI」，package-lock `+2` + 「锁文件」）。

## 本轮门禁（任务 0 / FR-11.2 / FR-12.4）

- `go build ./...`：0；`go vet ./...`：0；`go test ./... -count=1`：全绿（含 `internal/pi` 的新回归测试与 40 路 stress）。日志：`e2e/.artifacts/partial-batch2/go-test.log`。
- `pnpm --dir web test -- --run`：**22 个文件 / 186 例通过**；`pnpm --dir web exec next build`：通过。日志：`web-test.log`。
- 完整 e2e：**156 passed（6.3m）**，即 154 条加本批 2 条（`72-observe-input`、`73-git-overview`）。日志：`e2e/.artifacts/partial-batch2/full-e2e-clean.log`。**一次脏数据运行**（直接复用上一批留下的 `e2e/.data`）是 153 passed / 3 failed：`30-concurrency` 失败因为 `65` 上一轮把 dev 的 `parallel` 打开后没关，`38`/`69` 失败因为上一轮 `69` 把 `pdf-tools` 停用后没恢复。这与本批改动无关；在干净的 `e2e/.data` 上（本轮记录 154 时的方式）156 全绿。旧 `.data` 已移到 `e2e/.data.before-batch2`，未删除。
- `pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入，未打印、未落盘）：run `20261030-094558`，**75/75 PASS，failure=null**；一次性容器 `pf-drill` 与 volume `pf-drill-data` 已删除，`pf-local` 全程未被触碰。日志：`deliver-check.log`，summary：`e2e/.artifacts/delivery-drill/20261030-094558/summary.json`。
- 镜像与部署：`pwsh scripts/build-app-image.ps1` 重建 `piforge:local`（`sha256:7d4269d25296…`，drill 断言的是同一 id）；`pf-local` 用**同一个数据卷** `368d6186adb8…` 重启（新容器 `e3aa6ff3…`），环境仍是 `PIFORGE_DEFAULT_PROVIDER=volc` 与四个 seed 账号；重启后 `/healthz` 为 ok，`GET /v1/providers` 里 `volc` 仍 `enabled=true`、`has_key=true`。
- 截图（部署实例，不是 fake peer）：`e2e/.artifacts/partial-batch2/fr-12.4-change-overview.png`、`fr-11.2-observe-click-dropped.png`、`fr-11.2-takeover-click-delivered.png`。脚本：`e2e/scripts/shots-partial-batch2.mjs`（自建临时项目/会话，截图后删除；临时页面用一次性 helper 容器发布到 docker 网关，因为部署里的 Chromium 跑在容器内，127.0.0.1 是容器自己）。

## 仍是 PARTIAL 的相邻项（本批未动）

- **FR-11.6**：接管锁与输入门禁已由 FR-11.3 补齐；仍未解决的是 agent 侧——没有 Playwright MCP 预设，所以没有 `mcp__playwright__*` 可以在接管时挂起。这是 FR-11.6/15.7 的范围，不是补一条测试。
- **FR-12.1 的「本轮」**：工作区 / 已暂存 / 分支 vs 基准 / 提交范围 四个作用域都已可用并有测试；PRD 原文的「本轮」仍没有入口——它要回答「这一轮 agent 改了什么」，得从 run 的 tool events 里反推改动集合，不是加一个 tab，本批没有半做。
- **FR-12.1 的输入方式**：提交范围靠手输 `from`/`to`（默认 `HEAD~1..HEAD`），没有提交选择器；历史作用域不能逐 hunk 操作（显示 `git-hunk-readonly`），这是刻意的。
- **FR-12.4 的边界**：风险只覆盖台账点名的 lockfile 与 CI 两类（按已知文件名/目录）；二进制与 >4MB 文件的未跟踪行数记 0。这两点是刻意的，不假装覆盖任意 `*.lock` 或大文件。


## 本次修复（四个缺陷：项目删除遗留沙箱 / 导入不重启用 / 共享 Skill 无法创建 / 最后管理员测试名不副实）

### FIX 1 — 删项目遗留 `sandboxes/<user>/<project>`

- **根因**：`handleDeleteProject` 把沙箱销毁当 best-effort：`Destroy` 失败只记一条 warn，然后照删 projects 行。Windows 上只要 agent / 终端 / 沙箱浏览器还占着 checkout 里的文件，`os.RemoveAll` 就报 `unlinkat ...\repo: The process cannot access the file because it is being used by another process`，于是每次这样的删除都留一个没有任何行指向的目录。
- **旧代码实测（fail-first）**：`e2e/scripts/fix-orphans-repro.ps1` 在 `7dd8e8c` 的二进制上——DELETE 返回 200、`GET /v1/projects` 不再有该项目、`sandboxes/<user>/<project>` 仍在，日志原文 `level=WARN msg="delete project: sandbox destroy failed" ... unlinkat ...\repo\held-by-a-process.txt: The process cannot access the file because it is being used by another process.`（日志 `e2e/.artifacts/fix-orphans/fail-first.log`）。
- **选择：拒绝删除并保留项目**（而不是「删行 + 稍后清扫」）。理由：这个缺陷的本质是「行没了、目录还在」的不一致；让删除成功再异步清扫会把这个不一致变成用户可见的常态（项目从列表消失、磁盘上还在），而失败并保留项目让行与目录始终一致，且用户拿到可操作的句子（关掉占用进程后重试）。代价是持续占用时删不掉项目——所以先停占用者（agent/终端/浏览器）并用递增退避重试 6 次（约 2.25s），把「瞬时占用」吃在内部；只有真占死才失败。历史遗留（旧代码已经造出来的孤儿）另由 janitor 清扫，因为这条策略只能防新孤儿。
- **实现**：`stopProjectProcesses`（`Browser.Close`、每会话 `Runtime.Stop`、每 worktree `Term.CloseSandbox`；`TermManager` 新增该方法，`termAdapter` 转发到 `term.Manager.CloseSandbox`）→ `destroySandbox` 重试 → 仍失败返回 409「项目「X」的沙箱目录删不掉，项目已保留：…」。`internal/janitor` 在启动与每 `-sandbox-sweep`（默认 10m，0 关闭，只在本地 provider）删除没有 projects 行的 `usr_*/prj_*` 目录及其 `_state` 文件；目录名不符合 `usr_*`/`prj_*` 的一律不碰。
- **测试**：`project_delete_test.go` 两例（注入 destroy 失败 → 409、项目仍在、至少重试 2 次；释放后重试 → 行与目录都消失；终端在销毁之前被关闭的顺序）；`janitor_test.go` 5 例（只删孤儿、保留活沙箱、删孤儿 state 文件、不碰外来目录、missing root 是 no-op、dry-run 不删）；e2e `61-project-lifecycle.spec.ts` 两处：删除后**真实路径**消失；用 PowerShell `FileShare.None` 真锁住 checkout（Node 的 `openSync` 带 `FILE_SHARE_DELETE`，锁不住，这条教训写进了测试注释）→ UI 报「删除失败 / 沙箱目录删不掉 / 项目已保留」、项目仍 200、目录仍在，释放后重试两者都消失。部署验证：`e2e/scripts/fix-orphans-probe.mjs`（创建项目+会话 → UI 删除 → `docker exec pf-local test -d` 确认目录消失）。

### FIX 2 — 导入替换了 bundle 却不重启用

- **根因**：`persistCandidates` 对同名 Skill 只写 bundle、不碰 `enabled`；挂载只投影 `Enabled` 的 Skill，于是「导入成功」的 toast 下面是一个永远不会到达 agent 的行。
- **选择：导入即重新启用**，并在响应里回报 `enabled` / `reenabled`，UI toast 写「之前是停用状态，已重新启用」。理由：导入是明确的「我要用它」，而且旧行为的失败方式（报成功、实际不可用）正是本项目反复修的谎报类型；保持停用再补一句提示仍然让用户多点一次，并要解释一个反直觉的状态。顺带修掉同一路径上的越权：普通用户导入同名**共享** Skill 时，旧代码会先把磁盘上的共享 bundle 换掉、再因 `UpdateSkillBundle` 失败而标记 skip —— 现在 `SkillEditable` 先判权限，直接 skip 并给出原因。
- **fail-first**：把 `skill_import.go` 回退到 HEAD、保留新测试 → `import result = {Name:pdf-tools Skipped:false Enabled:false Reenabled:false}`（`e2e/.artifacts/fix-orphans/fix2-fail-first.log`）。
- **测试**：`skill_import_reenable_test.go` 走完整条链（建私有 Skill → PATCH 停用 → 真 local sandbox + 真 provisioner 建会话，断言 `.pi/skills/pdf-tools` 不存在 → 导入同名 zip → 响应 enabled/reenabled 为真 → 行 enabled → 新会话挂载的是新内容）；e2e `69-skill-zip.spec.ts` 追加「再次导入 → toast 含『已重新启用』→ 列表不再显示已停用 → 新会话挂载 pdf-tools」；`38` 与 `69` 里「导入不会重新启用」的过时注释同步改掉。

### FIX 3 — 管理员无法创建共享 Skill

- **根因**：`handleCreateSkill` 写死 `scope=user`；FR-16.3 那一行原本就写着「没有 admin 写共享 skill 的入口」，只有删除半边存在。
- **实现**：`POST /v1/skills` 接受 `scope`；`admin` 需要管理员（否则 403「共享 Skill 只能由管理员创建」），`store.CreateSkill` 落 scope 与 owner；共享 Skill 的开关改走 `SetSharedSkillEnabled`（任意管理员都能管，不只创建者）；UI 新建对话框对管理员显示「共享给所有用户」开关（`skill-shared`），列表已有的「共享」徽章与只读开关不变。
- **fail-first**：把后端回退到 HEAD、用新前端跑 `74-shared-skill-create.spec.ts` → 新建后 `[data-testid="skill-item"][data-name=…]` 始终不出现；直接对旧二进制发 `POST /v1/skills {"scope":"admin",…}` 得到 `400 invalid JSON body: json: unknown field "scope"`。
- **测试**：`skill_scope_test.go`（管理员建共享 → 普通用户列表可见且能读 bundle → 第二个管理员能开关；普通用户建共享 403 且不落库、`PATCH`/`DELETE`/`PUT bundle` 403、导入同名共享 Skill 被 skip 且共享 bundle 不变）；e2e `74-shared-skill-create.spec.ts`（管理员在 UI 勾选创建 → 普通用户在自己的账户看到「共享」徽章、`/` 面板里能选到它 → 开关/删除禁用、API 四种写操作 403 → `finally` 删除以免污染下一次 preflight）。

### FIX 4 — `09-admin-users` 的测试名与断言不符

- **根因**：那条测试叫「the last admin cannot demote or disable themselves」，但它断言的是 `isSelf` 让按钮 disabled——十个管理员时同样为真；旧测试既没构造「最后一个管理员」，也没区分「自己」与「最后一个」。
- **改法（采用首选方案）**：新增 API 断言：先把除种子管理员外的所有管理员临时降级（`finally` 恢复），此时降最后一个管理员 → 409 `the last admin cannot be demoted`；再提升一个临时管理员，用**同一个请求**（种子管理员降自己）→ 200。两次调用之间变的只有管理员数量，因此证明的是计数规则，不是自我守卫。另加自我停用 → 400 的 API 断言。原来的 UI 测试改名为「the user list disables self-demotion and self-disable controls」，它断言什么就叫什么；`a role can be changed` 结束时把它提升的账号降回普通用户，避免复用数据目录累积管理员。
- **复用数据目录的注意**：`e2e/.data` 里可能有上一轮遗留的管理员，所以新测试显式先把其他管理员「停放」再恢复，否则「最后一个」不成立（这正是第一次跑失败的原因）。

### 一个与四个缺陷无关的 e2e 稳定性修复

### 上一轮记为「仍存在」的 cmdk 选择竞态（本批修掉）

- 上一轮在 `60-session-search` 的「命令面板返回会话」上量到 `[cmdk-item][aria-selected="true"]` = 0，当时为了不掩盖产品缺陷，测试在 Enter 前补了一次 ArrowDown，并把「产品侧的选择竞态仍然存在」写在这里。**本批不再用这个 workaround**：根因是 cmdk 用行的 `value` 字符串做选中键，而会话行在 projects 查询返回后会从 `会话 <title> 项目` 变成 `会话 <title> <真实项目名>`，旧键失效、所有行 `aria-selected` 归零；cmdk 的 Enter 只向 `aria-selected="true"` 的行派发，所以「输入 → 回车」无事发生。面板改为自己管理选择（稳定 id + 由可见列表推导 active），spec 直接按 Enter，见文末「本轮修复（命令面板 / FR-14.1）」。旧构建实测仍是 0（`e2e/.artifacts/fix-palette-shadow/probe-log.json`）。

## 本轮门禁（四个缺陷）

- `go build ./...`：0；`go vet ./...`：0；`go test ./... -count=1`：**26 个有测试的包全部 ok**（另有 `cmd/sandbox-runner`、`tools/wsprobe` 两个无测试文件；含新包 `internal/janitor` 与 `internal/httpapi` 的 4 个新测试）。日志：`e2e/.artifacts/fix-orphans/go-test.log`。
- `pnpm --dir web test -- --run`：**22 个文件 / 186 例通过**（与上一批一致；本轮 UI 改动没有新增单测，行为由 Go 测试与 e2e 断言）。`pnpm --dir web exec next build`：通过。日志：`e2e/.artifacts/fix-orphans/web-test.log`。
- 完整 e2e（同一份 `e2e/.data`，未重置）：
  - run 1：**160 passed（7.9m）** = 原 157 加本轮 3 条（`61` 的 held-handle、`74` 的共享 Skill、`09` 的最后管理员）。日志 `e2e/.artifacts/fix-orphans/full-e2e-run1.log`。
  - run 2：**160 passed（8.3m）**，同一数据目录。日志 `full-e2e-run2b.log`。第一次 run 2 唯一失败是 `60-session-search` 的命令面板（cmdk 选择竞态，见上），修测试后连续两次全绿。
  - **第三次连续运行（同一目录，作为额外观察）不再绿**：从第 83 条起大量 20s 超时（`26-browser-tabs`、`27-clone`、`50-error-localization`、`51-project-repo-validation`），错误都是登录后 `app-sidebar` 没出现（`session-search` 之前的 run 2 就埋下了这个量级）。此时目录里有 **788 个沙箱目录、约 2.3 万个文件**（三轮各约 250 个会话）。把失败的四条 spec 单独跑：**9 passed（28.9s）**，说明不是代码回归，而是复用目录无限增长到这个体量后整套跑不动。要长期反复跑，得在两次运行之间重建 `.data`（`pnpm --dir e2e test:fresh`）或给套件加数据清理；这一点没有在本轮改，因为四个缺陷与它无关。
- `pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入，未打印、未落盘）：run `20261030-120612`，**75 个检查全部 PASS、`failure` 为空**（`summary.json` 的 `checks` 长度 75，无 FAIL/SKIP；日志里 150 行 PASS 是表格与摘要各一次）。镜像 id `sha256:44010d16b347…` 与随后部署的一致；一次性容器 `pf-drill` 与 volume `pf-drill-data` 已删除，`pf-local` 未被触碰（drill 自己断言容器 id 前后相同）。日志：`e2e/.artifacts/fix-orphans/deliver-check.log`，summary：`e2e/.artifacts/delivery-drill/20261030-120612/summary.json`。
- 镜像与部署：`pwsh scripts/build-app-image.ps1` 重建 `piforge:local`（`sha256:44010d16…`）；`pf-local` 用**同一个数据卷** `368d6186adb8…` 重建（新容器 `f90466d3…`，端口 7722→7700，环境仍是 `PIFORGE_DEFAULT_PROVIDER=volc` 与四个 seed 账号）。重启后 `/healthz` = ok，`GET /v1/providers` 里 `volc` 仍 `enabled=true`、`has_key=true`。
- **janitor 在部署上第一次跑就清掉了 11 个真实孤儿目录**（本项目此前在 Windows 上删项目留下的、或旧路径遗留的）：启动日志 `msg="orphaned sandbox directories are swept" root=/data/sandboxes interval=10m0s` 之后 11 条 `msg="janitor: removed an orphaned sandbox directory" user=… project=…`；dev 账户的沙箱目录从 45 个降到 35 个（另外 1 个属于另一个账户），剩下的 35 个目录都有 projects 行，`_state` 文件数也一致。这是「旧代码确实留了孤儿」在部署上的直接证据。
- 部署验证脚本 `e2e/scripts/fix-orphans-probe.mjs`：**17/17 检查通过**（`[RESULT] PASS`，`probe-log.json` 记了每一步）。截图（部署实例，`pf-local`）：`e2e/.artifacts/fix-orphans/fix1-delete-confirm.png`（删除确认，写明沙箱会删）、`fix1-deleted-home.png`（删除后回到首页；同一次运行里 `docker exec pf-local test -d /data/sandboxes/<user>/<project>` 从有变无）、`fix2-skill-enabled-after-import.png`（导入后列表不再有「已停用」）、`fix3-admin-create-shared-dialog.png`（管理员新建对话框的「共享给所有用户」开关已打开）、`fix3-admin-shared-created.png`（列表中的共享 Skill 与「Skill 已创建」）、`fix3-normal-user-sees-shared.png`（普通用户在**自己的账户**里看到共享徽章、开关与删除禁用；同页还能看到导入后重新启用的私有 Skill）。
- fail-first 日志：`e2e/.artifacts/fix-orphans/fail-first.log`（旧代码删项目 200 + 目录遗留）、`fix2-fail-first.log`（旧代码导入后 `enabled=false`）、`fix3-fail-first.log`（旧后端下 UI 建共享 Skill 失败）。FIX 3 的 API 侧另有一条：旧二进制对 `{"scope":"admin"}` 返回 `400 invalid JSON body: json: unknown field "scope"`。

## 本轮门禁（命令面板 / FR-14.1）

- `go build ./...`：0；`go vet ./...`：0；`go test ./... -count=1`：**27 个有测试的包全部 ok**（新增 `internal/skills`；`cmd/sandbox-runner` 与 `tools/wsprobe` 没有测试文件）。日志：`e2e/.artifacts/fix-palette-shadow/go-test.log`。
- `pnpm --dir web test -- --run`：**24 个文件 / 196 例通过**（新增 `command-palette.test.tsx` 4 例、`lib/skills.test.ts` 5 例，`skill-palette.test.tsx` 增加同名覆盖 1 例）。`pnpm --dir web exec next build`：通过。日志：`web-test.log`、`next-build.log`。
- e2e：**`pnpm --dir e2e test:fresh` → 162 passed（7.5m）**。162 = 上轮 160 + 本批两条（`60-session-search` 的「加载中 Enter」、`75-skill-shadow`）。**为什么用 fresh**：先在同目录跑了一次（`full-e2e-run1.log`），161 passed / 2 failed——一条是我在运行开始时删掉的临时调试 spec（Playwright 已枚举到文件但加载不到，属于自造噪音），另一条是 `59-auto-naming` 的侧栏项目名没有刷新：该目录此时已有 **934 个项目**，属于隔离工作之后已知的「复用目录无限增长」退化，不是本批代码路径。换成 fresh 后 162 条全绿。日志：`e2e/.artifacts/fix-palette-shadow/full-e2e-fresh.log`。
- `pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入，未打印、未落盘）：run `20261030-125125`，**75 个检查全部 PASS、`failure` 为空**；一次性容器 `pf-drill` 与 volume `pf-drill-data` 已删除，`pf-local` 全程未被触碰。日志：`e2e/.artifacts/fix-palette-shadow/deliver-check.log`，summary：`e2e/.artifacts/delivery-drill/20261030-125125/summary.json`。
- 镜像与部署：`pwsh scripts/build-app-image.ps1` 重建 `piforge:local`（`sha256:5f6db203eb4e…`，drill 断言的同一 id）；`pf-local` 用**同一个数据卷** `368d6186adb8…` 重建（新容器 `5867a7de9c58…`，端口 7722→7700，环境仍是 `PIFORGE_DEFAULT_PROVIDER=volc` 与四个 seed 账号）。重启后 `/healthz` = ok，`GET /v1/providers` 里 `volc` 仍 `enabled=true`、`has_key=true`（`models=1`）；`GET /v1/skills` 每一行都带 `effective`。
- 部署探针 `e2e/scripts/fix-palette-shadow-probe.mjs`：**旧构建 8/18 失败**（`selected=0`、Enter 不跳转、无 `effective`、两个同名行无提示、`/` 面板同时列两行且版本对不上、停用后列表不说明回退）；**新构建 18/18 通过**。截图：`palette-before-enter.png`（输入后第一行已高亮）、`palette-opened.png`（无 ArrowDown 直接 Enter 后进入会话）、`shadow-settings.png`（生效中 / 覆盖共享版本 / 已被同名私有 Skill 覆盖 / 解释行）、`shadow-palette.png`（`/` 面板只有生效行并写明覆盖）、`shadow-fallback.png`（停用私有后「已停用 · 使用共享版本 v1.0.0」）。日志与机器可读结果：`probe-run.log`、`probe-log.json`。
- fail-first：命令面板旧代码在「保留新 spec、删掉 ArrowDown」后必失败——`60-session-search` 的 URL 断言拿到 `http://127.0.0.1:3100/`（失败的页面快照：`old-code-e2e-fail.png`；同一次失败里的可访问性快照显示 listbox 只有会话行、没有任何 selected）。FR-14.1 的新测试无法在旧代码上编译/通过（`internal/skills` 与 `contracts.Skill.Effective` 都不存在），部署探针在旧构建上量到了同样的缺失（8/18）。

## 本批：FR-12.1 / FR-12.2（diff 作用域 + 逐 hunk UI）与 e2e 数据目录清理

### 1. e2e 数据目录：只删自己造成的，不再让它长到把无关 spec 拖垮

- **症状**：`e2e/.data` 只增不减。`freshAccount` 每个 spec 建一个账号，产品故意没有删账号接口；`openNewSession` 每次建一个项目，`53` 一轮建 42 个，`60` 建 2 个项目 40 个会话，`61` 留下改名/删除用的项目。目录大到 ~934 项目 / ~23k 文件后，连不碰项目的 spec（登录、侧栏、项目列表）也开始在复用的目录上超时，唯一能绿的跑法是 `test:fresh` 的 7.5 分钟重建——而“重建才能绿”正是让人学会忽略红色的方式。
- **删什么**（`e2e/scripts/clean-data.mjs`，`prepare.mjs` 在构建前调用）：除两个种子账号外的**所有账号**（providers / skills / MCP / API keys / feature flags / auth sessions / 会话随 schema 的 `ON DELETE CASCADE` 一起走）；**所有项目**（种子不建项目，所以种子账号上的项目一定是 spec 建的）；以及按 id 对照的磁盘残留：`sandboxes/<user>/<project>`、`sandboxes/_state/<user>/<project>.json`、`pi-agents/<session>`、`artifacts/<session>`、`skills/<id>`。不按名字猜，删哪条由数据库里仍然活着的 id 决定。
- **不删什么**：`admin@piforge.local` / `dev@piforge.local`、部署级共享 provider、三条种子 skill、`master.key` / `secret.key`。预检（`tests/preflight.setup.ts`）读的就是这些，清理后它的起点只会更干净。数据库里两个种子账号缺失时脚本**报错退出**，不会把别的部署当 e2e 目录清掉。
- **打印**：每次跑先打印删了多少账号/项目/会话/沙箱行/文件（含 MB）和前 10 个项目名，再打印清理后的账号/项目/会话/文件数与目录大小。
- **阈值告警**：超过 300 项目或 8000 文件时，启动先打印 WARNING 点名两个计数与 `pnpm --dir e2e test:fresh`；清理后仍超阈值则再告警一次，并说明“剩下的不是这套清理认识的写入者，要把它加进 `clean-data.mjs`”。演示（合成目录，非真实数据）：`e2e/.artifacts/task2/degradation-warning.txt` —— `471 projects (past the 300-project line)`、`8002 files (2.8 MB; past the 8000-file line)`。
- **同一 `.data` 连续运行**（每次都跑清理，从不 `--fresh`）：

| 运行 | 起跑前（上一次的残留） | 清理后 | 结果 |
|---|---|---|---|
| 1（162 条） | 169 项目 / 7,594 文件 | 2 账号 / 0 项目 / 6 文件 | **162 passed (7.4m)** |
| 2（162 条） | 169 项目 / 7,477 文件 | 删 35 账号 / 169 项目 / 7,588 文件 → 6 文件 | **162 passed (7.5m)** |
| 3（162 条） | 169 项目 / 7,471 文件 | 删 35 账号 / 169 项目 / 7,471 文件 → 6 文件 | 第 83 条起登录 401，停止（见下） |
| A（163 条，含 `76`） | 157 项目 / 6,862 文件 | 删 30 账号 / 157 项目 / 6,862 文件 → 6 文件 | 第 152 条起同一症状，停止 |
| B（163 条，含 `76`） | 170 项目 / 7,638 文件 | 删 30 账号 / 157 项目 / 6,862 文件 → 6 文件 | **163 passed (7.6m)** |

  跑完后的规模：7,594 → 7,477 → 7,477 → 7,638 文件，169 → 169 → 169 → 170 项目。也就是说，第 N 次运行后的目录规模由“一次运行会造多少”决定，不再由“跑过多少次”决定。日志：`e2e/.artifacts/task1/run{1,2,3}.log`、`e2e/.artifacts/task2/full-e2e-{1,2}.log`、`run{1,2}-after.txt`、`after-runB.txt`。

- **第三次/第 A 次没有全绿：另一个缺陷，已定位到原因并修掉**（不是数据增长，也不是清理）——两次起跑前目录都已回到 2 账号 / 0 项目 / 6 文件，症状是 `/v1/auth/me` 对**刚登录**的 cookie 返回 401，直接查库该 token 行存在，重启 `piforged` 后同一个 cookie 立刻 200（`e2e/.artifacts/task1/debug-wedge.log`）。四个并发探测把原因钉死：读池 4 条连接里**有一条停在旧快照上**（`found:0`），另外三条都看得到那一行（`found:1`），而 `sql.DB.Stats()` 说 `inuse=0 idle=4`，也就是池子把一条“在事务里”的连接当成空闲连接发了出来。复现脚本 `e2e/scripts/repro-auth-wedge.mjs`（不停 abort 流式/列表读 + 每 200ms 登录一次再 `/me`）在旧二进制上 **33s / 77s 各楔死一次**；修好后同一脚本 **300s / 1093 次探测零 401**，期间日志出现 7 次 `store: a read connection served a stale snapshot; dropping the idle read pool`——即条件仍会出现，但每次都被修复而不是变成永久 401。修法：`AuthSessionUser` 在读池未命中时向**写连接**（提交这条会话的那条连接）复核，命中即认定读池陈旧，丢掉空闲读连接（`SetMaxIdleConns(0)` 再恢复）让后续所有读一起恢复；写连接也说没有才是真的未登录，写连接报错则按错误上报而不是 401。回归测试 `store/auth_stale_read_test.go` 用一个“检查点后复制出来的数据库”当读池（真实旧快照），旧代码红在 `a stale read pool lost a live session: store: auth session: not found`，新代码绿；日志：`e2e/.artifacts/task2/repro-auth-wedge{,-fixed}.log`。**仍未定位**的是“为什么某条读连接会停在旧快照上”：`BEGIN/COMMIT`、被取消的查询、提前 `Close`、完整扫描四种模式在 store 层都没能制造出来，所以修复是“检测 + 修复连接池 + 鉴权以写连接为准”，不是“重试一次”。

### 2. FR-12.1 / FR-12.2

- **范围是真的换命令**：`contracts.ScopeRange` + `DiffRequest.From/To`；`gitx.Diff`/`DiffPatch` 走 `git diff <from>..<to>`，两端各自 `safeRevArg`（`..`、`-` 开头、空白一律拒绝，范围不会被拼成一个 revision）；`GET …/git/diff?scope=range&from=HEAD~1&to=HEAD`，缺 `from` 由 handler 返回 400 `invalid_argument`（不静默变成默认值）。`分支` 作用域保留原来的 merge-base 语义（`base...HEAD`），两者不是同一个东西。
- **逐 hunk 在 diff 视图里**：每个 hunk 一行（`git-hunk-row`，`data-index` 就是发给服务端的 index），工作区给「暂存」「丢弃」，已暂存给「取消暂存」；历史范围显示 `git-hunk-readonly` 且不画按钮——往已经存在的提交里暂存 hunk 不是一件事，能点却必然失败比没有更糟。`丢弃` 仍带确认（按 hunk 只还原那一段）。
- **历史作用域能列文件**：变更列表只认识工作区，所以“某个提交改了、但工作区干净”的文件原本点不开，而那正是 review 的场景。`范围`/`分支` 从自己的 diff 列文件（`git-scope-files`，每项 `git-scope-file` 带路径与 +/−），并默认选中第一个。
- **顺带修掉的坑**：提交原来把 `stagedPaths` 再交给后端 `git add` 一遍，于是“只暂存一个 hunk 再提交”会把另一个未暂存的 hunk 一起带走。现在 `git-panel.tsx` 用 `paths: []` 提交索引原样内容（`gitx.Commit` 的 paths 语义没动，避免影响其它调用方）；`76` 在第一次提交后断言仍有一行未暂存且内容是另一条改动。
- **fail-first（分层证明）**：只换回旧前端 → `76` 失败在 `getByTestId('git-diff-scope')` element not found（`e2e/.artifacts/task2/fr-12.1-fail-first-ui.log`）；只换回旧后端 → `76` 的 `git-scope-files` 停在 `data-files=0`（`scope=range` 被 400），即范围内容根本没取到（`fr-12.1-fail-first-backend.log`）。

### 3. 门禁（本批）

- `go build ./...`：0；`go vet ./...`：0；`go test ./... -count=1`：全绿（含新增 `gitx/diff_range_test.go`、`httpapi/git_diff_scopes_test.go` 与 `store/auth_stale_read_test.go`）。日志：`e2e/.artifacts/task2/go-test.log`、`go-test-after-auth-fix.log`。
- `pnpm --dir web test -- --run`：**25 个文件 / 201 例通过**（新增 `lib/git/diff-scope.test.ts` 5 例）。`pnpm --dir web exec next build`：通过。日志：`web-test.log`、`next-build.log`。
- 完整 e2e（同一份被反复复用的 `.data`，没有一次 `test:fresh`）：**163 passed / 7.6m、163 passed / 7.9m、163 passed / 8.5m**——最后三次连续全绿，中间那次就是修复后的验证跑。日志：`e2e/.artifacts/task2/full-e2e-{2,3,4-authfix}.log`。修复前的两次楔死（第 3 次 162 条在 83 条处、A 次 163 条在 152 条处）见上。
- `pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入，未打印、未落盘）：鉴权修复前的镜像 run `20261030-140727`，修复后的镜像 run `20261030-144953`，**两次都是 75 个检查全部 PASS、`failure` 为空**；每次的一次性容器 `pf-drill` 与 volume `pf-drill-data` 都已删除，`pf-local` 容器 id 前后一致。日志：`e2e/.artifacts/task2/deliver-check.log`、`deliver-check-2.log`，summary：`e2e/.artifacts/delivery-drill/20261030-140727|20261030-144953/summary.json`。
- 镜像与部署：`pwsh scripts/build-app-image.ps1` 重建 `piforge:local`（鉴权修复前后各一次，最终镜像即 drill 断言的同一个）；`pf-local` 用**同一个数据卷** `368d6186adb8…` 重建（最终容器 `3ffec11e5253…`，端口 7722→7700，环境仍是 `PIFORGE_DEFAULT_PROVIDER=volc` 与四个 seed 账号）。重启后 `/healthz` = 200，`GET /v1/providers` 里 `volc` 仍 `enabled=true`、`has_key=true`。

## 本次修复（FR-12.3：行内评论 → steer）

### 1. 三个设计选择，以及为什么是这三个

- **消息正文由服务端拼。** 客户端只发锚点（`path/line/side/hunk/quote/body`），服务端（`internal/httpapi/review_comment.go`）拼成 agent 收到的那一段文本。两个理由：① agent 读到的、transcript 显示的、库里存下的必须是**同一个字符串**，否则「用户以为他说了」和「agent 收到的」会分叉；② 只有服务端能拿工作区里**当前**的文件来核对行号。
- **行号会跑，所以给了明确语义**（任务要求「决定并记录」的那条）。服务端把引用行与当前文件逐行比对：

  | 情况 | 消息末尾的说明 |
  |---|---|
  | 引用代码仍在第 N 行 | `位置：这段代码现在仍在第 N 行。` |
  | 引用代码挪到第 M 行 | `位置：这段代码现在在第 M 行（写评论后文件改过，行号有偏移），以当前文件为准。` |
  | 找不到 | `位置：当前文件里找不到这段代码（可能已被修改或删除），请以 diff 和评论为准。` |
  | 这行是 diff 的删除行 | `位置：这一行在 diff 里是删除行，当前文件里没有它。` |
  | 文件读不到（无 worktree/沙箱不可用） | `位置：读取当前文件失败，引用来自 diff。` |

  锚点（`行内评论 · path:line`）**永远保持用户点击时的行号**，不静默改写：行号是用户指的东西，改掉它就等于换了一个问题。说明里写的是「现在的事实」。
- **发送走同一个消息端点。** `POST /v1/sessions/{id}/messages` + `review_comment`；只有在有 run 时才带 `streaming_behavior:"steer"`（idle 的 steer 服务端会拒绝）。没有第二条队列、没有第二套投递路径。

草稿是**会话级、落 localStorage**（`lib/review/comments.ts`，键 `piforge.review.drafts.v1`，按 sessionId 分区），所以重载还在；发送成功后删掉对应草稿（否则会重复发送），正文清空也等于删除（diff 里不留空标记）。

### 2. diff 渲染器换了，因为旧的那个一行都没有

`@git-diff-view/react@0.1.8` 在这个构建里不渲染行：`DiffUnifiedView` 抛错、`DiffBoundary` 回落到「原始 patch」分支，而这个分支是**一整段** `CodeBlockContent` 字符串——点不到任何一行，也拿不到行号。证据：组件测试里渲染 `GitDiffView` 之后 `{"view":false,"raw":true,"rows":0}`，而 `git-diff.tsx` 的注释本来就写着「renders an empty table for every patch」。所以换成本批自己的逐行渲染器（`patch-view.tsx` + `lib/git/patch-lines.ts`）：保留 `git-diff-view` / `git-diff-patch` 两个 testid 与 `+`/`-` 标记（`48-change-list` 断言的 `/\+fresh/` 仍然成立），old/new 行号来自 `@@` 头；并排视图是「同 hunk 内按顺序配对、多的一侧留空」，不做相似度对齐——那会是猜测。

### 3. 证据

- Go：`httpapi/review_comment_test.go` 7 组（引用与锚点、挪行、代码消失、删除行、读不到文件、6 种非法锚点），其中两条走真实路由：agent 收到的 prompt 必须含 `src/cache.ts:42`、`  42 |   return cache.get(key);`、`评论：这里没有处理未命中。`；非法锚点返回 400 且**不触达** agent。
- **fail-first（后端，跑出来的）**：把 `in.Text = composed` 去掉后同一条测试红在 `POST messages: 400 {"error":{"code":"invalid_argument","message":"text or images are required"}}`（`e2e/.artifacts/inline-comment/fail-first-backend.log`），恢复后绿。
- **旧代码里没有入口（命令输出，不是印象）**：`git show HEAD:web/components/workspace/git-diff.tsx` 只有 `git-diff-patch` 与 `git-diff-view` 两个 testid，`diff-line-comment|review-comment|onCommentLine` 命中 0；旧 `code-preview.tsx` 的 `file-line-comment|onCommentLine` 命中 0；旧 `message_handlers.go` 的 `review_comment|ReviewComment` 命中 0。所以 77 在旧构建上第一步就是 element not found（文件行入口同理），旧后端会忽略该字段、只把正文当普通消息发出去（transcript 里不会出现 `行内评论 · f.txt:3`）。
- Vitest：`lib/review/comments.test.ts` 9 例（持久化后「重载」仍在、会话分区、就地更新、清空即删除、损坏 JSON 不炸、坏行被丢弃、路径/行过滤、订阅）；`lib/git/patch-lines.test.ts` 8 例（old/new 行号、hunk 归属、无 hunk、hunk 内空行、锚点（含删除行）、配对）。
- e2e：`77-inline-comment.spec.ts` 一条走完：行上 `diff-line-comment` → 编辑器显示 `f.txt:3` 与引用 → 保存 → `review-comment-row` + `diff-comment-count`「1 条行内评论」+ `diff-line-marker[data-line="3"][data-count="1"]` → **reload 后草稿还在** → 发送 → `POST …/messages` 200 → 草稿与计数消失 → `turn-user` 里出现 `行内评论 · f.txt:3`、`line 2 EDITED-FOR-COMMENT`、评论正文与 `评论：`。transcript 是**agent 事件流**喂的，所以这一条就是「agent 收到了带引用的 prompt」的外部证据。

### 4. 还差的（不夸大 DONE）

- 多行选择（拖选一个范围）没有；一次评论只能锚在一行上，消息格式已经支持 `end_line`。
- 文件预览里的行评论没有独立的「标记」视图：草稿存在同一个 store 里，diff 里能看到同一个 `path:line` 的标记，但预览页自己不再重复列一遍。
- 没有「发给 agent 前先看一眼拼好的正文」的预览；正文由服务端拼，用户看到的是 transcript 里的事实。

## 读连接旧快照：触发条件、直方图与修复

### 1. 检测器现在记什么，以及为什么只能这么做

`*sql.Conn` 没有 id，`database/sql` 也不会告诉驱动「这条语句跑在哪条连接上」。唯一能给连接一个身份和历史的地方是驱动边界，所以 `store/sqlprobe.go` 用 `driver.Connector` 把 modernc 的驱动包了一层：每条连接有 id、pool（read/write）、谁打开的，以及最近 8 次操作的环形记录——**store 方法名**（`runtime.Callers` 取栈里第一个本包帧，所以是 `(*Store).ListEvents` 这种名字，不是数据库层的 `Query`）、SQL 前缀、开始时间与耗时、错误、读了几行、是否**一行都没读就被关掉**；另有 `in_flight` / `tx_depth` / `open_rows` / `resets` / `abandoned` / 最后一次错误的 tag 与时间。成本：每条语句一次 `runtime.Callers` + 少量帧解析（几微秒），每条连接一次 map 登记（池里一共 5 条）；`PIFORGE_SQL_PROBE=0` 可关。

检测点仍在 `AuthSessionUser`：它把读连接**钉住**（`s.r.Conn`）再查，未命中时先在这条连接上问四个问题，然后才问写连接：

- 同一条连接再查一次（`same_conn_sees_row`，以及不带过期条件的 `same_conn_sees_token`，用来把「行不存在」和「被时间条件挡掉」分开）；
- `PRAGMA data_version`；
- 另外两张表（`auth_sessions` / `session_events` / `projects` 的行数与最新时间）；
- **`BEGIN` 探针**：能成功 = autocommit（不在事务里）；报 `cannot start a transaction within a transaction` = 在事务里。这是对「是不是卡在事务里」的直接测量，不是推断。

探针用**脱离请求 ctx** 的 3s 超时执行。用请求 ctx 的话，客户端在 `BEGIN` 与 `ROLLBACK` 之间断开就会留下一个开着的写事务——那等于用探针制造它要测的东西（第一版就是这么写的，这里记下来）。

### 2. 十分钟探测（`e2e/scripts/repro-auth-wedge.mjs`，部署实例 127.0.0.1:7722）

- **`--seconds 600 --abort-pause 4`：600.1s / 2354 次探测 / `0` 次 401 / 项目列表漏 0 次 / **stale read 检测 0 次**（`e2e/.artifacts/task1-probe/probe-10min-4.log`，服务端日志 `server-10min.log`）。**没有任何一次检测**，所以这一次没有 tag 直方图可画——原因见 §3。
- 为了证明「条件还在、只是被处理掉了」，又在同一镜像上用**和修复前完全相同的强度**跑 120s（`--abort-pause 0`，即 harness 原来的形状）：**418 次探测 / 0 次 401 / 0 次项目漏 / 0 次 stale read / 514 条连接因「语句被中断」退役**（`e2e/.artifacts/task1-probe/server-120s-pause0.log`）。退役日志是**限流采样**的（每秒最多一行，共记了 13 行），里面 `last_err="context canceled"`，`last_err_on` 的分布是 `(*Store).Session` 7、`(*Store).authSessionUserOn` 5、`(*Store).ListEvents` 1。
- **修复前**（同一个 harness、同一个镜像只差退役这一处）：两次窗口各约 107s，分别 **6 次**和 **5 次** stale read；每一次的 `last_err` 都是 `context canceled`，`last_err_on` 是 `(*Store).ListProjects` / `(*Store).Session` / `(*Store).authSessionUserOn`，`txn_state=autocommit` 6/6，`open_rows=0` 6/6，`abandoned=0` 6/6，`same_conn_sees_row=false` 6/6，`write_sees_row=true` 6/6。这两次窗口的容器日志随重建丢了（只剩当时的输出），所以**结论不建立在这两个数字上**，而建立在 §3 的机制与 §4 的对照上。

顺带一个与本次改动无关的观察：harness 用 `--abort-pause 0` 时会把部署实例压到 ~470% CPU，随后 loopback 端口开始 connect timeout（`PIFORGE_SQL_PROBE=0` 的对照跑同样如此，所以不是诊断代码造成的）。这也是为什么十分钟那次用了 `--abort-pause 4`。

### 3. 结论：触发条件是什么，不是什么

- **不是** BEGIN/COMMIT 或应用层事务：被检测到的连接 `tx_depth=0`，`BEGIN` 在同一连接上全部成功（`txn_state=autocommit`）。
- **不是** 打开着没关的结果集：`open_rows=0`、`abandoned=0`（被 abort 的查询根本没返回 rows）。
- **不是** 写连接或隔离级别：写连接读得到那一行，读连接连**不带过期条件**的 token 行都看不到。
- **不是** store 层的用法（全表扫描、提前 Close 之类）：失败的操作 tag 各不相同，历史里也没有全扫。
- **是**：**语句被中断**。每条出问题的连接，最后一次错误都是 `context canceled`（客户端 abort 掉的请求，主要是事件流与列表读），之后它就从「中断那一刻」的快照作答——autocommit、没有打开的结果集、没有事务，却看不到中断之后提交的行。驱动层留下的 pager 读事务状态停在旧快照上，这是 modernc 的连接状态，不是我们 SQL 的形状。
- **仍未回答**：驱动里究竟哪一步把状态留下。我们没能在 Windows 上复现：`store/stale_snapshot_repro_test.go` 用同样形状（6 万行、reader 在 goroutine 里、2ms 后 cancel、再并发 Close）在本机 40 次里一次也没冻住，所以这条测试现在是「一旦某些平台/版本上复现就报错」的哨兵，不是修复证明。

### 4. 修法：被中断过的连接退役，不再发给下一个请求

`store/sqlprobe.go`：语句以 `context.Canceled` / `DeadlineExceeded`（或错误文本含 interrupt）结束时给该连接打 poison；`ResetSession` 见到 poison 返回 `driver.ErrBadConn`，database/sql 会关掉它并换一条新连接。于是「冻结的连接」不会被下一个请求继承。这不是修好了驱动，是**不把已知坏掉的连接发出去**；`PIFORGE_SQL_PROBE=0` 也不会关掉它（它是正确性，不是诊断）。原有的「检测 → 丢空闲读连接 → 鉴权以写连接为准」保留为第二道保险。

测试：`store/connection_retirement_test.go` 两例——被中断的连接在下次签出时确实被换掉、且新连接看得到中断之后提交的行；健康的连接**不会**被换掉（避免变成「每条查询一条新连接」）。

### 5. 风险（写清楚，不藏）

- 每次客户端 abort 都会退役一条连接，abort 风暴下有可观的建连开销（本地文件库上是 µs 级；远端/加密盘上更贵）。这是用「多开一条连接」换「不返回旧数据」。
- 只覆盖**经过 store 的、同进程内的**中断。驱动里其它可能造成旧快照的路径没有覆盖；如果还有，检测器仍会记录并修复连接池（第二道保险还在）。
- `PRAGMA data_version` 在两个连接之间不可比（它是每条连接自己的变化计数），日志里只当辅助信号，不据此下结论。
- 退役策略是在**部署实例（Linux 容器）**上验证的；单测在 Windows 上证明的是策略本身（连接被换掉、健康连接被复用），不是驱动的那个缺陷。

## 本轮门禁（FR-12.3 / 读连接旧快照）

- `go build ./...`：0；`go vet ./...`：0；`go test ./... -count=1`：全绿（新增 `httpapi/review_comment_test.go` 7 组、`store/stale_read_probe_test.go` 2 例、`store/connection_retirement_test.go` 2 例、`store/stale_snapshot_repro_test.go` 1 例哨兵）。
- `pnpm --dir web test -- --run`：**27 个文件 / 218 例通过**（新增 `lib/review/comments.test.ts` 9 例、`lib/git/patch-lines.test.ts` 8 例）。`pnpm --dir web exec next build`：通过。
- 完整 e2e（同一份被复用的 `.data`，未 `test:fresh`）：**164 passed / 7.9m**（GATE_RUNS）。新增的 `77-inline-comment.spec.ts` 让总数从 163 变 164。
- 部署：`pwsh scripts/build-app-image.ps1` 重建 `piforge:local`；`pf-local` 在同一个卷 `pf-local-data` 上重建（端口 7722→7700，`PIFORGE_DEFAULT_PROVIDER=volc`），`/healthz` 200、`dev@piforge.local` 能登录、`GET /v1/providers` 里 `volc` 仍 `enabled=true` / `has_key=true`。
- 十分钟探测：见上（600.1s / 2354 次探测 / 0 次 401 / 0 次 stale read；另有一次同强度的 120s 对照，514 条连接因中断退役）。
- `pwsh scripts/deliver-check.ps1`：DRILL_RESULT

## 本轮门禁（FR-12.6 一键开 PR）

- `go build ./...`：0；`go vet ./...`：0；`go test ./... -count=1`：全绿（新增 `forge/forge_test.go` 7 例、`gitx/pr_test.go` 4 例、`gitx/push_reject_test.go` 3 例、`httpapi/pr_test.go` 7 例）。日志：`e2e/.artifacts/fr126/go-test.log`。
- `pnpm --dir web test -- --run`：**28 个文件 / 223 例通过**（新增 `lib/git/pr.test.ts` 5 例）；`pnpm --dir web exec next build`：通过。
- 完整 e2e（复用同一份 `.data`，未 `test:fresh`）：**164 passed / 7.9m**，与上一批同数——本批没有新增 spec，PR 的端到端证据在部署实例上（见下），不是靠 fake peer 的本地套件。日志：`e2e/.artifacts/fr126/full-e2e.log`。
- 部署：`pwsh scripts/build-app-image.ps1` 重建 `piforge:local`（`sha256:2cdba2cd6585…`）；`pf-local` 在**同一个数据卷** `368d6186adb8…` 上重建（新容器 `9f0de87b7f5d…`，端口 7722→7700），环境仍是 `PIFORGE_DEFAULT_PROVIDER=volc` 与四个 seed 账号；重启后 `/healthz` 为 ok，`GET /v1/providers` 里 `volc` 仍 `enabled=true`、`has_key=true`。
- **部署实测（`e2e/scripts/fr126-verify.mjs`）**：项目配置指向宿主机上的 fake forge（`http://host.docker.internal:7799`，容器里可达），git 远端是容器 `/data/fr126/*/origin.git` 里的真实 bare 仓库；**19 项断言全过**——预览的分支/基准/提交数/文件、标题与正文预填、创建后链接、forge 收到的 bearer token 与 title/head/base、query 里没有 token、远端确实有该分支、重开对话框显示同一个 PR 且没有第二次 POST、forge 返回 422 时界面显示 `第 3 步 · 创建 PR` + `HTTP 422` + 原始 `Validation Failed: base branch main is protected` 且不显示链接。截图：`e2e/.artifacts/fr126/fr126-preview.png`、`fr126-created.png`、`fr126-existing.png`、`fr126-error.png`；日志：`e2e/.artifacts/fr126/probe-log.json`。
- `pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 读入，未打印、未落盘）：run `20261030-170745`，**75/75 PASS，failure=null**；一次性容器 `pf-drill` 与 volume `pf-drill-data` 已删除，`pf-local` 全程未被触碰（drill 前后容器 id 都是 `9f0de87b7f5d…`）。日志：`e2e/.artifacts/fr126/deliver-check.log`，summary：`e2e/.artifacts/delivery-drill/20261030-170745/summary.json`。

### FR-12.6 的设计决定（写进代码注释，也写在这里）

- **forge 抽象而不是 GitHub 硬编码**：`internal/forge` 是一个只做「列出 head→base 的 open PR」和「创建 PR」的 HTTP 客户端，按 `kind` 选方言。github 方言适用于 GitHub / GitHub Enterprise / Gitea / Forgejo（同一套 REST 形状）；gitlab 方言是 `PRIVATE-TOKEN` + `merge_requests`。没有做「任意 URL 模板」：请求体、认证头和响应字段都不同，用字符串模板覆盖它们只会把一次错误的猜测变成用户要 debug 的 404。
- **项目级配置，不是全局 token**：token 同时代表一个人和「他能写哪些仓库」，全局 token 会让所有项目的 PR 都以同一身份开出，包括 token 主人没打算碰的仓库；代价是每个项目多填一次，这是有意的取舍。
- **token 用既有的 sealed secret store**：与 provider key 同一个 `crypto.Box`（AES-256-GCM），密文进 `projects.forge_token_cipher`；API 只返回 `has_token`，token 只写一次、不回显、不进日志、不进 URL（forge 请求的 query 只有 head/base/state）。
- **推送是前置条件，错误按步归因**：`POST …/git/pr` 的 `details.step` 是 `config | push | create`；push 侧把 git stderr 分成 `ErrNoRemote` / `ErrPushRejected`（非快进）/ `ErrNoCredentials`，每种给出各自的下一步，原始 stderr 保留在消息里。forge 4xx 镜像它的状态码并原样保留 message，5xx 归 502；任何一步失败都不会返回一个 PR 链接。
- **幂等**：先 `FindOpen(head, base)`；POST 撞 422/409 时再查一次，查到就返回已有 PR（`created=false`），不创建第二个。本地 `pull_requests` 回执行是刷新后仍能看到链接的原因。
- **没做的**：PR 评论同步（行内评论仍只发给 agent）、PR 状态回写（merged/closed 不会更新本地回执）、多 PR 列表（只回答「这条分支」）、以及把 forge token 用于 `git push`（HTTPS push 仍需要 git 自己的凭据；工具会明确说出来，而不是假装 token 万能）。

## 本批：验收 pass 3 的三个发现（provider 生命周期 / 启动守卫 / 文件与文案）

验收证据：`e2e/.artifacts/acceptance3/stage{1,2,3}.md`。部署 `http://127.0.0.1:7722`（`dev@piforge.local`、`admin@piforge.local`），共享 `volc` 可用。本批三个修复提交：`85b7fca`（会话模型生命周期，HIGH）、`7181596`（默认服务商启动守卫，MEDIUM）、`26a5970`（文件面板 / 按钮可访问名 / 文案，minor）。

### 根因（复现出来的，不是推测）

- **HIGH：会话在 provider 变更后必死。** 旧路径上没有任何一步会重新读 provider 列表：`handlePostMessage` 只对**显式** `model_ref` 做一次检查，而检查失败时直接 400；更关键的是 agent 进程的 `models.json` 与网关 token 是**每次 spawn** 生成的，一个在 provider 缺失期间启动的进程会带着空配置活到进程结束，之后即使 provider 回来了，`ensure()` 仍复用这个进程，`pi` 报 `No API key found for the selected model`，UI 只能显示「服务端内部错误，请稍后重试」。重试没有意义，因为没有任何东西会重建进程；header 的 `跟随默认（unknown）` 是 Pi 对「没有模型」的 `unknown/unknown` 占位符被当成模型名格式化出来的。
- **MEDIUM：启动不校验默认服务商。** `-default-provider` / `PIFORGE_DEFAULT_PROVIDER` 只被存进 `Deps.DefaultProvider`，从未与 provider 表比对；`PIFORGE_DEFAULT_PROVIDER=volc` + 没有 volc 时安静启动，第一个信号是用户的一次 400（旧进程形态下是 500）。
- **minor：** 文件树只有空态有「新建文件」；停止按钮的可访问名是「停止 Esc」（可见文本 + 键位提示拼出来的），发送图标按钮在旧构建上没有名字；「跟随默认」是模型概念却和推理强度共用一套写法；对比页的「主会话 0 个文件」是正确的但没有任何解释。

### 改法

- `backend/internal/httpapi/session_models.go`（新）：`resolveSessionModel` 在每次发送前重解析 `model_ref`——provider 被删除/停用时改用部署默认（再退到按名字排序的第一个可用 provider），把新引用写回 session，并把 `model_ref` / `model_fallback_from` 放进 `PromptResult`；provider 存在但没有 key / 没有模型仍然返回原来的 typed `no_model`（不静默换模型）。`resolvedModelRef` / `model_unavailable` 让 header 在**没有活进程**时也能说出「会用什么」或「未配置」。
- `backend/internal/pi/runtime.go`：`StartedModelConfig` 记录进程 spawn 时是否拿到了平台写的 `models.json`（`PI_CODING_AGENT_DIR`），`modelRefOf` 把 `unknown` 归一成空；控制面据此识别「启动时没有模型配置」的活进程，在模型重新可用后先 `Stop` 再 spawn（`restartForUnconfiguredAgent`）。
- `backend/cmd/piforged/default_provider.go` + `store.ProviderNamed`：启动时若默认服务商不存在，记一条 WARN（非致命，写明后果）；`GET /v1/setup` 返回 `default_provider` / `default_provider_missing`，首次运行面板显示「默认服务商「volc」不存在，请添加或更改默认值」。
- 前端：`lib/session/model-display.ts` 统一 header 文案（已解析的模型 / 默认模型（…）/ 未配置，绝不 `unknown`）；发送失败为 `no_model` 时刷新 session/status/providers，让 header 与 no-model 条立即反映事实；自动切换模型时弹 toast 并刷新。文件树 header 加常驻 `file-create-header`；停止按钮 `aria-label="停止"`（发送按钮的 `aria-label="发送"` 本来就在，本批加断言）；模型选择器与对比页的「跟随默认」改为「默认模型」，推理强度改为「默认」；对比页加一行 `compare-explainer` 说明「改动」是相对分支基准、主会话就是基准所以对自己永远 0 个文件。

### 证据（部署实例，非 fake peer）

- 旧镜像（重建前）：`node e2e/scripts/probe-provider-lifecycle.mjs` → 删除 session 依赖的 provider 后发送 **400 `no_model`**，session 行仍指向已删的 provider，无 fallback；`GET /v1/setup` 没有 `default_provider` 字段。`e2e/.artifacts/fix-provider-lifecycle/lifecycle-api-OLD.json`。
- 新镜像（`piforge:local` `sha256:396c3a78…`；`pf-local` 用同一个数据卷 `9ce20975…` 重建，容器 `6f36ac21…`，端口 7722→7700，`volc` 仍在）：同一探针 → **200**，`model_ref:"volc/deepseek-v4-flash"`、`model_fallback_from:"accept3-dead-…/probe-model"`，session 行已写回新引用；`/v1/setup` 返回 `default_provider:"volc"`。`lifecycle-api-NEW.json`。
- 真实 UI（`e2e/scripts/verify-provider-lifecycle.mjs`，截图 7 张）：header 删除后不再出现 `unknown`，发送后变成 `deepseek-v4-flash` 且会话行 `model_ref=volc/deepseek-v4-flash`；toast「已自动切换模型…原模型…本轮改用…」；`composer-send` 的 `aria-label=发送`；文件树 header 的「新建文件」可见；对比页解释文案在；模型选择器只有「默认模型」、整页不含「跟随默认」。`e2e/.artifacts/fix-provider-lifecycle/verify.json` + `01…07-*.png`。
- 启动 WARN 与 `/v1/setup` 的实测（临时数据目录、`-default-provider volc-does-not-exist`）：日志 `level=WARN msg="default provider \"volc-does-not-exist\" does not exist: sessions that follow the default will answer 400 no_model until a provider with this name is added …"`，进程继续 listening；同实例登录后 `GET /v1/setup` 返回 `"default_provider":"volc-does-not-exist","default_provider_missing":true`。`startup-warn.log` + `startup-warn.log.setup`。

### 失败优先（新测试在旧代码上确实过不了）

- FIX 2：把 `setup_handlers.go` 还原后 `TestSetupReportsAMissingDefaultProvider` 失败在 `default_provider = ""`（日志 `fail-first.log`）。
- FIX 1：还原 contracts / message_handlers / session_handlers / pi runtime 后，新的 Go 测试**编译失败**（`PromptResult has no field ModelRef`、`RuntimeStatus has no field ResolvedModelRef`）——这些字段就是缺失的能力；用户可见的旧行为由上面的旧镜像探针证明（400 且不恢复）。
- FIX 3：把前端改动 stash 后跑同一批 e2e：`composer-stop` 的 `aria-label` 不存在、`file-create-header` 不存在、`compare-explainer` 不存在、`默认模型` 选项不存在（4 条失败，`fail-first-fix3.log`）。
- 78-changes-live：本批顺带把它的 setup 竞态证明清楚——API 提交后面板缓存的旧 status 不会被通知，断言在 20s 内等不到 0 行；改成提交后 reload 一次（面板仍在 run 之前打开，marker 之后的 no-reload 断言不变），`--repeat-each=2` 连续通过。

### 门禁

- `go build ./...`：0；`go vet ./...`：0；`go test ./... -count=1`：全部包 ok。
- `pnpm --dir web test -- --run`：**31 个文件 / 250 例通过**（新增 `lib/session/model-display.test.ts` 7 例）；`pnpm --dir web exec next build`：通过。
- 完整 e2e（复用同一份 `.data`，未 `test:fresh`）：**170 passed / 7.7m**（本批新增 `81-provider-lifecycle.spec.ts` 2 例 + 11-files 1 例 = 3 条；164 这个数在 `7e56ae7` 之后已被别的提交加过用例，按 `--list` 实数 170）。日志：`e2e/.artifacts/fix-provider-lifecycle-e2e-full3.log`。
- `pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入，未打印、未落盘）：run `20261031-120320`，**119 条 `[PASS]` / 0 条 FAIL**（源码树干净后重跑；第一次 118/119 的唯一失败是 drill 的「source checkout is clean」守卫，当时 `docs/STATUS.md` 与验证脚本还未提交）；一次性容器/volume 已删除，`pf-local` 容器 id 前后都是 `6f36ac21…`。日志：`e2e/.artifacts/fix-provider-lifecycle/deliver-check-clean.log`。

### 没做的 / 边界

- 只验证了「provider 被删除/停用」这一种失效；改 base URL 或轮换 key 不需要重建进程（网关每次请求都重新解析 provider），所以没有为它加重建逻辑。
- `model_ref` 指向一个存在但没有 key 的 provider 时仍然**不**自动换模型：那是配置错误，静默换模型会让用户以为自己的选择生效了；文案与设置页入口保持不变。
- 对比的「主会话 0 个文件」语义没有改，只加了说明；要问「主会话相对 main 改了什么」需要另一套基准，不在本批范围。
- 部署的数据卷仍是验收时的那个（`9ce20975…`），不是更早的 `pf-local-data`；本批只保证同一卷重启，不迁移数据。

## 本批：验收 pass 4 的「取消共享」两半（确认框 + typed 错误）

验收证据：`e2e/.artifacts/acceptance4/stage1.md`。部署 `http://127.0.0.1:7722`（容器 `pf-local`，共享 `volc` 是当时唯一可用的 provider，同时是 `PIFORGE_DEFAULT_PROVIDER`）。本批不改变任何 FR 状态，只修同一处混淆的两面：**共享 provider 的移除在界面上没有确认，在 API 上又在错误端点报 404。**

### 根因

- **界面：一次点击就删。** `/settings/providers` 的共享列表里，「取消共享」是一个只有垃圾桶图标的按钮，`onClick` 直接 `DELETE /v1/admin/providers/{id}`，没有对话框、没有撤销。验收 pass 4 中一次误点删掉了部署里唯一能用的 `volc`，只能手工重建。
- **API：404 说的是「不存在」，不是「端点不对」。** `DELETE /v1/providers/{id}` 走 `store.DeleteProvider`，SQL 只匹配 `scope='user' AND owner_id=?`；共享行是 `scope='admin'`，于是影响 0 行，`requireOneRow` 返回 `ErrNotFound`，对外就是 404 `not_found`「找不到这个对象，可能已被删除」。provider 明明在列表里、还能用，错误却让人以为它没了。

### 改法

- `backend/internal/contracts/contracts.go` 新增错误码 `shared_provider`，`httpapi/errors.go` 映射 409。`handleDeleteProvider` 在 `ErrNotFound` 时再查一次 `Store.Provider`：只有调用者可见的 `SourceInherited` 行才回 409，消息点名 `DELETE /v1/admin/providers/{id}` 与 provider 名；未知 id、别人的私有行仍然 404（不把 typed 错误变成探测手段）。删除自己的 provider 仍是 200。
- 前端 `/settings/providers`：共享行（管理区的「取消共享」与合并列表里管理员看到的共享行）都改成带文字的「取消共享」，打开同一个确认框；确认框写明 provider 名、所有继承用户会失去它、钉在「名称/模型」上的会话与跟随默认模型的会话下次发送会失败、key 一并删除且不可撤销；若它是部署默认（`GET /v1/setup` 的 `default_provider`），再加一条红色说明与「部署默认」徽章。自己的 provider 用带文字的「删除」，独立确认框，默认服务商时也提示。两个确认框的确认按钮都是 `variant="destructive"`。
- `web/lib/errors.ts` 为 `shared_provider` 加中文映射（保留 provider 名与 admin 端点），避免任何旧客户端把英文原文弹给用户。
- 测试：`backend/internal/httpapi/shared_provider_delete_test.go`（共享 → 409 + 命名端点 + 行仍在；未知 → 404；自己的 → 200；别人的私有 → 404）；`handlers_test.go` 里旧的「共享删除 = 404」断言改为新的 typed 错误；`web/lib/errors.test.ts` 新增映射断言；e2e `82-unshare-confirmation.spec.ts` 点击 → 断言确认框 → 取消后行与 API 都不变 → 确认后消失 → 管理 API 重建成功（并由 `withSharedProvidersClean` 保证跑完没有额外共享 provider）。部署验证脚本 `e2e/scripts/verify-unshare-confirm.mjs` 只走真实 UI/API，密钥从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入、不打印不落盘。

### 证据（部署实例，非 fake peer）

- 失败优先：先只加 `contracts.ErrSharedProvider` 与测试、保留旧 handler，`go test ./internal/httpapi -run TestDeletingASharedProviderThroughTheUserRouteExplainsItself` 失败在 `status = 404, want 409 (body {"error":{"code":"not_found",…}})`（`e2e/.artifacts/fix-unshare/red-newtest.log`）；改完 handler 同测试通过（`green-newtest.log`）。
- 新镜像（`piforge:local` 最终 `sha256:24d4abd4…`）用**同一个数据卷** `9ce20975…` 重启 `pf-local`（容器 `aaaf4fdd…`，端口仍是 7722→7700，`PIFORGE_DEFAULT_PROVIDER=volc`），`/healthz` 200。探针 `verify-unshare-confirm.mjs` **PASS**：
  - 错误端点：`DELETE /v1/providers/{volc}` → **409 `shared_provider`**，消息含 `/v1/admin/providers/{id}`，provider 仍在；
  - 确认框：标题「取消共享「volc」？」，含「所有用户」「下一次发送」，默认服务商红色提示含 `PIFORGE_DEFAULT_PROVIDER`（`1-dialog.png`）；
  - 取消：行仍在、API 仍 `has_key:true`（`2-after-cancel.png`）；
  - 确认：行消失、API 不再有 `volc`、`/v1/setup` → `needs_provider:true, default_provider_missing:true`（`3-after-confirm.png`，toast「已取消共享」）；同一旧 id 再删是 404 `not_found`；
  - 重建：管理 API 重建 `volc`（openai-completions、原 base URL、`deepseek-v4-flash`、key 来自凭据引用）→ 200、`has_key:true`（新 id）、`/v1/setup` → `needs_provider:false, default_provider:"volc"`；UI 刷新后「已配置 key」「部署默认」都在（`4-after-recreate.png`）。`verify.json`、`probe.log`。
- 最终状态（独立会话复核）：管理列表 `shared-anthropic`（无 key）+ `volc`（`has_key:true`）；`/v1/setup` → `needs_provider:false, shared_provider_available:true, default_provider:"volc"`。证据文件里没有出现密钥。

### 门禁

- `go build ./...`：0；`go vet ./...`：0；`go test ./... -count=1`：全部包 ok（`go-gates.log`）。
- `pnpm --dir web test -- --run`：**31 个文件 / 251 例通过**；`pnpm --dir web exec next build`：通过（`web-gates.log`、`web-build.log`）。
- 完整 e2e（复用同一份 `.data`）：**171 passed / 7.2m**（上一批 170 + 本批 `82-unshare-confirmation.spec.ts` 1 例）。日志：`e2e/.artifacts/fix-unshare/e2e-full-final.log`。
- 镜像与部署：`pwsh scripts/build-app-image.ps1` 重建 `piforge:local`；`pf-local` 用同一卷 `9ce20975…` 重建（容器 `aaaf4fdd…`），环境仍是四个 seed 账号、`PIFORGE_DEFAULT_PROVIDER=volc`。`docker inspect` 前后卷名一致（`deploy-2.log`、`image-build-2.log`）。
- `pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入，未打印、未落盘）：run `20261031-124731`，**119 条 `[PASS]` / 0 条 FAIL**，`failure` 为空；一次性容器/volume 已删除，`pf-local` 容器 id 前后都是 `aaaf4fdd…`、卷仍是 `9ce20975…`，源码树干净（drill 自己的守卫）。日志：`e2e/.artifacts/fix-unshare/deliver-check.log`；summary：`e2e/.artifacts/delivery-drill/20261031-124731/summary.json`。

### 没做的 / 边界

- `PATCH /v1/providers/{id}` 打在共享行上仍是 403 `forbidden`（store 明确拒绝用户编辑共享配置）。它不是「不存在」，但也不在本批的 404 混淆里；没有改成 `shared_provider`。
- 确认框不要求输入 provider 名（不是 GitHub 式二次输入）：本批要求是「显式确认 + 说清后果」，两步输入更适合不可恢复的批量操作。
- `/v1/providers` 创建与共享同名的 provider 仍返回 409 `conflict`（阴影规则的另一半），未改。


## 本批 FR-11.3：接管浏览器是一把独占租约

### 缺什么

`/v1/sessions/{id}/browser/mode` 只是内存里每个会话一个字符串：谁最后 POST，谁就是 `takeover`。浏览器属于**沙箱**（一个沙箱一个 Chrome），所以同一项目的两个会话看到的是同一页，两个标签页可以同时「接管」，输入门禁只看自己会话的字符串，于是两边都往同一页打字，谁也说不清哪一下是谁的。

### 改法

- 新包 `internal/browserlease`：`Claim` / `Renew` / `Release` / `Holder`，作用域是**沙箱**（`browserScope`：会话 → worktree → sandbox id；没有 checkout 时退回会话 id），owner = 用户 + 会话，TTL 45s（`-browser-takeover-ttl` / `PIFORGE_BROWSER_TAKEOVER_TTL`）。时钟是注入的，测试用 fake clock 而不是 sleep。
- 第二个接管：**409 `browser_busy`**。消息「浏览器正在被 <持有人> 控制，剩余 N 秒」，details 给 `holder` / `holder_user_id` / `holder_session_id` / `holder_session` / `remaining_seconds` / `expires_at`。持有人是同一个人同一会话时是续租，不是冲突。
- 释放：① 切回 observe → `Release`，立即生效；② 面板断线（不再发心跳）→ TTL 到期；③ 「观察」按钮本身就是显式释放。`effectiveBrowserMode` 在读取时把过期租约读成 observe，`Holder` 惰性删除过期项。
- 心跳：面板在接管期间每 `ttl/3`（2–30s 之间）发 `{"type":"heartbeat"}`；服务端只给 owner 续租，非 owner 的心跳返回 `owned:false`，**心跳永远不是接管**。
- 输入门禁：`input` 同时检查会话 mode 与租约（`held.Owner.Same(owner)`）。过期或别人的租约即使字符串还写着 takeover，输入照丢。
- UI：`browser-panel.tsx` 把 mode 改成 3s 轮询的 query；别人的租约显示「正在被 X 控制」徽章（`browser-holder`，title 带剩余秒数），接管按钮禁用；接管失败用服务端的中文消息（`humanizeError` 对含中文的消息原样透出）。
- 兼容：`GET/POST /browser/mode` 的旧字段 `mode` 不变，只是多了 `holder` 与 `ttl_seconds`；观察模式、单用户接管、`43-browser-mode.spec.ts` 全部照旧。

### 测试与失败优先

- `browserlease/lease_test.go` 5 例：grant + 拒绝并命名持有人与剩余时间、过期后释放、心跳只给 owner、显式释放只允许 owner、持有人文案。
- `httpapi/browser_takeover_test.go` 2 例：同一沙箱两个会话（第二个会话挂在第一个的 worktree 上）→ 第一个 200、第二个 409 + details 持有人与剩余秒数、切回 observe 后第二个成功；fake clock 过期后旧会话读回 `observe`、`holder` 消失、第二个可接管。
- e2e `83-browser-takeover.spec.ts`：两个浏览器上下文（同一个人的两个会话，同一项目 = 同一沙箱）。第二个面板出现「正在被 … 控制」且按钮禁用；API 409 `browser_busy` 且 `holder_session_id` 指向第一个；释放后第二个的真实 UI 接管成功。截图 `fr-11.3-controlling.png`、`fr-11.3-busy.png`、`fr-11.3-swapped.png`。
- **fail-first**：把 `Claim` 里的 holder 判断改成 `if false && …`（= 旧行为：后到者直接赢），`TestClaimIsExclusiveAndNamesTheHolder` 失败在「a second session took over a browser somebody else is driving」，`TestBrowserTakeoverIsExclusiveAndReleases` 失败在「second takeover: **200**」并打印了第二个会话的 `owned_by_me:true`。日志 `e2e/.artifacts/partial-batch3/fr-11.3-fail-first-lease.log`。恢复后两处都过。

### 边界

- 仓库里没有 Playwright MCP（FR-11.6 MISSING），所以没有 `mcp__playwright__*` 可以在接管时挂起。接管仍会中断当前 run；这把锁覆盖「人和人」以及「人 vs 已经在跑的工具」，不覆盖一个不存在的 agent 浏览器工具。
- 租约在内存里：控制面重启后没人持有是正确语义，不落库。
- 同一个用户、同一个会话的两个标签页是**同一个 owner**（任务定义的 owner 就是 user+session），因此它们不会互相拒绝；跨会话、跨用户才是被拒绝的一方。


## 本批 FR-14.2（续）：快照会回收，只读不再需要 SYS_ADMIN

上一轮把 skill 做成了「冻结 + 按平台显式命名机制」，但留下两个会随时间腐烂的洞：快照目录永不回收，以及默认部署为了只读而加 `--cap-add SYS_ADMIN`。本批各修一个。

### 缺什么（实测，不是推测）

- **快照只增不减**：`bind-ro` 每个会话在 `<data>/skill-snapshots/<session>` 写一份冻结树，删会话/删项目都不动它。部署实例上的 fail-first：建会话 → 目录出现（`ses-…`）→ `DELETE /v1/sessions/{id}` 返回 200 → 10 秒后目录仍在、容器内数量不变；旧数据卷上启动修复后的镜像时，janitor 第一轮清掉 **8 个**这样的历史孤儿（`count=8 bytes=14057 freed="13.7 KiB"`）。同类 bug：带**分支会话**的项目删除返回 **409**，因为 `.pi/skills` 的挂载点还在分支 worktree 里，`RemoveAll` 得到 EBUSY——上一轮 `unmountSkills` 只看了主 checkout。
- **默认部署要 SYS_ADMIN 才能只读**：没有它时机制退化成 `copy`，UI 写「副本（未只读）」。为一个「文件不可写」的属性在默认部署里加一个宽 capability，是拿容器边界换一个权限位就能给的保证。

### 改法 1：快照的生命周期（`internal/skillsnap` + janitor）

- 目录名就是**会话 id**（`ses_…`），不再用 slug：删除路径与清扫都不需要第二套映射。旧布局 `ses-…` 仍被识别为「可能是活会话」，升级时不会把在跑会话的快照扫掉；`Remove` 两种布局都删。
- **删会话**：`handleDeleteSession` 删行成功后删该会话的目录（`skillsnap.Remove`）。失败只记 warn——行已经没了，目录一定是孤儿，留给 janitor。
- **删项目**：`SessionIDsForProject` 在级联删除**之前**读出所有会话 id（含 branch/sidecar），删行成功后逐个删目录。被拒绝的项目删除（运行中 / 沙箱删不掉）不会走到这一步，快照跟着项目保留。
- **janitor**：启动即扫一轮，之后每个 `-sandbox-sweep`（默认 10m）一轮。判据仍是「数据库里没有这个会话」：`SessionIDs` 一次查询得到活集合，`LiveNames` 同时包含两种布局；目录名必须像 `ses[-_]…` 才动，外来目录一律不碰。删除前对目录及子目录 `umount(MNT_DETACH)`（普通目录 EINVAL，忽略；还在挂载时 detach 是唯一能删掉的办法），`RemoveAll` 失败先清只读位重试一次，仍失败**只记 warn 并等下一轮**，不中断整轮。日志一行：`janitor: removed orphaned skill snapshots count=… bytes=… freed=…`。读不到活集合时整段中止（不能把「store 没回答」当成「所有快照都是垃圾」）。
- 顺带修掉两处同源问题：`sandbox.unmountSkills` 现在递归处理 `worktrees/<slug>/.pi/skills`（分支会话的项目删除不再 409）；`provision.EnsureWorktree` 对已 attach 的 worktree 也重跑一次投影——容器重启会让挂载点变回空目录，不重跑的话该会话永远没有 skill（投影幂等，仍在「同一条 skill 行不重写」的冻结语义内）。

### 改法 2：机制决策顺序，默认不要 SYS_ADMIN

`PlanFor(goos, caps)` 的顺序是表，不是注释：

| 条件 | 机制 | 谁在拒绝写入 |
|---|---|---|
| 非 Linux（Windows/macOS） | `copy-ro` | 平台权限位（Windows = `FILE_ATTRIBUTE_READONLY`） |
| Linux，`CapEff` 有 `CAP_SYS_ADMIN` | `bind-ro` | 内核（bind + remount `MS_RDONLY`） |
| Linux，没有 `CAP_SYS_ADMIN` | `copy-ro` | 0444/0555 |
| copy 之后「以写打开 `SKILL.md`」成功 | `copy` | 没人：冻结但非只读，如实上报 |

`copy` 不是计划，是探测结果；无能力的部署因此不会再声称 bind-ro，也不会落到 copy。`copy-ro` 不需要任何 capability，**但 root 的 `CAP_DAC_OVERRIDE` 会忽略 0444**。这个 capability 由**部署**丢掉：`docker-compose.yml` 的 `cap_drop: [DAC_OVERRIDE]`（等价 `docker run --cap-drop DAC_OVERRIDE`），不是 `cap_add`。为什么不在进程里自己丢：Linux 只允许线程改**自己**的 capability 集（用 `capset` 指定别的 tid 返回 `EPERM`，在容器里实测过），Go 的 goroutine 跑在多条 OS 线程上，进程内丢只覆盖一条线程——那样的「只读」取决于调度，不能写进台账。忘了丢时产品照常启动，启动日志与徽章诚实报 `copy` 并提示加 `--cap-drop`。

UI：徽章的 `title` 与 `aria-label` 各一句可访问说明，区别是真实的——「只读挂载：agent 无法改写技能」/「只读副本：agent 无法覆盖，但可新建文件」（副本不是挂载）；`copy` 仍写「副本（未只读）」。

### 测试与失败优先

- **fail-first（部署，旧镜像/旧代码）**：`e2e/.artifacts/fix-gc/fail-first-probe.log`——建会话后快照目录出现，`DELETE` 返回 200 后 10s 目录仍在（`snapshot_dir_gone:false`，容器内数量 10→11）；带分支会话的项目删除返回 **409**，两个会话的快照都还在。
- **修好后同一探针**（同一镜像 + `--cap-add SYS_ADMIN` 的一次性容器，`e2e/.artifacts/fix-gc/snapshot-gc-probe-pass-final-image.log`）：机制 `bind-ro`；删会话 10s 内目录消失；删项目（主会话 + 分支会话）200 且两个目录都消失。
- **janitor 实跑**（`e2e/.artifacts/fix-gc/janitor-sweep.log`）：容器里手工放一个 `ses_01MJANITORORPHAN…` 目录（10 字节），3s 清扫周期内被删，日志 `count=1 bytes=10 freed="10 B"`；真实数据卷上启动时清掉 8 个历史孤儿（见「缺什么」）。
- **无 SYS_ADMIN 的部署探针**（`e2e/.artifacts/fix-gc/no-cap-probe.log` / `no-cap-skill-probe.json`）：`CapAdd=null`、`CapDrop=["CAP_DAC_OVERRIDE"]`、`piforged` 的 `CapEff=0xa80425f9`（无 bit21、无 bit1）；机制 `copy-ro`；PUT 既有 `SKILL.md` **500** 且内容不变；该会话没有快照目录；徽章「只读副本」+ tooltip/aria 原文。
- **Go**：`materialize/mount_plan_test.go` 的决策序列表（含无能力：不尝试 bind、不建快照、报 copy-ro）、`skillsnap/skillsnap_test.go`、`janitor/janitor_test.go`（只删无会话的、旧布局算活、先 detach、失败不中断且下一轮重试、索引失败拒扫、外来目录不动、count/bytes、dry-run）、`httpapi/skill_snapshot_test.go`（删会话只删自己的、删项目删主+分支、被拒的项目删除保留快照）、`provision/skill_mount_test.go`（投影丢失后修回来）。
- e2e 仍由 `84-skill-mount.spec.ts` 覆盖「机制 + 写被拒 + 冻结 + 徽章」（Windows 上是 copy-ro）。

### 边界（为什么仍是 PARTIAL）

- Windows 的 `copy-ro` 只挡住覆盖/删除已有文件；同一账号在目录里**新建**文件仍然可以。没有目录级不可变（要 `chattr +i` 或只读挂载，前者需要另一个 capability、后者需要 `SYS_ADMIN`），所以不把 copy-ro 说成 mount。
- `content_hash` 仍记在 `session_skills`（会话记录是事实），没有在挂载树里放标记文件——只读树上写不了，位置本来就不该在那里。
- 快照回收只在 `bind-ro` 产生的目录上有实际对象；`copy-ro` 不建快照（探针也断言了这一点）。旧布局目录在升级后如果属于活会话会一直留着（很小），属于已删会话的由 janitor 清掉。

## 本批门禁（FR-14.2 快照回收 + 无 SYS_ADMIN 的只读）

- `go build ./...`：0；`go vet ./...`：0；`go test ./... -count=1`：全部包 ok（`e2e/.artifacts/fix-gc/go-test.log`）。
- `pnpm --dir web test -- --run`：**31 个文件 / 251 例通过**；`pnpm --dir web exec next build`：通过（`web-test.log`、`web-build.log`）。
- 完整 e2e（复用同一份 `.data`，未 `test:fresh`）：**173 passed / 7.1m**（`full-e2e.log`）。本批新增的是**部署探针**而不是 e2e 用例：快照只在 `bind-ro` 下产生，而套件跑在 Windows（copy-ro），写进 e2e 只会是一条永远看不到对象的断言。套件仍由 `84-skill-mount.spec.ts` 覆盖机制/写拒绝/冻结/徽章，数目保持不变（173）。
- 镜像只用 `pwsh scripts/build-app-image.ps1` 重建：`piforge:local` = `sha256:739a9117…`（`image-build-final.log`）。
- 部署：`pf-local` 用**同一个数据卷** `9ce20975…` 重建——旧容器 `342be884…`（`--cap-add SYS_ADMIN`）→ 新容器 `382e46da…`；`HostConfig.CapAdd=null`、`HostConfig.CapDrop=["CAP_DAC_OVERRIDE"]`；端口仍是 7722→7700；`PIFORGE_DEFAULT_PROVIDER=volc`，`volc` provider 重启后仍可用（探针未改动它）。启动日志：`msg="skill mounts use read-only copies" mechanism=copy-ro`。
- **fail-first**（旧代码 + 旧部署，`fail-first-probe.log`）：建会话后容器内 `skill-snapshots/` 从 10 个变 11 个；`DELETE /v1/sessions/{id}` 返回 200 后 10s 目录仍在（`snapshot_dir_gone:false`）；带分支会话的项目删除返回 **409**，两个快照都留着。修好后（同一镜像 + `--cap-add SYS_ADMIN` 的一次性容器，`snapshot-gc-probe-pass-final-image.log`）：`bind-ro`，删会话 10s 内目录消失，删项目（主+分支）200 且两个目录都消失。
- **janitor 实跑**（`janitor-sweep.log`）：容器内放一个 `ses_01MJANITORORPHAN…`（10 字节），3s 周期的下一轮删除它，日志 `count=1 bytes=10 freed="10 B"`；真实数据卷上修复后首次启动清掉 8 个历史孤儿（`count=8 bytes=14057 freed="13.7 KiB"`，见「缺什么」）。
- **无 SYS_ADMIN 的部署探针**（`no-cap-probe.log` / `no-cap-skill-probe.json`）：`CapAdd=null`、`CapDrop=["CAP_DAC_OVERRIDE"]`；`piforged` 的 `CapEff=0xa80425f9`（bit21=0、bit1=0）；机制 `copy-ro`；PUT 既有 `SKILL.md` **500** 且读回的内容未变；该会话**没有**快照目录；UI 徽章 `data-mechanism=copy-ro`、文字「只读副本」、`title`/`aria-label` 都是「只读副本：agent 无法覆盖，但可新建文件」。同一镜像加 `--cap-add SYS_ADMIN` 的容器启动日志是 `mechanism=bind-ro`——升级路径仍然工作。
- `pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入，未打印、未落盘）：run `20261031-141029`，**119 条 `[PASS]` / 0 条 FAIL**，`failure` 为 null；源树干净（运行时 HEAD 即当时提交，工作树无未提交改动），一次性容器 `pf-drill` / volume `pf-drill-data` 已删除，`pf-local` 全程未被触碰（id 前后都是 `382e46da…`），数据卷仍是 `9ce20975…`。演练容器现在与默认部署同形：无 `SYS_ADMIN`、`--cap-drop DAC_OVERRIDE`；**没有任何一条检查依赖 bind-ro**（演练不创建 skill，因而不挂载），所以不需要把任何断言放宽成「只读强制、机制已报告」。
- 证据目录：`e2e/.artifacts/fix-gc/`（截图 `nocap-mechanism-copy-ro.png`、`gc-before-session-delete.png`、`gc-after-session-delete.png`、`gc-after-project-delete.png`；日志与 JSON 同上）。

## 本批：运行时目录生命周期（inventory / 级联 / janitor / 可见性）

### 清单与判定

`backend/internal/runtimedirs` 是唯一的表；`TestEveryInventoryEntryHasALifecycle` 逐行断言 creator / owner / 删除路径 / 兜底，`TestEveryDataDirLiteralIsInTheInventory` 扫描后端源码里的 `filepath.Join(dataDir, "…")`，所以「新加一个目录但没写生命周期」会先红在测试里。判定是 `Owner`（deployment / session / project / sandbox / skill），不是注释。

| 目录 | 创建者 | 删除路径 | 兜底 | 当前状态 |
|---|---|---|---|---|
| `sandboxes/` | `sandbox.Manager.Ensure`（本地 provider，`usr_*/prj_*`） | 删项目：停进程 → 重试 Destroy → 失败则 409 保留项目 | janitor 按 `projects` 行清扫，连同 `_state/<usr>/<prj>.json` | DONE；部署启动扫描 0 残留 |
| `pi-agents/` | agent 启动时 `pimodel.Write` / `WriteMCP` | 删会话 / 删项目（全部子会话） | janitor 按 `sessions` 行清扫 | **本批修复**；部署旧卷一次清掉 13 个（含探针 1 个） |
| `skill-snapshots/` | `provision` / `skillsnap`（FR-14.2） | 删会话 / 删项目 | janitor 按 `sessions` 行，双布局识别 | 已有；本批并入同一套 sweep |
| `snapshots/` | `snapshot.Writer.Take`（空闲回收前清单） | 删项目：按 `sandboxes` 行删 `snapshots/<sbx>` | janitor 按 `sandboxes` 行清扫 | **本批修复**（此前只增不减） |
| `artifacts/` | `saveScreenshot`（FR-11.8） | 删会话 / 删项目 | janitor 按 `sessions` 行清扫 | **本批修复**；旧卷上的探针目录在删会话后仍在，新镜像 10s 内清零 |
| `skills/` | `writeSkillBundle`、种子、导入 | `DELETE /v1/skills/{id}`（共享需管理员） | janitor 按 `skills` 行清扫；`skl_*.tmp` 无行即回收 | 本批补 janitor；旧卷 3 个均有行，未动 |
| `piforge.db`（+ WAL/SHM） | `store.Open` | 不删（部署状态） | 无；WAL 由 SQLite checkpoint 限制 | 部署文件，明确不做清扫 |
| `master.key` / `secret.key` | `crypto.NewFromEnv` / `secretBoxFor` | 不删（删了已存密钥不可读） | 无；属于部署备份 | 部署文件，明确不做清扫 |

### 部署实测（同一数据卷 `9ce20975…`）

旧镜像（`739a9117…`）上先跑 fail-first（`probe-before.json`）：建会话 → 发消息 → `pi-agents/<ses>` 出现（models.json / mcp.json / auth.json / models-store.json）；打开浏览器标签到 `/login` → 截图 201 → `artifacts/<ses>/screenshots/*.png` 出现；`DELETE /v1/sessions/{id}` 返回 200 后 10s 两个目录都还在，`GET /v1/admin/status` 没有 `storage` 字段。

新镜像（`cb8002bf…`，`pwsh scripts/build-app-image.ps1`）用同一卷重启后，janitor 启动即扫：

- `pi-agents` 17 → 5（`count=13 bytes=520546`；12 个历史孤儿 + 探针目录），`artifacts` 1 → 0（`count=1 bytes=25507`）。
- 新建会话后：`pi-agents` 5→6（470497 B）、`artifacts` 0→1（37795 B）；`DELETE` 会话 200 后 **<10s** 两处都回到 6→5、1→0（`probe-after.json`，`session_dirs_gone_within_10s` 两个 true）。
- admin 页面能看到 `storage` 块（`after-admin-status.png`）：`artifacts {entries:1,files:1,bytes:25507}`、`pi-agents {entries:6,files:24,bytes:441825}`、`sandboxes {entries:2,files:419,bytes:3228315}`、`skills {entries:3,files:3,bytes:1698}` 等；`storage_total_bytes` 是所有行之和。
- 手工孤儿（每类一个，3s 清扫周期）：`pi-agents/ses_01MJANITOR…`、`artifacts/ses_01MJANITOR…`、`skill-snapshots/ses_01MJANITOR…`、`snapshots/sbx_01MJANITOR…`、`skills/skl_01MJANITOR….tmp`、`sandboxes/usr_01MJANITOR…/prj_…` 全部在下一轮消失；日志分别为 `count=1 bytes=7`（skills 为 `count=2 bytes=14`，含 `.tmp`）。同时放的 live-owner fixture（活会话的 pi-agents/artifacts、活 sandbox 的 snapshots、活 skill 的 skills）全部保留。

### 边界（仍然无界的部分）

- `piforge.db` 与 SQLite `-wal`：行数由产品语义约束（删会话/项目会级联），但库文件本身不会自动收缩；WAL 由 checkpoint 限制，不是 janitor 的对象。
- `snapshots/` 在一个 sandbox 行存活期间可以按回收次数累积多个时间戳目录；它们有 owner（sandbox 行）与删除路径（删项目/该行消失），但没有按时间/份数的上限。**本批补的是同一类里的另一面**：`snapshots` 的**行**原来没有生命周期（删项目后永远留着、拖慢查询），现在有外键级联 + janitor 行清扫；目录本身的份数上限仍未做。
- `sandboxes/` 的大小现在有每沙箱预算（`-sandbox-disk-quota`，默认 2 GiB，0 关闭），但它是**每次运行前**测量：一个 turn 可以超一次再被下一轮拦住，不是每次写入都拦；本地 provider 仍没有 CPU/内存限制。
- `skills/` 的目录生命周期已闭合；单个 skill 的 bundle 有 4 MiB / 64 文件上限，上传 zip 有 8 MiB / 256 条目上限，一次导入的**实际展开总量**现在有 32 MiB 上限（`-skill-max-bytes` 可调），超过整包拒绝、不留半成品。
- `master.key` / `secret.key` 是故意不回收的部署文件；删它们不是清理，是让已存密钥不可读。

### 门禁（本批）

- `go build ./...` / `go vet ./...`：0；`go test ./... -count=1`：全部包 ok（`go-test.log`）。
- `pnpm --dir web test -- --run`：**31 个文件 / 251 例通过**；`pnpm --dir web exec next build`：通过（`web-test.log`、`web-build.log`）。
- 完整 e2e（同一份 `.data`）：**173 passed**（日志 `e2e-full.log`）。本批只改了 `15-quick-open-status.spec.ts` 的既有断言（storage 块可见），没有新增用例，所以仍是 173。
- 镜像只用 `scripts/build-app-image.ps1`；`pf-local` 始终用卷 `9ce20975…`，`CapDrop=["CAP_DAC_OVERRIDE"]`、`CapAdd=null`、端口 7722→7700、`PIFORGE_DEFAULT_PROVIDER=volc`（`deploy-before.log` / `deploy-final.log`）。
- `pwsh scripts/deliver-check.ps1`（key 来自 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY`，未打印）：119 条 `[PASS]` / 0 FAIL（`deliver-check.log`）。

## 本批：快照行生命周期 + 沙箱磁盘配额（FR-5.8）

两件事都来自「运行时目录生命周期」审计里「仍然无界的部分」：目录有了 owner、删除路径和 janitor，但 `snapshots` 的**行**没有；沙箱的体积也没有上限。目录闭环了，数据没有。

### 1. 快照行（`snapshots`）的生命周期

**缺陷**：`snapshots.sandbox_id` 是裸 `TEXT`，没有外键；删项目、删 sandbox 都不动这些行。每次空闲回收写一行，行只增不减，表越大 `ListSnapshots` 越慢——审计里的原话是「目录闭环了，行没有」。

**改法**：

- `schema.sql` 的 `snapshots.sandbox_id` 现在是 `REFERENCES sandboxes(id) ON DELETE CASCADE`。外键就是行的删除路径：删项目 → 级联 `sandboxes` → 行随 sandbox 一起消失，不需要每个删除路径都记得再删一次。
- **迁移（已存在的部署）**：`store.Open` 里的 `ensureSnapshotOwnership` 先读 `PRAGMA foreign_key_list(snapshots)`；没有这条外键就重建表——一个事务里「新表带外键 → 原样拷贝每一行 → DROP 旧表 → 改名 → 重建索引」，拷贝期间 `PRAGMA foreign_keys=OFF`（必须在事务外设置，SQLite 在事务内忽略它）。这样「sandbox 已经没了」的历史行不会被迁移拒绝，也不会被迁移**悄悄删掉**：那是 janitor 的判断题。
- **索引**：新增 `snapshots_sweep (sandbox_id, id, blob_key)`，是给清扫用的 covering index——janitor 每轮那条 `SnapshotIndex` 只读索引、不读表；原来的 `snapshots_sandbox (sandbox_id, created_at DESC)` 继续服务 `ListSnapshots`。
- **janitor 行清扫**（在同一轮的目录清扫之后）：`SnapshotIndex` 一次拿到所有行、`SandboxIDs` 一次拿到活沙箱，**只删**「manifest 目录不存在」或「sandbox 行不存在」的行。目录还在且 sandbox 还活着的行绝不删；没有 `blob_key` 的行不猜（只在 sandbox 没了时删）；索引读不到就整段中止（「store 没回答」不是「所有行都是垃圾」）。删除走 `DeleteSnapshots`，按 400 个 id 一批。
- **顺带发现并修掉的前提缺陷**：sandbox 被空闲回收后 provider 已经不认这个 id，`destroySandbox` 把它当失败 → 删项目永远 409 → 级联根本走不到，行也就永远留着。现在 `sandbox.ErrNotFound` 视为「已经销毁」，删项目返回 200（`TestDeleteProjectSucceedsWhenTheSandboxWasAlreadyReclaimed`）。

**测试**：`store/snapshot_ownership_test.go` 三例——删项目后行没了；外键 CASCADE 在直接删 sandbox 行时也生效；**老库迁移**（手写一个没有外键、含两行的数据库，一行的 sandbox 还在、一行已经没了：`Open` 后两行都在、外键出现、删项目只删活着的那行、第二次 `Open` 幂等）。`janitor/snapshot_rows_test.go` 5 例——删掉目录没了的行和 owner 没了的行、保留两者都在的行、没有 `blob_key` 不猜、索引失败拒扫、dry-run、没有 sandbox 索引时不扫。`httpapi/sandbox_snapshots_test.go` 的 `TestDeleteProjectRemovesItsSnapshotRowsOverHTTP` 走用户路径。

**fail-first**：新的 store 测试跑在 `a11991b` 上三条全红（行还在 / 没有 CASCADE / 迁移后仍无外键）；janitor 测试在旧代码上编译不过（`undefined: store.SnapshotRef`）。`e2e/.artifacts/fix-rows-quota/fail-first-go.log`。

**部署实测（同一数据卷 `9ce20975…`）**：

- 旧镜像 `sha256:cb8002bf…`（容器 `1d834988…`，为让 reaper 尽快回收另加 `-sandbox-idle-pause=5s -sandbox-reclaim-after=2s -sandbox-reap-interval=2s`）：建项目 + 会话 → 一次真实回合 → reaper 写下 `sbx_a905ab51…/2026…/manifest.json` 并记一行；`GET …/sandbox/snapshots` 列出 1 行、库里 1 行；`DELETE /v1/projects/{id}` → **409**（`sandbox: not found`，provider 已回收），行仍在（`probe-rows-before.json`）。
- 新镜像 `sha256:5bc1692a…`（`scripts/build-app-image.ps1`，同一卷）：同样的流程 → 行 `snp_01M68C67…` / `sbx_6a87f62c…` 在、接口列出 1、库里 1；`DELETE` → **200**，库里 **0**（`probe-rows-after.json`）。
- **janitor**：把其中一行的 manifest 目录手工删掉（`/data/snapshots/sbx_27ec4bd61f23c53d/…`），保留它的 sandbox 行，然后用默认参数重启：启动日志 `janitor: removed snapshot rows with no directory or owning sandbox count=1`，行数 8 → 7，其余 6 行（目录和 sandbox 都还在）没有被碰（`janitor-orphan-row.log`）。
- **迁移**：部署库是修复前建的；重启后 `PRAGMA foreign_key_list(snapshots)` 有 `sandboxes / sandbox_id / CASCADE`，`snapshots_sandbox` 与 `snapshots_sweep` 都在（`deployed-schema.log`）。

### 2. 沙箱磁盘配额（FR-5.8）

**设计（最小可用版）**：

- `-sandbox-disk-quota` / `PIFORGE_SANDBOX_DISK_QUOTA`，单位字节，默认 2 GiB，**0 关闭**。`internal/diskquota.Guardian` 对一个 sandbox root 做 du 式遍历，复用 `runtimedirs.Measure`（管理页和拒绝用的是同一个数）；`Measure` 在每次运行前新鲜测量，`Cached` 给管理页复用 60 秒——页面 15 秒轮询不该变成磁盘扫描。
- **超预算就停这次运行**：`POST /v1/sessions/{id}/messages` 在 provisioning 之后、启动 agent 之前检查；超过则返回 **507 `sandbox_disk_quota`**，中文文案（实测原文）：
  `沙箱磁盘用量已达到 6.0 MiB，超过配额 5.0 MiB（沙箱目录 /data/sandboxes/<usr>/<prj>）：本次运行已停止，没有启动 agent。请删除不需要的文件（node_modules、构建产物、下载的仓库或大文件）后重试，或请管理员调整 -sandbox-disk-quota / PIFORGE_SANDBOX_DISK_QUOTA。`
  `details` 带 `used_bytes` / `limit_bytes` 和可读的 `used` / `limit`。同时往会话事件里追加一条 `platform.error`（payload 带 code、两个字节数和同一条消息），所以转录与通知路径看得到，SSE 开着时也会推过去。
- **管理页看得到**：`GET /v1/admin/status` 新增 `sandbox_usage`（每个沙箱的 bytes / files / quota / over / measured_at）与 `sandbox_disk_quota`；admin 的「运行时目录」卡片下面逐个沙箱显示「用量 / 配额 · 百分比」，≥80% 琥珀色、超了红色并写「已超出」。
- **技能展开上限**：`-skill-max-bytes` / `PIFORGE_SKILL_MAX_BYTES`，默认 32 MiB，0 拒绝启动。zip 导入先看声明的解压总量，再按**实际读写的字节**计数（头里的数字是攻击者输入）；超过就整包拒绝、临时目录删掉、不写任何 skill 行。每个 skill 仍有 4 MiB / 64 文件、上传 zip 8 MiB / 256 条目的旧上限。

**测试**：`diskquota/diskquota_test.go` 7 例（未超 / 超了 / 关闭、缓存 TTL、缺失目录、Forget、nil guardian）；`httpapi/sandbox_quota_test.go` 4 例（超配额 → 507 + 文案 + 事件 + runtime 没收到 prompt；未超 → 200；关闭 → 200；admin status 每个沙箱一行、超了 `over=true`）；`httpapi/skill_import_cap_test.go` 3 例（handler 级拒绝且不留行/目录、函数级拒绝不写文件、默认值可被 Deps 覆盖）。

**fail-first（部署）**：旧镜像没有这个开关——同一个 6.4 MiB checkout 上 `POST /messages` 直接 **200**，run 照常开始（`probe-quota-before.json`）。

**部署实测**（同一卷，`PIFORGE_EXTRA_ARGS=-sandbox-disk-quota=5242880`）：

- `dd` 6 MiB 进 `repo/probe-big.bin`，`du -sb` = 6,408,517 B；
- `POST /messages` → **507**，`code=sandbox_disk_quota`，消息原文见上（`probe-quota-after.json`）；
- 会话事件里出现 `platform.error` 且 payload 含 `sandbox_disk_quota`；会话页把这条消息内联显示（`quota-after-session-error.png`）；
- 管理页：`sandbox_usage` 行 `bytes=6,318,405 quota=5,242,880 over=true`，页面显示 `6.0 MiB / 5.0 MiB · 121% · 已超出`（`quota-after-admin-quota.png`），`sandbox_disk_quota=5242880`；
- **恢复默认**：同一卷用默认参数重启，启动日志 `sandbox disk quota limit="2.0 GiB" bytes=2147483648 skill_import_max="32.0 MiB"`，`GET /v1/admin/status` 的 `sandbox_disk_quota=2147483648`（`cleanup.log`、`deploy-final.log`）；
- **技能 cap（部署）**：43,782 字节的 zip（声明解压 40,000,000 字节）→ **400** `invalid_argument`，消息「技能包展开后超过上限（32.0 MiB）：压缩包声明解压后为 38.1 MiB，没有导入任何文件…」；`/data/skills` 前后都是 3 个目录（`probe-skill-cap.json`）。

### 边界（本批没有做的）

- 配额是**周期性**的（每个 turn 开始前测量 + 管理页 60 秒缓存），不是每次写入都拦：一次 `npm install` 可以冲过一次预算，下一轮才被拦住。要更严需要沙箱内的文件系统配额或卷配额，本地 provider 没有。
- 本地 provider 仍没有 CPU / 内存限制（容器 provider 的 `-sandbox-cpus/-sandbox-memory` 不变）；配额也没有变成容器级 limit。
- 测量依据是 `sandboxes.root_dir`；sandbox 行还在、目录已经没了的情况按 0 字节处理，不会因此拒绝运行。
- 遍历有 `MaxFiles`（20 万）上限，截断时 bytes 是下界；下界超了仍然算超，下界没超但已截断时不保证没超。
- 迁移在拷贝期间关闭外键校验（事务外设 pragma），这是 SQLite 重建表的常规做法；`piforged` 在 `Open` 完成前不开始服务，所以没有并发写。
- 快照行的「份数上限」仍未做：行会随回收次数增长，直到项目/沙箱被删。本批只保证它们不再无主。
- 部署上还留了一个旧镜像探针留下的项目（`probe-quota-before-…`）：旧镜像在回合进行中被重启，`sessions.status` 停在 `running` 而没有对应进程，项目删除仍被「还有 1 个会话正在运行」拒绝。这是重启后的状态核对问题（与本批无关），已在报告里点名，没有在部署上手工改库。

### 门禁（本批）

- `go build ./...`：0；`go vet ./...`：0；`go test ./... -count=1`：全部包 ok，退出 0（新增 `diskquota` 7 例、`store/snapshot_ownership_test.go` 3 例、`janitor/snapshot_rows_test.go` 5 例、`httpapi/sandbox_quota_test.go` 4 例、`skill_import_cap_test.go` 3 例、`project_delete_gone_test.go` 1 例、`TestDeleteProjectRemovesItsSnapshotRowsOverHTTP`）。日志：`e2e/.artifacts/fix-rows-quota/gates-go*.log`。
- `pnpm --dir web test -- --run`：**31 个文件 / 251 例通过**；`pnpm --dir web exec next build`：退出 0（`gates-web-test.log`、`gates-web-build.log`）。
- 完整 e2e（同一份 `.data`）：**173 passed / 8.3m**（`e2e-full.log`）。本批没有新增 e2e 用例（快照行与配额都在 Go 测试和部署探针里验，e2e 仍由 `84-skill-mount`、`61-project-lifecycle` 等覆盖不回归）。
- 镜像只用 `scripts/build-app-image.ps1` 重建：`sha256:5bc1692ac16c8b1feb09dad624cfd34a65fe0180be8c2bf5c1a849f9a526ec8d`；`pf-local` 始终用**同一个数据卷** `9ce20975…`，`CapDrop=["CAP_DAC_OVERRIDE"]`、`CapAdd=null`、端口 7722→7700、`PIFORGE_DEFAULT_PROVIDER=volc`（`deploy-before.log` / `deploy-after.log` / `deploy-quota5mib.log` / `deploy-final.log`）。最终容器 `7c2c1c33…`，配额已恢复默认 2 GiB。
- `pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入，未打印、未落盘）：run `20261031-151551`，**119 条 `[PASS]` / 0 条 FAIL**，`failure` 为 null；镜像 id 与部署的同一个；一次性容器 `pf-drill` / volume `pf-drill-data` 已删除，`pf-local` 全程未被触碰（前后容器 id 都是 `7c2c1c33…`），drill 的「source checkout is clean」守卫通过。日志：`deliver-check.log`；summary：`e2e/.artifacts/delivery-drill/20261031-151551/summary.json`。
- 证据目录：`e2e/.artifacts/fix-rows-quota/`（`probe-rows-before.json`、`probe-rows-after.json`、`janitor-orphan-row.log`、`deployed-schema.log`、`probe-quota-before.json`、`probe-quota-after.json`、`probe-skill-cap.json`、`fail-first-go.log`；截图 `quota-after-session-error.png`、`quota-after-admin-quota.png`、`rows-before-admin-storage.png`、`rows-after-admin-storage.png`）。

## 本批：重启对账（running 只能是「有进程在服务」）

### 1. 缺陷

一个回合在进程死亡时不会留下终态：`hub.applyDerivedState` 只在 `run.started` 写 `sessions.status='running'`、在 `run.settled`/`run.aborted` 写回 idle；`runs.status` 也停在 `started`。控制面重启（或 agent 子进程崩溃）后，没有第二个写者，行就永远停在 running。用户撞上的三件事：

- 会话页一直显示「运行中」，没有回合会结束；
- 删项目被 `RunningSessionCount` 拒绝（「还有 1 个会话正在运行，请先停止这些回合再删除项目」），而用户没有可停的回合；
- 除了手工改库，没有任何修复路径。

上一批的部署上就留着一个这样的项目：`probe-quota-before-1793429858000`（旧镜像在回合进行中被重启）。

### 2. 机制（`backend/internal/reconcile`）

**唯一权威是进程内注册表**：`pi.Runtime.Alive(sessionID)` 问的是「这个 session 有没有活着的 bridge/子进程」，不是 SQLite、不是 hub 的订阅缓存、也不是 normalizer 的 `Status`（后两者都会在子进程死后残留）。`reconcile.Liveness` 只暴露这一个问题，`*pi.Runtime` 满足它。

一轮对账的候选集是 `sessions.status='running'` **或**存在 `runs.status='started'` 的会话（`store.RunningSessions` / `RunningSessionsInProject`，`schema.sql` 加了 `sessions_running` 部分索引）。对每个候选：

1. 注册表说活着 → `kept_live++`，**什么都不写**（debug 日志说明保留原因）；
2. 注册表说没有进程 → `store.InterruptRunningSession` 在**一个事务**里：
   - `UPDATE sessions SET status='interrupted' WHERE status='running'`（只有仍然是 running 才动，避免覆盖并发 settle）；
   - 把仍在 `started` 的 run 逐条落成 `interrupted`，`settled_at`、`interrupted_by='reconcile'`、`completion_note=原因`；
   - 追加 `run.settled`（`{"status":"interrupted","reason":…,"reconciled":true}`，run_id 指向被中断的 run）——这是前端唯一认的完成信号，打开的页面会因此停止「运行中」；
   - 追加一条 `platform.error`（`{"code":"run_interrupted","message":原因,"reason":原因}`），让之后打开会话的人看到原因；
3. 没有可改的行（status 已不是 running 且没有 started run）→ 事务不写事件，这就是幂等。

三个调用点：

- **启动时、端口打开前**（`cmd/piforged/main.go`，带 30s 上限）：`ReasonRestart =「服务重启，本次运行被中断」`。日志 `interrupted an orphaned run` + 一行 summary：`session reconciliation at startup scanned=… interrupted=… runs=… kept_live=…`。
- **看门狗**：`-session-reconcile-interval` / `PIFORGE_SESSION_RECONCILE_INTERVAL`，默认 30s，0 关闭周期（启动那一轮仍跑）。`ReasonProcessGone =「运行进程已退出，本次运行被中断」`；没有修复时只打 debug，有修复才打 summary。
- **删项目守卫**（见下）。

**保守性**：Runtime 为 nil 或未实现 `Alive` → 整轮跳过并 WARN（`the agent registry is unavailable`），一行都不改；store 出错 → 回滚并 WARN；注册表说活着 → 不动。先证明「死」，再写「中断」。

### 3. 删除守卫为什么不能再被谎言钉住

`handleDeleteProject` 现在在 `RunningSessionCount` **之前**调用 `Reconciler.Project(...)`：

- 谎（行 running、进程不在）→ 当场对账成 interrupted，计数变 0，**同一个请求继续走**销毁与删行；
- 真（注册表说活着）→ 保留原 409，行仍是 running。

选择「当场修复并继续」而不是「返回一个让用户重试的特定错误」：修复本身是幂等、保守且已经证明安全的；让人先看一次 409 再点一次只是把内部状态机暴露给用户，而「我能删掉我的项目」是这条修复唯一要兑现的结果。失败的对账（store 错误）仍然让守卫拒绝——不是「修不了就放行」。

### 4. fail-first

- **Go（旧代码）**：`TestDeleteProjectReconcilesAStaleRunningSession` 在 `2ccc85f` 上跑：`delete answered 409, not 200 … (session status after the refusal: running)`，日志 `e2e/.artifacts/fix-reconcile/fail-first-go.log`。修复后同一测试 200，项目消失。`TestDeleteProjectStillRefusesALiveRun` 在旧代码上通过（旧守卫一律拒绝），修复后仍通过（这次是因为注册表说活着）。
- **部署（旧镜像，未改库）**：`e2e/scripts/fix-reconcile-probe.mjs before` 对 `prj_01M68BZT60TFTGSJ64JAT29TZ7`：UI 删除 toast「删除失败 项目「probe-quota-before-1793429858000」还有 1 个会话正在运行…」、`DELETE /v1/projects/{id}` → **409**、库里 session 仍 `running`、run 仍 `started`、没有对账事件（`stuck-before.json`、截图 `stuck-before-delete-refused.png`）。

### 5. 部署实测（同一个卷 `9ce20975…`）

- 新镜像 `scripts/build-app-image.ps1` 构建（`sha256:7e829ca2…`），`pf-local` 在同一卷上重建（新容器 `760749a7…`，端口 7722→7700，`PIFORGE_DEFAULT_PROVIDER=volc`、`CapDrop=["CAP_DAC_OVERRIDE"]`、`CapAdd=null`、配额默认 2 GiB）。
- 启动日志（端口打开前）：`interrupted an orphaned run phase=startup session=ses_01M68BZT649A02VBB8JVPCKFWS project=prj_01M68BZT60TFTGSJ64JAT29TZ7 runs=1 reason=服务重启，本次运行被中断`，随后 `session reconciliation at startup scanned=1 interrupted=1 runs=1 kept_live=0`（`deploy-after.log`、`startup-reconcile.log`）。
- 数据库（只读查询，未手工改库）：session `running → interrupted`；run `run_18e38af5028a4fbb_3` `started → interrupted`，`interrupted_by=reconcile`、`completion_note=服务重启，本次运行被中断`；事件新增 `run.settled`（带 reason）与 `platform.error`（`code=run_interrupted`）。同项目里**早已正常结束**的 `run_18e38af45a3d90dd_2` 仍是 `settled`，没有被改写。
- 之后 UI 删除同一项目：成功回到首页，`GET /v1/projects/{id}` → **404**，库里 project/session/run/event/sandbox 行全部为空（`stuck-after.json`、截图 `stuck-after-session-interrupted.png`、`stuck-after-deleted-home.png`）。
- 说明：`stuck-after.json` 里有一条 `[BROKEN]` 是**探针断言写错了**（它要求项目里每一条 run 都变成 interrupted，包括崩溃前已经正常结束的那条）；产品事实在同一份 JSON 里，修正后的断言由 `node e2e/scripts/fix-reconcile-probe.mjs recheck` 在已删除项目的记录上重放，全部 `[ok]`（`stuck-after-recheck.json`、`probe-after-recheck-console.log`）。删除是不可逆的，没重跑一次 after 来掩盖这条断言错误。

### 6. 测试

- `backend/internal/reconcile/reconcile_test.go`
  - `TestBootReconcileInterruptsAnOrphanedRun`：running 行 + started run、空注册表 → interrupted、run 落盘、两种事件带原因、日志含 `interrupted=1`；第二遍 `Scanned=0` 且事件数不变（幂等）。
  - `TestReconcileLeavesALiveRunAlone`：注册表说活着，跑两遍（等于看门狗 tick 落在活跃回合旁）→ `kept_live=1`，session 仍 running、run 仍 started、0 事件。**这条就是演练期间「对账不得打扰活跃 run」的断言。**
  - `TestWatchdogInterruptsARunWhoseProcessDied`：先活着（不修）→ 子进程消失 → 同一轮看门狗修成 interrupted（无重启的崩溃）。
  - `TestReconcileWithoutARegistryInterruptsNothing`：没有注册表时保守不动并解释。
  - `TestProjectReconcileTouchesOnlyItsOwnProject`：按项目对账不动别的项目。
- `backend/internal/httpapi/project_delete_reconcile_test.go`：`TestDeleteProjectReconcilesAStaleRunningSession`（200）、`TestDeleteProjectStillRefusesALiveRun`（409，行仍 running）。
- `web/lib/session/reduce.test.ts`：interrupted 的 `run.settled` 停流并显示原因；`web/lib/types.ts` + `format.ts` 增加 `interrupted` /「已中断」。
- e2e `61-project-lifecycle.spec.ts` 的「a project with a running turn refuses to be deleted」改成**真正的活回合**（`[[hold-run]]` 经 API 发出并等到 `running`，再点删除），因为手改 status 的旧写法现在正是「谎」，会被对账修掉并放行——它不再能证明守卫。

### 7. 边界（明确没做的）

- **容器/远端 placement 的孤儿**：注册表是进程内的。如果 agent 跑在控制面之外（crashed runner 仍活着、沙箱里的 pi 在控制面重启后继续跑），新的控制面看不到它，会把它当作孤儿并标 interrupted。本部署是 `-sandbox local`（agent 是控制面子进程，重启即死），所以这条路径没有歧义；远端 placement 需要 runner 侧提供存活查询，本批没有做，也不假装做了——**下一批「远端存活探测」补上了这一条**（见该节：探针 + 沙箱心跳 marker，unknown 绝不中断）。
- 对账是「注册表说死」而不是「心跳超时」：没有一个跨进程的租约，所以一个在两次检查之间刚好起跑的回合，理论上仍有极小的窗口（注册表在 `Prompt` 前已经 spawn，因此窗口只在 `Alive` 检查与事务之间）；没有引入 grace 期，因为删除路径要的是「立刻可删」，而 grace 会把一个刚崩的回合继续钉住。
- 不做自动重放：不重新提交用户消息，也不 resume Pi 的回合；只把「被中断」写成事实。
- 停止的 run 的 token 统计仍是 0（进程没来得及上报），`completion_note` 是原因而不是 usage。

### 8. 门禁（本批）

- `go build ./...`：0；`go vet ./...`：0；`go test ./... -count=1`：全部包 ok、退出 0（新增 `internal/reconcile` 6 例、`httpapi/project_delete_reconcile_test.go` 2 例；`go-test.log`）。
- `pnpm --dir web test -- --run`：**31 个文件 / 252 例通过**（新增 1 例）；`pnpm --dir web exec next build`：退出 0（`web-test.log`、`web-build.log`）。
- 完整 e2e（同一份 `.data`）：**173 passed / 8.2m**（`e2e-full.log`）。用例数不变：`61-project-lifecycle.spec.ts` 的删除守卫用例从「手改 status」改成「真正的活回合」（`[[hold-run]]`），因为前者现在会被对账修掉并放行，已不能证明守卫。
- 镜像只用 `scripts/build-app-image.ps1` 重建：`sha256:7e829ca294e842f697aa84b79521735cceccf5a4bc70aa3f112e2cd1b3c2a8b0`；`pf-local` 用**同一个数据卷** `9ce20975…` 重建（`760749a7…`），`CapDrop=["CAP_DAC_OVERRIDE"]`、`CapAdd=null`、`PIFORGE_DEFAULT_PROVIDER=volc`、配额默认 2 GiB（`deploy-before.log` / `deploy-after.log`）。
- `pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入，未打印、未落盘）：run `20261031-154216`，**119 条 `[PASS]` / 0 条 FAIL**，`failure` 为 null（`summary.json` 的 `checks` 长度 119）；镜像 id 与部署的同一个 `sha256:7e829ca2…`；一次性容器 `pf-drill` / volume `pf-drill-data` 已删除，`pf-local` 全程未被触碰（前后容器 id 都是 `760749a7…`，`/healthz` 200）；drill 的「source checkout is clean」守卫通过。日志：`deliver-check.log`；summary：`e2e/.artifacts/delivery-drill/20261031-154216/summary.json`。
- 证据目录：`e2e/.artifacts/fix-reconcile/`（`fail-first-go.log`、`go-test.log`、`go-reconcile-tests.log`、`web-test.log`、`web-build.log`、`build-image.log`、`deploy-before.log`、`deploy-after.log`、`startup-reconcile.log`、`stuck-before.json`、`stuck-after.json`、`stuck-after-recheck.json`、`probe-*-console.log`、`e2e-61.log`、`e2e-full.log`；截图 `stuck-before-confirm.png`、`stuck-before-delete-refused.png`、`stuck-after-session-interrupted.png`、`stuck-after-deleted-home.png`）。

## 本批：远端存活探测（先问沙箱，再写 interrupted）

### 1. 缺陷

上一批「重启对账」第 7 条自己写明了边界：**权威是进程内注册表**。`-sandbox local` 下这条是完整的（agent 是控制面子进程，控制面重启即被杀，空注册表就是真相）；`-sandbox docker` / `-sandbox runner` 下它不是：agent 与工作区一起活在沙箱里，控制面重启只换掉了问问题的人。新进程的注册表按定义是空的，于是**每一个活着的远端 session 都会被写成 `interrupted`**——一次簿记错误杀掉真实 run，并往用户的 transcript 里塞一条「服务重启，本次运行被中断」。

### 2. 机制

**契约**：`contracts.LivenessProbe.Alive(ctx, sessionID) (bool, error)`，一个问题、三种答案：

- `true`：有 agent 在服务这个 session → 保留；
- `false`：没有任何 agent → 允许写 interrupted；
- `error`：权威答不上来 → **unknown**，保留（见 §3）。

**实现（`backend/internal/liveness`）**：

- `Process`：进程内注册表（`pi.Runtime.Alive`），local 的权威。没接注册表时返回 error 而不是 false——空结构体不能当证据。
- `Sandbox`：沙箱工作区里的**心跳 marker** + 租约。每 `DefaultBeat=15s` 由 agent 的 launcher 重写一次，超过 `DefaultTTL=2min`（4 个 beat）没更新即判死。
- `AIO`（`MarkerReader`）：通过沙箱**自己的** `/v1/shell/exec` 读 marker（一条 `[ -d ] / [ -f ] / stat -c %Y` 的命令，输出三种 token 之一或一个 Unix 秒），与文件面板、终端走同一扇门。**不**给 runner 加 exec 端点：`docs/SANDBOX-DEPLOY.md` §1 明确说 runner 只拥有生命周期，为了一个存活查询给它开一个更弱的 shell 是把边界做薄。
- `Composite`：把多个来源合成一个答案。**只要有一个来源证明活着就是 alive**（正面证据足够禁止中断）；没人证明活着且至少一个来源报错 → **error（unknown）**；所有来源都明确 `false` 才 `false`。没有来源 → error。
- `Supervise` / `MaybeLaunch` / `LauncherArgv`：launcher 本体，见下。

**marker 放在哪**：`<sandbox 工作区>/.piforge/live/<session-id>`（容器里是 `/workspace/.piforge/live/<session-id>`，宿主侧是 `<data>/sandboxes/<user>/<project>/.piforge/live/<session-id>`）。放在沙箱根而不是 worktree 里：worktree 是 git checkout，一个每 15 秒变一次的文件会出现在每一次 status/diff/commit 里。沙箱根是 bind mount，launcher（在 agent 旁边）与探针（经沙箱 API）看到的是同一份字节。

**为什么是 marker 而不是 `pgrep -f <session>`**：`pgrep` 的模式会匹配到 grep 自己和任何提到该路径的 shell；被 kill 但还没被回收的进程仍然匹配；沙箱镜像必须暴露进程表；而且它答不出「哪个 agent 最后服务过这个 session」。文件有明确的写者（launcher）、明确的结束（agent 退出即删除）和一个年龄——年龄正是「stale 的 marker」与「活着的 marker」的区别。选择 marker 是**要一个可解释的租约，而不是一次猜测**。

**launcher**：`piforged agent-launcher -root <sandbox 工作区> -session <id> -interval 15s -- <agent argv>`，是同一个二进制的子命令（不多一个要装、要记得装的 artifact；`main()` 在 flag 解析前处理它）。它继承 stdio，所以 RPC 管道原样穿过：bridge 写 launcher 的 stdin、读它的 stdout，Pi 拿到的是同一对 fd。启动即写 marker，每 15s 重写；agent 退出（或被取消）时删除 marker，下一次探针立刻答 dead 而不用等租约过期。Linux 上子进程带 `Pdeathsig=SIGKILL`：launcher 被 kill 时 agent 一起死，不会留下一个「没有 marker 的活 agent」。

**接线**：`cmd/piforged/session_liveness.go` 一处同时决定写者与读者——`-sandbox local` 只接 `Process`（并打一行说明「agent 是子进程，空注册表就是真相」）；`docker`/`runner` 接 `Composite(Process, Sandbox)`，并把每个 agent argv 交给 launcher。`store.SessionSandboxFor(sessionID)` 一条查询回答「这个 session 的沙箱是谁、它的宿主工作区在哪」，探针与 launcher 共用，两个答案不会互相矛盾。

**reconcile 的规则**：`Config.Probe` 优先；`Probe` 为 nil 时退回 `Config.Runtime`（旧行为）并打日志；两者都为 nil → 整轮跳过并 WARN。每个候选最多问 10s（`ProbeTimeout`），超时算 unknown。`unknown` 计数与 `kept_live` 分开，summary 里都有。

### 3. 保守性：unknown 绝不中断

- 探针报错（沙箱不可达、没有 API key、HTTP 5xx、超时、答案无法解析）→ WARN 写明 `session`/`project`/原因，行一个字节都不改；
- 探针与注册表都没配 → 整轮跳过并 WARN；
- 唯一会把「可能活着」判死的路径是 marker 超过 2 分钟没更新。理由：marker 的写者与 agent 同生共死（`Pdeathsig`），写者没了 agent 也没了；4 个 beat 的余量是给调度抖动和负载的。
- 删项目守卫也走同一个探针：沙箱不可达时项目会因 unknown 保留（保守方向是「不删」，不是「放行」）。

### 4. fail-first（真实二进制）

`e2e/scripts/fix-liveness-restart.mjs`：

1. 用**新二进制**（local）造出一个「控制面被杀时正在跑」的状态：建项目/会话、发一条持有回合的消息、确认 `running`、kill 整个进程树；
2. 用 `-sandbox runner` 重启，指向一个 fake runner，它的 fake 沙箱对 marker 查询回答**新鲜的 mtime**（= 一个活过重启的远端 agent）。

| 二进制 | session 状态 | 启动日志 | 沙箱被问 | 恢复事件 |
|---|---|---|---|---|
| `51ff223`（旧） | **interrupted** | `interrupted=1 runs=1` | **0 次** | `run.settled` + `platform.error` |
| 本批（新） | **running** | `kept_live=1 unknown=0` | **1 次** | 无 |

旧二进制那三行就是缺陷本身：它压根没有可问的沙箱，于是把一个活着的远端回合写成了中断（`restart-old.json`、`restart-old-boot.log`）。新二进制同一个场景保留它（`restart-new.json`、`restart-new-boot.log`）。

另有一份反证：新二进制在**没有沙箱 API key** 时（`restart-new-no-key-unknown.json`）探针报错、`unknown=1`，session 仍然 `running`——unknown 没有被当成 dead。

### 5. 测试

- `internal/reconcile/reconcile_test.go` 12 例：原 6 例不变；新增 6 例——活着的远端 run 两次看门狗都不动、探针 error → `unknown=1` 且行不变（WARN 点名 session 与原因）、只有明确 false 才中断、composite 在沙箱答不上来时保守、探针超时算 unknown、**旧接线（只问注册表）会把活着的远端 run 写成 interrupted**（把缺陷冻结成断言）。
- `internal/liveness/` 19 例：marker 租约（新鲜/恰好过期/过期/缺失/未来时间/无 mtime/读失败）、路径不能逃出工作区、launcher 写/刷新/删除 marker、退出码透传、被取消时删除 marker、`MaybeLaunch` 端到端解析、composite 四条规则、`Describe` 的启动日志标签、fake 沙箱（HTTP `/v1/shell/exec`）的 present/absent/error/garbage 四种答案。
- `cmd/piforged/session_liveness_test.go` 4 例：真 store + 真 runner 客户端 + fake runner + fake 沙箱，**重启语义**（空注册表 + 新鲜心跳 → 保留；心跳消失 → interrupted；沙箱坏 → unknown）；local 只接注册表；launcher 只给容器 provider。
- `store/session_sandbox_test.go`：worktree 的沙箱优先、无 worktree 回退到项目的沙箱、未知/空 id 是错误。
- `internal/pi/wrap_test.go` 3 例：`WrapArgv` 是最外层、失败会让 spawn 失败（而不是跑一个没有 marker 的 agent）。

### 6. 部署实测

**(a) local 部署上活着 run 不被看门狗碰**（`e2e/scripts/fix-liveness-probe.mjs live-local`，:7722，`pf-local`）：真实模型回合里用 bash 执行 `sleep 120`，15 次采样、76 秒（≥2 个 30s 看门狗周期）全部 `running`；数据库 session `running`、run `started`、0 条恢复事件；容器日志在窗口内没有 `interrupted an orphaned run`、没有 `interrupted=1`；第 40 秒的截图显示「运行中 · 1 个工具」「停止 Esc」（`live-run-mid-watchdog.png`、`live-live-local.json`）。探针随后中断自己的回合并删除自己建的项目。

**(b) `-sandbox runner` + fake runner 的重启**：见 §4——用**真实二进制**（不是容器）做的新旧对照；同等语义的 Go 版本在 `go test ./cmd/piforged/` 里（真 store + 真 runner 客户端 + fake runner + fake 沙箱），所以这条在 CI 上也会被跑。没有起第二个容器：本机 Docker Desktop 上起 runner 容器还需要一个真的 runner 服务，而脚本里用 HTTP fake 已经把「runner 被问 + 沙箱被问 + 决策」这条链路完整走通，多余的容器只会增加变量。

### 7. 门禁（本批）

（见下方「门禁与证据」小节。）

### 8. 边界（明确没做的 / 仍不确定）

- **agent 仍然由控制面在宿主机 spawn**（`pi.Runtime` + `WrapArgv`）。本批修的是「谁有权回答、怎么回答、答不上来怎么办」，不是「把 agent 搬进沙箱」。真实远端部署还需要把 launcher 放到 agent 所在的那台机器/容器里（它现在是 `piforged` 的子命令，镜像里没有单独的 `piforge-agent` 入口）；如果 launcher 与 agent 分处两台机器而工作区不是共享卷，marker 不可见 → 会被判死（保守方向错在「误杀」）。这是仍然存在的缺口，不假装补上了。
- **容器整体重启**（`docker restart`）会杀掉容器内所有进程（含 launcher/agent），marker 变 stale → 判死是正确的。只有「agent 在沙箱容器里、控制面容器重启」这一拓扑才会产生「活着的远端 session」，也正是本批要修的那一种。
- TTL 2 分钟 = 4 个 beat：调度抖动超过 2 分钟会误判死。没有做「租约续期探测」（在判断前主动让对端 touch 一次），因为那需要给 runner 加写端点。
- 时钟：mtime 与 `now` 同源（同内核或同宿主）；真正跨机器的部署里时钟漂移会侵蚀 2 分钟租约。
- 探针只回答「有没有 agent」，不回答「agent 是否健康」：一个卡死的 agent 仍然算活着（与 local 的注册表语义一致），把「卡死」交给 idle reaper 与用户的中断。

### 门禁与证据

- `go build ./...`：0（`go-build.log`）；`go vet ./...`：0（`go-vet.log`）；`go test ./... -count=1`：全部包 ok、退出 0（`go-test.log`；新增 `internal/liveness` 19 例、`internal/reconcile` +6 例、`cmd/piforged/session_liveness_test.go` 4 例、`store/session_sandbox_test.go`、`pi/wrap_test.go`）。
- `pnpm --dir web test -- --run`：**31 个文件 / 252 例通过**（本批没动前端；`web-test.log`）。
- `pnpm --dir web exec next build`：退出 0（`web-build.log`）。
- 完整 e2e（同一份 `.data`）：**173 passed / 8.3m**（`e2e-full.log`）。用例数不变：本批没有改任何 e2e 用例，只在 `e2e/scripts/` 新增两个探针脚本。
- 镜像只用 `scripts/build-app-image.ps1` 重建：`sha256:6a6b3dcd9118c7306ed9a136477e56f76b293bca4f9c0925e76e6a7fb6f4a994`（`build-image.log`）；`pf-local` 在**同一个数据卷** `9ce20975…` 上重建（容器 `760749a7…` → `a4d6765e…`），`CapDrop=["CAP_DAC_OVERRIDE"]`、`CapAdd=null`、端口 7722→7700、`PIFORGE_DEFAULT_PROVIDER=volc`、配额默认 2 GiB、`shm=64MiB`，`/healthz` 200（`deploy.log`）。启动日志新增一行 `session liveness: the in-process agent registry sandbox=local probe=process-registry`，随后 `session reconciliation at startup scanned=0 … unknown=0`（`startup.log`）。
- 部署实测（local，:7722）：见 §6(a)（`live-live-local.json`、`live-run-mid-watchdog.png`、`probe-live-local-console.log`）。
- `pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入，未打印、未落盘）：run `20261031-162307`，**119 条 `[PASS]` / 0 条 FAIL**，`failure` 为 null（`checks` 长度 119）；镜像 id 与部署的同一个 `sha256:6a6b3dcd…`；一次性容器 `pf-drill` / volume `pf-drill-data` 已删除，`pf-local` 全程未被触碰（前后容器 id 都是 `a4d6765e…`，`/healthz` 200）；drill 的「source checkout is clean」守卫通过。日志：`deliver-check.log`；summary：`e2e/.artifacts/delivery-drill/20261031-162307/summary.json`。
- 证据目录：`e2e/.artifacts/fix-liveness/`（`go-build.log`、`go-vet.log`、`go-test.log`、`web-test.log`、`web-build.log`、`e2e-full.log`、`build-image.log`、`deploy.log`、`startup.log`、`live-live-local.json`、`live-run-mid-watchdog.png`、`probe-live-local-console.log`、`restart-old.json`、`restart-new.json`、`restart-new-no-key-unknown.json`、`restart-old-boot.log`、`restart-new-boot.log`、`restart-prepare-boot.log`、`probe-restart-console.log`、`restart-findings.json`、`deliver-check.log`、`delivery-drill/<run>/summary.json`）。

## 本批：中断恢复（原因 + 存活状态 + 继续）

### 1. 三种结局共用一条恢复路径

一次运行可以三种方式结束而没有结果：用户点「停止」、控制面重启（启动对账）、agent 子进程退出（看门狗）。旧实现里只有停止有 `run.settled{aborted}`，重启/退出有一条 `platform.error`，转录里要么只有「已中断」，要么把中断画成红色的「运行时报告了错误」；用户回来后得自己判断工作区是什么状态、下一句该说什么。

现在三条路都写同一个 `run.interrupted` 事件，payload 带 `cause`（`stop` / `restart` / `process_exit`）与原因句：

- 停止由 `handleInterrupt` 写。写之前先确认「session 是 running **且** 有一个 status=started 的 run」，所以一个落在运行结束之后的点击不会在成功的回合下面留下假的中断标记。
- 重启与进程退出由 `store.InterruptRunningSession` 在**同一个事务**里随 `run.settled` 写，`platform.error` 与 `run.settled` 的 payload 也补上 `cause`；旧记录没有 `cause` 时，`recovery.CauseFromReason` 只匹配服务器自己的那三句原话，匹配不上就是 unknown，不硬塞进三个桶。

`docs/STATUS.md` 不把这条记成 FR：PRD 没有「中断后怎么恢复」的需求行；它是三种真实结局共用的一条交付能力。

### 2. 标记里的每个事实从哪来

- **原因**：事件日志。`GET /v1/sessions/{id}/recovery` 取最新的 `run.interrupted`（兼容旧的 `platform.error{code=run_interrupted}` 与 `run.settled{status=interrupted}`），返回 `cause` / `cause_label` / `reason`。
- **存活状态**：`survival` 由服务端在请求时实测——`Workspace.Status` 的变更条数、`Workspace.Log` 的 HEAD（`内部 -> 7 位短哈希`），不是客户端拼的字符串。沙箱行是 `MISSING`/`TERMINATED`、worktree 行不存在时返回「沙箱已不存在，工作区改动与最后一次提交无法确认」；git 读不到时写「工作区改动无法确认 / 最后一次提交无法确认」。任何情况下都不把「读不到」写成「干净」或编一个提交。
- **来源**：标记上另有一行「来源：停止 / 服务重启 / 进程退出」，直接用 `cause_label`，与原因句分开；重启和进程退出的原因句沿用旧文案，停止的新文案是「用户停止，本次运行被中断」。
- **上次请求**：中断事件之前最后一条 `message.final{role=user}`，事件被清理时回退到 `messages` 表。没有文本（例如只有图片）就返回空，页面因此不提供「继续」——不能假装知道用户在做什么。

### 3. 「继续」是服务端拼的一条消息

`POST /v1/sessions/{id}/messages` 新增 `continue_interrupted: true`。客户端只表达意图，正文由 `internal/recovery.ComposeContinue` 拼出：

```
继续上次被中断的工作。

上次的请求：
<中断前最后一条用户消息；超过 4000 rune 时明确写「只保留开头」>

中断原因：<原因句>（来源：<停止/服务重启/进程退出>）
当前工作区：<工作区有 N 个未提交改动 · 最后一次提交 xxxxxxx>

继续之前，请先检查当前状态：运行 git status 和 git log，确认工作区改动与最后一次提交；
不要重复已经完成的工作，也不要丢弃未提交的改动。
```

它和行内评论一样在 `POST /messages` 的同一路径里发送，所以转录里显示的就是 agent 收到的字符串。**不自动发送**：只有点「继续」才会发；`run.started` 之后同一个标记不再显示按钮（`activeInterruption` 被清空），再点也只会得到 409「这次中断已经继续过了」。沙箱已不存在时页面上只有「沙箱已不存在，无法继续；请新建一个会话」，没有按钮；就算陈旧客户端直接 POST，也返回 409 且不触碰 agent。

### 4. 不把「运行中」留在死掉的回合里

重启/进程退出不会再有 `tool.ended`。标记出现时，reducer 把仍未结束的工具块收成 `done + aborted`（与用户停止同一规则），所以重启后工具行是「1 个已中断」而不是永远「1 个运行中」。这也是部署截图里能看到的差异之一。

### 5. 测试与 fail-first

- `internal/recovery/recovery_test.go`：三种原因的 `ComposeContinue` 都含上次请求、原因、来源与核对指令；空请求/空原因被拒绝；存活摘要覆盖 dirty+HEAD、干净、还没有提交、HEAD 读不到、变更读不到、沙箱不存在、工作区不存在；`CauseFromReason` 对三句原话与未知句子。
- `httpapi/recovery_test.go` 5 例（真实路由 + 真 store）：GET 返回原因/上次请求/dirty 2/HEAD；`continue_interrupted` 真的把拼好的消息送进 runtime；沙箱 MISSING 时 `can_continue=false`、摘要写「无法确认」、POST 409 且 runtime 收到 0 条；停止在有 started run 时写 `cause=stop`，没有 started run 时不写；已继续过（中断后有 `run.started`）不再提供。
- `reconcile/reconcile_test.go`：原有 12 例保留；启动对账现在断言 `run.interrupted` 与 `cause=restart`（以及 `run.settled`/`platform.error` 的 `cause`），「只有 session 行没有 run 行」那条断言两个事件都在。
- `web/lib/session/reduce.test.ts`：`run.interrupted` 进时间线且不画错误横幅、旧的 `platform.error` 仍能解释、`run.started` 让标记变历史、被中断的工具收成终态。
- e2e `85-recovery.spec.ts`：点「停止」→ 标记含「停止」与「工作区有 1 个未提交改动 / 最后一次提交」→ 点「继续」→ `turn-user` 含上次请求、原因与「请先检查当前状态」→ 按钮消失。
- **fail-first**：
  - Go：先写 `recovery/recovery_test.go`、用空实现跑，`ComposeContinue: not implemented`、存活摘要 `summary "" is missing …`（`e2e/.artifacts/fix-recovery/fail-first-go.log`）。
  - e2e：把前端 stash 回旧实现、只保留新后端与新的 spec，旧页面在 `getByTestId('recovery-cause')` 处失败（element not found），证明这条 spec 断的是新行为（`e2e/.artifacts/fix-recovery/fail-first-e2e.log`）。

### 6. 部署实测

镜像用 `scripts/build-app-image.ps1` 重建（`sha256:32d0cc88…`），`pf-local` 在同一个卷 `9ce20975…` 上重建，`CapDrop=["CAP_DAC_OVERRIDE"]`、`PIFORGE_DEFAULT_PROVIDER=volc`、端口 7722→7700、默认 2 GiB 配额；`/healthz` 200，启动日志 `session reconciliation at startup scanned=0 …`。

`e2e/scripts/fix-recovery-probe.mjs` 对着 :7722 跑真实模型：

- **停止**：真实 `sleep 240` 运行中点停止 → 标记 `已中断 / 用户停止，本次运行被中断 / 来源：停止 / 工作区有 1 个未提交改动 · 最后一次提交 ae0b1b3` + 「继续」（`01-stop-marker.png`）；点继续后转录里的消息就是上面的模板（`02-stop-continued-turn.png`），再读 `/recovery` 得到 `can_continue=false`。
- **重启**：另一个运行中点停止之前 `docker restart pf-local`，启动日志 `interrupted an orphaned run … reason=服务重启，本次运行被中断`、`scanned=1 interrupted=1 runs=1`；页面重载后同一位置的标记变成 `来源：服务重启 / 服务重启，本次运行被中断`，存活状态仍是实测（`03-restart-marker.png`），与停止的文案不同。
- 探针结束删除两个 probe 项目，复查 `running=0`、`probe_sessions=0`、无 `probe-recovery-*` 项目；证据 `probe.json`、`probe-console.log`。

### 7. 门禁（本批）

- `go build ./...` 退出 0（`go-build.log`）；`go vet ./...` 退出 0（`go-vet.log`）；`go test ./... -count=1` 全部 ok、退出 0（`go-test.log`）。
- `pnpm --dir web test -- --run`：**31 个文件 / 256 例通过**（`web-test.log`）。
- `pnpm --dir web exec next build`：退出 0（`web-build.log`）。
- 完整 e2e：**174 passed**（原 173 + 新 `85-recovery.spec.ts`，同一份 `.data`；`e2e-full.log`）。
- `pwsh scripts/deliver-check.ps1`：`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入（只在本进程环境里，未打印、未落盘），run `20261031-170720`，**119 条 `[PASS]` / 0 FAIL**（`summary.json` 的 `checks` 长度 119、`failure` 为 null）；drill 镜像 id 与部署的同一个 `sha256:32d0cc88…`；`pf-local` 全程未被触碰（前后容器 id 都是 `88707cf4…`，`/healthz` 200），drill 容器/卷已删除，source checkout 干净；`deliver-check.log`。
- 证据目录：`e2e/.artifacts/fix-recovery/`（`fail-first-go.log`、`go-unit-pass.log`、`fail-first-e2e.log`、`e2e-pass.log`、`e2e-full.log`、`web-test.log`、`web-build.log`、`go-build.log`、`go-vet.log`、`go-test.log`、`build-image.log`、`deploy.log`、`startup.log`、`probe.json`、`probe-console.log`、`01-stop-marker.png`、`02-stop-continued-turn.png`、`03-restart-marker.png`、`deliver-check.log`）。

### 8. 边界（明确没做的）

- **没有自动恢复**。重启/进程退出后不会自动发一条继续消息：用户可能只是想看看到底发生了什么，自动发送会在用户没同意时消耗一次模型调用并改动工作区。恢复是一个需要点击的动作。
- 存活状态读的是**点击/加载时的当前工作区**，不是中断瞬间的快照。中断到点击之间用户或终端可能改过文件；标记不说「这是本次运行留下的」，只说「工作区现在有 N 个未提交改动」。要归因需要运行开始时的基线，本批没做。
- 只显示未提交文件数与 HEAD，不显示「测试跑过没有」。测试行（`test_runs`）是另一个数据源，本批没有把它拼进摘要；PRD 也没有这条要求。
- 旧会话如果连最后一条用户消息都没有（图片-only 或事件被清理且 messages 也缺失），「继续」被禁用并写明原因——不能凭空编一个「上次请求」。
- `run.interrupted` 的停止路径依赖「有 started run」这个持久事实；一个极端的顺序（run 行创建前用户就点停止）不会写标记，旧行为仍然保留（只显示 `run.settled{aborted}` 的「已中断」）。

## 本批：最终验收的三处真相修复

三处问题的共同点不是缺功能，而是**界面说了后端不知道的事**：测试面板承诺有测试结果却永远是空的、会话头声称有 run 在跑而 `/status` 说没有、「变更」面板在终端写文件后仍说干净。所以改的是数据来源，不是把文案说得更保守。

### 1. 测试面板：agent 的真实调用没有被识别（MEDIUM）

- **部署实测原因（命令输出，不是推测）**：在 `pf-local`（node v22.23.4）上让 agent 先跑 `node --test`，`GET /v1/sessions/{id}/test-runs` 立刻出现一条 `runner=node --test, passed 4/4`；再让它按验收里的方式跑 `node primes.test.js`，同一接口仍是 `count=1`。工具事件链本身是通的（`ToolCommand` 拿到了 `bash` 的 `command`，`finishTestRun` 也解析了 TAP），断点在 `testrun.Detect`：它只认 `--test` 标志，而 agent 加了 `--test` 以外的写法。
- **修的是检测**：`nodeTestScript` 按 node 自己发现测试文件的命名约定识别 `node x.test.js`（`*.test.*`、`*.spec.*`、`*-test.*`、`*_test.*`、`test-*.*`、`test.*`，只接受脚本扩展名），`tsx` 同理；`node primes.js`、`node scripts/check.js`、`node --version` 仍然不算。node 22 在沙箱里直接执行测试文件时打印的就是 TAP（不是 spec），所以解析器不需要新写。
- **文案按真实能力改写**：空态从「这里会显示 agent 或终端里运行的测试命令」改成点名**能识别的命令**（含 `node --test` 与 `node x.test.js`），并明说其他脚本不会被识别、也不会编造通过/失败。
- **测试**：`TestDetect` 新增 9 条（真实命令、`cd … &&`、带 `--require/--env-file` 前缀、`tsx`、以及 `node primes.js` / `node --version` / `node notes.md` 反例）；`TestParseTAP` 新增「agent 直接跑测试文件」用例，夹具是沙箱里打印的真实 TAP 字节；fake peer 新增 `[[node-test-file]]`（命令与输出就是那个形状）；`86-node-test-file.spec.ts` 断言面板出现 `data-runner="node --test"`、命令 `node primes.test.js`、`✅ 2`，并与 API 同一行。旧检测跑该 spec 失败在「`test-tab-badge` element not found」（`e2e/.artifacts/fix-truth/fix1-failfirst.log`）。
- **部署复验**：新镜像上再让 agent 跑一次 `node primes.test.js`，`test-runs` 从 `count=1` 变成 `count=2`，新行为 `runner=node --test / passed / total=4 / passed=4`（`e2e/.artifacts/fix-truth/deployed-test-runs.json`）。

### 2. 会话状态：running 来自状态 API，不来自被复制的转录（MEDIUM）

- **根因**：分支的转录是父会话事件日志的**副本**（`seedForkedHistory`，在当前构建里因为事件没有 entry id 而总是走「复制全部」的分支，且被 `ListEvents` 的 1000 条上限截断）。如果在父会话正在跑的时候 fork，副本就停在一个 run 中间；前端回放这段历史时 `run.started` 把 `running` 置真，而分支自己的 `/status` 是 `running:false`。验收看到的「运行中 · 3 个工具」正是这一段回放。
- **修法**：`running` 以状态 API 为准。hook 区分「开场的回放」与「真正的实时事件」（回放可以**结束**一个 run，不能**开始**一个 run），workbench 用 `GET /v1/sessions/{id}/status` 的答案 reconcile 到 reducer，并在每个 run 级事件后重读一次；状态说空闲时，转录里没有 `tool.ended` 的工具块被标成 `结束未记录`（既不是「运行中」也不是编出来的「完成」）。header、composer、context meter、行内评论的 steer 判断读的都是这同一个 state。
- **「变更」面板**：终端命令不产生会话事件，但终端已经有一条「打印了东西」的信号（文件树一直用它刷新）。`GitProvider` 现在订阅 workbench 那个去抖后的 fs revision（终端输出、编辑器保存），没有加轮询。
- **测试**：reducer 新增 5 例（回放不算运行、实时事件算、状态空闲清 running 并把悬空工具标成 `结束未记录`、状态说在跑时补上 run、状态先到时的回放工具）；hook 新增「开场回放不算运行」；e2e `87-fork-status-truth.spec.ts` 在父会话 `[[hold-run]]` 途中 fork，断言 API `running:false`、页面上 `run-badge`/`composer-stop` 都是 0 且 3 秒后仍为 0；`88-terminal-changes.spec.ts` 在真实 PTY 里 `echo CHANGE >> count.js`，断言「变更」在**不点刷新、不重载**的情况下出现该行。旧构建跑这两条分别失败在 `composer-stop` 计数 1 与「找不到 `git-change[data-path="count.js"]`」（`e2e/.artifacts/fix-truth/fix2-failfirst.log`）。
- **部署复验**（新镜像，:7722）：打开验收留下的那个被截断的 fork，API `running:false/streaming:false`，页面 `run-badge=0`、`composer-stop=0`、`run-state=settled`，3 秒后仍为 0，工具组显示「1 个工具调用…7ms」而不是「运行中」；终端里 `echo '// terminal probe' >> primes.js` 后，「变更」从「干净 / 0 行」变成「1 个改动 / M primes.js」，用时 1.09 秒，导航计数仍是 1（没有重载）。`90-fork-header.png`、`95-changes-before-terminal.png`、`96-changes-after-terminal.png`、`deployed-ui.json`。

### 3. 三个小承诺（LOW–MEDIUM）

- **管理页的数字**：`运行中的 agent` 数的是活进程（`hub.liveCount`），打开一个空闲 fork 就 1→2。现在 `handleAdminStatus` 同时返回 `runs_running`（`hub.runsInFlight`：逐会话问运行时的 Running/Streaming）与 `agents_live`（活进程，`agents_running` 作为同值别名保留），页面用 `lib/admin/status-metrics.ts` 的 `agentMetrics` 渲染「进行中的运行」与「agent 进程」两张卡，hint 写明「空闲的 agent 进程不算」。部署实测：`runs_running=0` 而 `agents_running=2`，两张卡分别显示 0 与 2，旧 `stat-agents` 已不存在（`94-admin-status.png`、`deployed-admin-status.json`）。
- **行内评论入口**：原来是 `invisible … opacity-0`、只有 hover 才出现。现在是常驻 `opacity-60`（hover/focus 全亮）、18×18；部署实测 `getComputedStyle` 的 `opacity=0.6`、`visibility=visible`、`18×18`（`92-comment-affordance.png`、`deployed-ui.json`），e2e 77 在 hover 之前就断言这一点，旧样式失败在 `visibility: hidden`。
- **新会话对话框**：`GET /v1/providers` 新增 `default_model_ref`（就是「默认模型」在第一条消息时会解析到的引用，服务端算），对话框显示「默认模型会使用 volc / deepseek-v4-flash；选它就按这个模型运行。」，缺 key 的提示改成「shared-anthropic 缺少 API Key，不能选择；这不影响默认模型。」；列表未加载完时不显示任何结论。部署实测两句话正是如此（`93-new-session-dialog.png`、`deployed-ui.json`）。测试：`status-metrics.test.ts`、`model-options.test.ts` 的 copy 用例、`new-session-dialog.test.tsx`（旧对话框失败在找不到 `model-default-hint`）、e2e 15（旧页面没有 `stat-runs`）。

### 4. 提交与门禁

- 提交：`1a41c02`（测试面板检测）、`f52bc6e`（状态来源 + 变更面板）、`fd4d00a`（管理页指标 / 评论入口 / 默认模型文案），本文件单独一笔。
- `go build ./...` 与 `go vet ./...` 退出 0；`go test ./... -count=1` 35 个包全 ok、无 FAIL。
- `pnpm --dir web test -- --run`：**268 passed（33 个文件）**；`pnpm --dir web exec next build` 退出 0。
- 完整 e2e：**177 passed**（原 174 + 新 `86-node-test-file` / `87-fork-status-truth` / `88-terminal-changes`，同一份 `.data`，7.7 分钟）。
- 镜像只用 `pwsh scripts/build-app-image.ps1` 重建（`sha256:40f6efa8…` 的 manifest，tag `piforge:local`）；`pf-local` 用**同一个数据卷** `9ce20975…` 重建（旧容器 `88707cf4…` → 新容器 `2507e794…`，端口 7722→7700，`PIFORGE_DEFAULT_PROVIDER=volc`、`volc` 仍可用、`CapDrop=["CAP_DAC_OVERRIDE"]`、`CapAdd=null`、沙箱配额默认 2 GiB），`/healthz` 200。
- 部署收尾：探针项目已删除（API 200），`GET /v1/admin/status` 为 `runs_running=0`、`agents_running=0`、`sandboxes_up=0`、`projects=6`（都是此前验收留下的，不是本批）、`sessions=7`；逐个会话 `GET …/status` 全部 `running:false`。
- 证据目录：`e2e/.artifacts/fix-truth/`（`fix1-failfirst.log`、`fix1-pass.log`、`fix2-failfirst.log`、`fix2-pass*.log`、`fix3-failfirst.log`、`fix3-pass.log`、`full-e2e.log`、`gate-go-test.log`、`gate-web-test.log`、`build-image.log`、`deliver-check.log`、`deployed-*.json`、`90…96-*.png`）。

## 本批：fork 转录的截断（lineage）+ Node 24 spec reporter

### 1. 选择：lineage（(a)），不是无上限复制（(b)）

旧代码 `seedForkedHistory` 用 `ListEvents(ctx, userID, parentID, 0, 1000)` 复制父会话的整段事件日志。`ListEvents` 是 SSE 回放查询，一页最多 1000 条（`store/events.go` 把 `limit` 夹到 1000），返回值没有「还有下一页 / 还有多少」的信号；更早设计里按 `entry_id → parent_id` 走父链选窗口的代码在这个构建里也是死的——`pi/normalize.go` 明确不在事件上写 `EntryID`（Pi 的事件流不报 entry id），所以 `parentOf` 永远为空，函数落到「复制它读到的全部」的兜底。父会话超过 1000 条事件时，fork 的转录从第 1001 条起静默丢失：没有缺口标记，也没有提示。

选 (a) 而不是 (b)：

- (b) 仍要写一份完整副本（一次 fork 复制 5 万条事件），还要新增 `history_complete` 标志与「历史不完整，已复制前 N 条」的 UI；副本会与父会话分叉，父会话以后被清理时两边不一致。
- (a) 只写一个数。父日志是唯一事实来源，fork 是 O(1)，结构上不可能被任何页大小截断。
- (a) 唯一的代价是读要合并；用「转录 seq」把每段映射进同一个单调光标即可，调用方的 `seq > after, asc, limit` 契约不变。

数据与读法：

- `contracts.Session.ForkedAtSeq` + `sessions.forked_at_seq INTEGER NOT NULL DEFAULT 0`；老库由 `store.migrate` 的 `addColumn` 加列。旧 fork 的 0 表示「不是 lineage fork」，它们保留当年复制的事件，不会被追溯合并。
- `handleFork` 在 `rt.Fork` **之前**用 `MergedSeqRange` 量出父会话合并转录的最后 seq；`branchHasHistory` 为真时把 cut 写进分支行。为假（在第一条用户消息处 fork，Pi 给空会话）不写：模型没看过的对话不显示。
- `Store.sessionLineage` 从根到叶构造块：`transcript seq = base + own seq`，块首尾相接；父的 cut 之后的块被丢弃。`ListMergedEvents` 按块分页（沿用 after/limit），`MergedSeqRange` 供 SSE 的 409 缺口检测。嵌套 fork（fork 的 fork）截断在父的**合并** seq 上，而不是父自己的行号上；`TestMergedTranscriptHonoursNestedCuts` 覆盖。
- 父会话删除：`DELETE /v1/sessions/{id}` 在存在 lineage 分叉时返回 409（「这个会话有 N 个分叉，它们的历史直接继承自这里；先删除分叉，再删除它」）。store 层仍防御性降级：父行真的不见了也能读，只是没有继承段，不报错也不伪造父事件。项目级删除按外键级联，整棵树一起走。
- 父会话之后写入的事件 seq 都在 cut 之后，任何块都读不到，所以「later events never leak in」。
- 合并读是只读；`hub.applyDerivedState` 只处理实时 pump 事件，父的 `run.started` 不可能把分支状态改成 running。前端 reducer 的「回放只能结束 run、不能开始 run」规则与 `87-fork-status-truth.spec.ts` 继续作为证据。
- 读路径全部改到 `ListMergedEvents`：`sse.go`（UI 唯一转录来源）、`session_export.go`（MD/HTML 导出）、`pr.go` 的 `firstUserText`（分支开 PR 的标题）、`session_handlers.go` 的 `forkPointsFromEvents`/`entryTimestamps`（无 agent 兜底）、`store.LastContextUsage`（fork 建好就显示继承的上下文，而不是 0%）。另外 `sidecar_promote.go` 的 `copyEvents`（clone 与 promote 共用）也改成无上限分页——那是同一个页大小的另一个受害者；它们没有 lineage，所以仍然复制，只是不再截断。

### 2. Node 24 spec reporter

Node 24 直接执行测试文件（`node x.test.js`，没有 `--test`）时默认打印 spec reporter，而不是 TAP：摘要是 `ℹ pass N` / `ℹ fail N` / `ℹ tests N`，套件是 `▶`，通过/失败是 `✔`/`✖`，失败详情在 `✖ failing tests:` 段（`test at file:line:col` + `✖ name` + 缩进消息 + 栈）。旧 `testrun` 只认 TAP，所以这条真实路径记成「未识别格式」。

- `backend/internal/testrun/parse_spec.go` 读摘要计数与 `duration_ms`，失败只从失败段读（不数 `✖` 行：套件行也是 `✖`，会把套件当测试），跳过栈帧；`Parse` 对 `node`/`tsx` 先 TAP 后 spec，对 `npm`/`jest`/`vitest` 在 JS 摘要后再 TAP/spec，`make` 与未知 family 的 `parseAny` 也包含它；`Complete` 认 `ℹ pass|fail|tests`，终端命令不会因为 spec 输出一直停在 running。
- 真正未知的格式仍返回 `Recognized=false`，计数清零、保留原文尾部（`TestParseUnrecognisedKeepsNoCounts` 不变）。
- 测试：`parse_spec_test.go` 5 例（node v24.11.1 真实字节夹具的 3/2/1 与失败位置 `suite.test.js:5`、通过运行、npm/make 包装、skipped+todo、半个摘要不算）；`TestParseRealNodeTestRun` 追加真实 runner 的「直接执行 pass/fail 两个文件」断言；`TestComplete` 增 3 条。

### 3. 测试与 fail-first

- **fail-first（旧代码，e2e）**：`e2e/tests/89-fork-long-transcript.spec.ts` 先建 32 轮（父会话 >1200 事件，测试自己断言过），fork 后的页面 `turn-user` = **24**，期望 **32**（`Expected: 32 / Received: 24`，重试同样失败）。日志 `e2e/.artifacts/fix-fork-copy/fail-first-before-fix.log`。这就是「fork 丢掉尾巴」的直接证据，不是推测。
- 修后同一 spec：32 轮全部在页面上，第一轮与最后一轮文本都在，最后一条助手消息也在；日志 `after-fix.log`、`full-e2e.log`（`[fork-long] parent seq=… prompts=32`）。
- Go：`store/lineage_test.go` 5 例（1500 条完整、cut 之后不泄漏、嵌套 cut、父删除降级、继承的 context.usage）；`httpapi/fork_history_test.go` 4 例（会话转录、1500 条 SSE 读到 `"seq":1500`、cut 冻结、父删除 409 且分支先删后父可删）；`httpapi/session_clone_test.go` 新增「1200 条 clone 不再截断」。
- `web/lib/session/reduce.test.ts` 的 5 例「running comes from the status API, not from replayed history」保持通过；`web/lib/types.ts` 补上 `forked_at_seq` 字段与转录 seq 的说明。
- e2e `87-fork-status-truth.spec.ts` 与 `89-fork-long-transcript.spec.ts` 都在最终代码上通过。

### 4. 部署实测（最终镜像）

- 镜像只用 `pwsh scripts/build-app-image.ps1` 重建：`sha256:9a5b41cde16e12f9943e0dcfa504fc0a5149387d487a7d5a6e8d13b8cc765e63`（tag `piforge:local`）。
- `pf-local` 用**同一个数据卷** `9ce20975339ab49b4b933f5a4b77acbb406eb4b426ba7ab79c3977ab15111f36` 重建：旧容器 `2507e794…` → `60c4c79f…`（第一版修复）→ 最终 `98abb92021acede76efbcb18a06cc34861e84fd4c48e53289fc7d29c7568d9d3`；端口 7722→7700；`PIFORGE_DEFAULT_PROVIDER=volc`；`CapDrop=["CAP_DAC_OVERRIDE"]`、`CapAdd=null`；启动日志 `sandbox disk quota limit="2.0 GiB"`；`/healthz` 200。
- `e2e/scripts/fix-fork-copy-probe.mjs` 对着 :7722 跑真实模型（volc/deepseek-v4-flash）：父会话 5 轮真实回复后 `last_seq=1162`（> 1000）；fork 响应 `forked_at_seq=1162`；分支页面 `turn-user=5/5`，第一轮是「第 1 轮」、最后一轮是「第 5 轮」，最后一条助手消息含 `END-5`；`GET …/export?format=md` 有 5 条「## 我」且含 `END-5`。**17/17 检查 PASS**（`deployed-probe.log`、`deployed-probe-log.json`、`deployed-fork-export.md`）。
- 截图（部署实例）：`deployed-fork-top.png`（第一轮 + `END-1`）与 `deployed-fork-bottom.png`（第 5 轮 + `END-5`）；探针会先把转录滚到顶部/底部并**验证 `scrollTop` 真的到位**再截图（自动锚定会把一次性的 scrollTo 拉回去）。
- 探针自删 fork / parent / project（200/200/200）；复查 `probe_projects=0`、`probe_sessions=0`、`running_sessions=0`（`projects=6`/`sessions=7` 都是此前验收留下的）。

### 5. 门禁（本批）

- `go build ./...` 退出 0（`go-build.log`）；`go vet ./...` 退出 0（`go-vet.log`）；`go test ./... -count=1` **35 个包全 ok、0 FAIL**、退出 0（`go-test.log`）。
- `pnpm --dir web test -- --run`：**268 passed（33 个文件）**（`web-test.log`）；`pnpm --dir web exec next build` 退出 0（`web-build.log`）。
- 完整 e2e：**178 passed**（原 177 + 新 `89-fork-long-transcript.spec.ts`，同一份 `.data`，8.3 分钟；最终代码重跑 `full-e2e.log`，其中 `[fork-long] parent seq=1368 prompts=32`）。
- `pwsh scripts/deliver-check.ps1`：`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入（只在本进程环境里，未打印、未落盘），run `20261031-192241`，**119 条 `[PASS]` / 0 FAIL / 0 SKIP，`failure` 为 null**；drill 断言的镜像 id 就是部署的 `sha256:9a5b41cd…`；一次性容器 `pf-drill` 与 volume `pf-drill-data` 已删除，`pf-local` 全程未被触碰（前后容器 id 都是 `98abb920…`，`/healthz` 200）；`deliver-check.log`。
- 提交：`9584ee9`（代码 + 测试 + e2e probe/spec + `web/lib/types.ts`），本文件单独一笔。只 `git add`/`git commit -- <paths>` 自己的 22 个路径，`git show --name-only` 已核对。

### 6. 边界（明确没做的）

- **旧 fork 不追溯**：本批之前生成的 fork 行 `forked_at_seq=0`，它们保留当年复制的事件（可能已被截断）。要完整历史，需要在父会话上重新 fork 一次；本批不重写旧分支的转录。
- **在更早的用户消息处 fork**：本批仍继承「fork 当时的整段父转录」，而不是只到所选消息。旧 copy 也一样（entry id 为空时复制全部），本批没有收窄，避免引入新的静默截断；代价是界面可能显示 Pi 的 agent 会话文件在所选消息之后的对话。要精确，需要把 entry id 写进事件或把 fork point 映射到 seq，本批不做。
- **clone / promote 仍复制**（它们要独立于源会话继续存在），只是不再被 1000 条页大小截断；它们不是 lineage，删源会话不影响它们。
- **父会话删除被拒绝**（有 lineage 分叉时 409）是刻意选择：宁可让用户先删分叉，也不静默丢掉分叉的历史。项目级删除仍级联整棵树。
- **Node <24 的 TAP 路径不变**；spec parser 只覆盖 `node:test` 的 spec 形状，自定义 reporter 仍按未知格式诚实显示原文。
- **合并后的 `seq` 是转录级**，不是分支行自己的行号；`status.last_seq` 仍是该进程自己的序号（当前前端不使用它做游标）。

## 本批：终端输入的三个缺陷（FR-10.1 / FR-10.3 / 分叉对话框）

验收证据：`e2e/.artifacts/reverify4/34-real-test-run.txt`（`node calc.test.js; echo EXIT=$?` 以 `t.js` 到达 bash，`EXIT=127`）、`15-terminal-ls.png`（`git log` 停在 `(END)`）、`23-fork-name-dialog.png`（「创建分支」灰着、界面不说为什么）。部署 `http://127.0.0.1:7722`（`pf-local`）。本批修的是「声称支持、实际会吞用户输入」，不改变任何 FR 状态：**126 条 FR 仍为 DONE 68 / PARTIAL 42 / MISSING 15 / 不做 1**；**整表 135 行 = DONE 77 / PARTIAL 42 / MISSING 15 / 不做 1**。

### 1. 丢字的根因：客户端在 socket 未 OPEN 时丢掉按键，不是合并、不是服务端 PTY

三个候选逐个量过：

- **客户端有没有合并/节流**：在页面里包住 `WebSocket.send`，socket 打开后输入 26 个字符 + Enter，记到 **27 个二进制帧**（一字符一帧），xterm 的 `onData` 没有 coalescing；`send` 路径上也没有 debounce/throttle。
- **服务端 relay 会不会丢**：绕过客户端，用登录态页面直接打开 `/pty`，按 25 ms/char 逐字节发 26 帧，再用 0 ms 发同长度的 burst。两次 bash 都执行了命令（`FAST-ISO-5678`、`BURST-ISO-1234`，`RC=0`），`e2e/.artifacts/fix-term/server-isolation.json`。所以 PTY 读循环、`Term.Write`、ring 都不是丢字点。
- **真正的丢弃点**：`web/lib/terminal/use-pty.ts` 的 `send`：`if (ws?.readyState !== WebSocket.OPEN) return;`。浏览器在 CONNECTING 状态调用 `send()` 会抛 `InvalidStateError`，旧代码于是选择静默丢掉——这就是每条命令丢「前缀」而不是整条的原因：能丢的只有「输入开始 → socket open」这一段，窗口结束后 socket 打开，剩下的字符照常到达。证据里的三处前缀缺口分别约 5 / 13 / 19 个字符；在 25 ms/char 下就是 125–475 ms 的量级，与「连接窗口有多长就丢多少个」的形状一致。

把窗口做成确定性的：CDP `Network.emulateNetworkConditions` 把延迟设成 500 ms 时，同一页面的 WebSocket handshake 从 **3.1 ms** 变成 **519.6 ms**（`server-isolation.json` 的 `handshakeNormalMs` / `handshakeLatencyMs`）；设成 1.5 s 时，旧客户端在窗口内发出 **0 个帧**，终端里只剩提示符（见下面的 e2e fail-first）。

**这不是与其他功能共享的 WebSocket 层**：`use-pty.ts` 是终端面板自己的 hook；浏览器 screencast、SSE 事件流、gate 的 WebSocket 都走别的路径，本批没有改它们。即便如此，完整 e2e（181 条）仍作为「其他功能没被带坏」的证明。

### 2. 修法

- **输入队列**：每次 `send` 作为一个独立 chunk 入队，`onopen` 时按原顺序**逐帧** flush（不合并、不拼接）；socket 已 OPEN 时仍然同步直发，不增加一次 round-trip，也不加 tick。断线重连（500 ms 退避）期间继续入队，下一次 open flush；`terminate()`、干净的 `close(1000)`、协议 `error`、以及组件卸载都会清空队列，避免把输入打进一个用户已经关掉的 shell。flush 中途抛错时把未发送的尾部按顺序放回队首。连接期间请求的 `resize` 只记最新一次，open 后补发（否则 shell 会用服务端默认列宽，长提示符与回显会错位）。
- **PTY 环境无分页器**：`term/process.go` 新增 `terminalEnv()`，为 PTY 会话设置 `PAGER=cat`、`GIT_PAGER=cat`、`GIT_TERMINAL_PROMPT=0`、`TERM=xterm-256color`。它是**替换**而不是追加：`execve` 会把重复项原样传下去，而 libc 的 `getenv` 取第一个匹配，追加的 `PAGER=cat` 盖不住继承来的 `PAGER=less`。unix PTY 与 Windows 管道共用这一份环境。
- **分叉对话框**：`fork-dialog.tsx` 在名称输入框下加一行「分支名称必填：它同时用作新分支的标签和 worktree 名，填写后「创建分支」才可用。」（`data-testid="fork-label-hint"`，并接到 `aria-describedby` / `aria-required`），禁用按钮补 `title="请先填写分支名称"`。按钮行为不变，只是不再「看起来坏了」。

### 3. fail-first（旧代码实测，不是推断）

- **单测**：把 `use-pty.ts` 换回改动前（`8fa5000^`）跑新测试 → `2 failed | 2 passed`；失败的两条收到的帧是 `[]`（排队与重连都没有 flush）。日志 `e2e/.artifacts/fix-term/fix1-unit-failfirst.log`。
- **e2e**：旧前端 + 1.5 s 延迟 handshake，`typing before the socket opens is buffered, not dropped` 失败在 `Expected substring: "PRECONNECT-…-42"`，`Received string` 只有 `PS C:\…\repo> `（一个字符都没到 shell）；同一 spec 里「socket 已打开时的 0 ms burst」通过，说明失败只发生在连接窗口。日志 `fix1-e2e-failfirst.log`。
- **部署（旧镜像）**：`fix-term-verify.mjs` 的旧镜像运行（`pre-fix-deployed.log`）——连接窗口内输入的整条命令没到 shell；`git log --oneline` 进入 `less`（帮助屏 + `(END)`），随后 `waitPrompt` 超时，因为终端被分页器占着，下一条命令根本进不去。
- **FIX 2 的环境部分**：旧 env（只有 `TERM`/`PIFORGE_TERM`）跑 `TestTerminalEnvReplacesInheritedPager` → `PAGER = "less", want "cat"`、`GIT_PAGER = "less"`、`GIT_TERMINAL_PROMPT = "1"` 三条失败（`fix2-failfirst.log`）。Windows 的终端是管道，git 不会在非 TTY 上起分页器，所以这条的「真实分页器」证据在部署的 Linux PTY 上。
- **FIX 3**：旧对话框跑新 e2e → `getByTestId("fork-label-hint")` `element(s) not found`（`fix3-e2e-failfirst.log`）。

### 4. 部署实测（新镜像，真实 Linux PTY）

- 镜像只用 `pwsh scripts/build-app-image.ps1` 重建：`sha256:e22828270617b079c77dac0487043dc5eae8060ec4d4d402c92501e66f1add69`。`pf-local` 用**同一个数据卷** `9ce20975339ab49b4b933f5a4b77acbb406eb4b426ba7ab79c3977ab15111f36` 重建：旧容器 `98abb92021ac…` → 新容器 `0fe80fa91b45…`，端口 7722→7700；`PIFORGE_DEFAULT_PROVIDER=volc`、`CapDrop=["CAP_DAC_OVERRIDE"]`；启动日志 `sandbox disk quota limit="2.0 GiB"`；`/healthz` 200。
- `e2e/scripts/fix-term-verify.mjs` 对新部署**全部通过**：
  - 0 ms burst 的 `echo AAAA-BBBB; echo EXIT=$?` 完整、`EXIT=0`；
  - 1.5 s 延迟 handshake 下、open 之前输入的 `PRECONNECT-…` 完整执行且 `EXIT=0`（截图 `verify-2-preconnect.png`）；
  - `git log --oneline` 打印两个 commit 后**回到提示符**，`echo AFTER-GIT-LOG` 执行（`verify-3-git-log.png`、`verify-4-git-log-after.png`）；
  - 300→420 行的 `git diff` 不分页，`AFTER-GIT-DIFF` 执行（`verify-5-git-diff.png`）；
  - 分叉对话框的必填提示可见、「创建分支」禁用中、填名后可用（`verify-6-fork-hint.png`）。
  - 终端原文：`verify-git-log-paged.txt`、`verify-git-log-after.txt`、`verify-git-diff.txt`；机器可读结果 `verify-log.json`。
- 探针建的 9 个 `fixterm-*` 项目已全部删除（DELETE 200/200/…）；部署上没有运行中的会话（7 个会话全部是 active/idle，无 agent 进程；`ps` 只有 `piforged` 本身）。

### 5. 门禁

- `go build ./...` 退出 0（`e2e/.artifacts/fix-term/go-build.log`）；`go vet ./...` 退出 0（`go-vet.log`）；`go test ./... -count=1` **35 个包全 ok、0 FAIL**、退出 0（`go-test.log`，含 `internal/term` 的 4 条新测试）。
- `pnpm --dir web test -- --run`：**34 个文件 / 272 例通过**（`web-test.log`）；`pnpm --dir web exec next build` 退出 0（`web-build.log`）。
- 完整 e2e：`pnpm --dir e2e test:fresh` → **181 passed（8.8 分钟）**，fresh 数据，`prepare.mjs` 重新编译了后端二进制（所以跑的是本批的 Go 代码）。181 = 原 178 + `90-terminal-input.spec.ts` 2 条 + `18-branching.spec.ts` 的 fork-hint 1 条。日志 `full-e2e-fresh.log`。
- `pwsh scripts/deliver-check.ps1`：`PIFORGE_DRILL_KEY` 从 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY` 读入（只在该进程环境里，未打印、未落盘），run `20261031-203352`，**119 条检查全部 PASS / 0 FAIL，`failure` 为 null**；drill 断言的镜像 id 就是部署的 `sha256:e2282827…`；同一份镜像的 drill UI 检查在真实 PTY 里 `echo ui-live > ui-live.txt` 并确认文件树无刷新出现，终端的输入与环境在这条路径上一起被验证；一次性容器 `pf-drill` 与 volume `pf-drill-data` 已删除，`pf-local` 容器 id 前后都是 `0fe80fa9…`。日志 `deliver-check.log`，summary `e2e/.artifacts/delivery-drill/20261031-203352/summary.json`。
- 提交：`8fa5000`（输入队列 + 单测 + e2e + 两个测量探针）、`19a8d5d`（PTY 环境 + Go 测试）、`d23c2d0`（对话框 + e2e + 部署验收探针），本文件单独一笔。每笔都只 `git add`/`git commit -- <自己的路径>`，`git show --name-only` 已核对。

### 6. 边界（明确没做的）

- **队列只覆盖「open 之前」**：它不保证「send 之后连接立刻断」的字节到达 PTY——WebSocket 没有应用层 ack。那需要输入序号/重传协议，本批没做；服务端的 ring 仍只保证**输出**侧回放（FR-10.3 的既有语义）。
- **队列没有大小上限**：连接长时间不恢复而用户持续输入时内存随输入增长；`terminate`/干净关闭/协议错误/卸载会清空，但没设阈值。任何「满了丢最旧/拒绝输入」都会再次变成「用户输入消失」，所以本批没有引入一个会静默丢字的阈值。
- **分页器只修 host PTY**：`internal/term/remote.go` 的 AIO 命令会话没有 TTY 语义，PAGER 由容器镜像决定，本批没碰。
- **用户显式选择仍生效**：在会话里自己 `export PAGER=less`（或 `git -c core.pager=less log`）仍会进分页器——那是用户的显式选择，不是默认环境。
- **单测覆盖的是 hook**；xterm 的按键路径、真实 PTY 的集成由 e2e 与部署探针覆盖。

## 本批：中断后下一条消息被拿去回答上一条请求（复现、定层、修法与边界）

部署 `http://127.0.0.1:7722`（`pf-local`），账号 `dev@piforge.local`。本批不改变任何 FR 状态：**126 条 FR 仍为 DONE 68 / PARTIAL 42 / MISSING 15 / 不做 1**；**整表 135 行 = DONE 77 / PARTIAL 42 / MISSING 15 / 不做 1**。它不是新功能，而是 HANDOFF §2 那条「未核实」的报告：停止后紧接着发的下一条消息，回答却关于上一条请求。

### 1. 先复现：确定性，不是偶发

探针 `e2e/scripts/fix-interrupt-context-probe.mjs`（原始数据 `e2e/.artifacts/fix-interrupt-context/`，每次运行一个 `run-N.json`）：建独立项目/会话 → 发「Count from 1 to 200…」→ 等到**正文在流式输出** → `POST /interrupt` → 立刻发「Say beta.」→ 记录 POST 回应、SSE 全量事件、`GET /entries` 与 agent 自己的 `/tmp/.piforge/sessions/<id>.jsonl`。

- **修复前 6/6 复现**：`Say beta.` 的回答是 1–200 的列表，或「The user wants me to count…」这类续写。只有一次（中断落在 **工具调用** 阶段、被中断的 assistant 消息里没有正文）回答是 `beta`，机制相同：没有可续写的正文。
- **UI 路径同样复现**：`fix-interrupt-context-ui.mjs` 用真 Chrome 点「停止」后立刻输入并回车，transcript 的最后一条 assistant 就是 1–200，最后还缀了一句 `beta`。

### 2. 逐层排除：交付、归属、reducer 都不是原因

同一次运行里三份证据互相独立：

- **消息到达了模型**：agent 的 session 文件里 `user: "Say beta."` 就在那条被截断的 assistant 之后，且它后面那条 assistant 是该 run 自己生成的（流式 delta 完整）。
- **没有旧 run 的影子**：`run.settled{aborted}` 之后是**新的** `run.started`（新 run id），POST 回 `disposition: "started"`；没有复用旧 run id，也没有「已加入队列」。
- **不是 reducer 张冠李戴**：事件顺序（seq）与新 run id 一致，前端把回答画在新 turn 下；不是把上一轮的文本挂到了新 turn。

也就是说：Pi 把被中断的 assistant 消息留在会话里，模型看到「计数请求 + 半截回答 + Say beta.」后选择继续/重做上一件事。**这一层不在我们这边**：Pi 的 session 是它自己的持久化历史，RPC 没有删除/改写 entry 的接口（`fork` 会新建分支，不能就地改）。

### 3. 修法：服务端在「中断后的第一条消息」里说明中断

`handlePostMessage` 在两种 compose 路径（行内评论、继续）之后，检查 `store.LastInterruption`：如果最近一次结局是中断、且之后没有 run 开始（`Continued=false`），就在消息前加一句
`[上一轮已被用户中断并作废：不要继续它，也不要重新执行上一轮的请求。只回答下面这条消息。]`。
它是**服务端拼装**，所以 transcript 显示的就是 agent 收到的字符串（与行内评论/继续同一条原则）；只加在中断后的**第一条**消息上，之后 `Continued=true`，不再重复。

**效果（同一序列，部署实测）**：修复前 0/6；加这句后 **5/5**（旧措辞 4/5，两句叠加的一次 3/4——它是强缓解，不是数学保证；模型仍可能执意做完上一件事）。这才是诚实的边界：产品能保证的是「消息一定以自己的 run 送达、归属正确，并把中断告诉模型」，不能保证任意模型一定听从。

### 4. 没复现的那一半：29 ms

报告里「interrupt→continue 只用了 29 ms」没有复现：13 次中断里，中断后的新 run 最短 1.4 s（多数 2–5 s），被中断的 run 在**发下一条消息之前**就已经 settle（`run.settled` 的 seq 在前）。库里唯一一次 <1 s 的 run（7 ms）是 provider 不可用，它渲染成可见错误，不是静默。UI 里「settled 很快」的观感有个更简单的解释：SSE 200 ms 轮询下，上一轮 `run.settled` 可能在用户发消息之后才到达页面，看起来像新 run 立刻结束——事件日志里它是旧 run 的 settle。

### 5. fail-first

- **单测**：`httpapi/interrupt_note_test.go` 三条（拼接后的 prompt、存库消息与 prompt 一致、run 开始后不再重复、继续路径不重复加）。
- **e2e**：`e2e/tests/91-interrupt-then-message.spec.ts`。去掉本批的 `handlePostMessage` 改动重编译后端后跑它 → 失败在 `Expected substring: "上一轮被用户中断"`（旧代码的 follow-up 是裸文本）；加回改动 → 通过。
- **部署**：上面 6/6（旧镜像）与 5/5 + 5/5（两个修复后镜像）。

### 6. 镜像与门禁

- 镜像只用 `pwsh scripts/build-app-image.ps1`：`piforge:local` = `sha256:15eb2f9c14ad9fb62ba9051e6a7c958de3c4683ef265e4a296f80d830d74a6f4`。`pf-local` 用**同一个数据卷** `9ce20975339ab49b4b933f5a4b77acbb406eb4b426ba7ab79c3977ab15111f36` 重建，容器 `7dc14e4a7525…`，端口 7722→7700，`PIFORGE_DEFAULT_PROVIDER=volc`、`CapDrop=["CAP_DAC_OVERRIDE"]`、`RestartPolicy: "no"`、启动日志 `sandbox disk quota limit="2.0 GiB"`、`/healthz` 200。
- 门禁：`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；`pnpm --dir web test -- --run` 34 文件 / 272 例；`pnpm --dir web exec next build` 退出 0；完整 e2e **182 passed**（含 preflight 守卫；chrome 项目 181 条）；`pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 取自 `~/.dsh/.credentials.yaml` 的 `VOLCE_API_KEY`，未打印、未落盘）run `20261031-215046`：**119 PASS / 0 FAIL，exit 0**，drill 断言的镜像就是部署的 `sha256:15eb2f9c14ad…`，演练前后 `pf-local` 容器 id 都是 `7dc14e4a7525…`，源 checkout 干净。
- 探针建的项目/会话已全部 `DELETE`，部署上没有运行中的 agent（`pgrep -x pi` 为空）。

### 7. 边界（明确没做的）

- **模型可能不听话**：句子是提示，不是硬约束；实测 10/10（最终措辞）但一次旧措辞/叠加测试里仍有 1 次跑偏。要 100% 只能把上一轮请求从上下文里抹掉，那等于篡改用户自己的历史，没做。
- **只覆盖第一条消息**：同一 run 里第二条消息仍可能接着上一条回答，那是正常对话，不是丢消息或错归属。
- **不改 Pi 的 session**：曾试过删掉被截断的 assistant entry（`context_edit`/截断文件 + 重启进程），实测模型改为去补做那条没人回答的旧请求，更糟；所以最终没有做文件手术。




## 本批：agent 进程进入沙箱（stage 1，`-agent-in-sandbox`，默认关闭）

这一条不是 FR：PRD 没有"执行边界"这一行，它是 `docs/HANDOFF.md` Known limit #1 与
`docs/SANDBOX-DEPLOY.md` §1.4.2 最后一条"还没修"的内容。设计写在
`docs/EXECUTION-BOUNDARY.md`（RPC 通道的三种做法与取舍、`materialize` 怎么离开共享卷、
`models.json` 与 provider key 的边界、远端心跳、四个阶段的验收），落地的是**阶段一**。

**改了什么**：控制面不再 `exec.Command("pi")`。`internal/pi` 的 `RuntimeConfig.Dial`
原本就为此留了缝，现在填进去的是"让沙箱启动同一个 argv、把 stdio 接回来"：
runner 新增 `POST /v1/sandboxes/{id}/attach`（hijack + `docker exec -i -u gem`），
`piforged agent-launcher` 与 `pi` 都在沙箱里，`/workspace/.piforge/live/<session>` 的
marker 由沙箱内的 supervisor 写、探针一行没改，`models.json` 经沙箱文件 API 写进
`/workspace/.piforge/agents/<session>/`，会话文件放在 `<pair>/.piforge/sessions/`（共享卷
上同一份字节，cwd header 由沙箱用户对齐）。`-sandbox local` 完全不变：该 flag 在 local 下
是启动错误。

**没改什么（必须这么说）**：agent 仍会随控制面重启而死——阶段一的 RPC 是控制面持有的
TCP 连接，重启即 EOF，`pi` 退出、marker 被 launcher 删除，于是对账**立刻**答"死"（比
§1.4.2 的 `kept_live=1` 两分钟更诚实，但不是"活下来了"）；`materialize`/`provision` 仍
写共享卷，所以控制面仍必须挂载工作区；MCP 配置故意不进沙箱（`mcp.json` 里是解密后的
凭据），带 MCP 的会话在该 flag 下暂时没有 MCP。

**验证**：7723 一次性 runner 实例（`pf-local` 未动，前后 `7dc14e4a7525…`、`/healthz` 200）。
沙箱内 `ps` 有 `gem … agent-launcher -root /workspace … -- pi` 与 `gem … pi`，控制面
`ps` 没有 `pi`；沙箱内 `/data` 不存在；一条真实任务 12 s 完成并提交 `de5061b`，测试面板
2/2，面板写/暂存/提交 `e30cbb5`，行内评论 21 ms 被接受后 agent 改名并提交 `3661297`；
`models.json` 与沙箱 env 里 provider key 命中数 0。门禁：`go build/vet/test` 全绿、
`pnpm --dir web test -- --run` 272/272、e2e 182/182、`deliver-check.ps1 -Image
piforge:agent-drill` 119 PASS（run `20261031-235815`）。清理：两个服务容器、四个
`piforge-*` 沙箱、卷 `pf-drill-data`、网络 `pf-drill-net`、三个镜像已删。


## 本批：工作区不再经过共享卷（stage 2，`-agent-in-sandbox`，默认关闭）

`docs/EXECUTION-BOUNDARY.md` 阶段二。上一批（阶段一）留下的原话是：`materialize`/`provision`
仍写共享卷，所以控制面仍必须挂载工作区。这一批把那一半搬走：**控制面完全不挂工作区**。

**改了什么**（按用户可见的顺序）：

1. **沙箱的 workspace 变成它自己的卷**。`piforge-ws-<pair hash>`，只挂在沙箱的 `/workspace`；
   `docker inspect` 里控制面只有状态卷、runner 只有 docker socket，挂这块卷的只有沙箱自己。
   挂载形状从**容器自己的记录**里认（卷名 = 容器名的 hash、无 subpath），不是从 flag 认，
   所以重启后的控制面和无状态 runner 对同一个容器得到同一个答案。
2. **沙箱行记录"工作区在哪"**：`sandboxes.workspace` = `host` | `api`，`root_dir` 在 detached
   形状下是容器路径 `/workspace`。之后每一次写都读这一列，而不是读一个内存里的 flag。
3. **投影只有一份实现，两棵树**。`materialize.FileSystem` 抽出 `MkdirAll/WriteFile/ReadFile/
   Lookup/List/RemoveAll/MakeReadOnly/MakeWritable/CanBind`；`Mount`（宿主）与 `MountFS`（沙箱
   文件 API）共用冻结规则、manifest、git exclude、digest 框架。provision 与 tooling 面板都经
   `WorkspaceFiles` 问沙箱，宿主路径不再出现。
4. **分支 worktree 的路径就是容器路径**（`/workspace/worktrees/<slug>`），面板/终端/git 用同
   一条字符串；`git worktree list` 在沙箱里能看到它，`.git` 文件里是容器路径。
5. **两个 workaround 按同一个位判断去留**：`planCheckout` 的 `chmod -R a+rwX` 和 git 的
   `safe.directory` 在 detached 形状里**不再注入**（只有一个写者、树本来就是 gem 的），在共享卷
   形状里**照旧注入**（那里确实有第二个写者，即 root 控制面）。没有"顺手放宽"。

**没有做的（写在 `EXECUTION-BOUNDARY.md` §10.1，不藏）**：`worktrees.base_commit` 在 detached
形状下为空（读 HEAD 需要容器里的 git，runner 没有 exec 端点）；agent stderr 仍进 runner 日志而
不是 Logs 面板；marker 仍带 pid 不带 boot id；回收快照与磁盘配额测不到只有容器能打开的工作区
（两者都是 fail-safe：快照失败就不回收，测不到就不拒绝）；容器内的 `CloneSession` 仍未搬。
`-agent-in-sandbox` 现在也要求 `-sandbox-files remote`（否则面板没有东西可读），是启动错误。

**验证**：7723 一次性 runner 实例，**不设 `SANDBOX_WORKSPACE_VOLUME`**、runner 不挂 `/data`
（`pf-local` 未动，前后 `7dc14e4a7525…`、`/healthz` 200）。控制面 `mounts: [volume pf-drill-data
-> /data]`、`ls /data` 里连 `sandboxes/` 都没有；沙箱 `mounts: [volume piforge-ws-ba8b2e998f171d81
-> /workspace]`，全机只有它挂这块卷。同一条 prompt 17 s 完成并提交 `ae151cc`（作者
`PiForge Sandbox|sandbox@piforge.local`）；技能树 `dr-xr-xr-x`/`-r--r--r--`，以 gem 写
`Permission denied`（覆盖与新建都被拒），`/v1/sessions/<sid>/skills` 的 mechanisms 与
`.pi/mounted.json` 都是三份 `copy-ro`；分支会话 `git worktree list` 显示
`/workspace/worktrees/asnbxt4n`；文件面板/测试面板（npm test 3/3）/行内评论（19 ms 接受，提交
`8bf6b63`）照常；沙箱里 `cat /data/piforge.db` 仍是 `No such file or directory`，env 与
`models.json` 的 provider key 命中数 0。门禁：`go build/vet/test` 全绿、`pnpm --dir web test --
--run` 272/272、e2e 182/182、`deliver-check.ps1` 119 PASS。清理：两个服务容器、`piforge-*` 沙箱、
卷 `piforge-ws-*` 与 `pf-drill-data`、网络 `pf-drill-net`、三个镜像已删。


## 本批：租户出口策略（`none` 默认关闭，`open` 显式，`allowlist` 启动即拒绝）

这不是 FR：它是上一批 `docs/EXECUTION-BOUNDARY.md` §12.4 自己写下的那条"没有出口控制"。补之前的事实是：沙箱在租户桥上仍能上公网，`npm install`、`curl`、被投毒的依赖都拿到宿主的出网能力，而部署里没有一处表达"这是不是故意的"。设计与取舍写在 `docs/EXECUTION-BOUNDARY.md` §13，运维与排查写在 `docs/SANDBOX-DEPLOY.md` §1.7.5。

**策略**：`internal/tenantegress`，文件 `deploy/tenant-egress.json`（`PIFORGE_TENANT_EGRESS_FILE`；控制面与 runner **都**读，启动校验；runner 之所以要读，是因为桥是它建的）。与配额分两个文件而不合成一份：配额只由控制面读、随 create 下发，出口必须由 runner 也读，而两个解析器都刻意拒绝未知字段——合成一份会让一个配额笔误能拖垮 runner 的启动。三种取值：

| 模式 | 执行 | 覆盖 / 不覆盖 |
|---|---|---|
| `none`（默认） | 该租户的桥 `docker network create --internal`（标签 `piforge.egress=none`） | 公网 TCP/UDP 无路由、外部 DNS 不解析；控制面与 runner 在这条桥上，所以 `/gw/llm`、`/gw/mcp`、checkout 照常。不覆盖：同租户桥上其它容器、控制面自己的出网、共享桥形状、`-sandbox local` |
| `allowlist` | **不执行，启动拒绝**（指出租户） | 全部。Docker 只能按桥过滤，按 hostname 需要内网桥上的过滤代理或宿主 iptables，两个都没做，所以不假装支持 |
| `open` | 普通桥 + Docker NAT | 和宿主一样 |

同一道启动闸门还拒绝：`none` + 共享网络、共享网络 + 按租户覆盖、`-sandbox local` + 任何关闭模式。没有配置文件又没给 `-tenant-egress-mode` 时，**每租户桥的形状默认关闭**（source=`network-mode`），共享桥的形状保持 open 并在日志 WARN、管理页写"未配置"——方向是"有边界的地方默认关，没有边界的地方说清楚"。改模式要重建桥：代码宁可拒绝建沙箱并给出 `docker network rm <name>`，也不留下"页面说关、桥还开着"的状态。

**可见性**：`GET /v1/admin/status` 的 `tenant_limits` 新增 `egress_mode`/`egress_source`/`egress_configured`/`egress_applied`，每行加 `egress`/`egress_override`；`/admin/status` 的「租户配额与网络」多一行「沙箱出口」，每个账号一个 `出口：关闭/开放`（单独配置会标出来）。`egress_applied=false` 只出现在 `-sandbox local`：模式被报出来，但它不是边界。

**实测**（`pwsh scripts/probe-egress.ps1 -Port 7723 -Project pf-egress`，一次性栈、自带 down/up、默认卷；同一栈上默认租户 `none`、`dev@piforge.local` `open`，**0 failures，exit 0**，产物 `e2e/.artifacts/egress/`）：

```
closed: network=piforge-t-29ec2c7a2fa8 Internal=true  label piforge.egress=none
opened: network=piforge-t-6a0e6d79cf7c Internal=false label piforge.egress=open

closed: getent hosts example.com -> DNS-FAILED
        curl -m 5 http://example.com  -> curl: (6) Could not resolve host: example.com   (000)
        curl -m 5 https://1.1.1.1     -> curl: (7) Couldn't connect to server             (000)
opened: getent hosts example.com -> 172.66.147.243 / 104.20.23.154
        curl -m 5 http://example.com  -> 200
        curl -m 5 https://1.1.1.1     -> 301

两个租户都: http://piforge:7700/healthz -> 200 ; /v1/projects -> 401
admin status: egress_mode=none source=/etc/piforge/tenant-egress.json configured=true applied=true
              admin=none / dev=open (override)
被关闭的租户里跑真实 prompt: settled=true，助手回复 DONE，
        http://piforge:7700/gw/llm/<token>/v1/chat/completions -> 200
pf-local (7722): 同一个容器 7dc14e4a7525…、StartedAt 不变、healthz 200（前后）
```

**门禁**：`go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿（新增 `internal/tenantegress`、`sandbox` 出口单测、两个 wiring 单测、httpapi 出口报告测试）；`pnpm --dir web test -- --run` 288/288；`pnpm --dir web exec next build` 通过；e2e 182/182；`deliver-check.ps1` 在 local 形态下用同一把 key 跑（见下）。

**没有宣称的**：出口是开关不是过滤器（`allowlist` 启动即拒绝）；共享单机形状与 `-sandbox local` 下没有出口边界；它不阻止沙箱让网关把 prompt 里的内容发出去（那是产品本身）；同租户两个沙箱仍互通 8080（§12.1 的既有边界）。


## 本批：对抗验收的四个真缺陷（root / 磁盘 / 网关 token / pause 422）

一次对抗验收（`e2e/.artifacts/adversarial/VERDICT.md`）以租户身份打了一套全新 isolated 栈：租户边界守住（读不到 `/data`、无出网、够不到别的租户），但边界**周围**有四个真缺陷。机制与取舍写进 `docs/EXECUTION-BOUNDARY.md` §14 与 `docs/SANDBOX-DEPLOY.md` §1.4.7；这里记的是改了什么、怎么证明、代价与仍开放的部分。

### FIX 1（LEAK，最高）租户在沙箱里是 root

**机制**：容器创建时 `--cap-drop ALL` + 只加回镜像实测需要的五个（`CHOWN`、`DAC_OVERRIDE`、`FOWNER`、`SETUID`、`SETGID`，逐个按镜像自己的启动日志加回，理由写在 `sandboxCapAdd`），外加 `--security-opt no-new-privileges`；沙箱镜像（`deploy/Dockerfile.sandbox-agent`）删掉 `/usr/bin/sudo` 与 `/etc/sudoers.d/gem`，`/opt/piforge` 与 `/opt/piforge/piforged` 设成 root 所有 `0555`，缺任何一项构建即失败。`no-new-privileges` 是结束 leak 的那一层：setuid 的 sudo 在 `no_new_privs` 下不再改变 euid，所以即使某个部署仍用原生 AIO 镜像，`sudo -n id` 也拿不到 root。

**前后对比**（同一套栈、同一个租户账号）：

```
before: sudo -n id -> uid=0(root) ; sudo -n -l -> (ALL) NOPASSWD: ALL ; CapEff 00000000a80425fb
after : id -> uid=1000(gem) ; sudo -> "not found" ; su root -> Authentication failure
        CapEff 0000000000000000 (agent) / 00000000000000cb (container)
        CapDrop=["ALL"] CapAdd=["CAP_CHOWN","CAP_DAC_OVERRIDE","CAP_FOWNER","CAP_SETGID","CAP_SETUID"]
        SecurityOpt=["no-new-privileges"]
        /opt/piforge dr-xr-xr-x ; piforged -r-xr-xr-x -> write: Permission denied ; rm: Permission denied
        SKILL.md -r--r--r-- -> echo >> : Permission denied
```

**代价**：五个 capability 留在容器（不是给 agent 账号，agent 是 0）；镜像的 root 引导（gem.sh、nginx master）仍需要它们，逐个写了原因。产品路径（pi、git、node、终端、code-server、Jupyter）全部以 `gem` 运行。

**仍然开放**：工作区里的 skill 树属于租户账号（沙箱文件 API 以 `gem` 写），0444/0555 挡住误写和旧的 sudo 路径，先 `chmod u+w` 仍可改——已在实测里明确复现并记录，不假装冻结。`/opt/piforge/piforged` 不可写但不做哈希校验。

### FIX 2（能搞掉部署）磁盘不限量而 UI 显示配额

**机制**：两层。① 容器 `--ulimit fsize=<配额>:<配额>`（`sandbox.Limits.DiskBytes`，从 `-sandbox-disk-quota` 经 runner 的 create 传到容器），软硬相同，shell 无法抬高；② 每 5 秒的沙箱内检查（`httpapi.StartDiskWatch`）：通过沙箱 API 跑 `du -sb /workspace`，超预算就停 agent（`Runtime.Stop` + 向沙箱内 supervisor 发 SIGTERM，因为容器里的 agent 故意能活过控制面断流）、写一条与 507 相同的 `platform.error`，持续增长则暂停沙箱；同一个测量同时供 admin 的 `sandbox_usage[]` 与运行边界 507 使用，三者不会再各说各话。测不出来时是 `unmeasurable: true`（页面「未知」），不再是 `0 B / over=false`。

**前后对比**：

```
before: fallocate -l 3G -> 成功，used_delta_bytes=3221229568，admin: quota=2 GiB over=false
after : ulimit -f -> 4194304 (2 GiB) ; /proc/1/limits Max file size 2147483648
        fallocate -l 3G -> File size limit exceeded（SIGXFSZ），0 字节文件
        dd 3 GiB 单文件 -> 截断在 2147483648 字节
        dd 2 GiB + 200 MiB -> admin: bytes=2357393472 over=true（3.0 GiB / 2.0 GiB 同理）
        跑着长任务的 agent：写完 2 GiB+200 MiB 后 supervisor 2 秒内消失，pi 进程 0 个，
        会话里出现 platform.error(code=sandbox_disk_quota, 与 507 同一句话)
        再发消息 -> HTTP 507 sandbox_disk_quota；删掉大文件后同一个会话可以继续（实测 accepted=True）
```

**这台 daemon 上不能用、已实测并写进文档的**：`--storage-opt size=` 被接受且被忽略（1 GiB rootfs 写进 1.5 GiB，`df` 仍 1007 GB；而且它管不到工作区卷）；ext4 project quota 需要 `prjquota` 挂载；size 受限 tmpfs 不持久；loopback 文件系统需要 `CAP_SYS_ADMIN`（已被 FIX 1 移除）。

**代价 / 仍开放**：单文件上限是每文件而不是每文件系统，5 秒检查意味着"很多个各自 2 GiB 的文件"可以超出几个 GiB 才被刹住（这里写入速度 ~1.3 GB/s，最坏约几 GiB）；暂停只发生在"停了 agent 之后还在长"的第 3 个 tick（约 10 秒），普通超配额只停 agent + 507，沙箱保持可清理。被暂停的沙箱由下一条消息唤醒（`wakeSandbox` + 沙箱 API 的就绪等待，之前会 500）。

### FIX 3（凭证爆炸半径）12 小时网关 token 在世界可写目录里

**机制**：① TTL 12h → 1h（`llmgw.DefaultTTL`，`-gateway-token-ttl` / `PIFORGE_GATEWAY_TOKEN_TTL` 可显式调大，boot 日志把数字和撤销时机一起打出来）；② 会话生命周期绑定：`pi.Runtime.Stop` 这个唯一收口调用 `Signer.Revoke(sessionID)`——手动暂停、空闲暂停、归档、删除、删项目、切模型、空闲回收、关闭进程全都会立刻作废；③ `.piforge/agents/<session>` 0700、`models.json`/`mcp.json` 0600，由拥有者账号在文件 API 写完（它写 0644）之后 chmod。

**前后对比**：

```
before: models.json 在 drwxrwxrwx 里 0644；token 12 小时；从宿主 GET /gw/llm/<t>/models -> 200
after : drwx------ 0700 / -rw------- 0600；claims ttl_s=3600 session_matches=True
        另一个账号（uid 65534）cat -> Permission denied
        宿主 GET /gw/llm/<t>/models -> 200；4-token 补全 -> 200（这一半是设计：沙箱要用）
        同一个 token 打 /v1/projects -> 401；/gw/mcp/<t> -> 401
        暂停沙箱（agent 停止）后，同一 token 从宿主 -> 401 invalid_token（立即，不等 TTL）
        恢复后新 spawn 发的新 token 可用，旧 token 仍然 401
```

**代价 / 仍开放**：撤销状态在内存里，控制面崩溃/重启会忘掉撤销，1 小时 TTL 是这个窗口的兜底（所以 TTL 不能再长）；单个 agent 进程跑超过 1 小时会到 TTL 后失去模型访问，下一次 prompt 重新签发。

**这个修复自己的复检发现的缺陷**（本批第二个 gateway 提交）：第一版只按 session id 撤销，暂停之后该会话的**所有** token 都被拒——包括下一次 spawn 新签的那个（实测 "a new spawn minted a different token: True; host GET with it -> 401"），于是"暂停过"的沙箱在 TTL 到期前都用不了网关。改成每会话一个 generation 计数：Revoke 自增、Issue 写进 claims（`g`）、Verify 要求相等。暂停前的 token 精确失效，恢复后的新 token 立即可用；不涉及时钟精度（暂停与恢复可能落在同一秒）。复检的最终输出：live token 200 / 补全 200，`/v1` 与 `/gw/mcp` 401，暂停后 401，恢复后旧 token 401、新 token 200。

### FIX 4（bug）pause/resume 422

**机制**：边界上做一次 id 归一（`sandbox.NormalizeSandboxID`：`piforge-<hash>` / `sbx_<hash>` / 裸 `<hash>` → 容器名），所有生命周期方法都走它，所以进程内 Docker provider 与 runner 的 HTTP API 都接受两种形式；handler 改为传行里记的 provider id，不再重新推导 pair id。

**前后对比**：

```
before: POST /v1/sessions/<A>/sandbox/pause -> 422 sandbox: not found: piforge-sbx_dff082834bc9e8d7
after : POST pause -> {"state":"PAUSED"} ; docker exited ; GET /sandbox -> PAUSED
        POST resume -> {"state":"READY"} ; docker running ; GET /sandbox -> READY
        暂停后立刻发消息：accepted=True（唤醒路径不再 500，见 FIX 2 的 wake 等待）
```

**回归测试**：`TestNormalizeSandboxID`、`TestDockerPauseAndResumeAcceptBothIDForms`（真容器，两种 id 形式都能暂停/唤醒）、`TestSandboxPauseAndResumeUseTheRowID`（HTTP 边界；在旧代码上失败信息是 `pause reached the provider with [sbx_…], want [piforge-…]`）。

### 门禁（本批）

见本节末尾的实测行（`go build` / `go vet` / `go test ./... -count=1`、`pnpm --dir web test -- --run`、`pnpm --dir web exec next build`、e2e 182、`deliver-check.ps1` 119）。对抗复检脚本与完整输出在 `e2e/.artifacts/adversarial-fixes/`（`final-transcript.txt`、`agent-stop-transcript.txt`），一次性栈用 `pwsh scripts/up-isolated.ps1` 起在 7723、用 `-Down` 收掉；`pf-local`（7722）前后同一个容器 id 与 healthz。

## 本批：第二轮对抗验收的三个缺陷（进程风暴的不可恢复、恢复后对话为空、两个不诚实的数字）

第二轮对抗验收（`e2e/.artifacts/adversarial2/VERDICT.md`）用一套全新 isolated 栈从租户
身份又打了一遍。租户边界仍然守住，上一批的四个修复逐条复检也都成立（没有 root、单文件
被内核卡住、一小时的 token 暂停即失效、pause/resume 可用）。它打出来的是**边界外围**的
三件事：一个租户可以让自己的项目永久不可用且产品里没有恢复手段；暂停再恢复之后对话端点
返回空；以及两个"产品说了一个数、实际是另一个数"的地方。机制与实测写进
`docs/EXECUTION-BOUNDARY.md` §15、`docs/SANDBOX-DEPLOY.md` §10、`docs/HANDOFF.md`
「Sandbox liveness」；这里记改了什么、怎么证明、代价与仍开放的部分。

### FIX 1（最严重的运维缺陷）进程风暴把沙箱顶死，产品没有恢复路径

**复现与机制**：沙箱里跑进程风暴把 `pids` cgroup 打满。此后容器**仍显示 running**，
但任何新进程都起不来：`docker exec` 报 `unable to spawn stage-1: Resource temporarily
unavailable`（exit 128，约 50 ms，概率性失败，因为风暴本身在抖动出空位）、平台的 `du`
超时（`unmeasurable: true`）、agent 起不来。唯一的恢复是人工 pause + resume。

**先测，再改**（`scripts/measure-pids.ps1`，本机、出厂镜像 `--cpus 1 --memory 2g`）：

```
空载/启动下限            镜像自带运行时：83–85 个 pid（线程）/ 25 个进程
                         pids=64 时少一个 node 服务（pids 45、22 进程）
                         pids=96/128/192/256/512/1024 完整启动

有界风暴（timeout 8 bash -c ':(){ :|:& };:'）
                         pids=64/96/128/192/256/512/1024 **每个上限**都在约 1 s 内被打满；
                         打满期间连 `cat /sys/fs/cgroup/pids.current` 都 fork 不出来；
                         `docker exec true` 在看得到的每个上限上都失败，约 50 ms

恢复（= 产品的 Pause/Resume）  docker stop -t5 4.99 s + docker start → 0.91 s 内可执行 = 5.9 s

正常租户（pids=256）      40 个并发进程 + 8 个 node worker 线程：85 → 125 个 pid，exec/du 正常
```

两个结论，第二个决定了修法：

1. 报告里"512 已被 AIO 镜像吃掉大半"在本机不成立（空载只有 ~83 个线程）。但默认值仍然
   从 **512 降到 256**（`deploy/tenant-limits.json`、`docker-compose.runner.yml` 的
   `${PIFORGE_SANDBOX_PIDS:-256}`）：在空载之上留 ~170 个槽位给租户的构建，同时把单个
   恶意沙箱能在共享 daemon 上压住的进程数减半。
2. **任何上限都挡不住这件事**：风暴会填满给它的任何数字，64 和 1024 一样。所以没有走
   "把 pids 调小到 exec 不失败"这条路（那条路不存在），而是做了检测 + 自动恢复——也正是
   任务里 (b) 的选项。`ulimit nproc` 的"保留额度"也实测过并放弃：`nproc=192` 时镜像
   自己的引导就死了（`su: failed to execute /bin/bash: Resource temporarily
   unavailable`），能启动的值又留不出有意义的余量。

**修法**（`backend/internal/httpapi/sandbox_watch.go`、`backend/cmd/piforged/sandbox_probe.go`）：

- 每 **10 s** 对每个 READY/BUSY/IDLE 沙箱跑一次 `true`，**经沙箱自己的 API**，3 s 上限
  ——和 agent 走同一扇门、同一个账号，所以"API 跑不动命令"就是用户遇到的那件事；`du`
  式的 30 s 测量不能当探测信号（它超时，而超时说明不了"不能 fork"）。
- 连续 **2 次**失败判为顶死（一次可能只是宿主机忙），最迟约 25 s 内发现。
- 恢复 = 运维手动做的那件事：`docker stop` + `docker start`（实测 ~5 s）。工作区是卷，
  所以 checkout、对话、agent 的文件都留着；风暴的进程全在容器里，随容器一起消失。
- 恢复后**再探一次**才算成功（最长 45 s），确认之后才写沙箱行的状态——"重启了"不等于
  "能用了"。
- 会话里用 `platform.error` 说两句话：动手前 `sandbox_unresponsive`
  「沙箱无响应，正在恢复」；成功后 `sandbox_recovered`「沙箱已恢复（用时 5.9 秒）…」；
  失败则 `sandbox_recovery_failed` 并说明下一步。
- 同一个动作给出**一键入口**：`POST /v1/sessions/{id}/sandbox/recover`（先探测，健康的
  沙箱是明说原因的空操作），会话视图顶部「沙箱无响应，正在恢复」旁边的**「恢复」按钮**；
  `GET /v1/sessions/{id}/sandbox` 暴露 `responsive`（`null` = 还没探过，不是"坏了"）、
  `recovering`、`can_recover`。

**代价 / 仍开放**：这是修复而不是资源策略——同租户再打爆一次，watchdog 会再修一次（每次
一个 WARN），循环打爆是租户治理问题（`max_sandboxes`、删项目），watchdog 不替人做决定。
容器"不见了"（而不是"顶死了"）归 `reconcile`，不在这里。恢复会丢掉容器内 `/tmp` 和正在
跑的 agent 进程（这正是目的），卷上的东西不丢。

### FIX 2（显示/API 缺口）pause → resume 之后 `entries` 是空的

**实测**：`POST …/sandbox/pause` → `resume`、**不发新 prompt** 时，
`GET /v1/sessions/{id}/entries` 返回 `{"entries":[],"leaf_id":""}`、`/status` 的
`last_seq=0`，而 `/stats` 仍是 2 条用户 / 2 条助手 / 3271 tokens，沙箱里的
`/workspace/.piforge/sessions/<id>.jsonl` 是完整的 8093 字节。数据没丢，端点答的是一个
resume 不会启动的进程。

**修法**：`pi.Runtime.Entries` 在没有活进程时回退到**会话文件**
（`RuntimeConfig.TranscriptFor`，`backend/internal/pi/transcript.go`），部署侧由
`cmd/piforged/agent_sandbox.go` 的 `Transcript` 实现——经沙箱自己的 API 读
`/workspace/.piforge/sessions/<id>.jsonl`，因为这正是容器placement 里对话真正所在的位置。
`?since=<entry>` 在文件上生效；游标在文件里找不到时返回整个 transcript（重复显示可以修，
显示成空不行）；文件读不出来仍然回退成"没有活进程"的诚实答复，绝不伪装成"成功的空对话"。

**测试**：`TestEntriesReadTheTranscriptWhenNoAgentIsRunning`、
`TestEntriesFromTranscriptHonourSince`、`TestEntriesFromTranscriptToleratesAColdOrTornFile`、
`TestEntriesWithoutATranscriptHookStillReportNoProcess`（pi），以及
`TestEntriesSurvivePauseAndResumeWithoutAPrompt`（httpapi，**真** `pi.Runtime` + 真会话
文件：pause → resume → GET entries 返回 4 条与 `leaf_id=a2`，全程不发 prompt）。

### FIX 3（便宜的两个诚实项）

**(a) 文件超限时租户看到的是 shell 的那句话。** 越界的写被内核用 `SIGXFSZ` 终止，租户
看到的就是：

```
$ fallocate -l 3G /workspace/ballast.bin
File size limit exceeded (core dumped)
$ echo $?
153                       # 128 + 25
```

内核信号无法被产品改写（包一层 shell 只会把 `exit 153` 变成别的数字，更容易误导），
所以修的是文档：`docs/SANDBOX-DEPLOY.md` §10.1 给出**精确原文**、退出码 0 字节文件、
`dd` 截断在正好配额处的样子，以及它和总配额 507 `sandbox_disk_quota` 的区别（谁说的、
什么意思、怎么处置）。同一节还写了终端写者收不到总配额消息这件事本身。

**(b) admin `sandbox_usage[].files` 恒为 0。** 沙箱内的测量只取了 `du -sb` 的字节数，
注释里写着"文件数故意不测，第二次 `find` 会把遍历翻倍"。其实从不需要第二次遍历，只需要
第二列：一次 GNU `find -printf '%y %s\n'`，`awk` 同时累加每个 entry 的 `st_size`（与
`du -sb` 逐字节一致）和非目录 entry 的个数。400 个文件的树实测 `412288 400`：字节
412288 = `du -sb` 的 412288，文件数 400（原来 0）。量不出来时仍是 `unmeasurable: true`，
不会伪装成 0。

### 门禁（本批，实测输出）

```
go build ./...                                   ok
go vet ./...                                     ok
go test ./... -count=1                           所有包 ok（httpapi 39.6s，pi 5.9s）
pnpm --dir web test -- --run                     38 files / 292 tests passed
pnpm --dir web exec next build                   ok（Next.js 16.4.3 / Turbopack）
pnpm --dir e2e test                              182 passed (8.8m)
pwsh scripts/deliver-check.ps1 -Image piforge:local   119/119 checks passed（110.2s，
                                                 artifacts: e2e/.artifacts/delivery-drill/20261101-093625）
```

一次说明：第一次全量 e2e 与三个镜像重建/Delivery drill 同时跑，出现 1 failed + 1 flaky
（`81-provider-lifecycle` 的页面是 Next 的 "This page couldn't load"、`83-browser-takeover`
等 `panel-tab-browser` 超时）。两个用例单独重跑 4 passed，机器空下来后全量重跑 182 passed
——这是负载导致的页面资源加载失败，不是本批代码；本批新增的会话页组件有 4 个组件测试
（`sandbox-health-bar.test.tsx`）与既有 `workbench.test.tsx` 覆盖。

实测栈用 `pwsh scripts/up-isolated.ps1 -Rebuild` 起在 7723、用 `-Down` 收掉；`pf-local`
（7722）全程未动：同一个容器 id `7dc14e4a7525a8…`、`started=2026-10-31T13:48:51Z`、
`restarts=0`、`/healthz -> 200`。租户沙箱实测 `PidsLimit=256`（新默认已落到容器）。

本批的实测输出：

```
e2e/.artifacts/fixes2/20261101-093320/drill.txt   全流程（FIX 2 / FIX 1 / FIX 1 二次 prompt / FIX 3b）
e2e/.artifacts/fixes2/20261101-093320/sse.txt     会话 SSE：seq 23 sandbox_unresponsive、seq 24 sandbox_recovered（7 391 ms）
e2e/.artifacts/fixes2/evidence-watchdog.txt       watchdog 日志、容器 PidsLimit、limits 文件、pf-local 身份
e2e/.artifacts/pids/measure-*.txt                 scripts/measure-pids.ps1 的完整表
```

## 本批：第三轮对抗验收留下的两条风险（磁盘突发上界 + 撤销落库，2026-11-01）

`e2e/.artifacts/adversarial2/VERDICT.md` 的三条「运维会担心」里，这一批关掉两条，第三条
（token 本身是 1 小时可携带 bearer 凭证）如实保留、不写成已解决。**（第三条已在下一批关掉：
见文末「本批：token 只在自己的沙箱里能用」（2026-11-01）；这一段保留原文。**

### FIX 1（安全）撤销集合从内存搬进数据库

- 新表 `gateway_revocations(session_id PK, generation, revoked_at, expires_at)`；
  `RevokeGatewayToken` 在一个事务里读+加+写，`expires_at = now + TTL`。
- 写发生在让 token 失效的同一次调用里（`Signer.Revoke` ← `pi.Runtime.Stop`），不是队列：
  `200 PAUSED` 意味着行已落盘。
- `Signer.Verify` 每次 `/gw/llm` 读这个 generation（主键一行，HMAC 之后），与内存快路取
  max；读失败才退回内存（永不比旧行为更松）。`Signer.Issue` 读同一个数，所以恢复后的新
  token 带落库的 generation 立即可用；`ExtendGatewayRevocation` 保证清扫不会删掉仍在用的
  generation。
- 清扫每 10 分钟一次（`cmd/piforged`）。表故意没有指向 `sessions` 的外键：删会话不能把
  墓碑一起删掉，否则撤销前的 token 会复活。
- **关机是唯一不落库的 stop**：容器里的 agent 按设计活过控制面，没有新 prompt 签不出新
  token，关机撤销等于推翻阶段三。`revocationHook.drain()` 注册在 `rt.Close()` 之前。
- 实测（7723 隔离栈，`scripts/probe-gateway-revocation.ps1`）：token A 暂停前 200 → 暂停后
  401 → `docker restart piforge` 之后仍 401，重启进程日志 `on_record=1`；恢复后新 token
  （g=1）200，而 token A 仍 401。token 只打印长度/sha256 前缀/claims。

### FIX 2（资源）磁盘突发：实测 3.27 倍 → 1.05 倍

先量后改：`scripts/probe-quota-overshoot.ps1` 在沙箱里以 `gem` 突发写、宿主每 200 ms 采
`du -sbx /workspace`。改动前（HEAD `0592c27`，2 GiB 预算）**7 029 794 947 B = 3.27x**，
十秒、写者仍在跑、沙箱仍 READY。

四个选项都在这台 daemon 上量过：(a) 自适应间隔（2 s 有 run / 10 s 空闲）采用；(b) size
受限 tmpfs 确实硬顶（2147483648 B 处 `No space left on device`）但 `docker restart` 后
`total 0`，工作区全丢 —— 否决；(c) `--storage-opt size` 被接受且静默忽略（`df /` 仍
1006.9G）—— 否决；(d) 沙箱内执行器采用。

采用 (a)+(d)：`piforged quota-enforcer` 每 100 ms `du -sbx`，超预算对"持有可写 fd 的进程
+ 能再起写者的祖先链（遇到监听套接字就停）"发 SIGSTOP；只有冻结后又长 ≥ 1 MiB（新写者）
才升 SIGKILL。由镜像的 `RUN_HOOK_PRE_SERVICES` 启动（runner 提供方刻意没有 exec 端点），
预算读 `/proc/self/limits`，与每文件 RLIMIT 同一个数。

实测（同一探针、同一栈）：文件流 **2 265 053 457 B = 1.05x**（同一形态复跑 2 172 778 769 B = 1.01x），单大文件 **2 156 001 554 B
= 1.00x**，混合 **2 171 730 202 B = 1.01x**；写者被找到时是 `T`，沙箱保持 READY。

比旧行为好 3 倍以上，但它**不是文件系统配额**：上界 = `du` 间隔 + 反应时间（约 0.2 s 的
写入）+ 一次在途 `write()`。租户能 `kill` 掉同 uid 的执行器，剩下的那层是平台看门狗。

### 边界（本批没有做的）

- token 仍是 1 小时可携带的 bearer 凭证（不绑定沙箱/容器/地址）；绑定的设计写在
  `docs/EXECUTION-BOUNDARY.md` §16.3，没有实现。**（下一批已实现并实测：见文末「本批：
  token 只在自己的沙箱里能用」；这条保留在这里，因为它当时是真的。）**
- 执行器被租户杀掉之后不自动重启（下次容器启动会重跑 hook）；没有"解冻"按钮，恢复路径
  就是暂停/恢复。
- 没有在 K8s/gVisor 下验证这套机制（本批只有 compose + runner 形态）。
### 门禁（本批，实测输出）

```
go build ./...                                   ok
go vet ./...                                     ok
go test ./... -count=1                           42 个包全 ok（httpapi 38.6s，gitx 21.6s）
pnpm --dir web test -- --run                     38 files / 292 tests passed (29.9s)
pnpm --dir web exec next build                   ok（Next.js 16.4.3 / Turbopack）
pnpm --dir e2e test                              182 passed (8.9m)
pwsh scripts/deliver-check.ps1                   119/119 checks passed（98.0s，镜像 piforge:local
                                                 sha256:f98f52f9…，artifacts: e2e/.artifacts/delivery-drill/）
```

栈：`pwsh scripts/up-isolated.ps1` 起在 7723（`-Rebuild` 重打过三个镜像），`-Down` 收掉；
`pf-local`（7722）全程未动：同一个容器 id `7dc14e4a7525a8…`、`/healthz -> 200`。

一次说明：deliver-check 的第一次跑在提交之前，唯一的失败是它自己的守卫
「the source checkout is clean after the drill」（118/119）——工作树里是本批未提交的改动，
不是产品缺陷；提交后重跑是 119/119。

## 本批：token 只在自己的沙箱里能用（第四轮，2026-11-01）

第三轮留下的那条（token 是 1 小时可携带的 bearer 凭证）关掉了。验收当时就是**从宿主**把
`/gw/llm` 用成功的，这一批之后同样一条命令从宿主拿到的是 `403 sandbox_address_mismatch`。
不改任何 FR 状态：**126 条 FR 仍为 DONE 68 / PARTIAL 42 / MISSING 15 / 不做 1**；**整表 135
行 = DONE 77 / PARTIAL 42 / MISSING 15 / 不做 1**。

**选型**：同时做 (b) 源地址绑定与 (a) per-sandbox 证明；(c) mTLS 明确不做（要 CA、per-sandbox
证书签发与轮换、第二个监听、SDK 信任，这一轮做不完）。两半各管一半，单独一半会漏的东西写在
`docs/EXECUTION-BOUNDARY.md` §16.3 的表里：地址这一半**复制不走**，所以复制出容器的 token 在
别处不可用；证明这一半是给「网络视图错了」准备的（两条沙箱源地址看起来一样时，秘密仍不同）。

**机制**（代码位置）：`llmgw.Claims.b` = 沙箱 id，`Signer.IssueBound` 写进签名载荷
（`internal/llmgw/token.go`）；地址 = 用本进程解析器查容器名（隔离部署里是 Docker 内建 DNS，
控制面在每条租户桥上）并与 `r.RemoteAddr` 比，**不看** `X-Forwarded-For`，缓存 2 s、不一致时
强制重新解析一次再拒绝、解析失败退回上次已知地址、从来解析不到就拒绝（`llmgw/binding.go`）；
证明 = `HMAC-SHA256(per-sandbox secret, token)`，秘密 32 字节随机、封在
`sandboxes.proof_cipher`（`store/sandbox_proof.go`，惰性生成、幂等、活过重启），**从不进入
容器**，过界的只有 HMAC，且它在 **agent 进程的环境变量** `PIFORGE_SANDBOX_PROOF` 里；
`models.json` 写的是 `"X-PF-Sandbox-Proof": "$PIFORGE_SANDBOX_PROOF"`（Pi 的 headers 支持
`$NAME` 环境插值），所以攻击者复制的那个文件不再包含一切。顺序：签名 401 → 撤销 401 → 地址
403 → 证明 403 → 取 provider，每次拒绝一行 WARN（session / sandbox / 源地址 / 原因）。MCP
broker token 同样绑（只有地址这一半，Pi 的 `mcp.json` 没有地方为每个请求插值），拒绝是 broker
惯用的 401 + 日志原因。

**实测**（`scripts/probe-sandbox-binding.ps1`，7723 隔离栈，产物
`e2e/.artifacts/sandbox-binding/20261101-112610`，failures=0）：

```
PASS  the first prompt settles with a model answer            (answer=READY)
PASS  token A carries the sandbox it may be spent from        (claim=piforge-b4e0a17c5c0fbc35)
PASS  models.json names the proof as a reference, not the value
PASS  GET <baseUrl>/models from INSIDE the sandbox -> 200
PASS  the same call without the proof is refused              (403 sandbox_proof_missing)
PASS  the same token from the HOST is refused, even with the proof copied along
      (403 sandbox_address_mismatch: addresses 172.27.0.4, arrived from 172.25.0.1)
PASS  token A from a SIBLING sandbox of the same tenant       (403 … arrived from 172.27.0.5)
PASS  token A from ANOTHER TENANT's sandbox                   (403 … arrived from 172.28.0.4)
PASS  token A still works from its sandbox after the restart  (200, same token, no re-mint)
PASS  the session's agent still works after the restart       (accepted=True token_unchanged=True)
PASS  token A refused after pause / after the restart         (401 / 401, on_record=4)
PASS  token B (g=1) works from the resumed sandbox            (200)
PASS  the broker token: 200 from its sandbox, 401 from the host and from a sibling
RESULT in_sandbox=200 host_with_proof=403 host_without_proof=403 sibling=403 other_tenant=403
       same_token_after_restart=200 revoked_after_pause=401 revoked_after_restart=401
       resumed_token=200 mcp_in_sandbox=200 mcp_from_host=401 on_record=4
```

同批改动与回归：`scripts/probe-gateway-revocation.ps1` 的两条断言改到沙箱里跑（「token 从宿主
能用」现在是 403；`RESULT … host_before_pause=403 … resumed_token=200`，failures=0），两个探针
都先删自己上一轮的 `binding-*` / `revocation-*` 项目，所以同一个库上可以反复跑；新增
`scripts/drill-key.ps1`：只经它读 `PIFORGE_DRILL_KEY`，任何输出只有 `len=36 sha256=xxxxxxxx…`
这种指纹。

**仍然不成立**（详见 §16.3）：沙箱里的 agent 自己当然能用（这一轮界定的是「在哪儿」，不是
「谁」）；`-sandbox local` 不绑定（启动日志 WARN）；地址这一半信任这套部署的网络形状，mTLS
仍是补这一点的下一步；`proof_cipher` 是新的状态，用丢了 `secret.key` 的备份恢复会让在跑的
agent 拿到 `403 sandbox_proof_unavailable`，下一次 prompt 重新签发即自愈（fail closed）。

### 门禁（本批，实测输出）

```
go build ./...                                   ok
go vet ./...                                     ok
go test ./... -count=1                           43 个包全 ok（httpapi 41.1s，gitx 23.2s）
pnpm --dir web test -- --run                     38 files / 292 tests passed (26.4s)
pnpm --dir web exec next build                   ok（Next.js 16.4.3 / Turbopack）
pnpm --dir e2e test                              182 passed
pwsh scripts/deliver-check.ps1                   119/119
```

栈：`pwsh scripts/up-isolated.ps1 -Rebuild` 起在 7723，`-Down` 收掉；`pf-local`（7722）全程
未动：同一个容器 id `7dc14e4a7525a8…`、`/healthz -> 200`。

## 本批：租户树上的读取（FIX 1）+ 磁盘预算的三层（FIX 2/3）

第三轮对抗验收的结论是"这台机器今天不能托管不可信的租户代码"，原因是一条路径：`POST /v1/skills/import`
克隆租户给的仓库，`findSkills` 用 `os.ReadFile` 读每个 `SKILL.md` 同目录的文件——`os.ReadFile` 跟符号链接。
`leakskill/key.txt -> /proc/1/environ` 于是把控制面自己的环境（真实的 36 字符 provider key、
`PIFORGE_MASTER_KEY`）当成 skill bundle 还给了租户，`/data/master.key` 和别人的 `models.json` 是同一个原语。

### 本轮修复

1. **FIX 1（租户可控树的读取边界）**：新增 `internal/tenantfs`（`Tree.ReadFile/Walk/WalkFiles`，
   逐分量 `Lstat` + `SameFile` + `EvalSymlinks`/`Rel`，类型化 `*tenantfs.Refusal` 点名路径）。
   `findSkills` 与 bundle 读回改走它；git 侧 `-c core.symlinks=false` 且索引里出现 `120000` 就拒绝整次
   导入；zip 的绝对路径/`..`/链接条目本来就是拒绝。整次拒绝，不写行、不写 bundle。
2. **审计了其它每一处"控制面读租户路径"**（表在 `docs/SANDBOX-DEPLOY.md` §1.8）：工作区预览/下载已有
   `fsx.Resolve`，是对的；搜索回退、`.gitignore`、`git status` 的未跟踪行数、agent 日志四处**修了**；
   挂载/快照/digest 本来就拒绝链接；artifacts、MCP、repo_url、导出、快照 listFiles 逐个确认过。
3. **FIX 2（磁盘预算）**：测量 = workspace + 容器可写层（`du -sbx /` 减开机基线
   `/run/piforge/disk-baseline`）。enforcer 仍以 gem 运行（必须，才能 signal 租户进程），但外面套一个
   root 所有的监督循环（gem 不能 signal 另一个 uid），平台磁盘看门狗在每次测量里用 `pgrep` 补一次。
4. **FIX 3（pids / 检测）**：`deploy/tenant-limits.json` 的 pids 256 → 512（镜像自身占 216/256，40 个
   fork 就能顶死）；liveness watchdog 探测间隔 10 s → 5 s，失败后 1.5 s 确认一次
   （`sandboxConfirmDelay`），健康容器仍然不会因一次错过被重启。

### 实测（隔离栈 7723，镜像从本轮 HEAD 重建）

```
attack1  POST /v1/skills/import （leakskill/key.txt -> /proc/1/environ） -> 400，点名 leakskill/key.txt
attack2  同仓库改指 /data/master.key                                   -> 400
rows     before/after 都是 [code-review, explain-codebase, test-gap]：没有 leakskill 行，没有 bundle 目录
clone    naive=符号链接；confined=普通文件，内容 "/proc/1/environ"；PIFORGE_MASTER_KEY 出现 0 次
legit    两文件仓库 -> 200；bundle = SKILL.md + scripts/split.py
scan     36 字符 drill key 在 1016 字符的响应里出现 0 次
disk     /tmp 写 1.5 GiB、workspace 129 306 B -> admin bytes=1 539 211 060；再写 1.5 GiB -> over=1
enforcer root 监督循环 275 / su 277 / gem 278；gem `pkill -9` 杀不掉 275/277（Operation not permitted），
         gem enforcer 被杀后 3 s 内以新 pid 回来；gem `kill -9 275` -> Operation not permitted
pids     PidsLimit=512，cgroup 512 / current 191；watchdog log interval=5s
storm    441 个 sleep 顶死 512 的 cgroup：first miss 05:01:00.810 -> repair 05:01:02.374（1.56 s，确认）
         -> "the sandbox answers again" 05:01:20.122（elapsed 17.7 s，端到端 19.3 s）
```

### 门禁

- `go build ./...`、`go vet ./...`、`go test ./... -count=1`
- `pnpm --dir web test -- --run`（38 files / 292 tests）、`pnpm --dir web exec next build`
- e2e 全量、`pwsh scripts/deliver-check.ps1`（`PIFORGE_DRILL_KEY` 经 `scripts/drill-key.ps1` 读取，不打印值）
- 新增 Go 测试：`internal/tenantfs` 10 个、`internal/httpapi` 8 个（见 `docs/SANDBOX-DEPLOY.md` §1.8 末）

### 仍未做

`file://` 导入源没有白名单（只保证带链接的仓库被拒）；clone 的落盘没有上限（4 MB 是**读**的上限）；
硬链接不在 `tenantfs` 检测范围（git/zip 造不出，未来的 tar 导入要重新想）；"根文件系统减基线"包含沙箱
自身服务写的东西，空闲沙箱会缓慢增长。