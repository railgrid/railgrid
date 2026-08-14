import type {
  DiscoveryCoordinates,
  DiscoveryLevel,
  RegistrationItem,
  RegistrationResult,
} from './registrationTypes.js'

export function suggestedRegistrationName(...parts: string[]): string {
  const value = parts.map(part => part.trim().toLowerCase()).filter(Boolean).join('-')
    .replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 63).replace(/-+$/g, '')
  return value || 'imported-resource'
}

export function selectKey(values: readonly string[], value: string, selected: boolean): string[] {
  const next = new Set(values)
  if (selected) next.add(value); else next.delete(value)
  return [...next]
}

export function summarizeRegistration(results: readonly RegistrationResult[]): string {
  const counts = new Map<string, number>()
  for (const result of results) counts.set(result.state, (counts.get(result.state) ?? 0) + 1)
  return ['created', 'existing', 'conflict', 'failed'].filter(state => counts.has(state)).map(state => `${counts.get(state)} ${state}`).join(', ')
}

/**
 * Capture the hierarchy inputs used by one discovery request.  A request must
 * be compared with the current coordinates before its response is committed;
 * a connection/catalog/schema change otherwise lets an old page populate the
 * new hierarchy.
 */
export function snapshotDiscoveryCoordinates(input: Partial<DiscoveryCoordinates>): DiscoveryCoordinates {
  return {
    connectionRef: input.connectionRef ?? '',
    warehouseRef: input.warehouseRef ?? '',
    catalog: input.catalog ?? '',
    schema: input.schema ?? '',
  }
}

export function discoveryCoordinatesEqual(left: DiscoveryCoordinates, right: DiscoveryCoordinates): boolean {
  return left.connectionRef === right.connectionRef
    && left.warehouseRef === right.warehouseRef
    && left.catalog === right.catalog
    && left.schema === right.schema
}

export function discoveryRequestKey(level: DiscoveryLevel, coordinates: DiscoveryCoordinates, pageToken?: string): string {
  return JSON.stringify([level, coordinates.connectionRef, coordinates.warehouseRef, coordinates.catalog, coordinates.schema, pageToken ?? ''])
}

/**
 * Materialize one result per submitted item.  The API returns indexes relative
 * to the request body, so a missing or malformed result is represented as a
 * retryable failure instead of silently disappearing from the review.
 */
export function materializeRegistrationResults(
  items: readonly RegistrationItem[],
  response: readonly RegistrationResult[],
  originalIndices: readonly number[] = items.map((_, index) => index),
): RegistrationResult[] {
  const byIndex = new Map<number, RegistrationResult>()
  for (const result of response) {
    if (Number.isInteger(result.index) && result.index >= 0 && !byIndex.has(result.index)) byIndex.set(result.index, result)
  }
  return items.map((item, batchIndex) => {
    const result = byIndex.get(batchIndex)
    const index = originalIndices[batchIndex] ?? batchIndex
    if (!result) {
      return {
        index,
        name: item.name,
        state: 'failed',
        message: 'The provider did not return a result for this item. Retry it.',
      }
    }
    return { ...result, index, name: result.name || item.name }
  })
}

/** Merge a retry batch while preserving the result and index of every item. */
export function mergeRegistrationResults(
  current: readonly RegistrationResult[],
  items: readonly RegistrationItem[],
  response: readonly RegistrationResult[],
  originalIndices: readonly number[],
): RegistrationResult[] {
  const next = new Map<number, RegistrationResult>(current.map(result => [result.index, result]))
  for (const result of materializeRegistrationResults(items, response, originalIndices)) next.set(result.index, result)
  return [...next.values()].sort((left, right) => left.index - right.index)
}

export function retryableRegistrationIndices(results: readonly RegistrationResult[]): number[] {
  return results.filter(result => result.state === 'failed').map(result => result.index)
}
