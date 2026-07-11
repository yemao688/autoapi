<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Doughnut } from 'vue-chartjs'
import type { ChartData, ChartOptions } from 'chart.js'
import type { model } from '../../../wailsjs/go/models'

const { t } = useI18n()

interface Props {
  data: model.ModelRanking[]
}
const props = defineProps<Props>()

// Eight-color palette tuned for the donut so adjacent slices stay legible
// against the white card surface and the dark theme background alike.
const PALETTE = [
  '#0071e3',
  '#34c759',
  '#ff9500',
  '#af52de',
  '#ff3b30',
  '#5856d6',
  '#00ced1',
  '#8e8e93',
]

// Top-N + Other grouping. Sorting by request count keeps the largest slices
// visible while "Other" aggregates the long tail. We deliberately keep
// more than seven visible (top 7 labeled + Other) so the leading slices
// stay readable instead of being squeezed by tiny tail entries.
const TOP_N = 7

interface SliceEntry {
  label: string
  value: number
  color: string
}

const processedData = computed<{ entries: SliceEntry[]; total: number }>(() => {
  const ranked = (props.data || [])
    .filter((m) => m && m.model && m.requests > 0)
    .slice()
    .sort((a, b) => b.requests - a.requests)

  if (ranked.length === 0) {
    return { entries: [], total: 0 }
  }

  const head = ranked.slice(0, TOP_N)
  const tail = ranked.slice(TOP_N)
  const tailTotal = tail.reduce((sum, m) => sum + m.requests, 0)

  const entries: SliceEntry[] = head.map((m, i) => ({
    label: m.model,
    value: m.requests,
    color: PALETTE[i % PALETTE.length],
  }))

  if (tailTotal > 0) {
    entries.push({
      label: t('usage.chart.donutOther'),
      value: tailTotal,
      color: PALETTE[TOP_N % PALETTE.length],
    })
  }

  const total = entries.reduce((sum, e) => sum + e.value, 0)
  return { entries, total }
})

const chartData = computed<ChartData<'doughnut'>>(() => ({
  labels: processedData.value.entries.map((e) => e.label),
  datasets: [
    {
      data: processedData.value.entries.map((e) => e.value),
      backgroundColor: processedData.value.entries.map((e) => e.color),
      borderColor: 'rgba(255, 255, 255, 0.9)',
      borderWidth: 2,
      hoverOffset: 6,
    },
  ],
}))

const chartOptions = computed<ChartOptions<'doughnut'>>(() => ({
  responsive: true,
  maintainAspectRatio: false,
  cutout: '62%',
  plugins: {
    legend: {
      position: 'bottom',
      labels: { boxWidth: 10, boxHeight: 10, font: { size: 11 } },
    },
    tooltip: {
      callbacks: {
        label: (ctx) => {
          const value = Number(ctx.parsed) || 0
          const total = processedData.value.total || 0
          const pct = total > 0 ? ((value / total) * 100).toFixed(1) : '0'
          return `${ctx.label}: ${value.toLocaleString()} · ${pct}%`
        },
      },
    },
  },
}))

const hasData = computed(() => processedData.value.entries.length > 0)
</script>

<template>
  <div class="donut-chart" role="status">
    <Doughnut
      v-if="hasData"
      :data="chartData"
      :options="chartOptions"
    />
    <div v-else class="donut-chart__empty">
      <span>{{ t('usage.chart.empty') }}</span>
    </div>
  </div>
</template>

<style scoped>
.donut-chart {
  position: relative;
  width: 100%;
  height: 280px;
}
.donut-chart__empty {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--muted, #6e6e73);
  font-size: 13px;
}
</style>
