<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "../i18n";
import { apiExperiment, apiExperiments } from "../expApi";
import type { ExperimentAggregate, ExperimentDetail } from "../expTypes";

const { t } = useI18n();

const list = ref<ExperimentAggregate[]>([]);
const selected = ref<ExperimentDetail | null>(null);
const error = ref("");
const loading = ref(false);
const root = ref("");

async function loadList() {
  loading.value = true;
  error.value = "";
  try {
    const data = await apiExperiments();
    list.value = data.experiments || [];
    root.value = data.root || "";
  } catch (e) {
    error.value = String((e as Error).message || e);
    list.value = [];
  } finally {
    loading.value = false;
  }
}

async function select(name: string) {
  loading.value = true;
  error.value = "";
  try {
    selected.value = await apiExperiment(name);
  } catch (e) {
    error.value = String((e as Error).message || e);
    selected.value = null;
  } finally {
    loading.value = false;
  }
}

const maxMedian = computed(() => {
  const vals = list.value.map((e) => e.duration_ms_stat?.median || 0);
  return Math.max(1, ...vals);
});

function pct(v: number, max: number) {
  return Math.max(2, Math.round((v / max) * 100));
}

function fmtN(v?: number) {
  if (v == null) return "—";
  return Number(v).toLocaleString();
}

function fmtMs(v?: number) {
  if (v == null) return "—";
  return `${Math.round(v)}ms`;
}

function successRate(a: ExperimentAggregate) {
  if (!a.runs) return "0%";
  return `${Math.round((a.successes / a.runs) * 100)}%`;
}

onMounted(loadList);
</script>

<template>
  <section class="panel exp-panel">
    <div class="panel-head">
      <h2>{{ t("exp.title") }}</h2>
      <div class="head-actions">
        <span class="hint">{{ root || "workspace experiments" }}</span>
        <button type="button" class="linkish" :disabled="loading" @click="loadList">
          {{ t("exp.refresh") }}
        </button>
      </div>
    </div>

    <div v-if="error" class="exp-error">{{ error }}</div>

    <div class="exp-body">
      <div class="exp-list">
        <div v-if="!list.length && !loading" class="empty">
          尚无实验。可用 CLI：
          <code>mincode experiment run --name base --task "..." --repeat 3</code>
        </div>
        <button
          v-for="a in list"
          :key="a.name"
          type="button"
          class="exp-card"
          :class="{ active: selected?.aggregate?.name === a.name }"
          @click="select(a.name)"
        >
          <div class="exp-name">{{ a.name }}</div>
          <div class="exp-meta">
            runs={{ a.runs }} · ok={{ a.successes }}/{{ a.runs }} ({{ successRate(a) }})
          </div>
          <div class="exp-meta">
            med steps {{ a.steps_stat?.median ?? 0 }}
            · med tok {{ fmtN(a.total_tokens_stat?.median) }}
            · med {{ fmtMs(a.duration_ms_stat?.median) }}
          </div>
          <div class="exp-bar">
            <i :style="{ width: pct(a.duration_ms_stat?.median || 0, maxMedian) + '%' }" />
          </div>
        </button>
      </div>

      <div class="exp-detail">
        <div v-if="!selected" class="empty">选择左侧实验查看 runs 与分布</div>
        <template v-else>
          <h3>{{ selected.aggregate.name }}</h3>
          <div class="stat-grid">
            <div class="stat">
              <span>Runs</span><b>{{ selected.aggregate.runs }}</b>
            </div>
            <div class="stat">
              <span>Success</span><b>{{ selected.aggregate.successes }}/{{ selected.aggregate.runs }}</b>
            </div>
            <div class="stat">
              <span>Med Steps</span><b>{{ selected.aggregate.steps_stat?.median ?? 0 }}</b>
            </div>
            <div class="stat">
              <span>Med Tokens</span><b>{{ fmtN(selected.aggregate.total_tokens_stat?.median) }}</b>
            </div>
            <div class="stat">
              <span>Med Time</span><b>{{ fmtMs(selected.aggregate.duration_ms_stat?.median) }}</b>
            </div>
            <div class="stat">
              <span>Models</span><b class="small">{{ selected.aggregate.models || "—" }}</b>
            </div>
          </div>

          <div class="subhead-inline">Distribution (min / median / max)</div>
          <table class="exp-table">
            <thead>
              <tr>
                <th>metric</th>
                <th>min</th>
                <th>median</th>
                <th>max</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td>steps</td>
                <td>{{ selected.aggregate.steps_stat?.min ?? 0 }}</td>
                <td>{{ selected.aggregate.steps_stat?.median ?? 0 }}</td>
                <td>{{ selected.aggregate.steps_stat?.max ?? 0 }}</td>
              </tr>
              <tr>
                <td>tool calls</td>
                <td>{{ selected.aggregate.tool_calls_stat?.min ?? 0 }}</td>
                <td>{{ selected.aggregate.tool_calls_stat?.median ?? 0 }}</td>
                <td>{{ selected.aggregate.tool_calls_stat?.max ?? 0 }}</td>
              </tr>
              <tr>
                <td>tokens</td>
                <td>{{ fmtN(selected.aggregate.total_tokens_stat?.min) }}</td>
                <td>{{ fmtN(selected.aggregate.total_tokens_stat?.median) }}</td>
                <td>{{ fmtN(selected.aggregate.total_tokens_stat?.max) }}</td>
              </tr>
              <tr>
                <td>duration</td>
                <td>{{ fmtMs(selected.aggregate.duration_ms_stat?.min) }}</td>
                <td>{{ fmtMs(selected.aggregate.duration_ms_stat?.median) }}</td>
                <td>{{ fmtMs(selected.aggregate.duration_ms_stat?.max) }}</td>
              </tr>
            </tbody>
          </table>

          <div class="subhead-inline">Runs</div>
          <div class="runs-wrap">
            <table class="exp-table">
              <thead>
                <tr>
                  <th>run</th>
                  <th>ok</th>
                  <th>steps</th>
                  <th>tools</th>
                  <th>llm</th>
                  <th>tokens</th>
                  <th>ms</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in selected.runs" :key="r.run_id" :class="{ fail: !r.success }">
                  <td>{{ r.run_id }}</td>
                  <td>{{ r.success ? "y" : "n" }}</td>
                  <td>{{ r.steps }}</td>
                  <td>{{ r.tool_calls }}</td>
                  <td>{{ r.llm_calls }}</td>
                  <td>{{ fmtN(r.total_tokens) }}</td>
                  <td>{{ r.duration_ms }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </div>
    </div>
  </section>
</template>
