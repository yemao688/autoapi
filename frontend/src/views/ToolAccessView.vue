<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/bridge'
import { useConfirm } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import ToolPresetModal from '@/components/ToolPresetModal.vue'
import OmoSlimModal from '@/components/OmoSlimModal.vue'
import OpencodeWorkbenchModal from '@/components/OpencodeWorkbenchModal.vue'
import type { model, service, toolconfig } from '../../wailsjs/go/models'

type ToolName = 'opencode' | 'codex' | 'claude'

const { t } = useI18n()
const toast = useToast()
const confirm = useConfirm()
const tools: ToolName[] = ['opencode', 'codex', 'claude']
const statuses = ref<toolconfig.ToolStatus[]>([])
const presets = ref<Record<string, service.ToolProviderView[]>>({})
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
const editingEnabled = ref(false)
const omoSlimOpen = ref(false)
const opencodeWorkbenchOpen = ref(false)
const opencodeWorkbenchProviderID = ref('')

const exportOpen = ref(false)
const exportLoading = ref(false)
const exportSnippet = ref<toolconfig.Snippet | null>(null)

const backupsOpen = ref(false)
const backupsTool = ref<ToolName>('opencode')
const backupsResource = ref('')
const backups = ref<service.ToolBackupInfo[]>([])
const backupsLoading = ref(false)
const backupsGeneration = ref(0)

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

function extraPathLabel(key: string) {
  if (key === 'auth_json') return t('toolAccess.status.authPath')
  if (key === 'omo_slim_config') return t('toolAccess.status.omoSlimPath')
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
    const results = await Promise.all(tools.map(async (tool) => [tool, await api.listToolProviders(tool)] as const))
    presets.value = Object.fromEntries(results)
  } catch (e: any) {
    loadError.value = e?.message || String(e)
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

const supportingLoading = ref(false)

async function loadSupportingData() {
  supportingLoading.value = true
  try {
    const [keys, rules] = await Promise.all([api.apiKeys(), api.modelRules()])
    apiKeys.value = keys || []
    modelRules.value = rules || []
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    supportingLoading.value = false
  }
}

async function openPreset(tool: ToolName, view: service.ToolProviderView | null = null) {
  // Open the modal immediately — ListModelRules can take seconds, and
  // awaiting it before opening looked like a dead button (no feedback).
  modalGeneration.value++
  editingPreset.value = view?.Preset || null
  editingEnabled.value = view?.Enabled || false
  presetModalTool.value = tool
  presetModalOpen.value = true
  void loadSupportingData()
}

function closePresetModal() {
  modalGeneration.value++
  presetModalOpen.value = false
  editingPreset.value = null
  editingEnabled.value = false
}

function openOpencodeWorkbench(view: service.ToolProviderView | null = null) {
  opencodeWorkbenchProviderID.value = view?.Preset.ProviderID || ''
  opencodeWorkbenchOpen.value = true
}

function closeOpencodeWorkbench() {
  opencodeWorkbenchOpen.value = false
  opencodeWorkbenchProviderID.value = ''
}

async function enableProvider(view: service.ToolProviderView) {
  if (mutationBusy.value) return
  mutationBusy.value = true
  try {
    await api.enableToolPreset(view.Preset.ID)
    toast.push(t('toolAccess.toast.presetEnabled'), 'success')
    await refresh()
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    mutationBusy.value = false
  }
}

async function disableProvider(tool: ToolName, view: service.ToolProviderView) {
  if (mutationBusy.value) return
  const ok = await confirm.open({
    title: t('toolAccess.confirm.disableTitle'),
    message: t('toolAccess.confirm.disableMessage', { name: view.Preset.Name, tool: toolLabel(tool) }),
    confirmText: t('toolAccess.presets.disable'),
    danger: true,
  })
  if (!ok || mutationBusy.value) return
  mutationBusy.value = true
  try {
    await api.disableToolPreset(tool, view.Preset.ProviderID)
    toast.push(t('toolAccess.toast.presetDisabled'), 'success')
    await refresh()
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
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

async function openExport(view: service.ToolProviderView) {
  exportOpen.value = true
  exportLoading.value = true
  exportSnippet.value = null
  try {
    exportSnippet.value = await api.exportToolSnippet(view.Preset.ID)
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

async function openBackups(tool: ToolName, resource = '') {
  const generation = ++backupsGeneration.value
  backupsTool.value = tool
  backupsResource.value = resource
  backupsOpen.value = true
  backupsLoading.value = true
  backups.value = []
  try {
    const rows = await api.listToolBackups(tool)
    if (generation !== backupsGeneration.value) return
    backups.value = resource ? rows.filter((backup) => backup.Resource === resource) : rows
  } catch (e: any) {
    if (generation !== backupsGeneration.value) return
    toast.push(e?.message || String(e), 'error')
  } finally {
    if (generation === backupsGeneration.value) backupsLoading.value = false
  }
}

function formatModTime(value: any) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? String(value || '') : date.toLocaleString()
}

async function restoreBackup(backup: service.ToolBackupInfo) {
  const tool = backupsTool.value
  const resource = backupsResource.value
  const ok = await confirm.open({
    title: t('toolAccess.confirm.restoreTitle'),
    message: t('toolAccess.confirm.restoreMessage', { path: backup.Path }),
    confirmText: t('toolAccess.backups.restore'),
    danger: true,
  })
  if (!ok) return
  try {
    await api.restoreToolBackup(tool, backup.Resource, backup.Path)
    toast.push(t('toolAccess.toast.backupRestored'), 'success')
    await openBackups(tool, resource)
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
      <button class="btn btn-secondary" :disabled="refreshing || mutationBusy" @click="refresh">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-2.64-6.36L21 8"/><path d="M21 3v5h-5"/></svg>
        {{ refreshing ? t('toolAccess.refreshing') : t('toolAccess.refresh') }}
      </button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner stack-loose">
      <div v-if="loading" class="text-muted tool-page-state">{{ t('toolAccess.loading') }}</div>
      <div v-else-if="loadError" class="tool-page-error" role="alert">{{ t('toolAccess.loadFailed', { error: loadError }) }} <button class="btn btn-secondary" :disabled="mutationBusy" @click="refresh">{{ t('toolAccess.retry') }}</button></div>
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
          </div>

          <div class="tool-path-block">
            <div class="tool-path-label">{{ t('toolAccess.status.configPath') }}</div>
            <div class="text-mono tool-path" :title="pathText(statusFor(card.tool)?.ConfigPath || '')">{{ pathText(statusFor(card.tool)?.ConfigPath || '') }}</div>
            <template v-for="(path, key) in (statusFor(card.tool)?.ExtraPaths || {})" :key="key">
              <div v-if="path && !(card.tool === 'opencode' && key === 'omo_slim_config')" class="tool-extra-path">
                <span>{{ extraPathLabel(key) }}</span><span class="text-mono" :title="path">{{ path }}</span>
              </div>
            </template>
          </div>

          <div v-if="card.tool === 'opencode'" class="row-between tool-live-line">
            <span class="text-muted">{{ t('toolAccess.status.currentLive') }}</span>
            <span class="text-mono" :class="{ 'text-muted': !opencodeLive?.Model }">{{ opencodeLive?.Model || t('toolAccess.status.modelUnset') }}</span>
          </div>

          <div class="h-divider tool-divider"></div>
          <div class="row-between tool-section-heading">
            <div><div class="section-title" style="font-size: 15px;">{{ t('toolAccess.presets.title') }}</div><div class="section-sub">{{ t('toolAccess.presets.count', { count: presetsFor(card.tool).length }) }}</div></div>
            <div class="row tool-heading-actions"><button v-if="card.tool === 'opencode'" class="btn btn-primary" :disabled="mutationBusy" style="padding: 5px 9px; font-size: 11.5px;" @click="openOpencodeWorkbench()"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M4 5h16v14H4z"/><path d="M8 9h8M8 13h5"/></svg>{{ t('toolAccess.opencode.editConfig') }}</button><button v-else class="btn btn-secondary" :disabled="mutationBusy" style="padding: 5px 9px; font-size: 11.5px;" @click="openPreset(card.tool)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>{{ t('toolAccess.presets.new') }}</button></div>
          </div>

          <div v-if="!presetsFor(card.tool).length" class="tool-empty">{{ t('toolAccess.presets.empty') }}</div>
          <div v-else class="tool-preset-list">
            <div v-for="view in presetsFor(card.tool)" :key="view.InDB ? view.Preset.ID : 'file-' + view.Preset.ProviderID" class="tool-preset-row" :class="{ active: view.Enabled }">
              <div class="tool-preset-main">
                <div class="row" style="gap: 6px; flex-wrap: wrap;"><strong>{{ view.Preset.Name }}</strong><span v-if="view.Enabled" class="badge success">{{ t('toolAccess.presets.enabled') }}</span><span v-else class="badge">{{ t('toolAccess.presets.disabled') }}</span><span class="badge" :class="view.Preset.Kind === 'autoapi' ? 'info' : ''">{{ view.Preset.Kind === 'autoapi' ? t('toolAccess.presets.autoapi') : t('toolAccess.presets.direct') }}</span></div>
                <div class="tool-preset-meta"><template v-if="card.tool === 'opencode' && view.Preset.Kind === 'direct' && view.Preset.Vendor"><span>{{ t('toolAccess.vendors.' + view.Preset.Vendor) }}</span><span>·</span></template><span>{{ view.Preset.Kind === 'autoapi' ? t('toolAccess.presets.relay') : view.Preset.BaseURL }}</span><span>·</span><span>{{ t('toolAccess.presets.models', { count: view.Preset.Models?.length || 0 }) }}</span><span v-if="view.Preset.APIKeyEnc" class="key-hint">· {{ t('toolAccess.presets.storedKey') }}</span></div>
              </div>
              <div v-if="card.tool !== 'opencode'" class="row tool-preset-actions">
                <button v-if="view.Enabled" class="btn btn-secondary" :disabled="mutationBusy" style="padding: 4px 9px; font-size: 11px;" @click="disableProvider(card.tool, view)">{{ t('toolAccess.presets.disable') }}</button>
                <button v-else class="btn btn-primary" style="padding: 4px 9px; font-size: 11px;" :disabled="mutationBusy || !statusFor(card.tool)?.Installed" :title="!statusFor(card.tool)?.Installed ? t('toolAccess.presets.installHint') : ''" @click="enableProvider(view)">{{ t('toolAccess.presets.enable') }}</button>
                <button class="btn btn-icon" :disabled="mutationBusy" :title="t('common.edit')" :aria-label="t('common.edit')" @click="openPreset(card.tool, view)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4z"/></svg></button>
                <button v-if="view.InDB" class="btn btn-icon" :disabled="mutationBusy" :title="t('toolAccess.presets.export')" :aria-label="t('toolAccess.presets.export')" @click="openExport(view)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v12M7 8l5-5 5 5M5 21h14"/></svg></button>
                <button v-if="!view.Enabled" class="btn btn-icon danger-icon" :disabled="mutationBusy" :title="t('common.delete')" :aria-label="t('common.delete')" @click="deletePreset(view.Preset)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/></svg></button>
              </div>
            </div>
          </div>

          <div v-if="card.tool === 'opencode'" class="omo-slim-card-block">
            <div class="row-between omo-slim-card-heading">
              <div>
                <div class="omo-slim-card-label">OMO Slim</div>
                <div v-if="opencodeLive?.OmoSlimConfigured" class="omo-slim-card-summary">{{ t('toolAccess.omoSlim.activeSummary', { preset: opencodeLive.OmoSlimActivePreset || t('toolAccess.status.unconfigured'), agents: opencodeLive.OmoSlimAgentCount, disabled: opencodeLive.OmoSlimDisabledCount }) }}</div>
                <div v-else class="omo-slim-card-summary muted">{{ t('toolAccess.omoSlim.notConfigured') }}</div>
              </div>
              <div class="row omo-slim-card-actions"><button v-if="opencodeLive?.OmoSlimConfigured" class="btn btn-secondary" :disabled="mutationBusy" style="padding: 5px 10px; font-size: 11.5px;" @click="omoSlimOpen = true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4z"/></svg>{{ t('toolAccess.omoSlim.edit') }}</button><button class="btn btn-ghost" :disabled="mutationBusy" style="padding: 5px 8px; font-size: 11.5px;" @click="openBackups('opencode', 'opencode-omo')"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18M5 6v14h14V6M8 6V3h8v3"/></svg>{{ t('toolAccess.omoSlim.backups') }}</button></div>
            </div>
            <div class="text-mono omo-slim-card-path" :title="statusFor('opencode')?.ExtraPaths?.omo_slim_config || ''">{{ pathText(statusFor('opencode')?.ExtraPaths?.omo_slim_config || '') }}</div>
          </div>

          <div class="row tool-card-actions">
            <button class="btn btn-ghost" :disabled="mutationBusy" @click="openBackups(card.tool)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18M5 6v14h14V6M8 6V3h8v3M9 10v6M12 10v6M15 10v6"/></svg>{{ t('toolAccess.presets.backups') }}</button>
          </div>
        </article>
      </section>
    </div>
  </div>

  <ToolPresetModal :open="presetModalOpen" :tool="presetModalTool" :preset="editingPreset" :enabled="editingEnabled" :api-keys="apiKeys" :model-rules="modelRules" :supporting-loading="supportingLoading" @close="closePresetModal" @saved="closePresetModal(); refresh()" />
  <OpencodeWorkbenchModal :open="opencodeWorkbenchOpen" :initial-provider-i-d="opencodeWorkbenchProviderID" @close="closeOpencodeWorkbench" @changed="refresh" />
  <OmoSlimModal :open="omoSlimOpen" @close="omoSlimOpen = false" @applied="refresh" />

  <Teleport to="body">
    <div v-if="exportOpen" class="modal-overlay" @click.self="exportOpen = false">
      <div class="modal-card wide modal-card-scroll"><div class="row-between"><div class="modal-title">{{ t('toolAccess.export.title') }}</div><button class="btn btn-icon" @click="exportOpen = false"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div><div v-if="exportLoading" class="tool-page-state">{{ t('toolAccess.export.loading') }}</div><template v-else-if="exportSnippet"><div class="export-meta"><div><span>{{ t('toolAccess.export.target') }}</span><strong class="text-mono">{{ exportSnippet.TargetPath }}</strong></div><div><span>{{ t('toolAccess.export.format') }}</span><strong>{{ exportSnippet.Format }}</strong></div><div v-if="exportSnippet.Notes"><span>{{ t('toolAccess.export.notes') }}</span><strong>{{ exportSnippet.Notes }}</strong></div></div><div class="row-between export-code-heading"><span class="field-label">{{ t('toolAccess.export.content') }}</span><button class="btn btn-secondary" style="padding: 5px 10px; font-size: 12px;" @click="copySnippet"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>{{ t('common.copy') }}</button></div><pre class="export-code">{{ visibleSnippet }}</pre></template></div>
    </div>

    <div v-if="backupsOpen" class="modal-overlay" @click.self="backupsOpen = false">
      <div class="modal-card wide modal-card-scroll"><div class="row-between"><div><div class="modal-title">{{ t('toolAccess.backups.title', { tool: toolLabel(backupsTool) }) }}</div><div class="section-sub">{{ t('toolAccess.backups.subtitle') }}</div></div><button class="btn btn-icon" @click="backupsOpen = false"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div><div v-if="backupsLoading" class="tool-page-state">{{ t('toolAccess.backups.loading') }}</div><div v-else-if="!backups.length" class="tool-page-state">{{ t('toolAccess.backups.empty') }}</div><div v-else class="tbl-wrap"><table class="tbl backups-table"><thead><tr><th>{{ t('toolAccess.backups.resource') }}</th><th>{{ t('toolAccess.backups.path') }}</th><th>{{ t('toolAccess.backups.modified') }}</th><th></th></tr></thead><tbody><tr v-for="backup in backups" :key="backup.Path"><td>{{ backup.Resource }}</td><td class="text-mono backup-path">{{ backup.Path }}</td><td>{{ formatModTime(backup.ModTime) }}</td><td class="right"><button class="btn btn-secondary" style="padding: 4px 9px; font-size: 11.5px;" @click="restoreBackup(backup)">{{ t('toolAccess.backups.restore') }}</button></td></tr></tbody></table></div></div>
    </div>
  </Teleport>
</template>

<style scoped>
.tool-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; align-items: start; }
.tool-card { padding: 16px; min-width: 0; }
.tool-card-opencode { grid-column: 1 / -1; }
.tool-card-heading { align-items: flex-start; margin-bottom: 14px; }
.tool-icon { color: white; background: var(--graphite); text-transform: uppercase; }
.tool-icon.opencode { background: var(--accent); }
.tool-icon.codex { background: #3c6e71; }
.tool-icon.claude { background: #9a5b3d; }
.tool-card-title { font-family: var(--font-display); font-size: 16px; font-weight: 600; }
.tool-status-line { gap: 9px; margin-top: 4px; flex-wrap: wrap; }
.status-chip { gap: 5px; color: var(--muted); font-size: 11px; white-space: nowrap; }
.tool-path-block { padding: 10px; border-radius: var(--radius-sm); background: color-mix(in srgb, var(--bg) 82%, transparent); margin-bottom: 12px; }
.tool-path-label { color: var(--muted); font-size: 10px; font-weight: 600; text-transform: uppercase; letter-spacing: .05em; margin-bottom: 4px; }
.tool-path, .tool-extra-path span:last-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11px; }
.tool-extra-path { display: flex; gap: 8px; margin-top: 5px; color: var(--muted); font-size: 10.5px; min-width: 0; }
.tool-extra-path span:last-child { flex: 1; min-width: 0; }
.tool-live-line { min-height: 24px; font-size: 12px; }
.tool-divider { margin: 12px 0; }
.tool-section-heading { align-items: flex-start; margin-bottom: 9px; }
.tool-heading-actions { gap: 5px; flex-wrap: wrap; justify-content: flex-end; }
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
.omo-slim-card-block { margin-top: 12px; padding: 11px 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: color-mix(in srgb, var(--accent-soft) 40%, var(--surface)); }
.omo-slim-card-heading { align-items: center; gap: 12px; }
.omo-slim-card-actions { flex: 0 0 auto; gap: 4px; }
.omo-slim-card-path { margin-top: 9px; overflow: hidden; color: var(--muted); font-size: 10.5px; text-overflow: ellipsis; white-space: nowrap; }
.omo-slim-card-label { font-family: var(--font-display); font-size: 12px; font-weight: 600; letter-spacing: .04em; }
.omo-slim-card-summary { margin-top: 3px; color: var(--muted); font-size: 11px; }
.omo-slim-card-summary.muted { color: var(--muted); }
.tool-page-error { display: flex; align-items: center; justify-content: center; gap: 10px; padding: 40px 0; color: var(--negative); font-size: 13px; }
.export-meta { display: grid; gap: 8px; margin: 16px 0; padding: 10px; border-radius: var(--radius-sm); background: color-mix(in srgb, var(--bg) 82%, transparent); font-size: 12px; }
.export-meta > div { display: flex; gap: 10px; }
.export-meta span { color: var(--muted); min-width: 52px; }
.export-meta strong { font-weight: 500; overflow-wrap: anywhere; }
.export-code-heading { margin: 0 0 6px; }
.export-code { max-height: 400px; overflow: auto; padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: #1d1d1f; color: #f5f5f7; font: 11.5px/1.55 var(--font-mono); white-space: pre-wrap; overflow-wrap: anywhere; }
.backups-table { min-width: 640px; }
.backup-path { max-width: 300px; overflow-wrap: anywhere; font-size: 11px; }
@media (max-width: 700px) { .tool-grid { grid-template-columns: 1fr; } .tool-card-opencode { grid-column: 1 / -1; } .tool-page-error { flex-direction: column; } }
</style>
