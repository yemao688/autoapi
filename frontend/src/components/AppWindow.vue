<script setup lang="ts">
import { onMounted } from 'vue'
import { RouterLink } from 'vue-router'
import SidebarNav from './SidebarNav.vue'
import StatusBar from './StatusBar.vue'
import DropdownMenu from './DropdownMenu.vue'
import { useTheme } from '@/composables/useTheme'

const { loadFromSettings, activeTheme, saveTheme } = useTheme()

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
        <DropdownMenu menu-id="theme-toggle" placement="down" :min-width="160">
          <template #trigger="{ toggle, open }">
            <button
              class="btn-icon"
              type="button"
              aria-label="切换外观"
              :aria-expanded="open"
              data-dropdown-trigger
              @click="toggle"
            >
              <!-- light: sun -->
              <svg v-if="activeTheme === 'light'" aria-hidden="true" focusable="false" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="5"/><path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42"/></svg>
              <!-- dark: moon -->
              <svg v-else-if="activeTheme === 'dark'" aria-hidden="true" focusable="false" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
              <!-- system: monitor -->
              <svg v-else aria-hidden="true" focusable="false" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="20" height="14" rx="2"/><path d="M8 21h8M12 17v4"/></svg>
            </button>
          </template>
          <template #menu="{ close }">
            <button class="dropdown-item" role="menuitem" type="button" @click="saveTheme('light'); close()">浅色</button>
            <button class="dropdown-item" role="menuitem" type="button" @click="saveTheme('dark'); close()">深色</button>
            <button class="dropdown-item" role="menuitem" type="button" @click="saveTheme('system'); close()">跟随系统</button>
          </template>
        </DropdownMenu>
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
