import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ApiClient } from '../api'
import { resolveConfirm } from '../portalkit/confirm'
import type { AppStore } from '../store'
import type { InboxItem, RunDetail as RunDetailData, RunSummary, Schedule } from '../types'
import { clearToasts } from '../ui/toast'
import Activity from '../views/Activity.vue'
import Automation from '../views/Automation.vue'
import RunDetail from '../views/RunDetail.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text, type MountedVue } from './vue-helper'

const mounted: MountedVue[] = []

afterEach(() => {
  for (const view of mounted.splice(0)) view.unmount()
  clearToasts()
  vi.useRealTimers()
})

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

async function mount(component: Parameters<typeof mountVue>[0], props: Record<string, unknown>): Promise<MountedVue> {
  const view = await mountVue(component, props)
  mounted.push(view)
  return view
}

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

const detail = (over: Partial<RunDetailData> = {}): RunDetailData => ({
  ...run({ id: 'r5' }),
  input: 'check the deploy',
  steps: [
    { id: 's1', tool: 'edges__pods_list', args: '{"ns":"prod"}', result: '["api-1"]', outcome: 'ok', durationMS: 120, at: new Date().toISOString() },
    { id: 's2', tool: 'web_search', args: '{}', outcome: 'error', error: 'timeout', durationMS: 5000, at: new Date().toISOString() },
  ],
  ...over,
})

function setValue(element: HTMLInputElement | HTMLTextAreaElement, value: string): void {
  element.value = value
  element.dispatchEvent(new Event('input', { bubbles: true }))
}

function buttonWithText(root: ParentNode, value: string): HTMLButtonElement {
  const button = [...root.querySelectorAll<HTMLButtonElement>('button')].find(candidate => text(candidate).includes(value))
  if (!button) throw new Error(`button containing ${value} not found`)
  return button
}

async function chooseRunFilter(view: MountedVue, index: number, label: string): Promise<HTMLButtonElement> {
  const triggers = view.element.querySelectorAll<HTMLButtonElement>('.k-table__filter-trigger')
  const trigger = triggers[index]
  if (!trigger) throw new Error(`run filter ${index} not found`)
  trigger.click()
  await settleVue()
  const option = [...document.querySelectorAll<HTMLElement>('.k-table__filter-option')]
    .find(candidate => text(candidate) === label)
  if (!option) throw new Error(`run filter option ${label} not found`)
  option.click()
  await settleVue()
  return trigger
}

describe('Activity.vue', () => {
  it('loads the first feed page and emits navigation from an accessible row', async () => {
    const listRuns = vi.fn().mockResolvedValue({ items: [run()], nextCursor: '' })
    const api = stubApi({ listRuns })
    const view = await mount(Activity, { store: makeStore(api), api })

    expect(listRuns).toHaveBeenCalledWith(expect.objectContaining({ limit: 50 }))
    expect(listRuns.mock.calls[0][0].since).toBeUndefined()
    expect(text(view.element.querySelector('.k-table__row'))).toContain('summarise the open PRs')
    expect(text(view.element.querySelector('.agents-phase'))).toContain('Succeeded')

    view.element.querySelector<HTMLElement>('.k-table__row')!.click()
    expect(view.navigations).toEqual([{ kind: 'run', id: 'r1' }])
  })

  it('activates rows with Enter and Space without stealing nested controls', async () => {
    const api = stubApi({ listRuns: vi.fn().mockResolvedValue({ items: [run()], nextCursor: '' }) })
    const view = await mount(Activity, { store: makeStore(api), api })
    const row = view.element.querySelector<HTMLElement>('.k-table__row')!

    expect(row.getAttribute('role')).toBeNull()
    expect(row.tabIndex).toBe(0)
    expect(row.getAttribute('aria-label')).toBe('Open run r1')
    row.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    row.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true }))
    expect(view.navigations).toEqual([{ kind: 'run', id: 'r1' }, { kind: 'run', id: 'r1' }])

    const nested = document.createElement('button')
    nested.type = 'button'
    row.querySelector('td')!.append(nested)
    nested.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    nested.click()
    expect(view.navigations).toHaveLength(2)
  })

  it('renders an initial load error instead of the empty state', async () => {
    const api = stubApi({ listRuns: vi.fn().mockRejectedValue(new Error('502 upstream error')) })
    const view = await mount(Activity, { store: makeStore(api), api })

    expect(text(view.element.querySelector('.k-table__error'))).toContain('502 upstream error')
    expect(text(view.element)).not.toContain('No runs yet')
  })

  it('uses the shared in-progress recipe for a foreground refresh', async () => {
    const pending = deferred<{ items: RunSummary[]; nextCursor: string }>()
    const api = stubApi({ listRuns: vi.fn().mockImplementation(() => pending.promise) })
    const view = await mount(Activity, { store: makeStore(api), api })
    const refresh = view.element.querySelector<HTMLButtonElement>('button[aria-label="Refresh runs"]')!

    expect(refresh.classList.contains('k-btn')).toBe(true)
    expect(refresh.disabled).toBe(true)
    expect(refresh.getAttribute('aria-busy')).toBe('true')
    expect(refresh.querySelector('.k-spin')).not.toBeNull()

    pending.resolve({ items: [], nextCursor: '' })
    await settleVue()
  })

  it('ignores unrelated server events and refreshes once for a run event', async () => {
    vi.useFakeTimers()
    const listRuns = vi.fn().mockResolvedValue({ items: [run()], nextCursor: '' })
    const api = stubApi({ listRuns })
    const store = makeStore(api)
    await mount(Activity, { store, api })

    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'credential', data: { name: 'model' } } }))
    await vi.advanceTimersByTimeAsync(700)
    expect(listRuns).toHaveBeenCalledTimes(1)

    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'r1' } } }))
    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'r2' } } }))
    await vi.advanceTimersByTimeAsync(700)
    await settleVue()
    expect(listRuns).toHaveBeenCalledTimes(2)
  })

  it('bounds range queries and exposes the current range', async () => {
    const listRuns = vi.fn().mockResolvedValue({ items: [run()], nextCursor: '' })
    const api = stubApi({ listRuns })
    const view = await mount(Activity, { store: makeStore(api), api })
    await chooseRunFilter(view, 3, '7d')

    const since = listRuns.mock.calls[1][0].since as string
    const ageDays = (Date.now() - new Date(since).getTime()) / 86_400_000
    expect(new Date(since).toISOString()).toBe(since)
    expect(ageDays).toBeGreaterThan(6.9)
    expect(ageDays).toBeLessThan(7.1)
    expect(text(view.element.querySelectorAll('.k-table__filter-trigger')[3])).toContain('7d')
  })

  it('scopes the feed to an agent and removes the redundant agent filter', async () => {
    const listRuns = vi.fn().mockResolvedValue({ items: [run()], nextCursor: '' })
    const api = stubApi({ listRuns })
    const view = await mount(Activity, { store: makeStore(api), api, agent: 'scout' })

    expect(listRuns).toHaveBeenCalledWith(expect.objectContaining({ agent: 'scout' }))
    expect([...view.element.querySelectorAll('.k-table__filter-label')].map(label => text(label))).toEqual(['Class', 'Phase', 'Range'])
  })

  it('uses cursor-backed ResourceTable pagination without inventing a total', async () => {
    const listRuns = vi.fn()
      .mockResolvedValueOnce({ items: [run({ id: 'r1', inputPreview: 'first page' })], nextCursor: 'cursor-2' })
      .mockResolvedValueOnce({ items: [run({ id: 'r2', inputPreview: 'second page' })], nextCursor: '' })
      .mockResolvedValueOnce({ items: [run({ id: 'r1', inputPreview: 'first page' })], nextCursor: 'cursor-2' })
    const api = stubApi({ listRuns })
    const view = await mount(Activity, { store: makeStore(api), api })
    const shell = view.element.querySelector<HTMLTableElement>('table[aria-label="Runs"]')!.closest<HTMLElement>('.k-table--resource')!

    expect(shell.querySelectorAll('.k-table__filter')).toHaveLength(4)
    expect(text(shell.querySelector('.k-table__page-indicator'))).toBe('Page 1')
    expect(text(shell.querySelector('.k-table__range'))).toContain('Showing 1–1')

    shell.querySelector<HTMLButtonElement>('button[aria-label="Next page"]')!.click()
    await settleVue()
    expect(listRuns.mock.calls[1][0]).toEqual(expect.objectContaining({ cursor: 'cursor-2', limit: 50 }))
    expect(text(shell.querySelector('.k-table__row'))).toContain('second page')
    expect(text(shell.querySelector('.k-table__page-indicator'))).toBe('Page 2')

    shell.querySelector<HTMLButtonElement>('button[aria-label="Previous page"]')!.click()
    await settleVue()
    expect(listRuns.mock.calls[2][0].cursor).toBeUndefined()
    expect(text(shell.querySelector('.k-table__row'))).toContain('first page')
  })

  it('keeps the backend next-page affordance when a filtered cursor page is empty', async () => {
    const listRuns = vi.fn()
      .mockResolvedValueOnce({ items: [run()], nextCursor: '' })
      .mockResolvedValueOnce({ items: [], nextCursor: 'filtered-cursor-2' })
      .mockResolvedValueOnce({ items: [run({ id: 'r2', class: 'background', inputPreview: 'filtered second page' })], nextCursor: '' })
    const api = stubApi({ listRuns })
    const view = await mount(Activity, { store: makeStore(api), api })

    await chooseRunFilter(view, 1, 'background')
    const next = view.element.querySelector<HTMLButtonElement>('button[aria-label="Next page"]')!
    expect(text(view.element.querySelector('tbody'))).toContain('No runs match these filters')
    expect(next.disabled).toBe(false)

    next.click()
    await settleVue()
    expect(listRuns.mock.calls[2][0]).toEqual(expect.objectContaining({
      class: 'background',
      cursor: 'filtered-cursor-2',
    }))
    expect(text(view.element.querySelector('.k-table__row'))).toContain('filtered second page')
  })

  it('renders Running as an in-progress warning tone in the feed', async () => {
    const api = stubApi({ listRuns: vi.fn().mockResolvedValue({ items: [run({ phase: 'Running' })], nextCursor: '' }) })
    const view = await mount(Activity, { store: makeStore(api), api })

    expect(view.element.querySelector('.agents-phase')?.classList.contains('k-badge--warning')).toBe(true)
    expect(view.element.querySelector('.agents-phase')?.classList.contains('k-badge--success')).toBe(false)
  })

  it('keeps the last successful feed visible when a server-event refresh fails', async () => {
    const listRuns = vi.fn()
      .mockResolvedValueOnce({ items: [run()], nextCursor: '' })
      .mockRejectedValueOnce(new Error('temporary refresh failure'))
    const api = stubApi({ listRuns })
    const store = makeStore(api)
    const view = await mount(Activity, { store, api })
    vi.useFakeTimers()

    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'r1' } } }))
    await vi.advanceTimersByTimeAsync(700)
    await settleVue()

    expect(text(view.element.querySelector('.k-table__row'))).toContain('scout')
    expect(text(view.element.querySelector('.k-table__stale'))).toContain('Showing the last successful result')
    expect(view.element.querySelector('.k-table__stale')?.getAttribute('role')).toBe('status')
  })

  it('keeps the last successful inbox snapshot visible when its background refresh fails', async () => {
    const api = stubApi({ listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }) })
    const store = makeStore(api)
    store.inbox.data = [{
      id: 'i1', agentName: 'scout', runID: 'r9', kind: 'approval', state: 'pending',
      prompt: 'approve the deployment', payload: { tool: 'deploy', args: '{}' }, createdAt: new Date().toISOString(),
    }]
    Object.assign(store.inbox, { loaded: true, hasSnapshot: true, error: 'inbox refresh failed' })
    const view = await mount(Activity, { store, api })

    expect(text(view.element.querySelector('.agents-approval-row'))).toContain('approve the deployment')
    expect(text(view.element.querySelector('.agents-state-error'))).toContain('Showing the last loaded data. inbox refresh failed')
  })

  it('does not allow an older identity response to overwrite the newer agent feed', async () => {
    const older = deferred<{ items: RunSummary[]; nextCursor: string }>()
    const newer = deferred<{ items: RunSummary[]; nextCursor: string }>()
    const listRuns = vi.fn()
      .mockImplementationOnce(() => older.promise)
      .mockImplementationOnce(() => newer.promise)
    const api = stubApi({ listRuns })
    const view = await mount(Activity, { store: makeStore(api), api, agent: 'old-agent' })

    const update = view.setProps({ agent: 'new-agent' })
    newer.resolve({ items: [run({ agent: 'new-agent', inputPreview: 'new result' })], nextCursor: '' })
    await update
    older.resolve({ items: [run({ agent: 'old-agent', inputPreview: 'old result' })], nextCursor: '' })
    await settleVue()

    expect(listRuns.mock.calls[0][0].agent).toBe('old-agent')
    expect(listRuns.mock.calls[1][0].agent).toBe('new-agent')
    expect(text(view.element.querySelector('.k-table__row'))).toContain('new result')
    expect(text(view.element)).not.toContain('old result')
  })

  it('reloads the same agent under new store and API authority and ignores the late old response', async () => {
    const oldRequest = deferred<{ items: RunSummary[]; nextCursor: string }>()
    const newRequest = deferred<{ items: RunSummary[]; nextCursor: string }>()
    const oldListRuns = vi.fn().mockImplementation(() => oldRequest.promise)
    const newListRuns = vi.fn().mockImplementation(() => newRequest.promise)
    const oldApi = stubApi({ listRuns: oldListRuns })
    const newApi = stubApi({ listRuns: newListRuns })
    const view = await mount(Activity, { store: makeStore(oldApi), api: oldApi, agent: 'scout' })

    await view.setProps({ store: makeStore(newApi), api: newApi })

    expect(oldListRuns).toHaveBeenCalledTimes(1)
    expect(newListRuns).toHaveBeenCalledTimes(1)
    expect(newListRuns).toHaveBeenCalledWith(expect.objectContaining({ agent: 'scout' }))
    expect(view.element.querySelector<HTMLButtonElement>('button[aria-label="Refresh runs"]')?.getAttribute('aria-busy')).toBe('true')

    newRequest.resolve({ items: [run({ agent: 'scout', inputPreview: 'new authority result' })], nextCursor: '' })
    await settleVue()
    oldRequest.resolve({ items: [run({ agent: 'scout', inputPreview: 'late old result' })], nextCursor: '' })
    await settleVue()

    expect(text(view.element.querySelector('.k-table__row'))).toContain('new authority result')
    expect(text(view.element)).not.toContain('late old result')
    expect(view.element.querySelector<HTMLButtonElement>('button[aria-label="Refresh runs"]')?.disabled).toBe(false)
  })

  it('pins an approval and resolves it without leaving the feed', async () => {
    const resolution = deferred<Record<string, never>>()
    const resolveInbox = vi.fn().mockImplementation(() => resolution.promise)
    const api = stubApi({ listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }), resolveInbox })
    const store = makeStore(api)
    const item: InboxItem = {
      id: 'i1', agentName: 'scout', runID: 'r9', kind: 'approval', state: 'pending',
      prompt: 'scout wants to run edges__pods_delete', payload: { tool: 'edges__pods_delete', args: '{}' }, createdAt: new Date().toISOString(),
    }
    store.inbox.data = [item]
    store.inbox.loaded = true
    store.inbox.hasSnapshot = true
    const view = await mount(Activity, { store, api })

    expect([...view.element.querySelectorAll('h1, h2, h3, h4, h5, h6')].map(heading => [heading.tagName, text(heading)])).toEqual([
      ['H3', 'Activity'],
      ['H4', 'Needs your attention (1)'],
    ])
    expect(text(view.element.querySelector('.agents-approval-disclosure'))).toContain('edges__pods_delete')
    expect(text(view.element.querySelector('.agents-approval-args'))).toBe('{}')

    const actions = view.element.querySelector('.agents-approval-actions')!
    buttonWithText(actions, 'Approve').click()
    buttonWithText(actions, 'Deny').click()
    await settleVue(2)

    expect(resolveInbox).toHaveBeenCalledWith('i1', 'approve')
    expect(resolveInbox).toHaveBeenCalledTimes(1)
    expect([...actions.querySelectorAll<HTMLButtonElement>('button')].every(button => button.disabled)).toBe(true)

    resolution.resolve({})
    await settleVue()
    expect(view.element.querySelector('.agents-approvals')).toBeNull()
  })

  it('blocks approval without a valid disclosed argument object but leaves denial available', async () => {
    const resolveInbox = vi.fn().mockResolvedValue({})
    const api = stubApi({ listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }), resolveInbox })
    const store = makeStore(api)
    store.inbox.data = [{
      id: 'i1', agentName: 'scout', runID: 'r9', kind: 'approval', state: 'pending',
      prompt: 'scout wants to run edges__pods_delete', payload: { tool: 'edges__pods_delete', args: 'not-json' }, createdAt: new Date().toISOString(),
    }]
    store.inbox.loaded = true
    store.inbox.hasSnapshot = true
    const view = await mount(Activity, { store, api })

    const actions = view.element.querySelector('.agents-approval-actions')!
    const approve = buttonWithText(actions, 'Approve')
    const deny = buttonWithText(actions, 'Deny')
    expect(approve.disabled).toBe(true)
    expect(deny.disabled).toBe(false)
    expect(text(view.element.querySelector('[role="alert"]'))).toContain('Approval details are unavailable or malformed')

    deny.click()
    await settleVue()
    expect(resolveInbox).toHaveBeenCalledWith('i1', 'deny')
  })
})

describe('RunDetail.vue', () => {
  it('renders Running as an in-progress warning tone', async () => {
    const api = stubApi({ getRun: vi.fn().mockResolvedValue(detail({
      phase: 'Running',
      durationMS: undefined,
      children: [run({ id: 'c1', phase: 'Running' })],
    })) })
    const view = await mount(RunDetail, { store: makeStore(api), api, runId: 'r5' })

    expect(view.element.querySelector('.agents-phase')?.classList.contains('k-badge--warning')).toBe(true)
    expect(view.element.querySelector('.agents-phase')?.classList.contains('k-badge--success')).toBe(false)
    expect(view.element.querySelector('.agents-elapsed .k-spin')).not.toBeNull()
    expect(view.element.querySelector('.agents-child-summary .k-spin')).not.toBeNull()
  })

  it('keeps the resource heading and back route available during the initial read', async () => {
    const pending = deferred<RunDetailData>()
    const api = stubApi({ getRun: vi.fn().mockImplementation(() => pending.promise) })
    const view = await mount(RunDetail, { store: makeStore(api), api, runId: 'pending-run' })

    expect(text(view.element.querySelector('.k-resource-page__title'))).toBe('Run')
    expect(text(view.element.querySelector('.k-resource-page__subtitle'))).toBe('pending-run')
    expect(view.element.querySelector<HTMLAnchorElement>('.k-back-action')?.getAttribute('href')).toBe('#/activity')
    expect(view.element.querySelector('.k-resource-page__loading')).not.toBeNull()
  })

  it('renders trace/output/source behavior and expands tool steps', async () => {
    const api = stubApi({ getRun: vi.fn().mockResolvedValue(detail({ output: '**Answer**', sources: ['https://a.example/x'] })) })
    const view = await mount(RunDetail, { store: makeStore(api), api, runId: 'r5' })

    expect(text(view.element.querySelector('.k-resource-page__title'))).toBe('Run')
    expect(text(view.element.querySelector('.k-resource-page__kind'))).toBe('Run')
    expect(text(view.element.querySelector('.k-resource-page__subtitle'))).toBe('r5')
    expect(view.element.querySelector<HTMLAnchorElement>('.k-back-action')?.getAttribute('href')).toBe('#/activity')
    expect(text(view.element.querySelector('.agents-runmeta'))).toContain('scout')
    expect(view.element.querySelectorAll('.agents-step')).toHaveLength(2)
    expect(view.element.querySelectorAll('[data-k-resource-section-card]')).toHaveLength(3)
    expect(view.element.querySelector('.agents-panel.k-card')).toBeNull()
    expect(view.element.querySelectorAll('.agents-step')[1].className).toContain('is-err')
    buttonWithText(view.element.querySelector('.agents-step')!, 'edges__pods_list').click()
    await settleVue()
    expect(text(view.element.querySelector('.agents-step-body'))).toContain('"ns"')
    expect(view.element.querySelector('.agents-body strong')?.textContent).toBe('Answer')
    expect(view.element.querySelector<HTMLAnchorElement>('.agents-runsources a')?.rel).toContain('noopener')
  })

  it('keeps a loaded trace visible after a background refresh failure', async () => {
    const getRun = vi.fn()
      .mockResolvedValueOnce(detail())
      .mockRejectedValueOnce(new Error('temporary refresh failure'))
    const api = stubApi({ getRun })
    const store = makeStore(api)
    const view = await mount(RunDetail, { store, api, runId: 'r5' })

    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'r5' } } }))
    await settleVue()

    expect(text(view.element.querySelector('.agents-runmeta'))).toContain('scout')
    expect(text(view.element.querySelector('.k-resource-page__stale'))).toContain('Showing the last successful result')
  })

  it('discards an older response when the run identity changes', async () => {
    const older = deferred<RunDetailData>()
    const newer = deferred<RunDetailData>()
    const getRun = vi.fn()
      .mockImplementationOnce(() => older.promise)
      .mockImplementationOnce(() => newer.promise)
    const api = stubApi({ getRun })
    const view = await mount(RunDetail, { store: makeStore(api), api, runId: 'r5' })

    const update = view.setProps({ runId: 'r6' })
    newer.resolve(detail({ id: 'r6', agent: 'new-agent' }))
    await update
    older.resolve(detail({ id: 'r5', agent: 'old-agent' }))
    await settleVue()

    expect(text(view.element.querySelector('.agents-runmeta'))).toContain('new-agent')
    expect(text(view.element)).not.toContain('old-agent')
  })

  it('reloads the same run under new store and API authority and ignores the late old response', async () => {
    const oldRequest = deferred<RunDetailData>()
    const newRequest = deferred<RunDetailData>()
    const oldGetRun = vi.fn().mockImplementation(() => oldRequest.promise)
    const newGetRun = vi.fn().mockImplementation(() => newRequest.promise)
    const oldApi = stubApi({ getRun: oldGetRun })
    const newApi = stubApi({ getRun: newGetRun })
    const view = await mount(RunDetail, { store: makeStore(oldApi), api: oldApi, runId: 'r5' })

    await view.setProps({ store: makeStore(newApi), api: newApi })

    expect(oldGetRun).toHaveBeenCalledTimes(1)
    expect(newGetRun).toHaveBeenCalledTimes(1)
    expect(newGetRun).toHaveBeenCalledWith('r5')
    expect(view.element.querySelector('.k-resource-page__loading')).not.toBeNull()

    newRequest.resolve(detail({ id: 'r5', agent: 'new-authority' }))
    await settleVue()
    oldRequest.resolve(detail({ id: 'r5', agent: 'old-authority' }))
    await settleVue()

    expect(text(view.element.querySelector('.agents-runmeta'))).toContain('new-authority')
    expect(text(view.element)).not.toContain('old-authority')
    expect(view.element.querySelector('.k-resource-page__loading')).toBeNull()
  })

  it('supports cancel, approval, child navigation, and unseen-child event refresh', async () => {
    let children: RunSummary[] = [run({ id: 'c1', agent: 'researcher', trigger: 'spawn', class: 'background' })]
    const cancelRun = vi.fn().mockResolvedValue({})
    const resolveInbox = vi.fn().mockResolvedValue({})
    const getRun = vi.fn().mockImplementation(() => Promise.resolve(detail({
      phase: 'PendingApproval',
      pending: { inboxID: 'i7', tool: 'edges__pods_delete', args: '{}' },
      children,
    })))
    const api = stubApi({ getRun, cancelRun, resolveInbox })
    const store = makeStore(api)
    const view = await mount(RunDetail, { store, api, runId: 'r5' })

    buttonWithText(view.element, 'Cancel run').click()
    buttonWithText(view.element.querySelector('.agents-approval-actions')!, 'Approve').click()
    await settleVue()
    expect(cancelRun).toHaveBeenCalledWith('r5')
    expect(resolveInbox).toHaveBeenCalledWith('i7', 'approve')

    view.element.querySelector<HTMLElement>('.k-table__row')!.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true }))
    expect(view.navigations).toContainEqual({ kind: 'run', id: 'c1' })

    children = [...children, run({ id: 'c2', agent: 'new-worker', trigger: 'spawn', class: 'background' })]
    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'c2', parentRunID: 'r5' } } }))
    await settleVue()
    expect(text(view.element)).toContain('new-worker')
  })

  it('keeps run cancellation single-flight while the request is pending', async () => {
    const cancellation = deferred<Record<string, never>>()
    const cancelRun = vi.fn().mockImplementation(() => cancellation.promise)
    const api = stubApi({
      getRun: vi.fn().mockResolvedValue(detail({ phase: 'Running' })),
      cancelRun,
    })
    const view = await mount(RunDetail, { store: makeStore(api), api, runId: 'r5' })
    const cancel = buttonWithText(view.element, 'Cancel run')

    cancel.click()
    cancel.click()
    await settleVue(2)

    expect(cancelRun).toHaveBeenCalledTimes(1)
    expect(cancelRun).toHaveBeenCalledWith('r5')
    expect(cancel.disabled).toBe(true)
    expect(cancel.getAttribute('aria-busy')).toBe('true')
    expect(text(cancel)).toContain('Cancelling')

    cancellation.resolve({})
    await settleVue()
  })

  it('keeps both run approval decisions single-flight while resolution is pending', async () => {
    const resolution = deferred<Record<string, never>>()
    const resolveInbox = vi.fn().mockImplementation(() => resolution.promise)
    const api = stubApi({
      getRun: vi.fn().mockResolvedValue(detail({
        phase: 'PendingApproval',
        pending: { inboxID: 'i7', tool: 'edges__pods_delete', args: '{}' },
      })),
      resolveInbox,
    })
    const view = await mount(RunDetail, { store: makeStore(api), api, runId: 'r5' })
    const actions = view.element.querySelector('.agents-approval-actions')!

    buttonWithText(actions, 'Approve').click()
    buttonWithText(actions, 'Deny').click()
    await settleVue(2)

    expect(resolveInbox).toHaveBeenCalledTimes(1)
    expect(resolveInbox).toHaveBeenCalledWith('i7', 'approve')
    expect([...actions.querySelectorAll<HTMLButtonElement>('button')].every(button => button.disabled)).toBe(true)

    resolution.resolve({})
    await settleVue()
  })

  it('blocks a run approval with missing disclosure while leaving denial available', async () => {
    const resolveInbox = vi.fn().mockResolvedValue({})
    const api = stubApi({
      getRun: vi.fn().mockResolvedValue(detail({
        phase: 'PendingApproval',
        pending: { inboxID: 'i7', tool: '', args: '{}' },
      })),
      resolveInbox,
    })
    const view = await mount(RunDetail, { store: makeStore(api), api, runId: 'r5' })
    const actions = view.element.querySelector('.agents-approval-actions')!
    const approve = buttonWithText(actions, 'Approve & resume')
    const deny = buttonWithText(actions, 'Deny')

    expect(approve.disabled).toBe(true)
    expect(deny.disabled).toBe(false)
    expect(text(view.element.querySelector('.agents-approval-disclosure-error'))).toContain('Deny this request or inspect the run')
    deny.click()
    await settleVue()
    expect(resolveInbox).toHaveBeenCalledWith('i7', 'deny')
  })

  it('ignores server events for unrelated runs', async () => {
    const getRun = vi.fn().mockResolvedValue(detail())
    const api = stubApi({ getRun })
    const store = makeStore(api)
    await mount(RunDetail, { store, api, runId: 'r5' })
    const calls = getRun.mock.calls.length

    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'other', parentRunID: 'other-parent' } } }))
    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'inbox', data: { id: 'i1', runID: 'other' } } }))
    await settleVue()

    expect(getRun).toHaveBeenCalledTimes(calls)
  })

  it('shows the error and partial output for a failed run', async () => {
    const api = stubApi({ getRun: vi.fn().mockResolvedValue(detail({ phase: 'Failed', message: 'model unavailable', output: 'got this far' })) })
    const view = await mount(RunDetail, { store: makeStore(api), api, runId: 'r5' })

    expect(text(view.element.querySelector('.agents-err'))).toContain('model unavailable')
    expect(text(view.element)).toContain('Partial output')
    expect(text(view.element.querySelector('.agents-body'))).toContain('got this far')
  })

  it('summarizes running, queued, approval-gated, completed, and failed children', async () => {
    const children = [
      run({ id: 'c1', phase: 'Running' }),
      run({ id: 'c2', phase: 'Pending' }),
      run({ id: 'c3', phase: 'PendingApproval' }),
      run({ id: 'c4', phase: 'Succeeded' }),
      run({ id: 'c5', phase: 'Failed' }),
    ]
    const api = stubApi({ getRun: vi.fn().mockResolvedValue(detail({ children })) })
    const view = await mount(RunDetail, { store: makeStore(api), api, runId: 'r5' })
    const summary = text(view.element.querySelector('.agents-child-summary'))

    expect(summary).toContain('1 running')
    expect(summary).toContain('1 queued')
    expect(summary).toContain('1 awaiting approval')
    expect(summary).toContain('1 done')
    expect(summary).toContain('1 failed')
    expect(summary).toContain('updates as they finish')
  })

  it('polls only while live and advances the elapsed readout', async () => {
    vi.useFakeTimers()
    let phase: RunDetailData['phase'] = 'Running'
    const getRun = vi.fn().mockImplementation(() => Promise.resolve(detail({
      phase,
      durationMS: undefined,
      startedAt: new Date(Date.now() - 90_000).toISOString(),
    })))
    const api = stubApi({ getRun })
    const view = await mount(RunDetail, { store: makeStore(api), api, runId: 'r5' })
    const initialCalls = getRun.mock.calls.length

    expect(text(view.element.querySelector('.agents-elapsed'))).toContain('1m')
    await vi.advanceTimersByTimeAsync(3000)
    await settleVue()
    expect(getRun.mock.calls.length).toBeGreaterThan(initialCalls)

    phase = 'Succeeded'
    await vi.advanceTimersByTimeAsync(3000)
    await settleVue()
    const terminalCalls = getRun.mock.calls.length
    await vi.advanceTimersByTimeAsync(9000)
    await settleVue()
    expect(getRun).toHaveBeenCalledTimes(terminalCalls)
  })

  it('renders fan-out truth from reactive store grants even with no children', async () => {
    const api = stubApi({ getRun: vi.fn().mockResolvedValue(detail({ children: [], steps: [] })) })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { tools: { interactive: { families: ['core'] } } })]
    const view = await mount(RunDetail, { store, api, runId: 'r5' })
    expect(text(view.element)).not.toContain('Child runs')

    store.agents.data[0].spec.tools!.interactive!.families = ['core', 'spawn']
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(text(view.element)).toContain('answered this request directly')
  })
})

describe('Automation.vue', () => {
  function storeWithAgent(api: ApiClient): AppStore {
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout', { channels: [{ name: 'alerts', connectionRef: 'slack', primary: true }] })]
    store.connections.data = [{ metadata: { name: 'github-main' }, spec: { type: 'github' } }]
    return store
  }

  it('uses ResourceTable read states for initial loading and stale rows', async () => {
    const api = stubApi()
    const loadingStore = storeWithAgent(api)
    const loadingView = await mount(Automation, { store: loadingStore, api, kind: 'schedule', agent: 'scout' })

    expect(loadingView.element.querySelector('.k-table__loading')).not.toBeNull()
    expect(text(loadingView.element)).not.toContain('No schedules yet')

    const staleStore = storeWithAgent(api)
    staleStore.schedules.data = [{ metadata: { name: 'daily' }, spec: { agentRef: 'scout', type: 'cron', schedule: '0 9 * * *' } }]
    staleStore.schedules.loaded = true
    staleStore.schedules.hasSnapshot = true
    staleStore.schedules.error = 'temporary refresh failure'
    const staleView = await mount(Automation, { store: staleStore, api, kind: 'schedule', agent: 'scout' })

    expect(text(staleView.element.querySelector('.k-table__row'))).toContain('daily')
    expect(text(staleView.element.querySelector('.k-table__stale'))).toContain('temporary refresh failure')
    expect(text(staleView.element.querySelector('.k-table__retry'))).toContain('Retry')
  })

  it('keeps edit-route snapshots and labels failed refreshes as stale', async () => {
    const api = stubApi()
    const store = storeWithAgent(api)
    store.schedules.data = [{ metadata: { name: 'daily' }, spec: { agentRef: 'scout', type: 'cron', schedule: '0 9 * * *' } }]
    store.schedules.loaded = true
    store.schedules.hasSnapshot = true
    store.schedules.error = 'temporary refresh failure'
    const existing = await mount(Automation, { store, api, kind: 'schedule', agent: 'scout', editName: 'daily' })

    expect(existing.element.querySelector('form')).not.toBeNull()
    expect(text(existing.element.querySelector('.k-stale'))).toContain('Showing the last loaded data')
    expect(text(existing.element.querySelector('.k-stale'))).toContain('temporary refresh failure')

    const missing = await mount(Automation, { store, api, kind: 'schedule', agent: 'scout', editName: 'missing' })
    expect(missing.element.querySelector('form')).toBeNull()
    expect(text(missing.element.querySelector('.k-stale'))).toContain('temporary refresh failure')
    expect(text(missing.element)).toContain('The last loaded data did not include schedule “missing”')
    expect(text(missing.element)).not.toContain('No schedule named')
  })

  it('preserves an in-progress schedule draft across store refreshes and sends the create payload', async () => {
    const pendingCreate = deferred<Schedule>()
    const createSchedule = vi.fn().mockImplementation(() => pendingCreate.promise)
    const api = stubApi({ createSchedule })
    const store = storeWithAgent(api)
    const view = await mount(Automation, { store, api, kind: 'schedule', agent: 'scout', createRoute: true })

    await settleVue()
    expect([...view.element.querySelectorAll('h1, h2, h3, h4, h5, h6')].map(heading => [heading.tagName, text(heading)])).toEqual([
      ['H1', 'New schedule'],
    ])
    setValue(view.element.querySelector<HTMLInputElement>('input[placeholder="daily-digest"]')!, 'daily')
    setValue(view.element.querySelector<HTMLInputElement>('input[placeholder="0 9 * * *"]')!, '0 9 * * *')
    setValue(view.element.querySelector<HTMLTextAreaElement>('textarea')!, 'Summarise open PRs')
    store.dispatchEvent(new Event('change'))
    await settleVue()
    expect(view.element.querySelector<HTMLInputElement>('input[placeholder="daily-digest"]')!.value).toBe('daily')

    const form = view.element.querySelector<HTMLFormElement>('form')!
    expect(form.getAttribute('aria-busy')).toBe('false')
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue()
    expect(createSchedule).toHaveBeenCalledWith(expect.objectContaining({
      name: 'daily', agentRef: 'scout', type: 'cron', schedule: '0 9 * * *', task: 'Summarise open PRs', suspend: false,
    }))
    expect(buttonWithText(view.element, 'Creating…').disabled).toBe(true)
    expect(form.getAttribute('aria-busy')).toBe('true')
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue()
    expect(createSchedule).toHaveBeenCalledTimes(1)
    pendingCreate.resolve({ metadata: { name: 'daily' }, spec: { agentRef: 'scout', type: 'cron' } })
    await settleVue()
    expect(view.navigations).toEqual([{ kind: 'agent', name: 'scout', tab: 'config' }])
  })

  it('associates and announces a required automation name error', async () => {
    const createSchedule = vi.fn()
    const api = stubApi({ createSchedule })
    const store = storeWithAgent(api)
    const view = await mount(Automation, { store, api, kind: 'schedule', agent: 'scout', createRoute: true })

    await settleVue()
    view.element.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue()

    const input = view.element.querySelector<HTMLInputElement>('input[placeholder="daily-digest"]')!
    const error = view.element.querySelector('#automation-schedule-name-error')
    expect(input.getAttribute('aria-invalid')).toBe('true')
    expect(input.getAttribute('aria-describedby')).toBe('automation-schedule-name-error')
    expect(error?.getAttribute('role')).toBe('alert')
    expect(text(error)).toContain('A name is required')
    expect(createSchedule).not.toHaveBeenCalled()
  })

  it('keeps failed edits open and sends the trigger-specific patch shape', async () => {
    const patchTrigger = vi.fn().mockRejectedValue(new Error('conflict'))
    const api = stubApi({ patchTrigger })
    const store = storeWithAgent(api)
    store.triggers.data = [{ metadata: { name: 'on-issue' }, spec: { agentRef: 'scout', source: 'github', connectionRef: 'github-main', task: 'old task' } }]
    store.triggers.hasSnapshot = true
    const view = await mount(Automation, { store, api, kind: 'trigger', agent: 'scout', editName: 'on-issue' })

    await settleVue()
    setValue(view.element.querySelector<HTMLTextAreaElement>('textarea')!, 'new task')
    view.element.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue()

    expect(patchTrigger).toHaveBeenCalledWith('on-issue', {
      source: 'github', connectionRef: 'github-main', task: 'new task', suspend: false, channelRef: '',
    })
    expect(view.element.querySelector('form')).not.toBeNull()
    expect(view.element.querySelector<HTMLTextAreaElement>('textarea')!.value).toBe('new task')
  })

  it('routes collection create and edit actions to the four focused automation surfaces', async () => {
    const api = stubApi()
    const store = storeWithAgent(api)
    store.schedules.data = [{ metadata: { name: 'daily/digest' }, spec: { agentRef: 'scout', type: 'cron', schedule: '0 9 * * *' } }]
    store.schedules.hasSnapshot = true
    store.triggers.data = [{ metadata: { name: 'on/issue' }, spec: { agentRef: 'scout', source: 'github' } }]
    store.triggers.hasSnapshot = true
    const schedules = await mount(Automation, { store, api, kind: 'schedule', agent: 'scout' })
    const triggers = await mount(Automation, { store, api, kind: 'trigger', agent: 'scout' })

    buttonWithText(schedules.element, 'New schedule').click()
    schedules.element.querySelector<HTMLButtonElement>('button[aria-label="Edit daily/digest"]')!.click()
    buttonWithText(triggers.element, 'New trigger').click()
    triggers.element.querySelector<HTMLButtonElement>('button[aria-label="Edit on/issue"]')!.click()

    expect(schedules.navigations).toEqual([
      { kind: 'automation', resource: 'schedule', agent: 'scout', action: 'create' },
      { kind: 'automation', resource: 'schedule', agent: 'scout', action: 'edit', name: 'daily/digest' },
    ])
    expect(triggers.navigations).toEqual([
      { kind: 'automation', resource: 'trigger', agent: 'scout', action: 'create' },
      { kind: 'automation', resource: 'trigger', agent: 'scout', action: 'edit', name: 'on/issue' },
    ])
  })

  it('returns a routed automation form to Agent Config on cancel', async () => {
    const api = stubApi()
    const store = storeWithAgent(api)
    const view = await mount(Automation, { store, api, kind: 'trigger', agent: 'scout', createRoute: true })

    expect(view.element.querySelector<HTMLAnchorElement>('.k-back-action')?.getAttribute('href')).toBe('#/agents/scout/config')
    buttonWithText(view.element, 'Cancel').click()

    expect(view.navigations).toEqual([{ kind: 'agent', name: 'scout', tab: 'config' }])
  })

  it('does not navigate from a late save after its routed form unmounts', async () => {
    const pending = deferred<Schedule>()
    const api = stubApi({ createSchedule: vi.fn().mockImplementation(() => pending.promise) })
    const store = storeWithAgent(api)
    const view = await mount(Automation, { store, api, kind: 'schedule', agent: 'scout', createRoute: true })
    setValue(view.element.querySelector<HTMLInputElement>('input[name="name"]')!, 'daily')

    view.element.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue()
    view.unmount()
    mounted.splice(mounted.indexOf(view), 1)
    pending.resolve({ metadata: { name: 'daily' }, spec: { agentRef: 'scout', type: 'cron' } })
    await settleVue()

    expect(view.navigations).toEqual([])
  })

  it('rolls back a failed optimistic pause and confirms destructive deletion', async () => {
    const pendingPatch = deferred<Schedule>()
    const patchSchedule = vi.fn().mockImplementation(() => pendingPatch.promise)
    const deleteSchedule = vi.fn().mockResolvedValue(undefined)
    const schedule: Schedule = { metadata: { name: 'daily' }, spec: { agentRef: 'scout', type: 'cron', schedule: '0 9 * * *' } }
    const api = stubApi({ patchSchedule, deleteSchedule, listSchedules: vi.fn().mockResolvedValue([schedule]) })
    const store = storeWithAgent(api)
    store.schedules.data = [schedule]
    store.schedules.hasSnapshot = true
    const view = await mount(Automation, { store, api, kind: 'schedule', agent: 'scout' })

    view.element.querySelector<HTMLButtonElement>('button[aria-label="Pause daily"]')!.click()
    await settleVue()
    expect(schedule.spec.suspend).toBe(true)
    expect(text(view.element)).toContain('paused')
    pendingPatch.reject(new Error('conflict'))
    await settleVue()
    expect(schedule.spec.suspend).toBe(false)
    expect(text(view.element)).toContain('armed')

    view.element.querySelector<HTMLButtonElement>('button[aria-label="Delete daily"]')!.click()
    await settleVue()
    expect(deleteSchedule).not.toHaveBeenCalled()
    resolveConfirm(true)
    await settleVue()
    expect(deleteSchedule).toHaveBeenCalledWith('daily')
  })

  it('locks every automation row action while delete confirmation is open', async () => {
    const deleteSchedule = vi.fn().mockResolvedValue(undefined)
    const patchSchedule = vi.fn().mockResolvedValue({})
    const runSchedule = vi.fn().mockResolvedValue({ runID: 'run-42' })
    const schedule: Schedule = { metadata: { name: 'daily' }, spec: { agentRef: 'scout', type: 'cron', schedule: '0 9 * * *' } }
    const api = stubApi({ deleteSchedule, patchSchedule, runSchedule })
    const store = storeWithAgent(api)
    store.schedules.data = [schedule]
    store.schedules.hasSnapshot = true
    const view = await mount(Automation, { store, api, kind: 'schedule', agent: 'scout' })

    view.element.querySelector<HTMLButtonElement>('button[aria-label="Delete daily"]')!.click()
    await settleVue(2)
    const actionButtons = [...view.element.querySelectorAll<HTMLButtonElement>('td:last-child button')]
    expect(actionButtons.every(button => button.disabled)).toBe(true)

    view.element.querySelector<HTMLButtonElement>('button[aria-label="Run daily now"]')!.click()
    view.element.querySelector<HTMLButtonElement>('button[aria-label="Pause daily"]')!.click()
    view.element.querySelector<HTMLButtonElement>('button[aria-label="Delete daily"]')!.click()
    await settleVue(2)
    expect(runSchedule).not.toHaveBeenCalled()
    expect(patchSchedule).not.toHaveBeenCalled()
    expect(deleteSchedule).not.toHaveBeenCalled()

    resolveConfirm(false)
    await settleVue(2)
    expect([...view.element.querySelectorAll<HTMLButtonElement>('td:last-child button')].every(button => !button.disabled)).toBe(true)
  })

  it('queues run-now work and exposes the returned run as opt-in navigation', async () => {
    const runSchedule = vi.fn().mockResolvedValue({ runID: 'run-42' })
    const api = stubApi({ runSchedule })
    const store = storeWithAgent(api)
    store.schedules.data = [{ metadata: { name: 'daily' }, spec: { agentRef: 'scout', type: 'cron', schedule: '0 9 * * *' } }]
    store.schedules.hasSnapshot = true
    const view = await mount(Automation, { store, api, kind: 'schedule', agent: 'scout' })

    view.element.querySelector<HTMLButtonElement>('button[aria-label="Run daily now"]')!.click()
    await settleVue()
    expect(runSchedule).toHaveBeenCalledWith('daily')
    buttonWithText(document, 'View run').click()
    expect(view.navigations).toEqual([{ kind: 'run', id: 'run-42' }])
  })
})
