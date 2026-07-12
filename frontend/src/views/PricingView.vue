<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '../api/bridge'
import { useApi } from '../composables/useApi'
import { useToast } from '../composables/useToast'
import { useConfirm } from '../composables/useConfirm'
import type { model } from '../../wailsjs/go/models'

const { t } = useI18n()
const toast = useToast()
const confirm = useConfirm()

const {
  data: prices,
  loading: pricesLoading,
  error: pricesError,
  execute: loadPrices,
} = useApi(() => api.listPrices())

const {
  data: providers,
  execute: loadProviders,
} = useApi(() => api.providers())

const search = ref('')
const modalOpen = ref(false)
const editingId = ref('')
const saving = ref(false)
const nowMs = ref(Date.now())
let statusTimer: ReturnType<typeof setInterval> | undefined

type PriceStatus = 'active' | 'future' | 'expired'

const form = ref<{
  provider_id: string
  upstream_model: string
  endpoint_kind: string
  billing_mode: string
  input_price_per_million: string
  output_price_per_million: string
  effective_at: string
  expires_at: string
  source: string
  version: string
}>({
  provider_id: '',
  upstream_model: '',
  endpoint_kind: '',
  billing_mode: 'token',
  input_price_per_million: '',
  output_price_per_million: '',
  effective_at: '',
  expires_at: '',
  source: '',
  version: '',
})

const filteredPrices = computed(() => {
  let list = prices.value || []
  const q = search.value.trim().toLowerCase()
  if (q) {
    list = list.filter((p) =>
      p.upstream_model.toLowerCase().includes(q) ||
      (p.provider_id ?? '').toLowerCase().includes(q) ||
      p.endpoint_kind.toLowerCase().includes(q)
    )
  }
  return list
})

const providerNameMap = computed(() => {
  const map: Record<string, string> = {}
  ;(providers.value || []).forEach((p) => (map[p.id] = p.name))
  return map
})

function toDateLocal(ms: number): string {
  if (!ms || ms <= 0) return ''
  const date = new Date(ms)
  if (Number.isNaN(date.getTime())) return ''
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function fromDateLocal(s: string): number {
  if (!s) return 0
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(s)
  if (!match) return 0
  const [, year, month, day, hour, minute] = match
  const date = new Date(Number(year), Number(month) - 1, Number(day), Number(hour), Number(minute))
  if (
    Number.isNaN(date.getTime()) ||
    date.getFullYear() !== Number(year) ||
    date.getMonth() !== Number(month) - 1 ||
    date.getDate() !== Number(day) ||
    date.getHours() !== Number(hour) ||
    date.getMinutes() !== Number(minute)
  ) {
    return 0
  }
  return date.getTime()
}

function priceStatus(price: model.Price): PriceStatus {
  const now = nowMs.value
  if (price.expires_at > 0 && now >= price.expires_at) return 'expired'
  if (price.effective_at > 0 && now < price.effective_at) return 'future'
  return 'active'
}

function openAddModal() {
  editingId.value = ''
  form.value = {
    provider_id: '',
    upstream_model: '',
    endpoint_kind: '',
    billing_mode: 'token',
    input_price_per_million: '',
    output_price_per_million: '',
    effective_at: '',
    expires_at: '',
    source: '',
    version: '',
  }
  modalOpen.value = true
}

function openEditModal(p: model.Price) {
  editingId.value = p.id || ''
  form.value = {
    provider_id: p.provider_id || '',
    upstream_model: p.upstream_model || '',
    endpoint_kind: p.endpoint_kind || '',
    billing_mode: p.billing_mode || 'token',
    input_price_per_million: p.input_price_per_million ? String(p.input_price_per_million) : '',
    output_price_per_million: p.output_price_per_million ? String(p.output_price_per_million) : '',
    effective_at: toDateLocal(p.effective_at),
    expires_at: toDateLocal(p.expires_at),
    source: p.source || '',
    version: p.version || '',
  }
  modalOpen.value = true
}

async function save() {
  saving.value = true
  try {
    const effectiveAt = fromDateLocal(form.value.effective_at)
    const expiresAt = fromDateLocal(form.value.expires_at)
    if ((form.value.effective_at && !effectiveAt) || (form.value.expires_at && !expiresAt)) {
      throw new Error(t('pricing.invalidDate'))
    }
    const input: model.PriceInput = {
      provider_id: form.value.provider_id || '',
      upstream_model: form.value.upstream_model.trim(),
      endpoint_kind: form.value.endpoint_kind.trim(),
      billing_mode: form.value.billing_mode,
      input_price_per_million: parseFloat(form.value.input_price_per_million) || 0,
      output_price_per_million: parseFloat(form.value.output_price_per_million) || 0,
      cache_read_price_per_million: 0,
      cache_write_price_per_million: 0,
      request_price_per_request: 0,
      currency: 'USD',
      source: form.value.source.trim(),
      version: form.value.version.trim(),
      effective_at: effectiveAt,
      expires_at: expiresAt,
      confidence: 'exact',
    }
    await api.upsertPrice(input)
    await loadPrices()
    modalOpen.value = false
    toast.push(t('toast.priceSaved'), 'success')
  } catch (e: any) {
    toast.push(t('toast.saveFailed') + ': ' + (e?.message || String(e)), 'error')
  } finally {
    saving.value = false
  }
}

async function deletePrice(id: string, model: string) {
  const ok = await confirm.open({
    title: t('confirm.deletePriceTitle'),
    message: t('confirm.deletePriceMessage', { model }),
    confirmText: t('common.delete'),
    danger: true,
  })
  if (!ok) return
  try {
    await api.deletePrice(id)
    await loadPrices()
    toast.push(t('toast.priceDeleted'), 'success')
  } catch (e: any) {
    toast.push(t('toast.deleteFailed') + ': ' + (e?.message || String(e)), 'error')
  }
}

onMounted(async () => {
  statusTimer = setInterval(() => {
    nowMs.value = Date.now()
  }, 60_000)
  await Promise.all([loadPrices(), loadProviders()])
})

onUnmounted(() => {
  if (statusTimer) clearInterval(statusTimer)
})
</script>

<template>
  <div class="view pricing-view">
    <div class="view-header">
      <div>
        <h1>{{ $t('pricing.title') }}</h1>
        <p class="subtitle">{{ $t('pricing.subtitle', { count: prices?.length ?? 0 }) }}</p>
      </div>
      <div class="header-actions">
        <button class="btn btn-primary" @click="openAddModal">{{ $t('pricing.add') }}</button>
      </div>
    </div>

    <div v-if="pricesLoading" class="loading">{{ $t('pricing.loading') }}</div>
    <div v-else-if="pricesError" class="error">{{ $t('pricing.loadFailed', { error: pricesError }) }}</div>

    <div v-else class="controls">
      <input
        v-model="search"
        type="text"
        class="input"
        :placeholder="$t('pricing.searchPlaceholder')"
      />
    </div>

    <table v-if="!pricesLoading && !pricesError" class="table">
      <thead>
        <tr>
          <th>{{ $t('pricing.columns.provider') }}</th>
          <th>{{ $t('pricing.columns.upstreamModel') }}</th>
          <th>{{ $t('pricing.columns.endpoint') }}</th>
          <th>{{ $t('pricing.columns.billingMode') }}</th>
          <th>{{ $t('pricing.columns.inputPrice') }}</th>
          <th>{{ $t('pricing.columns.outputPrice') }}</th>
          <th>{{ $t('pricing.columns.status') }}</th>
          <th>{{ $t('pricing.columns.effective') }}</th>
          <th>{{ $t('pricing.columns.expires') }}</th>
          <th>{{ $t('pricing.columns.actions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="p in filteredPrices" :key="p.id">
          <td>{{ providerNameMap[p.provider_id || ''] || p.provider_id || $t('pricing.globalPrice') }}</td>
          <td><code>{{ p.upstream_model }}</code></td>
          <td>{{ p.endpoint_kind || $t('pricing.emptyValue') }}</td>
          <td><span class="badge">{{ p.billing_mode }}</span></td>
          <td class="num">{{ $t('pricing.pricePerMillion', { value: p.input_price_per_million?.toFixed(6) || '0' }) }}</td>
          <td class="num">{{ $t('pricing.pricePerMillion', { value: p.output_price_per_million?.toFixed(6) || '0' }) }}</td>
          <td><span class="status-badge" :class="`status-${priceStatus(p)}`">{{ $t(`pricing.status.${priceStatus(p)}`) }}</span></td>
          <td class="nowrap">{{ p.effective_at ? new Date(p.effective_at).toLocaleDateString() : $t('pricing.emptyValue') }}</td>
          <td class="nowrap">{{ p.expires_at ? new Date(p.expires_at).toLocaleDateString() : $t('pricing.emptyValue') }}</td>
          <td class="actions">
            <button class="btn btn-sm" @click="openEditModal(p)">{{ $t('common.edit') }}</button>
            <button class="btn btn-sm btn-danger" @click="deletePrice(p.id!, p.upstream_model)">{{ $t('common.delete') }}</button>
          </td>
        </tr>
        <tr v-if="filteredPrices.length === 0">
          <td colspan="10" class="empty">{{ $t('pricing.empty') }}</td>
        </tr>
      </tbody>
    </table>

    <!-- Modal -->
    <div v-if="modalOpen" class="modal-overlay" @click.self="modalOpen = false">
      <div class="modal">
        <div class="modal-header">
          <h2>{{ editingId ? $t('pricing.modal.edit') : $t('pricing.modal.add') }}</h2>
        </div>
        <div class="modal-body">
          <label>{{ $t('pricing.modal.provider') }}
            <select v-model="form.provider_id" class="input">
              <option value="">{{ $t('pricing.globalPrice') }}</option>
              <option v-for="p in providers" :key="p.id" :value="p.id">{{ p.name }}</option>
            </select>
          </label>
          <label>{{ $t('pricing.modal.upstreamModel') }}
            <input v-model="form.upstream_model" type="text" class="input" :placeholder="$t('pricing.modal.upstreamModelPlaceholder')" />
          </label>
          <label>{{ $t('pricing.modal.endpoint') }}
            <input v-model="form.endpoint_kind" type="text" class="input" :placeholder="$t('pricing.modal.endpointPlaceholder')" />
          </label>
          <label>{{ $t('pricing.modal.billingMode') }}
            <select v-model="form.billing_mode" class="input">
              <option value="token">{{ $t('pricing.billingModes.token') }}</option>
              <option value="request">{{ $t('pricing.billingModes.request') }}</option>
            </select>
          </label>
          <div class="form-row">
            <label>{{ $t('pricing.modal.inputPrice') }}
              <input v-model="form.input_price_per_million" type="number" step="any" min="0" class="input" placeholder="0" />
            </label>
            <label>{{ $t('pricing.modal.outputPrice') }}
              <input v-model="form.output_price_per_million" type="number" step="any" min="0" class="input" placeholder="0" />
            </label>
          </div>
          <div class="form-row">
            <label>{{ $t('pricing.modal.effective') }}
              <input v-model="form.effective_at" type="datetime-local" class="input" />
            </label>
            <label>{{ $t('pricing.modal.expires') }}
              <input v-model="form.expires_at" type="datetime-local" class="input" />
            </label>
          </div>
          <div class="form-row">
            <label>{{ $t('pricing.modal.source') }}
              <input v-model="form.source" type="text" class="input" :placeholder="$t('pricing.modal.sourcePlaceholder')" />
            </label>
            <label>{{ $t('pricing.modal.version') }}
              <input v-model="form.version" type="text" class="input" :placeholder="$t('pricing.modal.versionPlaceholder')" />
            </label>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn" @click="modalOpen = false" :disabled="saving">{{ $t('common.cancel') }}</button>
          <button class="btn btn-primary" @click="save" :disabled="saving">{{ saving ? $t('common.processing') : $t('common.save') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.pricing-view .controls {
  margin-bottom: 12px;
}
.table {
  width: 100%;
  border-collapse: collapse;
}
.table th,
.table td {
  padding: 8px 12px;
  text-align: left;
  border-bottom: 1px solid var(--border-color, #e0e0e0);
}
.table th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary, #888);
  text-transform: uppercase;
}
.num {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.nowrap {
  white-space: nowrap;
}
.actions {
  white-space: nowrap;
}
.actions .btn {
  margin-right: 4px;
}
.empty {
  text-align: center;
  color: var(--text-secondary, #888);
  padding: 24px;
}
.badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  background: var(--bg-tertiary, #f0f0f0);
  color: var(--text-secondary, #666);
}
.status-badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
}
.status-active {
  background: color-mix(in srgb, var(--success-color, #34c759) 16%, transparent);
  color: var(--success-color, #248a3d);
}
.status-future {
  background: color-mix(in srgb, var(--warning-color, #ff9f0a) 16%, transparent);
  color: var(--warning-color, #b76e00);
}
.status-expired {
  background: color-mix(in srgb, var(--danger-color, #ff3b30) 16%, transparent);
  color: var(--danger-color, #c62828);
}
.form-row {
  display: flex;
  gap: 12px;
}
.form-row label {
  flex: 1;
}
</style>
