<script setup lang="ts">
import { computed } from "vue";
import type { RuntimeEvent } from "../types";

const props = defineProps<{
  open: boolean;
  events: RuntimeEvent[];
  /** Focus target from a Timeline permission.* deep-link. */
  focus?: Record<string, unknown> | null;
  timeFmt: (iso?: string) => string;
}>();

const emit = defineEmits<{ close: [] }>();

type PermRow = {
  key: string;
  id: string;
  time?: string;
  type: string;
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
  const focusId = props.focus?.id != null ? String(props.focus.id) : "";
  const focusTool = props.focus?.tool != null ? String(props.focus.tool) : "";
  const focusTime = props.focus?.time != null ? String(props.focus.time) : "";
  const out: PermRow[] = [];
  for (const e of props.events) {
    if (!String(e.type || "").startsWith("permission.")) continue;
    const d = e.data || {};
    const id = String(d.id || e.id || "");
    const tool = String(d.tool || "");
    const type = e.type.replace(/^permission\./, "");
    const focused =
      (focusId && id && id === focusId) ||
      (!focusId && focusTool && tool === focusTool && focusTime && e.time === focusTime) ||
      (!focusId && !focusTool && false);
    out.push({
      key: `${e.id || e.time}-${e.type}-${out.length}`,
      id,
      time: e.time,
      type,
      tool,
      summary: String(d.summary || ""),
      arguments: String(d.arguments || ""),
      decision: String(d.decision || type),
      reason: String(d.reason || ""),
      level: String(d.level || ""),
      diff: String(d.diff || ""),
      focused: !!focused,
    });
  }
  // If nothing matched focus id exactly, still show history; mark first same-tool.
  if (focusTool && !out.some((r) => r.focused)) {
    for (let i = out.length - 1; i >= 0; i--) {
      if (out[i].tool === focusTool) {
        out[i] = { ...out[i], focused: true };
        break;
      }
    }
  }
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
    aria-label="权限历史"
    @click.self="emit('close')"
  >
    <div class="dl-modal">
      <div class="dl-head">
        <div>
          <b>权限历史</b>
          <span class="hint">permission.* · {{ rows.length }} 条</span>
        </div>
        <button type="button" class="linkish" @click="emit('close')">关闭</button>
      </div>
      <div v-if="!rows.length" class="empty">尚无 permission 事件</div>
      <div v-else class="dl-list">
        <div
          v-for="r in rows"
          :key="r.key"
          class="dl-row"
          :class="{ focused: r.focused, [r.decision]: true }"
        >
          <div class="dl-row-head">
            <span class="ty">permission.{{ r.type }}</span>
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
