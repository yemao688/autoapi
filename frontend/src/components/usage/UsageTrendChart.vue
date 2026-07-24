<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { model } from '../../../wailsjs/go/models'
import { chartColors, formatCost, formatTokens } from '@/composables/useChartFormat'

const { t } = useI18n()

interface Props {
  data: model.UsageTrends
  loading?: boolean
}
const props = withDefaults(defineProps<Props>(), { loading: false })

const width = 720
const height = 320
const plot = { left: 54, right: 58, top: 22, bottom: 52 }
const plotWidth = width - plot.left - plot.right
const plotHeight = height - plot.top - plot.bottom

interface Series {
  key: string
  label: string
  color: string
  values: number[]
  fill: boolean
  dashed?: boolean
}

const buckets = computed(() => props.data?.buckets || [])
const hasData = computed(() => buckets.value.length > 0)

const series = computed<Series[]>(() => [
  { key: 'input', label: t('usage.chart.series.input'), color: chartColors.input, values: buckets.value.map((b) => b.input || 0), fill: true },
  { key: 'output', label: t('usage.chart.series.output'), color: chartColors.output, values: buckets.value.map((b) => b.output || 0), fill: true },
  { key: 'cacheCreation', label: t('usage.chart.series.cacheCreation'), color: chartColors.cacheCreation, values: buckets.value.map((b) => b.cache_creation || 0), fill: true },
  { key: 'cacheHit', label: t('usage.chart.series.cacheHit'), color: chartColors.cacheHit, values: buckets.value.map((b) => b.cache_hit || 0), fill: true },
  { key: 'cost', label: t('usage.chart.series.cost'), color: chartColors.cost, values: buckets.value.map((b) => b.cost || 0), fill: false, dashed: true },
])

const tokenMax = computed(() => niceMax(Math.max(...series.value.slice(0, 4).flatMap((s) => s.values), 0)))
const costMax = computed(() => niceMax(Math.max(...(series.value[4]?.values || [0]), 0)))
const tokenTicks = computed(() => makeTicks(tokenMax.value))
const costTicks = computed(() => makeTicks(costMax.value))
const labelIndexes = computed(() => {
  const count = buckets.value.length
  if (count <= 8) return buckets.value.map((_, index) => index)
  return Array.from({ length: 8 }, (_, index) => Math.round(index * (count - 1) / 7))
})

function niceMax(value: number): number {
  if (value <= 0) return 1
  const magnitude = 10 ** Math.floor(Math.log10(value))
  const normalized = value / magnitude
  const step = normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10
  return step * magnitude
}

function makeTicks(max: number): number[] {
  return Array.from({ length: 5 }, (_, index) => max * (4 - index) / 4)
}

function xFor(index: number): number {
  return plot.left + (buckets.value.length <= 1 ? plotWidth / 2 : index * plotWidth / (buckets.value.length - 1))
}

function yFor(value: number, max: number): number {
  return plot.top + plotHeight - Math.max(0, value) / max * plotHeight
}

function linePath(values: number[], max: number): string {
  if (values.length === 0) return ''
  return values.map((value, index) => `${index === 0 ? 'M' : 'L'} ${xFor(index).toFixed(2)} ${yFor(value, max).toFixed(2)}`).join(' ')
}

function areaPath(values: number[]): string {
  if (values.length === 0) return ''
  const line = linePath(values, tokenMax.value)
  return `${line} L ${xFor(values.length - 1).toFixed(2)} ${(plot.top + plotHeight).toFixed(2)} L ${xFor(0).toFixed(2)} ${(plot.top + plotHeight).toFixed(2)} Z`
}

function tickLabel(value: number, isCost = false): string {
  return isCost ? formatCost(value) : formatTokens(value)
}
</script>

<template>
  <div class="trend-chart" :aria-busy="loading">
    <svg
      v-if="hasData"
      class="trend-chart__svg"
      viewBox="0 0 720 320"
      role="img"
      :aria-label="t('usage.chart.axis.tokens') + ' ' + t('usage.chart.title')"
    >
      <title>{{ t('usage.chart.title') }}</title>
      <desc>{{ t('usage.chart.subtitle') }}</desc>
      <g class="trend-chart__grid">
        <line v-for="(tick, index) in tokenTicks" :key="`grid-${index}`" :x1="plot.left" :x2="width - plot.right" :y1="yFor(tick, tokenMax)" :y2="yFor(tick, tokenMax)" />
      </g>
      <g class="trend-chart__axis trend-chart__axis--left">
        <text v-for="(tick, index) in tokenTicks" :key="`token-${index}`" :x="plot.left - 9" :y="yFor(tick, tokenMax) + 3" text-anchor="end">{{ tickLabel(tick) }}</text>
        <text class="trend-chart__axis-title" :transform="`translate(13 ${plot.top + plotHeight / 2}) rotate(-90)`" text-anchor="middle">{{ t('usage.chart.axis.tokens') }}</text>
      </g>
      <g class="trend-chart__axis trend-chart__axis--right">
        <text v-for="(tick, index) in costTicks" :key="`cost-${index}`" :x="width - plot.right + 9" :y="yFor(tick, costMax) + 3">{{ tickLabel(tick, true) }}</text>
        <text class="trend-chart__axis-title" :transform="`translate(${width - 9} ${plot.top + plotHeight / 2}) rotate(90)`" text-anchor="middle">{{ t('usage.chart.axis.cost') }}</text>
      </g>
      <g class="trend-chart__series">
        <template v-for="item in series" :key="item.key">
          <path v-if="item.fill" class="trend-chart__area" :d="areaPath(item.values)" :fill="item.color" />
          <path class="trend-chart__line" :d="linePath(item.values, item.key === 'cost' ? costMax : tokenMax)" :stroke="item.color" :stroke-dasharray="item.dashed ? '5 4' : undefined" />
        </template>
      </g>
      <g class="trend-chart__x-labels">
        <text v-for="index in labelIndexes" :key="`label-${index}`" :x="xFor(index)" :y="height - 27" text-anchor="middle">{{ buckets[index]?.bucket }}</text>
      </g>
      <g class="trend-chart__legend" :transform="`translate(${plot.left} ${height - 8})`">
        <g v-for="(item, index) in series" :key="`legend-${item.key}`" :transform="`translate(${index * 130} 0)`">
          <rect width="9" height="9" rx="2" :fill="item.color" />
          <text x="14" y="8">{{ item.label }}</text>
        </g>
      </g>
    </svg>
    <div v-else class="trend-chart__empty" role="status">
      <span>{{ t('usage.chart.empty') }}</span>
    </div>
  </div>
</template>

<style scoped>
.trend-chart { position: relative; width: 100%; height: 320px; }
.trend-chart__svg { display: block; width: 100%; height: 100%; overflow: visible; }
.trend-chart__grid line { stroke: color-mix(in srgb, var(--text, #1d1d1f) 7%, transparent); stroke-width: 1; }
.trend-chart__axis text, .trend-chart__x-labels text { fill: var(--muted, #6e6e73); font-size: 10px; font-family: inherit; }
.trend-chart__axis-title { font-size: 9px !important; }
.trend-chart__line { fill: none; stroke-width: 2.2; stroke-linecap: round; stroke-linejoin: round; }
.trend-chart__area { opacity: .11; }
.trend-chart__legend text { fill: var(--muted, #6e6e73); font-size: 10px; font-family: inherit; }
.trend-chart__empty { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: var(--muted, #6e6e73); font-size: 13px; }
</style>
