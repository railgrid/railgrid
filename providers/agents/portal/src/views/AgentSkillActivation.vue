<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Loader2, Search, TriangleAlert, X } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import Tabs from '../portalkit/Tabs.vue'
import { filterAssistantSkills } from '../agentkit/composerSkillsSearch'
import { ensureAgentUIStyles } from '../agentkit/styles'
import ConfigSaveFeedback, { type ConfigSaveStatus } from '../components/ConfigSaveFeedback.vue'
import { useAuthorityGuard, useStoreRevision } from '../vue/runtime'
import { toast } from '../ui/toast'
import type { AppStore } from '../store'
import type { AgentSkill, AgentSkillCatalog } from '../types'

ensureAgentUIStyles()

const props = withDefaults(defineProps<{
  store: AppStore
  api: ApiClient
  name: string
  authorityEpoch?: number
  active?: boolean
}>(), { active: true })
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
const saveStatus = ref<ConfigSaveStatus>('idle')
const saveError = ref('')
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
  const authority = captureAuthority()
  const name = props.name
  const ownsPane = () => authorityIsCurrent(authority) && props.name === name
  busySkill.value = skillID
  saveStatus.value = 'pending'
  saveError.value = ''
  try {
    const updated = await mutate(authority.store, {
      run: () => authority.api.setAgentSkillEnabled(name, skillID, enabled),
      failure: 'Could not update agent skills',
      reload: ['agents'],
      onFailure: reason => {
        if (!props.active || !ownsPane()) return false
        saveError.value = `Could not ${enabled ? 'enable' : 'disable'} “${skillName}”. ${(reason as Error).message}`
        saveStatus.value = 'error'
        return true
      },
    })
    if (!updated || authority.store.isRetired()) return
    if (props.active && ownsPane()) saveStatus.value = 'saved'
    else toast('ok', `${enabled ? 'Enabled' : 'Disabled'} “${skillName}” for this agent.`)
    if (ownsPane()) await load()
  } finally {
    if (busySkill.value === skillID) {
      busySkill.value = ''
      if (saveStatus.value === 'pending') saveStatus.value = 'idle'
    }
  }
}

function setEnabled(skill: Pick<AgentSkill, 'id' | 'name'>, enabled: boolean): Promise<void> {
  return updateEnabled(skill.id, skill.name, enabled)
}

watch([() => props.name, () => props.api, () => props.authorityEpoch], () => {
  saveStatus.value = 'idle'
  saveError.value = ''
  void load()
}, { immediate: true })
</script>

<template>
  <div class="agents-skill-activation">
    <Tabs :tabs="[{ id: 'enabled', label: 'Enabled', count: enabledIDs.size }, { id: 'available', label: 'Available', count: availableSkills.length }]" :active="view" aria-label="Agent skill lists" @select="view = $event as 'enabled' | 'available'" />
    <div class="agents-skill-activation__toolbar">
      <label class="k-ai-search k-ai-search--clearable agents-skill-activation__search">
        <Search class="k-ai-search__icon" :stroke-width="1.75" aria-hidden="true" />
        <input ref="searchInput" v-model="query" type="search" class="k-input k-ai-search__input" placeholder="Search skills" :aria-label="`Search skills for ${name}`" />
        <button v-if="query" type="button" class="k-ai-search__clear" aria-label="Clear skill search" @click="clearSearch"><X :stroke-width="1.75" aria-hidden="true" /></button>
      </label>
      <button v-if="view === 'enabled'" type="button" class="k-btn k-btn--primary" @click="addSkills">Add skills</button>
    </div>
    <p class="agents-hint">Changes save automatically.</p>
    <ConfigSaveFeedback action="skills" :status="saveStatus" :error="saveError" />
    <div v-if="error" class="agents-state agents-state-error" role="alert">
      <span>Could not load skills. {{ error }}</span>
      <button class="k-btn k-btn--ghost secondary" type="button" :disabled="loading" @click="load">{{ loading ? 'Retrying…' : 'Retry' }}</button>
    </div>
    <p v-else-if="loading && !catalog.skills.length" class="agents-state agents-state-loading" role="status"><Loader2 class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" /> Loading skills…</p>
    <ul v-else-if="filteredSkills.length || filteredMissingSkillIDs.length" class="agents-skill-activation__list" role="list" :aria-label="view === 'enabled' ? 'Enabled skills' : 'Available skills'">
      <li v-for="id in filteredMissingSkillIDs" :key="`missing:${id}`" class="agents-skill-activation__row">
        <div class="agents-skill-activation__summary">
          <div class="agents-skill-activation__name">
            <TriangleAlert :size="14" class="text-warning" aria-hidden="true" />
            <strong>Unavailable skill</strong>
            <span class="k-badge agents-badge">Enabled</span>
          </div>
          <p>This enabled reference is missing from the current catalog. Remove it or restore the skill.</p>
          <code>{{ id }}</code>
        </div>
        <button type="button" class="k-btn k-btn--ghost secondary shrink-0 agents-skill-activation__action" :disabled="Boolean(busySkill)" :aria-busy="busySkill === id ? 'true' : undefined" @click="updateEnabled(id, id, false)">
          <Loader2 v-if="busySkill === id" class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" />
          {{ busySkill === id ? 'Saving…' : 'Remove' }}
        </button>
      </li>
      <li v-for="skill in filteredSkills" :key="skill.id" class="agents-skill-activation__row">
        <div class="agents-skill-activation__summary">
          <div class="agents-skill-activation__name">
            <strong>{{ skill.name }}</strong>
            <span class="k-badge agents-badge">{{ skill.scope }}</span>
            <span v-if="!skill.editable" class="k-badge agents-badge">Read-only</span>
          </div>
          <p>{{ skill.description || 'No description provided.' }}</p>
          <code>{{ skill.id }}</code>
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
          {{ busySkill === skill.id ? 'Saving…' : enabledIDs.has(skill.id) ? 'Disable' : 'Enable' }}
        </button>
      </li>
    </ul>
    <p v-else-if="query.trim()" class="agents-hint" role="status">No skills match this search.</p>
    <div v-else class="agents-state agents-state-empty" role="status">
      <p>{{ view === 'enabled' ? 'No skills are enabled for this agent.' : 'No additional skills are available.' }}</p>
      <p>{{ view === 'enabled' ? 'Add skills to make them available in chat.' : 'Create or import reusable skills in the Skills library.' }}</p>
    </div>
    <p v-for="warning in catalog.warnings || []" :key="warning" class="agents-hint" role="status">{{ warning }}</p>
  </div>
</template>
