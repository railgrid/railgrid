import { describe, expect, it, vi } from 'vitest'
import type { ApiClient } from '../api'
import { AgentsDashboardTileElement } from '../element'
import { createTilePoller } from '../portalkit/dashboardtile'
import { agentFixture, settle, stubApi, text } from './helpers'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

const TAG = 'agents-dashboard-tile-vue-test'
if (!customElements.get(TAG)) customElements.define(TAG, AgentsDashboardTileElement)

async function mountTile(api: ApiClient): Promise<AgentsDashboardTileElement> {
  const tile = document.createElement(TAG) as AgentsDashboardTileElement
  document.body.appendChild(tile)
  await settle(tile)
  Object.assign(tile.api!, api)
  tile.farosContext = { tenant: 'root:faros:tenants:org:ws', orgUUID: 'org', workspaceUUID: 'ws' }
  await tile.load()
  await settle(tile)
  return tile
}

describe('agents dashboard tile refresh resilience', () => {
  it('navigates recent runs through the provider hash route', async () => {
    const tile = await mountTile(stubApi({
      listRuns: vi.fn().mockResolvedValue({
        items: [{
          id: 'run/42',
          agent: 'scout',
          trigger: 'chat',
          class: 'interactive',
          phase: 'Succeeded',
          inputTokens: 1,
          outputTokens: 1,
          usdMicros: 0,
          createdAt: new Date().toISOString(),
          durationMS: 10,
        }],
        nextCursor: '',
      }),
    }))
    const navigate = vi.fn()
    tile.addEventListener('faros-navigate', navigate)

    tile.querySelector<HTMLButtonElement>('.agents-tile-rows button')!.click()

    expect(navigate).toHaveBeenCalledOnce()
    expect((navigate.mock.calls[0][0] as CustomEvent).detail).toEqual({
      provider: 'agents',
      path: '#/activity/run%2F42',
    })
    tile.remove()
  })

  it('does not run a coalesced refresh after the poller stops', async () => {
    let release!: () => void
    const load = vi.fn(async () => new Promise<void>(resolve => { release = resolve }))
    const poller = createTilePoller(load, 60_000)
    poller.start()
    poller.refresh()
    expect(load).toHaveBeenCalledOnce()
    poller.stop()
    release()
    await Promise.resolve()
    await Promise.resolve()
    expect(load).toHaveBeenCalledOnce()
  })

  it('retains populated and empty snapshots when a later poll fails', async () => {
    const populated = await mountTile(stubApi({
      listAgents: vi.fn().mockResolvedValue([agentFixture('scout')]),
      listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }),
      listSchedules: vi.fn().mockResolvedValue([]),
    }))
    expect(text(populated)).toContain('1 agent')
    Object.assign(populated.api!, stubApi({ listAgents: vi.fn().mockRejectedValue(new Error('temporarily unavailable')) }))
    await populated.load()
    await settle(populated)
    expect(text(populated)).toContain('1 agent')
    expect(text(populated.querySelector('.agents-tile-err'))).toContain('Showing the last loaded data')
    populated.remove()

    const empty = await mountTile(stubApi({ listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }) }))
    expect(text(empty)).toContain('No agents yet')
    Object.assign(empty.api!, stubApi({ listAgents: vi.fn().mockRejectedValue(new Error('temporarily unavailable')) }))
    await empty.load()
    await settle(empty)
    expect(text(empty)).toContain('No agents yet')
    expect(text(empty.querySelector('.agents-tile-err'))).toContain('Showing the last loaded data')
  })

  it('fences a same-workspace response when the host rotates authority', async () => {
    const stale = deferred<ReturnType<typeof agentFixture>[]>()
    const listAgents = vi.fn()
      .mockImplementationOnce(() => stale.promise)
      .mockResolvedValue([agentFixture('new-authority')])
    const tile = document.createElement(TAG) as AgentsDashboardTileElement
    document.body.appendChild(tile)
    await settle(tile)
    Object.assign(tile.api!, stubApi({
      listAgents,
      listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }),
      listSchedules: vi.fn().mockResolvedValue([]),
    }))

    tile.farosContext = { tenant: 'root:faros:tenants:org:ws', orgUUID: 'org', workspaceUUID: 'ws', token: 'shared', user: { userId: 'alice' } }
    await Promise.resolve()
    tile.farosContext = { tenant: 'root:faros:tenants:org:ws', orgUUID: 'org', workspaceUUID: 'ws', token: 'shared', user: { userId: 'bob' } }
    stale.resolve([agentFixture('stale-one'), agentFixture('stale-two')])
    await settle(tile)
    await settle(tile)

    expect(text(tile)).toContain('1 agent')
    expect(text(tile)).not.toContain('2 agents')
    expect(listAgents).toHaveBeenCalledTimes(2)
    tile.remove()
  })

  it('clears a prior snapshot when a different caller in the same workspace cannot refresh it', async () => {
    const tile = await mountTile(stubApi({
      listAgents: vi.fn().mockResolvedValue([agentFixture('alice-agent')]),
      listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }),
      listSchedules: vi.fn().mockResolvedValue([]),
    }))
    expect(text(tile)).toContain('1 agent')

    const denied = deferred<ReturnType<typeof agentFixture>[]>()
    Object.assign(tile.api!, stubApi({
      listAgents: vi.fn().mockImplementation(() => denied.promise),
      listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }),
      listSchedules: vi.fn().mockResolvedValue([]),
    }))

    tile.farosContext = {
      tenant: 'root:faros:tenants:org:ws',
      orgUUID: 'org',
      workspaceUUID: 'ws',
      token: 'bob-token',
      user: { userId: 'bob' },
    }
    await settle(tile)

    expect(text(tile)).toContain('Loading agents')
    expect(text(tile)).not.toContain('alice-agent')
    expect(text(tile)).not.toContain('1 agent')

    denied.reject(new Error('forbidden'))
    await settle(tile, 6)

    expect(text(tile)).toContain('Failed to load: forbidden')
    expect(text(tile)).not.toContain('alice-agent')
    tile.remove()
  })
})
