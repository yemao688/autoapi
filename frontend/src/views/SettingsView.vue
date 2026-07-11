<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { api as apiModels, model } from '../../wailsjs/go/models'
import { api } from '@/api/bridge'
import { useApi } from '@/composables/useApi'
import { useExportDownload } from '@/composables/useExportDownload'
import { useToast } from '@/composables/useToast'
import { useTheme } from '@/composables/useTheme'
import { LOCALE_STORAGE_KEY, SUPPORTED_LOCALES, type AppLocale } from '@/locales'
import i18n from '@/locales'

const { t, locale } = useI18n()
const { download } = useExportDownload()
const {
  loading,
  error: settingsLoadError,
  execute: fetchSettings,
} = useApi(api.getSettings)
const toast = useToast()
const { activeTheme, saveTheme } = useTheme()

const isDirty = ref(false)
const activeSection = ref('general')
const settingsLoaded = ref(false)
const runtimePaths = ref<apiModels.RuntimePaths | null>(null)
const runtimePathsLoading = ref(false)
const runtimePathsError = ref<string | null>(null)

// Proxy service status
const proxyRunning = ref<boolean | null>(null)
const proxyBusy = ref(false)

async function refreshProxyStatus() {
  try {
    const status = await api.proxyStatus()
    proxyRunning.value = status.running
  } catch {
    proxyRunning.value = null
  }
}

async function handleStartProxy() {
  proxyBusy.value = true
  try {
    await api.startProxy()
    await refreshProxyStatus()
    toast.push(t('settings.server.started'), 'success')
  } catch (e: any) {
    toast.push((e?.message || String(e)), 'error')
    await refreshProxyStatus()
  } finally {
    proxyBusy.value = false
  }
}

async function handleStopProxy() {
  proxyBusy.value = true
  try {
    await api.stopProxy()
    await refreshProxyStatus()
    toast.push(t('settings.server.stopped'), 'success')
  } catch (e: any) {
    toast.push((e?.message || String(e)), 'error')
    await refreshProxyStatus()
  } finally {
    proxyBusy.value = false
  }
}

async function handleRestartProxy() {
  proxyBusy.value = true
  try {
    await api.restartProxy()
    await refreshProxyStatus()
    toast.push(t('settings.server.restarted'), 'success')
  } catch (e: any) {
    toast.push((e?.message || String(e)), 'error')
    await refreshProxyStatus()
  } finally {
    proxyBusy.value = false
  }
}

function defaultSettings(): model.Settings {
  return {
    general: {
      launch_at_login: false,
      startup_action: 'show_window',
      menu_bar_item: true,
      close_action: 'background',
    },
    appearance: {
      theme: 'system',
      density: 'standard',
      accent_color: '#0071e3',
    },
    routing: {
      streaming_sse: true,
    },
    server: {
      port: 0,
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
    logging: {
      enabled: true,
      level: 'info',
      max_size_mb: 10,
      max_age_days: 7,
      max_backups: 3,
    },
  } as model.Settings
}

const settings = ref<model.Settings>(defaultSettings())
const selectedTheme = computed<'light' | 'dark' | 'system'>(() => {
  const theme = settings.value.appearance.theme
  if (theme === 'light' || theme === 'dark' || theme === 'system') return theme
  return 'system'
})
const storagePath = computed(() => settings.value.data.storage_path || '')
const logPath = computed(() => runtimePaths.value?.log_path || '')

// Language picker
const languageOptions: { value: AppLocale; label: string }[] = [
  { value: 'zh-CN', label: t('settings.general.languageZh') },
  { value: 'en-US', label: t('settings.general.languageEn') },
]
const currentLanguage = computed({
  get: () => (locale.value as AppLocale) || 'zh-CN',
  set: (value: AppLocale) => {
    if (!(SUPPORTED_LOCALES as string[]).includes(value)) return
    i18n.global.locale.value = value
    try {
      localStorage.setItem(LOCALE_STORAGE_KEY, value)
    } catch {
      // Ignore localStorage errors (private mode, quota, etc.)
    }
  },
})

function applySettings(value: model.Settings) {
  settings.value = JSON.parse(JSON.stringify(value)) as model.Settings
  normalizeLifecycleSettings(settings.value)
  activeTheme.value = settings.value.appearance.theme as any
  settingsLoaded.value = true
  isDirty.value = false
}

async function loadSettings() {
  settingsLoaded.value = false
  const value = await fetchSettings()
  if (value) applySettings(value)
}

async function resyncSettingsAfterFailure() {
  settingsLoaded.value = false
  const value = await fetchSettings()
  if (value) applySettings(value)
}

async function loadRuntimePaths() {
  runtimePathsLoading.value = true
  runtimePathsError.value = null
  try {
    runtimePaths.value = await api.runtimePaths()
  } catch (e: any) {
    runtimePaths.value = null
    runtimePathsError.value = e?.message || e?.toString() || t('settings.paths.loadFailed')
  } finally {
    runtimePathsLoading.value = false
  }
}

function markSettingsDirty() {
  if (!settingsLoaded.value) return
  isDirty.value = true
}

// Enforce cross-setting invariants: without a tray icon, the user has no
// way to restore a hidden window, so background close and hidden start are
// forced off. Also normalizes legacy startup_action values. The backend
// re-validates the same invariant in resolveLaunchConfig.
function normalizeLifecycleSettings(value: model.Settings) {
  const general = value.general
  if (general.startup_action === 'minimize_menubar' || general.startup_action === 'no_window') {
    general.startup_action = 'start_hidden'
  } else if (general.startup_action !== 'show_window' && general.startup_action !== 'start_hidden') {
    general.startup_action = 'show_window'
  }
  if (!general.menu_bar_item) {
    general.startup_action = 'show_window'
    general.close_action = 'quit'
  }
}

function onMenuBarItemChange() {
  normalizeLifecycleSettings(settings.value)
  markSettingsDirty()
}

async function saveChanges() {
  if (!settingsLoaded.value) return
  try {
    await api.saveSettings(settings.value)
    isDirty.value = false
    toast.push(t('toast.settingsSaved'), 'success')
  } catch (e: any) {
    await resyncSettingsAfterFailure()
    toast.push(t('toast.saveFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
  }
}

async function discardChanges() {
  if (!settingsLoaded.value) return
  await loadSettings()
}

async function restoreDefaults() {
  if (!settingsLoaded.value) return
  if (!confirm(t('confirm.restoreDefaultsMessage'))) return
  try {
    const defaults = await api.resetSettings()
    settings.value = defaults
    isDirty.value = false
    activeTheme.value = defaults.appearance.theme as any
    toast.push(t('toast.defaultsRestored'), 'success')
  } catch (e: any) {
    await resyncSettingsAfterFailure()
    toast.push(t('toast.saveFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
  }
}

async function selectTheme(theme: 'light' | 'dark' | 'system') {
  if (!settingsLoaded.value) return
  settings.value.appearance.theme = theme
  try {
    await saveTheme(theme)
  } catch (e: any) {
    await resyncSettingsAfterFailure()
    toast.push(t('toast.saveFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
  }
}

async function selectAccent(color: string) {
  if (!settingsLoaded.value) return
  settings.value.appearance.accent_color = color
  try {
    await api.saveSettings(settings.value)
    toast.push(t('toast.accentSaved'), 'success')
  } catch (e: any) {
    await resyncSettingsAfterFailure()
    toast.push(t('toast.saveFailed') + ': ' + (e?.message || e?.toString() || ''), 'error')
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
  if (!storagePath.value) return
  copyToClipboard(storagePath.value)
}

async function openInFinder() {
  try {
    await api.openStorageFolder()
  } catch (e: any) {
    toast.push(t('toast.openFolderFailed', { error: e?.message || e?.toString() || '' }), 'error')
  }
}

function notImplemented() {
  toast.push(t('settings.actions.notImplemented'), 'warning')
}

onMounted(() => {
  void loadSettings()
  void loadRuntimePaths()
  void refreshProxyStatus()
})

watch(activeTheme, (t) => {
  if (settings.value.appearance.theme !== t) settings.value.appearance.theme = t as any
})

</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">{{ t('settings.title') }}</h1>
      <span
        id="settings-status"
        :style="{ color: !settingsLoaded ? 'var(--muted)' : isDirty ? 'var(--warning)' : 'var(--positive)' }"
      >{{ loading ? t('settings.status.loading') : !settingsLoaded ? t('settings.status.loadFailed') : isDirty ? t('settings.status.unsaved') : t('settings.status.saved') }}</span>
    </div>
    <div class="main-actions">
      <button
        class="btn btn-secondary"
        :disabled="!settingsLoaded || !isDirty"
        :style="{ opacity: isDirty ? 1 : 0.45, cursor: isDirty ? 'pointer' : 'not-allowed' }"
        id="settings-discard"
        @click="discardChanges"
      >{{ t('settings.actions.discard') }}</button>
      <button
        class="btn btn-primary"
        :disabled="!settingsLoaded || !isDirty"
        :style="{ opacity: isDirty ? 1 : 0.45, cursor: isDirty ? 'pointer' : 'not-allowed' }"
        id="settings-save"
        @click="saveChanges"
      >{{ t('settings.actions.save') }}</button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner">
      <div class="col-3-7">

        <!-- Section nav -->
        <aside class="settings-nav-aside">
          <nav class="stack-tight" style="padding: 4px 0;">
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'general' }"
              href="#general"
              @click.prevent="scrollToSection('general')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M12 1v6m0 10v6M4.22 4.22l4.24 4.24m7.07 7.07l4.24 4.24M1 12h6m10 0h6M4.22 19.78l4.24-4.24m7.07-7.07l4.24-4.24"/></svg>
              <span>{{ t('settings.sections.general') }}</span>
            </a>
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'appearance' }"
              href="#appearance"
              @click.prevent="scrollToSection('appearance')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v1m0 16v1m-7.07-2.93l.7.7m12.74-.7l-.7.7M3 12h1m16 0h1M5.6 5.6l.7.7m11.4-.7l-.7.7M12 7a5 5 0 0 0-5 5c0 1.4.6 2.7 1.5 3.6.7.7 1.5 1.1 2.5 1.4h.5c.6 0 1 .4 1 1v.5c0 .6.4 1 1 1h.5c.6 0 1-.4 1-1V18c0-.6.4-1 1-1h.5c1-.3 1.8-.7 2.5-1.4.9-.9 1.5-2.2 1.5-3.6a5 5 0 0 0-5-5z"/></svg>
              <span>{{ t('settings.sections.appearance') }}</span>
            </a>
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'routing' }"
              href="#routing"
              @click.prevent="scrollToSection('routing')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="6" r="2.5"/><circle cx="12" cy="18" r="2.5"/><path d="M8 7l8 0M7 8l4 8M17 8l-4 8"/></svg>
              <span>{{ t('settings.sections.routing') }}</span>
            </a>
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'server' }"
              href="#server"
              @click.prevent="scrollToSection('server')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="18" rx="2"/><path d="M9 9h6v6H9z"/></svg>
              <span>{{ t('settings.sections.server') }}</span>
            </a>
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'data' }"
              href="#data"
              @click.prevent="scrollToSection('data')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v6a9 3 0 0 0 18 0V5M3 11v6a9 3 0 0 0 18 0v-6"/></svg>
              <span>{{ t('settings.sections.data') }}</span>
            </a>
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'advanced' }"
              href="#advanced"
              @click.prevent="scrollToSection('advanced')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/></svg>
              <span>{{ t('settings.sections.advanced') }}</span>
            </a>
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'logging' }"
              href="#logging"
              @click.prevent="scrollToSection('logging')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M14 3v4a1 1 0 0 0 1 1h4"/><path d="M17 21H7a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h7l5 5v11a2 2 0 0 1-2 2z"/><path d="M9 13h6M9 17h6M9 9h2"/></svg>
              <span>{{ t('settings.sections.logging') }}</span>
            </a>
            <a
              class="sub-nav-item"
              :class="{ active: activeSection === 'about' }"
              href="#about"
              @click.prevent="scrollToSection('about')"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 8v0M12 12v4"/></svg>
              <span>{{ t('settings.sections.about') }}</span>
            </a>
          </nav>
        </aside>

        <!-- Section content -->
        <div v-if="!settingsLoaded" class="settings-load-state">
          <span>{{ settingsLoadError || t('settings.status.loading') }}</span>
          <button v-if="settingsLoadError" class="btn btn-secondary" @click="loadSettings">{{ t('settings.actions.retry') }}</button>
        </div>
        <fieldset v-else class="settings-fields" :disabled="loading">
        <div class="stack-loose">
          <section class="card" id="general">
            <div class="section-head">
              <div>
                <div class="section-title">{{ t('settings.general.title') }}</div>
                <div class="section-sub">{{ t('settings.general.subtitle') }}</div>
              </div>
            </div>

            <div class="field">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">{{ t('settings.general.language') }}</div>
                  <div class="field-help">{{ t('settings.general.languageHelp') }}</div>
                </div>
                <select
                  class="select"
                  style="max-width: 240px;"
                  v-model="currentLanguage"
                >
                  <option v-for="opt in languageOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
                </select>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">{{ t('settings.general.launchAtLogin') }}</div>
                  <div class="field-help">{{ t('settings.general.launchAtLoginHelp') }}</div>
                </div>
                <label class="toggle"><input type="checkbox" v-model="settings.general.launch_at_login" @change="markSettingsDirty"><span class="toggle-slider"></span></label>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">{{ t('settings.general.startupAction') }}</div>
              <select class="select" style="max-width: 320px;" v-model="settings.general.startup_action" @change="markSettingsDirty" :disabled="!settings.general.menu_bar_item">
                <option value="show_window">{{ t('settings.general.startupShowWindow') }}</option>
                <option value="start_hidden">{{ t('settings.general.startupHidden') }}</option>
              </select>
              <div v-if="!settings.general.menu_bar_item" class="field-help">{{ t('settings.general.startupDisabledNoTray') }}</div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">{{ t('settings.general.menuBarItem') }}</div>
                  <div class="field-help">{{ t('settings.general.menuBarItemHelp') }}</div>
                </div>
                <label class="toggle"><input type="checkbox" v-model="settings.general.menu_bar_item" @change="onMenuBarItemChange"><span class="toggle-slider"></span></label>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field" style="margin-bottom: 0;">
              <div class="field-label">{{ t('settings.general.closeAction') }}</div>
              <select class="select" style="max-width: 320px;" v-model="settings.general.close_action" @change="markSettingsDirty" :disabled="!settings.general.menu_bar_item">
                <option value="background">{{ t('settings.general.closeBackground') }}</option>
                <option value="quit">{{ t('settings.general.closeQuit') }}</option>
              </select>
              <div v-if="!settings.general.menu_bar_item" class="field-help">{{ t('settings.general.closeDisabledNoTray') }}</div>
            </div>

            <div class="field-help" style="margin-top: 14px;">{{ t('settings.general.lifecycleRestartHint') }}</div>

            <div class="h-divider" style="margin: 18px 0 14px;"></div>

            <div class="field" style="margin-bottom: 0;">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label" style="color: var(--negative);">{{ t('settings.general.restoreDefaultsLabel') }}</div>
                  <div class="field-help">{{ t('settings.general.restoreDefaultsHelp') }}</div>
                </div>
                <button class="btn" style="background: rgba(217, 48, 37, 0.08); color: var(--negative); font-size: 12.5px; padding: 5px 12px;" @click="restoreDefaults">{{ t('settings.actions.restoreDefaults') }}</button>
              </div>
            </div>
          </section>

          <section class="card" id="appearance">
            <div class="section-head">
              <div>
                <div class="section-title">{{ t('settings.appearance.title') }}</div>
                <div class="section-sub">{{ t('settings.appearance.subtitle') }}</div>
              </div>
            </div>

            <div class="field">
              <div class="field-label">{{ t('settings.appearance.theme') }}</div>
              <div class="row" style="gap: 10px;">
                <label
                  class="theme-card"
                  :class="{ active: selectedTheme === 'system' }"
                  @click="selectTheme('system')"
                >
                  <div class="theme-preview split">
                    <div class="tp-side" style="background: #ececef; border-right: 1px solid rgba(0,0,0,0.08);"></div>
                    <div class="tp-body">
                      <div class="tp-line" style="background: #1d1d1f;"></div>
                      <div class="tp-line short" style="background: #d2d2d7;"></div>
                    </div>
                  </div>
                  <div style="font-size: 12.5px; font-weight: 500;">{{ t('settings.appearance.themeSystem') }}</div>
                </label>
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
                  <div style="font-size: 12.5px; font-weight: 500;">{{ t('settings.appearance.themeLight') }}</div>
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
                  <div style="font-size: 12.5px; font-weight: 500;">{{ t('settings.appearance.themeDark') }}</div>
                </label>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">{{ t('settings.appearance.density') }}</div>
              <div class="tabs">
                <button
                  class="tab"
                  :class="{ active: settings.appearance.density === 'compact' }"
                  @click="settings.appearance.density = 'compact'; markSettingsDirty()"
                >{{ t('settings.appearance.densityCompact') }}</button>
                <button
                  class="tab"
                  :class="{ active: settings.appearance.density === 'standard' }"
                  @click="settings.appearance.density = 'standard'; markSettingsDirty()"
                >{{ t('settings.appearance.densityStandard') }}</button>
                <button
                  class="tab"
                  :class="{ active: settings.appearance.density === 'loose' }"
                  @click="settings.appearance.density = 'loose'; markSettingsDirty()"
                >{{ t('settings.appearance.densityLoose') }}</button>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field" style="margin-bottom: 0;">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">{{ t('settings.appearance.accentColor') }}</div>
                  <div class="field-help">{{ t('settings.appearance.accentColorHelp') }}</div>
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
                <div class="section-title">{{ t('settings.routing.title') }}</div>
                <div class="section-sub">{{ t('settings.routing.subtitle') }}</div>
              </div>
            </div>

            <div class="field" style="margin-bottom: 0;">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">{{ t('settings.routing.streamingSse') }}</div>
                  <div class="field-help">{{ t('settings.routing.streamingSseHelp') }}</div>
                </div>
                <label class="toggle"><input type="checkbox" v-model="settings.routing.streaming_sse" @change="markSettingsDirty"><span class="toggle-slider"></span></label>
              </div>
            </div>
          </section>

          <section class="card" id="server">
            <div class="section-head">
              <div>
                <div class="section-title">{{ t('settings.server.title') }}</div>
                <div class="section-sub">{{ t('settings.server.subtitle') }}</div>
              </div>
            </div>

            <div class="field">
              <div class="row-between" style="flex-wrap: wrap; gap: 12px;">
                <div>
                  <div class="field-label">{{ t('settings.server.serviceStatus') }}</div>
                  <div class="row" style="gap: 6px; align-items: center; margin-top: 4px;">
                    <span class="dot" :class="proxyRunning ? 'green' : 'red'"></span>
                    <span v-if="proxyRunning === null" class="text-muted" style="font-size: 13px;">{{ t('settings.server.statusUnknown') }}</span>
                    <span v-else-if="proxyRunning" style="font-size: 13px; font-weight: 500;">{{ t('settings.server.statusRunning') }}</span>
                    <span v-else style="font-size: 13px; font-weight: 500; color: var(--negative);">{{ t('settings.server.statusStopped') }}</span>
                  </div>
                </div>
                <div class="row" style="gap: 8px;">
                  <button
                    v-if="!proxyRunning"
                    class="btn btn-primary"
                    style="font-size: 12.5px; padding: 5px 14px;"
                    :disabled="proxyBusy || isDirty"
                    :title="isDirty ? t('settings.server.saveFirst') : ''"
                    @click="handleStartProxy"
                  >
                    <svg viewBox="0 0 24 24" fill="currentColor" style="width:13px;height:13px;"><path d="M8 5v14l11-7z"/></svg>
                    {{ t('settings.server.start') }}
                  </button>
                  <button
                    v-else
                    class="btn btn-secondary"
                    style="font-size: 12.5px; padding: 5px 14px;"
                    :disabled="proxyBusy || isDirty"
                    :title="isDirty ? t('settings.server.saveFirst') : ''"
                    @click="handleStopProxy"
                  >
                    <svg viewBox="0 0 24 24" fill="currentColor" style="width:13px;height:13px;"><rect x="6" y="6" width="12" height="12" rx="1"/></svg>
                    {{ t('settings.server.stop') }}
                  </button>
                  <button
                    v-if="proxyRunning"
                    class="btn btn-secondary"
                    style="font-size: 12.5px; padding: 5px 14px;"
                    :disabled="proxyBusy || isDirty"
                    :title="isDirty ? t('settings.server.saveFirst') : ''"
                    @click="handleRestartProxy"
                  >
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;"><path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/></svg>
                    {{ t('settings.server.restart') }}
                  </button>
                </div>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">{{ t('settings.server.port') }}</div>
              <input class="input mono" style="max-width: 160px;" type="number" v-model.number="settings.server.port" @input="markSettingsDirty">
              <div class="field-help">{{ t('settings.server.portHelp') }}</div>
            </div>
            <div class="h-divider"></div>
            <div class="field">
              <div class="field-label">{{ t('settings.server.bindAddress') }}</div>
              <select class="select" style="max-width: 320px;" v-model="settings.server.bind_address" @change="markSettingsDirty">
                <option value="127.0.0.1">{{ t('settings.server.bindLocal') }}</option>
                <option value="0.0.0.0">{{ t('settings.server.bindAll') }}</option>
              </select>
            </div>
            <div class="h-divider"></div>
            <div class="field" style="margin-bottom: 0;">
              <div class="field-label">{{ t('settings.server.apiEndpoints') }}</div>
              <div class="text-mono" style="background: var(--bg); padding: 10px 12px; border-radius: 8px; font-size: 12.5px; line-height: 1.7;">
                <div class="row-between" style="padding: 2px 0;">
                  <span><span style="color: var(--muted);">POST</span> <span style="color: var(--accent);">/v1/chat/completions</span></span>
                  <button class="copy-btn" :title="t('settings.server.copyEndpoint')" @click="handleCopyBtn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
                <div class="row-between" style="padding: 2px 0;">
                  <span><span style="color: var(--muted);">POST</span> <span style="color: var(--accent);">/v1/embeddings</span></span>
                  <button class="copy-btn" :title="t('settings.server.copyEndpoint')" @click="handleCopyBtn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
                <div class="row-between" style="padding: 2px 0;">
                  <span><span style="color: var(--muted);">GET&nbsp;&nbsp;</span><span style="color: var(--accent);">/v1/models</span></span>
                  <button class="copy-btn" :title="t('settings.server.copyEndpoint')" @click="handleCopyBtn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
                <div class="row-between" style="padding: 2px 0;">
                  <span><span style="color: var(--muted);">GET&nbsp;&nbsp;</span><span style="color: var(--accent);">/v1/stats/tokens</span></span>
                  <button class="copy-btn" :title="t('settings.server.copyEndpoint')" @click="handleCopyBtn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
                <div class="row-between" style="padding: 2px 0;">
                  <span><span style="color: var(--muted);">WS&nbsp;&nbsp;&nbsp;</span><span style="color: var(--accent);">/v1/stream</span></span>
                  <button class="copy-btn" :title="t('settings.server.copyEndpoint')" @click="handleCopyBtn"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
              </div>
            </div>
          </section>

          <section class="card" id="data">
            <div class="section-head">
              <div>
                <div class="section-title">{{ t('settings.data.title') }}</div>
                <div class="section-sub">{{ t('settings.data.subtitle') }}</div>
              </div>
            </div>

            <div class="field">
              <div class="field-label">{{ t('settings.data.export') }}</div>
              <div class="row" style="gap: 8px; flex-wrap: wrap;">
                <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" @click="download('all_json')">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/></svg>
                  {{ t('settings.data.exportAll') }}
                </button>
                <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" @click="download('settings_json')">{{ t('settings.data.exportSettings') }}</button>
                <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" @click="download('tokens_csv')">{{ t('settings.data.exportTokens') }}</button>
                <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" @click="download('logs_csv')">{{ t('settings.data.exportLogs') }}</button>
              </div>
              <div class="field-help">{{ t('settings.data.exportHelp') }}</div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">{{ t('settings.data.import') }}</div>
              <div class="row" style="gap: 8px;">
                <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" @click="notImplemented">{{ t('settings.data.importBackup') }}</button>
              </div>
              <div class="field-help">{{ t('settings.data.importHelp') }}</div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">{{ t('settings.data.autoCleanup') }}</div>
              <div class="row" style="gap: 12px;">
                <span class="text-muted" style="font-size: 12.5px;">{{ t('settings.data.retentionLabel') }}</span>
                <select class="select" style="width: auto; padding: 5px 10px; font-size: 12.5px;" v-model.number="settings.data.log_retention_days" @change="markSettingsDirty">
                  <option :value="30">{{ t('settings.data.retentionDays', { days: 30 }) }}</option>
                  <option :value="60">{{ t('settings.data.retentionDays', { days: 60 }) }}</option>
                  <option :value="90">{{ t('settings.data.retentionDays', { days: 90 }) }}</option>
                  <option :value="180">{{ t('settings.data.retentionDays', { days: 180 }) }}</option>
                  <option :value="0">{{ t('settings.data.retentionNever') }}</option>
                </select>
              </div>
              <div class="field-help">{{ t('settings.data.retentionHelp', { size: 142 }) }}</div>
            </div>
            <div class="h-divider"></div>

            <div class="field" style="margin-bottom: 0;">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">{{ t('settings.data.storagePath') }}</div>
                  <div class="text-mono field-help">{{ storagePath }}</div>
                </div>
                <div class="row" style="gap: 6px;">
                  <button class="btn btn-secondary" style="font-size: 12px; padding: 4px 10px;" @click="copyStoragePath">{{ t('settings.data.copyPath') }}</button>
                  <button class="btn btn-secondary" style="font-size: 12px; padding: 4px 10px;" @click="openInFinder">{{ t('settings.data.openInFinder') }}</button>
                </div>
              </div>
            </div>
          </section>

          <section class="card" id="advanced">
            <div class="section-head">
              <div>
                <div class="section-title">{{ t('settings.advanced.title') }}</div>
                <div class="section-sub">{{ t('settings.advanced.subtitle') }}</div>
              </div>
            </div>

            <div class="field">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">{{ t('settings.advanced.debugMode') }}</div>
                  <div class="field-help">{{ t('settings.advanced.debugModeHelp') }}</div>
                </div>
                <label class="toggle"><input type="checkbox" v-model="settings.advanced.debug_mode" @change="markSettingsDirty"><span class="toggle-slider"></span></label>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">{{ t('settings.advanced.experimental') }}</div>
                  <div class="field-help">{{ t('settings.advanced.experimentalHelp') }}</div>
                </div>
                <label class="toggle"><input type="checkbox" v-model="settings.advanced.experimental" @change="markSettingsDirty"><span class="toggle-slider"></span></label>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">{{ t('settings.advanced.httpProxy') }}</div>
              <select class="select" style="max-width: 320px;" v-model="settings.advanced.http_proxy" @change="markSettingsDirty">
                <option value="system">{{ t('settings.advanced.httpProxySystem') }}</option>
                <option value="none">{{ t('settings.advanced.httpProxyNone') }}</option>
                <option value="manual">{{ t('settings.advanced.httpProxyManual') }}</option>
              </select>
            </div>
            <div class="h-divider"></div>

            <div class="field" style="margin-bottom: 0;">
              <div class="field-label">{{ t('settings.advanced.shortcuts') }}</div>
              <div class="stack-tight" style="margin-top: 4px;">
                <div class="row-between" style="padding: 4px 0;">
                  <span style="font-size: 12.5px;">{{ t('settings.advanced.shortcutSwitch') }}</span>
                  <span class="text-mono" style="font-size: 12px; color: var(--muted);"><span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">⌘</span> <span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">1</span> – <span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">7</span></span>
                </div>
                <div class="row-between" style="padding: 4px 0;">
                  <span style="font-size: 12.5px;">{{ t('settings.advanced.shortcutSearch') }}</span>
                  <span class="text-mono" style="font-size: 12px; color: var(--muted);"><span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">⌘</span> <span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">K</span></span>
                </div>
                <div class="row-between" style="padding: 4px 0;">
                  <span style="font-size: 12.5px;">{{ t('settings.advanced.shortcutSettings') }}</span>
                  <span class="text-mono" style="font-size: 12px; color: var(--muted);"><span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">⌘</span> <span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">,</span></span>
                </div>
                <div class="row-between" style="padding: 4px 0;">
                  <span style="font-size: 12.5px;">{{ t('settings.advanced.shortcutNewRoute') }}</span>
                  <span class="text-mono" style="font-size: 12px; color: var(--muted);"><span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">⌘</span> <span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">N</span></span>
                </div>
                <div class="row-between" style="padding: 4px 0;">
                  <span style="font-size: 12.5px;">{{ t('settings.advanced.shortcutRefresh') }}</span>
                  <span class="text-mono" style="font-size: 12px; color: var(--muted);"><span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">⌘</span> <span style="background: rgba(0,0,0,0.05); padding: 1px 6px; border-radius: 4px; border: 1px solid rgba(0,0,0,0.08);">R</span></span>
                </div>
              </div>
            </div>
          </section>

          <section class="card" id="logging">
            <div class="section-head">
              <div>
                <div class="section-title">{{ t('settings.logging.title') }}</div>
                <div class="section-sub">{{ t('settings.logging.subtitle') }}</div>
              </div>
            </div>

            <div class="field">
              <div class="row-between" style="margin-bottom: 0;">
                <div>
                  <div class="field-label">{{ t('settings.logging.enabled') }}</div>
                  <div v-if="logPath" class="field-help text-mono" style="font-size: 11.5px;">{{ logPath }}</div>
                  <div v-else class="field-help" style="font-size: 11.5px;">
                    <span>{{ runtimePathsLoading ? t('settings.status.loading') : t('settings.paths.loadFailed') }}</span>
                    <button v-if="runtimePathsError" class="btn btn-ghost" style="font-size: 11.5px; padding: 2px 6px;" @click="loadRuntimePaths">{{ t('settings.actions.retry') }}</button>
                  </div>
                </div>
                <label class="toggle"><input type="checkbox" v-model="settings.logging.enabled" @change="markSettingsDirty"><span class="toggle-slider"></span></label>
              </div>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">{{ t('settings.logging.level') }}</div>
              <select class="select" style="max-width: 320px;" v-model="settings.logging.level" @change="markSettingsDirty">
                <option value="error">{{ t('settings.logging.levelError') }}</option>
                <option value="warn">{{ t('settings.logging.levelWarn') }}</option>
                <option value="info">{{ t('settings.logging.levelInfo') }}</option>
                <option value="debug">{{ t('settings.logging.levelDebug') }}</option>
                <option value="trace">{{ t('settings.logging.levelTrace') }}</option>
              </select>
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">{{ t('settings.logging.maxSizeMB') }}</div>
              <input class="input mono" style="max-width: 160px;" type="number" min="1" v-model.number="settings.logging.max_size_mb" @input="markSettingsDirty">
            </div>
            <div class="h-divider"></div>

            <div class="field">
              <div class="field-label">{{ t('settings.logging.maxAgeDays') }}</div>
              <input class="input mono" style="max-width: 160px;" type="number" min="1" v-model.number="settings.logging.max_age_days" @input="markSettingsDirty">
            </div>
            <div class="h-divider"></div>

            <div class="field" style="margin-bottom: 0;">
              <div class="field-label">{{ t('settings.logging.maxBackups') }}</div>
              <input class="input mono" style="max-width: 160px;" type="number" min="0" v-model.number="settings.logging.max_backups" @input="markSettingsDirty">
            </div>
          </section>

          <section class="card" id="about">
            <div class="section-head">
              <div>
                <div class="section-title">{{ t('settings.about.title') }}</div>
                <div class="section-sub">{{ t('settings.about.subtitle', { version: '0.4.2' }) }}</div>
              </div>
            </div>

            <div class="row" style="gap: 16px; align-items: flex-start;">
              <div style="width: 56px; height: 56px; border-radius: 14px; background: var(--black); color: white; display: flex; align-items: center; justify-content: center; font-family: var(--font-display); font-size: 24px; font-weight: 700; flex-shrink: 0;">A</div>
              <div style="flex: 1;">
                <div style="font-size: 15px; font-weight: 600;">{{ t('app.tagline') }}</div>
                <div class="text-muted" style="font-size: 12.5px; margin-top: 2px;">{{ t('settings.about.tagline') }}</div>
                <div class="row" style="gap: 16px; margin-top: 12px; font-size: 12px; color: var(--muted);">
                  <span>{{ t('settings.about.versionLine', { version: '0.4.2', build: '20260518' }) }}</span>
                  <span>{{ t('settings.about.platform') }}</span>
                  <span>{{ t('settings.about.arch') }}</span>
                </div>
              </div>
              <div class="row" style="gap: 8px; flex-shrink: 0;">
                <button class="btn btn-secondary" style="font-size: 12px; padding: 5px 12px;" @click="notImplemented">{{ t('settings.about.checkUpdate') }}</button>
              </div>
            </div>

            <div class="h-divider" style="margin: 18px 0 14px;"></div>

            <div class="field" style="margin-bottom: 0;">
              <div class="field-label">{{ t('settings.about.recentUpdates') }}</div>
              <div class="stack-tight" style="margin-top: 6px;">
                <div class="row" style="gap: 12px; padding: 6px 0;">
                  <span class="text-mono text-muted" style="width: 76px; flex-shrink: 0; font-size: 11.5px;">v0.4.2</span>
                  <div style="flex: 1; font-size: 12.5px;">{{ t('settings.about.updates.v0.4.2') }}</div>
                </div>
                <div class="row" style="gap: 12px; padding: 6px 0;">
                  <span class="text-mono text-muted" style="width: 76px; flex-shrink: 0; font-size: 11.5px;">v0.4.1</span>
                  <div style="flex: 1; font-size: 12.5px;">{{ t('settings.about.updates.v0.4.1') }}</div>
                </div>
                <div class="row" style="gap: 12px; padding: 6px 0;">
                  <span class="text-mono text-muted" style="width: 76px; flex-shrink: 0; font-size: 11.5px;">v0.4.0</span>
                  <div style="flex: 1; font-size: 12.5px;">{{ t('settings.about.updates.v0.4.0') }}</div>
                </div>
              </div>
              <button class="btn btn-ghost" style="font-size: 12px; padding: 4px 8px; margin-top: 8px;" @click="notImplemented">{{ t('settings.about.viewChangelog') }}</button>
            </div>
          </section>
        </div>
        </fieldset>
      </div>
    </div>
  </div>

</template>

<style scoped>
.settings-fields {
  min-width: 0;
  margin: 0;
  padding: 0;
  border: 0;
}

.settings-load-state {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  color: var(--muted);
  font-size: 12.5px;
}
</style>
