import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/client", () => ({
  api: { auth: { login: vi.fn() } },
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}));

import LoginPage from "./page";

/**
 * The seeded-password banner is the first thing an operator reads, and it used
 * to say the opposite of what the backend does: `cmd/piforged/seed_log_test.go`
 * pins that the boot log names the accounts and never their passwords. The
 * password comes from PIFORGE_SEED_PASSWORD / PIFORGE_SEED_USER_PASSWORD, or is
 * generated at seed time and printed nowhere. This test pins the sentence so a
 * future copy edit cannot bring the "check the log" advice back.
 */
function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <LoginPage />
    </QueryClientProvider>,
  );
}

describe("login page seeded accounts", () => {
  it("names the accounts but sends the operator to the deployment, not the log", () => {
    renderPage();

    const note = screen.getByTestId("login-seed-password-note");
    const text = note.textContent ?? "";

    // Both seeded accounts are listed, so the operator knows what to ask for.
    expect(screen.getByText("admin@piforge.local")).toBeInTheDocument();
    expect(screen.getByText("dev@piforge.local")).toBeInTheDocument();

    // The two environment variables that actually set the password.
    expect(text).toContain("PIFORGE_SEED_PASSWORD");
    expect(text).toContain("PIFORGE_SEED_USER_PASSWORD");

    // The generated case: hash only, no log, not recoverable.
    expect(text).toContain("随机生成");
    expect(text).toContain("不会打印到任何日志");
    expect(text).toContain("无法找回");

    // The old lie, verbatim: the password is printed to the boot log once.
    expect(text).not.toContain("初始密码在首次启动后端时打印到它的日志里");
    expect(text).not.toContain("只打印一次");
  });
});
