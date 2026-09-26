import type {
  DevelopmentTemplate,
  ImportRepository,
  RailgridContext,
  ListResponse,
  Project,
  ProjectRestoreResult,
  ProjectAssistantRunMode,
  ProjectAssistantReviewTarget,
  ProjectAssistantRunStatus,
  ProjectAssistantApprovalMode,
  ProjectAssistantApprovalPreference,
  ProjectAssistantAttachmentReceipt,
  ProjectAssistantContextResource,
  ProjectAssistantContentPart,
  ProjectAssistantSkill,
  ProjectAssistantSkillDetail,
  ProjectAssistantSkillExport,
  ProjectAssistantSkillPackage,
  ProjectAssistantSkillResource,
  ProjectAssistantSkillsResponse,
  ProjectAssistantThread,
  ProjectAssistantThreadEvent,
  ProjectAssistantThreadItem,
  ProjectAssistantTurn,
  ProjectLLMSettings,
  ProjectLLMModelDiscovery,
  ProjectIntegration,
  ProjectProviderActionGrant,
  ProjectProviderResourceReference,
  ProjectCheckpoints,
  ProjectPromotionReadiness,
  ProjectRelease,
  ProjectReleasesResponse,
  ProjectPreviewAccess,
  ProjectPublishing,
  ProjectPublishingGrant,
  ProjectPublishingMember,
  ProjectPublishingMode,
  ProjectPromoteResult,
  ProviderItem,
  ProjectPlan,
  ProjectFileList,
  ProjectFileContent,
  ProjectFileWriteResult,
} from './types'
import type { ProjectCreateReadiness } from './createReadiness'
import type { PreviewBridgeSession } from './previewBridge'
import { providerFetch, readTenant, tenantHeaders } from './portalkit/tenant'
import { createKubeClient, isKubeNotFound, kubeVerbPath, type KubeObject, type KubeResourceRef } from './portalkit/kube'
import * as llmRegistry from './llmRegistry'
import { projectAssistantAttachmentReceipt } from './assistantAttachments'
import {
  classifyProjectFileError,
  projectFileErrorMessage,
  type ProjectFileErrorReason,
  type ProjectFileWriteIntent,
} from './projectFiles'

interface TenantSelection {
  orgUUID: string | null
  workspaceUUID: string | null
}

export class ProjectAPIInitializingError extends Error {
  constructor(message = 'App Studio is still initializing for this workspace. Try again shortly.') {
    super(message)
    this.name = 'ProjectAPIInitializingError'
  }
}

export class ProjectAPIRequestError extends Error {
  constructor(message: string, readonly status: number) {
    super(message)
    this.name = 'ProjectAPIRequestError'
  }
}

export class ProjectAssistantThreadPaginationError extends Error {
  constructor(message: string) {
    super(`Unable to load assistant threads: ${message}`)
    this.name = 'ProjectAssistantThreadPaginationError'
  }
}

export function isProjectAPIInitializingError(err: unknown): err is ProjectAPIInitializingError {
  return err instanceof ProjectAPIInitializingError
}

export function isProjectAPINotFoundError(err: unknown): err is ProjectAPIRequestError {
  return err instanceof ProjectAPIRequestError && err.status === 404
}

// tenantSelection reads the active org/workspace. Delegates to the shared,
// security-critical portalkit/tenant helper so the storage key + shape stay in
// lockstep with every other portal.
function tenantSelection(ctx: RailgridContext | null): TenantSelection {
  if (ctx && ('orgUUID' in ctx || 'workspaceUUID' in ctx)) {
    return { orgUUID: ctx.orgUUID ?? null, workspaceUUID: ctx.workspaceUUID ?? null }
  }
  return readTenant()
}

// Every call this module makes is a data-plane verb on a bound resource — a
// kcp custom subresource App Studio publishes on its APIExport — addressed on
// the hub's kcp front door like any other kube path:
//
//   /clusters/{cluster}/apis/ai.railgrid.ai/v1alpha1/projects/{name}/{verb}[/{tail}]
//   /clusters/{cluster}/apis/ai.railgrid.ai/v1alpha1/sessions/{thread}/{verb}[/{tail}]
//   /clusters/{cluster}/apis/ai.railgrid.ai/v1alpha1/studios/studio/{verb}
//
// kcp authenticates the caller (providerFetch injects the bearer), authorizes
// the HTTP method as the RBAC verb on {resource}/{verb}, and reverse-proxies
// the request to the provider with the caller's identity stamped. The cluster
// is in the PATH: it is what kcp authorizes against, so a request cannot
// address one workspace while claiming another. The hub-proxied
// /services/providers/app-studio/dataplane/… grammar these calls used to hit,
// like the /api/projects/* facade before it, is gone.
function verbCluster(ctx: RailgridContext | null): string {
  const t = tenantSelection(ctx)
  if (!t.orgUUID || !t.workspaceUUID) {
    throw new Error('select an organization and workspace first')
  }
  const cluster = ctx?.tenant?.trim() ?? ''
  if (!cluster) throw new Error('select an organization and workspace first')
  return cluster
}

const projectResource: KubeResourceRef = { group: 'ai.railgrid.ai', version: 'v1alpha1', resource: 'projects' }
const sessionResource: KubeResourceRef = { group: 'ai.railgrid.ai', version: 'v1alpha1', resource: 'sessions' }
const studioResource: KubeResourceRef = { group: 'ai.railgrid.ai', version: 'v1alpha1', resource: 'studios' }

// projectURL addresses a verb on one project. tail is the part of the address
// INSIDE the object — an integration alias, a grant id, a skill package —
// which stays out of the verb so one grant covers the set. Callers that need
// a query string append it: a verb path carries none of its own.
function projectURL(ctx: RailgridContext | null, name: string, verb: string, ...tail: string[]): string {
  return kubeVerbPath(verbCluster(ctx), projectResource, name, verb, { tail: tail.join('/') })
}

// sessionURL addresses a verb on one assistant conversation. The Session is
// named after the thread, and the provider reads the project off it — which is
// why no project travels here any more.
function sessionURL(ctx: RailgridContext | null, threadID: string, verb: string, ...tail: string[]): string {
  return kubeVerbPath(verbCluster(ctx), sessionResource, threadID, verb, { tail: tail.join('/') })
}

// studioURL addresses a workspace-wide verb. The Studio is the per-workspace
// singleton, so "create a project here" has an object to be authorized
// against instead of being a collection route.
function studioURL(ctx: RailgridContext | null, verb: string): string {
  return kubeVerbPath(verbCluster(ctx), studioResource, STUDIO_NAME, verb)
}

// STUDIO_NAME mirrors aiv1alpha1.StudioName: one Studio per workspace.
const STUDIO_NAME = 'studio'

// ProjectCR is the bound kind as the API server serves it. The list view needs
// only what the CR itself carries; anything joined — live instance status, the
// commit ledger, the source-revision fence — comes from the `view` verb, which
// is exactly why that verb exists.
interface ProjectCR extends KubeObject {
  spec?: { displayName?: string; description?: string; template?: { name?: string }; sharing?: Project['sharing'] }
  status?: { phase?: string; updatedAt?: string }
}

function projectFromCR(object: ProjectCR): Project {
  return {
    name: object.metadata.name,
    uid: object.metadata.uid,
    displayName: object.spec?.displayName ?? object.metadata.name,
    description: object.spec?.description,
    phase: object.status?.phase,
    deleting: Boolean(object.metadata.deletionTimestamp),
    template: object.spec?.template?.name,
    sharing: object.spec?.sharing,
    createdAt: object.metadata.creationTimestamp ?? '',
    updatedAt: object.status?.updatedAt,
  }
}

// ensureStudio creates the workspace's Studio when it is missing.
//
// A workspace-wide verb is authorized against the Studio, and gate 1 is a real
// GET — so in a workspace that has never created a project there is nothing to
// authorize against and the call would 404. Creating the bound CR first is the
// Pillar 1 answer: the API server validates it against the CRD and the
// caller's own membership, and the provider's reconciler fills in the service
// references afterwards. Already-exists is success.
//
// Every Studio verb goes through studioRequest below, so this runs before the
// FIRST such call of a session — including the create-readiness probe the
// "new project" page makes before anything else. It once ran only before
// create-project, and a fresh workspace 404'd on readiness. Once a Studio has
// been seen in a workspace the check is not repeated for that tenant.
const studioEnsured = new Map<string, Promise<void>>()

async function ensureStudio(ctx: RailgridContext | null): Promise<void> {
  const key = ctx ? `${ctx.orgUUID}/${ctx.workspaceUUID}` : ''
  const pending = studioEnsured.get(key)
  if (pending) return pending
  const attempt = (async () => {
    const client = projectKubeClient(ctx)
    try {
      await client.get(studioResource, STUDIO_NAME)
      return
    } catch {
      // fall through to create
    }
    try {
      await client.create(studioResource, {
        apiVersion: 'ai.railgrid.ai/v1alpha1',
        kind: 'Studio',
        metadata: { name: STUDIO_NAME },
        spec: { search: { size: 'small' }, browser: { size: 'small' } },
      })
    } catch {
      // A concurrent create, or a workspace where the binding has not caught up
      // yet: the verb below reports the real reason, and the next call retries.
      studioEnsured.delete(key)
    }
  })()
  studioEnsured.set(key, attempt)
  return attempt
}

// studioRequest is request() for a Studio verb: it makes sure the singleton
// exists first, because gate 1 is a real GET of it.
async function studioRequest<T>(ctx: RailgridContext | null, method: string, verb: string, body?: unknown): Promise<T> {
  await ensureStudio(ctx)
  return request<T>(ctx, method, studioURL(ctx, verb), body)
}

// Project is a kcp CR on the workspace cluster, so a write to its spec is a
// write to the API server — through the hub's kcp proxy, authorized against
// the caller's workspace membership, validated by the CRD. The backend's
// PATCH /api/projects/{p} facade re-implemented that with weaker validation
// than the schema it was writing to, and is gone.
function projectKubeClient(ctx: RailgridContext | null) {
  const cluster = ctx?.tenant?.trim() ?? ''
  if (!cluster) throw new Error('select an organization and workspace first')
  // The host-owned transport injects Authorization; this module never handles
  // the token. Same wiring the assistant resource pickers already use.
  return createKubeClient({ fetch: providerFetch(ctx), cluster })
}

interface ProjectAPIRequestOptions {
  timeoutMS?: number
  timeoutMessage?: string
}

const ASSISTANT_THREAD_PAGE_SIZE = 500
const MAX_ASSISTANT_THREAD_PAGES = 100
const ASSISTANT_THREAD_ITEM_PAGE_TURNS = 20
const PREVIEW_BRIDGE_TIMEOUT_MESSAGE = 'preview bridge request timed out'

export interface ProjectAssistantThreadItemPage {
  items: ProjectAssistantThreadItem[]
  nextCursor: string
}

async function request<T>(ctx: RailgridContext | null, method: string, path: string, body?: unknown, options: ProjectAPIRequestOptions = {}): Promise<T> {
  const headers = tenantHeaders({ json: body !== undefined })
  const controller = options.timeoutMS ? new AbortController() : null
  let timedOut = false
  const timeout = controller ? window.setTimeout(() => {
    timedOut = true
    controller.abort()
  }, options.timeoutMS) : undefined
  let res: Response
  let text: string
  try {
    res = await providerFetch(ctx)(path, {
      method,
      credentials: 'same-origin',
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined,
      signal: controller?.signal,
    })
    text = await res.text()
  } catch (error) {
    if (timedOut) throw new ProjectAPIRequestError(options.timeoutMessage || 'request timed out', 408)
    throw error
  } finally {
    if (timeout !== undefined) window.clearTimeout(timeout)
  }
  if (!res.ok) {
    const fallback = text || res.statusText
    let detail = fallback
    let reason = ''
    try {
      const parsed = JSON.parse(text) as { message?: string; reason?: string }
      if (parsed.message) detail = parsed.message
      if (parsed.reason) reason = parsed.reason
    } catch {
      // keep raw text
    }
    if (isProjectAPIInitializingResponse(res.status, reason, detail)) {
      throw new ProjectAPIInitializingError(detail)
    }
    throw new ProjectAPIRequestError(detail, res.status)
  }
  return (text ? JSON.parse(text) : null) as T
}

async function requestBlob(ctx: RailgridContext | null, path: string, signal?: AbortSignal): Promise<Blob> {
  const res = await providerFetch(ctx)(path, {
    method: 'GET',
    credentials: 'same-origin',
    headers: tenantHeaders({}),
    cache: 'no-cache',
    signal,
  })
  if (!res.ok) {
    throw new ProjectAPIRequestError(res.statusText || 'project thumbnail is unavailable', res.status)
  }
  return res.blob()
}

async function requestAssistantAttachmentUpload(
  ctx: RailgridContext | null,
  path: string,
  file: File,
  signal?: AbortSignal,
  clientAttachmentID?: string,
): Promise<ProjectAssistantAttachmentReceipt> {
  const form = new FormData()
  form.append('file', file, file.name || 'attachment')
  if (clientAttachmentID?.trim()) form.append('clientAttachmentID', clientAttachmentID.trim())
  const headers = tenantHeaders({})
  // The server promotes this provisional receipt atomically when the turn is
  // accepted; abandoned drafts are bounded by the provider retention policy.
  headers['X-Railgrid-Attachment-Draft'] = 'true'
  const res = await providerFetch(ctx)(path, {
    method: 'POST',
    credentials: 'same-origin',
    headers,
    body: form,
    signal,
  })
  const text = await res.text()
  if (!res.ok) {
    const fallback = text || res.statusText
    let detail = fallback
    let reason = ''
    try {
      const parsed = JSON.parse(text) as { message?: string; reason?: string }
      if (parsed.message) detail = parsed.message
      if (parsed.reason) reason = parsed.reason
    } catch {
      // keep raw text
    }
    if (isProjectAPIInitializingResponse(res.status, reason, detail)) throw new ProjectAPIInitializingError(detail)
    throw new ProjectAPIRequestError(detail, res.status)
  }
  let value: unknown
  try {
    value = text ? JSON.parse(text) : null
  } catch {
    throw new ProjectAPIRequestError('attachment upload returned invalid JSON', 502)
  }
  const receipt = projectAssistantAttachmentReceipt(
    value && typeof value === 'object' && !Array.isArray(value) && 'attachment' in value
      ? (value as { attachment?: unknown }).attachment
      : value,
  )
  if (!receipt) throw new ProjectAPIRequestError('attachment upload returned an invalid receipt', 502)
  return receipt
}

/** A workspace file request failed; reason drives recovery (overwrite, refresh). */
export class ProjectFileRequestError extends ProjectAPIRequestError {
  constructor(message: string, status: number, readonly reason: ProjectFileErrorReason, readonly detail: string) {
    super(message, status)
    this.name = 'ProjectFileRequestError'
  }
}

export function isProjectFileRequestError(err: unknown): err is ProjectFileRequestError {
  return err instanceof ProjectFileRequestError
}

function projectFileContentURL(ctx: RailgridContext | null, name: string, path: string): string {
  return `${projectURL(ctx, name, 'files-content')}?path=${encodeURIComponent(path)}`
}

async function projectFileResponseError(res: Response, intent: ProjectFileWriteIntent): Promise<Error> {
  let text = ''
  try {
    text = await res.text()
  } catch {
    // keep the status text
  }
  let detail = text || res.statusText
  let reason = ''
  try {
    const parsed = JSON.parse(text) as { message?: string; reason?: string }
    if (parsed.message) detail = parsed.message
    if (parsed.reason) reason = parsed.reason
  } catch {
    // keep raw text
  }
  if (isProjectAPIInitializingResponse(res.status, reason, detail)) return new ProjectAPIInitializingError(detail)
  const fileReason = classifyProjectFileError(res.status, detail, intent)
  return new ProjectFileRequestError(projectFileErrorMessage(fileReason, detail), res.status, fileReason, detail)
}

function projectFileWriteResult(text: string, path: string): ProjectFileWriteResult {
  try {
    const value = text ? JSON.parse(text) as Partial<ProjectFileWriteResult> : {}
    return {
      path: typeof value.path === 'string' ? value.path : path,
      size: typeof value.size === 'number' ? value.size : 0,
      ...(typeof value.version === 'string' ? { version: value.version } : {}),
      ...(typeof value.binary === 'boolean' ? { binary: value.binary } : {}),
    }
  } catch {
    return { path, size: 0 }
  }
}

function assistantAttachmentReceipts(value: unknown): ProjectAssistantAttachmentReceipt[] {
  const candidates = Array.isArray(value)
    ? value
    : value && typeof value === 'object'
    ? ((value as { attachments?: unknown; items?: unknown }).attachments ?? (value as { items?: unknown }).items)
    : []
  if (!Array.isArray(candidates)) return []
  return candidates.map(projectAssistantAttachmentReceipt).filter((receipt): receipt is ProjectAssistantAttachmentReceipt => receipt !== null)
}

function isProjectAPIInitializingResponse(status: number, reason: string, message: string): boolean {
  const normalized = message.toLowerCase()
  return (
    (status === 503 && reason === 'ServiceUnavailable' && normalized.includes('app studio')) ||
    normalized.includes('server could not find the requested resource')
  )
}

async function requestAssistantThreadEventStream(
  ctx: RailgridContext | null,
  _name: string,
  threadID: string,
  afterSequence: number,
  onEvent: (event: ProjectAssistantThreadEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const headers = tenantHeaders({})
  headers.Accept = 'text/event-stream'
  headers['Last-Event-ID'] = String(afterSequence)
  const res = await providerFetch(ctx)(`${sessionURL(ctx, threadID, 'events')}?afterSequence=${encodeURIComponent(String(afterSequence))}`, {
    credentials: 'same-origin', headers, signal,
  })
  if (!res.ok) throw new Error(`assistant thread stream failed: ${res.status} ${res.statusText}`)
  if (!res.body) throw new Error('missing assistant thread stream body')
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  const flush = (raw: string) => {
    let data = ''
    for (const line of raw.split('\n')) {
      if (line.startsWith('data:')) data = data ? `${data}\n${line.slice(5).trimStart()}` : line.slice(5).trimStart()
    }
    if (data) onEvent(JSON.parse(data) as ProjectAssistantThreadEvent)
  }
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      for (;;) {
        const separator = buffer.indexOf('\n\n')
        if (separator < 0) break
        flush(buffer.slice(0, separator))
        buffer = buffer.slice(separator + 2)
      }
    }
  } finally { reader.releaseLock() }
}

function normalizeAssistantSkill(value: ProjectAssistantSkill): ProjectAssistantSkill {
  const raw = (value && typeof value === 'object' ? value : {}) as ProjectAssistantSkill & Record<string, unknown>
  const scope = typeof raw.scope === 'string' ? raw.scope : typeof raw.source === 'string' ? raw.source : ''
  const packageName = typeof raw.packageName === 'string'
    ? raw.packageName
    : typeof raw.packagePath === 'string'
      ? raw.packagePath
      : typeof raw.id === 'string' && raw.id.includes(':')
        ? raw.id.slice(raw.id.indexOf(':') + 1)
        : ''
  const id = typeof raw.id === 'string' && raw.id.trim()
    ? raw.id
    : `${scope || 'project'}:${packageName}`
  const digest = typeof raw.digest === 'string'
    ? raw.digest
    : typeof raw.sha256 === 'string'
      ? raw.sha256
      : typeof raw.contentDigest === 'string'
        ? raw.contentDigest
        : ''
  const contentDigest = typeof raw.contentDigest === 'string' ? raw.contentDigest : digest
  const resources = Array.isArray(raw.resources)
    ? raw.resources
      .filter((resource) => !!resource && typeof resource === 'object')
      .map((resource) => {
        const item = resource as ProjectAssistantSkillResource & Record<string, unknown>
        return {
          path: typeof item.path === 'string' ? item.path : '',
          ...(typeof item.size === 'number' ? { size: item.size } : {}),
          ...(typeof item.digest === 'string' ? { digest: item.digest } : {}),
        }
      })
      .filter((resource) => resource.path)
    : undefined
  return {
    id,
    name: typeof raw.name === 'string' ? raw.name : packageName || id,
    description: typeof raw.description === 'string' ? raw.description : '',
    scope,
    ...(typeof raw.enabled === 'boolean' ? { enabled: raw.enabled } : {}),
    ...(typeof raw.editable === 'boolean' ? { editable: raw.editable } : { editable: scope === 'project' }),
    ...(packageName ? { packageName } : {}),
    ...(typeof raw.version === 'string' ? { version: raw.version } : typeof raw.packageVersion === 'string' ? { version: raw.packageVersion } : {}),
    ...(digest ? { digest } : {}),
    ...(contentDigest ? { contentDigest } : {}),
    ...(resources?.length ? { resources } : {}),
    ...(typeof raw.status === 'string' ? { status: raw.status } : {}),
  }
}

function normalizeAssistantSkillDetail(value: ProjectAssistantSkillDetail | ProjectAssistantSkill): ProjectAssistantSkillDetail {
  const raw = (value && typeof value === 'object' ? value : {}) as ProjectAssistantSkillDetail & Record<string, unknown>
  const normalized = normalizeAssistantSkill(raw)
  const instructions = typeof raw.instructions === 'string'
    ? raw.instructions
    : typeof raw.content === 'string'
      ? raw.content
      : typeof raw.body === 'string'
        ? raw.body
        : typeof raw.authorInstructions === 'string'
          ? raw.authorInstructions
          : ''
  const resources = Array.isArray(raw.resources)
    ? raw.resources
      .filter((resource) => !!resource && typeof resource === 'object')
      .map((resource) => {
        const item = resource as ProjectAssistantSkillResource & Record<string, unknown>
        return {
          path: typeof item.path === 'string' ? item.path : '',
          ...(typeof item.size === 'number' ? { size: item.size } : {}),
          ...(typeof item.digest === 'string' ? { digest: item.digest } : {}),
          ...(typeof item.content === 'string' ? { content: item.content } : {}),
        }
      })
      .filter((resource) => resource.path)
    : undefined
  return {
    ...normalized,
    ...(instructions ? { instructions } : {}),
    ...(resources?.length ? { resources } : {}),
    ...(typeof raw.content === 'string' ? { content: raw.content } : {}),
    ...(typeof raw.authorInstructions === 'string' ? { authorInstructions: raw.authorInstructions } : {}),
  }
}

function normalizeAssistantSkillPackage(value: ProjectAssistantSkillPackage): ProjectAssistantSkillPackage {
  return {
    packageName: value.packageName.trim(),
    name: value.name.trim(),
    description: value.description.trim(),
    instructions: value.instructions,
    resources: (value.resources ?? [])
      .filter((resource) => resource && typeof resource.path === 'string')
      .map((resource) => ({ path: resource.path.trim(), content: resource.content ?? '' }))
      .filter((resource) => resource.path),
  }
}

function normalizeAssistantSkillExport(value: Record<string, unknown>): ProjectAssistantSkillExport {
  const nested = value.package && typeof value.package === 'object' ? value.package : null
  const packageValue = nested ?? value
  const packageData = normalizeExportPackage(packageValue, nested ? undefined : value)
  return {
    ...(typeof value.filename === 'string' ? { filename: value.filename } : {}),
    ...(typeof value.content === 'string' ? { content: value.content } : {}),
    ...(packageData ? { package: packageData } : {}),
  }
}

function normalizeExportPackage(value: unknown, fallback?: Record<string, unknown>): ProjectAssistantSkillPackage | undefined {
  if (!value || typeof value !== 'object') return undefined
  const raw = value as Record<string, unknown>
  const packageName = typeof raw.packageName === 'string'
    ? raw.packageName
    : typeof fallback?.packageName === 'string'
      ? fallback.packageName
      : ''
  const name = typeof raw.name === 'string' ? raw.name : typeof fallback?.name === 'string' ? fallback.name : packageName
  const description = typeof raw.description === 'string' ? raw.description : typeof fallback?.description === 'string' ? fallback.description : ''
  const instructions = typeof raw.instructions === 'string'
    ? raw.instructions
    : typeof raw.content === 'string'
      ? raw.content
      : typeof fallback?.instructions === 'string'
        ? fallback.instructions
        : ''
  const resourcesValue = Array.isArray(raw.resources)
    ? raw.resources
    : Array.isArray(raw.files)
      ? raw.files
      : Array.isArray(fallback?.files)
        ? fallback.files
        : []
  const resources = resourcesValue
    .filter((resource) => !!resource && typeof resource === 'object')
    .map((resource) => {
      const item = resource as Record<string, unknown>
      return {
        path: typeof item.path === 'string' ? item.path.trim() : '',
        content: typeof item.content === 'string' ? item.content : '',
      }
    })
    .filter((resource) => resource.path)
  if (!packageName && !name && !description && !instructions && resources.length === 0) return undefined
  return normalizeAssistantSkillPackage({ packageName, name, description, instructions, resources })
}

export const api = {
  async listProviders(ctx: RailgridContext | null): Promise<ProviderItem[]> {
    const body = await request<ListResponse<ProviderItem>>(ctx, 'GET', '/api/providers')
    return body.items ?? []
  },

  // Projects are bound CRs, so listing them is a kube read through the hub's
  // kcp proxy — authorized against the caller's workspace membership by the
  // API server (Pillar 1). There is no list verb, and the provider serves no
  // collection route for it.
  async listProjects(ctx: RailgridContext | null): Promise<Project[]> {
    const list = await projectKubeClient(ctx).list<ProjectCR>(projectResource)
    return (list.items ?? []).map(projectFromCR)
  },

  async connectProjectRepository(ctx: RailgridContext | null, project: string, connectionRef: string, retry?: { retryRepositoryRef: string; projectUID: string }): Promise<Project> {
    return request<Project>(ctx, 'POST', `${projectURL(ctx, project, 'set-repository')}`, { connectionRef, ...retry })
  },

  async createProject(
    ctx: RailgridContext | null,
    body: {
      displayName?: string
      description?: string
      prompt?: string
      templateName?: string
      inferDevelopmentTemplate?: boolean
      connectionRef?: string
      repositoryMode?: 'auto' | 'none' | 'create'
      existingRepositoryRef?: string
    },
  ): Promise<Project> {
    return studioRequest<Project>(ctx, 'POST', 'create-project', body)
  },

  // createProjectStream creates a project over SSE, surfacing each creation
  // step (Planning, Configuring repository, Attaching scaffold to <template>,
  // …) via onStatus, and resolves with the created Project. Same inputs as
  // createProject; the caller starts the first assistant turn afterward.
  async createProjectStream(
    ctx: RailgridContext | null,
    body: {
      displayName?: string
      description?: string
      prompt?: string
      templateName?: string
      inferDevelopmentTemplate?: boolean
      connectionRef?: string
      repositoryMode?: 'auto' | 'none' | 'create'
      existingRepositoryRef?: string
    },
    onStatus: (message: string) => void,
    signal?: AbortSignal,
  ): Promise<Project> {
    const headers = tenantHeaders({})
    headers.Accept = 'text/event-stream'
    headers['Content-Type'] = 'application/json'
    await ensureStudio(ctx)
    const res = await providerFetch(ctx)(`${studioURL(ctx, 'create-project-stream')}`, {
      method: 'POST',
      credentials: 'same-origin',
      headers,
      body: JSON.stringify(body),
      signal,
    })
    if (!res.ok || !res.body) throw new Error(`project create stream failed: ${res.status} ${res.statusText}`)
    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''
    let created: Project | null = null
    let failure: string | null = null
    const handle = (raw: string) => {
      let event = 'message'
      let data = ''
      for (const line of raw.split('\n')) {
        if (line.startsWith('event:')) event = line.slice(6).trim()
        else if (line.startsWith('data:')) data = data ? `${data}\n${line.slice(5).trimStart()}` : line.slice(5).trimStart()
      }
      if (!data) return
      if (event === 'status') {
        const parsed = JSON.parse(data) as { message?: string }
        if (parsed.message) onStatus(parsed.message)
      } else if (event === 'created') {
        created = JSON.parse(data) as Project
      } else if (event === 'error') {
        failure = (JSON.parse(data) as { message?: string }).message ?? 'project creation failed'
      }
    }
    try {
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        for (;;) {
          const sep = buffer.indexOf('\n\n')
          if (sep < 0) break
          handle(buffer.slice(0, sep))
          buffer = buffer.slice(sep + 2)
        }
      }
    } finally {
      reader.releaseLock()
    }
    if (failure) throw new Error(failure)
    if (!created) throw new Error('project creation stream ended without a project')
    return created
  },

  // planProject returns a creation blueprint (proposed name, recommended
  // template, whether starter code will be attached) WITHOUT creating —
  // the wizard's confirm step. See ProjectPlan in the backend.
  async planProject(
    ctx: RailgridContext | null,
    body: { prompt?: string; templateName?: string },
  ): Promise<ProjectPlan> {
    return studioRequest<ProjectPlan>(ctx, 'POST', 'plan', body)
  },

  // reseedScaffold re-attaches the template's starter code to an empty
  // workspace (retry after a failed seed, or seed a legacy project).
  async reseedScaffold(
    ctx: RailgridContext | null,
    name: string,
  ): Promise<{ template: string; scaffold: { repository: string; ref?: string }; seeded: number }> {
    return request(ctx, 'POST', `${projectURL(ctx, name, 'scaffold')}`, {})
  },

  // listProjectFiles returns the live dev workspace file tree (flat, sorted
  // paths with sizes) for the code explorer.
  async listProjectFiles(ctx: RailgridContext | null, name: string): Promise<ProjectFileList> {
    return request<ProjectFileList>(ctx, 'GET', `${projectURL(ctx, name, 'files')}`)
  },

  // readProjectFile returns one workspace file's bounded content plus its full
  // size and version. Binary files return empty content with binary=true.
  async readProjectFile(ctx: RailgridContext | null, name: string, path: string): Promise<ProjectFileContent> {
    const body = await request<ProjectFileContent>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'files-content')}?path=${encodeURIComponent(path)}`,
    )
    return { ...body, size: typeof body?.size === 'number' ? body.size : new TextEncoder().encode(body?.content ?? '').byteLength }
  },

  // rawProjectFileURL is the files/raw address. Auth is header-based, so this
  // URL is only for providerFetch — never an <img src> or <a href>; use
  // fetchProjectFileRaw and an object URL instead.
  rawProjectFileURL(ctx: RailgridContext | null, name: string, path: string, options: { download?: boolean } = {}): string {
    const query = new URLSearchParams({ path })
    if (options.download) query.set('download', '1')
    return `${projectURL(ctx, name, 'files-raw')}?${query}`
  },

  // fetchProjectFileRaw returns a workspace file's raw bytes for previews and
  // downloads.
  async fetchProjectFileRaw(
    ctx: RailgridContext | null,
    name: string,
    path: string,
    options: { download?: boolean; signal?: AbortSignal } = {},
  ): Promise<Blob> {
    const res = await providerFetch(ctx)(api.rawProjectFileURL(ctx, name, path, options), {
      method: 'GET',
      credentials: 'same-origin',
      headers: { ...tenantHeaders({}), Accept: '*/*' },
      cache: 'no-cache',
      signal: options.signal,
    })
    if (!res.ok) throw await projectFileResponseError(res, {})
    return res.blob()
  },

  // putProjectFile creates or replaces one workspace file with a raw body.
  // createOnly sends If-None-Match: * (412 when the path exists); ifMatch
  // replaces only an unchanged version (412 when it changed).
  async putProjectFile(
    ctx: RailgridContext | null,
    name: string,
    path: string,
    body: Blob | string,
    options: { ifMatch?: string; createOnly?: boolean } = {},
  ): Promise<ProjectFileWriteResult> {
    const headers = tenantHeaders({})
    headers['Content-Type'] = typeof body === 'string'
      ? 'text/plain; charset=utf-8'
      : body.type || 'application/octet-stream'
    if (options.createOnly) headers['If-None-Match'] = '*'
    else if (options.ifMatch) headers['If-Match'] = options.ifMatch
    const res = await providerFetch(ctx)(projectFileContentURL(ctx, name, path), {
      method: 'PUT',
      credentials: 'same-origin',
      headers,
      body,
    })
    if (!res.ok) throw await projectFileResponseError(res, options)
    return projectFileWriteResult(await res.text(), path)
  },

  // deleteProjectFile removes one workspace file (204); ifMatch guards against
  // deleting a file that changed since it was loaded.
  async deleteProjectFile(ctx: RailgridContext | null, name: string, path: string, options: { ifMatch?: string } = {}): Promise<void> {
    const headers = tenantHeaders({})
    if (options.ifMatch) headers['If-Match'] = options.ifMatch
    const res = await providerFetch(ctx)(projectFileContentURL(ctx, name, path), {
      method: 'DELETE',
      credentials: 'same-origin',
      headers,
    })
    if (!res.ok) throw await projectFileResponseError(res, options)
  },

  // uploadProjectFiles sends one multipart request with a `file` part per file
  // into dir ("" = workspace root). Without overwrite an existing path fails
  // the request (reason "exists").
  async uploadProjectFiles(
    ctx: RailgridContext | null,
    name: string,
    files: File[],
    options: { dir?: string; overwrite?: boolean; signal?: AbortSignal } = {},
  ): Promise<ProjectFileWriteResult[]> {
    const form = new FormData()
    for (const file of files) form.append('file', file, file.name || 'upload')
    form.append('dir', options.dir ?? '')
    if (options.overwrite) form.append('overwrite', 'true')
    const res = await providerFetch(ctx)(`${projectURL(ctx, name, 'files-upload')}`, {
      method: 'POST',
      credentials: 'same-origin',
      headers: tenantHeaders({}),
      body: form,
      signal: options.signal,
    })
    if (!res.ok) throw await projectFileResponseError(res, { upload: !options.overwrite })
    const text = await res.text()
    let value: unknown
    try {
      value = text ? JSON.parse(text) : null
    } catch {
      throw new ProjectAPIRequestError('file upload returned invalid JSON', 502)
    }
    const entries = value && typeof value === 'object' && Array.isArray((value as { files?: unknown }).files)
      ? (value as { files: unknown[] }).files
      : []
    return entries
      .filter((entry): entry is ProjectFileWriteResult => !!entry && typeof entry === 'object' && typeof (entry as { path?: unknown }).path === 'string')
      .map((entry) => ({ ...entry, size: typeof entry.size === 'number' ? entry.size : 0 }))
  },

  async listDevelopmentTemplates(ctx: RailgridContext | null): Promise<DevelopmentTemplate[]> {
    const body = await studioRequest<{ templates: DevelopmentTemplate[] }>(ctx, 'GET', 'development-templates')
    return body.templates ?? []
  },

  async listImportRepositories(ctx: RailgridContext | null): Promise<ImportRepository[]> {
    const body = await studioRequest<{ repositories: ImportRepository[] }>(ctx, 'GET', 'import-repositories')
    return body.repositories ?? []
  },

  async setProjectTemplate(
    ctx: RailgridContext | null,
    name: string,
    template: string,
  ): Promise<{ template: string; components: Record<string, string> }> {
    return request<{ template: string; components: Record<string, string> }>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'set-template')}`,
      { template },
    )
  },

  async restoreWorkspace(ctx: RailgridContext | null, name: string, commitSHA: string, expectedSourceRevision: number): Promise<ProjectRestoreResult> {
    return request<ProjectRestoreResult>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'restore-workspace')}`,
      { commitSHA, expectedSourceRevision },
    )
  },

  async getPromotion(ctx: RailgridContext | null, name: string): Promise<ProjectPromotionReadiness> {
    return request<ProjectPromotionReadiness>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'promotion')}`,
    )
  },

  async listReleases(ctx: RailgridContext | null, name: string): Promise<ProjectRelease[]> {
    const body = await request<ProjectReleasesResponse>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'releases')}`,
    )
    return body.items ?? []
  },

  async getCheckpoints(ctx: RailgridContext | null, name: string): Promise<ProjectCheckpoints> {
    return request<ProjectCheckpoints>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'checkpoints')}`,
    )
  },

  async promoteProject(
    ctx: RailgridContext | null,
    name: string,
    values?: Record<string, unknown>,
    commitSHA?: string,
    releaseID?: string,
  ): Promise<ProjectPromoteResult> {
    const body: { values?: Record<string, unknown>; commitSHA?: string; releaseID?: string } = {}
    if (values) body.values = values
    if (commitSHA?.trim()) body.commitSHA = commitSHA.trim()
    if (releaseID?.trim()) body.releaseID = releaseID.trim()
    return request<ProjectPromoteResult>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'promote')}`,
      body,
    )
  },

  // Preview visibility is the development-side counterpart of publishing: the
  // same two modes on the dev URL, defaulting to restricted.
  async getPreviewAccess(ctx: RailgridContext | null, name: string): Promise<ProjectPreviewAccess> {
    return request<ProjectPreviewAccess>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'preview')}`,
    )
  },

  // The development-preview view speaks 'private' | 'public'; the verb
  // normalizes 'private' to 'restricted' (requestedPreviewMode in
  // api/project_preview_access.go), so both vocabularies are accepted here.
  async setPreviewAccess(
    ctx: RailgridContext | null,
    name: string,
    mode: ProjectPublishingMode | 'private',
  ): Promise<ProjectPreviewAccess> {
    return request<ProjectPreviewAccess>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'preview')}`,
      { mode },
    )
  },

  async listPreviewGrants(ctx: RailgridContext | null, name: string): Promise<ProjectPublishingGrant[]> {
    const res = await request<{ items?: ProjectPublishingGrant[] }>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'preview-grants')}`,
    )
    return res.items ?? []
  },

  async createPreviewGrant(
    ctx: RailgridContext | null,
    name: string,
    user: string,
    invite = false,
  ): Promise<ProjectPublishingGrant[]> {
    const res = await request<{ items?: ProjectPublishingGrant[] }>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'preview-grants')}`,
      { user, invite },
    )
    return res.items ?? []
  },

  async revokePreviewGrant(
    ctx: RailgridContext | null,
    name: string,
    grant: string,
  ): Promise<ProjectPublishingGrant[]> {
    const res = await request<{ items?: ProjectPublishingGrant[] }>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'preview-grants', grant)}`,
    )
    return res.items ?? []
  },

  async getPublishing(ctx: RailgridContext | null, name: string): Promise<ProjectPublishing> {
    return request<ProjectPublishing>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'publishing')}`,
    )
  },

  async publishProject(
    ctx: RailgridContext | null,
    name: string,
    mode: ProjectPublishingMode,
  ): Promise<ProjectPublishing> {
    return request<ProjectPublishing>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'publishing')}`,
      { mode },
    )
  },

  async unpublishProject(ctx: RailgridContext | null, name: string): Promise<ProjectPublishing> {
    return request<ProjectPublishing>(
      ctx,
      'DELETE',
      `${projectURL(ctx, name, 'publishing')}`,
    )
  },

  async listPublishingMembers(ctx: RailgridContext | null, name: string): Promise<ProjectPublishingMember[]> {
    const body = await request<{ items?: ProjectPublishingMember[] }>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'publishing-members')}`,
    )
    return body.items ?? []
  },

  async listPublishingGrants(ctx: RailgridContext | null, name: string): Promise<ProjectPublishingGrant[]> {
    const body = await request<{ items?: ProjectPublishingGrant[] }>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'publishing-grants')}`,
    )
    return body.items ?? []
  },

  async grantPublishingAccess(
    ctx: RailgridContext | null,
    name: string,
    user: string,
    invite = false,
  ): Promise<{ items?: ProjectPublishingGrant[] }> {
    // invite=true lets `user` be an email of someone not on the platform
    // yet: the hub pre-provisions their account and org membership, and the
    // grant applies the moment they first sign in.
    return request<{ items?: ProjectPublishingGrant[] }>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'publishing-grants')}`,
      invite ? { user, invite: true } : { user },
    )
  },

  async revokePublishingAccess(ctx: RailgridContext | null, name: string, grant: string): Promise<ProjectPublishingGrant> {
    return request<ProjectPublishingGrant>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'publishing-grants', grant)}`,
    )
  },

  async getProjectCreateReadiness(ctx: RailgridContext | null): Promise<ProjectCreateReadiness> {
    return studioRequest<ProjectCreateReadiness>(ctx, 'GET', 'create-readiness')
  },

  async listAssistantSkills(ctx: RailgridContext | null, name: string): Promise<ProjectAssistantSkillsResponse> {
    const body = await request<ProjectAssistantSkillsResponse>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'skills')}`,
    )
    return {
      skills: (body.skills ?? []).map(normalizeAssistantSkill),
      ...(body.catalogDigest ? { catalogDigest: body.catalogDigest } : {}),
      ...(body.warnings ? { warnings: body.warnings } : {}),
    }
  },

  async getAssistantSkill(ctx: RailgridContext | null, name: string, packageName: string): Promise<ProjectAssistantSkillDetail> {
    const body = await request<ProjectAssistantSkillDetail>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'skill', packageName)}`,
    )
    return normalizeAssistantSkillDetail(body)
  },

  /** Fetch author-visible detail for a catalog entry by its qualified ID. */
  async getAssistantSkillDetail(ctx: RailgridContext | null, name: string, id: string): Promise<ProjectAssistantSkillDetail> {
    const body = await request<ProjectAssistantSkillDetail>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'skill-detail')}?id=${encodeURIComponent(id)}`,
    )
    return normalizeAssistantSkillDetail(body)
  },

  async createAssistantSkill(
    ctx: RailgridContext | null,
    name: string,
    body: ProjectAssistantSkillPackage,
  ): Promise<ProjectAssistantSkillDetail> {
    const result = await request<ProjectAssistantSkillDetail>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'skills-create')}`,
      normalizeAssistantSkillPackage(body),
    )
    return normalizeAssistantSkillDetail(result)
  },

  /** Import uses the same bounded package payload as create, on its dedicated route. */
  async importAssistantSkill(
    ctx: RailgridContext | null,
    name: string,
    body: ProjectAssistantSkillPackage,
  ): Promise<ProjectAssistantSkillDetail> {
    const result = await request<ProjectAssistantSkillDetail>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'skills-import')}`,
      normalizeAssistantSkillPackage(body),
    )
    return normalizeAssistantSkillDetail(result)
  },

  async updateAssistantSkill(
    ctx: RailgridContext | null,
    name: string,
    packageName: string,
    body: ProjectAssistantSkillPackage,
    expectedDigest: string,
  ): Promise<ProjectAssistantSkillDetail> {
    const result = await request<ProjectAssistantSkillDetail>(
      ctx,
      'PUT',
      `${projectURL(ctx, name, 'skill', packageName)}`,
      { ...normalizeAssistantSkillPackage(body), expectedDigest },
    )
    return normalizeAssistantSkillDetail(result)
  },

  async setAssistantSkillActivation(
    ctx: RailgridContext | null,
    name: string,
    id: string,
    enabled: boolean,
  ): Promise<ProjectAssistantSkillDetail | ProjectAssistantSkill> {
    const result = await request<ProjectAssistantSkillDetail | ProjectAssistantSkill>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'skills-activation')}`,
      { id, enabled },
    )
    return normalizeAssistantSkillDetail(result)
  },

  async exportAssistantSkill(ctx: RailgridContext | null, name: string, packageName: string): Promise<ProjectAssistantSkillExport> {
    const result = await request<Record<string, unknown>>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'skill-export', packageName)}`,
    )
    return normalizeAssistantSkillExport(result)
  },

  async deleteAssistantSkill(ctx: RailgridContext | null, name: string, packageName: string, expectedDigest: string): Promise<void> {
    await request<null>(
      ctx,
      'DELETE',
      `${projectURL(ctx, name, 'skill', packageName)}?expectedDigest=${encodeURIComponent(expectedDigest)}`,
    )
  },

  // The model registry is Studio spec plus one Secret per model, both written
  // with the kube client as the caller — see ./llmRegistry. These stay on the
  // api object so every call site keeps one import; only where the data lives
  // has changed.
  //
  // `configured` comes from the Studio reconciler's status.models[], not from
  // a key the browser holds: the portal never reads credential material.
  async getLLMSettings(ctx: RailgridContext | null): Promise<ProjectLLMSettings> {
    return llmRegistry.getLLMSettings(ctx)
  },

  async discoverLLMModels(
    ctx: RailgridContext | null,
    body: { provider: string; baseURL: string; apiKey?: string; existingModelID?: string },
  ): Promise<ProjectLLMModelDiscovery> {
    return studioRequest<ProjectLLMModelDiscovery>(ctx, 'POST', 'discover-models', body)
  },

  async createLLMModel(
    ctx: RailgridContext | null,
    body: { name: string; provider?: string; baseURL?: string; model: string; apiKey: string },
  ): Promise<ProjectLLMSettings> {
    return llmRegistry.createLLMModel(ctx, body)
  },

  async testLLMConnection(
    ctx: RailgridContext | null,
    body: { provider?: string; baseURL?: string; model: string; apiKey: string; existingModelID?: string },
  ): Promise<{ ok: boolean }> {
    await ensureStudio(ctx)
    return request<{ ok: boolean }>(ctx, 'POST', `${studioURL(ctx, 'test-model')}`, body, {
      timeoutMS: 35_000,
      timeoutMessage: 'model connection test timed out',
    })
  },

  async patchLLMModel(
    ctx: RailgridContext | null,
    modelID: string,
    body: { name?: string; provider?: string; baseURL?: string; model?: string; apiKey?: string },
  ): Promise<ProjectLLMSettings> {
    return llmRegistry.patchLLMModel(ctx, modelID, body)
  },

  async deleteLLMModel(ctx: RailgridContext | null, modelID: string): Promise<ProjectLLMSettings> {
    return llmRegistry.deleteLLMModel(ctx, modelID)
  },

  async setDefaultLLMModel(ctx: RailgridContext | null, modelID: string): Promise<ProjectLLMSettings> {
    return llmRegistry.setDefaultLLMModel(ctx, modelID)
  },

  async getProject(ctx: RailgridContext | null, name: string): Promise<Project> {
    return request<Project>(ctx, 'GET', `${projectURL(ctx, name, 'view')}`)
  },

  async getProjectThumbnail(ctx: RailgridContext | null, name: string, revision = ''): Promise<Blob> {
    const suffix = revision ? `?revision=${encodeURIComponent(revision)}` : ''
    return requestBlob(ctx, `${projectURL(ctx, name, 'thumbnail')}${suffix}`)
  },

  async listProjectIntegrations(ctx: RailgridContext | null, name: string): Promise<ProjectIntegration[]> {
    const body = await request<ListResponse<ProjectIntegration>>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'integrations')}`,
    )
    return body.items ?? []
  },

  async createProjectIntegration(
    ctx: RailgridContext | null,
    name: string,
    body: {
      alias: string
      provider: string
      kind: 'providerReference'
      resourceRef: ProjectProviderResourceReference
      allowedActions: ProjectProviderActionGrant[]
      consentAccepted?: boolean
    },
  ): Promise<ProjectIntegration> {
    return request<ProjectIntegration>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'integrations')}`,
      body,
    )
  },

  async patchProjectIntegration(
    ctx: RailgridContext | null,
    name: string,
    alias: string,
    body: { allowedActions: ProjectProviderActionGrant[]; consentAccepted?: boolean },
  ): Promise<ProjectIntegration> {
    return request<ProjectIntegration>(
      ctx,
      'PATCH',
      `${projectURL(ctx, name, 'integrations', alias)}`,
      body,
    )
  },

  async removeProjectIntegration(ctx: RailgridContext | null, name: string, alias: string): Promise<void> {
    await request<null>(
      ctx,
      'DELETE',
      `${projectURL(ctx, name, 'integrations', alias)}`,
    )
  },

  // updateProjectDetails writes spec.displayName / spec.description straight
  // to the Project CR with a merge patch, then re-reads the project view.
  //
  // The re-read is not a round-trip we could skip: the view is a join the CR
  // does not contain — live infrastructure instance status, the code
  // provider's repository and commit ledger, the workspace source revision,
  // the thumbnail. The CR is the authority for what we just wrote; the view
  // is the authority for everything else about the project.
  //
  // Validation lives on the CRD: displayName is Required/MinLength=1/
  // MaxLength=128, so an empty name is rejected by the API server rather than
  // by a hand-written check. Sharing is NOT written here — preview visibility
  // is POST /preview and publishing is POST/DELETE /publishing, both of which
  // do more than set a field.
  async updateProjectDetails(
    ctx: RailgridContext | null,
    name: string,
    details: { displayName: string; description?: string },
  ): Promise<Project> {
    await projectKubeClient(ctx).patch(projectResource, name, {
      spec: { displayName: details.displayName, description: details.description ?? '' },
    }, { type: 'merge' })
    return api.getProject(ctx, name)
  },

  // deleteProject deletes the Project CR. There is no delete verb any more:
  // the teardown it used to orchestrate over HTTP — instances, the repository
  // claim, conversation rows, the hub identity, the workspace tree — is the
  // Project's finalizer, so `kubectl delete project` and this button are the
  // same operation (controller/project/teardown.go).
  //
  // The UID is a precondition rather than a query parameter: kcp answers 409
  // when the name has been recycled onto a different object, so a stale row
  // cannot delete its replacement.
  //
  // deleteRepository opts in to deleting the Git repository App Studio created
  // for the project. It is stamped on the object first, because the finalizer
  // reads the decision off the object it is finalizing; an adopted repository
  // is never deleted whatever this says. By default the repository survives
  // and only its project claim is released — git is the durable copy of the
  // user's work.
  async deleteProject(
    ctx: RailgridContext | null,
    name: string,
    uid: string,
    options: { deleteRepository?: boolean } = {},
  ): Promise<void> {
    const expectedUID = uid.trim()
    if (!expectedUID) throw new ProjectAPIRequestError('project UID is required before deleting', 400)
    const client = projectKubeClient(ctx)
    if (options.deleteRepository) {
      await client.patch(projectResource, name, {
        metadata: { annotations: { 'ai.railgrid.ai/delete-repository': 'true' } },
      }, { type: 'merge' })
    }
    await client.delete(projectResource, name, { preconditions: { uid: expectedUID } })
  },

  // awaitProjectDeleted polls until the Project is gone, or until it has been
  // replaced by a different object under the same name. A delete returns as
  // soon as the API server accepts it; the object stays visible, terminating,
  // for as long as its finalizer takes, and the list would otherwise read that
  // back as "still there".
  //
  // It is a poll and not a watch because this client speaks plain REST through
  // the hub's kcp proxy, and one disappearing object does not justify a
  // streaming connection. Giving up is not an error: the deletion was accepted
  // and the finalizer is still working, which is what the caller is told.
  async awaitProjectDeleted(
    ctx: RailgridContext | null,
    name: string,
    uid: string,
    options: { timeoutMS?: number; intervalMS?: number } = {},
  ): Promise<boolean> {
    const expectedUID = uid.trim()
    const client = projectKubeClient(ctx)
    const timeoutMS = options.timeoutMS ?? 30_000
    const intervalMS = options.intervalMS ?? 500
    const deadline = Date.now() + timeoutMS
    for (;;) {
      try {
        const current = await client.get<KubeObject>(projectResource, name)
        // A same-name replacement means the one we deleted is gone.
        if (expectedUID && current.metadata?.uid && current.metadata.uid !== expectedUID) return true
      } catch (e) {
        if (isKubeNotFound(e)) return true
        throw e
      }
      if (Date.now() >= deadline) return false
      await new Promise((resolve) => setTimeout(resolve, intervalMS))
    }
  },

  async syncDevelopment(ctx: RailgridContext | null, name: string): Promise<unknown> {
    return request<unknown>(ctx, 'POST', `${projectURL(ctx, name, 'sync-development')}`)
  },

  async authorizeDevelopmentPreview(ctx: RailgridContext | null, name: string): Promise<unknown> {
    return request<unknown>(ctx, 'POST', `${projectURL(ctx, name, 'authorize-development-preview')}`)
  },

  async listAssistantThreads(ctx: RailgridContext | null, name: string, includeArchived = false): Promise<ProjectAssistantThread[]> {
    const threads: ProjectAssistantThread[] = []
    const seenCursors = new Set<string>()
    let cursor = ''
    for (let pageIndex = 0; pageIndex < MAX_ASSISTANT_THREAD_PAGES; pageIndex += 1) {
      // A malformed or cyclic cursor must never spin the portal forever. The
      // current cursor is recorded before each request, so a repeated cursor
      // is detected without issuing the same page twice. Reject rather than
      // returning the prefix: callers replace their local pin/read projection
      // from this list and must not prune state from an incomplete response.
      if (seenCursors.has(cursor)) {
        throw new ProjectAssistantThreadPaginationError('cursor cycle detected')
      }
      seenCursors.add(cursor)
      const query = new URLSearchParams({
        includeArchived: String(includeArchived),
        limit: String(ASSISTANT_THREAD_PAGE_SIZE),
      })
      if (cursor) query.set('cursor', cursor)
      const page = await request<{ items?: ProjectAssistantThread[]; nextCursor?: string }>(
        ctx,
        'GET',
        `${projectURL(ctx, name, 'sessions')}?${query.toString()}`,
      )
      if (Array.isArray(page.items)) threads.push(...page.items)
      const nextCursor = typeof page.nextCursor === 'string' ? page.nextCursor.trim() : ''
      if (!nextCursor) return threads
      cursor = nextCursor
    }
    throw new ProjectAssistantThreadPaginationError(`page limit exceeded (${MAX_ASSISTANT_THREAD_PAGES})`)
  },

  async createAssistantThread(
    ctx: RailgridContext | null,
    name: string,
    title?: string,
    threadID?: string,
  ): Promise<ProjectAssistantThread> {
    return request<ProjectAssistantThread>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'create-session')}`,
      { title, ...(threadID?.trim() ? { id: threadID.trim() } : {}) },
    )
  },

  async patchAssistantThread(
    ctx: RailgridContext | null,
    _name: string,
    threadID: string,
    body: { title?: string; archived?: boolean },
  ): Promise<ProjectAssistantThread> {
    return request<ProjectAssistantThread>(
      ctx,
      'POST',
      `${sessionURL(ctx, threadID, 'edit')}`,
      body,
    )
  },

  async deleteAssistantThread(ctx: RailgridContext | null, _name: string, threadID: string): Promise<void> {
    await request<null>(
      ctx,
      'POST',
      `${sessionURL(ctx, threadID, 'discard')}`,
    )
  },

  async listAssistantThreadItemPage(
    ctx: RailgridContext | null,
    _name: string,
    threadID: string,
    beforeSequence = '',
  ): Promise<ProjectAssistantThreadItemPage> {
    const query = new URLSearchParams({ limit: String(ASSISTANT_THREAD_ITEM_PAGE_TURNS) })
    if (beforeSequence.trim()) query.set('beforeSequence', beforeSequence.trim())
    const body = await request<{ items?: ProjectAssistantThreadItem[]; nextCursor?: string }>(
      ctx,
      'GET',
      `${sessionURL(ctx, threadID, 'items')}?${query.toString()}`,
    )
    return {
      items: Array.isArray(body.items) ? body.items : [],
      nextCursor: typeof body.nextCursor === 'string' ? body.nextCursor.trim() : '',
    }
  },

  // Compatibility helper for call sites that only need the newest bounded
  // window. Interactive history uses listAssistantThreadItemPage so older
  // turns remain explicitly addressable without an unbounded response.
  async listAssistantThreadItems(ctx: RailgridContext | null, name: string, threadID: string): Promise<ProjectAssistantThreadItem[]> {
    return (await api.listAssistantThreadItemPage(ctx, name, threadID)).items
  },

  /** List durable project-scoped receipts; content parts carry only these references. */
  async listAssistantAttachments(ctx: RailgridContext | null, name: string): Promise<ProjectAssistantAttachmentReceipt[]> {
    const body = await request<unknown>(ctx, 'GET', `${projectURL(ctx, name, 'attachments')}`)
    return assistantAttachmentReceipts(body)
  },

  async getAssistantAttachment(ctx: RailgridContext | null, name: string, attachmentID: string, signal?: AbortSignal): Promise<Blob> {
    return requestBlob(ctx, `${projectURL(ctx, name, 'attachments', attachmentID)}`, signal)
  },

  async uploadAssistantAttachment(
    ctx: RailgridContext | null,
    name: string,
    file: File,
    signal?: AbortSignal,
    clientAttachmentID?: string,
  ): Promise<ProjectAssistantAttachmentReceipt> {
    return requestAssistantAttachmentUpload(
      ctx,
      `${projectURL(ctx, name, 'attachments')}`,
      file,
      signal,
      clientAttachmentID,
    )
  },

  async deleteAssistantAttachment(ctx: RailgridContext | null, name: string, attachmentID: string): Promise<void> {
    await request<null>(ctx, 'DELETE', `${projectURL(ctx, name, 'attachments', attachmentID)}`)
  },

  async startAssistantTurn(ctx: RailgridContext | null, _name: string, threadID: string, body: { content: string; clientUserMessageID: string; modelID?: string; collaborationMode: ProjectAssistantRunMode; skills?: string[]; contextResources?: ProjectAssistantContextResource[]; contentParts?: ProjectAssistantContentPart[] }): Promise<{ thread: ProjectAssistantThread; turn: ProjectAssistantTurn }> {
    return request<{ thread: ProjectAssistantThread; turn: ProjectAssistantTurn }>(ctx, 'POST', `${sessionURL(ctx, threadID, 'turn')}`, body)
  },

  async startAssistantReview(ctx: RailgridContext | null, _name: string, threadID: string, body: { target: ProjectAssistantReviewTarget; clientUserMessageID: string; modelID?: string; skills?: string[]; contextResources?: ProjectAssistantContextResource[]; contentParts?: ProjectAssistantContentPart[] }): Promise<{ thread: ProjectAssistantThread; turn: ProjectAssistantTurn }> {
    return request<{ thread: ProjectAssistantThread; turn: ProjectAssistantTurn }>(ctx, 'POST', `${sessionURL(ctx, threadID, 'review')}`, body)
  },

  async getActiveAssistantTurn(ctx: RailgridContext | null, _name: string, threadID: string): Promise<ProjectAssistantTurn | undefined> {
    const headers = tenantHeaders({})
    const res = await providerFetch(ctx)(`${sessionURL(ctx, threadID, 'active-turn')}`, { credentials: 'same-origin', headers })
    if (res.status === 204) return undefined
    if (!res.ok) throw new Error(`active assistant turn failed: ${res.status} ${res.statusText}`)
    return res.json() as Promise<ProjectAssistantTurn>
  },

  async steerAssistantTurn(ctx: RailgridContext | null, _name: string, threadID: string, turnID: string, body: { content: string; clientUserMessageID: string }): Promise<ProjectAssistantTurn> {
    return request<ProjectAssistantTurn>(ctx, 'POST', `${sessionURL(ctx, threadID, 'steer', turnID)}`, body)
  },

  async interruptAssistantTurn(ctx: RailgridContext | null, _name: string, threadID: string, turnID: string, clientRequestID: string): Promise<{ turnID: string; status: ProjectAssistantRunStatus }> {
    return request<{ turnID: string; status: ProjectAssistantRunStatus }>(ctx, 'POST', `${sessionURL(ctx, threadID, 'interrupt', turnID)}`, { clientRequestID })
  },

  async continueAssistantTurn(ctx: RailgridContext | null, _name: string, threadID: string, turnID: string, body: { content?: string; clientUserMessageID: string; skills?: string[]; contextResources?: ProjectAssistantContextResource[]; contentParts?: ProjectAssistantContentPart[] }): Promise<{ thread: ProjectAssistantThread; turn: ProjectAssistantTurn; continuationOfTurnID?: string }> {
    return request<{ thread: ProjectAssistantThread; turn: ProjectAssistantTurn; continuationOfTurnID?: string }>(ctx, 'POST', `${sessionURL(ctx, threadID, 'continue', turnID)}`, body)
  },

  async respondAssistantTurn(ctx: RailgridContext | null, _name: string, threadID: string, turnID: string, kind: 'approval' | 'input', body: { requestID: string; decision?: 'allow' | 'deny'; answer?: string; answers?: Record<string, { answers: string[] }> }): Promise<ProjectAssistantTurn> {
    return request<ProjectAssistantTurn>(ctx, 'POST', `${sessionURL(ctx, threadID, kind, turnID)}`, body)
  },

  async streamAssistantThread(ctx: RailgridContext | null, name: string, threadID: string, afterSequence: number, onEvent: (event: ProjectAssistantThreadEvent) => void, signal?: AbortSignal): Promise<void> {
    return requestAssistantThreadEventStream(ctx, name, threadID, afterSequence, onEvent, signal)
  },

  async getAssistantApprovalMode(ctx: RailgridContext | null, name: string): Promise<ProjectAssistantApprovalPreference> {
    return request<ProjectAssistantApprovalPreference>(
      ctx,
      'GET',
      `${projectURL(ctx, name, 'approval-mode')}`,
    )
  },

  async patchAssistantApprovalMode(
    ctx: RailgridContext | null,
    name: string,
    mode: ProjectAssistantApprovalMode,
  ): Promise<ProjectAssistantApprovalPreference> {
    return request<ProjectAssistantApprovalPreference>(
      ctx,
      'PATCH',
      `${projectURL(ctx, name, 'approval-mode')}`,
      { mode },
    )
  },

  async createPreviewBridgeSession(
    ctx: RailgridContext | null,
    name: string,
    generation: string,
    portalInstanceID: string,
  ): Promise<PreviewBridgeSession> {
    return request<PreviewBridgeSession>(
      ctx,
      'POST',
      `${projectURL(ctx, name, 'preview-bridge-sessions')}`,
      { generation, protocolVersion: 1, portalInstanceID },
      { timeoutMS: 8_000, timeoutMessage: PREVIEW_BRIDGE_TIMEOUT_MESSAGE },
    )
  },

  async deletePreviewBridgeSession(
    ctx: RailgridContext | null,
    name: string,
    sessionID: string,
  ): Promise<void> {
    await request<unknown>(
      ctx,
      'DELETE',
      `${projectURL(ctx, name, 'preview-bridge-sessions', sessionID)}`,
      undefined,
      { timeoutMS: 3_000, timeoutMessage: PREVIEW_BRIDGE_TIMEOUT_MESSAGE },
    )
  },
}
