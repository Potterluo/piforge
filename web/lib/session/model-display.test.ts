import { describe, expect, it } from "vitest";

import { cleanModelRef, sessionModelLabel } from "./model-display";

describe("cleanModelRef", () => {
  it("treats Pi's unknown placeholder as no model at all", () => {
    expect(cleanModelRef("unknown")).toBeNull();
    expect(cleanModelRef("unknown/unknown")).toBeNull();
    expect(cleanModelRef("volc/unknown")).toBeNull();
    expect(cleanModelRef("UNKNOWN/Unknown")).toBeNull();
  });

  it("keeps a real reference, including one without a provider", () => {
    expect(cleanModelRef("volc/deepseek-v4-flash")).toBe("volc/deepseek-v4-flash");
    expect(cleanModelRef("deepseek-v4-flash")).toBe("deepseek-v4-flash");
    expect(cleanModelRef("unknown/deepseek-v4-flash")).toBe("deepseek-v4-flash");
    expect(cleanModelRef("  ")).toBeNull();
  });
});

describe("sessionModelLabel", () => {
  it("names the pinned model when it is what resolves", () => {
    expect(
      sessionModelLabel({ stored: "volc/deepseek-v4-flash", resolved: "volc/deepseek-v4-flash" }),
    ).toBe("deepseek-v4-flash");
  });

  it("names the model that actually runs when the pinned provider is gone", () => {
    expect(
      sessionModelLabel({ stored: "deleted/gone-model", resolved: "volc/deepseek-v4-flash" }),
    ).toBe("deepseek-v4-flash");
  });

  it("marks a model that came from the deployment default", () => {
    expect(sessionModelLabel({ resolved: "volc/deepseek-v4-flash", hasUsableModel: true })).toBe(
      "默认模型（deepseek-v4-flash）",
    );
  });

  it("never renders unknown, from either source", () => {
    expect(sessionModelLabel({ stored: "unknown/unknown", resolved: "unknown/unknown" })).toBe(
      "默认模型",
    );
    // A stored reference whose model half is `unknown` is not a model: the
    // resolved default is the honest answer, with the inherited marker.
    expect(
      sessionModelLabel({ stored: "volc/unknown", resolved: "volc/deepseek-v4-flash" }),
    ).toBe("默认模型（deepseek-v4-flash）");
    expect(sessionModelLabel({ stored: "unknown/unknown" })).not.toContain("unknown");
  });

  it("says 未配置 when the account has nothing to send to", () => {
    expect(sessionModelLabel({ stored: "deleted/gone-model", hasUsableModel: false })).toBe("未配置");
    expect(sessionModelLabel({ hasUsableModel: false })).toBe("未配置");
    // The server's own verdict wins over a provider list the page is caching.
    expect(sessionModelLabel({ stored: "deleted/gone-model", unavailable: true })).toBe("未配置");
  });
});
