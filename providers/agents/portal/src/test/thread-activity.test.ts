import { afterEach, expect, it, vi } from 'vitest'
import { defineComponent, h, ref, shallowRef } from 'vue'
import { useThreadActivity } from '../vue/thread-activity'
import { stubApi } from './helpers'
import { mountVue, settleVue, type MountedVue } from './vue-helper'
import type { RunSummary } from '../types'

const mounted: MountedVue[] = []
afterEach(() => { mounted.splice(0).forEach(view => view.unmount()); vi.useRealTimers() })
const run = (id: string, sessionID: string, phase = 'Running') => ({ id, sessionID, phase, agent: 'coder' } as RunSummary)
async function setup(listRuns: () => Promise<{ items: RunSummary[] }>) {
  const api = shallowRef(stubApi({ listRuns }))
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
