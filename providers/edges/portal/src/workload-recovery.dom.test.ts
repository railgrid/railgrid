// @vitest-environment happy-dom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

const api = vi.hoisted(() => ({ listEdges: vi.fn(), deployMarketplaceApp: vi.fn(), createWorkload: vi.fn(), getWorkload: vi.fn(), getService: vi.fn() }))
vi.mock('./api', () => api)
import WorkloadCreate from './WorkloadCreate.vue'

const edge = { name: 'edge-a', type: 'kubernetes', labels: {} }
const settle = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
const deferred = () => {
  let resolve!: (value: typeof edge[]) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<typeof edge[]>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
beforeEach(() => {
  const values = new Map<string, string>()
  vi.stubGlobal('localStorage', { get length() { return values.size }, key: (index: number) => [...values.keys()][index] ?? null, getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key, value), removeItem: (key: string) => values.delete(key), clear: () => values.clear() })
})
afterEach(() => { document.body.innerHTML = ''; localStorage.clear(); vi.clearAllMocks() })

it('recovers from a stalled edge read and ignores its late result', async () => {
  vi.useFakeTimers()
  const pending = deferred()
  api.listEdges.mockReset().mockReturnValueOnce(pending.promise).mockResolvedValueOnce([edge])
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'marketplace', appType: 'grafana', cluster: 'one', userKey: 'user' }); app.mount(host)
  try {
    await settle()
    const signal = api.listEdges.mock.calls[0]![0] as AbortSignal
    await vi.advanceTimersByTimeAsync(15000); await settle()
    expect(signal.aborted).toBe(true)
    expect(host.textContent).toContain('The edge check timed out. Try again.')
    ;[...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Retry')!.click(); await settle()
    expect((host.querySelector('select') as HTMLSelectElement).value).toBe('edge-a')
    pending.resolve([{ ...edge, name: 'late-edge' }]); await settle()
    expect((host.querySelector('select') as HTMLSelectElement).value).toBe('edge-a')
    expect(host.textContent).not.toContain('late-edge')
  } finally { app.unmount(); vi.useRealTimers() }
})

it.each(['success', 'empty', 'failure', 'moved'] as const)('preserves appropriate focus during edge retry: %s', async outcome => {
  api.listEdges.mockReset().mockResolvedValueOnce([edge]).mockResolvedValueOnce([])
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'marketplace', appType: 'grafana', cluster: 'test-cluster', userKey: 'test-user' })
  app.mount(host)
  try {
    await settle()
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    const retry = [...host.querySelectorAll('button')].find(button => button.textContent?.includes('Check again'))!
    expect(retry).toBeTruthy()
    const pending = deferred()
    api.listEdges.mockReturnValueOnce(pending.promise)
    retry.focus()
    retry.click()
    await settle()
    expect(document.activeElement).toBe(retry)
    expect(retry.getAttribute('aria-disabled')).toBe('true')
    retry.click()
    expect(api.listEdges).toHaveBeenCalledTimes(3)
    const back = host.querySelector('button.k-back-action') as HTMLButtonElement
    if (outcome === 'moved') back.focus()
    if (outcome === 'failure') pending.reject(new Error('Cannot read edges'))
    else pending.resolve(outcome === 'empty' ? [] : [{ ...edge, name: 'edge-b' }])
    await settle()
    if (outcome === 'failure') {
      expect(document.activeElement).toBe(retry)
      expect(host.textContent).toContain('Cannot read edges')
      expect(retry.getAttribute('aria-disabled')).toBeNull()
    } else if (outcome === 'empty') {
      expect(document.activeElement).toBe(retry)
      expect(host.querySelector('[role="status"][aria-live="polite"]')?.textContent).toBe('No Kubernetes edges found. Connect an edge, then check again.')
    } else if (outcome === 'moved') {
      expect(document.activeElement).toBe(back)
    } else {
      expect(document.activeElement).toBe(host.querySelector('select'))
      expect((host.querySelector('select') as HTMLSelectElement).value).toBe('')
    }
  } finally { app.unmount() }
})


it.each(['MarketplaceWorkloadUnknown', 'MarketplaceServiceUnknown', 'MarketplaceServiceRejected'])('holds the submitted marketplace target for recovery: %s', async reason => {
  api.listEdges.mockReset().mockResolvedValue([edge])
  api.deployMarketplaceApp.mockReset().mockRejectedValue({ reason, message: 'Review this deployment.' })
  api.getWorkload.mockReset().mockResolvedValue({ name: 'grafana' })
  api.getService.mockReset().mockRejectedValue({ reason: 'NotFound' })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'marketplace', appType: 'grafana', cluster: 'test-cluster', userKey: 'test-user' })
  app.mount(host)
  try {
    await settle()
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(host.textContent).toContain('Submitted target:')
    expect(host.textContent).toContain('edge edge-a')
    const deploy = [...host.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Deploy')!
    expect(deploy.disabled).toBe(true)
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    expect(api.deployMarketplaceApp).toHaveBeenCalledTimes(1)
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Check workload and service'))!.click()
    await settle()
    await settle()
    expect(api.getWorkload).toHaveBeenCalledWith('grafana', expect.any(AbortSignal))
    expect(api.getService).toHaveBeenCalledWith('grafana', expect.any(AbortSignal))
    expect(host.textContent).toContain('A Workload with this name is present.')
    expect(host.textContent).toContain('No Service with this name was found.')
    expect(host.textContent).toContain('Names alone do not establish')
    expect(deploy.disabled).toBe(true)
  } finally { app.unmount() }
})

it('ignores an inspection response after the form leaves', async () => {
  api.listEdges.mockReset().mockResolvedValue([edge])
  api.deployMarketplaceApp.mockReset().mockRejectedValue({ reason: 'MarketplaceWorkloadUnknown', message: 'Unknown' })
  let resolve!: (value: unknown) => void
  api.getWorkload.mockReset().mockReturnValue(new Promise(yes => { resolve = yes }))
  api.getService.mockReset()
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'marketplace', appType: 'grafana', cluster: 'test-cluster', userKey: 'test-user' })
  app.mount(host)
  await settle()
  host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await settle()
  ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Check workload and service'))!.click()
  app.unmount()
  resolve({ name: 'grafana' })
  await settle()
  expect(api.getService).not.toHaveBeenCalled()
})


it('restores an interrupted attempt only in its originating workspace', async () => {
  api.listEdges.mockReset().mockResolvedValue([edge])
  api.deployMarketplaceApp.mockReset().mockReturnValue(new Promise(() => {}))
  const host = document.createElement('div'); document.body.append(host)
  const props = { mode: 'marketplace' as const, appType: 'grafana', cluster: 'one', userKey: 'user' }
  const first = createApp(WorkloadCreate, props); first.mount(host); await settle()
  host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
  expect(api.deployMarketplaceApp).toHaveBeenCalledTimes(1)
  first.unmount()
  const other = createApp(WorkloadCreate, { ...props, cluster: 'two' }); other.mount(host); await settle()
  expect(host.textContent).not.toContain('previous marketplace deployment')
  other.unmount()
  const restored = createApp(WorkloadCreate, props); restored.mount(host); await settle()
  expect(host.textContent).toContain('previous marketplace deployment')
  expect(host.textContent).toContain('edge edge-a')
  expect([...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Deploy')?.disabled).toBe(true)
  restored.unmount()
})

it('aborts a timed-out deployment and retains recovery', async () => {
  vi.useFakeTimers()
  api.listEdges.mockReset().mockResolvedValue([edge])
  api.deployMarketplaceApp.mockReset().mockReturnValue(new Promise(() => {}))
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'marketplace', appType: 'grafana', cluster: 'one', userKey: 'user' }); app.mount(host)
  try {
    await settle()
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    const signal = api.deployMarketplaceApp.mock.calls[0]![1] as AbortSignal
    await vi.advanceTimersByTimeAsync(15000); await settle()
    expect(signal.aborted).toBe(true)
    expect(host.textContent).toContain('outcome is unknown')
    expect(host.textContent).toContain('Submitted target:')
  } finally { app.unmount(); vi.useRealTimers() }
})

it('does not allow clearing recovery when a resource check fails', async () => {
  api.listEdges.mockReset().mockResolvedValue([edge])
  api.deployMarketplaceApp.mockReset().mockRejectedValue({ reason: 'MarketplaceServiceUnknown', message: 'Unknown' })
  api.getWorkload.mockReset().mockResolvedValue({ name: 'grafana' })
  api.getService.mockReset().mockRejectedValue({ reason: 'HTTPError', message: 'Forbidden' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'marketplace', appType: 'grafana', cluster: 'one', userKey: 'user' }); app.mount(host)
  try {
    await settle(); host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('Check workload and service'))!.click(); await settle()
    expect(host.textContent).toContain('Could not check the Service: Forbidden')
    expect(host.textContent).not.toContain('I reviewed Workloads and Services; clear recovery')
  } finally { app.unmount() }
})

it('permits explicitly clearing an observed malformed record', async () => {
  api.listEdges.mockReset().mockResolvedValue([edge])
  const key = `railgrid:edges:marketplace-attempt:${JSON.stringify(['one', 'user'])}`
  localStorage.setItem(key, '{')
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'marketplace', appType: 'grafana', cluster: 'one', userKey: 'user' }); app.mount(host)
  try {
    await settle()
    ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('clear unreadable recovery'))!.click(); await settle()
    expect(localStorage.getItem(key)).toBeNull()
  } finally { app.unmount() }
})

it('bounds a stalled preflight without creating resources', async () => {
  vi.useFakeTimers()
  api.listEdges.mockReset().mockResolvedValueOnce([edge]).mockReturnValueOnce(new Promise(() => {}))
  api.deployMarketplaceApp.mockReset()
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'marketplace', appType: 'grafana', cluster: 'one', userKey: 'user' }); app.mount(host)
  try {
    await settle(); host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    const signal = api.listEdges.mock.calls[1]![0] as AbortSignal
    await vi.advanceTimersByTimeAsync(15000); await settle()
    expect(signal.aborted).toBe(true)
    expect(api.deployMarketplaceApp).not.toHaveBeenCalled()
  } finally { app.unmount(); vi.useRealTimers() }
})

it('does not clear a recovery record replaced by another tab', async () => {
  api.listEdges.mockReset().mockResolvedValue([edge])
  const key = `railgrid:edges:marketplace-attempt:${JSON.stringify(['one', 'user'])}`
  localStorage.setItem(key, '{')
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'marketplace', appType: 'grafana', cluster: 'one', userKey: 'user' }); app.mount(host)
  try {
    await settle(); localStorage.setItem(key, 'new record')
    ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('clear unreadable recovery'))!.click(); await settle()
    expect(localStorage.getItem(key)).toBe('new record')
    expect(host.textContent).toContain('Could not clear deployment recovery state')
  } finally { app.unmount() }
})

it.each(['initial', 'after mount'])('treats an empty stored marker as unreadable: %s', async timing => {
  api.listEdges.mockReset().mockResolvedValue([edge]); api.deployMarketplaceApp.mockReset()
  const key = `railgrid:edges:marketplace-attempt:${JSON.stringify(['one', 'user'])}`
  if (timing === 'initial') localStorage.setItem(key, '')
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'marketplace', appType: 'grafana', cluster: 'one', userKey: 'user' }); app.mount(host)
  try {
    await settle()
    if (timing === 'after mount') {
      localStorage.setItem(key, '')
      host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    }
    expect(host.textContent).toContain('Recovery state is unreadable')
    expect(api.deployMarketplaceApp).not.toHaveBeenCalled()
    ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('clear unreadable recovery'))!.click(); await settle()
    expect(localStorage.getItem(key)).toBeNull()
  } finally { app.unmount() }
})

it.each(['missing identity', 'storage unavailable'])('offers page-local recovery when %s', async scenario => {
  api.listEdges.mockReset().mockResolvedValue([edge])
  api.deployMarketplaceApp.mockReset().mockRejectedValue({ reason: 'MarketplaceWorkloadUnknown', message: 'Unknown' })
  if (scenario === 'storage unavailable') vi.spyOn(localStorage, 'getItem').mockImplementation(() => { throw new Error('unavailable') })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'marketplace', appType: 'grafana', cluster: 'one', userKey: scenario === 'missing identity' ? undefined : 'user' }); app.mount(host)
  try {
    await settle()
    if (scenario === 'storage unavailable') {
      expect(host.textContent).toContain('Could not read workload recovery state')
      expect((host.querySelector('button[type=submit]') as HTMLButtonElement).disabled).toBe(true)
      return
    }
    expect(host.textContent).toContain('Recovery is available only on this page')
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
    expect(api.deployMarketplaceApp).toHaveBeenCalledTimes(1)
    expect(host.textContent).toContain('This recovery state will be lost when you leave this page')
    expect([...host.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Deploy')?.disabled).toBe(true)
  } finally { app.unmount(); vi.restoreAllMocks() }
})

it('recovers every marketplace record including legacy state without clearing other scopes', async () => {
  api.listEdges.mockReset().mockResolvedValue([edge])
  api.getWorkload.mockResolvedValue(null)
  api.getService.mockRejectedValue({ reason: 'NotFound' })
  const props = { mode: 'marketplace' as const, appType: 'grafana', cluster: 'one', userKey: 'user' }
  const base = `railgrid:edges:marketplace-attempt:${JSON.stringify(['one', 'user'])}`
  const other = `railgrid:edges:marketplace-attempt:${JSON.stringify(['two', 'user'])}:other`
  const record = (id: string, name: string) => JSON.stringify({ id, target: { name, edge: 'edge-a', namespace: 'default', type: 'grafana', port: 80 } })
  localStorage.setItem(base, record('legacy', 'legacy'))
  localStorage.setItem(`${base}:one`, record('one', 'first'))
  localStorage.setItem(`${base}:two`, record('two', 'second'))
  localStorage.setItem(other, record('other', 'other'))
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(WorkloadCreate, props); app.mount(host); await settle()
  for (const name of ['legacy', 'first', 'second']) {
    expect(host.textContent).toContain(`Workload and Service “${name}”`)
    expect(host.textContent).not.toContain('No Service with this name was found.')
    expect([...host.querySelectorAll('button')].some(b => b.textContent?.includes('clear recovery'))).toBe(false)
    ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('Check workload and service'))!.click(); await settle()
    ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('clear recovery'))!.click(); await settle()
  }
  expect(localStorage.length).toBe(1)
  expect(localStorage.getItem(other)).not.toBeNull()
  app.unmount()
})

it('does not clear a marketplace record changed after inspection', async () => {
  api.listEdges.mockReset().mockResolvedValue([edge])
  api.getWorkload.mockResolvedValue(null)
  api.getService.mockRejectedValue({ reason: 'NotFound' })
  const base = `railgrid:edges:marketplace-attempt:${JSON.stringify(['one', 'user'])}`
  const key = `${base}:record`
  const record = (name: string) => JSON.stringify({ id: 'record', target: { name, edge: 'edge-a', namespace: 'default', type: 'grafana', port: 80 } })
  localStorage.setItem(key, record('first'))
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'marketplace', appType: 'grafana', cluster: 'one', userKey: 'user' }); app.mount(host); await settle()
  ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('Check workload and service'))!.click(); await settle()
  localStorage.setItem(key, record('second'))
  ;[...host.querySelectorAll('button')].find(b => b.textContent?.includes('clear recovery'))!.click(); await settle()
  expect(localStorage.getItem(key)).toBe(record('second'))
  expect(host.textContent).toContain('Could not clear deployment recovery state')
  app.unmount()
})
