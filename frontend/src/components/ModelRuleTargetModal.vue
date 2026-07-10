<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { model } from '../../wailsjs/go/models'
import { api } from '@/api/client'
import { useToast } from '@/composables/useToast'

const { t } = useI18n()
const toast = useToast()

const props = defineProps<{
  open: boolean
  target: model.ModelRuleTarget | null
  providers: model.Provider[]
  saving?: boolean
}>()

const emit = defineEmits<{
  (e: 'save', target: model.ModelRuleTarget): void
  (e: 'close'): void
}>()

const form = ref<model.ModelRuleTarget>(new model.ModelRuleTarget({
  provider_id: '',
  model_name: '',
  max_retries: 0,
  enabled: true,
}))

// Available models for the selected upstream, loaded from local DB
const availableModels = ref<string[]>([])
const loadingModels = ref(false)
const testing = ref(false)

// Track the provider ID we're loading models for, to guard against stale responses
let loadingForProvider = ''

function reset() {
  form.value = new model.ModelRuleTarget({
    provider_id: '',
    model_name: '',
    max_retries: 0,
    enabled: true,
  })
  availableModels.value = []
  loadingModels.value = false
}

watch(() => props.target, (t) => {
  if (t) {
    form.value = new model.ModelRuleTarget({
      id: t.id,
      rule_id: t.rule_id,
      provider_id: t.provider_id,
      model_name: t.model_name,
      max_retries: t.max_retries,
      enabled: t.enabled,
      hit_count: t.hit_count,
      failure_count: t.failure_count,
    })
    // If editing an existing target, load the models for its provider
    if (t.provider_id) {
      void loadAvailableModels(t.provider_id)
    }
  } else {
    reset()
  }
}, { immediate: true })

// When the user changes the upstream, load its available models
// and clear the model name (unless it was set by the watcher above)
watch(() => form.value.provider_id, (newId, oldId) => {
  if (newId === oldId) return
  if (newId) {
    void loadAvailableModels(newId)
  } else {
    availableModels.value = []
  }
  // Clear model name when upstream changes (user-initiated, not initial load)
  if (oldId !== undefined) {
    form.value.model_name = ''
  }
})

async function loadAvailableModels(providerId: string) {
  loadingForProvider = providerId
  loadingModels.value = true
  try {
    const models = await api.listModels(providerId)
    // Guard against stale response after rapid upstream change
    if (loadingForProvider !== providerId) return
    availableModels.value = models.map((m) => m.name).sort()
  } catch {
    if (loadingForProvider !== providerId) return
    availableModels.value = []
  } finally {
    if (loadingForProvider === providerId) {
      loadingModels.value = false
    }
  }
}

const isEdit = computed(() => !!props.target)

// Validation: a target must have a provider and a non-empty (post-trim) model
// name. We trim here so the computed is the single source of truth for both
// the inline message and the Save button's disabled state.
const trimmedModelName = computed(() => (form.value.model_name || '').trim())
const isValid = computed(
  () => !!form.value.provider_id && trimmedModelName.value.length > 0
)
const showValidation = computed(
  // Only surface the inline message after the user has interacted (touched
  // either field); avoids yelling at them the moment the modal opens with
  // a freshly-reset form.
  () => !isValid.value && (form.value.provider_id !== '' || (form.value.model_name || '').length > 0)
)

function close() {
  emit('close')
}

function save() {
  if (!isValid.value) return
  // Trim model_name before emitting so the persisted value never carries
  // accidental leading/trailing whitespace. provider_id comes from a <select>
  // and is already canonical.
  emit('save', new model.ModelRuleTarget({
    id: form.value.id,
    rule_id: form.value.rule_id,
    provider_id: form.value.provider_id,
    model_name: trimmedModelName.value,
    max_retries: form.value.max_retries,
    // Preserve the enabled value: default true for new targets,
    // existing value for edits. The toggle is removed from the template
    // but the field stays in the form state.
    enabled: form.value.enabled,
    hit_count: form.value.hit_count,
    failure_count: form.value.failure_count,
  }))
}

async function testModel() {
  if (!form.value.provider_id || !trimmedModelName.value) return
  testing.value = true
  try {
    const result = await api.testModelLatency(form.value.provider_id, trimmedModelName.value)
    if (result.ok) {
      toast.push(t('modelRules.targets.testSuccess', { ms: result.latency_ms }), 'success')
    } else {
      toast.push(result.error || t('modelRules.targets.testFailed'), 'error')
    }
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-overlay" @click.self="close">
      <div class="modal-card">
        <div class="modal-title">{{ isEdit ? t('modelRules.targets.edit') : t('modelRules.targets.add') }}</div>
        <div class="field">
          <label class="field-label">{{ t('modelRules.targets.provider') }}</label>
          <select v-model="form.provider_id" class="select" :disabled="saving">
            <option value="" disabled>{{ t('modelRules.targets.providerPlaceholder') }}</option>
            <option v-for="p in providers" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
        </div>
        <div class="field">
          <label class="field-label">{{ t('modelRules.targets.model') }}</label>
          <input
            v-model="form.model_name"
            class="input"
            :placeholder="loadingModels ? t('modelRules.targets.loadingModels') : t('modelRules.targets.modelPlaceholder')"
            :disabled="saving || !form.provider_id"
            list="available-models-list"
          >
          <datalist id="available-models-list">
            <option v-for="name in availableModels" :key="name" :value="name" />
          </datalist>
          <div v-if="form.provider_id && availableModels.length" class="field-help">
            {{ t('modelRules.targets.modelHelp', { count: availableModels.length }) }}
          </div>
        </div>
        <div class="field">
          <label class="field-label">{{ t('modelRules.targets.maxRetries') }}</label>
          <input v-model.number="form.max_retries" type="number" class="input" min="0" step="1" :disabled="saving">
        </div>
        <div v-if="showValidation" class="text-muted" style="font-size: 12px; color: var(--negative); margin-top: -4px;">
          {{ t('modelRules.targets.validation') }}
        </div>
        <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
          <button class="btn btn-secondary" :disabled="saving" @click="close">{{ t('modelRules.targets.cancel') }}</button>
          <button
            class="btn btn-secondary"
            :disabled="saving || testing || !isValid"
            :title="!isValid ? t('modelRules.targets.validation') : ''"
            @click="testModel"
          >
            {{ testing ? t('modelRules.targets.testing') : t('modelRules.targets.test') }}
          </button>
          <button class="btn btn-primary" :disabled="saving || !isValid" @click="save">{{ saving ? t('modelRules.targets.saving') : t('modelRules.targets.save') }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
