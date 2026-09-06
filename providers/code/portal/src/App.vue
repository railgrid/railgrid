<script setup lang="ts">
import { computed, provide, ref, watch } from 'vue'
import { GitBranch, Package, Plug } from 'lucide-vue-next'
import type { FarosContext } from './types'
import { setAPIContext, setBasePath } from './api'
import ConnectionsView from './views/ConnectionsView.vue'
import ConnectionCreateView from './views/ConnectionCreateView.vue'
import ConnectionDetailView from './views/ConnectionDetailView.vue'
import RepositoriesView from './views/RepositoriesView.vue'
import RepositoryCreateView from './views/RepositoryCreateView.vue'
import RepoDetailView from './views/RepoDetailView.vue'
import PackagesView from './views/PackagesView.vue'
import ConfirmDialog from './portalkit/ConfirmDialog.vue'
import { resolveConfirm } from './portalkit/confirm'
import { createResourceDeletions } from './refresh'
import Tabs from './portalkit/Tabs.vue'
import { contextGenerationKey } from './context'
import {
  clearCodeReturnIntent,
  codeJourneyStorage,
  codeJourneyTenantKey,
  readCodeReturnIntent,
  writeCodeReturnIntent,
  type CodeReturnPath,
} from './journey'
import { codeNavigationDetail, parseCodeSubPath, type CodeRoute } from './routes'

// Sub-path routing (the shell pushes the trailing /providers/code/<sub> segment):
//   ''  | 'connections'        → Connections
//   'connections/<name>'       → ConnectionDetail
//   'repositories'             → Repositories
//   'repositories/<name>'      → RepoDetail
//   'packages'                 → Packages (workspace-wide)
//   'create/connection/token'  → token connection creation
//   'create/connection/github' → GitHub OAuth connection creation
//   'create/repository'        → repository creation
const props = defineProps<{ ctx: FarosContext | null }>()

const route = computed<CodeRoute>(() => parseCodeSubPath(props.ctx?.subPath))
const contextGeneration = ref(0)
provide(contextGenerationKey, contextGeneration)
const contextInitialized = computed(() => props.ctx !== null)
const deletions = createResourceDeletions()
const journeyStorage = codeJourneyStorage()
const prerequisiteReturnPath = ref<CodeReturnPath | null>(null)
const prerequisiteExpectedPath = ref<string | null>(null)
const prerequisiteTenantKey = ref<string | null>(null)
let observedJourneyAuthorityKey: string | null = null
let deletionAuthority = ''

function journeyTenantKey(): string | null {
  if (!props.ctx?.tenant) return null
  const caller = props.ctx.user?.sub ?? props.ctx.user?.email ?? null
  return codeJourneyTenantKey(props.ctx.tenant, caller)
}

function activeJourneyPath(): string {
  return (props.ctx?.subPath ?? '').replace(/^\/+|\/+$/g, '')
}

// Feed identity into the API client and remount the active route whenever its
// authority changes. This clears actionable old-workspace state immediately and
// lets each unmounted refresh controller reject late responses.
watch(
  () => [props.ctx?.basePath, props.ctx?.token, props.ctx?.tenant, props.ctx?.user?.sub, props.ctx?.user?.email] as const,
  ([basePath, token, tenant, userSub]) => {
    // This must run synchronously. The keyed route remount is a Vue render
    // effect and therefore happens later; an async form continuation can
    // otherwise commit between the context update and that unmount.
    contextGeneration.value += 1
    // The dialog is module-global, so remounting the routed page is not enough
    // to revoke a confirmation opened under the previous authority.
    resolveConfirm(false)
    const nextDeletionAuthority = JSON.stringify([basePath ?? '', tenant ?? '', userSub ?? ''])
    if (nextDeletionAuthority !== deletionAuthority) deletions.clear()
    deletionAuthority = nextDeletionAuthority
    setBasePath(basePath)
    setAPIContext({ fetch: props.ctx?.fetch, token, tenant, user: userSub })
  },
  { immediate: true, flush: 'sync' },
)
watch(
  () => [props.ctx?.tenant, props.ctx?.subPath, props.ctx?.user?.sub, props.ctx?.user?.email] as const,
  () => {
    const tenantKey = journeyTenantKey()
    const activePath = activeJourneyPath()
    if (observedJourneyAuthorityKey && observedJourneyAuthorityKey !== tenantKey) {
      clearCodeReturnIntent(journeyStorage, observedJourneyAuthorityKey)
    }
    observedJourneyAuthorityKey = tenantKey
    const storedReturnPath = tenantKey
      ? readCodeReturnIntent(journeyStorage, tenantKey, activePath)
      : null
    if (storedReturnPath) {
      prerequisiteReturnPath.value = storedReturnPath
      prerequisiteExpectedPath.value = null
      prerequisiteTenantKey.value = tenantKey
      return
    }

    // Storage is reload continuity, not live-transition authority. Preserve an
    // intent seeded by the prerequisite CTA when storage is blocked or full,
    // but only for the exact tenant and route that CTA selected.
    if (prerequisiteReturnPath.value
      && prerequisiteExpectedPath.value === activePath
      && prerequisiteTenantKey.value === tenantKey) {
      prerequisiteExpectedPath.value = null
      return
    }
    prerequisiteReturnPath.value = null
    prerequisiteExpectedPath.value = null
    prerequisiteTenantKey.value = null
  },
  { immediate: true },
)

const hasTenant = computed(() => !!props.ctx?.tenant)

const tabs = [
  { id: 'connections', label: 'Connections', icon: Plug },
  { id: 'repositories', label: 'Repositories', icon: GitBranch },
  { id: 'packages', label: 'Packages', icon: Package },
] as const

// navigate dispatches a faros-navigate CustomEvent from the component root so it
// bubbles up to the <faros-provider-code> element, where ProviderFrame listens
// and pushes the shell's vue-router. detail.path is the trailing segment the
// shell appends to /providers/code/.
const rootRef = ref<HTMLElement | null>(null)
function dispatchNavigation(path: string, options: { replace?: boolean } = {}) {
  const el = rootRef.value
  if (!el) return
  el.dispatchEvent(new CustomEvent('faros-navigate', { detail: codeNavigationDetail(path, options), bubbles: true }))
}

function clearCurrentJourneyIntent(): void {
  const tenantKey = journeyTenantKey()
  if (tenantKey) clearCodeReturnIntent(journeyStorage, tenantKey)
}

function navigate(path: string, options: { replace?: boolean } = {}) {
  prerequisiteReturnPath.value = null
  prerequisiteExpectedPath.value = null
  prerequisiteTenantKey.value = null
  clearCurrentJourneyIntent()
  dispatchNavigation(path, options)
}

function startRepositoryConnection(): void {
  const expectedPath = 'create/connection/token'
  const tenantKey = journeyTenantKey()
  prerequisiteReturnPath.value = 'create/repository'
  prerequisiteExpectedPath.value = expectedPath
  prerequisiteTenantKey.value = tenantKey
  if (tenantKey) writeCodeReturnIntent(journeyStorage, tenantKey, 'create/repository', expectedPath)
  dispatchNavigation(expectedPath)
}

function completeConnectionPrerequisite(success: boolean, connectionName?: string): void {
  const returnPath = prerequisiteReturnPath.value
  prerequisiteReturnPath.value = null
  prerequisiteExpectedPath.value = null
  prerequisiteTenantKey.value = null
  clearCurrentJourneyIntent()
  if (returnPath) {
    dispatchNavigation(success ? returnPath : 'repositories', { replace: true })
    return
  }
  dispatchNavigation(success && connectionName
    ? `connections/${encodeURIComponent(connectionName)}`
    : 'connections', { replace: true })
}
</script>

<template>
  <div ref="rootRef" class="app">
    <template v-if="!contextInitialized">
      <section class="page" role="status" aria-live="polite" aria-busy="true" aria-label="Loading workspace context">
        <header class="page-head">
          <div>
            <h2 class="page-title">Code</h2>
            <p class="page-meta">Loading workspace context…</p>
          </div>
        </header>
        <div class="detail-loading" aria-hidden="true">
          <div v-for="i in 4" :key="i" class="shimmer detail-loading-line" />
        </div>
      </section>
    </template>

    <template v-else>
      <template v-if="!route.repo && !route.connection">
        <template v-if="!route.create">
          <Tabs :tabs="tabs" :active="route.page" aria-label="Code provider sections" @select="navigate" />
        </template>
      </template>

      <p v-if="!hasTenant" class="empty">Select a workspace to manage code.</p>

      <template v-else>
        <ConnectionCreateView
          v-if="route.create?.resource === 'connection'"
          :key="`${contextGeneration}:create-connection:${route.create.method}`"
          :method="route.create.method"
          :deletions="deletions"
          @cancel="completeConnectionPrerequisite(false)"
          @created="(n: string) => completeConnectionPrerequisite(true, n)"
        />
        <RepositoryCreateView
          v-if="route.create?.resource === 'repository'"
          :key="`${contextGeneration}:create-repository`"
          :deletions="deletions"
          @cancel="navigate('repositories', { replace: true })"
          @created="(n: string) => navigate('repositories/' + encodeURIComponent(n), { replace: true })"
          @create-connection="startRepositoryConnection"
        />
        <ConnectionDetailView v-if="route.page === 'connections' && route.connection" :key="`${contextGeneration}:connection:${route.connection}`" :name="route.connection" :deletions="deletions" @back="navigate('connections')" />
        <RepoDetailView v-if="route.repo" :key="`${contextGeneration}:repository:${route.repo}`" :name="route.repo" :deletions="deletions" @back="navigate('repositories')" />
        <PackagesView v-if="route.page === 'packages'" :key="`${contextGeneration}:packages`" @open="(n: string) => navigate('repositories/' + encodeURIComponent(n))" />

        <!-- Keep collection-local query/filter/page/scroll state while a routed
             create or detail surface is active. The context key still drops the
             cache immediately when workspace authority changes. -->
        <KeepAlive :max="1">
          <ConnectionsView
            v-if="route.page === 'connections' && !route.create && !route.connection"
            :key="`${contextGeneration}:connections`"
            :deletions="deletions"
            @open="(n: string) => navigate('connections/' + encodeURIComponent(n))"
            @create="(method: 'token' | 'github') => navigate('create/connection/' + method)"
          />
        </KeepAlive>
        <KeepAlive :max="1">
          <RepositoriesView
            v-if="route.page === 'repositories' && !route.create && !route.repo"
            :key="`${contextGeneration}:repositories`"
            :deletions="deletions"
            @open="(n: string) => navigate('repositories/' + encodeURIComponent(n))"
            @create="navigate('create/repository')"
            @create-connection="startRepositoryConnection"
          />
        </KeepAlive>
      </template>
    </template>

    <ConfirmDialog />
  </div>
</template>
