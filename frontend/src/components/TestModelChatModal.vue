<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '../api/client'
import type { model } from '../../wailsjs/go/models'

const props = defineProps<{
  open: boolean
  providerId: string
  providerName: string
  modelName: string
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const { t } = useI18n()

const loading = ref(false)
const result = ref<model.ModelChatTestResult | null>(null)
const error = ref<string | null>(null)

// Monotonic generation token so a stale response from a closed/reopened
// modal can't overwrite the current state.
let testGeneration = 0

async function runTest() {
  const gen = ++testGeneration
  loading.value = true
  result.value = null
  error.value = null
  try {
    const res = await api.testModelChat(props.providerId, props.modelName)
    if (gen !== testGeneration) return // stale — a newer test superseded this one
    if (res.ok) {
      result.value = res
    } else {
      error.value = res.error || t('testModel.error')
    }
  } catch (e: any) {
    if (gen !== testGeneration) return
    error.value = e?.message || String(e)
  } finally {
    if (gen === testGeneration) {
      loading.value = false
    }
  }
}

watch(
  () => props.open,
  (open) => {
    if (open) {
      runTest()
    } else {
      // Invalidate any in-flight response so it can't land after close.
      testGeneration++
      loading.value = false
      result.value = null
      error.value = null
    }
  },
  { immediate: true }
)
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-overlay" @click.self="emit('close')">
      <div class="modal-card">
        <div class="modal-title">
          <div class="row-between" style="align-items: flex-start;">
            <div>
              <div>{{ t('testModel.title') }}</div>
              <div class="text-muted" style="font-size: 12px; font-weight: 400; margin-top: 2px;">
                {{ modelName }}
              </div>
            </div>
            <span
              v-if="result?.latency_ms"
              class="badge mono"
              style="flex-shrink: 0;"
            >{{ result.latency_ms }}ms</span>
            <span
              v-else-if="loading"
              class="badge mono"
              style="flex-shrink: 0;"
            >—</span>
          </div>
        </div>

        <div class="field">
          <label class="field-label">{{ t('testModel.prompt') }}</label>
          <div class="test-prompt">hi</div>
        </div>

        <div v-if="loading" class="test-state">
          <span class="spinner"></span>
          <span>{{ t('testModel.sending') }}</span>
        </div>

        <div v-else-if="error" class="test-state test-error">
          <div class="field-label" style="color: var(--negative);">{{ t('testModel.error') }}</div>
          <div class="test-error-message">{{ error }}</div>
        </div>

        <div v-else-if="result" class="field" style="margin-bottom: 0;">
          <div class="row-between">
            <label class="field-label">{{ t('testModel.response') }}</label>
            <span v-if="result.latency_ms" class="badge mono">{{ result.latency_ms }}ms</span>
          </div>
          <pre class="test-response">{{ result.response }}</pre>
        </div>

        <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 20px;">
          <button class="btn btn-secondary" @click="emit('close')">
            {{ t('testModel.close') }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.test-prompt {
  font-family: var(--font-mono);
  font-size: 13px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  padding: 8px 10px;
  color: var(--fg);
}

.test-state {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px;
  border-radius: var(--radius-sm);
  font-size: 13px;
  color: var(--muted);
}

.test-error {
  flex-direction: column;
  align-items: flex-start;
  gap: 6px;
  background: rgba(217, 48, 37, 0.06);
  border: 1px solid rgba(217, 48, 37, 0.12);
  color: var(--negative);
}

.test-error-message {
  font-size: 12.5px;
  line-height: 1.45;
  word-break: break-word;
  white-space: pre-wrap;
}

.test-response {
  font-family: var(--font-mono);
  font-size: 12.5px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  padding: 10px 12px;
  max-height: 240px;
  overflow: auto;
  color: var(--fg);
  margin: 0;
}

.spinner {
  width: 16px;
  height: 16px;
  border: 2px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spin 0.7s linear infinite;
  flex-shrink: 0;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}
</style>
