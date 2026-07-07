<script setup lang="ts">
import { useRelativeTime } from '@/composables/useRelativeTime'

useRelativeTime()
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">API 密钥</h1>
      <span class="main-subtitle">7 个密钥 · 存储于本地 Keychain</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary">导出 .env</button>
      <button class="btn btn-primary">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
        添加密钥
      </button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner stack-loose">

      <!-- summary stats -->
      <div class="stat-grid">
        <div class="stat-card">
          <div class="stat-label">活跃密钥</div>
          <div class="stat-value">7</div>
          <div class="stat-meta"><span>5 Provider · 2 自定义</span></div>
        </div>
        <div class="stat-card">
          <div class="stat-label">即将过期</div>
          <div class="stat-value">1</div>
          <div class="stat-meta"><span class="delta negative">14 天后</span></div>
        </div>
        <div class="stat-card">
          <div class="stat-label">本月请求</div>
          <div class="stat-value">12,485</div>
          <div class="stat-meta"><span class="delta positive">+8.4%</span><span>vs 上月</span></div>
        </div>
        <div class="stat-card">
          <div class="stat-label">最近轮换</div>
          <div class="stat-value">2d</div>
          <div class="stat-meta"><span>Anthropic · 生产</span></div>
        </div>
      </div>

      <!-- filter bar -->
      <div class="row" style="gap: 8px;">
        <div class="row" style="background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 6px 10px; gap: 6px; flex: 1; max-width: 360px;">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;color:var(--muted);"><circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/></svg>
          <input class="input" style="border: none; padding: 0; font-size: 13px;" placeholder="搜索密钥名称或 Provider">
        </div>
        <div class="tabs">
          <button class="tab active">全部</button>
          <button class="tab">生产</button>
          <button class="tab">测试</button>
          <button class="tab">已禁用</button>
        </div>
      </div>

      <!-- keys table -->
      <div class="card" style="padding: 0; overflow: hidden;">
        <table class="tbl">
          <thead>
            <tr>
              <th>Provider</th>
              <th>名称</th>
              <th>密钥</th>
              <th>权限</th>
              <th>最后使用</th>
              <th class="right">用量</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td>
                <div class="row" style="gap: 8px;">
                  <div class="list-icon" style="background: #10a37f; color: white; width: 26px; height: 26px; font-size: 11px; border-radius: 6px;">O</div>
                  <span>OpenAI</span>
                </div>
              </td>
              <td>
                <div style="font-weight: 500;">生产环境</div>
                <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">org-prod-001</div>
              </td>
              <td>
                <div class="row" style="gap: 6px;">
                  <span class="text-mono" style="font-size: 12.5px; color: var(--muted);">sk-prod-****3fA9</span>
                  <button class="btn btn-icon" style="width: 22px; height: 22px;" title="复制"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
              </td>
              <td><span class="badge info">读 + 写</span></td>
              <td><span data-time="1747401735000" class="text-muted">刚刚</span></td>
              <td class="num">3.42M</td>
              <td class="right">
                <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
              </td>
            </tr>
            <tr>
              <td>
                <div class="row" style="gap: 8px;">
                  <div class="list-icon" style="background: #10a37f; color: white; width: 26px; height: 26px; font-size: 11px; border-radius: 6px;">O</div>
                  <span>OpenAI</span>
                </div>
              </td>
              <td>
                <div style="font-weight: 500;">测试沙盒</div>
                <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">org-test-002</div>
              </td>
              <td>
                <div class="row" style="gap: 6px;">
                  <span class="text-mono" style="font-size: 12.5px; color: var(--muted);">sk-test-****8c2D</span>
                  <button class="btn btn-icon" style="width: 22px; height: 22px;"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
              </td>
              <td><span class="badge">只读</span></td>
              <td><span data-time="1747389600000" class="text-muted">3 小时前</span></td>
              <td class="num">128K</td>
              <td class="right">
                <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
              </td>
            </tr>
            <tr>
              <td>
                <div class="row" style="gap: 8px;">
                  <div class="list-icon" style="background: #d97757; color: white; width: 26px; height: 26px; font-size: 11px; border-radius: 6px;">A</div>
                  <span>Anthropic</span>
                </div>
              </td>
              <td>
                <div style="font-weight: 500;">生产环境</div>
                <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">2 天前轮换</div>
              </td>
              <td>
                <div class="row" style="gap: 6px;">
                  <span class="text-mono" style="font-size: 12.5px; color: var(--muted);">sk-ant-****1aB7</span>
                  <button class="btn btn-icon" style="width: 22px; height: 22px;"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
              </td>
              <td><span class="badge info">读 + 写</span></td>
              <td><span data-time="1747399500000" class="text-muted">39 分钟前</span></td>
              <td class="num">2.18M</td>
              <td class="right">
                <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
              </td>
            </tr>
            <tr>
              <td>
                <div class="row" style="gap: 8px;">
                  <div class="list-icon dark" style="width: 26px; height: 26px; font-size: 11px; border-radius: 6px;">D</div>
                  <span>DeepSeek</span>
                </div>
              </td>
              <td>
                <div style="font-weight: 500;">默认</div>
                <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">default</div>
              </td>
              <td>
                <div class="row" style="gap: 6px;">
                  <span class="text-mono" style="font-size: 12.5px; color: var(--muted);">sk-ds-****9eF4</span>
                  <button class="btn btn-icon" style="width: 22px; height: 22px;"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
              </td>
              <td><span class="badge info">读 + 写</span></td>
              <td><span data-time="1747396500000" class="text-muted">1 小时前</span></td>
              <td class="num">1.06M</td>
              <td class="right">
                <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
              </td>
            </tr>
            <tr>
              <td>
                <div class="row" style="gap: 8px;">
                  <div class="list-icon blue" style="width: 26px; height: 26px; font-size: 11px; border-radius: 6px;">M</div>
                  <span>Moonshot</span>
                </div>
              </td>
              <td>
                <div style="font-weight: 500;">长上下文</div>
                <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">moonshot-128k</div>
              </td>
              <td>
                <div class="row" style="gap: 6px;">
                  <span class="text-mono" style="font-size: 12.5px; color: var(--muted);">sk-ms-****5hJ2</span>
                  <button class="btn btn-icon" style="width: 22px; height: 22px;"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
              </td>
              <td><span class="badge info">读 + 写</span></td>
              <td><span data-time="1747382400000" class="text-muted">5 小时前</span></td>
              <td class="num">428K</td>
              <td class="right">
                <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
              </td>
            </tr>
            <tr>
              <td>
                <div class="row" style="gap: 8px;">
                  <div class="list-icon" style="background: #2563eb; color: white; width: 26px; height: 26px; font-size: 11px; border-radius: 6px;">G</div>
                  <span>智谱 GLM</span>
                </div>
              </td>
              <td>
                <div style="font-weight: 500;">生产 (旧)</div>
                <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">即将过期</div>
              </td>
              <td>
                <div class="row" style="gap: 6px;">
                  <span class="text-mono" style="font-size: 12.5px; color: var(--muted);">glm-****4kL8</span>
                  <button class="btn btn-icon" style="width: 22px; height: 22px;"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
              </td>
              <td><span class="badge warn">即将过期</span></td>
              <td><span data-time="1747315200000" class="text-muted">2 天前</span></td>
              <td class="num">—</td>
              <td class="right">
                <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
              </td>
            </tr>
            <tr>
              <td>
                <div class="row" style="gap: 8px;">
                  <div class="list-icon" style="background: var(--graphite); color: white; width: 26px; height: 26px; font-size: 11px; border-radius: 6px;">P</div>
                  <span>自部署</span>
                </div>
              </td>
              <td>
                <div style="font-weight: 500;">私有代理</div>
                <div class="text-muted" style="font-size: 11.5px; margin-top: 1px;">internal-proxy</div>
              </td>
              <td>
                <div class="row" style="gap: 6px;">
                  <span class="text-mono" style="font-size: 12.5px; color: var(--muted);">local-****7tR1</span>
                  <button class="btn btn-icon" style="width: 22px; height: 22px;"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:13px;height:13px;"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg></button>
                </div>
              </td>
              <td><span class="badge">只读</span></td>
              <td><span data-time="1747300800000" class="text-muted">2 天前</span></td>
              <td class="num">312K</td>
              <td class="right">
                <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
