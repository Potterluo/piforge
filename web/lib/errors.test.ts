import { describe, expect, it, vi } from "vitest";

import { humanizeError, repoUrlError } from "@/lib/errors";
import { ApiError } from "@/lib/types";

const apiError = (code: string, message: string) =>
  new ApiError(400, { error: { code, message } });

describe("humanizeError", () => {
  it("turns the three QA-13 English messages into Chinese", () => {
    expect(humanizeError(apiError("unauthenticated", "invalid email or password"))).toBe(
      "邮箱或密码不正确",
    );
    expect(humanizeError(apiError("invalid_argument", "this provider has no API key yet"))).toBe(
      "这个服务商还没有 API Key，请先填写后再测试",
    );
    expect(
      humanizeError(apiError("sandbox_unavailable", "this session has no worktree yet")),
    ).toBe("这个会话还没有工作区");
  });

  it("names the settings page that actually exists", () => {
    const text = humanizeError(
      apiError("no_model", 'the provider "volc" is not configured: add it in Settings → Models'),
    );
    expect(text).toContain("模型与凭据");
    expect(text).not.toContain("Settings");
    expect(text).not.toContain("Models");
  });

  it("keeps the provider name but not the English framing", () => {
    const text = humanizeError(apiError("no_model", 'the provider "volc" lists no models: add one'));
    expect(text).toContain("volc");
    expect(text).toContain("模型与凭据");
  });

  it("passes Chinese server messages through", () => {
    expect(humanizeError(apiError("invalid_argument", "至少选两个会话才能对比"))).toBe(
      "至少选两个会话才能对比",
    );
  });

  it("maps the gateway's provider_unavailable code to one Chinese sentence", () => {
    // What the gateway answers when the provider has no key (HTTP 400, code
    // provider_unavailable). The UI must never show the JSON body.
    expect(
      humanizeError(
        apiError("provider_unavailable", 'provider "shared-anthropic" has no key configured'),
      ),
    ).toBe("服务商「shared-anthropic」还没有配置 API Key，这一轮没有调用模型。请到「设置 → 模型与凭据」填写后重新发送");
    expect(
      humanizeError(apiError("provider_unavailable", 'provider "volc" is not available')),
    ).toContain("volc");
    expect(
      humanizeError(apiError("provider_unavailable", "stored key cannot be decrypted")),
    ).toContain("重新填写");
    // An unknown sentence under a known code still gets Chinese, not English.
    expect(humanizeError(apiError("provider_unavailable", "something new"))).toContain(
      "模型服务不可用",
    );
  });

  it("explains a shared provider on the wrong endpoint instead of saying not found", () => {
    const text = humanizeError(
      apiError(
        "shared_provider",
        'provider "volc" is shared with every user; cancel the share with DELETE /v1/admin/providers/prv_123',
      ),
    );
    expect(text).toContain("volc");
    expect(text).toContain("共享");
    expect(text).toContain("/v1/admin/providers/prv_123");
    expect(text).not.toContain("not_found");
  });

  it("does not leak an unmapped English message", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const text = humanizeError(apiError("internal", "some brand new english sentence"));
    expect(text).toBe("操作失败，请稍后重试");
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });

  it("maps a dead network to something actionable", () => {
    expect(humanizeError(new TypeError("Failed to fetch"))).toBe(
      "无法连接后端，请检查网络或服务是否在运行",
    );
  });
});

describe("repoUrlError", () => {
  it("allows an empty repository (a blank workspace)", () => {
    expect(repoUrlError("")).toBeNull();
    expect(repoUrlError("   ")).toBeNull();
  });

  it("accepts the two supported forms", () => {
    expect(repoUrlError("https://github.com/acme/cache-service.git")).toBeNull();
    expect(repoUrlError("https://git.example.com/team/repo")).toBeNull();
    expect(repoUrlError("git@github.com:acme/cache-service.git")).toBeNull();
    // The local sandbox provider clones a bare repository by absolute path.
    expect(repoUrlError("C:\\repos\\origin.git")).toBeNull();
    expect(repoUrlError("/tmp/origin.git")).toBeNull();
  });

  it("rejects a nonsense address, with a Chinese message", () => {
    const message = repoUrlError("not-a-repo-url");
    expect(message).toBeTruthy();
    expect(message).toMatch(/[\u4e00-\u9fff]/);
    expect(repoUrlError("http://github.com/acme/repo.git")).toBeTruthy();
    expect(repoUrlError("ftp://github.com/acme/repo.git")).toBeTruthy();
    expect(repoUrlError("https://")).toBeTruthy();
  });
});
