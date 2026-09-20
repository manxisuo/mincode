<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { useI18n } from "../i18n";
import {
  apiDecidePermission,
  apiPendingPermissions,
} from "../permissionApi";
import type { PendingPermission } from "../types";

const { t } = useI18n();

const pending = ref<PendingPermission[]>([]);
const busy = ref(false);
const error = ref("");

async function refresh() {
  try {
    const data = await apiPendingPermissions();
    pending.value = data.pending || [];
  } catch {
    /* server may not expose API yet */
  }
}

async function decide(p: PendingPermission, allow: boolean) {
  busy.value = true;
  error.value = "";
  try {
    await apiDecidePermission(p.id, allow);
    pending.value = pending.value.filter((x) => x.id !== p.id);
  } catch (e) {
    error.value = String((e as Error).message || e);
  } finally {
    busy.value = false;
  }
}

function argsPreview(p: PendingPermission): string {
  if (!p.arguments) return "";
  try {
    const o = JSON.parse(p.arguments) as Record<string, unknown>;
    if (o.path) return String(o.path);
    if (o.command) return String(o.command);
    return JSON.stringify(o).slice(0, 80);
  } catch {
    return String(p.arguments).slice(0, 80);
  }
}

function esc(s: string): string {
  return s
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;");
}

/** Classic unified-diff coloring: +/−/@@/headers. */
function diffHtml(diff: string): string {
  return diff
    .split(/\r?\n/)
    .map((line) => {
      let cls = "ctx";
      if (line.startsWith("+++") || line.startsWith("---") || line.startsWith("diff ")) {
        cls = "meta";
      } else if (line.startsWith("@@")) {
        cls = "hunk";
      } else if (line.startsWith("+")) {
        cls = "add";
      } else if (line.startsWith("-")) {
        cls = "del";
      } else if (line.startsWith("\\")) {
        cls = "meta";
      }
      return `<span class="dl ${cls}">${esc(line)}</span>`;
    })
    .join("\n");
}

let poll: number | null = null;
onMounted(() => {
  void refresh();
  poll = window.setInterval(() => void refresh(), 1500);
});
onBeforeUnmount(() => {
  if (poll != null) window.clearInterval(poll);
});

defineExpose({ refresh });
</script>

<template>
  <div v-if="pending.length" class="perm-overlay" role="dialog" aria-modal="true">
    <div class="perm-card">
      <div class="perm-head">
        <b>{{ t("perm.title") }}</b>
        <span class="hint">{{ t("perm.hint") }}</span>
      </div>
      <div v-if="error" class="exp-error">{{ error }}</div>
      <div v-for="p in pending" :key="p.id" class="perm-item">
        <div class="perm-title">
          <span class="perm-tool">{{ p.tool }}</span>
          <span class="perm-sum">{{ p.summary || argsPreview(p) }}</span>
        </div>
        <div v-if="p.diff" class="perm-diff">
          <pre v-html="diffHtml(p.diff)"></pre>
        </div>
        <div v-else-if="p.arguments" class="perm-args">
          <pre>{{ p.arguments }}</pre>
        </div>
        <div class="perm-actions">
          <button
            type="button"
            class="primary"
            :disabled="busy"
            @click="decide(p, true)"
          >
            {{ t("perm.allow") }}
          </button>
          <button type="button" :disabled="busy" @click="decide(p, false)">
            {{ t("perm.deny") }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
