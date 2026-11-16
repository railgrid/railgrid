<script lang="ts">
import type { RailgridContext as SkillsRailgridContext } from './types'

export interface AssistantSkillsRequestScope {
  serial: number
  projectName: string
  ctx: SkillsRailgridContext | null
}

/**
 * A catalog response may only update the view that initiated it. Keep this
 * pure so request-race behavior can be checked without a browser mount.
 */
export function assistantSkillsRequestIsCurrent(
  scope: AssistantSkillsRequestScope,
  currentSerial: number,
  currentProjectName: string,
  currentContext: SkillsRailgridContext | null,
): boolean {
  return scope.serial === currentSerial &&
    scope.projectName === currentProjectName &&
    scope.ctx === currentContext
}
</script>

<script setup lang="ts">
import AIWorkbenchPane from './agentkit/AIWorkbenchPane.vue'
import AISkillCatalogList from './agentkit/AISkillCatalogList.vue'
import InlineNotification from './portalkit/InlineNotification.vue'
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import {
  Check,
  Loader2,
  RefreshCw,
  Plug,
  TriangleAlert,
  X,
} from 'lucide-vue-next'
import { api, ProjectAPIRequestError } from './api'
import type {
  RailgridContext,
  ProjectAssistantSkill,
  ProjectAssistantSkillDetail,
  ProjectAssistantSkillsResponse,
} from './types'

const props = withDefaults(defineProps<{
  ctx: RailgridContext | null
  projectName: string
  skills: ProjectAssistantSkill[]
  hasSnapshot?: boolean
  loading?: boolean
  error?: string | null
  warnings?: string[]
}>(), {
  loading: false,
  hasSnapshot: false,
  error: null,
  warnings: () => [],
})

const emit = defineEmits<{
  catalogUpdated: [response: ProjectAssistantSkillsResponse]
}>()

const localSkills = ref<ProjectAssistantSkill[]>([...props.skills])
const query = ref('')
const selectedSkillID = ref('')
const focusedSkill = ref<ProjectAssistantSkill | null>(null)
const selectedDetail = ref<ProjectAssistantSkillDetail | null>(null)
const detailLoading = ref(false)
const detailError = ref<string | null>(null)
const detailErrorNotice = ref<HTMLElement | null>(null)
let detailLoadSerial = 0
const catalogLoading = ref(false)
const catalogError = ref<string | null>(null)
const catalogErrorNotice = ref<HTMLElement | null>(null)
const detailCatalogErrorNotice = ref<HTMLElement | null>(null)
const skillsRegion = ref<HTMLElement | null>(null)
let catalogRequestSerial = 0
let catalogScopeGeneration = 0
let catalogFailureGeneration = 0
const actionBusy = ref(false)
const activationSkillID = ref('')
let activationRequestSerial = 0
const managementError = ref<string | null>(null)
const statusMessage = ref<string | null>(null)
const detailDialogRef = ref<HTMLElement | null>(null)
const detailCloseRef = ref<HTMLButtonElement | null>(null)
let detailOpener: HTMLElement | null = null
let inertWorkspace: HTMLElement | null = null
let workspaceWasInert = false
let previousBodyOverflow = ''
let detailModalActive = false

watch(() => props.skills, (skills) => {
  localSkills.value = [...skills]
  const refreshedSelection = skills.find((skill) => skill.id === selectedSkillID.value)
  if (refreshedSelection) focusedSkill.value = refreshedSelection
  else if (selectedSkillID.value && skills.length) {
    selectedSkillID.value = ''
    focusedSkill.value = null
    selectedDetail.value = null
  }
}, { deep: true })

watch(() => [props.projectName, props.ctx] as const, () => {
  catalogScopeGeneration++
  detailLoadSerial++
  catalogRequestSerial++
  activationRequestSerial++
  localSkills.value = [...props.skills]
  query.value = ''
  selectedSkillID.value = ''
  focusedSkill.value = null
  selectedDetail.value = null
  detailError.value = null
  catalogError.value = null
  managementError.value = null
  statusMessage.value = null
  catalogLoading.value = false
  actionBusy.value = false
  activationSkillID.value = ''
})

const normalizedQuery = computed(() => query.value.trim().toLowerCase())
const selectedSkill = computed(() => localSkills.value.find((skill) => skill.id === selectedSkillID.value) ?? focusedSkill.value)
const selectedDigest = computed(() => selectedDetail.value?.digest || selectedSkill.value?.digest || selectedDetail.value?.contentDigest || selectedSkill.value?.contentDigest || '')
const detailInstructions = computed(() => selectedDetail.value?.instructions || selectedDetail.value?.content || selectedDetail.value?.authorInstructions || '')
const catalogFailure = computed(() => catalogError.value || props.error)
const emptyStateText = computed(() => {
  if (catalogFailure.value) {
    if (normalizedQuery.value && props.hasSnapshot) return 'No skills in the last successful catalog match your search.'
    return props.hasSnapshot ? 'No skills were listed in the last successful catalog.' : 'The catalog is unavailable. Retry to browse skills.'
  }
  if (normalizedQuery.value) return 'No skills match your search.'
  if (!props.hasSnapshot) return 'Skills are not available until the first catalog read completes.'
  return 'No skills are installed for this project yet.'
})

function focusWasLost(): boolean {
  return !document.activeElement || document.activeElement === document.body || !document.activeElement.isConnected
}

watch(catalogFailure, async (error, previous) => {
  if (error) {
    catalogFailureGeneration = catalogScopeGeneration
    return
  }
  if (!previous || catalogFailureGeneration !== catalogScopeGeneration) return
  const notice = selectedSkill.value ? detailCatalogErrorNotice : catalogErrorNotice
  if (!notice.value?.contains(document.activeElement)) return
  const scopeGeneration = catalogScopeGeneration
  const failureGeneration = catalogFailureGeneration
  const projectName = props.projectName
  const requestContext = props.ctx
  const selectedID = selectedSkillID.value
  await nextTick()
  if (
    scopeGeneration !== catalogScopeGeneration ||
    failureGeneration !== catalogFailureGeneration ||
    catalogFailure.value ||
    props.projectName !== projectName ||
    props.ctx !== requestContext ||
    selectedSkillID.value !== selectedID ||
    !focusWasLost()
  ) return
  const target = selectedSkill.value ? detailCloseRef : skillsRegion
  target.value?.focus({ preventScroll: true })
})

function packageNameFor(skill: ProjectAssistantSkill | null | undefined): string {
  if (!skill) return ''
  if (skill.packageName) return skill.packageName
  const separator = skill.id.indexOf(':')
  return separator >= 0 ? skill.id.slice(separator + 1) : skill.id
}

function sourceLabel(skill: ProjectAssistantSkill): string {
  return skill.scope?.toLowerCase() === 'system' ? 'System' : 'Project'
}

function clearManagementError() {
  managementError.value = null
}

function clearFeedback() {
  clearManagementError()
  statusMessage.value = null
}

function selectSkill(skill: ProjectAssistantSkill, options: { clearFeedback?: boolean } = {}) {
  if (actionBusy.value) return
  if (options.clearFeedback !== false) clearFeedback()
  if (!selectedSkill.value) detailOpener = document.activeElement instanceof HTMLElement ? document.activeElement : null
  selectedSkillID.value = skill.id
  focusedSkill.value = skill
  selectedDetail.value = null
  detailError.value = null
  void loadDetail(skill)
}

function closeSkillDetail() {
  if (actionBusy.value) return
  selectedSkillID.value = ''
  focusedSkill.value = null
  selectedDetail.value = null
  detailError.value = null
}

function setDetailModalEnvironment(active: boolean) {
  if (active === detailModalActive) return
  detailModalActive = active
  if (active) {
    inertWorkspace = document.querySelector<HTMLElement>('[data-app-studio-workspace]')
    workspaceWasInert = inertWorkspace?.inert ?? false
    if (inertWorkspace) inertWorkspace.inert = true
    previousBodyOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return
  }
  if (inertWorkspace && !workspaceWasInert) inertWorkspace.inert = false
  inertWorkspace = null
  workspaceWasInert = false
  document.body.style.overflow = previousBodyOverflow
}

function detailFocusableControls(): HTMLElement[] {
  return Array.from(detailDialogRef.value?.querySelectorAll<HTMLElement>(
    'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
  ) ?? [])
}

function handleDetailKeydown(event: KeyboardEvent) {
  if (!selectedSkill.value) return
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    if (!actionBusy.value) closeSkillDetail()
    return
  }
  if (event.key !== 'Tab') return
  const focusable = detailFocusableControls()
  if (!focusable.length) {
    event.preventDefault()
    detailDialogRef.value?.focus()
    return
  }
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (!detailDialogRef.value?.contains(document.activeElement)) {
    event.preventDefault()
    ;(event.shiftKey ? last : first).focus()
  } else if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

watch(() => Boolean(selectedSkill.value), async (open) => {
  if (open) {
    if (!detailOpener) detailOpener = document.activeElement instanceof HTMLElement ? document.activeElement : null
    setDetailModalEnvironment(true)
    await nextTick()
    detailCloseRef.value?.focus({ preventScroll: true })
    return
  }
  setDetailModalEnvironment(false)
  const opener = detailOpener
  detailOpener = null
  await nextTick()
  if (opener?.isConnected) opener.focus({ preventScroll: true })
})

onBeforeUnmount(() => setDetailModalEnvironment(false))

async function loadDetail(skill: ProjectAssistantSkill) {
  if (!props.projectName) return
  const skillID = skill.id.trim()
  if (!skillID) return
  const projectName = props.projectName
  const requestContext = props.ctx
  const serial = ++detailLoadSerial
  detailLoading.value = true
  try {
    const detail = await api.getAssistantSkillDetail(requestContext, projectName, skill.id)
    if (serial === detailLoadSerial && selectedSkillID.value === skillID) {
      const restoreFocus = Boolean(detailError.value && detailErrorNotice.value?.contains(document.activeElement))
      selectedDetail.value = detail
      detailError.value = null
      await nextTick()
      if (
        restoreFocus &&
        serial === detailLoadSerial &&
        props.projectName === projectName &&
        props.ctx === requestContext &&
        selectedSkillID.value === skillID &&
        focusWasLost()
      ) detailCloseRef.value?.focus({ preventScroll: true })
    }
  } catch (error) {
    if (
      serial !== detailLoadSerial ||
      props.projectName !== projectName ||
      props.ctx !== requestContext ||
      selectedSkillID.value !== skillID
    ) return
    detailError.value = friendlyError(error, 'Could not load this skill detail.')
  } finally {
    if (serial === detailLoadSerial) detailLoading.value = false
  }
}

function retrySkillDetail() {
  const skill = focusedSkill.value
  if (skill && !detailLoading.value) void loadDetail(skill)
}

async function refreshCatalog(selectSkillID = selectedSkillID.value, options: { preserveDetail?: boolean } = {}): Promise<boolean> {
  const projectName = props.projectName
  const requestContext = props.ctx
  if (!projectName || catalogLoading.value) return false
  const requestScope: AssistantSkillsRequestScope = {
    serial: ++catalogRequestSerial,
    projectName,
    ctx: requestContext,
  }
  catalogLoading.value = true
  try {
    const response = await api.listAssistantSkills(requestContext, projectName)
    if (!assistantSkillsRequestIsCurrent(requestScope, catalogRequestSerial, props.projectName, props.ctx)) return false
    localSkills.value = response.skills
    catalogError.value = null
    emit('catalogUpdated', response)
    if (selectSkillID) {
      const next = response.skills.find((skill) => skill.id === selectSkillID)
      if (next && options.preserveDetail && selectedSkillID.value === selectSkillID) {
        focusedSkill.value = next
      } else if (next) selectSkill(next, { clearFeedback: false })
      else {
        selectedSkillID.value = ''
        selectedDetail.value = null
      }
    }
    return true
  } catch (error) {
    if (!assistantSkillsRequestIsCurrent(requestScope, catalogRequestSerial, props.projectName, props.ctx)) return false
    catalogError.value = friendlyError(error, 'Could not refresh the skill catalog.')
    return false
  } finally {
    if (assistantSkillsRequestIsCurrent(requestScope, catalogRequestSerial, props.projectName, props.ctx)) catalogLoading.value = false
  }
}

async function retryDetailCatalog() {
  if (actionBusy.value) return
  actionBusy.value = true
  try {
    await refreshCatalog(selectedSkillID.value, { preserveDetail: true })
  } finally { actionBusy.value = false }
}

async function toggleSelectedSkill() {
  const skill = selectedSkill.value
  const projectName = props.projectName
  const requestContext = props.ctx
  if (!skill || !projectName || actionBusy.value) return
  const serial = ++activationRequestSerial
  actionBusy.value = true
  activationSkillID.value = skill.id
  clearFeedback()
  try {
    const updated = await api.setAssistantSkillActivation(props.ctx, props.projectName, skill.id, skill.enabled === false)
    if (serial !== activationRequestSerial || props.projectName !== projectName || props.ctx !== requestContext) return
    const successMessage = skill.enabled === false ? 'Skill enabled for future turns.' : 'Skill disabled for future turns.'
    if (updated.id === skill.id && typeof updated.enabled === 'boolean') {
      localSkills.value = localSkills.value.map(item => item.id === skill.id ? { ...item, enabled: updated.enabled } : item)
      focusedSkill.value = { ...skill, enabled: updated.enabled }
    }
    statusMessage.value = successMessage
    await refreshCatalog(skill.id, { preserveDetail: true })
  } catch (error) {
    if (serial === activationRequestSerial && props.projectName === projectName && props.ctx === requestContext) {
      managementError.value = friendlyError(error, 'Could not update skill activation.')
    }
  } finally {
    if (serial === activationRequestSerial) {
      actionBusy.value = false
      activationSkillID.value = ''
    }
  }
}

function friendlyError(error: unknown, fallback: string): string {
  if (error instanceof ProjectAPIRequestError && error.status === 409) return 'The skill changed since this view loaded.'
  return error instanceof Error && error.message ? error.message : fallback
}
</script>

<template>
  <AIWorkbenchPane title="Skills" description="Task-specific guidance available to App Studio.">
    <template #actions>
      <button
        type="button"
        class="k-btn k-btn--ghost"
        :disabled="catalogLoading || actionBusy || props.loading || !projectName"
        :aria-busy="catalogLoading ? 'true' : undefined"
        aria-label="Refresh skills"
        @click="refreshCatalog()"
      >
        <Loader2 v-if="catalogLoading" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
        <RefreshCw v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
        Refresh
      </button>
    </template>
    <div v-if="!projectName" class="rounded-xl border border-warning/30 bg-warning-subtle p-4 text-[12px] leading-5 text-warning" role="status">
      Open an existing project to manage its skills. Bundled skills become available when a project is selected.
    </div>

    <div v-if="catalogFailure && !selectedSkill" ref="catalogErrorNotice" class="mb-3">
      <InlineNotification
        :tone="props.hasSnapshot ? 'warning' : 'error'"
        :message="props.hasSnapshot ? `Could not refresh the skills catalog. Showing the last successful result. ${catalogFailure}` : `Could not load the skills catalog. ${catalogFailure}`"
        action-label="Retry"
        action-busy-label="Retrying skills…"
        :action-busy="catalogLoading || actionBusy || props.loading"
        @action="refreshCatalog()"
      />
    </div>
    <div v-if="managementError && !selectedSkill" class="flex items-start gap-2 rounded-xl border border-danger/30 bg-danger-subtle p-3 text-[12px] leading-5 text-danger" role="alert">
      <TriangleAlert class="mt-0.5 h-4 w-4 shrink-0" :stroke-width="1.75" />
      <span>{{ managementError }}</span>
    </div>
    <div v-if="props.warnings.length" class="flex items-start gap-2 rounded-xl border border-warning/30 bg-warning-subtle p-3 text-[12px] leading-5 text-warning" role="status">
      <TriangleAlert class="mt-0.5 h-4 w-4 shrink-0" :stroke-width="1.75" />
      <div class="grid gap-1">
        <div v-for="warning in props.warnings" :key="warning">{{ warning }}</div>
      </div>
    </div>
    <div v-if="statusMessage && !selectedSkill" class="flex items-center gap-2 rounded-xl border border-success/30 bg-success-subtle p-3 text-[12px] text-success" role="status">
      <Check class="h-4 w-4 shrink-0" :stroke-width="1.75" />
      {{ statusMessage }}
    </div>

    <div ref="skillsRegion" tabindex="-1" role="region" aria-label="Installed skills" class="min-w-0 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40">
      <AISkillCatalogList
        v-if="props.hasSnapshot"
        variant="workbench"
        title="Installed skills"
        :skills="localSkills"
        :query="query"
        :selected-i-d="selectedSkillID"
        :loading="props.loading || catalogLoading"
        :busy="actionBusy"
        detail-presentation="dialog"
        :empty-text="emptyStateText"
        @update:query="query = $event"
        @select="selectSkill($event)"
      />
      <p v-else class="px-4 py-8 text-center text-[12px] text-text-muted" role="status">
        {{ props.loading || catalogLoading ? 'Loading the skills catalog…' : catalogFailure ? 'Skill count is unavailable until the catalog loads.' : 'Skills will appear after the first catalog read.' }}
      </p>
    </div>

    <Teleport to="#app-studio-overlay-root">
      <div v-if="selectedSkill" class="fixed inset-0 flex items-center justify-center bg-surface/60 p-4 [z-index:var(--app-studio-z-modal-backdrop)] backdrop-blur-[2px]" role="presentation" @mousedown.self="closeSkillDetail">
        <section ref="detailDialogRef" tabindex="-1" class="skill-detail-dialog flex max-h-[min(860px,calc(100vh-2rem))] w-full max-w-3xl flex-col overflow-hidden rounded-xl border border-border-default bg-surface-raised shadow-2xl" role="dialog" aria-modal="true" :aria-labelledby="`skill-detail-title-${selectedSkill.id}`" :aria-busy="activationSkillID === selectedSkill.id ? 'true' : undefined" @keydown="handleDetailKeydown">
          <header class="skill-detail-header flex items-start gap-4 px-6 pb-5 pt-6">
            <span class="flex h-14 w-14 shrink-0 items-center justify-center rounded-xl border border-border-subtle bg-accent-subtle text-accent">
              <Plug class="h-7 w-7" :stroke-width="1.5" aria-hidden="true" />
            </span>
            <div class="skill-detail-heading min-w-0 flex-1 pt-0.5">
              <div class="flex flex-wrap items-baseline gap-x-2">
                <h3 :id="`skill-detail-title-${selectedSkill.id}`" class="skill-detail-title min-w-0 break-words text-[20px] font-semibold leading-tight text-text-primary">{{ selectedSkill.name }}</h3>
                <span class="text-[16px] text-text-muted">Skill</span>
              </div>
              <p class="mt-1 text-[13px] leading-5 text-text-secondary">{{ selectedSkill.description || 'No description provided.' }}</p>
            </div>
            <div class="skill-detail-actions mt-1 flex shrink-0 items-center gap-2">
              <span v-if="activationSkillID === selectedSkill.id" class="flex items-center gap-1 text-[11px] text-text-muted" role="status" aria-live="polite">
                <Loader2 class="h-3.5 w-3.5 animate-spin text-accent" :stroke-width="1.75" aria-hidden="true" />
                <span>Saving…</span>
              </span>
              <button
                type="button"
                role="switch"
                :aria-checked="selectedSkill.enabled !== false"
                aria-label="Enable skill"
                class="app-studio-touch-target flex h-11 shrink-0 items-center justify-center gap-2.5 rounded-md bg-transparent px-1 text-[12px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
                :aria-disabled="actionBusy ? 'true' : undefined"
                :aria-busy="activationSkillID === selectedSkill.id ? 'true' : undefined"
                :class="{ 'opacity-60': actionBusy }"
                @click="toggleSelectedSkill"
              >
                <span>{{ selectedSkill.enabled === false ? 'Skill disabled' : 'Skill enabled' }}</span>
                <span class="k-toggle" aria-hidden="true" :aria-checked="selectedSkill.enabled !== false">
                  <span class="k-toggle__knob" />
                </span>
              </button>
              <div class="ml-2 border-l border-border-subtle pl-2">
              <button ref="detailCloseRef" type="button" class="app-studio-touch-target flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-60" :disabled="actionBusy" aria-label="Close skill details" @click="closeSkillDetail">
                <X class="h-5 w-5" :stroke-width="1.75" />
              </button>
              </div>
            </div>
          </header>

          <div class="min-h-0 flex-1 overflow-y-auto border-t border-border-subtle px-6 py-5">
            <div v-if="catalogFailure" ref="detailCatalogErrorNotice" class="mb-4">
              <InlineNotification
                :tone="props.hasSnapshot ? 'warning' : 'error'"
                :message="props.hasSnapshot ? `Could not refresh the skills catalog. Showing the last successful result. ${catalogFailure}` : `Could not load the skills catalog. ${catalogFailure}`"
                action-label="Retry"
                action-busy-label="Retrying skills…"
                :action-busy="catalogLoading || actionBusy || props.loading"
                @action="retryDetailCatalog"
              />
            </div>
            <div v-if="managementError" class="mb-4 rounded-xl border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger" role="alert">{{ managementError }}</div>
            <div v-if="statusMessage" class="mb-4 rounded-xl border border-success/30 bg-success-subtle p-3 text-[12px] text-success" role="status">{{ statusMessage }}</div>
            <div v-if="detailError" ref="detailErrorNotice" class="mb-4">
              <InlineNotification
                :tone="selectedDetail ? 'warning' : 'error'"
                :message="selectedDetail ? `Could not refresh this skill detail. Showing the last loaded details. ${detailError}` : `Could not load this skill detail. ${detailError}`"
                action-label="Retry"
                action-busy-label="Retrying detail…"
                :action-busy="detailLoading"
                @action="retrySkillDetail"
              />
            </div>
            <div v-if="detailLoading && !selectedDetail" class="flex min-h-48 items-center justify-center gap-2 text-[12px] text-text-muted" role="status"><Loader2 class="h-4 w-4 animate-spin text-accent" :stroke-width="1.75" /> Loading skill…</div>
            <div v-else-if="selectedDetail" class="grid gap-4">
              <div class="rounded-xl border border-border-subtle bg-surface p-5">
                <pre v-if="detailInstructions" class="whitespace-pre-wrap font-sans text-[13px] leading-6 text-text-secondary">{{ detailInstructions }}</pre>
                <div v-else class="text-[12px] text-text-muted">No author-visible instructions were provided for this skill.</div>
              </div>
              <section v-if="(selectedDetail?.resources || selectedSkill.resources || []).length" class="grid gap-2">
                <h4 class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Supporting resources</h4>
                <ul class="grid gap-1">
                  <li v-for="resource in (selectedDetail?.resources || selectedSkill.resources || [])" :key="resource.path" class="flex items-center justify-between gap-2 rounded-lg bg-surface px-3 py-2 text-[11px]">
                    <code class="min-w-0 truncate font-mono text-text-secondary">{{ resource.path }}</code>
                    <span class="shrink-0 text-text-muted">{{ resource.size ? `${resource.size} B` : 'metadata' }}</span>
                  </li>
                </ul>
              </section>
            </div>
          </div>

          <footer class="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-border-subtle px-6 py-3 text-[10px] text-text-muted">
            <span>{{ sourceLabel(selectedSkill) }}</span>
            <code class="font-mono">{{ packageNameFor(selectedSkill) }}</code>
            <code v-if="selectedDigest" class="ml-auto max-w-48 truncate font-mono" :title="selectedDigest">{{ selectedDigest }}</code>
          </footer>
        </section>
      </div>
    </Teleport>
  </AIWorkbenchPane>
</template>

<style scoped>
.skill-detail-dialog {
  container-type: inline-size;
}

@container (max-width: 540px) {
  .skill-detail-header {
    display: grid;
    grid-template-columns: 56px minmax(0, 1fr);
    gap: 12px;
    padding: 16px;
  }

  .skill-detail-heading {
    min-width: 0;
  }

  .skill-detail-actions {
    grid-column: 1 / -1;
    justify-self: end;
    margin-top: 0;
  }
}
</style>
