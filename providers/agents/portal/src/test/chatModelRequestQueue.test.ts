import { describe, expect, it } from 'vitest'
import { chatModelRequestKey, serializeChatModelRequest } from '../vue/chatModelRequestQueue'

describe('chat model request queue', () => {
  it('serializes the same caller and chat across a component remount', async () => {
    const authority = { tenantKey: 'org/workspace', userKey: 'user-1' }
    const oldViewKey = chatModelRequestKey(authority, 'scout', 'session-1')
    // A remounted view may have a different runtime epoch, but the same
    // caller and persisted chat still share a request queue.
    const remountedViewKey = chatModelRequestKey({ ...authority }, 'scout', 'session-1')
    const pendingWrite = deferred<void>()
    let readStarted = false

    const oldWrite = serializeChatModelRequest(oldViewKey, () => true, () => pendingWrite.promise)
    const remountedRead = serializeChatModelRequest(remountedViewKey, () => true, async () => {
      readStarted = true
      return 'latest'
    })
    await Promise.resolve()

    expect(readStarted).toBe(false)
    pendingWrite.resolve()
    await expect(oldWrite).resolves.toBeUndefined()
    await expect(remountedRead).resolves.toBe('latest')
    expect(readStarted).toBe(true)
  })

  it('skips a queued request after its captured authority is stale', async () => {
    const key = chatModelRequestKey({ tenantKey: 'org/workspace', userKey: 'user-1' }, 'scout', 'session-1')
    const pendingWrite = deferred<void>()
    let oldAuthorityCurrent = true
    let staleRequestStarted = false

    const oldWrite = serializeChatModelRequest(key, () => oldAuthorityCurrent, () => pendingWrite.promise)
    const staleQueuedRead = serializeChatModelRequest(key, () => oldAuthorityCurrent, async () => {
      staleRequestStarted = true
      return 'stale'
    })
    await Promise.resolve()
    oldAuthorityCurrent = false
    pendingWrite.resolve()

    await oldWrite
    await expect(staleQueuedRead).resolves.toBeUndefined()
    expect(staleRequestStarted).toBe(false)
  })
})

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(res => { resolve = res })
  return { promise, resolve }
}
