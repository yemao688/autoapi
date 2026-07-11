<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '../api/bridge'
import { useApi } from '../composables/useApi'
import { useProviderStyle } from '../composables/useProviderStyle'
import { useFormatters } from '../composables/useFormatters'
import { useToast } from '../composables/useToast'
import { useConfirm } from '../composables/useConfirm'
import DropdownMenu from '@/components/DropdownMenu.vue'
import TestModelChatModal from '@/components/TestModelChatModal.vue'
import type { model } from '../../wailsjs/go/models'

const { t } = useI18n()
const { color: providerColor, initial: providerLetter } = useProviderStyle()
const { tokens: fmtTokens, latency: fmtLatency } = useFormatters()
const toast = useToast()
const confirm = useConfirm()

const {
  data: providers,
  loading: providersLoading,
  error: providersError,
  execute: loadProviders,
} = useApi(() => api.providers())

const modelsMap = ref<Record<string, model.Model[]>>({})

const activeTab = ref<'all' | 'connected' | 'error'>('all')
const search = ref('')
const sortBy = ref<'usage' | 'name' | 'last_tested'>('usage')

const modalOpen = ref(false)
const modalMode = ref<'add' | 'edit'>('add')
const editingId = ref('')
const saving = ref(false)
const deleting = ref(false)
const testingIds = ref<Set<string>>(new Set())

const models = ref<model.Model[]>([])
const testingModelIds = ref<Set<string>>(new Set())
const chatTestModal = ref({
  open: false,
  providerId: '',
  providerName: '',
  modelName: '',
})
const fetchingModels = ref(false)

// Monotonic generation token to guard against stale async responses after
// close/reopen of the same provider modal.
const modalGeneration = ref(0)

// In-flight guard for model mutations (add/rename/delete) to prevent double-submit.
const mutationBusy = ref(false)

// API key visibility / dirty tracking
const keyVisible = ref(false)
const keyDirty = ref(false)
const originalKey = ref('')
const loadingKey = ref(false)

// Fetch-from-upstream selection modal
const selectionModalOpen = ref(false)
const fetchedModels = ref<model.Model[]>([])
const selectedModelNames = ref<Set<string>>(new Set())

// Manual add inline input
const manualAddVisible = ref(false)
const manualAddName = ref('')

// Inline model rename
const editingModelId = ref('')
const editingModelName = ref('')
const renameInputRef = ref<HTMLInputElement | null>(null)

const form = ref<model.ProviderInput>({
  name: '',
  base_url: '',
  upstream_key: '',
  is_custom: false,
})

const filteredProviders = computed(() => {
  let list = providers.value || []
  if (activeTab.value === 'connected') {
    list = list.filter((p) => p.status === 'connected')
  } else if (activeTab.value === 'error') {
    list = list.filter((p) => p.status === 'error' || p.status === 'unknown')
  }
  const q = search.value.trim().toLowerCase()
  if (q) {
    list = list.filter((p) => p.name.toLowerCase().includes(q))
  }
  const sorted = [...list]
  if (sortBy.value === 'usage') {
    sorted.sort((a, b) => b.monthly_tokens - a.monthly_tokens)
  } else if (sortBy.value === 'name') {
    sorted.sort((a, b) => a.name.localeCompare(b.name))
  } else if (sortBy.value === 'last_tested') {
    sorted.sort((a, b) => b.last_tested_at - a.last_tested_at)
  }
  return sorted
})

const totalModelCount = computed(() =>
  (providers.value || []).reduce((sum, p) => sum + p.models_count, 0)
)

const currentProviderName = computed(
  () => (providers.value || []).find((p) => p.id === editingId.value)?.name || ''
)

// Only show models in the selection modal that aren't already in the local
// catalog. This prevents "Select all" from re-adding existing models.
const addableFetchedModels = computed(() => {
  const existing = new Set(models.value.map((m) => m.name))
  return fetchedModels.value.filter((m) => !existing.has(m.name))
})

const allSelected = computed(
  () =>
    addableFetchedModels.value.length > 0 &&
    selectedModelNames.value.size === addableFetchedModels.value.length
)

async function toggleProviderEnabled(provider: model.Provider) {
  // Optimistic update — flip locally first so the card reacts instantly.
  const prev = provider.enabled
  provider.enabled = !prev
  try {
    await api.setProviderEnabled(provider.id, !prev)
  } catch (e: any) {
    // Revert on failure so UI stays consistent with server.
    provider.enabled = prev
    toast.push(t('toast.toggleFailed') + ': ' + (e?.message || String(e)), 'error')
  }
}

async function loadModels() {
  const list = providers.value || []
  const entries: Record<string, model.Model[]> = {}
  await Promise.all(
    list.map(async (p) => {
      try {
        entries[p.id] = await api.listModels(p.id)
      } catch {
        entries[p.id] = []
      }
    })
  )
  modelsMap.value = entries
}

async function refresh() {
  await loadProviders()
  await loadModels()
}

async function refreshModels() {
  if (!editingId.value) return
  const idAtCall = editingId.value
  try {
    const list = await api.listModels(idAtCall)
    // Race-condition guard: only apply if the modal is still open for the
    // same provider.
    if (editingId.value === idAtCall && modalOpen.value) {
      models.value = list
      // If the row being edited disappeared (deleted / renamed), bail out.
      if (editingModelId.value && !list.find((m) => m.id === editingModelId.value)) {
        cancelEditModel()
      }
    }
    // Also refresh the card's badge map for this provider.
    modelsMap.value = { ...modelsMap.value, [idAtCall]: list }
  } catch {
    // ignore — keep stale list visible
  }
}

async function testOne(id: string) {
  testingIds.value.add(id)
  try {
    const res = await api.testProvider(id)
    toast.push(
      res.ok
        ? t('providers.testResult.success', { ms: res.latency_ms, count: res.models.length })
        : t('providers.testResult.failed', { error: res.error || t('providers.testResult.failedUnknown') }),
      res.ok ? 'success' : 'error'
    )
  } catch (e: any) {
    toast.push(t('toast.saveFailed') + ': ' + (e?.message || String(e)), 'error')
  } finally {
    testingIds.value.delete(id)
  }
  await refresh()
}

async function testAll() {
  try {
    await api.testAllProviders()
    toast.push(t('providers.testResult.allDone'), 'success')
  } catch (e: any) {
    toast.push(t('providers.testResult.allFailed', { error: e?.message || String(e) }), 'error')
  }
  await refresh()
}

function formatContext(n: number): string {
  if (!n) return '—'
  if (n >= 1000) return `${Math.round(n / 1000)}K`
  return String(n)
}

async function copyToClipboard(text: string): Promise<boolean> {
  if (!text) return false
  try {
    if (navigator.clipboard) {
      await navigator.clipboard.writeText(text)
      return true
    } else {
      const ta = document.createElement('textarea')
      ta.value = text
      document.body.appendChild(ta)
      ta.select()
      const ok = document.execCommand('copy')
      document.body.removeChild(ta)
      return ok
    }
  } catch {
    return false
  }
}

function toggleKeyVisibility() {
  keyVisible.value = !keyVisible.value
}

async function copyUpstreamKey() {
  const k = form.value.upstream_key
  if (!k) return
  const ok = await copyToClipboard(k)
  toast.push(ok ? t('providers.modal.keyCopied') : t('toast.copyFailed'), ok ? 'success' : 'error')
}

function onKeyInput() {
  keyDirty.value = form.value.upstream_key !== originalKey.value
}

async function fetchModels() {
  if (!editingId.value) return
  const gen = modalGeneration.value
  fetchingModels.value = true
  try {
    const list = await api.fetchUpstreamModels(editingId.value)
    if (modalGeneration.value !== gen) return
    fetchedModels.value = list
    if (!list.length) {
      toast.push(t('providers.modal.noModelsFetched'), 'error')
      return
    }
    // Pre-select all addable models (those not already in local catalog).
    selectedModelNames.value = new Set(addableFetchedModels.value.map((m) => m.name))
    selectionModalOpen.value = true
  } catch (e: any) {
    if (modalGeneration.value !== gen) return
    toast.push(e?.message || String(e), 'error')
  } finally {
    if (modalGeneration.value === gen) {
      fetchingModels.value = false
    }
  }
}

function closeSelectionModal() {
  selectionModalOpen.value = false
}

function toggleAllSelection() {
  if (allSelected.value) {
    selectedModelNames.value = new Set()
  } else {
    selectedModelNames.value = new Set(addableFetchedModels.value.map((m) => m.name))
  }
}

function toggleModelSelection(name: string) {
  const set = new Set(selectedModelNames.value)
  if (set.has(name)) set.delete(name)
  else set.add(name)
  selectedModelNames.value = set
}

async function confirmAddSelected() {
  if (mutationBusy.value) return
  if (!editingId.value) return
  const names = Array.from(selectedModelNames.value)
  if (!names.length) {
    closeSelectionModal()
    return
  }
  mutationBusy.value = true
  try {
    await api.addProviderModels(editingId.value, names)
    toast.push(t('providers.modal.modelsAdded', { count: names.length }), 'success')
    closeSelectionModal()
    await refreshModels()
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    mutationBusy.value = false
  }
}

function openManualAdd() {
  manualAddVisible.value = true
  manualAddName.value = ''
}

function closeManualAdd() {
  manualAddVisible.value = false
  manualAddName.value = ''
}

async function confirmManualAdd() {
  if (mutationBusy.value) return
  const name = manualAddName.value.trim()
  if (!name || !editingId.value) return
  if (models.value.some((m) => m.name === name)) {
    toast.push(t('providers.modal.alreadyAdded'), 'error')
    return
  }
  mutationBusy.value = true
  try {
    await api.addProviderModels(editingId.value, [name])
    toast.push(t('providers.modal.modelAdded', { name }), 'success')
    manualAddName.value = ''
    await refreshModels()
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    mutationBusy.value = false
  }
}

function startEditModel(m: model.Model) {
  if (mutationBusy.value) return
  editingModelId.value = m.id
  editingModelName.value = m.name
  nextTick(() => {
    renameInputRef.value?.focus()
    renameInputRef.value?.select()
  })
}

function cancelEditModel() {
  editingModelId.value = ''
  editingModelName.value = ''
}

async function saveEditModel(oldName: string) {
  if (mutationBusy.value) return
  const newName = editingModelName.value.trim()
  if (!newName || newName === oldName) {
    cancelEditModel()
    return
  }
  if (!editingId.value) return
  if (models.value.some((m) => m.name === newName && m.name !== oldName)) {
    toast.push(t('providers.modal.alreadyAdded'), 'error')
    return
  }
  mutationBusy.value = true
  try {
    await api.updateModelName(editingId.value, oldName, newName)
    toast.push(t('providers.modal.modelRenamed', { name: newName }), 'success')
    cancelEditModel()
    await refreshModels()
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    mutationBusy.value = false
  }
}

async function deleteModelConfirm(m: model.Model) {
  if (mutationBusy.value) return
  const ok = await confirm.open({
    title: t('providers.modal.deleteModel'),
    message: t('providers.modal.deleteModelConfirm', { name: m.name }),
    confirmText: t('common.delete'),
    danger: true,
  })
  if (!ok || !editingId.value) return
  mutationBusy.value = true
  try {
    await api.deleteModel(editingId.value, m.name)
    toast.push(t('providers.modal.modelDeleted', { name: m.name }), 'success')
    if (editingModelId.value === m.id) cancelEditModel()
    await refreshModels()
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    mutationBusy.value = false
  }
}

async function clearAllModels() {
  if (!editingId.value) return
  const ok = await confirm.open({
    title: t('providers.clearModels'),
    message: t('providers.clearModelsConfirm'),
    confirmText: t('common.delete'),
    danger: true,
  })
  if (!ok) return
  mutationBusy.value = true
  try {
    await api.clearProviderModels(editingId.value)
    models.value = []
    toast.push(t('providers.modelsCleared'), 'success')
  } catch (e: any) {
    toast.push(t('providers.modelsClearFailed') + ': ' + (e?.message || ''), 'error')
  } finally {
    mutationBusy.value = false
  }
}

async function toggleModelActive(m: model.Model) {
  if (!editingId.value) return
  try {
    await api.setModelsActive(editingId.value, [m.name], !m.active)
    m.active = !m.active
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  }
}

async function testModelLatency(m: model.Model) {
  if (!editingId.value) return
  testingModelIds.value.add(m.id)
  try {
    const result = await api.testModelLatency(editingId.value, m.name)
    if (result.ok) {
      m.latency_ms = result.latency_ms
      toast.push(t('providers.testResult.latency', { name: m.name, ms: result.latency_ms }), 'success')
    } else {
      toast.push(result.error || t('providers.testResult.failedUnknown'), 'error')
    }
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    testingModelIds.value.delete(m.id)
  }
}

function openChatTest(model: model.Model) {
  chatTestModal.value = {
    open: true,
    providerId: editingId.value,
    providerName: form.value.name,
    modelName: model.name,
  }
}

function resetModalState() {
  // Clear all secondary modal state to prevent stale data leaking between sessions.
  selectionModalOpen.value = false
  fetchedModels.value = []
  selectedModelNames.value = new Set()
  manualAddVisible.value = false
  manualAddName.value = ''
  editingModelId.value = ''
  editingModelName.value = ''
  keyVisible.value = false
  keyDirty.value = false
  originalKey.value = ''
  loadingKey.value = false
  fetchingModels.value = false
  mutationBusy.value = false
}

function openAdd(isCustom: boolean) {
  modalGeneration.value++
  resetModalState()
  modalMode.value = 'add'
  editingId.value = ''
  form.value = { name: '', base_url: '', upstream_key: '', is_custom: isCustom }
  modalOpen.value = true
}

function openEdit(provider: model.Provider) {
  modalGeneration.value++
  resetModalState()
  modalMode.value = 'edit'
  editingId.value = provider.id
  form.value = {
    name: provider.name,
    base_url: provider.base_url,
    upstream_key: '',
    is_custom: provider.is_custom,
  }
  loadingKey.value = true
  models.value = []
  modalOpen.value = true

  const gen = modalGeneration.value

  // Fetch the cleartext key. Guard against race conditions: if the user
  // closes or reopens the modal before this resolves, drop the result.
  void api
    .getProviderKey(provider.id)
    .then((key) => {
      if (modalGeneration.value !== gen) return
      originalKey.value = key
      form.value.upstream_key = key
      keyDirty.value = false
    })
    .catch(() => {
      // Couldn't decrypt — leave the field empty so the user can re-enter.
    })
    .finally(() => {
      if (modalGeneration.value === gen) {
        loadingKey.value = false
      }
    })

  // Refresh the local model list.
  void api
    .listModels(provider.id)
    .then((list) => {
      if (modalGeneration.value !== gen) return
      models.value = list
    })
    .catch(() => {
      if (modalGeneration.value === gen) {
        models.value = []
      }
    })
}

async function saveProvider() {
  saving.value = true
  try {
    // Build the payload. In edit mode, if the key was not modified by
    // the user, send an empty string so the backend keeps the existing
    // key. If it was modified, send the new value.
    const payload: model.ProviderInput = {
      name: form.value.name,
      base_url: form.value.base_url,
      upstream_key: form.value.upstream_key,
      is_custom: form.value.is_custom,
    }
    if (modalMode.value === 'edit' && !keyDirty.value) {
      payload.upstream_key = ''
    }
    if (modalMode.value === 'edit') {
      await api.updateProvider(editingId.value, payload)
      modalOpen.value = false
      resetModalState()
      await refresh()
      toast.push(t('toast.providerSaved'), 'success')
    } else {
      // After successful creation, stay in the modal but switch to edit
      // mode so the user can immediately manage the upstream's available
      // models without going through the list again.
      const created = await api.createProvider(payload)
      // Bump the generation token so any in-flight openEdit() callbacks
      // from a previous session drop their results against stale state.
      modalGeneration.value++
      editingId.value = created.id
      modalMode.value = 'edit'
      originalKey.value = form.value.upstream_key
      keyDirty.value = false
      // Sequential refresh: load providers first so the model map has
      // the new provider, then load models for the new provider.
      await loadProviders()
      await loadModels()
      // Load this provider's models for the modal section. Use a fresh
      // generation token so the result lands cleanly.
      const gen = modalGeneration.value
      try {
        const list = await api.listModels(created.id)
        if (modalGeneration.value === gen && editingId.value === created.id) {
          models.value = list
        }
      } catch {
        if (modalGeneration.value === gen && editingId.value === created.id) {
          models.value = []
        }
      }
      toast.push(t('providers.modal.createdCanManage'), 'success')
    }
  } catch (e: any) {
    toast.push(t('toast.saveFailed') + ': ' + (e?.message || String(e)), 'error')
  } finally {
    saving.value = false
  }
}

function closeModal() {
  modalGeneration.value++
  modalOpen.value = false
  models.value = []
  testingModelIds.value.clear()
  form.value.upstream_key = ''
  resetModalState()
}

async function deleteProvider(id: string, name: string) {
  const ok = await confirm.open({
    title: t('confirm.deleteProviderTitle'),
    message: t('confirm.deleteProviderMessage', { name }),
    confirmText: t('common.delete'),
    danger: true,
  })
  if (!ok) return
  deleting.value = true
  try {
    await api.deleteProvider(id)
    await refresh()
    toast.push(t('toast.providerDeleted'), 'success')
  } catch (e: any) {
    toast.push(t('toast.deleteFailed') + ': ' + (e?.message || String(e)), 'error')
  } finally {
    deleting.value = false
  }
}

function handleTabKeydown(e: KeyboardEvent) {
  const container = e.currentTarget as HTMLElement
  const tabs = container.querySelectorAll<HTMLButtonElement>('.tab')
  if (!tabs.length) return
  const currentIdx = Array.from(tabs).findIndex((t) => t === document.activeElement)
  let nextIdx = currentIdx
  switch (e.key) {
    case 'ArrowRight':
    case 'ArrowDown':
      e.preventDefault()
      nextIdx = (currentIdx + 1) % tabs.length
      break
    case 'ArrowLeft':
    case 'ArrowUp':
      e.preventDefault()
      nextIdx = (currentIdx - 1 + tabs.length) % tabs.length
      break
    case 'Home':
      e.preventDefault()
      nextIdx = 0
      break
    case 'End':
      e.preventDefault()
      nextIdx = tabs.length - 1
      break
    default:
      return
  }
  tabs[nextIdx]?.focus()
  tabs[nextIdx]?.click()
}

onMounted(() => {
  refresh()
})
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">{{ t('providers.title') }}</h1>
      <span class="main-subtitle">{{ t('providers.subtitle', { count: providers?.length ?? 0, models: totalModelCount }) }}</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary" :disabled="providersLoading" @click="testAll">
        {{ providersLoading ? t('providers.testing') : t('providers.testAll') }}
      </button>
      <button class="btn btn-primary" @click="openAdd(false)">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
        {{ t('providers.add') }}
      </button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner stack-loose">
      <!-- Loading / error -->
      <div v-if="providersLoading && !providers" class="text-muted" style="padding: 40px 0; text-align: center;">{{ t('providers.loading') }}</div>
      <div v-else-if="providersError" class="text-muted" style="padding: 40px 0; text-align: center; color: var(--negative);">{{ t('providers.loadFailed', { error: providersError }) }}</div>
      <template v-else>
        <!-- Filter bar -->
        <div class="row" style="gap: 8px; flex-wrap: wrap;">
          <div class="row" style="background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 6px 10px; gap: 6px; flex: 1; max-width: 360px;">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;color:var(--muted);"><circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/></svg>
            <input v-model="search" class="input" style="border: none; padding: 0; font-size: 13px;" :placeholder="t('providers.searchPlaceholder')">
          </div>
          <div class="tabs" tabindex="0" @keydown="handleTabKeydown" style="outline: none;">
            <button class="tab" :class="{ active: activeTab === 'all' }" @click="activeTab = 'all'">{{ t('providers.tabs.all') }}</button>
            <button class="tab" :class="{ active: activeTab === 'connected' }" @click="activeTab = 'connected'">{{ t('providers.tabs.connected') }}</button>
            <button class="tab" :class="{ active: activeTab === 'error' }" @click="activeTab = 'error'">{{ t('providers.tabs.error') }}</button>
          </div>
          <div class="spacer"></div>
          <div class="row" style="font-size: 12px; color: var(--muted);">
            {{ t('providers.sort') }}
            <select v-model="sortBy" class="select" style="width: auto; padding: 5px 10px; font-size: 12px;">
              <option value="usage">{{ t('providers.sortBy.usage') }}</option>
              <option value="name">{{ t('providers.sortBy.name') }}</option>
              <option value="last_tested">{{ t('providers.sortBy.lastTested') }}</option>
            </select>
          </div>
        </div>

        <!-- Empty state -->
        <div v-if="!filteredProviders.length" class="text-muted" style="padding: 40px 0; text-align: center;">{{ t('providers.empty') }}</div>

        <!-- Provider cards grid -->
        <section v-else class="col-2">
          <article
            v-for="provider in filteredProviders"
            :key="provider.id"
            class="card card-hover provider-card"
            :class="{ 'provider-disabled': !provider.enabled }"
          >
            <div class="row-between" style="margin-bottom: 14px;">
              <div class="row" style="gap: 12px; min-width: 0;">
                <div class="list-icon" :style="{ background: providerColor(provider.name), color: 'white', width: '38px', height: '38px', fontSize: '15px' }">
                  {{ providerLetter(provider.name) }}
                </div>
                <div style="min-width: 0;">
                  <div style="font-size: 15px; font-weight: 600;">{{ provider.name }}</div>
                  <div class="text-mono text-muted" style="font-size: 11.5px; margin-top: 1px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;">{{ provider.base_url }}</div>
                </div>
              </div>
              <label
                class="toggle toggle-sm"
                :aria-label="provider.enabled ? t('providers.disable') : t('providers.enable')"
                @click.stop
              >
                <input
                  type="checkbox"
                  :checked="provider.enabled"
                  @change="toggleProviderEnabled(provider)"
                >
                <span class="toggle-slider blue"></span>
              </label>
            </div>
            <div class="h-divider" style="margin: 0 0 14px;"></div>
            <div class="row-between" style="margin-bottom: 10px;">
              <span class="text-muted" style="font-size: 12px;">{{ t('providers.monthlyUsage') }}</span>
              <span class="text-mono" style="font-size: 13px; font-weight: 500;">{{ fmtTokens(provider.monthly_tokens) }} tokens</span>
            </div>
            <div class="row-between" style="margin-bottom: 14px;">
              <span class="text-muted" style="font-size: 12px;">{{ provider.status === 'connected' ? t('providers.avgLatency') : t('providers.lastError') }}</span>
              <span class="text-mono" :style="{ fontSize: '13px', fontWeight: 500, color: provider.status === 'connected' ? 'inherit' : 'var(--negative)' }">
                {{ provider.status === 'connected' ? fmtLatency(provider.avg_latency_ms) : (provider.error_message || '—') }}
              </span>
            </div>
            <div class="row" style="flex-wrap: wrap; gap: 4px; margin-bottom: 14px;">
              <template v-if="modelsMap[provider.id]?.length">
                <span v-for="m in modelsMap[provider.id]" :key="m.id" class="badge mono">{{ m.name }}</span>
              </template>
              <span v-else class="badge mono" style="color: var(--muted);">{{ t('providers.modelCount', { count: provider.models_count }) }}</span>
            </div>
            <div class="row" style="justify-content: flex-end; gap: 4px;">
              <button
                class="btn"
                :class="provider.status === 'connected' ? 'btn-secondary' : 'btn-primary'"
                style="padding: 4px 10px; font-size: 12px;"
                :disabled="testingIds.has(provider.id)"
                @click="testOne(provider.id)"
              >
                {{ testingIds.has(provider.id) ? t('providers.testing') : (provider.status === 'connected' ? t('providers.test') : t('providers.reconnect')) }}
              </button>
              <button class="btn btn-icon" :title="t('common.edit')" @click="openEdit(provider)">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4z"/></svg>
              </button>
              <DropdownMenu :menu-id="provider.id">
                <template #trigger="{ toggle, open }">
                  <button
                    class="btn btn-icon"
                    :title="t('providers.more')"
                    :aria-expanded="open"
                    aria-haspopup="menu"
                    :aria-label="t('providers.moreActions', { name: provider.name })"
                    @click="toggle"
                  >
                    <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>
                  </button>
                </template>
                <template #menu="{ close }">
                  <button class="dropdown-item" role="menuitem" @click="openEdit(provider); close()">{{ t('common.edit') }}</button>
                  <button class="dropdown-item danger" role="menuitem" :disabled="deleting" @click="deleteProvider(provider.id, provider.name); close()">{{ t('common.delete') }}</button>
                </template>
              </DropdownMenu>
            </div>
          </article>

          <article class="card card-hover" style="border-style: dashed; display: flex; align-items: center; justify-content: center; min-height: 240px; background: transparent; cursor: pointer;" @click="openAdd(true)">
            <div style="text-align: center; color: var(--muted);">
              <div style="width: 48px; height: 48px; border-radius: 24px; background: rgba(0, 113, 227, 0.08); display: inline-flex; align-items: center; justify-content: center; margin-bottom: 12px;">
                <svg viewBox="0 0 24 24" fill="none" stroke="#0071e3" stroke-width="1.6" stroke-linecap="round" style="width:22px;height:22px;"><path d="M12 5v14M5 12h14"/></svg>
              </div>
              <div style="font-size: 14px; font-weight: 500; color: var(--fg);">{{ t('providers.addCustom') }}</div>
              <div style="font-size: 12px; margin-top: 4px;">{{ t('providers.addCustomHint') }}</div>
            </div>
          </article>
        </section>
      </template>
    </div>
  </div>

  <!-- Provider modal -->
  <Teleport to="body">
    <div v-if="modalOpen" class="modal-overlay" @click.self="closeModal">
      <div class="modal-card wide">
        <div class="modal-title">{{ modalMode === 'edit' ? t('providers.modal.edit') : (form.is_custom ? t('providers.modal.addCustom') : t('providers.modal.add')) }}</div>
        <div class="field">
          <label class="field-label">{{ t('providers.modal.name') }}</label>
          <input v-model="form.name" class="input" :placeholder="t('providers.modal.namePlaceholder')">
        </div>
        <div class="field">
          <label class="field-label">{{ t('providers.modal.baseUrl') }}</label>
          <input v-model="form.base_url" class="input mono" :placeholder="t('providers.modal.baseUrlPlaceholder')">
        </div>
        <div class="field">
          <div class="row-between" style="margin-bottom: 6px;">
            <label class="field-label" style="margin: 0;">{{ t('providers.modal.upstreamKey') }}</label>
            <div class="row" style="gap: 4px;">
              <button
                type="button"
                class="btn btn-icon"
                :title="keyVisible ? t('providers.modal.hideKey') : t('providers.modal.showKey')"
                :aria-label="keyVisible ? t('providers.modal.hideKey') : t('providers.modal.showKey')"
                @click="toggleKeyVisibility"
              >
                <svg v-if="keyVisible" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
                <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>
              </button>
              <button
                type="button"
                class="btn btn-icon"
                :title="t('providers.modal.copyKey')"
                :aria-label="t('providers.modal.copyKey')"
                :disabled="!form.upstream_key"
                @click="copyUpstreamKey"
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
              </button>
            </div>
          </div>
          <div v-if="loadingKey" class="text-muted" style="font-size: 12px; margin-bottom: 6px;">{{ t('providers.modal.loadingKey') }}</div>
          <input
            v-model="form.upstream_key"
            :type="keyVisible ? 'text' : 'password'"
            class="input mono"
            :placeholder="modalMode === 'add' ? 'sk-...' : ''"
            :disabled="loadingKey"
            @input="onKeyInput"
          >
          <div class="field-help">{{ t('providers.modal.upstreamKeyHelp') }}</div>
        </div>
        <div v-if="modalMode === 'add'" class="field">
          <div class="row-between" style="margin-bottom: 0;">
            <label class="field-label">{{ t('providers.modal.customProvider') }}</label>
            <label class="toggle">
              <input v-model="form.is_custom" type="checkbox">
              <span class="toggle-slider"></span>
            </label>
          </div>
        </div>

        <div v-if="modalMode === 'edit'" class="field">
          <div class="row-between" style="margin-bottom: 8px;">
            <label class="field-label">{{ t('providers.modal.availableModels') }}</label>
            <div class="row" style="gap: 6px;">
              <button
                class="btn btn-secondary"
                style="padding: 4px 10px; font-size: 12px;"
                :disabled="manualAddVisible"
                @click="openManualAdd"
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" style="width:14px;height:14px;"><path d="M12 5v14M5 12h14"/></svg>
                {{ t('providers.modal.manualAdd') }}
              </button>
              <button
                class="btn btn-secondary"
                style="padding: 4px 10px; font-size: 12px;"
                :disabled="fetchingModels"
                @click="fetchModels"
              >
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;"><path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/></svg>
                {{ fetchingModels ? t('providers.modal.fetching') : t('providers.modal.fetchModels') }}
              </button>
              <button
                class="btn btn-danger-text"
                style="padding: 4px 10px; font-size: 12px;"
                :disabled="mutationBusy || models.length === 0"
                @click="clearAllModels"
              >
                {{ t('providers.clearModels') }}
              </button>
            </div>
          </div>

          <!-- Inline manual-add input row -->
          <div v-if="manualAddVisible" class="row" style="gap: 6px; margin-bottom: 8px;">
            <input
              v-model="manualAddName"
              class="input mono"
              style="flex: 1; font-size: 12.5px;"
              :placeholder="t('providers.modal.manualAddPlaceholder')"
              @keydown.enter="confirmManualAdd"
            >
            <button
              type="button"
              class="btn btn-primary"
              style="padding: 5px 14px; font-size: 12.5px;"
              :disabled="!manualAddName.trim()"
              @click="confirmManualAdd"
            >{{ t('providers.modal.manualAddConfirm') }}</button>
            <button
              type="button"
              class="btn btn-icon"
              :title="t('common.cancel')"
              :aria-label="t('common.cancel')"
              @click="closeManualAdd"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>
            </button>
          </div>

          <div class="model-list">
            <div v-if="fetchingModels" class="model-empty">{{ t('providers.modal.loading') }}</div>
            <div v-else-if="!models.length" class="model-empty">{{ t('providers.modal.empty') }}</div>
            <table v-else class="model-table">
              <thead>
                <tr>
                  <th>{{ t('providers.modal.model') }}</th>
                  <th class="right">{{ t('providers.modal.context') }}</th>
                  <th class="right">{{ t('providers.modal.latency') }}</th>
                  <th class="right">{{ t('providers.modal.enabled') }}</th>
                  <th></th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="m in models" :key="m.id">
                  <td>
                    <div v-if="editingModelId === m.id" class="row" style="gap: 4px;">
                      <input
                        v-model="editingModelName"
                        ref="renameInputRef"
                        class="input mono"
                        style="font-size: 12.5px; padding: 4px 8px; min-width: 140px; max-width: 220px;"
                        @keydown.enter="saveEditModel(m.name)"
                        @keydown.escape="cancelEditModel"
                      >
                      <button
                        type="button"
                        class="btn btn-icon"
                        :title="t('common.save')"
                        :aria-label="t('common.save')"
                        @click="saveEditModel(m.name)"
                      >
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
                      </button>
                      <button
                        type="button"
                        class="btn btn-icon"
                        :title="t('common.cancel')"
                        :aria-label="t('common.cancel')"
                        @click="cancelEditModel"
                      >
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>
                      </button>
                    </div>
                    <template v-else>
                      <div class="model-name">{{ m.name }}</div>
                      <div class="model-owner">{{ currentProviderName }}</div>
                    </template>
                  </td>
                  <td class="num">{{ formatContext(m.context_window) }}</td>
                  <td class="num">
                    <span v-if="m.latency_ms">{{ m.latency_ms }} ms</span>
                    <span v-else class="text-muted">—</span>
                  </td>
                  <td class="right">
                    <label class="toggle">
                      <input type="checkbox" :checked="m.active" @change="toggleModelActive(m)">
                      <span class="toggle-slider blue"></span>
                    </label>
                  </td>
                  <td class="right">
                    <div class="row" style="gap: 4px; justify-content: flex-end;">
                      <button class="btn btn-icon" :disabled="testingModelIds.has(m.id)" :title="t('providers.modal.testLatency')" :aria-label="t('providers.modal.testLatency')" @click="testModelLatency(m)">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;"><path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M2 12h4M18 12h4M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"/></svg>
                      </button>
                      <button class="btn btn-icon" :title="t('testModel.title')" :aria-label="t('testModel.title')" @click="openChatTest(m)">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>
                      </button>
                    </div>
                  </td>
                  <td class="right">
                    <DropdownMenu :menu-id="`model-${m.id}`" :min-width="140">
                      <template #trigger="{ toggle, open }">
                        <button
                          class="btn btn-icon"
                          :title="t('providers.more')"
                          :aria-expanded="open"
                          aria-haspopup="menu"
                          :aria-label="t('providers.moreActions', { name: m.name })"
                          @click="toggle"
                        >
                          <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>
                        </button>
                      </template>
                      <template #menu="{ close }">
                        <button class="dropdown-item" role="menuitem" @click="startEditModel(m); close()">{{ t('providers.modal.editModel') }}</button>
                        <button class="dropdown-item danger" role="menuitem" @click="deleteModelConfirm(m); close()">{{ t('providers.modal.deleteModel') }}</button>
                      </template>
                    </DropdownMenu>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
          <button class="btn btn-secondary" @click="closeModal">{{ t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="saving" @click="saveProvider">{{ saving ? t('common.processing') : (modalMode === 'add' ? t('providers.modal.createAndContinue') : t('common.save')) }}</button>
        </div>
      </div>
    </div>
  </Teleport>

  <!-- Selection modal: overlaying the provider modal, lists models fetched from upstream -->
  <Teleport to="body">
    <div v-if="selectionModalOpen" class="modal-overlay" @click.self="closeSelectionModal">
      <div class="modal-card wide modal-card-scroll">
        <div class="modal-title">{{ t('providers.modal.selectModels') }}</div>
        <div class="row-between" style="margin-bottom: 8px;">
          <span class="text-muted" style="font-size: 12px;">
            {{ selectedModelNames.size }} / {{ addableFetchedModels.length }}
          </span>
          <button
            type="button"
            class="btn btn-ghost"
            style="font-size: 12px; padding: 4px 10px;"
            @click="toggleAllSelection"
          >{{ allSelected ? t('providers.modal.deselectAll') : t('providers.modal.selectAll') }}</button>
        </div>
        <div class="model-list" style="max-height: 360px;">
          <div v-if="!fetchedModels.length" class="model-empty">{{ t('providers.modal.noModelsFetched') }}</div>
          <div v-else-if="!addableFetchedModels.length" class="model-empty">{{ t('providers.modal.allModelsAlreadyAdded') }}</div>
          <table v-else class="model-table">
            <tbody>
              <tr v-for="m in addableFetchedModels" :key="m.name">
                <td style="width: 36px;">
                  <label class="row" style="gap: 0; cursor: pointer;">
                    <input
                      type="checkbox"
                      :checked="selectedModelNames.has(m.name)"
                      :aria-label="m.name"
                      @change="toggleModelSelection(m.name)"
                    >
                  </label>
                </td>
                <td>
                  <div class="model-name">{{ m.name }}</div>
                  <div v-if="m.owned_by" class="model-owner">{{ m.owned_by }}</div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 16px;">
          <button class="btn btn-secondary" @click="closeSelectionModal">{{ t('common.cancel') }}</button>
          <button
            class="btn btn-primary"
            :disabled="!selectedModelNames.size || mutationBusy"
            @click="confirmAddSelected"
          >{{ t('providers.modal.confirmAdd', { count: selectedModelNames.size }) }}</button>
        </div>
      </div>
    </div>
  </Teleport>

  <TestModelChatModal
    :open="chatTestModal.open"
    :provider-id="chatTestModal.providerId"
    :provider-name="chatTestModal.providerName"
    :model-name="chatTestModal.modelName"
    @close="chatTestModal.open = false"
  />
</template>

<style scoped>
.btn-danger-text {
  background: transparent;
  color: var(--negative);
  border: 1px solid transparent;
}
.btn-danger-text:hover:not(:disabled) {
  background: rgba(217, 48, 37, 0.06);
  border-color: var(--negative);
}
.btn-danger-text:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* Disabled provider card — fade the whole card but keep text legible enough
   for users to still scan, identify, and re-enable the upstream. */
.provider-card.provider-disabled {
  opacity: 0.55;
}
.provider-card.provider-disabled:hover {
  opacity: 0.7;
}

/* Compact toggle used inside provider cards. Smaller than the default
   36x22 so it fits naturally next to the header identity block. */
.toggle.toggle-sm {
  width: 32px;
  height: 18px;
}
.toggle.toggle-sm .toggle-slider {
  border-radius: 9px;
}
.toggle.toggle-sm .toggle-slider::before {
  width: 14px;
  height: 14px;
  top: 2px;
  left: 2px;
}
.toggle.toggle-sm input:checked + .toggle-slider::before {
  transform: translateX(14px);
}
</style>