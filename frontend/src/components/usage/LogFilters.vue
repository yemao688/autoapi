<script setup lang="ts">
import type { ProviderOption } from '@/types/usage'

interface Props {
  providerOptions: ProviderOption[]
  provider: string
  status: string
  showStatus?: boolean
}
defineProps<Props>()

const emit = defineEmits<{
  (e: 'update:provider', value: string): void
  (e: 'update:status', value: string): void
  (e: 'clear'): void
}>()

function onProviderChange(e: Event) {
  emit('update:provider', (e.target as HTMLSelectElement).value)
}

function onStatusChange(e: Event) {
  emit('update:status', (e.target as HTMLSelectElement).value)
}
</script>

<template>
  <div class="filter-bar">
    <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" aria-label="选择日期范围">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;" aria-hidden="true"><rect x="3" y="4" width="18" height="18" rx="2"/><path d="M16 2v4M8 2v4M3 10h18"/></svg>
      本月
    </button>
    <select
      :value="provider"
      class="select"
      style="width: auto; padding: 5px 10px; font-size: 12.5px;"
      aria-label="按 Provider 筛选"
      @change="onProviderChange"
    >
      <option v-for="opt in providerOptions" :key="opt.id" :value="opt.id">Provider · {{ opt.name }}</option>
    </select>
    <select
      v-if="showStatus"
      :value="status"
      class="select"
      style="width: auto; padding: 5px 10px; font-size: 12.5px;"
      aria-label="按状态筛选"
      @change="onStatusChange"
    >
      <option>全部</option>
      <option>成功</option>
      <option>失败</option>
      <option>限流</option>
    </select>
    <div class="filter-spacer"></div>
    <button class="btn btn-ghost" style="font-size: 12.5px; padding: 5px 10px;" @click="emit('clear')">清除筛选</button>
  </div>
</template>