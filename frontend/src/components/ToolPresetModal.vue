<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/bridge'
import { useToast } from '@/composables/useToast'
import { toolconfig } from '../../wailsjs/go/models'
import type { model } from '../../wailsjs/go/models'

const props = defineProps<{
  open: boolean
  tool: string
  preset: toolconfig.Preset | null
  apiKeys: model.ApiKey[]
  modelRules: model.ModelRule[]
}>()

const emit = defineEmits<{
  close: []
  saved: []
}>()

const { t } = useI18n()
const toast = useToast()
const saving = ref(false)
const modalGeneration = ref(0)
const mutationBusy = ref(false)
const kind = ref<'direct' | 'autoapi'>('direct')
const name = ref('')
const providerID = ref('')
const vendor = ref('')
const baseURL = ref('')
const apiKeyID = ref('')
const plaintextKey = ref('')

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

const modelRows = ref<ModelRow[]>([])
const editing = computed(() => !!props.preset)
const toolLabel = computed(() => t(`toolAccess.tools.${props.tool}`))
const storedKeyHint = computed(() => editing.value && !!props.preset?.APIKeyEnc)
const valid = computed(() => {
  const hasModels = modelRows.value.some((row) => row.name.trim())
  return !!name.value.trim()
    && hasModels
    && (kind.value === 'autoapi' || !!baseURL.value.trim())
    && (kind.value === 'direct' || !!apiKeyID.value)
})

function emptyRow(nameValue = ''): ModelRow {
  return {
    name: nameValue,
    isDefault: false,
    context: '',
    output: '',
    modalities: '',
    reasoning: false,
    variants: [],
  }
}

function rowsFromPreset(preset: toolconfig.Preset | null): ModelRow[] {
  if (preset?.Models?.length) {
    return preset.Models.map((model) => ({
      name: model.name || '',
      isDefault: !!model.default,
      context: model.limit?.context != null ? String(model.limit.context) : '',
      output: model.limit?.output != null ? String(model.limit.output) : '',
      modalities: (model.modalities || []).join(', '),
      reasoning: !!model.reasoning,
      variants: Object.entries(model.variants || {}).map(([variantName, variant]) => ({
        name: variantName,
        reasoningEffort: variant.reasoningEffort || '',
      })),
    }))
  }
  if (!preset && kind.value === 'autoapi') {
    return props.modelRules.filter((rule) => rule.enabled).map((rule, index) => ({
      ...emptyRow(rule.name),
      isDefault: index === 0,
    }))
  }
  return [emptyRow()]
}

function reset() {
  const preset = props.preset
  kind.value = preset?.Kind === 'autoapi' ? 'autoapi' : 'direct'
  name.value = preset?.Name || ''
  providerID.value = preset?.ProviderID || ''
  vendor.value = preset?.Vendor || ''
  baseURL.value = preset?.BaseURL || ''
  apiKeyID.value = preset?.APIKeyID || ''
  plaintextKey.value = ''
  modelRows.value = rowsFromPreset(preset)
  modalGeneration.value++
  mutationBusy.value = false
}

function onKindChange() {
  if (kind.value === 'autoapi' && !modelRows.value.some((row) => row.name.trim())) {
    modelRows.value = props.modelRules.filter((rule) => rule.enabled).map((rule, index) => ({
      ...emptyRow(rule.name),
      isDefault: index === 0,
    }))
  }
}

function addModel() {
  modelRows.value.push(emptyRow())
}

function removeModel(index: number) {
  modelRows.value.splice(index, 1)
}

function addVariant(row: ModelRow) {
  row.variants.push({ name: '', reasoningEffort: '' })
}

function removeVariant(row: ModelRow, index: number) {
  row.variants.splice(index, 1)
}

function optionalNumber(value: string): number | undefined {
  const parsed = Number(value)
  return value.trim() && Number.isFinite(parsed) && parsed > 0 ? parsed : undefined
}

function buildPayload(): toolconfig.Preset {
  const models = modelRows.value
    .filter((row) => row.name.trim())
    .map((row) => {
      const context = optionalNumber(row.context)
      const output = optionalNumber(row.output)
      const variants = Object.fromEntries(
        row.variants
          .filter((variant) => variant.name.trim())
          .map((variant) => [variant.name.trim(), { reasoningEffort: variant.reasoningEffort.trim() || undefined }])
      )
      return {
        name: row.name.trim(),
        default: row.isDefault,
        limit: context || output ? { context, output } : undefined,
        modalities: row.modalities.split(',').map((item) => item.trim()).filter(Boolean),
        reasoning: row.reasoning,
        variants,
      }
    })

  return toolconfig.Preset.createFrom({
    ID: props.preset?.ID || 0,
    Tool: props.tool,
    Kind: kind.value,
    Name: name.value.trim(),
    ProviderID: providerID.value.trim(),
    Vendor: vendor.value.trim(),
    BaseURL: baseURL.value.trim(),
    APIKeyEnc: props.preset?.APIKeyEnc || '',
    APIKeyID: kind.value === 'autoapi' ? apiKeyID.value : '',
    Models: models,
    Extra: props.preset?.Extra || {},
    CreatedAt: props.preset?.CreatedAt || 0,
    UpdatedAt: props.preset?.UpdatedAt || 0,
  })
}

async function save() {
  if (saving.value || mutationBusy.value || !valid.value) return
  mutationBusy.value = true
  saving.value = true
  const generation = modalGeneration.value
  try {
    const payload = buildPayload()
    if (editing.value) await api.updateToolPreset(payload, plaintextKey.value)
    else await api.createToolPreset(payload, plaintextKey.value)
    if (generation !== modalGeneration.value) return
    toast.push(t('toolAccess.toast.presetSaved'), 'success')
    emit('saved')
  } catch (e: any) {
    if (generation === modalGeneration.value) toast.push(e?.message || String(e), 'error')
  } finally {
    if (generation === modalGeneration.value) {
      saving.value = false
      mutationBusy.value = false
    }
  }
}

watch(() => props.open, (open) => {
  if (open) reset()
})
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-overlay" @click.self="emit('close')">
      <div class="modal-card wide modal-card-scroll tool-preset-modal">
        <div class="row-between modal-heading">
          <div>
            <div class="modal-title">{{ editing ? t('toolAccess.preset.edit') : t('toolAccess.preset.create') }}</div>
            <div class="section-sub">{{ t('toolAccess.preset.toolHint', { tool: toolLabel }) }}</div>
          </div>
          <button class="btn btn-icon" :title="t('common.close')" :aria-label="t('common.close')" @click="emit('close')">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>
          </button>
        </div>

        <div class="field">
          <label class="field-label">{{ t('toolAccess.preset.kind') }}</label>
          <div class="tabs">
            <button class="tab" :class="{ active: kind === 'direct' }" :disabled="editing" @click="kind = 'direct'">{{ t('toolAccess.preset.direct') }}</button>
            <button class="tab" :class="{ active: kind === 'autoapi' }" :disabled="editing" @click="kind = 'autoapi'; onKindChange()">{{ t('toolAccess.preset.autoapi') }}</button>
          </div>
          <div class="field-help">{{ editing ? t('toolAccess.preset.kindImmutable') : t('toolAccess.preset.kindHelp') }}</div>
        </div>

        <div class="col-2 tool-form-grid">
          <div class="field">
            <label class="field-label">{{ t('toolAccess.preset.name') }}</label>
            <input v-model="name" class="input" :placeholder="t('toolAccess.preset.namePlaceholder')">
          </div>
          <div class="field">
            <label class="field-label">{{ t('toolAccess.preset.providerID') }}</label>
            <input v-model="providerID" class="input mono" :placeholder="t('toolAccess.preset.providerIDPlaceholder')">
          </div>
        </div>

        <template v-if="kind === 'direct'">
          <div class="col-2 tool-form-grid">
            <div class="field">
              <label class="field-label">{{ t('toolAccess.preset.baseURL') }}</label>
              <input v-model="baseURL" class="input mono" :placeholder="t('toolAccess.preset.baseURLPlaceholder')">
            </div>
            <div class="field">
              <label class="field-label">{{ t('toolAccess.preset.vendor') }}</label>
              <input v-model="vendor" class="input" :placeholder="t('toolAccess.preset.vendorPlaceholder')">
              <div class="field-help">{{ t('toolAccess.preset.vendorHelp') }}</div>
            </div>
          </div>
          <div class="field">
            <label class="field-label">{{ t('toolAccess.preset.apiKey') }}</label>
            <input v-model="plaintextKey" type="password" autocomplete="new-password" class="input mono" :placeholder="storedKeyHint ? t('toolAccess.preset.keyKeepHint') : t('toolAccess.preset.keyPlaceholder')">
            <div class="field-help">{{ storedKeyHint ? t('toolAccess.preset.keyKeepHelp') : t('toolAccess.preset.keyHelp') }}</div>
          </div>
        </template>
        <template v-else>
          <div class="field">
            <label class="field-label">{{ t('toolAccess.preset.apiKeySelector') }}</label>
            <select v-model="apiKeyID" class="select">
              <option value="" disabled>{{ t('toolAccess.preset.apiKeyPlaceholder') }}</option>
              <option v-for="key in apiKeys" :key="key.id" :value="key.id">{{ key.name }}</option>
            </select>
            <div class="field-help">{{ t('toolAccess.preset.apiKeyHelp') }}</div>
          </div>
        </template>

        <div class="field">
          <div class="row-between">
            <div>
              <label class="field-label">{{ t('toolAccess.preset.models') }}</label>
              <div class="field-help">{{ t('toolAccess.preset.modelsHelp') }}</div>
            </div>
            <button class="btn btn-secondary" style="padding: 5px 10px; font-size: 12px;" @click="addModel">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
              {{ t('toolAccess.preset.addModel') }}
            </button>
          </div>
          <div class="tool-model-editor">
            <div v-for="(row, index) in modelRows" :key="index" class="tool-model-row">
              <div class="row-between" style="align-items: flex-start;">
                <div class="tool-model-index">{{ index + 1 }}</div>
                <div class="tool-model-main">
                  <input v-model="row.name" class="input mono" :placeholder="t('toolAccess.preset.modelPlaceholder')">
                  <div class="row tool-model-options">
                    <label class="check-label"><input v-model="row.isDefault" type="checkbox"> {{ t('toolAccess.preset.defaultModel') }}</label>
                    <label class="check-label"><input v-model="row.reasoning" type="checkbox"> {{ t('toolAccess.preset.reasoning') }}</label>
                  </div>
                </div>
                <button class="btn btn-icon" :disabled="modelRows.length <= 1" :title="t('toolAccess.preset.removeModel')" :aria-label="t('toolAccess.preset.removeModel')" @click="removeModel(index)">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M5 12h14"/></svg>
                </button>
              </div>
              <div class="col-3 tool-model-fields">
                <div><label class="field-label">{{ t('toolAccess.preset.contextLimit') }}</label><input v-model="row.context" type="number" min="1" class="input mono" :placeholder="t('toolAccess.preset.optional')"></div>
                <div><label class="field-label">{{ t('toolAccess.preset.outputLimit') }}</label><input v-model="row.output" type="number" min="1" class="input mono" :placeholder="t('toolAccess.preset.optional')"></div>
                <div><label class="field-label">{{ t('toolAccess.preset.modalities') }}</label><input v-model="row.modalities" class="input" :placeholder="t('toolAccess.preset.modalitiesPlaceholder')"></div>
              </div>
              <div class="tool-variants">
                <div class="row-between">
                  <span class="field-label">{{ t('toolAccess.preset.variants') }}</span>
                  <button class="btn btn-ghost" style="padding: 2px 4px; font-size: 11px;" @click="addVariant(row)">{{ t('toolAccess.preset.addVariant') }}</button>
                </div>
                <div v-for="(variant, variantIndex) in row.variants" :key="variantIndex" class="row variant-row">
                  <input v-model="variant.name" class="input" :placeholder="t('toolAccess.preset.variantName')">
                  <input v-model="variant.reasoningEffort" class="input" :placeholder="t('toolAccess.preset.reasoningEffort')">
                  <button class="btn btn-icon" :title="t('toolAccess.preset.removeVariant')" :aria-label="t('toolAccess.preset.removeVariant')" @click="removeVariant(row, variantIndex)">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>
                  </button>
                </div>
                <div v-if="!row.variants.length" class="field-help">{{ t('toolAccess.preset.noVariants') }}</div>
              </div>
            </div>
          </div>
        </div>

        <div v-if="!valid" class="tool-validation" role="alert">{{ t('toolAccess.preset.validation') }}</div>
        <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 4px;">
          <button class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="saving || !valid" @click="save">{{ saving ? t('common.processing') : t('common.save') }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.tool-preset-modal { max-width: 820px; }
.modal-heading { align-items: flex-start; margin-bottom: 16px; }
.tool-form-grid { gap: 12px; }
.tool-model-editor { display: flex; flex-direction: column; gap: 8px; margin-top: 8px; }
.tool-model-row { padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: color-mix(in srgb, var(--surface) 92%, var(--bg)); }
.tool-model-index { width: 24px; height: 24px; border-radius: 50%; background: var(--accent-soft); color: var(--accent); display: inline-flex; align-items: center; justify-content: center; font: 11px var(--font-mono); flex: 0 0 auto; margin: 5px 8px 0 0; }
.tool-model-main { flex: 1; min-width: 0; }
.tool-model-options { gap: 12px; margin-top: 7px; flex-wrap: wrap; }
.check-label { display: inline-flex; align-items: center; gap: 5px; color: var(--muted); font-size: 11.5px; cursor: pointer; }
.check-label input { accent-color: var(--accent); }
.tool-model-fields { gap: 8px; margin-top: 10px; }
.tool-model-fields > div { min-width: 0; }
.tool-model-fields .field-label { margin-bottom: 5px; }
.tool-variants { margin-top: 10px; padding-top: 9px; border-top: 1px solid var(--border); }
.variant-row { margin-top: 6px; }
.variant-row .input { min-width: 0; flex: 1; }
.tool-validation { padding: 8px 10px; border-radius: var(--radius-sm); background: rgba(217, 48, 37, 0.08); color: var(--negative); font-size: 12px; margin-bottom: 8px; }
@media (max-width: 680px) {
  .tool-model-fields { grid-template-columns: 1fr; }
  .tool-form-grid { grid-template-columns: 1fr; }
}
</style>
