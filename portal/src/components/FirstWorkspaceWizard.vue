<script setup lang="ts">
// Shown when the active org has zero workspaces. Replaces the would-be
// page slot in AppLayout so the user gets a guided "create your first
// workspace" affordance instead of a broken edges/dashboard/provider view
// pointing at a non-existent cluster. Picking an org via the
// TenantContextChip clears workspaceUUID; without this guard the app
// keeps the previous org's clusterName pinned and every GraphQL query
// runs against the wrong shard.
//
// Switching to an org that does have workspaces will re-select one
// automatically (tenant.selectOrg → first workspace), so this view is
// only ever reached for genuinely empty orgs.

import { computed, ref } from 'vue'
import { useTenantStore } from '@/stores/tenant'
import { ArrowRight, FolderTree, Loader2, Sparkles, AlertCircle, Settings } from 'lucide-vue-next'

const tenant = useTenantStore()
const name = ref('')
const busy = ref(false)
const error = ref<string | null>(null)

const trimmed = computed(() => name.value.trim())
const canSubmit = computed(() => trimmed.value.length > 0 && !busy.value && !!tenant.orgUUID)

async function handleCreate() {
  if (!canSubmit.value || !tenant.orgUUID) return
  busy.value = true
  error.value = null
  try {
    const created = await tenant.createWorkspace(tenant.orgUUID, trimmed.value)
    if (!created) {
      error.value = tenant.error ?? 'Failed to create workspace'
    }
    // On success the tenant store selects the new workspace, App.vue's
    // watch on activeWorkspace.clusterName fires auth.setClusterName,
    // AppLayout re-keys the slot, and the original page renders.
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to create workspace'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-xl pt-8">
    <div class="mb-6 flex items-start gap-4">
      <div class="relative flex h-12 w-12 shrink-0 items-center justify-center">
        <div class="absolute inset-0 rounded-xl bg-accent/20 blur-md" />
        <div class="relative flex h-12 w-12 items-center justify-center rounded-xl border border-accent/25 bg-surface-overlay">
          <FolderTree class="h-6 w-6 text-accent" :stroke-width="1.5" />
        </div>
      </div>
      <div class="flex-1">
        <h1 class="flex items-center gap-2 text-[18px] font-bold text-text-primary">
          Create your first workspace
          <Sparkles class="h-4 w-4 text-accent" :stroke-width="1.75" />
        </h1>
        <p class="mt-1 text-[12px] text-text-muted">
          <span class="font-mono text-text-secondary">{{ tenant.activeOrg?.displayName ?? 'This org' }}</span>
          doesn't have a workspace yet. Workspaces are isolated kcp clusters where edges, MCP servers, and workloads live.
        </p>
      </div>
    </div>

    <div class="rounded-xl border border-border-default shadow-sm">
      <div class="space-y-5 rounded-xl border border-border-subtle bg-surface-raised/80 p-6 backdrop-blur">
        <div
          v-if="error"
          class="flex items-center gap-2 rounded-xl border border-danger/20 bg-danger-subtle p-3 text-[12px] text-danger"
        >
          <AlertCircle class="h-3.5 w-3.5 shrink-0" :stroke-width="1.75" />
          {{ error }}
        </div>

        <div>
          <label class="mb-1 block text-[10px] font-semibold uppercase tracking-[0.15em] text-text-muted">
            Workspace name
          </label>
          <input
            v-model="name"
            type="text"
            placeholder="e.g. production"
            class="w-full rounded-xl border border-border-default bg-surface-overlay/60 px-3 py-2.5 font-mono text-[12px] text-text-primary placeholder:text-text-muted/40 focus:border-accent/40 focus:outline-none"
            autofocus
            @keyup.enter="canSubmit && handleCreate()"
          />
          <p class="mt-1 text-[10px] text-text-muted">
            A friendly name. The hub will allocate the underlying kcp cluster automatically.
          </p>
        </div>

        <div class="flex items-center justify-between gap-3 pt-2">
          <router-link
            to="/tenant"
            class="flex items-center gap-1.5 text-[11px] font-medium text-text-muted transition-colors hover:text-text-secondary"
          >
            <Settings class="h-3 w-3" :stroke-width="2" />
            Manage workspaces in settings
          </router-link>
          <button
            type="button"
            class="group flex items-center gap-2 rounded-xl bg-accent px-4 py-2.5 text-[12px] font-semibold text-white transition-all hover:bg-accent-hover hover:shadow-lg hover:shadow-accent/20 active:scale-[0.98] disabled:pointer-events-none disabled:opacity-40"
            :disabled="!canSubmit"
            @click="handleCreate"
          >
            <Loader2 v-if="busy" class="h-3.5 w-3.5 animate-spin" :stroke-width="2" />
            <span>{{ busy ? 'Creating…' : 'Create workspace' }}</span>
            <ArrowRight v-if="!busy" class="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" :stroke-width="2" />
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
