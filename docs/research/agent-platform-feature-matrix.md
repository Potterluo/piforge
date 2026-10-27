# AI Coding-Agent Feature Matrix & Behavioral Notes
### Research input for a PRD: Web-based, multi-tenant coding-agent platform (Go backend, shadcn/ui frontend)

Compiled 2026-10-27 from vendor documentation (observable behavior only, no marketing claims).
DSH entries are observed directly from the running harness's tool contracts and session behavior
(this session: `danger-full-access` file policy, approvals auto-rejected).

---

## 1. Feature matrix

Legend: **CDX** = OpenAI Codex · **CC** = Claude Code · **DSH** = DeepSeek Harness · **CUR** = Cursor · **OTH** = Devin / Factory / Amp / OpenHands.

### 1.1 Task & execution model

| Feature | Who has it | One-line behavioral note |
|---|---|---|
| Thread/task as unit | CDX, CC, DSH, CUR, OTH | Every product models a persistent conversation object that owns turns; Codex app-server exposes `thread/start`, `thread/resume`, `thread/fork`, `turn/start`, `turn/steer`, `turn/interrupt` as JSON-RPC ([app-server](https://learn.chatgpt.com/codex/app-server)). |
| Parallel work isolation | CDX, CC, DSH, CUR, OTH | Codex desktop runs Local / **Worktree** / Cloud chats; managed worktrees live under `$CODEX_HOME/worktrees`, detached HEAD, LRU-capped at 15, snapshot-on-delete ([worktrees](https://learn.chatgpt.com/codex/environments/git-worktrees)). Claude Code subagents can use `isolation: worktree` ([sub-agents](https://docs.claude.com/en/docs/claude-code/sub-agents)); DSH has `create_worktree`; Cursor uses cloud VMs. |
| Cloud VM per task | CDX, CUR, DSH(no), OTH | Codex cloud tasks get 2 vCPU/8 GiB (Plus) or 4 vCPU/16 GiB/32 GiB disk (Pro+), saved VM state recoverable ~7 days ([cloud envs](https://learn.chatgpt.com/codex/environments/cloud-environments)). Cursor Cloud Agents = isolated VMs, many in parallel ([cloud agent](https://cursor.com/docs/cloud-agent)). Amp "Orbs" = machine per thread ([amp](https://ampcode.com/docs)). |
| OS-level sandbox on exec | CDX, CC, DSH | Codex: Seatbelt (macOS), bwrap+seccomp (Linux), MXC/Windows; applies to spawned subprocesses, not just built-in file ops ([sandboxing](https://learn.chatgpt.com/docs/sandboxing)). DSH: `read-only` / `workspace-write` / `danger-full-access` modes with a separate approval policy. |
| Network default-off | CDX, CC, DSH | Codex `workspace-write` keeps network off unless `sandbox_workspace_write.network_access = true`; optional `network_proxy` with allow/deny domain rules, `deny` wins, `allow_local_binding=false` blocks loopback/private, DNS-rebinding check ([approvals](https://www.codex-docs.com/en/docs/agent-approvals-security)). Cloud setup phase can reach network; secrets are removed before the agent phase. |
| Cloud egress allowlist | CDX, CUR, OTH | Codex cloud has a "Package managers" preset (npm/PyPI/crates/Go/Maven/GitHub/apt...) plus custom domains, and network **secrets** are injected as placeholders substituted by a proxy for allowed HTTPS/443 destinations only ([cloud envs](https://learn.chatgpt.com/codex/environments/cloud-environments)). |
| Environment setup script + cache | CDX, CUR, OTH | Codex cloud environment = repos + **Install script** + **Start skill** + env vars + secrets; new tasks start from the published prepared filesystem, existing tasks keep their own uncommitted state, and repository refresh preserves dependency caches without re-running install/start ([cloud envs](https://learn.chatgpt.com/codex/environments/cloud-environments)). Cursor: agent-led setup, saved snapshot, or `Dockerfile` in `.cursor/environment.json`, plus background "Builds". |
| Non-interactive/CI mode | CDX, CC, OTH | `codex exec --sandbox workspace-write` ([approvals](https://www.codex-docs.com/en/docs/agent-approvals-security)); `claude -p`. |
| Long-running goal loop | CDX, CC, DSH, CUR | Codex `/goal` turns the prompt into first message **and** completion criteria, with a progress row to pause/resume/edit/clear, and `thread/goal/set` carrying `tokenBudget`, `tokensUsed`, `timeUsedSeconds` ([long-running](https://learn.chatgpt.com/codex/long-running-work), [app-server](https://learn.chatgpt.com/codex/app-server)). DSH `create_goal` + automatic continuation rounds, capped by `max_goal_rounds`. Cursor `/goal`. |
| Scheduled/recurring runs | CDX, DSH, OTH | Codex scheduled tasks can run on dedicated background worktrees ([worktrees](https://learn.chatgpt.com/codex/environments/git-worktrees)); DSH `schedule_create` with cron/daily/weekly/interval; Devin scheduled sessions ([devin](https://docs.devin.ai/work-with-devin/advanced-capabilities)). |
| PR/VCS handoff | CDX, CUR, OTH | Cursor pushes to a separate branch and produces "merge-ready PRs with artifacts" ([cloud agent](https://cursor.com/docs/cloud-agent)); Codex cloud: review changes, then commit/open PR; Codex can push a fix back to the PR branch when it has permission ([github](https://learn.chatgpt.com/docs/third-party/github)). |

### 1.2 Trust, approval and review

| Feature | Who has it | One-line behavioral note |
|---|---|---|
| Layered approval policy | CDX, CC, DSH | Codex: `approval_policy` = `on-request` / `never` / granular `{sandbox_approval, rules, mcp_elicitations, request_permissions, skill_approval}`, plus `approvals_reviewer = "auto_review"` routing eligible requests to a reviewer agent ([approvals](https://www.codex-docs.com/en/docs/agent-approvals-security)). |
| Permission modes | CC, DSH, CUR | Claude Code: `default` (label "Manual"), `acceptEdits`, `plan`, `auto` (classifier reviews shell/network), `dontAsk` (auto-deny anything that would prompt), `bypassPermissions`; workspace trust dialog gates project rules/hooks ([permissions](https://docs.claude.com/en/docs/claude-code/permissions)). |
| Rule precedence | CC, CDX | CC evaluates **deny → ask → allow**, first match wins, specificity does not reorder; bare tool name in `deny` removes the tool from model context entirely ([permissions](https://docs.claude.com/en/docs/claude-code/permissions)). |
| Diff review pane | CDX, CUR, CC(web/desktop), OTH | Codex review pane scopes: Unstaged / Staged / Commit / Branch / **Last turn**, with per-file and per-hunk stage/unstage/revert, multi-repo selector, and line-level "+" comments delivered as review guidance ([code review](https://learn.chatgpt.com/codex/code-review)). |
| Review agent | CDX, CC, CUR | `@codex review` on a PR reacts 👀 then posts a normal GitHub review, flags only **P0/P1** ([github](https://learn.chatgpt.com/docs/third-party/github)); `/review` locally starts a dedicated reviewer that does not touch the working tree, with scopes base-branch / uncommitted / commit / custom instructions. |
| Checkpoint / rewind | CC, CUR, CDX(worktree snapshots) | CC `/rewind` or double-Esc opens a menu: Restore code and conversation / Restore conversation / Restore code / Summarize from here / Summarize up to here; snapshots kept for the 100 most recent checkpoints, saved with the conversation so they survive resume ([checkpointing](https://docs.claude.com/en/docs/claude-code/checkpointing)). Cursor: checkpoint click previews files, restore reverts files only, never messages ([agent](https://cursor.com/docs/agent/overview)). |
| Per-turn revert | CDX, DSH | `thread/revert` removes `beforeTurnId` and all later turns from history without touching files ([app-server](https://learn.chatgpt.com/codex/app-server)). |
| Session replay / event audit | OTH, CDX | OpenHands exposes `GET /conversation/{id}/events/search` and a downloadable trajectory ([openhands](https://docs.openhands.dev/enterprise/conversations-and-sandboxes)); Devin MCP can list event summaries, fetch event contents and search across events ([devin](https://docs.devin.ai/work-with-devin/advanced-capabilities)). |
| Cost/credit meter | OTH, CC, CDX | Devin meters **ACUs** per session and lets a coordinator monitor child-session ACU consumption and set per-child ACU limits ([devin](https://docs.devin.ai/work-with-devin/advanced-capabilities)); Codex OTel emits token counts on `response.completed` ([approvals](https://www.codex-docs.com/en/docs/agent-approvals-security)). |

### 1.3 Context, extension and UX surface

| Feature | Who has it | One-line behavioral note |
|---|---|---|
| Repo instruction file | CDX, CC, DSH, CUR | Codex builds an instruction **chain**: global `AGENTS.override.md`/`AGENTS.md` → each dir from repo root down to cwd (at most one file per dir, override wins), concatenated root→leaf; capped by `project_doc_max_bytes` (32 KiB default); fallback filenames configurable ([AGENTS.md](https://learn.chatgpt.com/codex/agent-configuration/agents-md)). Cursor supports root + nested `AGENTS.md` and `.cursor/rules/*.mdc`. |
| Scoped rules with globs | CUR, CC, CDX | Cursor `.mdc`: `alwaysApply` / `globs` / `description` / manual @-mention; precedence Team → Project → User ([rules](https://cursor.com/docs/context/rules)). |
| Skills (`SKILL.md`) + progressive disclosure | CC, DSH, CDX, CUR | CC: YAML frontmatter (`name`, `description`, `when_to_use`, `allowed-tools`, `disallowed-tools`, `model`, `context: fork`, `agent`, `paths`, `hooks`, `disable-model-invocation`) + body loaded only on use; `/skill-name` or auto-invoked by description; live-reload on file change ([skills](https://docs.claude.com/en/docs/claude-code/skills)). |
| Slash commands | CC, CDX, DSH, CUR | CC: custom commands merged into skills — `.claude/commands/x.md` and `.claude/skills/x/SKILL.md` both yield `/x`; bundled skills (`/code-review`, `/debug`, `/batch`, `/verify`, `/run`) are prompt-based; built-ins (`/help`, `/compact`) are fixed logic ([skills](https://docs.claude.com/en/docs/claude-code/skills)). |
| MCP transports | CC, CDX, DSH, CUR | CC: `stdio`, `http` (`streamable-http` alias), `sse` (deprecated, auto-fallback from http), `ws` (headers only, no OAuth), `--scope local|project|user`, `.mcp.json` project servers need interactive approval, per-server `timeout`, idle timeouts, auto-reconnect w/ 5 backoff attempts ([mcp](https://docs.claude.com/en/docs/claude-code/mcp)). |
| MCP tool search | CC, CDX | CC `ToolSearch` is the default path: tools from a server that finishes connecting mid-turn are listed to the model on the next request; without it, a `WaitForMcpServers` tool is used ([mcp](https://docs.claude.com/en/docs/claude-code/mcp)). |
| Hooks | CC, CDX, CUR, DSH | CC has ~30 lifecycle events incl. `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PermissionRequest`, `PermissionDenied`, `PostToolUse`, `PostToolBatch`, `SubagentStart/Stop`, `PreCompact`/`PostCompact`, `PreModelSwitch`, `Elicitation`, `Stop`, `SessionEnd`; handler types `command|http|mcp_tool|prompt|agent`; exit 2 = block; >12 concurrent prompt hooks queued ([hooks](https://docs.claude.com/en/docs/claude-code/hooks)). |
| Subagents | CC, CDX, DSH, CUR, OTH | CC: markdown + frontmatter in `.claude/agents` or `~/.claude/agents`; `tools`/`disallowedTools`, `model`, `permissionMode`, `maxTurns`, `mcpServers`, `skills` preload, `memory`, `background`; delegation appears in transcript as `name(task)` row; summary returned to parent ([sub-agents](https://docs.claude.com/en/docs/claude-code/sub-agents)). |
| Programmatic fan-out | DSH, CC, CUR, OTH | DSH `workflow` runs a JS script that fans out `agent()` calls with phases + JSON-schema results; `ralph` runs fresh-agent rounds. Cursor "Projects" = coordinator agent that plans and delegates ([agent](https://cursor.com/docs/agent/overview)); Devin = coordinator + managed Devins in isolated VMs. |
| Todo list | CC, DSH | CC exposes `TodoWrite` (and it is in the background-subagent tool allowlist) ([sub-agents](https://docs.claude.com/en/docs/claude-code/sub-agents)); DSH `todo_write` replaces the whole list each call. |
| Plan mode | CC, DSH, CDX | CC `plan`: read-only exploration, no source edits, delegates research to the built-in Plan subagent ([sub-agents](https://docs.claude.com/en/docs/claude-code/sub-agents)); DSH `exit_plan_mode` presents a plan for approval. |
| Compaction | CC, CDX, DSH | CC: auto-compact + `/compact` + targeted "Summarize from here/up to here"; `PreCompact`/`PostCompact` fire with matcher `manual|auto`; subagents get `autoCompactWindow` 100k–1M tokens ([hooks](https://docs.claude.com/en/docs/claude-code/hooks), [sub-agents](https://docs.claude.com/en/docs/claude-code/sub-agents)). Codex: `thread/compact/start` streams a `contextCompaction` item lifecycle ([app-server](https://learn.chatgpt.com/codex/app-server)). |
| Session resume / branch | CDX, CC, DSH | Codex `thread/resume` (pagination cursors, `excludeTurns`), `thread/fork` with `lastTurnId` or `ephemeral: true`, `thread/list` filters by cwd/model/archived/pinned/parent ([app-server](https://learn.chatgpt.com/codex/app-server)). CC keeps checkpoints across resume. |
| Session search | CDX, DSH, CUR, OTH | Codex `thread/list` `searchTerm` (title fragment); Cursor local index, Cmd+K across conversations ([agent](https://cursor.com/docs/agent/overview)); DSH `session_search` / `session_event_trace`. |
| Plugin marketplace | CDX, CC, DSH, CUR | Codex app-server: `marketplace/add|remove|upgrade`, `plugin/install|uninstall`, `plugin/list` (still marked "under development", don't ship against it) ([app-server](https://learn.chatgpt.com/codex/app-server)). DSH: SkillHub skills + DSH plugin market. |
| Streaming event protocol | CDX, CC, OTH | Codex: JSON-RPC notifications `turn/started`, `item/started`, `item/completed`, `item/agentMessage/delta`, `item/permissions/requestApproval` (server→client request), `thread/status/changed` with `activeFlags: ["waitingOnApproval"]`, `turn/completed` ([app-server](https://learn.chatgpt.com/codex/app-server)). |

---

## 2. Deep dives on the eight dimensions

### 2.1 Approval / permission UX
- **Two-layer model is the norm**: a technical boundary (sandbox) plus a decision policy (when to ask). Codex states this explicitly and warns that they are separate controls ([sandboxing](https://learn.chatgpt.com/docs/sandboxing)).
- **Presets over knobs**: Codex "Auto" = `workspace-write` + `on-request`; read-only detection is automatic — on launch it recommends Auto for a version-controlled folder and `read-only` for a non-VCS folder ([approvals](https://www.codex-docs.com/en/docs/agent-approvals-security)).
- **Non-obvious but PRD-relevant details** from Codex's model: protected paths inside writable roots (`.git`, `.agents`, `.codex`, `.aws` are read-only recursively), `--ask-for-approval never` composes with any sandbox, granular approval categories, and an **auto-review** path where a reviewer agent adjudicates eligible requests with risk levels and fail-closed on timeout.
- **Prompt affordances**: CC's prompt offers *Yes* / *Yes, and don't ask again for: `npm test *`* / *Yes, and switch to auto mode* / *No*, plus Tab-to-comment, and "don't ask again" is persisted per repository to `.claude/settings.local.json` while file-edit approvals last only until session end ([permissions](https://docs.claude.com/en/docs/claude-code/permissions)).
- **Deny semantics matter for UI copy**: a bare-tool deny hides the tool from the model (no prompt ever appears); a scoped deny keeps the tool and blocks matching calls.
- **DSH**: approval prompts are a session-level policy; when approvals are disabled, actions that would require approval are auto-rejected rather than silently allowed — i.e. the UI must render "blocked by policy", not "denied by user".

### 2.2 Diff review UX
- Converged shape: **file tree + unified diff + per-hunk actions + line-anchored comments**, with a *scope selector* that is not just "the diff".
- Codex's scope set is the best template: `Unstaged | Staged | Commit | Branch | Last turn`, plus `All repos` for multi-repo projects and a "Last turn" view that isolates what the agent just did ([code review](https://learn.chatgpt.com/codex/code-review)).
- Line comments are **guidance, not GitHub review comments**: they are attached to the diff and then you still send a chat message ("Address the inline comments and keep the scope minimal"). Posting a comment in Summary/Changes goes straight to the provider.
- Cursor/CC checkpoint timelines let you preview file state at any message and restore; Cursor explicitly restores **files only, never messages** ([agent](https://cursor.com/docs/agent/overview)) — a deliberate difference from CC's "restore code and conversation".

### 2.3 Streaming / event UX (what the UI shows)
- Codex app-server is the cleanest published taxonomy to copy for a Go backend: thread-level (`thread/started`, `thread/name/updated`, `thread/status/changed`, `thread/archived`, `thread/closed`), turn-level (`turn/started`, `turn/completed` w/ status incl. `interrupted`), item-level (`item/started`, `item/completed`, `item/agentMessage/delta`), tool/process progress (`command/exec/outputDelta`, `process/outputDelta`, `process/exited`), compaction (`contextCompaction` item), review (`enteredReviewMode`/`exitedReviewMode`), plus server→client **requests** for approvals (`item/permissions/requestApproval`, `mcpServer/elicitation/request`) ([app-server](https://learn.chatgpt.com/codex/app-server)).
- Status is a first-class object: `notLoaded | idle | systemError | active{activeFlags}` and `activeFlags` includes `waitingOnApproval` — this is what a sidebar badge should bind to.
- Backpressure is explicit: bounded queues → JSON-RPC `-32001 "Server overloaded; retry later."` with jittered retry ([app-server](https://learn.chatgpt.com/codex/app-server)). A multi-tenant web console needs the same contract per-tenant.
- Mid-turn input has two semantics worth copying: `turn/steer` (append to in-flight turn) vs queued message. Cursor implements both: Enter queues, Cmd+Enter sends immediately, "Send now"/Enter-twice steers at the next tool call ([agent](https://cursor.com/docs/agent/overview)).

### 2.4 Session persistence & resume
- Persisted as append-only logs per thread (Codex: JSONL rollout + SQLite state DB, paginated history: `thread/turns/list`, `thread/items/list`, `itemsView: notLoaded|summary|full`) ([app-server](https://learn.chatgpt.com/codex/app-server)).
- Resume ≠ reload: `thread/resume` loads into memory and subscribes; `thread/read` returns metadata without loading; `thread/unsubscribe` + 30-min no-subscriber grace → `notLoaded` + `thread/closed`.
- Fork semantics are subtle and PRD-worthy: fork from `lastTurnId`; an in-progress `lastTurnId` is rejected, and forking mid-turn records an **interruption marker**; `ephemeral: true` forks are in-memory only.
- Archival cascades: `thread/archive` also archives spawned descendant threads.
- OpenHands shows the infra alternative: conversation (messages, events, repo selection, usage metrics) is decoupled from sandbox (filesystem, credentials, compute); sandboxes have `STARTING|RUNNING|PAUSED|ERROR|MISSING`, idle-pause, deletion timer, and a hard 12-hour session cap ([openhands](https://docs.openhands.dev/enterprise/conversations-and-sandboxes)).

### 2.5 Context compaction semantics
- Three distinct operations get conflated in marketing and must be separate in a PRD:
  1. **Auto-compact at threshold** (CC auto-compact; Codex context window; subagent `autoCompactWindow` per-agent override 100k–1M) — ([sub-agents](https://docs.claude.com/en/docs/claude-code/sub-agents)).
  2. **Targeted summarize of a range** (CC "Summarize from here" / "Summarize up to here", leaving a "Summarized conversation" marker and keeping the original messages in the transcript so references remain valid) — ([checkpointing](https://docs.claude.com/en/docs/claude-code/checkpointing)).
  3. **Manual compaction as a visible event** (Codex `thread/compact/start` → `contextCompaction` item lifecycle) — ([app-server](https://learn.chatgpt.com/codex/app-server)).
- Hooks around it (`PreCompact`/`PostCompact`, matcher `manual|auto`) imply the UI should show *why* compaction happened.
- Subagent compaction is scoped: a subagent summarizes its own context and returns a summary, so the parent's window is unaffected ([sub-agents](https://docs.claude.com/en/docs/claude-code/sub-agents)).

### 2.6 Skills / slash commands / MCP UX
- **Skills = directory + `SKILL.md` + optional supporting files**, frontmatter gates invocation (`disable-model-invocation`, `user-invocable`), scoping (`paths` globs), execution mode (`context: fork` + `agent` + `background`), and permission grants (`allowed-tools` valid for one turn only) ([skills](https://docs.claude.com/en/docs/claude-code/skills)).
- **Precedence rules are a UX problem**: enterprise > personal > project for same name; built-in alias keeps working when your skill replaces the command; nested `apps/web/.claude/skills` gets `/apps/web:deploy`; skills below cwd don't load until Claude touches that subdir.
- **Discovery/cost control**: the skill *listing* is truncated at 1,536 chars of description+`when_to_use`; subagent descriptions are capped in aggregate (warning at 15,000 tokens) ([sub-agents](https://docs.claude.com/en/docs/claude-code/sub-agents)). This is the real constraint on a skill marketplace UI: show the listing text, not the body.
- **MCP UX**: status surfaces matter more than transports — `✔ Connected`, `! Needs authentication`, `✘ Failed`, `⏸ Pending approval`, `⊘ Disabled for this project`, `cached 2h ago · connects on first use · 5 tools` ([mcp](https://docs.claude.com/en/docs/claude-code/mcp)).
- Long MCP call handling: a main-conversation call still running after 2 min **auto-backgrounds** into `/tasks`, and MCP "tasks" can outlive the session and be re-followed on resume.

### 2.7 Sub-agent orchestration UX
- Three tiers in the market:
  - **Declarative workers**: CC agent files (frontmatter → tools/model/permissions/hooks/MCP scope) invoked implicitly by description or explicitly; transcript shows one compact row per delegation.
  - **Scripted fan-out**: DSH `workflow` (JS with `agent()`, `pipeline`, `parallel`, `phase`, schema-validated results) and `ralph` (fresh context per round, workspace as memory) — best fit when the platform must *record* the orchestration graph.
  - **Coordinator/VM fan-out**: Devin managed Devins (each in its own VM, coordinator can message children, gets woken on completion without polling, monitors ACU per child, sleeps/terminates children) ([devin](https://docs.devin.ai/work-with-devin/advanced-capabilities)); Cursor Projects; Amp Puck.
- CC's guardrails worth copying: depth limit, `Agent(agent_type)` allowlist in `tools`, background subagents get a **reduced built-in tool set** (no `AskUserQuestion`, no `EnterPlanMode`, etc.), and `Agent` tool deny disables delegation entirely.
- Known gap to design around: **subagent edits are usually not captured in parent checkpoints** (CC docs say rewind restores only files the parent already edited) ([checkpointing](https://docs.claude.com/en/docs/claude-code/checkpointing)).

### 2.8 Cost / token display
- Devin: ACU meter per session, visible to the coordinator, with per-child ACU limits ([devin](https://docs.devin.ai/work-with-devin/advanced-capabilities)) — the only product documented as a *budget* control rather than a *report*.
- Codex: goal carries `tokenBudget` / `tokensUsed` / `timeUsedSeconds` and resets accounting when the objective changes ([app-server](https://learn.chatgpt.com/codex/app-server)); OTel `codex.sse_event` carries token counts on `response.completed`, and auto-review consumes extra model calls ([approvals](https://www.codex-docs.com/en/docs/agent-approvals-security)).
- CC: subagent requests count against the same usage limits as the main conversation ([sub-agents](https://docs.claude.com/en/docs/claude-code/sub-agents)); `/cost`, `/usage`, `/context` are the in-product surfaces, and `/doctor` recommends plan-usage reductions from the last 7 days ([skills](https://docs.claude.com/en/docs/claude-code/skills)).
- Cursor: usage-based per-model rate, spend limit requested on first cloud-agent use; larger context window → more tokens ([cloud agent](https://cursor.com/docs/cloud-agent)).
- **Gap**: none of the docs describe per-tenant chargeback/ledger semantics — that is open design space for a multi-tenant platform.

---

## 3. Table stakes vs differentiators for a new multi-tenant web coding-agent platform

### 3.1 Now mandatory (table stakes)
1. **Thread/turn/item data model with paginated history** and resume + fork (from any turn) + archive. Copy Codex's `thread/*`, `turn/*`, `item/*` split.
2. **Two-layer safety**: enforced sandbox (per-task container) + explicit approval policy, with at least `read-only | workspace-write | full-access` and `on-request | never | granular`.
3. **Network default-off + per-environment domain allowlist + secret proxying** (placeholder substitution for allowed HTTPS hosts), never raw creds in the workspace.
4. **Environment definition with setup script, start/verify command, env vars, dependency cache**, plus a "setup report" the user can review before publishing.
5. **Diff review surface** with scope selector (uncommitted/staged/branch/last-turn), per-hunk stage/revert, line-anchored comments that feed back into the agent turn.
6. **Streaming event protocol with a stable taxonomy** (turn/item/tool/approval/cost) + resumable replay + backpressure error (`409`/`-32001`-style) so reconnecting clients can rebuild the transcript.
7. **Approval UI as an inbox**, not a modal-only flow: queue, per-request risk context, "approve once / approve for repo / switch mode", auto-reject-with-reason when policy denies.
8. **Repo instruction file support (`AGENTS.md`) with the root→leaf chain + override file**, shown in the UI as "instructions loaded" (Codex returns `instructionSources` on thread start — do the same).
9. **Skills with progressive disclosure** (`SKILL.md` + frontmatter + body-on-demand) and **slash commands** sharing one namespace.
10. **MCP client** with stdio + streamable-HTTP (+ SSE fallback), per-server status/auth state, tool discovery with on-demand search, and output size caps.
11. **Subagent primitive** with own context, tool allowlist, model override, and a visible child timeline; parent gets a summary only.
12. **Checkpoints + rewind** (code, conversation, or both) and **mid-turn steering** (queue vs steer).
13. **Cost surface**: per-turn tokens/cost, per-session rollup, per-tenant budget + hard stop.
14. **Audit trail**: every approval decision, sandbox escalation, secret access, and file write attributable to a user and a turn.
15. **Multi-tenant isolation**: workspace-scoped secrets/environments, role gates for "use agents" vs "manage environments", and read-only sharing of a run with repo-access verification (Cursor's model: team membership alone doesn't grant access to a run).

### 3.2 Differentiators (defensible, still under-served)
1. **Policy engine with a human-readable decision trace** — Codex/CC both have deny→ask→allow precedence and per-tool rules; none surfaces "why this was allowed" in the UI. Show the winning rule, its source file, and the matched scope.
2. **Cross-agent instruction portability** — read `AGENTS.md`, `.cursor/rules/*.mdc`, `CLAUDE.md`, and `SKILL.md`, and unify them into one precedence model with a "conflicts" view. Codex's `externalAgentConfig/import` (config, skills, AGENTS.md, plugins, MCP, subagents, hooks, commands, sessions) proves the demand ([app-server](https://learn.chatgpt.com/codex/app-server)).
3. **Recorded, resumable orchestration graphs** — a workflow that is a first-class artifact (nodes, phases, per-node cost, per-node diff) rather than a chat side-effect. DSH `workflow` and Devin "dynamic workflows" both gesture at it; nobody ships the UI.
4. **True chargeback** — per-tenant, per-repo, per-agent cost ledger with accrual at tool-call granularity and budget enforcement before a turn starts.
5. **Review as a first-class object separate from the chat** — Codex's detached review mode and P0/P1-only filtering ([github](https://learn.chatgpt.com/docs/third-party/github)) is a good filter; a platform can add severity policy per repo and "finding → fix → re-verify" state machine.
6. **Sandbox-as-a-product**: warm pools, snapshot/restore, VM state TTL, and an explicit lifecycle state machine (OpenHands' `STARTING|RUNNING|PAUSED|ERROR|MISSING` + 12 h cap is the reference) exposed to API users.
7. **Human handoff** — Codex "Hand off" moves a chat *and its code* between Local and Worktree with Git surgery and `.worktreeinclude` for ignored files ([worktrees](https://learn.chatgpt.com/codex/environments/git-worktrees)). A web platform can own the equivalent: agent VM → developer machine / PR branch.
8. **Compaction you can inspect and steer** — show what was summarized, let the user pin facts across compaction, and expose a per-worker compaction window (CC `autoCompactWindow`).

### 3.3 Traps observed in shipping products (avoid)
- Snapshot/checkpoint **doesn't cover subagent or out-of-band Bash edits** → users lose work. Design: content-addressed workspace snapshots per turn, not per-tool-call file copies.
- **Rate/usage collisions between parent and subagents** counting against one limit with no attribution.
- **Marker drift**: CC/Codex both document "don't call this from production clients yet" APIs (`plugin/*`) — pin your integration surface and version it.
- Skills/rules/instructions each having *different* precedence rules across products; unify early or the UI can't explain behavior.
- Session caps (OpenHands 12 h) and idle timeouts that silently `MISSING` a sandbox — surface countdowns in the UI.
