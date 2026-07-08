<script setup lang="ts">
import { ref, watch, computed, onMounted } from 'vue'
import { VueDraggable } from 'vue-draggable-plus'
import { api } from '../api/client'
import { useApi } from '../composables/useApi'
import { useRelativeTime } from '../composables/useRelativeTime'
import { useProviderStyle } from '../composables/useProviderStyle'
import { useFormatters } from '../composables/useFormatters'
import { useToast } from '../composables/useToast'
import { useConfirm } from '../composables/useConfirm'
import DropdownMenu from '@/components/DropdownMenu.vue'
import { model } from '../../wailsjs/go/models'

const { format } = useRelativeTime()
const { color: providerColor, initial: providerLetter } = useProviderStyle()
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

// Form state — targets live separately so vue-draggable-plus can rebind cleanly.
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
    toast.push('排序失败：' + (e?.message || String(e)), 'error')
    await loadRoutes() // revert to server truth
  }
}

// Stable v-for key for form targets (new ones lack a backend id).
let _targetSeq = 0
function targetKey(target: model.RouteTarget, idx: number): string {
  if (target.id) return target.id
  if (!(target as any)._cid) {
    Object.defineProperty(target, '_cid', { value: `t-${++_targetSeq}`, enumerable: false })
  }
  return (target as any)._cid
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
  formTargets.value.push(new model.RouteTarget({ provider_id: '', model_name: '', max_retries: 0 }))
}

function removeTarget(idx: number) {
  formTargets.value.splice(idx, 1)
}

function openCreate() {
  editingId.value = ''
  form.value = {
    name: '',
    description: '',
    enabled: true,
    conditions: [new model.RouteCondition({ field: 'model', operator: 'matches', value: '' })],
  }
  formTargets.value = [new model.RouteTarget({ provider_id: '', model_name: '', max_retries: 0 })]
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
  formTargets.value = route.targets.map((t) => new model.RouteTarget({
    id: t.id,
    route_id: t.route_id,
    provider_id: t.provider_id,
    model_name: t.model_name,
    max_retries: t.max_retries,
    hit_count: t.hit_count,
    failure_count: t.failure_count,
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

async function deleteRoute(id: string, name: string) {
  const ok = await confirm.open({
    title: '删除规则',
    message: `确定删除规则「${name}」？此操作不可撤销。`,
    confirmText: '删除',
    danger: true,
  })
  if (!ok) return
  deleting.value = true
  try {
    await api.deleteRoute(id)
    await loadRoutes()
    toast.push('规则已删除', 'success')
  } catch (e: any) {
    toast.push('删除失败：' + (e?.message || String(e)), 'error')
  } finally {
    deleting.value = false
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
      toast.push('设置加载失败', 'error')
      return
    }
    s.routing.default_provider_id = fallbackProviderId.value
    s.routing.default_model = fallbackModel.value
    await api.saveSettings(s)
    await loadSettings()
    fallbackModalOpen.value = false
    toast.push('默认兜底已更新', 'success')
  } catch (e: any) {
    toast.push('保存失败：' + (e?.message || String(e)), 'error')
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
        throw new Error('JSON 应为规则数组')
      }
      for (const item of parsed) {
        if (!item || typeof item.name !== 'string') {
          throw new Error('规则对象缺少 name 字段')
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
          })),
        }))
      }
      for (const input of inputs) {
        await api.createRoute(input)
      }
      await loadRoutes()
      toast.push(`已导入 ${inputs.length} 条规则`, 'success')
    } catch (e: any) {
      toast.push('导入失败：' + (e?.message || String(e)), 'error')
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
  a.download = `autoapi-routes-${new Date().toISOString().slice(0, 10)}.json`
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 0)
  toast.push('规则已导出', 'success')
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
  loadSettings().catch((e: any) => toast.push('加载设置失败：' + (e?.message || String(e)), 'error'))
})
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">路由规则</h1>
      <span class="main-subtitle">{{ ruleList.length ?? 0 }} 条规则 · 拖动排序，越靠前优先级越高</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary" @click="importJSON">导入 JSON</button>
      <button class="btn btn-secondary" @click="exportJSON">导出 JSON</button>
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
          <article v-for="(route, idx) in ruleList" :key="route.id" class="card" :style="{ opacity: route.enabled ? 1 : 0.6 }">
            <div class="row-between" style="margin-bottom: 14px;">
              <div class="row" style="gap: 12px;">
                <!-- Drag handle -->
                <svg class="drag-handle" viewBox="0 0 16 28" fill="currentColor" width="14" height="24" aria-label="拖拽排序">
                  <circle cx="5" cy="5.5" r="1.4"/>
                  <circle cx="11" cy="5.5" r="1.4"/>
                  <circle cx="5" cy="14" r="1.5"/>
                  <circle cx="11" cy="14" r="1.5"/>
                  <circle cx="5" cy="22.5" r="1.5"/>
                  <circle cx="11" cy="22.5" r="1.5"/>
                </svg>
                <div class="text-mono" style="font-size: 12px; color: var(--muted); width: 24px;">{{ String(idx + 1).padStart(2, '0') }}</div>
                <div>
                  <div style="font-size: 14px; font-weight: 600;">{{ route.name }}</div>
                  <div class="text-muted" style="font-size: 12px; margin-top: 2px;">{{ route.description }}</div>
                </div>
              </div>
              <div class="row" style="gap: 12px;">
                <label class="toggle">
                  <input type="checkbox" :checked="route.enabled" @change="toggleRoute(route)">
                  <span class="toggle-slider blue"></span>
                </label>
                <DropdownMenu :menu-id="route.id">
                  <template #trigger="{ toggle, open }">
                    <button
                      class="btn btn-icon"
                      :aria-expanded="open"
                      aria-haspopup="menu"
                      :aria-label="`更多操作：${route.name}`"
                      @click="toggle"
                    >
                      <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>
                    </button>
                  </template>
                  <template #menu="{ close }">
                    <button class="dropdown-item" role="menuitem" @click="openEdit(route); close()">编辑</button>
                    <button class="dropdown-item" role="menuitem" @click="toggleRoute(route); close()">{{ route.enabled ? '禁用' : '启用' }}</button>
                    <button class="dropdown-item danger" role="menuitem" :disabled="deleting" @click="deleteRoute(route.id, route.name); close()">删除</button>
                  </template>
                </DropdownMenu>
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
                <div v-for="(target, tidx) in route.targets" :key="tidx" class="row" style="gap: 8px; margin-bottom: 6px; align-items: flex-start;">
                  <div class="list-icon" :style="{ ...targetIconStyle(target.provider_id), width: '24px', height: '24px', fontSize: '11px', borderRadius: '6px' }">
                    {{ providerLetter(targetProviderName(target)) }}
                  </div>
                  <div style="flex: 1; min-width: 0;">
                    <div style="font-size: 13px; font-weight: 500;">{{ targetProviderName(target) }}</div>
                    <div class="text-mono text-muted" style="font-size: 11.5px;">{{ target.model_name || '默认' }}</div>
                    <div class="target-counters">
                      命中 <span>{{ formatHits(target.hit_count) }}</span>
                      <span style="margin: 0 3px;">·</span>
                      失败 <span :class="{ 'fail-hi': target.failure_count > 0 }">{{ formatHits(target.failure_count) }}</span>
                    </div>
                  </div>
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
        </VueDraggable>
      </template>
    </div>
  </div>

  <!-- Route modal -->
  <Teleport to="body">
    <div v-if="modalOpen" class="modal-overlay" @click.self="closeModal">
      <div class="modal-card wide modal-card-scroll">
        <div class="modal-title">{{ editingId ? '编辑规则' : '新建规则' }}</div>
        <div class="field">
          <label class="field-label">名称</label>
          <input v-model="form.name" class="input" placeholder="例如 成本优化">
        </div>
        <div class="field">
          <label class="field-label">描述</label>
          <input v-model="form.description" class="input" placeholder="规则用途">
        </div>
        <div class="field">
          <div class="row-between" style="margin-bottom: 0;">
            <label class="field-label">启用规则</label>
            <label class="toggle">
              <input v-model="form.enabled" type="checkbox">
              <span class="toggle-slider"></span>
            </label>
          </div>
        </div>

        <div class="field" style="margin-bottom: 8px;">
          <div class="field-label">条件</div>
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
            <input v-model="cond.value" class="input" style="flex: 1;" placeholder="值">
            <button class="btn btn-icon" style="width: 28px; height: 28px;" @click="removeCondition(idx)">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M18 6L6 18M6 6l12 12"/></svg>
            </button>
          </div>
          <button class="btn btn-secondary" style="align-self: flex-start;" @click="addCondition">添加条件</button>
        </div>

        <div class="field" style="margin-bottom: 8px;">
          <div class="field-label">目标 <span class="text-muted" style="font-weight: 400; text-transform: none; letter-spacing: 0;">· 拖动排序</span></div>
        </div>
        <VueDraggable
          v-model="formTargets"
          handle=".drag-handle"
          :animation="150"
          ghost-class="sortable-ghost"
          chosen-class="sortable-chosen"
          drag-class="sortable-drag"
          class="stack-tight"
          style="gap: 8px;"
        >
          <div v-for="(target, idx) in formTargets" :key="targetKey(target, idx)" class="row" style="gap: 8px; align-items: flex-start;">
            <!-- Target drag handle -->
            <svg class="drag-handle" viewBox="0 0 16 28" fill="currentColor" width="14" height="22" aria-label="拖拽排序" style="margin-top: 6px;">
              <circle cx="5" cy="5.5" r="1.4"/>
              <circle cx="11" cy="5.5" r="1.4"/>
              <circle cx="5" cy="14" r="1.5"/>
              <circle cx="11" cy="14" r="1.5"/>
              <circle cx="5" cy="22.5" r="1.5"/>
              <circle cx="11" cy="22.5" r="1.5"/>
            </svg>
            <select v-model="target.provider_id" class="select" style="flex: 1;">
              <option v-for="p in providers || []" :key="p.id" :value="p.id">{{ p.name }}</option>
            </select>
            <input v-model="target.model_name" class="input" style="flex: 1;" placeholder="模型">
            <input v-model.number="target.max_retries" type="number" class="input" style="width: 110px;" placeholder="最大重试" min="0" step="1">
            <button class="btn btn-icon" style="width: 28px; height: 28px;" @click="removeTarget(idx)">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M18 6L6 18M6 6l12 12"/></svg>
            </button>
          </div>
        </VueDraggable>
        <button class="btn btn-secondary" style="align-self: flex-start;" @click="addTarget">添加目标</button>

        <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
          <button class="btn btn-secondary" @click="closeModal">取消</button>
          <button class="btn btn-primary" :disabled="saving" @click="saveRoute">{{ saving ? '保存中…' : '保存' }}</button>
        </div>
      </div>
    </div>
  </Teleport>

  <!-- Default fallback modal -->
  <Teleport to="body">
    <div v-if="fallbackModalOpen" class="modal-overlay" @click.self="closeFallbackModal">
      <div class="modal-card">
        <div class="modal-title">修改默认兜底</div>
        <div class="field">
          <label class="field-label">Provider</label>
          <select v-model="fallbackProviderId" class="select">
            <option v-for="p in providers || []" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
        </div>
        <div class="field">
          <label class="field-label">模型</label>
          <input v-model="fallbackModel" class="input" placeholder="例如 gpt-4o-mini">
        </div>
        <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
          <button class="btn btn-secondary" @click="closeFallbackModal">取消</button>
          <button class="btn btn-primary" @click="saveDefaultFallback">保存</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
