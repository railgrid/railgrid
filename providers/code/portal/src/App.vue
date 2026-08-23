<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { GitBranch, Package, Plug } from 'lucide-vue-next'
import type { FarosContext } from './types'
import { setAPIContext, setBasePath } from './api'
import ConnectionsView from './views/ConnectionsView.vue'
import ConnectionDetailView from './views/ConnectionDetailView.vue'
import RepositoriesView from './views/RepositoriesView.vue'
import RepoDetailView from './views/RepoDetailView.vue'
import PackagesView from './views/PackagesView.vue'
import ConfirmDialog from './portalkit/ConfirmDialog.vue'
import { resolveConfirm } from './portalkit/confirm'
import { createResourceDeletions } from './refresh'
import Tabs from './portalkit/Tabs.vue'

// Sub-path routing (the shell pushes the trailing /providers/code/<sub> segment):
//   ''  | 'connections'        → Connections
//   'connections/<name>'       → ConnectionDetail
//   'repositories'             → Repositories
//   'repositories/<name>'      → RepoDetail
//   'packages'                 → Packages (workspace-wide)
const props = defineProps<{ ctx: FarosContext | null }>()

interface Route {
  page: 'connections' | 'repositories' | 'packages'
  repo?: string
  connection?: string
}

function parse(sub: string | null | undefined): Route {
  const s = (sub ?? '').replace(/^\/+|\/+$/g, '')
  if (s === '' || s === 'connections') return { page: 'connections' }
  if (s === 'packages') return { page: 'packages' }
  const parts = s.split('/')
  if (parts[0] === 'connections') {
    return parts.length > 1 ? { page: 'connections', connection: decodeURIComponent(parts[1]) } : { page: 'connections' }
  }
  if (parts[0] === 'repositories') {
    return parts.length > 1 ? { page: 'repositories', repo: decodeURIComponent(parts[1]) } : { page: 'repositories' }
  }
  return { page: 'connections' }
}

const route = computed(() => parse(props.ctx?.subPath))
const contextGeneration = ref(0)
const contextInitialized = computed(() => props.ctx !== null)
const deletions = createResourceDeletions()
let deletionAuthority = ''

// Feed identity into the API client and remount the active route whenever its
// authority changes. This clears actionable old-workspace state immediately and
// lets each unmounted refresh controller reject late responses.
watch(
  () => [props.ctx?.basePath, props.ctx?.token, props.ctx?.tenant, props.ctx?.user?.sub] as const,
  ([basePath, token, tenant, userSub]) => {
    // The dialog is module-global, so remounting the routed page is not enough
    // to revoke a confirmation opened under the previous authority.
    resolveConfirm(false)
    const nextDeletionAuthority = JSON.stringify([basePath ?? '', tenant ?? '', userSub ?? ''])
    if (nextDeletionAuthority !== deletionAuthority) deletions.clear()
    deletionAuthority = nextDeletionAuthority
    setBasePath(basePath)
    setAPIContext({ token, tenant })
    contextGeneration.value += 1
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
function navigate(path: string) {
  const el = rootRef.value
  if (!el) return
  el.dispatchEvent(new CustomEvent('faros-navigate', { detail: { path }, bubbles: true }))
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
      <Tabs :tabs="tabs" :active="route.page" aria-label="Code provider sections" @select="navigate" />

      <p v-if="!hasTenant" class="empty">Select a workspace to manage code.</p>

      <template v-else>
        <ConnectionDetailView v-if="route.page === 'connections' && route.connection" :key="`${contextGeneration}:connection:${route.connection}`" :name="route.connection" :deletions="deletions" @back="navigate('connections')" />
        <ConnectionsView v-else-if="route.page === 'connections'" :key="`${contextGeneration}:connections`" :deletions="deletions" @open="(n: string) => navigate('connections/' + encodeURIComponent(n))" />
        <PackagesView v-else-if="route.page === 'packages'" :key="`${contextGeneration}:packages`" @open="(n: string) => navigate('repositories/' + encodeURIComponent(n))" />
        <RepoDetailView v-else-if="route.repo" :key="`${contextGeneration}:repository:${route.repo}`" :name="route.repo" :deletions="deletions" @back="navigate('repositories')" />
        <RepositoriesView v-else :key="`${contextGeneration}:repositories`" :deletions="deletions" @open="(n: string) => navigate('repositories/' + encodeURIComponent(n))" />
      </template>
    </template>

    <ConfirmDialog />
  </div>
</template>
