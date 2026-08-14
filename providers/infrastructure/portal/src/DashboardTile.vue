<script setup lang="ts">
// Tile content for the infrastructure provider's dashboard summary.
// Mounted by <faros-dashboard-tile-infrastructure> (see element.ts).
//
// Gives the user an at-a-glance read on what they've provisioned in the
// CURRENT workspace:
//   - total instances + per-phase breakdown (Ready / Pending / Failed)
//   - top-4 most-recent instances with template + phase chip and a
//     click-through that bubbles faros-navigate up to the portal so it
//     pushes /providers/infrastructure/instances/<name>.
//
// Auth + workspace headers come from the farosContext the host pushed
// onto the element and the standard portal tenant slot in localStorage
// (same shape api.ts reads in App.vue). The tile is read-only — even if
// the workspace isn't bootstrapped yet (X-Faros-Tenant resolver returns
// nothing), we just render an empty state instead of bubbling errors.

import { computed, onMounted, onUnmounted, ref, watch, h } from 'vue'
import { api, setTenant, setToken } from './api'
import { tileClass } from './portalkit/dashboardtile'
import { ic } from './portalkit/icons'

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
const ChevronRight = inlineIcon('m9 18 6-6-6-6')

interface FarosContext {
  token?: string | null
  // tenant is the kcp cluster name (auth.clusterName in the shell).
  // Used as the /graphql/<cluster> path segment for gateway calls.
  tenant?: string | null
  basePath?: string
}

interface Instance {
  name: string
  namespace: string
  template: string
  phase: string
  createdAt: string
}

const props = defineProps<{ context: FarosContext | null }>()
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
  return { total, ready, pending, failed }
})

// Most-recent first, capped at 4 so the tile stays a fixed height.
const recent = computed(() =>
  [...instances.value]
    .sort((a, b) => (b.createdAt || '').localeCompare(a.createdAt || ''))
    .slice(0, 4),
)

// Refresh delegates to the shared GraphQL client in api.ts. The shell
// pushes the same token + tenant (auth.clusterName) to every mounted
// element, so even when the tile and the full provider page are open
// at the same time both end up with the same module state.
async function refresh() {
  const ctx = props.context
  if (!ctx?.tenant) {
    // No workspace selected yet — render the empty state rather than
    // querying the gateway without a cluster.
    instances.value = []
    error.value = null
    loading.value = false
    return
  }
  setToken(ctx.token ?? null)
  setTenant(ctx.tenant ?? null)
  try {
    const r = await api.listInstances()
    instances.value = r.items
    error.value = null
  } catch (e) {
    const err = e as { reason?: string; message?: string } | Error
    const reason = (err as { reason?: string }).reason
    const message = (err as { message?: string }).message ?? String(err)
    if (reason === 'APIBindingMissing' || reason === 'TenantMissing') {
      // Tenant has no binding yet (or never selected a workspace) —
      // empty tile is the right state, not an error banner.
      instances.value = []
      error.value = null
    } else {
      error.value = `${reason ?? 'Error'} — ${message}`
      instances.value = []
      // eslint-disable-next-line no-console
      console.warn('infrastructure tile listInstances failed', err)
    }
  } finally {
    loading.value = false
  }
}

function dispatchNavigate(path: string) {
  // CustomEvent bubbles up through the mount div → the portal-side
  // DashboardTile.vue listener → router.push('/providers/infrastructure/'+path).
  rootRef.value?.dispatchEvent(
    new CustomEvent('faros-navigate', { detail: { path }, bubbles: true }),
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

// Phase → dot colour. Unknown phases fall through to the neutral bucket so a
// future kro phase string doesn't render as "Failed" by mistake.
const phaseDot: Record<string, string> = {
  Ready: 'bg-success',
  Pending: 'bg-text-muted',
  Failed: 'bg-danger',
}
function dotFor(phase: string) {
  return phaseDot[phase] ?? 'bg-text-muted'
}
</script>

<template>
  <div ref="rootRef" :class="tileClass.root">
    <div v-if="loading" :class="tileClass.message">Loading instances&hellip;</div>
    <div v-else-if="error" :class="tileClass.error">Failed to load: {{ error }}</div>

    <template v-else>
      <!-- Slim horizontal status row (matches the clusters/edges tiles): a
           single inline line of icon + count + label chips rather than four
           stacked boxes, so the tile stays compact. -->
      <div :class="tileClass.stats">
        <span :class="[tileClass.stat, tileClass.statTotal]">
          <span v-html="ic('package', tileClass.statIcon)" />
          <span :class="tileClass.statNum">{{ stats.total }}</span>
          <span :class="tileClass.statLabel">total</span>
        </span>
        <span :class="[tileClass.stat, tileClass.statOk]">
          <span v-html="ic('check', tileClass.statIcon)" />
          <span class="tabular-nums">{{ stats.ready }}</span>
          <span :class="tileClass.statLabel">ready</span>
        </span>
        <span v-if="stats.pending > 0" :class="[tileClass.stat, tileClass.statMuted]">
          <span v-html="ic('clock', tileClass.statIcon)" />
          <span class="tabular-nums">{{ stats.pending }}</span>
          <span :class="tileClass.statLabel">pending</span>
        </span>
        <span v-if="stats.failed > 0" :class="[tileClass.stat, tileClass.statBad]">
          <span v-html="ic('alert-triangle', tileClass.statIcon)" />
          <span class="tabular-nums">{{ stats.failed }}</span>
          <span :class="tileClass.statLabel">failed</span>
        </span>
      </div>

      <!-- Recent instances. Click anywhere on the row → instance detail
           page. Bubbles via faros-navigate so the portal owns the URL.
           Row style matches the kubernetes-edges "Recent" list: a single
           compact line per item (phase icon · name · template · animated
           chevron) so the dashboard reads consistently across providers. -->
      <div v-if="recent.length > 0">
        <div :class="tileClass.sectionLabel">Recent</div>
        <ul :class="tileClass.list">
          <li v-for="i in recent" :key="i.name">
            <button
              type="button"
              :class="tileClass.row"
              @click="dispatchNavigate('instances/' + encodeURIComponent(i.name))"
            >
              <span :class="[tileClass.rowDot, dotFor(i.phase)]" aria-hidden="true" />
              <span :class="tileClass.rowPrimary">{{ i.name }}</span>
              <span :class="tileClass.rowSecondary">{{ i.template }}</span>
              <ChevronRight :class="tileClass.chevron" :stroke-width="2" />
            </button>
          </li>
        </ul>
      </div>

      <!-- Explicit empty state. The "scope hint" line covers a real
           migration footgun: instances provisioned before the user
           picked a workspace in the sidebar landed in the personal-org
           scope (no X-Faros-Workspace header), and the workspace-aware
           list now reads from a different namespace. The pointer to
           the Instances page lets them at least see their stranded
           CRs via the "no workspace" view there. -->
      <div v-else :class="tileClass.empty">
        <div>
          No instances yet in this workspace.
          <button
            type="button"
            class="ml-1 font-medium text-accent transition-colors hover:text-accent-hover"
            @click="dispatchNavigate('templates')"
          >
            Browse templates →
          </button>
        </div>
        <div class="mt-1 text-text-muted/70">
          Provisioned before picking a workspace?
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
