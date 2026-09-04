import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mutate } from '../mutate'
import { makeStore, stubApi } from './helpers'

const { toast } = vi.hoisted(() => ({ toast: vi.fn() }))
vi.mock('../ui/toast', () => ({ toast }))

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

describe('mutation authority lifecycle', () => {
  beforeEach(() => toast.mockReset())

  it('suppresses success UI and reloads when the owning store retires in flight', async () => {
    const store = makeStore(stubApi())
    const load = vi.spyOn(store, 'load').mockResolvedValue()
    const request = deferred<{ ok: boolean }>()
    const pending = mutate(store, {
      run: () => request.promise,
      success: 'Saved.',
      failure: 'Save failed',
      reload: ['agents'],
    })

    store.retire()
    request.resolve({ ok: true })

    await expect(pending).resolves.toBeUndefined()
    expect(toast).not.toHaveBeenCalled()
    expect(load).not.toHaveBeenCalled()
  })

  it('suppresses failure rollback, UI, and reloads when the owning store retires in flight', async () => {
    const store = makeStore(stubApi())
    const load = vi.spyOn(store, 'load').mockResolvedValue()
    const rollback = vi.fn()
    const request = deferred<void>()
    const pending = mutate(store, {
      run: () => request.promise,
      failure: 'Save failed',
      rollback,
      reload: ['agents'],
    })

    store.retire()
    request.reject(new Error('late failure'))

    await expect(pending).resolves.toBeUndefined()
    expect(rollback).not.toHaveBeenCalled()
    expect(toast).not.toHaveBeenCalled()
    expect(load).not.toHaveBeenCalled()
  })

  it('does not start a write through an already-retired store', async () => {
    const store = makeStore(stubApi())
    const run = vi.fn().mockResolvedValue({ ok: true })
    store.retire()

    await expect(mutate(store, { run, success: 'Saved.', failure: 'Save failed' })).resolves.toBeUndefined()

    expect(run).not.toHaveBeenCalled()
    expect(toast).not.toHaveBeenCalled()
  })
})
