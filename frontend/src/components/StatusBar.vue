<script setup lang="ts">
import { computed, onMounted, onUnmounted } from 'vue'
import { api } from '@/api/client'
import { useApi } from '@/composables/useApi'

const { data: health, execute: loadHealth } = useApi(api.systemHealth)

const proxyURL = computed(() => health.value?.proxy_url || '')
const proxyRunning = computed(() => health.value?.status === 'running')

let timer: ReturnType<typeof setInterval> | null = null

onMounted(() => {
  void loadHealth()
  timer = setInterval(() => void loadHealth(), 5000)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<template>
  <footer class="statusbar">
    <div class="status-row">
      <span
        class="status-dot"
        :style="{
          background: proxyRunning ? 'var(--positive)' : 'var(--negative)',
          boxShadow: proxyRunning ? '0 0 0 3px rgba(40, 167, 69, 0.18)' : '0 0 0 3px rgba(217, 48, 37, 0.18)',
        }"
      ></span>
      <span>{{ proxyRunning ? '服务运行中' : '服务已停止' }}</span>
    </div>
    <div class="status-url">{{ proxyURL || '未启动' }} · v0.4.2</div>
  </footer>
</template>
