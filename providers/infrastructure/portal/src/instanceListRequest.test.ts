import { describe, expect, it } from 'vitest'

import { isCurrentInstanceListRequest, type InstanceListRequest } from './instanceListRequest'
import { createLatestRefreshController } from './refresh'

const baseRequest: InstanceListRequest = {
  mode: 'client',
  active: true,
  page: 1,
  pageSize: 10,
  query: 'old',
  filters: { template: '', status: '' },
  cursor: null,
}

describe('instance full-list request authority', () => {
  it('commits one query-independent walk after query and filter edits', async () => {
    let current = { ...baseRequest }
    let release!: () => void
    let listCalls = 0
    let commits = 0
    const refresh = createLatestRefreshController(async requestID => {
      const request = { ...current }
      listCalls += 1
      await new Promise<void>(resolve => { release = resolve })
      if (isCurrentInstanceListRequest(request, current, refresh.isCurrent(requestID))) commits += 1
    })

    const read = refresh.request()
    current = { ...current, query: 'new', filters: { template: 'web', status: '' } }
    release()
    await read

    expect(listCalls).toBe(1)
    expect(commits).toBe(1)
    refresh.stop()
  })

  it('rejects page-size and mode changes while allowing filter changes', () => {
    const filtered = { ...baseRequest, query: 'new', filters: { template: 'web', status: '' } }
    expect(isCurrentInstanceListRequest(baseRequest, filtered, true)).toBe(true)
    expect(isCurrentInstanceListRequest(baseRequest, { ...filtered, pageSize: 25 }, true)).toBe(false)
    expect(isCurrentInstanceListRequest(baseRequest, { ...filtered, mode: 'server', active: false }, true)).toBe(false)
    expect(isCurrentInstanceListRequest(baseRequest, filtered, false)).toBe(false)
  })

  it('serializes clear-to-server and re-entry-to-client reads', async () => {
    type ReadMode = 'server' | 'client'
    let mode: ReadMode = 'server'
    let releaseServer!: () => void
    let releaseClient!: () => void
    let activeReads = 0
    let maximumConcurrentReads = 0
    const calls: ReadMode[] = []
    const commits: ReadMode[] = []
    const refresh = createLatestRefreshController(async requestID => {
      const requestMode = mode
      calls.push(requestMode)
      activeReads += 1
      maximumConcurrentReads = Math.max(maximumConcurrentReads, activeReads)
      try {
        await new Promise<void>(resolve => {
          if (requestMode === 'server') releaseServer = resolve
          else releaseClient = resolve
        })
        if (refresh.isCurrent(requestID) && mode === requestMode) commits.push(requestMode)
      } finally {
        activeReads -= 1
      }
    })

    const clearRead = refresh.request()
    mode = 'client'
    const reentryRead = refresh.request()
    expect(calls).toEqual(['server'])

    releaseServer()
    for (let attempt = 0; attempt < 10 && calls.length < 2; attempt += 1) {
      await Promise.resolve()
    }
    expect(calls).toEqual(['server', 'client'])
    releaseClient()
    await Promise.all([clearRead, reentryRead])

    expect(maximumConcurrentReads).toBe(1)
    expect(commits).toEqual(['client'])
    refresh.stop()
  })
})
