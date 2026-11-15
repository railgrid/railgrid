// @vitest-environment happy-dom
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

const api = vi.hoisted(() => ({ listEdges: vi.fn(), deployMarketplaceApp: vi.fn(), createWorkload: vi.fn(), getWorkload: vi.fn() }))
vi.mock('./api', () => api)
import WorkloadCreate from './WorkloadCreate.vue'

const settle = async () => { await Promise.resolve(); await nextTick(); await Promise.resolve(); await nextTick() }
afterEach(() => { document.body.innerHTML = ''; vi.resetAllMocks() })

it.each(['env', '=prod', 'env=prod,env=dev', 'Bad_Prefix/name=x', 'env=-bad', 'env=a=b', 'env=x,'])(
  'preserves and rejects an invalid selector %s', async selector => {
    api.listEdges.mockResolvedValue([{ name: 'edge-a', type: 'kubernetes', labels: {} }])
    const host = document.createElement('div'); document.body.append(host)
    const app = createApp(WorkloadCreate, { mode: 'manual' }); app.mount(host)
    try {
      await settle()
      const input = host.querySelector<HTMLInputElement>('[aria-labelledby="workload-selector-label"]')!
      input.value = selector; input.dispatchEvent(new Event('input')); await nextTick()
      expect(input.value).toBe(selector)
      expect(input.getAttribute('aria-invalid')).toBe('true')
      expect(host.querySelector('#workload-selector-error')?.textContent).toContain('unique Kubernetes labels')
      expect((host.querySelector('button[type="submit"]') as HTMLButtonElement).disabled).toBe(true)
      expect(api.createWorkload).not.toHaveBeenCalled()
    } finally { app.unmount() }
  },
)

it('previews and submits a selector with an explicit empty label value', async () => {
  api.listEdges.mockResolvedValue([{ name: 'edge-a', type: 'kubernetes', labels: { tier: '' } }])
  api.createWorkload.mockResolvedValue(undefined)
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'manual' }); app.mount(host)
  try {
    await settle()
    const selector = host.querySelector<HTMLInputElement>('[aria-labelledby="workload-selector-label"]')!
    selector.value = 'tier='; selector.dispatchEvent(new Event('input'))
    const name = host.querySelector<HTMLInputElement>('input[placeholder="my-workload"]') ?? host.querySelector<HTMLInputElement>('input')!
    name.value = 'example'; name.dispatchEvent(new Event('input'))
    await nextTick()
    expect(host.textContent).toContain('1 current match')
    expect(selector.getAttribute('aria-invalid')).toBeNull()
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(api.createWorkload).toHaveBeenCalledWith(expect.objectContaining({ name: 'example', selector: { tier: '' } }))
  } finally { app.unmount() }
})

it('locks submitted fields and requires inspection after an uncertain outcome', async () => {
  api.listEdges.mockResolvedValue([{ name: 'edge-a', type: 'kubernetes', labels: {} }])
  let rejectCreate!: (reason: unknown) => void
  api.createWorkload.mockReturnValue(new Promise((_, reject) => { rejectCreate = reject }))
  api.getWorkload.mockResolvedValue({ name: 'example' })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp(WorkloadCreate, { mode: 'manual' }); app.mount(host)
  try {
    await settle()
    const name = host.querySelector<HTMLInputElement>('input[placeholder="nginx-demo"]')!
    name.value = 'example'; name.dispatchEvent(new Event('input')); await nextTick()
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await nextTick()
    expect(api.createWorkload).toHaveBeenCalledWith(expect.objectContaining({ name: 'example' }))
    expect([...host.querySelectorAll<HTMLInputElement | HTMLSelectElement>('form input, form select')].every(field => field.disabled)).toBe(true)
    rejectCreate({ reason: 'CreateOutcomeUnknown' }); await settle()
    expect(name.disabled).toBe(false)
    expect((host.querySelector('button[type="submit"]') as HTMLButtonElement).disabled).toBe(true)
    expect(host.textContent).toContain('may have succeeded')
    const check = [...host.querySelectorAll('button')].find(button => button.textContent?.includes('Check workload'))!
    check.click(); await settle()
    expect(api.getWorkload).toHaveBeenCalledWith('example', expect.any(AbortSignal))
    expect(host.textContent).toContain('does not establish which request created it')
    const allow = [...host.querySelectorAll('button')].find(button => button.textContent?.includes('I reviewed Workloads; clear recovery'))!
    allow.click(); await nextTick()
    expect((host.querySelector('button[type="submit"]') as HTMLButtonElement).disabled).toBe(false)
  } finally { app.unmount() }
})
