import { UUID_PATTERN, type NavigationScope } from '@/portalkit/navigation'

type Account = { userId: string; email: string } | null
const uuid = new RegExp(`^${UUID_PATTERN}$`)

function key(account: Account): string | null {
  const identity = account?.userId || account?.email
  return identity ? `railgrid:portal:last-visited:${JSON.stringify(identity)}` : null
}

// A landing preference survives sign-out, separately from active tenant state.
// Store only IDs, per account; the router must recheck access before entering.
export function rememberLandingScope(account: Account, scope: NavigationScope): void {
  const storageKey = key(account)
  if (!storageKey || !scope.orgUUID) return
  try {
    localStorage.setItem(storageKey, JSON.stringify(scope))
    if (scope.workspaceUUID) localStorage.setItem(`${storageKey}:org:${scope.orgUUID}`, scope.workspaceUUID)
  } catch { /* Storage is optional. */ }
}

export function readLandingScope(account: Account): NavigationScope | null {
  const storageKey = key(account)
  if (!storageKey) return null
  try {
    const value = JSON.parse(localStorage.getItem(storageKey) ?? 'null')
    if (!value || typeof value.orgUUID !== 'string' || !uuid.test(value.orgUUID)) return null
    if (value.workspaceUUID !== null && (typeof value.workspaceUUID !== 'string' || !uuid.test(value.workspaceUUID))) return null
    return { orgUUID: value.orgUUID, workspaceUUID: value.workspaceUUID }
  } catch { return null }
}

// Organization-only pages must not erase the last operating workspace.
export function readOrganizationWorkspace(account: Account, orgUUID: string): string | null {
  const storageKey = key(account)
  if (!storageKey) return null
  try {
    const value = localStorage.getItem(`${storageKey}:org:${orgUUID}`)
    if (value && uuid.test(value)) return value
    const legacy = readLandingScope(account)
    return legacy?.orgUUID === orgUUID ? legacy.workspaceUUID : null
  } catch { return null }
}
