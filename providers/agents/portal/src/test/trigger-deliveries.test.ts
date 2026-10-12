// The trigger delivery log and the live filter tester: what the Automation
// tab shows for traffic that arrived and did not fire, and how a filter being
// edited is judged against it — always by the provider's `evaluate` verb,
// never by a second matcher in the browser.
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ServerEvent } from '../store'
import type { Trigger, TriggerDelivery, TriggerFilterVerdict } from '../types'
import { clearToasts } from '../ui/toast'
import Automation from '../views/Automation.vue'
import TriggerDeliveryLog from '../views/TriggerDeliveryLog.vue'
import TriggerFilterTester from '../views/TriggerFilterTester.vue'
import { deliveryWhat, parseHeaderLines } from '../views/trigger-deliveries'
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

const trigger: Trigger = {
  metadata: { name: 'on-pr', uid: 'u1' },
  spec: { agentRef: 'scout', source: 'github', task: 'review it', filter: { eventType: 'pull_request' } },
  status: { webhookPath: '/services/providers/agents/webhooks/triggers/c1/on-pr/t', lastReceived: new Date().toISOString(), lastDelivery: { time: new Date().toISOString(), outcome: 'filtered', reason: 'eventType does not match pull_request' } },
}

const deliveries: TriggerDelivery[] = [
  { id: 'd-pr', triggerName: 'on-pr', agentName: 'scout', deliveryID: 'gh-2', receivedAt: new Date().toISOString(), outcome: 'fired', eventType: 'pull_request', action: 'opened', repository: 'octo/api', headers: { 'X-Github-Event': 'pull_request' }, body: '{"action":"opened"}', bodySize: 19, runID: 'r-1' },
  { id: 'd-push', triggerName: 'on-pr', agentName: 'scout', deliveryID: 'gh-1', receivedAt: new Date(Date.now() - 60_000).toISOString(), outcome: 'filtered', reason: 'eventType does not match pull_request', eventType: 'push', repository: 'octo/api', headers: { 'X-Github-Event': 'push', 'X-Hub-Signature-256': '[redacted]' }, body: '{"ref":"refs/heads/main"', bodySize: 600 },
]

function buttons(root: Element): HTMLButtonElement[] {
  return [...root.querySelectorAll<HTMLButtonElement>('button')]
}
function button(root: Element, label: string): HTMLButtonElement {
  const found = buttons(root).find(b => text(b).includes(label) || b.getAttribute('aria-label')?.includes(label))
  if (!found) throw new Error(`no button "${label}" among ${buttons(root).map(b => text(b) || b.getAttribute('aria-label')).join(' | ')}`)
  return found
}

describe('trigger-deliveries helpers', () => {
  it('names a delivery by what a filter would see', () => {
    expect(deliveryWhat({ eventType: 'pull_request', action: 'opened' })).toBe('pull_request · opened')
    expect(deliveryWhat({ eventType: 'opened', action: 'opened' })).toBe('opened')
    expect(deliveryWhat({ contentType: 'application/json; charset=utf-8' })).toBe('application/json')
  })
  it('parses pasted header lines leniently', () => {
    expect(parseHeaderLines('X-GitHub-Event: push\nnot a header\nX-Env:prod\n')).toEqual({ 'X-GitHub-Event': 'push', 'X-Env': 'prod' })
  })
})

describe('TriggerDeliveryLog.vue', () => {
  it('lists every delivery with its outcome and reason, filters by outcome, and replays one', async () => {
    const listTriggerDeliveries = vi.fn().mockResolvedValue({ items: deliveries, retention: 200 })
    const replayTriggerDelivery = vi.fn().mockResolvedValue({ status: 'filtered', reason: 'eventType does not match pull_request', deliveryID: 'd-3' })
    const getTriggerDelivery = vi.fn().mockResolvedValue({ ...deliveries[1], body: '{"ref":"refs/heads/main","whole":true}' })
    const api = stubApi({ listTriggerDeliveries, replayTriggerDelivery, getTriggerDelivery })
    const store = makeStore(api)
    const view = await mount(TriggerDeliveryLog, { api, store, trigger })
    await settleVue()

    expect(listTriggerDeliveries).toHaveBeenCalledWith('on-pr', { limit: 100 })
    const body = text(view.element)
    expect(body).toContain('Deliveries for on-pr')
    expect(body).toContain('keeps the last 200')
    const rows = [...view.element.querySelectorAll('.agents-delivery')]
    expect(rows).toHaveLength(2)
    expect(text(rows[0])).toContain('pull_request · opened')
    expect(text(rows[0])).toContain('Fired')
    expect(text(rows[1])).toContain('push')
    expect(text(rows[1])).toContain('Filtered')
    expect(text(rows[1])).toContain('eventType does not match pull_request')
    // Only the fired one links to its run.
    expect(buttons(rows[0]).some(b => text(b) === 'View run')).toBe(true)
    expect(buttons(rows[1]).some(b => text(b) === 'View run')).toBe(false)

    button(view.element, 'Filtered').click()
    await settleVue()
    expect(view.element.querySelectorAll('.agents-delivery')).toHaveLength(1)
    expect(text(view.element.querySelector('.agents-delivery'))).toContain('push')

    // Details: recorded headers (redacted value kept as such) and a previewed
    // body that can be loaded whole.
    button(view.element, 'Show delivery details').click()
    await settleVue()
    const detail = view.element.querySelector('.agents-delivery-detail')!
    expect(text(detail)).toContain('X-Hub-Signature-256')
    expect(text(detail)).toContain('[redacted]')
    expect(text(detail)).toContain('600 bytes')
    button(detail, 'Show whole payload').click()
    await settleVue()
    expect(getTriggerDelivery).toHaveBeenCalledWith('on-pr', 'd-push')
    expect(text(view.element.querySelector('.agents-delivery-pre'))).toContain('"whole":true')

    button(view.element, 'Replay').click()
    await settleVue()
    expect(replayTriggerDelivery).toHaveBeenCalledWith('on-pr', 'd-push')
    // A replay refreshes the log.
    expect(listTriggerDeliveries).toHaveBeenCalledTimes(2)
  })

  it('says when nothing has arrived, and refreshes on a push for this trigger', async () => {
    vi.useFakeTimers()
    try {
      const listTriggerDeliveries = vi.fn().mockResolvedValue({ items: [], retention: 0 })
      const api = stubApi({ listTriggerDeliveries })
      const store = makeStore(api)
      const view = await mount(TriggerDeliveryLog, { api, store, trigger })
      await settleVue()
      expect(text(view.element)).toContain('Nothing has reached this webhook URL yet')
      expect(text(view.element)).toContain('keeps no log')

      store.dispatchEvent(new CustomEvent<ServerEvent>('server', { detail: { type: 'trigger-delivery', data: { name: 'other' } } }))
      store.dispatchEvent(new CustomEvent<ServerEvent>('server', { detail: { type: 'trigger-delivery', data: { name: 'on-pr' } } }))
      store.dispatchEvent(new CustomEvent<ServerEvent>('server', { detail: { type: 'trigger-delivery', data: { name: 'on-pr' } } }))
      await vi.advanceTimersByTimeAsync(600)
      // One initial load plus one debounced refresh for the two matching pushes.
      expect(listTriggerDeliveries).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
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

  it('evaluates the live filter against the picked delivery and paints each key', async () => {
    const listTriggerDeliveries = vi.fn().mockResolvedValue({ items: deliveries, retention: 200 })
    const evaluateTriggerFilter = vi.fn().mockImplementation((_name: string, body: { filter: Record<string, string>; deliveryID?: string }) => Promise.resolve(verdictFor(body.filter, body.deliveryID)))
    const api = stubApi({ listTriggerDeliveries, evaluateTriggerFilter })
    const store = makeStore(api)
    const view = await mount(TriggerFilterTester, { api, store, trigger, filter: { eventType: 'pull_request', labels: 'x' }, preselect: 'd-push', debounceMS: 0 })
    await settleVue(4, 20)

    expect(evaluateTriggerFilter).toHaveBeenLastCalledWith('on-pr', { filter: { eventType: 'pull_request', labels: 'x' }, deliveryID: 'd-push' })
    const verdict = view.element.querySelector('[data-testid="trigger-filter-verdict"]')!
    expect(text(verdict)).toContain('Would not fire')
    expect(text(verdict)).toContain('eventType does not match pull_request')
    expect(text(verdict)).toContain('eventType push')
    const keys = [...verdict.querySelectorAll('.agents-verdict-keys li')]
    expect(keys).toHaveLength(2)
    expect(keys[0].classList.contains('bad')).toBe(true)
    expect(text(keys[0])).toContain('got push')
    expect(keys[1].classList.contains('unknown')).toBe(true)
    expect(text(keys[1])).toContain('not a filter key')

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

  it('tests a pasted sample when the log is empty', async () => {
    const listTriggerDeliveries = vi.fn().mockResolvedValue({ items: [], retention: 200 })
    const evaluateTriggerFilter = vi.fn().mockResolvedValue({ matched: true, keys: [], extracted: {}, filter: {} })
    const api = stubApi({ listTriggerDeliveries, evaluateTriggerFilter })
    const store = makeStore(api)
    const view = await mount(TriggerFilterTester, { api, store, trigger, filter: {}, debounceMS: 0 })
    await settleVue(4, 20)
    expect(text(view.element)).toContain('Nothing has reached this webhook URL yet')
    expect(evaluateTriggerFilter).not.toHaveBeenCalled()

    button(view.element, 'Pasted sample').click()
    await settleVue(4, 20)
    expect(evaluateTriggerFilter).toHaveBeenCalledWith('on-pr', { filter: {}, request: { headers: { 'X-GitHub-Event': 'pull_request' }, body: '{"action": "opened", "repository": {"full_name": "octo-org/api"}}' } })
    expect(text(view.element)).toContain('Would fire')
    expect(text(view.element)).toContain('filter is empty')
  })
})

describe('Automation.vue deliveries', () => {
  it('shows the last delivery that did not fire on the row and opens the log', async () => {
    const listTriggerDeliveries = vi.fn().mockResolvedValue({ items: deliveries, retention: 200 })
    const api = stubApi({ listTriggerDeliveries })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.triggers.data = [trigger]
    store.triggers.loaded = true
    store.triggers.hasSnapshot = true
    const view = await mount(Automation, { store, api, kind: 'trigger', agent: 'scout' })
    await settleVue()

    const row = view.element.querySelector('.k-table__row')!
    expect(text(row)).toContain('last delivery')
    expect(text(row)).toContain('filtered: eventType does not match pull_request')
    expect(view.element.querySelector('[data-testid="trigger-deliveries"]')).toBeNull()

    button(row, 'Show deliveries for on-pr').click()
    await settleVue()
    expect(view.element.querySelector('[data-testid="trigger-deliveries"]')).not.toBeNull()
    expect(listTriggerDeliveries).toHaveBeenCalledWith('on-pr', { limit: 100 })

    // "Test filter" in the log navigates to the edit form for that trigger.
    button(view.element.querySelector('[data-testid="trigger-deliveries"]')!, 'Test filter').click()
    expect(view.navigations).toEqual([{ kind: 'automation', resource: 'trigger', agent: 'scout', action: 'edit', name: 'on-pr' }])
  })

  it('mounts the tester on the edit form and sends deliveryRetention with the patch', async () => {
    const listTriggerDeliveries = vi.fn().mockResolvedValue({ items: deliveries, retention: 200 })
    const evaluateTriggerFilter = vi.fn().mockResolvedValue({ matched: false, reason: 'eventType does not match pull_request', keys: [], extracted: {}, filter: {} })
    const patchTrigger = vi.fn().mockResolvedValue(trigger)
    const api = stubApi({ listTriggerDeliveries, evaluateTriggerFilter, patchTrigger })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.triggers.data = [trigger]
    store.triggers.loaded = true
    store.triggers.hasSnapshot = true
    const view = await mount(Automation, { store, api, kind: 'trigger', agent: 'scout', editName: 'on-pr' })
    await settleVue(4, 400)

    expect(view.element.querySelector('[data-testid="trigger-filter-tester"]')).not.toBeNull()
    expect(evaluateTriggerFilter).toHaveBeenCalledWith('on-pr', { filter: { eventType: 'pull_request' }, deliveryID: 'd-pr' })

    const retention = view.element.querySelector<HTMLInputElement>('input[name="deliveryRetention"]')!
    expect(retention.value).toBe('')
    expect(retention.placeholder).toBe('200')
    retention.value = '50'
    retention.dispatchEvent(new Event('input', { bubbles: true }))
    await settleVue()
    view.element.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue()
    expect(patchTrigger).toHaveBeenCalledWith('on-pr', expect.objectContaining({ deliveryRetention: 50 }))
  })
})
