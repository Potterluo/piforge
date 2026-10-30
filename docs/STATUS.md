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

---
# PiForge 对照 PRD 的实现审计

- 审计对象：`C:\Users\<user>\Downloads\piforge`，HEAD `be01575`（"fix(browser): serialise launches per sandbox, and recover a dead profile lock"），工作树干净（本文件除外）。
- 审计方法：先读 `docs/STATUS-AUDIT.md`（`06b61a3`）的每条判定，再逐条重读 `backend/internal/httpapi/router.go` 的路由表、各 handler、`web/components/**` 与 `e2e/tests/*.spec.ts`；以「实现 + 断言了用户可见结果的测试名」为准，不采信命名与注释。
- 状态口径：**DONE** 必须点名一条断言用户可见承诺的测试或 e2e 路径；**PARTIAL** 必须写清缺什么；**MISSING** 是无实现。
- 本版把 `06b61a3` 之后的修复接进台账：`f766651`（FR-2.5 / FR-2.6）、`bd85ade`（FR-2.7）、`9c4ab0b`（FR-11.8）、`717f7cb`（FR-12.5），以及浏览器启动按沙箱单飞与 profile 僵尸锁恢复（FR-11.1）。
- 计数：**126 条 FR，DONE 62 / PARTIAL 46 / MISSING 17 / 不做 1**（`be01575` 时是 52 / 54；FR-1.9 与 FR-9.2 补证据后为 54 / 52 / 19，FR-3.3 从 MISSING 移入 DONE，FR-14.4、FR-6.6、FR-10.2、FR-11.2、FR-12.4 与 FR-14.1 从 PARTIAL 移入 DONE，FR-16.3 从 MISSING 移入 DONE；旧值 37 / 50 / 39 是 `2e5f206` 时代的，且 FR-12.5 等行早已过期）。另有 4 条非 FR 交付能力（见文末「交付能力」），均 DONE；**整表 130 行 = DONE 66 / PARTIAL 46 / MISSING 17 / 不做 1**。这两个数是从本文件的表格行重新数出来的，不是从旧表头抄的。

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
| FR-2.5 对比视图（2–3 分支并排） | DONE | `GET /v1/sessions/compare?ids=` + `compare-page.tsx` + `/p/{project}/compare`；`f766651` 让「改动规模」按「分支 vs 基准」同时算已提交与未提交，clone 的继承轮次/token 也计入。`compare_test.go` `TestCompareCountsCommittedAndUncommittedWork`、`TestCompareCountsInheritedUsage`；`e2e/tests/29-compare.spec.ts` 断言真实数字（已提交 1 个文件 / +2 行、未提交 1 个 / +1 行、合计 2 个 / +3 −0、未动分支为 0、`含继承 1`），并把 `2 个文件` / `+3 / -0` 读回页面。缺 checkout 时报 `diff_error` 而非 0 |
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
| FR-6.1 虚拟化文件树（展开/折叠/搜索/新建/改名/删除） | PARTIAL | `file-tree.tsx` + `file-tree-canvas.tsx`（`react-arborist` `Tree`，懒加载 `files/tree?depth=`，客户端按名过滤，下载/删除）；**无新建文件、无重命名**；测试：`03-workspace.spec.ts`、`smoke.test.tsx` |
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
| FR-8.8 fetch/pull/push + 开 PR | PARTIAL（同步有，PR 没有） | fetch/pull/push：`gitx/remote.go` + `GET /git/remote`；**开 PR 未实现**（没有托管平台集成） |
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
| FR-11.3 接管模式（输入 + 暂停 agent 浏览器工具） | PARTIAL | 输入注入可用（`Session.TabDispatch` → `Input.dispatch*`，`browser_test.go`）；没有「接管时拦截 `mcp__playwright__*`」的浏览器锁 |
| FR-11.4 模式互斥 + 「谁在控制」标识 + 确认 | DONE | `GET/POST …/browser/mode`；进入 takeover 前弹确认（说明会中断 agent），确认后中断当前运行；面板按钮显示「观察 / 控制中」；`browser_mode_test.go`、`43-browser-mode.spec.ts` |
| FR-11.5 地址栏 + 前进/后退/刷新 + 端口快捷入口 | PARTIAL | `browser-panel.tsx` 有 URL `Input` 与刷新/导航；无前进/后退，无端口列表下拉 |
| FR-11.6 agent 侧通过 Playwright MCP 驱动同一 chromium | MISSING | 无 Playwright MCP 预设或 CDP 端点下发（`materialize.mcpEntry` 只写用户自己填的 command/url） |
| FR-11.7 多 tab（≥3，tab 条 + 缩略图） | DONE | `browser/tabs.go` + `handleListBrowserTabs`/`handleOpenBrowserTab`/`handleCloseBrowserTab` + `browser-panel.tsx` tab 条；`26-browser-tabs.spec.ts`、`tabs_test.go` |
| FR-11.8 截图入会话附件（不再弄脏 checkout） | DONE | `9c4ab0b`：截图跟随面板当前激活的 tab（`Session.SetActiveTab` / `ScreenshotTab`），空图被拒并给出原因（`capture.go` `ErrBlankCapture`）。**本轮把落点移出仓库**：截图写 `<dataDir>/artifacts/<session>/screenshots/<ts>-<ms>.png`，响应虚拟路径 `artifacts/<session>/screenshots/…`，面板经新路由 `GET /v1/sessions/{id}/artifacts?path=…` 预览/下载；`git status` 不再出现 `screenshots/`，而旧会话里已在 checkout 内的 `screenshots/…` 仍走 `/files` 可预览（不迁移、不删除用户文件）。测试：`screenshot_test.go`（about:blank、纯色图、非 PNG、连续两次）、`real_chrome_test.go` `TestRealChromeScreenshotFollowsTheActiveTab`、`httpapi/artifacts_test.go` `TestScreenshotWritesOutsideTheCheckoutAndIsServable`（POST 后 checkout 零新增文件、artifacts 路由 200、legacy workspace 路径仍 200）；`e2e/tests/20-screenshot.spec.ts` 先导航到真实页面，再解码 PNG 断言 >8 种颜色、两次 API 截图后 `git/status.clean=true`、面板相机按钮预览 `src` 为 `/artifacts?...&inline=1`、空白 tab 返回 422 且不落文件 |
| FR-11.9 profile 持久化（sandbox 内 .piforge-browser） | DONE | `browser.go` `profile := filepath.Join(profileRoot, ".piforge-browser")` + `--user-data-dir`；`profile_test.go` `TestRealChromeProfileIsStableAcrossSessions`（边界已注明） |
| FR-12.1 Diff 作用域（未暂存/已暂存/某提交/分支 vs 基准/本轮） | PARTIAL | `parseDiffScope` 支持 unstaged/staged/branch/commit；前端 `git-panel.tsx` 的 `SCOPES` 只有 3 项（**无「某次提交」**），且无「本轮」作用域 |
| FR-12.2 逐文件 + 逐 hunk stage/unstage/revert | PARTIAL（逐 hunk 只有 API，UI 是逐文件） | `gitx/hunks.go`（客户端只传 index，补丁由服务端重建）；`POST …/git/hunk`、`POST …/git/discard`；变更面板每行「丢弃」（带确认）；`hunks_test.go`、`37-hunks.spec.ts`。**diff 视图里没有逐 hunk 按钮** |
| FR-12.3 行内评论 → 作为 steer 发给 agent | MISSING | diff 组件没有评论入口，也无 `streaming_behavior:"steer"` 的调用链 |
| FR-12.4 变更概览（增删行数 + 风险标记） | DONE | `GitStatus` 新增 `additions`/`deletions`，每个 `GitChange` 新增 `additions`/`deletions`/`risk`（`lockfile` / `ci`）。统计相对 HEAD 合并已暂存与未暂存：`git diff --numstat -z HEAD`（无 HEAD 时分别数 index 与工作区），重命名取新路径，未跟踪文件按行数计入（二进制/超过 4MB 记 0）。风险在 `gitx` 判定：`.github/workflows/`、`.circleci/`、`.buildkite/` 与常见 CI 文件为 `ci`，`go.sum`、`package-lock.json`、`pnpm-lock.yaml` 等为 `lockfile`。`git status` 改用 `--untracked-files=all`：用 `normal` 时新 `.github/workflows/ci.yml` 只显示成 `.github/` 一行，行数与风险都挂不到文件上。UI 每行显示 `+N/−N`（`git-line-counts`）与「锁文件」/「CI」徽章（`git-risk`），表头 `git-change-totals`。测试：`TestStatusCountsLinesAndFlagsRisk`（a.txt +1、b.txt −2、new.txt +2、package-lock +2/锁文件、ci.yml +1/CI，合计 +6 −2）与未跟踪分支计数；e2e `73-git-overview.spec.ts` 先断言 API 的 `+6 −1` 与逐行字段，再断言页面上的同样数字与徽章。旧代码：后端没有这些字段，spec 失败在 `expect(status.additions).toBe(6)`；只换回旧前端则失败在 `getByTestId('git-change-totals')` element not found（`fr-12.4-fail-first.log`、`fr-12.4-fail-first-ui.log`）。部署截图 `fr-12.4-change-overview.png` |
| FR-12.5 测试结果面板 | DONE | `717f7cb`：`internal/testrun` 解析 go test / vitest / jest / pytest 输出（含 `race detected`、`file:line`），`GET /v1/sessions/{id}/test-runs` 从工具事件与终端两条来源入库，`test-panel.tsx` 有失败/通过徽章与逐条失败展开。测试：`testrun/parse_test.go`、`store/testruns_test.go`、`httpapi/test_run_handlers_test.go`；`e2e/tests/62-test-panel.spec.ts` 断言空态、失败徽章与 `TestCacheEvict` / `cache_test.go:42`、随后同一会话变成通过徽章、刷新后仍有两条且 API 与面板一致 |
| FR-12.6 一键开 PR | MISSING | 无 `…/pr` 路由与 UI |
| FR-12.7 产物交付（present 为下载/预览卡） | PARTIAL | 预览区与 `binary-card.tsx` 提供下载/预览；没有「present 任意产物」的语义（截图现在写入 `<dataDir>/artifacts/<session>/screenshots` 并由 artifacts 路由预览，不再写入 worktree） |
| FR-13.1 admin 共享 provider + Inherited 徽章 | DONE | `handleAdminUpsertProvider`/`handleAdminListProviders` + `handleListProviders` 合并标 `inherited`；`12-shared-providers.spec.ts`、`admin_management_test.go` `TestAdminSharesAProviderThatUsersInherit` |
| FR-13.2 用户私有 provider + 同名 shadow | DONE | `handleCreateProvider`/`handleUpdateProvider` + 按名合并；`handlers_test.go` `TestProvidersMergeAndShadow` |
| FR-13.3 key AES-GCM 存库 + 沙箱内无明文 | DONE | `crypto.Box` 封存（`handleCreateProvider` 拒绝无 Box 的写入）+ `llmgw` 短期 token 注入环境（`piforged/main.go` `setupFor` 的 `ANTHROPIC_API_KEY=token`）；`TestProviderKeysAreSealedAtRest`、`gateway_test.go` `TestGatewayInjectsAnthropicKeyAndStripsTheEphemeralOne` |
| FR-13.4 模型选择器（按 provider 分组 + 窗口/推理/成本） | PARTIAL | `workbench.tsx` 构造扁平 `provider/model` 选项，`composer.tsx` 单层 `Select`；不显示 context window / reasoning / 成本（这些字段在 settings 页有） |
| FR-13.5 推理强度随模型能力动态显示 | PARTIAL | 前端写死 `THINKING_OPTIONS`（5 档），后端只有 `thinking_level` 列与 PATCH；`pi/runtime.go` 从不发 `set_thinking_level`，也没有 `get_available_thinking_levels`（只有 `pitest/serve.go` 的假实现） |
| FR-13.6 会话级模型覆盖 | DONE | `composer.tsx` `persistSetting({model_ref})` → `PATCH` + `handlePostMessage` 的 `ModelRef`；`10-models.spec.ts` "a custom model id typed in the composer is what the session uses" |
| FR-13.7 连通性测试 | DONE | `provider_probe.go` + `POST /v1/providers/{id}/test`；`provider_probe_test.go` 4 例、`06-providers.spec.ts` |
| FR-13.8 用量显示（token + 估算成本） | PARTIAL | `SessionStats.TokensIn/Out/CostUSD` + `ContextMeter`；无测试断言成本显示 |
| FR-13.9 用户 API Key（pf_…） | DONE | `api_key_handlers.go` + `api_keys.go` + Bearer 分支（`router.go` `authed`/`mutatingBearer`）；`24-api-keys.spec.ts`、`api_keys_test.go` |
| FR-14.1 两层作用域（admin 共享 + 用户私有，同名覆盖） | DONE | 两种作用域都能创建（`POST /v1/skills` 的 `scope`；非管理员建共享 403），共享行对每个用户可见且不可改。**本批补齐「同名覆盖」并把它定义成规则**（`backend/internal/skills/resolve.go`）：① 停用的行既不挂载也不遮蔽（停用 = 没配置）；② 同名时 `user` 作用域覆盖 `admin`；③ 同作用域多版本取最高版本（数字比较），再按创建时间、id 兜底；④ 名字 trim + 小写比较，因为挂载目录是 `materialize` 的小写 slug。结果：`provision.mountTooling` 只把生效行交给 `materialize.Mount`，停用私有行后共享行重新生效（回退，不是消失）；`product` 侧 `materialize.Mount` 也保留「同名目录 private 优先」的兜底，防调用方绕过解析器。API `GET /v1/skills` 给每行加 `effective`（每调用者计算，不落库）。会话记录：`session_skills` 由生效行生成，绝不因 SQL 排序/遍历顺序而记错（FR-14.5）。UI：设置页同名两行不再无法区分——生效行「生效中」，被遮蔽的共享行「已被同名私有 Skill 覆盖」并解释「停用会回退」，停用的私有行显示「使用共享版本 vX」；`/` 面板按名字只列生效行（一条 `/skill:name`），私有覆盖时标注「覆盖同名共享 Skill」。测试：`skills/resolve_test.go` 5 例（private 覆盖 shared 且与顺序无关、停用回退、两个用户各自解析、2.10.0 > 2.9.0、大小写同名）、`provision_test.go` `TestMountUsesThePrivateSkillAndFallsBackWhenDisabled`（真的读 `.pi/skills/review/SKILL.md` 与 `session_skills`，再停用私有行后读回共享内容）、`materialize_test.go` `TestMountPrefersThePrivateBundleWhenTwoScopesShareAName`（两种传入顺序）、`e2e/tests/75-skill-shadow.spec.ts`（fresh 用户建同名私有 → 设置页徽章、`/` 面板只有生效行、新会话挂载私有 bundle 且记录 private id；停用后新会话挂载共享 bundle 且记录 shared id、列表说明回退）。**旧行为**：挂载选私有只是 `ListSkills` 的 `ORDER BY scope` + map 覆盖的副作用，没有规则、没有 `effective`、UI 两行完全相同、`/` 面板同时列两行且版本对不上；部署实例实测 `effective` 缺失、两行无任何提示（`e2e/.artifacts/fix-palette-shadow/probe-log.json` 旧构建 8/18 失败）。 |
| FR-14.2 挂载到 worktree（只读 bind mount + content_hash） | PARTIAL | `materialize.Mount` 把 bundle **复制**到 `.pi/skills/<slug>`（不是 `.agents/skills/`、不是只读 bind mount）；`content_hash` 只存 DB，目录里没有；`materialize_test.go` |
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
| 首次运行 setup：`GET /v1/setup` + 首次运行面板 + `PIFORGE_BOOTSTRAP_PROVIDER` 预填（`ba532a2`） | DONE | `httpapi/setup_handlers.go` 的 `GET /v1/setup` 返回 `needs_provider` / `is_admin` / `shared_provider_available` / 非密 `bootstrap`（name/base_url/model，没有 key 字段）；`web/components/setup/first-run-setup.tsx` 在首页与会话输入框上方显示「先配置一个模型服务」，按角色提供「添加共享服务商」或设置页链接；`bootstrap.Ensure` 只在「没有可用共享服务商」时创建，重启不覆盖；启动环境持有 key，日志不打印。测试：`setup_test.go` `TestSetupReportsFirstRunState`、`TestSetupExposesBootstrapPrefillWithoutSecrets`，`bootstrap_test.go`，e2e `68-first-run.spec.ts`（新用户首页/输入框都看到面板、配好共享服务商后面板消失、管理员可从面板建共享服务商） |
| 惰性浏览器面板 + 空闲停止（host placement，`15b995c`） | DONE | `workspace-panels.tsx` 只在浏览器面板可见时挂载它；`browser.Manager` 的 idle reaper 在无 viewer 附着 `PIFORGE_BROWSER_IDLE_TIMEOUT` 后 stop 该 sandbox 浏览器；`GET /v1/admin/status` 暴露 `browser_running` / `browser_idle_timeout`。测试：`browser/idle_test.go` 4 例、`67-browser-lazy.spec.ts` 断言「打开会话不启动、选中面板才启动、切走后停止」；部署实测选中前 `docker exec pf-local pgrep chrome` 为空，选中后才出现 |
| 项目删除：先停进程、重试销毁，失败则拒绝并保留项目；清扫历史孤儿目录（本次修复） | DONE | `handleDeleteProject` 的顺序改为「停进程 → 重试销毁 → 删行」：`stopProjectProcesses` 先关该沙箱的浏览器、`Runtime.Stop` 每个会话的 agent、`Term.CloseSandbox` 每个 worktree 的终端（`TermManager` 新增该方法），再 `destroySandbox` 以 150ms 递增退避重试 6 次；仍失败返回 409 并**保留项目**（文案「沙箱目录删不掉，项目已保留…」），不再像旧代码那样记一条 warn 后照删项目行。历史遗留由 `internal/janitor` 处理：启动即扫一轮、之后每 `-sandbox-sweep`（默认 10m，0 关闭，仅本地 provider）扫一次，删除 `sandboxes/<user>/<project>` 中没有 projects 行的目录及其 `_state/<user>/<project>.json`，只认 `usr_*`/`prj_*` 形状的目录名。测试：`project_delete_test.go`（注入 destroy 失败 → 409 + 项目仍在 + 至少重试 2 次；释放后重试 → 行与目录都消失；终端先关后销毁的顺序）、`janitor_test.go` 5 例、`61-project-lifecycle.spec.ts`（UI 删除后**真实路径**消失；用 PowerShell `FileShare.None` 真锁住 checkout → UI 报「沙箱目录删不掉 / 项目已保留」且项目仍在，释放后重试两者都消失）、`e2e/scripts/fix-orphans-repro.ps1`（Windows 复现/验证脚本）。旧代码实测：删除返回 200、项目行消失、`sandboxes/<user>/<project>` 留盘，日志 `delete project: sandbox destroy failed ... unlinkat ...\repo\held-by-a-process.txt: The process cannot access the file...`（`e2e/.artifacts/fix-orphans/fail-first.log`） |
| ⌘K 命令面板的默认选中 / Enter（PRD §10.1-5、§13「全部交互可键盘到达」；PRD 没有对应 FR 行） | DONE | 旧实现把选择交给 cmdk 1.1.1，它按行的 `value` 字符串认选中行。会话行的 label 在 projects 查询返回后才从「项目」变成真实项目名，`value` 随之变化，存储的选中值不再匹配任何行：`[cmdk-item][aria-selected="true"]` 实测 0，而 cmdk 的 Enter 只会向选中行派发，于是「输入 → 回车」什么都不发生。现在面板自己管理选择：行有稳定 id（`session:<id>` 等），active 行由可见列表**推导**（当前 id 不在了就选第一行），`aria-selected`、`aria-activedescendant`、`role=listbox/option`、`aria-controls/expanded` 同源；↑↓/Home/End/Enter 由面板处理。列表仍在加载时按 Enter **忽略而不是排队**：结果随时可能变化，排队的 Enter 会执行用户没看见的行；面板保持打开，行出现后下一次 Enter 生效。测试：`web/components/shell/command-palette.test.tsx` 4 例（异步落定后第一行默认选中且 Enter 进入会话、label 变化不丢选择、加载中 Enter 无效、↑↓ 仍可用）、`e2e/tests/60-session-search.spec.ts` 的「Ctrl+K → 输入 → Enter 直接打开」（旧代码在保留新 spec、去掉 ArrowDown 后必失败）与「列表仍在加载时 Enter 被忽略，随后生效」；部署实例探针 `e2e/scripts/fix-palette-shadow-probe.mjs` 旧构建 `selected=0`、URL 未变。 |

计数：上面 126 条 FR 是 **DONE 62 / PARTIAL 46 / MISSING 17 / 不做 1**（FR-16.3 从 MISSING 移入 DONE；FR-14.1 本批从 PARTIAL 移入 DONE——两个作用域、同名覆盖规则、挂载/记录/UI 与测试都齐了）；加上这四条（均 DONE）后整表 130 行 = **DONE 66 / PARTIAL 46 / MISSING 17 / 不做 1**。

**明确边界：远端 / AIO 沙箱里的浏览器仍不会在空闲时被停止。** `15b995c` 的 idle 策略只在 host placement 生效：控制面直接持有 Chrome 进程，`browser.Manager` 能 stop 它。远端 / AIO 模式下 Chrome 跑在沙箱里，控制面断开 CDP 只意味着没人看了，进程仍在沙箱里；`PIFORGE_BROWSER_IDLE_TIMEOUT` 不会杀它，也没有 runner 侧的空闲回收。要覆盖这条路径，需要沙箱 provider 按「无 viewer」自行 stop 浏览器（或 runner 提供 idle 端点），目前没有实现，也没有测试。

## 最影响用户的缺口（按「用户多久会撞上」排序）

1. **FR-11.3 接管时没有浏览器锁**——输入注入可用，但没有在接管时拦下 `mcp__playwright__*`；人和 agent 仍可能同时操作同一页。（FR-11.2 的「观察下输入被丢弃」已在本批补上端到端断言，见文末。）
2. **FR-15.x MCP 只到一半**——没有 OAuth 代办（15.3）、没有连接状态徽章（15.5）、没有一键导入 mcpServers JSON（15.6）、没有内置预设（15.7）、没有工具列表/逐个禁用（15.8）。
3. **FR-5.8 / 16.4 / 16.6 运维面缺失**——资源护栏只有 docker provider 的启动参数，没有管理入口；镜像管理、护栏配置都没有端点或页面。
4. **FR-9.6 多路跟随**——跟随终端没有「每分支一个跟随 tab」（单路跟随已修，见文末）；终端本体已有多 tab（FR-10.2 本轮补齐）。
5. **FR-12.3 / 12.6 / 12.1**——diff 里不能写行内评论转成 steer；没有一键开 PR；diff 作用域缺「某次提交」与「本轮」。
6. **FR-2.4 / 8.8 / 14.2 / 16.3**——删除分支不清理 worktree（`RemoveWorktree` 无调用者）；没有开 PR；skill 是复制到 `.pi/skills` 而不是只读 bind mount；没有 admin 共享 skill 的写入口。
7. **FR-5.3 快照只能看不能恢复**——清单索引在，但没有 restore-from-snapshot；docker 的回收未实跑；状态条也没有倒计时（FR-5.1）。

## 本次复核改了什么

本版以 `docs/STATUS-AUDIT.md`（`06b61a3`）为输入，逐行重读测试，并把 06b61a3 之后的修复接进台账：

- **补上 DONE 的证据**：FR-2.5 / FR-2.6（`f766651`）、FR-2.7（`bd85ade`）、FR-11.8（`9c4ab0b`）、FR-12.5（`717f7cb`，从 MISSING 移到 DONE）——每一行都指向断言用户可见结果的测试（并排对比的真实数字、已提交分支的采纳、截图的颜色数、测试面板的失败/通过徽章），而不是「handler 返回 200」。
- **下调**：FR-3.3 DONE → MISSING（上限没有配置入口）；FR-1.9 / FR-11.2 / FR-9.2 DONE → PARTIAL（代码在，但缺一条断言用户可见承诺的测试，具体缺什么写在行内）。
- **标题按实际行为**：FR-5.2 由「打开会话自动唤醒」改为「发消息时唤醒」——打开一个 PAUSED 会话不会恢复，发消息才会。
- **去掉不能当证据的引用**：`30-concurrency`、`33-follow-terminal` 这两个永真/不相关断言在对应行里注明了，不再算作 DONE 的依据。
- **计数**：旧表头写 37 / 50 / 39（`2e5f206` 时代，且 FR-12.5 等行早已过期）；本版按 126 行实际状态重算。

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

- **FR-11.3**：输入注入可用，但接管时没有拦下 agent 的 `mcp__playwright__*`，人和 agent 仍可能同抢一页；需要浏览器锁，不是补一条测试。
- **FR-12.1 / FR-12.2 / FR-12.6**：diff 作用域仍缺「某次提交」与「本轮」，逐 hunk 仍只有 API，开 PR 未实现。FR-12.4 只做概览，没有扩大 diff 的作用域。
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
