import type { InitializationResource, InitializationState } from './registrationTypes.js'

export interface WarehouseReference {
  name: string
  connectionRef: string
}

export type ImportKind = 'warehouse' | 'table'

export function importPrerequisiteMessage(
  connections: readonly unknown[],
  warehouses: readonly WarehouseReference[],
  connectionRef?: string,
): string {
  if (connections.length === 0) return 'Add a connection before importing tables.'
  const matchingWarehouses = connectionRef
    ? warehouses.some(warehouse => warehouse.connectionRef === connectionRef)
    : warehouses.length > 0
  if (!matchingWarehouses) {
    return connectionRef
      ? 'Register a warehouse on the selected connection before importing tables.'
      : 'Add a warehouse before importing tables.'
  }
  return ''
}

export function importPrerequisitesReady(
  kind: ImportKind,
  initializationState: Readonly<InitializationState>,
  connectionRef: string,
  warehouseRef: string,
  warehouses: readonly WarehouseReference[],
): boolean {
  const required: readonly InitializationResource[] = kind === 'table'
    ? ['connections', 'warehouses', 'tables']
    : ['connections', 'warehouses']
  if (!required.every(resource => initializationState[resource] === 'success')) return false
  if (!connectionRef) return false
  return kind === 'warehouse' || warehouses.some(warehouse => warehouse.name === warehouseRef && warehouse.connectionRef === connectionRef)
}

export function warehousesForConnection<T extends WarehouseReference>(
  warehouses: readonly T[],
  connectionRef: string,
): T[] {
  if (!connectionRef) return []
  return warehouses.filter(warehouse => warehouse.connectionRef === connectionRef)
}

export function nextValidWarehouseRef(
  warehouses: readonly WarehouseReference[],
  connectionRef: string,
  currentWarehouseRef: string,
): string {
  const candidates = warehousesForConnection(warehouses, connectionRef)
  if (candidates.some(warehouse => warehouse.name === currentWarehouseRef)) return currentWarehouseRef
  return candidates[0]?.name ?? ''
}
