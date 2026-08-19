import { describe, expect, it } from 'vitest'

import { createResourceDeletions } from './refresh'

describe('resource deletions', () => {
  it('retains an acknowledged UID through stale and terminating snapshots until absence', () => {
    const deletions = createResourceDeletions()

    deletions.acknowledge('connection', 'demo', 'old-uid')
    deletions.reconcile('connection', [{ name: 'demo', uid: 'old-uid' }])
    expect(deletions.has('connection', 'demo', 'old-uid')).toBe(true)

    deletions.reconcile('connection', [])
    expect(deletions.has('connection', 'demo', 'old-uid')).toBe(false)
  })

  it('releases an old acknowledgement when the same name has a new UID', () => {
    const deletions = createResourceDeletions()

    deletions.acknowledge('repository', 'demo', 'old-uid')
    deletions.reconcile('repository', [{ name: 'demo', uid: 'new-uid' }])

    expect(deletions.has('repository', 'demo', 'new-uid')).toBe(false)
  })

  it('reconciles repository-scoped child lists independently', () => {
    const deletions = createResourceDeletions()

    deletions.acknowledge('deploy-key:repo-a', 'key-a', 'uid-a')
    deletions.acknowledge('deploy-key:repo-b', 'key-b', 'uid-b')
    deletions.reconcile('deploy-key:repo-b', [])

    expect(deletions.has('deploy-key:repo-a', 'key-a', 'uid-a')).toBe(true)
    expect(deletions.has('deploy-key:repo-b', 'key-b', 'uid-b')).toBe(false)
  })

  it('clears all acknowledged deletions at an authority boundary', () => {
    const deletions = createResourceDeletions()

    deletions.acknowledge('connection', 'demo', 'uid')
    deletions.clear()

    expect(deletions.has('connection', 'demo', 'uid')).toBe(false)
  })
})
