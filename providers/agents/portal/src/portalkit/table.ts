// CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
// under providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`.

export interface TableFilterOption {
  value: string
  label: string
}

export interface TableFilterDefinition {
  key: string
  label: string
  allLabel?: string
  options?: TableFilterOption[]
  /** Bespoke select-only listbox by default; resource inventories opt into search. */
  control?: 'select' | 'combobox'
  searchPlaceholder?: string
}

export type TableFilterState = Record<string, string>

/** Client pagination is the default; server mode renders one supplied page. */
export type TablePaginationMode = 'client' | 'server'

/** Metadata returned with one server-fetched page. Cursor values are opaque. */
export interface TablePageInfo {
  hasNext?: boolean
  nextCursor?: string | null
  total?: number | null
}

export interface FirstCursorPageInput {
  page: number
  cursor?: string | null
  pageInfo?: TablePageInfo | null
}

/**
 * Return true only for an authoritative, complete first cursor page.
 * Missing cursor/next-cursor values are accepted only in their first-page
 * positions; a missing `hasNext` is deliberately not treated as complete.
 */
export function isCompleteFirstCursorPage(input: FirstCursorPageInput): boolean {
  return input.page === 1
    && (input.cursor === null || input.cursor === undefined)
    && input.pageInfo?.hasNext === false
    && (input.pageInfo.nextCursor === null || input.pageInfo.nextCursor === undefined)
}

/** A typed page envelope for adapters that keep rows and metadata together. */
export interface CursorPage<T> extends TablePageInfo {
  items: T[]
}

/** Pure state for a cursor-backed table. `pageCursors[n]` fetches page n + 1. */
export interface CursorPagerState<Filters extends TableFilterState = TableFilterState> {
  page: number
  pageSize: number
  query: string
  filters: Filters
  cursor: string | null
  nextCursor: string | null
  /** Undefined entries mean that page has not been reached and saved yet. */
  pageCursors: Array<string | null | undefined>
  hasNext: boolean
  total: number | null
}

export interface CursorPagerRequest<Filters extends TableFilterState = TableFilterState> {
  page: number
  pageSize: number
  query: string
  filters: Filters
  cursor: string | null
}

export type TableChangeReason = 'page' | 'page-size' | 'query' | 'filter'

export interface ResourceTableChange<Filters extends TableFilterState = TableFilterState> {
  reason: TableChangeReason
  page: number
  pageSize: number
  query: string
  filters: Filters
  cursor: string | null
}

function safePageSize(pageSize: number): number {
  return Number.isFinite(pageSize) && pageSize > 0 ? Math.floor(pageSize) : 1
}

function safePage(page: number): number {
  return Number.isFinite(page) && page > 0 ? Math.floor(page) : 1
}

function safeTotal(total: number | null | undefined): number | null {
  if (total === null || total === undefined) return null
  return Number.isFinite(total) && total >= 0 ? Math.floor(total) : null
}

function copyFilters<Filters extends TableFilterState>(filters: Filters): Filters {
  return { ...filters } as Filters
}

function sameFilterState(left: TableFilterState, right: TableFilterState): boolean {
  const leftKeys = Object.keys(left)
  const rightKeys = Object.keys(right)
  if (leftKeys.length !== rightKeys.length) return false
  return leftKeys.every(key => left[key] === right[key])
}

/** Create deterministic cursor state without inspecting or interpreting tokens. */
export function createCursorPager<Filters extends TableFilterState = TableFilterState>(
  initial: Partial<CursorPagerState<Filters>> = {},
): CursorPagerState<Filters> {
  const page = safePage(initial.page ?? 1)
  const pageCursors = initial.pageCursors?.map(cursor => cursor) ?? []
  while (pageCursors.length < page) pageCursors.push(undefined)
  if (pageCursors.length === 0) pageCursors.push(null)
  if (initial.cursor !== undefined) pageCursors[page - 1] = initial.cursor ?? null
  if (page === 1 && pageCursors[0] === undefined) pageCursors[0] = null
  const cursor = initial.cursor ?? pageCursors[page - 1] ?? null

  return {
    page,
    pageSize: safePageSize(initial.pageSize ?? 10),
    query: initial.query ?? '',
    filters: copyFilters((initial.filters ?? {}) as Filters),
    cursor,
    nextCursor: initial.nextCursor ?? null,
    pageCursors,
    hasNext: initial.hasNext ?? false,
    total: safeTotal(initial.total),
  }
}

/** Convert state to the exact request inputs consumed by a list adapter. */
export function cursorPagerRequest<Filters extends TableFilterState = TableFilterState>(
  state: CursorPagerState<Filters>,
): CursorPagerRequest<Filters> {
  return {
    page: safePage(state.page),
    pageSize: safePageSize(state.pageSize),
    query: state.query,
    filters: copyFilters(state.filters),
    cursor: state.cursor ?? null,
  }
}

/** Reset page and all saved cursors after a query-shape change. */
export function resetCursorPager<Filters extends TableFilterState = TableFilterState>(
  state: CursorPagerState<Filters>,
  changes: Partial<Pick<CursorPagerState<Filters>, 'pageSize' | 'query' | 'filters'>> = {},
): CursorPagerState<Filters> {
  return {
    ...state,
    page: 1,
    pageSize: safePageSize(changes.pageSize ?? state.pageSize),
    query: changes.query ?? state.query,
    filters: copyFilters((changes.filters ?? state.filters) as Filters),
    cursor: null,
    nextCursor: null,
    pageCursors: [null],
    hasNext: false,
    total: null,
  }
}

/** Apply controlled values; page-size, query, and filters always reset cursors. */
export function updateCursorPager<Filters extends TableFilterState = TableFilterState>(
  state: CursorPagerState<Filters>,
  changes: Partial<Pick<CursorPagerState<Filters>, 'page' | 'pageSize' | 'query' | 'filters' | 'cursor'>>,
): CursorPagerState<Filters> {
  const nextPageSize = safePageSize(changes.pageSize ?? state.pageSize)
  const nextQuery = changes.query ?? state.query
  const nextFilters = (changes.filters ?? state.filters) as Filters
  const reset = nextPageSize !== state.pageSize
    || nextQuery !== state.query
    || !sameFilterState(state.filters, nextFilters)
  if (reset) return resetCursorPager(state, { pageSize: nextPageSize, query: nextQuery, filters: nextFilters })

  if (changes.page === undefined && changes.cursor === undefined) {
    return { ...state, pageSize: nextPageSize, query: nextQuery, filters: copyFilters(nextFilters) }
  }

  const page = safePage(changes.page ?? state.page)
  const pageCursors = [...state.pageCursors]
  while (pageCursors.length < page) pageCursors.push(undefined)
  const cursor = changes.cursor === undefined ? pageCursors[page - 1] ?? null : changes.cursor ?? null
  pageCursors[page - 1] = cursor
  return {
    ...state,
    page,
    pageSize: nextPageSize,
    query: nextQuery,
    filters: copyFilters(nextFilters),
    cursor,
    nextCursor: null,
    hasNext: false,
    pageCursors,
  }
}

/** Apply server metadata without inventing an exact total or cursor. */
export function applyCursorPage<T, Filters extends TableFilterState = TableFilterState>(
  state: CursorPagerState<Filters>,
  page: CursorPage<T>,
): CursorPagerState<Filters> {
  const nextCursor = page.nextCursor ?? null
  return {
    ...state,
    nextCursor,
    hasNext: page.hasNext ?? nextCursor !== null,
    total: safeTotal(page.total),
  }
}

/** Advance only when the response supplies both permission and an opaque token. */
export function nextCursorPager<Filters extends TableFilterState = TableFilterState>(
  state: CursorPagerState<Filters>,
): CursorPagerState<Filters> {
  if (!state.hasNext || state.nextCursor === null) return state
  const page = state.page + 1
  const pageCursors = state.pageCursors.slice(0, page)
  pageCursors[page - 1] = state.nextCursor
  return {
    ...state,
    page,
    cursor: state.nextCursor,
    nextCursor: null,
    pageCursors,
    hasNext: false,
  }
}

/** Move back using the saved request cursor for the target page. */
export function previousCursorPager<Filters extends TableFilterState = TableFilterState>(
  state: CursorPagerState<Filters>,
): CursorPagerState<Filters> {
  if (state.page <= 1) return state
  const page = state.page - 1
  const pageCursors = state.pageCursors.slice(0, page)
  const cursor = pageCursors[page - 1]
  if (cursor === undefined) return state
  return {
    ...state,
    page,
    cursor,
    nextCursor: null,
    pageCursors,
    hasNext: false,
  }
}

/** Compute a range for already-paged rows without pretending they are all rows. */
export function cursorPageRange(
  page: number,
  pageSize: number,
  itemCount: number,
  total?: number | null,
): { start: number; end: number } {
  if (itemCount <= 0) return { start: 0, end: 0 }
  const start = (safePage(page) - 1) * safePageSize(pageSize) + 1
  const end = start + Math.max(0, itemCount) - 1
  return { start, end: total === null || total === undefined ? end : Math.min(Math.max(0, total), end) }
}

function scalarText(value: unknown): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') return String(value)
  return ''
}

function collectText(value: unknown, seen: Set<unknown>): string[] {
  const scalar = scalarText(value)
  if (scalar) return [scalar]
  if (!value || typeof value !== 'object' || seen.has(value)) return []
  seen.add(value)
  if (Array.isArray(value)) return value.flatMap(item => collectText(item, seen))
  return Object.values(value as Record<string, unknown>).flatMap(item => collectText(item, seen))
}

export function tableSearchText(row: Record<string, unknown>, keys?: string[]): string {
  const values = keys?.length ? keys.map(key => row[key]) : Object.values(row)
  return values.flatMap(value => collectText(value, new Set())).join(' ').toLocaleLowerCase()
}

export function tableFilterValues(value: unknown): string[] {
  if (Array.isArray(value)) return value.flatMap(tableFilterValues)
  const text = scalarText(value).trim()
  return text ? [text] : []
}

export function deriveTableFilterOptions(
  rows: Array<Record<string, unknown>>,
  definition: TableFilterDefinition,
): TableFilterOption[] {
  if (definition.options) return definition.options
  const values = new Set<string>()
  rows.forEach(row => tableFilterValues(row[definition.key]).forEach(value => values.add(value)))
  return [...values]
    .sort((left, right) => left.localeCompare(right, undefined, { numeric: true, sensitivity: 'base' }))
    .map(value => ({ value, label: value }))
}

export function filterTableRows(
  rows: Array<Record<string, unknown>>,
  query: string,
  searchKeys: string[] | undefined,
  selectedFilters: Record<string, string>,
): Array<Record<string, unknown>> {
  const normalizedQuery = query.trim().toLocaleLowerCase()
  return rows.filter(row => {
    if (normalizedQuery && !tableSearchText(row, searchKeys).includes(normalizedQuery)) return false
    return Object.entries(selectedFilters).every(([key, selected]) =>
      !selected || tableFilterValues(row[key]).some(value => value === selected),
    )
  })
}

export function tablePageCount(total: number, pageSize: number): number {
  return Math.max(1, Math.ceil(Math.max(0, total) / Math.max(1, pageSize)))
}

export function paginateTableRows(
  rows: Array<Record<string, unknown>>,
  page: number,
  pageSize: number,
): Array<Record<string, unknown>> {
  const safeSize = Math.max(1, pageSize)
  const safePage = Math.max(1, Math.min(page, tablePageCount(rows.length, safeSize)))
  const start = (safePage - 1) * safeSize
  return rows.slice(start, start + safeSize)
}

export function tableRange(total: number, page: number, pageSize: number): { start: number; end: number } {
  if (total <= 0) return { start: 0, end: 0 }
  const safeSize = Math.max(1, pageSize)
  const safePage = Math.max(1, Math.min(page, tablePageCount(total, safeSize)))
  const start = (safePage - 1) * safeSize + 1
  return { start, end: Math.min(total, start + safeSize - 1) }
}
