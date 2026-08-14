import { createLatestRefreshController } from './refresh.js'

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
