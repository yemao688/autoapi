<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { api } from '../api/client'
import { useApi } from '../composables/useApi'
import { useRelativeTime } from '../composables/useRelativeTime'
import { useProviderMeta } from '../composables/useProviderMeta'
import { useFormatters } from '../composables/useFormatters'
import type { model } from '../../wailsjs/go/models'

const { format } = useRelativeTime()
const { color: providerColor, letter: providerLetter } = useProviderMeta()
const { tokens: fmtTokens } = useFormatters()

const {
  data: keys,
  loading: keysLoading,
  error: keysError,
  execute: loadKeys,
} = useApi(() => api.apiKeys())

const {
  data: providers,
  execute: loadProviders,
} = useApi(() => api.providers())

const activeTab = ref<'all' | 'production' | 'test' | 'disabled'>('all')
const search = ref('')
const modalOpen = ref(false)
const saving = ref(false)
const openMenuId = ref('')

const form = ref<model.ApiKeyInput>({
  provider_id: '',
  name: '',
  key: '',
  permission: 'read_write',
  environment: 'production',
  expires_at: 0,
})

const filteredKeys = computed(() => {
  let list = keys.value || []
  if (activeTab.value !== 'all') {
    list = list.filter((k) => k.environment === activeTab.value)
  }
  const q = search.value.trim().toLowerCase()
  if (q) {
    list = list.filter(
      (k) =>
        k.name.toLowerCase().includes(q) ||
        providerNameMap.value[k.provider_id]?.toLowerCase().includes(q)
    )
  }
  return list
})

const providerNameMap = computed(() => {
  const map: Record<string, string> = {}
  ;(providers.value || []).forEach((p) => (map[p.id] = p.name))
  return map
})

const activeCount = computed(() => (keys.value || []).length)
const providerKeyCount = computed(() => (keys.value || []).filter((k) => k.provider_id).length)
const customKeyCount = computed(() => (keys.value || []).filter((k) => !k.provider_id).length)

const expiringCount = computed(() => {
  const now = Date.now()
  const fourteenDays = 14 * 24 * 60 * 60 * 1000
  return (keys.value || []).filter(
    (k) => k.expires_at > 0 && k.expires_at - now > 0 && k.expires_at - now <= fourteenDays
  ).length
})

const totalMonthlyTokens = computed(() =>
  (keys.value || []).reduce((sum, k) => sum + k.monthly_tokens, 0)
)

const mostRecentKey = computed(() => {
  const list = keys.value || []
  if (!list.length) return null
  return [...list].sort((a, b) => b.updated_at - a.updated_at)[0]
})

function keyMasked(key: model.ApiKey): string {
  if (key.key_masked) return key.key_masked
  if (key.key_prefix && key.key_suffix) return `${key.key_prefix}****${key.key_suffix}`
  return '****'
}

function permissionLabel(p: string): string {
  if (p === 'read_write') return '读 + 写'
  if (p === 'read_only') return '只读'
  if (p === 'write_only') return '只写'
  if (p === 'admin') return '管理'
  return p
}

function permissionBadgeClass(p: string): string {
  if (p === 'read_write') return 'info'
  if (p === 'admin') return 'warn'
  return ''
}

function environmentLabel(env: string): string {
  if (env === 'production') return '生产'
  if (env === 'test') return '测试'
  if (env === 'disabled') return '已禁用'
  return env
}

function environmentBadgeClass(env: string): string {
  if (env === 'production') return 'info'
  if (env === 'disabled') return 'warn'
  return ''
}

async function copyKey(key: model.ApiKey) {
  try {
    await navigator.clipboard.writeText(keyMasked(key))
    alert('已复制到剪贴板')
  } catch (e: any) {
    alert('复制失败：' + (e?.message || String(e)))
  }
  openMenuId.value = ''
}

async function deleteKey(id: string) {
  if (!confirm('确认删除？')) return
  try {
    await api.deleteApiKey(id)
    await loadKeys()
  } catch (e: any) {
    alert('删除失败：' + (e?.message || String(e)))
  }
  openMenuId.value = ''
}

async function exportEnv() {
  try {
    const res = await api.exportData('settings_json')
    const bytes = new Uint8Array(res.data)
    const blob = new Blob([bytes], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = res.filename || 'export.json'
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
  } catch (e: any) {
    alert('导出失败：' + (e?.message || String(e)))
  }
}

function openModal() {
  form.value = {
    provider_id: '',
    name: '',
    key: '',
    permission: 'read_write',
    environment: 'production',
    expires_at: 0,
  }
  modalOpen.value = true
}

function closeModal() {
  modalOpen.value = false
}

async function saveKey() {
  saving.value = true
  try {
    await api.createApiKey(form.value)
    form.value.key = ''
    modalOpen.value = false
    await loadKeys()
  } catch (e: any) {
    alert('保存失败：' + (e?.message || String(e)))
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  loadKeys()
  loadProviders()
})
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">API 密钥</h1>
      <span class="main-subtitle">{{ activeCount }} 个密钥 · 存储于本地加密库</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary" @click="exportEnv">导出 .env</button>
      <button class="btn btn-primary" @click="openModal">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
        添加密钥
      </button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner stack-loose">
      <!-- Loading / error -->
      <div v-if="keysLoading && !keys" class="text-muted" style="padding: 40px 0; text-align: center;">加载中…</div>
      <div v-else-if="keysError" class="text-muted" style="padding: 40px 0; text-align: center; color: var(--negative);">加载失败：{{ keysError }}</div>
      <template v-else>
        <!-- summary stats -->
        <div class="stat-grid">
          <div class="stat-card">
            <div class="stat-label">活跃密钥</div>
            <div class="stat-value">{{ activeCount }}</div>
            <div class="stat-meta"><span>{{ providerKeyCount }} Provider · {{ customKeyCount }} 自定义</span></div>
          </div>
          <div class="stat-card">
            <div class="stat-label">即将过期</div>
            <div class="stat-value">{{ expiringCount }}</div>
            <div class="stat-meta">
              <span v-if="expiringCount" class="delta negative">14 天内</span>
              <span v-else>—</span>
            </div>
          </div>
          <div class="stat-card">
            <div class="stat-label">本月用量</div>
            <div class="stat-value">{{ fmtTokens(totalMonthlyTokens) }}</div>
            <div class="stat-meta"><span class="delta positive">tokens</span><span>累计</span></div>
          </div>
          <div class="stat-card">
            <div class="stat-label">最近轮换</div>
            <div class="stat-value">{{ mostRecentKey ? format(mostRecentKey.updated_at) : '—' }}</div>
            <div class="stat-meta" v-if="mostRecentKey">
              <span>{{ providerNameMap[mostRecentKey.provider_id] || '自定义' }} · {{ environmentLabel(mostRecentKey.environment) }}</span>
            </div>
            <div class="stat-meta" v-else><span>—</span></div>
          </div>
        </div>

        <!-- filter bar -->
        <div class="row" style="gap: 8px;">
          <div class="row" style="background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 6px 10px; gap: 6px; flex: 1; max-width: 360px;">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;color:var(--muted);"><circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/></svg>
            <input v-model="search" class="input" style="border: none; padding: 0; font-size: 13px;" placeholder="搜索密钥名称或 Provider">
          </div>
          <div class="tabs">
            <button class="tab" :class="{ active: activeTab === 'all' }" @click="activeTab = 'all'">全部</button>
            <button class="tab" :class="{ active: activeTab === 'production' }" @click="activeTab = 'production'">生产</button>
            <button class="tab" :class="{ active: activeTab === 'test' }" @click="activeTab = 'test'">测试</button>
            <button class="tab" :class="{ active: activeTab === 'disabled' }" @click="activeTab = 'disabled'">已禁用</button>
          </div>
        </div>

        <!-- Empty state -->
        <div v-if="!filteredKeys.length" class="text-muted" style="padding: 40px 0; text-align: center;">暂无数据</div>

        <!-- keys table -->
        <div v-else class="card" style="padding: 0; overflow: hidden;">
          <table class="tbl">
            <thead>
              <tr>
                <th>Provider</th>
                <th>名称</th>
                <th>密钥</th>
                <th>权限</th>
                <th>最后使用</th>
                <th class="right">用量</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="key in filteredKeys" :key="key.id">
                <td>
                  <div class="row" style="gap: 8px;">
                    <div
                      class="list-icon"
                      :style="{ background: providerColor(providerNameMap[key.provider_id] || '自定义'), color: 'white', width: '26px', height: '26px', fontSize: '11px', borderRadius: '6px' }"
                    >
                      {{ providerLetter(providerNameMap[key.provider_id] || '自定义') }}
                    </div>
                    <span>{{ providerNameMap[key.provider_id] || '自定义' }}</span>
                  </div>
                </td>
                <td>
                  <div style="font-weight: 500;">{{ key.name }}</div>
                  <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">{{ key.id }}</div>
                </td>
                <td>
                  <div class="row" style="gap: 6px;">
                    <span class="text-mono" style="font-size: 12.5px; color: var(--muted);">{{ keyMasked(key) }}</span>
                    <button class="btn btn-icon" style="width: 22px; height: 22px;" title="复制" @click="copyKey(key)">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
                    </button>
                  </div>
                </td>
                <td><span class="badge" :class="permissionBadgeClass(key.permission)">{{ permissionLabel(key.permission) }}</span></td>
                <td><span :data-time="key.last_used_at" class="text-muted">{{ format(key.last_used_at) }}</span></td>
                <td class="num">{{ fmtTokens(key.monthly_tokens) }}</td>
                <td class="right">
                  <div style="position: relative; display: inline-block;">
                    <button class="btn btn-icon" @click="openMenuId = openMenuId === key.id ? '' : key.id">
                      <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>
                    </button>
                    <div v-if="openMenuId === key.id" class="dropdown-menu" style="position: absolute; right: 0; top: 100%; background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 4px; box-shadow: var(--shadow-md); z-index: 10; min-width: 120px;">
                      <button class="dropdown-item" style="display: block; width: 100%; text-align: left; padding: 6px 10px; border-radius: 6px; font-size: 13px; background: transparent; border: none; color: var(--fg); cursor: pointer;" @click="copyKey(key)">复制密钥</button>
                      <button class="dropdown-item" style="display: block; width: 100%; text-align: left; padding: 6px 10px; border-radius: 6px; font-size: 13px; background: transparent; border: none; color: var(--negative); cursor: pointer;" @click="deleteKey(key.id)">删除</button>
                    </div>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>
  </div>

  <!-- Add key modal -->
  <Teleport to="body">
    <div v-if="modalOpen" class="modal-overlay" @click.self="closeModal">
      <div class="modal-card">
        <div style="font-size: 16px; font-weight: 600; margin-bottom: 16px;">添加密钥</div>
        <div class="field" style="margin-bottom: 12px;">
          <label class="field-label">Provider</label>
          <select v-model="form.provider_id" class="select">
            <option value="">自定义</option>
            <option v-for="p in providers || []" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
        </div>
        <div class="field" style="margin-bottom: 12px;">
          <label class="field-label">名称</label>
          <input v-model="form.name" class="input" placeholder="例如 生产环境">
        </div>
        <div class="field" style="margin-bottom: 12px;">
          <label class="field-label">密钥</label>
          <input v-model="form.key" type="password" class="input mono" placeholder="sk-...">
        </div>
        <div class="field" style="margin-bottom: 12px;">
          <label class="field-label">权限</label>
          <select v-model="form.permission" class="select">
            <option value="read_write">读 + 写</option>
            <option value="read_only">只读</option>
            <option value="write_only">只写</option>
            <option value="admin">管理</option>
          </select>
        </div>
        <div class="field" style="margin-bottom: 12px;">
          <label class="field-label">环境</label>
          <select v-model="form.environment" class="select">
            <option value="production">生产</option>
            <option value="test">测试</option>
            <option value="disabled">已禁用</option>
          </select>
        </div>
        <div class="field" style="margin-bottom: 12px;">
          <label class="field-label">过期时间</label>
          <input v-model="form.expires_at" type="number" class="input" placeholder="0 表示不过期">
          <div class="field-help">Unix 时间戳（毫秒），0 表示不过期</div>
        </div>
        <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
          <button class="btn btn-secondary" @click="closeModal">取消</button>
          <button class="btn btn-primary" :disabled="saving" @click="saveKey">{{ saving ? '保存中…' : '保存' }}</button>
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
  width: 480px;
  max-width: 90vw;
  box-shadow: var(--shadow-lg);
  border: 1px solid var(--border);
}
.dropdown-item:hover {
  background: rgba(0, 0, 0, 0.04) !important;
}
</style>
