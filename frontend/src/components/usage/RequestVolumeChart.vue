<script setup lang="ts">
import { computed } from 'vue'
import type { model } from '../../../wailsjs/go/models'
import { chartColors, formatNumber } from '@/composables/useChartFormat'

interface Props {
  data: model.ChartAggregates
}
const props = defineProps<Props>()

const option = computed(() => {
  const buckets = props.data.buckets || []
  const labels = buckets.map((b) => b.bucket)
  return {
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    legend: { bottom: 0 },
    grid: { left: 48, right: 24, top: 24, bottom: 40 },
    xAxis: { type: 'category', boundaryGap: false, data: labels },
    yAxis: { type: 'value', name: '请求' },
    series: [
      {
        name: '成功',
        type: 'line',
        stack: 'Total',
        areaStyle: { opacity: 0.2 },
        smooth: true,
        data: buckets.map((b) => b.success),
        itemStyle: { color: chartColors.success },
        tooltip: { valueFormatter: (v: number) => formatNumber(v) },
      },
      {
        name: '错误',
        type: 'line',
        stack: 'Total',
        areaStyle: { opacity: 0.2 },
        smooth: true,
        data: buckets.map((b) => b.error),
        itemStyle: { color: chartColors.error },
        tooltip: { valueFormatter: (v: number) => formatNumber(v) },
      },
      {
        name: '限流',
        type: 'line',
        stack: 'Total',
        areaStyle: { opacity: 0.2 },
        smooth: true,
        data: buckets.map((b) => b.rate_limited),
        itemStyle: { color: chartColors.rateLimited },
        tooltip: { valueFormatter: (v: number) => formatNumber(v) },
      },
    ],
  }
})
</script>

<template>
  <VChart class="chart" :option="option" autoresize :update-options="{ notMerge: true }" /></template>
