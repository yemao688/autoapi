<script setup lang="ts">
import { computed } from 'vue'
import type { model } from '../../../wailsjs/go/models'
import { chartColors, formatDuration } from '@/composables/useChartFormat'

interface Props {
  data: model.ChartAggregates
}
const props = defineProps<Props>()

const option = computed(() => {
  const buckets = props.data.buckets || []
  const labels = buckets.map((b) => b.bucket)
  return {
    tooltip: { trigger: 'axis' },
    legend: { bottom: 0 },
    grid: { left: 48, right: 24, top: 24, bottom: 40 },
    xAxis: { type: 'category', data: labels },
    yAxis: { type: 'value', name: '延迟', axisLabel: { formatter: (v: number) => formatDuration(v) } },
    series: [
      {
        name: '平均延迟',
        type: 'line',
        smooth: true,
        data: buckets.map((b) => b.avg_latency_ms),
        itemStyle: { color: chartColors.latency },
        tooltip: { valueFormatter: (v: number) => formatDuration(v) },
      },
      {
        name: '首字延迟 (TTFT)',
        type: 'line',
        smooth: true,
        data: buckets.map((b) => b.avg_ttft_ms || 0),
        itemStyle: { color: chartColors.ttft },
        tooltip: { valueFormatter: (v: number) => formatDuration(v) },
      },
    ],
  }
})
</script>

<template>
  <VChart class="chart" :option="option" autoresize :update-options="{ notMerge: true }" /></template>
