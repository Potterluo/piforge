# 实现状态（对照 PRD）

这份表说明**代码里到底有什么**，而不是设计意图。每一行都必须能在仓库里找到证据：
路由、handler、组件或测试名。写这份表的原因是上一轮我说过“P2 全部落地”，
那是错的——P2 的最后一项确实做完了，但 P1 里还有几条从未实现。

状态含义：

- **完成**：实现 + 至少一条测试（单测或 e2e）覆盖。
- **部分**：实现了，但缺测试，或只做了 API / 只做了界面。
- **未做**：没有对应实现。

## 会话（FR-1）

| 需求 | 状态 | 证据 |
|---|---|---|
| FR-1.1 新建会话 | 完成 | `POST /v1/projects/{id}/sessions`、`components/shell/new-session-dialog.tsx`、`e2e/tests/01-auth.spec.ts` |
| FR-1.2 会话流就地折叠 | 完成 | `components/session/message-item.tsx`、`reduce.ts`、`e2e/tests/02-conversation.spec.ts` |
| FR-1.3 双队列输入（follow-up / steer） | 完成 | `composer.tsx`、`handlePostMessage` 的 `streaming_behavior`、`02-conversation.spec.ts` |
| FR-1.4 `Esc` 清队列 + 回填 | 完成 | `composer.tsx`、`handleInterrupt`、`02-conversation.spec.ts` |
| FR-1.5 列表搜索/排序/状态徽章 | 完成 | `app-sidebar.tsx`、`StatusDot` |
| FR-1.6 断线续传 | 完成 | `GET /v1/sessions/{id}/events`（`Last-Event-ID`）、`use-session-events.ts`、`13-live-refresh.spec.ts` |
| FR-1.7 重命名/归档/删除/导出 | 完成 | `PATCH/DELETE /v1/sessions/{id}`、`GET …/export`、`14-session-actions.spec.ts` |
| FR-1.8 上下文用量 | 完成 | `GET …/stats`、`workbench.tsx` 的窗口占用 |
| FR-1.9 压缩可见 | 完成 | `POST …/compact`、`05-branches.spec.ts`（compaction 用例） |
| FR-1.10 临时会话 | 完成 | `ephemeral` 列 + sweeper、`23-ephemeral.spec.ts` |

## 分支（FR-2）—— 这里有缺口

| 需求 | 状态 | 证据 |
|---|---|---|
| FR-2.1 任意用户消息处“从这里分叉” | 完成 | `GET …/fork-points`、`POST …/fork`、`18-branching.spec.ts` |
| FR-2.2 分叉建新 session + 新 worktree | 完成 | `handleFork` → `provisionSession` |
| FR-2.3 分支树视图 | 部分 | `components/session/branch-tree.tsx`（展示条目与父链，但**不是**按分支对比的图） |
| FR-2.4 分支命名/归档/删除 | 完成 | `PATCH`、`kind=branch`、`14-session-actions.spec.ts` |
| FR-2.5 对比视图（2–3 分支并排） | 未做 | 无路由、无组件 |
| FR-2.6 采纳（cherry-pick / 整树覆盖回主干） | 未做 | 无路由、无组件 |
| FR-2.7 clone 当前分支 | 完成 | `POST /v1/sessions/{id}/clone`、侧边栏「克隆（再试一次）」、`27-clone.spec.ts` |

## 并行执行（FR-3）

| 需求 | 状态 | 证据 |
|---|---|---|
| FR-3.1 同一主会话只有一个分支在执行 | 未做 | 没有任何“切走前 pause”的逻辑 |
| FR-3.2 显式并行运行 2–3 分支 | 未做 | 每个会话有独立进程，但没有“并行”这层语义与开关 |
| FR-3.3 并行上限 + 排队原因 | 未做 | 无上限、无排队 |
| FR-3.4 多窗格 | 完成 | `workspace-panels.tsx` 的 split、`25-split.spec.ts` |

## Sidecar（FR-4）

| 需求 | 状态 | 证据 |
|---|---|---|
| FR-4.1 旁路对话、共享 worktree | 完成 | `POST …/sidecar`、`05-branches.spec.ts` |
| FR-4.5 提权（只读工具） | 部分 | `sessionSpec` 里 `ReadOnlyTools: kind == sidecar`，但**没有**用户可见的提权操作；也没测试 |
| FR-4.6 多个 sidecar 并存 | 完成 | 每条 sidecar 是一个会话，列表与侧边栏都支持多个 |
| FR-4.7 提升为分支（带上下文） | 完成 | `POST …/promote` + `CloneSession`、`21-promote.spec.ts` |

## 工作区与文件（FR-6 / FR-7）

| 需求 | 状态 | 证据 |
|---|---|---|
| FR-6.4 快速打开（⌘P） | 完成 | `quick-open.tsx`、`15-quick-open-status.spec.ts` |
| FR-6.x 全局搜索 | 完成 | `GET …/search` |
| FR-7.x 文件树 / 预览 / 读写 / 下载 | 完成 | `GET/PUT/DELETE …/files`、`19-preview.spec.ts`、`11-files.spec.ts` |

## 浏览器（FR-11）

| 需求 | 状态 | 证据 |
|---|---|---|
| FR-11.1 实时画面 + 输入 | 完成 | `08-browser.spec.ts` |
| FR-11.7 多标签 | 完成 | `tabs.go`、`26-browser-tabs.spec.ts` |
| FR-11.8 截图入工作区 | 完成 | `20-screenshot.spec.ts` |
| FR-11.9 profile 持久化 | 完成（有边界） | 每 sandbox 一个 profile 目录；跨浏览器重启的 cookie 存活取决于 Chromium，不作承诺（见 `profile_test.go` 注释） |

## 权限、运维、平台（FR-8 / FR-13 / FR-16）

| 需求 | 状态 | 证据 |
|---|---|---|
| FR-8.x 项目/会话归属、跨用户 404 | 完成 | 所有 handler 走 `Store.*(userID, …)`；`support_test.go` 等 |
| FR-13.9 用户 API Key | 完成 | `api_keys.go`、`24-api-keys.spec.ts` |
| FR-16.7 功能开关 | 完成 | `17-feature-flags.spec.ts` |
| FR-16.8 只读排障（30 分钟授权 + 回执） | 完成 | `16-support-mode.spec.ts` |
| FR-5.10 agent 日志 | 完成 | `GET …/logs`、`22-logs.spec.ts` |

## 结论

未完成项集中在**分支对比与合并**（FR-2.5 / 2.6）和**并行执行控制**（FR-3.1 / 3.2 / 3.3），
另有 FR-4.5 的提权操作缺界面。这些都不是“打磨”，而是 PRD 里明确写着的能力。
