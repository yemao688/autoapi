<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/bridge'
import { useConfirm } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import { toolconfig } from '../../wailsjs/go/models'
import type { service } from '../../wailsjs/go/models'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: []; applied: [] }>()
const { t } = useI18n()
const toast = useToast()
const confirm = useConfirm()
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const path = ref('')
const activePreset = ref('')
const originalActivePreset = ref('')
const knownPresets = ref<string[]>([])
const validModels = ref<string[]>([])
const variants = ref<string[]>([])
const agents = ref<Record<string, { model: string; variant: string }>>({})
const dirtyAgents = ref<Set<string>>(new Set())
const disabledAgents = ref<string[]>([])

const configuredAgentNames = computed(() => Object.keys(agents.value).sort())
const agentNames = computed(() => [...new Set([...Object.keys(agents.value), ...disabledAgents.value])].sort())
const disabledSet = computed(() => new Set(disabledAgents.value))

function driftMessage(states: service.DriftState[]) {
  const details = states.length
    ? states.map((state) => `${state.Resource}: ${state.Missing ? t('toolAccess.drift.missing') : state.Drifted ? t('toolAccess.drift.changed') : t('toolAccess.drift.unchanged')}\n${state.Path}`).join('\n\n')
    : t('toolAccess.drift.none')
  return `${t('toolAccess.drift.confirmMessage')}\n\n${details}`
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const config = await api.getOmoConfig()
    path.value = config.Path || ''
    activePreset.value = config.ActivePreset || ''
    originalActivePreset.value = activePreset.value
    knownPresets.value = config.KnownPresets || []
    validModels.value = config.ValidModels || []
    variants.value = config.AvailableVariants || []
    agents.value = Object.fromEntries(Object.entries(config.Agents || {}).map(([name, agent]) => [name, { model: agent.Model || '', variant: agent.Variant || '' }]))
    disabledAgents.value = [...(config.DisabledAgents || [])]
    dirtyAgents.value = new Set()
  } catch (e: any) {
    error.value = e?.message || String(e)
  } finally {
    loading.value = false
  }
}

function markAgentDirty(name: string) {
  dirtyAgents.value = new Set(dirtyAgents.value).add(name)
}

function toggleDisabled(name: string) {
  const set = new Set(disabledAgents.value)
  if (set.has(name)) set.delete(name)
  else set.add(name)
  disabledAgents.value = [...set]
}

async function apply(allowDrift = false) {
  if ((saving.value && !allowDrift) || loading.value) return
  saving.value = true
  error.value = ''
  try {
    const changedAgents = Object.fromEntries([...dirtyAgents.value].map((name) => [name, { Model: agents.value[name]?.model || '', Variant: agents.value[name]?.variant || '' }]))
    const change = toolconfig.OmoChange.createFrom({
      ActivePreset: activePreset.value !== originalActivePreset.value ? activePreset.value : undefined,
      Agents: changedAgents,
      DisabledAgents: [...disabledAgents.value],
    })
    await api.applyOmoConfig(change, allowDrift)
    toast.push(t('toolAccess.toast.omoApplied'), 'success')
    await load()
    emit('applied')
  } catch (e: any) {
    const message = e?.message || String(e)
    if (!allowDrift && message.includes('config file changed externally since last apply')) {
      try {
        const states = await api.checkToolDrift('opencode')
        const ok = await confirm.open({
          title: t('toolAccess.drift.confirmTitle'),
          message: driftMessage(states),
          confirmText: t('toolAccess.drift.confirm'),
          danger: true,
        })
        if (ok) await apply(true)
      } catch (driftError: any) {
        error.value = driftError?.message || String(driftError)
      }
    } else {
      error.value = message
    }
  } finally {
    saving.value = false
  }
}

watch(() => props.open, (open) => {
  if (open) void load()
})
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-overlay" @click.self="emit('close')">
      <div class="modal-card wide modal-card-scroll omo-modal">
        <div class="row-between modal-heading">
          <div>
            <div class="modal-title">{{ t('toolAccess.omo.title') }}</div>
            <div class="section-sub text-mono">{{ path || t('toolAccess.omo.pathUnavailable') }}</div>
          </div>
          <button class="btn btn-icon" :title="t('common.close')" :aria-label="t('common.close')" @click="emit('close')">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>
          </button>
        </div>

        <div v-if="loading" class="text-muted omo-state">{{ t('toolAccess.omo.loading') }}</div>
        <div v-else-if="error" class="tool-inline-error" role="alert">
          <strong>{{ t('toolAccess.omo.loadFailed') }}</strong>
          <span>{{ error }}</span>
          <button class="btn btn-secondary" @click="load">{{ t('toolAccess.omo.retry') }}</button>
        </div>
        <template v-else>
          <div class="field">
            <label class="field-label">{{ t('toolAccess.omo.activePreset') }}</label>
            <select v-model="activePreset" class="select">
              <option v-for="preset in knownPresets" :key="preset" :value="preset">{{ preset }}</option>
            </select>
            <div class="field-help">{{ t('toolAccess.omo.activePresetHelp') }}</div>
          </div>

          <div class="field">
            <div class="row-between">
              <label class="field-label">{{ t('toolAccess.omo.agents') }}</label>
              <span class="text-muted" style="font-size: 11px;">{{ t('toolAccess.omo.agentCount', { count: configuredAgentNames.length }) }}</span>
            </div>
            <div class="tbl-wrap omo-table-wrap">
              <table class="tbl omo-table">
                <thead><tr><th>{{ t('toolAccess.omo.agent') }}</th><th>{{ t('toolAccess.omo.model') }}</th><th>{{ t('toolAccess.omo.variant') }}</th></tr></thead>
                <tbody>
                  <tr v-for="agentName in configuredAgentNames" :key="agentName">
                    <td class="text-mono">{{ agentName }}</td>
                    <td><input v-model="agents[agentName].model" class="input mono" :list="`omo-models-${agentName}`" @input="markAgentDirty(agentName)"><datalist :id="`omo-models-${agentName}`"><option v-for="model in validModels" :key="model" :value="model" /></datalist></td>
                    <td><select v-model="agents[agentName].variant" class="select" @change="markAgentDirty(agentName)"><option value="">{{ t('toolAccess.omo.variantNone') }}</option><option v-for="variant in variants" :key="variant" :value="variant">{{ variant }}</option></select></td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <div class="field">
            <label class="field-label">{{ t('toolAccess.omo.disabledAgents') }}</label>
            <div class="omo-agent-tags">
              <label v-for="agentName in agentNames" :key="agentName" class="check-label omo-agent-tag"><input type="checkbox" :checked="disabledSet.has(agentName)" @change="toggleDisabled(agentName)">{{ agentName }}</label>
            </div>
            <div class="field-help">{{ t('toolAccess.omo.disabledHelp') }}</div>
          </div>

          <div class="row" style="justify-content: flex-end; gap: 8px; margin-top: 4px;">
            <button class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
            <button class="btn btn-primary" :disabled="saving" @click="apply()">{{ saving ? t('common.processing') : t('common.save') }}</button>
          </div>
        </template>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.omo-modal { max-width: 760px; }
.modal-heading { align-items: flex-start; margin-bottom: 16px; }
.omo-state { padding: 28px 0; text-align: center; }
.omo-table-wrap { border: 1px solid var(--border); border-radius: var(--radius-sm); }
.omo-table { min-width: 620px; }
.omo-table td { padding: 8px 10px; }
.omo-table .input, .omo-table .select { min-height: 31px; font-size: 12px; }
.omo-agent-tags { display: flex; flex-wrap: wrap; gap: 6px; }
.omo-agent-tag { padding: 5px 8px; border: 1px solid var(--border); border-radius: var(--radius-pill); background: var(--surface); }
.omo-agent-tag:has(input:checked) { border-color: var(--accent); background: var(--accent-soft); color: var(--accent); }
.tool-inline-error { display: flex; flex-direction: column; gap: 8px; padding: 12px; border-radius: var(--radius-sm); background: rgba(217, 48, 37, 0.08); color: var(--negative); font-size: 12px; }
.tool-inline-error .btn { align-self: flex-start; color: var(--fg); }
</style>
