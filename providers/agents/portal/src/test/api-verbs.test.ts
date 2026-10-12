// Every provider verb is a kcp custom subresource on the bound object, fetched
// on the hub's kcp front door like any other kube path. There is no hub-proxied
// spelling left: only /oauth/providers still goes through the service proxy.
import { afterEach, describe, expect, it } from 'vitest'

import { ApiClient } from '../api'

const CLUSTER = 'c1'
const KUBE = `/clusters/${CLUSTER}/apis/agents.railgrid.ai/v1alpha1`

interface Call { url: string; method: string }

// bodies records each request body by call index, apart from `calls`, so the
// URL assertions stay exact while a verb's payload can still be checked.
const bodies = new Map<number, unknown>()

// providerFetch falls back to the global fetch when the host exposes no
// transport, which is what lets the recorder see every call.
function clientWithRecorder(body: unknown = { items: [] }): { api: ApiClient; calls: Call[] } {
  const calls: Call[] = []
  bodies.clear()
  const api = new ApiClient()
  api.setContext({ basePath: '/ui/providers/agents', tenant: CLUSTER, orgUUID: 'o', workspaceUUID: 'w', token: 't' } as never)
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(typeof input === 'string' ? input : (input as Request).url ?? input)
    if (typeof init?.body === 'string') {
      try { bodies.set(calls.length, JSON.parse(init.body)) } catch { bodies.set(calls.length, init.body) }
    }
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
    await api.enableSchedule('daily')
    await api.runTrigger('deploys')
    await api.enableTrigger('deploys')
    await api.evaluateTriggerFilter('deploys', { filter: { eventType: 'push' }, deliveryID: 'd1' })
    await api.replayTriggerDelivery('deploys', 'd1')
    await api.listWebhookDeliveries('github-org', { limit: 20, outcome: 'filtered', trigger: 'on-pr' })
    await api.listWebhookDeliveries('github-org')
    await api.getWebhookDelivery('github-org', 'd1')
    await api.runWebhook('github-org', { deliveryID: 'd1' })
    await api.runWebhook('github-org', { headers: { 'X-GitHub-Event': 'push' }, body: '{}' })
    await api.cancelRun('r1')
    await api.waitRun('r1', 5)

    const bodyOf = (url: string, nth = 0) => {
      const index = calls.map((c, i) => ({ c, i })).filter(({ c }) => c.method === 'POST' && c.url === url)[nth]?.i
      return index === undefined ? undefined : bodies.get(index)
    }
    expect(bodyOf(`${KUBE}/triggers/deploys/run`, 1)).toEqual({ deliveryID: 'd1' })
    expect(bodyOf(`${KUBE}/webhooks/github-org/run`, 0)).toEqual({ deliveryID: 'd1' })
    expect(bodyOf(`${KUBE}/webhooks/github-org/run`, 1)).toEqual({ headers: { 'X-GitHub-Event': 'push' }, body: '{}' })
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
      `POST ${KUBE}/schedules/daily/enable`,
      `POST ${KUBE}/triggers/deploys/run`,
      `POST ${KUBE}/triggers/deploys/enable`,
      `POST ${KUBE}/triggers/deploys/evaluate`,
      // Replaying one webhook delivery through one trigger is the trigger's
      // plain run verb with a {deliveryID} body.
      `POST ${KUBE}/triggers/deploys/run`,
      // The delivery sink belongs to the Webhook, not to any trigger.
      `GET ${KUBE}/webhooks/github-org/deliveries?limit=20&outcome=filtered&trigger=on-pr`,
      `GET ${KUBE}/webhooks/github-org/deliveries`,
      `GET ${KUBE}/webhooks/github-org/deliveries/d1`,
      `POST ${KUBE}/webhooks/github-org/run`,
      `POST ${KUBE}/webhooks/github-org/run`,
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
