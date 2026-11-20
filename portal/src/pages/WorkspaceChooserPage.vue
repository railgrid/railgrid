<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowRight, FolderTree, Loader2 } from 'lucide-vue-next'
import InlineNotification from '@/portalkit/InlineNotification.vue'
import { useAuthStore } from '@/stores/auth'
import { useRouteContextStore } from '@/stores/routeContext'
import { authFetch } from '@/auth/session'
import { isWorkspaceAvailable, isWorkspaceUsable, useTenantStore, type OrgRow, type WorkspaceRow } from '@/stores/tenant'
import { readOrganizationWorkspace } from '@/router/landingPreference'
import { preferredWorkspace } from '@/router/workspaceEntry'

const tenant = useTenantStore()
const auth = useAuthStore()
const context = useRouteContextStore()
const route = useRoute()
const router = useRouter()
const orgID = computed(() => String(route.params.orgID))
const loadedOrg = ref<OrgRow | null>(null)
const org = computed(() => loadedOrg.value ?? tenant.activeOrg)
const rows = computed(() => (tenant.workspacesByOrg[orgID.value] ?? []).filter(isWorkspaceAvailable))
const search = ref('')
const visible = computed(() => rows.value.filter(row => `${row.displayName ?? ''} ${row.uuid}`.toLowerCase().includes(search.value.toLowerCase())))
const loading = ref(false)
const error = ref('')
const hasSnapshot = ref(false)
const heading = ref<HTMLElement | null>(null)
const createError = ref('')
const navigationError = ref<{ workspace: WorkspaceRow; reload: boolean } | null>(null)
const restoreNavigationFocus = ref(false)
const opening = ref<string | null>(null)
const creating = ref(false)
const name = ref('')
const waiting = ref(false)
const timedOut = ref(false)
const canCreate = computed(() => org.value?.role === 'admin' || org.value?.workspaceCreation === 'members')
let generation = 0
let timer: ReturnType<typeof setTimeout> | undefined
let started = 0
let workspaceToRetry: string | null = null

async function enter(workspace: WorkspaceRow) {
  if (!isWorkspaceUsable(workspace) || loading.value || error.value || opening.value || creating.value) return
  const revision = generation
  const target = orgID.value
  const focused = typeof document === 'undefined' ? null : document.activeElement
  opening.value = workspace.uuid
  try {
    const failure = await router.replace({
      name: 'dashboard',
      params: { orgID: target, workspaceID: workspace.uuid },
      ...(route.query.firstTask === 'personal' || route.query.firstTask === 'team' ? { query: { firstTask: route.query.firstTask } } : {}),
    })
    if (revision === generation) navigationError.value = failure ? { workspace, reload: false } : null
  } catch {
    if (revision === generation) navigationError.value = { workspace, reload: true }
  } finally {
    if (revision === generation) {
      opening.value = null
      restoreNavigationFocus.value = !!(navigationError.value && focused && heading.value?.closest('main')?.contains(focused))
    }
  }
}

const chooserBlocked = computed(() => context.blocksRoute(route.path, !!route.meta.public))
watch([restoreNavigationFocus, chooserBlocked], async ([restore, blocked]) => {
  if (!restore || blocked) return
  const revision = generation
  restoreNavigationFocus.value = false
  await nextTick()
  if (revision === generation && !chooserBlocked.value && document.activeElement === document.body) heading.value?.focus()
})

const navigationRetryTarget = computed(() => rows.value.find(row =>
  row.uuid === navigationError.value?.workspace.uuid && isWorkspaceUsable(row),
))

function retryNavigation() {
  const workspace = navigationRetryTarget.value
  if (!workspace || opening.value || loading.value || error.value) return
  if (!navigationError.value?.reload) return enter(workspace)
  // A failed module import can be cached for the lifetime of the document.
  // Reload the chooser and retry the same workspace after checking access.
  opening.value = workspace.uuid
  const history = router.options.history
  history.replace(route.fullPath, { retryWorkspace: workspace.uuid })
  window.location.reload()
}

async function load(revision = generation) {
  const target = orgID.value
  loading.value = true
  try {
    // Read lifecycle first: a completed response must precede the workspace
    // list, otherwise a concurrent pre-creation list could look permanently empty.
    const response = await authFetch(`/api/orgs/${encodeURIComponent(target)}`, { headers: { 'X-Railgrid-Org': target } })
    if (revision !== generation) return
    if (!response.ok) throw new Error('Unable to check organization readiness. Try again.')
    const latest = await response.json() as OrgRow
    if (revision !== generation) return
    if (latest.uuid !== target || latest.deletionRequestedAt) throw new Error('This organization is unavailable.')
    await tenant.fetchWorkspaces(target, { selectDefault: false })
    if (revision !== generation) return
    loadedOrg.value = latest
  } catch (e) {
    if (revision !== generation) return
    loading.value = false
    waiting.value = false
    error.value = e instanceof Error ? e.message : 'Unable to check organization readiness. Try again.'
    return
  }
  if (revision !== generation) return
  loading.value = false
  if (tenant.workspaceLoadStateByOrg[target] !== 'ready') {
    error.value = 'Unable to load workspaces. Check your connection and try again.'
    waiting.value = false
    return
  }
  hasSnapshot.value = true
  error.value = ''
  if (!navigationRetryTarget.value) navigationError.value = null
  const workspace = workspaceToRetry
    ? rows.value.find(row => row.uuid === workspaceToRetry && isWorkspaceUsable(row))
    : preferredWorkspace(rows.value, readOrganizationWorkspace(auth.user, target))
  workspaceToRetry = null
  if (workspace) {
    waiting.value = false
    timedOut.value = false
    await enter(workspace)
    return
  }
  waiting.value = rows.value.some(row => !isWorkspaceUsable(row)) || org.value?.initialWorkspacePending === true
  // Poll only while provisioning, and stop after a minute or leaving this page.
  timedOut.value = waiting.value && Date.now() - started >= 60_000
  if (waiting.value && !timedOut.value) timer = setTimeout(() => { void load(revision) }, 2500)
}
async function retry() {
  if (loading.value || creating.value || opening.value) return
  const focused = typeof document === 'undefined' ? null : document.activeElement
  clearTimeout(timer)
  timedOut.value = false
  started = Date.now()
  const revision = ++generation
  await load(revision)
  await nextTick()
  if (revision === generation && focused && !focused.isConnected && document.activeElement === document.body) heading.value?.focus()
}
async function create() {
  if (!name.value.trim() || !canCreate.value || creating.value || loading.value || error.value || opening.value) return
  const revision = generation
  const target = orgID.value
  creating.value = true
  try {
    const created = await tenant.createWorkspace(target, name.value.trim(), { selectCreated: false })
    if (revision !== generation) return
    if (!created) throw new Error('Unable to create the workspace. Try again.')
    createError.value = ''
    name.value = ''
    creating.value = false
    await retry()
  } catch (e) {
    if (revision === generation) createError.value = e instanceof Error ? e.message : 'Unable to create workspace.'
  } finally { if (revision === generation) creating.value = false }
}
watch(orgID, () => {
  clearTimeout(timer)
  search.value = ''
  name.value = ''
  loadedOrg.value = null
  hasSnapshot.value = false
  loading.value = false
  error.value = ''
  createError.value = ''
  navigationError.value = null
  restoreNavigationFocus.value = false
  opening.value = null
  creating.value = false
  waiting.value = false
  const history = router.options.history
  const retryWorkspace = history.state.retryWorkspace
  workspaceToRetry = typeof retryWorkspace === 'string' ? retryWorkspace : null
  if (workspaceToRetry) history.replace(route.fullPath, { retryWorkspace: null })
  void retry()
}, { immediate: true })
onBeforeUnmount(() => { generation++; clearTimeout(timer) })
</script>

<template>
  <main class="mx-auto w-full max-w-3xl px-4 py-10 sm:px-8">
    <router-link to="/organizations" class="k-btn k-btn--text mb-6">Change organization</router-link>
    <h1 ref="heading" tabindex="-1" class="break-words text-2xl font-semibold text-text-primary">Choose a workspace in {{ org?.displayName || 'your organization' }}</h1>
    <p class="mt-2 text-sm text-text-secondary">Open a workspace to use its tools and resources.</p>

    <InlineNotification v-if="error" class="mt-6" :tone="hasSnapshot ? 'warning' : 'error'"
      title="Could not load workspaces" :message="`${hasSnapshot ? 'Showing the last successful result. ' : ''}${error}`"
      action-label="Try again" action-busy-label="Trying again…" :action-busy="loading" @action="retry" />
    <InlineNotification v-if="navigationError" class="mt-6" tone="error"
      :title="`Could not open ${navigationError.workspace.displayName || navigationError.workspace.uuid}`"
      :action-label="navigationRetryTarget ? 'Try again' : ''" action-busy-label="Opening…"
      :action-busy="!!opening || loading || !!error" @action="retryNavigation" />
    <div v-if="!error && loading && !hasSnapshot" role="status" class="flex items-center gap-3 py-10 text-text-secondary">
      <Loader2 class="h-5 w-5 animate-spin" />Loading workspaces…
    </div>
    <template v-else-if="hasSnapshot || !error">
      <input v-if="rows.length > 5" v-model="search" type="search" class="k-input mt-6" aria-label="Search workspaces" placeholder="Search workspaces" />
      <div v-if="rows.length" class="mt-6 divide-y divide-border-subtle border-y border-border-subtle" :aria-busy="loading">
        <button v-for="workspace in visible" :key="workspace.uuid" type="button"
          class="flex min-h-16 w-full items-center gap-4 px-3 py-4 text-left transition-colors hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-wait"
          :disabled="!isWorkspaceUsable(workspace) || loading || !!error || !!opening" @click="enter(workspace)">
          <FolderTree class="h-5 w-5 shrink-0 text-accent" aria-hidden="true" />
          <span class="min-w-0 flex-1"><span class="block truncate font-medium text-text-primary">{{ workspace.displayName || workspace.uuid.slice(0, 8) }}</span>
            <span v-if="rows.filter(row => row.displayName === workspace.displayName).length > 1" class="text-xs text-text-secondary">{{ workspace.uuid.slice(0, 8) }}</span>
          </span>
          <span class="flex shrink-0 items-center gap-2 text-sm text-accent"><template v-if="isWorkspaceUsable(workspace)">Open workspace <ArrowRight class="h-4 w-4" /></template><template v-else><Loader2 class="h-4 w-4 animate-spin" />Preparing…</template></span>
        </button>
        <p v-if="!visible.length" class="py-6 text-sm text-text-secondary">No workspaces match your search.</p>
      </div>
      <div v-if="waiting && !error" role="status" class="mt-6 text-sm text-text-secondary">
        <p class="font-medium text-text-primary">Preparing your workspace…</p>
        <p v-if="timedOut" class="mt-1">This is taking longer than expected. Check again to resume waiting.</p>
        <p v-else class="mt-1">We’ll open it when it’s ready if it’s your only workspace or your last visited one.</p>
        <button class="k-btn k-btn--text mt-3" :aria-disabled="loading ? 'true' : undefined" :aria-busy="loading" @click="retry">{{ loading ? 'Checking…' : 'Check again' }}</button>
      </div>
      <div v-else-if="!rows.length" class="mt-8">
        <h2 class="text-lg font-medium text-text-primary">{{ error ? 'No workspaces in the last successful result' : 'No workspaces yet' }}</h2>
        <InlineNotification v-if="createError" class="mt-4" tone="error" title="Could not create workspace" :message="createError" />
        <form v-if="canCreate" class="mt-4 flex flex-wrap items-end gap-3" @submit.prevent="create">
          <label class="min-w-0 flex-1 text-sm text-text-secondary">Workspace name<input v-model="name" class="k-input mt-2" required placeholder="e.g. Development" :disabled="creating || loading || !!error" /></label>
          <button class="k-btn k-btn--primary" :disabled="creating || loading || !!error || !name.trim()">{{ creating ? 'Creating…' : 'Create workspace' }}</button>
        </form>
        <p v-else class="mt-2 text-sm text-text-secondary">Ask an organization admin to create a workspace or give you access.</p>
      </div>
    </template>
    <router-link :to="`/${orgID}/settings/organizations`" class="k-btn k-btn--text mt-8">Organization settings</router-link>
  </main>
</template>
