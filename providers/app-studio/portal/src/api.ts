import type {
  DevelopmentTemplate,
  ImportRepository,
  FarosContext,
  ListResponse,
  Project,
  ProjectRestoreResult,
  ProjectAssistantRunMode,
  ProjectAssistantReviewTarget,
  ProjectAssistantRunStatus,
  ProjectAssistantApprovalMode,
  ProjectAssistantApprovalPreference,
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
} from './types'
import type { ProjectCreateReadiness } from './createReadiness'
import type { PreviewConsoleEvent, PreviewConsoleSession } from './previewConsole'
import { readTenant, serviceBase, tenantHeaders } from './portalkit/tenant'

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

// tenantSelection reads the active org/workspace. Delegates to the shared,
// security-critical portalkit/tenant helper so the storage key + shape stay in
// lockstep with every other portal.
function tenantSelection(): TenantSelection {
  return readTenant()
}

// providerBase resolves the hub backend-proxy prefix for this provider from the
// micro-frontend basePath the host injects (/ui/providers/app-studio →
// /services/providers/app-studio). The hub strips that prefix, injects the
// verified X-Faros-Tenant/X-Faros-User headers, and forwards to the provider's
// /api/* routes. Falls back to the well-known prefix if no basePath arrived yet.
function providerBase(ctx: FarosContext | null): string {
  const derived = ctx?.basePath ? serviceBase(ctx.basePath) : ''
  return (derived || '/services/providers/app-studio').replace(/\/$/, '')
}

function baseURL(ctx: FarosContext | null): string {
  const t = tenantSelection()
  if (!t.orgUUID || !t.workspaceUUID) {
    throw new Error('select an organization and workspace first')
  }
  // org/workspace travel as X-Faros-Org / X-Faros-Workspace headers (see
  // request()); the hub resolves them to the workspace the provider acts on.
  return `${providerBase(ctx)}/api/projects`
}

interface ProjectAPIRequestOptions {
  timeoutMS?: number
}

const ASSISTANT_THREAD_PAGE_SIZE = 500
const MAX_ASSISTANT_THREAD_PAGES = 100

async function request<T>(ctx: FarosContext | null, method: string, path: string, body?: unknown, options: ProjectAPIRequestOptions = {}): Promise<T> {
  const headers = tenantHeaders({ token: ctx?.token, json: body !== undefined })
  const controller = options.timeoutMS ? new AbortController() : null
  let timedOut = false
  const timeout = controller ? window.setTimeout(() => {
    timedOut = true
    controller.abort()
  }, options.timeoutMS) : undefined
  let res: Response
  let text: string
  try {
    res = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined,
      signal: controller?.signal,
    })
    text = await res.text()
  } catch (error) {
    if (timedOut) throw new ProjectAPIRequestError('preview console request timed out', 408)
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

async function requestBlob(ctx: FarosContext | null, path: string): Promise<Blob> {
  const res = await fetch(path, {
    method: 'GET',
    credentials: 'same-origin',
    headers: tenantHeaders({ token: ctx?.token }),
    cache: 'no-cache',
  })
  if (!res.ok) {
    throw new ProjectAPIRequestError(res.statusText || 'project thumbnail is unavailable', res.status)
  }
  return res.blob()
}

function isProjectAPIInitializingResponse(status: number, reason: string, message: string): boolean {
  const normalized = message.toLowerCase()
  return (
    (status === 503 && reason === 'ServiceUnavailable' && normalized.includes('app studio')) ||
    normalized.includes('server could not find the requested resource')
  )
}

async function requestAssistantThreadEventStream(
  ctx: FarosContext | null,
  name: string,
  threadID: string,
  afterSequence: number,
  onEvent: (event: ProjectAssistantThreadEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const headers = tenantHeaders({ token: ctx?.token })
  headers.Accept = 'text/event-stream'
  headers['Last-Event-ID'] = String(afterSequence)
  const res = await fetch(`${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/threads/${encodeURIComponent(threadID)}/events?afterSequence=${encodeURIComponent(String(afterSequence))}`, {
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
  async listProviders(ctx: FarosContext | null): Promise<ProviderItem[]> {
    const body = await request<ListResponse<ProviderItem>>(ctx, 'GET', '/api/providers')
    return body.items ?? []
  },

  async listProjects(ctx: FarosContext | null): Promise<Project[]> {
    const body = await request<ListResponse<Project>>(ctx, 'GET', baseURL(ctx))
    return body.items ?? []
  },

  async createProject(
    ctx: FarosContext | null,
    body: {
      displayName?: string
      description?: string
      prompt?: string
      templateName?: string
      inferDevelopmentTemplate?: boolean
      connectionRef?: string
      existingRepositoryRef?: string
    },
  ): Promise<Project> {
    return request<Project>(ctx, 'POST', baseURL(ctx), body)
  },

  // createProjectStream creates a project over SSE, surfacing each creation
  // step (Planning, Configuring repository, Attaching scaffold to <template>,
  // …) via onStatus, and resolves with the created Project. Same inputs as
  // createProject; the caller starts the first assistant turn afterward.
  async createProjectStream(
    ctx: FarosContext | null,
    body: {
      displayName?: string
      description?: string
      prompt?: string
      templateName?: string
      inferDevelopmentTemplate?: boolean
      connectionRef?: string
      existingRepositoryRef?: string
    },
    onStatus: (message: string) => void,
    signal?: AbortSignal,
  ): Promise<Project> {
    const headers = tenantHeaders({ token: ctx?.token })
    headers.Accept = 'text/event-stream'
    headers['Content-Type'] = 'application/json'
    const res = await fetch(`${baseURL(ctx)}/stream`, {
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
    ctx: FarosContext | null,
    body: { prompt?: string; templateName?: string },
  ): Promise<ProjectPlan> {
    return request<ProjectPlan>(ctx, 'POST', `${baseURL(ctx)}/plan`, body)
  },

  // reseedScaffold re-attaches the template's starter code to an empty
  // workspace (retry after a failed seed, or seed a legacy project).
  async reseedScaffold(
    ctx: FarosContext | null,
    name: string,
  ): Promise<{ template: string; scaffold: { repository: string; ref?: string }; seeded: number }> {
    return request(ctx, 'POST', `${baseURL(ctx)}/${encodeURIComponent(name)}/scaffold`, {})
  },

  // listProjectFiles returns the live dev workspace file tree (flat, sorted
  // paths with sizes) for the code explorer.
  async listProjectFiles(ctx: FarosContext | null, name: string): Promise<ProjectFileList> {
    return request<ProjectFileList>(ctx, 'GET', `${baseURL(ctx)}/${encodeURIComponent(name)}/files`)
  },

  // readProjectFile returns one workspace file's bounded content.
  async readProjectFile(ctx: FarosContext | null, name: string, path: string): Promise<ProjectFileContent> {
    return request<ProjectFileContent>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/files/content?path=${encodeURIComponent(path)}`,
    )
  },

  async listDevelopmentTemplates(ctx: FarosContext | null): Promise<DevelopmentTemplate[]> {
    const body = await request<{ templates: DevelopmentTemplate[] }>(
      ctx,
      'GET',
      `${baseURL(ctx)}/development-templates`,
    )
    return body.templates ?? []
  },

  async listImportRepositories(ctx: FarosContext | null): Promise<ImportRepository[]> {
    const body = await request<{ repositories: ImportRepository[] }>(
      ctx,
      'GET',
      `${baseURL(ctx)}/import-repositories`,
    )
    return body.repositories ?? []
  },

  async setProjectTemplate(
    ctx: FarosContext | null,
    name: string,
    template: string,
  ): Promise<{ template: string; components: Record<string, string> }> {
    return request<{ template: string; components: Record<string, string> }>(
      ctx,
      'PUT',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/template`,
      { template },
    )
  },

  async restoreWorkspace(ctx: FarosContext | null, name: string, commitSHA: string, expectedSourceRevision: number): Promise<ProjectRestoreResult> {
    return request<ProjectRestoreResult>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/restore-workspace`,
      { commitSHA, expectedSourceRevision },
    )
  },

  async getPromotion(ctx: FarosContext | null, name: string): Promise<ProjectPromotionReadiness> {
    return request<ProjectPromotionReadiness>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/promotion`,
    )
  },

  async listReleases(ctx: FarosContext | null, name: string): Promise<ProjectRelease[]> {
    const body = await request<ProjectReleasesResponse>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/releases`,
    )
    return body.items ?? []
  },

  async getCheckpoints(ctx: FarosContext | null, name: string): Promise<ProjectCheckpoints> {
    return request<ProjectCheckpoints>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/checkpoints`,
    )
  },

  async promoteProject(
    ctx: FarosContext | null,
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
      `${baseURL(ctx)}/${encodeURIComponent(name)}/promote`,
      body,
    )
  },

  // Preview visibility is the development-side counterpart of publishing: the
  // same two modes on the dev URL, defaulting to restricted.
  async getPreviewAccess(ctx: FarosContext | null, name: string): Promise<ProjectPreviewAccess> {
    return request<ProjectPreviewAccess>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/preview`,
    )
  },

  async setPreviewAccess(
    ctx: FarosContext | null,
    name: string,
    mode: ProjectPublishingMode,
  ): Promise<ProjectPreviewAccess> {
    return request<ProjectPreviewAccess>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/preview`,
      { mode },
    )
  },

  async listPreviewGrants(ctx: FarosContext | null, name: string): Promise<ProjectPublishingGrant[]> {
    const res = await request<{ items?: ProjectPublishingGrant[] }>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/preview/grants`,
    )
    return res.items ?? []
  },

  async createPreviewGrant(
    ctx: FarosContext | null,
    name: string,
    user: string,
    invite = false,
  ): Promise<ProjectPublishingGrant[]> {
    const res = await request<{ items?: ProjectPublishingGrant[] }>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/preview/grants`,
      { user, invite },
    )
    return res.items ?? []
  },

  async revokePreviewGrant(
    ctx: FarosContext | null,
    name: string,
    grant: string,
  ): Promise<ProjectPublishingGrant[]> {
    const res = await request<{ items?: ProjectPublishingGrant[] }>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/preview/grants/${encodeURIComponent(grant)}`,
    )
    return res.items ?? []
  },

  async getPublishing(ctx: FarosContext | null, name: string): Promise<ProjectPublishing> {
    return request<ProjectPublishing>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/publishing`,
    )
  },

  async publishProject(
    ctx: FarosContext | null,
    name: string,
    mode: ProjectPublishingMode,
  ): Promise<ProjectPublishing> {
    return request<ProjectPublishing>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/publishing`,
      { mode },
    )
  },

  async unpublishProject(ctx: FarosContext | null, name: string): Promise<ProjectPublishing> {
    return request<ProjectPublishing>(
      ctx,
      'DELETE',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/publishing`,
    )
  },

  async listPublishingMembers(ctx: FarosContext | null, name: string): Promise<ProjectPublishingMember[]> {
    const body = await request<{ items?: ProjectPublishingMember[] }>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/publishing/members`,
    )
    return body.items ?? []
  },

  async listPublishingGrants(ctx: FarosContext | null, name: string): Promise<ProjectPublishingGrant[]> {
    const body = await request<{ items?: ProjectPublishingGrant[] }>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/publishing/grants`,
    )
    return body.items ?? []
  },

  async grantPublishingAccess(
    ctx: FarosContext | null,
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
      `${baseURL(ctx)}/${encodeURIComponent(name)}/publishing/grants`,
      invite ? { user, invite: true } : { user },
    )
  },

  async revokePublishingAccess(ctx: FarosContext | null, name: string, grant: string): Promise<ProjectPublishingGrant> {
    return request<ProjectPublishingGrant>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/publishing/grants/${encodeURIComponent(grant)}`,
    )
  },

  async getProjectCreateReadiness(ctx: FarosContext | null): Promise<ProjectCreateReadiness> {
    return request<ProjectCreateReadiness>(ctx, 'GET', `${baseURL(ctx)}/create-readiness`)
  },

  async listAssistantSkills(ctx: FarosContext | null, name: string): Promise<ProjectAssistantSkillsResponse> {
    const body = await request<ProjectAssistantSkillsResponse>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/skills`,
    )
    return {
      skills: (body.skills ?? []).map(normalizeAssistantSkill),
      ...(body.catalogDigest ? { catalogDigest: body.catalogDigest } : {}),
      ...(body.warnings ? { warnings: body.warnings } : {}),
    }
  },

  async getAssistantSkill(ctx: FarosContext | null, name: string, packageName: string): Promise<ProjectAssistantSkillDetail> {
    const body = await request<ProjectAssistantSkillDetail>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/skills/project/${encodeURIComponent(packageName)}`,
    )
    return normalizeAssistantSkillDetail(body)
  },

  /** Fetch author-visible detail for a catalog entry by its qualified ID. */
  async getAssistantSkillDetail(ctx: FarosContext | null, name: string, id: string): Promise<ProjectAssistantSkillDetail> {
    const body = await request<ProjectAssistantSkillDetail>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/skills/detail?id=${encodeURIComponent(id)}`,
    )
    return normalizeAssistantSkillDetail(body)
  },

  async createAssistantSkill(
    ctx: FarosContext | null,
    name: string,
    body: ProjectAssistantSkillPackage,
  ): Promise<ProjectAssistantSkillDetail> {
    const result = await request<ProjectAssistantSkillDetail>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/skills/project`,
      normalizeAssistantSkillPackage(body),
    )
    return normalizeAssistantSkillDetail(result)
  },

  /** Import uses the same bounded package payload as create, on its dedicated route. */
  async importAssistantSkill(
    ctx: FarosContext | null,
    name: string,
    body: ProjectAssistantSkillPackage,
  ): Promise<ProjectAssistantSkillDetail> {
    const result = await request<ProjectAssistantSkillDetail>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/skills/project/import`,
      normalizeAssistantSkillPackage(body),
    )
    return normalizeAssistantSkillDetail(result)
  },

  async updateAssistantSkill(
    ctx: FarosContext | null,
    name: string,
    packageName: string,
    body: ProjectAssistantSkillPackage,
    expectedDigest: string,
  ): Promise<ProjectAssistantSkillDetail> {
    const result = await request<ProjectAssistantSkillDetail>(
      ctx,
      'PUT',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/skills/project/${encodeURIComponent(packageName)}`,
      { ...normalizeAssistantSkillPackage(body), expectedDigest },
    )
    return normalizeAssistantSkillDetail(result)
  },

  async setAssistantSkillActivation(
    ctx: FarosContext | null,
    name: string,
    id: string,
    enabled: boolean,
  ): Promise<ProjectAssistantSkillDetail | ProjectAssistantSkill> {
    const result = await request<ProjectAssistantSkillDetail | ProjectAssistantSkill>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/skills/activation`,
      { id, enabled },
    )
    return normalizeAssistantSkillDetail(result)
  },

  async exportAssistantSkill(ctx: FarosContext | null, name: string, packageName: string): Promise<ProjectAssistantSkillExport> {
    const result = await request<Record<string, unknown>>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/skills/project/${encodeURIComponent(packageName)}/export`,
    )
    return normalizeAssistantSkillExport(result)
  },

  async deleteAssistantSkill(ctx: FarosContext | null, name: string, packageName: string, expectedDigest: string): Promise<void> {
    await request<null>(
      ctx,
      'DELETE',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/skills/project/${encodeURIComponent(packageName)}?expectedDigest=${encodeURIComponent(expectedDigest)}`,
    )
  },

  async getLLMSettings(ctx: FarosContext | null): Promise<ProjectLLMSettings> {
    return request<ProjectLLMSettings>(ctx, 'GET', `${baseURL(ctx)}/llm-settings`)
  },

  async patchLLMSettings(
    ctx: FarosContext | null,
    body: { provider?: string; baseURL?: string; model?: string; apiKey?: string },
  ): Promise<ProjectLLMSettings> {
    return request<ProjectLLMSettings>(ctx, 'PATCH', `${baseURL(ctx)}/llm-settings`, body)
  },

  async createLLMModel(
    ctx: FarosContext | null,
    body: { name: string; provider?: string; baseURL?: string; model: string; apiKey?: string },
  ): Promise<ProjectLLMSettings> {
    return request<ProjectLLMSettings>(ctx, 'POST', `${baseURL(ctx)}/llm-settings/models`, body)
  },

  async patchLLMModel(
    ctx: FarosContext | null,
    modelID: string,
    body: { name?: string; provider?: string; baseURL?: string; model?: string; apiKey?: string },
  ): Promise<ProjectLLMSettings> {
    return request<ProjectLLMSettings>(ctx, 'PATCH', `${baseURL(ctx)}/llm-settings/models/${encodeURIComponent(modelID)}`, body)
  },

  async deleteLLMModel(ctx: FarosContext | null, modelID: string): Promise<ProjectLLMSettings> {
    return request<ProjectLLMSettings>(ctx, 'DELETE', `${baseURL(ctx)}/llm-settings/models/${encodeURIComponent(modelID)}`)
  },

  async setDefaultLLMModel(ctx: FarosContext | null, modelID: string): Promise<ProjectLLMSettings> {
    return request<ProjectLLMSettings>(ctx, 'PATCH', `${baseURL(ctx)}/llm-settings/default`, { modelID })
  },

  async getProject(ctx: FarosContext | null, name: string): Promise<Project> {
    return request<Project>(ctx, 'GET', `${baseURL(ctx)}/${encodeURIComponent(name)}`)
  },

  async getProjectThumbnail(ctx: FarosContext | null, name: string, revision = ''): Promise<Blob> {
    const suffix = revision ? `?revision=${encodeURIComponent(revision)}` : ''
    return requestBlob(ctx, `${baseURL(ctx)}/${encodeURIComponent(name)}/thumbnail${suffix}`)
  },

  async listProjectIntegrations(ctx: FarosContext | null, name: string): Promise<ProjectIntegration[]> {
    const body = await request<ListResponse<ProjectIntegration>>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/integrations`,
    )
    return body.items ?? []
  },

  async createProjectIntegration(
    ctx: FarosContext | null,
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
      `${baseURL(ctx)}/${encodeURIComponent(name)}/integrations`,
      body,
    )
  },

  async patchProjectIntegration(
    ctx: FarosContext | null,
    name: string,
    alias: string,
    body: { allowedActions: ProjectProviderActionGrant[]; consentAccepted?: boolean },
  ): Promise<ProjectIntegration> {
    return request<ProjectIntegration>(
      ctx,
      'PATCH',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/integrations/${encodeURIComponent(alias)}`,
      body,
    )
  },

  async removeProjectIntegration(ctx: FarosContext | null, name: string, alias: string): Promise<void> {
    await request<null>(
      ctx,
      'DELETE',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/integrations/${encodeURIComponent(alias)}`,
    )
  },

  async patchProject(
    ctx: FarosContext | null,
    name: string,
    body: {
      displayName?: string
      description?: string
      sharing?: Project['sharing']
    },
  ): Promise<Project> {
    return request<Project>(ctx, 'PATCH', `${baseURL(ctx)}/${encodeURIComponent(name)}`, body)
  },

  async deleteProject(ctx: FarosContext | null, name: string, uid: string): Promise<void> {
    const expectedUID = uid.trim()
    if (!expectedUID) throw new ProjectAPIRequestError('project UID is required before deleting', 400)
    const query = new URLSearchParams({ uid: expectedUID })
    await request<null>(ctx, 'DELETE', `${baseURL(ctx)}/${encodeURIComponent(name)}?${query}`)
  },

  async syncDevelopment(ctx: FarosContext | null, name: string): Promise<unknown> {
    return request<unknown>(ctx, 'POST', `${baseURL(ctx)}/${encodeURIComponent(name)}/sync-development`)
  },

  async authorizeDevelopmentPreview(ctx: FarosContext | null, name: string): Promise<unknown> {
    return request<unknown>(ctx, 'POST', `${baseURL(ctx)}/${encodeURIComponent(name)}/authorize-development-preview`)
  },

  async listAssistantThreads(ctx: FarosContext | null, name: string, includeArchived = false): Promise<ProjectAssistantThread[]> {
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
        `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/threads?${query.toString()}`,
      )
      if (Array.isArray(page.items)) threads.push(...page.items)
      const nextCursor = typeof page.nextCursor === 'string' ? page.nextCursor.trim() : ''
      if (!nextCursor) return threads
      cursor = nextCursor
    }
    throw new ProjectAssistantThreadPaginationError(`page limit exceeded (${MAX_ASSISTANT_THREAD_PAGES})`)
  },

  async createAssistantThread(ctx: FarosContext | null, name: string, title?: string): Promise<ProjectAssistantThread> {
    return request<ProjectAssistantThread>(ctx, 'POST', `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/threads`, { title })
  },

  async patchAssistantThread(
    ctx: FarosContext | null,
    name: string,
    threadID: string,
    body: { title?: string; archived?: boolean },
  ): Promise<ProjectAssistantThread> {
    return request<ProjectAssistantThread>(
      ctx,
      'PATCH',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/threads/${encodeURIComponent(threadID)}`,
      body,
    )
  },

  async deleteAssistantThread(ctx: FarosContext | null, name: string, threadID: string): Promise<void> {
    await request<null>(
      ctx,
      'DELETE',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/threads/${encodeURIComponent(threadID)}`,
    )
  },

  async listAssistantThreadItems(ctx: FarosContext | null, name: string, threadID: string): Promise<ProjectAssistantThreadItem[]> {
    const body = await request<{ items: ProjectAssistantThreadItem[] }>(ctx, 'GET', `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/threads/${encodeURIComponent(threadID)}/items`)
    return body.items ?? []
  },

  async startAssistantTurn(ctx: FarosContext | null, name: string, threadID: string, body: { content: string; clientUserMessageID: string; modelID?: string; collaborationMode: ProjectAssistantRunMode; skills?: string[]; contextResources?: ProjectAssistantContextResource[]; contentParts?: ProjectAssistantContentPart[] }): Promise<{ thread: ProjectAssistantThread; turn: ProjectAssistantTurn }> {
    return request<{ thread: ProjectAssistantThread; turn: ProjectAssistantTurn }>(ctx, 'POST', `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/threads/${encodeURIComponent(threadID)}/turns`, body)
  },

  async startAssistantReview(ctx: FarosContext | null, name: string, threadID: string, body: { target: ProjectAssistantReviewTarget; clientUserMessageID: string; modelID?: string; skills?: string[]; contextResources?: ProjectAssistantContextResource[]; contentParts?: ProjectAssistantContentPart[] }): Promise<{ thread: ProjectAssistantThread; turn: ProjectAssistantTurn }> {
    return request<{ thread: ProjectAssistantThread; turn: ProjectAssistantTurn }>(ctx, 'POST', `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/threads/${encodeURIComponent(threadID)}/reviews`, body)
  },

  async getActiveAssistantTurn(ctx: FarosContext | null, name: string, threadID: string): Promise<ProjectAssistantTurn | undefined> {
    const headers = tenantHeaders({ token: ctx?.token })
    const res = await fetch(`${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/threads/${encodeURIComponent(threadID)}/turns/active`, { credentials: 'same-origin', headers })
    if (res.status === 204) return undefined
    if (!res.ok) throw new Error(`active assistant turn failed: ${res.status} ${res.statusText}`)
    return res.json() as Promise<ProjectAssistantTurn>
  },

  async steerAssistantTurn(ctx: FarosContext | null, name: string, threadID: string, turnID: string, body: { content: string; clientUserMessageID: string }): Promise<ProjectAssistantTurn> {
    return request<ProjectAssistantTurn>(ctx, 'POST', `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/threads/${encodeURIComponent(threadID)}/turns/${encodeURIComponent(turnID)}/steer`, body)
  },

  async interruptAssistantTurn(ctx: FarosContext | null, name: string, threadID: string, turnID: string, clientRequestID: string): Promise<{ turnID: string; status: ProjectAssistantRunStatus }> {
    return request<{ turnID: string; status: ProjectAssistantRunStatus }>(ctx, 'POST', `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/threads/${encodeURIComponent(threadID)}/turns/${encodeURIComponent(turnID)}/interrupt`, { clientRequestID })
  },

  async continueAssistantTurn(ctx: FarosContext | null, name: string, threadID: string, turnID: string, body: { content?: string; clientUserMessageID: string; skills?: string[]; contextResources?: ProjectAssistantContextResource[]; contentParts?: ProjectAssistantContentPart[] }): Promise<{ thread: ProjectAssistantThread; turn: ProjectAssistantTurn; continuationOfTurnID?: string }> {
    return request<{ thread: ProjectAssistantThread; turn: ProjectAssistantTurn; continuationOfTurnID?: string }>(ctx, 'POST', `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/threads/${encodeURIComponent(threadID)}/turns/${encodeURIComponent(turnID)}/continue`, body)
  },

  async respondAssistantTurn(ctx: FarosContext | null, name: string, threadID: string, turnID: string, kind: 'approval' | 'input', body: { requestID: string; decision?: 'allow' | 'deny'; answer?: string; answers?: Record<string, { answers: string[] }> }): Promise<ProjectAssistantTurn> {
    return request<ProjectAssistantTurn>(ctx, 'POST', `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/threads/${encodeURIComponent(threadID)}/turns/${encodeURIComponent(turnID)}/${kind}`, body)
  },

  async streamAssistantThread(ctx: FarosContext | null, name: string, threadID: string, afterSequence: number, onEvent: (event: ProjectAssistantThreadEvent) => void, signal?: AbortSignal): Promise<void> {
    return requestAssistantThreadEventStream(ctx, name, threadID, afterSequence, onEvent, signal)
  },

  async getAssistantApprovalMode(ctx: FarosContext | null, name: string): Promise<ProjectAssistantApprovalPreference> {
    return request<ProjectAssistantApprovalPreference>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/approval-mode`,
    )
  },

  async patchAssistantApprovalMode(
    ctx: FarosContext | null,
    name: string,
    mode: ProjectAssistantApprovalMode,
  ): Promise<ProjectAssistantApprovalPreference> {
    return request<ProjectAssistantApprovalPreference>(
      ctx,
      'PATCH',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/approval-mode`,
      { mode },
    )
  },

  async createPreviewConsoleSession(
    ctx: FarosContext | null,
    name: string,
    generation: string,
    portalInstanceID: string,
  ): Promise<PreviewConsoleSession> {
    return request<PreviewConsoleSession>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/preview-console/sessions`,
      { generation, protocolVersion: 1, portalInstanceID },
      { timeoutMS: 8_000 },
    )
  },

  async uploadPreviewConsoleEvents(
    ctx: FarosContext | null,
    name: string,
    sessionID: string,
    generation: string,
    events: PreviewConsoleEvent[],
    droppedCount: number,
  ): Promise<void> {
    await request<unknown>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/preview-console/sessions/${encodeURIComponent(sessionID)}/events`,
      { generation, protocolVersion: 1, droppedCount, events },
      { timeoutMS: 5_000 },
    )
  },

  async deletePreviewConsoleSession(
    ctx: FarosContext | null,
    name: string,
    sessionID: string,
  ): Promise<void> {
    await request<unknown>(
      ctx,
      'DELETE',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/preview-console/sessions/${encodeURIComponent(sessionID)}`,
      undefined,
      { timeoutMS: 3_000 },
    )
  },
}
