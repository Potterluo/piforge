#!/usr/bin/env node
/**
 * Reproduces acceptance pass 3's dead session against a *deployed* control plane.
 *
 * The scenario is the tester's: a session exists, the provider it pins is
 * deleted (the pass deleted and re-created providers), and the next send to that
 * session is the question.
 *
 * On the old image the answer is 400 `no_model` (or, with an agent spawned while
 * the provider was missing, 500) even though the deployment has a working
 * default provider — the conversation is dead until somebody creates a new
 * session. After the fix the same send re-resolves onto the working default,
 * answers 200, and names both the used model and the one that disappeared.
 *
 * Usage: node probe-provider-lifecycle.mjs [baseURL] [outFile]
 * The provider it creates is a placeholder (invalid endpoint); no model is
 * called by this probe, so the only provider usage is whatever fallback the
 * control plane picks.
 */
import { writeFileSync } from "node:fs";
import { randomUUID } from "node:crypto";

const base = (process.argv[2] ?? "http://127.0.0.1:7722").replace(/\/$/, "");
const out = process.argv[3] ?? "e2e/.artifacts/fix-provider-lifecycle/lifecycle-api.json";
const DEV = { email: "dev@piforge.local", password: "piforge-dev-123" };

/** Minimal cookie jar: the API uses cookies + a double-submit CSRF header. */
class Session {
  constructor() {
    this.cookies = new Map();
    this.csrf = "";
  }
  absorb(res) {
    const raw = res.headers.getSetCookie?.() ?? [];
    for (const entry of raw) {
      const [pair] = entry.split(";");
      const idx = pair.indexOf("=");
      if (idx > 0) this.cookies.set(pair.slice(0, idx).trim(), pair.slice(idx + 1).trim());
    }
    const csrf = this.cookies.get("pf_csrf");
    if (csrf) this.csrf = csrf;
  }
  headers(extra = {}) {
    const cookie = [...this.cookies.entries()].map(([k, v]) => `${k}=${v}`).join("; ");
    return { ...(cookie ? { cookie } : {}), ...(this.csrf ? { "X-PF-CSRF": this.csrf } : {}), ...extra };
  }
  async call(method, path, body) {
    const res = await fetch(base + path, {
      method,
      headers: this.headers(body === undefined ? {} : { "content-type": "application/json" }),
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    this.absorb(res);
    let json = null;
    try {
      json = await res.json();
    } catch {
      /* some routes answer empty */
    }
    return { status: res.status, body: json };
  }
}

const evidence = { base, at: new Date().toISOString(), steps: [] };
const step = (name, value) => {
  evidence.steps.push({ name, ...value });
  console.log(`${name}:`, JSON.stringify(value));
};

const session = new Session();
const login = await session.call("POST", "/v1/auth/login", DEV);
step("login", { status: login.status });
if (login.status !== 200) {
  writeFileSync(out, JSON.stringify(evidence, null, 2));
  process.exit(1);
}

const stamp = Date.now();
const providerName = `accept3-dead-${stamp}`;
const created = await session.call("POST", "/v1/providers", {
  name: providerName,
  api: "openai-compat",
  base_url: "https://example.invalid/v1",
  api_key: "sk-probe-not-a-real-key",
  models: [{ id: "probe-model" }],
});
step("create provider", { status: created.status, name: providerName });
const providerId = created.body?.provider?.id;

const project =
  (await session.call("POST", "/v1/projects", { name: `accept3-dead-${stamp}`, repo_url: "" })).body
    ?.project ?? null;
step("create project", { status: project ? 201 : 0, id: project?.id });

const sessionRes = await session.call("POST", `/v1/projects/${project.id}/sessions`, {
  title: "provider deleted under me",
  model_ref: `${providerName}/probe-model`,
});
const sessionId = sessionRes.body?.session?.id;
step("create session", { status: sessionRes.status, id: sessionId, model_ref: sessionRes.body?.session?.model_ref });

const deleted = await session.call("DELETE", `/v1/providers/${providerId}`);
step("delete provider", { status: deleted.status });

const send = await session.call("POST", `/v1/sessions/${sessionId}/messages`, {
  client_msg_id: randomUUID(),
  text: "are you still there after the provider change?",
});
step("send after the provider was deleted", { status: send.status, body: send.body });

const detail = await session.call("GET", `/v1/sessions/${sessionId}`);
step("session row after the send", { status: detail.status, model_ref: detail.body?.session?.model_ref });

const status = await session.call("GET", `/v1/sessions/${sessionId}/status`);
step("session status", {
  status: status.status,
  model_ref: status.body?.model_ref ?? "",
  resolved_model_ref: status.body?.resolved_model_ref ?? "",
});

const setup = await session.call("GET", "/v1/setup");
step("setup", {
  status: setup.status,
  needs_provider: setup.body?.needs_provider,
  default_provider: setup.body?.default_provider,
  default_provider_missing: setup.body?.default_provider_missing,
});

// Cleanup: remove the probe session and project. The provider is already gone.
const removedSession = await session.call("DELETE", `/v1/sessions/${sessionId}`);
step("cleanup session", { status: removedSession.status });
const removedProject = await session.call("DELETE", `/v1/projects/${project.id}`);
step("cleanup project", { status: removedProject.status });

writeFileSync(out, JSON.stringify(evidence, null, 2));
console.log(`evidence: ${out}`);
