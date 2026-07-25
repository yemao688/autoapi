<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ToastItem } from '@/composables/useToast'

const props = defineProps<{
  toast: ToastItem
}>()

const { t } = useI18n()

const modelTest = computed(() => props.toast.modelTest!)

function formatElapsedSeconds(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 s'
  if (value < 60) return `${Math.round(value)} s`
  const minutes = Math.floor(value / 60)
  const seconds = Math.round(value % 60)
  return `${minutes}m ${String(seconds).padStart(2, '0')}s`
}

const icon = computed(() => {
  if (modelTest.value.status === 'success') return '✓'
  if (modelTest.value.status === 'error') return '✕'
  if (modelTest.value.status === 'cancelled') return '!'
  return ''
})

const stateClass = computed(() => `model-test-toast model-test-${modelTest.value.status}`)
</script>

<template>
  <div :class="stateClass" role="status">
    <div class="model-test-main">
      <div class="model-test-title-row">
        <div class="model-test-title">{{ modelTest.title }}</div>
        <div v-if="modelTest.status === 'running'" class="model-test-spinner" aria-hidden="true"></div>
        <div v-else class="model-test-icon" aria-hidden="true">{{ icon }}</div>
      </div>
      <div class="model-test-detail">
        <template v-if="modelTest.status === 'running'">
          {{ t('testModel.runningSeconds', { elapsed: formatElapsedSeconds(modelTest.elapsedSeconds) }) }}
        </template>
        <template v-else>
          <div v-if="modelTest.summary" class="model-test-summary">{{ modelTest.summary }}</div>
          <div class="model-test-body">{{ modelTest.detail }}</div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.model-test-toast {
  display: flex;
  align-items: stretch;
  min-width: 0;
}

.model-test-main {
  flex: 1;
  min-width: 0;
}

.model-test-title-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.model-test-title {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  font-weight: 600;
  color: var(--fg);
}

.model-test-detail {
  margin-top: 3px;
  font-size: 12px;
  line-height: 1.45;
  color: var(--muted);
  word-break: break-word;
}

.model-test-summary {
  color: var(--muted);
}

.model-test-body {
  margin-top: 5px;
  max-height: 168px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--fg);
  font-size: 12.5px;
  line-height: 1.5;
  padding-right: 4px;
}

.model-test-icon,
.model-test-spinner {
  width: 18px;
  height: 18px;
  flex: 0 0 18px;
}

.model-test-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  font-size: 11px;
  font-weight: 700;
  color: #fff;
}

.model-test-spinner {
  border: 2px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: model-test-spin 0.7s linear infinite;
}

.model-test-success .model-test-icon {
  background: var(--positive);
}

.model-test-error .model-test-icon {
  background: var(--negative);
}

.model-test-cancelled .model-test-icon {
  background: var(--warning);
}

@keyframes model-test-spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
