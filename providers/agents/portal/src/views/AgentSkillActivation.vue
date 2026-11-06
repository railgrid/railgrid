<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Check, Circle, Loader2, Plug, Search, X } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import Tabs from '../portalkit/Tabs.vue'
import { filterAssistantSkills } from '../agentkit/composerSkillsSearch'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import { ensureAgentUIStyles } from '../agentkit/styles'
import { useAgentDOMID, useAuthorityGuard, useStoreRevision } from '../vue/runtime'
import type { AppStore } from '../store'
import type { AgentSkill, AgentSkillCatalog } from '../types'

ensureAgentUIStyles()

const props = defineProps<{ store: AppStore; api: ApiClient; name: string; authorityEpoch?: number }>()
const domID = useAgentDOMID()
const revision = useStoreRevision(() => props.store)
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)
const catalog = ref<AgentSkillCatalog>({ skills: [], warnings: [] })
const query = ref('')
const view = ref<'enabled' | 'available'>('enabled')
function addSkills() { view.value = 'available'; query.value = ''; searchInput.value?.focus() }
const searchInput = ref<HTMLInputElement | null>(null)
function clearSearch() { query.value = ''; searchInput.value?.focus() }
const loading = ref(false)
const error = ref('')
const busySkill = ref('')
let requestSerial = 0

const agent = computed(() => { revision.value; return props.store.agent(props.name) })
const enabledIDs = computed(() => new Set((agent.value?.spec.skills || []).map((item) => item.name)))
const missingSkillIDs = computed(() => [...enabledIDs.value].filter((id) => !catalog.value.skills.some((skill) => skill.id === id)))
const availableSkills = computed(() => catalog.value.skills.filter(skill => !enabledIDs.value.has(skill.id)))
const filteredSkills = computed(() => filterAssistantSkills(
  [...catalog.value.skills].filter(skill => view.value === 'enabled' ? enabledIDs.value.has(skill.id) : !enabledIDs.value.has(skill.id))
    .sort((left, right) => left.name.localeCompare(right.name) || left.id.localeCompare(right.id)), query.value))
const filteredMissingSkillIDs = computed(() => view.value === 'enabled' ? missingSkillIDs.value.filter(id => !query.value.trim() || id.toLocaleLowerCase().includes(query.value.trim().toLocaleLowerCase())) : [])

async function load(): Promise<void> {
  const serial = ++requestSerial
  const name = props.name
  const api = props.api
  const authority = captureAuthority()
  loading.value = true
  error.value = ''
  try {
    const result = await api.getAgentSkillCatalog(name)
    if (serial !== requestSerial || props.name !== name || props.api !== api || !authorityIsCurrent(authority)) return
    catalog.value = result
  } catch (reason) {
    if (serial === requestSerial && props.name === name && props.api === api && authorityIsCurrent(authority)) error.value = (reason as Error).message
  } finally {
    if (serial === requestSerial) loading.value = false
  }
}

async function updateEnabled(skillID: string, skillName: string, enabled: boolean): Promise<void> {
  if (busySkill.value) return
  const current = [...(agent.value?.spec.skills || [])]
  const next = enabled
    ? [...current.filter((item) => item.name !== skillID), { name: skillID }]
    : current.filter((item) => item.name !== skillID)
  const authority = captureAuthority()
  const name = props.name
  busySkill.value = skillID
  try {
    const updated = await mutate(authority.store, {
      run: () => authority.api.patchAgent(name, { skills: next }),
      success: enabled ? `Enabled “${skillName}” for this agent.` : `Disabled “${skillName}” for this agent.`,
      failure: 'Could not update agent skills',
      reload: ['agents'],
    })
    if (updated && authorityIsCurrent(authority) && props.name === name) await load()
  } finally {
    if (busySkill.value === skillID) busySkill.value = ''
  }
}

function setEnabled(skill: Pick<AgentSkill, 'id' | 'name'>, enabled: boolean): Promise<void> {
  return updateEnabled(skill.id, skill.name, enabled)
}

watch([() => props.name, () => props.api, () => props.authorityEpoch], () => { void load() }, { immediate: true })
</script>

<template>
  <ResourceSectionCard class="agents-config-sec agents-skill-activation" variant="document" :heading-id="domID('agent-skills-activation-heading')" :title="view === 'enabled' ? 'Enabled skills' : 'Available skills'" description="Enable skills for this agent. Chat can select from the enabled set for each turn.">
    <template #actions><button v-if="view === 'enabled'" type="button" class="k-btn k-btn--primary" @click="addSkills">Add skills</button></template>
    <Tabs :tabs="[{ id: 'enabled', label: 'Enabled', count: enabledIDs.size }, { id: 'available', label: 'Available', count: availableSkills.length }]" :active="view" aria-label="Agent skill lists" @select="view = $event as 'enabled' | 'available'" />
    <label class="k-ai-search k-ai-search--clearable">
      <Search class="k-ai-search__icon" :stroke-width="1.75" aria-hidden="true" />
      <input ref="searchInput" v-model="query" type="search" class="k-input k-ai-search__input" placeholder="Search skills" :aria-label="`Search skills for ${name}`" />
      <button v-if="query" type="button" class="k-ai-search__clear" aria-label="Clear skill search" @click="clearSearch"><X :stroke-width="1.75" aria-hidden="true" /></button>
    </label>
    <div v-if="error" class="agents-state agents-state-error" role="alert">
      <span>Could not load skills. {{ error }}</span>
      <button class="k-btn k-btn--ghost secondary" type="button" :disabled="loading" @click="load">{{ loading ? 'Retrying…' : 'Retry' }}</button>
    </div>
    <p v-else-if="loading && !catalog.skills.length" class="agents-state agents-state-loading" role="status"><Loader2 class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" /> Loading skills…</p>
    <div v-else-if="filteredSkills.length || filteredMissingSkillIDs.length" class="agents-skill-activation__list">
      <div v-for="id in filteredMissingSkillIDs" :key="`missing:${id}`" class="agents-tool-row agents-skill-activation__row">
        <div class="agents-skill-activation__summary">
          <div class="flex min-w-0 items-center gap-2">
            <Circle class="h-4 w-4 shrink-0 text-warning" aria-hidden="true" />
            <strong class="truncate text-[13px]">Unavailable skill</strong>
            <span class="k-badge agents-badge">Enabled</span>
          </div>
          <p class="mt-0.5 text-[11px] text-text-secondary">This enabled reference is missing from the current catalog. Remove it or restore the skill.</p>
          <code class="block truncate text-[10px] text-text-muted" :title="id">{{ id }}</code>
        </div>
        <button type="button" class="k-btn k-btn--ghost secondary shrink-0 agents-skill-activation__action" :disabled="Boolean(busySkill)" :aria-busy="busySkill === id ? 'true' : undefined" @click="updateEnabled(id, id, false)">
          <Loader2 v-if="busySkill === id" class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" />
          <X v-else class="agents-skill-activation__button-icon" aria-hidden="true" />
          {{ busySkill === id ? 'Saving…' : 'Remove' }}
        </button>
      </div>
      <div v-for="skill in filteredSkills" :key="skill.id" class="agents-tool-row agents-skill-activation__row">
        <div class="agents-skill-activation__summary">
          <div class="flex min-w-0 items-center gap-2">
            <Plug class="h-4 w-4 shrink-0" :stroke-width="1.75" aria-hidden="true" />
            <strong class="truncate text-[13px]">{{ skill.name }}</strong>
            <span class="k-badge agents-badge">{{ skill.scope }}</span>
            <span v-if="!skill.editable" class="k-badge agents-badge">Read-only</span>
          </div>
          <p class="mt-0.5 truncate text-[11px] text-text-secondary">{{ skill.description || 'No description provided.' }}</p>
          <code class="block truncate text-[10px] text-text-muted" :title="skill.id">{{ skill.id }}</code>
        </div>
        <button
          type="button"
          class="k-btn k-btn--ghost secondary shrink-0 agents-skill-activation__action"
          :disabled="Boolean(busySkill)"
          :aria-label="`${enabledIDs.has(skill.id) ? 'Disable' : 'Enable'} ${skill.name} for ${name}`"
          :aria-busy="busySkill === skill.id ? 'true' : undefined"
          @click="setEnabled(skill, !enabledIDs.has(skill.id))"
        >
          <Loader2 v-if="busySkill === skill.id" class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" />
          <Check v-else-if="enabledIDs.has(skill.id)" class="agents-skill-activation__button-icon" aria-hidden="true" />
          <Circle v-else class="agents-skill-activation__button-icon" aria-hidden="true" />
          {{ busySkill === skill.id ? 'Saving…' : enabledIDs.has(skill.id) ? 'Disable' : 'Enable' }}
        </button>
      </div>
    </div>
    <p v-else-if="query.trim()" class="agents-hint" role="status">No skills match this search.</p>
    <div v-else class="agents-state agents-state-empty" role="status">
      <p>{{ view === 'enabled' ? 'No skills are enabled for this agent.' : 'No additional skills are available.' }}</p>
      <p>{{ view === 'enabled' ? 'Add skills to make them available in chat.' : 'Create or import reusable skills in the Skills library.' }}</p>
    </div>
    <p v-for="warning in catalog.warnings || []" :key="warning" class="agents-hint" role="status">{{ warning }}</p>
  </ResourceSectionCard>
</template>
