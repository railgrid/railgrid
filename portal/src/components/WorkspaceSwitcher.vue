<!--
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

<script setup lang="ts">
import { computed, nextTick, onMounted, ref, useId, watch } from 'vue'
import { useRouter } from 'vue-router'
import {
  AlertCircle,
  Check,
  ChevronDown,
  FolderTree,
  RefreshCw,
  Search,
  Settings2,
  Sparkles,
} from 'lucide-vue-next'
import { useAnchoredPopover } from '@/composables/useAnchoredPopover'
import { isWorkspaceAvailable, isWorkspaceUsable, useTenantStore, type WorkspaceRow } from '@/stores/tenant'

const props = withDefaults(defineProps<{
  variant?: 'sidebar' | 'horizontal' | 'compact'
}>(), {
  variant: 'sidebar',
})
const variant = computed(() => props.variant)

const tenant = useTenantStore()
const router = useRouter()
const WORKSPACE_SEARCH_THRESHOLD = 5
const search = ref('')
const searchRef = ref<HTMLInputElement | null>(null)
const manageWorkspacesRef = ref<HTMLButtonElement | null>(null)
const panelId = useId()
const listboxId = useId()
const { open, triggerRef, panelRef, panelStyle, close, toggle } = useAnchoredPopover({ width: 344 })

type WorkspaceStatus = 'Ready' | 'Pending' | 'Unverified'

const cachedWorkspaces = computed(() =>
  tenant.orgUUID ? tenant.workspacesByOrg[tenant.orgUUID] ?? [] : [],
)
const workspaces = computed(() =>
  cachedWorkspaces.value.filter(isWorkspaceAvailable),
)
const showWorkspaceSearch = computed(() => workspaces.value.length > WORKSPACE_SEARCH_THRESHOLD)
const usableWorkspaces = computed(() => workspaces.value.filter(isWorkspaceUsable))
const orgLoadState = computed(() => tenant.orgLoadState)
const orgLoading = computed(() => orgLoadState.value === 'loading')
const orgError = computed(() => tenant.orgError)
const orgListLoaded = computed(() => tenant.orgListLoaded)
const hasCachedOrgRows = computed(() => tenant.orgs.length > 0)
const orgFirstLoadFailed = computed(() => orgLoadState.value === 'error' && !orgListLoaded.value)
const orgRefreshFailed = computed(() => orgLoadState.value === 'error' && orgListLoaded.value)
const orgRefreshing = computed(() => orgLoading.value && orgListLoaded.value)
const orgAuthorityUnverified = computed(() => orgRefreshFailed.value || orgRefreshing.value)
const workspaceLoadState = computed(() =>
  tenant.orgUUID ? tenant.workspaceLoadStateByOrg[tenant.orgUUID] ?? 'idle' : 'idle',
)
const workspaceLoading = computed(() => workspaceLoadState.value === 'loading')
const workspaceError = computed(() =>
  tenant.orgUUID ? tenant.workspaceErrorByOrg[tenant.orgUUID] ?? null : null,
)
const hasWorkspaceCache = computed(() =>
  !!tenant.orgUUID && Object.prototype.hasOwnProperty.call(tenant.workspacesByOrg, tenant.orgUUID),
)
const hasCachedWorkspaceRows = computed(() => workspaces.value.length > 0)
const workspaceFirstLoadFailed = computed(() => workspaceLoadState.value === 'error' && !hasWorkspaceCache.value)
const workspaceRefreshFailed = computed(() => workspaceLoadState.value === 'error' && hasWorkspaceCache.value)
const workspaceRefreshing = computed(() => workspaceLoading.value && hasWorkspaceCache.value)
const workspaceDataUnverified = computed(() =>
  orgFirstLoadFailed.value || orgAuthorityUnverified.value || workspaceRefreshFailed.value || workspaceRefreshing.value,
)
const workspaceSwitchingBlocked = computed(() =>
  orgFirstLoadFailed.value || orgAuthorityUnverified.value || workspaceLoadState.value === 'error' || workspaceRefreshing.value,
)
const contextLoading = computed(() => orgLoading.value || workspaceLoading.value)
const workspaceCanShowEmpty = computed(() =>
  orgLoadState.value === 'ready' && orgListLoaded.value && !!tenant.orgUUID &&
  workspaceLoadState.value === 'ready' && hasWorkspaceCache.value && !workspaceError.value,
)
const contextAuthorityVerified = computed(() =>
  orgLoadState.value === 'ready' &&
  !orgError.value &&
  workspaceLoadState.value === 'ready' &&
  !workspaceError.value &&
  !workspaceDataUnverified.value,
)
const showContextGuide = computed(() =>
  usableWorkspaces.value.length > 1 && contextAuthorityVerified.value,
)
const workspaceLabel = computed(() => workspaceName(tenant.activeWorkspace))
const orgLabel = computed(() => {
  const displayName = tenant.activeOrg?.displayName?.trim()
  if (displayName) return displayName
  if (tenant.orgUUID) return `Organization ${tenant.orgUUID.slice(0, 8)}`
  return 'Choose organization'
})
const orgContextLabel = computed(() =>
  orgAuthorityUnverified.value && hasCachedOrgRows.value
    ? `${orgLabel.value} · Last known · unverified`
    : orgLabel.value,
)
const workspaceNameCounts = computed(() => {
  const counts = new Map<string, number>()
  for (const workspace of workspaces.value) {
    const name = workspaceName(workspace)
    counts.set(name, (counts.get(name) ?? 0) + 1)
  }
  return counts
})
const filteredWorkspaces = computed(() => {
  const query = showWorkspaceSearch.value ? search.value.trim().toLocaleLowerCase() : ''
  if (!query) return workspaces.value
  return workspaces.value.filter((workspace) =>
    `${workspaceName(workspace)} ${workspace.uuid}`.toLocaleLowerCase().includes(query),
  )
})

watch(showWorkspaceSearch, async (visible) => {
  if (visible) return
  const searchOwnedFocus = searchRef.value === document.activeElement
  search.value = ''
  if (!open.value) return
  await nextTick()
  if (!open.value || showWorkspaceSearch.value) return
  const focusOutsidePanel = !panelRef.value?.contains(document.activeElement)
  if (!searchOwnedFocus && !focusOutsidePanel) return
  focusInitialPanelControl()
})

const workspaceReady = computed(() =>
  isWorkspaceUsable(tenant.activeWorkspace) && contextAuthorityVerified.value,
)
const workspaceTriggerWarning = computed(() => !!tenant.workspaceUUID && !workspaceReady.value)
const contextUnavailable = computed(() =>
  orgFirstLoadFailed.value || (orgRefreshFailed.value && !hasCachedOrgRows.value) ||
  workspaceFirstLoadFailed.value || (workspaceRefreshFailed.value && !hasCachedWorkspaceRows.value),
)
const workspaceTriggerState = computed(() => {
  if (workspaceReady.value) return 'verified'
  if (contextUnavailable.value) return 'unavailable'
  if (workspaceDataUnverified.value || orgLoadState.value === 'error' || workspaceLoadState.value === 'error') {
    return 'last known and unverified'
  }
  if (tenant.workspaceUUID) return 'pending verification'
  return 'not selected'
})

const workspaceTriggerLabel = computed(() =>
  `Workspace: ${workspaceLabel.value}. Organization provenance: ${orgContextLabel.value}. Context: ${workspaceTriggerState.value}.`,
)

function recoveryDetail(error: string | null): string {
  return (error ?? '')
    .trim()
    .replace(/\s*Try again\.?\s*$/i, '')
    .replace(/[.!?]+$/, '')
}

function recoveryMessage(message: string, error: string | null): string {
  const detail = recoveryDetail(error)
  if (detail.toLocaleLowerCase().startsWith(message.toLocaleLowerCase())) return `${detail}.`
  return `${message}${detail ? ` — ${detail}` : ''}.`
}

const organizationFirstLoadMessage = computed(() =>
  recoveryMessage('Unable to load organizations', orgError.value),
)
const organizationRefreshMessage = computed(() =>
  recoveryMessage(
    hasCachedOrgRows.value
      ? 'Organizations could not be refreshed; showing the last-known organization (unverified), so workspace switching is paused'
      : 'Organizations could not be refreshed; no verified organization list is available, so workspace switching is paused',
    orgError.value,
  ),
)
const workspaceFirstLoadMessage = computed(() =>
  recoveryMessage('Unable to load workspaces', workspaceError.value),
)
const workspaceRefreshMessage = computed(() =>
  recoveryMessage(
    hasCachedWorkspaceRows.value
      ? 'Workspaces could not be refreshed; showing last-known workspaces (unverified), so switching is paused'
      : 'Workspaces could not be refreshed; the last verified workspace list was empty, so switching is paused',
    workspaceError.value,
  ),
)

function workspaceName(workspace: WorkspaceRow | null): string {
  if (!workspace) return 'Choose workspace'
  return workspace.displayName || workspace.uuid.slice(0, 8)
}

async function ensureContextLoaded() {
  if (tenant.orgs.length === 0 && orgLoadState.value === 'idle') await tenant.fetchOrgs()
  if (orgLoadState.value === 'error' || !tenant.orgUUID || tenant.workspacesByOrg[tenant.orgUUID]) return
  const loadState = tenant.workspaceLoadStateByOrg[tenant.orgUUID] ?? 'idle'
  if (loadState !== 'idle') return
  await tenant.fetchWorkspaces(tenant.orgUUID, {
    selectDefault: tenant.workspaceMode !== 'organization',
  })
}

function workspaceUnavailable(workspace: WorkspaceRow): boolean {
  return workspaceSwitchingBlocked.value || !isWorkspaceUsable(workspace)
}

function workspaceStatus(workspace: WorkspaceRow): WorkspaceStatus {
  if (workspaceDataUnverified.value) return 'Unverified'
  return workspace.clusterName ? 'Ready' : 'Pending'
}

function workspaceNeedsDisambiguation(workspace: WorkspaceRow): boolean {
  return (workspaceNameCounts.value.get(workspaceName(workspace)) ?? 0) > 1
}

function workspaceIdentifier(workspace: WorkspaceRow): string {
  if (!workspaceNeedsDisambiguation(workspace)) return ''
  const peers = workspaces.value.filter((candidate) =>
    candidate.uuid !== workspace.uuid && workspaceName(candidate) === workspaceName(workspace),
  )
  let length = Math.min(8, workspace.uuid.length)
  while (length < workspace.uuid.length && peers.some((peer) =>
    peer.uuid.slice(0, length) === workspace.uuid.slice(0, length),
  )) {
    length = Math.min(workspace.uuid.length, length + 4)
  }
  return `ID ${workspace.uuid.slice(0, length)}`
}

function workspaceDisabledReason(workspace: WorkspaceRow): string | undefined {
  if (!workspaceUnavailable(workspace)) return undefined
  if (orgRefreshing.value) return 'Organization data is being refreshed. Wait for verification before switching.'
  if (orgAuthorityUnverified.value && !hasCachedOrgRows.value) return 'Organization data could not be verified. Retry before switching.'
  if (orgAuthorityUnverified.value) return 'Organization data is last known and unverified. Retry before switching.'
  if (workspaceRefreshing.value) return 'Workspace data is being refreshed. Wait for verification before switching.'
  if (workspaceRefreshFailed.value) return 'Workspace data is last known and unverified. Retry before switching.'
  if (!workspace.clusterName) return 'This workspace is still provisioning.'
  return 'This workspace cannot be selected until the context is verified.'
}

function workspaceOptionLabel(workspace: WorkspaceRow): string {
  const current = tenant.workspaceUUID === workspace.uuid ? ', current workspace' : ''
  const disabledReason = workspaceDisabledReason(workspace)
  return `${workspaceName(workspace)}, ID ${workspace.uuid}, ${workspaceStatus(workspace)}${current}${disabledReason ? `, ${disabledReason}` : ''}`
}

async function chooseWorkspace(workspace: WorkspaceRow): Promise<void> {
  // The hub withholds clusterName until the workspace's kcp cluster is
  // serving. A pending row must not replace a usable cluster context.
  if (!isWorkspaceUsable(workspace)) return
  if (workspaceUnavailable(workspace)) return
  const changed = tenant.selectWorkspace(workspace.uuid)
  // A successful switch starts a new workspace-scoped session. Returning to
  // the named dashboard route keeps provider/detail URLs from being replayed
  // against the new workspace before their own data has been revalidated.
  // Same/current selections return false and stay on the current route.
  close({ restoreFocus: true })
  if (!changed) return
  const transitionToken = tenant.beginWorkspaceTransition()
  try {
    await router.replace({ name: 'dashboard' })
  } finally {
    tenant.endWorkspaceTransition(transitionToken)
  }
}

function manageWorkspaces() {
  close({ restoreFocus: true })
  void router.push('/settings/workspaces')
}

async function retryContext(): Promise<void> {
  if (orgLoadState.value === 'error' || tenant.orgs.length === 0) await tenant.fetchOrgs()
  if (tenant.orgLoadState === 'error' || !tenant.orgUUID) return
  await tenant.fetchWorkspaces(tenant.orgUUID, {
    selectDefault: tenant.workspaceMode !== 'organization',
  })
}

function retryOrganizations() {
  void retryContext()
}

function retryWorkspaces() {
  void retryContext()
}

function workspaceOptions(): HTMLButtonElement[] {
  const panel = panelRef.value
  if (!panel) return []
  return Array.from(panel.querySelectorAll<HTMLButtonElement>('button[role="option"]:not(:disabled)'))
}

function focusWorkspaceOption(index: number) {
  const options = workspaceOptions()
  if (options.length === 0) return
  const bounded = (index + options.length) % options.length
  options[bounded]?.focus()
}

function focusInitialPanelControl() {
  const panel = panelRef.value
  if (!panel) return

  if (showWorkspaceSearch.value && searchRef.value) {
    searchRef.value.focus()
    return
  }

  const options = workspaceOptions()
  const selected = options.find((option) => option.getAttribute('aria-selected') === 'true')
  const manage = manageWorkspacesRef.value
  const target = selected ?? options[0] ?? (manage && !manage.disabled ? manage : null) ??
    panel.querySelector<HTMLElement>('button:not(:disabled), input:not(:disabled), [href]')
  target?.focus()
  if (!panel.contains(document.activeElement)) panel.focus()
}

function onPanelKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    close({ restoreFocus: true })
    return
  }

  if (event.target === searchRef.value) {
    if (!showWorkspaceSearch.value || !['ArrowDown', 'ArrowUp'].includes(event.key)) return
    const options = workspaceOptions()
    if (options.length === 0) return
    event.preventDefault()
    focusWorkspaceOption(event.key === 'ArrowDown' ? 0 : options.length - 1)
    return
  }

  // Keep other text fields' cursor movement and editing shortcuts intact.
  // Arrow/Home/End navigation belongs to focused workspace options.
  if (
    event.target instanceof HTMLInputElement
    || event.target instanceof HTMLTextAreaElement
    || event.target instanceof HTMLElement && event.target.isContentEditable
  ) return

  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const options = workspaceOptions()
  if (options.length === 0) return

  const active = document.activeElement
  if (!(active instanceof HTMLButtonElement) || active.getAttribute('role') !== 'option') return

  event.preventDefault()
  const current = options.indexOf(active)
  if (event.key === 'Home') focusWorkspaceOption(0)
  else if (event.key === 'End') focusWorkspaceOption(options.length - 1)
  else if (event.key === 'ArrowDown') focusWorkspaceOption(current < 0 ? 0 : current + 1)
  else focusWorkspaceOption(current < 0 ? options.length - 1 : current - 1)
}

watch(open, async (isOpen) => {
  if (!isOpen) {
    search.value = ''
    return
  }
  await nextTick()
  if (!open.value) return
  // Keep focus inside the dialog while a first-load request resolves. A later
  // pass below moves it to the selected/first available row once data exists.
  focusInitialPanelControl()
  await ensureContextLoaded()
  if (!open.value) return
  await nextTick()
  if (!open.value) return
  focusInitialPanelControl()
})

onMounted(() => { void ensureContextLoaded() })
</script>

<template>
  <div
    :class="[
      variant === 'sidebar' ? 'w-full px-1' : 'shrink-0',
      variant === 'compact' ? 'flex justify-center' : '',
    ]"
  >
    <div v-if="variant === 'sidebar'" class="mb-1 px-2">
      <span class="k-eyebrow">Workspace</span>
    </div>
    <button
      ref="triggerRef"
      type="button"
      class="workspace-switcher-trigger group relative flex min-w-0 items-center rounded-md border-0 bg-transparent text-left text-text-secondary transition-colors hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      :class="[
        variant === 'sidebar' ? 'w-full justify-start gap-2 px-2 py-1.5' : '',
        variant === 'horizontal' ? 'max-w-44 justify-start gap-1.5 px-1.5 py-1' : '',
        variant === 'compact' ? 'workspace-switcher-trigger--compact h-8 w-8 p-0' : '',
        open ? 'bg-accent-subtle text-accent' : '',
      ]"
      :aria-label="workspaceTriggerLabel"
      :aria-controls="panelId"
      aria-haspopup="dialog"
      :aria-expanded="open"
      :title="variant === 'compact' ? workspaceTriggerLabel : undefined"
      @click="toggle"
    >
      <span class="relative flex h-5 w-5 shrink-0 items-center justify-center text-current">
        <FolderTree class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
        <span
          v-if="workspaceReady"
          class="absolute -right-0.5 -top-0.5 h-1.5 w-1.5 rounded-full bg-success"
          aria-hidden="true"
        />
        <span
          v-else-if="workspaceTriggerWarning"
          class="absolute -right-0.5 -top-0.5 h-1.5 w-1.5 rounded-full bg-warning"
          aria-hidden="true"
        />
      </span>
      <span
        v-if="variant === 'compact' && tenant.orgUUID"
        class="absolute -bottom-0.5 -left-0.5 h-1.5 w-1.5 rounded-[1px] border border-accent/80 bg-surface-raised"
        aria-hidden="true"
      />
      <span v-if="variant === 'horizontal'" class="flex min-w-0 flex-1 items-baseline gap-1 overflow-hidden whitespace-nowrap">
        <span class="min-w-0 flex-1 truncate font-mono text-[11px] leading-4 text-text-primary" :title="workspaceLabel">{{ workspaceLabel }}</span>
        <span
          class="min-w-0 max-w-[44%] shrink-0 truncate border-l border-border-subtle/70 pl-1 text-[10px] leading-3 text-text-secondary"
          :title="orgContextLabel"
        >
          {{ orgContextLabel }}
        </span>
      </span>
      <span v-else-if="variant === 'sidebar'" class="min-w-0 flex-1">
        <span class="block truncate font-mono text-[11px] text-text-primary" :title="workspaceLabel">{{ workspaceLabel }}</span>
        <span class="mt-0.5 block truncate text-[10px] text-text-secondary">
          {{ orgContextLabel }}
        </span>
      </span>
      <ChevronDown
        v-if="variant !== 'compact'"
        class="h-3 w-3 shrink-0 text-text-secondary transition-transform"
        :class="open ? 'rotate-180' : ''"
        :stroke-width="1.75"
        aria-hidden="true"
      />
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        :id="panelId"
        ref="panelRef"
        role="dialog"
        aria-label="Switch workspace"
        tabindex="-1"
        class="k-menu fixed z-[80] flex max-h-[calc(100vh-16px)] max-w-[calc(100vw-16px)] min-h-0 flex-col overflow-hidden"
        :style="panelStyle"
        @keydown="onPanelKeydown"
      >
        <div class="border-b border-border-subtle px-3 py-2.5">
          <div class="flex items-center justify-between gap-3">
            <div class="min-w-0">
              <div class="text-[12px] font-semibold text-text-primary">Switch workspace</div>
              <div class="mt-0.5 truncate text-[10px] text-text-secondary">Within {{ orgContextLabel }}</div>
            </div>
          </div>
          <div v-if="showWorkspaceSearch" class="relative mt-2">
            <Search class="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
            <input
              ref="searchRef"
              v-model="search"
              type="search"
              class="workspace-switcher-search k-input py-1.5 pl-8 pr-2 text-[11px] placeholder:text-text-secondary"
              placeholder="Search workspaces"
              aria-label="Search workspaces"
              :aria-controls="listboxId"
            />
          </div>
        </div>

        <div v-if="showContextGuide" class="flex items-start gap-2 border-b border-border-subtle bg-accent-subtle/40 px-3 py-2 text-[10px] text-text-secondary">
          <Sparkles class="mt-0.5 h-3 w-3 shrink-0 text-accent" :stroke-width="1.75" aria-hidden="true" />
          <span>AI tools and resources follow the selected context. A successful switch opens Dashboard.</span>
        </div>

        <div class="min-h-0 flex-1 overflow-y-auto">
          <div
            :id="listboxId"
            class="p-1"
            role="listbox"
            aria-label="Workspaces"
            :aria-busy="contextLoading"
          >
            <button
              v-for="workspace in filteredWorkspaces"
              :key="workspace.uuid"
              type="button"
              role="option"
              class="workspace-switcher-option k-menu-item py-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-inset"
              :class="[
                tenant.workspaceUUID === workspace.uuid ? 'is-selected' : '',
                workspaceUnavailable(workspace) ? 'cursor-not-allowed' : '',
              ]"
              :aria-label="workspaceOptionLabel(workspace)"
              :aria-selected="tenant.workspaceUUID === workspace.uuid"
              :aria-disabled="workspaceUnavailable(workspace)"
              :disabled="workspaceUnavailable(workspace)"
              :title="workspaceDisabledReason(workspace)"
              @click="chooseWorkspace(workspace)"
            >
              <span class="flex h-6 w-6 shrink-0 items-center justify-center text-text-secondary">
                <FolderTree class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
              </span>
              <span class="min-w-0 flex-1">
                <span class="block truncate font-mono text-[11px] text-text-primary">{{ workspaceName(workspace) }}</span>
                <span v-if="workspaceIdentifier(workspace)" class="mt-0.5 block truncate font-mono text-[9px] text-text-secondary">{{ workspaceIdentifier(workspace) }}</span>
              </span>
              <span
                v-if="workspaceStatus(workspace) !== 'Ready'"
                class="k-badge shrink-0 px-1.5 py-0.5 text-[10px]"
                :class="workspaceStatus(workspace) === 'Pending' ? 'k-badge--warning' : 'k-badge--muted'"
              >
                <span class="k-badge__dot" aria-hidden="true" />
                <span class="text-text-primary">{{ workspaceStatus(workspace) }}</span>
              </span>
              <Check
                v-if="tenant.workspaceUUID === workspace.uuid"
                class="h-3.5 w-3.5 shrink-0 text-accent"
                :stroke-width="2"
                aria-hidden="true"
              />
            </button>
          </div>

          <div v-if="orgLoading && !hasCachedOrgRows" class="px-3 py-6 text-center text-[11px] text-text-secondary" role="status" aria-live="polite">
            Loading organizations…
          </div>
          <div v-else-if="orgFirstLoadFailed" class="flex flex-col items-center gap-2 px-3 py-5 text-center text-[11px] text-danger" role="alert" aria-live="assertive">
            <div class="flex items-start gap-2">
              <AlertCircle class="mt-px h-3 w-3 shrink-0" :stroke-width="1.75" aria-hidden="true" />
              <span>{{ organizationFirstLoadMessage }}</span>
            </div>
            <button type="button" class="workspace-switcher-action k-btn k-btn--text text-[10px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-inset" @click="retryOrganizations">
              <RefreshCw class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
              Retry
            </button>
          </div>
          <div v-if="orgRefreshing && hasCachedOrgRows" class="flex items-start gap-2 border-b border-border-subtle px-3 py-2 text-[10px] text-text-secondary" role="status" aria-live="polite">
            <RefreshCw class="mt-px h-3 w-3 shrink-0 animate-spin text-accent" :stroke-width="1.75" aria-hidden="true" />
            <span>Refreshing organizations; workspace switching is paused until verification succeeds.</span>
          </div>
          <div v-if="orgRefreshFailed" class="flex flex-col items-center gap-2 border-b border-border-subtle px-3 py-3 text-center text-[10px] text-warning" role="alert" aria-live="assertive">
            <div class="flex items-start gap-2">
              <AlertCircle class="mt-px h-3 w-3 shrink-0" :stroke-width="1.75" aria-hidden="true" />
              <span>{{ organizationRefreshMessage }}</span>
            </div>
            <button type="button" class="workspace-switcher-action k-btn k-btn--text text-[10px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-inset" @click="retryOrganizations">
              <RefreshCw class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
              Retry
            </button>
          </div>
          <div v-if="workspaceLoading && !hasCachedWorkspaceRows && !orgFirstLoadFailed" class="px-3 py-6 text-center text-[11px] text-text-secondary" role="status" aria-live="polite">
            {{ workspaceRefreshing ? 'Refreshing workspaces…' : 'Loading workspaces…' }}
          </div>
          <div v-else-if="workspaceFirstLoadFailed && !orgFirstLoadFailed" class="flex flex-col items-center gap-2 px-3 py-5 text-center text-[11px] text-danger" role="alert" aria-live="assertive">
            <div class="flex items-start gap-2">
              <AlertCircle class="mt-px h-3 w-3 shrink-0" :stroke-width="1.75" aria-hidden="true" />
              <span>{{ workspaceFirstLoadMessage }}</span>
            </div>
            <button type="button" class="workspace-switcher-action k-btn k-btn--text text-[10px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-inset" @click="retryWorkspaces">
              <RefreshCw class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
              Retry
            </button>
          </div>
          <div v-else-if="workspaceRefreshFailed" class="flex flex-col items-center gap-2 border-t border-border-subtle px-3 py-3 text-center text-[10px] text-warning" role="alert" aria-live="assertive">
            <div class="flex items-start gap-2">
              <AlertCircle class="mt-px h-3 w-3 shrink-0" :stroke-width="1.75" aria-hidden="true" />
              <span>{{ workspaceRefreshMessage }}</span>
            </div>
            <button type="button" class="workspace-switcher-action k-btn k-btn--text text-[10px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-inset" @click="retryWorkspaces">
              <RefreshCw class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
              Retry
            </button>
          </div>
          <div v-else-if="!tenant.orgUUID && !orgLoading && !orgFirstLoadFailed" class="px-3 py-6 text-center text-[11px] text-text-secondary" role="status" aria-live="polite">
            Choose an organization to view workspaces.
          </div>
          <div v-else-if="workspaceCanShowEmpty && filteredWorkspaces.length === 0" class="px-3 py-6 text-center text-[11px] text-text-secondary" role="status" aria-live="polite">
            {{ showWorkspaceSearch && search ? 'No workspaces match this search.' : 'No available workspaces in this organization.' }}
          </div>
        </div>

        <div class="border-t border-border-subtle p-1">
          <button ref="manageWorkspacesRef" type="button" class="workspace-switcher-action k-menu-item focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-inset" @click="manageWorkspaces">
            <Settings2 class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
            <span class="flex-1">Manage workspaces</span>
            <ChevronDown class="h-3 w-3 -rotate-90 text-text-secondary" :stroke-width="1.75" aria-hidden="true" />
          </button>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
@media (pointer: coarse) {
  .workspace-switcher-trigger,
  .workspace-switcher-search,
  .workspace-switcher-option,
  .workspace-switcher-action {
    min-height: 44px;
    min-width: 44px;
  }

  .workspace-switcher-trigger--compact {
    min-width: 44px;
  }
}
</style>
