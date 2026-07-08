<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { model } from '../../wailsjs/go/models'
import { api } from '@/api/client'
import { useApi } from '@/composables/useApi'
import { useExportDownload } from '@/composables/useExportDownload'
import { useRelativeTime } from '@/composables/useRelativeTime'
import { useToast } from '@/composables/useToast'
import { useConfirm } from '@/composables/useConfirm'
import { useTabKeyboard } from '@/composables/useTabKeyboard'
import type { ProviderOption } from '@/types/usage'
import { EventsOff, EventsOn } from '../../wailsjs/runtime/runtime'
import TokensPane from '@/components/usage/TokensPane.vue'
import LogsPane from '@/components/usage/LogsPane.vue'
import LogFilters, { type DateRangePreset, type RouteOption } from '@/components/usage/LogFilters.vue'

useRelativeTime()
const { download } = useExportDownload()
const toast = useToast()
const confirm = useConfirm()

const { data: usageData, loading, execute: fetchUsage } = useApi(api.usageStats)

const activePane = ref<'logs' | 'tokens'>('logs')
const liveSync = ref(false)
let liveTimer: ReturnType<typeof setInterval> | null = null

const selectedProviderId = ref('')
const statusFilter = ref('全部')
const statusMap: Record<string, string> = {
  '全部': '',
  '成功': 'success',
  '失败': 'failed',
  '限流': 'rate_limited',
}

const selectedRouteId = ref('')
const modelFilter = ref('')
const searchText = ref('')

const dateRangePreset = ref<DateRangePreset>('month')
const customStart = ref('') // ISO local datetime string from <input type="datetime-local">
const customEnd = ref('')

const logs = ref<model.RequestLog[]>([])
const logPage = ref(1)
const logPageSize = ref(50)
const logTotal = ref(0)
const chartData = ref<model.UsageTrends>(new model.UsageTrends({
  range: '',
  bucket_size: 'day',
  buckets: [],
}))

const providerOptions = computed<ProviderOption[]>(() => {
  const list = usageData.value?.providers || []
  return [{ name: '全部', id: '' }, ...list.map(p => ({ name: p.provider_name, id: p.provider_id }))]
})

const { data: routesData, execute: fetchRoutes } = useApi(api.routes)
const routeOptions = computed<RouteOption[]>(() => {
  const list = routesData.value || []
  return [{ name: '全部', id: '' }, ...list.map(r => ({ name: r.name || r.id, id: r.id }))]
})

const tokenStats = computed(() => (usageData.value?.token_stats || []).slice(0, 4))
const logStats = computed(() => (usageData.value?.log_stats || []).slice(0, 4))
const providerShares = computed(() => usageData.value?.providers || [])
const modelRanking = computed(() => (usageData.value?.model_ranking || []).slice(0, 5))
// Badge on the "Token 用量" tab: show the total token count rather than the
// cost stat (token_stats[2] is "Estimated Cost" since the backend reordered
// the KPI cards to Total Requests / Total Tokens / Estimated Cost).
const totalTokenValue = computed(() => {
  const stat = tokenStats.value.find((s) => s.label === 'Total Tokens')
  if (stat?.value) return stat.value
  // Fallback: derive from provider shares if the stat isn't present yet
  // (e.g. before the first refresh completes).
  if (providerShares.value.length > 0) {
    const total = providerShares.value.reduce((sum, p) => sum + p.tokens, 0)
    return total > 0 ? total.toLocaleString() : '—'
  }
  return '—'
})
const logTotalValue = computed(() => usageData.value?.log_total || 0)

// totalPages is used to bound `goToPage` callers and to gate hasNextPage.
const totalPages = computed(() => {
  if (logTotal.value <= 0 || logPageSize.value <= 0) return 0
  return Math.max(1, Math.ceil(logTotal.value / logPageSize.value))
})
const hasNextPage = computed(() => logPage.value < totalPages.value)
const hasPrevPage = computed(() => logPage.value > 1)

const safePage = computed(() => {
  if (logPage.value < 1) return 1
  if (logPage.value > totalPages.value) return Math.max(1, totalPages.value)
  return logPage.value
})

function customRangeToMs(iso: string): number {
  if (!iso) return 0
  const t = new Date(iso).getTime()
  return Number.isFinite(t) ? t : 0
}

const selectedRange = computed<{ start_date: number; end_date: number }>(() => {
  const now = Date.now()
  switch (dateRangePreset.value) {
    case 'today': {
      const d = new Date()
      d.setHours(0, 0, 0, 0)
      return { start_date: d.getTime(), end_date: now }
    }
    case 'day': {
      return { start_date: now - 24 * 3600 * 1000, end_date: now }
    }
    case 'week': {
      return { start_date: now - 7 * 24 * 3600 * 1000, end_date: now }
    }
    case 'month': {
      const d = new Date()
      return {
        start_date: new Date(d.getFullYear(), d.getMonth(), 1).getTime(),
        end_date: now,
      }
    }
    case 'custom': {
      const start = customRangeToMs(customStart.value)
      const end = customRangeToMs(customEnd.value) || now
      // If user only set start, default end to now. If only end, default
      // start to 0 (no lower bound) to avoid silently dropping everything.
      return {
        start_date: start > 0 ? start : 0,
        end_date: end,
      }
    }
    default:
      return { start_date: 0, end_date: now }
  }
})

async function queryLogs() {
  const { start_date, end_date } = selectedRange.value
  const provider = selectedProviderId.value
  const route_id = selectedRouteId.value
  const modelName = modelFilter.value.trim()
  const search = searchText.value.trim()
  const status = statusMap[statusFilter.value] || ''
  try {
    const result = await api.queryLogs({
      start_date,
      end_date,
      provider,
      route_id,
      model: modelName,
      search,
      status,
      page: logPage.value,
      page_size: logPageSize.value,
    })
    logs.value = result?.logs || []
    logTotal.value = result?.total || 0
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  }
}

async function loadCharts() {
  const { start_date, end_date } = selectedRange.value
  const provider = selectedProviderId.value
  const route_id = selectedRouteId.value
  const modelName = modelFilter.value.trim()
  const search = searchText.value.trim()
  try {
    const result = await api.usageTrends({
      start_date,
      end_date,
      provider,
      route_id,
      model: modelName,
      search,
    })
    chartData.value = result || new model.UsageTrends({
      range: '',
      bucket_size: 'day',
      buckets: [],
    })
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  }
}

async function refreshAll() {
  await fetchUsage().catch((e) => toast.push(e?.message || String(e), 'error'))
  await queryLogs()
  await loadCharts()
}

function startLive() {
  if (liveTimer) return
  liveTimer = setInterval(() => {
    void refreshAll()
  }, 2000)
}

function stopLive() {
  if (liveTimer) {
    clearInterval(liveTimer)
    liveTimer = null
  }
}

function toggleLive() {
  liveSync.value = !liveSync.value
  if (liveSync.value) startLive()
  else stopLive()
}

function switchPane(paneId: 'logs' | 'tokens') {
  activePane.value = paneId
  liveSync.value = false
  stopLive()
  if (paneId === 'logs') {
    void queryLogs()
  }
}

async function applyFilters() {
  logPage.value = 1
  await queryLogs()
  await loadCharts()
}

async function clearFilters() {
  selectedProviderId.value = ''
  statusFilter.value = '全部'
  selectedRouteId.value = ''
  modelFilter.value = ''
  searchText.value = ''
  logPage.value = 1
  await queryLogs()
  await loadCharts()
}

async function purgeLogs() {
  const ok = await confirm.open({
    title: '清理历史日志',
    message: '确定清理 90 天前的请求日志？此操作不可撤销。',
    confirmText: '清理',
    danger: true,
  })
  if (!ok) return
  try {
    const deleted = await api.purgeLogs(90)
    toast.push(`已清理 ${deleted} 条日志`, 'success')
    await refreshAll()
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  }
}

async function exportLogs() {
  await download('logs_csv')
}

async function exportTokens() {
  await download('tokens_csv')
}

function goToPage(page: number) {
  if (page < 1 || page > totalPages.value) return
  if (page === logPage.value) return
  logPage.value = page
  void queryLogs()
}

function goFirstPage() {
  if (!hasPrevPage.value) return
  logPage.value = 1
  void queryLogs()
}

function goPrevPage() {
  if (!hasPrevPage.value) return
  logPage.value = safePage.value - 1
  void queryLogs()
}

function goNextPage() {
  if (!hasNextPage.value) return
  logPage.value = safePage.value + 1
  void queryLogs()
}

function goLastPage() {
  if (!hasNextPage.value) return
  logPage.value = totalPages.value
  void queryLogs()
}

const { handleKeydown: handleTabKeydown } = useTabKeyboard(
  '#usage-tab-strip',
  activePane,
  switchPane,
)

onMounted(() => {
  void refreshAll()
  void fetchRoutes().catch((e) => toast.push(e?.message || String(e), 'error'))
  EventsOn('log:new', () => {
    if (activePane.value === 'logs') {
      void queryLogs()
      void loadCharts()
    }
  })
})

onUnmounted(() => {
  stopLive()
  EventsOff('log:new')
  if (searchDebounce) clearTimeout(searchDebounce)
})

// Re-query immediately when selection or date range changes.
watch(
  [selectedProviderId, selectedRouteId, statusFilter, dateRangePreset, customStart, customEnd],
  () => {
    void applyFilters()
  },
)

// Debounce text inputs so we don't fire a Wails call on every keystroke.
let searchDebounce: ReturnType<typeof setTimeout> | null = null
watch([modelFilter, searchText], () => {
  if (searchDebounce) clearTimeout(searchDebounce)
  searchDebounce = setTimeout(() => {
    searchDebounce = null
    void applyFilters()
  }, 300)
})
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">使用统计</h1>
      <span class="main-subtitle">请求日志与 Token 用量 · 单页切换</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary" @click="exportLogs">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;" aria-hidden="true"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/></svg>
        导出
      </button>
      <button
        class="btn btn-primary"
        :class="{ active: liveSync }"
        :aria-pressed="liveSync"
        aria-label="切换实时同步"
        @click="toggleLive"
      >
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;" aria-hidden="true"><path d="M3 12a9 9 0 1 0 9-9M3 12a9 9 0 0 1 9-9M12 3v9l5 3"/></svg>
        <span class="live-label">实时同步</span>
      </button>
    </div>
  </header>

  <div class="tabs-strip" role="tablist" aria-label="使用统计视图" id="usage-tab-strip" @keydown="handleTabKeydown">
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
      请求日志<span class="tab-meta" aria-hidden="true">{{ logTotalValue.toLocaleString() }}</span>
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
      Token 用量<span class="tab-meta" aria-hidden="true">{{ totalTokenValue }}</span>
    </button>
  </div>

  <LogFilters
    :providerOptions="providerOptions"
    :routeOptions="routeOptions"
    v-model:provider="selectedProviderId"
    v-model:status="statusFilter"
    v-model:route="selectedRouteId"
    v-model:model="modelFilter"
    v-model:search="searchText"
    v-model:dateRangePreset="dateRangePreset"
    :showStatus="activePane === 'logs'"
    @clear="clearFilters"
  />

  <div v-if="dateRangePreset === 'custom'" class="filter-bar" style="margin-top: 8px;">
    <label class="text-muted" style="font-size: 12px;">自定义起</label>
    <input
      v-model="customStart"
      type="datetime-local"
      class="input"
      style="width: auto; padding: 5px 10px; font-size: 12.5px;"
      aria-label="自定义起始时间"
    />
    <label class="text-muted" style="font-size: 12px;">自定义止</label>
    <input
      v-model="customEnd"
      type="datetime-local"
      class="input"
      style="width: auto; padding: 5px 10px; font-size: 12.5px;"
      aria-label="自定义结束时间"
    />
  </div>

  <div class="main-content">
    <div v-if="loading && !usageData" class="loading-overlay">加载中…</div>
    <div class="main-content-inner stack-loose">

      <!-- ================== TOKENS VIEW ================== -->
      <TokensPane
        v-show="activePane === 'tokens'"
        :tokenStats="tokenStats"
        :modelRanking="modelRanking"
        :providerShares="providerShares"
        :chartData="chartData"
      />

      <!-- ================== LOGS VIEW ================== -->
      <LogsPane
        v-show="activePane === 'logs'"
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
.loading-overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(245, 245, 247, 0.78);
  backdrop-filter: blur(2px);
  z-index: 10;
  font-size: 14px;
  color: var(--muted, #6e6e73);
}
</style>