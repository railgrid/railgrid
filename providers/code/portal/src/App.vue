<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { KedgeContext } from './types'
import { setBasePath, setTenant, setToken } from './api'
import ConnectionsView from './views/ConnectionsView.vue'
import RepositoriesView from './views/RepositoriesView.vue'
import RepoDetailView from './views/RepoDetailView.vue'
import PackagesView from './views/PackagesView.vue'

// Sub-path routing (the shell pushes the trailing /providers/code/<sub> segment):
//   ''  | 'connections'        → Connections
//   'repositories'             → Repositories
//   'repositories/<name>'      → RepoDetail
//   'packages'                 → Packages (workspace-wide)
const props = defineProps<{ ctx: KedgeContext | null }>()

interface Route {
  page: 'connections' | 'repositories' | 'packages'
  repo?: string
}

function parse(sub: string | null | undefined): Route {
  const s = (sub ?? '').replace(/^\/+|\/+$/g, '')
  if (s === '' || s === 'connections') return { page: 'connections' }
  if (s === 'packages') return { page: 'packages' }
  const parts = s.split('/')
  if (parts[0] === 'repositories') {
    return parts.length > 1 ? { page: 'repositories', repo: decodeURIComponent(parts[1]) } : { page: 'repositories' }
  }
  return { page: 'connections' }
}

const route = computed(() => parse(props.ctx?.subPath))

// Feed identity into the api client whenever the shell re-pushes context.
watch(() => props.ctx?.basePath, v => setBasePath(v), { immediate: true })
watch(() => props.ctx?.token, v => setToken(v), { immediate: true })
watch(() => props.ctx?.tenant, v => setTenant(v), { immediate: true })

const hasTenant = computed(() => !!props.ctx?.tenant)

// navigate dispatches a kedge-navigate CustomEvent from the component root so it
// bubbles up to the <kedge-provider-code> element, where ProviderFrame listens
// and pushes the shell's vue-router. detail.path is the trailing segment the
// shell appends to /providers/code/.
const rootRef = ref<HTMLElement | null>(null)
function navigate(path: string) {
  const el = rootRef.value
  if (!el) return
  el.dispatchEvent(new CustomEvent('kedge-navigate', { detail: { path }, bubbles: true }))
}
</script>

<template>
  <div ref="rootRef" class="app">
    <nav class="tabs">
      <button :class="{ active: route.page === 'connections' }" @click="navigate('connections')">Connections</button>
      <button :class="{ active: route.page === 'repositories' }" @click="navigate('repositories')">Repositories</button>
      <button :class="{ active: route.page === 'packages' }" @click="navigate('packages')">Packages</button>
    </nav>

    <p v-if="!hasTenant" class="empty">Select a workspace to manage code.</p>

    <template v-else>
      <ConnectionsView v-if="route.page === 'connections'" />
      <PackagesView v-else-if="route.page === 'packages'" @open="(n: string) => navigate('repositories/' + encodeURIComponent(n))" />
      <RepoDetailView v-else-if="route.repo" :name="route.repo" @back="navigate('repositories')" />
      <RepositoriesView v-else @open="(n: string) => navigate('repositories/' + encodeURIComponent(n))" />
    </template>
  </div>
</template>
