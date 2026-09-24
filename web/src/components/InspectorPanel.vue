<script setup lang="ts">
import { computed, ref } from "vue";
import { apiTrace, apiTraces, type TraceListItem } from "../traceApi";
import { useI18n } from "../i18n";
import type { ContextItem, ContextSnapshot, MetricsInfo, RuntimeEvent } from "../types";

const { t } = useI18n();

const props = defineProps<{
  metrics: MetricsInfo;
  snapshot: ContextSnapshot | null;
  events: RuntimeEvent[];
  timeFmt: (iso?: string) => string;
  eventClass: (type: string) => string;
  shortType: (type: string) => string;
  previewData: (data?: Record<string, unknown>) => string;
}>();

const emit = defineEmits<{
  deeplink: [
    type: string,
    data?: Record<string, unknown>,
    sourceEvents?: RuntimeEvent[],
  ];
  replay: [e: RuntimeEvent, sourceEvents?: RuntimeEvent[]];
}>();

function isDeeplink(e: RuntimeEvent): boolean {
  const t = typeof e.type === "string" ? e.type : "";
  return (
    t.startsWith("skill.") ||
    t.startsWith("plan.") ||
    t.startsWith("permission.") ||
    t === "instruction.loaded" ||
    t === "file.changed" ||
    t === "tool.started" ||
    t === "tool.finished" ||
    t === "tool.failed" ||
    t === "tool.requested" ||
    t === "llm.wire_request" ||
    t === "llm.request_finished" ||
    t === "llm.request_started"
  );
}

function deeplinkTitle(e: RuntimeEvent): string | undefined {
  if (!isDeeplink(e)) return undefined;
  if (e.type.startsWith("permission.")) return "permission history";
  if (e.type === "instruction.loaded") return "Instructions";
  if (e.type === "file.changed") return "Diff / path";
  if (e.type.startsWith("tool.")) return "tool call detail (args / result)";
  if (
    e.type === "llm.wire_request" ||
    e.type === "llm.request_finished" ||
    e.type === "llm.request_started"
  ) {
    return "wire request view";
  }
  return "Skill / Plan";
}

function onTimelineClick(e: RuntimeEvent) {
  if (!isDeeplink(e)) return;
  const d = e.data || {};
  const rowKey = [
    e.id || "",
    e.type || "",
    e.time || "",
    String(d.id || ""),
    String(d.tool || ""),
  ].join("|");
  const payload = {
    ...(e.data || {}),
    time: e.time,
    id: d.id != null ? String(d.id) : e.id || "",
    tool: d.tool != null ? String(d.tool) : "",
    __event_id: e.id,
    __type: e.type,
    __row_key: rowKey,
  } as Record<string, unknown>;
  emit("deeplink", e.type, payload, displayEvents.value);
}

/** T-obs-5: pin Inspector to the historical state at this event. */
function onReplayHere(e: RuntimeEvent) {
  emit("replay", e, displayEvents.value);
}

type ItemRow = {
  index: number;
  source: string;
  role?: string;
  /** Collapsed one-line preview shown by default. */
  preview: string;
  /** Full preview content from the snapshot (may be multi-line). */
  fullPreview: string;
  hasMore: boolean;
  tok: number;
  included: boolean;
  excluded: boolean;
  truncated: boolean;
  pinned: boolean;
  reason?: string;
  policy?: string;
  saved?: number;
  prov?: ContextItem["prov"];
};

type RunRow = {
  id: string;
  source: string;
  count: number;
  tok: number;
  start: number; // 0-based index in snapshot.items
  end: number;
  excluded: boolean;
  items: ItemRow[];
  pct: number;
};

const expanded = ref<Set<string>>(new Set());
/** Keys of individual context items whose full preview is open. */
const expandedItems = ref<Set<string>>(new Set());
/** Section collapse + split height (Context vs Timeline). */
const ctxOpen = ref(true);
const tlOpen = ref(true);
const ctxSplit = ref(340);
let splitDragY = 0;
let splitDragH = 0;

function startSplitDrag(ev: MouseEvent) {
  splitDragY = ev.clientY;
  splitDragH = ctxSplit.value;
  const onMove = (e: MouseEvent) => {
    const next = splitDragH + (e.clientY - splitDragY);
    ctxSplit.value = Math.min(Math.max(120, next), Math.max(240, window.innerHeight - 260));
  };
  const onUp = () => {
    window.removeEventListener("mousemove", onMove);
    window.removeEventListener("mouseup", onUp);
  };
  window.addEventListener("mousemove", onMove);
  window.addEventListener("mouseup", onUp);
}

function truncatePreview(s: string, n = 72): string {
  const t = (s || "").replace(/\s+/g, " ").trim();
  if (t.length <= n) return t || "—";
  return t.slice(0, n) + "…";
}

function toItemRow(i: ContextItem, index: number): ItemRow {
  const fullPreview = i.preview || "";
  const collapsed = truncatePreview(fullPreview);
  const flat = fullPreview.replace(/\s+/g, " ").trim();
  return {
    index,
    source: i.source || "unknown",
    role: i.role,
    preview: collapsed,
    fullPreview,
    hasMore: flat.length > 72 || fullPreview.includes("\n"),
    tok: i.token_count || 0,
    included: !!i.included,
    excluded: !!i.excluded,
    truncated: !!i.truncated,
    pinned: !!i.pinned,
    reason: i.reason,
    policy: i.policy,
    saved: i.saved_tokens,
    prov: i.prov,
  };
}

/**
 * Run-length fold: merge only consecutive items with the same source.
 * Preserves original context order (history/tool_result may interleave).
 */
const ctxRuns = computed<RunRow[]>(() => {
  const items = props.snapshot?.items || [];
  const runs: Omit<RunRow, "pct">[] = [];
  for (let idx = 0; idx < items.length; idx++) {
    const row = toItemRow(items[idx], idx);
    const last = runs[runs.length - 1];
    if (last && last.source === row.source) {
      last.items.push(row);
      last.tok += row.tok;
      last.count += 1;
      last.end = idx;
      if (!row.excluded) last.excluded = false;
    } else {
      runs.push({
        id: `run-${idx}`,
        source: row.source,
        count: 1,
        tok: row.tok,
        start: idx,
        end: idx,
        excluded: row.excluded,
        items: [row],
      });
    }
  }
  const maxTok = Math.max(1, ...runs.map((r) => r.tok));
  return runs.map((r) => ({
    ...r,
    pct: Math.max(2, Math.round((r.tok / maxTok) * 100)),
  }));
});

function toggleRun(id: string) {
  const next = new Set(expanded.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  expanded.value = next;
  if (!next.has(id)) {
    // Collapse child item previews when the run itself collapses.
    const items = new Set(expandedItems.value);
    let changed = false;
    for (const key of items) {
      if (key.startsWith(id + ":")) {
        items.delete(key);
        changed = true;
      }
    }
    if (changed) expandedItems.value = items;
  }
}

function toggleAll() {
  if (expanded.value.size > 0) {
    expanded.value = new Set();
    expandedItems.value = new Set();
    return;
  }
  expanded.value = new Set(ctxRuns.value.map((r) => r.id));
}

function itemKey(runId: string, index: number): string {
  return `${runId}:${index}`;
}

function isItemOpen(runId: string, index: number): boolean {
  return expandedItems.value.has(itemKey(runId, index));
}

function toggleItem(runId: string, index: number) {
  const key = itemKey(runId, index);
  const next = new Set(expandedItems.value);
  if (next.has(key)) next.delete(key);
  else next.add(key);
  expandedItems.value = next;
}

const ctxTotal = computed(() => {
  const s = props.snapshot;
  if (!s) return 0;
  return (s.total_tokens || 0) + (s.tool_tokens || 0);
});

/** Non-kept rows from the snapshot diff for display. */
const diffChanges = computed(() => {
  const entries = props.snapshot?.diff?.entries || [];
  return entries.filter((e) => e.change && e.change !== "kept");
});

/** Runtime decision traces from live/history events (T-obs-4). */
const decisionRows = computed(() => {
  const out: Array<{
    domain: string;
    action: string;
    target?: string;
    policy?: string;
    reason: string;
    evidence?: string[];
  }> = [];
  for (const e of displayEvents.value) {
    if (e.type !== "decision.recorded") continue;
    const d = e.data || {};
    out.push({
      domain: String(d.domain || ""),
      action: String(d.action || ""),
      target: d.target != null ? String(d.target) : undefined,
      policy: d.policy != null ? String(d.policy) : undefined,
      reason: String(d.reason || ""),
      evidence: Array.isArray(d.evidence) ? (d.evidence as string[]) : undefined,
    });
  }
  return out.slice(-40);
});

// --- Timeline: live SSE events vs historical trace JSONL ---
const tlMode = ref<"live" | "history">("live");
const showRawJson = ref(false);
const traces = ref<TraceListItem[]>([]);
const traceId = ref("");
const typeFilter = ref("");
const histEvents = ref<RuntimeEvent[]>([]);
const histMeta = ref({ total: 0, shown: 0, path: "", dir: "" });
const tlError = ref("");
const tlLoading = ref(false);
const tlDir = ref("");
const tlDirs = ref<string[]>([]);

const displayEvents = computed(() =>
  tlMode.value === "live" ? props.events : histEvents.value,
);

/** Merged tool row inside a parallel batch. */
type BatchToolRow = {
  tool: string;
  args: string;
  ok: boolean;
  ms: number;
  bytes: number;
  error: string;
  status: string;
  /** Best raw event for detail deep-link (prefer tool.finished). */
  event?: RuntimeEvent;
};

type TimelineRow =
  | { kind: "event"; e: RuntimeEvent }
  | {
      kind: "batch";
      id: string;
      time?: string;
      tools: string[];
      rows: BatchToolRow[];
      ms: number;
      succeeded: number;
      failed: number;
      maxWorkers: number;
      raw: RuntimeEvent[];
    };

function compactArg(s?: string): string {
  if (!s) return "";
  try {
    return JSON.stringify(JSON.parse(s));
  } catch {
    return String(s).slice(0, 72);
  }
}

function shortErr(s?: string, n = 72): string {
  const t = (s || "").replace(/\s+/g, " ").trim();
  if (t.length <= n) return t;
  return t.slice(0, n) + "…";
}

function mergeBatchTools(tools: RuntimeEvent[]): BatchToolRow[] {
  const map = new Map<string, BatchToolRow>();
  const order: string[] = [];
  for (const e of tools) {
    const d = e.data || {};
    const key = String(d.call_id || d.tool || e.id || order.length);
    let row = map.get(key);
    if (!row) {
      row = {
        tool: String(d.tool || ""),
        args: compactArg(typeof d.arguments === "string" ? d.arguments : ""),
        ok: true,
        ms: 0,
        bytes: 0,
        error: "",
        status: e.type,
        event: e,
      };
      map.set(key, row);
      order.push(key);
    }
    if (d.tool) row.tool = String(d.tool);
    if (typeof d.arguments === "string") {
      const c = compactArg(d.arguments);
      if (c) row.args = c;
    }
    if (e.type === "tool.finished" || e.type === "tool.failed") {
      row.event = e;
    } else if (!row.event || row.event.type === "tool.started") {
      row.event = e;
    }
    if (e.type === "tool.finished") {
      row.status = "finished";
      row.ms = Number(d.duration_ms || 0);
      row.bytes = Number(d.result_size || 0);
      row.ok = !d.is_error;
      if (d.error) row.error = String(d.error);
    } else if (e.type === "tool.failed") {
      row.status = "failed";
      row.ok = false;
      row.ms = Number(d.duration_ms || 0);
      row.error = String(d.error || "");
    } else {
      row.status = e.type;
    }
  }
  return order.map((k) => map.get(k)!);
}

/** Fold tool.batch_* + member tool events into a visual batch block. */
const timelineRows = computed<TimelineRow[]>(() => {
  const events = displayEvents.value;
  const out: TimelineRow[] = [];
  let i = 0;
  while (i < events.length) {
    const e = events[i];
    if (e.type !== "tool.batch_started") {
      out.push({ kind: "event", e });
      i++;
      continue;
    }
    const tools: RuntimeEvent[] = [];
    let j = i + 1;
    let finish: RuntimeEvent | undefined;
    while (j < events.length) {
      const t = events[j];
      if (t.type === "tool.batch_finished" || t.type === "tool.batch_started") break;
      if (
        t.type === "tool.started" ||
        t.type === "tool.finished" ||
        t.type === "tool.failed" ||
        t.type === "permission.requested" ||
        t.type === "permission.approved" ||
        t.type === "permission.denied"
      ) {
        tools.push(t);
      }
      j++;
    }
    if (j < events.length && events[j].type === "tool.batch_finished") {
      finish = events[j];
      j++;
    }
    const startData = e.data || {};
    const finData = (finish && finish.data) || {};
    const named =
      Array.isArray(startData.tools) && startData.tools.length
        ? (startData.tools as unknown[]).map(String)
        : mergeBatchTools(tools).map((r) => r.tool || "?");
    out.push({
      kind: "batch",
      id: String(e.id || startData.call_ids || `batch-${i}`),
      time: e.time,
      tools: named,
      rows: mergeBatchTools(tools.filter((t) => t.type.startsWith("tool."))),
      ms: Number(finData.duration_ms || 0),
      succeeded: Number(finData.succeeded || 0),
      failed: Number(finData.failed || 0),
      maxWorkers: Number(
        finData.max_workers || startData.max_workers || named.length,
      ),
      raw: finish ? [e, ...tools, finish] : [e, ...tools],
    });
    i = j;
  }
  return out;
});

async function loadTraceList() {
  tlError.value = "";
  try {
    const data = await apiTraces();
    traces.value = data.traces || [];
    tlDir.value = data.dir || "";
    tlDirs.value = data.dirs || (data.dir ? [data.dir] : []);
  } catch (e) {
    tlError.value = String((e as Error).message || e);
    traces.value = [];
    tlDirs.value = [];
  }
}

async function loadTrace() {
  if (!traceId.value) return;
  tlLoading.value = true;
  tlError.value = "";
  try {
    const data = await apiTrace(traceId.value, {
      type: typeFilter.value || undefined,
      limit: 500,
    });
    histEvents.value = data.events || [];
    histMeta.value = {
      total: data.total,
      shown: data.shown,
      path: data.path,
      dir: tlDir.value,
    };
  } catch (e) {
    tlError.value = String((e as Error).message || e);
    histEvents.value = [];
  } finally {
    tlLoading.value = false;
  }
}

async function enterHistory() {
  tlMode.value = "history";
  histEvents.value = [];
  await loadTraceList();
  const pick =
    traces.value.find((t) => t.is_current) ||
    traces.value.find((t) => (t.size || 0) > 0) ||
    traces.value[0];
  if (!pick) {
    histEvents.value = [];
    return;
  }
  traceId.value = pick.id;
  await loadTrace();
}

function enterLive() {
  tlMode.value = "live";
  tlError.value = "";
}

function rawJson(e: RuntimeEvent) {
  try {
    return JSON.stringify(e);
  } catch {
    return String(e);
  }
}
</script>

<template>
  <section class="panel inspector">
    <div class="panel-head">
      <h2>{{ t("insp.title") }}</h2>
      <span class="hint">{{ events.length }} {{ t("insp.events") }}</span>
    </div>

    <div class="metrics">
      <div class="m"><span>LLM</span><b>{{ metrics.llm_calls ?? 0 }}</b></div>
      <div class="m"><span>Tokens</span><b>{{ metrics.total_tokens ?? 0 }}</b></div>
      <div class="m"><span>LLM Time</span><b>{{ metrics.llm_duration_ms ?? 0 }}ms</b></div>
      <div class="m"><span>∥ Batches</span><b>{{ metrics.parallel_batches ?? 0 }}</b></div>
      <div class="m"><span>Errors</span><b>{{ metrics.errors ?? 0 }}</b></div>
    </div>

    <div class="subhead togglable" @click="ctxOpen = !ctxOpen">
      <span>
        <span class="chev">{{ ctxOpen ? "▾" : "▸" }}</span>
        {{ t("insp.ctx") }}
        <span v-if="ctxOpen" class="hint">{{ t("insp.ctxHint") }}</span>
      </span>
      <button
        v-if="ctxOpen && ctxRuns.length"
        type="button"
        class="linkish"
        @click.stop="toggleAll()"
      >
        {{ expanded.size > 0 ? t("insp.collapseAll") : t("insp.expandAll") }}
      </button>
    </div>
    <div v-show="ctxOpen" class="context" :style="{ maxHeight: ctxSplit + 'px' }">
      <div v-if="!ctxRuns.length" class="empty">{{ t("insp.noSnap") }}</div>
      <template v-else>
        <div v-if="snapshot?.notes?.length" class="ctx-notes">
          <div class="dl-subhead">{{ t("insp.notesTitle") }}</div>
          <ul class="ctx-notes-list">
            <li v-for="(n, i) in snapshot.notes" :key="i">{{ n }}</li>
          </ul>
        </div>
        <div v-if="snapshot?.diff" class="ctx-diff">
          <div class="dl-subhead">
            {{ t("insp.diffTitle") }}
            <span class="hint">
              {{ t("insp.diffVs") }} {{ snapshot.diff.from_step ?? "—" }}
              → {{ snapshot.diff.to_step }}
              · {{ t("insp.diffAdded") }} {{ snapshot.diff.added ?? 0 }}
              · {{ t("insp.diffExcluded") }} {{ snapshot.diff.excluded_now ?? 0 }}
              · {{ t("insp.diffTrunc") }} {{ snapshot.diff.truncated_now ?? 0 }}
              · {{ t("insp.diffSaved") }} {{ snapshot.diff.saved_tokens ?? 0 }}
            </span>
          </div>
          <div v-if="snapshot.diff.notes?.length" class="ctx-notes-list">
            <div v-for="(n, i) in snapshot.diff.notes" :key="'d' + i">{{ n }}</div>
          </div>
          <div v-if="diffChanges.length" class="ctx-diff-rows">
            <div v-for="(e, i) in diffChanges" :key="i" class="ctx-diff-row" :data-change="e.change">
              <span class="ch">{{ e.change }}</span>
              <span class="src">{{ e.source }}</span>
              <span class="pv">{{ e.preview }}</span>
              <span v-if="e.reason" class="rs">{{ e.reason }}</span>
              <span v-if="e.policy" class="pl">{{ t("insp.policy") }}={{ e.policy }}</span>
              <span v-if="e.saved_tokens" class="sv">-{{ e.saved_tokens }} tok</span>
            </div>
          </div>
        </div>
        <div class="ctx-decisions">
          <div class="dl-subhead">
            {{ t("decision.title") }}
            <span class="hint">{{ t("decision.note") }}</span>
          </div>
          <div v-if="decisionRows.length" class="ctx-diff-rows">
            <div v-for="(d, i) in decisionRows" :key="'dec' + i" class="ctx-diff-row" data-change="decision">
              <span class="ch">{{ d.domain }}</span>
              <span class="src">{{ d.action }}</span>
              <span class="pv" :title="d.target">{{ d.target }}</span>
              <span v-if="d.policy" class="pl">{{ d.policy }}</span>
              <span class="rs">{{ d.reason }}</span>
              <span v-if="d.evidence?.length" class="sv">{{ d.evidence.join("; ") }}</span>
            </div>
          </div>
          <div v-else class="empty">{{ t("decision.empty") }}</div>
        </div>

        <template v-for="run in ctxRuns" :key="run.id">
          <div
            class="ctx-row ctx-row-summary"
            :class="{ excluded: run.excluded, open: expanded.has(run.id) }"
            role="button"
            tabindex="0"
            @click="toggleRun(run.id)"
            @keydown.enter.prevent="toggleRun(run.id)"
            @keydown.space.prevent="toggleRun(run.id)"
          >
            <div class="src">
              <span class="chev">{{ expanded.has(run.id) ? "▾" : "▸" }}</span>
              <span class="idx">#{{ run.start }}</span>
              {{ run.source }}
              <span v-if="run.count > 1" class="cnt">×{{ run.count }}</span>
            </div>
            <div class="ctx-bar"><i :style="{ width: run.pct + '%' }" /></div>
            <div class="tok">{{ run.tok }}</div>
          </div>

          <div v-if="expanded.has(run.id)" class="ctx-detail">
            <div
              v-for="item in run.items"
              :key="run.id + '-' + item.index"
              class="ctx-detail-row"
              :class="{
                excluded: item.excluded,
                truncated: item.truncated,
                open: isItemOpen(run.id, item.index),
              }"
              :role="item.hasMore ? 'button' : undefined"
              :tabindex="item.hasMore ? 0 : undefined"
              :title="item.hasMore && !isItemOpen(run.id, item.index) ? t('insp.clickExpand') : undefined"
              @click="item.hasMore && toggleItem(run.id, item.index)"
              @keydown.enter.prevent="item.hasMore && toggleItem(run.id, item.index)"
              @keydown.space.prevent="item.hasMore && toggleItem(run.id, item.index)"
            >
              <div class="d-tok">{{ item.tok }}</div>
              <div class="d-meta">
                <span class="tag">#{{ item.index }}</span>
                <span v-if="item.role" class="tag">{{ item.role }}</span>
                <span v-if="item.excluded" class="tag warn">{{ t("insp.tagExcluded") }}</span>
                <span v-else-if="item.truncated" class="tag warn">{{ t("insp.tagTruncated") }}</span>
                <span v-else class="tag ok">{{ t("insp.tagIncluded") }}</span>
                <span v-if="item.pinned" class="tag">{{ t("insp.tagPinned") }}</span>
                <span v-if="item.policy" class="reason">{{ t("insp.policy") }}={{ item.policy }}</span>
                <span v-if="item.saved" class="reason">{{ t("insp.saved") }}≈{{ item.saved }}</span>
                <span v-if="item.reason" class="reason">{{ item.reason }}</span>
              </div>
              <div v-if="item.prov" class="d-prov">
                <span v-if="item.prov.tool">tool={{ item.prov.tool }}</span>
                <span v-if="item.prov.call_id">call={{ item.prov.call_id }}</span>
                <span v-if="item.prov.path" :title="item.prov.path">{{ item.prov.path }}</span>
                <span v-if="item.prov.lines">lines {{ item.prov.lines }}</span>
                <span v-if="item.prov.produced_at_step">produced@{{ item.prov.produced_at_step }}</span>
                <span v-if="item.prov.entered_at_step">entered@{{ item.prov.entered_at_step }}</span>
                <span v-if="item.prov.transformed">{{ item.prov.transformed }}</span>
              </div>
              <div
                class="d-preview"
                :class="{ expandable: item.hasMore }"
                :title="item.hasMore && !isItemOpen(run.id, item.index) ? item.fullPreview : undefined"
              >
                <template v-if="isItemOpen(run.id, item.index)">
                  <pre class="d-full">{{ item.fullPreview || "—" }}</pre>
                  <span class="d-collapse-hint">{{ t("insp.clickCollapse") }}</span>
                </template>
                <template v-else>
                  <span>{{ item.preview }}</span>
                  <span v-if="item.hasMore" class="d-more">{{ t("insp.more") }}</span>
                </template>
              </div>
            </div>
          </div>
        </template>

        <div class="ctx-total">
          {{ t("insp.total") }} <b>{{ ctxTotal }}</b> / {{ t("insp.budget") }} {{ snapshot?.budget ?? "-" }}
          · {{ t("insp.step") }} {{ snapshot?.step ?? "-" }}
          · {{ t("insp.items") }} {{ snapshot?.items?.length ?? 0 }}
          · {{ t("insp.incl") }} {{ snapshot?.included_count ?? 0 }}
          · {{ t("insp.excl") }} {{ snapshot?.excluded_count ?? 0 }}
        </div>
      </template>
    </div>

    <div
      v-if="ctxOpen && tlOpen"
      class="tl-split"
      title="拖动调整 Context / Timeline 高度"
      @mousedown.prevent="startSplitDrag"
    ></div>
    <div class="subhead togglable" @click="tlOpen = !tlOpen">
      <span>
        <span class="chev">{{ tlOpen ? "▾" : "▸" }}</span>
        {{ t("insp.timeline") }}
        <span v-if="tlOpen" class="hint">
          {{
            tlMode === "live"
              ? t("insp.liveHint")
              : `${t("insp.histHint")} ${traceId || "—"}`
          }}
        </span>
      </span>
      <div class="tl-tools" @click.stop>
        <button
          type="button"
          class="linkish"
          :class="{ on: tlMode === 'live' }"
          @click="enterLive()"
        >
          {{ t("insp.live") }}
        </button>
        <button
          type="button"
          class="linkish"
          :class="{ on: tlMode === 'history' }"
          @click="enterHistory()"
        >
          {{ t("insp.history") }}
        </button>
      </div>
    </div>

    <div v-if="tlMode === 'history'" class="tl-history-bar">
      <select v-model="traceId" class="tl-select" @change="loadTrace()">
        <option v-for="tr in traces" :key="tr.id" :value="tr.id">
          {{ tr.id }}{{ tr.is_current ? ` (${t("common.current")})` : "" }}{{ tr.size ? ` · ${tr.size}B` : "" }}
        </option>
      </select>
      <input
        v-model="typeFilter"
        class="tl-input"
        :placeholder="t('insp.typeFilter')"
        @keyup.enter="loadTrace()"
      />
      <button
        type="button"
        class="linkish"
        :disabled="tlLoading"
        @click="
          loadTraceList().then(() => {
            if (traceId) return loadTrace();
          })
        "
      >
        {{ t("insp.reload") }}
      </button>
      <label class="tl-raw">
        <input v-model="showRawJson" type="checkbox" /> {{ t("insp.json") }}
      </label>
    </div>
    <div v-if="tlMode === 'history' && (tlDirs.length || tlDir || histMeta.total)" class="tl-history-meta">
      {{ (tlDirs.length ? tlDirs : [tlDir]).filter(Boolean).join(" | ") }}
      · {{ traces.length }} files
      <template v-if="histMeta.total || histMeta.shown">
        · showing {{ histMeta.shown }} / {{ histMeta.total }} events
      </template>
    </div>
    <div v-if="tlError" class="exp-error">{{ tlError }}</div>

    <div v-show="tlOpen" class="timeline">
      <div v-if="!timelineRows.length" class="empty">
        {{
          tlMode === "live"
            ? t("insp.wait")
            : tlLoading
              ? t("insp.loading")
              : tlError
                ? t("insp.histFail")
                : traces.length
                  ? `${t("insp.noEvents")}${traceId || "—"}`
                  : `${t("insp.noJsonl")}${(tlDirs.length ? tlDirs : [tlDir || "…"]).join(" | ")}`
        }}
      </div>
      <template v-else>
        <template v-for="(row, idx) in timelineRows" :key="row.kind === 'batch' ? row.id : row.e.id || idx">
          <!-- Parallel tool batch: graphical fork block -->
          <div
            v-if="row.kind === 'batch'"
            class="tl-batch"
          >
            <div class="tl-batch-head">
              <span class="fork-icon">⇉</span>
              <span class="ty">parallel batch</span>
              <span class="cnt">×{{ row.tools.length }}</span>
              <span class="meta">
                workers={{ row.maxWorkers }}
                <template v-if="row.ms"> · {{ row.ms }}ms</template>
                <template v-if="row.succeeded || row.failed">
                  · ok={{ row.succeeded }} err={{ row.failed }}
                </template>
              </span>
              <span class="t">{{ timeFmt(row.time) }}</span>
            </div>
            <div class="tl-batch-body">
              <div
                v-for="(tr, ti) in row.rows.length ? row.rows : row.tools.map((name) => ({ tool: name, args: '', ok: true, ms: 0, bytes: 0, error: '', status: 'named' as const, event: undefined as RuntimeEvent | undefined }))"
                :key="ti"
                class="tl-branch"
                :class="{
                  fail: tr.ok === false,
                  deeplink: !!tr.event && isDeeplink(tr.event),
                  last: ti === (row.rows.length ? row.rows.length : row.tools.length) - 1,
                }"
                :title="tr.event ? deeplinkTitle(tr.event) : undefined"
                @click="tr.event && onTimelineClick(tr.event)"
              >
                <span class="branch-arm" aria-hidden="true"></span>
                <span class="branch-tool">{{ tr.tool }}</span>
                <span class="d" :title="tr.args">{{ tr.args }}</span>
                <span class="branch-stat" :title="tr.ok === false ? tr.error : undefined">
                  <template v-if="tr.ok === false">✗ {{ shortErr(tr.error, 80) || "error" }}</template>
                  <template v-else-if="tr.status === 'finished' || tr.ms || tr.bytes">
                    ✓ {{ tr.ms }}ms · {{ tr.bytes }}B
                  </template>
                  <template v-else>…</template>
                  <span v-if="tr.event && isDeeplink(tr.event)" class="tl-goto">↗</span>
                </span>
              </div>
            </div>
          </div>

          <!-- Ordinary timeline event -->
          <div
            v-else
            class="tl-item"
            :class="[eventClass(row.e.type), { deeplink: isDeeplink(row.e) }]"
            :title="deeplinkTitle(row.e)"
            @click="onTimelineClick(row.e)"
          >
            <template v-if="showRawJson && tlMode === 'history'">
              <pre class="tl-raw-json">{{ rawJson(row.e) }}</pre>
            </template>
            <template v-else>
              <span class="t">{{ timeFmt(row.e.time) }}</span>
              <span class="ty">
                {{ shortType(row.e.type) }}{{
                  row.e.type === "llm.stream_delta" && Number(row.e.data?.count || 1) > 1
                    ? ` (x${Number(row.e.data?.count)})`
                    : ""
                }}
              </span>
              <span v-if="previewData(row.e.data)" class="d">{{ previewData(row.e.data) }}</span>
              <span
                class="tl-goto"
                title="回到此处（历史状态）"
                @click.stop="onReplayHere(row.e)"
              >⏱</span>
              <span v-if="isDeeplink(row.e)" class="tl-goto">↗</span>
            </template>
          </div>
        </template>
      </template>
    </div>
  </section>
</template>
