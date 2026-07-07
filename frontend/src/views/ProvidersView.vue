<script setup lang="ts">
function handleTabKeydown(e: KeyboardEvent) {
  const container = e.currentTarget as HTMLElement
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
      <h1 class="main-title">Provider 管理</h1>
      <span class="main-subtitle">5 个连接 · 16 个模型</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary">测试全部</button>
      <button class="btn btn-primary">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
        添加 Provider
      </button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner stack-loose">
      <!-- Filter bar -->
      <div class="row" style="gap: 8px; flex-wrap: wrap;">
        <div class="row" style="background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 6px 10px; gap: 6px; flex: 1; max-width: 360px;">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:14px;height:14px;color:var(--muted);"><circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/></svg>
          <input class="input" style="border: none; padding: 0; font-size: 13px;" placeholder="搜索 Provider 或模型">
        </div>
        <div class="tabs" @keydown="handleTabKeydown" style="outline: none;">
          <button class="tab active">全部</button>
          <button class="tab">已连接</button>
          <button class="tab">异常</button>
        </div>
        <div class="spacer"></div>
        <div class="row" style="font-size: 12px; color: var(--muted);">
          排序：
          <select class="select" style="width: auto; padding: 5px 10px; font-size: 12px;">
            <option>用量</option><option>名称</option><option>最近测试</option>
          </select>
        </div>
      </div>

      <!-- Provider cards grid -->
      <section class="col-2">
        <article class="card card-hover">
          <div class="row-between" style="margin-bottom: 14px;">
            <div class="row" style="gap: 12px;">
              <div class="list-icon" style="background: #10a37f; color: white; width: 38px; height: 38px; font-size: 15px;">O</div>
              <div>
                <div style="font-size: 15px; font-weight: 600;">OpenAI</div>
                <div class="text-mono text-muted" style="font-size: 11.5px; margin-top: 1px;">api.openai.com</div>
              </div>
            </div>
            <span class="badge success"><span class="dot green"></span>已连接</span>
          </div>
          <div class="h-divider" style="margin: 0 0 14px;"></div>
          <div class="row-between" style="margin-bottom: 10px;">
            <span class="text-muted" style="font-size: 12px;">本月用量</span>
            <span class="text-mono" style="font-size: 13px; font-weight: 500;">3.42M tokens</span>
          </div>
          <div class="row-between" style="margin-bottom: 14px;">
            <span class="text-muted" style="font-size: 12px;">平均延迟</span>
            <span class="text-mono" style="font-size: 13px; font-weight: 500;">0.92s</span>
          </div>
          <div class="row" style="flex-wrap: wrap; gap: 4px; margin-bottom: 14px;">
            <span class="badge mono">gpt-4o</span>
            <span class="badge mono">gpt-4o-mini</span>
            <span class="badge mono">o1</span>
            <span class="badge mono">o1-mini</span>
          </div>
          <div class="row-between">
            <span class="text-muted" style="font-size: 11px;">测试于 3 分钟前</span>
            <div class="row" style="gap: 4px;">
              <button class="btn btn-secondary" style="padding: 4px 10px; font-size: 12px;">测试</button>
              <button class="btn btn-icon" title="编辑">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4z"/></svg>
              </button>
              <button class="btn btn-icon" title="更多">
                <svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg>
              </button>
            </div>
          </div>
        </article>

        <article class="card card-hover">
          <div class="row-between" style="margin-bottom: 14px;">
            <div class="row" style="gap: 12px;">
              <div class="list-icon" style="background: #d97757; color: white; width: 38px; height: 38px; font-size: 15px;">A</div>
              <div>
                <div style="font-size: 15px; font-weight: 600;">Anthropic</div>
                <div class="text-mono text-muted" style="font-size: 11.5px; margin-top: 1px;">api.anthropic.com</div>
              </div>
            </div>
            <span class="badge success"><span class="dot green"></span>已连接</span>
          </div>
          <div class="h-divider" style="margin: 0 0 14px;"></div>
          <div class="row-between" style="margin-bottom: 10px;">
            <span class="text-muted" style="font-size: 12px;">本月用量</span>
            <span class="text-mono" style="font-size: 13px; font-weight: 500;">2.18M tokens</span>
          </div>
          <div class="row-between" style="margin-bottom: 14px;">
            <span class="text-muted" style="font-size: 12px;">平均延迟</span>
            <span class="text-mono" style="font-size: 13px; font-weight: 500;">1.34s</span>
          </div>
          <div class="row" style="flex-wrap: wrap; gap: 4px; margin-bottom: 14px;">
            <span class="badge mono">claude-sonnet-4-5</span>
            <span class="badge mono">claude-opus-4-1</span>
            <span class="badge mono">claude-haiku-4-5</span>
          </div>
          <div class="row-between">
            <span class="text-muted" style="font-size: 11px;">测试于 5 分钟前</span>
            <div class="row" style="gap: 4px;">
              <button class="btn btn-secondary" style="padding: 4px 10px; font-size: 12px;">测试</button>
              <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4z"/></svg></button>
              <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
            </div>
          </div>
        </article>

        <article class="card card-hover">
          <div class="row-between" style="margin-bottom: 14px;">
            <div class="row" style="gap: 12px;">
              <div class="list-icon dark" style="width: 38px; height: 38px; font-size: 15px;">D</div>
              <div>
                <div style="font-size: 15px; font-weight: 600;">DeepSeek</div>
                <div class="text-mono text-muted" style="font-size: 11.5px; margin-top: 1px;">api.deepseek.com</div>
              </div>
            </div>
            <span class="badge success"><span class="dot green"></span>已连接</span>
          </div>
          <div class="h-divider" style="margin: 0 0 14px;"></div>
          <div class="row-between" style="margin-bottom: 10px;">
            <span class="text-muted" style="font-size: 12px;">本月用量</span>
            <span class="text-mono" style="font-size: 13px; font-weight: 500;">1.06M tokens</span>
          </div>
          <div class="row-between" style="margin-bottom: 14px;">
            <span class="text-muted" style="font-size: 12px;">平均延迟</span>
            <span class="text-mono" style="font-size: 13px; font-weight: 500;">2.18s</span>
          </div>
          <div class="row" style="flex-wrap: wrap; gap: 4px; margin-bottom: 14px;">
            <span class="badge mono">deepseek-chat</span>
            <span class="badge mono">deepseek-reasoner</span>
          </div>
          <div class="row-between">
            <span class="text-muted" style="font-size: 11px;">测试于 8 分钟前</span>
            <div class="row" style="gap: 4px;">
              <button class="btn btn-secondary" style="padding: 4px 10px; font-size: 12px;">测试</button>
              <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4z"/></svg></button>
              <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
            </div>
          </div>
        </article>

        <article class="card card-hover">
          <div class="row-between" style="margin-bottom: 14px;">
            <div class="row" style="gap: 12px;">
              <div class="list-icon blue" style="width: 38px; height: 38px; font-size: 15px;">M</div>
              <div>
                <div style="font-size: 15px; font-weight: 600;">Moonshot</div>
                <div class="text-mono text-muted" style="font-size: 11.5px; margin-top: 1px;">api.moonshot.cn</div>
              </div>
            </div>
            <span class="badge success"><span class="dot green"></span>已连接</span>
          </div>
          <div class="h-divider" style="margin: 0 0 14px;"></div>
          <div class="row-between" style="margin-bottom: 10px;">
            <span class="text-muted" style="font-size: 12px;">本月用量</span>
            <span class="text-mono" style="font-size: 13px; font-weight: 500;">428K tokens</span>
          </div>
          <div class="row-between" style="margin-bottom: 14px;">
            <span class="text-muted" style="font-size: 12px;">平均延迟</span>
            <span class="text-mono" style="font-size: 13px; font-weight: 500;">2.85s</span>
          </div>
          <div class="row" style="flex-wrap: wrap; gap: 4px; margin-bottom: 14px;">
            <span class="badge mono">moonshot-v1-128k</span>
            <span class="badge mono">moonshot-v1-32k</span>
            <span class="badge mono">kimi-latest</span>
          </div>
          <div class="row-between">
            <span class="text-muted" style="font-size: 11px;">测试于 12 分钟前</span>
            <div class="row" style="gap: 4px;">
              <button class="btn btn-secondary" style="padding: 4px 10px; font-size: 12px;">测试</button>
              <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4z"/></svg></button>
              <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
            </div>
          </div>
        </article>

        <article class="card card-hover" style="opacity: 0.78;">
          <div class="row-between" style="margin-bottom: 14px;">
            <div class="row" style="gap: 12px;">
              <div class="list-icon" style="background: #2563eb; color: white; width: 38px; height: 38px; font-size: 15px;">G</div>
              <div>
                <div style="font-size: 15px; font-weight: 600;">智谱 GLM</div>
                <div class="text-mono text-muted" style="font-size: 11.5px; margin-top: 1px;">open.bigmodel.cn</div>
              </div>
            </div>
            <span class="badge error"><span class="dot red"></span>未连接</span>
          </div>
          <div class="h-divider" style="margin: 0 0 14px;"></div>
          <div class="row-between" style="margin-bottom: 10px;">
            <span class="text-muted" style="font-size: 12px;">本月用量</span>
            <span class="text-mono text-muted" style="font-size: 13px; font-weight: 500;">—</span>
          </div>
          <div class="row-between" style="margin-bottom: 14px;">
            <span class="text-muted" style="font-size: 12px;">最后错误</span>
            <span class="text-mono" style="font-size: 12px; color: var(--negative);">401 Unauthorized</span>
          </div>
          <div class="row" style="flex-wrap: wrap; gap: 4px; margin-bottom: 14px;">
            <span class="badge mono">glm-4-plus</span>
            <span class="badge mono">glm-4-air</span>
            <span class="badge mono">glm-4-flash</span>
            <span class="badge mono">glm-zero</span>
          </div>
          <div class="row-between">
            <span class="text-muted" style="font-size: 11px;">失败于 2 小时前</span>
            <div class="row" style="gap: 4px;">
              <button class="btn btn-primary" style="padding: 4px 10px; font-size: 12px;">重新连接</button>
              <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
            </div>
          </div>
        </article>

        <article class="card card-hover" style="border-style: dashed; display: flex; align-items: center; justify-content: center; min-height: 240px; background: transparent; cursor: pointer;">
          <div style="text-align: center; color: var(--muted);">
            <div style="width: 48px; height: 48px; border-radius: 24px; background: rgba(0, 113, 227, 0.08); display: inline-flex; align-items: center; justify-content: center; margin-bottom: 12px;">
              <svg viewBox="0 0 24 24" fill="none" stroke="#0071e3" stroke-width="1.6" stroke-linecap="round" style="width:22px;height:22px;"><path d="M12 5v14M5 12h14"/></svg>
            </div>
            <div style="font-size: 14px; font-weight: 500; color: var(--fg);">添加自定义 Provider</div>
            <div style="font-size: 12px; margin-top: 4px;">OpenAI 兼容 / 自部署网关</div>
          </div>
        </article>
      </section>
    </div>
  </div>
</template>
