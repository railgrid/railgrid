import type {
  RailgridContext,
  ProjectIntegration,
  ProjectProviderActionGrant,
  ProviderAction,
  ProviderItem,
} from './types'

const schemaDigestPattern = /^sha256:[a-f0-9]{64}$/

// BoundProviderAction is one action paired with the exported resource it hangs
// off. The catalog publishes the coordinate on the parent resource entry only,
// so everything that needs an action's kind walks these instead of reading the
// action. Mirrors providerCatalogBoundAction on the gateway side.
export interface BoundProviderAction {
  apiVersion: string
  kind: string
  resource: string
  action: ProviderAction
}

export interface ReadyProviderAction extends BoundProviderAction {
  provider: ProviderItem
}

/**
 * Flatten one catalog entry's export into every action it publishes, in
 * declaration order, each paired with its parent's coordinate. A resource
 * missing any of that triple is skipped: the catalog is external data, and an
 * action nothing can be addressed on is not a grantable action.
 */
export function providerBoundActions(provider: ProviderItem): BoundProviderAction[] {
  const out: BoundProviderAction[] = []
  for (const resource of provider.export?.resources ?? []) {
    const apiVersion = typeof resource.apiVersion === 'string' ? resource.apiVersion.trim() : ''
    const kind = typeof resource.kind === 'string' ? resource.kind.trim() : ''
    const name = typeof resource.name === 'string' ? resource.name.trim() : ''
    if (!apiVersion || !kind || !name) continue
    for (const action of resource.actions ?? []) {
      out.push({ apiVersion, kind, resource: name, action })
    }
  }
  return out
}

export interface ProjectIntegrationCreatePayload {
  alias: string
  provider: string
  kind: 'providerReference'
  resourceRef: {
    name: string
    apiVersion: string
    kind: string
    resource: string
  }
  allowedActions: ProjectProviderActionGrant[]
  consentAccepted?: boolean
}

/**
 * The project integration read is owned by the selected project and tenant
 * workspace. Token rotation and host context object replacement do not change
 * that resource authority, so they intentionally do not participate here.
 */
export function projectIntegrationsAuthorityKey(ctx: RailgridContext | null, projectName: string): string {
  return JSON.stringify([
    projectName.trim(),
    ctx?.tenant ?? '',
    ctx?.orgUUID ?? '',
    ctx?.workspaceUUID ?? '',
    ctx?.user?.userId || ctx?.user?.sub || ctx?.user?.email || '',
  ])
}

/** Return whether an async integration read may commit its result. */
export function projectIntegrationsRequestIsCurrent(
  requestSerial: number,
  requestAuthority: string,
  currentSerial: number,
  currentAuthority: string,
): boolean {
  return requestSerial === currentSerial && requestAuthority === currentAuthority
}

/**
 * Return only catalog actions that can be selected for a new grant. A
 * provider must be Ready, deprecated actions are not selectable, and a
 * digest is immutable catalog evidence rather than an optional UI value.
 */
export function readyProviderActions(providers: ProviderItem[]): ReadyProviderAction[] {
  return providers
    .filter((provider) => provider.ready)
    .flatMap((provider) => providerBoundActions(provider)
      .filter(({ action }) => !action.deprecation?.deprecated && schemaDigestPattern.test(action.schemaDigest))
      .map((bound) => ({ provider, ...bound })))
    .sort((left, right) => {
      const providerOrder = (left.provider.displayName || left.provider.name).localeCompare(right.provider.displayName || right.provider.name)
      if (providerOrder !== 0) return providerOrder
      return left.action.id.localeCompare(right.action.id)
    })
}

export function splitProviderActionID(id: string): { name: string; version: string } | null {
  const parts = id.trim().split('/')
  if (parts.length !== 2 || !parts[0] || !parts[1]) return null
  return { name: parts[0], version: parts[1] }
}

export function buildProjectIntegrationCreatePayload(
  provider: ProviderItem,
  bound: BoundProviderAction,
  alias: string,
  resourceName: string,
  consentAccepted: boolean,
): ProjectIntegrationCreatePayload | null {
  const action = bound.action
  const actionID = splitProviderActionID(action.id)
  const normalizedAlias = alias.trim()
  const normalizedResourceName = resourceName.trim()
  if (!actionID || !provider.ready || !normalizedAlias || !normalizedResourceName || action.deprecation?.deprecated) return null
  if (!schemaDigestPattern.test(action.schemaDigest)) return null
  return {
    alias: normalizedAlias,
    provider: provider.name,
    kind: 'providerReference',
    // The grant names the kind the catalog publishes on the action's parent
    // resource, never anything the caller typed.
    resourceRef: {
      name: normalizedResourceName,
      apiVersion: bound.apiVersion,
      kind: bound.kind,
      resource: bound.resource,
    },
    allowedActions: [{ name: actionID.name, version: actionID.version, schemaDigest: action.schemaDigest }],
    consentAccepted,
  }
}

export function buildProjectIntegrationRevokePayload(
  integration: ProjectIntegration,
  actionName: string,
  actionVersion: string,
): { allowedActions: ProjectProviderActionGrant[]; consentAccepted: true } | null {
  const target = integration.allowedActions.find((action) =>
    action.name === actionName && action.version === actionVersion && !action.revoked,
  )
  if (!target) return null
  return {
    allowedActions: integration.allowedActions.map((action) => {
      const revoked = action.name === actionName && action.version === actionVersion ? true : action.revoked
      return {
        name: action.name,
        version: action.version,
        schemaDigest: action.schemaDigest,
        ...(revoked === undefined ? {} : { revoked }),
      }
    }),
    // The server requires an explicit consent marker for consent-gated
    // catalog actions on every grant mutation. The user has just confirmed
    // this destructive revoke in the portalkit dialog.
    consentAccepted: true,
  }
}
