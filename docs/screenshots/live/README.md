# Live screenshots

Taken from a running deployment with a **real model**, not a stubbed peer: the transcript text, tool calls, test results, diffs and audit rows below were produced by an agent run against `volc / deepseek-v4-flash`.

- Deployment: `pwsh scripts/up-isolated.ps1 -Port 7723` (isolated placement: control plane + runner + sandbox container)
- Capture script: `e2e/scripts/public-shots.mjs` (`PIFORGE_BASE` selects the deployment, `OUT` the output directory)
- Viewport 1440×900 at 2×, so each PNG is 2880×1800 (09/10 are 2880×2200: those pages do not fit one viewport)
- Captured 2026-11-02; the script creates its own throwaway project, runs one real prompt, then deletes it

| File | What it shows |
|---|---|
| `01-home.png` | The workspace list after sign-in: projects on the left, the session list under them. |
| `02-workbench-running.png` | A run in flight: streamed assistant text, a live tool call, the composer switched to stop/queue. |
| `03-workbench-settled.png` | The same run settled; the tool trace is one collapsed line and the composer is back to send mode. |
| `04-test-panel.png` | The test panel after the agent ran `node --test`: parsed counts, duration and the exact command. |
| `05-changes-panel.png` | The changes panel with a scoped diff, per-file `+N/−N`, and the diff open on the right. |
| `06-terminal.png` | The built-in terminal attached to **the sandbox container** — its hostname differs from the control plane's. The path shown is the checkout inside that container. |
| `07-inline-comment.png` | An inline review comment on a diff line: the quote, the comment, and the `位置` line the server added. |
| `08-fork-dialog.png` | Forking a session into a branch with its own worktree. |
| `09-admin-status.png` | `/admin/status`: runtime directories, per-sandbox disk quota, tenant limits and the egress mode. |
| `10-audit.png` | `/admin/audit`: tool calls with model, commit, duration, token usage and the redacted argument preview. |

Screens here are of a **local single-host** deployment: one control plane, a runner, and one sandbox container per (user, project). What changes on other placements is written in [`docs/SANDBOX.md`](../SANDBOX.md) and [`docs/SANDBOX-K8S.md`](../SANDBOX-K8S.md).
