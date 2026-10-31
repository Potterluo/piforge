import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("@/lib/api/client", () => ({
  api: {
    providers: { list: vi.fn() },
    sessions: { create: vi.fn() },
  },
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}));

import { api } from "@/lib/api/client";
import { NewSessionDialog } from "@/components/shell/new-session-dialog";
import type { Provider } from "@/lib/types";

function provider(overrides: Partial<Provider>): Provider {
  return {
    id: "prv_1",
    name: "volc",
    api: "openai-compat",
    models: [
      {
        id: "deepseek-v4-flash",
        name: "deepseek-v4-flash",
        context_window: 128000,
        max_tokens: 8192,
        reasoning: false,
        input_cost: 0,
        output_cost: 0,
      },
    ],
    source: "custom",
    enabled: true,
    has_key: true,
    created_at: "2026-10-31T00:00:00Z",
    ...overrides,
  };
}

function renderDialog() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <NewSessionDialog open onOpenChange={() => {}} projectId="p1" />
    </QueryClientProvider>,
  );
}

/**
 * The new-session dialog's copy (acceptance finding 6).
 *
 * It used to show 「shared-anthropic 缺少 API Key，不能选择」 directly under
 * 默认模型, which reads as "the default cannot be used" — while the default was
 * a working `volc/deepseek-v4-flash` the whole time. The dialog now names the
 * model the default resolves to, and says the blocked provider does not affect
 * it.
 */
describe("NewSessionDialog model copy", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.providers.list).mockResolvedValue({
      providers: [
        provider({
          id: "prv_2",
          name: "shared-anthropic",
          api: "anthropic-messages",
          has_key: false,
          models: [
            {
              id: "claude-sonnet-4-5",
              name: "Claude Sonnet 4.5",
              context_window: 200000,
              max_tokens: 8192,
              reasoning: true,
              input_cost: 0,
              output_cost: 0,
            },
          ],
        }),
        provider({}),
      ],
      default_model_ref: "volc/deepseek-v4-flash",
    });
  });

  it("names the resolved default and does not present it as broken", async () => {
    renderDialog();

    const hint = await screen.findByTestId("model-default-hint");
    expect(hint).toHaveTextContent("默认模型会使用");
    expect(hint).toHaveTextContent("volc / deepseek-v4-flash");

    const blocked = await screen.findByTestId("model-blocked-hint");
    expect(blocked).toHaveTextContent("shared-anthropic");
    expect(blocked).toHaveTextContent("这不影响默认模型");
    // The old sentence, which implied the default itself could not be used.
    expect(blocked).not.toHaveTextContent("不能选择。去填写");
  });
});
