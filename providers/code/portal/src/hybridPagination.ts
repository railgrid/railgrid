/**
 * Coordinates the query-independent full read behind a hybrid cursor table.
 *
 * A server page is allowed to seed this cache only when the caller has proved
 * that it is the complete first page. Otherwise the first active query starts
 * one bounded walk. Repeated callers share the in-flight walk, and a stale
 * caller cannot discard a successful result because the cache is updated by
 * the coordinator before the caller applies its request guard.
 */
export interface FullListReadCoordinator<T> {
  read(force?: boolean): Promise<T[]>
  seed(items: readonly T[]): T[]
  peek(): T[] | null
  clear(): void
}

export function createFullListReadCoordinator<T>(
  readFullList: () => Promise<readonly T[]> | readonly T[],
): FullListReadCoordinator<T> {
  let cached: T[] | null = null
  let epoch = 0
  let inFlight: { epoch: number; promise: Promise<T[]> } | null = null

  const snapshot = (items: readonly T[]): T[] => [...items]

  const read = (force = false): Promise<T[]> => {
    if (inFlight) {
      if (inFlight.epoch === epoch) return inFlight.promise
      // A clear/seed invalidated the older walk. Let it settle before starting
      // a replacement so full walks remain serialized, but never return the
      // invalidated result as the new authority.
      return inFlight.promise.then(() => read(force), () => read(force))
    }
    if (!force && cached) return Promise.resolve(snapshot(cached))

    const readEpoch = epoch
    const request = Promise.resolve()
      .then(readFullList)
      .then(items => {
        const result = snapshot(items)
        if (readEpoch === epoch) cached = result
        return snapshot(result)
      })
    const settled = request.finally(() => {
      if (inFlight?.promise === settled) inFlight = null
    })
    inFlight = { epoch: readEpoch, promise: settled }
    return settled
  }

  return {
    read,
    seed(items) {
      epoch += 1
      cached = snapshot(items)
      return snapshot(cached)
    },
    peek() {
      return cached ? snapshot(cached) : null
    },
    clear() {
      epoch += 1
      cached = null
    },
  }
}
