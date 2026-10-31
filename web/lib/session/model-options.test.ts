import { describe, expect, test } from "vitest";

import { buildModelChoices, defaultModelCopy, modelRefLabel } from "./model-options";
import type { Provider } from "@/lib/types";

const provider = (overrides: Partial<Provider> = {}): Provider => ({
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
});

const model = (id: string, name = id): Provider["models"][number] => ({
  id,
  name,
  context_window: 200000,
  max_tokens: 8192,
  reasoning: true,
  input_cost: 0,
  output_cost: 0,
});

describe("buildModelChoices", () => {
  test("a keyless provider is shown disabled with the reason, never selectable", () => {
    // The exact acceptance trap: `shared-anthropic` has no key and the picker
    // offered it like any other model.
    const { options, blocked } = buildModelChoices([
      provider({
        name: "shared-anthropic",
        api: "anthropic-messages",
        has_key: false,
        models: [model("claude-sonnet-4-5", "Claude Sonnet 4.5")],
      }),
      provider(),
    ]);

    const keyless = options.find((option) => option.value === "shared-anthropic/claude-sonnet-4-5");
    expect(keyless).toBeDefined();
    expect(keyless?.selectable).toBe(false);
    expect(keyless?.note).toBe("缺少 API Key");
    expect(keyless?.label).toContain("Claude Sonnet 4.5");
    expect(blocked).toEqual([{ name: "shared-anthropic", reason: "缺少 API Key" }]);
    // The keyed one is still a normal choice.
    expect(options.find((option) => option.value === "volc/deepseek-v4-flash")?.selectable).toBe(true);
  });

  test("a disabled provider is not offered at all", () => {
    const { options, blocked } = buildModelChoices([
      provider({ name: "off", enabled: false, has_key: false }),
    ]);
    expect(options).toEqual([]);
    expect(blocked).toEqual([]);
  });

  test("a keyed provider with no models offers nothing and is not called blocked", () => {
    const { options, blocked } = buildModelChoices([provider({ models: [] })]);
    expect(options).toEqual([]);
    expect(blocked).toEqual([]);
  });

  test("every model of a keyless provider is blocked, not just the first", () => {
    const { options } = buildModelChoices([
      provider({
        name: "keyless",
        has_key: false,
        models: [model("a"), model("b", "Model B")],
      }),
    ]);
    expect(options).toHaveLength(2);
    expect(options.every((option) => !option.selectable && option.note === "缺少 API Key")).toBe(true);
  });
});

describe("the default model's copy", () => {
  const keyless = provider({
    name: "shared-anthropic",
    api: "anthropic-messages",
    has_key: false,
    models: [model("claude-sonnet-4-5", "Claude Sonnet 4.5")],
  });

  test("names the model the default resolves to", () => {
    // The acceptance trap: a keyless provider was named right under 默认模型, so
    // the dialog read as "the default is broken" while it resolved to a working
    // model. The sentence now starts with that model.
    const copy = defaultModelCopy("volc/deepseek-v4-flash", [keyless, provider()], [
      { name: "shared-anthropic", reason: "缺少 API Key" },
    ]);
    expect(copy.label).toBe("volc / deepseek-v4-flash");
    expect(copy.hint).toContain("默认模型会使用 volc / deepseek-v4-flash");
    // The blocked provider is still named, with the fact that it does not touch
    // the default.
    expect(copy.blockedHint).toContain("shared-anthropic");
    expect(copy.blockedHint).toContain("这不影响默认模型");
    expect(copy.blockedHint).not.toContain("默认模型无法使用");
  });

  test("uses the provider's display name for the model when it has one", () => {
    expect(
      modelRefLabel("shared-anthropic/claude-sonnet-4-5", [keyless]),
    ).toBe("shared-anthropic / Claude Sonnet 4.5");
    // An unknown provider keeps the raw reference rather than inventing a name.
    expect(modelRefLabel("nope/model-x", [keyless])).toBe("nope / model-x");
    expect(modelRefLabel("", [keyless])).toBe("");
  });

  test("says plainly when nothing can answer", () => {
    const copy = defaultModelCopy("", [keyless], [
      { name: "shared-anthropic", reason: "缺少 API Key" },
    ]);
    expect(copy.label).toBe("");
    expect(copy.hint).toContain("还没有可用的默认模型");
    expect(copy.hint).toContain("API Key");
    expect(copy.blockedHint).toContain("shared-anthropic");
    expect(copy.blockedHint).not.toContain("不影响默认模型");
  });
});
