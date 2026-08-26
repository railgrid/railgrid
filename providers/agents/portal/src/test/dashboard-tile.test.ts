import { describe, expect, it, vi } from 'vitest'
import type { ApiClient } from '../api'
import { AgentsDashboardTile } from '../views/dashboard-tile'
import { createTilePoller } from '../portalkit/dashboardtile'
import { agentFixture, settle, stubApi, text } from './helpers'

const TAG = 'agents-dashboard-tile-test'
if (!customElements.get(TAG)) customElements.define(TAG, AgentsDashboardTile)

async function mountTile(api: ApiClient): Promise<AgentsDashboardTile> {
  const tile = document.createElement(TAG) as AgentsDashboardTile
  ;(tile as unknown as { api: ApiClient }).api = api
  tile.farosContext = { tenant: 'root:faros:tenants:org:ws', orgUUID: 'org', workspaceUUID: 'ws' }
  document.body.appendChild(tile)
  await settle(tile)
  return tile
}

describe('agents dashboard tile refresh resilience', () => {
  it('does not run a coalesced refresh after the poller stops', async () => {
    let release!: () => void
    const load = vi.fn(async () => new Promise<void>((resolve) => { release = resolve }))
    const poller = createTilePoller(load, 60_000)

    poller.start()
    poller.refresh()
    expect(load).toHaveBeenCalledTimes(1)
    poller.stop()
    release()
    await Promise.resolve()
    await Promise.resolve()

    expect(load).toHaveBeenCalledTimes(1)
  })

  it('retains a populated snapshot when a later poll fails', async () => {
    const api = stubApi({
      listAgents: vi.fn().mockResolvedValue([agentFixture('scout')]),
      listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }),
      listSchedules: vi.fn().mockResolvedValue([]),
    })
    const tile = await mountTile(api)
    expect(text(tile)).toContain('1 agent')

    ;(tile as unknown as { api: ApiClient }).api = stubApi({
      listAgents: vi.fn().mockRejectedValue(new Error('temporarily unavailable')),
      listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }),
      listSchedules: vi.fn().mockResolvedValue([]),
    })
    await (tile as unknown as { load(): Promise<void> }).load()
    await settle(tile)

    expect(text(tile)).toContain('1 agent')
    expect(text(tile.querySelector('.agents-tile-err'))).toContain('Showing the last loaded data')
    tile.remove()
  })

  it('retains an authoritative empty snapshot when a later poll fails', async () => {
    const api = stubApi({
      listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }),
    })
    const tile = await mountTile(api)
    expect(text(tile)).toContain('No agents yet')

    ;(tile as unknown as { api: ApiClient }).api = stubApi({
      listAgents: vi.fn().mockRejectedValue(new Error('temporarily unavailable')),
      listRuns: vi.fn().mockResolvedValue({ items: [], nextCursor: '' }),
      listSchedules: vi.fn().mockResolvedValue([]),
    })
    await (tile as unknown as { load(): Promise<void> }).load()
    await settle(tile)

    expect(text(tile)).toContain('No agents yet')
    expect(text(tile.querySelector('.agents-tile-err'))).toContain('Showing the last loaded data')
    tile.remove()
  })
})
