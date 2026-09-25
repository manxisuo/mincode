<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "../i18n";
import { apiRepoMap, type RepoMapFile, type RepoMapResponse } from "../repoMapApi";

const { t } = useI18n();

const data = ref<RepoMapResponse | null>(null);
const error = ref("");
const loading = ref(false);
const pathInput = ref("");
const focusInput = ref("");
const expanded = ref<Set<string>>(new Set());
const showText = ref(false);

async function load() {
  loading.value = true;
  error.value = "";
  try {
    data.value = await apiRepoMap({
      path: pathInput.value.trim(),
      focus: focusInput.value.trim(),
    });
    expanded.value = new Set();
  } catch (e) {
    error.value = String((e as Error).message || e);
    data.value = null;
  } finally {
    loading.value = false;
  }
}

function reset() {
  pathInput.value = "";
  focusInput.value = "";
  void load();
}

function toggle(path: string) {
  const next = new Set(expanded.value);
  if (next.has(path)) next.delete(path);
  else next.add(path);
  expanded.value = next;
}

const files = computed<RepoMapFile[]>(() => data.value?.files || []);
const totalSymbols = computed(() =>
  files.value.reduce((n, f) => n + (f.symbols?.length || 0), 0),
);
const cacheTotal = computed(
  () => (data.value?.cache_hits || 0) + (data.value?.cache_misses || 0),
);

onMounted(load);
</script>

<template>
  <section class="panel repomap-panel">
    <div class="panel-head">
      <h2>{{ t("repomap.title") }}</h2>
      <div class="head-actions">
        <span v-if="data && data.enabled" class="hint">
          {{ files.length }} {{ t("repomap.files") }} ·
          {{ totalSymbols }} {{ t("repomap.symbols") }} · ~{{ data.tokens }} tok ·
          {{ data.build_ms }}ms · {{ t("repomap.scanned") }} {{ data.scanned }}
          <template v-if="cacheTotal">
            · cache {{ data.cache_hits }}/{{ cacheTotal }}
          </template>
          <template v-if="data.truncated">
            · <span class="repomap-trunc">{{ t("repomap.truncated") }}</span>
          </template>
        </span>
        <button type="button" class="linkish" :disabled="loading" @click="load">
          {{ t("repomap.refresh") }}
        </button>
      </div>
    </div>

    <div class="repomap-bar">
      <input
        v-model="pathInput"
        class="tl-input"
        :placeholder="t('repomap.pathPh')"
        @keyup.enter="load"
      />
      <input
        v-model="focusInput"
        class="tl-input"
        :placeholder="t('repomap.focusPh')"
        @keyup.enter="load"
      />
      <button type="button" class="primary" :disabled="loading" @click="load">
        {{ t("repomap.apply") }}
      </button>
      <button type="button" class="linkish" @click="reset">
        {{ t("repomap.reset") }}
      </button>
    </div>

    <div v-if="data && data.root" class="instr-meta">
      {{ data.root }}
      <template v-if="data.subpath"> · {{ data.subpath }}</template>
      <template v-if="data.focus"> · focus={{ data.focus }}</template>
    </div>

    <div v-if="error" class="exp-error">{{ error }}</div>
    <div v-else-if="data && !data.enabled" class="empty">
      {{ t("repomap.disabled") }}
    </div>
    <div v-else-if="!files.length" class="empty">{{ t("repomap.empty") }}</div>

    <div v-else class="repomap-body">
      <table class="repomap-table">
        <thead>
          <tr>
            <th class="num">#</th>
            <th>{{ t("repomap.colPath") }}</th>
            <th class="num">{{ t("repomap.colScore") }}</th>
            <th>{{ t("repomap.colWhy") }}</th>
            <th class="num">{{ t("repomap.colSyms") }}</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="(f, i) in files" :key="f.path">
            <tr :class="{ open: expanded.has(f.path) }" @click="toggle(f.path)">
              <td class="num">{{ i + 1 }}</td>
              <td class="path">
                {{ f.path }}
                <span class="lang">{{ f.lang }}</span>
              </td>
              <td class="num score">{{ f.score }}</td>
              <td class="why">
                <span class="chip">
                  {{ t("repomap.defs") }} {{ f.rank.defs
                  }}<template v-if="f.rank.defs_capped">+</template>
                </span>
                <span class="chip" :class="{ hot: f.rank.exported > 0 }">
                  {{ t("repomap.exported") }} {{ f.rank.exported }}
                </span>
                <span class="chip" :class="{ hot: f.rank.refs > 0 }">
                  {{ t("repomap.refs") }} {{ f.rank.refs
                  }}<template v-if="f.rank.refs_capped">+</template>
                </span>
                <span v-if="f.rank.path_hit" class="chip focus">
                  {{ t("repomap.pathHit") }}
                </span>
                <span v-if="f.rank.symbol_hits" class="chip focus">
                  {{ t("repomap.symHits") }} {{ f.rank.symbol_hits }}
                </span>
                <span v-if="f.rank.focus_boost" class="chip focus">
                  +{{ f.rank.focus_boost }}
                </span>
                <span v-if="f.rank.test_penalty" class="chip test">
                  test {{ f.rank.test_penalty }}
                </span>
              </td>
              <td class="num">{{ f.symbols?.length || 0 }}</td>
            </tr>
            <tr v-if="expanded.has(f.path)" class="syms-row">
              <td></td>
              <td colspan="4">
                <div class="syms">
                  <div
                    v-for="s in f.symbols"
                    :key="s.name + '-' + (s.line || 0)"
                    class="sym"
                  >
                    <span class="kind">{{ s.kind }}</span>
                    <span class="name">{{ s.sig || s.name }}</span>
                  </div>
                  <div v-if="!f.symbols?.length" class="hint">
                    {{ t("repomap.noSyms") }}
                  </div>
                </div>
              </td>
            </tr>
          </template>
        </tbody>
      </table>

      <div class="repomap-text">
        <button type="button" class="linkish" @click="showText = !showText">
          {{ showText ? t("repomap.hideText") : t("repomap.showText") }}
        </button>
        <pre v-if="showText" class="repomap-pre">{{ data?.text }}</pre>
      </div>
    </div>
  </section>
</template>
