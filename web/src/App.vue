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
import RepoMapPanel from "./components/RepoMapPanel.vue";
import SessionsPanel from "./components/SessionsPanel.vue";
import SkillsPanel from "./components/SkillsPanel.vue";
import ConfigResolutionModal, {
  type ConfigField,
} from "./components/ConfigResolutionModal.vue";
import { apiContext, apiContextAtStep } from "./api";
import ToolCallModal, {
  type ToolCallDetail,
} from "./components/ToolCallModal.vue";
import WireRequestModal from "./components/WireRequestModal.vue";
import { useI18n } from "./i18n";
import { useInspector } from "./composables/useInspector";
import { useTheme } from "./theme";
import type { ContextSnapshot, RuntimeEvent } from "./types";
import { apiWire, type WireRecord } from "./wireApi";

const { theme, toggle } = useTheme();
const { t, toggleLocale, langLabel } = useI18n();

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
  | "repomap"
  | "sessions"
  | "experiments"
>("inspector");

const stateLabel = computed(() => session.value?.state || "IDLE");
const running = computed(() => !!session.value?.running);
const meta = computed(() => {
  const s = session.value;
  if (!s) return t("app.connecting");
  return `${s.session_id} · ${s.provider}/${s.model} · ${s.workspace}`;
});
const themeLabel = computed(() =>
  theme.value === "dark" ? t("app.theme.dark") : t("app.theme.light"),
);
const themeIcon = computed(() => (theme.value === "dark" ? "🌙" : "☀️"));
const themeTitle = computed(() =>
  theme.value === "dark" ? t("app.theme.toLight") : t("app.theme.toDark"),
);
const exportBusy = ref(false);
const openSkillName = ref("");
const planBump = ref(0);
const openInstructionPath = ref("");
const openRepoMapPath = ref("");
const openRepoMapFocus = ref("");
const permOpen = ref(false);
const permFocus = ref<Record<string, unknown> | null>(null);
const permEvents = ref<RuntimeEvent[]>([]);
const fileOpen = ref(false);
const fileDetail = ref<FileChangeDetail | null>(null);
const toolOpen = ref(false);
const toolDetail = ref<ToolCallDetail | null>(null);
const cfgOpen = ref(false);
const cfgFields = ref<ConfigField[]>([]);
const cfgFile = ref("");

async function openConfig() {
  try {
    const res = await fetch("/api/config");
    const data = (await res.json()) as {
      resolution?: { fields?: ConfigField[]; config_file?: string };
    };
    cfgFields.value = data.resolution?.fields || [];
    cfgFile.value = data.resolution?.config_file || "";
  } catch {
    cfgFields.value = [];
    cfgFile.value = "";
  }
  cfgOpen.value = true;
}
const replaySnap = ref<ContextSnapshot | null>(null);
const replayStep = ref<number | null>(null);

/** T-obs-5: pin Inspector to historical state at this Timeline event. */
function onReplay(e: RuntimeEvent, sourceEvents?: RuntimeEvent[]) {
  const list = sourceEvents && sourceEvents.length ? sourceEvents : events.value;
  let idx = list.findIndex((x) => x === e);
  if (idx < 0 && e.id) {
    idx = list.findIndex((x) => x.id === e.id && x.type === e.type);
  }
  if (idx < 0) idx = list.length - 1;
  // Snapshot.Step is BuildRequest order (1..N). Event.Step is often ctx length —
  // take the nearest preceding context.built / llm.wire_request's data.step.
  let step = 0;
  for (let i = idx; i >= 0; i--) {
    const ev = list[i];
    if (ev.type === "context.built" || ev.type === "llm.wire_request") {
      step = Number((ev.data as Record<string, unknown> | undefined)?.step || 0);
      if (step > 0) break;
    }
  }
  if (!step) step = Number(e.data?.step || e.step || 0);
  void (async () => {
    if (!step) {
      try {
        const cur = await apiContext();
        step = Number((cur as unknown as { step?: number }).step || 0);
      } catch {
        step = 0;
      }
    }
    if (!step) {
      replaySnap.value = null;
      replayStep.value = -1;
      return;
    }
    try {
      const data = await apiContextAtStep(step);
      replaySnap.value = data.snapshot || null;
    } catch {
      replaySnap.value = null;
    }
    replayStep.value = step;
  })();
}

function exitReplay() {
  replaySnap.value = null;
  replayStep.value = null;
}
const wireOpen = ref(false);
const wireRecord = ref<WireRecord | null>(null);
const wireEvent = ref<RuntimeEvent | null>(null);

async function openWireView(evt?: RuntimeEvent) {
  wireEvent.value = evt || null;
  wireOpen.value = true;
  try {
    const data = await apiWire();
    wireRecord.value = data.wire || null;
  } catch {
    wireRecord.value = null;
  }
}

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
  if (type === "repo_map.built") {
    openRepoMapPath.value = String(d.subpath || "");
    openRepoMapFocus.value = String(d.focus || "");
    view.value = "repomap";
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
    return;
  }
  if (
    type === "llm.wire_request" ||
    type === "llm.request_finished" ||
    type === "llm.request_started"
  ) {
    // Reconstruct a synthetic RuntimeEvent for token fields when needed.
    const synthetic: RuntimeEvent = {
      type,
      time: d.time != null ? String(d.time) : undefined,
      data: d,
    };
    void openWireView(synthetic);
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
      showToast(t("app.export.ok"), "ok");
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
      showToast(data.error || res.statusText || t("app.export.fail"), "err");
      return;
    }
    const target = data.rel || data.path || "";
    const n = data.bytes != null ? ` · ${data.bytes}B` : "";
    showToast(`${t("app.export.done")}: ${target}${n}`, "ok");
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
    <ConfigResolutionModal
      :open="cfgOpen"
      :fields="cfgFields"
      :config-file="cfgFile"
      @close="cfgOpen = false"
    />
    <WireRequestModal
      :open="wireOpen"
      :wire="wireRecord"
      :event="wireEvent"
      :time-fmt="fmtTime"
      @close="wireOpen = false"
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
          <strong>{{ t("app.brand") }}</strong>
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
            {{ t("nav.inspector") }}
          </button>
          <button
            type="button"
            :class="{ active: view === 'plan' }"
            @click="view = 'plan'"
          >
            {{ t("nav.plan") }}
          </button>
          <button
            type="button"
            :class="{ active: view === 'skills' }"
            @click="view = 'skills'"
          >
            {{ t("nav.skills") }}
          </button>
          <button
            type="button"
            :class="{ active: view === 'instructions' }"
            @click="view = 'instructions'"
          >
            {{ t("nav.instructions") }}
          </button>
          <button
            type="button"
            :class="{ active: view === 'memory' }"
            @click="view = 'memory'"
          >
            {{ t("nav.memory") }}
          </button>
          <button
            type="button"
            :class="{ active: view === 'repomap' }"
            @click="view = 'repomap'"
          >
            {{ t("nav.repomap") }}
          </button>
          <button
            type="button"
            :class="{ active: view === 'sessions' }"
            @click="view = 'sessions'"
          >
            {{ t("nav.sessions") }}
          </button>
          <button
            type="button"
            :class="{ active: view === 'experiments' }"
            @click="view = 'experiments'"
          >
            {{ t("nav.experiments") }}
          </button>
        </nav>
        <button type="button" class="theme-btn" :title="t('app.lang')" @click="toggleLocale()">
          {{ langLabel }}
        </button>
        <button
          type="button"
          class="theme-btn"
          :title="t('cfg.title')"
          @click="openConfig()"
        >
          {{ t("cfg.btn") }}
        </button>
        <button type="button" class="theme-btn" :title="themeTitle" @click="toggle()">
          {{ themeIcon }} {{ themeLabel }}
        </button>
        <button
          type="button"
          class="theme-btn"
          :title="t('app.export.title')"
          :disabled="exportBusy"
          @click="exportMarkdown(false)"
        >
          {{ t("app.export.btn") }}
        </button>
        <button
          type="button"
          class="theme-btn"
          :title="t('app.export.mdTitle')"
          @click="exportMarkdown(true)"
        >
          ↓MD
        </button>
        <span class="pill" :data-state="stateLabel">{{ stateLabel }}</span>
        <button type="button" :disabled="!running" @click="cancel()">{{ t("app.cancel") }}</button>
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
          view === 'repomap' ||
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
          :snapshot="replaySnap || snapshot"
          :replay-step="replayStep"
          :events="events"
          :time-fmt="fmtTime"
          :event-class="eventClass"
          :short-type="shortType"
          :preview-data="previewData"
          @deeplink="onTimelineDeepLink"
          @replay="onReplay"
          @exit-replay="exitReplay"
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
      <RepoMapPanel
        v-else-if="view === 'repomap'"
        :open-path="openRepoMapPath"
        :open-focus="openRepoMapFocus"
      />
      <SessionsPanel
        v-else-if="view === 'sessions'"
        :load-session="onSwitchSession"
      />
      <ExperimentPanel v-else />
    </main>
  </div>
</template>
