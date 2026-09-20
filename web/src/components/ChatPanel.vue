<script setup lang="ts">
import type { ChatMessage } from "../types";
import { useI18n } from "../i18n";
import MdText from "./MdText.vue";

const { t } = useI18n();

const props = defineProps<{
  messages: ChatMessage[];
  input: string;
  disabled?: boolean;
}>();

const emit = defineEmits<{
  "update:input": [value: string];
  send: [];
}>();

function onSubmit() {
  if (!props.disabled) emit("send");
}

function clock(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString(undefined, { hour12: false });
}
</script>

<template>
  <section class="panel chat">
    <div class="panel-head">
      <h2>{{ t("chat.title") }}</h2>
      <span class="hint">{{ t("chat.hint") }}</span>
    </div>
    <div class="messages" aria-live="polite">
      <div v-for="m in messages" :key="m.id" class="msg" :class="m.role">
        <div class="msg-head">
          <span class="role">{{ m.role }}</span>
          <span v-if="m.ts" class="ts">{{ clock(m.ts) }}</span>
        </div>
        <MdText :content="m.text" :plain="m.role === 'system'" />
      </div>
    </div>
    <form class="composer" @submit.prevent="onSubmit">
      <textarea
        rows="3"
        :placeholder="t('chat.placeholder')"
        :value="input"
        :disabled="disabled"
        @input="emit('update:input', ($event.target as HTMLTextAreaElement).value)"
      />
      <button class="primary" type="submit" :disabled="disabled">{{ t("chat.send") }}</button>
    </form>
  </section>
</template>
