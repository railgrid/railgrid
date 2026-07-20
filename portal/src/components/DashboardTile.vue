<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import type { ProviderDTO } from '@/stores/providers'
import { Puzzle, ChevronRight, X } from 'lucide-vue-next'

// DashboardTile is the portal-side mount point for one provider's
// dashboard summary. Mirrors ProviderFrame.vue's lifecycle but for the
// tile element instead of the full-page element: each provider's
// /main.js may register a second custom element
// <kedge-dashboard-tile-{name}>; if it does we mount that here, push
// the same kedgeContext shape, and proxy kedge-navigate events to the
// portal router. If the provider has no tile (the tag never registers
// within the timeout), it emits `no-tile` so the dashboard can drop it
// from the grid — exposing a tile is opt-in per provider.
//
// In `edit-mode` the tile is a draggable/resizable grid cell: it shows a
// remove affordance and disables its own interactive surfaces (the Open
// link and the provider's mounted element) so a drag started anywhere on
// the card isn't swallowed by a click target inside it.

const props = defineProps<{ provider: ProviderDTO; editMode?: boolean }>()
const emit = defineEmits<{
  (e: 'no-tile', name: string): void
  (e: 'remove', name: string): void
}>()

const auth = useAuthStore()
const theme = useThemeStore()
const router = useRouter()

const mountRef = ref<HTMLDivElement | null>(null)
const elementRef = ref<HTMLElement | null>(null)
const loadState = ref<'idle' | 'loading' | 'ready' | 'no-tile' | 'error'>('idle')
const loadError = ref<string | null>(null)

const tagFor = (name: string) => `kedge-dashboard-tile-${name}`

watch(
  () => props.provider,
  async (p) => {
    if (!p.ready) return
    await loadAndMount(p.name, p.version)
  },
  { immediate: true },
)

watch(
  () => [theme.mode, auth.token, auth.clusterName] as const,
  () => pushContext(),
)

async function loadAndMount(name: string, version: string | undefined) {
  loadState.value = 'loading'
  loadError.value = null

  // Reuse ProviderFrame's script id so we don't double-load the bundle
  // when both the tile and the page are visible (e.g. user is on the
  // provider page and the dashboard pre-fetches tiles). customElements
  // is idempotent — second define() is a no-op.
  const scriptID = `kedge-provider-script-${name}`
  const tag = tagFor(name)

  if (!customElements.get(tag) && !document.getElementById(scriptID)) {
    const v = encodeURIComponent(version ?? '0')
    const src = `/ui/providers/${name}/main.js?v=${v}`
    await new Promise<void>((resolve, reject) => {
      const s = document.createElement('script')
      s.id = scriptID
      s.src = src
      s.async = true
      s.onload = () => resolve()
      s.onerror = () => reject(new Error(`failed to load ${src}`))
      document.head.appendChild(s)
    }).catch((e: Error) => {
      loadState.value = 'error'
      loadError.value = e.message
      throw e
    })
  }

  // Race whenDefined against a short timeout. A provider that doesn't
  // ship a tile element is a normal case — fall through to the fallback
  // card rather than treating it as an error.
  const defined = await Promise.race([
    customElements.whenDefined(tag).then(() => true),
    new Promise<boolean>((resolve) => setTimeout(() => resolve(false), 1500)),
  ])

  if (!defined) {
    loadState.value = 'no-tile'
    emit('no-tile', name)
    return
  }

  await nextTick()
  if (!mountRef.value) return
  mountRef.value.replaceChildren()
  const el = document.createElement(tag) as HTMLElement
  mountRef.value.appendChild(el)
  elementRef.value = el
  pushContext()
  loadState.value = 'ready'
}

function pushContext() {
  const el = elementRef.value as HTMLElement & { kedgeContext?: unknown } | null
  if (!el) return
  el.kedgeContext = {
    token: auth.token,
    user: auth.user,
    tenant: auth.clusterName,
    theme: theme.mode,
    basePath: `/ui/providers/${props.provider.name}`,
  }
}

function onNavigate(e: Event) {
  const ce = e as CustomEvent<{ path: string }>
  const p = ce.detail?.path
  if (typeof p !== 'string') return
  router.push(`/providers/${props.provider.name}/${p.replace(/^\//, '')}`)
}

onMounted(() => mountRef.value?.addEventListener('kedge-navigate', onNavigate))
onBeforeUnmount(() => {
  mountRef.value?.removeEventListener('kedge-navigate', onNavigate)
  if (elementRef.value && mountRef.value?.contains(elementRef.value)) {
    mountRef.value.removeChild(elementRef.value)
  }
  elementRef.value = null
})
</script>

<template>
  <!-- A provider with no dashboard tile contributes nothing to the grid:
       skip the whole card (rather than showing an empty "no summary"
       placeholder). The `no-tile` emit also tells the dashboard to drop
       this cell so it leaves no gap in the layout. -->
  <div
    v-if="loadState !== 'no-tile'"
    class="relative flex h-full flex-col overflow-hidden rounded-xl border bg-surface-raised/80 p-5 backdrop-blur"
    :class="editMode ? 'cursor-move border-accent/40 ring-1 ring-accent/30' : 'border-border-subtle'"
  >
    <!-- Remove affordance — only in edit mode. `tile-no-drag` keeps the
         click from starting a grid drag (see DashboardPage's GridItem
         drag-ignore-from). -->
    <button
      v-if="editMode"
      type="button"
      class="tile-no-drag absolute right-2 top-2 z-10 flex h-6 w-6 items-center justify-center rounded-full border border-border-subtle bg-surface-overlay text-text-muted transition-colors hover:border-danger/40 hover:text-danger"
      title="Remove tile"
      @click.stop="emit('remove', provider.name)"
    >
      <X class="h-3.5 w-3.5" :stroke-width="2" />
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
        :to="`/providers/${provider.name}`"
        class="flex items-center gap-0.5 text-[11px] font-medium text-accent transition-colors hover:text-accent-hover"
      >
        Open <ChevronRight class="h-3 w-3" :stroke-width="2" />
      </router-link>
    </div>

    <div v-if="loadState === 'loading'" class="text-[11px] text-text-muted">Loading&hellip;</div>
    <div v-else-if="loadState === 'error'" class="text-[11px] text-danger">
      Failed to load tile: <span class="font-mono">{{ loadError }}</span>
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
    />
  </div>
</template>
