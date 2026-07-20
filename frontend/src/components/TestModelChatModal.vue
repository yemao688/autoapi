<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '../api/bridge'
import type { model } from '../../wailsjs/go/models'

const props = defineProps<{ open: boolean; providerId: string; providerName: string; modelName: string }>()
const emit = defineEmits<{ (e: 'close'): void }>()
const { t } = useI18n()

type TestState = 'idle' | 'running' | 'success' | 'error' | 'cancelled'
type Protocol = 'responses' | 'messages' | 'chat' | 'gemini'
const state = ref<TestState>('idle')
const protocol = ref<Protocol>('chat')
const streamMode = ref(true)
const result = ref<model.ModelChatTestResult | null>(null)
const error = ref<string | null>(null)
const activeTestId = ref('')
let generation = 0

async function runTest() {
  if (state.value === 'running') return
  const current = ++generation
  const testId = typeof crypto !== 'undefined' && crypto.randomUUID ? crypto.randomUUID() : `test-${Date.now()}`
  activeTestId.value = testId
  state.value = 'running'
  result.value = null
  error.value = null
  try {
    const response = await api.testModelChat(props.providerId, props.modelName, protocol.value, streamMode.value, testId)
    if (current !== generation) return
    if (response.ok) {
      result.value = response
      state.value = 'success'
    } else {
      error.value = response.error || t('testModel.error')
      state.value = 'error'
    }
  } catch (e: any) {
    if (current !== generation) return
    error.value = e?.message || String(e)
    state.value = 'error'
  } finally {
    if (current === generation) activeTestId.value = ''
  }
}

async function stopTest() {
  if (state.value !== 'running') return
  const testId = activeTestId.value
  generation++
  activeTestId.value = ''
  state.value = 'cancelled'
  if (testId) {
    try { await api.cancelModelTest(testId) } catch { /* local cancellation remains authoritative */ }
  }
}

watch(() => props.open, (open) => {
  generation++
  if (open) {
    state.value = 'idle'
    protocol.value = 'chat'
    streamMode.value = true
    result.value = null
    error.value = null
    activeTestId.value = ''
  } else {
    const testId = activeTestId.value
    if (state.value === 'running' && testId) {
      void api.cancelModelTest(testId).catch(() => { /* closing is still local and authoritative */ })
    }
    state.value = 'idle'
    activeTestId.value = ''
  }
}, { immediate: true })
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-overlay" @click.self="emit('close')">
      <div class="modal-card test-modal" role="dialog" aria-modal="true">
        <div class="modal-title">
          <div class="row-between" style="align-items: flex-start;">
            <div><div>{{ t('testModel.title') }}</div><div class="text-muted test-model-name">{{ modelName }}</div></div>
            <span v-if="result?.latency_ms" class="badge mono">{{ result.latency_ms }}ms</span>
            <span v-else-if="state === 'running'" class="badge mono">—</span>
          </div>
        </div>
        <div class="test-modal-body">
          <div class="field"><label class="field-label">{{ t('testModel.prompt') }}</label><div class="test-prompt">hi</div></div>
          <div class="test-controls">
            <label class="stream-control"><input v-model="streamMode" type="checkbox" :disabled="state === 'running'"><span>{{ t('testModel.stream') }}</span></label>
            <div class="field protocol-field"><label class="field-label">{{ t('testModel.protocol') }}</label><select v-model="protocol" class="select" :disabled="state === 'running'"><option value="responses">{{ t('testModel.responses') }}</option><option value="messages">{{ t('testModel.messages') }}</option><option value="chat">{{ t('testModel.chat') }}</option><option value="gemini">{{ t('testModel.gemini') }}</option></select></div>
          </div>
          <div v-if="state === 'running'" class="test-state"><span class="spinner"></span><span>{{ t('testModel.sending') }}</span></div>
          <div v-else-if="state === 'error'" class="test-state test-error"><div class="field-label">{{ t('testModel.error') }}</div><div class="test-error-message">{{ error }}</div></div>
          <div v-else-if="state === 'cancelled'" class="test-state">{{ t('testModel.cancelled') }}</div>
          <div v-else-if="result" class="field" style="margin-bottom: 0;"><div class="row-between"><label class="field-label">{{ t('testModel.response') }}</label><div class="row" style="gap: 6px;"><span v-if="result.finish_reason" class="badge mono">{{ result.finish_reason }}</span><span v-if="result.latency_ms" class="badge mono">{{ result.latency_ms }}ms</span></div></div><pre class="test-response">{{ result.response }}</pre></div>
        </div>
        <div class="test-modal-footer row"><button class="btn btn-primary" @click="state === 'running' ? stopTest() : runTest()">{{ state === 'running' ? t('testModel.stop') : (state === 'idle' ? t('testModel.start') : t('testModel.retry')) }}</button><button class="btn btn-secondary" @click="emit('close')">{{ t('testModel.close') }}</button></div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.test-modal { display: flex; flex-direction: column; }
.test-modal-body { min-height: 0; overflow-y: auto; }
.test-model-name { font-size: 12px; font-weight: 400; margin-top: 2px; }
.test-prompt,.test-response { font-family: var(--font-mono); font-size: 13px; background: var(--bg); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 8px 10px; color: var(--fg); }
.test-controls { display: flex; align-items: flex-end; gap: 16px; margin-bottom: 16px; }
.stream-control { display: flex; align-items: center; gap: 8px; min-height: 36px; font-size: 13px; color: var(--fg); }
.protocol-field { flex: 1; margin-bottom: 0; }
.test-state { display: flex; align-items: center; gap: 10px; padding: 12px; border-radius: var(--radius-sm); font-size: 13px; color: var(--muted); }
.test-error { flex-direction: column; align-items: flex-start; gap: 6px; background: rgba(217,48,37,.06); border: 1px solid rgba(217,48,37,.12); color: var(--negative); }
.test-error-message { font-size: 12.5px; line-height: 1.45; word-break: break-word; white-space: pre-wrap; }
.test-response { line-height: 1.5; white-space: pre-wrap; word-break: break-word; max-height: 240px; overflow: auto; margin: 0; }
.test-modal-footer { justify-content: flex-end; gap: 8px; margin-top: 20px; flex-shrink: 0; }
.spinner { width: 16px; height: 16px; border: 2px solid var(--border); border-top-color: var(--accent); border-radius: 50%; animation: spin .7s linear infinite; flex-shrink: 0; }
@keyframes spin { to { transform: rotate(360deg); } }
@media (max-height: 520px) { .test-modal { padding: 16px; } .test-response { max-height: 120px; } }
</style>
