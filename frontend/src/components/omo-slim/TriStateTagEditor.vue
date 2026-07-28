<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AutoComplete from '@/components/AutoComplete.vue'

type Mode = 'inherit' | 'all' | 'custom'

const props = defineProps<{
  label: string
  mode: Mode
  items: string[]
  options: string[]
}>()

const emit = defineEmits<{
  'update:mode': [value: Mode]
  'update:items': [value: string[]]
  change: []
}>()

const { t } = useI18n()
const input = ref('')

function setMode(mode: Mode) {
  emit('update:mode', mode)
  emit('change')
}

function addTag() {
  const value = input.value.trim()
  if (!value) return
  const normalized = props.mode === 'all' ? value.replace(/^!+/, '') : value
  if (!normalized) return
  if (!props.items.includes(normalized)) {
    emit('update:items', [...props.items, normalized])
    emit('change')
  }
  input.value = ''
}

function removeTag(value: string) {
  emit('update:items', props.items.filter((item) => item !== value))
  emit('change')
}
</script>

<template>
  <div class="tri-state-editor">
    <div class="row-between tri-state-heading">
      <label class="field-label">{{ label }}</label>
      <select :value="mode" class="select tri-state-select" @change="setMode(($event.target as HTMLSelectElement).value as Mode)">
        <option value="inherit">{{ t('toolAccess.omoSlim.triState.inherit') }}</option>
        <option value="all">{{ t('toolAccess.omoSlim.triState.all') }}</option>
        <option value="custom">{{ t('toolAccess.omoSlim.triState.custom') }}</option>
      </select>
    </div>
    <div v-if="mode === 'inherit'" class="field-help">{{ t('toolAccess.omoSlim.triState.inheritHelp') }}</div>
    <template v-else>
      <div class="row tri-state-input-row">
        <AutoComplete v-model="input" :options="options" :placeholder="mode === 'all' ? t('toolAccess.omoSlim.triState.excludePlaceholder') : t('toolAccess.omoSlim.triState.addPlaceholder')" @keydown.enter.prevent="addTag" />
        <button class="btn btn-secondary tri-state-add" type="button" :title="t('toolAccess.omoSlim.triState.add')" :aria-label="t('toolAccess.omoSlim.triState.add')" @click="addTag">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>
        </button>
      </div>
      <div v-if="mode === 'all'" class="field-help">{{ t('toolAccess.omoSlim.triState.allHelp') }}</div>
      <div v-if="mode === 'all' || items.length" class="tri-state-tags">
        <span v-if="mode === 'all'" class="badge info tri-state-tag">*</span>
        <button v-for="item in items" :key="item" type="button" class="badge mono tri-state-tag" :title="t('toolAccess.omoSlim.triState.remove')" @click="removeTag(item)">
          <span>{{ mode === 'all' ? `!${item}` : item }}</span>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>
        </button>
      </div>
      <div v-else class="field-help">{{ t('toolAccess.omoSlim.triState.emptyCustom') }}</div>
    </template>
  </div>
</template>

<style scoped>
.tri-state-editor { display: flex; flex-direction: column; gap: 6px; }
.tri-state-heading { align-items: center; gap: 8px; }
.tri-state-select { width: auto; min-width: 112px; min-height: 30px; padding-top: 5px; padding-bottom: 5px; font-size: 11px; }
.tri-state-input-row { gap: 6px; }
.tri-state-input-row > :first-child { min-width: 0; flex: 1; }
.tri-state-add { width: 30px; height: 30px; padding: 0; border-radius: var(--radius-sm); flex: 0 0 auto; }
.tri-state-add svg { width: 14px; height: 14px; }
.tri-state-tags { display: flex; flex-wrap: wrap; gap: 5px; }
.tri-state-tag { border: none; cursor: pointer; }
.tri-state-tag svg { width: 11px; height: 11px; margin-left: 2px; }
</style>
