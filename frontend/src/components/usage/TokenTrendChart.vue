<script setup lang="ts">
import { computed } from 'vue'
import type { model } from '../../../wailsjs/go/models'
import { chartColors, formatCost, formatTokens } from '@/composables/useChartFormat'

interface Props {
  data: model.ChartAggregates
}
const props = defineProps<Props>()

const option = computed(() => {
  const buckets = props.data.buckets || []
  const labels = buckets.map((b) => b.bucket)
  return {
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
    },
    legend: { bottom: 0 },
    grid: { left: 48, right: 64, top: 24, bottom: 40 },
    xAxis: { type: 'category', boundaryGap: false, data: labels },
    yAxis: [
      { type: 'value', name: 'Tokens', axisLabel: { formatter: (v: number) => formatTokens(v) } },
      { type: 'value', name: 'Cost', axisLabel: { formatter: (v: number) => formatCost(v) }, splitLine: { show: false } },
    ],
    series: [
      {
        name: '输入 Token',
        type: 'line',
        stack: 'Total',
        areaStyle: { opacity: 0.2 },
        smooth: true,
        data: buckets.map((b) => b.input_tokens),
        itemStyle: { color: chartColors.input },
        tooltip: { valueFormatter: (v: number) => formatTokens(v) },
      },
      {
        name: '输出 Token',
        type: 'line',
        stack: 'Total',
        areaStyle: { opacity: 0.2 },
        smooth: true,
        data: buckets.map((b) => b.output_tokens),
        itemStyle: { color: chartColors.output },
        tooltip: { valueFormatter: (v: number) => formatTokens(v) },
      },
      {
        name: '成本',
        type: 'line',
        yAxisIndex: 1,
        smooth: true,
        data: buckets.map((b) => b.cost),
        itemStyle: { color: chartColors.cost },
        lineStyle: { type: 'dashed' },
        tooltip: { valueFormatter: (v: number) => formatCost(v) },
      },
    ],
  }
})
</script>

<template>
  <VChart class="chart" :option="option" autoresize :update-options="{ notMerge: true }" /></template>
