<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { model } from '../../../wailsjs/go/models'
import { api } from '@/api/bridge'

import { useProviderStyle } from '@/composables/useProviderStyle'

const { t, locale } = useI18n()

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
const replayResults = ref<Map<string, model.ReplayResult>>(new Map())
const replayErrors = ref<Set<string>>(new Set())
const replayLoadingId = ref<string | null>(null)

function replayFor(log: model.RequestLog): model.ReplayResult | undefined {
  return replayResults.value.get(log.id)
}

async function replay(log: model.RequestLog) {
  if (replayLoadingId.value) return
  replayLoadingId.value = log.id
  const errors = new Set(replayErrors.value)
  errors.delete(log.id)
  replayErrors.value = errors
  try {
    const result = await api.replayLog(log.id)
    const next = new Map(replayResults.value)
    next.set(log.id, result)
    replayResults.value = next
  } catch {
    const next = new Set(replayErrors.value)
    next.add(log.id)
    replayErrors.value = next
  } finally {
    replayLoadingId.value = null
  }
}

function replayScore(value: number): string {
  return Number.isFinite(value) ? value.toFixed(0) : '—'
}

function replayOutcomeLabel(outcome: string | undefined): string {
  switch (outcome) {
    case 'success': return t('usage.logTable.replayOutcomeSuccess')
    case 'partial':
    case 'partial_success': return t('usage.logTable.replayOutcomePartial')
    case 'failure':
    case 'failed': return t('usage.logTable.replayOutcomeFailed')
    default: return outcome ? t('usage.logTable.replayOutcomeOther', { value: readableInternalValue(outcome) }) : t('usage.logTable.replayUnknown')
  }
}

function availabilityLabel(availability: string | undefined): string {
  switch (availability) {
    case 'available': return t('usage.logTable.replayAvailable')
    case 'unavailable': return t('usage.logTable.replayUnavailableStatus')
    default: return availability ? t('usage.logTable.replayStatusOther', { value: readableInternalValue(availability) }) : t('usage.logTable.replayUnknown')
  }
}

function readableInternalValue(value: string): string {
  if (locale.value.startsWith('zh')) return value
  return value.replace(/[_-]+/g, ' ').replace(/\b\w/g, character => character.toUpperCase())
}

type ReplayReason = 'target_breaker_open' | 'circuit_open' | 'disabled' | 'cooldown'

function replayReasonLabel(reason: ReplayReason | string | undefined): string {
  switch (reason) {
    case 'disabled': return t('usage.logTable.replayReasonDisabled')
    case 'target_breaker_open':
    case 'circuit_open': return t('usage.logTable.replayReasonCircuitOpen')
    case 'cooldown': return t('usage.logTable.replayReasonCooldown')
    default: return reason ? t('usage.logTable.replayReasonOther', { value: readableInternalValue(reason) }) : ''
  }
}

function replayLimitationLabel(value: string): string {
  if (value.includes('historical breaker state unavailable')) return t('usage.logTable.replayHistoricalBreakerUnavailable')
  return t('usage.logTable.replayLimitationValue', { value: readableInternalValue(value) })
}

function replayWarningLabel(value: string): string {
  if (value.includes('historical breaker state unavailable')) return t('usage.logTable.replayHistoricalBreakerUnavailable')
  return t('usage.logTable.replayWarningValue', { value: readableInternalValue(value) })
}

function replaySelectedAttempt(result: model.ReplayResult): model.ReplayAttemptScore | undefined {
  return result.attempts?.find(attempt => attempt.target_id === result.selected_target)
}

function replayTargetLabel(result: model.ReplayResult | undefined): string {
  if (!result) return t('usage.logTable.replayUnknown')
  const selected = replaySelectedAttempt(result)
  const provider = selected?.attempt?.provider_name || selected?.provider_id
  const modelName = selected?.model_name || selected?.attempt?.model_name
  if (provider || modelName) return [provider, modelName].filter(Boolean).join(' / ')
  return result.selected_target ? t('usage.logTable.replayTargetShort', { value: result.selected_target.slice(0, 8) }) : t('usage.logTable.replayUnknown')
}

function replayAttemptProvider(replayAttempt: model.ReplayAttemptScore): string {
  return replayAttempt.attempt?.provider_name || replayAttempt.provider_id || t('usage.logTable.replayUnknown')
}

function replayAttemptModel(replayAttempt: model.ReplayAttemptScore): string {
  return replayAttempt.model_name || replayAttempt.attempt?.model_name || t('usage.logTable.replayUnknown')
}

function replayAttemptTargetId(replayAttempt: model.ReplayAttemptScore): string {
  return replayAttempt.target_id ? replayAttempt.target_id.slice(0, 8) : '—'
}

function costLabel(value: number, available: boolean | undefined, decimals: number): string {
  return available === true && Number.isFinite(value) ? `$${value.toFixed(decimals)}` : '—'
}

function costTitle(available: boolean | undefined): string {
  return available === true ? '' : t('usage.logTable.usageUnavailable')
}

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
  return normalizedChainArray(log).length
}

// chainStatusLabel maps a ChainEntry.status string to the i18n key
// shown as a badge in the detail row. Unknown statuses fall through to
// the raw string so future server-side additions don't render blank.
type ChainStatus = model.RequestLogChainEntry['status'] | 'target_breaker_open' | 'truncated' | 'downstream_error'

function chainStatusLabel(status: ChainStatus): string {
  switch (status) {
    case 'success':
      return t('usage.logTable.statusSuccess')
    case 'retryable':
      return t('usage.logTable.statusRetryable')
    case 'non_retryable':
      return t('usage.logTable.statusNonRetryable')
    case 'target_breaker_open':
    case 'circuit_open':
      return t('usage.logTable.statusBreakerSkipped')
    case 'preflight_error':
      return t('usage.logTable.statusPreflightError')
    case 'client_abort':
      return t('usage.logTable.statusClientAbort')
    case 'truncated':
      return t('usage.logTable.statusTruncated')
    case 'downstream_error':
      return t('usage.logTable.statusDownstreamError')
    default:
      return status
  }
}

// chainStatusClass colors the status badge in the chain timeline. The
// classes (success/warn/error/info) are the same ones used in the
// status cell on the main row so the visual language stays consistent.
function chainStatusClass(status: ChainStatus): string {
  switch (status) {
    case 'success':
      return 'success'
    case 'retryable':
    case 'target_breaker_open':
    case 'circuit_open':
    case 'client_abort':
    case 'downstream_error':
    case 'truncated':
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
  return `${(ms / 1000).toFixed(1)}s`
}

/** Returns a CSS class for latency-based color coding. */
function timingClass(ms: number, green: number, orange: number): string {
  if (ms <= 0) return ''
  if (ms < green) return 'timing-fast'
  if (ms < orange) return 'timing-medium'
  return 'timing-slow'
}

function statusBadgeClass(statusCode: number): string {
  if (statusCode === 0) return 'pending'
  if (statusCode >= 200 && statusCode < 300) return 'success'
  if (statusCode === 429) return 'warn'
  if (statusCode >= 400) return 'error'
  return 'info'
}

function statusDotClass(statusCode: number): string {
  const cls = statusBadgeClass(statusCode)
  if (cls === 'success') return 'green'
  if (cls === 'warn') return 'amber'
  if (cls === 'pending') return 'blue'
  return 'red'
}

function statusText(statusCode: number): string {
  if (statusCode === 0) return '···'
  return String(statusCode)
}

interface NormalizedChainEntry extends model.RequestLogChainEntry {
  status: ChainStatus
  error: string
}

// Compatibility boundary for old logs: generated DTOs do not expose the
// legacy downstream_error field, so inspect the runtime object only here.
function normalizeChainEntry(entry: model.RequestLogChainEntry): NormalizedChainEntry {
  const raw = entry as unknown as Record<string, unknown>
  const downstreamError = typeof raw.downstream_error === 'string' ? raw.downstream_error : ''
  const error = typeof entry.error === 'string' ? entry.error : ''
  const status: ChainStatus = entry.status === 'success' && downstreamError ? 'downstream_error' : entry.status as ChainStatus
  return { ...entry, status, error: downstreamError || error }
}

function normalizedChainArray(log: model.RequestLog): NormalizedChainEntry[] {
  return Array.isArray(log.chain) ? log.chain.map(normalizeChainEntry) : []
}

function replayAttemptEntry(attempt: model.ReplayAttemptScore): NormalizedChainEntry {
  return normalizeChainEntry(attempt.attempt)
}

// Explicit top-level 2xx presentations derived from the final chain entry.
// We do not fall back to a generic "non-success == partial" heuristic;
// only these named outcomes change the status badge from a clean success.
type OutcomePresentation = 'success' | 'truncated' | 'downstream_error' | 'client_abort'

function topLevel2xxOutcome(log: model.RequestLog): OutcomePresentation | null {
  if (log.status_code < 200 || log.status_code >= 300) return null
  const chain = normalizedChainArray(log)
  if (chain.length === 0) return null
  const final = chain[chain.length - 1].status
  if (final === 'success') return 'success'
  if (final === 'truncated') return 'truncated'
  if (final === 'downstream_error') return 'downstream_error'
  if (final === 'client_abort') return 'client_abort'
  return null
}

// hitModel finds the last successful chain entry and returns the
// provider/model that actually served the request. Returns null when
// no attempt succeeded (e.g. all retries exhausted).
function hitModel(log: model.RequestLog): { provider: string; model: string } | null {
  const chain = normalizedChainArray(log)
  for (let i = chain.length - 1; i >= 0; i--) {
    if (chain[i].status === 'success') {
      return {
        provider: chain[i].provider_name || '',
        model: chain[i].model_name || '',
      }
    }
  }
  return null
}

function apiKeyLabel(log: model.RequestLog): string {
  const raw = log as unknown as Record<string, unknown>
  if (typeof raw.api_key_id !== 'string' || !raw.api_key_id) return '—'
  return typeof raw.api_key_name === 'string' && raw.api_key_name.trim() ? raw.api_key_name : t('usage.logTable.unknownToken')
}

</script>

<template>
  <div class="tbl-wrap">
  <table class="tbl">
    <thead>
      <tr>
        <th>{{ t('usage.logTable.time') }}</th>
        <th>{{ t('usage.logTable.status') }}</th>
        <th>{{ t('usage.logTable.requestModel') }}</th>
        <th>{{ t('usage.logTable.hitModel') }}</th>
        <th class="right">{{ t('usage.logTable.latencyTtft') }}</th>
        <th class="right">{{ t('usage.logTable.input') }}</th>
        <th class="right">{{ t('usage.logTable.output') }}</th>
        <th class="right">{{ t('usage.logTable.totalCost') }}</th>
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
            <template v-if="topLevel2xxOutcome(log) === 'truncated'">
              <span class="badge warn" :title="t('usage.logTable.partialTruncatedTitle')">
                <span class="dot amber"></span>~{{ log.status_code }}
              </span>
            </template>
            <template v-else-if="topLevel2xxOutcome(log) === 'downstream_error'">
              <span class="badge warn" :title="t('usage.logTable.partialDownstreamTitle')">
                <span class="dot amber"></span>~{{ log.status_code }}
              </span>
            </template>
            <template v-else-if="topLevel2xxOutcome(log) === 'client_abort'">
              <span class="badge warn" :title="t('usage.logTable.partialClientAbortTitle')">
                <span class="dot amber"></span>~{{ log.status_code }}
              </span>
            </template>
            <template v-else>
              <span class="badge" :class="statusBadgeClass(log.status_code)">
                <span :class="'dot ' + statusDotClass(log.status_code)"></span>{{ statusText(log.status_code) }}
              </span>
            </template>
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

          <!-- 5. TTFT / Latency + stream suffix + expand chevron -->
          <td class="right cell-timing">
            <div class="timing-values">
              <template v-if="log.first_token_ms > 0">
                <span class="timing-badge" :class="timingClass(log.first_token_ms, 3000, 10000)">{{ (log.first_token_ms / 1000).toFixed(1) }}s</span>
                <span class="timing-sep">/</span>
              </template>
              <template v-else>
                <span class="timing-badge timing-neutral">—</span>
                <span class="timing-sep">/</span>
              </template>
              <span class="timing-badge" :class="timingClass(log.latency_ms, 100000, 180000)">{{ (log.latency_ms / 1000).toFixed(1) }}s</span>
              <span class="stream-suffix" :class="log.is_stream ? 'stream' : 'nostream'">{{ log.is_stream ? t('usage.logTable.streamSuffix') : t('usage.logTable.nonStreamSuffix') }}</span>
            </div>
            <span
              class="expand-chevron"
              :class="{ 'expand-chevron-open': isExpanded(log) }"
              :aria-label="isExpanded(log) ? t('usage.logTable.collapse') : t('usage.logTable.expand')"
              aria-hidden="true"
            >›</span>
          </td>

          <!-- 6. Input (with cache sub-line) -->
          <td class="num">
            <div class="cell-tokens">
              <span :title="log.input_tokens > 0 ? '' : t('usage.logTable.usageUnavailable')">{{ log.input_tokens > 0 ? log.input_tokens.toLocaleString() : '—' }}</span>
              <span v-if="log.cache_hit > 0" class="cache-sub" :title="t('usage.logTable.inputCache')">R{{ log.cache_hit.toLocaleString() }}</span>
            </div>
          </td>

          <!-- 7. Output -->
          <td class="num"><span :title="log.output_tokens > 0 ? '' : t('usage.logTable.usageUnavailable')">{{ log.output_tokens > 0 ? log.output_tokens.toLocaleString() : '—' }}</span></td>

          <!-- 8. Total cost -->
          <td class="num"><span :title="costTitle(log.cost_available)">{{ costLabel(log.cost, log.cost_available, 3) }}</span></td>
        </tr>
        <tr v-if="isExpanded(log)" class="log-detail-row">
          <td :colspan="columns">
            <div class="log-detail">
              <div class="log-detail-grid">
                <div class="log-detail-item"><span class="log-detail-label">{{ t('usage.logTable.token') }}</span><span class="log-detail-value">{{ apiKeyLabel(log) }}</span></div>
                <div class="log-detail-item">
                  <span class="log-detail-label">{{ t('usage.logTable.requestId') }}</span>
                  <span class="log-detail-value text-mono">{{ log.request_id || '—' }}</span>
                </div>
                <div class="log-detail-item">
                  <span class="log-detail-label">{{ t('usage.logTable.requestUri') }}</span>
                  <span class="log-detail-value text-mono">{{ log.request_uri || '—' }}</span>
                </div>
                <div class="log-detail-item">
                  <span class="log-detail-label">{{ t('usage.logTable.userAgent') }}</span>
                  <span class="log-detail-value text-mono log-detail-ua">{{ log.user_agent || '—' }}</span>
                </div>
                <div class="log-detail-item">
                  <span class="log-detail-label">{{ t('usage.logTable.clientIp') }}</span>
                  <span class="log-detail-value text-mono">{{ log.client_ip || '—' }}</span>
                </div>
                <div class="log-detail-item">
                  <span class="log-detail-label">{{ t('usage.logTable.reasoningEffort') }}</span>
                  <span class="log-detail-value text-mono">{{ log.reasoning_effort || '—' }}</span>
                </div>
              </div>
              <div v-if="normalizedChainArray(log).length > 0" class="log-detail-chain">
                <div class="log-detail-chain-header">
                  <span class="log-detail-label">{{ t('usage.logTable.chain') }}</span>
                  <span v-if="normalizedChainArray(log).length > 1" class="text-muted log-detail-tried">
                    {{ t('usage.logTable.triedTargets', { n: normalizedChainArray(log).length }) }}
                  </span>
                </div>
                <ol class="log-detail-chain-list">
                  <li v-for="entry in normalizedChainArray(log)" :key="entry.attempt_order" class="log-detail-chain-item">
                    <span class="log-detail-attempt">{{ t('usage.logTable.attempt', { n: entry.attempt_order }) }}</span>
                    <span class="log-detail-chain-provider">{{ entry.provider_name || '—' }}</span>
                    <span class="log-detail-chain-model text-mono">{{ entry.model_name || '—' }}</span>
                    <span class="badge" :class="chainStatusClass(entry.status)">{{ chainStatusLabel(entry.status) }}</span>
                    <span class="log-detail-chain-endpoint text-mono" :title="entry.endpoint || t('usage.logTable.endpointUnavailable')"><span class="log-detail-chain-endpoint-label">{{ t('usage.logTable.endpoint') }}</span> {{ entry.endpoint || '—' }}</span>
                    <span class="log-detail-chain-latency text-muted" :title="costTitle(entry.request_cost_available)">· {{ costLabel(entry.request_cost, entry.request_cost_available, 4) }}</span>
                    <span class="log-detail-chain-latency text-muted">{{ formatLatency(entry.latency_ms) }}</span>
                    <span v-if="entry.first_token_ms > 0" class="log-detail-chain-latency text-muted" style="margin-left: 2px;">· {{ t('usage.logTable.ttft') }} {{ formatLatency(entry.first_token_ms) }}</span>
                    <span v-if="entry.status === 'downstream_error' && entry.error" class="log-detail-chain-downstream-error text-mono">
                      <span class="log-detail-chain-downstream-label">{{ t('usage.logTable.downstreamError') }}:</span>
                      {{ entry.error }}
                    </span>
                    <span v-else-if="entry.error" class="log-detail-chain-error text-mono">{{ entry.error }}</span>
                  </li>
                </ol>
              </div>
              <div class="replay-panel">
                <div class="replay-header">
                  <div>
                    <div class="log-detail-label">{{ t('usage.logTable.replay') }}</div>
                    <div v-if="replayFor(log)" class="replay-summary">
                      <strong class="replay-conclusion">{{ replayOutcomeLabel(replayFor(log)?.request_outcome) }}</strong>
                      <span>{{ t('usage.logTable.replaySelectedTarget') }}: {{ replayTargetLabel(replayFor(log)) }}</span>
                      <span>{{ t('usage.logTable.replayRule') }}: {{ replayFor(log)?.rule_name || t('usage.logTable.replayNoRule') }}</span>
                      <span>{{ t('usage.logTable.replayEndpoint') }}: {{ replayFor(log)?.endpoint || '—' }}</span>
                      <span v-if="replayFor(log)?.endpoint_assumed" class="replay-note">{{ t('usage.logTable.replayEndpointAssumed') }}</span>
                    </div>
                  </div>
                  <button class="btn btn-secondary replay-button" type="button" :disabled="replayLoadingId !== null" @click.stop="replay(log)">{{ replayLoadingId === log.id ? t('usage.logTable.replayLoading') : replayFor(log) ? t('usage.logTable.replayRefresh') : t('usage.logTable.replay') }}</button>
                </div>
                <div v-if="replayErrors.has(log.id)" class="replay-error">{{ t('usage.logTable.replayUnavailable') }}</div>
                <template v-else-if="replayFor(log)">
                  <div v-if="replayFor(log)?.warnings?.length" class="replay-warnings"><strong>{{ t('usage.logTable.replayWarnings') }}</strong><span v-for="warning in replayFor(log)?.warnings" :key="warning">{{ replayWarningLabel(warning) }}</span></div>
                  <div v-if="replayFor(log)?.attempts?.length" class="replay-attempts">
                    <div class="replay-attempts-title">{{ t('usage.logTable.replayAttempts') }} · {{ t('usage.logTable.replayScores') }}</div>
                    <div v-for="(replayAttempt, index) in replayFor(log)?.attempts" :key="replayAttempt.attempt.attempt_order || index" class="replay-attempt">
                      <div class="replay-attempt-main"><span class="log-detail-attempt">{{ t('usage.logTable.attempt', { n: replayAttemptEntry(replayAttempt).attempt_order }) }}</span><span class="replay-attempt-provider">{{ replayAttemptProvider(replayAttempt) }}</span><span class="text-mono text-muted">{{ replayAttemptModel(replayAttempt) }}</span><span v-if="replayAttempt.target_id" class="replay-target-id text-muted" :title="replayAttempt.target_id">{{ t('usage.logTable.replayTarget') }} {{ replayAttemptTargetId(replayAttempt) }}</span><span class="badge" :class="chainStatusClass(replayAttemptEntry(replayAttempt).status)">{{ chainStatusLabel(replayAttemptEntry(replayAttempt).status) }}</span><span class="replay-attempt-endpoint text-mono"><span class="log-detail-chain-endpoint-label">{{ t('usage.logTable.endpoint') }}</span> {{ replayAttemptEntry(replayAttempt).endpoint || '—' }}</span></div>
                      <div class="replay-score-grid"><span class="replay-score-overall">{{ t('modelRules.diagnostics.score') }} {{ replayScore(replayAttempt.score.overall) }}</span><span>{{ t('modelRules.diagnostics.reliabilityPlain') }} {{ replayScore(replayAttempt.score.reliability) }}</span><span>{{ t('modelRules.diagnostics.latencyPlain') }} {{ replayScore(replayAttempt.score.latency) }}</span><span>{{ t('modelRules.diagnostics.ttftPlain') }} {{ replayScore(replayAttempt.score.ttft) }}</span><span>{{ t('modelRules.diagnostics.capacityPlain') }} {{ replayScore(replayAttempt.score.capacity) }}</span><span>{{ t('modelRules.diagnostics.costEfficiencyPlain') }} {{ replayScore(replayAttempt.score.cost_efficiency) }}</span><span>{{ t('usage.logTable.replayEstimatedCost') }} {{ costLabel(replayAttempt.score.estimated_cost, replayAttempt.score.cost?.available, 4) }}</span><span>{{ t('usage.logTable.replayAvailability') }}: {{ availabilityLabel(replayAttempt.score.availability) }}<template v-if="replayAttempt.score.reason"> · {{ replayReasonLabel(replayAttempt.score.reason) }}</template></span></div>
                      <div v-if="replayAttempt.target_missing || replayAttempt.provider_missing || replayAttempt.replay_limitation || !replayAttempt.score.metrics_fresh" class="replay-attempt-notes"><span v-if="replayAttempt.target_missing">{{ t('usage.logTable.replayMissingTarget') }}</span><span v-if="replayAttempt.provider_missing">{{ t('usage.logTable.replayMissingProvider') }}</span><span v-if="!replayAttempt.score.metrics_fresh">{{ t('usage.logTable.replayNoSamples') }}</span><span v-if="replayAttempt.replay_limitation">{{ t('usage.logTable.replayLimitation') }}: {{ replayLimitationLabel(replayAttempt.replay_limitation) }}</span></div>
                    </div>
                  </div>
                  <div v-else class="replay-muted">{{ t('usage.logTable.replayNoChain') }}</div>
                </template>
              </div>
            </div>
          </td>
        </tr>
      </template>
      <tr v-if="props.logs.length === 0" class="logs-empty-row">
        <td :colspan="columns" style="padding: 56px 20px;">
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
  </div>
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

/* ── Column min-widths ──────────────────────────────────────────────
   The 8-column log table is naturally wider than the narrowest
   viewports we support (~760px after sidebar + padding). Without
   min-widths the browser squeezes every column down to fit, which
   character-wraps the Chinese headers ("请求模型", "命中模型") one
   glyph per line. Forcing a minimum width per column tells the
   browser "this column cannot shrink further"; once the sum exceeds
   .tbl-wrap, the wrapper's overflow-x:auto kicks in and the table
   scrolls horizontally instead of breaking layout.

   The widths are tuned to comfortably hold the largest realistic
   content per column (status badge, model name, hit-model icon+name,
   token numbers, latency/TTFT suffix, etc.). */
.tbl th:nth-child(1),
.tbl td:nth-child(1) { min-width: 78px; }   /* Time             (07/10 23:28) */
.tbl th:nth-child(2),
.tbl td:nth-child(2) { min-width: 64px; }   /* Status           (● 200) */
.tbl th:nth-child(3),
.tbl td:nth-child(3) { min-width: 120px; }  /* Request model    (minimax-m3 / claude-3-5-…) */
.tbl th:nth-child(4),
.tbl td:nth-child(4) { min-width: 150px; }  /* Hit model        (provider + model) */
.tbl th:nth-child(5),
.tbl td:nth-child(5) { min-width: 130px; }  /* Latency/TTFT     (3.2s/6.8s · stream) */
.tbl th:nth-child(6),
.tbl td:nth-child(6) { min-width: 64px; }   /* Input            (12345 + cache sub) */
.tbl th:nth-child(7),
.tbl td:nth-child(7) { min-width: 64px; }   /* Output           (1234) */
.tbl th:nth-child(8),
.tbl td:nth-child(8) { min-width: 72px; }   /* Total cost       ($0.123) */

/* nowrap on the header cells prevents the column titles from
   character-wrapping even before min-widths are reached. */
.tbl th {
  white-space: nowrap;
}

/* ── Column 3: Request model ────────────────────────────────────────
   The route_label / model can be long ("claude-3-5-sonnet-20241022")
   and Chinese column headers squeeze this column at narrow widths,
   which used to character-wrap individual CJK glyphs. Forcing nowrap
   with overflow ellipsis keeps a single line; the cell's max-width
   caps the worst case so the table can scroll horizontally inside
   .tbl-wrap when the column does need to extend. */
.cell-request-model {
  font-size: 12.5px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  display: inline-block;
  max-width: 220px;
  vertical-align: middle;
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

/* ── Column 6: Input tokens + cache sub-line ── */
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

/* ── Column 5: TTFT / Latency + stream suffix ── */
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
.timing-badge {
  display: inline-flex;
  align-items: center;
  padding: 1px 6px;
  border-radius: var(--radius-xs);
  font-size: 11px;
  font-weight: 500;
  font-variant-numeric: tabular-nums;
}
.timing-badge.timing-fast { background: color-mix(in srgb, var(--positive) 12%, transparent); color: var(--positive); }
.timing-badge.timing-medium { background: color-mix(in srgb, var(--warning) 12%, transparent); color: var(--warning); }
.timing-badge.timing-slow { background: color-mix(in srgb, var(--negative) 12%, transparent); color: var(--negative); }
.timing-badge.timing-neutral { background: var(--bg-secondary); color: var(--muted); }
.timing-sep {
  color: var(--muted);
  font-size: 11px;
  margin: 0 1px;
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
.log-detail-chain-endpoint {
  color: var(--fg, #1d1d1f);
  max-width: min(100%, 520px);
  overflow-wrap: anywhere;
  font-size: 11px;
}
.log-detail-chain-endpoint-label { color: var(--muted); font-family: var(--font-body); }
.log-detail-chain-latency {
  font-size: 11.5px;
}
.log-detail-chain-error {
  flex-basis: 100%;
  font-size: 11px;
  color: var(--muted, #6e6e73);
  word-break: break-all;
}
.log-detail-chain-downstream-error {
  flex-basis: 100%;
  font-size: 11px;
  color: var(--warning, #ad6700);
  word-break: break-all;
}
.log-detail-chain-downstream-label {
  font-weight: 500;
  color: var(--warning, #ad6700);
}
.replay-panel {
  border-top: 1px dashed var(--border, rgba(0, 0, 0, 0.08));
  padding-top: 12px;
}
.replay-header,
.replay-summary,
.replay-attempt-main,
.replay-warnings,
.replay-attempt-notes {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.replay-header { justify-content: space-between; gap: 12px; }
.replay-summary { margin-top: 4px; color: var(--muted); font-size: 11.5px; }
.replay-conclusion { color: var(--fg); font-size: 12px; }
.replay-button { padding: 4px 9px; font-size: 11.5px; flex-shrink: 0; }
.replay-note,
.replay-error,
.replay-attempt-notes { color: var(--warning, #ad6700); }
.replay-error,
.replay-muted { margin-top: 8px; color: var(--muted); font-size: 12px; }
.replay-warnings { margin-top: 8px; color: var(--warning, #ad6700); font-size: 11.5px; }
.replay-warnings span { flex-basis: 100%; padding-left: 8px; }
.replay-attempts { margin-top: 10px; display: flex; flex-direction: column; gap: 6px; }
.replay-attempts-title { color: var(--muted); font-size: 11px; text-transform: uppercase; letter-spacing: 0.04em; }
.replay-attempt { padding: 7px 8px; border-radius: 6px; background: var(--bg, rgba(0, 0, 0, 0.02)); }
.replay-attempt-provider { color: var(--fg); font-weight: 500; }
.replay-attempt-endpoint { color: var(--fg); max-width: 100%; overflow-wrap: anywhere; font-size: 11px; }
.replay-target-id { font-family: var(--font-mono); font-size: 10px; }
.replay-score-grid { display: flex; flex-wrap: wrap; gap: 6px 12px; margin-top: 5px; color: var(--muted); font-family: var(--font-mono); font-size: 10.5px; font-variant-numeric: tabular-nums; }
.replay-score-overall { color: var(--fg); font-weight: 600; }
.replay-attempt-notes { margin-top: 5px; font-size: 11px; }
@media (max-width: 700px) {
  .replay-header { align-items: flex-start; }
  .replay-score-grid { gap: 5px 9px; }
}
</style>
