import {
  importPrerequisiteMessage,
  nextValidWarehouseRef,
  warehousesForConnection,
} from './tableRefs.js'

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

assertEqual(importPrerequisiteMessage([], warehouses), 'Add a connection before importing tables.', 'missing connection prerequisite')
assertEqual(importPrerequisiteMessage(['orders'], []), 'Add a warehouse before importing tables.', 'missing warehouse prerequisite')
assertEqual(importPrerequisiteMessage(['orders'], warehouses), '', 'prerequisites satisfied')

assertArray(warehousesForConnection(warehouses, 'orders').map(wh => wh.name), ['orders-sql', 'orders-large'], 'filters warehouses by connection')
assertArray(warehousesForConnection(warehouses, '').map(wh => wh.name), [], 'empty connection has no warehouses')

assertEqual(nextValidWarehouseRef(warehouses, 'orders', 'orders-large'), 'orders-large', 'keeps matching warehouse')
assertEqual(nextValidWarehouseRef(warehouses, 'orders', 'finance-sql'), 'orders-sql', 'replaces mismatched warehouse')
assertEqual(nextValidWarehouseRef(warehouses, 'finance', ''), 'finance-sql', 'selects first matching warehouse')
assertEqual(nextValidWarehouseRef(warehouses, 'unknown', 'orders-sql'), '', 'clears when no warehouse matches')
