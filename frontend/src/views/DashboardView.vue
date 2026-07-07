<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import type { model } from '../../wailsjs/go/models'
import { api } from '@/api/client'
import { useApi } from '@/composables/useApi'
import { useExportDownload } from '@/composables/useExportDownload'
import { useMasterGate } from '@/composables/useMasterGate'
import { useRelativeTime } from '@/composables/useRelativeTime'
import { useToast } from '@/composables/useToast'

useRelativeTime()
const { download } = useExportDownload()
const { state: gateState } = useMasterGate()
const toast = useToast()

const { data: dashboardData, loading, execute: fetchDashboard } = useApi(api.dashboard)
const { data: proxyStatus, execute: fetchProxyStatus } = useApi(api.proxyStatus)

const initialLoading = ref(true)
let pollTimer: ReturnType<typeof setInterval> | null = null

const stats = computed(() => dashboardData.value?.stats.slice(0, 4) || [])
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

function formatNumber(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return String(n)
}

function formatUptime(seconds: number): string {
  if (!seconds) return '0 秒'
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const mins = Math.floor((seconds % 3600) / 60)
  if (days > 0) return `Uptime ${days}d ${hours}h · ${mins}m`
  if (hours > 0) return `Uptime ${hours}h ${mins}m`
  return `Uptime ${mins}m`
}

const { format: relativeFormat } = useRelativeTime()

function formatTime(ts: number): string {
  return relativeFormat(ts)
}

async function fetchAll() {
  if (gateState.value !== 'ready') return
  initialLoading.value = false
  await Promise.all([fetchDashboard(), fetchProxyStatus()]).catch((e) => {
    toast.push(e?.message || String(e), 'error')
  })
}

async function exportReport() {
  await download('all_json')
}

onMounted(() => {
  void fetchAll()
  pollTimer = setInterval(() => {
    void fetchAll()
  }, 5000)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})

watch(gateState, (s) => {
  if (s === 'ready') void fetchAll()
})
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">总览</h1>
      <span class="main-subtitle">关键指标与最近活动</span>
    </div>
    <div class="main-actions">
      <span class="badge success"><span :class="proxyRunning ? 'dot green' : 'dot red'"></span>{{ proxyRunning ? '服务运行中' : '服务已停止' }}</span>
      <button class="btn btn-secondary" @click="exportReport">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v14M5 12h14"/></svg>
        导出报告
      </button>
    </div>
  </header>

  <div class="main-content">
    <div v-if="loading && !dashboardData" class="loading-overlay">加载中…</div>
    <div class="main-content-inner stack-loose">
      <!-- Stat row -->
      <section class="stat-grid">
        <div
          v-for="(stat, idx) in stats"
          :key="stat.label + idx"
          class="stat-card"
          :class="{ dark: stat.label.includes('服务状态') }"
        >
          <div class="stat-label">{{ stat.label }}</div>
          <div class="stat-value">{{ stat.value }}</div>
          <div class="stat-meta">
            <template v-if="idx === 3">
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
            <div class="card-title" style="margin: 0;">Token 用量趋势 · 近 7 日</div>
            <div class="text-muted" style="font-size: 12px; margin-top: 4px;">按输入 / 输出 Token 拆分</div>
          </div>
          <div class="chart-legend">
            <span class="chart-legend-item"><span class="chart-legend-swatch" style="background: #0071e3;"></span>输入</span>
            <span class="chart-legend-item"><span class="chart-legend-swatch" style="background: rgba(0, 113, 227, 0.32);"></span>输出</span>
          </div>
        </div>

        <div class="chart-wrap">
          <svg class="chart-svg" viewBox="0 0 800 220" preserveAspectRatio="none" role="img" aria-label="7 日 Token 用量趋势">
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
              <text x="512" y="15" text-anchor="middle" font-family="SF Mono, monospace" font-size="11" font-weight="500" fill="#fff">532K · 周五</text>
            </g>

            <g font-family="SF Pro Text, sans-serif" font-size="11" fill="#6e6e73" text-anchor="middle">
              <text x="80" y="218">周一</text>
              <text x="188" y="218">周二</text>
              <text x="296" y="218">周三</text>
              <text x="404" y="218">周四</text>
              <text x="512" y="218">周五</text>
              <text x="620" y="218">周六</text>
              <text x="728" y="218">今天</text>
            </g>
          </svg>
        </div>
      </section>

      <!-- Two columns: providers + activity -->
      <section class="col-2-3-7">
        <div class="card">
          <div class="card-title">
            <span>Provider 状态</span>
            <RouterLink class="card-title-link" to="/providers">管理</RouterLink>
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
                   {{ p.status === 'connected' ? `${p.models_count} 模型` : (p.error_message || '未连接') }}
                </div>
              </div>
              <div class="list-meta">{{ p.monthly_tokens ? formatNumber(p.monthly_tokens) : '—' }}</div>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card-title">
            <span>最近活动</span>
            <RouterLink class="card-title-link" to="/usage-stats">查看全部</RouterLink>
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
                <div class="list-title">{{ log.provider_name }} · 输入 {{ log.input_tokens }} / 输出 {{ log.output_tokens }}</div>
              </div>
              <span class="text-mono text-muted">{{ (log.latency_ms / 1000).toFixed(1) }}s</span>
            </div>
          </div>
        </div>
      </section>

      <!-- Service health -->
      <section class="col-3">
        <div class="card">
          <div class="card-title">CPU 占用</div>
          <div class="stat-value" style="font-size: 24px;">{{ health ? health.cpu_percent.toFixed(1) : '—' }}%</div>
          <div class="stat-meta"><span class="delta positive">−1.8%</span><span>近 1 小时均值</span></div>
        </div>
        <div class="card">
          <div class="card-title">内存</div>
          <div class="stat-value" style="font-size: 24px;">{{ health ? Math.round(health.memory_mb) : '—' }} MB</div>
          <div class="stat-meta"><span>本机 · 4.2%</span></div>
        </div>
        <div class="card">
          <div class="card-title">活动连接</div>
          <div class="stat-value" style="font-size: 24px;">{{ health ? health.active_connections : '—' }}</div>
          <div class="stat-meta"><span>WebSocket {{ health ? health.websocket_count : '—' }} · HTTP {{ health ? health.http_count : '—' }}</span></div>
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
