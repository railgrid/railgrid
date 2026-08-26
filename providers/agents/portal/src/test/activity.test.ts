// Activity list + run trace viewer.

import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
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

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise })
  return { promise, resolve }
}

describe('activity list', () => {
  it('keeps the shared route panel inset responsive without changing every agents panel', () => {
    const styles = readFileSync(resolve(process.cwd(), 'src/style.css'), 'utf8')

    expect(styles).toMatch(/\.agents-route-panel\s*\{\s*padding:\s*20px;/)
    expect(styles).toMatch(/@media \(max-width:\s*720px\)[\s\S]*?\.agents-route-panel\s*\{\s*padding:\s*14px;/)
    expect(styles).not.toMatch(/\.agents-panel\s*\{[^}]*padding:/)
  })

  it('groups the activity heading and filters inside the card layout', async () => {
    const api = stubApi({ listRuns: vi.fn().mockResolvedValue({ items: [run()], nextCursor: '' }) })
    const el = await mount<Activity>('agents-activity', { store: makeStore(api), api })

    expect(el.querySelector('.agents-activity-panel')).not.toBeNull()
    expect(el.querySelector('.agents-activity-panel.agents-route-panel')).not.toBeNull()
    expect(el.querySelector('.agents-activity-head h3')?.textContent).toBe('Activity')
    expect(el.querySelector('.agents-filters')?.getAttribute('aria-label')).toBe('Run filters')
    expect(el.querySelectorAll('.agents-filter-label')).toHaveLength(4)
    expect(el.querySelectorAll('.agents-filters .k-input')).toHaveLength(3)
    expect(el.querySelector('.agents-filters .agents-seg')?.getAttribute('aria-label')).toBe('Run range')
  })

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

  it('keeps loaded runs visible when a background refresh fails', async () => {
    const listRuns = vi.fn()
      .mockResolvedValueOnce({ items: [run()], nextCursor: '' })
      .mockRejectedValueOnce(new Error('temporary refresh failure'))
    const api = stubApi({ listRuns })
    const el = await mount<Activity>('agents-activity', { store: makeStore(api), api })

    await (el as unknown as { reload(mode: 'background'): Promise<void> }).reload('background')
    await settle(el)

    expect(text(el.querySelector('.agents-run-row'))).toContain('scout')
    expect(text(el.querySelector('.agents-state-error'))).toContain('Showing the last loaded data')
  })

  it('does not let an older activity response overwrite a newer filter result', async () => {
    const older = deferred<{ items: RunSummary[]; nextCursor: string }>()
    const newer = deferred<{ items: RunSummary[]; nextCursor: string }>()
    const listRuns = vi.fn()
      .mockResolvedValueOnce({ items: [run({ agent: 'initial' })], nextCursor: '' })
      .mockImplementationOnce(() => older.promise)
      .mockImplementationOnce(() => newer.promise)
    const api = stubApi({ listRuns })
    const el = await mount<Activity>('agents-activity', { store: makeStore(api), api })

    const oldRead = (el as unknown as { reload(mode: 'background'): Promise<void> }).reload('background')
    ;(el as unknown as { filterAgent: string }).filterAgent = 'new-agent'
    const newRead = (el as unknown as { reload(mode: 'foreground'): Promise<void> }).reload('foreground')
    newer.resolve({ items: [run({ agent: 'new-agent' })], nextCursor: '' })
    await newRead
    older.resolve({ items: [run({ agent: 'old-agent' })], nextCursor: '' })
    await oldRead
    await settle(el)

    expect(text(el.querySelector('.agents-run-row'))).toContain('new-agent')
    expect(text(el.querySelector('.agents-run-row'))).not.toContain('old-agent')
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

  it('keeps the run trace visible when a live background refresh fails', async () => {
    const getRun = vi.fn()
      .mockResolvedValueOnce(detail())
      .mockRejectedValueOnce(new Error('temporary refresh failure'))
    const api = stubApi({ getRun })
    const store = makeStore(api)
    const el = await mount<RunDetailView>('agents-run-detail', { store, api, runID: 'r5' })

    store.applyServerEvent({ type: 'run', data: { id: 'r5', agent: 'scout', phase: 'Running' } })
    await settle(el)

    expect(text(el.querySelector('.agents-runmeta'))).toContain('scout')
    expect(text(el.querySelector('.agents-state-error'))).toContain('Showing the last loaded data')
  })

  it('does not let an older run response overwrite a newer run identity', async () => {
    const older = deferred<RunDetail>()
    const newer = deferred<RunDetail>()
    const getRun = vi.fn()
      .mockResolvedValueOnce(detail({ agent: 'initial' }))
      .mockImplementationOnce(() => older.promise)
      .mockImplementationOnce(() => newer.promise)
    const api = stubApi({ getRun })
    const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })

    const oldRead = (el as unknown as { load(mode: 'background'): Promise<void> }).load('background')
    el.runID = 'r6'
    await el.updateComplete
    newer.resolve(detail({ id: 'r6', agent: 'new-agent' }))
    await settle(el)
    older.resolve(detail({ id: 'r5', agent: 'old-agent' }))
    await oldRead
    await settle(el)

    expect(text(el.querySelector('.agents-runmeta'))).toContain('new-agent')
    expect(text(el.querySelector('.agents-runmeta'))).not.toContain('old-agent')
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

  it('links child runs, distinguishing spawned workers from delegations', async () => {
    const api = stubApi({
      getRun: () =>
        Promise.resolve(
          detail({
            children: [
              run({ id: 'c1', agent: 'researcher' }),
              run({ id: 'c2', agent: 'scout', trigger: 'spawn' }),
            ],
          }),
        ),
    })
    const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })
    expect(text(el)).toContain('Child runs')
    expect(text(el)).toContain('researcher')
    // A fan-out's workers are labelled as such — one spawned worker here.
    expect(text(el)).toContain('1 spawned worker')
    expect(text(el)).toContain('delegated')
    expect(text(el)).toContain('worker')
  })

  it('shows the run answer and its sources', async () => {
    const api = stubApi({
      getRun: () =>
        Promise.resolve(
          detail({ output: 'The answer is 42.', sources: ['https://a.example/x', 'https://b.example/y'] }),
        ),
    })
    const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })
    expect(text(el)).toContain('The answer is 42.')
    const links = [...el.querySelectorAll('.agents-runsources a')] as HTMLAnchorElement[]
    expect(links.map((a) => a.getAttribute('href'))).toEqual(['https://a.example/x', 'https://b.example/y'])
  })

  it('shows the error and any partial output for a failed run', async () => {
    const api = stubApi({
      getRun: () => Promise.resolve(detail({ phase: 'Failed', message: 'model unavailable', output: 'got this far' })),
    })
    const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })
    expect(text(el)).toContain('model unavailable')
    expect(text(el)).toContain('Partial output')
    expect(text(el)).toContain('got this far')
  })
})

// Watching a fan-out is the point of the run tree: without live child updates a
// user fires a research pass and sees nothing until it is over.
describe('live fan-out view', () => {
  // Local fixtures: the ones above live inside another describe block.
  const detail = (over: Record<string, unknown> = {}) => ({
    id: 'r5',
    agent: 'scout',
    trigger: 'chat',
    class: 'interactive',
    phase: 'Running',
    inputTokens: 0,
    outputTokens: 0,
    usdMicros: 0,
    createdAt: new Date().toISOString(),
    input: 'research it',
    steps: [],
    children: [],
    ...over,
  })
  const child = (over: Record<string, unknown> = {}) => ({
    id: 'c1',
    agent: 'researcher',
    trigger: 'spawn',
    class: 'background',
    phase: 'Running',
    inputTokens: 0,
    outputTokens: 0,
    usdMicros: 0,
    createdAt: new Date().toISOString(),
    ...over,
  })

  it('picks up a worker it has never seen, from parentRunID alone', async () => {
    let children: unknown[] = []
    const getRun = vi.fn().mockImplementation(() => Promise.resolve(detail({ children })))
    const api = stubApi({ getRun })
    const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })
    await settle(el, 4)
    expect(text(el)).not.toContain('researcher')

    // A worker starts. The view has never loaded it, so matching on already-known
    // children would miss it entirely — parentRunID is what makes it appear.
    children = [child()]
    el.store.dispatchEvent(
      new CustomEvent('server', { detail: { type: 'run', data: { id: 'c1', parentRunID: 'r5', phase: 'Running' } } }),
    )
    await settle(el, 4)
    expect(text(el)).toContain('researcher')
  })

  it('ignores a child of some other run', async () => {
    const getRun = vi.fn().mockResolvedValue(detail({}))
    const api = stubApi({ getRun })
    const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })
    await settle(el, 4)
    const before = getRun.mock.calls.length

    el.store.dispatchEvent(
      new CustomEvent('server', { detail: { type: 'run', data: { id: 'z9', parentRunID: 'other-run', phase: 'Running' } } }),
    )
    await settle(el, 4)
    expect(getRun.mock.calls.length).toBe(before)
  })

  it('separates running from queued, so a queued worker does not look stuck', async () => {
    const api = stubApi({
      getRun: () =>
        Promise.resolve(
          detail({
            children: [
              child({ id: 'c1', phase: 'Running' }),
              child({ id: 'c2', phase: 'Running' }),
              child({ id: 'c3', phase: 'Pending' }),
              child({ id: 'c4', phase: 'Succeeded' }),
              child({ id: 'c5', phase: 'Failed' }),
            ],
          }),
        ),
    })
    const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })
    await settle(el, 4)
    const summary = text(el.querySelector('.agents-child-summary')!)
    expect(summary).toContain('2 running')
    expect(summary).toContain('1 queued')
    expect(summary).toContain('1 done')
    expect(summary).toContain('1 failed')
    // Work is still in flight, so the view says it will keep updating.
    expect(summary).toContain('updates as they finish')
    expect(el.querySelector('.agents-child-summary .agents-spinner')).toBeTruthy()
  })

  it('drops the in-flight affordance once everything has finished', async () => {
    const api = stubApi({
      getRun: () =>
        Promise.resolve(detail({ children: [child({ id: 'c1', phase: 'Succeeded' }), child({ id: 'c2', phase: 'Succeeded' })] })),
    })
    const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })
    await settle(el, 4)
    const summary = text(el.querySelector('.agents-child-summary')!)
    expect(summary).toContain('2 done')
    expect(summary).not.toContain('running')
    expect(el.querySelector('.agents-child-summary .agents-spinner')).toBeNull()
  })

  it('counts approval-gated workers separately from failures', async () => {
    const api = stubApi({
      getRun: () => Promise.resolve(detail({ children: [child({ id: 'c1', phase: 'PendingApproval' })] })),
    })
    const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })
    await settle(el, 4)
    const summary = text(el.querySelector('.agents-child-summary')!)
    expect(summary).toContain('awaiting approval')
    expect(summary).not.toContain('failed')
  })
})

// "Where is it in the UI?" — with zero workers the fan-out section used to
// render nothing at all, so "not configured", "model chose not to" and "feature
// missing" were indistinguishable.
describe('fan-out visibility with no workers', () => {
  const detailNoKids = () => ({
    id: 'r5', agent: 'scout', trigger: 'chat', class: 'interactive', phase: 'Succeeded',
    inputTokens: 0, outputTokens: 0, usdMicros: 0, createdAt: new Date().toISOString(),
    input: 'research it', steps: [] as unknown[], children: [] as unknown[],
  })

  async function mountWith(families: string[], steps: unknown[] = []) {
    const api = stubApi({ getRun: () => Promise.resolve({ ...detailNoKids(), steps }) })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { tools: { interactive: { families } } })]
    const el = await mount<RunDetailView>('agents-run-detail', { store, api, runID: 'r5' })
    await settle(el, 4)
    return el
  }

  it('stays quiet when the agent cannot fan out at all', async () => {
    const el = await mountWith(['core', 'web'])
    expect(text(el)).not.toContain('Child runs')
  })

  it('says so when fan-out was available but unused', async () => {
    const el = await mountWith(['core', 'web', 'spawn'])
    expect(text(el)).toContain('Child runs')
    expect(text(el)).toContain('answered this request directly')
  })

  it('flags the broken case: spawn was called but produced no workers', async () => {
    const el = await mountWith(['core', 'web', 'spawn'], [
      { id: 's1', tool: 'spawn', outcome: 'error', error: 'limit', at: new Date().toISOString() },
    ])
    expect(text(el)).toContain('no worker runs were recorded')
  })
})

// The complaint: open a running run and the view sits frozen — no steps, no
// clock, nothing — until it suddenly completes. Runs publish an event when they
// start and when they finish and nothing in between, so subscribing alone is not
// enough; the view has to poll while the run is live.
describe('live run detail', () => {
  const base = (over: Record<string, unknown> = {}) => ({
    id: 'r5', agent: 'scout', trigger: 'channel', class: 'interactive', phase: 'Running',
    inputTokens: 0, outputTokens: 0, usdMicros: 0,
    createdAt: new Date(Date.now() - 90_000).toISOString(),
    startedAt: new Date(Date.now() - 90_000).toISOString(),
    input: 'do the research', steps: [] as unknown[], children: [] as unknown[],
    ...over,
  })

  it('shows a moving elapsed time instead of a dash while running', async () => {
    const api = stubApi({ getRun: () => Promise.resolve(base()) })
    const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })
    await settle(el, 4)
    // 90s in: the duration cell must show real elapsed time, not "—".
    expect(text(el)).toContain('1m')
    expect(el.querySelector('.agents-elapsed')).toBeTruthy()
  })

  it('polls while the run is live and stops once it settles', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    try {
      let phase = 'Running'
      const getRun = vi.fn().mockImplementation(() => Promise.resolve(base({ phase })))
      const api = stubApi({ getRun })
      const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })
      await settle(el, 4)
      const afterMount = getRun.mock.calls.length

      await vi.advanceTimersByTimeAsync(7000)
      const whileLive = getRun.mock.calls.length
      expect(whileLive).toBeGreaterThan(afterMount) // it polled

      phase = 'Succeeded'
      await vi.advanceTimersByTimeAsync(4000)
      const atSettle = getRun.mock.calls.length
      await vi.advanceTimersByTimeAsync(15000)
      // Once terminal, polling stops — a finished run must not keep hitting the API.
      expect(getRun.mock.calls.length).toBe(atSettle)
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not poll a run that was already finished when opened', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    try {
      const getRun = vi.fn().mockResolvedValue(base({ phase: 'Succeeded', durationMS: 1234 }))
      const api = stubApi({ getRun })
      const el = await mount<RunDetailView>('agents-run-detail', { store: makeStore(api), api, runID: 'r5' })
      await settle(el, 4)
      const n = getRun.mock.calls.length
      await vi.advanceTimersByTimeAsync(20000)
      expect(getRun.mock.calls.length).toBe(n)
    } finally {
      vi.useRealTimers()
    }
  })
})
