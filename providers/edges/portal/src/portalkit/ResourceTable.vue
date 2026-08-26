<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies under providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`. -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { AlertCircle, ChevronLeft, ChevronRight, Inbox, Search, X } from 'lucide-vue-next'
import type { ResourceRefreshMode } from '../portalkit/page-state'
import {
  cursorPageRange,
  deriveTableFilterOptions,
  filterTableRows,
  paginateTableRows,
  tablePageCount,
  tableRange,
  type ResourceTableChange,
  type TableFilterDefinition,
  type TableFilterState,
  type TablePageInfo,
  type TablePaginationMode,
} from './table'

const props = withDefaults(defineProps<{
  columns: Array<{ key: string; label: string }>
  rows: Array<Record<string, unknown>>
  /** Queryable is the default/current resource-list contract. Simple is an explicit bounded-list opt-in. */
  variant?: 'queryable' | 'simple'
  /** Stable row identity. Resource names/ids are used when omitted. */
  rowKey?: string | ((row: Record<string, unknown>, index: number) => string | number)
  /** True after the first authoritative read has completed successfully. */
  loaded?: boolean | null
  /** True while a read is in flight. Cached rows remain visible after load. */
  loading?: boolean
  /** Controls whether an empty authoritative body shows visible refresh progress. */
  refreshMode?: ResourceRefreshMode
  error?: string | null
  /** Marks cached rows as stale when the latest read failed. */
  stale?: boolean
  /** Shows the built-in Retry action. Callers must handle the retry event. */
  retryable?: boolean
  emptyText?: string
  filterEmptyText?: string
  interactive?: boolean
  searchable?: boolean
  searchPlaceholder?: string
  searchKeys?: string[]
  filters?: TableFilterDefinition[]
  /** Legacy client-side pagination switch. Server mode uses paginationMode. */
  paginated?: boolean
  pageSize?: number
  pageSizeOptions?: number[]
  /** `client` preserves the legacy local filtering/paging behavior. */
  paginationMode?: TablePaginationMode
  /** Controlled page for server mode (one-based). */
  page?: number
  /** Controlled query; server mode never applies it to supplied rows. */
  query?: string
  /** Controlled selected filter values. Server definitions must provide options. */
  filterValues?: TableFilterState
  /** Cursor used to fetch the controlled page. Values are opaque. */
  cursor?: string | null
  /** Metadata for the supplied server page; total is optional. */
  pageInfo?: TablePageInfo | null
  /** Accessible name for an interactive row. A function can derive it from the row. */
  rowAriaLabel?: string | ((row: Record<string, unknown>, index: number) => string)
}>(), {
  // Vue casts an omitted Boolean prop to false in child components. A null
  // sentinel preserves omission so legacy callers retain loading -> content
  // behavior while explicit false still means the first read is incomplete.
  loaded: null,
  refreshMode: 'foreground',
  variant: 'queryable',
  stale: false,
  retryable: false,
  emptyText: 'No data',
  filterEmptyText: 'No resources match these filters.',
  interactive: true,
  searchable: false,
  searchPlaceholder: 'Search…',
  searchKeys: () => [],
  filters: () => [],
  paginated: false,
  pageSize: 10,
  pageSizeOptions: () => [10, 25, 50],
  paginationMode: 'client',
})

const query = ref('')
const page = ref(1)
const selectedPageSize = ref(normalizePageSize(props.pageSize))
const selectedFilters = reactive<Record<string, string>>({})

// `undefined` means that no request cursor for that page has been saved yet;
// null is a real, known first-page cursor. This distinction keeps Previous
// disabled when a caller lands on a page without cursor history.
const cursorHistory = ref<Array<string | null | undefined>>(
  normalizePage(props.page) > 1
    ? Array.from({ length: normalizePage(props.page) }, () => undefined)
    : [props.cursor ?? null],
)

const isServerPagination = computed(() => props.paginationMode === 'server')
const paginationEnabled = computed(() => props.variant === 'queryable' && (props.paginated || isServerPagination.value))
const currentPage = computed(() => normalizePage(props.page ?? page.value))
const currentPageSize = computed(() => normalizePageSize(selectedPageSize.value))
const currentQuery = computed(() => props.variant === 'simple'
  ? ''
  : props.query === undefined ? query.value : props.query)
const controlledFilterValues = computed(() => props.filterValues)
const currentFilters = computed<TableFilterState>(() => {
  if (props.variant === 'simple') return {}
  const values = controlledFilterValues.value ?? selectedFilters
  return Object.fromEntries(Object.entries(values).map(([key, value]) => [key, String(value ?? '')]))
})
const filterSignature = computed(() => Object.entries(currentFilters.value)
  .sort(([left], [right]) => left.localeCompare(right))
  .map(([key, value]) => `${key}\u0000${value}`)
  .join('\u0001'))

const explicitReadState = computed(() => props.loaded !== null)
const showInitialError = computed(() =>
  explicitReadState.value ? props.loaded === false && !!props.error : !!props.error,
)
const showInitialLoading = computed(() =>
  explicitReadState.value ? props.loaded === false : !!props.loading,
)
const ariaBusy = computed(() =>
  explicitReadState.value
    ? (!!props.loading && !(props.loaded === false && !!props.error)) || (props.loaded === false && !props.error)
    : !!props.loading,
)
const filterOptions = computed(() => Object.fromEntries(
  props.filters.map(definition => [definition.key, isServerPagination.value
    ? (definition.options ?? [])
    : deriveTableFilterOptions(props.rows, definition)]),
))
const filteredRows = computed(() => {
  if (isServerPagination.value) return props.rows
  return filterTableRows(
    props.rows,
    currentQuery.value,
    props.searchKeys.length ? props.searchKeys : props.columns.map(column => column.key).filter(key => key !== 'actions'),
    currentFilters.value,
  )
})
const visibleRows = computed(() => isServerPagination.value
  ? props.rows
  : paginationEnabled.value
    ? paginateTableRows(filteredRows.value, currentPage.value, currentPageSize.value)
    : filteredRows.value,
)
const serverTotal = computed(() => {
  if (!isServerPagination.value || props.pageInfo?.total === undefined) return null
  return normalizeTotal(props.pageInfo.total)
})
const serverHasNext = computed(() => {
  if (!isServerPagination.value || !props.pageInfo) return false
  return props.pageInfo.hasNext ?? (props.pageInfo.nextCursor !== null && props.pageInfo.nextCursor !== undefined)
})
const serverNextCursor = computed(() =>
  isServerPagination.value ? props.pageInfo?.nextCursor ?? null : null,
)
const totalPages = computed(() => {
  if (isServerPagination.value) {
    return serverTotal.value === null ? currentPage.value : tablePageCount(serverTotal.value, currentPageSize.value)
  }
  return tablePageCount(filteredRows.value.length, currentPageSize.value)
})
const visibleRange = computed(() => isServerPagination.value
  ? cursorPageRange(currentPage.value, currentPageSize.value, visibleRows.value.length, serverTotal.value)
  : tableRange(filteredRows.value.length, currentPage.value, currentPageSize.value))
const activeFilters = computed(() => !!currentQuery.value.trim() || Object.values(currentFilters.value).some(Boolean))
const showPendingBody = computed(() =>
  props.refreshMode === 'foreground' && !!props.loading && visibleRows.value.length === 0,
)
const pendingBodyText = computed(() => activeFilters.value ? 'Searching resources' : 'Loading resources')
const hasConfiguredControls = computed(() =>
  props.variant === 'queryable' && (props.searchable || props.filters.length > 0),
)
const showControls = computed(() =>
  hasConfiguredControls.value
    && (isServerPagination.value || props.rows.length > 0 || activeFilters.value || !!props.loading),
)
const showPagination = computed(() => {
  if (!paginationEnabled.value) return false
  if (!isServerPagination.value) return props.rows.length > 0
  return props.rows.length > 0
    || currentPage.value > 1
    || (serverTotal.value !== null && serverTotal.value > 0)
    || serverHasNext.value
})
const normalizedPageSizes = computed(() => [...new Set([...props.pageSizeOptions, props.pageSize])]
  .filter(value => Number.isFinite(value) && value > 0)
  .sort((left, right) => left - right))
const canPrevious = computed(() => {
  if (currentPage.value <= 1) return false
  if (!isServerPagination.value) return true
  return cursorHistory.value[currentPage.value - 2] !== undefined
})
const canNext = computed(() => {
  if (!isServerPagination.value) return currentPage.value < totalPages.value && filteredRows.value.length > 0
  return serverHasNext.value && serverNextCursor.value !== null
})

const emit = defineEmits<{
  rowClick: [row: Record<string, unknown>]
  retry: []
  /** One typed state event covers page changes and query-shape resets. */
  change: [change: ResourceTableChange]
  'update:page': [page: number]
  'update:pageSize': [pageSize: number]
  'update:query': [query: string]
  'update:filterValues': [filters: TableFilterState]
}>()

watch(() => props.pageSize, value => {
  const next = normalizePageSize(value)
  if (selectedPageSize.value !== next) selectedPageSize.value = next
  if (props.page === undefined) page.value = 1
  if (isServerPagination.value) resetCursorHistory()
})
watch([currentQuery, filterSignature, selectedPageSize], () => {
  if (props.page === undefined) page.value = 1
})
watch([currentQuery, filterSignature], () => {
  if (isServerPagination.value) resetCursorHistory()
})
watch(totalPages, value => {
  if (!isServerPagination.value && props.page === undefined) page.value = Math.min(page.value, value)
})
watch(() => props.filters.map(filter => filter.key), keys => {
  keys.forEach(key => { if (!(key in selectedFilters)) selectedFilters[key] = '' })
  Object.keys(selectedFilters).forEach(key => { if (!keys.includes(key)) delete selectedFilters[key] })
}, { immediate: true })
watch([currentPage, () => props.cursor], ([nextPage, cursor]) => {
  if (cursor !== undefined) rememberCursor(nextPage, cursor)
}, { immediate: true })

function normalizePage(value: number | undefined): number {
  return Number.isFinite(value) && (value ?? 0) > 0 ? Math.floor(value as number) : 1
}

function normalizePageSize(value: number | undefined): number {
  return Number.isFinite(value) && (value ?? 0) > 0 ? Math.floor(value as number) : 1
}

function normalizeTotal(value: number | null | undefined): number | null {
  if (value === null || value === undefined) return null
  return Number.isFinite(value) && value >= 0 ? Math.floor(value) : null
}

function rememberCursor(nextPage: number, cursor: string | null) {
  const history = [...cursorHistory.value]
  while (history.length < nextPage) history.push(undefined)
  history[nextPage - 1] = cursor
  cursorHistory.value = history
}

function resetCursorHistory() {
  cursorHistory.value = [null]
}

function snapshotFilters(): TableFilterState {
  return { ...currentFilters.value }
}

function emitChange(
  reason: ResourceTableChange['reason'],
  nextPage: number,
  cursor: string | null,
  overrides: Partial<Pick<ResourceTableChange, 'query' | 'filters'>> = {},
) {
  emit('change', {
    reason,
    page: nextPage,
    pageSize: currentPageSize.value,
    query: overrides.query ?? currentQuery.value,
    filters: overrides.filters ?? snapshotFilters(),
    cursor,
  })
}

function setPage(nextPage: number, cursor: string | null) {
  const target = Math.max(1, Math.floor(nextPage))
  if (props.page === undefined) page.value = target
  emit('update:page', target)
  emitChange('page', target, cursor)
}

function setPageSize(value: number) {
  const target = normalizePageSize(value)
  selectedPageSize.value = target
  resetCursorHistory()
  if (props.page === undefined) page.value = 1
  emit('update:pageSize', target)
  emit('update:page', 1)
  emitChange('page-size', 1, null)
}

function setQuery(value: string) {
  if (props.query === undefined) query.value = value
  resetCursorHistory()
  if (props.page === undefined) page.value = 1
  emit('update:query', value)
  emit('update:page', 1)
  emitChange('query', 1, null, { query: value })
}

function setFilter(key: string, value: string) {
  const next = { ...snapshotFilters(), [key]: value }
  if (controlledFilterValues.value === undefined) selectedFilters[key] = value
  resetCursorHistory()
  if (props.page === undefined) page.value = 1
  emit('update:filterValues', next)
  emit('update:page', 1)
  emitChange('filter', 1, null, { filters: next })
}

function rowIdentity(row: Record<string, unknown>, index: number): string | number {
  if (typeof props.rowKey === 'function') return props.rowKey(row, index)
  if (typeof props.rowKey === 'string') {
    const value = row[props.rowKey]
    if (typeof value === 'string' || typeof value === 'number') return value
  }
  for (const key of ['name', 'id', 'uid']) {
    const value = row[key]
    if (typeof value === 'string' || typeof value === 'number') return value
  }
  return index
}

function clearFilters() {
  const next = Object.fromEntries(props.filters.map(filter => [filter.key, '']))
  if (props.query === undefined) query.value = ''
  if (controlledFilterValues.value === undefined) {
    Object.keys(selectedFilters).forEach(key => { selectedFilters[key] = '' })
  }
  resetCursorHistory()
  if (props.page === undefined) page.value = 1
  emit('update:query', '')
  emit('update:filterValues', next)
  emit('update:page', 1)
  emitChange('filter', 1, null, { query: '', filters: next })
}

function previousPage() {
  if (!canPrevious.value) return
  const target = currentPage.value - 1
  const cursor = isServerPagination.value ? cursorHistory.value[target - 1] : null
  if (cursor === undefined) return
  setPage(target, cursor)
}

function nextPage() {
  if (!canNext.value) return
  const cursor = isServerPagination.value ? serverNextCursor.value : null
  if (isServerPagination.value && cursor === null) return
  const target = currentPage.value + 1
  if (isServerPagination.value) rememberCursor(target, cursor)
  setPage(target, cursor)
}

function rowAriaLabel(row: Record<string, unknown>, index: number): string | undefined {
  const label = typeof props.rowAriaLabel === 'function'
    ? props.rowAriaLabel(row, index)
    : props.rowAriaLabel
  return label || undefined
}

function isExplicitControlTarget(event: Event): boolean {
  const currentTarget = event.currentTarget as Element | null
  const target = event.target as Element | null
  if (!target || target === currentTarget) return false
  const element = target as Element | null
  const control = element?.closest?.(
    'a, button, input, select, textarea, summary, [contenteditable="true"], [role="button"], [role="link"], [tabindex]:not([tabindex="-1"])',
  )
  return Boolean(control && control !== currentTarget)
}

function onRowClick(row: Record<string, unknown>, event?: MouseEvent | KeyboardEvent) {
  if (!props.interactive || (event && isExplicitControlTarget(event))) return
  emit('rowClick', row)
}

function onRowKeydown(row: Record<string, unknown>, event: KeyboardEvent) {
  if (!props.interactive || event.repeat || isExplicitControlTarget(event)) return
  if (event.key !== 'Enter' && event.key !== ' ' && event.key !== 'Spacebar') return
  event.preventDefault()
  onRowClick(row, event)
}
</script>

<template>
  <div
    class="k-table k-table--resource"
    :class="`k-table--${variant}`"
    :aria-busy="ariaBusy"
  >
    <!-- Keep the live region outside layout so background reads cannot move the table. -->
    <span
      class="k-table__live"
      role="status"
      aria-live="polite"
      aria-atomic="true"
      style="block-size: 1px; clip: rect(0 0 0 0); clip-path: inset(50%); inline-size: 1px; margin: -1px; overflow: hidden; padding: 0; position: absolute; white-space: nowrap;"
    >
      {{ explicitReadState && loading && loaded ? 'Updating…' : '' }}
    </span>
    <div v-if="showInitialError" class="k-table__error" role="alert" aria-live="assertive">
      <AlertCircle class="k-table__error-icon" :stroke-width="1.75" />
      <span class="k-table__error-message">{{ error }}</span>
      <button v-if="retryable" class="k-table__retry" type="button" @click="emit('retry')">Retry</button>
    </div>

    <div v-else-if="showInitialLoading" class="k-table__loading" role="status" aria-live="polite" aria-label="Loading resources">
      <div v-if="hasConfiguredControls" class="k-table__loading-controls" aria-hidden="true">
        <div v-if="searchable" class="shimmer k-table__loading-control k-table__loading-control--search" />
        <div v-for="filter in filters" :key="filter.key" class="shimmer k-table__loading-control k-table__loading-control--filter" />
        <div v-if="activeFilters" class="shimmer k-table__loading-control k-table__loading-control--clear" />
      </div>
      <div class="k-table__loading-head">
        <div class="shimmer k-table__skeleton k-table__skeleton--short" />
      </div>
      <div v-for="i in 5" :key="i" class="k-table__loading-row">
        <div class="shimmer k-table__skeleton k-table__skeleton--wide" />
        <div class="shimmer k-table__skeleton k-table__skeleton--mid" />
        <div class="shimmer k-table__skeleton k-table__skeleton--small" />
      </div>
    </div>

    <template v-else>
      <div v-if="explicitReadState && error" class="k-table__stale" role="alert" aria-live="assertive">
        <AlertCircle class="k-table__error-icon" :stroke-width="1.75" />
        <span class="k-table__error-message">
          {{ stale ? 'Showing the last successful result. ' : '' }}{{ error }}
        </span>
        <button v-if="retryable" class="k-table__retry" type="button" @click="emit('retry')">Retry</button>
      </div>

      <div v-if="showControls" class="k-table__controls" role="search" aria-label="Filter table">
        <label v-if="searchable" class="k-table__search">
          <span class="sr-only" style="position:absolute;block-size:1px;inline-size:1px;overflow:hidden;clip:rect(0 0 0 0)">Search table</span>
          <Search class="k-table__search-icon" :stroke-width="1.75" aria-hidden="true" />
          <input :value="currentQuery" class="k-table__search-input" type="search" :placeholder="searchPlaceholder" autocomplete="off" @input="setQuery(($event.target as HTMLInputElement).value)">
          <button v-if="currentQuery" class="k-table__search-clear" type="button" aria-label="Clear search" @click="setQuery('')"><X :stroke-width="1.75" /></button>
        </label>
        <label v-for="filter in filters" :key="filter.key">
          <span class="sr-only" style="position:absolute;block-size:1px;inline-size:1px;overflow:hidden;clip:rect(0 0 0 0)">Filter by {{ filter.label }}</span>
          <select :value="currentFilters[filter.key] || ''" class="k-table__filter-select" :aria-label="`Filter by ${filter.label}`" @change="setFilter(filter.key, ($event.target as HTMLSelectElement).value)">
            <option value="">{{ filter.allLabel || `All ${filter.label.toLocaleLowerCase()}` }}</option>
            <option v-for="option in filterOptions[filter.key]" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>
        </label>
        <button v-if="activeFilters" class="k-table__clear-filters" type="button" @click="clearFilters">Clear filters</button>
      </div>

      <div class="k-table__scroll" role="region" aria-label="Scrollable table" tabindex="0">
        <table class="k-table__table">
          <thead><tr class="k-table__head-row"><th v-for="col in columns" :key="col.key" class="k-table__heading">{{ col.label }}</th></tr></thead>
          <tbody>
            <template v-for="(row, i) in visibleRows" :key="rowIdentity(row, i)">
              <tr
                class="stagger-item k-table__row"
                :class="{ 'k-table__row--interactive': interactive }"
                :tabindex="interactive ? 0 : undefined"
                :aria-label="interactive ? rowAriaLabel(row, i) : undefined"
                :style="{ animationDelay: `${i * 35}ms` }"
                @click="onRowClick(row, $event)"
                @keydown="onRowKeydown(row, $event)"
              >
                <td v-for="col in columns" :key="col.key" class="k-table__cell">
                  <slot :name="col.key" :value="row[col.key]" :row="row">{{ row[col.key] }}</slot>
                </td>
              </tr>
              <slot name="after-row" :row="row" />
            </template>
            <tr v-if="showPendingBody"><td :colspan="columns.length" class="k-table__pending-cell" role="status" aria-live="polite">
              <p class="k-table__pending-label">{{ pendingBodyText }}</p>
            </td></tr>
            <tr v-else-if="visibleRows.length === 0"><td :colspan="columns.length" class="k-table__empty-cell">
              <Inbox class="k-table__empty-icon" :stroke-width="1.25" />
              <p class="k-table__empty-label">{{ activeFilters ? filterEmptyText : emptyText }}</p>
            </td></tr>
          </tbody>
        </table>
      </div>

      <footer v-if="showPagination" class="k-table__pagination" aria-label="Table pagination">
        <div class="k-table__range" aria-live="polite">
          <template v-if="isServerPagination && serverTotal !== null && visibleRows.length">Showing <strong>{{ visibleRange.start }}–{{ visibleRange.end }}</strong> of <strong>{{ serverTotal }}</strong></template>
          <template v-else-if="isServerPagination && visibleRows.length">Showing <strong>{{ visibleRange.start }}–{{ visibleRange.end }}</strong></template>
          <template v-else-if="filteredRows.length">Showing <strong>{{ visibleRange.start }}–{{ visibleRange.end }}</strong> of <strong>{{ filteredRows.length }}</strong></template>
          <template v-else>Showing <strong>0</strong> results</template>
        </div>
        <label class="k-table__page-size">Rows per page
          <select :value="currentPageSize" class="k-table__page-size-select" aria-label="Rows per page" @change="setPageSize(Number(($event.target as HTMLSelectElement).value))">
            <option v-for="size in normalizedPageSizes" :key="size" :value="size">{{ size }}</option>
          </select>
        </label>
        <div class="k-table__page-actions">
          <button class="k-table__page-button" type="button" aria-label="Previous page" :disabled="!canPrevious" @click="previousPage"><ChevronLeft :stroke-width="1.75" /></button>
          <span class="k-table__page-indicator" aria-live="polite">
            <template v-if="isServerPagination && serverTotal === null">Page {{ currentPage }}</template>
            <template v-else>{{ currentPage }} / {{ totalPages }}</template>
          </span>
          <button class="k-table__page-button" type="button" aria-label="Next page" :disabled="!canNext" @click="nextPage"><ChevronRight :stroke-width="1.75" /></button>
        </div>
      </footer>
    </template>
  </div>
</template>
