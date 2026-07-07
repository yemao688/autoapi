<script setup lang="ts">
// Master-password gate overlay. Shown until the user has either set or
// unlocked the master password. Renders a modal-like card centered over the
// app shell (which is dimmed behind it).

import { useMasterGate } from '@/composables/useMasterGate'
import { onMounted, ref } from 'vue'

const { state, error, init, setPassword, unlock } = useMasterGate()
const password = ref('')
const busy = ref(false)

onMounted(() => {
  void init()
})

async function submit() {
  if (busy.value || !password.value) return
  busy.value = true
  try {
    if (state.value === 'set') {
      await setPassword(password.value)
    } else if (state.value === 'unlock') {
      await unlock(password.value)
    }
    if (state.value === 'ready') password.value = ''
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <div v-if="state !== 'ready'" class="gate">
      <div class="gate-card">
        <div class="gate-logo">A</div>
        <h1 class="gate-title">
          {{ state === 'set' ? '设置主密码' : '解锁 autoapi' }}
        </h1>
        <p class="gate-sub">
          {{
            state === 'set'
              ? '用于在本地加密 Provider API 密钥。忘记后将无法恢复。'
              : '请输入主密码以使用 autoapi。'
          }}
        </p>
        <form class="gate-form" @submit.prevent="submit">
          <input
            v-model="password"
            class="gate-input"
            type="password"
            :placeholder="state === 'set' ? '至少 6 位' : '主密码'"
            autofocus
            :disabled="busy"
          />
          <button class="gate-btn" type="submit" :disabled="busy || !password">
            {{ state === 'set' ? '设置并解锁' : '解锁' }}
          </button>
        </form>
        <div v-if="error" class="gate-error">{{ error }}</div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.gate {
  position: fixed;
  inset: 0;
  z-index: 9999;
  background: rgba(245, 245, 247, 0.92);
  backdrop-filter: blur(12px);
  -webkit-backdrop-filter: blur(12px);
  display: flex;
  align-items: center;
  justify-content: center;
}
.gate-card {
  width: 360px;
  padding: 32px 28px 24px;
  background: #fff;
  border: 1px solid var(--border, #d2d2d7);
  border-radius: 14px;
  box-shadow: 0 30px 80px rgba(0, 0, 0, 0.18), 0 8px 20px rgba(0, 0, 0, 0.08);
  text-align: center;
  font: 14px/1.5 -apple-system, "SF Pro Text", sans-serif;
  color: #1d1d1f;
}
.gate-logo {
  width: 48px;
  height: 48px;
  margin: 0 auto 16px;
  background: linear-gradient(180deg, #2c2c2e, #1d1d1f);
  border-radius: 12px;
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font: 600 22px "SF Pro Display", sans-serif;
}
.gate-title {
  margin: 0 0 6px;
  font: 600 20px/1.3 "SF Pro Display", sans-serif;
  letter-spacing: -0.01em;
}
.gate-sub {
  margin: 0 0 20px;
  font-size: 13px;
  color: #6e6e73;
}
.gate-form {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.gate-input {
  height: 36px;
  padding: 0 12px;
  border: 1px solid #d2d2d7;
  border-radius: 8px;
  background: #fff;
  font: 14px -apple-system, "SF Pro Text", sans-serif;
  color: #1d1d1f;
  outline: none;
  transition: border-color 0.12s, box-shadow 0.12s;
}
.gate-input:focus {
  border-color: #0071e3;
  box-shadow: 0 0 0 3px rgba(0, 113, 227, 0.15);
}
.gate-btn {
  height: 36px;
  border: none;
  border-radius: 8px;
  background: #0071e3;
  color: #fff;
  font: 600 14px -apple-system, "SF Pro Text", sans-serif;
  cursor: pointer;
  transition: background 0.12s, opacity 0.12s;
}
.gate-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.gate-btn:not(:disabled):hover {
  background: #0077ed;
}
.gate-error {
  margin-top: 12px;
  font-size: 12px;
  color: #d93025;
}
</style>
