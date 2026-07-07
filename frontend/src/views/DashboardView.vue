<script setup lang="ts">
import { useRelativeTime } from '@/composables/useRelativeTime'

useRelativeTime()

// Tab groups keyboard navigation
function handleTabKeydown(e: KeyboardEvent, container: HTMLElement) {
  const tabs = container.querySelectorAll<HTMLButtonElement>('.tab')
  if (!tabs.length) return
  const currentIdx = Array.from(tabs).findIndex((t) => t === document.activeElement)
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

  tabs[nextIdx]?.focus()
  tabs[nextIdx]?.click()
}
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">总览</h1>
      <span class="main-subtitle">关键指标与最近活动</span>
    </div>
    <div class="main-actions">
      <span class="badge success"><span class="dot green"></span>实时同步</span>
      <button class="btn btn-secondary">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v14M5 12h14"/></svg>
        导出报告
      </button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner stack-loose">
      <!-- Stat row -->
      <section class="stat-grid">
        <div class="stat-card">
          <div class="stat-label">今日 Token</div>
          <div class="stat-value">245,832</div>
          <div class="stat-meta">
            <span class="delta positive">+12.4%</span>
            <span>vs 昨日</span>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-label">本周 Token</div>
          <div class="stat-value">1.84M</div>
          <div class="stat-meta">
            <span class="delta positive">+8.1%</span>
            <span>vs 上周</span>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-label">本月成本</div>
          <div class="stat-value">¥458.76</div>
          <div class="stat-meta">
            <span class="delta negative">-3.2%</span>
            <span>智能路由节省</span>
          </div>
        </div>
        <div class="stat-card dark">
          <div class="stat-label">服务状态</div>
          <div class="stat-value">正常</div>
          <div class="stat-meta">
            <span class="dot green"></span>
            <span>Uptime 12d 4h · 0 错误</span>
          </div>
        </div>
      </section>

      <!-- Main chart -->
      <section class="card">
        <div class="row-between" style="margin-bottom: 16px;">
          <div>
            <div class="card-title" style="margin: 0;">Token 用量趋势 · 近 7 日</div>
            <div class="text-muted" style="font-size: 12px; margin-top: 4px;">按输入 / 输出 Token 拆分</div>
          </div>
          <div class="chart-legend">
            <span class="chart-legend-item"><span class="chart-legend-swatch" style="background: #0071e3;"></span>输入</span>
            <span class="chart-legend-item"><span class="chart-legend-swatch" style="background: rgba(0, 113, 227, 0.32);"></span>输出</span>
          </div>
        </div>

        <div class="chart-wrap">
          <svg class="chart-svg" viewBox="0 0 800 220" preserveAspectRatio="none" role="img" aria-label="7 日 Token 用量趋势">
            <defs>
              <linearGradient id="areaInput" x1="0" x2="0" y1="0" y2="1">
                <stop offset="0%" stop-color="#0071e3" stop-opacity="0.22"/>
                <stop offset="100%" stop-color="#0071e3" stop-opacity="0"/>
              </linearGradient>
              <linearGradient id="areaOutput" x1="0" x2="0" y1="0" y2="1">
                <stop offset="0%" stop-color="#0071e3" stop-opacity="0.08"/>
                <stop offset="100%" stop-color="#0071e3" stop-opacity="0"/>
              </linearGradient>
            </defs>

            <g stroke="rgba(0,0,0,0.06)" stroke-width="1">
              <line x1="40" y1="40" x2="780" y2="40"/>
              <line x1="40" y1="80" x2="780" y2="80"/>
              <line x1="40" y1="120" x2="780" y2="120"/>
              <line x1="40" y1="160" x2="780" y2="160"/>
              <line x1="40" y1="200" x2="780" y2="200"/>
            </g>
            <g font-family="SF Mono, monospace" font-size="10" fill="#6e6e73" text-anchor="end">
              <text x="34" y="44">600K</text>
              <text x="34" y="84">450K</text>
              <text x="34" y="124">300K</text>
              <text x="34" y="164">150K</text>
              <text x="34" y="204">0</text>
            </g>

            <path d="M 80,148 L 188,128 L 296,138 L 404,82 L 512,46 L 620,98 L 728,146 L 728,200 L 80,200 Z" fill="url(#areaInput)"/>
            <path d="M 80,148 L 188,128 L 296,138 L 404,82 L 512,46 L 620,98 L 728,146" fill="none" stroke="#0071e3" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>

            <path d="M 80,178 L 188,168 L 296,170 L 404,148 L 512,128 L 620,156 L 728,176 L 728,200 L 80,200 Z" fill="url(#areaOutput)"/>
            <path d="M 80,178 L 188,168 L 296,170 L 404,148 L 512,128 L 620,156 L 728,176" fill="none" stroke="rgba(0,113,227,0.5)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" stroke-dasharray="3 2"/>

            <g>
              <circle cx="80" cy="148" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"/>
              <circle cx="188" cy="128" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"/>
              <circle cx="296" cy="138" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"/>
              <circle cx="404" cy="82" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"/>
              <circle cx="512" cy="46" r="4" fill="#0071e3" stroke="#fff" stroke-width="2"/>
              <circle cx="620" cy="98" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"/>
              <circle cx="728" cy="146" r="3.5" fill="#fff" stroke="#0071e3" stroke-width="2"/>
            </g>

            <g>
              <line x1="512" y1="46" x2="512" y2="14" stroke="rgba(0,113,227,0.4)" stroke-width="1" stroke-dasharray="2 2"/>
              <rect x="470" y="0" width="84" height="22" rx="11" fill="#0071e3"/>
              <text x="512" y="15" text-anchor="middle" font-family="SF Mono, monospace" font-size="11" font-weight="500" fill="#fff">532K · 周五</text>
            </g>

            <g font-family="SF Pro Text, sans-serif" font-size="11" fill="#6e6e73" text-anchor="middle">
              <text x="80" y="218">周一</text>
              <text x="188" y="218">周二</text>
              <text x="296" y="218">周三</text>
              <text x="404" y="218">周四</text>
              <text x="512" y="218">周五</text>
              <text x="620" y="218">周六</text>
              <text x="728" y="218">今天</text>
            </g>
          </svg>
        </div>
      </section>

      <!-- Two columns: providers + activity -->
      <section class="col-2-3-7">
        <div class="card">
          <div class="card-title">
            <span>Provider 状态</span>
            <RouterLink class="card-title-link" to="/providers">管理</RouterLink>
          </div>
          <div class="stack-tight">
            <div class="list-row" style="padding: 10px 0;">
              <div class="list-icon" style="background: #10a37f;">O</div>
              <div class="list-main">
                <div class="list-title">OpenAI</div>
                <div class="list-sub">gpt-4o · 4 模型</div>
              </div>
              <div class="list-meta">118K</div>
            </div>
            <div class="list-row" style="padding: 10px 0;">
              <div class="list-icon" style="background: #d97757;">A</div>
              <div class="list-main">
                <div class="list-title">Anthropic</div>
                <div class="list-sub">claude-sonnet-4-5 · 3 模型</div>
              </div>
              <div class="list-meta">76K</div>
            </div>
            <div class="list-row" style="padding: 10px 0;">
              <div class="list-icon dark">D</div>
              <div class="list-main">
                <div class="list-title">DeepSeek</div>
                <div class="list-sub">deepseek-chat · 2 模型</div>
              </div>
              <div class="list-meta">32K</div>
            </div>
            <div class="list-row" style="padding: 10px 0;">
              <div class="list-icon blue">M</div>
              <div class="list-main">
                <div class="list-title">Moonshot</div>
                <div class="list-sub">moonshot-v1-128k · 3 模型</div>
              </div>
              <div class="list-meta">14K</div>
            </div>
            <div class="list-row" style="padding: 10px 0;">
              <div class="list-icon" style="background: #2563eb;">G</div>
              <div class="list-main">
                <div class="list-title">智谱 GLM</div>
                <div class="list-sub"><span class="dot red" style="margin-right: 4px;"></span>未连接</div>
              </div>
              <div class="list-meta text-muted">—</div>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card-title">
            <span>最近活动</span>
            <RouterLink class="card-title-link" to="/usage-stats">查看全部</RouterLink>
          </div>
          <div class="stack-tight">
            <div class="list-row" style="padding: 8px 0;">
              <span class="text-mono text-muted" style="width: 60px;">10:42</span>
              <span class="badge mono">gpt-4o</span>
              <div class="list-main">
                <div class="list-title">OpenAI · 输入 245 / 输出 47</div>
              </div>
              <span class="text-mono text-muted">1.2s</span>
            </div>
            <div class="list-row" style="padding: 8px 0;">
              <span class="text-mono text-muted" style="width: 60px;">10:38</span>
              <span class="badge mono">sonnet-4-5</span>
              <div class="list-main">
                <div class="list-title">Anthropic · 输入 128 / 输出 24</div>
              </div>
              <span class="text-mono text-muted">0.8s</span>
            </div>
            <div class="list-row" style="padding: 8px 0;">
              <span class="text-mono text-muted" style="width: 60px;">10:35</span>
              <span class="badge mono">deepseek-chat</span>
              <div class="list-main">
                <div class="list-title">DeepSeek · 输入 312 / 输出 58</div>
              </div>
              <span class="text-mono text-muted">2.1s</span>
            </div>
            <div class="list-row" style="padding: 8px 0;">
              <span class="text-mono text-muted" style="width: 60px;">10:30</span>
              <span class="badge mono">gpt-4o-mini</span>
              <div class="list-main">
                <div class="list-title">OpenAI · 输入 89 / 输出 16</div>
              </div>
              <span class="text-mono text-muted">0.6s</span>
            </div>
            <div class="list-row" style="padding: 8px 0;">
              <span class="text-mono text-muted" style="width: 60px;">10:24</span>
              <span class="badge mono">claude-opus-4-1</span>
              <div class="list-main">
                <div class="list-title">Anthropic · 输入 1,240 / 输出 380</div>
              </div>
              <span class="text-mono text-muted">5.4s</span>
            </div>
            <div class="list-row" style="padding: 8px 0;">
              <span class="text-mono text-muted" style="width: 60px;">10:18</span>
              <span class="badge mono">kimi-latest</span>
              <div class="list-main">
                <div class="list-title">Moonshot · 输入 580 / 输出 124</div>
              </div>
              <span class="text-mono text-muted">3.2s</span>
            </div>
          </div>
        </div>
      </section>

      <!-- Service health -->
      <section class="col-3">
        <div class="card">
          <div class="card-title">CPU 占用</div>
          <div class="stat-value" style="font-size: 24px;">12.4%</div>
          <div class="stat-meta"><span class="delta positive">−1.8%</span><span>近 1 小时均值</span></div>
        </div>
        <div class="card">
          <div class="card-title">内存</div>
          <div class="stat-value" style="font-size: 24px;">182 MB</div>
          <div class="stat-meta"><span>本机 · 4.2%</span></div>
        </div>
        <div class="card">
          <div class="card-title">活动连接</div>
          <div class="stat-value" style="font-size: 24px;">42</div>
          <div class="stat-meta"><span>WebSocket 38 · HTTP 4</span></div>
        </div>
      </section>
    </div>
  </div>
</template>
