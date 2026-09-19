import { beforeEach, describe, expect, it } from 'vitest'
import { Resources, ResourceError } from '../resources'
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

  /** reply registers a body for `${method} ${pathSuffix}` (suffix-matched). */
  reply(key: string, body: unknown) {
    this.responses.set(key, body)
  }

  fail(key: string, status: number, reason: string, message = reason) {
    this.failures.set(key, { status, reason, message })
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
    const key = [...this.failures.keys()].find((k) => this.matches(k, method, url))
    if (key) {
      const f = this.failures.get(key)!
      return jsonResponse(f.status, { kind: 'Status', status: 'Failure', reason: f.reason, message: f.message, code: f.status })
    }
    const hit = [...this.responses.keys()].find((k) => this.matches(k, method, url))
    return jsonResponse(200, hit ? this.responses.get(hit) : { metadata: { name: 'ok' } })
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

function b64(value: string): string {
  return btoa(value)
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
        models: { chat: 'primary' },
        modelFallbacks: ['a', 'b'],
        budget: { window: 'month', tokenLimit: 1000 },
        // Unknown families are dropped and core is always granted.
        tools: { interactive: { families: ['core', 'web'] } },
      },
    })
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
    expect(kcp.last('/agents/scout').body).toEqual({ spec: { models: { chat: null } } })
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

  it('refuses a trigger source the provider cannot deliver', async () => {
    await expect(resources.createTrigger({ name: 't', agentRef: 'a', source: 'email' })).rejects.toMatchObject({ status: 400 })
  })

  it('never writes a webhook path, because the token is the provider’s to mint', async () => {
    await resources.createTrigger({ name: 't', agentRef: 'a', source: 'webhook' })
    expect(JSON.stringify(kcp.lastOf('POST').body)).not.toContain('webhookPath')
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
    expect(call.body).toEqual({ stringData: { token: 'ghp_new' } })
    // No GET of the Secret: a one-key patch cannot drop the keys it omits.
    expect(kcp.calls.filter((c) => c.method === 'GET' && c.url.includes('/secrets'))).toHaveLength(0)
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
  it('projects the Secret onto a key-free view and ignores unrelated Secrets', async () => {
    kcp.reply('GET /secrets', {
      items: [
        {
          metadata: { name: 'railgrid-agents-model-primary' },
          data: { provider: b64('openai-compatible'), model: b64('gpt-5'), apiKey: b64('sk-secret') },
        },
        { metadata: { name: 'railgrid-agents-conn-gh' }, data: { token: b64('ghp_x') } },
        { metadata: { name: 'unrelated' }, data: {} },
      ],
    })
    const creds = await resources.listCredentials()
    expect(creds).toEqual([
      { name: 'primary', provider: 'openai-compatible', baseURL: '', model: 'gpt-5', hasAPIKey: true },
    ])
    expect(JSON.stringify(creds)).not.toContain('sk-secret')
  })

  it('keeps the stored key when an edit does not retype it', async () => {
    kcp.reply('GET /secrets/railgrid-agents-model-primary', {
      metadata: { name: 'railgrid-agents-model-primary' },
      data: { apiKey: b64('sk-stored') },
    })
    await resources.saveCredential({ name: 'primary', model: 'gpt-5' })
    const body = kcp.lastOf('PATCH').body as { stringData: Record<string, string> }
    expect(body.stringData.apiKey).toBe('sk-stored')
  })

  it('refuses to store a credential with no key at all', async () => {
    kcp.fail('GET /secrets/railgrid-agents-model-new', 404, 'NotFound')
    await expect(resources.saveCredential({ name: 'new', model: 'gpt-5' })).rejects.toMatchObject({ status: 400 })
  })

  it('requires a model, which is the thing an agent references', async () => {
    await expect(resources.saveCredential({ name: 'primary', apiKey: 'sk-x' })).rejects.toMatchObject({ status: 400 })
  })
})
