<script setup lang="ts">
import { onMounted } from 'vue'
import { RouterLink } from 'vue-router'
import SidebarNav from './SidebarNav.vue'
import StatusBar from './StatusBar.vue'
import { useTheme } from '@/composables/useTheme'

const { loadFromSettings } = useTheme()

onMounted(() => {
  void loadFromSettings()
})
</script>

<template>
  <div class="window">
    <!-- Title bar -->
    <div class="titlebar">
      <div class="window-title">Autoapi — 模型路由</div>
      <div class="titlebar-actions">
        <RouterLink to="/settings" class="btn-icon" aria-label="设置">
          <svg aria-hidden="true" focusable="false" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3h0a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8v0a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/></svg>
        </RouterLink>
      </div>
    </div>

    <!-- App body -->
    <div class="app-body">
      <SidebarNav />
      <main class="main">
        <router-view />
      </main>
    </div>

    <!-- Status bar -->
    <StatusBar />
  </div>
</template>
