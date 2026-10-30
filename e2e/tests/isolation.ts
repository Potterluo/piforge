import {
  expect,
  request as apiFactory,
  type APIRequestContext,
  type Page,
} from "@playwright/test";

import { ADMIN, login } from "./helpers";

/**
 * Per-spec isolation for the e2e suite.
 *
 * The suite runs one server against one `.data` directory, so anything a spec
 * writes to an *account* or to the *deployment* outlives that spec — and, on a
 * reused data directory, outlives the whole run. That is how a full suite once
 * reported 3 false reds: 65 left the dev account's `parallel` flag on (and
 * 30 asserted the default), and 69 left a skill disabled (and 38 imported the
 * same name). Moving `.data` aside made all of it disappear, which is the
 * definition of state contamination.
 *
 * Two rules replace "remember to clean up":
 *
 *   1. A spec that writes account state asks for its own account
 *      (`freshAccount`). Nothing it creates can reach another spec, a retry, or
 *      the next run, so there is nothing to clean up.
 *   2. A spec that must change deployment state wraps the change in
 *      `withFeatureRestore` / `withSharedProvidersClean`. Those helpers read the
 *      documented default *before* the body, refuse to start from a leaked
 *      value, and put the default back in a `finally` — including when the body
 *      throws, which is the case an `afterAll` written as a separate block
 *      misses.
 *
 * The pre-flight guard (`preflight.setup.ts`) is the third leg: it fails the
 * run at the start, naming the setting and the spec that likely left it, so a
 * regression is a loud first failure rather than a random late one.
 */

/**
 * The control plane the harness starts (playwright.config.ts, `webServer`). The
 * admin helpers talk to it directly instead of through the Next proxy so that
 * swapping an admin cookie never disturbs the page a spec is driving.
 */
export const API = process.env.PIFORGE_E2E_API ?? "http://127.0.0.1:8080";

/** An account this spec owns; nothing else in the run can see it. */
export interface Account {
  email: string;
  password: string;
  userId: string;
}

/** The documented defaults this suite relies on. Kept next to the guard. */
export const SEEDED_SKILLS = ["code-review", "explain-codebase", "test-gap"] as const;

/**
 * The shared provider a fresh deployment seeds in `cmd/piforged` (it has no key,
 * so it never counts as a usable model — it exists so the settings page has
 * something to show). An extra shared provider is the leak that matters: with a
 * key it makes every account look ready before a spec says so.
 */
export const SEEDED_SHARED_PROVIDERS = ["shared-anthropic"] as const;

/**
 * Opens an API session in its own cookie jar, signed in as the seeded admin.
 *
 * A separate jar is the point: helpers here create accounts and restore
 * settings while the spec's page is signed in as somebody else (usually the
 * account under test), and a half-finished admin login on that page is exactly
 * the kind of cross-talk this file exists to remove.
 */
export async function adminSession(): Promise<APIRequestContext> {
  const api = await apiFactory.newContext({ baseURL: API });
  const res = await api.post("/v1/auth/login", { data: ADMIN });
  if (!res.ok()) {
    await api.dispose();
    throw new Error(
      `[isolation] the seeded admin could not sign in (${res.status()}): ` +
        `is piforged running on ${API} with PIFORGE_SEED_EMAIL/PASSWORD?`,
    );
  }
  return api;
}

async function csrf(api: APIRequestContext): Promise<Record<string, string>> {
  const state = await api.storageState();
  const token = state.cookies.find((cookie) => cookie.name === "pf_csrf")?.value ?? "";
  return { "X-PF-CSRF": token };
}

/** A state-changing call on an API context, with the double-submit header. */
export async function apiCall(
  api: APIRequestContext,
  method: "POST" | "PATCH" | "PUT" | "DELETE",
  path: string,
  data?: unknown,
): Promise<{ status: number; body: unknown }> {
  const res = await api.fetch(path, {
    method,
    headers: await csrf(api),
    data,
  });
  let body: unknown = null;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  return { status: res.status(), body };
}

export interface SeedUser {
  id: string;
  email: string;
  role: string;
  status: string;
}

/** Looks one account up in the admin list (admin credentials required). */
export async function findUser(api: APIRequestContext, email: string): Promise<SeedUser> {
  const res = await api.get("/v1/admin/users");
  expect(res.status(), "the admin user list must be readable").toBe(200);
  const { users } = (await res.json()) as { users: SeedUser[] };
  const found = users.find((user) => user.email.toLowerCase() === email.toLowerCase());
  if (!found) {
    throw new Error(`[isolation] the seeded account ${email} is missing from this data dir`);
  }
  return found;
}

let accountSeq = 0;

/**
 * Creates a brand-new account through the admin API and signs `page` in as it.
 *
 * The account starts with no providers, no skills, no MCP servers and default
 * feature flags: the same shape a first-run deployment gives a new tenant, and
 * the only state that is guaranteed to belong to this spec alone. The account
 * is not deleted afterwards — the API has no delete-account route by design —
 * but an unused account is inert, and every assertion that matters is scoped to
 * the caller.
 */
export async function freshAccount(page: Page, label = "spec"): Promise<Account> {
  accountSeq += 1;
  const stamp = `${Date.now()}-${accountSeq}`;
  const email = `e2e-${label}-${stamp}@example.test`;
  const password = "e2e-isolated-123";

  const api = await adminSession();
  try {
    const created = await apiCall(api, "POST", "/v1/admin/users", {
      email,
      password,
      display_name: `e2e ${label}`,
      role: "user",
    });
    expect(
      created.status,
      `creating the isolated account ${email} failed: ${JSON.stringify(created.body)}`,
    ).toBe(201);
    const userId = (created.body as { users: { id: string }[] }).users[0].id;

    // The form is the documented way in, and it is what 01-auth pins; using it
    // here also proves the account can actually sign in, not just that it exists.
    await login(page, { email, password });
    return { email, password, userId };
  } finally {
    await api.dispose();
  }
}

type FeatureFlag = "export" | "browser" | "sidecar" | "parallel";

async function userFeatures(
  api: APIRequestContext,
  userId: string,
): Promise<Record<string, boolean>> {
  const res = await api.get(`/v1/admin/users/${userId}/features`);
  expect(res.status(), "reading a user's feature flags requires an admin").toBe(200);
  return ((await res.json()) as { features: Record<string, boolean> }).features;
}

/**
 * Runs `body` against one account's feature flag and restores the documented
 * default afterwards, even when `body` throws.
 *
 * It refuses to start from a value that is not the default: "restore whatever
 * was there" would happily preserve a leak from the previous run, which is how
 * the parallel toggle survived a whole suite. `expected` is the product default
 * (`store.featureDefaults`), not a snapshot.
 */
export async function withFeatureRestore<T>(
  userId: string,
  flag: FeatureFlag,
  expected: boolean,
  body: () => Promise<T>,
): Promise<T> {
  const api = await adminSession();
  try {
    const before = await userFeatures(api, userId);
    if (before[flag] !== expected) {
      throw new Error(
        `[isolation] ${flag} was ${String(before[flag])} for ${userId} before this spec ran, ` +
          `expected the default ${String(expected)}. A previous spec or run left it behind ` +
          `(65-parallel-limit and 17-feature-flags are the only specs that change flags). ` +
          `Start from a clean data dir with \`pnpm --dir e2e test:fresh\`.`,
      );
    }
    try {
      return await body();
    } finally {
      const restored = await apiCall(api, "POST", `/v1/admin/users/${userId}/features`, {
        flag,
        enabled: expected,
      });
      expect(
        restored.status,
        `[isolation] restoring ${flag}=${String(expected)} for ${userId} failed`,
      ).toBe(200);
    }
  } finally {
    await api.dispose();
  }
}

interface SharedProvider {
  id: string;
  name: string;
}

async function sharedProviders(api: APIRequestContext): Promise<SharedProvider[]> {
  const res = await api.get("/v1/admin/providers");
  expect(res.status()).toBe(200);
  return ((await res.json()) as { providers: SharedProvider[] }).providers;
}

/**
 * Runs `body` on a deployment whose shared providers are exactly the seeded
 * ones, and deletes any provider the body created.
 *
 * A shared provider is deployment-wide: every account inherits it, so one left
 * behind makes the next run's "a new account has no model" specs start from the
 * wrong premise (55 drove that state through a stubbed provider list until this
 * cleanup made the real state deterministic).
 * Deleting is the only honest cleanup — the key is sealed and never returned, so
 * a deleted provider cannot be re-created faithfully. Hence the hard
 * precondition: an extra shared provider fails loudly instead of being deleted
 * as if it were this spec's.
 */
export async function withSharedProvidersClean<T>(body: () => Promise<T>): Promise<T> {
  const api = await adminSession();
  try {
    const before = await sharedProviders(api);
    const seeded = [...SEEDED_SHARED_PROVIDERS].sort();
    const names = before.map((provider) => provider.name).sort();
    if (JSON.stringify(names) !== JSON.stringify(seeded)) {
      throw new Error(
        `[isolation] the deployment's shared providers are ` +
          `${JSON.stringify(names)}, expected the seeded ${JSON.stringify(seeded)}. ` +
          `12-shared-providers and 68-first-run are the specs that create them; ` +
          `start from a clean data dir with \`pnpm --dir e2e test:fresh\`.`,
      );
    }

    try {
      return await body();
    } finally {
      const after = await sharedProviders(api);
      const leaked = after.filter((provider) => !before.some((old) => old.id === provider.id));
      for (const provider of leaked) {
        const gone = await apiCall(api, "DELETE", `/v1/admin/providers/${provider.id}`);
        expect(
          [200, 404],
          `[isolation] could not delete the shared provider ${provider.name} this spec created`,
        ).toContain(gone.status);
      }
    }
  } finally {
    await api.dispose();
  }
}
