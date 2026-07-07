<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { api } from '@/api/client'
import { useApi } from '@/composables/useApi'
import { useMasterGate } from '@/composables/useMasterGate'

const route = useRoute()
const { state: gateState } = useMasterGate()

const { data: providers, execute: loadProviders } = useApi(api.providers)
const { data: routes, execute: loadRoutes } = useApi(api.routes)
const { data: health, execute: loadHealth } = useApi(api.systemHealth)

const providerCount = computed(() => providers.value?.length ?? 0)
const routeCount = computed(() => routes.value?.length ?? 0)
const proxyURL = computed(() => health.value?.proxy_url || '')
const proxyRunning = computed(() => health.value?.status === 'running')

const navItems = computed(() => [
  { to: '/dashboard', label: '总览', icon: 'dashboard', badge: null as number | null },
  { to: '/providers', label: 'Provider', icon: 'cloud', badge: providerCount.value },
  { to: '/routes', label: '路由规则', icon: 'routes', badge: routeCount.value },
  { to: '/api-keys', label: 'API 密钥', icon: 'key', badge: null as number | null },
  { to: '/usage-stats', label: '使用统计', icon: 'chart', badge: null as number | null },
])

const systemItems = [
  { to: '/settings', label: '设置', icon: 'settings', badge: null },
]

function isActive(to: string): boolean {
  return route.path === to
}

let timer: ReturnType<typeof setInterval> | null = null

async function refresh() {
  if (gateState.value !== 'ready') return
  await Promise.all([loadProviders(), loadRoutes(), loadHealth()])
}

onMounted(() => {
  void refresh()
  timer = setInterval(() => void refresh(), 5000)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<template>
  <aside class="sidebar">
    <div class="sidebar-brand">
      <span class="logo-dot">A</span>
      <span>autoapi</span>
    </div>
    <div class="sidebar-section-label">主导航</div>
    <nav class="sidebar-nav">
      <RouterLink
        v-for="item in navItems"
        :key="item.to"
        :to="item.to"
        class="nav-item"
        :class="{ active: isActive(item.to) }"
      >
        <!-- Dashboard icon -->
        <svg v-if="item.icon === 'dashboard'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="9"/><rect x="14" y="3" width="7" height="5"/><rect x="14" y="12" width="7" height="9"/><rect x="3" y="16" width="7" height="5"/></svg>
        <!-- Cloud icon -->
        <svg v-else-if="item.icon === 'cloud'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M17.5 19a4.5 4.5 0 0 0 0-9 6 6 0 0 0-11.6 1.4A4 4 0 0 0 6.5 19h11z"/></svg>
        <!-- Routes icon -->
        <svg v-else-if="item.icon === 'routes'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="6" r="2.5"/><circle cx="12" cy="18" r="2.5"/><path d="M8 7l8 0M7 8l4 8M17 8l-4 8"/></svg>
        <!-- Key icon -->
        <svg v-else-if="item.icon === 'key'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="15" r="4"/><path d="M11 12l9-9M16 7l3 3M14 9l3 3"/></svg>
        <!-- Chart icon -->
        <svg v-else-if="item.icon === 'chart'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3v18h18"/><path d="M7 14l3-3 3 3 5-6"/></svg>
        <!-- Settings icon -->
        <svg v-else-if="item.icon === 'settings'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3h0a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8v0a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/></svg>
        <span>{{ item.label }}</span>
        <span v-if="item.badge" class="badge">{{ item.badge }}</span>
      </RouterLink>
    </nav>
    <div class="sidebar-section-label">系统</div>
    <nav class="sidebar-nav">
      <RouterLink
        v-for="item in systemItems"
        :key="item.to"
        :to="item.to"
        class="nav-item"
        :class="{ active: isActive(item.to) }"
      >
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3h0a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8v0a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/></svg>
        <span>{{ item.label }}</span>
      </RouterLink>
    </nav>
    <div class="sidebar-footer">
      <div class="status-row">
        <span class="status-dot" :style="{ background: proxyRunning ? 'var(--positive)' : 'var(--negative)', boxShadow: proxyRunning ? '0 0 0 3px rgba(40, 167, 69, 0.18)' : '0 0 0 3px rgba(217, 48, 37, 0.18)' }"></span>
        <span>{{ proxyRunning ? '服务运行中' : '服务已停止' }}</span>
      </div>
      <div>{{ proxyURL || '未启动' }} · v0.4.2</div>
    </div>
  </aside>
</template>
