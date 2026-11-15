// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import { createResourceDeletions } from './refresh'

const mocks = vi.hoisted(() => ({ api: { listConnections: vi.fn(), oauthConfig: vi.fn() } }))
vi.mock('./api', () => ({ ...mocks, normalizeResourceName: (name: string) => name, isPreDispatchError: () => false, isRejectedConnectionWrite: () => false }))
import ConnectionCreateView from './views/ConnectionCreateView.vue'
import { contextGenerationKey } from './context'

const settle = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
const pending = () => { let resolve!: (value: { enabled: boolean; startURL?: string }) => void; const promise = new Promise<{ enabled: boolean; startURL?: string }>(r => { resolve = r }); return { promise, resolve } }
afterEach(() => { document.body.innerHTML = ''; vi.resetAllMocks() })

it.each(['initial', 'retry'] as const)('hands focus to GitHub after the %s fallback disappears', async path => {
  mocks.api.listConnections.mockResolvedValue([])
  const result = pending()
  if (path === 'retry') mocks.api.oauthConfig.mockRejectedValueOnce(new Error('offline')).mockReturnValueOnce(result.promise)
  else mocks.api.oauthConfig.mockReturnValueOnce(result.promise)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(ConnectionCreateView, { method: 'github', deletions: createResourceDeletions(), recoveryScope: 'focus' }) })
  app.mount(host)
  try {
    await settle()
    const button = (text: string) => Array.from(host.querySelectorAll('button')).find(item => item.textContent?.trim() === text)!
    if (path === 'retry') {
      button('Retry GitHub sign-in').focus(); button('Retry GitHub sign-in').click(); await settle()
      expect(document.activeElement).toBe(button('Use a personal access token'))
    } else button('Use a personal access token').focus()
    expect(button('Use a personal access token').classList.contains('k-btn--primary')).toBe(true)
    result.resolve({ enabled: true, startURL: '/oauth/github/start' }); await settle()
    expect(document.activeElement).toBe(button('Continue with GitHub'))
  } finally { app.unmount() }
})

it('does not steal focus moved away while configuration is pending', async () => {
  mocks.api.listConnections.mockResolvedValue([])
  const result = pending(); mocks.api.oauthConfig.mockReturnValueOnce(result.promise)
  const host = document.createElement('div'); const outside = document.createElement('button'); document.body.append(host, outside)
  const app = createApp({ render: () => h(ConnectionCreateView, { method: 'github', deletions: createResourceDeletions(), recoveryScope: 'focus' }) })
  app.mount(host)
  try {
    await settle(); host.querySelectorAll('button')[1].focus(); outside.focus()
    result.resolve({ enabled: true, startURL: '/oauth/github/start' }); await settle()
    expect(document.activeElement).toBe(outside)
  } finally { app.unmount() }
})

it.each(['disabled', 'error'] as const)('keeps the token fallback primary and focused when a context refresh ends %s', async outcome => {
  mocks.api.listConnections.mockResolvedValue([])
  let resolve!: (value: { enabled: boolean }) => void
  let reject!: (error: Error) => void
  const refresh = new Promise<{ enabled: boolean }>((yes, no) => { resolve = yes; reject = no })
  mocks.api.oauthConfig.mockResolvedValueOnce({ enabled: true, startURL: '/oauth/github/start' }).mockReturnValueOnce(refresh)
  const generation = ref(0)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(ConnectionCreateView, { method: 'github', deletions: createResourceDeletions(), recoveryScope: 'focus' }) })
  app.provide(contextGenerationKey, generation)
  app.mount(host)
  try {
    await settle()
    const button = (text: string) => Array.from(host.querySelectorAll('button')).find(item => item.textContent?.trim() === text)
    button('Continue with GitHub')!.focus()
    generation.value++
    await settle()
    const fallback = button('Use a personal access token')!
    expect(fallback.classList.contains('k-btn--primary')).toBe(true)
    expect(button('Continue with GitHub')).toBeUndefined()
    expect(document.activeElement).toBe(fallback)
    if (outcome === 'disabled') resolve({ enabled: false })
    else reject(new Error('offline'))
    await settle()
    expect(button('Use a personal access token')?.classList.contains('k-btn--primary')).toBe(true)
    expect(document.activeElement).toBe(button('Use a personal access token'))
  } finally { app.unmount() }
})

it.each(['blocked', 'closed', 'error'] as const)('offers token recovery after OAuth %s and switches routes', async failure => {
  mocks.api.listConnections.mockResolvedValue([])
  mocks.api.oauthConfig.mockResolvedValue({ enabled: true, startURL: '/oauth/github/start' })
  const popup = { closed: false, close: vi.fn() }
  const open = vi.spyOn(window, 'open').mockReturnValue(failure === 'blocked' ? null : popup as unknown as Window)
  vi.useFakeTimers()
  const host = document.createElement('div'); document.body.append(host)
  const useToken = vi.fn()
  const app = createApp({ render: () => h(ConnectionCreateView, { method: 'github', deletions: createResourceDeletions(), recoveryScope: 'oauth-recovery', onUseToken: useToken }) })
  app.mount(host)
  try {
    await settle()
    const button = (text: string) => Array.from(host.querySelectorAll('button')).find(item => item.textContent?.trim() === text)
    expect(button('Use a personal access token')).toBeUndefined()
    button('Continue with GitHub')!.click(); await settle()
    if (failure !== 'blocked') {
      expect(button('Waiting for GitHub…')).toBeDefined()
      expect(button('Use a personal access token')).toBeUndefined()
      if (failure === 'closed') {
        popup.closed = true
        vi.advanceTimersByTime(500)
      } else {
        const url = new URL(open.mock.calls[0][0] as string)
        window.dispatchEvent(new MessageEvent('message', { origin: url.origin, source: popup as unknown as Window, data: { type: 'railgrid-github-oauth', state: url.searchParams.get('state'), error: 'Authorization failed' } }))
      }
      await settle()
    }
    expect(host.querySelector('[role="alert"]')?.textContent).toBeTruthy()
    const fallback = button('Use a personal access token')!
    expect(fallback).toBeDefined()
    expect(fallback.classList.contains('k-btn--ghost')).toBe(true)
    expect(button('Continue with GitHub')!.classList.contains('k-btn--primary')).toBe(true)
    fallback.focus(); fallback.click()
    expect(useToken).toHaveBeenCalledOnce()
    expect(button('Continue with GitHub')).toBeDefined()
  } finally { app.unmount(); open.mockRestore(); vi.useRealTimers() }
})
