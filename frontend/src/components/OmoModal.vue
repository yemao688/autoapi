<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/bridge'
import AutoComplete from '@/components/AutoComplete.vue'
import TriStateTagEditor from '@/components/omo/TriStateTagEditor.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import { toolconfig } from '../../wailsjs/go/models'
import type { service } from '../../wailsjs/go/models'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: []; applied: [] }>()
const { t } = useI18n()
const toast = useToast()
const confirm = useConfirm()

type TriMode = 'inherit' | 'all' | 'custom'
type TriDraft = { mode: TriMode; items: string[] }
type AgentDraft = {
  model: string
  variant: string
  displayName: string
  skills: TriDraft
  mcps: TriDraft
}
type CustomDraft = AgentDraft & {
  name: string
  isNew: boolean
  prompt: string
  orchestratorPrompt: string
}
type ProjectionAgent = { model?: string; variant?: string; Model?: string; Variant?: string }
type Projection = Record<string, Record<string, ProjectionAgent>>
type Section = 'agent' | 'custom' | 'global'
type GlobalKind = 'agents' | 'skills' | 'mcps'

const builtInNames = ['orchestrator', 'oracle', 'librarian', 'explorer', 'designer', 'fixer', 'observer', 'council']

const loading = ref(false)
const previewLoading = ref(false)
const saving = ref(false)
const error = ref('')
const path = ref('')
const activePreset = ref('')
const originalActivePreset = ref('')
const knownPresets = ref<string[]>([])
const validModels = ref<string[]>([])
const variants = ref<string[]>([])
const knownSkills = ref<string[]>([])
const knownMcps = ref<string[]>([])
const agents = ref<Record<string, AgentDraft>>({})
const customAgents = ref<CustomDraft[]>([])
const presetAgents = ref<Projection>({})
const disabledAgents = ref<string[]>([])
const disabledSkills = ref<string[]>([])
const disabledMcps = ref<string[]>([])
const dirtyAgents = ref<Set<string>>(new Set())
const customDirty = ref(false)
const dirtyGlobals = ref<Set<GlobalKind>>(new Set())
const selectedSection = ref<Section>('agent')
const selectedAgent = ref('orchestrator')
const selectedCustomIndex = ref(-1)
const globalInputs = ref<Record<GlobalKind, string>>({ agents: '', skills: '', mcps: '' })
const previewOpen = ref(false)
const previewData = ref<service.OmoPreview | null>(null)
const pendingChange = ref<toolconfig.OmoChange | null>(null)

const builtInAgentNames = computed(() => builtInNames.filter((name) => Object.prototype.hasOwnProperty.call(agents.value, name)))
const customAgentNames = computed(() => customAgents.value.map((agent) => agent.name).filter(Boolean))
const allAgentNames = computed(() => [...new Set([...builtInNames, ...customAgentNames.value])].sort((a, b) => a.localeCompare(b)))
const disabledSet = computed(() => new Set(disabledAgents.value))
const otherDisabledAgents = computed(() => disabledAgents.value.filter((name) => !Object.prototype.hasOwnProperty.call(agents.value, name)))
const currentProjection = computed(() => presetAgents.value[originalActivePreset.value] || {})
const selectedProjection = computed(() => presetAgents.value[activePreset.value])
const presetSwitchPending = computed(() => activePreset.value !== originalActivePreset.value)
const previewRows = computed(() => Object.entries(selectedProjection.value || {}).sort(([a], [b]) => a.localeCompare(b)))
const selectedBuiltIn = computed(() => selectedSection.value === 'agent' ? agents.value[selectedAgent.value] : undefined)
const selectedCustom = computed(() => selectedSection.value === 'custom' && selectedCustomIndex.value >= 0 ? customAgents.value[selectedCustomIndex.value] : undefined)
const previewTitle = computed(() => activePreset.value || t('toolAccess.omo.modelUnset'))

function createTri(value: string[] | undefined): TriDraft {
  if (value === undefined) return { mode: 'inherit', items: [] }
  if (value.includes('*')) {
    return { mode: 'all', items: value.filter((item) => item !== '*').map((item) => item.startsWith('!') ? item.slice(1) : item) }
  }
  return { mode: 'custom', items: [...value] }
}

function triValue(value: TriDraft): string[] | undefined {
  if (value.mode === 'inherit') return undefined
  if (value.mode === 'all') return ['*', ...value.items.map((item) => item.startsWith('!') ? item : `!${item}`)]
  return [...value.items]
}

function makeAgent(source?: toolconfig.OmoAgent | ProjectionAgent): AgentDraft {
  const agent = source || {}
  return {
    model: agent.model || (agent as ProjectionAgent).Model || '',
    variant: agent.variant || (agent as ProjectionAgent).Variant || '',
    displayName: (agent as toolconfig.OmoAgent).displayName || '',
    skills: createTri((agent as toolconfig.OmoAgent).skills),
    mcps: createTri((agent as toolconfig.OmoAgent).mcps),
  }
}

function makeCustom(name: string, source?: toolconfig.OmoCustomAgent): CustomDraft {
  const agent = source || ({} as toolconfig.OmoCustomAgent)
  return {
    ...makeAgent(agent),
    name,
    isNew: false,
    prompt: agent.prompt || '',
    orchestratorPrompt: agent.orchestratorPrompt || '',
  }
}

function projectionValue(agent: ProjectionAgent | undefined) {
  return `${agent?.model || agent?.Model || ''}\u0000${agent?.variant || agent?.Variant || ''}`
}

function previewClass(agentName: string, agent: ProjectionAgent) {
  return projectionValue(agent) === projectionValue(currentProjection.value[agentName]) ? 'same' : 'changed'
}

function previewText(agent: ProjectionAgent) {
  const model = agent.model || agent.Model || t('toolAccess.omo.modelUnset')
  const variant = agent.variant || agent.Variant
  return `${model}${variant ? ` · ${variant}` : ''}`
}

function agentDescription(name: string) {
  return builtInNames.includes(name) ? t(`toolAccess.omo.agentDesc.${name}`) : ''
}

function isModelUnknown(model: string) {
  return !!model.trim() && validModels.value.length > 0 && !validModels.value.includes(model.trim())
}

function isAgentDisabled(name: string) {
  return disabledSet.value.has(name)
}

function markAgentDirty(name: string) {
  dirtyAgents.value = new Set(dirtyAgents.value).add(name)
}

function markCustomDirty() {
  customDirty.value = true
}

function markGlobalDirty(kind: GlobalKind) {
  dirtyGlobals.value = new Set(dirtyGlobals.value).add(kind)
}

function toggleDisabled(name: string) {
  const next = new Set(disabledAgents.value)
  if (next.has(name)) next.delete(name)
  else next.add(name)
  disabledAgents.value = [...next]
  markGlobalDirty('agents')
}

function addGlobalTag(kind: GlobalKind) {
  const value = globalInputs.value[kind].trim()
  if (!value) return
  const target = kind === 'agents' ? disabledAgents : kind === 'skills' ? disabledSkills : disabledMcps
  if (!target.value.includes(value)) target.value = [...target.value, value]
  globalInputs.value[kind] = ''
  markGlobalDirty(kind)
}

function removeGlobalTag(kind: GlobalKind, value: string) {
  const target = kind === 'agents' ? disabledAgents : kind === 'skills' ? disabledSkills : disabledMcps
  target.value = target.value.filter((item) => item !== value)
  markGlobalDirty(kind)
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
    knownSkills.value = config.KnownSkills || []
    knownMcps.value = config.KnownMcps || []
    agents.value = Object.fromEntries(builtInNames.map((name) => [name, makeAgent(config.Agents?.[name])]))
    customAgents.value = Object.entries(config.CustomAgents || {}).map(([name, agent]) => makeCustom(name, agent))
    presetAgents.value = (config.PresetAgents || {}) as Projection
    disabledAgents.value = [...(config.DisabledAgents || [])]
    disabledSkills.value = [...(config.DisabledSkills || [])]
    disabledMcps.value = [...(config.DisabledMcps || [])]
    dirtyAgents.value = new Set()
    customDirty.value = false
    dirtyGlobals.value = new Set()
    selectedSection.value = 'agent'
    selectedAgent.value = builtInAgentNames.value[0] || 'orchestrator'
    selectedCustomIndex.value = customAgents.value.length ? 0 : -1
  } catch (e: any) {
    error.value = e?.message || String(e)
  } finally {
    loading.value = false
  }
}

function selectCustom(index: number) {
  selectedSection.value = 'custom'
  selectedCustomIndex.value = index
}

function addCustomAgent() {
  customAgents.value.push({
    name: '', isNew: true, model: '', variant: '', displayName: '',
    skills: { mode: 'inherit', items: [] }, mcps: { mode: 'inherit', items: [] },
    prompt: '', orchestratorPrompt: '',
  })
  selectedCustomIndex.value = customAgents.value.length - 1
  selectedSection.value = 'custom'
  markCustomDirty()
}

async function deleteCustomAgent(index: number) {
  const agent = customAgents.value[index]
  if (!agent) return
  const ok = await confirm.open({
    title: t('toolAccess.omo.deleteAgentTitle'),
    message: t('toolAccess.omo.deleteAgentMessage', { name: agent.name || t('toolAccess.omo.newAgent') }),
    confirmText: t('common.delete'),
    danger: true,
  })
  if (!ok) return
  customAgents.value.splice(index, 1)
  selectedCustomIndex.value = Math.min(index, customAgents.value.length - 1)
  markCustomDirty()
}

function customFieldChanged() {
  markCustomDirty()
}

function customNameChanged(agent: CustomDraft) {
  agent.name = agent.name.trimStart()
  markCustomDirty()
}

function buildChange() {
  const changedAgents: Record<string, toolconfig.OmoAgent> = {}
  for (const name of dirtyAgents.value) {
    const agent = agents.value[name]
    if (!agent) continue
    changedAgents[name] = toolconfig.OmoAgent.createFrom({
      model: agent.model,
      variant: agent.variant,
      displayName: agent.displayName,
      skills: triValue(agent.skills),
      mcps: triValue(agent.mcps),
    })
  }

  let customMap: Record<string, toolconfig.OmoCustomAgent> | undefined
  if (customDirty.value) {
    customMap = {}
    for (const agent of customAgents.value) {
      const name = agent.name.trim()
      if (!name) continue
      customMap[name] = toolconfig.OmoCustomAgent.createFrom({
        model: agent.model,
        variant: agent.variant,
        displayName: agent.displayName,
        skills: triValue(agent.skills),
        mcps: triValue(agent.mcps),
        prompt: agent.prompt,
        orchestratorPrompt: agent.orchestratorPrompt,
      })
    }
  }

  return toolconfig.OmoChange.createFrom({
    ActivePreset: activePreset.value !== originalActivePreset.value ? activePreset.value : undefined,
    Agents: changedAgents,
    CustomAgents: customMap,
    DisabledAgents: dirtyGlobals.value.has('agents') ? [...disabledAgents.value] : undefined,
    DisabledSkills: dirtyGlobals.value.has('skills') ? [...disabledSkills.value] : undefined,
    DisabledMcps: dirtyGlobals.value.has('mcps') ? [...disabledMcps.value] : undefined,
  })
}

async function previewChange() {
  if (previewLoading.value || loading.value) return
  previewLoading.value = true
  try {
    pendingChange.value = buildChange()
    previewData.value = await api.previewToolOmoChange(pendingChange.value)
    previewOpen.value = true
  } catch (e: any) {
    toast.push(e?.message || String(e), 'error')
  } finally {
    previewLoading.value = false
  }
}

async function confirmWrite(allowDrift = false) {
  const change = pendingChange.value
  if (!change || saving.value) return
  saving.value = true
  try {
    await api.applyOmoConfig(change, allowDrift)
    toast.push(t('toolAccess.toast.omoApplied'), 'success')
    await load()
    previewOpen.value = false
    pendingChange.value = null
    emit('applied')
    emit('close')
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
        if (ok) {
          saving.value = false
          await confirmWrite(true)
        }
      } catch (driftError: any) {
        toast.push(driftError?.message || String(driftError), 'error')
      }
    } else {
      toast.push(message, 'error')
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
      <div class="modal-card omo-workbench" role="dialog" aria-modal="true">
        <div class="row-between modal-heading omo-workbench-heading">
          <div>
            <div class="modal-title">{{ t('toolAccess.omo.title') }}</div>
            <div class="section-sub text-mono omo-path">{{ path || t('toolAccess.omo.pathUnavailable') }}</div>
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
          <div class="omo-workbench-body">
            <aside class="omo-sidebar">
              <div class="field omo-preset-field">
                <label class="field-label">{{ t('toolAccess.omo.activePreset') }}</label>
                <select v-model="activePreset" class="select">
                  <option v-for="preset in knownPresets" :key="preset" :value="preset">{{ preset }}</option>
                </select>
                <div class="field-help">{{ t('toolAccess.omo.activePresetHelp') }}</div>
              </div>

              <div v-if="presetSwitchPending" class="omo-switch-summary">
                <div class="row-between">
                  <strong>{{ t('toolAccess.omo.switchPreview') }}</strong>
                  <span class="badge info">{{ previewTitle }}</span>
                </div>
                <div v-if="previewRows.length" class="omo-summary-list">
                  <div v-for="([name, agent]) in previewRows" :key="name" class="omo-summary-row" :class="previewClass(name, agent)">
                    <span class="text-mono">{{ name }}</span><span class="text-mono">{{ previewText(agent) }}</span>
                  </div>
                </div>
                <div v-else class="field-help">{{ t('toolAccess.omo.noProjection') }}</div>
              </div>

              <div class="omo-nav-label">{{ t('toolAccess.omo.builtInAgents') }} <span>{{ builtInAgentNames.length }}</span></div>
              <nav class="omo-agent-nav" aria-label="agents">
                <button v-for="name in builtInAgentNames" :key="name" type="button" class="omo-nav-item" :class="{ active: selectedSection === 'agent' && selectedAgent === name }" @click="selectedSection = 'agent'; selectedAgent = name">
                  <span class="omo-nav-item-main"><span class="text-mono" :class="{ 'omo-disabled-name': isAgentDisabled(name) }">{{ name }}</span><span v-if="isAgentDisabled(name)" class="badge warn">{{ t('toolAccess.omo.disabledShort') }}</span></span>
                  <span v-if="agentDescription(name)" class="omo-nav-description">{{ agentDescription(name) }}</span>
                  <label class="toggle toggle-sm omo-nav-toggle" :aria-label="t(isAgentDisabled(name) ? 'toolAccess.omo.enableAgent' : 'toolAccess.omo.disableAgent', { name })" @click.stop>
                    <input type="checkbox" :checked="!isAgentDisabled(name)" @change="toggleDisabled(name)"><span class="toggle-slider blue"/>
                  </label>
                </button>
              </nav>

              <div class="row-between omo-nav-label omo-custom-label"><span>{{ t('toolAccess.omo.customAgents') }} <span>{{ customAgents.length }}</span></span><button class="btn btn-icon btn-sm" :title="t('toolAccess.omo.newAgent')" :aria-label="t('toolAccess.omo.newAgent')" @click="addCustomAgent"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg></button></div>
              <nav v-if="customAgents.length" class="omo-agent-nav">
                <button v-for="(agent, index) in customAgents" :key="`${agent.name}-${index}`" type="button" class="omo-nav-item" :class="{ active: selectedSection === 'custom' && selectedCustomIndex === index }" @click="selectCustom(index)">
                  <span class="omo-nav-item-main"><span class="text-mono" :class="{ 'omo-disabled-name': isAgentDisabled(agent.name) }">{{ agent.name || t('toolAccess.omo.newAgent') }}</span><span v-if="isAgentDisabled(agent.name)" class="badge warn">{{ t('toolAccess.omo.disabledShort') }}</span></span>
                  <label class="toggle toggle-sm omo-nav-toggle" :aria-label="t(isAgentDisabled(agent.name) ? 'toolAccess.omo.enableAgent' : 'toolAccess.omo.disableAgent', { name: agent.name || t('toolAccess.omo.newAgent') })" @click.stop>
                    <input type="checkbox" :checked="!isAgentDisabled(agent.name)" @change="toggleDisabled(agent.name)"><span class="toggle-slider blue"/>
                  </label>
                </button>
              </nav>
              <div v-else class="field-help omo-empty-nav">{{ t('toolAccess.omo.noCustomAgents') }}</div>
              <div v-if="otherDisabledAgents.length" class="omo-other-disabled">
                <div class="omo-nav-label">{{ t('toolAccess.omo.otherDisabled') }}</div>
                <div class="tri-state-tags">
                  <button v-for="name in otherDisabledAgents" :key="name" type="button" class="badge mono tri-state-tag" :title="t('toolAccess.omo.reenableAgent', { name })" @click="toggleDisabled(name)">
                    {{ name }}
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>
                  </button>
                </div>
              </div>

              <button type="button" class="omo-global-nav" :class="{ active: selectedSection === 'global' }" @click="selectedSection = 'global'"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M4 7h16M4 12h16M4 17h16"/><circle cx="9" cy="7" r="2" fill="var(--surface)"/><circle cx="15" cy="12" r="2" fill="var(--surface)"/><circle cx="11" cy="17" r="2" fill="var(--surface)"/></svg>{{ t('toolAccess.omo.globalSettings') }}</button>
            </aside>

            <main class="omo-editor">
              <template v-if="selectedSection === 'agent' && selectedBuiltIn">
                <div class="omo-editor-header"><div><h3>{{ selectedAgent }}</h3><p>{{ agentDescription(selectedAgent) }}</p></div><span class="badge" :class="isAgentDisabled(selectedAgent) ? 'warn' : 'success'">{{ isAgentDisabled(selectedAgent) ? t('toolAccess.omo.disabledShort') : t('toolAccess.omo.enabled') }}</span></div>
                <div class="omo-editor-grid">
                  <div class="field"><label class="field-label">{{ t('toolAccess.omo.model') }}</label><AutoComplete v-model="selectedBuiltIn.model" :options="validModels" :placeholder="t('toolAccess.omo.modelPlaceholder')" @update:model-value="markAgentDirty(selectedAgent)"/><div v-if="isModelUnknown(selectedBuiltIn.model)" class="omo-model-warning">{{ t('toolAccess.omo.modelUnknown') }}</div></div>
                  <div class="field"><label class="field-label">{{ t('toolAccess.omo.variant') }}</label><select v-model="selectedBuiltIn.variant" class="select" @change="markAgentDirty(selectedAgent)"><option value="">{{ t('toolAccess.omo.variantNone') }}</option><option v-for="variant in variants" :key="variant" :value="variant">{{ variant }}</option></select></div>
                  <div class="field omo-field-wide"><label class="field-label">{{ t('toolAccess.omo.displayName') }}</label><input v-model="selectedBuiltIn.displayName" class="input" :placeholder="t('toolAccess.omo.displayNamePlaceholder')" @input="markAgentDirty(selectedAgent)"></div>
                </div>
                <div class="omo-editor-section"><TriStateTagEditor v-model:mode="selectedBuiltIn.skills.mode" v-model:items="selectedBuiltIn.skills.items" :label="t('toolAccess.omo.skills')" :options="knownSkills" @change="markAgentDirty(selectedAgent)"/><TriStateTagEditor v-model:mode="selectedBuiltIn.mcps.mode" v-model:items="selectedBuiltIn.mcps.items" :label="t('toolAccess.omo.mcps')" :options="knownMcps" @change="markAgentDirty(selectedAgent)"/></div>
              </template>

              <template v-else-if="selectedSection === 'custom' && selectedCustom">
                <div class="omo-editor-header"><div><h3>{{ selectedCustom.name || t('toolAccess.omo.newAgent') }}</h3><p>{{ t('toolAccess.omo.customAgentHelp') }}</p></div><button class="btn btn-danger-ghost btn-sm" type="button" @click="deleteCustomAgent(selectedCustomIndex)">{{ t('common.delete') }}</button></div>
                <div class="omo-editor-grid">
                  <div class="field omo-field-wide"><label class="field-label">{{ t('toolAccess.omo.agentName') }}</label><input v-model="selectedCustom.name" class="input mono" :readonly="!selectedCustom.isNew" :placeholder="t('toolAccess.omo.agentNamePlaceholder')" @input="customNameChanged(selectedCustom)"><div v-if="!selectedCustom.isNew" class="field-help">{{ t('toolAccess.omo.agentNameReadonly') }}</div></div>
                  <div class="field"><label class="field-label">{{ t('toolAccess.omo.model') }}</label><AutoComplete v-model="selectedCustom.model" :options="validModels" :placeholder="t('toolAccess.omo.modelPlaceholder')" @update:model-value="customFieldChanged"/><div v-if="isModelUnknown(selectedCustom.model)" class="omo-model-warning">{{ t('toolAccess.omo.modelUnknown') }}</div></div>
                  <div class="field"><label class="field-label">{{ t('toolAccess.omo.variant') }}</label><select v-model="selectedCustom.variant" class="select" @change="customFieldChanged"><option value="">{{ t('toolAccess.omo.variantNone') }}</option><option v-for="variant in variants" :key="variant" :value="variant">{{ variant }}</option></select></div>
                  <div class="field omo-field-wide"><label class="field-label">{{ t('toolAccess.omo.displayName') }}</label><input v-model="selectedCustom.displayName" class="input" :placeholder="t('toolAccess.omo.displayNamePlaceholder')" @input="customFieldChanged"></div>
                </div>
                <div class="omo-editor-section"><TriStateTagEditor v-model:mode="selectedCustom.skills.mode" v-model:items="selectedCustom.skills.items" :label="t('toolAccess.omo.skills')" :options="knownSkills" @change="customFieldChanged"/><TriStateTagEditor v-model:mode="selectedCustom.mcps.mode" v-model:items="selectedCustom.mcps.items" :label="t('toolAccess.omo.mcps')" :options="knownMcps" @change="customFieldChanged"/></div>
                <div class="omo-textarea-grid"><div class="field"><label class="field-label">{{ t('toolAccess.omo.prompt') }}</label><textarea v-model="selectedCustom.prompt" class="input omo-textarea" :placeholder="t('toolAccess.omo.promptPlaceholder')" @input="customFieldChanged"/></div><div class="field"><label class="field-label">{{ t('toolAccess.omo.orchestratorPrompt') }}</label><textarea v-model="selectedCustom.orchestratorPrompt" class="input omo-textarea" :placeholder="t('toolAccess.omo.orchestratorPromptPlaceholder')" @input="customFieldChanged"/><div class="field-help">{{ t('toolAccess.omo.orchestratorPromptHelp', { name: selectedCustom.name || 'agent' }) }}</div><div v-if="selectedCustom.orchestratorPrompt.trim() && !selectedCustom.orchestratorPrompt.trim().startsWith(`@${selectedCustom.name.trim()}`)" class="omo-model-warning">{{ t('toolAccess.omo.orchestratorPromptWarning') }}</div></div></div>
              </template>

              <template v-else>
                <div class="omo-editor-header"><div><h3>{{ t('toolAccess.omo.globalSettings') }}</h3><p>{{ t('toolAccess.omo.globalSettingsHelp') }}</p></div></div>
                <div class="omo-global-editors">
                  <div v-for="kind in (['agents', 'skills', 'mcps'] as GlobalKind[])" :key="kind" class="omo-global-editor">
                    <label class="field-label">{{ t(`toolAccess.omo.globalDisabled.${kind}`) }}</label>
                    <div class="row tri-state-input-row"><AutoComplete v-model="globalInputs[kind]" :options="kind === 'agents' ? allAgentNames : kind === 'skills' ? knownSkills : knownMcps" :placeholder="t('toolAccess.omo.addTagPlaceholder')" @keydown.enter.prevent="addGlobalTag(kind)"/><button class="btn btn-secondary tri-state-add" type="button" :title="t('toolAccess.omo.addTag')" @click="addGlobalTag(kind)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg></button></div>
                    <div class="tri-state-tags"><button v-for="item in (kind === 'agents' ? disabledAgents : kind === 'skills' ? disabledSkills : disabledMcps)" :key="item" type="button" class="badge mono tri-state-tag" :title="t('toolAccess.omo.removeTag')" @click="removeGlobalTag(kind, item)">{{ item }}<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div>
                  </div>
                </div>
              </template>
            </main>
          </div>

          <div class="omo-workbench-footer"><span class="field-help">{{ t('toolAccess.omo.previewBeforeWrite') }}</span><div class="row"><button class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button><button class="btn btn-primary" :disabled="previewLoading" @click="previewChange">{{ previewLoading ? t('toolAccess.omo.previewLoading') : t('toolAccess.omo.previewChanges') }}</button></div></div>
        </template>
      </div>
    </div>

    <div v-if="previewOpen && previewData" class="modal-overlay omo-preview-overlay" @click.self="previewOpen = false">
      <div class="modal-card omo-preview-modal">
        <div class="row-between modal-heading"><div><div class="modal-title">{{ t('toolAccess.omo.previewTitle') }}</div><div class="section-sub text-mono omo-path">{{ previewData.Path }}</div></div><button class="btn btn-icon" :title="t('common.close')" :aria-label="t('common.close')" @click="previewOpen = false"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div>
        <div class="omo-preview-note">{{ t('toolAccess.omo.previewBackupNote') }}</div>
        <div class="field"><label class="field-label">{{ t('toolAccess.omo.previewAfter') }}</label><pre class="omo-after-code">{{ previewData.After }}</pre></div>
        <div class="row omo-preview-actions"><button class="btn btn-secondary" :disabled="saving" @click="previewOpen = false">{{ t('toolAccess.omo.cancelPreview') }}</button><button class="btn btn-primary" :disabled="saving" @click="confirmWrite()">{{ saving ? t('common.processing') : t('toolAccess.omo.confirmWrite') }}</button></div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.omo-workbench { width: 92vw; max-width: 1200px; height: 86vh; max-height: 900px; display: flex; flex-direction: column; overflow: hidden; }
.omo-workbench-heading { flex: 0 0 auto; align-items: flex-start; margin-bottom: 14px; }
.omo-path { max-width: 80vw; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.omo-workbench-body { min-height: 0; flex: 1 1 auto; display: grid; grid-template-columns: 272px minmax(0, 1fr); border: 1px solid var(--border); border-radius: var(--radius-sm); overflow: hidden; }
.omo-sidebar { min-width: 0; padding: 14px 12px; border-right: 1px solid var(--border); background: color-mix(in srgb, var(--bg) 44%, var(--surface)); overflow-y: auto; overflow-x: hidden; }
.omo-preset-field { padding-bottom: 13px; border-bottom: 1px solid var(--border); }
.omo-switch-summary { margin: 11px 0 14px; padding: 9px; border: 1px solid color-mix(in srgb, var(--accent) 35%, var(--border)); border-radius: var(--radius-sm); background: color-mix(in srgb, var(--accent-soft) 42%, var(--surface)); font-size: 11px; }
.omo-summary-list { display: flex; flex-direction: column; gap: 2px; margin-top: 7px; }
.omo-summary-row { display: flex; justify-content: space-between; gap: 7px; padding: 4px 5px; color: var(--muted); border-radius: var(--radius-xs); }
.omo-summary-row span { min-width: 0; max-width: 55%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.omo-summary-row.changed { color: var(--fg); background: color-mix(in srgb, var(--warning) 12%, transparent); }
.omo-nav-label { display: flex; justify-content: space-between; align-items: center; margin: 12px 4px 5px; color: var(--muted); font-size: 10.5px; font-weight: 600; text-transform: uppercase; letter-spacing: .03em; }
.omo-custom-label { margin-top: 17px; }
.omo-custom-label .btn { color: var(--muted); }
.omo-custom-label .btn svg { width: 14px; height: 14px; }
.omo-agent-nav { display: flex; flex-direction: column; gap: 2px; }
.omo-nav-item { position: relative; display: flex; flex-direction: column; align-items: stretch; gap: 3px; min-width: 0; padding: 8px 36px 8px 9px; border: 1px solid transparent; border-radius: var(--radius-sm); background: transparent; color: var(--fg); text-align: left; cursor: pointer; }
.omo-nav-item:hover { background: color-mix(in srgb, var(--surface) 70%, transparent); }
.omo-nav-item.active { border-color: color-mix(in srgb, var(--accent) 45%, var(--border)); background: var(--accent-soft); }
.omo-nav-item-main { display: flex; align-items: center; gap: 6px; min-width: 0; }
.omo-nav-item-main .text-mono, .omo-nav-item > .text-mono { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.omo-nav-description { overflow: hidden; color: var(--muted); font-size: 10px; line-height: 1.3; text-overflow: ellipsis; white-space: nowrap; }
.omo-nav-toggle { position: absolute; top: 11px; right: 8px; }
.omo-disabled-name { color: var(--muted); text-decoration: line-through; }
.omo-empty-nav { padding: 4px 4px 8px; }
.omo-other-disabled { margin-top: 14px; padding-top: 2px; }
.omo-other-disabled .omo-nav-label { margin-top: 0; }
.omo-global-nav { display: flex; align-items: center; gap: 8px; width: 100%; margin-top: 18px; padding: 9px; border: 1px solid transparent; border-radius: var(--radius-sm); background: transparent; color: var(--muted); font: inherit; font-size: 12px; text-align: left; cursor: pointer; }
.omo-global-nav:hover, .omo-global-nav.active { border-color: var(--border); background: var(--surface); color: var(--fg); }
.omo-global-nav svg { width: 15px; height: 15px; }
.omo-editor { min-width: 0; padding: 21px 24px; overflow-y: auto; overflow-x: hidden; }
.omo-editor-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 14px; margin-bottom: 20px; }
.omo-editor-header h3 { margin: 0; font-size: 17px; font-weight: 650; }
.omo-editor-header p { margin: 5px 0 0; color: var(--muted); font-size: 12px; line-height: 1.45; }
.omo-editor-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 15px 16px; }
.omo-field-wide { grid-column: 1 / -1; }
.omo-editor-section { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 18px; margin-top: 22px; padding-top: 18px; border-top: 1px solid var(--border); }
.omo-textarea-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; margin-top: 22px; padding-top: 18px; border-top: 1px solid var(--border); }
.omo-textarea { min-height: 150px; resize: vertical; line-height: 1.5; }
.omo-model-warning { margin-top: 4px; color: var(--warning); font-size: 11px; line-height: 1.35; }
.omo-global-editors { display: flex; flex-direction: column; gap: 20px; max-width: 720px; }
.omo-global-editor { display: flex; flex-direction: column; gap: 7px; }
.omo-workbench-footer { display: flex; align-items: center; justify-content: space-between; gap: 16px; flex: 0 0 auto; padding-top: 13px; }
.omo-workbench-footer .field-help { min-width: 0; }
.omo-state { padding: 45px 0; text-align: center; }
.tool-inline-error { display: flex; flex-direction: column; gap: 8px; padding: 12px; border-radius: var(--radius-sm); background: rgba(217, 48, 37, .08); color: var(--negative); font-size: 12px; }
.tool-inline-error .btn { align-self: flex-start; color: var(--fg); }
.omo-preview-overlay { z-index: 20; }
.omo-preview-modal { width: min(860px, 88vw); max-height: 82vh; display: flex; flex-direction: column; overflow: hidden; }
.omo-preview-modal .modal-heading { flex: 0 0 auto; align-items: flex-start; margin-bottom: 15px; }
.omo-preview-note { flex: 0 0 auto; margin-bottom: 12px; padding: 9px 11px; border: 1px solid color-mix(in srgb, var(--accent) 28%, var(--border)); border-radius: var(--radius-sm); background: var(--accent-soft); color: var(--muted); font-size: 12px; }
.omo-after-code { min-height: 240px; max-height: 53vh; margin: 0; padding: 12px; overflow: auto; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--bg); color: var(--fg); font: 11px/1.55 var(--font-mono); white-space: pre-wrap; overflow-wrap: anywhere; }
.omo-preview-actions { justify-content: flex-end; margin-top: 14px; flex: 0 0 auto; }
@media (max-width: 900px) {
  .omo-workbench { width: 96vw; height: 90vh; }
  .omo-workbench-body { grid-template-columns: 220px minmax(0, 1fr); }
  .omo-editor { padding: 17px; }
  .omo-editor-section, .omo-textarea-grid { grid-template-columns: 1fr; }
}
@media (max-width: 680px) {
  .omo-workbench { width: 100%; height: 100%; max-height: none; border-radius: 0; }
  .omo-workbench-body { grid-template-columns: 1fr; overflow: auto; }
  .omo-sidebar { max-height: 250px; border-right: none; border-bottom: 1px solid var(--border); }
  .omo-editor-grid { grid-template-columns: 1fr; }
  .omo-workbench-footer { align-items: flex-end; flex-direction: column; }
  .omo-workbench-footer .row { width: 100%; justify-content: flex-end; }
}
</style>
