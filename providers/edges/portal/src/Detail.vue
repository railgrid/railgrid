<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import { ArrowUpCircle, Boxes, Cable, Check, ChevronDown, ChevronUp, Cloud, Copy, Cpu, Ellipsis, Globe2, Home, Plug, Plus, RefreshCw, Server, TerminalSquare, Trash2 } from 'lucide-vue-next'
import { getEdge, deleteEdge, listEdgeServices, connectEdgeService, deleteEdgeService } from './api'
import { confirmDialog } from './portalkit/confirm'
import ConditionsPanel from './portalkit/ConditionsPanel.vue'
import ResourcePage from './portalkit/ResourcePage.vue'
import ResourceBackLink from './portalkit/ResourceBackLink.vue'
import ResourceSectionCard from './portalkit/ResourceSectionCard.vue'
import ResourceStatCards, { type ResourceStatCard } from './portalkit/ResourceStatCards.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import {
  FAST_REFRESH_MS,
  STABLE_REFRESH_MS,
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  type LatestRefreshController,
  type ResourceRefreshMode,
} from './refresh'
import type { EdgeDetail, EdgeService, EdgeType, ErrorResponse } from './types'

const props = defineProps<{ name: string; type: EdgeType; cluster: string | null; token: string | null }>()
const emit = defineEmits<{ back: []; deleted: []; addService: [] }>()

// SSH terminals dock at the bottom of the host portal (survives page
// navigation) rather than rendering inline here. The provider is an isolated
// micro-frontend and can't reach the host's Pinia terminal store directly, so
// it dispatches a window-scoped "faros-terminal-open" CustomEvent that the
// host TerminalDock listens for (see portal/src/components/TerminalDock.vue).
function openTerminal() {
  if (!props.cluster) return
  window.dispatchEvent(
    new CustomEvent('faros-terminal-open', {
      detail: { edgeName: props.name, cluster: props.cluster, displayName: props.name },
    }),
  )
}

const edge = ref<EdgeDetail | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const readComplete = ref(false)
const deleting = ref(false)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const detailRefreshing = computed(() => loading.value && refreshMode.value === 'foreground')
const mutationError = ref<string | null>(null)
const copied = ref<string | null>(null)
const actionsMenu = ref<HTMLDetailsElement | null>(null)
const servicesExpanded = ref(false)
const technicalExpanded = ref(false)

let detailRefresh!: LatestRefreshController

async function loadEdgeSnapshot(requestID: number) {
  const hadSnapshot = edge.value !== null
  try {
    const nextEdge = await getEdge(props.name, props.type)
    if (!detailRefresh.isCurrent(requestID)) return
    edge.value = nextEdge
    readComplete.value = true
    error.value = null
  } catch (e) {
    if (!detailRefresh.isCurrent(requestID)) return
    const response = e as ErrorResponse
    if (response?.reason === 'NotFound') {
      // A confirmed not-found must not leave an old snapshot looking current.
      edge.value = null
      readComplete.value = false
      error.value = 'This edge is no longer available.'
    } else {
      // Keep the last successful snapshot visible when a refresh fails.
      readComplete.value = hadSnapshot
      error.value = response?.message ?? 'Failed to load edge'
    }
  }
}

function load(mode: ResourceRefreshMode | Event = 'foreground') {
  const requestedMode = typeof mode === 'string' ? mode : 'foreground'
  if (requestedMode === 'foreground') {
    refreshMode.value = 'foreground'
    loading.value = true
  }
  return detailRefresh.request(requestedMode)
}

// The header Refresh represents the complete Edge detail snapshot: refresh
// the authoritative edge and its provider-owned service catalog together.
// Retry uses the same complete snapshot so the summary and service section do
// not disagree after an explicit recovery.
async function refreshDetail() {
  if (deleting.value || detailRefreshing.value || connecting.value) return
  await load('foreground')
}

async function onDelete() {
  if (!edge.value || deleting.value) return
  actionsMenu.value?.removeAttribute('open')
  if (!(await confirmDialog({ title: `Delete ${props.type === 'server' ? 'server' : 'cluster'} "${props.name}"?`, danger: true, confirmLabel: 'Delete' }))) return
  deleting.value = true
  mutationError.value = null
  try {
    await deleteEdge(edge.value)
    emit('deleted')
  } catch (e) {
    mutationError.value = (e as ErrorResponse)?.message ?? 'Delete failed'
    deleting.value = false
  }
}

async function copy(text: string, field: string) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = field
    setTimeout(() => (copied.value = null), 2000)
  } catch { /* clipboard denied */ }
}

const edgeTypeLabel = computed(() => props.type === 'server' ? 'Linux server' : 'Kubernetes cluster')
const edgeStatus = computed(() => {
  if (deleting.value) return 'Deleting'
  if (!edge.value) return loading.value ? 'Loading' : 'Unavailable'
  return edge.value.connected ? 'Connected' : (edge.value.phase || 'Disconnected')
})

function statusTone(status: string): 'success' | 'warning' | 'danger' | 'muted' {
  const normalized = status.toLowerCase()
  if (normalized === 'connected' || normalized === 'ready' || normalized === 'active') return 'success'
  if (normalized === 'deleting' || normalized === 'pending' || normalized === 'provisioning' || normalized === 'loading') return 'warning'
  if (normalized === 'disconnected' || normalized === 'unavailable' || normalized === 'failed' || normalized === 'error') return 'danger'
  return 'muted'
}

const edgeStatusTone = computed(() => statusTone(edgeStatus.value))

const metadataRows = computed(() => {
  const value = edge.value
  return [
    { label: 'Resource name', value: value?.name || props.name, mono: true },
    { label: 'Kind', value: value?.kind || (props.type === 'server' ? 'LinuxServer' : 'KubernetesCluster'), mono: false },
    { label: 'API version', value: value?.apiVersion || 'edges.faros.sh/v1alpha1', mono: true },
    { label: 'Workspace', value: value?.workspacePath || '—', mono: true },
    { label: 'Namespace', value: value?.namespace || '—', mono: true },
    { label: 'UID', value: value?.uid || '—', mono: true },
    { label: 'Resource version', value: value?.resourceVersion || '—', mono: true },
    { label: 'Generation', value: value?.generation ?? '—', mono: true },
    { label: 'Created', value: value?.creationTimestamp ? formatTimestamp(value.creationTimestamp) : '—', mono: false },
  ]
})

const labelRows = computed(() => Object.entries(edge.value?.labels ?? {}).sort(([a], [b]) => a.localeCompare(b)))

const configurationRows = computed(() => {
  const value = edge.value
  if (!value) return []
  if (props.type === 'server') {
    return [
      { label: 'SSH port', value: value.spec.sshPort ?? 22, mono: true },
      { label: 'SSH user mapping', value: value.spec.sshUserMapping || 'Inherited', mono: false },
      { label: 'SSH credentials', value: value.spec.sshCredentialsRef?.name || 'Agent-managed', mono: true },
    ]
  }
  return [
    { label: 'Scheduling labels', value: Object.keys(value.spec.labels ?? {}).length ? `${Object.keys(value.spec.labels ?? {}).length} configured` : 'None configured', mono: false },
    { label: 'Cluster access', value: value.connected ? 'Tunnel available' : 'Waiting for agent', mono: false },
  ]
})

function formatTimestamp(value: string): string {
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString()
}

function yamlScalar(value: unknown): string {
  if (value === null) return 'null'
  if (typeof value === 'string') return JSON.stringify(value)
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  return JSON.stringify(value)
}

function toYaml(value: unknown, indent = 0): string {
  const padding = ' '.repeat(indent)
  if (Array.isArray(value)) {
    return value.map(item => {
      if (item && typeof item === 'object') return `${padding}-\n${toYaml(item, indent + 2)}`
      return `${padding}- ${yamlScalar(item)}`
    }).join('\n')
  }
  if (value && typeof value === 'object') {
    return Object.entries(value as Record<string, unknown>)
      .filter(([, child]) => child !== undefined)
      .map(([key, child]) => {
        if (child && typeof child === 'object' && Object.keys(child as object).length > 0) {
          return `${padding}${key}:\n${toYaml(child, indent + 2)}`
        }
        return `${padding}${key}: ${yamlScalar(child)}`
      }).join('\n')
  }
  return `${padding}${yamlScalar(value)}`
}

const edgeYaml = computed(() => edge.value ? toYaml(edge.value.rawObject) : '')

function serviceTone(service: EdgeService): 'success' | 'warning' | 'danger' | 'muted' {
  if (service.phase === 'Ready') return 'success'
  if (service.phase === 'Failed' || service.phase === 'Error') return 'danger'
  return 'warning'
}

// ─── Upgrade ─────────────────────────────────────────────────────────
// The version reconciler stamps an UpgradeAvailable condition (status True)
// when the agent is behind the hub release, embedding the target version in the
// message ("upgrade available to <version>."). We read that condition rather
// than comparing versions client-side.
const showUpgrade = ref(false)
const upgradeCond = computed(() =>
  edge.value?.conditions.find((c) => c.type === 'UpgradeAvailable' && c.status === 'True'),
)
const upgradeAvailable = computed(() => !!upgradeCond.value)
const targetVersion = computed(() => {
  const m = upgradeCond.value?.message?.match(/upgrade available to (\S+?)\.?$/)
  return m?.[1] ?? 'latest'
})
const upgradeCliCommand = computed(() => `faros agent upgrade ${props.name}`)
const upgradeHelmSnippet = computed(
  () => `helm upgrade faros-agent oci://ghcr.io/faroshq/charts/faros-agent \\
  --namespace faros-agent \\
  --reuse-values \\
  --set agent.image.tag=${targetVersion.value}`,
)
const upgradeServerSnippet = computed(
  () => `curl -fsSL https://github.com/faroshq/faros/releases/latest/download/kubectl-faros_linux_amd64.tar.gz | tar xz
sudo mv kubectl-faros /usr/local/bin/faros
sudo systemctl restart faros-agent-${props.name}`,
)

// ─── Services ────────────────────────────────────────────────────────
// Server edges: discovered by the agent. Kube edges: declared here (a cluster
// has far more services than a host, so we don't auto-scan).
const services = ref<EdgeService[]>([])
const servicesLoaded = ref(false)
const svcError = ref<string | null>(null)
const connectFor = ref<string | null>(null) // Service name being connected
const tokenInput = ref('')
const connecting = ref(false)

const servicesCardValue = computed(() => servicesLoaded.value ? String(services.value.length) : '—')
const servicesCardDetail = computed(() => {
  if (svcError.value) return servicesLoaded.value ? 'Stale' : 'Unavailable'
  if (!servicesLoaded.value) return 'Loading'
  return services.value.length === 1 ? 'service' : 'services'
})
const edgeStatCards = computed<ResourceStatCard[]>(() => [
  {
    id: 'connection',
    label: 'Connection',
    value: edgeStatus.value,
    icon: Cable,
    tone: edgeStatusTone.value === 'muted' ? 'default' : edgeStatusTone.value,
  },
  {
    id: 'provider',
    label: 'Provider',
    value: 'Edges',
    icon: Cloud,
  },
  {
    id: 'type',
    label: 'Type',
    value: edgeTypeLabel.value,
    icon: props.type === 'server' ? Server : Boxes,
  },
  {
    id: 'hostname',
    label: 'Hostname',
    value: edge.value?.hostname || '—',
    icon: Globe2,
    mono: true,
  },
  {
    id: 'agent',
    label: 'Agent version',
    value: edge.value?.agentVersion || '—',
    detail: upgradeAvailable.value ? 'Upgrade available' : undefined,
    icon: Cpu,
    tone: upgradeAvailable.value ? 'warning' : 'default',
    mono: true,
  },
  {
    id: 'services',
    label: 'Services',
    value: servicesCardValue.value,
    detail: servicesCardDetail.value,
    icon: Plug,
  },
])

async function loadServicesSnapshot(requestID: number) {
  try {
    const nextServices = await listEdgeServices(props.name)
    if (!detailRefresh.isCurrent(requestID)) return
    services.value = nextServices
    servicesLoaded.value = true
    svcError.value = null
  } catch (e) {
    if (!detailRefresh.isCurrent(requestID)) return
    svcError.value = (e as ErrorResponse)?.message ?? 'Failed to load services'
  }
}

function loadServices() {
  return load('foreground')
}

async function removeService(name: string) {
  if (!(await confirmDialog({ title: `Delete service "${name}"?`, danger: true, confirmLabel: 'Delete' }))) return
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

const poller = createAdaptiveRefreshTimer(() => {
  void load('background')
}, () => {
  if (!readComplete.value || error.value || svcError.value || !edge.value) return FAST_REFRESH_MS
  const phase = (edge.value.phase || '').toLowerCase()
  const unsettledEdge = !edge.value.connected || ['pending', 'provisioning', 'deleting'].includes(phase)
  const unsettledService = services.value.some(service => (service.phase || 'Pending').toLowerCase() !== 'ready')
  return unsettledEdge || unsettledService ? FAST_REFRESH_MS : STABLE_REFRESH_MS
})

detailRefresh = createLatestRefreshController(async (requestID, mode) => {
  if (deleting.value) return
  refreshMode.value = mode
  loading.value = true
  if (mode === 'foreground') {
    error.value = null
    svcError.value = null
  }
  try {
    await Promise.all([loadEdgeSnapshot(requestID), loadServicesSnapshot(requestID)])
  } finally {
    if (detailRefresh.isCurrent(requestID)) loading.value = false
    poller.schedule()
  }
})

watch(() => [props.name, props.type], () => {
  detailRefresh.invalidate()
  edge.value = null
  services.value = []
  readComplete.value = false
  servicesLoaded.value = false
  error.value = null
  svcError.value = null
  void load('foreground')
}, { immediate: true })
onUnmounted(() => {
  detailRefresh.stop()
  poller.stop()
})

</script>

<template>
  <div class="edge-detail">
    <ResourceBackLink class="edge-detail__back" href="/ui/providers/edges" @back="emit('back')">
      Edges
    </ResourceBackLink>

    <div class="edge-detail__resource">
      <div class="edge-detail__provider-mark" role="img" :aria-label="`${edgeTypeLabel} icon`">
        <Server v-if="type === 'server'" :size="20" :stroke-width="1.75" aria-hidden="true" />
        <Boxes v-else :size="20" :stroke-width="1.75" aria-hidden="true" />
      </div>

      <ResourcePage
        :title="name"
        :kind="edgeTypeLabel"
        :loaded="readComplete"
        :loading="loading"
        :refresh-mode="refreshMode"
        :error="error"
        :stale="Boolean(edge && error)"
        retryable
        @retry="load"
      >
        <template #meta>
          <span>Edges</span>
          <template v-if="edge?.hostname">
            <span class="edge-header__separator" aria-hidden="true">·</span>
            <span class="mono">{{ edge.hostname }}</span>
          </template>
        </template>
        <template #status>
          <StatusBadge :status="edgeStatus" :tone="edgeStatusTone" :connected="edge?.connected ?? null" />
        </template>

        <template #actions>
          <div class="edge-detail__actions" role="group" aria-label="Edge actions">
            <button
              v-if="type === 'server' && edge?.connected"
              class="k-btn k-btn--primary"
              type="button"
              :disabled="deleting"
              @click="openTerminal"
            >
              <TerminalSquare :size="14" aria-hidden="true" /> Open terminal
            </button>
            <button
              type="button"
              class="k-btn k-btn--ghost"
              :disabled="detailRefreshing || deleting || connecting"
              :aria-busy="detailRefreshing || undefined"
              @click="refreshDetail"
            >
              <RefreshCw :size="14" :class="{ spin: detailRefreshing }" aria-hidden="true" />
              {{ detailRefreshing ? 'Refreshing…' : 'Refresh' }}
            </button>
            <details ref="actionsMenu" class="edge-detail__menu">
              <summary class="k-btn k-btn--ghost" aria-label="More edge actions">
                <Ellipsis :size="16" aria-hidden="true" />
                <span class="sr-only">More actions</span>
              </summary>
              <div class="edge-detail__menu-popover">
                <button
                  type="button"
                  class="edge-detail__menu-item"
                  :disabled="!edge || deleting || detailRefreshing || connecting"
                  @click="onDelete"
                >
                  Delete {{ type === 'server' ? 'server' : 'cluster' }}
                </button>
              </div>
            </details>
          </div>
        </template>

        <template #summary>
          <ResourceStatCards :cards="edgeStatCards" aria-label="Edge summary" />
        </template>

        <template #body>
          <div v-if="mutationError" class="banner error" role="alert">{{ mutationError }}</div>
          <div v-if="deleting" class="waiting" role="status">Deleting this edge. The last successful snapshot remains visible until the hub confirms removal.</div>

          <div class="edge-detail__sections">
            <ResourceSectionCard id="edge-connectivity" eyebrow="Access" title="Connectivity" description="Connect the agent, reach the edge, and keep its software current.">
              <template #actions>
                <StatusBadge :status="edgeStatus" :tone="edgeStatusTone" :connected="edge?.connected ?? null" />
              </template>

              <div v-if="edge" class="edge-connectivity-content">
                <!-- Upgrade available: agent behind the hub release (UpgradeAvailable condition). -->
                <div v-if="upgradeAvailable" class="edge-upgrade">
                  <div class="banner warn edge-upgrade__banner">
                    <span class="row">
                      <ArrowUpCircle :size="14" aria-hidden="true" />
                      {{ upgradeCond?.message || 'A newer agent version is available.' }}
                    </span>
                    <button
                      type="button"
                      class="k-btn k-btn--ghost compact-control"
                      :aria-expanded="showUpgrade"
                      aria-controls="edges-upgrade-commands"
                      @click="showUpgrade = !showUpgrade"
                    >
                      {{ showUpgrade ? 'Hide' : 'Show' }} commands
                      <component :is="showUpgrade ? ChevronUp : ChevronDown" :size="14" aria-hidden="true" />
                    </button>
                  </div>

                  <div v-if="showUpgrade" id="edges-upgrade-commands" class="edge-upgrade__commands">
                    <template v-if="type === 'kubernetes'">
                      <div class="snippet k-card">
                        <div class="snippet-head"><span>Option A — CLI</span>
                          <button class="copy" @click="copy(upgradeCliCommand, 'up-cli')">
                            <component :is="copied === 'up-cli' ? Check : Copy" :size="12" aria-hidden="true" /> {{ copied === 'up-cli' ? 'Copied' : 'Copy' }}
                          </button>
                        </div>
                        <pre>{{ upgradeCliCommand }}</pre>
                      </div>
                      <div class="snippet k-card">
                        <div class="snippet-head"><span>Option B — Helm</span>
                          <button class="copy" @click="copy(upgradeHelmSnippet, 'up-helm')">
                            <component :is="copied === 'up-helm' ? Check : Copy" :size="12" aria-hidden="true" /> {{ copied === 'up-helm' ? 'Copied' : 'Copy' }}
                          </button>
                        </div>
                        <pre>{{ upgradeHelmSnippet }}</pre>
                      </div>
                    </template>

                    <div v-else class="snippet k-card">
                      <div class="snippet-head"><span>Replace binary and restart</span>
                        <button class="copy" @click="copy(upgradeServerSnippet, 'up-srv')">
                          <component :is="copied === 'up-srv' ? Check : Copy" :size="12" aria-hidden="true" /> {{ copied === 'up-srv' ? 'Copied' : 'Copy' }}
                        </button>
                      </div>
                      <pre>{{ upgradeServerSnippet }}</pre>
                    </div>

                    <p class="muted">After upgrading, the agent reports its new version on the next heartbeat and this notice clears.</p>
                  </div>
                </div>

                <!-- Join instructions are intentionally collapsed so the credential is not the default read. -->
                <details v-if="!edge.connected && edge.joinToken" class="edge-disclosure">
                  <summary>
                    <span>Connect the agent</span>
                    <span class="edge-disclosure__hint">Show join command</span>
                  </summary>
                  <div class="edge-disclosure__body">
                    <p class="muted">This edge is waiting for its agent. Run on the target {{ type === 'server' ? 'server' : 'cluster' }}:</p>
                    <div class="snippet k-card">
                      <div class="snippet-head"><span>faros agent join</span>
                        <button class="copy" @click="copy(`faros agent join --edge-name ${name} --type ${type} --token ${edge.joinToken}`, 'join')">
                          <component :is="copied === 'join' ? Check : Copy" :size="12" aria-hidden="true" /> {{ copied === 'join' ? 'Copied' : 'Copy' }}
                        </button>
                      </div>
                      <pre>faros agent join --edge-name {{ name }} --type {{ type }} --token {{ edge.joinToken }}</pre>
                    </div>
                  </div>
                </details>

                <!-- Kubernetes: how to connect to the cluster. -->
                <details v-if="type === 'kubernetes' && edge.connected" class="edge-disclosure">
                  <summary>
                    <span>Connect to this cluster</span>
                    <span class="edge-disclosure__hint">Show kubectl command</span>
                  </summary>
                  <div class="edge-disclosure__body">
                    <p class="muted">Download a kubeconfig scoped to this edge and use kubectl through the hub tunnel:</p>
                    <div class="snippet k-card">
                      <div class="snippet-head"><span>kubectl</span>
                        <button class="copy" @click="copy(`faros kubeconfig edge ${name} > ${name}.kubeconfig\nkubectl --kubeconfig ${name}.kubeconfig get nodes`, 'kube')">
                          <component :is="copied === 'kube' ? Check : Copy" :size="12" aria-hidden="true" /> {{ copied === 'kube' ? 'Copied' : 'Copy' }}
                        </button>
                      </div>
                      <pre>faros kubeconfig edge {{ name }} &gt; {{ name }}.kubeconfig
kubectl --kubeconfig {{ name }}.kubeconfig get nodes</pre>
                    </div>
                  </div>
                </details>

                <!-- Server: SSH command + interactive terminal. -->
                <details v-if="type === 'server' && edge.connected" class="edge-disclosure">
                  <summary>
                    <span>SSH access</span>
                    <span class="edge-disclosure__hint">Show SSH command</span>
                  </summary>
                  <div class="edge-disclosure__body">
                    <p class="muted">Open an interactive shell in the browser, or SSH from your own terminal:</p>
                    <div class="snippet k-card">
                      <div class="snippet-head"><span>faros ssh</span>
                        <button class="copy" @click="copy(`faros ssh ${name}`, 'ssh')">
                          <component :is="copied === 'ssh' ? Check : Copy" :size="12" aria-hidden="true" /> {{ copied === 'ssh' ? 'Copied' : 'Copy' }}
                        </button>
                      </div>
                      <pre>faros ssh {{ name }}</pre>
                    </div>
                  </div>
                </details>
              </div>
            </ResourceSectionCard>

            <ResourceSectionCard id="edge-services" eyebrow="Provider services" title="Services" :description="type === 'server' ? 'Services discovered running on this host. Attach a token to let AI agents control them.' : 'Kubernetes Services on this cluster, reached over cluster DNS. Attach a token to let AI agents control them.'">
              <template #actions>
                <span class="edge-section-card__count"><strong>{{ servicesLoaded ? services.length : '—' }}</strong> {{ servicesLoaded ? (services.length === 1 ? 'service' : 'services') : 'services' }}</span>
                <button
                  type="button"
                  class="k-btn k-btn--ghost"
                  :disabled="!edge"
                  :aria-expanded="servicesExpanded"
                  aria-controls="edges-services-content"
                  @click="servicesExpanded = !servicesExpanded"
                >
                  {{ servicesExpanded ? 'Hide services' : 'Manage services' }}
                  <component :is="servicesExpanded ? ChevronUp : ChevronDown" :size="14" aria-hidden="true" />
                </button>
              </template>

              <div v-if="edge && servicesExpanded" id="edges-services-content" class="edge-services-content">
                <div v-if="svcError" class="banner error" role="alert">{{ svcError }}</div>

                <div v-if="type === 'kubernetes'" class="edge-services-content__actions">
                  <button
                    type="button"
                    class="k-btn k-btn--ghost"
                    :disabled="deleting"
                    @click="emit('addService')"
                  ><Plus :size="14" aria-hidden="true" /> Add service</button>
                </div>

                <div v-if="!servicesLoaded && !svcError" class="muted" role="status" aria-live="polite">
                  Loading services…
                </div>
                <div v-else-if="servicesLoaded && services.length === 0" class="muted">
                  {{ type === 'server'
                    ? 'No services discovered yet. Discovery runs when the agent is connected.'
                    : 'No services declared yet. Add one to point at a Kubernetes Service in this cluster.' }}
                </div>
                <div v-else-if="servicesLoaded && services.length" class="svc-cards">
                  <div v-for="es in services" :key="es.name" class="svc-card k-card">
                    <div class="svc-head">
                      <span class="svc-title">
                        <Home v-if="es.serviceType === 'home-assistant'" :size="15" aria-hidden="true" />
                        <Plug v-else :size="15" aria-hidden="true" />
                        {{ es.serviceType === 'home-assistant' ? 'Home Assistant' : (es.serviceType || es.name) }}
                      </span>
                      <div class="row">
                        <StatusBadge :status="es.phase || 'Detected'" :tone="serviceTone(es)" />
                        <button v-if="type === 'kubernetes'" class="k-table-action k-table-action--delete" type="button" title="Delete service" :disabled="deleting" @click="removeService(es.name)">
                          <Trash2 :size="14" aria-hidden="true" />
                        </button>
                      </div>
                    </div>
                    <div class="svc-meta">
                      <span v-if="es.version" class="mono">v{{ es.version }}</span>
                      <span v-if="es.targetNamespace" class="mono">{{ es.targetName }}.{{ es.targetNamespace }}.svc:{{ es.port }}</span>
                      <span v-else class="mono">:{{ es.port }}</span>
                      <span v-if="es.installType" class="k-badge k-badge--muted">{{ es.installType }}</span>
                      <span v-if="es.hasCredentials" class="k-badge k-badge--success">token set</span>
                    </div>

                    <!-- Connect form. -->
                    <div v-if="connectFor === es.name" class="svc-connect">
                      <input
                        v-model="tokenInput" type="password" class="svc-input k-input"
                        placeholder="Paste a long-lived access token" autocomplete="off"
                        @keyup.enter="submitConnect"
                      />
                      <div class="wiz-actions" style="justify-content: flex-start;">
                        <button class="k-btn k-btn--primary" :disabled="connecting || !tokenInput.trim()" @click="submitConnect">
                          <Plug :size="14" aria-hidden="true" /> {{ connecting ? 'Connecting…' : 'Save token' }}
                        </button>
                        <button class="k-btn k-btn--ghost" :disabled="connecting" @click="connectFor = null">Cancel</button>
                      </div>
                      <p v-if="es.serviceType === 'home-assistant'" class="muted small">
                        Create one in Home Assistant → your profile → Security → Long-lived access tokens.
                      </p>
                    </div>
                    <div v-else class="wiz-actions" style="justify-content: flex-start;">
                      <button class="k-btn k-btn--ghost" type="button" :disabled="deleting" @click="startConnect(es.name)">
                        <Plug :size="14" aria-hidden="true" /> {{ es.hasCredentials ? 'Update token' : 'Connect' }}
                      </button>
                    </div>
                  </div>
                </div>
              </div>
            </ResourceSectionCard>

            <ResourceSectionCard id="edge-technical" eyebrow="Diagnostics" title="Technical details" description="Configuration, health, metadata, and the read-only object snapshot.">
              <template #actions>
                <button
                  type="button"
                  class="k-btn k-btn--ghost"
                  :disabled="!edge"
                  :aria-expanded="technicalExpanded"
                  aria-controls="edges-technical-content"
                  @click="technicalExpanded = !technicalExpanded"
                >
                  {{ technicalExpanded ? 'Hide technical details' : 'Show technical details' }}
                  <component :is="technicalExpanded ? ChevronUp : ChevronDown" :size="14" aria-hidden="true" />
                </button>
              </template>

              <div v-if="edge && technicalExpanded" id="edges-technical-content" class="edge-technical">
                <div class="k-resource-technical__body">
                  <section class="k-resource-technical__section">
                    <h3 class="k-resource-technical__section-title">Configuration</h3>
                    <div class="k-resource-technical__content">
                      <dl class="k-resource-technical__definition">
                        <div v-for="row in configurationRows" :key="row.label">
                          <dt>{{ row.label }}</dt>
                          <dd :class="{ mono: row.mono }">{{ row.value }}</dd>
                        </div>
                      </dl>
                    </div>
                  </section>

                  <section class="k-resource-technical__section">
                    <h3 class="k-resource-technical__section-title">Health data</h3>
                    <div class="k-resource-technical__content">
                      <ConditionsPanel
                        :conditions="edge.conditions"
                        :generation="edge.generation"
                        :observed-generation="edge.observedGeneration"
                        empty-text="No health conditions reported yet."
                      />
                      <dl class="k-resource-technical__definition" style="margin-top: 12px;">
                        <div><dt>Last heartbeat</dt><dd>{{ edge.lastHeartbeatTime ? formatTimestamp(edge.lastHeartbeatTime) : '—' }}</dd></div>
                        <div><dt>Agent endpoint</dt><dd class="mono">{{ edge.statusURL || '—' }}</dd></div>
                      </dl>
                    </div>
                  </section>

                  <section class="k-resource-technical__section">
                    <h3 class="k-resource-technical__section-title">Metadata</h3>
                    <div class="k-resource-technical__content">
                      <dl class="k-resource-technical__definition">
                        <div v-for="row in metadataRows" :key="row.label">
                          <dt>{{ row.label }}</dt>
                          <dd :class="{ mono: row.mono }">{{ row.value }}</dd>
                        </div>
                      </dl>
                      <template v-if="labelRows.length">
                        <p class="k-resource-technical__section-title" style="margin-top: 14px;">Labels</p>
                        <dl class="k-resource-technical__definition k-resource-technical__labels">
                          <template v-for="([key, value]) in labelRows" :key="key">
                            <dt>{{ key }}</dt>
                            <dd class="mono">{{ value }}</dd>
                          </template>
                        </dl>
                      </template>
                    </div>
                  </section>

                  <section class="k-resource-technical__section">
                    <h3 class="k-resource-technical__section-title">YAML / read-only object</h3>
                    <div class="k-resource-technical__content">
                      <p class="muted">Read-only snapshot from the latest successful object read.</p>
                      <pre class="k-resource-technical__pre">{{ edgeYaml || 'No object snapshot available.' }}</pre>
                    </div>
                  </section>
                </div>
              </div>
            </ResourceSectionCard>
          </div>
        </template>
      </ResourcePage>
    </div>
  </div>
</template>
