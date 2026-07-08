<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useToast } from '@/composables/useToast'

const { toasts, remove } = useToast()
const { t } = useI18n()

function iconFor(type: string) {
  if (type === 'success') return '✓'
  if (type === 'error') return '✕'
  if (type === 'warning') return '!'
  return 'i'
}

function toastClass(type: string) {
  return `toast toast-${type}`
}
</script>

<template>
  <div class="toast-container" aria-live="polite" aria-atomic="true">
    <TransitionGroup name="toast">
      <div
        v-for="toast in toasts"
        :key="toast.id"
        :class="toastClass(toast.type)"
        role="status"
      >
        <span class="toast-icon">{{ iconFor(toast.type) }}</span>
        <span class="toast-message">{{ toast.message }}</span>
        <button class="toast-close" :aria-label="t('common.close')" @click="remove(toast.id)">×</button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.toast-container {
  position: fixed;
  top: 16px;
  right: 16px;
  z-index: 10000;
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-width: 360px;
  pointer-events: none;
}

.toast {
  pointer-events: auto;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 11px 14px;
  border-radius: var(--radius-md, 12px);
  background: var(--surface, #ffffff);
  color: var(--fg, #1d1d1f);
  box-shadow: var(--shadow-lg, 0 12px 32px rgba(0, 0, 0, 0.12));
  border: 1px solid var(--border, #d2d2d7);
  font-size: 13px;
  font-weight: 500;
  animation: toast-in 0.22s ease;
}

.toast-icon {
  width: 20px;
  height: 20px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  font-weight: 700;
  flex-shrink: 0;
  color: #fff;
}

.toast-success { border-color: rgba(40, 167, 69, 0.35); }
.toast-success .toast-icon { background: var(--positive, #28a745); }

.toast-error { border-color: rgba(217, 48, 37, 0.35); }
.toast-error .toast-icon { background: var(--negative, #d93025); }

.toast-warning { border-color: rgba(245, 166, 35, 0.4); }
.toast-warning .toast-icon { background: var(--warning, #f5a623); }

.toast-info { border-color: rgba(0, 113, 227, 0.3); }
.toast-info .toast-icon { background: var(--accent, #0071e3); }

.toast-message {
  flex: 1;
  line-height: 1.4;
}

.toast-close {
  width: 20px;
  height: 20px;
  border-radius: 50%;
  border: none;
  background: transparent;
  color: var(--muted, #6e6e73);
  font-size: 16px;
  line-height: 1;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: background 0.12s ease, color 0.12s ease;
}

.toast-close:hover {
  background: rgba(0, 0, 0, 0.06);
  color: var(--fg, #1d1d1f);
}

.toast-enter-active,
.toast-leave-active {
  transition: all 0.22s ease;
}

.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateX(16px);
}

@keyframes toast-in {
  from {
    opacity: 0;
    transform: translateX(16px) scale(0.98);
  }
  to {
    opacity: 1;
    transform: translateX(0) scale(1);
  }
}
</style>
