<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { api } from '../api/client'
import { useApi } from '../composables/useApi'
import { useRelativeTime } from '../composables/useRelativeTime'
import { toDateTimeLocal, fromDateTimeLocal } from '../composables/useDateTime'
import { useToast } from '../composables/useToast'
import type { model } from '../../wailsjs/go/models'

const { format } = useRelativeTime()
const toast = useToast()

const {
  data: keys,
  loading: keysLoading,
  error: keysError,
  execute: loadKeys,
} = useApi(() => api.apiKeys())

const search = ref('')
const modalOpen = ref(false)
const modalMode = ref<'create' | 'edit'>('create')
const editingId = ref('')
const saving = ref(false)
const openMenuId = ref('')
const revealedToken = ref('')

const form = ref<model.ApiKeyInput>({
  name: '',
  expires_at: 0,
})

const filteredKeys = computed(() => {
  let list = keys.value || []
  const q = search.value.trim().toLowerCase()
  if (q) {
    list = list.filter((k) => k.name.toLowerCase().includes(q))
  }
  return list
})

const activeCount = computed(() => (keys.value || []).length)

const expiringCount = computed(() => {
  const now = Date.now()
  const fourteenDays = 14 * 24 * 60 * 60 * 1000
  return (keys.value || []).filter(
    (k) => k.expires_at > 0 && k.expires_at - now > 0 && k.expires_at - now <= fourteenDays
  ).length
})

function tokenDisplay(id: string): string {
  if (id.length <= 12) return id
  return `${id.slice(0, 8)}...${id.slice(-4)}`
}

function formatExpiresAt(ms: number): string {
  if (!ms || ms <= 0) return '不过期'
  return new Date(ms).toLocaleString()
}

async function copyToken(token: string) {
  try {
    await navigator.clipboard.writeText(token)
    toast.push('已复制到剪贴板', 'success')
  } catch (e: any) {
    toast.push('复制失败：' + (e?.message || String(e)), 'error')
  }
}

async function copyKey(key: model.ApiKey) {
  await copyToken(key.id)
  openMenuId.value = ''
}

async function deleteKey(id: string) {
  if (!confirm('确认删除？')) return
  try {
    await api.deleteApiKey(id)
    await loadKeys()
    toast.push('令牌已删除', 'success')
  } catch (e: any) {
    toast.push('删除失败：' + (e?.message || String(e)), 'error')
  }
  openMenuId.value = ''
}

function openCreateModal() {
  modalMode.value = 'create'
  editingId.value = ''
  revealedToken.value = ''
  form.value = {
    name: '',
    expires_at: 0,
  }
  modalOpen.value = true
}

function openEditModal(key: model.ApiKey) {
  modalMode.value = 'edit'
  editingId.value = key.id
  revealedToken.value = ''
  form.value = {
    name: key.name,
    expires_at: key.expires_at,
  }
  modalOpen.value = true
}

function closeModal() {
  modalOpen.value = false
  revealedToken.value = ''
}

async function saveKey() {
  saving.value = true
  try {
    if (modalMode.value === 'edit') {
      await api.updateApiKey(editingId.value, form.value)
      modalOpen.value = false
      await loadKeys()
      toast.push('令牌已更新', 'success')
    } else {
      const created = await api.createApiKey(form.value)
      revealedToken.value = created.id
      await loadKeys()
      toast.push('令牌已创建，请复制保存', 'success')
    }
  } catch (e: any) {
    toast.push('保存失败：' + (e?.message || String(e)), 'error')
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  loadKeys()
})
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">API 密钥</h1>
      <span class="main-subtitle">{{ activeCount }} 个令牌 · 存储于本地加密库</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-primary" @click="openCreateModal">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
        添加令牌
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
            <div class="stat-label">活跃令牌</div>
            <div class="stat-value">{{ activeCount }}</div>
            <div class="stat-meta"><span>全部可用</span></div>
          </div>
          <div class="stat-card">
            <div class="stat-label">即将过期</div>
            <div class="stat-value">{{ expiringCount }}</div>
            <div class="stat-meta">
              <span v-if="expiringCount" class="delta negative">14 天内</span>
              <span v-else>—</span>
            </div>
          </div>
        </div>

        <!-- filter bar -->
        <div class="row" style="gap: 8px;">
          <div class="row" style="background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 6px 10px; gap: 6px; flex: 1; max-width: 360px;">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;color:var(--muted);"><circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/></svg>
            <input v-model="search" class="input" style="border: none; padding: 0; font-size: 13px;" placeholder="搜索令牌名称">
          </div>
        </div>

        <!-- Empty state -->
        <div v-if="!filteredKeys.length" class="text-muted" style="padding: 40px 0; text-align: center;">暂无数据</div>

        <!-- keys table -->
        <div v-else class="card" style="padding: 0; overflow: hidden;">
          <table class="tbl">
            <thead>
              <tr>
                <th>名称</th>
                <th>访问令牌</th>
                <th>有效期至</th>
                <th class="right">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="key in filteredKeys" :key="key.id">
                <td>
                  <div style="font-weight: 500;">{{ key.name }}</div>
                  <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">{{ format(key.created_at) }}</div>
                </td>
                <td>
                  <div class="row" style="gap: 6px;">
                    <span class="text-mono" style="font-size: 12.5px; color: var(--muted);">{{ tokenDisplay(key.id) }}</span>
                    <button class="btn btn-icon" style="width: 22px; height: 22px;" title="复制" @click="copyKey(key)">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
                    </button>
                  </div>
                </td>
                <td>
                  <span :class="{ 'text-muted': !key.expires_at }">{{ formatExpiresAt(key.expires_at) }}</span>
                </td>
                <td class="right">
                  <div style="position: relative; display: inline-block;">
                    <button class="btn btn-icon" @click="openMenuId = openMenuId === key.id ? '' : key.id">
                      <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>
                    </button>
                    <div v-if="openMenuId === key.id" class="dropdown-menu" style="position: absolute; right: 0; top: 100%; background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 4px; box-shadow: var(--shadow-md); z-index: 10; min-width: 120px;">
                      <button class="dropdown-item" style="display: block; width: 100%; text-align: left; padding: 6px 10px; border-radius: 6px; font-size: 13px; background: transparent; border: none; color: var(--fg); cursor: pointer;" @click="openEditModal(key); openMenuId = ''">编辑</button>
                      <button class="dropdown-item" style="display: block; width: 100%; text-align: left; padding: 6px 10px; border-radius: 6px; font-size: 13px; background: transparent; border: none; color: var(--fg); cursor: pointer;" @click="copyKey(key)">复制</button>
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

  <!-- Create / edit modal -->
  <Teleport to="body">
    <div v-if="modalOpen" class="modal-overlay" @click.self="closeModal">
      <div class="modal-card">
        <div v-if="revealedToken" class="modal-title">保存您的访问令牌</div>
        <div v-else class="modal-title">{{ modalMode === 'edit' ? '编辑令牌' : '添加令牌' }}</div>

        <template v-if="revealedToken">
          <div class="field">
            <label class="field-label">访问令牌</label>
            <div class="row" style="gap: 6px;">
              <input
                :value="revealedToken"
                readonly
                class="input mono"
                style="flex: 1;"
              >
              <button class="btn btn-secondary" @click="copyToken(revealedToken)">复制</button>
            </div>
            <div class="field-help">这是您的访问令牌 ID，可在列表中随时复制。</div>
          </div>
          <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
            <button class="btn btn-primary" @click="closeModal">完成</button>
          </div>
        </template>

        <template v-else>
          <div class="field">
            <label class="field-label">名称</label>
            <input v-model="form.name" class="input" placeholder="例如 生产环境">
          </div>
          <div class="field">
            <label class="field-label">过期时间</label>
            <input
              :value="toDateTimeLocal(form.expires_at)"
              type="datetime-local"
              class="input input-datetime"
              @input="form.expires_at = fromDateTimeLocal(($event.target as HTMLInputElement).value)"
            >
            <div class="field-help">本地时间，留空表示不过期</div>
          </div>
          <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
            <button class="btn btn-secondary" @click="closeModal">取消</button>
            <button class="btn btn-primary" :disabled="saving" @click="saveKey">{{ saving ? '保存中…' : '保存' }}</button>
          </div>
        </template>
      </div>
    </div>
  </Teleport>
</template>
