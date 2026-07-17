<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import { Server, Boxes, RefreshCw, Trash2, CircleDot, Plus } from 'lucide-vue-next'
import { setToken, setTenant, listEdges, deleteEdge } from './api'
import Wizard from './Wizard.vue'
import Detail from './Detail.vue'
import Workloads from './Workloads.vue'
import type { Edge, EdgeType, KedgeContext, ErrorResponse } from './types'

const props = defineProps<{ ctx: KedgeContext | null }>()

// Top-level view is driven by the shell route: /providers/edges → the edges
// fleet; /providers/edges/workloads → the workloads scheduled across them. The
// sidebar renders both as nav items (CatalogEntry ui.children), so switching
// happens via the menu; the in-page toggle mirrors it through navigate().
const view = computed<'edges' | 'workloads'>(() =>
  (props.ctx?.subPath ?? '').startsWith('workloads') ? 'workloads' : 'edges',
)

// navigate pushes the shell's router via a bubbling CustomEvent the element's
// ProviderFrame host listens for. path is the trailing segment appended to
// /providers/edges/ (empty = the edges list).
const rootRef = ref<HTMLElement | null>(null)
function navigate(path: string) {
  rootRef.value?.dispatchEvent(new CustomEvent('kedge-navigate', { detail: { path }, bubbles: true }))
}

// The wizard shows automatically on first load when the workspace has no edges,
// and on demand via "Connect edge". It closes back to the list on completion.
const wizardOpen = ref(false)
const firstLoadDone = ref(false)

// Selected edge → detail view. Null = list.
const selected = ref<{ name: string; type: EdgeType } | null>(null)

function openDetail(e: Edge) {
  selected.value = { name: e.name, type: e.type }
}
function closeDetail() {
  selected.value = null
  refresh()
}

const edges = ref<Edge[]>([])
const loading = ref(true)
const error = ref<string | null>(null)

async function refresh() {
  loading.value = true
  error.value = null
  try {
    edges.value = await listEdges()
    // Auto-open the wizard the first time we confirm the workspace has no edges.
    if (!firstLoadDone.value) {
      firstLoadDone.value = true
      if (edges.value.length === 0) wizardOpen.value = true
    }
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Failed to load edges'
  } finally {
    loading.value = false
  }
}

function onWizardDone() {
  wizardOpen.value = false
  refresh()
}

async function onDelete(edge: Edge) {
  if (!confirm(`Delete ${edge.type === 'server' ? 'server' : 'cluster'} "${edge.name}"?`)) return
  try {
    await deleteEdge(edge)
    await refresh()
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Delete failed'
  }
}

// Re-auth + reload whenever the shell pushes a new context (token/workspace).
watch(
  () => [props.ctx?.token, props.ctx?.tenant] as const,
  ([token, tenant]) => {
    setToken(token ?? null)
    setTenant(tenant ?? null)
    if (tenant) refresh()
  },
  { immediate: true },
)

// Light polling so status/connected updates without a manual refresh.
const timer = setInterval(() => {
  if (props.ctx?.tenant && !loading.value) refresh()
}, 10000)
onUnmounted(() => clearInterval(timer))

function rel(ts?: string): string {
  if (!ts) return '—'
  const d = new Date(ts).getTime()
  if (Number.isNaN(d)) return '—'
  const secs = Math.max(0, Math.floor((Date.now() - d) / 1000))
  if (secs < 60) return `${secs}s ago`
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`
  return `${Math.floor(secs / 86400)}d ago`
}
</script>

<template>
  <div ref="rootRef" class="edges-app">
    <!-- Section nav: Edges | Workloads. Mirrors the sidebar's sub-nav items and
         pushes the shell route via navigate(). Hidden while the wizard or a
         detail view is open so those flows stay focused. -->
    <nav v-if="!wizardOpen && !selected" class="wiz-steps" style="margin-bottom: 4px;">
      <button class="wiz-step" :class="{ active: view === 'edges' }" @click="navigate('')">Edges</button>
      <button class="wiz-step" :class="{ active: view === 'workloads' }" @click="navigate('workloads')">Workloads</button>
    </nav>

    <!-- Workloads view. -->
    <Workloads v-if="view === 'workloads' && !wizardOpen && !selected" />

    <!-- Onboarding / add-edge wizard (shown on first load when empty, or on demand). -->
    <Wizard v-else-if="wizardOpen" :cluster="props.ctx?.tenant ?? null" @connected="onWizardDone" />

    <!-- Per-edge detail view. -->
    <Detail
      v-else-if="selected"
      :name="selected.name"
      :type="selected.type"
      :cluster="props.ctx?.tenant ?? null"
      :token="props.ctx?.token ?? null"
      @back="closeDetail"
      @deleted="closeDetail"
    />

    <template v-else>
    <header class="edges-header">
      <div>
        <h1>Edges</h1>
        <p>Kubernetes clusters and Linux/SSH servers connected to this workspace.</p>
      </div>
      <div class="header-actions">
        <button class="btn" :disabled="loading" @click="refresh">
          <RefreshCw :size="14" :class="{ spin: loading }" /> Refresh
        </button>
        <button class="btn primary" @click="wizardOpen = true">
          <Plus :size="14" /> Connect edge
        </button>
      </div>
    </header>

    <div v-if="error" class="banner error">{{ error }}</div>

    <div v-if="loading && edges.length === 0" class="muted pad">Loading edges…</div>

    <div v-else-if="edges.length === 0" class="empty">
      <Boxes :size="28" />
      <div class="empty-title">No edges connected yet</div>
      <div class="muted">Click <b>Connect edge</b> to onboard one, or run <code>kedge edge create</code>.</div>
    </div>

    <div v-else class="edges-table-wrap">
      <table class="edges-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Status</th>
            <th>Agent</th>
            <th>Last heartbeat</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in edges" :key="e.type + '/' + e.name" class="clickable" @click="openDetail(e)">
            <td class="name">{{ e.name }}</td>
            <td>
              <span class="pill">
                <component :is="e.type === 'server' ? Server : Boxes" :size="12" />
                {{ e.type === 'server' ? 'Server' : 'Kubernetes' }}
              </span>
            </td>
            <td>
              <span class="status" :class="e.connected ? 'ok' : 'down'">
                <CircleDot :size="11" /> {{ e.connected ? 'Connected' : (e.phase || 'Disconnected') }}
              </span>
            </td>
            <td class="mono muted">{{ e.agentVersion || '—' }}</td>
            <td class="muted">{{ rel(e.lastHeartbeatTime) }}</td>
            <td class="actions">
              <button class="icon danger" title="Delete" @click.stop="onDelete(e)"><Trash2 :size="14" /></button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    </template>
  </div>
</template>
