<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ProviderOption } from '@/types/usage'

const { t } = useI18n()

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
  { value: 'today', label: t('usage.presets.today') },
  { value: 'day', label: t('usage.presets.day') },
  { value: 'week', label: t('usage.presets.week') },
  { value: 'month', label: t('usage.presets.month') },
  { value: 'custom', label: t('usage.presets.custom') },
]
</script>

<template>
  <div class="filter-bar">
    <select
      :value="dateRangePreset"
      class="select"
      style="width: auto; padding: 5px 28px 5px 10px; font-size: 12.5px; flex: 0 0 auto;"
      :aria-label="t('usage.filters.presetAria')"
      @change="onDateRangeChange"
    >
      <option v-for="opt in presetOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
    </select>
    <select
      :value="provider"
      class="select"
      style="width: auto; padding: 5px 28px 5px 10px; font-size: 12.5px; flex: 0 0 auto;"
      :aria-label="t('usage.filters.providerAria')"
      @change="onProviderChange"
    >
      <option v-for="opt in providerOptions" :key="opt.id" :value="opt.id">{{ t('usage.filters.providerOption', { name: opt.name }) }}</option>
    </select>
    <select
      :value="route"
      class="select"
      style="width: auto; padding: 5px 28px 5px 10px; font-size: 12.5px; flex: 0 0 auto;"
      :aria-label="t('usage.filters.routeAria')"
      @change="onRouteChange"
    >
      <option v-for="opt in routeOptions" :key="opt.id" :value="opt.id">{{ t('usage.filters.routeOption', { name: opt.name }) }}</option>
    </select>
    <input
      :value="model"
      type="text"
      class="input"
      style="width: auto; padding: 5px 10px; font-size: 12.5px; min-width: 0; flex: 1 1 120px;"
      :placeholder="t('usage.filters.modelPlaceholder')"
      :aria-label="t('usage.filters.modelAria')"
      @input="onModelInput"
    />
    <input
      :value="search"
      type="text"
      class="input"
      style="width: auto; padding: 5px 10px; font-size: 12.5px; min-width: 0; flex: 1 1 120px;"
      :placeholder="t('usage.filters.searchPlaceholder')"
      :aria-label="t('usage.filters.searchAria')"
      @input="onSearchInput"
    />
    <select
      v-if="showStatus"
      :value="status"
      class="select"
      style="width: auto; padding: 5px 28px 5px 10px; font-size: 12.5px; flex: 0 0 auto;"
      :aria-label="t('usage.filters.statusAria')"
      @change="onStatusChange"
    >
      <option value="all">{{ t('usage.status.all') }}</option>
      <option value="success">{{ t('usage.status.success') }}</option>
      <option value="failed">{{ t('usage.status.failed') }}</option>
      <option value="rate_limited">{{ t('usage.status.rateLimited') }}</option>
    </select>
    <div class="filter-spacer"></div>
    <button class="btn btn-ghost" style="font-size: 12.5px; padding: 5px 10px;" @click="emit('clear')">{{ t('usage.filters.clear') }}</button>
  </div>
</template>
