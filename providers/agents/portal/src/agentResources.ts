import type { RailgridContext } from './types'
import {
  agentKitResourceInstances,
  agentKitResourceProviders,
  discoverAgentKitResources,
  type AgentKitResourceDiscoveryResult,
  type AgentKitResourceGroup,
  type AgentKitResourceInstance,
  type AgentKitResourceProvider,
  type AgentKitResourceType,
} from './agentkit/composer'
import type { AgentProviderResourceCoordinate } from './api'
import { providerFetch } from './portalkit/tenant'
import { createKubeClient, type KubeObject, type KubeResourceRef } from './portalkit/kube'

export function agentResourceProviders(providers: AgentProviderResourceCoordinate[]): AgentKitResourceProvider[] {
  return agentKitResourceProviders(providers)
}

async function listResourceType(
  context: RailgridContext | null,
  type: AgentKitResourceType,
): Promise<AgentKitResourceGroup<AgentKitResourceInstance & { providerDisplayName: string }>> {
  const tenant = context?.tenant?.trim() || ''
  if (!context || !tenant) throw new Error('Workspace context is unavailable.')
  const [group, version] = type.apiVersion.split('/')
  const ref: KubeResourceRef = { group, version, resource: type.resource }
  const client = createKubeClient({ fetch: providerFetch(context), cluster: tenant })
  const result = await client.list<KubeObject>(ref)
  const items = agentKitResourceInstances(type, result.items || []).map((item) => ({
    ...item,
    providerDisplayName: type.providerDisplayName,
  }))
  return { type, items }
}

export async function discoverAgentResources(
  context: RailgridContext | null,
  types: AgentKitResourceType[],
): Promise<AgentKitResourceDiscoveryResult<AgentKitResourceInstance & { providerDisplayName: string }>> {
  if (!context) return { groups: [], warnings: ['Workspace context is unavailable.'] }
  return discoverAgentKitResources(types, (type) => listResourceType(context, type))
}
