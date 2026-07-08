<script setup lang="ts">
import { computed } from 'vue'
import type { model } from '../../../wailsjs/go/models'
import { chartColors } from '@/composables/useChartFormat'

interface Props {
  data: model.ChartAggregates
}
const props = defineProps<Props>()

const option = computed(() => {
  const breakdown = props.data.status_breakdown || []
  const palette: Record<string, string> = {
    '2xx': chartColors.success,
    '429': chartColors.rateLimited,
    '错误': chartColors.error,
    '其他': '#9ca3af',
  }
  return {
    tooltip: { trigger: 'item' },
    legend: { bottom: 0 },
    series: [
      {
        name: '状态分布',
        type: 'pie',
        radius: ['45%', '70%'],
        center: ['50%', '45%'],
        data: breakdown.map((b) => ({
          value: b.count,
          name: b.label,
          itemStyle: { color: palette[b.label] || '#6e6e73' },
        })),
        label: { formatter: '{b}: {c} ({d}%)' },
      },
    ],
  }
})
</script>

<template>
  <VChart class="chart" :option="option" autoresize :update-options="{ notMerge: true }" /></template>
