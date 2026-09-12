// Canonical portal URL contract. Navigation is separate from provider asset URLs.
export const UUID_PATTERN = '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'
export const ORGANIZATION_ROUTE = `/:orgID(${UUID_PATTERN})`
export const WORKSPACE_ROUTE = `${ORGANIZATION_ROUTE}/:workspaceID(${UUID_PATTERN})`
const scopePattern = new RegExp(`^/(${UUID_PATTERN})(?:/(${UUID_PATTERN}))?(?=/|$)`)

export interface NavigationScope {
  orgUUID: string | null
  workspaceUUID: string | null
}

/** Takes a router path (without /ui); global routes have no scope. */
export function parsePortalScope(path: string): NavigationScope | null {
  const match = scopePattern.exec(path)
  return match ? { orgUUID: match[1], workspaceUUID: match[2] ?? null } : null
}

export function portalRoutePath(path: string): string {
  return path.replace(scopePattern, '') || '/'
}

/** Builds a router path. No remembered/default workspace is inferred here. */
export function scopedPath(path: string, scope: NavigationScope): string {
  if (parsePortalScope(path) || /^\/(?:login|auth|organizations|bonkers)(?:[/?#]|$)/.test(path)) return path
  if (!scope.orgUUID) return '/organizations'
  const org = `/${encodeURIComponent(scope.orgUUID)}`
  if (path === '/settings' || path.startsWith('/settings/')) return org + path
  if (!scope.workspaceUUID) return path === '/providers' ? org + path : org + '/settings/workspaces'
  return `${org}/${encodeURIComponent(scope.workspaceUUID)}${path === '/' ? '' : path}`
}

/** Native links use the current document's URL, never cross-tab localStorage. */
export function portalHref(path: string, scope?: Partial<NavigationScope> | null): string {
  const route = path.replace(/^\/ui(?=\/|$)/, '') || '/'
  const current = scope === undefined && typeof window !== 'undefined' && !!window.location?.pathname
    ? parsePortalScope(window.location.pathname.replace(/^\/ui(?=\/|$)/, ''))
    : scope
  return current ? '/ui' + scopedPath(route, { orgUUID: current.orgUUID ?? null, workspaceUUID: current.workspaceUUID ?? null }) : '/ui/'
}
