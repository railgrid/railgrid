// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { expect, it, vi } from 'vitest'
import DashboardTile from './DashboardTile.vue'
import { setHostFetch, setTenant } from './api'
import type { TileContext } from './portalkit/dashboardtile'

it('uses the replacement host transport while retaining a known caller snapshot', async () => {
  const empty = () => new Response(JSON.stringify({ items: [], metadata: {} }), { headers: { 'Content-Type': 'application/json' } })
  const first = vi.fn(async () => empty())
  let finish!: (response: Response) => void
  const second = vi.fn(() => new Promise<Response>(resolve => { finish = resolve }))
  const context = ref<TileContext>({ tenant: 'cluster-a', user: { email: 'one@example.com' }, fetch: first })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp({ render: () => h(DashboardTile, { context: context.value }) })
  try {
    app.mount(host)
    await vi.waitFor(() => expect(first).toHaveBeenCalled())
    await vi.waitFor(() => expect(host.querySelector('[aria-busy]')?.getAttribute('aria-busy')).toBe('false'))
    expect(host.textContent).toContain('No instances yet')
    context.value = { ...context.value, fetch: second }
    await nextTick()
    await vi.waitFor(() => expect(second).toHaveBeenCalled())
    await nextTick()

    expect(host.textContent).toContain('No instances yet')
    finish(empty())
    await vi.waitFor(() => expect(host.textContent).toContain('No instances yet'))
    expect(first).toHaveBeenCalled()
  } finally {
    app.unmount()
    host.remove()
    setHostFetch(null)
    setTenant(null)
  }
})

it('waits for host access instead of using a token or global fetch, then recovers', async () => {
  const globalFetch = vi.spyOn(globalThis, 'fetch')
  const hostFetch = vi.fn(async () => new Response(JSON.stringify({ items: [], metadata: {} }), { headers: { 'Content-Type': 'application/json' } }))
  const context = ref<TileContext>({ tenant: 'cluster-a', token: 'legacy', user: { email: 'one@example.com' } })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp({ render: () => h(DashboardTile, { context: context.value }) })
  try {
    app.mount(host)
    await vi.waitFor(() => expect(host.textContent).toContain('Waiting for host access'))
    expect(host.textContent).not.toContain('No instances yet')
    expect(globalFetch).not.toHaveBeenCalled()
    context.value = { ...context.value, fetch: hostFetch }
    await vi.waitFor(() => expect(hostFetch).toHaveBeenCalled())
    await vi.waitFor(() => expect(host.textContent).toContain('No instances yet'))
    expect(host.textContent).not.toContain('Waiting for host access')
    expect(globalFetch).not.toHaveBeenCalled()
  } finally {
    app.unmount()
    host.remove()
    globalFetch.mockRestore()
    setHostFetch(null)
    setTenant(null)
  }
})
