<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { model } from '../../wailsjs/go/models'
import { api } from '@/api/bridge'
import { useApi } from '@/composables/useApi'
import { useExportDownload } from '@/composables/useExportDownload'
import { usePolling } from '@/composables/usePolling'
import { useRelativeTime } from '@/composables/useRelativeTime'
import { useToast } from '@/composables/useToast'
import { useCompactNumber } from '@/composables/useCompactNumber'

const { t } = useI18n()

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
const recentActivity = computed(() => dashboardData.value?.recent_activity.slice(0, 10) || [])
const providers = computed(() => dashboardData.value?.providers || [])
const health = computed(() => dashboardData.value?.service_health)
const proxyRunning = computed(() => proxyStatus.value?.running === true)

const providerColors: Record<string, string> = {
  openai: '#10a37f',
  anthropic: '#d97757',
  deepseek: '#272729',
  moonshot: '#0071e3',
  '智谱 glm': '#2563eb',
  glm: '#2563eb',
}

function providerColor(p: model.Provider): string {
  const key = p.name.toLowerCase()
  return providerColors[key] || (p.status === 'connected' ? '#0071e3' : '#6e6e73')
}

function providerInitial(p: model.Provider): string {
  const name = p.name
  const code = name.match(/[\u4e00-\u9fa5]/)
    ? name[name.length - 1]
    : name.trim().charAt(0).toUpperCase()
  return code || name.charAt(0).toUpperCase()
}

function formatUptime(seconds: number): string {
  if (!seconds) return t('dashboard.uptime.zero')
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const mins = Math.floor((seconds % 3600) / 60)
  if (days > 0) return t('dashboard.uptime.days', { days, hours, mins })
  if (hours > 0) return t('dashboard.uptime.hours', { hours, mins })
  return t('dashboard.uptime.minutes', { mins })
}

const { format: relativeFormat } = useRelativeTime()

function formatTime(ts: number): string {
  return relativeFormat(ts)
}

function formatCost(c: number): string {
  if (c >= 1) return '$' + c.toFixed(2)
  if (c >= 0.01) return '$' + c.toFixed(3)
  return '$' + c.toFixed(4)
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
            <template v-if="stat.label === 'dashboard.stats.activeProviders'">
              <span :class="proxyRunning ? 'dot green' : 'dot red'"></span>
              <span>{{ health ? formatUptime(health.uptime_seconds) : '—' }}</span>
            </template>
            <template v-else>
              <span class="delta" :class="stat.trend">{{ stat.delta }}</span>
              <span>{{ stat.note }}</span>
            </template>
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
          <svg class="chart-svg" viewBox="0 0 800 220" preserveAspectRatio="none" role="img" :aria-label="t('dashboard.chart.ariaLabel')">
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
              <line x1="40" y1="40" x2="780" y2="40"/>
              <line x1="40" y1="80" x2="780" y2="80"/>
              <line x1="40" y1="120" x2="780" y2="120"/>
              <line x1="40" y1="160" x2="780" y2="160"/>
              <line x1="40" y1="200" x2="780" y2="200"/>
            </g>
            <g font-family="SF Mono, monospace" font-size="10" fill="#6e6e73" text-anchor="end">
              <text x="34" y="44">600K</text>
              <text x="34" y="84">450K</text>
              <text x="34" y="124">300K</text>
              <text x="34" y="164">150K</text>
              <text x="34" y="204">0</text>
            </g>

            <path d="M 80,148 L 188,128 L 296,138 L 404,82 L 512,46 L 620,98 L 728,146 L 728,200 L 80,200 Z" fill="url(#areaInput)"/>
            <path d="M 80,148 L 188,128 L 296,138 L 404,82 L 512,46 L 620,98 L 728,146" fill="none" stroke="#0071e3" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>

            <path d="M 80,178 L 188,168 L 296,170 L 404,148 L 512,128 L 620,156 L 728,176 L 728,200 L 80,200 Z" fill="url(#areaOutput)"/>
            <path d="M 80,178 L 188,168 L 296,170 L 404,148 L 512,128 L 620,156 L 728,176" fill="none" stroke="rgba(0,113,227,0.5)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" stroke-dasharray="3 2"/>

            <g>
              <circle cx="80" cy="148" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"/>
              <circle cx="188" cy="128" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"/>
              <circle cx="296" cy="138" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"/>
              <circle cx="404" cy="82" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"/>
              <circle cx="512" cy="46" r="4" fill="#0071e3" stroke="#fff" stroke-width="2"/>
              <circle cx="620" cy="98" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"/>
              <circle cx="728" cy="146" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"/>
            </g>

            <g>
              <line x1="512" y1="46" x2="512" y2="14" stroke="rgba(0,113,227,0.4)" stroke-width="1" stroke-dasharray="2 2"/>
              <rect x="470" y="0" width="84" height="22" rx="11" fill="#0071e3"/>
              <text x="512" y="15" text-anchor="middle" font-family="SF Mono, monospace" font-size="11" font-weight="500" fill="#fff">{{ t('dashboard.chart.highlight') }}</text>
            </g>

            <g font-family="SF Pro Text, sans-serif" font-size="11" fill="#6e6e73" text-anchor="middle">
              <text x="80" y="218">{{ t('dashboard.chart.weekday.mon') }}</text>
              <text x="188" y="218">{{ t('dashboard.chart.weekday.tue') }}</text>
              <text x="296" y="218">{{ t('dashboard.chart.weekday.wed') }}</text>
              <text x="404" y="218">{{ t('dashboard.chart.weekday.thu') }}</text>
              <text x="512" y="218">{{ t('dashboard.chart.weekday.fri') }}</text>
              <text x="620" y="218">{{ t('dashboard.chart.weekday.sat') }}</text>
              <text x="728" y="218">{{ t('dashboard.chart.weekday.today') }}</text>
            </g>
          </svg>
        </div>
      </section>

      <!-- Two columns: providers + activity -->
      <section class="col-2-3-7">
        <div class="card">
          <div class="card-title">
            <span>{{ t('dashboard.providersCard') }}</span>
            <RouterLink class="card-title-link" to="/providers">{{ t('dashboard.manage') }}</RouterLink>
          </div>
          <div class="stack-tight">
            <div
              v-for="p in providers"
              :key="p.id"
              class="list-row"
              style="padding: 10px 0;"
            >
              <div
                class="list-icon"
                :style="{ background: providerColor(p), color: providerColor(p) === '#272729' ? 'rgba(255,255,255,0.86)' : '#fff' }"
              >{{ providerInitial(p) }}</div>
              <div class="list-main">
                <div class="list-title">{{ p.name }}</div>
                <div class="list-sub">
                   <span v-if="p.status !== 'connected'" class="dot red" style="margin-right: 4px;"></span>
                   {{ p.status === 'connected' ? t('dashboard.modelCount', { count: p.models_count }) : (p.error_message || t('dashboard.notConnected')) }}
                </div>
              </div>
              <div class="list-meta">{{ p.monthly_tokens ? compact(p.monthly_tokens) : '—' }}</div>
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
</style>
