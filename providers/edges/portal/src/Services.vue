<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { RefreshCw, Trash2, Plus, Boxes, Pencil, Check } from 'lucide-vue-next'
import {
  listServices, createKubeEdgeService, deleteEdgeService, listEdges,
  fetchServiceCatalog,
} from './api'
import type { CatalogEntry } from './api'
import type { EdgeService, EdgeServiceDraft, Edge, ErrorResponse } from './types'
import { confirmDialog } from './portalkit/confirm'
import ServiceEdit from './ServiceEdit.vue'

// Service type catalog — fetched from the backend (svccatalog.All()) so the form
// never drifts from the provider's auth/probe knowledge. Each entry seeds the
// port/scheme and describes the credential fields the UI collects.
const catalog = ref<CatalogEntry[]>([])
function catalogFor(t?: string): CatalogEntry | undefined {
  return catalog.value.find((c) => c.type === t)
}
// Types grouped by category (first-seen order) for <optgroup> rendering.
const CATALOG_GROUPS = computed(() =>
  catalog.value.reduce<{ category: string; items: CatalogEntry[] }[]>((groups, c) => {
    const cat = c.category || 'Other'
    let g = groups.find((x) => x.category === cat)
    if (!g) { g = { category: cat, items: [] }; groups.push(g) }
    g.items.push(c)
    return groups
  }, []),
)
// A one-entry fallback so the form still works if the catalog fetch fails.
const GENERIC_FALLBACK: CatalogEntry = {
  type: 'generic', displayName: 'Generic HTTP service', category: 'Other',
  defaultPort: 80, defaultScheme: 'http', auth: 'bearer',
  credential: { optional: true, packing: 'single', fields: [{ key: 'token', label: 'Bearer token (optional)', secret: true }] },
}
async function loadCatalog() {
  try {
    catalog.value = await fetchServiceCatalog()
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Failed to load service catalog'
    if (!catalog.value.length) catalog.value = [GENERIC_FALLBACK]
  }
}
// schemeLocked types (e.g. UniFi is always https) pin the scheme select.
const createSchemeLocked = computed(() => !!catalogFor(draft.value.serviceType)?.schemeLocked)
function onTypeChange() {
  const c = catalogFor(draft.value.serviceType)
  if (c) {
    if (c.defaultPort) draft.value.port = c.defaultPort
    if (c.defaultScheme) draft.value.scheme = c.defaultScheme
  }
  resetTargetMode()
}

const services = ref<EdgeService[]>([])
const edges = ref<Edge[]>([])
const loading = ref(true)
const error = ref<string | null>(null)

const showCreate = ref(false)
const busy = ref(false)
const draft = ref<EdgeServiceDraft>({
  name: '',
  edgeName: '',
  serviceType: 'home-assistant',
  targetNamespace: '',
  targetName: '',
  scheme: 'http',
  host: '',
  port: 8123,
  instructions: '',
})

// How the service is reached is an explicit choice, independent of the edge kind:
//   host — dial an address directly (agent loopback, or a LAN device like a UniFi
//          console). Works on either edge kind.
//   kube — reach a named Kubernetes Service by cluster DNS (KubernetesCluster only).
const targetMode = ref<'host' | 'kube'>('host')

const selectedEdgeIsServer = computed(
  () => edges.value.find((e) => e.name === draft.value.edgeName)?.type === 'server',
)

// Sensible default target mode when the edge or type changes: LAN-style services
// (UniFi) and LinuxServer edges default to host; KubernetesCluster edges default
// to a cluster Service. The user can override with the toggle.
function resetTargetMode() {
  // Types flagged hostRequired live on the edge LAN (e.g. a UniFi console), not
  // on the agent loopback, so they default to host addressing.
  const needsHost = !!catalogFor(draft.value.serviceType)?.hostRequired
  targetMode.value = selectedEdgeIsServer.value || needsHost ? 'host' : 'kube'
}

function toggleCreate() {
  showCreate.value = !showCreate.value
  if (showCreate.value) resetTargetMode()
}

// spec.host is a bare hostname/IP (scheme + port are separate fields). If the
// user pastes a full URL, split it into host + scheme + port for convenience.
// Any path is dropped — services proxy at the root.
function applyHostUrl() {
  const raw = draft.value.host?.trim()
  if (!raw || !/^https?:\/\//i.test(raw)) return
  try {
    const u = new URL(raw)
    draft.value.scheme = u.protocol.replace(':', '')
    draft.value.host = u.hostname
    draft.value.port = Number(u.port) || (u.protocol === 'https:' ? 443 : 80)
  } catch {
    /* not a valid URL — leave the field as typed */
  }
}

// Edit opens a dedicated per-service page (ServiceEdit.vue) that hosts the
// provider info, config, credentials and status. Held as local state (no shell
// router), the same way App.vue drives the Edges Detail view.
const editing = ref<EdgeService | null>(null)
function openEdit(s: EdgeService) {
  editing.value = s
}
// After a save in the edit page, refresh the list and re-seed the open page from
// the fresh object so status/conditions update in place.
async function onEditSaved() {
  const name = editing.value?.name
  await refresh()
  if (name) editing.value = services.value.find((s) => s.name === name) ?? null
}

async function refresh() {
  loading.value = true
  error.value = null
  try {
    ;[services.value, edges.value] = await Promise.all([listServices(), listEdges()])
    if (!draft.value.edgeName && edges.value.length) draft.value.edgeName = edges.value[0].name
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Failed to load services'
  } finally {
    loading.value = false
  }
}

const canCreate = computed(
  () =>
    !!draft.value.name.trim() &&
    !!draft.value.edgeName &&
    // host mode: host optional (empty = agent loopback). kube mode: need a target.
    (targetMode.value === 'host' || !!draft.value.targetName.trim()),
)

async function onCreate() {
  if (!canCreate.value) return
  if (targetMode.value === 'host') applyHostUrl() // normalize a pasted URL
  busy.value = true
  error.value = null
  try {
    const byHost = targetMode.value === 'host'
    await createKubeEdgeService({
      name: draft.value.name.trim(),
      edgeName: draft.value.edgeName,
      edgeKind: selectedEdgeIsServer.value ? 'LinuxServer' : 'KubernetesCluster',
      serviceType: draft.value.serviceType,
      targetNamespace: draft.value.targetNamespace.trim() || 'default',
      targetName: byHost ? '' : draft.value.targetName.trim(),
      scheme: draft.value.scheme || 'http',
      host: byHost ? draft.value.host?.trim() || undefined : undefined,
      port: Number(draft.value.port) || 8123,
      instructions: draft.value.instructions?.trim() || undefined,
    })
    showCreate.value = false
    draft.value = { name: '', edgeName: edges.value[0]?.name ?? '', serviceType: 'home-assistant', targetNamespace: '', targetName: '', scheme: 'http', host: '', port: 8123, instructions: '' }
    await refresh()
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Create failed'
  } finally {
    busy.value = false
  }
}

async function onDelete(s: EdgeService) {
  if (!(await confirmDialog({ title: `Delete service "${s.name}"?`, message: 'Its MCP tools stop being exposed.', danger: true, confirmLabel: 'Delete' }))) return
  try {
    await deleteEdgeService(s.name)
    await refresh()
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Delete failed'
  }
}

onMounted(() => {
  loadCatalog()
  refresh()
})
const timer = setInterval(refresh, 10000)
onUnmounted(() => clearInterval(timer))

function phaseClass(p?: string): string {
  return p === 'Ready' ? 'ok' : p === 'Unreachable' ? 'down' : 'pending'
}
</script>

<template>
  <ServiceEdit
    v-if="editing"
    :service="editing"
    :catalog="catalog"
    :edges="edges"
    @back="editing = null"
    @saved="onEditSaved"
  />
  <div v-else class="edges-app">
    <header class="edges-header">
      <div>
        <h1>Services</h1>
        <p>Services running next to your edges (e.g. Home Assistant). Attach a token to make one Ready, and give it AI guidance — its tools appear in the MCP endpoint.</p>
      </div>
      <div class="header-actions">
        <button class="btn" :disabled="loading" @click="refresh">
          <RefreshCw :size="14" :class="{ spin: loading }" /> Refresh
        </button>
        <button class="btn primary" @click="toggleCreate">
          <Plus :size="14" /> New service
        </button>
      </div>
    </header>

    <div v-if="error" class="banner error">{{ error }}</div>

    <!-- Create form -->
    <div v-if="showCreate" class="wiz-card" style="margin-bottom: 16px;">
      <h3>New service</h3>
      <div class="row" style="gap: 12px; align-items: flex-start;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Name</span>
          <input v-model="draft.name" class="input" placeholder="ha" />
        </label>
        <label class="fld" style="flex: 1;">
          <span class="lbl">Edge</span>
          <select v-model="draft.edgeName" class="input" @change="resetTargetMode">
            <option v-for="e in edges" :key="e.name" :value="e.name">{{ e.name }} ({{ e.type === 'server' ? 'LinuxServer' : 'KubernetesCluster' }})</option>
          </select>
        </label>
      </div>
      <div class="row" style="gap: 12px; align-items: flex-start;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Type</span>
          <select v-model="draft.serviceType" class="input" @change="onTypeChange">
            <optgroup v-for="g in CATALOG_GROUPS" :key="g.category" :label="g.category">
              <option v-for="c in g.items" :key="c.type" :value="c.type">{{ c.displayName }}</option>
            </optgroup>
          </select>
        </label>
        <label class="fld" style="flex: 0 0 120px;">
          <span class="lbl">Scheme</span>
          <select v-model="draft.scheme" class="input" :disabled="createSchemeLocked" :title="createSchemeLocked ? 'Fixed by the service type' : ''">
            <option value="http">http</option>
            <option value="https">https</option>
          </select>
        </label>
        <label class="fld" style="flex: 0 0 120px;">
          <span class="lbl">Port</span>
          <input v-model="draft.port" type="number" min="1" max="65535" class="input" />
        </label>
      </div>
      <!-- Target: an explicit choice, independent of the edge kind. -->
      <label class="fld">
        <span class="lbl">Target</span>
        <div class="row" style="gap: 16px;">
          <label style="display: flex; align-items: center; gap: 6px; cursor: pointer;">
            <input type="radio" value="host" v-model="targetMode" /> Host / IP
          </label>
          <label style="display: flex; align-items: center; gap: 6px; cursor: pointer;" :style="{ opacity: selectedEdgeIsServer ? 0.5 : 1 }">
            <input type="radio" value="kube" v-model="targetMode" :disabled="selectedEdgeIsServer" /> Kubernetes Service
          </label>
        </div>
      </label>
      <!-- Host: dial an address directly (loopback, or a LAN device like UniFi). -->
      <div v-if="targetMode === 'host'" class="row" style="gap: 12px; align-items: flex-start;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Host {{ catalogFor(draft.serviceType)?.hostRequired ? '(required)' : '(blank = agent loopback)' }}</span>
          <input v-model="draft.host" class="input" @blur="applyHostUrl" placeholder="192.168.1.1, myui.example.com, or paste https://myui.example.com — blank = 127.0.0.1" />
          <span v-if="catalogFor(draft.serviceType)?.hostHelp" class="muted" style="font-size: 12px; margin-top: 4px;">{{ catalogFor(draft.serviceType)?.hostHelp }}</span>
        </label>
      </div>
      <!-- Kubernetes Service: reach it by cluster DNS. -->
      <div v-else class="row" style="gap: 12px; align-items: flex-start;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Target namespace</span>
          <input v-model="draft.targetNamespace" class="input" placeholder="home" />
        </label>
        <label class="fld" style="flex: 1;">
          <span class="lbl">Target service name</span>
          <input v-model="draft.targetName" class="input" placeholder="home-assistant" />
        </label>
      </div>
      <label class="fld">
        <span class="lbl">AI instructions (optional)</span>
        <textarea v-model="draft.instructions" class="input" rows="3" placeholder="Gates are cover.gate_main. Living room light is light.living_room."></textarea>
      </label>
      <div class="wiz-actions">
        <button class="btn" @click="showCreate = false">Cancel</button>
        <button class="btn primary" :disabled="busy || !canCreate" @click="onCreate">Create</button>
      </div>
    </div>

    <div v-if="loading && services.length === 0" class="muted pad">Loading services…</div>

    <div v-else-if="services.length === 0" class="empty">
      <Boxes :size="28" />
      <div class="empty-title">No services yet</div>
      <div class="muted">Click <b>New service</b> to declare one (e.g. Home Assistant) on a Kubernetes edge.</div>
    </div>

    <div v-else class="edges-table-wrap">
      <table class="edges-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Edge</th>
            <th>Type</th>
            <th>Target</th>
            <th>Status</th>
            <th>Creds</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in services" :key="s.name" class="clickable" @click="openEdit(s)">
            <td class="name">{{ s.name }}</td>
            <td class="muted">{{ s.edgeName || '—' }}</td>
            <td class="mono muted">{{ catalogFor(s.serviceType)?.displayName || s.serviceType || '—' }}</td>
            <td class="mono muted">{{ s.host || (s.targetNamespace ? s.targetNamespace + '/' : '') + (s.targetName || '—') }}:{{ s.port || '' }}</td>
            <td><span class="status" :class="phaseClass(s.phase)">{{ s.phase || 'Pending' }}</span></td>
            <td><Check v-if="s.hasCredentials" :size="16" class="ok-check" /><span v-else class="muted">—</span></td>
            <td class="actions">
              <button class="icon" title="Edit" @click.stop="openEdit(s)"><Pencil :size="14" /></button>
              <button class="icon danger" title="Delete" @click.stop="onDelete(s)"><Trash2 :size="14" /></button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
