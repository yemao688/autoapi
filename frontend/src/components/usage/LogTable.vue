<script setup lang="ts">
import type { model } from '../../../wailsjs/go/models'

interface Props {
  logs: model.RequestLog[]
}
defineProps<Props>()

const emit = defineEmits<{
  (e: 'clearFilters'): void
}>()

function formatTime(ts: number): string {
  const d = new Date(ts)
  const mm = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  const hh = String(d.getHours()).padStart(2, '0')
  const mi = String(d.getMinutes()).padStart(2, '0')
  return `${mm}/${dd} ${hh}:${mi}`
}

const providerColors: Record<string, string> = {
  openai: '#10a37f',
  anthropic: '#d97757',
  deepseek: '#272729',
  moonshot: '#0071e3',
  '智谱 glm': '#2563eb',
  glm: '#2563eb',
}

// TODO(uiux/polish): deduplicate provider color/initial logic with TokensPane.vue
// by reconciling useProviderMeta for case-insensitive / Chinese-name handling.
function providerColor(name: string): string {
  return providerColors[name.toLowerCase()] || '#6e6e73'
}

function providerInitial(name: string): string {
  const code = name.match(/[\u4e00-\u9fa5]/)
    ? name[name.length - 1]
    : name.trim().charAt(0).toUpperCase()
  return code || name.charAt(0).toUpperCase()
}

function statusBadgeClass(statusCode: number): string {
  if (statusCode >= 200 && statusCode < 300) return 'success'
  if (statusCode === 429) return 'warn'
  if (statusCode >= 400 || statusCode === 0) return 'error'
  return 'info'
}

function statusDotClass(statusCode: number): string {
  const cls = statusBadgeClass(statusCode)
  if (cls === 'success') return 'green'
  if (cls === 'warn') return 'amber'
  return 'red'
}

function statusText(statusCode: number): string {
  return String(statusCode)
}
</script>

<template>
  <table class="tbl">
    <thead>
      <tr>
        <th>时间</th>
        <th>状态</th>
        <th>Provider</th>
        <th>Model</th>
        <th class="right">输入</th>
        <th class="right">输出</th>
        <th class="right">成本</th>
        <th class="right">延迟/首字</th>
        <th>路由</th>
      </tr>
    </thead>
    <tbody>
      <tr v-for="log in logs" :key="log.id">
        <td><span class="text-mono" style="font-size: 12.5px;">{{ formatTime(log.timestamp) }}</span></td>
        <td>
          <span class="badge" :class="statusBadgeClass(log.status_code)">
            <span :class="'dot ' + statusDotClass(log.status_code)"></span>{{ statusText(log.status_code) }}
          </span>
        </td>
        <td>
          <div class="row" style="gap: 6px;">
            <div
              class="list-icon"
              :style="{
                background: providerColor(log.provider_name),
                color: providerColor(log.provider_name) === '#272729' ? 'rgba(255,255,255,0.86)' : '#fff',
                width: '22px',
                height: '22px',
                fontSize: '10px',
                borderRadius: '5px',
              }"
            >{{ providerInitial(log.provider_name) }}</div>
            <span style="font-size: 12.5px;">{{ log.provider_name }}</span>
          </div>
        </td>
        <td>
          <span class="text-mono" style="font-size: 12.5px;">
            {{ log.model }}
            <span v-if="log.is_stream" class="text-muted" style="font-size: 10px;" title="流式请求">⇄</span>
          </span>
        </td>
        <td class="num">{{ log.input_tokens > 0 ? log.input_tokens : '—' }}</td>
        <td class="num">{{ log.output_tokens > 0 ? log.output_tokens : '—' }}</td>
        <td class="num">{{ log.cost > 0 ? '$' + log.cost.toFixed(3) : '—' }}</td>
        <td class="num">
          {{ (log.latency_ms / 1000).toFixed(2) }}s
          <span v-if="log.is_stream && log.first_token_ms > 0" class="text-muted" style="font-size: 11px;">
            /{{ (log.first_token_ms / 1000).toFixed(2) }}s
          </span>
        </td>
        <td><span class="badge info" style="font-size: 10px;">{{ log.route_label || '默认' }}</span></td>
      </tr>
      <tr v-if="logs.length === 0" class="logs-empty-row">
        <td colspan="9" style="padding: 56px 20px;">
          <div style="display: flex; flex-direction: column; align-items: center; gap: 10px; text-align: center;">
            <div style="width: 40px; height: 40px; border-radius: 10px; background: var(--bg); display: flex; align-items: center; justify-content: center; color: var(--muted);">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:20px;height:20px;" aria-hidden="true"><circle cx="11" cy="11" r="7"></circle><path d="m21 21-4.3-4.3"></path></svg>
            </div>
            <div style="font-size: 14px; font-weight: 500; color: var(--fg);">暂无匹配日志</div>
            <div style="font-size: 12.5px; color: var(--muted);">尝试调整时间范围或清除筛选条件</div>
            <button class="btn btn-secondary" style="font-size: 12.5px; padding: 5px 12px; margin-top: 4px;" @click="emit('clearFilters')">清除筛选</button>
          </div>
        </td>
      </tr>
    </tbody>
  </table>
</template>
