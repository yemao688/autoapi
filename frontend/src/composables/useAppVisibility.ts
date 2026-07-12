import { readonly, ref } from 'vue'
import { api, type AppVisibilityState } from '@/api/bridge'
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'

// The backend already exposes App.GetAppVisibilityState() returning the
// string 'foreground' or 'background', and emits the same string via the
// Wails 'app:visibility' event. The frontend type is a typed union for
// clarity; this file maps it to a boolean isVisible ref.

const isVisible = ref(true)
let eventsOff: (() => void) | null = null
let initialized = false

// Generation counter for the app:visibility event stream. The initial
// getAppVisibilityState() query is tagged with the counter value at the
// moment it begins; if an event fires while the query is in flight (the
// counter changes), the query response is stale and must be discarded.
let eventGen = 0

function setVisible(visible: boolean) {
  eventGen++
  isVisible.value = visible
}

/**
 * useAppVisibility is a singleton coordinator that tracks whether the
 * Wails application window is currently visible/foregrounded.
 *
 * It bootstraps from api.getAppVisibilityState() and keeps state in sync
 * via the Wails 'app:visibility' event. Components can import the same
 * shared ref so there is only one runtime listener and one async fetch.
 */
export function useAppVisibility() {
  if (!initialized) {
    initialized = true

    // Subscribe first so we cannot miss a state change that happens while
    // the initial query is in flight. The event handler owns the source of
    // truth; it bumps the generation counter on every event.
    const off = EventsOn('app:visibility', (state: string) => {
      setVisible(state === 'foreground')
    })
    eventsOff = typeof off === 'function' ? off : () => EventsOff('app:visibility')

    // Capture the generation at the moment the query starts. If an event
    // fires during the query (eventGen changes), the query result is stale
    // and must be ignored in favor of the newer event state.
    const queryGen = eventGen
    api.getAppVisibilityState()
      .then((state) => {
        if (queryGen !== eventGen) return
        isVisible.value = state === 'foreground'
      })
      .catch((e: any) => {
        if (queryGen !== eventGen) return
        console.warn('[visibility] getAppVisibilityState failed:', e)
        isVisible.value = true
      })
  }

  return { isVisible: readonly(isVisible) }
}
