import { ref, watch } from 'vue'

// Shared, persistent refresh-mode control for the usage-stats view.
//
// The mode is stored in localStorage so a user who enables realtime or
// polling sync sees the same state when they switch tabs and come back,
// or when the Vue app reloads. The localStorage value is parsed once at
// module load and the watcher below writes back on every change.
//
// This composable exports a single shared ref via `useLiveSync()` so the
// view and any future toolbars stay in sync without prop-drilling.
// The module-level `liveSync` ref is intentionally a singleton.

const STORAGE_KEY = 'autoapi-live-sync-mode'

export const SYNC_MODES = ['realtime', '5s', '30s', 'off'] as const
export type SyncMode = (typeof SYNC_MODES)[number]

function readInitial(): SyncMode {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw && (SYNC_MODES as readonly string[]).includes(raw)) {
      return raw as SyncMode
    }
  } catch {
    // localStorage is unavailable (private mode, quota error, SSR with
    // no window). Fall back to the off state — the UI still works, the
    // mode just won't persist across reloads.
  }
  return 'off'
}

const liveSync = ref<SyncMode>(readInitial())

watch(liveSync, (v) => {
  try {
    localStorage.setItem(STORAGE_KEY, v)
  } catch {
    // Silently ignore write errors — same rationale as readInitial.
  }
})

export function useLiveSync() {
  return { liveSync }
}
