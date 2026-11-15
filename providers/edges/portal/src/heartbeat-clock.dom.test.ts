// @vitest-environment happy-dom
import { expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import EdgeCollection from './EdgeCollection.vue'

it('updates heartbeat age without changing the connection snapshot and stops its clock on unmount', async () => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-01-01T00:00:05Z'))
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(EdgeCollection, {
    edges: [{ name: 'edge-a', type: 'kubernetes', connected: true, lastHeartbeatTime: '2026-01-01T00:00:00Z' }],
    loaded: true, loading: false, refreshMode: 'foreground', error: null, foregroundLoading: false,
  }) })
  try {
    app.mount(host)
    await nextTick()
    expect(host.textContent).toContain('5s ago')
    vi.advanceTimersByTime(60_000)
    await nextTick()
    expect(host.textContent).toContain('1m ago')
    expect(host.textContent).toContain('Connected')
    app.unmount()
    expect(vi.getTimerCount()).toBe(0)
  } finally {
    host.remove()
    vi.useRealTimers()
  }
})
