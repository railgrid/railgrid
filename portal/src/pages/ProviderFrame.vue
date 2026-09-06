<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import { useProvidersStore } from '@/stores/providers'
import { useAuthStore } from '@/stores/auth'
import { useTenantStore } from '@/stores/tenant'
import { useThemeStore } from '@/stores/theme'
import {
  canReloadProviderScriptInDocument,
  invalidateProviderScript,
  loadProviderScript,
} from '@/providers/providerScriptLoader'
import { createProviderContext } from '@/providers/providerContext'
import { AlertCircle, Puzzle } from 'lucide-vue-next'
import { useDelayedLoading } from '@/portalkit/useDelayedLoading'

// Micro-frontend mount: instead of dropping an iframe, we load the
// provider's /main.js (which defines a custom element faros-provider-{name})
// and render that element directly in the portal's DOM tree. The provider
// shares our stylesheet — CSS variables from :root cascade in — so there's
// no visible boundary, no scrollbars, and no postMessage shuttle.
//
// Trust statement: a provider bundle therefore executes as fully trusted code
// in this document. Two things bound what a bundle gets by default: the
// script is pinned with the SRI hash the hub computed at registration
// (catalog mainJSIntegrity), and the bundle talks to the hub through the
// host-owned `farosContext.fetch` (providerFetch.ts) rather than holding the
// user's raw id token. A sandboxed iframe with a postMessage bridge is the
// larger follow-up for untrusted third-party providers.

const props = defineProps<{ providerName: string; subPath: string }>()

const providers = useProvidersStore()
const auth = useAuthStore()
const tenant = useTenantStore()
const theme = useThemeStore()
const router = useRouter()

// Mount point the custom element is appended into.
const mountRef = ref<HTMLDivElement | null>(null)
// The custom element instance (or null while not yet defined / mounted).
const elementRef = ref<HTMLElement | null>(null)
// Loading state covers script fetch + customElements.whenDefined.
const loadState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const loadError = ref<string | null>(null)
const providerLoadPending = computed(() => loadState.value === 'loading')
const showProviderLoading = useDelayedLoading(providerLoadPending)
const providerFullBleedOverride = ref<boolean | null>(null)
// Every async bundle load and mount is fenced by this generation. A workspace
// switch can make access disappear while a script is still in flight; that
// completion must not resurrect an element for the old context.
let mountGeneration = 0
let boundMount: HTMLDivElement | null = null

// Each provider's tag is faros-provider-<name>. The hyphen requirement
// of custom element names matches naturally because provider names are
// already kebab-case in the catalog.
const tagFor = (name: string) => `faros-provider-${name}`

const APP_STUDIO_CREATE_ROUTE = '~new'
const APP_STUDIO_MODELS_ROUTE = '~models'
const APP_STUDIO_CREATE_MODEL_ROUTE = 'create/model'

onMounted(() => {
  if (!providers.loaded || providers.catalogOrgUUID !== tenant.orgUUID) {
    providers.load(tenant.orgUUID)
  }
})

const entry = computed(() => providers.byName(props.providerName))
const providerReadinessLabel = computed(() => {
  if (entry.value?.ready) return 'Ready'
  switch (entry.value?.readinessReason) {
    case 'BackendUnhealthy': return 'Degraded'
    case 'HeartbeatStale': return 'Disconnected'
    case 'InvalidEndpoint': return 'Not ready'
    default: return 'Starting'
  }
})
const providerReadinessClass = computed(() => {
  if (entry.value?.ready) return 'border border-success/30 bg-success-subtle text-success'
  if (entry.value?.readinessReason === 'HeartbeatStale') return 'border border-danger/30 bg-danger-subtle text-danger'
  if (entry.value?.readinessReason === 'BackendUnhealthy') return 'border border-warning/30 bg-warning-subtle text-warning'
  return 'border border-border-default bg-surface-overlay text-text-muted'
})
const catalogSettled = computed(() =>
  providers.loaded && !providers.loading && !providers.error && providers.catalogOrgUUID === tenant.orgUUID,
)
const catalogError = computed(() => providers.error)
const catalogLoading = computed(() => !catalogError.value && !catalogSettled.value)
const catalogMissing = computed(() => catalogSettled.value && !entry.value)
const providerRoutePath = computed(() => props.subPath.split('/').filter(Boolean).join('/'))
const isAppStudioLandingRoute = computed(() =>
  props.providerName === 'app-studio' &&
  ['', APP_STUDIO_CREATE_ROUTE, APP_STUDIO_MODELS_ROUTE, APP_STUDIO_CREATE_MODEL_ROUTE].includes(providerRoutePath.value),
)
const isAppStudioWorkspaceRoute = computed(() => props.providerName === 'app-studio' && !isAppStudioLandingRoute.value)
const isFullBleedProvider = computed(() =>
  props.providerName === 'app-studio' &&
  (!isAppStudioLandingRoute.value || providerFullBleedOverride.value === true),
)

const isBuiltinProvider = computed(() => {
  const p = entry.value
  return !!p && (!!p.builtin || !!p.builtinRoute)
})

// Only providers that publish an APIExport need a workspace APIBinding. A
// builtin provider is shipped with the portal and remains usable without one.
const requiresBinding = computed(() => {
  const p = entry.value
  return !!p && p.hasUI && !isBuiltinProvider.value && !!(p.apiExportName || p.apiExportPath)
})

const bindingRequestCurrent = computed(() =>
  !!tenant.orgUUID &&
  !!tenant.workspaceUUID &&
  providers.bindingsRequestOrgUUID === tenant.orgUUID &&
  providers.bindingsRequestWorkspaceUUID === tenant.workspaceUUID,
)
const bindingProjectionCurrent = computed(() =>
  !!tenant.orgUUID &&
  !!tenant.workspaceUUID &&
  providers.bindingsOrgUUID === tenant.orgUUID &&
  providers.bindingsWorkspace === tenant.workspaceUUID,
)
const bindingsAuthoritative = computed(() =>
  requiresBinding.value &&
  providers.bindingsLoadState === 'ready' &&
  bindingProjectionCurrent.value,
)
const bindingErrorCurrent = computed(() =>
  requiresBinding.value &&
  providers.bindingsLoadState === 'error' &&
  bindingRequestCurrent.value,
)
const bindingNoWorkspace = computed(() =>
  requiresBinding.value && (!tenant.orgUUID || !tenant.workspaceUUID),
)
const bindingChecking = computed(() =>
  requiresBinding.value &&
  !bindingNoWorkspace.value &&
  !bindingErrorCurrent.value &&
  !bindingsAuthoritative.value,
)
const providerBound = computed(() =>
  !!entry.value && providers.isEnabled(entry.value.name),
)
const canRetryProviderBundleInDocument = computed(() =>
  !!entry.value && canReloadProviderScriptInDocument(entry.value.name),
)
const bindingMissing = computed(() =>
  requiresBinding.value &&
  !bindingNoWorkspace.value &&
  bindingsAuthoritative.value &&
  !providerBound.value,
)
const accessAllowed = computed(() =>
  catalogSettled.value &&
  !!entry.value &&
  !!entry.value.hasUI &&
  !!entry.value.ready &&
  (!requiresBinding.value || (bindingsAuthoritative.value && providerBound.value)),
)

watch(
  () => [props.providerName, providerRoutePath.value] as const,
  () => { providerFullBleedOverride.value = null },
  { flush: 'sync' },
)

// The binding projection is workspace-scoped. Start it when a settled
// catalog exposes an APIExport provider, but do not retry an authoritative
// error automatically; the user gets a finite error state and an explicit
// Retry action instead.
const bindingRefreshNeeded = computed(() =>
  requiresBinding.value &&
  catalogSettled.value &&
  !!tenant.orgUUID &&
  !!tenant.workspaceUUID &&
  !bindingsAuthoritative.value &&
  !bindingErrorCurrent.value,
)

watch(
  bindingRefreshNeeded,
  (needed) => {
    if (needed) void providers.refreshBindings().catch(() => {})
  },
  { immediate: true, flush: 'post' },
)

// Mount only after both catalog and workspace access are settled. This also
// handles provider/org/workspace changes: an access transition first detaches
// the old element, then a fresh bundle instance is created for the new scope.
// mountRef is part of the watched identity because AppLayout temporarily
// suppresses its slot while a persisted workspace hydrates on hard refresh.
// Starting the loader before that outlet exists would let the script finish
// with nowhere to mount and leave loadState stuck at "loading" forever.
watch(
  () => [
    entry.value?.name,
    entry.value?.version,
    entry.value?.ready,
    catalogSettled.value,
    accessAllowed.value,
    mountRef.value,
  ] as const,
  async ([name, version, ready, settled, allowed, mount]) => {
    clearMountedElement()
    if (!name || !ready || !settled || !allowed || !mount) return
    await loadAndMount(name, version, mount)
  },
  { immediate: true, flush: 'post' },
)

// Theme / token / sub-route changes → push fresh context to the mounted
// element via the property setter. The element's setter recomputes
// subPath from window.location and re-syncs its internal router, so a
// portal-side nav (clicking a child like "Workloads") actually reaches
// the micro-frontend. Without props.subPath in the dep list the element
// stayed on its initial route until a hard refresh.
watch(
  () => [theme.resolved, auth.token, auth.clusterName, tenant.orgUUID, tenant.workspaceUUID, props.subPath] as const,
  () => pushContext(),
)

// Workspace/org switch: AppLayout keys its slot wrapper on auth.clusterName,
// so the <div ref="mountRef" /> below is torn down and a new empty div is
// mounted. The custom element that lived in the old div detaches from
// DOM, its disconnectedCallback fires, and its Vue app + Pinia tear down
// cleanly — but ProviderFrame itself stays mounted (it's the route
// component above the slot), so elementRef still points at the orphan
// and loadAndMount is never re-invoked. Symptom: switch workspace,
// provider page reads as a blank panel instead of the new context's
// list.
//
// flush: 'post' runs the effect after AppLayout's slot has finished
// re-rendering, so mountRef.value already points at the new (empty) div
// when we append the fresh element.
watch(
  () => auth.clusterName,
  async () => {
    // Only an already-mounted bundle needs the transport remount. During the
    // initial load, invalidating the generation here would abandon the real
    // loader and leave the frame stuck in its loading state.
    if (loadState.value !== 'ready' || !accessAllowed.value || !entry.value?.ready) return
    const generation = mountGeneration
    const name = entry.value.name
    await nextTick()
    if (
      generation !== mountGeneration ||
      loadState.value !== 'ready' ||
      !accessAllowed.value ||
      entry.value?.name !== name
    ) return
    mountElement(name)
  },
  { flush: 'post' },
)

function isCurrentMount(generation: number, name: string): boolean {
  return generation === mountGeneration &&
    accessAllowed.value &&
    entry.value?.name === name
}

function bindMountEvents() {
  const mount = mountRef.value
  if (!mount || boundMount === mount) return
  boundMount?.removeEventListener('faros-navigate', onNavigate)
  boundMount?.removeEventListener('faros-layout-change', onLayoutChange)
  boundMount?.removeEventListener('faros-provider-bootstrap-retry', onProviderBootstrapRetry)
  boundMount = mount
  boundMount.addEventListener('faros-navigate', onNavigate)
  boundMount.addEventListener('faros-layout-change', onLayoutChange)
  boundMount.addEventListener('faros-provider-bootstrap-retry', onProviderBootstrapRetry)
}

// Detach both the live element and any listeners attached to its mount point.
// The explicit cleanup matters when access is revoked while the route
// component itself remains mounted (for example, during a workspace switch).
function clearMountedElement() {
  mountGeneration++
  boundMount?.removeEventListener('faros-navigate', onNavigate)
  boundMount?.removeEventListener('faros-layout-change', onLayoutChange)
  boundMount?.removeEventListener('faros-provider-bootstrap-retry', onProviderBootstrapRetry)
  boundMount = null
  const element = elementRef.value
  if (element?.parentNode) element.parentNode.removeChild(element)
  mountRef.value?.replaceChildren()
  elementRef.value = null
  loadState.value = 'idle'
  loadError.value = null
}

// mountElement creates a fresh custom-element instance and appends it into
// the current access-approved mount point. Without an explicit generation it
// is a deliberate remount for a transport/context change.
function mountElement(name: string, generation?: number): boolean {
  const currentGeneration = generation ?? ++mountGeneration
  if (!mountRef.value || !isCurrentMount(currentGeneration, name)) return false
  mountRef.value.replaceChildren()
  const el = document.createElement(tagFor(name)) as HTMLElement
  mountRef.value.appendChild(el)
  elementRef.value = el
  bindMountEvents()
  pushContext()
  return true
}

async function loadAndMount(name: string, version: string | undefined, mount: HTMLDivElement) {
  const generation = ++mountGeneration
  loadState.value = 'loading'
  loadError.value = null

  // Wait until the element is defined. customElements.whenDefined resolves
  // immediately if the tag is already registered (re-mount on nav).
  const tag = tagFor(name)
  const ready = customElements.whenDefined(tag)

  try {
    await loadProviderScript(name, version, document, undefined, {
      integrity: entry.value?.mainJSIntegrity,
    })

    // 5s timeout so a script that loaded but never called customElements.define
    // doesn't hang the loader forever.
    await Promise.race([
      ready,
      new Promise<never>((_, reject) =>
        setTimeout(() => reject(new Error(`${tag} not defined within 5s`)), 5000),
      ),
    ])
  } catch (e: unknown) {
    if (isCurrentMount(generation, name)) {
      // A downloaded script can still fail before defining its element.
      // Generation-aware providers may clear that attempt for in-document
      // retry; direct-registration providers keep it terminal and offer a page
      // reload because a detached classic body may still execute late.
      invalidateProviderScript(name, version)
      loadState.value = 'error'
      loadError.value = e instanceof Error ? e.message : String(e)
      mountRef.value?.replaceChildren()
      elementRef.value = null
    }
    return
  }

  if (!isCurrentMount(generation, name) || mountRef.value !== mount) return
  // The access-approved div is created by the same render that flips
  // accessAllowed. Wait one tick so the ref points at that fresh node.
  await nextTick()
  if (
    !isCurrentMount(generation, name) ||
    mountRef.value !== mount ||
    !mountElement(name, generation)
  ) return
  loadState.value = 'ready'
}

function pushContext() {
  const el = elementRef.value as HTMLElement & { farosContext?: unknown } | null
  if (!el || !entry.value || !accessAllowed.value) return
  const providerName = entry.value.name
  el.farosContext = createProviderContext(
    {
      // subPath is what the shell's vue-router parsed from
      // /providers/{name}/<rest> — empty for the bare provider URL,
      // 'instances' for /providers/{name}/instances, etc. Providers
      // use this to drive their own page-routing without taking a
      // dependency on the shell's router. Watch on props.subPath
      // upstream guarantees this object is re-pushed when the URL
      // changes (browser back / forward / refresh).
      subPath: props.subPath,
      user: auth.user,
      tenant: auth.clusterName,
      // Sidebar-selected org/workspace. The host fetch forwards these as
      // X-Faros-Org / X-Faros-Workspace so the hub's tenant resolver can
      // inject X-Faros-Tenant (the backend proxy honours the same headers
      // the console's own /api/orgs/* calls send). They are also exposed so
      // a provider can key its own caches on the active scope.
      orgUUID: tenant.orgUUID,
      workspaceUUID: tenant.workspaceUUID,
      // The RESOLVED theme, never the raw mode. Providers render with it
      // (`ctx.theme === 'dark'`), and 'system' is not something to render — a
      // provider handed it renders its light branch on a dark desktop.
      theme: theme.resolved,
      basePath: `/ui/providers/${providerName}`,
    },
    {
      providerName,
      // Read lazily per request so token rotation and a workspace switch
      // apply without waiting for the next context push.
      scope: () => ({ token: auth.token, orgUUID: tenant.orgUUID, workspaceUUID: tenant.workspaceUUID }),
    },
  )
}

// Bubble faros-navigate CustomEvents up into Vue Router.
function onNavigate(e: Event) {
  const ce = e as CustomEvent<{ path: string; replace?: boolean }>
  const p = ce.detail?.path
  if (typeof p !== 'string' || !entry.value) return
  // A cancelable provider event uses preventDefault as a synchronous
  // acknowledgement that the host router owns this history transition.
  // Providers can otherwise fall back to standalone hash routing.
  e.preventDefault()
  const target = `/providers/${entry.value.name}/${p.replace(/^\//, '')}`
  if (ce.detail.replace === true) void router.replace(target)
  else void router.push(target)
}

function onLayoutChange(e: Event) {
  if (props.providerName !== 'app-studio') return
  const fullBleed = (e as CustomEvent<{ fullBleed?: unknown }>).detail?.fullBleed
  if (typeof fullBleed === 'boolean') providerFullBleedOverride.value = fullBleed
}

function retryCatalog() {
  void providers.load(tenant.orgUUID).catch(() => {})
}

function retryBindings() {
  void providers.refreshBindings().catch(() => {})
}

function retryProviderBundle() {
  const provider = entry.value
  const mount = mountRef.value
  if (!provider || !mount || !accessAllowed.value) return
  if (!canReloadProviderScriptInDocument(provider.name)) {
    window.location.reload()
    return
  }
  invalidateProviderScript(provider.name, provider.version)
  void loadAndMount(provider.name, provider.version, mount)
}

function onProviderBootstrapRetry(event: Event) {
  const providerName = (event as CustomEvent<{ providerName?: unknown }>).detail?.providerName
  if (providerName !== entry.value?.name || !accessAllowed.value) return
  // preventDefault is the acknowledgement in this cross-bundle contract. It
  // tells the retained wrapper not to retry its stale lazy-loader closure.
  event.preventDefault()
  retryProviderBundle()
}

onMounted(() => {
  bindMountEvents()
})
onBeforeUnmount(() => {
  // Leave the script + custom element class registered — re-visits are
  // free and the registry can't be unregistered anyway. Just detach.
  clearMountedElement()
})
</script>

<template>
  <AppLayout :full-bleed="isFullBleedProvider">
    <div class="flex h-full min-h-0 min-w-0 flex-col">
      <!-- Portal chrome. Lives outside the provider's own element so the
           name/version/status come from the catalog, not the provider. -->
      <header v-if="catalogSettled && entry && !isFullBleedProvider" class="mb-4 flex flex-wrap items-center gap-3">
        <div class="flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-lg border border-border-subtle bg-surface-raised">
          <img
            v-if="entry.iconURL"
            :src="entry.iconURL"
            alt=""
            class="h-5 w-5 object-contain"
            @error="(e) => ((e.target as HTMLImageElement).style.display = 'none')"
          />
          <Puzzle v-else class="h-4 w-4 text-text-muted" :stroke-width="1.75" />
        </div>
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2">
            <h1 class="truncate text-base font-semibold text-text-primary">
              {{ entry.displayName }}
            </h1>
            <span
              class="rounded-sm px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider"
              :class="providerReadinessClass"
            >
              {{ providerReadinessLabel }}
            </span>
          </div>
          <p class="mt-0.5 truncate font-mono text-[10px] text-text-muted">
            {{ entry.name }}<span v-if="entry.version"> · {{ entry.version }}</span>
          </p>
        </div>
      </header>

      <div
        v-if="catalogError"
        class="flex items-start gap-2 rounded-lg border border-danger/30 bg-danger-subtle p-4 text-sm text-danger"
      >
        <AlertCircle class="mt-0.5 h-4 w-4 shrink-0" :stroke-width="1.75" />
        <div class="min-w-0">
          <div class="font-medium">Failed to load provider catalog</div>
          <div class="mt-1 text-xs font-mono">{{ catalogError }}</div>
          <button type="button" class="k-btn k-btn--text mt-3 text-[11px]" @click="retryCatalog">
            Retry
          </button>
        </div>
      </div>
      <div v-else-if="catalogLoading" class="rounded-lg border border-border-subtle bg-surface-raised/60 p-4 text-sm text-text-muted" role="status" aria-live="polite" aria-busy="true">
        Loading provider catalog&hellip;
      </div>
      <div v-else-if="catalogMissing" class="rounded-lg border border-border-subtle bg-surface-raised/60 p-4 text-sm text-text-muted">
        Provider <code class="font-mono text-text-secondary">{{ props.providerName }}</code> is not available in this catalog.
      </div>
      <div
        v-else-if="entry && !entry.ready"
        class="flex items-start gap-2 rounded-lg border border-border-subtle bg-surface-raised/60 p-4 text-sm text-text-muted"
      >
        <AlertCircle class="h-4 w-4 mt-0.5 text-text-muted" :stroke-width="1.75" />
        <div>
          <div class="font-medium text-text-secondary">Provider {{ providerReadinessLabel.toLowerCase() }}</div>
          <div class="mt-1 text-xs">
            {{ entry.readinessMessage || `Waiting for ${entry.name} to report ready.` }}
          </div>
        </div>
      </div>
      <div v-else-if="entry && !entry.hasUI" class="rounded-lg border border-border-subtle bg-surface-raised/60 p-4 text-sm text-text-muted">
        This provider does not publish a portal UI.
      </div>
      <div
        v-else-if="bindingErrorCurrent"
        class="flex items-start gap-2 rounded-lg border border-danger/30 bg-danger-subtle p-4 text-sm text-danger"
      >
        <AlertCircle class="mt-0.5 h-4 w-4 shrink-0" :stroke-width="1.75" />
        <div class="min-w-0">
          <div class="font-medium">Could not check provider access</div>
          <div class="mt-1 text-xs font-mono">{{ providers.bindingsError }}</div>
          <button type="button" class="k-btn k-btn--text mt-3 text-[11px]" @click="retryBindings">
            Retry
          </button>
        </div>
      </div>
      <div
        v-else-if="bindingNoWorkspace"
        class="flex items-start gap-2 rounded-lg border border-border-subtle bg-surface-raised/60 p-4 text-sm text-text-muted"
      >
        <AlertCircle class="mt-0.5 h-4 w-4 shrink-0" :stroke-width="1.75" />
        <div>
          <div class="font-medium text-text-secondary">Select a workspace to use this provider</div>
          <div class="mt-1 text-xs">Provider access is enabled separately in each workspace.</div>
          <div class="mt-3 flex flex-wrap gap-2">
            <router-link :to="{ name: 'dashboard' }" class="k-btn k-btn--ghost text-[11px]">Dashboard</router-link>
            <router-link to="/providers" class="k-btn k-btn--ghost text-[11px]">Providers</router-link>
          </div>
        </div>
      </div>
      <div
        v-else-if="bindingChecking"
        class="rounded-lg border border-border-subtle bg-surface-raised/60 p-4 text-sm text-text-muted"
        role="status"
        aria-live="polite"
        aria-busy="true"
      >
        Checking provider access&hellip;
      </div>
      <div
        v-else-if="bindingMissing"
        class="flex items-start gap-2 rounded-lg border border-border-subtle bg-surface-raised/60 p-4 text-sm text-text-muted"
      >
        <AlertCircle class="mt-0.5 h-4 w-4 shrink-0" :stroke-width="1.75" />
        <div>
          <div class="font-medium text-text-secondary">Provider not enabled in this workspace</div>
          <div class="mt-1 text-xs">Enable this provider from the catalog to use it here.</div>
          <div class="mt-3 flex flex-wrap gap-2">
            <router-link :to="{ name: 'dashboard' }" class="k-btn k-btn--ghost text-[11px]">Dashboard</router-link>
            <router-link to="/providers" class="k-btn k-btn--ghost text-[11px]">Providers</router-link>
          </div>
        </div>
      </div>
      <div
        v-else-if="loadState === 'loading' && showProviderLoading && isAppStudioWorkspaceRoute"
        class="grid min-h-0 flex-1 grid-cols-1 overflow-hidden rounded-lg border border-border-subtle bg-border-subtle md:grid-cols-[minmax(12rem,18rem)_minmax(0,2fr)_minmax(16rem,28rem)]"
        role="status"
        aria-live="polite"
        aria-busy="true"
        aria-label="Loading App Studio workspace"
      >
        <span class="sr-only">Loading App Studio workspace</span>
        <div class="hidden content-start gap-3 bg-surface-raised p-4 md:grid" aria-hidden="true">
          <div class="shimmer h-4 w-2/3 rounded bg-surface-overlay" />
          <div v-for="i in 4" :key="`thread-${i}`" class="shimmer h-9 rounded bg-surface-overlay" />
        </div>
        <div class="grid content-start gap-3 bg-surface p-4" aria-hidden="true">
          <div class="shimmer h-4 w-1/3 rounded bg-surface-overlay" />
          <div v-for="i in 5" :key="`message-${i}`" class="shimmer h-16 rounded bg-surface-overlay" />
        </div>
        <div class="hidden content-start gap-3 bg-surface-raised p-4 md:grid" aria-hidden="true">
          <div class="shimmer h-4 w-1/2 rounded bg-surface-overlay" />
          <div v-for="i in 3" :key="`workbench-${i}`" class="shimmer h-12 rounded bg-surface-overlay" />
        </div>
      </div>
      <div
        v-else-if="loadState === 'loading' && showProviderLoading"
        class="rounded-lg border border-border-subtle bg-surface-raised/60 p-4 text-sm text-text-muted"
        role="status"
        aria-live="polite"
        aria-busy="true"
      >
        Loading provider&hellip;
      </div>
      <div
        v-else-if="loadState === 'error'"
        class="flex items-start gap-2 rounded-lg border border-danger/30 bg-danger-subtle p-4 text-sm text-danger"
      >
        <AlertCircle class="h-4 w-4 mt-0.5" :stroke-width="1.75" />
        <div>
          <div class="font-medium">Failed to load provider bundle</div>
          <div class="mt-1 text-xs font-mono">{{ loadError }}</div>
          <button type="button" class="k-btn k-btn--text mt-3 text-[11px]" @click="retryProviderBundle">
            {{ canRetryProviderBundleInDocument ? 'Retry' : 'Reload page' }}
          </button>
        </div>
      </div>

      <!-- The provider's custom element mounts here. No iframe, no border,
           no scrollbars; it's just DOM in the portal's tree. -->
      <div
        v-if="accessAllowed"
        ref="mountRef"
        class="min-h-0 min-w-0 flex-1"
      />
    </div>
  </AppLayout>
</template>
