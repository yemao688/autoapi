// Master-password gate state and helpers.
//
// Flow:
//   1. App boot → check api.hasMasterPassword()
//   2. If false  → show "Set master password" overlay
//   3. If true   → show "Unlock" overlay until api.unlock(p) succeeds
//   4. The gate composable exposes reactive `state` ('init'|'set'|'unlock'|'ready')
//      and async `setPassword(p)` / `unlock(p)` / `isUnlocked` for views to use.

import { reactive, ref } from 'vue'
import { api } from '@/api/client'

export type GateState = 'init' | 'set' | 'unlock' | 'ready'

const state = ref<GateState>('init')
const error = ref<string>('')

async function init(): Promise<void> {
  if (state.value !== 'init') return
  try {
    const has = await api.hasMasterPassword()
    state.value = has ? 'unlock' : 'set'
  } catch (e) {
    error.value = String(e)
  }
}

async function setPassword(p: string): Promise<void> {
  error.value = ''
  if (p.length < 6) {
    error.value = '密码至少 6 位'
    return
  }
  try {
    await api.setMasterPassword(p)
    state.value = 'ready'
  } catch (e) {
    error.value = String(e)
  }
}

async function unlock(p: string): Promise<void> {
  error.value = ''
  try {
    await api.unlock(p)
    state.value = 'ready'
  } catch (e) {
    error.value = '密码错误'
  }
}

export function useMasterGate() {
  return { state, error, init, setPassword, unlock }
}
