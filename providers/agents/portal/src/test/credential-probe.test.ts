import { describe, expect, it } from 'vitest'
import { ApiClient } from '../api'
import type { RailgridContext } from '../types'

type Call = { url: string; method: string; body: Record<string, any> }
const candidate = { name: 'live-model', provider: 'openai', baseURL: 'https://candidate.example/v1', apiKey: 'new-test-key', model: 'candidate-model' }
const notFound = () => new Response(JSON.stringify({ reason: 'NotFound', code: 404 }), { status: 404 })
function fixture(intercept?: (call: Call, persisted: Map<string, any>) => Response | Promise<Response> | undefined) {
  const calls: Call[] = []
  const persisted = new Map<string, any>()
  const fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const call = { url: String(input), method: init?.method || 'GET', body: init?.body ? JSON.parse(String(init.body)) : {} }
    calls.push(call)
    // Commit first, so rejected responses exercise ambiguous server outcomes.
    if (call.method === 'POST' && call.body.kind) {
      persisted.set(call.url + '/' + call.body.metadata.name, { ...call.body, metadata: { ...call.body.metadata, uid: 'uid-' + call.body.metadata.name, resourceVersion: '1', creationTimestamp: new Date().toISOString() } })
    }
    const response = intercept?.(call, persisted)
    if (response) return response
    if (call.url.endsWith('/test') || call.url.endsWith('/discover')) return new Response(JSON.stringify({ ok: true, latencyMS: 1, models: ['candidate-model'] }))
    if (call.method === 'GET') return persisted.has(call.url) ? new Response(JSON.stringify(persisted.get(call.url))) : notFound()
    if (call.method === 'DELETE') { persisted.delete(call.url); return new Response('{}') }
    return new Response(JSON.stringify(persisted.get(call.url + '/' + call.body.metadata.name)))
  }
  const api = new ApiClient()
  api.setContext({ tenant: 'original', basePath: '/ui/providers/agents', fetch } as RailgridContext)
  return { api, calls, fetch, persisted }
}

describe('exact-candidate model probes', () => {
  it.each([true, false])('keeps keys only in owned Secrets and cleans up exact objects (discovery=%s)', async discover => {
    const { api, calls, persisted } = fixture()
    const started = Date.now()
    expect(await api.probeCredentialDraft(candidate, discover)).toMatchObject({ ok: true })
    const secret = calls.find(call => call.method === 'POST' && call.url.endsWith('/secrets'))!
    const object = calls.find(call => call.method === 'POST' && call.url.endsWith('/modelcredentials'))!
    const verb = calls.find(call => call.url.endsWith(discover ? '/discover' : '/test'))!
    expect(secret.body.stringData).toEqual({ apiKey: 'new-test-key' })
    expect(object.body.spec).toMatchObject({ provider: 'openai', baseURL: candidate.baseURL, secretRef: { name: secret.body.metadata.name } })
    expect(object.body.metadata.name).toMatch(/^model-check-/)
    expect(secret.body.metadata.ownerReferences[0]).toMatchObject({ kind: 'ModelCredential', name: object.body.metadata.name, uid: 'uid-' + object.body.metadata.name })
    const expiry = Date.parse(object.body.metadata.annotations['agents.railgrid.ai/credential-probe-expires-at'])
    expect(expiry).toBeGreaterThanOrEqual(started + 10 * 60 * 1000)
    expect(expiry).toBeLessThanOrEqual(Date.now() + 10 * 60 * 1000)
    expect(secret.body.metadata.annotations).toEqual(object.body.metadata.annotations)
    expect(verb.body).toEqual(discover ? {} : { model: 'candidate-model' })
    expect(JSON.stringify(calls.filter(call => call !== secret))).not.toContain(candidate.apiKey)
    expect(calls.some(call => call.url.includes('/live-model'))).toBe(false)
    const deletes = calls.filter(call => call.method === 'DELETE')
    expect(deletes).toHaveLength(2)
    expect(deletes.every(call => Boolean(call.body.preconditions.uid) && call.body.preconditions.resourceVersion === '1')).toBe(true)
    expect(persisted.size).toBe(0)
  })

  it('cleans up both temporary objects when the provider probe fails without echoing its body', async () => {
    const { api, persisted } = fixture(call => call.url.endsWith('/test')
      ? new Response(JSON.stringify({ message: 'provider unavailable' }), { status: 503 }) : undefined)
    await expect(api.probeCredentialDraft(candidate, false)).rejects.toMatchObject({ status: 503, technicalDiagnostic: 'provider unavailable' })
    expect(persisted.size).toBe(0)
  })

  it.each(['secrets', 'modelcredentials'])('cleans up an ambiguous committed %s POST whose response was lost', async resource => {
    const { api, calls, persisted } = fixture(call => call.method === 'POST' && call.url.endsWith('/' + resource)
      ? Promise.reject(new TypeError('response lost')) : undefined)
    await expect(api.probeCredentialDraft(candidate, false)).rejects.toThrow()
    expect(persisted.size).toBe(0)
    expect(calls.some(call => call.url.endsWith('/test'))).toBe(false)
  })

  it('does not create a Secret if credential creation is denied', async () => {
    const { api, calls, persisted } = fixture((call, objects) => {
      if (call.method !== 'POST' || !call.url.endsWith('/modelcredentials')) return
      objects.delete(call.url + '/' + call.body.metadata.name)
      return new Response(JSON.stringify({ message: 'denied', reason: 'Forbidden' }), { status: 403 })
    })
    await expect(api.probeCredentialDraft(candidate, false)).rejects.toMatchObject({ status: 403 })
    expect(calls.some(call => call.method === 'POST' && call.url.endsWith('/secrets'))).toBe(false)
    expect(persisted.size).toBe(0)
  })

  it.each([409, 202])('keeps the credential while its Secret remains after DELETE status %s', async status => {
    const { api, calls, persisted } = fixture(call => call.method === 'DELETE' && call.url.includes('/secrets/')
      ? new Response(JSON.stringify({ reason: status === 409 ? 'Conflict' : 'Success', code: status }), { status }) : undefined)
    await expect(api.probeCredentialDraft(candidate, false)).rejects.toMatchObject({
      reason: 'ProbeCleanupFailed',
      message: expect.stringContaining('Automatic cleanup will retry'),
      technicalDiagnostic: expect.stringContaining('Secret railgrid-agents-model-model-check-'),
    })
    expect(calls.some(call => call.method === 'DELETE' && call.url.includes('/modelcredentials/'))).toBe(false)
    expect(persisted.size).toBe(2)
  })

  it.each(['expired', 'removed', 'terminating', 'endpoint changed'])('does not call the model when the candidate is %s during Secret creation', async change => {
    const { api, calls, persisted } = fixture((call, objects) => {
      if (call.method !== 'POST' || !call.url.endsWith('/secrets')) return
      for (const [path, object] of objects.entries()) {
        if (object.kind !== 'ModelCredential') continue
        if (change === 'removed') objects.delete(path)
        else if (change === 'expired') object.metadata.creationTimestamp = new Date(Date.now() - 11 * 60 * 1000).toISOString()
        else if (change === 'terminating') object.metadata.deletionTimestamp = new Date().toISOString()
        else object.spec.baseURL = 'https://different.example/v1'
      }
      return undefined
    })
    await expect(api.probeCredentialDraft(candidate, false)).rejects.toThrow()
    expect(calls.some(call => call.url.endsWith('/test'))).toBe(false)
    expect(persisted.size).toBe(0)
  })

  it('does not claim verification succeeded or delete a replacement when ownership changed', async () => {
    const { api, calls } = fixture((call, objects) => {
      if (call.url.endsWith('/test')) for (const object of objects.values()) object.metadata.annotations['agents.railgrid.ai/credential-probe-id'] = 'different-owner'
      return undefined
    })
    await expect(api.probeCredentialDraft(candidate, false)).rejects.toMatchObject({ reason: 'ProbeCleanupFailed' })
    expect(calls.some(call => call.method === 'DELETE')).toBe(false)
  })

  it.each(['owner', 'uid'])('leaves a Secret alone when its %s no longer matches the created probe', async changed => {
    const { api, calls } = fixture((call, objects) => {
      if (call.url.endsWith('/test')) for (const object of objects.values()) {
        if (object.kind !== 'Secret') continue
        if (changed === 'owner') object.metadata.ownerReferences[0].uid = 'unrelated-owner'
        else object.metadata.uid = 'replacement-uid'
      }
      return undefined
    })
    await expect(api.probeCredentialDraft(candidate, false)).rejects.toMatchObject({ reason: 'ProbeCleanupFailed' })
    expect(calls.some(call => call.method === 'DELETE' && call.url.includes('/secrets/'))).toBe(false)
  })

  it('retains the host authority fence after scope changes instead of sending cleanup in a new workspace', async () => {
    let current = true
    const { api, calls, fetch, persisted } = fixture(call => {
      if (call.method === 'POST' && call.url.endsWith('/secrets')) current = false
      return undefined
    })
    // Production host fetch guards before AND after awaiting the HTTP response.
    const guardedFetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      if (!current) throw new DOMException('Workspace changed', 'AbortError')
      const response = await fetch(input, init)
      if (!current) throw new DOMException('Workspace changed', 'AbortError')
      return response
    }
    api.setContext({ tenant: 'original', basePath: '/ui/providers/agents', fetch: guardedFetch } as RailgridContext)
    await expect(api.probeCredentialDraft(candidate, false)).rejects.toMatchObject({ reason: 'ProbeCleanupFailed' })
    expect(calls.every(call => call.url.startsWith('/clusters/original/'))).toBe(true)
    expect(calls.some(call => call.method === 'DELETE')).toBe(false)
    // No false client-side cleanup guarantee: server lifecycle cleanup is required.
    expect(persisted.size).toBe(2)
  })
})
