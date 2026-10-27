# PiForge

A multi-user, browser-based coding-agent workbench built on [Pi](https://github.com/earendil-works/pi).

One user, one agent, one workspace — with a file tree, a git graph, file preview, a
follow-along terminal, an interactive terminal, a built-in browser, and conversations
that can fork, run in parallel and carry sidecars. Everything runs in an isolated
sandbox; model keys never leave the control plane.

The product spec lives in [`docs/PRD.md`](docs/PRD.md). It is the authority for every
behavioural question; this README only covers running the thing.

---

## Why it is built this way

| Piece | Responsibility |
|---|---|
| **Pi** (`pi --mode rpc`) | The agent loop: provider abstraction, tool pipeline, session tree, compaction, skills, MCP. |
| **Go control plane** | Everything Pi deliberately refuses to do: sandboxes, worktrees, credentials, persistence, SSE fan-out, file/git/terminal/browser services. |
| **Next.js + shadcn/ui** | The workbench. No bespoke chat primitives — `MessageScroller`, `Trace`, `Code Block` and `Bubble` are used as shipped. |

Two rules the codebase never breaks:

1. **The control plane does not execute untrusted code.** It never runs agent output;
   only the sandbox does.
2. **`agent_settled` is the only completion signal.** `agent_end` means one low-level run
   finished — retries, compaction, steering and follow-ups can still follow. Treating
   `agent_end` as "done" is the single most common way to get this wrong.

---

## Layout

```
backend/
  cmd/piforged/        the server
  cmd/fakepi/          Pi-compatible RPC peer for tests and local dev (no model needed)
  internal/contracts/  domain types + AgentRuntime/Workspace/SandboxProvider interfaces
                       (the bottom of the dependency graph; imports nothing internal)
  internal/pi/         Pi RPC bridge: framing, command correlation, stream assembly
  internal/pitest/     the fake peer, importable so tests run in-process
  internal/store/      SQLite persistence, user-scoped repositories
  internal/auth/       opaque session tokens, bcrypt, CSRF
  internal/httpapi/    REST + SSE router (stdlib mux, Go 1.22 patterns)
  internal/gitx/       git via argv (status, diff, DAG, worktrees)
  internal/fsx/        file tree, previews, search, path-traversal defence
  internal/sandbox/    SandboxProvider; local implementation runs on the host
  internal/term/       PTY sessions with an offset-addressed ring buffer
web/                   Next.js App Router + shadcn/ui
e2e/                   Playwright end-to-end suite
docs/PRD.md            product spec
docs/research/         the research briefings the spec was derived from
```

## Requirements

- Go 1.25+
- Node 22+ and pnpm
- git on PATH
- Optional: `pi` on PATH for a real agent (without it, the fake peer drives everything)
- Optional: `rg` (ripgrep) for fast content search; a bounded walk is used otherwise

## Run it locally

```bash
# 1. backend (creates ./data, seeds an admin + demo user, prints the passwords once)
cd backend
go run ./cmd/piforged -data ../data -addr 127.0.0.1:8080 -fake-pi

# 2. frontend
cd web
pnpm install
pnpm dev            # http://localhost:3000, proxies /v1 to 127.0.0.1:8080
```

`-fake-pi` points the runtime at the built-in fake peer instead of a real `pi`
binary. The fake peer performs a scripted run (thinking → text → a `bash` tool call
with streaming output → a final message), which is enough to exercise the entire
product surface without an API key.

Drop `-fake-pi` once `pi` is installed and configured; nothing else changes.

## Test

```bash
# backend unit + integration
cd backend && go test ./... -race

# frontend unit (git lane layout, SSE reducer, formatting)
cd web && pnpm test

# end-to-end (starts the backend with the fake peer, drives a real browser)
cd e2e && pnpm install && pnpm test
```

## Security model in one paragraph

A sandbox is created per `(project, user)` and never shared. It holds no provider key,
no cloud metadata route and no service-account token; the agent talks to the LLM
gateway with a short-lived token that only the gateway accepts. `PTY` masters live
*inside* the sandbox so a snapshot cannot sever a terminal. Ports, preview URLs and the
browser CDP channel are all addressed by a signed token bound to
`(user, sandbox, port, expiry)`. File APIs clean and prefix-check every path and refuse
symlinks that escape the worktree. The full reasoning, including the traps, is in
PRD §6.

## Status

This is a working vertical slice, not a finished product. What is real: the RPC bridge
and its event normalization, the session/branch/sidecar model, SQLite persistence with
user scoping, SSE replay, git status/diff/DAG, the file tree and previews, terminal
sessions, and the whole workbench UI. What is a stub with a documented seam: the
Firecracker/gVisor sandbox provider (the local provider is what runs), the built-in
browser relay, and the LLM gateway. Each stub carries a comment naming the exact
contract its production implementation must satisfy.
