<script setup lang="ts">
import { ref, watch, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { VueDraggable } from 'vue-draggable-plus'
import { api } from '../api/bridge'
import { useApi } from '../composables/useApi'
import { useRelativeTime } from '../composables/useRelativeTime'
import { useProviderStyle } from '../composables/useProviderStyle'
import { useToast } from '../composables/useToast'
import { useConfirm } from '../composables/useConfirm'
import DropdownMenu from '@/components/DropdownMenu.vue'
import ModelRuleTargetModal from '@/components/ModelRuleTargetModal.vue'
import { model } from '../../wailsjs/go/models'

const { t } = useI18n()
const { format } = useRelativeTime()
const { color: providerColor, initial: providerLetter, textColor: providerTextColor } = useProviderStyle()
const toast = useToast()
const confirm = useConfirm()

const {
  data: rules,
  loading: rulesLoading,
  error: rulesError,
  execute: fetchRules,
} = useApi(() => api.modelRules())

const {
  data: providers,
  execute: loadProviders,
} = useApi(() => api.providers())

const { data: settings, execute: loadSettings } = useApi(api.getSettings)

const modalOpen = ref(false)
const editingId = ref('')
const saving = ref(false)
const deleting = ref(false)

// Form state — only rule-level fields are edited in the modal. Targets are
// managed inline on the rule card.
const form = ref<{
  name: string
  enabled: boolean
  first_byte_timeout_seconds: number
  strategy: string
}>({
  name: '',
  enabled: true,
  first_byte_timeout_seconds: 0,
  strategy: 'priority_first',
})
const formTargets = ref<model.ModelRuleTarget[]>([])

// ---- Target inline management ----
const targetModalOpen = ref(false)
const targetModalRule = ref<model.ModelRule | null>(null)
const targetModalTarget = ref<model.ModelRuleTarget | null>(null)
const targetSaving = ref(false)
const diagnostics = ref<model.TargetShadowScore[]>([])
const diagnosticsLoading = ref(false)
const diagnosticsError = ref(false)

// sample_count is added by the backend lane. Keep this narrow compatibility
// type local until the generated models are regenerated; no fallback aliases or
// untyped diagnostic objects are used.
type TargetShadowScoreWithSampleCount = model.TargetShadowScore & { sample_count?: number }

// The view is driven by a single interaction state. Server responses are merged
// into this list by ID so nested target Sortable instances stay bound to the
// same arrays and rule objects survive refreshes.
const ruleList = ref<model.ModelRule[]>([])

watch(
  rules,
  (val) => {
    // A null result from useApi means the load failed; keep the current list so
    // a failed refresh doesn't wipe the interaction state.
    if (!val) return

    const existingMap = new Map(ruleList.value.map((r) => [r.id, r]))
    const next: model.ModelRule[] = []
    for (const source of val) {
      const existing = existingMap.get(source.id)
      if (existing) {
        syncRuleFromSource(existing, source)
        next.push(existing)
      } else {
        next.push(new model.ModelRule(source))
      }
    }

    // Mutate the list in place rather than replacing the array. VueDraggable
    // and its underlying SortableJS instances rely on the outer ruleList and
    // each rule's targets array remaining the same object across refreshes.
    ruleList.value.splice(0, ruleList.value.length, ...next)
  },
  { immediate: true }
)

const providerNameMap = computed(() => {
  const map: Record<string, string> = {}
  ;(providers.value || []).forEach((p) => (map[p.id] = p.name))
  return map
})

const providerEnabledMap = computed(() => {
  const map: Record<string, boolean> = {}
  ;(providers.value || []).forEach((p) => (map[p.id] = !!p.enabled))
  return map
})

function getTargetTimeout(target: model.ModelRuleTarget): number {
  return target.first_token_timeout_seconds ?? 0
}

function cloneTarget(target: model.ModelRuleTarget): model.ModelRuleTarget {
  const clone = new model.ModelRuleTarget({
    id: target.id,
    rule_id: target.rule_id,
    provider_id: target.provider_id,
    model_name: target.model_name,
    max_retries: target.max_retries,
    tier: target.tier,
    hit_count: target.hit_count,
    failure_count: target.failure_count,
    enabled: target.enabled,
  })
  clone.first_token_timeout_seconds = target.first_token_timeout_seconds ?? 0
  return clone
}

// Convert an output ModelRuleTarget to the input payload used by Create/Update.
// Read-only fields (rule_id, hit_count, failure_count) are dropped. Existing
// targets carry their tier explicitly (including 0); new targets without a tier
// leave it undefined so the store falls back to positional ordering.
function targetToInput(target: model.ModelRuleTarget): model.ModelRuleTargetInput {
  return new model.ModelRuleTargetInput({
    id: target.id || '',
    provider_id: target.provider_id,
    model_name: target.model_name,
    max_retries: target.max_retries,
    tier: typeof target.tier === 'number' ? target.tier : undefined,
    first_token_timeout_seconds: target.first_token_timeout_seconds ?? 0,
    enabled: target.enabled,
  })
}

function targetsToInput(targets: model.ModelRuleTarget[]): model.ModelRuleTargetInput[] {
  return targets.map(targetToInput)
}

function targetIconStyle(providerId: string) {
  const name = providerNameMap.value[providerId] || ''
  return {
    background: providerColor(name),
    color: providerTextColor(name),
  }
}

function targetProviderName(target: model.ModelRuleTarget): string {
  return providerNameMap.value[target.provider_id] || t('common.unknown')
}

function formatHits(n: number): string {
  return n.toLocaleString()
}

function formatSuccessRate(rule: model.ModelRule): string {
  if (rule.today_request_count === 0) {
    return '—'
  }
  const rate = rule.today_success_rate ?? 0
  return `${rate.toFixed(1)}%`
}

function diagnosticKey(ruleId: string, targetId: string): string {
  return `${ruleId}\u0000${targetId}`
}

const diagnosticsMap = computed(() => {
  const map = new Map<string, model.TargetShadowScore>()
  for (const item of diagnostics.value) {
    map.set(diagnosticKey(item.rule_id, item.target_id), item)
  }
  return map
})

function diagnosticFor(rule: model.ModelRule, target: model.ModelRuleTarget): model.TargetShadowScore | undefined {
  return diagnosticsMap.value.get(diagnosticKey(rule.id, target.id))
}

function diagnosticNumber(value: number | undefined, suffix = ''): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '—'
  return `${value % 1 === 0 ? value : value.toFixed(1)}${suffix}`
}

function diagnosticScore(value: number | undefined): string {
  return diagnosticNumber(value)
}

function diagnosticSampleCount(diagnostic?: model.TargetShadowScore): number | undefined {
  return (diagnostic as TargetShadowScoreWithSampleCount | undefined)?.sample_count
}

function diagnosticStatus(rule: model.ModelRule, target: model.ModelRuleTarget): string {
  if (diagnosticsLoading.value) return t('modelRules.diagnostics.loading')
  if (diagnosticsError.value) return t('modelRules.diagnostics.unavailable')
  const diagnostic = diagnosticFor(rule, target)
  if (!diagnostic || !diagnostic.metrics_fresh) return t('modelRules.diagnostics.noSamples')
  return ''
}

async function loadDiagnostics() {
  diagnosticsLoading.value = true
  diagnosticsError.value = false
  try {
    diagnostics.value = await api.getTargetDiagnostics()
  } catch {
    // Diagnostics are optional and must never block or replace rules.
    diagnostics.value = []
    diagnosticsError.value = true
  } finally {
    diagnosticsLoading.value = false
  }
}

async function loadRules() {
  const result = await fetchRules()
  void loadDiagnostics()
  return result
}

function openCreate() {
  editingId.value = ''
  form.value = {
    name: '',
    enabled: true,
    first_byte_timeout_seconds: 0,
    strategy: 'priority_first',
  }
  // New rules still need at least one default target; it is managed inline afterward.
  formTargets.value = [cloneTarget(new model.ModelRuleTarget({ provider_id: '', model_name: '', max_retries: 0, enabled: true }))]
  modalOpen.value = true
}

function openEdit(rule: model.ModelRule) {
  editingId.value = rule.id
  form.value = {
    name: rule.name,
    enabled: rule.enabled,
    first_byte_timeout_seconds: rule.first_byte_timeout_seconds || 0,
    strategy: rule.strategy || 'priority_first',
  }
  // Preserve existing targets even though they are edited outside the modal.
  formTargets.value = rule.targets.map(cloneTarget)
  modalOpen.value = true
}

async function saveRule() {
  saving.value = true
  try {
    const input = new model.ModelRuleInput({
      name: form.value.name,
      enabled: form.value.enabled,
      first_byte_timeout_seconds: form.value.first_byte_timeout_seconds,
      strategy: form.value.strategy || 'priority_first',
      targets: targetsToInput(formTargets.value),
    })
    if (editingId.value) {
      await api.updateModelRule(editingId.value, input)
    } else {
      await api.createModelRule(input)
    }
    modalOpen.value = false
    await loadRules()
    toast.push(t('toast.modelRuleSaved'), 'success')
  } catch (e: any) {
    toast.push(t('toast.saveFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
  } finally {
    saving.value = false
  }
}

async function toggleRule(rule: model.ModelRule) {
  try {
    const full = await api.getModelRule(rule.id)
    const input = new model.ModelRuleInput({
      name: full.name,
      enabled: !full.enabled,
      first_byte_timeout_seconds: full.first_byte_timeout_seconds,
      strategy: full.strategy || 'priority_first',
      targets: targetsToInput(full.targets),
    })
    await api.updateModelRule(rule.id, input)
    await loadRules()
    toast.push(full.enabled ? t('toast.modelRuleToggledDisabled') : t('toast.modelRuleToggledEnabled'), 'success')
  } catch (e: any) {
    toast.push(t('toast.toggleFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
  }
}

async function deleteRule(id: string, name: string) {
  const ok = await confirm.open({
    title: t('confirm.deleteModelRuleTitle'),
    message: t('confirm.deleteModelRuleMessage', { name }),
    confirmText: t('common.delete'),
    danger: true,
  })
  if (!ok) return
  deleting.value = true
  try {
    await api.deleteModelRule(id)
    await loadRules()
    toast.push(t('toast.modelRuleDeleted'), 'success')
  } catch (e: any) {
    toast.push(t('toast.deleteFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
  } finally {
    deleting.value = false
  }
}

// ---- Inline target management ----
function openAddTarget(rule: model.ModelRule) {
  targetModalRule.value = rule
  targetModalTarget.value = null
  targetModalOpen.value = true
}

function openEditTarget(rule: model.ModelRule, target: model.ModelRuleTarget) {
  targetModalRule.value = rule
  targetModalTarget.value = cloneTarget(target)
  targetModalOpen.value = true
}

function closeTargetModal() {
  targetModalOpen.value = false
  targetModalRule.value = null
  targetModalTarget.value = null
}

async function updateRuleTargets(rule: model.ModelRule, targets: model.ModelRuleTarget[]): Promise<boolean> {
  try {
    const input = new model.ModelRuleInput({
      name: rule.name,
      enabled: rule.enabled,
      first_byte_timeout_seconds: rule.first_byte_timeout_seconds,
      strategy: rule.strategy || 'priority_first',
      targets: targetsToInput(targets),
    })
    await api.updateModelRule(rule.id, input)
    await loadRules()
    toast.push(t('toast.targetsUpdated'), 'success')
    return true
  } catch (e: any) {
    toast.push(t('toast.targetsUpdateFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
    await loadRules()
    return false
  }
}

// Set of target IDs currently mid-toggle. Used to disable the toggle UI
// while a request is in flight so rapid clicks don't race the in-progress
// update (e.g. user mashes the toggle before the first PATCH lands, which
// could otherwise leave the server in the wrong state because the rule's
// targets list is read once at the start of `updateRuleTargets`).
const togglingTargets = ref<Set<string>>(new Set())

function isTogglingTarget(id: string): boolean {
  return togglingTargets.value.has(id)
}

async function toggleTarget(rule: model.ModelRule, target: model.ModelRuleTarget) {
  // Guard against re-entry on the same target. A new target (no id) is
  // treated as never-in-flight so the guard is a no-op for it.
  if (target.id && togglingTargets.value.has(target.id)) return
  if (target.id) {
    togglingTargets.value.add(target.id)
    // Reassign so Vue's reactivity tracks the Set mutation.
    togglingTargets.value = new Set(togglingTargets.value)
  }
  try {
    const newTargets = rule.targets.map((t) =>
      t.id === target.id
        ? cloneTarget({ ...t, enabled: !t.enabled })
        : t
    )
    await updateRuleTargets(rule, newTargets)
  } finally {
    if (target.id) {
      togglingTargets.value.delete(target.id)
      togglingTargets.value = new Set(togglingTargets.value)
    }
  }
}

async function deleteTarget(rule: model.ModelRule, target: model.ModelRuleTarget) {
  const targetLabel = `${targetProviderName(target)} · ${target.model_name || t('modelRules.targetDefault')}`
  const ok = await confirm.open({
    title: t('confirm.deleteTargetTitle'),
    message: t('confirm.deleteTargetMessage', { target: targetLabel }),
    confirmText: t('common.delete'),
    danger: true,
  })
  if (!ok) return
  const newTargets = rule.targets.filter((t) => t.id !== target.id)
  await updateRuleTargets(rule, newTargets)
}

// Per-rule target reorder state. We keep this separate from the rule-level
// drag reorder to avoid coupling their lifecycles.
const reorderStates = ref<Record<string, { inFlight: boolean; reconciling: boolean }>>({})

function getReorderState(ruleId: string) {
  if (!reorderStates.value[ruleId]) {
    reorderStates.value[ruleId] = {
      inFlight: false,
      reconciling: false,
    }
  }
  return reorderStates.value[ruleId]
}

function isSavingRule(ruleId: string): boolean {
  return getReorderState(ruleId).inFlight
}

function isReconcilingRule(ruleId: string): boolean {
  return getReorderState(ruleId).reconciling
}

interface ReorderResult {
  conflict: boolean
  error?: string
}

async function persistReorder(ruleId: string, targetIds: string[]): Promise<ReorderResult> {
  try {
    const result = await api.reorderRuleTargets(ruleId, targetIds)
    return { conflict: result.conflict }
  } catch (e: any) {
    return { conflict: false, error: e?.message || String(e) }
  }
}

async function onTargetsReorder(rule: model.ModelRule) {
  if (isSavingRule(rule.id) || isReconcilingRule(rule.id)) return

  const targetIds = rule.targets.map((t) => t.id).filter((id): id is string => !!id)
  if (targetIds.length < 2) return

  const state = getReorderState(rule.id)
  state.inFlight = true
  try {
    const result = await persistReorder(rule.id, targetIds)
    if (result.conflict) {
      toast.push(t('toast.reorderConflict'), 'error')
      await recoverTargetRule(rule)
    } else if (result.error) {
      toast.push(t('toast.reorderSaveFailed') + ': ' + result.error, 'error')
      await recoverTargetRule(rule)
    } else {
      // The drag has already mutated rule.targets in place; no full reload.
      toast.push(t('toast.reorderSaved'), 'success')
    }
  } catch (e: any) {
    toast.push(t('toast.reorderSaveFailed') + ': ' + (e?.message || String(e)), 'error')
    await recoverTargetRule(rule)
  } finally {
    state.inFlight = false
  }
}

async function recoverTargetRule(rule: model.ModelRule) {
  const state = getReorderState(rule.id)
  state.reconciling = true
  try {
    const refreshed = await api.getModelRule(rule.id)
    const existing = ruleList.value.find((r) => r.id === rule.id)
    if (!existing) {
      // Rule was deleted while we were retrying; leave it gone.
      return
    }
    // Refresh only editable fields and targets, preserving today's stats.
    syncRuleFields(existing, refreshed)
  } catch (e: any) {
    // Don't trust the error text to decide deletion. Reload the display list
    // and let the ID-based reconcile prove deletion by removing the rule.
    const reloaded = await loadRules()
    if (reloaded === null) {
      // Keep the rule in place so the user can retry; report the reload failure.
      toast.push(t('toast.reorderReloadFailed'), 'error')
    }
    // If reloaded succeeded, the watch has already reconciled ruleList and
    // removed the rule if it is missing from the authoritative list.
  } finally {
    state.reconciling = false
  }
}

// ---- Rule-level drag reorder ----
const rulesOrderSaving = ref(false)

async function onRulesReorder() {
  if (rulesOrderSaving.value) return

  const ids = ruleList.value.map((r) => r.id)
  rulesOrderSaving.value = true
  try {
    const result = await api.reorderModelRules(ids)
    if (result.conflict) {
      toast.push(t('toast.reorderConflict'), 'error')
      await reconcileRulesOrder()
    } else {
      toast.push(t('toast.reorderSaved'), 'success')
    }
  } catch (e: any) {
    toast.push(t('toast.reorderSaveFailed') + ': ' + (e?.message || String(e)), 'error')
    await reconcileRulesOrder()
  } finally {
    rulesOrderSaving.value = false
  }
}

async function reconcileRulesOrder(): Promise<boolean> {
  const reloaded = await loadRules()
  if (reloaded === null) {
    toast.push(t('toast.reorderReloadFailed'), 'error')
    return false
  }
  // The watch has already synced ruleList to the authoritative order.
  return true
}

async function onTargetModalSave(target: model.ModelRuleTarget) {
  const rule = targetModalRule.value
  if (!rule) return
  targetSaving.value = true
  try {
    const newTargets = target.id
      ? rule.targets.map((t) => (t.id === target.id ? target : t))
      : [...rule.targets, target]
    const ok = await updateRuleTargets(rule, newTargets)
    if (ok) closeTargetModal()
  } finally {
    targetSaving.value = false
  }
}

function importJSON() {
  const input = document.createElement('input')
  input.type = 'file'
  input.accept = '.json,application/json'
  input.onchange = async (e: Event) => {
    const file = (e.target as HTMLInputElement).files?.[0]
    if (!file) return
    try {
      const text = await file.text()
      const parsed = JSON.parse(text)
      const inputs: model.ModelRuleInput[] = []
      if (!Array.isArray(parsed)) {
        throw new Error(t('toast.invalidJson'))
      }
      for (const item of parsed) {
        if (!item || typeof item.name !== 'string') {
          throw new Error(t('toast.invalidItem'))
        }
        const targets: model.ModelRuleTarget[] = Array.isArray(item.targets) ? item.targets.map((raw: any) => new model.ModelRuleTarget({
          id: raw.id || '',
          rule_id: raw.rule_id || '',
          provider_id: raw.provider_id || '',
          model_name: raw.model_name || '',
          max_retries: typeof raw.max_retries === 'number' ? raw.max_retries : 0,
          tier: typeof raw.tier === 'number' ? raw.tier : undefined,
          enabled: raw.enabled !== false,
          hit_count: typeof raw.hit_count === 'number' ? raw.hit_count : 0,
          failure_count: typeof raw.failure_count === 'number' ? raw.failure_count : 0,
          first_token_timeout_seconds: typeof raw.first_token_timeout_seconds === 'number' ? raw.first_token_timeout_seconds : 0,
        })) : []
        inputs.push(new model.ModelRuleInput({
          name: item.name || '',
          enabled: item.enabled !== false,
          strategy: typeof item.strategy === 'string' ? item.strategy : 'priority_first',
          targets: targetsToInput(targets),
        }))
      }
      // Create in reverse so the backend's "newest at top" ordering preserves
      // the file's top-to-bottom order after refresh.
      for (let i = inputs.length - 1; i >= 0; i--) {
        await api.createModelRule(inputs[i])
      }
      await loadRules()
      toast.push(t('toast.imported', { count: inputs.length }), 'success')
    } catch (e: any) {
      toast.push(t('toast.importFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
    }
  }
  input.click()
}

function exportJSON() {
  const data = ruleList.value || []
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `Autoapi-model-rules-${new Date().toISOString().slice(0, 10)}.json`
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 0)
  toast.push(t('toast.exported'), 'success')
}

function closeModal() {
  modalOpen.value = false
}

onMounted(() => {
  void loadRules()
  loadProviders()
  loadSettings().catch((e: any) => toast.push(t('toast.loadSettingsFailed') + ': ' + (e?.message || e?.toString() || ''), 'error'))
})

// ---- Rule-list coordination helpers ----
function arraysEqual(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false
  }
  return true
}

function syncRuleFromSource(existing: model.ModelRule, source: model.ModelRule) {
  existing.name = source.name
  existing.enabled = source.enabled
  existing.first_byte_timeout_seconds = source.first_byte_timeout_seconds
  existing.strategy = source.strategy || 'priority_first'
  existing.created_at = source.created_at
  existing.updated_at = source.updated_at
  syncTargets(existing, source)
  existing.today_success_rate = source.today_success_rate
  existing.today_request_count = source.today_request_count
}

function syncRuleFields(existing: model.ModelRule, source: model.ModelRule) {
  // Editable rule fields and target order, leaving display stats untouched.
  existing.name = source.name
  existing.enabled = source.enabled
  existing.first_byte_timeout_seconds = source.first_byte_timeout_seconds
  existing.strategy = source.strategy || 'priority_first'
  syncTargets(existing, source)
}

function syncTargets(existing: model.ModelRule, source: model.ModelRule) {
  const existingIds = existing.targets.map((t) => t.id)
  const sourceIds = source.targets.map((t) => t.id)
  if (existingIds.length !== sourceIds.length || !arraysEqual(existingIds, sourceIds)) {
    // Preserve the targets array identity: SortableJS instances inside
    // VueDraggable hold references to the original array, so we replace
    // contents with splice rather than assignment.
    existing.targets.splice(0, existing.targets.length, ...source.targets.map(cloneTarget))
    return
  }
  for (let i = 0; i < existing.targets.length; i++) {
    const et = existing.targets[i]
    const st = source.targets[i]
    et.rule_id = st.rule_id
    et.provider_id = st.provider_id
    et.model_name = st.model_name
    et.max_retries = st.max_retries
    et.first_token_timeout_seconds = st.first_token_timeout_seconds
    et.tier = st.tier
    et.hit_count = st.hit_count
    et.failure_count = st.failure_count
    et.enabled = st.enabled
  }
}
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">{{ t('modelRules.title') }}</h1>
      <span class="main-subtitle">{{ t('modelRules.subtitle', { count: ruleList.length ?? 0 }) }}</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary" @click="importJSON">{{ t('modelRules.import') }}</button>
      <button class="btn btn-secondary" @click="exportJSON">{{ t('modelRules.export') }}</button>
      <button class="btn btn-primary" @click="openCreate">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
        {{ t('modelRules.new') }}
      </button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner stack-loose">
      <!-- Loading / error -->
      <div v-if="rulesLoading && !rules" class="text-muted" style="padding: 40px 0; text-align: center;">{{ t('modelRules.loading') }}</div>
      <div v-else-if="rulesError" class="text-muted" style="padding: 40px 0; text-align: center; color: var(--negative);">{{ t('modelRules.loadFailed', { error: rulesError }) }}</div>
      <template v-else>
        <!-- Rule list with simple drag reorder -->
        <VueDraggable
          v-model="ruleList"
          :animation="150"
          :disabled="rulesOrderSaving"
          handle=".rule-drag-handle"
          class="rule-list stack-loose"
          :class="{ 'rule-list-saving': rulesOrderSaving }"
          @end="onRulesReorder"
        >
          <article
            v-for="rule in ruleList"
            :key="rule.id"
            class="card rule-card"
            :class="{ 'rule-disabled': !rule.enabled }"
          >
            <header class="rule-header">
              <div class="rule-header-main">
                <span
                  class="rule-drag-handle"
                  :aria-label="t('modelRules.ruleDragHandle')"
                  :title="t('modelRules.ruleDragHandle')"
                  role="button"
                  tabindex="-1"
                >⋮⋮⋮</span>
                <div class="rule-title">
                  <div class="rule-name">{{ rule.name }}</div>
                  <div class="rule-subtitle">{{ t('modelRules.clientModelName') }}</div>
                </div>
              </div>
              <div class="rule-header-actions">
                <label class="toggle" :aria-label="rule.enabled ? t('modelRules.ruleToggleDisable') : t('modelRules.ruleToggleEnable')">
                  <input type="checkbox" :checked="rule.enabled" @change="toggleRule(rule)">
                  <span class="toggle-slider blue"></span>
                </label>
                <DropdownMenu :menu-id="rule.id">
                  <template #trigger="{ toggle, open }">
                    <button
                      class="btn btn-icon"
                      :aria-expanded="open"
                      aria-haspopup="menu"
                      :aria-label="t('modelRules.moreActions', { name: rule.name })"
                      @click="toggle"
                    >
                      <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>
                    </button>
                  </template>
                  <template #menu="{ close }">
                    <button class="dropdown-item" role="menuitem" @click="openEdit(rule); close()">{{ t('modelRules.edit') }}</button>
                    <button class="dropdown-item" role="menuitem" @click="toggleRule(rule); close()">{{ rule.enabled ? t('modelRules.disable') : t('modelRules.enable') }}</button>
                    <button class="dropdown-item danger" role="menuitem" :disabled="deleting" @click="deleteRule(rule.id, rule.name); close()">{{ t('modelRules.delete') }}</button>
                  </template>
                </DropdownMenu>
              </div>
            </header>

            <div class="rule-body">
              <div class="rule-targets">
                <h3 class="rule-section-label">
                  <span>{{ t('modelRules.targetsLabel') }}</span>
                  <span v-if="isSavingRule(rule.id)" class="reorder-saving">{{ t('modelRules.reorderSaving') }}</span>
                  <button
                    class="btn btn-icon"
                    style="width: 22px; height: 22px;"
                    :aria-label="t('modelRules.addTarget')"
                    @click="openAddTarget(rule)"
                  >
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
                  </button>
                </h3>
                <VueDraggable
                  v-model="rule.targets"
                  :animation="150"
                  :disabled="isSavingRule(rule.id) || isReconcilingRule(rule.id)"
                  handle=".drag-handle"
                  class="rule-target-list"
                  :class="{ 'rule-target-list-saving': isSavingRule(rule.id) }"
                  @end="onTargetsReorder(rule)"
                >
                  <li
                    v-for="(target, tidx) in rule.targets"
                    :key="target.id || tidx"
                    class="target-row"
                    :class="{ 'target-disabled': !target.enabled, 'target-provider-disabled': providerEnabledMap[target.provider_id] === false }"
                  >
                    <span
                      class="drag-handle"
                      :aria-label="t('modelRules.dragHandle')"
                      role="button"
                      tabindex="-1"
                    >⋮⋮</span>
                    <div class="list-icon target-icon" :style="targetIconStyle(target.provider_id)">
                      {{ providerLetter(targetProviderName(target)) }}
                    </div>
                    <div class="target-info">
                      <div class="target-primary-line">
                      <span class="target-provider">{{ targetProviderName(target) }}</span>
                      <span class="target-model">{{ target.model_name || t('modelRules.targetDefault') }}</span>
                      <span v-if="target.max_retries > 0" class="badge mono">{{ t('modelRules.targetRetries', { count: target.max_retries }) }}</span>
                      <span v-if="!target.enabled" class="badge" style="font-size: 10px; padding: 1px 6px;">{{ t('modelRules.targetDisabled') }}</span>
                      <span
                        v-else-if="providerEnabledMap[target.provider_id] === false"
                        class="badge badge-provider-disabled"
                        :title="t('modelRules.targets.providerDisabled')"
                      >{{ t('modelRules.targets.providerDisabled') }}</span>
                      </div>
                      <div class="target-diagnostics" :class="{ 'target-diagnostics-empty': !diagnosticFor(rule, target) || !diagnosticFor(rule, target)?.metrics_fresh }">
                        <template v-if="diagnosticFor(rule, target)?.metrics_fresh">
                          <span class="diag-score">{{ t('modelRules.diagnostics.score') }} {{ diagnosticScore(diagnosticFor(rule, target)?.overall) }}</span>
                          <span>{{ t('modelRules.diagnostics.reliability') }} {{ diagnosticScore(diagnosticFor(rule, target)?.reliability) }}</span>
                          <span>{{ t('modelRules.diagnostics.latencyScore') }} {{ diagnosticScore(diagnosticFor(rule, target)?.latency) }}</span>
                          <span>{{ t('modelRules.diagnostics.ttftScore') }} {{ diagnosticScore(diagnosticFor(rule, target)?.ttft) }}</span>
                          <span>{{ t('modelRules.diagnostics.capacity') }} {{ diagnosticScore(diagnosticFor(rule, target)?.capacity) }}</span>
                          <span>{{ t('modelRules.diagnostics.costEfficiency') }} {{ diagnosticScore(diagnosticFor(rule, target)?.cost_efficiency) }}</span>
                          <span>{{ t('modelRules.diagnostics.estimatedCost') }} {{ diagnosticNumber(diagnosticFor(rule, target)?.estimated_cost, ' USD') }}</span>
                          <span class="diag-samples">{{ t('modelRules.diagnostics.samples') }} {{ diagnosticNumber(diagnosticSampleCount(diagnosticFor(rule, target))) }} · {{ t('modelRules.diagnostics.confidence') }} {{ diagnosticScore(diagnosticFor(rule, target)?.confidence) }}</span>
                          <span v-if="diagnosticFor(rule, target)?.cost.available === false" class="diag-note">{{ t('modelRules.diagnostics.priceUnknown') }}</span>
                        </template>
                        <span v-else class="diag-note">{{ diagnosticStatus(rule, target) }}</span>
                      </div>
                    </div>
                    <div class="target-counters">
                      <span>{{ t('modelRules.targetHits', { count: formatHits(target.hit_count) }) }}</span>
                      <span :class="{ 'fail-hi': target.failure_count > 0 }">{{ t('modelRules.targetFailures', { count: formatHits(target.failure_count) }) }}</span>
                      <span>T{{ tidx + 1 }}</span>
                    </div>
                    <div class="target-actions">
                      <label class="toggle toggle-target" :aria-label="target.enabled ? t('modelRules.targetToggleDisable') : t('modelRules.targetToggleEnable')">
                        <input
                          type="checkbox"
                          :checked="target.enabled"
                          :disabled="isTogglingTarget(target.id || '')"
                          @change="toggleTarget(rule, target)"
                        >
                        <span class="toggle-slider"></span>
                      </label>
                      <DropdownMenu :menu-id="`${rule.id}-target-${target.id || tidx}`">
                        <template #trigger="{ toggle, open }">
                          <button
                            class="btn btn-icon"
                            style="width: 26px; height: 26px;"
                            :aria-expanded="open"
                            aria-haspopup="menu"
                            :aria-label="t('modelRules.targetActions')"
                            @click="toggle"
                          >
                            <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>
                          </button>
                        </template>
                        <template #menu="{ close }">
                          <button class="dropdown-item" role="menuitem" @click="openEditTarget(rule, target); close()">{{ t('modelRules.edit') }}</button>
                          <button class="dropdown-item danger" role="menuitem" @click="deleteTarget(rule, target); close()">{{ t('modelRules.delete') }}</button>
                        </template>
                      </DropdownMenu>
                    </div>
                  </li>
                  <li v-if="!rule.targets.length" class="text-muted" style="font-size: 12px;">{{ t('modelRules.empty') }}</li>
                </VueDraggable>
              </div>
            </div>

            <div class="rule-footer">
              <template v-if="rule.enabled">
                <div class="rule-stats">
                  <span class="rule-stats-item">
                    <span class="rule-stats-label">{{ t('modelRules.stats.todaySuccessRate') }}</span>
                    <span
                      class="rule-stats-value"
                      :class="{ 'rate-zero': rule.today_request_count > 0 && rule.today_success_rate === 0 }"
                      :title="rule.today_request_count === 0 ? t('modelRules.stats.todaySuccessRateNoData') : t('modelRules.stats.todaySuccessRateTooltip', { count: rule.today_request_count })"
                    >
                      {{ formatSuccessRate(rule) }}
                    </span>
                  </span>
                </div>
              </template>
              <div v-else class="rule-created">
                {{ t('modelRules.disabledCreated', { time: format(rule.created_at) }) }}
              </div>
            </div>
          </article>
        </VueDraggable>
      </template>
    </div>
  </div>

  <!-- Model rule modal (model name + enabled + targets) -->
  <Teleport to="body">
    <div v-if="modalOpen" class="modal-overlay" @click.self="closeModal">
      <div class="modal-card wide modal-card-scroll">
        <div class="modal-title">{{ editingId ? t('modelRules.modal.edit') : t('modelRules.modal.create') }}</div>
        <div class="field">
          <label class="field-label">{{ t('modelRules.modal.name') }}</label>
          <input v-model="form.name" class="input" :placeholder="t('modelRules.modal.namePlaceholder')">
          <div class="text-muted" style="font-size: 11px; margin-top: 4px;">{{ t('modelRules.modal.nameHelp') }}</div>
        </div>
        <div class="field">
          <label class="field-label">{{ t('modelRules.modal.strategy') }}</label>
          <select v-model="form.strategy" class="input" :disabled="saving">
            <option value="priority_first">{{ t('modelRules.modal.strategyPriority') }}</option>
            <option value="score_within_tier">{{ t('modelRules.modal.strategyScore') }}</option>
            <option value="cost_first">{{ t('modelRules.modal.strategyCost') }}</option>
          </select>
          <div class="text-muted" style="font-size: 11px; margin-top: 4px;">{{ t('modelRules.modal.strategyHelp') }}</div>
        </div>
        <div class="field">
          <label class="field-label">{{ t('modelRules.modal.timeout') }}</label>
          <input v-model.number="form.first_byte_timeout_seconds" type="number" class="input" min="0" step="1" :disabled="saving" :placeholder="t('modelRules.modal.timeoutPlaceholder')">
          <div class="text-muted" style="font-size: 11px; margin-top: 4px;">{{ t('modelRules.modal.timeoutHelp') }}</div>
        </div>
        <div class="field">
          <div class="row-between" style="margin-bottom: 0;">
            <label class="field-label">{{ t('modelRules.modal.enabled') }}</label>
            <label class="toggle">
              <input v-model="form.enabled" type="checkbox">
              <span class="toggle-slider"></span>
            </label>
          </div>
        </div>

        <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
          <button class="btn btn-secondary" @click="closeModal">{{ t('modelRules.modal.cancel') }}</button>
          <button class="btn btn-primary" :disabled="saving" @click="saveRule">{{ saving ? t('modelRules.modal.saving') : t('modelRules.modal.save') }}</button>
        </div>
      </div>
    </div>
  </Teleport>

  <!-- Target add/edit modal -->
  <ModelRuleTargetModal
    :open="targetModalOpen"
    :target="targetModalTarget"
    :providers="providers || []"
    :saving="targetSaving"
    @close="closeTargetModal"
    @save="onTargetModalSave"
  />
</template>

<style scoped>
.rule-list {
  display: flex;
  flex-direction: column;
}
.rule-list-saving {
  opacity: 0.7;
  transition: opacity 150ms ease;
}

.rule-card {
  --rule-border: rgba(0, 0, 0, 0.05);
  --rule-border-subtle: rgba(0, 0, 0, 0.04);
  --rule-bg-tint: rgba(0, 0, 0, 0.015);
  --rule-bg-footer: rgba(0, 0, 0, 0.01);
  --target-icon-size: 26px;
  --target-row-gap: 12px;
  padding: 0;
  overflow: hidden;
}
.rule-card.rule-disabled {
  opacity: 0.72;
}

.rule-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 16px 20px;
  border-bottom: 1px solid var(--rule-border);
}
.rule-header-main {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
  flex: 1;
}
.rule-header-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}

.rule-drag-handle {
  font-size: 14px;
  line-height: 1;
  padding: 6px 4px;
  color: var(--muted, #999);
  cursor: grab;
  user-select: none;
  -webkit-user-select: none;
  flex-shrink: 0;
  border-radius: 4px;
  transition: color 120ms ease, background-color 120ms ease;
  touch-action: none;
}
.rule-drag-handle:hover {
  color: var(--fg, #333);
  background-color: rgba(0, 0, 0, 0.04);
}
.rule-drag-handle:active,
.rule-drag-handle.sortable-chosen {
  cursor: grabbing;
}
html[data-theme="dark"] .rule-drag-handle:hover {
  background-color: rgba(255, 255, 255, 0.06);
}

.rule-title {
  min-width: 0;
}
.rule-name {
  font-family: var(--font-mono);
  font-size: 14px;
  font-weight: 600;
  line-height: 1.3;
}
.rule-subtitle {
  font-size: 12px;
  color: var(--muted);
  margin-top: 2px;
  line-height: 1.35;
}

.rule-body {
  display: block;
}
.rule-targets {
  padding: 16px 20px;
  min-width: 0;
}

.rule-section-label {
  font-size: 11px;
  font-weight: 600;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.06em;
  margin-bottom: 10px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.rule-target-list {
  display: flex;
  flex-direction: column;
  list-style: none;
}

.rule-target-list-saving {
  opacity: 0.7;
  transition: opacity 150ms ease;
}

.reorder-saving {
  font-size: 11px;
  color: var(--muted);
  font-weight: 400;
  text-transform: none;
  letter-spacing: 0;
}

.target-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 0;
  border-bottom: 1px solid var(--rule-border-subtle);
}
.target-row:last-child {
  border-bottom: none;
}
.target-row .target-icon {
  width: 26px;
  height: 26px;
  font-size: 11px;
  border-radius: 6px;
  flex-shrink: 0;
}
.target-info {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
  flex-wrap: wrap;
}
.target-primary-line {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  min-width: 0;
}
.target-diagnostics {
  display: flex;
  align-items: center;
  gap: 9px;
  flex-wrap: wrap;
  margin-top: 4px;
  color: var(--muted);
  font-family: var(--font-mono);
  font-size: 10px;
  line-height: 1.35;
  font-variant-numeric: tabular-nums;
  letter-spacing: -0.01em;
}
.target-diagnostics-empty { opacity: 0.72; }
.diag-score { color: var(--fg); font-weight: 600; }
.diag-samples { opacity: 0.8; }
.diag-note { color: var(--warning); font-family: inherit; }
.target-provider {
  font-size: 13px;
  font-weight: 500;
  color: var(--fg);
}
.target-model {
  font-family: var(--font-mono);
  font-size: 11.5px;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}
.target-counters {
  display: flex;
  align-items: center;
  gap: 10px;
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
  flex-shrink: 0;
  white-space: nowrap;
}
.target-counters .fail-hi {
  color: var(--negative);
  font-weight: 500;
}
.target-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.drag-handle {
  font-size: 14px;
  line-height: 1;
  padding: 4px 6px;
  color: var(--muted, #999);
  cursor: grab;
  user-select: none;
  -webkit-user-select: none;
  flex-shrink: 0;
  border-radius: 4px;
  transition: color 120ms ease, background-color 120ms ease;
  touch-action: none;
}
.drag-handle:hover {
  color: var(--fg, #333);
  background-color: rgba(0, 0, 0, 0.04);
}
.drag-handle:active,
.drag-handle.sortable-chosen {
  cursor: grabbing;
}
html[data-theme="dark"] .drag-handle:hover {
  background-color: rgba(255, 255, 255, 0.06);
}

.target-disabled .target-provider {
  color: var(--muted);
  text-decoration: line-through;
}
.target-disabled .target-model,
.target-disabled .target-counters {
  opacity: 0.7;
}
.target-disabled .target-icon {
  opacity: 0.5;
}

/* Target row whose upstream (provider) itself has been disabled. We keep
   the row readable — the mapping is still valid, it just can't be reached
   right now — and rely on the warning badge plus a soft dim to signal it. */
.target-provider-disabled {
  opacity: 0.62;
}
.target-provider-disabled .target-icon {
  opacity: 0.55;
  filter: grayscale(0.4);
}
.target-provider-disabled:hover {
  opacity: 0.85;
}

/* Warn-tinted badge used for "Provider disabled" so it visually differs
   from the muted-gray "Mapping disabled" badge above it. Amber conveys
   "something is wrong upstream" without being an error. */
.badge-provider-disabled {
  font-size: 10px;
  padding: 1px 6px;
  background: rgba(245, 166, 35, 0.14);
  color: var(--warning);
}
html[data-theme="dark"] .badge-provider-disabled {
  background: rgba(255, 159, 10, 0.18);
  color: #ffb340;
}

.rule-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 20px;
  border-top: 1px solid rgba(0, 0, 0, 0.05);
  background: var(--rule-bg-footer);
}
.rule-stats {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
}
.rule-stats-item {
  display: flex;
  align-items: baseline;
  gap: 6px;
}
.rule-stats-label {
  font-size: 11px;
  color: var(--muted);
}
.rule-stats-value {
  font-family: var(--font-mono);
  font-size: 15px;
  font-weight: 600;
  color: var(--fg);
  font-variant-numeric: tabular-nums;
}
.rule-stats-value.rate-zero {
  color: var(--negative);
}
.rule-created {
  font-size: 12px;
  color: var(--muted);
  font-family: var(--font-mono);
  margin-left: auto;
}

.toggle-target {
  width: 32px;
  height: 18px;
}
.toggle-target .toggle-slider::before {
  width: 14px;
  height: 14px;
  top: 2px;
  left: 2px;
}
.toggle-target input:checked + .toggle-slider::before {
  transform: translateX(14px);
}

@media (max-width: 800px) {
  .rule-footer {
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }
  .rule-created {
    margin-left: 0;
  }
  .target-row {
    flex-wrap: wrap;
    row-gap: 8px;
  }
  .target-info {
    flex: 1;
  }
  .target-diagnostics {
    flex-basis: 100%;
    padding-left: calc(var(--target-icon-size) + var(--target-row-gap));
  }
  .target-counters {
    order: 3;
    width: 100%;
    padding-left: calc(var(--target-icon-size) + var(--target-row-gap));
  }
  .target-actions {
    margin-left: auto;
  }
}

html[data-theme="dark"] .rule-card {
  --rule-border: rgba(255, 255, 255, 0.05);
  --rule-border-subtle: rgba(255, 255, 255, 0.05);
  --rule-bg-tint: rgba(255, 255, 255, 0.02);
  --rule-bg-footer: rgba(255, 255, 255, 0.01);
}
html[data-theme="dark"] .rule-header,
html[data-theme="dark"] .rule-targets,
html[data-theme="dark"] .rule-footer {
  border-color: var(--rule-border);
}
html[data-theme="dark"] .rule-footer {
  background: var(--rule-bg-footer);
}
html[data-theme="dark"] .target-row {
  border-bottom-color: var(--rule-border-subtle);
}
</style>
