<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import {
  apiSessions,
  apiSessionDelete,
  apiSessionUpdate,
} from "../sessionApi";
import { useI18n } from "../i18n";
import type { SessionListItem } from "../types";

const { t } = useI18n();

const props = defineProps<{
  loadSession: (id: string) => Promise<void>;
}>();

const items = ref<SessionListItem[]>([]);
const current = ref("");
const sessionsDir = ref("");
const error = ref("");
const busy = ref("");

const editingId = ref("");
const editTitle = ref("");
const editNote = ref("");
const editBusy = ref(false);

async function refresh() {
  error.value = "";
  try {
    const data = await apiSessions();
    items.value = data.sessions || [];
    current.value = data.current || "";
    sessionsDir.value = data.sessions_dir || "";
  } catch (e) {
    error.value = String((e as Error).message || e);
    items.value = [];
  }
}

async function load(id: string) {
  busy.value = id;
  error.value = "";
  try {
    await props.loadSession(id);
    current.value = id;
    await refresh();
  } catch (e) {
    error.value = String((e as Error).message || e);
  } finally {
    busy.value = "";
  }
}

function startEdit(s: SessionListItem) {
  editingId.value = s.id;
  editTitle.value = s.title || "";
  editNote.value = s.note || "";
}

function cancelEdit() {
  editingId.value = "";
  editTitle.value = "";
  editNote.value = "";
}

async function saveEdit(id: string) {
  editBusy.value = true;
  error.value = "";
  try {
    await apiSessionUpdate(id, {
      title: editTitle.value.trim(),
      note: editNote.value.trim(),
    });
    cancelEdit();
    await refresh();
  } catch (e) {
    error.value = String((e as Error).message || e);
  } finally {
    editBusy.value = false;
  }
}

async function remove(id: string) {
  if (!window.confirm(t("sess.confirmDelete", { id }))) return;
  busy.value = id;
  error.value = "";
  try {
    await apiSessionDelete(id);
    if (editingId.value === id) cancelEdit();
    await refresh();
  } catch (e) {
    error.value = String((e as Error).message || e);
  } finally {
    busy.value = "";
  }
}

function fmtTime(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, { hour12: false });
}

let poll: number | null = null;
onMounted(() => {
  void refresh();
  poll = window.setInterval(() => void refresh(), 5000);
});
onBeforeUnmount(() => {
  if (poll != null) window.clearInterval(poll);
});
</script>

<template>
  <section class="panel sess-panel">
    <div class="panel-head">
      <h2>{{ t("sess.title") }}</h2>
      <div class="head-actions">
        <span class="hint">
          {{ t("sess.current") }}: {{ current || "—" }} · {{ items.length }} {{ t("sess.count") }}
        </span>
        <button type="button" class="linkish" @click="refresh()">{{ t("sess.refresh") }}</button>
      </div>
    </div>

    <div v-if="error" class="exp-error">{{ error }}</div>
    <div v-if="sessionsDir" class="instr-meta">{{ sessionsDir }}</div>

    <div class="sess-list">
      <div v-if="!items.length && !error" class="empty">
        {{ t("sess.empty") }}
      </div>
      <div
        v-for="s in items"
        :key="s.id"
        class="skill-card"
        :class="{ active: s.is_current || s.id === current }"
      >
        <template v-if="editingId === s.id">
          <div class="sess-edit">
            <label class="sess-edit-label">
              {{ t("sess.labelTitle") }}
              <input
                v-model="editTitle"
                class="tl-input"
                :placeholder="t('sess.titlePh')"
                maxlength="80"
              />
            </label>
            <label class="sess-edit-label">
              {{ t("sess.labelNote") }}
              <textarea
                v-model="editNote"
                class="tl-input sess-note-input"
                rows="3"
                :placeholder="t('sess.notePh')"
                maxlength="500"
              />
            </label>
            <div class="skill-actions">
              <button
                type="button"
                class="primary"
                :disabled="editBusy"
                @click="saveEdit(s.id)"
              >
                {{ editBusy ? t("sess.saving") : t("sess.save") }}
              </button>
              <button type="button" class="linkish" :disabled="editBusy" @click="cancelEdit()">
                {{ t("sess.cancelEdit") }}
              </button>
            </div>
          </div>
        </template>
        <template v-else>
          <div class="skill-name">
            {{ s.title || s.id }}
            <span v-if="s.is_current || s.id === current" class="tag-on">{{ t("common.current") }}</span>
          </div>
          <div class="skill-path">
            <template v-if="s.title">{{ s.id }} · </template>{{ fmtTime(s.updated_at) }}
            · {{ t("sess.turns") }}={{ s.turns ?? 0 }}
            · {{ t("sess.msgs") }}={{ s.message_count ?? 0 }}
            <template v-if="s.model"> · {{ s.provider }}/{{ s.model }}</template>
          </div>
          <div v-if="s.note" class="sess-note">{{ s.note }}</div>
          <div class="skill-actions">
            <button
              type="button"
              class="linkish"
              :disabled="busy === s.id || s.id === current"
              @click="load(s.id)"
            >
              {{ busy === s.id ? t("sess.loading") : t("sess.load") }}
            </button>
            <button type="button" class="linkish" @click="startEdit(s)">
              {{ t("sess.rename") }}
            </button>
            <button
              type="button"
              class="linkish danger"
              :disabled="busy === s.id || s.id === current"
              :title="s.id === current ? t('sess.cannotDeleteActive') : t('sess.deleteTitle')"
              @click="remove(s.id)"
            >
              {{ t("sess.delete") }}
            </button>
          </div>
        </template>
      </div>
    </div>
  </section>
</template>
