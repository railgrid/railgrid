// Mutation operations for the workspace's LLM registry. New model revisions
// can pin shared ModelProviderConnections and their immutable key snapshots;
// legacy direct model keys remain supported. Studio updates are conditional on
// the observed UID/resourceVersion, and connection edits reserve immutable key
// versions. Read APIs and public read types live in ./llmRegistryRead so page
// startup can load settings without downloading this mutation code.
import { isKubeConflict, isKubeNotFound, KubeError, type KubeClient, type KubeObject, type KubeResourceRef } from './portalkit/kube'
import {
  CREDENTIAL_KEY,
  CONNECTION_DISPLAY_NAME_ANNOTATION,
  GENERATED_SECRET_ANNOTATION,
  LLMRegistryError,
  MODEL_PROVIDER_CONNECTION_RESOURCE,
  STUDIO_NAME,
  STUDIO_RESOURCE,
  connectionView,
  getLLMSettings,
  kube,
  readStudio,
  toSettings,
  type ModelProviderConnectionObject,
  type StudioLLMModel,
  type StudioLLMSpec,
  type StudioObject,
} from './llmRegistryRead'
import type {
  ProjectLLMSettings,
  ProjectModelProviderConnection,
  ProjectModelProviderConnectionWrite,
  ProjectLLMConnectionSnapshot,
  RailgridContext,
} from './types'

export {
  LLMRegistryError,
  getLLMSettings,
  listModelProviderConnections,
  getModelProviderConnection,
  lookupCatalog,
} from './llmRegistryRead'
export type { StudioLLMModel, StudioLLMSpec } from './llmRegistryRead'

const SECRET_RESOURCE: KubeResourceRef = { group: '', version: 'v1', resource: 'secrets', namespaced: true }
const SECRET_NAMESPACE = 'default'
const PROVIDER_CONNECTION_FINALIZER = 'ai.railgrid.ai/model-provider-connection-protection'
const PENDING_SECRET_ANNOTATION = 'ai.railgrid.ai/pending-key-secret'
const PROVIDER_CONNECTION_SECRET_PREFIX = 'railgrid-app-studio-model-'
const PROVIDER_CONNECTION_API_VERSION = 'ai.railgrid.ai/v1alpha1'

// These bounds mirror the reconciler. The server remains authoritative; the
// portal checks first so a save reports the limit before it is sent.
const MAX_ACTIVE_MODELS = 20
const MAX_REVISIONS = 200

// credentialSecretName mirrors the reconciler's LLMCredentialSecretName. The
// name is derived from the model id rather than chosen, so the writer here and
// the reader in the provider agree without passing a name between them.
export function credentialSecretName(modelID: string): string {
  return `railgrid-projects-llm-${modelID.trim()}`
}

// modelID slugifies a display name into a DNS label, matching the pattern the
// CRD enforces. Doing it here means the API server's rejection is a backstop
// rather than the normal path.
export function modelID(value: string): string {
  const slug = value
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63)
    .replace(/^-+|-+$/g, '')
  return slug || 'model'
}

function newRevisionID(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  return `rev-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
}

async function connectionSnapshotFor(
  client: KubeClient,
  connectionRef: string,
  candidate?: ProjectModelProviderConnection,
): Promise<ProjectLLMConnectionSnapshot> {
  let connection: ModelProviderConnectionObject | null = null
  let secretName = ''
  let secretUID = ''
  let secretResourceVersion = ''
  let connectionFingerprint = ''
  for (let attempt = 0; attempt < 6; attempt += 1) {
    connection = await client.get<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, connectionRef)
    if (connection.metadata.deletionTimestamp) {
      throw new LLMRegistryError('This provider connection is being deleted. Refresh connections and choose another provider before saving a model.')
    }
    secretName = connection.spec?.secretRef?.name?.trim() ?? ''
    const candidateMatches = candidate?.name === connectionRef && candidate.uid === connection.metadata.uid
      && (candidate.generation === undefined || candidate.generation === connection.metadata.generation)
      && candidate.secretName === secretName
      && candidate.provider === (connection.spec?.provider ?? '')
      && candidate.baseURL === (connection.spec?.baseURL ?? '')
      && (candidate.anthropicWorkspaceID ?? '') === (connection.spec?.anthropicWorkspaceID ?? '')
      && (candidate.secretKey ?? CREDENTIAL_KEY) === (connection.spec?.secretKey ?? CREDENTIAL_KEY)
    if (candidate && !candidateMatches) {
      throw new LLMRegistryError('The provider connection changed while you were saving this model. Refresh and verify again.')
    }
    const observedCurrentGeneration = connection.metadata.generation !== undefined
      && connection.status?.observedGeneration === connection.metadata.generation
    connectionFingerprint = observedCurrentGeneration ? connection.status?.connectionFingerprint ?? '' : ''
    if (connection.status?.activeSnapshot?.connectionFingerprint !== connectionFingerprint) connectionFingerprint = ''
    const observedSecretUID = observedCurrentGeneration ? connection.status?.secretUID ?? '' : ''
    const observedSecretResourceVersion = observedCurrentGeneration ? connection.status?.secretResourceVersion ?? '' : ''
    if (observedCurrentGeneration && candidateMatches && candidate?.secretUID && observedSecretUID && candidate.secretUID !== observedSecretUID) {
      throw new LLMRegistryError('The saved provider key changed while you were saving this model. Refresh and verify again.')
    }
    if (observedCurrentGeneration && candidateMatches && candidate?.secretResourceVersion && observedSecretResourceVersion && candidate.secretResourceVersion !== observedSecretResourceVersion) {
      throw new LLMRegistryError('The saved provider key changed while you were saving this model. Refresh and verify again.')
    }
    if (observedCurrentGeneration && candidateMatches && candidate?.connectionFingerprint && connectionFingerprint && candidate.connectionFingerprint !== connectionFingerprint) {
      throw new LLMRegistryError('The provider connection changed while you were saving this model. Refresh and verify again.')
    }
    secretUID = observedSecretUID
    secretResourceVersion = observedSecretResourceVersion
    if (connection.metadata.uid && secretName && connection.spec?.provider && connection.spec.baseURL
      && connectionFingerprint && secretUID && secretResourceVersion) break
    if (attempt < 5) await new Promise(resolve => setTimeout(resolve, 250))
  }
  if (!connection?.metadata.uid || !secretName || !connection.spec?.provider || !connection.spec.baseURL
    || !connectionFingerprint || !secretUID || !secretResourceVersion) {
    throw new LLMRegistryError('The provider connection is still confirming its saved key version. Wait briefly, refresh, and try again.')
  }
  const active = connection.status?.activeSnapshot
  if (!active || active.connectionRef !== connectionRef || active.connectionUID !== connection.metadata.uid
    || active.secretRef?.name !== secretName || active.secretUID !== secretUID
    || active.secretResourceVersion !== secretResourceVersion || active.connectionFingerprint !== connectionFingerprint) {
    throw new LLMRegistryError('The provider connection is still confirming its admitted configuration. Refresh and try again.')
  }
  // The backend compares the entire snapshot with controller-owned admission.
  // Preserve its normalized transport instead of reconstructing it from spec.
  return structuredClone(active)
}

// writeRegistry merge-patches spec.llm. The whole list goes in the patch
// because `models` is an atomic replacement from the client's point of view —
// it holds no secrets, so rewriting it costs nothing and avoids the
// list-merge-key subtleties of a partial update.
async function writeRegistry(client: KubeClient, spec: StudioLLMSpec, baseline: StudioObject | null): Promise<void> {
  const active = (spec.models ?? []).filter((model) => !model.archived)
  if (active.length > MAX_ACTIVE_MODELS) {
    throw new LLMRegistryError(`at most ${MAX_ACTIVE_MODELS} model configurations are supported`)
  }
  if ((spec.models ?? []).length > MAX_REVISIONS) {
    throw new LLMRegistryError('model configuration revision history is full')
  }
  if (!baseline?.metadata.uid || !baseline.metadata.resourceVersion) {
    throw new LLMRegistryError('Workspace model settings could not be confirmed. Refresh before saving.')
  }
  try {
    await client.patch(STUDIO_RESOURCE, STUDIO_NAME, {
      metadata: { uid: baseline.metadata.uid, resourceVersion: baseline.metadata.resourceVersion },
      spec: { llm: spec },
    }, { type: 'merge' })
  } catch (error) {
    if (isKubeConflict(error)) throw new LLMRegistryError('Workspace model settings changed while you were editing them. Refresh and try again.')
    throw error
  }
}

// writeCredential server-side-applies ONE model's Secret. force: true because
// this field manager owns that key outright; there is no other writer to
// negotiate with, and a conflict here would strand a user who just typed a
// valid key.
async function writeCredential(client: KubeClient, id: string, apiKey: string): Promise<void> {
  await client.apply(
    SECRET_RESOURCE,
    {
      apiVersion: 'v1',
      kind: 'Secret',
      metadata: {
        name: credentialSecretName(id),
        namespace: SECRET_NAMESPACE,
        // The provider's `secrets` permission claim is selector-scoped to
        // railgrid.ai/owner: app-studio (manifest.yaml). This Secret is
        // written by the tenant AS THE CALLER, so nothing else stamps the
        // label, and an unlabelled one is invisible to the Studio reconciler
        // through the APIExport virtual workspace — the model would never
        // report `configured`.
        labels: { 'railgrid.ai/owner': 'app-studio' },
      },
      type: 'Opaque',
      stringData: { [CREDENTIAL_KEY]: apiKey },
    } as KubeObject,
    { namespace: SECRET_NAMESPACE, force: true },
  )
}

function newProviderConnectionName(): string {
  const value = crypto.randomUUID().replace(/-/g, '').toLowerCase()
  if (!/^[0-9a-f]{32}$/.test(value)) throw new LLMRegistryError('the browser could not create a provider connection identity')
  return `model-${value.slice(0, 24)}`
}

function newProviderConnectionSecret(): { id: string; name: string } {
  const id = crypto.randomUUID().replace(/-/g, '').toLowerCase()
  if (!/^[0-9a-f]{32}$/.test(id)) throw new LLMRegistryError('the browser could not create a provider key identity')
  return { id, name: `${PROVIDER_CONNECTION_SECRET_PREFIX}${id}` }
}

function stableJSONValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(stableJSONValue)
  if (value && typeof value === 'object') {
    const record = value as Record<string, unknown>
    return Object.fromEntries(Object.keys(record).sort().map(key => [key, stableJSONValue(record[key])]))
  }
  return value
}

function sameJSONValue(left: unknown, right: unknown): boolean {
  return JSON.stringify(stableJSONValue(left)) === JSON.stringify(stableJSONValue(right))
}

function expectedConnectionMatchesLatest(
  expected: ProjectModelProviderConnection,
  latest: ModelProviderConnectionObject,
): boolean {
  return expected.name === latest.metadata.name
    && expected.uid === latest.metadata.uid
    && expected.generation === latest.metadata.generation
    && expected.displayName === (latest.metadata.annotations?.[CONNECTION_DISPLAY_NAME_ANNOTATION] || undefined)
    && expected.provider === (latest.spec?.provider ?? '')
    && expected.baseURL === (latest.spec?.baseURL ?? '')
    && (expected.anthropicWorkspaceID ?? '') === (latest.spec?.anthropicWorkspaceID ?? '')
    && (expected.secretName ?? '') === (latest.spec?.secretRef?.name?.trim() ?? '')
    && (expected.secretKey ?? CREDENTIAL_KEY) === (latest.spec?.secretKey ?? CREDENTIAL_KEY)
    && (!expected.connectionFingerprint || expected.connectionFingerprint === latest.status?.connectionFingerprint)
    && (!expected.secretUID || expected.secretUID === latest.status?.secretUID)
    && (!expected.secretResourceVersion || expected.secretResourceVersion === latest.status?.secretResourceVersion)
}

function desiredConnectionSpec(
  current: ModelProviderConnectionObject['spec'],
  body: ProjectModelProviderConnectionWrite,
  secretRef?: string,
): NonNullable<ModelProviderConnectionObject['spec']> {
  const spec = { ...(current ?? {}) }
  spec.provider = body.provider.trim()
  spec.baseURL = body.baseURL.trim()
  if (body.anthropicWorkspaceID !== undefined) {
    const workspaceID = body.anthropicWorkspaceID.trim()
    if (workspaceID) spec.anthropicWorkspaceID = workspaceID
    else delete spec.anthropicWorkspaceID
  }
  if (secretRef) spec.secretRef = { name: secretRef }
  if (secretRef || !spec.secretKey) spec.secretKey = CREDENTIAL_KEY
  return spec
}

function metadataDisplayNamePatch(displayName: string | undefined, current?: Record<string, string>): Record<string, string | null> | undefined {
  if (displayName === undefined) return undefined
  const value = displayName.trim()
  if (value) return { [CONNECTION_DISPLAY_NAME_ANNOTATION]: value }
  return current?.[CONNECTION_DISPLAY_NAME_ANNOTATION]
    ? { [CONNECTION_DISPLAY_NAME_ANNOTATION]: null }
    : undefined
}

async function createConnectionKeySecret(
  client: KubeClient,
  name: string,
  uid: string,
  secret: { id: string; name: string },
  apiKey: string,
): Promise<KubeObject> {
  return client.create(SECRET_RESOURCE, {
    apiVersion: 'v1',
    kind: 'Secret',
    metadata: {
      name: secret.name,
      namespace: SECRET_NAMESPACE,
      labels: { 'railgrid.ai/owner': 'app-studio' },
      annotations: { [GENERATED_SECRET_ANNOTATION]: secret.id },
      ownerReferences: [{ apiVersion: PROVIDER_CONNECTION_API_VERSION, kind: 'ModelProviderConnection', name, uid }],
    },
    type: 'Opaque',
    immutable: true,
    stringData: { [CREDENTIAL_KEY]: apiKey },
  } as KubeObject, { namespace: SECRET_NAMESPACE })
}

function withSecretIdentity(connection: ModelProviderConnectionObject, secret: KubeObject): ModelProviderConnectionObject {
  return {
    ...connection,
    status: {
      ...connection.status,
      // A new immutable Secret version invalidates every probe observation on
      // the previous version. Keep only the identity the create response
      // confirms; the controller must publish a matching fingerprint before
      // a model binding can capture it.
      secretUID: secret.metadata.uid,
      secretResourceVersion: secret.metadata.resourceVersion,
    },
  }
}

async function activeSecretVersion(
  client: KubeClient,
  name: string,
  uid: string,
  secret: { id: string; name: string },
): Promise<ModelProviderConnectionObject | null> {
  const connection = await client.get<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, name)
  if (connection.metadata.uid !== uid
    || connection.spec?.secretRef?.name !== secret.name
    || connection.metadata.annotations?.[GENERATED_SECRET_ANNOTATION] !== secret.id
    || connection.metadata.annotations?.[PENDING_SECRET_ANNOTATION] !== undefined) return null
  return connection
}

async function releaseConnectionKeyReservation(
  client: KubeClient,
  name: string,
  uid: string,
  secretName: string,
): Promise<void> {
  try {
    const current = await client.get<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, name)
    if (current.metadata.uid !== uid || !current.metadata.resourceVersion
      || current.metadata.annotations?.[PENDING_SECRET_ANNOTATION] !== secretName) return
    await client.patch(MODEL_PROVIDER_CONNECTION_RESOURCE, name, [
      { op: 'test', path: '/metadata/uid', value: uid },
      { op: 'test', path: '/metadata/resourceVersion', value: current.metadata.resourceVersion },
      { op: 'test', path: `/metadata/annotations/${PENDING_SECRET_ANNOTATION.replace(/\//g, '~1')}`, value: secretName },
      { op: 'remove', path: `/metadata/annotations/${PENDING_SECRET_ANNOTATION.replace(/\//g, '~1')}` },
    ], { type: 'json' })
  } catch {
    // The original write error is more useful. If the object changed, the
    // reservation belongs to a newer request and its writer owns cleanup.
  }
}

async function deleteCreatedConnection(client: KubeClient, name: string, uid: string): Promise<void> {
  try {
    await client.delete(MODEL_PROVIDER_CONNECTION_RESOURCE, name, { preconditions: { uid } })
  } catch {
    // The finalizer and controller own cleanup of any exact-owned Secret.
  }
}

async function activateConnectionSecret(
  client: KubeClient,
  name: string,
  uid: string,
  originalSpec: NonNullable<ModelProviderConnectionObject['spec']>,
  desiredSpec: NonNullable<ModelProviderConnectionObject['spec']>,
  secret: { id: string; name: string },
  displayName?: string,
): Promise<ModelProviderConnectionObject> {
  let current = await client.get<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, name)
  let activated: ModelProviderConnectionObject
  for (let attempt = 1; ; attempt += 1) {
    if (current.metadata.uid !== uid || current.metadata.deletionTimestamp
      || current.metadata.annotations?.[PENDING_SECRET_ANNOTATION] !== secret.name
      || !current.spec || !sameJSONValue(current.spec, originalSpec)
      || !current.metadata.resourceVersion) {
      throw new LLMRegistryError('The provider connection changed while its key was being saved. Refresh and try again.')
    }
    // Recheck after creating the staged key: model edits during verification or
    // staging must not turn a successful connection save into a blocked rollover.
    await assertConnectionRolloverCapacity(client, name)
    const operations: Array<Record<string, unknown>> = [
      { op: 'test', path: '/metadata/uid', value: uid },
      { op: 'test', path: '/metadata/resourceVersion', value: current.metadata.resourceVersion },
      { op: 'test', path: `/metadata/annotations/${PENDING_SECRET_ANNOTATION.replace(/\//g, '~1')}`, value: secret.name },
      { op: 'test', path: '/spec', value: current.spec },
      { op: 'replace', path: '/spec', value: desiredSpec },
      { op: 'add', path: `/metadata/annotations/${GENERATED_SECRET_ANNOTATION.replace(/\//g, '~1')}`, value: secret.id },
      { op: 'remove', path: `/metadata/annotations/${PENDING_SECRET_ANNOTATION.replace(/\//g, '~1')}` },
    ]
    const displayPath = `/metadata/annotations/${CONNECTION_DISPLAY_NAME_ANNOTATION.replace(/\//g, '~1')}`
    const label = displayName?.trim()
    if (label) operations.push({ op: 'add', path: displayPath, value: label })
    else if (current.metadata.annotations?.[CONNECTION_DISPLAY_NAME_ANNOTATION]) operations.push({ op: 'remove', path: displayPath })
    try {
      activated = await client.patch<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, name, operations, { type: 'json' })
      break
    } catch (error) {
      // Kubernetes reports a failed JSON Patch test as 422. Retry only when a
      // fresh read confirms the version changed without changing write authority.
      if (!(error instanceof KubeError) || (error.status !== 409 && error.status !== 422)) throw error
      const latest = await client.get<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, name)
      if (latest.metadata.resourceVersion === current.metadata.resourceVersion) throw error
      const { resourceVersion: _currentVersion, managedFields: _currentFields, ...currentMetadata } = current.metadata
      const { resourceVersion: _latestVersion, managedFields: _latestFields, ...latestMetadata } = latest.metadata
      if (!sameJSONValue(latestMetadata, currentMetadata) || !sameJSONValue(latest.spec, current.spec)) {
        throw new LLMRegistryError('The provider connection changed while its key was being saved. Refresh and try again.')
      }
      if (attempt >= 4) {
        throw new LLMRegistryError('The provider connection status is still updating. Refresh and try again.')
      }
      current = latest
    }
  }
  if (activated.metadata.uid !== uid
    || activated.spec?.secretRef?.name !== secret.name
    || activated.metadata.annotations?.[GENERATED_SECRET_ANNOTATION] !== secret.id
    || activated.metadata.annotations?.[PENDING_SECRET_ANNOTATION] !== undefined) {
    throw new LLMRegistryError('The saved provider key could not be confirmed. Refresh before retrying.')
  }
  // Spec is desired state. Only the controller admits it for runtime after
  // its Studio CAS has written every required binding revision.
  for (let attempt = 0; attempt < 80; attempt += 1) {
    const observed = await client.get<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, name)
    if (observed.metadata.uid !== uid || !sameJSONValue(observed.spec, desiredSpec)
      || observed.metadata.annotations?.[GENERATED_SECRET_ANNOTATION] !== secret.id) {
      throw new LLMRegistryError('The provider connection changed while activation was pending. Refresh before retrying.')
    }
    const active = observed.status?.activeSnapshot
    if (active?.secretRef?.name === secret.name && active.generatedSecretID === secret.id
      && active.connectionFingerprint === observed.status?.connectionFingerprint
      && observed.status?.observedGeneration === observed.metadata.generation) return observed
    const blocked = observed.status?.conditions?.some(condition => condition.type === 'BindingsReady'
      && condition.status === 'False' && condition.reason === 'RevisionHistoryFull')
    if (blocked && observed.status?.observedGeneration === observed.metadata.generation) return observed
    await new Promise(resolve => setTimeout(resolve, 250))
  }
  throw new LLMRegistryError('Provider changes were saved and are awaiting activation. Existing model bindings retain their previous configuration. Refresh to check progress.')
}

async function reserveConnectionKey(
  client: KubeClient,
  existing: ModelProviderConnectionObject,
  secret: { id: string; name: string },
  expected?: ProjectModelProviderConnection,
): Promise<{ uid: string; originalSpec: NonNullable<ModelProviderConnectionObject['spec']> }> {
  const uid = existing.metadata.uid
  const resourceVersion = existing.metadata.resourceVersion
  if (!uid || !resourceVersion || !existing.spec) throw new LLMRegistryError('The provider connection identity could not be confirmed. Refresh before saving it.')
  if (expected && !expectedConnectionMatchesLatest(expected, existing)) {
    throw new LLMRegistryError('The provider connection changed while you were editing it. Refresh and verify again.')
  }
  const annotations = { ...(existing.metadata.annotations ?? {}), [PENDING_SECRET_ANNOTATION]: secret.name }
  const reserved = await client.patch<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, existing.metadata.name, {
    metadata: {
      uid,
      resourceVersion,
      finalizers: [...new Set([...(existing.metadata.finalizers ?? []), PROVIDER_CONNECTION_FINALIZER])],
      annotations,
    },
  }, { type: 'merge' })
  if (reserved.metadata.uid !== uid || reserved.metadata.annotations?.[PENDING_SECRET_ANNOTATION] !== secret.name) {
    throw new LLMRegistryError('The provider key reservation could not be confirmed. Refresh before saving again.')
  }
  const current = await client.get<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, existing.metadata.name)
  if (current.metadata.uid !== uid || current.metadata.deletionTimestamp
    || current.metadata.annotations?.[PENDING_SECRET_ANNOTATION] !== secret.name
    || !current.spec || !sameJSONValue(current.spec, existing.spec)) {
    throw new LLMRegistryError('The provider connection changed while its key was being saved. Refresh and try again.')
  }
  return { uid, originalSpec: current.spec }
}

async function createModelProviderConnection(
  client: KubeClient,
  name: string,
  body: ProjectModelProviderConnectionWrite,
  key: string,
  secret: { id: string; name: string },
): Promise<ModelProviderConnectionObject> {
  const spec = desiredConnectionSpec(undefined, body, secret.name)
  const annotations = {
    [PENDING_SECRET_ANNOTATION]: secret.name,
    ...(body.displayName?.trim() ? { [CONNECTION_DISPLAY_NAME_ANNOTATION]: body.displayName.trim() } : {}),
  }
  let created: ModelProviderConnectionObject
  try {
    created = await client.create<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, {
      apiVersion: PROVIDER_CONNECTION_API_VERSION,
      kind: 'ModelProviderConnection',
      metadata: { name, finalizers: [PROVIDER_CONNECTION_FINALIZER], annotations },
      spec,
    } as ModelProviderConnectionObject)
  } catch (error) {
    try {
      const uncertain = await client.get<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, name)
      if (uncertain.metadata.uid && uncertain.metadata.annotations?.[PENDING_SECRET_ANNOTATION] === secret.name) {
        await deleteCreatedConnection(client, name, uncertain.metadata.uid)
      }
    } catch {
      // A failed identity read must not risk deleting a connection we cannot
      // prove belongs to this create request.
    }
    throw error
  }
  const uid = created.metadata.uid
  if (created.metadata.name !== name || !uid || !created.spec) {
    throw new LLMRegistryError('The workspace did not confirm the provider connection identity.')
  }
  let secretObject: KubeObject | null = null
  try {
    secretObject = await createConnectionKeySecret(client, name, uid, secret, key)
    return withSecretIdentity(
      await activateConnectionSecret(client, name, uid, created.spec, spec, secret, body.displayName),
      secretObject,
    )
  } catch (error) {
    let active: ModelProviderConnectionObject | null
    try {
      active = await activeSecretVersion(client, name, uid, secret)
    } catch {
      // The patch response may have been lost. Preserve the object until its
      // exact active state can be confirmed.
      throw error
    }
    if (active && secretObject) return withSecretIdentity(active, secretObject)
    await deleteCreatedConnection(client, name, uid)
    throw error
  }
}

export async function saveModelProviderConnection(
  ctx: RailgridContext | null,
  body: ProjectModelProviderConnectionWrite,
  expectedConnection?: ProjectModelProviderConnection,
): Promise<ProjectModelProviderConnection> {
  const client = kube(ctx)
  const name = body.name?.trim() || newProviderConnectionName()
  const apiKey = body.apiKey?.trim() || ''
  let existing: ModelProviderConnectionObject | null = null
  try {
    existing = await client.get<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, name)
  } catch (error) {
    if (!isKubeNotFound(error)) throw error
  }
  if (!existing) {
    if (!apiKey) throw new LLMRegistryError('Enter an API key to create a provider connection.')
    const secret = newProviderConnectionSecret()
    return connectionView(await createModelProviderConnection(client, name, body, apiKey, secret))
  }
  if (!existing.metadata.uid || !existing.metadata.resourceVersion || !existing.spec || existing.metadata.deletionTimestamp) {
    throw new LLMRegistryError('The provider connection is being removed. Refresh before editing it.')
  }
  if (body.name && !expectedConnection) {
    throw new LLMRegistryError('Refresh the provider connection before saving changes.')
  }
  if (expectedConnection) {
    if (!expectedConnectionMatchesLatest(expectedConnection, existing)) {
      throw new LLMRegistryError('The provider connection changed while you were editing it. Refresh and verify again.')
    }
  }
  const provider = body.provider.trim()
  const baseURL = body.baseURL.trim()
  const workspaceID = body.anthropicWorkspaceID?.trim() ?? existing.spec.anthropicWorkspaceID ?? ''
  const changedProviderConfig = provider !== (existing.spec.provider ?? '')
    || baseURL !== (existing.spec.baseURL ?? '')
    || workspaceID !== (existing.spec.anthropicWorkspaceID ?? '')
  if (changedProviderConfig && !apiKey) {
    throw new LLMRegistryError('Enter the provider key again after changing its endpoint or workspace.')
  }
  if (changedProviderConfig && expectedConnection
    && (expectedConnection.connectionFingerprint !== existing.status?.connectionFingerprint
      || existing.status?.observedGeneration !== existing.metadata.generation)) {
    throw new LLMRegistryError('The provider connection status is still updating. Refresh it before verifying changes.')
  }
  const annotationPatch = metadataDisplayNamePatch(body.displayName, existing.metadata.annotations)
  if (!apiKey) {
    if (changedProviderConfig) throw new LLMRegistryError('Verify the provider with a new key before saving these changes.')
    const metadata: Record<string, unknown> = { uid: existing.metadata.uid, resourceVersion: existing.metadata.resourceVersion }
    if (annotationPatch) metadata.annotations = annotationPatch
    const saved = await client.patch<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, name, { metadata }, { type: 'merge' })
    return connectionView(saved)
  }

  await assertConnectionRolloverCapacity(client, name)
  const secret = newProviderConnectionSecret()
  const uid = existing.metadata.uid
  if (!uid) throw new LLMRegistryError('The provider connection identity could not be confirmed. Refresh before saving it.')
  let originalSpec: NonNullable<ModelProviderConnectionObject['spec']>
  try {
    ({ originalSpec } = await reserveConnectionKey(client, existing, secret, expectedConnection))
  } catch (error) {
    await releaseConnectionKeyReservation(client, name, uid, secret.name)
    throw error
  }
  const desiredSpec = desiredConnectionSpec(originalSpec, {
    ...body,
    provider,
    baseURL,
    anthropicWorkspaceID: workspaceID,
  }, secret.name)
  let secretObject: KubeObject | null = null
  try {
    secretObject = await createConnectionKeySecret(client, name, uid, secret, apiKey)
    return connectionView(withSecretIdentity(
      await activateConnectionSecret(client, name, uid, originalSpec, desiredSpec, secret, body.displayName),
      secretObject,
    ))
  } catch (error) {
    let active: ModelProviderConnectionObject | null
    try {
      active = await activeSecretVersion(client, name, uid, secret)
    } catch {
      // Leave the reservation for the connection controller if activation is
      // ambiguous. A later read can determine whether this version is active.
      throw error
    }
    if (active && secretObject) return connectionView(withSecretIdentity(active, secretObject))
    await releaseConnectionKeyReservation(client, name, uid, secret.name)
    throw error
  }
}

async function assertConnectionRolloverCapacity(client: KubeClient, name: string): Promise<void> {
  const studio = await readStudio(client)
  const models = studio?.spec?.llm?.models ?? []
  const revisionsNeeded = models.filter(model => !model.archived && model.connectionRef === name).length
  if (revisionsNeeded && models.length + revisionsNeeded > MAX_REVISIONS) {
    throw new LLMRegistryError('Model revision history has no room to update every model using this connection. The current provider key has been preserved.')
  }
}

export async function deleteModelProviderConnection(ctx: RailgridContext | null, name: string): Promise<void> {
  const client = kube(ctx)
  const studio = await readStudio(client)
  const activeBindings = (studio?.spec?.llm?.models ?? []).filter(model => !model.archived && model.connectionRef === name)
  if (activeBindings.length) {
    throw new LLMRegistryError(`Move or delete the ${activeBindings.length} model${activeBindings.length === 1 ? '' : 's'} using this connection before deleting it.`)
  }
  let connection: ModelProviderConnectionObject
  try {
    connection = await client.get<ModelProviderConnectionObject>(MODEL_PROVIDER_CONNECTION_RESOURCE, name)
  } catch (error) {
    if (isKubeNotFound(error)) return
    throw error
  }
  if (!connection.metadata.uid || !connection.metadata.resourceVersion) {
    throw new LLMRegistryError('The provider connection identity could not be confirmed. Refresh before deleting it.')
  }
  await client.delete(MODEL_PROVIDER_CONNECTION_RESOURCE, name, {
    preconditions: { uid: connection.metadata.uid, resourceVersion: connection.metadata.resourceVersion },
  })
}

export async function createLLMModel(
  ctx: RailgridContext | null,
  body: { name: string; provider?: string; baseURL?: string; connectionRef?: string; connectionInfo?: ProjectModelProviderConnection; model: string; reasoningEffort?: string; apiKey?: string },
): Promise<ProjectLLMSettings> {
  const client = kube(ctx)
  const studio = await readStudio(client)
  const spec: StudioLLMSpec = { ...(studio?.spec?.llm ?? {}) }
  const models = [...(spec.models ?? [])]

  const name = body.name.trim()
  if (!name) throw new LLMRegistryError('model configuration name is required')
  const id = modelID(name)
  if (models.some((model) => !model.archived && model.id === id)) {
    throw new LLMRegistryError(`a model configuration named ${name} already exists`)
  }

  const connectionRef = body.connectionRef?.trim()
  const connectionSnapshot = connectionRef
    ? await connectionSnapshotFor(client, connectionRef, body.connectionInfo)
    : undefined
  const binding: StudioLLMModel = {
    id,
    revisionID: newRevisionID(),
    name,
    model: body.model.trim(),
    ...(body.reasoningEffort ? { reasoningEffort: body.reasoningEffort } : {}),
    ...(connectionRef
      ? { connectionRef, connectionSnapshot }
      : {
        provider: body.provider?.trim() || undefined,
        baseURL: body.baseURL?.trim() || undefined,
        secretRef: { name: credentialSecretName(id) },
      }),
  }
  models.push(binding)
  spec.models = models
  // First model configured becomes the default, so a workspace is usable
  // immediately after adding one.
  if (!spec.defaultModel) spec.defaultModel = id

  // Legacy direct entries still own one Secret. New entries reference a shared
  // provider connection and therefore never copy or rewrite its key.
  if (!body.connectionRef?.trim()) {
    if (!body.apiKey?.trim()) throw new LLMRegistryError('a credential is required to connect this model')
    await writeCredential(client, id, body.apiKey)
  }
  await writeRegistry(client, spec, studio)
  return getLLMSettings(ctx)
}

export async function patchLLMModel(
  ctx: RailgridContext | null,
  id: string,
  body: { name?: string; expectedName?: string; expectedRevisionID?: string; provider?: string; baseURL?: string; connectionRef?: string; connectionInfo?: ProjectModelProviderConnection; model?: string; reasoningEffort?: string; apiKey?: string },
): Promise<ProjectLLMSettings> {
  const client = kube(ctx)
  const studio = await readStudio(client)
  const spec: StudioLLMSpec = { ...(studio?.spec?.llm ?? {}) }
  const models = [...(spec.models ?? [])]
  const index = models.findIndex((model) => !model.archived && model.id === id)
  if (index < 0) throw new LLMRegistryError('selected model configuration was not found')

  const current = models[index]
  if (body.expectedRevisionID !== undefined && current.revisionID !== body.expectedRevisionID) {
    throw new LLMRegistryError('This model changed while you were editing it. Refresh and try again.')
  }
  if (body.expectedName !== undefined && current.name !== body.expectedName) {
    throw new LLMRegistryError('This model display name changed while you were editing it. Refresh and try again.')
  }
  const nextName = body.name?.trim() ?? ''
  const requestedConnectionRef = body.connectionRef === undefined ? current.connectionRef ?? '' : body.connectionRef.trim()
  const requestedModel = body.model === undefined ? current.model : body.model.trim()
  const requestedReasoningEffort = body.reasoningEffort === undefined ? current.reasoningEffort ?? '' : body.reasoningEffort.trim()
  const sameBindingSettings = requestedConnectionRef === (current.connectionRef ?? '')
    && requestedModel === current.model
    && requestedReasoningEffort === (current.reasoningEffort ?? '')
  const sameDirectProvider = current.connectionRef || (
    (body.provider === undefined ? current.provider ?? '' : body.provider.trim()) === (current.provider ?? '')
    && (body.baseURL === undefined ? current.baseURL ?? '' : body.baseURL.trim()).replace(/\/+$/, '')
      === (current.baseURL ?? '').replace(/\/+$/, '')
  )
  const nameOnlyEdit = Boolean(nextName && nextName !== current.name && !body.apiKey?.trim()
    && sameBindingSettings && sameDirectProvider)
  if (nameOnlyEdit) {
    // A display-label change does not alter inference identity. Keep the same
    // active revision and snapshot so runs pinned to it continue to resolve,
    // and avoid requiring provider metadata access for a label-only edit.
    models[index] = { ...current, name: nextName }
    spec.models = models
    await writeRegistry(client, spec, studio)
    return toSettings({ ...(studio as StudioObject), spec: { ...(studio?.spec ?? {}), llm: spec } }, [])
  }

  const archivedCurrent = current.connectionRef && !current.connectionSnapshot
    ? { ...current, connectionSnapshot: await connectionSnapshotFor(client, current.connectionRef) }
    : current
  const next: StudioLLMModel = {
    ...current,
    // A new revision, so an in-flight assistant turn pinned to the old one
    // keeps resolving to what it started with.
    revisionID: newRevisionID(),
    name: body.name?.trim() || current.name,
    model: body.model?.trim() || current.model,
    reasoningEffort: body.reasoningEffort === undefined ? current.reasoningEffort : body.reasoningEffort || undefined,
  }
  const targetConnectionRef = body.connectionRef?.trim() || current.connectionRef
  if (targetConnectionRef) {
    next.connectionRef = targetConnectionRef
    next.connectionSnapshot = await connectionSnapshotFor(
      client,
      targetConnectionRef,
      body.connectionInfo,
    )
    delete next.provider
    delete next.baseURL
    delete next.secretRef
  } else {
    next.provider = body.provider?.trim() ?? current.provider
    next.baseURL = body.baseURL?.trim() ?? current.baseURL
    next.secretRef = { name: credentialSecretName(id) }
  }
  models[index] = next
  // Retire the superseded revision rather than dropping it.
  models.push({ ...archivedCurrent, archived: true })
  spec.models = models

  if (!next.connectionRef && typeof body.apiKey === 'string' && body.apiKey.trim() !== '') {
    await writeCredential(client, id, body.apiKey)
  }
  await writeRegistry(client, spec, studio)
  return getLLMSettings(ctx)
}

export async function deleteLLMModel(ctx: RailgridContext | null, id: string): Promise<ProjectLLMSettings> {
  const client = kube(ctx)
  const studio = await readStudio(client)
  const spec: StudioLLMSpec = { ...(studio?.spec?.llm ?? {}) }
  // Keep history so runs pinned before this deletion still resolve. Retire
  // the active revision and capture its provider snapshot first.
  const revisions = [...(spec.models ?? [])]
  for (let index = 0; index < revisions.length; index += 1) {
    const model = revisions[index]
    if (model.id !== id || model.archived) continue
    revisions[index] = {
      ...model,
      ...(model.connectionRef && !model.connectionSnapshot
        ? { connectionSnapshot: await connectionSnapshotFor(client, model.connectionRef) }
        : {}),
      archived: true,
    }
  }
  spec.models = revisions
  if (spec.defaultModel === id) {
    spec.defaultModel = spec.models.find((model) => !model.archived)?.id
  }
  await writeRegistry(client, spec, studio)
  return getLLMSettings(ctx)
}

export async function setDefaultLLMModel(ctx: RailgridContext | null, id: string): Promise<ProjectLLMSettings> {
  const client = kube(ctx)
  const studio = await readStudio(client)
  const spec: StudioLLMSpec = { ...(studio?.spec?.llm ?? {}) }
  if (!(spec.models ?? []).some((model) => !model.archived && model.id === id)) {
    throw new LLMRegistryError('selected model configuration was not found')
  }
  spec.defaultModel = id
  await writeRegistry(client, spec, studio)
  return getLLMSettings(ctx)
}
