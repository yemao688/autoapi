<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/bridge'
import { useApi } from '@/composables/useApi'
import { useExportDownload } from '@/composables/useExportDownload'
import { usePolling } from '@/composables/usePolling'
import { useRelativeTime } from '@/composables/useRelativeTime'
import { useToast } from '@/composables/useToast'
import { useCompactNumber } from '@/composables/useCompactNumber'

const { t, locale } = useI18n()

useRelativeTime()
const { download } = useExportDownload()
const toast = useToast()
const { format: compact } = useCompactNumber()

const tokenStatLabels = new Set([
  'dashboard.stats.todayTokens',
  'dashboard.stats.thisWeek',
  'dashboard.stats.thisMonth',
])

function formatStatValue(stat: { label: string; value: string }): string {
  if (tokenStatLabels.has(stat.label)) {
    const n = Number(stat.value)
    if (Number.isFinite(n)) return compact(n)
  }
  return stat.value
}

const { data: dashboardData, loading, execute: fetchDashboard } = useApi(api.dashboard)
const { data: proxyStatus, execute: fetchProxyStatus } = useApi(api.proxyStatus)

const initialLoading = ref(true)

const stats = computed(() => dashboardData.value?.stats || [])
const tokenTrend = computed(() => dashboardData.value?.token_trend || [])
const recentActivity = computed(() => dashboardData.value?.recent_activity.slice(0, 10) || [])
const modelRules = computed(() => (dashboardData.value?.model_rules || []).slice(0, 6))
const health = computed(() => dashboardData.value?.service_health)
const proxyRunning = computed(() => proxyStatus.value?.running === true)

const { format: relativeFormat } = useRelativeTime()

function formatTime(ts: number): string {
  return relativeFormat(ts)
}

function formatCost(c: number): string {
  if (c >= 1) return '$' + c.toFixed(2)
  if (c >= 0.01) return '$' + c.toFixed(3)
  return '$' + c.toFixed(4)
}

// Chart geometry
const CHART_WIDTH = 800
const CHART_HEIGHT = 220
const PAD_LEFT = 40
const PAD_RIGHT = 20
const PAD_TOP = 20
const PAD_BOTTOM = 30
const Y_TICKS = 4

const chartData = computed(() => {
  const points = tokenTrend.value
  if (points.length === 0) {
    return { empty: true }
  }

  const maxValue = points.reduce((m, p) => Math.max(m, p.input_tokens, p.output_tokens), 0)
  if (maxValue === 0) {
    return { empty: true }
  }

  const plotWidth = CHART_WIDTH - PAD_LEFT - PAD_RIGHT
  const plotHeight = CHART_HEIGHT - PAD_TOP - PAD_BOTTOM

  const xFor = (i: number) => {
    if (points.length === 1) return PAD_LEFT + plotWidth / 2
    return PAD_LEFT + (i * plotWidth) / (points.length - 1)
  }

  const yFor = (value: number) => {
    const ratio = value / maxValue
    return PAD_TOP + plotHeight - ratio * plotHeight
  }

  const mappedPoints = points.map((p, i) => ({
    date: p.date,
    x: xFor(i),
    inputY: yFor(p.input_tokens),
    outputY: yFor(p.output_tokens),
  }))

  const inputPoints = mappedPoints.map((p) => ({ x: p.x, y: p.inputY }))
  const outputPoints = mappedPoints.map((p) => ({ x: p.x, y: p.outputY }))

  const linePath = (pts: { x: number; y: number }[]) =>
    pts.map((p, i) => `${i === 0 ? 'M' : 'L'} ${p.x},${p.y}`).join(' ')

  const areaPath = (pts: { x: number; y: number }[]) => {
    if (pts.length === 0) return ''
    const bottom = PAD_TOP + plotHeight
    const top = pts.map((p) => `L ${p.x},${p.y}`).join(' ')
    return `M ${pts[0].x},${bottom} ${top} L ${pts[pts.length - 1].x},${bottom} Z`
  }

  const yTicks = Array.from({ length: Y_TICKS + 1 }, (_, i) => {
    const value = (maxValue * i) / Y_TICKS
    const y = PAD_TOP + plotHeight - (i * plotHeight) / Y_TICKS
    return { value, y }
  })

  return {
    empty: false,
    points: mappedPoints,
    inputLine: linePath(inputPoints),
    outputLine: linePath(outputPoints),
    inputArea: areaPath(inputPoints),
    outputArea: areaPath(outputPoints),
    yTicks,
  }
})

function formatChartDate(dateStr: string): string {
  const [y, m, d] = dateStr.split('-').map(Number)
  if (!y || !m || !d) return dateStr
  const date = new Date(y, m - 1, d)
  return date.toLocaleDateString(locale.value, { month: 'short', day: 'numeric' })
}

function formatSuccessRate(rate: number | undefined): string {
  if (rate === undefined || rate === null) return '—'
  return rate.toFixed(1) + '%'
}

async function fetchAll() {
  initialLoading.value = false
  await Promise.all([fetchDashboard(), fetchProxyStatus()]).catch((e) => {
    toast.push(e?.message || String(e), 'error')
  })
}

async function exportReport() {
  await download('all_json')
}

usePolling(fetchAll, 15000)
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">{{ t('dashboard.title') }}</h1>
      <span class="main-subtitle">{{ t('dashboard.subtitle') }}</span>
    </div>
    <div class="main-actions">
      <span class="badge success"><span :class="proxyRunning ? 'dot green' : 'dot red'"></span>{{ proxyRunning ? t('status.serviceRunning') : t('status.serviceStopped') }}</span>
      <button class="btn btn-secondary" @click="exportReport">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v14M5 12h14"/></svg>
        {{ t('dashboard.exportReport') }}
      </button>
    </div>
  </header>

  <div class="main-content">
    <div v-if="loading && !dashboardData" class="loading-overlay">{{ t('dashboard.loading') }}</div>
    <div class="main-content-inner stack-loose">
      <!-- Stat row -->
      <section class="stat-grid">
        <div
          v-for="(stat, idx) in stats"
          :key="stat.label + idx"
          class="stat-card"
        >
          <div class="stat-label">{{ t(stat.label) }}</div>
          <div class="stat-value">{{ formatStatValue(stat) }}</div>
          <div class="stat-meta">
            <span class="delta" :class="stat.trend">{{ stat.delta }}</span>
            <span>{{ stat.note }}</span>
          </div>
        </div>
      </section>

      <!-- Main chart -->
      <section class="card">
        <div class="row-between" style="margin-bottom: 16px;">
          <div>
            <div class="card-title" style="margin: 0;">{{ t('dashboard.chart.title') }}</div>
            <div class="text-muted" style="font-size: 12px; margin-top: 4px;">{{ t('dashboard.chart.subtitle') }}</div>
          </div>
          <div class="chart-legend">
            <span class="chart-legend-item"><span class="chart-legend-swatch" style="background: #0071e3;"></span>{{ t('dashboard.chart.inputSwatch') }}</span>
            <span class="chart-legend-item"><span class="chart-legend-swatch" style="background: rgba(0, 113, 227, 0.32);"></span>{{ t('dashboard.chart.outputSwatch') }}</span>
          </div>
        </div>

        <div class="chart-wrap">
          <div v-if="chartData.empty" class="chart-empty">{{ t('dashboard.chart.empty') }}</div>
          <svg
            v-else
            class="chart-svg"
            viewBox="0 0 800 220"
            preserveAspectRatio="none"
            role="img"
            :aria-label="t('dashboard.chart.ariaLabel')"
          >
            <defs>
              <linearGradient id="areaInput" x1="0" x2="0" y1="0" y2="1">
                <stop offset="0%" stop-color="#0071e3" stop-opacity="0.22"/>
                <stop offset="100%" stop-color="#0071e3" stop-opacity="0"/>
              </linearGradient>
              <linearGradient id="areaOutput" x1="0" x2="0" y1="0" y2="1">
                <stop offset="0%" stop-color="#0071e3" stop-opacity="0.08"/>
                <stop offset="100%" stop-color="#0071e3" stop-opacity="0"/>
              </linearGradient>
            </defs>

            <g stroke="rgba(0,0,0,0.06)" stroke-width="1">
              <line
                v-for="(tick, idx) in chartData.yTicks"
                :key="'grid-' + idx"
                x1="40"
                :y1="tick.y"
                x2="780"
                :y2="tick.y"
              />
            </g>
            <g font-family="SF Mono, monospace" font-size="10" fill="#6e6e73" text-anchor="end">
              <text
                v-for="(tick, idx) in chartData.yTicks"
                :key="'label-' + idx"
                x="34"
                :y="tick.y + 4"
              >{{ compact(tick.value) }}</text>
            </g>

            <path :d="chartData.inputArea" fill="url(#areaInput)"/>
            <path
              :d="chartData.inputLine"
              fill="none"
              stroke="#0071e3"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
            />

            <path :d="chartData.outputArea" fill="url(#areaOutput)"/>
            <path
              :d="chartData.outputLine"
              fill="none"
              stroke="rgba(0,113,227,0.5)"
              stroke-width="1.5"
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-dasharray="3 2"
            />

            <g>
              <circle
                v-for="(p, idx) in chartData.points"
                :key="'input-' + idx"
                :cx="p.x"
                :cy="p.inputY"
                r="3.5"
                fill="#fff"
                stroke="#0071e3"
                stroke-width="2"
              />
              <circle
                v-for="(p, idx) in chartData.points"
                :key="'output-' + idx"
                :cx="p.x"
                :cy="p.outputY"
                r="3.5"
                fill="#fff"
                stroke="rgba(0,113,227,0.5)"
                stroke-width="2"
              />
            </g>

            <g font-family="SF Pro Text, sans-serif" font-size="11" fill="#6e6e73" text-anchor="middle">
              <text
                v-for="(p, idx) in chartData.points"
                :key="'xlabel-' + idx"
                :x="p.x"
                y="218"
              >{{ formatChartDate(p.date) }}</text>
            </g>
          </svg>
        </div>
      </section>

      <!-- Two columns: model rules + activity -->
      <section class="col-2-3-7">
        <div class="card">
          <div class="card-title">
            <span>{{ t('dashboard.modelRulesCard') }}</span>
            <RouterLink class="card-title-link" to="/model-rules">{{ t('dashboard.manage') }}</RouterLink>
          </div>
          <div class="stack-tight">
            <div v-if="modelRules.length === 0" class="text-muted" style="font-size: 13px; padding: 12px 0;">
              {{ t('dashboard.modelRulesEmpty') }}
            </div>
            <div
              v-for="rule in modelRules"
              :key="rule.id"
              class="list-row"
              style="padding: 10px 0;"
            >
              <div class="list-main">
                <div class="list-title">
                  {{ rule.name }}
                  <span v-if="!rule.enabled" class="badge" style="margin-left: 6px; background: rgba(0,0,0,0.05); color: var(--muted);">{{ t('common.disabled') }}</span>
                </div>
                <div class="list-sub">
                  <span v-if="rule.today_request_count === 0">{{ t('modelRules.stats.todaySuccessRateNoData') }}</span>
                  <span v-else>{{ t('modelRules.stats.todaySuccessRate') }} {{ formatSuccessRate(rule.today_success_rate) }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card-title">
            <span>{{ t('dashboard.recentActivity') }}</span>
            <RouterLink class="card-title-link" to="/usage-stats">{{ t('dashboard.viewAll') }}</RouterLink>
          </div>
          <div class="stack-tight">
            <div
              v-for="log in recentActivity"
              :key="log.id"
              class="list-row"
              style="padding: 8px 0;"
            >
              <span class="text-mono text-muted" style="width: 60px;">{{ formatTime(log.timestamp) }}</span>
              <span class="badge mono">{{ log.model }}</span>
              <div class="list-main">
                <div class="list-title">{{ t('dashboard.activityLine', { provider: log.provider_name, input: log.input_tokens, output: log.output_tokens }) }}</div>
                <div class="list-sub">{{ t('dashboard.approxCost', { cost: formatCost(log.cost) }) }}</div>
              </div>
              <span class="text-mono text-muted">{{ (log.latency_ms / 1000).toFixed(1) }}s</span>
            </div>
          </div>
        </div>
      </section>

      <!-- Service health -->
      <section class="col-3">
        <div class="card">
          <div class="card-title">{{ t('dashboard.health.cpu') }}</div>
          <div class="stat-value" style="font-size: 24px;">{{ health ? health.cpu_percent.toFixed(1) : '—' }}%</div>
          <div class="stat-meta"><span class="delta positive">−1.8%</span><span>{{ t('dashboard.health.cpuSub') }}</span></div>
        </div>
        <div class="card">
          <div class="card-title">{{ t('dashboard.health.mem') }}</div>
          <div class="stat-value" style="font-size: 24px;">{{ health ? Math.round(health.memory_mb) : '—' }} MB</div>
          <div class="stat-meta"><span>{{ t('dashboard.health.memSub', { percent: '4.2%' }) }}</span></div>
        </div>
        <div class="card">
          <div class="card-title">{{ t('dashboard.health.connections') }}</div>
          <div class="stat-value" style="font-size: 24px;">{{ health ? health.active_connections : '—' }}</div>
          <div class="stat-meta"><span>{{ t('dashboard.health.connectionsSub', { ws: health ? health.websocket_count : '—', http: health ? health.http_count : '—' }) }}</span></div>
        </div>
      </section>
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

.chart-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 220px;
  color: var(--muted, #6e6e73);
  font-size: 13px;
  background: var(--bg, #f5f5f7);
  border-radius: 10px;
}
</style>
