<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import type { model } from '../../wailsjs/go/models'
import { api } from '@/api/client'
import { useApi } from '@/composables/useApi'
import { useExportDownload } from '@/composables/useExportDownload'
import { useMasterGate } from '@/composables/useMasterGate'
import { useToast } from '@/composables/useToast'
import { useTheme, type ThemeValue } from '@/composables/useTheme'

const { state: gateState } = useMasterGate()
const { download } = useExportDownload()
const { data: fetchedSettings, loading, execute: fetchSettings } = useApi(api.getSettings)
const toast = useToast()
const { activeTheme } = useTheme()

const isDirty = ref(false)
const showPasswordModal = ref(false)
const oldPassword = ref('')
const newPassword = ref('')
const activeSection = ref('general')

const defaultStoragePath = '~/Library/Application Support/autoapi/'

function defaultSettings(): model.Settings {
  return {
    general: {
      launch_at_login: false,
      startup_action: 'show_window',
      menu_bar_item: true,
      close_action: 'background',
    },
    appearance: {
      theme: 'light',
      density: '标准',
      accent_color: '#0071e3',
    },
    routing: {
      default_provider_id: 'openai',
      default_model: 'gpt-4o-mini',
      auto_retry: false,
      streaming_sse: true,
    },
    server: {
      port: 8344,
      bind_address: '0.0.0.0',
    },
    data: {
      log_retention_days: 90,
      storage_path: '',
    },
    advanced: {
      debug_mode: false,
      experimental: false,
      http_proxy: 'system',
    },
  } as model.Settings
}

const settings = ref<model.Settings>(defaultSettings())
const selectedTheme = computed<'light' | 'dark' | 'auto'>(() => {
  const t = settings.value.appearance.theme
  if (t === 'light' || t === 'dark' || t === 'auto') return t
  return 'light'
})
const storagePath = computed(() => settings.value.data.storage_path || defaultStoragePath)

function loadSettings() {
  if (fetchedSettings.value) {
    settings.value = JSON.parse(JSON.stringify(fetchedSettings.value)) as model.Settings
    activeTheme.value = settings.value.appearance.theme as any
  }
  isDirty.value = false
}

function markSettingsDirty() {
  isDirty.value = true
}

async function saveChanges() {
  try {
    await api.saveSettings(settings.value)
    isDirty.value = false
    toast.push('设置已保存。服务端口与绑定地址的更改将在代理启动后生效。', 'success')
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  }
}

async function discardChanges() {
  await fetchSettings()
  loadSettings()
}

async function restoreDefaults() {
  if (!confirm('确定将常规设置恢复为默认值？')) return
  const defaults = defaultSettings()
  try {
    await api.saveSettings(defaults)
    settings.value = defaults
    isDirty.value = false
    activeTheme.value = defaults.appearance.theme as any
    toast.push('已恢复默认设置', 'success')
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  }
}

async function selectTheme(theme: 'light' | 'dark' | 'auto') {
  settings.value.appearance.theme = theme
  activeTheme.value = theme
  try {
    await api.saveSettings(settings.value)
    toast.push('主题已保存', 'success')
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  }
}

async function selectAccent(color: string) {
  settings.value.appearance.accent_color = color
  try {
    await api.saveSettings(settings.value)
    toast.push('强调色已保存', 'success')
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  }
}

function scrollToSection(id: string) {
  activeSection.value = id
  const el = document.getElementById(id)
  if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

function copyToClipboard(text: string) {
  if (navigator.clipboard) {
    navigator.clipboard.writeText(text).catch(() => {})
  } else {
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
  }
}

function handleCopyBtn(e: Event) {
  const btn = e.currentTarget as HTMLElement
  const row = btn.closest('.row-between')
  if (!row) return
  const textEl = row.querySelector('span[style*="color: var(--accent)"]')
  if (textEl) {
    const endpoint = (textEl as HTMLElement).textContent || ''
    copyToClipboard(endpoint)
  }
}

function copyStoragePath() {
  copyToClipboard(storagePath.value)
}

function openInFinder() {
  toast.push('暂未实现', 'warning')
}

function notImplemented() {
  toast.push('暂未实现', 'warning')
}

async function submitPasswordChange() {
  if (!oldPassword.value || !newPassword.value) {
    toast.push('请输入当前密码和新密码', 'warning')
    return
  }
  if (!confirm('确定修改主密码？')) return
  try {
    await api.changeMasterPassword(oldPassword.value, newPassword.value)
    showPasswordModal.value = false
    oldPassword.value = ''
    newPassword.value = ''
    toast.push('主密码已修改', 'success')
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  }
}

function closePasswordModal() {
  showPasswordModal.value = false
  oldPassword.value = ''
  newPassword.value = ''
}

onMounted(() => {
  if (gateState.value === 'ready') {
    void fetchSettings().then(loadSettings)
  }
})

watch(gateState, (s) => {
  if (s === 'ready') void fetchSettings().then(loadSettings)
})

watch(fetchedSettings, loadSettings, { once: true })
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">设置</h1>
      <span
        id="settings-status"
        :style="{ color: isDirty ? 'var(--warning)' : 'var(--positive)' }"
      >{{ isDirty ? '有未保存的更改' : '所有更改已保存' }}</span>
    </div>
    <div class="main-actions">
      <button
        class="btn btn-secondary"
        :disabled="!isDirty"
        :style="{ opacity: isDirty ? 1 : 0.45, cursor: isDirty ? 'pointer' : 'not-allowed' }"
        id="settings-discard"
        @click="discardChanges"
      >放弃更改</button>
      <button
        class="btn btn-primary"
        :disabled="!isDirty"
        :style="{ opacity: isDirty ? 1 : 0.45, cursor: isDirty ? 'pointer' : 'not-allowed' }"
        id="settings-save"
        @click="saveChanges"
      >保存更改</button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner">
      <div class="col-3-7">

        <!-- Section nav -->
        <aside style="position: sticky; top: 0; align-self: flex-start;">
          <nav class="stack-tight" style="padding: 4px 0;">
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'general' }"
              href="#general"
              @click.prevent="scrollToSection('general')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M12 1v6m0 10v6M4.22 4.22l4.24 4.24m7.07 7.07l4.24 4.24M1 12h6m10 0h6M4.22 19.78l4.24-4.24m7.07-7.07l4.24-4.24"/></svg>
              <span>常规</span>
            </a>
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'appearance' }"
              href="#appearance"
              @click.prevent="scrollToSection('appearance')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v1m0 16v1m-7.07-2.93l.7.7m12.74-.7l-.7.7M3 12h1m16 0h1M5.6 5.6l.7.7m11.4-.7l-.7.7M12 7a5 5 0 0 0-5 5c0 1.4.6 2.7 1.5 3.6.7.7 1.5 1.1 2.5 1.4h.5c.6 0 1 .4 1 1v.5c0 .6.4 1 1 1h.5c.6 0 1-.4 1-1V18c0-.6.4-1 1-1h.5c1-.3 1.8-.7 2.5-1.4.9-.9 1.5-2.2 1.5-3.6a5 5 0 0 0-5-5z"/></svg>
              <span>外观</span>
            </a>
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'routing' }"
              href="#routing"
              @click.prevent="scrollToSection('routing')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="6" r="2.5"/><circle cx="12" cy="18" r="2.5"/><path d="M8 7l8 0M7 8l4 8M17 8l-4 8"/></svg>
              <span>路由</span>
            </a>
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'server' }"
              href="#server"
              @click.prevent="scrollToSection('server')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="18" rx="2"/><path d="M9 9h6v6H9z"/></svg>
              <span>API 服务</span>
            </a>
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'data' }"
              href="#data"
              @click.prevent="scrollToSection('data')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v6a9 3 0 0 0 18 0V5M3 11v6a9 3 0 0 0 18 0v-6"/></svg>
              <span>数据</span>
            </a>
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'advanced' }"
              href="#advanced"
              @click.prevent="scrollToSection('advanced')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/></svg>
              <span>高级</span>
            </a>
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'about' }"
              href="#about"
              @click.prevent="scrollToSection('about')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v0M12 12v4"/></svg>
              <span>关于</span>
            </a>
          </nav>
        </aside>

        <!-- Section content -->
        <div class="stack-loose">
          <section class="card" id="general">
            <div class="section-head">
              <div>
                <div class="section-title">常规</div>
                <div class="section-sub">启动行为与系统集成</div>
              </div>
            </div>

            <div class="field">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">登录时启动</div>
                  <div class="field-help">macOS 登录后自动在后台启动 autoapi</div>
                </div>
                <label class="toggle"><input type="checkbox" v-model="settings.general.launch_at_login" @change="markSettingsDirty"><span class="toggle-slider"></span></label>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">启动时</div>
              <select class="select" style="max-width: 320px;" v-model="settings.general.startup_action" @change="markSettingsDirty">
                <option value="show_window">显示主窗口</option>
                <option value="minimize_menubar">最小化到菜单栏</option>
                <option value="no_window">不显示窗口</option>
              </select>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">菜单栏图标</div>
                  <div class="field-help">在 macOS 菜单栏显示快速访问入口</div>
                </div>
                <label class="toggle"><input type="checkbox" v-model="settings.general.menu_bar_item" @change="markSettingsDirty"><span class="toggle-slider"></span></label>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field" style="margin-bottom: 0;">
              <div class="field-label">关闭主窗口时</div>
              <select class="select" style="max-width: 320px;" v-model="settings.general.close_action" @change="markSettingsDirty">
                <option value="background">继续在后台运行</option>
                <option value="quit">退出 autoapi</option>
                <option value="ask">每次询问</option>
              </select>
            </div>

            <div class="h-divider" style="margin: 18px 0 14px;"></div>

            <div class="field" style="margin-bottom: 0;">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label" style="color: var(--negative);">恢复默认设置</div>
                  <div class="field-help">将本节所有选项重置为出厂默认值 · 不影响路由规则、密钥与日志</div>
                </div>
                <button class="btn" style="background: rgba(217, 48, 37, 0.08); color: var(--negative); font-size: 12.5px; padding: 5px 12px;" @click="restoreDefaults">恢复默认</button>
              </div>
            </div>
          </section>

          <section class="card" id="appearance">
            <div class="section-head">
              <div>
                <div class="section-title">外观</div>
                <div class="section-sub">主题与界面密度</div>
              </div>
            </div>

            <div class="field">
              <div class="field-label">主题</div>
              <div class="row" style="gap: 10px;">
                <label
                  class="theme-card"
                  :class="{ active: selectedTheme === 'light' }"
                  @click="selectTheme('light')"
                >
                  <div class="theme-preview" style="background: #f5f5f7;">
                    <div class="tp-side" style="background: #ececef;"></div>
                    <div class="tp-body">
                      <div class="tp-line" style="background: #1d1d1f;"></div>
                      <div class="tp-line short" style="background: #d2d2d7;"></div>
                      <div class="tp-line tiny" style="background: #d2d2d7;"></div>
                    </div>
                  </div>
                  <div style="font-size: 12.5px; font-weight: 500;">浅色</div>
                </label>
                <label
                  class="theme-card"
                  :class="{ active: selectedTheme === 'dark' }"
                  @click="selectTheme('dark')"
                >
                  <div class="theme-preview dark" style="background: #1d1d1f;">
                    <div class="tp-side" style="background: #000000;"></div>
                    <div class="tp-body">
                      <div class="tp-line" style="background: rgba(255,255,255,0.85);"></div>
                      <div class="tp-line short" style="background: rgba(255,255,255,0.18);"></div>
                      <div class="tp-line tiny" style="background: rgba(255,255,255,0.18);"></div>
                    </div>
                  </div>
                  <div style="font-size: 12.5px; font-weight: 500;">深色</div>
                </label>
                <label
                  class="theme-card"
                  :class="{ active: selectedTheme === 'auto' }"
                  @click="selectTheme('auto')"
                >
                  <div class="theme-preview split">
                    <div class="tp-side" style="background: #ececef; border-right: 1px solid rgba(0,0,0,0.08);"></div>
                    <div class="tp-body">
                      <div class="tp-line" style="background: #1d1d1f;"></div>
                      <div class="tp-line short" style="background: #d2d2d7;"></div>
                    </div>
                  </div>
                  <div style="font-size: 12.5px; font-weight: 500;">跟随系统</div>
                </label>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">界面密度</div>
              <div class="tabs">
                <button
                  class="tab"
                  :class="{ active: settings.appearance.density === 'compact' }"
                  @click="settings.appearance.density = 'compact'; markSettingsDirty()"
                >紧凑</button>
                <button
                  class="tab"
                  :class="{ active: settings.appearance.density === 'standard' }"
                  @click="settings.appearance.density = 'standard'; markSettingsDirty()"
                >标准</button>
                <button
                  class="tab"
                  :class="{ active: settings.appearance.density === 'loose' }"
                  @click="settings.appearance.density = 'loose'; markSettingsDirty()"
                >宽松</button>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field" style="margin-bottom: 0;">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">强调色</div>
                  <div class="field-help">用于高亮与选中态</div>
                </div>
                <div class="row" style="gap: 6px;">
                  <span
                    class="dot"
                    style="width: 18px; height: 18px; background: #0071e3; border-radius: 50%;"
                    :style="{ boxShadow: settings.appearance.accent_color === '#0071e3' ? '0 0 0 2px var(--surface), 0 0 0 3px var(--accent)' : undefined }"
                    @click="selectAccent('#0071e3')"
                  ></span>
                  <span
                    class="dot"
                    style="width: 18px; height: 18px; background: #10a37f; border-radius: 50%;"
                    :style="{ boxShadow: settings.appearance.accent_color === '#10a37f' ? '0 0 0 2px var(--surface), 0 0 0 3px var(--accent)' : undefined }"
                    @click="selectAccent('#10a37f')"
                  ></span>
                  <span
                    class="dot"
                    style="width: 18px; height: 18px; background: #d97757; border-radius: 50%;"
                    :style="{ boxShadow: settings.appearance.accent_color === '#d97757' ? '0 0 0 2px var(--surface), 0 0 0 3px var(--accent)' : undefined }"
                    @click="selectAccent('#d97757')"
                  ></span>
                  <span
                    class="dot"
                    style="width: 18px; height: 18px; background: #6e6e73; border-radius: 50%;"
                    :style="{ boxShadow: settings.appearance.accent_color === '#6e6e73' ? '0 0 0 2px var(--surface), 0 0 0 3px var(--accent)' : undefined }"
                    @click="selectAccent('#6e6e73')"
                  ></span>
                </div>
              </div>
            </div>
          </section>

          <section class="card" id="routing">
            <div class="section-head">
              <div>
                <div class="section-title">路由默认行为</div>
                <div class="section-sub">当请求不匹配任何规则时的兜底策略</div>
              </div>
            </div>

            <div class="field">
              <div class="field-label">默认 Provider</div>
              <select class="select" style="max-width: 320px;" v-model="settings.routing.default_provider_id" @change="markSettingsDirty">
                <option value="openai">OpenAI</option>
                <option value="anthropic">Anthropic</option>
                <option value="deepseek">DeepSeek</option>
                <option value="moonshot">Moonshot</option>
              </select>
            </div>
            <div class="field">
              <div class="field-label">默认模型</div>
              <select class="select" style="max-width: 320px;" v-model="settings.routing.default_model" @change="markSettingsDirty">
                <option value="gpt-4o-mini">gpt-4o-mini</option>
                <option value="gpt-4o">gpt-4o</option>
                <option value="claude-haiku-4-5">claude-haiku-4-5</option>
              </select>
            </div>
            <div class="h-divider"></div>
            <div class="field">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">自动重试失败请求</div>
                  <div class="field-help">429 / 5xx 时切换备用 Provider</div>
                </div>
                <label class="toggle"><input type="checkbox" v-model="settings.routing.auto_retry" @change="markSettingsDirty"><span class="toggle-slider"></span></label>
              </div>
            </div>
            <div class="h-divider"></div>
            <div class="field" style="margin-bottom: 0;">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">流式响应 (SSE)</div>
                  <div class="field-help">逐 Token 推送给客户端</div>
                </div>
                <label class="toggle"><input type="checkbox" v-model="settings.routing.streaming_sse" @change="markSettingsDirty"><span class="toggle-slider"></span></label>
              </div>
            </div>
          </section>

          <section class="card" id="server">
            <div class="section-head">
              <div>
                <div class="section-title">API 服务</div>
                <div class="section-sub">本地 HTTP / WebSocket 服务</div>
              </div>
            </div>

            <div class="field">
              <div class="field-label">服务端口</div>
              <input class="input mono" style="max-width: 160px;" type="number" v-model.number="settings.server.port" @input="markSettingsDirty">
              <div class="field-help">修改后需重启服务</div>
            </div>
            <div class="h-divider"></div>
            <div class="field">
              <div class="field-label">绑定地址</div>
              <select class="select" style="max-width: 320px;" v-model="settings.server.bind_address" @change="markSettingsDirty">
                <option value="127.0.0.1">127.0.0.1 (仅本机)</option>
                <option value="0.0.0.0">0.0.0.0 (所有接口)</option>
              </select>
            </div>
            <div class="h-divider"></div>
            <div class="field" style="margin-bottom: 0;">
              <div class="field-label">API 端点</div>
              <div class="text-mono" style="background: var(--bg); padding: 10px 12px; border-radius: 8px; font-size: 12.5px; line-height: 1.7;">
                <div class="row-between" style="padding: 2px 0;">
                  <span><span style="color: var(--muted);">POST</span> <span style="color: var(--accent);">/v1/chat/completions</span></span>
                  <button class="copy-btn" title="复制端点" @click="handleCopyBtn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
                <div class="row-between" style="padding: 2px 0;">
                  <span><span style="color: var(--muted);">POST</span> <span style="color: var(--accent);">/v1/embeddings</span></span>
                  <button class="copy-btn" title="复制端点" @click="handleCopyBtn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
                <div class="row-between" style="padding: 2px 0;">
                  <span><span style="color: var(--muted);">GET&nbsp;&nbsp;</span><span style="color: var(--accent);">/v1/models</span></span>
                  <button class="copy-btn" title="复制端点" @click="handleCopyBtn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
                <div class="row-between" style="padding: 2px 0;">
                  <span><span style="color: var(--muted);">GET&nbsp;&nbsp;</span><span style="color: var(--accent);">/v1/stats/tokens</span></span>
                  <button class="copy-btn" title="复制端点" @click="handleCopyBtn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
                <div class="row-between" style="padding: 2px 0;">
                  <span><span style="color: var(--muted);">WS&nbsp;&nbsp;&nbsp;</span><span style="color: var(--accent);">/v1/stream</span></span>
                  <button class="copy-btn" title="复制端点" @click="handleCopyBtn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
              </div>
            </div>
          </section>

          <section class="card" id="data">
            <div class="section-head">
              <div>
                <div class="section-title">数据</div>
                <div class="section-sub">导入、导出与本地存储</div>
              </div>
            </div>

            <div class="field">
              <div class="field-label">导出</div>
              <div class="row" style="gap: 8px; flex-wrap: wrap;">
                <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" @click="download('all_json')">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/></svg>
                  全部数据 (.json)
                </button>
                <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" @click="download('settings_json')">仅设置 (.json)</button>
                <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" @click="download('tokens_csv')">Token 用量 (.csv)</button>
                <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" @click="download('logs_csv')">请求日志 (.csv)</button>
              </div>
              <div class="field-help">导出文件包含当前所有路由、密钥配置（密钥值脱敏）、用量与日志</div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">导入</div>
              <div class="row" style="gap: 8px;">
                <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" @click="notImplemented">从 .json 备份恢复</button>
              </div>
              <div class="field-help">导入将覆盖现有设置，建议先导出当前数据</div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">自动清理</div>
              <div class="row" style="gap: 12px;">
                <span class="text-muted" style="font-size: 12.5px;">保留请求日志</span>
                <select class="select" style="width: auto; padding: 5px 10px; font-size: 12.5px;" v-model.number="settings.data.log_retention_days" @change="markSettingsDirty">
                  <option :value="30">30 天</option>
                  <option :value="60">60 天</option>
                  <option :value="90">90 天</option>
                  <option :value="180">180 天</option>
                  <option :value="0">永不过期</option>
                </select>
              </div>
              <div class="field-help">过期日志将在每日凌晨 03:00 自动清理 · 当前占用 142 MB</div>
            </div>
            <div class="h-divider"></div>

            <div class="field" style="margin-bottom: 0;">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">数据存储位置</div>
                  <div class="text-mono field-help">{{ storagePath }}</div>
                </div>
                <div class="row" style="gap: 6px;">
                  <button class="btn btn-secondary" style="font-size: 12px; padding: 4px 10px;" @click="copyStoragePath">复制路径</button>
                  <button class="btn btn-secondary" style="font-size: 12px; padding: 4px 10px;" @click="openInFinder">在 Finder 中显示</button>
                </div>
              </div>
            </div>
          </section>

          <section class="card" id="advanced">
            <div class="section-head">
              <div>
                <div class="section-title">高级</div>
                <div class="section-sub">调试、代理与开发者选项</div>
              </div>
            </div>

            <div class="field">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">调试模式</div>
                  <div class="field-help">输出详细日志到控制台 · 启用后服务需重启</div>
                </div>
                <label class="toggle"><input type="checkbox" v-model="settings.advanced.debug_mode" @change="markSettingsDirty"><span class="toggle-slider"></span></label>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">实验性功能</div>
                  <div class="field-help">提前使用未稳定的能力 · 可能影响稳定性</div>
                </div>
                <label class="toggle"><input type="checkbox" v-model="settings.advanced.experimental" @change="markSettingsDirty"><span class="toggle-slider"></span></label>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">HTTP 代理</div>
              <select class="select" style="max-width: 320px;" v-model="settings.advanced.http_proxy" @change="markSettingsDirty">
                <option value="system">系统默认</option>
                <option value="none">不使用代理</option>
                <option value="manual">手动配置</option>
              </select>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">主密码</div>
                  <div class="field-help">修改本地加密主密码</div>
                </div>
                <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" @click="showPasswordModal = true">修改主密码</button>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field" style="margin-bottom: 0;">
              <div class="field-label">键盘快捷键</div>
              <div class="stack-tight" style="margin-top: 4px;">
                <div class="row-between" style="padding: 4px 0;">
                  <span style="font-size: 12.5px;">切换模块 1–7</span>
                  <span class="text-mono" style="font-size: 12px; color: var(--muted);"><span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">⌘</span> <span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">1</span> – <span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">7</span></span>
                </div>
                <div class="row-between" style="padding: 4px 0;">
                  <span style="font-size: 12.5px;">打开搜索</span>
                  <span class="text-mono" style="font-size: 12px; color: var(--muted);"><span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">⌘</span> <span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">K</span></span>
                </div>
                <div class="row-between" style="padding: 4px 0;">
                  <span style="font-size: 12.5px;">打开设置</span>
                  <span class="text-mono" style="font-size: 12px; color: var(--muted);"><span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">⌘</span> <span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">,</span></span>
                </div>
                <div class="row-between" style="padding: 4px 0;">
                  <span style="font-size: 12.5px;">新建路由规则</span>
                  <span class="text-mono" style="font-size: 12px; color: var(--muted);"><span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">⌘</span> <span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">N</span></span>
                </div>
                <div class="row-between" style="padding: 4px 0;">
                  <span style="font-size: 12.5px;">实时刷新日志</span>
                  <span class="text-mono" style="font-size: 12px; color: var(--muted);"><span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">⌘</span> <span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">R</span></span>
                </div>
              </div>
            </div>
          </section>

          <section class="card" id="about">
            <div class="section-head">
              <div>
                <div class="section-title">关于</div>
                <div class="section-sub">autoapi v0.4.2</div>
              </div>
            </div>

            <div class="row" style="gap: 16px; align-items: flex-start;">
              <div style="width: 56px; height: 56px; border-radius: 14px; background: var(--black); color: white; display: flex; align-items: center; justify-content: center; font-family: var(--font-display); font-size: 24px; font-weight: 700; flex-shrink: 0;">A</div>
              <div style="flex: 1;">
                <div style="font-size: 15px; font-weight: 600;">autoapi</div>
                <div class="text-muted" style="font-size: 12.5px; margin-top: 2px;">自研模型路由软件 · 个人使用</div>
                <div class="row" style="gap: 16px; margin-top: 12px; font-size: 12px; color: var(--muted);">
                  <span><span class="text-mono">v0.4.2</span> · 构建 20260518</span>
                  <span>macOS 14.0+</span>
                  <span>Apple Silicon 优化</span>
                </div>
              </div>
              <div class="row" style="gap: 8px; flex-shrink: 0;">
                <button class="btn btn-secondary" style="font-size: 12px; padding: 5px 12px;" @click="notImplemented">检查更新</button>
              </div>
            </div>

            <div class="h-divider" style="margin: 18px 0 14px;"></div>

            <div class="field" style="margin-bottom: 0;">
              <div class="field-label">最近更新</div>
              <div class="stack-tight" style="margin-top: 6px;">
                <div class="row" style="gap: 12px; padding: 6px 0;">
                  <span class="text-mono text-muted" style="width: 76px; flex-shrink: 0; font-size: 11.5px;">v0.4.2</span>
                  <div style="flex: 1; font-size: 12.5px;">新增设置面板 · 智能路由节省统计 · 30 日 Token 堆叠图</div>
                </div>
                <div class="row" style="gap: 12px; padding: 6px 0;">
                  <span class="text-mono text-muted" style="width: 76px; flex-shrink: 0; font-size: 11.5px;">v0.4.1</span>
                  <div style="flex: 1; font-size: 12.5px;">支持 Moonshot kimi-latest · 修复 Anthropic 401 抖动</div>
                </div>
                <div class="row" style="gap: 12px; padding: 6px 0;">
                  <span class="text-mono text-muted" style="width: 76px; flex-shrink: 0; font-size: 11.5px;">v0.4.0</span>
                  <div style="flex: 1; font-size: 12.5px;">路由规则重构 · 引入优先级 + 命中数 · SSE 流式响应</div>
                </div>
              </div>
              <button class="btn btn-ghost" style="font-size: 12px; padding: 4px 8px; margin-top: 8px;" @click="notImplemented">查看完整 Changelog →</button>
            </div>
          </section>
        </div>
      </div>
    </div>
  </div>

  <Teleport to="body">
    <div v-if="showPasswordModal" class="modal-overlay" @click.self="closePasswordModal">
      <div class="modal-card">
        <h3 class="modal-title">修改主密码</h3>
        <form class="stack" @submit.prevent="submitPasswordChange">
          <input v-model="oldPassword" type="password" class="input" placeholder="当前密码" />
          <input v-model="newPassword" type="password" class="input" placeholder="新密码" />
          <div class="row mt-8" style="justify-content: flex-end;">
            <button type="button" class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" @click="closePasswordModal">取消</button>
            <button type="submit" class="btn btn-primary" style="font-size: 12.5px; padding: 5px 12px;">修改</button>
          </div>
        </form>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.loading-overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(245, 245, 247, 0.78);
  backdrop-filter: blur(2px);
  z-index: 10;
  font-size: 14px;
  color: var(--muted, #6e6e73);
}
</style>
