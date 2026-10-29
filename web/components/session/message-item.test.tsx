import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { Turn } from "@/lib/session/reduce";

import { MessageItem } from "./message-item";

function assistant(overrides: Partial<Turn> = {}): Turn {
  return {
    id: "turn-1",
    role: "assistant",
    blocks: [],
    streaming: false,
    ts: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("MessageItem", () => {
  it("shows the provider error instead of an empty bubble", () => {
    // The failure mode this exists for: the model could not be reached, so the
    // turn has no content. A bare "PiForge" label reads as a broken page.
    render(<MessageItem turn={assistant({ error: '502: provider "volc" is not available' })} />);

    const alert = screen.getByTestId("turn-error");
    expect(alert).toHaveTextContent("模型调用失败");
    expect(alert).toHaveTextContent("not available");
    // A credential failure is the one case where the key advice is right.
    expect(alert).toHaveTextContent("地址与密钥");
  });

  it("gives model-specific advice when the model id does not exist", () => {
    // QA-14: a typo'd model id sent the user to the settings page to check a key
    // that was fine.
    render(
      <MessageItem
        turn={assistant({
          error: '404: {"code":"model_not_found","message":"The requested model does not exist."}',
        })}
      />,
    );

    const alert = screen.getByTestId("turn-error");
    expect(alert).toHaveTextContent("这个模型用不了");
    expect(alert).toHaveTextContent("模型 id");
    expect(alert).not.toHaveTextContent("地址与密钥");
  });

  it("renders nothing for an empty assistant turn", () => {
    const { container } = render(<MessageItem turn={assistant()} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("still renders the thinking indicator while streaming", () => {
    render(<MessageItem turn={assistant({ streaming: true })} />);
    expect(screen.getByText("正在思考…")).toBeInTheDocument();
  });
});
