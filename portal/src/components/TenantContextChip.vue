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

<!--
Compact org/workspace context chip. Renders the current selection as a
single clickable button; clicking opens a popover with two small
dropdowns for switching and a footer link to the full /tenant settings
page.

Shown in the sidebar (vertical mode) above the static nav and in the
horizontal/floating dock between the logo and nav. Sized to be
unobtrusive — the goal is "where am I" awareness, not management.

`variant` controls the layout:
  - "sidebar"     — block-width chip with org + workspace stacked
  - "horizontal"  — single-row chip with org · workspace
-->

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useTenantStore } from '@/stores/tenant'
import { Building2, FolderTree, ChevronDown, Settings, AlertCircle, Download, Loader2 } from 'lucide-vue-next'

defineProps<{ variant?: 'sidebar' | 'horizontal' }>()

const tenant = useTenantStore()
const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)

const orgLabel = computed(() => tenant.activeOrg?.displayName ?? 'No org')
const wsLabel = computed(() => {
  const w = tenant.activeWorkspace
  if (!w) return 'No workspace'
  // Default workspace has no display-name annotation; fall back to the
  // UUID prefix so the chip still reflects an existing context rather
  // than reading as "no workspace selected".
  return w.displayName || w.uuid.slice(0, 8)
})

const workspaces = computed(() =>
  tenant.orgUUID ? tenant.workspacesByOrg[tenant.orgUUID] ?? [] : [],
)

// Lazy-load on mount so the chip shows the right context the first time
// the user opens the app, not just after navigating to /tenant.
onMounted(async () => {
  if (tenant.orgs.length === 0) {
    await tenant.fetchOrgs()
  }
  if (tenant.orgUUID && !tenant.workspacesByOrg[tenant.orgUUID]) {
    await tenant.fetchWorkspaces(tenant.orgUUID)
  }
})

// If the org switches (via the /tenant page or any other code path) make
// sure we have the workspace list for it cached so the popover is useful
// immediately when the user opens it.
watch(
  () => tenant.orgUUID,
  (id) => {
    if (id && !tenant.workspacesByOrg[id]) void tenant.fetchWorkspaces(id)
  },
)

function onOrgChange(e: Event) {
  const v = (e.target as HTMLSelectElement).value
  if (v) tenant.selectOrg(v)
}

function onWorkspaceChange(e: Event) {
  const v = (e.target as HTMLSelectElement).value
  if (v) tenant.selectWorkspace(v)
}

const downloading = ref(false)
async function onDownloadKubeconfig() {
  if (!tenant.orgUUID || !tenant.workspaceUUID || downloading.value) return
  downloading.value = true
  try {
    await tenant.downloadKubeconfig(tenant.orgUUID, tenant.workspaceUUID)
  } finally {
    downloading.value = false
  }
}

// Close-on-outside-click. We bind on document so a click anywhere outside
// the chip's root collapses the popover. Esc also closes.
function onDocClick(e: MouseEvent) {
  if (!open.value || !rootRef.value) return
  if (!rootRef.value.contains(e.target as Node)) {
    open.value = false
  }
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') open.value = false
}
onMounted(() => {
  document.addEventListener('mousedown', onDocClick)
  document.addEventListener('keydown', onKey)
})
onUnmounted(() => {
  document.removeEventListener('mousedown', onDocClick)
  document.removeEventListener('keydown', onKey)
})
</script>

<template>
  <div ref="rootRef" class="relative" :class="variant === 'horizontal' ? 'inline-block' : 'block w-full px-1'">
    <!-- Closed chip -->
    <button
      type="button"
      class="group flex w-full items-center gap-1.5 rounded-lg border border-border-subtle bg-surface-overlay/60 px-2 py-1 text-left text-[11px] transition-colors hover:border-accent/30"
      :class="open ? 'border-accent/40 bg-surface-overlay' : ''"
      :title="`${orgLabel} · ${wsLabel}`"
      @click="open = !open"
    >
      <Building2 class="h-3 w-3 flex-shrink-0 text-text-muted/70" :stroke-width="2" />
      <span class="min-w-0 flex-1 truncate font-medium text-text-secondary">{{ orgLabel }}</span>
      <span v-if="variant !== 'horizontal'" class="text-text-muted/40">·</span>
      <FolderTree v-if="variant !== 'horizontal'" class="h-3 w-3 flex-shrink-0 text-text-muted/70" :stroke-width="2" />
      <span v-if="variant !== 'horizontal'" class="min-w-0 flex-1 truncate text-text-muted">{{ wsLabel }}</span>
      <ChevronDown
        class="h-3 w-3 flex-shrink-0 text-text-muted/60 transition-transform"
        :class="open ? 'rotate-180' : ''"
        :stroke-width="2"
      />
    </button>

    <!-- Popover -->
    <Transition name="popover">
      <div
        v-if="open"
        class="absolute z-[80] mt-1 w-56 rounded-lg border border-border-default bg-surface-raised/95 p-2 shadow-2xl backdrop-blur-xl"
        :class="variant === 'horizontal' ? 'left-0 top-full' : 'left-1 top-full'"
      >
        <label class="block text-[9px] font-semibold uppercase tracking-wider text-text-muted/70">
          Organization
        </label>
        <select
          class="mt-1 w-full rounded-md border border-border-default/50 bg-surface-overlay/60 px-2 py-1 text-[11px] text-text-primary focus:border-accent focus:outline-none"
          :value="tenant.orgUUID ?? ''"
          :disabled="tenant.orgs.length === 0"
          @change="onOrgChange"
        >
          <option v-if="tenant.orgs.length === 0" value="">No orgs</option>
          <option v-for="o in tenant.orgs" :key="o.uuid" :value="o.uuid">
            {{ o.displayName }}{{ o.personal ? ' (personal)' : '' }}
          </option>
        </select>

        <label class="mt-2 block text-[9px] font-semibold uppercase tracking-wider text-text-muted/70">
          Workspace
        </label>
        <select
          class="mt-1 w-full rounded-md border border-border-default/50 bg-surface-overlay/60 px-2 py-1 text-[11px] text-text-primary focus:border-accent focus:outline-none"
          :value="tenant.workspaceUUID ?? ''"
          :disabled="!tenant.orgUUID || workspaces.length === 0"
          @change="onWorkspaceChange"
        >
          <option v-if="!tenant.orgUUID || workspaces.length === 0" value="">No workspaces</option>
          <option v-for="w in workspaces" :key="w.uuid" :value="w.uuid">
            {{ w.displayName || w.uuid }}
          </option>
        </select>

        <div v-if="tenant.error" class="mt-2 flex items-start gap-1 text-[10px] text-danger">
          <AlertCircle class="mt-px h-3 w-3 flex-shrink-0" :stroke-width="2" />
          <span class="truncate">{{ tenant.error }}</span>
        </div>

        <div class="mx-1 my-2 h-px bg-border-default/40" />

        <button
          type="button"
          class="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-[11px] font-medium text-text-muted transition-colors hover:bg-surface-overlay/60 hover:text-accent disabled:cursor-not-allowed disabled:opacity-50"
          :disabled="!tenant.workspaceUUID || downloading"
          :title="tenant.workspaceUUID ? 'Download kubeconfig for the active workspace' : 'Select a workspace first'"
          @click="onDownloadKubeconfig"
        >
          <Loader2 v-if="downloading" class="h-3 w-3 animate-spin" :stroke-width="2" />
          <Download v-else class="h-3 w-3" :stroke-width="2" />
          Download kubeconfig
        </button>

        <router-link
          to="/tenant"
          class="flex items-center gap-1.5 rounded-md px-2 py-1.5 text-[11px] font-medium text-text-muted transition-colors hover:bg-surface-overlay/60 hover:text-accent"
          @click="open = false"
        >
          <Settings class="h-3 w-3" :stroke-width="2" />
          Manage tenants
        </router-link>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.popover-enter-active,
.popover-leave-active {
  transition: opacity 0.12s ease, transform 0.12s ease;
}
.popover-enter-from,
.popover-leave-to {
  opacity: 0;
  transform: translateY(-2px);
}
</style>
