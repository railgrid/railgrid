import type { TablePageInfo } from './portalkit/table'

export type PaginationMode = 'server' | 'client'

export interface TableRequestState {
  mode: PaginationMode
  active: boolean
  page: number
  pageSize: number
  query: string
  filters: Record<string, string>
  cursor: string | null
}

/**
 * Coordinates the query-independent complete read behind a hybrid cursor
 * table. All active query changes share one in-flight walk, while a successful
 * walk remains available for later local query/filter changes. `force` is
 * used by polling and CRUD refreshes; if a walk is already in flight, one
 * forced refresh is queued after it settles instead of starting a duplicate.
 */
export interface FullListReadCoordinator<T> {
  read(force?: boolean): Promise<T[]>
  seed(items: readonly T[]): T[]
  peek(): T[] | null
  pending(): boolean
  generation(): number
  clear(): void
}

/** Share a query-independent supporting read while it is in flight. */
export interface InFlightReadCoordinator<T> {
  read(): Promise<T>
  pending(): boolean
}

export function createInFlightReadCoordinator<T>(
  readValue: () => Promise<T> | T,
): InFlightReadCoordinator<T> {
  let inFlight: Promise<T> | null = null

  const read = (): Promise<T> => {
    if (inFlight) return inFlight
    let request: Promise<T>
    try {
      request = Promise.resolve(readValue())
    } catch (error) {
      request = Promise.reject(error)
    }
    const settled = request.finally(() => {
      if (inFlight === settled) inFlight = null
    })
    inFlight = settled
    return settled
  }

  return {
    read,
    pending: () => inFlight !== null,
  }
}

export function createFullListReadCoordinator<T>(
  readFullList: () => Promise<readonly T[]> | readonly T[],
): FullListReadCoordinator<T> {
  let cached: T[] | null = null
  let inFlight: Promise<T[]> | null = null
  let inFlightGeneration: number | null = null
  let queuedForce: Promise<T[]> | null = null
  let cacheGeneration = 0

  const snapshot = (items: readonly T[]): T[] => [...items]

  const start = (): Promise<T[]> => {
    const generation = cacheGeneration
    const request = Promise.resolve()
      .then(readFullList)
      .then(items => {
        if (generation === cacheGeneration) cached = snapshot(items)
        return snapshot(items)
      })
    const settled = request.finally(() => {
      if (inFlight === settled) {
        inFlight = null
        inFlightGeneration = null
      }
    })
    inFlight = settled
    inFlightGeneration = generation
    return settled
  }

  const read = (force = false): Promise<T[]> => {
    if (inFlight) {
      const invalidated = inFlightGeneration !== cacheGeneration
      if (!force && !invalidated) return inFlight
      if (!queuedForce) {
        const active = inFlight
        const queuedGeneration = cacheGeneration
        queuedForce = active
          .catch(() => undefined)
          .then(() => {
            queuedForce = null
            // A clear/seed may have established a newer authority while the
            // invalidated request was settling. Reuse that authority; only
            // start a fresh walk when no newer cache exists.
            if (queuedGeneration !== cacheGeneration && cached !== null) {
              return snapshot(cached)
            }
            return read(true)
          })
      }
      return queuedForce
    }
    if (!force && cached !== null) return Promise.resolve(snapshot(cached))
    return start()
  }

  return {
    read,
    seed(items) {
      cacheGeneration += 1
      cached = snapshot(items)
      return snapshot(cached)
    },
    peek() {
      return cached === null ? null : snapshot(cached)
    },
    pending() {
      return inFlight !== null || queuedForce !== null
    },
    generation() {
      return cacheGeneration
    },
    clear() {
      cacheGeneration += 1
      cached = null
    },
  }
}

/** Query and selected filters switch a table from one-page server mode to a complete local view. */
export function hasActiveTableFilters(query: string, filters: Record<string, string>): boolean {
  return !!query.trim() || Object.values(filters).some(Boolean)
}

/** Keep ResourceTable's cursor contract explicit without inventing a total. */
export function tablePageInfo(nextCursor?: string | null): TablePageInfo {
  const cursor = nextCursor || null
  return { hasNext: cursor !== null, nextCursor: cursor }
}

/** Compare every user-visible request dimension before committing an async read. */
export function sameTableRequest(current: TableRequestState, expected: TableRequestState): boolean {
  if (current.mode !== expected.mode || current.active !== expected.active ||
    current.page !== expected.page || current.pageSize !== expected.pageSize ||
    current.query !== expected.query || current.cursor !== expected.cursor) return false
  const keys = new Set([...Object.keys(current.filters), ...Object.keys(expected.filters)])
  return [...keys].every(key => current.filters[key] === expected.filters[key])
}
