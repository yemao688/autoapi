<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/bridge'
import type { model } from '../../wailsjs/go/models'

const { t, locale } = useI18n()

type MonitorStatus = 'available' | 'empty' | 'error' | 'checking'
const STORAGE_KEY = 'autoapi.upstream-monitoring.v1'
const STORAGE_VERSION = 1

interface PersistedMonitoringState {
  version: number
  selectionByKey: Record<string, boolean>
  statuses: Record<string, MonitorStatus>
  results: Record<string, model.UpstreamMonitorResult>
  summary: { total: number; available: number; empty: number; errors: number }
  completedAtMs: number
  completionMs: number
}

const targets = ref<model.UpstreamMonitorModel[]>([])
const selectedKeys = ref<Set<string>>(new Set())
const statuses = ref<Record<string, MonitorStatus>>({})
const results = ref<Record<string, model.UpstreamMonitorResult>>({})
const loading = ref(false)
const running = ref(false)
const loadError = ref('')
const runError = ref('')
const expandedKey = ref('')
const completedAtMs = ref(0)
const completionMs = ref(0)
const summary = ref({ total: 0, available: 0, empty: 0, errors: 0 })
let savedSelectionByKey: Record<string, boolean> | null = null

function monitorKey(row: { provider_id: string; model_name: string; protocol: string }): string {
  return `${row.provider_id}:${row.model_name}:${row.protocol}`
}

const selectedCount = computed(() => selectedKeys.value.size)
const abnormalCount = computed(() => summary.value.empty + summary.value.errors)
const hasResults = computed(() => Object.keys(results.value).length > 0)

function statusFor(target: model.UpstreamMonitorModel): MonitorStatus | '' {
  return statuses.value[monitorKey(target)] || ''
}

function resultFor(target: model.UpstreamMonitorModel): model.UpstreamMonitorResult | undefined {
  return results.value[monitorKey(target)]
}

function statusLabel(status: MonitorStatus | ''): string {
  if (!status) return '—'
  return t(`monitoring.status.${status}`)
}

function statusClass(status: MonitorStatus | ''): string {
  if (status === 'available') return 'success'
  if (status === 'empty') return 'warn'
  if (status === 'error') return 'error'
  if (status === 'checking') return 'pending'
  return ''
}

function formatMs(value: number | undefined): string {
  return value && value > 0 ? `${value} ms` : '—'
}

function formatCompletedAt(value: number): string {
  if (!value) return '—'
  return new Intl.DateTimeFormat(locale.value, {
    dateStyle: 'short',
    timeStyle: 'medium',
  }).format(new Date(value))
}

function protocolLabel(protocol: string): string {
  const labels: Record<string, string> = {
    chat: 'OpenAI Chat',
    responses: 'Responses',
    messages: 'Messages',
    gemini: 'Gemini',
  }
  return labels[protocol] || protocol
}

function persistState() {
  if (typeof localStorage === 'undefined') return
  const payload: PersistedMonitoringState = {
    version: STORAGE_VERSION,
    selectionByKey: Object.fromEntries(targets.value.map((target) => [monitorKey(target), selectedKeys.value.has(monitorKey(target))])),
    statuses: statuses.value,
    results: results.value,
    summary: summary.value,
    completedAtMs: completedAtMs.value,
    completionMs: completionMs.value,
  }
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(payload))
  } catch {
    // Local persistence is best effort; monitoring remains usable if storage is unavailable.
  }
}

function restoreState() {
  if (typeof localStorage === 'undefined') return
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return
    const parsed = JSON.parse(raw) as Partial<PersistedMonitoringState>
    if (parsed.version !== STORAGE_VERSION) return
    savedSelectionByKey = parsed.selectionByKey || {}
    statuses.value = parsed.statuses || {}
    results.value = parsed.results || {}
    summary.value = parsed.summary || summary.value
    completedAtMs.value = parsed.completedAtMs || 0
    completionMs.value = parsed.completionMs || 0
  } catch {
    savedSelectionByKey = null
  }
}

function keepCurrentKeys<T>(source: Record<string, T>, keys: Set<string>): Record<string, T> {
  return Object.fromEntries(Object.entries(source).filter(([key]) => keys.has(key))) as Record<string, T>
}

async function loadTargets() {
  loading.value = true
  loadError.value = ''
  try {
    const nextTargets = await api.listUpstreamMonitorModels()
    const previousSelection = savedSelectionByKey || Object.fromEntries(
      targets.value.map((target) => [monitorKey(target), selectedKeys.value.has(monitorKey(target))])
    )
    targets.value = nextTargets
    const currentKeys = new Set(nextTargets.map((target) => monitorKey(target)))
    const nextSelection = nextTargets.map((target) => {
      const key = monitorKey(target)
      return [key, key in previousSelection ? previousSelection[key] : true] as const
    })
    selectedKeys.value = new Set(nextSelection.filter(([, selected]) => selected).map(([key]) => key))
    statuses.value = keepCurrentKeys(statuses.value, currentKeys)
    results.value = keepCurrentKeys(results.value, currentKeys)
    savedSelectionByKey = Object.fromEntries(nextSelection)
    persistState()
  } catch (error: any) {
    loadError.value = error?.message || String(error)
  } finally {
    loading.value = false
  }
}

function toggleSelected(target: model.UpstreamMonitorModel) {
  const key = monitorKey(target)
  const next = new Set(selectedKeys.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  selectedKeys.value = next
  savedSelectionByKey = {
    ...(savedSelectionByKey || {}),
    [key]: next.has(key),
  }
  persistState()
}

function failureResult(target: model.UpstreamMonitorModel, error: unknown): model.UpstreamMonitorResult {
  const message = error instanceof Error ? error.message : String(error)
  return {
    provider_id: target.provider_id,
    model_name: target.model_name,
    protocol: target.protocol,
    status: 'error',
    detail: message,
    error: message,
    response: '',
    first_byte_latency_ms: 0,
    total_latency_ms: 0,
    http_status: 0,
  }
}

async function runCheck() {
  if (running.value || selectedKeys.value.size === 0) return
  running.value = true
  runError.value = ''
  expandedKey.value = ''

  const selected = targets.value.filter((target) => selectedKeys.value.has(monitorKey(target)))
  const nextStatuses: Record<string, MonitorStatus> = { ...statuses.value }
  const nextResults = { ...results.value }
  for (const target of selected) {
    const key = monitorKey(target)
    nextStatuses[key] = 'checking'
    delete nextResults[key]
  }
  statuses.value = nextStatuses
  results.value = nextResults
  persistState()

  const runStartedAt = Date.now()
  await Promise.allSettled(selected.map(async (target) => {
    const key = monitorKey(target)
    const selection = {
      provider_id: target.provider_id,
      model_name: target.model_name,
      protocol: target.protocol,
    }
    try {
      const result = await api.probeUpstreamMonitorModel(selection)
      results.value = { ...results.value, [key]: result }
      statuses.value = { ...statuses.value, [key]: result.status as MonitorStatus }
    } catch (error) {
      const result = failureResult(target, error)
      results.value = { ...results.value, [key]: result }
      statuses.value = { ...statuses.value, [key]: 'error' }
      runError.value = t('monitoring.runPartial')
    }
    persistState()
  }))

  const selectedKeysForRun = new Set(selected.map((target) => monitorKey(target)))
  const settledResults = selected.map((target) => results.value[monitorKey(target)]).filter(Boolean) as model.UpstreamMonitorResult[]
  summary.value = {
    total: selected.length,
    available: settledResults.filter((result) => result.status === 'available').length,
    empty: settledResults.filter((result) => result.status === 'empty').length,
    errors: settledResults.filter((result) => result.status === 'error').length,
  }
  completedAtMs.value = Date.now()
  completionMs.value = completedAtMs.value - runStartedAt
  statuses.value = Object.fromEntries(Object.entries(statuses.value).map(([key, status]) => [
    key,
    selectedKeysForRun.has(key) && status === 'checking' ? 'error' : status,
  ])) as Record<string, MonitorStatus>
  persistState()
  running.value = false
}

function toggleDetails(target: model.UpstreamMonitorModel) {
  const key = monitorKey(target)
  expandedKey.value = expandedKey.value === key ? '' : key
}

onMounted(() => {
  restoreState()
  void loadTargets()
})
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">{{ t('monitoring.title') }}</h1>
      <span class="main-subtitle">{{ t('monitoring.subtitle') }}</span>
    </div>
    <div class="main-actions">
      <button
        class="btn btn-primary"
        type="button"
        :disabled="running || selectedCount === 0 || loading"
        :aria-busy="running"
        @click="runCheck"
      >
        <svg v-if="!running" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 12a9 9 0 1 0 3-6.7"/><path d="M3 4v5h5"/><path d="M12 7v5l3 2"/></svg>
        <span v-else class="monitor-spinner" aria-hidden="true"></span>
        {{ running ? t('monitoring.running') : t('monitoring.run') }}
      </button>
    </div>
  </header>

  <div class="main-content monitoring-content">
    <div class="main-content-inner stack-loose">
      <div class="stat-grid monitoring-summary" aria-live="polite">
        <div class="stat-card">
          <div class="stat-label">{{ t('monitoring.summary.total') }}</div>
          <div class="stat-value">{{ summary.total || targets.length }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('monitoring.summary.completed') }}</div>
          <div class="monitoring-time">{{ formatCompletedAt(completedAtMs) }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('monitoring.summary.duration') }}</div>
          <div class="stat-value">{{ formatMs(completionMs) }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('monitoring.summary.available') }}</div>
          <div class="stat-value monitoring-positive">{{ summary.available }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('monitoring.summary.abnormal') }}</div>
          <div class="stat-value" :class="{ 'monitoring-negative': abnormalCount > 0 }">{{ abnormalCount }}</div>
        </div>
      </div>

      <div v-if="loadError" class="monitoring-state monitoring-error" role="alert">
        <strong>{{ t('monitoring.loadFailed') }}</strong>
        <span>{{ loadError }}</span>
        <button class="btn btn-secondary" type="button" @click="loadTargets">{{ t('monitoring.retry') }}</button>
      </div>
      <div v-else-if="loading" class="monitoring-state" aria-live="polite">{{ t('monitoring.loading') }}</div>
      <div v-else-if="!targets.length" class="monitoring-state">{{ t('monitoring.empty') }}</div>
      <template v-else>
        <div v-if="runError" class="monitoring-state monitoring-error" role="alert">
          <strong>{{ t('monitoring.runFailed') }}</strong>
          <span>{{ runError }}</span>
        </div>

        <section class="monitoring-table-shell" :aria-busy="running">
          <div class="tbl-wrap">
            <table class="tbl monitor-table">
              <thead>
                <tr>
                  <th>{{ t('monitoring.columns.upstream') }}</th>
                  <th>{{ t('monitoring.columns.model') }}</th>
                  <th>{{ t('monitoring.columns.status') }}</th>
                  <th class="num">{{ t('monitoring.columns.firstToken') }}</th>
                  <th class="num">{{ t('monitoring.columns.totalTime') }}</th>
                  <th class="right">{{ t('monitoring.columns.details') }}</th>
                  <th class="right">{{ t('monitoring.columns.enabled') }}</th>
                </tr>
              </thead>
              <tbody>
                <template v-for="target in targets" :key="monitorKey(target)">
                  <tr :class="{ 'monitor-row-disabled': !selectedKeys.has(monitorKey(target)) }">
                    <td>
                      <div class="monitor-primary">{{ target.provider_name }}</div>
                      <div class="monitor-secondary">{{ protocolLabel(target.protocol) }}</div>
                    </td>
                    <td><span class="text-mono monitor-model">{{ target.model_name }}</span></td>
                    <td>
                      <span v-if="statusFor(target)" class="badge" :class="statusClass(statusFor(target))">
                        <span v-if="statusFor(target) !== 'checking'" class="dot" :class="statusFor(target) === 'available' ? 'green' : (statusFor(target) === 'empty' ? 'amber' : 'red')"></span>
                        <span v-else class="monitor-status-dot"></span>
                        {{ statusLabel(statusFor(target)) }}
                        <span v-if="resultFor(target)?.http_status" class="monitor-http-status">{{ t('monitoring.httpStatus', { code: resultFor(target)?.http_status }) }}</span>
                      </span>
                      <span v-else class="text-muted">—</span>
                    </td>
                    <td class="num">{{ formatMs(resultFor(target)?.first_byte_latency_ms) }}</td>
                    <td class="num">{{ formatMs(resultFor(target)?.total_latency_ms) }}</td>
                    <td class="right">
                      <button
                        class="btn btn-icon"
                        type="button"
                        :disabled="!resultFor(target)"
                        :aria-label="expandedKey === monitorKey(target) ? t('monitoring.hideDetails') : t('monitoring.showDetails')"
                        :title="expandedKey === monitorKey(target) ? t('monitoring.hideDetails') : t('monitoring.showDetails')"
                        @click="toggleDetails(target)"
                      >
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" :class="{ 'is-expanded': expandedKey === monitorKey(target) }" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg>
                      </button>
                    </td>
                    <td class="right">
                      <label class="toggle toggle-sm" :aria-label="selectedKeys.has(monitorKey(target)) ? t('monitoring.disable') : t('monitoring.enable')">
                        <input type="checkbox" :checked="selectedKeys.has(monitorKey(target))" @change="toggleSelected(target)">
                        <span class="toggle-slider blue"></span>
                      </label>
                    </td>
                  </tr>
                  <tr v-if="expandedKey === monitorKey(target)" class="monitor-detail-row">
                    <td colspan="7">
                      <div class="monitor-detail">
                        <div class="row-between monitor-detail-heading">
                          <span class="field-label">{{ t('monitoring.returnedResult') }}</span>
                          <span v-if="resultFor(target)?.detail && resultFor(target)?.detail !== resultFor(target)?.response" class="text-muted monitor-detail-error">{{ resultFor(target)?.detail }}</span>
                        </div>
                        <pre class="test-response">{{ resultFor(target)?.response || resultFor(target)?.detail || resultFor(target)?.error || t('monitoring.noResult') }}</pre>
                      </div>
                    </td>
                  </tr>
                </template>
              </tbody>
            </table>
          </div>
          <div v-if="!hasResults" class="monitoring-table-hint">{{ t('monitoring.runHint') }}</div>
        </section>
      </template>
    </div>
  </div>
</template>

<style scoped>
.monitoring-content { background: var(--surface); }
.monitoring-summary { grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); }
.monitoring-time {
  margin-top: 10px;
  color: var(--fg);
  font-family: var(--font-mono);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  line-height: 1.35;
}
.monitoring-positive { color: var(--positive); }
.monitoring-negative { color: var(--negative); }
.monitoring-state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  min-height: 140px;
  padding: 24px;
  color: var(--muted);
  text-align: center;
}
.monitoring-state.monitoring-error {
  flex-wrap: wrap;
  justify-content: flex-start;
  min-height: 0;
  padding: 14px 16px;
  border: 1px solid rgba(217, 48, 37, 0.16);
  border-radius: var(--radius-md);
  background: rgba(217, 48, 37, 0.05);
  color: var(--negative);
}
.monitoring-state.monitoring-error .btn { margin-left: auto; }
.monitoring-table-shell {
  overflow: hidden;
  border: 1px solid rgba(0, 0, 0, 0.07);
  border-radius: var(--radius-md);
  background: var(--surface);
}
.monitor-table { min-width: 920px; table-layout: fixed; }
.monitor-table th:nth-child(1) { width: 17%; }
.monitor-table th:nth-child(2) { width: 23%; }
.monitor-table th:nth-child(3) { width: 14%; }
.monitor-table th:nth-child(4),
.monitor-table th:nth-child(5) { width: 12%; }
.monitor-table th:nth-child(6) { width: 10%; }
.monitor-table th:nth-child(7) { width: 8%; }
.monitor-primary { font-weight: 500; }
.monitor-secondary { margin-top: 2px; color: var(--muted); font-size: 11px; }
.monitor-model { font-size: 12.5px; }
.monitor-http-status {
  margin-left: 3px;
  font-family: var(--font-mono);
  font-size: 10px;
  font-variant-numeric: tabular-nums;
  opacity: 0.78;
}
.monitor-row-disabled { opacity: 0.58; }
.monitor-detail-row:hover { background: transparent !important; }
.monitor-detail-row td { width: 100%; max-width: 0; padding: 0 14px 14px !important; }
.monitor-detail {
  width: 100%;
  max-width: 100%;
  min-width: 0;
  overflow: hidden;
  padding: 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--bg);
}
.monitor-detail-heading { margin-bottom: 8px; }
.monitor-detail-error { max-width: 70%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.monitor-detail .test-response {
  width: 100%;
  max-width: 100%;
  max-height: 220px;
  min-width: 0;
  margin: 0;
  overflow: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  word-break: break-word;
}
.monitor-table .btn-icon svg { transition: transform 0.15s ease; }
.monitor-table .btn-icon svg.is-expanded { transform: rotate(180deg); }
.monitoring-table-hint {
  padding: 12px 14px;
  border-top: 1px solid rgba(0, 0, 0, 0.05);
  color: var(--muted);
  font-size: 12px;
}
.monitor-spinner {
  width: 14px;
  height: 14px;
  border: 2px solid rgba(255, 255, 255, 0.45);
  border-top-color: white;
  border-radius: 50%;
  animation: monitor-spin 0.7s linear infinite;
}
.monitor-status-dot {
  width: 7px;
  height: 7px;
  border: 1.5px solid currentColor;
  border-right-color: transparent;
  border-radius: 50%;
  animation: monitor-spin 0.7s linear infinite;
}
@keyframes monitor-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) {
  .monitor-spinner, .monitor-status-dot { animation: none; }
  .monitor-table .btn-icon svg { transition: none; }
}
@media (max-width: 640px) {
  .monitoring-state.monitoring-error .btn { width: 100%; margin-left: 0; }
  .monitor-detail-error { max-width: 55%; }
}
</style>
