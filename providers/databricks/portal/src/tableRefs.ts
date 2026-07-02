export interface WarehouseReference {
  name: string
  connectionRef: string
}

export function importPrerequisiteMessage(
  connections: readonly unknown[],
  warehouses: readonly WarehouseReference[],
): string {
  if (connections.length === 0) return 'Add a connection before importing tables.'
  if (warehouses.length === 0) return 'Add a warehouse before importing tables.'
  return ''
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
