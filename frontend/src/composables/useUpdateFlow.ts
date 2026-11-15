import { computed, reactive } from 'vue'
import { Events } from '@wailsio/runtime'
import { api } from '@/api/bridge'
import type { UpdateInfo } from '@/api/models'
import { useToast } from '@/composables/useToast'
import i18n from '@/locales'

/**
 * The update flow is a module-scoped singleton: views are not kept alive, and
 * the menu / tray 检查更新 entry can fire while the Settings view is unmounted.
 * Keeping the state — and the updater event subscriptions — here means a
 * check started from the menu, or a download that keeps running after the
 * dialog is hidden, survives navigation.
 */

export type UpdateFlowPhase =
  | 'idle'
  | 'checking'
  | 'current'
  | 'available'
  | 'downloading'
  | 'ready'
  | 'error'

/** The action the inline / dialog 重试 button repeats. */
export type UpdateFlowAction = 'check' | 'download' | 'restart'

export interface UpdateProgress {
  written: number
  total: number
  rate: number
}

export interface UpdateFlowState {
  phase: UpdateFlowPhase
  /** The dialog is showing. Tracked separately from `phase` so 稍后 can hide it while a staged update stays ready. */
  dialogOpen: boolean
  /** The user hid the dialog while a download keeps running. */
  background: boolean
  info: UpdateInfo | null
  progress: UpdateProgress | null
  errorMessage: string
  failedAction: UpdateFlowAction
  restarting: boolean
}

/** App event the Go menu / tray 检查更新 entries emit. */
export const UPDATE_CHECK_EVENT = 'autoapi:update:check'

const PROGRESS_EVENT = 'wails:updater:download-progress'
const READY_EVENT = 'wails:updater:update-ready'
const ERROR_EVENT = 'wails:updater:error'

const state = reactive<UpdateFlowState>({
  phase: 'idle',
  dialogOpen: false,
  background: false,
  info: null,
  progress: null,
  errorMessage: '',
  failedAction: 'check',
  restarting: false,
})

const toast = useToast()

let unsubscribeUpdater: (() => void) | null = null
let checkInFlight = false
let downloadInFlight = false

type InterpolationValues = Record<string, string | number>

function t(key: string, values?: InterpolationValues): string {
  return values ? i18n.global.t(key, values) : i18n.global.t(key)
}

/**
 * The Go side rejects with "update checker is not available in this build" for
 * development builds, unsupported platforms and builds that never attached the
 * updater. Retrying cannot help, so it becomes a plain statement rather than a
 * raw error string.
 */
function isUnavailableMessage(message: string): boolean {
  const normalized = message.toLowerCase()
  return (
    normalized.includes('update checker is not available') ||
    normalized.includes('not available in this build')
  )
}

/** Wails binding rejections are Errors whose message is the Go error string. */
function describeError(error: unknown): string {
  if (typeof error === 'string') return error
  if (error instanceof Error) return error.message
  if (error && typeof error === 'object' && 'message' in error) {
    const message = (error as { message?: unknown }).message
    if (typeof message === 'string') return message
  }
  return String(error ?? '')
}

/** Event payloads arrive decoded, but tolerate a JSON string as well. */
function payloadOf(data: unknown): Record<string, unknown> {
  if (typeof data === 'string') {
    try {
      return payloadOf(JSON.parse(data) as unknown)
    } catch {
      return {}
    }
  }
  if (typeof data === 'object' && data !== null) {
    return data as Record<string, unknown>
  }
  return {}
}

function stringField(payload: Record<string, unknown>, key: string): string {
  const value = payload[key]
  return typeof value === 'string' ? value : ''
}

function numberField(payload: Record<string, unknown>, key: string): number {
  const value = payload[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

// ---------------------------------------------------------------- subscriptions

function startWatchingUpdater(): void {
  stopWatchingUpdater()
  const offProgress = Events.On(PROGRESS_EVENT, (event) => {
    const payload = payloadOf(event.data)
    state.progress = {
      written: numberField(payload, 'written'),
      total: numberField(payload, 'total'),
      rate: numberField(payload, 'rate'),
    }
  })
  const offReady = Events.On(READY_EVENT, () => {
    completeDownload()
  })
  const offError = Events.On(ERROR_EVENT, (event) => {
    // The awaited downloadUpdate() rejection is the source of truth; the event
    // only supplies the message before the promise settles.
    if (state.phase !== 'downloading') return
    const message = stringField(payloadOf(event.data), 'message')
    failDownload(message || t('settings.about.update.errorFallback'))
  })
  unsubscribeUpdater = () => {
    offProgress()
    offReady()
    offError()
  }
}

function stopWatchingUpdater(): void {
  unsubscribeUpdater?.()
  unsubscribeUpdater = null
}

// ---------------------------------------------------------------- transitions

function completeDownload(): void {
  if (state.phase === 'ready') return
  state.phase = 'ready'
  state.errorMessage = ''
  state.progress = null
  // The user hid the dialog to keep working: announce instead of stealing focus.
  if (state.background && !state.dialogOpen) {
    toast.push(t('settings.about.update.readyToast'), 'success', 5000)
  }
}

function failDownload(message: string): void {
  // wails:updater:error and the rejected downloadUpdate() both report the same
  // failure; the event's message wins because it arrives first.
  if (state.phase === 'error') return
  state.phase = 'error'
  state.failedAction = 'download'
  state.errorMessage = message
  state.progress = null
  if (!state.background) {
    state.dialogOpen = true
  }
}

// ---------------------------------------------------------------- actions

async function check(): Promise<void> {
  if (checkInFlight || downloadInFlight) return
  // A staged update is already the answer; the About row offers the restart.
  if (state.phase === 'downloading' || state.phase === 'ready') return

  checkInFlight = true
  state.phase = 'checking'
  state.background = false
  state.errorMessage = ''
  state.progress = null
  try {
    const info = await api.checkForUpdate()
    state.info = info
    if (info?.available) {
      state.phase = 'available'
      state.dialogOpen = true
      return
    }
    state.phase = 'current'
    state.dialogOpen = false
  } catch (error: unknown) {
    const message = describeError(error)
    state.phase = 'error'
    state.failedAction = 'check'
    state.errorMessage = message
    // Development builds can never check; a modal for that is just noise.
    state.dialogOpen = !isUnavailableMessage(message)
  } finally {
    checkInFlight = false
  }
}

async function download(): Promise<void> {
  if (downloadInFlight || checkInFlight) return
  if (!state.info?.available) return

  downloadInFlight = true
  state.phase = 'downloading'
  state.background = false
  state.dialogOpen = true
  state.errorMessage = ''
  state.progress = { written: 0, total: state.info.sizeBytes ?? 0, rate: 0 }
  startWatchingUpdater()
  try {
    await api.downloadUpdate()
    completeDownload()
  } catch (error: unknown) {
    // wails:updater:error usually lands first and carries the useful message;
    // failDownload keeps that message instead of the generic one.
    failDownload(describeError(error))
  } finally {
    downloadInFlight = false
    stopWatchingUpdater()
  }
}

function restart(): void {
  if (state.restarting) return
  state.restarting = true
  // The app quits into the updater helper, so this promise usually never
  // settles. A rejection means the helper could not start; that must not look
  // like a silent hang.
  void api
    .restartToUpdate()
    .then(() => {
      // The helper normally quits the app straight away; if the call resolves
      // and we are still here, let the user press it again.
      state.restarting = false
    })
    .catch((error: unknown) => {
      state.restarting = false
      state.phase = 'error'
      state.failedAction = 'restart'
      state.errorMessage = describeError(error)
      state.dialogOpen = true
      state.background = false
    })
}

function retry(): void {
  switch (state.failedAction) {
    case 'download':
      void download()
      return
    case 'restart':
      restart()
      return
    default:
      void check()
  }
}

function dismiss(): void {
  state.dialogOpen = false
  // Hiding the dialog during a download keeps the flow going in the background.
  state.background = state.phase === 'downloading'
}

function openDialog(): void {
  if (state.phase === 'idle' || state.phase === 'checking' || state.phase === 'current') return
  state.background = false
  state.dialogOpen = true
}

/** Releases the updater subscriptions; called when the app shell unmounts. */
export function disposeUpdateFlow(): void {
  stopWatchingUpdater()
  checkInFlight = false
  downloadInFlight = false
  state.dialogOpen = false
  state.background = false
}

// ---------------------------------------------------------------- derivations

const errorText = computed(() => {
  const message = state.errorMessage
  if (!message) return ''
  if (isUnavailableMessage(message)) return t('settings.about.update.unsupported')
  return message
})

const unsupportedBuild = computed(() => isUnavailableMessage(state.errorMessage))

const downloadPercent = computed<number | null>(() => {
  const progress = state.progress
  if (!progress || progress.total <= 0) return null
  return Math.min(100, Math.max(0, (progress.written / progress.total) * 100))
})

/**
 * useUpdateFlow exposes the shared update state plus the actions the About
 * section and the app shell drive.
 */
export function useUpdateFlow() {
  return {
    state,
    errorText,
    unsupportedBuild,
    downloadPercent,
    check,
    download,
    retry,
    restart,
    dismiss,
    openDialog,
  }
}
