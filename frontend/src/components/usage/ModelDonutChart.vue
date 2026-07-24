<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { model } from '../../../wailsjs/go/models'

const { t } = useI18n()

interface Props { data: model.ModelRanking[] }
const props = defineProps<Props>()

const PALETTE = ['#0071e3', '#34c759', '#ff9500', '#af52de', '#ff3b30', '#5856d6', '#00ced1', '#8e8e93']
const TOP_N = 7

interface SliceEntry { label: string; value: number; color: string }
const processedData = computed<{ entries: SliceEntry[]; total: number }>(() => {
  const ranked = (props.data || []).filter((m) => m && m.model && m.requests > 0).slice().sort((a, b) => b.requests - a.requests)
  if (!ranked.length) return { entries: [], total: 0 }
  const head = ranked.slice(0, TOP_N)
  const tailTotal = ranked.slice(TOP_N).reduce((sum, m) => sum + m.requests, 0)
  const entries = head.map((m, index) => ({ label: m.model, value: m.requests, color: PALETTE[index % PALETTE.length] }))
  if (tailTotal > 0) entries.push({ label: t('usage.chart.donutOther'), value: tailTotal, color: PALETTE[TOP_N] })
  return { entries, total: entries.reduce((sum, entry) => sum + entry.value, 0) }
})

const hasData = computed(() => processedData.value.entries.length > 0)
const requestLabel = computed(() => t('usage.modelRanking.requests', { count: 0 }).replace('0', '').trim())
const radius = 68
const circumference = 2 * Math.PI * radius

function dash(entry: SliceEntry): string {
  const length = entry.value / processedData.value.total * circumference
  return `${length} ${circumference - length}`
}

function offset(index: number): number {
  const prior = processedData.value.entries.slice(0, index).reduce((sum, entry) => sum + entry.value, 0)
  return -prior / processedData.value.total * circumference
}

function percentage(entry: SliceEntry): string {
  return `${((entry.value / processedData.value.total) * 100).toFixed(1)}%`
}
</script>

<template>
  <div class="donut-chart" role="status">
    <template v-if="hasData">
      <svg class="donut-chart__svg" viewBox="0 0 280 196" role="img" :aria-label="t('usage.modelRanking.chartTitle')">
        <title>{{ t('usage.modelRanking.chartTitle') }}</title>
        <desc>{{ processedData.entries.map((entry) => `${entry.label}: ${entry.value.toLocaleString()} (${percentage(entry)})`).join(', ') }}</desc>
        <g transform="translate(140 94) rotate(-90)">
          <circle class="donut-chart__track" :r="radius" cx="0" cy="0" fill="none" />
          <circle
            v-for="(entry, index) in processedData.entries"
            :key="entry.label"
            class="donut-chart__slice"
            :r="radius"
            cx="0"
            cy="0"
            fill="none"
            :stroke="entry.color"
            :stroke-dasharray="dash(entry)"
            :stroke-dashoffset="offset(index)"
          />
        </g>
        <text class="donut-chart__total" x="140" y="91" text-anchor="middle">{{ processedData.total.toLocaleString() }}</text>
        <text class="donut-chart__total-label" x="140" y="108" text-anchor="middle">{{ requestLabel }}</text>
      </svg>
      <div class="donut-chart__legend" :aria-label="t('usage.modelRanking.chartTitle')">
        <div v-for="entry in processedData.entries" :key="`legend-${entry.label}`" class="donut-chart__legend-item">
          <span class="donut-chart__swatch" :style="{ backgroundColor: entry.color }" aria-hidden="true" />
          <span class="donut-chart__name" :title="entry.label">{{ entry.label }}</span>
          <span class="donut-chart__value">{{ percentage(entry) }}</span>
        </div>
      </div>
    </template>
    <div v-else class="donut-chart__empty">
      <span>{{ t('usage.chart.empty') }}</span>
    </div>
  </div>
</template>

<style scoped>
.donut-chart { position: relative; width: 100%; height: 280px; }
.donut-chart__svg { display: block; width: 100%; height: 196px; }
.donut-chart__track { stroke: color-mix(in srgb, var(--text, #1d1d1f) 8%, transparent); stroke-width: 25; }
.donut-chart__slice { stroke-width: 25; stroke-linecap: butt; transition: opacity .18s ease, stroke-width .18s ease; }
.donut-chart__slice:hover { opacity: .8; stroke-width: 29; }
.donut-chart__total { fill: var(--text, #1d1d1f); font-size: 22px; font-weight: 600; font-variant-numeric: tabular-nums; }
.donut-chart__total-label { fill: var(--muted, #6e6e73); font-size: 10px; }
.donut-chart__legend { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 7px 12px; padding: 4px 14px 0; }
.donut-chart__legend-item { display: flex; align-items: center; min-width: 0; gap: 6px; color: var(--muted, #6e6e73); font-size: 11px; }
.donut-chart__swatch { flex: 0 0 9px; width: 9px; height: 9px; border-radius: 2px; }
.donut-chart__name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.donut-chart__value { margin-left: auto; font-variant-numeric: tabular-nums; }
.donut-chart__empty { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: var(--muted, #6e6e73); font-size: 13px; }
</style>
