<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { useTenantStore } from '@/stores/tenant'
// The fallback body is a card in the same grid as real tiles, so it renders
// from the same vocabulary rather than approximating it.
import { tileClass } from '@/portalkit/dashboardtile'
import { createProviderLoadGeneration } from '@/providers/providerLoadGeneration'
import {
  canReloadProviderScriptInDocument,
  invalidateProviderScript,
  loadProviderScript,
} from '@/providers/providerScriptLoader'
import { createProviderContext } from '@/providers/providerContext'
import type { ProviderDTO } from '@/stores/providers'
import { CircleAlert, Puzzle, ChevronRight, RefreshCw, X } from 'lucide-vue-next'

// DashboardTile is the portal-side mount point for one provider's
// dashboard summary. Mirrors ProviderFrame.vue's lifecycle but for the
// tile element instead of the full-page element: each provider's
// /main.js may register a second custom element
// <faros-dashboard-tile-{name}>; if it does we mount that here, push
// the same farosContext shape, and proxy faros-navigate events to the
// portal router.
//
// A provider that ships NO tile element is still a first-class tile: the
// card renders its portal chrome (icon, name, Open link) with a muted
// "no summary" body, and stays on the grid. Every enabled provider is a
// persistent, arrangeable card — we no longer drop tileless providers at
// runtime (which flickered on every load); a user who doesn't want a card
// hides it via Customize, and that choice persists.
//
// In `edit-mode` the tile is a draggable/resizable grid cell: it shows a
// remove affordance and disables its own interactive surfaces (the Open
// link and the provider's mounted element) so a drag started anywhere on
// the card isn't swallowed by a click target inside it.

const props = defineProps<{ provider: ProviderDTO; editMode?: boolean }>()
const emit = defineEmits<{
  (e: 'remove', name: string): void
}>()

const auth = useAuthStore()
const theme = useThemeStore()
const tenant = useTenantStore()
const router = useRouter()

const mountRef = ref<HTMLDivElement | null>(null)
const elementRef = ref<HTMLElement | null>(null)
const loadState = ref<'idle' | 'loading' | 'ready' | 'no-tile' | 'error'>('idle')
const loadGeneration = createProviderLoadGeneration()
const canRetryInDocument = computed(() => canReloadProviderScriptInDocument(props.provider.name))

const tagFor = (name: string) => `faros-dashboard-tile-${name}`

// Route the tile's "Open" link and sub-page shortcuts point at. Mirrors the
// side nav's rule (providers.ts): built-in providers route to /{builtinRoute},
// everything else to /providers/{name}, with children hung off that. Used by
// the fallback body so a provider without its own tile element is still a
// useful launcher rather than a blank card.
const parentTo = computed(() =>
  props.provider.builtinRoute ? `/${props.provider.builtinRoute}` : `/providers/${props.provider.name}`,
)
const quickLinks = computed(() =>
  (props.provider.children ?? []).map((c) => ({
    label: c.displayName,
    to: props.provider.builtinRoute ? `/${c.builtinRoute}` : `${parentTo.value}/${c.builtinRoute}`,
  })),
)

watch(
  () => [props.provider.name, props.provider.version, props.provider.ready] as const,
  async ([name, version, ready]) => {
    const generation = loadGeneration.begin()
    clearMountedTile()
    if (!ready) {
      loadState.value = 'idle'
      return
    }
    await loadAndMount(name, version, generation)
  },
  { immediate: true },
)

watch(
  () => [theme.resolved, auth.token, auth.clusterName, tenant.orgUUID, tenant.workspaceUUID] as const,
  () => pushContext(),
)

function isCurrentLoad(generation: number, name: string, version: string | undefined): boolean {
  return loadGeneration.isCurrent(generation) &&
    props.provider.name === name &&
    props.provider.version === version &&
    props.provider.ready
}

function clearMountedTile() {
  mountRef.value?.replaceChildren()
  elementRef.value = null
}

async function loadAndMount(name: string, version: string | undefined, generation: number) {
  if (!isCurrentLoad(generation, name, version)) return
  loadState.value = 'loading'

  const tag = tagFor(name)
  try {
    // Pass the catalog version even when the element is already defined. App
    // Studio uses the bootstrap reload to refresh its lazy-loader registry;
    // page and dashboard callers coalesce through the shared loader.
    await loadProviderScript(name, version, document, undefined, {
      integrity: props.provider.mainJSIntegrity,
    })
  } catch {
    if (isCurrentLoad(generation, name, version)) loadState.value = 'error'
    return
  }
  if (!isCurrentLoad(generation, name, version)) return

  // Registration is synchronous inside the bootstrap, so a missing tag after
  // the shared load resolves means this provider intentionally ships no tile.
  if (!customElements.get(tag)) {
    // No tile element — a normal, opt-in case for most providers. Keep the
    // card (chrome + muted body) rather than dropping it from the grid, but
    // say so once in the console: when a provider DOES ship a tile and this
    // still fires, the fallback card is indistinguishable from the intended
    // empty state and there is nothing else to go on.
    // eslint-disable-next-line no-console
    console.debug(`[faros] provider "${name}" registered no <${tag}> after loading its bundle`)
    loadState.value = 'no-tile'
    return
  }

  await nextTick()
  if (!isCurrentLoad(generation, name, version) || !mountRef.value) return
  mountRef.value.replaceChildren()
  const el = document.createElement(tag) as HTMLElement
  mountRef.value.appendChild(el)
  elementRef.value = el
  pushContext()
  loadState.value = 'ready'
}

function retryLoad() {
  if (!canRetryInDocument.value) {
    window.location.reload()
    return
  }
  const generation = loadGeneration.begin()
  clearMountedTile()
  void loadAndMount(props.provider.name, props.provider.version, generation)
}

function onProviderBootstrapRetry(event: Event) {
  const providerName = (event as CustomEvent<{ providerName?: unknown }>).detail?.providerName
  if (providerName !== props.provider.name || !props.provider.ready) return
  event.preventDefault()
  invalidateProviderScript(props.provider.name, props.provider.version)
  retryLoad()
}

function pushContext() {
  const el = elementRef.value as HTMLElement & { farosContext?: unknown } | null
  if (!el) return
  const providerName = props.provider.name
  // Same shape and same host-owned fetch as ProviderFrame.pushContext; the
  // tile is just a second element from the same bundle.
  el.farosContext = createProviderContext(
    {
      user: auth.user,
      tenant: auth.clusterName,
      // The sidebar's org/workspace, same as ProviderFrame pushes. Without it a
      // provider client that scopes on X-Faros-Org / X-Faros-Workspace queries
      // the wrong workspace (or none) and the tile renders a convincing empty
      // state instead of the user's actual resources.
      orgUUID: tenant.orgUUID,
      workspaceUUID: tenant.workspaceUUID,
      // Resolved, not the raw mode — see ProviderFrame.pushContext.
      theme: theme.resolved,
      basePath: `/ui/providers/${providerName}`,
    },
    {
      providerName,
      scope: () => ({ token: auth.token, orgUUID: tenant.orgUUID, workspaceUUID: tenant.workspaceUUID }),
    },
  )
}

function onNavigate(e: Event) {
  const ce = e as CustomEvent<{ path: string; replace?: boolean }>
  const p = ce.detail?.path
  if (typeof p !== 'string') return
  e.preventDefault()
  const target = `/providers/${props.provider.name}/${p.replace(/^\//, '')}`
  if (ce.detail.replace === true) void router.replace(target)
  else void router.push(target)
}

onMounted(() => {
  mountRef.value?.addEventListener('faros-navigate', onNavigate)
  mountRef.value?.addEventListener('faros-provider-bootstrap-retry', onProviderBootstrapRetry)
})
onBeforeUnmount(() => {
  loadGeneration.invalidate()
  mountRef.value?.removeEventListener('faros-navigate', onNavigate)
  mountRef.value?.removeEventListener('faros-provider-bootstrap-retry', onProviderBootstrapRetry)
  if (elementRef.value && mountRef.value?.contains(elementRef.value)) {
    mountRef.value.removeChild(elementRef.value)
  }
  elementRef.value = null
})
</script>

<template>
  <!-- Every enabled provider is a persistent card, whether or not it ships
       a tile element. A tileless provider shows its chrome (icon, name,
       Open) with a muted "no summary" body — it is not dropped from the
       grid, so the layout is stable and never flickers on load. -->
  <div
    class="relative flex h-full flex-col overflow-hidden rounded-xl border bg-surface-raised/80 p-5 backdrop-blur"
    :class="editMode ? 'cursor-move border-accent/40 ring-1 ring-accent/30' : 'border-border-subtle'"
  >
    <!-- Remove affordance — only in edit mode. `tile-no-drag` keeps the
         click from starting a grid drag (see DashboardPage's GridItem
         drag-ignore-from). -->
    <button
      v-if="editMode"
      type="button"
      class="tile-no-drag k-btn k-btn--ghost absolute right-2 top-2 z-10 flex h-11 w-11 items-center justify-center rounded-md border border-border-subtle bg-surface-overlay p-0 text-text-muted transition-colors hover:border-danger/40 hover:text-danger sm:h-8 sm:w-8"
      :aria-label="`Remove ${provider.displayName} tile`"
      title="Remove tile"
      @click.stop="emit('remove', provider.name)"
    >
      <X class="h-4 w-4" :stroke-width="1.75" />
    </button>

    <!-- Tile header is portal chrome (icon, name, status) so a provider's
         tile body never has to repeat the catalog metadata. -->
    <div class="mb-4 flex items-center gap-3">
      <div class="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-lg border border-border-subtle bg-surface-overlay">
        <img
          v-if="provider.iconURL"
          :src="provider.iconURL"
          alt=""
          class="h-4 w-4 object-contain"
          @error="(e) => ((e.target as HTMLImageElement).style.display = 'none')"
        />
        <Puzzle v-else class="h-3.5 w-3.5 text-text-muted" :stroke-width="1.75" />
      </div>
      <div class="min-w-0 flex-1">
        <div class="truncate text-[13px] font-medium text-text-primary">{{ provider.displayName }}</div>
        <div class="truncate font-mono text-[10px] text-text-muted">{{ provider.name }}</div>
      </div>
      <router-link
        v-if="!editMode"
        :to="parentTo"
        :aria-label="`Open ${provider.displayName}`"
        class="flex items-center gap-0.5 text-[11px] font-medium text-accent transition-colors hover:text-accent-hover"
      >
        Open <ChevronRight class="h-3 w-3" :stroke-width="1.75" />
      </router-link>
    </div>

    <div v-if="loadState === 'loading'" role="status" aria-live="polite" :class="tileClass.message">
      Loading summary&hellip;
    </div>
    <div v-else-if="loadState === 'error'" role="alert" class="flex items-start gap-2 text-[11px] text-text-muted">
      <CircleAlert class="mt-0.5 h-4 w-4 flex-shrink-0 text-danger" :stroke-width="1.75" />
      <div class="min-w-0">
        <p class="font-medium text-text-primary">Summary unavailable</p>
        <p class="mt-1">This provider's dashboard summary could not be loaded.</p>
        <div class="mt-3 flex flex-wrap items-center gap-3">
          <button type="button" class="tile-no-drag k-btn k-btn--ghost h-8 px-2.5 text-[11px]" @click="retryLoad">
            <RefreshCw class="h-3.5 w-3.5" :stroke-width="1.75" />
            {{ canRetryInDocument ? 'Retry' : 'Reload page' }}
          </button>
          <router-link :to="parentTo" class="tile-no-drag font-medium text-accent hover:text-accent-hover">
            Open provider
          </router-link>
        </div>
      </div>
    </div>
    <!-- Provider ships no tile element: instead of a blank card, render a
         generic launcher from catalog metadata — status, category/version,
         and shortcuts into the provider's sub-pages. Pointer events are
         disabled in edit mode so the links don't swallow a grid drag. -->
    <div
      v-else-if="loadState === 'no-tile'"
      class="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto pr-1"
      :class="editMode ? 'pointer-events-none select-none' : ''"
      :inert="editMode"
    >
      <div :class="tileClass.stats">
        <span :class="[tileClass.stat, provider.ready ? tileClass.statOk : tileClass.statWarn]">
          <span class="h-1.5 w-1.5 rounded-full" :class="provider.ready ? 'bg-success' : 'bg-warning'" />
          <span :class="tileClass.statLabel">{{ provider.ready ? 'Ready' : 'Not ready' }}</span>
        </span>
        <span v-if="provider.category" :class="[tileClass.stat, tileClass.statMuted]">
          <span class="uppercase tracking-wide">{{ provider.category }}</span>
        </span>
        <span v-if="provider.version" :class="[tileClass.stat, tileClass.statMuted]">
          <span class="font-mono">v{{ provider.version }}</span>
        </span>
      </div>

      <!-- Sub-page shortcuts when the provider declares nav children. -->
      <div v-if="quickLinks.length" class="flex flex-wrap gap-1.5">
        <router-link
          v-for="l in quickLinks"
          :key="l.to"
          :to="l.to"
          class="tile-no-drag k-btn k-btn--ghost rounded-md border border-border-subtle bg-surface-overlay px-2 py-1 text-[11px] text-text-secondary transition-colors hover:border-accent/40 hover:text-accent"
        >{{ l.label }}</router-link>
      </div>
      <p v-else :class="tileClass.empty">
        No dashboard summary yet — open {{ provider.displayName }} to manage its resources.
      </p>

      <router-link
        :to="parentTo"
        class="tile-no-drag mt-auto inline-flex items-center gap-0.5 text-[11px] font-medium text-accent transition-colors hover:text-accent-hover"
      >
        Open {{ provider.displayName }} <ChevronRight class="h-3 w-3" :stroke-width="1.75" />
      </router-link>
    </div>
    <!-- The provider's tile element mounts here. Always render the mount
         node so the watch can attach to it before the script finishes
         loading; visibility flips through loadState. In edit mode its
         pointer events are disabled so a drag isn't captured by the
         provider's own interactive content. -->
    <div
      ref="mountRef"
      class="min-h-0 flex-1 overflow-auto"
      :class="[loadState === 'ready' ? '' : 'hidden', editMode ? 'pointer-events-none select-none' : '']"
      :inert="editMode"
    />
  </div>
</template>
