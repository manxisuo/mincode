<script setup lang="ts">
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
    aria-label="文件变更"
    @click.self="emit('close')"
  >
    <div class="dl-modal">
      <div class="dl-head">
        <div>
          <b>file.changed</b>
          <span class="hint">{{ detail.operation || "change" }}</span>
        </div>
        <button type="button" class="linkish" @click="emit('close')">关闭</button>
      </div>
      <div class="dl-file-meta">
        <div class="path" :title="detail.path">{{ detail.path || "—" }}</div>
        <div class="meta">
          operation={{ detail.operation || "?" }}
          <template v-if="detail.bytes"> · {{ detail.bytes }}B</template>
        </div>
      </div>
      <div v-if="detail.diff" class="dl-diff-wrap">
        <div class="dl-subhead">Diff</div>
        <pre class="dl-diff">{{ detail.diff }}</pre>
      </div>
      <div v-else class="empty">该事件未携带 diff（可能为 create/overwrite 全量写入）</div>
    </div>
  </div>
</template>
