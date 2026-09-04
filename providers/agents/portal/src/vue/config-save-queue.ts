import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import type { AppStore } from '../store'
import type { Agent, AgentPatch } from '../types'

interface ConfigSave {
  api: ApiClient
  patch: AgentPatch
  apply: (spec: Agent['spec']) => void
  before: Agent['spec']
  success: string
  resolve: (saved: boolean) => void
}

interface AgentSaveQueue {
  operations: ConfigSave[]
  processing: boolean
}

// A Config route may unmount while its writes are still in flight, then mount
// again for the same live store and agent. Store/agent ownership gives both
// component instances one FIFO and prevents a newer write from bypassing an
// older queue. Weak ownership lets retired/discarded stores be collected.
const queues = new WeakMap<AppStore, Map<string, AgentSaveQueue>>()

function queueFor(store: AppStore, name: string): AgentSaveQueue {
  let byAgent = queues.get(store)
  if (!byAgent) {
    byAgent = new Map()
    queues.set(store, byAgent)
  }
  let queue = byAgent.get(name)
  if (!queue) {
    queue = { operations: [], processing: false }
    byAgent.set(name, queue)
  }
  return queue
}

function cloneSpec(spec: Agent['spec']): Agent['spec'] {
  return JSON.parse(JSON.stringify(spec)) as Agent['spec']
}

export function queueAgentConfigSave(options: {
  store: AppStore
  api: ApiClient
  name: string
  patch: AgentPatch
  apply: (spec: Agent['spec']) => void
  success: string
}): Promise<boolean> {
  const { store, api, name, patch, apply, success } = options
  if (store.isRetired()) return Promise.resolve(false)
  const source = store.agent(name)
  if (!source) return Promise.resolve(false)

  const before = cloneSpec(source.spec)
  apply(source.spec)
  store.dispatchEvent(new Event('change'))

  const queue = queueFor(store, name)
  const result = new Promise<boolean>(resolve => {
    queue.operations.push({ api, patch, apply, before, success, resolve })
  })
  void drain(store, name, queue)
  return result
}

async function drain(store: AppStore, name: string, queue: AgentSaveQueue): Promise<void> {
  if (queue.processing) return
  queue.processing = true
  let writesSinceReconcile = false
  try {
    while (true) {
      while (queue.operations.length) {
        const operation = queue.operations.shift()!
        // Enqueuing was the user's authorization. Component unmount is not an
        // authority change; explicit store retirement is.
        if (store.isRetired()) {
          operation.resolve(false)
          continue
        }
        const source = store.agent(name)
        if (!source) {
          operation.resolve(false)
          continue
        }
        writesSinceReconcile = true
        const result = await mutate(store, {
          run: () => operation.api.patchAgent(name, operation.patch),
          success: operation.success,
          failure: 'Save failed',
          rollback: () => {
            source.spec = cloneSpec(operation.before)
            rebasePending(source, queue)
          },
        })
        operation.resolve(result !== undefined && !store.isRetired())
      }

      if (!writesSinceReconcile || store.isRetired()) break

      // Reconciliation is part of the same critical section as writes. Saves
      // arriving while this read is in flight remain queued behind it.
      const dataBeforeReload = store.agents.data
      await store.load('agents')
      writesSinceReconcile = false
      if (store.isRetired()) break

      // A successful authoritative read replaces the collection. Pending
      // optimistic edits were applied to the prior agent object, so rebuild
      // them on the fresh spec and capture fresh rollback points before their
      // writes begin. A failed read preserves object identity; in that case the
      // existing snapshots remain correct and must not be rebased.
      if (store.agents.data !== dataBeforeReload && !store.agents.error && queue.operations.length) {
        const fresh = store.agent(name)
        if (fresh) {
          rebasePending(fresh, queue)
          store.dispatchEvent(new Event('change'))
        }
      }

      if (!queue.operations.length) break
    }
  } finally {
    queue.processing = false
    if (queue.operations.length) void drain(store, name, queue)
  }
}

function rebasePending(source: Agent, queue: AgentSaveQueue): void {
  for (const pending of queue.operations) {
    pending.before = cloneSpec(source.spec)
    pending.apply(source.spec)
  }
}
