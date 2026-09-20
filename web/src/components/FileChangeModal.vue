<script setup lang="ts">
import { useI18n } from "../i18n";

const { t } = useI18n();

export interface FileChangeDetail {
  path: string;
  operation: string;
  bytes: number;
  diff: string;
}

defineProps<{
  open: boolean;
  detail: FileChangeDetail | null;
}>();

const emit = defineEmits<{ close: [] }>();
</script>

<template>
  <div
    v-if="open && detail"
    class="dl-overlay"
    role="dialog"
    aria-modal="true"
    aria-label="file.changed"
    @click.self="emit('close')"
  >
    <div class="dl-modal">
      <div class="dl-head">
        <div>
          <b>{{ t("fileModal.title") }}</b>
          <span class="hint">{{ detail.operation || "change" }}</span>
        </div>
        <button type="button" class="linkish" @click="emit('close')">{{ t("fileModal.close") }}</button>
      </div>
      <div class="dl-file-meta">
        <div class="path" :title="detail.path">{{ detail.path || "—" }}</div>
        <div class="meta">
          operation={{ detail.operation || "?" }}
          <template v-if="detail.bytes"> · {{ detail.bytes }}B</template>
        </div>
      </div>
      <div v-if="detail.diff" class="dl-diff-wrap">
        <div class="dl-subhead">{{ t("fileModal.diff") }}</div>
        <pre class="dl-diff">{{ detail.diff }}</pre>
      </div>
      <div v-else class="empty">{{ t("fileModal.noDiff") }}</div>
    </div>
  </div>
</template>
