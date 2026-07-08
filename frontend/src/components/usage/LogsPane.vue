<script setup lang="ts">
import type { model } from '../../../wailsjs/go/models'
import LogTable from './LogTable.vue'
import Pagination from './Pagination.vue'
import UsageTrendChart from './UsageTrendChart.vue'

interface Props {
  logs: model.RequestLog[]
  logStats: model.Stat[]
  logTotal: number
  logPage: number
  logPageSize: number
  chartData: model.UsageTrends
}
defineProps<Props>()

const emit = defineEmits<{
  (e: 'first'): void
  (e: 'prev'): void
  (e: 'goto', page: number): void
  (e: 'next'): void
  (e: 'last'): void
  (e: 'clearFilters'): void
}>()
</script>

<template>
  <div class="view-pane" role="tabpanel" id="usage-pane-logs" aria-labelledby="usage-tab-logs" data-pane-group="usage-view" data-pane-id="logs" tabindex="0">

    <section class="stat-grid-4" style="gap: 16px; margin-bottom: 24px;">
      <div v-for="(stat, idx) in logStats" :key="stat.label + idx" class="metric-card">
        <div class="metric-label">{{ stat.label }}</div>
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
          <div class="card-title" style="margin: 0;">使用趋势</div>
          <div class="text-muted" style="font-size: 12px; margin-top: 4px;">输入 / 输出 / 缓存 / 成本 · 按时间聚合</div>
        </div>
      </div>
      <UsageTrendChart :data="chartData" />
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
