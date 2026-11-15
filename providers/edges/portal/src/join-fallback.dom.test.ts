// @vitest-environment happy-dom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

const api = vi.hoisted(() => ({ createEdge: vi.fn(), probeEdge: vi.fn(), getEdge: vi.fn(), listEdgeServices: vi.fn(), isContextChangedError: () => false }))
vi.mock('./api', () => api)
import Wizard from './Wizard.vue'
import Detail from './Detail.vue'

const settle = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
beforeEach(() => {
  const values = new Map<string, string>()
  vi.stubGlobal('localStorage', { get length() { return values.size }, key: (i: number) => [...values.keys()][i] ?? null, getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key, value), removeItem: (key: string) => values.delete(key) })
})
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.resetAllMocks(); document.body.innerHTML = '' })

it.each(['kubernetes', 'server', 'macos'] as const)('offers a scoped CLI fallback when %s setup cannot copy', async type => {
  vi.useFakeTimers()
  vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('clipboard denied'))
  api.createEdge.mockResolvedValue(undefined)
  api.probeEdge.mockResolvedValue({ joinToken: 'secret-token', connected: false })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a', requiredType: type })
  app.mount(host)
  try {
    const input = host.querySelector<HTMLInputElement>('#edge-name')!
    input.value = 'edge-a'; input.dispatchEvent(new Event('input'))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle(); await vi.advanceTimersByTimeAsync(2500); await settle()
    host.querySelector<HTMLButtonElement>('[aria-label="Copy CLI command"]')!.click(); await settle()
    const alert = [...host.querySelectorAll('[role="alert"]')].map(node => node.textContent).find(text => text?.includes('Clipboard access'))!
    expect(alert).toContain(`railgrid edge join-command '${type}/edge-a'`)
    expect(alert).toContain('same hub and workspace')
    expect(host.textContent).not.toContain('secret-token')
  } finally { app.unmount() }
})

it('offers a usable fallback for a masked macOS detail command', async () => {
  vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('clipboard denied'))
  api.getEdge.mockResolvedValue({ name: 'mini', type: 'macos', connected: false, joinToken: 'secret-token', conditions: [], spec: { labels: {} }, rawObject: { metadata: { name: 'mini' } } })
  api.listEdgeServices.mockResolvedValue([])
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Detail, { name: 'mini', type: 'macos', cluster: 'tenant-a', token: null })
  app.mount(host)
  try {
    await settle()
    host.querySelector<HTMLButtonElement>('[aria-label="Copy agent join command"]')!.click(); await settle()
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("railgrid edge join-command 'macos/mini'")
    expect(host.textContent).not.toContain('secret-token')
    expect(host.textContent).not.toContain('Select the command and copy it manually')
  } finally { app.unmount() }
})
