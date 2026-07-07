<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { api } from '../api/client'
import { useApi } from '../composables/useApi'
import { useRelativeTime } from '../composables/useRelativeTime'
import { useProviderMeta } from '../composables/useProviderMeta'
import { useFormatters } from '../composables/useFormatters'
import { useToast } from '../composables/useToast'
import { model } from '../../wailsjs/go/models'

const { format } = useRelativeTime()
const { color: providerColor, letter: providerLetter } = useProviderMeta()
const { currency: fmtCurrency } = useFormatters()
const toast = useToast()

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

const modalOpen = ref(false)
const editingId = ref('')
const saving = ref(false)
const openMenuId = ref('')

const form = ref<{
  name: string
  description: string
  priority: number
  enabled: boolean
  conditions: model.RouteCondition[]
  targets: model.RouteTarget[]
}>({
  name: '',
  description: '',
  priority: 10,
  enabled: true,
  conditions: [],
  targets: [],
})

const sortedRoutes = computed(() => {
  const list = routes.value || []
  return [...list].sort((a, b) => a.priority - b.priority)
})

const providerNameMap = computed(() => {
  const map: Record<string, string> = {}
  ;(providers.value || []).forEach((p) => (map[p.id] = p.name))
  return map
})

const defaultFallback = computed(() => {
  // Fallback used for the banner; no backend default-route endpoint yet, so we keep the prototype text.
  return { provider: 'OpenAI', model: 'gpt-4o-mini' }
})

function operatorLabel(op: string): string {
  const map: Record<string, string> = {
    matches: 'matches',
    equals: 'equals',
    lt: 'lt',
    gt: 'gt',
    between: 'between',
    in: 'in',
  }
  return map[op] || op
}

function targetIconStyle(providerId: string) {
  const name = providerNameMap.value[providerId] || ''
  return {
    background: providerColor(name),
    color: 'white',
  }
}

function targetProviderName(target: model.RouteTarget): string {
  return providerNameMap.value[target.provider_id] || '未知'
}

function targetActionLabel(target: model.RouteTarget): string {
  if (target.action === 'skip') return `跳过 Provider: ${targetProviderName(target)}`
  return `路由到 ${targetProviderName(target)} · ${target.model_name || '默认'}`
}

function tierLabel(n: number): string {
  return `P${n}`
}

function priorityBadgeClass(route: model.Route): string {
  return sortedRoutes.value[0]?.id === route.id ? 'info' : ''
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

function addTarget() {
  form.value.targets.push(new model.RouteTarget({ provider_id: '', model_name: '', action: 'forward', tier: 0 }))
}

function removeTarget(idx: number) {
  form.value.targets.splice(idx, 1)
}

function openCreate() {
  editingId.value = ''
  form.value = {
    name: '',
    description: '',
    priority: 10,
    enabled: true,
    conditions: [new model.RouteCondition({ field: 'model', operator: 'matches', value: '' })],
    targets: [new model.RouteTarget({ provider_id: '', model_name: '', action: 'forward', tier: 0 })],
  }
  modalOpen.value = true
}

function openEdit(route: model.Route) {
  editingId.value = route.id
  form.value = {
    name: route.name,
    description: route.description,
    priority: route.priority,
    enabled: route.enabled,
    conditions: route.conditions.map((c) => new model.RouteCondition({ field: c.field, operator: c.operator, value: c.value })),
    targets: route.targets.map((t) => new model.RouteTarget({ provider_id: t.provider_id, model_name: t.model_name, action: t.action, tier: t.tier })),
  }
  modalOpen.value = true
}

async function saveRoute() {
  saving.value = true
  try {
    const input = new model.RouteInput({
      name: form.value.name,
      description: form.value.description,
      priority: form.value.priority,
      enabled: form.value.enabled,
      conditions: form.value.conditions,
      targets: form.value.targets,
    })
    if (editingId.value) {
      await api.updateRoute(editingId.value, input)
    } else {
      await api.createRoute(input)
    }
    modalOpen.value = false
    await loadRoutes()
    toast.push('规则已保存', 'success')
  } catch (e: any) {
    toast.push('保存失败：' + (e?.message || String(e)), 'error')
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
      priority: full.priority,
      enabled: !full.enabled,
      conditions: full.conditions,
      targets: full.targets,
    })
    await api.updateRoute(route.id, input)
    await loadRoutes()
    toast.push(full.enabled ? '规则已禁用' : '规则已启用', 'success')
  } catch (e: any) {
    toast.push('切换失败：' + (e?.message || String(e)), 'error')
  }
}

async function deleteRoute(id: string) {
  if (!confirm('确认删除？')) return
  try {
    await api.deleteRoute(id)
    await loadRoutes()
    toast.push('规则已删除', 'success')
  } catch (e: any) {
    toast.push('删除失败：' + (e?.message || String(e)), 'error')
  }
  openMenuId.value = ''
}

function editDefault() {
  toast.push('暂未实现', 'warning')
}

function importJSON() {
  toast.push('暂未实现', 'warning')
}

function closeModal() {
  modalOpen.value = false
}

onMounted(() => {
  loadRoutes()
  loadProviders()
})
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">路由规则</h1>
      <span class="main-subtitle">{{ routes?.length ?? 0 }} 条规则 · 按优先级自上而下匹配</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary" @click="importJSON">从 JSON 导入</button>
      <button class="btn btn-primary" @click="openCreate">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
        新建规则
      </button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner stack-loose">
      <!-- Loading / error -->
      <div v-if="routesLoading && !routes" class="text-muted" style="padding: 40px 0; text-align: center;">加载中…</div>
      <div v-else-if="routesError" class="text-muted" style="padding: 40px 0; text-align: center; color: var(--negative);">加载失败：{{ routesError }}</div>
      <template v-else>
        <!-- Default fallback banner -->
        <div class="card" style="background: var(--black); color: white; display: flex; align-items: center; gap: 16px; padding: 16px 20px;">
          <div style="width: 36px; height: 36px; border-radius: 10px; background: rgba(255,255,255,0.12); display: flex; align-items: center; justify-content: center; flex-shrink: 0;">
            <svg viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:18px;height:18px;"><path d="M5 12h14M12 5l7 7-7 7"/></svg>
          </div>
          <div style="flex: 1;">
            <div style="font-size: 13px; font-weight: 600;">默认兜底</div>
            <div style="font-size: 12px; color: rgba(255,255,255,0.6); margin-top: 2px;">所有未匹配的请求将路由至 <span class="text-mono" style="color: white;">{{ defaultFallback.provider }} · {{ defaultFallback.model }}</span></div>
          </div>
          <button class="btn" style="background: rgba(255,255,255,0.12); color: white;" @click="editDefault">修改</button>
        </div>

        <!-- Rules -->
        <article v-for="(route, idx) in sortedRoutes" :key="route.id" class="card" :style="{ opacity: route.enabled ? 1 : 0.6 }">
          <div class="row-between" style="margin-bottom: 14px;">
            <div class="row" style="gap: 12px;">
              <div class="text-mono" style="font-size: 12px; color: var(--muted); width: 24px;">{{ String(idx + 1).padStart(2, '0') }}</div>
              <div>
                <div style="font-size: 14px; font-weight: 600;">{{ route.name }}</div>
                <div class="text-muted" style="font-size: 12px; margin-top: 2px;">{{ route.description }}</div>
              </div>
            </div>
            <div class="row" style="gap: 12px;">
              <span class="badge" :class="priorityBadgeClass(route)">优先级 {{ route.priority }}</span>
              <label class="toggle">
                <input type="checkbox" :checked="route.enabled" @change="toggleRoute(route)">
                <span class="toggle-slider blue"></span>
              </label>
              <div style="position: relative;">
                <button class="btn btn-icon" @click="openMenuId = openMenuId === route.id ? '' : route.id">
                  <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>
                </button>
                <div v-if="openMenuId === route.id" class="dropdown-menu" style="position: absolute; right: 0; top: 100%; background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 4px; box-shadow: var(--shadow-md); z-index: 10; min-width: 120px;">
                  <button class="dropdown-item" style="display: block; width: 100%; text-align: left; padding: 6px 10px; border-radius: 6px; font-size: 13px; background: transparent; border: none; color: var(--fg); cursor: pointer;" @click="openEdit(route); openMenuId = ''">编辑</button>
                  <button class="dropdown-item" style="display: block; width: 100%; text-align: left; padding: 6px 10px; border-radius: 6px; font-size: 13px; background: transparent; border: none; color: var(--fg); cursor: pointer;" @click="toggleRoute(route); openMenuId = ''">{{ route.enabled ? '禁用' : '启用' }}</button>
                  <button class="dropdown-item" style="display: block; width: 100%; text-align: left; padding: 6px 10px; border-radius: 6px; font-size: 13px; background: transparent; border: none; color: var(--negative); cursor: pointer;" @click="deleteRoute(route.id)">删除</button>
                </div>
              </div>
            </div>
          </div>
          <div class="col-3" style="gap: 0;">
            <div>
              <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">条件 (所有满足)</div>
              <div class="stack-tight">
                <div v-for="(c, cidx) in route.conditions" :key="cidx" class="row" style="gap: 6px;">
                  <span class="text-mono" style="font-size: 11.5px; color: var(--fg);">{{ c.field }}</span>
                  <span class="badge mono" style="background: rgba(0,113,227,0.08); color: var(--accent); font-weight: 500;">{{ operatorLabel(c.operator) }}</span>
                  <span class="text-mono" style="font-size: 11.5px;">{{ c.value }}</span>
                </div>
                <div v-if="!route.conditions.length" class="text-muted" style="font-size: 12px;">无</div>
              </div>
            </div>
            <div>
              <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">目标</div>
              <div v-for="(target, tidx) in route.targets" :key="tidx" class="row" style="gap: 8px; margin-bottom: 4px;">
                <div class="list-icon" :style="{ ...targetIconStyle(target.provider_id), width: '24px', height: '24px', fontSize: '11px', borderRadius: '6px' }">
                  {{ providerLetter(targetProviderName(target)) }}
                </div>
                <div>
                  <div style="font-size: 13px; font-weight: 500;">{{ targetProviderName(target) }}</div>
                  <div class="text-mono text-muted" style="font-size: 11.5px;">{{ target.model_name || targetActionLabel(target) }}</div>
                </div>
                <span class="badge" style="font-size: 10px; color: var(--muted); align-self: center;">{{ tierLabel(target.tier) }}</span>
              </div>
              <div v-if="!route.targets.length" class="text-muted" style="font-size: 12px;">无</div>
            </div>
            <div>
              <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">{{ route.enabled ? '本月命中' : '状态' }}</div>
              <template v-if="route.enabled">
                <div class="text-mono" style="font-size: 22px; font-weight: 600; letter-spacing: -0.02em;">{{ formatHits(route.monthly_hits) }}</div>
                <div class="text-muted text-mono" style="font-size: 11px; margin-top: 2px;" v-if="route.monthly_savings > 0">节省 {{ fmtCurrency(route.monthly_savings) }}</div>
                <div class="text-muted text-mono" style="font-size: 11px; margin-top: 2px;" v-else>占总请求 {{ route.monthly_hits ? '1.7%' : '0%' }}</div>
              </template>
              <template v-else>
                <div class="text-mono" style="font-size: 12px; color: var(--muted);">已禁用 · 创建于 <span :data-time="route.created_at">{{ format(route.created_at) }}</span></div>
              </template>
            </div>
          </div>
        </article>
      </template>
    </div>
  </div>

  <!-- Route modal -->
  <Teleport to="body">
    <div v-if="modalOpen" class="modal-overlay" @click.self="closeModal">
      <div class="modal-card" style="width: 640px; max-height: 90vh; overflow: auto;">
        <div style="font-size: 16px; font-weight: 600; margin-bottom: 16px;">{{ editingId ? '编辑规则' : '新建规则' }}</div>
        <div class="row" style="gap: 12px;">
          <div class="field" style="flex: 1; margin-bottom: 12px;">
            <label class="field-label">名称</label>
            <input v-model="form.name" class="input" placeholder="例如 成本优化">
          </div>
          <div class="field" style="width: 120px; margin-bottom: 12px;">
            <label class="field-label">优先级</label>
            <input v-model.number="form.priority" type="number" class="input">
          </div>
        </div>
        <div class="field" style="margin-bottom: 12px;">
          <label class="field-label">描述</label>
          <input v-model="form.description" class="input" placeholder="规则用途">
        </div>
        <div class="field" style="margin-bottom: 12px;">
          <label class="field-label" style="flex-direction: row; align-items: center; gap: 8px; cursor: pointer;">
            <input v-model="form.enabled" type="checkbox" style="width: 16px; height: 16px;">
            启用规则
          </label>
        </div>

        <div style="font-size: 13px; font-weight: 600; margin: 16px 0 8px;">条件</div>
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
            <input v-model="cond.value" class="input" style="flex: 1;" placeholder="值">
            <button class="btn btn-icon" style="width: 28px; height: 28px;" @click="removeCondition(idx)">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M18 6L6 18M6 6l12 12"/></svg>
            </button>
          </div>
          <button class="btn btn-secondary" style="align-self: flex-start;" @click="addCondition">添加条件</button>
        </div>

        <div style="font-size: 13px; font-weight: 600; margin: 16px 0 8px;">目标</div>
        <div class="stack-tight" style="gap: 8px;">
          <div v-for="(target, idx) in form.targets" :key="idx" class="row" style="gap: 8px; align-items: flex-start;">
            <select v-model="target.provider_id" class="select" style="flex: 1;">
              <option v-for="p in providers || []" :key="p.id" :value="p.id">{{ p.name }}</option>
            </select>
            <select v-model.number="target.tier" class="select" style="width: 80px;">
              <option value="0">P0</option>
              <option value="1">P1</option>
              <option value="2">P2</option>
              <option value="3">P3</option>
            </select>
            <input v-model="target.model_name" class="input" style="flex: 1;" placeholder="模型">
            <select v-model="target.action" class="select" style="width: 110px;">
              <option value="forward">forward</option>
              <option value="skip">skip</option>
            </select>
            <button class="btn btn-icon" style="width: 28px; height: 28px;" @click="removeTarget(idx)">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M18 6L6 18M6 6l12 12"/></svg>
            </button>
          </div>
          <button class="btn btn-secondary" style="align-self: flex-start;" @click="addTarget">添加目标</button>
        </div>

        <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
          <button class="btn btn-secondary" @click="closeModal">取消</button>
          <button class="btn btn-primary" :disabled="saving" @click="saveRoute">{{ saving ? '保存中…' : '保存' }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.35);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
  backdrop-filter: blur(4px);
}
.modal-card {
  background: var(--surface);
  border-radius: var(--radius-lg);
  padding: 24px;
  width: 640px;
  max-width: 90vw;
  box-shadow: var(--shadow-lg);
  border: 1px solid var(--border);
}
.dropdown-item:hover {
  background: rgba(0, 0, 0, 0.04) !important;
}
</style>
