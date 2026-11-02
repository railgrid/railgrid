import { describe, expect, it, vi } from 'vitest'
import { queueAgentConfigSave, waitForAgentConfigSaves } from '../vue/config-save-queue'
import { agentFixture, makeStore, modelBackend, stubApi } from './helpers'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

describe('Agent config collection load races', () => {
  it('keeps an acknowledged save and queued optimistic edit over an older poll, then rolls B back to A', async () => {
    const oldPoll = deferred<ReturnType<typeof agentFixture>[]>()
    const saveA = deferred<ReturnType<typeof agentFixture>>()
    const saveB = deferred<ReturnType<typeof agentFixture>>()
    const patchAgent = vi.fn()
      .mockImplementationOnce(() => saveA.promise)
      .mockImplementationOnce(() => saveB.promise)
    const listAgents = vi.fn()
      .mockImplementationOnce(() => oldPoll.promise)
      .mockRejectedValueOnce(new Error('final refresh unavailable'))
    const api = stubApi({ patchAgent, listAgents })
    const store = makeStore(api)
    const initial = agentFixture('scout', { backend: modelBackend('old-model'), description: 'server description' })
    initial.metadata.uid = 'agent-uid'
    initial.metadata.generation = 1
    store.agents.data = [initial]
    Object.assign(store.agents, { loaded: true, hasSnapshot: true })

    // This request began before either write and remains the latest collection
    // load while the queue is processing A and B.
    const staleLoad = store.load('agents')
    const first = queueAgentConfigSave({
      store,
      api,
      name: 'scout',
      patch: { modelCredential: 'model-a' },
      apply: spec => { spec.backend = modelBackend('model-a') },
      success: 'Saved model A',
    })
    const second = queueAgentConfigSave({
      store,
      api,
      name: 'scout',
      patch: { description: 'queued B description' },
      apply: spec => { spec.description = 'queued B description' },
      success: 'Saved B description',
    })

    const acknowledgedA = agentFixture('scout', { backend: modelBackend('model-a'), description: 'server description' })
    acknowledgedA.metadata.uid = 'agent-uid'
    acknowledgedA.metadata.generation = 2
    acknowledgedA.status = {
      conditions: [{ type: 'BackendReady', status: 'False', observedGeneration: 1 }],
    }
    saveA.resolve(acknowledgedA)
    await expect(first).resolves.toBe(true)
    expect(patchAgent).toHaveBeenCalledTimes(2)
    expect(patchAgent.mock.calls[1][1]).toEqual({ description: 'queued B description' })

    // The pre-A poll returns after A's response. Its stale generation must not
    // replace the same UID row, including B's optimistic edit layered on A.
    const staleAgent = agentFixture('scout', { backend: modelBackend('old-model'), description: 'old poll' })
    staleAgent.metadata.uid = 'agent-uid'
    staleAgent.metadata.generation = 1
    oldPoll.resolve([staleAgent])
    await staleLoad
    expect(store.agent('scout')?.metadata.generation).toBe(2)
    expect(store.agent('scout')?.status?.conditions?.[0]).toMatchObject({ status: 'False', observedGeneration: 1 })
    expect(store.agent('scout')?.spec.backend).toEqual(modelBackend('model-a'))
    expect(store.agent('scout')?.spec.description).toBe('queued B description')

    saveB.reject(new Error('B rejected'))
    await expect(second).resolves.toBe(false)
    await waitForAgentConfigSaves(store, 'scout')
    expect(listAgents).toHaveBeenCalledTimes(2)
    expect(store.agent('scout')?.metadata.generation).toBe(2)
    expect(store.agent('scout')?.spec.backend).toEqual(modelBackend('model-a'))
    expect(store.agent('scout')?.spec.description).toBe('server description')
  })
})
