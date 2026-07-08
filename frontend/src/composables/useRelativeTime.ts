import { onMounted, onUnmounted, ref } from 'vue'
import i18n from '@/locales'

export function useRelativeTime() {
  const tick = ref(0)
  let timer: ReturnType<typeof setInterval> | null = null

  function format(timestamp: number | string | undefined): string {
    if (!timestamp) return '—'
    const ts = typeof timestamp === 'string' ? parseInt(timestamp, 10) : timestamp
    const now = Date.now()
    const diff = now - ts
    const seconds = Math.floor(diff / 1000)
    const t = (key: string, params?: Record<string, unknown>) => i18n.global.t(key, params ?? {})
    if (seconds < 5) return t('relativeTime.justNow')
    if (seconds < 60) return t('relativeTime.secondsAgo', { n: seconds })
    const minutes = Math.floor(seconds / 60)
    if (minutes < 60) return t('relativeTime.minutesAgo', { n: minutes })
    const hours = Math.floor(minutes / 60)
    if (hours < 24) return t('relativeTime.hoursAgo', { n: hours })
    const days = Math.floor(hours / 24)
    if (days < 30) return t('relativeTime.daysAgo', { n: days })
    const months = Math.floor(days / 30)
    return t('relativeTime.monthsAgo', { n: months })
  }

  function refreshAll() {
    tick.value = Date.now()
    document.querySelectorAll<HTMLElement>('[data-time]').forEach((el) => {
      const ts = el.getAttribute('data-time')
      if (ts) {
        el.textContent = format(ts)
      }
    })
  }

  onMounted(() => {
    refreshAll()
    timer = setInterval(refreshAll, 30000)
  })

  onUnmounted(() => {
    if (timer) clearInterval(timer)
  })

  return { tick, format }
}
