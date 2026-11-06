import type {
  ProjectAssistantContextResource,
  ProviderItem,
  ProviderResourceCoordinate,
  RailgridContext,
} from './types'
import {
  agentKitResourceInstances,
  agentKitResourceProviders,
  agentKitResourceSelectionKey,
  discoverAgentKitResources,
  parseAgentKitBoundResource,
  type AgentKitResourceDiscoveryResult,
  type AgentKitResourceGroup,
  type AgentKitResourceProvider,
  type AgentKitResourceType,
} from './agentkit/composer'
import { providerFetch } from './portalkit/tenant'
import { createKubeClient, kubeResourcePath, type KubeObject, type KubeResourceRef } from './portalkit/kube'

export type AssistantResourceType = AgentKitResourceType

export interface AssistantResourceInstance extends ProjectAssistantContextResource {
  providerDisplayName: string
  uid: string
  resourceVersion: string
}

export type AssistantResourceGroup = AgentKitResourceGroup<AssistantResourceInstance>
export type AssistantResourceDiscoveryResult = AgentKitResourceDiscoveryResult<AssistantResourceInstance>

// AssistantResourceRequest is the provider-owned kube REST request for one
// bound resource type. The shared parser validates identifiers; this adapter
// owns the tenant path and authenticated transport.
export interface AssistantResourceRequest {
  ref: KubeResourceRef
  path: string
}

export function parseAssistantBoundResource(bound: ProviderResourceCoordinate | null | undefined): Omit<AssistantResourceType, 'provider' | 'providerDisplayName'> | null {
  return parseAgentKitBoundResource(bound)
}

export function assistantResourceProviders(providers: ProviderItem[]): AgentKitResourceProvider[] {
  return agentKitResourceProviders(providers)
}

export function buildAssistantResourceRequest(type: Pick<AssistantResourceType, 'apiVersion' | 'kind' | 'resource'>, tenant: string): AssistantResourceRequest {
  const parsed = parseAgentKitBoundResource(type)
  if (!parsed) throw new Error('Provider Action publishes an invalid bound resource')
  const cluster = tenant.trim()
  if (!cluster) throw new Error('tenant context unavailable')
  const [group, version] = parsed.apiVersion.split('/')
  const ref: KubeResourceRef = { group, version, resource: parsed.resource }
  return { ref, path: kubeResourcePath(cluster, ref) }
}

async function queryResourceType(
  ctx: RailgridContext,
  type: AssistantResourceType,
  fetcher: typeof fetch | undefined,
): Promise<AssistantResourceGroup> {
  const tenant = ctx.tenant?.trim() ?? ''
  if (!tenant || !(typeof ctx.fetch === 'function' || ctx.token?.trim())) throw new Error('tenant context unavailable')
  const { ref } = buildAssistantResourceRequest(type, tenant)
  // An injected fetcher (tests) is used as-is; otherwise the host-owned
  // transport injects Authorization so this module never handles the token.
  const client = createKubeClient({ fetch: fetcher ?? providerFetch(ctx), cluster: tenant })
  const list = await client.list<KubeObject>(ref)
  const items = agentKitResourceInstances(type, list.items).map((item) => ({
    ...item,
    uid: item.uid ?? '',
    resourceVersion: item.resourceVersion ?? '',
  })) as AssistantResourceInstance[]
  return { type, items }
}

export async function discoverAssistantResources(
  ctx: RailgridContext | null,
  types: AssistantResourceType[],
  fetcher?: typeof fetch,
): Promise<AssistantResourceDiscoveryResult> {
  if (!ctx) return { groups: [], warnings: ['Tenant context is unavailable.'] }
  return discoverAgentKitResources<AssistantResourceInstance>(types, (type) => queryResourceType(ctx, type, fetcher))
}

export function assistantResourceSelectionKey(resource: ProjectAssistantContextResource): string {
  return agentKitResourceSelectionKey(resource)
}
