<script setup lang="ts">
import { onMounted } from 'vue'
import SidebarNav from './SidebarNav.vue'
import { useTheme } from '@/composables/useTheme'

const { loadFromSettings } = useTheme()

function onTitlebarMouseDown(e: MouseEvent) {
  if ((e.target as HTMLElement).closest('.titlebar-actions')) return
  // Wails v2 exposes runtime on window; generated wrapper lacks WindowStartDragging in this project
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ;(window as any).runtime?.WindowStartDragging?.()
}

onMounted(() => {
  void loadFromSettings()
})
</script>

<template>
  <div class="window">
    <!-- Title bar -->
    <div class="titlebar" @mousedown="onTitlebarMouseDown">
      <div class="window-title">autoapi — 模型路由</div>
      <div class="titlebar-actions" @mousedown.stop>
        <span class="kbd">⌘K</span>
        <svg aria-hidden="true" focusable="false" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/></svg>
      </div>
    </div>

    <!-- App body -->
    <div class="app-body">
      <SidebarNav />
      <main class="main">
        <router-view />
      </main>
    </div>
  </div>
</template>
