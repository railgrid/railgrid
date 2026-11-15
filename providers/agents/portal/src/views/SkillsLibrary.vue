<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { Download, FileUp, Loader2, Plus, RefreshCw, Save, Trash2, Upload, X } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import type { AppStore } from '../store'
import type { AgentSkill, AgentSkillDetail } from '../types'
import type { AgentKitSkill } from '../agentkit/composer'
import { confirmDialog } from '../portalkit/confirm'
import { providerFetch } from '../portalkit/tenant'
import { createKubeClient, type KubeObject, type KubeResourceRef } from '../portalkit/kube'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import { ensureAgentUIStyles } from '../agentkit/styles'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import { useCollectionSession } from '../portalkit/useCollectionSession'
import { hashFor, type Route } from '../router'
import AISkillCatalogList from '../agentkit/AISkillCatalogList.vue'
import { toast } from '../portalkit/toast'
import { useAuthorityGuard } from '../vue/runtime'
import { createSkillDocument, parseSkillDocument, skillPackageName, validateSkillResources } from '../skill-document'

ensureAgentUIStyles()

const props = defineProps<{ store: AppStore; api: ApiClient; returnAgent?: string; authorityEpoch?: number; createRoute?: boolean }>()
const emit = defineEmits<{ navigate: [route: Route] }>()
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)

interface SkillResourceWrite { name: string; content: string }
interface WorkspaceSkillCR extends KubeObject {
  apiVersion: string
  kind: string
  spec?: { document?: string; resources?: SkillResourceWrite[] }
}
const SKILLS_REF: KubeResourceRef = { group: 'agents.railgrid.ai', version: 'v1alpha1', resource: 'skills' }

const catalog = ref<AgentSkill[]>([])
const catalogWarnings = ref<string[]>([])
const catalogLoading = ref(false)
const catalogLoaded = ref(false)
const catalogError = ref('')
const selectedSkill = ref<AgentKitSkill | null>(null)
const detail = ref<AgentSkillDetail | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
const search = ref('')
const mode = ref<'detail' | 'create' | 'edit'>('detail')
const packageName = ref('')
const skillDocument = ref('')
const resources = ref<SkillResourceWrite[]>([])
const validationError = ref('')
const validationField = ref<'document' | 'resources' | 'package'>('document')
const saveError = ref('')
const saving = ref(false)
const deleting = ref(false)
const importing = ref(false)
const editConflict = ref(false)
const editBaseline = ref<{ document: string; resources: SkillResourceWrite[]; version?: string } | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)
const refreshButton = ref<HTMLButtonElement | null>(null)
const retryButton = ref<HTMLButtonElement | null>(null)
const createButton = ref<HTMLButtonElement | null>(null)
const importButton = ref<HTMLButtonElement | null>(null)
const guideCreateButton = ref<HTMLButtonElement | null>(null)
const guideImportButton = ref<HTMLButtonElement | null>(null)
const createHeading = ref<HTMLElement | null>(null)
const createDraftStarted = ref(false)
const completedCreate = ref('')
let createRouteEpoch = 0
let catalogSerial = 0
let catalogRead: Promise<void> | null = null
let detailSerial = 0
let requestSerial = 0
let importSerial = 0

const parsedDocument = computed(() => {
  try { return parseSkillDocument(skillDocument.value) } catch { return null }
})
const sortedCatalog = computed(() => [...catalog.value].sort((left, right) => left.name.localeCompare(right.name) || left.id.localeCompare(right.id)))
const collectionShown = useCollectionSession(() => (catalogLoaded.value && catalog.value.length > 0) || !!search.value.trim(), () => props.authorityEpoch)
const showFirstRun = computed(() => !props.createRoute && catalogLoaded.value && !catalogLoading.value && !catalogError.value && !catalogWarnings.value.length && !collectionShown.value && !catalog.value.length && !selectedSkill.value)
const readOnlySkill = computed(() => selectedSkill.value && selectedSkill.value.editable !== true)
const workbenchLocked = computed(() => saving.value || deleting.value || importing.value)

watch(showFirstRun, (shown, wasShown) => {
  if (shown === wasShown || props.createRoute) return
  const active = document.activeElement
  const target = shown
    ? active === createButton.value ? guideCreateButton : active === importButton.value ? guideImportButton : null
    : active === guideCreateButton.value ? createButton : active === guideImportButton.value ? importButton : null
  if (!target) return
  void nextTick(() => {
    if (!props.createRoute && (document.activeElement === active || document.activeElement === document.body)) target.value?.focus()
  })
})

function isCurrent(serial: number, api: ApiClient): boolean {
  return serial === requestSerial && props.api === api
}

async function loadCatalog(selectID?: string, maySelect: () => boolean = () => true): Promise<void> {
  if (catalogRead) {
    const previous = catalogRead
    const serial = catalogSerial
    await previous
    if (serial !== catalogSerial) return
    return loadCatalog(selectID, maySelect)
  }
  const serial = ++catalogSerial
  const api = props.api
  const authority = captureAuthority()
  catalogLoading.value = true
  const read = (async () => { try {
    const result = await api.getSkillLibrary()
    if (serial !== catalogSerial || !authorityIsCurrent(authority) || props.api !== api) return
    catalog.value = result.skills || []
    catalogLoaded.value = true
    const restoreRetryFocus = document.activeElement === retryButton.value
    catalogError.value = ''
    if (restoreRetryFocus) void nextTick(() => refreshButton.value?.focus())
    catalogWarnings.value = result.warnings || []
    if (selectID && maySelect()) {
      const next = catalog.value.find((skill) => skill.id === selectID)
      if (next) await selectSkill(next, true)
      else { selectedSkill.value = null; detail.value = null }
    } else if (selectedSkill.value && mode.value !== 'create') {
      const current = catalog.value.find((skill) => skill.id === selectedSkill.value?.id)
      if (mode.value === 'edit') {
        const baselineVersion = editBaseline.value?.version
        if (!current || !baselineVersion || !current.version || baselineVersion !== current.version || !current.editable) editConflict.value = true
        if (current) selectedSkill.value = current
      } else if (current) {
        await selectSkill(current, true)
      } else { selectedSkill.value = null; detail.value = null; detailSerial += 1 }
    }
  } catch (error) {
    if (serial === catalogSerial && authorityIsCurrent(authority) && props.api === api) catalogError.value = (error as Error).message
  } finally {
    if (serial === catalogSerial) catalogLoading.value = false
  } })()
  catalogRead = read
  await read
  if (catalogRead === read) catalogRead = null
}

function refreshCatalog(): void {
  if (!catalogLoading.value) void loadCatalog()
}

async function selectSkill(skill: AgentKitSkill, allowLocked = false): Promise<void> {
  if (workbenchLocked.value && !allowLocked) return
  selectedSkill.value = skill
  detail.value = null
  detailError.value = ''
  mode.value = 'detail'
  const serial = ++detailSerial
  const api = props.api
  const authority = captureAuthority()
  detailLoading.value = true
  try {
    const result = await api.getSkillLibraryDetail(skill.id)
    if (serial !== detailSerial || !authorityIsCurrent(authority) || props.api !== api || selectedSkill.value?.id !== skill.id) return
    detail.value = result
  } catch (error) {
    if (serial === detailSerial && authorityIsCurrent(authority) && selectedSkill.value?.id === skill.id) detailError.value = (error as Error).message
  } finally {
    if (serial === detailSerial) detailLoading.value = false
  }
}

function retrySkillDetail(): void {
  const skill = selectedSkill.value
  if (skill && !detailLoading.value) void selectSkill(skill)
}

function startCreate(): void {
  if (workbenchLocked.value) return
  emit('navigate', { kind: 'create', resource: 'skill', ...(props.returnAgent ? { returnAgent: props.returnAgent } : {}) })
}

function initializeCreate(): void {
  mode.value = 'create'
  if (createDraftStarted.value) return
  createDraftStarted.value = true
  completedCreate.value = ''
  packageName.value = ''
  skillDocument.value = createSkillDocument('new-skill', 'Describe what this skill helps the agent do.', '')
  resources.value = []
  validationError.value = ''
  saveError.value = ''
}

function startEdit(): void {
  if (workbenchLocked.value) return
  const selected = selectedSkill.value
  const current = detail.value
  if (!selected || !current || !selected.editable || selected.scope !== 'workspace') return
  mode.value = 'edit'
  editConflict.value = false
  packageName.value = selected.packageName || ''
  skillDocument.value = current.document ?? createSkillDocument(current.name, current.description, current.instructions || '')
  resources.value = (current.resources || []).map((resource) => ({ name: resource.path, content: resource.content || '' }))
  editBaseline.value = current.document == null ? null : { document: current.document, resources: resources.value.map(resource => ({ ...resource })), version: current.version }
  validationError.value = ''
  saveError.value = ''
}

function cancelEdit(): void {
  if (props.createRoute) {
    createDraftStarted.value = false
    emit('navigate', { kind: 'menu', menu: 'skills', ...(props.returnAgent ? { returnAgent: props.returnAgent } : {}) })
    return
  }
  mode.value = 'detail'
  editConflict.value = false
  const current = catalog.value.find(skill => skill.id === selectedSkill.value?.id)
  if (current) void selectSkill(current)
  else { selectedSkill.value = null; detail.value = null }
}

function addResource(): void { resources.value = [...resources.value, { name: '', content: '' }] }
function updateResource(index: number, patch: Partial<SkillResourceWrite>): void {
  resources.value = resources.value.map((resource, row) => row === index ? { ...resource, ...patch } : resource)
}
function removeResource(index: number): void { resources.value = resources.value.filter((_, row) => row !== index) }

function validateEditor(): { name: string; description: string; document: string; resources: SkillResourceWrite[] } | null {
  validationError.value = ''
  try {
    validationField.value = 'document'
    const parsed = parseSkillDocument(skillDocument.value)
    validationField.value = 'resources'
    const cleanResources = validateSkillResources(skillDocument.value, resources.value)
    validationField.value = 'package'
    const nextPackageName = mode.value === 'edit' ? packageName.value.trim() : packageName.value.trim() || skillPackageName(parsed.name)
    if (nextPackageName.length > 253) throw new Error('Package name must be 253 characters or shorter.')
    if (!/^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/u.test(nextPackageName)) throw new Error('Package name must be a lowercase DNS label.')
    packageName.value = nextPackageName
    return { name: parsed.name, description: parsed.description, document: skillDocument.value, resources: cleanResources }
  } catch (error) {
    validationError.value = (error as Error).message
    return null
  }
}

function kubeClient(api: ApiClient) {
  const context = api.context()
  const cluster = context?.tenant?.trim() || ''
  if (!cluster) throw new Error('Select a workspace before editing skills.')
  return createKubeClient({ fetch: providerFetch(context), cluster })
}

async function save(): Promise<void> {
  if (workbenchLocked.value || completedCreate.value || (mode.value === 'edit' && editConflict.value)) return
  const value = validateEditor()
  if (!value) return
  const operation = mode.value
  const routeEpoch = createRouteEpoch
  const savedPackageName = packageName.value
  const authority = captureAuthority()
  const api = authority.api
  const serial = ++requestSerial
  saving.value = true
  saveError.value = ''
  try {
    const client = kubeClient(api)
    if (operation === 'create') {
      await client.create<WorkspaceSkillCR>(SKILLS_REF, {
        apiVersion: 'agents.railgrid.ai/v1alpha1',
        kind: 'Skill',
        metadata: { name: savedPackageName },
        spec: { document: value.document, resources: value.resources },
      })
    } else {
      const currentName = savedPackageName
      const current = await client.get<WorkspaceSkillCR>(SKILLS_REF, currentName)
      if (!isCurrent(serial, api) || !authorityIsCurrent(authority)) return
      const baseline = editBaseline.value
      const normalizeResources = (values: SkillResourceWrite[]) => [...values].sort((a, b) => a.name.localeCompare(b.name)).map(({ name, content }) => ({ name, content }))
      if (editConflict.value || !baseline || current.spec?.document !== baseline.document || JSON.stringify(normalizeResources(current.spec?.resources || [])) !== JSON.stringify(normalizeResources(baseline.resources))) {
        editConflict.value = true
        return
      }
      await client.update<WorkspaceSkillCR>(SKILLS_REF, {
        ...current,
        apiVersion: 'agents.railgrid.ai/v1alpha1',
        kind: 'Skill',
        spec: { ...(current.spec || {}), document: value.document, resources: value.resources },
      })
    }
    if (!isCurrent(serial, api) || !authorityIsCurrent(authority)) return
    if (operation === 'create' && routeEpoch !== createRouteEpoch) {
      completedCreate.value = savedPackageName
      toast('ok', `Created workspace skill “${value.name}”.`)
      if (!props.createRoute) { createDraftStarted.value = false; await loadCatalog(`workspace:${savedPackageName}`, () => routeEpoch !== createRouteEpoch && !props.createRoute) }
      return
    }
    toast('ok', operation === 'create' ? `Created workspace skill “${value.name}”.` : `Saved workspace skill “${value.name}”.`)
    mode.value = 'detail'
    createDraftStarted.value = false
    if (operation === 'create' && props.createRoute) {
      saving.value = false
      emit('navigate', { kind: 'menu', menu: 'skills', ...(props.returnAgent ? { returnAgent: props.returnAgent } : {}) })
      await nextTick()
      if (isCurrent(serial, api) && authorityIsCurrent(authority)) void loadCatalog(`workspace:${savedPackageName}`, () => !props.createRoute && createRouteEpoch === routeEpoch + 1)
    } else {
      await loadCatalog(`workspace:${savedPackageName}`)
    }
  } catch (error) {
    if (isCurrent(serial, api) && authorityIsCurrent(authority)) {
      if (operation === 'create' && routeEpoch !== createRouteEpoch) toast('error', `Could not confirm skill creation: ${(error as Error).message}`)
      else saveError.value = (error as Error).message
    }
  } finally {
    if (serial === requestSerial) saving.value = false
  }
}

async function removeSkill(): Promise<void> {
  const selected = selectedSkill.value
  const packageID = selected?.packageName
  if (!selected || !packageID || !selected.editable || selected.scope !== 'workspace' || workbenchLocked.value) return
  const authority = captureAuthority()
  const api = authority.api
  const confirmationSerial = requestSerial
  const ok = await confirmDialog({ title: `Delete “${selected.name}”?`, message: 'This removes the workspace skill package for every agent in this workspace. Agents that enabled it will no longer be able to use it.', danger: true, confirmLabel: 'Delete skill' })
  if (!ok || !isCurrent(confirmationSerial, api) || !authorityIsCurrent(authority) || workbenchLocked.value || selectedSkill.value?.id !== selected.id) return
  const serial = ++requestSerial
  deleting.value = true
  try {
    const client = kubeClient(api)
    const current = await client.get<WorkspaceSkillCR>(SKILLS_REF, packageID)
    if (!isCurrent(serial, api) || !authorityIsCurrent(authority)) return
    await client.delete(SKILLS_REF, packageID, { preconditions: { uid: current.metadata.uid, resourceVersion: current.metadata.resourceVersion } })
    if (!isCurrent(serial, api) || !authorityIsCurrent(authority)) return
    toast('ok', `Deleted workspace skill “${selected.name}”.`)
    selectedSkill.value = null
    detail.value = null
    await loadCatalog()
  } catch (error) {
    if (isCurrent(serial, api) && authorityIsCurrent(authority)) toast('error', `Could not delete skill: ${(error as Error).message}`)
  } finally {
    if (serial === requestSerial) deleting.value = false
  }
}

function downloadJSON(filename: string, data: unknown): void {
  const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' }))
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  anchor.click()
  setTimeout(() => URL.revokeObjectURL(url), 0)
}

function exportSkill(): void {
  const selected = selectedSkill.value
  const current = detail.value
  if (!selected || !current || !selected.editable || !selected.packageName) return
  downloadJSON(`${selected.packageName}.skill.json`, {
    apiVersion: 'agents.railgrid.ai/v1alpha1',
    kind: 'Skill',
    metadata: { name: selected.packageName },
    spec: {
      document: current.document ?? createSkillDocument(current.name, current.description, current.instructions || ''),
      resources: (current.resources || []).map((resource) => ({ name: resource.path, content: resource.content || '' })),
    },
  })
}

function openImport(): void {
  if (workbenchLocked.value) return
  fileInput.value?.click()
}
async function importFile(event: Event): Promise<void> {
  const input = event.target instanceof HTMLInputElement ? event.target : null
  const file = input?.files?.[0]
  if (!file || workbenchLocked.value) return
  const serial = ++importSerial
  const routeEpoch = createRouteEpoch
  importing.value = true
  const authority = captureAuthority()
  try {
    const text = await file.text()
    if (serial !== importSerial || routeEpoch !== createRouteEpoch || !authorityIsCurrent(authority) || saving.value || deleting.value) return
    if (file.name.toLowerCase().endsWith('.json')) {
      const value = JSON.parse(text) as { metadata?: { name?: string }; packageName?: string; spec?: { document?: string; resources?: SkillResourceWrite[] }; document?: string; resources?: SkillResourceWrite[] }
      const importedDocument = value.spec?.document || value.document
      if (typeof importedDocument !== 'string') throw new Error('The import file needs a spec.document or document string.')
      const parsed = parseSkillDocument(importedDocument)
      packageName.value = value.metadata?.name || value.packageName || skillPackageName(parsed.name)
      skillDocument.value = importedDocument
      resources.value = (value.spec?.resources || value.resources || []).map((resource) => ({ name: resource.name, content: resource.content }))
    } else {
      const parsed = parseSkillDocument(text)
      packageName.value = skillPackageName(parsed.name)
      skillDocument.value = text
      resources.value = []
    }
    createDraftStarted.value = true
    completedCreate.value = ''
    validationError.value = ''
    saveError.value = ''
    emit('navigate', { kind: 'create', resource: 'skill', ...(props.returnAgent ? { returnAgent: props.returnAgent } : {}) })
  } catch (error) {
    if (serial === importSerial && routeEpoch === createRouteEpoch && authorityIsCurrent(authority)) toast('error', `Could not import skill: ${(error as Error).message}`)
  } finally {
    if (serial === importSerial) {
      if (input) input.value = ''
      importing.value = false
    }
  }
}

watch([() => props.returnAgent, () => props.api, () => props.authorityEpoch], () => {
  catalogSerial += 1
  catalogRead = null
  detailSerial += 1
  requestSerial += 1
  importSerial += 1
  catalog.value = []
  catalogLoaded.value = false
  catalogWarnings.value = []
  catalogError.value = ''
  selectedSkill.value = null
  detail.value = null
  detailLoading.value = false
  detailError.value = ''
  saving.value = false
  deleting.value = false
  importing.value = false
  mode.value = 'detail'
  createDraftStarted.value = false
  completedCreate.value = ''
  if (props.createRoute) initializeCreate()
  editConflict.value = false
  void loadCatalog()
}, { immediate: true })
watch(() => props.createRoute, (creating, previous) => {
  if (previous !== undefined) createRouteEpoch += 1
  if (creating) { initializeCreate(); void nextTick(() => createHeading.value?.focus()) }
  else if (previous) {
    mode.value = 'detail'
    if (completedCreate.value) {
      const createdName = completedCreate.value
      completedCreate.value = ''
      createDraftStarted.value = false
      void loadCatalog(`workspace:${createdName}`, () => !props.createRoute)
    }
    const active = document.activeElement
    const ownsFocus = active === document.body || !!createHeading.value?.closest('.agents-skill-workbench')?.contains(active)
    void nextTick(() => {
      if (ownsFocus && !props.createRoute && (document.activeElement === active || document.activeElement === document.body)) (guideCreateButton.value || createButton.value)?.focus()
    })
  }
}, { immediate: true })
onBeforeUnmount(() => { catalogSerial += 1; detailSerial += 1; requestSerial += 1; importSerial += 1; catalogRead = null })
</script>

<template>
  <ResourcePage class="agents-skill-workbench" title="Skills library" variant="collection" subtitle="Manage reusable workspace skills and inspect published provider packages." :show-header="!createRoute">
    <template v-if="returnAgent" #summary><ResourceBackLink :href="hashFor({ kind: 'agent', name: returnAgent, tab: 'skills' })" @back="emit('navigate', { kind: 'agent', name: returnAgent, tab: 'skills' })">Back to {{ returnAgent }}’s skills</ResourceBackLink></template>
    <template #actions>
      <button ref="refreshButton" type="button" class="k-btn k-btn--ghost secondary" :aria-disabled="catalogLoading" :aria-busy="catalogLoading || undefined" @click="refreshCatalog"><RefreshCw :class="{ 'k-spin': catalogLoading }" :size="14" :stroke-width="1.75" aria-hidden="true" /> {{ catalogLoading ? 'Refreshing…' : 'Refresh' }}</button>
      <button v-if="!showFirstRun" ref="importButton" type="button" class="k-btn k-btn--ghost secondary" :disabled="workbenchLocked" :aria-busy="importing ? 'true' : undefined" @click="openImport"><Loader2 v-if="importing" class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" /><FileUp v-else class="agents-skill-workbench__button-icon" aria-hidden="true" /> {{ importing ? 'Importing…' : 'Import' }}</button>
      <button v-if="!showFirstRun" ref="createButton" type="button" class="k-btn k-btn--primary" :disabled="workbenchLocked" @click="startCreate"><Plus class="agents-skill-workbench__button-icon" aria-hidden="true" /> New skill</button>
      <input ref="fileInput" type="file" class="hidden" accept=".md,.markdown,.json,application/json,text/markdown,text/plain" :disabled="workbenchLocked" @change="importFile" />
    </template>

    <div v-if="createRoute" class="k-create-page">
      <ResourceBackLink :href="hashFor({ kind: 'menu', menu: 'skills', ...(returnAgent ? { returnAgent } : {}) })" :disabled="workbenchLocked" @back="cancelEdit">Back to Skills library</ResourceBackLink>
      <header class="k-create-header"><h2 ref="createHeading" class="k-create-title" tabindex="-1">Create a workspace skill</h2><p class="k-create-description">Write instructions and supporting resources for agents in this workspace.</p></header>
    </div>
    <div v-if="!createRoute && catalogError" class="k-inline-notification k-inline-notification--error mb-3" role="alert">
      <span class="k-inline-notification__body"><span class="k-inline-notification__message">{{ catalogLoaded ? 'Showing the last loaded skills. ' : 'Could not load skill library. ' }}{{ catalogError }}</span></span>
      <button ref="retryButton" class="k-inline-notification__action" type="button" :aria-disabled="catalogLoading" @click="refreshCatalog">{{ catalogLoading ? 'Retrying…' : 'Retry' }}</button>
    </div>
    <p v-for="warning in (createRoute ? [] : catalogWarnings)" :key="warning" class="agents-hint" role="status">{{ warning }}</p>

    <FirstRunGuide v-if="showFirstRun" title="Create your first skill" description="Give agents reusable instructions and supporting resources for work in this workspace." :steps="[{ label: 'Create or import', description: 'Write instructions or bring an existing skill' }, { label: 'Enable for an agent', description: 'Choose the skill in an agent’s Skills tab' }, { label: 'Use the skill', description: 'Start work with your agent' }]" journey-label="Skill setup">
      <template #icon><Upload :stroke-width="1.75" aria-hidden="true" /></template>
      <template #actions><button ref="guideCreateButton" type="button" class="k-btn k-btn--primary" :disabled="workbenchLocked" @click="startCreate">New skill</button><button ref="guideImportButton" type="button" class="k-btn k-btn--ghost" :disabled="workbenchLocked" :aria-busy="importing ? 'true' : undefined" @click="openImport">{{ importing ? 'Importing…' : 'Import skill' }}</button></template>
    </FirstRunGuide>

    <div v-show="!showFirstRun" :class="createRoute ? 'k-create-surface k-create-surface--wide' : 'agents-skill-workbench__columns grid min-h-0 gap-4 xl:grid-cols-[minmax(300px,0.8fr)_minmax(0,1.2fr)]'">
      <section v-show="!createRoute" class="min-w-0 rounded-xl border border-border-subtle bg-surface p-4" aria-label="Skill catalog">
        <p v-if="!catalogLoaded && catalogLoading" class="agents-state agents-state-loading" role="status">Loading skills…</p>
        <AISkillCatalogList
          v-else-if="catalogLoaded"
          v-model:query="search"
          :skills="sortedCatalog"
          :show-status="false"
          :selected-id="selectedSkill?.id || ''"
          :loading="false"
          :busy="detailLoading || workbenchLocked"
          title="Workspace and provider skills"
          empty-text="No skills have been published in this workspace."
          @select="selectSkill($event)"
        />
      </section>

      <section :class="createRoute ? 'k-create-body' : 'min-w-0 rounded-xl border border-border-subtle bg-surface p-4'" aria-label="Skill details">
        <template v-if="mode === 'create' || mode === 'edit'">
          <div v-if="!createRoute" class="mb-4 flex items-start justify-between gap-3">
            <div><h3 class="text-[16px] font-semibold text-text-primary">{{ mode === 'create' ? 'Create a workspace skill' : 'Edit workspace skill' }}</h3><p class="mt-1 text-[12px] text-text-secondary">The SKILL.md frontmatter supplies the skill name and description.</p></div>
            <button type="button" class="k-btn k-btn--ghost secondary" :disabled="workbenchLocked" @click="cancelEdit"><X class="agents-skill-workbench__button-icon" aria-hidden="true" /> Cancel</button>
          </div>
          <label v-if="mode === 'create'" class="mb-3 block">Package name<input v-model="packageName" class="k-input mt-1" placeholder="my-skill" autocomplete="off" :disabled="workbenchLocked" :aria-invalid="validationError && validationField === 'package' ? 'true' : undefined" :aria-describedby="validationError && validationField === 'package' ? 'skill-validation-error' : undefined" /><span class="agents-hint">Unique package identifier used for imports and updates; separate from the skill name in SKILL.md.</span></label>
          <label class="block">SKILL.md<textarea v-model="skillDocument" class="agents-skill-workbench__document-editor k-input mt-1 min-h-72 font-mono text-[12px]" spellcheck="false" :disabled="workbenchLocked" :aria-invalid="validationError && validationField === 'document' ? 'true' : undefined" :aria-describedby="validationError && validationField === 'document' ? 'skill-validation-error' : undefined" /></label>
          <div v-if="parsedDocument" class="my-2 rounded-md bg-surface-raised px-3 py-2 text-[11px] text-text-secondary"><strong>{{ parsedDocument.name }}</strong><span aria-hidden="true"> · </span>{{ parsedDocument.description }}</div>
          <fieldset class="mt-4 grid gap-2" :disabled="workbenchLocked" :aria-describedby="validationError && validationField === 'resources' ? 'skill-validation-error' : undefined">
            <legend class="mb-1 text-[13px] font-semibold">Supporting resources</legend>
            <p class="text-[11px] text-text-secondary">Text files shipped with this skill, such as examples or reference material.</p>
            <div v-for="(resource, index) in resources" :key="index" class="rounded-lg border border-border-subtle p-3">
              <div class="mb-2 flex items-center gap-2"><label class="min-w-0 flex-1 text-[11px]">Resource path<input :value="resource.name" class="k-input mt-1" placeholder="references/guide.md" @input="updateResource(index, { name: ($event.target as HTMLInputElement).value })" /></label><button type="button" class="k-icon-action agents-iconbtn-danger mt-4" :aria-label="`Remove resource ${resource.name || index + 1}`" title="Remove resource" @click="removeResource(index)"><Trash2 class="agents-skill-workbench__button-icon" aria-hidden="true" /></button></div>
              <label class="block text-[11px]">Content<textarea :value="resource.content" class="agents-skill-workbench__resource-editor k-input mt-1 min-h-32 font-mono text-[11px]" @input="updateResource(index, { content: ($event.target as HTMLTextAreaElement).value })" /></label>
            </div>
            <button type="button" class="k-btn k-btn--ghost secondary justify-self-start" @click="addResource"><Plus class="agents-skill-workbench__button-icon" aria-hidden="true" /> Add resource</button>
          </fieldset>
          <p v-if="completedCreate" class="k-inline-notification" role="status">Skill “{{ completedCreate }}” was created. Return to the library to review it.</p>
          <p v-if="validationError" id="skill-validation-error" class="agents-fielderr" role="alert">{{ validationError }}</p>
          <p v-if="saveError" class="agents-fielderr" role="alert">Could not save skill: {{ saveError }}</p>
          <p v-if="editConflict" class="agents-fielderr" role="alert">This skill changed or was removed while you were editing. Your draft is preserved; copy it before canceling and reopening the latest skill.</p>
          <div :class="createRoute ? 'k-create-actions' : 'mt-4 flex flex-wrap justify-end gap-2'">
            <button v-if="createRoute" type="button" class="k-btn k-btn--ghost secondary" :disabled="workbenchLocked" @click="cancelEdit">Cancel</button>
            <button type="button" class="k-btn k-btn--primary" :disabled="workbenchLocked || editConflict || !!completedCreate" :aria-busy="saving ? 'true' : undefined" @click="save"><Loader2 v-if="saving" class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" /><Save v-else class="agents-skill-workbench__button-icon" aria-hidden="true" /> {{ saving ? 'Saving…' : mode === 'create' ? 'Create skill' : 'Save skill' }}</button>
          </div>
        </template>
        <div v-else-if="!selectedSkill" class="flex min-h-72 flex-col items-center justify-center gap-3 text-center" role="status">
          <Upload class="h-7 w-7" :stroke-width="1.75" aria-hidden="true" />
          <h3 class="text-[14px] font-medium text-text-primary">Choose a skill to review</h3>
          <p class="max-w-sm text-[12px] text-text-secondary">Create a workspace skill or select an existing package from the catalog.</p>
        </div>
        <template v-else>
          <div class="agents-skill-workbench__detail-header mb-4 flex items-start justify-between gap-3">
            <div class="agents-skill-workbench__detail-heading min-w-0"><p class="text-[10px] uppercase tracking-wide text-text-muted">{{ selectedSkill.scope }}{{ selectedSkill.editable ? ' · editable' : ' · read-only' }}</p><h3 class="mt-1 break-words text-[18px] font-semibold text-text-primary">{{ selectedSkill.name }}</h3><p class="mt-1 text-[12px] text-text-secondary">{{ selectedSkill.description }}</p><code class="mt-1 block break-all text-[10px] text-text-muted" :title="selectedSkill.id">{{ selectedSkill.packageName || selectedSkill.packagePath }}</code></div>
            <div v-if="selectedSkill.editable" class="agents-skill-workbench__detail-actions flex shrink-0 gap-1">
              <button type="button" class="k-btn k-btn--ghost secondary" :disabled="detailLoading || workbenchLocked" @click="exportSkill"><Download class="agents-skill-workbench__button-icon" aria-hidden="true" /> Export</button>
              <button type="button" class="k-btn k-btn--ghost secondary" :disabled="detailLoading || workbenchLocked" @click="startEdit"><Save class="agents-skill-workbench__button-icon" aria-hidden="true" /> Edit</button>
              <button type="button" class="k-icon-action agents-iconbtn-danger" :disabled="workbenchLocked" aria-label="Delete workspace skill" title="Delete workspace skill" @click="removeSkill"><Trash2 class="agents-skill-workbench__button-icon" aria-hidden="true" /></button>
            </div>
          </div>
          <div v-if="detailError" class="agents-skill-workbench__detail-error agents-fielderr" role="alert"><span>Could not load skill detail. {{ detailError }}</span><button type="button" class="k-btn k-btn--ghost secondary" :disabled="detailLoading" @click="retrySkillDetail">{{ detailLoading ? 'Retrying…' : 'Retry' }}</button></div>
          <p v-else-if="detailLoading" class="agents-state agents-state-loading" role="status"><Loader2 class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" /> Loading skill…</p>
          <template v-else-if="detail">
            <ResourceSectionCard class="mb-3" variant="document" title="Instructions" description="This guidance is added to the agent context when the skill is selected.">
              <pre class="max-h-[50vh] overflow-auto whitespace-pre-wrap break-words rounded-md bg-surface-raised p-3 text-[11px] leading-5 text-text-secondary">{{ detail.instructions }}</pre>
            </ResourceSectionCard>
            <ResourceSectionCard v-if="detail.resources?.length" variant="document" title="Supporting resources" :description="`${detail.resources.length} resource${detail.resources.length === 1 ? '' : 's'} in this package.`">
              <details v-for="resource in detail.resources" :key="resource.path" class="border-b border-border-subtle py-2 last:border-0"><summary class="cursor-pointer text-[12px] font-mono">{{ resource.path }} <span class="text-text-muted">· {{ resource.size }} bytes</span></summary><pre class="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-surface-raised p-3 text-[11px] text-text-secondary">{{ resource.content }}</pre></details>
            </ResourceSectionCard>
          </template>
          <div v-if="readOnlySkill" class="mt-3 rounded-md border border-border-subtle bg-surface-raised px-3 py-2 text-[11px] text-text-secondary" role="note">Provider packages are read-only. Enable skills from an Agent’s Skills workbench tab.</div>
        </template>
      </section>
    </div>
  </ResourcePage>
</template>
