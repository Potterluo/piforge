import { describe, expect, it } from "vitest";

import {
  classifyModelError,
  modelErrorAdvice,
  modelErrorText,
  modelErrorTitle,
} from "@/lib/session/model-error";

describe("classifyModelError", () => {
  it("recognises a missing model", () => {
    // The exact body a provider sent in QA-14.
    expect(
      classifyModelError(
        '404: {"message":"The requested model does not exist. Please check the \'model\' parameter in your request, or call GET /v1/models to list available models.","type":"invalid_request_error","param":"model","code":"model_not_found"}',
      ),
    ).toBe("model_not_found");
    expect(classifyModelError("404: model_not_found")).toBe("model_not_found");
  });

  it("recognises a credential failure", () => {
    expect(
      classifyModelError('502: {"code":"provider_unavailable","message":"provider \\"nokey\\" has no key configured"}'),
    ).toBe("credentials");
    expect(classifyModelError("401: invalid api key")).toBe("credentials");
  });

  it("recognises rate limits and upstream outages", () => {
    expect(classifyModelError("429: rate limit exceeded")).toBe("rate_limit");
    expect(classifyModelError('502: {"code":"upstream_error","message":"upstream request failed"}')).toBe(
      "upstream",
    );
    expect(classifyModelError("504: upstream timeout")).toBe("upstream");
  });
});

describe("modelErrorAdvice", () => {
  it("sends a wrong model id to the model list, not to the key field", () => {
    const advice = modelErrorAdvice("404: model_not_found: the requested model does not exist");
    expect(advice).toContain("模型 id");
    expect(advice).not.toContain("密钥");
    expect(modelErrorTitle("404: model_not_found")).toBe("这个模型用不了");
  });

  it("keeps the credential advice for a real credential failure", () => {
    const advice = modelErrorAdvice('502: provider "volc" has no key configured');
    expect(advice).toContain("地址与密钥");
    expect(modelErrorTitle('502: provider "volc" has no key configured')).toBe("模型调用失败");
  });

  it("names the settings page that exists", () => {
    for (const error of ["401: unauthorized", "404: model_not_found", "weird"]) {
      expect(modelErrorAdvice(error)).toContain("模型与凭据");
      expect(modelErrorAdvice(error)).not.toContain("设置 → 模型」");
    }
  });
});

describe("modelErrorText", () => {
  it("turns the gateway's keyless body into a Chinese sentence", () => {
    // The exact string the acceptance tester saw in the transcript.
    const raw =
      '502: {"error":{"code":"provider_unavailable","message":"provider \\"shared-anthropic\\" has no key configured"}}';
    const text = modelErrorText(raw);
    expect(text).toContain("shared-anthropic");
    expect(text).toContain("API Key");
    expect(text).toContain("模型与凭据");
    expect(text).not.toContain('"error"');
    expect(text).not.toContain("provider_unavailable");
    expect(text).not.toContain("502");
  });

  it("still names the provider when the body has no error envelope", () => {
    // The gateway writes `{"error":{…}}`; Pi may keep only the inner object.
    const text = modelErrorText(
      '400: {"code":"provider_unavailable","message":"provider \\"nokey\\" has no key configured"}',
    );
    expect(text).toContain("nokey");
    expect(text).not.toContain("provider_unavailable");
  });

  it("keeps a non-gateway error exactly as it arrived", () => {
    const raw = "404: model_not_found: the requested model does not exist";
    expect(modelErrorText(raw)).toBe(raw);
  });
});
