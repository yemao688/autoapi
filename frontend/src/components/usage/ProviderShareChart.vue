<script setup lang="ts">
import { computed } from 'vue'
import type { model } from '../../../wailsjs/go/models'
import { formatTokens } from '@/composables/useChartFormat'

interface Props {
  data: model.ChartAggregates
}
const props = defineProps<Props>()

const providerColors: Record<string, string> = {
  openai: '#10a37f',
  anthropic: '#d97757',
  deepseek: '#272729',
  moonshot: '#0071e3',
  '智谱 glm': '#2563eb',
  glm: '#2563eb',
}

function color(name: string): string {
  return providerColors[name.toLowerCase()] || '#6e6e73'
}

const option = computed(() => {
  const shares = props.data.provider_shares || []
  return {
    tooltip: { trigger: 'item', formatter: (p: any) => `${p.name}<br/>${p.value.toLocaleString()} tokens (${p.percent}%)` },
    legend: { bottom: 0 },
    series: [
      {
        name: 'Provider 占比',
        type: 'pie',
        radius: ['45%', '70%'],
        center: ['50%', '45%'],
        data: shares.map((s) => ({
          value: s.tokens,
          name: s.provider_name,
          itemStyle: { color: color(s.provider_name) },
        })),
        label: { formatter: '{b}: {d}%' },
      },
    ],
  }
})
</script>

<template>
  <VChart class="chart" :option="option" autoresize :update-options="{ notMerge: true }" /></template>
