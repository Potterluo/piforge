import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

vi.mock("@/lib/api/client", () => ({
  api: {
    sessions: {
      send: vi.fn(),
      interrupt: vi.fn(),
      update: vi.fn(),
    },
  },
}));

import { api } from "@/lib/api/client";
import { Composer } from "@/components/session/composer";
import type { SessionSkill, Skill } from "@/lib/types";

const send = vi.mocked(api.sessions.send);
const interrupt = vi.mocked(api.sessions.interrupt);

const review: Skill = {
  id: "skl_review",
  scope: "admin",
  name: "code-review",
  version: "1.0.0",
  description: "审查当前工作区的改动，只报告真实存在的问题。",
  content_hash: "abcdef0123456789abcdef0123456789",
  enabled: true,
  created_at: "2026-01-01T00:00:00.000Z",
};

const gaps: Skill = {
  id: "skl_gaps",
  scope: "admin",
  name: "test-gap",
  version: "0.0.0",
  description: "找出改动中缺少测试覆盖的行为。",
  content_hash: "fedcba9876543210fedcba9876543210",
  enabled: true,
  created_at: "2026-01-01T00:00:00.000Z",
};

const sessionRecord: SessionSkill = {
  session_id: "s1",
  skill_id: "skl_review",
  name: "code-review",
  version: "0.9.0",
  content_hash: "0011223344556677",
  description: review.description,
  mounted_path: "/data/sandboxes/demo/repo/.pi/skills/code-review",
  recorded_at: "2026-01-01T00:00:00.000Z",
};

function renderComposer(options: {
  skills?: Skill[];
  sessionSkills?: SessionSkill[];
  running?: boolean;
} = {}) {
  render(
    <Composer
      sessionId="s1"
      running={options.running ?? false}
      queue={[]}
      skills={options.skills ?? [review, gaps]}
      sessionSkills={options.sessionSkills ?? []}
    />,
  );
  return screen.getByLabelText("消息输入") as HTMLTextAreaElement;
}

beforeEach(() => {
  vi.clearAllMocks();
  send.mockResolvedValue({
    accepted: true,
    disposition: "started",
    queue_position: 0,
    run_id: "run-1",
  });
  interrupt.mockResolvedValue({ cleared_steering: [], cleared_follow_up: [] });
});

describe("skill palette (FR-14.6)", () => {
  it("opens on / with every skill, its description and version", () => {
    const textarea = renderComposer();

    fireEvent.change(textarea, { target: { value: "/" } });

    expect(textarea).toHaveAttribute("aria-expanded", "true");
    const list = screen.getByRole("listbox", { name: "Skill 命令" });
    expect(list).toBeVisible();
    const items = screen.getAllByTestId("skill-palette-item");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveAttribute("data-name", "code-review");
    expect(items[0]).toHaveTextContent("v1.0.0");
    expect(items[0]).toHaveTextContent("abcdef01");
    expect(items[0]).toHaveTextContent("审查当前工作区");
    expect(items[0]).toHaveAttribute("aria-selected", "true");
    // The active option is announced through the input, not by moving focus.
    expect(textarea).toHaveAttribute("aria-activedescendant", items[0].id);
  });

  it("lists one row per name: the private copy, with the shadowed shared one explained", () => {
    const shared: Skill = {
      ...review,
      id: "skl_shared_review",
      name: "review",
      scope: "admin",
      version: "1.0.0",
      effective: false,
    };
    const mine: Skill = {
      ...review,
      id: "skl_private_review",
      name: "review",
      scope: "user",
      version: "2.0.0",
      description: "我的审查口径",
      effective: true,
    };
    const textarea = renderComposer({ skills: [shared, mine] });

    fireEvent.change(textarea, { target: { value: "/review" } });

    // Two identical "/skill:review" rows would be a guess about which one runs;
    // only the effective row is offered.
    const items = screen.getAllByTestId("skill-palette-item");
    expect(items).toHaveLength(1);
    expect(items[0]).toHaveAttribute("data-name", "review");
    expect(items[0]).toHaveAttribute("data-version", "2.0.0");
    expect(items[0]).toHaveAttribute("data-overrides", "shared");
    expect(items[0]).toHaveTextContent("覆盖同名共享 Skill");
  });
  it("filters as the user types and marks a no-match state", () => {
    const textarea = renderComposer();

    fireEvent.change(textarea, { target: { value: "/test" } });

    const items = screen.getAllByTestId("skill-palette-item");
    expect(items).toHaveLength(1);
    expect(items[0]).toHaveAttribute("data-name", "test-gap");

    fireEvent.change(textarea, { target: { value: "/nothing-like-this" } });

    expect(screen.queryAllByTestId("skill-palette-item")).toHaveLength(0);
    expect(screen.getByTestId("skill-palette-no-match")).toHaveTextContent("没有匹配");
  });

  it("navigates with the arrow keys and inserts on Enter without sending", () => {
    const textarea = renderComposer();

    fireEvent.change(textarea, { target: { value: "/" } });
    fireEvent.keyDown(textarea, { key: "ArrowDown" });

    const items = screen.getAllByTestId("skill-palette-item");
    expect(items[1]).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(textarea, { key: "Enter" });

    expect(textarea.value).toBe("/skill:test-gap ");
    expect(send).not.toHaveBeenCalled();
    expect(textarea).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    // The invocation is a chip in the composer, with the version it will use.
    const chip = screen.getByTestId("composer-skill-chip");
    expect(chip).toHaveAttribute("data-name", "test-gap");
    expect(chip).toHaveTextContent("v0.0.0");
  });

  it("closes on Escape without interrupting the run", () => {
    const textarea = renderComposer({ running: true });

    fireEvent.change(textarea, { target: { value: "/rev" } });
    expect(screen.getByRole("listbox")).toBeVisible();

    fireEvent.keyDown(textarea, { key: "Escape" });

    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(textarea.value).toBe("/rev");
    expect(interrupt).not.toHaveBeenCalled();
  });

  it("says the account has no skills and links to Skill 中心", () => {
    const textarea = renderComposer({ skills: [] });

    fireEvent.change(textarea, { target: { value: "/" } });

    expect(screen.getByTestId("skill-palette-empty")).toHaveTextContent("还没有可用的 Skill");
    expect(screen.getByTestId("skill-palette-empty-link")).toHaveAttribute(
      "href",
      "/settings/skills",
    );
  });

  it("shows the mounted version in the chip, not the library's current one", () => {
    const textarea = renderComposer({ sessionSkills: [sessionRecord] });

    fireEvent.change(textarea, { target: { value: "/skill:code-review 看下这次改动" } });

    const chip = screen.getByTestId("composer-skill-chip");
    expect(chip).toHaveAttribute("data-version", "0.9.0");
    expect(chip).toHaveAttribute("data-hash", "0011223344556677");
    expect(chip).toHaveTextContent("本会话挂载的版本");
    // A space ends the command token, so the palette is closed while writing
    // the actual instruction.
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });
});
