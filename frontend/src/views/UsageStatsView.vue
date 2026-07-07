<script setup lang="ts">
import { ref } from 'vue'
import { useRelativeTime } from '@/composables/useRelativeTime'

useRelativeTime()

const activePane = ref('tokens')

function switchPane(paneId: string) {
  activePane.value = paneId
}

// Pane keyboard navigation
function handlePaneKeydown(e: KeyboardEvent) {
  const tabs = document.querySelectorAll<HTMLButtonElement>('#usage-tab-strip .tab')
  if (!tabs.length) return
  const currentIdx = Array.from(tabs).findIndex((t) => t.getAttribute('data-pane-id') === activePane.value)
  let nextIdx = currentIdx
  switch (e.key) {
    case 'ArrowRight':
    case 'ArrowDown':
      e.preventDefault()
      nextIdx = (currentIdx + 1) % tabs.length
      break
    case 'ArrowLeft':
    case 'ArrowUp':
      e.preventDefault()
      nextIdx = (currentIdx - 1 + tabs.length) % tabs.length
      break
    case 'Home':
      e.preventDefault()
      nextIdx = 0
      break
    case 'End':
      e.preventDefault()
      nextIdx = tabs.length - 1
      break
    default:
      return
  }
  const targetId = tabs[nextIdx]?.getAttribute('data-pane-id')
  if (targetId) switchPane(targetId)
  tabs[nextIdx]?.focus()
}

// Live toggle
const liveSync = ref(true)
function toggleLive() {
  liveSync.value = !liveSync.value
}

// Clear filters (toggle empty state)
const showEmpty = ref(false)
function clearFilters() {
  showEmpty.value = !showEmpty.value
}
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">使用统计</h1>
      <span class="main-subtitle">Token 用量与请求日志 · 单页切换</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;" aria-hidden="true"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/></svg>
        导出
      </button>
      <button
        class="btn btn-primary"
        :class="{ active: liveSync }"
        :aria-pressed="liveSync"
        aria-label="切换实时同步"
        @click="toggleLive"
      >
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;" aria-hidden="true"><path d="M3 12a9 9 0 1 0 9-9M3 12a9 9 0 0 1 9-9M12 3v9l5 3"/></svg>
        <span class="live-label">实时同步</span>
      </button>
    </div>
  </header>

  <div class="tabs-strip" role="tablist" aria-label="使用统计视图" id="usage-tab-strip" @keydown="handlePaneKeydown">
    <button
      class="tab"
      :class="{ active: activePane === 'tokens' }"
      role="tab"
      id="usage-tab-tokens"
      :aria-selected="activePane === 'tokens'"
      aria-controls="usage-pane-tokens"
      data-pane-id="tokens"
      @click="switchPane('tokens')"
    >
      Token 用量<span class="tab-meta" aria-hidden="true">8.45M</span>
    </button>
    <button
      class="tab"
      :class="{ active: activePane === 'logs' }"
      role="tab"
      id="usage-tab-logs"
      :aria-selected="activePane === 'logs'"
      aria-controls="usage-pane-logs"
      data-pane-id="logs"
      @click="switchPane('logs')"
    >
      请求日志<span class="tab-meta" aria-hidden="true">12,485</span>
    </button>
  </div>

  <div class="filter-bar">
    <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px;" aria-label="选择日期范围">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;" aria-hidden="true"><rect x="3" y="4" width="18" height="18" rx="2"/><path d="M16 2v4M8 2v4M3 10h18"/></svg>
      本月 · 2026-05
    </button>
    <select class="select" style="width: auto; padding: 5px 10px; font-size: 12.5px;" aria-label="按 Provider 筛选">
      <option>Provider · 全部</option>
      <option>OpenAI</option>
      <option>Anthropic</option>
      <option>DeepSeek</option>
      <option>Moonshot</option>
      <option>智谱 GLM</option>
    </select>
    <select v-if="activePane === 'logs'" class="select" style="width: auto; padding: 5px 10px; font-size: 12.5px;" aria-label="按状态筛选">
      <option>状态 · 全部</option>
      <option>成功</option>
      <option>失败</option>
      <option>限流</option>
    </select>
    <div class="filter-spacer"></div>
    <button class="btn btn-ghost" style="font-size: 12.5px; padding: 5px 10px;" @click="clearFilters">清除筛选</button>
  </div>

  <div class="main-content">
    <div class="main-content-inner stack-loose">

      <!-- ================== TOKENS VIEW ================== -->
      <div v-show="activePane === 'tokens'" class="view-pane" role="tabpanel" id="usage-pane-tokens" aria-labelledby="usage-tab-tokens" data-pane-group="usage-view" data-pane-id="tokens" tabindex="0">

        <section class="stat-grid-4" style="gap: 16px; margin-bottom: 24px;">
          <div class="metric-card">
            <div class="metric-label">今日 Token<span class="text-mono" style="color: var(--muted);">tokens</span></div>
            <div class="metric-value">245,832</div>
            <div class="metric-meta">
              <span class="metric-trend up">↑ 12.4%</span>
              <span>vs 昨日</span>
            </div>
          </div>
          <div class="metric-card">
            <div class="metric-label">本周 Token<span class="text-mono" style="color: var(--muted);">7d</span></div>
            <div class="metric-value">1.84M</div>
            <div class="metric-meta">
              <span class="metric-trend up">↑ 8.1%</span>
              <span>vs 上周</span>
            </div>
          </div>
          <div class="metric-card">
            <div class="metric-label">本月 Token<span class="text-mono" style="color: var(--muted);">5月</span></div>
            <div class="metric-value">8.45M</div>
            <div class="metric-meta">
              <span class="metric-trend up">↑ 12.4%</span>
              <span>vs 上月</span>
            </div>
          </div>
          <div class="metric-card">
            <div class="metric-label">本月成本<span class="text-mono" style="color: var(--muted);">CNY</span></div>
            <div class="metric-value">¥458.76</div>
            <div class="metric-meta">
              <span class="metric-trend down">↓ 3.2%</span>
              <span>智能路由节省 ¥86.42</span>
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
                <text x="70" y="84" text-anchor="middle" font-family="SF Pro Display" font-size="18" font-weight="600" fill="#1d1d1f" letter-spacing="-0.02em">8.45M</text>
              </svg>
              <div class="stack-tight" style="flex: 1;">
                <div class="row-between"><div class="row" style="gap: 6px;"><span class="chart-legend-swatch" style="background: #10a37f;"></span><span style="font-size: 13px;">OpenAI</span></div><span class="text-mono" style="font-size: 13px; font-weight: 500;">40%</span></div>
                <div class="row-between"><div class="row" style="gap: 6px;"><span class="chart-legend-swatch" style="background: #d97757;"></span><span style="font-size: 13px;">Anthropic</span></div><span class="text-mono" style="font-size: 13px; font-weight: 500;">26%</span></div>
                <div class="row-between"><div class="row" style="gap: 6px;"><span class="chart-legend-swatch" style="background: #272729;"></span><span style="font-size: 13px;">DeepSeek</span></div><span class="text-mono" style="font-size: 13px; font-weight: 500;">13%</span></div>
                <div class="row-between"><div class="row" style="gap: 6px;"><span class="chart-legend-swatch" style="background: #0071e3;"></span><span style="font-size: 13px;">Moonshot</span></div><span class="text-mono" style="font-size: 13px; font-weight: 500;">12%</span></div>
                <div class="row-between"><div class="row" style="gap: 6px;"><span class="chart-legend-swatch" style="background: #6e6e73;"></span><span style="font-size: 13px;">其他</span></div><span class="text-mono" style="font-size: 13px; font-weight: 500;">9%</span></div>
              </div>
            </div>
          </div>

          <div class="card">
            <div class="card-title"><span>模型用量排行</span><RouterLink class="card-title-link" to="/usage-stats" style="text-transform:none;">详情</RouterLink></div>
            <div class="stack-tight" style="padding-top: 4px;">
              <div class="list-row" style="padding: 8px 0;">
                <div class="text-mono text-muted" style="width: 18px; font-size: 12px;">01</div>
                <div class="list-main">
                  <div class="text-mono" style="font-size: 13px;">gpt-4o</div>
                  <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">OpenAI · 4,128 请求</div>
                </div>
                <div class="list-meta" style="min-width: 80px; text-align: right;">
                  <div>2.42M</div>
                  <div class="text-mono list-meta-sub">¥218.20</div>
                </div>
              </div>
              <div class="list-row" style="padding: 8px 0;">
                <div class="text-mono text-muted" style="width: 18px; font-size: 12px;">02</div>
                <div class="list-main">
                  <div class="text-mono" style="font-size: 13px;">claude-sonnet-4-5</div>
                  <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">Anthropic · 1,842 请求</div>
                </div>
                <div class="list-meta" style="min-width: 80px; text-align: right;">
                  <div>1.86M</div>
                  <div class="text-mono list-meta-sub">¥148.80</div>
                </div>
              </div>
              <div class="list-row" style="padding: 8px 0;">
                <div class="text-mono text-muted" style="width: 18px; font-size: 12px;">03</div>
                <div class="list-main">
                  <div class="text-mono" style="font-size: 13px;">gpt-4o-mini</div>
                  <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">OpenAI · 6,128 请求</div>
                </div>
                <div class="list-meta" style="min-width: 80px; text-align: right;">
                  <div>998K</div>
                  <div class="text-mono list-meta-sub">¥19.96</div>
                </div>
              </div>
              <div class="list-row" style="padding: 8px 0;">
                <div class="text-mono text-muted" style="width: 18px; font-size: 12px;">04</div>
                <div class="list-main">
                  <div class="text-mono" style="font-size: 13px;">deepseek-reasoner</div>
                  <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">DeepSeek · 1,408 请求</div>
                </div>
                <div class="list-meta" style="min-width: 80px; text-align: right;">
                  <div>812K</div>
                  <div class="text-mono list-meta-sub">¥8.12</div>
                </div>
              </div>
              <div class="list-row" style="padding: 8px 0;">
                <div class="text-mono text-muted" style="width: 18px; font-size: 12px;">05</div>
                <div class="list-main">
                  <div class="text-mono" style="font-size: 13px;">moonshot-v1-128k</div>
                  <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">Moonshot · 214 请求</div>
                </div>
                <div class="list-meta" style="min-width: 80px; text-align: right;">
                  <div>428K</div>
                  <div class="text-mono list-meta-sub">¥4.28</div>
                </div>
              </div>
            </div>
          </div>
        </section>
      </div>

      <!-- ================== LOGS VIEW ================== -->
      <div v-show="activePane === 'logs'" class="view-pane" role="tabpanel" id="usage-pane-logs" aria-labelledby="usage-tab-logs" data-pane-group="usage-view" data-pane-id="logs" tabindex="0">

        <section class="stat-grid-4" style="gap: 16px; margin-bottom: 24px;">
          <div class="metric-card">
            <div class="metric-label">总请求<span class="text-mono" style="color: var(--muted);">本月</span></div>
            <div class="metric-value">12,485</div>
            <div class="metric-meta">
              <span class="metric-trend up">↑ 8.4%</span>
              <span>vs 上月</span>
            </div>
          </div>
          <div class="metric-card">
            <div class="metric-label">成功率<span class="text-mono" style="color: var(--muted);">%</span></div>
            <div class="metric-value">99.6<span class="unit">%</span></div>
            <div class="metric-meta">
              <span class="metric-trend up">↑ 0.2%</span>
              <span>vs 上月</span>
            </div>
          </div>
          <div class="metric-card">
            <div class="metric-label">P95 延迟<span class="text-mono" style="color: var(--muted);">秒</span></div>
            <div class="metric-value">2.4<span class="unit">s</span></div>
            <div class="metric-meta">
              <span class="metric-trend down">↑ 0.3s</span>
              <span>略升高</span>
            </div>
          </div>
          <div class="metric-card">
            <div class="metric-label">错误数<span class="text-mono" style="color: var(--muted);">5xx+429</span></div>
            <div class="metric-value">52</div>
            <div class="metric-meta">
              <span class="metric-trend neutral">主要 401</span>
              <span>2 小时前</span>
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
          <table class="tbl">
            <thead>
              <tr>
                <th>时间</th>
                <th>状态</th>
                <th>Provider</th>
                <th>Model</th>
                <th class="right">输入</th>
                <th class="right">输出</th>
                <th class="right">延迟</th>
                <th>路由</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td><span class="text-mono" style="font-size: 12.5px;">10:42:15</span></td>
                <td><span class="badge success"><span class="dot green"></span>200</span></td>
                <td><div class="row" style="gap: 6px;"><div class="list-icon" style="background: #d97757; color: white; width: 22px; height: 22px; font-size: 10px; border-radius: 5px;">A</div><span style="font-size: 12.5px;">Anthropic</span></div></td>
                <td><span class="text-mono" style="font-size: 12.5px;">claude-sonnet-4-5</span></td>
                <td class="num">245</td>
                <td class="num">47</td>
                <td class="num">1.24s</td>
                <td><span class="badge info" style="font-size: 10px;">R1</span></td>
              </tr>
              <tr>
                <td><span class="text-mono" style="font-size: 12.5px;">10:42:08</span></td>
                <td><span class="badge success"><span class="dot green"></span>200</span></td>
                <td><div class="row" style="gap: 6px;"><div class="list-icon" style="background: #10a37f; color: white; width: 22px; height: 22px; font-size: 10px; border-radius: 5px;">O</div><span style="font-size: 12.5px;">OpenAI</span></div></td>
                <td><span class="text-mono" style="font-size: 12.5px;">gpt-4o</span></td>
                <td class="num">89</td>
                <td class="num">156</td>
                <td class="num">0.82s</td>
                <td><span class="badge info" style="font-size: 10px;">R2</span></td>
              </tr>
              <tr>
                <td><span class="text-mono" style="font-size: 12.5px;">10:41:42</span></td>
                <td><span class="badge error"><span class="dot red"></span>401</span></td>
                <td><div class="row" style="gap: 6px;"><div class="list-icon" style="background: #2563eb; color: white; width: 22px; height: 22px; font-size: 10px; border-radius: 5px;">G</div><span style="font-size: 12.5px;">智谱 GLM</span></div></td>
                <td><span class="text-mono" style="font-size: 12.5px;">glm-4-plus</span></td>
                <td class="num">412</td>
                <td class="num text-muted">—</td>
                <td class="num">0.34s</td>
                <td><span class="badge" style="font-size: 10px;">默认</span></td>
              </tr>
              <tr>
                <td><span class="text-mono" style="font-size: 12.5px;">10:41:18</span></td>
                <td><span class="badge success"><span class="dot green"></span>200</span></td>
                <td><div class="row" style="gap: 6px;"><div class="list-icon" style="background: #10a37f; color: white; width: 22px; height: 22px; font-size: 10px; border-radius: 5px;">O</div><span style="font-size: 12.5px;">OpenAI</span></div></td>
                <td><span class="text-mono" style="font-size: 12.5px;">gpt-4o-mini</span></td>
                <td class="num">128</td>
                <td class="num">24</td>
                <td class="num">0.61s</td>
                <td><span class="badge info" style="font-size: 10px;">R2</span></td>
              </tr>
              <tr>
                <td><span class="text-mono" style="font-size: 12.5px;">10:40:55</span></td>
                <td><span class="badge success"><span class="dot green"></span>200</span></td>
                <td><div class="row" style="gap: 6px;"><div class="list-icon dark" style="width: 22px; height: 22px; font-size: 10px; border-radius: 5px;">D</div><span style="font-size: 12.5px;">DeepSeek</span></div></td>
                <td><span class="text-mono" style="font-size: 12.5px;">deepseek-reasoner</span></td>
                <td class="num">312</td>
                <td class="num">580</td>
                <td class="num">4.18s</td>
                <td><span class="badge info" style="font-size: 10px;">R4</span></td>
              </tr>
              <tr>
                <td><span class="text-mono" style="font-size: 12.5px;">10:40:32</span></td>
                <td><span class="badge success"><span class="dot green"></span>200</span></td>
                <td><div class="row" style="gap: 6px;"><div class="list-icon" style="background: #d97757; color: white; width: 22px; height: 22px; font-size: 10px; border-radius: 5px;">A</div><span style="font-size: 12.5px;">Anthropic</span></div></td>
                <td><span class="text-mono" style="font-size: 12.5px;">claude-opus-4-1</span></td>
                <td class="num">1,240</td>
                <td class="num">380</td>
                <td class="num">5.42s</td>
                <td><span class="badge" style="font-size: 10px;">默认</span></td>
              </tr>
              <tr>
                <td><span class="text-mono" style="font-size: 12.5px;">10:40:11</span></td>
                <td><span class="badge warn"><span class="dot amber"></span>429</span></td>
                <td><div class="row" style="gap: 6px;"><div class="list-icon" style="background: #10a37f; color: white; width: 22px; height: 22px; font-size: 10px; border-radius: 5px;">O</div><span style="font-size: 12.5px;">OpenAI</span></div></td>
                <td><span class="text-mono" style="font-size: 12.5px;">gpt-4o</span></td>
                <td class="num text-muted">—</td>
                <td class="num text-muted">—</td>
                <td class="num">0.12s</td>
                <td><span class="badge info" style="font-size: 10px;">R1</span></td>
              </tr>
              <tr>
                <td><span class="text-mono" style="font-size: 12.5px;">10:39:48</span></td>
                <td><span class="badge success"><span class="dot green"></span>200</span></td>
                <td><div class="row" style="gap: 6px;"><div class="list-icon blue" style="width: 22px; height: 22px; font-size: 10px; border-radius: 5px;">M</div><span style="font-size: 12.5px;">Moonshot</span></div></td>
                <td><span class="text-mono" style="font-size: 12.5px;">kimi-latest</span></td>
                <td class="num">58,420</td>
                <td class="num">2,140</td>
                <td class="num">12.3s</td>
                <td><span class="badge info" style="font-size: 10px;">R3</span></td>
              </tr>
              <tr>
                <td><span class="text-mono" style="font-size: 12.5px;">10:39:20</span></td>
                <td><span class="badge success"><span class="dot green"></span>200</span></td>
                <td><div class="row" style="gap: 6px;"><div class="list-icon" style="background: #d97757; color: white; width: 22px; height: 22px; font-size: 10px; border-radius: 5px;">A</div><span style="font-size: 12.5px;">Anthropic</span></div></td>
                <td><span class="text-mono" style="font-size: 12.5px;">claude-sonnet-4-5</span></td>
                <td class="num">680</td>
                <td class="num">142</td>
                <td class="num">1.78s</td>
                <td><span class="badge" style="font-size: 10px;">默认</span></td>
              </tr>
              <tr>
                <td><span class="text-mono" style="font-size: 12.5px;">10:38:55</span></td>
                <td><span class="badge success"><span class="dot green"></span>200</span></td>
                <td><div class="row" style="gap: 6px;"><div class="list-icon dark" style="width: 22px; height: 22px; font-size: 10px; border-radius: 5px;">D</div><span style="font-size: 12.5px;">DeepSeek</span></div></td>
                <td><span class="text-mono" style="font-size: 12.5px;">deepseek-chat</span></td>
                <td class="num">94</td>
                <td class="num">28</td>
                <td class="num">1.42s</td>
                <td><span class="badge info" style="font-size: 10px;">R4</span></td>
              </tr>
              <tr>
                <td><span class="text-mono" style="font-size: 12.5px;">10:38:30</span></td>
                <td><span class="badge success"><span class="dot green"></span>200</span></td>
                <td><div class="row" style="gap: 6px;"><div class="list-icon" style="background: #10a37f; color: white; width: 22px; height: 22px; font-size: 10px; border-radius: 5px;">O</div><span style="font-size: 12.5px;">OpenAI</span></div></td>
                <td><span class="text-mono" style="font-size: 12.5px;">gpt-4o</span></td>
                <td class="num">2,840</td>
                <td class="num">640</td>
                <td class="num">3.18s</td>
                <td><span class="badge info" style="font-size: 10px;">R1</span></td>
              </tr>
              <!-- Empty state row -->
              <tr v-if="showEmpty" class="logs-empty-row">
                <td colspan="8" style="padding: 56px 20px;">
                  <div style="display: flex; flex-direction: column; align-items: center; gap: 10px; text-align: center;">
                    <div style="width: 40px; height: 40px; border-radius: 10px; background: var(--bg); display: flex; align-items: center; justify-content: center; color: var(--muted);">
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:20px;height:20px;" aria-hidden="true"><circle cx="11" cy="11" r="7"></circle><path d="m21 21-4.3-4.3"></path></svg>
                    </div>
                    <div style="font-size: 14px; font-weight: 500; color: var(--fg);">未找到匹配的请求</div>
                    <div style="font-size: 12.5px; color: var(--muted);">尝试调整时间范围或清除筛选条件</div>
                    <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px; margin-top: 4px;" @click="clearFilters">清除筛选</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>

          <div class="row-between" style="padding: 12px 16px; border-top: 1px solid rgba(0, 0, 0, 0.05);">
            <div class="text-muted" style="font-size: 12px;">显示 1–11 / 共 12,485 条</div>
            <div class="row" style="gap: 6px;" role="group" aria-label="分页">
              <button class="btn btn-secondary" style="padding: 4px 10px; font-size: 12px;" aria-label="上一页">‹ 上一页</button>
              <button class="btn btn-primary" style="padding: 4px 10px; font-size: 12px; min-width: 28px;" aria-current="page">1</button>
              <button class="btn btn-secondary" style="padding: 4px 10px; font-size: 12px; min-width: 28px;" aria-label="第 2 页">2</button>
              <button class="btn btn-secondary" style="padding: 4px 10px; font-size: 12px; min-width: 28px;" aria-label="第 3 页">3</button>
              <span class="text-muted" style="font-size: 12px; padding: 0 4px;" aria-hidden="true">…</span>
              <button class="btn btn-secondary" style="padding: 4px 10px; font-size: 12px; min-width: 28px;" aria-label="第 1135 页">1135</button>
              <button class="btn btn-secondary" style="padding: 4px 10px; font-size: 12px;" aria-label="下一页">下一页 ›</button>
            </div>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>
