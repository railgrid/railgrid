<script setup lang="ts">
import { computed, onActivated, onMounted, onUnmounted, ref } from 'vue'
import { ExternalLink, GitBranch, Link2 } from 'lucide-vue-next'
import { api } from '../api'
import { CODE_JOURNEY_STEPS, codeFirstRunModel, type CodeJourneyAction } from '../journey'
import type { Connection, ErrorResponse, Repository } from '../types'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
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
  cloneRepositoryFilters,
  EMPTY_REPOSITORY_FILTERS,
  hasActiveRepositoryFilters,
  applyRepositoryPaginationChange,
  REPOSITORY_PAGE_SIZE,
  repositoryFilters,
  repositoryCollectionIsAuthoritativelyEmpty,
  repositoryPageInfo as toRepositoryPageInfo,
  repositoryStatus,
  type RepositoryFilterValues,
  type RepositoryPaginationState,
  type RepositoryPaginationMode,
} from '../repositoriesPagination'

const props = defineProps<{ deletions: ResourceDeletions }>()
const emit = defineEmits<{
  (e: 'open', name: string): void
  (e: 'create'): void
  (e: 'create-connection'): void
}>()
const deletionScope = 'repository'

const repos = ref<Repository[]>([])
const connections = ref<Connection[]>([])
const error = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const loading = ref(false)
const loaded = ref(false)
const repositoryMode = ref<RepositoryPaginationMode>('server')
const repositoryPage = ref(1)
const repositoryPageSize = ref(REPOSITORY_PAGE_SIZE)
const repositoryQuery = ref('')
const repositoryFiltersValue = ref<RepositoryFilterValues>(cloneRepositoryFilters(EMPTY_REPOSITORY_FILTERS))
const repositoryCursor = ref<string | null>(null)
const repositoryPageInfo = ref<ReturnType<typeof toRepositoryPageInfo> | null>(null)
const repositoryFullRead = createFullListReadCoordinator(() => api.listRepositories())
const connectionsError = ref<string | null>(null)
const connectionsLoading = ref(false)
const connectionsLoaded = ref(false)
const operations = createOperationLocks()
const columns = [
  { key: 'name', label: 'Name', primary: true, fullValue: (row: Record<string, unknown>) => String(row.repo || row.name) },
  { key: 'connectionRef', label: 'Connection' },
  { key: 'visibility', label: 'Visibility' },
  { key: 'url', label: 'URL' },
  { key: 'status', label: 'Status' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]
const rows = computed<Array<Record<string, unknown>>>(() => repos.value
  .map(repository => {
    const deleting = isDeleting(repository)
    return { ...repository, deleting, url: repository.htmlURL || '', status: deleting ? 'Deleting' : repositoryStatus(repository), actions: '' }
  }))
const availableConnections = computed(() => connections.value.filter(connection => (
  !connection.deletionTimestamp && !props.deletions.has('connection', connection.name, connection.uid)
)))
const repositoryCollectionEmpty = computed(() => repositoryCollectionIsAuthoritativelyEmpty({
  mode: repositoryMode.value,
  page: repositoryPage.value,
  cursor: repositoryCursor.value,
  pageInfo: repositoryPageInfo.value,
  rowCount: repos.value.length,
}))
const showFirstRun = computed(() => loaded.value
  && connectionsLoaded.value
  && !error.value
  && !connectionsError.value
  && repositoryCollectionEmpty.value)
const firstRun = computed(() => codeFirstRunModel('repository', availableConnections.value.length > 0))

function isDeleting(repository: Pick<Repository, 'name' | 'uid' | 'deletionTimestamp'>): boolean {
  return !!repository.deletionTimestamp || props.deletions.has(deletionScope, repository.name, repository.uid)
}

function handleFirstRun(action: CodeJourneyAction): void {
  if (action === 'create-connection') emit('create-connection')
  else emit('create')
}
const repositoryFilterDefinitions = computed(() => repositoryFilters(connections.value))

let mounted = false
let repoRefresh!: LatestRefreshController
let connectionRefresh!: LatestRefreshController
let forceRepositoryFullRead = false
const repositoryRefreshMode = ref<ResourceRefreshMode>('foreground')
const connectionsRefreshMode = ref<ResourceRefreshMode>('foreground')
const poller = createAdaptiveRefreshTimer(() => load('background'), () => {
  if (!loaded.value || !connectionsLoaded.value || error.value || connectionsError.value) return FAST_REFRESH_MS
  const repositoryPending = repos.value.some(repository => {
    const status = repositoryStatus(repository)
    return status === 'pending' || status === 'Deleting'
  })
  const connectionPending = connections.value.some(connection => (
    !!connection.deletionTimestamp || !connection.validated || (
      connection.generation !== undefined &&
      (connection.observedGeneration === undefined || connection.observedGeneration < connection.generation)
    )
  ))
  return repositoryPending || connectionPending ? FAST_REFRESH_MS : STABLE_REFRESH_MS
})

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function loadRepositories(mode: ResourceRefreshMode = 'foreground', forceFullRead = repositoryMode.value === 'client') {
  if (forceFullRead) forceRepositoryFullRead = true
  if (mode === 'foreground') {
    repositoryRefreshMode.value = 'foreground'
    loading.value = true
  }
  repoRefresh.request(mode)
}

function loadConnections(mode: ResourceRefreshMode = 'foreground') {
  if (mode === 'foreground') {
    connectionsRefreshMode.value = 'foreground'
    connectionsLoading.value = true
  }
  connectionRefresh.request(mode)
}

function load(mode: ResourceRefreshMode = 'foreground') {
  loadRepositories(mode)
  loadConnections(mode)
}

function openRepository(row: Record<string, unknown>) {
  const resourceName = String(row.name)
  if (!row.deleting && !operations.isLocked(operationKey('repository', resourceName))) emit('open', resourceName)
}

interface RepositoryRequest {
  mode: RepositoryPaginationMode
  active: boolean
  page: number
  pageSize: number
  query: string
  filters: RepositoryFilterValues
  cursor: string | null
}

function currentRepositoryRequest(): RepositoryRequest {
  return {
    mode: repositoryMode.value,
    active: hasActiveRepositoryFilters(repositoryQuery.value, repositoryFiltersValue.value),
    page: repositoryPage.value,
    pageSize: repositoryPageSize.value,
    query: repositoryQuery.value,
    filters: cloneRepositoryFilters(repositoryFiltersValue.value),
    cursor: repositoryCursor.value,
  }
}

function repositoryRequestIsCurrent(requestID: number, request: RepositoryRequest): boolean {
  const current = currentRepositoryRequest()
  return repoRefresh.isCurrent(requestID) &&
    current.mode === request.mode &&
    current.active === request.active &&
    current.page === request.page &&
    current.pageSize === request.pageSize &&
    current.query === request.query &&
    current.cursor === request.cursor &&
    current.filters.connectionRef === request.filters.connectionRef &&
    current.filters.visibility === request.filters.visibility &&
    current.filters.status === request.filters.status
}

function handleRepositoryChange(change: ResourceTableChange): void {
  const wasClientMode = repositoryMode.value === 'client'
  const canReuseCurrentServerPage = !wasClientMode &&
    (change.reason === 'query' || change.reason === 'filter') &&
    isCompleteFirstCursorPage({
      page: repositoryPage.value,
      cursor: repositoryCursor.value,
      pageInfo: repositoryPageInfo.value,
    })
  const transition = applyRepositoryPaginationChange({
    mode: repositoryMode.value,
    page: repositoryPage.value,
    pageSize: repositoryPageSize.value,
    query: repositoryQuery.value,
    filters: cloneRepositoryFilters(repositoryFiltersValue.value),
    cursor: repositoryCursor.value,
  } satisfies RepositoryPaginationState, change)
  const next = transition.state
  repositoryMode.value = next.mode
  repositoryPage.value = next.page
  repositoryPageSize.value = next.pageSize
  repositoryQuery.value = next.query
  repositoryFiltersValue.value = next.filters
  repositoryCursor.value = next.cursor
  repositoryPageInfo.value = null

  if (!hasActiveRepositoryFilters(next.query, next.filters)) {
    // The full walk may have started while mode was still server during an
    // active-query transition. Clearing must invalidate that pending result
    // even before client mode has been committed.
    repositoryFullRead.clear()
    if (transition.clearRows) repos.value = []
    if (transition.reload) loadRepositories('foreground', false)
    return
  }

  // A terminal first server page is already the complete authority. Promote it
  // synchronously so entering a query never causes an unnecessary full walk.
  if (canReuseCurrentServerPage) {
    repositoryFullRead.seed(repos.value)
    repos.value
      .filter(item => item.deletionTimestamp)
      .forEach(item => props.deletions.acknowledge(deletionScope, item.name, item.uid))
    props.deletions.reconcile(deletionScope, repos.value)
    repositoryMode.value = 'client'
    repositoryPage.value = 1
    repositoryCursor.value = null
    repositoryPageInfo.value = null
    return
  }

  // A prior complete walk remains query-independent. Reuse it immediately
  // while a server page or an older request is still settling; this prevents a
  // rapid query/filter change from presenting an empty table or waiting for the
  // next polling interval.
  const cachedFullRows = repositoryFullRead.peek()
  if (cachedFullRows) {
    repos.value = cachedFullRows
    repositoryMode.value = 'client'
    repositoryPage.value = 1
    repositoryCursor.value = null
    repositoryPageInfo.value = null
    return
  }

  if (transition.clearRows) repos.value = []
  if (transition.reload) loadRepositories()
}

async function remove(row: Record<string, unknown>) {
  const repository = row as unknown as Repository
  if (isDeleting(repository)) return
  const ok = await confirmDialog({
    title: `Delete repository "${repository.repo}"?`,
    message: 'This removes the repository on the git host. This cannot be undone.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok || !mounted) return
  const lock = operationKey('repository', repository.name)
  if (!operations.acquire(lock, 'deleting')) return
  mutationError.value = null
  try {
    await api.deleteRepository(repository.name)
    if (!mounted) return
    props.deletions.acknowledge(deletionScope, repository.name, repository.uid)
    loadRepositories()
  } catch (e) {
    mutationError.value = errMessage(e)
  } finally {
    operations.release(lock)
  }
}

repoRefresh = createLatestRefreshController(async (requestID, mode) => {
  repositoryRefreshMode.value = mode
  const request = currentRepositoryRequest()
  const forceFullRead = forceRepositoryFullRead
  forceRepositoryFullRead = false
  loading.value = true
  // Never render an unfiltered server page as the result of a newly entered
  // query. Same-mode polling keeps cached rows visible.
  if (request.active && request.mode === 'server') {
    repos.value = []
    repositoryPageInfo.value = null
  }
  try {
    if (request.active || request.mode === 'client') {
      const next = await repositoryFullRead.read(forceFullRead)
      // The walk is query-independent. Retain its complete result even when
      // this particular request was superseded, so the newest query can apply
      // it without starting another walk.
      const hasCurrentAuthority = repositoryFullRead.peek() !== null
      if (mounted && hasCurrentAuthority) {
        next.filter(item => item.deletionTimestamp).forEach(item => props.deletions.acknowledge(deletionScope, item.name, item.uid))
        props.deletions.reconcile(deletionScope, next)
      }

      if (!repositoryRequestIsCurrent(requestID, request)) {
        // A rapid active-query edit can supersede the request while the walk
        // is in flight. Promote only if the current state is still active;
        // never expose the complete rows through server-mode filtering.
        const current = currentRepositoryRequest()
        if (mounted && hasCurrentAuthority && current.active && repositoryMode.value === 'server') {
          repos.value = next
          repositoryMode.value = 'client'
          repositoryPage.value = 1
          repositoryCursor.value = null
          repositoryPageInfo.value = null
          loaded.value = true
          error.value = null
        }
        return
      }

      repos.value = next
      repositoryMode.value = 'client'
      repositoryPage.value = 1
      repositoryCursor.value = null
      repositoryPageInfo.value = null
    } else {
      const next = await api.listRepositoriesPage({
        limit: request.pageSize,
        ...(request.cursor ? { continue: request.cursor } : {}),
      })
      if (!repositoryRequestIsCurrent(requestID, request)) return
      repos.value = next.items
      repositoryCursor.value = request.cursor
      const nextPageInfo = toRepositoryPageInfo(next.continue)
      repositoryPageInfo.value = nextPageInfo
      // Even a terminal server page remains server-owned until a query/filter
      // asks for local authority. A partial page can prove a tombstone is
      // present but not absence; a terminal first page may reconcile safely
      // without changing pagination mode.
      next.items.filter(item => item.deletionTimestamp).forEach(item => props.deletions.acknowledge(deletionScope, item.name, item.uid))
      if (isCompleteFirstCursorPage({
        page: request.page,
        cursor: request.cursor,
        pageInfo: nextPageInfo,
      })) props.deletions.reconcile(deletionScope, next.items)
    }
    loaded.value = true
    error.value = null
  } catch (e) {
    if (!repoRefresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (repoRefresh.isCurrent(requestID)) {
      loading.value = false
      poller.schedule()
    }
  }
})

connectionRefresh = createLatestRefreshController(async (requestID, mode) => {
  connectionsRefreshMode.value = mode
  connectionsLoading.value = true
  try {
    const next = await api.listConnections()
    if (!connectionRefresh.isCurrent(requestID)) return
    connections.value = next
    next.filter(item => item.deletionTimestamp).forEach(item => props.deletions.acknowledge('connection', item.name, item.uid))
    props.deletions.reconcile('connection', next)
    connectionsLoaded.value = true
    connectionsError.value = null
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

onMounted(() => {
  mounted = true
})
onActivated(() => {
  load()
  poller.schedule()
})
onUnmounted(() => {
  mounted = false
  poller.stop()
  repoRefresh.stop()
  connectionRefresh.stop()
})
</script>

<template>
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">Repositories</h2>
        <p class="page-meta">Repositories the provider manages on the git host. Click one to manage deploy keys and collaborators.</p>
      </div>
      <button v-if="!showFirstRun" class="k-btn k-btn--primary" :disabled="!loaded || !connectionsLoaded" @click="emit('create')">
        New repository
      </button>
    </header>

    <span v-if="connectionsLoading && !connectionsLoaded" class="sr-only" role="status" aria-live="polite">Loading connections…</span>
    <div v-if="connectionsError" class="error read-error" role="alert" aria-live="assertive">
      <span>{{ connectionsLoaded ? 'Showing cached connection choices. ' : '' }}{{ connectionsError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="loadConnections()">Retry connections</button>
    </div>
    <span v-else-if="connectionsLoading && connectionsLoaded" class="sr-only" role="status" aria-live="polite">Updating connections…</span>
    <p v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">{{ mutationError }}</p>
    <FirstRunGuide
      v-if="showFirstRun"
      :title="firstRun.title"
      :description="firstRun.description"
      :primary-label="firstRun.primary.label"
      :steps="CODE_JOURNEY_STEPS"
      :current-step="firstRun.currentStep"
      journey-label="Code setup path"
      @primary="handleFirstRun(firstRun.primary.action)"
    >
      <template #icon>
        <Link2 v-if="firstRun.currentStep === 0" :stroke-width="1.5" />
        <GitBranch v-else :stroke-width="1.5" />
      </template>
    </FirstRunGuide>

    <ResourceTable
      v-else
      :columns="columns"
      :rows="rows"
      aria-label="Repositories"
      searchable
      search-placeholder="Search repositories…"
      :filters="repositoryFilterDefinitions"
      :pagination-mode="repositoryMode"
      :page="repositoryPage"
      :page-size="repositoryPageSize"
      :query="repositoryQuery"
      :filter-values="repositoryFiltersValue"
      :cursor="repositoryCursor"
      :page-info="repositoryPageInfo"
      row-key="name"
      :loaded="loaded"
      :loading="loading"
      :refresh-mode="repositoryRefreshMode"
      :error="error"
      :stale="loaded && !!error"
      retryable
      empty-text="No repositories yet."
      :row-aria-label="(row) => `Open repository ${String(row.repo || row.name)}`"
      @retry="loadRepositories"
      @change="handleRepositoryChange"
      @row-click="openRepository"
    >
      <template #name="{ value, row }"><span v-if="row.deleting">{{ row.repo || value }}</span><button v-else class="k-btn k-btn--ghost k-table-resource-link" type="button" @click.stop="openRepository(row)">{{ row.repo || value }}</button></template>
      <template #connectionRef="{ value }">{{ value }}</template>
      <template #visibility="{ value }">{{ value }}</template>
      <template #url="{ row }"><a v-if="row.htmlURL && !row.deleting" :href="String(row.htmlURL)" target="_blank" rel="noopener" @click.stop>open <ExternalLink :size="12" aria-hidden="true" /></a><span v-else class="muted">—</span></template>
      <template #status="{ row }"><StatusBadge :status="String(row.status)" :tone="row.deleting ? 'warning' : null" :title="String(row.message || '')" /></template>
      <template #actions="{ row }">
        <div class="code-row-actions">
          <ResourceTableDeleteButton
            :label="`Delete repository ${String(row.repo || row.name)}`"
            :busy-label="`Deleting repository ${String(row.repo || row.name)}…`"
            :busy="Boolean(row.deleting) || operations.phase(operationKey('repository', String(row.name))) === 'deleting'"
            :disabled="Boolean(row.deleting) || operations.isLocked(operationKey('repository', String(row.name)))"
            @click="remove(row)"
          />
        </div>
      </template>
    </ResourceTable>
  </section>
</template>
