import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AGENT_PATCH_TIMEOUT_MS, Resources, ResourceError } from '../resources'
import type { RailgridContext } from '../types'

// The kube client is exercised for real — only the transport is faked, so these
// assert the exact requests that reach kcp: the path, the verb, the patch
// content type, and the object bodies. That is the contract this file moved
// onto; a mock of the client itself would assert nothing about it.

interface Call {
  method: string
  url: string
  contentType: string
  body: unknown
}

class FakeKcp {
  readonly calls: Call[] = []
  private responses = new Map<string, unknown>()
  private failures = new Map<string, { status: number; reason: string; message: string }>()
  private oneShotFailures = new Map<string, { status: number; reason: string; message: string }>()
  private oneShotJsonPatchFailures = new Map<string, { status: number; reason: string; message: string }>()
  private objects = new Map<string, Record<string, unknown>>()
  private deletedObjects = new Set<string>()
  private nextUID = 1

  /** reply registers a body for `${method} ${pathSuffix}` (suffix-matched). */
  reply(key: string, body: unknown) {
    this.responses.set(key, body)
  }

  fail(key: string, status: number, reason: string, message = reason) {
    this.failures.set(key, { status, reason, message })
  }

  failNext(key: string, status: number, reason: string, message = reason) {
    this.oneShotFailures.set(key, { status, reason, message })
  }

  failNextJsonPatch(key: string, status: number, reason: string, message = reason) {
    this.oneShotJsonPatchFailures.set(key, { status, reason, message })
  }

  object(pathSuffix: string): Record<string, unknown> | undefined {
    const path = [...this.objects.keys()].find(key => key.endsWith(pathSuffix))
    const object = path ? this.objects.get(path) : undefined
    return object ? structuredClone(object) : undefined
  }

  fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = String(input)
    const method = (init?.method ?? 'GET').toUpperCase()
    const headers = new Headers(init?.headers)
    const raw = init?.body
    this.calls.push({
      method,
      url,
      contentType: headers.get('Content-Type') ?? '',
      body: typeof raw === 'string' && raw ? JSON.parse(raw) : undefined,
    })
    const path = new URL(url, 'https://test.invalid').pathname
    if (method === 'PATCH' && headers.get('Content-Type') === 'application/json-patch+json') {
      const onceJsonKey = [...this.oneShotJsonPatchFailures.keys()].find(k => this.matches(k, method, url))
      if (onceJsonKey) {
        const f = this.oneShotJsonPatchFailures.get(onceJsonKey)!
        this.oneShotJsonPatchFailures.delete(onceJsonKey)
        return jsonResponse(f.status, { kind: 'Status', status: 'Failure', reason: f.reason, message: f.message, code: f.status })
      }
    }
    const onceKey = [...this.oneShotFailures.keys()].find((k) => this.matches(k, method, url))
    if (onceKey) {
      const f = this.oneShotFailures.get(onceKey)!
      this.oneShotFailures.delete(onceKey)
      return jsonResponse(f.status, { kind: 'Status', status: 'Failure', reason: f.reason, message: f.message, code: f.status })
    }
    const key = [...this.failures.keys()].find((k) => this.matches(k, method, url))
    if (key) {
      const f = this.failures.get(key)!
      return jsonResponse(f.status, { kind: 'Status', status: 'Failure', reason: f.reason, message: f.message, code: f.status })
    }
    if (method === 'GET' && this.deletedObjects.has(path)) {
      return jsonResponse(404, { kind: 'Status', status: 'Failure', reason: 'NotFound', message: 'not found', details: { name: path.split('/').pop() }, code: 404 })
    }
    if (method === 'GET' && this.objects.has(path)) return jsonResponse(200, structuredClone(this.objects.get(path)))
    const hit = [...this.responses.keys()].find((k) => this.matches(k, method, url))
    if (hit) {
      const response = structuredClone(this.responses.get(hit)) as Record<string, unknown>
      if (method === 'GET' && !path.endsWith('/modelproviderconnections') && !path.endsWith('/modelcredentials')) this.objects.set(path, response)
      return jsonResponse(200, response)
    }
    if (method === 'GET') {
      const stored = this.objects.get(path)
      if (stored) return jsonResponse(200, structuredClone(stored))
      if (path.endsWith('/modelproviderconnections') || path.endsWith('/modelcredentials')) return jsonResponse(200, { items: [] })
    }
    if (method === 'POST' && raw) {
      const object = JSON.parse(String(raw)) as { metadata?: Record<string, unknown> }
      const kind = (object as { kind?: string }).kind
      const spec = (object as { spec?: Record<string, unknown> }).spec
      if ((kind === 'ModelCredential' || kind === 'ModelProviderConnection') && spec && spec.secretKey === undefined) spec.secretKey = 'apiKey'
      const created = {
        ...object,
        ...(spec ? { spec } : {}),
        metadata: {
          ...object.metadata,
          name: object.metadata?.name ?? path.split('/').pop(),
          uid: `created-${this.nextUID++}`,
          resourceVersion: '1',
          creationTimestamp: new Date().toISOString(),
        },
      }
      const objectName = String((created.metadata as Record<string, unknown>).name)
      const objectPath = `${path}/${objectName}`
      this.deletedObjects.delete(objectPath)
      this.objects.set(objectPath, created)
      return jsonResponse(200, created)
    }
    if (method === 'PATCH' && headers.get('Content-Type') === 'application/json-patch+json' && raw) {
      const operations = JSON.parse(String(raw)) as Array<{ op: string; path: string; value?: unknown }>
      const object = structuredClone(this.objects.get(path) ?? { metadata: { name: path.split('/').pop() } })
      const get = (pointer: string): unknown => {
        if (pointer === '/metadata/uid') return (object.metadata as Record<string, unknown>)?.uid
        if (pointer === '/metadata/resourceVersion') return (object.metadata as Record<string, unknown>)?.resourceVersion
        if (pointer === '/spec') return object.spec
        const annotationPrefix = '/metadata/annotations/'
        if (pointer.startsWith(annotationPrefix)) return ((object.metadata as Record<string, unknown>)?.annotations as Record<string, unknown> | undefined)?.[pointer.slice(annotationPrefix.length).replaceAll('~1', '/')]
        return undefined
      }
      for (const operation of operations.filter(item => item.op === 'test')) {
        if (JSON.stringify(get(operation.path)) !== JSON.stringify(operation.value)) {
          return jsonResponse(409, { kind: 'Status', status: 'Failure', reason: 'Conflict', message: 'JSON patch test failed', code: 409 })
        }
      }
      const metadata = object.metadata as Record<string, unknown>
      const annotations = { ...((metadata.annotations as Record<string, string> | undefined) ?? {}) }
      for (const operation of operations) {
        const prefix = '/metadata/annotations/'
        if (operation.path === '/spec' && operation.op === 'replace') object.spec = operation.value
        else if (operation.path.startsWith(prefix)) {
          const key = operation.path.slice(prefix.length).replaceAll('~1', '/')
          if (operation.op === 'remove') delete annotations[key]
          else if (operation.op === 'add' && typeof operation.value === 'string') annotations[key] = operation.value
        }
      }
      const kind = (object as { kind?: string }).kind
      const spec = object.spec as Record<string, unknown> | undefined
      if ((kind === 'ModelCredential' || kind === 'ModelProviderConnection') && spec && spec.secretKey === undefined) spec.secretKey = 'apiKey'
      metadata.annotations = annotations
      metadata.resourceVersion = String(Number(metadata.resourceVersion ?? '1') + 1)
      this.deletedObjects.delete(path)
      this.objects.set(path, object)
      return jsonResponse(200, object)
    }
    if (method === 'PATCH' && raw) {
      const body = JSON.parse(String(raw)) as { metadata?: Record<string, unknown>; spec?: unknown }
      const existing = structuredClone(this.objects.get(path) ?? { metadata: { name: path.split('/').pop(), uid: body.metadata?.uid ?? `patched-${this.nextUID++}`, resourceVersion: '1' } })
      const priorMetadata = existing.metadata as Record<string, unknown>
      const patchMetadata = body.metadata ?? {}
      if (patchMetadata.resourceVersion && patchMetadata.resourceVersion !== priorMetadata.resourceVersion) {
        return jsonResponse(409, { kind: 'Status', status: 'Failure', reason: 'Conflict', message: 'resourceVersion precondition failed', code: 409 })
      }
      const updatedMetadata = { ...priorMetadata, ...patchMetadata }
      if (patchMetadata.annotations) updatedMetadata.annotations = { ...((priorMetadata.annotations as Record<string, unknown> | undefined) ?? {}), ...(patchMetadata.annotations as Record<string, unknown>) }
      updatedMetadata.resourceVersion = String(Number(priorMetadata.resourceVersion ?? '1') + 1)
      existing.metadata = updatedMetadata
      if (body.spec && typeof body.spec === 'object') {
        const nextSpec = { ...((existing.spec as Record<string, unknown> | undefined) ?? {}), ...(body.spec as Record<string, unknown>) }
        for (const [field, value] of Object.entries(nextSpec)) if (value === null) delete nextSpec[field]
        existing.spec = nextSpec
      }
      this.deletedObjects.delete(path)
      this.objects.set(path, existing)
      return jsonResponse(200, existing)
    }
    if (method === 'DELETE') {
      this.objects.delete(path)
      this.deletedObjects.add(path)
      return jsonResponse(200, { kind: 'Status', status: 'Success' })
    }
    return jsonResponse(200, { metadata: { name: path.split('/').pop() ?? 'ok' } })
  }

  private matches(key: string, method: string, url: string): boolean {
    const [wantMethod, suffix] = key.split(' ')
    return wantMethod === method && url.includes(suffix)
  }

  /** lastOf returns the most recent call made with `method`. */
  lastOf(method: string): Call {
    const call = [...this.calls].reverse().find((c) => c.method === method)
    if (!call) throw new Error(`no ${method}; saw ${this.calls.map((c) => `${c.method} ${c.url}`).join(', ')}`)
    return call
  }

  /** last returns the most recent call whose URL contains `fragment`. */
  last(fragment: string): Call {
    const call = [...this.calls].reverse().find((c) => c.url.includes(fragment))
    if (!call) throw new Error(`no call to ${fragment}; saw ${this.calls.map((c) => `${c.method} ${c.url}`).join(', ')}`)
    return call
  }
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

const CLUSTER = 'cluster-xyz'

let kcp: FakeKcp
let resources: Resources

function contextWith(tenant: string | null): RailgridContext {
  return { fetch: kcp.fetch, tenant, basePath: '/ui/providers/agents' }
}

beforeEach(() => {
  kcp = new FakeKcp()
  resources = new Resources()
  resources.setContext(contextWith(CLUSTER))
})

describe('addressing', () => {
  it('reads every kind from the tenant cluster, not from the provider backend', async () => {
    kcp.reply('GET /agents', { items: [] })
    await resources.listAgents()
    const call = kcp.last('/agents')
    expect(call.url).toContain(`/clusters/${CLUSTER}/apis/agents.railgrid.ai/v1alpha1/agents`)
    expect(call.url).not.toContain('/api/agents')
  })

  it('refuses before touching the network when no workspace is selected', async () => {
    resources.setContext(contextWith(null))
    await expect(resources.listAgents()).rejects.toMatchObject({ reason: 'TenantMissing' })
    expect(kcp.calls).toHaveLength(0)
  })

  it('reports a missing APIBinding as such, not as a missing object', async () => {
    // A 404 with no details.name is the resource TYPE being absent: the tenant
    // has not enabled the provider here. Reporting it as "not found" would send
    // the user looking for an agent they never created.
    kcp.fail('GET /agents', 404, 'NotFound', 'the server could not find the requested resource')
    await expect(resources.listAgents()).rejects.toMatchObject({ reason: 'APIBindingMissing' })
  })

  it('abandons a reply that lands after the workspace changed', async () => {
    kcp.reply('GET /agents', { items: [] })
    const pending = resources.listAgents()
    resources.setContext(contextWith('another-cluster'))
    await expect(pending).rejects.toMatchObject({ reason: 'ContextChanged' })
  })
})

describe('bounded Agent writes', () => {
  it('times out an Agent PATCH when the host response body never arrives', async () => {
    vi.useFakeTimers()
    try {
      let requestSignal: AbortSignal | undefined
      const responseBody = new Promise<string>(() => {})
      resources.setContext({
        ...contextWith(CLUSTER),
        fetch: async (_input, init) => {
          requestSignal = init?.signal ?? undefined
          return { ok: true, status: 200, text: () => responseBody } as Response
        },
      })

      const pending = resources.patchAgent('scout', { modelCredential: 'new-model' })
      const rejected = expect(pending).rejects.toMatchObject({
        name: 'ResourceError',
        status: 504,
        reason: 'Timeout',
        message: 'Saving agent settings timed out. The change may still have been saved; refresh to confirm it.',
      })
      await vi.advanceTimersByTimeAsync(AGENT_PATCH_TIMEOUT_MS)
      await rejected
      expect(requestSignal?.aborted).toBe(true)
    } finally {
      vi.useRealTimers()
    }
  })

  it('aborts a reconciliation list and ignores a response arriving after its deadline', async () => {
    let resolveResponse!: (response: Response) => void
    let requestSignal: AbortSignal | undefined
    const lateResponse = new Promise<Response>(resolve => { resolveResponse = resolve })
    const controller = new AbortController()
    resources.setContext({
      ...contextWith(CLUSTER),
      fetch: async (_input, init) => {
        requestSignal = init?.signal ?? undefined
        return lateResponse
      },
    })

    const pending = resources.listAgents(controller.signal)
    await Promise.resolve()
    const error = new Error('Agent settings refresh timed out. The saved change may still be present; refresh to confirm it.')
    error.name = 'TimeoutError'
    controller.abort(error)
    await expect(pending).rejects.toMatchObject({ status: 504, reason: 'Timeout', message: error.message })
    expect(requestSignal?.aborted).toBe(true)

    resolveResponse(jsonResponse(200, { items: [{ metadata: { name: 'stale' }, spec: {} }] }))
    await Promise.resolve()
    expect(kcp.calls).toHaveLength(0)
  })
})

describe('agents', () => {
  it('creates an Agent object rather than posting a DTO', async () => {
    await resources.createAgent({
      name: 'scout',
      modelCredential: ' primary ',
      modelFallbacks: ['a', 'a', ' b '],
      interactiveFamilies: ['web', 'bogus'],
      budgetTokens: 1000,
    })
    const call = kcp.last('/agents')
    expect(call.method).toBe('POST')
    expect(call.body).toMatchObject({
      apiVersion: 'agents.railgrid.ai/v1alpha1',
      kind: 'Agent',
      metadata: { name: 'scout' },
      spec: {
        // An empty display name falls back to the object name, as the REST
        // handler did — a nameless row in the grid is not a useful save.
        displayName: 'scout',
        // spec.backend replaced spec.models / spec.modelFallbacks: the old
        // fields do not exist on the CRD, so writing them meant the apiserver
        // pruned the credential and the agent could not run.
        backend: { type: 'model', model: { credentials: { chat: 'primary' }, fallbacks: ['a', 'b'] } },
        budget: { window: 'month', tokenLimit: 1000 },
        // Unknown families are dropped and core is always granted.
        tools: { interactive: { families: ['core', 'web'] } },
      },
    })
  })

  it('writes no backend block at all when a create names no credential', async () => {
    // An absent spec.backend IS a model-backed agent with no credential yet, so
    // there is nothing to write. An empty block would be a claim about a choice
    // nobody made.
    kcp.reply('GET /agents', { items: [] })
    await resources.createAgent({ name: 'scout' })
    const spec = (kcp.lastOf('POST').body as { spec: Record<string, unknown> }).spec
    expect(spec.backend).toBeUndefined()
  })

  it('writes spec.backend.model and never the pruned spec.models / spec.modelFallbacks', async () => {
    // The old fields do not exist on the CRD. Writing them meant the apiserver
    // pruned them silently: the agent saved, lost its credential, and could not
    // run — so this is the assertion that the break stays fixed.
    await resources.createAgent({ name: 'scout', modelCredential: 'primary', modelFallbacks: ['backup'] })
    const spec = (kcp.lastOf('POST').body as { spec: Record<string, unknown> }).spec
    expect(spec.backend).toEqual({ type: 'model', model: { credentials: { chat: 'primary' }, fallbacks: ['backup'] } })
    expect(spec).not.toHaveProperty('models')
    expect(spec).not.toHaveProperty('modelFallbacks')
  })

  it('creates a harness-backed agent with the harness block and NO model block', async () => {
    // spec.backend's CEL rules refuse a harness type carrying a model block, so
    // a writer that emitted both would have the create rejected outright.
    kcp.reply('GET /agents', { items: [] })
    await resources.createAgent({
      name: 'builder',
      backendType: 'harness',
      // A credential the model backend would have used is ignored: the caller
      // asked for a harness, and a leftover chat credential is not part of one.
      modelCredential: 'primary',
      harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'my-codex', model: 'gpt-6-luna', reasoningEffort: 'max', workspace: 'ephemeral' },
    })
    const spec = (kcp.lastOf('POST').body as { spec: Record<string, unknown> }).spec
    expect(spec.backend).toEqual({
      type: 'harness',
      harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'my-codex', model: 'gpt-6-luna', reasoningEffort: 'max', workspace: 'ephemeral' },
    })
    expect(spec.backend).not.toHaveProperty('model')
  })

  it('refuses a harness backend whose edge kind cannot host a runner', async () => {
    // A runner is a process on a machine, so a KubernetesCluster edge is not a
    // candidate — the CRD enum says so and this refuses before the network.
    await expect(resources.createAgent({
      name: 'builder',
      backendType: 'harness',
      harness: { edgeRef: { kind: 'KubernetesCluster' as 'LinuxServer', name: 'prod' }, credentialRef: 'my-claude' },
    })).rejects.toMatchObject({ status: 400 })
    await expect(resources.createAgent({
      name: 'builder',
      backendType: 'harness',
      harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: '' },
    })).rejects.toMatchObject({ status: 400 })
    expect(kcp.calls.filter((c) => c.method === 'POST')).toHaveLength(0)
  })

  it('passes the harness GitHub connection through and drops a blank one', async () => {
    await resources.createAgent({
      name: 'reviewer',
      backendType: 'harness',
      harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'my-claude', githubConnectionRef: ' gh-main ' },
    })
    const spec = (kcp.lastOf('POST').body as { spec: { backend: { harness: Record<string, unknown> } } }).spec
    expect(spec.backend.harness.githubConnectionRef).toBe('gh-main')
    await resources.patchAgent('reviewer', {
      backendType: 'harness',
      harness: { edgeRef: { kind: 'LinuxServer', name: 'build-01' }, credentialRef: 'my-claude', githubConnectionRef: '' },
    })
    expect(JSON.stringify(kcp.lastOf('PATCH').body)).not.toContain('githubConnectionRef')
  })

  it('patches the chat credential under spec.backend.model without touching the type', async () => {
    // The model section owns its own fields and nothing else: naming the type
    // here would let it clobber a choice the backend section owns.
    await resources.patchAgent('scout', { modelCredential: 'primary', modelFallbacks: ['a', 'a', ' b '] })
    expect(kcp.lastOf('PATCH').body).toEqual({
      spec: { backend: { model: { credentials: { chat: 'primary' }, fallbacks: ['a', 'b'] } } },
    })
  })

  it('clears the other block when a patch switches backends', async () => {
    // The two are mutually exclusive by CEL, and merge patch deletes a key by
    // setting it to null. A patch that left the old block behind would describe
    // an agent configured for either, and which one it used would be decided by
    // whichever reader looked first.
    await resources.patchAgent('scout', {
      backendType: 'harness',
      harness: { edgeRef: { kind: 'MacOSServer', name: 'mini-02' }, credentialRef: 'my-codex' },
    })
    // Tool grants and delegates go with the model block: the provider refuses
    // a harness agent that still carries them, so a switch that kept a
    // model-era grant would leave an agent no run can start.
    expect(kcp.lastOf('PATCH').body).toEqual({
      spec: {
        backend: {
          type: 'harness',
          harness: { edgeRef: { kind: 'MacOSServer', name: 'mini-02' }, credentialRef: 'my-codex', model: null, reasoningEffort: null },
          model: null,
        },
        tools: null,
        delegates: null,
      },
    })

    await resources.patchAgent('scout', { backendType: 'model' })
    expect(kcp.lastOf('PATCH').body).toEqual({ spec: { backend: { type: 'model', harness: null } } })
  })

  it('rejects a budget the CRD would happily store', async () => {
    await expect(resources.createAgent({ name: 'scout', budgetUSD: '-1' })).rejects.toBeInstanceOf(ResourceError)
    await expect(resources.createAgent({ name: 'scout', budgetUSD: 'lots' })).rejects.toBeInstanceOf(ResourceError)
    expect(kcp.calls.filter((c) => c.method === 'POST')).toHaveLength(0)
  })

  it('refuses a channel another agent already holds', async () => {
    // Inbound routing maps a Connection to one agent, so this is a conflict
    // rather than a preference.
    kcp.reply('GET /agents', {
      items: [{ metadata: { name: 'other' }, spec: { channels: [{ name: 'primary', connectionRef: 'tg' }] } }],
    })
    await expect(
      resources.createAgent({ name: 'scout', channels: [{ name: 'alerts', connectionRef: 'tg' }] }),
    ).rejects.toMatchObject({ status: 409 })
    expect(kcp.calls.filter((c) => c.method === 'POST')).toHaveLength(0)
  })

  it('refuses a half-filled channel row instead of silently dropping it', async () => {
    await expect(
      resources.createAgent({ name: 'scout', channels: [{ name: 'alerts', connectionRef: '' }] }),
    ).rejects.toMatchObject({ status: 400 })
  })

  it('marks exactly one channel primary', async () => {
    kcp.reply('GET /agents', { items: [] })
    await resources.createAgent({
      name: 'scout',
      channels: [
        { name: 'a', connectionRef: 'c1' },
        { name: 'b', connectionRef: 'c2', primary: true },
        { name: 'c', connectionRef: 'c3', primary: true },
      ],
    })
    const body = kcp.lastOf('POST').body as { spec: { channels: { name: string; primary: boolean }[] } }
    expect(body.spec.channels.map((c) => c.primary)).toEqual([false, true, false])
  })

  it('patches with a merge patch carrying only the keys it was given', async () => {
    await resources.patchAgent('scout', { systemPrompt: 'be brief' })
    const call = kcp.last('/agents/scout')
    expect(call.method).toBe('PATCH')
    expect(call.contentType).toBe('application/merge-patch+json')
    expect(call.body).toEqual({ spec: { systemPrompt: 'be brief' } })
  })

  it('clears the chat model with an explicit null, which is how merge patch deletes a key', async () => {
    await resources.patchAgent('scout', { modelCredential: '' })
    expect(kcp.last('/agents/scout').body).toEqual({ spec: { backend: { model: { credentials: { chat: null } } } } })
  })

  it('merges a partial budget onto the stored one', async () => {
    kcp.reply('GET /agents/scout', {
      metadata: { name: 'scout' },
      spec: { budget: { window: 'month', usdLimit: '25' } },
    })
    await resources.patchAgent('scout', { budgetTokens: 500 })
    expect(kcp.lastOf('PATCH').body).toEqual({
      spec: { budget: { window: 'month', tokenLimit: 500, usdLimit: '25' } },
    })
  })

  it('stores "no cap" as a null budget rather than zeroes', async () => {
    kcp.reply('GET /agents/scout', { metadata: { name: 'scout' }, spec: {} })
    await resources.patchAgent('scout', { budgetTokens: 0, budgetUSD: '0' })
    expect(kcp.lastOf('PATCH').body).toEqual({ spec: { budget: null } })
  })

  it('patches one tool grant without disturbing the other run class', async () => {
    // spec.tools has an interactive and a background half, each owned by a
    // different section of the config form. A patch that named both would let
    // one section's save clobber the other's.
    await resources.patchAgent('scout', { interactiveToolsets: ['research'] })
    expect(kcp.lastOf('PATCH').body).toEqual({
      spec: { tools: { interactive: { toolsets: ['research'] } } },
    })
  })

  it('grants core alongside whatever families were chosen', async () => {
    // The reconciler reports a families grant without core as invalid rather
    // than rewriting it, so the writer has to include it.
    await resources.patchAgent('scout', { backgroundFamilies: ['web'] })
    expect(kcp.lastOf('PATCH').body).toEqual({
      spec: { tools: { background: { families: ['core', 'web'] } } },
    })
  })

  it('drops a self-delegation loop', async () => {
    await resources.patchAgent('scout', { delegates: ['scout', 'helper'] })
    expect(kcp.lastOf('PATCH').body).toEqual({ spec: { delegates: ['helper'] } })
  })
})

describe('schedules and triggers', () => {
  it('requires the field the chosen schedule type actually fires on', async () => {
    await expect(resources.createSchedule({ name: 's', agentRef: 'a', type: 'cron' })).rejects.toMatchObject({ status: 400 })
    await expect(resources.createSchedule({ name: 's', agentRef: 'a', type: 'wakeup' })).rejects.toMatchObject({ status: 400 })
    await expect(resources.createSchedule({ name: 's', agentRef: 'a', type: 'weekly' })).rejects.toMatchObject({ status: 400 })
  })

  it('normalizes runAt to RFC3339 and rejects what is not a time', async () => {
    await resources.createSchedule({ name: 's', agentRef: 'a', type: 'wakeup', runAt: '2026-07-13T09:00:00Z' })
    expect(kcp.lastOf('POST').body).toMatchObject({ spec: { runAt: '2026-07-13T09:00:00.000Z' } })
    await expect(
      resources.createSchedule({ name: 's', agentRef: 'a', type: 'wakeup', runAt: 'tomorrow' }),
    ).rejects.toMatchObject({ status: 400 })
  })

  it('clears a one-shot fire time with null', async () => {
    await resources.patchSchedule('s', { runAt: '' })
    expect(kcp.lastOf('PATCH').body).toEqual({ spec: { runAt: null } })
  })

  it('requires the webhook a trigger routes from and writes no source or retention of its own', async () => {
    await expect(resources.createTrigger({ name: 't', agentRef: 'a', webhookRef: ' ' })).rejects.toMatchObject({ status: 400 })
    await resources.createTrigger({ name: 't', agentRef: 'a', webhookRef: ' github-org ' })
    expect(kcp.lastOf('POST').body).toMatchObject({ kind: 'Trigger', spec: { agentRef: 'a', webhookRef: 'github-org' } })
    const spec = (kcp.lastOf('POST').body as { spec: Record<string, unknown> }).spec
    expect(Object.keys(spec).sort()).toEqual(['agentRef', 'webhookRef'])
  })

  it('writes a trimmed spec.filter on create and omits an empty one', async () => {
    await resources.createTrigger({ name: 't', agentRef: 'a', webhookRef: 'gh', filter: { eventType: ' pull_request ', match: '', stale: null } })
    expect((kcp.lastOf('POST').body as { spec: Record<string, unknown> }).spec.filter).toEqual({ eventType: 'pull_request' })
    await resources.createTrigger({ name: 't', agentRef: 'a', webhookRef: 'gh', filter: {} })
    expect(JSON.stringify(kcp.lastOf('POST').body)).not.toContain('filter')
  })

  it('patches spec.filter as a merge fragment where null removes a key', async () => {
    await resources.patchTrigger('t', { filter: { eventType: 'pull_request', match: null } })
    expect(kcp.lastOf('PATCH').body).toEqual({ spec: { filter: { eventType: 'pull_request', match: null } } })
    await resources.patchTrigger('t', { task: 'x', filter: {} })
    expect(kcp.lastOf('PATCH').body).toEqual({ spec: { task: 'x' } })
  })

  it('retargets a trigger at another webhook and refuses an empty one', async () => {
    await resources.patchTrigger('t', { webhookRef: 'deploys' })
    expect(kcp.lastOf('PATCH').body).toEqual({ spec: { webhookRef: 'deploys' } })
    await expect(resources.patchTrigger('t', { webhookRef: '' })).rejects.toMatchObject({ status: 400 })
  })
})

describe('webhooks', () => {
  it('creates a cluster-scoped Webhook and never writes its path, because the token is the provider’s to mint', async () => {
    await resources.createWebhook({ name: 'github-org', source: 'github', displayName: ' GitHub org ', deliveryRetention: 50 })
    const post = kcp.lastOf('POST')
    expect(post.url).toContain('/apis/agents.railgrid.ai/v1alpha1/webhooks')
    expect(post.body).toEqual({
      apiVersion: 'agents.railgrid.ai/v1alpha1',
      kind: 'Webhook',
      metadata: { name: 'github-org' },
      spec: { source: 'github', displayName: 'GitHub org', deliveryRetention: 50 },
    })
    expect(JSON.stringify(post.body)).not.toContain('webhookPath')
  })

  it('refuses a source the provider cannot receive and a retention out of bounds', async () => {
    await expect(resources.createWebhook({ name: 'w', source: 'email' })).rejects.toMatchObject({ status: 400 })
    await expect(resources.createWebhook({ name: 'w', source: 'webhook', deliveryRetention: 1001 })).rejects.toMatchObject({ status: 400 })
    await expect(resources.patchWebhook('w', { deliveryRetention: -1 })).rejects.toMatchObject({ status: 400 })
  })

  it('merge-patches settings, where null restores the default retention and an empty name drops it', async () => {
    await resources.patchWebhook('w', { displayName: '', deliveryRetention: null })
    expect(kcp.lastOf('PATCH').body).toEqual({ spec: { displayName: null, deliveryRetention: null } })
    await resources.patchWebhook('w', { source: 'github', deliveryRetention: 0 })
    expect(kcp.lastOf('PATCH').body).toEqual({ spec: { source: 'github', deliveryRetention: 0 } })
  })
})

describe('connections', () => {
  it('writes the credential Secret before the Connection that references it', async () => {
    await resources.createConnection({ name: 'gh', type: 'github', secret: 'ghp_x' })
    const secretCall = kcp.calls.findIndex((c) => c.url.includes('/secrets'))
    const connCall = kcp.calls.findIndex((c) => c.url.includes('/connections') && c.method === 'POST')
    expect(secretCall).toBeGreaterThanOrEqual(0)
    expect(secretCall).toBeLessThan(connCall)
    expect(kcp.calls[connCall].body).toMatchObject({
      spec: { type: 'github', auth: 'secret', secretRef: 'railgrid-agents-conn-gh' },
    })
  })

  it('generates a Telegram secret_token so the connection is born verifiable', async () => {
    await resources.createConnection({ name: 'tg', type: 'telegram', secret: 'bot:1' })
    const body = kcp.last('/secrets').body as { stringData: Record<string, string> }
    expect(body.stringData.signing_secret).toMatch(/^[0-9a-f]{64}$/)
  })

  it('refuses an oauth connection with no client credentials and no platform app', async () => {
    await expect(
      resources.createConnection({ name: 'gh', type: 'github', auth: 'oauth' }, {}),
    ).rejects.toMatchObject({ status: 400 })
    await expect(
      resources.createConnection({ name: 'gh', type: 'github', auth: 'oauth' }, { github: true }),
    ).resolves.toBeDefined()
  })

  it('takes over field ownership when applying a Secret', async () => {
    // Credentials written before this moved to kcp are owned by the provider
    // backend's writer; an un-forced apply would 409 on every key.
    await resources.createConnection({ name: 'gh', type: 'github', secret: 'ghp_x' })
    expect(kcp.last('/secrets').url).toContain('force=true')
    expect(kcp.last('/secrets').contentType).toBe('application/apply-patch+yaml')
  })

  it('rotates one Secret key without reading or rewriting the others', async () => {
    await resources.patchConnection('gh', { secret: 'ghp_new' })
    const call = kcp.last('/secrets')
    expect(call.method).toBe('PATCH')
    expect(call.contentType).toBe('application/merge-patch+json')
    // The owner label rides along: the provider's `secrets` claim is scoped to
    // it, so a Secret written before the claim was narrowed is adopted on the
    // next rotation rather than staying invisible to the provider forever.
    expect(call.body).toEqual({
      metadata: { labels: { 'railgrid.ai/owner': 'agents' } },
      stringData: { token: 'ghp_new' },
    })
    // No GET of the Secret: a one-key patch cannot drop the keys it omits.
    expect(kcp.calls.filter((c) => c.method === 'GET' && c.url.includes('/secrets'))).toHaveLength(0)
  })

  it('stamps the owner label on every Secret it writes', async () => {
    // Without it the Secret falls outside the provider's label-scoped
    // `secrets` permission claim: it saves fine here (the browser writes as
    // the user, through the hub kcp proxy) and is then invisible to the
    // provider, including to unattended runs, which have no caller token to
    // borrow. The portal is the only writer of the model credential, so this
    // assertion is the whole guard.
    type labelled = { metadata: { labels: Record<string, string> } }
    await resources.createConnection({ name: 'gh2', type: 'github', secret: 'ghp_x' })
    expect((kcp.last('/secrets').body as labelled).metadata.labels).toEqual({ 'railgrid.ai/owner': 'agents' })
    kcp.failNext('GET /modelcredentials/primary', 404, 'NotFound')
    await resources.saveCredential({ name: 'primary', baseURL: 'https://api.openai.com/v1', model: 'gpt-4o', apiKey: 'sk-x' })
    expect((kcp.last('/secrets').body as labelled).metadata.labels).toEqual({ 'railgrid.ai/owner': 'agents' })
  })

  it('refuses a signing secret on a connection that has no use for one', async () => {
    kcp.reply('GET /connections/tg', { metadata: { name: 'tg' }, spec: { type: 'telegram' } })
    await expect(resources.patchConnection('tg', { signingSecret: 'abc' })).rejects.toMatchObject({ status: 400 })
  })

  it('removes the credential Secret with the Connection', async () => {
    await resources.deleteConnection('gh')
    expect(kcp.calls.filter((c) => c.method === 'DELETE').map((c) => c.url.split('/').pop())).toEqual([
      'gh',
      'railgrid-agents-conn-gh',
    ])
  })

  it('tolerates a Connection that never had a Secret', async () => {
    kcp.fail('DELETE /secrets', 404, 'NotFound')
    await expect(resources.deleteConnection('gh')).resolves.toBeUndefined()
  })
})

describe('model credentials', () => {
  it('reads the objects, not the Secrets, and projects the reconciler\'s verdict', async () => {
    // The old shape listed every Secret in the namespace and filtered by name
    // prefix, which pulled real API keys into the browser on every page load
    // just to answer "is one set?". Objects carry no key, and they carry
    // something the Secret never could: whether the endpoint answers.
    kcp.reply('GET /modelcredentials', {
      items: [
        {
          metadata: { name: 'primary' },
          spec: {
            provider: 'openai-compatible',
            baseURL: 'https://api.openai.com/v1',
            model: 'gpt-5',
            secretRef: { name: 'railgrid-agents-model-primary' },
            secretKey: 'apiKey',
          },
          status: {
            models: ['gpt-5', 'gpt-4o'],
            conditions: [
              { type: 'SecretResolved', status: 'True', reason: 'SecretResolved' },
              { type: 'Reachable', status: 'True', reason: 'Reachable' },
              { type: 'Ready', status: 'True', reason: 'Ready' },
            ],
          },
        },
        {
          metadata: { name: 'broken' },
          spec: { provider: 'openai-compatible', baseURL: 'https://x.example/v1', secretRef: { name: 's' } },
          status: {
            conditions: [
              { type: 'SecretResolved', status: 'False', reason: 'SecretUnreadable', message: 'secret "s" is not readable' },
              { type: 'Ready', status: 'False', reason: 'NotReady', message: 'secret "s" is not readable' },
            ],
          },
        },
      ],
    })
    const creds = await resources.listCredentials()
    expect(creds).toEqual([
      {
        name: 'broken', isConnection: true, provider: 'openai-compatible', baseURL: 'https://x.example/v1', model: '',
        secretRef: 's', secretKey: '', ready: false, secretResolved: false,
        statusMessage: 'secret "s" is not readable', discovered: [],
      },
      {
        name: 'primary', provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', model: 'gpt-5',
        secretRef: 'railgrid-agents-model-primary', secretKey: 'apiKey', ready: true, secretResolved: true,
        statusMessage: '', discovered: ['gpt-5', 'gpt-4o'],
      },
    ])
    // No Secret is read at all on a list.
    expect(kcp.calls.filter((c) => c.url.includes('/secrets'))).toHaveLength(0)
  })

  it('leaves a credential the reconciler has not seen yet with an unknown verdict', async () => {
    // "not checked yet" and "checked and broken" must stay distinguishable, or
    // a credential saved a second ago renders as a failure.
    kcp.reply('GET /modelcredentials', { items: [{ metadata: { name: 'fresh' }, spec: { baseURL: 'https://x/v1' } }] })
    const [cred] = await resources.listCredentials()
    expect(cred.ready).toBeUndefined()
    expect(cred.secretResolved).toBeUndefined()
  })

  it('hides only a positively identified migration source, keeping harness and independent same-name objects visible', async () => {
    kcp.reply('GET /modelcredentials', { items: [
      { metadata: { name: 'migrating', uid: 'source-uid' }, spec: { provider: 'openai-compatible', baseURL: 'https://old.example/v1', secretRef: { name: 'old-key' } } },
      { metadata: { name: 'independent', uid: 'independent-uid' }, spec: { provider: 'openai-compatible', baseURL: 'https://legacy.example/v1', secretRef: { name: 'legacy-key' } } },
      { metadata: { name: 'harness', uid: 'harness-uid' }, spec: { provider: 'codex', secretRef: { name: 'harness-key' } } },
    ] })
    kcp.reply('GET /modelproviderconnections', { items: [
      { metadata: { name: 'migrating', uid: 'destination-uid', annotations: { 'agents.railgrid.ai/migrated-model-credential-uid': 'source-uid' } }, spec: { provider: 'openai-compatible', baseURL: 'https://new.example/v1', secretRef: { name: 'new-key' } } },
      { metadata: { name: 'independent', uid: 'other-destination-uid', annotations: { 'agents.railgrid.ai/migrated-model-credential-uid': 'some-other-uid' } }, spec: { provider: 'openai-compatible', baseURL: 'https://connection.example/v1', secretRef: { name: 'connection-key' } } },
      { metadata: { name: 'harness', uid: 'harness-destination-uid', annotations: { 'agents.railgrid.ai/migrated-model-credential-uid': 'harness-uid' } }, spec: { provider: 'openai-compatible', baseURL: 'https://harness.example/v1', secretRef: { name: 'harness-destination-key' } } },
    ] })

    const listed = await resources.listCredentials()
    expect(listed.filter(item => item.name === 'migrating')).toHaveLength(1)
    expect(listed.filter(item => item.name === 'independent')).toHaveLength(2)
    expect(listed.some(item => item.name === 'harness' && item.provider === 'codex')).toBe(true)
    expect(listed.some(item => item.name === 'harness' && item.provider === 'openai-compatible')).toBe(true)
  })

  it('creates the owner before its immutable Secret and activates the matching key', async () => {
    // The owner exists before Secret creation, so the exact UID ownerRef also
    // protects the delete-before-late-POST race. A random Secret ID keeps two
    // writers from ever sharing an upsert target.
    kcp.failNext('GET /modelcredentials/new', 404, 'NotFound')
    await resources.saveCredential({ name: 'new', baseURL: 'https://api.openai.com/v1', apiKey: 'sk-x' })
    const ownerCall = kcp.calls.find(call => call.method === 'POST' && call.url.includes('/modelcredentials'))!
    const secretCall = kcp.calls.find(call => call.method === 'POST' && call.url.includes('/secrets'))!
    expect(kcp.calls.indexOf(ownerCall)).toBeLessThan(kcp.calls.indexOf(secretCall))
    const created = ownerCall.body as { metadata: { name: string; uid?: string; finalizers: string[]; annotations: Record<string, string> }; spec: Record<string, unknown> }
    const secretName = (created.spec.secretRef as { name: string }).name
    const id = secretName.slice('railgrid-agents-model-'.length)
    expect(secretName).toMatch(/^railgrid-agents-model-[0-9a-f]{32}$/)
    expect(created.metadata.finalizers).toContain('agents.railgrid.ai/model-credential-secret-protection')
    expect(created.metadata.annotations).toEqual({ 'agents.railgrid.ai/pending-key-secret': secretName })
    expect(created.spec).toEqual({
      provider: 'openai-compatible',
      baseURL: 'https://api.openai.com/v1',
      secretRef: { name: secretName },
      secretKey: 'apiKey',
    })
    expect(secretCall.url).toContain(`/secrets`)
    expect(secretCall.body).toMatchObject({
      immutable: true,
      metadata: {
        name: secretName,
        labels: { 'railgrid.ai/owner': 'agents' },
        annotations: { 'agents.railgrid.ai/generated-secret-id': id },
        ownerReferences: [{ apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'ModelCredential', name: 'new', uid: 'created-1' }],
      },
      stringData: { apiKey: 'sk-x' },
    })
    expect(kcp.lastOf('PATCH')).toMatchObject({ contentType: 'application/json-patch+json' })
    expect(kcp.lastOf('PATCH').body).toContainEqual({ op: 'test', path: '/metadata/uid', value: 'created-1' })
    expect(kcp.lastOf('PATCH').body).toContainEqual({ op: 'test', path: '/metadata/annotations/agents.railgrid.ai~1pending-key-secret', value: secretName })
    expect(kcp.lastOf('PATCH').body).toContainEqual({ op: 'add', path: '/metadata/annotations/agents.railgrid.ai~1generated-secret-id', value: id })
  })

  it('keeps endpoint and key paired when two verified saves interleave on one resourceVersion', async () => {
    const first = { name: 'primary', provider: 'openai', baseURL: 'https://first.example/v1', apiKey: 'first-key' }
    const second = { name: 'primary', provider: 'openai', baseURL: 'https://second.example/v1', apiKey: 'second-key' }
    await Promise.all([
      resources.withCredentialProbe(first, async () => 'verified'),
      resources.withCredentialProbe(second, async () => 'verified'),
    ])
    kcp.reply('GET /modelcredentials/primary', {
      metadata: { name: 'primary', uid: 'primary-uid', resourceVersion: '9', annotations: {} },
      spec: { provider: 'openai', baseURL: 'https://old.example/v1', secretRef: { name: 'old-key' }, secretKey: 'apiKey' },
    })

    const results = await Promise.allSettled([
      resources.saveCredential(first),
      resources.saveCredential(second),
    ])

    expect(results.filter(result => result.status === 'fulfilled')).toHaveLength(1)
    expect(results.filter(result => result.status === 'rejected')).toHaveLength(1)
    const active = kcp.object('/modelcredentials/primary') as { spec: { baseURL: string; secretRef: { name: string } } }
    const stagedSecrets = kcp.calls.filter(call => call.method === 'POST' && call.url.includes('/secrets')
      && /^railgrid-agents-model-[0-9a-f]{32}$/.test(String((call.body as { metadata?: { name?: string } }).metadata?.name ?? '')))
    expect(stagedSecrets).toHaveLength(1)
    const secretBody = stagedSecrets[0]!.body as { metadata: { name: string }; stringData: { apiKey: string } }
    expect(active.spec.secretRef.name).toBe(secretBody.metadata.name)
    const expectedKey = active.spec.baseURL === 'https://first.example/v1' ? 'first-key' : 'second-key'
    expect(secretBody.stringData.apiKey).toBe(expectedKey)
  })

  it('leaves the active key unchanged when activation loses its compare-and-swap', async () => {
    const originalSpec = { provider: 'openai', baseURL: 'https://old.example/v1', secretRef: { name: 'old-key' }, secretKey: 'apiKey' }
    kcp.reply('GET /modelcredentials/primary', {
      metadata: { name: 'primary', uid: 'primary-uid', resourceVersion: '9', annotations: {} },
      spec: originalSpec,
    })
    kcp.failNextJsonPatch('PATCH /modelcredentials/primary', 409, 'Conflict', 'activation compare failed')

    await expect(resources.saveCredential({ name: 'primary', provider: 'openai', baseURL: 'https://new.example/v1', apiKey: 'new-key' }))
      .rejects.toMatchObject({ status: 409 })

    expect(kcp.object('/modelcredentials/primary')?.spec).toEqual(originalSpec)
    expect((kcp.object('/modelcredentials/primary')?.metadata as { annotations: Record<string, string> }).annotations)
      .not.toHaveProperty('agents.railgrid.ai/pending-key-secret')
    expect(kcp.calls.some(call => call.method === 'DELETE' && call.url.includes('/secrets'))).toBe(false)
    expect(kcp.calls.filter(call => call.method === 'POST' && call.url.includes('/secrets'))).toHaveLength(1)
  })

  it('uses the created UID owner reference when the parent is deleted before its late Secret arrives', async () => {
    const calls: Call[] = []
    let parentDeleted = false
    const fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const url = String(input)
      const method = (init?.method ?? 'GET').toUpperCase()
      const headers = new Headers(init?.headers)
      const body = typeof init?.body === 'string' && init.body ? JSON.parse(init.body) : undefined
      const path = new URL(url, 'https://test.invalid').pathname
      calls.push({ method, url, contentType: headers.get('Content-Type') ?? '', body })
      if (method === 'GET' && path.endsWith('/modelcredentials/deleted')) {
        return jsonResponse(404, { kind: 'Status', status: 'Failure', reason: 'NotFound', message: 'not found', details: { name: 'deleted' }, code: 404 })
      }
      if (method === 'POST' && path.endsWith('/modelcredentials')) {
        parentDeleted = true
        const object = body as { metadata: Record<string, unknown> }
        return jsonResponse(200, { ...object, metadata: { ...object.metadata, uid: 'deleted-parent-uid', resourceVersion: '1' } })
      }
      if (method === 'POST' && path.endsWith('/secrets')) {
        const object = body as { metadata: Record<string, unknown> }
        return jsonResponse(200, { ...object, metadata: { ...object.metadata, uid: 'late-secret-uid', resourceVersion: '1' } })
      }
      if (method === 'GET' && path.endsWith('/modelcredentials/deleted') && parentDeleted) {
        return jsonResponse(404, { kind: 'Status', status: 'Failure', reason: 'NotFound', message: 'deleted', details: { name: 'deleted' }, code: 404 })
      }
      return jsonResponse(500, { kind: 'Status', status: 'Failure', reason: 'InternalError', message: `unexpected ${method} ${path}`, code: 500 })
    }
    resources.setContext({ ...contextWith(CLUSTER), fetch })

    await expect(resources.saveCredential({ name: 'deleted', baseURL: 'https://api.openai.com/v1', apiKey: 'late-key' }))
      .rejects.toMatchObject({ status: 404 })

    const ownerCall = calls.find(call => call.method === 'POST' && call.url.endsWith('/modelcredentials'))!
    const secretCall = calls.find(call => call.method === 'POST' && call.url.endsWith('/secrets'))!
    expect(parentDeleted).toBe(true)
    expect(calls.indexOf(ownerCall)).toBeLessThan(calls.indexOf(secretCall))
    expect((secretCall.body as { metadata: { ownerReferences: Array<{ uid: string; name: string }> } }).metadata.ownerReferences)
      .toEqual([{ apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'ModelCredential', name: 'deleted', uid: 'deleted-parent-uid' }])
    expect(calls.some(call => call.method === 'DELETE')).toBe(false)
  })

  it('creates, reads, updates and clears the Anthropic workspace ID', async () => {
    kcp.failNext('GET /modelcredentials/claude', 404, 'NotFound')
    await resources.saveCredential({ name: 'claude', baseURL: 'https://api.anthropic.com/v1', apiKey: 'test-key', anthropicWorkspaceID: 'wrkspc_team' })
    expect((kcp.calls.find(call => call.method === 'POST' && call.url.includes('/modelcredentials'))!.body as { spec: Record<string, unknown> }).spec.anthropicWorkspaceID).toBe('wrkspc_team')
    kcp = new FakeKcp()
    resources.setContext(contextWith(CLUSTER))
    const object = { metadata: { name: 'claude' }, spec: { baseURL: 'https://api.anthropic.com/v1', secretRef: { name: 's' }, anthropicWorkspaceID: 'wrkspc_team' } }
    kcp.reply('GET /modelcredentials/claude', { ...object, metadata: { ...object.metadata, uid: 'claude-uid', resourceVersion: '4' } })
    kcp.reply('GET /modelcredentials', { items: [object] })
    expect((await resources.listCredentials())[0]?.anthropicWorkspaceID).toBe('wrkspc_team')
    await resources.saveCredential({ name: 'claude', baseURL: object.spec.baseURL, anthropicWorkspaceID: 'wrkspc_other' })
    expect((kcp.lastOf('PATCH').body as { spec: Record<string, unknown> }).spec.anthropicWorkspaceID).toBe('wrkspc_other')
    await resources.saveCredential({ name: 'claude', baseURL: object.spec.baseURL, anthropicWorkspaceID: '' })
    expect((kcp.lastOf('PATCH').body as { spec: Record<string, unknown> }).spec.anthropicWorkspaceID).toBeNull()
  })

  it('rotates an external key by snapshotting it into a new owned Secret', async () => {
    // A hand-written credential may point its key anywhere. Rotation copies
    // the newly entered value into a fresh immutable Secret and never mutates
    // the external Secret the old spec referenced.
    kcp.reply('GET /modelcredentials/byo', {
      metadata: { name: 'byo', uid: 'byo-uid', resourceVersion: '7' },
      spec: { baseURL: 'https://api.openai.com/v1', secretRef: { name: 'team-shared-key' }, secretKey: 'token' },
    })
    await resources.saveCredential({ name: 'byo', baseURL: 'https://api.openai.com/v1', apiKey: 'sk-new', model: 'gpt-5' })
    const secret = kcp.last('/secrets')
    expect(secret.method).toBe('POST')
    const stagedSecretName = (secret.body as { metadata: { name: string } }).metadata.name
    expect(stagedSecretName).not.toBe('team-shared-key')
    expect(stagedSecretName).toMatch(/^railgrid-agents-model-[0-9a-f]{32}$/)
    expect((secret.body as { stringData: Record<string, string> }).stringData).toEqual({ token: 'sk-new' })
    expect(kcp.calls.some(call => call.url.includes('/secrets/team-shared-key'))).toBe(false)
    const activation = kcp.last('/modelcredentials/byo').body as Array<{ op: string; path: string; value?: Record<string, unknown> }>
    expect(activation.find(op => op.op === 'replace' && op.path === '/spec')?.value).toMatchObject({ secretKey: 'token', model: 'gpt-5' })
    expect(activation.find(op => op.op === 'replace' && op.path === '/spec')?.value?.secretRef).not.toEqual({ name: 'team-shared-key' })
  })

  it('does not touch the Secret when an edit does not retype the key', async () => {
    kcp.reply('GET /modelcredentials/primary', {
      metadata: { name: 'primary', uid: 'primary-uid', resourceVersion: '3' },
      spec: { baseURL: 'https://api.openai.com/v1', secretRef: { name: 'railgrid-agents-model-primary' } },
    })
    await resources.saveCredential({ name: 'primary', baseURL: 'https://api.openai.com/v1', model: 'gpt-5' })
    expect(kcp.calls.filter((c) => c.url.includes('/secrets'))).toHaveLength(0)
    expect(kcp.lastOf('PATCH').body).toEqual({
      spec: { provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', model: 'gpt-5' },
      metadata: {
        uid: 'primary-uid', resourceVersion: '3',
        finalizers: ['agents.railgrid.ai/model-credential-secret-protection'],
      },
    })
  })

  it('saves without a model, which is what makes the endpoint askable', async () => {
    // A credential must exist before "which models do you serve?" can be asked
    // of it, so the first save cannot require the answer.
    kcp.failNext('GET /modelcredentials/first', 404, 'NotFound')
    await expect(
      resources.saveCredential({ name: 'first', baseURL: 'https://api.openai.com/v1', apiKey: 'sk-x' }),
    ).resolves.toMatchObject({ name: 'first' })
  })

  it('writes provider connections to their own kind without a model field', async () => {
    kcp.failNext('GET /modelproviderconnections/primary',404,'NotFound')
    await resources.saveCredential({ name: 'primary', baseURL: 'https://api.openai.com/v1', model: '', apiKey: 'test-key' })
    const write = kcp.calls.find(call => call.method === 'POST' && call.url.includes('/modelproviderconnections'))!
    expect(write.url).toContain('/modelproviderconnections')
    expect(write.body).toMatchObject({
      kind: 'ModelProviderConnection',
      metadata: { finalizers: ['agents.railgrid.ai/model-provider-connection-protection'] },
    })
    expect((write.body as { spec: object }).spec).not.toHaveProperty('model')
    const modelConnection = write.body as { metadata: { annotations: Record<string, string> }; spec: { secretRef: { name: string } } }
    const connectionSecret = kcp.calls.find(call => call.method === 'POST' && call.url.includes('/secrets'))!.body as { metadata: { ownerReferences: Array<{ kind: string; name: string; uid: string }> } }
    const secretID = modelConnection.spec.secretRef.name.slice('railgrid-agents-model-'.length)
    expect(secretID).toMatch(/^[0-9a-f]{32}$/)
    expect(modelConnection.metadata.annotations).toEqual({ 'agents.railgrid.ai/pending-key-secret': modelConnection.spec.secretRef.name })
    expect(kcp.lastOf('PATCH').body).toContainEqual({ op: 'add', path: '/metadata/annotations/agents.railgrid.ai~1generated-secret-id', value: secretID })
    expect(connectionSecret.metadata.ownerReferences).toEqual([{
      apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'ModelProviderConnection', name: 'primary', uid: 'created-1',
    }])
  })

  it('switches a provider connection endpoint and key in the same activation', async () => {
    kcp.reply('GET /modelproviderconnections/account', {
      metadata: { name: 'account', uid: 'account-uid', resourceVersion: '14', annotations: {} },
      spec: { provider: 'openai', baseURL: 'https://old.example/v1', secretRef: { name: 'team-owned-key' }, secretKey: 'token' },
    })

    await resources.saveCredential({ name: 'account', provider: 'openai', model: '', baseURL: 'https://new.example/v1', apiKey: 'new-key' })

    const secretCall = kcp.calls.find(call => call.method === 'POST' && call.url.includes('/secrets'))!
    const secretBody = secretCall.body as { metadata: { name: string; ownerReferences: Array<{ kind: string; uid: string }> }; stringData: { token: string } }
    expect(secretBody.stringData).toEqual({ token: 'new-key' })
    expect(secretBody.metadata.ownerReferences).toEqual([{ apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'ModelProviderConnection', name: 'account', uid: 'account-uid' }])
    expect(kcp.calls.some(call => call.url.includes('/secrets/team-owned-key'))).toBe(false)
    const activation = kcp.last('/modelproviderconnections/account').body as Array<{ op: string; path: string; value?: Record<string, unknown> }>
    expect(activation.find(op => op.op === 'replace' && op.path === '/spec')?.value).toMatchObject({
      provider: 'openai', baseURL: 'https://new.example/v1', secretKey: 'token',
      secretRef: { name: secretBody.metadata.name },
    })
  })

  it('still writes a chat endpoint as a stringData apiKey pointed at by spec.secretKey', async () => {
    // The family that has always worked, asserted whole, because the harness
    // branch below must not have moved it: stringData, one key, spec.secretKey
    // naming it, baseURL and model on the object.
    kcp.failNext('GET /modelcredentials/chat', 404, 'NotFound')
    await resources.saveCredential({
      name: 'chat', provider: 'openai', baseURL: 'https://api.openai.com/v1', model: 'gpt-4o', apiKey: 'sk-x',
    })
    const secret = kcp.last('/secrets')
    expect((secret.body as { stringData: Record<string, string> }).stringData).toEqual({ apiKey: 'sk-x' })
    expect(secret.body).not.toHaveProperty('data')
    const created = kcp.calls.find(call => call.method === 'POST' && call.url.includes('/modelcredentials'))!.body as { spec: Record<string, unknown> }
    const secretName = (created.spec.secretRef as { name: string }).name
    expect(secretName).toMatch(/^railgrid-agents-model-[0-9a-f]{32}$/)
    expect(created.spec).toEqual({
      provider: 'openai',
      baseURL: 'https://api.openai.com/v1',
      model: 'gpt-4o',
      secretRef: { name: secretName },
      secretKey: 'apiKey',
    })
  })

  it('writes a claude-code setup token as the Secret KEY that declares it, and never spec.secretKey', async () => {
    // For a harness identity the KEY is the kind: oauthToken is injected as
    // CLAUDE_CODE_OAUTH_TOKEN and apiKey as ANTHROPIC_API_KEY, so the
    // dispatcher branches on which one is present. spec.secretKey naming one
    // of them would be a second answer the Secret could contradict — and there
    // is no baseURL, because nothing here is ever called.
    kcp.failNext('GET /modelcredentials/my-claude', 404, 'NotFound')
    await resources.saveCredential({
      name: 'my-claude', provider: 'claude-code', harnessSecret: { key: 'oauthToken', value: 'sk-ant-oat01-x' },
    })
    const secret = kcp.last('/secrets')
    expect(secret.method).toBe('POST')
    expect(secret.contentType).toBe('application/json')
    expect((secret.body as { data: Record<string, string> }).data).toEqual({ oauthToken: btoa('sk-ant-oat01-x') })
    expect(secret.body).not.toHaveProperty('stringData')
    expect(secret.body).toMatchObject({ immutable: true, metadata: { labels: { 'railgrid.ai/owner': 'agents' } } })
    const created = kcp.calls.find(call => call.method === 'POST' && call.url.includes('/modelcredentials'))!.body as { metadata: { name: string }; spec: Record<string, unknown> }
    expect((created.spec.secretRef as { name: string }).name).toMatch(/^railgrid-agents-model-[0-9a-f]{32}$/)
    expect((secret.body as { metadata: { ownerReferences: Array<{ name: string; uid: string }> } }).metadata.ownerReferences).toEqual([
      { apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'ModelCredential', name: 'my-claude', uid: 'created-1' },
    ])
    expect(kcp.calls.findIndex(call => call.method === 'POST' && call.url.includes('/modelcredentials')))
      .toBeLessThan(kcp.calls.findIndex(call => call.method === 'POST' && call.url.includes('/secrets')))
  })

  it('creates the harness Secret when there is none yet', async () => {
    kcp.failNext('GET /modelcredentials/my-claude', 404, 'NotFound')
    kcp.fail('PATCH /secrets/railgrid-agents-model-my-claude', 404, 'NotFound')
    await resources.saveCredential({
      name: 'my-claude', provider: 'claude-code', harnessSecret: { key: 'oauthToken', value: 'sk-ant-oat01-x' },
    })
    const secret = kcp.last('/secrets')
    expect(secret.method).toBe('POST')
    expect((secret.body as { data: Record<string, string> }).data).toEqual({ oauthToken: btoa('sk-ant-oat01-x') })
    expect((secret.body as { metadata: { labels: Record<string, string> } }).metadata.labels).toEqual({ 'railgrid.ai/owner': 'agents' })
  })

  it('replaces a claude-code key instead of accumulating one, whoever wrote the old one', async () => {
    // This used to rely on server-side apply dropping the key this manager
    // declared last time, which left a key written by anyone else in place —
    // and llm.ReadHarnessSecret refuses a Secret with both as two identities.
    // The merge patch removes the other kind explicitly, so field ownership
    // cannot leave a stale identity behind.
    kcp.reply('GET /modelcredentials/my-claude', {
      metadata: { name: 'my-claude', uid: 'claude-uid', resourceVersion: '4' },
      spec: { provider: 'claude-code', secretRef: { name: 'railgrid-agents-model-my-claude' }, secretKey: 'apiKey' },
    })
    await resources.saveCredential({
      name: 'my-claude', provider: 'claude-code', harnessSecret: { key: 'apiKey', value: 'sk-ant-api03-x' },
    })
    const secret = kcp.last('/secrets')
    expect(secret.method).toBe('POST')
    expect((secret.body as { data: Record<string, string> }).data).toEqual({ apiKey: btoa('sk-ant-api03-x') })
    expect((secret.body as { data: Record<string, string> }).data).not.toHaveProperty('oauthToken')
    const activation = kcp.last('/modelcredentials/my-claude').body as Array<{ op: string; path: string; value?: Record<string, unknown> }>
    const replacedSpec = activation.find(op => op.op === 'replace' && op.path === '/spec')?.value
    expect(replacedSpec?.provider).toBe('claude-code')
    expect(replacedSpec?.secretRef).toEqual({ name: (secret.body as { metadata: { name: string } }).metadata.name })
    expect(replacedSpec).not.toHaveProperty('secretKey')
    expect(kcp.calls.some(call => call.url.endsWith('/secrets/railgrid-agents-model-my-claude'))).toBe(false)
  })

  it('writes a codex login under auth.json', async () => {
    kcp.failNext('GET /modelcredentials/my-codex', 404, 'NotFound')
    await resources.saveCredential({
      name: 'my-codex', provider: 'codex', harnessSecret: { key: 'auth.json', value: '{"tokens":{"access_token":"a"}}' },
    })
    expect((kcp.last('/secrets').body as { data: Record<string, string> }).data).toEqual({
      'auth.json': btoa('{"tokens":{"access_token":"a"}}'),
    })
    const created = kcp.calls.find(call => call.method === 'POST' && call.url.includes('/modelcredentials'))!.body as { spec: Record<string, unknown> }
    expect(created.spec).toMatchObject({ provider: 'codex' })
    expect((created.spec.secretRef as { name: string }).name).toMatch(/^railgrid-agents-model-[0-9a-f]{32}$/)
  })

  it('leaves the stored harness Secret alone when an edit does not retype it', async () => {
    kcp.reply('GET /modelcredentials/my-claude', {
      metadata: { name: 'my-claude', uid: 'claude-uid', resourceVersion: '4' },
      spec: { provider: 'claude-code', secretRef: { name: 'railgrid-agents-model-my-claude' } },
    })
    await resources.saveCredential({ name: 'my-claude', provider: 'claude-code' })
    expect(kcp.calls.filter((c) => c.url.includes('/secrets'))).toHaveLength(0)
    expect(kcp.lastOf('PATCH').body).toEqual({
      spec: { provider: 'claude-code' },
      metadata: {
        uid: 'claude-uid', resourceVersion: '4',
        finalizers: ['agents.railgrid.ai/model-credential-secret-protection'],
      },
    })
  })

  it('refuses a malformed harness secret before a single request is sent', async () => {
    // A half-pasted auth.json and a key belonging to the other harness are both
    // checked here, before the read that would tell us whether the credential
    // exists: neither costs a round-trip to reject.
    await expect(resources.saveCredential({
      name: 'my-codex', provider: 'codex', harnessSecret: { key: 'auth.json', value: 'sk-ant-oat01-wrong-box' },
    })).rejects.toMatchObject({ status: 400 })
    await expect(resources.saveCredential({
      name: 'my-codex', provider: 'codex', harnessSecret: { key: 'oauthToken', value: 'sk-ant-oat01-x' },
    })).rejects.toMatchObject({ status: 400 })
    await expect(resources.saveCredential({
      name: 'my-claude', provider: 'claude-code', harnessSecret: { key: 'auth.json', value: '{}' },
    })).rejects.toMatchObject({ status: 400 })
    expect(kcp.calls).toHaveLength(0)
  })

  it('refuses to create a harness identity with no credential at all', async () => {
    kcp.fail('GET /modelcredentials/my-claude', 404, 'NotFound')
    await expect(
      resources.saveCredential({ name: 'my-claude', provider: 'claude-code' }),
    ).rejects.toMatchObject({ status: 400 })
  })

  it('refuses to create a credential with no key at all', async () => {
    kcp.fail('GET /modelcredentials/new', 404, 'NotFound')
    await expect(
      resources.saveCredential({ name: 'new', baseURL: 'https://api.openai.com/v1' }),
    ).rejects.toMatchObject({ status: 400 })
  })

  it('requires an endpoint, which is the thing the probe calls', async () => {
    await expect(resources.saveCredential({ name: 'primary', apiKey: 'sk-x' })).rejects.toMatchObject({ status: 400 })
  })

  it('deletes legacy models with preconditions and leaves key cleanup to Kubernetes ownership', async () => {
    kcp.reply('GET /modelcredentials/primary', {
      metadata: { name: 'primary', uid: 'model-uid', resourceVersion: '4', finalizers: ['agents.railgrid.ai/model-credential-secret-protection'] },
      spec: { baseURL: 'https://x/v1', secretRef: { name: 'team-shared-key' } },
    })
    kcp.reply('GET /modelcredentials', { items: [] })
    await resources.deleteCredential('primary')
    expect(kcp.calls.filter(c => c.method === 'DELETE').map(c => c.url.split('/').pop())).toEqual(['primary'])
    expect(kcp.lastOf('DELETE').body).toMatchObject({ preconditions: { uid: 'model-uid', resourceVersion: '4' } })
    expect(kcp.calls.some(c => c.url.includes('/secrets'))).toBe(false)
  })

  it('preserves a connection-owned key when deleting an interrupted migration source', async () => {
    kcp.reply('GET /modelcredentials/account', { metadata: { name: 'account', uid: 'source', resourceVersion: '7', finalizers: ['agents.railgrid.ai/model-credential-secret-protection'] }, spec: { model: 'one', secretRef: { name: 'shared-key' } } })
    kcp.reply('GET /modelcredentials', { items: [] })
    kcp.reply('GET /modelproviderconnections/account', { metadata: { name: 'account', uid: 'destination' }, spec: { secretRef: { name: 'shared-key' } } })
    kcp.reply('GET /secrets/shared-key', { metadata: { name: 'shared-key', ownerReferences: [{ kind: 'ModelProviderConnection', uid: 'destination' }] } })
    await resources.deleteCredential('account')
    expect(kcp.calls.filter(c => c.method === 'DELETE').map(c => c.url.split('/').pop())).toEqual(['account'])
    expect(kcp.calls.some(c => c.url.includes('/secrets'))).toBe(false)
  })

  it('does nothing when the model was already deleted', async () => {
    kcp.failNext('GET /modelcredentials/gone', 404, 'NotFound')
    await resources.deleteCredential('gone')
    expect(kcp.calls.some(c => c.method === 'DELETE')).toBe(false)
  })

  it('persists cleanup protection before deleting an unreconciled legacy owner', async () => {
    const protection = 'agents.railgrid.ai/model-credential-secret-protection'
    kcp.reply('GET /modelcredentials/primary', {
      metadata: { name: 'primary', uid: 'source', resourceVersion: '2', finalizers: ['example.com/keep'] },
      spec: { secretRef: { name: 'shared-key' } },
    })
    kcp.reply('GET /modelcredentials', { items: [] })
    kcp.reply('PATCH /modelcredentials/primary', {
      metadata: { name: 'primary', uid: 'source', resourceVersion: '3', finalizers: ['example.com/keep', protection] },
      spec: { secretRef: { name: 'shared-key' } },
    })
    await resources.deleteCredential('primary')
    expect(kcp.lastOf('PATCH').body).toEqual({ metadata: {
      uid: 'source', resourceVersion: '2', finalizers: ['example.com/keep', protection],
    } })
    expect(kcp.lastOf('DELETE').body).toMatchObject({ preconditions: { uid: 'source', resourceVersion: '3' } })
    expect(kcp.calls.findIndex(call => call.method === 'PATCH')).toBeLessThan(kcp.calls.findIndex(call => call.method === 'DELETE'))
    expect(kcp.calls.some(call => call.url.includes('/secrets'))).toBe(false)
  })

  it('does not delete a legacy owner when cleanup protection loses a version race', async () => {
    kcp.reply('GET /modelcredentials/primary', {
      metadata: { name: 'primary', uid: 'source', resourceVersion: '2' },
      spec: { secretRef: { name: 'shared-key' } },
    })
    kcp.reply('GET /modelcredentials', { items: [] })
    kcp.fail('PATCH /modelcredentials/primary', 409, 'Conflict')
    await expect(resources.deleteCredential('primary')).rejects.toMatchObject({ status: 409 })
    expect(kcp.calls.some(call => call.method === 'DELETE')).toBe(false)
  })

  it('does not need Secret access to delete a legacy model', async () => {
    kcp.reply('GET /modelcredentials/primary', { metadata: { name: 'primary', uid: 'source', resourceVersion: '2', finalizers: ['agents.railgrid.ai/model-credential-secret-protection'] }, spec: { secretRef: { name: 'gone-key' } } })
    kcp.reply('GET /modelcredentials', { items: [] })
    kcp.fail('GET /secrets', 403, 'Forbidden')
    await expect(resources.deleteCredential('primary')).resolves.toBeUndefined()
  })
})

describe('credential display names', () => {
  it('renames a model annotation only and preserves the hydrated connection view', async () => {
    const credential = {
      name: 'favorite-model',
      connectionRef: 'account',
      connectionName: 'Team account',
      displayName: 'Old label',
      isConnection: false,
      provider: 'openai',
      baseURL: 'https://api.example/v1',
      model: 'model-one',
      secretRef: 'team-key',
      secretKey: 'token',
      ready: true,
      discovered: ['model-one'],
    }
    kcp.reply('GET /modelcredentials/favorite-model', {
      metadata: {
        name: 'favorite-model', uid: 'model-uid', resourceVersion: '12',
        annotations: { 'example.com/keep': 'yes', 'agents.railgrid.ai/display-name': 'Old label' },
      },
      spec: { connectionRef: 'account', model: 'model-one' },
    })
    kcp.reply('PATCH /modelcredentials/favorite-model', {
      metadata: {
        name: 'favorite-model', uid: 'model-uid', resourceVersion: '13',
        annotations: { 'example.com/keep': 'yes', 'agents.railgrid.ai/display-name': 'Renamed model' },
      },
    })

    const renamed = await resources.renameCredential(credential, '  Renamed model  ')

    expect(renamed).toEqual({ ...credential, displayName: 'Renamed model' })
    expect(kcp.calls).toHaveLength(2)
    expect(kcp.calls[0]).toMatchObject({
      method: 'GET',
      url: `/clusters/${CLUSTER}/apis/agents.railgrid.ai/v1alpha1/modelcredentials/favorite-model`,
      body: undefined,
    })
    expect(kcp.calls[1]).toMatchObject({
      method: 'PATCH',
      url: `/clusters/${CLUSTER}/apis/agents.railgrid.ai/v1alpha1/modelcredentials/favorite-model`,
      contentType: 'application/merge-patch+json',
      body: {
        metadata: {
          uid: 'model-uid',
          resourceVersion: '12',
          annotations: {
            'example.com/keep': 'yes',
            'agents.railgrid.ai/display-name': 'Renamed model',
          },
        },
      },
    })
    expect(kcp.calls[1]?.body).not.toHaveProperty('spec')
    expect(kcp.calls.some(call => call.url.includes('/secrets') || call.url.includes('/test') || call.url.includes('/discover'))).toBe(false)
  })

  it('routes connection renames to ModelProviderConnection and keeps the connection identity', async () => {
    const connection = {
      name: 'account', isConnection: true, displayName: 'Old account', provider: 'openai',
      baseURL: 'https://api.example/v1', secretRef: 'shared-key', secretKey: 'token',
      ready: true, discovered: ['model-one'],
    }
    kcp.reply('GET /modelproviderconnections/account', {
      metadata: { name: 'account', uid: 'connection-uid', resourceVersion: '4', annotations: { 'other.example/note': 'preserve' } },
      spec: { provider: 'openai', baseURL: 'https://api.example/v1', secretRef: { name: 'shared-key' } },
    })
    kcp.reply('PATCH /modelproviderconnections/account', {
      metadata: {
        name: 'account', uid: 'connection-uid', resourceVersion: '5',
        annotations: { 'other.example/note': 'preserve', 'agents.railgrid.ai/display-name': 'New account' },
      },
    })

    const renamed = await resources.renameCredential(connection, 'New account')

    expect(renamed).toEqual({ ...connection, displayName: 'New account' })
    expect(kcp.calls.map(call => [call.method, new URL(call.url, 'https://test.invalid').pathname])).toEqual([
      ['GET', `/clusters/${CLUSTER}/apis/agents.railgrid.ai/v1alpha1/modelproviderconnections/account`],
      ['PATCH', `/clusters/${CLUSTER}/apis/agents.railgrid.ai/v1alpha1/modelproviderconnections/account`],
    ])
    expect(kcp.lastOf('PATCH').body).toEqual({ metadata: {
      uid: 'connection-uid',
      resourceVersion: '4',
      annotations: { 'other.example/note': 'preserve', 'agents.railgrid.ai/display-name': 'New account' },
    } })
    expect(kcp.calls.some(call => call.url.includes('/secrets'))).toBe(false)
  })

  it('validates trimmed display names before making requests', async () => {
    await expect(resources.renameCredential({ name: 'model' }, '   ')).rejects.toMatchObject({ status: 400 })
    await expect(resources.renameCredential({ name: 'model' }, 'x'.repeat(254))).rejects.toMatchObject({ status: 400 })
    expect(kcp.calls).toHaveLength(0)
  })

  it('requires UID and resourceVersion before attempting an annotation patch', async () => {
    kcp.reply('GET /modelcredentials/model', { metadata: { name: 'model', uid: 'model-uid' } })

    await expect(resources.renameCredential({ name: 'model' }, 'Named model')).rejects.toMatchObject({ status: 400 })

    expect(kcp.calls).toHaveLength(1)
    expect(kcp.calls[0]?.method).toBe('GET')
  })

  it('surfaces a resourceVersion conflict without retrying or changing the view', async () => {
    kcp.reply('GET /modelcredentials/model', {
      metadata: { name: 'model', uid: 'model-uid', resourceVersion: '8', annotations: { 'existing.example/key': 'value' } },
      spec: { model: 'model-one' },
    })
    kcp.fail('PATCH /modelcredentials/model', 409, 'Conflict', 'object changed')

    await expect(resources.renameCredential({ name: 'model', model: 'model-one' }, 'Named model')).rejects.toMatchObject({ status: 409, reason: 'Conflict' })

    expect(kcp.calls.map(call => call.method)).toEqual(['GET', 'PATCH'])
    expect(kcp.lastOf('PATCH').body).toEqual({ metadata: {
      uid: 'model-uid',
      resourceVersion: '8',
      annotations: { 'existing.example/key': 'value', 'agents.railgrid.ai/display-name': 'Named model' },
    } })
  })
})

describe('shared provider connections', () => {
  it('creates a model reference without writing or fetching the shared Secret', async () => {
    kcp.failNext('GET /modelcredentials/new-model', 404, 'NotFound')
    kcp.reply('GET /modelproviderconnections/account', { metadata: { name: 'account' }, spec: { provider: 'openai', baseURL: 'https://api.openai.com/v1', secretRef: { name: 'shared-key' } } })
    await resources.saveCredential({ name: 'new-model', connectionRef: 'account', model: 'model-two' })
    const write = kcp.lastOf('POST').body as { spec: unknown }
    expect(write.spec).toEqual({ connectionRef: 'account', model: 'model-two' })
    expect(kcp.calls.some(call => call.url.includes('/secrets'))).toBe(false)
  })

  it('removes one model without deleting its connection or shared key', async () => {
    kcp.reply('GET /modelcredentials/model-two', { metadata: { name: 'model-two' }, spec: { connectionRef: 'account', model: 'two' } })
    await resources.deleteCredential('model-two')
    expect(kcp.calls.filter(call => call.method === 'DELETE').map(call => call.url.split('/').pop())).toEqual(['model-two'])
    expect(kcp.calls.some(call => call.url.includes('/secrets'))).toBe(false)
  })

  it('refuses to delete a provider connection while models reference it', async () => {
    kcp.reply('GET /modelcredentials/account', { metadata: { name: 'account' }, spec: { secretRef: { name: 'shared-key' } } })
    kcp.reply('GET /modelcredentials', { items: [{ metadata: { name: 'model-two' }, spec: { connectionRef: 'account', model: 'two' } }] })
    await expect(resources.deleteCredential('account')).rejects.toMatchObject({ status: 400 })
    expect(kcp.calls.some(call => call.method === 'DELETE')).toBe(false)
  })

  it('hydrates model metadata from the current provider connection', async () => {
    kcp.reply('GET /modelcredentials', { items: [{ metadata: { name: 'model-two' }, spec: { connectionRef: 'account', model: 'two' } }] })
    kcp.reply('GET /modelproviderconnections', { items: [{ kind: 'ModelProviderConnection', metadata: { name: 'account' }, spec: { provider: 'openai', baseURL: 'https://rotated.example/v1', secretRef: { name: 'rotated-key' } } }] })
    const listed = await resources.listCredentials()
    expect(listed.find(item => !item.isConnection && item.name === 'model-two')).toMatchObject({ connectionRef: 'account', model: 'two', baseURL: 'https://rotated.example/v1', secretRef: 'rotated-key' })
    expect(listed.find(item => item.isConnection && item.name === 'account')).toBeTruthy()
    expect(kcp.calls).toHaveLength(2)
  })
})

describe('edges', () => {
  it('reads host edges from the edges group with the same client, and never asks for clusters', async () => {
    // A foreign group's objects, bound in the tenant's own workspace — the same
    // path capabilities() takes for the hub's MCPServer. No provider route, and
    // no new HTTP path.
    kcp.reply('GET /linuxservers', { items: [{ metadata: { name: 'build-01' }, status: { connected: true, phase: 'Ready' } }] })
    kcp.reply('GET /macosservers', { items: [{ metadata: { name: 'mini-02' }, status: { connected: false } }] })
    const edges = await resources.listEdges()

    expect(edges).toEqual([
      { kind: 'LinuxServer', name: 'build-01', connected: true, phase: 'Ready' },
      { kind: 'MacOSServer', name: 'mini-02', connected: false, phase: undefined },
    ])
    expect(kcp.last('/linuxservers').url).toContain(`/clusters/${CLUSTER}/apis/edges.railgrid.ai/v1alpha1/linuxservers`)
    // A KubernetesCluster edge can never host a harness, so the list it would
    // come from is not read at all. That is what makes it unofferable, rather
    // than a filter a later reader can forget.
    expect(kcp.calls.some((c) => c.url.includes('kubernetescluster'))).toBe(false)
  })

  it('reports a missing edges binding as itself, not as the agents provider being absent', async () => {
    // "the edges provider is not enabled here" and "no machines yet" are
    // different things, and only the first one is something the user can act on.
    kcp.fail('GET /linuxservers', 404, 'NotFound', 'the server could not find the requested resource')
    await expect(resources.listEdges()).rejects.toMatchObject({ reason: 'EdgesBindingMissing' })
  })
})


describe('model listing with named connection grants', () => {
  it('resolves named connections when listing all provider accounts is forbidden', async () => {
    kcp.reply('GET /modelcredentials', { items: [
      { metadata: { name: 'fast' }, spec: { connectionRef: 'production', model: 'one' } },
      { metadata: { name: 'careful' }, spec: { connectionRef: 'production', model: 'two' } },
    ] })
    // Override only collection requests; a named get remains authorized.
    resources.setContext({ ...contextWith(CLUSTER), fetch: async (input, init) => {
      if (String(input).split('?')[0].endsWith('/modelproviderconnections')) return jsonResponse(403, { kind: 'Status', reason: 'Forbidden', message: 'list forbidden' })
      return kcp.fetch(input, init)
    } })
    kcp.reply('GET /modelproviderconnections/production', { metadata: { name: 'production' }, spec: { provider: 'openai', baseURL: 'https://api.openai.com/v1' } })
    const listed = await resources.listCredentials()
    expect(listed.find(item => item.name === 'fast')).toMatchObject({ model: 'one', baseURL: 'https://api.openai.com/v1' })
    expect(listed.find(item => item.name === 'careful')).toMatchObject({ model: 'two', baseURL: 'https://api.openai.com/v1' })
    expect(kcp.calls.filter(call => call.url.includes('/modelproviderconnections/production'))).toHaveLength(1)
    expect(kcp.calls.some(call => call.url.includes('/secrets'))).toBe(false)
  })

  it('preserves a readable model and its observed readiness when connection metadata is forbidden', async () => {
    kcp.reply('GET /modelcredentials', { items: [{ metadata: { name: 'fast' }, spec: { connectionRef: 'private', model: 'one' }, status: { conditions: [{ type: 'Ready', status: 'True' }] } }] })
    kcp.fail('GET /modelproviderconnections', 403, 'Forbidden')
    const listed = await resources.listCredentials()
    expect(listed).toEqual([expect.objectContaining({ name: 'fast', model: 'one', ready: true })])
  })

  it('still reports failed model reads instead of hiding them as an empty collection', async () => {
    kcp.fail('GET /modelcredentials', 403, 'Forbidden')
    await expect(resources.listCredentials()).rejects.toMatchObject({ status: 403 })
  })
})

describe('overlay setup permissions', () => {
  it('checks named connection get/use without requesting Secret access', async () => {
    kcp.reply('POST /selfsubjectaccessreviews', { status: { allowed: true } })
    expect(await resources.canConfigureChatModel(['production'])).toBe(true)
    expect(kcp.calls.map(call => (call.body as { spec: { resourceAttributes: unknown } }).spec.resourceAttributes)).toEqual([
      { group: 'agents.railgrid.ai', resource: 'modelcredentials', verb: 'create' },
      { group: 'agents.railgrid.ai', resource: 'modelproviderconnections', verb: 'get', name: 'production' },
      { group: 'agents.railgrid.ai', resource: 'modelproviderconnections', verb: 'use', name: 'production' },
      { group: 'agents.railgrid.ai', resource: 'modelproviderconnections/test', verb: 'create', name: 'production' },
    ])
  })
  it('requires provider and Secret write permissions for a new connection', async () => {
    kcp.reply('POST /selfsubjectaccessreviews', { status: { allowed: true } })
    expect(await resources.canConfigureChatModel([])).toBe(true)
    const attributes = kcp.calls.map(call => (call.body as { spec: { resourceAttributes: unknown } }).spec.resourceAttributes)
    expect(attributes).toContainEqual({ group: '', resource: 'secrets', verb: 'create', namespace: 'default' })
    expect(attributes).toContainEqual({ group: '', resource: 'secrets', verb: 'patch', namespace: 'default' })
    expect(kcp.calls.every(call => call.url.includes('/selfsubjectaccessreviews'))).toBe(true)
  })
  it('does not offer setup to a caller who cannot create models', async () => {
    kcp.reply('POST /selfsubjectaccessreviews', { status: { allowed: false } })
    expect(await resources.canConfigureChatModel(['production'])).toBe(false)
    expect(kcp.calls).toHaveLength(1)
  })
})

describe('bound model recovery metadata', () => {
  it('projects a missing shared key into the model without reading the Secret', async () => {
    kcp.reply('GET /modelcredentials', { items: [{ metadata: { name: 'model' }, spec: { connectionRef: 'account', model: 'one' } }] })
    kcp.reply('GET /modelproviderconnections', { items: [{ metadata: { name: 'account' }, spec: { provider: 'openai', baseURL: 'https://example/v1' }, status: { conditions: [{ type: 'SecretResolved', status: 'False' }] } }] })
    const models = await resources.listCredentials()
    expect(models.find(model => model.name === 'model')).toMatchObject({ secretResolved: false, connectionRef: 'account' })
    expect(kcp.calls.some(call => call.url.includes('/secrets'))).toBe(false)
  })
  it('marks a missing provider connection explicitly', async () => {
    kcp.reply('GET /modelcredentials', { items: [{ metadata: { name: 'model' }, spec: { connectionRef: 'gone', model: 'one' } }] })
    kcp.reply('GET /modelproviderconnections', { items: [] })
    expect((await resources.listCredentials())[0]).toMatchObject({ connectionUnavailable: true, ready: false })
  })
})
