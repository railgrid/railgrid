// Fetch wrappers for /services/providers/kro-multicluster/api/*.
//
// The hub authenticates browser requests with `Authorization: Bearer`
// — not cookies. The portal shell stores its idToken and pushes it to
// us via `kedgeContext.token` when it mounts the custom element;
// App.vue forwards both to setBasePath / setToken below. Without the
// bearer the hub's TenantResolver returns ErrAnonymous and the
// provider's tenant-required endpoints fail with
// "X-Kedge-Tenant header required".

import type { ErrorResponse, Instance, Template } from './types'

let basePath: string = '/services/providers/kro-multicluster'
let bearerToken: string | null = null

// setBasePath is called by App.vue once the kedgeContext arrives so
// the URL prefix matches the hub's actual mount point (the shell
// could mount us at a different prefix in a future deployment).
export function setBasePath(ctxBasePath?: string | null) {
  if (!ctxBasePath) return
  // ctxBasePath is /ui/providers/kro-multicluster from the shell;
  // swap to /services/providers/kro-multicluster for the backend.
  basePath = ctxBasePath.replace(/^\/ui\/providers\//, '/services/providers/')
}

// setToken stashes the bearer token from kedgeContext so subsequent
// requests carry it. Empty / null clears the header so an
// unauthenticated mode still returns a coherent 401 from the hub
// rather than leaking a stale token across sessions.
export function setToken(token?: string | null) {
  bearerToken = token || null
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (body) headers['Content-Type'] = 'application/json'
  if (bearerToken) headers['Authorization'] = 'Bearer ' + bearerToken
  const res = await fetch(basePath + path, {
    method,
    credentials: 'same-origin',
    headers,
    body: body ? JSON.stringify(body) : undefined,
  })
  const text = await res.text()
  const parsed = text ? JSON.parse(text) : null
  if (!res.ok) {
    const err: ErrorResponse = parsed && parsed.reason
      ? parsed
      : { reason: 'HTTPError', message: text || res.statusText }
    throw err
  }
  return parsed as T
}

export const api = {
  listTemplates(filter: { category?: string; cloud?: string } = {}): Promise<{ items: Template[] }> {
    const q = new URLSearchParams()
    if (filter.category) q.set('category', filter.category)
    if (filter.cloud) q.set('cloud', filter.cloud)
    const suffix = q.toString() ? '?' + q.toString() : ''
    return request('GET', '/api/templates' + suffix)
  },
  getTemplate(name: string, version?: string): Promise<{ template: Template }> {
    const q = version ? '?version=' + encodeURIComponent(version) : ''
    return request('GET', '/api/templates/' + encodeURIComponent(name) + q)
  },
  createInstance(body: { templateName: string; templateVersion?: string; name: string; values: Record<string, unknown> }): Promise<Instance> {
    return request('POST', '/api/instances', body)
  },
  listInstances(): Promise<{ items: Instance[] }> {
    return request('GET', '/api/instances')
  },
  getInstance(name: string): Promise<Instance> {
    return request('GET', '/api/instances/' + encodeURIComponent(name))
  },
  deleteInstance(name: string): Promise<void> {
    return request('DELETE', '/api/instances/' + encodeURIComponent(name))
  },
}
