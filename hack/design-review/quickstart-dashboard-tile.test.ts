import { expect, it, onTestFinished, vi } from 'vitest'
import { QuickstartDashboardTileElement } from '../../providers/quickstart/portal/src/tile'
import { createKubeClient } from '../../providers/quickstart/portal/src/portalkit/kube'

vi.mock('../../providers/quickstart/portal/src/portalkit/kube', () => ({ createKubeClient: vi.fn() }))
vi.mock('../../providers/quickstart/portal/src/element', () => ({ escapeHTML: (value: string) => value }))

it('keeps retry focus and returns it to the restored summary', async () => {
  let release!: (value: never[]) => void
  const listAll = vi.fn().mockRejectedValueOnce(new Error('Unavailable'))
    .mockImplementationOnce(() => new Promise(resolve => { release = resolve }))
    .mockResolvedValue([])
  vi.mocked(createKubeClient).mockReturnValue({ listAll } as never)
  const tag = 'quickstart-dashboard-retry-test'
  if (!customElements.get(tag)) customElements.define(tag, QuickstartDashboardTileElement)
  const tile = document.createElement(tag) as QuickstartDashboardTileElement
  tile.railgridContext = { tenant: 'review', fetch: async () => new Response() }
  document.body.append(tile)
  onTestFinished(() => { tile.remove(); vi.resetAllMocks() })
  await vi.waitFor(() => expect(tile.textContent).toContain('Could not load greetings'))
  const retry = tile.querySelector<HTMLButtonElement>('[data-retry]')!
  retry.focus(); retry.click(); retry.click()
  expect(retry.getAttribute('aria-busy')).toBe('true')
  expect(document.activeElement).toBe(retry)
  expect(listAll).toHaveBeenCalledTimes(2)
  tile.railgridContext = { tenant: 'review', fetch: async () => new Response() }
  expect(document.activeElement).toBe(retry)
  release([])
  await vi.waitFor(() => expect(tile.textContent).toContain('No greetings yet'))
  expect(document.activeElement).toBe(tile)
})
