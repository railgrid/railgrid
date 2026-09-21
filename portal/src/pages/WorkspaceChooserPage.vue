<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowRight, FolderTree, Loader2, RefreshCw } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { isWorkspaceAvailable, isWorkspaceUsable, useTenantStore, type WorkspaceRow } from '@/stores/tenant'
import { readOrganizationWorkspace } from '@/router/landingPreference'
import { preferredWorkspace } from '@/router/workspaceEntry'

const tenant = useTenantStore()
const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const orgID = computed(() => String(route.params.orgID))
const org = computed(() => tenant.activeOrg)
const rows = computed(() => (tenant.workspacesByOrg[orgID.value] ?? []).filter(isWorkspaceAvailable))
const search = ref('')
const visible = computed(() => rows.value.filter(row => `${row.displayName ?? ''} ${row.uuid}`.toLowerCase().includes(search.value.toLowerCase())))
const loading = ref(true)
const error = ref('')
const creating = ref(false)
const name = ref('')
const waiting = ref(false)
const timedOut = ref(false)
const canCreate = computed(() => org.value?.role === 'admin' || org.value?.workspaceCreation === 'members')
let generation = 0
let timer: ReturnType<typeof setTimeout> | undefined
let started = 0

async function enter(workspace: WorkspaceRow) {
  if (!isWorkspaceUsable(workspace) || loading.value || error.value) return
  try {
    await router.replace({ name: 'dashboard', params: { orgID: orgID.value, workspaceID: workspace.uuid } })
  } catch { error.value = 'Unable to open this workspace. Try again.' }
}

async function load(revision = generation) {
  const target = orgID.value
  loading.value = true
  error.value = ''
  await tenant.fetchWorkspaces(target, { selectDefault: false })
  if (revision !== generation) return
  loading.value = false
  if (tenant.workspaceLoadStateByOrg[target] !== 'ready') {
    error.value = 'Unable to load workspaces. Check your connection and try again.'
    waiting.value = false
    return
  }
  const workspace = preferredWorkspace(rows.value, readOrganizationWorkspace(auth.user, target))
  if (workspace) {
    await enter(workspace)
    return
  }
  waiting.value = rows.value.some(row => !isWorkspaceUsable(row)) || (route.query.preparing === '1' && rows.value.length === 0)
  // Poll only while provisioning, and stop after a minute or leaving this page.
  timedOut.value = waiting.value && Date.now() - started >= 60_000
  if (waiting.value && !timedOut.value) timer = setTimeout(() => { void load(revision) }, 2500)
}
function retry() {
  clearTimeout(timer)
  timedOut.value = false
  started = Date.now()
  void load(++generation)
}
async function create() {
  if (!name.value.trim() || !canCreate.value || creating.value) return
  const revision = generation
  const target = orgID.value
  creating.value = true
  error.value = ''
  try {
    const created = await tenant.createWorkspace(target, name.value.trim(), { selectCreated: false })
    if (revision !== generation) return
    if (!created) throw new Error('Unable to create the workspace. Try again.')
    name.value = ''
    retry()
  } catch (e) {
    if (revision === generation) error.value = e instanceof Error ? e.message : 'Unable to create workspace.'
  } finally { if (revision === generation) creating.value = false }
}
watch(orgID, () => {
  clearTimeout(timer)
  search.value = ''
  creating.value = false
  waiting.value = false
  retry()
}, { immediate: true })
onBeforeUnmount(() => { generation++; clearTimeout(timer) })
</script>

<template>
  <main class="mx-auto w-full max-w-3xl px-4 py-10 sm:px-8">
    <router-link to="/organizations" class="k-btn k-btn--text mb-6">Change organization</router-link>
    <h1 class="break-words text-2xl font-semibold text-text-primary">Choose a workspace in {{ org?.displayName || 'your organization' }}</h1>
    <p class="mt-2 text-sm text-text-secondary">Open a workspace to use its tools and resources.</p>

    <div v-if="error" role="alert" class="mt-6 k-alert k-alert--error">
      <p>{{ error }}</p>
      <button class="k-btn k-btn--secondary mt-3" @click="retry"><RefreshCw class="h-4 w-4" />Try again</button>
    </div>
    <div v-else-if="loading && !rows.length" role="status" class="flex items-center gap-3 py-10 text-text-secondary">
      <Loader2 class="h-5 w-5 animate-spin" />Loading workspaces…
    </div>
    <template v-else>
      <input v-if="rows.length > 5" v-model="search" type="search" class="k-input mt-6" aria-label="Search workspaces" placeholder="Search workspaces" />
      <div v-if="rows.length" class="mt-6 divide-y divide-border-subtle border-y border-border-subtle" :aria-busy="loading">
        <button v-for="workspace in visible" :key="workspace.uuid" type="button"
          class="flex min-h-16 w-full items-center gap-4 px-3 py-4 text-left transition-colors hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-wait"
          :disabled="!isWorkspaceUsable(workspace) || loading" @click="enter(workspace)">
          <FolderTree class="h-5 w-5 shrink-0 text-accent" aria-hidden="true" />
          <span class="min-w-0 flex-1"><span class="block truncate font-medium text-text-primary">{{ workspace.displayName || workspace.uuid.slice(0, 8) }}</span>
            <span v-if="rows.filter(row => row.displayName === workspace.displayName).length > 1" class="text-xs text-text-secondary">{{ workspace.uuid.slice(0, 8) }}</span>
          </span>
          <span class="flex shrink-0 items-center gap-2 text-sm text-accent"><template v-if="isWorkspaceUsable(workspace)">Open workspace <ArrowRight class="h-4 w-4" /></template><template v-else><Loader2 class="h-4 w-4 animate-spin" />Preparing…</template></span>
        </button>
        <p v-if="!visible.length" class="py-6 text-sm text-text-secondary">No workspaces match your search.</p>
      </div>
      <div v-if="waiting" role="status" class="mt-6 text-sm text-text-secondary">
        <p class="font-medium text-text-primary">Preparing your workspace…</p>
        <p v-if="timedOut" class="mt-1">This is taking longer than expected. Check again to resume waiting.</p>
        <p v-else class="mt-1">We’ll open it when it’s ready if it’s your only workspace or your last visited one.</p>
        <button class="k-btn k-btn--text mt-3" :disabled="loading" @click="retry">Check again</button>
      </div>
      <div v-else-if="!rows.length" class="mt-8">
        <h2 class="text-lg font-medium text-text-primary">No workspaces yet</h2>
        <form v-if="canCreate" class="mt-4 flex flex-wrap items-end gap-3" @submit.prevent="create">
          <label class="min-w-0 flex-1 text-sm text-text-secondary">Workspace name<input v-model="name" class="k-input mt-2" required placeholder="e.g. Development" :disabled="creating" /></label>
          <button class="k-btn k-btn--primary" :disabled="creating || !name.trim()">{{ creating ? 'Creating…' : 'Create workspace' }}</button>
        </form>
        <p v-else class="mt-2 text-sm text-text-secondary">Ask an organization admin to create a workspace or give you access.</p>
      </div>
    </template>
    <router-link :to="`/${orgID}/settings/workspaces`" class="k-btn k-btn--text mt-8">Manage workspaces</router-link>
  </main>
</template>
