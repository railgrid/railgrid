import type {
  FarosContext,
  ProjectAssistantContextResource,
  ProviderActionBoundResource,
  ProviderItem,
} from './types'

const DNS_LABEL = /^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/
const VERSION = /^[a-z][a-z0-9]*$/
const RESOURCE = /^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/
const KIND = /^[A-Za-z][A-Za-z0-9]*$/

export interface AssistantResourceType {
  provider: string
  providerDisplayName: string
  apiVersion: string
  kind: string
  resource: string
}

export interface AssistantResourceInstance extends ProjectAssistantContextResource {
  providerDisplayName: string
  uid: string
  resourceVersion: string
}

export interface AssistantResourceGroup {
  type: AssistantResourceType
  items: AssistantResourceInstance[]
}

export interface AssistantResourceDiscoveryResult {
  groups: AssistantResourceGroup[]
  warnings: string[]
}

interface GraphQLResourceQuery {
  query: string
  groupField: string
  versionField: string
  listField: string
}

function resourceTypeKey(type: Pick<AssistantResourceType, 'provider' | 'apiVersion' | 'kind' | 'resource'>): string {
  return [type.provider, type.apiVersion, type.kind, type.resource].join('\u0000')
}

export function parseAssistantBoundResource(bound: ProviderActionBoundResource | null | undefined): Omit<AssistantResourceType, 'provider' | 'providerDisplayName'> | null {
  if (!bound) return null
  const apiVersion = typeof bound.apiVersion === 'string' ? bound.apiVersion.trim() : ''
  const separator = apiVersion.indexOf('/')
  if (separator <= 0 || separator === apiVersion.length - 1 || apiVersion.indexOf('/', separator + 1) >= 0) return null
  const group = apiVersion.slice(0, separator)
  const version = apiVersion.slice(separator + 1)
  const kind = typeof bound.kind === 'string' ? bound.kind.trim() : ''
  const resource = typeof bound.resource === 'string' ? bound.resource.trim() : ''
  const validGroup = group.length <= 253 && group.split('.').every((label) => label.length > 0 && label.length <= 63 && DNS_LABEL.test(label))
  if (!validGroup || !VERSION.test(version) || !RESOURCE.test(resource) || !KIND.test(kind)) return null
  return { apiVersion: `${group}/${version}`, kind, resource }
}

export function assistantResourceProviders(providers: ProviderItem[]): Array<ProviderItem & { resourceTypes: AssistantResourceType[] }> {
  return providers
    .filter((provider) => provider?.ready === true && typeof provider.name === 'string' && provider.name.trim().length > 0)
    .map((provider) => {
      const providerName = provider.name.trim()
      const providerDisplayName = typeof provider.displayName === 'string' && provider.displayName.trim() ? provider.displayName.trim() : providerName
      const seen = new Set<string>()
      const resourceTypes: AssistantResourceType[] = []
      for (const action of Array.isArray(provider.actions) ? provider.actions : []) {
        if (!action || typeof action !== 'object') continue
        if (action.deprecation?.deprecated) continue
        const parsed = parseAssistantBoundResource(action.boundResource)
        if (!parsed) continue
        const type = { provider: providerName, providerDisplayName, ...parsed }
        const key = resourceTypeKey(type)
        if (seen.has(key)) continue
        seen.add(key)
        resourceTypes.push(type)
      }
      resourceTypes.sort((a, b) => a.kind.localeCompare(b.kind) || a.apiVersion.localeCompare(b.apiVersion) || a.resource.localeCompare(b.resource))
      return { ...provider, name: providerName, displayName: providerDisplayName, resourceTypes }
    })
    .filter((provider) => provider.resourceTypes.length > 0)
    .sort((a, b) => (a.displayName || a.name).localeCompare(b.displayName || b.name) || a.name.localeCompare(b.name))
}

export function buildAssistantResourceQuery(type: Pick<AssistantResourceType, 'apiVersion' | 'kind' | 'resource'>): GraphQLResourceQuery {
  const parsed = parseAssistantBoundResource(type)
  if (!parsed) throw new Error('Provider Action publishes an invalid bound resource')
  const [group, version] = parsed.apiVersion.split('/')
  const groupField = group.replace(/[^A-Za-z0-9_]/g, '_')
  const versionField = version
  const listField = parsed.resource.charAt(0).toUpperCase() + parsed.resource.slice(1)
  if (!/^[_A-Za-z][_0-9A-Za-z]*$/.test(groupField) || !/^[_A-Za-z][_0-9A-Za-z]*$/.test(versionField) || !/^[_A-Za-z][_0-9A-Za-z]*$/.test(listField)) {
    throw new Error('Provider Action cannot be represented safely in GraphQL')
  }
  return {
    groupField,
    versionField,
    listField,
    query: `query AppStudioContextResources { ${groupField} { ${versionField} { ${listField} { items { metadata { name uid resourceVersion } } } } } }`,
  }
}

function graphqlEndpoint(tenant: string): string {
  return `/graphql/${encodeURIComponent(tenant)}`
}

async function queryResourceType(ctx: FarosContext, type: AssistantResourceType, fetcher: typeof fetch): Promise<AssistantResourceGroup> {
  const tenant = ctx.tenant?.trim() ?? ''
  const token = ctx.token?.trim() ?? ''
  if (!tenant || !token) throw new Error('tenant context unavailable')
  const built = buildAssistantResourceQuery(type)
  const response = await fetcher(graphqlEndpoint(tenant), {
    method: 'POST',
    credentials: 'same-origin',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ query: built.query }),
  })
  if (!response.ok) throw new Error(`request failed (${response.status})`)
  const payload = await response.json() as { data?: Record<string, unknown>; errors?: unknown[] }
  if (payload.errors?.length) throw new Error('provider query failed')
  const group = payload.data?.[built.groupField] as Record<string, unknown> | undefined
  const version = group?.[built.versionField] as Record<string, unknown> | undefined
  const list = version?.[built.listField] as { items?: unknown[] } | undefined
  const items: AssistantResourceInstance[] = []
  const seen = new Set<string>()
  for (const candidate of list?.items ?? []) {
    if (!candidate || typeof candidate !== 'object') continue
    const metadata = (candidate as { metadata?: Record<string, unknown> }).metadata
    const name = typeof metadata?.name === 'string' ? metadata.name.trim() : ''
    if (!name || seen.has(name)) continue
    seen.add(name)
    items.push({
      provider: type.provider,
      providerDisplayName: type.providerDisplayName,
      resourceRef: { apiVersion: type.apiVersion, kind: type.kind, resource: type.resource, name },
      uid: typeof metadata?.uid === 'string' ? metadata.uid : '',
      resourceVersion: typeof metadata?.resourceVersion === 'string' ? metadata.resourceVersion : '',
    })
  }
  items.sort((a, b) => a.resourceRef.name.localeCompare(b.resourceRef.name))
  return { type, items }
}

export async function discoverAssistantResources(
  ctx: FarosContext | null,
  types: AssistantResourceType[],
  fetcher: typeof fetch = fetch,
): Promise<AssistantResourceDiscoveryResult> {
  if (!ctx) return { groups: [], warnings: ['Tenant context is unavailable.'] }
  const settled = await Promise.allSettled(types.map((type) => queryResourceType(ctx, type, fetcher)))
  const groups: AssistantResourceGroup[] = []
  const warnings: string[] = []
  settled.forEach((result, index) => {
    const type = types[index]
    if (result.status === 'fulfilled') groups.push(result.value)
    else warnings.push(`${type.kind} resources are temporarily unavailable.`)
  })
  groups.sort((a, b) => a.type.kind.localeCompare(b.type.kind) || a.type.apiVersion.localeCompare(b.type.apiVersion))
  return { groups, warnings }
}

export function assistantResourceSelectionKey(resource: ProjectAssistantContextResource): string {
  const ref = resource.resourceRef
  return [resource.provider, ref.apiVersion, ref.kind, ref.resource, ref.name].join('\u0000')
}
