<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { model } from '../../../wailsjs/go/models'
import { useCompactNumber } from '@/composables/useCompactNumber'
import ModelDonutChart from './ModelDonutChart.vue'

const { t } = useI18n()
const { format: compact } = useCompactNumber()

interface Props {
  tokenStats: model.Stat[]
  modelRanking: model.ModelRanking[]
  modelRankingFull: model.ModelRanking[]
  providerShares: model.ProviderShare[]
  isVisible: boolean
  activePane: 'logs' | 'tokens'
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

function providerColor(name: string): string {
  return providerColors[name.toLowerCase()] || '#6e6e73'
}

function formatStatValue(stat: { label: string; value: string }): string {
  if (stat.label === 'usage.stats.totalTokens') {
    const n = Number(stat.value)
    if (Number.isFinite(n)) return compact(n)
  }
  return stat.value
}

// Mount the chart only when the app is visible and this pane is active.
// Unmounting releases the Chart.js canvas/context via vue-chartjs' destroy.
const shouldMountChart = computed(() => props.isVisible && props.activePane === 'tokens')
</script>

<template>
  <div class="view-pane" role="tabpanel" id="usage-pane-tokens" aria-labelledby="usage-tab-tokens" data-pane-group="usage-view" data-pane-id="tokens" tabindex="0">

    <section class="stat-grid-4" style="gap: 16px; margin-bottom: 24px;">
      <div v-for="(stat, idx) in tokenStats" :key="stat.label + idx" class="metric-card">
        <div class="metric-label">{{ t(stat.label) }}</div>
        <div class="metric-value">{{ formatStatValue(stat) }}</div>
        <div class="metric-meta">
          <span class="metric-trend" :class="stat.trend">{{ stat.delta }}</span>
          <span>{{ stat.note }}</span>
        </div>
      </div>
    </section>

    <section class="card token-card">
      <div class="card-title token-card-title">
        <span>{{ t('usage.modelRanking.chartTitle') }}</span>
      </div>
      <ModelDonutChart v-if="shouldMountChart" :data="modelRankingFull" />
    </section>

    <section class="col-2 token-lists">
      <div class="card">
        <div class="card-title">
          <span>{{ t('usage.providerShares.title') }}</span>
        </div>
        <div class="stack-tight shares-list">
          <div v-for="p in providerShares" :key="p.provider_id" class="list-row share-row">
            <div class="row share-row-name">
              <span class="chart-legend-swatch" :style="{ background: providerColor(p.provider_name) }"></span>
              <span class="share-row-label">{{ p.provider_name }}</span>
            </div>
            <div class="share-row-meta text-mono">
              <div class="share-row-percent">{{ p.percent }}%</div>
              <div class="text-muted share-row-sub">
                {{ t('usage.providerShares.tokensWithCost', { tokens: compact(p.tokens), cost: p.cost.toFixed(2) }) }}
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-title">
          <span>{{ t('usage.modelRanking.title') }}</span>
        </div>
        <div class="stack-tight ranking-list">
          <div v-for="(m, idx) in modelRanking" :key="m.model" class="list-row ranking-row">
            <div class="ranking-rank text-mono">{{ String(idx + 1).padStart(2, '0') }}</div>
            <div class="list-main ranking-main">
              <div class="text-mono ranking-model">{{ m.model }}</div>
              <div class="text-muted ranking-sub">
                {{ m.provider_name }} · {{ t('usage.modelRanking.requests', { count: compact(m.requests) }) }}
              </div>
            </div>
            <div class="ranking-meta text-mono">
              <div>{{ compact(m.tokens) }}</div>
              <div class="text-muted ranking-meta-sub">${{ m.cost.toFixed(2) }}</div>
            </div>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
/* ===== Donut card: tighter header so the chart gets more breathing room. ===== */
.token-card {
  padding: 24px;
  margin-bottom: 20px;
}
.token-card-title {
  margin-bottom: 16px;
}

/* ===== Provider share list ===== */
.shares-list {
  padding-top: 4px;
}
.share-row {
  padding: 9px 0;
  gap: 12px;
}
.share-row-name {
  gap: 10px;
  min-width: 0;
  flex: 1;
}
.share-row-label {
  font-size: 13px;
  font-weight: 500;
  color: var(--fg);
  letter-spacing: -0.005em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.share-row-meta {
  text-align: right;
  font-size: 13px;
  font-weight: 500;
  flex-shrink: 0;
}
.share-row-percent {
  font-variant-numeric: tabular-nums;
  color: var(--fg);
}
.share-row-sub {
  font-size: 11px;
  font-weight: 400;
  margin-top: 1px;
  letter-spacing: -0.005em;
}

/* ===== Model ranking list ===== */
.ranking-list {
  padding-top: 4px;
}
.ranking-row {
  padding: 9px 0;
  gap: 12px;
}
.ranking-rank {
  width: 22px;
  height: 22px;
  border-radius: 6px;
  background: rgba(0, 113, 227, 0.08);
  color: var(--accent);
  font-size: 11px;
  font-weight: 600;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  letter-spacing: 0;
  font-variant-numeric: tabular-nums;
}
.ranking-main {
  min-width: 0;
}
.ranking-model {
  font-size: 13px;
  font-weight: 500;
  color: var(--fg);
  letter-spacing: -0.005em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ranking-sub {
  font-size: 11.5px;
  margin-top: 1px;
  letter-spacing: -0.005em;
}
.ranking-meta {
  font-size: 13px;
  font-weight: 500;
  text-align: right;
  min-width: 88px;
  flex-shrink: 0;
  font-variant-numeric: tabular-nums;
}
.ranking-meta-sub {
  font-size: 11px;
  font-weight: 400;
  margin-top: 1px;
}

html[data-theme="dark"] .ranking-rank {
  background: rgba(10, 132, 255, 0.18);
  color: #4aa3ff;
}

/* ===== Layout polish ===== */
.token-lists {
  margin-top: 20px;
}
@media (max-width: 640px) {
  .share-row-label,
  .ranking-model {
    font-size: 12.5px;
  }
  .ranking-meta {
    min-width: 76px;
  }
}
</style>