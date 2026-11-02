import type { ProviderBundle } from './providerBundle'
import type { AcceptedComposition, ClaimState, ProviderRequirement } from '@/stores/providers'

export interface IsolatedProviderContext {
  providerName: string
  apiGroups: string[]
  dependencies: ProviderSandboxDependency[]
  tenant: string | null
  orgUUID: string | null
  workspaceUUID: string | null
  theme: 'light' | 'dark'
  subPath?: string
  user: { email?: string; userId?: string } | null
  navigationBasePath: string
  basePath: string
  agentOverlay?: {
    selectedAgent: string | null
    settingsOpen: boolean
    followContext: boolean
  }
}

export interface ProviderSandboxDependency {
  group: string
  resource: string
  verbs: string[]
  selector?: Record<string, string>
}

export function acceptedSandboxDependencies(
  requirements: ProviderRequirement[] | undefined,
  claims: ClaimState | undefined,
  grantedCompositions: AcceptedComposition[] | undefined,
): ProviderSandboxDependency[] {
  if (!claims || claims.mismatch) return []
  const unsettledClaims = [...(claims?.pending || []), ...(claims?.rejected || [])]
  const acceptedClaims = claims.accepted || []
  const dependencies: ProviderSandboxDependency[] = []
  const dataVerbs = ['get', 'list', 'watch', 'create', 'update', 'patch', 'delete']
  const verbsFor = (verbs: string[] | undefined): string[] =>
    verbs?.includes('*') ? dataVerbs :
      [...new Set((verbs || []).map(verb => verb.toLowerCase()).filter(verb => dataVerbs.includes(verb)))]
  for (const requirement of requirements || []) {
    const group = requirement.group || ''
    if (group === 'authorization.k8s.io') continue
    for (const resource of requirement.resources || []) {
      const accepted = acceptedClaims.find(claim => (claim.group || '') === group && claim.resource === resource.name)
      if (!accepted?.verbs?.length || !accepted.selector ||
          unsettledClaims.some(claim => (claim.group || '') === group && claim.resource === resource.name)) continue
      if (requirement.provider && !(grantedCompositions || []).some(grant =>
        grant.provider === requirement.provider && grant.group === group && grant.resource === resource.name)) continue
      if (!group && resource.name !== 'secrets') continue
      let selector: Record<string, string> | undefined
      if (!group && resource.name === 'secrets') {
        const approvedLabels = accepted.selector.matchLabels
        const declaredLabels = resource.selector?.matchLabels
        if (!approvedLabels || !Object.keys(approvedLabels).length || !declaredLabels || !Object.keys(declaredLabels).length) continue
        if (Object.entries(approvedLabels).some(([key, value]) => key in declaredLabels && declaredLabels[key] !== value)) continue
        // Both restrictions apply. A changed declaration cannot replace the
        // labels approved by the workspace, even before kcp reports mismatch.
        selector = { ...approvedLabels, ...declaredLabels }
      } else if (!accepted.selector.matchAll ||
        Object.keys(accepted.selector.matchLabels || {}).length || resource.selector) {
        // Selected foreign resources need object filtering, which this bridge
        // does not implement. Never turn a selected claim into broad access.
        continue
      }
      const approvedVerbs = verbsFor(accepted.verbs)
      const verbs = resource.name.includes('/')
        ? (accepted.verbs.includes('*') || approvedVerbs.includes('create') ? ['subresource'] : [])
        : verbsFor(resource.verbs).filter(verb => approvedVerbs.includes(verb))
      if (!verbs.length) continue
      dependencies.push({
        group,
        resource: resource.name,
        verbs,
        ...(selector ? { selector } : {}),
      })
    }
  }
  return dependencies
}

export interface ProviderSandboxCallbacks {
  isCurrent(): boolean
  scope(): { tenant: string | null; orgUUID: string | null; workspaceUUID: string | null }
  getToken(): Promise<string | null>
  onUnauthorized(): void
  onNavigate(path: string, replace: boolean): void
  onLayoutChange(fullBleed: boolean): void
  onRouteReady(): void
  onPublishPageResource(value: unknown): void
  onPublishAgentContext(value: unknown): void
  onAgentOverlay(action: string, value?: unknown): void
  onRetry(): void
  onReady(): void
  onError(error: Error): void
}

const MAX_BUNDLE_BYTES = 12 * 1024 * 1024
const MAX_REQUEST_BODY_BYTES = 8 * 1024 * 1024
const MAX_INFLIGHT_REQUEST_BODY_BYTES = 16 * 1024 * 1024
const MAX_INFLIGHT_REQUESTS = 16
const MAX_INFLIGHT_RESPONSE_BYTES = 64 * 1024 * 1024
const MAX_ACK_WAIT_MS = 30_000
const MAX_RESPONSE_CHUNK_BYTES = 128 * 1024
const MAX_RESPONSE_BYTES = 32 * 1024 * 1024
const FETCH_METHODS = new Set(['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE'])
const UI_METHODS = new Set(['GET', 'HEAD'])
const SAFE_REQUEST_HEADERS = new Set([
  'accept', 'accept-language', 'cache-control', 'content-type', 'if-match',
  'if-none-match', 'if-modified-since', 'if-unmodified-since', 'last-event-id',
  'prefer', 'range', 'x-requested-with',
])
const PRIVATE_RESPONSE_HEADERS = new Set([
  'authorization', 'proxy-authenticate', 'proxy-authorization', 'set-cookie', 'set-cookie2',
  'x-railgrid-cluster', 'x-railgrid-tenant', 'x-railgrid-user', 'x-remote-user',
])

function safePathname(pathname: string): boolean {
  // Kubernetes route coordinates use a canonical, unescaped grammar. Applying
  // different decoding rules in authorization and response filtering can skip
  // a selected-object check, so reject alternate encoded spellings outright.
  if (!pathname.startsWith('/') || pathname.includes('//') || pathname.includes('\\') || pathname.includes('%')) return false
  for (const segment of pathname.split('/').slice(1)) {
    if (!segment) return false
    let decoded: string
    try { decoded = decodeURIComponent(segment) } catch { return false }
    if (decoded === '.' || decoded === '..' || decoded.includes('/') || decoded.includes('\\')) return false
  }
  return true
}

export function isProviderSandboxRequestAllowed(
  input: string | URL,
  origin: string,
  context: Pick<IsolatedProviderContext, 'providerName' | 'apiGroups' | 'tenant' | 'dependencies'>,
  method: string,
): boolean {
  let url: URL
  try { url = new URL(String(input), origin) } catch { return false }
  const verb = method.toUpperCase()
  if (url.origin !== origin || !['http:', 'https:'].includes(url.protocol) || !safePathname(url.pathname)) return false

  const provider = encodeURIComponent(context.providerName)
  const uiPrefix = `/ui/providers/${provider}/`
  if (url.pathname.startsWith(uiPrefix)) return UI_METHODS.has(verb)

  const serviceRoot = `/services/providers/${provider}/`
  if (url.pathname.startsWith(serviceRoot)) {
    const relative = url.pathname.slice(serviceRoot.length)
    if (relative === 'readyz' || relative === 'healthz') return UI_METHODS.has(verb)
    if (relative === 'mcp' || relative === 'mcp/sse') {
      return relative === 'mcp/sse' ? verb === 'GET' : ['GET', 'POST', 'DELETE'].includes(verb)
    }
    return false
  }

  if (!context.tenant || !FETCH_METHODS.has(verb)) return false
  const clusterRoute = /^\/clusters\/([^/]+)\/(apis|api)\/(.+)$/.exec(url.pathname)
  if (!clusterRoute) return false
  let clusterName: string
  try { clusterName = decodeURIComponent(clusterRoute[1]) } catch { return false }
  if (clusterName !== context.tenant) return false
  if (clusterRoute[2] === 'apis') {
    const parts = clusterRoute[3].split('/')
    const group = decodeURIComponent(parts[0] || '')
    if (context.apiGroups.includes(group)) return true
    return dependencyPathAllowed(parts.slice(1), group, context.dependencies, verb, url.searchParams)
  }
  return dependencyPathAllowed(clusterRoute[3].split('/'), '', context.dependencies, verb, url.searchParams)
}

function isKubernetesAPIURL(url: URL): boolean {
  return /^\/clusters\/[^/]+\/(?:apis|api)\/.+/.test(url.pathname)
}

function providerSandboxKubernetesURL(url: URL, context: IsolatedProviderContext): URL | null {
  if (!context.orgUUID || !context.workspaceUUID || !context.providerName || !isKubernetesAPIURL(url)) return null
  const path = url.pathname.slice(1)
  const routed = new URL(
    `/api/orgs/${encodeURIComponent(context.orgUUID)}/workspaces/${encodeURIComponent(context.workspaceUUID)}` +
    `/providers/${encodeURIComponent(context.providerName)}/sandbox/${path}${url.search}`,
    url.origin,
  )
  return routed
}

function capabilityFingerprint(context: IsolatedProviderContext): string {
  const dependencies = context.dependencies.map(dependency => ({
    group: dependency.group,
    resource: dependency.resource,
    verbs: [...dependency.verbs].sort(),
    selector: dependency.selector ? Object.fromEntries(Object.entries(dependency.selector).sort(([a], [b]) => a.localeCompare(b))) : undefined,
  })).sort((a, b) => a.group.localeCompare(b.group) || a.resource.localeCompare(b.resource))
  return JSON.stringify({ apiGroups: [...context.apiGroups].sort(), dependencies })
}

function dependencyPathAllowed(
  apiPath: string[],
  group: string,
  dependencies: ProviderSandboxDependency[],
  method: string,
  searchParams: URLSearchParams,
): boolean {
  if (apiPath.length < 2 || !apiPath[0]) return false
  const route = apiPath.slice(1).map(part => {
    try { return decodeURIComponent(part) } catch { return '' }
  })
  if (route.some(part => !part)) return false
  const nsIndex = route[0] === 'namespaces' ? 2 : 0
  const resource = route[nsIndex]
  if (!resource) return false
  const resourceVerb = route[nsIndex + 2]
  for (const dependency of dependencies) {
    if (dependency.group !== group) continue
    const [requiredResource, requiredVerb] = dependency.resource.split('/', 2)
    if (requiredResource !== resource) continue
    if (requiredVerb) {
      if (!dependency.verbs.includes('subresource') || method !== 'POST' ||
          route.length !== nsIndex + 3 || route[nsIndex + 1] === undefined || resourceVerb !== requiredVerb) continue
      return true
    }
    // Core Secrets is name-only: no collection list/watch, no mutations and
    // no other namespace can use this browser capability.
    if (!group && resource === 'secrets') {
      if (method !== 'GET' || !dependency.verbs.includes('get') ||
          route[0] !== 'namespaces' || route[1] !== 'default' || route.length !== 4 || !route[3] ||
          searchParams.has('watch')) continue
      if (!dependency.selector || Object.keys(dependency.selector).length === 0) continue
      return true
    }
    const baseLength = nsIndex + 1
    const collection = route.length === baseLength
    const named = route.length === baseLength + 1
    if (!collection && !named) continue
    if (route.includes('status') || route.includes('scale')) continue
    const verbs = new Set(dependency.verbs.map(verb => verb.toLowerCase()))
    if (method === 'GET') {
      if (searchParams.has('watch') && searchParams.get('watch') !== 'false') {
        if (searchParams.get('watch') !== 'true' || !verbs.has('watch')) continue
        return true
      }
      if (collection ? verbs.has('list') : verbs.has('get')) return true
    } else if (method === 'POST' && collection && verbs.has('create')) return true
    else if (method === 'PUT' && named && verbs.has('update')) return true
    else if (method === 'PATCH' && named && verbs.has('patch')) return true
    else if (method === 'DELETE' && named && verbs.has('delete')) return true
  }
  return false
}

function declaredSecretSelector(
  pathname: string,
  context: Pick<IsolatedProviderContext, 'tenant' | 'dependencies'>,
  method: string,
): Record<string, string> | undefined {
  if (method !== 'GET' || !context.tenant) return undefined
  const match = /^\/clusters\/([^/]+)\/api\/v1\/namespaces\/default\/secrets\/([^/]+)$/.exec(pathname)
  if (!match) return undefined
  try { if (decodeURIComponent(match[1]) !== context.tenant) return undefined } catch { return undefined }
  return context.dependencies.find(dependency =>
    dependency.group === '' && dependency.resource === 'secrets' &&
    dependency.verbs.includes('get') && dependency.selector && Object.keys(dependency.selector).length > 0,
  )?.selector
}

export async function verifyProviderBundleIntegrity(bytes: ArrayBuffer, integrity: string): Promise<boolean> {
  const match = /^sha384-([A-Za-z0-9+/]+={0,2})$/.exec(integrity)
  if (!match || bytes.byteLength === 0 || bytes.byteLength > MAX_BUNDLE_BYTES) return false
  const digest = await crypto.subtle.digest('SHA-384', bytes)
  let binary = ''
  for (const byte of new Uint8Array(digest)) binary += String.fromCharCode(byte)
  return `sha384-${btoa(binary)}` === integrity
}

export async function fetchVerifiedProviderBundle(
  bundle: ProviderBundle,
  providerName: string,
  fetchImpl: (url: string, init: RequestInit) => Promise<Response>,
  origin = window.location.origin,
  signal?: AbortSignal,
): Promise<{ bytes: ArrayBuffer; assetBase: string }> {
  if (!bundle.src || !bundle.integrity) throw new Error('Bring-your-own provider bundle has no signed grant or integrity pin.')
  const url = new URL(bundle.src, origin)
  if (url.origin !== origin || url.pathname !== `/ui/providers/${encodeURIComponent(providerName)}/main.js` ||
      !url.searchParams.get('grant')) {
    throw new Error('The provider bundle grant did not resolve to its own same-origin bundle URL.')
  }
  const response = await fetchImpl(url.toString(), { method: 'GET', cache: 'no-store', redirect: 'error', signal })
  if (!response.ok || response.redirected || new URL(response.url || url.toString(), origin).origin !== origin) {
    throw new Error(`Could not fetch the provider bundle (${response.status}).`)
  }
  const bytes = await response.arrayBuffer()
  if (!await verifyProviderBundleIntegrity(bytes, bundle.integrity)) {
    throw new Error('The provider bundle did not match the hub integrity pin.')
  }
  if (bytes.byteLength > MAX_BUNDLE_BYTES) throw new Error('The provider bundle exceeds the 12 MiB safety limit.')
  return { bytes, assetBase: url.toString() }
}

function requestHeaderEntries(input: unknown): [string, string][] {
  if (!Array.isArray(input)) return []
  return input.filter((pair): pair is [string, string] =>
    Array.isArray(pair) && typeof pair[0] === 'string' && typeof pair[1] === 'string' &&
    SAFE_REQUEST_HEADERS.has(pair[0].toLowerCase()))
}

function errorForMessage(message: string, name = 'TypeError'): Error {
  const error = new Error(message)
  error.name = name
  return error
}

interface InflightRequest {
  controller: AbortController
  cancelled?: boolean
  reader?: ReadableStreamDefaultReader<Uint8Array>
  ack?: { seq: number; resolve(): void }
  requestBytes?: number
  responseBytes?: number
}

/** Host half of the BYO provider MessagePort protocol. */
export function createProviderSandboxBridge(
  port: MessagePort,
  context: IsolatedProviderContext,
  assetBase: string,
  callbacks: Pick<ProviderSandboxCallbacks, 'isCurrent' | 'scope' | 'getToken' | 'onUnauthorized' | 'onNavigate' | 'onLayoutChange' | 'onRouteReady' | 'onPublishPageResource' | 'onPublishAgentContext' | 'onAgentOverlay' | 'onRetry'>,
  fetchImpl: typeof fetch = fetch,
) {
  const inflight = new Map<string, InflightRequest>()
  let inflightRequestBytes = 0
  let inflightResponseBytes = 0
  let active = { tenant: context.tenant, orgUUID: context.orgUUID, workspaceUUID: context.workspaceUUID }
  let activeCapabilities = capabilityFingerprint(context)
  let closed = false

  const current = () => !closed && callbacks.isCurrent() &&
    JSON.stringify(callbacks.scope()) === JSON.stringify(active)

  const reply = (message: Record<string, unknown>, transfer?: Transferable[]) => {
    if (!closed) port.postMessage(message, transfer || [])
  }

  const cancelAll = () => {
    for (const [id, request] of inflight) {
      reply({ type: 'fetch-error', id, message: 'Provider request was cancelled because its authority changed.', name: 'AbortError' })
      request.controller.abort()
      request.cancelled = true
      void request.reader?.cancel().catch(() => undefined)
      request.ack?.resolve()
      inflightRequestBytes -= request.requestBytes || 0
      inflightResponseBytes -= request.responseBytes || 0
      request.requestBytes = 0
      request.responseBytes = 0
    }
    inflight.clear()
  }

  async function performFetch(message: Record<string, unknown>) {
    const id = typeof message.id === 'string' ? message.id : ''
    if (!/^[A-Za-z0-9_-]{1,128}$/.test(id) || inflight.has(id) || !current()) return
    if (inflight.size >= MAX_INFLIGHT_REQUESTS) {
      reply({ type: 'fetch-error', id, message: 'Too many provider requests are active.', name: 'QuotaExceededError' })
      return
    }
    const urlText = typeof message.url === 'string' ? message.url : ''
    if (urlText.length > 4096) {
      reply({ type: 'fetch-error', id, message: 'Provider request URL exceeds the safety limit.', name: 'SecurityError' })
      return
    }
    const method = typeof message.method === 'string' ? message.method.toUpperCase() : 'GET'
    let url: URL
    try { url = new URL(urlText, window.location.origin) } catch {
      reply({ type: 'fetch-error', id, message: 'Provider request URL is invalid.', name: 'SecurityError' })
      return
    }
    if (!isProviderSandboxRequestAllowed(url, window.location.origin, context, method)) {
      reply({ type: 'fetch-error', id, message: 'Request is outside this provider capability.', name: 'SecurityError' })
      return
    }

    const controller = new AbortController()
    const pending: InflightRequest = { controller }
    inflight.set(id, pending)
    try {
      let body: BodyInit | undefined
      const rawBody = message.body as { type?: string; value?: unknown; entries?: unknown[] } | undefined
      if (Array.isArray(message.headers) && message.headers.length > 32) throw new Error('Provider request has too many headers.')
      if (Array.isArray(message.headers) && message.headers.some(pair =>
        Array.isArray(pair) && (String(pair[0]).length > 128 || String(pair[1]).length > 8192))) {
        throw new Error('Provider request header exceeds the safety limit.')
      }
      if (rawBody?.type === 'form-data' && Array.isArray(rawBody.entries)) {
        if (rawBody.entries.length > 128) throw new Error('Provider request has too many form entries.')
        const form = new FormData()
        for (const entry of rawBody.entries) {
          if (!Array.isArray(entry) || typeof entry[0] !== 'string') continue
          if (entry[0].length > 256) throw new Error('Provider form field name exceeds the safety limit.')
          const value = entry[1] as { name?: string; type?: string; bytes?: ArrayBuffer } | string
          if (typeof value === 'string') form.append(entry[0], value)
          else if (value?.bytes instanceof ArrayBuffer) {
            const blob = new Blob([value.bytes], { type: value.type || 'application/octet-stream' })
            form.append(entry[0], blob, value.name || 'upload')
          }
        }
        body = form
      } else if (rawBody?.type === 'string' && typeof rawBody.value === 'string') body = rawBody.value
      else if (rawBody?.type === 'bytes' && rawBody.value instanceof ArrayBuffer) body = rawBody.value
      if (rawBody && rawBody.type !== 'none') {
        const size = typeof body === 'string' ? new TextEncoder().encode(body).byteLength :
          body instanceof ArrayBuffer ? body.byteLength : body instanceof Blob ? body.size :
            rawBody.type === 'form-data' && Array.isArray(rawBody.entries) ? rawBody.entries.reduce<number>((sum, entry) => {
              if (!Array.isArray(entry)) return sum
              const value = entry[1] as { bytes?: ArrayBuffer } | string
              return sum + (typeof value === 'string' ? new TextEncoder().encode(value).byteLength : value?.bytes?.byteLength || 0)
            }, 0) : 0
        if (size > MAX_REQUEST_BODY_BYTES) throw new Error('Request body exceeds the 8 MiB safety limit.')
        if (inflightRequestBytes + size > MAX_INFLIGHT_REQUEST_BODY_BYTES) throw new Error('Concurrent provider request bodies exceed the 16 MiB safety limit.')
        pending.requestBytes = size
        inflightRequestBytes += size
      }

      const token = await callbacks.getToken()
      if (!current() || controller.signal.aborted) throw errorForMessage('Provider authority changed during token refresh.', 'AbortError')
      const headers = new Headers(requestHeaderEntries(message.headers))
      headers.set('X-Railgrid-Org', active.orgUUID || '')
      headers.set('X-Railgrid-Workspace', active.workspaceUUID || '')
      if (token) headers.set('Authorization', `Bearer ${token}`)
      else headers.delete('Authorization')
      const isAsset = url.pathname.startsWith(`/ui/providers/${encodeURIComponent(context.providerName)}/`)
      if (isAsset) {
        const granted = new URL(assetBase)
        url.searchParams.set('grant', granted.searchParams.get('grant') || '')
        if (granted.searchParams.has('v')) url.searchParams.set('v', granted.searchParams.get('v') || '')
      }
      const requestURL = url
      const fetchURL = isKubernetesAPIURL(requestURL) ? providerSandboxKubernetesURL(requestURL, context) : requestURL
      if (!fetchURL) throw errorForMessage('Provider Kubernetes request has no current tenant route.', 'SecurityError')
      const response = await fetchImpl(fetchURL.toString(), {
        method, headers, body, signal: controller.signal, credentials: 'same-origin', redirect: 'error', cache: 'no-store',
      })
      if (!current()) throw errorForMessage('Provider authority changed during request.', 'AbortError')
      const secretSelector = declaredSecretSelector(requestURL.pathname, context, method)
      let checkedSecretBytes: Uint8Array | undefined
      if (secretSelector && response.ok) {
        if (!response.body) throw errorForMessage('The declared Secret response had no body.', 'SecurityError')
        const secretReader = response.body.getReader()
        pending.reader = secretReader
        const parts: Uint8Array[] = []
        let size = 0
        for (;;) {
          const { done, value } = await secretReader.read()
          if (done) break
          size += value.byteLength
          if (size > MAX_RESPONSE_BYTES) {
            await secretReader.cancel()
            throw errorForMessage('The declared Secret response exceeds the 32 MiB safety limit.', 'SecurityError')
          }
          parts.push(value)
        }
        checkedSecretBytes = new Uint8Array(size)
        let offset = 0
        for (const part of parts) { checkedSecretBytes.set(part, offset); offset += part.byteLength }
        let secret: unknown
        try { secret = JSON.parse(new TextDecoder().decode(checkedSecretBytes)) } catch {
          throw errorForMessage('The declared Secret response was not valid JSON.', 'SecurityError')
        }
        const labels = (secret as { metadata?: { labels?: Record<string, unknown> } })?.metadata?.labels
        if (!labels || Object.entries(secretSelector).some(([key, value]) => labels[key] !== value)) {
          throw errorForMessage('The Secret does not match the accepted provider selector.', 'SecurityError')
        }
        if (!current()) throw errorForMessage('Provider authority changed while checking the Secret.', 'AbortError')
      }
      if (response.status === 401 && isKubernetesAPIURL(requestURL)) callbacks.onUnauthorized()
      const responseHeaders = [...response.headers.entries()].filter(([name]) => !PRIVATE_RESPONSE_HEADERS.has(name.toLowerCase()))
      reply({ type: 'fetch-response', id, status: response.status, statusText: response.statusText, headers: responseHeaders })
      if (checkedSecretBytes) {
        const chunk = checkedSecretBytes
        if (inflightResponseBytes + chunk.byteLength > MAX_INFLIGHT_RESPONSE_BYTES) {
          throw new Error('Concurrent provider response bodies exceed the 64 MiB safety limit.')
        }
        pending.responseBytes = chunk.byteLength
        inflightResponseBytes += chunk.byteLength
        await sendChunk(id, pending, 0, chunk)
        reply({ type: 'fetch-end', id })
        return
      }
      if (method === 'HEAD' || !response.body) {
        reply({ type: 'fetch-end', id })
        return
      }
      const reader = response.body.getReader()
      pending.reader = reader
      let seq = 0
      let total = 0
      for (;;) {
        const { done, value } = await reader.read()
        if (done || !current()) break
        total += value.byteLength
        if (total > MAX_RESPONSE_BYTES) throw new Error('Provider response exceeds the 32 MiB safety limit.')
        for (let start = 0; start < value.byteLength; start += MAX_RESPONSE_CHUNK_BYTES) {
          const chunk = value.slice(start, Math.min(start + MAX_RESPONSE_CHUNK_BYTES, value.byteLength))
          if (inflightResponseBytes + chunk.byteLength > MAX_INFLIGHT_RESPONSE_BYTES) {
            throw new Error('Concurrent provider response bodies exceed the 64 MiB safety limit.')
          }
          pending.responseBytes = (pending.responseBytes || 0) + chunk.byteLength
          inflightResponseBytes += chunk.byteLength
          const currentSeq = seq++
          await sendChunk(id, pending, currentSeq, chunk)
        }
      }
      reply({ type: 'fetch-end', id })
    } catch (error) {
      if (current() && !pending.cancelled) reply({
        type: 'fetch-error', id,
        message: error instanceof Error ? error.message : 'Provider request failed.',
        name: error instanceof Error ? error.name : 'TypeError',
      })
    } finally {
      inflightRequestBytes -= pending.requestBytes || 0
      inflightResponseBytes -= pending.responseBytes || 0
      inflight.delete(id)
    }
  }

  function sendChunk(id: string, pending: InflightRequest, seq: number, chunk: Uint8Array): Promise<void> {
    const acked = new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.controller.abort()
        void pending.reader?.cancel().catch(() => undefined)
        reject(new Error('Provider did not accept a response chunk in time.'))
      }, MAX_ACK_WAIT_MS)
      pending.ack = { seq, resolve: () => { clearTimeout(timer); resolve() } }
    })
    reply({ type: 'fetch-chunk', id, seq, bytes: chunk.buffer }, [chunk.buffer])
    return acked.then(() => {
      pending.ack = undefined
      if (!current() || pending.controller.signal.aborted) {
        throw errorForMessage('Provider request was cancelled or authority changed.', 'AbortError')
      }
    })
  }

  port.onmessage = event => {
    const message = event.data as Record<string, unknown> | null
    if (!message || typeof message !== 'object' || !current()) return
    const id = typeof message.id === 'string' ? message.id : ''
    switch (message.type) {
      case 'fetch': void performFetch(message); break
      case 'fetch-cancel': {
        const pending = inflight.get(id)
        if (pending) pending.cancelled = true
        pending?.controller.abort()
        void pending?.reader?.cancel().catch(() => undefined)
        pending?.ack?.resolve()
        break
      }
      case 'fetch-ack': {
        const pending = inflight.get(id)
        const ack = pending?.ack
        if (ack && ack.seq === message.seq) ack.resolve()
        break
      }
      case 'navigate':
        if (typeof message.path === 'string' && isSafeProviderNavigation(message.path)) callbacks.onNavigate(message.path, message.replace === true)
        break
      case 'layout-change': if (typeof message.fullBleed === 'boolean') callbacks.onLayoutChange(message.fullBleed); break
      case 'route-ready': callbacks.onRouteReady(); break
      case 'publish-page-resource': callbacks.onPublishPageResource(message.value); break
      case 'publish-agent-context': callbacks.onPublishAgentContext(message.value); break
      case 'agent-overlay':
        if (context.providerName === 'agents' && typeof message.action === 'string') callbacks.onAgentOverlay(message.action, message.value)
        break
      case 'bootstrap-retry': callbacks.onRetry(); break
    }
  }
  port.start()

  return {
    update(next: IsolatedProviderContext) {
      const nextScope = { tenant: next.tenant, orgUUID: next.orgUUID, workspaceUUID: next.workspaceUUID }
      const nextCapabilities = capabilityFingerprint(next)
      if (JSON.stringify(nextScope) !== JSON.stringify(active) || nextCapabilities !== activeCapabilities) cancelAll()
      active = nextScope
      activeCapabilities = nextCapabilities
      context = next
      reply({ type: 'context', context: safeChildContext(next) })
    },
    close() {
      if (closed) return
      closed = true
      cancelAll()
      port.onmessage = null
      port.close()
    },
  }
}

function safeChildContext(context: IsolatedProviderContext): Record<string, unknown> {
  return {
    providerName: context.providerName,
    tenant: context.tenant,
    orgUUID: context.orgUUID,
    workspaceUUID: context.workspaceUUID,
    theme: context.theme,
    subPath: context.subPath || '',
    user: context.user,
    navigationBasePath: context.navigationBasePath,
    basePath: context.basePath,
    agentOverlay: context.agentOverlay,
  }
}

export function isSafeProviderNavigation(path: string): boolean {
  if (path.length > 2048 || path.startsWith('//') || path.includes('\\') || /^[a-z][a-z0-9+.-]*:/i.test(path)) return false
  const route = path.split(/[?#]/, 1)[0]
  if (route.split('/').some(segment => {
    try { const decoded = decodeURIComponent(segment); return decoded === '.' || decoded === '..' || decoded.includes('/') || decoded.includes('\\') }
    catch { return true }
  })) return false
  return true
}

export function createProviderSandboxFrame(
  iframe: HTMLIFrameElement,
  bundle: { bytes: ArrayBuffer; assetBase: string },
  context: IsolatedProviderContext,
  callbacks: ProviderSandboxCallbacks,
  options: { origin?: string; css?: string; timeoutMs?: number } = {},
) {
  const origin = options.origin || window.location.origin
  const nonce = crypto.randomUUID()
  const channel = new MessageChannel()
  const bridge = createProviderSandboxBridge(channel.port1, context, bundle.assetBase, callbacks)
  let closed = false
  let initialized = false
  let timer: ReturnType<typeof setTimeout> | undefined
  const onLoad = () => {
    if (closed || initialized || !iframe.contentWindow) return
    initialized = true
    const bundleBytes = bundle.bytes
    iframe.contentWindow.postMessage({
      type: 'railgrid-provider-init', nonce, hostOrigin: origin, context: safeChildContext(context),
      apiGroups: context.apiGroups, bytes: bundleBytes, css: options.css || '',
    }, '*', [channel.port2, bundleBytes])
  }
  // The initial message is sent only to this exact iframe. Its opaque origin
  // requires '*'; the child validates the exact parent source and origin.
  iframe.addEventListener('load', onLoad)
  channel.port1.addEventListener('message', event => {
    if (event.data?.type === 'ready' && event.data?.nonce === nonce && !closed) {
      if (timer) clearTimeout(timer)
      callbacks.onReady()
    } else if (event.data?.type === 'bootstrap-error' && !closed) {
      callbacks.onError(new Error('The isolated provider frame could not start.'))
    }
  })
  channel.port1.start()
  timer = setTimeout(() => {
    if (!closed && !callbacks.isCurrent()) return
    callbacks.onError(new Error('The isolated provider frame did not become ready.'))
  }, options.timeoutMs ?? 15_000)
  iframe.setAttribute('sandbox', 'allow-scripts')
  iframe.referrerPolicy = 'no-referrer'
  iframe.allow = ''
  iframe.src = '/ui/provider-sandbox.html'

  return {
    update(next: IsolatedProviderContext) { if (!closed) bridge.update(next) },
    close() {
      if (closed) return
      closed = true
      if (timer) clearTimeout(timer)
      iframe.removeEventListener('load', onLoad)
      bridge.close()
      try { channel.port2.close() } catch { /* already transferred */ }
      iframe.removeAttribute('src')
      iframe.src = 'about:blank'
    },
  }
}
