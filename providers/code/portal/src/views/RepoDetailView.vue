<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { AlertTriangle, ArrowLeftRight, ExternalLink, Eye, GitBranch, Github, Package as PackageIcon, PackageOpen, Plug, RefreshCw, User, Users, X } from 'lucide-vue-next'
import { api } from '../api'
import type { Collaborator, Connection, DeployKey, ErrorResponse, Package, RepositoryDetail } from '../types'
import ActionMenu, { type ActionMenuItem } from '../portalkit/ActionMenu.vue'
import ConditionsPanel from '../portalkit/ConditionsPanel.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import ResourceStatCards, { type ResourceStatCard } from '../portalkit/ResourceStatCards.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import { isCompleteFirstCursorPage, type ResourceTableChange } from '../portalkit/table'
import {
  FAST_REFRESH_MS,
  STABLE_REFRESH_MS,
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  createOperationLocks,
  operationKey,
  type LatestRefreshController,
  type ResourceDeletions,
  type ResourceRefreshMode,
} from '../refresh'
import { createFullListReadCoordinator } from '../hybridPagination'
import {
  applyPackagePaginationChange,
  clonePackageFilters,
  EMPTY_PACKAGE_FILTERS,
  hasActivePackageFilters,
  PACKAGE_FILTERS,
  PACKAGE_PAGE_SIZE,
  packagePageInfo as toPackagePageInfo,
  packageVisibility,
  type PackageFilterValues,
  type PackagePageInfo,
  type PackagePaginationMode,
} from '../packagesPagination'

const props = defineProps<{ name: string; deletions: ResourceDeletions }>()
const emit = defineEmits<{ (e: 'back'): void }>()
const repositoryScope = 'repository'
const keyScope = `deploy-key:${props.name}`
const collaboratorScope = `collaborator:${props.name}`

const repo = ref<RepositoryDetail | null>(null)
const repoLoading = ref(true)
const repoLoaded = ref(false)
const repoError = ref<string | null>(null)
const repositoryMutationError = ref<string | null>(null)
const connectionExpanded = ref(false)
const accessExpanded = ref(false)
const packagesExpanded = ref(false)

const connections = ref<Connection[]>([])
const connectionsLoading = ref(true)
const connectionsLoaded = ref(false)
const connectionsError = ref<string | null>(null)
const selectedConn = ref('')
const changingConn = ref(false)
const connError = ref<string | null>(null)

const keys = ref<DeployKey[]>([])
const keysLoading = ref(true)
const keysLoaded = ref(false)
const keysError = ref<string | null>(null)
const keyTitle = ref('')
const keyPublic = ref('')
const keyReadOnly = ref(true)
const keySubmitting = ref(false)
const keyError = ref<string | null>(null)
const keyDeleteError = ref<string | null>(null)

const collabs = ref<Collaborator[]>([])
const collabsLoading = ref(true)
const collabsLoaded = ref(false)
const collabsError = ref<string | null>(null)
const collabUser = ref('')
const collabPerm = ref('push')
const collabSubmitting = ref(false)
const collabError = ref<string | null>(null)
const collabDeleteError = ref<string | null>(null)

const packages = ref<Package[]>([])
const packagesLoading = ref(true)
const packagesLoaded = ref(false)
const packagesError = ref<string | null>(null)
const packageMode = ref<PackagePaginationMode>('server')
const packagePage = ref(1)
const packagePageSize = ref(PACKAGE_PAGE_SIZE)
const packageQuery = ref('')
const packageFilters = ref<PackageFilterValues>(clonePackageFilters(EMPTY_PACKAGE_FILTERS))
const packageCursor = ref<string | null>(null)
const packagePageInfo = ref<PackagePageInfo | null>(null)
const packageFullRead = createFullListReadCoordinator(() => api.listPackages(props.name))

const operations = createOperationLocks()
const keyColumns = [
  { key: 'title', label: 'Deploy key', primary: true },
  { key: 'access', label: 'Access' },
  { key: 'status', label: 'Status' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]
const collabColumns = [
  { key: 'username', label: 'Collaborator', primary: true },
  { key: 'permission', label: 'Permission' },
  { key: 'status', label: 'Status' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]
const packageColumns = [
  { key: 'name', label: 'Package', primary: true },
  { key: 'type', label: 'Type' },
  { key: 'visibility', label: 'Visibility' },
  { key: 'versionCount', label: 'Versions', align: 'end' as const },
  { key: 'status', label: 'Status' },
  { key: 'url', label: '', ariaLabel: 'Package link' },
]
function controllerCaughtUp(resource: { generation?: number; observedGeneration?: number }): boolean {
  return resource.generation === undefined ||
    (resource.observedGeneration !== undefined && resource.observedGeneration >= resource.generation)
}
function isDeleting(scope: string, resource: { name: string; uid?: string; deletionTimestamp?: string }): boolean {
  return !!resource.deletionTimestamp || props.deletions.has(scope, resource.name, resource.uid)
}
function isPackageDeleting(resource: Package): boolean {
  return !!resource.deletionTimestamp
}
const repositoryDeleteInFlight = computed(() => operations.phase(operationKey('repository', props.name)) === 'deleting')
const repositoryDeleting = computed(() => repositoryDeleteInFlight.value || (!!repo.value && isDeleting(repositoryScope, repo.value)))
const repositoryRefreshMode = ref<ResourceRefreshMode>('foreground')
const connectionRefreshMode = ref<ResourceRefreshMode>('foreground')
const keyRefreshMode = ref<ResourceRefreshMode>('foreground')
const collabRefreshMode = ref<ResourceRefreshMode>('foreground')
const packageRefreshMode = ref<ResourceRefreshMode>('foreground')
const repositoryRefreshing = computed(() => repoLoading.value)
const repositoryForegroundRefreshing = computed(() => repoLoading.value && repositoryRefreshMode.value === 'foreground')
const repositoryActionBusy = computed(() => repositoryForegroundRefreshing.value || repositoryDeleting.value || operations.isLocked(operationKey('repository', props.name)))
const actionItems = computed<ActionMenuItem[]>(() => [{
  id: 'delete',
  label: repositoryDeleting.value ? 'Deleting repository…' : 'Delete repository',
  tone: 'danger',
  disabled: !repo.value || repositoryActionBusy.value,
  busy: repositoryDeleting.value,
}])
const keyRows = computed<Array<Record<string, unknown>>>(() => keys.value
  .map(key => {
    const deleting = isDeleting(keyScope, key)
    return { ...key, deleting, title: key.title || key.name, access: key.readOnly ? 'read-only' : 'read-write', status: deleting ? 'Deleting' : key.ready ? 'ready' : 'pending', actions: '' }
  }))
const collabRows = computed<Array<Record<string, unknown>>>(() => collabs.value
  .map(collab => ({
    ...collab,
    deleting: isDeleting(collaboratorScope, collab),
    status: isDeleting(collaboratorScope, collab) ? 'Deleting' : !controllerCaughtUp(collab) || collab.invitationPending ? 'pending' : collab.ready ? 'active' : 'unknown',
    actions: '',
  })))
const packageRows = computed<Array<Record<string, unknown>>>(() => packages.value.map(item => ({
  ...item,
  visibility: packageVisibility(item.visibility),
  deleting: isPackageDeleting(item),
  rowKey: item.uid || `${item.type}/${item.name}`,
  status: isPackageDeleting(item) ? 'Deleting' : !controllerCaughtUp(item) ? 'pending' : item.ready ? 'ready' : item.message ? 'failed' : 'pending',
  url: item.htmlURL || '',
})))
const packagesSummaryStatus = computed(() => {
  if (!packagesLoaded.value) return packagesLoading.value ? 'Loading' : 'Unavailable'
  if (packagesError.value) return 'Stale'
  return packages.value.length ? 'Published' : 'No packages'
})

const connectionChoices = computed(() => connections.value.filter(connection => !isDeleting('connection', connection)))
const currentConn = computed(() => connections.value.find(c => c.name === repo.value?.connectionRef))
const newConn = computed(() => connections.value.find(c => c.name === selectedConn.value))
const currentOwner = computed(() => repo.value?.owner || currentConn.value?.owner || '')
const newOwner = computed(() => repo.value?.owner || newConn.value?.owner || '')
const integrationHealth = computed(() => {
  if (connectionsLoading.value && !connectionsLoaded.value) return 'Loading'
  if (!currentConn.value) return connectionsLoaded.value ? 'Unavailable' : 'Loading'
  if (currentConn.value.validated) return 'Connected'
  return currentConn.value.message ? 'Needs attention' : 'Pending'
})
const integrationHealthTone = computed(() => {
  if (integrationHealth.value === 'Connected') return null
  if (integrationHealth.value === 'Needs attention' || integrationHealth.value === 'Unavailable') return 'danger'
  return 'warning'
})
const ownerWillChange = computed(() =>
  !!repo.value &&
  selectedConn.value !== repo.value.connectionRef &&
  !repo.value.owner &&
  !!newConn.value &&
  !!currentConn.value &&
  newConn.value.owner !== currentConn.value.owner,
)

const repositoryStatus = computed(() => {
  if (repositoryDeleting.value) return 'Deleting'
  if (!repo.value) return repoLoading.value ? 'Loading' : 'Unavailable'
  return repo.value.failed ? 'failed' : repo.value.ready ? 'ready' : 'pending'
})
const repositoryStatusTone = computed(() => {
  if (repositoryDeleting.value || repositoryStatus.value === 'Loading') return 'warning'
  if (repositoryStatus.value === 'Unavailable' || repositoryStatus.value === 'failed') return 'danger'
  return null
})
function providerLabel(provider: string | undefined): string {
  if (!provider) return ''
  if (provider.toLowerCase() === 'github') return 'GitHub'
  if (provider.toLowerCase() === 'gitlab') return 'GitLab'
  return provider
}
const repositoryOwner = computed(() => repo.value?.owner || currentConn.value?.owner || '')
const repositoryTitle = computed(() => {
  const repositoryName = repo.value?.repo || props.name
  return repositoryOwner.value ? `${repositoryOwner.value}/${repositoryName}` : repositoryName
})
const isGitHubProvider = computed(() => currentConn.value?.provider?.toLowerCase() === 'github')
const integrationSummary = computed(() => {
  const value = repo.value
  if (!value) return '—'
  const provider = providerLabel(currentConn.value?.provider)
  return provider ? `${provider} · ${value.connectionRef}` : value.connectionRef || '—'
})
// TenantMissing is an expected no-context state while the host is switching
// workspaces. ResourcePage's explicit `loaded=false` contract intentionally
// shows a first-read skeleton, so use its legacy null sentinel once that
// expected no-context read has settled without an error or snapshot.
const repositoryReadState = computed<boolean | null>(() => {
  if (repoLoaded.value) return true
  if (repoError.value) return false
  return repoLoading.value ? false : null
})

const repositoryStatCards = computed<ResourceStatCard[]>(() => [
  {
    id: 'integration',
    label: 'Integration',
    value: integrationSummary.value,
    detail: integrationHealth.value,
    icon: Plug,
    tone: integrationHealthTone.value || 'default',
  },
  {
    id: 'provider',
    label: 'Provider',
    value: providerLabel(currentConn.value?.provider) || '—',
    icon: isGitHubProvider.value ? Github : GitBranch,
  },
  {
    id: 'type',
    label: 'Type',
    value: 'Repository',
    icon: PackageIcon,
  },
  {
    id: 'owner',
    label: 'Owner',
    value: repositoryOwner.value || '—',
    icon: User,
    mono: true,
  },
  {
    id: 'default-branch',
    label: 'Default branch',
    value: repo.value?.defaultBranch || 'Provider default',
    icon: GitBranch,
    mono: true,
  },
  {
    id: 'visibility',
    label: 'Visibility',
    value: repo.value?.visibility || '—',
    icon: Eye,
  },
])

let mounted = false
let repoRefresh!: LatestRefreshController
let connectionRefresh!: LatestRefreshController
let keyRefresh!: LatestRefreshController
let collabRefresh!: LatestRefreshController
let packageRefresh!: LatestRefreshController
let forcePackageFullRead = false

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function loadRepository(mode: ResourceRefreshMode = 'foreground') {
  if (mode === 'foreground') {
    repositoryRefreshMode.value = 'foreground'
    repoLoading.value = true
  }
  repoRefresh.request(mode)
}
function loadConnections(mode: ResourceRefreshMode = 'foreground') {
  if (mode === 'foreground') {
    connectionRefreshMode.value = 'foreground'
    connectionsLoading.value = true
  }
  connectionRefresh.request(mode)
}
function loadKeys(mode: ResourceRefreshMode = 'foreground') {
  if (mode === 'foreground') {
    keyRefreshMode.value = 'foreground'
    keysLoading.value = true
  }
  keyRefresh.request(mode)
}
function loadCollaborators(mode: ResourceRefreshMode = 'foreground') {
  if (mode === 'foreground') {
    collabRefreshMode.value = 'foreground'
    collabsLoading.value = true
  }
  collabRefresh.request(mode)
}
function loadPackages(mode: ResourceRefreshMode = 'foreground', forceFullRead = packageMode.value === 'client') {
  if (forceFullRead) forcePackageFullRead = true
  if (mode === 'foreground') {
    packageRefreshMode.value = 'foreground'
    packagesLoading.value = true
  }
  packageRefresh.request(mode)
}
function loadAllMode(mode: ResourceRefreshMode = 'foreground') {
  loadRepository(mode)
  loadConnections(mode)
  if (mode === 'foreground' || accessExpanded.value || keyRows.value.some(row => row.deleting) || collabRows.value.some(row => row.deleting) || keySubmitting.value || collabSubmitting.value) {
    loadKeys(mode)
    loadCollaborators(mode)
  }
  if (mode === 'foreground' || packagesExpanded.value || repositoryDeleting.value) loadPackages(mode)
}

function loadAll() {
  loadAllMode()
}

function toggleAccess() {
  accessExpanded.value = !accessExpanded.value
  if (accessExpanded.value) {
    loadKeys()
    loadCollaborators()
  }
}

function togglePackages() {
  packagesExpanded.value = !packagesExpanded.value
  if (packagesExpanded.value) loadPackages()
}

const poller = createAdaptiveRefreshTimer(() => loadAllMode('background'), () => {
  if (!repoLoaded.value || !connectionsLoaded.value || repoError.value || connectionsError.value) return FAST_REFRESH_MS
  const repositoryPending = repositoryStatus.value === 'pending' || repositoryStatus.value === 'Deleting'
  const connectionPending = connections.value.some(connection => (
    !!connection.deletionTimestamp || !connection.validated || (
      connection.generation !== undefined &&
      (connection.observedGeneration === undefined || connection.observedGeneration < connection.generation)
    )
  ))
  const accessPending = (accessExpanded.value || keysLoaded.value) && (
    !keysLoaded.value || !!keysError.value || keyRows.value.some(row => row.status === 'pending' || row.status === 'Deleting') ||
    !collabsLoaded.value || !!collabsError.value || collabRows.value.some(row => row.status === 'pending' || row.status === 'Deleting')
  )
  const packagesPending = (packagesExpanded.value || packagesLoaded.value) && (
    !packagesLoaded.value || !!packagesError.value || packageRows.value.some(row => row.status === 'pending' || row.status === 'Deleting')
  )
  return repositoryPending || connectionPending || accessPending || packagesPending ? FAST_REFRESH_MS : STABLE_REFRESH_MS
})

async function deleteRepository() {
  const current = repo.value
  if (!current || repositoryDeleting.value) return
  const ok = await confirmDialog({
    title: `Delete repository "${current.repo}"?`,
    message: 'This removes the repository on the git host. This cannot be undone.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok || !mounted) return
  const lock = operationKey('repository', current.name)
  if (!operations.acquire(lock, 'deleting')) return
  repositoryMutationError.value = null
  try {
    await api.deleteRepository(current.name)
    if (!mounted) return
    props.deletions.acknowledge(repositoryScope, current.name, current.uid)
    loadRepository()
  } catch (e) {
    repositoryMutationError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

function selectAction(action: string): void {
  if (action === 'delete') void deleteRepository()
}

function toggleConnectionEditor() {
  connectionExpanded.value = !connectionExpanded.value
}

interface PackageRequest {
  mode: PackagePaginationMode
  active: boolean
  page: number
  pageSize: number
  query: string
  filters: PackageFilterValues
  cursor: string | null
}

function currentPackageRequest(): PackageRequest {
  const filters = clonePackageFilters(packageFilters.value)
  return {
    mode: packageMode.value,
    active: hasActivePackageFilters(packageQuery.value, filters),
    page: packagePage.value,
    pageSize: packagePageSize.value,
    query: packageQuery.value,
    filters,
    cursor: packageCursor.value,
  }
}

function packageRequestIsCurrent(requestID: number, request: PackageRequest): boolean {
  const current = currentPackageRequest()
  return packageRefresh.isCurrent(requestID) &&
    current.mode === request.mode &&
    current.active === request.active &&
    current.page === request.page &&
    current.pageSize === request.pageSize &&
    current.query === request.query &&
    current.cursor === request.cursor &&
    current.filters.type === request.filters.type &&
    current.filters.visibility === request.filters.visibility &&
    current.filters.status === request.filters.status
}

function handlePackageChange(change: ResourceTableChange) {
  const wasClientMode = packageMode.value === 'client'
  const canReuseCurrentServerPage = !wasClientMode &&
    (change.reason === 'query' || change.reason === 'filter') &&
    isCompleteFirstCursorPage({
      page: packagePage.value,
      cursor: packageCursor.value,
      pageInfo: packagePageInfo.value,
    })
  const transition = applyPackagePaginationChange({
    mode: packageMode.value,
    page: packagePage.value,
    pageSize: packagePageSize.value,
    query: packageQuery.value,
    filters: { ...packageFilters.value },
    cursor: packageCursor.value,
  }, change)
  const next = transition.state
  packageMode.value = next.mode
  packagePage.value = next.page
  packagePageSize.value = next.pageSize
  packageQuery.value = next.query
  packageFilters.value = next.filters
  packageCursor.value = next.cursor
  packagePageInfo.value = null

  if (!hasActivePackageFilters(next.query, next.filters)) {
    // A pending active-query walk can still exist while mode is server; clear
    // invalidates it before the queued first server page is allowed to commit.
    packageFullRead.clear()
    if (transition.clearRows) packages.value = []
    if (transition.reload) loadPackages('foreground', false)
    return
  }

  // Reuse a terminal first page as the complete repository package source.
  if (canReuseCurrentServerPage) {
    packageFullRead.seed(packages.value)
    packageMode.value = 'client'
    packagePage.value = 1
    packageCursor.value = null
    packagePageInfo.value = null
    return
  }

  // A complete package walk is independent of the current query. Keep it
  // available across rapid edits and while an older server request settles.
  const cachedFullRows = packageFullRead.peek()
  if (cachedFullRows) {
    packages.value = cachedFullRows
    packageMode.value = 'client'
    packagePage.value = 1
    packageCursor.value = null
    packagePageInfo.value = null
    return
  }

  if (transition.clearRows) packages.value = []
  if (transition.reload) loadPackages()
}

async function changeConnection() {
  const current = repo.value
  if (!current || repositoryDeleting.value || selectedConn.value === current.connectionRef) return
  const message = ownerWillChange.value
    ? `Its owner (${newOwner.value}) differs from the current (${currentOwner.value}).\n` +
      `The repository will be re-targeted to that account — a new repo may be created there, ` +
      `and the existing repo on ${currentOwner.value} is left untouched.`
    : 'Only the managing credential changes; the repository stays on the same account.'
  const ok = await confirmDialog({
    title: `Change connection to "${selectedConn.value}"?`,
    message,
    confirmLabel: 'Change',
    danger: ownerWillChange.value,
  })
  if (!ok || !mounted) return
  const lock = operationKey('repository', current.name)
  if (!operations.acquire(lock, 'saving')) return
  changingConn.value = true
  connError.value = null
  try {
    const updated = await api.updateRepositoryConnection(current.name, selectedConn.value)
    repo.value = { ...current, ...updated, conditions: current.conditions }
    connectionExpanded.value = false
    loadRepository()
  } catch (e) {
    connError.value = errMessage(e)
  } finally {
    changingConn.value = false
    operations.release(lock)
  }
}

async function addKey() {
  keyError.value = null
  if (repositoryDeleting.value) return
  if (!keysLoaded.value) {
    keyError.value = 'Deploy keys are still loading. Retry the read before adding a key.'
    return
  }
  keySubmitting.value = true
  try {
    const created = await api.createDeployKey({
      repositoryRef: props.name,
      title: keyTitle.value || undefined,
      publicKey: keyPublic.value || undefined,
      readOnly: keyReadOnly.value,
    })
    keys.value = [...keys.value.filter(item => item.name !== created.name), created]
    keysLoaded.value = true
    keyTitle.value = keyPublic.value = ''
    loadKeys()
  } catch (e) {
    keyError.value = errMessage(e)
  } finally {
    keySubmitting.value = false
  }
}

async function removeKey(row: Record<string, unknown>) {
  const key = row as unknown as DeployKey
  if (repositoryDeleting.value || isDeleting(keyScope, key)) return
  const ok = await confirmDialog({ title: `Delete deploy key "${key.title || key.name}"?`, confirmLabel: 'Delete', danger: true })
  if (!ok || !mounted) return
  const lock = operationKey('deploy-key', key.name)
  if (!operations.acquire(lock, 'deleting')) return
  keyDeleteError.value = null
  try {
    await api.deleteDeployKey(key.name)
    props.deletions.acknowledge(keyScope, key.name, key.uid)
    loadKeys()
  } catch (e) {
    keyDeleteError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

async function addCollab() {
  collabError.value = null
  if (repositoryDeleting.value) return
  if (!collabsLoaded.value) {
    collabError.value = 'Collaborators are still loading. Retry the read before adding one.'
    return
  }
  if (!collabUser.value) {
    collabError.value = 'username is required'
    return
  }
  collabSubmitting.value = true
  try {
    const created = await api.createCollaborator({ repositoryRef: props.name, username: collabUser.value, permission: collabPerm.value })
    collabs.value = [...collabs.value.filter(item => item.name !== created.name), created]
    collabsLoaded.value = true
    collabUser.value = ''
    loadCollaborators()
  } catch (e) {
    collabError.value = errMessage(e)
  } finally {
    collabSubmitting.value = false
  }
}

async function removeCollab(row: Record<string, unknown>) {
  const collab = row as unknown as Collaborator
  if (repositoryDeleting.value || isDeleting(collaboratorScope, collab)) return
  const ok = await confirmDialog({ title: `Remove collaborator "${collab.username}"?`, confirmLabel: 'Remove', danger: true })
  if (!ok || !mounted) return
  const lock = operationKey('collaborator', collab.name)
  if (!operations.acquire(lock, 'deleting')) return
  collabDeleteError.value = null
  try {
    await api.deleteCollaborator(collab.name)
    props.deletions.acknowledge(collaboratorScope, collab.name, collab.uid)
    loadCollaborators()
  } catch (e) {
    collabDeleteError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

repoRefresh = createLatestRefreshController(async (requestID, mode) => {
  repositoryRefreshMode.value = mode
  repoLoading.value = true
  try {
    const next = await api.getRepository(props.name)
    if (!repoRefresh.isCurrent(requestID)) return
    repo.value = next
    if (next.deletionTimestamp) props.deletions.acknowledge(repositoryScope, next.name, next.uid)
    repoLoaded.value = true
    repoError.value = null
    if (selectedConn.value === '') selectedConn.value = next.connectionRef
  } catch (e) {
    if (!repoRefresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    if (err.reason === 'NotFound' && (repositoryDeleting.value || props.deletions.has(repositoryScope, props.name))) {
      repo.value = null
      emit('back')
      return
    }
    repoError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (repoRefresh.isCurrent(requestID)) {
      repoLoading.value = false
      poller.schedule()
    }
  }
})

connectionRefresh = createLatestRefreshController(async (requestID, mode) => {
  connectionRefreshMode.value = mode
  connectionsLoading.value = true
  try {
    const next = await api.listConnections()
    if (!connectionRefresh.isCurrent(requestID)) return
    connections.value = next
    next.filter(item => item.deletionTimestamp).forEach(item => props.deletions.acknowledge('connection', item.name, item.uid))
    props.deletions.reconcile('connection', next)
    connectionsLoaded.value = true
    connectionsError.value = null
    const available = next.filter(item => !isDeleting('connection', item))
    if (repo.value && (selectedConn.value === '' || !available.some(item => item.name === selectedConn.value))) {
      selectedConn.value = available.some(item => item.name === repo.value?.connectionRef) ? repo.value.connectionRef : available[0]?.name || ''
    }
  } catch (e) {
    if (!connectionRefresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    connectionsError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (connectionRefresh.isCurrent(requestID)) {
      connectionsLoading.value = false
      poller.schedule()
    }
  }
})

keyRefresh = createLatestRefreshController(async (requestID, mode) => {
  keyRefreshMode.value = mode
  keysLoading.value = true
  try {
    const next = await api.listDeployKeys(props.name)
    if (!keyRefresh.isCurrent(requestID)) return
    keys.value = next
    next.filter(item => item.deletionTimestamp).forEach(item => props.deletions.acknowledge(keyScope, item.name, item.uid))
    props.deletions.reconcile(keyScope, next)
    keysLoaded.value = true
    keysError.value = null
  } catch (e) {
    if (!keyRefresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    keysError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (keyRefresh.isCurrent(requestID)) {
      keysLoading.value = false
      poller.schedule()
    }
  }
})

collabRefresh = createLatestRefreshController(async (requestID, mode) => {
  collabRefreshMode.value = mode
  collabsLoading.value = true
  try {
    const next = await api.listCollaborators(props.name)
    if (!collabRefresh.isCurrent(requestID)) return
    collabs.value = next
    next.filter(item => item.deletionTimestamp).forEach(item => props.deletions.acknowledge(collaboratorScope, item.name, item.uid))
    props.deletions.reconcile(collaboratorScope, next)
    collabsLoaded.value = true
    collabsError.value = null
  } catch (e) {
    if (!collabRefresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    collabsError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (collabRefresh.isCurrent(requestID)) {
      collabsLoading.value = false
      poller.schedule()
    }
  }
})

packageRefresh = createLatestRefreshController(async (requestID, mode) => {
  packageRefreshMode.value = mode
  const request = currentPackageRequest()
  const forceFullRead = forcePackageFullRead
  forcePackageFullRead = false
  packagesLoading.value = true
  // Do not render an unfiltered server page as the result of a newly entered
  // query. Same-page polling keeps cached rows visible until the replacement
  // arrives, preserving the existing stale-read behavior.
  if (request.active && request.mode === 'server') {
    packages.value = []
    packagePageInfo.value = null
  }
  try {
    if (request.active || request.mode === 'client') {
      const next = await packageFullRead.read(forceFullRead)
      if (!packageRequestIsCurrent(requestID, request)) {
        // Keep the query-independent result for the newest request. Promote
        // only an active current state; server mode must never locally filter
        // a complete walk until the mode switch is explicit.
        const current = currentPackageRequest()
        if (packageFullRead.peek() !== null && current.active && packageMode.value === 'server') {
          packages.value = next
          packageMode.value = 'client'
          packagePage.value = 1
          packageCursor.value = null
          packagePageInfo.value = null
          packagesLoaded.value = true
          packagesError.value = null
        }
        return
      }

      packages.value = next
      packageMode.value = 'client'
      packagePage.value = 1
      packageCursor.value = null
      packagePageInfo.value = null
    } else {
      const next = await api.listPackagesPage(props.name, {
        limit: request.pageSize,
        ...(request.cursor ? { continue: request.cursor } : {}),
      })
      if (!packageRequestIsCurrent(requestID, request)) return
      packages.value = next.items
      packageCursor.value = request.cursor
      const nextPageInfo = toPackagePageInfo(next.continue)
      packagePageInfo.value = nextPageInfo
      // Keep server ownership until an active query/filter asks to reuse this
      // terminal page. The metadata is retained for that transition.
    }
    packagesLoaded.value = true
    packagesError.value = null
  } catch (e) {
    if (!packageRequestIsCurrent(requestID, request)) return
    const err = e as ErrorResponse
    packagesError.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (packageRefresh.isCurrent(requestID)) {
      packagesLoading.value = false
      poller.schedule()
    }
  }
})

onMounted(() => {
  mounted = true
  loadAll()
  poller.schedule()
})
onUnmounted(() => {
  mounted = false
  poller.stop()
  repoRefresh.stop()
  connectionRefresh.stop()
  keyRefresh.stop()
  collabRefresh.stop()
  packageRefresh.stop()
})
</script>

<template>
  <div class="repo-detail">
    <ResourceBackLink class="repo-detail__back" href="/ui/providers/code/repositories" @back="emit('back')">
      Repositories
    </ResourceBackLink>
    <div class="repo-detail__resource">
      <div class="repo-detail__provider-mark" role="img" :aria-label="`${providerLabel(currentConn?.provider) || 'Provider unavailable'} mark`">
        <Github v-if="isGitHubProvider" :size="20" :stroke-width="1.75" aria-hidden="true" />
        <GitBranch v-else :size="20" :stroke-width="1.75" aria-hidden="true" />
      </div>
      <ResourcePage
        :title="repositoryTitle"
        kind="Repository"
        :loaded="repositoryReadState"
        :loading="repoLoading"
        :refresh-mode="repositoryRefreshMode"
        :error="repoError"
        :stale="repoLoaded && !!repoError"
        retryable
        @retry="loadRepository"
      >
    <template #meta>
      <span>{{ providerLabel(currentConn?.provider) || 'Provider unavailable' }}</span>
    </template>
    <template #status>
      <StatusBadge :status="repositoryStatus" :tone="repositoryStatusTone" :title="repo?.message" />
    </template>
    <template #actions>
      <div class="repo-detail__actions" role="group" aria-label="Repository actions">
        <a v-if="repo?.htmlURL && !repositoryDeleting" class="k-btn k-btn--primary" :href="repo.htmlURL" target="_blank" rel="noopener">
          Open repository <ExternalLink :size="13" aria-hidden="true" />
        </a>
        <button
          type="button"
          class="k-btn k-btn--ghost"
          :disabled="repositoryActionBusy"
          :aria-busy="repositoryRefreshing || undefined"
          @click="loadAll"
        >
            <RefreshCw :size="14" :class="{ spin: repositoryForegroundRefreshing }" aria-hidden="true" />
          {{ repositoryForegroundRefreshing ? 'Refreshing…' : 'Refresh' }}
        </button>
        <ActionMenu
          label="More repository actions"
          :items="actionItems"
          :disabled="!repo || repositoryActionBusy"
          @select="selectAction"
        />
      </div>
    </template>

    <template #summary>
      <ResourceStatCards :cards="repositoryStatCards" density="compact" aria-label="Repository summary" />
    </template>

    <template #body>
      <p v-if="repositoryMutationError" class="error mutation-error" role="alert" aria-live="assertive">{{ repositoryMutationError }}</p>
      <p v-if="repositoryDeleting" class="repo-detail__deleting" role="status" aria-live="polite">
        Deleting this repository. The last successful snapshot remains visible until the hub confirms removal.
      </p>
      <div class="repo-detail__sections">
        <ResourceSectionCard
          id="repository-integration"
          eyebrow="Repository"
          title="Integration"
          description="Repository description and the managing provider connection."
        >
            <p class="repo-integration-card__description">{{ repo?.description || 'No description provided.' }}</p>

            <div v-if="repo" class="repo-integration-card__connection">
              <h3 class="repo-integration-card__connection-title">Managing connection</h3>
              <div class="repo-integration-row">
                <div class="repo-integration-row__content">
                  <span class="repo-integration-row__provider">{{ providerLabel(currentConn?.provider) || 'Provider unavailable' }}</span>
                  <span class="repo-integration-row__connection mono">{{ repo.connectionRef || '—' }}</span>
                  <StatusBadge :status="integrationHealth" :tone="integrationHealthTone" :title="currentConn?.message" />
                </div>
                <button class="k-btn k-btn--ghost" type="button" :disabled="repositoryDeleting || changingConn" :aria-expanded="connectionExpanded" aria-controls="repository-integration-editor" @click="toggleConnectionEditor">
                  <ArrowLeftRight v-if="!connectionExpanded" :size="14" aria-hidden="true" />
                  <X v-else :size="14" aria-hidden="true" />
                  {{ connectionExpanded ? 'Cancel' : 'Change' }}
                </button>
              </div>
              <div v-if="connectionExpanded" id="repository-integration-editor" class="repo-integration-editor">
              <div class="conn-edit" :aria-busy="connectionsLoading">
                <select v-model="selectedConn" class="k-input" :disabled="repositoryDeleting || changingConn || !connectionsLoaded">
                  <option v-for="c in connectionChoices" :key="c.name" :value="c.name">{{ c.name }} ({{ c.owner }})</option>
                </select>
                <button class="k-btn k-btn--primary" type="button" :disabled="repositoryDeleting || changingConn || !connectionsLoaded || selectedConn === repo.connectionRef" @click="changeConnection"><ArrowLeftRight :size="14" aria-hidden="true" />{{ changingConn ? 'Changing…' : 'Change' }}</button>
              </div>
              <span v-if="connectionsLoading && !connectionsLoaded" class="muted" role="status" aria-live="polite">Loading connections…</span>
              <div v-if="connectionsError" class="error read-error" role="alert" aria-live="assertive">
                <span>{{ connectionsLoaded ? 'Showing cached connection choices. ' : '' }}{{ connectionsError }}</span><button class="k-btn k-btn--ghost" type="button" @click="loadConnections()">Retry</button>
              </div>
              <span v-else-if="connectionsLoading && connectionsLoaded" class="sr-only" role="status" aria-live="polite">Updating connections…</span>
              <p v-if="ownerWillChange" class="conn-warn"><AlertTriangle :size="15" class="warn-ic" /> Owner <code>{{ newOwner }}</code> differs from current <code>{{ currentOwner }}</code> — this re-targets the repo to a different account and may create a new repo there.</p>
              <p v-else-if="selectedConn !== repo.connectionRef" class="muted">Same owner — only the managing credential changes.</p>
              <span v-if="connError" class="error" role="alert">{{ connError }}</span>
              </div>
            </div>
        </ResourceSectionCard>

        <ResourceSectionCard id="repository-access" eyebrow="Permissions" title="Access" description="Manage deploy credentials and collaborators.">
          <template #actions>
            <div class="repo-section-card__facts" aria-label="Access counts">
              <span><strong>{{ keysLoaded ? keyRows.length : '—' }}</strong> deploy keys</span>
              <span><strong>{{ collabsLoaded ? collabRows.length : '—' }}</strong> collaborators</span>
            </div>
            <button class="k-btn k-btn--ghost" type="button" :disabled="!repo" :aria-expanded="accessExpanded" aria-controls="repository-access-content" @click="toggleAccess">
              <Users :size="14" aria-hidden="true" />
              {{ accessExpanded ? 'Hide access' : 'Manage access' }}
            </button>
          </template>
          <div v-if="accessExpanded" id="repository-access-content" class="grid-2">
          <div v-if="repo" class="repo-domain-block">
            <div class="panel-head"><h3 class="panel-title">Deploy keys</h3><span v-if="keysLoaded" class="muted">{{ keyRows.length }}</span></div>
            <form class="form" @submit.prevent="addKey">
              <label class="field"><span class="field-label">Title</span><input v-model="keyTitle" class="k-input" :disabled="repositoryDeleting" placeholder="ci-deploy" autocomplete="off" /></label>
              <label class="field"><span class="field-label">Public key (leave empty to generate)</span><textarea v-model="keyPublic" class="k-input" :disabled="repositoryDeleting" rows="2" placeholder="ssh-ed25519 AAAA…" /></label>
              <label class="field field-check"><input v-model="keyReadOnly" type="checkbox" :disabled="repositoryDeleting" /> read-only</label>
              <div class="code-form-actions"><button class="k-btn k-btn--primary" type="submit" :disabled="repositoryDeleting || keySubmitting || !keysLoaded">{{ keySubmitting ? 'Adding…' : 'Add deploy key' }}</button><span v-if="keyError" class="error" role="alert">{{ keyError }}</span></div>
              <p class="muted">A generated key's private half is written to a Secret in your workspace.</p>
            </form>
            <p v-if="keyDeleteError" class="error mutation-error" role="alert" aria-live="assertive">{{ keyDeleteError }}</p>
            <ResourceTable aria-label="Deploy keys" :columns="keyColumns" :rows="keyRows" row-key="name" :loaded="keysLoaded" :loading="keysLoading" :refresh-mode="keyRefreshMode" :error="keysError" :stale="keysLoaded && !!keysError" retryable searchable search-placeholder="Search deploy keys…" :filters="[{ key: 'access', label: 'Access' }, { key: 'status', label: 'Status', allLabel: 'Any status' }]" paginated :page-size="10" empty-text="No deploy keys." :interactive="false" @retry="loadKeys">
              <template #title="{ row }"><strong>{{ row.title }}</strong><div v-if="row.generated && row.secretName" class="muted">secret: <code>{{ row.secretName }}</code></div></template>
              <template #access="{ value }"><span class="k-badge k-badge--muted">{{ value }}</span></template>
              <template #status="{ row }"><StatusBadge :status="String(row.status)" :tone="row.deleting ? 'warning' : null" :title="String(row.message || '')" /></template>
              <template #actions="{ row }"><div class="code-row-actions"><ResourceTableDeleteButton :label="`Delete deploy key ${String(row.title)}`" :busy-label="`Deleting deploy key ${String(row.title)}…`" :busy="Boolean(row.deleting) || operations.phase(operationKey('deploy-key', String(row.name))) === 'deleting'" :disabled="repositoryDeleting || Boolean(row.deleting) || operations.isLocked(operationKey('deploy-key', String(row.name)))" @click="removeKey(row)" /></div></template>
            </ResourceTable>
          </div>
          <div v-if="repo" class="repo-domain-block">
            <div class="panel-head"><h3 class="panel-title">Collaborators</h3><span v-if="collabsLoaded" class="muted">{{ collabRows.length }}</span></div>
            <form class="form" @submit.prevent="addCollab">
              <label class="field"><span class="field-label">Username</span><input v-model="collabUser" class="k-input" :disabled="repositoryDeleting" placeholder="octocat" autocomplete="off" /></label>
              <label class="field"><span class="field-label">Permission</span><select v-model="collabPerm" class="k-input" :disabled="repositoryDeleting"><option value="pull">pull</option><option value="push">push</option><option value="admin">admin</option></select></label>
              <div class="code-form-actions"><button class="k-btn k-btn--primary" type="submit" :disabled="repositoryDeleting || collabSubmitting || !collabsLoaded">{{ collabSubmitting ? 'Adding…' : 'Add collaborator' }}</button><span v-if="collabError" class="error" role="alert">{{ collabError }}</span></div>
            </form>
            <p v-if="collabDeleteError" class="error mutation-error" role="alert" aria-live="assertive">{{ collabDeleteError }}</p>
            <ResourceTable aria-label="Repository collaborators" :columns="collabColumns" :rows="collabRows" row-key="name" :loaded="collabsLoaded" :loading="collabsLoading" :refresh-mode="collabRefreshMode" :error="collabsError" :stale="collabsLoaded && !!collabsError" retryable searchable search-placeholder="Search collaborators…" :filters="[{ key: 'permission', label: 'Permission' }, { key: 'status', label: 'Status', allLabel: 'Any status' }]" paginated :page-size="10" empty-text="No collaborators." :interactive="false" @retry="loadCollaborators">
              <template #username="{ value }"><strong>{{ value }}</strong></template>
              <template #permission="{ value }"><span class="k-badge k-badge--muted">{{ value }}</span></template>
              <template #status="{ row }"><StatusBadge :status="String(row.status)" :tone="row.deleting ? 'warning' : null" :title="String(row.message || '')" /></template>
              <template #actions="{ row }"><div class="code-row-actions"><ResourceTableDeleteButton :label="`Remove collaborator ${String(row.username)}`" :busy-label="`Removing collaborator ${String(row.username)}…`" :busy="Boolean(row.deleting) || operations.phase(operationKey('collaborator', String(row.name))) === 'deleting'" :disabled="repositoryDeleting || Boolean(row.deleting) || operations.isLocked(operationKey('collaborator', String(row.name)))" @click="removeCollab(row)" /></div></template>
            </ResourceTable>
          </div>
          </div>
        </ResourceSectionCard>

        <ResourceSectionCard id="repository-packages" eyebrow="Artifacts" title="Packages" description="Published artifacts observed for this repository.">
          <template #actions>
            <div class="repo-section-card__facts" aria-label="Package summary">
              <span><strong>{{ packagesLoaded ? packages.length : '—' }}</strong> visible</span>
              <span>{{ packagesSummaryStatus }}</span>
            </div>
            <button class="k-btn k-btn--ghost" type="button" :disabled="!repo" :aria-expanded="packagesExpanded" aria-controls="repository-packages-content" @click="togglePackages">
              <PackageOpen :size="14" aria-hidden="true" />
              {{ packagesExpanded ? 'Hide packages' : 'View packages' }}
            </button>
          </template>
          <div v-if="repo && packagesExpanded" id="repository-packages-content" class="repo-domain-block">
            <div class="panel-head"><h3 class="panel-title">Packages</h3><span v-if="packagesLoaded && packageMode === 'client'" class="muted">{{ packageRows.length }}</span></div>
            <ResourceTable aria-label="Repository packages" :columns="packageColumns" :rows="packageRows" row-key="rowKey" :loaded="packagesLoaded" :loading="packagesLoading" :refresh-mode="packageRefreshMode" :error="packagesError" :stale="packagesLoaded && !!packagesError" retryable searchable search-placeholder="Search packages…" :filters="PACKAGE_FILTERS" :pagination-mode="packageMode" :page="packagePage" :page-size="packagePageSize" :query="packageQuery" :filter-values="packageFilters" :cursor="packageCursor" :page-info="packagePageInfo" empty-text="No packages published to this repository yet." :interactive="false" @retry="loadPackages" @change="handlePackageChange">
              <template #name="{ row }"><strong><a v-if="row.htmlURL && !repositoryDeleting && !row.deleting" class="k-table-resource-link" :href="String(row.htmlURL)" target="_blank" rel="noopener">{{ row.name }}</a><template v-else>{{ row.name }}</template></strong></template>
              <template #type="{ value }"><span class="k-badge k-badge--muted">{{ value }}</span></template>
              <template #visibility="{ value }"><span class="muted">{{ value === 'unknown' ? '—' : value }}</span></template>
              <template #versionCount="{ value }"><span class="muted">{{ value || 0 }}</span></template>
              <template #status="{ row }"><StatusBadge :status="String(row.status)" :tone="row.deleting ? 'warning' : null" :title="String(row.message || '')" /></template>
              <template #url="{ row }"><a v-if="row.htmlURL && !repositoryDeleting && !row.deleting" :href="String(row.htmlURL)" target="_blank" rel="noopener">View <ExternalLink :size="12" aria-hidden="true" /></a></template>
            </ResourceTable>
            <p class="muted">Packages appear automatically when artifacts are pushed (e.g. <code>docker push</code>, <code>npm publish</code>).</p>
          </div>
        </ResourceSectionCard>

        <ResourceSectionCard id="repository-conditions" eyebrow="Diagnostics" title="Health" description="Controller health and provider details for this repository.">
          <ConditionsPanel
            :conditions="repo?.conditions || []"
            :generation="repo?.generation"
            :observed-generation="repo?.observedGeneration"
            empty-text="No health conditions reported yet."
          />
          <dl class="repo-conditions__facts" aria-label="Repository health facts">
            <div><dt>Provider status</dt><dd>{{ repo?.ready ? 'Ready' : 'Waiting for reconciliation' }}</dd></div>
            <div><dt>Repository ID</dt><dd class="mono">{{ repo?.repoID || '—' }}</dd></div>
            <div><dt>Browser URL</dt><dd class="mono">{{ repo?.htmlURL || '—' }}</dd></div>
            <div><dt>Clone URL</dt><dd class="mono">{{ repo?.cloneURL || '—' }}</dd></div>
            <div><dt>SSH URL</dt><dd class="mono">{{ repo?.sshURL || '—' }}</dd></div>
          </dl>
        </ResourceSectionCard>
      </div>
    </template>
      </ResourcePage>
    </div>
  </div>
</template>
