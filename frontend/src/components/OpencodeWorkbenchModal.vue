<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/bridge'
import { useConfirm } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import DiffPreview from '@/components/DiffPreview.vue'
import { service, toolconfig } from '../../wailsjs/go/models'
import type { model } from '../../wailsjs/go/models'

type Section = 'provider' | 'global'
type VariantRow = { name: string; reasoningEffort: string }
type ModelRow = {
  name: string
  isDefault: boolean
  context: string
  output: string
  modalities: string
  reasoning: boolean
  variants: VariantRow[]
}
type DraftKey = `db:${number}` | `file:${string}` | `new:${number}`
type AutoupdateDraft = '' | 'true' | 'false'
type ProviderDraft = {
  key: DraftKey
  view: service.ToolProviderView | null
  isNew: boolean
  enabled: boolean
  kind: 'direct' | 'autoapi'
  name: string
  providerID: string
  vendor: string
  baseURL: string
  apiKeyID: string
  plaintextKey: string
  keyTouched: boolean
  modelRows: ModelRow[]
}
type GlobalDraft = {
  model: string
  smallModel: string
  theme: string
  share: string
  autoupdate: AutoupdateDraft
}

const props = withDefaults(defineProps<{
  open: boolean
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
const section = ref<Section>('provider')
const selectedKey = ref<DraftKey | null>(null)
const pendingDelete = ref<Set<string>>(new Set())
const nextNewID = ref(1)
const keyVisible = ref(false)
const revealLoading = ref(false)
const revealError = ref(false)
const revealGeneration = ref(0)
const previewLoading = ref(false)
const saving = ref(false)
const modalGeneration = ref(0)
const previewOpen = ref(false)
const previewData = ref<service.OmoSlimPreview | null>(null)
const pendingPlan = ref<service.OpencodeConfigPlan | null>(null)

const globalDraft = ref<GlobalDraft>({ model: '', smallModel: '', theme: '', share: '', autoupdate: '' })
const globalBaseline = ref('')

const selectedDraft = computed(() => selectedKey.value ? drafts.value[selectedKey.value] || null : null)
const selectedView = computed(() => selectedDraft.value?.view || null)
const selectedIsPendingDelete = computed(() => !!selectedKey.value && pendingDelete.value.has(selectedKey.value))
const newDrafts = computed(() => Object.values(drafts.value).filter((draft) => draft.isNew))
const selectedTitle = computed(() => selectedDraft.value?.isNew ? t('toolAccess.opencode.newProvider') : selectedDraft.value?.name || selectedDraft.value?.providerID || t('toolAccess.opencode.provider'))
const selectedValidation = computed(() => selectedDraft.value ? draftValidation(selectedDraft.value) : '')

function viewKey(view: service.ToolProviderView): DraftKey {
  return view.InDB ? `db:${view.Preset.ID}` : `file:${view.Preset.ProviderID}`
}

function emptyRow(name = ''): ModelRow {
  return { name, isDefault: false, context: '', output: '', modalities: '', reasoning: false, variants: [] }
}

function rowsFromPreset(preset: toolconfig.Preset | null, kind: 'direct' | 'autoapi'): ModelRow[] {
  if (preset?.Models?.length) {
    return preset.Models.map((item) => ({
      name: item.name || '',
      isDefault: !!item.default,
      context: item.limit?.context != null ? String(item.limit.context) : '',
      output: item.limit?.output != null ? String(item.limit.output) : '',
      modalities: (item.modalities || []).join(', '),
      reasoning: !!item.reasoning,
      variants: Object.entries(item.variants || {}).map(([variantName, variant]) => ({ name: variantName, reasoningEffort: variant.reasoningEffort || '' })),
    }))
  }
  if (!preset && kind === 'autoapi') {
    return modelRules.value.filter((rule) => rule.enabled).map((rule, index) => ({ ...emptyRow(rule.name), isDefault: index === 0 }))
  }
  return [emptyRow()]
}

function createDraft(view: service.ToolProviderView | null, key: DraftKey, isNew = false): ProviderDraft {
  const preset = view?.Preset || null
  const kind = preset?.Kind === 'autoapi' ? 'autoapi' : 'direct'
  return {
    key,
    view,
    isNew,
    enabled: isNew ? false : !!view?.Enabled,
    kind,
    name: preset?.Name || '',
    providerID: preset?.ProviderID || '',
    vendor: preset?.Vendor || 'openai-compatible',
    baseURL: preset?.BaseURL || '',
    apiKeyID: preset?.APIKeyID || '',
    plaintextKey: '',
    keyTouched: false,
    modelRows: rowsFromPreset(preset, kind),
  }
}

function draftSnapshot(draft: ProviderDraft) {
  return JSON.stringify({
    kind: draft.kind,
    name: draft.name,
    providerID: draft.providerID,
    vendor: draft.vendor,
    baseURL: draft.baseURL,
    apiKeyID: draft.apiKeyID,
    modelRows: draft.modelRows,
  })
}

function globalSnapshot() {
  return JSON.stringify(globalDraft.value)
}

function draftValidation(draft: ProviderDraft) {
  if (!draft.name.trim()) return t('toolAccess.opencode.validationName')
  if (!draft.providerID.trim()) return t('toolAccess.opencode.validationProviderID')
  if (!draft.modelRows.some((row) => row.name.trim())) return t('toolAccess.opencode.validationModels')
  if (draft.kind === 'direct' && !draft.baseURL.trim()) return t('toolAccess.opencode.validationBaseURL')
  if (draft.kind === 'autoapi' && !draft.apiKeyID) return t('toolAccess.opencode.validationApiKey')
  return ''
}

function providerChanged(key: string, draft: ProviderDraft) {
  return draft.isNew || draft.keyTouched || draftSnapshot(draft) !== providerBaseline.value[key]
}

function stagedKeys() {
  return Object.keys(drafts.value).filter((key) => {
    const draft = drafts.value[key]
    if (!draft || pendingDelete.value.has(key)) return false
    return providerChanged(key, draft) || (!draft.isNew && draft.enabled !== !!draft.view?.Enabled)
  })
}

const invalidStagedKeys = computed(() => stagedKeys().filter((key) => !!draftValidation(drafts.value[key])))
const globalsChanged = computed(() => globalSnapshot() !== globalBaseline.value)
const hasStagedChanges = computed(() => stagedKeys().length > 0 || [...pendingDelete.value].some((key) => drafts.value[key] && !drafts.value[key].isNew) || globalsChanged.value)
const previewDisabled = computed(() => previewLoading.value || !hasStagedChanges.value || invalidStagedKeys.value.length > 0)
const isDirty = computed(() => hasStagedChanges.value)

function resetGlobal(settings: toolconfig.OpencodeGlobalSettings) {
  globalDraft.value = {
    model: settings.Model || '',
    smallModel: settings.SmallModel || '',
    theme: settings.Theme || '',
    share: settings.Share || '',
    autoupdate: settings.Autoupdate == null ? '' : settings.Autoupdate ? 'true' : 'false',
  }
  globalBaseline.value = globalSnapshot()
}

function buildGlobalSettings() {
  return toolconfig.OpencodeGlobalSettings.createFrom({
    Model: globalDraft.value.model.trim(),
    SmallModel: globalDraft.value.smallModel.trim(),
    Theme: globalDraft.value.theme.trim(),
    Share: globalDraft.value.share,
    Autoupdate: globalDraft.value.autoupdate === '' ? undefined : globalDraft.value.autoupdate === 'true',
  })
}

function resetProviderDrafts(nextProviders: service.ToolProviderView[]) {
  const nextDrafts: Record<string, ProviderDraft> = {}
  const baseline: Record<string, string> = {}
  for (const view of nextProviders) {
    const key = viewKey(view)
    const draft = createDraft(view, key)
    nextDrafts[key] = draft
    baseline[key] = draftSnapshot(draft)
  }
  drafts.value = nextDrafts
  providerBaseline.value = baseline
  pendingDelete.value = new Set()
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
  previewOpen.value = false
  pendingPlan.value = null
  try {
    const [nextProviders, settings] = await Promise.all([api.listToolProviders('opencode'), api.getOpencodeGlobalSettings()])
    providers.value = nextProviders || []
    resetProviderDrafts(providers.value)
    resetGlobal(settings)
    selectedKey.value = null
    section.value = 'provider'
    const initial = props.initialProviderID ? providers.value.find((view) => view.Preset.ProviderID === props.initialProviderID) : null
    if (initial) await selectProvider(viewKey(initial), true)
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
    const value = await api.revealToolProviderKey('opencode', draft.providerID)
    if (generation === revealGeneration.value && drafts.value[key]) drafts.value[key].plaintextKey = value || ''
  } catch {
    if (generation === revealGeneration.value) {
      if (drafts.value[key]) drafts.value[key].plaintextKey = ''
      revealError.value = true
      toast.push(t('toolAccess.opencode.revealFailed'), 'error')
    }
  } finally {
    if (generation === revealGeneration.value) revealLoading.value = false
  }
}

async function selectProvider(key: DraftKey, force = false) {
  section.value = 'provider'
  if (!force && selectedKey.value === key) {
    if (revealError.value) void revealKey(key)
    return
  }
  revealGeneration.value++
  selectedKey.value = key
  keyVisible.value = false
  revealError.value = false
  const draft = drafts.value[key]
  if (draft?.kind === 'direct') void revealKey(key)
}

function toggleKeyVisibility(key: DraftKey) {
  if (revealError.value) {
    void revealKey(key)
    return
  }
  keyVisible.value = !keyVisible.value
}

function selectGlobal() {
  section.value = 'global'
}

function addProvider() {
  const key = `new:${nextNewID.value++}` as DraftKey
  drafts.value[key] = createDraft(null, key, true)
  selectedKey.value = key
  section.value = 'provider'
  keyVisible.value = false
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
  draft.modelRows = named.length ? named.map((rule, index) => ({ ...emptyRow(rule.name), isDefault: index === 0 })) : [emptyRow()]
}

function addModel() {
  if (selectedDraft.value) selectedDraft.value.modelRows.push(emptyRow())
}

function removeModel(index: number) {
  const draft = selectedDraft.value
  if (draft && draft.modelRows.length > 1) draft.modelRows.splice(index, 1)
}

function addVariant(row: ModelRow) { row.variants.push({ name: '', reasoningEffort: '' }) }
function removeVariant(row: ModelRow, index: number) { row.variants.splice(index, 1) }
function optionalNumber(value: string): number | undefined {
  const parsed = Number(value)
  return value.trim() && Number.isFinite(parsed) && parsed > 0 ? parsed : undefined
}

function buildPreset(draft: ProviderDraft) {
  const models = draft.modelRows.filter((row) => row.name.trim()).map((row) => {
    const context = optionalNumber(row.context)
    const output = optionalNumber(row.output)
    const variants = Object.fromEntries(row.variants.filter((variant) => variant.name.trim()).map((variant) => [variant.name.trim(), toolconfig.PresetVariant.createFrom({ reasoningEffort: variant.reasoningEffort.trim() || undefined })]))
    return toolconfig.PresetModel.createFrom({
      name: row.name.trim(),
      default: row.isDefault,
      limit: context || output ? toolconfig.ModelLimit.createFrom({ context, output }) : undefined,
      modalities: row.modalities.split(',').map((item) => item.trim()).filter(Boolean),
      reasoning: row.reasoning,
      variants,
    })
  })
  const original = draft.view?.Preset
  return toolconfig.Preset.createFrom({
    ID: original?.ID || 0,
    Tool: 'opencode',
    Kind: draft.kind,
    Name: draft.name.trim(),
    ProviderID: draft.providerID.trim(),
    Vendor: draft.kind === 'direct' ? draft.vendor.trim() : '',
    BaseURL: draft.baseURL.trim(),
    APIKeyEnc: original?.APIKeyEnc || '',
    APIKeyID: draft.kind === 'autoapi' ? draft.apiKeyID : '',
    Models: models,
    Extra: original?.Extra || {},
    CreatedAt: original?.CreatedAt || 0,
    UpdatedAt: original?.UpdatedAt || 0,
  })
}

function buildPlan() {
  const operations: service.OpencodeProviderPlan[] = []
  for (const key of Object.keys(drafts.value)) {
    const draft = drafts.value[key]
    if (!draft) continue
    if (pendingDelete.value.has(key)) {
      if (!draft.isNew) operations.push(service.OpencodeProviderPlan.createFrom({ Action: 'remove', Preset: buildPreset(draft), PlaintextKey: '' }))
      continue
    }
    if (!providerChanged(key, draft) && (!draft.view || draft.enabled === !!draft.view.Enabled)) continue
    operations.push(service.OpencodeProviderPlan.createFrom({
      Action: draft.enabled ? 'upsert' : 'park',
      Preset: buildPreset(draft),
      PlaintextKey: draft.plaintextKey,
    }))
  }
  return service.OpencodeConfigPlan.createFrom({ Providers: operations, Globals: buildGlobalSettings() })
}

async function previewChange() {
  if (previewDisabled.value) return
  previewLoading.value = true
  try {
    pendingPlan.value = buildPlan()
    previewData.value = await api.previewOpencodeConfigChange(pendingPlan.value)
    previewOpen.value = true
  } catch (error: any) {
    toast.push(error?.message || String(error), 'error')
  } finally {
    previewLoading.value = false
  }
}

function driftMessage(states: service.DriftState[]) {
  const details = states.length ? states.map((state) => `${state.Resource}: ${state.Missing ? t('toolAccess.omoSlim.driftMissing') : state.Drifted ? t('toolAccess.omoSlim.driftChanged') : t('toolAccess.omoSlim.driftUnchanged')}\n${state.Path}`).join('\n\n') : t('toolAccess.omoSlim.driftNone')
  return `${t('toolAccess.omoSlim.configChangedMessage')}\n\n${details}`
}

async function confirmWrite(allowDrift = false) {
  if (!pendingPlan.value || saving.value) return
  const generation = modalGeneration.value
  saving.value = true
  try {
    await api.applyOpencodeConfigChange(pendingPlan.value, allowDrift)
    if (generation !== modalGeneration.value || !props.open) return
    toast.push(t('toolAccess.toast.opencodeApplied'), 'success')
    previewOpen.value = false
    pendingPlan.value = null
    emit('changed')
    emit('close')
  } catch (error: any) {
    if (generation !== modalGeneration.value || !props.open) return
    const message = error?.message || String(error)
    if (!allowDrift && message.includes('config file changed externally since last apply')) {
      try {
        const states = await api.checkToolDrift('opencode')
        const ok = await confirm.open({ title: t('toolAccess.omoSlim.configChangedTitle'), message: driftMessage(states), confirmText: t('toolAccess.omoSlim.configChangedConfirm'), danger: true })
        if (ok && generation === modalGeneration.value && props.open) {
          saving.value = false
          await confirmWrite(true)
        }
      } catch (driftError: any) {
        toast.push(driftError?.message || String(driftError), 'error')
      }
    } else toast.push(message, 'error')
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
    const ok = await confirm.open({ title: t('toolAccess.opencode.discardTitle'), message: t('toolAccess.opencode.discardMessage'), confirmText: t('toolAccess.opencode.discardConfirm'), danger: true })
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
  if (named.length) draft.modelRows = named.map((rule, index) => ({ ...emptyRow(rule.name), isDefault: index === 0 }))
})

watch(() => props.open, (open) => {
  modalGeneration.value++
  if (open) {
    saving.value = false
    void load()
  }
})
watch(() => props.initialProviderID, (id) => {
  if (!props.open || !id) return
  modalGeneration.value++
  saving.value = false
  void load()
})
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-overlay" @click.self="close">
      <div class="modal-card opencode-workbench" role="dialog" aria-modal="true">
        <div class="row-between modal-heading opencode-workbench-heading">
          <div><div class="modal-title">{{ t('toolAccess.opencode.title') }}</div><div class="section-sub">{{ t('toolAccess.opencode.subtitle') }}</div></div>
          <button class="btn btn-icon" :disabled="saving" :title="t('common.close')" :aria-label="t('common.close')" @click="close"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button>
        </div>

        <div v-if="loading" class="text-muted opencode-workbench-state">{{ t('toolAccess.opencode.loading') }}</div>
        <div v-else-if="loadingError" class="tool-inline-error" role="alert"><strong>{{ t('toolAccess.opencode.loadFailed') }}</strong><span>{{ loadingError }}</span><button class="btn btn-secondary" @click="load">{{ t('toolAccess.retry') }}</button></div>
        <template v-else>
          <div class="opencode-workbench-body">
            <aside class="opencode-sidebar">
              <div class="row-between opencode-provider-heading"><span class="field-label">{{ t('toolAccess.opencode.providers') }}</span><button class="btn btn-secondary opencode-add-provider" type="button" @click="addProvider"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>{{ t('toolAccess.presets.new') }}</button></div>
              <nav class="opencode-provider-list" :aria-label="t('toolAccess.opencode.providers')">
                <div v-for="view in providers" :key="viewKey(view)" class="opencode-provider-row" :class="{ active: section === 'provider' && selectedKey === viewKey(view), 'is-invalid': !!draftValidation(drafts[viewKey(view)]), 'is-pending-delete': pendingDelete.has(viewKey(view)) }">
                  <button type="button" class="opencode-provider-select" @click="selectProvider(viewKey(view))">
                    <span class="opencode-provider-main"><strong>{{ drafts[viewKey(view)]?.name || view.Preset.Name }}</strong><span class="opencode-provider-meta"><span class="badge" :class="drafts[viewKey(view)]?.enabled ? 'success' : ''">{{ drafts[viewKey(view)]?.enabled ? t('toolAccess.presets.enabled') : t('toolAccess.presets.disabled') }}</span><span class="badge" :class="drafts[viewKey(view)]?.kind === 'autoapi' ? 'info' : ''">{{ drafts[viewKey(view)]?.kind === 'autoapi' ? t('toolAccess.presets.autoapi') : t('toolAccess.presets.direct') }}</span></span><span v-if="drafts[viewKey(view)]?.kind === 'direct' && drafts[viewKey(view)]?.vendor" class="opencode-provider-vendor">{{ t('toolAccess.vendors.' + drafts[viewKey(view)]?.vendor) }}</span></span>
                    <svg class="opencode-provider-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="m9 18 6-6-6-6"/></svg>
                  </button>
                  <label class="toggle toggle-sm opencode-provider-toggle" :aria-label="t(drafts[viewKey(view)]?.enabled ? 'toolAccess.opencode.disableProvider' : 'toolAccess.opencode.enableProvider', { name: drafts[viewKey(view)]?.name || view.Preset.Name })" @click.stop>
                    <input type="checkbox" :checked="drafts[viewKey(view)]?.enabled" :disabled="pendingDelete.has(viewKey(view))" @change="toggleProvider(viewKey(view))"><span class="toggle-slider blue"/>
                  </label>
                  <button v-if="!drafts[viewKey(view)]?.enabled && !pendingDelete.has(viewKey(view))" class="btn btn-icon btn-sm danger-icon opencode-provider-delete" type="button" :title="t('common.delete')" :aria-label="t('common.delete')" @click="stageDelete(viewKey(view))"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/></svg></button>
                  <button v-if="pendingDelete.has(viewKey(view))" class="btn btn-ghost opencode-undo" type="button" @click="undoDelete(viewKey(view))">{{ t('toolAccess.opencode.undoDelete') }}</button>
                  <span v-if="draftValidation(drafts[viewKey(view)])" class="opencode-row-hint">{{ draftValidation(drafts[viewKey(view)]) }}</span>
                </div>
                <div v-for="draft in newDrafts" :key="draft.key" class="opencode-provider-row" :class="{ active: section === 'provider' && selectedKey === draft.key, 'is-invalid': !!draftValidation(draft), 'is-pending-delete': pendingDelete.has(draft.key) }">
                  <button type="button" class="opencode-provider-select" @click="selectProvider(draft.key)">
                    <span class="opencode-provider-main"><strong>{{ draft.name || t('toolAccess.opencode.newProvider') }}</strong><span class="opencode-provider-meta"><span class="badge" :class="draft.enabled ? 'success' : ''">{{ draft.enabled ? t('toolAccess.presets.enabled') : t('toolAccess.presets.disabled') }}</span><span class="badge info">{{ draft.kind === 'autoapi' ? t('toolAccess.presets.autoapi') : t('toolAccess.presets.direct') }}</span><span class="badge info">{{ t('toolAccess.opencode.draft') }}</span></span></span>
                    <svg class="opencode-provider-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="m9 18 6-6-6-6"/></svg>
                  </button>
                  <label class="toggle toggle-sm opencode-provider-toggle" :aria-label="t(draft.enabled ? 'toolAccess.opencode.disableProvider' : 'toolAccess.opencode.enableProvider', { name: draft.name || t('toolAccess.opencode.newProvider') })" @click.stop>
                    <input type="checkbox" :checked="draft.enabled" :disabled="pendingDelete.has(draft.key)" @change="toggleProvider(draft.key)"><span class="toggle-slider blue"/>
                  </label>
                  <button v-if="!draft.enabled && !pendingDelete.has(draft.key)" class="btn btn-icon btn-sm danger-icon opencode-provider-delete" type="button" :title="t('common.delete')" :aria-label="t('common.delete')" @click="stageDelete(draft.key)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/></svg></button>
                  <button v-if="pendingDelete.has(draft.key)" class="btn btn-ghost opencode-undo" type="button" @click="undoDelete(draft.key)">{{ t('toolAccess.opencode.undoDelete') }}</button>
                  <span v-if="draftValidation(draft)" class="opencode-row-hint">{{ draftValidation(draft) }}</span>
                </div>
              </nav>
              <button type="button" class="opencode-global-nav" :class="{ active: section === 'global' }" @click="selectGlobal"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M4 7h16M4 12h16M4 17h16"/><circle cx="9" cy="7" r="2" fill="var(--surface)"/><circle cx="15" cy="12" r="2" fill="var(--surface)"/><circle cx="11" cy="17" r="2" fill="var(--surface)"/></svg>{{ t('toolAccess.opencode.globalSettings') }}</button>
            </aside>

            <main class="opencode-editor">
              <template v-if="section === 'provider' && selectedDraft">
                <div class="opencode-editor-header"><div><h3 :class="{ 'opencode-pending-title': selectedIsPendingDelete }">{{ selectedTitle }}</h3><p>{{ selectedDraft.isNew ? t('toolAccess.opencode.newProviderHelp') : t('toolAccess.opencode.providerHelp') }}</p></div><div class="row opencode-editor-actions"><span class="badge" :class="selectedDraft.enabled ? 'success' : ''">{{ selectedDraft.enabled ? t('toolAccess.presets.enabled') : t('toolAccess.presets.disabled') }}</span><button v-if="!selectedDraft.enabled && !selectedIsPendingDelete" class="btn btn-icon danger-icon" type="button" :title="t('common.delete')" :aria-label="t('common.delete')" @click="stageDelete(selectedDraft.key)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/></svg></button><button v-if="selectedIsPendingDelete" class="btn btn-secondary" type="button" @click="undoDelete(selectedDraft.key)">{{ t('toolAccess.opencode.undoDelete') }}</button></div></div>
                <div v-if="selectedIsPendingDelete" class="opencode-pending-note" role="status">{{ t('toolAccess.opencode.pendingDelete') }}</div>
                <div class="field"><label class="field-label">{{ t('toolAccess.preset.kind') }}</label><div class="tabs"><button class="tab" :class="{ active: selectedDraft.kind === 'direct' }" :disabled="!selectedDraft.isNew || selectedIsPendingDelete" @click="selectedDraft.kind = 'direct'">{{ t('toolAccess.preset.direct') }}</button><button class="tab" :class="{ active: selectedDraft.kind === 'autoapi' }" :disabled="!selectedDraft.isNew || selectedIsPendingDelete" @click="selectedDraft.kind = 'autoapi'; onKindChange()">{{ t('toolAccess.preset.autoapi') }}</button></div><div class="field-help">{{ selectedDraft.isNew ? t('toolAccess.preset.kindHelp') : t('toolAccess.preset.kindImmutable') }}</div></div>
                <div class="col-2 opencode-form-grid"><div class="field"><label class="field-label">{{ t('toolAccess.preset.name') }}</label><input v-model="selectedDraft.name" class="input" :disabled="selectedIsPendingDelete" :placeholder="t('toolAccess.preset.namePlaceholder')"></div><div class="field"><label class="field-label">{{ t('toolAccess.preset.providerID') }}</label><input v-model="selectedDraft.providerID" class="input mono" :disabled="!!selectedView?.Enabled || selectedIsPendingDelete" :placeholder="t('toolAccess.preset.providerIDPlaceholder')"><div v-if="selectedView?.Enabled" class="field-help">{{ t('toolAccess.preset.providerIDLocked') }}</div></div></div>
                <template v-if="selectedDraft.kind === 'direct'"><div class="col-2 opencode-form-grid"><div class="field"><label class="field-label">{{ t('toolAccess.preset.baseURL') }}</label><input v-model="selectedDraft.baseURL" class="input mono" :disabled="selectedIsPendingDelete" :placeholder="t('toolAccess.preset.baseURLPlaceholder')"></div><div class="field"><label class="field-label">{{ t('toolAccess.preset.vendor') }}</label><select v-model="selectedDraft.vendor" class="select" :disabled="selectedIsPendingDelete"><option value="openai-responses">{{ t('toolAccess.vendors.openai-responses') }}</option><option value="openai-compatible">{{ t('toolAccess.vendors.openai-compatible') }}</option><option value="anthropic">{{ t('toolAccess.vendors.anthropic') }}</option><option value="amazon-bedrock">{{ t('toolAccess.vendors.amazon-bedrock') }}</option><option value="google-gemini">{{ t('toolAccess.vendors.google-gemini') }}</option></select><div class="field-help">{{ t('toolAccess.preset.vendorHelp.' + (selectedDraft.vendor || 'openai-compatible')) }}</div></div></div><div class="field"><label class="field-label">{{ t('toolAccess.preset.apiKey') }}</label><div class="opencode-key-input"><input v-model="selectedDraft.plaintextKey" :type="keyVisible ? 'text' : 'password'" autocomplete="new-password" class="input mono" :disabled="selectedIsPendingDelete" :placeholder="selectedView?.Preset.APIKeyEnc || revealError ? t('toolAccess.preset.keyKeepHint') : t('toolAccess.preset.keyPlaceholder')" @input="selectedDraft.keyTouched = true"><button type="button" class="btn btn-icon" :disabled="revealLoading || selectedIsPendingDelete" :title="revealError ? t('toolAccess.opencode.retryRevealKey') : keyVisible ? t('toolAccess.opencode.hideKey') : t('toolAccess.opencode.revealKey')" :aria-label="revealError ? t('toolAccess.opencode.retryRevealKey') : keyVisible ? t('toolAccess.opencode.hideKey') : t('toolAccess.opencode.revealKey')" @click="toggleKeyVisibility(selectedDraft.key)"><svg v-if="!keyVisible" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6S2 12 2 12Z"/><circle cx="12" cy="12" r="2.5"/></svg><svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="m3 3 18 18M10.6 5.1A10.7 10.7 0 0 1 12 5c6.5 0 10 7 10 7a18.3 18.3 0 0 1-3.1 3.7M6.2 6.2C3.4 8.1 2 12 2 12s3.5 7 10 7a9.9 9.9 0 0 0 3.5-.6"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg></button></div><div class="field-help">{{ selectedView?.Preset.APIKeyEnc || revealError ? t('toolAccess.preset.keyKeepHelp') : t('toolAccess.preset.keyHelp') }}</div><div v-if="revealError" class="field-help opencode-key-error" role="alert">{{ t('toolAccess.opencode.revealFailed') }}</div></div></template>
                <div v-else class="field"><label class="field-label">{{ t('toolAccess.preset.apiKeySelector') }}</label><select v-model="selectedDraft.apiKeyID" class="select" :disabled="supportingLoading || selectedIsPendingDelete"><option value="" disabled>{{ t('toolAccess.preset.apiKeyPlaceholder') }}</option><option v-for="key in apiKeys" :key="key.id" :value="key.id">{{ key.name }}</option></select><div class="field-help">{{ supportingLoading ? t('toolAccess.preset.loadingSupporting') : t('toolAccess.preset.apiKeyHelp') }}</div></div>
                <div class="field opencode-model-section"><div class="row-between"><div><label class="field-label">{{ t('toolAccess.preset.models') }}</label><div class="field-help">{{ t('toolAccess.preset.modelsHelp') }}</div></div><button class="btn btn-secondary" type="button" :disabled="selectedIsPendingDelete" @click="addModel"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>{{ t('toolAccess.preset.addModel') }}</button></div><div class="tool-model-editor"><div v-for="(row, index) in selectedDraft.modelRows" :key="index" class="tool-model-row"><div class="row-between" style="align-items: flex-start;"><div class="tool-model-index">{{ index + 1 }}</div><div class="tool-model-main"><input v-model="row.name" class="input mono" :disabled="selectedIsPendingDelete" :placeholder="t('toolAccess.preset.modelPlaceholder')"><div class="row tool-model-options"><label class="check-label"><input v-model="row.isDefault" type="checkbox" :disabled="selectedIsPendingDelete"> {{ t('toolAccess.preset.defaultModel') }}</label><label class="check-label"><input v-model="row.reasoning" type="checkbox" :disabled="selectedIsPendingDelete"> {{ t('toolAccess.preset.reasoning') }}</label></div></div><button class="btn btn-icon" :disabled="selectedDraft.modelRows.length <= 1 || selectedIsPendingDelete" :title="t('toolAccess.preset.removeModel')" :aria-label="t('toolAccess.preset.removeModel')" @click="removeModel(index)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M5 12h14"/></svg></button></div><div class="col-3 tool-model-fields"><div><label class="field-label">{{ t('toolAccess.preset.contextLimit') }}</label><input v-model="row.context" type="number" min="1" class="input mono" :disabled="selectedIsPendingDelete" :placeholder="t('toolAccess.preset.optional')"></div><div><label class="field-label">{{ t('toolAccess.preset.outputLimit') }}</label><input v-model="row.output" type="number" min="1" class="input mono" :disabled="selectedIsPendingDelete" :placeholder="t('toolAccess.preset.optional')"></div><div><label class="field-label">{{ t('toolAccess.preset.modalities') }}</label><input v-model="row.modalities" class="input mono" :disabled="selectedIsPendingDelete" :placeholder="t('toolAccess.preset.modalitiesPlaceholder')"></div></div><div class="tool-variants"><div class="row-between"><span class="field-label">{{ t('toolAccess.preset.variants') }}</span><button class="btn btn-ghost" type="button" :disabled="selectedIsPendingDelete" @click="addVariant(row)">{{ t('toolAccess.preset.addVariant') }}</button></div><div v-if="row.variants.length" v-for="(variant, variantIndex) in row.variants" :key="variantIndex" class="row variant-row"><input v-model="variant.name" class="input mono" :disabled="selectedIsPendingDelete" :placeholder="t('toolAccess.preset.variantName')"><input v-model="variant.reasoningEffort" class="input" :disabled="selectedIsPendingDelete" :placeholder="t('toolAccess.preset.reasoningEffort')"><button class="btn btn-icon" :disabled="selectedIsPendingDelete" :title="t('toolAccess.preset.removeVariant')" :aria-label="t('toolAccess.preset.removeVariant')" @click="removeVariant(row, variantIndex)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M5 12h14"/></svg></button></div><div v-else class="field-help">{{ t('toolAccess.preset.noVariants') }}</div></div></div></div></div>
                <div v-if="selectedValidation && !selectedIsPendingDelete" class="tool-validation" role="alert">{{ selectedValidation }}</div>
              </template>
              <template v-else-if="section === 'global'">
                <div class="opencode-editor-header"><div><h3>{{ t('toolAccess.opencode.globalSettings') }}</h3><p>{{ t('toolAccess.opencode.globalHelp') }}</p></div></div>
                <div class="opencode-global-form"><div class="field"><label class="field-label">{{ t('toolAccess.opencode.model') }}</label><input v-model="globalDraft.model" class="input mono" placeholder="providerID/model"><div class="field-help">{{ t('toolAccess.opencode.modelHelp') }}</div></div><div class="field"><label class="field-label">{{ t('toolAccess.opencode.smallModel') }}</label><input v-model="globalDraft.smallModel" class="input mono" placeholder="providerID/model"></div><div class="field"><label class="field-label">{{ t('toolAccess.opencode.theme') }}</label><input v-model="globalDraft.theme" class="input" placeholder="default"></div><div class="field"><label class="field-label">{{ t('toolAccess.opencode.share') }}</label><select v-model="globalDraft.share" class="select"><option value="">{{ t('toolAccess.opencode.unset') }}</option><option value="manual">{{ t('toolAccess.opencode.shareManual') }}</option><option value="auto">{{ t('toolAccess.opencode.shareAuto') }}</option><option value="disabled">{{ t('toolAccess.opencode.shareDisabled') }}</option></select></div><div class="field"><label class="field-label">{{ t('toolAccess.opencode.autoupdate') }}</label><div class="tabs"><button class="tab" :class="{ active: globalDraft.autoupdate === '' }" @click="globalDraft.autoupdate = ''">{{ t('toolAccess.opencode.unset') }}</button><button class="tab" :class="{ active: globalDraft.autoupdate === 'true' }" @click="globalDraft.autoupdate = 'true'">{{ t('toolAccess.opencode.enabled') }}</button><button class="tab" :class="{ active: globalDraft.autoupdate === 'false' }" @click="globalDraft.autoupdate = 'false'">{{ t('toolAccess.opencode.disabled') }}</button></div></div></div>
              </template>
              <div v-else class="opencode-empty-editor"><div class="opencode-empty-mark">↗</div><h3>{{ t('toolAccess.opencode.selectProvider') }}</h3><p>{{ t('toolAccess.opencode.selectProviderHelp') }}</p></div>
            </main>
          </div>
          <div class="opencode-workbench-footer"><span class="field-help">{{ hasStagedChanges ? t('toolAccess.opencode.stagedHint') : t('toolAccess.opencode.footerHint') }}</span><div class="row"><button class="btn btn-secondary" type="button" :disabled="saving" @click="close">{{ t('common.cancel') }}</button><button class="btn btn-primary" type="button" :disabled="previewDisabled" @click="previewChange">{{ previewLoading ? t('toolAccess.opencode.previewLoading') : t('toolAccess.opencode.previewChanges') }}</button></div></div>
        </template>
      </div>
    </div>

    <div v-if="previewOpen && previewData" class="modal-overlay modal-overlay-stacked opencode-preview-overlay" @click.self="closePreview">
      <div class="modal-card opencode-preview-modal"><div class="row-between modal-heading"><div><div class="modal-title">{{ t('toolAccess.opencode.previewTitle') }}</div><div class="section-sub text-mono opencode-preview-path">{{ previewData.Path }}</div></div><button class="btn btn-icon" :disabled="saving" :title="t('common.close')" :aria-label="t('common.close')" @click="closePreview"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div><div class="opencode-preview-note">{{ t('toolAccess.opencode.previewNote') }}</div><DiffPreview class="opencode-diff-preview" :before="previewData.Before" :after="previewData.After" /><div class="row omo-slim-preview-actions"><button class="btn btn-secondary" :disabled="saving" @click="closePreview">{{ t('toolAccess.opencode.cancelPreview') }}</button><button class="btn btn-primary" :disabled="saving" @click="confirmWrite()">{{ saving ? t('common.processing') : t('toolAccess.opencode.confirmSave') }}</button></div></div>
    </div>
  </Teleport>
</template>

<style scoped>
.opencode-workbench { width: 92vw; max-width: 1240px; height: 88vh; max-height: 920px; display: flex; flex-direction: column; overflow: hidden; }
.opencode-workbench-heading { align-items: flex-start; flex: 0 0 auto; margin-bottom: 14px; }
.opencode-workbench-body { min-height: 0; flex: 1 1 auto; display: grid; grid-template-columns: 310px minmax(0, 1fr); border: 1px solid var(--border); border-radius: var(--radius-sm); overflow: hidden; }
.opencode-sidebar { min-width: 0; display: flex; flex-direction: column; padding: 14px 12px; border-right: 1px solid var(--border); background: color-mix(in srgb, var(--bg) 44%, var(--surface)); overflow-y: auto; }
.opencode-provider-heading { align-items: center; gap: 8px; padding: 0 4px 10px; border-bottom: 1px solid var(--border); }
.opencode-add-provider { min-height: 29px; padding: 5px 8px; font-size: 11px; }
.opencode-add-provider svg { width: 13px; height: 13px; }
.opencode-provider-list { display: flex; flex-direction: column; gap: 4px; margin-top: 8px; }
.opencode-provider-row { position: relative; display: flex; align-items: center; gap: 3px; width: 100%; min-width: 0; padding: 5px 5px 5px 6px; border: 1px solid transparent; border-radius: var(--radius-sm); }
.opencode-provider-row:hover { background: color-mix(in srgb, var(--surface) 70%, transparent); }
.opencode-provider-row.active { border-color: color-mix(in srgb, var(--accent) 45%, var(--border)); background: var(--accent-soft); }
.opencode-provider-row.is-invalid { border-color: color-mix(in srgb, var(--negative) 48%, var(--border)); }
.opencode-provider-row.is-pending-delete { opacity: .58; }
.opencode-provider-row.is-pending-delete .opencode-provider-main strong { text-decoration: line-through; }
.opencode-provider-select { display: flex; align-items: center; gap: 7px; min-width: 0; flex: 1; padding: 4px 2px; border: 0; background: transparent; color: var(--fg); text-align: left; cursor: pointer; }
.opencode-provider-main { display: flex; flex-direction: column; gap: 5px; min-width: 0; flex: 1; }
.opencode-provider-main strong { overflow: hidden; font-size: 12px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.opencode-provider-meta { display: flex; gap: 4px; flex-wrap: wrap; }
.opencode-provider-meta .badge { font-size: 9.5px; }
.opencode-provider-vendor { overflow: hidden; color: var(--muted); font-size: 10.5px; text-overflow: ellipsis; white-space: nowrap; }
.opencode-provider-arrow { width: 14px; height: 14px; flex: 0 0 auto; color: var(--muted); }
.opencode-provider-toggle { margin-left: 2px; }
.opencode-provider-delete { color: var(--muted); }
.opencode-undo { flex: 0 0 auto; padding: 3px 5px; font-size: 10px; }
.opencode-row-hint { position: absolute; left: 8px; right: 8px; bottom: -1px; overflow: hidden; color: var(--negative); font-size: 9px; text-overflow: ellipsis; white-space: nowrap; pointer-events: none; transform: translateY(100%); }
.opencode-provider-row:has(.opencode-row-hint) { margin-bottom: 10px; }
.opencode-global-nav { display: flex; align-items: center; gap: 8px; width: 100%; margin-top: auto; padding: 10px 9px 9px; border: 1px solid transparent; border-top-color: var(--border); border-radius: var(--radius-sm); background: transparent; color: var(--muted); font: inherit; font-size: 12px; text-align: left; cursor: pointer; }
.opencode-global-nav:hover, .opencode-global-nav.active { border-color: var(--border); background: var(--surface); color: var(--fg); }
.opencode-global-nav svg { width: 15px; height: 15px; }
.opencode-editor { min-width: 0; padding: 22px 26px; overflow-y: auto; overflow-x: hidden; }
.opencode-editor-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 14px; margin-bottom: 20px; }
.opencode-editor-header h3 { margin: 0; font-size: 18px; font-weight: 650; }
.opencode-editor-header p { margin: 5px 0 0; color: var(--muted); font-size: 12px; line-height: 1.45; }
.opencode-editor-actions { align-items: center; flex: 0 0 auto; }
.opencode-pending-title { color: var(--muted); text-decoration: line-through; }
.opencode-pending-note { margin: -8px 0 16px; padding: 8px 10px; border-radius: var(--radius-sm); background: color-mix(in srgb, var(--warning) 12%, transparent); color: var(--warning); font-size: 12px; }
.opencode-form-grid { gap: 15px 16px; margin-top: 15px; }
.opencode-key-input { display: flex; align-items: center; gap: 5px; }
.opencode-key-input .input { min-width: 0; flex: 1; }
.opencode-key-input .btn { flex: 0 0 auto; }
.opencode-key-error { color: var(--negative); }
.opencode-model-section { margin-top: 23px; padding-top: 18px; border-top: 1px solid var(--border); }
.tool-model-editor { display: flex; flex-direction: column; gap: 8px; margin-top: 8px; }
.tool-model-row { padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: color-mix(in srgb, var(--surface) 92%, var(--bg)); }
.tool-model-index { width: 24px; height: 24px; margin: 5px 8px 0 0; border-radius: 50%; background: var(--accent-soft); color: var(--accent); display: inline-flex; align-items: center; justify-content: center; font: 11px var(--font-mono); flex: 0 0 auto; }
.tool-model-main { min-width: 0; flex: 1; }
.tool-model-options { gap: 12px; margin-top: 7px; flex-wrap: wrap; }
.check-label { display: inline-flex; align-items: center; gap: 5px; color: var(--muted); font-size: 11.5px; cursor: pointer; }
.check-label input { accent-color: var(--accent); }
.tool-model-fields { gap: 8px; margin-top: 10px; }
.tool-model-fields > div { min-width: 0; }
.tool-model-fields .field-label { margin-bottom: 5px; }
.tool-variants { margin-top: 10px; padding-top: 9px; border-top: 1px solid var(--border); }
.variant-row { margin-top: 6px; }
.variant-row .input { min-width: 0; flex: 1; }
.tool-validation { margin: 14px 0 8px; padding: 8px 10px; border-radius: var(--radius-sm); background: rgba(217, 48, 37, .08); color: var(--negative); font-size: 12px; }
.opencode-global-form { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 18px 16px; max-width: 760px; }
.opencode-global-form .field:first-child { grid-column: 1 / -1; }
.opencode-empty-editor { display: grid; place-items: center; min-height: 360px; padding: 30px; text-align: center; }
.opencode-empty-mark { width: 42px; height: 42px; margin-bottom: 14px; border: 1px solid var(--border); border-radius: 50%; color: var(--accent); display: grid; place-items: center; font-size: 21px; }
.opencode-empty-editor h3 { margin: 0; font-size: 17px; }
.opencode-empty-editor p { max-width: 320px; margin: 7px 0 0; color: var(--muted); font-size: 12px; line-height: 1.5; }
.opencode-workbench-footer { display: flex; align-items: center; justify-content: space-between; gap: 16px; flex: 0 0 auto; padding-top: 13px; }
.opencode-workbench-state { padding: 55px 0; text-align: center; }
.opencode-preview-overlay { isolation: isolate; }
.opencode-preview-modal { width: min(1040px, 92vw); height: min(86vh, 860px); max-height: 86vh; display: flex; flex-direction: column; overflow: hidden; }
.opencode-preview-modal .modal-heading { flex: 0 0 auto; align-items: flex-start; margin-bottom: 15px; }
.opencode-preview-path { max-width: 70vw; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.opencode-preview-note { flex: 0 0 auto; margin-bottom: 12px; padding: 9px 11px; border: 1px solid color-mix(in srgb, var(--accent) 28%, var(--border)); border-radius: var(--radius-sm); background: var(--accent-soft); color: var(--muted); font-size: 12px; }
.opencode-diff-preview { min-height: 0; flex: 1 1 auto; }
@media (max-width: 900px) { .opencode-workbench { width: 96vw; height: 90vh; } .opencode-workbench-body { grid-template-columns: 245px minmax(0, 1fr); } .opencode-editor { padding: 18px; } }
@media (max-width: 680px) { .opencode-workbench { width: 100%; height: 100%; max-height: none; border-radius: 0; } .opencode-workbench-body { grid-template-columns: 1fr; overflow: auto; } .opencode-sidebar { max-height: 290px; border-right: none; border-bottom: 1px solid var(--border); } .opencode-global-nav { margin-top: 12px; } .opencode-form-grid, .opencode-global-form { grid-template-columns: 1fr; } .opencode-global-form .field:first-child { grid-column: auto; } .opencode-workbench-footer { align-items: flex-end; flex-direction: column; } .opencode-workbench-footer .row { width: 100%; justify-content: flex-end; } }
</style>
