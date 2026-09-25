<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "../i18n";

const { t } = useI18n();

export interface ToolCallDetail {
  type: string;
  tool: string;
  callId: string;
  arguments: string;
  durationMs: number;
  resultSize: number;
  isError: boolean;
  error: string;
  outputPreview: string;
  parallel: boolean;
  index: number;
  time?: string;
}

const props = defineProps<{
  open: boolean;
  detail: ToolCallDetail | null;
  timeFmt: (iso?: string) => string;
}>();

const emit = defineEmits<{ close: [] }>();

type ArgRow = { key: string; value: string };

const argRows = computed<ArgRow[]>(() => {
  const raw = props.detail?.arguments || "";
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      return Object.entries(parsed as Record<string, unknown>).map(([key, val]) => ({
        key,
        value: formatVal(val),
      }));
    }
    return [{ key: "(json)", value: formatVal(parsed) }];
  } catch {
    return [{ key: "(raw)", value: raw }];
  }
});

const statusLabel = computed(() => {
  const d = props.detail;
  if (!d) return "";
  if (d.type === "tool.failed" || d.isError) return "failed";
  if (d.type === "tool.finished") return "finished";
  return d.type.replace(/^tool\./, "");
});

function formatVal(v: unknown): string {
  if (v == null) return "null";
  if (typeof v === "string") return v;
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

const outputLines = computed(() => {
  if (fullText.value) return fullText.value;
  const s = props.detail?.outputPreview || props.detail?.error || "";
  return s;
});

const fullText = ref("");
const loadingFull = ref(false);

async function loadFull() {
  const id = props.detail?.callId;
  if (!id) return;
  loadingFull.value = true;
  try {
    const res = await fetch(
      "/api/tools/result?call_id=" + encodeURIComponent(id),
    );
    const data = (await res.json()) as { content?: string; error?: string };
    fullText.value = data.content || data.error || "";
  } catch (e) {
    fullText.value = "load failed: " + String(e);
  } finally {
    loadingFull.value = false;
  }
}
</script>

<template>
  <div
    v-if="open && detail"
    class="dl-overlay"
    role="dialog"
    aria-modal="true"
    aria-label="tool call detail"
    @click.self="emit('close')"
  >
    <div class="dl-modal">
      <div class="dl-head">
        <div>
          <b>{{ detail.tool || "tool" }}</b>
          <span class="hint">
            {{ detail.type }}
            <template v-if="detail.time"> · {{ timeFmt(detail.time) }}</template>
          </span>
        </div>
        <button type="button" class="linkish" @click="emit('close')">{{ t("toolModal.close") }}</button>
      </div>

      <div class="tool-meta">
        <span class="pill" :data-state="statusLabel">{{ statusLabel }}</span>
        <span v-if="detail.durationMs" class="meta">{{ detail.durationMs }}ms</span>
        <span v-if="detail.resultSize" class="meta">{{ detail.resultSize }}B</span>
        <span v-if="detail.parallel" class="meta">parallel</span>
        <span v-if="detail.index != null && detail.index > 0" class="meta">#{{ detail.index }}</span>
        <span v-if="detail.callId" class="meta mono" :title="detail.callId">{{ detail.callId }}</span>
      </div>

      <div class="dl-subhead">{{ t("toolModal.args") }}</div>
      <div v-if="!argRows.length" class="empty">{{ t("toolModal.noArgs") }}</div>
      <table v-else class="tool-args">
        <tbody>
          <tr v-for="r in argRows" :key="r.key">
            <td class="k">{{ r.key }}</td>
            <td class="v"><pre>{{ r.value }}</pre></td>
          </tr>
        </tbody>
      </table>

      <div class="dl-subhead">
        {{ detail.isError || detail.type === "tool.failed" ? t("toolModal.error") : t("toolModal.result") }}
        <button
          v-if="detail.callId && !fullText"
          type="button"
          class="linkish"
          :disabled="loadingFull"
          style="margin-left: 8px"
          @click="loadFull()"
        >
          {{ loadingFull ? "…" : t("toolModal.loadFull") }}
        </button>
      </div>
      <pre
        class="tool-out"
        :class="{ err: detail.isError || detail.type === 'tool.failed' }"
      >{{ outputLines || t("toolModal.emptyOut") }}</pre>
      <div v-if="detail.resultSize && detail.outputPreview && detail.outputPreview.length < detail.resultSize" class="hint tool-trunc">
        {{ t("toolModal.trunc", { a: detail.outputPreview.length, b: detail.resultSize }) }}
      </div>
    </div>
  </div>
</template>
