import { describe, expect, it, vi } from 'vitest'
import { InlineAppLoadCoordinator } from '../vue/inline-app-loads'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}

describe('inline app load coordination', () => {
  it('deduplicates identical receipt reads in the same authority scope', async () => {
    const coordinator = new InlineAppLoadCoordinator<string>(2)
    const pending = deferred<string | null>()
    const task = vi.fn(() => pending.promise)
    const current = () => true
    const first = coordinator.load('receipt', 'scope', current, task)
    const second = coordinator.load('receipt', 'scope', current, task)

    expect(first).toBe(second)
    await Promise.resolve()
    expect(task).toHaveBeenCalledOnce()
    pending.resolve('authorized bytes')
    await expect(first).resolves.toBe('authorized bytes')
  })

  it('keeps at most two attended reads active and starts the next when one finishes', async () => {
    const coordinator = new InlineAppLoadCoordinator<number>(2)
    const pending = [deferred<number | null>(), deferred<number | null>(), deferred<number | null>()]
    const started: number[] = []
    const loads = pending.map((item, index) => coordinator.load(String(index), 'scope', () => true, async () => {
      started.push(index)
      return item.promise
    }))

    await Promise.resolve()
    expect(started).toEqual([0, 1])
    pending[0]!.resolve(0)
    await loads[0]
    for (let turn = 0; turn < 4; turn += 1) await Promise.resolve()
    expect(started).toEqual([0, 1, 2])
    pending[1]!.resolve(1)
    pending[2]!.resolve(2)
    await expect(Promise.all(loads)).resolves.toEqual([0, 1, 2])
  })

  it('discards a result when authority becomes stale while the request is in flight', async () => {
    const coordinator = new InlineAppLoadCoordinator<string>(2)
    const pending = deferred<string | null>()
    let current = true
    const load = coordinator.load('receipt', 'scope', () => current, () => pending.promise)
    await Promise.resolve()
    current = false
    pending.resolve('old authority bytes')
    await expect(load).resolves.toBeNull()
  })

  it('aborts outstanding calls when the inspector takes ownership', async () => {
    const coordinator = new InlineAppLoadCoordinator<string>(2)
    const pending = deferred<string | null>()
    let signal: AbortSignal | undefined
    const load = coordinator.load('receipt', 'scope', () => true, receivedSignal => {
      signal = receivedSignal
      return pending.promise
    })
    await Promise.resolve()
    coordinator.abortAll()
    expect(signal?.aborted).toBe(true)
    pending.resolve('old inline result')
    await expect(load).resolves.toBeNull()
  })
})
