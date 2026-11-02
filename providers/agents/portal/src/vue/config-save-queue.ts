import type { ApiClient } from '../api'
import { mutate } from '../mutate'
import { AGENT_RECONCILE_TIMEOUT_MS } from '../resources'
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
  idleWaiters: Set<() => void>
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
    queue = { operations: [], processing: false, idleWaiters: new Set() }
    byAgent.set(name, queue)
  }
  return queue
}

/** Wait until this agent's queued writes and their authoritative reload settle. */
export function waitForAgentConfigSaves(store: AppStore, name: string): Promise<void> {
  const queue = queues.get(store)?.get(name)
  if (!queue || (!queue.processing && queue.operations.length === 0)) return Promise.resolve()
  return new Promise(resolve => queue.idleWaiters.add(resolve))
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
  const queue = queueFor(store, name)
  const result = new Promise<boolean>(resolve => {
    queue.operations.push({ api, patch, apply, before, success, resolve })
  })
  // Publish the queue entry before the optimistic change. Synchronous store
  // watchers can then wait for this save before reading server-derived state.
  apply(source.spec)
  store.dispatchEvent(new Event('change'))
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
        let ambiguousTimeout = false
        const saved = await mutate(store, {
          run: () => operation.api.patchAgent(name, operation.patch),
          success: operation.success,
          failure: 'Save failed',
          onFailure: error => {
            ambiguousTimeout = isAmbiguousTimeoutError(error)
          },
          rollback: () => {
            source.spec = cloneSpec(operation.before)
            rebasePending(source, queue)
          },
        })
        if (saved && !store.isRetired()) {
          const current = store.agent(name)
          if (current) {
            adoptSavedAgent(current, saved, queue)
            store.dispatchEvent(new Event('change'))
          }
        }
        operation.resolve(saved !== undefined && !store.isRetired())
        // A timed-out write may have committed before its response was lost.
        // Reconcile that ambiguous result before sending a later patch, so its
        // rollback point and visible base come from the server. A definite
        // rejection retains the queue's existing consecutive-write behavior.
        if (ambiguousTimeout) break
      }

      if (!writesSinceReconcile || store.isRetired()) break

      // Reconciliation is part of the same critical section as writes. Saves
      // arriving while this read is in flight remain queued behind it.
      const dataBeforeReload = store.agents.data
      const controller = new AbortController()
      const timeout = setTimeout(() => {
        const error = new Error('Agent settings refresh timed out. The saved change may still be present; refresh to confirm it.')
        error.name = 'TimeoutError'
        controller.abort(error)
      }, AGENT_RECONCILE_TIMEOUT_MS)
      try {
        await store.load('agents', { signal: controller.signal })
      } finally {
        clearTimeout(timeout)
      }
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
    if (queue.operations.length) {
      void drain(store, name, queue)
    } else {
      for (const resolve of queue.idleWaiters) resolve()
      queue.idleWaiters.clear()
    }
  }
}

function rebasePending(source: Agent, queue: AgentSaveQueue): void {
  for (const pending of queue.operations) {
    pending.before = cloneSpec(source.spec)
    pending.apply(source.spec)
  }
}

function adoptSavedAgent(source: Agent, saved: Agent, queue: AgentSaveQueue): void {
  source.metadata = { ...saved.metadata }
  source.spec = cloneSpec(saved.spec)
  if (saved.status === undefined) delete source.status
  else source.status = saved.status
  rebasePending(source, queue)
}

function isAmbiguousTimeoutError(error: unknown): boolean {
  if (error instanceof Error && error.name === 'TimeoutError') return true
  return !!error && typeof error === 'object' && (
    (error as { status?: unknown }).status === 504 ||
    (error as { reason?: unknown }).reason === 'Timeout'
  )
}
