// Activity list + run trace viewer.

import { describe, expect, it, vi } from 'vitest'
import type { Activity } from '../views/activity'
import type { RunDetailView } from '../views/run-detail'
import type { InboxItem, RunDetail, RunSummary } from '../types'
import '../views/activity'
import '../views/run-detail'
import { agentFixture, makeStore, mount, settle, stubApi, text } from './helpers'

const run = (over: Partial<RunSummary> = {}): RunSummary => ({
  id: 'r1',
  agent: 'scout',
  trigger: 'chat',
  class: 'interactive',
  phase: 'Succeeded',
  inputPreview: 'summarise the open PRs',
  inputTokens: 1200,
  outputTokens: 300,
  usdMicros: 4200,
  createdAt: new Date().toISOString(),
  durationMS: 3400,
  ...over,
})

describe('activity list', () => {
  it('renders a run row with phase, duration and usage', async () => {
    const listRuns = vi.fn().mockResolvedValue({ items: [run()], nextCursor: '' })
    const api = stubApi({ listRuns })
    const el = await mount<Activity>('agents-activity', { store: makeStore(api), api })

    const row = el.querySelector('.agents-run-row')!
    expect(text(row)).toContain('scout')
    expect(text(row)).toContain('summarise the open PRs')
    expect(text(row.querySelector('.agents-phase'))).toBe('Succeeded')
    expect(text(row)).toContain('3.4s')
    expect(text(row)).toContain('$0.0042')
  })

  it('reports a load failure instead of an empty state', async () => {
    const api = stubApi({ listRuns: () => Promise.reject(new Error('502 upstream error')) })
    const el = await mount<Activity>('agents-activity', { store: makeStore(api), api })
    expect(text(el.querySelector('.agents-state-error'))).toContain('502 upstream error')
  })

  it('pins pending approvals and resolves them inline', async () => {
    const resolveInbox = vi.fn().mockResolvedValue({})
    const api = stubApi({ listRuns: () => Promise.resolve({ items: [], nextCursor: '' }), resolveInbox })
    const store = makeStore(api)
    const item: InboxItem = {
      id: 'i1',
      agentName: 'scout',
      runID: 'r9',
      kind: 'approval',
      state: 'pending',
      prompt: 'scout wants to run edges__pods_delete',
      payload: { tool: 'edges__pods_delete' },
      createdAt: new Date().toISOString(),
    }
    store.inbox.data = [item]
    store.inbox.loaded = true

    const el = await mount<Activity>('agents-activity', { store, api })
    const box = el.querySelector('.agents-approvals')!
    expect(text(box)).toContain('edges__pods_delete')

    box.querySelector<HTMLButtonElement>('.agents-approval-actions button')!.click()
    await settle(el, 4)
    expect(resolveInbox).toHaveBeenCalledWith('i1', 'approve')
  })

  it('refetches when a run event arrives', async () => {
    const listRuns = vi.fn().mockResolvedValue({ items: [], nextCursor: '' })
    const api = stubApi({ listRuns })
    const store = makeStore(api)
    const el = await mount<Activity>('agents-activity', { store, api })
    expect(listRuns).toHaveBeenCalledTimes(1)

    store.applyServerEvent({ type: 'run', data: { id: 'r1', agent: 'scout', phase: 'Running' } })
    await new Promise((r) => setTimeout(r, 900))
    await settle(el)
    expect(listRuns).toHaveBeenCalledTimes(2)
  })

  it('bounds the query with since when a range preset is picked', async () => {
    const listRuns = vi.fn().mockResolvedValue({ items: [], nextCursor: '' })
    const api = stubApi({ listRuns })
    const el = await mount<Activity>('agents-activity', { store: makeStore(api), api })
    // Defaults to unbounded.
    expect(listRuns.mock.calls[0][0].since).toBeUndefined()

    const seg = el.querySelector('.agents-seg')!
    const buttons = [...seg.querySelectorAll('button')] as HTMLButtonElement[]
    const sevenDay = buttons.find((b) => b.textContent?.trim() === '7d')!
    sevenDay.click()
    await settle(el, 4)

    const since = listRuns.mock.calls[1][0].since as string
    expect(new Date(since).toISOString()).toBe(since) // RFC3339 round-trip
    const ageDays = (Date.now() - new Date(since).getTime()) / 86_400_000
    expect(ageDays).toBeGreaterThan(6.9)
    expect(ageDays).toBeLessThan(7.1)
    expect(sevenDay.getAttribute('aria-pressed')).toBe('true')
  })

  it('scopes to one agent and hides the agent filter on the agent Runs tab', async () => {
    const listRuns = vi.fn().mockResolvedValue({ items: [run()], nextCursor: '' })
    const api = stubApi({ listRuns })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const el = await mount<Activity>('agents-activity', { store, api, agent: 'scout' })
    expect(listRuns.mock.calls[0][0]).toMatchObject({ agent: 'scout' })
    expect(text(el.querySelector('.agents-filters'))).not.toContain('Agent')
  })
})

describe('run detail', () => {
  const detail = (over: Partial<RunDetail> = {}): RunDetail => ({
    ...run({ id: 'r5' }),
    input: 'check the deploy',
    steps: [
      { id: 's1', tool: 'edges__pods_list', args: '{"ns":"prod"}', result: '["api-1"]', outcome: 'ok', durationMS: 120, at: new Date().toISOString() },
      { id: 's2', tool: 'web_search', args: '{}', outcome: 'error', error: 'timeout', durationMS: 5000, at: new Date().toISOString() },
    ],
    ...over,
  })

  it('renders the header, the step timeline and expands a step', async () => {
    const api = stubApi({ getRun: () => Promise.resolve(detail()) })
    const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })

    expect(text(el.querySelector('.agents-runmeta'))).toContain('scout')
    const steps = el.querySelectorAll('.agents-step')
    expect(steps.length).toBe(2)
    expect(steps[1].className).toContain('is-err')

    steps[0].querySelector<HTMLButtonElement>('.agents-step-head')!.click()
    await settle(el)
    expect(text(steps[0].querySelector('.agents-step-body'))).toContain('"ns"')
  })

  it('offers Cancel while running and approve/deny while paused', async () => {
    const cancelRun = vi.fn().mockResolvedValue({ id: 'r5', cancelling: true })
    const api = stubApi({ getRun: () => Promise.resolve(detail({ phase: 'Running' })), cancelRun })
    const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })

    const cancel = [...el.querySelectorAll('button')].find((b) => b.textContent?.includes('Cancel run'))!
    cancel.click()
    await settle(el, 4)
    expect(cancelRun).toHaveBeenCalledWith('r5')

    const resolveInbox = vi.fn().mockResolvedValue({})
    const paused = stubApi({
      getRun: () => Promise.resolve(detail({ phase: 'PendingApproval', pending: { inboxID: 'i7', tool: 'edges__pods_delete', args: '{}' } })),
      resolveInbox,
    })
    const el2 = await mount<RunDetailView>('agents-run-detail', { store: makeStore(paused), api: paused, runID: 'r5' })
    const approve = [...el2.querySelectorAll('.agents-approval-actions button')][0] as HTMLButtonElement
    approve.click()
    await settle(el2, 4)
    expect(resolveInbox).toHaveBeenCalledWith('i7', 'approve')
  })

  it('links delegated child runs', async () => {
    const api = stubApi({ getRun: () => Promise.resolve(detail({ children: [run({ id: 'c1', agent: 'researcher' })] })) })
    const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })
    expect(text(el)).toContain('Delegated runs')
    expect(text(el)).toContain('researcher')
  })
})
