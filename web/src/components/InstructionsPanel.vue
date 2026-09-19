<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  apiInstructionsAll,
  apiInstructionsReload,
  type InstructionItem,
} from "../instructionApi";
import MdText from "./MdText.vue";

const props = defineProps<{
  /** Timeline deep-link: open this instruction rel_path/path when set. */
  openPath?: string;
}>();

const items = ref<InstructionItem[]>([]);
const composedChars = ref(0);
const fileName = ref("AGENTS.md");
const error = ref("");
const selected = ref<InstructionItem | null>(null);
const busy = ref(false);

async function refresh() {
  error.value = "";
  try {
    const data = await apiInstructionsAll();
    items.value = data.instructions || [];
    composedChars.value = data.composed_chars || 0;
    if (data.file_name) fileName.value = data.file_name;
  } catch (e) {
    error.value = String((e as Error).message || e);
    items.value = [];
  }
}

function selectByPath(path: string) {
  if (!path) return;
  const hit =
    items.value.find((f) => f.rel_path === path) ||
    items.value.find((f) => f.path === path) ||
    items.value.find(
      (f) => path.endsWith("/" + f.rel_path) || path.endsWith("\\" + f.rel_path),
    ) ||
    items.value[0];
  if (hit) selected.value = hit;
}

async function reload() {
  busy.value = true;
  error.value = "";
  try {
    await apiInstructionsReload();
    await refresh();
  } catch (e) {
    error.value = String((e as Error).message || e);
  } finally {
    busy.value = false;
  }
}

watch(
  () => props.openPath,
  (p) => {
    if (!p) return;
    void (async () => {
      if (!items.value.length) await refresh();
      selectByPath(p);
    })();
  },
);

let poll: number | null = null;
onMounted(() => {
  void refresh().then(() => {
    if (props.openPath) selectByPath(props.openPath);
  });
  poll = window.setInterval(() => void refresh(), 4000);
});
onBeforeUnmount(() => {
  if (poll != null) window.clearInterval(poll);
});
</script>

<template>
  <section class="panel instr-panel">
    <div class="panel-head">
      <h2>Instructions</h2>
      <div class="head-actions">
        <span class="hint">{{ fileName }} · {{ items.length }} file(s)</span>
        <button type="button" class="linkish" :disabled="busy" @click="reload()">
          Reload
        </button>
      </div>
    </div>

    <div v-if="error" class="exp-error">{{ error }}</div>
    <div class="instr-meta">
      composed: {{ composedChars }} chars injected as system context
    </div>

    <div class="instr-body">
      <div class="instr-list">
        <div v-if="!items.length && !error" class="empty">
          未加载指令 — 在 workspace 下添加 <code>{{ fileName }}</code>
        </div>
        <div
          v-for="f in items"
          :key="f.path"
          class="skill-card"
          :class="{ selected: selected?.path === f.path }"
          role="button"
          tabindex="0"
          @click="selected = f"
          @keydown.enter="selected = f"
        >
          <div class="skill-name">
            <span v-if="f.rel_dir === '.'" class="tag-on">root</span>
            {{ f.rel_path }}
          </div>
          <div class="skill-path">{{ f.rel_dir }} · {{ f.bytes }}B</div>
          <div v-if="f.preview" class="skill-sum">{{ f.preview }}</div>
        </div>
      </div>

      <div class="instr-detail">
        <div v-if="!selected" class="empty">
          左侧选择 AGENTS.md 查看全文（等价 CLI <code>/instructions</code>）
        </div>
        <template v-else>
          <div class="skills-detail-head">
            <div>
              <b>{{ selected.rel_path }}</b>
              <div class="hint">{{ selected.path }}</div>
            </div>
            <button type="button" class="linkish" @click="selected = null">
              关闭
            </button>
          </div>
          <div class="skills-md">
            <MdText :content="selected.content || selected.preview || ''" />
          </div>
        </template>
      </div>
    </div>
  </section>
</template>
