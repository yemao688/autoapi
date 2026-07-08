<script setup lang="ts">
import type { model } from '../../../wailsjs/go/models'
import LogTable from './LogTable.vue'
import Pagination from './Pagination.vue'

interface Props {
  logs: model.RequestLog[]
  logStats: model.Stat[]
  logTotal: number
  logPage: number
  logPageSize: number
}
defineProps<Props>()

const emit = defineEmits<{
  (e: 'prev'): void
  (e: 'next'): void
  (e: 'clearFilters'): void
}>()
</script>

<template>
  <div class="view-pane" role="tabpanel" id="usage-pane-logs" aria-labelledby="usage-tab-logs" data-pane-group="usage-view" data-pane-id="logs" tabindex="0">

    <section class="stat-grid-4" style="gap: 16px; margin-bottom: 24px;">
      <div v-for="(stat, idx) in logStats" :key="stat.label + idx" class="metric-card">
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
          <div class="card-title" style="margin: 0;">请求量 · 近 24 小时</div>
          <div class="text-muted" style="font-size: 12px; margin-top: 4px;">按小时聚合 · 成功 / 失败 / 限流</div>
        </div>
        <div class="chart-legend">
          <span class="chart-legend-item"><span class="chart-legend-swatch" style="background: #0071e3;"></span>成功</span>
          <span class="chart-legend-item"><span class="chart-legend-swatch" style="background: #d93025;"></span>失败</span>
          <span class="chart-legend-item"><span class="chart-legend-swatch" style="background: #f5a623;"></span>限流</span>
        </div>
      </div>

      <div class="chart-wrap">
        <svg class="chart-svg" viewBox="0 0 1100 300" preserveAspectRatio="none" role="img" aria-label="24 小时请求量面积图">
          <defs>
            <linearGradient id="usLogSuccess" x1="0" x2="0" y1="0" y2="1">
              <stop offset="0%" stop-color="#0071e3" stop-opacity="0.22"></stop>
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
            <text x="50" y="44">300</text>
            <text x="50" y="104">225</text>
            <text x="50" y="164">150</text>
            <text x="50" y="224">75</text>
            <text x="50" y="284">0</text>
          </g>

          <path d="M 80,220 C 110,215 130,210 160,200 C 190,180 210,170 240,160 C 270,140 290,130 320,120 C 350,100 370,90 400,85 C 430,80 450,75 480,60 C 510,55 530,70 560,90 C 590,110 610,140 640,150 C 670,160 690,150 720,130 C 750,110 770,100 800,110 C 830,120 850,140 880,160 C 910,180 930,200 960,210 C 990,218 1030,225 1080,240 L 1080,280 L 80,280 Z" fill="url(#usLogSuccess)"></path>
          <path d="M 80,220 C 110,215 130,210 160,200 C 190,180 210,170 240,160 C 270,140 290,130 320,120 C 350,100 370,90 400,85 C 430,80 450,75 480,60 C 510,55 530,70 560,90 C 590,110 610,140 640,150 C 670,160 690,150 720,130 C 750,110 770,100 800,110 C 830,120 850,140 880,160 C 910,180 930,200 960,210 C 990,218 1030,225 1080,240" fill="none" stroke="#0071e3" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"></path>

          <g>
            <rect x="475" y="58" width="14" height="4" fill="#d93025" rx="1"></rect>
            <rect x="600" y="118" width="14" height="3" fill="#d93025" rx="1"></rect>
            <rect x="880" y="170" width="14" height="3" fill="#f5a623" rx="1"></rect>
          </g>

          <g>
            <line x1="480" y1="60" x2="480" y2="-4" stroke="rgba(217,48,37,0.4)" stroke-width="1" stroke-dasharray="2 2"></line>
            <rect x="436" y="-22" width="88" height="20" rx="10" fill="#1d1d1f"></rect>
            <text x="480" y="-9" text-anchor="middle" font-family="SF Mono, monospace" font-size="10.5" font-weight="500" fill="#fff">3 错误 · 09:30</text>
          </g>

          <g font-family="SF Pro Text, sans-serif" font-size="11" fill="#6e6e73" text-anchor="middle">
            <text x="80" y="298">00:00</text>
            <text x="240" y="298">04:00</text>
            <text x="400" y="298">08:00</text>
            <text x="560" y="298">12:00</text>
            <text x="720" y="298">16:00</text>
            <text x="880" y="298">20:00</text>
            <text x="1080" y="298">现在</text>
          </g>
        </svg>
      </div>
    </section>

    <section class="card" style="padding: 0; overflow: hidden;">
      <LogTable :logs="logs" @clearFilters="emit('clearFilters')" />
      <Pagination
        :page="logPage"
        :pageSize="logPageSize"
        :total="logTotal"
        :count="logs.length"
        @prev="emit('prev')"
        @next="emit('next')"
      />
    </section>
  </div>
</template>