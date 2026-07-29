<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/bridge'
import AutoComplete from '@/components/AutoComplete.vue'
import DiffPreview from '@/components/DiffPreview.vue'
import TriStateTagEditor from '@/components/omo-slim/TriStateTagEditor.vue'
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
type PresetOperation = 'upsert' | 'rename' | 'delete'
type PresetOpDraft = {
  Operation: PresetOperation
  Name: string
  NewName?: string
  Agents?: Record<string, toolconfig.OmoSlimAgent>
}
type PresetEntry = {
  name: string
  agents: Record<string, ProjectionAgent>
  isNew: boolean
  upsertIndex: number
}
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
const presetOps = ref<PresetOpDraft[]>([])
const dirtyAgents = ref<Set<string>>(new Set())
const customDirty = ref(false)
const dirtyGlobals = ref<Set<GlobalKind>>(new Set())
const selectedSection = ref<Section>('agent')
const selectedAgent = ref('orchestrator')
const selectedCustomIndex = ref(-1)
const globalInputs = ref<Record<GlobalKind, string>>({ agents: '', skills: '', mcps: '' })
const previewOpen = ref(false)
const previewData = ref<service.OmoSlimPreview | null>(null)
const pendingChange = ref<toolconfig.OmoSlimChange | null>(null)
const renamingPreset = ref('')
const renameInput = ref('')
const renameError = ref('')

const builtInAgentNames = computed(() => builtInNames.filter((name) => Object.prototype.hasOwnProperty.call(agents.value, name)))
const customAgentNames = computed(() => customAgents.value.map((agent) => agent.name).filter(Boolean))
const allAgentNames = computed(() => [...new Set([...builtInNames, ...customAgentNames.value])].sort((a, b) => a.localeCompare(b)))
const disabledSet = computed(() => new Set(disabledAgents.value))
const otherDisabledAgents = computed(() => disabledAgents.value.filter((name) => !Object.prototype.hasOwnProperty.call(agents.value, name)))
const currentProjection = computed(() => presetAgents.value[originalActivePreset.value] || {})
const presetEntries = computed<PresetEntry[]>(() => {
  const entries = new Map<string, PresetEntry>()
  for (const name of knownPresets.value) {
    entries.set(name, { name, agents: presetAgents.value[name] || {}, isNew: false, upsertIndex: -1 })
  }

  presetOps.value.forEach((op, index) => {
    if (op.Operation === 'upsert') {
      const previous = entries.get(op.Name)
      entries.set(op.Name, {
        name: op.Name,
        agents: op.Agents || {},
        isNew: previous ? previous.isNew : !knownPresets.value.includes(op.Name),
        upsertIndex: index,
      })
    } else if (op.Operation === 'rename' && op.NewName) {
      const previous = entries.get(op.Name)
      if (!previous) return
      entries.delete(op.Name)
      entries.set(op.NewName, { ...previous, name: op.NewName })
    } else if (op.Operation === 'delete') {
      entries.delete(op.Name)
    }
  })

  return [...entries.values()]
})
const selectedProjection = computed(() => presetEntries.value.find((entry) => entry.name === activePreset.value)?.agents)
const presetSwitchPending = computed(() => activePreset.value !== originalActivePreset.value)
const previewRows = computed(() => Object.entries(selectedProjection.value || {}).sort(([a], [b]) => a.localeCompare(b)))
const selectedBuiltIn = computed(() => selectedSection.value === 'agent' ? agents.value[selectedAgent.value] : undefined)
const selectedCustom = computed(() => selectedSection.value === 'custom' && selectedCustomIndex.value >= 0 ? customAgents.value[selectedCustomIndex.value] : undefined)
const previewTitle = computed(() => activePreset.value || t('toolAccess.omoSlim.modelUnset'))

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

function makeAgent(source?: toolconfig.OmoSlimAgent | ProjectionAgent): AgentDraft {
  const agent = source || {}
  return {
    model: agent.model || (agent as ProjectionAgent).Model || '',
    variant: agent.variant || (agent as ProjectionAgent).Variant || '',
    displayName: (agent as toolconfig.OmoSlimAgent).displayName || '',
    skills: createTri((agent as toolconfig.OmoSlimAgent).skills),
    mcps: createTri((agent as toolconfig.OmoSlimAgent).mcps),
  }
}

function makeCustom(name: string, source?: toolconfig.OmoSlimCustomAgent): CustomDraft {
  const agent = source || ({} as toolconfig.OmoSlimCustomAgent)
  return {
    ...makeAgent(agent),
    name,
    isNew: false,
    prompt: agent.prompt || '',
    orchestratorPrompt: agent.orchestratorPrompt || '',
  }
}

function draftToAgent(agent: AgentDraft): toolconfig.OmoSlimAgent {
  return toolconfig.OmoSlimAgent.createFrom({
    model: agent.model,
    variant: agent.variant,
    displayName: agent.displayName,
    skills: triValue(agent.skills),
    mcps: triValue(agent.mcps),
  })
}

function clonePresetAgents(source: Record<string, ProjectionAgent> | undefined): Record<string, toolconfig.OmoSlimAgent> {
  return Object.fromEntries(Object.entries(source || {}).map(([name, agent]) => [name, toolconfig.OmoSlimAgent.createFrom({
    model: agent.model || agent.Model || '',
    variant: agent.variant || agent.Variant || '',
    displayName: (agent as toolconfig.OmoSlimAgent).displayName || '',
    skills: (agent as toolconfig.OmoSlimAgent).skills,
    mcps: (agent as toolconfig.OmoSlimAgent).mcps,
  })]))
}

function entryForPreset(name: string) {
  return presetEntries.value.find((entry) => entry.name === name)
}

function stagedPresetAgents(name: string) {
  const entry = entryForPreset(name)
  const cloned = clonePresetAgents(entry?.agents)
  if (name === activePreset.value && dirtyAgents.value.size) {
    for (const agentName of dirtyAgents.value) {
      const agent = agents.value[agentName]
      if (agent) cloned[agentName] = draftToAgent(agent)
    }
  }
  return cloned
}

function stageUpsert(name: string, source: Record<string, ProjectionAgent>) {
  const agentsForPreset = clonePresetAgents(source)
  let currentIndex = -1
  for (let index = presetOps.value.length - 1; index >= 0; index--) {
    const op = presetOps.value[index]
    if (op.Operation === 'upsert' && op.Name === name) {
      currentIndex = index
      break
    }
  }
  const next = [...presetOps.value]
  const operation: PresetOpDraft = { Operation: 'upsert', Name: name, Agents: agentsForPreset }
  if (currentIndex >= 0) next[currentIndex] = operation
  else next.push(operation)
  presetOps.value = next
}

function syncNewPresetAgents() {
  const entry = entryForPreset(activePreset.value)
  if (!entry?.isNew || entry.upsertIndex < 0) return
  const next = [...presetOps.value]
  next[entry.upsertIndex] = {
    ...next[entry.upsertIndex],
    Agents: Object.fromEntries(Object.entries(agents.value).map(([name, agent]) => [name, draftToAgent(agent)])),
  }
  presetOps.value = next
}

function uniquePresetName(base: string) {
  const names = new Set(presetEntries.value.map((entry) => entry.name))
  if (!names.has(base)) return base
  let index = 2
  while (names.has(`${base}-${index}`)) index++
  return `${base}-${index}`
}

function selectPreset(name: string) {
  activePreset.value = name
  selectedSection.value = 'agent'
  const entry = entryForPreset(name)
  if (entry?.isNew) {
    agents.value = Object.fromEntries(builtInNames.map((agentName) => [agentName, makeAgent(entry.agents[agentName])]))
    dirtyAgents.value = new Set()
  }
}

function startRename(name: string) {
  renamingPreset.value = name
  renameInput.value = name
  renameError.value = ''
  void nextTick(() => document.querySelector<HTMLInputElement>('.omo-slim-preset-rename-input')?.focus())
}

function cancelRename() {
  renamingPreset.value = ''
  renameInput.value = ''
  renameError.value = ''
}

function commitRename() {
  const oldName = renamingPreset.value
  if (!oldName) return
  const newName = renameInput.value.trim()
  if (!newName) {
    renameError.value = t('toolAccess.omoSlim.presetNameRequired')
    return
  }
  if (newName === oldName) {
    cancelRename()
    return
  }
  if (presetEntries.value.some((entry) => entry.name !== oldName && entry.name === newName)) {
    renameError.value = t('toolAccess.omoSlim.presetNameTaken')
    return
  }

  presetOps.value = [...presetOps.value, { Operation: 'rename', Name: oldName, NewName: newName }]
  if (activePreset.value === oldName) activePreset.value = newName
  cancelRename()
}

function addPreset() {
  let index = 1
  let name = t('toolAccess.omoSlim.presetDefaultName', { index })
  while (presetEntries.value.some((entry) => entry.name === name)) {
    index++
    name = t('toolAccess.omoSlim.presetDefaultName', { index })
  }
  stageUpsert(name, {})
  activePreset.value = name
  agents.value = Object.fromEntries(builtInNames.map((agentName) => [agentName, makeAgent()]))
  dirtyAgents.value = new Set()
  selectedSection.value = 'agent'
  startRename(name)
}

function duplicatePreset(name: string) {
  const source = stagedPresetAgents(name)
  const copyName = uniquePresetName(`${name}${t('toolAccess.omoSlim.presetCopySuffix')}`)
  stageUpsert(copyName, source)
  activePreset.value = copyName
  agents.value = Object.fromEntries(builtInNames.map((agentName) => [agentName, makeAgent(source[agentName])]))
  dirtyAgents.value = new Set()
  selectedSection.value = 'agent'
}

async function deletePreset(name: string) {
  const isActive = activePreset.value === name
  const ok = await confirm.open({
    title: t('toolAccess.omoSlim.deletePresetTitle'),
    message: t(isActive ? 'toolAccess.omoSlim.deleteActivePresetMessage' : 'toolAccess.omoSlim.deletePresetMessage', { name }),
    confirmText: t('common.delete'),
    danger: true,
  })
  if (!ok) return
  presetOps.value = [...presetOps.value, { Operation: 'delete', Name: name }]
  if (isActive) activePreset.value = ''
  if (renamingPreset.value === name) cancelRename()
}

function projectionValue(agent: ProjectionAgent | undefined) {
  return `${agent?.model || agent?.Model || ''}\u0000${agent?.variant || agent?.Variant || ''}`
}

function previewClass(agentName: string, agent: ProjectionAgent) {
  return projectionValue(agent) === projectionValue(currentProjection.value[agentName]) ? 'same' : 'changed'
}

function previewText(agent: ProjectionAgent) {
  const model = agent.model || agent.Model || t('toolAccess.omoSlim.modelUnset')
  const variant = agent.variant || agent.Variant
  return `${model}${variant ? ` · ${variant}` : ''}`
}

function agentDescription(name: string) {
  return builtInNames.includes(name) ? t(`toolAccess.omoSlim.agentDesc.${name}`) : ''
}

function isModelUnknown(model: string) {
  return !!model.trim() && validModels.value.length > 0 && !validModels.value.includes(model.trim())
}

function isAgentDisabled(name: string) {
  return disabledSet.value.has(name)
}

function markAgentDirty(name: string) {
  if (entryForPreset(activePreset.value)?.isNew) {
    dirtyAgents.value = new Set(dirtyAgents.value).add(name)
    syncNewPresetAgents()
    return
  }
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
    ? states.map((state) => `${state.Resource}: ${state.Missing ? t('toolAccess.omoSlim.driftMissing') : state.Drifted ? t('toolAccess.omoSlim.driftChanged') : t('toolAccess.omoSlim.driftUnchanged')}\n${state.Path}`).join('\n\n')
    : t('toolAccess.omoSlim.driftNone')
  return `${t('toolAccess.omoSlim.configChangedMessage')}\n\n${details}`
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const config = await api.getOmoSlimConfig()
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
    presetOps.value = []
    renamingPreset.value = ''
    renameInput.value = ''
    renameError.value = ''
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
    title: t('toolAccess.omoSlim.deleteAgentTitle'),
    message: t('toolAccess.omoSlim.deleteAgentMessage', { name: agent.name || t('toolAccess.omoSlim.newAgent') }),
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
  const changedAgents: Record<string, toolconfig.OmoSlimAgent> = {}
  for (const name of dirtyAgents.value) {
    const agent = agents.value[name]
    if (!agent) continue
    changedAgents[name] = draftToAgent(agent)
  }

  let customMap: Record<string, toolconfig.OmoSlimCustomAgent> | undefined
  if (customDirty.value) {
    customMap = {}
    for (const agent of customAgents.value) {
      const name = agent.name.trim()
      if (!name) continue
      customMap[name] = toolconfig.OmoSlimCustomAgent.createFrom({
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

  return toolconfig.OmoSlimChange.createFrom({
    ActivePreset: activePreset.value !== originalActivePreset.value ? activePreset.value : undefined,
    Agents: changedAgents,
    CustomAgents: customMap,
    DisabledAgents: dirtyGlobals.value.has('agents') ? [...disabledAgents.value] : undefined,
    DisabledSkills: dirtyGlobals.value.has('skills') ? [...disabledSkills.value] : undefined,
    DisabledMcps: dirtyGlobals.value.has('mcps') ? [...disabledMcps.value] : undefined,
    PresetOps: presetOps.value.map((op) => toolconfig.OmoSlimPresetOp.createFrom({
      Operation: op.Operation,
      Name: op.Name,
      NewName: op.NewName,
      Agents: op.Agents,
    })),
  })
}

async function previewChange() {
  if (previewLoading.value || loading.value) return
  previewLoading.value = true
  try {
    pendingChange.value = buildChange()
    previewData.value = await api.previewToolOmoSlimChange(pendingChange.value)
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
    await api.applyOmoSlimConfig(change, allowDrift)
    toast.push(t('toolAccess.toast.omoSlimApplied'), 'success')
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
          title: t('toolAccess.omoSlim.configChangedTitle'),
          message: driftMessage(states),
          confirmText: t('toolAccess.omoSlim.configChangedConfirm'),
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
      <div class="modal-card omo-slim-workbench" role="dialog" aria-modal="true">
        <div class="row-between modal-heading omo-slim-workbench-heading">
          <div>
            <div class="modal-title">{{ t('toolAccess.omoSlim.title') }}</div>
            <div class="section-sub text-mono omo-slim-path">{{ path || t('toolAccess.omoSlim.pathUnavailable') }}</div>
          </div>
          <button class="btn btn-icon" :title="t('common.close')" :aria-label="t('common.close')" @click="emit('close')">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>
          </button>
        </div>

        <div v-if="loading" class="text-muted omo-slim-state">{{ t('toolAccess.omoSlim.loading') }}</div>
        <div v-else-if="error" class="tool-inline-error" role="alert">
          <strong>{{ t('toolAccess.omoSlim.loadFailed') }}</strong>
          <span>{{ error }}</span>
          <button class="btn btn-secondary" @click="load">{{ t('toolAccess.omoSlim.retry') }}</button>
        </div>
        <template v-else>
          <div class="omo-slim-workbench-body">
            <aside class="omo-slim-sidebar">
              <div class="field omo-slim-preset-field">
                <div class="row-between omo-slim-preset-heading">
                  <label class="field-label">{{ t('toolAccess.omoSlim.activePreset') }}</label>
                  <button class="btn btn-secondary omo-slim-add-preset" type="button" @click="addPreset">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
                    {{ t('toolAccess.omoSlim.addPreset') }}
                  </button>
                </div>
                <div v-if="presetEntries.length" class="omo-slim-preset-list" role="listbox" :aria-label="t('toolAccess.omoSlim.activePreset')">
                  <div v-for="preset in presetEntries" :key="preset.name" class="omo-slim-preset-row" :class="{ active: activePreset === preset.name }" role="option" :aria-selected="activePreset === preset.name">
                    <input v-if="renamingPreset === preset.name" v-model="renameInput" class="input mono omo-slim-preset-rename-input" :aria-label="t('toolAccess.omoSlim.renamePreset')" @click.stop @keydown.enter.prevent="commitRename" @keydown.esc.prevent="cancelRename" @blur="commitRename">
                    <button v-else class="omo-slim-preset-name" type="button" @click="selectPreset(preset.name)">
                      <span class="text-mono">{{ preset.name }}</span>
                      <span v-if="preset.isNew" class="badge info">{{ t('toolAccess.omoSlim.staged') }}</span>
                    </button>
                    <div class="omo-slim-preset-actions">
                      <button class="btn btn-icon btn-sm" type="button" :title="t('toolAccess.omoSlim.duplicatePreset')" :aria-label="t('toolAccess.omoSlim.duplicatePreset')" @click="duplicatePreset(preset.name)">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"/></svg>
                      </button>
                      <button class="btn btn-icon btn-sm" type="button" :title="t('toolAccess.omoSlim.renamePreset')" :aria-label="t('toolAccess.omoSlim.renamePreset')" @click="startRename(preset.name)">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1-1-4z"/></svg>
                      </button>
                      <button class="btn btn-icon btn-sm danger-icon" type="button" :title="t('common.delete')" :aria-label="t('toolAccess.omoSlim.deletePreset')" @click="deletePreset(preset.name)">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/></svg>
                      </button>
                    </div>
                  </div>
                </div>
                <div v-else class="field-help omo-slim-empty-presets">{{ t('toolAccess.omoSlim.noPresets') }}</div>
                <div v-if="renameError" class="omo-slim-preset-error" role="alert">{{ renameError }}</div>
                <div class="field-help">{{ t('toolAccess.omoSlim.activePresetHelp') }}</div>
              </div>

              <div v-if="presetSwitchPending" class="omo-slim-switch-summary">
                <div class="row-between">
                  <strong>{{ t('toolAccess.omoSlim.switchPreview') }}</strong>
                  <span class="badge info">{{ previewTitle }}</span>
                </div>
                <div v-if="previewRows.length" class="omo-slim-summary-list">
                  <div v-for="([name, agent]) in previewRows" :key="name" class="omo-slim-summary-row" :class="previewClass(name, agent)">
                    <span class="text-mono">{{ name }}</span><span class="text-mono">{{ previewText(agent) }}</span>
                  </div>
                </div>
                <div v-else class="field-help">{{ t('toolAccess.omoSlim.noProjection') }}</div>
              </div>

              <div class="omo-slim-nav-label">{{ t('toolAccess.omoSlim.builtInAgents') }} <span>{{ builtInAgentNames.length }}</span></div>
              <nav class="omo-slim-agent-nav" aria-label="agents">
                <button v-for="name in builtInAgentNames" :key="name" type="button" class="omo-slim-nav-item" :class="{ active: selectedSection === 'agent' && selectedAgent === name }" @click="selectedSection = 'agent'; selectedAgent = name">
                  <span class="omo-slim-nav-item-main"><span class="text-mono" :class="{ 'omo-slim-disabled-name': isAgentDisabled(name) }">{{ name }}</span><span v-if="isAgentDisabled(name)" class="badge warn">{{ t('toolAccess.omoSlim.disabledShort') }}</span></span>
                  <span v-if="agentDescription(name)" class="omo-slim-nav-description">{{ agentDescription(name) }}</span>
                  <label class="toggle toggle-sm omo-slim-nav-toggle" :aria-label="t(isAgentDisabled(name) ? 'toolAccess.omoSlim.enableAgent' : 'toolAccess.omoSlim.disableAgent', { name })" @click.stop>
                    <input type="checkbox" :checked="!isAgentDisabled(name)" @change="toggleDisabled(name)"><span class="toggle-slider blue"/>
                  </label>
                </button>
              </nav>

              <div class="row-between omo-slim-nav-label omo-slim-custom-label"><span>{{ t('toolAccess.omoSlim.customAgents') }} <span>{{ customAgents.length }}</span></span><button class="btn btn-icon btn-sm" :title="t('toolAccess.omoSlim.newAgent')" :aria-label="t('toolAccess.omoSlim.newAgent')" @click="addCustomAgent"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg></button></div>
              <nav v-if="customAgents.length" class="omo-slim-agent-nav">
                <button v-for="(agent, index) in customAgents" :key="`${agent.name}-${index}`" type="button" class="omo-slim-nav-item" :class="{ active: selectedSection === 'custom' && selectedCustomIndex === index }" @click="selectCustom(index)">
                  <span class="omo-slim-nav-item-main"><span class="text-mono" :class="{ 'omo-slim-disabled-name': isAgentDisabled(agent.name) }">{{ agent.name || t('toolAccess.omoSlim.newAgent') }}</span><span v-if="isAgentDisabled(agent.name)" class="badge warn">{{ t('toolAccess.omoSlim.disabledShort') }}</span></span>
                  <label class="toggle toggle-sm omo-slim-nav-toggle" :aria-label="t(isAgentDisabled(agent.name) ? 'toolAccess.omoSlim.enableAgent' : 'toolAccess.omoSlim.disableAgent', { name: agent.name || t('toolAccess.omoSlim.newAgent') })" @click.stop>
                    <input type="checkbox" :checked="!isAgentDisabled(agent.name)" @change="toggleDisabled(agent.name)"><span class="toggle-slider blue"/>
                  </label>
                </button>
              </nav>
              <div v-else class="field-help omo-slim-empty-nav">{{ t('toolAccess.omoSlim.noCustomAgents') }}</div>
              <div v-if="otherDisabledAgents.length" class="omo-slim-other-disabled">
                <div class="omo-slim-nav-label">{{ t('toolAccess.omoSlim.otherDisabled') }}</div>
                <div class="tri-state-tags">
                  <button v-for="name in otherDisabledAgents" :key="name" type="button" class="badge mono tri-state-tag" :title="t('toolAccess.omoSlim.reenableAgent', { name })" @click="toggleDisabled(name)">
                    {{ name }}
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>
                  </button>
                </div>
              </div>

              <button type="button" class="omo-slim-global-nav" :class="{ active: selectedSection === 'global' }" @click="selectedSection = 'global'"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M4 7h16M4 12h16M4 17h16"/><circle cx="9" cy="7" r="2" fill="var(--surface)"/><circle cx="15" cy="12" r="2" fill="var(--surface)"/><circle cx="11" cy="17" r="2" fill="var(--surface)"/></svg>{{ t('toolAccess.omoSlim.globalSettings') }}</button>
            </aside>

            <main class="omo-slim-editor">
              <template v-if="selectedSection === 'agent' && selectedBuiltIn">
                <div class="omo-slim-editor-header"><div><h3>{{ selectedAgent }}</h3><p>{{ agentDescription(selectedAgent) }}</p></div><span class="badge" :class="isAgentDisabled(selectedAgent) ? 'warn' : 'success'">{{ isAgentDisabled(selectedAgent) ? t('toolAccess.omoSlim.disabledShort') : t('toolAccess.omoSlim.enabled') }}</span></div>
                <div class="omo-slim-editor-grid">
                  <div class="field"><label class="field-label">{{ t('toolAccess.omoSlim.model') }}</label><AutoComplete v-model="selectedBuiltIn.model" :options="validModels" :placeholder="t('toolAccess.omoSlim.modelPlaceholder')" @update:model-value="markAgentDirty(selectedAgent)"/><div v-if="isModelUnknown(selectedBuiltIn.model)" class="omo-slim-model-warning">{{ t('toolAccess.omoSlim.modelUnknown') }}</div></div>
                  <div class="field"><label class="field-label">{{ t('toolAccess.omoSlim.variant') }}</label><select v-model="selectedBuiltIn.variant" class="select" @change="markAgentDirty(selectedAgent)"><option value="">{{ t('toolAccess.omoSlim.variantNone') }}</option><option v-for="variant in variants" :key="variant" :value="variant">{{ variant }}</option></select></div>
                  <div class="field omo-slim-field-wide"><label class="field-label">{{ t('toolAccess.omoSlim.displayName') }}</label><input v-model="selectedBuiltIn.displayName" class="input" :placeholder="t('toolAccess.omoSlim.displayNamePlaceholder')" @input="markAgentDirty(selectedAgent)"></div>
                </div>
                <div class="omo-slim-editor-section"><TriStateTagEditor v-model:mode="selectedBuiltIn.skills.mode" v-model:items="selectedBuiltIn.skills.items" :label="t('toolAccess.omoSlim.skills')" :options="knownSkills" @change="markAgentDirty(selectedAgent)"/><TriStateTagEditor v-model:mode="selectedBuiltIn.mcps.mode" v-model:items="selectedBuiltIn.mcps.items" :label="t('toolAccess.omoSlim.mcps')" :options="knownMcps" @change="markAgentDirty(selectedAgent)"/></div>
              </template>

              <template v-else-if="selectedSection === 'custom' && selectedCustom">
                <div class="omo-slim-editor-header"><div><h3>{{ selectedCustom.name || t('toolAccess.omoSlim.newAgent') }}</h3><p>{{ t('toolAccess.omoSlim.customAgentHelp') }}</p></div><button class="btn btn-danger-ghost btn-sm" type="button" @click="deleteCustomAgent(selectedCustomIndex)">{{ t('common.delete') }}</button></div>
                <div class="omo-slim-editor-grid">
                  <div class="field omo-slim-field-wide"><label class="field-label">{{ t('toolAccess.omoSlim.agentName') }}</label><input v-model="selectedCustom.name" class="input mono" :readonly="!selectedCustom.isNew" :placeholder="t('toolAccess.omoSlim.agentNamePlaceholder')" @input="customNameChanged(selectedCustom)"><div v-if="!selectedCustom.isNew" class="field-help">{{ t('toolAccess.omoSlim.agentNameReadonly') }}</div></div>
                  <div class="field"><label class="field-label">{{ t('toolAccess.omoSlim.model') }}</label><AutoComplete v-model="selectedCustom.model" :options="validModels" :placeholder="t('toolAccess.omoSlim.modelPlaceholder')" @update:model-value="customFieldChanged"/><div v-if="isModelUnknown(selectedCustom.model)" class="omo-slim-model-warning">{{ t('toolAccess.omoSlim.modelUnknown') }}</div></div>
                  <div class="field"><label class="field-label">{{ t('toolAccess.omoSlim.variant') }}</label><select v-model="selectedCustom.variant" class="select" @change="customFieldChanged"><option value="">{{ t('toolAccess.omoSlim.variantNone') }}</option><option v-for="variant in variants" :key="variant" :value="variant">{{ variant }}</option></select></div>
                  <div class="field omo-slim-field-wide"><label class="field-label">{{ t('toolAccess.omoSlim.displayName') }}</label><input v-model="selectedCustom.displayName" class="input" :placeholder="t('toolAccess.omoSlim.displayNamePlaceholder')" @input="customFieldChanged"></div>
                </div>
                <div class="omo-slim-editor-section"><TriStateTagEditor v-model:mode="selectedCustom.skills.mode" v-model:items="selectedCustom.skills.items" :label="t('toolAccess.omoSlim.skills')" :options="knownSkills" @change="customFieldChanged"/><TriStateTagEditor v-model:mode="selectedCustom.mcps.mode" v-model:items="selectedCustom.mcps.items" :label="t('toolAccess.omoSlim.mcps')" :options="knownMcps" @change="customFieldChanged"/></div>
                <div class="omo-slim-textarea-grid"><div class="field"><label class="field-label">{{ t('toolAccess.omoSlim.prompt') }}</label><textarea v-model="selectedCustom.prompt" class="input omo-slim-textarea" :placeholder="t('toolAccess.omoSlim.promptPlaceholder')" @input="customFieldChanged"/></div><div class="field"><label class="field-label">{{ t('toolAccess.omoSlim.orchestratorPrompt') }}</label><textarea v-model="selectedCustom.orchestratorPrompt" class="input omo-slim-textarea" :placeholder="t('toolAccess.omoSlim.orchestratorPromptPlaceholder')" @input="customFieldChanged"/><div class="field-help">{{ t('toolAccess.omoSlim.orchestratorPromptHelp', { name: selectedCustom.name || 'agent' }) }}</div><div v-if="selectedCustom.orchestratorPrompt.trim() && !selectedCustom.orchestratorPrompt.trim().startsWith(`@${selectedCustom.name.trim()}`)" class="omo-slim-model-warning">{{ t('toolAccess.omoSlim.orchestratorPromptWarning') }}</div></div></div>
              </template>

              <template v-else>
                <div class="omo-slim-editor-header"><div><h3>{{ t('toolAccess.omoSlim.globalSettings') }}</h3><p>{{ t('toolAccess.omoSlim.globalSettingsHelp') }}</p></div></div>
                <div class="omo-slim-global-editors">
                  <div v-for="kind in (['agents', 'skills', 'mcps'] as GlobalKind[])" :key="kind" class="omo-slim-global-editor">
                    <label class="field-label">{{ t(`toolAccess.omoSlim.globalDisabled.${kind}`) }}</label>
                    <div class="row tri-state-input-row"><AutoComplete v-model="globalInputs[kind]" :options="kind === 'agents' ? allAgentNames : kind === 'skills' ? knownSkills : knownMcps" :placeholder="t('toolAccess.omoSlim.addTagPlaceholder')" @keydown.enter.prevent="addGlobalTag(kind)"/><button class="btn btn-secondary tri-state-add" type="button" :title="t('toolAccess.omoSlim.addTag')" @click="addGlobalTag(kind)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg></button></div>
                    <div class="tri-state-tags"><button v-for="item in (kind === 'agents' ? disabledAgents : kind === 'skills' ? disabledSkills : disabledMcps)" :key="item" type="button" class="badge mono tri-state-tag" :title="t('toolAccess.omoSlim.removeTag')" @click="removeGlobalTag(kind, item)">{{ item }}<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div>
                  </div>
                </div>
              </template>
            </main>
          </div>

          <div class="omo-slim-workbench-footer"><span class="field-help">{{ t('toolAccess.omoSlim.previewBeforeWrite') }}</span><div class="row"><button class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button><button class="btn btn-primary" :disabled="previewLoading" @click="previewChange">{{ previewLoading ? t('toolAccess.omoSlim.previewLoading') : t('toolAccess.omoSlim.previewChanges') }}</button></div></div>
        </template>
      </div>
    </div>

    <div v-if="previewOpen && previewData" class="modal-overlay modal-overlay-stacked omo-slim-preview-overlay" @click.self="previewOpen = false">
      <div class="modal-card omo-slim-preview-modal">
        <div class="row-between modal-heading"><div><div class="modal-title">{{ t('toolAccess.omoSlim.previewTitle') }}</div><div class="section-sub text-mono omo-slim-path">{{ previewData.Path }}</div></div><button class="btn btn-icon" :title="t('common.close')" :aria-label="t('common.close')" @click="previewOpen = false"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg></button></div>
        <div class="omo-slim-preview-note">{{ t('toolAccess.omoSlim.previewBackupNote') }}</div>
        <DiffPreview class="omo-slim-diff-preview" :before="previewData.Before" :after="previewData.After" />
        <div class="row omo-slim-preview-actions"><button class="btn btn-secondary" :disabled="saving" @click="previewOpen = false">{{ t('toolAccess.omoSlim.cancelPreview') }}</button><button class="btn btn-primary" :disabled="saving" @click="confirmWrite()">{{ saving ? t('common.processing') : t('toolAccess.omoSlim.confirmWrite') }}</button></div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.omo-slim-workbench { width: 92vw; max-width: 1200px; height: 86vh; max-height: 900px; display: flex; flex-direction: column; overflow: hidden; }
.omo-slim-workbench-heading { flex: 0 0 auto; align-items: flex-start; margin-bottom: 14px; }
.omo-slim-path { max-width: 80vw; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.omo-slim-workbench-body { min-height: 0; flex: 1 1 auto; display: grid; grid-template-columns: 272px minmax(0, 1fr); border: 1px solid var(--border); border-radius: var(--radius-sm); overflow: hidden; }
.omo-slim-sidebar { min-width: 0; padding: 14px 12px; border-right: 1px solid var(--border); background: color-mix(in srgb, var(--bg) 44%, var(--surface)); overflow-y: auto; overflow-x: hidden; }
.omo-slim-preset-field { padding-bottom: 13px; border-bottom: 1px solid var(--border); }
.omo-slim-preset-heading { align-items: center; gap: 8px; }
.omo-slim-add-preset { min-height: 29px; padding: 5px 8px; font-size: 11px; }
.omo-slim-add-preset svg { width: 13px; height: 13px; }
.omo-slim-preset-list { display: flex; flex-direction: column; gap: 2px; margin-top: 7px; }
.omo-slim-preset-row { display: flex; align-items: center; gap: 3px; min-width: 0; padding: 3px 4px 3px 7px; border: 1px solid transparent; border-radius: var(--radius-sm); }
.omo-slim-preset-row.active { border-color: color-mix(in srgb, var(--accent) 45%, var(--border)); background: var(--accent-soft); }
.omo-slim-preset-name { min-width: 0; flex: 1; display: flex; align-items: center; gap: 6px; padding: 6px 2px; border: none; background: transparent; color: var(--fg); font: inherit; text-align: left; cursor: pointer; }
.omo-slim-preset-name .text-mono { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.omo-slim-preset-actions { display: flex; flex: 0 0 auto; gap: 0; }
.omo-slim-preset-actions .btn { color: var(--muted); }
.omo-slim-preset-actions .btn:hover { color: var(--fg); }
.omo-slim-preset-actions .btn.danger-icon:hover { color: var(--negative); }
.omo-slim-preset-rename-input { min-width: 0; flex: 1; min-height: 30px; padding: 5px 7px; font-size: 11.5px; }
.omo-slim-preset-error { margin-top: 5px; color: var(--negative); font-size: 10.5px; line-height: 1.35; }
.omo-slim-empty-presets { padding: 8px 4px 2px; }
.omo-slim-switch-summary { margin: 11px 0 14px; padding: 9px; border: 1px solid color-mix(in srgb, var(--accent) 35%, var(--border)); border-radius: var(--radius-sm); background: color-mix(in srgb, var(--accent-soft) 42%, var(--surface)); font-size: 11px; }
.omo-slim-summary-list { display: flex; flex-direction: column; gap: 2px; margin-top: 7px; }
.omo-slim-summary-row { display: flex; justify-content: space-between; gap: 7px; padding: 4px 5px; color: var(--muted); border-radius: var(--radius-xs); }
.omo-slim-summary-row span { min-width: 0; max-width: 55%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.omo-slim-summary-row.changed { color: var(--fg); background: color-mix(in srgb, var(--warning) 12%, transparent); }
.omo-slim-nav-label { display: flex; justify-content: space-between; align-items: center; margin: 12px 4px 5px; color: var(--muted); font-size: 10.5px; font-weight: 600; text-transform: uppercase; letter-spacing: .03em; }
.omo-slim-custom-label { margin-top: 17px; }
.omo-slim-custom-label .btn { color: var(--muted); }
.omo-slim-custom-label .btn svg { width: 14px; height: 14px; }
.omo-slim-agent-nav { display: flex; flex-direction: column; gap: 2px; }
.omo-slim-nav-item { position: relative; display: flex; flex-direction: column; align-items: stretch; gap: 3px; min-width: 0; padding: 8px 36px 8px 9px; border: 1px solid transparent; border-radius: var(--radius-sm); background: transparent; color: var(--fg); text-align: left; cursor: pointer; }
.omo-slim-nav-item:hover { background: color-mix(in srgb, var(--surface) 70%, transparent); }
.omo-slim-nav-item.active { border-color: color-mix(in srgb, var(--accent) 45%, var(--border)); background: var(--accent-soft); }
.omo-slim-nav-item-main { display: flex; align-items: center; gap: 6px; min-width: 0; }
.omo-slim-nav-item-main .text-mono, .omo-slim-nav-item > .text-mono { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.omo-slim-nav-description { overflow: hidden; color: var(--muted); font-size: 10px; line-height: 1.3; text-overflow: ellipsis; white-space: nowrap; }
.omo-slim-nav-toggle { position: absolute; top: 11px; right: 8px; }
.omo-slim-disabled-name { color: var(--muted); text-decoration: line-through; }
.omo-slim-empty-nav { padding: 4px 4px 8px; }
.omo-slim-other-disabled { margin-top: 14px; padding-top: 2px; }
.omo-slim-other-disabled .omo-slim-nav-label { margin-top: 0; }
.omo-slim-global-nav { display: flex; align-items: center; gap: 8px; width: 100%; margin-top: 18px; padding: 9px; border: 1px solid transparent; border-radius: var(--radius-sm); background: transparent; color: var(--muted); font: inherit; font-size: 12px; text-align: left; cursor: pointer; }
.omo-slim-global-nav:hover, .omo-slim-global-nav.active { border-color: var(--border); background: var(--surface); color: var(--fg); }
.omo-slim-global-nav svg { width: 15px; height: 15px; }
.omo-slim-editor { min-width: 0; padding: 21px 24px; overflow-y: auto; overflow-x: hidden; }
.omo-slim-editor-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 14px; margin-bottom: 20px; }
.omo-slim-editor-header h3 { margin: 0; font-size: 17px; font-weight: 650; }
.omo-slim-editor-header p { margin: 5px 0 0; color: var(--muted); font-size: 12px; line-height: 1.45; }
.omo-slim-editor-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 15px 16px; }
.omo-slim-field-wide { grid-column: 1 / -1; }
.omo-slim-editor-section { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 18px; margin-top: 22px; padding-top: 18px; border-top: 1px solid var(--border); }
.omo-slim-textarea-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; margin-top: 22px; padding-top: 18px; border-top: 1px solid var(--border); }
.omo-slim-textarea { min-height: 150px; resize: vertical; line-height: 1.5; }
.omo-slim-model-warning { margin-top: 4px; color: var(--warning); font-size: 11px; line-height: 1.35; }
.omo-slim-global-editors { display: flex; flex-direction: column; gap: 20px; max-width: 720px; }
.omo-slim-global-editor { display: flex; flex-direction: column; gap: 7px; }
.omo-slim-workbench-footer { display: flex; align-items: center; justify-content: space-between; gap: 16px; flex: 0 0 auto; padding-top: 13px; }
.omo-slim-workbench-footer .field-help { min-width: 0; }
.omo-slim-state { padding: 45px 0; text-align: center; }
.tool-inline-error { display: flex; flex-direction: column; gap: 8px; padding: 12px; border-radius: var(--radius-sm); background: rgba(217, 48, 37, .08); color: var(--negative); font-size: 12px; }
.tool-inline-error .btn { align-self: flex-start; color: var(--fg); }
.omo-slim-preview-overlay { isolation: isolate; }
.omo-slim-preview-modal { width: min(920px, 90vw); height: min(84vh, 820px); max-height: 84vh; display: flex; flex-direction: column; overflow: hidden; }
.omo-slim-preview-modal .modal-heading { flex: 0 0 auto; align-items: flex-start; margin-bottom: 15px; }
.omo-slim-preview-note { flex: 0 0 auto; margin-bottom: 12px; padding: 9px 11px; border: 1px solid color-mix(in srgb, var(--accent) 28%, var(--border)); border-radius: var(--radius-sm); background: var(--accent-soft); color: var(--muted); font-size: 12px; }
.omo-slim-diff-preview { min-height: 0; flex: 1 1 auto; }
.omo-slim-preview-actions { justify-content: flex-end; margin-top: 14px; flex: 0 0 auto; }
@media (max-width: 900px) {
  .omo-slim-workbench { width: 96vw; height: 90vh; }
  .omo-slim-preview-modal { width: 94vw; height: 84vh; }
  .omo-slim-workbench-body { grid-template-columns: 220px minmax(0, 1fr); }
  .omo-slim-editor { padding: 17px; }
  .omo-slim-editor-section, .omo-slim-textarea-grid { grid-template-columns: 1fr; }
}
@media (max-width: 680px) {
  .omo-slim-workbench { width: 100%; height: 100%; max-height: none; border-radius: 0; }
  .omo-slim-preview-modal { width: 100%; height: 100%; max-height: none; border-radius: 0; }
  .omo-slim-workbench-body { grid-template-columns: 1fr; overflow: auto; }
  .omo-slim-sidebar { max-height: 250px; border-right: none; border-bottom: 1px solid var(--border); }
  .omo-slim-editor-grid { grid-template-columns: 1fr; }
  .omo-slim-workbench-footer { align-items: flex-end; flex-direction: column; }
  .omo-slim-workbench-footer .row { width: 100%; justify-content: flex-end; }
}
</style>
