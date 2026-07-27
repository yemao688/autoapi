<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/bridge'
import { useConfirm } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import ToolPresetModal from '@/components/ToolPresetModal.vue'
import OmoModal from '@/components/OmoModal.vue'
import type { model, service, toolconfig } from '../../wailsjs/go/models'

type ToolName = 'opencode' | 'codex' | 'claude'

const { t } = useI18n()
const toast = useToast()
const confirm = useConfirm()
const tools: ToolName[] = ['opencode', 'codex', 'claude']
const statuses = ref<toolconfig.ToolStatus[]>([])
const presets = ref<Record<string, toolconfig.Preset[]>>({})
const opencodeLive = ref<service.OpencodeLiveState | null>(null)
const apiKeys = ref<model.ApiKey[]>([])
const modelRules = ref<model.ModelRule[]>([])
const loading = ref(true)
const refreshing = ref(false)
const loadError = ref('')
const modalGeneration = ref(0)
const mutationBusy = ref(false)

const presetModalOpen = ref(false)
const presetModalTool = ref<ToolName>('opencode')
const editingPreset = ref<toolconfig.Preset | null>(null)
const omoOpen = ref(false)

const driftOpen = ref(false)
const driftTool = ref<ToolName>('opencode')
const driftStates = ref<service.DriftState[]>([])
const driftLoading = ref(false)
const applyDriftOpen = ref(false)
const applyDriftPreset = ref<toolconfig.Preset | null>(null)
const applyDriftStates = ref<service.DriftState[]>([])
const applyDriftLoading = ref(false)

const importOpen = ref(false)
const importTool = ref<ToolName>('opencode')
const importProviderID = ref('')
const importName = ref('')
const importing = ref(false)

const exportOpen = ref(false)
const exportLoading = ref(false)
const exportSnippet = ref<toolconfig.Snippet | null>(null)

const backupsOpen = ref(false)
const backupsTool = ref<ToolName>('opencode')
const backups = ref<service.ToolBackupInfo[]>([])
const backupsLoading = ref(false)

const toolCards = computed(() => tools.map((tool) => ({ tool, label: t(`toolAccess.tools.${tool}`) })))
const statusMap = computed(() => Object.fromEntries(statuses.value.map((status) => [status.Tool, status])))

function toolLabel(tool: string) {
  return t(`toolAccess.tools.${tool}`)
}

function statusFor(tool: ToolName) {
  return statusMap.value[tool] as toolconfig.ToolStatus | undefined
}

function presetsFor(tool: ToolName) {
  return presets.value[tool] || []
}

function activePreset(status: toolconfig.ToolStatus | undefined, tool: ToolName) {
  if (!status?.ActivePresetID) return null
  return presetsFor(tool).find((preset) => preset.ID === status.ActivePresetID) || null
}

function extraPathLabel(key: string) {
  if (key === 'auth_json') return t('toolAccess.status.authPath')
  if (key === 'omo_config') return t('toolAccess.status.omoPath')
  return key
}

function pathText(path: string) {
  return path || t('toolAccess.status.notFound')
}

async function refresh() {
  refreshing.value = true
  loadError.value = ''
  try {
    const [nextStatuses, nextLive] = await Promise.all([
      api.listToolStatuses(),
      api.getOpencodeLiveState(),
    ])
    statuses.value = nextStatuses
    opencodeLive.value = nextLive
    const results = await Promise.all(tools.map(async (tool) => [tool, await api.listToolPresets(tool)] as const))
    presets.value = Object.fromEntries(results)
  } catch (e: any) {
    loadError.value = e?.message || String(e)
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

async function loadSupportingData() {
  try {
    const [keys, rules] = await Promise.all([api.apiKeys(), api.modelRules()])
    apiKeys.value = keys
    modelRules.value = rules
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  }
}

async function openPreset(tool: ToolName, preset: toolconfig.Preset | null = null) {
  modalGeneration.value++
  const generation = modalGeneration.value
  editingPreset.value = preset
  presetModalTool.value = tool
  await loadSupportingData()
  if (generation !== modalGeneration.value) return
  presetModalOpen.value = true
}

function closePresetModal() {
  modalGeneration.value++
  presetModalOpen.value = false
  editingPreset.value = null
}

async function openDrift(tool: ToolName) {
  driftTool.value = tool
  driftOpen.value = true
  driftLoading.value = true
  driftStates.value = []
  try {
    driftStates.value = await api.checkToolDrift(tool)
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
    driftOpen.value = false
  } finally {
    driftLoading.value = false
  }
}

async function applyPreset(preset: toolconfig.Preset, allowDrift = false) {
  if (mutationBusy.value && !allowDrift) return
  mutationBusy.value = true
  try {
    const result = await api.applyToolPreset(preset.ID, allowDrift)
    toast.push(result.HotReload ? t('toolAccess.toast.hotReloaded') : t('toolAccess.toast.restartRequired'), 'success')
    await refresh()
    if (preset.Tool === 'opencode' && opencodeLive.value?.OmoConfigured) {
      omoOpen.value = true
    }
  } catch (e: any) {
    const message = e?.message || String(e)
    if (!allowDrift && message.includes('config file changed externally since last apply')) {
      applyDriftLoading.value = true
      try {
        const states = await api.checkToolDrift(preset.Tool)
        applyDriftPreset.value = preset
        applyDriftStates.value = states
        applyDriftOpen.value = true
      } catch (driftError: any) {
        toast.push(driftError?.message || String(driftError), 'error')
      } finally {
        applyDriftLoading.value = false
      }
    } else {
      toast.push(message, 'error')
    }
  } finally {
    mutationBusy.value = false
  }
}

async function deletePreset(preset: toolconfig.Preset) {
  const ok = await confirm.open({
    title: t('toolAccess.confirm.deleteTitle'),
    message: t('toolAccess.confirm.deleteMessage', { name: preset.Name }),
    confirmText: t('common.delete'),
    danger: true,
  })
  if (!ok || mutationBusy.value) return
  mutationBusy.value = true
  try {
    await api.deleteToolPreset(preset.ID)
    toast.push(t('toolAccess.toast.presetDeleted'), 'success')
    await refresh()
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    mutationBusy.value = false
  }
}

function openImport(tool: ToolName, providerID = '', name = '') {
  importTool.value = tool
  importProviderID.value = providerID
  importName.value = name
  importOpen.value = true
}

async function continueApplyAfterDrift() {
  const preset = applyDriftPreset.value
  applyDriftOpen.value = false
  if (preset) await applyPreset(preset, true)
}

function importDriftedConfig() {
  const preset = applyDriftPreset.value
  if (!preset) return
  const current = activePreset(statusFor(preset.Tool as ToolName), preset.Tool as ToolName)
  applyDriftOpen.value = false
  openImport(
    preset.Tool as ToolName,
    current?.ProviderID || preset.ProviderID || '',
    t('toolAccess.import.suggestedName', { tool: toolLabel(preset.Tool) })
  )
}

async function importPreset() {
  if (importing.value || !importProviderID.value.trim() || !importName.value.trim()) return
  importing.value = true
  try {
    await api.importToolPreset(importTool.value, importProviderID.value.trim(), importName.value.trim())
    importOpen.value = false
    toast.push(t('toolAccess.toast.presetImported'), 'success')
    await refresh()
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    importing.value = false
  }
}

async function openExport(preset: toolconfig.Preset) {
  exportOpen.value = true
  exportLoading.value = true
  exportSnippet.value = null
  try {
    exportSnippet.value = await api.exportToolSnippet(preset.ID)
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
    exportOpen.value = false
  } finally {
    exportLoading.value = false
  }
}

function safeSnippet(content: string) {
  return content
    .replace(/("(?:api[_-]?key|access[_-]?token|token|password|secret)"\s*:\s*")([^"\\]*(?:\\.[^"\\]*)*)(")/gi, '$1********$3')
    .replace(/((?:api[_-]?key|access[_-]?token|token|password|secret)\s*[=:]\s*["']?)([^\s"'&,}]+)/gi, '$1********')
}

const visibleSnippet = computed(() => safeSnippet(exportSnippet.value?.Content || ''))

async function copySnippet() {
  if (!visibleSnippet.value) return
  try {
    await navigator.clipboard.writeText(visibleSnippet.value)
    toast.push(t('toolAccess.toast.snippetCopied'), 'success')
  } catch (e: any) {
    toast.push(t('toast.copyFailed') + ': ' + (e?.message || String(e)), 'error')
  }
}

async function openBackups(tool: ToolName) {
  backupsTool.value = tool
  backupsOpen.value = true
  backupsLoading.value = true
  backups.value = []
  try {
    backups.value = await api.listToolBackups(tool)
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    backupsLoading.value = false
  }
}

function formatModTime(value: any) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? String(value || '') : date.toLocaleString()
}

async function restoreBackup(backup: service.ToolBackupInfo) {
  const ok = await confirm.open({
    title: t('toolAccess.confirm.restoreTitle'),
    message: t('toolAccess.confirm.restoreMessage', { path: backup.Path }),
    confirmText: t('toolAccess.backups.restore'),
    danger: true,
  })
  if (!ok) return
  try {
    await api.restoreToolBackup(backupsTool.value, backup.Resource, backup.Path)
    toast.push(t('toolAccess.toast.backupRestored'), 'success')
    await openBackups(backupsTool.value)
    await refresh()
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  }
}

onMounted(() => {
  void refresh()
})
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">{{ t('toolAccess.title') }}</h1>
      <span class="main-subtitle">{{ t('toolAccess.subtitle') }}</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary" :disabled="refreshing" @click="refresh">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-2.64-6.36L21 8"/><path d="M21 3v5h-5"/></svg>
        {{ refreshing ? t('toolAccess.refreshing') : t('toolAccess.refresh') }}
      </button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner stack-loose">
      <div v-if="loading" class="text-muted tool-page-state">{{ t('toolAccess.loading') }}</div>
      <div v-else-if="loadError" class="tool-page-error" role="alert">{{ t('toolAccess.loadFailed', { error: loadError }) }} <button class="btn btn-secondary" @click="refresh">{{ t('toolAccess.retry') }}</button></div>
      <section v-else class="tool-grid">
        <article v-for="card in toolCards" :key="card.tool" class="card card-hover tool-card" :class="{ 'tool-card-opencode': card.tool === 'opencode' }">
          <div class="row-between tool-card-heading">
            <div class="row" style="gap: 10px; min-width: 0;">
              <div class="list-icon tool-icon" :class="card.tool">{{ card.label.slice(0, 1) }}</div>
              <div style="min-width: 0;">
                <h2 class="tool-card-title">{{ card.label }}</h2>
                <div class="row tool-status-line">
                  <span class="row status-chip"><span class="dot" :class="statusFor(card.tool)?.Installed ? 'green' : 'gray'" />{{ statusFor(card.tool)?.Installed ? t('toolAccess.status.installed') : t('toolAccess.status.notInstalled') }}</span>
                  <span class="row status-chip"><span class="dot" :class="statusFor(card.tool)?.ConfigExists ? 'green' : 'gray'" />{{ statusFor(card.tool)?.ConfigExists ? t('toolAccess.status.configExists') : t('toolAccess.status.configMissing') }}</span>
                </div>
              </div>
            </div>
            <span v-if="statusFor(card.tool)?.Drifted" class="badge warn drift-button" role="button" tabindex="0" @click="openDrift(card.tool)" @keydown.enter="openDrift(card.tool)">{{ t('toolAccess.status.drifted') }}</span>
          </div>

          <div class="tool-path-block">
            <div class="tool-path-label">{{ t('toolAccess.status.configPath') }}</div>
            <div class="text-mono tool-path" :title="pathText(statusFor(card.tool)?.ConfigPath || '')">{{ pathText(statusFor(card.tool)?.ConfigPath || '') }}</div>
            <div v-for="(path, key) in (statusFor(card.tool)?.ExtraPaths || {})" v-show="path" :key="key" class="tool-extra-path">
              <span>{{ extraPathLabel(key) }}</span><span class="text-mono" :title="path">{{ path }}</span>
            </div>
          </div>

          <div class="row-between tool-active-line">
            <span class="text-muted">{{ t('toolAccess.status.activePreset') }}</span>
            <span v-if="activePreset(statusFor(card.tool), card.tool)" class="badge info">{{ activePreset(statusFor(card.tool), card.tool)?.Name }}</span>
            <span v-else class="text-muted">{{ t('toolAccess.status.unconfigured') }}</span>
          </div>
          <div v-if="card.tool === 'opencode'" class="row-between tool-live-line">
            <span class="text-muted">{{ t('toolAccess.status.currentLive') }}</span>
            <span class="text-mono" :class="{ 'text-muted': !opencodeLive?.Model }">{{ opencodeLive?.Model || t('toolAccess.status.modelUnset') }}</span>
          </div>

          <div class="h-divider tool-divider"></div>
          <div class="row-between tool-section-heading">
            <div><div class="section-title" style="font-size: 15px;">{{ t('toolAccess.presets.title') }}</div><div class="section-sub">{{ t('toolAccess.presets.count', { count: presetsFor(card.tool).length }) }}</div></div>
            <button class="btn btn-secondary" style="padding: 5px 9px; font-size: 11.5px;" @click="openPreset(card.tool)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>{{ t('toolAccess.presets.new') }}</button>
          </div>

          <div v-if="!presetsFor(card.tool).length" class="tool-empty">{{ t('toolAccess.presets.empty') }}</div>
          <div v-else class="tool-preset-list">
            <div v-for="preset in presetsFor(card.tool)" :key="preset.ID" class="tool-preset-row" :class="{ active: preset.ID === statusFor(card.tool)?.ActivePresetID }">
              <div class="tool-preset-main">
                <div class="row" style="gap: 6px; flex-wrap: wrap;"><strong>{{ preset.Name }}</strong><span v-if="preset.ID === statusFor(card.tool)?.ActivePresetID" class="badge success">{{ t('toolAccess.presets.active') }}</span><span class="badge" :class="preset.Kind === 'autoapi' ? 'info' : ''">{{ preset.Kind === 'autoapi' ? t('toolAccess.presets.autoapi') : t('toolAccess.presets.direct') }}</span></div>
                <div class="tool-preset-meta"><span>{{ preset.Kind === 'autoapi' ? t('toolAccess.presets.relay') : preset.BaseURL }}</span><span>·</span><span>{{ t('toolAccess.presets.models', { count: preset.Models?.length || 0 }) }}</span><span v-if="preset.APIKeyEnc" class="key-hint">· {{ t('toolAccess.presets.storedKey') }}</span></div>
              </div>
              <div class="row tool-preset-actions">
                <button class="btn btn-primary" :disabled="mutationBusy || !statusFor(card.tool)?.Installed" :title="!statusFor(card.tool)?.Installed ? t('toolAccess.presets.installHint') : ''" @click="applyPreset(preset)">{{ t('toolAccess.presets.apply') }}</button>
                <button class="btn btn-icon" :title="t('common.edit')" :aria-label="t('common.edit')" @click="openPreset(card.tool, preset)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4z"/></svg></button>
                <button class="btn btn-icon" :title="t('toolAccess.presets.export')" :aria-label="t('toolAccess.presets.export')" @click="openExport(preset)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v12M7 8l5-5 5 5M5 21h14"/></svg></button>
                <button class="btn btn-icon danger-icon" :title="t('common.delete')" :aria-label="t('common.delete')" @click="deletePreset(preset)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/></svg></button>
              </div>
            </div>
          </div>

          <div v-if="card.tool === 'opencode'" class="omo-card-block">
            <div class="row-between omo-card-heading">
              <div>
                <div class="omo-card-label">OMO</div>
                <div v-if="opencodeLive?.OmoConfigured" class="omo-card-summary">{{ t('toolAccess.omo.activeSummary', { preset: opencodeLive.OmoActivePreset || t('toolAccess.status.unconfigured'), agents: opencodeLive.OmoAgentCount, disabled: opencodeLive.OmoDisabledCount }) }}</div>
                <div v-else class="omo-card-summary muted">{{ t('toolAccess.omo.notConfigured') }}</div>
              </div>
              <button v-if="opencodeLive?.OmoConfigured" class="btn btn-secondary" style="padding: 5px 10px; font-size: 11.5px;" @click="omoOpen = true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4z"/></svg>{{ t('toolAccess.omo.edit') }}</button>
            </div>
          </div>

          <div class="row tool-card-actions">
            <button class="btn btn-ghost" style="padding-left: 0;" @click="openImport(card.tool)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v12M7 10l5 5 5-5M5 21h14"/></svg>{{ t('toolAccess.presets.import') }}</button>
            <button class="btn btn-ghost" @click="openBackups(card.tool)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18M5 6v14h14V6M8 6V3h8v3M9 10v6M12 10v6M15 10v6"/></svg>{{ t('toolAccess.presets.backups') }}</button>
            <button class="btn btn-ghost tool-drift-link" @click="openDrift(card.tool)">{{ t('toolAccess.status.checkDrift') }}</button>
          </div>
        </article>
      </section>
    </div>
  </div>

  <ToolPresetModal :open="presetModalOpen" :tool="presetModalTool" :preset="editingPreset" :api-keys="apiKeys" :model-rules="modelRules" @close="closePresetModal" @saved="closePresetModal(); refresh()" />
  <OmoModal :open="omoOpen" @close="omoOpen = false" @applied="refresh" />

  <Teleport to="body">
    <div v-if="applyDriftOpen" class="modal-overlay" @click.self="applyDriftOpen = false">
      <div class="modal-card wide">
        <div class="row-between"><div><div class="modal-title">{{ t('toolAccess.drift.confirmTitle') }}</div><div class="section-sub">{{ t('toolAccess.drift.confirmMessage') }}</div></div><button class="btn btn-icon" :title="t('common.close')" :aria-label="t('common.close')" @click="applyDriftOpen = false"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div>
        <div v-if="applyDriftLoading" class="tool-page-state">{{ t('toolAccess.drift.loading') }}</div>
        <div v-else class="drift-list"><div v-for="state in applyDriftStates" :key="state.Resource" class="drift-row"><div class="row"><span class="dot" :class="state.Missing ? 'red' : state.Drifted ? 'amber' : 'green'"/><strong>{{ state.Resource }}</strong><span class="badge" :class="state.Missing || state.Drifted ? 'warn' : 'success'">{{ state.Missing ? t('toolAccess.drift.missing') : state.Drifted ? t('toolAccess.drift.changed') : t('toolAccess.drift.unchanged') }}</span></div><div class="text-mono drift-path">{{ state.Path }}</div></div></div>
        <div class="row drift-choice-actions"><button class="btn btn-secondary" @click="applyDriftOpen = false">{{ t('common.cancel') }}</button><button class="btn btn-secondary" @click="importDriftedConfig">{{ t('toolAccess.drift.importBackfill') }}</button><button class="btn btn-danger" @click="continueApplyAfterDrift">{{ t('toolAccess.drift.applyAnyway') }}</button></div>
      </div>
    </div>

    <div v-if="driftOpen" class="modal-overlay" @click.self="driftOpen = false">
      <div class="modal-card wide">
        <div class="row-between"><div><div class="modal-title">{{ t('toolAccess.drift.title', { tool: toolLabel(driftTool) }) }}</div><div class="section-sub">{{ t('toolAccess.drift.subtitle') }}</div></div><button class="btn btn-icon" @click="driftOpen = false"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div>
        <div v-if="driftLoading" class="tool-page-state">{{ t('toolAccess.drift.loading') }}</div>
        <div v-else-if="!driftStates.length" class="tool-page-state">{{ t('toolAccess.drift.none') }}</div>
        <div v-else class="drift-list"><div v-for="state in driftStates" :key="state.Resource" class="drift-row"><div class="row"><span class="dot" :class="state.Missing ? 'red' : state.Drifted ? 'amber' : 'green'"/><strong>{{ state.Resource }}</strong><span class="badge" :class="state.Missing || state.Drifted ? 'warn' : 'success'">{{ state.Missing ? t('toolAccess.drift.missing') : state.Drifted ? t('toolAccess.drift.changed') : t('toolAccess.drift.unchanged') }}</span></div><div class="text-mono drift-path">{{ state.Path }}</div></div></div>
        <div class="row" style="justify-content: flex-end; margin-top: 16px;"><button class="btn btn-primary" @click="driftOpen = false">{{ t('common.close') }}</button></div>
      </div>
    </div>

    <div v-if="importOpen" class="modal-overlay" @click.self="importOpen = false">
      <div class="modal-card"><div class="modal-title">{{ t('toolAccess.import.title', { tool: toolLabel(importTool) }) }}</div><div class="section-sub" style="margin-bottom: 16px;">{{ t('toolAccess.import.subtitle') }}</div><div class="field"><label class="field-label">{{ t('toolAccess.import.providerID') }}</label><input v-model="importProviderID" class="input mono" :placeholder="t('toolAccess.import.providerPlaceholder')"></div><div class="field"><label class="field-label">{{ t('toolAccess.import.name') }}</label><input v-model="importName" class="input" :placeholder="t('toolAccess.import.namePlaceholder')"></div><div class="row" style="justify-content: flex-end; gap: 8px;"><button class="btn btn-secondary" @click="importOpen = false">{{ t('common.cancel') }}</button><button class="btn btn-primary" :disabled="importing || !importProviderID.trim() || !importName.trim()" @click="importPreset">{{ importing ? t('common.processing') : t('toolAccess.import.confirm') }}</button></div></div>
    </div>

    <div v-if="exportOpen" class="modal-overlay" @click.self="exportOpen = false">
      <div class="modal-card wide modal-card-scroll"><div class="row-between"><div class="modal-title">{{ t('toolAccess.export.title') }}</div><button class="btn btn-icon" @click="exportOpen = false"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div><div v-if="exportLoading" class="tool-page-state">{{ t('toolAccess.export.loading') }}</div><template v-else-if="exportSnippet"><div class="export-meta"><div><span>{{ t('toolAccess.export.target') }}</span><strong class="text-mono">{{ exportSnippet.TargetPath }}</strong></div><div><span>{{ t('toolAccess.export.format') }}</span><strong>{{ exportSnippet.Format }}</strong></div><div v-if="exportSnippet.Notes"><span>{{ t('toolAccess.export.notes') }}</span><strong>{{ exportSnippet.Notes }}</strong></div></div><div class="row-between export-code-heading"><span class="field-label">{{ t('toolAccess.export.content') }}</span><button class="btn btn-secondary" style="padding: 5px 10px; font-size: 12px;" @click="copySnippet"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>{{ t('common.copy') }}</button></div><pre class="export-code">{{ visibleSnippet }}</pre></template></div>
    </div>

    <div v-if="backupsOpen" class="modal-overlay" @click.self="backupsOpen = false">
      <div class="modal-card wide modal-card-scroll"><div class="row-between"><div><div class="modal-title">{{ t('toolAccess.backups.title', { tool: toolLabel(backupsTool) }) }}</div><div class="section-sub">{{ t('toolAccess.backups.subtitle') }}</div></div><button class="btn btn-icon" @click="backupsOpen = false"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div><div v-if="backupsLoading" class="tool-page-state">{{ t('toolAccess.backups.loading') }}</div><div v-else-if="!backups.length" class="tool-page-state">{{ t('toolAccess.backups.empty') }}</div><div v-else class="tbl-wrap"><table class="tbl backups-table"><thead><tr><th>{{ t('toolAccess.backups.resource') }}</th><th>{{ t('toolAccess.backups.path') }}</th><th>{{ t('toolAccess.backups.modified') }}</th><th></th></tr></thead><tbody><tr v-for="backup in backups" :key="backup.Path"><td>{{ backup.Resource }}</td><td class="text-mono backup-path">{{ backup.Path }}</td><td>{{ formatModTime(backup.ModTime) }}</td><td class="right"><button class="btn btn-secondary" style="padding: 4px 9px; font-size: 11.5px;" @click="restoreBackup(backup)">{{ t('toolAccess.backups.restore') }}</button></td></tr></tbody></table></div></div>
    </div>
  </Teleport>
</template>

<style scoped>
.tool-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 16px; align-items: start; }
.tool-card { padding: 16px; min-width: 0; }
.tool-card-opencode { grid-column: span 2; }
.tool-card-heading { align-items: flex-start; margin-bottom: 14px; }
.tool-icon { color: white; background: var(--graphite); text-transform: uppercase; }
.tool-icon.opencode { background: var(--accent); }
.tool-icon.codex { background: #3c6e71; }
.tool-icon.claude { background: #9a5b3d; }
.tool-card-title { font-family: var(--font-display); font-size: 16px; font-weight: 600; }
.tool-status-line { gap: 9px; margin-top: 4px; flex-wrap: wrap; }
.status-chip { gap: 5px; color: var(--muted); font-size: 11px; white-space: nowrap; }
.drift-button { cursor: pointer; }
.tool-path-block { padding: 10px; border-radius: var(--radius-sm); background: color-mix(in srgb, var(--bg) 82%, transparent); margin-bottom: 12px; }
.tool-path-label { color: var(--muted); font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: .05em; margin-bottom: 4px; }
.tool-path, .tool-extra-path span:last-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11px; }
.tool-extra-path { display: flex; gap: 8px; margin-top: 5px; color: var(--muted); font-size: 10.5px; min-width: 0; }
.tool-extra-path span:last-child { flex: 1; min-width: 0; }
.tool-active-line { font-size: 12px; min-height: 26px; }
.tool-live-line { min-height: 24px; font-size: 12px; }
.tool-divider { margin: 12px 0; }
.tool-section-heading { align-items: flex-start; margin-bottom: 9px; }
.tool-empty, .tool-page-state { padding: 18px 8px; text-align: center; color: var(--muted); font-size: 12px; }
.tool-preset-list { border: 1px solid var(--border); border-radius: var(--radius-sm); overflow: hidden; }
.tool-preset-row { display: flex; gap: 8px; align-items: center; padding: 9px 10px; border-bottom: 1px solid var(--border); min-width: 0; }
.tool-preset-row:last-child { border-bottom: none; }
.tool-preset-row.active { background: var(--accent-soft); }
.tool-preset-main { min-width: 0; flex: 1; }
.tool-preset-main strong { font-size: 12.5px; font-weight: 600; }
.tool-preset-meta { display: flex; gap: 5px; align-items: center; color: var(--muted); font-size: 10.5px; margin-top: 4px; min-width: 0; }
.tool-preset-meta span:first-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.key-hint { color: var(--positive); white-space: nowrap; }
.tool-preset-actions { gap: 2px; flex: 0 0 auto; }
.tool-preset-actions .btn-primary { padding: 4px 9px; font-size: 11px; }
.danger-icon:hover { color: var(--negative); }
.tool-card-actions { flex-wrap: wrap; gap: 2px 10px; margin-top: 10px; }
.tool-card-actions .btn-ghost { font-size: 11.5px; }
.tool-drift-link { margin-left: auto; }
.omo-card-block { margin-top: 12px; padding: 11px 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: color-mix(in srgb, var(--accent-soft) 40%, var(--surface)); }
.omo-card-heading { align-items: center; gap: 12px; }
.omo-card-label { font-family: var(--font-display); font-size: 12px; font-weight: 600; letter-spacing: .04em; }
.omo-card-summary { margin-top: 3px; color: var(--muted); font-size: 11px; }
.omo-card-summary.muted { color: var(--muted); }
.tool-page-error { display: flex; align-items: center; justify-content: center; gap: 10px; padding: 40px 0; color: var(--negative); font-size: 13px; }
.drift-list { margin-top: 18px; border: 1px solid var(--border); border-radius: var(--radius-sm); overflow: hidden; }
.drift-row { padding: 11px 12px; border-bottom: 1px solid var(--border); }
.drift-row:last-child { border-bottom: none; }
.drift-row .row { gap: 7px; font-size: 12px; }
.drift-path { color: var(--muted); font-size: 10.5px; margin: 5px 0 0 14px; overflow-wrap: anywhere; }
.export-meta { display: grid; gap: 8px; margin: 16px 0; padding: 10px; border-radius: var(--radius-sm); background: color-mix(in srgb, var(--bg) 82%, transparent); font-size: 12px; }
.export-meta > div { display: flex; gap: 10px; }
.export-meta span { color: var(--muted); min-width: 52px; }
.export-meta strong { font-weight: 500; overflow-wrap: anywhere; }
.export-code-heading { margin: 0 0 6px; }
.export-code { max-height: 400px; overflow: auto; padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: #1d1d1f; color: #f5f5f7; font: 11.5px/1.55 var(--font-mono); white-space: pre-wrap; overflow-wrap: anywhere; }
.backups-table { min-width: 640px; }
.backup-path { max-width: 300px; overflow-wrap: anywhere; font-size: 11px; }
 .drift-choice-actions { justify-content: flex-end; gap: 8px; margin-top: 16px; flex-wrap: wrap; }
@media (max-width: 1050px) { .tool-grid { grid-template-columns: 1fr 1fr; } .tool-card-opencode { grid-column: span 1; } }
@media (max-width: 700px) { .tool-grid { grid-template-columns: 1fr; } .tool-page-error { flex-direction: column; } }
</style>
