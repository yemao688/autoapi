import { ref, watch, onMounted } from 'vue'
import { api } from '@/api/bridge'

export type ThemeValue = 'light' | 'dark' | 'system'

const activeTheme = ref<ThemeValue>('system')
let mediaQuery: MediaQueryList | null = null

function resolveTheme(t: ThemeValue): 'light' | 'dark' {
  if (t === 'dark') return 'dark'
  if (t === 'light') return 'light'
  // system
  if (mediaQuery) {
    return mediaQuery.matches ? 'dark' : 'light'
  }
  return 'light'
}

function applyTheme() {
  if (typeof document === 'undefined') return
  document.documentElement.setAttribute('data-theme', resolveTheme(activeTheme.value))
}

watch(activeTheme, applyTheme, { immediate: true })

if (typeof window !== 'undefined' && window.matchMedia) {
  mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
  const handler = () => {
    if (activeTheme.value === 'system') {
      applyTheme()
    }
  }
  mediaQuery.addEventListener('change', handler)
}

export function useTheme() {
  onMounted(() => {
    applyTheme()
  })

  async function loadFromSettings() {
    try {
      const s = await api.getSettings()
      const t = s.appearance.theme
      if (t === 'light' || t === 'dark' || t === 'system') {
        activeTheme.value = t as ThemeValue
      }
    } catch {
      // ignore
    }
  }

  async function saveTheme(theme: ThemeValue) {
	activeTheme.value = theme
	const s = await api.getSettings()
	s.appearance.theme = theme
	await api.saveSettings(s)
  }

  return {
    activeTheme,
    loadFromSettings,
    saveTheme,
  }
}
