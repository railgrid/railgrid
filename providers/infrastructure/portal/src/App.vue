<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import CatalogPage from './views/CatalogPage.vue'
import ProvisionPage from './views/ProvisionPage.vue'
import InstanceListPage from './views/InstanceListPage.vue'
import InstanceDetailPage from './views/InstanceDetailPage.vue'
import MissingCredentialsPage from './views/MissingCredentialsPage.vue'
import ConfirmDialog from './portalkit/ConfirmDialog.vue'
import { resolveConfirm } from './portalkit/confirm'
import { setBasePath, setHostFetch, setTenant, setToken } from './api'
import { createResourceTombstones } from './refresh'
import { legacyInfrastructurePath, parseInfrastructureSubPath } from './routes'
import type { FarosContext } from './types'
import { useDelayedLoading } from './portalkit/useDelayedLoading'

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
// and pushes <rest> to us via farosContext.subPath. Internal nav
// dispatches a 'faros-navigate' CustomEvent (bubbles up to
// ProviderFrame.vue's listener, which calls router.push), so the
// browser URL stays in sync — refresh, back, forward all land on
// the same page. Previously navigation was tracked in a local ref
// and refresh always snapped back to the catalog.

const props = defineProps<{ ctx: FarosContext | null }>()

const route = computed(() => parseInfrastructureSubPath(props.ctx?.subPath))
const tenantPath = computed(() => props.ctx?.tenant ?? null)
const contextInitialized = computed(() => props.ctx !== null)
const contextPending = computed(() => !contextInitialized.value)
const showContextLoading = useDelayedLoading(contextPending)
const contextVersion = ref(0)
// Route-local pages remount during detail/list navigation, but acknowledged
// deletions must remain marked Deleting until a successful list proves the old
// UID is gone. Keep that state at the active authority boundary instead.
const instanceTombstones = createResourceTombstones()
let instanceTombstoneTenant: string | null | undefined

// React to ctx changes — basePath drives URL prefixes on fetches,
// token feeds Authorization, both reactively update when the shell
// re-pushes context (e.g. token rotation, workspace switch).
watch(
  () => [props.ctx?.basePath, props.ctx?.token, props.ctx?.tenant] as const,
  ([basePath, token, tenant]) => {
    // Keep the existing API setters as the public context boundary. Each
    // setter invalidates in-flight reads, while this owner remounts pages so
    // no route-local state crosses an authority change.
    setBasePath(basePath)
    setHostFetch(props.ctx?.fetch)
    setToken(token)
    setTenant(tenant)
    resolveConfirm(false)
    // A refreshed bearer token is still the same KRM authority. Preserve
    // deletion markers across token rotation, but never across tenants.
    if (instanceTombstoneTenant !== tenant) instanceTombstones.clear()
    instanceTombstoneTenant = tenant
    contextVersion.value += 1
  },
  { immediate: true },
)

// navigate dispatches a faros-navigate CustomEvent (bubbles) so the
// shell updates the browser URL. Children call this through the
// emitted 'navigate' event so they don't need to know about the
// custom-event protocol. Path is RELATIVE to the provider root
// ('templates', 'instances/foo', etc.); ProviderFrame.vue prefixes
// with /providers/{name}/.
const rootRef = ref<HTMLElement | null>(null)
function navigate(path: string) {
  const el = rootRef.value
  if (!el) return
  el.dispatchEvent(new CustomEvent('faros-navigate', { detail: { path }, bubbles: true }))
}

// Bridge legacy navigate('catalog' | 'provision' | 'instances' | 'detail' | 'missing-credentials')
// emits from the existing view components — they were written before URL
// routing existed. Maps each legacy verb to the new path scheme so we
// don't have to edit every child to know about the new contract.
function legacyNavigate(view: string) {
  navigate(legacyInfrastructurePath(view))
}

function selectTemplate(name: string) {
  navigate('templates/' + encodeURIComponent(name))
}
function selectInstance(name: string) {
  navigate('instances/' + encodeURIComponent(name))
}
function provisioned(name: string) {
  selectInstance(name)
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
    <template v-if="!contextInitialized">
      <section
        class="page k-delayed-loading"
        :role="showContextLoading ? 'status' : undefined"
        :aria-live="showContextLoading ? 'polite' : undefined"
        :aria-busy="showContextLoading ? 'true' : undefined"
        :aria-hidden="showContextLoading ? undefined : 'true'"
      >
        <header class="page-head">
          <div>
            <h2 class="page-title">Infrastructure</h2>
            <p class="page-meta">Loading workspace context…</p>
          </div>
        </header>
        <div class="page-loading-shell" aria-hidden="true">
          <div class="shimmer page-loading-line page-loading-line-short" />
          <div class="shimmer page-loading-panel" />
        </div>
      </section>
    </template>
    <template v-else-if="!tenantPath">
      <section class="page">
        <header class="page-head">
          <div>
            <h2 class="page-title">Templates</h2>
            <p class="page-meta">Pick a template to provision into your tenant scope.</p>
          </div>
        </header>
        <div class="muted" role="status">
          Select a workspace from the org/workspace chip in the
          sidebar to view the catalog.
        </div>
      </section>
    </template>
    <template v-else-if="route.page === 'templates' && !route.id">
      <CatalogPage :key="`catalog:${contextVersion}`" @select="selectTemplate" @navigate="legacyNavigate" />
    </template>
    <template v-else-if="route.page === 'templates' && route.id">
      <ProvisionPage
        :key="`provision:${contextVersion}:${route.id}`"
        :template-name="route.id"
        @navigate="legacyNavigate"
        @provisioned="provisioned"
      />
    </template>
    <template v-else-if="route.page === 'instances' && !route.id">
      <InstanceListPage
        :key="`instances:${contextVersion}`"
        :tombstones="instanceTombstones"
        @navigate="legacyNavigate"
        @select="selectInstance"
      />
    </template>
    <template v-else-if="route.page === 'instances' && route.id">
      <InstanceDetailPage
        :key="`instance:${contextVersion}:${route.id}`"
        :instance-name="route.id"
        :tombstones="instanceTombstones"
        @navigate="legacyNavigate"
      />
    </template>
    <template v-else-if="route.page === 'missing-credentials'">
      <MissingCredentialsPage :key="`credentials:${contextVersion}`" :tenant-path="tenantPath" @navigate="legacyNavigate" />
    </template>
    <ConfirmDialog />
  </div>
</template>
