// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { expect, it, vi } from 'vitest'
import type { RailgridContext } from './types'
const state = vi.hoisted(() => ({ token: '', reads: [] as string[] }))
vi.mock('./api', async importOriginal => ({
  ...await importOriginal<typeof import('./api')>(),
  setBasePath: vi.fn(),
  setAPIContext: (ctx: { token?: string | null }) => { state.token = ctx.token || '' },
  api: {
    listConnections: vi.fn(async () => []),
    oauthConfig: vi.fn(async () => { state.reads.push(state.token); return { enabled: true } }),
  },
}))
import App from './App.vue'
const settle = async () => { for (let i = 0; i < 10; i++) { await Promise.resolve(); await nextTick() } }
it('reads OAuth with the installed token after a mounted App renews credentials', async () => {
  state.reads.length = 0
  const ctx = ref<RailgridContext>({ tenant: 'workspace', user: { sub: 'user' }, token: 'old', subPath: 'connections' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx: ctx.value }) })
  app.mount(host)
  try {
    await settle()
    expect(state.reads).toEqual(['old'])
    ctx.value = { ...ctx.value, token: 'new' }
    await settle()
    expect(state.reads).toEqual(['old', 'new'])
    expect(host.textContent).toContain('Connect with GitHub')
  } finally { app.unmount(); host.remove() }
})
