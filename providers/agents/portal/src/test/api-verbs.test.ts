// Every provider verb is a kcp custom subresource on the bound object, fetched
// on the hub's kcp front door like any other kube path. There is no hub-proxied
// spelling left: only /oauth/providers still goes through the service proxy.
import { afterEach, describe, expect, it } from 'vitest'

import { ApiClient } from '../api'

const CLUSTER = 'c1'
const KUBE = `/clusters/${CLUSTER}/apis/agents.railgrid.ai/v1alpha1`

interface Call { url: string; method: string }

// providerFetch falls back to the global fetch when the host exposes no
// transport, which is what lets the recorder see every call.
function clientWithRecorder(body: unknown = { items: [] }): { api: ApiClient; calls: Call[] } {
  const calls: Call[] = []
  const api = new ApiClient()
  api.setContext({ basePath: '/ui/providers/agents', tenant: CLUSTER, orgUUID: 'o', workspaceUUID: 'w', token: 't' } as never)
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(typeof input === 'string' ? input : (input as Request).url ?? input)
    calls.push({ url, method: init?.method ?? 'GET' })
    return Promise.resolve({
      ok: true, status: 200, statusText: 'OK',
      json: () => Promise.resolve(body),
      text: () => Promise.resolve(JSON.stringify(body)),
      body: null,
    } as unknown as Response)
  }) as typeof fetch
  return { api, calls }
}

describe('verbs are kube paths', () => {
  const realFetch = globalThis.fetch
  afterEach(() => { globalThis.fetch = realFetch })

  it('addresses each verb as a custom subresource on its object', async () => {
    const { api, calls } = clientWithRecorder()
    await api.listSessions('scout')
    await api.deleteSession('scout', 'schedule:daily')
    await api.listMessages('scout', 's 1', 50)
    await api.testCredential('openai', 'gpt-x')
    await api.discoverCredential('openai')
    await api.testConnection('slack')
    await api.runSchedule('daily')
    await api.runTrigger('deploys')
    await api.cancelRun('r1')
    await api.waitRun('r1', 5)

    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      `GET ${KUBE}/agents/scout/sessions`,
      // The session id rides as the verb's one tail segment, unencoded: the
      // provider refuses a percent-encoded path, and ":" is a legal segment
      // character.
      `DELETE ${KUBE}/agents/scout/session/schedule:daily`,
      `GET ${KUBE}/agents/scout/messages?session=s+1&limit=50`,
      `POST ${KUBE}/modelcredentials/openai/test`,
      `POST ${KUBE}/modelcredentials/openai/discover`,
      `POST ${KUBE}/connections/slack/test`,
      `POST ${KUBE}/schedules/daily/run`,
      `POST ${KUBE}/triggers/deploys/run`,
      `POST ${KUBE}/runs/r1/cancel`,
      `GET ${KUBE}/runs/r1/wait?timeoutSeconds=5`,
    ])
    for (const call of calls) {
      expect(call.url).not.toContain('/services/providers/')
      expect(call.url).not.toContain('/dataplane/')
    }
  })

  it('streams chat on the same kube path', async () => {
    const { api, calls } = clientWithRecorder()
    // No body on the stub: the stream fails after the fetch, which is all
    // this asserts on.
    await expect(async () => {
      for await (const _ of api.chatStream('scout', 'hi', 'default')) { /* drain */ }
    }).rejects.toThrow()
    expect(calls).toEqual([{ method: 'POST', url: `${KUBE}/agents/scout/chat` }])
  })

  it('refuses a tail segment the provider would refuse', () => {
    const { api } = clientWithRecorder()
    for (const bad of ['a/b', 'a b', '..', '', 'x?y', 'x%2Fy', 'x#y']) {
      expect(() => api.deleteSession('scout', bad), bad).toThrow('not a valid path segment')
    }
  })

  it('refuses a verb without a workspace or a name', () => {
    // Refused before any request is built, so the failure is synchronous.
    const api = new ApiClient()
    api.setContext({ basePath: '/ui/providers/agents', tenant: '', orgUUID: 'o', workspaceUUID: 'w', token: 't' } as never)
    expect(() => api.listSessions('scout')).toThrow('no workspace selected')
    const { api: scoped } = clientWithRecorder()
    expect(() => scoped.listSessions('')).toThrow('name is required')
  })

  it('keeps only the OAuth probe on the hub service proxy', async () => {
    const { api, calls } = clientWithRecorder({ providers: {} })
    await api.oauthProviders()
    expect(calls).toEqual([{ method: 'GET', url: '/services/providers/agents/oauth/providers' }])
  })
})
