import {
  importPrerequisiteMessage,
  importPrerequisitesReady,
  nextValidWarehouseRef,
  warehousesForConnection,
} from './tableRefs.js'
import type { InitializationState } from './registrationTypes.js'

interface WarehouseRef {
  name: string
  connectionRef: string
}

function assertEqual<T>(actual: T, expected: T, label: string) {
  if (actual !== expected) {
    throw new Error(`${label}: expected ${String(expected)}, got ${String(actual)}`)
  }
}

function assertArray(actual: string[], expected: string[], label: string) {
  const matches = actual.length === expected.length && actual.every((value, index) => value === expected[index])
  if (!matches) {
    throw new Error(`${label}: expected [${expected.join(', ')}], got [${actual.join(', ')}]`)
  }
}

const warehouses: WarehouseRef[] = [
  { name: 'orders-sql', connectionRef: 'orders' },
  { name: 'finance-sql', connectionRef: 'finance' },
  { name: 'orders-large', connectionRef: 'orders' },
]

const ready: InitializationState = { connections: 'success', warehouses: 'success', tables: 'success' }
const loading: InitializationState = { connections: 'loading', warehouses: 'success', tables: 'success' }
const failed: InitializationState = { connections: 'error', warehouses: 'success', tables: 'success' }

assertEqual(importPrerequisitesReady('warehouse', ready, 'orders', '', warehouses), true, 'warehouse browse is ready after successful initialization and connection selection')
assertEqual(importPrerequisitesReady('warehouse', loading, 'orders', '', warehouses), false, 'warehouse browse waits for initialization')
assertEqual(importPrerequisitesReady('warehouse', failed, 'orders', '', warehouses), false, 'warehouse browse stays disabled after initialization failure')
assertEqual(importPrerequisitesReady('warehouse', ready, '', '', warehouses), false, 'warehouse browse requires a selected connection')
assertEqual(importPrerequisitesReady('table', ready, 'orders', 'orders-sql', warehouses), true, 'table browse accepts a same-connection warehouse')
assertEqual(importPrerequisitesReady('table', ready, 'orders', 'finance-sql', warehouses), false, 'table browse rejects a warehouse from another connection')
assertEqual(importPrerequisitesReady('table', ready, 'orders', '', warehouses), false, 'table browse requires a selected warehouse')

assertEqual(importPrerequisiteMessage([], warehouses), 'Add a connection before importing tables.', 'missing connection prerequisite')
assertEqual(importPrerequisiteMessage(['orders'], []), 'Add a warehouse before importing tables.', 'missing warehouse prerequisite')
assertEqual(importPrerequisiteMessage(['orders', 'finance'], [{ name: 'finance-sql', connectionRef: 'finance' }], 'orders'), 'Register a warehouse on the selected connection before importing tables.', 'foreign warehouse does not satisfy selected connection prerequisite')
assertEqual(importPrerequisiteMessage(['orders'], warehouses), '', 'prerequisites satisfied')

assertArray(warehousesForConnection(warehouses, 'orders').map(wh => wh.name), ['orders-sql', 'orders-large'], 'filters warehouses by connection')
assertArray(warehousesForConnection(warehouses, '').map(wh => wh.name), [], 'empty connection has no warehouses')

assertEqual(nextValidWarehouseRef(warehouses, 'orders', 'orders-large'), 'orders-large', 'keeps matching warehouse')
assertEqual(nextValidWarehouseRef(warehouses, 'orders', 'finance-sql'), 'orders-sql', 'replaces mismatched warehouse')
assertEqual(nextValidWarehouseRef(warehouses, 'finance', ''), 'finance-sql', 'selects first matching warehouse')
assertEqual(nextValidWarehouseRef(warehouses, 'unknown', 'orders-sql'), '', 'clears when no warehouse matches')

const manyWarehouses = Array.from({ length: 101 }, (_, index) => ({ name: `warehouse-${index}`, connectionRef: 'orders' }))
assertEqual(warehousesForConnection(manyWarehouses, 'orders').length, 101, 'connection selection retains warehouses after item 100')
assertEqual(nextValidWarehouseRef(manyWarehouses, 'orders', 'warehouse-100'), 'warehouse-100', 'valid warehouse selection after item 100 is preserved')
