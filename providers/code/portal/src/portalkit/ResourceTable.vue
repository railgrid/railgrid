<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies under providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`. -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, useId, watch } from 'vue'
import { AlertCircle, ChevronLeft, ChevronRight, Inbox, Info, Search, X } from 'lucide-vue-next'
import type { ResourceRefreshMode } from '../portalkit/page-state'
import {
  cursorPageRange,
  deriveTableFilterOptions,
  filterTableRows,
  paginateTableRows,
  tablePageCount,
  tableRange,
  pruneClientSelectionKeys,
  selectionPageState,
  uniqueSelectionKeys,
  updatePageSelection,
  type TableSelectionKey,
  type ResourceTableChange,
  type TableFilterDefinition,
  type TableFilterState,
  type TablePageInfo,
  type TablePaginationMode,
} from './table'
import ResourceTableFilter from './ResourceTableFilter.vue'
import { useDelayedLoading } from './useDelayedLoading'

type ResourceTableColumn = {
  key: string
  label: string
  /** Accessible name for a visually blank header (for example an expand control). */
  ariaLabel?: string
  /** Full rendered value for truncation disclosure when a slot does not display row[key] verbatim. */
  fullValue?: (row: Record<string, unknown>) => string
  /** Logical cell/header alignment. Defaults to start. */
  align?: 'start' | 'center' | 'end'
  /** Receives the table's remaining width and hosts row actions. */
  primary?: boolean
}

const CLIENT_FILTER_DEBOUNCE_MS = 100
const MAX_SKELETON_COLUMNS = 6
const PRIMARY_TOOLTIP_GAP = 6
const PRIMARY_TOOLTIP_VIEWPORT_MARGIN = 8

const props = withDefaults(defineProps<{
  columns: ResourceTableColumn[]
  rows: Array<Record<string, unknown>>
  /** Accessible name shared by the semantic table and its scroll affordance. */
  ariaLabel?: string
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
  searchEmptyText?: string
  combinedFilterEmptyText?: string
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
  /** Adds native row checkboxes and a controlled selection bar. */
  selectable?: boolean
  /** Controlled stable resource keys. Bind with v-model:selected-keys. */
  selectedKeys?: TableSelectionKey[]
  /** Disables every selection control while a bulk action is busy or rows are unverified. */
  selectionDisabled?: boolean
  /** Override Clear selection's disabled state, e.g. to leave an unverified selection. */
  selectionClearDisabled?: boolean | null
  /** Return false when a row must not participate in selection. */
  rowSelectable?: (row: Record<string, unknown>) => boolean
  /** Explain why a row cannot be selected; a non-empty reason also disables it. */
  rowSelectionDisabledReason?: (row: Record<string, unknown>) => string
  /** Complete accessible name for a row selection checkbox. */
  selectionLabel?: (row: Record<string, unknown>) => string
}>(), {
  // Vue casts an omitted Boolean prop to false in child components. A null
  // sentinel preserves omission so legacy callers retain loading -> content
  // behavior while explicit false still means the first read is incomplete.
  loaded: null,
  ariaLabel: 'Resource table',
  refreshMode: 'foreground',
  variant: 'queryable',
  stale: false,
  retryable: false,
  emptyText: 'No data',
  filterEmptyText: 'No resources match these filters.',
  searchEmptyText: 'No resources match your search.',
  combinedFilterEmptyText: 'No resources match your search and selected filters.',
  interactive: true,
  searchable: false,
  searchPlaceholder: 'Search…',
  searchKeys: () => [],
  filters: () => [],
  paginated: false,
  pageSize: 10,
  pageSizeOptions: () => [10, 25, 50],
  paginationMode: 'client',
  selectable: false,
  selectedKeys: () => [],
  selectionDisabled: false,
  selectionClearDisabled: null,
})

const componentID = useId()
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
const normalizedSelectedKeys = computed(() => uniqueSelectionKeys(props.selectedKeys))
const selectedKeySet = computed(() => new Set(normalizedSelectedKeys.value))
const selectedCount = computed(() => normalizedSelectedKeys.value.length)
const clearSelectionDisabled = computed(() => props.selectionClearDisabled ?? props.selectionDisabled)
const selectionToolbarActive = computed(() => props.selectable && selectedCount.value > 0)
const duplicateRowKeys = computed(() => {
  const counts = new Map<TableSelectionKey, number>()
  props.rows.forEach(row => {
    const key = stableSelectionKey(row)
    if (key !== null) counts.set(key, (counts.get(key) ?? 0) + 1)
  })
  return new Set([...counts].filter(([, count]) => count > 1).map(([key]) => key))
})
const authoritativeSelectionRows = computed(() => props.rows.flatMap(row => {
  const state = rowSelectionState(row)
  return state.key === null ? [] : [{ key: state.key, selectable: state.selectable }]
}))
const filterSignature = computed(() => Object.entries(currentFilters.value)
  .sort(([left], [right]) => left.localeCompare(right))
  .map(([key, value]) => `${key}\u0000${value}`)
  .join('\u0001'))
// Client-side filtering can walk a complete provider result set. Keep the
// input/filter state responsive and apply the expensive walk after a short
// quiet period so each keystroke does not synchronously scan every row.
const deferredQuery = ref(currentQuery.value)
const deferredFilters = ref<TableFilterState>({ ...currentFilters.value })
const filterPending = ref(false)
const selectionAnnouncement = ref('')
const primaryTooltip = ref<{
  value: string
  left: number
  top: number
  positioned: boolean
} | null>(null)
const primaryTooltipElement = ref<HTMLElement | null>(null)
const headerSelectionCheckbox = ref<HTMLInputElement | null>(null)
const tableScrollRegion = ref<HTMLElement | null>(null)
let activeTooltipAnchor: HTMLElement | null = null
let activeSelectionTooltip: {
  anchor: HTMLElement
  rowIdentity: string | number
  value: string
} | null = null
let primaryTooltipRequest = 0

const explicitReadState = computed(() => props.loaded !== null)
const showInitialError = computed(() =>
  explicitReadState.value ? props.loaded === false && !!props.error : !!props.error,
)
const initialReadPending = computed(() =>
  explicitReadState.value ? props.loaded === false : !!props.loading,
)
const showInitialLoading = useDelayedLoading(initialReadPending)
const ariaBusy = computed(() =>
  explicitReadState.value
    ? (!!props.loading && !(props.loaded === false && !!props.error)) || (props.loaded === false && !props.error) || filterPending.value
    : !!props.loading || filterPending.value,
)
const filterOptions = computed(() => Object.fromEntries(
  props.filters.map(definition => [definition.key, isServerPagination.value
    ? (definition.options ?? [])
    : deriveTableFilterOptions(props.rows, definition)]),
))
const filteredRows = computed(() => {
  if (isServerPagination.value) return props.rows
  if (filterPending.value) return []
  return filterTableRows(
    props.rows,
    deferredQuery.value,
    props.searchKeys.length ? props.searchKeys : props.columns.map(column => column.key).filter(key => key !== 'actions'),
    deferredFilters.value,
  )
})
const visibleRows = computed(() => isServerPagination.value
  ? props.rows
  : paginationEnabled.value
    ? paginateTableRows(filteredRows.value, currentPage.value, currentPageSize.value)
    : filteredRows.value,
)
const eligiblePageKeys = computed(() => uniqueSelectionKeys(visibleRows.value.flatMap(row => {
  const state = rowSelectionState(row)
  return state.selectable && state.key !== null ? [state.key] : []
})))
const selectionOnPage = computed(() => selectionPageState(normalizedSelectedKeys.value, eligiblePageKeys.value))
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
const hasQuery = computed(() => !!currentQuery.value.trim())
const hasFacetFilters = computed(() => Object.values(currentFilters.value).some(Boolean))
const activeFilters = computed(() => hasQuery.value || hasFacetFilters.value)
const confirmedEmptyInventory = computed(() => props.loaded === true
  && props.rows.length === 0
  && !activeFilters.value
  && (!isServerPagination.value || (serverTotal.value === 0 && !serverHasNext.value)))
const selectionSurfaceVisible = computed(() =>
  props.selectable && (!confirmedEmptyInventory.value || selectedCount.value > 0),
)
const clearActionLabel = computed(() => hasQuery.value && hasFacetFilters.value ? 'Clear all' : 'Clear filters')
const noMatchText = computed(() => {
  if (hasQuery.value && hasFacetFilters.value) return props.combinedFilterEmptyText
  if (hasQuery.value) return props.searchEmptyText
  return props.filterEmptyText
})
const tableAriaLabel = computed(() => props.ariaLabel?.trim() || 'Resource table')
const visibleColumns = computed(() => props.columns.filter(column => column.key !== 'actions'))
const actionsColumn = computed(() => props.columns.find(column => column.key === 'actions') ?? null)
const skeletonColumns = computed(() => {
  const columns = visibleColumns.value.slice(0, MAX_SKELETON_COLUMNS)
  return columns.length > 0 ? columns : [{ key: '__skeleton__', label: '' }]
})
const primaryColumnKey = computed(() => {
  const columns = visibleColumns.value
  return columns.find(column => column.primary)?.key
    ?? columns.find(column => column.key === 'name')?.key
    ?? columns[0]?.key
    ?? null
})
const renderedColumnCount = computed(() => Math.max(visibleColumns.value.length + (selectionSurfaceVisible.value ? 1 : 0), 1))
const staleMessageRole = computed(() => props.refreshMode === 'background' ? 'status' : 'alert')
const staleMessageLive = computed(() => props.refreshMode === 'background' ? 'polite' : 'assertive')
const showPendingBody = computed(() =>
  (filterPending.value || (props.refreshMode === 'foreground' && !!props.loading)) && visibleRows.value.length === 0,
)
const pendingBodyText = computed(() => filterPending.value
  ? (activeFilters.value ? 'Searching resources' : 'Updating results')
  : activeFilters.value ? 'Searching resources' : 'Loading resources')
const hasConfiguredControls = computed(() =>
  props.variant === 'queryable' && (props.searchable || props.filters.length > 0),
)
const showControls = computed(() =>
  hasConfiguredControls.value
    && (isServerPagination.value || props.rows.length > 0 || activeFilters.value || !!props.loading),
)
const showPagination = computed(() => {
  if (!paginationEnabled.value) return false
  if (filterPending.value) return false
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
  'update:selectedKeys': [keys: TableSelectionKey[]]
}>()

watch([currentQuery, filterSignature], () => clearSelection(), { flush: 'sync' })
watch(selectedCount, count => {
  selectionAnnouncement.value = count > 0
    ? `${count} ${count === 1 ? 'resource' : 'resources'} selected.`
    : 'Selection cleared.'
}, { flush: 'post' })
watch([
  authoritativeSelectionRows,
  () => props.selectedKeys,
  () => props.loaded,
  () => props.loading,
  () => props.error,
  () => props.stale,
  () => props.selectionDisabled,
  isServerPagination,
  () => props.selectable,
], pruneAuthoritativeClientSelection, { immediate: true, flush: 'post' })
watch(() => props.pageSize, value => {
  const next = normalizePageSize(value)
  if (selectedPageSize.value !== next) selectedPageSize.value = next
  if (props.page === undefined) page.value = 1
  if (isServerPagination.value) resetCursorHistory()
})
watch([currentQuery, filterSignature, selectedPageSize], () => {
  if (props.page === undefined) page.value = 1
})
watch([currentQuery, filterSignature, isServerPagination], ([nextQuery], _previous, onCleanup) => {
  const nextFilters = { ...currentFilters.value }
  if (isServerPagination.value) {
    filterPending.value = false
    deferredQuery.value = nextQuery
    deferredFilters.value = nextFilters
    return
  }

  filterPending.value = true
  const timer = setTimeout(() => {
    deferredQuery.value = nextQuery
    deferredFilters.value = nextFilters
    filterPending.value = false
  }, CLIENT_FILTER_DEBOUNCE_MS)
  onCleanup(() => clearTimeout(timer))
}, { flush: 'post' })
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
  if (value !== currentQuery.value) clearSelection()
  if (props.query === undefined) query.value = value
  resetCursorHistory()
  if (props.page === undefined) page.value = 1
  emit('update:query', value)
  emit('update:page', 1)
  emitChange('query', 1, null, { query: value })
}

function setFilter(key: string, value: string) {
  const next = { ...snapshotFilters(), [key]: value }
  if (next[key] !== currentFilters.value[key]) clearSelection()
  if (controlledFilterValues.value === undefined) selectedFilters[key] = value
  resetCursorHistory()
  if (props.page === undefined) page.value = 1
  emit('update:filterValues', next)
  emit('update:page', 1)
  emitChange('filter', 1, null, { filters: next })
}

function clearSelection() {
  if (normalizedSelectedKeys.value.length > 0) emit('update:selectedKeys', [])
}

function checkboxIsVisibleInScrollRegion(checkbox: HTMLInputElement, region: HTMLElement): boolean {
  const checkboxRect = checkbox.getBoundingClientRect()
  const regionRect = region.getBoundingClientRect()
  const regionLeft = regionRect.left + region.clientLeft
  const regionTop = regionRect.top + region.clientTop
  const visibleLeft = Math.max(0, regionLeft)
  const visibleTop = Math.max(0, regionTop)
  const visibleRight = Math.min(window.innerWidth, regionLeft + region.clientWidth)
  const visibleBottom = Math.min(window.innerHeight, regionTop + region.clientHeight)

  return checkboxRect.width > 0
    && checkboxRect.height > 0
    && checkboxRect.left >= visibleLeft
    && checkboxRect.right <= visibleRight
    && checkboxRect.top >= visibleTop
    && checkboxRect.bottom <= visibleBottom
}

async function clearSelectionFromToolbar() {
  clearSelection()
  await nextTick()
  if (selectedCount.value === 0) {
    const checkbox = headerSelectionCheckbox.value
    const region = tableScrollRegion.value
    if (checkbox && !checkbox.disabled && region && checkboxIsVisibleInScrollRegion(checkbox, region)) {
      checkbox.focus({ preventScroll: true })
      return
    }
    const regionRect = region?.getBoundingClientRect()
    if (region && regionRect && regionRect.top < window.innerHeight && regionRect.bottom > 0
      && regionRect.left < window.innerWidth && regionRect.right > 0) {
      region.focus({ preventScroll: true })
      return
    }
    // If the whole table is outside the viewport, allow native focus scrolling
    // to reveal the header or the region instead of leaving focus offscreen.
    const target = checkbox && !checkbox.disabled ? checkbox : region
    target?.focus()
  }
}

function isSelectionKey(value: unknown): value is TableSelectionKey {
  return (typeof value === 'string' && value.trim().length > 0)
    || (typeof value === 'number' && Number.isFinite(value))
}

function stableSelectionKey(row: Record<string, unknown>): TableSelectionKey | null {
  if (typeof props.rowKey === 'function') {
    const value = props.rowKey(row, props.rows.indexOf(row))
    if (isSelectionKey(value)) return value
  } else if (typeof props.rowKey === 'string' && isSelectionKey(row[props.rowKey])) {
    return row[props.rowKey] as TableSelectionKey
  }

  for (const key of ['name', 'id', 'uid']) {
    if (isSelectionKey(row[key])) return row[key] as TableSelectionKey
  }
  return null
}

function rowSelectionState(row: Record<string, unknown>): {
  key: TableSelectionKey | null
  selectable: boolean
  reason: string
} {
  const key = stableSelectionKey(row)
  const requestedReason = props.rowSelectionDisabledReason?.(row)?.trim() ?? ''
  const rowAllowed = props.rowSelectable?.(row) !== false
  const duplicate = key !== null && duplicateRowKeys.value.has(key)
  const reason = key === null
    ? 'This resource has no stable key for selection.'
    : duplicate
      ? 'This resource has a duplicate stable key and cannot be selected.'
      : requestedReason || (rowAllowed ? '' : 'This resource is not available for selection.')
  return { key, selectable: key !== null && !duplicate && rowAllowed && !requestedReason, reason }
}

function selectionCheckboxLabel(row: Record<string, unknown>, index: number): string {
  const customLabel = props.selectionLabel?.(row)?.trim()
  if (customLabel) return customLabel
  const key = stableSelectionKey(row)
  const resource = key === null ? `resource on row ${index + 1}` : String(key)
  return `${selectedKeySet.value.has(key as TableSelectionKey) ? 'Deselect' : 'Select'} ${resource}`
}

function selectionReasonID(index: number): string {
  return `${componentID}-selection-reason-${index}`
}

function selectionDescription(row: Record<string, unknown>): string {
  const state = rowSelectionState(row)
  if (!state.selectable) return state.reason
  return props.selectionDisabled ? 'Selection is unavailable while resources are busy or unverified.' : ''
}

function selectionHelpLabel(row: Record<string, unknown>, index: number): string {
  const label = selectionCheckboxLabel(row, index)
  return `Why can't I select ${label.replace(/^(Select|Deselect)\s+/i, '')}?`
}

function toggleRowSelection(row: Record<string, unknown>, checked: boolean) {
  if (props.selectionDisabled) return
  const state = rowSelectionState(row)
  if (!state.selectable || state.key === null) return
  emit('update:selectedKeys', updatePageSelection(normalizedSelectedKeys.value, [state.key], checked))
}

function togglePageSelection(checked: boolean) {
  if (props.selectionDisabled) return
  emit('update:selectedKeys', updatePageSelection(normalizedSelectedKeys.value, eligiblePageKeys.value, checked))
}

function isRowSelected(row: Record<string, unknown>): boolean {
  const key = stableSelectionKey(row)
  return key !== null && selectedKeySet.value.has(key)
}

function pruneAuthoritativeClientSelection() {
  if (!props.selectable
    || props.paginationMode === 'server'
    || props.loaded !== true
    || props.loading
    || props.error
    || props.stale
    || props.selectionDisabled
    || normalizedSelectedKeys.value.length === 0) return

  const next = pruneClientSelectionKeys(normalizedSelectedKeys.value, authoritativeSelectionRows.value)
  if (next.length !== normalizedSelectedKeys.value.length) emit('update:selectedKeys', next)
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

function primaryValue(row: Record<string, unknown>): string {
  const key = primaryColumnKey.value
  if (!key) return ''
  const column = visibleColumns.value.find(candidate => candidate.key === key)
  const renderedValue = column?.fullValue?.(row)
  if (renderedValue !== null && renderedValue !== undefined) return String(renderedValue)
  const value = row[key]
  return value === null || value === undefined ? '' : String(value)
}

function updatePrimaryOverflow(container: HTMLElement | null) {
  if (!container) return false
  const value = container.querySelector<HTMLElement>('.k-table__primary-value')
  if (!value) return false
  const overflows = value.scrollWidth > value.clientWidth + 1
  container.dataset.overflow = String(overflows)
  return overflows
}

function hideTooltip() {
  primaryTooltipRequest += 1
  activeTooltipAnchor = null
  activeSelectionTooltip = null
  primaryTooltip.value = null
}

async function showTooltip(anchor: HTMLElement | null, value: string) {
  if (!anchor || !value.trim()) {
    hideTooltip()
    return
  }

  const request = ++primaryTooltipRequest
  activeTooltipAnchor = anchor
  primaryTooltip.value = {
    value,
    left: PRIMARY_TOOLTIP_VIEWPORT_MARGIN,
    top: PRIMARY_TOOLTIP_VIEWPORT_MARGIN,
    positioned: false,
  }

  await nextTick()
  if (request !== primaryTooltipRequest || activeTooltipAnchor !== anchor) return

  const tooltip = primaryTooltipElement.value
  if (!tooltip) return

  const anchorRect = anchor.getBoundingClientRect()
  const tooltipRect = tooltip.getBoundingClientRect()
  const maxLeft = Math.max(
    PRIMARY_TOOLTIP_VIEWPORT_MARGIN,
    window.innerWidth - tooltipRect.width - PRIMARY_TOOLTIP_VIEWPORT_MARGIN,
  )
  const left = Math.min(Math.max(anchorRect.left, PRIMARY_TOOLTIP_VIEWPORT_MARGIN), maxLeft)
  const below = anchorRect.bottom + PRIMARY_TOOLTIP_GAP
  const above = anchorRect.top - tooltipRect.height - PRIMARY_TOOLTIP_GAP
  const preferredTop = above >= PRIMARY_TOOLTIP_VIEWPORT_MARGIN ? above : below
  const maxTop = Math.max(
    PRIMARY_TOOLTIP_VIEWPORT_MARGIN,
    window.innerHeight - tooltipRect.height - PRIMARY_TOOLTIP_VIEWPORT_MARGIN,
  )

  primaryTooltip.value = {
    value,
    left,
    top: Math.min(Math.max(preferredTop, PRIMARY_TOOLTIP_VIEWPORT_MARGIN), maxTop),
    positioned: true,
  }
}

async function showPrimaryTooltip(container: HTMLElement | null) {
  activeSelectionTooltip = null
  if (!container || !updatePrimaryOverflow(container)) {
    hideTooltip()
    return
  }

  const value = container.dataset.fullValue?.trim()
  if (!value) {
    hideTooltip()
    return
  }

  await showTooltip(container, value)
}

function showSelectionReasonTooltip(event: Event, row: Record<string, unknown>, index: number) {
  const anchor = event.currentTarget as HTMLElement | null
  const reason = rowSelectionState(row).reason
  if (!anchor || !reason) return

  activeSelectionTooltip = {
    anchor,
    rowIdentity: rowIdentity(row, index),
    value: reason,
  }
  void showTooltip(anchor, reason)
}

watch(
  () => visibleRows.value.map((row, index) => ({
    rowIdentity: rowIdentity(row, index),
    reason: rowSelectionState(row).reason,
  })),
  rows => {
    const active = activeSelectionTooltip
    if (!active) return
    if (activeTooltipAnchor !== active.anchor) {
      activeSelectionTooltip = null
      return
    }

    const current = rows.find(row => row.rowIdentity === active.rowIdentity)
    if (!current?.reason || !active.anchor.isConnected) {
      hideTooltip()
      return
    }
    if (current.reason !== active.value) {
      active.value = current.reason
      void showTooltip(active.anchor, current.reason)
    }
  },
  { flush: 'post' },
)

function leaveSelectionReasonTooltip(event: MouseEvent) {
  if (document.activeElement !== event.currentTarget) hideTooltip()
}

function syncPrimaryOverflow(event: MouseEvent) {
  void showPrimaryTooltip(event.currentTarget as HTMLElement | null)
}

function syncRowPrimaryOverflow(event: FocusEvent) {
  const row = event.currentTarget as HTMLElement | null
  const container = row?.querySelector<HTMLElement>('.k-table__primary-content') ?? null
  const target = event.target as Node | null
  if (target instanceof Element && target.closest('.k-table__selection-help')) return
  if (!row || !container || (target !== row && !container.contains(target))) {
    hideTooltip()
    return
  }
  void showPrimaryTooltip(container)
}

onMounted(() => {
  window.addEventListener('resize', hideTooltip)
  window.addEventListener('scroll', hideTooltip, true)
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', hideTooltip)
  window.removeEventListener('scroll', hideTooltip, true)
})

function clearFilters() {
  const next = Object.fromEntries(props.filters.map(filter => [filter.key, '']))
  if (currentQuery.value || hasFacetFilters.value) clearSelection()
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
    'a, button, input, label, select, textarea, summary, [contenteditable="true"], [role="button"], [role="link"], [tabindex]:not([tabindex="-1"])',
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
    :class="[`k-table--${variant}`, { 'k-table--selecting': selectionSurfaceVisible && selectedCount > 0 }]"
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
      {{ filterPending ? 'Updating table results…' : explicitReadState && loading && loaded ? 'Updating…' : '' }}
    </span>
    <div v-if="showInitialError" class="k-table__error" role="alert" aria-live="assertive">
      <AlertCircle class="k-table__error-icon" :stroke-width="1.75" aria-hidden="true" />
      <span class="k-table__error-message">{{ error }}</span>
      <button v-if="retryable" class="k-table__retry" type="button" @click="emit('retry')">Retry</button>
    </div>

    <div
      v-else-if="initialReadPending"
      class="k-table__loading k-delayed-loading"
      :role="showInitialLoading ? 'status' : undefined"
      :aria-live="showInitialLoading ? 'polite' : undefined"
      :aria-label="showInitialLoading ? `Loading ${tableAriaLabel.toLocaleLowerCase()}` : undefined"
      :aria-hidden="showInitialLoading ? undefined : 'true'"
    >
      <div v-if="hasConfiguredControls" class="k-table__loading-controls" aria-hidden="true">
        <div v-if="searchable" class="shimmer k-table__loading-control k-table__loading-control--search" />
        <div v-for="filter in filters" :key="filter.key" class="shimmer k-table__loading-control k-table__loading-control--filter" />
        <div v-if="hasFacetFilters" class="shimmer k-table__loading-control k-table__loading-control--clear" />
      </div>
      <div class="k-table__loading-head" :style="{ '--k-table-loading-columns': skeletonColumns.length }">
        <div
          v-for="(column, index) in skeletonColumns"
          :key="column.key"
          class="shimmer k-table__skeleton"
          :class="index === 0 ? 'k-table__skeleton--short' : ''"
        />
      </div>
      <div
        v-for="i in 5"
        :key="i"
        class="k-table__loading-row"
        :style="{ '--k-table-loading-columns': skeletonColumns.length }"
      >
        <div
          v-for="(column, index) in skeletonColumns"
          :key="column.key"
          class="shimmer k-table__skeleton"
          :class="[
            index === 0 ? 'k-table__skeleton--wide' : '',
            index === 1 ? 'k-table__skeleton--mid' : '',
            index === 2 ? 'k-table__skeleton--small' : '',
          ]"
        />
      </div>
    </div>

    <template v-else>
      <div v-if="explicitReadState && error" class="k-table__stale" :role="staleMessageRole" :aria-live="staleMessageLive">
        <AlertCircle class="k-table__error-icon" :stroke-width="1.75" aria-hidden="true" />
        <span class="k-table__error-message">
          {{ stale ? 'Showing the last successful result. ' : '' }}{{ error }}
        </span>
        <button v-if="retryable" class="k-table__retry" type="button" @click="emit('retry')">Retry</button>
      </div>

      <div v-if="showControls || selectionSurfaceVisible" class="k-table__toolbar-stack">
        <div
          v-if="showControls"
          class="k-table__controls"
          role="search"
          :aria-label="`Filter ${tableAriaLabel.toLocaleLowerCase()}`"
          :class="['k-table__toolbar-panel', { 'k-table__toolbar-panel--inactive': selectionToolbarActive }]"
          :aria-hidden="selectionToolbarActive ? 'true' : undefined"
          :inert="selectionToolbarActive"
        >
          <label v-if="searchable" class="k-table__search">
            <span class="sr-only" style="position:absolute;block-size:1px;inline-size:1px;overflow:hidden;clip:rect(0 0 0 0)">Search {{ tableAriaLabel }}</span>
            <Search class="k-table__search-icon" :stroke-width="1.75" aria-hidden="true" />
            <input :value="currentQuery" class="k-table__search-input" type="search" :aria-label="`Search ${tableAriaLabel}`" :placeholder="searchPlaceholder" autocomplete="off" @input="setQuery(($event.target as HTMLInputElement).value)">
            <button v-if="currentQuery" class="k-table__search-clear" type="button" aria-label="Clear search" @click="setQuery('')"><X :stroke-width="1.75" aria-hidden="true" /></button>
          </label>
          <ResourceTableFilter
            v-for="filter in filters"
            :key="filter.key"
            :definition="filter"
            :options="filterOptions[filter.key]"
            :model-value="currentFilters[filter.key] || ''"
            @update:model-value="setFilter(filter.key, $event)"
          />
          <button v-if="hasFacetFilters" class="k-table__clear-filters" type="button" @click="clearFilters">{{ clearActionLabel }}</button>
        </div>

        <div
          v-if="selectionSurfaceVisible"
          class="k-table__selection-bar"
          :class="['k-table__toolbar-panel', { 'k-table__toolbar-panel--inactive': !selectionToolbarActive }]"
          :aria-hidden="!selectionToolbarActive ? 'true' : undefined"
          :inert="!selectionToolbarActive"
        >
          <div class="k-table__selection-summary">
            <p class="k-table__selection-count">{{ selectedCount }} selected</p>
            <button class="k-table__clear-selection" type="button" :disabled="clearSelectionDisabled" @click="clearSelectionFromToolbar">
              Clear selection
            </button>
          </div>
          <div class="k-table__selection-actions">
            <slot name="selection-actions" :selectedKeys="normalizedSelectedKeys" :keys="normalizedSelectedKeys" :count="selectedCount" />
          </div>
        </div>
      </div>

      <span v-if="selectable" class="k-table__selection-live" role="status" aria-live="polite" aria-atomic="true">
        {{ selectionAnnouncement }}
      </span>

      <div ref="tableScrollRegion" class="k-table__scroll" role="region" :aria-label="`${tableAriaLabel} scroll area`" tabindex="0">
        <table class="k-table__table" :aria-label="tableAriaLabel">
          <thead v-if="!confirmedEmptyInventory || selectedCount > 0"><tr class="k-table__head-row">
            <th v-if="selectionSurfaceVisible" class="k-table__heading k-table__selection-heading" scope="col">
              <label class="k-table__checkbox-target">
                <input
                  ref="headerSelectionCheckbox"
                  class="k-table__checkbox"
                  type="checkbox"
                  :checked="selectionOnPage.allSelected"
                  :indeterminate="selectionOnPage.partiallySelected"
                  :aria-checked="selectionOnPage.partiallySelected ? 'mixed' : undefined"
                  :aria-label="`Select all eligible ${tableAriaLabel.toLocaleLowerCase()} on this page`"
                  :disabled="selectionDisabled || eligiblePageKeys.length === 0"
                  @change="togglePageSelection(($event.target as HTMLInputElement).checked)"
                >
              </label>
            </th>
            <th v-for="col in visibleColumns" :key="col.key" class="k-table__heading" :class="[`k-table__heading--${col.align ?? 'start'}`, { 'k-table__heading--primary': col.key === primaryColumnKey }]" :aria-label="col.ariaLabel || col.label || col.key">{{ col.label }}</th>
          </tr></thead>
          <tbody>
            <template v-for="(row, i) in visibleRows" :key="rowIdentity(row, i)">
              <tr
                class="stagger-item k-table__row"
                :class="{
                  'k-table__row--interactive': interactive,
                  'k-table__row--selected': selectionSurfaceVisible && isRowSelected(row) && rowSelectionState(row).selectable,
                }"
                :tabindex="interactive ? 0 : undefined"
                :aria-label="interactive ? rowAriaLabel(row, i) : undefined"
                :style="{ animationDelay: `${i * 35}ms` }"
                @click="onRowClick(row, $event)"
                @focusin="syncRowPrimaryOverflow"
                @focusout="hideTooltip"
                @keydown="onRowKeydown(row, $event)"
              >
                <td v-if="selectionSurfaceVisible" class="k-table__cell k-table__selection-cell">
                  <label
                    class="k-table__checkbox-target"
                    :class="{ 'k-table__checkbox-target--explained': !!rowSelectionState(row).reason }"
                  >
                    <input
                      class="k-table__checkbox"
                      type="checkbox"
                      :checked="isRowSelected(row)"
                      :aria-label="selectionCheckboxLabel(row, i)"
                      :aria-describedby="selectionDescription(row) ? selectionReasonID(i) : undefined"
                      :disabled="selectionDisabled || !rowSelectionState(row).selectable"
                      @change="toggleRowSelection(row, ($event.target as HTMLInputElement).checked)"
                    >
                  </label>
                  <span v-if="selectionDescription(row)" :id="selectionReasonID(i)" class="k-table__selection-reason">
                    {{ selectionDescription(row) }}
                  </span>
                  <button
                    v-if="rowSelectionState(row).reason"
                    class="k-table__selection-help"
                    type="button"
                    :aria-label="selectionHelpLabel(row, i)"
                    :aria-describedby="selectionReasonID(i)"
                    @mouseenter="showSelectionReasonTooltip($event, row, i)"
                    @mouseleave="leaveSelectionReasonTooltip"
                    @focusin.stop="showSelectionReasonTooltip($event, row, i)"
                    @focusout.stop="hideTooltip"
                    @click.stop="showSelectionReasonTooltip($event, row, i)"
                    @keydown.esc.stop="hideTooltip"
                  >
                    <Info :stroke-width="1.75" aria-hidden="true" />
                  </button>
                </td>
                <td v-for="col in visibleColumns" :key="col.key" class="k-table__cell" :class="[`k-table__cell--${col.align ?? 'start'}`, { 'k-table__cell--primary': col.key === primaryColumnKey }]">
                  <div v-if="col.key === primaryColumnKey && actionsColumn" class="k-table__primary">
                    <div class="k-table__primary-content" :data-full-value="primaryValue(row)" @mouseenter="syncPrimaryOverflow" @mouseleave="hideTooltip">
                      <span class="k-table__primary-value">
                        <slot :name="col.key" :value="row[col.key]" :row="row">{{ row[col.key] }}</slot>
                      </span>
                    </div>
                    <div class="k-table__primary-actions">
                      <slot :name="actionsColumn.key" :value="row[actionsColumn.key]" :row="row">{{ row[actionsColumn.key] }}</slot>
                    </div>
                  </div>
                  <div v-else-if="col.key === primaryColumnKey" class="k-table__primary-content" :data-full-value="primaryValue(row)" @mouseenter="syncPrimaryOverflow" @mouseleave="hideTooltip">
                    <span class="k-table__primary-value">
                      <slot :name="col.key" :value="row[col.key]" :row="row">{{ row[col.key] }}</slot>
                    </span>
                  </div>
                  <slot v-else :name="col.key" :value="row[col.key]" :row="row">{{ row[col.key] }}</slot>
                </td>
              </tr>
              <slot name="after-row" :row="row" :column-count="renderedColumnCount" />
            </template>
            <tr v-if="showPendingBody"><td :colspan="renderedColumnCount" class="k-table__pending-cell" role="status" aria-live="polite">
              <p class="k-table__pending-label">{{ pendingBodyText }}</p>
            </td></tr>
            <tr v-else-if="visibleRows.length === 0"><td :colspan="renderedColumnCount" class="k-table__empty-cell">
              <Inbox class="k-table__empty-icon" :stroke-width="1.25" aria-hidden="true" />
              <p class="k-table__empty-label">{{ activeFilters ? noMatchText : emptyText }}</p>
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
          <button class="k-table__page-button" type="button" aria-label="Previous page" :disabled="!canPrevious" @click="previousPage"><ChevronLeft :stroke-width="1.75" aria-hidden="true" /></button>
          <span class="k-table__page-indicator" aria-live="polite">
            <template v-if="isServerPagination && serverTotal === null">Page {{ currentPage }}</template>
            <template v-else>{{ currentPage }} / {{ totalPages }}</template>
          </span>
          <button class="k-table__page-button" type="button" aria-label="Next page" :disabled="!canNext" @click="nextPage"><ChevronRight :stroke-width="1.75" aria-hidden="true" /></button>
        </div>
      </footer>
    </template>
  </div>

  <Teleport to="body">
    <div
      v-if="primaryTooltip"
      ref="primaryTooltipElement"
      class="k-table__primary-tooltip"
      :class="{ 'k-table__primary-tooltip--positioned': primaryTooltip.positioned }"
      :style="{ left: `${primaryTooltip.left}px`, top: `${primaryTooltip.top}px` }"
      aria-hidden="true"
    >
      {{ primaryTooltip.value }}
    </div>
  </Teleport>
</template>
