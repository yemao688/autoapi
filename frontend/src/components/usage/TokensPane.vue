<script setup lang="ts">
import type { model } from '../../../wailsjs/go/models'

interface Props {
  tokenStats: model.Stat[]
  providerShares: model.ProviderShare[]
  modelRanking: model.ModelRanking[]
  totalTokens: number
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

// TODO(uiux/polish): deduplicate provider color/initial logic with LogTable.vue
// by reconciling useProviderMeta for case-insensitive / Chinese-name handling.
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
          <div class="card-title" style="margin: 0;">30 日 Token 用量</div>
          <div class="text-muted" style="font-size: 12px; margin-top: 4px;">按日聚合 · 单位 K</div>
        </div>
        <div class="chart-legend">
          <span class="chart-legend-item"><span class="chart-legend-swatch" style="background: #0071e3;"></span>输入 Token</span>
          <span class="chart-legend-item"><span class="chart-legend-swatch" style="background: rgba(0,113,227,0.32);"></span>输出 Token</span>
        </div>
      </div>

      <div class="chart-wrap">
        <svg class="chart-svg" viewBox="0 0 1100 320" preserveAspectRatio="none" role="img" aria-label="30 日 Token 用量面积图">
          <defs>
            <linearGradient id="usAreaInput" x1="0" x2="0" y1="0" y2="1">
              <stop offset="0%" stop-color="#0071e3" stop-opacity="0.28"></stop>
              <stop offset="100%" stop-color="#0071e3" stop-opacity="0"></stop>
            </linearGradient>
            <linearGradient id="usAreaOutput" x1="0" x2="0" y1="0" y2="1">
              <stop offset="0%" stop-color="#0071e3" stop-opacity="0.10"></stop>
              <stop offset="100%" stop-color="#0071e3" stop-opacity="0"></stop>
            </linearGradient>
          </defs>

          <g stroke="rgba(0,0,0,0.05)" stroke-width="1">
            <line x1="56" y1="40" x2="1080" y2="40"></line>
            <line x1="56" y1="100" x2="1080" y2="100"></line>
            <line x1="56" y1="160" x2="1080" y2="160"></line>
            <line x1="56" y1="220" x2="1080" y2="220"></line>
            <line x1="56" y1="280" x2="1080" y2="280"></line>
          </g>
          <g font-family="SF Mono, monospace" font-size="11" fill="#6e6e73" text-anchor="end">
            <text x="50" y="44">600K</text>
            <text x="50" y="104">450K</text>
            <text x="50" y="164">300K</text>
            <text x="50" y="224">150K</text>
            <text x="50" y="284">0</text>
          </g>

          <path d="M 80,200 C 130,180 160,150 200,140 C 240,135 270,150 320,90 C 360,60 400,40 440,75 C 480,110 520,150 560,120 C 600,90 640,60 680,80 C 720,100 760,160 800,170 C 840,180 900,180 960,200 L 1080,250 L 80,250 Z" fill="url(#usAreaInput)"></path>
          <path d="M 80,200 C 130,180 160,150 200,140 C 240,135 270,150 320,90 C 360,60 400,40 440,75 C 480,110 520,150 560,120 C 600,90 640,60 680,80 C 720,100 760,160 800,170 C 840,180 900,180 960,200 L 1080,250" fill="none" stroke="#0071e3" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"></path>

          <path d="M 80,250 C 130,235 160,225 200,220 C 240,218 270,225 320,205 C 360,195 400,180 440,200 C 480,210 520,225 560,215 C 600,200 640,180 680,195 C 720,210 760,235 800,240 C 840,245 900,245 960,250 L 1080,275 L 80,275 Z" fill="url(#usAreaOutput)"></path>
          <path d="M 80,250 C 130,235 160,225 200,220 C 240,218 270,225 320,205 C 360,195 400,180 440,200 C 480,210 520,225 560,215 C 600,200 640,180 680,195 C 720,210 760,235 800,240 C 840,245 900,245 960,250 L 1080,275" fill="none" stroke="rgba(0,113,227,0.5)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" stroke-dasharray="3 2"></path>

          <g>
            <circle cx="80" cy="200" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"></circle>
            <circle cx="200" cy="140" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"></circle>
            <circle cx="320" cy="90" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"></circle>
            <circle cx="440" cy="75" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"></circle>
            <circle cx="560" cy="120" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"></circle>
            <circle cx="680" cy="80" r="4" fill="#0071e3" stroke="#fff" stroke-width="2"></circle>
            <circle cx="800" cy="170" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"></circle>
            <circle cx="920" cy="190" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"></circle>
            <circle cx="1040" cy="215" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"></circle>
          </g>

          <g>
            <line x1="680" y1="80" x2="680" y2="0" stroke="rgba(0,113,227,0.4)" stroke-width="1" stroke-dasharray="2 2"></line>
            <rect x="632" y="-2" width="96" height="22" rx="11" fill="#0071e3"></rect>
            <text x="680" y="13" text-anchor="middle" font-family="SF Mono, monospace" font-size="11" font-weight="500" fill="#fff">532K · 5/24</text>
          </g>

          <g font-family="SF Pro Text, sans-serif" font-size="11" fill="#6e6e73" text-anchor="middle">
            <text x="80" y="300">5/1</text>
            <text x="200" y="300">5/5</text>
            <text x="320" y="300">5/9</text>
            <text x="440" y="300">5/13</text>
            <text x="560" y="300">5/17</text>
            <text x="680" y="300">5/21</text>
            <text x="800" y="300">5/25</text>
            <text x="1040" y="300">今天</text>
          </g>
        </svg>
      </div>
    </section>

    <section class="col-2">
      <div class="card">
        <div class="card-title"><span>Provider 占比</span><span class="card-title-link" style="text-transform:none;">本月</span></div>
        <div class="row" style="gap: 24px; align-items: center; padding: 8px 0 0;">
          <svg viewBox="0 0 140 140" style="width: 140px; height: 140px; flex-shrink: 0;" role="img" aria-label="Provider 占比饼图">
            <circle cx="70" cy="70" r="50" fill="none" stroke="#10a37f" stroke-width="20" stroke-dasharray="125.66 314.16" transform="rotate(-90 70 70)"></circle>
            <circle cx="70" cy="70" r="50" fill="none" stroke="#d97757" stroke-width="20" stroke-dasharray="81.68 314.16" stroke-dashoffset="-125.66" transform="rotate(-90 70 70)"></circle>
            <circle cx="70" cy="70" r="50" fill="none" stroke="#272729" stroke-width="20" stroke-dasharray="40.84 314.16" stroke-dashoffset="-207.34" transform="rotate(-90 70 70)"></circle>
            <circle cx="70" cy="70" r="50" fill="none" stroke="#0071e3" stroke-width="20" stroke-dasharray="37.70 314.16" stroke-dashoffset="-248.18" transform="rotate(-90 70 70)"></circle>
            <circle cx="70" cy="70" r="50" fill="none" stroke="#6e6e73" stroke-width="20" stroke-dasharray="28.27 314.16" stroke-dashoffset="-285.88" transform="rotate(-90 70 70)"></circle>
            <text x="70" y="68" text-anchor="middle" font-family="SF Pro Display" font-size="11" font-weight="500" fill="#6e6e73">TOTAL</text>
            <text x="70" y="84" text-anchor="middle" font-family="SF Pro Display" font-size="18" font-weight="600" fill="#1d1d1f" letter-spacing="-0.02em">{{ formatNumber(totalTokens) }}</text>
          </svg>
          <div class="stack-tight" style="flex: 1;">
            <div v-for="p in providerShares" :key="p.provider_id" class="row-between">
              <div class="row" style="gap: 6px;">
                <span class="chart-legend-swatch" :style="{ background: providerColor(p.provider_name) }"></span>
                <span style="font-size: 13px;">{{ p.provider_name }}</span>
              </div>
              <div class="text-mono" style="font-size: 13px; font-weight: 500; text-align: right;">
                <div>{{ p.percent }}%</div>
                <div class="text-muted" style="font-size: 11px; font-weight: 400;">约 ${{ p.cost.toFixed(2) }}</div>
              </div>
            </div>
          </div>
        </div>
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