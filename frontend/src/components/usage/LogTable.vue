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
const columns = 8

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

// hitModel finds the last successful chain entry and returns the
// provider/model that actually served the request. Returns null when
// no attempt succeeded (e.g. all retries exhausted).
function hitModel(log: model.RequestLog): { provider: string; model: string } | null {
  if (!Array.isArray(log.chain) || log.chain.length === 0) return null
  for (let i = log.chain.length - 1; i >= 0; i--) {
    if (log.chain[i].status === 'success') {
      return {
        provider: log.chain[i].provider_name || '',
        model: log.chain[i].model_name || '',
      }
    }
  }
  return null
}

const columnCount = computed(() => columns)
</script>

<template>
  <table class="tbl">
    <thead>
      <tr>
        <th>{{ t('usage.logTable.time') }}</th>
        <th>{{ t('usage.logTable.status') }}</th>
        <th>{{ t('usage.logTable.requestModel') }}</th>
        <th>{{ t('usage.logTable.hitModel') }}</th>
        <th class="right">{{ t('usage.logTable.input') }}</th>
        <th class="right">{{ t('usage.logTable.output') }}</th>
        <th class="right">{{ t('usage.logTable.totalCost') }}</th>
        <th class="right">{{ t('usage.logTable.latencyTtft') }}</th>
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
          <!-- 1. Time -->
          <td><span class="text-mono" style="font-size: 12.5px;">{{ formatTime(log.timestamp) }}</span></td>

          <!-- 2. Status -->
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

          <!-- 3. Request model (rule name → upstream model fallback) -->
          <td>
            <span class="text-mono cell-request-model">{{ log.route_label || log.model }}</span>
          </td>

          <!-- 4. Hit model (last successful chain entry) -->
          <td>
            <template v-for="hit in [hitModel(log)]" :key="'hit'">
              <div v-if="hit" class="hit-model-cell">
                <div
                  class="hit-model-icon"
                  :style="{
                    background: providerColor(hit.provider),
                    color: providerTextColor(hit.provider),
                  }"
                >{{ providerInitial(hit.provider) }}</div>
                <div class="hit-model-text">
                  <span class="hit-model-provider">{{ hit.provider }}</span>
                  <span class="hit-model-model text-mono">{{ hit.model }}</span>
                </div>
              </div>
              <span v-else class="text-muted">—</span>
            </template>
          </td>

          <!-- 5. Input (with cache sub-line) -->
          <td class="num">
            <div class="cell-tokens">
              <span>{{ log.input_tokens > 0 ? log.input_tokens.toLocaleString() : '—' }}</span>
              <span v-if="log.cache_hit > 0" class="cache-sub" :title="t('usage.logTable.inputCache')">R{{ log.cache_hit.toLocaleString() }}</span>
            </div>
          </td>

          <!-- 6. Output -->
          <td class="num">{{ log.output_tokens > 0 ? log.output_tokens.toLocaleString() : '—' }}</td>

          <!-- 7. Total cost -->
          <td class="num">{{ log.cost > 0 ? '$' + log.cost.toFixed(3) : '—' }}</td>

          <!-- 8. TTFT / Latency + stream suffix + expand chevron -->
          <td class="right cell-timing">
            <div class="timing-values">
              <template v-if="log.first_token_ms > 0">
                <span class="timing-ttft">{{ (log.first_token_ms / 1000).toFixed(2) }}s</span>
                <span class="timing-sep">/</span>
              </template>
              <span class="timing-latency">{{ (log.latency_ms / 1000).toFixed(2) }}s</span>
              <span class="stream-suffix" :class="log.is_stream ? 'stream' : 'nostream'">{{ log.is_stream ? t('usage.logTable.streamSuffix') : t('usage.logTable.nonStreamSuffix') }}</span>
            </div>
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
                    <span v-if="entry.first_token_ms > 0" class="log-detail-chain-latency text-muted" style="margin-left: 2px;">· {{ t('usage.logTable.ttft') }} {{ formatLatency(entry.first_token_ms) }}</span>
                    <span v-if="entry.error" class="log-detail-chain-error text-mono">{{ entry.error }}</span>
                  </li>
                </ol>
              </div>
            </div>
          </td>
        </tr>
      </template>
      <tr v-if="props.logs.length === 0" class="logs-empty-row">
        <td :colspan="columnCount" style="padding: 56px 20px;">
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

/* ── Retry indicator (↻ circle) ── */
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

/* ── Column 3: Request model ── */
.cell-request-model {
  font-size: 12.5px;
  word-break: break-word;
}

/* ── Column 4: Hit model (provider / model) ── */
.hit-model-cell {
  display: flex;
  align-items: center;
  gap: 6px;
}
.hit-model-icon {
  width: 20px;
  height: 20px;
  border-radius: 5px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 9px;
  font-weight: 600;
  flex-shrink: 0;
}
.hit-model-text {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.hit-model-provider {
  font-size: 12px;
  font-weight: 500;
  color: var(--fg);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.hit-model-model {
  font-size: 11px;
  color: var(--muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* ── Column 5: Input tokens + cache sub-line ── */
.cell-tokens {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 1px;
}
.cache-sub {
  font-size: 10px;
  color: var(--muted);
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  letter-spacing: 0;
  line-height: 1;
}

/* ── Column 8: TTFT / Latency + stream suffix ── */
.cell-timing {
  white-space: nowrap;
}
.timing-values {
  display: inline-flex;
  align-items: baseline;
  gap: 3px;
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  font-size: 12.5px;
}
.timing-ttft {
  font-weight: 500;
  color: var(--fg);
}
.timing-sep {
  color: var(--muted);
  font-size: 11px;
  margin: 0 1px;
}
.timing-latency {
  color: var(--muted);
}
.stream-suffix {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-family: var(--font-body);
  font-size: 10px;
  font-weight: 500;
  padding: 1px 5px;
  border-radius: var(--radius-xs, 5px);
  margin-left: 4px;
  line-height: 1;
  vertical-align: baseline;
}
.stream-suffix.stream {
  color: var(--accent, #0071e3);
  background: rgba(0, 113, 227, 0.08);
}
.stream-suffix.nostream {
  color: var(--muted, #6e6e73);
  background: rgba(0, 0, 0, 0.04);
}

/* ── Expand chevron ── */
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

/* ── Detail row ── */
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
