/**
 * The session event reducer.
 *
 * SSE delivers a flat, monotonic stream of `PfEvent`s. Re-rendering that raw
 * stream would mean every component knows the wire format, so this module folds
 * events into a small render model (`Turn` + `Block`) that the session stream
 * components read directly. It is pure on purpose: the two traps in the Pi
 * protocol (message.final replaces, run.settled is the only completion signal)
 * are cheap to test here and expensive to debug in a browser.
 */
import { EV, type CompactionPayload, type PfEvent } from "@/lib/types";

export type Block =
  | { kind: "text"; text: string; index?: number }
  | { kind: "thinking"; text: string; index?: number }
  | {
      kind: "tool";
      id: string;
      name: string;
      args?: unknown;
      chunk: string;
      done: boolean;
      isError?: boolean;
      /**
       * The run was stopped while this call was in flight. Pi reports the aborted
       * shell as an error ("Command aborted"), which painted a red "1 个失败"
       * group after every interrupt — a normal outcome shown as a failure.
       */
      aborted?: boolean;
      durationMs?: number;
      index?: number;
    };

export interface Turn {
  id: string;
  role: "user" | "assistant";
  blocks: Block[];
  /** Cleared only by run.settled — never by turn.ended (see reduceEvent). */
  streaming: boolean;
  ts: string;
  /** Pi entry id when the event carried one; the fork API needs it. */
  entryId?: string;
  /**
   * The provider failure for this turn, verbatim. A failed model call still
   * produces an assistant message with no content, so without this the UI can
   * only draw an empty bubble and leave the user guessing.
   */
  error?: string;
  /**
   * How many times the provider failed for this turn. Pi retries a failed call
   * automatically, and each attempt lands as its own message; stacking four
   * identical errors says less than one error that admits it kept trying.
   */
  retries?: number;
  /**
   * Which model produced this turn, as "provider/model". The composer can point a
   * session at a custom id, and without this there is no way to tell what
   * actually answered.
   */
  model?: string;
  /**
   * Pi's stop reason for the message ("stop", "toolUse", "error", "aborted").
   * A user stop arrives as "error" with the abort text, so the reason is the only
   * structural hint that the failure was a stop.
   */
  stopReason?: string;
}

export interface ContextMarker {
  id: string;
  seq: number;
  ts: string;
  reason: string;
  summary?: string;
  tokensBefore?: number;
  tokensAfter?: number;
  firstKeptEntryId?: string;
}

export interface QueuedPrompt {
  text: string;
  behavior: "follow_up" | "steer";
}

export interface LiveUsage {
  tokens?: number;
  window?: number;
  percent?: number;
}

export interface SessionState {
  turns: Turn[];
  markers: ContextMarker[];
  /**
   * One-line facts that happened to the session from outside the conversation — a push,
   * a pull. They are recorded as events, and an event nobody renders is a claim nobody
   * can check.
   */
  notes: NoteMarker[];
  queue: QueuedPrompt[];
  usage?: LiveUsage;
  /** True between run.started and run.settled. */
  running: boolean;
  /**
   * The last run ended because the user stopped it. It is not an error: the
   * transcript says 已中断 and nothing else, and a later run clears it.
   */
  aborted: boolean;
  /** True until run.settled. Also drives the streaming cursor. */
  streaming: boolean;
  /**
   * Increments on every run that settles. Anything holding a cached view of the
   * workspace (the file tree, the session header, the sidebar) uses it as a
   * signal to refetch: after a run the agent has probably written files, and a
   * tree that only updates when the user clicks 刷新 is a tree that lies.
   */
  settled: number;
  lastSeq: number;
  /** Set when a seq gap is detected; the hook repairs the stream. */
  needsResync: boolean;
  error: string | null;
  /**
   * The message currently being streamed. The normalizer's delta payloads do
   * not carry a message id, so deltas are attached to this cursor instead of
   * creating a turn per event (which would split one answer into hundreds).
   */
  activeMessageId?: string;
}

/** A short factual line in the timeline, distinct from a compaction marker. */
export interface NoteMarker {
  id: string;
  seq: number;
  ts: string;
  text: string;
}

export const initialSessionState: SessionState = {
  turns: [],
  markers: [],
  notes: [],
  queue: [],
  running: false,
  streaming: false,
  aborted: false,
  settled: 0,
  lastSeq: 0,
  needsResync: false,
  error: null,
};

// ---------------------------------------------------------------------------
// Payload helpers
// ---------------------------------------------------------------------------

type Payload = Record<string, unknown>;

function asRecord(value: unknown): Payload {
  return value && typeof value === "object" ? (value as Payload) : {};
}

function str(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function num(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === "string") : [];
}

/**
 * Resolves the turn a message event belongs to.
 *
 * Two payload shapes exist in the wild: `lib/types.ts` documents
 * `message_id`/`content_index`/`block`, while the Go normalizer emits
 * `kind`/`index` and omits the message id. Both are accepted; when no id is
 * present the active-message cursor (or the newest assistant turn) is used.
 */
function messageIdOf(
  event: PfEvent,
  payload: Payload,
  state: SessionState,
  fallbackToTail: boolean,
): string {
  const explicit = str(payload.message_id) ?? str(event.entry_id);
  if (explicit) return explicit;
  if (state.activeMessageId) return state.activeMessageId;
  if (fallbackToTail) {
    for (let i = state.turns.length - 1; i >= 0; i -= 1) {
      if (state.turns[i].role === "assistant") return state.turns[i].id;
    }
  }
  return `msg-${event.seq}`;
}

// ---------------------------------------------------------------------------
// Turn mutation helpers
// ---------------------------------------------------------------------------

function findTurn(turns: Turn[], id: string): number {
  for (let i = turns.length - 1; i >= 0; i -= 1) {
    if (turns[i].id === id) return i;
  }
  return -1;
}

function replaceTurn(state: SessionState, index: number, update: (turn: Turn) => Turn): SessionState {
  const turns = state.turns.slice();
  turns[index] = update(turns[index]);
  return { ...state, turns };
}

function newTurn(id: string, role: Turn["role"], ts: string, streaming: boolean): Turn {
  return { id, role, blocks: [], streaming, ts };
}

/** The assistant turn that owns a tool call: the tail, created on demand. */
function assistantTail(state: SessionState, ts: string): { state: SessionState; index: number } {
  const last = state.turns.length - 1;
  if (last >= 0 && state.turns[last].role === "assistant") {
    return { state, index: last };
  }
  const turn = newTurn(`turn-${state.lastSeq}`, "assistant", ts, true);
  return { state: { ...state, turns: [...state.turns, turn] }, index: state.turns.length };
}

/**
 * Appends one delta to the block at `contentIndex`, creating the block on the
 * first delta. `content_index` is the model's content-block index, so it is
 * kept on the block: two streaming blocks (thinking at 0, text at 1) must not
 * merge just because they arrived interleaved.
 */
function appendDelta(
  blocks: Block[],
  contentIndex: number,
  kind: "text" | "thinking",
  delta: string,
): Block[] {
  const next = blocks.slice();
  const at = next.findIndex((block) => block.index === contentIndex);
  if (at >= 0) {
    const current = next[at];
    if (current.kind === kind) {
      next[at] = { ...current, text: current.text + delta };
    } else {
      // The backend corrected the block kind mid-stream; the latest wins.
      next[at] = { kind, text: delta, index: contentIndex };
    }
    return next;
  }

  const block: Block = { kind, text: delta, index: contentIndex };
  let insertAt = next.length;
  for (let i = 0; i < next.length; i += 1) {
    const index = next[i].index;
    if (index !== undefined && index > contentIndex) {
      insertAt = i;
      break;
    }
  }
  next.splice(insertAt, 0, block);
  return next;
}

/**
 * `message.final` is authoritative for text and thinking, but tool output
 * lives only in the tool.progress events. Replacing a turn wholesale would
 * erase a running tool's streamed output, so terminal tool state is carried
 * over by tool_call_id.
 */
function mergeToolState(existing: Block[], incoming: Block[]): Block[] {
  const live = new Map<string, Extract<Block, { kind: "tool" }>>();
  for (const block of existing) {
    if (block.kind === "tool") live.set(block.id, block);
  }
  return incoming.map((block) => {
    if (block.kind !== "tool") return block;
    const previous = live.get(block.id);
    if (!previous) return block;
    return {
      ...block,
      chunk: previous.chunk,
      done: previous.done,
      isError: previous.isError,
      durationMs: previous.durationMs,
    };
  });
}

/**
 * Drops the empty error turns left behind by a provider failure that Pi then
 * retried successfully.
 *
 * Unless a run has a new attempt behind it, a failed attempt is the outcome and
 * must stay visible. Once a later assistant message carries content, the failure
 * is history: leaving it in the transcript keeps reporting an outage that healed
 * on its own (QA-14 minor: a transient gateway 502).
 *
 * `answeredAt` is the index of the turn that answered. The attempts it retried
 * sit immediately before it — Pi starts a fresh agent run for the retry, so the
 * failed turns are neither trailing nor after the answer.
 */
function dropRecoveredFailures(turns: Turn[], answeredAt: number): Turn[] {
  const start = Math.min(answeredAt, turns.length);
  let from = start;
  while (from > 0) {
    const turn = turns[from - 1];
    if (turn.role !== "assistant" || !turn.error || turn.blocks.length > 0) break;
    from -= 1;
  }
  if (from >= start) return turns;
  return [...turns.slice(0, from), ...turns.slice(start)];
}

/** Reads the authoritative block list out of a message.final payload. */
function finalBlocks(payload: Payload): Block[] {
  const out: Block[] = [];
  if (Array.isArray(payload.blocks)) {
    payload.blocks.forEach((item, index) => {
      const record = asRecord(item);
      const rawKind = str(record.kind) ?? str(record.type) ?? "text";
      if (rawKind === "text" || rawKind === "thinking") {
        const text = str(record.text) ?? "";
        if (text) out.push({ kind: rawKind, text, index });
        return;
      }
      if (rawKind === "tool" || rawKind === "toolCall" || rawKind === "tool_call") {
        const call = asRecord(record.tool_call ?? record.toolCall);
        out.push({
          kind: "tool",
          id: str(call.id) ?? str(record.tool_call_id) ?? str(record.id) ?? `tool-${index}`,
          name: str(call.name) ?? str(record.name) ?? "tool",
          args: call.arguments ?? call.args ?? record.arguments ?? record.args,
          chunk: "",
          done: false,
          index,
        });
      }
    });
  }

  // Tolerated fallbacks for a final that carries only the joined strings.
  if (out.length === 0) {
    const thinking = str(payload.thinking);
    if (thinking) out.push({ kind: "thinking", text: thinking, index: 0 });
    const text = str(payload.text);
    if (text) out.push({ kind: "text", text, index: out.length });
  }
  return out;
}

// ---------------------------------------------------------------------------
// Event application
// ---------------------------------------------------------------------------

/**
 * True for a message Pi emitted on behalf of a tool rather than the model.
 *
 * A tool result is a `message` entry with role "toolResult". Rendering it as an
 * assistant turn printed every command's output a second time as prose — and after
 * a stop it printed "Command aborted" under the PiForge label, which is what made
 * an interrupt look like the model said it (QA-14). The tool trace already owns
 * that text.
 */
function isToolResult(role: string): boolean {
  return role === "toolResult" || role === "tool_result";
}

/**
 * True when a failure is the echo of a user stop.
 *
 * Real Pi reports a stop as stopReason "error" with "This operation was aborted",
 * so the wording is the only evidence left on the message once the control plane
 * has not stripped it. Kept narrow on purpose: "502: upstream error" must survive.
 */
function isAbortEcho(error?: string, stopReason?: string): boolean {
  if (stopReason === "aborted") return true;
  return Boolean(error) && /abort/i.test(error as string);
}

function applyEvent(state: SessionState, event: PfEvent): SessionState {
  const payload = asRecord(event.payload);

  switch (event.type) {
    case EV.runStarted: {
      return { ...state, running: true, streaming: true, error: null, aborted: false };
    }

    case EV.runAborted: {
      // A user stop: nothing failed, and the calls that were in flight were cut
      // short rather than reported. Marking them here is what keeps the tool
      // group neutral instead of a red "1 个失败" — and it is the only place a
      // tool left running by the abort gets a terminal state at all.
      return {
        ...state,
        aborted: true,
        turns: state.turns.map((turn) => {
          if (!turn.streaming) return turn;
          const blocks = turn.blocks.map((block) => {
            if (block.kind !== "tool") return block;
            if (block.isError) return { ...block, aborted: true };
            if (block.done) return block;
            return { ...block, done: true, aborted: true };
          });
          // The stop's own echo is not a failure. A genuine error from earlier in
          // the same run is kept: the user stopping does not un-fail it.
          const error = isAbortEcho(turn.error, turn.stopReason) ? undefined : turn.error;
          return { ...turn, blocks, error };
        }),
      };
    }

    case EV.runSettled: {
      // The ONLY place streaming turns off. `turn.ended` and `message.final`
      // both arrive before the agent has actually finished thinking about the
      // next turn, so treating them as completion makes the UI flicker idle.
      const settled = asRecord(event.payload);
      const stopped = settled.aborted === true;
      return {
        ...state,
        running: false,
        streaming: false,
        aborted: stopped ? true : state.aborted,
        settled: state.settled + 1,
        turns: state.turns.map((turn) => {
          if (!turn.streaming) return turn;
          const error = stopped && isAbortEcho(turn.error, turn.stopReason) ? undefined : turn.error;
          return { ...turn, streaming: false, error };
        }),
        error: stopped ? null : state.error,
      };
    }

    case EV.turnStarted:
      // A turn is a run container, not a message: creating a turn here would
      // render an empty assistant bubble before every message.started.
      return { ...state, running: true, streaming: true };

    case EV.turnEnded:
      // Intentionally does not touch `streaming`: a run can span several
      // turns, and run.settled is the completion signal (PRD §7.4).
      return state;

    case EV.msgStarted: {
      // A tool result proves nothing about what the model said: it is folded into
      // the tool trace instead of becoming a turn (see isToolResult).
      if (isToolResult(str(payload.role) ?? "")) return state;
      const id = messageIdOf(event, payload, state, false);
      const role: Turn["role"] = str(payload.role) === "user" ? "user" : "assistant";
      const existing = findTurn(state.turns, id);
      if (existing >= 0) {
        return {
          ...replaceTurn(state, existing, (turn) => ({
            ...turn,
            streaming: role === "assistant",
            entryId: event.entry_id ?? turn.entryId,
            error: undefined,
          })),
          activeMessageId: id,
        };
      }
      const turn = newTurn(id, role, event.ts, role === "assistant");
      turn.entryId = event.entry_id;
      return {
        ...state,
        turns: [...state.turns, turn],
        activeMessageId: id,
        streaming: role === "assistant" ? true : state.streaming,
        running: role === "assistant" ? true : state.running,
      };
    }

    case EV.msgDelta: {
      const kindRaw = str(payload.block) ?? str(payload.kind) ?? "text";
      // Tool-argument deltas are display-only and incomplete JSON; the
      // authoritative arguments arrive with tool.started.
      if (kindRaw === "toolArgs" || kindRaw === "tool_args") return state;

      const id = messageIdOf(event, payload, state, true);
      const kind: "text" | "thinking" = kindRaw === "thinking" ? "thinking" : "text";
      const contentIndex = num(payload.content_index) ?? num(payload.index) ?? 0;
      const delta = str(payload.delta) ?? "";
      if (!delta) return state;

      let turns = state.turns;
      let index = findTurn(turns, id);
      if (index < 0) {
        const created = newTurn(id, "assistant", event.ts, true);
        created.entryId = event.entry_id;
        turns = [...turns, created];
        index = turns.length - 1;
      }
      const base = { ...state, turns, activeMessageId: id, streaming: true, running: true };
      return replaceTurn(base, index, (turn) => ({
        ...turn,
        blocks: appendDelta(turn.blocks, contentIndex, kind, delta),
      }));
    }

    case EV.msgFinal: {
      // Tool output belongs to the tool trace, not to the conversation: rendering
      // it as an assistant message printed "Command aborted" under the PiForge
      // label after every stop (QA-14).
      if (isToolResult(str(payload.role) ?? "")) return state;
      const id = messageIdOf(event, payload, state, true);
      const role: Turn["role"] = str(payload.role) === "user" ? "user" : "assistant";
      const incoming = finalBlocks(payload);
      // The provider's failure travels with the final message; keep it on the
      // turn so the transcript can say what happened instead of drawing an
      // empty assistant bubble.
      const errorText = str(payload.error) || undefined;
      const modelRef = modelLabel(payload);
      // Pi retries a failed call by itself, and a retry that succeeds leaves the
      // earlier attempts behind as empty error turns. The recovery is the
      // outcome: keeping the 502 would report an outage that already healed.
      const recovered = role === "assistant" && !errorText;
      const index = findTurn(state.turns, id);
      if (index < 0) {
        const turns = recovered ? dropRecoveredFailures(state.turns, state.turns.length) : state.turns;
        return {
          ...state,
          activeMessageId: undefined,
          turns: [
            ...turns,
            {
              ...newTurn(id, role, event.ts, false),
              entryId: event.entry_id,
              blocks: incoming,
              error: errorText,
              model: modelRef,
              stopReason: str(payload.stop_reason),
            },
          ],
        };
      }
      const next = replaceTurn(state, index, (turn) => ({
        ...turn,
        role,
        entryId: event.entry_id ?? turn.entryId,
        // Replacement, never append: the final content is authoritative.
        blocks: mergeToolState(turn.blocks, incoming),
        error: errorText,
        model: modelLabel(payload) ?? turn.model,
        stopReason: str(payload.stop_reason) ?? turn.stopReason,
      }));
      return {
        ...next,
        turns: recovered ? dropRecoveredFailures(next.turns, index) : next.turns,
        activeMessageId: undefined,
      };
    }

    case EV.toolStarted: {
      const id = str(payload.tool_call_id) ?? `tool-${event.seq}`;
      // message.final already carries the tool call as a block, and it can
      // arrive before tool.started. Update that block instead of adding a
      // second row for the same invocation.
      const existing = findToolBlock(state, id);
      if (existing) {
        return replaceTurn(state, existing.turn, (turn) => {
          const blocks = turn.blocks.slice();
          const block = blocks[existing.block];
          if (block.kind === "tool") {
            blocks[existing.block] = {
              ...block,
              name: str(payload.name) ?? block.name,
              args: payload.args ?? block.args,
              done: false,
            };
          }
          return { ...turn, blocks };
        });
      }

      const block: Block = {
        kind: "tool",
        id,
        name: str(payload.name) ?? "tool",
        args: payload.args,
        chunk: "",
        done: false,
      };
      const tail = assistantTail(state, event.ts);
      return {
        ...replaceTurn(tail.state, tail.index, (turn) => ({
          ...turn,
          blocks: [...turn.blocks, block],
        })),
        running: true,
        streaming: true,
      };
    }

    case EV.toolProgress: {
      const id = str(payload.tool_call_id);
      const chunk = str(payload.chunk) ?? str(payload.text) ?? "";
      if (!id || !chunk) return state;
      return updateToolBlock(
        state,
        id,
        (block) => ({ ...block, chunk: mergeToolChunk(block.chunk, chunk) }),
        event.ts,
        payload,
      );
    }

    case EV.toolEnded: {
      const id = str(payload.tool_call_id);
      if (!id) return state;
      const preview = str(payload.result_preview) ?? str(payload.text);
      const duration = num(payload.duration_ms);
      return updateToolBlock(
        state,
        id,
        (block) => ({
          ...block,
          done: true,
          isError: payload.is_error === true,
          durationMs: duration && duration > 0 ? duration : undefined,
          // tool.ended may carry the full output; it wins over the streamed
          // fragments only when it actually says more.
          chunk: preview && preview.length > block.chunk.length ? preview : block.chunk,
        }),
        event.ts,
        payload,
      );
    }

    case EV.queueChanged: {
      const queue: QueuedPrompt[] = [
        ...stringList(payload.steering).map((text) => ({ text, behavior: "steer" as const })),
        ...stringList(payload.follow_up).map((text) => ({ text, behavior: "follow_up" as const })),
      ];
      return { ...state, queue };
    }

    case EV.compaction: {
      // The normalizer emits compaction twice (start/end). Only the end of the
      // compaction has the summary and token counts, so the start is ignored.
      const phase = str(payload.phase);
      if (phase === "start") return state;

      const compaction = event.payload as CompactionPayload | undefined;
      const marker: ContextMarker = {
        id: `compaction-${event.seq}`,
        seq: event.seq,
        ts: event.ts,
        reason: compaction?.reason ?? "manual",
        summary: compaction?.summary,
        tokensBefore: compaction?.tokens_before,
        tokensAfter: compaction?.estimated_tokens_after,
        firstKeptEntryId: compaction?.first_kept_entry_id,
      };
      return { ...state, markers: [...state.markers, marker] };
    }

    case "git.pushed":
    case "git.pulled": {
      const detail = str(payload.detail) ?? "";
      const note: NoteMarker = {
        id: `${event.type}-${event.seq}`,
        seq: event.seq,
        ts: event.ts,
        text: `${event.type === "git.pushed" ? "已推送" : "已拉取"}${detail ? ` ${detail}` : ""}`,
      };
      return { ...state, notes: [...state.notes, note] };
    }

    case EV.contextUsage: {
      // Two shapes: `{context_tokens, context_window, context_percent}` from
      // lib/types.ts and `{usage:{input,cacheRead,cacheWrite}, context_window,
      // percent}` from the normalizer. The prompt size is what fills the
      // window, so cached reads count towards it.
      const usageRecord = asRecord(payload.usage);
      const derived =
        (num(usageRecord.input) ?? 0) +
        (num(usageRecord.cacheRead) ?? 0) +
        (num(usageRecord.cacheWrite) ?? 0);
      const tokens = num(payload.context_tokens) ?? (derived > 0 ? derived : undefined);
      const window = num(payload.context_window);
      const percent =
        num(payload.context_percent) ??
        num(payload.percent) ??
        (tokens !== undefined && window ? (tokens / window) * 100 : undefined);
      return { ...state, usage: { tokens, window, percent } };
    }

    case EV.platformErr: {
      const message = str(payload.message) ?? str(payload.code) ?? "运行时错误";
      return { ...state, error: message };
    }

    default:
      return state;
  }
}

/** Locates a tool block by call id across every turn. */
function findToolBlock(
  state: SessionState,
  id: string,
): { turn: number; block: number } | null {
  for (let t = state.turns.length - 1; t >= 0; t -= 1) {
    const b = state.turns[t].blocks.findIndex(
      (block) => block.kind === "tool" && block.id === id,
    );
    if (b >= 0) return { turn: t, block: b };
  }
  return null;
}

/**
 * Applies a tool-block update to the turn that owns the call. A progress or
 * end event can arrive for a call whose start was dropped by the bridge, so a
 * missing block is opened from the payload rather than ignored.
 */
function updateToolBlock(
  state: SessionState,
  id: string,
  update: (block: Extract<Block, { kind: "tool" }>) => Block,
  ts: string,
  payload?: Payload,
): SessionState {
  const found = findToolBlock(state, id);
  if (found) {
    return replaceTurn(state, found.turn, (current) => {
      const blocks = current.blocks.slice();
      blocks[found.block] = update(blocks[found.block] as Extract<Block, { kind: "tool" }>);
      return { ...current, blocks };
    });
  }

  const tail = assistantTail(state, ts);
  const block = update({
    kind: "tool",
    id,
    name: str(payload?.name) ?? "tool",
    args: payload?.args,
    chunk: "",
    done: false,
  });
  return replaceTurn(tail.state, tail.index, (turn) => ({
    ...turn,
    blocks: [...turn.blocks, block],
  }));
}

// ---------------------------------------------------------------------------
// Reducer entry points
// ---------------------------------------------------------------------------

/**
 * Folds one event into the state.
 *
 * `seq` is monotonic per session: duplicates and out-of-order replays are
 * dropped, and a gap marks the stream for repair instead of silently leaving a
 * hole in the transcript.
 */
export function reduceEvent(state: SessionState, event: PfEvent): SessionState {
  if (!Number.isFinite(event.seq)) return state;
  if (event.seq <= state.lastSeq) return state;

  const gap = state.lastSeq > 0 && event.seq > state.lastSeq + 1;
  const base: SessionState = {
    ...state,
    lastSeq: event.seq,
    needsResync: state.needsResync || gap,
  };
  return applyEvent(base, event);
}

export type SessionAction =
  | { type: "event"; event: PfEvent }
  | { type: "reset" }
  | { type: "clear-queue" }
  | { type: "clear-error" };

export function sessionReducer(state: SessionState, action: SessionAction): SessionState {
  switch (action.type) {
    case "event":
      return reduceEvent(state, action.event);
    case "reset":
      return { ...initialSessionState };
    case "clear-queue":
      return { ...state, queue: [] };
    case "clear-error":
      return { ...state, error: null };
    default:
      return state;
  }
}

// ---------------------------------------------------------------------------
// Selectors
// ---------------------------------------------------------------------------

export type TimelineItem =
  | { kind: "turn"; key: string; ts: string; turn: Turn }
  | { kind: "marker"; key: string; ts: string; marker: ContextMarker }
  | { kind: "note"; key: string; ts: string; text: string };

/**
 * Interleaves compaction markers with turns. Turn order is the arrival order
 * (authoritative), while markers are placed before the first turn that was
 * created after them.
 */
export function selectTimeline(state: SessionState): TimelineItem[] {
  const items: TimelineItem[] = [];
  const pending = state.markers.slice();
  // Notes interleave with turns on their timestamp, which is all either kind has in
  // common. They are merged here rather than in a second pass so the order a person
  // reads matches the order things happened.
  const pendingNotes = state.notes.slice();
  const take = (limit: number) => {
    while (pending.length > 0) {
      const ts = Date.parse(pending[0].ts);
      if (Number.isFinite(ts) && ts <= limit) {
        const marker = pending.shift() as ContextMarker;
        items.push({ kind: "marker", key: marker.id, ts: marker.ts, marker });
        continue;
      }
      break;
    }
  };

  for (const turn of state.turns) {
    const ts = Date.parse(turn.ts);
    take(Number.isFinite(ts) ? ts : Number.POSITIVE_INFINITY);
    const turnTs = Number.isFinite(ts) ? ts : Number.POSITIVE_INFINITY;
    while (pendingNotes.length > 0) {
      const noteTs = Date.parse(pendingNotes[0].ts);
      if (Number.isFinite(noteTs) && noteTs <= turnTs) {
        const note = pendingNotes.shift() as NoteMarker;
        items.push({ kind: "note", key: note.id, ts: note.ts, text: note.text });
        continue;
      }
      break;
    }
    // Pi retries a failed provider call on its own, and every attempt lands as
    // its own empty assistant message. Four identical boxes say less than one
    // that admits it kept trying, so consecutive failures of the same kind
    // collapse into the first of them.
    const last = items[items.length - 1];
    if (
      turn.role === "assistant" &&
      turn.error &&
      turn.blocks.length === 0 &&
      last?.kind === "turn" &&
      last.turn.role === "assistant" &&
      last.turn.error === turn.error &&
      last.turn.blocks.length === 0
    ) {
      last.turn = { ...last.turn, retries: (last.turn.retries ?? 0) + 1 };
      continue;
    }
    items.push({ kind: "turn", key: turn.id, ts: turn.ts, turn });
  }
  take(Number.POSITIVE_INFINITY);
  for (const note of pendingNotes) {
    items.push({ kind: "note", key: note.id, ts: note.ts, text: note.text });
  }
  return items;
}

/** Tool calls that are still running, for the "正在执行" header summary. */
export function runningToolCount(state: SessionState): number {
  let count = 0;
  for (const turn of state.turns) {
    for (const block of turn.blocks) {
      if (block.kind === "tool" && !block.done) count += 1;
    }
  }
  return count;
}
/**
 * modelLabel renders what produced a message.
 *
 * Pi talks to our gateway, so the provider it reports is always the gateway's own
 * name (`piforge`) whichever upstream account actually served the call. Repeating
 * that to the user contradicts the composer, which names their provider, so the
 * gateway's name is dropped and only the model is shown.
 */
function modelLabel(payload: Record<string, unknown>): string | undefined {
  const model = str(payload.model);
  const provider = str(payload.provider);
  if (!model) return undefined;
  if (!provider || provider === "piforge") return model;
  return `${provider}/${model}`;
}
/**
 * Folds one tool-output event into the block's accumulated text.
 *
 * `tool_execution_update` carries the output *so far* (Pi's `partialResult`), so
 * a payload that already starts with what we hold is a replacement, not an
 * increment: appending it duplicated every line the follow view and the trace
 * had already shown. A peer that sends only the new bytes (and our fake agent's
 * markers do) must still work, so a non-prefix payload is appended.
 */
export function mergeToolChunk(existing: string, incoming: string): string {
  if (!existing) return incoming;
  if (incoming === existing) return existing;
  if (incoming.startsWith(existing)) return incoming;
  return existing + incoming;
}

/**
 * The agent's bash output as plain text, for the follow view (FR-9.2).
 *
 * Only `bash` tool blocks contribute: this is a mirror of the commands the agent
 * actually ran, not a second terminal that would invent a shell of its own. Text
 * keeps its order across turns, and a reconnecting client re-derives the same
 * text from the timeline it already has.
 */
export function collectBashOutput(items: TimelineItem[]): string {
  let text = "";
  for (const item of items) {
    if (item.kind !== "turn") continue;
    for (const block of item.turn.blocks) {
      if (block.kind !== "tool") continue;
      if (block.name !== "bash" && block.name !== "Bash") continue;
      if (!block.chunk) continue;
      // Separate two commands that did not end with a newline, so their output
      // cannot run together into one unreadable line.
      if (text && !text.endsWith("\n")) text += "\n";
      text += block.chunk.endsWith("\n") ? block.chunk : block.chunk + "\n";
    }
  }
  return text;
}
