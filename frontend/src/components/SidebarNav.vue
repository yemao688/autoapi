<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { api } from '@/api/bridge'
import { useApi } from '@/composables/useApi'
import { usePolling } from '@/composables/usePolling'

const route = useRoute()

const { data: providers, execute: loadProviders } = useApi(api.providers)
const { data: modelRules, execute: loadModelRules } = useApi(api.modelRules)

const providerCount = computed(() => providers.value?.length ?? 0)
const modelRuleCount = computed(() => modelRules.value?.length ?? 0)

const navItems = computed(() => [
  { to: '/dashboard', labelKey: 'nav.dashboard', icon: 'dashboard', badge: null as number | null },
  { to: '/providers', labelKey: 'nav.providers', icon: 'cloud', badge: providerCount.value },
  { to: '/upstream-monitoring', labelKey: 'nav.upstreamMonitoring', icon: 'activity', badge: null as number | null },
  { to: '/model-rules', labelKey: 'nav.modelRules', icon: 'modelRules', badge: modelRuleCount.value },
  { to: '/tool-access', labelKey: 'nav.toolAccess', icon: 'toolAccess', badge: null as number | null },
  { to: '/api-keys', labelKey: 'nav.apiKeys', icon: 'key', badge: null as number | null },
  { to: '/usage-stats', labelKey: 'nav.usage', icon: 'chart', badge: null as number | null },
])

function isActive(to: string): boolean {
  return route.path === to
}

async function refresh() {
  await Promise.all([loadProviders(), loadModelRules()])
}

usePolling(refresh, 30000)
</script>

<template>
  <aside class="sidebar">
    <div class="sidebar-brand">
      <span class="logo-dot">A</span>
      <span>{{ $t('app.tagline') }}</span>
    </div>
    <div class="sidebar-section-label">{{ $t('nav.main') }}</div>
    <nav class="sidebar-nav">
      <RouterLink
        v-for="item in navItems"
        :key="item.to"
        :to="item.to"
        class="nav-item"
        :class="{ active: isActive(item.to) }"
      >
        <svg v-if="item.icon === 'dashboard'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="9"/><rect x="14" y="3" width="7" height="5"/><rect x="14" y="12" width="7" height="9"/><rect x="3" y="16" width="7" height="5"/></svg>
        <svg v-else-if="item.icon === 'cloud'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M17.5 19a4.5 4.5 0 0 0 0-9 6 6 0 0 0-11.6 1.4A4 4 0 0 0 6.5 19h11z"/></svg>
        <svg v-else-if="item.icon === 'modelRules'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="6" r="2.5"/><circle cx="12" cy="18" r="2.5"/><path d="M8 7l8 0M7 8l4 8M17 8l-4 8"/></svg>
        <svg v-else-if="item.icon === 'toolAccess'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M14.7 6.3a4 4 0 0 0-5.4 5.4L3.5 17.5a2.12 2.12 0 0 0 3 3l5.8-5.8a4 4 0 0 0 5.4-5.4l-2.2 2.2-2.5-.5-.5-2.5 2.2-2.2z"/><path d="m15.5 15.5 5 5"/></svg>
        <svg v-else-if="item.icon === 'key'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="15" r="4"/><path d="M11 12l9-9M16 7l3 3M14 9l3 3"/></svg>
        <svg v-else-if="item.icon === 'chart'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3v18h18"/><path d="M7 14l3-3 3 3 5-6"/></svg>
        <svg v-else-if="item.icon === 'activity'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12h4l2.2-6 4.1 12 2.2-6H21"/></svg>
        <svg v-else-if="item.icon === 'dollar'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="1" x2="12" y2="23"/><path d="M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"/></svg>
        <span>{{ $t(item.labelKey) }}</span>
        <span v-if="item.badge" class="badge">{{ item.badge }}</span>
      </RouterLink>
    </nav>
  </aside>
</template>
