# 实现状态（对照 PRD）

这份表是**逐条核对代码后**的结果，不是设计意图。判据（比上一版更严）：

- **DONE**：实现 + 至少一条断言**用户可见承诺**的测试或端到端路径。只断言 handler 返回 200、或断言永远为真的测试，不算。
- **PARTIAL**：实现了但缺这样一条测试，或只做了 API / 只做了界面，或只在某一条路径上成立；行内写明缺什么。
- **MISSING**：仓库里没有对应实现。

保留它的原因：我此前两次在汇报里把“做完”说早了。第一次是“P2 全落地”（真），第二次暗示 PRD 全落地（假）。
一张带证据的表比一段自我评价可靠。

基线：HEAD `be01575`（本次复核时的 HEAD，含浏览器启动按沙箱单飞与 profile 僵尸锁恢复）；共 126 条 FR：DONE 52 / PARTIAL 54 / MISSING 19 / 不做 1。

---
# PiForge 对照 PRD 的实现审计

- 审计对象：`C:\Users\<user>\Downloads\piforge`，HEAD `be01575`（"fix(browser): serialise launches per sandbox, and recover a dead profile lock"），工作树干净（本文件除外）。
- 审计方法：先读 `docs/STATUS-AUDIT.md`（`06b61a3`）的每条判定，再逐条重读 `backend/internal/httpapi/router.go` 的路由表、各 handler、`web/components/**` 与 `e2e/tests/*.spec.ts`；以「实现 + 断言了用户可见结果的测试名」为准，不采信命名与注释。
- 状态口径：**DONE** 必须点名一条断言用户可见承诺的测试或 e2e 路径；**PARTIAL** 必须写清缺什么；**MISSING** 是无实现。
- 本版把 `06b61a3` 之后的修复接进台账：`f766651`（FR-2.5 / FR-2.6）、`bd85ade`（FR-2.7）、`9c4ab0b`（FR-11.8）、`717f7cb`（FR-12.5），以及浏览器启动按沙箱单飞与 profile 僵尸锁恢复（FR-11.1）。
- 计数：**126 条 FR，DONE 52 / PARTIAL 54 / MISSING 19 / 不做 1**（本次重算；旧值 37 / 50 / 39 是 `2e5f206` 时代的，且 FR-12.5 等行早已过期）。

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
| FR-1.9 压缩可见 | PARTIAL | 代码在：`context-marker.tsx` `CompactionMarker`（原因、tokens 前后、`firstKeptEntryId`、摘要可展开）+ `handleCompact`。但「可见」没有任何测试断言：`e2e/tests/05-branches.spec.ts` 只 `POST /compact` 后查 `/entries` 里有 `type=compaction`，从不看 `CompactionMarker` 是否渲染；**实测**小会话 `POST /compact` 返回 **500**，容器日志 `pi: command compact failed: Nothing to compact (session too small)`。缺：把 pi 的「会话太小」映射成带原因的 4xx；一条断言「上下文已压缩」出现在 DOM 的测试。 |
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
| FR-3.3 并行上限 3→5 + 拒绝原因 | MISSING | 超限拒绝本身在：`httpapi/concurrency.go` 的 `enforceConcurrency` 返回 429「并行上限是 N 个分支，先让其中一个跑完」，`concurrency_test.go` 覆盖。但 **`Deps.ParallelLimit` 在全仓库只有 `router.go` / `concurrency.go` / `concurrency_test.go` 出现，`piforged/main.go` 从未赋值，也没有 flag/env/管理页** —— 上限 5 是 `concurrency.go` 里的一个 clamp 常量，永远到不了，产品实际永远是默认 3。`concurrency_test.go` 直接把 `env.deps.ParallelLimit` 改成 1 来测 429，绕过了产品配置路径。缺：一个能配置上限的入口（flag/env/admin UI），以及一条从该入口走到 429 的产品级测试。 |
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
| FR-6.6 内容搜索 ⌘⇧F | PARTIAL | 后端 `handleSearch`（`?type=content` + `fsx.Search`）完整；前端无 ⌘⇧F 入口，`quick-open.tsx` 只做文件名搜索 |
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
| FR-9.2 跟随终端 | PARTIAL | `FollowTerminal` 已挂到面板条「跟随」（`workspace-panels.tsx`），`collectBashOutput` 从时间线的 bash 工具块取内容；审计时在容器里实测能看到 agent 的 bash 输出。但唯一的 e2e `33-follow-terminal.spec.ts` 断言 `[data-testid=follow-terminal]` 的 `textContent.length != 0`，而该容器包含工具栏文字「跟随终端」，**永远为真**。缺：一条断言 `.xterm-rows` 里出现该 bash 命令输出的测试 |
| FR-9.3 只读渲染（ansi / 只读 xterm） | PARTIAL | 工具输出走 `tool-trace.tsx` 的 `Trace` + `CodeBlock`（只读）；`FollowTerminal` 的 `readOnly` 终端从未挂载 |
| FR-9.4 命令重放（历史命令列表） | MISSING | 无命令历史面板/端点 |
| FR-9.5 长输出 >5000 行折叠 + 完整输出路径 | PARTIAL | `tool-trace.tsx` 可折叠输出；无 5000 行阈值，无 `fullOutputPath` 概念 |
| FR-9.6 多路跟随（每分支一个跟随 tab） | MISSING | 无跟随 tab 体系 |
| FR-10.1 完整 PTY + xterm（支持 TUI） | DONE | `term/manager.go`（真 PTY，`pty_unix.go`/`pty_windows.go`）+ `terminal-surface.tsx`（`@xterm/xterm`）；`03-workspace.spec.ts` "the terminal opens a real PTY in the sandbox"、"keystrokes reach the shell and output comes back" |
| FR-10.2 多 tab（≥5，重命名/关闭） | PARTIAL | 后端按 `?tab=` 支持任意 tab 名（`pty_handlers.go` `sanitizeTab`）；前端 `workspace-panels.tsx` 只挂一个 `TerminalPane`（tab 默认 `main`），无 tab 条、无重命名/关闭 |
| FR-10.3 断线重连（offset + 60s 不丢） | DONE | `pty_handlers.go` `from_offset` + `pumpPTYToWS`；`term/ring.go` 2MB ring；`manager_test.go` `TestReadOffsetsAreStable`、`ring_test.go` 5 例 |
| FR-10.4 addon-fit + resize 同步 TIOCSWINSZ | DONE | `terminal-surface.tsx`（`AddonFit`）+ `use-pty.ts` 发 `{type:"resize"}` + `pty_handlers.go` `Term.Resize`；`manager_test.go` `TestResize` |
| FR-10.5 复制粘贴/搜索/清屏/字号 | PARTIAL | `terminal-pane.tsx` `Toolbar`：搜索（`addon-search`）、清屏、字号 ±；无复制/粘贴按钮（仅 xterm 选择） |
| FR-10.6 布局（右侧/底部/全屏/拖拽分割） | PARTIAL | 只有右侧面板内的 tab；`ResizablePanelGroup` 分的是会话流/检查器，不是把终端放到下方或全屏 |
| FR-10.7 沙箱暂停 → 终端变灰只读 + 唤醒后回放 | MISSING | 无暂停态 UI；`terminal-pane.tsx` 只有 socket 断开覆盖层，不是沙箱暂停 |
| FR-10.8 与 agent 共享文件系统 | DONE | 终端 cwd 取自 `worktreeRoot(sess)`，与 agent 的 `spec.Worktree` 同目录（`pty_handlers.go` vs `runtime.go spawn` `cfg.Dir`）；`03-workspace.spec.ts` |
| FR-11.1 画面投屏（screencast → WS → canvas） | DONE | `browser.go` `Screencast`/`Page.startScreencast` + `browser_handlers.go` + `browser-panel.tsx` canvas；`08-browser.spec.ts`、`browser_test.go` `TestScreencastForwardsFramesAndAcks`。并发打开面板的启动竞态由 `be01575` 修掉（每 sandbox 单飞、失败返回 typed `LaunchError`、devtools 断线视为死会话）：`TestConcurrentEnsureLaunchesASandboxOnce`、`TestTwoSandboxesLaunchInParallel`、`TestRealChromeConcurrentEnsureSharesOneBrowser`；部署实测同 tick 打开两个面板 socket 都拿到同一个 pid |
| FR-11.2 观察模式（默认不可输入） | PARTIAL | 默认 `observe` 成立；`browser_handlers.go` 在非 takeover 时对 `input` 命令直接 `break`（服务端判定，不靠面板自觉）。但**没有任何测试断言「输入被丢弃」**：`browser_mode_test.go` 只读写 mode，`43-browser-mode.spec.ts` 只断言按钮文字与确认弹窗。缺：一条 observe 下键入/点击不生效、takeover 下生效的测试（单测或 e2e 均可） |
| FR-11.3 接管模式（输入 + 暂停 agent 浏览器工具） | PARTIAL | 输入注入可用（`Session.TabDispatch` → `Input.dispatch*`，`browser_test.go`）；没有「接管时拦截 `mcp__playwright__*`」的浏览器锁 |
| FR-11.4 模式互斥 + 「谁在控制」标识 + 确认 | DONE | `GET/POST …/browser/mode`；进入 takeover 前弹确认（说明会中断 agent），确认后中断当前运行；面板按钮显示「观察 / 控制中」；`browser_mode_test.go`、`43-browser-mode.spec.ts` |
| FR-11.5 地址栏 + 前进/后退/刷新 + 端口快捷入口 | PARTIAL | `browser-panel.tsx` 有 URL `Input` 与刷新/导航；无前进/后退，无端口列表下拉 |
| FR-11.6 agent 侧通过 Playwright MCP 驱动同一 chromium | MISSING | 无 Playwright MCP 预设或 CDP 端点下发（`materialize.mcpEntry` 只写用户自己填的 command/url） |
| FR-11.7 多 tab（≥3，tab 条 + 缩略图） | DONE | `browser/tabs.go` + `handleListBrowserTabs`/`handleOpenBrowserTab`/`handleCloseBrowserTab` + `browser-panel.tsx` tab 条；`26-browser-tabs.spec.ts`、`tabs_test.go` |
| FR-11.8 截图入 workspace | DONE | `9c4ab0b`：截图跟随面板当前激活的 tab（`Session.SetActiveTab` / `ScreenshotTab`），空图被拒并给出原因（`capture.go` `ErrBlankCapture`）。测试：`screenshot_test.go`（about:blank、纯色图、非 PNG、连续两次）、`real_chrome_test.go` `TestRealChromeScreenshotFollowsTheActiveTab`（红/蓝两页，证明截的是当前页）；`e2e/tests/20-screenshot.spec.ts` 先导航到真实页面，再解码 PNG 断言 >8 种颜色、连拍两次都 201、空白 tab 返回 422 且不落文件、预览用 `inline=1` |
| FR-11.9 profile 持久化（sandbox 内 .piforge-browser） | DONE | `browser.go` `profile := filepath.Join(profileRoot, ".piforge-browser")` + `--user-data-dir`；`profile_test.go` `TestRealChromeProfileIsStableAcrossSessions`（边界已注明） |
| FR-12.1 Diff 作用域（未暂存/已暂存/某提交/分支 vs 基准/本轮） | PARTIAL | `parseDiffScope` 支持 unstaged/staged/branch/commit；前端 `git-panel.tsx` 的 `SCOPES` 只有 3 项（**无「某次提交」**），且无「本轮」作用域 |
| FR-12.2 逐文件 + 逐 hunk stage/unstage/revert | PARTIAL（逐 hunk 只有 API，UI 是逐文件） | `gitx/hunks.go`（客户端只传 index，补丁由服务端重建）；`POST …/git/hunk`、`POST …/git/discard`；变更面板每行「丢弃」（带确认）；`hunks_test.go`、`37-hunks.spec.ts`。**diff 视图里没有逐 hunk 按钮** |
| FR-12.3 行内评论 → 作为 steer 发给 agent | MISSING | diff 组件没有评论入口，也无 `streaming_behavior:"steer"` 的调用链 |
| FR-12.4 变更概览（增删行数 + 风险标记） | PARTIAL | `git-changes.tsx` 分组 + 文件列表；**无 +/- 行数汇总，无 lockfile/CI 风险标记**（`GitStatus` 也没有这些字段） |
| FR-12.5 测试结果面板 | DONE | `717f7cb`：`internal/testrun` 解析 go test / vitest / jest / pytest 输出（含 `race detected`、`file:line`），`GET /v1/sessions/{id}/test-runs` 从工具事件与终端两条来源入库，`test-panel.tsx` 有失败/通过徽章与逐条失败展开。测试：`testrun/parse_test.go`、`store/testruns_test.go`、`httpapi/test_run_handlers_test.go`；`e2e/tests/62-test-panel.spec.ts` 断言空态、失败徽章与 `TestCacheEvict` / `cache_test.go:42`、随后同一会话变成通过徽章、刷新后仍有两条且 API 与面板一致 |
| FR-12.6 一键开 PR | MISSING | 无 `…/pr` 路由与 UI |
| FR-12.7 产物交付（present 为下载/预览卡） | PARTIAL | 预览区与 `binary-card.tsx` 提供下载/预览；没有「present 任意产物」的语义（截图会写入 worktree 并在树里可打开） |
| FR-13.1 admin 共享 provider + Inherited 徽章 | DONE | `handleAdminUpsertProvider`/`handleAdminListProviders` + `handleListProviders` 合并标 `inherited`；`12-shared-providers.spec.ts`、`admin_management_test.go` `TestAdminSharesAProviderThatUsersInherit` |
| FR-13.2 用户私有 provider + 同名 shadow | DONE | `handleCreateProvider`/`handleUpdateProvider` + 按名合并；`handlers_test.go` `TestProvidersMergeAndShadow` |
| FR-13.3 key AES-GCM 存库 + 沙箱内无明文 | DONE | `crypto.Box` 封存（`handleCreateProvider` 拒绝无 Box 的写入）+ `llmgw` 短期 token 注入环境（`piforged/main.go` `setupFor` 的 `ANTHROPIC_API_KEY=token`）；`TestProviderKeysAreSealedAtRest`、`gateway_test.go` `TestGatewayInjectsAnthropicKeyAndStripsTheEphemeralOne` |
| FR-13.4 模型选择器（按 provider 分组 + 窗口/推理/成本） | PARTIAL | `workbench.tsx` 构造扁平 `provider/model` 选项，`composer.tsx` 单层 `Select`；不显示 context window / reasoning / 成本（这些字段在 settings 页有） |
| FR-13.5 推理强度随模型能力动态显示 | PARTIAL | 前端写死 `THINKING_OPTIONS`（5 档），后端只有 `thinking_level` 列与 PATCH；`pi/runtime.go` 从不发 `set_thinking_level`，也没有 `get_available_thinking_levels`（只有 `pitest/serve.go` 的假实现） |
| FR-13.6 会话级模型覆盖 | DONE | `composer.tsx` `persistSetting({model_ref})` → `PATCH` + `handlePostMessage` 的 `ModelRef`；`10-models.spec.ts` "a custom model id typed in the composer is what the session uses" |
| FR-13.7 连通性测试 | DONE | `provider_probe.go` + `POST /v1/providers/{id}/test`；`provider_probe_test.go` 4 例、`06-providers.spec.ts` |
| FR-13.8 用量显示（token + 估算成本） | PARTIAL | `SessionStats.TokensIn/Out/CostUSD` + `ContextMeter`；无测试断言成本显示 |
| FR-13.9 用户 API Key（pf_…） | DONE | `api_key_handlers.go` + `api_keys.go` + Bearer 分支（`router.go` `authed`/`mutatingBearer`）；`24-api-keys.spec.ts`、`api_keys_test.go` |
| FR-14.1 两层作用域（admin 共享 + 用户私有，同名覆盖） | PARTIAL | `store/resources.go` `ListSkills` 的 `WHERE (scope='user' AND owner_id=?) OR scope='admin'` 只做「并集」，无同名 shadow/去重；且没有 admin 写共享 skill 的入口 |
| FR-14.2 挂载到 worktree（只读 bind mount + content_hash） | PARTIAL | `materialize.Mount` 把 bundle **复制**到 `.pi/skills/<slug>`（不是 `.agents/skills/`、不是只读 bind mount）；`content_hash` 只存 DB，目录里没有；`materialize_test.go` |
| FR-14.3 Skill 中心（浏览/搜索/详情/SKILL.md 渲染） | PARTIAL | `web/app/settings/skills/page.tsx`：列表/新建/删除/上传（含 `SKILL.md` 文本域）；无搜索框、无详情页（不渲染文件树与脚本预览） |
| FR-14.4 一键安装（Git URL / zip）+ 可禁用 | PARTIAL（Git 有，zip 没有） | `POST /v1/skills/import`：克隆仓库、按含 SKILL.md 的目录逐个导入、只取该目录下文件、4 MB 上限；`skill_import_test.go`、`38-skill-import.spec.ts`；UI 在设置页「从仓库导入」。**zip 上传未实现** |
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
| FR-16.3 共享 skill 管理 | MISSING | 无 admin skill 路由（`/v1/admin/skills` 不存在），页面只说明「共享 Skill 由管理员维护」 |
| FR-16.4 镜像管理 | MISSING | 无镜像版本/构建状态端点或页面 |
| FR-16.5 运行状态（在线用户/活跃沙箱/节点水位/镜像告警） | PARTIAL | `handleAdminStatus` 返回 users/sessions/sandboxes/providers/skills 计数（`admin_status_test.go`、`15-quick-open-status.spec.ts`）；无节点水位、无镜像拉取失败告警 |
| FR-16.6 资源护栏配置 | MISSING | 无配置端点/页面（与 FR-5.8 同源缺口） |
| FR-16.7 功能开关（按用户灰度） | DONE | `feature_handlers.go` + `feature_flags` 表 + 服务端强制（`featureDenied`）；`17-feature-flags.spec.ts`、`feature_test.go` |
| FR-16.8 支持模式（只读 + 通知留痕） | DONE | `support_handlers.go` + `support_access` 表 + `support-notices`；`16-support-mode.spec.ts`、`support_test.go` 2 例 |

## 最影响用户的缺口（按「用户多久会撞上」排序）

1. **FR-3.3 并行上限不可配置**——`Deps.ParallelLimit` 在 `piforged/main.go` 从未赋值，也没有 flag/env/管理页：产品永远按默认 3 个分支跑，PRD 承诺的 5 和「可调」都不存在。超限拒绝（429 文案）本身有实现与单测。
2. **FR-1.9 压缩不可见，小会话还会 500**——`CompactionMarker` 没有任何测试断言「上下文已压缩」出现在页面上；对小会话点压缩，服务端返回 500（`Nothing to compact`），应是一个带原因的 4xx。
3. **FR-11.2 观察模式的「不可输入」只有代码**——服务端确实丢弃输入，但 `browser_mode_test.go` 与 `43-browser-mode.spec.ts` 都不验证这一点；一旦回归，人和 agent 会互相抢鼠标。
4. **FR-15.x MCP 只到一半**——没有 OAuth 代办（15.3）、没有连接状态徽章（15.5）、没有一键导入 mcpServers JSON（15.6）、没有内置预设（15.7）、没有工具列表/逐个禁用（15.8）。
5. **FR-5.8 / 16.4 / 16.6 运维面缺失**——资源护栏只有 docker provider 的启动参数，没有管理入口；镜像管理、护栏配置都没有端点或页面。
6. **FR-10.2 / 9.6 多终端与多路跟随**——后端支持 `?tab=`，前端只有一个终端面板；跟随终端也没有「每分支一个跟随 tab」，且现有 e2e 是永真断言。
7. **FR-12.3 / 12.6 / 12.1**——diff 里不能写行内评论转成 steer；没有一键开 PR；diff 作用域缺「某次提交」与「本轮」。
8. **FR-2.4 / 8.8 / 14.2 / 14.4 / 16.3**——删除分支不清理 worktree（`RemoveWorktree` 无调用者）；没有开 PR；skill 是复制到 `.pi/skills` 而不是只读 bind mount、zip 安装缺失、没有 admin 共享 skill 的写入口。
9. **FR-5.3 快照只能看不能恢复**——清单索引在，但没有 restore-from-snapshot；docker 的回收未实跑；状态条也没有倒计时（FR-5.1）。

## 本次复核改了什么

本版以 `docs/STATUS-AUDIT.md`（`06b61a3`）为输入，逐行重读测试，并把 06b61a3 之后的修复接进台账：

- **补上 DONE 的证据**：FR-2.5 / FR-2.6（`f766651`）、FR-2.7（`bd85ade`）、FR-11.8（`9c4ab0b`）、FR-12.5（`717f7cb`，从 MISSING 移到 DONE）——每一行都指向断言用户可见结果的测试（并排对比的真实数字、已提交分支的采纳、截图的颜色数、测试面板的失败/通过徽章），而不是「handler 返回 200」。
- **下调**：FR-3.3 DONE → MISSING（上限没有配置入口）；FR-1.9 / FR-11.2 / FR-9.2 DONE → PARTIAL（代码在，但缺一条断言用户可见承诺的测试，具体缺什么写在行内）。
- **标题按实际行为**：FR-5.2 由「打开会话自动唤醒」改为「发消息时唤醒」——打开一个 PAUSED 会话不会恢复，发消息才会。
- **去掉不能当证据的引用**：`30-concurrency`、`33-follow-terminal` 这两个永真/不相关断言在对应行里注明了，不再算作 DONE 的依据。
- **计数**：旧表头写 37 / 50 / 39（`2e5f206` 时代，且 FR-12.5 等行早已过期）；本版按 126 行实际状态重算。

## 无法核实

- **FR-5.3 的 docker provider 回收**：只在 local provider 上跑过；docker 的 `Destroy` 是否保留宿主目录、reclaim 后 `MISSING` 行是否一致，需要一台有 docker 的机器单独跑。
- **FR-1.9 的 UI marker**：无法把真实会话压到 pi 的压缩阈值（小会话直接 500），所以没看到「上下文已压缩」在页面上出现。
- **FR-11.9 的 cookie 复用**：只证明 profile 目录在多个沙箱里存在/复用，没有验证登录态跨浏览器重启保留。
- **FR-13.6 的「真的用了这个模型」**：只证明存进 session 并进了 pi 的 spec；上游实际模型只能从网关 token 的 claim 推断。