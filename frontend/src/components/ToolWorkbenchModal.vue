<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/bridge'
import { useConfirm } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import DiffPreview from '@/components/DiffPreview.vue'
import { service, toolconfig } from '../../wailsjs/go/models'
import type { model } from '../../wailsjs/go/models'

type Tool = 'codex' | 'claude'
type Section = 'provider' | 'common'
type DraftKey = string
type ModelRow = { name: string; isDefault: boolean }
type ProviderDraft = {
  key: DraftKey
  view: service.ToolProviderView | null
  isNew: boolean
  enabled: boolean
  kind: 'direct' | 'autoapi'
  name: string
  providerID: string
  baseURL: string
  apiKeyID: string
  plaintextKey: string
  keyTouched: boolean
  keyRevealed: boolean
  extra: Record<string, string>
  modelRows: ModelRow[]
}

const props = withDefaults(defineProps<{
  open: boolean
  tool: Tool
  initialProviderID?: string
}>(), { initialProviderID: '' })

const emit = defineEmits<{ close: []; changed: [] }>()
const { t } = useI18n()
const toast = useToast()
const confirm = useConfirm()

const loading = ref(false)
const loadingError = ref('')
const providers = ref<service.ToolProviderView[]>([])
const drafts = ref<Record<string, ProviderDraft>>({})
const providerBaseline = ref<Record<string, string>>({})
const apiKeys = ref<model.ApiKey[]>([])
const modelRules = ref<model.ModelRule[]>([])
const supportingLoading = ref(false)
const commonConfig = ref('')
const commonConfigBaseline = ref('')
const commonConfigLoading = ref(false)
const section = ref<Section>('provider')
const selectedKey = ref<DraftKey | null>(null)
const pendingDelete = ref<Set<DraftKey>>(new Set())
const nextNewID = ref(1)
const keyVisible = ref(false)
const revealLoading = ref(false)
const revealError = ref(false)
const revealGeneration = ref(0)
const previewLoading = ref(false)
const saving = ref(false)
const modalGeneration = ref(0)
const previewOpen = ref(false)
const previewData = ref<service.ToolFilePreview[]>([])
const selectedPreviewIndex = ref(0)
const pendingPlan = ref<service.ToolConfigPlan | null>(null)
const actionError = ref('')

const toolLabel = computed(() => t(`toolAccess.tools.${props.tool}`))
const providerRows = computed(() => Object.values(drafts.value))
const selectedDraft = computed(() => selectedKey.value ? drafts.value[selectedKey.value] || null : null)
const selectedView = computed(() => selectedDraft.value?.view || null)
const selectedIsPendingDelete = computed(() => !!selectedKey.value && pendingDelete.value.has(selectedKey.value))
const commonConfigChanged = computed(() => commonConfig.value !== commonConfigBaseline.value)
const commonConfigValidation = computed(() => {
  if (!commonConfigChanged.value) return ''
  if (props.tool === 'codex') return commonConfig.value.trim() ? '' : t('toolAccess.workbench.commonConfigRequiredCodex')
  if (!commonConfig.value.trim()) return t('toolAccess.workbench.commonConfigRequiredClaude')
  try {
    JSON.parse(commonConfig.value)
    return ''
  } catch {
    return t('toolAccess.workbench.commonConfigInvalidJSON')
  }
})
const primaryModelName = computed(() => {
  const rows = selectedDraft.value?.modelRows || []
  return rows.find((row) => row.isDefault && row.name.trim())?.name.trim() || rows.find((row) => row.name.trim())?.name.trim() || ''
})
const selectedPreview = computed(() => previewData.value[selectedPreviewIndex.value] || null)
const previewFileLabel = computed(() => selectedPreview.value ? basename(selectedPreview.value.Path) : '')
const selectedTitle = computed(() => {
  if (!selectedDraft.value) return t('toolAccess.workbench.provider')
  if (selectedDraft.value.isNew) return props.tool === 'claude' ? t('toolAccess.workbench.claude.provider') : t('toolAccess.workbench.codex.newProvider')
  return selectedDraft.value.name || selectedDraft.value.providerID
})

function tierPlaceholder() {
  return primaryModelName.value || t('toolAccess.workbench.tierFallback')
}

function basename(path: string) {
  const parts = path.split(/[\\/]/)
  return parts[parts.length - 1] || path
}

function viewKey(view: service.ToolProviderView): DraftKey {
  return view.InDB ? `db:${view.Preset.ID}` : `file:${view.Preset.ProviderID}`
}

function emptyRow(name = ''): ModelRow {
  return { name, isDefault: false }
}

function rowsFromPreset(preset: toolconfig.Preset | null, kind: 'direct' | 'autoapi'): ModelRow[] {
  if (preset?.Models?.length) {
    return preset.Models.map((item) => ({ name: item.name || '', isDefault: !!item.default }))
  }
  if (!preset && kind === 'autoapi') {
    return modelRules.value.filter((rule) => rule.enabled).map((rule, index) => ({ name: rule.name, isDefault: index === 0 }))
  }
  return [emptyRow()]
}

function createDraft(view: service.ToolProviderView | null, key: DraftKey, isNew = false): ProviderDraft {
  const preset = view?.Preset || null
  const kind = preset?.Kind === 'autoapi' ? 'autoapi' : 'direct'
  const extra = { ...(preset?.Extra || {}) }
  if (props.tool === 'codex' && !extra.wire_api) extra.wire_api = 'responses'
  return {
    key,
    view,
    isNew,
    enabled: isNew ? false : !!view?.Enabled,
    kind,
    name: preset?.Name || (props.tool === 'claude' ? 'Anthropic' : ''),
    providerID: preset?.ProviderID || (props.tool === 'claude' ? 'anthropic' : ''),
    baseURL: preset?.BaseURL || '',
    apiKeyID: preset?.APIKeyID || '',
    plaintextKey: '',
    keyTouched: false,
    keyRevealed: false,
    extra,
    modelRows: rowsFromPreset(preset, kind),
  }
}

function draftSnapshot(draft: ProviderDraft) {
  return JSON.stringify({
    kind: draft.kind,
    name: draft.name,
    providerID: draft.providerID,
    baseURL: draft.baseURL,
    apiKeyID: draft.apiKeyID,
    extra: draft.extra,
    modelRows: draft.modelRows,
  })
}

function draftChanged(key: DraftKey, draft: ProviderDraft) {
  return draft.keyTouched || draftSnapshot(draft) !== providerBaseline.value[key]
}

function draftValidation(draft: ProviderDraft) {
  if (!draft.name.trim()) return t('toolAccess.workbench.validationName')
  if (props.tool === 'codex') {
    if (!draft.providerID.trim()) return t('toolAccess.workbench.validationProviderID')
    if (!/^[A-Za-z0-9_-]+$/.test(draft.providerID.trim())) return t('toolAccess.workbench.validationProviderIDFormat')
  }
  if (!draft.modelRows.some((row) => row.name.trim())) return t('toolAccess.workbench.validationModels')
  if (draft.kind === 'direct' && !draft.baseURL.trim()) return t('toolAccess.workbench.validationBaseURL')
  if (draft.kind === 'autoapi' && !draft.apiKeyID) return t('toolAccess.workbench.validationApiKey')
  return ''
}

function providerChanged(key: DraftKey, draft: ProviderDraft) {
  return draftChanged(key, draft) || (!draft.isNew && draft.enabled !== !!draft.view?.Enabled)
}

function stagedKeys() {
  return Object.keys(drafts.value).filter((key) => {
    const draft = drafts.value[key]
    return !!draft && !pendingDelete.value.has(key) && providerChanged(key, draft)
  })
}

const invalidStagedKeys = computed(() => stagedKeys().filter((key) => !!draftValidation(drafts.value[key])))
const hasStagedChanges = computed(() => stagedKeys().length > 0 || [...pendingDelete.value].some((key) => drafts.value[key] && !drafts.value[key].isNew) || commonConfigChanged.value)
const previewDisabled = computed(() => previewLoading.value || !hasStagedChanges.value || invalidStagedKeys.value.length > 0 || !!commonConfigValidation.value)
const isDirty = computed(() => hasStagedChanges.value)

function resetProviderDrafts(nextProviders: service.ToolProviderView[]) {
  const nextDrafts: Record<string, ProviderDraft> = {}
  const baseline: Record<string, string> = {}
  const source = props.tool === 'claude'
    ? nextProviders.filter((view) => view.Preset.ProviderID === 'anthropic').slice(0, 1)
    : nextProviders

  for (const view of source) {
    const key = viewKey(view)
    const draft = createDraft(view, key)
    nextDrafts[key] = draft
    baseline[key] = draftSnapshot(draft)
  }

  if (props.tool === 'claude' && !Object.keys(nextDrafts).length) {
    const draft = createDraft(null, 'new:anthropic', true)
    nextDrafts[draft.key] = draft
    baseline[draft.key] = draftSnapshot(draft)
  }

  drafts.value = nextDrafts
  providerBaseline.value = baseline
  pendingDelete.value = new Set()
}

async function loadCommonConfig() {
  commonConfigLoading.value = true
  try {
    const snippet = await api.getToolCommonConfig(props.tool)
    commonConfig.value = snippet || ''
    commonConfigBaseline.value = commonConfig.value
  } catch (error: any) {
    toast.push(error?.message || String(error), 'error')
  } finally {
    commonConfigLoading.value = false
  }
}

async function loadSupportingData() {
  supportingLoading.value = true
  try {
    const [keys, rules] = await Promise.all([api.apiKeys(), api.modelRules()])
    apiKeys.value = keys || []
    modelRules.value = rules || []
  } catch (error: any) {
    toast.push(error?.message || String(error), 'error')
  } finally {
    supportingLoading.value = false
  }
}

async function load() {
  loading.value = true
  loadingError.value = ''
  actionError.value = ''
  previewOpen.value = false
  previewData.value = []
  pendingPlan.value = null
  try {
    providers.value = await api.listToolProviders(props.tool)
    resetProviderDrafts(providers.value || [])
    await loadCommonConfig()
    selectedKey.value = null
    section.value = 'provider'

    if (props.initialProviderID) {
      const initial = providerRows.value.find((draft) => draft.providerID === props.initialProviderID)
      if (initial) selectedKey.value = initial.key
    } else if (props.tool === 'codex') {
      addProvider()
    } else {
      selectedKey.value = providerRows.value[0]?.key || null
    }
    keyVisible.value = false
    void loadSupportingData()
  } catch (error: any) {
    loadingError.value = error?.message || String(error)
  } finally {
    loading.value = false
  }
}

async function revealKey(key: DraftKey) {
  const draft = drafts.value[key]
  if (!draft || draft.isNew || draft.kind !== 'direct' || !draft.providerID) return
  const generation = ++revealGeneration.value
  revealLoading.value = true
  revealError.value = false
  try {
    const value = await api.revealToolProviderKey(props.tool, draft.providerID)
    if (generation === revealGeneration.value && drafts.value[key]) {
      drafts.value[key].plaintextKey = value || ''
      drafts.value[key].keyRevealed = true
      keyVisible.value = true
    }
  } catch {
    if (generation === revealGeneration.value) {
      revealError.value = true
      toast.push(t('toolAccess.workbench.revealFailed'), 'error')
    }
  } finally {
    if (generation === revealGeneration.value) revealLoading.value = false
  }
}

function selectProvider(key: DraftKey) {
  if (selectedKey.value === key) return
  revealGeneration.value++
  section.value = 'provider'
  selectedKey.value = key
  keyVisible.value = false
  revealError.value = false
  actionError.value = ''
}

function selectCommonConfig() {
  section.value = 'common'
  actionError.value = ''
}

function toggleKeyVisibility(key: DraftKey) {
  const draft = drafts.value[key]
  if (!draft || draft.isNew) return
  if (revealError.value || !draft.keyRevealed) {
    void revealKey(key)
    return
  }
  keyVisible.value = !keyVisible.value
}

function addProvider() {
  if (props.tool === 'claude') return
  const key = `new:${nextNewID.value++}`
  const draft = createDraft(null, key, true)
  drafts.value[key] = draft
  providerBaseline.value[key] = draftSnapshot(draft)
  selectedKey.value = key
  keyVisible.value = false
  revealError.value = false
}

function undoDelete(key: DraftKey) {
  const next = new Set(pendingDelete.value)
  next.delete(key)
  pendingDelete.value = next
}

function stageDelete(key: DraftKey) {
  const draft = drafts.value[key]
  if (!draft || draft.enabled) return
  const next = new Set(pendingDelete.value)
  next.add(key)
  pendingDelete.value = next
}

function toggleProvider(key: DraftKey) {
  const draft = drafts.value[key]
  if (!draft || pendingDelete.value.has(key)) return
  draft.enabled = !draft.enabled
}

function onKindChange() {
  const draft = selectedDraft.value
  if (!draft || draft.kind !== 'autoapi' || draft.modelRows.some((row) => row.name.trim())) return
  const named = modelRules.value.filter((rule) => rule.enabled)
  draft.modelRows = named.length ? named.map((rule, index) => ({ name: rule.name, isDefault: index === 0 })) : [emptyRow()]
}

function addModel() {
  selectedDraft.value?.modelRows.push(emptyRow())
}

function removeModel(index: number) {
  const draft = selectedDraft.value
  if (draft && draft.modelRows.length > 1) draft.modelRows.splice(index, 1)
}

function buildPreset(draft: ProviderDraft) {
  const models = draft.modelRows
    .filter((row) => row.name.trim())
    .map((row) => toolconfig.PresetModel.createFrom({ name: row.name.trim(), default: row.isDefault }))
  const original = draft.view?.Preset
  return toolconfig.Preset.createFrom({
    ID: original?.ID || 0,
    Tool: props.tool,
    Kind: draft.kind,
    Name: draft.name.trim(),
    ProviderID: props.tool === 'claude' ? 'anthropic' : draft.providerID.trim(),
    Vendor: '',
    BaseURL: draft.baseURL.trim(),
    APIKeyEnc: original?.APIKeyEnc || '',
    APIKeyID: draft.kind === 'autoapi' ? draft.apiKeyID : '',
    Models: models,
    Extra: { ...(original?.Extra || {}), ...(draft.extra || {}) },
    CreatedAt: original?.CreatedAt || 0,
    UpdatedAt: original?.UpdatedAt || 0,
  })
}

function buildPlan() {
  const operations: service.ToolProviderPlan[] = []
  for (const key of Object.keys(drafts.value)) {
    const draft = drafts.value[key]
    if (!draft) continue
    if (pendingDelete.value.has(key)) {
      if (!draft.isNew) operations.push(service.ToolProviderPlan.createFrom({ Action: 'remove', Preset: buildPreset(draft), PlaintextKey: '' }))
      continue
    }
    if (!providerChanged(key, draft)) continue
    operations.push(service.ToolProviderPlan.createFrom({
      Action: draft.enabled ? 'upsert' : 'park',
      Preset: buildPreset(draft),
      PlaintextKey: draft.kind === 'direct' ? draft.plaintextKey : '',
    }))
  }
  return service.ToolConfigPlan.createFrom({ Providers: operations, CommonConfig: commonConfig.value })
}

async function previewChange() {
  if (previewDisabled.value) return
  previewLoading.value = true
  actionError.value = ''
  try {
    pendingPlan.value = buildPlan()
    previewData.value = await api.previewToolConfigChange(props.tool, pendingPlan.value)
    selectedPreviewIndex.value = 0
    previewOpen.value = true
  } catch (error: any) {
    actionError.value = error?.message || String(error)
  } finally {
    previewLoading.value = false
  }
}

function driftMessage(states: service.DriftState[]) {
  const details = states.length
    ? states.map((state) => `${state.Resource}: ${state.Missing ? t('toolAccess.workbench.driftMissing') : state.Drifted ? t('toolAccess.workbench.driftChanged') : t('toolAccess.workbench.driftUnchanged')}\n${state.Path}`).join('\n\n')
    : t('toolAccess.workbench.driftNone')
  return `${t('toolAccess.workbench.configChangedMessage')}\n\n${details}`
}

async function confirmWrite(allowDrift = false) {
  if (!pendingPlan.value || saving.value) return
  const generation = modalGeneration.value
  saving.value = true
  actionError.value = ''
  try {
    await api.applyToolConfigChange(props.tool, pendingPlan.value, allowDrift)
    if (generation !== modalGeneration.value || !props.open) return
    toast.push(t('toolAccess.workbench.applied', { tool: toolLabel.value }), 'success')
    previewOpen.value = false
    pendingPlan.value = null
    emit('changed')
    emit('close')
  } catch (error: any) {
    if (generation !== modalGeneration.value || !props.open) return
    const message = error?.message || String(error)
    if (!allowDrift && message.includes('config file changed externally since last apply')) {
      try {
        const states = await api.checkToolDrift(props.tool)
        const ok = await confirm.open({
          title: t('toolAccess.workbench.configChangedTitle'),
          message: driftMessage(states),
          confirmText: t('toolAccess.workbench.configChangedConfirm'),
          danger: true,
        })
        if (ok && generation === modalGeneration.value && props.open) {
          saving.value = false
          await confirmWrite(true)
        }
      } catch (driftError: any) {
        actionError.value = driftError?.message || String(driftError)
      }
    } else actionError.value = message
  } finally {
    if (generation === modalGeneration.value) saving.value = false
  }
}

async function close() {
  if (saving.value) return
  if (previewOpen.value) {
    previewOpen.value = false
    return
  }
  if (isDirty.value) {
    const ok = await confirm.open({
      title: t('toolAccess.workbench.discardTitle'),
      message: t('toolAccess.workbench.discardMessage'),
      confirmText: t('toolAccess.workbench.discardConfirm'),
      danger: true,
    })
    if (!ok) return
  }
  emit('close')
}

function closePreview() {
  if (saving.value) return
  previewOpen.value = false
}

watch(modelRules, (rules) => {
  const draft = selectedDraft.value
  if (!draft?.isNew || draft.kind !== 'autoapi' || draft.modelRows.some((row) => row.name.trim())) return
  const named = (rules || []).filter((rule) => rule.enabled)
  if (named.length) draft.modelRows = named.map((rule, index) => ({ name: rule.name, isDefault: index === 0 }))
})

watch(() => props.open, (open) => {
  modalGeneration.value++
  if (open) {
    saving.value = false
    void load()
  }
})

watch(() => [props.tool, props.initialProviderID] as const, () => {
  if (!props.open) return
  modalGeneration.value++
  saving.value = false
  void load()
})
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-overlay" @click.self="close">
      <div class="modal-card tool-workbench" role="dialog" aria-modal="true">
        <div class="row-between modal-heading tool-workbench-heading">
          <div><div class="modal-title">{{ t(`toolAccess.workbench.${tool}.title`) }}</div><div class="section-sub">{{ t(`toolAccess.workbench.${tool}.subtitle`) }}</div></div>
          <button class="btn btn-icon" :disabled="saving" :title="t('common.close')" :aria-label="t('common.close')" @click="close"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button>
        </div>

        <div v-if="loading" class="text-muted tool-workbench-state">{{ t('toolAccess.workbench.loading') }}</div>
        <div v-else-if="loadingError" class="tool-inline-error" role="alert"><strong>{{ t('toolAccess.workbench.loadFailed') }}</strong><span>{{ loadingError }}</span><button class="btn btn-secondary" @click="load">{{ t('toolAccess.retry') }}</button></div>
        <template v-else>
          <div class="tool-workbench-body">
            <aside class="tool-workbench-sidebar">
              <div class="row-between tool-workbench-provider-heading"><span class="field-label">{{ t('toolAccess.workbench.providers') }}</span><button v-if="tool === 'codex'" class="btn btn-secondary tool-workbench-add" type="button" @click="addProvider"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>{{ t('toolAccess.presets.new') }}</button></div>
              <nav class="tool-workbench-provider-list" :aria-label="t('toolAccess.workbench.providers')">
                <div v-for="draft in providerRows" :key="draft.key" class="tool-workbench-provider-row" :class="{ active: selectedKey === draft.key, 'is-invalid': !!draftValidation(draft) && (draft.isNew ? draftChanged(draft.key, draft) : true), 'is-pending-delete': pendingDelete.has(draft.key) }">
                  <button type="button" class="tool-workbench-provider-select" @click="selectProvider(draft.key)">
                    <span class="tool-workbench-provider-main"><strong>{{ draft.name || (tool === 'claude' ? t('toolAccess.workbench.claude.provider') : t('toolAccess.workbench.codex.newProvider')) }}</strong><span class="tool-workbench-provider-meta"><span class="badge" :class="draft.enabled ? 'success' : ''">{{ draft.enabled ? t('toolAccess.presets.enabled') : t('toolAccess.presets.disabled') }}</span><span class="badge" :class="draft.kind === 'autoapi' ? 'info' : ''">{{ draft.kind === 'autoapi' ? t('toolAccess.presets.autoapi') : t('toolAccess.presets.direct') }}</span><span v-if="draft.isNew" class="badge info">{{ t('toolAccess.workbench.draft') }}</span></span></span>
                    <svg class="tool-workbench-provider-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="m9 18 6-6-6-6"/></svg>
                  </button>
                  <label class="toggle toggle-sm tool-workbench-provider-toggle" :aria-label="t(draft.enabled ? 'toolAccess.workbench.disableProvider' : 'toolAccess.workbench.enableProvider', { name: draft.name || draft.providerID })" @click.stop>
                    <input type="checkbox" :checked="draft.enabled" :disabled="pendingDelete.has(draft.key)" @change="toggleProvider(draft.key)"><span class="toggle-slider blue"/>
                  </label>
                  <button v-if="!draft.enabled && !pendingDelete.has(draft.key)" class="btn btn-icon btn-sm danger-icon tool-workbench-provider-delete" type="button" :title="t('common.delete')" :aria-label="t('common.delete')" @click="stageDelete(draft.key)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/></svg></button>
                  <button v-if="pendingDelete.has(draft.key)" class="btn btn-ghost tool-workbench-undo" type="button" @click="undoDelete(draft.key)">{{ t('toolAccess.workbench.undoDelete') }}</button>
                  <span v-if="draftValidation(draft) && (draft.isNew ? draftChanged(draft.key, draft) : true)" class="tool-workbench-row-hint">{{ draftValidation(draft) }}</span>
                </div>
              </nav>
              <button type="button" class="tool-workbench-global-nav" :class="{ active: section === 'common' }" @click="selectCommonConfig"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M4 12h16M4 17h16"/><circle cx="9" cy="7" r="2" fill="var(--surface)"/><circle cx="15" cy="12" r="2" fill="var(--surface)"/><circle cx="11" cy="17" r="2" fill="var(--surface)"/></svg>{{ t('toolAccess.workbench.commonConfig') }}</button>
              <div v-if="tool === 'claude'" class="tool-workbench-fixed-hint">{{ t('toolAccess.workbench.claude.fixedProviderHint') }}</div>
            </aside>

            <main class="tool-workbench-editor">
              <template v-if="section === 'provider' && selectedDraft">
                <div class="tool-workbench-editor-header"><div><h3 :class="{ 'tool-workbench-pending-title': selectedIsPendingDelete }">{{ selectedTitle }}</h3><p>{{ t(`toolAccess.workbench.${tool}.providerHelp`) }}</p></div><div class="row tool-workbench-editor-actions"><span class="badge" :class="selectedDraft.enabled ? 'success' : ''">{{ selectedDraft.enabled ? t('toolAccess.presets.enabled') : t('toolAccess.presets.disabled') }}</span><button v-if="!selectedDraft.enabled && !selectedIsPendingDelete" class="btn btn-icon danger-icon" type="button" :title="t('common.delete')" :aria-label="t('common.delete')" @click="stageDelete(selectedDraft.key)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/></svg></button><button v-if="selectedIsPendingDelete" class="btn btn-secondary" type="button" @click="undoDelete(selectedDraft.key)">{{ t('toolAccess.workbench.undoDelete') }}</button></div></div>
                <div v-if="selectedIsPendingDelete" class="tool-workbench-pending-note" role="status">{{ t('toolAccess.workbench.pendingDelete') }}</div>

                <div class="field"><label class="field-label">{{ t('toolAccess.preset.kind') }}</label><div class="tabs"><button class="tab" :class="{ active: selectedDraft.kind === 'direct' }" :disabled="selectedIsPendingDelete" @click="selectedDraft.kind = 'direct'">{{ t('toolAccess.preset.direct') }}</button><button class="tab" :class="{ active: selectedDraft.kind === 'autoapi' }" :disabled="selectedIsPendingDelete" @click="selectedDraft.kind = 'autoapi'; onKindChange()">{{ t('toolAccess.preset.autoapi') }}</button></div><div class="field-help">{{ t('toolAccess.workbench.kindHelp') }}</div></div>
                <div class="col-2 tool-workbench-form-grid"><div class="field"><label class="field-label">{{ t('toolAccess.preset.name') }}</label><input v-model="selectedDraft.name" class="input" :disabled="selectedIsPendingDelete" :placeholder="t('toolAccess.preset.namePlaceholder')"></div><div v-if="tool === 'codex'" class="field"><label class="field-label">{{ t('toolAccess.preset.providerID') }}</label><input v-model="selectedDraft.providerID" class="input mono" :disabled="selectedDraft.view?.Enabled || selectedIsPendingDelete" :placeholder="t('toolAccess.preset.providerIDPlaceholder')"><div class="field-help">{{ t('toolAccess.workbench.codex.providerIDHelp') }}</div></div><div v-else class="field"><label class="field-label">{{ t('toolAccess.preset.providerID') }}</label><div class="input mono tool-workbench-readonly">anthropic</div></div></div>

                <template v-if="selectedDraft.kind === 'direct'"><div class="field"><label class="field-label">{{ t('toolAccess.preset.baseURL') }}</label><input v-model="selectedDraft.baseURL" class="input mono" :disabled="selectedIsPendingDelete" :placeholder="t('toolAccess.preset.baseURLPlaceholder')"></div><div class="field"><label class="field-label">{{ t('toolAccess.preset.apiKey') }}</label><div class="tool-workbench-key-input"><input v-model="selectedDraft.plaintextKey" :type="keyVisible ? 'text' : 'password'" autocomplete="new-password" class="input mono" :disabled="selectedIsPendingDelete" :placeholder="selectedView?.Preset.APIKeyEnc || revealError ? t('toolAccess.preset.keyKeepHint') : t('toolAccess.preset.keyPlaceholder')" @input="selectedDraft.keyTouched = true"><button type="button" class="btn btn-icon" :disabled="revealLoading || selectedIsPendingDelete || selectedDraft.isNew" :title="revealError ? t('toolAccess.workbench.retryRevealKey') : keyVisible ? t('toolAccess.workbench.hideKey') : t('toolAccess.workbench.revealKey')" :aria-label="revealError ? t('toolAccess.workbench.retryRevealKey') : keyVisible ? t('toolAccess.workbench.hideKey') : t('toolAccess.workbench.revealKey')" @click="toggleKeyVisibility(selectedDraft.key)"><svg v-if="keyVisible" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3l18 18M10.6 10.6a2 2 0 0 0 2.8 2.8M9.9 4.3A10.6 10.6 0 0 1 12 4c5 0 8.6 4.3 9.5 8a10.7 10.7 0 0 1-2.1 4.1M6.2 6.2C4.5 7.5 3.4 9.3 2.5 12c.8 2.7 2.5 5 4.7 6.5A10.6 10.6 0 0 0 12 20c1.1 0 2.2-.2 3.2-.5"/></svg><svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 12S6 4 12 4s9.5 8 9.5 8-3.5 8-9.5 8-9.5-8-9.5-8Z"/><circle cx="12" cy="12" r="2.5"/></svg></button></div><div class="field-help">{{ selectedView?.Preset.APIKeyEnc ? t('toolAccess.preset.keyKeepHelp') : t('toolAccess.preset.keyHelp') }}</div></div></template>
                <template v-else><div class="field"><label class="field-label">{{ t('toolAccess.preset.apiKeySelector') }}</label><select v-model="selectedDraft.apiKeyID" class="select" :disabled="supportingLoading || selectedIsPendingDelete"><option value="" disabled>{{ t('toolAccess.preset.apiKeyPlaceholder') }}</option><option v-for="key in apiKeys" :key="key.id" :value="key.id">{{ key.name }}</option></select><div class="field-help">{{ supportingLoading ? t('toolAccess.preset.loadingSupporting') : t('toolAccess.preset.apiKeyHelp') }}</div></div><div class="field-help tool-workbench-relay-note">{{ t('toolAccess.workbench.relayHelp', { tool: toolLabel }) }}</div></template>

                <div v-if="tool === 'codex'" class="field"><label class="field-label">{{ t('toolAccess.workbench.wireApi') }}</label><select v-model="selectedDraft.extra.wire_api" class="select" :disabled="selectedIsPendingDelete"><option value="responses">{{ t('toolAccess.workbench.wireApiResponses') }}</option><option value="chat">{{ t('toolAccess.workbench.wireApiChat') }}</option></select><div class="field-help">{{ t('toolAccess.workbench.wireApiHelp') }}</div></div>

                <div class="field tool-workbench-model-section"><div class="row-between"><div><label class="field-label">{{ t('toolAccess.preset.models') }}</label><div class="field-help">{{ t('toolAccess.workbench.modelsHelp') }}</div></div><button class="btn btn-secondary" type="button" :disabled="selectedIsPendingDelete" @click="addModel"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>{{ t('toolAccess.preset.addModel') }}</button></div><div class="tool-workbench-model-editor"><div v-for="(row, index) in selectedDraft.modelRows" :key="index" class="tool-workbench-model-row"><div class="tool-workbench-model-index">{{ index + 1 }}</div><div class="tool-workbench-model-main"><input v-model="row.name" class="input mono" :disabled="selectedIsPendingDelete" :placeholder="t('toolAccess.preset.modelPlaceholder')"><label class="check-label"><input v-model="row.isDefault" type="checkbox" :disabled="selectedIsPendingDelete"> {{ t('toolAccess.preset.defaultModel') }}</label></div><button class="btn btn-icon" :disabled="selectedDraft.modelRows.length <= 1 || selectedIsPendingDelete" :title="t('toolAccess.preset.removeModel')" :aria-label="t('toolAccess.preset.removeModel')" @click="removeModel(index)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M5 12h14"/></svg></button></div></div></div>
                <div v-if="tool === 'claude'" class="field"><label class="field-label">{{ t('toolAccess.workbench.tierOverrides') }}</label><div class="tool-workbench-tier-grid"><div class="field"><label class="field-label">{{ t('toolAccess.workbench.tierHaiku') }}</label><input v-model="selectedDraft.extra.ANTHROPIC_DEFAULT_HAIKU_MODEL" class="input mono" :disabled="selectedIsPendingDelete" :placeholder="tierPlaceholder()"></div><div class="field"><label class="field-label">{{ t('toolAccess.workbench.tierSonnet') }}</label><input v-model="selectedDraft.extra.ANTHROPIC_DEFAULT_SONNET_MODEL" class="input mono" :disabled="selectedIsPendingDelete" :placeholder="tierPlaceholder()"></div><div class="field"><label class="field-label">{{ t('toolAccess.workbench.tierOpus') }}</label><input v-model="selectedDraft.extra.ANTHROPIC_DEFAULT_OPUS_MODEL" class="input mono" :disabled="selectedIsPendingDelete" :placeholder="tierPlaceholder()"></div></div><div class="field-help">{{ t('toolAccess.workbench.tierOverridesHelp') }}</div></div>
                <div v-if="draftValidation(selectedDraft) && (selectedDraft.isNew ? draftChanged(selectedDraft.key, selectedDraft) : true)" class="tool-workbench-validation" role="alert">{{ draftValidation(selectedDraft) }}</div>
                <div v-if="actionError" class="tool-workbench-error" role="alert">{{ actionError }}</div>
              </template>
              <template v-else-if="section === 'common'">
                <div class="tool-workbench-editor-header"><div><h3>{{ t('toolAccess.workbench.commonConfig') }}</h3><p>{{ tool === 'claude' ? t('toolAccess.workbench.commonConfigHelpClaude') : t('toolAccess.workbench.commonConfigHelpCodex') }}</p></div></div>
                <div class="field"><label class="field-label">{{ t('toolAccess.workbench.commonConfig') }}</label><textarea v-model="commonConfig" class="input mono tool-workbench-common-config" :disabled="commonConfigLoading" :rows="tool === 'claude' ? 10 : 8" :placeholder="tool === 'claude' ? t('toolAccess.workbench.commonConfigPlaceholderClaude') : t('toolAccess.workbench.commonConfigPlaceholderCodex')"></textarea><div class="field-help">{{ tool === 'claude' ? t('toolAccess.workbench.commonConfigHelpClaude') : t('toolAccess.workbench.commonConfigHelpCodex') }}</div></div>
                <div v-if="commonConfigValidation" class="tool-workbench-validation" role="alert">{{ commonConfigValidation }}</div>
                <div v-if="actionError" class="tool-workbench-error" role="alert">{{ actionError }}</div>
              </template>
              <div v-else class="tool-workbench-empty"><div class="tool-workbench-empty-mark">↗</div><h3>{{ t('toolAccess.workbench.selectProvider') }}</h3><p>{{ t('toolAccess.workbench.selectProviderHelp') }}</p></div>
            </main>
          </div>
          <div class="tool-workbench-footer"><span class="field-help">{{ hasStagedChanges ? t('toolAccess.workbench.stagedHint') : t('toolAccess.workbench.footerHint') }}</span><div class="row"><button class="btn btn-secondary" type="button" :disabled="saving" @click="close">{{ t('common.cancel') }}</button><button class="btn btn-primary" type="button" :disabled="previewDisabled" @click="previewChange">{{ previewLoading ? t('toolAccess.workbench.previewLoading') : t('toolAccess.workbench.previewChanges') }}</button></div></div>
        </template>
      </div>
    </div>

    <div v-if="previewOpen && selectedPreview" class="modal-overlay modal-overlay-stacked tool-workbench-preview-overlay" @click.self="closePreview">
      <div class="modal-card tool-workbench-preview-modal">
        <div class="row-between modal-heading tool-workbench-preview-heading"><div><div class="modal-title">{{ t(`toolAccess.workbench.${tool}.previewTitle`) }}</div><div class="section-sub text-mono tool-workbench-preview-path" :title="selectedPreview.Path">{{ previewFileLabel }}</div></div><button class="btn btn-icon" :disabled="saving" :title="t('common.close')" :aria-label="t('common.close')" @click="closePreview"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div>
        <div class="tool-workbench-preview-note">{{ t('toolAccess.workbench.previewNote') }}</div>
        <div v-if="previewData.length > 1" class="tabs tool-workbench-file-tabs" role="tablist" :aria-label="t('toolAccess.workbench.files')"><button v-for="(file, index) in previewData" :key="file.Path" class="tab" :class="{ active: selectedPreviewIndex === index }" role="tab" :aria-selected="selectedPreviewIndex === index" :title="file.Path" @click="selectedPreviewIndex = index">{{ basename(file.Path) }}</button></div>
        <DiffPreview class="tool-workbench-diff-preview" :before="selectedPreview.Before" :after="selectedPreview.After" />
        <div v-if="actionError" class="tool-workbench-error" role="alert">{{ actionError }}</div>
        <div class="row tool-workbench-preview-actions"><button class="btn btn-secondary" :disabled="saving" @click="closePreview">{{ t('toolAccess.workbench.cancelPreview') }}</button><button class="btn btn-primary" :disabled="saving" @click="confirmWrite()">{{ saving ? t('common.processing') : t('toolAccess.workbench.confirmSave') }}</button></div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.tool-workbench { width: 92vw; max-width: 1180px; height: 88vh; max-height: 920px; display: flex; flex-direction: column; overflow: hidden; }
.tool-workbench-heading { align-items: flex-start; flex: 0 0 auto; margin-bottom: 14px; }
.tool-workbench-body { min-height: 0; flex: 1 1 auto; display: grid; grid-template-columns: 280px minmax(0, 1fr); border: 1px solid var(--border); border-radius: var(--radius-sm); overflow: hidden; }
.tool-workbench-sidebar { min-width: 0; display: flex; flex-direction: column; padding: 14px 12px; border-right: 1px solid var(--border); background: color-mix(in srgb, var(--bg) 44%, var(--surface)); overflow-y: auto; }
.tool-workbench-provider-heading { align-items: center; gap: 8px; padding: 0 4px 10px; border-bottom: 1px solid var(--border); }
.tool-workbench-add { min-height: 29px; padding: 5px 8px; font-size: 11px; }
.tool-workbench-add svg { width: 13px; height: 13px; }
.tool-workbench-provider-list { display: flex; flex-direction: column; gap: 4px; margin-top: 8px; }
.tool-workbench-provider-row { position: relative; display: flex; align-items: center; gap: 3px; width: 100%; min-width: 0; padding: 5px 5px 5px 6px; border: 1px solid transparent; border-radius: var(--radius-sm); }
.tool-workbench-provider-row:hover { background: color-mix(in srgb, var(--surface) 70%, transparent); }
.tool-workbench-provider-row.active { border-color: color-mix(in srgb, var(--accent) 45%, var(--border)); background: var(--accent-soft); }
.tool-workbench-provider-row.is-invalid { border-color: color-mix(in srgb, var(--negative) 48%, var(--border)); }
.tool-workbench-provider-row.is-pending-delete { opacity: .58; }
.tool-workbench-provider-row.is-pending-delete strong { text-decoration: line-through; }
.tool-workbench-provider-select { display: flex; align-items: center; gap: 7px; min-width: 0; flex: 1; padding: 4px 2px; border: 0; background: transparent; color: var(--fg); text-align: left; cursor: pointer; }
.tool-workbench-provider-main { display: flex; flex-direction: column; gap: 5px; min-width: 0; flex: 1; }
.tool-workbench-provider-main strong { overflow: hidden; font-size: 12px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.tool-workbench-provider-meta { display: flex; gap: 4px; flex-wrap: wrap; }
.tool-workbench-provider-meta .badge { font-size: 9.5px; }
.tool-workbench-provider-arrow { width: 14px; height: 14px; flex: 0 0 auto; color: var(--muted); }
.tool-workbench-provider-toggle { margin-left: 2px; }
.tool-workbench-provider-delete { color: var(--muted); }
.tool-workbench-undo { flex: 0 0 auto; padding: 3px 5px; font-size: 10px; }
.tool-workbench-row-hint { position: absolute; left: 8px; right: 8px; bottom: -1px; overflow: hidden; color: var(--negative); font-size: 9px; text-overflow: ellipsis; white-space: nowrap; pointer-events: none; transform: translateY(100%); }
.tool-workbench-provider-row:has(.tool-workbench-row-hint) { margin-bottom: 10px; }
.tool-workbench-global-nav { display: flex; align-items: center; gap: 8px; width: 100%; margin-top: auto; padding: 10px 9px 9px; border: 1px solid transparent; border-top-color: var(--border); border-radius: var(--radius-sm); background: transparent; color: var(--muted); font: inherit; font-size: 12px; text-align: left; cursor: pointer; }
.tool-workbench-global-nav:hover, .tool-workbench-global-nav.active { border-color: var(--border); background: var(--surface); color: var(--fg); }
.tool-workbench-global-nav svg { width: 15px; height: 15px; }
.tool-workbench-fixed-hint { margin: auto 4px 0; padding: 10px 0 0; border-top: 1px solid var(--border); color: var(--muted); font-size: 10.5px; line-height: 1.45; }
.tool-workbench-editor { min-width: 0; padding: 22px 26px; overflow-y: auto; overflow-x: hidden; }
.tool-workbench-editor-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 14px; margin-bottom: 20px; }
.tool-workbench-editor-header h3 { margin: 0; font-size: 18px; font-weight: 650; }
.tool-workbench-editor-header p { margin: 5px 0 0; color: var(--muted); font-size: 12px; line-height: 1.45; }
.tool-workbench-editor-actions { align-items: center; flex: 0 0 auto; }
.tool-workbench-pending-title { color: var(--muted); text-decoration: line-through; }
.tool-workbench-pending-note { margin: -8px 0 16px; padding: 8px 10px; border-radius: var(--radius-sm); background: color-mix(in srgb, var(--warning) 12%, transparent); color: var(--warning); font-size: 12px; }
.tool-workbench-form-grid { gap: 15px 16px; margin-top: 15px; }
.tool-workbench-readonly { display: flex; align-items: center; color: var(--muted); }
.tool-workbench-key-input { display: flex; align-items: center; gap: 5px; }
.tool-workbench-key-input .input { min-width: 0; flex: 1; }
.tool-workbench-key-input .btn { flex: 0 0 auto; }
.tool-workbench-key-input svg { width: 16px; height: 16px; }
.tool-workbench-relay-note { margin: 6px 0 16px; padding: 9px 10px; border: 1px solid color-mix(in srgb, var(--accent) 28%, var(--border)); border-radius: var(--radius-sm); background: var(--accent-soft); }
.tool-workbench-tier-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; }
.tool-workbench-common-config { min-height: 220px; resize: vertical; white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.5; }
.tool-workbench-model-section { margin-top: 23px; padding-top: 18px; border-top: 1px solid var(--border); }
.tool-workbench-model-editor { display: flex; flex-direction: column; gap: 8px; margin-top: 8px; }
.tool-workbench-model-row { display: flex; align-items: center; gap: 9px; padding: 10px 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: color-mix(in srgb, var(--surface) 92%, var(--bg)); }
.tool-workbench-model-index { width: 24px; height: 24px; border-radius: 50%; background: var(--accent-soft); color: var(--accent); display: inline-flex; align-items: center; justify-content: center; font: 11px var(--font-mono); flex: 0 0 auto; }
.tool-workbench-model-main { min-width: 0; flex: 1; }
.tool-workbench-model-main .check-label { margin-top: 7px; }
.tool-workbench-validation, .tool-workbench-error { margin: 14px 0 8px; padding: 8px 10px; border-radius: var(--radius-sm); font-size: 12px; }
.tool-workbench-validation { background: color-mix(in srgb, var(--negative) 10%, transparent); color: var(--negative); }
.tool-workbench-error { border: 1px solid color-mix(in srgb, var(--negative) 35%, var(--border)); background: color-mix(in srgb, var(--negative) 10%, transparent); color: var(--negative); overflow-wrap: anywhere; }
.tool-workbench-empty { display: grid; place-items: center; min-height: 360px; padding: 30px; text-align: center; }
.tool-workbench-empty-mark { width: 42px; height: 42px; margin-bottom: 14px; border: 1px solid var(--border); border-radius: 50%; color: var(--accent); display: grid; place-items: center; font-size: 21px; }
.tool-workbench-empty h3 { margin: 0; font-size: 17px; }
.tool-workbench-empty p { max-width: 320px; margin: 7px 0 0; color: var(--muted); font-size: 12px; line-height: 1.5; }
.tool-workbench-footer { display: flex; align-items: center; justify-content: space-between; gap: 16px; flex: 0 0 auto; padding-top: 13px; }
.tool-workbench-footer .field-help { min-width: 0; }
.tool-workbench-state { padding: 55px 0; text-align: center; }
.tool-workbench-preview-overlay { isolation: isolate; }
.tool-workbench-preview-modal { width: min(1040px, 92vw); height: min(86vh, 860px); max-height: 86vh; display: flex; flex-direction: column; overflow: hidden; }
.tool-workbench-preview-heading { flex: 0 0 auto; align-items: flex-start; margin-bottom: 15px; }
.tool-workbench-preview-path { max-width: 70vw; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tool-workbench-preview-note { flex: 0 0 auto; margin-bottom: 12px; padding: 9px 11px; border: 1px solid color-mix(in srgb, var(--accent) 28%, var(--border)); border-radius: var(--radius-sm); background: var(--accent-soft); color: var(--muted); font-size: 12px; }
.tool-workbench-file-tabs { flex: 0 0 auto; align-self: flex-start; margin-bottom: 10px; }
.tool-workbench-diff-preview { min-height: 0; flex: 1 1 auto; }
.tool-workbench-preview-actions { flex: 0 0 auto; justify-content: flex-end; margin-top: 12px; }
@media (max-width: 900px) { .tool-workbench { width: 96vw; height: 90vh; } .tool-workbench-body { grid-template-columns: 245px minmax(0, 1fr); } .tool-workbench-editor { padding: 18px; } .tool-workbench-tier-grid { grid-template-columns: 1fr; } }
@media (max-width: 680px) { .tool-workbench { width: 100%; height: 100%; max-height: none; border-radius: 0; } .tool-workbench-body { grid-template-columns: 1fr; overflow: auto; } .tool-workbench-sidebar { max-height: 290px; border-right: none; border-bottom: 1px solid var(--border); } .tool-workbench-form-grid, .tool-workbench-tier-grid { grid-template-columns: 1fr; } .tool-workbench-footer { align-items: flex-end; flex-direction: column; } .tool-workbench-footer .row { width: 100%; justify-content: flex-end; } }
</style>
