import { useRouter } from 'vue-router'
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'

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
//   - Reload after RESET_AFTER_NAVIGATIONS route changes, or — for sessions
//     with little UI navigation (the proxy keeps working while the window sits
//     on one view) — after RESET_AFTER_ELAPSED_MS since the previous reset.
//     The elapsed trigger only applies after the first reset: on app start the
//     render process is fresh and sessionStorage was wiped with it.
//   - Never more often than once per MIN_INTERVAL_BETWEEN_RESETS (cooldown).
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
const MIN_INTERVAL_BETWEEN_RESETS_MS = 2 * 60 * 60 * 1000 // 2 hours (cooldown)
const RESET_AFTER_ELAPSED_MS = 3 * 60 * 60 * 1000 // 3 hours (time-based trigger)
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

// performReset re-baselines the sessionStorage counters and reloads the page.
// Re-baselining keeps the automatic reset from firing again right after a
// manual one (the cooldown/time window restarts from the manual reload).
function performReset(reason: string) {
  writeNumber(KEY_LAST_RESET, Date.now())
  writeNumber(KEY_NAV_COUNT, 0)
  console.info('[wvreset] reloading WebView:', reason)
  location.reload()
}

function maybeReset() {
  if (typeof document !== 'undefined' && document.visibilityState !== 'visible') {
    return
  }
  const now = Date.now()

  const lastReset = readNumber(KEY_LAST_RESET)
  // Cooldown: never reset more often than the minimum interval.
  if (lastReset > 0 && now - lastReset < MIN_INTERVAL_BETWEEN_RESETS_MS) return

  const navCount = readNumber(KEY_NAV_COUNT)
  // Trigger on enough route changes, or on elapsed time since the previous
  // reset (see policy above — the time trigger needs a previous reset so a
  // freshly started app with a new render process doesn't reload immediately).
  const dueByTime = lastReset > 0 && now - lastReset >= RESET_AFTER_ELAPSED_MS
  if (navCount < RESET_AFTER_NAVIGATIONS && !dueByTime) return

  const lastActive = readNumber(KEY_LAST_ACTIVE)
  if (lastActive > 0 && now - lastActive < IDLE_BEFORE_RESET_MS) return

  performReset(
    navCount >= RESET_AFTER_NAVIGATIONS ? 'nav ' + navCount : 'elapsed ' + Math.round((now - lastReset) / 36e5) + 'h',
  )
}

// Manual reload requested from the tray / application menu: Go emits
// 'app:reload-webview' via App.ReloadWebView(). Always honored — the user
// explicitly asked, so the automatic policy gates (idle, cooldown) do not
// apply; the counters are re-baselined so the auto-reset won't fire again
// shortly after.
function onManualReloadRequested() {
  performReset('manual request from menu')
}

let manualReloadOff: (() => void) | null = null
let manualReloadBound = false

function bindManualReload() {
  if (manualReloadBound) return
  manualReloadBound = true
  const off = EventsOn('app:reload-webview', onManualReloadRequested)
  manualReloadOff = typeof off === 'function' ? off : () => EventsOff('app:reload-webview')
}

/**
 * useWebViewReset is a singleton coordinator that periodically resets the
 * WebView by reloading the page when the app has been idle long enough and
 * enough route changes have accumulated — or on demand via the tray /
 * application menu. Import once at app startup.
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
    bindManualReload()
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
  manualReloadOff?.()
  manualReloadOff = null
  manualReloadBound = false
}
