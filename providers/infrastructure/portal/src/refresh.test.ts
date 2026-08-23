import { describe, expect, it } from 'vitest'

import { createLatestRefreshController, createResourceTombstones, sameResourceIdentity } from './refresh'

describe('latest refresh controller', () => {
  it('only lets the queued latest read commit after an older read settles', async () => {
    let releaseFirst!: () => void
    let calls = 0
    const committed: number[] = []
    const refresh = createLatestRefreshController(async requestID => {
      if (calls++ === 0) await new Promise<void>(resolve => { releaseFirst = resolve })
      if (refresh.isCurrent(requestID)) committed.push(requestID)
    })

    const first = refresh.request()
    const second = refresh.request()
    releaseFirst()
    await Promise.all([first, second])

    expect(committed).toHaveLength(1)
    refresh.stop()
  })

  it('starts a latest query walk after an older walk is rejected as stale', async () => {
    let releaseFirst!: () => void
    let query = 'old'
    const calls: string[] = []
    const committed: string[] = []
    const refresh = createLatestRefreshController(async requestID => {
      const requestQuery = query
      calls.push(requestQuery)
      if (requestQuery === 'old') await new Promise<void>(resolve => { releaseFirst = resolve })
      if (refresh.isCurrent(requestID) && requestQuery === query) committed.push(requestQuery)
    })

    const first = refresh.request()
    query = 'new'
    const second = refresh.request()
    releaseFirst()
    await Promise.all([first, second])

    expect(calls).toEqual(['old', 'new'])
    expect(committed).toEqual(['new'])
    refresh.stop()
  })
})

describe('resource tombstones', () => {
  it('keeps a Back or direct-route stale same-UID read marked until list absence', () => {
    const tombstones = createResourceTombstones()

    tombstones.add('demo', 'old-uid')
    expect(tombstones.has('demo', 'old-uid')).toBe(true)

    // A newly mounted detail/list route sees the same shared marker, so a
    // stale read cannot repaint the acknowledged UID as active.
    tombstones.reconcile([{ name: 'demo', uid: 'old-uid' }])
    expect(tombstones.has('demo', 'old-uid')).toBe(true)

    tombstones.reconcile([])
    expect(tombstones.has('demo', 'old-uid')).toBe(false)
  })

  it('retains a tombstone through termination and stale snapshots until true absence', () => {
    const tombstones = createResourceTombstones()

    tombstones.add('demo', 'old-uid')

    // listInstances renders this terminating resource as Deleting and returns
    // its raw identity for marker reconciliation.
    tombstones.reconcile([{ name: 'demo', uid: 'old-uid' }])
    expect(tombstones.has('demo', 'old-uid')).toBe(true)

    // An older list snapshot that still presents the object as active must not
    // resurrect the acknowledged deletion.
    tombstones.reconcile([{ name: 'demo', uid: 'old-uid' }])
    expect(tombstones.has('demo', 'old-uid')).toBe(true)

    tombstones.reconcile([])
    expect(tombstones.has('demo', 'old-uid')).toBe(false)

    tombstones.reconcile([{ name: 'demo', uid: 'new-uid' }])
    expect(tombstones.has('demo', 'new-uid')).toBe(false)
  })

  it('reveals a same-name replacement with a different UID', () => {
    const tombstones = createResourceTombstones()
    tombstones.add('demo', 'old-uid')

    tombstones.reconcile([{ name: 'demo', uid: 'new-uid' }])

    expect(tombstones.has('demo', 'new-uid')).toBe(false)
  })

  it('keeps an unknown-UID deletion marked until the name is absent', () => {
    const tombstones = createResourceTombstones()
    tombstones.add('demo')

    // Without a UID the current row cannot prove whether it is the object
    // acknowledged for deletion or a same-name replacement.
    tombstones.reconcile([{ name: 'demo', uid: 'new-uid' }])
    expect(tombstones.has('demo', 'new-uid')).toBe(true)

    tombstones.reconcile([])
    expect(tombstones.has('demo', 'new-uid')).toBe(false)
  })

  it('clears acknowledged deletions when authority changes', () => {
    const tombstones = createResourceTombstones()
    tombstones.add('demo', 'old-uid')

    tombstones.clear()

    expect(tombstones.has('demo', 'old-uid')).toBe(false)
  })
})

describe('resource identity revalidation', () => {
  it('rejects a same-name replacement that appears while confirmation is open', () => {
    const expected = { name: 'demo', uid: 'old-uid' }

    expect(sameResourceIdentity(expected, { name: 'demo', uid: 'new-uid' })).toBe(false)
    expect(sameResourceIdentity(expected, { name: 'demo', uid: 'old-uid' })).toBe(true)
  })

  it('requires the same object reference when the API omitted UID', () => {
    const expected = { name: 'demo' }

    expect(sameResourceIdentity(expected, expected)).toBe(true)
    expect(sameResourceIdentity(expected, { name: 'demo' })).toBe(false)
  })
})
