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
const search = ref('')
const searchRef = ref<HTMLInputElement | null>(null)
const panelId = useId()
const { open, triggerRef, panelRef, panelStyle, close, toggle } = useAnchoredPopover({ width: 344 })

const workspaces = computed(() =>
  (tenant.orgUUID ? tenant.workspacesByOrg[tenant.orgUUID] ?? [] : []).filter(isWorkspaceAvailable),
)
const workspaceLoadState = computed(() =>
  tenant.orgUUID ? tenant.workspaceLoadStateByOrg[tenant.orgUUID] ?? 'idle' : 'idle',
)
const workspaceLoading = computed(() => workspaceLoadState.value === 'loading')
const workspaceError = computed(() =>
  tenant.orgUUID ? tenant.workspaceErrorByOrg[tenant.orgUUID] ?? null : null,
)
const workspaceLabel = computed(() => workspaceName(tenant.activeWorkspace))
const orgLabel = computed(() => tenant.activeOrg?.displayName ?? 'No organization')
const filteredWorkspaces = computed(() => {
  const query = search.value.trim().toLocaleLowerCase()
  if (!query) return workspaces.value
  return workspaces.value.filter((workspace) =>
    `${workspaceName(workspace)} ${workspace.uuid}`.toLocaleLowerCase().includes(query),
  )
})
const workspaceReady = computed(() => isWorkspaceUsable(tenant.activeWorkspace))

function workspaceName(workspace: WorkspaceRow | null): string {
  if (!workspace) return 'Choose workspace'
  return workspace.displayName || workspace.uuid.slice(0, 8)
}

async function ensureContextLoaded() {
  if (tenant.orgs.length === 0) await tenant.fetchOrgs()
  if (!tenant.orgUUID || tenant.workspacesByOrg[tenant.orgUUID]) return
  const loadState = tenant.workspaceLoadStateByOrg[tenant.orgUUID] ?? 'idle'
  if (loadState !== 'idle') return
  await tenant.fetchWorkspaces(tenant.orgUUID, {
    selectDefault: tenant.workspaceMode !== 'organization',
  })
}

function workspaceUnavailable(workspace: WorkspaceRow): boolean {
  return !isWorkspaceAvailable(workspace) || !workspace.clusterName
}

function workspaceStatus(workspace: WorkspaceRow): 'Ready' | 'Pending' | 'Deleting' {
  if (!isWorkspaceAvailable(workspace)) return 'Deleting'
  return workspace.clusterName ? 'Ready' : 'Pending'
}

async function chooseWorkspace(workspace: WorkspaceRow): Promise<void> {
  // The hub withholds clusterName until the workspace's kcp cluster is
  // serving. A pending row must not replace a usable cluster context.
  if (!isWorkspaceUsable(workspace)) return
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

function retryWorkspaces() {
  if (!tenant.orgUUID) return
  void tenant.fetchWorkspaces(tenant.orgUUID, {
    selectDefault: tenant.workspaceMode !== 'organization',
  })
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

function onPanelKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    close({ restoreFocus: true })
    return
  }

  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const options = workspaceOptions()
  if (options.length === 0) return

  event.preventDefault()
  const active = document.activeElement
  const current = active instanceof HTMLButtonElement ? options.indexOf(active) : -1
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
  await ensureContextLoaded()
  await nextTick()
  searchRef.value?.focus()
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
      class="group flex min-w-0 items-center rounded-md border-0 bg-transparent text-left text-text-muted transition-colors hover:bg-surface-hover hover:text-text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/30"
      :class="[
        variant === 'sidebar' ? 'w-full justify-start gap-2 px-2 py-1.5' : '',
        variant === 'horizontal' ? 'max-w-44 justify-start gap-1.5 px-1.5 py-1' : '',
        variant === 'compact' ? 'h-8 w-8 p-0' : '',
        open ? 'bg-accent-subtle text-accent' : '',
      ]"
      :aria-label="`Workspace: ${workspaceLabel}. Organization: ${orgLabel}`"
      :aria-controls="panelId"
      aria-haspopup="dialog"
      :aria-expanded="open"
      :title="variant === 'compact' ? `Workspace: ${workspaceLabel}` : undefined"
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
          v-else-if="tenant.workspaceUUID"
          class="absolute -right-0.5 -top-0.5 h-1.5 w-1.5 rounded-full bg-warning"
          aria-hidden="true"
        />
      </span>
      <span v-if="variant !== 'compact'" class="min-w-0 flex-1">
        <span class="block truncate font-mono text-[11px] text-text-primary">{{ workspaceLabel }}</span>
        <span v-if="variant === 'sidebar'" class="mt-0.5 block truncate text-[9px] text-text-muted">
          {{ workspaceReady ? 'Active context' : 'Select an operating context' }}
        </span>
      </span>
      <ChevronDown
        v-if="variant !== 'compact'"
        class="h-3 w-3 shrink-0 text-text-muted transition-transform"
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
        class="k-menu fixed z-[80] max-h-[calc(100vh-16px)] max-w-[calc(100vw-16px)] overflow-hidden"
        :style="panelStyle"
        @keydown="onPanelKeydown"
      >
        <div class="border-b border-border-subtle px-3 py-2.5">
          <div class="flex items-center justify-between gap-3">
            <div class="min-w-0">
              <div class="text-[12px] font-semibold text-text-primary">Switch workspace</div>
              <div class="mt-0.5 truncate text-[10px] text-text-muted">Within {{ orgLabel }}</div>
            </div>
          </div>
          <div class="relative mt-2">
            <Search class="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
            <input
              ref="searchRef"
              v-model="search"
              type="search"
              class="k-input py-1.5 pl-8 pr-2 text-[11px]"
              placeholder="Search workspaces"
              aria-label="Search workspaces"
            />
          </div>
        </div>

        <div class="flex items-start gap-2 border-b border-border-subtle bg-accent-subtle/40 px-3 py-2 text-[10px] text-text-secondary">
          <Sparkles class="mt-0.5 h-3 w-3 shrink-0 text-accent" :stroke-width="1.75" aria-hidden="true" />
          <span>AI tools, resources, and activity follow the active workspace.</span>
        </div>

        <div class="max-h-64 overflow-y-auto p-1" role="listbox" aria-label="Workspaces">
          <button
            v-for="workspace in filteredWorkspaces"
            :key="workspace.uuid"
            type="button"
            role="option"
            class="k-menu-item py-2"
            :class="[
              tenant.workspaceUUID === workspace.uuid ? 'is-selected' : '',
              workspaceUnavailable(workspace) ? 'cursor-not-allowed opacity-60' : '',
            ]"
            :aria-selected="tenant.workspaceUUID === workspace.uuid"
            :aria-disabled="workspaceUnavailable(workspace)"
            :disabled="workspaceUnavailable(workspace)"
            :title="workspaceStatus(workspace) === 'Deleting' ? 'This workspace is being deleted' : workspaceStatus(workspace) === 'Pending' ? 'This workspace is still provisioning' : undefined"
            @click="chooseWorkspace(workspace)"
          >
            <span class="flex h-6 w-6 shrink-0 items-center justify-center text-text-secondary">
              <FolderTree class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
            </span>
            <span class="min-w-0 flex-1">
              <span class="block truncate font-mono text-[11px] text-text-primary">{{ workspaceName(workspace) }}</span>
              <span class="mt-0.5 block truncate font-mono text-[9px] text-text-muted">{{ workspace.uuid.slice(0, 12) }}</span>
            </span>
            <span
              class="k-badge shrink-0 px-1.5 py-0.5 text-[8px]"
              :class="workspaceStatus(workspace) === 'Ready' ? 'k-badge--success' : workspaceStatus(workspace) === 'Deleting' ? 'k-badge--danger' : 'k-badge--warning'"
            >
              <span class="k-badge__dot" />
              {{ workspaceStatus(workspace) }}
            </span>
            <Check
              v-if="tenant.workspaceUUID === workspace.uuid"
              class="h-3.5 w-3.5 shrink-0 text-accent"
              :stroke-width="2"
              aria-hidden="true"
            />
          </button>

          <div v-if="workspaceLoading && workspaces.length === 0" class="px-3 py-6 text-center text-[11px] text-text-muted">
            Loading workspaces…
          </div>
          <div v-else-if="workspaceError" class="flex flex-col items-center gap-2 px-3 py-5 text-center text-[11px] text-danger">
            <div class="flex items-start gap-2">
              <AlertCircle class="mt-px h-3 w-3 shrink-0" :stroke-width="1.75" aria-hidden="true" />
              <span>{{ workspaceError }}</span>
            </div>
            <button type="button" class="k-btn k-btn--text text-[10px]" @click="retryWorkspaces">
              <RefreshCw class="h-3 w-3" :stroke-width="1.75" aria-hidden="true" />
              Retry
            </button>
          </div>
          <div v-else-if="filteredWorkspaces.length === 0" class="px-3 py-6 text-center text-[11px] text-text-muted">
            {{ search ? 'No workspaces match this search.' : 'This organization has no workspaces.' }}
          </div>
        </div>

        <div class="border-t border-border-subtle p-1">
          <button type="button" class="k-menu-item" @click="manageWorkspaces">
            <Settings2 class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" aria-hidden="true" />
            <span class="flex-1">Manage workspaces</span>
            <ChevronDown class="h-3 w-3 -rotate-90 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
          </button>
        </div>
      </div>
    </Teleport>
  </div>
</template>
