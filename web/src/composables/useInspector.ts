import { onBeforeUnmount, onMounted, ref } from "vue";
import { apiCancel, apiChat, apiMetrics, apiSession } from "../api";
import { apiSessionLoad } from "../sessionApi";
import { t } from "../i18n";
import type { ChatMessage, ContextSnapshot, MetricsInfo, RuntimeEvent, SessionInfo } from "../types";

function msgId(): string {
  return Math.random().toString(36).slice(2, 10);
}

function eventClass(type: string): string {
  if (type.startsWith("tool.batch")) return "batch";
  if (type.startsWith("tool.")) return "tool";
  if (type.startsWith("llm.stream")) return "stream";
  if (type.startsWith("llm.")) return "llm";
  if (type.startsWith("skill.")) return "skill";
  if (type.startsWith("plan.")) return "plan";
  if (type.startsWith("permission.")) return "perm";
  if (type.includes("failed") || type.includes("denied") || type.includes("loop")) return "err";
  return "";
}

/** Full event type for Timeline labels (no A./C./L. abbreviations). */
function shortType(type: string): string {
  return type;
}

function compact(v: unknown): string {
  if (v == null) return "";
  if (typeof v === "string") {
    try {
      return JSON.stringify(JSON.parse(v));
    } catch {
      return v.slice(0, 80);
    }
  }
  return JSON.stringify(v);
}

function previewData(data?: Record<string, unknown>): string {
  if (!data) return "";
  if (data.name != null && typeof data.name === "string") {
    return data.name;
  }
  if (data.operation != null && data.path != null) {
    return `${data.operation} ${data.path}`;
  }
  if (data.domain != null && data.action != null) {
    return `${data.domain} ${data.action} ${data.target || ""} ${data.policy || ""}`;
  }
  if (data.stage != null && data.message_count != null) {
    return `stage=${data.stage} msgs=${data.message_count} tools=${data.tool_count ?? 0} est=${data.estimated_prompt_tokens ?? "—"}`;
  }
  if (data.estimated_prompt_tokens != null && data.input_tokens != null) {
    return `est=${data.estimated_prompt_tokens} provider=${data.input_tokens} Δ=${data.prompt_token_delta ?? "?"}`;
  }
  if (data.rel_path != null) {
    return `${data.rel_path}${data.bytes != null ? ` · ${data.bytes}B` : ""}`;
  }
  if (data.count != null && (data.preview_tail || data.total_len != null)) {
    const n = Number(data.count) || 1;
    const tail = String(data.preview_tail || data.text || "").slice(0, 36);
    const len = data.total_len != null ? ` · ${data.total_len}B` : "";
    return `${n > 1 ? `x${n}` : ""} ${tail}${len}`.trim();
  }
  if (data.text != null && data.total_len != null) {
    return String(data.text).slice(0, 40);
  }
  if (data.tool) {
    const args = data.arguments ? " " + compact(data.arguments) : "";
    return String(data.tool) + args;
  }
  if (data.summary) return String(data.summary);
  if (data.provider && data.model) return `${data.provider} ${data.model}`;
  if (data.total_tokens != null) return `tokens≈${data.total_tokens}`;
  if (data.to) return `→ ${data.to}`;
  if (data.error) return String(data.error);
  return "";
}

function fmtTime(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return String(iso);
  return (
    d.toLocaleTimeString(undefined, { hour12: false }) +
    "." +
    String(d.getMilliseconds()).padStart(3, "0")
  );
}

export function useInspector() {
  const messages = ref<ChatMessage[]>([]);
  const events = ref<RuntimeEvent[]>([]);
  const session = ref<SessionInfo | null>(null);
  const metrics = ref<MetricsInfo>({});
  const snapshot = ref<ContextSnapshot | null>(null);
  const input = ref("");
  const connected = ref(false);

  const lastErrorShown = ref("");
  let es: EventSource | null = null;
  let pollTimer: number | null = null;
  let refreshTimer: number | null = null;
  let streamMsgId: string | null = null;
  let streamFlush: number | null = null;
  let streamPending = "";
  /** True once this turn's final answer has been written into chat. */
  let turnStreamClosed = false;
  /** Last session turn.id whose final answer was written into the chat. */
  let lastFinalTurnId = -1;
  /** Text of the last assistant bubble written for final/stream sync. */
  let lastAssistantText = "";
  /** Coalesce SSE event handling to avoid freezing the UI. */
  let sseQueue: RuntimeEvent[] = [];
  let sseTimer: number | null = null;
  let sessionRefreshAt = 0;
  let metricsRefreshAt = 0;

  function nowISO(): string {
    return new Date().toISOString();
  }

  function fmtClock(iso?: string): string {
    return fmtTime(iso);
  }

  function addMessage(role: ChatMessage["role"], text: string) {
    messages.value.push({ id: msgId(), role, text, ts: nowISO() });
    if (role === "assistant") lastAssistantText = text;
  }

  function addSystemOnce(text: string) {
    if (lastErrorShown.value === text) return;
    if (text.startsWith("error:") || text.startsWith("cancelled") || text.startsWith("cancel ")) {
      lastErrorShown.value = text;
    }
    addMessage("system", text);
  }

  function clearPollTimer() {
    if (pollTimer != null) {
      window.clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  /**
   * Insert or refine the assistant answer once per completed turn.
   * Collapses any stream fragment bubbles from this turn into a single
   * final bubble so a coalesced tail + session poll cannot duplicate text.
   */
  function appendAssistantOnce(final: string, turnId?: number) {
    if (!final) return;
    const fin = final.trim();
    if (!fin) return;
    const tid = turnId ?? 0;
    if (tid && tid === lastFinalTurnId && fin === lastAssistantText) return;

    let lastUser = -1;
    for (let i = messages.value.length - 1; i >= 0; i--) {
      if (messages.value[i].role === "user") {
        lastUser = i;
        break;
      }
    }
    const assistIdx: number[] = [];
    for (let i = lastUser + 1; i < messages.value.length; i++) {
      if (messages.value[i].role === "assistant") assistIdx.push(i);
    }

    if (assistIdx.length === 0) {
      addMessage("assistant", fin);
    } else {
      // One turn → one assistant answer. Stream fragments are display
      // artifacts of the same final content.
      const first = messages.value[assistIdx[0]];
      first.text = fin;
      for (let k = assistIdx.length - 1; k >= 1; k--) {
        messages.value.splice(assistIdx[k], 1);
      }
      lastAssistantText = fin;
    }

    streamMsgId = null;
    streamPending = "";
    if (streamFlush != null) {
      window.clearTimeout(streamFlush);
      streamFlush = null;
    }
    turnStreamClosed = true;
    if (tid) lastFinalTurnId = tid;
  }

  function flushStreamDelta() {
    if (!streamPending) return;
    if (turnStreamClosed) {
      streamPending = "";
      return;
    }
    const piece = streamPending;
    streamPending = "";
    if (!streamMsgId) {
      const id = msgId();
      streamMsgId = id;
      messages.value.push({ id, role: "assistant", text: piece, ts: nowISO() });
      lastAssistantText = piece;
    } else {
      const m = messages.value.find((x) => x.id === streamMsgId);
      if (m) {
        m.text += piece;
        lastAssistantText = m.text;
      } else {
        // Bubble was collapsed/removed — start a fresh fragment.
        const id = msgId();
        streamMsgId = id;
        messages.value.push({ id, role: "assistant", text: piece, ts: nowISO() });
        lastAssistantText = piece;
      }
    }
  }

  function scheduleStreamFlush() {
    if (streamFlush != null) return;
    streamFlush = window.setTimeout(() => {
      streamFlush = null;
      flushStreamDelta();
    }, 40);
  }

  function applySession(s: SessionInfo) {
    session.value = s;
    // Always replace snapshot (including null) so a session switch
    // cannot leave the previous turn's context on screen.
    snapshot.value = s.snapshot ?? null;
    // Only sync chat text when idle; mid-run finals are stale by design.
    if (!s.running && s.turn?.final) {
      appendAssistantOnce(s.turn.final, s.turn.id);
    }
    if (s.last_error) addSystemOnce("error: " + s.last_error);
    if (!s.running) clearPollTimer();
  }

  function pushEvent(evt: RuntimeEvent) {
    // Collapse consecutive stream deltas in the timeline: "L.stream.delta (x10)"
    if (evt.type === "llm.stream_delta") {
      const last = events.value[events.value.length - 1];
      const text = typeof evt.data?.text === "string" ? evt.data.text : "";
      if (last && last.type === "llm.stream_delta") {
        const count = Number(last.data?.count || 1) + 1;
        last.data = {
          ...(last.data || {}),
          ...(evt.data || {}),
          count,
          // Keep a short preview of recent fragments only.
          text: String(last.data?.preview_tail || "") + text,
          preview_tail: (String(last.data?.preview_tail || "") + text).slice(-40),
          time: last.time,
        };
      } else {
        events.value.push({
          ...evt,
          data: { ...(evt.data || {}), count: 1, preview_tail: text.slice(-40), text },
        });
      }
      // Stream bubble updates use the same delta stream.
      // Drop fragments that arrive after the turn's final was applied —
      // they must not reopen or append to a closed answer.
      if (text && !turnStreamClosed) {
        streamPending += text;
        scheduleStreamFlush();
      }
      return;
    }

    events.value.push(evt);
    if (events.value.length > 300) events.value.splice(0, events.value.length - 300);

    if (evt.type === "llm.request_finished" || evt.type === "llm.request_failed") {
      // Flush what we have, but keep streamMsgId: the eventHub may still
      // deliver a coalesced delta batch that was in flight. Cleared only
      // when the turn final is applied or a new user message starts.
      flushStreamDelta();
    }

    if (evt.type === "agent.state_changed") {
      const to = String(evt.data?.to || "");
      if (to === "CANCELLED") {
        flushStreamDelta();
        addSystemOnce("cancelled");
        clearPollTimer();
      }
      if (to === "FINISHED" || to === "FAILED") {
        flushStreamDelta();
        clearPollTimer();
      }
      void refreshSessionThrottled();
      void refreshMetricsThrottled();
    }
    if (evt.type === "llm.request_finished" || evt.type === "context.built") {
      void refreshSessionThrottled();
      void refreshMetricsThrottled();
    }
    if (evt.type === "tool.batch_started") {
      const tools = evt.data?.tools;
      const size = evt.data?.size;
      if (Array.isArray(tools)) {
        addSystemOnce(`${t("chat.batch")} (${size}): ${tools.join(", ")}`);
      }
    }
  }

  async function refreshSession() {
    try {
      applySession(await apiSession());
    } catch {
      /* server may be down */
    }
  }

  async function refreshMetrics() {
    try {
      metrics.value = await apiMetrics();
    } catch {
      /* ignore */
    }
  }

  function refreshSessionThrottled() {
    const now = Date.now();
    if (now - sessionRefreshAt < 800) return;
    sessionRefreshAt = now;
    void refreshSession();
  }

  function refreshMetricsThrottled() {
    const now = Date.now();
    if (now - metricsRefreshAt < 800) return;
    metricsRefreshAt = now;
    void refreshMetrics();
  }

  function flushSseQueue() {
    sseTimer = null;
    const batch = sseQueue;
    sseQueue = [];
    for (const evt of batch) {
      pushEvent(evt);
    }
  }

  function enqueueSse(evt: RuntimeEvent) {
    sseQueue.push(evt);
    // Keep queue bounded if the tab is backgrounded.
    if (sseQueue.length > 200) {
      sseQueue = sseQueue.slice(-200);
    }
    if (sseTimer == null) {
      sseTimer = window.setTimeout(flushSseQueue, 80);
    }
  }

  function connectSSE() {
    if (es) es.close();
    es = new EventSource("/api/events");
    es.addEventListener("hello", () => {
      connected.value = true;
      addSystemOnce(t("chat.sse"));
    });
    es.addEventListener("runtime", (ev) => {
      try {
        enqueueSse(JSON.parse((ev as MessageEvent).data) as RuntimeEvent);
      } catch {
        /* skip malformed */
      }
    });
    es.onerror = () => {
      connected.value = false;
    };
  }

  async function send() {
    const message = input.value.trim();
    if (!message) return;
    lastErrorShown.value = "";
    input.value = "";
    // New turn: reset stream buffers so the next answer gets a fresh bubble.
    streamMsgId = null;
    streamPending = "";
    turnStreamClosed = false;
    if (streamFlush != null) {
      window.clearTimeout(streamFlush);
      streamFlush = null;
    }
    addMessage("user", message);
    await apiChat(message);
    session.value = {
      ...(session.value || {
        session_id: "",
        workspace: "",
        provider: "",
        model: "",
        state: "BUILDING_CONTEXT",
        running: true,
        turn: { final: "" },
      }),
      state: "BUILDING_CONTEXT",
      running: true,
      turn: { ...(session.value?.turn || {}), final: "" },
    };
    clearPollTimer();
    pollTimer = window.setInterval(() => {
      void refreshSessionThrottled();
    }, 600);
  }

  async function cancel() {
    try {
      const r = await apiCancel();
      lastErrorShown.value = "";
      addSystemOnce(r.cancelled ? t("chat.cancelReq") : t("chat.nothingCancel"));
    } catch (err) {
      addSystemOnce(String((err as Error).message || err));
    }
  }

  /** Replace chat bubbles with a restored session's user/assistant turns. */
  async function loadSession(id: string) {
    lastErrorShown.value = "";
    streamMsgId = null;
    streamPending = "";
    turnStreamClosed = false;
    lastAssistantText = "";
    lastFinalTurnId = -1;
    const data = await apiSessionLoad(id);
    messages.value = [];
    events.value = [];
    snapshot.value = null;
    metrics.value = {};
    for (const m of data.messages || []) {
      const role = (m.role === "user" || m.role === "assistant" || m.role === "system")
        ? m.role
        : "system";
      addMessage(role, m.content);
    }
    addSystemOnce(`${t("chat.restored")} ${data.id} · ${data.entries ?? data.messages.length} entries`);
    session.value = {
      ...(session.value || {
        session_id: "",
        workspace: "",
        provider: "",
        model: "",
        state: "IDLE",
        running: false,
      }),
      state: "IDLE",
      running: false,
      last_error: "",
      turn: { final: "" },
    };
    void refreshSession();
  }

  function boot() {
    connectSSE();
    void refreshSession().catch(() => addMessage("system", t("chat.connectFail")));
    void refreshMetrics();
    refreshTimer = window.setInterval(() => {
      void refreshSessionThrottled();
      void refreshMetricsThrottled();
    }, 3000);
  }

  onMounted(boot);
  onBeforeUnmount(() => {
    es?.close();
    clearPollTimer();
    if (refreshTimer != null) window.clearInterval(refreshTimer);
    if (streamFlush != null) window.clearTimeout(streamFlush);
  });

  return {
    messages,
    events,
    session,
    metrics,
    snapshot,
    input,
    connected,
    send,
    cancel,
    loadSession,
    eventClass,
    shortType,
    previewData,
    fmtTime,
    fmtClock,
  };
}
