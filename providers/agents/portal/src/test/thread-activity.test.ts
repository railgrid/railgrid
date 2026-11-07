import { afterEach, expect, it, vi } from 'vitest'
import { defineComponent, h, ref, shallowRef } from 'vue'
import { useThreadActivity } from '../vue/thread-activity'
import { stubApi } from './helpers'
import { mountVue, settleVue, type MountedVue } from './vue-helper'
import type { RunSummary } from '../types'

const mounted: MountedVue[] = []
afterEach(() => { mounted.splice(0).forEach(view => view.unmount()); vi.useRealTimers() })
const run = (id: string, sessionID: string, phase = 'Running') => ({ id, sessionID, phase, agent: 'coder' } as RunSummary)
async function setup(listRuns: () => Promise<{ items: RunSummary[] }>, getRun?: unknown) {
  const api = shallowRef(stubApi({ listRuns, ...(getRun ? { getRun } : {}) }))
  const epoch = ref(0)
  let activity!: ReturnType<typeof useThreadActivity>
  const view = await mountVue(defineComponent({ setup() {
    activity = useThreadActivity(() => api.value, () => 'coder', [epoch])
    return () => h('div', [...activity.runningSessions.value].sort().join(','))
  } }), {})
  mounted.push(view)
  await settleVue(12)
  return { activity, view, api, epoch }
}
it('hydrates background work and polls terminal and approval transitions without events', async () => {
  vi.useFakeTimers()
  let items = [run('a', 'first'), run('b', 'second'), run('c', 'parked', 'PendingApproval'), { ...run('foreign', 'wrong'), agent: 'other' }]
  const { view } = await setup(async () => ({ items }))
  expect(view.element.textContent).toBe('first,second')
  items = [run('a', 'first', 'Succeeded'), run('b', 'second', 'PendingApproval'), run('c', 'parked', 'Running')]
  await vi.advanceTimersByTimeAsync(10000)
  expect(view.element.textContent).toBe('parked')
})
it('does not let a stale collection read undo a newer terminal event', async () => {
  vi.useFakeTimers()
  let finish!: (value: { items: RunSummary[] }) => void
  const { activity, view } = await setup(() => new Promise(resolve => { finish = resolve }))
  activity.record(run('a', 'first'))
  activity.record(run('a', 'first', 'Succeeded'))
  finish({ items: [run('a', 'first')] })
  await settleVue()
  expect(view.element.textContent).toBe('')
  await vi.advanceTimersByTimeAsync(10000)
  finish({ items: [run('a', 'first')] })
  await settleVue()
  expect(view.element.textContent).toBe('')
})
it('discards old authority reads and stops polling after unmount', async () => {
  vi.useFakeTimers()
  let finish!: (value: { items: RunSummary[] }) => void
  const oldRead = vi.fn(() => new Promise<{ items: RunSummary[] }>(resolve => { finish = resolve }))
  const { view, api, epoch } = await setup(oldRead)
  const newRead = vi.fn(async () => ({ items: [run('new', 'new-scope')] }))
  api.value = stubApi({ listRuns: newRead }); epoch.value++
  await settleVue(12)
  finish({ items: [run('old', 'old-scope')] })
  await settleVue(12)
  expect(view.element.textContent).toBe('new-scope')
  view.unmount()
  await vi.advanceTimersByTimeAsync(30000)
  expect(oldRead).toHaveBeenCalledTimes(1)
  expect(newRead).toHaveBeenCalledTimes(1)
})
it('recovers from a hung activity read without blocking future updates', async () => {
  vi.useFakeTimers()
  const list = vi.fn().mockImplementationOnce(() => new Promise(() => {})).mockResolvedValue({ items: [run('a', 'first')] })
  const { view } = await setup(list)
  await vi.advanceTimersByTimeAsync(25000)
  expect(view.element.textContent).toBe('first')
  expect(list).toHaveBeenCalledTimes(2)
})

it.each(['PendingApproval', 'Running'])('confirms stale collections against known %s activity', async phase => {
  vi.useFakeTimers()
  const stale = phase === 'Running' ? 'PendingApproval' : 'Running'
  let confirmed = { ...run('a', 'first', phase), ...(phase === 'PendingApproval' ? { pending: { inboxID: 'approval', tool: 'shell', args: '{}' } } : {}) }
  const getRun = vi.fn(async () => confirmed)
  const { activity, view } = await setup(async () => ({ items: [run('a', 'first', stale)] }), getRun)
  activity.record(confirmed)
  await vi.advanceTimersByTimeAsync(10000)
  expect(view.element.textContent).toBe(phase === 'Running' ? 'first' : '')
  expect(getRun).toHaveBeenCalledWith('a', { requireTrace: true })
  confirmed = { ...run('a', 'first', stale), ...(stale === 'PendingApproval' ? { pending: { inboxID: 'next', tool: 'shell', args: '{}' } } : {}) }
  await vi.advanceTimersByTimeAsync(10000)
  expect(view.element.textContent).toBe(stale === 'Running' ? 'first' : '')
})
it('fences terminal snapshots against delayed events and later stale lists', async () => {
  vi.useFakeTimers()
  let items = [run('a', 'first', 'Succeeded')]
  const { activity, view } = await setup(async () => ({ items }))
  activity.record({ id: 'a', agent: 'coder', phase: 'Running' })
  await settleVue()
  expect(view.element.textContent).toBe('')
  items = [run('a', 'first')]
  await vi.advanceTimersByTimeAsync(10000)
  expect(view.element.textContent).toBe('')
})
it('keeps known approval state when trace access fails, then resumes after access returns', async () => {
  vi.useFakeTimers()
  const getRun = vi.fn().mockRejectedValueOnce(new Error('Forbidden')).mockResolvedValue(run('a', 'first'))
  const { activity, view } = await setup(async () => ({ items: [run('a', 'first')] }), getRun)
  activity.record({ ...run('a', 'first'), pending: { inboxID: 'approval', tool: 'shell', args: '{}' } })
  await vi.advanceTimersByTimeAsync(10000)
  expect(view.element.textContent).toBe('')
  await vi.advanceTimersByTimeAsync(10000)
  expect(view.element.textContent).toBe('first')
})
it('does not let a late conflict confirmation undo a newer checkpoint', async () => {
  vi.useFakeTimers()
  let finish!: (value: RunSummary) => void
  const { activity, view } = await setup(async () => ({ items: [run('a', 'first')] }), () => new Promise(resolve => { finish = resolve }))
  activity.record(run('a', 'first', 'PendingApproval'))
  await vi.advanceTimersByTimeAsync(10000)
  activity.record(run('a', 'first', 'Succeeded'))
  finish(run('a', 'first'))
  await settleVue(12)
  expect(view.element.textContent).toBe('')
})
it('merges unrelated run snapshots even when another run changes during the read', async () => {
  vi.useFakeTimers()
  let finish!: (value: { items: RunSummary[] }) => void
  const { activity, view } = await setup(() => new Promise(resolve => { finish = resolve }))
  activity.record(run('a', 'first'))
  finish({ items: [run('a', 'first', 'PendingApproval'), run('b', 'second')] })
  await settleVue(12)
  expect(view.element.textContent).toBe('first,second')
})

it('times out a disconnected trace and retries without losing the checkpoint', async () => {
  vi.useFakeTimers()
  const getRun = vi.fn().mockImplementationOnce(() => new Promise(() => {})).mockResolvedValue(run('a', 'first'))
  const { activity, view } = await setup(async () => ({ items: [run('a', 'first')] }), getRun)
  activity.record(run('a', 'first', 'PendingApproval'))
  await vi.advanceTimersByTimeAsync(25000)
  expect(view.element.textContent).toBe('')
  await vi.advanceTimersByTimeAsync(10000)
  expect(view.element.textContent).toBe('first')
})
it('ignores a disconnected trace completing after an authority change', async () => {
  vi.useFakeTimers()
  let finish!: (value: RunSummary) => void
  const { activity, view, api, epoch } = await setup(async () => ({ items: [run('a', 'first')] }), () => new Promise(resolve => { finish = resolve }))
  activity.record(run('a', 'first', 'PendingApproval'))
  await vi.advanceTimersByTimeAsync(10000)
  api.value = stubApi({ listRuns: async () => ({ items: [run('b', 'second')] }) }); epoch.value++
  await settleVue(12)
  finish(run('a', 'first'))
  await settleVue(12)
  expect(view.element.textContent).toBe('second')
})

it('does not undo a known resume with PendingApproval metadata lacking a checkpoint', async () => {
  vi.useFakeTimers()
  const { activity, view } = await setup(async () => ({ items: [run('a', 'first', 'PendingApproval')] }), async () => run('a', 'first', 'PendingApproval'))
  activity.record(run('a', 'first'))
  await vi.advanceTimersByTimeAsync(20000)
  expect(view.element.textContent).toBe('first')
})
