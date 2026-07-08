<script setup lang="ts">
import type { ProviderOption } from '@/types/usage'

export interface RouteOption {
  id: string
  name: string
}

export type DateRangePreset = 'today' | 'day' | 'week' | 'month' | 'custom'

interface Props {
  providerOptions: ProviderOption[]
  provider: string
  status: string
  route: string
  model: string
  search: string
  dateRangePreset: DateRangePreset
  showStatus?: boolean
  routeOptions: RouteOption[]
}
defineProps<Props>()

const emit = defineEmits<{
  (e: 'update:provider', value: string): void
  (e: 'update:status', value: string): void
  (e: 'update:route', value: string): void
  (e: 'update:model', value: string): void
  (e: 'update:search', value: string): void
  (e: 'update:dateRangePreset', value: DateRangePreset): void
  (e: 'clear'): void
}>()

function onProviderChange(e: Event) {
  emit('update:provider', (e.target as HTMLSelectElement).value)
}

function onStatusChange(e: Event) {
  emit('update:status', (e.target as HTMLSelectElement).value)
}

function onRouteChange(e: Event) {
  emit('update:route', (e.target as HTMLSelectElement).value)
}

function onModelInput(e: Event) {
  emit('update:model', (e.target as HTMLInputElement).value)
}

function onSearchInput(e: Event) {
  emit('update:search', (e.target as HTMLInputElement).value)
}

function onDateRangeChange(e: Event) {
  emit('update:dateRangePreset', (e.target as HTMLSelectElement).value as DateRangePreset)
}

const presetOptions: { value: DateRangePreset; label: string }[] = [
  { value: 'today', label: '今天' },
  { value: 'day', label: '近 24 小时' },
  { value: 'week', label: '近 7 天' },
  { value: 'month', label: '本月' },
  { value: 'custom', label: '自定义' },
]
</script>

<template>
  <div class="filter-bar">
    <select
      :value="dateRangePreset"
      class="select"
      style="width: auto; padding: 5px 10px; font-size: 12.5px;"
      aria-label="选择时间范围"
      @change="onDateRangeChange"
    >
      <option v-for="opt in presetOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
    </select>
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
      :value="route"
      class="select"
      style="width: auto; padding: 5px 10px; font-size: 12.5px;"
      aria-label="按路由筛选"
      @change="onRouteChange"
    >
      <option v-for="opt in routeOptions" :key="opt.id" :value="opt.id">路由 · {{ opt.name }}</option>
    </select>
    <input
      :value="model"
      type="text"
      class="input"
      style="width: auto; padding: 5px 10px; font-size: 12.5px; min-width: 140px;"
      placeholder="模型"
      aria-label="按模型筛选"
      @input="onModelInput"
    />
    <input
      :value="search"
      type="text"
      class="input"
      style="width: auto; padding: 5px 10px; font-size: 12.5px; min-width: 160px;"
      placeholder="搜索 模型 / 路由 / 错误"
      aria-label="搜索"
      @input="onSearchInput"
    />
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