import snapshotSuffixPattern from './generated/model-snapshot-suffix.json'
import { createKubeClient, isKubeForbidden, isKubeNotFound, type KubeClient, type KubeObject, type KubeResourceRef } from './portalkit/kube'
import { providerFetch } from './portalkit/tenant'
import type {
  ProjectLLMDiscoveredModel,
  ProjectLLMSettings,
  ProjectLLMModelSettings,
  ProjectModelProviderConnection,
  RailgridContext,
} from './types'
import catalogEntries from './generated/model-catalog.json'

// Read-only model registry access. The Studio stores model revisions and can
// pin a shared ModelProviderConnection snapshot; connection status carries key
// identity, while credential bytes remain in Secrets and are never read here.
// The Studio reconciler reports whether each model's credential is configured.
export const STUDIO_NAME = 'studio'
export const STUDIO_RESOURCE: KubeResourceRef = { group: 'ai.railgrid.ai', version: 'v1alpha1', resource: 'studios' }
export const MODEL_PROVIDER_CONNECTION_RESOURCE: KubeResourceRef = { group: 'ai.railgrid.ai', version: 'v1alpha1', resource: 'modelproviderconnections' }
export const CREDENTIAL_KEY = 'apiKey'
export const GENERATED_SECRET_ANNOTATION = 'ai.railgrid.ai/generated-secret-id'
export const CONNECTION_DISPLAY_NAME_ANNOTATION = 'ai.railgrid.ai/display-name'

export interface StudioLLMModel {
  id: string
  revisionID: string
  archived?: boolean
  name: string
  connectionRef?: string
  connectionSnapshot?: {
    connectionRef: string
    connectionUID: string
    provider: string
    baseURL: string
    anthropicWorkspaceID?: string
    secretRef: { name: string }
    secretKey: string
    secretUID: string
    secretResourceVersion: string
    connectionFingerprint: string
    generatedSecretID?: string
  }
  provider?: string
  baseURL?: string
  model: string
  reasoningEffort?: string
  secretRef?: { name: string }
}

export interface StudioLLMSpec {
  defaultModel?: string
  models?: StudioLLMModel[]
  runtime?: { maxRetries?: number; retryBackoffMS?: number; streamIdleTimeoutMS?: number }
}

export interface StudioObject extends KubeObject {
  spec?: { llm?: StudioLLMSpec }
  status?: { models?: Array<{ id: string; configured?: boolean }> }
}

export interface ModelProviderConnectionObject extends KubeObject {
  spec?: { provider?: string; baseURL?: string; anthropicWorkspaceID?: string; secretRef?: { name?: string }; secretKey?: string }
  status?: {
    activeSnapshot?: ProjectModelProviderConnection['activeSnapshot']
    observedGeneration?: number
    connectionFingerprint?: string
    credentialMode?: 'api-key' | 'service-account-json'
    secretUID?: string
    secretResourceVersion?: string
    models?: Array<string | { id?: string; name?: string; compatibility?: string; capabilities?: string[] }>
    lastProbeTime?: string
    lastProbeError?: string
    conditions?: Array<{ type?: string; status?: string; reason?: string; message?: string; lastTransitionTime?: string }>
  }
}

export class LLMRegistryError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'LLMRegistryError'
  }
}

interface CatalogEntry {
  id: string
  inputPer1M: number
  outputPer1M: number
  contextWindow?: number
  vision?: boolean
  toolCall?: boolean
  reasoning?: boolean
}

const catalog = catalogEntries as CatalogEntry[]

// lookupCatalog mirrors modelcatalog.LookupModel: normalize (lowercase, drop
// any provider prefix), then accept exact IDs, dated snapshots, or latest aliases.
// Unknown generations and variants must not inherit an older model price.
export function lookupCatalog(model: string): CatalogEntry | undefined {
  const normalized = model.toLowerCase().trim().split('/').pop() ?? ''
  if (!normalized) return undefined
  const exact = catalog.find((entry) => entry.id === normalized)
  if (exact) return exact
  let best: CatalogEntry | undefined
  for (const entry of catalog) {
    if (normalized.startsWith(entry.id) && new RegExp(snapshotSuffixPattern).test(normalized.slice(entry.id.length)) && (!best || entry.id.length > best.id.length)) best = entry
  }
  return best
}

export function kube(ctx: RailgridContext | null): KubeClient {
  const cluster = ctx?.tenant?.trim() ?? ''
  if (!cluster) throw new LLMRegistryError('select an organization and workspace first')
  return createKubeClient({ fetch: providerFetch(ctx), cluster })
}

export async function readStudio(client: KubeClient): Promise<StudioObject | null> {
  try {
    return await client.get<StudioObject>(STUDIO_RESOURCE, STUDIO_NAME)
  } catch (e) {
    // No Studio yet means no models yet, which is a normal empty state and
    // not something to show the user an error about.
    if (isKubeNotFound(e)) return null
    throw e
  }
}

export function connectionView(object: ModelProviderConnectionObject): ProjectModelProviderConnection {
  const conditions = (object.status?.conditions ?? []).flatMap(condition => {
    if (!condition.type || !condition.status) return []
    return [{
      type: condition.type,
      status: condition.status,
      reason: condition.reason,
      message: condition.message,
      lastTransitionTime: condition.lastTransitionTime,
    }]
  })
  const condition = (type: string) => conditions.find(item => item.type === type)
  const flag = (type: string): boolean | undefined => {
    const status = condition(type)?.status
    return status === 'True' ? true : status === 'False' ? false : undefined
  }
  const models: ProjectLLMDiscoveredModel[] = (object.status?.models ?? []).flatMap(model => {
    const id = typeof model === 'string' ? model : model.id
    if (!id) return []
    const metadata = typeof model === 'string' ? undefined : model
    const compatibility = metadata?.compatibility === 'recommended' || metadata?.compatibility === 'unsuitable'
      ? metadata.compatibility
      : 'available'
    return [{ id, name: metadata?.name || id, compatibility, capabilities: metadata?.capabilities }]
  })
  return {
    name: object.metadata.name,
    uid: object.metadata.uid,
    resourceVersion: object.metadata.resourceVersion,
    generation: object.metadata.generation,
    observedGeneration: object.status?.observedGeneration,
    displayName: object.metadata.annotations?.[CONNECTION_DISPLAY_NAME_ANNOTATION] || undefined,
    provider: object.spec?.provider ?? '',
    baseURL: object.spec?.baseURL ?? '',
    anthropicWorkspaceID: object.spec?.anthropicWorkspaceID || undefined,
    credentialMode: object.status?.credentialMode,
    secretResolved: flag('SecretResolved'),
    reachable: flag('Reachable'),
    ready: flag('Ready'),
    activeSnapshot: object.status?.activeSnapshot,
    activationPending: !object.status?.activeSnapshot
      || object.status.observedGeneration !== object.metadata.generation
      || object.status.activeSnapshot.connectionFingerprint !== object.status.connectionFingerprint
      || object.status.activeSnapshot.secretRef?.name?.trim() !== object.spec?.secretRef?.name?.trim(),
    connectionFingerprint: object.status?.connectionFingerprint,
    secretUID: object.status?.secretUID,
    secretResourceVersion: object.status?.secretResourceVersion,
    secretName: object.spec?.secretRef?.name?.trim(),
    secretKey: object.spec?.secretKey,
    generatedSecretID: object.metadata.annotations?.[GENERATED_SECRET_ANNOTATION],
    models,
    conditions,
    lastProbeTime: object.status?.lastProbeTime,
    lastProbeError: object.status?.lastProbeError,
    deleting: Boolean(object.metadata.deletionTimestamp),
  }
}

export function toSettings(studio: StudioObject | null, connections: ProjectModelProviderConnection[] = []): ProjectLLMSettings {
  const spec = studio?.spec?.llm ?? {}
  const configured = new Map<string, boolean>()
  const byConnection = new Map(connections.map(connection => [connection.name, connection]))
  for (const entry of studio?.status?.models ?? []) {
    if (entry?.id) configured.set(entry.id, !!entry.configured)
  }

  const models: ProjectLLMModelSettings[] = (spec.models ?? [])
    .filter((model) => model && !model.archived)
    .map((model) => {
      const connection = model.connectionRef ? byConnection.get(model.connectionRef) : undefined
      const snapshot = connection?.activeSnapshot ?? model.connectionSnapshot
      return {
        catalog: lookupCatalog(model.model),
        id: model.id,
        revisionID: model.revisionID,
        name: model.name,
        connectionRef: model.connectionRef,
        connectionSnapshot: snapshot,
        connectionName: connection?.displayName || connection?.name || model.connectionRef,
        connectionFingerprint: snapshot?.connectionFingerprint ?? connection?.connectionFingerprint,
        provider: snapshot?.provider ?? model.provider ?? connection?.provider ?? '',
        baseURL: snapshot?.baseURL ?? model.baseURL ?? connection?.baseURL ?? '',
        anthropicWorkspaceID: snapshot ? snapshot.anthropicWorkspaceID : connection?.anthropicWorkspaceID,
        credentialMode: connection?.credentialMode,
        model: model.model,
        reasoningEffort: model.reasoningEffort || undefined,
        // From the reconciler's observation, never from a key we hold.
        configured: configured.get(model.id) ?? false,
        default: model.id === spec.defaultModel,
      }
    })
  models.sort((a, b) => Number(b.default ?? false) - Number(a.default ?? false) || a.name.toLowerCase().localeCompare(b.name.toLowerCase()))

  const selected = models.find((model) => model.default) ?? models[0]
  return {
    provider: selected?.provider ?? '',
    baseURL: selected?.baseURL ?? '',
    model: selected?.model ?? '',
    configured: !!selected?.configured,
    defaultModelID: spec.defaultModel,
    models,
  }
}

export async function getLLMSettings(ctx: RailgridContext | null): Promise<ProjectLLMSettings> {
  const client = kube(ctx)
  const studio = await readStudio(client)
  const connectionNames = [...new Set((studio?.spec?.llm?.models ?? [])
    .filter(model => !model.archived && model.connectionRef?.trim())
    .map(model => model.connectionRef!.trim()))]
  const connectionReads = await Promise.allSettled(connectionNames.map(name =>
    client.get<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, name),
  ))
  // Missing or forbidden metadata still permits snapshot-backed, label-only edits.
  // Unexpected failures must remain observable without preventing model loading.
  connectionReads.forEach((result, index) => {
    if (result.status === 'rejected' && !isKubeNotFound(result.reason) && !isKubeForbidden(result.reason)) {
      console.warn('Model provider connection metadata unavailable', connectionNames[index], result.reason)
    }
  })
  const connections = connectionReads.flatMap(result => result.status === 'fulfilled' ? [connectionView(result.value)] : [])
  return toSettings(studio, connections)
}

export async function listModelProviderConnections(ctx: RailgridContext | null): Promise<ProjectModelProviderConnection[]> {
  const items = await kube(ctx).listAll<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE)
  return items.map(connectionView).sort((left, right) => left.name.localeCompare(right.name))
}

export async function getModelProviderConnection(ctx: RailgridContext | null, name: string): Promise<ProjectModelProviderConnection> {
  return connectionView(await kube(ctx).get<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, name))
}
