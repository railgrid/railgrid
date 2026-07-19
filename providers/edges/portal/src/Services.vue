<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { RefreshCw, Trash2, Plus, Boxes, ChevronRight, ChevronDown, Save, KeyRound } from 'lucide-vue-next'
import {
  listServices, createKubeEdgeService, deleteEdgeService,
  updateEdgeServiceInstructions, connectEdgeService, listEdges,
} from './api'
import type { EdgeService, EdgeServiceDraft, Edge, ErrorResponse } from './types'

// Service catalog — mirrors the backend svcCatalog (providers/edges/internal/
// tunnel/svc_catalog.go) + Home Assistant. Each entry seeds the default port and
// tells the operator what credential the service expects.
interface ServicePreset { type: string; label: string; category: string; port: number; tokenHint: string }
const PRESETS: ServicePreset[] = [
  { type: 'home-assistant', label: 'Home Assistant', category: 'Home & IoT', port: 8123, tokenHint: 'Long-lived access token' },
  { type: 'adguard', label: 'AdGuard Home', category: 'Network', port: 80, tokenHint: 'Web credentials as "username:password"' },
  { type: 'pihole', label: 'Pi-hole', category: 'Network', port: 80, tokenHint: 'Web-interface password (Pi-hole v6)' },
  { type: 'unifi-network', label: 'UniFi Network', category: 'Network', port: 443, tokenHint: 'UniFi OS local API key (X-API-KEY) — set scheme https, host = console IP' },
  { type: 'unifi-protect', label: 'UniFi Protect', category: 'Network', port: 443, tokenHint: 'UniFi OS local API key (X-API-KEY) — set scheme https, host = console IP' },
  { type: 'grafana', label: 'Grafana', category: 'Observability', port: 3000, tokenHint: 'Service-account / API token' },
  { type: 'grafana-loki', label: 'Grafana Loki', category: 'Observability', port: 3100, tokenHint: 'Bearer token (optional)' },
  { type: 'prometheus', label: 'Prometheus', category: 'Observability', port: 9090, tokenHint: 'Bearer token (optional — often none)' },
  { type: 'proxmox', label: 'Proxmox VE', category: 'Infra', port: 8006, tokenHint: 'API token "USER@REALM!ID=UUID" — set scheme https' },
  { type: 'portainer', label: 'Portainer', category: 'Infra', port: 9000, tokenHint: 'Access token (X-API-Key)' },
  { type: 'qbittorrent', label: 'qBittorrent', category: 'Media', port: 8080, tokenHint: 'WebUI credentials as "username:password"' },
  { type: 'prowlarr', label: 'Prowlarr', category: 'Media', port: 9696, tokenHint: 'API key (Settings → General)' },
  { type: 'sonarr', label: 'Sonarr', category: 'Media', port: 8989, tokenHint: 'API key (Settings → General)' },
  { type: 'radarr', label: 'Radarr', category: 'Media', port: 7878, tokenHint: 'API key (Settings → General)' },
  { type: 'jellyfin', label: 'Jellyfin', category: 'Media', port: 8096, tokenHint: 'API key (Dashboard → API Keys)' },
  { type: 'plex', label: 'Plex', category: 'Media', port: 32400, tokenHint: 'X-Plex-Token' },
  { type: 'generic', label: 'Generic (proxy only)', category: 'Other', port: 80, tokenHint: 'Bearer token (optional)' },
]
function presetFor(t?: string): ServicePreset | undefined {
  return PRESETS.find((p) => p.type === t)
}
// Presets grouped by category, preserving first-seen category order, for
// <optgroup> rendering in the type dropdown.
const PRESET_GROUPS = PRESETS.reduce<{ category: string; items: ServicePreset[] }[]>((groups, p) => {
  let g = groups.find((x) => x.category === p.category)
  if (!g) { g = { category: p.category, items: [] }; groups.push(g) }
  g.items.push(p)
  return groups
}, [])
function onTypeChange() {
  const p = presetFor(draft.value.serviceType)
  if (p) draft.value.port = p.port
  // UniFi OS speaks https (self-signed); default the scheme so it just works.
  if (draft.value.serviceType.startsWith('unifi-')) draft.value.scheme = 'https'
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

// The selected edge's kind decides the form shape: LinuxServer services target
// the host (loopback, or spec.host for a LAN device like a UniFi console);
// KubernetesCluster services target a named Kubernetes Service (targetRef).
const selectedEdgeIsServer = computed(
  () => edges.value.find((e) => e.name === draft.value.edgeName)?.type === 'server',
)

// Per-row expand for edit (instructions) + connect (token).
const expanded = ref<string | null>(null)
const editInstructions = ref('')
const editToken = ref('')
function toggle(s: EdgeService) {
  if (expanded.value === s.name) {
    expanded.value = null
    return
  }
  expanded.value = s.name
  editInstructions.value = s.instructions ?? ''
  editToken.value = ''
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
    (selectedEdgeIsServer.value || !!draft.value.targetName.trim()),
)

async function onCreate() {
  if (!canCreate.value) return
  busy.value = true
  error.value = null
  try {
    const isServer = selectedEdgeIsServer.value
    await createKubeEdgeService({
      name: draft.value.name.trim(),
      edgeName: draft.value.edgeName,
      edgeKind: isServer ? 'LinuxServer' : 'KubernetesCluster',
      serviceType: draft.value.serviceType,
      targetNamespace: draft.value.targetNamespace.trim() || 'default',
      targetName: draft.value.targetName.trim(),
      scheme: draft.value.scheme || 'http',
      host: isServer ? draft.value.host?.trim() || undefined : undefined,
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
  if (!confirm(`Delete service "${s.name}"? Its MCP tools stop being exposed.`)) return
  try {
    await deleteEdgeService(s.name)
    await refresh()
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Delete failed'
  }
}

async function onSaveInstructions(s: EdgeService) {
  busy.value = true
  error.value = null
  try {
    await updateEdgeServiceInstructions(s.name, editInstructions.value)
    await refresh()
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Save failed'
  } finally {
    busy.value = false
  }
}

async function onConnect(s: EdgeService) {
  if (!editToken.value.trim()) return
  busy.value = true
  error.value = null
  try {
    await connectEdgeService(s.name, editToken.value.trim())
    editToken.value = ''
    await refresh()
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Connect failed'
  } finally {
    busy.value = false
  }
}

onMounted(refresh)
const timer = setInterval(refresh, 10000)
onUnmounted(() => clearInterval(timer))

function phaseClass(p?: string): string {
  return p === 'Ready' ? 'ok' : p === 'Unreachable' ? 'down' : 'pending'
}
</script>

<template>
  <div class="edges-app">
    <header class="edges-header">
      <div>
        <h1>Services</h1>
        <p>Services running next to your edges (e.g. Home Assistant). Attach a token to make one Ready, and give it AI guidance — its tools appear in the MCP endpoint.</p>
      </div>
      <div class="header-actions">
        <button class="btn" :disabled="loading" @click="refresh">
          <RefreshCw :size="14" :class="{ spin: loading }" /> Refresh
        </button>
        <button class="btn primary" @click="showCreate = !showCreate">
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
          <select v-model="draft.edgeName" class="input">
            <option v-for="e in edges" :key="e.name" :value="e.name">{{ e.name }} ({{ e.type === 'server' ? 'LinuxServer' : 'KubernetesCluster' }})</option>
          </select>
        </label>
      </div>
      <div class="row" style="gap: 12px; align-items: flex-start;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Type</span>
          <select v-model="draft.serviceType" class="input" @change="onTypeChange">
            <optgroup v-for="g in PRESET_GROUPS" :key="g.category" :label="g.category">
              <option v-for="p in g.items" :key="p.type" :value="p.type">{{ p.label }}</option>
            </optgroup>
          </select>
        </label>
        <label class="fld" style="flex: 0 0 120px;">
          <span class="lbl">Scheme</span>
          <select v-model="draft.scheme" class="input">
            <option value="http">http</option>
            <option value="https">https</option>
          </select>
        </label>
        <label class="fld" style="flex: 0 0 120px;">
          <span class="lbl">Port</span>
          <input v-model="draft.port" type="number" min="1" max="65535" class="input" />
        </label>
      </div>
      <!-- LinuxServer: reach the host loopback, or a device on the edge's LAN. -->
      <div v-if="selectedEdgeIsServer" class="row" style="gap: 12px; align-items: flex-start;">
        <label class="fld" style="flex: 1;">
          <span class="lbl">Host (optional — device on the edge's LAN)</span>
          <input v-model="draft.host" class="input" placeholder="127.0.0.1 (loopback) or e.g. 192.168.1.1 for a UniFi console" />
        </label>
      </div>
      <!-- KubernetesCluster: reach a named Kubernetes Service. -->
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
            <th></th>
            <th>Name</th>
            <th>Edge</th>
            <th>Type</th>
            <th>Target</th>
            <th>Status</th>
            <th>Token</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <template v-for="s in services" :key="s.name">
            <tr class="clickable" @click="toggle(s)">
              <td><component :is="expanded === s.name ? ChevronDown : ChevronRight" :size="14" /></td>
              <td class="name">{{ s.name }}</td>
              <td class="muted">{{ s.edgeName || '—' }}</td>
              <td class="mono muted">{{ s.serviceType || '—' }}</td>
              <td class="mono muted">{{ s.targetNamespace ? s.targetNamespace + '/' : '' }}{{ s.targetName || '—' }}:{{ s.port || '' }}</td>
              <td><span class="status" :class="phaseClass(s.phase)">{{ s.phase || 'Pending' }}</span></td>
              <td>{{ s.hasCredentials ? '✓' : '—' }}</td>
              <td class="actions">
                <button class="icon danger" title="Delete" @click.stop="onDelete(s)"><Trash2 :size="14" /></button>
              </td>
            </tr>
            <tr v-if="expanded === s.name" class="detail-row">
              <td colspan="8">
                <div class="es-head">AI instructions</div>
                <textarea v-model="editInstructions" class="input" rows="4" placeholder="Describe this service's entities/rooms so the AI knows your setup."></textarea>
                <div class="wiz-actions" style="margin: 8px 0 16px;">
                  <button class="btn primary" :disabled="busy" @click="onSaveInstructions(s)"><Save :size="14" /> Save instructions</button>
                </div>

                <div class="es-head">Access token</div>
                <div class="muted" style="margin-bottom: 6px;">{{ presetFor(s.serviceType)?.tokenHint ?? 'Access token' }} — makes the service Ready. Stored as a Secret, never on the agent host.</div>
                <div class="row" style="gap: 8px;">
                  <input v-model="editToken" type="password" class="input" style="flex: 1;" placeholder="token" />
                  <button class="btn" :disabled="busy || !editToken.trim()" @click="onConnect(s)"><KeyRound :size="14" /> {{ s.hasCredentials ? 'Update token' : 'Set token' }}</button>
                </div>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>
  </div>
</template>
