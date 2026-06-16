import type {
  KedgeContext,
  ListResponse,
  Project,
  ProjectLLMSettings,
  ProjectMemory,
  ProjectMessage,
  ProjectMessageStreamEvent,
  ProjectMessagesPage,
  ProviderItem,
} from './types'

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

export function isProjectAPIInitializingError(err: unknown): err is ProjectAPIInitializingError {
  return err instanceof ProjectAPIInitializingError
}

function tenantSelection(): TenantSelection {
  try {
    const raw = localStorage.getItem('kedge:portal:tenant')
    if (!raw) return { orgUUID: null, workspaceUUID: null }
    const parsed = JSON.parse(raw) as { orgUUID?: string | null; workspaceUUID?: string | null }
    return {
      orgUUID: parsed.orgUUID ?? null,
      workspaceUUID: parsed.workspaceUUID ?? null,
    }
  } catch {
    return { orgUUID: null, workspaceUUID: null }
  }
}

// providerBase resolves the hub backend-proxy prefix for this provider from the
// micro-frontend basePath the host injects (/ui/providers/app-studio →
// /services/providers/app-studio). The hub strips that prefix, injects the
// verified X-Kedge-Tenant/X-Kedge-User headers, and forwards to the provider's
// /api/* routes. Falls back to the well-known prefix if no basePath arrived yet.
function providerBase(ctx: KedgeContext | null): string {
  const derived = (ctx?.basePath || '').replace(/^\/ui\/providers\//, '/services/providers/')
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
  const t = tenantSelection()
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (ctx?.token) headers.Authorization = `Bearer ${ctx.token}`
  if (t.orgUUID) headers['X-Kedge-Org'] = t.orgUUID
  if (t.workspaceUUID) headers['X-Kedge-Workspace'] = t.workspaceUUID

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
    throw new Error(detail)
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

function parseRequestError(text: string, fallback: string): string {
  if (!text) return fallback
  try {
    const parsed = JSON.parse(text) as { message?: string }
    return parsed.message || fallback
  } catch {
    return text
  }
}

async function requestStream(
  ctx: KedgeContext | null,
  method: string,
  path: string,
  body: unknown,
  onEvent: (event: ProjectMessageStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const t = tenantSelection()
  const headers: Record<string, string> = {
    Accept: 'text/event-stream',
    'Content-Type': 'application/json',
  }
  if (ctx?.token) headers.Authorization = `Bearer ${ctx.token}`
  if (t.orgUUID) headers['X-Kedge-Org'] = t.orgUUID
  if (t.workspaceUUID) headers['X-Kedge-Workspace'] = t.workspaceUUID

  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers,
    body: JSON.stringify(body),
    signal,
  })
  if (!res.ok) {
    const text = await res.text()
    const fallback = `${method} ${path} failed: ${res.status} ${res.statusText}`
    let detail = parseRequestError(text, fallback)
    let reason = ''
    try {
      const parsed = JSON.parse(text) as { message?: string; reason?: string }
      detail = parsed.message || detail
      reason = parsed.reason || ''
    } catch {
      // keep parsed text fallback
    }
    if (isProjectAPIInitializingResponse(res.status, reason, detail)) {
      throw new ProjectAPIInitializingError(detail)
    }
    throw new Error(detail)
  }
  if (!res.body) {
    throw new Error('missing response stream body')
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  const flushEvent = (raw: string) => {
    const lines = raw.split('\n')
    let type: string | undefined
    let data = ''
    for (const line of lines) {
      if (line.startsWith('event:')) {
        type = line.slice(6).trim()
      } else if (line.startsWith('data:')) {
        data = data ? `${data}\n${line.slice(5).trimStart()}` : line.slice(5).trimStart()
      }
    }
    if (!data) return
    let event: ProjectMessageStreamEvent
    try {
      event = JSON.parse(data) as ProjectMessageStreamEvent
    } catch {
      onEvent({ type: 'error', error: data })
      return
    }
    if (!event.type) event.type = type === 'done' || type === 'error' ? type : 'chunk'
    onEvent(event)
  }

  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      while (true) {
        const sep = buffer.indexOf('\n\n')
        if (sep < 0) break
        const raw = buffer.slice(0, sep)
        buffer = buffer.slice(sep + 2)
        flushEvent(raw)
      }
    }
    if (buffer.trim()) flushEvent(buffer)
  } finally {
    reader.releaseLock()
  }
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
    body: { displayName?: string; description?: string; prompt?: string; connectionRef?: string },
  ): Promise<Project> {
    return request<Project>(ctx, 'POST', baseURL(ctx), body)
  },

  async createProjectStream(
    ctx: KedgeContext | null,
    body: { displayName?: string; description?: string; prompt: string; connectionRef?: string },
    onEvent: (event: ProjectMessageStreamEvent) => void,
    signal?: AbortSignal,
  ): Promise<void> {
    return requestStream(ctx, 'POST', `${baseURL(ctx)}/stream`, body, onEvent, signal)
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

  async createMessageStream(
    ctx: KedgeContext | null,
    name: string,
    content: string,
    onEvent: (event: ProjectMessageStreamEvent) => void,
    signal?: AbortSignal,
  ): Promise<void> {
    return requestStream(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/messages/stream`,
      { role: 'user', content },
      onEvent,
      signal,
    )
  },

  async getMemory(ctx: KedgeContext | null, name: string): Promise<ProjectMemory> {
    return request<ProjectMemory>(ctx, 'GET', `${baseURL(ctx)}/${encodeURIComponent(name)}/memory`)
  },
}
