<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/bridge'
import { useConfirm } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import { toolconfig } from '../../wailsjs/go/models'
import type { model, service } from '../../wailsjs/go/models'

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
type DraftKey = 'new' | `db:${number}` | `file:${string}`
type AutoupdateDraft = '' | 'true' | 'false'

const props = withDefaults(defineProps<{
  open: boolean
  initialProviderID?: string
}>(), { initialProviderID: '' })

const emit = defineEmits<{ close: []; changed: []; export: [view: service.ToolProviderView] }>()
const { t } = useI18n()
const toast = useToast()
const confirm = useConfirm()

const loading = ref(false)
const loadingError = ref('')
const providers = ref<service.ToolProviderView[]>([])
const apiKeys = ref<model.ApiKey[]>([])
const modelRules = ref<model.ModelRule[]>([])
const supportingLoading = ref(false)
const section = ref<Section>('provider')
const selectedKey = ref<DraftKey | null>(null)
const saving = ref(false)
const mutationBusy = ref(false)
const revealLoading = ref(false)
const revealError = ref(false)
const keyVisible = ref(false)
const previewLoading = ref(false)
const previewOpen = ref(false)
const previewData = ref<service.OmoSlimPreview | null>(null)
const pendingSettings = ref<toolconfig.OpencodeGlobalSettings | null>(null)
const globalSaving = ref(false)

const kind = ref<'direct' | 'autoapi'>('direct')
const name = ref('')
const providerID = ref('')
const vendor = ref('openai-compatible')
const baseURL = ref('')
const apiKeyID = ref('')
const plaintextKey = ref('')
const modelRows = ref<ModelRow[]>([])
const providerBaseline = ref('')

const globalModel = ref('')
const globalSmallModel = ref('')
const globalTheme = ref('')
const globalShare = ref('')
const globalAutoupdate = ref<AutoupdateDraft>('')
const globalBaseline = ref('')

const selectedView = computed(() => providers.value.find((view) => viewKey(view) === selectedKey.value) || null)
const editing = computed(() => selectedKey.value !== null && selectedKey.value !== 'new')
const editingEnabled = computed(() => editing.value && !!selectedView.value?.Enabled)
const storedKeyHint = computed(() => editing.value && !!selectedView.value?.Preset.APIKeyEnc)
const selectedTitle = computed(() => editing.value ? selectedView.value?.Preset.Name || providerID.value : t('toolAccess.opencode.newProvider'))
const providerValid = computed(() => {
  const hasModels = modelRows.value.some((row) => row.name.trim())
  return !!name.value.trim()
    && !!providerID.value.trim()
    && hasModels
    && (kind.value === 'autoapi' || !!baseURL.value.trim())
    && (kind.value === 'direct' || !!apiKeyID.value)
})
const isDirty = computed(() => section.value === 'global'
  ? globalSnapshot() !== globalBaseline.value
  : providerSnapshot() !== providerBaseline.value)

function viewKey(view: service.ToolProviderView): DraftKey {
  return view.InDB ? `db:${view.Preset.ID}` : `file:${view.Preset.ProviderID}`
}

function emptyRow(nameValue = ''): ModelRow {
  return { name: nameValue, isDefault: false, context: '', output: '', modalities: '', reasoning: false, variants: [] }
}

function rowsFromPreset(preset: toolconfig.Preset | null): ModelRow[] {
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
  if (!preset && kind.value === 'autoapi') {
    return modelRules.value.filter((rule) => rule.enabled).map((rule, index) => ({ ...emptyRow(rule.name), isDefault: index === 0 }))
  }
  return [emptyRow()]
}

function providerSnapshot() {
  return JSON.stringify({ kind: kind.value, name: name.value, providerID: providerID.value, vendor: vendor.value, baseURL: baseURL.value, apiKeyID: apiKeyID.value, plaintextKey: plaintextKey.value, modelRows: modelRows.value })
}

function globalSnapshot() {
  return JSON.stringify({ model: globalModel.value, smallModel: globalSmallModel.value, theme: globalTheme.value, share: globalShare.value, autoupdate: globalAutoupdate.value })
}

function resetProvider(preset: toolconfig.Preset | null) {
  kind.value = preset?.Kind === 'autoapi' ? 'autoapi' : 'direct'
  name.value = preset?.Name || ''
  providerID.value = preset?.ProviderID || ''
  vendor.value = preset?.Vendor || 'openai-compatible'
  baseURL.value = preset?.BaseURL || ''
  apiKeyID.value = preset?.APIKeyID || ''
  plaintextKey.value = ''
  revealError.value = false
  keyVisible.value = false
  modelRows.value = rowsFromPreset(preset)
  providerBaseline.value = providerSnapshot()
  if (preset?.Kind === 'direct' && preset.ProviderID) void revealKey(preset.ProviderID)
}

function resetNewProvider() {
  resetProvider(null)
  kind.value = 'direct'
  modelRows.value = [emptyRow()]
  providerBaseline.value = providerSnapshot()
}

function resetGlobal(settings: toolconfig.OpencodeGlobalSettings) {
  globalModel.value = settings.Model || ''
  globalSmallModel.value = settings.SmallModel || ''
  globalTheme.value = settings.Theme || ''
  globalShare.value = settings.Share || ''
  globalAutoupdate.value = settings.Autoupdate == null ? '' : settings.Autoupdate ? 'true' : 'false'
  globalBaseline.value = globalSnapshot()
}

async function revealKey(id: string) {
  revealLoading.value = true
  revealError.value = false
  try {
    plaintextKey.value = await api.revealToolProviderKey('opencode', id)
  } catch {
    plaintextKey.value = ''
    revealError.value = true
  } finally {
    revealLoading.value = false
    providerBaseline.value = providerSnapshot()
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
  try {
    const [nextProviders, settings] = await Promise.all([api.listToolProviders('opencode'), api.getOpencodeGlobalSettings()])
    providers.value = nextProviders || []
    resetGlobal(settings)
    const initial = props.initialProviderID ? providers.value.find((view) => view.Preset.ProviderID === props.initialProviderID) : null
    if (initial) await selectProvider(viewKey(initial), true)
    else if (props.initialProviderID) {
      selectedKey.value = null
      section.value = 'provider'
    } else {
      selectedKey.value = null
      section.value = 'provider'
    }
    void loadSupportingData()
  } catch (error: any) {
    loadingError.value = error?.message || String(error)
  } finally {
    loading.value = false
  }
}

function onKindChange() {
  if (kind.value === 'autoapi' && !modelRows.value.some((row) => row.name.trim())) {
    const named = modelRules.value.filter((rule) => rule.enabled)
    modelRows.value = named.length ? named.map((rule, index) => ({ ...emptyRow(rule.name), isDefault: index === 0 })) : [emptyRow()]
  }
}

function addModel() { modelRows.value.push(emptyRow()) }
function removeModel(index: number) { modelRows.value.splice(index, 1) }
function addVariant(row: ModelRow) { row.variants.push({ name: '', reasoningEffort: '' }) }
function removeVariant(row: ModelRow, index: number) { row.variants.splice(index, 1) }
function optionalNumber(value: string): number | undefined {
  const parsed = Number(value)
  return value.trim() && Number.isFinite(parsed) && parsed > 0 ? parsed : undefined
}

function buildPayload(): toolconfig.Preset {
  const models = modelRows.value.filter((row) => row.name.trim()).map((row) => {
    const context = optionalNumber(row.context)
    const output = optionalNumber(row.output)
    const variants = Object.fromEntries(row.variants.filter((variant) => variant.name.trim()).map((variant) => [variant.name.trim(), { reasoningEffort: variant.reasoningEffort.trim() || undefined }]))
    return { name: row.name.trim(), default: row.isDefault, limit: context || output ? { context, output } : undefined, modalities: row.modalities.split(',').map((item) => item.trim()).filter(Boolean), reasoning: row.reasoning, variants }
  })
  return toolconfig.Preset.createFrom({
    ID: selectedView.value?.Preset.ID || 0,
    Tool: 'opencode', Kind: kind.value, Name: name.value.trim(), ProviderID: providerID.value.trim(),
    Vendor: kind.value === 'direct' ? vendor.value.trim() : '', BaseURL: baseURL.value.trim(),
    APIKeyEnc: selectedView.value?.Preset.APIKeyEnc && selectedView.value.Preset.APIKeyEnc !== '********' ? selectedView.value.Preset.APIKeyEnc : '',
    APIKeyID: kind.value === 'autoapi' ? apiKeyID.value : '', Models: models, Extra: selectedView.value?.Preset.Extra || {},
    CreatedAt: selectedView.value?.Preset.CreatedAt || 0, UpdatedAt: selectedView.value?.Preset.UpdatedAt || 0,
  })
}

async function saveProvider() {
  if (saving.value || !providerValid.value) return
  saving.value = true
  try {
    const payload = buildPayload()
    if (editingEnabled.value) await api.updateEnabledToolPreset(payload, plaintextKey.value)
    else if (editing.value) await api.updateToolPreset(payload, plaintextKey.value)
    else await api.createToolPreset(payload, plaintextKey.value)
    const savedProviderID = payload.ProviderID
    toast.push(t('toolAccess.toast.presetSaved'), 'success')
    await reloadProviders(`provider:${savedProviderID}`)
    emit('changed')
  } catch (error: any) {
    toast.push(error?.message || String(error), 'error')
  } finally {
    saving.value = false
  }
}

async function reloadProviders(prefer: string) {
  providers.value = await api.listToolProviders('opencode')
  const preferred = prefer.startsWith('provider:') ? providers.value.find((view) => view.Preset.ProviderID === prefer.slice(9)) : null
  if (preferred) await selectProvider(viewKey(preferred), true)
  else if (selectedKey.value && selectedKey.value !== 'new') {
    const current = providers.value.find((view) => viewKey(view) === selectedKey.value)
    if (current) await selectProvider(viewKey(current), true)
  }
}

async function discardIfDirty() {
  if (!isDirty.value) return true
  return confirm.open({ title: t('toolAccess.opencode.discardTitle'), message: t('toolAccess.opencode.discardMessage'), confirmText: t('toolAccess.opencode.discardConfirm'), danger: true })
}

async function selectProvider(key: DraftKey, force = false) {
  if (!force && key === selectedKey.value) return
  if (!force && !(await discardIfDirty())) return
  section.value = 'provider'
  selectedKey.value = key
  if (key === 'new') resetNewProvider()
  else resetProvider(providers.value.find((view) => viewKey(view) === key)?.Preset || null)
}

async function selectGlobal() {
  if (section.value === 'global') return
  if (!(await discardIfDirty())) return
  section.value = 'global'
}

async function close() {
  if (!(await discardIfDirty())) return
  emit('close')
}

function buildGlobalSettings() {
  return toolconfig.OpencodeGlobalSettings.createFrom({
    Model: globalModel.value.trim(), SmallModel: globalSmallModel.value.trim(), Theme: globalTheme.value.trim(), Share: globalShare.value,
    Autoupdate: globalAutoupdate.value === '' ? undefined : globalAutoupdate.value === 'true',
  })
}

async function previewGlobal() {
  if (previewLoading.value || globalSaving.value || !isDirty.value) return
  previewLoading.value = true
  try {
    pendingSettings.value = buildGlobalSettings()
    previewData.value = await api.previewOpencodeGlobalChange(pendingSettings.value)
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

async function confirmGlobalWrite(allowDrift = false) {
  if (!pendingSettings.value || globalSaving.value) return
  globalSaving.value = true
  try {
    await api.applyOpencodeGlobalChange(pendingSettings.value, allowDrift)
    toast.push(t('toolAccess.opencode.globalApplied'), 'success')
    resetGlobal(await api.getOpencodeGlobalSettings())
    previewOpen.value = false
    pendingSettings.value = null
    emit('changed')
  } catch (error: any) {
    const message = error?.message || String(error)
    if (!allowDrift && message.includes('config file changed externally since last apply')) {
      try {
        const states = await api.checkToolDrift('opencode')
        const ok = await confirm.open({ title: t('toolAccess.omoSlim.configChangedTitle'), message: driftMessage(states), confirmText: t('toolAccess.omoSlim.configChangedConfirm'), danger: true })
        if (ok) {
          globalSaving.value = false
          await confirmGlobalWrite(true)
        }
      } catch (driftError: any) {
        toast.push(driftError?.message || String(driftError), 'error')
      }
    } else toast.push(message, 'error')
  } finally {
    globalSaving.value = false
  }
}

async function toggleProvider() {
  const view = selectedView.value
  if (!view || mutationBusy.value) return
  if (view.Enabled) {
    const ok = await confirm.open({ title: t('toolAccess.confirm.disableTitle'), message: t('toolAccess.confirm.disableMessage', { name: view.Preset.Name, tool: 'opencode' }), confirmText: t('toolAccess.presets.disable'), danger: true })
    if (!ok) return
  }
  mutationBusy.value = true
  try {
    if (view.Enabled) {
      await api.disableToolPreset('opencode', view.Preset.ProviderID)
      toast.push(t('toolAccess.toast.presetDisabled'), 'success')
    } else {
      await api.enableToolPreset(view.Preset.ID)
      toast.push(t('toolAccess.toast.presetEnabled'), 'success')
    }
    await reloadProviders(`provider:${view.Preset.ProviderID}`)
    emit('changed')
  } catch (error: any) {
    toast.push(error?.message || String(error), 'error')
  } finally {
    mutationBusy.value = false
  }
}

async function deleteProvider() {
  const view = selectedView.value
  if (!view || view.Enabled || !view.InDB || mutationBusy.value) return
  const ok = await confirm.open({ title: t('toolAccess.confirm.deleteTitle'), message: t('toolAccess.confirm.deleteMessage', { name: view.Preset.Name }), confirmText: t('common.delete'), danger: true })
  if (!ok) return
  mutationBusy.value = true
  try {
    await api.deleteToolPreset(view.Preset.ID)
    toast.push(t('toolAccess.toast.presetDeleted'), 'success')
    providers.value = (await api.listToolProviders('opencode')) || []
    selectedKey.value = null
    section.value = 'provider'
    emit('changed')
  } catch (error: any) {
    toast.push(error?.message || String(error), 'error')
  } finally {
    mutationBusy.value = false
  }
}

async function exportProvider() {
  const view = selectedView.value
  if (!view?.InDB) return
  emit('export', view)
}

watch(modelRules, (rules) => {
  if (selectedKey.value !== 'new' || kind.value !== 'autoapi' || modelRows.value.some((row) => row.name.trim())) return
  const named = (rules || []).filter((rule) => rule.enabled)
  if (named.length) modelRows.value = named.map((rule, index) => ({ ...emptyRow(rule.name), isDefault: index === 0 }))
})
watch(() => props.open, (open) => { if (open) void load() })
watch(() => props.initialProviderID, (id) => { if (props.open && id) void load() })
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-overlay" @click.self="close">
      <div class="modal-card opencode-workbench" role="dialog" aria-modal="true">
        <div class="row-between modal-heading opencode-workbench-heading">
          <div><div class="modal-title">{{ t('toolAccess.opencode.title') }}</div><div class="section-sub">{{ t('toolAccess.opencode.subtitle') }}</div></div>
          <button class="btn btn-icon" :title="t('common.close')" :aria-label="t('common.close')" @click="close"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button>
        </div>

        <div v-if="loading" class="text-muted opencode-workbench-state">{{ t('toolAccess.opencode.loading') }}</div>
        <div v-else-if="loadingError" class="tool-inline-error" role="alert"><strong>{{ t('toolAccess.opencode.loadFailed') }}</strong><span>{{ loadingError }}</span><button class="btn btn-secondary" @click="load">{{ t('toolAccess.retry') }}</button></div>
        <template v-else>
          <div class="opencode-workbench-body">
            <aside class="opencode-sidebar">
              <div class="row-between opencode-provider-heading"><span class="field-label">{{ t('toolAccess.opencode.providers') }}</span><button class="btn btn-secondary opencode-add-provider" type="button" @click="selectProvider('new')"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>{{ t('toolAccess.presets.new') }}</button></div>
              <nav class="opencode-provider-list" :aria-label="t('toolAccess.opencode.providers')">
                <button v-for="view in providers" :key="viewKey(view)" type="button" class="opencode-provider-row" :class="{ active: section === 'provider' && selectedKey === viewKey(view) }" @click="selectProvider(viewKey(view))">
                  <span class="opencode-provider-main"><strong>{{ view.Preset.Name }}</strong><span class="opencode-provider-meta"><span class="badge" :class="view.Enabled ? 'success' : ''">{{ view.Enabled ? t('toolAccess.presets.enabled') : t('toolAccess.presets.disabled') }}</span><span class="badge" :class="view.Preset.Kind === 'autoapi' ? 'info' : ''">{{ view.Preset.Kind === 'autoapi' ? t('toolAccess.presets.autoapi') : t('toolAccess.presets.direct') }}</span></span><span v-if="view.Preset.Kind === 'direct' && view.Preset.Vendor" class="opencode-provider-vendor">{{ t('toolAccess.vendors.' + view.Preset.Vendor) }}</span></span>
                  <svg class="opencode-provider-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="m9 18 6-6-6-6"/></svg>
                </button>
              </nav>
              <div v-if="selectedKey === 'new'" class="opencode-new-row"><span>{{ t('toolAccess.opencode.newProvider') }}</span><span class="badge info">{{ t('toolAccess.opencode.draft') }}</span></div>
              <button type="button" class="opencode-global-nav" :class="{ active: section === 'global' }" @click="selectGlobal"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M4 7h16M4 12h16M4 17h16"/><circle cx="9" cy="7" r="2" fill="var(--surface)"/><circle cx="15" cy="12" r="2" fill="var(--surface)"/><circle cx="11" cy="17" r="2" fill="var(--surface)"/></svg>{{ t('toolAccess.opencode.globalSettings') }}</button>
            </aside>

            <main class="opencode-editor">
              <template v-if="section === 'provider' && selectedKey">
                <div class="opencode-editor-header"><div><h3>{{ selectedTitle }}</h3><p>{{ editing ? t('toolAccess.opencode.providerHelp') : t('toolAccess.opencode.newProviderHelp') }}</p></div><span v-if="editing" class="badge" :class="selectedView?.Enabled ? 'success' : ''">{{ selectedView?.Enabled ? t('toolAccess.presets.enabled') : t('toolAccess.presets.disabled') }}</span></div>
                <div class="field"><label class="field-label">{{ t('toolAccess.preset.kind') }}</label><div class="tabs"><button class="tab" :class="{ active: kind === 'direct' }" :disabled="editing" @click="kind = 'direct'">{{ t('toolAccess.preset.direct') }}</button><button class="tab" :class="{ active: kind === 'autoapi' }" :disabled="editing" @click="kind = 'autoapi'; onKindChange()">{{ t('toolAccess.preset.autoapi') }}</button></div><div class="field-help">{{ editing ? t('toolAccess.preset.kindImmutable') : t('toolAccess.preset.kindHelp') }}</div></div>
                <div class="col-2 opencode-form-grid"><div class="field"><label class="field-label">{{ t('toolAccess.preset.name') }}</label><input v-model="name" class="input" :placeholder="t('toolAccess.preset.namePlaceholder')"></div><div class="field"><label class="field-label">{{ t('toolAccess.preset.providerID') }}</label><input v-model="providerID" class="input mono" :disabled="editingEnabled" :placeholder="t('toolAccess.preset.providerIDPlaceholder')"><div v-if="editingEnabled" class="field-help">{{ t('toolAccess.preset.providerIDLocked') }}</div></div></div>
                <template v-if="kind === 'direct'"><div class="col-2 opencode-form-grid"><div class="field"><label class="field-label">{{ t('toolAccess.preset.baseURL') }}</label><input v-model="baseURL" class="input mono" :placeholder="t('toolAccess.preset.baseURLPlaceholder')"></div><div class="field"><label class="field-label">{{ t('toolAccess.preset.vendor') }}</label><select v-model="vendor" class="select"><option value="openai-responses">{{ t('toolAccess.vendors.openai-responses') }}</option><option value="openai-compatible">{{ t('toolAccess.vendors.openai-compatible') }}</option><option value="anthropic">{{ t('toolAccess.vendors.anthropic') }}</option><option value="amazon-bedrock">{{ t('toolAccess.vendors.amazon-bedrock') }}</option><option value="google-gemini">{{ t('toolAccess.vendors.google-gemini') }}</option></select><div class="field-help">{{ t('toolAccess.preset.vendorHelp.' + (vendor || 'openai-compatible')) }}</div></div></div><div class="field"><label class="field-label">{{ t('toolAccess.preset.apiKey') }}</label><div class="opencode-key-input"><input v-model="plaintextKey" :type="keyVisible ? 'text' : 'password'" autocomplete="new-password" class="input mono" :placeholder="storedKeyHint || revealError ? t('toolAccess.preset.keyKeepHint') : t('toolAccess.preset.keyPlaceholder')"><button type="button" class="btn btn-icon" :disabled="revealLoading" :title="keyVisible ? t('toolAccess.opencode.hideKey') : t('toolAccess.opencode.revealKey')" :aria-label="keyVisible ? t('toolAccess.opencode.hideKey') : t('toolAccess.opencode.revealKey')" @click="keyVisible = !keyVisible"><svg v-if="!keyVisible" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6S2 12 2 12Z"/><circle cx="12" cy="12" r="2.5"/></svg><svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="m3 3 18 18M10.6 10.6a2 2 0 0 0 2.8 2.8M9.9 5.2A10.7 10.7 0 0 1 12 5c6.5 0 10 7 10 7a18.7 18.7 0 0 1-3.1 3.8M6.2 6.2C3.5 8.2 2 12 2 12s3.5 7 10 7c1.5 0 2.8-.3 4-.8"/></svg></button></div><div class="field-help">{{ revealLoading ? t('toolAccess.opencode.revealingKey') : revealError ? t('toolAccess.preset.keyKeepHelp') : storedKeyHint ? t('toolAccess.preset.keyKeepHelp') : t('toolAccess.preset.keyHelp') }}</div></div></template>
                <div v-else class="field"><label class="field-label">{{ t('toolAccess.preset.apiKeySelector') }}</label><select v-model="apiKeyID" class="select" :disabled="supportingLoading"><option value="" disabled>{{ t('toolAccess.preset.apiKeyPlaceholder') }}</option><option v-for="key in apiKeys" :key="key.id" :value="key.id">{{ key.name }}</option></select><div class="field-help">{{ supportingLoading ? t('toolAccess.preset.loadingSupporting') : t('toolAccess.preset.apiKeyHelp') }}</div></div>
                <div class="field opencode-model-section"><div class="row-between"><div><label class="field-label">{{ t('toolAccess.preset.models') }}</label><div class="field-help">{{ t('toolAccess.preset.modelsHelp') }}</div></div><button class="btn btn-secondary" type="button" @click="addModel"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>{{ t('toolAccess.preset.addModel') }}</button></div><div class="tool-model-editor"><div v-for="(row, index) in modelRows" :key="index" class="tool-model-row"><div class="row-between" style="align-items: flex-start;"><div class="tool-model-index">{{ index + 1 }}</div><div class="tool-model-main"><input v-model="row.name" class="input mono" :placeholder="t('toolAccess.preset.modelPlaceholder')"><div class="row tool-model-options"><label class="check-label"><input v-model="row.isDefault" type="checkbox"> {{ t('toolAccess.preset.defaultModel') }}</label><label class="check-label"><input v-model="row.reasoning" type="checkbox"> {{ t('toolAccess.preset.reasoning') }}</label></div></div><button class="btn btn-icon" :disabled="modelRows.length <= 1" :title="t('toolAccess.preset.removeModel')" :aria-label="t('toolAccess.preset.removeModel')" @click="removeModel(index)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M5 12h14"/></svg></button></div><div class="col-3 tool-model-fields"><div><label class="field-label">{{ t('toolAccess.preset.contextLimit') }}</label><input v-model="row.context" type="number" min="1" class="input mono" :placeholder="t('toolAccess.preset.optional')"></div><div><label class="field-label">{{ t('toolAccess.preset.outputLimit') }}</label><input v-model="row.output" type="number" min="1" class="input mono" :placeholder="t('toolAccess.preset.optional')"></div><div><label class="field-label">{{ t('toolAccess.preset.modalities') }}</label><input v-model="row.modalities" class="input" :placeholder="t('toolAccess.preset.modalitiesPlaceholder')"></div></div><div class="tool-variants"><div class="row-between"><span class="field-label">{{ t('toolAccess.preset.variants') }}</span><button class="btn btn-ghost" type="button" @click="addVariant(row)">{{ t('toolAccess.preset.addVariant') }}</button></div><div v-for="(variant, variantIndex) in row.variants" :key="variantIndex" class="row variant-row"><input v-model="variant.name" class="input" :placeholder="t('toolAccess.preset.variantName')"><input v-model="variant.reasoningEffort" class="input" :placeholder="t('toolAccess.preset.reasoningEffort')"><button class="btn btn-icon" :title="t('toolAccess.preset.removeVariant')" :aria-label="t('toolAccess.preset.removeVariant')" @click="removeVariant(row, variantIndex)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div><div v-if="!row.variants.length" class="field-help">{{ t('toolAccess.preset.noVariants') }}</div></div></div></div></div>
                <div v-if="!providerValid" class="tool-validation" role="alert">{{ t('toolAccess.preset.validation') }}</div>
                <div class="opencode-provider-footer"><div class="row opencode-secondary-actions"><button v-if="editing && selectedView?.InDB" class="btn btn-ghost" type="button" @click="exportProvider">{{ t('toolAccess.presets.export') }}</button><button v-if="editing && !selectedView?.Enabled && selectedView?.InDB" class="btn btn-danger-ghost" type="button" @click="deleteProvider">{{ t('common.delete') }}</button><button v-if="editing" class="btn btn-secondary" type="button" :disabled="mutationBusy" @click="toggleProvider">{{ selectedView?.Enabled ? t('toolAccess.presets.disable') : t('toolAccess.presets.enable') }}</button></div><div class="row"><button class="btn btn-secondary" type="button" @click="close">{{ t('common.cancel') }}</button><button class="btn btn-primary" type="button" :disabled="saving || !providerValid" @click="saveProvider">{{ saving ? t('common.processing') : t('common.save') }}</button></div></div>
              </template>
              <template v-else-if="section === 'global'"><div class="opencode-editor-header"><div><h3>{{ t('toolAccess.opencode.globalSettings') }}</h3><p>{{ t('toolAccess.opencode.globalHelp') }}</p></div></div><div class="opencode-global-form"><div class="field"><label class="field-label">{{ t('toolAccess.opencode.model') }}</label><input v-model="globalModel" class="input mono" placeholder="providerID/model"><div class="field-help">{{ t('toolAccess.opencode.modelHelp') }}</div></div><div class="field"><label class="field-label">{{ t('toolAccess.opencode.smallModel') }}</label><input v-model="globalSmallModel" class="input mono" placeholder="providerID/model"></div><div class="field"><label class="field-label">{{ t('toolAccess.opencode.theme') }}</label><input v-model="globalTheme" class="input" placeholder="default"></div><div class="field"><label class="field-label">{{ t('toolAccess.opencode.share') }}</label><select v-model="globalShare" class="select"><option value="">{{ t('toolAccess.opencode.unset') }}</option><option value="manual">{{ t('toolAccess.opencode.shareManual') }}</option><option value="auto">{{ t('toolAccess.opencode.shareAuto') }}</option><option value="disabled">{{ t('toolAccess.opencode.shareDisabled') }}</option></select></div><div class="field"><label class="field-label">{{ t('toolAccess.opencode.autoupdate') }}</label><div class="tabs"><button class="tab" :class="{ active: globalAutoupdate === '' }" @click="globalAutoupdate = ''">{{ t('toolAccess.opencode.unset') }}</button><button class="tab" :class="{ active: globalAutoupdate === 'true' }" @click="globalAutoupdate = 'true'">{{ t('toolAccess.opencode.enabled') }}</button><button class="tab" :class="{ active: globalAutoupdate === 'false' }" @click="globalAutoupdate = 'false'">{{ t('toolAccess.opencode.disabled') }}</button></div></div></div><div class="opencode-global-footer"><span class="field-help">{{ t('toolAccess.opencode.previewBeforeWrite') }}</span><button class="btn btn-primary" :disabled="previewLoading || !isDirty" @click="previewGlobal">{{ previewLoading ? t('toolAccess.opencode.previewLoading') : t('common.save') }}</button></div></template>
              <div v-else class="opencode-empty-editor"><div class="opencode-empty-mark">↗</div><h3>{{ t('toolAccess.opencode.selectProvider') }}</h3><p>{{ t('toolAccess.opencode.selectProviderHelp') }}</p></div>
            </main>
          </div>
          <div class="opencode-workbench-footer"><span class="field-help">{{ t('toolAccess.opencode.footerHint') }}</span><button class="btn btn-secondary" type="button" @click="close">{{ t('common.cancel') }}</button></div>
        </template>
      </div>
    </div>

    <div v-if="previewOpen && previewData" class="modal-overlay modal-overlay-stacked opencode-preview-overlay" @click.self="previewOpen = false">
      <div class="modal-card opencode-preview-modal"><div class="row-between modal-heading"><div><div class="modal-title">{{ t('toolAccess.opencode.previewTitle') }}</div><div class="section-sub text-mono opencode-preview-path">{{ previewData.Path }}</div></div><button class="btn btn-icon" :title="t('common.close')" :aria-label="t('common.close')" @click="previewOpen = false"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div><div class="omo-slim-preview-note">{{ t('toolAccess.opencode.previewNote') }}</div><div class="field"><label class="field-label">{{ t('toolAccess.opencode.previewAfter') }}</label><pre class="omo-slim-after-code">{{ previewData.After }}</pre></div><div class="row omo-slim-preview-actions"><button class="btn btn-secondary" :disabled="globalSaving" @click="previewOpen = false">{{ t('toolAccess.omoSlim.cancelPreview') }}</button><button class="btn btn-primary" :disabled="globalSaving" @click="confirmGlobalWrite()">{{ globalSaving ? t('common.processing') : t('toolAccess.omoSlim.confirmWrite') }}</button></div></div>
    </div>
  </Teleport>
</template>

<style scoped>
.opencode-workbench { width: 92vw; max-width: 1240px; height: 88vh; max-height: 920px; display: flex; flex-direction: column; overflow: hidden; }
.opencode-workbench-heading { align-items: flex-start; flex: 0 0 auto; margin-bottom: 14px; }
.opencode-workbench-body { min-height: 0; flex: 1 1 auto; display: grid; grid-template-columns: 286px minmax(0, 1fr); border: 1px solid var(--border); border-radius: var(--radius-sm); overflow: hidden; }
.opencode-sidebar { min-width: 0; display: flex; flex-direction: column; padding: 14px 12px; border-right: 1px solid var(--border); background: color-mix(in srgb, var(--bg) 44%, var(--surface)); overflow-y: auto; }
.opencode-provider-heading { align-items: center; gap: 8px; padding: 0 4px 10px; border-bottom: 1px solid var(--border); }
.opencode-add-provider { min-height: 29px; padding: 5px 8px; font-size: 11px; }
.opencode-add-provider svg { width: 13px; height: 13px; }
.opencode-provider-list { display: flex; flex-direction: column; gap: 2px; margin-top: 8px; }
.opencode-provider-row { display: flex; align-items: center; gap: 7px; width: 100%; min-width: 0; padding: 9px 6px 9px 9px; border: 1px solid transparent; border-radius: var(--radius-sm); background: transparent; color: var(--fg); text-align: left; cursor: pointer; }
.opencode-provider-row:hover { background: color-mix(in srgb, var(--surface) 70%, transparent); }
.opencode-provider-row.active { border-color: color-mix(in srgb, var(--accent) 45%, var(--border)); background: var(--accent-soft); }
.opencode-provider-main { display: flex; flex-direction: column; gap: 5px; min-width: 0; flex: 1; }
.opencode-provider-main strong { overflow: hidden; font-size: 12px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.opencode-provider-meta { display: flex; gap: 4px; flex-wrap: wrap; }
.opencode-provider-meta .badge { font-size: 9.5px; }
.opencode-provider-vendor { overflow: hidden; color: var(--muted); font-size: 10.5px; text-overflow: ellipsis; white-space: nowrap; }
.opencode-provider-arrow { width: 14px; height: 14px; flex: 0 0 auto; color: var(--muted); }
.opencode-new-row { display: flex; align-items: center; justify-content: space-between; gap: 7px; margin-top: 3px; padding: 9px; border: 1px dashed color-mix(in srgb, var(--accent) 45%, var(--border)); border-radius: var(--radius-sm); color: var(--accent); font-size: 11.5px; }
.opencode-global-nav { display: flex; align-items: center; gap: 8px; width: 100%; margin-top: auto; padding: 10px 9px 9px; border: 1px solid transparent; border-top-color: var(--border); border-radius: var(--radius-sm); background: transparent; color: var(--muted); font: inherit; font-size: 12px; text-align: left; cursor: pointer; }
.opencode-global-nav:hover, .opencode-global-nav.active { border-color: var(--border); background: var(--surface); color: var(--fg); }
.opencode-global-nav svg { width: 15px; height: 15px; }
.opencode-editor { min-width: 0; padding: 22px 26px; overflow-y: auto; overflow-x: hidden; }
.opencode-editor-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 14px; margin-bottom: 20px; }
.opencode-editor-header h3 { margin: 0; font-size: 18px; font-weight: 650; }
.opencode-editor-header p { margin: 5px 0 0; color: var(--muted); font-size: 12px; line-height: 1.45; }
.opencode-form-grid { gap: 15px 16px; margin-top: 15px; }
.opencode-key-input { display: flex; align-items: center; gap: 5px; }
.opencode-key-input .input { min-width: 0; flex: 1; }
.opencode-key-input .btn { flex: 0 0 auto; }
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
.opencode-provider-footer { display: flex; justify-content: space-between; gap: 12px; margin-top: 20px; padding-top: 14px; border-top: 1px solid var(--border); }
.opencode-secondary-actions { gap: 4px; flex-wrap: wrap; }
.opencode-global-form { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 18px 16px; max-width: 760px; }
.opencode-global-form .field:first-child { grid-column: 1 / -1; }
.opencode-global-footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; max-width: 760px; margin-top: 26px; padding-top: 14px; border-top: 1px solid var(--border); }
.opencode-empty-editor { display: grid; place-items: center; min-height: 360px; padding: 30px; text-align: center; }
.opencode-empty-mark { width: 42px; height: 42px; margin-bottom: 14px; border: 1px solid var(--border); border-radius: 50%; color: var(--accent); display: grid; place-items: center; font-size: 21px; }
.opencode-empty-editor h3 { margin: 0; font-size: 17px; }
.opencode-empty-editor p { max-width: 320px; margin: 7px 0 0; color: var(--muted); font-size: 12px; line-height: 1.5; }
.opencode-workbench-footer { display: flex; align-items: center; justify-content: space-between; gap: 16px; flex: 0 0 auto; padding-top: 13px; }
.opencode-workbench-state { padding: 55px 0; text-align: center; }
.opencode-preview-overlay { isolation: isolate; }
.opencode-preview-modal { width: min(860px, 88vw); max-height: 82vh; display: flex; flex-direction: column; overflow: hidden; }
.opencode-preview-modal .modal-heading { flex: 0 0 auto; align-items: flex-start; margin-bottom: 15px; }
.opencode-preview-path { max-width: 70vw; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
@media (max-width: 900px) { .opencode-workbench { width: 96vw; height: 90vh; } .opencode-workbench-body { grid-template-columns: 225px minmax(0, 1fr); } .opencode-editor { padding: 18px; } }
@media (max-width: 680px) { .opencode-workbench { width: 100%; height: 100%; max-height: none; border-radius: 0; } .opencode-workbench-body { grid-template-columns: 1fr; overflow: auto; } .opencode-sidebar { max-height: 270px; border-right: none; border-bottom: 1px solid var(--border); } .opencode-global-nav { margin-top: 12px; } .opencode-form-grid, .opencode-global-form { grid-template-columns: 1fr; } .opencode-global-form .field:first-child { grid-column: auto; } .opencode-provider-footer, .opencode-global-footer, .opencode-workbench-footer { align-items: flex-end; flex-direction: column; } .opencode-provider-footer > .row, .opencode-global-footer, .opencode-workbench-footer > .row { width: 100%; justify-content: flex-end; } }
</style>
