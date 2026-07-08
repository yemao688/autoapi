import { ref, watch } from 'vue'

// Shared, persistent toggle for the usage-stats view's "Live sync" button.
//
// The toggle is stored in localStorage so a user who enables live sync
// sees the same state when they switch tabs and come back, or when the
// Vue app reloads (e.g. after `wails build` redeploy). The localStorage
// value is parsed exactly once at module load — the watcher below writes
// back on every change but never re-reads.
//
// This composable exports a single shared ref via `useLiveSync()` so the
// view and any future toolbars (settings, status bar, etc.) stay in sync
// without prop-drilling or an event bus. The module-level `liveSync`
// ref is intentionally a singleton: there is only one source of truth.
const STORAGE_KEY = 'autoapi-live-sync'

function readInitial(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === 'true'
  } catch {
    // localStorage is unavailable (private mode, quota error, SSR with
    // no window). Fall back to the off state — the UI still works, the
    // toggle just won't persist across reloads.
    return false
  }
}

const liveSync = ref<boolean>(readInitial())

watch(liveSync, (v) => {
  try {
    localStorage.setItem(STORAGE_KEY, String(v))
  } catch {
    // Silently ignore write errors — same rationale as readInitial.
  }
})

export function useLiveSync() {
  return { liveSync }
}
