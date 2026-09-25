<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "../i18n";

const { t } = useI18n();

export interface ConfigLayer {
  from: string;
  value: string;
  path?: string;
}

export interface ConfigField {
  key: string;
  value: string;
  winner: string;
  trail: ConfigLayer[];
}

const props = defineProps<{
  open: boolean;
  fields: ConfigField[];
  configFile?: string;
}>();

const emit = defineEmits<{ close: [] }>();
const onlyWinner = ref(false);

const rows = computed(() =>
  onlyWinner.value
    ? props.fields.map((f) => ({
        ...f,
        trail: f.trail.slice(-1),
      }))
    : props.fields,
);
watch(
  () => props.open,
  () => {
    onlyWinner.value = false;
  },
);
</script>

<template>
  <div v-if="open" class="dl-overlay" @click.self="emit('close')">
    <div class="dl-modal cfg-modal">
      <div class="dl-head">
        <div>
          <b>{{ t("cfg.title") }}</b>
          <span class="hint">{{ configFile || t("cfg.noFile") }}</span>
        </div>
        <button type="button" class="linkish" @click="emit('close')">
          {{ t("cfg.close") }}
        </button>
      </div>
      <p class="hint">{{ t("cfg.note") }}</p>
      <label class="tl-raw">
        <input v-model="onlyWinner" type="checkbox" />
        {{ t("cfg.onlyWinner") }}
      </label>
      <div v-if="!fields.length" class="empty">{{ t("cfg.empty") }}</div>
      <div v-else class="cfg-list">
        <div v-for="f in rows" :key="f.key" class="cfg-field">
          <div class="cfg-key">
            <code>{{ f.key }}</code>
            <span class="val">{{ f.value || "—" }}</span>
            <span class="win">← {{ f.winner }}</span>
          </div>
          <ol class="cfg-trail">
            <li v-for="(l, i) in f.trail" :key="i">
              <span class="from">{{ l.from }}</span>
              <span class="v">{{ l.value }}</span>
              <span v-if="l.path" class="p">{{ l.path }}</span>
            </li>
          </ol>
        </div>
      </div>
    </div>
  </div>
</template>
