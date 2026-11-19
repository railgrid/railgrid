<script setup lang="ts">
import LoadingSkeleton from '../portalkit/LoadingSkeleton.vue'
import ResourceDeleteFailure from '../portalkit/ResourceDeleteFailure.vue'
import { computed, nextTick, ref, watch } from 'vue'
import { Download, FileUp, Loader2, Package, Pencil, Plus, Trash2 } from 'lucide-vue-next'
import type { ApiClient } from '../api'
import type { AppStore } from '../store'
import type { AgentSkill, AgentSkillDetail } from '../types'
import type { AgentKitSkill } from '../agentkit/composer'
import { confirmDialog } from '../portalkit/confirm'
import { providerFetch } from '../portalkit/tenant'
import { createKubeClient, isKubeConflict, type KubeObject } from '../portalkit/kube'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import InlineNotification from '../portalkit/InlineNotification.vue'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import { ensureAgentUIStyles } from '../agentkit/styles'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import { hashFor, type HashHistoryMode, type Route } from '../router'
import AISkillCatalogList from '../agentkit/AISkillCatalogList.vue'
import { toast } from '../portalkit/toast'
import { useAuthorityGuard } from '../vue/runtime'
import { createSkillDocument, parseSkillDocument, skillPackageName, validateSkillResources } from '../skill-document'
import { WORKSPACE_SKILLS, type SkillLibrarySource } from '../skill-library'

ensureAgentUIStyles()

const props = defineProps<{ store: AppStore; api: ApiClient; returnAgent?: string; authorityEpoch?: number; createRoute?: boolean }>()
const emit = defineEmits<{ navigate: [route: Route, history?: HashHistoryMode] }>()
const { captureAuthority, authorityIsCurrent } = useAuthorityGuard(() => props.store, () => props.api)

interface SkillResourceWrite { name: string; content: string }
interface WorkspaceSkillCR extends KubeObject {
  apiVersion: string
  kind: string
  spec?: { document?: string; resources?: SkillResourceWrite[] }
}
const catalog = ref<AgentSkill[]>([])
const catalogWarnings = ref<string[]>([])
const unavailableSources = ref<SkillLibrarySource[]>([])
const catalogLoading = ref(false)
const catalogHasSnapshot = ref(false)
const catalogIncomplete = computed(() => catalogWarnings.value.length > 0 || unavailableSources.value.length > 0)
const catalogError = ref('')
const catalogNotice = ref<HTMLElement | null>(null)
const catalogSection = ref<HTMLElement | null>(null)
const firstRunCreate = ref<HTMLAnchorElement | null>(null)
const selectedSkill = ref<AgentKitSkill | null>(null)
const detail = ref<AgentSkillDetail | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
const detailNotice = ref<HTMLElement | null>(null)
const detailSection = ref<HTMLElement | null>(null)
const search = ref('')
const editing = ref(false)
const mode = computed(() => props.createRoute ? 'create' : editing.value ? 'edit' : 'detail')
const editorHeading = ref<HTMLElement | null>(null)
const editorNotice = ref<HTMLElement | null>(null)
const editSource = ref<{ uid: string; resourceVersion: string } | null>(null)
const packageName = ref('')
const skillDocument = ref('')
const resources = ref<SkillResourceWrite[]>([])
const validationError = ref('')
const saveError = ref('')
const saving = ref(false)
const deleting = ref(false)
const deleteFailure = ref('')
const skillDeleteButton = ref<HTMLButtonElement | null>(null)
watch(() => selectedSkill.value?.id, () => { deleteFailure.value = '' })
const importing = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
let catalogSerial = 0
let detailSerial = 0
let requestSerial = 0
let selectAfterCreate: string | undefined

const sortedCatalog = computed(() => [...catalog.value].sort((left, right) => left.name.localeCompare(right.name) || left.id.localeCompare(right.id)))
const readOnlySkill = computed(() => selectedSkill.value && selectedSkill.value.editable !== true)
const selectedSkillUnverified = computed(() => Boolean(
  selectedSkill.value && (unavailableSources.value.includes(librarySource(selectedSkill.value)) ||
    (catalogIncomplete.value && !catalog.value.some((skill) => skill.id === selectedSkill.value?.id))),
))
const workbenchLocked = computed(() => saving.value || deleting.value || importing.value)
const canEditDetail = computed(() => !detailLoading.value && !detailError.value && !workbenchLocked.value &&
  !selectedSkillUnverified.value && !!detail.value?.version && !!detail.value.sourceUID)
const libraryRoute = computed<Route>(() => ({ kind: 'menu', menu: 'skills', returnAgent: props.returnAgent }))
const createSkillRoute = computed<Route>(() => ({ kind: 'create', resource: 'skill', returnAgent: props.returnAgent }))
const firstRun = computed(() => catalogHasSnapshot.value && !catalog.value.length &&
  !catalogWarnings.value.length && !unavailableSources.value.length)
const catalogProblem = computed(() => {
  if (catalogError.value) return catalogHasSnapshot.value
    ? `Could not refresh the skill library. Showing the last loaded catalog. ${catalogError.value}`
    : `Could not load the skill library. ${catalogError.value}`
  const source = unavailableSources.value[0]
  if (!source) return catalogWarnings.value.length
    ? 'Catalog needs attention. Returned skills remain usable, but the list may be incomplete. Review the warning details below.'
    : ''
  const name = source === 'workspace' ? 'workspace skills' : 'provider skills'
  const hasRetained = catalog.value.some(skill => librarySource(skill) === source)
  return hasRetained
    ? `Could not refresh ${name}. Showing the last loaded ${name}.`
    : `Could not load ${name}.`
})

function librarySource(skill: Pick<AgentKitSkill, 'scope'>): SkillLibrarySource {
  return skill.scope === 'workspace' ? 'workspace' : 'provider'
}

function isCurrent(serial: number, api: ApiClient): boolean {
  return serial === requestSerial && props.api === api
}

async function loadCatalog(selectID?: string): Promise<void> {
  const serial = ++catalogSerial
  const api = props.api
  const authority = captureAuthority()
  const restoreRetryFocus = Boolean(catalogNotice.value?.contains(document.activeElement))
  catalogLoading.value = true
  try {
    const result = await api.getSkillLibrary()
    if (serial !== catalogSerial || !authorityIsCurrent(authority) || props.api !== api) return
    unavailableSources.value = result.unavailableSources || []
    const retained = catalog.value.filter(skill => unavailableSources.value.includes(librarySource(skill)))
    catalog.value = [...(result.skills || []), ...retained]
    catalogWarnings.value = result.warnings || []
    catalogHasSnapshot.value = true
    catalogError.value = ''
    if (selectID) {
      const next = catalog.value.find((skill) => skill.id === selectID)
      if (next) await selectSkill(next, true)
      else if (!catalogIncomplete.value || selectedSkill.value?.id !== selectID) { selectedSkill.value = null; detail.value = null }
    } else if (selectedSkill.value) {
      const current = catalog.value.find((skill) => skill.id === selectedSkill.value?.id)
      if (current) selectedSkill.value = current
      else if (!catalogIncomplete.value) { selectedSkill.value = null; detail.value = null }
    }
    await nextTick()
    if (
      restoreRetryFocus &&
      serial === catalogSerial &&
      authorityIsCurrent(authority) &&
      props.api === api &&
      focusWasLost()
    ) (firstRunCreate.value || catalogSection.value)?.focus({ preventScroll: true })
  } catch (error) {
    if (serial === catalogSerial && authorityIsCurrent(authority) && props.api === api) {
      catalogError.value = (error as Error).message
      await nextTick()
      if (
        restoreRetryFocus &&
        serial === catalogSerial &&
        authorityIsCurrent(authority) &&
        props.api === api &&
        focusWasLost()
      ) catalogNotice.value?.querySelector<HTMLButtonElement>('button')?.focus({ preventScroll: true })
    }
  } finally {
    if (serial === catalogSerial) catalogLoading.value = false
  }
}

function focusWasLost(): boolean {
  return !document.activeElement || document.activeElement === document.body || !document.activeElement.isConnected
}

function retryCatalog(): void {
  if (!catalogLoading.value) void loadCatalog()
}

async function loadSkillDetail(skill: AgentKitSkill): Promise<void> {
  const serial = ++detailSerial
  const api = props.api
  const authority = captureAuthority()
  detailLoading.value = true
  try {
    const result = await api.getSkillLibraryDetail(skill.id)
    if (serial !== detailSerial || !authorityIsCurrent(authority) || props.api !== api || selectedSkill.value?.id !== skill.id) return
    const restoreFocus = Boolean(detailError.value && detailNotice.value?.contains(document.activeElement))
    detail.value = result
    detailError.value = ''
    await nextTick()
    if (
      restoreFocus &&
      serial === detailSerial &&
      authorityIsCurrent(authority) &&
      props.api === api &&
      selectedSkill.value?.id === skill.id &&
      focusWasLost()
    ) detailSection.value?.focus({ preventScroll: true })
  } catch (error) {
    if (serial === detailSerial && authorityIsCurrent(authority) && props.api === api && selectedSkill.value?.id === skill.id) detailError.value = (error as Error).message
  } finally {
    if (serial === detailSerial) detailLoading.value = false
  }
}

async function selectSkill(skill: AgentKitSkill, allowLocked = false): Promise<void> {
  if (workbenchLocked.value && !allowLocked) return
  const sameSkill = selectedSkill.value?.id === skill.id
  selectedSkill.value = skill
  if (!sameSkill) {
    detail.value = null
    detailError.value = ''
  }
  editing.value = false
  await loadSkillDetail(skill)
}

function openSkill(skill: AgentKitSkill): void {
  if (workbenchLocked.value) return
  void selectSkill(skill)
  void nextTick(() => detailSection.value?.focus())
}

function retrySkillDetail(): void {
  const skill = selectedSkill.value
  if (skill && !detailLoading.value) void loadSkillDetail(skill)
}

function resetDraft(): void {
  editSource.value = null
  packageName.value = ''
  skillDocument.value = createSkillDocument('new-skill', 'Describe what this skill helps the agent do.', '')
  resources.value = []
  validationError.value = ''
  saveError.value = ''
}

function startCreate(event: MouseEvent): void {
  if (workbenchLocked.value) {
    event.preventDefault()
    return
  }
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  resetDraft()
  emit('navigate', createSkillRoute.value)
}

function leaveEditor(): void {
  if (workbenchLocked.value) return
  if (props.createRoute) emit('navigate', libraryRoute.value, 'replace')
  else {
    editing.value = false
    resetDraft()
    void nextTick(() => detailSection.value?.focus())
  }
}

function startEdit(): void {
  if (!canEditDetail.value) return
  const selected = selectedSkill.value
  const current = detail.value
  if (!selected || !current?.version || !current.sourceUID || !selected.editable || selected.scope !== 'workspace') return
  editSource.value = { uid: current.sourceUID, resourceVersion: current.version }
  editing.value = true
  packageName.value = selected.packageName || ''
  skillDocument.value = current.document ?? createSkillDocument(current.name, current.description, current.instructions || '')
  resources.value = (current.resources || []).map((resource) => ({ name: resource.path, content: resource.content || '' }))
  validationError.value = ''
  saveError.value = ''
  void nextTick(() => editorHeading.value?.focus())
}

function addResource(): void { resources.value = [...resources.value, { name: '', content: '' }] }
function updateResource(index: number, patch: Partial<SkillResourceWrite>): void {
  resources.value = resources.value.map((resource, row) => row === index ? { ...resource, ...patch } : resource)
}
function removeResource(index: number): void { resources.value = resources.value.filter((_, row) => row !== index) }

function validateEditor(): { name: string; description: string; document: string; resources: SkillResourceWrite[] } | null {
  validationError.value = ''
  try {
    const parsed = parseSkillDocument(skillDocument.value)
    const cleanResources = validateSkillResources(skillDocument.value, resources.value)
    const nextPackageName = mode.value === 'edit' ? packageName.value.trim() : packageName.value.trim() || skillPackageName(parsed.name)
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

const editConflictMessage = 'This skill changed since you opened it. Copy your draft before reopening the skill to review the latest version.'

async function save(): Promise<void> {
  const operation = mode.value
  const source = editSource.value
  if (operation === 'detail' || workbenchLocked.value || (operation === 'edit' && (!source || selectedSkillUnverified.value))) return
  const value = validateEditor()
  if (!value) {
    await nextTick()
    editorNotice.value?.focus()
    return
  }
  const savedPackageName = packageName.value
  const authority = captureAuthority()
  const api = authority.api
  const serial = ++requestSerial
  saving.value = true
  saveError.value = ''
  try {
    const client = kubeClient(api)
    if (operation === 'create') {
      await client.create<WorkspaceSkillCR>(WORKSPACE_SKILLS, {
        apiVersion: 'agents.railgrid.ai/v1alpha1',
        kind: 'Skill',
        metadata: { name: savedPackageName },
        spec: { document: value.document, resources: value.resources },
      })
    } else if (operation === 'edit' && source) {
      const currentName = savedPackageName
      const current = await client.get<WorkspaceSkillCR>(WORKSPACE_SKILLS, currentName)
      if (!isCurrent(serial, api) || !authorityIsCurrent(authority)) return
      if (current.metadata.uid !== source.uid || current.metadata.resourceVersion !== source.resourceVersion) {
        saveError.value = editConflictMessage
        return
      }
      await client.update<WorkspaceSkillCR>(WORKSPACE_SKILLS, {
        ...current,
        apiVersion: 'agents.railgrid.ai/v1alpha1',
        kind: 'Skill',
        spec: { ...(current.spec || {}), document: value.document, resources: value.resources },
      })
    }
    if (!isCurrent(serial, api) || !authorityIsCurrent(authority)) return
    toast('ok', operation === 'create' ? `Created workspace skill “${value.name}”.` : `Saved workspace skill “${value.name}”.`)
    const id = `workspace:${savedPackageName}`
    if (operation === 'create') {
      selectAfterCreate = id
      emit('navigate', libraryRoute.value, 'replace')
    } else {
      editing.value = false
      resetDraft()
      await loadCatalog(id)
      if (authorityIsCurrent(authority)) detailSection.value?.focus()
    }
  } catch (error) {
    if (isCurrent(serial, api) && authorityIsCurrent(authority)) {
      saveError.value = operation === 'edit' && isKubeConflict(error) ? editConflictMessage : (error as Error).message
      await nextTick()
      editorNotice.value?.focus()
    }
  } finally {
    if (serial === requestSerial) saving.value = false
  }
}

async function removeSkill(): Promise<void> {
  const selected = selectedSkill.value
  const packageID = selected?.packageName
  if (!selected || !packageID || !selected.editable || selected.scope !== 'workspace' || workbenchLocked.value || selectedSkillUnverified.value) return
  const authority = captureAuthority()
  const displayedUID = detail.value?.sourceUID
  if (!displayedUID) return
  const ok = await confirmDialog({ title: `Delete “${selected.name}”?`, message: 'This removes the workspace skill package for every agent in this workspace. Agents that enabled it will no longer be able to use it.', danger: true, confirmLabel: 'Delete skill' })
  if (!ok || !authorityIsCurrent(authority) || workbenchLocked.value || selectedSkillUnverified.value ||
      selectedSkill.value?.id !== selected.id || detail.value?.sourceUID !== displayedUID) return
  const api = authority.api
  const serial = ++requestSerial
  deleting.value = true
  try {
    const client = kubeClient(api)
    const current = await client.get<WorkspaceSkillCR>(WORKSPACE_SKILLS, packageID)
    if (!isCurrent(serial, api) || !authorityIsCurrent(authority)) return
    if (current.metadata.uid !== displayedUID) {
      deleteFailure.value = 'Refresh and review this skill before deleting it.'
      return
    }
    await client.delete(WORKSPACE_SKILLS, packageID, { preconditions: { uid: current.metadata.uid, resourceVersion: current.metadata.resourceVersion } })
    if (!isCurrent(serial, api) || !authorityIsCurrent(authority)) return
    deleteFailure.value = ''
    toast('ok', `Workspace skill deletion requested for ${selected.name}.`)
    catalog.value = catalog.value.filter(skill => skill.id !== selected.id)
    selectedSkill.value = null
    detail.value = null
    await loadCatalog()
  } catch (error) {
    if (isCurrent(serial, api) && authorityIsCurrent(authority)) {
      deleteFailure.value = isKubeConflict(error) ? 'Refresh and review this skill before deleting it.' : 'The request failed.'
    }
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
  if (!selected || !current || !selected.editable || !selected.packageName || selectedSkillUnverified.value) return
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
  importing.value = true
  const authority = captureAuthority()
  const serial = ++requestSerial
  try {
    const text = await file.text()
    if (serial !== requestSerial || !authorityIsCurrent(authority)) return
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
    validationError.value = ''
    saveError.value = ''
    emit('navigate', createSkillRoute.value)
  } catch (error) {
    if (serial === requestSerial && authorityIsCurrent(authority)) toast('error', `Could not import skill: ${(error as Error).message}`)
  } finally {
    if (input) input.value = ''
    if (serial === requestSerial) importing.value = false
  }
}

watch([() => props.returnAgent, () => props.api, () => props.authorityEpoch], () => {
  catalogSerial += 1
  detailSerial += 1
  requestSerial += 1
  catalog.value = []
  catalogWarnings.value = []
  unavailableSources.value = []
  catalogHasSnapshot.value = false
  catalogLoading.value = false
  catalogError.value = ''
  selectedSkill.value = null
  detail.value = null
  detailLoading.value = false
  detailError.value = ''
  deleteFailure.value = ''
  editing.value = false
  saving.value = false
  deleting.value = false
  importing.value = false
  selectAfterCreate = undefined
  resetDraft()
  if (!props.createRoute) void loadCatalog()
}, { immediate: true })

watch(() => props.createRoute, (creating) => {
  catalogSerial += 1
  detailSerial += 1
  requestSerial += 1
  catalogLoading.value = false
  detailLoading.value = false
  saving.value = false
  deleting.value = false
  importing.value = false
  if (creating) {
    if (editing.value) resetDraft()
    editing.value = false
    return
  }
  resetDraft()
  const id = selectAfterCreate
  selectAfterCreate = undefined
  if (id) search.value = ''
  void loadCatalog(id ?? (detail.value ? undefined : selectedSkill.value?.id))
})
</script>

<template>
  <div v-if="mode !== 'detail'" class="k-create-page">
    <ResourceBackLink :href="hashFor(libraryRoute)" :disabled="workbenchLocked" @back="leaveEditor">Skills library</ResourceBackLink>
    <header class="k-create-header">
      <h1 ref="editorHeading" class="k-create-title" tabindex="-1">{{ mode === 'create' ? 'Create skill' : 'Edit skill' }}</h1>
      <p class="k-create-description">Write instructions that agents in this workspace can reuse.</p>
    </header>
    <form class="k-create-surface" :aria-busy="saving" @submit.prevent="save">
      <div class="k-create-body">
        <div class="k-create-fields">
          <label v-if="mode === 'create'" class="grid min-w-0 gap-1 text-[13px] font-medium">
            Package name
            <input v-model="packageName" class="k-input font-mono" placeholder="my-skill" autocomplete="off" :disabled="workbenchLocked" aria-describedby="agents-skill-package-hint" />
            <span id="agents-skill-package-hint" class="text-[12px] font-normal text-text-secondary">Optional. Derived from the name in SKILL.md.</span>
          </label>
          <label class="grid min-w-0 gap-1 text-[13px] font-medium">
            SKILL.md
            <textarea v-model="skillDocument" class="agents-skill-workbench__document-editor k-input font-mono" rows="9" spellcheck="false" :disabled="workbenchLocked" aria-describedby="agents-skill-document-hint" />
            <span id="agents-skill-document-hint" class="text-[12px] font-normal text-text-secondary">Include a name, a description, and the instructions agents should follow.</span>
          </label>
          <fieldset class="grid min-w-0 gap-2 border-t border-border-subtle pt-3" :disabled="workbenchLocked">
            <legend class="pr-2 text-[13px] font-medium">Supporting files <span class="text-[12px] font-normal text-text-secondary">(optional)</span></legend>
            <p class="text-[12px] text-text-secondary">Include examples or references with the skill.</p>
            <div v-for="(resource, index) in resources" :key="index" class="grid min-w-0 gap-2 rounded-lg border border-border-subtle p-3">
              <label class="grid min-w-0 gap-1 text-[12px]">
                File path
                <input :value="resource.name" class="k-input font-mono" placeholder="references/example.md" @input="updateResource(index, { name: ($event.target as HTMLInputElement).value })" />
              </label>
              <label class="grid min-w-0 gap-1 text-[12px]">
                File contents
                <textarea :value="resource.content" class="agents-skill-workbench__resource-editor k-input font-mono" rows="4" @input="updateResource(index, { content: ($event.target as HTMLTextAreaElement).value })" />
              </label>
              <button type="button" class="k-btn k-btn--text justify-self-start" :aria-label="`Remove file ${resource.name || index + 1}`" @click="removeResource(index)">
                <Trash2 class="agents-skill-workbench__button-icon" aria-hidden="true" /> Remove file
              </button>
            </div>
            <button type="button" class="k-btn k-btn--ghost justify-self-start" @click="addResource"><Plus class="agents-skill-workbench__button-icon" aria-hidden="true" /> Add file</button>
          </fieldset>
          <div v-if="validationError || saveError" ref="editorNotice" tabindex="-1">
            <InlineNotification tone="error" :message="validationError || `Could not save skill: ${saveError}`" />
          </div>
        </div>
      </div>
      <div class="k-create-actions">
        <button type="button" class="k-btn k-btn--ghost" :disabled="workbenchLocked" @click="leaveEditor">Cancel</button>
        <button type="submit" class="k-btn k-btn--primary" :disabled="workbenchLocked || (mode === 'edit' && selectedSkillUnverified)" :aria-busy="saving ? 'true' : undefined">
          <Loader2 v-if="saving" class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" />
          {{ saving ? (mode === 'create' ? 'Creating…' : 'Saving…') : (mode === 'create' ? 'Create skill' : 'Save skill') }}
        </button>
      </div>
    </form>
  </div>

  <ResourcePage v-else class="agents-skill-workbench" title="Skills library" variant="collection"
    subtitle="Manage workspace skills and browse skills published by providers."
    :loaded="catalogHasSnapshot || undefined" :loading="catalogLoading && !catalogProblem">
    <template v-if="returnAgent" #summary>
      <ResourceBackLink :href="hashFor({ kind: 'agent', name: returnAgent, tab: 'skills' })" @back="emit('navigate', { kind: 'agent', name: returnAgent, tab: 'skills' })">Back to {{ returnAgent }}’s skills</ResourceBackLink>
    </template>
    <template v-if="!firstRun && (catalogHasSnapshot || catalogProblem)" #actions>
      <button type="button" class="k-btn k-btn--ghost" :disabled="workbenchLocked" :aria-busy="importing ? 'true' : undefined" @click="openImport">
        <Loader2 v-if="importing" class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" />
        <FileUp v-else class="agents-skill-workbench__button-icon" aria-hidden="true" />
        {{ importing ? 'Importing…' : 'Import skill' }}
      </button>
      <a class="k-btn k-btn--primary" :href="hashFor(createSkillRoute)" :aria-disabled="workbenchLocked || undefined" @click="startCreate">
        <Plus class="agents-skill-workbench__button-icon" aria-hidden="true" /> Create skill
      </a>
    </template>

    <input ref="fileInput" type="file" hidden accept=".md,.markdown,.json,application/json,text/markdown,text/plain" :disabled="workbenchLocked" @change="importFile" />
    <div v-if="catalogProblem" ref="catalogNotice" class="mb-3">
      <InlineNotification
        :tone="catalogHasSnapshot ? 'warning' : 'error'"
        :message="catalogProblem"
        action-label="Retry"
        action-busy-label="Retrying catalog…"
        :action-busy="catalogLoading"
        @action="retryCatalog"
      />
    </div>
    <p v-for="warning in catalogWarnings" :key="warning" class="agents-hint" role="status">{{ warning }}</p>

    <FirstRunGuide v-if="firstRun" title="Create your first skill"
      description="Give your agents reusable instructions for tasks such as reviewing a rollout or summarizing an incident."
      :steps="[]">
      <template #icon><Package :stroke-width="1.5" aria-hidden="true" /></template>
      <template #actions>
        <a ref="firstRunCreate" class="k-btn k-btn--primary" :href="hashFor(createSkillRoute)" :aria-disabled="workbenchLocked || undefined" @click="startCreate">
          <Plus class="agents-skill-workbench__button-icon" aria-hidden="true" /> Create skill
        </a>
        <button type="button" class="k-btn k-btn--ghost" :disabled="workbenchLocked" :aria-busy="importing ? 'true' : undefined" @click="openImport">
          <Loader2 v-if="importing" class="k-spin" :size="14" :stroke-width="1.75" aria-hidden="true" />
          <FileUp v-else class="agents-skill-workbench__button-icon" aria-hidden="true" />
          {{ importing ? 'Importing…' : 'Import skill' }}
        </button>
      </template>
    </FirstRunGuide>

    <div v-else-if="catalogHasSnapshot" class="agents-skill-workbench__columns" :class="{ 'has-detail': selectedSkill }">
      <section ref="catalogSection" tabindex="-1" class="k-card k-card--flat min-w-0 p-4" aria-label="Skill catalog">
        <AISkillCatalogList
          v-model:query="search"
          :skills="sortedCatalog"
          :show-status="false"
          :selectedID="selectedSkill?.id"
          :loading="catalogLoading && !!catalog.length"
          :busy="workbenchLocked"
          :incomplete="catalogIncomplete"
          variant="workbench"
          title="Workspace and provider skills"
          empty-text="No skills are available from the sources that loaded."
          @select="openSkill"
        />
      </section>

      <div v-if="selectedSkill" ref="detailSection" tabindex="-1" role="region" aria-label="Skill details" class="min-w-0">
        <ResourceSectionCard :title="selectedSkill.name" :description="selectedSkill.description">
          <template v-if="selectedSkill.editable" #actions>
            <button type="button" class="k-btn k-btn--ghost" :disabled="detailLoading || workbenchLocked || selectedSkillUnverified || !detail" @click="exportSkill"><Download class="agents-skill-workbench__button-icon" aria-hidden="true" /> Export</button>
            <button type="button" class="k-btn k-btn--ghost" :disabled="!canEditDetail" @click="startEdit"><Pencil class="agents-skill-workbench__button-icon" aria-hidden="true" /> Edit</button>
            <button
              ref="skillDeleteButton"
              type="button"
              class="k-icon-action agents-iconbtn-danger"
              :disabled="!deleting && (workbenchLocked || selectedSkillUnverified || !detail?.sourceUID)"
              :aria-disabled="deleting || undefined"
              :aria-busy="deleting || undefined"
              :aria-label="deleting ? 'Deleting workspace skill…' : 'Delete workspace skill'"
              title="Delete workspace skill"
              @click="removeSkill"
            >
              <Loader2 v-if="deleting" class="agents-skill-workbench__button-icon k-spin" aria-hidden="true" />
              <Trash2 v-else class="agents-skill-workbench__button-icon" aria-hidden="true" />
            </button>
          </template>
          <InlineNotification
            v-if="selectedSkillUnverified"
            tone="warning"
            title="Skill not listed in the latest catalog response."
            message="Its existing details are retained. Retry to verify the skill before editing, exporting, or deleting it."
          />
          <p class="text-[11px] text-text-secondary">
            {{ readOnlySkill ? 'Provider skill · Read-only' : 'Workspace skill' }}
            <span aria-hidden="true"> · </span>
            <code class="break-all" :title="selectedSkill.id">{{ selectedSkill.packageName || selectedSkill.packagePath }}</code>
          </p>
          <ResourceDeleteFailure
            v-if="deleteFailure"
            :title="`Could not delete workspace skill ${selectedSkill.name}`"
            :message="deleteFailure"
            :return-focus="skillDeleteButton"
            retry-hint="Use Delete workspace skill to try again."
            @dismiss="deleteFailure = ''"
          />
          <div v-if="detailError" ref="detailNotice">
            <InlineNotification
              :tone="detail ? 'warning' : 'error'"
              :message="detail ? `Could not refresh this skill detail. Showing the last loaded details. ${detailError}` : `Could not load skill detail. ${detailError}`"
              action-label="Retry"
              action-busy-label="Retrying detail…"
              :action-busy="detailLoading"
              @action="retrySkillDetail"
            />
          </div>
          <LoadingSkeleton v-if="detailLoading && !detail" label="Loading skill…" compact />
          <template v-else-if="detail">
            <div>
              <h3 class="mb-2 text-[13px] font-semibold">Instructions</h3>
              <pre class="max-h-[50vh] overflow-auto whitespace-pre-wrap break-words rounded-md bg-surface-overlay p-3 text-[12px] leading-5 text-text-secondary">{{ detail.instructions }}</pre>
            </div>
            <div v-if="detail.resources?.length">
              <h3 class="text-[13px] font-semibold">Supporting files</h3>
              <details v-for="resource in detail.resources" :key="resource.path" class="border-b border-border-subtle py-2 last:border-0">
                <summary class="cursor-pointer text-[12px] font-mono break-all">{{ resource.path }} <span class="text-text-muted">· {{ resource.size }} bytes</span></summary>
                <pre class="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-surface-overlay p-3 text-[12px] text-text-secondary">{{ resource.content }}</pre>
              </details>
            </div>
          </template>
        </ResourceSectionCard>
      </div>
    </div>
  </ResourcePage>
</template>
