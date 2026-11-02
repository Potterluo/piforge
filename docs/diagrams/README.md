# Architecture diagrams

Hand-written SVGs for the README and the GitHub Pages site: no drawing tool, no
custom fonts, text is real `<text>` (selectable and searchable), one accent colour,
and no CJK inside the artwork — the caption under each figure in the markdown is
where the sentence goes.

**The rule: diagrams are drawn from the documents; if they disagree, the document
wins and the diagram is a bug.**

| File | What it shows | Authority |
|---|---|---|
| [`01-system.svg`](01-system.svg) | The deployment: browser → control plane (Go, SQLite, session hub, LLM gateway) → sandbox provider (local, docker, runner, k8s) → one sandbox per `(user, project)` with the agent inside; the provider key stays in the control plane, the sandbox holds only a per-session gateway token. | [`EXECUTION-BOUNDARY.md`](../EXECUTION-BOUNDARY.md) §2, §4, §6; [`README.md`](../../README.md) "How the moving parts fit"; [`SANDBOX-K8S.md`](../SANDBOX-K8S.md) for the fourth provider |
| [`02-request-path.svg`](02-request-path.svg) | One message's life: `POST /messages` → model resolution → `hub.ensure` spawn or reattach → the agent calls `/gw/llm/<token>` with its proof → the provider streams back → the agent's RPC events reach the hub → SSE to the browser. The token is shown bound to that sandbox. | [`EXECUTION-BOUNDARY.md`](../EXECUTION-BOUNDARY.md) §2, §5, §16.3; [`README.md`](../../README.md) "Model traffic never carries a real key" |
| [`03-execution-boundary.svg`](03-execution-boundary.svg) | The two axes: what runs in the control plane and what runs in the sandbox, the crossings between them, the three things a sandbox cannot reach (`/data`, the provider key, the Docker socket), and `-sandbox local` drawn honestly as host placement for development. | [`EXECUTION-BOUNDARY.md`](../EXECUTION-BOUNDARY.md) §1, §8, §12 |
| [`04-tenancy.svg`](04-tenancy.svg) | Tenant isolation: one bridge per tenant, one sandbox and one workspace volume per `(user, project)`, default-deny egress, quotas enforced where containers are made, and the one documented non-claim — two sandboxes of the same tenant share their bridge. | [`EXECUTION-BOUNDARY.md`](../EXECUTION-BOUNDARY.md) §12.1, §12.2, §13.1, §13.2, §13.4 |
| [`05-data-layout.svg`](05-data-layout.svg) | The data directory: every path the control plane creates, the row that owns it, the eager delete, and the janitor plus audit retention as the reclaim path; what lives outside `<data>` and is never swept. | [`AUDIT.md`](../AUDIT.md) §7; [`SANDBOX.md`](../SANDBOX.md) "快照的生命周期"; the inventory in `backend/internal/runtimedirs` and `backend/internal/janitor` (read to confirm, never invented) |
| [`06-run-state.svg`](06-run-state.svg) | A run's states — `started` → `settled`, `aborted`, `interrupted` — with the three interruption paths (stop, control-plane restart with reattach, process death) and the events each one writes into the transcript. | [`PRD.md`](../PRD.md) §7.4.5 and the `runs.status` schema; [`EXECUTION-BOUNDARY.md`](../EXECUTION-BOUNDARY.md) §5, §6, §15.2; `backend/internal/recovery` (read to confirm) |

## Where the documents disagree

These figures follow `README.md` and `EXECUTION-BOUNDARY.md` where the older
`SANDBOX.md` says something else; the disagreement is real and is not resolved here:

* `SANDBOX.md` calls `local` the default provider and lists three providers;
  `README.md` says `-sandbox` has no default and must be named, and
  `EXECUTION-BOUNDARY.md` §8 records that after adversarial round 6 there is no
  default at all. `SANDBOX-K8S.md` adds a fourth provider that neither of the other
  two mentions.
* `SANDBOX.md` describes the docker workspace as a bind mount of
  `<data>/sandboxes/<user>/<project>` that the control plane writes to;
  `EXECUTION-BOUNDARY.md` §3.1 (and the README's "isolated stack") describe a
  `piforge-ws-<hash>` volume that only the sandbox mounts, with the control plane
  never mounting it. 01, 04 and 05 draw the detached volume.
* `SANDBOX.md` says a branch session's linked worktree is still created by host
  git; `EXECUTION-BOUNDARY.md` §3.1 records it as a `git worktree add` inside the
  container. No figure depends on it.

## Checking a change

`e2e/.artifacts/diagrams/render.mjs` (gitignored) opens each file in Chrome at the
width a 900px README column renders it, screenshots it, and fails on text that
leaves the viewBox or collides with another text box. Look at the PNGs before
committing; the automated check cannot see a box that is merely ugly.
