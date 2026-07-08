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
  (e: 'prev'): void
  (e: 'next'): void
}>()

const paginationStart = computed(() =>
  props.count > 0 ? (props.page - 1) * props.pageSize + 1 : 0,
)
const paginationEnd = computed(() =>
  props.count > 0 ? paginationStart.value + props.count - 1 : 0,
)
const hasPrevPage = computed(() => props.page > 1)
// TODO(uiux/log-filters): replace this heuristic with hasNextPage computed from
// backend total once QueryLogs returns total count.
const hasNextPage = computed(() => props.count === props.pageSize)
</script>

<template>
  <div class="row-between" style="padding: 12px 16px; border-top: 1px solid rgba(0, 0, 0, 0.05);">
    <div class="text-muted" style="font-size: 12px;">
      显示 {{ count ? paginationStart : 0 }}–{{ count ? paginationEnd : 0 }} / 共 {{ total.toLocaleString() }} 条
    </div>
    <div class="row" style="gap: 6px;" role="group" aria-label="分页">
      <button
        class="btn btn-secondary"
        style="padding: 4px 10px; font-size: 12px;"
        :disabled="!hasPrevPage"
        aria-label="上一页"
        @click="emit('prev')"
      >‹ 上一页</button>
      <button
        class="btn btn-primary"
        style="padding: 4px 10px; font-size: 12px; min-width: 28px;"
        aria-current="page"
      >{{ page }}</button>
      <button
        class="btn btn-secondary"
        style="padding: 4px 10px; font-size: 12px;"
        :disabled="!hasNextPage"
        aria-label="下一页"
        @click="emit('next')"
      >下一页 ›</button>
    </div>
  </div>
</template>
