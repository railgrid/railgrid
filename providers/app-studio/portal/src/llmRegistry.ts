// The workspace's LLM model registry, read and written directly against kcp.
//
// The registry is `spec.llm` on the Studio — a typed CR field holding no
// credentials — and each model's API key is its own Secret named after the
// model. That split is what lets this module exist: listing models touches no
// Secret at all, and writing a key writes exactly one, so editing model A
// never requires holding model B's credential.
//
// Whether a model is usable is NOT decided here. The Studio reconciler looks
// at each credential Secret and publishes one boolean per model in
// `status.models[]`; this module reads that. No key material is ever fetched
// by the portal.

import { createKubeClient, isKubeNotFound, type KubeClient, type KubeObject, type KubeResourceRef } from './portalkit/kube'
import { providerFetch } from './portalkit/tenant'
import type { ProjectLLMSettings, ProjectLLMModelSettings, RailgridContext } from './types'
import catalogEntries from './generated/model-catalog.json'

// STUDIO_NAME matches aiv1alpha1.StudioName: one Studio per workspace, at a
// fixed name, because every project addresses the same one.
const STUDIO_NAME = 'studio'
const STUDIO_RESOURCE: KubeResourceRef = { group: 'ai.railgrid.ai', version: 'v1alpha1', resource: 'studios' }
const SECRET_RESOURCE: KubeResourceRef = { group: '', version: 'v1', resource: 'secrets', namespaced: true }
const SECRET_NAMESPACE = 'default'
const CREDENTIAL_KEY = 'apiKey'

// MAX_ACTIVE_MODELS and MAX_REVISIONS mirror the reconciler's bounds. Checking
// here too is not duplication of authority — the Studio condition remains the
// truth — it is so a person gets told before they save rather than after.
const MAX_ACTIVE_MODELS = 20
const MAX_REVISIONS = 200

export interface StudioLLMModel {
  id: string
  revisionID: string
  archived?: boolean
  name: string
  provider?: string
  baseURL?: string
  model: string
  secretRef?: { name: string }
}

export interface StudioLLMSpec {
  defaultModel?: string
  models?: StudioLLMModel[]
  runtime?: { maxRetries?: number; retryBackoffMS?: number; streamIdleTimeoutMS?: number }
}

interface StudioObject extends KubeObject {
  spec?: { llm?: StudioLLMSpec }
  status?: { models?: Array<{ id: string; configured?: boolean }> }
}

export class LLMRegistryError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'LLMRegistryError'
  }
}

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
// any provider prefix), match exactly, else take the LONGEST catalog id that
// is a prefix — so "gpt-4o-mini-2024" prefers "gpt-4o-mini" over "gpt-4o".
export function lookupCatalog(model: string): CatalogEntry | undefined {
  const normalized = model.toLowerCase().trim().split('/').pop() ?? ''
  if (!normalized) return undefined
  const exact = catalog.find((entry) => entry.id === normalized)
  if (exact) return exact
  let best: CatalogEntry | undefined
  for (const entry of catalog) {
    if (normalized.startsWith(entry.id) && (!best || entry.id.length > best.id.length)) best = entry
  }
  return best
}

function kube(ctx: RailgridContext | null): KubeClient {
  const cluster = ctx?.tenant?.trim() ?? ''
  if (!cluster) throw new LLMRegistryError('select an organization and workspace first')
  return createKubeClient({ fetch: providerFetch(ctx), cluster })
}

async function readStudio(client: KubeClient): Promise<StudioObject | null> {
  try {
    return await client.get<StudioObject>(STUDIO_RESOURCE, STUDIO_NAME)
  } catch (e) {
    // No Studio yet means no models yet, which is a normal empty state and
    // not something to show the user an error about.
    if (isKubeNotFound(e)) return null
    throw e
  }
}

function toSettings(studio: StudioObject | null): ProjectLLMSettings {
  const spec = studio?.spec?.llm ?? {}
  const configured = new Map<string, boolean>()
  for (const entry of studio?.status?.models ?? []) {
    if (entry?.id) configured.set(entry.id, !!entry.configured)
  }

  const models: ProjectLLMModelSettings[] = (spec.models ?? [])
    .filter((model) => model && !model.archived)
    .map((model) => ({
      catalog: lookupCatalog(model.model),
      id: model.id,
      name: model.name,
      provider: model.provider ?? '',
      baseURL: model.baseURL ?? '',
      model: model.model,
      // From the reconciler's observation, never from a key we hold.
      configured: configured.get(model.id) ?? false,
      default: model.id === spec.defaultModel,
    }))
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

// writeRegistry merge-patches spec.llm. The whole list goes in the patch
// because `models` is an atomic replacement from the client's point of view —
// it holds no secrets, so rewriting it costs nothing and avoids the
// list-merge-key subtleties of a partial update.
async function writeRegistry(client: KubeClient, spec: StudioLLMSpec): Promise<void> {
  const active = (spec.models ?? []).filter((model) => !model.archived)
  if (active.length > MAX_ACTIVE_MODELS) {
    throw new LLMRegistryError(`at most ${MAX_ACTIVE_MODELS} model configurations are supported`)
  }
  if ((spec.models ?? []).length > MAX_REVISIONS) {
    throw new LLMRegistryError('model configuration revision history is full')
  }
  await client.patch(STUDIO_RESOURCE, STUDIO_NAME, { spec: { llm: spec } }, { type: 'merge' })
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

async function deleteCredential(client: KubeClient, id: string): Promise<void> {
  try {
    await client.delete(SECRET_RESOURCE, credentialSecretName(id), { namespace: SECRET_NAMESPACE })
  } catch (e) {
    // A model whose key was never entered has no Secret to remove.
    if (!isKubeNotFound(e)) throw e
  }
}

export async function getLLMSettings(ctx: RailgridContext | null): Promise<ProjectLLMSettings> {
  return toSettings(await readStudio(kube(ctx)))
}

export async function createLLMModel(
  ctx: RailgridContext | null,
  body: { name: string; provider?: string; baseURL?: string; model: string; apiKey: string },
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

  models.push({
    id,
    revisionID: newRevisionID(),
    name,
    provider: body.provider?.trim() || undefined,
    baseURL: body.baseURL?.trim() || undefined,
    model: body.model.trim(),
    secretRef: { name: credentialSecretName(id) },
  })
  spec.models = models
  // First model configured becomes the default, so a workspace is usable
  // immediately after adding one.
  if (!spec.defaultModel) spec.defaultModel = id

  // Credential first: a model whose Secret does not exist yet is reported
  // SecretMissing, which is a worse state to leave behind than a Secret with
  // no model pointing at it.
  await writeCredential(client, id, body.apiKey)
  await writeRegistry(client, spec)
  return getLLMSettings(ctx)
}

export async function patchLLMModel(
  ctx: RailgridContext | null,
  id: string,
  body: { name?: string; provider?: string; baseURL?: string; model?: string; apiKey?: string },
): Promise<ProjectLLMSettings> {
  const client = kube(ctx)
  const studio = await readStudio(client)
  const spec: StudioLLMSpec = { ...(studio?.spec?.llm ?? {}) }
  const models = [...(spec.models ?? [])]
  const index = models.findIndex((model) => !model.archived && model.id === id)
  if (index < 0) throw new LLMRegistryError('selected model configuration was not found')

  const current = models[index]
  const next: StudioLLMModel = {
    ...current,
    // A new revision, so an in-flight assistant turn pinned to the old one
    // keeps resolving to what it started with.
    revisionID: newRevisionID(),
    name: body.name?.trim() || current.name,
    provider: body.provider?.trim() ?? current.provider,
    baseURL: body.baseURL?.trim() ?? current.baseURL,
    model: body.model?.trim() || current.model,
    secretRef: { name: credentialSecretName(id) },
  }
  models[index] = next
  // Retire the superseded revision rather than dropping it.
  models.push({ ...current, archived: true })
  spec.models = models

  if (typeof body.apiKey === 'string' && body.apiKey.trim() !== '') {
    await writeCredential(client, id, body.apiKey)
  }
  await writeRegistry(client, spec)
  return getLLMSettings(ctx)
}

export async function deleteLLMModel(ctx: RailgridContext | null, id: string): Promise<ProjectLLMSettings> {
  const client = kube(ctx)
  const studio = await readStudio(client)
  const spec: StudioLLMSpec = { ...(studio?.spec?.llm ?? {}) }
  // Every revision of this model goes, history included: the user asked for
  // the model to be gone, and leaving archived revisions behind would keep
  // its credential Secret referenced.
  spec.models = (spec.models ?? []).filter((model) => model.id !== id)
  if (spec.defaultModel === id) {
    spec.defaultModel = spec.models.find((model) => !model.archived)?.id
  }
  await writeRegistry(client, spec)
  await deleteCredential(client, id)
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
  await writeRegistry(client, spec)
  return getLLMSettings(ctx)
}
