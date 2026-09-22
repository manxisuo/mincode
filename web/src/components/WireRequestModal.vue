<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "../i18n";
import type { RuntimeEvent } from "../types";
import type { WireMessageView, WireRecord } from "../wireApi";

const props = defineProps<{
  open: boolean;
  wire: WireRecord | null;
  /** Optional event payload for token fields when wire is stale. */
  event?: RuntimeEvent | null;
  timeFmt: (iso?: string) => string;
}>();

const emit = defineEmits<{ close: [] }>();
const { t } = useI18n();

const chain = computed(
  () =>
    [
      { stage: "context_build", desc: t("wire.chain.context") },
      { stage: "sanitize_tool_pairs", desc: t("wire.chain.sanitize") },
      { stage: "provider_payload", desc: t("wire.chain.provider") },
    ] as Array<{ stage: string; desc: string }>,
);

const messages = computed<WireMessageView[]>(() => {
  const w = props.wire;
  if (!w) return [];
  if (w.summary?.messages?.length) return w.summary.messages;
  return (w.messages || []).map((m, index) => ({
    index,
    role: m.role,
    content_len: (m.content || "").length,
    content_preview: (m.content || "").slice(0, 240),
    tool_call_id: m.tool_call_id,
    tool_calls: (m.tool_calls || []).map((c) => c.name || "?"),
  }));
});

const tools = computed(() => {
  return props.wire?.summary?.tools || props.wire?.tools || [];
});

const meta = computed(() => {
  const w = props.wire;
  const ev = props.event?.data || {};
  const provider =
    w?.summary?.provider ||
    w?.provider ||
    String(ev.provider || props.event?.data?.provider || "");
  const model =
    w?.summary?.model || w?.model || String(ev.model || "");
  const stream =
    w?.summary?.stream ?? w?.stream ?? !!ev.streamed;
  return {
    provider,
    model,
    stream,
    step: w?.step,
    time: w?.time,
    temperature: w?.temperature,
    maxTokens: w?.max_tokens,
  };
});

const tokens = computed(() => {
  const w = props.wire;
  const ev = props.event?.data || {};
  const estimated =
    w?.estimated_prompt_tokens ??
    Number(ev.estimated_prompt_tokens || 0);
  const provider =
    w?.provider_prompt_tokens ??
    Number(ev.input_tokens || ev.provider_prompt_tokens || 0);
  const snapTotal = w?.snapshot_total_tokens ?? 0;
  const snapTools = w?.snapshot_tool_tokens ?? 0;
  const delta =
    w?.prompt_token_delta ??
    (estimated > 0 && provider > 0 ? provider - estimated : Number(ev.prompt_token_delta || 0));
  return { estimated, provider, snapTotal, snapTools, delta };
});

function prettyTools(): string {
  return tools.value
    .map((x) => `${x.name} (${x.schema_bytes}B)`)
    .join(", ");
}

function fmtCall(c: NonNullable<WireMessageView["tool_calls"]>[number] | string): string {
  return typeof c === "string" ? c : String(c);
}

function msgContent(m: WireMessageView): string {
  const full = props.wire?.messages?.[m.index]?.content;
  return full != null && full !== "" ? full : m.content_preview || "";
}
</script>

<template>
  <div
    v-if="open"
    class="dl-overlay"
    role="dialog"
    aria-modal="true"
    aria-label="wire request"
    @click.self="emit('close')"
  >
    <div class="dl-modal wire-modal">
      <div class="dl-head">
        <div>
          <b>{{ t("wire.title") }}</b>
          <span class="hint">
            {{ meta.provider }}/{{ meta.model }}
            <template v-if="meta.stream"> · stream</template>
            <template v-if="meta.time"> · {{ timeFmt(meta.time) }}</template>
          </span>
        </div>
        <button type="button" class="linkish" @click="emit('close')">{{ t("wire.close") }}</button>
      </div>

      <div class="wire-chain">
        <div class="dl-subhead">{{ t("wire.chainTitle") }}</div>
        <ol class="wire-chain-list">
          <li v-for="c in chain" :key="c.stage">
            <code>{{ c.stage }}</code>
            <span class="hint">{{ c.desc }}</span>
          </li>
        </ol>
      </div>

      <div class="dl-subhead">{{ t("wire.tokensTitle") }}</div>
      <table class="tool-args wire-tokens">
        <tbody>
          <tr>
            <td class="k">{{ t("wire.tokEstimated") }}</td>
            <td class="v">{{ tokens.estimated || "—" }}</td>
          </tr>
          <tr>
            <td class="k">{{ t("wire.tokSnapshot") }}</td>
            <td class="v">
              {{ tokens.snapTotal || "—" }}
              <template v-if="tokens.snapTools"> (+{{ tokens.snapTools }} {{ t("wire.tokTools") }})</template>
            </td>
          </tr>
          <tr>
            <td class="k">{{ t("wire.tokProvider") }}</td>
            <td class="v">{{ tokens.provider || "—" }}</td>
          </tr>
          <tr>
            <td class="k">{{ t("wire.tokDelta") }}</td>
            <td class="v" :class="{ warn: tokens.delta !== 0 }">
              {{ tokens.provider ? (tokens.delta > 0 ? "+" : "") + tokens.delta : "—" }}
            </td>
          </tr>
        </tbody>
      </table>

      <div class="dl-subhead">{{ t("wire.messagesTitle") }} ({{ messages.length }})</div>
      <div v-if="!messages.length" class="empty">{{ t("wire.empty") }}</div>
      <div v-else class="wire-msgs">
        <div v-for="m in messages" :key="m.index" class="wire-msg">
          <div class="wire-msg-head">
            <span class="idx">#{{ m.index }}</span>
            <span class="role">{{ m.role }}</span>
            <span class="len">{{ m.content_len }}B</span>
            <span v-if="m.tool_call_id" class="tid">{{ m.tool_call_id }}</span>
            <span v-if="m.tool_calls?.length" class="calls">
              tools: {{ m.tool_calls.map(fmtCall).join(", ") }}
            </span>
          </div>
          <pre class="wire-body">{{ msgContent(m) || "—" }}</pre>
        </div>
      </div>

      <div class="dl-subhead">{{ t("wire.toolsTitle") }} ({{ tools.length }})</div>
      <div v-if="!tools.length" class="empty">—</div>
      <div v-else class="hint wire-tools">{{ prettyTools() }}</div>

      <div v-if="meta.temperature != null || meta.maxTokens != null" class="wire-params hint">
        <template v-if="meta.temperature != null">temperature={{ meta.temperature }}</template>
        <template v-if="meta.maxTokens != null"> · max_tokens={{ meta.maxTokens }}</template>
      </div>
    </div>
  </div>
</template>
