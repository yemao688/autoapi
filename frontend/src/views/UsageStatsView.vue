<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import type { model } from '../../wailsjs/go/models'
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
import LogFilters from '@/components/usage/LogFilters.vue'

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
  '失败': 'error',
  '限流': 'rate_limited',
}

const dateRangePreset = ref<'month' | 'today' | 'week' | 'day'>('month')

const logs = ref<model.RequestLog[]>([])
const logPage = ref(1)
const logPageSize = ref(50)
const logTotal = ref(0)

const providerOptions = computed<ProviderOption[]>(() => {
  const list = usageData.value?.providers || []
  return [{ name: '全部', id: '' }, ...list.map(p => ({ name: p.provider_name, id: p.provider_id }))]
})

const tokenStats = computed(() => (usageData.value?.token_stats || []).slice(0, 4))
const logStats = computed(() => (usageData.value?.log_stats || []).slice(0, 4))
const providerShares = computed(() => usageData.value?.providers || [])
const modelRanking = computed(() => (usageData.value?.model_ranking || []).slice(0, 5))
const totalTokens = computed(() =>
  providerShares.value.reduce((sum, p) => sum + p.tokens, 0)
)
const totalTokenValue = computed(() => {
  const stat = tokenStats.value.find((s) => s.label.includes('本月'))
  return stat?.value || usageData.value?.token_stats?.[2]?.value || '—'
})
const logTotalValue = computed(() => usageData.value?.log_total || 0)

const selectedRange = computed<{ start_date: number; end_date: number }>(() => {
  if (dateRangePreset.value === 'month') {
    const now = new Date()
    const start = new Date(now.getFullYear(), now.getMonth(), 1).getTime()
    return { start_date: start, end_date: Date.now() }
  }
  return { start_date: 0, end_date: Date.now() }
})

async function queryLogs() {
  const { start_date, end_date } = selectedRange.value
  const provider = selectedProviderId.value
  const status = statusMap[statusFilter.value] || ''
  try {
    const result = await api.queryLogs({
      start_date,
      end_date,
      provider,
      status,
      page: logPage.value,
      page_size: logPageSize.value,
    })
    logs.value = result || []
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  }
}

async function refreshAll() {
  await fetchUsage().catch((e) => toast.push(e?.message || String(e), 'error'))
  if (usageData.value) {
    logs.value = usageData.value.logs || []
    logTotal.value = usageData.value.log_total || 0
  }
  await queryLogs()
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
  await refreshAll()
}

async function clearFilters() {
  selectedProviderId.value = ''
  statusFilter.value = '全部'
  logPage.value = 1
  await refreshAll()
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

function goPrevPage() {
  if (logPage.value > 1) {
    logPage.value--
    void queryLogs()
  }
}

function goNextPage() {
  const next = logPage.value + 1
  const wouldHaveFullPage = logs.value.length === logPageSize.value
  if (wouldHaveFullPage) {
    logPage.value = next
    void queryLogs()
  }
}

const { handleKeydown: handleTabKeydown } = useTabKeyboard(
  '#usage-tab-strip',
  activePane,
  switchPane,
)

onMounted(() => {
  void refreshAll()
  EventsOn('log:new', () => {
    // Only refresh if the user is currently viewing the logs pane.
    // This avoids unnecessary fetches when on the tokens pane.
    if (activePane.value === 'logs') {
      void queryLogs()
    }
  })
})

onUnmounted(() => {
  stopLive()
  EventsOff('log:new')
})

watch([selectedProviderId, statusFilter], () => {
  void applyFilters()
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
    v-model:provider="selectedProviderId"
    v-model:status="statusFilter"
    :showStatus="activePane === 'logs'"
    @clear="clearFilters"
  />

  <div class="main-content">
    <div v-if="loading && !usageData" class="loading-overlay">加载中…</div>
    <div class="main-content-inner stack-loose">

      <!-- ================== TOKENS VIEW ================== -->
      <TokensPane
        v-show="activePane === 'tokens'"
        :tokenStats="tokenStats"
        :providerShares="providerShares"
        :modelRanking="modelRanking"
        :totalTokens="totalTokens"
      />

      <!-- ================== LOGS VIEW ================== -->
      <LogsPane
        v-show="activePane === 'logs'"
        :logs="logs"
        :logStats="logStats"
        :logTotal="logTotal"
        :logPage="logPage"
        :logPageSize="logPageSize"
         @prev="goPrevPage"
         @next="goNextPage"
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