import { useRouter } from 'vue-router'

// ============================================================================
// WebView memory pressure reset
//
// The macOS WKWebView render process (WebKit.WebContent) accumulates
// layer-backing IOSurface buffers that WebKit keeps in its surface pools and
// does not reclaim (WebKit bugs 296891 / 277392, both open). Long-running
// sessions grow the render process footprint to multiple GB. The only
// reliable app-side countermeasure is to reload the page periodically so the
// render process is rebuilt and its surface pools are discarded.
//
// Policy (constants below, tune here — there is deliberately no settings UI):
//   - Reload only after RESET_AFTER_NAVIGATIONS route changes, and never more
//     often than once per MIN_INTERVAL_BETWEEN_RESETS.
//   - Only reload while the document is visible and the user has been idle
//     for IDLE_BEFORE_RESET (last click / keydown / mousemove), so the reload
//     never interrupts active work. The hash history preserves the current
//     route across the reload.
//
// State lives in sessionStorage: it survives `location.reload()` (so the
// navigation counter keeps accumulating and the reset timestamp prevents
// reload loops) but is dropped when the app process is restarted — at which
// point the WebContent process is rebuilt anyway and the counter restarts
// from zero.
// ============================================================================

const RESET_AFTER_NAVIGATIONS = 60
const MIN_INTERVAL_BETWEEN_RESETS_MS = 2 * 60 * 60 * 1000 // 2 hours
const IDLE_BEFORE_RESET_MS = 5 * 60 * 1000 // 5 minutes
const CHECK_INTERVAL_MS = 60 * 1000 // check once per minute
const ACTIVITY_THROTTLE_MS = 30 * 1000

const KEY_NAV_COUNT = 'autoapi.wvreset.navCount'
const KEY_LAST_RESET = 'autoapi.wvreset.lastReset'
const KEY_LAST_ACTIVE = 'autoapi.wvreset.lastActive'

let timer: ReturnType<typeof setInterval> | null = null

function readNumber(key: string): number {
  const raw = sessionStorage.getItem(key)
  if (raw === null) return 0
  const n = Number(raw)
  return Number.isFinite(n) ? n : 0
}

function writeNumber(key: string, value: number) {
  try {
    sessionStorage.setItem(key, String(value))
  } catch {
    // sessionStorage unavailable (private mode, quota) — skip persistence;
    // the reset simply becomes less likely, never harmful.
  }
}

function recordActivity() {
  writeNumber(KEY_LAST_ACTIVE, Date.now())
}

let lastActivityWrite = 0
function throttledRecordActivity() {
  const now = Date.now()
  if (now - lastActivityWrite >= ACTIVITY_THROTTLE_MS) {
    lastActivityWrite = now
    recordActivity()
  }
}

function maybeReset() {
  if (typeof document !== 'undefined' && document.visibilityState !== 'visible') {
    return
  }
  const now = Date.now()

  const navCount = readNumber(KEY_NAV_COUNT)
  if (navCount < RESET_AFTER_NAVIGATIONS) return

  const lastReset = readNumber(KEY_LAST_RESET)
  if (lastReset > 0 && now - lastReset < MIN_INTERVAL_BETWEEN_RESETS_MS) return

  const lastActive = readNumber(KEY_LAST_ACTIVE)
  if (lastActive > 0 && now - lastActive < IDLE_BEFORE_RESET_MS) return

  writeNumber(KEY_LAST_RESET, now)
  writeNumber(KEY_NAV_COUNT, 0)
  // console.info('[wvreset] reloading WebView after', navCount, 'navigations')
  location.reload()
}

/**
 * useWebViewReset is a singleton coordinator that periodically resets the
 * WebView by reloading the page when the app has been idle long enough and
 * enough route changes have accumulated. Import once at app startup.
 */
export function useWebViewReset() {
  const router = useRouter()

  router.afterEach(() => {
    writeNumber(KEY_NAV_COUNT, readNumber(KEY_NAV_COUNT) + 1)
  })

  if (timer === null) {
    window.addEventListener('click', throttledRecordActivity, { passive: true })
    window.addEventListener('keydown', throttledRecordActivity, { passive: true })
    window.addEventListener('mousemove', throttledRecordActivity, { passive: true })
    timer = setInterval(maybeReset, CHECK_INTERVAL_MS)
  }
}

export function disposeWebViewReset() {
  if (timer !== null) {
    clearInterval(timer)
    timer = null
    window.removeEventListener('click', throttledRecordActivity)
    window.removeEventListener('keydown', throttledRecordActivity)
    window.removeEventListener('mousemove', throttledRecordActivity)
  }
}
