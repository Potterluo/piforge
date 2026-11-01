"use client";

import { useId, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  ArrowUp,
  CornerDownLeft,
  LoaderCircle,
  Paperclip,
  PencilLine,
  Sparkles,
  SquareStop,
  TriangleAlert,
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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
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
import { humanizeError } from "@/lib/errors";
import {
  MAX_COMPOSER_IMAGES,
  imageLimitNotice,
  planImageAttachments,
} from "@/lib/session/attachments";
import { shortModel, truncate } from "@/lib/session/format";
import { qk } from "@/lib/session/queries";
import type { QueuedPrompt } from "@/lib/session/reduce";
import { mountableSkills, overridesShared } from "@/lib/skills";
import { ApiError, type SessionSkill, type Skill } from "@/lib/types";
import { cn } from "@/lib/utils";

import { skillInvocation, skillVersionLabel, SkillPalette } from "./skill-palette";

export interface ModelOption {
  value: string;
  label: string;
  /**
   * False for a provider that cannot answer (no key). The row is still shown so
   * the model does not silently vanish from the picker; the reason is `note`.
   */
  selectable?: boolean;
  note?: string;
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
  /**
   * The account has no provider that can answer (disabled, no key or no models).
   * The workbench owns the explanation bar; the composer only accepts the flag so
   * the two stay in step about what the session can do.
   */
  noModel?: boolean;
  placeholder?: string;
  className?: string;
  /** Skills the account can invoke; typing `/` lists these (FR-14.6). */
  skills?: Skill[];
  /** True while the account's skill library is still loading. */
  skillsLoading?: boolean;
  /**
   * The versions this session was mounted with (FR-14.5). The invocation chip
   * reads this, so it shows the version the run will actually use rather than
   * whatever the skill's row says today.
   */
  sessionSkills?: SessionSkill[];
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
  { value: "default", label: "默认" },
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
 * slashQuery returns what the palette should filter by, or null when the draft
 * is not a bare command token.
 *
 * A space ends the token on purpose: `/skill:review 看下 diff` is a complete
 * prompt, not a command still being typed, so the palette must get out of the
 * way before the user finishes their sentence.
 */
export function slashQuery(text: string): string | null {
  if (!text.startsWith("/") || text.includes("\n")) return null;
  if (/\s/.test(text)) return null;
  const raw = text.slice(1);
  const prefix = "skill:";
  return raw.toLowerCase().startsWith(prefix) ? raw.slice(prefix.length) : raw;
}

/** filterSkills is the palette's search: name first, description second. */
export function filterSkills(skills: Skill[], query: string): Skill[] {
  const needle = query.trim().toLowerCase();
  return skills.filter((skill) => {
    if (!skill.enabled) return false;
    if (!needle) return true;
    return `${skill.name}\n${skill.description}`.toLowerCase().includes(needle);
  });
}

const INVOCATION_RE = /^\/skill:([^\s]+)/i;

/**
 * matchInvocation finds the skill named by a leading `/skill:name`.
 *
 * The record wins over the current row: the chip must describe what this
 * session will actually run, and after an edit those are different versions.
 */
function matchInvocation(
  text: string,
  skills: Skill[],
  records: SessionSkill[],
): { skill: Skill | null; record: SessionSkill | null; name: string } | null {
  const match = INVOCATION_RE.exec(text.trimStart());
  if (!match) return null;
  const name = match[1];
  const needle = name.toLowerCase();
  return {
    skill: skills.find((skill) => skill.name.toLowerCase() === needle) ?? null,
    record: records.find((record) => record.name.toLowerCase() === needle) ?? null,
    name,
  };
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
  skills = [],
  skillsLoading = false,
  sessionSkills = [],
  testIds,
}: ComposerProps) {
  const [text, setText] = useState("");
  const [images, setImages] = useState<ImageDraft[]>([]);
  const [sending, setSending] = useState(false);
  const [interrupting, setInterrupting] = useState(false);
  const [modelOverride, setModelOverride] = useState<string | null>(null);
  const [thinkingOverride, setThinkingOverride] = useState<string | null>(null);
  // FR-3.3: a parallel-limit refusal is a product answer, not an outage. It is
  // kept inline (with the server's own sentence naming the cap and the branches
  // occupying it) instead of a toast titled "发送失败", which hid the reason.
  const [refusal, setRefusal] = useState<string | null>(null);
  // The palette closes on Escape and on insert even though the draft still
  // starts with "/", so the open state needs to be more than "text starts with
  // slash". Any further typing reopens it: the user is editing the command.
  const [paletteDismissed, setPaletteDismissed] = useState(false);
  const [paletteIndex, setPaletteIndex] = useState(0);
  // A re-resolved model changes the session row and its runtime status; both
  // are read elsewhere (header, picker), so they are refreshed after the send.
  const queryClient = useQueryClient();
  const model = modelOverride ?? (modelRef || "inherit");
  const thinking = thinkingOverride ?? (thinkingLevel || "default");
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const paletteId = useId();

  const query = slashQuery(text);
  // The palette lists what provisioning would actually mount: enabled rows that
  // are effective for this user. Listing a shadowed shared row and the private
  // row that hides it as two identical "/skill:review" entries was how "same
  // name" became a guess (FR-14.1).
  const enabledSkills = useMemo(() => mountableSkills(skills), [skills]);
  const overridingShared = useMemo(
    () =>
      new Set(
        enabledSkills.filter((skill) => overridesShared(skill, skills)).map((skill) => skill.name),
      ),
    [enabledSkills, skills],
  );
  const paletteItems = useMemo(
    () => (query === null ? [] : filterSkills(enabledSkills, query)),
    [enabledSkills, query],
  );
  const paletteOpen = !disabled && query !== null && !paletteDismissed;
  const activeIndex =
    paletteItems.length === 0 ? -1 : Math.min(paletteIndex, paletteItems.length - 1);
  const invocation = useMemo(
    () => matchInvocation(text, enabledSkills, sessionSkills),
    [text, enabledSkills, sessionSkills],
  );

  // A new filter starts at the top: the cursor must not stay on item five of a
  // list that no longer has five items. Reset happens where the filter changes
  // (the input), not in an effect that would re-render the whole composer.

  const insertSkill = (skill: Skill) => {
    setText(skillInvocation(skill));
    setPaletteDismissed(true);
    setPaletteIndex(0);
    // The textarea is where the user continues typing; a click on an option must
    // not leave focus on a button that is about to disappear.
    requestAnimationFrame(() => {
      const input = textareaRef.current;
      if (!input) return;
      input.focus();
      input.setSelectionRange(input.value.length, input.value.length);
    });
  };

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
      setRefusal(null);
      if (result.disposition === "queued") {
        toast.success(`已加入队列（第 ${result.queue_position} 位）`, {
          description: behavior === "steer" ? "立即打断并改口" : "下一轮执行",
        });
      }
      // The control plane had to replace a model reference that no longer
      // resolved (its provider was deleted or re-created). Say so, name the
      // model the turn actually used, and refresh the header so it agrees.
      if (result.model_fallback_from) {
        toast.info("已自动切换模型", {
          description: `原模型「${result.model_fallback_from}」已不可用，本轮改用「${
            result.model_ref ?? "默认模型"
          }」。要恢复原配置，请到「设置 → 模型与凭据」检查该服务商。`,
        });
        void queryClient.invalidateQueries({ queryKey: qk.session(sessionId) });
        void queryClient.invalidateQueries({ queryKey: ["session-status", sessionId] });
      }
    } catch (error) {
      // The server explained exactly what is running (FR-3.3). Show that
      // sentence, keep the draft, and do not dress it up as a generic failure.
      if (error instanceof ApiError && error.status === 429) {
        setRefusal(humanizeError(error));
        return;
      }
      // A refused send is also new information about the account: the provider
      // it pinned may have just been deleted. Refreshing the session, its
      // status and the provider list makes the header and the no-model bar say
      // what is true now, instead of waiting for the next poll.
      if (error instanceof ApiError && error.code === "no_model") {
        void queryClient.invalidateQueries({ queryKey: qk.session(sessionId) });
        void queryClient.invalidateQueries({ queryKey: ["session-status", sessionId] });
        void queryClient.invalidateQueries({ queryKey: qk.providers });
      }
      toast.error("发送失败", {
        description: humanizeError(error),
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
        description: humanizeError(error),
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

    // The palette owns the arrow keys and Enter while it is open (FR-14.6).
    // Escape closes it before it can mean "interrupt": a user dismissing a
    // suggestion list must not stop the agent.
    if (paletteOpen) {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        if (paletteItems.length > 0) {
          const delta = event.key === "ArrowDown" ? 1 : -1;
          setPaletteIndex(
            (current) => (current + delta + paletteItems.length) % paletteItems.length,
          );
        }
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        setPaletteDismissed(true);
        return;
      }
      if (event.key === "Enter" && !event.shiftKey) {
        // Enter picks the highlighted skill instead of sending "/skill:" as a
        // prompt. With no match there is nothing to pick, and sending a
        // half-typed command is never what the user meant.
        event.preventDefault();
        const picked = paletteItems[activeIndex];
        if (picked) insertSkill(picked);
        return;
      }
    }

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
    const candidates = files.filter((file) => file.type.startsWith("image/"));
    if (candidates.length === 0) return;
    // The cap is four images per message. Dropping the rest silently would let
    // someone paste six and believe the agent saw six, so the count that did
    // not fit is said out loud — before the send, next to the composer.
    const { accepted, dropped } = planImageAttachments(candidates.length, images.length);
    if (dropped > 0) {
      toast.warning("图片没有全部加入", { description: imageLimitNotice(dropped) });
    }
    const picked = candidates.slice(0, accepted);
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
      setImages((current) => [...current, ...drafts].slice(0, MAX_COMPOSER_IMAGES));
    } catch (error) {
      toast.error("添加图片失败", {
        description: humanizeError(error),
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
        description: humanizeError(error),
      });
    }
  };

  // A model id the provider list does not know yet. Pi takes any id the endpoint
  // accepts, so typing one here must work without a trip to Settings first: the
  // gateway forwards it and the session's model config gains it at spawn time.
  const [customOpen, setCustomOpen] = useState(false);
  const [customModel, setCustomModel] = useState("");

  const modelItems = models?.length
    ? [...models, { value: "__custom", label: "自定义模型 id…" }]
    : [];

  return (
    <div className={cn("flex flex-col gap-2 border-t border-border bg-background p-3", className)}>
      {refusal ? (
        <div
          role="alert"
          data-testid="parallel-limit-refusal"
          className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/5 p-2 text-xs"
        >
          <TriangleAlert className="mt-0.5 size-3.5 shrink-0 text-destructive" />
          <div className="flex min-w-0 flex-1 flex-col gap-0.5">
            <span className="font-medium text-destructive">这次没有发出去</span>
            <span data-testid="parallel-limit-reason" className="break-words text-muted-foreground">
              {refusal}
            </span>
            <span className="text-[11px] text-muted-foreground">
              草稿还在输入框里。停掉其中一个分支，或等它跑完，再发一次。
            </span>
          </div>
          <button
            type="button"
            aria-label="关闭提示"
            title="关闭提示"
            className="rounded-sm p-0.5 text-muted-foreground hover:text-foreground"
            onClick={() => setRefusal(null)}
          >
            <X className="size-3.5" />
          </button>
        </div>
      ) : null}
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

      {invocation && (invocation.record || invocation.skill) ? (
        <div className="flex flex-wrap items-center gap-2" data-testid="composer-skill-chip"
          data-name={invocation.record?.name ?? invocation.skill?.name ?? invocation.name}
          data-version={invocation.record?.version ?? invocation.skill?.version ?? ""}
          data-hash={invocation.record?.content_hash ?? invocation.skill?.content_hash ?? ""}
        >
          <Badge variant="secondary" className="max-w-full gap-1.5 text-[11px]">
            <Sparkles className="size-3 shrink-0" />
            <span className="truncate font-medium">
              {invocation.record?.name ?? invocation.skill?.name ?? invocation.name}
            </span>
            <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
              {invocation.record
                ? `v${invocation.record.version} · ${invocation.record.content_hash.slice(0, 8)}`
                : skillVersionLabel(invocation.skill!)}
            </span>
          </Badge>
          <span className="text-[10px] text-muted-foreground">
            {invocation.record
              ? "本会话挂载的版本"
              : "当前版本：下次创建会话时才会挂载"}
          </span>
        </div>
      ) : null}

      {paletteOpen ? (
        <SkillPalette
          id={paletteId}
          items={paletteItems}
          total={enabledSkills.length}
          overridingShared={overridingShared}
          activeIndex={activeIndex}
          query={query ?? ""}
          loading={skillsLoading}
          onSelect={insertSkill}
          onHover={setPaletteIndex}
        />
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
          onChange={(event) => {
            setText(event.target.value);
            // Any edit reopens a palette closed with Escape (the user is editing
            // the command) and restarts the cursor at the first match, so the
            // highlighted row is always one the current filter produced.
            setPaletteDismissed(false);
            setPaletteIndex(0);
          }}
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
          // Combobox semantics: focus stays in the textarea and the active
          // option is announced through aria-activedescendant, which is what
          // makes the palette usable with a screen reader (FR-14.6).
          role="combobox"
          aria-expanded={paletteOpen}
          aria-controls={paletteOpen ? paletteId : undefined}
          aria-autocomplete="list"
          aria-activedescendant={
            paletteOpen && activeIndex >= 0 && paletteItems[activeIndex]
              ? `${paletteId}-${paletteItems[activeIndex].id}`
              : undefined
          }
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
              aria-label="停止"
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
                if (next === "__custom") {
                  setCustomModel(model === "inherit" ? "" : model);
                  setCustomOpen(true);
                  return;
                }
                setModelOverride(next);
                // "inherit" is a choice too: it clears the session's pinned
                // model, and leaving the old ref in the row made the header and
                // the picker disagree.
                void persistSetting({ model_ref: next === "inherit" ? "" : next });
                // A model switch cannot be applied to a live agent process: Pi
                // resolves the model at spawn (models.json + the session's
                // gateway token). Saying so is the difference between a control
                // that works and one that only looks like it does.
                toast.info("已切换模型", {
                  description:
                    next === "inherit"
                      ? "下一次发送会用默认模型重启这个会话的 agent。"
                      : `下一次发送会用 ${next} 重启这个会话的 agent。`,
                });
              }}
            >
              <SelectTrigger
                size="sm"
                data-testid="composer-model"
                className="h-6 border-0 bg-transparent px-1 text-xs"
                aria-label="模型"
                disabled={disabled}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent className="min-w-64">
                {modelItems.map((option) => (
                  <SelectItem
                    key={option.value}
                    value={option.value}
                    disabled={option.selectable === false}
                    data-testid="composer-model-option"
                    data-selectable={option.selectable === false ? "false" : "true"}
                  >
                    <span className="flex min-w-0 flex-col items-start">
                      <span className="max-w-full truncate">{option.label}</span>
                      {option.note ? (
                        <span className="text-[10px] text-muted-foreground">{option.note}</span>
                      ) : null}
                    </span>
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

      <Dialog open={customOpen} onOpenChange={setCustomOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>自定义模型</DialogTitle>
            <DialogDescription>
              格式是「provider/模型 id」。id 由端点决定，Pi 会把它原样发给 provider，所以端点认什么就填什么。
            </DialogDescription>
          </DialogHeader>
          <Field>
            <FieldLabel htmlFor="custom-model">模型</FieldLabel>
            <Input
              id="custom-model"
              data-testid="custom-model-input"
              value={customModel}
              onChange={(event) => setCustomModel(event.target.value)}
              placeholder="openrouter/anthropic/claude-sonnet-4"
              className="font-mono text-xs"
              autoFocus
            />
            <FieldDescription>
              {customModel.trim() && !models?.some((option) => option.value === customModel.trim()) ? (
                <span className="text-amber-600 dark:text-amber-500">
                  这个 id 不在任何 provider 的模型列表里：端点多半会拒绝。确认拼写，或先在「设置 → 模型」里加上。
                </span>
              ) : (
                "新 id 会写进这个会话的模型配置，提供给 Pi 使用；不改动 provider 本身。"
              )}
            </FieldDescription>
          </Field>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCustomOpen(false)}>
              取消
            </Button>
            <Button
              data-testid="custom-model-submit"
              disabled={!/^[^/]+\/.+$/.test(customModel.trim())}
              onClick={() => {
                const next = customModel.trim();
                setModelOverride(next);
                void persistSetting({ model_ref: next });
                toast.info("已切换模型", {
                  description: `下一次发送会用 ${next} 重启这个会话的 agent。`,
                });
                setCustomOpen(false);
              }}
            >
              使用
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}