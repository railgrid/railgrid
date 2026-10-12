// The Webhooks page, its delivery log and the trigger filter tester: what
// arrived at a webhook and what each routed trigger made of it, and how a
// trigger's filter being edited is judged against that traffic — always by
// the provider's `evaluate` verb, never by a second matcher in the browser.
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ServerEvent } from '../store'
import type { Trigger, TriggerFilterVerdict, Webhook, WebhookDelivery } from '../types'
import { clearToasts } from '../ui/toast'
import Automation from '../views/Automation.vue'
import TriggerFilterTester from '../views/TriggerFilterTester.vue'
import WebhookDeliveryLog from '../views/WebhookDeliveryLog.vue'
import Webhooks from '../views/Webhooks.vue'
import { deliveryOptionLabel, deliveryWhat, parseHeaderLines, routeText } from '../views/webhook-deliveries'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text, type MountedVue } from './vue-helper'

const mounted: MountedVue[] = []
afterEach(() => {
  for (const view of mounted.splice(0)) view.unmount()
  clearToasts()
})

async function mount(component: Parameters<typeof mountVue>[0], props: Record<string, unknown>): Promise<MountedVue> {
  const view = await mountVue(component, props)
  mounted.push(view)
  return view
}

const webhookPath = '/services/providers/agents/webhooks/c1/github-org/0123456789abcdef'
const webhook: Webhook = {
  metadata: { name: 'github-org', uid: 'w1' },
  spec: { source: 'github', displayName: 'GitHub org' },
  status: {
    webhookPath,
    lastReceived: new Date().toISOString(),
    lastDelivery: { time: new Date().toISOString(), outcome: 'filtered', reason: 'no trigger matched', routed: 2, fired: 0 },
    routes: [{ name: 'on-pr' }, { name: 'on-push' }],
  },
}

const trigger: Trigger = {
  metadata: { name: 'on-pr', uid: 'u1' },
  spec: { agentRef: 'scout', webhookRef: 'github-org', task: 'review it', filter: { eventType: 'pull_request' } },
  status: { lastReceived: new Date().toISOString(), lastDelivery: { time: new Date().toISOString(), outcome: 'filtered', reason: 'eventType does not match pull_request' } },
}

const deliveries: WebhookDelivery[] = [
  {
    id: 'd-pr', webhookName: 'github-org', deliveryID: 'gh-2', receivedAt: new Date().toISOString(), outcome: 'fired',
    eventType: 'pull_request', action: 'opened', repository: 'octo/api', headers: { 'X-Github-Event': 'pull_request' }, body: '{"action":"opened"}', bodySize: 19,
    routes: [{ trigger: 'on-pr', outcome: 'fired', runID: 'r-1' }, { trigger: 'on-push', outcome: 'filtered', reason: 'eventType does not match push' }],
  },
  {
    id: 'd-push', webhookName: 'github-org', deliveryID: 'gh-1', receivedAt: new Date(Date.now() - 60_000).toISOString(), outcome: 'filtered',
    eventType: 'push', repository: 'octo/api', headers: { 'X-Github-Event': 'push', 'X-Hub-Signature-256': '[redacted]' }, body: '{"ref":"refs/heads/main"', bodySize: 600,
    routes: [{ trigger: 'on-pr', outcome: 'filtered', reason: 'eventType does not match pull_request' }, { trigger: 'on-push', outcome: 'suspended' }],
  },
  {
    id: 'd-ping', webhookName: 'github-org', receivedAt: new Date(Date.now() - 120_000).toISOString(), outcome: 'unrouted',
    eventType: 'ping', bodySize: 2, body: '{}', routes: [],
  },
]

function buttons(root: Element): HTMLButtonElement[] {
  return [...root.querySelectorAll<HTMLButtonElement>('button')]
}
function button(root: Element, label: string): HTMLButtonElement {
  const found = buttons(root).find(b => text(b).includes(label) || b.getAttribute('aria-label')?.includes(label))
  if (!found) throw new Error(`no button "${label}" among ${buttons(root).map(b => text(b) || b.getAttribute('aria-label')).join(' | ')}`)
  return found
}
/** pick opens a portalled FormSelect by its label id and clicks the matching option. */
async function pick(root: ParentNode, labelledby: string, match: (label: string) => boolean): Promise<void> {
  const select = [...root.querySelectorAll<HTMLElement>('[data-form-select]')]
    .find(c => c.querySelector('[role="combobox"]')?.getAttribute('aria-labelledby')?.includes(labelledby))!
  select.querySelector<HTMLButtonElement>('[role="combobox"]')!.click()
  await settleVue()
  ;[...document.querySelectorAll<HTMLElement>('[role="option"]')].find(o => match(text(o)))!.click()
  await settleVue()
}
function setValue(element: HTMLInputElement | HTMLTextAreaElement, value: string): void {
  element.value = value
  element.dispatchEvent(new Event('input', { bubbles: true }))
}

describe('webhook-deliveries helpers', () => {
  it('names a delivery by what a filter would see', () => {
    expect(deliveryWhat({ eventType: 'pull_request', action: 'opened' })).toBe('pull_request · opened')
    expect(deliveryWhat({ eventType: 'opened', action: 'opened' })).toBe('opened')
    expect(deliveryWhat({ contentType: 'application/json; charset=utf-8' })).toBe('application/json')
  })
  it('spells one route as trigger, outcome and reason', () => {
    expect(routeText({ trigger: 'on-pr', outcome: 'filtered', reason: 'eventType does not match pull_request' })).toBe('on-pr filtered: eventType does not match pull_request')
    expect(routeText({ trigger: 'on-push', outcome: 'fired' })).toBe('on-push fired')
  })
  it('labels a picker option with the one trigger’s route when asked', () => {
    const when = () => 'now'
    expect(deliveryOptionLabel(deliveries[0], when)).toBe('now · pull_request · opened · octo/api · fired')
    expect(deliveryOptionLabel(deliveries[0], when, 'on-push')).toBe('now · pull_request · opened · octo/api · filtered')
    expect(deliveryOptionLabel(deliveries[2], when, 'on-pr')).toBe('now · ping · not routed here')
  })
  it('parses pasted header lines leniently', () => {
    expect(parseHeaderLines('X-GitHub-Event: push\nnot a header\nX-Env:prod\n')).toEqual({ 'X-GitHub-Event': 'push', 'X-Env': 'prod' })
  })
})

describe('WebhookDeliveryLog.vue', () => {
  it('lists deliveries with the rollup and one line per route, filters by chip, and replays to every route', async () => {
    const listWebhookDeliveries = vi.fn().mockResolvedValue({ items: deliveries, retention: 200 })
    const runWebhook = vi.fn().mockResolvedValue({ deliveryID: 'd-4', status: 'fired', routes: [{ trigger: 'on-push', outcome: 'fired', runID: 'r-9' }] })
    const getWebhookDelivery = vi.fn().mockResolvedValue({ ...deliveries[1], body: '{"ref":"refs/heads/main","whole":true}' })
    const api = stubApi({ listWebhookDeliveries, runWebhook, getWebhookDelivery })
    const store = makeStore(api)
    const view = await mount(WebhookDeliveryLog, { api, store, webhook })
    await settleVue()

    expect(listWebhookDeliveries).toHaveBeenCalledWith('github-org', { limit: 100, trigger: undefined })
    expect(text(view.element)).toContain('keeps the last 200')
    const rows = [...view.element.querySelectorAll('.agents-delivery')]
    expect(rows).toHaveLength(3)
    expect(text(rows[0].querySelector('.agents-delivery-outcome'))).toContain('Fired')
    // Which trigger fired and why the others did not, per route.
    const routes = [...rows[0].querySelectorAll('.agents-delivery-route')]
    expect(text(routes[0])).toContain('on-pr fired')
    expect(text(routes[1])).toContain('on-push filtered: eventType does not match push')
    button(routes[0], 'View run').click()
    expect(view.navigations).toEqual([{ kind: 'run', id: 'r-1' }])
    expect(buttons(routes[1]).some(b => text(b) === 'View run')).toBe(false)
    expect(text(rows[2])).toContain('Unrouted')
    expect(text(rows[2])).toContain('No trigger routed from this webhook')

    button(view.element, 'Filtered').click()
    await settleVue()
    expect(view.element.querySelectorAll('.agents-delivery')).toHaveLength(1)
    expect(text(view.element.querySelector('.agents-delivery'))).toContain('push')
    button(view.element, 'Unrouted').click()
    await settleVue()
    expect(view.element.querySelectorAll('.agents-delivery')).toHaveLength(1)
    expect(text(view.element.querySelector('.agents-delivery'))).toContain('ping')
    button(view.element, 'All').click()
    await settleVue()

    // Details: recorded headers (redacted value kept as such) and a previewed
    // body that can be loaded whole.
    button(view.element.querySelectorAll('.agents-delivery')[1], 'Show delivery details').click()
    await settleVue()
    const detail = view.element.querySelector('.agents-delivery-detail')!
    expect(text(detail)).toContain('X-Hub-Signature-256')
    expect(text(detail)).toContain('[redacted]')
    expect(text(detail)).toContain('600 bytes')
    button(detail, 'Show whole payload').click()
    await settleVue()
    expect(getWebhookDelivery).toHaveBeenCalledWith('github-org', 'd-push')
    expect(text(view.element.querySelector('.agents-delivery-pre'))).toContain('"whole":true')

    button(view.element, 'Replay delivery gh-1').click()
    await settleVue()
    expect(runWebhook).toHaveBeenCalledWith('github-org', { deliveryID: 'd-push' })
    // A replay refreshes the log and offers the one run it started.
    expect(listWebhookDeliveries).toHaveBeenCalledTimes(2)
    expect(text(document.body)).toContain('on-push fired')
  })

  it('narrows to one trigger’s deliveries on the server when a route is picked', async () => {
    const listWebhookDeliveries = vi.fn().mockResolvedValue({ items: deliveries, retention: 200 })
    const api = stubApi({ listWebhookDeliveries })
    const store = makeStore(api)
    const view = await mount(WebhookDeliveryLog, { api, store, webhook })
    await settleVue()

    await pick(view.element, 'agents-deliveries-route-label', label => label === 'on-push')
    expect(listWebhookDeliveries).toHaveBeenLastCalledWith('github-org', { limit: 100, trigger: 'on-push' })
  })

  it('says when nothing has arrived, and refreshes on a push for this webhook only', async () => {
    vi.useFakeTimers()
    try {
      const listWebhookDeliveries = vi.fn().mockResolvedValue({ items: [], retention: 0 })
      const api = stubApi({ listWebhookDeliveries })
      const store = makeStore(api)
      const view = await mount(WebhookDeliveryLog, { api, store, webhook })
      await settleVue()
      expect(text(view.element)).toContain('Nothing has reached this webhook URL yet')
      expect(text(view.element)).toContain('keeps no log')

      const push = (name: string) => store.applyServerEvent({ type: 'webhook-delivery', data: { webhook: name, id: 'x', outcome: 'fired', routes: [] } } as ServerEvent)
      push('other')
      push('github-org')
      push('github-org')
      await vi.advanceTimersByTimeAsync(600)
      // One initial load plus one debounced refresh for the two matching pushes.
      expect(listWebhookDeliveries).toHaveBeenCalledTimes(2)
      // An unrouted delivery belongs to no agent stream and pushes nothing, so
      // the open log also polls.
      await vi.advanceTimersByTimeAsync(10_000)
      expect(listWebhookDeliveries).toHaveBeenCalledTimes(3)
      view.unmount()
      await vi.advanceTimersByTimeAsync(20_000)
      expect(listWebhookDeliveries).toHaveBeenCalledTimes(3)
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('Webhooks.vue', () => {
  it('lists webhooks with sender, routed trigger count, last delivery and a URL copy action', async () => {
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
    const api = stubApi()
    const store = makeStore(api)
    store.webhooks.data = [
      webhook,
      {
        metadata: { name: 'deploys' }, spec: { source: 'webhook' },
        status: { conditions: [{ type: 'Validated', status: 'False', reason: 'NotMinted', message: 'token key missing' }] },
      },
    ]
    store.webhooks.loaded = true
    store.webhooks.hasSnapshot = true
    const view = await mount(Webhooks, { store, api })
    await settleVue()

    const rows = [...view.element.querySelectorAll('.k-table__row')]
    const org = rows.find(row => text(row).includes('GitHub org'))!
    expect(text(org)).toContain('GitHub webhook')
    expect(text(org)).toContain('2 triggers')
    expect(text(org)).toContain('Filtered')
    expect(text(org)).toContain('fired 0 of 2')
    const deploys = rows.find(row => text(row).includes('deploys'))!
    expect(text(deploys)).toContain('0 triggers')
    expect(text(deploys)).toContain('Nothing received')
    expect(text(deploys.querySelector('.agents-badge-warn'))).toContain('NotMinted')
    // Only a minted webhook has a URL to copy.
    expect(deploys.querySelector('button[aria-label^="Copy webhook URL"]')).toBeNull()
    org.querySelector<HTMLButtonElement>('button[aria-label="Copy webhook URL for github-org"]')!.click()
    await settleVue()
    expect(writeText).toHaveBeenCalledWith(`${location.origin}${webhookPath}`)

    org.querySelector<HTMLButtonElement>('button[aria-label="Open webhook github-org"]')!.click()
    expect(view.navigations).toEqual([{ kind: 'edit', resource: 'webhook', name: 'github-org' }])
  })

  it('creates the Webhook object with the typed settings and validates the retention', async () => {
    const created: Webhook = { metadata: { name: 'gh' }, spec: { source: 'github' } }
    const createWebhook = vi.fn().mockResolvedValue(created)
    const api = stubApi({ createWebhook })
    const store = makeStore(api)
    const view = await mount(Webhooks, { store, api, createRoute: true, authorityEpoch: 0, createSession: 1 })
    await settleVue()

    const form = view.element.querySelector<HTMLFormElement>('form')!
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue()
    expect(text(view.element.querySelector('#webhook-create-name-error'))).toContain('A name is required')

    setValue(view.element.querySelector<HTMLInputElement>('input[name="name"]')!, 'gh')
    setValue(view.element.querySelector<HTMLInputElement>('input[name="displayName"]')!, 'GitHub')
    await pick(view.element, 'webhook-create-source-label', label => label.includes('GitHub webhook'))
    const retention = view.element.querySelector<HTMLInputElement>('input[name="deliveryRetention"]')!
    expect(retention.placeholder).toBe('200')
    setValue(retention, '5000')
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue()
    expect(createWebhook).not.toHaveBeenCalled()
    expect(text(view.element.querySelector('#webhook-create-retention-error'))).toContain('between 0 and 1000')

    setValue(retention, '50')
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue()
    expect(createWebhook).toHaveBeenCalledWith({ name: 'gh', source: 'github', displayName: 'GitHub', deliveryRetention: 50 })
    expect(view.events['create-success']?.[0]).toMatchObject({ resource: 'webhook', name: 'gh', item: created })
  })

  it('shows the URL, the routed triggers, the log, and sends a sample through webhooks/run', async () => {
    const listWebhookDeliveries = vi.fn().mockResolvedValue({ items: deliveries, retention: 200 })
    const runWebhook = vi.fn().mockResolvedValue({ deliveryID: 'd-5', status: 'filtered', routes: [{ trigger: 'on-pr', outcome: 'filtered', reason: 'action does not match' }] })
    const api = stubApi({ listWebhookDeliveries, runWebhook })
    const store = makeStore(api)
    store.webhooks.data = [webhook]
    store.webhooks.loaded = true
    store.webhooks.hasSnapshot = true
    store.triggers.data = [trigger]
    store.triggers.hasSnapshot = true
    const view = await mount(Webhooks, { store, api, editRoute: true, editName: 'github-org' })
    await settleVue()

    expect(text(view.element.querySelector('[data-testid="webhook-url"]'))).toBe(`${location.origin}${webhookPath}`)
    expect(text(view.element)).toContain('Settings → Webhooks')
    // on-pr is known with its agent; on-push only through the webhook status.
    const routes = view.element.querySelector('[data-testid="webhook-routes"]')!
    expect(text(routes)).toContain('on-pr')
    expect(text(routes)).toContain('on agent scout')
    expect(text(routes)).toContain('on-push')
    ;[...routes.querySelectorAll<HTMLAnchorElement>('a')].find(link => text(link) === 'on-pr')!.click()
    expect(view.navigations).toEqual([{ kind: 'agent', name: 'scout', tab: 'automation' }])
    expect(view.element.querySelector('[data-testid="webhook-deliveries"]')).not.toBeNull()
    expect(listWebhookDeliveries).toHaveBeenCalledTimes(1)

    // The sample starts from GitHub's shape for a GitHub webhook.
    expect(view.element.querySelector<HTMLTextAreaElement>('textarea[name="sampleHeaders"]')!.value).toBe('X-GitHub-Event: pull_request')
    setValue(view.element.querySelector<HTMLTextAreaElement>('textarea[name="sampleHeaders"]')!, 'X-GitHub-Event: issues\nX-Env: prod')
    setValue(view.element.querySelector<HTMLTextAreaElement>('textarea[name="sampleBody"]')!, '{"action":"closed"}')
    button(view.element, 'Send sample').click()
    await settleVue()
    expect(runWebhook).toHaveBeenCalledWith('github-org', { headers: { 'X-GitHub-Event': 'issues', 'X-Env': 'prod' }, body: '{"action":"closed"}' })
    expect(text(document.body)).toContain('on-pr filtered: action does not match')
    // The log picks the injected delivery up.
    expect(listWebhookDeliveries).toHaveBeenCalledTimes(2)
  })

  it('says the URL is not minted yet and saves settings as a merge patch', async () => {
    const patchWebhook = vi.fn().mockResolvedValue({ metadata: { name: 'deploys' }, spec: { source: 'webhook' } })
    const api = stubApi({ patchWebhook, listWebhookDeliveries: vi.fn().mockResolvedValue({ items: [], retention: 10 }) })
    const store = makeStore(api)
    store.webhooks.data = [{ metadata: { name: 'deploys' }, spec: { source: 'webhook', deliveryRetention: 10 } }]
    store.webhooks.loaded = true
    store.webhooks.hasSnapshot = true
    const view = await mount(Webhooks, { store, api, editRoute: true, editName: 'deploys' })
    await settleVue()

    expect(view.element.querySelector('[data-testid="webhook-url"]')).toBeNull()
    expect(text(view.element)).toContain('has not been minted yet')
    expect(text(view.element)).toContain('No trigger routes from this webhook yet')
    const retention = view.element.querySelector<HTMLInputElement>('#webhook-edit-retention')!
    expect(retention.value).toBe('10')
    setValue(retention, '')
    button(view.element, 'Save changes').click()
    await settleVue()
    expect(patchWebhook).toHaveBeenCalledWith('deploys', { source: 'webhook', displayName: '', deliveryRetention: null })
  })
})

describe('TriggerFilterTester.vue', () => {
  const verdictFor = (filter: Record<string, string>, deliveryID?: string): TriggerFilterVerdict => ({
    matched: filter.eventType === 'push',
    reason: filter.eventType === 'push' ? undefined : 'eventType does not match pull_request',
    keys: Object.entries(filter).map(([key, want]) => ({ key, want, got: key === 'eventType' ? 'push' : '', matched: key !== 'eventType' || want === 'push', known: key !== 'labels', reason: key === 'labels' ? 'labels is not a filter key and is ignored' : undefined })),
    extracted: { eventType: 'push', repository: 'octo/api' },
    deliveryID,
    filter,
  })

  it('lists the trigger’s webhook deliveries and evaluates the live filter against the picked one', async () => {
    const listWebhookDeliveries = vi.fn().mockResolvedValue({ items: [deliveries[1], deliveries[0]], retention: 200 })
    const evaluateTriggerFilter = vi.fn().mockImplementation((_name: string, body: { filter: Record<string, string>; deliveryID?: string }) => Promise.resolve(verdictFor(body.filter, body.deliveryID)))
    const api = stubApi({ listWebhookDeliveries, evaluateTriggerFilter })
    const store = makeStore(api)
    const view = await mount(TriggerFilterTester, { api, store, trigger, filter: { eventType: 'pull_request', labels: 'x' }, debounceMS: 0 })
    await settleVue(4, 20)

    // The deliveries are the webhook's sink, not a per-trigger log.
    expect(listWebhookDeliveries).toHaveBeenCalledWith('github-org', { limit: 50 })
    expect(evaluateTriggerFilter).toHaveBeenLastCalledWith('on-pr', { filter: { eventType: 'pull_request', labels: 'x' }, deliveryID: 'd-push' })
    expect(text(view.element)).toContain('This trigger recorded it as filtered')
    const verdict = view.element.querySelector('[data-testid="trigger-filter-verdict"]')!
    expect(text(verdict)).toContain('Would not fire')
    expect(text(verdict)).toContain('eventType does not match pull_request')
    const keys = [...verdict.querySelectorAll('.agents-verdict-keys li')]
    expect(keys).toHaveLength(2)
    expect(keys[0].classList.contains('bad')).toBe(true)
    expect(text(keys[0])).toContain('got push')
    expect(keys[1].classList.contains('unknown')).toBe(true)

    // Editing the filter re-evaluates without any click.
    await view.setProps({ filter: { eventType: 'push' } })
    await settleVue(4, 20)
    expect(evaluateTriggerFilter).toHaveBeenLastCalledWith('on-pr', { filter: { eventType: 'push' }, deliveryID: 'd-push' })
    expect(text(view.element.querySelector('[data-testid="trigger-filter-verdict"]'))).toContain('Would fire')

    // A broken filter line pauses evaluation rather than sending garbage.
    const calls = evaluateTriggerFilter.mock.calls.length
    await view.setProps({ filterError: 'Each filter line is key=value' })
    await settleVue(4, 20)
    expect(evaluateTriggerFilter.mock.calls.length).toBe(calls)
    expect(text(view.element.querySelector('[data-testid="trigger-filter-verdict"]'))).toContain('Fix the filter lines')
  })

  it('replays the picked delivery through this trigger only', async () => {
    const listWebhookDeliveries = vi.fn().mockResolvedValue({ items: [deliveries[0]], retention: 200 })
    const evaluateTriggerFilter = vi.fn().mockResolvedValue({ matched: true, keys: [], extracted: {}, filter: {} })
    const replayTriggerDelivery = vi.fn().mockResolvedValue({ deliveryID: 'd-9', status: 'fired', runID: 'r-7' })
    const api = stubApi({ listWebhookDeliveries, evaluateTriggerFilter, replayTriggerDelivery })
    const store = makeStore(api)
    const view = await mount(TriggerFilterTester, { api, store, trigger, filter: {}, debounceMS: 0 })
    await settleVue(4, 20)

    button(view.element, 'Replay this delivery').click()
    await settleVue()
    expect(replayTriggerDelivery).toHaveBeenCalledWith('on-pr', 'd-pr')
    button(document.body, 'View run').click()
    expect(view.navigations).toEqual([{ kind: 'run', id: 'r-7' }])
  })

  it('tests a pasted sample when the webhook has recorded nothing, and flags an unsaved webhook change', async () => {
    const listWebhookDeliveries = vi.fn().mockResolvedValue({ items: [], retention: 200 })
    const evaluateTriggerFilter = vi.fn().mockResolvedValue({ matched: true, keys: [], extracted: {}, filter: {} })
    const api = stubApi({ listWebhookDeliveries, evaluateTriggerFilter })
    const store = makeStore(api)
    const view = await mount(TriggerFilterTester, { api, store, trigger, filter: {}, draftWebhook: 'deploys', debounceMS: 0 })
    await settleVue(4, 20)
    expect(text(view.element)).toContain('Nothing has reached this webhook URL yet')
    expect(text(view.element)).toContain('save to test against deploys')
    expect(evaluateTriggerFilter).not.toHaveBeenCalled()

    button(view.element, 'Pasted sample').click()
    await settleVue(4, 20)
    expect(evaluateTriggerFilter).toHaveBeenCalledWith('on-pr', { filter: {}, request: { headers: { 'X-GitHub-Event': 'pull_request' }, body: '{"action": "opened", "repository": {"full_name": "octo-org/api"}}' } })
    expect(text(view.element)).toContain('Would fire')
    expect(text(view.element)).toContain('filter is empty')
  })
})

describe('Automation.vue tester', () => {
  it('mounts the tester on the trigger edit form against the webhook’s deliveries', async () => {
    const listWebhookDeliveries = vi.fn().mockResolvedValue({ items: deliveries, retention: 200 })
    const evaluateTriggerFilter = vi.fn().mockResolvedValue({ matched: false, reason: 'eventType does not match pull_request', keys: [], extracted: {}, filter: {} })
    const api = stubApi({ listWebhookDeliveries, evaluateTriggerFilter })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.webhooks.data = [webhook]
    store.webhooks.hasSnapshot = true
    store.triggers.data = [trigger]
    store.triggers.loaded = true
    store.triggers.hasSnapshot = true
    const view = await mount(Automation, { store, api, kind: 'trigger', agent: 'scout', editName: 'on-pr' })
    await settleVue(4, 400)

    expect(view.element.querySelector('[data-testid="trigger-filter-tester"]')).not.toBeNull()
    expect(listWebhookDeliveries).toHaveBeenCalledWith('github-org', { limit: 50 })
    expect(evaluateTriggerFilter).toHaveBeenCalledWith('on-pr', { filter: { eventType: 'pull_request' }, deliveryID: 'd-pr' })
  })

  it('keeps the last delivery that did not fire on the trigger row', async () => {
    const api = stubApi()
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.webhooks.data = [webhook]
    store.triggers.data = [trigger]
    store.triggers.loaded = true
    store.triggers.hasSnapshot = true
    const view = await mount(Automation, { store, api, kind: 'trigger', agent: 'scout' })
    await settleVue()
    const row = view.element.querySelector('.k-table__row')!
    expect(text(row)).toContain('last delivery')
    expect(text(row)).toContain('filtered: eventType does not match pull_request')
  })
})
