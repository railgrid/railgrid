// @vitest-environment happy-dom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
const api = vi.hoisted(() => ({ listEdges: vi.fn(), createWorkload: vi.fn(), getWorkload: vi.fn(), getService: vi.fn(), deployMarketplaceApp: vi.fn() }))
vi.mock('./api', () => api)
import WorkloadCreate from './WorkloadCreate.vue'
const settle = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
const props = { mode: 'manual' as const, cluster: 'cluster-a', userKey: 'user-a' }
const key = `railgrid:edges:workload-create-attempt:${JSON.stringify([props.cluster, props.userKey])}`
let host: HTMLDivElement
beforeEach(() => { const values = new Map<string, string>(); vi.stubGlobal('localStorage', { get length() { return values.size }, key: (index: number) => [...values.keys()][index] ?? null, getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key, value), removeItem: (key: string) => values.delete(key), clear: () => values.clear() }); host = document.createElement('div'); document.body.append(host); api.listEdges.mockResolvedValue([{ name: 'edge', type: 'kubernetes', labels: {} }]) })
afterEach(() => { document.body.innerHTML = ''; localStorage.clear(); vi.resetAllMocks(); vi.useRealTimers() })
const currentRecord = () => { const k = Array.from({ length: localStorage.length }, (_, i) => localStorage.key(i)).find(k => k?.startsWith(`${key}:`)); return k ? localStorage.getItem(k) : null }
async function submit() {
  const input = host.querySelector<HTMLInputElement>('input[placeholder="nginx-demo"]')!
  input.value = 'example'; input.dispatchEvent(new Event('input')); await nextTick()
  host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
}
it('restores an in-flight attempt only for the original scope and stores no draft secrets', async () => {
  let resolve!: () => void
  api.createWorkload.mockReturnValue(new Promise<void>(r => { resolve = r }))
  const first = createApp(WorkloadCreate, props); first.mount(host); await settle(); await submit()
  expect(JSON.parse(currentRecord()!)).toEqual({ id: expect.any(String), name: 'example' })
  first.unmount()
  const other = createApp(WorkloadCreate, { ...props, userKey: 'user-b' }); other.mount(host); await settle()
  expect(host.textContent).not.toContain('request for “example”'); other.unmount()
  const restored = createApp(WorkloadCreate, props); restored.mount(host); await settle()
  expect(host.textContent).toContain('request for “example”')
  expect((host.querySelector('button[type="submit"]') as HTMLButtonElement).disabled).toBe(true)
  resolve(); await settle()
  expect(currentRecord()).not.toBeNull()
  restored.unmount()
})
it('retains timed out attempts until explicit inspection and clear', async () => {
  vi.useFakeTimers()
  api.createWorkload.mockReturnValue(new Promise(() => {})); api.getWorkload.mockResolvedValue(null)
  const app = createApp(WorkloadCreate, props); app.mount(host); await settle(); await submit()
  await vi.advanceTimersByTimeAsync(15000); await settle()
  expect(host.textContent).toContain('may have succeeded')
  expect(currentRecord()).not.toBeNull()
  ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('Check workload'))!.click(); await settle()
  expect(host.textContent).toContain('original request may still be processing')
  ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('I reviewed Workloads; clear recovery'))!.click(); await settle()
  expect(currentRecord()).toBeNull(); app.unmount()
})
it('warns when storage is unavailable', async () => {
  vi.spyOn(localStorage, 'getItem').mockImplementation(() => { throw new Error('unavailable') })
  const app = createApp(WorkloadCreate, props); app.mount(host); await settle()
  expect(host.textContent).toContain('Could not read workload recovery state')
  app.unmount(); vi.restoreAllMocks()
})

it('blocks both creation modes until each existing recovery record is inspected and cleared', async () => {
  const marketplaceKey = `railgrid:edges:marketplace-attempt:${JSON.stringify([props.cluster, props.userKey])}`
  localStorage.setItem(key, JSON.stringify({ id: 'manual-id', name: 'manual-one' }))
  localStorage.setItem(marketplaceKey, JSON.stringify({ id: 'market-id', target: { name: 'chart-one', edge: 'edge', namespace: 'default', type: 'grafana', port: 3000 } }))
  api.getWorkload.mockResolvedValue(null); api.getService.mockRejectedValue({ reason: 'NotFound' })
  const market = createApp(WorkloadCreate, { ...props, mode: 'marketplace', appType: 'grafana' }); market.mount(host); await settle()
  expect(host.textContent).toContain('request for “manual-one”')
  expect(host.textContent).toContain('previous marketplace deployment')
  expect((host.querySelector('button[type="submit"]') as HTMLButtonElement).disabled).toBe(true)
  ;[...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Check workload')!.click(); await settle()
  ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('I reviewed Workloads; clear recovery'))!.click(); await settle()
  expect(localStorage.getItem(key)).toBeNull()
  expect(localStorage.getItem(marketplaceKey)).not.toBeNull()
  expect((host.querySelector('button[type="submit"]') as HTMLButtonElement).disabled).toBe(true)
  market.unmount()
  const manual = createApp(WorkloadCreate, props); manual.mount(host); await settle()
  expect(host.textContent).toContain('previous marketplace deployment')
  const name = host.querySelector<HTMLInputElement>('input[placeholder="nginx-demo"]')!
  name.value = 'new-one'; name.dispatchEvent(new Event('input')); await settle()
  expect((host.querySelector('button[type="submit"]') as HTMLButtonElement).disabled).toBe(true)
  ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('Check workload and service'))!.click(); await settle()
  ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('clear recovery'))!.click(); await settle()
  expect(localStorage.getItem(marketplaceKey)).toBeNull()
  expect((host.querySelector('button[type="submit"]') as HTMLButtonElement).disabled).toBe(false)
  manual.unmount()
})

it('times out a manual inspection without clearing the recovery record', async () => {
  vi.useFakeTimers()
  localStorage.setItem(key, JSON.stringify({ id: 'manual-id', name: 'manual-one' }))
  api.getWorkload.mockReturnValue(new Promise(() => {}))
  const app = createApp(WorkloadCreate, props); app.mount(host); await settle()
  const check = [...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Check workload')!
  check.click(); await settle(); await vi.advanceTimersByTimeAsync(15000); await settle()
  expect(host.textContent).toContain('workload check timed out')
  expect(check.disabled).toBe(false)
  expect(localStorage.getItem(key)).not.toBeNull()
  expect(host.textContent).not.toContain('I reviewed Workloads; clear recovery')
  app.unmount()
})

it('rechecks the other flow before submitting a form that was already open', async () => {
  const marketplaceKey = `railgrid:edges:marketplace-attempt:${JSON.stringify([props.cluster, props.userKey])}`
  const app = createApp(WorkloadCreate, props); app.mount(host); await settle()
  localStorage.setItem(marketplaceKey, JSON.stringify({ id: 'market-id', target: { name: 'chart-one', edge: 'edge', namespace: 'default', type: 'grafana', port: 3000 } }))
  await submit()
  expect(api.createWorkload).not.toHaveBeenCalled()
  expect(host.textContent).toContain('previous marketplace deployment')
  app.unmount()
})

it('retains overlapping tab attempts separately and reviews each record', async () => {
  api.createWorkload.mockReturnValue(new Promise(() => {}))
  api.getWorkload.mockResolvedValue(null)
  const secondHost = document.createElement('div'); document.body.append(secondHost)
  const first = createApp(WorkloadCreate, props); first.mount(host)
  const second = createApp(WorkloadCreate, props); second.mount(secondHost)
  await settle()
  for (const [container, name] of [[host, 'first'], [secondHost, 'second']] as const) {
    const input = container.querySelector<HTMLInputElement>('input[placeholder="nginx-demo"]')!
    input.value = name; input.dispatchEvent(new Event('input'))
  }
  await settle()
  const original = localStorage.setItem.bind(localStorage)
  let interleaved = false
  const spy = vi.spyOn(localStorage, 'setItem').mockImplementation((storageKey, value) => {
    if (!interleaved && storageKey.startsWith(`${key}:`)) {
      interleaved = true
      secondHost.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    }
    original(storageKey, value)
  })
  host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await settle(); spy.mockRestore()
  expect(api.createWorkload).toHaveBeenCalledTimes(2)
  expect(localStorage.length).toBe(2)
  first.unmount(); second.unmount()
  const restored = createApp(WorkloadCreate, props); restored.mount(host); await settle()
  const initial = host.textContent!
  expect(initial.includes('request for “first”') || initial.includes('request for “second”')).toBe(true)
  for (let remaining = 1; remaining >= 0; remaining--) {
    ;[...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Check workload')!.click(); await settle()
    ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('I reviewed Workloads; clear recovery'))!.click(); await settle()
    expect(localStorage.length).toBe(remaining)
    if (remaining) expect(host.textContent).toContain(initial.includes('request for “first”') ? 'request for “second”' : 'request for “first”')
  }
  restored.unmount()
})

it('blocks submission if recovery enumeration fails', async () => {
  vi.spyOn(localStorage, 'key').mockImplementation(() => { throw new Error('unavailable') })
  localStorage.setItem('unrelated', 'value')
  const app = createApp(WorkloadCreate, props); app.mount(host); await settle()
  expect(host.textContent).toContain('Could not read workload recovery state')
  expect((host.querySelector('button[type="submit"]') as HTMLButtonElement).disabled).toBe(true)
  expect(api.createWorkload).not.toHaveBeenCalled()
  app.unmount(); vi.restoreAllMocks()
})
