<script setup lang="ts">
import { onMounted } from 'vue'
import SidebarNav from './SidebarNav.vue'
import { useTheme } from '@/composables/useTheme'

const { loadFromSettings } = useTheme()

function handleClose() {
  const win = document.querySelector('.window')
  if (win) {
    win.classList.add('is-closing')
    setTimeout(() => {
      win.classList.remove('is-closing')
    }, 350)
  }
}

onMounted(() => {
  void loadFromSettings()
})
</script>

<template>
  <div class="window">
    <!-- Title bar -->
    <div class="titlebar">
      <div class="traffic-lights">
        <span class="light close" title="关闭" @click="handleClose"></span>
        <span class="light min" title="最小化"></span>
        <span class="light max" title="最大化"></span>
      </div>
      <div class="window-title">autoapi — 模型路由</div>
      <div class="titlebar-actions">
        <span class="kbd">⌘K</span>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/></svg>
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
