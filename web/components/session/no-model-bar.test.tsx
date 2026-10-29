import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { Provider } from "@/lib/types";

import { hasUsableModel, NoModelBar } from "./no-model-bar";

function provider(overrides: Partial<Provider> = {}): Provider {
  return {
    id: "p1",
    name: "volc",
    api: "openai",
    models: [{ id: "m1", name: "m1", context_window: 1, max_tokens: 1, reasoning: false, input_cost: 0, output_cost: 0 }],
    source: "custom",
    enabled: true,
    has_key: true,
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("hasUsableModel", () => {
  it("requires a key, a model and an enabled provider", () => {
    expect(hasUsableModel([provider()])).toBe(true);
    expect(hasUsableModel([provider({ has_key: false })])).toBe(false);
    expect(hasUsableModel([provider({ enabled: false })])).toBe(false);
    expect(hasUsableModel([provider({ models: [] })])).toBe(false);
    // A shared provider inherited without its key is exactly the fresh-account
    // shape: visible in the list, useless for sending.
    expect(hasUsableModel([provider({ source: "inherited", has_key: false })])).toBe(false);
    expect(hasUsableModel([])).toBe(false);
  });
});

describe("NoModelBar", () => {
  it("says what is missing and links to the page that fixes it", () => {
    render(<NoModelBar />);
    const bar = screen.getByTestId("no-model-bar");
    expect(bar).toHaveTextContent("这个账号还没有可用模型");
    expect(screen.getByTestId("no-model-settings-link")).toHaveAttribute("href", "/settings/providers");
    expect(bar).not.toHaveTextContent("unknown");
  });

  it("carries the server's explanation after a refused send", () => {
    render(<NoModelBar error="服务商「volc」尚未配置，请到「设置 → 模型与凭据」添加" />);
    expect(screen.getByTestId("no-model-error")).toHaveTextContent("volc");
  });
});
