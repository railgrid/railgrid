// @vitest-environment happy-dom
import { createApp, h, KeepAlive, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import { createResourceDeletions } from './refresh'
import { contextGenerationKey } from './context'
const mocks = vi.hoisted(() => ({ api: { listConnections: vi.fn(), oauthConfig: vi.fn() } }))
vi.mock('./api', () => ({ ...mocks }))
import ConnectionsView from './views/ConnectionsView.vue'
const settle = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
function deferred() {
  let resolve!: (value: { enabled: boolean }) => void
  const promise = new Promise<{ enabled: boolean }>(r => { resolve = r })
  return { promise, resolve }
}
afterEach(() => { vi.useRealTimers(); vi.resetAllMocks(); document.body.innerHTML = '' })
it('times out a stalled read, retains token setup, and hands off retry focus', async () => {
  vi.useFakeTimers()
  const stalled = deferred()
  mocks.api.listConnections.mockResolvedValue([])
  mocks.api.oauthConfig.mockReturnValueOnce(stalled.promise).mockResolvedValueOnce({ enabled: true })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(KeepAlive, null, { default: () => h(ConnectionsView, { deletions: createResourceDeletions() }) }) })
  app.mount(host)
  try {
    await settle()
    expect(host.textContent).toContain('Add token manually')
    await vi.advanceTimersByTimeAsync(15_000); await settle()
    expect(host.textContent).toContain('The request timed out.')
    const retry = Array.from(host.querySelectorAll('button')).find(button => button.textContent?.includes('Retry GitHub sign-in'))!
    retry.focus(); retry.click(); await settle()
    expect(document.activeElement?.textContent).toContain('Connections')
    expect(host.textContent).toContain('Connect with GitHub')
    stalled.resolve({ enabled: false }); await settle()
    expect(host.textContent).toContain('Connect with GitHub')
  } finally { app.unmount() }
})
it('rejects older authority reads and ignores completion after unmount', async () => {
  const old = deferred(), current = deferred()
  mocks.api.listConnections.mockResolvedValue([])
  mocks.api.oauthConfig.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
  const authority = ref(0)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(KeepAlive, null, { default: () => h(ConnectionsView, { deletions: createResourceDeletions() }) }) })
  app.provide(contextGenerationKey, authority); app.mount(host)
  await settle(); authority.value++; await settle()
  current.resolve({ enabled: true }); await settle()
  old.resolve({ enabled: false }); await settle()
  expect(host.textContent).toContain('Connect with GitHub')
  app.unmount()
  expect(host.textContent).toBe('')
})
it('uses the latest overlapping retry and ignores a pending result after unmount', async () => {
  const first = deferred(), second = deferred(), last = deferred()
  mocks.api.listConnections.mockResolvedValue([])
  mocks.api.oauthConfig.mockRejectedValueOnce(new Error('offline')).mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockReturnValueOnce(last.promise)
  const authority = ref(0)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(KeepAlive, null, { default: () => h(ConnectionsView, { deletions: createResourceDeletions() }) }) })
  app.provide(contextGenerationKey, authority); app.mount(host)
  await settle()
  const retry = Array.from(host.querySelectorAll('button')).find(button => button.textContent?.includes('Retry GitHub sign-in'))!
  retry.click(); retry.click(); await settle()
  second.resolve({ enabled: true }); await settle()
  first.resolve({ enabled: false }); await settle()
  expect(host.textContent).toContain('Connect with GitHub')
  authority.value++; await settle()
  app.unmount(); last.resolve({ enabled: false }); await settle()
  expect(host.textContent).toBe('')
})
