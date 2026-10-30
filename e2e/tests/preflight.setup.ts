import { expect, request as apiFactory, test, type APIRequestContext } from "@playwright/test";

import { ADMIN, USER } from "./helpers";
import { API, SEEDED_SHARED_PROVIDERS, SEEDED_SKILLS } from "./isolation";

/**
 * The pre-flight guard.
 *
 * The e2e suite drives one control plane and one `.data` directory, and it is
 * meant to be run twice in a row on that directory. Anything a spec leaves
 * behind is therefore visible to the next spec — and to the next run, where it
 * arrives as a random-looking red far from the cause. This project runs before
 * any spec (see `dependencies` in playwright.config.ts) and asserts the known
 * shared state is at its documented default. A leak fails here, at the start,
 * naming the setting and the spec that is supposed to restore it.
 *
 * It reads only through the API: no browser, no UI, so it costs a couple of
 * hundred milliseconds and cannot itself change state.
 */

interface Problem {
  setting: string;
  actual: string;
  expected: string;
  likely: string;
  fix: string;
}

const problems: Problem[] = [];

function check(
  setting: string,
  actual: unknown,
  expected: unknown,
  likely: string,
  fix: string,
): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    problems.push({
      setting,
      actual: JSON.stringify(actual),
      expected: JSON.stringify(expected),
      likely,
      fix,
    });
  }
}

async function signIn(who: { email: string; password: string }): Promise<APIRequestContext> {
  const api = await apiFactory.newContext({ baseURL: API });
  const res = await api.post("/v1/auth/login", { data: who });
  if (!res.ok()) {
    await api.dispose();
    throw new Error(
      `e2e pre-flight: ${who.email} could not sign in (${res.status()}). ` +
        `The seeded accounts are created on first boot from PIFORGE_SEED_*; ` +
        `if this data dir is from another deployment, rebuild it with \`pnpm --dir e2e test:fresh\`.`,
    );
  }
  return api;
}

async function json<T>(api: APIRequestContext, path: string): Promise<T> {
  const res = await api.get(path);
  if (!res.ok()) {
    throw new Error(`e2e pre-flight: GET ${path} answered ${res.status()}; the guard cannot run`);
  }
  return (await res.json()) as T;
}

test("the shared e2e state is at its documented defaults", async () => {
  const admin = await signIn(ADMIN);
  const dev = await signIn(USER);
  try {
    // --- the seeded accounts -------------------------------------------------
    const { users } = await json<{
      users: { id: string; email: string; role: string; status: string }[];
    }>(admin, "/v1/admin/users");
    const seededDev = users.find((user) => user.email.toLowerCase() === USER.email.toLowerCase());
    const seededAdmin = users.find(
      (user) => user.email.toLowerCase() === ADMIN.email.toLowerCase(),
    );
    check(
      `seeded admin ${ADMIN.email} is an active admin`,
      seededAdmin ? `${seededAdmin.role}/${seededAdmin.status}` : "missing",
      "admin/active",
      "09-admin-users (the last-admin spec must not demote the seeded admin)",
      "restore the admin role/status through /v1/admin/users",
    );
    check(
      `seeded user ${USER.email} is an active normal user`,
      seededDev ? `${seededDev.role}/${seededDev.status}` : "missing",
      "user/active",
      "09-admin-users (it promotes and disables accounts through the admin page)",
      "restore the role/status through /v1/admin/users",
    );
    if (!seededDev) throw new Error("e2e pre-flight: the dev account disappeared");

    // --- per-account feature flags ------------------------------------------
    // The defaults are `store.featureDefaults`: everything on except parallel,
    // which is opt-in (FR-3.2).
    const { features } = await json<{ features: Record<string, boolean> }>(
      admin,
      `/v1/admin/users/${seededDev.id}/features`,
    );
    check(
      `dev feature flag "parallel"`,
      features.parallel,
      false,
      "65-parallel-limit (it turns parallel on to test the cap)",
      "wrap the toggle in withFeatureRestore(id, \"parallel\", false, ...) from tests/isolation.ts",
    );
    check(
      `dev feature flag "export"`,
      features.export,
      true,
      "17-feature-flags (it turns export off, then back on at the end)",
      "wrap the toggle in withFeatureRestore(id, \"export\", true, ...) from tests/isolation.ts",
    );
    for (const flag of ["browser", "sidecar"] as const) {
      check(
        `dev feature flag "${flag}"`,
        features[flag],
        true,
        "no spec should touch this flag; an admin page or a hand-made change did",
        "reset it through /v1/admin/users/{id}/features",
      );
    }

    // --- deployment-wide providers ------------------------------------------
    const shared = await json<{ providers: { id: string; name: string }[] }>(
      admin,
      "/v1/admin/providers",
    );
    check(
      "shared providers",
      shared.providers.map((provider) => provider.name).sort(),
      [...SEEDED_SHARED_PROVIDERS].sort(),
      "12-shared-providers (it creates one to test inheritance) or 68-first-run (it creates two)",
      "wrap the spec in withSharedProvidersClean(...) from tests/isolation.ts",
    );

    // --- the dev account's own configuration --------------------------------
    // Dev's provider list is merged with the inherited ones, so the shared names
    // are subtracted: a leftover shared provider is reported above, and counting
    // it as dev's own would name the wrong spec.
    const sharedNames = new Set(shared.providers.map((provider) => provider.name));
    const providers = await json<{ providers: { name: string; scope: string }[] }>(
      dev,
      "/v1/providers",
    );
    check(
      "dev user providers",
      providers.providers
        .filter((provider) => !sharedNames.has(provider.name))
        .map((provider) => provider.name),
      [],
      "06-providers, 10-models, 50-error-localization or 57-model-switch (they create providers for the caller)",
      "use freshAccount(page) from tests/isolation.ts so the provider belongs to a throwaway account",
    );

    const skills = await json<{
      skills: { name: string; scope: string; enabled: boolean }[];
    }>(dev, "/v1/skills");
    check(
      "dev user-scope skills",
      skills.skills.filter((skill) => skill.scope === "user").map((skill) => skill.name),
      [],
      "07-tooling, 38-skill-import or 69-skill-zip (they create skills for the caller)",
      "use freshAccount(page) from tests/isolation.ts; a skill name that survives a run is reused by the next import",
    );
    check(
      "shared (admin-scope) skills",
      skills.skills
        .filter((skill) => skill.scope === "admin")
        .map((skill) => skill.name)
        .sort(),
      [...SEEDED_SKILLS].sort(),
      "42-shared-skill-delete creates and deletes a shared skill; a leftover means it failed midway",
      "delete the extra shared skill as the admin, or rebuild the data dir",
    );
    check(
      "disabled skills",
      skills.skills.filter((skill) => !skill.enabled).map((skill) => skill.name),
      [],
      "69-skill-zip disables pdf-tools to prove a disabled skill stops mounting; it must re-enable or delete it",
      "re-enable it through PATCH /v1/skills/{id}, or use an isolated account for the disable test",
    );

    const mcp = await json<{ servers: { name: string }[] }>(dev, "/v1/mcp/servers");
    check(
      "dev MCP servers",
      mcp.servers.map((server) => server.name),
      [],
      "07-tooling or 32-mcp-secrets (they create MCP servers for the caller)",
      "use freshAccount(page) from tests/isolation.ts",
    );

    const keys = await json<{ api_keys: { name: string; revoked_at?: string | null }[] }>(
      dev,
      "/v1/api-keys",
    );
    check(
      "dev active API keys",
      keys.api_keys.filter((key) => !key.revoked_at).map((key) => key.name),
      [],
      "24-api-keys (the token it mints must be revoked, and a fresh account keeps runs from piling up)",
      "use freshAccount(page) from tests/isolation.ts",
    );

    const notices = await json<{ unseen: number }>(dev, "/v1/support-notices");
    check(
      "dev unseen support notices",
      notices.unseen,
      0,
      "16-support-mode (the owner's receipt is left unread only when that spec failed midway)",
      "the notice list marks receipts seen when opened; a fresh account keeps this at zero",
    );

    // --- deployment configuration (read-only, but a drift explains reds) ----
    const status = await json<{ parallel_limit: number }>(admin, "/v1/admin/status");
    check(
      "PIFORGE_PARALLEL_LIMIT",
      status.parallel_limit,
      2,
      "playwright.config.ts (65-parallel-limit reads the cap from the environment)",
      "restore the webServer env in playwright.config.ts",
    );
  } finally {
    await dev.dispose();
    await admin.dispose();
  }

  if (problems.length > 0) {
    const lines = problems.map(
      (problem, index) =>
        `  ${index + 1}. ${problem.setting}\n` +
        `     actual:   ${problem.actual}\n` +
        `     expected: ${problem.expected}\n` +
        `     likely left by: ${problem.likely}\n` +
        `     fix: ${problem.fix}`,
    );
    throw new Error(
      `e2e pre-flight: the data directory is not in its documented default state.\n` +
        `The suite is order-dependent until these are restored, and a full run would fail ` +
        `somewhere unrelated instead of here.\n\n` +
        `${lines.join("\n\n")}\n\n` +
        `If no spec was changed, this data directory predates the isolation work: ` +
        `\`pnpm --dir e2e test:fresh\` rebuilds it from empty.`,
    );
  }

  // A visible line in the run log: a silent guard is indistinguishable from one
  // that never ran.
  console.log(
    `[e2e pre-flight] defaults intact: ${SEEDED_SKILLS.length} seeded skills, ` +
      `only the seeded shared provider, no dev providers/skills/MCP/API keys, ` +
      `parallel off, export on.`,
  );
});
