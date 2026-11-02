# Live screenshots

Every file here comes from a running deployment with a real provider (`volc / deepseek-v4-flash`).
The transcripts, tool calls, test results, diffs and audit rows were produced by actual agent
runs, not by a stubbed model.

- Captured 2026-11-02 against `http://127.0.0.1:7722` (container `pf-local`)
- All ten files are from **one** run: one throwaway project (`pubshots-<epoch-ms>`) and one
  session, `素数脚本：测试与提交`; the script deletes the project afterwards and re-checks `/healthz`
- Script: `e2e/scripts/public-shots.mjs` — `PIFORGE_BASE` picks the deployment, `OUT` the output
  directory, `ONLY=05` captures one file, `KEEP_PROJECT=1` leaves the project for debugging
- Viewport 1440×900 at 2×, so 2880×1800 px. `09` and `10` are 2880×2600: those admin pages have
  no inner scroll container, so at 900 px the per-sandbox quota list and the tenant/egress card
  would be clipped

| File | What is actually in it |
|---|---|
| `01-home.png` | The project list after sign-in; the filter narrows it to the demo project, whose card shows branch `main`, sandbox state, and one session. |
| `02-workbench-running.png` | A run in flight: streamed assistant text, `执行命令 node --test 运行中` in the tool trace, and the composer in stop/queue mode. |
| `03-workbench-settled.png` | The same run settled; the tool trace collapsed to one line. |
| `04-test-panel.png` | The test panel after `node --test`: parsed counts, duration, and the command that produced them. |
| `05-changes-panel.png` | The changes panel with a scoped diff, per-file `+N/−N`, and the diff open beside it. |
| `06-terminal.png` | The terminal attached to **the per-session sandbox container**: `hostname` is the sandbox, `pwd` is the checkout inside it. Neither the host nor the control plane's own name appears. |
| `07-inline-comment.png` | An inline review comment on a diff line, with the quoted code and the `位置` line the server composed. |
| `08-fork-dialog.png` | The fork dialog, filling in the branch that gets its own worktree. |
| `09-admin-status.png` | `/admin/status`: runtime directories with sizes, per-sandbox quota against its budget, and the tenant limits / egress card. |
| `10-audit.png` | `/admin/audit`: 13 rows from that run with tool, duration, model, commit, token counts and the redacted argument preview. |

The set shows a single-host deployment: one control plane, a runner, and one sandbox container per
(user, project). Other placements are described in [`docs/SANDBOX.md`](../SANDBOX.md) and
[`docs/SANDBOX-K8S.md`](../SANDBOX-K8S.md).
