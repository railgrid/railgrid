import type {
  FarosContext,
  ProjectAssistantContextResource,
  ProviderActionBoundResource,
  ProviderItem,
} from './types'
import { providerFetch } from './portalkit/tenant'
import { createKubeClient, kubeResourcePath, type KubeObject, type KubeResourceRef } from './portalkit/kube'

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

// AssistantResourceRequest is the kube REST list request for one bound
// resource type: the typed ref the kube client consumes plus the exact
// /clusters/<tenant>/apis/<group>/<version>/<resource> path it resolves to.
export interface AssistantResourceRequest {
  ref: KubeResourceRef
  path: string
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

export function buildAssistantResourceRequest(type: Pick<AssistantResourceType, 'apiVersion' | 'kind' | 'resource'>, tenant: string): AssistantResourceRequest {
  // parseAssistantBoundResource is the injection guard: only DNS-shaped
  // group/version/resource identifiers reach the URL builder, and the kube
  // client percent-encodes every segment on top of that.
  const parsed = parseAssistantBoundResource(type)
  if (!parsed) throw new Error('Provider Action publishes an invalid bound resource')
  const cluster = tenant.trim()
  if (!cluster) throw new Error('tenant context unavailable')
  const [group, version] = parsed.apiVersion.split('/')
  const ref: KubeResourceRef = { group, version, resource: parsed.resource }
  return { ref, path: kubeResourcePath(cluster, ref) }
}

async function queryResourceType(ctx: FarosContext, type: AssistantResourceType, fetcher: typeof fetch | undefined): Promise<AssistantResourceGroup> {
  const tenant = ctx.tenant?.trim() ?? ''
  if (!tenant || !(typeof ctx.fetch === 'function' || ctx.token?.trim())) throw new Error('tenant context unavailable')
  const { ref } = buildAssistantResourceRequest(type, tenant)
  // An injected fetcher (tests) is used as-is; otherwise the host-owned
  // transport injects Authorization so this module never handles the token.
  const client = createKubeClient({ fetch: fetcher ?? providerFetch(ctx), cluster: tenant })
  const list = await client.list<KubeObject>(ref)
  const items: AssistantResourceInstance[] = []
  const seen = new Set<string>()
  for (const candidate of list.items) {
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
  fetcher?: typeof fetch,
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
