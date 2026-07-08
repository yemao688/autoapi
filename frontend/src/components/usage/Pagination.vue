<script setup lang="ts">
import { computed } from 'vue'

interface Props {
  page: number
  pageSize: number
  total: number
  count: number
}
const props = defineProps<Props>()

const emit = defineEmits<{
  (e: 'first'): void
  (e: 'prev'): void
  (e: 'goto', page: number): void
  (e: 'next'): void
  (e: 'last'): void
}>()

const totalPages = computed(() => {
  if (props.total <= 0 || props.pageSize <= 0) return 0
  return Math.max(1, Math.ceil(props.total / props.pageSize))
})

const safePage = computed(() => {
  if (props.page < 1) return 1
  if (props.page > totalPages.value) return totalPages.value
  return props.page
})

const paginationStart = computed(() =>
  props.total > 0 ? (safePage.value - 1) * props.pageSize + 1 : 0,
)
const paginationEnd = computed(() => {
  if (props.total <= 0) return 0
  return Math.min(props.total, paginationStart.value + props.count - 1)
})

const hasPrevPage = computed(() => safePage.value > 1)
const hasNextPage = computed(() => safePage.value < totalPages.value)

// Page-number window: always show 1 and totalPages, and ±2 around the current
// page, replacing the gap with a single ellipsis. Returns the list of items
// that should be rendered in the numbered slot.
type PageItem =
  | { kind: 'page'; page: number }
  | { kind: 'ellipsis'; key: 'left' | 'right' }

const visiblePages = computed<PageItem[]>(() => {
  const total = totalPages.value
  if (total === 0) return []
  const current = safePage.value
  const window = 2 // pages on either side of current
  const set = new Set<number>([1, total])
  for (let p = current - window; p <= current + window; p++) {
    if (p >= 1 && p <= total) set.add(p)
  }
  const sorted = Array.from(set).sort((a, b) => a - b)
  const out: PageItem[] = []
  let prev = 0
  for (const p of sorted) {
    if (prev !== 0 && p - prev > 1) {
      out.push({ kind: 'ellipsis', key: prev === 1 ? 'right' : 'left' })
    }
    out.push({ kind: 'page', page: p })
    prev = p
  }
  return out
})

function onGoto(p: number) {
  if (p < 1 || p > totalPages.value || p === safePage.value) return
  emit('goto', p)
}
</script>

<template>
  <div class="row-between" style="padding: 12px 16px; border-top: 1px solid rgba(0, 0, 0, 0.05);">
    <div class="text-muted" style="font-size: 12px;">
      显示 {{ total ? paginationStart : 0 }}–{{ total ? paginationEnd : 0 }} / 共 {{ total.toLocaleString() }} 条
    </div>
    <div class="row" style="gap: 4px;" role="group" aria-label="分页">
      <button
        class="btn btn-secondary"
        style="padding: 4px 10px; font-size: 12px;"
        :disabled="!hasPrevPage"
        aria-label="首页"
        @click="emit('first')"
      >« 首页</button>
      <button
        class="btn btn-secondary"
        style="padding: 4px 10px; font-size: 12px;"
        :disabled="!hasPrevPage"
        aria-label="上一页"
        @click="emit('prev')"
      >‹ 上一页</button>
      <template v-for="item in visiblePages" :key="item.kind === 'page' ? item.page : item.key">
        <span
          v-if="item.kind === 'ellipsis'"
          class="text-muted"
          style="padding: 4px 6px; font-size: 12px;"
          aria-hidden="true"
        >…</span>
        <button
          v-else
          class="btn"
          :class="item.page === safePage ? 'btn-primary' : 'btn-secondary'"
          style="padding: 4px 10px; font-size: 12px; min-width: 30px;"
          :aria-current="item.page === safePage ? 'page' : undefined"
          :aria-label="`第 ${item.page} 页`"
          @click="onGoto(item.page)"
        >{{ item.page }}</button>
      </template>
      <button
        class="btn btn-secondary"
        style="padding: 4px 10px; font-size: 12px;"
        :disabled="!hasNextPage"
        aria-label="下一页"
        @click="emit('next')"
      >下一页 ›</button>
      <button
        class="btn btn-secondary"
        style="padding: 4px 10px; font-size: 12px;"
        :disabled="!hasNextPage"
        aria-label="末页"
        @click="emit('last')"
      >末页 »</button>
    </div>
  </div>
</template>