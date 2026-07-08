<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useConfirm } from '@/composables/useConfirm'

const { state, resolve } = useConfirm()
const { t } = useI18n()

const titleId = computed(() => `confirm-dialog-title-${state.id}`)
const bodyId = computed(() => `confirm-dialog-body-${state.id}`)

function onCancel() {
  resolve(false)
}

function onConfirm() {
  resolve(true)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="confirm-fade">
      <div
        v-if="state.open"
        class="modal-overlay confirm-overlay"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="titleId"
        :aria-describedby="bodyId"
        @click.self="onCancel"
        @keydown.esc.stop="onCancel"
      >
        <div class="modal-card confirm-card">
          <div :id="titleId" class="modal-title">{{ state.title }}</div>
          <div :id="bodyId" class="confirm-body">{{ state.message }}</div>
          <div class="confirm-actions">
            <button
              type="button"
              class="btn btn-secondary"
              :disabled="state.busy"
              @click="onCancel"
            >
              {{ state.cancelText }}
            </button>
            <button
              type="button"
              class="btn"
              :class="state.danger ? 'btn-danger' : 'btn-primary'"
              :disabled="state.busy"
              @click="onConfirm"
            >
              {{ state.busy ? t('common.processing') : state.confirmText }}
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.confirm-overlay {
  z-index: 11000;
}

.confirm-card {
  width: 420px;
  max-width: 92vw;
}

.confirm-body {
  font-size: 13px;
  line-height: 1.55;
  color: var(--fg);
  margin-bottom: 20px;
  white-space: pre-wrap;
  word-break: break-word;
}

.confirm-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.btn-danger {
  background: var(--negative, #d93025);
  color: #fff;
}

.btn-danger:hover { background: #c1261c; }

.btn-danger:disabled,
.btn-secondary:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}

.confirm-fade-enter-active,
.confirm-fade-leave-active {
  transition: opacity 0.18s ease;
}

.confirm-fade-enter-from,
.confirm-fade-leave-to {
  opacity: 0;
}
</style>
