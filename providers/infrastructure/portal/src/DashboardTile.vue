<script setup lang="ts">
// Tile content for the infrastructure provider's dashboard summary.
// Mounted by <kedge-dashboard-tile-infrastructure> (see element.ts).
//
// Gives the user an at-a-glance read on what they've provisioned in the
// CURRENT workspace:
//   - total instances + per-phase breakdown (Ready / Pending / Failed)
//   - top-4 most-recent instances with template + phase chip and a
//     click-through that bubbles kedge-navigate up to the portal so it
//     pushes /providers/infrastructure/instances/<name>.
//
// Auth + workspace headers come from the kedgeContext the host pushed
// onto the element and the standard portal tenant slot in localStorage
// (same shape api.ts reads in App.vue). The tile is read-only — even if
// the workspace isn't bootstrapped yet (X-Kedge-Tenant resolver returns
// nothing), we just render an empty state instead of bubbling errors.

import { computed, onMounted, onUnmounted, ref, watch, h } from 'vue'

// Inline icon components — the provider's portal bundle is
// intentionally self-contained (no parent node_modules symlink) so we
// can't pull lucide-vue-next here without bloating package.json. SVG
// strings copied verbatim from lucide.dev so a future swap to the
// icon lib (if we add the symlink later) is a 1:1 visual replacement.
//
// Each component accepts the same `class` prop a real lucide component
// would, so the call sites read identically in the template.
function inlineIcon(path: string) {
  return (props: { class?: string }) =>
    h(
      'svg',
      {
        xmlns: 'http://www.w3.org/2000/svg',
        viewBox: '0 0 24 24',
        fill: 'none',
        stroke: 'currentColor',
        'stroke-width': 2,
        'stroke-linecap': 'round',
        'stroke-linejoin': 'round',
        class: props.class,
      },
      [h('path', { d: path })],
    )
}
const Layers = (props: { class?: string }) =>
  h(
    'svg',
    {
      xmlns: 'http://www.w3.org/2000/svg',
      viewBox: '0 0 24 24',
      fill: 'none',
      stroke: 'currentColor',
      'stroke-width': 2,
      'stroke-linecap': 'round',
      'stroke-linejoin': 'round',
      class: props.class,
    },
    [
      h('path', { d: 'm12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.65 0l8.58-3.9a1 1 0 0 0 0-1.83Z' }),
      h('path', { d: 'm22 17.65-9.17 4.16a2 2 0 0 1-1.66 0L2 17.65' }),
      h('path', { d: 'm22 12.65-9.17 4.16a2 2 0 0 1-1.66 0L2 12.65' }),
    ],
  )
const ChevronRight = inlineIcon('m9 18 6-6-6-6')
const CheckCircle2 = (props: { class?: string }) =>
  h(
    'svg',
    {
      xmlns: 'http://www.w3.org/2000/svg',
      viewBox: '0 0 24 24',
      fill: 'none',
      stroke: 'currentColor',
      'stroke-width': 2,
      'stroke-linecap': 'round',
      'stroke-linejoin': 'round',
      class: props.class,
    },
    [
      h('circle', { cx: 12, cy: 12, r: 10 }),
      h('path', { d: 'm9 12 2 2 4-4' }),
    ],
  )
const Clock = (props: { class?: string }) =>
  h(
    'svg',
    {
      xmlns: 'http://www.w3.org/2000/svg',
      viewBox: '0 0 24 24',
      fill: 'none',
      stroke: 'currentColor',
      'stroke-width': 2,
      'stroke-linecap': 'round',
      'stroke-linejoin': 'round',
      class: props.class,
    },
    [
      h('circle', { cx: 12, cy: 12, r: 10 }),
      h('polyline', { points: '12 6 12 12 16 14' }),
    ],
  )
const AlertCircle = (props: { class?: string }) =>
  h(
    'svg',
    {
      xmlns: 'http://www.w3.org/2000/svg',
      viewBox: '0 0 24 24',
      fill: 'none',
      stroke: 'currentColor',
      'stroke-width': 2,
      'stroke-linecap': 'round',
      'stroke-linejoin': 'round',
      class: props.class,
    },
    [
      h('circle', { cx: 12, cy: 12, r: 10 }),
      h('line', { x1: 12, x2: 12, y1: 8, y2: 12 }),
      h('line', { x1: 12, x2: 12.01, y1: 16, y2: 16 }),
    ],
  )

interface KedgeContext {
  token?: string | null
  basePath?: string
}

interface Instance {
  name: string
  namespace: string
  template: string
  phase: string
  createdAt: string
}

const props = defineProps<{ context: KedgeContext | null }>()
const rootRef = ref<HTMLElement | null>(null)

const instances = ref<Instance[]>([])
const loading = ref(true)
const error = ref<string | null>(null)
let pollHandle: number | null = null

const stats = computed(() => {
  const total = instances.value.length
  const ready = instances.value.filter((i) => i.phase === 'Ready').length
  const pending = instances.value.filter((i) => i.phase === 'Pending').length
  const failed = instances.value.filter((i) => i.phase === 'Failed').length
  const healthPct = total === 0 ? 0 : Math.round((ready / total) * 100)
  return { total, ready, pending, failed, healthPct }
})

// Most-recent first, capped at 4 so the tile stays a fixed height.
const recent = computed(() =>
  [...instances.value]
    .sort((a, b) => (b.createdAt || '').localeCompare(a.createdAt || ''))
    .slice(0, 4),
)

function authHeaders(ctx: KedgeContext | null): Record<string, string> {
  const h: Record<string, string> = {}
  if (ctx?.token) h['Authorization'] = `Bearer ${ctx.token}`
  // Workspace context mirrors api.ts's readTenantSelection — kept inline
  // to avoid coupling the tile to the main app's setBasePath/setToken
  // module state (the tile and the full element can be mounted
  // simultaneously, and module-scoped state would race).
  try {
    const raw = localStorage.getItem('kedge:portal:tenant')
    if (raw) {
      const t = JSON.parse(raw) as { orgUUID?: string | null; workspaceUUID?: string | null }
      if (t.orgUUID) h['X-Kedge-Org'] = t.orgUUID
      if (t.workspaceUUID) h['X-Kedge-Workspace'] = t.workspaceUUID
    }
  } catch {
    /* ignore */
  }
  return h
}

async function refresh() {
  const ctx = props.context
  if (!ctx?.basePath) {
    // Context hasn't arrived yet — the host pushes it after the
    // element appends. The watcher will re-call us once it does.
    return
  }
  // basePath comes in as /ui/providers/infrastructure (the shell prefix).
  // The REST surface lives at the matching /services prefix.
  const apiBase = ctx.basePath.replace(/^\/ui\/providers\//, '/services/providers/')
  const url = apiBase + '/api/instances'
  try {
    const res = await fetch(url, {
      headers: authHeaders(ctx),
      credentials: 'same-origin',
    })
    if (res.ok) {
      const body = (await res.json()) as { items?: Instance[] }
      instances.value = body.items ?? []
      error.value = null
    } else {
      // Show the failure on the tile rather than silently zero-ing
      // counts — a tile that reports "0 / 0 / 0" when there are
      // instances is worse than no tile at all. The status code + a
      // short message body is enough for a developer (or motivated
      // user) to diagnose without opening devtools.
      const bodyText = await res.text().catch(() => '')
      const snippet = bodyText.length > 160 ? bodyText.slice(0, 160) + '…' : bodyText
      error.value = `${res.status} ${res.statusText}${snippet ? ' — ' + snippet : ''}`
      instances.value = []
      // eslint-disable-next-line no-console
      console.warn('infrastructure tile fetch failed', { url, status: res.status, body: bodyText })
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    instances.value = []
    // eslint-disable-next-line no-console
    console.warn('infrastructure tile fetch threw', { url, err: e })
  } finally {
    loading.value = false
  }
}

function dispatchNavigate(path: string) {
  // CustomEvent bubbles up through the mount div → the portal-side
  // DashboardTile.vue listener → router.push('/providers/infrastructure/'+path).
  rootRef.value?.dispatchEvent(
    new CustomEvent('kedge-navigate', { detail: { path }, bubbles: true }),
  )
}

onMounted(() => {
  refresh()
  // 30s poll matches the catalog/instance list cadence in the main app —
  // anything tighter wastes the hub roundtrips for a tile users glance at.
  pollHandle = window.setInterval(refresh, 30000)
})
onUnmounted(() => {
  if (pollHandle !== null) window.clearInterval(pollHandle)
})
watch(() => props.context, refresh)

// Phase → icon + tailwind classes. Unknown phases fall through to the
// neutral bucket so a future kro phase string doesn't render as
// "Failed" by mistake.
const phaseStyle: Record<string, { icon: typeof CheckCircle2; cls: string }> = {
  Ready: { icon: CheckCircle2, cls: 'text-success' },
  Pending: { icon: Clock, cls: 'text-text-muted' },
  Failed: { icon: AlertCircle, cls: 'text-danger' },
}
function styleFor(phase: string) {
  return phaseStyle[phase] ?? { icon: Clock, cls: 'text-text-muted' }
}
</script>

<template>
  <div ref="rootRef" class="space-y-3">
    <div v-if="loading" class="text-[11px] text-text-muted">Loading instances&hellip;</div>
    <div v-else-if="error" class="text-[11px] text-danger">Failed to load: {{ error }}</div>

    <template v-else>
      <!-- Totals + per-phase chips. Mirrors kubernetes-edges' "cluster
           trio" so the dashboard reads consistently across providers. -->
      <div class="grid grid-cols-4 gap-2">
        <div class="rounded-lg border border-border-subtle bg-surface-overlay/60 p-2">
          <div class="flex items-center gap-1 text-[10px] font-medium uppercase tracking-wider text-text-muted">
            <Layers class="h-3 w-3" :stroke-width="2" />
            Total
          </div>
          <div class="mt-1 text-[18px] font-bold tabular-nums text-text-primary">{{ stats.total }}</div>
        </div>
        <div class="rounded-lg border border-border-subtle bg-surface-overlay/60 p-2">
          <div class="text-[10px] font-medium uppercase tracking-wider text-success">Ready</div>
          <div class="mt-1 text-[18px] font-bold tabular-nums text-text-primary">{{ stats.ready }}</div>
        </div>
        <div class="rounded-lg border border-border-subtle bg-surface-overlay/60 p-2">
          <div class="text-[10px] font-medium uppercase tracking-wider text-text-muted">Pending</div>
          <div class="mt-1 text-[18px] font-bold tabular-nums text-text-primary">{{ stats.pending }}</div>
        </div>
        <div class="rounded-lg border border-border-subtle bg-surface-overlay/60 p-2">
          <div class="text-[10px] font-medium uppercase tracking-wider text-danger">Failed</div>
          <div class="mt-1 text-[18px] font-bold tabular-nums text-text-primary">{{ stats.failed }}</div>
        </div>
      </div>

      <!-- Health bar — only meaningful when there's anything to be
           healthy. Hidden on empty state to avoid a 0%-of-0 oddity. -->
      <div v-if="stats.total > 0" class="space-y-1">
        <div class="flex items-center justify-between text-[10px] text-text-muted">
          <span>Health</span>
          <span class="font-mono tabular-nums">{{ stats.healthPct }}%</span>
        </div>
        <div class="h-1.5 overflow-hidden rounded-full bg-surface-overlay">
          <div class="h-full bg-success transition-all" :style="{ width: stats.healthPct + '%' }" />
        </div>
      </div>

      <!-- Recent instances. Click anywhere on the row → instance detail
           page. Bubbles via kedge-navigate so the portal owns the URL. -->
      <div v-if="recent.length > 0" class="space-y-1">
        <div class="text-[10px] font-medium uppercase tracking-wider text-text-muted">Recent</div>
        <button
          v-for="i in recent"
          :key="i.name"
          type="button"
          class="flex w-full items-center gap-2 rounded-lg border border-border-subtle bg-surface-overlay/60 p-2 text-left transition-colors hover:border-border-strong hover:bg-surface-overlay"
          @click="dispatchNavigate('instances/' + encodeURIComponent(i.name))"
        >
          <component :is="styleFor(i.phase).icon" :class="styleFor(i.phase).cls + ' h-3.5 w-3.5 flex-shrink-0'" :stroke-width="2" />
          <div class="min-w-0 flex-1">
            <div class="truncate text-[11px] font-medium text-text-primary">{{ i.name }}</div>
            <div class="truncate text-[10px] text-text-muted">{{ i.template }}</div>
          </div>
          <ChevronRight class="h-3 w-3 flex-shrink-0 text-text-muted" :stroke-width="2" />
        </button>
      </div>

      <!-- Explicit empty state. The "scope hint" line covers a real
           migration footgun: instances provisioned before the user
           picked a workspace in the sidebar landed in the personal-org
           scope (no X-Kedge-Workspace header), and the workspace-aware
           list now reads from a different namespace. The pointer to
           the Instances page lets them at least see their stranded
           CRs via the "no workspace" view there. -->
      <div v-else class="rounded-lg border border-dashed border-border-subtle p-3 text-[11px] text-text-muted">
        <div class="text-center">
          No instances yet in this workspace.
          <button
            type="button"
            class="ml-1 font-medium text-accent transition-colors hover:text-accent-hover"
            @click="dispatchNavigate('templates')"
          >
            Browse templates →
          </button>
        </div>
        <div class="mt-2 text-center text-[10px] text-text-muted/70">
          Expected to see instances? They may have been provisioned in a different scope.
          <button
            type="button"
            class="font-medium text-accent transition-colors hover:text-accent-hover"
            @click="dispatchNavigate('instances')"
          >
            Open Instances →
          </button>
        </div>
      </div>
    </template>
  </div>
</template>
