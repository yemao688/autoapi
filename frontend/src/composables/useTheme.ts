import { ref, watch, onMounted, onUnmounted } from 'vue'
import { api } from '@/api/client'

export type ThemeValue = 'light' | 'dark' | 'system' | 'auto'

const activeTheme = ref<ThemeValue>('light')
let mediaListenerAdded = false
let mediaQuery: MediaQueryList | null = null

function resolveTheme(t: ThemeValue): 'light' | 'dark' {
  if (t === 'dark') return 'dark'
  if (t === 'light') return 'light'
  // system / auto
  if (typeof window !== 'undefined' && window.matchMedia) {
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  }
  return 'light'
}

function applyTheme() {
  if (typeof document === 'undefined') return
  document.documentElement.setAttribute('data-theme', resolveTheme(activeTheme.value))
}

watch(activeTheme, applyTheme, { immediate: true })

export function useTheme() {
  onMounted(() => {
    if (mediaListenerAdded) return
    mediaListenerAdded = true
    applyTheme()

    if (typeof window !== 'undefined' && window.matchMedia) {
      mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
      const handler = () => {
        if (activeTheme.value === 'system' || activeTheme.value === 'auto') {
          applyTheme()
        }
      }
      mediaQuery.addEventListener('change', handler)
      onUnmounted(() => {
        mediaQuery?.removeEventListener('change', handler)
      })
    }
  })

  async function loadFromSettings() {
    try {
      const s = await api.getSettings()
      const t = s.appearance.theme
      if (t === 'light' || t === 'dark' || t === 'system' || t === 'auto') {
        activeTheme.value = t as ThemeValue
      }
    } catch {
      // ignore
    }
  }

  return {
    activeTheme,
    loadFromSettings,
  }
}
