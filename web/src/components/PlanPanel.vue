<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "../i18n";
import {
  apiPlan,
  apiPlanApprove,
  apiPlanCancel,
  apiPlanDraft,
  apiPlanReject,
} from "../planApi";
import type { Plan, RuntimeEvent, SessionInfo } from "../types";

const { t } = useI18n();

const props = defineProps<{
  /** Timeline deep-link: increment to force refresh. */
  bump?: number;
  /** Live runtime events for plan.step_* sync. */
  events?: RuntimeEvent[];
  /** Current session; used to show in-flight step final. */
  session?: SessionInfo | null;
}>();

const plan = ref<Plan | null>(null);
const goal = ref("");
const error = ref("");
const busy = ref(false);
const expandedSteps = ref<Set<number>>(new Set());

const progress = computed(() => {
  if (!plan.value) return { done: 0, total: 0 };
  const steps = plan.value.steps || [];
  const done = steps.filter(
    (s) => s.status === "done" || s.status === "skipped",
  ).length;
  return { done, total: steps.length };
});

const canApprove = computed(() => plan.value?.status === "draft");
const canReject = computed(
  () => plan.value?.status === "draft" || plan.value?.status === "approved",
);
const canCancel = computed(
  () =>
    plan.value?.status === "running" || plan.value?.status === "approved",
);

/** Live final text while the current plan step's agent turn is running. */
const liveStepFinal = computed(() => {
  if (!plan.value || plan.value.status !== "running") return "";
  if (props.session?.running === false) return "";
  return (props.session?.turn?.final || "").trim();
});

async function refresh() {
  try {
    const data = await apiPlan();
    plan.value = data.plan || null;
  } catch (e) {
    error.value = String((e as Error).message || e);
  }
}

async function draft() {
  const g = goal.value.trim();
  if (!g) {
    error.value = t("plan.needGoal");
    return;
  }
  busy.value = true;
  error.value = "";
  try {
    const data = await apiPlanDraft(g);
    plan.value = data.plan;
  } catch (e) {
    error.value = String((e as Error).message || e);
  } finally {
    busy.value = false;
  }
}

async function approve() {
  busy.value = true;
  error.value = "";
  try {
    await apiPlanApprove();
    await refresh();
  } catch (e) {
    error.value = String((e as Error).message || e);
  } finally {
    busy.value = false;
  }
}

async function reject() {
  busy.value = true;
  error.value = "";
  try {
    await apiPlanReject();
    plan.value = null;
  } catch (e) {
    error.value = String((e as Error).message || e);
  } finally {
    busy.value = false;
  }
}

async function cancel() {
  busy.value = true;
  error.value = "";
  try {
    const data = await apiPlanCancel();
    plan.value = data.plan || plan.value;
  } catch (e) {
    error.value = String((e as Error).message || e);
  } finally {
    busy.value = false;
  }
}

function mark(st: string): string {
  switch (st) {
    case "done":
      return "✓";
    case "running":
      return "›";
    case "failed":
      return "✗";
    case "cancelled":
      return "·";
    case "skipped":
      return "-";
    default:
      return "○";
  }
}

function toggleExpand(index: number) {
  const next = new Set(expandedSteps.value);
  if (next.has(index)) next.delete(index);
  else next.add(index);
  expandedSteps.value = next;
}

function isLong(text?: string): boolean {
  return !!text && (text.length > 120 || text.includes("\n"));
}

/** Apply plan.* SSE events so step finals appear without waiting for poll. */
function applyPlanEvents(events?: RuntimeEvent[]) {
  if (!events?.length || !plan.value) return;
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i];
    if (!e.type.startsWith("plan.")) continue;
    const d = e.data || {};
    const planId = String(d.plan_id || "");
    if (planId && plan.value.id && planId !== plan.value.id) continue;
    const idx = Number(d.step_index || 0);
    if (!idx) continue;
    const step = plan.value.steps.find((s) => s.index === idx);
    if (!step) continue;
    if (e.type === "plan.step_finished" && d.result != null) {
      step.status = "done";
      step.result = String(d.result);
      step.error = "";
    } else if (e.type === "plan.step_failed" && d.error != null) {
      step.status = "failed";
      step.error = String(d.error);
    } else if (e.type === "plan.step_started") {
      step.status = "running";
      if (Number(d.done_count) != null || d.step_count != null) {
        /* progress shown via poll too */
      }
    } else if (e.type === "plan.finished") {
      plan.value.status = "done";
    } else if (e.type === "plan.cancelled") {
      plan.value.status = "cancelled";
    }
  }
  // Reassign for reactivity when mutating nested step fields.
  plan.value = { ...plan.value, steps: [...plan.value.steps] };
}

watch(
  () => props.events,
  (ev) => applyPlanEvents(ev),
  { deep: true },
);

defineExpose({ refresh });

watch(
  () => props.bump,
  () => {
    void refresh();
  },
);

let poll: number | null = null;
onMounted(() => {
  void refresh();
  poll = window.setInterval(() => {
    // Keep polling while a plan runs even if a button click set busy.
    if (plan.value?.status === "running" || plan.value?.status === "approved") {
      void refresh();
      return;
    }
    if (!busy.value) void refresh();
  }, 1000);
});
onBeforeUnmount(() => {
  if (poll != null) window.clearInterval(poll);
});
</script>

<template>
  <section class="panel plan-panel">
    <div class="panel-head">
      <h2>{{ t("plan.title") }}</h2>
      <button type="button" class="linkish" :disabled="busy" @click="refresh()">
        {{ t("plan.refresh") }}
      </button>
    </div>

    <div class="plan-compose">
      <input
        v-model="goal"
        class="tl-input plan-goal"
        :placeholder="t('plan.goalPh')"
        :disabled="busy"
        @keyup.enter="draft()"
      />
      <button type="button" class="primary" :disabled="busy" @click="draft()">
        {{ t("plan.draft") }}
      </button>
    </div>

    <div v-if="error" class="exp-error">{{ error }}</div>

    <div v-if="!plan" class="empty plan-empty">
      {{ t("plan.empty") }}
    </div>

    <div v-else class="plan-body">
      <div class="plan-meta">
        <div class="plan-goal-line">
          <b>{{ plan.id }}</b>
          <span class="pill plan-status" :data-status="plan.status">{{ plan.status }}</span>
        </div>
        <div class="plan-goal-text">{{ plan.goal }}</div>
        <div class="plan-progress">
          {{ t("plan.steps") }} {{ progress.done }}/{{ progress.total }}
          <template v-if="plan.current"> · {{ t("plan.current") }} #{{ plan.current }}</template>
        </div>
      </div>

      <div class="plan-actions">
        <button
          v-if="canApprove"
          type="button"
          class="primary"
          :disabled="busy"
          @click="approve()"
        >
          {{ t("plan.approve") }}
        </button>
        <button v-if="canReject" type="button" :disabled="busy" @click="reject()">
          {{ t("plan.reject") }}
        </button>
        <button v-if="canCancel" type="button" :disabled="busy" @click="cancel()">
          {{ t("plan.cancel") }}
        </button>
      </div>

      <ol class="plan-steps">
        <li
          v-for="step in plan.steps"
          :key="step.index"
          class="plan-step"
          :data-status="step.status"
        >
          <span class="step-mark">{{ mark(step.status) }}</span>
          <div class="step-body">
            <div class="step-title">{{ step.index }}. {{ step.title }}</div>

            <div
              v-if="step.status === 'running' && liveStepFinal"
              class="step-live"
            >
              <div class="step-live-label">{{ t("plan.liveLabel") }}</div>
              <pre class="step-final live">{{ liveStepFinal }}</pre>
            </div>
            <div
              v-else-if="step.status === 'running'"
              class="step-live-label muted"
            >
              {{ t("plan.running") }}
            </div>

            <div v-if="step.result" class="step-result-block">
              <div class="step-live-label">{{ t("plan.finalLabel") }}</div>
              <pre
                class="step-final"
                :class="{ open: expandedSteps.has(step.index) }"
                :title="isLong(step.result) ? t('plan.expand') : undefined"
                @click="isLong(step.result) && toggleExpand(step.index)"
              >{{ step.result }}</pre>
              <button
                v-if="isLong(step.result)"
                type="button"
                class="linkish step-expand"
                @click="toggleExpand(step.index)"
              >
                {{ expandedSteps.has(step.index) ? t("plan.collapse") : t("plan.expand") }}
              </button>
            </div>
            <div v-if="step.error" class="step-err">{{ step.error }}</div>
          </div>
        </li>
      </ol>
    </div>
  </section>
</template>
