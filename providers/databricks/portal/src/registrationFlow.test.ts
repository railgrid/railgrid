import {
  discoveryCoordinatesEqual,
  discoveryRequestKey,
  materializeRegistrationResults,
  mergeRegistrationResults,
  retryableRegistrationIndices,
  selectKey,
  snapshotDiscoveryCoordinates,
  suggestedRegistrationName,
  summarizeRegistration,
} from './registrationFlow.js'

function equal(actual: unknown, expected: unknown, label: string) { if (JSON.stringify(actual) !== JSON.stringify(expected)) throw new Error(`${label}: ${JSON.stringify(actual)}`) }
equal(suggestedRegistrationName('Main Catalog', 'Sales_Data', 'Orders'), 'main-catalog-sales-data-orders', 'normalized name')
equal(suggestedRegistrationName('___'), 'imported-resource', 'fallback name')
equal(selectKey(['a'], 'a', true), ['a'], 'deduplicated selection')
equal(selectKey(['a', 'b'], 'a', false), ['b'], 'deselection')
equal(summarizeRegistration([{ index: 0, state: 'created' }, { index: 1, state: 'conflict' }]), '1 created, 1 conflict', 'summary')

const coordinates = snapshotDiscoveryCoordinates({ connectionRef: 'sales', warehouseRef: 'warehouse', catalog: 'main', schema: 'orders' })
const sameCoordinates = snapshotDiscoveryCoordinates(coordinates)
equal(discoveryCoordinatesEqual(coordinates, sameCoordinates), true, 'coordinate snapshot')
equal(discoveryRequestKey('tables', coordinates, 'page-1') === discoveryRequestKey('tables', coordinates, 'page-2'), false, 'page ownership key')

const items = [{ name: 'orders', warehouseID: 'wh-1' }, { name: 'customers', warehouseID: 'wh-2' }, { name: 'events', warehouseID: 'wh-3' }]
const initial = materializeRegistrationResults(items, [
  { index: 0, state: 'created' },
  { index: 1, state: 'conflict', message: 'different spec' },
  { index: 2, state: 'failed', message: 'temporary failure' },
])
equal(initial.map(result => result.state), ['created', 'conflict', 'failed'], 'indexed results')
equal(retryableRegistrationIndices(initial), [2], 'failed result selection')
const retried = mergeRegistrationResults(initial, [items[2]], [{ index: 0, state: 'existing' }], [2])
equal(retried.map(result => result.state), ['created', 'conflict', 'existing'], 'retry merges only failed item')

const missing = materializeRegistrationResults(items.slice(0, 2), [{ index: 1, state: 'existing' }])
equal(missing.map(result => result.state), ['failed', 'existing'], 'missing result is retryable')
