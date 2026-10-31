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

---
# PiForge 对照 PRD 的实现审计

- 审计对象：`C:\Users\<user>\Downloads\piforge`，HEAD `be01575`（"fix(browser): serialise launches per sandbox, and recover a dead profile lock"），工作树干净（本文件除外）。
- 审计方法：先读 `docs/STATUS-AUDIT.md`（`06b61a3`）的每条判定，再逐条重读 `backend/internal/httpapi/router.go` 的路由表、各 handler、`web/components/**` 与 `e2e/tests/*.spec.ts`；以「实现 + 断言了用户可见结果的测试名」为准，不采信命名与注释。
- 状态口径：**DONE** 必须点名一条断言用户可见承诺的测试或 e2e 路径；**PARTIAL** 必须写清缺什么；**MISSING** 是无实现。
- 本版把 `06b61a3` 之后的修复接进台账：`f766651`（FR-2.5 / FR-2.6）、`bd85ade`（FR-2.7）、`9c4ab0b`（FR-11.8）、`717f7cb`（FR-12.5），以及浏览器启动按沙箱单飞与 profile 僵尸锁恢复（FR-11.1）。本批补上 FR-12.6（一键开 PR）并把 FR-8.8 从 PARTIAL 移到 DONE——开 PR 是那一条里唯一缺的能力。
- 计数：**126 条 FR，DONE 67 / PARTIAL 43 / MISSING 15 / 不做 1**（`be01575` 时是 52 / 54；FR-1.9 与 FR-9.2 补证据后为 54 / 52 / 19，FR-3.3 从 MISSING 移入 DONE，FR-14.4、FR-6.6、FR-10.2、FR-11.2、FR-12.4、FR-14.1、FR-12.1 与 FR-12.2 从 PARTIAL 移入 DONE，FR-16.3 从 MISSING 移入 DONE，FR-12.3 从 MISSING 移入 DONE，FR-12.6 从 MISSING 移入 DONE，FR-8.8 从 PARTIAL 移入 DONE；旧值 37 / 50 / 39 是 `2e5f206` 时代的，且 FR-12.5 等行早已过期）。另有 5 条非 FR 交付能力（见文末「交付能力」），均 DONE；**整表 131 行 = DONE 72 / PARTIAL 43 / MISSING 15 / 不做 1**（本批新增「会话在 provider 变更后自动恢复」一条，126 条 FR 的状态未变）。这两个数是从本文件的表格行重新数出来的，不是从旧表头抄的；上一版表头的 64 / 44 / 17 与上一批自己记的 65 / 44 / 16 已经不一致，本批按实际行重数并改正。

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
| FR-5.8 资源护栏（并发/CPU/内存/磁盘） | PARTIAL（容器 provider 有限制参数，本地 provider 没有） | `-sandbox docker -sandbox-cpus/-sandbox-memory`；`docs/SANDBOX.md`。本地 provider 仍无任何限制 |
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
| FR-10.1 完整 PTY + xterm（支持 TUI） | DONE | `term/manager.go`（真 PTY，`pty_unix.go`/`pty_windows.go`）+ `terminal-surface.tsx`（`@xterm/xterm`）；`03-workspace.spec.ts` "the terminal opens a real PTY in the sandbox"、"keystrokes reach the shell and output comes back" |
| FR-10.2 多 tab（≥5，重命名/关闭） | DONE | `terminal-tabs.tsx`：可新增/切换/关闭（≥5，上限 8），每个 tab 一个 `TerminalPane`，`tab` id 就是服务端的 PTY key（`data-pty-tab`）；切走的 tab 只隐藏不卸载，shell 与滚动缓冲都留着。关闭时先发 `{type:"close"}` 让服务端 `Term.Close`（掉 socket 只是重连语义，见 FR-10.3），再移除标签；双击标签改名只改 `label`，id 不变，所以改名不会换 shell；关掉最后一个会新开一个。`workspace-panels.tsx` 的「在终端中打开」跟随当前标签（`paneRef` 指向 active）。测试：`e2e/tests/71-terminal-tabs.spec.ts`（加到 5 个 tab、5 个 pane 的 `data-pty-tab` 各不相同且 `data-pty-status` 全为 `open`；在第 5 个 tab 键入 `echo five > tab-five.txt`、第 1 个键入 `echo one > tab-one.txt`，两个文件都能从 Files API 读到；改名「构建」后 `t1` 仍 `open`；关闭 `t1` 后剩 4 个 tab/pane，文件仍在）。**实现坑**：`nextNumber.current++` 被 React Compiler 编译成前置自增，第一个 tab 的编号从 3 开始，改成显式 `const n = ref.current; ref.current = n + 1` |
| FR-10.3 断线重连（offset + 60s 不丢） | DONE | `pty_handlers.go` `from_offset` + `pumpPTYToWS`；`term/ring.go` 2MB ring；`manager_test.go` `TestReadOffsetsAreStable`、`ring_test.go` 5 例 |
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
| FR-12.3 行内评论 → 作为 steer 发给 agent | DONE | 在 diff 的某一行（以及文件预览的某一行）上写评论：`patch-view.tsx` 每行一个 `diff-line-comment`（行号来自 `@@` 头，见 `lib/git/patch-lines.ts`），`code-preview.tsx` 每行一个 `file-line-comment`。评论先存成草稿（`lib/review/comments.ts`：localStorage、按会话分区、键 `piforge.review.drafts.v1`，重载后仍在），diff 里有标记与计数（`review-comment-row` / `diff-line-marker` / `diff-comment-count`）；发送走**同一个** `POST /v1/sessions/{id}/messages`，带 `review_comment{path,line,side,hunk,quote,body}`，run 进行中时 `streaming_behavior:"steer"`。**消息正文由服务端拼**（`internal/httpapi/review_comment.go`）：`行内评论 · <path>:<line>` + diff 片段 + 逐行引用（` 42 \| 代码`）+ `评论：…` + 位置说明；服务端拿工作区里**当前**的文件核对引用，所以「行挪了」有明确语义（见文末「FR-12.3 的设计与证据」）。测试：`review_comment_test.go`（引用/路径行号/挪行/删除行/读不到文件/校验，以及两条走真实路由断言 agent 收到的 prompt 含 path:line 与引用）、`lib/review/comments.test.ts` 9 例、`lib/git/patch-lines.test.ts` 8 例、e2e `77-inline-comment.spec.ts`（行上评论 → 保存 → 标记/计数 → 重载草稿仍在 → 发送 → 草稿清除 → transcript 的 `turn-user` 含 `行内评论 · f.txt:3`、被引用的代码与评论正文）。**渲染器换了**：`@git-diff-view/react` 在这个构建里 `DiffUnifiedView` 抛错、错误边界一直回落到原始 patch 文本（一行都点不了），所以 diff 现在是自己的逐行渲染器，保留原 testid（`git-diff-view`/`git-diff-patch`）与 `+`/`-` 标记；split 用同 hunk 内顺序配对 |
| FR-12.4 变更概览（增删行数 + 风险标记） | DONE | `GitStatus` 新增 `additions`/`deletions`，每个 `GitChange` 新增 `additions`/`deletions`/`risk`（`lockfile` / `ci`）。统计相对 HEAD 合并已暂存与未暂存：`git diff --numstat -z HEAD`（无 HEAD 时分别数 index 与工作区），重命名取新路径，未跟踪文件按行数计入（二进制/超过 4MB 记 0）。风险在 `gitx` 判定：`.github/workflows/`、`.circleci/`、`.buildkite/` 与常见 CI 文件为 `ci`，`go.sum`、`package-lock.json`、`pnpm-lock.yaml` 等为 `lockfile`。`git status` 改用 `--untracked-files=all`：用 `normal` 时新 `.github/workflows/ci.yml` 只显示成 `.github/` 一行，行数与风险都挂不到文件上。UI 每行显示 `+N/−N`（`git-line-counts`）与「锁文件」/「CI」徽章（`git-risk`），表头 `git-change-totals`。测试：`TestStatusCountsLinesAndFlagsRisk`（a.txt +1、b.txt −2、new.txt +2、package-lock +2/锁文件、ci.yml +1/CI，合计 +6 −2）与未跟踪分支计数；e2e `73-git-overview.spec.ts` 先断言 API 的 `+6 −1` 与逐行字段，再断言页面上的同样数字与徽章。旧代码：后端没有这些字段，spec 失败在 `expect(status.additions).toBe(6)`；只换回旧前端则失败在 `getByTestId('git-change-totals')` element not found（`fr-12.4-fail-first.log`、`fr-12.4-fail-first-ui.log`）。部署截图 `fr-12.4-change-overview.png` |
| FR-12.5 测试结果面板 | DONE | `717f7cb`：`internal/testrun` 解析 go test / vitest / jest / pytest 输出（含 `race detected`、`file:line`），`GET /v1/sessions/{id}/test-runs` 从工具事件与终端两条来源入库，`test-panel.tsx` 有失败/通过徽章与逐条失败展开。测试：`testrun/parse_test.go`、`store/testruns_test.go`、`httpapi/test_run_handlers_test.go`；`e2e/tests/62-test-panel.spec.ts` 断言空态、失败徽章与 `TestCacheEvict` / `cache_test.go:42`、随后同一会话变成通过徽章、刷新后仍有两条且 API 与面板一致 |
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
| FR-16.5 运行状态（在线用户/活跃沙箱/节点水位/镜像告警） | PARTIAL | `handleAdminStatus` 返回 users/sessions/sandboxes/providers/skills 计数（`admin_status_test.go`、`15-quick-open-status.spec.ts`）；无节点水位、无镜像拉取失败告警 |
| FR-16.6 资源护栏配置 | MISSING | 无配置端点/页面（与 FR-5.8 同源缺口） |
| FR-16.7 功能开关（按用户灰度） | DONE | `feature_handlers.go` + `feature_flags` 表 + 服务端强制（`featureDenied`）；`17-feature-flags.spec.ts`、`feature_test.go` |
| FR-16.8 支持模式（只读 + 通知留痕） | DONE | `support_handlers.go` + `support_access` 表 + `support-notices`；`16-support-mode.spec.ts`、`support_test.go` 2 例 |

## 交付能力（非 FR，单独计）

PRD 没有这两条的 FR 行，但它们是演练暴露、已修复、且有用户可见证据的交付能力。放在这里而不是塞进 FR 表，是为了不把「产品需求覆盖」和「交付流程」混成同一个数字。

| 能力 | 状态 | 证据 |
|---|---|---|
| 首次运行 setup：`GET /v1/setup` + 首次运行面板 + `PIFORGE_BOOTSTRAP_PROVIDER` 预填（`ba532a2`） | DONE | `httpapi/setup_handlers.go` 的 `GET /v1/setup` 返回 `needs_provider` / `is_admin` / `shared_provider_available` / 非密 `bootstrap`（name/base_url/model，没有 key 字段）；`web/components/setup/first-run-setup.tsx` 在首页与会话输入框上方显示「先配置一个模型服务」，按角色提供「添加共享服务商」或设置页链接；`bootstrap.Ensure` 只在「没有可用共享服务商」时创建，重启不覆盖；启动环境持有 key，日志不打印。**本批补上默认服务商守卫（验收 pass 3，MEDIUM）**：`PIFORGE_DEFAULT_PROVIDER=volc` 指向不存在的 provider 时，启动日志有一条 WARN（`cmd/piforged/default_provider.go`，非致命，写明后果是「跟随默认的会话会 400 no_model」），`GET /v1/setup` 返回 `default_provider` 与 `default_provider_missing`，面板据此显示「默认服务商「volc」不存在，请添加或更改默认值」。测试：`setup_test.go` `TestSetupReportsAMissingDefaultProvider`（缺失→true；加回→false；停用→true）、`default_provider_test.go`；旧代码跑该测试失败在 `default_provider = ""`（`e2e/.artifacts/fix-provider-lifecycle/fail-first.log`） |
| 会话在 provider 变更后自动恢复（验收 pass 3，HIGH；PRD 没有单独 FR 行，属 FR-13.6 的生命周期面） | DONE | 旧行为：会话的 `model_ref` 指向已删除的 provider 时，每次发送都是 500（server log `pi: command prompt failed: No API key found for the selected model`），UI 只说「服务端内部错误」，重试无用，只能新建会话；并且一个在 provider 缺失期间启动的 agent 进程会带着空的模型配置活到进程结束——models.json 与网关 token 是每次 spawn 生成的，改 provider 到不了它。现在：① `session_models.go` 在发送前重解析失效引用，改用部署默认/第一个可用 provider 并写回 session；② `pi.Runtime.StartedModelConfig` 让控制面识别「启动时没有模型配置」的活进程，解析出可用模型后先 `Stop` 再 spawn；③ `RuntimeStatus.resolved_model_ref` / `model_unavailable` 让 header 显示真正会用的模型或「未配置」，`modelRefOf` 把 Pi 的 `unknown` 占位符归一成空，`跟随默认（unknown）` 不可能再出现。测试：`session_model_lifecycle_test.go` 四例（失效引用→换到默认并持久化、无可用 provider→带 provider 名的 400、无配置进程→重建后再 prompt、status 字段）、`pi/runtime_test.go` 两例（`unknown` 归一、`StartedModelConfig` 跟随 spawn env）、e2e `81-provider-lifecycle.spec.ts` 两例（删 provider 后发送成功并弹出「已自动切换模型」+ header/会话行都更新；无 fallback 时 400 文案含 provider 与「模型与凭据」）。旧后端实测：同一会话 `400 no_model`、session 行仍是死引用（`e2e/.artifacts/fix-provider-lifecycle/lifecycle-api-OLD.json`） |
| 惰性浏览器面板 + 空闲停止（host placement，`15b995c`） | DONE | `workspace-panels.tsx` 只在浏览器面板可见时挂载它；`browser.Manager` 的 idle reaper 在无 viewer 附着 `PIFORGE_BROWSER_IDLE_TIMEOUT` 后 stop 该 sandbox 浏览器；`GET /v1/admin/status` 暴露 `browser_running` / `browser_idle_timeout`。测试：`browser/idle_test.go` 4 例、`67-browser-lazy.spec.ts` 断言「打开会话不启动、选中面板才启动、切走后停止」；部署实测选中前 `docker exec pf-local pgrep chrome` 为空，选中后才出现 |
| 项目删除：先停进程、重试销毁，失败则拒绝并保留项目；清扫历史孤儿目录（本次修复） | DONE | `handleDeleteProject` 的顺序改为「停进程 → 重试销毁 → 删行」：`stopProjectProcesses` 先关该沙箱的浏览器、`Runtime.Stop` 每个会话的 agent、`Term.CloseSandbox` 每个 worktree 的终端（`TermManager` 新增该方法），再 `destroySandbox` 以 150ms 递增退避重试 6 次；仍失败返回 409 并**保留项目**（文案「沙箱目录删不掉，项目已保留…」），不再像旧代码那样记一条 warn 后照删项目行。历史遗留由 `internal/janitor` 处理：启动即扫一轮、之后每 `-sandbox-sweep`（默认 10m，0 关闭，仅本地 provider）扫一次，删除 `sandboxes/<user>/<project>` 中没有 projects 行的目录及其 `_state/<user>/<project>.json`，只认 `usr_*`/`prj_*` 形状的目录名。测试：`project_delete_test.go`（注入 destroy 失败 → 409 + 项目仍在 + 至少重试 2 次；释放后重试 → 行与目录都消失；终端先关后销毁的顺序）、`janitor_test.go` 5 例、`61-project-lifecycle.spec.ts`（UI 删除后**真实路径**消失；用 PowerShell `FileShare.None` 真锁住 checkout → UI 报「沙箱目录删不掉 / 项目已保留」且项目仍在，释放后重试两者都消失）、`e2e/scripts/fix-orphans-repro.ps1`（Windows 复现/验证脚本）。旧代码实测：删除返回 200、项目行消失、`sandboxes/<user>/<project>` 留盘，日志 `delete project: sandbox destroy failed ... unlinkat ...\repo\held-by-a-process.txt: The process cannot access the file...`（`e2e/.artifacts/fix-orphans/fail-first.log`） |
| ⌘K 命令面板的默认选中 / Enter（PRD §10.1-5、§13「全部交互可键盘到达」；PRD 没有对应 FR 行） | DONE | 旧实现把选择交给 cmdk 1.1.1，它按行的 `value` 字符串认选中行。会话行的 label 在 projects 查询返回后才从「项目」变成真实项目名，`value` 随之变化，存储的选中值不再匹配任何行：`[cmdk-item][aria-selected="true"]` 实测 0，而 cmdk 的 Enter 只会向选中行派发，于是「输入 → 回车」什么都不发生。现在面板自己管理选择：行有稳定 id（`session:<id>` 等），active 行由可见列表**推导**（当前 id 不在了就选第一行），`aria-selected`、`aria-activedescendant`、`role=listbox/option`、`aria-controls/expanded` 同源；↑↓/Home/End/Enter 由面板处理。列表仍在加载时按 Enter **忽略而不是排队**：结果随时可能变化，排队的 Enter 会执行用户没看见的行；面板保持打开，行出现后下一次 Enter 生效。测试：`web/components/shell/command-palette.test.tsx` 4 例（异步落定后第一行默认选中且 Enter 进入会话、label 变化不丢选择、加载中 Enter 无效、↑↓ 仍可用）、`e2e/tests/60-session-search.spec.ts` 的「Ctrl+K → 输入 → Enter 直接打开」（旧代码在保留新 spec、去掉 ArrowDown 后必失败）与「列表仍在加载时 Enter 被忽略，随后生效」；部署实例探针 `e2e/scripts/fix-palette-shadow-probe.mjs` 旧构建 `selected=0`、URL 未变。 |

计数：上面 126 条 FR 是 **DONE 67 / PARTIAL 43 / MISSING 15 / 不做 1**（本批按表格行逐行重数；旧段里的 62 / 46 / 17 已与表格不一致）；加上这五条（均 DONE）后整表 131 行 = **DONE 72 / PARTIAL 43 / MISSING 15 / 不做 1**。本批 FR-11.3 由 PARTIAL 转 DONE（租约 + 拒绝 + UI + 测试），FR-14.2 仍是 PARTIAL（Windows 是只读副本；Linux 无 mount 能力时是冻结但可写的 `copy`），所以现在是 **DONE 73 / PARTIAL 42 / MISSING 15 / 不做 1 = 131**。本批（FR-14.2 的快照回收 + 无 SYS_ADMIN 的只读）不改变任何 FR 状态：FR-14.2 仍是 PARTIAL（Windows 仍没有目录级不可变，且 copy-ro 不阻止在目录里新建文件），所以 126 条 FR 与 131 行整表的计数都不变。

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
