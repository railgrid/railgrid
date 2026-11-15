// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
const api = vi.hoisted(() => ({ getEdge: vi.fn(), listEdgeServices: vi.fn(), isContextChangedError: () => false }))
vi.mock('./api', () => api)
import Detail from './Detail.vue'
const settle = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
afterEach(() => { document.body.innerHTML = ''; vi.resetAllMocks() })
it.each(['success', 'moved', 'scope'] as const)('edge retry focus: %s', async outcome => {
  api.getEdge.mockRejectedValueOnce(new Error('Unavailable')); api.listEdgeServices.mockResolvedValue([])
  let resolve!: (value: any) => void
  api.getEdge.mockReturnValueOnce(new Promise(r => { resolve = r }))
  const generation = ref(1); const host = document.createElement('div'); const outside = document.createElement('button'); document.body.append(host, outside)
  const app = createApp({ render: () => h(Detail, { name: 'demo', type: 'server', cluster: 'tenant', token: null, authorityGeneration: generation.value }) })
  app.mount(host)
  try {
    await settle(); const retry = host.querySelector<HTMLButtonElement>('.k-resource-page__retry')!; expect(retry).toBeTruthy(); retry.focus(); if (outcome === 'moved') outside.focus(); if (outcome === 'scope') generation.value++; retry.click(); await nextTick(); resolve({ name: 'demo', type: 'server', conditions: [], spec: { labels: {} }, rawObject: { metadata: { name: 'demo' } } }); await settle()
    expect(document.activeElement).toBe(outcome === 'success' ? host.querySelector('.k-resource-page__title') : outcome === 'moved' ? outside : document.body)
  } finally { app.unmount() }
})
