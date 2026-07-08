<script setup lang="ts">
import type { model } from '../../../wailsjs/go/models'
import TokenTrendChart from './TokenTrendChart.vue'
import ProviderShareChart from './ProviderShareChart.vue'

interface Props {
  tokenStats: model.Stat[]
  modelRanking: model.ModelRanking[]
  chartData: model.ChartAggregates
}
defineProps<Props>()

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
          <div class="card-title" style="margin: 0;">Token 用量趋势</div>
          <div class="text-muted" style="font-size: 12px; margin-top: 4px;">按时间聚合 · 输入 / 输出 / 成本</div>
        </div>
      </div>
      <TokenTrendChart :data="chartData" />
    </section>

    <section class="col-2">
      <div class="card">
        <div class="card-title"><span>Provider 占比</span></div>
        <ProviderShareChart :data="chartData" />
      </div>

      <div class="card">
        <div class="card-title"><span>模型用量排行</span><RouterLink class="card-title-link" to="/usage-stats" style="text-transform:none;">详情</RouterLink></div>
        <div class="stack-tight" style="padding-top: 4px;">
          <div v-for="(m, idx) in modelRanking" :key="m.model" class="list-row" style="padding: 8px 0;">
            <div class="text-mono text-muted" style="width: 18px; font-size: 12px;">{{ String(idx + 1).padStart(2, '0') }}</div>
            <div class="list-main">
              <div class="text-mono" style="font-size: 13px;">{{ m.model }}</div>
              <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">{{ m.provider_name }} · {{ m.requests.toLocaleString() }} 请求</div>
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