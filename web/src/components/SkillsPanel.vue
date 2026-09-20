<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "../i18n";
import {
  apiSkill,
  apiSkillActivate,
  apiSkillDeactivate,
  apiSkills,
} from "../skillApi";
import type { SkillDetail, SkillListItem } from "../types";
import MdText from "./MdText.vue";

const { t } = useI18n();

const props = defineProps<{
  /** Timeline deep-link: open this skill name when set/changed. */
  openName?: string;
}>();

const skills = ref<SkillListItem[]>([]);
const skillsDir = ref("skills/");
const skillsDirAbs = ref("");
const activeCount = ref(0);
const contextChars = ref(0);
const error = ref("");
const selected = ref<SkillDetail | null>(null);
const loading = ref(false);
const busyName = ref("");

async function refresh() {
  error.value = "";
  try {
    const data = await apiSkills();
    skills.value = data.skills || [];
    skillsDir.value = data.dir || "skills/";
    skillsDirAbs.value = data.skills_dir || "";
    activeCount.value = data.active_count || 0;
    contextChars.value = data.context_chars || 0;
  } catch (e) {
    error.value = String((e as Error).message || e);
    skills.value = [];
  }
}

async function open(name: string) {
  loading.value = true;
  error.value = "";
  try {
    selected.value = await apiSkill(name);
  } catch (e) {
    error.value = String((e as Error).message || e);
    selected.value = null;
  } finally {
    loading.value = false;
  }
}

function closeDetail() {
  selected.value = null;
}

async function toggleActive(name: string, active: boolean, ev?: Event) {
  ev?.stopPropagation();
  busyName.value = name;
  error.value = "";
  try {
    if (active) {
      await apiSkillDeactivate(name);
    } else {
      await apiSkillActivate(name);
    }
    await refresh();
    if (selected.value?.name === name) {
      selected.value = await apiSkill(name);
    }
  } catch (e) {
    error.value = String((e as Error).message || e);
  } finally {
    busyName.value = "";
  }
}

watch(
  () => props.openName,
  (name) => {
    if (!name) return;
    void (async () => {
      await refresh();
      await open(name);
    })();
  },
);

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
  <section class="panel skills-panel">
    <div class="panel-head">
      <h2>{{ t("skills.title") }}</h2>
      <div class="head-actions">
        <span class="hint">{{ skillsDir }} · {{ skills.length }} {{ t("skills.available") }}</span>
        <button type="button" class="linkish" @click="refresh()">{{ t("skills.refresh") }}</button>
      </div>
    </div>

    <div v-if="error" class="exp-error">{{ error }}</div>
    <div v-if="skillsDirAbs" class="skills-meta">
      dir: {{ skillsDirAbs }} · {{ t("skills.inContext") }}: {{ activeCount }} skill(s),
      {{ contextChars }} chars
    </div>

    <div class="skills-body">
      <div class="skills-list">
        <div v-if="!skills.length && !error" class="empty">
          {{ t("skills.empty") }}
          <code>skills/&lt;name&gt;/SKILL.md</code>
        </div>
        <div
          v-for="sk in skills"
          :key="sk.name"
          class="skill-card"
          :class="{ active: sk.active, selected: selected?.name === sk.name }"
          role="button"
          tabindex="0"
          @click="open(sk.name)"
          @keydown.enter="open(sk.name)"
        >
          <div class="skill-name">
            <span class="mark">{{ sk.active ? "●" : "○" }}</span>
            {{ sk.name }}
            <span v-if="sk.active" class="tag-on">{{ t("common.active") }}</span>
          </div>
          <div v-if="sk.summary" class="skill-sum">{{ sk.summary }}</div>
          <div class="skill-path">{{ sk.rel_path }} · {{ sk.bytes }}B</div>
          <div class="skill-actions">
            <button
              type="button"
              class="linkish"
              :disabled="busyName === sk.name"
              @click="toggleActive(sk.name, !!sk.active, $event)"
            >
              {{ sk.active ? t("skills.deactivate") : t("skills.activate") }}
            </button>
          </div>
        </div>
      </div>

      <div class="skills-detail">
        <div v-if="!selected" class="empty">
          {{ t("skills.detailEmpty") }}
          <code>/skill &lt;name&gt;</code>
        </div>
        <template v-else>
          <div class="skills-detail-head">
            <div>
              <b>{{ selected.name }}</b>
              <span v-if="selected.active" class="tag-on">{{ t("common.active") }}</span>
              <div class="hint">{{ selected.rel_path }}</div>
            </div>
            <div class="skill-actions">
              <button
                type="button"
                :disabled="busyName === selected.name"
                @click="toggleActive(selected.name, !!selected.active)"
              >
                {{ selected.active ? t("skills.deactivate") : t("skills.activate") }}
              </button>
              <button type="button" class="linkish" @click="closeDetail()">
                {{ t("skills.close") }}
              </button>
            </div>
          </div>
          <div class="skills-md">
            <MdText :content="selected.content" />
          </div>
        </template>
      </div>
    </div>
  </section>
</template>
