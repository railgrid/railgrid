<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import { useProvidersStore } from '@/stores/providers'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { AlertCircle, Puzzle } from 'lucide-vue-next'

// Micro-frontend mount: instead of dropping an iframe, we load the
// provider's /main.js (which defines a custom element kedge-provider-{name})
// and render that element directly in the portal's DOM tree. The provider
// shares our stylesheet — CSS variables from :root cascade in — so there's
// no visible boundary, no scrollbars, and no postMessage shuttle.

const props = defineProps<{ providerName: string; subPath: string }>()

const providers = useProvidersStore()
const auth = useAuthStore()
const theme = useThemeStore()
const router = useRouter()

// Mount point the custom element is appended into.
const mountRef = ref<HTMLDivElement | null>(null)
// The custom element instance (or null while not yet defined / mounted).
const elementRef = ref<HTMLElement | null>(null)
// Loading state covers script fetch + customElements.whenDefined.
const loadState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const loadError = ref<string | null>(null)

// Each provider's tag is kedge-provider-<name>. The hyphen requirement
// of custom element names matches naturally because provider names are
// already kebab-case in the catalog.
const tagFor = (name: string) => `kedge-provider-${name}`

onMounted(() => {
  if (!providers.loaded) providers.load()
})

const entry = computed(() => providers.byName(props.providerName))
const isFullBleedProvider = computed(() => props.providerName === 'app-studio')

// On entry resolve OR provider switch, (re)load the script and mount.
watch(
  () => entry.value,
  async (e) => {
    if (!e || !e.ready) return
    await loadAndMount(e.name, e.version)
  },
  { immediate: true },
)

// Theme / token / sub-route changes → push fresh context to the mounted
// element via the property setter. The element's setter recomputes
// subPath from window.location and re-syncs its internal router, so a
// portal-side nav (clicking a child like "Workloads") actually reaches
// the micro-frontend. Without props.subPath in the dep list the element
// stayed on its initial route until a hard refresh.
watch(
  () => [theme.mode, auth.token, auth.clusterName, props.subPath] as const,
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
    if (!entry.value?.ready) return
    await mountElement(entry.value.name)
  },
  { flush: 'post' },
)

// mountElement creates a fresh custom-element instance and appends it
// into the current mountRef div. Split out from loadAndMount so the
// workspace-switch watch above can re-mount without re-fetching the
// provider's main.js script.
async function mountElement(name: string) {
  if (!mountRef.value) return
  mountRef.value.replaceChildren()
  const el = document.createElement(tagFor(name)) as HTMLElement
  mountRef.value.appendChild(el)
  elementRef.value = el
  pushContext()
}

async function loadAndMount(name: string, version: string | undefined) {
  loadState.value = 'loading'
  loadError.value = null

  const scriptID = `kedge-provider-script-${name}`
  const v = encodeURIComponent(version ?? '0')
  const src = `/ui/providers/${name}/main.js?v=${v}`

  // Replace any prior script tag for this provider so version bumps land.
  document.getElementById(scriptID)?.remove()

  // Wait until the element is defined. customElements.whenDefined resolves
  // immediately if the tag is already registered (re-mount on nav).
  const tag = tagFor(name)
  const ready = customElements.whenDefined(tag)

  // Load the script.
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

  // 5s timeout so a script that loaded but never called customElements.define
  // doesn't hang the loader forever.
  await Promise.race([
    ready,
    new Promise<never>((_, reject) =>
      setTimeout(() => reject(new Error(`${tag} not defined within 5s`)), 5000),
    ),
  ]).catch((e: Error) => {
    loadState.value = 'error'
    loadError.value = e.message
    throw e
  })

  // DOM is ready; create + mount the element.
  await nextTick()
  await mountElement(name)
  loadState.value = 'ready'
}

function pushContext() {
  const el = elementRef.value as HTMLElement & { kedgeContext?: unknown } | null
  if (!el || !entry.value) return
  el.kedgeContext = {
    // subPath is what the shell's vue-router parsed from
    // /providers/{name}/<rest> — empty for the bare provider URL,
    // 'instances' for /providers/{name}/instances, etc. Providers
    // use this to drive their own page-routing without taking a
    // dependency on the shell's router. Watch on props.subPath
    // upstream guarantees this object is re-pushed when the URL
    // changes (browser back / forward / refresh).
    subPath: props.subPath,
    token: auth.token,
    user: auth.user,
    tenant: auth.clusterName,
    theme: theme.mode,
    basePath: `/ui/providers/${entry.value.name}`,
  }
}

// Bubble kedge-navigate CustomEvents up into Vue Router.
function onNavigate(e: Event) {
  const ce = e as CustomEvent<{ path: string }>
  const p = ce.detail?.path
  if (typeof p !== 'string' || !entry.value) return
  router.push(`/providers/${entry.value.name}/${p.replace(/^\//, '')}`)
}

onMounted(() => mountRef.value?.addEventListener('kedge-navigate', onNavigate))
onBeforeUnmount(() => {
  mountRef.value?.removeEventListener('kedge-navigate', onNavigate)
  // Leave the script + custom element class registered — re-visits are
  // free and the registry can't be unregistered anyway. Just detach.
  if (elementRef.value && mountRef.value?.contains(elementRef.value)) {
    mountRef.value.removeChild(elementRef.value)
  }
  elementRef.value = null
})
</script>

<template>
  <AppLayout :full-bleed="isFullBleedProvider">
    <div class="flex h-full min-h-0 flex-col">
      <!-- Portal chrome. Lives outside the provider's own element so the
           name/version/status come from the catalog, not the provider. -->
      <header v-if="entry && !isFullBleedProvider" class="mb-4 flex flex-wrap items-center gap-3">
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
              class="rounded-full px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider"
              :class="entry.ready
                ? 'border border-success/30 bg-success-subtle text-success'
                : 'border border-border-default bg-surface-overlay text-text-muted'"
            >
              {{ entry.ready ? 'Ready' : 'Pending' }}
            </span>
          </div>
          <p class="mt-0.5 truncate font-mono text-[10px] text-text-muted">
            {{ entry.name }}<span v-if="entry.version"> · {{ entry.version }}</span>
          </p>
        </div>
      </header>

      <div v-if="!entry" class="rounded-lg border border-border-subtle bg-surface-raised/60 p-4 text-sm text-text-muted">
        Loading provider&hellip;
      </div>
      <div
        v-else-if="!entry.ready"
        class="flex items-start gap-2 rounded-lg border border-border-subtle bg-surface-raised/60 p-4 text-sm text-text-muted"
      >
        <AlertCircle class="h-4 w-4 mt-0.5 text-text-muted" :stroke-width="2" />
        <div>
          <div class="font-medium text-text-secondary">Provider not ready</div>
          <div class="mt-1 text-xs">
            Waiting for <code class="text-text-secondary">{{ entry.name }}</code> to report Ready.
          </div>
        </div>
      </div>
      <div
        v-else-if="loadState === 'error'"
        class="flex items-start gap-2 rounded-lg border border-danger/30 bg-danger-subtle p-4 text-sm text-danger"
      >
        <AlertCircle class="h-4 w-4 mt-0.5" :stroke-width="2" />
        <div>
          <div class="font-medium">Failed to load provider bundle</div>
          <div class="mt-1 text-xs font-mono">{{ loadError }}</div>
        </div>
      </div>

      <!-- The provider's custom element mounts here. No iframe, no border,
           no scrollbars; it's just DOM in the portal's tree. -->
      <div
        ref="mountRef"
        class="min-h-0 flex-1"
      />
    </div>
  </AppLayout>
</template>
