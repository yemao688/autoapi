<script setup lang="ts">
/**
 * DropdownMenu
 *
 * A lightweight, headless-friendly three-dots menu. Renders the trigger
 * inline and teleports the menu panel to <body> so it can never be
 * clipped by a parent with `overflow: hidden` or a small scroll
 * container. Only one menu is open at a time across the whole app:
 * opening a new one (or pressing Escape / clicking outside) closes any
 * other that was open.
 *
 * Usage:
 *   <DropdownMenu :menu-id="row.id">
 *     <template #trigger="{ toggle, open }">
 *       <button class="btn btn-icon" @click="toggle" :aria-expanded="open">⋯</button>
 *     </template>
 *     <template #menu="{ close }">
 *       <button class="dropdown-item" role="menuitem" @click="edit(row); close()">编辑</button>
 *       <button class="dropdown-item danger" role="menuitem" @click="del(row); close()">删除</button>
 *     </template>
 *   </DropdownMenu>
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'

type Placement = 'down' | 'up' | 'auto'

const props = withDefaults(
  defineProps<{
    /** Unique id for the row this menu belongs to. */
    menuId: string
    /** Vertical placement hint. "auto" (default) flips up if needed. */
    placement?: Placement
    /** Minimum menu width in px. */
    minWidth?: number
  }>(),
  {
    placement: 'auto',
    minWidth: 160,
  }
)

// ---- Module-level "only one open at a time" registry ----
// Any DropdownMenu instance with the same active id is considered open.
// Opening a new one (by setting `activeId` to a different menuId) makes
// every other instance close itself.
const activeId = ref<string | null>(null)
let outsideClickHandler: ((e: MouseEvent) => void) | null = null
let escapeHandler: ((e: KeyboardEvent) => void) | null = null

function registerOutsideHandlers() {
  if (outsideClickHandler) return
  outsideClickHandler = (e: MouseEvent) => {
    const target = e.target as Element | null
    if (!target) return
    // Ignore clicks on any trigger or any open menu.
    if (target.closest?.('[data-dropdown-trigger]')) return
    if (target.closest?.('[data-dropdown-menu]')) return
    activeId.value = null
  }
  escapeHandler = (e: KeyboardEvent) => {
    if (e.key === 'Escape' && activeId.value !== null) {
      activeId.value = null
    }
  }
  // Use BUBBLE-phase `click` (not `mousedown` capture). mousedown
  // capture fires *before* the trigger's own `click` handler, so the
  // outside-handler would null out `activeId` and the trigger's
  // `toggle()` would then immediately reopen it. Bubble-phase `click`
  // lets the trigger fire first; the `closest` check above then
  // short-circuits for trigger clicks, giving natural toggle behavior.
  document.addEventListener('click', outsideClickHandler)
  document.addEventListener('keydown', escapeHandler)
}

function unregisterOutsideHandlers() {
  if (outsideClickHandler) {
    document.removeEventListener('click', outsideClickHandler)
    outsideClickHandler = null
  }
  if (escapeHandler) {
    document.removeEventListener('keydown', escapeHandler)
    escapeHandler = null
  }
}

// ---- Per-instance state ----
const triggerEl = ref<HTMLElement | null>(null)
const menuEl = ref<HTMLElement | null>(null)
const isOpen = computed(() => activeId.value === props.menuId)

const position = ref<{ top: number; left: number; width: number; placement: 'down' | 'up' }>({
  top: 0,
  left: 0,
  width: 0,
  placement: 'down',
})

function computePosition() {
  const trigger = triggerEl.value
  const menu = menuEl.value
  if (!trigger) return
  const rect = trigger.getBoundingClientRect()
  const menuHeight = menu?.offsetHeight ?? 0
  const menuWidth = Math.max(props.minWidth, menu?.offsetWidth ?? props.minWidth)
  const viewportW = window.innerWidth
  const viewportH = window.innerHeight
  const margin = 8

  let placement: 'down' | 'up' = 'down'
  let top = rect.bottom + 4
  if (props.placement === 'auto') {
    if (top + menuHeight + margin > viewportH && rect.top - menuHeight - 4 >= margin) {
      placement = 'up'
      top = rect.top - menuHeight - 4
    }
  } else if (props.placement === 'up') {
    placement = 'up'
    top = rect.top - menuHeight - 4
  }

  let left = rect.right - menuWidth
  if (left < margin) {
    left = margin
  } else if (left + menuWidth + margin > viewportW) {
    left = viewportW - menuWidth - margin
  }

  position.value = { top, left, width: menuWidth, placement }
}

function getMenuItems(): HTMLElement[] {
  const menu = menuEl.value
  if (!menu) return []
  return Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))
}

function focusFirstItem() {
  void nextTick(() => {
    const items = getMenuItems()
    items[0]?.focus()
  })
}

function returnFocusToTrigger() {
  // The trigger may have been removed from the DOM (e.g. row deleted)
  // — only refocus if it's still attached.
  const trigger = triggerEl.value
  if (trigger && document.contains(trigger)) {
    trigger.focus()
  }
}

function open() {
  activeId.value = props.menuId
  registerOutsideHandlers()
  void nextTick(() => {
    computePosition()
    focusFirstItem()
  })
}

function close() {
  if (activeId.value === props.menuId) {
    activeId.value = null
  }
}

function toggle() {
  if (isOpen.value) close()
  else open()
}

// Centralize focus management on the activeId transition so every close
// path (Escape, outside click, item click, another menu opening) does
// the right thing.
watch(activeId, (val, prev) => {
  if (val === props.menuId) {
    // We just became the active menu (either opened by us, or another
    // menu handed the focus to us).
    void nextTick(() => {
      computePosition()
      if (prev !== props.menuId) {
        focusFirstItem()
      }
    })
  } else if (val === null) {
    // We were open and just got closed. Return focus to the trigger so
    // keyboard users land somewhere sensible.
    returnFocusToTrigger()
  }
  // If val is a different menuId, another menu stole focus — leave
  // our own state alone and let its owner handle focus.
})

// Recompute on window resize / scroll so the menu follows the trigger.
let resizeHandler: (() => void) | null = null
let scrollHandler: (() => void) | null = null
let rafScheduled = false

function scheduleRecompute() {
  if (!isOpen.value) return
  if (rafScheduled) return
  rafScheduled = true
  requestAnimationFrame(() => {
    rafScheduled = false
    if (isOpen.value) computePosition()
  })
}

function attachWindowListeners() {
  if (resizeHandler) return
  resizeHandler = scheduleRecompute
  scrollHandler = scheduleRecompute
  window.addEventListener('resize', resizeHandler)
  window.addEventListener('scroll', scrollHandler, true)
}

function detachWindowListeners() {
  if (resizeHandler) {
    window.removeEventListener('resize', resizeHandler)
    resizeHandler = null
  }
  if (scrollHandler) {
    window.removeEventListener('scroll', scrollHandler, true)
    scrollHandler = null
  }
}

watch(isOpen, (val) => {
  if (val) {
    attachWindowListeners()
  } else {
    detachWindowListeners()
    if (activeId.value === null) unregisterOutsideHandlers()
  }
})

onBeforeUnmount(() => {
  if (isOpen.value) {
    activeId.value = null
  }
  detachWindowListeners()
  unregisterOutsideHandlers()
})

function onMenuKeydown(e: KeyboardEvent) {
  const items = getMenuItems()
  if (!items.length) return
  const currentIdx = items.findIndex((el) => el === document.activeElement)

  switch (e.key) {
    case 'ArrowDown': {
      e.preventDefault()
      const next = currentIdx < 0 ? 0 : (currentIdx + 1) % items.length
      items[next]?.focus()
      break
    }
    case 'ArrowUp': {
      e.preventDefault()
      const next = currentIdx < 0 ? items.length - 1 : (currentIdx - 1 + items.length) % items.length
      items[next]?.focus()
      break
    }
    case 'Home': {
      e.preventDefault()
      items[0]?.focus()
      break
    }
    case 'End': {
      e.preventDefault()
      items[items.length - 1]?.focus()
      break
    }
    case 'Enter':
    case ' ': {
      // Activate the focused item by clicking it.
      if (currentIdx >= 0) {
        e.preventDefault()
        items[currentIdx].click()
      }
      break
    }
    // Escape is handled document-level above; no-op here.
  }
}
</script>

<template>
  <span class="dropdown-wrapper">
    <span
      ref="triggerEl"
      class="dropdown-trigger"
      data-dropdown-trigger
    >
      <slot name="trigger" :toggle="toggle" :open="isOpen" :close="close" />
    </span>
    <Teleport to="body">
      <Transition name="dropdown-fade">
        <div
          v-if="isOpen"
          ref="menuEl"
          class="dropdown-menu"
          data-dropdown-menu
          role="menu"
          tabindex="-1"
          :style="{
            top: position.top + 'px',
            left: position.left + 'px',
            width: position.width + 'px',
          }"
          @keydown="onMenuKeydown"
        >
          <slot name="menu" :close="close" />
        </div>
      </Transition>
    </Teleport>
  </span>
</template>

<style scoped>
.dropdown-wrapper {
  position: relative;
  display: inline-block;
}

.dropdown-trigger {
  display: inline-flex;
}

.dropdown-menu {
  /* Position is set inline; the global `.dropdown-menu` rule provides
     the visual style (background, border, padding, shadow, etc.). */
  position: fixed;
  z-index: 1000;
}

.dropdown-fade-enter-active,
.dropdown-fade-leave-active {
  transition: opacity 0.12s ease, transform 0.12s ease;
}

.dropdown-fade-enter-from,
.dropdown-fade-leave-to {
  opacity: 0;
  transform: translateY(-2px);
}
</style>
