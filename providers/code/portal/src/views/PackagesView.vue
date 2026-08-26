<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { CornerDownRight, ExternalLink } from 'lucide-vue-next'
import { api } from '../api'
import type { ErrorResponse, PackageRow } from '../types'
import ResourceTable from '../portalkit/ResourceTable.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { isCompleteFirstCursorPage, type ResourceTableChange } from '../portalkit/table'
import {
  FAST_REFRESH_MS,
  STABLE_REFRESH_MS,
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  type LatestRefreshController,
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

const emit = defineEmits<{ (e: 'open', repositoryRef: string): void }>()

const packages = ref<PackageRow[]>([])
const error = ref<string | null>(null)
const loading = ref(false)
const loaded = ref(false)
const packageMode = ref<PackagePaginationMode>('server')
const packagePage = ref(1)
const packagePageSize = ref(PACKAGE_PAGE_SIZE)
const packageQuery = ref('')
const packageFilters = ref<PackageFilterValues>(clonePackageFilters(EMPTY_PACKAGE_FILTERS))
const packageCursor = ref<string | null>(null)
const pageInfo = ref<PackagePageInfo | null>(null)
const packageFullRead = createFullListReadCoordinator(() => api.listAllPackages())
const columns = [
  { key: 'repositoryRef', label: 'Repository' },
  { key: 'name', label: 'Package' },
  { key: 'type', label: 'Type' },
  { key: 'visibility', label: 'Visibility' },
  { key: 'versionCount', label: 'Versions' },
  { key: 'status', label: 'Status' },
  { key: 'url', label: '' },
]
function controllerCaughtUp(resource: { generation?: number; observedGeneration?: number }): boolean {
  return resource.generation === undefined ||
    (resource.observedGeneration !== undefined && resource.observedGeneration >= resource.generation)
}
const rows = computed<Array<Record<string, unknown>>>(() => {
  let previousRepository = ''
  return [...packages.value]
    .sort((a, b) => a.repositoryRef.localeCompare(b.repositoryRef) || a.type.localeCompare(b.type) || a.name.localeCompare(b.name))
    .map(item => {
      const showRepository = item.repositoryRef !== previousRepository
      previousRepository = item.repositoryRef
      const deleting = !!item.deletionTimestamp
      return {
        ...item,
        visibility: packageVisibility(item.visibility),
        deleting,
        rowKey: `${item.repositoryRef}:${item.uid || `${item.type}/${item.name}`}`,
        showRepository,
        status: deleting ? 'Deleting' : !controllerCaughtUp(item) ? 'pending' : item.ready ? 'ready' : item.message ? 'failed' : 'pending',
        url: item.htmlURL || '',
      }
    })
})

let refresh!: LatestRefreshController
let forcePackageFullRead = false
const refreshMode = ref<ResourceRefreshMode>('foreground')
const poller = createAdaptiveRefreshTimer(() => load('background'), () => {
  if (!loaded.value || error.value) return FAST_REFRESH_MS
  const pending = rows.value.some(row => row.status === 'pending' || row.status === 'Deleting')
  return pending ? FAST_REFRESH_MS : STABLE_REFRESH_MS
})

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function load(mode: ResourceRefreshMode = 'foreground', forceFullRead = packageMode.value === 'client') {
  if (forceFullRead) forcePackageFullRead = true
  if (mode === 'foreground') {
    refreshMode.value = 'foreground'
    loading.value = true
  }
  refresh.request(mode)
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
  return refresh.isCurrent(requestID) &&
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

refresh = createLatestRefreshController(async (requestID, mode) => {
  refreshMode.value = mode
  const request = currentPackageRequest()
  const forceFullRead = forcePackageFullRead
  forcePackageFullRead = false
  loading.value = true
  // A page from the inactive server query must never be rendered as though it
  // matched a newly-entered search/filter. Keep old rows for same-page polling,
  // but clear them when switching to the complete client-side query.
  if (request.active && request.mode === 'server') {
    packages.value = []
    pageInfo.value = null
  }
  try {
    if (request.active || request.mode === 'client') {
      const next = await packageFullRead.read(forceFullRead)
      if (!packageRequestIsCurrent(requestID, request)) {
        // The complete read is independent of query/filter state. Promote the
        // newest active state immediately when an older request was superseded,
        // but never expose complete rows through a server-mode table.
        const current = currentPackageRequest()
        if (packageFullRead.peek() !== null && current.active && packageMode.value === 'server') {
          packages.value = next
          packageMode.value = 'client'
          packagePage.value = 1
          packageCursor.value = null
          pageInfo.value = null
          loaded.value = true
          error.value = null
        }
        return
      }

      packages.value = next
      packageMode.value = 'client'
      packagePage.value = 1
      packageCursor.value = null
      pageInfo.value = null
    } else {
      const next = await api.listAllPackagesPage({
        limit: request.pageSize,
        ...(request.cursor ? { continue: request.cursor } : {}),
      })
      if (!packageRequestIsCurrent(requestID, request)) return
      packages.value = next.items
      packageCursor.value = request.cursor
      const nextPageInfo = toPackagePageInfo(next.continue)
      pageInfo.value = nextPageInfo
      // Keep server ownership until an active query/filter asks to reuse this
      // terminal page. The metadata is retained for that transition.
    }
    loaded.value = true
    error.value = null
  } catch (e) {
    if (!packageRequestIsCurrent(requestID, request)) return
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (refresh.isCurrent(requestID)) {
      loading.value = false
      poller.schedule()
    }
  }
})

function handlePackageChange(change: ResourceTableChange) {
  const wasClientMode = packageMode.value === 'client'
  const canReuseCurrentServerPage = !wasClientMode &&
    (change.reason === 'query' || change.reason === 'filter') &&
    isCompleteFirstCursorPage({
      page: packagePage.value,
      cursor: packageCursor.value,
      pageInfo: pageInfo.value,
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
  pageInfo.value = null

  if (!hasActivePackageFilters(next.query, next.filters)) {
    // A pending active-query walk can still exist while mode is server; clear
    // invalidates it before the queued first server page is allowed to commit.
    packageFullRead.clear()
    if (transition.clearRows) packages.value = []
    if (transition.reload) load('foreground', false)
    return
  }

  // A complete first page is a local source immediately; active filtering
  // should not trigger an unnecessary workspace walk.
  if (canReuseCurrentServerPage) {
    packageFullRead.seed(packages.value)
    packageMode.value = 'client'
    packagePage.value = 1
    packageCursor.value = null
    pageInfo.value = null
    return
  }

  // Preserve a prior complete result across rapid query/filter transitions,
  // including while a server-page response is still in flight.
  const cachedFullRows = packageFullRead.peek()
  if (cachedFullRows) {
    packages.value = cachedFullRows
    packageMode.value = 'client'
    packagePage.value = 1
    packageCursor.value = null
    pageInfo.value = null
    return
  }

  if (transition.clearRows) packages.value = []
  if (transition.reload) load()
}

onMounted(() => {
  load()
  poller.schedule()
})
onUnmounted(() => {
  poller.stop()
  refresh.stop()
})
</script>

<template>
  <section class="page">
    <header class="page-head">
      <div>
        <h2 class="page-title">Packages</h2>
        <p class="page-meta">Artifacts (container images, npm/maven packages, …) published under the workspace's repositories. Observed state — they appear automatically when artifacts are pushed.</p>
      </div>
    </header>

    <ResourceTable
      :columns="columns"
      :rows="rows"
      searchable
      search-placeholder="Search packages…"
      :filters="PACKAGE_FILTERS"
      :pagination-mode="packageMode"
      :page="packagePage"
      :page-size="packagePageSize"
      :query="packageQuery"
      :filter-values="packageFilters"
      :cursor="packageCursor"
      :page-info="pageInfo"
      row-key="rowKey"
      :loaded="loaded"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      :stale="loaded && !!error"
      retryable
      empty-text="No packages published in this workspace yet."
      :interactive="false"
      @retry="load"
      @change="handlePackageChange"
    >
      <template #repositoryRef="{ row }">
        <button v-if="row.showRepository && !row.deleting" class="k-btn k-btn--ghost k-table-resource-link" type="button" @click="emit('open', String(row.repositoryRef))">{{ row.repositoryRef }}</button>
        <span v-else-if="row.showRepository">{{ row.repositoryRef }}</span>
        <CornerDownRight v-else class="muted" :size="14" aria-label="Same repository as above" />
      </template>
      <template #name="{ row }"><strong><a v-if="row.htmlURL && !row.deleting" :href="String(row.htmlURL)" target="_blank" rel="noopener">{{ row.name }}</a><template v-else>{{ row.name }}</template></strong></template>
      <template #type="{ value }"><span class="k-badge k-badge--muted">{{ value }}</span></template>
      <template #visibility="{ value }"><span class="muted">{{ value === 'unknown' ? '—' : value }}</span></template>
      <template #versionCount="{ value }"><span class="muted">{{ value || 0 }}</span></template>
      <template #status="{ row }"><StatusBadge :status="String(row.status)" :tone="row.deleting ? 'warning' : null" :title="String(row.message || '')" /></template>
      <template #url="{ row }"><a v-if="row.htmlURL && !row.deleting" :href="String(row.htmlURL)" target="_blank" rel="noopener">View <ExternalLink :size="12" aria-hidden="true" /></a></template>
    </ResourceTable>
    <p class="muted">Packages appear automatically when artifacts are pushed (e.g. <code>docker push</code>, <code>npm publish</code>); the provider crawls each repository periodically.</p>
  </section>
</template>
