<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '../api/bridge'
import { useApi } from '../composables/useApi'
import { useRelativeTime } from '../composables/useRelativeTime'
import { toDateTimeLocal, fromDateTimeLocal } from '../composables/useDateTime'
import { useToast } from '../composables/useToast'
import { useConfirm } from '../composables/useConfirm'
import DropdownMenu from '@/components/DropdownMenu.vue'
import type { model } from '../../wailsjs/go/models'

const { t } = useI18n()
const { format } = useRelativeTime()
const toast = useToast()
const confirm = useConfirm()

const {
  data: keys,
  loading: keysLoading,
  error: keysError,
  execute: loadKeys,
} = useApi(() => api.apiKeys())

const search = ref('')
const modalOpen = ref(false)
const modalMode = ref<'create' | 'edit'>('create')
const editingId = ref('')
const saving = ref(false)
const deleting = ref(false)
const revealedToken = ref('')

const form = ref<model.ApiKeyInput>({
  name: '',
  expires_at: 0,
})

const filteredKeys = computed(() => {
  let list = keys.value || []
  const q = search.value.trim().toLowerCase()
  if (q) {
    list = list.filter((k) => k.name.toLowerCase().includes(q))
  }
  return list
})

const activeCount = computed(() => (keys.value || []).length)

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
                <th>{{ t('apiKeys.columns.expires') }}</th>
                <th class="right">{{ t('apiKeys.columns.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="key in filteredKeys" :key="key.id">
                <td>
                  <div style="font-weight: 500;">{{ key.name }}</div>
                  <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">{{ format(key.created_at) }}</div>
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
      <div class="modal-card">
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
          <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
            <button class="btn btn-secondary" @click="closeModal">{{ t('common.cancel') }}</button>
            <button class="btn btn-primary" :disabled="saving" @click="saveKey">{{ saving ? t('common.processing') : t('common.save') }}</button>
          </div>
        </template>
      </div>
    </div>
  </Teleport>
</template>
