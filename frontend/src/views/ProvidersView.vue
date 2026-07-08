<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { api } from '../api/client'
import { useApi } from '../composables/useApi'
import { useRelativeTime } from '../composables/useRelativeTime'
import { useProviderStyle } from '../composables/useProviderStyle'
import { useFormatters } from '../composables/useFormatters'
import { useToast } from '../composables/useToast'
import { useConfirm } from '../composables/useConfirm'
import type { model } from '../../wailsjs/go/models'

const { format } = useRelativeTime()
const { color: providerColor, initial: providerLetter } = useProviderStyle()
const { tokens: fmtTokens, latency: fmtLatency } = useFormatters()
const toast = useToast()
const confirm = useConfirm()

const {
  data: providers,
  loading: providersLoading,
  error: providersError,
  execute: loadProviders,
} = useApi(() => api.providers())

const modelsMap = ref<Record<string, model.Model[]>>({})

const activeTab = ref<'all' | 'connected' | 'error'>('all')
const search = ref('')
const sortBy = ref<'usage' | 'name' | 'last_tested'>('usage')

const modalOpen = ref(false)
const modalMode = ref<'add' | 'edit'>('add')
const editingId = ref('')
const saving = ref(false)
const testingIds = ref<Set<string>>(new Set())
const openMenuId = ref('')

const models = ref<model.Model[]>([])
const testingModelIds = ref<Set<string>>(new Set())
const fetchingModels = ref(false)

const form = ref<model.ProviderInput>({
  name: '',
  base_url: '',
  upstream_key: '',
  is_custom: false,
})

const filteredProviders = computed(() => {
  let list = providers.value || []
  if (activeTab.value === 'connected') {
    list = list.filter((p) => p.status === 'connected')
  } else if (activeTab.value === 'error') {
    list = list.filter((p) => p.status === 'error' || p.status === 'unknown')
  }
  const q = search.value.trim().toLowerCase()
  if (q) {
    list = list.filter((p) => p.name.toLowerCase().includes(q))
  }
  const sorted = [...list]
  if (sortBy.value === 'usage') {
    sorted.sort((a, b) => b.monthly_tokens - a.monthly_tokens)
  } else if (sortBy.value === 'name') {
    sorted.sort((a, b) => a.name.localeCompare(b.name))
  } else if (sortBy.value === 'last_tested') {
    sorted.sort((a, b) => b.last_tested_at - a.last_tested_at)
  }
  return sorted
})

const totalModelCount = computed(() =>
  (providers.value || []).reduce((sum, p) => sum + p.models_count, 0)
)

function statusLabel(status: string): string {
  if (status === 'connected') return '已连接'
  if (status === 'error') return '异常'
  return '未连接'
}

function statusBadgeClass(status: string): string {
  if (status === 'connected') return 'success'
  return 'error'
}

function statusDotClass(status: string): string {
  if (status === 'connected') return 'green'
  return 'red'
}

async function loadModels() {
  const list = providers.value || []
  const entries: Record<string, model.Model[]> = {}
  await Promise.all(
    list.map(async (p) => {
      try {
        entries[p.id] = await api.listModels(p.id)
      } catch {
        entries[p.id] = []
      }
    })
  )
  modelsMap.value = entries
}

async function refresh() {
  await loadProviders()
  await loadModels()
}

async function testOne(id: string) {
  testingIds.value.add(id)
  try {
    const res = await api.testProvider(id)
    toast.push(
      res.ok
        ? `测试成功：延迟 ${res.latency_ms}ms，模型 ${res.models.length} 个`
        : `测试失败：${res.error || '未知错误'}`,
      res.ok ? 'success' : 'error'
    )
  } catch (e: any) {
    toast.push('测试失败：' + (e?.message || String(e)), 'error')
  } finally {
    testingIds.value.delete(id)
  }
  await refresh()
}

async function testAll() {
  try {
    await api.testAllProviders()
    toast.push('全部测试完成', 'success')
  } catch (e: any) {
    toast.push('全部测试失败：' + (e?.message || String(e)), 'error')
  }
  await refresh()
}

function formatContext(n: number): string {
  if (!n) return '—'
  if (n >= 1000) return `${Math.round(n / 1000)}K`
  return String(n)
}

async function fetchModels() {
  if (!editingId.value) return
  fetchingModels.value = true
  try {
    models.value = await api.fetchUpstreamModels(editingId.value)
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    fetchingModels.value = false
  }
}

async function toggleModelActive(m: model.Model) {
  if (!editingId.value) return
  try {
    await api.setModelsActive(editingId.value, [m.name], !m.active)
    m.active = !m.active
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  }
}

async function testModelLatency(m: model.Model) {
  if (!editingId.value) return
  testingModelIds.value.add(m.id)
  try {
    const result = await api.testModelLatency(editingId.value, m.name)
    if (result.ok) {
      m.latency_ms = result.latency_ms
      toast.push(`${m.name} 延迟 ${result.latency_ms} ms`, 'success')
    } else {
      toast.push(result.error || '测试失败', 'error')
    }
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    testingModelIds.value.delete(m.id)
  }
}

function openAdd(isCustom: boolean) {
  modalMode.value = 'add'
  editingId.value = ''
  form.value = { name: '', base_url: '', upstream_key: '', is_custom: isCustom }
  modalOpen.value = true
}

function openEdit(provider: model.Provider) {
  modalMode.value = 'edit'
  editingId.value = provider.id
  form.value = {
    name: provider.name,
    base_url: provider.base_url,
    upstream_key: '',
    is_custom: provider.is_custom,
  }
  models.value = []
  modalOpen.value = true
  void api.listModels(provider.id).then((list) => { models.value = list }).catch(() => { models.value = [] })
}

async function saveProvider() {
  saving.value = true
  try {
    if (modalMode.value === 'edit') {
      await api.updateProvider(editingId.value, form.value)
    } else {
      await api.createProvider(form.value)
    }
    modalOpen.value = false
    await refresh()
    toast.push('Provider 已保存', 'success')
  } catch (e: any) {
    toast.push('保存失败：' + (e?.message || String(e)), 'error')
  } finally {
    saving.value = false
  }
}

function closeModal() {
  modalOpen.value = false
  models.value = []
  testingModelIds.value.clear()
  fetchingModels.value = false
}

async function deleteProvider(id: string, name: string) {
  openMenuId.value = ''
  const ok = await confirm.open({
    title: '删除 Provider',
    message: `确定删除 Provider「${name}」？该 Provider 下的所有模型与路由引用都将被清理，此操作不可撤销。`,
    confirmText: '删除',
    danger: true,
  })
  if (!ok) return
  saving.value = true
  try {
    await api.deleteProvider(id)
    await refresh()
    toast.push('Provider 已删除', 'success')
  } catch (e: any) {
    toast.push('删除失败：' + (e?.message || String(e)), 'error')
  } finally {
    saving.value = false
  }
}

function handleTabKeydown(e: KeyboardEvent) {
  const container = e.currentTarget as HTMLElement
  const tabs = container.querySelectorAll<HTMLButtonElement>('.tab')
  if (!tabs.length) return
  const currentIdx = Array.from(tabs).findIndex((t) => t === document.activeElement)
  let nextIdx = currentIdx
  switch (e.key) {
    case 'ArrowRight':
    case 'ArrowDown':
      e.preventDefault()
      nextIdx = (currentIdx + 1) % tabs.length
      break
    case 'ArrowLeft':
    case 'ArrowUp':
      e.preventDefault()
      nextIdx = (currentIdx - 1 + tabs.length) % tabs.length
      break
    case 'Home':
      e.preventDefault()
      nextIdx = 0
      break
    case 'End':
      e.preventDefault()
      nextIdx = tabs.length - 1
      break
    default:
      return
  }
  tabs[nextIdx]?.focus()
  tabs[nextIdx]?.click()
}

onMounted(() => {
  refresh()
})
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">Provider 管理</h1>
      <span class="main-subtitle">{{ providers?.length ?? 0 }} 个连接 · {{ totalModelCount }} 个模型</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary" :disabled="providersLoading" @click="testAll">
        {{ providersLoading ? '测试中…' : '测试全部' }}
      </button>
      <button class="btn btn-primary" @click="openAdd(false)">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
        添加 Provider
      </button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner stack-loose">
      <!-- Loading / error -->
      <div v-if="providersLoading && !providers" class="text-muted" style="padding: 40px 0; text-align: center;">加载中…</div>
      <div v-else-if="providersError" class="text-muted" style="padding: 40px 0; text-align: center; color: var(--negative);">加载失败：{{ providersError }}</div>
      <template v-else>
        <!-- Filter bar -->
        <div class="row" style="gap: 8px; flex-wrap: wrap;">
          <div class="row" style="background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 6px 10px; gap: 6px; flex: 1; max-width: 360px;">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;color:var(--muted);"><circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/></svg>
            <input v-model="search" class="input" style="border: none; padding: 0; font-size: 13px;" placeholder="搜索 Provider 或模型">
          </div>
          <div class="tabs" tabindex="0" @keydown="handleTabKeydown" style="outline: none;">
            <button class="tab" :class="{ active: activeTab === 'all' }" @click="activeTab = 'all'">全部</button>
            <button class="tab" :class="{ active: activeTab === 'connected' }" @click="activeTab = 'connected'">已连接</button>
            <button class="tab" :class="{ active: activeTab === 'error' }" @click="activeTab = 'error'">异常</button>
          </div>
          <div class="spacer"></div>
          <div class="row" style="font-size: 12px; color: var(--muted);">
            排序：
            <select v-model="sortBy" class="select" style="width: auto; padding: 5px 10px; font-size: 12px;">
              <option value="usage">用量</option>
              <option value="name">名称</option>
              <option value="last_tested">最近测试</option>
            </select>
          </div>
        </div>

        <!-- Empty state -->
        <div v-if="!filteredProviders.length" class="text-muted" style="padding: 40px 0; text-align: center;">暂无数据</div>

        <!-- Provider cards grid -->
        <section v-else class="col-2">
          <article v-for="provider in filteredProviders" :key="provider.id" class="card card-hover" :style="{ opacity: provider.status === 'connected' ? 1 : 0.78 }">
            <div class="row-between" style="margin-bottom: 14px;">
              <div class="row" style="gap: 12px;">
                <div class="list-icon" :style="{ background: providerColor(provider.name), color: 'white', width: '38px', height: '38px', fontSize: '15px' }">
                  {{ providerLetter(provider.name) }}
                </div>
                <div>
                  <div style="font-size: 15px; font-weight: 600;">{{ provider.name }}</div>
                  <div class="text-mono text-muted" style="font-size: 11.5px; margin-top: 1px;">{{ provider.base_url }}</div>
                </div>
              </div>
              <span class="badge" :class="statusBadgeClass(provider.status)">
                <span class="dot" :class="statusDotClass(provider.status)"></span>
                {{ statusLabel(provider.status) }}
              </span>
            </div>
            <div class="h-divider" style="margin: 0 0 14px;"></div>
            <div class="row-between" style="margin-bottom: 10px;">
              <span class="text-muted" style="font-size: 12px;">本月用量</span>
              <span class="text-mono" style="font-size: 13px; font-weight: 500;">{{ fmtTokens(provider.monthly_tokens) }} tokens</span>
            </div>
            <div class="row-between" style="margin-bottom: 14px;">
              <span class="text-muted" style="font-size: 12px;">{{ provider.status === 'connected' ? '平均延迟' : '最后错误' }}</span>
              <span class="text-mono" :style="{ fontSize: '13px', fontWeight: 500, color: provider.status === 'connected' ? 'inherit' : 'var(--negative)' }">
                {{ provider.status === 'connected' ? fmtLatency(provider.avg_latency_ms) : (provider.error_message || '—') }}
              </span>
            </div>
            <div class="row" style="flex-wrap: wrap; gap: 4px; margin-bottom: 14px;">
              <template v-if="modelsMap[provider.id]?.length">
                <span v-for="m in modelsMap[provider.id]" :key="m.id" class="badge mono">{{ m.name }}</span>
              </template>
              <span v-else class="badge mono" style="color: var(--muted);">{{ provider.models_count }} 个模型</span>
            </div>
            <div class="row-between">
              <span class="text-muted" style="font-size: 11px;" :data-time="provider.last_tested_at">{{ provider.status === 'connected' ? '测试于' : '失败于' }} {{ format(provider.last_tested_at) }}</span>
              <div class="row" style="gap: 4px;">
                <button
                  class="btn"
                  :class="provider.status === 'connected' ? 'btn-secondary' : 'btn-primary'"
                  style="padding: 4px 10px; font-size: 12px;"
                  :disabled="testingIds.has(provider.id)"
                  @click="testOne(provider.id)"
                >
                  {{ testingIds.has(provider.id) ? '测试中…' : (provider.status === 'connected' ? '测试' : '重新连接') }}
                </button>
                <button class="btn btn-icon" title="编辑" @click="openEdit(provider)">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4z"/></svg>
                </button>
                <div style="position: relative; display: inline-block;">
                  <button class="btn btn-icon" title="更多" @click="openMenuId = openMenuId === provider.id ? '' : provider.id">
                    <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>
                  </button>
                  <div v-if="openMenuId === provider.id" class="dropdown-menu">
                    <button class="dropdown-item" @click="openEdit(provider); openMenuId = ''">编辑</button>
                    <button class="dropdown-item danger" @click="deleteProvider(provider.id, provider.name)">删除</button>
                  </div>
                </div>
              </div>
            </div>
          </article>

          <article class="card card-hover" style="border-style: dashed; display: flex; align-items: center; justify-content: center; min-height: 240px; background: transparent; cursor: pointer;" @click="openAdd(true)">
            <div style="text-align: center; color: var(--muted);">
              <div style="width: 48px; height: 48px; border-radius: 24px; background: rgba(0, 113, 227, 0.08); display: inline-flex; align-items: center; justify-content: center; margin-bottom: 12px;">
                <svg viewBox="0 0 24 24" fill="none" stroke="#0071e3" stroke-width="1.6" stroke-linecap="round" style="width:22px;height:22px;"><path d="M12 5v14M5 12h14"/></svg>
              </div>
              <div style="font-size: 14px; font-weight: 500; color: var(--fg);">添加自定义 Provider</div>
              <div style="font-size: 12px; margin-top: 4px;">OpenAI 兼容 / 自部署网关</div>
            </div>
          </article>
        </section>
      </template>
    </div>
  </div>

  <!-- Provider modal -->
  <Teleport to="body">
    <div v-if="modalOpen" class="modal-overlay" @click.self="closeModal">
      <div class="modal-card wide">
        <div class="modal-title">{{ modalMode === 'edit' ? '编辑 Provider' : (form.is_custom ? '添加自定义 Provider' : '添加 Provider') }}</div>
        <div class="field">
          <label class="field-label">名称</label>
          <input v-model="form.name" class="input" placeholder="例如 OpenAI">
        </div>
        <div class="field">
          <label class="field-label">Base URL</label>
          <input v-model="form.base_url" class="input mono" placeholder="https://api.example.com">
        </div>
        <div class="field">
          <label class="field-label">上游密钥</label>
          <div v-if="modalMode === 'edit' && (providers || []).find((p) => p.id === editingId)?.key_masked" class="text-mono" style="font-size: 12px; color: var(--muted); margin-bottom: 6px;">
            当前：{{ (providers || []).find((p) => p.id === editingId)?.key_masked }}
          </div>
          <input v-model="form.upstream_key" type="password" class="input mono" placeholder="sk-...">
          <div class="field-help">用于向该 Provider 转发请求的加密密钥。编辑时留空可保持现有密钥不变。</div>
        </div>
        <div v-if="modalMode === 'add'" class="field">
          <div class="row-between" style="margin-bottom: 0;">
            <label class="field-label">自定义 Provider</label>
            <label class="toggle">
              <input v-model="form.is_custom" type="checkbox">
              <span class="toggle-slider"></span>
            </label>
          </div>
        </div>

        <div v-if="modalMode === 'edit'" class="field">
          <div class="row-between" style="margin-bottom: 8px;">
            <label class="field-label">模型</label>
            <button class="btn btn-secondary" style="padding: 4px 10px; font-size: 12px;" :disabled="fetchingModels" @click="fetchModels">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;"><path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/></svg>
              {{ fetchingModels ? '获取中…' : '获取上游模型' }}
            </button>
          </div>
          <div class="model-list">
            <div v-if="fetchingModels" class="model-empty">获取上游模型列表…</div>
            <div v-else-if="!models.length" class="model-empty">暂无模型，点击右上角获取</div>
            <table v-else class="model-table">
              <thead>
                <tr>
                  <th>模型</th>
                  <th class="right">上下文</th>
                  <th class="right">延迟</th>
                  <th class="right">启用</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="m in models" :key="m.id">
                  <td>
                    <div class="model-name">{{ m.name }}</div>
                    <div class="model-owner">{{ (providers || []).find((p) => p.id === editingId)?.name }}</div>
                  </td>
                  <td class="num">{{ formatContext(m.context_window) }}</td>
                  <td class="num">
                    <span v-if="m.latency_ms">{{ m.latency_ms }} ms</span>
                    <span v-else class="text-muted">—</span>
                  </td>
                  <td class="right">
                    <label class="toggle">
                      <input type="checkbox" :checked="m.active" @change="toggleModelActive(m)">
                      <span class="toggle-slider blue"></span>
                    </label>
                  </td>
                  <td class="right">
                    <button class="btn btn-icon" :disabled="testingModelIds.has(m.id)" title="测试延迟" @click="testModelLatency(m)">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;"><path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M2 12h4M18 12h4M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"/></svg>
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
          <button class="btn btn-secondary" @click="closeModal">取消</button>
          <button class="btn btn-primary" :disabled="saving" @click="saveProvider">{{ saving ? '保存中…' : '保存' }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
