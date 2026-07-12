<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { model } from '../../../wailsjs/go/models'
import LogTable from './LogTable.vue'
import Pagination from './Pagination.vue'
import UsageTrendChart from './UsageTrendChart.vue'

const { t } = useI18n()

interface Props {
  logs: model.RequestLog[]
  logStats: model.Stat[]
  logTotal: number
  logPage: number
  logPageSize: number
  chartData: model.UsageTrends
  isVisible: boolean
  activePane: 'logs' | 'tokens'
}
const props = defineProps<Props>()

const emit = defineEmits<{
  (e: 'first'): void
  (e: 'prev'): void
  (e: 'goto', page: number): void
  (e: 'next'): void
  (e: 'last'): void
  (e: 'clearFilters'): void
}>()

// Mount the chart only when the app is visible and this pane is active.
// Unmounting releases the Chart.js canvas/context via vue-chartjs' destroy.
const shouldMountChart = computed(() => props.isVisible && props.activePane === 'logs')
</script>

<template>
  <div class="view-pane" role="tabpanel" id="usage-pane-logs" aria-labelledby="usage-tab-logs" data-pane-group="usage-view" data-pane-id="logs" tabindex="0">

    <section class="stat-grid-4" style="gap: 16px; margin-bottom: 24px;">
      <div v-for="(stat, idx) in logStats" :key="stat.label + idx" class="metric-card">
        <div class="metric-label">{{ t(stat.label) }}</div>
        <div class="metric-value">{{ stat.value }}</div>
        <div class="metric-meta">
          <span class="metric-trend" :class="stat.trend">{{ stat.delta }}</span>
          <span>{{ stat.note }}</span>
        </div>
      </div>
    </section>

    <section class="card" style="padding: 24px; margin-bottom: 24px;">
      <div class="row-between" style="margin-bottom: 20px;">
        <div>
          <div class="card-title" style="margin: 0;">{{ t('usage.chart.title') }}</div>
          <div class="text-muted" style="font-size: 12px; margin-top: 4px;">{{ t('usage.chart.subtitle') }}</div>
        </div>
      </div>
      <UsageTrendChart v-if="shouldMountChart" :data="chartData" />
    </section>

    <section class="card" style="padding: 0; overflow: hidden;">
      <LogTable :logs="logs" @clearFilters="emit('clearFilters')" />
      <Pagination
        :page="logPage"
        :pageSize="logPageSize"
        :total="logTotal"
        :count="logs.length"
        @first="emit('first')"
        @prev="emit('prev')"
        @goto="(p) => emit('goto', p)"
        @next="emit('next')"
        @last="emit('last')"
      />
    </section>
  </div>
</template>
