<script setup lang="ts">
import StatusBadge from '../portalkit/StatusBadge.vue'
import { computed, nextTick, ref, watch } from 'vue'
import { Loader2, Search, TriangleAlert, X } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import Tabs from '../portalkit/Tabs.vue'
import InlineNotification from '../portalkit/InlineNotification.vue'
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
const catalogHasSnapshot = ref(false)
const catalogIncomplete = computed(() => catalogHasSnapshot.value && Boolean(catalog.value.warnings?.length))
const error = ref('')
const errorNotice = ref<HTMLElement | null>(null)
const warningNotice = ref<HTMLElement | null>(null)
const busySkill = ref('')
const saveStatus = ref<ConfigSaveStatus>('idle')
const saveError = ref('')
let requestSerial = 0
let mutationSerial = 0

const agent = computed(() => { revision.value; return props.store.agent(props.name) })
const enabledIDs = computed(() => new Set((agent.value?.spec.skills || []).map((item) => item.name)))
const missingSkillIDs = computed(() => catalogHasSnapshot.value && !catalogIncomplete.value
  ? [...enabledIDs.value].filter((id) => !catalog.value.skills.some((skill) => skill.id === id))
  : [])
const unverifiedEnabledIDs = computed(() => catalogIncomplete.value
  ? [...enabledIDs.value].filter((id) => !catalog.value.skills.some((skill) => skill.id === id))
  : [])
const availableSkills = computed(() => catalog.value.skills.filter(skill => !enabledIDs.value.has(skill.id)))
const filteredSkills = computed(() => filterAssistantSkills(
  [...catalog.value.skills].filter(skill => view.value === 'enabled' ? enabledIDs.value.has(skill.id) : !enabledIDs.value.has(skill.id))
    .sort((left, right) => left.name.localeCompare(right.name) || left.id.localeCompare(right.id)), query.value))
const filteredMissingSkillIDs = computed(() => view.value === 'enabled' ? missingSkillIDs.value.filter(id => !query.value.trim() || id.toLocaleLowerCase().includes(query.value.trim().toLocaleLowerCase())) : [])
const filteredUnverifiedEnabledIDs = computed(() => view.value === 'enabled' ? unverifiedEnabledIDs.value.filter(id => !query.value.trim() || id.toLocaleLowerCase().includes(query.value.trim().toLocaleLowerCase())) : [])
const catalogWarningMessage = 'Catalog needs attention. Returned skills remain usable, but the list may be incomplete. Review the warning details below.'
const incompleteAvailableEmptyMessage = computed(() => catalog.value.skills.length
  ? 'No additional skills were listed in this response. Review the warnings or retry.'
  : 'No skills were listed in this response. Review the warnings or retry.')

async function load(): Promise<void> {
  if (loading.value) return
  const serial = ++requestSerial
  const name = props.name
  const api = props.api
  const authority = captureAuthority()
  const restoreRetryFocus = Boolean(
    errorNotice.value?.contains(document.activeElement) ||
    warningNotice.value?.contains(document.activeElement),
  )
  loading.value = true
  try {
    const result = await api.getAgentSkillCatalog(name)
    if (serial !== requestSerial || props.name !== name || props.api !== api || !authorityIsCurrent(authority)) return
    catalog.value = result
    catalogHasSnapshot.value = true
    error.value = ''
    await nextTick()
    if (
      restoreRetryFocus &&
      serial === requestSerial &&
      props.name === name &&
      props.api === api &&
      authorityIsCurrent(authority) &&
      (!document.activeElement || document.activeElement === document.body || !document.activeElement.isConnected)
    ) {
      searchInput.value?.focus({ preventScroll: true })
    }
  } catch (reason) {
    if (serial === requestSerial && props.name === name && props.api === api && authorityIsCurrent(authority)) {
      error.value = (reason as Error).message
      await nextTick()
      if (
        restoreRetryFocus &&
        serial === requestSerial &&
        props.name === name &&
        props.api === api &&
        authorityIsCurrent(authority) &&
        (!document.activeElement || document.activeElement === document.body || !document.activeElement.isConnected)
      ) errorNotice.value?.querySelector<HTMLButtonElement>('button')?.focus({ preventScroll: true })
    }
  } finally {
    if (serial === requestSerial) loading.value = false
  }
}

async function updateEnabled(skillID: string, skillName: string, enabled: boolean): Promise<void> {
  if (busySkill.value || loading.value) return
  const authority = captureAuthority()
  const name = props.name
  const operationSerial = ++mutationSerial
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
    if (operationSerial === mutationSerial && busySkill.value === skillID) {
      busySkill.value = ''
      if (saveStatus.value === 'pending') saveStatus.value = 'idle'
    }
  }
}

function setEnabled(skill: Pick<AgentSkill, 'id' | 'name'>, enabled: boolean): Promise<void> {
  return updateEnabled(skill.id, skill.name, enabled)
}

watch([() => props.name, () => props.api, () => props.authorityEpoch], () => {
  requestSerial += 1
  mutationSerial += 1
  catalog.value = { skills: [], warnings: [] }
  catalogHasSnapshot.value = false
  loading.value = false
  error.value = ''
  busySkill.value = ''
  saveStatus.value = 'idle'
  saveError.value = ''
  void load()
}, { immediate: true })
</script>

<template>
  <div class="agents-skill-activation">
    <Tabs :tabs="[{ id: 'enabled', label: 'Enabled', count: enabledIDs.size }, { id: 'available', label: 'Available', count: catalogHasSnapshot && !catalogIncomplete ? availableSkills.length : undefined }]" :active="view" aria-label="Agent skill lists" @select="view = $event as 'enabled' | 'available'" />
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
    <div v-if="error" ref="errorNotice" class="mb-3">
      <InlineNotification
        :tone="catalogHasSnapshot ? 'warning' : 'error'"
        :message="catalogHasSnapshot ? `Could not refresh skills for ${name}. Showing the last loaded catalog. ${error}` : `Could not load skills for ${name}. ${error}`"
        action-label="Retry"
        action-busy-label="Retrying skills…"
        :action-busy="loading"
        @action="load"
      />
    </div>
    <div v-if="catalogIncomplete" ref="warningNotice" class="mb-3">
      <InlineNotification
        tone="warning"
        :message="catalogWarningMessage"
        :action-label="error ? '' : 'Retry'"
        action-busy-label="Retrying skills…"
        :action-busy="loading"
        @action="load"
      />
    </div>
    <p v-if="loading && !catalogHasSnapshot" class="agents-state agents-state-loading" role="status"><Loader2 class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" /> Loading skills…</p>
    <p v-else-if="loading && catalogHasSnapshot" class="sr-only" role="status" aria-live="polite">Refreshing skills…</p>
    <ul v-if="catalogHasSnapshot && (filteredSkills.length || filteredMissingSkillIDs.length || filteredUnverifiedEnabledIDs.length)" class="agents-skill-activation__list" role="list" :aria-label="view === 'enabled' ? 'Enabled skills' : 'Available skills'">
      <li v-for="id in filteredUnverifiedEnabledIDs" :key="`unverified:${id}`" class="agents-skill-activation__row">
        <div class="agents-skill-activation__summary">
          <div class="agents-skill-activation__name">
            <TriangleAlert :size="14" class="text-warning" aria-hidden="true" />
            <strong>Not listed in this catalog</strong>
            <StatusBadge class="agents-badge" status="Enabled" tone="muted" />
          </div>
          <p>This enabled reference could not be verified because the catalog returned warnings. Retry before removing it.</p>
          <code>{{ id }}</code>
        </div>
      </li>
      <li v-for="id in filteredMissingSkillIDs" :key="`missing:${id}`" class="agents-skill-activation__row">
        <div class="agents-skill-activation__summary">
          <div class="agents-skill-activation__name">
            <TriangleAlert :size="14" class="text-warning" aria-hidden="true" />
            <strong>Unavailable skill</strong>
            <StatusBadge class="agents-badge" status="Enabled" tone="muted" />
          </div>
          <p>This enabled reference is missing from the current catalog. Remove it or restore the skill.</p>
          <code>{{ id }}</code>
        </div>
          <button type="button" class="k-btn k-btn--ghost secondary shrink-0 agents-skill-activation__action" :disabled="Boolean(busySkill) || loading" :aria-busy="busySkill === id ? 'true' : undefined" @click="updateEnabled(id, id, false)">
          <Loader2 v-if="busySkill === id" class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" />
          {{ busySkill === id ? 'Saving…' : 'Remove' }}
        </button>
      </li>
      <li v-for="skill in filteredSkills" :key="skill.id" class="agents-skill-activation__row">
        <div class="agents-skill-activation__summary">
          <div class="agents-skill-activation__name">
            <strong>{{ skill.name }}</strong>
            <span class="k-badge k-badge--muted k-badge--label agents-badge">{{ skill.scope }}</span>
            <span v-if="!skill.editable" class="k-badge k-badge--muted k-badge--label agents-badge">Read-only</span>
          </div>
          <p>{{ skill.description || 'No description provided.' }}</p>
          <code>{{ skill.id }}</code>
        </div>
        <button
          type="button"
          class="k-btn k-btn--ghost secondary shrink-0 agents-skill-activation__action"
          :disabled="Boolean(busySkill) || loading"
          :aria-label="`${enabledIDs.has(skill.id) ? 'Disable' : 'Enable'} ${skill.name} for ${name}`"
          :aria-busy="busySkill === skill.id ? 'true' : undefined"
          @click="setEnabled(skill, !enabledIDs.has(skill.id))"
        >
          <Loader2 v-if="busySkill === skill.id" class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" />
          {{ busySkill === skill.id ? 'Saving…' : enabledIDs.has(skill.id) ? 'Disable' : 'Enable' }}
        </button>
      </li>
    </ul>
    <p v-else-if="error && !catalogHasSnapshot" class="agents-hint" role="status">Skill availability is unknown until this catalog can be loaded.</p>
    <p v-else-if="catalogIncomplete && query.trim()" class="agents-hint" role="status">No listed skills match this search. The catalog needs attention, so other matches may be missing.</p>
    <p v-else-if="catalogHasSnapshot && query.trim()" class="agents-hint" role="status">No skills match this search.</p>
    <p v-else-if="catalogIncomplete && view === 'available'" class="agents-hint" role="status">{{ incompleteAvailableEmptyMessage }}</p>
    <div v-else-if="catalogHasSnapshot" class="agents-state agents-state-empty" role="status">
      <p>{{ view === 'enabled' ? 'No skills are enabled for this agent.' : 'No additional skills are available.' }}</p>
      <p>{{ view === 'enabled' ? 'Add skills to make them available in chat.' : 'Create or import reusable skills in the Skills library.' }}</p>
    </div>
    <p v-for="warning in catalog.warnings || []" :key="warning" class="agents-hint" role="status">{{ warning }}</p>
  </div>
</template>
