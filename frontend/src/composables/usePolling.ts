import { onUnmounted } from 'vue'

/**
 * usePolling provides a visibility-aware recursive setTimeout that avoids
 * overlapping calls. The callback is invoked immediately on mount, then
 * repeatedly after `intervalMs` has elapsed since the last call completed.
 * Polling pauses while document.hidden is true and resumes immediately
 * when the page becomes visible again.
 */
export function usePolling(callback: () => unknown | Promise<unknown>, intervalMs: number) {
  let timeoutId: ReturnType<typeof setTimeout> | null = null
  let running = false
  let stopped = false

  function schedule() {
    if (stopped || document.hidden) return
    timeoutId = setTimeout(run, intervalMs)
  }

  async function run() {
    if (running || stopped) return
    running = true
    try {
      await callback()
    } finally {
      running = false
      schedule()
    }
  }

  function onVisibilityChange() {
    if (!document.hidden && !stopped) {
      // Page became visible — poll immediately, then resume normal scheduling.
      run()
    }
  }

  // Start: immediate first call + visibility listener
  document.addEventListener('visibilitychange', onVisibilityChange)
  run()

  onUnmounted(() => {
    stopped = true
    if (timeoutId) clearTimeout(timeoutId)
    document.removeEventListener('visibilitychange', onVisibilityChange)
  })
}
