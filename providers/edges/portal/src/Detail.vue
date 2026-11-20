<script setup lang="ts">
import LoadingSkeleton from './portalkit/LoadingSkeleton.vue'
import { useClipboardFeedback } from './portalkit/useClipboardFeedback'
import ResourceDeleteFailure from './portalkit/ResourceDeleteFailure.vue'
import { portalHref } from './portalkit/navigation'
import { ref, computed, watch, onUnmounted, nextTick } from 'vue'
import { Boxes, Cable, Check, ChevronDown, ChevronUp, Cloud, Copy, Cpu, Globe2, Home, Laptop, Plug, Plus, RefreshCw, Server, TerminalSquare } from 'lucide-vue-next'
import { getEdge, deleteEdge, listEdgeServices, connectEdgeService, deleteEdgeService, isContextChangedError } from './api'
import { MACOS_MASKED_JOIN_TOKEN, macosJoinSnippet } from './macos'
import { edgeStatusLabel } from './edge-status'
import { EDGE_DELETE_MESSAGE } from './edge-delete'
import { confirmDialog } from './portalkit/confirm'
import { toast } from './portalkit/toast'
import ConditionsPanel from './portalkit/ConditionsPanel.vue'
import HarnessCard from './HarnessCard.vue'
import ActionMenu, { type ActionMenuItem } from './portalkit/ActionMenu.vue'
import ResourcePage from './portalkit/ResourcePage.vue'
import ResourceTimestamp from './portalkit/ResourceTimestamp.vue'
import ResourceBackLink from './portalkit/ResourceBackLink.vue'
import ResourceTableDeleteButton from './portalkit/ResourceTableDeleteButton.vue'
import ResourceSectionCard from './portalkit/ResourceSectionCard.vue'
import ResourceStatCards, { type ResourceStatCard } from './portalkit/ResourceStatCards.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import InlineNotification from './portalkit/InlineNotification.vue'
import {
  FAST_REFRESH_MS,
  STABLE_REFRESH_MS,
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  type LatestRefreshController,
  type ResourceRefreshMode,
} from './refresh'
import { track, trackFailure } from './telemetry'
import { edgeSupportsHarness, edgeTypeLabel as edgeTypeName, type EdgeDetail, type EdgeService, type EdgeType, type ErrorResponse } from './types'

const props = defineProps<{ name: string; type: EdgeType; cluster: string | null; token: string | null }>()
const emit = defineEmits<{ back: []; deleted: []; addService: [] }>()
let stopped = false
let scopeGeneration = 0
let connectionReported = false

// Fixed telemetry labels for the copyable commands; the text is never sent.
const COPY_COMMANDS: Record<string, string> = {
  join: 'join_cli',
  kube: 'kubectl',
  ssh: 'ssh',
  'up-cli': 'upgrade',
  'up-helm': 'upgrade',
  'up-srv': 'upgrade',
  'up-mac': 'upgrade',
}

// SSH terminals dock at the bottom of the host portal (survives page
// navigation) rather than rendering inline here. The provider is an isolated
// micro-frontend and can't reach the host's Pinia terminal store directly, so
// it dispatches a window-scoped "railgrid-terminal-open" CustomEvent that the
// host TerminalDock listens for (see portal/src/components/TerminalDock.vue).
function openTerminal() {
  if (!props.cluster) return
  window.dispatchEvent(
    new CustomEvent('railgrid-terminal-open', {
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
const edgeUID = computed(() => edge.value?.uid)
const edgeJoinToken = computed(() => edge.value?.joinToken)
const { copiedField: copied, pendingField, errors: copyErrors, copy: writeClipboard } = useClipboardFeedback(
  () => [props.name, props.type, props.cluster, edgeUID.value, edgeJoinToken.value],
)
const servicesExpanded = ref(false)
const technicalExpanded = ref(false)

let detailRefresh!: LatestRefreshController

async function loadEdgeSnapshot(requestID: number) {
  const hadSnapshot = edge.value !== null
  try {
    const nextEdge = await getEdge(props.name, props.type)
    if (!detailRefresh.isCurrent(requestID)) return false
    // The agent connected while this page was open: at most once per edge.
    if (!connectionReported && edge.value && !edge.value.connected && nextEdge.connected) {
      connectionReported = true
      track('edge_connected', { kind: props.type, placement: 'detail' })
    }
    edge.value = nextEdge
    readComplete.value = true
    return true
  } catch (e) {
    if (!detailRefresh.isCurrent(requestID)) return false
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
    return false
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
  const current = edge.value
  const generation = scopeGeneration
  if (!current || deleting.value) return
  const scope = { name: props.name, type: props.type, cluster: props.cluster }
  if (!(await confirmDialog({ title: `Delete ${edgeDeleteLabel.value} "${props.name}"?`, message: EDGE_DELETE_MESSAGE, danger: true, confirmLabel: 'Delete' }))) return
  if (stopped || generation !== scopeGeneration || deleting.value || props.name !== scope.name || props.type !== scope.type || props.cluster !== scope.cluster) return
  if (!edge.value || edge.value.uid !== current.uid || edge.value.phase === 'Deleting') return
  deleting.value = true
  try {
    await deleteEdge(current)
    track('edge_deleted', { kind: scope.type, placement: 'detail' })
    if (stopped || generation !== scopeGeneration) return
    mutationError.value = null
    toast('info', `${edgeMutationLabel.value} deletion requested for ${current.name}.`)
    emit('deleted')
  } catch (e) {
    trackFailure('edge_delete_failed', e, { kind: scope.type, placement: 'detail' })
    if (stopped || generation !== scopeGeneration) return
    if (!isContextChangedError(e)) mutationError.value = (e as ErrorResponse)?.message ?? 'Delete failed'
    deleting.value = false
  }
}

function copyControlLabel(field: string, label: string): string {
  return copied.value === field ? 'Copied' : `Copy ${label}`
}

async function copy(text: string, field: string, label: string) {
  const failure = field === 'join'
    ? 'Could not copy the agent join command. Try copying again; the token may be masked.'
    : `Could not copy the ${label.toLowerCase()}. Select the command and copy it manually.`
  const done = await writeClipboard(text, field, failure)
  if (done && COPY_COMMANDS[field]) track('edge_command_copied', { command: COPY_COMMANDS[field], kind: props.type, placement: 'detail' })
  return done
}

function copyStatus(field: string, label: string): string {
  return copyErrors.value[field] || (copied.value === field ? `${label} copied to clipboard.` : '')
}

const macosJoinDisplay = computed(() => macosJoinSnippet(props.name, props.cluster, MACOS_MASKED_JOIN_TOKEN))
const macosJoinCommand = computed(() => macosJoinSnippet(props.name, props.cluster, edge.value?.joinToken ?? ''))
const joinDisplay = computed(() => props.type === 'macos'
  ? macosJoinDisplay.value
  : `railgrid agent join --edge-name ${props.name} --type ${props.type} --token ${edge.value?.joinToken ?? ''}`)
const joinCommand = computed(() => props.type === 'macos'
  ? macosJoinCommand.value
  : `railgrid agent join --edge-name ${props.name} --type ${props.type} --token ${edge.value?.joinToken ?? ''}`)

// Only the two host kinds run a supervised harness child; a Kubernetes cluster
// edge would need a Deployment and that is deliberately not built.
const harnessSupported = computed(() => edgeSupportsHarness(props.type))

const edgeTypeLabel = computed(() => edgeTypeName(props.type))
const edgeDeleteLabel = computed(() => `${edgeTypeName(props.type)} edge`)
const edgeMutationLabel = computed(() => `${edgeTypeName(props.type)} edge`)
const edgeStatus = computed(() => edgeStatusLabel(edge.value, loading.value, deleting.value))

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
    { label: 'Kind', value: value?.kind || (props.type === 'server' ? 'LinuxServer' : props.type === 'macos' ? 'MacOSServer' : 'KubernetesCluster'), mono: false },
    { label: 'API version', value: value?.apiVersion || 'edges.railgrid.ai/v1alpha1', mono: true },
    { label: 'Workspace', value: value?.workspacePath || '—', mono: true },
    { label: 'Namespace', value: value?.namespace || '—', mono: true },
    { label: 'UID', value: value?.uid || '—', mono: true },
    { label: 'Resource version', value: value?.resourceVersion || '—', mono: true },
    { label: 'Generation', value: value?.generation ?? '—', mono: true },
    { label: 'Created', value: value?.creationTimestamp || '', mono: false },
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
  if (props.type === 'macos') {
    const readyServices = services.value.filter((service) => service.phase === 'Ready').length
    return [
      { label: 'Host access', value: value.connected ? 'Tunnel available' : 'Waiting for agent', mono: false },
      { label: 'Service readiness', value: servicesLoaded.value ? `${readyServices}/${services.value.length} Ready` : 'Loading', mono: false },
      { label: 'Execution', value: 'Service-only host', mono: false },
    ]
  }
  return [
    { label: 'Scheduling labels', value: Object.keys(value.spec.labels ?? {}).length ? `${Object.keys(value.spec.labels ?? {}).length} configured` : 'None configured', mono: false },
    { label: 'Cluster access', value: value.connected ? 'Tunnel available' : 'Waiting for agent', mono: false },
  ]
})

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
const upgradeCliCommand = computed(() => `railgrid agent upgrade ${props.name}`)
const upgradeHelmSnippet = computed(
  () => `helm upgrade railgrid-agent oci://ghcr.io/railgrid/charts/railgrid-agent \\
  --namespace railgrid-agent \\
  --reuse-values \\
  --set image.tag=${targetVersion.value}`,
)
// Both host snippets mirror what `railgrid edge upgrade` prints: the release
// archives are named after uname (kubectl-railgrid_Linux_x86_64, …), the
// installer places the binary at /usr/local/bin/railgrid, the systemd unit is
// always railgrid-edge.service and the launchd label carries the edge name
// (pkg/cli/cmd/agent.go, launchd.go).
const upgradeServerSnippet = computed(
  () => `curl -fsSL "https://github.com/railgrid/railgrid/releases/latest/download/kubectl-railgrid_$(uname -s)_$(uname -m).tar.gz" | tar xz &&
sudo mv kubectl-railgrid /usr/local/bin/railgrid &&
sudo systemctl restart railgrid-edge`,
)
const upgradeMacOSSnippet = computed(
  () => `curl -fsSL "https://github.com/railgrid/railgrid/releases/latest/download/kubectl-railgrid_$(uname -s)_$(uname -m).tar.gz" | tar xz &&
sudo mv kubectl-railgrid /usr/local/bin/railgrid &&
sudo launchctl kickstart -k system/com.railgrid.agent.${props.name}`,
)

// ─── Services ────────────────────────────────────────────────────────
// Linux hosts report discovered services and also accept declared ones (for
// example a local runner on loopback). Kubernetes and MacOS services are only
// declared: a cluster has far more services than a host, and no host is
// treated as a coding worker just because it connects.
const services = ref<EdgeService[]>([])
const servicesLoaded = ref(false)
const svcError = ref<string | null>(null)
const svcMutationErrors = ref<Record<string, { name: string; action: 'delete' | 'connect'; message: string }>>({})
const servicesContent = ref<HTMLElement | null>(null)
const servicesToggle = ref<HTMLButtonElement | null>(null)
const deletingServiceName = ref<string | null>(null)
const connectFor = ref<string | null>(null) // Service name being connected
const tokenInput = ref('')
const connecting = ref(false)

// Read recovery and a completed deletion can remove the focused retry or card.
// Keep focus in this section, including when recovery arrives from polling.
watch([services, svcError, () => connectFor.value], () => {
  if (!edge.value || typeof document === 'undefined') return
  const focused = document.activeElement
  if (!focused || !servicesContent.value?.contains(focused)) return
  const generation = scopeGeneration
  void nextTick(() => {
    if (generation === scopeGeneration && !focused.isConnected && document.activeElement === document.body) servicesToggle.value?.focus()
  })
})
const actionItems = computed<ActionMenuItem[]>(() => [{
  id: 'delete',
  label: deleting.value ? `Deleting ${edgeDeleteLabel.value}…` : `Delete ${edgeDeleteLabel.value}`,
  tone: 'danger',
  disabled: !edge.value || detailRefreshing.value || deleting.value || connecting.value,
  busy: deleting.value,
}])

function selectAction(action: string): void {
  if (action === 'delete') void onDelete()
}

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
    icon: props.type === 'server' ? Server : props.type === 'macos' ? Laptop : Boxes,
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

function retryServiceRead() {
  if (loading.value || deleting.value || connecting.value) return
  void loadServices()
}

function clearServiceMutationError(key: string) {
  const next = { ...svcMutationErrors.value }
  delete next[key]
  svcMutationErrors.value = next
}

async function dismissServiceMutationError(key: string) {
  clearServiceMutationError(key)
  await nextTick()
  servicesToggle.value?.focus()
}

async function removeService(name: string) {
  if (deleting.value || deletingServiceName.value || connecting.value) return
  const uid = services.value.find(service => service.name === name)?.uid
  if (!uid) return
  const focused = typeof document === 'undefined' ? null : document.activeElement
  const scope = { name: props.name, type: props.type, cluster: props.cluster }
  const generation = scopeGeneration
  if (!(await confirmDialog({ title: 'Delete service', message: `"${name}" will be deleted, and its MCP tools will no longer be available.`, danger: true, confirmLabel: 'Delete service' }))) return
  if (stopped || generation !== scopeGeneration || deleting.value || deletingServiceName.value || connecting.value || props.name !== scope.name || props.type !== scope.type || props.cluster !== scope.cluster) return
  deletingServiceName.value = name
  try {
    await deleteEdgeService(name, uid)
    track('service_deleted', { placement: 'edge_detail' })
    if (stopped || generation !== scopeGeneration) return
    clearServiceMutationError(`${name}:delete`)
    clearServiceMutationError(`${name}:connect`)
    toast('info', `Service deletion requested for ${name}.`)
    await loadServices()
    await nextTick()
    if (!stopped && generation === scopeGeneration && focused && !focused.isConnected && document.activeElement === document.body) servicesToggle.value?.focus()
  } catch (e) {
    trackFailure('service_delete_failed', e, { placement: 'edge_detail' })
    if (!stopped && generation === scopeGeneration && !isContextChangedError(e)) svcMutationErrors.value = { ...svcMutationErrors.value, [`${name}:delete`]: { name, action: 'delete', message: (e as ErrorResponse)?.message ?? 'The request failed.' } }
  } finally {
    if (generation === scopeGeneration && deletingServiceName.value === name) deletingServiceName.value = null
  }
}

let connectForUID: string | undefined
function startConnect(name: string) {
  if (connecting.value || deleting.value || deletingServiceName.value) return
  connectForUID = services.value.find(service => service.name === name)?.uid
  if (!connectForUID) return
  connectFor.value = name
  tokenInput.value = ''
}

async function submitConnect() {
  if (!connectFor.value || !connectForUID || !tokenInput.value.trim() || connecting.value || deleting.value || deletingServiceName.value) return
  const name = connectFor.value
  const generation = scopeGeneration
  connecting.value = true
  try {
    await connectEdgeService(name, tokenInput.value.trim(), connectForUID)
    track('service_credentials_saved', { placement: 'edge_detail' })
    if (stopped || generation !== scopeGeneration || connectFor.value !== name) return
    clearServiceMutationError(`${name}:connect`)
    connectFor.value = null
    tokenInput.value = ''
    toast('ok', `Service credentials saved for ${name}.`)
    await loadServices()
  } catch (e) {
    trackFailure('service_credentials_failed', e, { placement: 'edge_detail' })
    if (!stopped && generation === scopeGeneration && connectFor.value === name && !isContextChangedError(e)) svcMutationErrors.value = { ...svcMutationErrors.value, [`${name}:connect`]: { name, action: 'connect', message: (e as ErrorResponse)?.message ?? 'The request failed.' } }
  } finally {
    if (generation === scopeGeneration) connecting.value = false
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
  try {
    const [edgeReadSucceeded] = await Promise.all([loadEdgeSnapshot(requestID), loadServicesSnapshot(requestID)])
    if (edgeReadSucceeded && detailRefresh.isCurrent(requestID)) error.value = null
  } finally {
    if (detailRefresh.isCurrent(requestID)) loading.value = false
    poller.schedule()
  }
})

watch(() => [props.name, props.type, props.cluster], () => {
  scopeGeneration++
  connectionReported = false
  deleting.value = false
  deletingServiceName.value = null
  connecting.value = false
  connectFor.value = null
  tokenInput.value = ''
  mutationError.value = null
  detailRefresh.invalidate()
  edge.value = null
  services.value = []
  readComplete.value = false
  servicesLoaded.value = false
  error.value = null
  svcError.value = null
  svcMutationErrors.value = {}
  void load('foreground')
}, { immediate: true })
onUnmounted(() => {
  stopped = true
  detailRefresh.stop()
  poller.stop()
})

</script>

<template>
  <div class="edge-detail">
    <ResourceBackLink class="edge-detail__back" :href="portalHref('/ui/providers/edges')" @back="emit('back')">
      Edges
    </ResourceBackLink>

    <div class="edge-detail__resource">
      <div class="edge-detail__provider-mark" role="img" :aria-label="`${edgeTypeLabel} icon`">
        <Server v-if="type === 'server'" :size="20" :stroke-width="1.75" aria-hidden="true" />
        <Laptop v-else-if="type === 'macos'" :size="20" :stroke-width="1.75" aria-hidden="true" />
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
              data-track="edge_terminal_open"
              @click="openTerminal"
            >
              <TerminalSquare :size="14" aria-hidden="true" /> Open terminal
            </button>
            <button
              type="button"
              class="k-btn k-btn--ghost"
              aria-label="Refresh edge"
              :disabled="deleting || connecting"
              :aria-disabled="detailRefreshing || undefined"
              :aria-busy="detailRefreshing || undefined"
              @click="refreshDetail"
            >
              <RefreshCw :size="14" :class="{ 'k-spin': detailRefreshing }" aria-hidden="true" />
              {{ detailRefreshing ? 'Refreshing…' : 'Refresh' }}
            </button>
            <ActionMenu
              label="More edge actions"
              :busy="deleting" busy-label="Deleting edge…"
              :items="actionItems"
              :disabled="!edge || deleting || detailRefreshing || connecting"
              @select="selectAction"
            />
          </div>
        </template>

        <template #summary>
          <ResourceStatCards :cards="edgeStatCards" aria-label="Edge summary" />
        </template>

        <template #body>
          <ResourceDeleteFailure v-if="mutationError" :title="`Could not delete edge ${name}`" :message="mutationError" retry-hint="Use Delete to try again." @dismiss="mutationError = null" />
          <div v-if="deleting" class="waiting" role="status">Requesting deletion of this edge…</div>

          <div class="edge-detail__sections">
            <ResourceSectionCard id="edge-connectivity" eyebrow="Access" title="Connectivity" description="Connect the agent, reach the edge, and keep its software current.">
              <template #actions>
                <StatusBadge :status="edgeStatus" :tone="edgeStatusTone" :connected="edge?.connected ?? null" />
              </template>

              <div v-if="edge" class="edge-connectivity-content">
                <!-- Upgrade available: agent behind the hub release (UpgradeAvailable condition). -->
                <div v-if="upgradeAvailable" class="edge-upgrade">
                  <InlineNotification tone="warning" announce="off">
                    <div class="edge-upgrade__notice-content">
                      <span>{{ upgradeCond?.message || 'A newer agent version is available.' }}</span>
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
                  </InlineNotification>

                  <div v-if="showUpgrade" id="edges-upgrade-commands" class="edge-upgrade__commands">
                    <template v-if="type === 'kubernetes'">
                      <div class="snippet">
                        <div class="snippet-head"><span>Option A — CLI</span>
                          <button
                            type="button"
                            class="k-icon-action k-icon-action--quiet"
                            aria-label="Copy CLI upgrade command"
                            :aria-disabled="pendingField !== null || undefined"
                            :aria-busy="pendingField === 'up-cli' || undefined"
                            :data-k-tip="copyControlLabel('up-cli', 'CLI upgrade command')"
                            @click="copy(upgradeCliCommand, 'up-cli', 'CLI upgrade command')"
                          >
                            <component :is="copied === 'up-cli' ? Check : Copy" :size="12" :stroke-width="1.75" aria-hidden="true" />
                          </button>
                        </div>
                        <pre>{{ upgradeCliCommand }}</pre>
                      <p v-if="copyErrors['up-cli'] || copied === 'up-cli'" class="edge-copy-feedback" :class="{ 'edge-copy-feedback--error': copyErrors['up-cli'] }" role="status" aria-live="polite">{{ copyStatus('up-cli', 'CLI upgrade command') }}</p>
                      </div>
                      <div class="snippet">
                        <div class="snippet-head"><span>Option B — Helm</span>
                          <button
                            type="button"
                            class="k-icon-action k-icon-action--quiet"
                            aria-label="Copy Helm upgrade command"
                            :aria-disabled="pendingField !== null || undefined"
                            :aria-busy="pendingField === 'up-helm' || undefined"
                            :data-k-tip="copyControlLabel('up-helm', 'Helm upgrade command')"
                            @click="copy(upgradeHelmSnippet, 'up-helm', 'Helm upgrade command')"
                          >
                            <component :is="copied === 'up-helm' ? Check : Copy" :size="12" :stroke-width="1.75" aria-hidden="true" />
                          </button>
                        </div>
                        <pre>{{ upgradeHelmSnippet }}</pre>
                      <p v-if="copyErrors['up-helm'] || copied === 'up-helm'" class="edge-copy-feedback" :class="{ 'edge-copy-feedback--error': copyErrors['up-helm'] }" role="status" aria-live="polite">{{ copyStatus('up-helm', 'Helm upgrade command') }}</p>
                      </div>
                    </template>

                    <div v-else-if="type === 'server'" class="snippet">
                      <div class="snippet-head"><span>Replace binary and restart</span>
                        <button
                          type="button"
                          class="k-icon-action k-icon-action--quiet"
                          aria-label="Copy server upgrade command"
                          :aria-disabled="pendingField !== null || undefined"
                          :aria-busy="pendingField === 'up-srv' || undefined"
                          :data-k-tip="copyControlLabel('up-srv', 'server upgrade command')"
                          @click="copy(upgradeServerSnippet, 'up-srv', 'server upgrade command')"
                        >
                          <component :is="copied === 'up-srv' ? Check : Copy" :size="12" :stroke-width="1.75" aria-hidden="true" />
                        </button>
                      </div>
                      <pre>{{ upgradeServerSnippet }}</pre>
                      <p v-if="copyErrors['up-srv'] || copied === 'up-srv'" class="edge-copy-feedback" :class="{ 'edge-copy-feedback--error': copyErrors['up-srv'] }" role="status" aria-live="polite">{{ copyStatus('up-srv', 'server upgrade command') }}</p>
                    </div>
                    <div v-else class="snippet">
                      <div class="snippet-head"><span>Replace binary and restart the launchd service</span>
                        <button
                          type="button"
                          class="k-icon-action k-icon-action--quiet"
                          aria-label="Copy macOS upgrade command"
                          :aria-disabled="pendingField !== null || undefined"
                          :aria-busy="pendingField === 'up-mac' || undefined"
                          :data-k-tip="copyControlLabel('up-mac', 'macOS upgrade command')"
                          @click="copy(upgradeMacOSSnippet, 'up-mac', 'macOS upgrade command')"
                        >
                          <component :is="copied === 'up-mac' ? Check : Copy" :size="12" :stroke-width="1.75" aria-hidden="true" />
                        </button>
                      </div>
                      <p class="muted">The restart command assumes the default system LaunchDaemon; custom or user installs need their own label and domain.</p>
                      <pre>{{ upgradeMacOSSnippet }}</pre>
                      <p v-if="copyErrors['up-mac'] || copied === 'up-mac'" class="edge-copy-feedback" :class="{ 'edge-copy-feedback--error': copyErrors['up-mac'] }" role="status" aria-live="polite">{{ copyStatus('up-mac', 'macOS upgrade command') }}</p>
                    </div>

                    <p class="muted">After upgrading, the agent reports its new version on the next heartbeat and this notice clears.</p>
                  </div>
                </div>

                <!-- Join instructions are intentionally collapsed so the credential is not the default read. -->
                <details v-if="!edge.connected && edge.joinToken" class="k-resource-technical">
                  <summary class="k-resource-technical__summary">
                    <span class="k-resource-technical__summary-copy">
                      <span class="k-resource-technical__summary-label">Connect the agent</span>
                      <span class="k-resource-technical__summary-context">Show join command</span>
                    </span>
                    <ChevronDown class="k-resource-technical__chevron" :size="16" aria-hidden="true" />
                  </summary>
                  <div class="k-resource-technical__body edge-disclosure__body">
                    <p class="muted">This edge is waiting for its agent. Run on the target {{ type === 'kubernetes' ? 'cluster' : `${edgeTypeLabel} host` }}:</p>
                    <div class="snippet">
                      <div class="snippet-head"><span>railgrid agent join</span>
                        <button
                          type="button"
                          class="k-icon-action k-icon-action--quiet"
                          aria-label="Copy agent join command"
                          :aria-disabled="pendingField !== null || undefined"
                          :aria-busy="pendingField === 'join' || undefined"
                          :data-k-tip="copyControlLabel('join', 'agent join command')"
                          @click="copy(joinCommand, 'join', 'agent join command')"
                        >
                          <component :is="copied === 'join' ? Check : Copy" :size="12" :stroke-width="1.75" aria-hidden="true" />
                        </button>
                      </div>
                      <pre>{{ joinDisplay }}</pre>
                      <p v-if="copyErrors['join'] || copied === 'join'" class="edge-copy-feedback" :class="{ 'edge-copy-feedback--error': copyErrors['join'] }" role="status" aria-live="polite">{{ copyStatus('join', 'agent join command') }}</p>
                    </div>
                  </div>
                </details>

                <!-- Kubernetes: how to connect to the cluster. -->
                <details v-if="type === 'kubernetes' && edge.connected" class="k-resource-technical">
                  <summary class="k-resource-technical__summary">
                    <span class="k-resource-technical__summary-copy">
                      <span class="k-resource-technical__summary-label">Connect to this cluster</span>
                      <span class="k-resource-technical__summary-context">Show kubectl command</span>
                    </span>
                    <ChevronDown class="k-resource-technical__chevron" :size="16" aria-hidden="true" />
                  </summary>
                  <div class="k-resource-technical__body edge-disclosure__body">
                    <p class="muted">Download a kubeconfig scoped to this edge and use kubectl through the hub tunnel:</p>
                    <div class="snippet">
                      <div class="snippet-head"><span>kubectl</span>
                        <button
                          type="button"
                          class="k-icon-action k-icon-action--quiet"
                          aria-label="Copy kubectl command"
                          :aria-disabled="pendingField !== null || undefined"
                          :aria-busy="pendingField === 'kube' || undefined"
                          :data-k-tip="copyControlLabel('kube', 'kubectl command')"
                          @click="copy(`railgrid edge kubeconfig ${name} > ${name}.kubeconfig\nkubectl --kubeconfig ${name}.kubeconfig get nodes`, 'kube', 'kubectl command')"
                        >
                          <component :is="copied === 'kube' ? Check : Copy" :size="12" :stroke-width="1.75" aria-hidden="true" />
                        </button>
                      </div>
                      <pre>railgrid edge kubeconfig {{ name }} &gt; {{ name }}.kubeconfig
kubectl --kubeconfig {{ name }}.kubeconfig get nodes</pre>
                      <p v-if="copyErrors['kube'] || copied === 'kube'" class="edge-copy-feedback" :class="{ 'edge-copy-feedback--error': copyErrors['kube'] }" role="status" aria-live="polite">{{ copyStatus('kube', 'kubectl command') }}</p>
                    </div>
                  </div>
                </details>

                <!-- Server: SSH command + interactive terminal. -->
                <details v-if="type === 'server' && edge.connected" class="k-resource-technical">
                  <summary class="k-resource-technical__summary">
                    <span class="k-resource-technical__summary-copy">
                      <span class="k-resource-technical__summary-label">SSH access</span>
                      <span class="k-resource-technical__summary-context">Show SSH command</span>
                    </span>
                    <ChevronDown class="k-resource-technical__chevron" :size="16" aria-hidden="true" />
                  </summary>
                  <div class="k-resource-technical__body edge-disclosure__body">
                    <p class="muted">Open an interactive shell in the browser, or SSH from your own terminal:</p>
                    <div class="snippet">
                      <div class="snippet-head"><span>railgrid ssh</span>
                        <button
                          type="button"
                          class="k-icon-action k-icon-action--quiet"
                          aria-label="Copy SSH command"
                          :aria-disabled="pendingField !== null || undefined"
                          :aria-busy="pendingField === 'ssh' || undefined"
                          :data-k-tip="copyControlLabel('ssh', 'SSH command')"
                          @click="copy(`railgrid ssh ${name}`, 'ssh', 'SSH command')"
                        >
                          <component :is="copied === 'ssh' ? Check : Copy" :size="12" :stroke-width="1.75" aria-hidden="true" />
                        </button>
                      </div>
                      <pre>railgrid ssh {{ name }}</pre>
                      <p v-if="copyErrors['ssh'] || copied === 'ssh'" class="edge-copy-feedback" :class="{ 'edge-copy-feedback--error': copyErrors['ssh'] }" role="status" aria-live="polite">{{ copyStatus('ssh', 'SSH command') }}</p>
                    </div>
                  </div>
                </details>
              </div>
            </ResourceSectionCard>

            <!-- Harness: which coding agents this host offers. Writes spec.harness. -->
            <HarnessCard
              v-if="edge && harnessSupported"
              :edge-name="edge.name"
              :edge-type="type"
              :harness="edge.spec.harness ?? null"
              :harnesses="edge.harnesses ?? null"
              :disabled="deleting"
              @changed="load('background')"
            />

            <ResourceSectionCard id="edge-services" eyebrow="Provider services" title="Services" :description="type === 'server' ? 'Services discovered on or declared for this host, such as a local runner. Attach a token to let AI agents control them.' : type === 'macos' ? 'Services declared on this host. Host connectivity and Service readiness are reported separately.' : 'Kubernetes Services on this cluster, reached over cluster DNS. Attach a token to let AI agents control them.'">
              <template #actions>
                <span class="edge-section-card__count"><strong>{{ servicesLoaded ? services.length : '—' }}</strong> {{ servicesLoaded ? (services.length === 1 ? 'service' : 'services') : 'services' }}</span>
                <button
                  ref="servicesToggle"
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

              <div v-if="edge && servicesExpanded" id="edges-services-content" ref="servicesContent" class="edge-services-content">
                <InlineNotification
                  v-if="svcError"
                  :tone="servicesLoaded ? 'warning' : 'error'"
                  :title="servicesLoaded ? 'Could not refresh services' : 'Could not load services'"
                  :message="`${servicesLoaded ? 'Showing the last successful result. ' : ''}${svcError}`"
                  action-label="Retry" action-busy-label="Retrying…"
                  :action-busy="loading || deleting || connecting"
                  @action="retryServiceRead"
                />
                <template v-for="(failure, key) in svcMutationErrors" :key="key">
                  <ResourceDeleteFailure
                    v-if="failure.action === 'delete'"
                    :title="`Could not delete service ${failure.name}`"
                    :message="failure.message"
                    :return-focus="servicesToggle"
                    retry-hint="Use the service’s Delete action to try again."
                    @dismiss="clearServiceMutationError(String(key))"
                  />
                  <InlineNotification
                    v-else
                    tone="error"
                    :title="`Could not save credentials for ${failure.name}`"
                    :message="failure.message"
                    dismissible
                    @dismiss="dismissServiceMutationError(String(key))"
                  />
                </template>

                <div class="edge-services-content__actions">
                  <button
                    type="button"
                    class="k-btn k-btn--ghost"
                    :disabled="deleting"
                    data-track="service_create_open"
                    data-track-placement="edge_detail"
                    @click="emit('addService')"
                  ><Plus :size="14" aria-hidden="true" /> Add service</button>
                </div>

                <LoadingSkeleton v-if="!servicesLoaded && !svcError" label="Loading services…" compact />
                <div v-else-if="servicesLoaded && services.length === 0" class="muted">
                  {{ type === 'server'
                    ? 'No services yet. Discovery runs when the agent is connected, or add one to point at a host-local service such as a runner.'
                    : type === 'macos'
                      ? 'No services declared yet. Add one to point at a host-local service.'
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
                        <ResourceTableDeleteButton
                          v-if="type !== 'server' || !es.discovered"
                          :label="`Delete service ${es.name}`"
                          :busy-label="`Deleting service ${es.name}`"
                          :busy="deletingServiceName === es.name"
                          :disabled="deleting || connecting || (deletingServiceName !== null && deletingServiceName !== es.name)"
                          @click="removeService(es.name)"
                        />
                      </div>
                    </div>
                    <div class="svc-meta">
                      <span v-if="es.version" class="mono">v{{ es.version }}</span>
                      <span v-if="es.targetNamespace" class="mono">{{ es.targetName }}.{{ es.targetNamespace }}.svc:{{ es.port }}</span>
                      <span v-else class="mono">:{{ es.port }}</span>
                      <span v-if="es.installType" class="k-badge k-badge--muted k-badge--label">{{ es.installType }}</span>
                      <StatusBadge v-if="es.hasCredentials" status="token set" tone="success" />
                    </div>

                    <!-- Connect form. -->
                    <div v-if="connectFor === es.name" class="svc-connect" :aria-busy="connecting">
                      <label class="fld" :for="`edge-service-token-${es.name}`">
                        <span class="lbl">Access token</span>
                        <input
                          :id="`edge-service-token-${es.name}`"
                          v-model="tokenInput" type="password" class="svc-input k-input"
                          :aria-describedby="es.serviceType === 'home-assistant' ? `edge-service-token-${es.name}-help` : undefined"
                          :disabled="connecting"
                          placeholder="Paste a long-lived access token" autocomplete="new-password"
                          @keyup.enter="submitConnect"
                        />
                      </label>
                      <div class="wiz-actions" style="justify-content: flex-start;">
                        <button type="button" class="k-btn k-btn--primary" :disabled="connecting || !tokenInput.trim()" @click="submitConnect">
                          <Plug :size="14" aria-hidden="true" /> {{ connecting ? 'Connecting…' : 'Save token' }}
                        </button>
                        <button type="button" class="k-btn k-btn--ghost" :disabled="connecting" @click="connectFor = null">Cancel</button>
                      </div>
                      <p v-if="es.serviceType === 'home-assistant'" :id="`edge-service-token-${es.name}-help`" class="muted small">
                        Create one in Home Assistant → your profile → Security → Long-lived access tokens.
                      </p>
                    </div>
                    <div v-else class="wiz-actions" style="justify-content: flex-start;">
                      <button class="k-btn k-btn--ghost" type="button" :disabled="deleting || connecting || deletingServiceName !== null" @click="startConnect(es.name)">
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
                          <dd :class="{ mono: row.mono }"><ResourceTimestamp v-if="row.label === 'Created'" :value="String(row.value || '')" label="Edge created" /><template v-else>{{ row.value }}</template></dd>
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
                        <div><dt>Last heartbeat</dt><dd><ResourceTimestamp :value="edge.lastHeartbeatTime" label="Last heartbeat" /></dd></div>
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
                          <dd :class="{ mono: row.mono }"><ResourceTimestamp v-if="row.label === 'Created'" :value="String(row.value || '')" label="Edge created" /><template v-else>{{ row.value }}</template></dd>
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
