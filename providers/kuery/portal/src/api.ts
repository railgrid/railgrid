// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

/**
 * Framework-neutral client for the Kuery query verb.
 *
 * There is one route: POST
 * {base}/dataplane/clusters/{clusterID}/savedviews/{name}/run. A query is a
 * verb on a SavedView the caller must be able to see and must be granted the
 * verb on, which is why every call needs a cluster and a view name. The
 * playground's ad-hoc queries are not an exception: they run a per-user
 * scratch SavedView, created with the kube client, with the query supplied as
 * the request's input override.
 *
 * The response is an actionwire envelope; this module unwraps it so a Vue view
 * still sees a QueryStatus and does not need to know the wire format (or make
 * assumptions about an opaque cursor).
 */

export type RootKind = 'objects' | 'clusters'
export type SortDirection = 'Asc' | 'Desc'
export type OrderField = 'name' | 'namespace' | 'kind' | 'apiGroup' | 'cluster' | 'creationTimestamp'

export interface ClusterFilter {
  name?: string
  labels?: Record<string, string>
}

export interface GroupKindFilter {
  apiGroup?: string
  kind: string
}

export interface ObjectFilter {
  groupKind?: GroupKindFilter
  name?: string
  namespace?: string
  labels?: Record<string, string>
  labelExpressions?: Array<Record<string, unknown>>
  conditions?: Array<Record<string, unknown>>
  creationTimestamp?: Record<string, unknown>
  id?: string
  jsonpath?: string
  categories?: string[]
}

export interface QueryFilter {
  objects?: ObjectFilter[]
}

/** A sparse object projection accepted by Kuery's ObjectsSpec.object field. */
export interface QueryProjection {
  [key: string]: boolean | QueryProjection
}

export interface RelationSpec {
  limit?: number
  filters?: ObjectFilter[]
  objects?: ObjectsSpec
}

export interface ObjectsSpec {
  id?: boolean
  cluster?: boolean
  mutablePath?: boolean
  object?: QueryProjection
  relations?: Record<string, RelationSpec>
}

export interface PageSpec {
  /** Offset used by Kuery's offset pagination mode. */
  first?: number
  /** An opaque cursor returned by a previous response. */
  cursor?: string
}

export interface OrderSpec {
  field: OrderField
  direction?: SortDirection
}

/** The JSON shape accepted by the pinned kuery QuerySpec API. */
export interface QuerySpec {
  root?: RootKind
  cluster?: ClusterFilter
  filter?: QueryFilter
  limit?: number
  page?: PageSpec
  order?: OrderSpec[]
  count?: boolean
  cursor?: boolean
  maxDepth?: number
  objects?: ObjectsSpec
}

export interface ObjectResult {
  id?: string
  cluster?: string
  mutablePath?: string
  object?: {
    kind?: string
    apiVersion?: string
    metadata?: { name?: string; namespace?: string; creationTimestamp?: string }
  }
  relations?: Record<string, ObjectResult[]>
}

export interface CursorResult {
  /** The next cursor is opaque; callers must pass it back unchanged. */
  next?: string
  page?: number
  pageSize?: number
}

/** Raw QueryStatus JSON, unwrapped from the run verb's envelope. */
export interface QueryStatus<T extends ObjectResult = ObjectResult> {
  objects?: T[]
  cursor?: CursorResult
  count?: number
  incomplete?: boolean
  warnings?: string[]
}

/**
 * Normalized metadata consumed by a paginated resource table.
 *
 * `hasNext` is derived only from the presence of a non-empty response cursor.
 * `incomplete` remains separate: Kuery uses it to report truncation/limits,
 * and it is not evidence that another page exists.
 */
export interface QueryPage<T extends ObjectResult = ObjectResult> {
  objects: T[]
  cursor: CursorResult | null
  nextCursor: string | null
  total: number | null
  hasNext: boolean
  incomplete: boolean
  warnings: string[]
}

export type InventoryPage = QueryPage<ObjectResult>

export interface QueryRequestOptions {
  signal?: AbortSignal
  /** Run a different SavedView than the client's default for this call. */
  savedView?: string
}

export type HeaderSource = HeadersInit | (() => HeadersInit)

export interface KueryApiOptions {
  /** The provider service base, normally /services/providers/kuery. */
  basePath: string
  /**
   * The tenant workspace's kcp logical-cluster ID (railgridContext.tenant).
   * It addresses the request; the caller's bearer is what authorizes it.
   */
  cluster: string
  /**
   * The SavedView every query runs as. Views own their own queries; a query
   * passed to query() overrides the saved one for that call.
   */
  savedView: string
  /** Caller-supplied tenant headers from the host context. */
  headers?: HeaderSource
  /** Injectable fetch makes the adapter usable in tests and non-browser hosts. */
  fetch?: typeof globalThis.fetch
}

/** The actionwire envelope the run verb answers with. */
interface RunEnvelope {
  result?: unknown
  error?: { code?: string; message?: string; retryable?: boolean }
}

/**
 * runPath is the one tenant route. Every segment is encoded: a view name is
 * caller-authored and must not be able to escape its slot.
 */
export function runPath(basePath: string, cluster: string, savedView: string): string {
  const base = basePath.replace(/\/+$/, '')
  return `${base}/dataplane/clusters/${encodeURIComponent(cluster)}/savedviews/${encodeURIComponent(savedView)}/run`
}

export interface InventoryFilters {
  /** Kuery calls this field cluster; the portal commonly labels it Edge. */
  cluster?: string
  /** Alias accepted for callers that use the portal's visible Edge label. */
  edge?: string
  kind?: string
  namespace?: string
  name?: string
}

export interface InventoryPageRequest {
  pageSize: number
  cursor?: string | null
  count?: boolean
  filters?: InventoryFilters
}

export const DEFAULT_INVENTORY_PAGE_SIZE = 50
export const MAX_INVENTORY_PAGE_SIZE = 10_000

/**
 * Explicit, deterministic fleet ordering. The cluster key is first because
 * the inventory spans multiple edges. The remaining identity dimensions make
 * each page stable even when different API kinds share one namespace/name.
 */
export const INVENTORY_ORDER = [
  { field: 'cluster', direction: 'Asc' },
  { field: 'namespace', direction: 'Asc' },
  { field: 'apiGroup', direction: 'Asc' },
  { field: 'kind', direction: 'Asc' },
  { field: 'name', direction: 'Asc' },
] as const satisfies readonly OrderSpec[]

function inventoryProjection(): ObjectsSpec {
  return {
    id: true,
    cluster: true,
    mutablePath: true,
    object: {
      kind: true,
      apiVersion: true,
      metadata: {
        name: true,
        namespace: true,
        creationTimestamp: true,
      },
    },
  }
}

function assertPageSize(pageSize: number): void {
  if (!Number.isInteger(pageSize) || pageSize < 1 || pageSize > MAX_INVENTORY_PAGE_SIZE) {
    throw new RangeError(`inventory page size must be an integer from 1 to ${MAX_INVENTORY_PAGE_SIZE}`)
  }
}

/**
 * Build one inventory query without interpreting or manufacturing cursors.
 * A cursor is emitted only when the caller supplies a non-empty token; the
 * token itself is copied byte-for-byte into PageSpec.cursor.
 */
export function buildInventoryQuery(request: InventoryPageRequest): QuerySpec {
  const pageSize = request.pageSize ?? DEFAULT_INVENTORY_PAGE_SIZE
  assertPageSize(pageSize)

  const filters = request.filters ?? {}
  const objectFilter: ObjectFilter = {}
  if (filters.kind) objectFilter.groupKind = { kind: filters.kind }
  if (filters.namespace) objectFilter.namespace = filters.namespace
  if (filters.name) objectFilter.name = filters.name

  const query: QuerySpec = {
    limit: pageSize,
    cursor: true,
    order: INVENTORY_ORDER.map(order => ({ ...order })),
    objects: inventoryProjection(),
  }

  if (request.count === true) query.count = true

  const cluster = filters.cluster || filters.edge
  if (cluster) query.cluster = { name: cluster }
  if (Object.keys(objectFilter).length > 0) query.filter = { objects: [objectFilter] }

  // An empty cursor is the first page. Any non-empty value is opaque and is
  // intentionally not decoded, trimmed, validated, or otherwise transformed.
  if (request.cursor !== undefined && request.cursor !== null && request.cursor !== '') {
    query.page = { cursor: request.cursor }
  }

  return query
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function decodeQueryStatus(value: unknown): QueryStatus {
  if (!isRecord(value)) throw new Error('kuery returned an invalid QueryStatus body')

  if (value.objects !== undefined && !Array.isArray(value.objects)) {
    throw new Error('kuery returned an invalid QueryStatus.objects value')
  }
  if (value.cursor !== undefined && !isRecord(value.cursor)) {
    throw new Error('kuery returned an invalid QueryStatus.cursor value')
  }
  if (value.count !== undefined && (typeof value.count !== 'number' || !Number.isSafeInteger(value.count) || value.count < 0)) {
    throw new Error('kuery returned an invalid QueryStatus.count value')
  }
  if (value.incomplete !== undefined && typeof value.incomplete !== 'boolean') {
    throw new Error('kuery returned an invalid QueryStatus.incomplete value')
  }
  if (value.warnings !== undefined && (!Array.isArray(value.warnings) || value.warnings.some(warning => typeof warning !== 'string'))) {
    throw new Error('kuery returned an invalid QueryStatus.warnings value')
  }

  const cursor = value.cursor as Record<string, unknown> | undefined
  if (cursor?.next !== undefined && typeof cursor.next !== 'string') {
    throw new Error('kuery returned an invalid QueryStatus.cursor.next value')
  }

  return value as QueryStatus
}

/** Map QueryStatus metadata without inferring pagination from truncation. */
export function mapQueryStatus<T extends ObjectResult>(status: QueryStatus<T>): QueryPage<T> {
  const cursor = status.cursor ?? null
  // Empty next is the API's absent cursor representation. Preserve every
  // non-empty token exactly; it is opaque to this client.
  const nextCursor = cursor?.next ? cursor.next : null

  return {
    objects: status.objects ?? [],
    cursor,
    nextCursor,
    total: status.count ?? null,
    hasNext: nextCursor !== null,
    incomplete: status.incomplete ?? false,
    warnings: [...(status.warnings ?? [])],
  }
}

export class KueryApiError extends Error {
  readonly status: number
  readonly body: string

  constructor(status: number, body: string, statusText = '') {
    const detail = body.trim()
    super(`kuery request failed (${status}): ${detail || statusText || 'unknown error'}`)
    this.name = 'KueryApiError'
    this.status = status
    this.body = body
  }
}

export class KueryApi {
  private readonly basePath: string
  private readonly cluster: string
  private readonly savedView: string
  private readonly headerSource?: HeaderSource
  private readonly fetchImpl: typeof globalThis.fetch

  constructor(options: KueryApiOptions) {
    this.basePath = options.basePath.replace(/\/+$/, '')
    this.cluster = options.cluster
    this.savedView = options.savedView
    this.headerSource = options.headers
    this.fetchImpl = options.fetch ?? globalThis.fetch.bind(globalThis)
  }

  async query(spec: QuerySpec, options: QueryRequestOptions = {}): Promise<QueryStatus> {
    const headers = new Headers(typeof this.headerSource === 'function' ? this.headerSource() : this.headerSource)
    headers.set('Accept', 'application/json')
    headers.set('Content-Type', 'application/json')

    const response = await this.fetchImpl(runPath(this.basePath, this.cluster, options.savedView || this.savedView), {
      method: 'POST',
      headers,
      // The verb takes {"input": …} and nothing else; an omitted query runs
      // the view as saved, which is not what an explicit call wants.
      body: JSON.stringify({ input: { query: spec } }),
      signal: options.signal,
    })
    const body = await response.text()

    let parsed: unknown
    try {
      parsed = body ? JSON.parse(body) : {}
    } catch {
      if (!response.ok) throw new KueryApiError(response.status, body, response.statusText)
      throw new Error('kuery returned an invalid JSON response')
    }

    // A refused request may be answered by the gates (a plain status body) or
    // by the executor (an envelope carrying the reason). Prefer the envelope's
    // message: it is the one that says what to change.
    const envelope = isRecord(parsed) ? (parsed as RunEnvelope) : null
    if (envelope?.error) {
      throw new KueryApiError(response.ok ? 422 : response.status, envelope.error.message || '', envelope.error.code || response.statusText)
    }
    if (!response.ok) throw new KueryApiError(response.status, body, response.statusText)
    return decodeQueryStatus(envelope?.result ?? {})
  }

  async inventoryPage(request: InventoryPageRequest, options: QueryRequestOptions = {}): Promise<InventoryPage> {
    const status = await this.query(buildInventoryQuery(request), options)
    return mapQueryStatus(status)
  }
}

export function createKueryApi(options: KueryApiOptions): KueryApi {
  return new KueryApi(options)
}
