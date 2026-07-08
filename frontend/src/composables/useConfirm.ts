import { reactive } from 'vue'

export interface ConfirmOptions {
  title?: string
  message: string
  confirmText?: string
  cancelText?: string
  danger?: boolean
}

interface ConfirmState {
  open: boolean
  id: number
  title: string
  message: string
  confirmText: string
  cancelText: string
  danger: boolean
  busy: boolean
}

let nextId = 1
const state = reactive<ConfirmState>({
  open: false,
  id: 0,
  title: '确认',
  message: '',
  confirmText: '确认',
  cancelText: '取消',
  danger: false,
  busy: false,
})

let resolver: ((value: boolean) => void) | null = null

/**
 * Shared confirmation dialog. Returns a promise that resolves to `true`
 * when the user confirms, `false` when they cancel (or dismiss the
 * backdrop / press Escape).
 *
 * The `<ConfirmDialog />` component must be mounted once at app level
 * (registered in `App.vue`) so the dialog can render from anywhere.
 */
export function useConfirm() {
  function open(options: ConfirmOptions): Promise<boolean> {
    // If a previous prompt is still open, resolve it as cancelled so we
    // never deadlock awaiting two stacked dialogs.
    if (resolver) {
      const prev = resolver
      resolver = null
      state.open = false
      state.busy = false
      prev(false)
    }
    state.id = nextId++
    state.title = options.title ?? '确认'
    state.message = options.message
    state.confirmText = options.confirmText ?? '确认'
    state.cancelText = options.cancelText ?? '取消'
    state.danger = options.danger ?? false
    state.busy = false
    state.open = true
    return new Promise<boolean>((resolve) => {
      resolver = resolve
    })
  }

  function resolve(confirmed: boolean) {
    if (!state.open) return
    state.busy = true
    const r = resolver
    resolver = null
    state.open = false
    // Slight delay so the busy flag can settle; reset on next tick.
    Promise.resolve().then(() => {
      state.busy = false
    })
    if (r) r(confirmed)
  }

  return { state, open, resolve }
}
