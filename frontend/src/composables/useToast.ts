import { ref } from 'vue'

export type ToastType = 'success' | 'error' | 'warning' | 'info'
export type ModelTestStatus = 'running' | 'success' | 'error' | 'cancelled'

export interface ModelTestToastPayload {
  key: string
  title: string
  status: ModelTestStatus
  elapsedSeconds: number
  detail: string
  onRunningClose?: () => void
}

export interface ToastItem {
  id: number
  message: string
  type: ToastType
  kind?: 'default' | 'model-test'
  modelTest?: ModelTestToastPayload
}

const toasts = ref<ToastItem[]>([])
let nextId = 1
const MAX_TOASTS = 10
const removeTimers = new Map<number, ReturnType<typeof setTimeout>>()
const modelTestIntervals = new Map<number, ReturnType<typeof setInterval>>()

function clearRemoveTimer(id: number) {
  const timer = removeTimers.get(id)
  if (!timer) return
  clearTimeout(timer)
  removeTimers.delete(id)
}

function clearModelTestInterval(id: number) {
  const timer = modelTestIntervals.get(id)
  if (!timer) return
  clearInterval(timer)
  modelTestIntervals.delete(id)
}

/**
 * Module-level singleton for app-wide transient toast notifications.
 */
export function useToast() {
  function scheduleRemove(id: number, duration: number) {
    clearRemoveTimer(id)
    if (duration <= 0) return
    removeTimers.set(id, setTimeout(() => remove(id), duration))
  }

  function push(message: string, type: ToastType = 'info', duration = 3000): number {
    const id = nextId++
    if (toasts.value.length >= MAX_TOASTS) {
      const dropped = toasts.value[0]
      if (dropped) remove(dropped.id)
    }
    toasts.value.push({ id, message, type, kind: 'default' })
    scheduleRemove(id, duration)
    return id
  }

  function startModelTest(options: {
    key: string
    title: string
    detail: string
    onRunningClose?: () => void
  }): number {
    const existing = toasts.value.find((toast) => toast.kind === 'model-test' && toast.modelTest?.key === options.key)
    const id = existing?.id ?? nextId++
    clearRemoveTimer(id)
    clearModelTestInterval(id)

    const item: ToastItem = {
      id,
      message: '',
      type: 'info',
      kind: 'model-test',
      modelTest: {
        key: options.key,
        title: options.title,
        status: 'running',
        elapsedSeconds: 0,
        detail: options.detail,
        onRunningClose: options.onRunningClose,
      },
    }

    if (existing) {
      const index = toasts.value.findIndex((toast) => toast.id === id)
      if (index >= 0) toasts.value.splice(index, 1, item)
    } else {
      if (toasts.value.length >= MAX_TOASTS) {
        const dropped = toasts.value[0]
        remove(dropped.id)
      }
      toasts.value.push(item)
    }

    modelTestIntervals.set(id, setInterval(() => {
      const toast = toasts.value.find((entry) => entry.id === id)
      if (!toast?.modelTest || toast.modelTest.status !== 'running') {
        clearModelTestInterval(id)
        return
      }
      toast.modelTest.elapsedSeconds += 1
    }, 1000))

    return id
  }

  function finishModelTest(id: number, options: {
    status: Exclude<ModelTestStatus, 'running'>
    detail: string
    type?: ToastType
    duration?: number
  }) {
    const toast = toasts.value.find((entry) => entry.id === id)
    if (!toast?.modelTest) return
    clearModelTestInterval(id)
    toast.type = options.type ?? (options.status === 'success' ? 'success' : options.status === 'cancelled' ? 'warning' : 'error')
    toast.modelTest = {
      ...toast.modelTest,
      status: options.status,
      detail: options.detail,
      onRunningClose: undefined,
    }
    scheduleRemove(id, options.duration ?? 5000)
  }

  function remove(id: number) {
    const idx = toasts.value.findIndex((t) => t.id === id)
    if (idx < 0) return
    const toast = toasts.value[idx]
    clearRemoveTimer(id)
    clearModelTestInterval(id)
    if (toast.modelTest?.status === 'running') {
      toast.modelTest.onRunningClose?.()
    }
    toasts.value.splice(idx, 1)
  }

  function clear() {
    for (const toast of [...toasts.value]) {
      remove(toast.id)
    }
    toasts.value = []
  }

  return {
    toasts,
    push,
    startModelTest,
    finishModelTest,
    remove,
    clear,
  }
}
