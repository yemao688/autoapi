import { ref } from 'vue'

export type ToastType = 'success' | 'error' | 'warning' | 'info'

export interface ToastItem {
  id: number
  message: string
  type: ToastType
}

const toasts = ref<ToastItem[]>([])
let nextId = 1

/**
 * Module-level singleton for app-wide transient toast notifications.
 */
export function useToast() {
  function push(message: string, type: ToastType = 'info', duration = 3000): number {
    const id = nextId++
    toasts.value.push({ id, message, type })
    if (duration > 0) {
      setTimeout(() => remove(id), duration)
    }
    return id
  }

  function remove(id: number) {
    const idx = toasts.value.findIndex((t) => t.id === id)
    if (idx >= 0) toasts.value.splice(idx, 1)
  }

  function clear() {
    toasts.value = []
  }

  return {
    toasts,
    push,
    remove,
    clear,
  }
}
