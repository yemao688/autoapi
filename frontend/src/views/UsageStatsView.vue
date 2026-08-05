<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { model } from "../../wailsjs/go/models";
import { api } from "@/api/bridge";
import { useApi } from "@/composables/useApi";
import { useExportDownload } from "@/composables/useExportDownload";
import { useToast } from "@/composables/useToast";
import { useConfirm } from "@/composables/useConfirm";
import { useTabKeyboard } from "@/composables/useTabKeyboard";
import { useCompactNumber } from "@/composables/useCompactNumber";
import { useAppVisibility } from "@/composables/useAppVisibility";
import type { ProviderOption } from "@/types/usage";
import { EventsOff, EventsOn } from "../../wailsjs/runtime/runtime";
import TokensPane from "@/components/usage/TokensPane.vue";
import LogsPane from "@/components/usage/LogsPane.vue";
import LogFilters, {
  type DateRangePreset,
  type RouteOption,
} from "@/components/usage/LogFilters.vue";

const { t } = useI18n();

const { download } = useExportDownload();
const toast = useToast();
const confirm = useConfirm();
const { format: compact } = useCompactNumber();

// usageFilter is the current filter snapshot passed to usageStats. It
// is updated by buildLogQuery() before each fetch so the KPI cards,
// provider shares, and model ranking stay in sync with the filter bar.
const usageFilter = ref<model.LogQuery | null>(null);

const {
  data: usageData,
  loading,
  execute: fetchUsage,
} = useApi(() => api.usageStats(usageFilter.value || undefined));

const activePane = ref<"logs" | "tokens">("logs");
const { isVisible } = useAppVisibility();
// isRefreshing guards against overlapping refreshes when multiple realtime
// events land in quick succession.
const isRefreshing = ref(false);
// Wails event unsubscribe handle.
let eventsOff: (() => void) | null = null;
let refreshTimeout: ReturnType<typeof setTimeout> | null = null;
let realtimeRefreshTimer: ReturnType<typeof setTimeout> | null = null;
let lastRefreshStartedAt = 0;
let pendingRefresh = false;
const REALTIME_MIN_REFRESH_MS = 2000;

const selectedProviderId = ref("");
const selectedApiKeyId = ref("");
// Use stable internal keys for the v-model so changing the active locale does
// not break the selected option (the dropdown text is translated, the value
// stays the same).
type StatusFilterKey = "all" | "success" | "failed" | "rate_limited";
const statusFilter = ref<StatusFilterKey>("all");
const statusMap: Record<StatusFilterKey, string> = {
  all: "",
  success: "success",
  failed: "failed",
  rate_limited: "rate_limited",
};

const selectedRouteId = ref("");
const modelFilter = ref("");
const searchText = ref("");

const dateRangePreset = ref<DateRangePreset>("today");
const customStart = ref(""); // ISO local datetime string from <input type="datetime-local">
const customEnd = ref("");

const logs = ref<model.RequestLog[]>([]);
const logPage = ref(1);
const logPageSize = ref(50);
const logTotal = ref(0);
const chartData = ref<model.UsageTrends>(
  new model.UsageTrends({
    range: "",
    bucket_size: "day",
    buckets: [],
  }),
);

// allProviders is the unfiltered list of upstream providers, loaded
// once on mount. This is intentionally separate from the filtered
// providerShares in usageData so the dropdown always shows all
// available providers regardless of the active filter.
const allProviders = ref<model.Provider[]>([]);

const providerOptions = computed<ProviderOption[]>(() => {
  return [
    { name: t("usage.status.all"), id: "" },
    ...allProviders.value.map((p) => ({ name: p.name, id: p.id })),
  ];
});

const apiKeyOptions = ref<RouteOption[]>([]);

const { data: modelRulesData, execute: fetchModelRules } = useApi(
  api.modelRules,
);
const routeOptions = computed<RouteOption[]>(() => {
  const list = modelRulesData.value || [];
  return [
    { name: t("usage.status.all"), id: "" },
    ...list.map((r) => ({ name: r.name || r.id, id: r.id })),
  ];
});

const tokenStats = computed(() =>
  (usageData.value?.token_stats || []).slice(0, 4),
);
const logStats = computed(() => usageData.value?.log_stats || []);
const providerShares = computed(() => usageData.value?.providers || []);
// modelRanking is the top-5 list for the ranking card. modelRankingFull
// is the untruncated list passed to the donut chart so it can group
// models beyond the top 7 into an "Other" slice.
const modelRanking = computed(() =>
  (usageData.value?.model_ranking || []).slice(0, 5),
);
const modelRankingFull = computed(() => usageData.value?.model_ranking || []);
// Badge on the "Token 用量" tab: show the total token count rather than the
// cost stat (token_stats[2] is "Estimated Cost" since the backend reordered
// the KPI cards to Total Requests / Total Tokens / Estimated Cost).
const totalTokenValue = computed(() => {
  const stat = tokenStats.value.find(
    (s) => s.label === "usage.stats.totalTokens",
  );
  if (stat?.value) {
    const n = Number(stat.value);
    if (Number.isFinite(n) && n > 0) return compact(n);
  }
  // Fallback: derive from provider shares if the stat isn't present yet
  // (e.g. before the first refresh completes).
  if (providerShares.value.length > 0) {
    const total = providerShares.value.reduce((sum, p) => sum + p.tokens, 0);
    return total > 0 ? compact(total) : "—";
  }
  return "—";
});
// logTotalValue is the filtered log count shown as a badge on the
// "请求日志" tab. Uses the filtered logTotal from QueryLogs, not the
// (now-removed) usageData.log_total.
const logTotalValue = computed(() => logTotal.value || 0);

// totalPages is used to bound `goToPage` callers and to gate hasNextPage.
const totalPages = computed(() => {
  if (logTotal.value <= 0 || logPageSize.value <= 0) return 0;
  return Math.max(1, Math.ceil(logTotal.value / logPageSize.value));
});
const hasNextPage = computed(() => logPage.value < totalPages.value);
const hasPrevPage = computed(() => logPage.value > 1);

const safePage = computed(() => {
  if (logPage.value < 1) return 1;
  if (logPage.value > totalPages.value) return Math.max(1, totalPages.value);
  return logPage.value;
});

function customRangeToMs(iso: string): number {
  if (!iso) return 0;
  const t = new Date(iso).getTime();
  return Number.isFinite(t) ? t : 0;
}

// getSelectedRange computes the current log query window using a live
// `Date.now()`. This is intentionally a plain function (not a computed)
// because the end bound must advance every time queryLogs/loadCharts is
// called — otherwise realtime/polling refreshes filter out freshly-written
// logs whose timestamp_ms is greater than the stale cached `end_date`.
function getSelectedRange(): { start_date: number; end_date: number } {
  const now = Date.now();
  switch (dateRangePreset.value) {
    case "today": {
      const d = new Date();
      d.setHours(0, 0, 0, 0);
      return { start_date: d.getTime(), end_date: now };
    }
    case "day": {
      return { start_date: now - 24 * 3600 * 1000, end_date: now };
    }
    case "week": {
      return { start_date: now - 7 * 24 * 3600 * 1000, end_date: now };
    }
    case "month": {
      const d = new Date();
      return {
        start_date: new Date(d.getFullYear(), d.getMonth(), 1).getTime(),
        end_date: now,
      };
    }
    case "custom": {
      const start = customRangeToMs(customStart.value);
      const end = customRangeToMs(customEnd.value) || now;
      // If user only set start, default end to now. If only end, default
      // start to 0 (no lower bound) to avoid silently dropping everything.
      return {
        start_date: start > 0 ? start : 0,
        end_date: end,
      };
    }
    default:
      return { start_date: 0, end_date: now };
  }
}

// buildLogQuery constructs a LogQuery from the current filter state.
// All three data calls (usageStats, queryLogs, usageTrends) share the
// same snapshot so cards, charts, and table always agree on the same
// filtered row set. The function reads Date.now() once per call for
// the end bound.
function buildLogQuery(): model.LogQuery {
  const { start_date, end_date } = getSelectedRange();
  return new model.LogQuery({
    start_date,
    end_date,
    provider: selectedProviderId.value,
    api_key_id: selectedApiKeyId.value,
    route_id: selectedRouteId.value,
    model: modelFilter.value.trim(),
    search: searchText.value.trim(),
    status: statusMap[statusFilter.value] || "",
    page: logPage.value,
    page_size: logPageSize.value,
  });
}

async function queryLogs() {
  const filter = buildLogQuery();
  try {
    const result = await api.queryLogsLite(filter);
    logs.value = result?.logs || [];
    logTotal.value = result?.total || 0;
  } catch (e: any) {
    toast.push(e?.message || String(e), "error");
  }
}

async function loadCharts() {
  const filter = buildLogQuery();
  try {
    const result = await api.usageTrends({
      start_date: filter.start_date,
      end_date: filter.end_date,
      provider: filter.provider,
      api_key_id: filter.api_key_id,
      route_id: filter.route_id,
      model: filter.model,
      search: filter.search,
    });
    chartData.value =
      result ||
      new model.UsageTrends({
        range: "",
        bucket_size: "day",
        buckets: [],
      });
  } catch (e: any) {
    toast.push(e?.message || String(e), "error");
  }
}

async function refreshAll() {
  // Guard against overlapping refreshes. The Wails runtime fires
  // 'log:new' once per batch flush, and during a burst of traffic the
  // second event can land before the first refreshAll's await chain
  // finishes. The flag is read+written in the same microtask so a
  // concurrent caller will see `true` and bail out.
  if (isRefreshing.value) {
    console.warn("[sync] refreshAll skipped (already refreshing)");
    return;
  }
  isRefreshing.value = true;
  lastRefreshStartedAt = Date.now();
  if (refreshTimeout) clearTimeout(refreshTimeout);
  refreshTimeout = setTimeout(() => {
    if (isRefreshing.value) {
      console.error("[sync] refreshAll timed out after 15s, resetting guard");
      isRefreshing.value = false;
      pendingRefresh = false;
    }
  }, 15000);
  try {
    const filter = buildLogQuery();
    usageFilter.value = filter;
    // Fire all three data calls in parallel using the same filter
    // snapshot. They are independent and can overlap safely.
    await Promise.all([
      fetchUsage().catch((e) =>
        toast.push(e?.message || String(e), "error"),
      ),
      (async () => {
        try {
          const result = await api.queryLogsLite(filter);
          logs.value = result?.logs || [];
          logTotal.value = result?.total || 0;
        } catch (e: any) {
          toast.push(e?.message || String(e), "error");
        }
      })(),
      (async () => {
        try {
          const result = await api.usageTrends({
            start_date: filter.start_date,
            end_date: filter.end_date,
            provider: filter.provider,
            api_key_id: filter.api_key_id,
            route_id: filter.route_id,
            model: filter.model,
            search: filter.search,
          });
          chartData.value =
            result ||
            new model.UsageTrends({
              range: "",
              bucket_size: "day",
              buckets: [],
            });
        } catch (e: any) {
          toast.push(e?.message || String(e), "error");
        }
      })(),
    ]);
  } finally {
    if (refreshTimeout) {
      clearTimeout(refreshTimeout);
      refreshTimeout = null;
    }
    isRefreshing.value = false;
    if (pendingRefresh && isVisible.value) {
      pendingRefresh = false;
      scheduleRealtimeRefresh();
    }
  }
}

function scheduleRealtimeRefresh() {
  if (realtimeRefreshTimer) return;
  const remaining = Math.max(0, REALTIME_MIN_REFRESH_MS - (Date.now() - lastRefreshStartedAt));
  realtimeRefreshTimer = setTimeout(() => {
    realtimeRefreshTimer = null;
    if (!isVisible.value) return;
    void refreshAll();
  }, remaining);
}

function stopRealtime() {
  if (eventsOff) {
    eventsOff();
    eventsOff = null;
  }
  if (realtimeRefreshTimer) {
    clearTimeout(realtimeRefreshTimer);
    realtimeRefreshTimer = null;
  }
  pendingRefresh = false;
}

function startRealtime() {
  stopRealtime();
  const off = EventsOn("log:new", () => {
    try {
      // Guard against events that were already queued when the app was
      // backgrounded before the listener is removed.
      if (!isVisible.value) {
        return;
      }
      if (
        isRefreshing.value ||
        Date.now() - lastRefreshStartedAt < REALTIME_MIN_REFRESH_MS
      ) {
        pendingRefresh = true;
        if (!isRefreshing.value) {
          pendingRefresh = false;
          scheduleRealtimeRefresh();
        }
        return;
      }
      void refreshAll();
    } catch (e: any) {
      console.warn("[sync] log:new handler error", e);
    }
  });
  eventsOff = typeof off === "function" ? off : () => EventsOff("log:new");
  // Ping the backend to verify the Wails event channel is alive.
  api.pingLogEvent().catch((e: any) => {
    console.warn("[sync] PingLogEvent failed", e);
  });
}

function switchPane(paneId: "logs" | "tokens") {
  // Initial and scheduled refreshAll calls load both datasets; avoid a
  // tab-triggered refresh to prevent redundant DOM/layer churn.
  activePane.value = paneId;
}

async function applyFilters() {
  logPage.value = 1;
  await refreshAll();
}

async function clearFilters() {
  selectedProviderId.value = "";
  selectedApiKeyId.value = "";
  statusFilter.value = "all";
  selectedRouteId.value = "";
  modelFilter.value = "";
  searchText.value = "";
  logPage.value = 1;
  await refreshAll();
}

async function purgeLogs() {
  const ok = await confirm.open({
    title: t("confirm.purgeLogsTitle"),
    message: t("confirm.purgeLogsMessage"),
    confirmText: t("confirm.purgeConfirm"),
    danger: true,
  });
  if (!ok) return;
  try {
    const deleted = await api.purgeLogs(90);
    toast.push(t("toast.logsPurged", { count: deleted }), "success");
    await refreshAll();
  } catch (e: any) {
    toast.push(e?.message || String(e), "error");
  }
}

const clearing = ref(false);
const clearAllLogs = async () => {
  const ok = await confirm.open({
    title: t("usage.clearAllTitle"),
    message: t("usage.clearAllMessage"),
    confirmText: t("usage.clearAllConfirm"),
    cancelText: t("common.cancel"),
    danger: true,
  });
  if (!ok) return;
  clearing.value = true;
  try {
    const deleted = await api.clearLogs();
    toast.push(t("usage.clearAllDone", { count: deleted }), "success");
    await refreshAll();
  } catch (e: any) {
    toast.push(e?.message || String(e), "error");
  } finally {
    clearing.value = false;
  }
};

async function exportLogs() {
  await download("logs_csv");
}

async function exportTokens() {
  await download("tokens_csv");
}

function goToPage(page: number) {
  if (page < 1 || page > totalPages.value) return;
  if (page === logPage.value) return;
  logPage.value = page;
  void queryLogs();
}

function goFirstPage() {
  if (!hasPrevPage.value) return;
  logPage.value = 1;
  void queryLogs();
}

function goPrevPage() {
  if (!hasPrevPage.value) return;
  logPage.value = safePage.value - 1;
  void queryLogs();
}

function goNextPage() {
  if (!hasNextPage.value) return;
  logPage.value = safePage.value + 1;
  void queryLogs();
}

function goLastPage() {
  if (!hasNextPage.value) return;
  logPage.value = totalPages.value;
  void queryLogs();
}

const { handleKeydown: handleTabKeydown } = useTabKeyboard(
  "#usage-tab-strip",
  activePane,
  switchPane,
);

onMounted(() => {
  // Only refresh immediately and subscribe to live events if the app window
  // is visible. When the app is backgrounded, the visibility watcher below
  // will refresh and subscribe once it becomes visible again.
  if (isVisible.value) {
    void refreshAll();
    startRealtime();
  }
  void fetchModelRules().catch((e) =>
    toast.push(e?.message || String(e), "error"),
  );
  // Load the unfiltered provider list for the dropdown options.
  api.providers().then((list) => {
    allProviders.value = list || [];
  }).catch((e) => toast.push(e?.message || String(e), "error"));
  api.apiKeys().then((list) => {
    apiKeyOptions.value = (list || []).map((key) => ({ id: key.id, name: key.name || key.id }));
  }).catch((e) => toast.push(e?.message || String(e), "error"));
});

onUnmounted(() => {
  stopRealtime();
  if (searchDebounce) clearTimeout(searchDebounce);
});

// Pause the live subscription while the app is backgrounded; resume with a
// refresh and re-subscribe to live events when it comes back to the
// foreground.
watch(isVisible, (visible) => {
  if (visible) {
    // Refresh once with current filters, then re-subscribe to live events.
    // refreshAll's isRefreshing guard prevents duplicate in-flight requests.
    void refreshAll().finally(() => {
      if (isVisible.value) {
        startRealtime();
      }
    });
  } else {
    stopRealtime();
  }
});

// Re-query immediately when selection or date range changes.
watch(
  [
    selectedProviderId,
    selectedApiKeyId,
    selectedRouteId,
    statusFilter,
    dateRangePreset,
    customStart,
    customEnd,
  ],
  () => {
    void applyFilters();
  },
);

// Debounce text inputs so we don't fire a Wails call on every keystroke.
let searchDebounce: ReturnType<typeof setTimeout> | null = null;
watch([modelFilter, searchText], () => {
  if (searchDebounce) clearTimeout(searchDebounce);
  searchDebounce = setTimeout(() => {
    searchDebounce = null;
    void applyFilters();
  }, 300);
});
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">{{ t("usage.title") }}</h1>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary" @click="exportLogs">
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.6"
          stroke-linecap="round"
          stroke-linejoin="round"
          style="width: 14px; height: 14px"
          aria-hidden="true"
        >
          <path
            d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"
          />
        </svg>
        {{ t("usage.export") }}
      </button>
      <button
        class="btn btn-danger"
        :disabled="clearing"
        @click="clearAllLogs"
      >
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.6"
          stroke-linecap="round"
          stroke-linejoin="round"
          style="width: 14px; height: 14px"
          aria-hidden="true"
        >
          <path
            d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6h14z"
          />
          <line x1="10" y1="11" x2="10" y2="17" />
          <line x1="14" y1="11" x2="14" y2="17" />
        </svg>
        {{ t("usage.clearAllConfirm") }}
      </button>
    </div>
  </header>

  <div
    class="tabs-strip"
    role="tablist"
    :aria-label="t('usage.ariaLabel')"
    id="usage-tab-strip"
    @keydown="handleTabKeydown"
  >
    <button
      class="tab"
      :class="{ active: activePane === 'logs' }"
      role="tab"
      id="usage-tab-logs"
      :aria-selected="activePane === 'logs'"
      aria-controls="usage-pane-logs"
      data-pane-id="logs"
      @click="switchPane('logs')"
    >
      {{ t("usage.tabs.logs")
      }}<span class="tab-meta" aria-hidden="true">{{
        logTotalValue.toLocaleString()
      }}</span>
    </button>
    <button
      class="tab"
      :class="{ active: activePane === 'tokens' }"
      role="tab"
      id="usage-tab-tokens"
      :aria-selected="activePane === 'tokens'"
      aria-controls="usage-pane-tokens"
      data-pane-id="tokens"
      @click="switchPane('tokens')"
    >
      {{ t("usage.tabs.tokens")
      }}<span class="tab-meta" aria-hidden="true">{{ totalTokenValue }}</span>
    </button>
  </div>

  <LogFilters
    :providerOptions="providerOptions"
    :apiKeyOptions="apiKeyOptions"
    :routeOptions="routeOptions"
    v-model:provider="selectedProviderId"
    v-model:apiKey="selectedApiKeyId"
    v-model:status="statusFilter"
    v-model:route="selectedRouteId"
    v-model:model="modelFilter"
    v-model:search="searchText"
    v-model:dateRangePreset="dateRangePreset"
    :showStatus="activePane === 'logs'"
    @clear="clearFilters"
  />

  <div
    v-if="dateRangePreset === 'custom'"
    class="filter-bar"
    style="margin-top: 8px"
  >
    <label class="text-muted" style="font-size: 12px">{{
      t("usage.custom.start")
    }}</label>
    <input
      v-model="customStart"
      type="datetime-local"
      class="input"
      style="width: auto; padding: 5px 10px; font-size: 12.5px"
      :aria-label="t('usage.custom.startAria')"
    />
    <label class="text-muted" style="font-size: 12px">{{
      t("usage.custom.end")
    }}</label>
    <input
      v-model="customEnd"
      type="datetime-local"
      class="input"
      style="width: auto; padding: 5px 10px; font-size: 12.5px"
      :aria-label="t('usage.custom.endAria')"
    />
  </div>

  <div class="main-content">
    <div v-if="loading && !usageData" class="loading-overlay">
      {{ t("usage.loading") }}
    </div>
    <div class="main-content-inner stack-loose">
      <!-- ================== TOKENS VIEW ================== -->
      <TokensPane
        v-if="activePane === 'tokens'"
        :aria-hidden="activePane !== 'tokens'"
        :active="activePane === 'tokens'"
        :tokenStats="tokenStats"
        :modelRanking="modelRanking"
        :modelRankingFull="modelRankingFull"
        :providerShares="providerShares"
      />

      <!-- ================== LOGS VIEW ================== -->
      <LogsPane
        v-if="activePane === 'logs'"
        :aria-hidden="activePane !== 'logs'"
        :active="activePane === 'logs'"
        :logs="logs"
        :logStats="logStats"
        :logTotal="logTotal"
        :logPage="logPage"
        :logPageSize="logPageSize"
        :chartData="chartData"
        @first="goFirstPage"
        @prev="goPrevPage"
        @goto="(p: number) => goToPage(p)"
        @next="goNextPage"
        @last="goLastPage"
        @clearFilters="clearFilters"
      />
    </div>
  </div>
</template>

<style scoped>
.view-pane {
  position: relative;
}

.loading-overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(245, 245, 247, 0.78);
  z-index: 10;
  font-size: 14px;
  color: var(--muted, #6e6e73);
}

.btn-danger {
  background: var(--surface);
  color: var(--negative);
  border-color: var(--border);
}

.btn-danger:hover:not(:disabled) {
  background: rgba(217, 48, 37, 0.06);
  border-color: var(--negative);
}

.btn-danger:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
