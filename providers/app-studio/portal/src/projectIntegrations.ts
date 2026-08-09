import type {
  ProjectIntegration,
  ProjectProviderActionGrant,
  ProviderAction,
  ProviderItem,
} from './types'

const schemaDigestPattern = /^sha256:[a-f0-9]{64}$/

export interface ReadyProviderAction {
  provider: ProviderItem
  action: ProviderAction
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
 * Return only catalog actions that can be selected for a new grant. A
 * provider must be Ready, deprecated actions are not selectable, and a
 * digest is immutable catalog evidence rather than an optional UI value.
 */
export function readyProviderActions(providers: ProviderItem[]): ReadyProviderAction[] {
  return providers
    .filter((provider) => provider.ready)
    .flatMap((provider) => (provider.actions ?? [])
      .filter((action) => !action.deprecation?.deprecated && schemaDigestPattern.test(action.schemaDigest))
      .map((action) => ({ provider, action })))
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
  action: ProviderAction,
  alias: string,
  resourceName: string,
  consentAccepted: boolean,
): ProjectIntegrationCreatePayload | null {
  const actionID = splitProviderActionID(action.id)
  const normalizedAlias = alias.trim()
  const normalizedResourceName = resourceName.trim()
  if (!actionID || !provider.ready || !normalizedAlias || !normalizedResourceName || action.deprecation?.deprecated) return null
  if (!schemaDigestPattern.test(action.schemaDigest)) return null
  return {
    alias: normalizedAlias,
    provider: provider.name,
    kind: 'providerReference',
    resourceRef: {
      name: normalizedResourceName,
      apiVersion: action.boundResource.apiVersion,
      kind: action.boundResource.kind,
      resource: action.boundResource.resource,
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
