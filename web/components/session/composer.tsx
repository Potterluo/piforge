"use client";

import { useRef, useState } from "react";
import {
  ArrowUp,
  CornerDownLeft,
  LoaderCircle,
  Paperclip,
  PencilLine,
  SquareStop,
  X,
  Zap,
} from "lucide-react";
import { toast } from "sonner";

import {
  Attachment,
  AttachmentAction,
  AttachmentActions,
  AttachmentContent,
  AttachmentDescription,
  AttachmentMedia,
  AttachmentTitle,
} from "@/components/ui/attachment";
import { Button } from "@/components/ui/button";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupTextarea,
} from "@/components/ui/input-group";
import { Item, ItemActions, ItemContent, ItemDescription, ItemTitle } from "@/components/ui/item";
import { Kbd } from "@/components/ui/kbd";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { api } from "@/lib/api/client";
import { shortModel, truncate } from "@/lib/session/format";
import type { QueuedPrompt } from "@/lib/session/reduce";
import { cn } from "@/lib/utils";

export interface ModelOption {
  value: string;
  label: string;
}

export interface ComposerTestIds {
  input?: string;
  send?: string;
  steer?: string;
  stop?: string;
  queue?: string;
}

export interface ComposerProps {
  sessionId: string;
  /** A run is in flight: Enter queues instead of sending. */
  running: boolean;
  queue?: QueuedPrompt[];
  modelRef?: string;
  thinkingLevel?: string;
  models?: ModelOption[];
  compact?: boolean;
  disabled?: boolean;
  placeholder?: string;
  className?: string;
  /** The sidecar reuses this composer with its own e2e hooks. */
  testIds?: ComposerTestIds;
}

interface ImageDraft {
  id: string;
  mimeType: string;
  base64: string;
  preview: string;
}

const THINKING_OPTIONS = [
  { value: "default", label: "跟随默认" },
  { value: "off", label: "不推理" },
  { value: "low", label: "低" },
  { value: "medium", label: "中" },
  { value: "high", label: "高" },
];

function newId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `msg-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function readAsDataUrl(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result ?? ""));
    reader.onerror = () => reject(new Error("读取图片失败"));
    reader.readAsDataURL(file);
  });
}

/**
 * The dual-queue composer (PRD §10.5.1).
 *
 * The two behaviours are genuinely different Pi paths, so they are two
 * buttons with two labels, never one button whose meaning depends on a
 * modifier the user cannot see:
 *   - Enter while running  → follow_up  → "下一轮执行"
 *   - ⌘/Ctrl+Enter         → steer      → "立即打断并改口"
 *   - Esc                  → interrupt  → queued text comes back to the box
 */
export function Composer({
  sessionId,
  running,
  queue = [],
  modelRef,
  thinkingLevel,
  models,
  compact = false,
  disabled = false,
  placeholder,
  className,
  testIds,
}: ComposerProps) {
  const [text, setText] = useState("");
  const [images, setImages] = useState<ImageDraft[]>([]);
  const [sending, setSending] = useState(false);
  const [interrupting, setInterrupting] = useState(false);
  const [modelOverride, setModelOverride] = useState<string | null>(null);
  const [thinkingOverride, setThinkingOverride] = useState<string | null>(null);
  const model = modelOverride ?? (modelRef || "inherit");
  const thinking = thinkingOverride ?? (thinkingLevel || "default");
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const submit = async (behavior?: "steer" | "follow_up") => {
    const trimmed = text.trim();
    if (disabled || sending) return;
    if (!trimmed && images.length === 0) return;

    setSending(true);
    try {
      const result = await api.sessions.send(sessionId, {
        client_msg_id: newId(),
        text: trimmed,
        images: images.length
          ? images.map((image) => ({ mimeType: image.mimeType, data: image.base64 }))
          : undefined,
        // Only present while a run is in flight: the backend rejects it
        // otherwise, and an idle send must not look like a queue.
        ...(behavior ? { streaming_behavior: behavior } : {}),
      });

      setText("");
      setImages([]);
      if (result.disposition === "queued") {
        toast.success(`已加入队列（第 ${result.queue_position} 位）`, {
          description: behavior === "steer" ? "立即打断并改口" : "下一轮执行",
        });
      }
    } catch (error) {
      toast.error("发送失败", {
        description: error instanceof Error ? error.message : "未知错误",
      });
    } finally {
      setSending(false);
    }
  };

  const interrupt = async (successMessage = "已中断当前 run") => {
    if (interrupting) return;
    setInterrupting(true);
    try {
      const result = await api.sessions.interrupt(sessionId);
      const restored = [...result.cleared_steering, ...result.cleared_follow_up].join("\n\n");
      if (restored) {
        setText((current) => (current ? `${restored}\n\n${current}` : restored));
      }
      toast.info(successMessage, {
        description: restored ? "排队消息已回填到输入框" : undefined,
      });
      textareaRef.current?.focus();
    } catch (error) {
      toast.error("中断失败", {
        description: error instanceof Error ? error.message : "未知错误",
      });
    } finally {
      setInterrupting(false);
    }
  };

  const takeBackQueued = async (target: QueuedPrompt) => {
    // There is no per-item queue API: interrupt clears the queue and hands the
    // texts back, which is exactly the edit affordance the queue needs.
    await interrupt("已取回排队消息，可在输入框编辑");
    if (target.text) {
      setText((current) => (current ? `${current}\n\n${target.text}` : target.text));
    }
  };

  const onKeyDown = (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.nativeEvent.isComposing) return;

    if (event.key === "Escape") {
      event.preventDefault();
      if (running) void interrupt("已中断，排队消息已回填");
      return;
    }
    if (event.key !== "Enter" || event.shiftKey) return;

    event.preventDefault();
    if (running) {
      void submit(event.metaKey || event.ctrlKey ? "steer" : "follow_up");
      return;
    }
    void submit();
  };

  const addImages = async (files: File[]) => {
    const picked = files.filter((file) => file.type.startsWith("image/")).slice(0, 4);
    if (picked.length === 0) return;
    try {
      const drafts = await Promise.all(
        picked.map(async (file) => {
          const dataUrl = await readAsDataUrl(file);
          return {
            id: newId(),
            mimeType: file.type,
            base64: dataUrl.split(",")[1] ?? "",
            preview: dataUrl,
          } satisfies ImageDraft;
        }),
      );
      setImages((current) => [...current, ...drafts].slice(0, 4));
    } catch (error) {
      toast.error("添加图片失败", {
        description: error instanceof Error ? error.message : "未知错误",
      });
    }
  };

  const persistSetting = async (patch: { model_ref?: string; thinking_level?: string }) => {
    try {
      // The Go PATCH accepts thinking_level; the frozen client signature in
      // lib/api/client.ts predates it, so the wider shape is narrowed here.
      const body = patch as Parameters<typeof api.sessions.update>[1];
      await api.sessions.update(sessionId, body);
    } catch (error) {
      toast.error("更新会话设置失败", {
        description: error instanceof Error ? error.message : "未知错误",
      });
    }
  };

  const modelItems = models?.length ? models : [];

  return (
    <div className={cn("flex flex-col gap-2 border-t border-border bg-background p-3", className)}>
      {queue.length > 0 ? (
        <div
          className="flex flex-col gap-1"
          aria-label="排队中的消息"
          data-testid={testIds?.queue ?? "composer-queue"}
        >
          {queue.map((item, index) => (
            <Item key={`${item.behavior}-${index}`} size="xs" variant="muted" className="py-1">
              <ItemContent className="gap-0">
                <ItemTitle className="text-[11px] text-muted-foreground">
                  {item.behavior === "steer" ? "立即改口" : "下一轮执行"}
                </ItemTitle>
                <ItemDescription className="line-clamp-1 text-xs">
                  {truncate(item.text, 160)}
                </ItemDescription>
              </ItemContent>
              <ItemActions className="gap-0.5">
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label="取回这条排队消息"
                  title="取回编辑（会中断当前 run）"
                  onClick={() => void takeBackQueued(item)}
                >
                  <PencilLine />
                </Button>
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label="清空队列"
                  title="清空队列（会中断当前 run）"
                  onClick={() => void interrupt("已中断并清空队列")}
                >
                  <X />
                </Button>
              </ItemActions>
            </Item>
          ))}
        </div>
      ) : null}

      <InputGroup className="h-auto flex-col items-stretch">
        {images.length > 0 ? (
          <InputGroupAddon align="block-start" className="flex-wrap gap-2">
            {images.map((image) => (
              <Attachment key={image.id} size="xs" className="w-fit">
                <AttachmentMedia variant="image">
                  {/* Blob URL previews are local-only and never leave the tab. */}
                  {/* eslint-disable-next-line @next/next/no-img-element */}
                  <img src={image.preview} alt="" />
                </AttachmentMedia>
                <AttachmentContent>
                  <AttachmentTitle>图片</AttachmentTitle>
                  <AttachmentDescription>随消息一起发送</AttachmentDescription>
                </AttachmentContent>
                <AttachmentActions>
                  <AttachmentAction
                    aria-label="移除图片"
                    onClick={() => setImages((current) => current.filter((i) => i.id !== image.id))}
                  >
                    <X />
                  </AttachmentAction>
                </AttachmentActions>
              </Attachment>
            ))}
          </InputGroupAddon>
        ) : null}

        <InputGroupTextarea
          ref={textareaRef}
          data-testid={testIds?.input ?? "composer-input"}
          value={text}
          onChange={(event) => setText(event.target.value)}
          onKeyDown={onKeyDown}
          onPaste={(event) => {
            const files = Array.from(event.clipboardData?.files ?? []);
            if (files.some((file) => file.type.startsWith("image/"))) {
              event.preventDefault();
              void addImages(files);
            }
          }}
          rows={compact ? 1 : 2}
          disabled={disabled}
          placeholder={
            placeholder ??
            (running ? "补充说明：Enter 下一轮执行 · ⌘↵ 立即改口" : "描述你想让 agent 做的事…")
          }
          aria-label="消息输入"
          className="max-h-40 min-h-9 field-sizing-content text-sm"
        />

        <InputGroupAddon align="block-end" className="justify-end gap-1">
          <input
            ref={fileInputRef}
            type="file"
            accept="image/*"
            multiple
            className="hidden"
            onChange={(event) => {
              void addImages(Array.from(event.target.files ?? []));
              event.target.value = "";
            }}
          />
          <InputGroupButton
            size="icon-xs"
            aria-label="粘贴或选择图片"
            title="粘贴或选择图片"
            onClick={() => fileInputRef.current?.click()}
            disabled={disabled}
          >
            <Paperclip />
          </InputGroupButton>
          {running ? (
            <Button
              type="button"
              size="sm"
              variant="secondary"
              data-testid={testIds?.send ?? "composer-send"}
              onClick={() => void submit("follow_up")}
              disabled={disabled || sending}
              title="当前 run 结束后再处理（Enter）"
            >
              <CornerDownLeft />
              下一轮执行
            </Button>
          ) : (
            <Button
              type="button"
              size="icon-xs"
              data-testid={testIds?.send ?? "composer-send"}
              aria-label="发送"
              title="发送（Enter）"
              onClick={() => void submit()}
              disabled={disabled || sending}
            >
              {sending ? <LoaderCircle className="animate-spin" /> : <ArrowUp />}
            </Button>
          )}
        </InputGroupAddon>
      </InputGroup>

      <div className="flex flex-wrap items-center gap-2">
        {running ? (
          <>
            <Button
              type="button"
              size="sm"
              data-testid={testIds?.steer ?? "composer-steer"}
              onClick={() => void submit("steer")}
              disabled={disabled || sending}
              title="当前 turn 的工具跑完后立刻注入（⌘/Ctrl + Enter）"
            >
              <Zap />
              立即打断并改口
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              data-testid={testIds?.stop ?? "composer-stop"}
              onClick={() => void interrupt()}
              disabled={disabled || interrupting}
              title="停止并清空队列（Esc）"
            >
              <SquareStop />
              停止
              <Kbd>Esc</Kbd>
            </Button>
          </>
        ) : (
          <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
            <Kbd>Enter</Kbd> 发送 · <Kbd>Shift</Kbd>+<Kbd>Enter</Kbd> 换行
          </span>
        )}

        <div className="ml-auto flex flex-wrap items-center gap-2">
          {modelItems.length > 0 ? (
            <Select
              items={modelItems}
              value={model}
              onValueChange={(value) => {
                const next = String(value);
                setModelOverride(next);
                if (next !== "inherit") {
                  void persistSetting({ model_ref: next });
                }
              }}
            >
              <SelectTrigger
                size="sm"
                className="h-6 border-0 bg-transparent px-1 text-xs"
                aria-label="模型"
                disabled={disabled}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {modelItems.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : (
            <span className="text-[11px] text-muted-foreground">{shortModel(modelRef)}</span>
          )}

          <Select
            items={THINKING_OPTIONS}
            value={thinking}
            onValueChange={(value) => {
              const next = String(value);
              setThinkingOverride(next);
              void persistSetting({ thinking_level: next === "default" ? "" : next });
            }}
          >
            <SelectTrigger
              size="sm"
              className="h-6 border-0 bg-transparent px-1 text-xs"
              aria-label="推理强度"
              disabled={disabled}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {THINKING_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  推理 · {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
    </div>
  );
}
