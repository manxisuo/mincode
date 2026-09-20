<script setup lang="ts">
import { computed, ref } from "vue";
import ChatPanel from "./components/ChatPanel.vue";
import ExperimentPanel from "./components/ExperimentPanel.vue";
import FileChangeModal, {
  type FileChangeDetail,
} from "./components/FileChangeModal.vue";
import InspectorPanel from "./components/InspectorPanel.vue";
import InstructionsPanel from "./components/InstructionsPanel.vue";
import MemoryPanel from "./components/MemoryPanel.vue";
import PermissionHistoryModal from "./components/PermissionHistoryModal.vue";
import PlanPanel from "./components/PlanPanel.vue";
import PermissionBar from "./components/PermissionBar.vue";
import SessionsPanel from "./components/SessionsPanel.vue";
import SkillsPanel from "./components/SkillsPanel.vue";
import ToolCallModal, {
  type ToolCallDetail,
} from "./components/ToolCallModal.vue";
import { useInspector } from "./composables/useInspector";
import { useTheme } from "./theme";
import type { RuntimeEvent } from "./types";

const { theme, toggle } = useTheme();

const {
  messages,
  events,
  session,
  metrics,
  snapshot,
  input,
  send,
  cancel,
  loadSession,
  eventClass,
  shortType,
  previewData,
  fmtTime,
} = useInspector();

const view = ref<
  | "inspector"
  | "plan"
  | "skills"
  | "instructions"
  | "memory"
  | "sessions"
  | "experiments"
>("inspector");

const stateLabel = computed(() => session.value?.state || "IDLE");
const running = computed(() => !!session.value?.running);
const meta = computed(() => {
  const s = session.value;
  if (!s) return "connecting…";
  return `${s.session_id} · ${s.provider}/${s.model} · ${s.workspace}`;
});
const themeLabel = computed(() => (theme.value === "dark" ? "Light" : "Dark"));
const exportBusy = ref(false);
const openSkillName = ref("");
const planBump = ref(0);
const openInstructionPath = ref("");
const permOpen = ref(false);
const permFocus = ref<Record<string, unknown> | null>(null);
const permEvents = ref<RuntimeEvent[]>([]);
const fileOpen = ref(false);
const fileDetail = ref<FileChangeDetail | null>(null);
const toolOpen = ref(false);
const toolDetail = ref<ToolCallDetail | null>(null);

function onTimelineDeepLink(
  type: string,
  data?: Record<string, unknown>,
  sourceEvents?: RuntimeEvent[],
) {
  const name = data && data.name != null ? String(data.name) : "";
  const d = data || {};

  if (type.startsWith("skill.")) {
    if (name) openSkillName.value = name;
    view.value = "skills";
    return;
  }
  if (type.startsWith("plan.")) {
    planBump.value++;
    view.value = "plan";
    return;
  }
  if (type.startsWith("permission.")) {
    permEvents.value = sourceEvents && sourceEvents.length ? sourceEvents : events.value;
    permFocus.value = { ...d, time: d.time };
    permOpen.value = true;
    return;
  }
  if (type === "instruction.loaded") {
    const p = String(d.rel_path || d.path || "");
    openInstructionPath.value = p;
    view.value = "instructions";
    return;
  }
  if (type === "file.changed") {
    fileDetail.value = {
      path: String(d.path || ""),
      operation: String(d.operation || ""),
      bytes: Number(d.bytes || 0),
      diff: String(d.diff || ""),
    };
    fileOpen.value = true;
    return;
  }
  if (type.startsWith("tool.")) {
    toolDetail.value = {
      type,
      tool: String(d.tool || ""),
      callId: String(d.call_id || d.callId || ""),
      arguments: String(d.arguments || ""),
      durationMs: Number(d.duration_ms || 0),
      resultSize: Number(d.result_size || 0),
      isError: !!d.is_error || type === "tool.failed",
      error: String(d.error || ""),
      outputPreview: String(d.output_preview || ""),
      parallel: !!d.parallel,
      index: Number(d.index || 0),
      time: d.time != null ? String(d.time) : undefined,
    };
    toolOpen.value = true;
  }
}
const toast = ref<{ text: string; kind: "ok" | "err" | "info" } | null>(null);
let toastTimer: number | null = null;

function showToast(text: string, kind: "ok" | "err" | "info" = "ok") {
  toast.value = { text, kind };
  if (toastTimer != null) window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => {
    toast.value = null;
    toastTimer = null;
  }, 4500);
}

async function exportMarkdown(download: boolean) {
  if (download) {
    try {
      window.open("/api/export/download", "_blank");
      showToast("已开始下载 Markdown", "ok");
    } catch (e) {
      showToast(String((e as Error).message || e), "err");
    }
    return;
  }
  exportBusy.value = true;
  try {
    const res = await fetch("/api/export", { method: "POST" });
    const data = (await res.json().catch(() => ({}))) as {
      ok?: boolean;
      rel?: string;
      path?: string;
      bytes?: number;
      error?: string;
    };
    if (!res.ok || !data.ok) {
      showToast(data.error || res.statusText || "export failed", "err");
      return;
    }
    const target = data.rel || data.path || "";
    const n = data.bytes != null ? ` · ${data.bytes}B` : "";
    showToast(`导出成功：${target}${n}`, "ok");
  } catch (e) {
    showToast(String((e as Error).message || e), "err");
  } finally {
    exportBusy.value = false;
  }
}

function onInput(v: string) {
  input.value = v;
}

function onSend() {
  void send();
}

async function onSwitchSession(id: string) {
  await loadSession(id);
  view.value = "inspector";
}
</script>

<template>
  <div class="app-shell">
    <PermissionBar />
    <PermissionHistoryModal
      :open="permOpen"
      :events="permEvents"
      :focus="permFocus"
      :time-fmt="fmtTime"
      @close="permOpen = false"
    />
    <FileChangeModal
      :open="fileOpen"
      :detail="fileDetail"
      @close="fileOpen = false"
    />
    <ToolCallModal
      :open="toolOpen"
      :detail="toolDetail"
      :time-fmt="fmtTime"
      @close="toolOpen = false"
    />
    <div
      v-if="toast"
      class="toast"
      :data-kind="toast.kind"
      role="status"
      aria-live="polite"
    >
      {{ toast.text }}
    </div>
    <header class="top">
      <div class="brand">
        <span class="logo">MC</span>
        <div>
          <strong>MinCode Inspector</strong>
          <div class="meta">{{ meta }}</div>
        </div>
      </div>
      <div class="top-right">
        <nav class="view-tabs">
          <button
            type="button"
            :class="{ active: view === 'inspector' }"
            @click="view = 'inspector'"
          >
            Inspector
          </button>
          <button
            type="button"
            :class="{ active: view === 'plan' }"
            @click="view = 'plan'"
          >
            Plan
          </button>
          <button
            type="button"
            :class="{ active: view === 'skills' }"
            @click="view = 'skills'"
          >
            Skills
          </button>
          <button
            type="button"
            :class="{ active: view === 'instructions' }"
            @click="view = 'instructions'"
          >
            Instructions
          </button>
          <button
            type="button"
            :class="{ active: view === 'memory' }"
            @click="view = 'memory'"
          >
            Memory
          </button>
          <button
            type="button"
            :class="{ active: view === 'sessions' }"
            @click="view = 'sessions'"
          >
            Sessions
          </button>
          <button
            type="button"
            :class="{ active: view === 'experiments' }"
            @click="view = 'experiments'"
          >
            Experiments
          </button>
        </nav>
        <button type="button" class="theme-btn" :title="'切换到' + themeLabel + '主题'" @click="toggle()">
          {{ theme === "dark" ? "🌙" : "☀" }} {{ themeLabel }}
        </button>
        <button
          type="button"
          class="theme-btn"
          title="导出到 workspace/exports/*.md"
          :disabled="exportBusy"
          @click="exportMarkdown(false)"
        >
          Export
        </button>
        <button
          type="button"
          class="theme-btn"
          title="浏览器下载 Markdown"
          @click="exportMarkdown(true)"
        >
          ↓MD
        </button>
        <span class="pill" :data-state="stateLabel">{{ stateLabel }}</span>
        <button type="button" :disabled="!running" @click="cancel()">Cancel</button>
      </div>
    </header>

    <main
      class="layout"
      :class="{
        'layout-full':
          view === 'experiments' ||
          view === 'plan' ||
          view === 'skills' ||
          view === 'instructions' ||
          view === 'memory' ||
          view === 'sessions',
      }"
    >
      <template v-if="view === 'inspector'">
        <ChatPanel
          :messages="messages"
          :input="input"
          :disabled="running"
          @update:input="onInput"
          @send="onSend"
        />
        <InspectorPanel
          :metrics="metrics"
          :snapshot="snapshot"
          :events="events"
          :time-fmt="fmtTime"
          :event-class="eventClass"
          :short-type="shortType"
          :preview-data="previewData"
          @deeplink="onTimelineDeepLink"
        />
      </template>
      <PlanPanel
        v-else-if="view === 'plan'"
        :bump="planBump"
        :events="events"
        :session="session"
      />
      <SkillsPanel v-else-if="view === 'skills'" :open-name="openSkillName" />
      <InstructionsPanel
        v-else-if="view === 'instructions'"
        :open-path="openInstructionPath"
      />
      <MemoryPanel v-else-if="view === 'memory'" />
      <SessionsPanel
        v-else-if="view === 'sessions'"
        :load-session="onSwitchSession"
      />
      <ExperimentPanel v-else />
    </main>
  </div>
</template>
