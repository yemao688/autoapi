import { onMounted, onUnmounted, ref } from 'vue'

export function useRelativeTime() {
  const tick = ref(0)
  let timer: ReturnType<typeof setInterval> | null = null

  function format(timestamp: number | string | undefined): string {
    if (!timestamp) return '—'
    const ts = typeof timestamp === 'string' ? parseInt(timestamp, 10) : timestamp
    const now = Date.now()
    const diff = now - ts
    const seconds = Math.floor(diff / 1000)
    if (seconds < 5) return '刚刚'
    if (seconds < 60) return `${seconds} 秒前`
    const minutes = Math.floor(seconds / 60)
    if (minutes < 60) return `${minutes} 分钟前`
    const hours = Math.floor(minutes / 60)
    if (hours < 24) return `${hours} 小时前`
    const days = Math.floor(hours / 24)
    if (days < 30) return `${days} 天前`
    const months = Math.floor(days / 30)
    return `${months} 个月前`
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
