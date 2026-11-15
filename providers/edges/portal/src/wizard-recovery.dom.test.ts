// @vitest-environment happy-dom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
const api = vi.hoisted(() => ({ createEdge: vi.fn(), getEdge: vi.fn(), probeEdge: vi.fn(), isContextChangedError: (error: unknown) => (error as { reason?: string })?.reason === 'ContextChanged' }))
vi.mock('./api', () => api)
import Wizard from './Wizard.vue'
beforeEach(() => {
  const values = new Map<string, string>()
  vi.stubGlobal('localStorage', { get length() { return values.size }, key: (index: number) => [...values.keys()][index] ?? null, getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key, value), removeItem: (key: string) => values.delete(key), clear: () => values.clear() })
})
const settle = async () => { for (let i = 0; i < 5; i++) { await Promise.resolve(); await nextTick() } }
afterEach(() => { vi.useRealTimers(); vi.resetAllMocks(); document.body.innerHTML = ''; localStorage.clear() })
function button(host: HTMLElement, text: string): HTMLButtonElement { return [...host.querySelectorAll('button')].find(item => item.textContent?.includes(text))! }
async function mountUncertain() {
  api.createEdge.mockRejectedValue({ reason: 'EdgeCreateUnknown' })
  const host = document.createElement('div'); document.body.append(host)
  const inspect = vi.fn()
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a', onInspect: inspect }); app.mount(host)
  const input = host.querySelector('#edge-name') as HTMLInputElement
  input.value = 'edge-a'; input.dispatchEvent(new Event('input', { bubbles: true }))
  const labels = host.querySelector('#edge-labels') as HTMLInputElement
  labels.value = 'env=prod'; labels.dispatchEvent(new Event('input', { bubbles: true }))
  host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
  return { host, app, inspect, input, labels }
}
it.each(['found', 'missing', 'error'] as const)('requires inspection before another request: %s', async result => {
  const { host, app, inspect, input, labels } = await mountUncertain()
  try {
    expect(button(host, 'Create & continue').disabled).toBe(true)
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    expect(api.createEdge).toHaveBeenCalledTimes(1)
    if (result === 'found') api.getEdge.mockResolvedValue({ name: 'edge-a' })
    else api.getEdge.mockRejectedValue(result === 'missing' ? { reason: 'NotFound' } : { message: 'Unavailable' })
    button(host, 'Check edge').click(); await settle()
    expect(api.getEdge).toHaveBeenCalledWith('edge-a', 'kubernetes', expect.any(AbortSignal))
    expect(input.value).toBe('edge-a'); expect(labels.value).toBe('env=prod')
    if (result === 'error') {
      expect(host.textContent).toContain('Could not check the edge')
      expect(button(host, 'Create & continue').disabled).toBe(true)
      expect(host.textContent).not.toContain('Allow another request')
    } else {
      expect(host.textContent).toContain(result === 'found' ? 'does not establish which request' : 'may still be processing')
      if (result === 'found') { button(host, 'Review edge').click(); expect(inspect).toHaveBeenCalledWith('edge-a', 'kubernetes') }
      else {
        const allow = button(host, 'Allow another request')
        allow.focus(); allow.click(); await settle()
        expect(document.activeElement).toBe(input)
        expect(button(host, 'Create & continue').disabled).toBe(false)
      }
    }
  } finally { app.unmount() }
})

it('requires recovery when authentication changes after create was sent', async () => {
  let reject!: (error: unknown) => void
  api.createEdge.mockReturnValue(new Promise((_, fail) => { reject = fail }))
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a' }); app.mount(host)
  try {
    const input = host.querySelector('#edge-name') as HTMLInputElement
    input.value = 'edge-a'; input.dispatchEvent(new Event('input', { bubbles: true }))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    reject({ reason: 'ContextChanged' }); await settle()
    expect(host.textContent).toContain('may have succeeded')
    expect(button(host, 'Create & continue').disabled).toBe(true)
    expect(api.probeEdge).not.toHaveBeenCalled()
  } finally { app.unmount() }
})

it('bounds a stalled create and inspection without retrying', async () => {
  vi.useFakeTimers()
  api.createEdge.mockReturnValue(new Promise(() => {}))
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a' }); app.mount(host)
  try {
    const input = host.querySelector('#edge-name') as HTMLInputElement
    input.value = 'edge-a'; input.dispatchEvent(new Event('input', { bubbles: true }))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    await vi.advanceTimersByTimeAsync(15000); await settle()
    expect(host.textContent).toContain('may have succeeded')
    expect((api.createEdge.mock.calls[0][3] as AbortSignal).aborted).toBe(true)
    api.getEdge.mockReturnValue(new Promise(() => {}))
    button(host, 'Check edge').click(); await settle()
    await vi.advanceTimersByTimeAsync(15000); await settle()
    expect(host.textContent).toContain('Could not check the edge')
    expect((api.getEdge.mock.calls[0][2] as AbortSignal).aborted).toBe(true)
    expect(button(host, 'Check edge').disabled).toBe(false)
    expect(api.createEdge).toHaveBeenCalledTimes(1)
  } finally { app.unmount() }
})
it('ignores an inspection that resolves after the wizard unmounts', async () => {
  const { host, app, inspect } = await mountUncertain()
  let resolve!: (value: unknown) => void
  api.getEdge.mockReturnValue(new Promise(yes => { resolve = yes }))
  button(host, 'Check edge').click(); await settle()
  app.unmount(); resolve({ name: 'edge-a' }); await settle()
  expect(inspect).not.toHaveBeenCalled(); expect(host.textContent).not.toContain('Review edge')
})


it('restores a dispatched attempt after unmount and requires inspection before retry', async () => {
  api.createEdge.mockReturnValue(new Promise(() => {}))
  const host = document.createElement('div'); document.body.append(host)
  const props = { cluster: 'tenant-a', userKey: 'user-a' }
  const first = createApp(Wizard, props); first.mount(host)
  const input = host.querySelector('#edge-name') as HTMLInputElement
  input.value = 'edge-a'; input.dispatchEvent(new Event('input', { bubbles: true }))
  host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
  first.unmount()
  const second = createApp(Wizard, props); second.mount(host); await settle()
  try {
    expect(host.textContent).toContain('may have succeeded')
    expect(button(host, 'Create & continue').disabled).toBe(true)
    expect(api.createEdge).toHaveBeenCalledTimes(1)
    api.getEdge.mockRejectedValue({ reason: 'NotFound' })
    button(host, 'Check edge').click(); await settle()
    button(host, 'Allow another request').click(); await settle()
    expect(button(host, 'Create & continue').disabled).toBe(false)
  } finally { second.unmount() }
})

it('retains recovery after a late successful response to an unmounted wizard', async () => {
  let resolve!: () => void
  api.createEdge.mockReturnValue(new Promise<void>(yes => { resolve = yes }))
  const host = document.createElement('div'); document.body.append(host)
  const props = { cluster: 'tenant-a', userKey: 'user-a' }
  const first = createApp(Wizard, props); first.mount(host)
  const input = host.querySelector('#edge-name') as HTMLInputElement
  input.value = 'edge-a'; input.dispatchEvent(new Event('input', { bubbles: true }))
  host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
  first.unmount(); resolve(); await settle()
  const second = createApp(Wizard, props); second.mount(host); await settle()
  try { expect(host.textContent).toContain('may have succeeded'); expect(button(host, 'Create & continue').disabled).toBe(true) }
  finally { second.unmount() }
})

it('requires explicit action to clear an unreadable recovery record', async () => {
  const key = `railgrid:edges:create-attempt:${JSON.stringify(['tenant-a', 'user-a'])}`
  localStorage.setItem(key, '{invalid')
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a' }); app.mount(host)
  try {
    const input = host.querySelector('#edge-name') as HTMLInputElement
    input.value = 'edge-a'; input.dispatchEvent(new Event('input', { bubbles: true })); await settle()
    expect(host.textContent).toContain('Check Edges for any previous request')
    expect(button(host, 'Create & continue').disabled).toBe(true)
    expect(localStorage.getItem(key)).toBe('{invalid')
    const clear = button(host, 'Clear unreadable record')
    clear.focus(); clear.click(); await settle()
    expect(document.activeElement).toBe(input)
    expect(localStorage.getItem(key)).toBeNull()
    expect(button(host, 'Create & continue').disabled).toBe(false)
  } finally { app.unmount() }
})

it('does not steal focus moved during recovery', async () => {
  const key = `railgrid:edges:create-attempt:${JSON.stringify(['tenant-a', 'user-a'])}`
  localStorage.setItem(key, '{invalid')
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a' }); app.mount(host)
  try {
    const clear = button(host, 'Clear unreadable record')
    const labels = host.querySelector('#edge-labels') as HTMLInputElement
    clear.focus(); clear.click(); labels.focus(); await settle()
    expect(document.activeElement).toBe(labels)
  } finally { app.unmount() }
})

it.each(['before mount', 'after mount'])('blocks creation for an empty recovery record added %s', async when => {
  const key = `railgrid:edges:create-attempt:${JSON.stringify(['tenant-a', 'user-a'])}`
  if (when === 'before mount') localStorage.setItem(key, '')
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a' }); app.mount(host)
  try {
    if (when === 'after mount') localStorage.setItem(key, '')
    const input = host.querySelector('#edge-name') as HTMLInputElement
    input.value = 'edge-a'; input.dispatchEvent(new Event('input', { bubbles: true })); await settle()
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    expect(host.textContent).toContain('Check Edges for any previous request')
    expect(button(host, 'Create & continue').disabled).toBe(true)
    expect(localStorage.getItem(key)).toBe('')
    expect(api.createEdge).not.toHaveBeenCalled()
  } finally { app.unmount() }
})

it('preserves a newer recovery record when an earlier create completes', async () => {
  let resolve!: () => void
  api.createEdge.mockReturnValue(new Promise<void>(yes => { resolve = yes }))
  const key = `railgrid:edges:create-attempt:${JSON.stringify(['tenant-a', 'user-a'])}`
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a' }); app.mount(host)
  try {
    const input = host.querySelector('#edge-name') as HTMLInputElement
    input.value = 'edge-a'; input.dispatchEvent(new Event('input', { bubbles: true }))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    const newer = JSON.stringify({ name: 'edge-b', type: 'server', id: 'newer' })
    const ownKey = localStorage.key(0)!
    expect(ownKey.startsWith(`${key}:`)).toBe(true)
    localStorage.setItem(ownKey, newer)
    resolve(); await settle()
    expect(localStorage.getItem(ownKey)).toBe(newer)
    expect(host.textContent).toContain('was created, but its recovery record could not be cleared')
    expect(button(host, 'Create & continue').disabled).toBe(true)
    expect(api.probeEdge).not.toHaveBeenCalled()
  } finally { app.unmount() }
})

it('recovers concurrent records one at a time, including legacy records', async () => {
  const key = `railgrid:edges:create-attempt:${JSON.stringify(['tenant-a', 'user-a'])}`
  localStorage.setItem(key, JSON.stringify({ name: 'legacy', type: 'server' }))
  localStorage.setItem(`${key}:a`, JSON.stringify({ name: 'edge-a', type: 'kubernetes', id: 'a' }))
  localStorage.setItem(`${key}:b`, JSON.stringify({ name: 'edge-b', type: 'macos', id: 'b' }))
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a' }); app.mount(host)
  api.getEdge.mockRejectedValue({ reason: 'NotFound' })
  try {
    for (const name of ['legacy', 'edge-a', 'edge-b']) {
      expect(host.textContent).toContain(name)
      expect(button(host, 'Create & continue').disabled).toBe(true)
      button(host, 'Check edge').click(); await settle()
      button(host, 'Allow another request').click(); await settle()
    }
    expect(localStorage.length).toBe(0)
    expect(button(host, 'Create & continue').disabled).toBe(false)
  } finally { app.unmount() }
})

it('preserves a concurrent tab record when a dispatched create completes', async () => {
  let resolve!: () => void
  api.createEdge.mockReturnValue(new Promise<void>(yes => { resolve = yes }))
  const key = `railgrid:edges:create-attempt:${JSON.stringify(['tenant-a', 'user-a'])}`
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a' }); app.mount(host)
  try {
    const input = host.querySelector('#edge-name') as HTMLInputElement
    input.value = 'edge-a'; input.dispatchEvent(new Event('input', { bubbles: true }))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    const other = JSON.stringify({ name: 'edge-b', type: 'server', id: 'other' })
    localStorage.setItem(`${key}:other`, other)
    resolve(); await settle()
    expect(localStorage.getItem(`${key}:other`)).toBe(other)
    expect(localStorage.length).toBe(1)
  } finally { app.unmount() }
})

it('retains both attempts when two tabs pass the recovery check concurrently', async () => {
  api.createEdge.mockReturnValue(new Promise(() => {}))
  const props = { cluster: 'tenant-a', userKey: 'user-a' }
  const hosts = [document.createElement('div'), document.createElement('div')]
  hosts.forEach(host => document.body.append(host))
  const apps = hosts.map(host => { const app = createApp(Wizard, props); app.mount(host); return app })
  try {
    hosts.forEach((host, index) => {
      const input = host.querySelector('#edge-name') as HTMLInputElement
      input.value = `edge-${index}`; input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    const set = localStorage.setItem.bind(localStorage)
    let interleaved = false
    const spy = vi.spyOn(localStorage, 'setItem').mockImplementation((key, value) => {
      if (!interleaved) {
        interleaved = true
        hosts[1].querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      }
      set(key, value)
    })
    hosts[0].querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle(); spy.mockRestore()
    expect(api.createEdge).toHaveBeenCalledTimes(2)
    expect(localStorage.length).toBe(2)
    const records = [0, 1].map(i => JSON.parse(localStorage.getItem(localStorage.key(i)!)!).name).sort()
    expect(records).toEqual(['edge-0', 'edge-1'])
  } finally { apps.forEach(app => app.unmount()) }
})

it.each(['enumerate', 'read'] as const)('blocks creation when recovery storage cannot %s, then recovers on retry', async failure => {
  const key = `railgrid:edges:create-attempt:${JSON.stringify(['tenant-a', 'user-a'])}`
  localStorage.setItem(`${key}:pending`, JSON.stringify({ name: 'pending-edge', type: 'server', id: 'pending' }))
  const get = localStorage.getItem.bind(localStorage)
  const enumerate = localStorage.key.bind(localStorage)
  let failing = true
  const getSpy = vi.spyOn(localStorage, 'getItem').mockImplementation(k => {
    if (failing && failure === 'read' && k === `${key}:pending`) throw new Error('denied')
    return get(k)
  })
  const keySpy = vi.spyOn(localStorage, 'key').mockImplementation(i => {
    if (failing && failure === 'enumerate') throw new Error('denied')
    return enumerate(i)
  })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a' }); app.mount(host)
  try {
    const input = host.querySelector('#edge-name') as HTMLInputElement
    input.value = 'new-edge'; input.dispatchEvent(new Event('input', { bubbles: true })); await settle()
    expect(button(host, 'Create & continue').disabled).toBe(true)
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    expect(api.createEdge).not.toHaveBeenCalled()
    failing = false
    button(host, 'Retry recovery check').click(); await settle()
    expect(host.textContent).toContain('pending-edge')
    expect(button(host, 'Create & continue').disabled).toBe(true)
    expect(localStorage.getItem(`${key}:pending`)).not.toBeNull()
  } finally { app.unmount(); getSpy.mockRestore(); keySpy.mockRestore() }
})

it('blocks a submission when storage becomes unreadable after mount', async () => {
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a' }); app.mount(host)
  const spy = vi.spyOn(localStorage, 'key').mockImplementation(() => { throw new Error('denied') })
  try {
    localStorage.setItem('unrelated', 'value')
    const input = host.querySelector('#edge-name') as HTMLInputElement
    input.value = 'new-edge'; input.dispatchEvent(new Event('input', { bubbles: true }))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    expect(api.createEdge).not.toHaveBeenCalled()
    expect(button(host, 'Create & continue').disabled).toBe(true)
    spy.mockRestore()
    button(host, 'Retry recovery check').click(); await settle()
    expect(button(host, 'Create & continue').disabled).toBe(false)
  } finally { spy.mockRestore(); app.unmount() }
})

it('blocks creation when the recovery record cannot be saved and retries after storage is repaired', async () => {
  api.createEdge.mockResolvedValue(undefined)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a' }); app.mount(host)
  const set = vi.spyOn(localStorage, 'setItem').mockImplementation(() => { throw new Error('quota exceeded') })
  try {
    const input = host.querySelector('#edge-name') as HTMLInputElement
    input.value = 'new-edge'; input.dispatchEvent(new Event('input', { bubbles: true })); await settle()
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    expect(api.createEdge).not.toHaveBeenCalled()
    expect(host.textContent).toContain('Could not save edge creation recovery state')
    expect(button(host, 'Create & continue').disabled).toBe(true)
    set.mockRestore()
    button(host, 'Retry recovery check').click(); await settle()
    expect(button(host, 'Create & continue').disabled).toBe(false)
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    expect(api.createEdge).toHaveBeenCalledTimes(1)
    expect(host.textContent).toContain('Install the agent')
  } finally { set.mockRestore(); app.unmount() }
})

it('reports confirmed creation when recovery storage cannot be cleared', async () => {
  api.createEdge.mockResolvedValue(undefined)
  const remove = vi.spyOn(localStorage, 'removeItem').mockImplementation(() => { throw new Error('denied') })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(Wizard, { cluster: 'tenant-a', userKey: 'user-a' }); app.mount(host)
  try {
    const input = host.querySelector('#edge-name') as HTMLInputElement
    input.value = 'edge-a'; input.dispatchEvent(new Event('input', { bubbles: true }))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    expect(host.textContent).toContain('was created, but its recovery record could not be cleared')
    expect(button(host, 'Create & continue').disabled).toBe(true)
    expect(api.probeEdge).not.toHaveBeenCalled()
  } finally { remove.mockRestore(); app.unmount() }
})
