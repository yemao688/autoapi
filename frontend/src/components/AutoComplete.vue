<script setup lang="ts">
/**
 * AutoComplete
 *
 * A free-text input with a filterable suggestion list. Replaces the native
 * <datalist> element, whose popup cannot be styled and renders with a white
 * background in WKWebView's dark mode.
 *
 * Features:
 *   - Free text input (modelValue is not constrained to the option list)
 *   - Case-insensitive substring filter; empty query shows all options
 *   - Keyboard navigation: ArrowUp/ArrowDown to move, Enter to select,
 *     Escape to close, Tab to commit and leave
 *   - Outside-click + delayed blur close
 *   - Bounded scroll area with the active option scrolled into view
 *   - Empty state ("No matches") when the filter yields nothing
 *   - Combobox / listbox ARIA wiring
 *
 * Usage:
 *   <AutoComplete
 *     v-model="value"
 *     :options="['a', 'b', 'c']"
 *     placeholder="..."
 *     :disabled="false"
 *   />
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const props = withDefaults(
  defineProps<{
    modelValue: string
    options: string[]
    disabled?: boolean
    placeholder?: string
  }>(),
  {
    disabled: false,
    placeholder: '',
  }
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
}>()

const { t } = useI18n()

const wrapperEl = ref<HTMLElement | null>(null)
const inputEl = ref<HTMLInputElement | null>(null)
const listboxEl = ref<HTMLElement | null>(null)
const isOpen = ref(false)
const activeIndex = ref(-1)

// Unique IDs for ARIA wiring. Stable for the component's lifetime so the
// input's aria-controls and the listbox's id stay in sync.
const uid = Math.random().toString(36).slice(2, 10)
const inputId = `autocomplete-input-${uid}`
const listboxId = `autocomplete-listbox-${uid}`

// Case-insensitive substring filter; empty query → show all options.
const filteredOptions = computed(() => {
  const q = (props.modelValue || '').trim().toLowerCase()
  if (!q) return props.options
  return props.options.filter((o) => o.toLowerCase().includes(q))
})

// Show the dropdown while the user is focused/typing, but never when the
// component is disabled or there are no options to suggest at all.
const showDropdown = computed(() => {
  if (props.disabled) return false
  if (props.options.length === 0) return false
  return isOpen.value
})

// Empty state lives inside the same dropdown panel so the layout doesn't
// shift when the last option is filtered out.
const showEmptyState = computed(
  () => showDropdown.value && filteredOptions.value.length === 0
)

function open() {
  if (props.disabled) return
  if (props.options.length === 0) return
  if (isOpen.value) return
  isOpen.value = true
  activeIndex.value = -1
}

function close() {
  if (!isOpen.value) return
  isOpen.value = false
  activeIndex.value = -1
}

function selectAt(index: number) {
  const opt = filteredOptions.value[index]
  if (opt === undefined) return
  emit('update:modelValue', opt)
  close()
  // Keep focus on the input so the next keystroke feels natural.
  inputEl.value?.focus()
}

function onInput(e: Event) {
  const target = e.target as HTMLInputElement
  emit('update:modelValue', target.value)
  // Typing should always reveal the dropdown (filtered list or empty state).
  open()
}

function onFocus() {
  open()
}

function onBlur() {
  // Defer closing so a mousedown on an option can fire before the
  // listbox unmounts. On the next tick we check whether focus is still
  // inside the wrapper (e.g. moved to an option button).
  window.setTimeout(() => {
    const wrapper = wrapperEl.value
    if (!wrapper) return
    if (!wrapper.contains(document.activeElement)) {
      close()
    }
  }, 120)
}

function onKeydown(e: KeyboardEvent) {
  if (props.disabled) return

  const items = filteredOptions.value

  switch (e.key) {
    case 'ArrowDown': {
      e.preventDefault()
      if (!isOpen.value) {
        open()
        return
      }
      if (items.length === 0) return
      activeIndex.value = activeIndex.value < items.length - 1 ? activeIndex.value + 1 : 0
      scrollActiveIntoView()
      break
    }
    case 'ArrowUp': {
      e.preventDefault()
      if (!isOpen.value) {
        open()
        return
      }
      if (items.length === 0) return
      activeIndex.value = activeIndex.value > 0 ? activeIndex.value - 1 : items.length - 1
      scrollActiveIntoView()
      break
    }
    case 'Enter': {
      // Only hijack Enter when the user is actively navigating the list;
      // otherwise let it submit the surrounding form / modal.
      if (isOpen.value && activeIndex.value >= 0 && activeIndex.value < items.length) {
        e.preventDefault()
        selectAt(activeIndex.value)
      }
      break
    }
    case 'Escape': {
      if (isOpen.value) {
        e.preventDefault()
        close()
      }
      break
    }
    case 'Tab': {
      // Tab commits and moves focus on; close without selecting.
      close()
      break
    }
  }
}

function scrollActiveIntoView() {
  void nextTick(() => {
    const listbox = listboxEl.value
    if (!listbox) return
    const el = listbox.querySelector<HTMLElement>(`[data-index="${activeIndex.value}"]`)
    if (el) el.scrollIntoView({ block: 'nearest' })
  })
}

// Outside click closes the dropdown. Using mousedown (not click) so we
// race ahead of the blur handler and feel instant.
function onDocumentMousedown(e: MouseEvent) {
  const target = e.target as Element | null
  if (!target) return
  const wrapper = wrapperEl.value
  if (!wrapper) return
  if (wrapper.contains(target)) return
  close()
}

onMounted(() => {
  document.addEventListener('mousedown', onDocumentMousedown)
})

onBeforeUnmount(() => {
  document.removeEventListener('mousedown', onDocumentMousedown)
})

// Reset the highlight when the user edits the value: new query, new
// starting position for arrow nav.
watch(
  () => props.modelValue,
  () => {
    activeIndex.value = -1
  }
)
</script>

<template>
  <div ref="wrapperEl" class="autocomplete">
    <input
      :id="inputId"
      ref="inputEl"
      class="input"
      type="text"
      role="combobox"
      autocomplete="off"
      spellcheck="false"
      :value="modelValue"
      :disabled="disabled"
      :placeholder="placeholder"
      :aria-expanded="showDropdown"
      :aria-controls="listboxId"
      :aria-autocomplete="filteredOptions.length > 0 ? 'list' : 'none'"
      :aria-activedescendant="
        activeIndex >= 0 ? `${listboxId}-opt-${activeIndex}` : undefined
      "
      @input="onInput"
      @focus="onFocus"
      @blur="onBlur"
      @keydown="onKeydown"
    >
    <div v-if="showDropdown" class="autocomplete-menu">
      <ul
        v-if="!showEmptyState"
        :id="listboxId"
        ref="listboxEl"
        class="autocomplete-list"
        role="listbox"
      >
        <li
          v-for="(opt, i) in filteredOptions"
          :id="`${listboxId}-opt-${i}`"
          :key="opt"
          class="autocomplete-option"
          :class="{ 'is-active': i === activeIndex }"
          role="option"
          :aria-selected="i === activeIndex"
          :data-index="i"
          @mousedown.prevent="selectAt(i)"
          @mouseenter="activeIndex = i"
        >
          {{ opt }}
        </li>
      </ul>
      <div v-else class="autocomplete-empty" role="status">
        {{ t('common.noMatches') }}
      </div>
    </div>
  </div>
</template>

<style scoped>
.autocomplete {
  position: relative;
  width: 100%;
}

/* The input uses the global `.input` class so it inherits the existing
   light/dark styling, focus ring, font metrics, padding, etc. — no
   duplication of those tokens here. */

/* Dropdown panel. Pinned just below the input, full input width. */
.autocomplete-menu {
  position: absolute;
  top: calc(100% + 4px);
  left: 0;
  right: 0;
  z-index: 1000;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  box-shadow: var(--shadow-md);
  overflow: hidden;
}

.autocomplete-list {
  list-style: none;
  margin: 0;
  padding: 4px;
  max-height: 240px;
  overflow-y: auto;
  /* Stop scroll chaining into the page behind the dropdown. */
  overscroll-behavior: contain;
}

.autocomplete-option {
  padding: 6px 10px;
  border-radius: var(--radius-xs);
  font-size: 13px;
  color: var(--fg);
  cursor: pointer;
  user-select: none;
  -webkit-user-select: none;
  transition: background 0.08s ease;
}

/* The same accent-soft tint is used for hover and keyboard-active so
   mouse and keyboard navigation feel identical. */
.autocomplete-option.is-active,
.autocomplete-option:hover {
  background: var(--accent-soft);
  color: var(--accent);
}

.autocomplete-empty {
  padding: 10px 12px;
  font-size: 12.5px;
  color: var(--muted);
  text-align: center;
}
</style>