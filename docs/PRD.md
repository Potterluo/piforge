# PiForge · 基于 Pi-Agent 的多用户 Web Coding Agent 工作台

**产品需求文档（PRD）+ 技术架构设计**

| 项目 | 内容 |
|---|---|
| 文档版本 | **v2.0**（按 v2 需求重写） |
| 日期 | 2026-10-27 |
| 定位 | **内部提效工具**：每个用户一个"私享 coding agent"，纯 Web，不做治理 |
| 形态 | Web（浏览器内完成全部工作），后端 Go，前端 Next.js + shadcn/ui |
| Agent 内核 | [Pi Coding Agent](https://github.com/earendil-works/pi)（`pi --mode rpc`，JSONL） |
| 设计参考 | [fastclaw](https://github.com/fastclaw-ai/fastclaw)（BYOK + 配置继承）、[nanobot](https://github.com/HKUDS/nanobot)（workbench 多窗格）、[umeko-desktop](https://github.com/umeiko/umeko-desktop)（会话/项目侧栏） |

> **一句话定义**
> 打开浏览器就有一个自己的 coding agent：有文件树、有终端、有浏览器、能跑能改能测；会话可以分叉、可以多开、可以挂 sidecar。用户不需要知道底下是共享的还是独占的。

---

## 0. v2.0 相对 v1.0 的变更（请先核对）

### 0.1 按你的决定删掉的

| v1.0 内容 | 处理 | 理由 |
|---|---|---|
| 组织 / 团队 / 项目 四层租户 + 三条继承链 | **删除**，降级为 `user` 单层 | 每个人看到就是一个普通 coding agent |
| 策略引擎 + 决策追踪 + 模拟器 | **删除** | 不做治理 |
| 审批收件箱 + 审批流 | **删除** | 提效工具不需要审批仪式 |
| 审计账本 / 哈希链 / 审计检索页 | **删除** | 不做合规功能 |
| 计费 / 配额 / 公平调度 / showback | **删除**（仅保留"防打挂"的资源护栏） | 无需计费 |
| 能力包（AgentProfile） | **删除** | 无团队层 |
| 六种角色（owner/admin/maintainer/member/auditor/service） | **缩为 `user` / `admin`** | 按你的要求 |
| 本地 CLI 连平台 | **删除** | 纯 Web |
| stdio MCP 白名单限制 | **删除**，全放开 | 按你的决定 |
| `(project, branch, user)` 沙箱复用 | **改为 `(project, user)`** | 按你的决定 |
| 项目级策略/审计 | **删除** | — |

### 0.2 按你的期待新增/加强的

| 新增能力 | 章节 |
|---|---|
| 文件树 + 文件预览（代码/MD/图片/PDF/CSV/HTML） | §5.3 |
| Git 树（commit DAG 可视化） | §5.3.4 |
| 实时终端跟随（只读镜像 agent 执行） | §5.4.1 |
| 可交互终端（独立 PTY） | §5.4.2 |
| 内置浏览器（agent 与人都能用，同一个 chromium） | §5.5 |
| 会话分叉 / 多分支会话（worktree 级隔离） | §5.1.3 |
| Sidecar 会话（附着式旁路对话） | §5.1.4 |
| 每用户自带模型（BYOK + admin 共享 inherit 覆盖） | §5.7 |
| 一人多 worktree 并行（低成本并行方案） | §6.4-B |

### 0.3 保留但重新定框的

- **多租户**：不再是"策略单元"，而改述为 **"独享错觉"**——用户体验到的是私有 agent，平台共享一切能共享的东西。§6 整章重写。
- **凭据零下沉**：仍然保留，但机制从"策略驱动"改为"**自动无感**"（登录发短期令牌，用户不用管）。§6.2
- **沙箱隔离**：仍然保留，但按每人一个长生命周期沙箱设计（而非每 run 一个）。§5.2

---

## 1. 背景与目标

### 1.1 为什么做

团队在用 Claude Code / Codex / Cursor，但遇到四个具体问题：

1. **环境绑机器**。`~/.claude`、MCP 配置、模型 key、装好的工具链都在个人笔记本上；换台机器就重来，新人一周起步。
2. **跑不动**。跑 40 分钟构建、跑 200 文件重构，笔记本发烫、断电就断。
3. **看不动**。终端里刷过的 diff、跑过的测试、改过的文件，滚过去就找不回来了。
4. **不能并线**。"我想同时试三种改法"——本地做不到，因为只有一个工作区。

这四条都不是"治理"问题，是**效率**问题。所以产品定位必须克制：

> **它是一个更好的 Coding Agent 工作台，不是一个研发管理平台。**

### 1.2 为什么基于 Pi

| Pi 提供的（不造） | 平台要补的 |
|---|---|
| Provider 归一化（OpenAI / Anthropic / 兼容端点） | 沙箱与工作区（Pi 明确不做 sandbox） |
| 有状态 tool loop + steering / follow-up + abort | 浏览器、终端、文件树、git 图（前端） |
| 有序事件流（`agent_start` … `agent_settled`） | 会话分叉 / 多分支 / sidecar 的产品化 |
| **append-only 树状 session**（JSONL，`id`/`parentId`） | 树的产品化（Pi 只有 CLI 的 `/tree`） |
| compaction（改 model view，不改历史） | 跨轮次的可视化（用量、压缩事件） |
| Skills（渐进披露） | 用户级 skill 库与一键安装 |
| MCP（stdio + HTTP + OAuth） | 每用户 MCP 定义与凭据托管 |
| Extension API（tool / hook / provider / MCP server） | 平台桥接扩展（工具转发、终端跟随） |

**关键判断**：Pi 的 session 本来就是一棵树，`fork` 命令原生存在。所以"会话分叉"对我们不是新功能，而是**把 Pi 已有的能力做成 UI**。这是本方案最大的杠杆。

### 1.3 目标与非目标

**目标（v2 结束）**

- G1 打开浏览器 5 秒内有一个可用的 agent 和工作区，不需要装任何东西。
- G2 用户在页面里能看到：文件树、git 图、文件预览、终端（跟随 + 交互）、浏览器、diff。
- G3 一个任务可以从任意点分叉出多条路径，并行跑，对比后择优；旁路问题用 sidecar 聊，不污染主线。
- G4 每个用户可以接自己的模型、自己的 MCP、自己的 skill。
- G5 关掉页面、换台电脑、第二天回来，工作区和会话都还在。

**非目标（明确不做）**

- ✗ 不做审计、审批、策略引擎、计费、多级 RBAC。
- ✗ 不做 CLI / 桌面客户端，纯 Web。
- ✗ 不做 LSP 级 IDE（不做定义跳转、重构、调试器）。
- ✗ 不做跨用户协作（共享会话、评论、@提及）——V2 之后再谈。
- ✗ 不重写 Agent Loop。

---

## 2. 调研结论（精简版）

### 2.1 Pi 的关键技术点（实现层面的硬事实）

**a) RPC 是唯一集成方式**

`pi --mode rpc --no-session` → stdin/stdout 上的 JSONL。四类记录：`command`（进）、`response`（可按 `id` 关联）、`session event`（流）、`extension ui record`（双向）。

四个必须记住的坑：

| 坑 | 事实 | 后果 |
|---|---|---|
| 分帧 | **严格 LF**。不能用 Node `readline`（会按 `U+2028`/`U+2029` 切，而这俩在 JSON 字符串里合法） | Go 用 `bufio.Reader.ReadBytes('\n')` 即可 |
| 流式 | `message_update` **剥离了累积快照 `partial`**，只有 `*_delta` | 必须自己累加，`text_end`/`toolcall_end` 权威替换，`message_end` 最终为准 |
| 结束信号 | **`agent_settled` 才是结束**，`agent_end` 不是（重试/压缩/steering 还会再起） | 用错会导致"以为完成了其实还在跑" |
| 背压 | stdout 必须持续读，否则管道满会卡死 Pi | 独立读协程 + 有界队列 |

**b) Session 树：分叉的底座**

JSONL 每条 entry 有 `id`/`parentId`。entry 类型：`message`、`model_change`、`thinking_level_change`、`usage`、`compaction`、`context_edit`、`branch_summary`、`custom`、`custom_message`、`label`、`session_info`。

RPC 里直接可用的树操作：

| 命令 | 作用 | 我们的 UI |
|---|---|---|
| `fork {entryId}` | 从某条用户消息分叉出新 session | "从这里分叉"按钮 |
| `clone` | 复制当前活跃分支到新 session | "复制这条分支" |
| `get_tree` | 返回整棵树 `{entry, children, label?}[]` | 分支树视图 |
| `get_entries {since}` | 树形游标增量拉取 | 断线续传 |
| `switch_session` | 切到另一个 session 文件 | 分支切换 |

> **重要限制**：一个 Pi 进程 = 一个 session 文件 = 一条活跃分支。所以"多分支并行"必须 **N 个 Pi 进程**。这直接决定了 §5.1.3 的架构。

**c) Skills 就是文件**

启动时只把 `name` + `description` + `path` 放进 system prompt，模型判断相关才 `read` 完整 `SKILL.md`。发现路径含 `~/.agents/skills/`、`.agents/skills/`、`.pi/skills/`。

**含义**：用户级 skill = 挂载目录。不需要新协议，只需要一个"内容寻址 + 挂载"的存储。

**d) MCP 已经够用**

- 传输：stdio（`command`/`args`/`env`/`cwd`）、streamable HTTP（`url`/`headers`/`oauth`），**不支持 SSE**。
- 工具名：`mcp__<server>__<tool>`。
- 曝光：`codemode`（默认，脚本调用）/ `deferred`（tool search）/ `direct`（直接声明）/ `hidden`。
- OAuth 回调是 `http://127.0.0.1:<port>/callback`——**在云端沙箱里用户浏览器够不到**，必须由控制面代办（§6.2-C）。

**e) Extension 是注入平台能力的唯一入口**

`pi.registerTool()` 注册模型可调用工具；`tool_call` hook 返回 `{block:true, reason}` 拦截；`tool_result` hook 改写/脱敏；`before_agent_start` 改 system prompt 的结构化 section；`ctx.executeTool()` 嵌套调用。

工具的 MCP 语义注解（`readOnlyHint` / `destructiveHint` / `idempotentHint` / `openWorldHint`）在"不做审批"的前提下仍然有用——**用于给工具自动分级打标签**（只读工具在 UI 上给不同颜色，而不是弹窗）。

### 2.2 fastclaw 给了什么（你点名参照的部分）

| fastclaw 设计 | 我们的用法 |
|---|---|
| **单二进制 + 逻辑全在 DB**（无配置文件） | 所有用户配置（provider / MCP / skill / 偏好）都在 Postgres，不做 YAML |
| **作用域继承 + overlay**：非管理员看到 `Inherited` 资源并可叠加私有同名覆盖 | **§5.7 的核心**：admin 配的共享 provider = `Inherited`；用户可加自己的 = overlay |
| **API key 三级**（admin / user / agent） | 简化成两级：`admin`（全局）/ `user`（自己的资源） |
| **`forkFrom` 克隆 agent 身份** | 用于"新用户默认环境"：admin 发布一个基础模板，用户首次登录 fork 一份自己改 |
| **sandbox 在 hosted 是边界、自托管是工具** | 我们固定当**边界**用（因为是共享的多人环境） |
| **SQLite 默认 / Postgres 多副本** | 直接 Postgres |
| **`fastclaw agents config <name> set k v`** | 我们只做 Web UI，不做 CLI |

> 特别值得抄的一条：fastclaw 把"系统级 provider"与"用户级 provider"做成同一张 `configs` 表的 `scope` 字段，用名字 shadow。**这比 `admin_config` + `user_config` 两张表干净得多**，我们照抄。

### 2.3 前端：shadcn 已原生覆盖，只准 5 个例外

shadcn 官方提供的 agent 原语（`npx shadcn@latest add <name>`）：

| 原语 | 用途 |
|---|---|
| `MessageScroller` | **流式聊天滚动容器**（16 条原则已内置，禁止自己写 stick-to-bottom） |
| `Message` / `MessageGroup` / `Bubble` | 消息与气泡 |
| `Trace` | 工具调用 / 思考 / 计划 / sidecar 时间线 |
| `Code Block` | Shiki 高亮；`variant: added/removed` 即 unified diff；`language="ansi"` 渲染终端色 |
| `Attachment` | 拖入文件/截图 |
| `Marker` / `Empty` / `Item` / `Kbd` / `Field` / `Input Group` / `Spinner` / `Questionnaire` | 状态行 / 空态 / 列表 / 快捷键 / 表单 / composer / 加载 / 澄清提问 |
| `Sidebar`(sidebar-07/11/13) / `Resizable` / `Sheet` / `Command` / `Tabs` / `ScrollArea` / `Collapsible` | 应用外壳 |
| `Data Table` + TanStack Table / `Chart` / `Dialog` / `AlertDialog` / `Switch` / `Select` / `Progress` / `Skeleton` | 管理页与面板 |

另有 20 个 **AI Elements 模式**（复制即用）：`Checkpoint`、`Confirmation`、`Context`、`File Tree`、`Model Selector`、`Plan`、`Prompt Input`、`Queue`、`Reasoning`、`Stack Trace`、`Subagent`、`Terminal`、`Test Results`、`Tool` 等。

**允许引入的 5 类非 shadcn 依赖**（v1 是 4 类，v2 新增 PDF）：

| 缺口 | 选型 | 备注 |
|---|---|---|
| 真 PTY 终端 | `@xterm/xterm` + `addon-fit` + `addon-webgl` + `addon-search` | 服务端 PTY 在沙箱内 |
| 可编辑代码 | **CodeMirror 6**（`@codemirror/*`） | 优于 Monaco：体积、tree-shake、移动端 |
| split diff / 词级高亮 | `@git-diff-view/react` | Shiki 驱动 |
| 大仓文件树 | `react-arborist` | 虚拟化 + 键盘 + 拖拽 |
| PDF 预览 | `pdfjs-dist` | 只渲染 canvas |

**Git 图**：没有合适的库（`@gitgraph/js` 风格不可控，`react-flow` 过重）。**自绘 SVG**，只写 lane 布局算法（~300 行），视觉全部用 shadcn token。见 §10.6。

**ElevenLabs UI**：对纯文本 agent 只有 `response`（streamdown 封装）和 `shimmering-text` 的思路值得借；语音组件全跳过。它真正的价值是**证明"私有 shadcn registry"这条路可行**（§10.7）。

### 2.4 竞品里我们直接抄的交互（不发明）

| 交互 | 抄谁 | 具体行为 |
|---|---|---|
| 文件树 | Cursor / VS Code | 虚拟化 + 折叠 + git 状态色（M 改 / A 增 / D 删 / U 未跟踪） |
| 文件预览 | GitHub / Cursor | 点文件即预览，不弹窗；`⌘P` 快速打开 |
| 终端 | Cursor / VS Code | 多 tab、可交互、可 resize、可分离到下方/右侧 |
| **终端跟随** | Devin / Claude Code web | agent 执行命令时，终端自动切到该 tab 并跟随输出（只读） |
| 内置浏览器 | Browserbase / Devin / Cursor Browser | 画面投屏 + 接管操作；agent 可用 Playwright 驱动同一个实例 |
| Git 图 | GitKraken / Fork | 分栏 commit DAG、ref 标签、点击看 diff |
| 分叉对比 | 无（我们做） | 并排 diff + 测试结果 + 成本 |
| Sidecar | 无标准（Cursor 的 "@ 另一个 chat" 有点像） | 旁路对话，共享工作区、独立上下文 |

---

## 3. 用户与角色

只有两种角色。

| 角色 | 能做什么 | 看不到什么 |
|---|---|---|
| **user** | 全功能使用自己的会话、工作区、模型、MCP、skill | 别人的任何东西 |
| **admin** | user 的全部 + 用户管理 + 共享模型配置 + 镜像/资源 + 全局运行状态 | **默认看不到别人的会话内容**（支持模式需显式开启且被记录） |

**admin 不是 superuser**。这条很重要：否则"提效工具"会变成"监控工具"，用户不会真心用。管理员需要的是"容量与健康"，不是"读别人代码"。

**为什么只要两种角色**：任何第三种角色都会带来"这个人为啥看不到那个"的问题，而这些问题在没有审计/审批体系时无法被安全地管理。**两种角色 + 强隔离 = 简单且安全**。

---

## 4. 用户场景

**JS-1 修 bug（最高频）**
> 贴一段报错，让 agent 在仓库里定位、改、跑测试、给我 diff。我不想装环境。

路径：新建会话（选仓库 + 分支）→ 粘贴 → 看 Trace 里读文件/改文件/跑测试 → 打开 diff → 逐 hunk 看 → 提交。

**JS-2 并行试三种改法**
> 这个缓存层有三种接法，我不想只赌一种。

路径：在会话某轮点 **"从这里分叉 ×3"** → 三条分支各自实现（独立 worktree、独立终端）→ **对比视图**（并排 diff + 测试结果）→ 留一条，其余归档。

**JS-3 边跑边问**
> agent 正在跑长任务，我有个不相关的小问题想问，但不想打断它。

路径：点 **"打开 Sidecar"** → 右侧挂一个小对话，共享工作区、独立上下文、可以用另一个（更便宜的）模型 → 问完关掉，主线完全不受影响。

**JS-4 手动接管**
> agent 改完了，但我想自己手动跑一遍、调一下。

路径：切到**可交互终端** → 直接敲命令 → 同时 agent 那边 resume 时用的是同一份文件系统。

**JS-5 看一眼页面效果**
> agent 起了个 dev server，我要看渲染对不对。

路径：**内置浏览器**里打开 `localhost:3000`（自动发现沙箱内端口）→ 观察模式看 agent 点；不满意点"接管"自己操作。

**JS-6 昨天那事继续**
> 昨天那个会话关掉了，今天接着改。

路径：登录 → 会话列表 → 打开 → 工作区还在（沙箱已暂停，点一下唤醒）→ 文件、终端、diff 全部原样。

**JS-7 换模型**
> 这个任务我想用自己的 key 和贵一点的模型。

路径：composer 底栏 `模型 ▾` → 选自己加的 provider/模型 → 只影响这个会话。

---

## 5. 功能需求

> 优先级：**P0** = 首个可用版本必须；**P1** = 第二个版本；**P2** = 后续。

### 5.1 会话

会话是主对象。三种形态共用一套消息渲染，但生命周期不同。

| 形态 | 定义 | 一个 Pi 进程？ | 一个 worktree？ |
|---|---|---|---|
| **主会话（main）** | 用户从零开始的一次任务 | 是 | 是 |
| **分支会话（branch）** | 从主会话某点 `fork` 出来的平行路径 | 是（独立进程） | 是（独立 worktree） |
| **Sidecar** | 附着在主会话上的旁路对话 | 是（独立进程） | 否，**共享**主会话的 worktree |

#### 5.1.1 主会话

| ID | 需求 | 优先级 |
|---|---|---|
| FR-1.1 | 新建会话：选仓库（或空白工作区）、基准分支、模型、推理强度 | P0 |
| FR-1.2 | 会话流：消息 / 思考 / 工具 / 结果 / diff 片段 / 计划，**全部就地折叠**，不弹窗 | P0 |
| FR-1.3 | 双队列输入：空闲 `Enter` 发送；运行中 `Enter` = 下一轮（follow-up），`⌘↵` = 立即打断改口（steer） | P0 |
| FR-1.4 | `Esc` = 清空队列 + abort，被清空的文本回填输入框（对齐 Pi `clear_queue` → `abort`） | P0 |
| FR-1.5 | 会话列表：搜索、按更新时间排序、状态徽章（运行中 / 待唤醒 / 已暂停 / 失败） | P0 |
| FR-1.6 | 断线续传：SSE `Last-Event-ID` + `get_entries?since=`，**不重放已见事件、不重复气泡** | P0 |
| FR-1.7 | 重命名、归档、删除、导出（Markdown / HTML） | P1 |
| FR-1.8 | 上下文用量：按轮的 in/out/cache + 当前窗口占用百分比 | P0 |
| FR-1.9 | 压缩可见：`compaction` 渲染为时间线事件，可展开看摘要与保留范围 | P1 |
| FR-1.10 | 临时会话（不写历史、关掉即焚） | P2 |

#### 5.1.2 会话分叉（Fork）

| ID | 需求 | 优先级 |
|---|---|---|
| FR-2.1 | 在任意**用户消息**上提供 "从这里分叉"（Pi 的 `fork` 只支持 user message 作为分叉点） | P0 |
| FR-2.2 | 分叉时创建：新 session + 新 worktree（`git worktree add`，从当前 HEAD） | P0 |
| FR-2.3 | **分支树视图**：图形化展示主干与各分支的父子关系、创建时间、状态、diff 规模 | P1 |
| FR-2.4 | 分支命名（写 Pi 的 `label` entry）、归档、删除（同时清理 worktree） | P1 |
| FR-2.5 | **对比视图**：任选 2–3 个分支，并排展示 diff + 测试结果 + token 成本 + 耗时 | P1 |
| FR-2.6 | "采纳"：把某分支的改动 cherry-pick / 整树覆盖回主干（带二次确认） | P1 |
| FR-2.7 | `clone`：复制当前分支（用于"在现有基础上再试一次"） | P1 |

**AC-2.2**：分叉 3 个分支后，三个 worktree 在文件系统上互不影响；在分支 A 里 `git status` 看不到分支 B 的改动。

#### 5.1.3 多分支会话（并行）

这是"分叉"的进阶：分支不仅能存在，还能**同时跑**。

| ID | 需求 | 优先级 |
|---|---|---|
| FR-3.1 | 默认：同一主会话下**只有一个分支在执行**（切到别的分支时，当前分支先 pause） | P0 |
| FR-3.2 | 显式"并行运行"：允许 2–3 个分支同时执行（每个一个 Pi 进程，独立 PTY） | P1 |
| FR-3.3 | 并行上限：默认 3，可在设置中调到 5；超限时排队并显示原因 | P1 |
| FR-3.4 | **多窗格 Workbench**：并排看 2–4 个分支的实时会话流（借 nanobot 的布局：main + supporting panes） | P2 |
| FR-3.5 | 分支级资源隔离：每个分支配自己的终端 tab、自己的浏览器 tab（cookie 共享 / 分离可选） | P1 |
| FR-3.6 | 冲突预警：两个分支改了同一个文件时，在切换/采纳前提示"文件级冲突" | P1 |

**为什么默认互斥**：同一份 worktree 里两个 agent 同时写文件 = 数据损坏。多 worktree 解决了目录冲突，但**依赖缓存是共享的**（`$GOMODCACHE`、`.npm/_cacache`），并发写入同一缓存仍有风险。所以默认互斥，并行是显式选项 + 上限。

#### 5.1.4 Sidecar 会话

这是我认为最值钱的一个小功能。

**定义**：附着在主会话上的**旁路对话**。共享主会话的工作区与沙箱，但有自己的 transcript、自己的上下文、自己的模型。

**与子 Agent 的区别**：

| | Sidecar | 子 Agent |
|---|---|---|
| 谁驱动 | **人**（你对它说话） | agent（主会话派发） |
| 上下文 | 独立，**显式引用**主线内容 | 独立，自动回摘要 |
| 工具 | 默认只读（`read`/`grep`/`glob`/`bash` 只读命令） | 按定义 |
| 生命周期 | 跟着主会话，可长期挂着 | 一次任务结束即止 |
| 用途 | "我就问一句"、"帮我解释这段"、"这条命令啥意思" | 并行探索、独立验证 |

| ID | 需求 | 优先级 |
|---|---|---|
| FR-4.1 | 打开/收起 Sidecar：右侧 docked 面板（可拖宽、可全屏），或底部 Sheet（窄屏） | P1 |
| FR-4.2 | 共享工作区：同一个 sandbox、同一个 worktree，**默认只读工具集** | P1 |
| FR-4.3 | **显式引用**：把主会话的某条消息 / 某个 diff / 某个文件拖进 sidecar 作为附件（`@main:msg_xxx`），**不共享整个上下文** | P1 |
| FR-4.4 | 独立模型：sidecar 可指定更便宜/更快的模型（如主会话用旗舰，sidecar 用轻量） | P1 |
| FR-4.5 | 提权开关："允许 sidecar 修改文件"（默认关，开启后与主会话串行执行，避免抢文件） | P2 |
| FR-4.6 | 多个 sidecar：最多 3 个，用 tab 切换 | P2 |
| FR-4.7 | "提升为分支"：把一个 sidecar 变成正式分支会话（带上它的上下文） | P2 |

**命名理由**：sidecar 是摩托车边车——挂在主车上、共享动力、能坐人、不是主车的一部分。这个名字比 "副会话" 或 "子对话" 都准确。

**AC-4.2**：sidecar 里让 agent `rm -rf` 会被工具集直接拦住（工具不在白名单里，模型看不到该工具），不是靠提示词。

### 5.2 工作区与沙箱

**核心决策（按你的选择）：沙箱复用粒度 = `(project, user)`。**

一个用户在同一个项目上有一个**长生命周期沙箱**。沙箱内可以有多个 worktree（对应多个分支）。

```
用户 A
 └── 沙箱（sandbox，长生命周期，可暂停）
      ├── 镜像：tenant-base + A 的技术栈
      ├── 共享缓存：$GOMODCACHE / .npm/_cacache / $CARGO_HOME（同一用户内共享）
      └── worktrees/
           ├── main/          ← 主会话（Pi 进程 1，PTY 1）
           ├── fix-cache/     ← 分支 1（Pi 进程 2，PTY 2）
           └── alt-approach/  ← 分支 2（Pi 进程 3，PTY 3）
```

| ID | 需求 | 优先级 |
|---|---|---|
| FR-5.1 | 沙箱状态机：`CREATING → STARTING → READY → BUSY → IDLE → PAUSED → TERMINATED`（+ `ERROR`/`MISSING`）。**UI 显示状态与倒计时** | P0 |
| FR-5.2 | 自动唤醒：打开会话时若沙箱 `PAUSED`，自动唤醒并显示进度（目标 ≤ 3s，从快照恢复） | P0 |
| FR-5.3 | 空闲策略：`idle 15min → 快照 + PAUSED`；`PAUSED 14d → 回收（保留快照）`；**先快照后销毁，永不先杀** | P0 |
| FR-5.4 | 多 worktree 管理：创建（`git worktree add`）、列出、清理；每个 worktree 绑定一个会话 | P0 |
| FR-5.5 | 依赖缓存共享：同一用户跨 worktree 共享只读/读写缓存，消除重复 `npm install` | P1 |
| FR-5.6 | 快照：`filesystem` 与 `filesystem+memory` 两档；**只在工具边界触发**（不在 `bash` 执行中） | P1 |
| FR-5.7 | 端口发现：自动列出沙箱内监听端口（`ss -ltnp`），一键在浏览器面板打开 | P1 |
| FR-5.8 | 资源护栏（非计费）：单用户并发沙箱 ≤ 2，单沙箱 CPU ≤ 4 核 / 内存 ≤ 8G / 磁盘 ≤ 40G；超限拒绝并说明 | P0 |
| FR-5.9 | 手动操作：暂停 / 唤醒 / 快照 / 重建沙箱 | P1 |
| FR-5.10 | 沙箱日志：容器启动日志、镜像构建日志可查看（排障用） | P2 |

**AC-5.2**：会话页打开时若沙箱在 `PAUSED`，首屏消息立即可见（从 DB 读），沙箱状态条显示"唤醒中 1.2s"，唤醒完成后终端/文件树才可用。**不允许"白屏等沙箱"**。

### 5.3 代码浏览（新增重点）

#### 5.3.1 文件树

| ID | 需求 | 优先级 |
|---|---|---|
| FR-6.1 | 虚拟化文件树（`react-arborist`），支持展开/折叠/搜索/新建/重命名/删除 | P0 |
| FR-6.2 | **Git 状态色**：已修改(M)/新增(A)/删除(D)/未跟踪(U)/冲突(!)，与 diff 联动 | P0 |
| FR-6.3 | 尊重 `.gitignore`，可切换"显示忽略文件" | P0 |
| FR-6.4 | `⌘P` 快速打开（模糊搜索文件名，走 `rg --files` / `fd`） | P0 |
| FR-6.5 | 右键菜单：在终端中打开、复制路径、下载、对比（与 HEAD） | P1 |
| FR-6.6 | 文件搜索（内容）：`⌘⇧F`，走 `rg`，结果可点击跳转行 | P1 |

#### 5.3.2 文件预览

| ID | 需求 | 优先级 |
|---|---|---|
| FR-7.1 | 代码：Shiki 高亮 + 行号 + 折叠 + 可切换只读/编辑（CodeMirror 6） | P0 |
| FR-7.2 | Markdown：渲染（`streamdown`）+ 源码切换 | P0 |
| FR-7.3 | 图片：png/jpg/svg/webp/gif，支持缩放与平移 | P0 |
| FR-7.4 | PDF：`pdfjs-dist` canvas 渲染 + 翻页 | P1 |
| FR-7.5 | CSV/TSV：`Data Table` 渲染，可切换原始文本 | P1 |
| FR-7.6 | HTML：`iframe sandbox` 预览，或走预览域名（若含 JS） | P1 |
| FR-7.7 | 二进制：下载卡片 + 大小/类型 | P1 |
| FR-7.8 | 大文件保护：> 2MB 只显示首尾各 2000 行 + "下载完整文件"；> 20MB 只给下载 | P0 |
| FR-7.9 | Diff 视图：单文件 `@git-diff-view`，unified / split 切换 | P0 |
| FR-7.10 | 编辑保存：写回沙箱（记录"人工编辑"事件，让 agent 下一轮知道文件变了） | P1 |

**AC-7.3（图片）**：在沙箱里生成一张图（agent 或用户），文件树里点开立即预览，支持滚轮缩放 / 拖拽平移 / 点击关闭。

#### 5.3.3 Git 面板

| ID | 需求 | 优先级 |
|---|---|---|
| FR-8.1 | 当前分支、ahead/behind、工作区状态（干净/有改动） | P0 |
| FR-8.2 | 变更列表：按 staged / unstaged / untracked 分组 | P0 |
| FR-8.3 | 提交：message 输入 + 只提交选中文件 + " amend 上一次" | P1 |
| FR-8.4 | 分支操作：新建 / 切换 / 删除 / 合并 / rebase（冲突时给出明确指引） | P1 |
| FR-8.5 | 历史：commit 列表（作者/时间/message）+ 点击看该 commit 的 diff | P1 |
| FR-8.6 | **Git 树（DAG）**：分栏 commit 图，ref 标签（分支/tag/HEAD），merge 连线，点击看详情 | P1 |
| FR-8.7 | 与分支会话联动：每个 worktree 的分支在图上高亮，"这条分支是谁跑的" | P1 |
| FR-8.8 | 远端操作：fetch / pull / push（**凭据由代理注入**）；"开 PR" 走 GitHub/GitLab API | P1 |

#### 5.3.4 Git 图（DAG）的实现要点

没有合适的成熟库，自绘 SVG：

```ts
// 1) 取数据：一条命令拿到父子关系
//    git log --all --date-order --parents --pretty=format:'%H%x09%P%x09%an%x09%at%x09%s%x09%D'
// 2) lane 分配（贪心）：
//    对每个 commit，若其某个父 commit 已在某 lane，则占用该 lane；
//    否则占用最小空闲 lane（从右往左让新增分支靠右，符合 GitKraken 观感）
// 3) 边：子 → 父，若 lane 变化用 cubic bezier 连接
// 4) 虚拟化：只渲染视口内 ±20 个 commit
```

渲染规格：
- 行高 28px；lane 宽 18px；节点半径 4.5px
- ref 标签用 shadcn `Badge`（`variant="secondary"` 分支 / `outline` tag / `default` HEAD）
- hover 出 `HoverCard`（完整 hash、作者、时间、改动文件数）
- 点击 → 右侧 Sheet 展示该 commit 的 diff
- 性能目标：5000 个 commit 首屏 ≤ 300ms（虚拟化 + 分层渲染）

### 5.4 终端（新增重点）

两个**不同**的终端，别混：

#### 5.4.1 跟随终端（Follow / 只读）

**定义**：镜像 agent 正在执行的 `bash` 工具输出。用户看着它跑，不介入。

| ID | 需求 | 优先级 |
|---|---|---|
| FR-9.1 | 数据源：Pi 的 `tool_execution_start/_update/_end` 事件中的 `partialResult` / `result` | P0 |
| FR-9.2 | 自动跟随：agent 开始执行命令时，终端自动切到该 tab 并滚动到底（可关） | P0 |
| FR-9.3 | 只读渲染：`Code Block language="ansi"` 或 xterm 只读模式 | P0 |
| FR-9.4 | 命令重放：历史命令列表，点击可回看那次输出（不用往上翻会话流） | P1 |
| FR-9.5 | 长输出：> 5000 行折叠，"查看完整输出"（`fullOutputPath` 指向沙箱内日志文件） | P1 |
| FR-9.6 | 多路跟随：并行分支时，每个分支一个跟随 tab | P1 |

> **诚实的技术说明**：Pi 的 `bash` 工具是在 Pi 进程内 spawn shell 的，我们没有它的 TTY 句柄。所以跟随终端的数据来自**官方事件流**（`partialResult` 是工具主动上报的增量），不是伪造的字节流。
> 如果需要**字节级**跟随，方案 B：平台 extension 覆盖 shell 环境，在沙箱内用 `script -f` 或自建 shim 旁路 tee 到文件，前端 tail 该文件。**这是 P2 的可选增强，V1 不做**——因为事件流已经够用，而 shim 会引入"agent 的 shell 和用户的 shell 不一致"的新问题。

#### 5.4.2 交互终端（Interactive）

**定义**：用户自己的 shell，独立于 agent。可以跑命令、调环境、看进程。

| ID | 需求 | 优先级 |
|---|---|---|
| FR-10.1 | 完整 PTY（沙箱内 `tmux` 或自建 pty），`@xterm/xterm` 渲染，支持 TUI 程序（vim/htop/top） | P0 |
| FR-10.2 | 多 tab（≥ 5），每个 tab 独立 PTY，可重命名、可关闭 | P0 |
| FR-10.3 | 断线重连：按 `(sandbox_id, pty_id)` + 字节偏移恢复，**断开 60s 内不丢输出** | P0 |
| FR-10.4 | 缩放手势 + 自适应（`addon-fit`），窗口 resize 时同步 `TIOCSWINSZ` | P0 |
| FR-10.5 | 复制粘贴、搜索（`addon-search`）、清屏、字号调节 | P1 |
| FR-10.6 | 布局：右侧面板 / 底部面板 / 全屏，可拖拽分割 | P1 |
| FR-10.7 | 沙箱暂停时终端变灰只读 + 提示"唤醒后恢复"；唤醒后**自动重连并回放 ring buffer** | P1 |
| FR-10.8 | 与 agent 共享文件系统（这是重点：用户在终端 `git checkout` 后，agent 下一轮看到的就是新状态） | P0 |

**关键设计：PTY master 必须在沙箱内。**

```
沙箱内：tmux server（master 端）
         └─ session "user-1" / "user-2" / "agent-1"
控制面：只做字节中继（WebSocket ↔ tmux attach 的 stdio）
```

如果 master 在控制面，一次快照/恢复就把所有终端连接永久切断；而且沙箱暂停后 tmux 里的进程状态就丢了。**这是必须做对的一条。**

**AC-10.3**：在终端里跑 `ping` 循环 → 断开网络 30s → 恢复 → 输出连续、无重复行、无丢失（服务端 ring buffer ≥ 2MB/PTY）。

### 5.5 内置浏览器（新增重点）

**定义**：沙箱内跑一个 headless Chromium，画面投屏到页面，人和 agent 共享同一个实例。

```
┌──────────────────── 沙箱 ────────────────────┐
│  chromium --headless=new                      │
│    --remote-debugging-port=9222               │
│    --user-data-dir=/home/dev/.browser-profile │
│         ▲                    ▲                │
│         │ CDP                │ CDP            │
│  ┌──────┴──────┐      ┌──────┴───────┐        │
│  │ Playwright  │      │ 投屏/输入中继 │        │
│  │ MCP (agent) │      │  (人)         │        │
│  └─────────────┘      └──────┬───────┘        │
└──────────────────────────────┼────────────────┘
                               │ WS（JPEG frames + 输入事件）
                        控制面（鉴权 + 路由）
                               │
                          前端 Canvas
```

| ID | 需求 | 优先级 |
|---|---|---|
| FR-11.1 | 画面投屏：CDP `Page.startScreencast` → JPEG 帧 → WS → 前端 canvas 绘制 | P1 |
| FR-11.2 | **观察模式**（默认）：agent 操作时实时可见，用户不可输入 | P1 |
| FR-11.3 | **接管模式**：用户可点击/输入/滚动（CDP `Input.dispatch*`）；接管时 agent 的浏览器工具被暂停 | P1 |
| FR-11.4 | 模式互斥与提示：切换必须显式确认，界面上有明确的"谁在控制"标识 | P1 |
| FR-11.5 | 地址栏 + 前进/后退/刷新；`localhost:<port>` 快捷入口（自动列出沙箱内端口） | P1 |
| FR-11.6 | agent 侧：通过 **Playwright MCP**（stdio，沙箱内）驱动同一 chromium | P1 |
| FR-11.7 | 多 tab：≥ 3 个页面，tab 条 + 缩略图 | P2 |
| FR-11.8 | 截图/下载：一键截图保存到 workspace；下载文件落到 workspace 供 agent 使用 | P2 |
| FR-11.9 | profile 持久化：cookie/localStorage 存在 sandbox 根目录的 `.piforge-browser` 内，同一 sandbox 的所有会话共用一个浏览器与一份登录态；**跨控制平面重启的 cookie 存活取决于 Chromium 的加密与懒写入，不承诺** | P2 ✅ |

**为什么用 Playwright MCP 而不是自建浏览器工具**：你已确认 stdio MCP 全放开，而 Playwright MCP 是现成的、能力完整的（导航/点击/填表/截图/网络拦截/多页），让 agent 用它比我们造一个更好。**内置浏览器只是"给同一实例加一面屏幕"**，这是这个设计最优雅的地方。

**多用户隔离**：browser 实例必须在用户自己的沙箱内，`--user-data-dir` 在 workspace 内。**绝不能做共享浏览器池**——cookie 会串。

### 5.6 Diff 与提交

| ID | 需求 | 优先级 |
|---|---|---|
| FR-12.1 | Diff 作用域：`未暂存` / `已暂存` / `某次提交` / `分支 vs 基准` / `本轮` | P0 |
| FR-12.2 | 逐文件 + 逐 hunk：stage / unstage / revert；unified / split 切换 | P1 |
| FR-12.3 | 行内评论 → **作为下一轮 steer 消息发给 agent**（不是代码修改） | P1 |
| FR-12.4 | 变更概览：增删行数 + 文件树 + 风险标记（改了 lockfile / CI 配置要醒目） | P1 |
| FR-12.5 | 测试结果面板：解析常见输出，展示 pass/fail 与失败堆栈 | P1 |
| FR-12.6 | 一键开 PR（GitHub/GitLab），标题/描述自动草稿；**凭据由代理注入** | P1 |
| FR-12.7 | 产物交付：任意文件可"present"为下载/预览卡（图片、文档、构建产物） | P1 |

### 5.7 模型与凭据（BYOK，参考 fastclaw）

**这是与 v1 差别最大的一块**：从"平台池化 + 配额"改为 **"每人自带 + admin 共享兜底"**。

#### 5.7.1 三层来源

```
① admin 共享 provider（Inherited，只读，用户可见但不可改）
      └─ ② 用户私有 provider（overlay，同名时 shadow 掉 ①）
              └─ ③ 会话级临时覆盖（只影响这个会话）
```

这正是 fastclaw 的 `scope` + shadow 模式。

| ID | 需求 | 优先级 |
|---|---|---|
| FR-13.1 | admin 可配共享 provider（名称、base_url、模型清单、key）；用户侧显示为 `Inherited` 徽章 | P0 |
| FR-13.2 | 用户可加自己的 provider（OpenAI 兼容 / Anthropic / 任意兼容端点），可覆盖同名共享项 | P0 |
| FR-13.3 | key 加密存 DB（AES-GCM + 主密钥），**永不出现在沙箱内明文** | P0 |
| FR-13.4 | 模型选择器：按 provider 分组，显示 context window / 是否支持 reasoning / 成本（若有） | P0 |
| FR-13.5 | 推理强度：随模型能力动态显示（对齐 Pi `get_available_thinking_levels`） | P0 |
| FR-13.6 | 会话级覆盖：composer 底栏改模型只影响当前会话 | P0 |
| FR-13.7 | 连通性测试：保存前"测试一下"（发一个 1 token 请求） | P1 |
| FR-13.8 | 用量显示：会话内 token 计数与估算成本（仅展示，不限额、不计费） | P1 |
| FR-13.9 | 用户 API Key：生成 `pf_xxx` 供自动化脚本调本平台 API（可选，P2） | P2 |

#### 5.7.2 凭据怎么用（关键机制）

**key 不进沙箱**：

```
沙箱内：ANTHROPIC_BASE_URL = https://llm-gw.internal/u/<user>/<session>
        ANTHROPIC_API_KEY  = "pf-ephemeral-<short-lived>"
                    │
                    ▼
            控制面 LLM Gateway
              ├ 校验 ephemeral token（归属 user + session + 过期）
              ├ 查该会话的有效 provider（③→②→①）
              ├ 替换成真实 key
              ├ 记账（token 数，仅展示用）
              └──▶ 上游 provider
```

好处（每一条都是用户能感知的）：
- 用户在页面上换模型，**不需要重启沙箱**（改的是网关路由，沙箱内只有 base_url）。
- key 泄漏风险降到最低（沙箱里那个 token 5 分钟过期、只能打网关）。
- 用户换 key 只改 DB 一行。
- 出问题能看"这次请求打到哪个 provider、返回什么错误"。

**成本**：多一跳。实测同区域内 +5~15ms 首字延迟，可接受；且能顺手做 prompt cache 复用。

### 5.8 Skill

| ID | 需求 | 优先级 |
|---|---|---|
| FR-14.1 | 两层作用域：`admin 共享` + `用户私有`（同名时用户覆盖） | P1 |
| FR-14.2 | 挂载：只读 bind mount 到 `<worktree>/.agents/skills/`，每个目录带 `content_hash` | P1 |
| FR-14.3 | Skill 中心：浏览、搜索、详情（渲染 `SKILL.md` + 文件树 + 脚本预览） | P1 |
| FR-14.4 | 安装/启用：一键从 Git URL 或 zip 导入；可禁用 | P1 |
| FR-14.5 | 会话记录所用版本（`skill@version` + hash），便于复现 | P1 |
| FR-14.6 | 调用：`/skill:name` 显式调用 + 出现在 `/` 命令面板 | P0 |
| FR-14.7 | 内置几个开箱可用的 skill（代码审查、提交信息、测试生成） | P1 |

### 5.9 MCP

**按你的决定：支持，不做任何限制。** stdio 与 HTTP 都放开，不设白名单。

| ID | 需求 | 优先级 |
|---|---|---|
| FR-15.1 | 用户级 MCP 定义：stdio（命令/参数/env/cwd）与 HTTP（url/headers） | P1 |
| FR-15.2 | **凭据托管**：header/env 存加密 vault，写入沙箱时用**运行时注入**而非静态落盘 | P1 |
| FR-15.3 | **OAuth 代办**：`POST /v1/mcp/{id}/oauth/start` 返回平台回调 URL，浏览器完成授权，token 回存（解决沙箱内 `127.0.0.1` 回调不可达） | P1 |
| FR-15.4 | 曝光配置：按服务器 + 按工具（通配符）设 `direct`/`deferred`/`codemode`/`hidden` | P1 |
| FR-15.5 | 状态徽章：`Connected` / `Needs auth` / `Failed` / `Disabled` / `cached · N tools` | P1 |
| FR-15.6 | 一键导入：从 Claude Desktop / Cursor / VS Code 的 `mcpServers` JSON 粘贴导入 | P1 |
| FR-15.7 | 内置预设：Playwright（浏览器）、Filesystem、GitHub、Context7 等，一键添加 | P1 |
| FR-15.8 | 工具列表：展示该服务器的工具名/描述/注解，可逐个禁用 | P1 |

**已接受的风险（你确认过）**：stdio MCP 在沙箱内执行任意二进制，等于用户可以在自己沙箱里跑任意代码。这是可接受的——**用户本来就有 bash**。真正的边界是"不能出沙箱、不能拿到别人的东西"，不是"不能跑代码"。

### 5.10 管理与运维

只剩最小集。

| ID | 需求 | 优先级 |
|---|---|---|
| FR-16.1 | 用户管理：邀请/创建、禁用、重置密码、角色（user/admin） | P0 |
| FR-16.2 | 共享 provider 管理 | P0 |
| FR-16.3 | 共享 skill 管理 | P1 |
| FR-16.4 | 镜像管理：基础镜像版本、预装工具链、构建状态 | P1 |
| FR-16.5 | 运行状态：在线用户、活跃沙箱数、节点水位、镜像拉取失败告警 | P1 |
| FR-16.6 | 资源护栏配置：单用户并发沙箱、CPU/内存/磁盘上限 | P1 |
| FR-16.7 | 功能开关：新 UI、实验特性按用户灰度 | P2 |
| FR-16.8 | 支持模式（默认关）：admin 可临时只读进入某用户会话排障，**必须被通知且留痕**（不是审计体系，只是一条记录） | P2 |

---

## 6. 多租户设计：从"治理"改为"独享错觉"

> v1 把这个章节写成了"策略与隔离体系"。v2 的定位变了，所以整章重写。
> **新的核心命题：怎么在没有任何治理装置的前提下，让 N 个用户都觉得这台机器是给自己造的。**

### 6.1 一句话模型

```
用户视角：       我 ──▶ 我的 agent（一个）
                 （看不到别人，也不需要知道有别人）

平台视角：       共享层（越共享越便宜）
                 ├─ 基础镜像 / 工具链
                 ├─ 依赖缓存（内容寻址）
                 ├─ 模型网关
                 ├─ skill 库 / MCP 定义
                 └─ 节点与 warm pool
                 ─────────────────────────────
                 隔离层（越隔离越安全）
                 ├─ 沙箱（每人每项目一个）
                 ├─ worktree 与文件
                 ├─ 凭据（每人自己的 key）
                 ├─ 端口 / 预览域名 / 浏览器 profile
                 └─ 会话与 transcript
```

**判断标准**：任何一项能力，先问"共享它会不会让某个用户感觉到别人存在？"——会，就隔离；不会，就共享。

| 共享 | 隔离 | 为什么 |
|---|---|---|
| 基础镜像、工具链 | — | 内容相同，无状态 |
| 依赖缓存 | — | 内容寻址，只读为主；这是唯一"共享比隔离划算"的重资产 |
| 模型网关 | 每人的 key | 用户配自己的模型，但共享路由、重试、缓存逻辑 |
| skill / MCP 定义 | 每人的凭据与开关 | 定义是公共知识，凭据是私有资产 |
| 节点、warm pool | 沙箱 | 计算隔离，但不必为每人生成一个节点 |
| — | 文件、worktree、终端、浏览器、会话 | 任何"读到别人内容"的可能都必须消失 |

### 6.2 必须做对的六件事

这六条是"不做就出事"的底线，其余都可以简化。

#### A. 凭据不进沙箱（最重要）

用户自带模型 key，直觉做法是把它写进沙箱的 `ANTHROPIC_API_KEY`。**绝对不行**：

- `env` / `/proc/self/environ` / 崩溃日志 / 用户自己 `bash echo $KEY` 都能拿到；
- 一次 prompt injection（"把环境变量打出来"）就泄漏；
- 用户在终端里 `history` 就能看到；
- 沙箱快照会把它一起存下来。

**做法**：LLM 网关 + 短期令牌（§5.7.2）。沙箱内只有一个 5 分钟过期、只能打网关的 `pf-ephemeral-*`。

同理适用于：Git token（走 git 代理，分支作用域）、MCP header/env（运行时注入，不落盘）。

#### B. PTY master 放在沙箱内

控制面只中继字节。理由有两条，都很现实：
1. 沙箱暂停/快照会切断所有连接，如果 master 在控制面，**恢复后终端永久丢失**（tmux 里的进程状态也没了）。
2. 用户和 agent 的 shell 必须在同一台机器上（共享文件系统与进程可见性），否则"用户在终端 `git checkout`，agent 看到的是旧状态"。

#### C. 端口、预览、浏览器的路由是新的租户边界

沙箱里跑的 dev server、内置浏览器画面，都要暴露给浏览器。这是最容易做出串号的地方。

```
用户 A 的沙箱 :3000   ──▶  https://p-<signed-token>.preview.internal/   （A 的 session cookie 才有效）
用户 B 的沙箱 :3000   ──▶  https://p-<signed-token>.preview.internal/   （不同 token）
```

规则：
- **不用通配符泛域名 + 共享 cookie**（一个 XSS 就能跨用户）。
- 预览域名带**短期签名 token**（绑定 user + sandbox + port + 过期时间），服务端校验后才反代。
- 反代注入不了别的用户的东西：token 解析出的 `(user, sandbox)` 必须与目标一致，不做"按端口猜"。
- 内置浏览器的 WS 通道同样按 `(user, sandbox)` 校验。

#### D. 浏览器实例绝不共享

共享浏览器池（Browserbase 那种多租户共享池）在这里不适用：`--user-data-dir` 里是 cookie 和 localStorage，**串了就是账号串了**。

做法：chromium 跑在用户自己的沙箱里，profile 目录在 workspace 内（顺带得到"登录状态随快照保留"的好处）。

#### E. 依赖缓存共享，但只共享"可验证内容"

`$GOMODCACHE` / `.npm/_cacache` / `$CARGO_HOME` 按 `(user, lockfile_hash)` 内容寻址，同一用户跨 worktree 共享。

- **跨用户共享**：只在"完全内容寻址 + 只读"的部分做（如预下载的公共包索引），因为 npm 包本身可能带恶意 postinstall——一个用户投毒的缓存不该污染另一个用户。
- **同一用户内**：可读写共享，因为都是他自己的东西。
- 这条设计消除了 80–90% 的冷启动成本（真正慢的是 `npm install`，不是启动 VM）。

#### F. 删除要彻底

用户删会话 / 删账号时，要清理：worktree、沙箱快照 blob、终端 ring buffer、浏览器 profile、导出文件、临时日志。

做法：所有路径前缀带 `u/<user_id>/`；删除按前缀清扫 + 一个"孤儿扫描"定时任务（因为沙箱异常退出会留下没登记的文件）。

### 6.3 六个会踩的坑

**坑 1：`git worktree` 的 `.git` 是共享的**
- worktree 里没有独立 `.git` 目录，而是一个 `.git` **文件**指向主 checkout 的 `.git/worktrees/<name>`。
- 后果：① 沙箱里 `rm -rf worktree` 必须配 `git worktree prune`，否则主仓留下死引用；② 某些工具（老版本 husky、部分 IDE）对 worktree 支持不好；③ 子模块在 worktree 里行为有差异。
- 做法：删除分支时必须 `git worktree remove --force` + `prune`；worktree 创建后跑一次 `git submodule update --init` 验证。

**坑 2：默认并行会写坏缓存**
- 两个分支同时 `npm install` 会并发写同一个 `_cacache`。npm 有锁，但不总是可靠。
- 做法：默认分支互斥（§5.1.3 FR-3.1）；显式并行时，**缓存写操作串行化**（用一个用户级文件锁），或者并行分支用独立的 `--cache` 目录。

**坑 3：沙箱被悄悄回收**
- 空闲 15 分钟暂停、14 天回收。用户第二天回来发现"东西没了"。
- 做法：状态条永远显示状态与倒计时；回收前 24 小时在列表里标灰 + 一次通知；**先快照后销毁**；回收后会话仍可读，一键"从快照恢复"。

**坑 4：大文件把浏览器拖死**
- 用户在文件树点开一个 200MB 的日志或一个 50MB 的 CSV。
- 做法：服务端先 `stat`，按大小分级（§5.3.2 FR-7.8）；列表里直接显示文件大小；超过阈值不给预览入口，只给下载。

**坑 5：终端字节流把控制面打死**
- `yes` 或一个死循环 `cat` 能瞬间产生 GB 级输出。
- 做法：服务端限速（每 PTY 2MB/s）+ 环形缓冲（2MB）+ 背压（前端消费慢时丢弃中间帧而非无限缓冲）+ 单用户 PTY 总数上限。

**坑 6：文件路径穿越**
- 文件读写 API 接受 `?path=`，`../../etc/passwd` 就是越权（虽然只在用户自己的沙箱里，但能读到挂载的只读资源与 shim 配置）。
- 做法：路径必须 `filepath.Clean` + 前缀校验 + 拒绝 symlink 逃逸（`openat2` 的 `RESOLVE_BENEATH` 或逐段 `Lstat`）。

### 6.4 八个值得做的设计点

#### A. 独享错觉（Singleton Illusion）——这是整个产品的设计基调

用户永远只看到一个 agent、一个工作区、一条时间线。所有"共享"都在他看不见的地方。

具体到 UI：
- **没有"团队空间"入口**，没有"共享给我的"分组。侧栏只有"我的会话"。
- 用户不会看到任何别人的名字（除了 git commit 的 author，那是自然信息）。
- 加载态用"正在准备你的工作区"，而不是"正在分配共享资源"。
- 资源不足时的文案是"当前排队中，约 40 秒"，而不是"集群容量不足"。

这听起来只是文案，但它决定了整个信息架构——**没有组织树、没有权限矩阵、没有审批流**，因为这些东西的存在本身就在提醒用户"你不是一个人"。

#### B. 一个沙箱，多 worktree —— 成本与并行的公约数

这是 v2 最重要的一个技术选择。

| 方案 | 隔离度 | 冷启动 | 并行成本 | 判断 |
|---|---|---|---|---|
| 每 run 一个沙箱 | 最高 | 每次都要 clone+install（分钟级） | O(N) 个 VM | ✗ 太贵 |
| 每分支一个沙箱 | 高 | 每次都要 clone+install | O(N) 个 VM | ✗ 并行 3 个 = 3 台机 |
| **一个沙箱 + N 个 worktree** | 够（同一用户） | **一次** clone+install | O(N) 个进程（MB 级） | ✓ **选它** |
| 一个沙箱 + 一个 worktree（串行） | 够 | 一次 | 无并行 | ✓ 作为默认 |

收益：
- 用户开三个分支，只花**一次** `git clone` + **一次** `npm install`。
- 三个 agent 共享文件系统缓存、共享系统依赖、共享 chromium 与 tmux。
- 沙箱暂停/恢复只操作一个对象。

代价与对策：
- 进程间干扰（一个 agent 编译占满 CPU）→ cgroup 限制单 worktree 的进程组（`systemd-run --scope -p CPUQuota=`）。
- worktree 数量膨胀 → 上限 5 个，超限要求归档旧分支。
- 缓存并发写 → 见坑 2。

#### C. Sidecar：一个人的"微多租户"

Sidecar 看起来是个小功能，但它解决了一个真实矛盾：**"我想问一句，但不想污染上下文"**。

现有产品的解法都不好：
- 开新会话 → 新上下文、新工作区、找不到刚才那个文件；
- 在主线里问 → 上下文变脏，agent 后面可能被这句带偏；
- 让 agent 自己派子 agent → 用户失去了对"这个问题"的直接控制。

Sidecar 的定义正是这三者的正交补：**共享工作区、独立上下文、人驱动**。

技术上它只是"另一个 Pi 进程 + 独立 session 文件 + 同 cwd"，成本极低；但产品上它把"对话"从一个线性流变成了**有主次的两层结构**。

更有意思的是它的演进路径：sidecar → "提升为分支"（把讨论变成正式路径）→ 这构成了一个自然的"想法 → 探索 → 采纳"漏斗。

#### D. BYOK 三层继承（fastclaw 的 shadow 模式）

```
admin 共享 provider（Inherited，只读）
   └ 用户私有 provider（同名 shadow，可覆盖 base_url / key / 模型清单）
        └ 会话级覆盖（只影响这个会话）
```

为什么用**一张表 + `scope` 字段**而不是两张表：fastclaw 已经验证过，两张表会导致"用户想改共享项的某一个字段"时无解。单表 + 同名 shadow 让"我只想把模型换成便宜的那个"变成一次覆盖而不是复制整个配置。

UI 上要诚实标注：`Inherited`（继承自 admin）vs `Custom`（你自己的）。用户一眼知道"这个配置是谁给的"。

#### E. LLM 网关：换模型不用重启沙箱

如果没有网关，用户换 provider 就需要改沙箱内的环境变量 + 重启 Pi 进程（丢上下文）。有了网关，换模型只是：

```
PUT /v1/sessions/{id}/model  →  更新 DB 一行  →  下一次 turn 用新 provider
```

沙箱内 `base_url` 一直是网关地址，不变。

这条带来的体验差异很实在：用户可以在一个会话里"前 20 轮用旗舰模型规划，后 30 轮用轻量模型执行"，而不用重开会话。

#### F. 端口 / 预览 / 浏览器的统一路由

沙箱内的服务要给人看，有三个来源：用户自己起的 dev server、agent 起的服务、内置浏览器。三者应该用**同一套路由**：

```
① 端口发现（ss -ltnp，30s 轮询）
② 生成签名 URL：https://p-<base32(token)>.preview.internal/
   token = sign(user_id | sandbox_id | port | exp, platform_key)
③ 反代时校验 token → 找到沙箱内 127.0.0.1:port → 转发
```

好处：一处实现，三处复用；token 过期即失效；不需要给每个用户配证书。

**注意**：WebSocket 也要走同一条路（dev server 的 HMR、浏览器的 CDP 都一样）。

#### G. 内容寻址的缓存共享

这是唯一一处"共享比隔离划算"的重资产，值得单独说。

```
缓存键 = sha256( (语言, lockfile 内容 hash) )
缓存体 = 只读 tarball / overlay 层
```

- 命中 → 秒级就绪（`go build` 的模块下载、`npm ci` 的 tarball 都能省掉）。
- 未命中 → 正常安装，装完异步上传（同用户、跨用户都可，因为内容可验证）。
- **安全性**：只共享"包内容本身"（tarball），不共享"安装产物"（可能有 postinstall 副作用）——这是关键区分，很多方案在这里翻车。

#### H. 没有审计，但保留"最小可解释性"

你说了不要审计。我完全同意——但有一类"解释"不是审计，而是**产品质量**：

| 需求 | 是审计吗 | 做不做 |
|---|---|---|
| 每次工具调用能看到参数与结果 | 不是，是对话本身 | ✓ 做（就是 Trace） |
| 能看到"agent 当时为什么改了这行" | 不是，是上下文 | ✓ 做（消息 + diff 关联） |
| 能追溯"谁批准了这个操作" | 是审计 | ✗ 不做（因为不审批） |
| 能查"某用户 30 天前访问了什么" | 是审计 | ✗ 不做 |
| 支持模式下进入用户会话留一条记录 | 半个审计 | ✓ 做（一行日志，不建系统） |

**结论**：所有"解释"都留在会话内（用户自己看），不做跨会话的检索与聚合。这一条划清了"提效工具"和"治理平台"的界线。

---

## 7. 系统架构

### 7.1 分层

```
┌───────────────────────────────────────────────────────────────────────────────┐
│ 浏览器（Next.js App Router + shadcn/ui + Tailwind v4）                        │
│  ├── 会话工作台    MessageScroller / Message / Bubble / Trace / Code Block     │
│  ├── 文件树+预览   react-arborist / Code Block / CodeMirror6 / pdfjs           │
│  ├── Git 面板      自绘 SVG DAG + @git-diff-view                              │
│  ├── 终端          xterm.js（follow + interactive）                           │
│  └── 浏览器面板    Canvas（CDP screencast 帧）                                 │
└──────────────┬────────────────────────────────────────────────────────────────┘
               │ HTTPS REST（命令） + SSE（事件） + WS（PTY / CDP / 文件流）
┌──────────────▼────────────────────────────────────────────────────────────────┐
│ Go 控制面                                                                     │
│                                                                               │
│  API Gateway ── Auth(user/admin) ── Session Service ── Sandbox Manager        │
│       │                                │                    │                 │
│       │                                │                    ├ Worktree Mgr    │
│       │                                │                    ├ Snapshot Mgr    │
│       │                                │                    └ Warm Pool       │
│       │                                ▼                                      │
│       │                          Pi RPC Bridge × N（每分支一个）              │
│       │                                │                                      │
│  ┌────┴─────┬──────────┬──────────┬────┴─────┬──────────┬──────────────┐      │
│  │ FS       │ Git      │ Terminal │ Browser  │ LLM      │ MCP / Skill  │      │
│  │ Service  │ Service  │ Relay    │ Relay    │ Gateway  │ Registry     │      │
│  │ 树/读/写 │ 状态/diff│ PTY 字节 │ CDP 帧   │ key 替换 │ 定义/挂载    │      │
│  │ /搜索    │ /DAG 数据│ /重连    │ /输入    │ /记账    │ /OAuth 代办  │      │
│  └──────────┴──────────┴──────────┴──────────┴──────────┴──────────────┘      │
│                                                                               │
│  Event Bus（NATS JetStream，可重放）    Postgres    Object Store（快照/导出） │
└──────────────┬────────────────────────────────────────────────────────────────┘
               │ （唯一入口：JSONL 协议 + 只读挂载 + 反代）
┌──────────────▼────────────────────────────────────────────────────────────────┐
│  用户沙箱（Firecracker 微VM / gVisor，K8s RuntimeClass，粒度 = (project, user)）│
│  ┌─────────────────────────────────────────────────────────────────────────┐  │
│  │ 只读根 + /home/dev（rw） + .agents/skills（ro）                          │  │
│  │                                                                         │  │
│  │  /home/dev/wt/<project>/main/         ← worktree 1                     │  │
│  │     ├── pi --mode rpc  (Pi 进程 1)                                      │  │
│  │     └── tmux "agent-1"                                                  │  │
│  │  /home/dev/wt/<project>/fix-cache/    ← worktree 2                     │  │
│  │     ├── pi --mode rpc  (Pi 进程 2)                                      │  │
│  │     └── tmux "agent-2"                                                  │  │
│  │                                                                         │  │
│  │  tmux server（user-1, user-2 …）      ← 用户交互终端                     │  │
│  │  chromium --headless --remote-debugging-port=9222                       │  │
│  │  piforge-bridge（Pi 扩展：工具转发 / 只读分级 / 终端旁路）              │  │
│  │  shim（exec/pty/fs/health，~2MB，PID 1 回收僵尸）                        │  │
│  │                                                                         │  │
│  │  ✗ 无真实 provider key  ✗ 无云元数据  ✗ 无 SA token  ✗ 出站按需        │  │
│  └─────────────────────────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────────────────────────┘
```

**三条不可协商的架构原则**：

1. **控制面不执行不可信代码**。transcript、路由、git 逻辑、记账都在控制面；沙箱只跑 agent、终端、浏览器。
2. **进入沙箱只有一条通道**：JSONL + 只读挂载 + 反代。没有"方便一下"的旁路。
3. **PTY 与浏览器的 master 都在沙箱内**。控制面只中继字节/帧。

### 7.2 组件职责

| 组件 | 职责 | 关键实现 |
|---|---|---|
| **API Gateway** | REST/SSE/WS 终结、鉴权、幂等、限速 | 写操作要 `Idempotency-Key`；SSE 支持 `Last-Event-ID` |
| **Auth** | 登录（OIDC 或本地密码）、`user`/`admin` 两角色、用户 API Key（P2） | 会话用 HttpOnly cookie + CSRF token |
| **Session Service** | 会话树（main / branch / sidecar）、生命周期、绑定 worktree | 一个会话 = 一个 Pi 进程 + 一个 session 文件 |
| **Sandbox Manager** | 申请/创建/暂停/唤醒/快照/销毁；warm pool；TTL 看门狗 | 粒度 `(project, user)`；状态机严格；先快照后销毁 |
| **Worktree Mgr** | `git worktree add/remove/prune`、子模块初始化、分支↔worktree 映射 | 删除必 `prune`；创建后校验子模块 |
| **Pi RPC Bridge** | 每分支一个；JSONL 编解码、事件规范化、背压、重连 | 见 §7.4 |
| **FS Service** | 文件树（含 gitignore 处理）、读写、搜索（`rg`）、大文件分级 | 路径穿越防护（§6.3-坑6） |
| **Git Service** | status / diff / commit / branch / log / DAG 数据 / 远端操作 | DAG 用 `--parents` 一次取全；凭据经代理 |
| **Terminal Relay** | PTY 字节流中继、ring buffer、重连偏移 | master 在沙箱（tmux）；限速 2MB/s |
| **Browser Relay** | CDP screencast 帧转发、输入事件注入、模式互斥 | 按 `(user, sandbox)` 校验；不共享实例 |
| **LLM Gateway** | 三层 provider 解析、ephemeral token 校验、key 替换、用量记录 | 换模型不重启沙箱 |
| **MCP Registry** | 用户 MCP 定义、凭据托管、OAuth 代办、mcp.json 生成 | OAuth 回调走平台域名 |
| **Skill Store** | 内容寻址存储、版本、挂载清单 | `sha256` 为地址；挂载前校验 |
| **Event Bus** | 事件扇出、可重放 | NATS JetStream（不要用 Redis pub/sub，无重放） |

### 7.3 一次 run 的时序

```
User        Web              Control Plane                        Sandbox
 │           │                     │                                 │
 │─提问──────▶│─POST /messages─────▶│ 1. 鉴权 + 解析有效 provider     │
 │           │                     │ 2. 取会话→worktree→沙箱         │
 │           │                     │    (PAUSED? 先唤醒，≤3s)        │
 │           │                     │ 3. 确保 Pi 进程在跑              │
 │           │                     │──── {"type":"prompt"} ─────────▶│
 │           │◀──SSE: accepted─────│                                 │
 │           │                     │◀──── agent_start / turn_start ──│
 │           │◀──SSE: turn_start───│                                 │
 │           │                     │◀──── message_update(delta) ─────│
 │           │◀──SSE: delta────────│ 4. 累加 + 增量渲染               │
 │           │                     │◀──── tool_execution_start ──────│
 │           │◀──SSE: tool.started─│                                 │
 │           │                     │    bash 输出经 partialResult 回来│
 │           │◀──SSE: tool.progress│ 5. 跟随终端实时渲染（只读）      │
 │           │                     │◀──── tool_execution_end ────────│
 │           │◀──SSE: tool.ended───│ 6. 刷新 git status / 文件树 diff │
 │           │                     │◀──── turn_end …. agent_settled ─│
 │           │◀──SSE: settled──────│ 7. 落库 + 空闲计时器启动         │
```

**必须持久化的落点**（写错就"关页面丢进度"）：

| 时刻 | 必须已落库 |
|---|---|
| 发给 Pi 之前 | 用户消息 + `client_msg_id`（幂等） |
| `tool_execution_start` 之后、执行之前 | 工具调用意图（含参数 hash） |
| `tool_execution_end` 之后 | 结果或错误、耗时 |
| `agent_settled` 之后 | 会话终态、token 统计 |

> 这是 Pi 文档强调的 "durability-sensitive listener 必须 await"。**副作用之前记意图，之后记观察。**日志能识别"不确定操作"，但不能自动回滚。

### 7.4 Pi RPC Bridge（后端最关键的部分）

#### 7.4.1 进程模型

- **一个 Pi 进程 = 一个会话（一条分支）**。不进程内多路复用（Pi 的 session 状态是进程内的）。
- 一个沙箱内可以同时有 N 个 Pi 进程（N = 并行分支数 + sidecar 数 + 1）。默认上限 5。
- 进程跑在沙箱里；Bridge 通过 shim 的 stdio 通道通信。
- Bridge 只做：分帧、反序列化、事件规范化、背压、重连、指标。

#### 7.4.2 分帧与调用（Go 要点）

```go
// Pi 严格 LF 分帧。绝不能用会按 U+2028/U+2029 切分的读取器。
func (b *Bridge) readLoop(r *bufio.Reader) error {
    for {
        line, err := r.ReadBytes('\n')
        if err != nil { return err }
        line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
        if len(line) == 0 { continue }
        var rec Record
        if err := json.Unmarshal(line, &rec); err != nil {
            b.metrics.Malformed.Inc(); continue
        }
        b.dispatch(rec)
    }
}

// 命令必须带唯一 id；Pi 是异步处理的，不能靠响应顺序关联
func (b *Bridge) Call(ctx context.Context, cmd Command) (Response, error) {
    id := ulid.Make().String(); cmd.ID = id
    ch := make(chan Response, 1)
    b.pending.Store(id, ch); defer b.pending.Delete(id)
    if err := b.writeJSONL(cmd); err != nil { return Response{}, err }
    select {
    case resp := <-ch:  return resp, nil
    case <-ctx.Done():  return Response{}, ctx.Err()
    case <-b.dead:      return Response{}, ErrBridgeClosed
    }
}
```

#### 7.4.3 流式消息重建（最容易写错）

```go
type blockBuf struct {
    kind       string // text | thinking | toolCall
    text       strings.Builder
    toolCall   *ToolCall
    contentIdx int
}

func (s *StreamAssembler) OnUpdate(ev AssistantMessageEvent, u Usage) {
    switch ev.Type {
    case "text_start", "thinking_start", "toolcall_start":
        s.blocks[ev.ContentIndex] = &blockBuf{kind: kindOf(ev.Type), contentIdx: ev.ContentIndex}
    case "text_delta", "thinking_delta":
        s.blocks[ev.ContentIndex].text.WriteString(ev.Delta)
        s.emitDelta(ev)                                  // 只发增量给前端
    case "text_end", "thinking_end":
        b := s.blocks[ev.ContentIndex]
        b.text.Reset(); b.text.WriteString(ev.Content)    // ★ 用权威内容整体替换
        s.emitBlockEnd(ev.Content)
    case "toolcall_delta":
        // 累积参数 JSON 仅供展示"正在生成参数"，不解析、不执行
    case "toolcall_end":
        s.blocks[ev.ContentIndex].toolCall = ev.ToolCall  // ★ 权威参数
    }
    s.lastUsage = u   // 流内是累积值，可能一直为 0 直到结束
}

func (s *StreamAssembler) OnMessageEnd(m Message) {
    s.emitFinal(m)    // ★ 唯一权威：整条替换
    s.reset()
}
```

**三条铁律**：
1. 前端渲染增量，但**永远以 `message_end` 为最终真相**。
2. `usage` 流式期间可能全 0，别用它做实时计数。
3. **被 output 上限截断的 tool call 一律不执行**（Pi 自己也拒绝），Bridge 不许"帮忙修 JSON"。

#### 7.4.4 事件规范化与映射

统一信封：

```jsonc
{
  "seq": 1843,                 // 会话内单调递增；SSE 的 id 就是它
  "session_id": "ses_01H...",
  "branch_id": "br_01H...",    // 分支（= 一个 Pi 进程）
  "kind": "main|branch|sidecar",
  "type": "tool.execution.end",
  "ts": "2026-10-27T17:03:00.123+08:00",
  "payload": { "..." : "..." },
  "pi_seq": 512                // 原始序号，便于对账
}
```

| Pi 事件 | 平台事件 | UI |
|---|---|---|
| `agent_start` / `turn_start` | `run.started` / `turn.started` | 状态条 |
| `message_start` | `message.started` | 骨架 |
| `message_update` | `message.delta` | 增量文本 |
| `message_end` | `message.final` | 定稿替换 |
| `tool_execution_start` | `tool.started` | Trace running + **跟随终端开 tab** |
| `tool_execution_update` | `tool.progress` | Trace 滚动 + 终端追加 |
| `tool_execution_end` | `tool.ended` | Trace 完成 + **刷新 git/文件树** |
| `compaction_start/end` | `context.compaction` | 时间线事件 |
| `auto_retry_start/end` | `run.retry` | "上游过载，2s 后重试 (1/3)" |
| `queue_update` | `queue.updated` | composer 上方队列 |
| `agent_settled` | `run.settled` | **唯一的"完成"信号** |
| `extension_error` | `platform.error` | 告警 |
| `bash_execution_update` | `pty.stream` | 终端字节流（直连 bash 命令时） |

#### 7.4.5 重连与幂等

- Bridge ↔ Pi：进程崩溃 → 标 `interrupted`，**不自动重放**；提示"继续 / 重跑 / 查看已有改动"。
- Bridge ↔ 前端：SSE `id: {seq}` + `Last-Event-ID`；超保留窗口返回 `409 need_full_resync`，前端改走 `get_entries`。
- 提交消息带 `client_msg_id`，重复提交返回同一结果。

### 7.5 沙箱、worktree 与快照

| 能力 | 实现要点 |
|---|---|
| 粒度 | `(project, user)`。一个用户一个项目一个沙箱，内含 N 个 worktree |
| Warm pool | 按**基础镜像**预热（不是按用户，成本太高）；用户首次进入命中通用池 → 90ms 级 |
| 镜像分层 | `base(平台工具链) → user-extra(用户预装) → worktree 内容` |
| 依赖缓存 | 按 `(user, lockfile_hash)` 内容寻址；同一用户跨 worktree 读写共享 |
| worktree | `git worktree add ../wt/<slug> -b <branch>`；删除 `git worktree remove --force` + `prune` |
| 快照 | 只在工具边界；`fs` / `fs+mem` 两档；绑定 `(commit, content_hash)` |
| 暂停/唤醒 | `PAUSED` 时 memory 快照保留进程状态（含 tmux 会话）；唤醒 ≤ 3s |
| 资源限制 | cgroup v2：单沙箱 CPU/内存/磁盘；单 worktree 进程组 CPU 配额（防一个分支饿死其他） |
| 时间 | 恢复后墙钟会跳变；**一律用控制面时间打戳** |

**关于 memory 快照的价值**：它把"tmux 里的 npm run dev 进程"也一起保住了。如果没有 memory 快照，暂停后 dev server 就没了，用户回来要重跑——这是体验上的巨大差异。**建议 M2 就上 memory 快照。**

### 7.6 终端与浏览器通道

#### 7.6.1 终端

```
沙箱内 tmux server
 ├ session "user-tab-1"   ← 用户交互终端
 ├ session "user-tab-2"
 └ session "agent-1"      ← 可选：Pi 的 bash 若走 tmux 包装

控制面 Terminal Relay
 ├ 鉴权（session cookie / WS ticket）
 ├ 路由到 (sandbox_id, tmux_session)
 ├ 双向字节转发 + 限速 + ring buffer(2MB)
 └ 记录 offset，支持 from_offset 重连
```

#### 7.6.2 浏览器

```
沙箱内 chromium --headless=new --remote-debugging-port=9222
 ├ Playwright MCP（stdio） → agent 用
 └ Browser Relay（控制面）→ 人用
      ├ Page.startScreencast → JPEG → WS → canvas
      ├ Input.dispatchMouseEvent / dispatchKeyEvent（仅接管模式）
      └ 模式状态机：OBSERVE | TAKEOVER（互斥，切换需确认）
```

**接管模式要暂停 agent 的浏览器工具**：否则人和 agent 抢鼠标，体验灾难。实现上用一个"浏览器锁"——接管时 Bridge 把 `mcp__playwright__*` 的 `tool_call` 拦成 `{block:true, reason:"用户正在操作浏览器"}`，释放后恢复。

### 7.7 技术选型

| 领域 | 选择 | 理由 |
|---|---|---|
| 语言 | Go 1.24+ | 用户指定；适合长连接 + 子进程管理 |
| HTTP | `chi` + 自实现 SSE | 需求简单 |
| WS | `nhooyr.io/websocket`（或 `coder/websocket`） | 终端/浏览器都是长字节流 |
| 事件总线 | **NATS JetStream** | 可重放（Redis pub/sub 不行） |
| 数据库 | PostgreSQL 16（**不用 RLS**，见 §8.1） | 单实例内部工具，用应用层 `user_id` 过滤足够 |
| 对象存储 | S3 兼容 / MinIO | 快照、导出、大文件 |
| 沙箱 | Firecracker（默认）/ gVisor（轻量档） | 需要 `docker build` / FUSE 时微VM 更省事 |
| K8s SDK | `firecracker-go-sdk`、`containerd/client` | 官方维护 |
| 加密 | AES-GCM + 主密钥（env/KMS） | 用户 provider key / MCP 凭据 |
| 可观测 | OTel（一个 run 一条 trace）+ Prometheus + Loki | span 下钻到 tool call |
| 前端 | Next.js 15 App Router + RSC；Tailwind v4；shadcn（锁 `radix` base） | §2.3 |
| 前端状态 | TanStack Query（服务端状态）+ Zustand（本地 UI） | 不要 Redux |
| 前端流 | `EventSource`（SSE）+ 原生 WS | 带 `Last-Event-ID` |

---

## 8. 数据模型

### 8.1 一个简化决策：不用 RLS

v1 用了 Postgres RLS。v2 **不用**，原因：

- 这是**单实例内部工具**，用户数十到数百，不是多组织 SaaS。
- RLS 的坑（owner 绕过、外键完整性检查绕过、`SET LOCAL` 忘记导致静默全表）需要持续维护，收益在这里不成比例。
- 改用**应用层强约束**，代价更低、可测试性更好：

```go
// 所有仓储方法必须从 ctx 取 userID，且 SQL 必须带 user_id 条件。
// 用一个 lint 规则 + 一组集成测试兜底：任何不带 user_id 的 SELECT 都让 CI 失败。
func (r *SessionRepo) Get(ctx context.Context, id string) (*Session, error) {
    uid := auth.MustUserID(ctx)                 // 没有就 panic，绝不默认放行
    row := r.db.QueryRowContext(ctx,
        `SELECT ... FROM sessions WHERE id=$1 AND user_id=$2`, id, uid)
    ...
}
```

**唯一的例外**是 admin 的"支持模式"——它必须走一个显式的、会留痕的代码路径，不能复用普通查询。

### 8.2 表结构

```sql
-- ============ 用户 ============
CREATE TABLE users (
  id            text PRIMARY KEY,              -- ULID
  email         citext UNIQUE NOT NULL,
  display_name  text,
  password_hash text,                          -- 本地登录；用 OIDC 时为空
  idp_subject   text UNIQUE,
  role          text NOT NULL DEFAULT 'user',   -- user | admin
  status        text NOT NULL DEFAULT 'active', -- active | disabled
  settings      jsonb NOT NULL DEFAULT '{}',    -- 主题、密度、默认模型、偏好
  created_at    timestamptz NOT NULL DEFAULT now(),
  deleted_at    timestamptz
);

-- ============ 模型 Provider（单表 + scope shadow，参考 fastclaw）============
CREATE TABLE providers (
  id          text PRIMARY KEY,
  scope       text NOT NULL,          -- 'admin'（共享） | 'user'（私有）
  owner_id    text REFERENCES users(id),  -- scope=user 时必填
  name        text NOT NULL,          -- 同名时 user 覆盖 admin
  api         text NOT NULL,          -- anthropic-messages | openai-responses | openai-compAT
  base_url    text,
  key_cipher  bytea,                  -- AES-GCM 密文；永不出库到沙箱
  models      jsonb NOT NULL,         -- [{id,name,contextWindow,maxTokens,reasoning,cost}]
  extra       jsonb NOT NULL DEFAULT '{}',  -- 代理、超时、自定义 header
  enabled     boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (scope, owner_id, name)
);

-- ============ 项目（= 一个 Git 仓库 + 用户）============
CREATE TABLE projects (
  id             text PRIMARY KEY,
  user_id        text NOT NULL REFERENCES users(id),
  name           text NOT NULL,          -- 用户自己起的名字
  repo_url       text,                   -- 空 = 空白工作区
  default_branch text NOT NULL DEFAULT 'main',
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, name)
);

-- ============ 沙箱（粒度 = project × user）============
CREATE TABLE sandboxes (
  id           text PRIMARY KEY,
  user_id      text NOT NULL,
  project_id   text NOT NULL REFERENCES projects(id),
  state        text NOT NULL,  -- CREATING|STARTING|READY|BUSY|IDLE|PAUSED|TERMINATED|ERROR|MISSING
  provider     text NOT NULL,  -- firecracker|gvisor
  node         text,
  image        text NOT NULL,
  cpu_milli    int, mem_mb int, disk_gb int,
  last_active_at timestamptz NOT NULL DEFAULT now(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz,        -- UI 必须展示
  UNIQUE (project_id)              -- 一项目一沙箱（user 已隐含在 project 上）
);

-- ============ worktree（= 会话的工作目录）============
CREATE TABLE worktrees (
  id           text PRIMARY KEY,
  sandbox_id   text NOT NULL REFERENCES sandboxes(id),
  user_id      text NOT NULL,
  path         text NOT NULL,     -- /home/dev/wt/<project>/<slug>
  branch       text NOT NULL,     -- git 分支名
  base_commit  text,              -- 创建时的 HEAD
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (sandbox_id, path)
);

-- ============ 会话（main | branch | sidecar）============
CREATE TABLE sessions (
  id            text PRIMARY KEY,
  user_id       text NOT NULL,
  project_id    text NOT NULL,
  worktree_id   text REFERENCES worktrees(id),   -- sidecar 与 main 共享同一 worktree
  kind          text NOT NULL,                   -- main | branch | sidecar
  root_id       text REFERENCES sessions(id),    -- 分支树的根（main 的 id）
  parent_id     text REFERENCES sessions(id),    -- fork 的直接父
  fork_entry_id text,                            -- 从哪条 entry 分叉（Pi entry id）
  label         text,                            -- 分支名
  title         text,
  status        text NOT NULL,   -- active|running|waiting|paused|settled|failed|archived
  model_ref     text,            -- 会话级模型覆盖（provider:model）
  thinking_level text,
  config        jsonb NOT NULL DEFAULT '{}',  -- 快照：启用哪些 skill/mcp、工具白名单
  created_at    timestamptz NOT NULL DEFAULT now(),
  last_active_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON sessions (user_id, last_active_at DESC);
CREATE INDEX ON sessions (root_id);

-- ============ 运行（一次 prompt → agent_settled）============
CREATE TABLE runs (
  id           text PRIMARY KEY,
  session_id   text NOT NULL REFERENCES sessions(id),
  trigger      text NOT NULL,   -- user|sidecar|fork
  status       text NOT NULL,   -- started|settled|aborted|failed|interrupted
  tokens_in    bigint DEFAULT 0, tokens_out bigint DEFAULT 0,
  tokens_cache_r bigint DEFAULT 0, tokens_cache_w bigint DEFAULT 0,
  cost_usd     numeric(12,6) DEFAULT 0,   -- 仅展示，不计费
  started_at   timestamptz NOT NULL DEFAULT now(),
  settled_at   timestamptz
);

-- ============ 事件（transcript 投影，用于查询与续传）============
CREATE TABLE events (
  session_id  text NOT NULL REFERENCES sessions(id),
  seq         bigint NOT NULL,
  run_id      text,
  entry_id    text,        -- Pi entry id（树游标）
  parent_id   text,        -- Pi parentId（树）
  type        text NOT NULL,
  payload     jsonb NOT NULL,
  blob_ref    text,        -- 大内容进对象存储
  ts          timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, seq)
);
CREATE INDEX ON events (session_id, entry_id);

-- ============ 工具调用（会话内展示 + 调试，非审计）============
CREATE TABLE tool_calls (
  id           text PRIMARY KEY,     -- Pi 的 toolCallId
  session_id   text NOT NULL,
  run_id       text NOT NULL,
  name         text NOT NULL,        -- bash|edit|read|mcp__playwright__click ...
  namespace    text,                 -- mcp__<server>
  args_hash    text NOT NULL,
  args_preview jsonb,                -- 脱敏摘要
  is_error     boolean,
  exit_code    int,
  duration_ms  int,
  result_bytes int,
  started_at   timestamptz NOT NULL,
  ended_at     timestamptz
);
CREATE INDEX ON tool_calls (session_id, started_at);

-- ============ MCP（单表 + scope）============
CREATE TABLE mcp_servers (
  id           text PRIMARY KEY,
  scope        text NOT NULL,        -- 'admin' | 'user'
  owner_id     text REFERENCES users(id),
  name         text NOT NULL,        -- mcp__<name>__<tool>
  transport    text NOT NULL,        -- stdio | http
  command      text, args jsonb, cwd text,   -- stdio
  url          text, headers jsonb,          -- http（headers 值可为 ${SECRET} 引用）
  secrets_cipher bytea,              -- 加密后的 env/header 真值
  oauth        jsonb,                -- driver 状态（不存 token）
  exposure     text NOT NULL DEFAULT 'codemode',
  tool_overrides jsonb DEFAULT '{}',
  enabled      boolean NOT NULL DEFAULT true,
  UNIQUE (scope, owner_id, name)
);

-- ============ Skill ============
CREATE TABLE skills (
  id           text PRIMARY KEY,
  scope        text NOT NULL,        -- 'admin' | 'user'
  owner_id     text REFERENCES users(id),
  name         text NOT NULL,
  version      text NOT NULL,
  description  text NOT NULL,        -- 进 system prompt 的那句
  content_hash text NOT NULL,        -- sha256，内容寻址
  bundle_key   text NOT NULL,        -- 对象存储里的 tar.zst
  source_url   text,                 -- 从哪导入的
  enabled      boolean NOT NULL DEFAULT true,
  UNIQUE (scope, owner_id, name, version)
);

-- ============ 快照 ============
CREATE TABLE snapshots (
  id           text PRIMARY KEY,
  user_id      text NOT NULL,
  sandbox_id   text NOT NULL,
  kind         text NOT NULL,   -- fs | fs_mem
  git_commit   text,
  content_hash text NOT NULL,   -- 挂载内容清单 hash
  size_bytes   bigint,
  blob_key     text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now()
);
```

### 8.3 保留策略

| 表 | 保留 | 说明 |
|---|---|---|
| `events` | 热 60 天，冷归档 1 年 | 归档进对象存储，按 `(session, seq)` 分段 |
| `tool_calls` | 60 天 | 随事件一起归档 |
| `snapshots` | 跟随沙箱生命周期 | 沙箱回收后保留最近 2 个快照 |
| `runs` | 90 天 | 只用于会话内统计 |

---

## 9. API 设计

### 9.1 约定

- Base `/v1`；认证：HttpOnly cookie（Web）+ `Authorization: Bearer pf_...`（API Key，P2）。
- 写操作要 `Idempotency-Key`；分页用游标（`?cursor=&limit=`）。
- 错误：`{ "error": { "code": "...", "message": "...", "details": {}, "request_id": "..." } }`。

### 9.2 会话

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/v1/projects` | 建项目 `{name, repo_url, default_branch}` |
| `GET` | `/v1/projects` | 列表（含沙箱状态、最后活跃） |
| `POST` | `/v1/projects/{p}/sessions` | 建会话 `{kind:"main", title, model_ref, thinking_level}` |
| `GET` | `/v1/sessions` | `?project=&status=&q=&cursor=` |
| `GET` | `/v1/sessions/{id}` | 详情（含分支树概要、worktree、沙箱状态） |
| `POST` | `/v1/sessions/{id}/messages` | `{client_msg_id, text, images[], streaming_behavior}` |
| `POST` | `/v1/sessions/{id}/interrupt` | `{clear_queue?:bool}` → 返回被清空的文本 |
| `GET` | `/v1/sessions/{id}/events` | **SSE**，`Last-Event-ID` 续传 |
| `GET` | `/v1/sessions/{id}/entries` | `?since=<entry_id>`（对齐 Pi `get_entries`） |
| `GET` | `/v1/sessions/{id}/stats` | token / cost / context 用量 |
| `PATCH` | `/v1/sessions/{id}` | 改名、改模型、归档 |
| `POST` | `/v1/sessions/{id}/fork` | `{entry_id}` → 建分支会话 + worktree |
| `POST` | `/v1/sessions/{id}/clone` | 复制当前分支 |
| `GET` | `/v1/sessions/{id}/tree` | 分支树（Pi `get_tree` + 我们的元数据） |
| `POST` | `/v1/sessions/{id}/compact` | 手动压缩 |

### 9.3 分支与 Sidecar

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/v1/sessions/{root}/branches` | 分支列表（含状态、diff 规模、成本） |
| `POST` | `/v1/branches/{id}/run` | 让某分支执行（若默认互斥，先 pause 其他） |
| `POST` | `/v1/branches/{id}/pause` | |
| `POST` | `/v1/branches/compare` | `{ids:[a,b,c]}` → 并排 diff + 测试 + 成本 |
| `POST` | `/v1/branches/{id}/adopt` | 采纳：cherry-pick / 整树覆盖到主干（二次确认） |
| `POST` | `/v1/sessions/{id}/sidecar` | 建 sidecar `{model_ref, readonly_tools:true}` |
| `GET` | `/v1/sessions/{id}/sidecars` | 列出（最多 3） |
| `POST` | `/v1/sidecar/{id}/promote` | 提升为正式分支（携带上下文） |

### 9.4 文件 / Git / 终端 / 浏览器

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/v1/worktrees/{id}/tree?path=&depth=` | 文件树（含 git 状态） |
| `GET` | `/v1/worktrees/{id}/file?path=` | 读文件（大小分级见 FR-7.8） |
| `PUT` | `/v1/worktrees/{id}/file?path=` | 写文件（人工编辑） |
| `DELETE` | `/v1/worktrees/{id}/file?path=` | 删文件 |
| `GET` | `/v1/worktrees/{id}/search?q=&type=name\|content` | `rg` 搜索 |
| `GET` | `/v1/worktrees/{id}/git/status` | 分支、ahead/behind、变更列表 |
| `GET` | `/v1/worktrees/{id}/git/diff?scope=&path=` | 结构化 diff |
| `POST` | `/v1/worktrees/{id}/git/stage` | `{paths[], hunks[]}` |
| `POST` | `/v1/worktrees/{id}/git/commit` | `{message, paths[], amend}` |
| `GET` | `/v1/worktrees/{id}/git/log?limit=&cursor=` | commit 列表 |
| `GET` | `/v1/worktrees/{id}/git/graph?limit=` | **DAG 数据**（`hash, parents[], author, ts, subject, refs[]`） |
| `POST` | `/v1/worktrees/{id}/git/branch` | 新建/切换/删除 |
| `POST` | `/v1/worktrees/{id}/git/push` \| `/pull` \| `/fetch` | 凭据经代理注入 |
| `POST` | `/v1/worktrees/{id}/pr` | 开 PR（GitHub/GitLab） |
| `WS` | `/v1/sandboxes/{id}/pty?tab=&from_offset=` | 交互终端 |
| `WS` | `/v1/sessions/{id}/follow?branch=` | **跟随终端**（只读事件流，可用 SSE 替代） |
| `WS` | `/v1/sandboxes/{id}/browser?mode=observe\|takeover` | 浏览器画面与输入 |
| `GET` | `/v1/sandboxes/{id}/ports` | 端口列表 |
| `GET` | `https://p-<token>.preview.internal/` | 预览反代（不经 REST） |

### 9.5 模型 / MCP / Skill / 管理

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/v1/providers` | 合并视图：`{name, source:"inherited"\|"custom", ...}` |
| `POST` | `/v1/providers` | 建私有 provider |
| `PATCH` | `/v1/providers/{id}` | 改（只能改自己的） |
| `POST` | `/v1/providers/{id}/test` | 连通性测试 |
| `GET` | `/v1/mcp/servers` | 列表 + 健康 |
| `POST` | `/v1/mcp/servers/{id}/oauth/start` | **返回平台回调 URL** |
| `POST` | `/v1/mcp/import` | 粘贴 `mcpServers` JSON 导入 |
| `GET` | `/v1/skills` | 列表（admin 共享 + 私有） |
| `POST` | `/v1/skills` | 上传（zip / git url） |
| `GET` | `/v1/admin/users` | 用户管理 |
| `POST` | `/v1/admin/providers` | 建共享 provider |
| `GET` | `/v1/admin/runtime` | 活跃沙箱、节点水位、镜像状态 |

### 9.6 契约示例

**提交消息（运行中）**

```http
POST /v1/sessions/ses_01HX/messages
Idempotency-Key: 7f3c...
Content-Type: application/json

{ "client_msg_id": "cm_01HX9",
  "text": "先别改源码，只分析为什么迁移会锁表",
  "streaming_behavior": "steer" }
```

```json
{ "accepted": true, "disposition": "queued", "queue_position": 1, "run_id": "run_01HX8" }
```

**SSE 帧（工具执行 → 跟随终端）**

```
id: 1843
event: tool.progress
data: {"seq":1843,"session_id":"ses_01HX","branch_id":"br_main","kind":"main",
       "type":"tool.execution.update","ts":"2026-10-27T17:03:00Z",
       "payload":{"tool_call_id":"call_9","name":"bash",
                  "chunk":"--- FAIL: TestCacheEvict (0.02s)\n"}}
```

**Git DAG 数据**

```json
{
  "commits": [
    { "hash": "a1b2c3d", "parents": ["e4f5a6b"], "author": "alice",
      "ts": "2026-10-27T09:12:00Z", "subject": "fix: cache eviction race",
      "refs": [{ "type": "branch", "name": "fix-cache" }, { "type": "head" }],
      "lane": 1 }
  ],
  "head": "a1b2c3d",
  "next_cursor": "e4f5a6b"
}
```

### 9.7 错误码

| HTTP | code | 场景 | 客户端行为 |
|---|---|---|---|
| 400 | `invalid_argument` | 参数错 | 提示 |
| 401 | `unauthenticated` | 未登录 | 跳登录 |
| 403 | `forbidden` | 不是你的资源 | 提示（不暴露存在性） |
| 404 | `not_found` | 不存在或无权 | 同上 |
| 409 | `session_busy` | 运行中且未给 `streaming_behavior` | 弹"入队/打断" |
| 409 | `need_full_resync` | SSE seq 超窗口 | 改走 `get_entries` |
| 409 | `branch_conflict` | 该分支有另一个进程在跑 | 提示先暂停 |
| 413 | `file_too_large` | 文件超预览阈值 | 给下载 |
| 422 | `sandbox_unavailable` | 沙箱创建失败/池耗尽 | 排队 + 显示位置 |
| 423 | `sandbox_paused` | 沙箱已暂停 | 一键唤醒 |
| 429 | `rate_limited` | 限速 | 退避 |
| 503 | `sandbox_expired` | 已回收 | 一键从快照恢复 |
| 503 | `upstream_overloaded` | LLM 过载 | 自动退避，显示"重试 2/3" |

---

## 10. 前端设计

### 10.1 设计原则

1. **复用优先**。每个界面先问 shadcn 有没有，再问 AI Elements 能不能抄，最后才引入第三方（且只有 §2.3 的 5 个例外）。
2. **一个页面装下全部**。不跳页：文件、终端、浏览器、diff 都在同一个工作台里，靠面板切换。
3. **跟随优先于打断**。agent 干活时用户默认是"看"，不是"操作"；想操作要显式接管。
4. **折叠但不隐藏**。思考、工具、压缩、上下文用量都可展开。
5. **键盘优先**。`⌘K` 命令面板、`⌘P` 开文件、`⌘↵` 打断、`Esc` 中断、`⌘B` 折叠侧栏、`⌘J` 切终端。
6. **状态永远可见**。运行中 / 暂停 / 唤醒中 / 排队中，任何等待都要有原因和倒计时。

### 10.2 路由与页面

| 路由 | 页面 | 说明 | 优先级 |
|---|---|---|---|
| `/login` | 登录 | `Card` + `Field` + `Button` | P0 |
| `/` | 首页 | 项目卡片网格 + 最近会话 + "新建" | P0 |
| `/p/[project]` | 项目页 | 会话列表 + 沙箱状态 + 分支概览 | P0 |
| `/p/[project]/s/[session]` | **工作台（核心）** | 见 §10.3 | P0 |
| `/settings/providers` | 模型与凭据 | 继承项 + 私有项 | P0 |
| `/settings/mcp` | MCP 中心 | 列表 / 详情 / OAuth / 导入 | P1 |
| `/settings/skills` | Skill 中心 | 浏览 / 安装 / 详情 | P1 |
| `/settings/preferences` | 偏好 | 主题、密度、默认模型、快捷键 | P1 |
| `/admin/users` | 用户管理（admin） | DataTable | P0 |
| `/admin/providers` | 共享 provider（admin） | DataTable + 表单 | P0 |
| `/admin/runtime` | 运行状态（admin） | 沙箱数、节点水位、镜像 | P1 |

### 10.3 工作台布局（核心页）

两种布局，可一键切换（记住用户偏好）。

#### 布局 A：对话优先（默认，适合"让它干活"）

```
┌──────────┬───────────────────────────────────┬──────────────────────┐
│Sidebar   │  会话流（MessageScroller）          │ 检查器（Tabs）        │
│sidebar-07│  Message / Bubble / Trace          │ ┌──────────────────┐ │
│          │  Code Block / Marker               │ │文件 变更 Git 终端 浏览器│
│ 项目 ▾   │                                    │ ├──────────────────┤ │
│ ├ ses 1● │                                    │ │ 文件树            │ │
│ ├ ses 2  │                                    │ │  (react-arborist) │ │
│ └ ses 3⏸ │  ── Marker: 上下文已压缩 72%→31%    │ │                   │ │
│          │                                    │ │                   │ │
│ [+ 新建]  │  ┌──────────────────────────────┐  │ │                   │ │
│          │  │ 队列：· "顺便修测试" ✎ ✕      │  │ │                   │ │
│ 分支 ▾   │  │ ┌──────────────────────────┐ │  │ └──────────────────┘ │
│ ├ main   │  │ │ [@] 输入…          ↑ ⌘↵  │ │  │  (Resizable)        │
│ ├ fix-c● │  │ └──────────────────────────┘ │  │                     │
│ └ alt-a⏸ │  │ 模型▾ · 推理▾ · 沙箱 ● READY │  │  [打开 Sidecar ▸]   │
│          │  └──────────────────────────────┘  │                     │
└──────────┴───────────────────────────────────┴──────────────────────┘
```

#### 布局 B：工作台（适合"我要自己看/改"）

```
┌──────────┬───────────────┬────────────────────────────┬──────────────┐
│Sidebar   │  会话流        │  编辑区（Tabs）             │ Sidecar      │
│          │  (30%)        │  文件预览 / Diff / 提交详情  │ (可收起)      │
│ 分支 ▾   │               │                            │              │
│ ├ main   │  …            │  ┌──────────────────────┐  │ 独立对话      │
│ ├ fix-c  │  …            │  │ cache.go     +12 -3  │  │ 独立模型      │
│ └ alt-a  │  …            │  │ ▸ @git-diff-view     │  │ 只读工具      │
│          │               │  └──────────────────────┘  │              │
│          │  ┌─────────┐  ├────────────────────────────┤ 引用：        │
│          │  │composer │  │  底部：终端 / 浏览器（Tab） │ @main:msg_12 │
│          │  └─────────┘  │  $ go test ./...           │ @diff:cache  │
└──────────┴───────────────┴────────────────────────────┴──────────────┘
```

**面板规则**：
- 所有分割用 `Resizable`，宽度记忆到用户偏好。
- 任一面板可 `⌘⇧↵` 全屏专注（Esc 退出）。
- 窄屏（< 1280px）：检查器与编辑区折叠为 `Sheet`，终端折叠为底部抽屉。
- 分支切换在左栏第二段（`Collapsible`），带状态圆点。

### 10.4 面板组件规格

#### 10.4.1 会话流

| 元素 | 组件 | 规格 |
|---|---|---|
| 容器 | **`MessageScroller`** | `scrollAnchor="turn"`、`scrollPreviousItemPeek=64`、`defaultScrollPosition="last-anchor"`、`preserveScrollOnPrepend`。**禁止自写 stick-to-bottom** |
| 消息 | `Message` + `MessageGroup` + `Bubble`（助手 `ghost` / 用户 `secondary`） | 长文不截断；hover 出时间戳与复制 |
| 思考 | `Trace` + `TraceCollapsible`（`status="running"` 时 shimmer） | 默认折叠，标题带耗时；可设"始终展开" |
| 工具 | `Trace` + `Code Block size="sm"`（失败 `variant="destructive"`） | 一行：图标 + 名称 + 参数摘要 + 耗时 + 状态 |
| 代码块 | `Code Block`（`variant: added/removed`、`language="ansi"`） | 围栏未闭合时禁用 copy |
| 计划 | `Trace` + 列表（AI Elements `Queue` 模式） | 未完成/已完成两段 |
| 上下文事件 | `Marker` + 可展开面板 | `上下文已压缩 72% → 31%，保留第 71 条起` |
| 队列 | `Item` 列表（可编辑/删） | 标注"下一轮"/"立即" |
| Composer | `Input Group` + `Attachment` + `Command`(`/` 命令、`@` 提及) + `Kbd` | 自增高 max 8 行 |
| 底栏 | `Select`(模型) + `Select`(推理) + `Badge`(沙箱状态) + `Button`(Sidecar) | 模型+推理是双菜单 |

#### 10.4.2 文件树

| 元素 | 实现 |
|---|---|
| 树 | `react-arborist`（虚拟化、键盘导航、拖拽） |
| 节点 | 图标（按扩展名）+ 名称 + git 状态点（M 黄 / A 绿 / D 红 / U 灰 / ! 橙） |
| 头部 | 项目名 + 分支 `Badge` + 刷新 + "显示忽略文件" `Switch` |
| 搜索 | `⌘P` 文件搜索（`Command` 面板）；`⌘⇧F` 内容搜索 |
| 右键 | `ContextMenu`：在终端打开 / 复制路径 / 下载 / 与 HEAD 对比 / 删除 |
| 联动 | 点文件 → 编辑区开 tab；agent 改了文件 → 该节点闪一下 + 计数徽章 |

#### 10.4.3 文件预览

| 类型 | 渲染 | 组件 |
|---|---|---|
| 代码 | Shiki 高亮 + 行号 + 折叠 | `Code Block`（只读）/ CodeMirror 6（编辑） |
| Markdown | 渲染 + 源码切换 | `Tabs` + `streamdown` / `Code Block` |
| 图片 | 缩放/平移/适应 | `img` + 自写 pan-zoom（~80 行） |
| PDF | canvas 逐页 | `pdfjs-dist` + `Button`(翻页) + `Input`(页码) |
| CSV/TSV | 表格，可切原文 | `Data Table` + `Tabs` |
| HTML | `iframe sandbox="allow-scripts"` 或预览域名 | `iframe` |
| Diff | unified / split | `@git-diff-view` |
| 二进制 | 下载卡 | `Card` + `Button` |
| 超大 | 首尾片段 + 下载 | `Code Block` + `CodeBlockFooter`("下载完整文件") |

#### 10.4.4 Git 面板与 Git 树

```
┌─ Git ──────────────────────────────────────┐
│ fix-cache ↑2 ↓0            [提交] [推送]    │
├────────────────────────────────────────────┤
│ 已暂存 (2)                    [全部取消]    │
│  M internal/cache/v3.go                    │
│  A internal/cache/v3_test.go               │
│ 未暂存 (1)                                  │
│  M internal/cache/lru.go                   │
├─ 历史 ─────────────────────────────────────┤
│ ●─┐ a1b2c3d  fix: cache eviction race  [fix-cache]│
│ │ ● e4f5a6b  chore: bump deps                  │
│ ●─┘ 9c8d7e6  feat: add v3 cache       [main]    │
│   ● 5b4a3c2  init                               │
└────────────────────────────────────────────┘
```

| 元素 | 组件 |
|---|---|
| 状态头 | `Badge`(分支) + `Badge`(↑↓) + `Button`(提交/推送) |
| 变更分组 | `Collapsible` + `Item` + 状态字母 |
| 提交框 | `Textarea` + `Switch`(amend) + `Button` |
| 历史列表 | 自绘 SVG DAG（§10.6）+ `ScrollArea` |
| ref 标签 | `Badge`（分支 `secondary` / tag `outline` / HEAD `default`） |
| commit 详情 | `Sheet` + `@git-diff-view` |
| 分支操作 | `DropdownMenu`（新建/切换/删除/合并/rebase） |

#### 10.4.5 终端

| 元素 | 实现 |
|---|---|
| tab 条 | `Tabs`，可 `+` 新建、双击重命名、`×` 关闭；**agent 执行的命令自动开一个只读 tab** |
| 渲染 | `@xterm/xterm` + `addon-fit` + `addon-webgl` + `addon-search` |
| 主题 | 跟随应用主题（亮/暗各一套 ANSI 16 色，用 shadcn token 派生） |
| 跟随标识 | 只读 tab 带 `Badge`("agent")，输入被禁用，右上角"接管"按钮 |
| 工具条 | 清屏、复制、搜索、字号 +/−、全屏 |
| 暂停态 | 整体降透明度 + `Marker`("工作区已暂停，唤醒后恢复") |
| 重连 | 断线自动重连 + "已恢复 N 行"提示 |

#### 10.4.6 浏览器面板

| 元素 | 实现 |
|---|---|
| 画布 | `<canvas>` 绘制 CDP 帧（`requestAnimationFrame` 合并，避免抖动） |
| 地址栏 | `Input Group`：后退/前进/刷新 + URL + 端口快捷 `Select` |
| 模式 | `Badge`("观察中" / "你正在操作") + `Button`("接管"/"交还") |
| tab | 多页 `Tabs` + 缩略图 |
| 空态 | `Empty`("agent 还没打开网页，或输入一个地址") |
| 延迟 | 显示帧延迟（`< 150ms` 为绿） |

#### 10.4.7 分支树与对比

```
┌─ 分支 ───────────────────────────────────────┐
│ ● main        12 轮 · 3 文件 · $0.42   [当前] │
│ ├─ ○ fix-cache  8 轮 · 5 文件 · $0.31  ●运行  │
│ │   └─ ○ fix-cache-2  2 轮 · 1 文件          │
│ └─ ○ alt-approach 15 轮 · 7 文件 · $0.55 ⏸暂停│
│                                  [对比选中 2 个]│
└──────────────────────────────────────────────┘
```

| 元素 | 组件 |
|---|---|
| 树 | `Collapsible` 嵌套 + `Item`（或用 `Trace` 的缩进样式） |
| 状态 | `Badge`（`●运行` / `○空闲` / `⏸暂停` / `✓已采纳`） |
| 操作 | `DropdownMenu`：执行/暂停/重命名/采纳/删除/对比 |
| 对比视图 | `Tabs` 分组 + 三列 `Resizable`：diff / 测试结果 / 指标（token、耗时、改动行数） |

#### 10.4.8 Sidecar 面板

```
┌─ Sidecar ─────────────────── [×] ─┐
│ 模型 lightweight-4o ▾  只读工具 ✓  │
├───────────────────────────────────┤
│ 头像 Bubble: 这条命令啥意思？      │
│ 助手 Bubble: `git worktree prune`… │
│                                   │
│ 引用 ┌──────────────────────────┐  │
│      │ @main:msg_12  "改缓存"    │  │
│      └──────────────────────────┘  │
├───────────────────────────────────┤
│ ┌───────────────────────────────┐ │
│ │ 问点什么…              ↑      │ │
│ └───────────────────────────────┘ │
└───────────────────────────────────┘
```

| 元素 | 组件 |
|---|---|
| 容器 | 右侧 `Resizable` 面板（可拖宽、可收起为图标条） |
| 头部 | `Select`(模型) + `Switch`(只读工具) + `Button`(提升为分支) |
| 消息 | `Message` + `Bubble`（同主会话渲染，但更紧凑） |
| 引用卡 | `Attachment` 样式："@main:msg_12" + hover 预览原文 |
| 输入 | `Input Group`（单行，`Enter` 发送） |
| 多实例 | `Tabs`（最多 3） |

**引用是怎么实现的**（重要）：sidecar 的引用**不是**共享上下文，而是把被引用内容作为一个 `custom_message`（通过平台 extension 的 `pi.sendMessage()`）注入到 sidecar 自己的 session。Pi 的 `custom_message` entry 支持参与 LLM 上下文（`display: true` 还会显示在 UI）。这样引用是**显式、可审计、可撤销**的。

### 10.5 六个关键交互规格

#### 10.5.1 Composer 双队列

| 操作 | 键 | 语义 | 后端 |
|---|---|---|---|
| 发送 | `Enter`（空闲） | 开始 run | `prompt` |
| 下一轮 | `Enter`（运行中） | 等 agent 自然结束再处理 | `follow_up` |
| 立即改口 | `⌘/Ctrl + Enter`（运行中） | 当前 turn 工具执行完后注入 | `steer` |
| 停止 | `Esc` / `■` | 清空队列 + abort，文本回填 | `clear_queue` → `abort` |
| 换行 | `Shift + Enter` | — | — |

**文案必须区分**："下一轮执行" vs "立即打断并改口"。这不是文案洁癖——两个按钮背后是 Pi 的两条不同路径，用错会让用户觉得"它没听我的"。

#### 10.5.2 跟随终端

```
agent 调 bash
   → tool_execution_start  → 自动切到"agent" tab，标题显示命令首行
   → tool_execution_update → 追加输出（rAF 合并，60fps）
   → tool_execution_end    → 追加退出码行（绿/红） + 停止跟随
```

- 自动切换可关（设置里 `autoFollowTerminal`）。
- 用户手动切到别的 tab 后，**不再抢焦点**（只在 tab 上显示未读圆点）。
- 并行分支时，每个分支一个 tab，标题带分支名。

#### 10.5.3 浏览器接管

```
OBSERVE ──[点"接管"]──▶ 确认对话框 ──▶ TAKEOVER
   ▲                                      │
   └────────[点"交还"] / 30s 无操作 ◀──────┘
```

- 接管期间：agent 的 `mcp__playwright__*` 工具调用被 Bridge 拦住，返回 `{block:true, reason:"用户正在操作浏览器"}`，模型会看到并等待。
- 界面上有醒目的"谁在控制"，避免用户以为 agent 在操作其实自己被点了。
- 30 秒无操作自动交还（可关）。

#### 10.5.4 分叉

```
在某条用户消息的 hover 工具条上：[复制] [分叉] [↺ 回到这里]
                              ↓ 点"分叉"
┌────────────────────────────────────────┐
│ 从这里分叉，创建新分支                  │
│ 名称  [fix-cache__________]            │
│ 模型  [继承当前 ▾]                      │
│ ☑ 复制当前未提交的改动到这个分支         │
│                        [取消] [创建]    │
└────────────────────────────────────────┘
```

创建后：新 worktree（`git worktree add`）、新 Pi 进程、左栏多一条分支、**当前视图切到新分支**（用户刚点的就是要看它）。

#### 10.5.5 Sidecar 引用

主会话里任意消息/文件/diff 的 hover 工具条有"发送到 Sidecar"。点击后：sidecar 的 composer 上方出现一个 `Attachment` 引用卡，发送时一并注入（`custom_message`）。

**不能**做"把整个主会话历史塞给 sidecar"——那等于复制一份上下文，既贵又乱。

#### 10.5.6 沙箱暂停/唤醒

- 状态条常驻（`Badge` + 倒计时 tooltip）。
- `PAUSED` 时：消息流照常可读，文件/终端/浏览器面板显示"唤醒后才能用"+ 一个按钮。
- 唤醒中：`Skeleton` + 进度文案（"恢复文件系统 → 恢复进程 → 就绪"）。
- 唤醒后：终端自动重连并回放；文件树自动刷新。

### 10.6 Git 图（自绘 SVG）实现要点

```ts
// 1) 数据：一条命令拿全（服务端执行，前端只做布局）
//    git log --all --date-order --parents \
//      --pretty=format:'%H%x09%P%x09%an%x09%at%x09%s%x09%D'
//
// 2) lane 分配（贪心）
function assignLanes(commits: Commit[]): Map<string, number> {
  const laneOf = new Map<string, number>();   // commit -> lane
  const used: (string | null)[] = [];         // lane -> 占用的 commit
  for (const c of commits) {
    let lane = used.findIndex(h => h === c.hash);       // 已被某个子 commit 预留
    if (lane < 0) lane = used.findIndex(h => h === null) ?? used.length;
    laneOf.set(c.hash, lane);
    used[lane] = c.parents[0] ?? null;                  // 用第一个父继承 lane
    for (const p of c.parents.slice(1)) {               // 其余父开新 lane（merge）
      let pl = used.findIndex(h => h === null);
      if (pl < 0) pl = used.length;
      used[pl] = p;
      laneOf.set(`p:${p}:${c.hash}`, pl);
    }
  }
  return laneOf;
}

// 3) 边：子 → 父，lane 变化时用 cubic bezier
//    M x1 y1 C x1 (y1+dy/2), x2 (y1+dy/2), x2 y2
// 4) 虚拟化：只渲染视口内 ±20 行
```

**渲染规格**：
- 行高 `28px`、lane 宽 `18px`、节点半径 `4.5px`
- 配色：lane 从 shadcn 的 `--chart-1..5` 取色（自动适配亮/暗）
- ref 标签：`Badge`（分支 `secondary` / tag `outline` / HEAD `default`）+ 最多显示 3 个，多的用 `+N`
- hover → `HoverCard`（hash、作者、时间、改动文件数）
- 点击 → 右侧 `Sheet` 展示该 commit diff
- 性能：5000 commit 首屏 ≤ 300ms（虚拟化 + 分层 `<g>` + `will-change: transform`）

**为什么自绘而不是用库**：`@gitgraph/js` 的视觉不可控、`react-flow` 为了通用性带了大量我们不需要的能力（拖拽、节点编辑器），而 lane 布局算法本身只有 ~100 行。**我们只写算法，不写视觉**——颜色、字号、圆角全部用 shadcn token。

### 10.7 我们的私有 shadcn registry

把 agent 专属的复合组件做成内部 registry，其他内部产品可 `npx shadcn@latest add @piforge/<name>`：

```json
// 消费方 components.json
{ "registries": { "@piforge": "https://ui.piforge.internal/r/{name}.json" } }
```

| registry 项 | 内容 | 依赖 |
|---|---|---|
| `@piforge/session-stream` | 会话流装配（Scroller + Message + Trace） | message-scroller, message, bubble, trace |
| `@piforge/tool-trace` | 工具调用单行 + 展开 + 完整输出 | trace, code-block |
| `@piforge/file-tree` | 虚拟化文件树（含 git 状态） | `react-arborist` |
| `@piforge/file-preview` | 多类型预览分发 | code-block, tabs, `pdfjs-dist`, `@tanstack/react-table` |
| `@piforge/git-graph` | commit DAG（自绘 SVG） | `badge`, `hover-card`, `sheet` |
| `@piforge/diff-panel` | diff + 作用域 + hunk 操作 | `@git-diff-view` |
| `@piforge/terminal-pane` | xterm 封装 + 重连 + 主题 | `@xterm/xterm` |
| `@piforge/browser-pane` | CDP 帧渲染 + 输入转发 | — |
| `@piforge/branch-tree` | 分支树 + 状态 | collapsible, item, badge |
| `@piforge/sidecar-dock` | sidecar 面板 | resizable, tabs |
| `@piforge/context-meter` | 上下文用量 + 压缩事件 | chart |

好处：设计一致性有强制力（代码分发而非文档约定）、Shiki 配置只维护一份、新内部产品接入成本趋近 0。

### 10.8 主题与无障碍

- **主题**：只用 shadcn CSS 变量 + Tailwind v4（无配置文件）。`light` / `dark` / `system` + `high-contrast`。
- **密度**：`--density-*` 控制行高与间距（`compact` / `comfortable`）。
- **终端配色**：从主题变量派生 ANSI 16 色，保证亮暗都好看。
- **无障碍**：
  - 会话流 `role="log"` `aria-relevant="additions"`，streaming 时 `aria-busy="true"`（`MessageScroller` 原生支持）；
  - 全部交互可键盘到达；`⌘K` 是键盘用户的完整替代路径；
  - 状态变化（run 完成、审批/错误、沙箱唤醒）走 `aria-live="polite"`；
  - 颜色不单独承载语义（git 状态同时有字母 M/A/D/U）；
  - 对比度 ≥ WCAG AA。

### 10.9 性能预算

| 项 | 目标 | 手段 |
|---|---|---|
| 会话首屏（50 轮历史） | ≤ 1.2s | RSC 预取 + 事件分页 + `content-visibility` |
| 流式渲染 | 60fps，无 > 50ms 长任务 | 增量只更新文本节点；rAF 合并 |
| 文件树（10 万文件） | 首屏 ≤ 800ms | 服务端按需展开（`?depth=2`）+ 虚拟化 |
| 文件预览（1MB 代码） | ≤ 500ms | 服务端 Shiki 高亮 + 流式返回 |
| Git 图（5000 commit） | ≤ 300ms | 虚拟化 + 分层 SVG |
| 终端 | ≤ 2MB/s 不丢、延迟 ≤ 80ms | webgl renderer + ring buffer + 限速 |
| 浏览器帧 | ≤ 150ms 端到端 | CDP `quality=60`、rAF 合并、丢旧帧 |
| 首屏 JS | ≤ 250KB gzip | Shiki 按语言懒加载；CM6/xterm/pdfjs 动态 import |

---

## 11. 非功能需求

### 11.1 安全

| 项 | 要求 |
|---|---|
| 凭据 | 沙箱内零明文（CI 硬测试：对沙箱全盘 `grep -rE 'sk-\|ghp_\|xoxb-'` 必须 0 命中）；DB 里 AES-GCM |
| 网络 | 沙箱出站默认按需；阻断云元数据（`169.254.169.254`、`fd00:ec2::254`、ULA、CGNAT、RFC1918，**在解析后 IP 上校验**）；无 SA token |
| 隔离 | 沙箱粒度 `(project,user)`；微VM 默认；只读根 + 唯一可写 `/home/dev` |
| 路径 | 文件 API 严格 `Clean` + 前缀校验 + 防 symlink 逃逸（`openat2`/`RESOLVE_BENEATH`） |
| 预览 | 签名 token 绑定 `(user, sandbox, port, exp)`；不用通配符 cookie |
| 浏览器 | 实例不跨用户共享；profile 在 workspace 内 |
| 注入 | 仓库内容、issue、MCP 返回一律视为不可信；工具调用走基准沙箱权限 |
| 多用户 | 所有查询强制 `user_id`；admin 支持模式是独立代码路径且留痕 |

### 11.2 性能与容量

| 指标 | 目标 |
|---|---|
| 会话打开 → 首屏 | ≤ 1.2s（DB 读，不等沙箱） |
| 沙箱唤醒（从 PAUSED） | p50 ≤ 3s，p95 ≤ 8s |
| 沙箱创建（冷，通用池未命中） | 必须有进度与排队提示；目标 p95 ≤ 60s |
| 事件端到端（Pi → 浏览器） | p95 ≤ 300ms |
| 单实例并发沙箱 | 100（M3） |
| 单实例并发 SSE 连接 | 5k |
| 单沙箱内并发 Pi 进程 | ≤ 5 |

### 11.3 可用性

- 控制面无状态化（状态在 DB + NATS），滚动发布不掉连接（SSE 自动重连 + `Last-Event-ID`）。
- 沙箱故障不影响 transcript；`MISSING` 时会话只读 + "从快照恢复"。
- 目标 SLA：99.5%（内部工具）。

### 11.4 可观测性

- **一个 run 一条 trace**：`api → session → bridge → sandbox → llm`，span 下钻到每个 tool call。
- SLI：首屏延迟、沙箱唤醒 P95、沙箱池命中率、事件延迟、PTY 丢字节率、浏览器帧延迟。
- 日志：结构化 + `session_id`/`branch_id` 贯穿；**日志禁止出现工具参数明文**（用 `args_hash`）。

---

## 12. 里程碑

按 1.5 人后端 + 1.5 人前端 + 0.5 人设计 + 0.5 人 SRE 估算。

| 里程碑 | 周期 | 交付 | 出口标准 |
|---|---|---|---|
| **M0 骨架** | 3 周 | 沙箱 + Pi RPC Bridge + 会话流 + 文件树 + 基础 diff + SSE | 能提任务、看 agent 改代码、看 diff、关页面重开不丢 |
| **M1 可用** | +4 周 | 终端（跟随 + 交互）、文件预览（代码/MD/图片）、Git 面板、BYOK 模型、多用户 | 5 个真人日用一周；无 P0；首屏 ≤ 1.5s |
| **M2 工作台** | +4 周 | Git 图、文件预览补齐（PDF/CSV/HTML）、浏览器面板、Skill、MCP、沙箱暂停/唤醒 | 浏览器能人和 agent 共用；唤醒 ≤ 3s |
| **M3 并行** | +4 周 | 分叉 + 多分支 + 对比视图 + Sidecar + 私有 registry | 并行 3 分支稳定；sidecar 日均使用 ≥ 30 次 |
| **M4 打磨** | +3 周 | 性能、warm pool、memory 快照、大仓适配、快捷键与可访问性 | 10 万文件仓库可用；热池命中 ≥ 80% |

### 12.1 关键路径

```
M0 ──▶ M1 ──▶ M2 ──▶ M3 ──▶ M4
 │      │      │      │
 │      │      │      └─ 依赖：worktree 管理稳定（M1 就要做，不能等到 M3）
 │      │      └─ 依赖：memory 快照（否则暂停会杀掉 dev server）
 │      └─ 依赖：LLM 网关（否则换模型要重启沙箱）
 └─ 最长风险：Pi RPC 流式重建的正确性
```

### 12.2 必须提前做的三个 PoC（都在 M0）

1. **流式重建**：构造 20 种 delta 序列（截断的 tool call、并发工具、中途 abort、空 thinking），验证渲染与 `message_end` 一致。
2. **沙箱首条 shell 输出延迟**：真实 Go/Node/Rust 镜像上测 Firecracker vs gVisor，**量的是端到端，不是 VMM 启动**。
3. **worktree + 缓存共享**：两个 worktree 并行 `npm ci`，验证缓存不坏、`git worktree remove` 后主仓干净。

---

## 13. 风险与开放问题

### 13.1 风险

| # | 风险 | 影响 | 概率 | 对策 |
|---|---|---|---|---|
| R1 | Pi 升级破坏 RPC 契约 | 高 | 中 | 钉版本 + 录制回放契约测试 + 升级前跑全量会话回归 |
| R2 | 沙箱冷启动太慢 | 高 | 中 | 通用 warm pool + 排队可见 + 首屏不等沙箱（DB 优先） |
| R3 | 凭据意外泄漏 | 极高 | 中 | 网关 + 短期令牌 + CI grep 硬测试 + 出站按需 |
| R4 | worktree 与缓存并发写坏 | 中 | 高 | 默认分支互斥 + 缓存写锁 + `prune` 必做 |
| R5 | 浏览器投屏延迟/卡顿 | 中 | 中 | `quality=60` + rAF 合并 + 丢旧帧 + 显示延迟指标 |
| R6 | 大仓（>10 万文件）拖死前端 | 中 | 高 | 服务端分页树 + 虚拟化 + `⌘P` 替代浏览 |
| R7 | memory 快照不稳定 | 中 | 中 | 降级到 fs-only（会杀进程，需明确提示） |
| R8 | Sidecar 被用成"第二个主会话"导致体验混乱 | 低 | 中 | 只读工具默认 + UI 明确分区 + 最多 3 个 |
| R9 | 并行分支让用户迷失（不知道改在哪） | 中 | 高 | 分支色标 + 每个面板标分支名 + 对比视图 |

### 13.2 开放问题

1. **沙箱粒度 `(project,user)` 的边界**：用户在 3 个项目上工作 = 3 个沙箱，资源 ×3。是否需要"暂停不用的项目"策略？（建议：`IDLE 15min` 暂停，但用户切回要 ≤3s，所以 prefer fs+mem 快照）
2. **Sidecar 是否要给 `bash` 只读能力**？给了就能自己跑 `grep`（更实用），但和主会话抢 CPU。建议：给，但限 cgroup 权重低。
3. **分支上限**：3 还是 5？建议默认 3、设置里可调 5，超过要求归档。
4. **Git 图的数据量**：`--all` 在巨型单仓（Linux kernel 级）上很慢。是否需要"只看当前分支 + 最近 N 天"的降级策略？
5. **内置浏览器是否需要"无头模式以外"的窗口**（多窗口、文件下载、打印）？V1 只做单窗口。
6. **用户离职/账号删除**：workspace 与快照保留多久？（建议 30 天软删除）
7. **是否需要"导入 Cursor/Claude Code 的会话"**？技术上做不了（格式不同），但可以导入它们的 `mcp.json` / `AGENTS.md` / `skills`。建议 M3 做一个导入向导。

---

## 14. 附录

### 14.1 Pi 事件字典（Bridge 必须完整处理）

| 类别 | 事件 | 备注 |
|---|---|---|
| Agent | `agent_start` / `agent_end` / `agent_settled` | **`agent_settled` 才是结束** |
| Turn | `turn_start` / `turn_end` | `turn_end` 带 `toolResults` |
| Message | `message_start` / `message_update` / `message_end` | update 是纯增量 |
| Tool | `tool_execution_start` / `_update` / `_end` | `_end` 带 `durationMs`；**跟随终端的数据源** |
| Queue | `queue_update` | 完整队列，非增量 |
| Compaction | `compaction_start` / `compaction_end` | `reason: manual\|threshold\|overflow` |
| Retry | `auto_retry_start` / `_end`、`summarization_retry_*` | 前端显示"重试 1/3" |
| Session | `entry_appended` / `session_info_changed` / `thinking_level_changed` | |
| Bash | `bash_execution_update` | 仅直连 bash 命令 |
| Error | `extension_error` | extension 崩了必须可见 |

**Pi RPC 命令清单**：`prompt`、`steer`、`follow_up`、`abort`、`clear_queue`、`new_session`、`get_state`、`get_messages`、`set_model`、`cycle_model`、`get_available_models`、`set_thinking_level`、`cycle_thinking_level`、`get_available_thinking_levels`、`set_steering_mode`、`set_follow_up_mode`、`compact`、`set_auto_compaction`、`set_auto_retry`、`abort_retry`、`bash`、`abort_bash`、`get_session_stats`、`export_html`、`switch_session`、`fork`、`clone`、`get_fork_messages`、`get_entries`、`get_tree`、`get_last_assistant_text`、`set_session_name`、`get_commands`。

> **对分叉功能重要的一条**：`fork` 只能从**用户消息**分叉（`get_fork_messages` 返回可分叉点列表）。所以 UI 上的"分叉"按钮只出现在 user message 上，不要出现在任意位置。

### 14.2 术语表

| 术语 | 含义 |
|---|---|
| **Project** | 一个 Git 仓库 + 一个用户 |
| **Sandbox** | 隔离的执行环境，粒度 `(project, user)`，长生命周期，可暂停 |
| **Worktree** | 沙箱内一个 git worktree，对应一条分支 |
| **Session** | 一次对话，三种形态：`main` / `branch` / `sidecar` |
| **Branch（分支会话）** | 从某条用户消息 fork 出来的独立会话，独立 worktree |
| **Sidecar** | 附着在主会话上的旁路对话，共享 worktree、独立上下文 |
| **Run** | 一次 `prompt` → `agent_settled` |
| **Turn** | 一次模型响应 + 它触发的工具调用 |
| **Follow 终端** | 只读镜像 agent 执行的命令输出 |
| **Interactive 终端** | 用户自己的 PTY，独立于 agent |
| **DAG / Git 树** | commit 的有向无环图可视化 |

### 14.3 参考资料

**Agent 内核**
- [earendil-works/pi](https://github.com/earendil-works/pi) — RPC、JSON 事件流、session 格式、skills、MCP、extensions、security 均在 `packages/coding-agent/docs/`
- [Pi Agent 架构解读](https://pinktalk.online/computer_sci/llm/agent/pi_agent_architecture)

**参考项目**
- [fastclaw-ai/fastclaw](https://github.com/fastclaw-ai/fastclaw) — BYOK 与 `scope` shadow 继承
- [HKUDS/nanobot](https://github.com/HKUDS/nanobot) — 多窗格 workbench、上下文用量可视化
- [umeiko/umeko-desktop](https://github.com/umeiko/umeko-desktop) — 会话/项目侧栏、折叠式思考

**前端**
- shadcn/ui：[组件](https://ui.shadcn.com/docs/components) · [AI Elements](https://ui.shadcn.com/docs/ai-elements) · [Blocks](https://ui.shadcn.com/blocks) · [Registry](https://ui.shadcn.com/docs/registry)
- [ElevenLabs UI](https://ui.elevenlabs.io/) · [源码](https://github.com/elevenlabs/ui)
- [Streamdown](https://streamdown.ai) · [Shiki](https://shiki.style) · [xterm.js](https://github.com/xtermjs/xterm.js) · [CodeMirror](https://codemirror.net) · [react-arborist](https://github.com/brimdata/react-arborist) · [pdf.js](https://mozilla.github.io/pdf.js/)

**浏览器投屏与自动化**
- [Chrome DevTools Protocol](https://chromedevtools.github.io/devtools-protocol/) · [Playwright MCP](https://github.com/microsoft/playwright-mcp)

**基础设施**
- [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox)（含[性能调优](https://github.com/kubernetes-sigs/agent-sandbox/blob/main/docs/performance-tuning.md)）
- [Firecracker](https://firecracker-microvm.github.io/) · [gVisor checkpoint/restore](https://gvisor.dev/docs/user_guide/checkpoint_restore/)
- [NATS JetStream](https://docs.nats.io/)

**调研底稿**（`research/` 目录）
- [`research/agent-platform-feature-matrix.md`](research/agent-platform-feature-matrix.md) — 竞品功能矩阵与行为细节
- [`research/agent-sandbox-isolation-briefing.md`](research/agent-sandbox-isolation-briefing.md) — 沙箱、快照、warm pool 的实测数据与陷阱

---

## 15. 一页纸总结

1. **定位**：一个人的私享 coding agent 工作台，不是治理平台。没有组织、没有审批、没有审计、没有计费。
2. **两种角色**：`user` / `admin`。admin 管人、管共享模型、管镜像，**默认不看别人代码**。
3. **一个沙箱，多 worktree**：沙箱粒度 `(project, user)`；分支 = worktree = 一个 Pi 进程；共享依赖缓存换来"并行几乎免费"。
4. **凭据走网关**：用户自带 key，沙箱里只有一个 5 分钟的临时令牌。换模型不用重启沙箱。
5. **会话三层**：`main`（主线）/ `branch`（分叉，独立 worktree）/ `sidecar`（旁路，共享 worktree、独立上下文）。分叉是 Pi 原生能力的产品化。
6. **终端两个**：跟随（只读镜像 agent，数据来自官方事件流）+ 交互（用户自己的 PTY，master 在沙箱内）。
7. **浏览器一个实例两种用法**：agent 用 Playwright MCP 驱动，人用 CDP 投屏看/接管。**同一实例**。
8. **前端零新造**：shadcn 的 `MessageScroller` / `Trace` / `Code Block` / `Bubble` 全用上；只有 5 个例外（xterm、CM6、git-diff-view、react-arborist、pdfjs），外加一个自绘的 Git DAG（只写算法不写视觉）。
9. **状态永远可见**：运行中 / 唤醒中 / 排队中 / 谁在操作浏览器 —— 每个等待都要有原因。
10. **Pi 的 `agent_settled` 才是结束**，`agent_end` 不是。这句话值得写进每个后端的注释。

---

*文档结束 · 未决问题见 §13.2*
