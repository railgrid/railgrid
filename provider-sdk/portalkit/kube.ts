// CANONICAL SOURCE — provider-sdk/portalkit. Do not edit vendored copies under
// providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`.
//
// Minimal Kubernetes REST client for provider portals that address a tenant
// workspace by kcp cluster ID. Every call goes through the hub's kcp proxy at
// /clusters/<cluster>/..., which authorizes the caller's bearer against their
// workspace membership and forwards the request to kcp as that user. The
// portal never handles the token: the host-owned transport (farosContext.fetch,
// see providerFetch in ./tenant.ts) injects Authorization.
//
// This replaces the former GraphQL gateway data path. It speaks plain
// Kubernetes wire shapes — List envelopes with continue/remainingItemCount,
// Status bodies on failure, DeleteOptions preconditions, server-side apply —
// so a portal gets exactly the semantics kubectl would, with no schema
// translation layer in between.
//
// Framework-agnostic plain TS; synced to both the vanilla-TS and Vue kits.

// ProviderFetch mirrors the transport type in ./tenant.ts (the host-owned
// farosContext.fetch). Declared locally so this file has no cross-file import
// and type-checks under both bundler and NodeNext module resolution.
export type ProviderFetch = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>

// KubeResourceRef identifies an API resource. `resource` is the plural REST
// segment (e.g. "instances"), not the Kind. The core group is "".
export interface KubeResourceRef {
  group: string
  version: string
  resource: string
  namespaced?: boolean
}

export interface KubeObjectMeta {
  name: string
  namespace?: string
  uid?: string
  resourceVersion?: string
  generation?: number
  creationTimestamp?: string
  deletionTimestamp?: string
  labels?: Record<string, string>
  annotations?: Record<string, string>
  ownerReferences?: Array<Record<string, unknown>>
  finalizers?: string[]
  [key: string]: unknown
}

export interface KubeObject {
  apiVersion?: string
  kind?: string
  metadata: KubeObjectMeta
  spec?: unknown
  status?: unknown
  [key: string]: unknown
}

export interface KubeListOptions {
  namespace?: string
  labelSelector?: string
  fieldSelector?: string
  limit?: number
  continue?: string
}

// KubeList mirrors a Kubernetes List response. `continue` is undefined (never
// the empty string) on a terminal page so callers can branch on truthiness.
export interface KubeList<T extends KubeObject = KubeObject> {
  apiVersion?: string
  kind?: string
  items: T[]
  continue?: string
  remainingItemCount?: number
  resourceVersion?: string
}

export interface KubeStatus {
  kind: 'Status'
  apiVersion?: string
  status: 'Success' | 'Failure'
  message?: string
  reason?: string
  code?: number
  details?: {
    name?: string
    group?: string
    kind?: string
    uid?: string
    causes?: Array<{ reason?: string; message?: string; field?: string }>
  }
}

// KubeError carries the HTTP status plus the decoded Kubernetes Status body,
// when the server sent one. `reason` follows metav1.StatusReason
// ("NotFound", "AlreadyExists", "Conflict", "Forbidden", "Unauthorized",
// "Invalid", "BadRequest", ...) and falls back to an HTTP-derived reason when
// the body is not a Status (e.g. a hub-side 502).
export class KubeError extends Error {
  readonly name = 'KubeError'
  readonly status: number
  readonly reason: string
  readonly body: KubeStatus | null
  readonly method: string
  readonly path: string

  constructor(method: string, path: string, status: number, body: KubeStatus | null, fallbackMessage: string) {
    const message = body?.message?.trim() || fallbackMessage || `${method} ${path} failed with HTTP ${status}`
    super(message)
    this.method = method
    this.path = path
    this.status = status
    this.body = body
    this.reason = body?.reason?.trim() || reasonForHTTPStatus(status)
  }
}

function reasonForHTTPStatus(status: number): string {
  switch (status) {
    case 400: return 'BadRequest'
    case 401: return 'Unauthorized'
    case 403: return 'Forbidden'
    case 404: return 'NotFound'
    case 405: return 'MethodNotAllowed'
    case 409: return 'Conflict'
    case 410: return 'Gone'
    case 422: return 'Invalid'
    case 429: return 'TooManyRequests'
    case 500: return 'InternalError'
    case 503: return 'ServiceUnavailable'
    case 504: return 'Timeout'
    default: return status >= 500 ? 'ServerError' : 'HTTPError'
  }
}

export function isKubeError(error: unknown): error is KubeError {
  return error instanceof KubeError || (!!error && typeof error === 'object' && (error as { name?: string }).name === 'KubeError')
}
export function isKubeNotFound(error: unknown): boolean {
  return isKubeError(error) && error.status === 404
}
export function isKubeAlreadyExists(error: unknown): boolean {
  return isKubeError(error) && error.reason === 'AlreadyExists'
}
export function isKubeConflict(error: unknown): boolean {
  return isKubeError(error) && error.status === 409
}
export function isKubeForbidden(error: unknown): boolean {
  return isKubeError(error) && error.status === 403
}
// isKubeResourceUnavailable reports a 404 for the *resource type* rather than
// a named object — what kcp returns when the workspace has no APIBinding for
// the provider yet. A named-object miss carries details.name; a type miss
// does not.
export function isKubeResourceUnavailable(error: unknown): boolean {
  if (!isKubeNotFound(error)) return false
  const body = (error as KubeError).body
  if (!body) return true
  if (body.details?.name) return false
  return true
}

// kubeResourcePath builds /clusters/<cluster>/{api|apis/<group>}/<version>
// [/namespaces/<ns>]/<resource>[/<name>][/<subresource>]. Every segment is
// percent-encoded so a caller-supplied name cannot escape its slot.
export function kubeResourcePath(
  cluster: string,
  ref: KubeResourceRef,
  opts: { namespace?: string; name?: string; subresource?: string } = {},
): string {
  if (!cluster) throw new Error('kube client: cluster is required')
  if (!ref.version || !ref.resource) throw new Error('kube client: resource ref needs version and resource')
  const seg = (s: string) => encodeURIComponent(s)
  let path = `/clusters/${seg(cluster)}`
  path += ref.group ? `/apis/${seg(ref.group)}/${seg(ref.version)}` : `/api/${seg(ref.version)}`
  if (opts.namespace) path += `/namespaces/${seg(opts.namespace)}`
  path += `/${seg(ref.resource)}`
  if (opts.name) path += `/${seg(opts.name)}`
  if (opts.subresource) path += `/${seg(opts.subresource)}`
  return path
}

export interface KubeDeleteOptions {
  namespace?: string
  // preconditions.uid / resourceVersion make the delete conditional; kcp
  // answers 409 Conflict when they no longer match.
  preconditions?: { uid?: string; resourceVersion?: string }
  propagationPolicy?: 'Orphan' | 'Background' | 'Foreground'
  gracePeriodSeconds?: number
}

export type KubePatchType = 'merge' | 'json' | 'strategic' | 'apply'

export interface KubePatchOptions {
  namespace?: string
  subresource?: string
  type?: KubePatchType
  // For type "apply": the field manager (defaults to the client's) and
  // whether to take over fields owned by other managers.
  fieldManager?: string
  force?: boolean
}

export interface KubeClientOptions {
  // The host-owned transport (providerFetch(ctx)). Relative paths resolve
  // against the portal origin.
  fetch: ProviderFetch
  // kcp logical cluster ID of the tenant workspace (farosContext.tenant).
  cluster: string
  // Field manager for server-side apply. Defaults to "faros-portal".
  fieldManager?: string
  // Optional hook for callers that fence in-flight requests against a
  // context switch; invoked after the response body is read, before parsing.
  onResponse?: () => void
}

export interface KubeClient {
  readonly cluster: string
  get<T extends KubeObject = KubeObject>(ref: KubeResourceRef, name: string, opts?: { namespace?: string; subresource?: string }): Promise<T>
  list<T extends KubeObject = KubeObject>(ref: KubeResourceRef, opts?: KubeListOptions): Promise<KubeList<T>>
  // listAll walks `continue` until exhaustion. Repeated or runaway cursors are
  // protocol failures rather than silently partial results.
  listAll<T extends KubeObject = KubeObject>(ref: KubeResourceRef, opts?: Omit<KubeListOptions, 'continue'> & { pageSize?: number; maxPages?: number }): Promise<T[]>
  create<T extends KubeObject = KubeObject>(ref: KubeResourceRef, obj: T, opts?: { namespace?: string }): Promise<T>
  // update is a PUT: a full replacement. Include metadata.resourceVersion for
  // optimistic concurrency, omit it for last-writer-wins.
  update<T extends KubeObject = KubeObject>(ref: KubeResourceRef, obj: T, opts?: { namespace?: string; subresource?: string }): Promise<T>
  // apply is server-side apply (create-or-update). The object must carry
  // apiVersion + kind + metadata.name. Fields the manifest omits are left as
  // they are; fields it includes are owned by this manager.
  apply<T extends KubeObject = KubeObject>(ref: KubeResourceRef, obj: T, opts?: { namespace?: string; subresource?: string; fieldManager?: string; force?: boolean }): Promise<T>
  patch<T extends KubeObject = KubeObject>(ref: KubeResourceRef, name: string, body: unknown, opts?: KubePatchOptions): Promise<T>
  delete(ref: KubeResourceRef, name: string, opts?: KubeDeleteOptions): Promise<KubeStatus | KubeObject>
}

const PATCH_CONTENT_TYPE: Record<KubePatchType, string> = {
  merge: 'application/merge-patch+json',
  json: 'application/json-patch+json',
  strategic: 'application/strategic-merge-patch+json',
  // Apply accepts JSON: YAML is a superset, and the server parses the body
  // with a YAML decoder regardless of the JSON-vs-YAML spelling.
  apply: 'application/apply-patch+yaml',
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function isStatus(value: unknown): value is KubeStatus {
  return isRecord(value) && value.kind === 'Status'
}

// normalizeList validates the List envelope and fails closed on malformed
// pagination metadata: a wrong-typed continue token or item count would
// otherwise make a bounded page walk silently stop early or loop, and a
// partial list presented as complete is worse than an error.
function normalizeList<T extends KubeObject>(value: unknown, path: string): KubeList<T> {
  const fail = (message: string) => new KubeError('GET', path, 200, null, `kube client: ${message}`)
  if (!isRecord(value) || !Array.isArray(value.items)) throw fail('list response has no items array')
  if (value.metadata !== undefined && !isRecord(value.metadata)) throw fail('list metadata is not an object')
  const metadata = isRecord(value.metadata) ? value.metadata : {}
  const rawContinue = metadata.continue
  if (rawContinue !== undefined && rawContinue !== null && typeof rawContinue !== 'string') throw fail('list continue token is not a string')
  const continueToken = typeof rawContinue === 'string' && rawContinue.length > 0 ? rawContinue : undefined
  const rawRemaining = metadata.remainingItemCount
  if (rawRemaining !== undefined && rawRemaining !== null && (!Number.isSafeInteger(rawRemaining) || (rawRemaining as number) < 0)) {
    throw fail('list remainingItemCount is not a non-negative integer')
  }
  const remaining = typeof rawRemaining === 'number' ? rawRemaining : undefined
  if (remaining !== undefined && remaining > 0 && continueToken === undefined) throw fail('list reported remaining items without a continue token')
  const rawRV = metadata.resourceVersion
  if (rawRV !== undefined && rawRV !== null && typeof rawRV !== 'string') throw fail('list resourceVersion is not a string')
  return {
    apiVersion: typeof value.apiVersion === 'string' ? value.apiVersion : undefined,
    kind: typeof value.kind === 'string' ? value.kind : undefined,
    items: value.items as T[],
    continue: continueToken,
    remainingItemCount: remaining,
    resourceVersion: typeof rawRV === 'string' ? rawRV : undefined,
  }
}

export function createKubeClient(options: KubeClientOptions): KubeClient {
  const { fetch: transport, cluster } = options
  const defaultManager = options.fieldManager || 'faros-portal'
  if (typeof transport !== 'function') throw new Error('kube client: fetch transport is required')
  if (!cluster) throw new Error('kube client: cluster is required')

  async function request<T>(method: string, path: string, init: { body?: unknown; contentType?: string; query?: Record<string, string | number | undefined> } = {}): Promise<T> {
    const url = new URL(path, 'http://placeholder.invalid')
    for (const [key, value] of Object.entries(init.query ?? {})) {
      if (value !== undefined && value !== '') url.searchParams.set(key, String(value))
    }
    const target = url.pathname + url.search
    const headers: Record<string, string> = { Accept: 'application/json' }
    let body: string | undefined
    if (init.body !== undefined) {
      headers['Content-Type'] = init.contentType ?? 'application/json'
      body = typeof init.body === 'string' ? init.body : JSON.stringify(init.body)
    }
    const res = await transport(target, { method, credentials: 'same-origin', headers, body })
    const text = await res.text()
    options.onResponse?.()
    let parsed: unknown = null
    if (text) {
      try {
        parsed = JSON.parse(text)
      } catch {
        if (res.ok) throw new KubeError(method, target, res.status, null, 'kube client: response was not JSON')
      }
    }
    if (!res.ok) {
      throw new KubeError(method, target, res.status, isStatus(parsed) ? parsed : null, text.trim() || res.statusText)
    }
    return parsed as T
  }

  const client: KubeClient = {
    cluster,

    get(ref, name, opts = {}) {
      if (!name) return Promise.reject(new Error('kube client: name is required'))
      return request(`GET`, kubeResourcePath(cluster, ref, { namespace: opts.namespace, name, subresource: opts.subresource }))
    },

    async list(ref, opts = {}) {
      const path = kubeResourcePath(cluster, ref, { namespace: opts.namespace })
      const raw = await request<unknown>('GET', path, {
        query: {
          labelSelector: opts.labelSelector,
          fieldSelector: opts.fieldSelector,
          limit: opts.limit,
          continue: opts.continue,
        },
      })
      return normalizeList(raw, path)
    },

    async listAll(ref, opts = {}) {
      const pageSize = opts.pageSize ?? 500
      const maxPages = opts.maxPages ?? 100
      const items: KubeObject[] = []
      const seen = new Set<string>()
      let cursor: string | undefined
      for (let page = 0; page < maxPages; page += 1) {
        const result = await client.list(ref, { ...opts, limit: pageSize, continue: cursor })
        items.push(...result.items)
        if (!result.continue) return items as never
        if (seen.has(result.continue)) {
          throw new KubeError('GET', kubeResourcePath(cluster, ref, { namespace: opts.namespace }), 200, null, 'kube client: list repeated a continue token')
        }
        seen.add(result.continue)
        cursor = result.continue
      }
      throw new KubeError('GET', kubeResourcePath(cluster, ref, { namespace: opts.namespace }), 200, null, `kube client: list exceeded ${maxPages} pages`)
    },

    create(ref, obj, opts = {}) {
      const namespace = opts.namespace ?? obj.metadata?.namespace
      return request('POST', kubeResourcePath(cluster, ref, { namespace }), { body: obj })
    },

    update(ref, obj, opts = {}) {
      const name = obj.metadata?.name
      if (!name) return Promise.reject(new Error('kube client: update needs metadata.name'))
      const namespace = opts.namespace ?? obj.metadata?.namespace
      return request('PUT', kubeResourcePath(cluster, ref, { namespace, name, subresource: opts.subresource }), { body: obj })
    },

    apply(ref, obj, opts = {}) {
      const name = obj.metadata?.name
      if (!name) return Promise.reject(new Error('kube client: apply needs metadata.name'))
      if (!obj.apiVersion || !obj.kind) return Promise.reject(new Error('kube client: apply needs apiVersion and kind'))
      return client.patch(ref, name, obj, {
        namespace: opts.namespace ?? obj.metadata?.namespace,
        subresource: opts.subresource,
        type: 'apply',
        fieldManager: opts.fieldManager,
        force: opts.force,
      })
    },

    patch(ref, name, body, opts = {}) {
      if (!name) return Promise.reject(new Error('kube client: name is required'))
      const type = opts.type ?? 'merge'
      const query: Record<string, string | undefined> = {}
      if (type === 'apply') {
        query.fieldManager = opts.fieldManager || defaultManager
        query.force = opts.force === false ? 'false' : 'true'
      }
      return request('PATCH', kubeResourcePath(cluster, ref, { namespace: opts.namespace, name, subresource: opts.subresource }), {
        body,
        contentType: PATCH_CONTENT_TYPE[type],
        query,
      })
    },

    delete(ref, name, opts = {}) {
      if (!name) return Promise.reject(new Error('kube client: name is required'))
      const deleteOptions: Record<string, unknown> = { apiVersion: 'v1', kind: 'DeleteOptions' }
      if (opts.preconditions) deleteOptions.preconditions = opts.preconditions
      if (opts.propagationPolicy) deleteOptions.propagationPolicy = opts.propagationPolicy
      if (opts.gracePeriodSeconds !== undefined) deleteOptions.gracePeriodSeconds = opts.gracePeriodSeconds
      return request('DELETE', kubeResourcePath(cluster, ref, { namespace: opts.namespace, name }), { body: deleteOptions })
    },
  }
  return client
}
