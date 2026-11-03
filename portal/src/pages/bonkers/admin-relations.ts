import type { LocationQuery } from 'vue-router'
import type { AdminOrg, AdminProvider, AdminProviderReference, AdminWorkspace } from '@/stores/admin'

export function workspaceReferences(workspace: AdminWorkspace): AdminProviderReference[] {
  if (workspace.providersUnavailable) return []
  return workspace.providerReferences ?? workspace.providers.map(name => ({ name, scope: 'unknown', exportPath: '' }))
}

export function platformProvider(reference: AdminProviderReference, providers: AdminProvider[]) {
  if (reference.scope !== 'platform') return undefined
  return providers.find(provider => provider.name === reference.name
    && (!provider.apiExportPath || provider.apiExportPath === reference.exportPath))
}

export function providerUsages(reference: Pick<AdminProviderReference, 'name' | 'scope' | 'exportPath'>, orgs: AdminOrg[]) {
  if (reference.scope === 'unknown' || !reference.exportPath) return []
  return orgs.flatMap(org => org.workspaces.flatMap(workspace => workspaceReferences(workspace)
    .filter(binding => binding.name === reference.name && binding.scope === reference.scope && binding.exportPath === reference.exportPath)
    .map(binding => ({
      name: `${org.name}/${workspace.uuid}`, displayName: workspace.displayName || workspace.uuid,
      organization: org.displayName || org.name, orgId: org.name, workspaceId: workspace.uuid,
      state: binding.terminating ? 'Disabling' : 'Enabled', deletionBlocked: binding.deletionBlocked,
    }))))
}

export function workspaceLocation(orgId: string, workspaceId: string) {
  return { path: '/bonkers/organizations', query: { organization: orgId, workspace: workspaceId } }
}

export function providerReferenceLocation(reference: AdminProviderReference, orgId: string, workspaceId: string, context: Record<string, unknown> = {}, originView = 'workspace') {
  return { path: '/bonkers/providers', query: {
    ...tableContext(context), originView,
    provider: reference.name, providerScope: reference.scope, exportPath: reference.exportPath,
    originOrganization: orgId, originWorkspace: workspaceId,
  } }
}

// Only known resource coordinates may change the cross-section return path.
export function organizationReturn(query: Record<string, unknown>, orgId: string) {
  if (!orgId || query.returnOrganization !== orgId) return null
  return { path: '/bonkers/organizations', query: { ...tableContext(query), organization: orgId } }
}

export type AdminTableChange = { query: string; page: number; pageSize: number; sort?: { key: string; direction: 'asc' | 'desc' } | null }
const tablePrefixes = ['organizations', 'providers', 'workspaces', 'bindings', 'workspaceProviders', 'providerWorkspaces'] as const
const text = (value: unknown) => typeof value === 'string' ? value.slice(0, 256) : ''
const positivePage = (value: unknown) => Math.min(100000, Math.max(1, Math.floor(Number(value) || 1)))
export function tableState(query: Record<string, unknown>, prefix: string, sortKeys: string[] = []) {
  const key = text(query[`${prefix}Sort`])
  return {
    query: text(query[`${prefix}Query`]), page: positivePage(query[`${prefix}Page`]),
    pageSize: [10, 25, 50, 100].includes(Number(query[`${prefix}Size`])) ? Number(query[`${prefix}Size`]) : 10,
    sort: sortKeys.includes(key) ? { key, direction: query[`${prefix}Direction`] === 'desc' ? 'desc' as const : 'asc' as const } : sortKeys.length ? { key: sortKeys[0]!, direction: 'asc' as const } : null,
  }
}
export function tableContext(query: Record<string, unknown>): Record<string, string> {
  const result: Record<string, string> = {}
  for (const prefix of tablePrefixes) for (const suffix of ['Query', 'Page', 'Size', 'Sort', 'Direction']) {
    const key = `${prefix}${suffix}`
    if (typeof query[key] === 'string' && query[key]) result[key] = text(query[key])
  }
  return result
}
export function changeTableQuery(query: LocationQuery, prefix: string, change: AdminTableChange) {
  return { ...query, [`${prefix}Query`]: change.query || undefined, [`${prefix}Page`]: change.page > 1 ? String(change.page) : undefined,
    [`${prefix}Size`]: change.pageSize !== 10 ? String(change.pageSize) : undefined,
    [`${prefix}Sort`]: change.sort?.key, [`${prefix}Direction`]: change.sort?.direction }
}
export function bindingKey(workspaceId: string, reference: AdminProviderReference) {
  return JSON.stringify([workspaceId, reference.name, reference.scope, reference.exportPath])
}
export function bindingRows(org: AdminOrg, providers: AdminProvider[]) {
  return org.workspaces.flatMap(workspace => workspaceReferences(workspace).map(reference => {
    const provider = platformProvider(reference, providers)
    return { ...reference, key: bindingKey(workspace.uuid, reference), displayName: provider?.displayName || reference.name,
      workspaceId: workspace.uuid, workspace: workspace.displayName || workspace.uuid,
      scopeLabel: reference.scope === 'platform' ? 'Platform' : reference.scope === 'organization' ? 'Organization' : 'Unknown',
      bindingState: reference.terminating ? 'Disabling' : 'Enabled',
      health: reference.scope === 'organization' ? 'Not reported here' : provider?.registered ? provider.ready ? 'Ready' : 'Not ready' : 'Unavailable' }
  }))
}
