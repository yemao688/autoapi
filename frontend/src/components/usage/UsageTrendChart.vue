<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Line } from 'vue-chartjs'
import type { Chart, ChartData, ChartOptions } from 'chart.js'
import type { model } from '../../../wailsjs/go/models'
import { chartColors, formatCost, formatTokens } from '@/composables/useChartFormat'

const { t } = useI18n()

interface Props {
  data: model.UsageTrends
  loading?: boolean
}
const props = withDefaults(defineProps<Props>(), { loading: false })

// Build five series. Cost is plotted on a secondary Y axis (right), the
// other four (input / output / cacheCreation / cacheHit) share the primary
// left axis. Order in the dataset array matches the legend stack order.
const chartData = computed<ChartData<'line'>>(() => {
  const buckets = props.data?.buckets || []
  const labels = buckets.map((b) => b.bucket)
  return {
    labels,
    datasets: [
      {
        label: t('usage.chart.series.input'),
        data: buckets.map((b) => b.input),
        borderColor: chartColors.input,
        backgroundColor: makeBackgroundColor(chartColors.input),
        fill: true,
        tension: 0.32,
        yAxisID: 'yTokens',
        pointRadius: 0,
        pointHoverRadius: 4,
        borderWidth: 2,
      },
      {
        label: t('usage.chart.series.output'),
        data: buckets.map((b) => b.output),
        borderColor: chartColors.output,
        backgroundColor: makeBackgroundColor(chartColors.output),
        fill: true,
        tension: 0.32,
        yAxisID: 'yTokens',
        pointRadius: 0,
        pointHoverRadius: 4,
        borderWidth: 2,
      },
      {
        label: t('usage.chart.series.cacheCreation'),
        data: buckets.map((b) => b.cache_creation),
        borderColor: chartColors.cacheCreation,
        backgroundColor: makeBackgroundColor(chartColors.cacheCreation),
        fill: true,
        tension: 0.32,
        yAxisID: 'yTokens',
        pointRadius: 0,
        pointHoverRadius: 4,
        borderWidth: 2,
      },
      {
        label: t('usage.chart.series.cacheHit'),
        data: buckets.map((b) => b.cache_hit),
        borderColor: chartColors.cacheHit,
        backgroundColor: makeBackgroundColor(chartColors.cacheHit),
        fill: true,
        tension: 0.32,
        yAxisID: 'yTokens',
        pointRadius: 0,
        pointHoverRadius: 4,
        borderWidth: 2,
      },
      {
        label: t('usage.chart.series.cost'),
        data: buckets.map((b) => b.cost),
        borderColor: chartColors.cost,
        backgroundColor: chartColors.cost,
        fill: false,
        tension: 0.32,
        yAxisID: 'yCost',
        pointRadius: 0,
        pointHoverRadius: 4,
        borderWidth: 2,
        borderDash: [4, 3],
      },
    ],
  }
})

const chartOptions = computed<ChartOptions<'line'>>(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: { mode: 'index', intersect: false },
  plugins: {
    legend: { position: 'bottom', labels: { boxWidth: 10, boxHeight: 10, font: { size: 11 } } },
    tooltip: {
      callbacks: {
        label: (ctx) => {
          const v = ctx.parsed.y ?? 0
          if (ctx.dataset.yAxisID === 'yCost') return `${ctx.dataset.label}: ${formatCost(v)}`
          return `${ctx.dataset.label}: ${formatTokens(v)}`
        },
      },
    },
  },
  scales: {
    x: {
      grid: { display: false },
      ticks: { font: { size: 10 }, maxRotation: 0, autoSkip: true, maxTicksLimit: 8 },
    },
    yTokens: {
      type: 'linear',
      position: 'left',
      beginAtZero: true,
      title: { display: true, text: t('usage.chart.axis.tokens'), font: { size: 10 } },
      grid: { color: 'rgba(0,0,0,0.05)' },
      ticks: { font: { size: 10 }, callback: (v) => formatTokens(Number(v)) },
    },
    yCost: {
      type: 'linear',
      position: 'right',
      beginAtZero: true,
      title: { display: true, text: t('usage.chart.axis.cost'), font: { size: 10 } },
      grid: { display: false },
      ticks: { font: { size: 10 }, callback: (v) => formatCost(Number(v)) },
    },
  },
}))

// Per-color gradient cache. The outer key is the hex so each color has its
// own bucket; the inner WeakMap keys the gradient by the Chart instance plus
// the chartArea top/bottom so a resize rebuilds once, but a plain re-render
// reuses the existing gradient instead of allocating a new canvas.
interface CachedGradient {
  top: number
  bottom: number
  gradient: CanvasGradient
}
const gradientCache = new Map<string, WeakMap<Chart, CachedGradient>>()

// Scriptable backgroundColor that paints a vertical gradient using the chart's
// own 2D context and current `chartArea` (so the fade ends at the bottom of
// the plot area, not an arbitrary 220 px). Falls back to the flat hex if the
// chart isn't mounted yet or the gradient cannot be created.
function makeBackgroundColor(hex: string): (ctx: { chart: Chart }) => CanvasGradient | string {
  return (ctx) => {
    const chart = ctx.chart
    if (!chart) return hex
    const area = chart.chartArea
    if (!area) return hex
    const ctx2d = chart.ctx
    if (!ctx2d) return hex

    let bucket = gradientCache.get(hex)
    const cached = bucket?.get(chart)
    if (cached && cached.top === area.top && cached.bottom === area.bottom) {
      return cached.gradient
    }

    const height = area.bottom - area.top
    if (height <= 0) return hex
    let gradient: CanvasGradient
    try {
      gradient = ctx2d.createLinearGradient(0, area.top, 0, area.bottom)
      gradient.addColorStop(0, hexAlpha(hex, 0.32))
      gradient.addColorStop(1, hexAlpha(hex, 0))
    } catch {
      return hex
    }

    if (!bucket) {
      bucket = new WeakMap()
      gradientCache.set(hex, bucket)
    }
    bucket.set(chart, { top: area.top, bottom: area.bottom, gradient })
    return gradient
  }
}

function hexAlpha(hex: string, alpha: number): string {
  // Accept #RGB or #RRGGBB; default to grey on parse failure.
  let r = 0, g = 0, b = 0
  if (hex.length === 4) {
    r = parseInt(hex[1] + hex[1], 16)
    g = parseInt(hex[2] + hex[2], 16)
    b = parseInt(hex[3] + hex[3], 16)
  } else if (hex.length === 7) {
    r = parseInt(hex.slice(1, 3), 16)
    g = parseInt(hex.slice(3, 5), 16)
    b = parseInt(hex.slice(5, 7), 16)
  } else {
    return hex
  }
  return `rgba(${r}, ${g}, ${b}, ${alpha})`
}
</script>

<template>
  <div class="trend-chart" :aria-busy="loading">
    <Line
      v-if="(data?.buckets?.length ?? 0) > 0"
      :data="chartData"
      :options="chartOptions"
    />
    <div v-else class="trend-chart__empty">
      <span>{{ t('usage.chart.empty') }}</span>
    </div>
  </div>
</template>

<style scoped>
.trend-chart {
  position: relative;
  width: 100%;
  height: 320px;
}
.trend-chart__empty {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--muted, #6e6e73);
  font-size: 13px;
}
</style>
