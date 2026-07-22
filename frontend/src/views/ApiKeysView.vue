<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '../api/bridge'
import { useApi } from '../composables/useApi'
import { useRelativeTime } from '../composables/useRelativeTime'
import { toDateTimeLocal, fromDateTimeLocal } from '../composables/useDateTime'
import { useToast } from '../composables/useToast'
import { useConfirm } from '../composables/useConfirm'
import { useFormatters } from '../composables/useFormatters'
import DropdownMenu from '@/components/DropdownMenu.vue'
import type { model } from '../../wailsjs/go/models'

const { t } = useI18n()
const { format } = useRelativeTime()
const { tokens: formatTokens } = useFormatters()
const toast = useToast()
const confirm = useConfirm()

const {
  data: keys,
  loading: keysLoading,
  error: keysError,
  execute: loadKeys,
} = useApi(() => api.apiKeys())

const {
  data: modelRules,
  loading: modelRulesLoading,
  error: modelRulesError,
  execute: loadModelRules,
} = useApi(() => api.modelRules())

const search = ref('')
const modalOpen = ref(false)
const modalMode = ref<'create' | 'edit'>('create')
const editingId = ref('')
const saving = ref(false)
const deleting = ref(false)
const revealedToken = ref('')

type ApiKeyForm = model.ApiKeyInput & { allowed_rule_ids: string[] }

const form = ref<ApiKeyForm>({
  name: '',
  expires_at: 0,
  allowed_rule_ids: [],
})

const filteredKeys = computed(() => {
  let list = keys.value || []
  const q = search.value.trim().toLowerCase()
  if (q) {
    list = list.filter((k) => k.name.toLowerCase().includes(q))
  }
  return list
})

const activeCount = computed(() => (keys.value || []).filter((key) => keyField(key, 'enabled') !== false).length)

const expiringCount = computed(() => {
  const now = Date.now()
  const fourteenDays = 14 * 24 * 60 * 60 * 1000
  return (keys.value || []).filter(
    (k) => k.expires_at > 0 && k.expires_at - now > 0 && k.expires_at - now <= fourteenDays
  ).length
})

function tokenDisplay(id: string): string {
  if (id.length <= 12) return id
  return `${id.slice(0, 8)}...${id.slice(-4)}`
}

function formatExpiresAt(ms: number): string {
  if (!ms || ms <= 0) return t('apiKeys.noExpiry')
  return new Date(ms).toLocaleString()
}

function keyField(key: model.ApiKey, field: string): number | boolean | undefined {
  return (key as unknown as Record<string, number | boolean | undefined>)[field]
}

function formatUsage(value: number | boolean | undefined): string {
  const count = typeof value === 'number' ? value : 0
  return `${formatTokens(count)} ${t('apiKeys.tokenUnit')}`
}

function formatLastUsed(key: model.ApiKey): string {
  const value = keyField(key, 'last_used_at')
  return typeof value === 'number' && value > 0 ? format(value) : t('apiKeys.neverUsed')
}

function keyAllowedRuleIds(key: model.ApiKey): string[] {
  return ((key as unknown as { allowed_rule_ids?: string[] }).allowed_rule_ids || []).filter(Boolean)
}

function ruleName(id: string): string {
  const rule = (modelRules.value || []).find((item) => item.id === id)
  return rule?.name || id
}

function formatRuleAccess(key: model.ApiKey): string {
  const ids = keyAllowedRuleIds(key)
  if (!ids.length) return t('apiKeys.access.unrestricted')
  const names = ids.map(ruleName)
  const preview = names.slice(0, 2).join(', ')
  return names.length > 2
    ? t('apiKeys.access.allowedMany', { count: names.length, names: preview })
    : t('apiKeys.access.allowed', { names: preview })
}

function toggleRule(id: string, checked: boolean) {
  const next = new Set(form.value.allowed_rule_ids)
  if (checked) next.add(id)
  else next.delete(id)
  form.value.allowed_rule_ids = [...next]
}

function setUnrestricted() {
  form.value.allowed_rule_ids = []
}

async function toggleKeyEnabled(key: model.ApiKey) {
  const previous = keyField(key, 'enabled') !== false
  ;(key as unknown as Record<string, unknown>).enabled = !previous
  try {
    await api.setApiKeyEnabled(key.id, !previous)
  } catch (e: any) {
    ;(key as unknown as Record<string, unknown>).enabled = previous
    toast.push(t('toast.toggleFailed') + ': ' + (e?.message || String(e)), 'error')
  }
}

async function copyToken(token: string) {
  try {
    await navigator.clipboard.writeText(token)
    toast.push(t('toast.copiedToClipboard'), 'success')
  } catch (e: any) {
    toast.push(t('toast.copyFailed') + ': ' + (e?.message || String(e)), 'error')
  }
}

async function copyKey(key: model.ApiKey) {
  await copyToken(key.id)
}

async function deleteKey(id: string, name: string) {
  const ok = await confirm.open({
    title: t('confirm.deleteApiKeyTitle'),
    message: t('confirm.deleteApiKeyMessage', { name }),
    confirmText: t('common.delete'),
    danger: true,
  })
  if (!ok) return
  deleting.value = true
  try {
    await api.deleteApiKey(id)
    await loadKeys()
    toast.push(t('toast.apiKeyDeleted'), 'success')
  } catch (e: any) {
    toast.push(t('toast.deleteFailed') + ': ' + (e?.message || String(e)), 'error')
  } finally {
    deleting.value = false
  }
}

function openCreateModal() {
  modalMode.value = 'create'
  editingId.value = ''
  revealedToken.value = ''
  form.value = {
    name: '',
    expires_at: 0,
    allowed_rule_ids: [],
  }
  modalOpen.value = true
}

function openEditModal(key: model.ApiKey) {
  modalMode.value = 'edit'
  editingId.value = key.id
  revealedToken.value = ''
  form.value = {
    name: key.name,
    expires_at: key.expires_at,
    allowed_rule_ids: keyAllowedRuleIds(key),
  }
  modalOpen.value = true
}

function closeModal() {
  modalOpen.value = false
  revealedToken.value = ''
}

async function saveKey() {
  saving.value = true
  try {
    if (modalMode.value === 'edit') {
      await api.updateApiKey(editingId.value, form.value)
      modalOpen.value = false
      await loadKeys()
      toast.push(t('toast.apiKeyUpdated'), 'success')
    } else {
      const created = await api.createApiKey(form.value)
      revealedToken.value = created.id
      await loadKeys()
      toast.push(t('toast.apiKeyCreated'), 'success')
    }
  } catch (e: any) {
    toast.push(t('toast.saveFailed') + ': ' + (e?.message || String(e)), 'error')
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  loadKeys()
  loadModelRules()
})
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">{{ t('apiKeys.title') }}</h1>
      <span class="main-subtitle">{{ t('apiKeys.subtitle', { count: activeCount }) }}</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-primary" @click="openCreateModal">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
        {{ t('apiKeys.add') }}
      </button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner stack-loose">
      <!-- Loading / error -->
      <div v-if="keysLoading && !keys" class="text-muted" style="padding: 40px 0; text-align: center;">{{ t('apiKeys.loading') }}</div>
      <div v-else-if="keysError" class="text-muted" style="padding: 40px 0; text-align: center; color: var(--negative);">{{ t('apiKeys.loadFailed', { error: keysError }) }}</div>
      <template v-else>
        <!-- summary stats -->
        <div class="stat-grid">
          <div class="stat-card">
            <div class="stat-label">{{ t('apiKeys.activeTokens') }}</div>
            <div class="stat-value">{{ activeCount }}</div>
            <div class="stat-meta"><span>{{ t('apiKeys.activeTokensSub') }}</span></div>
          </div>
          <div class="stat-card">
            <div class="stat-label">{{ t('apiKeys.expiringSoon') }}</div>
            <div class="stat-value">{{ expiringCount }}</div>
            <div class="stat-meta">
              <span v-if="expiringCount" class="delta negative">{{ t('apiKeys.withinDays', { days: 14 }) }}</span>
              <span v-else>—</span>
            </div>
          </div>
        </div>

        <!-- filter bar -->
        <div class="row" style="gap: 8px;">
          <div class="row" style="background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 6px 10px; gap: 6px; flex: 1; max-width: 360px;">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;color:var(--muted);"><circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/></svg>
            <input v-model="search" class="input" style="border: none; padding: 0; font-size: 13px;" :placeholder="t('apiKeys.searchPlaceholder')">
          </div>
        </div>

        <!-- Empty state -->
        <div v-if="!filteredKeys.length" class="text-muted" style="padding: 40px 0; text-align: center;">{{ t('apiKeys.empty') }}</div>

        <!-- keys table -->
        <div v-else class="card" style="padding: 0; overflow: hidden;">
          <table class="tbl">
            <thead>
              <tr>
                <th>{{ t('apiKeys.columns.name') }}</th>
                <th>{{ t('apiKeys.columns.token') }}</th>
                <th>{{ t('apiKeys.columns.status') }}</th>
                <th>{{ t('apiKeys.columns.usage') }}</th>
                <th>{{ t('apiKeys.columns.expires') }}</th>
                <th class="right">{{ t('apiKeys.columns.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="key in filteredKeys" :key="key.id">
                <td>
                  <div style="font-weight: 500;">{{ key.name }}</div>
                  <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">{{ t('apiKeys.createdAt') }} · {{ format(key.created_at) }}</div>
                  <div class="api-key-access" :title="formatRuleAccess(key)">
                    <span>{{ formatRuleAccess(key) }}</span>
                  </div>
                </td>
                <td>
                  <div class="row" style="gap: 6px;">
                    <span class="text-mono" style="font-size: 12.5px; color: var(--muted);">{{ tokenDisplay(key.id) }}</span>
                    <button class="btn btn-icon" style="width: 22px; height: 22px;" :title="t('common.copy')" @click="copyKey(key)">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
                    </button>
                  </div>
                </td>
                <td>
                  <label class="toggle toggle-sm" :aria-label="keyField(key, 'enabled') === false ? t('apiKeys.enable') : t('apiKeys.disable')" @click.stop>
                    <input type="checkbox" :checked="keyField(key, 'enabled') !== false" @change="toggleKeyEnabled(key)">
                    <span class="toggle-slider blue"></span>
                  </label>
                </td>
                <td class="api-key-usage">
                  <div><span class="text-muted">{{ t('apiKeys.today') }}</span> <span class="text-mono">{{ formatUsage(keyField(key, 'today_tokens')) }}</span></div>
                  <div><span class="text-muted">{{ t('apiKeys.last30Days') }}</span> <span class="text-mono">{{ formatUsage(keyField(key, 'thirty_day_tokens')) }}</span></div>
                  <div class="text-muted" style="font-size: 11px;">{{ t('apiKeys.lastUsed') }} · {{ formatLastUsed(key) }}</div>
                </td>
                <td>
                  <span :class="{ 'text-muted': !key.expires_at }">{{ formatExpiresAt(key.expires_at) }}</span>
                </td>
                <td class="right">
                  <DropdownMenu :menu-id="key.id">
                    <template #trigger="{ toggle, open }">
                      <button
                        class="btn btn-icon"
                        :aria-expanded="open"
                        aria-haspopup="menu"
                        :aria-label="t('apiKeys.moreActions', { name: key.name })"
                        @click="toggle"
                      >
                        <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>
                      </button>
                    </template>
                    <template #menu="{ close }">
                      <button class="dropdown-item" role="menuitem" @click="openEditModal(key); close()">{{ t('common.edit') }}</button>
                      <button class="dropdown-item" role="menuitem" @click="copyKey(key); close()">{{ t('common.copy') }}</button>
                      <button class="dropdown-item danger" role="menuitem" :disabled="deleting" @click="deleteKey(key.id, key.name); close()">{{ t('common.delete') }}</button>
                    </template>
                  </DropdownMenu>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>
  </div>

  <!-- Create / edit modal -->
  <Teleport to="body">
    <div v-if="modalOpen" class="modal-overlay" @click.self="closeModal">
      <div class="modal-card modal-card-scroll">
        <div v-if="revealedToken" class="modal-title">{{ t('apiKeys.modal.saveTitle') }}</div>
        <div v-else class="modal-title">{{ modalMode === 'edit' ? t('apiKeys.modal.edit') : t('apiKeys.modal.add') }}</div>

        <template v-if="revealedToken">
          <div class="field">
            <label class="field-label">{{ t('apiKeys.modal.accessToken') }}</label>
            <div class="row" style="gap: 6px;">
              <input
                :value="revealedToken"
                readonly
                class="input mono"
                style="flex: 1;"
              >
              <button class="btn btn-secondary" @click="copyToken(revealedToken)">{{ t('common.copy') }}</button>
            </div>
            <div class="field-help">{{ t('apiKeys.modal.tokenHelp') }}</div>
          </div>
          <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
            <button class="btn btn-primary" @click="closeModal">{{ t('apiKeys.modal.done') }}</button>
          </div>
        </template>

        <template v-else>
          <div class="field">
            <label class="field-label">{{ t('apiKeys.modal.name') }}</label>
            <input v-model="form.name" class="input" :placeholder="t('apiKeys.modal.namePlaceholder')">
          </div>
          <div class="field">
            <label class="field-label">{{ t('apiKeys.modal.expires') }}</label>
            <input
              :value="toDateTimeLocal(form.expires_at)"
              type="datetime-local"
              class="input input-datetime"
              @input="form.expires_at = fromDateTimeLocal(($event.target as HTMLInputElement).value)"
            >
            <div class="field-help">{{ t('apiKeys.modal.expiresHelp') }}</div>
          </div>
          <div class="field">
            <label class="field-label">{{ t('apiKeys.modal.ruleAccess') }}</label>
            <details class="rule-picker">
              <summary class="rule-picker-trigger">
                <span>{{ form.allowed_rule_ids.length ? t('apiKeys.modal.rulesSelected', { count: form.allowed_rule_ids.length }) : t('apiKeys.modal.unrestricted') }}</span>
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"/></svg>
              </summary>
              <div class="rule-picker-panel">
                <label class="rule-option rule-option-unrestricted">
                  <input type="checkbox" :checked="form.allowed_rule_ids.length === 0" @change="setUnrestricted">
                  <span><strong>{{ t('apiKeys.modal.unrestricted') }}</strong><small>{{ t('apiKeys.modal.unrestrictedHelp') }}</small></span>
                </label>
                <div v-if="modelRulesLoading" class="rule-picker-message">{{ t('apiKeys.modal.rulesLoading') }}</div>
                <div v-else-if="modelRulesError" class="rule-picker-message rule-picker-error">{{ t('apiKeys.modal.rulesLoadFailed') }}</div>
                <div v-else-if="!modelRules?.length" class="rule-picker-message">{{ t('apiKeys.modal.noRules') }}</div>
                <label v-for="rule in modelRules || []" v-else :key="rule.id" class="rule-option">
                  <input type="checkbox" :checked="form.allowed_rule_ids.includes(rule.id)" @change="toggleRule(rule.id, ($event.target as HTMLInputElement).checked)">
                  <span>{{ rule.name || rule.id }}</span>
                </label>
              </div>
            </details>
            <div class="field-help">{{ t('apiKeys.modal.ruleAccessHelp') }}</div>
          </div>
          <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
            <button class="btn btn-secondary" @click="closeModal">{{ t('common.cancel') }}</button>
            <button class="btn btn-primary" :disabled="saving" @click="saveKey">{{ saving ? t('common.processing') : t('common.save') }}</button>
          </div>
        </template>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.api-key-access {
  display: flex;
  max-width: 260px;
  margin-top: 4px;
  color: var(--muted);
  font-size: 11px;
  line-height: 1.35;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rule-picker-trigger {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 38px;
  padding: 8px 10px;
  border: 1px solid var(--border);
  border-radius: 7px;
  background: var(--surface);
  color: var(--text);
  cursor: pointer;
  font-size: 13px;
  list-style: none;
}

.rule-picker-trigger::-webkit-details-marker { display: none; }
.rule-picker-trigger svg { width: 15px; height: 15px; color: var(--muted); flex: 0 0 auto; }
.rule-picker[open] .rule-picker-trigger { border-color: var(--accent); }
.rule-picker-panel {
  display: grid;
  gap: 2px;
  max-height: 220px;
  overflow-y: auto;
  margin-top: 5px;
  padding: 5px;
  border: 1px solid var(--border);
  border-radius: 7px;
  background: var(--surface);
  box-shadow: 0 8px 22px rgb(0 0 0 / 10%);
}
.rule-option {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 7px 6px;
  border-radius: 5px;
  color: var(--text);
  cursor: pointer;
  font-size: 13px;
}
.rule-option:hover { background: var(--accent-soft); }
.rule-option input { margin-top: 2px; accent-color: var(--accent); }
.rule-option-unrestricted { border-bottom: 1px solid var(--border); margin-bottom: 2px; }
.rule-option strong, .rule-option small { display: block; }
.rule-option small { margin-top: 2px; color: var(--muted); font-size: 11px; }
.rule-picker-message { padding: 9px 6px; color: var(--muted); font-size: 12px; }
.rule-picker-error { color: var(--negative); }

@media (max-width: 720px) {
  .api-key-access { max-width: 180px; }
}
</style>
