import type {
  KedgeContext,
  ListResponse,
  Project,
  ProjectAssistantResumeResponse,
  ProjectAssistantUIComponent,
  ProjectAssistantUIEvent,
  ProjectLLMSettings,
  ProjectMemory,
  ProjectMessage,
  ProjectMessageStreamControlEvent,
  ProjectMessageStreamEvent,
  ProjectMessagesPage,
  ProviderItem,
} from './types'
import type { ProjectCreateReadiness } from './createReadiness'

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

interface ProjectMessageStreamEventParseResult {
  event?: ProjectMessageStreamEvent
  error?: string
}

const allowedStreamEventTypes = new Set<ProjectMessageStreamControlEvent['type']>([
  'run_failed',
  'run_finished',
  'project',
])

const assistantUIEnvelopeKeys = ['beginRendering', 'surfaceUpdate', 'dataModelUpdate', 'interruptRequest'] as const
const allowedStreamControlTopLevelKeys = new Set(['type', 'assistantMessageID', 'error', 'project'])
const allowedAssistantUIEventKeys = new Set(['beginRendering', 'surfaceUpdate', 'dataModelUpdate', 'interruptRequest'])
const allowedAssistantUIBeginRenderingKeys = new Set(['surfaceId', 'root'])
const allowedAssistantUISurfaceUpdateKeys = new Set(['surfaceId', 'components'])
const allowedAssistantUISurfaceComponentKeys = new Set(['id', 'component'])
const allowedAssistantUIComponentValueKeys = new Set(['Text', 'Column', 'Card', 'Row'])
const allowedAssistantUITextComponentKeys = new Set(['value', 'dataKey', 'usageHint'])
const allowedAssistantUIContainerComponentKeys = new Set(['children'])
const allowedAssistantUIDataModelUpdateKeys = new Set(['surfaceId', 'contents'])
const allowedAssistantUIDataContentKeys = new Set(['key', 'valueString', 'append'])
const allowedAssistantUIInterruptRequestKeys = new Set([
  'interruptId',
  'kind',
  'surfaceId',
  'description',
  'questions',
  'status',
  'action',
])
const allowedAssistantUIInterruptActionKeys = new Set(['runId', 'requestId', 'assistantMessageId'])

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isString(value: unknown): value is string {
  return typeof value === 'string'
}

function unknownKeys(value: Record<string, unknown>, allowed: Set<string>): string[] {
  return Object.keys(value).filter((key) => !allowed.has(key))
}

function parseProjectMessageStreamEvent(raw: string, hintType?: string): ProjectMessageStreamEventParseResult {
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return { error: `invalid JSON for stream event${hintType ? ` (${hintType})` : ''}` }
  }

  if (!isRecord(parsed)) return { error: 'stream event must be a JSON object' }

  const envelopeKeys = assistantUIEnvelopeKeys.filter((key) => parsed[key] !== undefined)
  if (envelopeKeys.length > 0) {
    if (envelopeKeys.length !== 1) {
      return { error: `A2UI stream event must include exactly one envelope key, got ${envelopeKeys.join(', ')}` }
    }
    const topLevelExtra = unknownKeys(parsed, allowedAssistantUIEventKeys)
    if (topLevelExtra.length > 0) {
      return { error: `A2UI stream event has unknown top-level keys: ${topLevelExtra.join(', ')}` }
    }
    if (hintType && hintType !== envelopeKeys[0]) {
      return { error: `stream event type mismatch: ${envelopeKeys[0]} vs ${hintType}` }
    }
    return parseAssistantUIEnvelope(parsed, envelopeKeys[0])
  }

  const topLevelExtra = unknownKeys(parsed, allowedStreamControlTopLevelKeys)
  if (topLevelExtra.length > 0) {
    return { error: `stream control event has unknown top-level keys: ${topLevelExtra.join(', ')}` }
  }

  if (!isString(parsed.type)) {
    return { error: `stream control event missing or invalid type${hintType ? ` (${hintType})` : ''}` }
  }
  if (!allowedStreamEventTypes.has(parsed.type as ProjectMessageStreamControlEvent['type'])) {
    return { error: `unsupported stream event type ${parsed.type}` }
  }
  if (hintType && parsed.type !== hintType) {
    return { error: `stream event type mismatch: ${parsed.type} vs ${hintType}` }
  }

  const event: ProjectMessageStreamControlEvent = {
    type: parsed.type as ProjectMessageStreamControlEvent['type'],
  }

  if (parsed.assistantMessageID !== undefined) {
    if (!isString(parsed.assistantMessageID)) {
      return { error: 'stream event assistantMessageID must be a string when provided' }
    }
    event.assistantMessageID = parsed.assistantMessageID
  }

  if (event.type === 'project') {
    if (!isRecord(parsed.project)) {
      return { error: 'stream event project must be an object' }
    }
    if (!isString(parsed.project.name) || parsed.project.name.trim() === '') {
      return { error: 'stream event project must include name string' }
    }
    event.project = parsed.project as unknown as Project
    return { event }
  }

  if (event.type === 'run_failed') {
    if (!isString(parsed.assistantMessageID) || parsed.assistantMessageID.trim() === '') {
      return { error: 'stream event run_failed requires assistantMessageID string' }
    }
    if (!isString(parsed.error) || parsed.error.trim() === '') {
      return { error: 'stream event run_failed requires error string' }
    }
    event.assistantMessageID = parsed.assistantMessageID
    event.error = parsed.error
    return { event }
  }

  if (event.type === 'run_finished') {
    if (!isString(parsed.assistantMessageID) || parsed.assistantMessageID.trim() === '') {
      return { error: 'stream event run_finished requires assistantMessageID string' }
    }
    if (parsed.error !== undefined) return { error: 'stream event run_finished must not include error' }
    event.assistantMessageID = parsed.assistantMessageID
    return { event }
  }

  return { error: `unsupported stream event type ${event.type}` }
}

function parseAssistantUIEnvelope(parsed: Record<string, unknown>, envelopeKey: typeof assistantUIEnvelopeKeys[number]): ProjectMessageStreamEventParseResult {
  const ui: ProjectAssistantUIEvent = {}
  if (envelopeKey === 'beginRendering') {
    if (!isRecord(parsed.beginRendering)) {
      return { error: 'A2UI beginRendering must be an object' }
    }
    const beginExtra = unknownKeys(parsed.beginRendering, allowedAssistantUIBeginRenderingKeys)
    if (beginExtra.length > 0) {
      return { error: `A2UI beginRendering has unknown keys: ${beginExtra.join(', ')}` }
    }
    if (!isString(parsed.beginRendering.surfaceId) || !isString(parsed.beginRendering.root)) {
      return { error: 'A2UI beginRendering requires surfaceId and root strings' }
    }
    ui.beginRendering = {
      surfaceId: parsed.beginRendering.surfaceId,
      root: parsed.beginRendering.root,
    }
    return { event: ui }
  }

  if (envelopeKey === 'surfaceUpdate') {
    if (!isRecord(parsed.surfaceUpdate)) {
      return { error: 'A2UI surfaceUpdate must be an object' }
    }
    const surfaceUpdateExtra = unknownKeys(parsed.surfaceUpdate, allowedAssistantUISurfaceUpdateKeys)
    if (surfaceUpdateExtra.length > 0) {
      return { error: `A2UI surfaceUpdate has unknown keys: ${surfaceUpdateExtra.join(', ')}` }
    }
    if (!isString(parsed.surfaceUpdate.surfaceId)) {
      return { error: 'A2UI surfaceUpdate requires surfaceId string' }
    }
    const components = parsed.surfaceUpdate.components
    if (components !== undefined && !Array.isArray(components)) {
      return { error: 'A2UI surfaceUpdate.components must be an array when provided' }
    }
    const parsedComponents: ProjectAssistantUIComponent[] = []
    for (const component of components ?? []) {
      const parsedComponent = parseAssistantUIComponent(component)
      if ('error' in parsedComponent) return parsedComponent
      parsedComponents.push(parsedComponent.component)
    }
    ui.surfaceUpdate = {
      surfaceId: parsed.surfaceUpdate.surfaceId,
      components: parsedComponents,
    }
    return { event: ui }
  }

  if (envelopeKey === 'dataModelUpdate') {
    if (!isRecord(parsed.dataModelUpdate)) {
      return { error: 'A2UI dataModelUpdate must be an object' }
    }
    const dataModelExtra = unknownKeys(parsed.dataModelUpdate, allowedAssistantUIDataModelUpdateKeys)
    if (dataModelExtra.length > 0) {
      return { error: `A2UI dataModelUpdate has unknown keys: ${dataModelExtra.join(', ')}` }
    }
    if (!isString(parsed.dataModelUpdate.surfaceId)) {
      return { error: 'A2UI dataModelUpdate requires surfaceId string' }
    }
    const contents = parsed.dataModelUpdate.contents
    if (contents !== undefined && !Array.isArray(contents)) {
      return { error: 'A2UI dataModelUpdate.contents must be an array when provided' }
    }
    const parsedContents: NonNullable<NonNullable<ProjectAssistantUIEvent['dataModelUpdate']>['contents']> = []
    for (const content of contents ?? []) {
      if (!isRecord(content)) {
        return { error: 'A2UI dataModelUpdate.contents entries must be objects' }
      }
      const contentExtra = unknownKeys(content, allowedAssistantUIDataContentKeys)
      if (contentExtra.length > 0) {
        return { error: `A2UI dataModelUpdate.contents[] has unknown keys: ${contentExtra.join(', ')}` }
      }
      if (!isString(content.key)) {
        return { error: 'A2UI dataModelUpdate.contents[] requires key string' }
      }
      if (content.valueString !== undefined && !isString(content.valueString)) {
        return { error: 'A2UI dataModelUpdate.contents[].valueString must be a string when provided' }
      }
      if (content.append !== undefined && typeof content.append !== 'boolean') {
        return { error: 'A2UI dataModelUpdate.contents[].append must be a boolean when provided' }
      }
      parsedContents.push({
        key: content.key,
        ...(content.valueString !== undefined ? { valueString: content.valueString } : {}),
        ...(content.append !== undefined ? { append: content.append } : {}),
      })
    }
    ui.dataModelUpdate = {
      surfaceId: parsed.dataModelUpdate.surfaceId,
      contents: parsedContents,
    }
    return { event: ui }
  }

  if (!isRecord(parsed.interruptRequest)) {
    return { error: 'A2UI interruptRequest must be an object when provided' }
  }
  const interruptExtra = unknownKeys(parsed.interruptRequest, allowedAssistantUIInterruptRequestKeys)
  if (interruptExtra.length > 0) {
    return { error: `A2UI interruptRequest has unknown keys: ${interruptExtra.join(', ')}` }
  }
  if (!isString(parsed.interruptRequest.interruptId)) {
    return { error: 'A2UI interruptRequest requires interruptId string' }
  }
  if (parsed.interruptRequest.kind !== undefined &&
    parsed.interruptRequest.kind !== 'permission' &&
    parsed.interruptRequest.kind !== 'follow_up') {
    return { error: 'A2UI interruptRequest.kind must be permission or follow_up when provided' }
  }
  if (parsed.interruptRequest.status !== undefined &&
    parsed.interruptRequest.status !== 'pending' &&
    parsed.interruptRequest.status !== 'resolved') {
    return { error: 'A2UI interruptRequest.status must be pending or resolved when provided' }
  }
  if (parsed.interruptRequest.description !== undefined && !isString(parsed.interruptRequest.description)) {
    return { error: 'A2UI interruptRequest.description must be string when provided' }
  }
  if (parsed.interruptRequest.surfaceId !== undefined && !isString(parsed.interruptRequest.surfaceId)) {
    return { error: 'A2UI interruptRequest.surfaceId must be string when provided' }
  }
  if (parsed.interruptRequest.questions !== undefined) {
    if (!Array.isArray(parsed.interruptRequest.questions) || !parsed.interruptRequest.questions.every(isString)) {
      return { error: 'A2UI interruptRequest.questions must be a string array when provided' }
    }
  }
  const parsedInterrupt: NonNullable<ProjectAssistantUIEvent['interruptRequest']> = {
    interruptId: parsed.interruptRequest.interruptId,
    kind: parsed.interruptRequest.kind as NonNullable<ProjectAssistantUIEvent['interruptRequest']>['kind'],
    surfaceId: parsed.interruptRequest.surfaceId,
    description: parsed.interruptRequest.description,
    questions: parsed.interruptRequest.questions as string[] | undefined,
    status: parsed.interruptRequest.status as NonNullable<ProjectAssistantUIEvent['interruptRequest']>['status'],
  }
  if (parsed.interruptRequest.action !== undefined) {
    if (!isRecord(parsed.interruptRequest.action)) {
      return { error: 'A2UI interruptRequest.action must be an object when provided' }
    }
    const actionExtra = unknownKeys(parsed.interruptRequest.action, allowedAssistantUIInterruptActionKeys)
    if (actionExtra.length > 0) {
      return { error: `A2UI interruptRequest.action has unknown keys: ${actionExtra.join(', ')}` }
    }
    if (!isString(parsed.interruptRequest.action.runId) || !isString(parsed.interruptRequest.action.requestId)) {
      return { error: 'A2UI interruptRequest.action requires runId and requestId strings' }
    }
    if (
      parsed.interruptRequest.action.assistantMessageId !== undefined &&
      !isString(parsed.interruptRequest.action.assistantMessageId)
    ) {
      return { error: 'A2UI interruptRequest.action.assistantMessageId must be string when provided' }
    }
    parsedInterrupt.action = {
      runId: parsed.interruptRequest.action.runId,
      requestId: parsed.interruptRequest.action.requestId,
      ...(parsed.interruptRequest.action.assistantMessageId !== undefined
        ? { assistantMessageId: parsed.interruptRequest.action.assistantMessageId }
        : {}),
    }
  }
  ui.interruptRequest = parsedInterrupt
  return { event: ui }
}

function parseAssistantUIComponent(raw: unknown): { component: ProjectAssistantUIComponent } | { error: string } {
  if (!isRecord(raw)) {
    return { error: 'A2UI surfaceUpdate.components entries must be objects' }
  }
  const componentExtra = unknownKeys(raw, allowedAssistantUISurfaceComponentKeys)
  if (componentExtra.length > 0) {
    return { error: `A2UI surfaceUpdate.components[] has unknown keys: ${componentExtra.join(', ')}` }
  }
  if (!isString(raw.id)) {
    return { error: 'A2UI surfaceUpdate.components[] requires id string' }
  }
  if (!isRecord(raw.component)) {
    return { error: 'A2UI surfaceUpdate.components[].component must be an object' }
  }
  const rawComponent = raw.component
  const valueExtra = unknownKeys(rawComponent, allowedAssistantUIComponentValueKeys)
  if (valueExtra.length > 0) {
    return { error: `A2UI surfaceUpdate.components[].component has unknown keys: ${valueExtra.join(', ')}` }
  }
  const componentKinds = ['Text', 'Column', 'Card', 'Row'].filter((key) => rawComponent[key] !== undefined)
  if (componentKinds.length !== 1) {
    return { error: `A2UI surfaceUpdate.components[].component must include exactly one component type, got ${componentKinds.join(', ') || 'none'}` }
  }
  const component: ProjectAssistantUIComponent = { id: raw.id, component: {} }
  const kind = componentKinds[0]
  if (kind === 'Text') {
    if (!isRecord(rawComponent.Text)) {
      return { error: 'A2UI Text component must be an object' }
    }
    const text = rawComponent.Text
    const textExtra = unknownKeys(text, allowedAssistantUITextComponentKeys)
    if (textExtra.length > 0) {
      return { error: `A2UI Text component has unknown keys: ${textExtra.join(', ')}` }
    }
    if (text.value !== undefined && !isString(text.value)) {
      return { error: 'A2UI Text component value must be a string when provided' }
    }
    if (text.dataKey !== undefined && !isString(text.dataKey)) {
      return { error: 'A2UI Text component dataKey must be a string when provided' }
    }
    if (text.usageHint !== undefined && !isString(text.usageHint)) {
      return { error: 'A2UI Text component usageHint must be a string when provided' }
    }
    component.component.Text = {
      ...(text.value !== undefined ? { value: text.value } : {}),
      ...(text.dataKey !== undefined ? { dataKey: text.dataKey } : {}),
      ...(text.usageHint !== undefined ? { usageHint: text.usageHint } : {}),
    }
    return { component }
  }

  const container = rawComponent[kind]
  if (!isRecord(container)) {
    return { error: `A2UI ${kind} component must be an object` }
  }
  const containerExtra = unknownKeys(container, allowedAssistantUIContainerComponentKeys)
  if (containerExtra.length > 0) {
    return { error: `A2UI ${kind} component has unknown keys: ${containerExtra.join(', ')}` }
  }
  const children = container.children
  if (!Array.isArray(children) || !children.every(isString)) {
    return { error: `A2UI ${kind} component requires children string array` }
  }
  if (kind === 'Column') component.component.Column = { children }
  if (kind === 'Card') component.component.Card = { children }
  if (kind === 'Row') component.component.Row = { children }
  return { component }
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
    let eventType: string | undefined
    let data = ''
    for (const line of lines) {
      if (line.startsWith('event:')) {
        eventType = line.slice(6).trim()
      } else if (line.startsWith('data:')) {
        data = data ? `${data}\n${line.slice(5).trimStart()}` : line.slice(5).trimStart()
      }
    }
    if (!data) return
    const parsed = parseProjectMessageStreamEvent(data, eventType)
    if (parsed.error) {
      onEvent({ type: 'run_failed', error: parsed.error })
      return
    }
    if (parsed.event) onEvent(parsed.event)
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

  async resumeAssistantRun(
    ctx: KedgeContext | null,
    name: string,
    runID: string,
    body: { requestID: string; decision?: 'allow' | 'deny'; answer?: string; assistantMessageID?: string },
  ): Promise<ProjectAssistantResumeResponse> {
    return request<ProjectAssistantResumeResponse>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/${encodeURIComponent(runID)}/resume`,
      body,
    )
  },

  async abortAssistantRun(ctx: KedgeContext | null, name: string, runID: string): Promise<ProjectAssistantResumeResponse> {
    return request<ProjectAssistantResumeResponse>(
      ctx,
      'POST',
      `${baseURL(ctx)}/${encodeURIComponent(name)}/assistant/${encodeURIComponent(runID)}/abort`,
    )
  },

  async getMemory(ctx: KedgeContext | null, name: string): Promise<ProjectMemory> {
    return request<ProjectMemory>(ctx, 'GET', `${baseURL(ctx)}/${encodeURIComponent(name)}/memory`)
  },
}
