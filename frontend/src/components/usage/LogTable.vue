<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { model } from '../../../wailsjs/go/models'

import { useProviderStyle } from '@/composables/useProviderStyle'

const { t } = useI18n()

interface Props {
  logs: model.RequestLog[]
}
const props = defineProps<Props>()

const emit = defineEmits<{
  (e: 'clearFilters'): void
}>()

const { color: providerColor, initial: providerInitial, textColor: providerTextColor } = useProviderStyle()

// expandedRows tracks which log rows are currently showing the detail
// panel. A Set keyed by log.id avoids O(N) scans on toggle. The detail
// panel only renders for expanded rows so memory cost is minimal.
const expandedRows = ref<Set<string>>(new Set())

function isExpanded(log: model.RequestLog): boolean {
  return expandedRows.value.has(log.id)
}

function toggleRow(log: model.RequestLog) {
  const next = new Set(expandedRows.value)
  if (next.has(log.id)) {
    next.delete(log.id)
  } else {
    next.add(log.id)
  }
  expandedRows.value = next
}

// hasChain reports whether the row's chain has more than one entry —
// single-attempt requests (the common case) get a "1 attempt" label
// only, so the retry indicator and "Tried N targets" summary are
// reserved for rows that actually show failover.
function chainLength(log: model.RequestLog): number {
  return Array.isArray(log.chain) ? log.chain.length : 0
}

// chainStatusLabel maps a ChainEntry.status string to the i18n key
// shown as a badge in the detail row. Unknown statuses fall through to
// the raw string so future server-side additions don't render blank.
function chainStatusLabel(status: string): string {
  switch (status) {
    case 'success':
      return t('usage.logTable.statusSuccess')
    case 'retryable':
      return t('usage.logTable.statusRetryable')
    case 'non_retryable':
      return t('usage.logTable.statusNonRetryable')
    case 'circuit_open':
      return t('usage.logTable.statusCircuitOpen')
    case 'preflight_error':
      return t('usage.logTable.statusPreflightError')
    case 'client_abort':
      return t('usage.logTable.statusClientAbort')
    default:
      return status
  }
}

// chainStatusClass colors the status badge in the chain timeline. The
// classes (success/warn/error/info) are the same ones used in the
// status cell on the main row so the visual language stays consistent.
function chainStatusClass(status: string): string {
  switch (status) {
    case 'success':
      return 'success'
    case 'retryable':
    case 'circuit_open':
    case 'client_abort':
      return 'warn'
    case 'non_retryable':
    case 'preflight_error':
      return 'error'
    default:
      return 'info'
  }
}

// columns is the table header count. Used by the detail row's
// :colspan so the row always spans the full width regardless of
// future column additions. Computed once because it's a literal.
const columns = 10

// showRetryIndicator is true only for rows where multiple attempts
// happened, which is the only case the user benefits from a "retried"
// hint on the main row.
function showRetryIndicator(log: model.RequestLog): boolean {
  return chainLength(log) > 1
}

function formatTime(ts: number): string {
  const d = new Date(ts)
  const mm = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  const hh = String(d.getHours()).padStart(2, '0')
  const mi = String(d.getMinutes()).padStart(2, '0')
  return `${mm}/${dd} ${hh}:${mi}`
}

function formatLatency(ms: number): string {
  if (ms <= 0) return '—'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

function statusBadgeClass(statusCode: number): string {
  if (statusCode >= 200 && statusCode < 300) return 'success'
  if (statusCode === 429) return 'warn'
  if (statusCode >= 400 || statusCode === 0) return 'error'
  return 'info'
}

function statusDotClass(statusCode: number): string {
  const cls = statusBadgeClass(statusCode)
  if (cls === 'success') return 'green'
  if (cls === 'warn') return 'amber'
  return 'red'
}

function statusText(statusCode: number): string {
  return String(statusCode)
}

// chainArray returns the chain slice with a defensive copy so a missing
// `chain` field (older rows from before migration 012) renders as an
// empty array instead of throwing. The render code uses chain.length
// directly so it stays safe.
function chainArray(log: model.RequestLog): model.RequestLogChainEntry[] {
  return Array.isArray(log.chain) ? log.chain : []
}

// computedLogCount feeds the "Tried N targets" footer; the loop below
// iterates the chain entries to render the per-attempt timeline.
const columnCount = computed(() => columns)
</script>

<template>
  <table class="tbl">
    <thead>
      <tr>
        <th>{{ t('usage.logTable.time') }}</th>
        <th>{{ t('usage.logTable.status') }}</th>
        <th>{{ t('usage.logTable.provider') }}</th>
        <th>{{ t('usage.logTable.model') }}</th>
        <th class="right">{{ t('usage.logTable.input') }}</th>
        <th class="right">{{ t('usage.logTable.output') }}</th>
        <th class="right">{{ t('usage.logTable.cost') }}</th>
        <th class="right">{{ t('usage.logTable.latencyTtft') }}</th>
        <th>{{ t('usage.logTable.route') }}</th>
        <th class="right" style="width: 56px;"></th>
      </tr>
    </thead>
    <tbody>
      <template v-for="log in props.logs" :key="log.id">
        <tr
          class="log-row"
          :class="{ 'log-row-expanded': isExpanded(log) }"
          :aria-expanded="isExpanded(log)"
          :tabindex="0"
          role="button"
          @click="toggleRow(log)"
          @keydown.enter.prevent="toggleRow(log)"
          @keydown.space.prevent="toggleRow(log)"
        >
          <td><span class="text-mono" style="font-size: 12.5px;">{{ formatTime(log.timestamp) }}</span></td>
          <td>
            <span class="badge" :class="statusBadgeClass(log.status_code)">
              <span :class="'dot ' + statusDotClass(log.status_code)"></span>{{ statusText(log.status_code) }}
            </span>
            <span
              v-if="showRetryIndicator(log)"
              class="retry-indicator"
              :title="t('usage.logTable.triedTargets', { n: chainLength(log) })"
              aria-hidden="true"
            >↻</span>
          </td>
          <td>
            <div class="row" style="gap: 6px;">
              <div
                class="list-icon"
                :style="{
                  background: providerColor(log.provider_name),
                  color: providerTextColor(log.provider_name),
                  width: '22px',
                  height: '22px',
                  fontSize: '10px',
                  borderRadius: '5px',
                }"
              >{{ providerInitial(log.provider_name) }}</div>
              <span style="font-size: 12.5px;">{{ log.provider_name }}</span>
            </div>
          </td>
          <td>
            <span class="text-mono" style="font-size: 12.5px;">
              {{ log.model }}
              <span v-if="log.is_stream" class="text-muted" style="font-size: 10px;" :title="t('usage.logTable.streamHint')">⇄</span>
            </span>
          </td>
          <td class="num">{{ log.input_tokens > 0 ? log.input_tokens : '—' }}</td>
          <td class="num">{{ log.output_tokens > 0 ? log.output_tokens : '—' }}</td>
          <td class="num">{{ log.cost > 0 ? '$' + log.cost.toFixed(3) : '—' }}</td>
          <td class="num">
            {{ (log.latency_ms / 1000).toFixed(2) }}s
            <span v-if="log.is_stream && log.first_token_ms > 0" class="text-muted" style="font-size: 11px;">
              /{{ (log.first_token_ms / 1000).toFixed(2) }}s
            </span>
          </td>
          <td>
            <span class="badge info" style="font-size: 10px;">{{ log.route_label || t('usage.logTable.defaultRoute') }}</span>
          </td>
          <td class="right">
            <span
              v-if="!log.is_stream"
              class="non-stream-badge"
              :title="t('usage.logTable.streamHint')"
            >{{ t('usage.logTable.nonStreamBadge') }}</span>
            <span
              class="expand-chevron"
              :class="{ 'expand-chevron-open': isExpanded(log) }"
              :aria-label="isExpanded(log) ? t('usage.logTable.collapse') : t('usage.logTable.expand')"
              aria-hidden="true"
            >›</span>
          </td>
        </tr>
        <tr v-if="isExpanded(log)" class="log-detail-row">
          <td :colspan="columnCount">
            <div class="log-detail">
              <div class="log-detail-grid">
                <div class="log-detail-item">
                  <span class="log-detail-label">{{ t('usage.logTable.requestId') }}</span>
                  <span class="log-detail-value text-mono">{{ log.request_id || '—' }}</span>
                </div>
                <div class="log-detail-item">
                  <span class="log-detail-label">{{ t('usage.logTable.userAgent') }}</span>
                  <span class="log-detail-value text-mono log-detail-ua">{{ log.user_agent || '—' }}</span>
                </div>
                <div class="log-detail-item">
                  <span class="log-detail-label">{{ t('usage.logTable.clientIp') }}</span>
                  <span class="log-detail-value text-mono">{{ log.client_ip || '—' }}</span>
                </div>
              </div>
              <div v-if="chainArray(log).length > 0" class="log-detail-chain">
                <div class="log-detail-chain-header">
                  <span class="log-detail-label">{{ t('usage.logTable.chain') }}</span>
                  <span v-if="chainArray(log).length > 1" class="text-muted log-detail-tried">
                    {{ t('usage.logTable.triedTargets', { n: chainArray(log).length }) }}
                  </span>
                </div>
                <ol class="log-detail-chain-list">
                  <li v-for="entry in chainArray(log)" :key="entry.attempt_order" class="log-detail-chain-item">
                    <span class="log-detail-attempt">{{ t('usage.logTable.attempt', { n: entry.attempt_order }) }}</span>
                    <span class="log-detail-chain-provider">{{ entry.provider_name || '—' }}</span>
                    <span class="log-detail-chain-model text-mono">{{ entry.model_name || '—' }}</span>
                    <span class="badge" :class="chainStatusClass(entry.status)">{{ chainStatusLabel(entry.status) }}</span>
                    <span class="log-detail-chain-latency text-muted">{{ formatLatency(entry.latency_ms) }}</span>
                    <span v-if="entry.error" class="log-detail-chain-error text-mono">{{ entry.error }}</span>
                  </li>
                </ol>
              </div>
            </div>
          </td>
        </tr>
      </template>
      <tr v-if="props.logs.length === 0" class="logs-empty-row">
        <td colspan="10" style="padding: 56px 20px;">
          <div style="display: flex; flex-direction: column; align-items: center; gap: 10px; text-align: center;">
            <div style="width: 40px; height: 40px; border-radius: 10px; background: var(--bg); display: flex; align-items: center; justify-content: center; color: var(--muted);">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:20px;height:20px;" aria-hidden="true"><circle cx="11" cy="11" r="7"></circle><path d="m21 21-4.3-4.3"></path></svg>
            </div>
            <div style="font-size: 14px; font-weight: 500; color: var(--fg);">{{ t('usage.logTable.empty') }}</div>
            <div style="font-size: 12.5px; color: var(--muted);">{{ t('usage.logTable.emptyHint') }}</div>
            <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px; margin-top: 4px;" @click="emit('clearFilters')">{{ t('usage.logTable.clear') }}</button>
          </div>
        </td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.log-row {
  cursor: pointer;
}
.log-row:hover {
  background: var(--row-hover, rgba(0, 0, 0, 0.025));
}
.log-row:focus {
  outline: 1px solid var(--accent, #0071e3);
  outline-offset: -1px;
}
.log-row-expanded {
  background: var(--row-active, rgba(0, 0, 0, 0.04));
}
.retry-indicator {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  margin-left: 4px;
  border-radius: 50%;
  font-size: 11px;
  color: var(--muted, #6e6e73);
  background: var(--bg, rgba(0, 0, 0, 0.04));
  vertical-align: middle;
}
.non-stream-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  margin-left: 4px;
  border-radius: 50%;
  font-size: 10px;
  font-weight: 500;
  color: var(--muted, #6e6e73);
  background: var(--bg, rgba(0, 0, 0, 0.05));
  border: 1px solid var(--border, rgba(0, 0, 0, 0.08));
  vertical-align: middle;
  user-select: none;
}
.expand-chevron {
  display: inline-block;
  margin-left: 6px;
  font-size: 14px;
  line-height: 1;
  color: var(--muted, #6e6e73);
  transition: transform 0.12s ease;
  transform: rotate(90deg);
}
.expand-chevron-open {
  transform: rotate(-90deg);
}
.log-detail-row td {
  background: var(--row-detail-bg, rgba(0, 0, 0, 0.02));
  border-top: 1px solid var(--border, rgba(0, 0, 0, 0.06));
  padding: 14px 16px;
}
.log-detail {
  display: flex;
  flex-direction: column;
  gap: 12px;
  font-size: 12.5px;
}
.log-detail-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 8px 16px;
}
.log-detail-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.log-detail-label {
  font-size: 11px;
  color: var(--muted, #6e6e73);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.log-detail-value {
  font-size: 12.5px;
  color: var(--fg, #1d1d1f);
  word-break: break-all;
}
.log-detail-ua {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.log-detail-chain {
  display: flex;
  flex-direction: column;
  gap: 8px;
  border-top: 1px dashed var(--border, rgba(0, 0, 0, 0.08));
  padding-top: 12px;
}
.log-detail-chain-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.log-detail-tried {
  font-size: 11.5px;
}
.log-detail-chain-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.log-detail-chain-item {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 6px 8px;
  background: var(--bg, rgba(0, 0, 0, 0.02));
  border-radius: 6px;
  font-size: 12px;
}
.log-detail-attempt {
  color: var(--muted, #6e6e73);
  font-size: 11.5px;
  min-width: 60px;
}
.log-detail-chain-provider {
  font-weight: 500;
}
.log-detail-chain-model {
  color: var(--muted, #6e6e73);
  font-size: 11.5px;
}
.log-detail-chain-latency {
  font-size: 11.5px;
}
.log-detail-chain-error {
  flex-basis: 100%;
  font-size: 11px;
  color: var(--muted, #6e6e73);
  word-break: break-all;
}
</style>
