<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { useI18n } from "../i18n";
import type { RuntimeEvent } from "../types";

const { t } = useI18n();

const props = defineProps<{
  open: boolean;
  events: RuntimeEvent[];
  /** Focus target from a Timeline permission.* deep-link. */
  focus?: Record<string, unknown> | null;
  timeFmt: (iso?: string) => string;
}>();

const emit = defineEmits<{ close: [] }>();

const modalEl = ref<HTMLElement | null>(null);

const focusKey = computed(() => {
  const f = props.focus || {};
  return String(f.__row_key || [f.__type, f.id, f.time, f.tool].filter(Boolean).join("|"));
});

watch(
  () => (props.open ? focusKey.value || "open" : ""),
  async (key) => {
    if (!key || !props.open) return;
    await nextTick();
    requestAnimationFrame(() => {
      const el = modalEl.value?.querySelector(".dl-row.focused") as HTMLElement | null;
      el?.scrollIntoView({ block: "center", behavior: "smooth" });
    });
  },
);

function eventRowKey(e: RuntimeEvent): string {
  const d = e.data || {};
  return [
    e.id || "",
    e.type || "",
    e.time || "",
    String(d.id || ""),
    String(d.tool || ""),
  ].join("|");
}

type PermRow = {
  key: string;
  rowKey: string;
  id: string;
  time?: string;
  fullType: string;
  tool: string;
  summary: string;
  arguments: string;
  decision: string;
  reason: string;
  level: string;
  diff: string;
  focused: boolean;
};

const rows = computed<PermRow[]>(() => {
  const focusRowKey = props.focus?.__row_key != null ? String(props.focus.__row_key) : "";
  const focusType = props.focus?.__type != null ? String(props.focus.__type) : "";
  const focusId = props.focus?.id != null ? String(props.focus.id) : "";
  const focusTool = props.focus?.tool != null ? String(props.focus.tool) : "";
  const focusTime = props.focus?.time != null ? String(props.focus.time) : "";

  const out: PermRow[] = [];
  const seen = new Set<string>();
  for (const e of props.events) {
    if (!String(e.type || "").startsWith("permission.")) continue;
    const d = e.data || {};
    const rowKey = eventRowKey(e);
    if (seen.has(rowKey)) continue;
    seen.add(rowKey);
    const id = String(d.id || "");
    const type = e.type.replace(/^permission\./, "");
    out.push({
      key: rowKey,
      rowKey,
      id,
      time: e.time,
      fullType: e.type,
      tool: String(d.tool || ""),
      summary: String(d.summary || ""),
      arguments: String(d.arguments || ""),
      decision: String(d.decision || type),
      reason: String(d.reason || ""),
      level: String(d.level || ""),
      diff: String(d.diff || ""),
      focused: false,
    });
  }

  // Exactly one blue frame: prefer the precise Timeline row key.
  if (focusRowKey) {
    const hit = out.find((r) => r.rowKey === focusRowKey);
    if (hit) {
      hit.focused = true;
      return out;
    }
  }

  // Fallback when the snapshot event id/key is missing: score then pick ONE.
  let bestIdx = -1;
  let bestScore = -1;
  for (let i = 0; i < out.length; i++) {
    const r = out[i];
    let score = 0;
    if (focusType && r.fullType === focusType) score += 40;
    if (focusId && r.id && r.id === focusId) score += 30;
    if (focusTool && r.tool && r.tool === focusTool) score += 20;
    if (focusTime && r.time && r.time === focusTime) score += 10;
    if (score > bestScore) {
      bestScore = score;
      bestIdx = i;
    }
  }
  if (bestIdx >= 0 && bestScore > 0) out[bestIdx].focused = true;
  return out;
});

function prettyArgs(s: string): string {
  if (!s) return "";
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}
</script>

<template>
  <div
    v-if="open"
    class="dl-overlay"
    role="dialog"
    aria-modal="true"
    aria-label="permission history"
    @click.self="emit('close')"
  >
    <div ref="modalEl" class="dl-modal">
      <div class="dl-head">
        <div>
          <b>{{ t("permHistory.title") }}</b>
          <span class="hint">{{ t("permHistory.hint", { n: rows.length }) }}</span>
        </div>
        <button type="button" class="linkish" @click="emit('close')">{{ t("permHistory.close") }}</button>
      </div>
      <div v-if="!rows.length" class="empty">{{ t("permHistory.empty") }}</div>
      <div v-else class="dl-list">
        <div
          v-for="r in rows"
          :key="r.key"
          class="dl-row"
          :class="{ focused: r.focused, [r.decision]: true }"
        >
          <div class="dl-row-head">
            <span class="ty">{{ r.fullType }}</span>
            <span class="tool">{{ r.tool }}</span>
            <span class="dec" :data-d="r.decision">{{ r.decision }}</span>
            <span class="t">{{ timeFmt(r.time) }}</span>
          </div>
          <div v-if="r.summary" class="sum">{{ r.summary }}</div>
          <div v-if="r.reason" class="reason">{{ r.reason }}</div>
          <div v-if="r.level" class="meta">level={{ r.level }}<template v-if="r.id"> · {{ r.id }}</template></div>
          <pre v-if="r.arguments" class="args">{{ prettyArgs(r.arguments) }}</pre>
          <pre v-if="r.diff" class="diff">{{ r.diff }}</pre>
        </div>
      </div>
    </div>
  </div>
</template>
