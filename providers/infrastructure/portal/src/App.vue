<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import CatalogPage from './views/CatalogPage.vue'
import ProvisionPage from './views/ProvisionPage.vue'
import InstanceListPage from './views/InstanceListPage.vue'
import InstanceDetailPage from './views/InstanceDetailPage.vue'
import MissingCredentialsPage from './views/MissingCredentialsPage.vue'
import { setBasePath, setTenant, setToken } from './api'
import type { KedgeContext } from './types'

// Two top-level pages: 'templates' and 'instances'. Sub-routes:
//
//   ''                          → templates (default landing)
//   'templates'                 → templates
//   'templates/<name>'          → provision form for that template
//   'instances'                 → my instances
//   'instances/<name>'          → instance detail
//   'missing-credentials'       → onboarding error (provision side-effect)
//
// The shell's vue-router parses /providers/infrastructure/<rest>
// and pushes <rest> to us via kedgeContext.subPath. Internal nav
// dispatches a 'kedge-navigate' CustomEvent (bubbles up to
// ProviderFrame.vue's listener, which calls router.push), so the
// browser URL stays in sync — refresh, back, forward all land on
// the same page. Previously navigation was tracked in a local ref
// and refresh always snapped back to the catalog.

const props = defineProps<{ ctx: KedgeContext | null }>()

interface Route {
  page: 'templates' | 'instances' | 'missing-credentials'
  id?: string
}

function parseSubPath(sub: string | null | undefined): Route {
  const s = (sub ?? '').replace(/^\/+|\/+$/g, '')
  if (s === '' || s === 'templates') return { page: 'templates' }
  if (s === 'instances') return { page: 'instances' }
  if (s === 'missing-credentials') return { page: 'missing-credentials' }
  const [head, ...rest] = s.split('/')
  if (head === 'templates' && rest.length) return { page: 'templates', id: rest.join('/') }
  if (head === 'instances' && rest.length) return { page: 'instances', id: rest.join('/') }
  // Unknown sub-path: fall back to templates rather than 404'ing —
  // the shell's URL might have stale segments from a prior provider.
  return { page: 'templates' }
}

const route = computed<Route>(() => parseSubPath(props.ctx?.subPath))
const tenantPath = computed(() => props.ctx?.tenant ?? null)

// React to ctx changes — basePath drives URL prefixes on fetches,
// token feeds Authorization, both reactively update when the shell
// re-pushes context (e.g. token rotation, workspace switch).
watch(() => props.ctx?.basePath, (v) => setBasePath(v), { immediate: true })
watch(() => props.ctx?.token, (v) => setToken(v), { immediate: true })
// ctx.tenant is the kcp cluster name auth.clusterName, used as the
// /graphql/<cluster> path segment for every gateway call in api.ts.
watch(() => props.ctx?.tenant, (v) => setTenant(v), { immediate: true })

// navigate dispatches a kedge-navigate CustomEvent (bubbles) so the
// shell updates the browser URL. Children call this through the
// emitted 'navigate' event so they don't need to know about the
// custom-event protocol. Path is RELATIVE to the provider root
// ('templates', 'instances/foo', etc.); ProviderFrame.vue prefixes
// with /providers/{name}/.
const rootRef = ref<HTMLElement | null>(null)
function navigate(path: string) {
  const el = rootRef.value
  if (!el) return
  el.dispatchEvent(new CustomEvent('kedge-navigate', { detail: { path }, bubbles: true }))
}

// Bridge legacy navigate('catalog' | 'provision' | 'instances' | 'detail' | 'missing-credentials')
// emits from the existing view components — they were written before URL
// routing existed. Maps each legacy verb to the new path scheme so we
// don't have to edit every child to know about the new contract.
function legacyNavigate(view: string) {
  switch (view) {
    case 'catalog':
    case 'templates':
      navigate('templates')
      break
    case 'instances':
      navigate('instances')
      break
    case 'missing-credentials':
      navigate('missing-credentials')
      break
    // 'provision' / 'detail' are reached by selectTemplate / selectInstance below
    // — they always come with an ID, never as a bare view name.
    default:
      navigate(view)
  }
}

function selectTemplate(name: string) {
  navigate('templates/' + encodeURIComponent(name))
}
function selectInstance(name: string) {
  navigate('instances/' + encodeURIComponent(name))
}
function provisioned(name: string) {
  navigate('instances/' + encodeURIComponent(name))
}
</script>

<template>
  <div ref="rootRef" class="app">
    <!--
      Every routed page calls into api.ts on mount, which queries the
      /graphql/<tenant> gateway. Without a tenant the call
      throws "no workspace selected" — accurate, but ugly. Gate page
      render on a non-empty tenantPath so the page only mounts when
      api.ts is ready. The host pushes ctx.tenant immediately after
      append; the wait is usually a single frame. When the user has
      genuinely no workspace selected, the friendly message below
      stays put until they pick one in the shell's sidebar chip.
    -->
    <template v-if="!tenantPath">
      <section class="page">
        <header class="page-head">
          <div>
            <h2 class="page-title">Templates</h2>
            <p class="page-meta">Pick a template to provision into your tenant scope.</p>
          </div>
        </header>
        <div class="muted">
          Select a workspace from the org/workspace chip in the
          sidebar to view the catalog.
        </div>
      </section>
    </template>
    <template v-else-if="route.page === 'templates' && !route.id">
      <CatalogPage @select="selectTemplate" @navigate="legacyNavigate" />
    </template>
    <template v-else-if="route.page === 'templates' && route.id">
      <ProvisionPage
        :template-name="route.id"
        @navigate="legacyNavigate"
        @provisioned="provisioned"
      />
    </template>
    <template v-else-if="route.page === 'instances' && !route.id">
      <InstanceListPage @navigate="legacyNavigate" @select="selectInstance" />
    </template>
    <template v-else-if="route.page === 'instances' && route.id">
      <InstanceDetailPage :instance-name="route.id" @navigate="legacyNavigate" />
    </template>
    <template v-else-if="route.page === 'missing-credentials'">
      <MissingCredentialsPage :tenant-path="tenantPath" @navigate="legacyNavigate" />
    </template>
  </div>
</template>
