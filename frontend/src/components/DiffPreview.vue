<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

type DiffKind = 'same' | 'remove' | 'add'
type DiffRow = {
  kind: DiffKind
  beforeNumber: number | null
  afterNumber: number | null
  text: string
}

const props = defineProps<{
  before: string
  after: string
}>()

const { t } = useI18n()

function lines(value: string) {
  if (!value) return []
  const result = value.replace(/\r\n/g, '\n').split('\n')
  if (result[result.length - 1] === '') result.pop()
  return result
}

function buildDiff(before: string, after: string): DiffRow[] {
  const oldLines = lines(before)
  const newLines = lines(after)
  const table = Array.from({ length: oldLines.length + 1 }, () => new Array<number>(newLines.length + 1).fill(0))

  for (let oldIndex = oldLines.length - 1; oldIndex >= 0; oldIndex--) {
    for (let newIndex = newLines.length - 1; newIndex >= 0; newIndex--) {
      table[oldIndex][newIndex] = oldLines[oldIndex] === newLines[newIndex]
        ? table[oldIndex + 1][newIndex + 1] + 1
        : Math.max(table[oldIndex + 1][newIndex], table[oldIndex][newIndex + 1])
    }
  }

  const result: DiffRow[] = []
  let oldIndex = 0
  let newIndex = 0
  while (oldIndex < oldLines.length || newIndex < newLines.length) {
    if (oldIndex < oldLines.length && newIndex < newLines.length && oldLines[oldIndex] === newLines[newIndex]) {
      result.push({ kind: 'same', beforeNumber: oldIndex + 1, afterNumber: newIndex + 1, text: oldLines[oldIndex] })
      oldIndex++
      newIndex++
    } else if (oldIndex < oldLines.length && (newIndex === newLines.length || table[oldIndex + 1][newIndex] >= table[oldIndex][newIndex + 1])) {
      result.push({ kind: 'remove', beforeNumber: oldIndex + 1, afterNumber: null, text: oldLines[oldIndex] })
      oldIndex++
    } else {
      result.push({ kind: 'add', beforeNumber: null, afterNumber: newIndex + 1, text: newLines[newIndex] })
      newIndex++
    }
  }
  return result
}

const diffRows = computed(() => buildDiff(props.before, props.after))
const hasChanges = computed(() => diffRows.value.some((row) => row.kind !== 'same'))
</script>

<template>
  <div class="diff-preview">
    <div class="diff-preview-toolbar">
      <div class="diff-preview-files"><span class="text-mono">{{ t('toolAccess.diff.before') }}</span><span aria-hidden="true">→</span><span class="text-mono">{{ t('toolAccess.diff.after') }}</span></div>
      <div class="diff-preview-legend" :aria-label="t('toolAccess.diff.legend')">
        <span class="diff-preview-legend-item removed"><span class="diff-preview-swatch" aria-hidden="true">−</span>{{ t('toolAccess.diff.removed') }}</span>
        <span class="diff-preview-legend-item added"><span class="diff-preview-swatch" aria-hidden="true">+</span>{{ t('toolAccess.diff.added') }}</span>
        <span class="diff-preview-legend-item unchanged"><span class="diff-preview-swatch" aria-hidden="true"> </span>{{ t('toolAccess.diff.unchanged') }}</span>
      </div>
    </div>
    <div v-if="!hasChanges" class="diff-preview-empty">{{ t('toolAccess.diff.noChanges') }}</div>
    <div v-else class="diff-preview-scroll" role="region" :aria-label="t('toolAccess.diff.content')" tabindex="0">
      <div v-for="(row, index) in diffRows" :key="`${row.kind}-${index}`" class="diff-preview-row" :class="row.kind">
        <span class="diff-preview-gutter text-mono">{{ row.beforeNumber || '' }}</span>
        <span class="diff-preview-gutter text-mono">{{ row.afterNumber || '' }}</span>
        <span class="diff-preview-marker" aria-hidden="true">{{ row.kind === 'remove' ? '−' : row.kind === 'add' ? '+' : ' ' }}</span>
        <span class="diff-preview-code text-mono">{{ row.text || ' ' }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.diff-preview { display: flex; flex-direction: column; min-height: 0; height: 100%; }
.diff-preview-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex: 0 0 auto; padding: 0 0 10px; color: var(--muted); font-size: 11px; }
.diff-preview-files, .diff-preview-legend, .diff-preview-legend-item { display: flex; align-items: center; gap: 6px; }
.diff-preview-files { color: var(--fg); }
.diff-preview-legend { flex-wrap: wrap; justify-content: flex-end; gap: 9px; }
.diff-preview-legend-item { gap: 4px; }
.diff-preview-legend-item.removed { color: var(--negative); }
.diff-preview-legend-item.added { color: var(--positive); }
.diff-preview-swatch { width: 14px; text-align: center; font: 600 12px/1 var(--font-mono); }
.diff-preview-scroll { min-height: 0; flex: 1 1 auto; overflow: auto; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--bg); font: 11px/1.55 var(--font-mono); }
.diff-preview-row { display: grid; grid-template-columns: 42px 42px 20px minmax(0, 1fr); min-width: 0; padding: 2px 10px 2px 0; }
.diff-preview-row.same { color: var(--muted); }
.diff-preview-row.remove { background: color-mix(in srgb, var(--negative) 13%, transparent); color: var(--negative); }
.diff-preview-row.add { background: color-mix(in srgb, var(--positive) 13%, transparent); color: var(--positive); }
.diff-preview-gutter { padding-right: 8px; color: color-mix(in srgb, var(--muted) 70%, transparent); font-size: 10px; text-align: right; user-select: none; }
.diff-preview-marker { color: inherit; font-weight: 700; text-align: center; user-select: none; }
.diff-preview-code { min-width: 0; white-space: pre-wrap; overflow-wrap: anywhere; word-break: break-word; }
.diff-preview-empty { display: grid; min-height: 150px; flex: 1 1 auto; place-items: center; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--bg); color: var(--muted); font-size: 12px; }
@media (max-width: 600px) { .diff-preview-toolbar { align-items: flex-start; flex-direction: column; } .diff-preview-legend { justify-content: flex-start; } }
</style>
