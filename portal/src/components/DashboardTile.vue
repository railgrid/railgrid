<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import type { ProviderDTO } from '@/stores/providers'
import { Puzzle, ChevronRight } from 'lucide-vue-next'

// DashboardTile is the portal-side mount point for one provider's
// dashboard summary. Mirrors ProviderFrame.vue's lifecycle but for the
// tile element instead of the full-page element: each provider's
// /main.js may register a second custom element
// <kedge-dashboard-tile-{name}>; if it does we mount that here, push
// the same kedgeContext shape, and proxy kedge-navigate events to the
// portal router. If the provider has no tile (the tag never registers
// within the timeout), we fall back to a static card linking to the
// provider's page — so adding a tile is opt-in per provider.

const props = defineProps<{ provider: ProviderDTO }>()

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
  <div class="rounded-2xl border border-border-subtle bg-surface-raised/80 p-5 backdrop-blur">
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
    <div v-else-if="loadState === 'no-tile'" class="text-[11px] text-text-muted">
      This provider doesn't expose a dashboard summary.
    </div>
    <!-- The provider's tile element mounts here. Always render the mount
         node so the watch can attach to it before the script finishes
         loading; visibility flips via the v-show above through loadState. -->
    <div ref="mountRef" :class="loadState === 'ready' ? '' : 'hidden'" />
  </div>
</template>
