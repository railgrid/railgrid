import type {
  DevelopmentTemplate,
  ImportRepository,
  KedgeContext,
  ListResponse,
  Project,
  ProjectHydrateResult,
  ProjectAssistantRunStart,
  ProjectAssistantWorkItem,
  ProjectAssistantAbortResponse,
  ProjectAssistantApprovalMode,
  ProjectAssistantApprovalPreference,
  ProjectAssistantSnapshot,
  ProjectAssistantUndoResponse,
  ProjectLLMSettings,
  ProjectMemory,
  ProjectMessage,
  ProjectMessagesPage,
  ProjectCheckpoints,
  ProjectPromotionReadiness,
  ProjectPromoteResult,
  ProviderItem,
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
// verified X-Kedge-Tenant/X-Kedge-User headers, and forwards to the provider's
// /api/* routes. Falls back to the well-known prefix if no basePath arrived yet.
function providerBase(ctx: KedgeContext | null): string {
  const derived = ctx?.basePath ? serviceBase(ctx.basePath) : ''
  return (derived || '/services/providers/app-studio').replace(/\/$/, '')
}

function baseURL(ctx: KedgeContext | null): string {
  const t = tenantSelection()
  if (!t.orgUUID || !t.workspaceUUID) {
    throw new Error('select an organization and workspace first')
  }
  // org/workspace travel as X-Kedge-Org / X-Kedge-Workspace headers (see
  // request()); the hub resolves them to the workspace the provider acts on.
  return `${providerBase(ctx)}/api/projects`
}

async function request<T>(ctx: KedgeContext | null, method: string, path: string, body?: unknown): Promise<T> {
  const headers = tenantHeaders({ token: ctx?.token, json: body !== undefined })

  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
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
    if (isProjectAPIInitializingResponse(res.status, reason, detail)) {
      throw new ProjectAPIInitializingError(detail)
    }
    throw new ProjectAPIRequestError(detail, res.status)
  }
  return (text ? JSON.parse(text) : null) as T
}

function isProjectAPIInitializingResponse(status: number, reason: string, message: string): boolean {
  const normalized = message.toLowerCase()
  return (
    (status === 503 && reason === 'ServiceUnavailable' && normalized.includes('app studio')) ||
    normalized.includes('server could not find the requested resource')
  )
}

async function requestAssistantSnapshotStream(
  ctx: KedgeContext | null,
  name: string,
  runID: string,
  afterRevision: number,
  onSnapshot: (snapshot: ProjectAssistantSnapshot) => void,
  signal?: AbortSignal,
): Promise<void> {
  const headers = tenantHeaders({ token: ctx?.token })
  headers.Accept = 'text/event-stream'
  headers['Last-Event-ID'] = String(afterRevision)
  const res = await fetch(`${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/${encodeURIComponent(runID)}/stream?afterRevision=${encodeURIComponent(String(afterRevision))}`, {
    credentials: 'same-origin', headers, signal,
  })
  if (!res.ok) throw new Error(`assistant stream failed: ${res.status} ${res.statusText}`)
  if (!res.body) throw new Error('missing assistant stream body')
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  const flush = (raw: string) => {
    let event = ''
    let data = ''
    for (const line of raw.split('\n')) {
      if (line.startsWith('event:')) event = line.slice(6).trim()
      if (line.startsWith('data:')) data = data ? `${data}\n${line.slice(5).trimStart()}` : line.slice(5).trimStart()
    }
    if (event === 'snapshot' && data) onSnapshot(JSON.parse(data) as ProjectAssistantSnapshot)
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

export const api = {
  async listProviders(ctx: KedgeContext | null): Promise<ProviderItem[]> {
    const body = await request<ListResponse<ProviderItem>>(ctx, 'GET', '/api/providers')
    return body.items ?? []
  },

  async listProjects(ctx: KedgeContext | null): Promise<Project[]> {
    const body = await request<ListResponse<Project>>(ctx, 'GET', baseURL(ctx))
    return body.items ?? []
  },

  async createProject(
    ctx: KedgeContext | null,
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

  async listDevelopmentTemplates(ctx: KedgeContext | null): Promise<DevelopmentTemplate[]> {
    const body = await request<{ templates: DevelopmentTemplate[] }>(
      ctx,
      'GET',
      `${baseURL(ctx)}/development-templates`,
    )
    return body.templates ?? []
  },

  async listImportRepositories(ctx: KedgeContext | null): Promise<ImportRepository[]> {
    const body = await request<{ repositories: ImportRepository[] }>(
      ctx,
      'GET',
      `${baseURL(ctx)}/import-repositories`,
    )
    return body.repositories ?? []
  },

  async setProjectTemplate(
    ctx: KedgeContext | null,
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

  async hydrateWorkspace(ctx: KedgeContext | null, name: string, ref?: string): Promise<ProjectHydrateResult> {
    return request<ProjectHydrateResult>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/hydrate-workspace`,
      ref ? { ref } : {},
    )
  },

  async getPromotion(ctx: KedgeContext | null, name: string): Promise<ProjectPromotionReadiness> {
    return request<ProjectPromotionReadiness>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/promotion`,
    )
  },

  async getCheckpoints(ctx: KedgeContext | null, name: string): Promise<ProjectCheckpoints> {
    return request<ProjectCheckpoints>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/checkpoints`,
    )
  },

  async promoteProject(
    ctx: KedgeContext | null,
    name: string,
    values?: Record<string, unknown>,
  ): Promise<ProjectPromoteResult> {
    return request<ProjectPromoteResult>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/promote`,
      values ? { values } : {},
    )
  },

  async getProjectCreateReadiness(ctx: KedgeContext | null): Promise<ProjectCreateReadiness> {
    return request<ProjectCreateReadiness>(ctx, 'GET', `${baseURL(ctx)}/create-readiness`)
  },

  async getLLMSettings(ctx: KedgeContext | null): Promise<ProjectLLMSettings> {
    return request<ProjectLLMSettings>(ctx, 'GET', `${baseURL(ctx)}/llm-settings`)
  },

  async patchLLMSettings(
    ctx: KedgeContext | null,
    body: { provider?: string; baseURL?: string; model?: string; apiKey?: string },
  ): Promise<ProjectLLMSettings> {
    return request<ProjectLLMSettings>(ctx, 'PATCH', `${baseURL(ctx)}/llm-settings`, body)
  },

  async getProject(ctx: KedgeContext | null, name: string): Promise<Project> {
    return request<Project>(ctx, 'GET', `${baseURL(ctx)}/${encodeURIComponent(name)}`)
  },

  async patchProject(
    ctx: KedgeContext | null,
    name: string,
    body: { displayName?: string; description?: string },
  ): Promise<Project> {
    return request<Project>(ctx, 'PATCH', `${baseURL(ctx)}/${encodeURIComponent(name)}`, body)
  },

  async deleteProject(ctx: KedgeContext | null, name: string): Promise<void> {
    await request<null>(ctx, 'DELETE', `${baseURL(ctx)}/${encodeURIComponent(name)}`)
  },

  async syncDevelopment(ctx: KedgeContext | null, name: string): Promise<unknown> {
    return request<unknown>(ctx, 'POST', `${baseURL(ctx)}/${encodeURIComponent(name)}/sync-development`)
  },

  async authorizeDevelopmentPreview(ctx: KedgeContext | null, name: string): Promise<unknown> {
    return request<unknown>(ctx, 'POST', `${baseURL(ctx)}/${encodeURIComponent(name)}/authorize-development-preview`)
  },

  async listMessages(ctx: KedgeContext | null, name: string, cursor?: string): Promise<ProjectMessagesPage> {
    const query = cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''
    const body = await request<ProjectMessagesPage>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/messages${query}`,
    )
    return body
  },

  async listAllMessages(ctx: KedgeContext | null, name: string): Promise<ProjectMessage[]> {
    const items: ProjectMessage[] = []
    let cursor: string | undefined
    for (;;) {
      const query = cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''
      const page = await request<ProjectMessagesPage>(
        ctx,
        'GET',
        `${baseURL(ctx)}/${encodeURIComponent(name)}/messages${query}`,
      )
      items.push(...(page.items ?? []))
      if (!page.nextCursor) break
      cursor = page.nextCursor
    }
    return items
  },

  async startAssistantRun(ctx: KedgeContext | null, name: string, body: { content: string; clientRequestID: string; assistantAction?: 'auto' | 'ask' | 'build' | 'continue'; workItemID?: string; workItemRevision?: number }): Promise<ProjectAssistantRunStart> {
    return request<ProjectAssistantRunStart>(ctx, 'POST', `${baseURL(ctx)}/${encodeURIComponent(name)}/messages`, body)
  },

  async getLatestAssistantRun(ctx: KedgeContext | null, name: string): Promise<ProjectAssistantSnapshot | undefined> {
    const headers = tenantHeaders({ token: ctx?.token })
    const res = await fetch(`${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/runs/latest`, { credentials: 'same-origin', headers })
    if (res.status === 204) return undefined
    if (!res.ok) throw new Error(`latest assistant run failed: ${res.status} ${res.statusText}`)
    return res.json() as Promise<ProjectAssistantSnapshot>
  },

  async streamAssistantRun(ctx: KedgeContext | null, name: string, runID: string, afterRevision: number, onSnapshot: (snapshot: ProjectAssistantSnapshot) => void, signal?: AbortSignal): Promise<void> {
    return requestAssistantSnapshotStream(ctx, name, runID, afterRevision, onSnapshot, signal)
  },

  async resumeAssistantRun(
    ctx: KedgeContext | null,
    name: string,
    runID: string,
    body: { requestID: string; decision?: 'allow' | 'deny'; answer?: string; assistantMessageID?: string },
  ): Promise<ProjectAssistantSnapshot> {
    return request<ProjectAssistantSnapshot>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/${encodeURIComponent(runID)}/resume`,
      body,
    )
  },

  async stopAssistantRun(ctx: KedgeContext | null, name: string, runID: string, clientRequestID: string): Promise<ProjectAssistantAbortResponse> {
    return request<ProjectAssistantAbortResponse>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/${encodeURIComponent(runID)}/stop`,
      { clientRequestID },
    )
  },

  async undoAssistantRun(ctx: KedgeContext | null, name: string, runID: string): Promise<ProjectAssistantUndoResponse> {
    return request<ProjectAssistantUndoResponse>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/${encodeURIComponent(runID)}/undo`,
      {},
    )
  },

  async getAssistantApprovalMode(ctx: KedgeContext | null, name: string): Promise<ProjectAssistantApprovalPreference> {
    return request<ProjectAssistantApprovalPreference>(
      ctx,
      'GET',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/approval-mode`,
    )
  },

  async patchAssistantApprovalMode(
    ctx: KedgeContext | null,
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

  async listAssistantWorkItems(ctx: KedgeContext | null, name: string): Promise<ProjectAssistantWorkItem[]> {
    return request<ProjectAssistantWorkItem[]>(ctx, 'GET', `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/work-items`)
  },

  async cancelAssistantWorkItem(ctx: KedgeContext | null, name: string, workItemID: string, revision: number, clientRequestID: string): Promise<ProjectAssistantWorkItem> {
    return request<ProjectAssistantWorkItem>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/work-items/${encodeURIComponent(workItemID)}/cancel`,
      { revision, clientRequestID },
    )
  },

  async getMemory(ctx: KedgeContext | null, name: string): Promise<ProjectMemory> {
    return request<ProjectMemory>(ctx, 'GET', `${baseURL(ctx)}/${encodeURIComponent(name)}/memory`)
  },

  async createPreviewConsoleSession(
    ctx: KedgeContext | null,
    name: string,
    generation: string,
  ): Promise<PreviewConsoleSession> {
    return request<PreviewConsoleSession>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/preview-console/sessions`,
      { generation, protocolVersion: 1 },
    )
  },

  async uploadPreviewConsoleEvents(
    ctx: KedgeContext | null,
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
    )
  },

  async deletePreviewConsoleSession(
    ctx: KedgeContext | null,
    name: string,
    sessionID: string,
  ): Promise<void> {
    await request<unknown>(
      ctx,
      'DELETE',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/preview-console/sessions/${encodeURIComponent(sessionID)}`,
    )
  },
}
