<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/client'
import { useApi } from '@/composables/useApi'
import { usePolling } from '@/composables/usePolling'

const { t } = useI18n()

const { data: health, execute: loadHealth } = useApi(api.systemHealth)

const proxyURL = computed(() => health.value?.proxy_url || '')
const apiAddress = computed(() => health.value?.api_address || '')
const proxyRunning = computed(() => health.value?.status === 'running')

const copyState = ref<'idle' | 'copied'>('idle')
let copyTimer: ReturnType<typeof setTimeout> | null = null

const displayAddress = computed(() => {
  if (!proxyRunning.value) return t('status.serviceNotStarted')
  return apiAddress.value || proxyURL.value || t('status.serviceNotStarted')
})

async function copyApiAddress() {
  const value = apiAddress.value
  if (!value) return

  try {
    if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
      await navigator.clipboard.writeText(value)
    } else {
      const ta = document.createElement('textarea')
      ta.value = value
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    copyState.value = 'copied'
    if (copyTimer) clearTimeout(copyTimer)
    copyTimer = setTimeout(() => {
      copyState.value = 'idle'
    }, 1500)
  } catch {
    // Silent: clipboard can be denied in some webview contexts.
  }
}

onUnmounted(() => {
  if (copyTimer) clearTimeout(copyTimer)
})

usePolling(loadHealth, 30000)
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
      <span>{{ proxyRunning ? t('status.serviceRunning') : t('status.serviceStopped') }}</span>
      <span class="status-divider" aria-hidden="true"></span>
      <span class="status-address" :class="{ 'is-empty': !proxyRunning || !apiAddress }">
        {{ displayAddress }}
      </span>
      <button
        v-if="proxyRunning && apiAddress"
        class="status-copy"
        type="button"
        :title="copyState === 'copied' ? t('status.copied') : t('status.copyAddress')"
        :aria-label="copyState === 'copied' ? t('status.copied') : t('status.copyAddress')"
        @click="copyApiAddress"
      >
        <svg
          v-if="copyState === 'idle'"
          width="12"
          height="12"
          viewBox="0 0 16 16"
          fill="none"
          aria-hidden="true"
        >
          <rect x="4" y="4" width="9" height="9" rx="1.5" stroke="currentColor" stroke-width="1.4" />
          <path
            d="M3 11V3.5A1.5 1.5 0 0 1 4.5 2H11"
            stroke="currentColor"
            stroke-width="1.4"
            stroke-linecap="round"
          />
        </svg>
        <svg
          v-else
          width="12"
          height="12"
          viewBox="0 0 16 16"
          fill="none"
          aria-hidden="true"
        >
          <path
            d="M3.5 8.5L6.5 11.5L12.5 4.5"
            stroke="currentColor"
            stroke-width="1.6"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
        </svg>
      </button>
      <span class="sr-only" role="status" aria-live="polite">{{ copyState === 'copied' ? t('status.copied') : '' }}</span>
    </div>
  </footer>
</template>

<style scoped>
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
.status-divider {
  width: 1px;
  height: 12px;
  background: rgba(0, 0, 0, 0.12);
  margin: 0 2px;
}

html[data-theme="dark"] .status-divider {
  background: rgba(255, 255, 255, 0.14);
}

.status-address {
  font-family: var(--font-mono);
  font-size: 10.5px;
  color: var(--muted);
  user-select: text;
}

.status-address.is-empty {
  color: var(--negative);
  opacity: 0.85;
}

.status-copy {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  padding: 0;
  margin-left: 2px;
  border: none;
  background: transparent;
  color: var(--muted);
  border-radius: 4px;
  cursor: pointer;
  transition: background-color 120ms ease, color 120ms ease;
}

.status-copy:hover {
  background: rgba(0, 0, 0, 0.06);
  color: var(--fg);
}

.status-copy:active {
  background: rgba(0, 0, 0, 0.1);
}

html[data-theme="dark"] .status-copy:hover {
  background: rgba(255, 255, 255, 0.08);
}

html[data-theme="dark"] .status-copy:active {
  background: rgba(255, 255, 255, 0.14);
}
</style>
