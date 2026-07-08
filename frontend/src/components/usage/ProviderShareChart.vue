<script setup lang="ts">
import { computed } from 'vue'
import type { model } from '../../../wailsjs/go/models'
import { formatTokens } from '@/composables/useChartFormat'

import { useProviderStyle } from '@/composables/useProviderStyle'

interface Props {
  data: model.ChartAggregates
}
const props = defineProps<Props>()

const { color } = useProviderStyle()

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
