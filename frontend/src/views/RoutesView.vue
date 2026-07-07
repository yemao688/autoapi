<script setup lang="ts">
</script>

<template>
  <header class="main-header">
    <div class="main-title-group">
      <h1 class="main-title">路由规则</h1>
      <span class="main-subtitle">7 条规则 · 按优先级自上而下匹配</span>
    </div>
    <div class="main-actions">
      <button class="btn btn-secondary">从 JSON 导入</button>
      <button class="btn btn-primary">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
        新建规则
      </button>
    </div>
  </header>

  <div class="main-content">
    <div class="main-content-inner stack-loose">

      <!-- Default fallback banner -->
      <div class="card" style="background: var(--black); color: white; display: flex; align-items: center; gap: 16px; padding: 16px 20px;">
        <div style="width: 36px; height: 36px; border-radius: 10px; background: rgba(255,255,255,0.12); display: flex; align-items: center; justify-content: center; flex-shrink: 0;">
          <svg viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" style="width:18px;height:18px;"><path d="M5 12h14M12 5l7 7-7 7"/></svg>
        </div>
        <div style="flex: 1;">
          <div style="font-size: 13px; font-weight: 600;">默认兜底</div>
          <div style="font-size: 12px; color: rgba(255,255,255,0.6); margin-top: 2px;">所有未匹配的请求将路由至 <span class="text-mono" style="color: white;">OpenAI · gpt-4o-mini</span></div>
        </div>
        <button class="btn" style="background: rgba(255,255,255,0.12); color: white;">修改</button>
      </div>

      <!-- Rule 1 -->
      <article class="card">
        <div class="row-between" style="margin-bottom: 14px;">
          <div class="row" style="gap: 12px;">
            <div class="text-mono" style="font-size: 12px; color: var(--muted); width: 24px;">01</div>
            <div>
              <div style="font-size: 14px; font-weight: 600;">智能路由 · 高优先级</div>
              <div class="text-muted" style="font-size: 12px; margin-top: 2px;">将高优先级请求自动改用 Claude</div>
            </div>
          </div>
          <div class="row" style="gap: 12px;">
            <span class="badge info">优先级 1</span>
            <label class="toggle"><input type="checkbox" checked><span class="toggle-slider blue"></span></label>
            <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
          </div>
        </div>
        <div class="col-3" style="gap: 0;">
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">条件 (所有满足)</div>
            <div class="stack-tight">
              <div class="row" style="gap: 6px;"><span class="text-mono" style="font-size: 11.5px; color: var(--fg);">model</span><span class="badge mono" style="background: rgba(0,113,227,0.08); color: var(--accent); font-weight: 500;">matches</span><span class="text-mono" style="font-size: 11.5px;">gpt-4o*</span></div>
              <div class="row" style="gap: 6px;"><span class="text-mono" style="font-size: 11.5px; color: var(--fg);">header.x-priority</span><span class="badge mono" style="background: rgba(0,113,227,0.08); color: var(--accent); font-weight: 500;">equals</span><span class="text-mono" style="font-size: 11.5px;">high</span></div>
            </div>
          </div>
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">目标</div>
            <div class="row" style="gap: 8px;">
              <div class="list-icon solid-orange" style="width: 24px; height: 24px; font-size: 11px; border-radius: 6px;">A</div>
              <div>
                <div style="font-size: 13px; font-weight: 500;">Anthropic</div>
                <div class="text-mono text-muted" style="font-size: 11.5px;">claude-sonnet-4-5</div>
              </div>
            </div>
          </div>
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">本月命中</div>
            <div class="text-mono" style="font-size: 22px; font-weight: 600; letter-spacing: -0.02em;">3,842</div>
            <div class="text-muted text-mono" style="font-size: 11px; margin-top: 2px;">节省 ¥128.40</div>
          </div>
        </div>
      </article>

      <!-- Rule 2 -->
      <article class="card">
        <div class="row-between" style="margin-bottom: 14px;">
          <div class="row" style="gap: 12px;">
            <div class="text-mono" style="font-size: 12px; color: var(--muted); width: 24px;">02</div>
            <div>
              <div style="font-size: 14px; font-weight: 600;">成本优化 · 短文本</div>
              <div class="text-muted" style="font-size: 12px; margin-top: 2px;">轻量请求走 gpt-4o-mini</div>
            </div>
          </div>
          <div class="row" style="gap: 12px;">
            <span class="badge">优先级 2</span>
            <label class="toggle"><input type="checkbox" checked><span class="toggle-slider blue"></span></label>
            <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
          </div>
        </div>
        <div class="col-3" style="gap: 0;">
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">条件 (所有满足)</div>
            <div class="stack-tight">
              <div class="row" style="gap: 6px;"><span class="text-mono" style="font-size: 11.5px;">estimated_tokens</span><span class="badge mono" style="background: rgba(0,113,227,0.08); color: var(--accent); font-weight: 500;">lt</span><span class="text-mono" style="font-size: 11.5px;">1,000</span></div>
              <div class="row" style="gap: 6px;"><span class="text-mono" style="font-size: 11.5px;">task</span><span class="badge mono" style="background: rgba(0,113,227,0.08); color: var(--accent); font-weight: 500;">in</span><span class="text-mono" style="font-size: 11.5px;">[chat, classify, extract]</span></div>
            </div>
          </div>
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">目标</div>
            <div class="row" style="gap: 8px;">
              <div class="list-icon" style="background: #10a37f; color: white; width: 24px; height: 24px; font-size: 11px; border-radius: 6px;">O</div>
              <div>
                <div style="font-size: 13px; font-weight: 500;">OpenAI</div>
                <div class="text-mono text-muted" style="font-size: 11.5px;">gpt-4o-mini</div>
              </div>
            </div>
          </div>
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">本月命中</div>
            <div class="text-mono" style="font-size: 22px; font-weight: 600; letter-spacing: -0.02em;">6,128</div>
            <div class="text-muted text-mono" style="font-size: 11px; margin-top: 2px;">节省 ¥42.18</div>
          </div>
        </div>
      </article>

      <!-- Rule 3 -->
      <article class="card">
        <div class="row-between" style="margin-bottom: 14px;">
          <div class="row" style="gap: 12px;">
            <div class="text-mono" style="font-size: 12px; color: var(--muted); width: 24px;">03</div>
            <div>
              <div style="font-size: 14px; font-weight: 600;">长上下文 · Moonshot</div>
              <div class="text-muted" style="font-size: 12px; margin-top: 2px;">超过 50K token 时使用 128k 模型</div>
            </div>
          </div>
          <div class="row" style="gap: 12px;">
            <span class="badge">优先级 3</span>
            <label class="toggle"><input type="checkbox" checked><span class="toggle-slider blue"></span></label>
            <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
          </div>
        </div>
        <div class="col-3" style="gap: 0;">
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">条件 (所有满足)</div>
            <div class="stack-tight">
              <div class="row" style="gap: 6px;"><span class="text-mono" style="font-size: 11.5px;">estimated_tokens</span><span class="badge mono" style="background: rgba(0,113,227,0.08); color: var(--accent); font-weight: 500;">gt</span><span class="text-mono" style="font-size: 11.5px;">50,000</span></div>
            </div>
          </div>
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">目标</div>
            <div class="row" style="gap: 8px;">
              <div class="list-icon blue" style="width: 24px; height: 24px; font-size: 11px; border-radius: 6px;">M</div>
              <div>
                <div style="font-size: 13px; font-weight: 500;">Moonshot</div>
                <div class="text-mono text-muted" style="font-size: 11.5px;">moonshot-v1-128k</div>
              </div>
            </div>
          </div>
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">本月命中</div>
            <div class="text-mono" style="font-size: 22px; font-weight: 600; letter-spacing: -0.02em;">214</div>
            <div class="text-muted text-mono" style="font-size: 11px; margin-top: 2px;">占总请求 1.7%</div>
          </div>
        </div>
      </article>

      <!-- Rule 4 -->
      <article class="card">
        <div class="row-between" style="margin-bottom: 14px;">
          <div class="row" style="gap: 12px;">
            <div class="text-mono" style="font-size: 12px; color: var(--muted); width: 24px;">04</div>
            <div>
              <div style="font-size: 14px; font-weight: 600;">推理任务 · DeepSeek</div>
              <div class="text-muted" style="font-size: 12px; margin-top: 2px;">o1 / o1-mini 调用转给 deepseek-reasoner</div>
            </div>
          </div>
          <div class="row" style="gap: 12px;">
            <span class="badge">优先级 4</span>
            <label class="toggle"><input type="checkbox" checked><span class="toggle-slider blue"></span></label>
            <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
          </div>
        </div>
        <div class="col-3" style="gap: 0;">
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">条件 (所有满足)</div>
            <div class="stack-tight">
              <div class="row" style="gap: 6px;"><span class="text-mono" style="font-size: 11.5px;">model</span><span class="badge mono" style="background: rgba(0,113,227,0.08); color: var(--accent); font-weight: 500;">matches</span><span class="text-mono" style="font-size: 11.5px;">o1*</span></div>
            </div>
          </div>
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">目标</div>
            <div class="row" style="gap: 8px;">
              <div class="list-icon dark" style="width: 24px; height: 24px; font-size: 11px; border-radius: 6px;">D</div>
              <div>
                <div style="font-size: 13px; font-weight: 500;">DeepSeek</div>
                <div class="text-mono text-muted" style="font-size: 11.5px;">deepseek-reasoner</div>
              </div>
            </div>
          </div>
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">本月命中</div>
            <div class="text-mono" style="font-size: 22px; font-weight: 600; letter-spacing: -0.02em;">1,408</div>
            <div class="text-muted text-mono" style="font-size: 11px; margin-top: 2px;">节省 ¥312.50</div>
          </div>
        </div>
      </article>

      <!-- Disabled rule -->
      <article class="card" style="opacity: 0.6;">
        <div class="row-between" style="margin-bottom: 14px;">
          <div class="row" style="gap: 12px;">
            <div class="text-mono" style="font-size: 12px; color: var(--muted); width: 24px;">05</div>
            <div>
              <div style="font-size: 14px; font-weight: 600;">夜间静音 · 跳过 Claude</div>
              <div class="text-muted" style="font-size: 12px; margin-top: 2px;">23:00 - 07:00 不路由到 Anthropic</div>
            </div>
          </div>
          <div class="row" style="gap: 12px;">
            <span class="badge">优先级 5</span>
            <label class="toggle"><input type="checkbox"><span class="toggle-slider blue"></span></label>
            <button class="btn btn-icon"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/></svg></button>
          </div>
        </div>
        <div class="col-3" style="gap: 0;">
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">条件</div>
            <div class="stack-tight">
              <div class="row" style="gap: 6px;"><span class="text-mono" style="font-size: 11.5px;">time.hour</span><span class="badge mono" style="background: rgba(0,113,227,0.08); color: var(--accent); font-weight: 500;">between</span><span class="text-mono" style="font-size: 11.5px;">23, 7</span></div>
            </div>
          </div>
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">动作</div>
            <div style="font-size: 13px;">跳过 Provider: Anthropic</div>
          </div>
          <div>
            <div class="text-muted" style="font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; margin-bottom: 6px;">状态</div>
            <div class="text-mono" style="font-size: 12px; color: var(--muted);">已禁用 · 创建于 1 周前</div>
          </div>
        </div>
      </article>
    </div>
  </div>
</template>
