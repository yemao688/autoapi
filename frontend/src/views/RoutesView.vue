<script setup lang="ts">
import { ref, watch, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { VueDraggable } from 'vue-draggable-plus'
import { api } from '../api/client'
import { useApi } from '../composables/useApi'
import { useRelativeTime } from '../composables/useRelativeTime'
import { useProviderStyle } from '../composables/useProviderStyle'
import { useFormatters } from '../composables/useFormatters'
import { useToast } from '../composables/useToast'
import { useConfirm } from '../composables/useConfirm'
import DropdownMenu from '@/components/DropdownMenu.vue'
import RouteTargetModal from '@/components/RouteTargetModal.vue'
import { model } from '../../wailsjs/go/models'

const { t } = useI18n()
const { format } = useRelativeTime()
const { color: providerColor, initial: providerLetter, textColor: providerTextColor } = useProviderStyle()
const { currency: fmtCurrency } = useFormatters()
const toast = useToast()
const confirm = useConfirm()

const {
  data: routes,
  loading: routesLoading,
  error: routesError,
  execute: loadRoutes,
} = useApi(() => api.routes())

const {
  data: providers,
  execute: loadProviders,
} = useApi(() => api.providers())

const { data: settings, execute: loadSettings } = useApi(api.getSettings)

const modalOpen = ref(false)
const editingId = ref('')
const saving = ref(false)
const deleting = ref(false)

// Form state — only route-level fields are edited in the modal now.
// Targets are managed inline on the rule card.
const form = ref<{
  name: string
  description: string
  enabled: boolean
  conditions: model.RouteCondition[]
}>({
  name: '',
  description: '',
  enabled: true,
  conditions: [],
})
const formTargets = ref<model.RouteTarget[]>([])

// ---- Target inline management ----
const targetModalOpen = ref(false)
const targetModalRoute = ref<model.Route | null>(null)
const targetModalTarget = ref<model.RouteTarget | null>(null)
const targetSaving = ref(false)

// ---- Rule list drag (live-persist on reorder) ----
// `vue-draggable-plus` mutates the bound array via splice, so a regular
// `ref` triggers Vue reactivity on every reorder. That fixes the old
// `useSortable` bug where `shallowRef` + DOM mutations left the array
// stale when `onEnd` read it back.
const ruleList = ref<model.Route[]>([])

watch(
  routes,
  (val) => {
    ruleList.value = val ? [...val] : []
  },
  { immediate: true }
)

async function persistRuleOrder() {
  try {
    await api.reorderRoutes(ruleList.value.map((r) => r.id))
    await loadRoutes()
  } catch (e: any) {
    toast.push(t('toast.reorderFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
    await loadRoutes() // revert to server truth
  }
}

const providerNameMap = computed(() => {
  const map: Record<string, string> = {}
  ;(providers.value || []).forEach((p) => (map[p.id] = p.name))
  return map
})

const defaultFallback = computed(() => ({
  provider: providerNameMap.value[settings.value?.routing?.default_provider_id || ''] || 'OpenAI',
  model: settings.value?.routing?.default_model || 'gpt-4o-mini',
  providerId: settings.value?.routing?.default_provider_id || '',
}))

const fallbackModalOpen = ref(false)
const fallbackProviderId = ref('')
const fallbackModel = ref('')

function operatorLabel(op: string): string {
  const key = `routes.operators.${op}`
  const translated = t(key)
  return translated === key ? op : translated
}

function targetIconStyle(providerId: string) {
  const name = providerNameMap.value[providerId] || ''
  return {
    background: providerColor(name),
    color: providerTextColor(name),
  }
}

function targetProviderName(target: model.RouteTarget): string {
  return providerNameMap.value[target.provider_id] || t('common.unknown')
}

function formatHits(n: number): string {
  return n.toLocaleString()
}

function addCondition() {
  form.value.conditions.push(new model.RouteCondition({ field: 'model', operator: 'matches', value: '' }))
}

function removeCondition(idx: number) {
  form.value.conditions.splice(idx, 1)
}

function openCreate() {
  editingId.value = ''
  form.value = {
    name: '',
    description: '',
    enabled: true,
    conditions: [new model.RouteCondition({ field: 'model', operator: 'matches', value: '' })],
  }
  // New routes still need at least one default target; it is managed inline afterward.
  formTargets.value = [new model.RouteTarget({ provider_id: '', model_name: '', max_retries: 0, enabled: true })]
  modalOpen.value = true
}

function openEdit(route: model.Route) {
  editingId.value = route.id
  form.value = {
    name: route.name,
    description: route.description,
    enabled: route.enabled,
    conditions: route.conditions.map((c) => new model.RouteCondition({ field: c.field, operator: c.operator, value: c.value })),
  }
  // Preserve existing targets even though they are edited outside the modal.
  formTargets.value = route.targets.map((t) => new model.RouteTarget({
    id: t.id,
    route_id: t.route_id,
    provider_id: t.provider_id,
    model_name: t.model_name,
    max_retries: t.max_retries,
    hit_count: t.hit_count,
    failure_count: t.failure_count,
    enabled: t.enabled,
  }))
  modalOpen.value = true
}

async function saveRoute() {
  saving.value = true
  try {
    const input = new model.RouteInput({
      name: form.value.name,
      description: form.value.description,
      enabled: form.value.enabled,
      conditions: form.value.conditions,
      targets: formTargets.value,
    })
    if (editingId.value) {
      await api.updateRoute(editingId.value, input)
    } else {
      await api.createRoute(input)
    }
    modalOpen.value = false
    await loadRoutes()
    toast.push(t('toast.routeSaved'), 'success')
  } catch (e: any) {
    toast.push(t('toast.saveFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
  } finally {
    saving.value = false
  }
}

async function toggleRoute(route: model.Route) {
  try {
    const full = await api.getRoute(route.id)
    const input = new model.RouteInput({
      name: full.name,
      description: full.description,
      enabled: !full.enabled,
      conditions: full.conditions,
      targets: full.targets,
    })
    await api.updateRoute(route.id, input)
    await loadRoutes()
    toast.push(full.enabled ? t('toast.routeToggledDisabled') : t('toast.routeToggledEnabled'), 'success')
  } catch (e: any) {
    toast.push(t('toast.toggleFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
  }
}

async function deleteRoute(id: string, name: string) {
  const ok = await confirm.open({
    title: t('confirm.deleteRouteTitle'),
    message: t('confirm.deleteRouteMessage', { name }),
    confirmText: t('common.delete'),
    danger: true,
  })
  if (!ok) return
  deleting.value = true
  try {
    await api.deleteRoute(id)
    await loadRoutes()
    toast.push(t('toast.routeDeleted'), 'success')
  } catch (e: any) {
    toast.push(t('toast.deleteFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
  } finally {
    deleting.value = false
  }
}

// ---- Inline target management ----
function openAddTarget(route: model.Route) {
  targetModalRoute.value = route
  targetModalTarget.value = null
  targetModalOpen.value = true
}

function openEditTarget(route: model.Route, target: model.RouteTarget) {
  targetModalRoute.value = route
  targetModalTarget.value = new model.RouteTarget({
    id: target.id,
    route_id: target.route_id,
    provider_id: target.provider_id,
    model_name: target.model_name,
    max_retries: target.max_retries,
    enabled: target.enabled,
    hit_count: target.hit_count,
    failure_count: target.failure_count,
  })
  targetModalOpen.value = true
}

function closeTargetModal() {
  targetModalOpen.value = false
  targetModalRoute.value = null
  targetModalTarget.value = null
}

async function updateRouteTargets(route: model.Route, targets: model.RouteTarget[]): Promise<boolean> {
  try {
    const input = new model.RouteInput({
      name: route.name,
      description: route.description,
      enabled: route.enabled,
      conditions: route.conditions,
      targets,
    })
    await api.updateRoute(route.id, input)
    await loadRoutes()
    toast.push(t('toast.targetsUpdated'), 'success')
    return true
  } catch (e: any) {
    toast.push(t('toast.targetsUpdateFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
    await loadRoutes()
    return false
  }
}

// Set of target IDs currently mid-toggle. Used to disable the toggle UI
// while a request is in flight so rapid clicks don't race the in-progress
// update (e.g. user mashes the toggle before the first PATCH lands, which
// could otherwise leave the server in the wrong state because the route's
// targets list is read once at the start of `updateRouteTargets`).
const togglingTargets = ref<Set<string>>(new Set())

function isTogglingTarget(id: string): boolean {
  return togglingTargets.value.has(id)
}

async function toggleTarget(route: model.Route, target: model.RouteTarget) {
  // Guard against re-entry on the same target. A new target (no id) is
  // treated as never-in-flight so the guard is a no-op for it.
  if (target.id && togglingTargets.value.has(target.id)) return
  if (target.id) {
    togglingTargets.value.add(target.id)
    // Reassign so Vue's reactivity tracks the Set mutation.
    togglingTargets.value = new Set(togglingTargets.value)
  }
  try {
    const newTargets = route.targets.map((t) =>
      t.id === target.id
        ? new model.RouteTarget({ ...t, enabled: !t.enabled })
        : t
    )
    await updateRouteTargets(route, newTargets)
  } finally {
    if (target.id) {
      togglingTargets.value.delete(target.id)
      togglingTargets.value = new Set(togglingTargets.value)
    }
  }
}

async function deleteTarget(route: model.Route, target: model.RouteTarget) {
  const targetLabel = `${targetProviderName(target)} · ${target.model_name || t('routes.targetDefault')}`
  const ok = await confirm.open({
    title: t('confirm.deleteTargetTitle'),
    message: t('confirm.deleteTargetMessage', { target: targetLabel }),
    confirmText: t('common.delete'),
    danger: true,
  })
  if (!ok) return
  const newTargets = route.targets.filter((t) => t.id !== target.id)
  await updateRouteTargets(route, newTargets)
}

async function onTargetModalSave(target: model.RouteTarget) {
  const route = targetModalRoute.value
  if (!route) return
  targetSaving.value = true
  try {
    const newTargets = target.id
      ? route.targets.map((t) => (t.id === target.id ? target : t))
      : [...route.targets, target]
    const ok = await updateRouteTargets(route, newTargets)
    if (ok) closeTargetModal()
  } finally {
    targetSaving.value = false
  }
}

function editDefault() {
  fallbackProviderId.value = settings.value?.routing?.default_provider_id || ''
  fallbackModel.value = settings.value?.routing?.default_model || ''
  fallbackModalOpen.value = true
}

async function saveDefaultFallback() {
  try {
    const s = settings.value ? new model.Settings(JSON.parse(JSON.stringify(settings.value))) : await api.getSettings()
    if (!s) {
      toast.push(t('toast.loadSettingsFailed'), 'error')
      return
    }
    s.routing.default_provider_id = fallbackProviderId.value
    s.routing.default_model = fallbackModel.value
    await api.saveSettings(s)
    await loadSettings()
    fallbackModalOpen.value = false
    toast.push(t('toast.defaultUpdated'), 'success')
  } catch (e: any) {
    toast.push(t('toast.saveFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
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
      const inputs: model.RouteInput[] = []
      if (!Array.isArray(parsed)) {
        throw new Error(t('toast.invalidJson'))
      }
      for (const item of parsed) {
        if (!item || typeof item.name !== 'string') {
          throw new Error(t('toast.invalidItem'))
        }
        const conditions = Array.isArray(item.conditions) ? item.conditions : []
        const targets = Array.isArray(item.targets) ? item.targets : []
        inputs.push(new model.RouteInput({
          name: item.name || '',
          description: item.description || '',
          enabled: item.enabled !== false,
          conditions: conditions.map((c: any) => new model.RouteCondition({
            field: c.field || 'model',
            operator: c.operator || 'matches',
            value: c.value || '',
          })),
          targets: targets.map((t: any) => new model.RouteTarget({
            provider_id: t.provider_id || '',
            model_name: t.model_name || '',
            max_retries: typeof t.max_retries === 'number' ? t.max_retries : 0,
            enabled: t.enabled !== false,
          })),
        }))
      }
      for (const input of inputs) {
        await api.createRoute(input)
      }
      await loadRoutes()
      toast.push(t('toast.imported', { count: inputs.length }), 'success')
    } catch (e: any) {
      toast.push(t('toast.importFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
    }
  }
  input.click()
}

function exportJSON() {
  const data = routes.value || []
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `Autoapi-routes-${new Date().toISOString().slice(0, 10)}.json`
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 0)
  toast.push(t('toast.exported'), 'success')
}

function closeModal() {
  modalOpen.value = false
}

function closeFallbackModal() {
  fallbackModalOpen.value = false
}

onMounted(() => {
  loadRoutes()
  loadProviders()
  loadSettings().catch((e: any) => toast.push(t('toast.loadSettingsFailed') + ': ' + (e?.message || e?.toString() || ''), 'error'))
})
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">{{ t('routes.title') }}</h1>
      <span class="main-subtitle">{{ t('routes.subtitle', { count: ruleList.length ?? 0 }) }}</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary" @click="importJSON">{{ t('routes.import') }}</button>
      <button class="btn btn-secondary" @click="exportJSON">{{ t('routes.export') }}</button>
      <button class="btn btn-primary" @click="openCreate">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
        {{ t('routes.new') }}
      </button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner stack-loose">
      <!-- Loading / error -->
      <div v-if="routesLoading && !routes" class="text-muted" style="padding: 40px 0; text-align: center;">{{ t('routes.loading') }}</div>
      <div v-else-if="routesError" class="text-muted" style="padding: 40px 0; text-align: center; color: var(--negative);">{{ t('routes.loadFailed', { error: routesError }) }}</div>
      <template v-else>
        <!-- Default fallback banner -->
        <div class="card fallback-banner">
          <div class="fallback-icon">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14M12 5l7 7-7 7"/></svg>
          </div>
          <div class="fallback-body">
            <div class="fallback-title">{{ t('routes.fallbackTitle') }}</div>
            <div class="fallback-desc">
              {{ t('routes.fallbackDesc', { target: `${defaultFallback.provider} · ${defaultFallback.model}` }) }}
            </div>
          </div>
          <button class="btn btn-secondary" @click="editDefault">{{ t('routes.fallbackEdit') }}</button>
        </div>

        <!-- Rule list (sortable container) -->
        <VueDraggable
          v-model="ruleList"
          handle=".drag-handle"
          :animation="150"
          ghost-class="sortable-ghost"
          chosen-class="sortable-chosen"
          drag-class="sortable-drag"
          class="stack-loose"
          @end="persistRuleOrder"
        >
          <article
            v-for="(route, idx) in ruleList"
            :key="route.id"
            class="card route-card"
            :class="{ 'route-disabled': !route.enabled }"
          >
            <header class="route-header">
              <div class="route-header-main">
                <svg class="drag-handle" viewBox="0 0 16 28" fill="currentColor" width="14" height="24" :aria-label="t('common.drag')">
                  <circle cx="5" cy="5.5" r="1.4"/>
                  <circle cx="11" cy="5.5" r="1.4"/>
                  <circle cx="5" cy="14" r="1.5"/>
                  <circle cx="11" cy="14" r="1.5"/>
                  <circle cx="5" cy="22.5" r="1.5"/>
                  <circle cx="11" cy="22.5" r="1.5"/>
                </svg>
                <div class="route-number text-mono">{{ String(idx + 1).padStart(2, '0') }}</div>
                <div class="route-title">
                  <div class="route-name">{{ route.name }}</div>
                  <div class="route-desc">{{ route.description }}</div>
                </div>
              </div>
              <div class="route-header-actions">
                <label class="toggle" :aria-label="route.enabled ? t('routes.ruleToggleDisable') : t('routes.ruleToggleEnable')">
                  <input type="checkbox" :checked="route.enabled" @change="toggleRoute(route)">
                  <span class="toggle-slider blue"></span>
                </label>
                <DropdownMenu :menu-id="route.id">
                  <template #trigger="{ toggle, open }">
                    <button
                      class="btn btn-icon"
                      :aria-expanded="open"
                      aria-haspopup="menu"
                      :aria-label="t('routes.moreActions', { name: route.name })"
                      @click="toggle"
                    >
                      <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>
                    </button>
                  </template>
                  <template #menu="{ close }">
                    <button class="dropdown-item" role="menuitem" @click="openEdit(route); close()">{{ t('routes.edit') }}</button>
                    <button class="dropdown-item" role="menuitem" @click="toggleRoute(route); close()">{{ route.enabled ? t('routes.disable') : t('routes.enable') }}</button>
                    <button class="dropdown-item danger" role="menuitem" :disabled="deleting" @click="deleteRoute(route.id, route.name); close()">{{ t('routes.delete') }}</button>
                  </template>
                </DropdownMenu>
              </div>
            </header>

            <div class="route-body">
              <div class="route-conditions">
                <h3 class="route-section-label">{{ t('routes.conditionsLabel') }}</h3>
                <ul class="route-condition-list">
                  <li v-for="(c, cidx) in route.conditions" :key="cidx" class="route-condition">
                    <span class="route-condition-field">{{ c.field }}</span>
                    <span class="route-condition-op">{{ operatorLabel(c.operator) }}</span>
                    <span class="route-condition-value">{{ c.value }}</span>
                  </li>
                  <li v-if="!route.conditions.length" class="text-muted" style="font-size: 12px;">{{ t('routes.empty') }}</li>
                </ul>
              </div>
              <div class="route-targets">
                <h3 class="route-section-label">
                  <span>{{ t('routes.targetsLabel') }}</span>
                  <button
                    class="btn btn-icon"
                    style="width: 22px; height: 22px;"
                    :aria-label="t('routes.addTarget')"
                    @click="openAddTarget(route)"
                  >
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
                  </button>
                </h3>
                <ul class="route-target-list">
                  <li
                    v-for="(target, tidx) in route.targets"
                    :key="target.id || tidx"
                    class="target-row"
                    :class="{ 'target-disabled': !target.enabled }"
                  >
                    <div class="list-icon target-icon" :style="targetIconStyle(target.provider_id)">
                      {{ providerLetter(targetProviderName(target)) }}
                    </div>
                    <div class="target-info">
                      <span class="target-provider">{{ targetProviderName(target) }}</span>
                      <span class="target-model">{{ target.model_name || t('routes.targetDefault') }}</span>
                      <span v-if="target.max_retries > 0" class="badge mono">{{ t('routes.targetRetries', { count: target.max_retries }) }}</span>
                      <span v-if="!target.enabled" class="badge" style="font-size: 10px; padding: 1px 6px;">{{ t('routes.targetDisabled') }}</span>
                    </div>
                    <div class="target-counters">
                      <span>{{ t('routes.targetHits', { count: formatHits(target.hit_count) }) }}</span>
                      <span :class="{ 'fail-hi': target.failure_count > 0 }">{{ t('routes.targetFailures', { count: formatHits(target.failure_count) }) }}</span>
                      <span>T{{ tidx + 1 }}</span>
                    </div>
                    <div class="target-actions">
                      <label class="toggle toggle-target" :aria-label="target.enabled ? t('routes.targetToggleDisable') : t('routes.targetToggleEnable')">
                        <input
                          type="checkbox"
                          :checked="target.enabled"
                          :disabled="isTogglingTarget(target.id || '')"
                          @change="toggleTarget(route, target)"
                        >
                        <span class="toggle-slider"></span>
                      </label>
                      <DropdownMenu :menu-id="`${route.id}-target-${target.id || tidx}`">
                        <template #trigger="{ toggle, open }">
                          <button
                            class="btn btn-icon"
                            style="width: 26px; height: 26px;"
                            :aria-expanded="open"
                            aria-haspopup="menu"
                            :aria-label="t('routes.targetActions')"
                            @click="toggle"
                          >
                            <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>
                          </button>
                        </template>
                        <template #menu="{ close }">
                          <button class="dropdown-item" role="menuitem" @click="openEditTarget(route, target); close()">{{ t('routes.edit') }}</button>
                          <button class="dropdown-item danger" role="menuitem" @click="deleteTarget(route, target); close()">{{ t('routes.delete') }}</button>
                        </template>
                      </DropdownMenu>
                    </div>
                  </li>
                  <li v-if="!route.targets.length" class="text-muted" style="font-size: 12px;">{{ t('routes.empty') }}</li>
                </ul>
              </div>
            </div>

            <div class="route-footer">
              <template v-if="route.enabled">
                <div class="route-stats">
                  <span class="route-stats-item">
                    <span class="route-stats-label">{{ t('routes.stats.monthlyHits') }}</span>
                    <span class="route-stats-value">{{ formatHits(route.monthly_hits) }}</span>
                  </span>
                  <span v-if="route.monthly_savings > 0" class="route-stats-item">
                    <span class="route-stats-label">{{ t('routes.stats.savings') }}</span>
                    <span class="route-stats-value">{{ fmtCurrency(route.monthly_savings) }}</span>
                  </span>
                  <span v-else class="route-stats-item">
                    <span class="route-stats-label">{{ t('routes.stats.share') }}</span>
                    <span class="route-stats-value">{{ route.monthly_hits ? '1.7%' : '0%' }}</span>
                  </span>
                </div>
              </template>
              <div v-else class="route-created">
                {{ t('routes.disabledStatus') }} · {{ t('routes.disabledCreated', { time: format(route.created_at) }) }}
              </div>
            </div>
          </article>
        </VueDraggable>
      </template>
    </div>
  </div>

  <!-- Route modal -->
  <Teleport to="body">
    <div v-if="modalOpen" class="modal-overlay" @click.self="closeModal">
      <div class="modal-card wide modal-card-scroll">
        <div class="modal-title">{{ editingId ? t('routes.modal.edit') : t('routes.modal.create') }}</div>
        <div class="field">
          <label class="field-label">{{ t('routes.modal.name') }}</label>
          <input v-model="form.name" class="input" :placeholder="t('routes.modal.namePlaceholder')">
        </div>
        <div class="field">
          <label class="field-label">{{ t('routes.modal.description') }}</label>
          <input v-model="form.description" class="input" :placeholder="t('routes.modal.descriptionPlaceholder')">
        </div>
        <div class="field">
          <div class="row-between" style="margin-bottom: 0;">
            <label class="field-label">{{ t('routes.modal.enabled') }}</label>
            <label class="toggle">
              <input v-model="form.enabled" type="checkbox">
              <span class="toggle-slider"></span>
            </label>
          </div>
        </div>

        <div class="field" style="margin-bottom: 8px;">
          <div class="field-label">{{ t('routes.modal.conditions') }}</div>
        </div>
        <div class="stack-tight" style="gap: 8px;">
          <div v-for="(cond, idx) in form.conditions" :key="idx" class="row" style="gap: 8px; align-items: flex-start;">
            <select v-model="cond.field" class="select" style="flex: 1;">
              <option value="model">model</option>
              <option value="header.x-priority">header.x-priority</option>
              <option value="estimated_tokens">estimated_tokens</option>
              <option value="task">task</option>
              <option value="time.hour">time.hour</option>
            </select>
            <select v-model="cond.operator" class="select" style="width: 120px;">
              <option value="matches">matches</option>
              <option value="equals">equals</option>
              <option value="lt">lt</option>
              <option value="gt">gt</option>
              <option value="between">between</option>
              <option value="in">in</option>
            </select>
            <input v-model="cond.value" class="input" style="flex: 1;" :placeholder="t('routes.modal.valuePlaceholder')">
            <button class="btn btn-icon" style="width: 28px; height: 28px;" @click="removeCondition(idx)">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M18 6L6 18M6 6l12 12"/></svg>
            </button>
          </div>
          <button class="btn btn-secondary" style="align-self: flex-start;" @click="addCondition">{{ t('routes.modal.addCondition') }}</button>
        </div>

        <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
          <button class="btn btn-secondary" @click="closeModal">{{ t('routes.modal.cancel') }}</button>
          <button class="btn btn-primary" :disabled="saving" @click="saveRoute">{{ saving ? t('routes.modal.saving') : t('routes.modal.save') }}</button>
        </div>
      </div>
    </div>
  </Teleport>

  <!-- Target add/edit modal -->
  <RouteTargetModal
    :open="targetModalOpen"
    :target="targetModalTarget"
    :providers="providers || []"
    :saving="targetSaving"
    @close="closeTargetModal"
    @save="onTargetModalSave"
  />

  <!-- Default fallback modal -->
  <Teleport to="body">
    <div v-if="fallbackModalOpen" class="modal-overlay" @click.self="closeFallbackModal">
      <div class="modal-card">
        <div class="modal-title">{{ t('routes.editFallbackTitle') }}</div>
        <div class="field">
          <label class="field-label">{{ t('routes.fallback.provider') }}</label>
          <select v-model="fallbackProviderId" class="select">
            <option v-for="p in providers || []" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
        </div>
        <div class="field">
          <label class="field-label">{{ t('routes.fallback.model') }}</label>
          <input v-model="fallbackModel" class="input" :placeholder="t('routes.fallback.modelPlaceholder')">
        </div>
        <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
          <button class="btn btn-secondary" @click="closeFallbackModal">{{ t('routes.modal.cancel') }}</button>
          <button class="btn btn-primary" @click="saveDefaultFallback">{{ t('routes.modal.save') }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.route-card {
  --route-border: rgba(0, 0, 0, 0.05);
  --route-border-subtle: rgba(0, 0, 0, 0.04);
  --route-bg-tint: rgba(0, 0, 0, 0.015);
  --route-bg-footer: rgba(0, 0, 0, 0.01);
  --target-icon-size: 26px;
  --target-row-gap: 12px;
  padding: 0;
  overflow: hidden;
}
.route-card.route-disabled {
  opacity: 0.72;
}

.route-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 16px 20px;
  border-bottom: 1px solid var(--route-border);
}
.route-header-main {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
  flex: 1;
}
.route-header-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}

.route-number {
  font-size: 12px;
  color: var(--muted);
  width: 24px;
  text-align: right;
  flex-shrink: 0;
}
.route-title {
  min-width: 0;
}
.route-name {
  font-size: 14px;
  font-weight: 600;
  line-height: 1.3;
}
.route-desc {
  font-size: 12px;
  color: var(--muted);
  margin-top: 2px;
  line-height: 1.35;
}

.route-body {
  display: grid;
  grid-template-columns: 35% 65%;
}
.route-conditions {
  padding: 16px 20px;
  border-right: 1px solid var(--route-border);
  background: var(--route-bg-tint);
  min-width: 0;
}
.route-targets {
  padding: 16px 20px;
  min-width: 0;
}

.route-section-label {
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

.route-condition-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  list-style: none;
}
.route-condition {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.route-condition-field {
  font-family: var(--font-mono);
  font-size: 11.5px;
  color: var(--fg);
}
.route-condition-op {
  font-size: 10px;
  font-weight: 600;
  padding: 2px 7px;
  border-radius: 100px;
  background: var(--accent-soft);
  color: var(--accent);
  font-family: var(--font-mono);
}
.route-condition-value {
  font-family: var(--font-mono);
  font-size: 11.5px;
  color: var(--fg);
}

.route-target-list {
  display: flex;
  flex-direction: column;
  list-style: none;
}

.target-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 0;
  border-bottom: 1px solid var(--route-border-subtle);
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

.route-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 20px;
  border-top: 1px solid rgba(0, 0, 0, 0.05);
  background: var(--route-bg-footer);
}
.route-stats {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
}
.route-stats-item {
  display: flex;
  align-items: baseline;
  gap: 6px;
}
.route-stats-label {
  font-size: 11px;
  color: var(--muted);
}
.route-stats-value {
  font-family: var(--font-mono);
  font-size: 15px;
  font-weight: 600;
  color: var(--fg);
  font-variant-numeric: tabular-nums;
}
.route-created {
  font-size: 12px;
  color: var(--muted);
  font-family: var(--font-mono);
  margin-left: auto;
}

.fallback-banner {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 14px 18px;
  background: var(--accent-soft);
  border: 1px solid rgba(0, 113, 227, 0.18);
  color: var(--fg);
}
.fallback-icon {
  width: 34px;
  height: 34px;
  border-radius: 9px;
  background: var(--accent);
  color: white;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.fallback-icon svg {
  width: 16px;
  height: 16px;
}
.fallback-body {
  flex: 1;
  min-width: 0;
}
.fallback-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--fg);
}
.fallback-desc {
  font-size: 12px;
  color: var(--muted);
  margin-top: 2px;
  line-height: 1.35;
}
.fallback-model {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--accent);
  font-weight: 500;
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
  .route-body {
    grid-template-columns: 1fr;
  }
  .route-conditions {
    border-right: none;
    border-bottom: 1px solid var(--route-border);
  }
  .route-footer {
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }
  .route-created {
    margin-left: 0;
  }
  .target-row {
    flex-wrap: wrap;
    row-gap: 8px;
  }
  .target-info {
    flex: 1;
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

html[data-theme="dark"] .route-card {
  --route-border: rgba(255, 255, 255, 0.05);
  --route-border-subtle: rgba(255, 255, 255, 0.05);
  --route-bg-tint: rgba(255, 255, 255, 0.02);
  --route-bg-footer: rgba(255, 255, 255, 0.01);
}
html[data-theme="dark"] .route-header,
html[data-theme="dark"] .route-conditions,
html[data-theme="dark"] .route-footer {
  border-color: var(--route-border);
}
html[data-theme="dark"] .route-conditions {
  background: var(--route-bg-tint);
}
html[data-theme="dark"] .route-footer {
  background: var(--route-bg-footer);
}
html[data-theme="dark"] .target-row {
  border-bottom-color: var(--route-border-subtle);
}
html[data-theme="dark"] .fallback-banner {
  background: var(--accent-soft);
  border-color: rgba(10, 132, 255, 0.25);
}
</style>
