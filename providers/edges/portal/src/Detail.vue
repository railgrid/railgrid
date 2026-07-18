<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import { ArrowLeft, RefreshCw, Trash2, CircleDot, Server, Boxes, Copy, Check, TerminalSquare, Home, Plug, Plus } from 'lucide-vue-next'
import { getEdge, deleteEdge, listEdgeServices, connectEdgeService, createKubeEdgeService, deleteEdgeService } from './api'
import type { EdgeDetail, EdgeService, EdgeType, ErrorResponse } from './types'

const props = defineProps<{ name: string; type: EdgeType; cluster: string | null; token: string | null }>()
const emit = defineEmits<{ back: []; deleted: [] }>()

// SSH terminals dock at the bottom of the host portal (survives page
// navigation) rather than rendering inline here. The provider is an isolated
// micro-frontend and can't reach the host's Pinia terminal store directly, so
// it dispatches a window-scoped "kedge-terminal-open" CustomEvent that the
// host TerminalDock listens for (see portal/src/components/TerminalDock.vue).
function openTerminal() {
  if (!props.cluster) return
  window.dispatchEvent(
    new CustomEvent('kedge-terminal-open', {
      detail: { edgeName: props.name, cluster: props.cluster, displayName: props.name },
    }),
  )
}

const edge = ref<EdgeDetail | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const copied = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    edge.value = await getEdge(props.name, props.type)
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Failed to load edge'
  } finally {
    loading.value = false
  }
}

async function onDelete() {
  if (!edge.value) return
  if (!confirm(`Delete ${props.type === 'server' ? 'server' : 'cluster'} "${props.name}"?`)) return
  try {
    await deleteEdge(edge.value)
    emit('deleted')
  } catch (e) {
    error.value = (e as ErrorResponse)?.message ?? 'Delete failed'
  }
}

async function copy(text: string, field: string) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = field
    setTimeout(() => (copied.value = null), 2000)
  } catch { /* clipboard denied */ }
}

// ─── Services ────────────────────────────────────────────────────────
// Server edges: discovered by the agent. Kube edges: declared here (a cluster
// has far more services than a host, so we don't auto-scan).
const services = ref<EdgeService[]>([])
const svcError = ref<string | null>(null)
const connectFor = ref<string | null>(null) // Service name being connected
const tokenInput = ref('')
const connecting = ref(false)

async function loadServices() {
  try {
    services.value = await listEdgeServices(props.name)
    svcError.value = null
  } catch (e) {
    svcError.value = (e as ErrorResponse)?.message ?? 'Failed to load services'
  }
}

// Declare-service form (kube edges only).
const adding = ref(false)
const saving = ref(false)
const draft = ref({ name: '', serviceType: 'home-assistant', targetNamespace: '', targetName: '', port: 8123 })

function startAdd() {
  adding.value = true
  draft.value = { name: '', serviceType: 'home-assistant', targetNamespace: '', targetName: '', port: 8123 }
}

const draftValid = computed(
  () =>
    !!draft.value.name.trim() &&
    !!draft.value.targetNamespace.trim() &&
    !!draft.value.targetName.trim() &&
    draft.value.port > 0,
)

async function submitAdd() {
  if (!draftValid.value) return
  saving.value = true
  svcError.value = null
  try {
    await createKubeEdgeService({
      name: draft.value.name.trim(),
      edgeName: props.name,
      serviceType: draft.value.serviceType,
      targetNamespace: draft.value.targetNamespace.trim(),
      targetName: draft.value.targetName.trim(),
      port: Number(draft.value.port),
    })
    adding.value = false
    await loadServices()
  } catch (e) {
    svcError.value = (e as ErrorResponse)?.message ?? 'Failed to add service'
  } finally {
    saving.value = false
  }
}

async function removeService(name: string) {
  if (!confirm(`Delete service "${name}"?`)) return
  try {
    await deleteEdgeService(name)
    await loadServices()
  } catch (e) {
    svcError.value = (e as ErrorResponse)?.message ?? 'Delete failed'
  }
}

function startConnect(name: string) {
  connectFor.value = name
  tokenInput.value = ''
}

async function submitConnect() {
  if (!connectFor.value || !tokenInput.value.trim()) return
  connecting.value = true
  svcError.value = null
  try {
    await connectEdgeService(connectFor.value, tokenInput.value.trim())
    connectFor.value = null
    tokenInput.value = ''
    await loadServices()
  } catch (e) {
    svcError.value = (e as ErrorResponse)?.message ?? 'Connect failed'
  } finally {
    connecting.value = false
  }
}

function svcOk(es: EdgeService): boolean {
  return es.phase === 'Ready'
}

watch(() => [props.name, props.type], () => { load(); loadServices() }, { immediate: true })
const timer = setInterval(() => { load(); loadServices() }, 10000)
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
  <div class="edges-app">
    <header class="edges-header">
      <div class="row">
        <button class="icon" title="Back" @click="emit('back')"><ArrowLeft :size="16" /></button>
        <div>
          <h1 class="row">
            <component :is="type === 'server' ? Server : Boxes" :size="16" />
            {{ name }}
          </h1>
          <p>{{ type === 'server' ? 'Linux/SSH server' : 'Kubernetes cluster' }} edge</p>
        </div>
      </div>
      <div class="header-actions">
        <button class="btn" :disabled="loading" @click="load"><RefreshCw :size="14" :class="{ spin: loading }" /> Refresh</button>
        <button class="btn danger" @click="onDelete"><Trash2 :size="14" /> Delete</button>
      </div>
    </header>

    <div v-if="error" class="banner error">{{ error }}</div>
    <div v-else-if="loading && !edge" class="muted pad">Loading…</div>

    <template v-else-if="edge">
      <!-- Overview -->
      <div class="detail-grid">
        <div class="field">
          <span class="lbl">Status</span>
          <span class="status" :class="edge.connected ? 'ok' : 'down'">
            <CircleDot :size="12" /> {{ edge.connected ? 'Connected' : (edge.phase || 'Disconnected') }}
          </span>
        </div>
        <div class="field"><span class="lbl">Agent version</span><span class="mono">{{ edge.agentVersion || '—' }}</span></div>
        <div class="field"><span class="lbl">Hostname</span><span class="mono">{{ edge.hostname || '—' }}</span></div>
        <div class="field"><span class="lbl">Last heartbeat</span><span>{{ rel(edge.lastHeartbeatTime) }}</span></div>
        <div class="field"><span class="lbl">Created</span><span>{{ rel(edge.creationTimestamp) }}</span></div>
        <div class="field"><span class="lbl">Workspace</span><span class="mono">{{ edge.workspacePath || '—' }}</span></div>
      </div>

      <!-- Labels -->
      <div v-if="edge.labels && Object.keys(edge.labels).length" class="section">
        <h3>Labels</h3>
        <div class="chips">
          <span v-for="(v, k) in edge.labels" :key="k" class="pill">{{ k }}={{ v }}</span>
        </div>
      </div>

      <!-- Join instructions (only while not yet connected + a token is present) -->
      <div v-if="!edge.connected && edge.joinToken" class="section">
        <h3>Connect the agent</h3>
        <p class="muted">This edge is waiting for its agent. Run on the target {{ type === 'server' ? 'server' : 'cluster' }}:</p>
        <div class="snippet">
          <div class="snippet-head"><span>kedge agent join</span>
            <button class="copy" @click="copy(`kedge agent join --edge-name ${name} --type ${type} --token ${edge.joinToken}`, 'join')">
              <component :is="copied === 'join' ? Check : Copy" :size="12" /> {{ copied === 'join' ? 'Copied' : 'Copy' }}
            </button>
          </div>
          <pre>kedge agent join --edge-name {{ name }} --type {{ type }} --token {{ edge.joinToken }}</pre>
        </div>
      </div>

      <!-- Kubernetes: how to connect to the cluster. -->
      <div v-if="type === 'kubernetes' && edge.connected" class="section">
        <h3>Connect to this cluster</h3>
        <p class="muted">Download a kubeconfig scoped to this edge and use kubectl through the hub tunnel:</p>
        <div class="snippet">
          <div class="snippet-head"><span>kubectl</span>
            <button class="copy" @click="copy(`kedge kubeconfig edge ${name} > ${name}.kubeconfig\nkubectl --kubeconfig ${name}.kubeconfig get nodes`, 'kube')">
              <component :is="copied === 'kube' ? Check : Copy" :size="12" /> {{ copied === 'kube' ? 'Copied' : 'Copy' }}
            </button>
          </div>
          <pre>kedge kubeconfig edge {{ name }} &gt; {{ name }}.kubeconfig
kubectl --kubeconfig {{ name }}.kubeconfig get nodes</pre>
        </div>
      </div>

      <!-- Server: SSH command + interactive terminal. -->
      <div v-if="type === 'server' && edge.connected" class="section">
        <h3>SSH access</h3>
        <p class="muted">Open an interactive shell in the browser, or SSH from your own terminal:</p>
        <div class="snippet">
          <div class="snippet-head"><span>kedge ssh</span>
            <button class="copy" @click="copy(`kedge ssh ${name}`, 'ssh')">
              <component :is="copied === 'ssh' ? Check : Copy" :size="12" /> {{ copied === 'ssh' ? 'Copied' : 'Copy' }}
            </button>
          </div>
          <pre>kedge ssh {{ name }}</pre>
        </div>
        <div class="wiz-actions" style="justify-content: flex-start;">
          <button class="btn primary" @click="openTerminal">
            <TerminalSquare :size="14" /> Open terminal
          </button>
        </div>
      </div>

      <!-- Services: discovered on server edges, declared on kube edges. -->
      <div class="section">
        <div class="row" style="justify-content: space-between; align-items: baseline;">
          <h3>Services</h3>
          <button v-if="type === 'kubernetes'" class="btn" @click="startAdd"><Plus :size="14" /> Add service</button>
        </div>
        <p class="muted">
          {{ type === 'server'
            ? 'Services discovered running on this host. Attach a token to let AI agents control them.'
            : 'Kubernetes Services on this cluster, reached over cluster DNS. Attach a token to let AI agents control them.' }}
        </p>
        <div v-if="svcError" class="banner error">{{ svcError }}</div>

        <!-- Declare form (kube edges) -->
        <div v-if="adding" class="svc-card">
          <div class="svc-head"><span class="svc-title"><Plus :size="15" /> New service</span></div>
          <div class="svc-form">
            <label>Name<input v-model="draft.name" class="svc-input" placeholder="home-assistant" /></label>
            <label>Type
              <select v-model="draft.serviceType" class="svc-input">
                <option value="home-assistant">Home Assistant</option>
                <option value="generic">Generic (proxy only)</option>
              </select>
            </label>
            <label>Target namespace<input v-model="draft.targetNamespace" class="svc-input" placeholder="home" /></label>
            <label>Target service<input v-model="draft.targetName" class="svc-input" placeholder="home-assistant" /></label>
            <label>Port<input v-model.number="draft.port" type="number" class="svc-input" placeholder="8123" /></label>
          </div>
          <div class="wiz-actions" style="justify-content: flex-start;">
            <button class="btn primary" :disabled="saving || !draftValid" @click="submitAdd">
              {{ saving ? 'Adding…' : 'Add service' }}
            </button>
            <button class="btn" :disabled="saving" @click="adding = false">Cancel</button>
          </div>
        </div>

        <div v-if="services.length === 0 && !adding" class="muted">
          {{ type === 'server'
            ? 'No services discovered yet. Discovery runs when the agent is connected.'
            : 'No services declared yet. Add one to point at a Kubernetes Service in this cluster.' }}
        </div>
        <div v-else-if="services.length" class="svc-cards">
          <div v-for="es in services" :key="es.name" class="svc-card">
            <div class="svc-head">
              <span class="svc-title">
                <Home v-if="es.serviceType === 'home-assistant'" :size="15" />
                <Plug v-else :size="15" />
                {{ es.serviceType === 'home-assistant' ? 'Home Assistant' : (es.serviceType || es.name) }}
              </span>
              <div class="row">
                <span class="status" :class="svcOk(es) ? 'ok' : 'down'">
                  <CircleDot :size="12" /> {{ es.phase || 'Detected' }}
                </span>
                <button v-if="type === 'kubernetes'" class="icon danger" title="Delete service" @click="removeService(es.name)">
                  <Trash2 :size="14" />
                </button>
              </div>
            </div>
            <div class="svc-meta">
              <span v-if="es.version" class="mono">v{{ es.version }}</span>
              <span v-if="es.targetNamespace" class="mono">{{ es.targetName }}.{{ es.targetNamespace }}.svc:{{ es.port }}</span>
              <span v-else class="mono">:{{ es.port }}</span>
              <span v-if="es.installType" class="pill">{{ es.installType }}</span>
              <span v-if="es.hasCredentials" class="pill ok-pill">token set</span>
            </div>

            <!-- Connect form -->
            <div v-if="connectFor === es.name" class="svc-connect">
              <input
                v-model="tokenInput" type="password" class="svc-input"
                placeholder="Paste a long-lived access token" autocomplete="off"
                @keyup.enter="submitConnect"
              />
              <div class="wiz-actions" style="justify-content: flex-start;">
                <button class="btn primary" :disabled="connecting || !tokenInput.trim()" @click="submitConnect">
                  <Plug :size="14" /> {{ connecting ? 'Connecting…' : 'Save token' }}
                </button>
                <button class="btn" :disabled="connecting" @click="connectFor = null">Cancel</button>
              </div>
              <p v-if="es.serviceType === 'home-assistant'" class="muted small">
                Create one in Home Assistant → your profile → Security → Long-lived access tokens.
              </p>
            </div>
            <div v-else class="wiz-actions" style="justify-content: flex-start;">
              <button class="btn" @click="startConnect(es.name)">
                <Plug :size="14" /> {{ es.hasCredentials ? 'Update token' : 'Connect' }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- Conditions -->
      <div class="section">
        <h3>Conditions</h3>
        <div v-if="edge.conditions.length === 0" class="muted">No conditions reported yet.</div>
        <div v-else class="edges-table-wrap">
          <table class="edges-table">
            <thead><tr><th>Type</th><th>Status</th><th>Reason</th><th>Message</th><th>Updated</th></tr></thead>
            <tbody>
              <tr v-for="c in edge.conditions" :key="c.type">
                <td class="name">{{ c.type }}</td>
                <td><span class="status" :class="c.status === 'True' ? 'ok' : 'down'">{{ c.status }}</span></td>
                <td class="muted">{{ c.reason || '—' }}</td>
                <td class="muted">{{ c.message || '—' }}</td>
                <td class="muted">{{ rel(c.lastTransitionTime) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>
  </div>
</template>
