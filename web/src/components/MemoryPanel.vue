<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { useI18n } from "../i18n";
import { apiMemory, apiMemoryAdd } from "../memoryApi";
import MdText from "./MdText.vue";

const { t } = useI18n();

const content = ref("");
const path = ref("");
const fileName = ref("MEMORY.md");
const exists = ref(false);
const entries = ref(0);
const composedChars = ref(0);
const error = ref("");
const entry = ref("");
const busy = ref(false);

async function refresh() {
  error.value = "";
  try {
    const data = await apiMemory();
    content.value = data.content || "";
    path.value = data.path || "";
    if (data.file_name) fileName.value = data.file_name;
    exists.value = !!data.exists;
    entries.value = data.entries || 0;
    composedChars.value = data.composed_chars || 0;
  } catch (e) {
    error.value = String((e as Error).message || e);
  }
}

async function add() {
  const fact = entry.value.trim();
  if (!fact) {
    error.value = t("mem.needFact");
    return;
  }
  busy.value = true;
  error.value = "";
  try {
    const data = await apiMemoryAdd(fact);
    content.value = data.content || "";
    entries.value = data.entries || 0;
    composedChars.value = data.composed_chars || 0;
    exists.value = true;
    entry.value = "";
  } catch (e) {
    error.value = String((e as Error).message || e);
  } finally {
    busy.value = false;
  }
}

let poll: number | null = null;
onMounted(() => {
  void refresh();
  poll = window.setInterval(() => void refresh(), 4000);
});
onBeforeUnmount(() => {
  if (poll != null) window.clearInterval(poll);
});
</script>

<template>
  <section class="panel mem-panel">
    <div class="panel-head">
      <h2>{{ t("mem.title") }}</h2>
      <div class="head-actions">
        <span class="hint">
          {{ fileName }} · {{ entries }} {{ t("mem.entries") }} · {{ composedChars }} {{ t("mem.charsInCtx") }}
        </span>
        <button type="button" class="linkish" @click="refresh()">{{ t("mem.refresh") }}</button>
      </div>
    </div>

    <div v-if="error" class="exp-error">{{ error }}</div>
    <div v-if="path" class="instr-meta">{{ path }}</div>

    <div class="mem-compose">
      <input
        v-model="entry"
        class="tl-input"
        :placeholder="t('mem.ph')"
        :disabled="busy"
        @keyup.enter="add()"
      />
      <button type="button" class="primary" :disabled="busy" @click="add()">
        {{ t("mem.add") }}
      </button>
    </div>

    <div class="mem-body">
      <div v-if="!content && !error" class="empty">
        {{ t("mem.empty") }}
        <code>/memory add &lt;fact&gt;</code>
      </div>
      <div v-else class="skills-md mem-md">
        <MdText :content="content" />
      </div>
    </div>
  </section>
</template>
