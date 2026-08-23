import { createCoalescedRead, createLatestRefreshController } from './refresh.js'

function assert(condition: unknown, label: string): asserts condition {
  if (!condition) throw new Error(label)
}

const pending: Array<() => void> = []
let active = 0
let maxActive = 0
const seen: number[] = []
const controller = createLatestRefreshController(async requestID => {
  active += 1
  maxActive = Math.max(maxActive, active)
  seen.push(requestID)
  await new Promise<void>(resolve => pending.push(resolve))
  active -= 1
})

controller.request()
controller.request()
assert(maxActive === 1, 'refreshes overlap before the first request settles')
assert(seen.length === 1, 'a second timer refresh started before the first settled')
assert(!controller.isCurrent(seen[0]), 'a queued refresh did not supersede the old request')
pending.shift()?.()

await new Promise<void>(resolve => setTimeout(resolve, 0))
if ((seen.length as number) !== 2) throw new Error('queued refresh did not start after the first settled')
assert(maxActive === 1, 'queued refresh overlapped the first request')
assert(controller.isCurrent(seen[1]), 'latest request does not own the result')
pending.shift()?.()
await new Promise<void>(resolve => setTimeout(resolve, 0))

controller.stop()
controller.request()
assert((seen.length as number) === 2, 'stopped refresh controller accepted a new request')

let readCalls = 0
let resolveRead: ((value: string[]) => void) | undefined
const completeRead = createCoalescedRead(async () => {
  readCalls += 1
  return new Promise<string[]>(resolve => { resolveRead = resolve })
})
const firstRead = completeRead.request()
const coalescedRead = completeRead.request()
assert(firstRead === coalescedRead, 'concurrent complete reads were not coalesced')
assert(readCalls === 1, 'coalesced reads started more than one bounded walk')
resolveRead?.(['connection-a'])
assert((await firstRead).join(',') === 'connection-a', 'coalesced read lost the complete result')
const secondRead = completeRead.request()
assert(secondRead !== firstRead, 'a later read reused a settled promise')
assert((readCalls as number) === 2, 'a later read did not start a fresh bounded walk')
resolveRead?.(['connection-b'])
assert((await secondRead).join(',') === 'connection-b', 'serialized follow-up read lost its result')

let invalidatedCalls = 0
let resolveInvalidated: ((value: string[]) => void) | undefined
const invalidatedRead = createCoalescedRead(async () => {
  invalidatedCalls += 1
  return new Promise<string[]>(resolve => { resolveInvalidated = resolve })
})
const oldAuthority = invalidatedRead.request()
invalidatedRead.invalidate()
const freshAuthority = invalidatedRead.request()
assert((invalidatedCalls as number) === 1, 'invalidated reads overlapped instead of serializing')
resolveInvalidated?.(['old'])
assert((await oldAuthority).join(',') === 'old', 'the invalidated caller did not settle')
assert((invalidatedCalls as number) === 2, 'invalidated read did not schedule a fresh walk')
resolveInvalidated?.(['fresh'])
assert((await freshAuthority).join(',') === 'fresh', 'fresh read did not replace invalidated authority')

let stoppedCalls = 0
let resolveStopped: ((value: string[]) => void) | undefined
const stoppedRead = createCoalescedRead(async () => {
  stoppedCalls += 1
  return new Promise<string[]>(resolve => { resolveStopped = resolve })
})
const stoppedOld = stoppedRead.request()
stoppedRead.invalidate()
const stoppedFresh = stoppedRead.request()
stoppedRead.stop()
resolveStopped?.(['old'])
assert((await stoppedOld).join(',') === 'old', 'stopped read did not settle its existing caller')
let stoppedFreshRejected = false
try {
  await stoppedFresh
} catch {
  stoppedFreshRejected = true
}
assert(stoppedFreshRejected, 'stop allowed an invalidated follow-up read to start')
assert(stoppedCalls === 1, 'stop started a new read after unmount')

// A view's support metadata and complete target walk are query-independent.
// Model the staged transition directly so a query edit during the support
// request cannot enqueue another support+target pair, while the eventual
// target result still commits the newest query.
type Deferred<T> = {
  promise: Promise<T>
  resolve(value: T): void
}

function deferred<T>(): Deferred<T> {
  let resolvePromise!: (value: T) => void
  const promise = new Promise<T>(resolve => { resolvePromise = resolve })
  return { promise, resolve: resolvePromise }
}

let supportCalls = 0
let targetCalls = 0
let supportPending: Deferred<string> | undefined
let targetPending: Deferred<string> | undefined
const supportStage = createCoalescedRead(async () => {
  supportCalls += 1
  supportPending = deferred<string>()
  return supportPending.promise
})
const targetStage = createCoalescedRead(async () => {
  targetCalls += 1
  targetPending = deferred<string>()
  return targetPending.promise
})
let authorityGeneration = 0
let transitionActive = false
let latestQuery = ''
let committedQuery = ''

async function stagedTransition(): Promise<void> {
  const generation = authorityGeneration
  await supportStage.request()
  if (!transitionActive || generation !== authorityGeneration) return
  await targetStage.request()
  if (!transitionActive || generation !== authorityGeneration) return
  committedQuery = latestQuery
}

const firstTransition = stagedTransition()
transitionActive = true
latestQuery = 'first-search'
assert(supportCalls === 1, 'initial support stage did not start exactly once')
supportPending?.resolve('support')
await new Promise<void>(resolve => setTimeout(resolve, 0))
assert(targetCalls === 1, 'search during support load started more than one target walk')
latestQuery = 'latest-filter'
targetPending?.resolve('complete-source')
await firstTransition
assert(committedQuery === 'latest-filter', 'complete target walk did not commit the latest query')

// Clear/context invalidation rejects the old staged result. A new transition
// then waits behind the invalidated support promise and performs one fresh
// support+target pair without overlap.
transitionActive = false
const staleTransition = stagedTransition()
assert((supportCalls as number) === 2, 'stale transition did not start its support read')
authorityGeneration += 1
supportStage.invalidate()
targetStage.invalidate()
transitionActive = true
latestQuery = 'after-clear'
const freshTransition = stagedTransition()
assert((supportCalls as number) === 2, 'clear invalidation overlapped the support read')
supportPending?.resolve('stale-support')
await new Promise<void>(resolve => setTimeout(resolve, 0))
assert((supportCalls as number) === 3, 'clear did not serialize exactly one fresh support read')
supportPending?.resolve('fresh-support')
await new Promise<void>(resolve => setTimeout(resolve, 0))
assert((targetCalls as number) === 2, 'clear did not start exactly one fresh target walk')
targetPending?.resolve('fresh-source')
await Promise.all([staleTransition, freshTransition])
assert((committedQuery as string) === 'after-clear', 'stale support/target result committed after authority invalidation')
