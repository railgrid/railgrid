// ApiClient wraps the agents provider REST API. It holds a reference to the
// live KedgeContext (the host sets it as a property on the element, which then
// hands it here) and reads the active tenant from localStorage — exactly as the
// monolithic element did. Every call carries the Bearer token plus the
// X-Kedge-Org / X-Kedge-Workspace headers the backend requires.

import type { KedgeContext } from './types'
import { readTenant, hasWorkspace as sharedHasWorkspace, serviceBase, tenantHeaders, type Tenant } from './portalkit/tenant'

export type { Tenant }

// SSE frame parsed from the streaming chat endpoint.
export interface ChatEvent {
  event: string
  data: any
}

export class ApiClient {
  private ctx: KedgeContext | null = null

  setContext(ctx: KedgeContext | null): void {
    this.ctx = ctx
  }

  // The host passes basePath as /ui/providers/agents; the API lives under the
  // service-proxy path (portalkit/tenant.serviceBase rewrites the prefix).
  private url(path: string): string {
    return serviceBase(this.ctx?.basePath || '/ui/providers/agents') + path
  }

  tenant(): Tenant {
    return readTenant()
  }

  hasWorkspace(): boolean {
    return sharedHasWorkspace()
  }

  // tenantKey identifies the current workspace for change-detection (load dedupe).
  tenantKey(): string {
    return this.ctx?.tenant || JSON.stringify(this.tenant())
  }

  private headers(hasBody: boolean): Record<string, string> {
    return tenantHeaders({ token: this.ctx?.token, json: hasBody })
  }

  async get<T>(path: string): Promise<T> {
    const r = await fetch(this.url(path), { credentials: 'same-origin', headers: this.headers(false) })
    if (!r.ok) throw new Error(`${r.status} ${(await r.json().catch(() => ({})))?.message || r.statusText}`)
    return r.json()
  }

  async send<T>(method: string, path: string, body?: unknown): Promise<T> {
    const r = await fetch(this.url(path), {
      method,
      credentials: 'same-origin',
      headers: this.headers(body !== undefined),
      body: body !== undefined ? JSON.stringify(body) : undefined,
    })
    if (!r.ok) throw new Error(`${r.status} ${(await r.json().catch(() => ({})))?.message || r.statusText}`)
    return (r.status === 204 ? (undefined as unknown) : r.json()) as Promise<T>
  }

  // list<T> unwraps the standard { items: [...] } list envelope, tolerating a
  // missing body.
  async list<T>(path: string): Promise<T[]> {
    const res = await this.get<{ items?: T[] }>(path)
    return res.items || []
  }

  // chatStream POSTs a message and yields parsed SSE frames as they arrive. The
  // caller drives the loop and applies deltas/tool rows/errors. Throws on a
  // non-OK response or a missing body.
  async *chatStream(agent: string, message: string, sessionID: string): AsyncGenerator<ChatEvent> {
    const r = await fetch(this.url(`/api/agents/${encodeURIComponent(agent)}/chat`), {
      method: 'POST',
      credentials: 'same-origin',
      headers: this.headers(true),
      body: JSON.stringify({ message, sessionID: sessionID || undefined }),
    })
    if (!r.ok || !r.body) throw new Error(`${r.status} ${(await r.json().catch(() => ({})))?.message || r.statusText}`)
    const reader = r.body.getReader()
    const dec = new TextDecoder()
    let buf = ''
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buf += dec.decode(value, { stream: true })
      const frames = buf.split('\n\n')
      buf = frames.pop() || ''
      for (const f of frames) {
        const ev = parseSSE(f)
        if (ev) yield ev
      }
    }
  }
}

function parseSSE(frame: string): ChatEvent | null {
  let event = 'message'
  let data = ''
  for (const line of frame.split('\n')) {
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) data += line.slice(5).trim()
  }
  if (!data) return null
  try {
    return { event, data: JSON.parse(data) }
  } catch {
    return { event, data }
  }
}
