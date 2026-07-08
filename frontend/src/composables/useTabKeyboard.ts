import type { Ref } from 'vue'

/**
 * Composable that provides keyboard navigation for a tab strip.
 *
 * It looks up tabs via the supplied CSS selector (each tab must be a
 * descendant with the `.tab` class and a `data-pane-id` attribute). Arrow keys,
 * Home/End move focus and trigger activation through `onActivate`. The handler
 * is meant to be attached to the tab strip's keydown listener.
 */
export function useTabKeyboard<T extends string>(
  selector: string,
  activeId: Ref<T>,
  onActivate: (id: T) => void
) {
  function handleKeydown(e: KeyboardEvent) {
    const tabs = document.querySelectorAll<HTMLButtonElement>(`${selector} .tab`)
    if (!tabs.length) return
    const currentIdx = Array.from(tabs).findIndex(
      (t) => t.getAttribute('data-pane-id') === activeId.value,
    )
    let nextIdx = currentIdx
    switch (e.key) {
      case 'ArrowRight':
      case 'ArrowDown':
        e.preventDefault()
        nextIdx = (currentIdx + 1) % tabs.length
        break
      case 'ArrowLeft':
      case 'ArrowUp':
        e.preventDefault()
        nextIdx = (currentIdx - 1 + tabs.length) % tabs.length
        break
      case 'Home':
        e.preventDefault()
        nextIdx = 0
        break
      case 'End':
        e.preventDefault()
        nextIdx = tabs.length - 1
        break
      default:
        return
    }
    const targetId = tabs[nextIdx]?.getAttribute('data-pane-id')
    if (targetId) onActivate(targetId as T)
    tabs[nextIdx]?.focus()
  }

  return { handleKeydown }
}
