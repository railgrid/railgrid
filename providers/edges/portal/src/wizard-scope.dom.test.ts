// @vitest-environment happy-dom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive } from 'vue'

const api = vi.hoisted(() => ({ createEdge: vi.fn(), getEdge: vi.fn(), probeEdge: vi.fn(), listEdges: vi.fn(), setTenant: vi.fn(), setToken: vi.fn(), setHostFetch: vi.fn() }))
vi.mock('./api', async importOriginal => ({ ...await importOriginal<typeof import('./api')>(), ...api }))
import App from './App.vue'

const settle = async () => { for (let i = 0; i < 5; i++) { await Promise.resolve(); await nextTick() } }
beforeEach(() => {
  const values = new Map<string, string>()
  vi.stubGlobal('localStorage', { get length() { return values.size }, key: (i: number) => [...values.keys()][i] ?? null, getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key, value), removeItem: (key: string) => values.delete(key) })
})
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(accept => { resolve = accept })
  return { promise, resolve }
}
afterEach(() => { vi.useRealTimers(); vi.resetAllMocks(); document.body.innerHTML = '' })

it.each(['create', 'probe'] as const)('clears the wizard and fences an in-flight %s on tenant switch', async phase => {
  vi.useFakeTimers()
  api.listEdges.mockResolvedValue([])
  const pendingCreate = deferred<void>()
  const pendingProbe = deferred<{ joinToken: string; connected: boolean }>()
  api.createEdge.mockReturnValue(phase === 'create' ? pendingCreate.promise : Promise.resolve())
  api.probeEdge.mockReturnValue(pendingProbe.promise)
  const state = reactive({ ctx: { tenant: 'tenant-a', token: 'token', user: { userId: 'user-a' }, subPath: 'connect/edge/kubernetes' } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx: state.ctx }) })
  try {
    app.mount(host); await settle()
    const name = host.querySelector('#edge-name') as HTMLInputElement
    name.value = 'old-edge'; name.dispatchEvent(new Event('input', { bubbles: true }))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    if (phase === 'probe') {
      await vi.advanceTimersByTimeAsync(2500); await settle()
      expect(api.probeEdge).toHaveBeenCalledTimes(1)
    }
    const signal = phase === 'probe' ? api.probeEdge.mock.calls[0][2] as AbortSignal : null
    state.ctx = { ...state.ctx, tenant: 'tenant-b' }; await settle()
    expect((host.querySelector('#edge-name') as HTMLInputElement).value).toBe('')
    if (signal) expect(signal.aborted).toBe(true)
    pendingCreate.resolve()
    pendingProbe.resolve({ joinToken: 'old-secret', connected: false })
    await settle()
    expect(host.textContent).not.toContain('old-secret')
    expect((host.querySelector('#edge-name') as HTMLInputElement).value).toBe('')
    if (phase === 'create') expect(api.probeEdge).not.toHaveBeenCalled()
  } finally { app.unmount() }
})

it('fences an uncertain-create inspection across tenant changes', async () => {
  api.listEdges.mockResolvedValue([])
  api.createEdge.mockRejectedValue({ reason: 'EdgeCreateUnknown' })
  const pending = deferred<{ name: string }>()
  api.getEdge.mockReturnValue(pending.promise)
  const state = reactive({ ctx: { tenant: 'tenant-a', token: 'token', user: { userId: 'user-a' }, subPath: 'connect/edge/kubernetes' } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx: state.ctx }) })
  try {
    app.mount(host); await settle()
    const input = host.querySelector('#edge-name') as HTMLInputElement
    input.value = 'old-edge'; input.dispatchEvent(new Event('input', { bubbles: true }))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Check edge'))!.click(); await settle()
    state.ctx = { ...state.ctx, tenant: 'tenant-b' }; await settle()
    pending.resolve({ name: 'old-edge' }); await settle()
    expect(host.textContent).not.toContain('Review edge')
    expect((host.querySelector('#edge-name') as HTMLInputElement).value).toBe('')
  } finally { app.unmount() }
})
