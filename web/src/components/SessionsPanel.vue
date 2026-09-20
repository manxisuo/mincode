<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { apiSessions } from "../sessionApi";
import type { SessionListItem } from "../types";

const props = defineProps<{
  loadSession: (id: string) => Promise<void>;
}>();

const items = ref<SessionListItem[]>([]);
const current = ref("");
const sessionsDir = ref("");
const error = ref("");
const busy = ref("");

async function refresh() {
  error.value = "";
  try {
    const data = await apiSessions();
    items.value = data.sessions || [];
    current.value = data.current || "";
    sessionsDir.value = data.sessions_dir || "";
  } catch (e) {
    error.value = String((e as Error).message || e);
    items.value = [];
  }
}

async function load(id: string) {
  busy.value = id;
  error.value = "";
  try {
    await props.loadSession(id);
    current.value = id;
    await refresh();
  } catch (e) {
    error.value = String((e as Error).message || e);
  } finally {
    busy.value = "";
  }
}

function fmtTime(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, { hour12: false });
}

let poll: number | null = null;
onMounted(() => {
  void refresh();
  poll = window.setInterval(() => void refresh(), 5000);
});
onBeforeUnmount(() => {
  if (poll != null) window.clearInterval(poll);
});
</script>

<template>
  <section class="panel sess-panel">
    <div class="panel-head">
      <h2>Sessions</h2>
      <div class="head-actions">
        <span class="hint">
          current: {{ current || "—" }} · {{ items.length }} session(s)
        </span>
        <button type="button" class="linkish" @click="refresh()">Refresh</button>
      </div>
    </div>

    <div v-if="error" class="exp-error">{{ error }}</div>
    <div v-if="sessionsDir" class="instr-meta">{{ sessionsDir }}</div>

    <div class="sess-list">
      <div v-if="!items.length && !error" class="empty">
        尚无已保存会话。进行对话后会自动写入 session 存储。
      </div>
      <div
        v-for="s in items"
        :key="s.id"
        class="skill-card"
        :class="{ active: s.is_current || s.id === current }"
      >
        <div class="skill-name">
          {{ s.title || s.id }}
          <span v-if="s.is_current || s.id === current" class="tag-on">current</span>
        </div>
        <div class="skill-path">
          <template v-if="s.title">{{ s.id }} · </template>{{ fmtTime(s.updated_at) }}
          · turns={{ s.turns ?? 0 }}
          · msgs={{ s.message_count ?? 0 }}
          <template v-if="s.model"> · {{ s.provider }}/{{ s.model }}</template>
        </div>
        <div class="skill-actions">
          <button
            type="button"
            class="linkish"
            :disabled="busy === s.id || s.id === current"
            @click="load(s.id)"
          >
            {{ busy === s.id ? "加载中…" : "切换到此会话" }}
          </button>
        </div>
      </div>
    </div>
  </section>
</template>
