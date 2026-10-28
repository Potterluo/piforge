# 实现状态（对照 PRD）

这份表是**逐条核对代码后**的结果，不是设计意图。判据：

- **DONE**：实现 + 至少一条测试（单测或 e2e）。
- **PARTIAL**：实现了但缺测试，或只做了 API / 只做了界面，或只在某一条路径上成立。
- **MISSING**：仓库里没有对应实现。

保留它的原因：我此前两次在汇报里把“做完”说早了。第一次是“P2 全落地”（真），第二次暗示 PRD 全落地（假）。
一张带证据的表比一段自我评价可靠。

基线：HEAD 2e5f206。共 126 条 FR：DONE 35 / PARTIAL 51 / MISSING 40。

---
# PiForge 对照 PRD 的实现审计

- 审计对象：`C:\Users\<user>\Downloads\piforge`，HEAD `2e5f206`（"docs: say what is actually implemented, and implement clone"），工作树干净。
- 审计方法：逐条打开 `backend/internal/httpapi/router.go` 的路由表、各 handler、`web/components/**` 与 `e2e/tests/*.spec.ts`，以「路由 + handler/组件 + 测试名」为准，不采信命名与注释。
- 状态口径：**DONE** = 有实现且至少一条单测/e2e 覆盖；**PARTIAL** = 有实现但无测试，或只做了 API / 只做了界面 / 只做了 PRD 描述的一部分；**MISSING** = 无实现。
- 审计过程中仓库被并发修改过：审计开始时 `POST /v1/sessions/{id}/clone`（FR-2.7）尚是未提交的未跟踪文件，约 03:50 被提交进 `2e5f206`。下表按该提交为准。
- 计数：**126 条 FR，DONE 35 / PARTIAL 51 / MISSING 40**。

> 说明：PRD 中没有「部署」类 FR（§12 是里程碑，`deploy/` 是 Dockerfile.app / Dockerfile.offline / k8s.yaml / entrypoint.sh，属交付物而非需求行），因此下面没有部署行。

| FR | 状态 | 证据 |
|---|---|---|
| FR-1.1 新建会话（仓库/基准分支/模型/推理强度） | PARTIAL | 会话只选标题/模型/推理/临时：`web/components/shell/new-session-dialog.tsx`（`FieldLabel` 标题、`session-model`、`session-thinking`）；仓库与基准分支只在建项目时选（`new-project-dialog.tsx` 的 `repo_url`/`default_branch`，`handleCreateProject`）。测试：`e2e/tests/00-shell.spec.ts` "creates a project and a session through the dialogs"（未覆盖分支选择） |
| FR-1.2 会话流就地折叠 | DONE | `web/components/session/message-item.tsx` + `thinking-trace.tsx` + `tool-trace.tsx`（Trace/Collapsible，无弹窗）；`e2e/tests/02-conversation.spec.ts` "a full run streams a reply, a tool call and settles"（点 `tool-trace` 展开 `tool-output`） |
| FR-1.3 双队列输入（follow-up / steer） | DONE | `composer.tsx` `onKeyDown` → `submit("follow_up")` 或 `submit("steer")`；后端 `handlePostMessage` 的 `streaming_behavior` 校验；`composer.test.tsx` 三个用例 + `02-conversation.spec.ts` "Enter queues a follow-up while running, Ctrl+Enter steers" |
| FR-1.4 Esc 清队列 + abort + 回填 | DONE | `composer.tsx` `interrupt()` 把 `cleared_steering`/`cleared_follow_up` 合并回输入框；后端 `handleInterrupt` → `rt.Interrupt`（`clear_queue`）；`composer.test.tsx` "interrupts on Esc and restores the cleared queue text" |
| FR-1.5 会话列表：搜索/排序/状态徽章 | PARTIAL | `app-sidebar.tsx` `groupSessions` + `StatusDot` + 归档切换 + 按 `last_active_at` 排序；**没有搜索框**（`GET /v1/sessions?q=` 有后端支持，客户端 `sessions.list` 只传 `project`）；无对应测试 |
| FR-1.6 断线续传（SSE Last-Event-ID + entries） | DONE | `sse.go` `handleEvents`（读 `Last-Event-ID`/`after_seq`，窗口超限回 409 `need_full_resync`）+ `handleEntries`；`sse_test.go` 5 个用例，含 `TestSSEReplayFromLastEventID`、`TestSSEGapReturnsNeedFullResync` |
| FR-1.7 重命名/归档/删除/导出（MD / HTML） | PARTIAL | 重命名/归档/删除：`handlePatchSession`/`handleDeleteSession` + `app-sidebar.tsx` `SessionActions`，`14-session-actions.spec.ts`；导出：`session_export.go` 同时有 `renderMarkdown` 与 `renderHTML`，但 UI 只有 `api.sessions.exportUrl(id,"md")`，HTML 无入口 |
| FR-1.8 上下文用量（按轮 in/out/cache + 窗口占用） | PARTIAL | `handleSessionStats` + `Store.SessionStats`/`LastContextUsage`；`context-marker.tsx` `ContextMeter`（tokens/percent/cost）。无单测或 e2e 断言用量数值 |
| FR-1.9 压缩可见 | DONE | `context-marker.tsx` `CompactionMarker`（原因、tokens 前后、`firstKeptEntryId`、摘要可展开）+ `handleCompact`；`e2e/tests/05-branches.spec.ts` "compaction is recorded as a visible context event" |
| FR-1.10 临时会话 | DONE | `project_handlers.go` `ephemeral` 字段 + `store.SweepEphemeral` + `piforged/main.go` 6 小时清理循环；`ephemeral_test.go` `TestEphemeralSessionsAreHiddenAndSwept` 与 `23-ephemeral.spec.ts` |
| FR-2.1 任意用户消息处「从这里分叉」 | DONE | `GET /v1/sessions/{id}/fork-points` → `handleForkPoints`（只列 user message）、`POST …/fork` → `handleFork`；UI `branch-tree.tsx`（"可分叉的用户消息"）与 `fork-dialog.tsx`；`18-branching.spec.ts` 3 例 |
| FR-2.2 分叉建新 session + 新 worktree | DONE | `handleFork` → `provisionSession` → `provision.EnsureWorktree` → `gitx.AddWorktree`（`worktree.go`）；`05-branches.spec.ts` "forking a user message creates a branch session with its own worktree"、`fork_history_test.go` |
| FR-2.3 分支树视图（父子/时间/状态/diff 规模） | PARTIAL | `branch-tree.tsx` 画的是 **Pi entry 树**（`TreeNodeRow`，按 `children` 缩进）+ 分叉点列表；主干↔分支的父子只在侧栏 `app-sidebar.tsx` `groupSessions` 里体现（`Collapsible` + `StatusDot`）。没有「创建时间/diff 规模」列，也没有分支级图形。无测试 |
| FR-2.4 分支命名 / 归档 / 删除（清理 worktree） | PARTIAL | 命名与归档：`handleFork` 的 `label`、`handlePatchSession`；删除：`handleDeleteSession` **明确保留 worktree 在磁盘上**（注释 "The worktree stays on disk"），`gitx.RemoveWorktree`/`PruneWorktrees` 全仓库零调用；且从未写 Pi 的 `label` entry（`pi/runtime.go` 无 `set_label`/`label`）。`18-branching.spec.ts` 只覆盖父子关系 |
| FR-2.5 对比视图（2–3 分支并排） | DONE（无「测试」列） | `GET /v1/sessions/compare?ids=`、`compare-page.tsx`、`/p/{project}/compare`、`compare_test.go` + `29-compare.spec.ts`；本部署不跑测试，故不设测试列；缺 checkout 时报 `diff_error` 而非 0 |
| FR-2.6 采纳（cherry-pick / 整树覆盖） | DONE | `gitx.Runner.Adopt` + `POST /v1/sessions/{id}/adopt` + 侧栏「采纳回主干」；`adopt_test.go`（真 git，含两种拒绝）、`branch_adopt_test.go`、`28-adopt.spec.ts` |
| FR-2.7 `clone` 复制当前分支 | DONE | `session_clone.go` `handleCloneSession` + `POST /v1/sessions/{id}/clone`（router.go:203）+ `api.sessions.clone` + 侧栏 `data-testid="session-clone"`；`session_clone_test.go` `TestCloneKeepsTheWholeConversation`（无 e2e） |
| FR-3.1 同一主会话下只有一个分支在执行 | DONE | `httpapi/concurrency.go` `enforceConcurrency`：发消息前中断同 root 的其它运行中分支，并在对方事件流写 `session.paused`；`concurrency_test.go` 断言中断 + 事件 |
| FR-3.2 显式并行运行 2–3 分支 | DONE（每个分支仍是自己的会话与 PTY） | 每用户开关 `parallel`（`store.FeatureParallel`，默认关）；管理员「功能开关」里可打开 |
| FR-3.3 并行上限 3→5 + 拒绝原因 | DONE（超限拒绝，不做排队） | `Deps.ParallelLimit`（默认 3，上限 5），超限返回 429「并行上限是 N 个分支，先让其中一个跑完」；`concurrency_test.go` |
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
| FR-5.2 打开会话自动唤醒（≤3s） | MISSING | `sandbox.Resume` 除测试外无调用者；打开会话不触达 sandbox，PAUSED 也不会自愈 |
| FR-5.3 空闲 15min 暂停 / 14d 回收 / 先快照后销毁 | MISSING | 无 TTL 看门狗；`Local.Pause` 注释明说 "nothing is snapshotted"；`piforged/main.go` 的定时循环只清理 ephemeral 会话 |
| FR-5.4 多 worktree 创建/列出/清理 | PARTIAL | 创建：`provision.EnsureWorktree`（`provision_test.go`、`routes_test.go` `TestCreateSessionProvisionsWorktree`）；列出/清理：无 HTTP 端点，`gitx.ListWorktrees`/`RemoveWorktree`/`PruneWorktrees` 无调用者 |
| FR-5.5 依赖缓存共享 | MISSING | `GOMODCACHE`/`_cacache`/`CARGO_HOME` 全仓库零命中 |
| FR-5.6 快照（fs / fs+mem，仅工具边界） | PARTIAL | 只有数据层：`store/resources.go` `CreateSnapshot`/`ListSnapshots` + `store_test.go`；没有任何生产者、没有工具边界钩子、无 HTTP、`sandbox.Pause` 不快照 |
| FR-5.7 空闲回收 | DONE | `Runtime.ReapIdle` + `StartIdleReaper`（`PIFORGE_AGENT_IDLE_TTL`，默认 30 分钟）；有 run 在跑时不回收；`TestReapIdleStopsUnusedAgentsOnly`；e2e 用 45s TTL |
| FR-5.8 资源护栏（并发/CPU/内存/磁盘） | MISSING | `sandboxes` 表有 `cpu_milli`/`mem_mb`/`disk_gb` 列，但无校验、无拒绝、无 cgroup 限制 |
| FR-5.9 手动暂停/唤醒/快照/重建 | MISSING | 路由表里没有 sandbox 操作端点（`router.go` 全量对照） |
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
| FR-7.7 二进制下载卡 | PARTIAL | `preview/binary-card.tsx`（大小/类型/下载）；无测试 |
| FR-7.8 大文件保护（2MB / 20MB） | DONE | `fsx.go` `DefaultMaxInline = 2<<20`、`DefaultMaxPreview = 20<<20`、首尾截断（`truncationMarker`）；`19-preview.spec.ts` "a download streams the whole file, not the preview window"、`fsx_test.go` |
| FR-7.9 Diff 视图 unified / split | PARTIAL | `git-diff.tsx` + `git-diff-impl.tsx`（`@git-diff-view/react`，`DiffModeEnum.Split/Unified`）；`04-git.spec.ts` 只验 patch 内容，切换未测 |
| FR-7.10 网页内改文件并落盘 | DONE | `PUT /v1/sessions/{id}/files` 有调用者了（`api.files.write`），保存后重新读取；`31-file-edit.spec.ts` 断言服务端字节已更新、取消不写 |
| FR-8.1 当前分支 / ahead-behind / 干净状态 | DONE | `gitx/status.go` + `handleGitStatus` + `git-data.tsx`/`git-panel.tsx` 状态头；`04-git.spec.ts` "the branch is reported…" |
| FR-8.2 变更列表（staged/unstaged/untracked 分组） | DONE | `git-changes.tsx`（三组 `ChangeGroup`）；`04-git.spec.ts` 断言 untracked 出现在 status |
| FR-8.3 提交（消息 + 选中文件 + amend） | PARTIAL | `git-panel.tsx` 提交框 + `amend` `Switch` + `stagedPaths`；`handleGitCommit` 支持 `amend`；`04-git.spec.ts` 覆盖提交与路径，**amend 无测试** |
| FR-8.4 分支操作（新建/切换/删除/合并/rebase） | PARTIAL | 只有 `POST …/git/branch`（`handleGitBranch`）与 `…/git/checkout`（`handleGitCheckout`）；`gitx/ops.go` 无 Merge/Rebase，无删除分支，无冲突指引 |
| FR-8.5 历史列表 + 点击看该 commit diff | PARTIAL | `git-history.tsx` 列表 + `git-commit-sheet.tsx`，但 sheet 注释明说「无法按 commit 取 diff」，只展示元数据；`api.git.diff` 不接受 commit 参数 |
| FR-8.6 Git 树（SVG DAG + ref 标签 + merge 线 + 点击详情） | PARTIAL | `git-graph.tsx`（`assignLanes`、28/18/4.5、ref 最多 3 个 + `+N`）+ `lib/git/lanes.test.ts` + `04-git.spec.ts`（`git-graph-row` 渲染）；无 HoverCard，点击只有元数据 sheet（无 diff） |
| FR-8.7 与分支会话联动（图上高亮「谁跑的分支」） | MISSING | `git-graph.tsx`/`git-data.tsx` 没有 worktree/session ↔ 分支的映射，也没有高亮 |
| FR-8.8 远端操作 fetch/pull/push + 开 PR | MISSING | 无 `…/git/push`、`pull`、`fetch`、无 `…/pr` 路由；`gitx/ops.go` 无 push/fetch；"pull" 全仓库零命中 |
| FR-9.1 跟随终端数据源（tool_execution_* partialResult） | DONE | `pi/normalize.go` `ToolProgressPayload` → `EvToolProgress`（tool_execution_update）、`PtyStreamPayload` → `EvPtyStream`；`normalize_test.go`、`web/lib/session/reduce.ts` 消费 `tool.progress` |
| FR-9.2 跟随终端 | DONE | `FollowTerminal` 已挂到面板条「跟随」；`collectBashOutput` 从时间线的 bash 工具块取内容；`33-follow-terminal.spec.ts` |
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
| FR-11.1 画面投屏（screencast → WS → canvas） | DONE | `browser.go` `Screencast`/`Page.startScreencast` + `browser_handlers.go` + `browser-panel.tsx` canvas；`08-browser.spec.ts`、`browser_test.go` `TestScreencastForwardsFramesAndAcks` |
| FR-11.2 观察模式（默认不可输入） | MISSING | `browser_handlers.go` 收到 `input` 一律 `TabDispatch`；`browser-panel.tsx` 的 click/key/wheel 始终发送，无只读模式 |
| FR-11.3 接管模式（输入 + 暂停 agent 浏览器工具） | PARTIAL | 输入注入可用（`Session.TabDispatch` → `Input.dispatch*`，`browser_test.go`）；没有「接管时拦截 `mcp__playwright__*`」的浏览器锁 |
| FR-11.4 模式互斥 + 「谁在控制」标识 + 确认 | MISSING | 无 mode 状态机（OBSERVE 或 TAKEOVER）、无确认弹窗、无标识 |
| FR-11.5 地址栏 + 前进/后退/刷新 + 端口快捷入口 | PARTIAL | `browser-panel.tsx` 有 URL `Input` 与刷新/导航；无前进/后退，无端口列表下拉 |
| FR-11.6 agent 侧通过 Playwright MCP 驱动同一 chromium | MISSING | 无 Playwright MCP 预设或 CDP 端点下发（`materialize.mcpEntry` 只写用户自己填的 command/url） |
| FR-11.7 多 tab（≥3，tab 条 + 缩略图） | DONE | `browser/tabs.go` + `handleListBrowserTabs`/`handleOpenBrowserTab`/`handleCloseBrowserTab` + `browser-panel.tsx` tab 条；`26-browser-tabs.spec.ts`、`tabs_test.go` |
| FR-11.8 截图入 workspace | DONE | `browser_screenshot.go` `handleBrowserScreenshot` + `20-screenshot.spec.ts`（两例） |
| FR-11.9 profile 持久化（sandbox 内 .piforge-browser） | DONE | `browser.go` `profile := filepath.Join(profileRoot, ".piforge-browser")` + `--user-data-dir`；`profile_test.go` `TestRealChromeProfileIsStableAcrossSessions`（边界已注明） |
| FR-12.1 Diff 作用域（未暂存/已暂存/某提交/分支 vs 基准/本轮） | PARTIAL | `parseDiffScope` 支持 unstaged/staged/branch/commit；前端 `git-panel.tsx` 的 `SCOPES` 只有 3 项（**无「某次提交」**），且无「本轮」作用域 |
| FR-12.2 逐文件 + 逐 hunk stage/unstage/revert | PARTIAL | `handleGitStage`/`handleGitUnstage` + `git-changes.tsx`（文件级、可整组 stage）；**无 hunk 级操作、无 revert** |
| FR-12.3 行内评论 → 作为 steer 发给 agent | MISSING | diff 组件没有评论入口，也无 `streaming_behavior:"steer"` 的调用链 |
| FR-12.4 变更概览（增删行数 + 风险标记） | PARTIAL | `git-changes.tsx` 分组 + 文件列表；**无 +/- 行数汇总，无 lockfile/CI 风险标记**（`GitStatus` 也没有这些字段） |
| FR-12.5 测试结果面板 | MISSING | 无解析/面板；`tool-trace.tsx` 只是把输出当文本 |
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
| FR-14.4 一键安装（Git URL / zip）+ 可禁用 | PARTIAL | `POST /v1/skills` + `PUT /v1/skills/{id}/bundle`（提交文件数组）+ `skill_bundle_handlers_test.go`；**无 Git URL / zip 导入**，UI 无启停开关（列存在） |
| FR-14.5 会话记录所用 skill@version | MISSING | `skills.version` 只在自己表里，`sessions.config` 未记录任何 skill 版本；无相关代码 |
| FR-14.6 `/skill:name` + `/` 命令面板 | MISSING | `command-palette.tsx` 只有会话/项目命令；无 `/` 斜杠命令、无 skill 调用 |
| FR-14.7 内置开箱可用 skill | MISSING | `store/seed.go` 只种 admin 用户与共享 provider（`EnsureAdminSeed`），无内置 skill |
| FR-15.1 MCP server 配置 | PARTIAL（**配置不生效**） | CRUD + 加密凭据都在，但**没有任何代码把 MCP server 写进 Pi 的 `mcp.json`**，所以 agent 里没有这些工具。这一节的其它行都以此为前提 |
| FR-15.2 MCP 凭据加密保存 | PARTIAL（存得住、读不回；未注入到进程） | `store/secrets.go` AES-GCM（`PIFORGE_SECRET_KEY` 或数据目录 `secret.key`），`secrets_cipher` 列；`MCPServerSecrets` 只给启动器用；`secrets_test.go` 断言密文不含明文、跨用户读不到；`32-mcp-secrets.spec.ts`。**还没把凭据注入到 MCP 进程的 env** |
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

1. **FR-2.5 对比视图**——PRD 的招牌场景 JS-2「三种改法并行试，对比后择优」现在有了分叉、clone、采纳（FR-2.6 已完成），还差「并排对比」这一屏。
2. **FR-3.1 / 3.2 / 3.3 并行执行控制**——既没有「切走前 pause」的互斥，也没有「显式并行」开关与 3→5 上限/排队原因。多分支现在只是「多个独立会话」，PRD 里那层语义完全不存在；`docs/STATUS.md` 也承认这三条未做。
3. **FR-4.2 / FR-4.5 sidecar 只读不成立**——`ReadOnlyTools` 只是个被赋值 4 次、从未被 `pi/runtime.go` 使用的字段。PRD 的 AC-4.2（sidecar 里 `rm -rf` 被工具集拦住）现实是拦不住，且没有提权开关。这是「说了但没做到」的安全承诺。
4. **FR-15.2 MCP 凭据被静默丢弃，UI 却宣称已加密**——`POST /v1/mcp/servers` 接受 `secrets` 后直接不落库（代码注释自认；`contracts.MCPServer` 连存密文的字段都没有），而设置页写着「凭据单独加密保存」。用户填的 token 消失且无报错，需要 header/env 的 MCP 服务器永远连不上——这是「文案撒谎」而不只是「功能缺失」。
5. **FR-7.1 / FR-7.10 不能编辑文件**——PRD 的 P0 要求 CodeMirror 6 只读/编辑切换；现在 `web/package.json` 连 `@codemirror/*` 都没有，`api.files.write` 无任何调用者，`PUT …/files` 是死路由。用户想手改一行只能去终端。
6. **FR-8.8 完全没有任何远端操作**——fetch/pull/push/开 PR 全无（"pull" 全仓库零命中）。写完代码只能留在本地分支。
7. **FR-5.2 / 5.3 沙箱唤醒与回收策略**——`sandbox.Resume` 无调用者，没有 15min/14d 的 TTL，也没有快照。PRD 的 JS-6「昨天那事继续」和「PAUSED 打开即唤醒 ≤3s」都不存在，状态条也没有倒计时。
8. **FR-9.2 / 9.4 跟随终端没接上**——`FollowTerminal` 组件写好了（含只读 xterm 与工具栏）但**没有任何地方渲染**；命令重放也没有。PRD 把「跟随」列为新增重点，现在 agent 跑 bash 只能在 Trace 里看折叠文本。
9. **FR-11.2 / 11.4 浏览器没有观察/接管模式**——输入永远直通（click/key/wheel 全发），没有「谁在控制」、没有二次确认、也没有接管时拦 agent 的浏览器工具。人和 agent 会互相抢鼠标，正是 PRD §7.6.2 说要避免的灾难。
10. **FR-1.5 会话列表不能搜索**——`GET /v1/sessions?q=` 后端有了，侧栏没有输入框。会话一多就翻不动，这是每天都会撞上的小缺口。
11. **FR-10.2 终端只有一个 tab**——后端支持 `?tab=`，前端没有 tab 条、不能重命名/关闭；PRD 要求 ≥5 个独立 PTY。
12. **FR-12.5 / 12.6 测试结果面板与一键开 PR**——两者都没有；`FR-12.1` 的「某次提交/本轮」作用域也缺（前端 `SCOPES` 只有 3 项）。
13. **FR-2.3 / FR-8.5 / FR-8.6 分支与 commit 的可视化只到一半**——分支树画的是 Pi entry 树而非分支图（无创建时间/diff 规模）；点 commit 只有元数据 sheet，`git-commit-sheet.tsx` 自己写明「取不到该 commit 的 diff」。
14. **FR-14.6 / 14.7 skill 无法被显式调用**——没有 `/skill:name`、没有 `/` 命令面板、没有内置 skill，skill 只能靠模型自己判断去读。
15. **FR-5.7 / 5.8 / 16.4 / 16.6 运维面整体缺失**——端口发现、资源护栏、镜像管理、护栏配置都没有端点；`admin/status` 只有计数，没有水位与镜像告警。
16. **FR-4.3 / 4.4 sidecar 引用与独立模型**——引用卡（`@main:msg_xxx`）不存在；独立模型 API 支持但 UI 不提供。

## 与 `docs/STATUS.md` 的差异（自查表本身的问题）

- `FR-3.4 多窗格` 记为「完成」——实际是多面板检查器（`PANELS = changes/git/browser/terminal`），不是 2–4 个分支的实时流，`25-split.spec.ts` 也只验证两个检查器面板并排。
- `FR-4.6 多个 sidecar` 记为「完成」——后端有 3 个上限与单测，但 UI 只有一个 dock，没有 tab 切换。
- `FR-1.5` 记为「完成」——缺搜索；`FR-2.4` 记为「完成」——删除分支不清理 worktree（`RemoveWorktree` 无调用者），也从未写 Pi 的 `label` entry。
- `FR-2.7 clone`（2e5f206）与 `FR-2.6 采纳`（85ceaba）已在审计之后落地，两行都已更新。
- STATUS.md 完全没有覆盖 FR-5.x、FR-9.x、FR-10.x、FR-12.x、FR-14.x、FR-15.x 的大部分行，这些正是 MISSING 最集中的区域。
