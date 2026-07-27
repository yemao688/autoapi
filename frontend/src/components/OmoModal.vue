<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/bridge'
import AutoComplete from '@/components/AutoComplete.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import { toolconfig } from '../../wailsjs/go/models'
import type { service } from '../../wailsjs/go/models'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: []; applied: [] }>()
const { t } = useI18n()
const toast = useToast()
const confirm = useConfirm()

type AgentDraft = { model: string; variant: string }
type AgentProjection = { Model?: string; Variant?: string }
type PresetAgentProjection = Record<string, Record<string, AgentProjection>>

const loading = ref(false)
const saving = ref(false)
const error = ref('')
const path = ref('')
const activePreset = ref('')
const originalActivePreset = ref('')
const knownPresets = ref<string[]>([])
const validModels = ref<string[]>([])
const variants = ref<string[]>([])
const agents = ref<Record<string, AgentDraft>>({})
const presetAgents = ref<PresetAgentProjection>({})
const dirtyAgents = ref<Set<string>>(new Set())
const disabledAgents = ref<string[]>([])

const configuredAgentNames = computed(() => Object.keys(agents.value).sort())
const disabledSet = computed(() => new Set(disabledAgents.value))
const otherDisabledAgents = computed(() => disabledAgents.value.filter((name) => !Object.prototype.hasOwnProperty.call(agents.value, name)))
const currentProjection = computed(() => presetAgents.value[originalActivePreset.value] || {})
const presetSwitchPending = computed(() => activePreset.value !== originalActivePreset.value)
const selectedProjection = computed(() => presetAgents.value[activePreset.value])
const previewRows = computed(() => Object.entries(selectedProjection.value || {}).sort(([a], [b]) => a.localeCompare(b)))

const agentGroups = computed(() => {
  // Group by the CURRENTLY active preset on disk, not the pending dropdown
  // selection — otherwise rows would jump between groups before saving.
  const builtIn = new Set(Object.keys(currentProjection.value))
  return [
    {
      key: 'builtIn',
      titleKey: 'toolAccess.omo.builtInAgents',
      names: configuredAgentNames.value.filter((name) => builtIn.has(name)),
    },
    {
      key: 'custom',
      titleKey: 'toolAccess.omo.customAgents',
      names: configuredAgentNames.value.filter((name) => !builtIn.has(name)),
    },
  ]
})

const describedAgents = new Set(['orchestrator', 'oracle', 'librarian', 'explorer', 'designer', 'fixer', 'observer', 'council'])

function agentDescription(name: string) {
  return describedAgents.has(name) ? t(`toolAccess.omo.agentDesc.${name}`) : ''
}

function projectionValue(agent: AgentProjection | undefined) {
  return `${agent?.Model || ''}\u0000${agent?.Variant || ''}`
}

function previewClass(agentName: string, agent: AgentProjection) {
  return projectionValue(agent) === projectionValue(currentProjection.value[agentName]) ? 'same' : 'changed'
}

function previewText(agent: AgentProjection) {
  const model = agent.Model || t('toolAccess.omo.modelUnset')
  return `${model}${agent.Variant ? ` · ${agent.Variant}` : ''}`
}

function isModelUnknown(model: string) {
  return !!model.trim() && validModels.value.length > 0 && !validModels.value.includes(model.trim())
}

function isAgentDisabled(name: string) {
  return disabledSet.value.has(name)
}

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
    presetAgents.value = (config.PresetAgents || {}) as PresetAgentProjection
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
  const next = new Set(disabledAgents.value)
  if (next.has(name)) next.delete(name)
  else next.add(name)
  disabledAgents.value = [...next]
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

          <div v-if="presetSwitchPending" class="omo-preview">
            <div class="row-between omo-preview-heading">
              <div>
                <div class="field-label">{{ t('toolAccess.omo.switchPreview') }}</div>
                <div class="field-help">{{ t('toolAccess.omo.switchPreviewHelp', { preset: activePreset }) }}</div>
              </div>
              <span class="badge info">{{ t('toolAccess.omo.previewOnly') }}</span>
            </div>
            <div v-if="previewRows.length" class="omo-preview-list">
              <div v-for="([agentName, agent]) in previewRows" :key="agentName" class="omo-preview-row" :class="previewClass(agentName, agent)">
                <span class="text-mono">{{ agentName }}</span>
                <span class="text-mono">{{ previewText(agent) }}</span>
              </div>
            </div>
            <div v-else class="field-help omo-no-projection">{{ t('toolAccess.omo.noProjection') }}</div>
          </div>

          <div class="field">
            <div class="row-between omo-section-heading">
              <div>
                <label class="field-label">{{ t('toolAccess.omo.agents') }}</label>
                <div class="field-help">{{ t('toolAccess.omo.agentHelp') }}</div>
              </div>
              <span class="text-muted" style="font-size: 11px;">{{ t('toolAccess.omo.agentCount', { count: configuredAgentNames.length }) }}</span>
            </div>

            <div class="omo-agent-groups">
              <section v-for="group in agentGroups" v-show="group.names.length" :key="group.key" class="omo-agent-group">
                <div class="omo-group-title"><span>{{ t(group.titleKey) }}</span><span class="text-mono">{{ group.names.length }}</span></div>
                <div class="tbl-wrap omo-table-wrap">
                  <table class="tbl omo-table">
                    <thead><tr><th>{{ t('toolAccess.omo.agent') }}</th><th>{{ t('toolAccess.omo.model') }}</th><th>{{ t('toolAccess.omo.variant') }}</th><th class="right">{{ t('toolAccess.omo.enabled') }}</th></tr></thead>
                    <tbody>
                      <tr v-for="agentName in group.names" :key="agentName" :class="{ 'omo-agent-disabled': isAgentDisabled(agentName) }">
                        <td>
                          <div class="omo-agent-name" :class="{ disabled: isAgentDisabled(agentName) }">{{ agentName }}</div>
                          <div v-if="agentDescription(agentName)" class="omo-agent-description">{{ agentDescription(agentName) }}</div>
                        </td>
                        <td>
                          <AutoComplete v-model="agents[agentName].model" class="omo-model-complete" :options="validModels" :placeholder="t('toolAccess.omo.modelPlaceholder')" @update:model-value="markAgentDirty(agentName)" />
                          <div v-if="isModelUnknown(agents[agentName].model)" class="omo-model-warning">{{ t('toolAccess.omo.modelUnknown') }}</div>
                        </td>
                        <td><select v-model="agents[agentName].variant" class="select" @change="markAgentDirty(agentName)"><option value="">{{ t('toolAccess.omo.variantNone') }}</option><option v-for="variant in variants" :key="variant" :value="variant">{{ variant }}</option></select></td>
                        <td class="right omo-agent-toggle"><label class="toggle toggle-sm" :aria-label="t(isAgentDisabled(agentName) ? 'toolAccess.omo.enableAgent' : 'toolAccess.omo.disableAgent', { name: agentName })"><input type="checkbox" :checked="!isAgentDisabled(agentName)" @change="toggleDisabled(agentName)"><span class="toggle-slider blue"/></label></td>
                      </tr>
                    </tbody>
                  </table>
                </div>
              </section>
            </div>

            <div v-if="otherDisabledAgents.length" class="omo-other-disabled">
              <div class="omo-group-title"><span>{{ t('toolAccess.omo.otherDisabled') }}</span><span class="text-mono">{{ otherDisabledAgents.length }}</span></div>
              <div class="omo-disabled-chips">
                <button v-for="agentName in otherDisabledAgents" :key="agentName" class="omo-disabled-chip" :title="t('toolAccess.omo.reenableAgent', { name: agentName })" @click="toggleDisabled(agentName)">
                  <span>{{ agentName }}</span><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>
                </button>
              </div>
            </div>
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
.omo-modal { max-width: 860px; }
.modal-heading { align-items: flex-start; margin-bottom: 16px; }
.omo-state { padding: 28px 0; text-align: center; }
.omo-preview { margin: -4px 0 18px; padding: 11px 12px; border: 1px solid color-mix(in srgb, var(--accent) 35%, var(--border)); border-radius: var(--radius-sm); background: color-mix(in srgb, var(--accent-soft) 48%, var(--surface)); }
.omo-preview-heading { align-items: flex-start; gap: 12px; }
.omo-preview-list { display: flex; flex-direction: column; gap: 3px; margin-top: 10px; }
.omo-preview-row { display: flex; justify-content: space-between; gap: 12px; padding: 6px 8px; border-radius: var(--radius-xs); color: var(--muted); font-size: 11.5px; }
.omo-preview-row span:last-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.omo-preview-row.changed { background: color-mix(in srgb, var(--warning) 12%, transparent); color: var(--fg); }
.omo-preview-row.same { opacity: .7; }
.omo-no-projection { padding: 12px 0 2px; }
.omo-section-heading { align-items: flex-start; margin-bottom: 9px; }
.omo-agent-groups { display: flex; flex-direction: column; gap: 13px; }
.omo-group-title { display: flex; align-items: center; justify-content: space-between; margin: 0 0 5px; color: var(--muted); font-size: 11px; font-weight: 600; }
.omo-table-wrap { border: 1px solid var(--border); border-radius: var(--radius-sm); }
.omo-table { min-width: 760px; }
.omo-table td { padding: 8px 10px; vertical-align: middle; }
.omo-table .input, .omo-table .select { min-height: 31px; font-size: 12px; }
.omo-model-complete { min-width: 180px; }
.omo-model-complete :deep(.input) { font-family: var(--font-mono); font-size: 12px; }
.omo-agent-name { font-family: var(--font-mono); font-size: 12px; }
.omo-agent-name.disabled { color: var(--muted); text-decoration: line-through; }
.omo-agent-description { max-width: 220px; margin-top: 2px; color: var(--muted); font-size: 10.5px; line-height: 1.35; }
.omo-agent-disabled td { background: color-mix(in srgb, var(--bg) 48%, transparent); }
.omo-agent-disabled .omo-model-complete, .omo-agent-disabled .select { opacity: .7; }
.omo-model-warning { margin-top: 3px; color: var(--warning); font-size: 10.5px; }
.omo-agent-toggle { width: 70px; }
.omo-other-disabled { margin-top: 14px; }
.omo-disabled-chips { display: flex; flex-wrap: wrap; gap: 5px; }
.omo-disabled-chip { display: inline-flex; align-items: center; gap: 5px; min-height: 27px; padding: 4px 8px; border: 1px solid var(--border); border-radius: var(--radius-pill); background: var(--surface); color: var(--muted); font: 11px var(--font-mono); }
.omo-disabled-chip:hover { border-color: var(--accent); color: var(--accent); }
.omo-disabled-chip svg { width: 12px; height: 12px; }
.tool-inline-error { display: flex; flex-direction: column; gap: 8px; padding: 12px; border-radius: var(--radius-sm); background: rgba(217, 48, 37, 0.08); color: var(--negative); font-size: 12px; }
.tool-inline-error .btn { align-self: flex-start; color: var(--fg); }
@media (max-width: 720px) { .omo-table { min-width: 700px; } }
</style>
