<script lang="ts">
import type { FarosContext as SkillsFarosContext } from './types'

export interface AssistantSkillsRequestScope {
  serial: number
  projectName: string
  ctx: SkillsFarosContext | null
}

/**
 * A catalog response may only update the view that initiated it. Keep this
 * pure so request-race behavior can be checked without a browser mount.
 */
export function assistantSkillsRequestIsCurrent(
  scope: AssistantSkillsRequestScope,
  currentSerial: number,
  currentProjectName: string,
  currentContext: SkillsFarosContext | null,
): boolean {
  return scope.serial === currentSerial &&
    scope.projectName === currentProjectName &&
    scope.ctx === currentContext
}
</script>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import {
  Check,
  Loader2,
  RefreshCw,
  Search,
  Plug,
  TriangleAlert,
  X,
} from 'lucide-vue-next'
import { api, ProjectAPIRequestError } from './api'
import { filterAssistantSkills } from './skillsSearch'
import type {
  FarosContext,
  ProjectAssistantSkill,
  ProjectAssistantSkillDetail,
  ProjectAssistantSkillsResponse,
} from './types'

const props = withDefaults(defineProps<{
  ctx: FarosContext | null
  projectName: string
  skills: ProjectAssistantSkill[]
  loading?: boolean
  error?: string | null
  warnings?: string[]
}>(), {
  loading: false,
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
let detailLoadSerial = 0
const catalogLoading = ref(false)
let catalogRequestSerial = 0
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
  detailLoadSerial++
  catalogRequestSerial++
  activationRequestSerial++
  localSkills.value = [...props.skills]
  query.value = ''
  selectedSkillID.value = ''
  focusedSkill.value = null
  selectedDetail.value = null
  detailError.value = null
  managementError.value = null
  statusMessage.value = null
  catalogLoading.value = false
  actionBusy.value = false
  activationSkillID.value = ''
})

const normalizedQuery = computed(() => query.value.trim().toLowerCase())
const filteredSkills = computed(() => filterAssistantSkills(localSkills.value, query.value))
const selectedSkill = computed(() => localSkills.value.find((skill) => skill.id === selectedSkillID.value) ?? focusedSkill.value)
const selectedDigest = computed(() => selectedDetail.value?.digest || selectedSkill.value?.digest || selectedDetail.value?.contentDigest || selectedSkill.value?.contentDigest || '')
const detailInstructions = computed(() => selectedDetail.value?.instructions || selectedDetail.value?.content || selectedDetail.value?.authorInstructions || '')
const emptyStateText = computed(() => {
  if (props.error) return 'The catalog could not be loaded. Retry to browse skills.'
  if (normalizedQuery.value) return 'No skills match your search.'
  return 'No skills are installed for this project yet.'
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

function statusLabel(skill: ProjectAssistantSkill): string {
  return skill.enabled === false ? 'Disabled' : 'Enabled'
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
  const serial = ++detailLoadSerial
  detailLoading.value = true
  detailError.value = null
  try {
    const detail = await api.getAssistantSkillDetail(props.ctx, props.projectName, skill.id)
    if (serial === detailLoadSerial && selectedSkillID.value === skillID) selectedDetail.value = detail
  } catch (error) {
    if (serial !== detailLoadSerial || selectedSkillID.value !== skillID) return
    detailError.value = friendlyError(error, 'Could not load this skill detail.')
  } finally {
    if (serial === detailLoadSerial) detailLoading.value = false
  }
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
  // Refreshing the catalog may be part of a successful lifecycle action. It
  // should clear a stale error, but must not erase the action result before
  // the user can read it.
  clearManagementError()
  try {
    const response = await api.listAssistantSkills(requestContext, projectName)
    if (!assistantSkillsRequestIsCurrent(requestScope, catalogRequestSerial, props.projectName, props.ctx)) return false
    localSkills.value = response.skills
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
    managementError.value = friendlyError(error, 'Could not refresh the skill catalog.')
    return false
  } finally {
    if (assistantSkillsRequestIsCurrent(requestScope, catalogRequestSerial, props.projectName, props.ctx)) catalogLoading.value = false
  }
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
    await api.setAssistantSkillActivation(props.ctx, props.projectName, skill.id, skill.enabled === false)
    if (serial !== activationRequestSerial || props.projectName !== projectName || props.ctx !== requestContext) return
    const successMessage = skill.enabled === false ? 'Skill enabled for future turns.' : 'Skill disabled for future turns.'
    if (await refreshCatalog(skill.id, { preserveDetail: true })) statusMessage.value = successMessage
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
  <div class="grid min-h-full content-start gap-3">
    <header class="flex flex-wrap items-center gap-3 border-b border-border-subtle pb-3">
      <div class="mr-auto min-w-48">
        <h2 class="text-[18px] font-semibold tracking-tight text-text-primary">Skills</h2>
        <p class="mt-0.5 text-[11px] text-text-secondary">Task-specific guidance available to App Studio</p>
      </div>
      <label class="relative min-w-56 flex-1 sm:max-w-sm">
        <Search class="pointer-events-none absolute left-3 top-2.5 h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" />
        <input
          v-model="query"
          type="search"
          class="app-studio-touch-target h-9 w-full rounded-md border border-border-default bg-surface pl-9 pr-9 text-[12px] text-text-primary outline-none transition focus:border-accent/50"
          placeholder="Search skills"
          aria-label="Search skills"
        />
        <button
          v-if="query"
          type="button"
          class="app-studio-touch-target absolute right-2 top-1.5 flex h-6 w-6 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary"
          aria-label="Clear skill search"
          @click="query = ''"
        >
          <X class="h-3.5 w-3.5" :stroke-width="1.75" />
        </button>
      </label>
      <button
          type="button"
          class="app-studio-touch-target inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-2.5 text-[11px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
          :disabled="catalogLoading || actionBusy || !projectName"
          aria-label="Refresh skills"
          @click="refreshCatalog()"
        >
          <Loader2 v-if="catalogLoading" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
          <RefreshCw v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
          Refresh
        </button>
    </header>

    <div v-if="!projectName" class="rounded-xl border border-warning/30 bg-warning-subtle p-4 text-[12px] leading-5 text-warning" role="status">
      Open an existing project to manage its skills. Bundled skills become available when a project is selected.
    </div>

    <div v-if="managementError || props.error" class="flex items-start gap-2 rounded-xl border border-danger/30 bg-danger-subtle p-3 text-[12px] leading-5 text-danger" role="alert">
      <TriangleAlert class="mt-0.5 h-4 w-4 shrink-0" :stroke-width="1.75" />
      <span>{{ managementError || props.error }}</span>
      <button v-if="props.error" type="button" class="app-studio-touch-target ml-auto shrink-0 font-medium underline underline-offset-2" @click="refreshCatalog()">Retry</button>
    </div>
    <div v-if="props.warnings.length" class="flex items-start gap-2 rounded-xl border border-warning/30 bg-warning-subtle p-3 text-[12px] leading-5 text-warning" role="status">
      <TriangleAlert class="mt-0.5 h-4 w-4 shrink-0" :stroke-width="1.75" />
      <div class="grid gap-1">
        <div v-for="warning in props.warnings" :key="warning">{{ warning }}</div>
      </div>
    </div>
    <div v-if="statusMessage" class="flex items-center gap-2 rounded-xl border border-success/30 bg-success-subtle p-3 text-[12px] text-success" role="status">
      <Check class="h-4 w-4 shrink-0" :stroke-width="1.75" />
      {{ statusMessage }}
    </div>

    <section class="grid min-h-0 gap-4">
      <div class="min-h-0">
        <div class="flex items-center justify-between border-b border-border-subtle pb-2">
          <h3 class="text-[14px] font-semibold text-text-primary">Installed</h3>
          <span class="text-[11px] text-text-muted">{{ filteredSkills.length }} skill{{ filteredSkills.length === 1 ? '' : 's' }}</span>
        </div>

        <div v-if="(props.loading || catalogLoading) && !localSkills.length" class="mt-3 flex min-h-52 items-center justify-center gap-2 rounded-xl border border-dashed border-border-subtle text-[12px] text-text-muted" role="status">
          <Loader2 class="h-4 w-4 animate-spin text-accent" :stroke-width="1.75" />
          Loading skills…
        </div>
        <div v-else-if="!filteredSkills.length" class="mt-3 flex min-h-52 flex-col items-center justify-center rounded-xl border border-dashed border-border-subtle px-5 text-center" role="status">
          <Plug class="h-7 w-7 text-text-muted" :stroke-width="1.5" />
          <div class="mt-2 text-[13px] font-medium text-text-secondary">{{ emptyStateText }}</div>
        </div>
        <ul v-else class="mt-2 grid gap-x-4 gap-y-0.5 md:grid-cols-2" aria-label="Installed skills">
          <li v-for="skill in filteredSkills" :key="skill.id" class="min-w-0">
            <button
              type="button"
              class="group flex w-full min-w-0 items-center gap-2.5 rounded-md px-2.5 py-2.5 text-left transition hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
              :class="skill.id === selectedSkillID ? 'bg-accent-subtle' : ''"
              :disabled="actionBusy"
              :aria-busy="activationSkillID === skill.id ? 'true' : undefined"
              :aria-label="`${skill.name}, ${statusLabel(skill)}. Select to view details.`"
              aria-haspopup="dialog"
              @click="selectSkill(skill)"
            >
              <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-border-subtle bg-surface-raised text-accent">
                <Plug class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" />
              </span>
              <span class="min-w-0 flex-1">
                <span class="flex min-w-0 items-center gap-2">
                  <span class="truncate text-[13px] font-medium text-text-primary">{{ skill.name }}</span>
                </span>
                <span class="mt-0.5 block truncate text-[11px] text-text-secondary">{{ skill.description || 'No description provided.' }}</span>
                <span class="mt-1 flex min-w-0 items-center gap-1.5 text-[10px] text-text-muted">
                  <span>{{ sourceLabel(skill) }}</span>
                  <span aria-hidden="true">·</span>
                  <code class="truncate font-mono" :title="skill.packageName || skill.id">{{ skill.packageName || skill.id }}</code>
                </span>
              </span>
              <Check v-if="skill.enabled !== false" class="h-5 w-5 shrink-0 text-text-muted" :stroke-width="1.75" aria-label="Enabled" />
              <span v-else class="h-5 w-5 shrink-0 rounded-full border border-border-default" aria-label="Disabled" />
              <span v-if="activationSkillID === skill.id" class="flex shrink-0 items-center gap-1 text-[11px] text-accent" role="status" aria-label="Saving skill">
                <Loader2 class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" aria-hidden="true" />
                <span>Saving…</span>
              </span>
            </button>
          </li>
        </ul>
      </div>

    </section>

    <Teleport to="#app-studio-overlay-root">
      <div v-if="selectedSkill" class="fixed inset-0 flex items-center justify-center bg-surface/60 p-4 [z-index:var(--app-studio-z-modal-backdrop)] backdrop-blur-[2px]" role="presentation" @mousedown.self="closeSkillDetail">
        <section ref="detailDialogRef" tabindex="-1" class="flex max-h-[min(860px,calc(100vh-2rem))] w-full max-w-3xl flex-col overflow-hidden rounded-xl border border-border-default bg-surface-raised shadow-2xl" role="dialog" aria-modal="true" :aria-labelledby="`skill-detail-title-${selectedSkill.id}`" :aria-busy="activationSkillID === selectedSkill.id ? 'true' : undefined" @keydown="handleDetailKeydown">
          <header class="flex items-start gap-4 px-6 pb-5 pt-6">
            <span class="flex h-14 w-14 shrink-0 items-center justify-center rounded-xl border border-border-subtle bg-accent-subtle text-accent">
              <Plug class="h-7 w-7" :stroke-width="1.5" aria-hidden="true" />
            </span>
            <div class="min-w-0 flex-1 pt-0.5">
              <div class="flex flex-wrap items-baseline gap-x-2">
                <h3 :id="`skill-detail-title-${selectedSkill.id}`" class="truncate text-[20px] font-semibold text-text-primary">{{ selectedSkill.name }}</h3>
                <span class="text-[16px] text-text-muted">Skill</span>
              </div>
              <p class="mt-1 text-[13px] leading-5 text-text-secondary">{{ selectedSkill.description || 'No description provided.' }}</p>
            </div>
            <div class="mt-1 flex shrink-0 items-center gap-2">
              <span v-if="activationSkillID === selectedSkill.id" class="flex items-center gap-1 text-[11px] text-text-muted" role="status" aria-live="polite">
                <Loader2 class="h-3.5 w-3.5 animate-spin text-accent" :stroke-width="1.75" aria-hidden="true" />
                <span>Saving…</span>
              </span>
              <button
                type="button"
                role="switch"
                :aria-checked="selectedSkill.enabled !== false"
                :aria-label="selectedSkill.enabled === false ? 'Enable skill' : 'Disable skill'"
                class="flex h-11 w-12 shrink-0 items-center rounded-sm bg-transparent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="actionBusy"
                @click="toggleSelectedSkill"
              >
                <span
                  aria-hidden="true"
                  class="relative block h-7 w-12 shrink-0 rounded-sm transition"
                  :class="selectedSkill.enabled === false ? 'bg-border-default' : 'bg-accent'"
                >
                  <span class="absolute top-1 h-5 w-5 rounded-xs bg-text-primary shadow-sm transition-all" :class="selectedSkill.enabled === false ? 'left-1' : 'left-6'" />
                </span>
              </button>
            </div>
            <button ref="detailCloseRef" type="button" class="app-studio-touch-target flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-text-muted transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60" :disabled="actionBusy" aria-label="Close skill details" @click="closeSkillDetail">
              <X class="h-5 w-5" :stroke-width="1.75" />
            </button>
          </header>

          <div class="min-h-0 flex-1 overflow-y-auto border-t border-border-subtle px-6 py-5">
            <div v-if="detailLoading" class="flex min-h-48 items-center justify-center gap-2 text-[12px] text-text-muted" role="status"><Loader2 class="h-4 w-4 animate-spin text-accent" :stroke-width="1.75" /> Loading skill…</div>
            <div v-else class="grid gap-4">
              <div v-if="detailError" class="rounded-xl border border-danger/30 bg-danger-subtle p-3 text-[12px] text-danger" role="alert">{{ detailError }}</div>
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
  </div>
</template>
