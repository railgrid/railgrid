export type CollectionPage = 'connections' | 'warehouses' | 'tables'
export type CreateKind = 'connection' | 'warehouse' | 'table'
export type CreateMode = 'manual' | 'browse'

export interface CollectionRoute {
  page: CollectionPage
  connection?: string
  table?: string
  warehouse?: string
}

export interface CreateRoute {
  page: 'create'
  kind: CreateKind
  mode: CreateMode
}

export type DatabricksRoute = CollectionRoute | CreateRoute

function decodeSegment(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}

/**
 * Parse the path after `/providers/databricks/`.
 *
 * The shell owns the provider prefix and passes only this relative subPath to
 * the custom element. Keeping that boundary here prevents a provider route
 * from accidentally dispatching a second `/providers/databricks` prefix.
 */
export function parseSubPath(sub: string | null | undefined): DatabricksRoute {
  const normalized = (sub ?? '').replace(/^\/+|\/+$/g, '')
  const parts = normalized ? normalized.split('/') : []

  if (parts[0] === 'create') {
    if (parts[1] === 'connection' && parts.length === 2) {
      return { page: 'create', kind: 'connection', mode: 'manual' }
    }
    if ((parts[1] === 'warehouse' || parts[1] === 'table') &&
      (parts[2] === 'manual' || parts[2] === 'browse') && parts.length === 3) {
      return { page: 'create', kind: parts[1], mode: parts[2] }
    }
    return { page: 'connections' }
  }

  if (parts[0] === 'connections') {
    return parts.length > 1
      ? { page: 'connections', connection: decodeSegment(parts.slice(1).join('/')) }
      : { page: 'connections' }
  }
  if (parts[0] === 'warehouses') {
    return parts.length > 1
      ? { page: 'warehouses', warehouse: decodeSegment(parts.slice(1).join('/')) }
      : { page: 'warehouses' }
  }
  if (parts[0] === 'tables') {
    return parts.length > 1
      ? { page: 'tables', table: decodeSegment(parts.slice(1).join('/')) }
      : { page: 'tables' }
  }
  return { page: 'connections' }
}

export function collectionPath(page: CollectionPage): string {
  return page
}

export function createPath(kind: CreateKind, mode: CreateMode = 'manual'): string {
  return kind === 'connection' ? 'create/connection' : `create/${kind}/${mode}`
}

export function detailPath(page: Exclude<CollectionPage, 'connections'> | 'connections', name: string): string {
  return `${page}/${encodeURIComponent(name)}`
}
