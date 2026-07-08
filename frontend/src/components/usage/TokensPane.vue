<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { model } from '../../../wailsjs/go/models'
import UsageTrendChart from './UsageTrendChart.vue'

const { t } = useI18n()

interface Props {
  tokenStats: model.Stat[]
  modelRanking: model.ModelRanking[]
  providerShares: model.ProviderShare[]
  chartData: model.UsageTrends
}
defineProps<Props>()

const providerColors: Record<string, string> = {
  openai: '#10a37f',
  anthropic: '#d97757',
  deepseek: '#272729',
  moonshot: '#0071e3',
  '智谱 glm': '#2563eb',
  glm: '#2563eb',
}

function providerColor(name: string): string {
  return providerColors[name.toLowerCase()] || '#6e6e73'
}

function formatNumber(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return String(n)
}
</script>

<template>
  <div class="view-pane" role="tabpanel" id="usage-pane-tokens" aria-labelledby="usage-tab-tokens" data-pane-group="usage-view" data-pane-id="tokens" tabindex="0">

    <section class="stat-grid-4" style="gap: 16px; margin-bottom: 24px;">
      <div v-for="(stat, idx) in tokenStats" :key="stat.label + idx" class="metric-card">
        <div class="metric-label">{{ stat.label }}</div>
        <div class="metric-value">{{ stat.value }}</div>
        <div class="metric-meta">
          <span class="metric-trend" :class="stat.trend">{{ stat.delta }}</span>
          <span>{{ stat.note }}</span>
        </div>
      </div>
    </section>

    <section class="card" style="padding: 24px;">
      <div class="row-between" style="margin-bottom: 20px;">
        <div>
          <div class="card-title" style="margin: 0;">{{ t('usage.chart.title') }}</div>
          <div class="text-muted" style="font-size: 12px; margin-top: 4px;">{{ t('usage.chart.subtitle') }}</div>
        </div>
      </div>
      <UsageTrendChart :data="chartData" />
    </section>

    <section class="col-2">
      <div class="card">
        <div class="card-title"><span>{{ t('usage.providerShares.title') }}</span><span class="card-title-link" style="text-transform:none;">{{ t('usage.providerShares.month') }}</span></div>
        <div class="stack-tight" style="padding-top: 4px;">
          <div v-for="p in providerShares" :key="p.provider_id" class="list-row" style="padding: 8px 0;">
            <div class="row" style="gap: 8px; align-items: center; min-width: 0;">
              <span class="chart-legend-swatch" :style="{ background: providerColor(p.provider_name) }"></span>
              <span style="font-size: 13px;">{{ p.provider_name }}</span>
            </div>
            <div class="text-mono" style="font-size: 13px; font-weight: 500; text-align: right;">
              <div>{{ p.percent }}%</div>
              <div class="text-muted" style="font-size: 11px; font-weight: 400;">{{ t('usage.providerShares.tokensWithCost', { tokens: formatNumber(p.tokens), cost: p.cost.toFixed(2) }) }}</div>
            </div>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-title"><span>{{ t('usage.modelRanking.title') }}</span><RouterLink class="card-title-link" to="/usage-stats" style="text-transform:none;">{{ t('usage.modelRanking.detail') }}</RouterLink></div>
        <div class="stack-tight" style="padding-top: 4px;">
          <div v-for="(m, idx) in modelRanking" :key="m.model" class="list-row" style="padding: 8px 0;">
            <div class="text-mono text-muted" style="width: 18px; font-size: 12px;">{{ String(idx + 1).padStart(2, '0') }}</div>
            <div class="list-main">
              <div class="text-mono" style="font-size: 13px;">{{ m.model }}</div>
              <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">{{ m.provider_name }} · {{ t('usage.modelRanking.requests', { count: m.requests.toLocaleString() }) }}</div>
            </div>
            <div class="list-meta" style="min-width: 80px; text-align: right;">
              <div>{{ formatNumber(m.tokens) }}</div>
              <div class="text-mono list-meta-sub">${{ m.cost.toFixed(2) }}</div>
            </div>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>
