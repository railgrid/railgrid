import { afterEach, describe, expect, it, vi } from 'vitest'
import Automation from '../views/Automation.vue'
import { Resources } from '../resources'
import { makeStore, stubApi } from './helpers'
import { mountVue, settleVue, unmountVueApps } from './vue-helper'

afterEach(() => { unmountVueApps(); localStorage.clear(); vi.useRealTimers() })
const authority = () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null })
const prefix = `railgrid:agents:automation-attempt:${JSON.stringify(['org', 'ws', 'alice', 'scout', 'schedule'])}:`
const click = (element: Element, label: string) => [...element.querySelectorAll('button')].find(button => button.textContent?.includes(label))!.click()
describe('automation creation recovery', () => {
  it('keeps a lost response across navigation and requires inspection and acknowledgment', async () => {
    const createSchedule = vi.fn(async () => { throw { reason: 'AutomationCreateUnknown' } })
    const listSchedules = vi.fn(async () => [{ metadata: { name: 'daily' }, spec: { agentRef: 'scout', type: 'cron' } }] as never)
    const api = stubApi({ createSchedule, listSchedules, contextAuthority: authority })
    const store = makeStore(api)
    let view = await mountVue(Automation, { store, api, kind: 'schedule', agent: 'scout', createRoute: true, createModal: true })
    const input = view.element.querySelector<HTMLInputElement>('input[name="name"]')!
    input.value = 'daily'; input.dispatchEvent(new Event('input'))
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue(5)
    expect(createSchedule).toHaveBeenCalledTimes(1)
    expect(view.element.textContent).toContain('may have been created')
    expect(view.element.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(true)
    view.unmount()
    view = await mountVue(Automation, { store, api, kind: 'schedule', agent: 'scout', createRoute: true, createModal: true })
    expect(view.element.textContent).toContain('daily')
    click(view.element, 'Check Schedules'); await settleVue(5)
    expect(view.element.textContent).toContain('name alone does not establish')
    expect(view.element.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(true)
    click(view.element, 'Allow another request'); await settleVue()
    expect(view.element.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(false)
    expect(localStorage.length).toBe(0)
  })
  it.each(['org', 'workspace', 'user'] as const)('does not dispatch without %s identity', async missing => {
    const createSchedule = vi.fn()
    const api = stubApi({
      createSchedule,
      tenant: () => ({ orgUUID: missing === 'org' ? null : 'org', workspaceUUID: missing === 'workspace' ? null : 'ws' }),
      contextAuthority: () => ({ ...authority(), userKey: missing === 'user' ? null : 'alice' }),
    })
    const store = makeStore(api)
    const view = await mountVue(Automation, { store, api, kind: 'schedule', agent: 'scout', createRoute: true })
    const input = view.element.querySelector<HTMLInputElement>('input[name="name"]')!
    input.value = 'daily'; input.dispatchEvent(new Event('input'))
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue(5)
    expect(createSchedule).not.toHaveBeenCalled()
    expect(view.element.textContent).toContain('identity is unavailable')
    expect(localStorage.length).toBe(0)
  })
  it('isolates records by agent and preserves a replaced attempt', async () => {
    localStorage.setItem(prefix + 'a', JSON.stringify({ name: 'daily', id: 'a' }))
    const api = stubApi({ contextAuthority: authority, listSchedules: async () => [] })
    const store = makeStore(api)
    const other = await mountVue(Automation, { store, api, kind: 'schedule', agent: 'other', createRoute: true })
    expect(other.element.textContent).not.toContain('may have been created')
    const view = await mountVue(Automation, { store, api, kind: 'schedule', agent: 'scout', createRoute: true })
    click(view.element, 'Check Schedules'); await settleVue(5)
    localStorage.setItem(prefix + 'a', JSON.stringify({ name: 'replacement', id: 'a' }))
    click(view.element, 'Allow another request'); await settleVue()
    expect(view.element.textContent).toContain('replacement')
    expect(localStorage.getItem(prefix + 'a')).toContain('replacement')
  })
  it('preserves independently submitted attempts when writes interleave', async () => {
    const methods = Object.hasOwn(localStorage, 'setItem') ? localStorage : Object.getPrototypeOf(localStorage) as Storage
    const original = methods.setItem
    const spy = vi.spyOn(methods, 'setItem').mockImplementation(function (this: Storage, key: string, value: string) {
      if (key.startsWith(prefix)) original.call(this, prefix + 'concurrent', JSON.stringify({ name: 'other', id: 'concurrent' }))
      original.call(this, key, value)
    })
    const api = stubApi({ contextAuthority: authority, createSchedule: async () => { throw { reason: 'AutomationCreateUnknown' } } })
    const store = makeStore(api)
    const view = await mountVue(Automation, { store, api, kind: 'schedule', agent: 'scout', createRoute: true })
    const input = view.element.querySelector<HTMLInputElement>('input[name="name"]')!
    input.value = 'daily'; input.dispatchEvent(new Event('input'))
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue(5)
    spy.mockRestore()
    const keys = Array.from({ length: localStorage.length }, (_, i) => localStorage.key(i)!).filter(key => key.startsWith(prefix))
    expect(keys.map(key => JSON.parse(localStorage.getItem(key)!).name).sort()).toEqual(['daily', 'other'])
  })
  it('keeps a timeout uncertain after a late successful response', async () => {
    vi.useFakeTimers()
    let resolve!: (response: Response) => void
    const resources = new Resources()
    resources.setContext({ basePath: '/ui/providers/agents', tenant: 'tenant-a', fetch: () => new Promise<Response>(done => { resolve = done }) } as never)
    const request = resources.createSchedule({ name: 'daily', agentRef: 'scout', type: 'cron', schedule: '* * * * *' })
    const result = expect(request).rejects.toMatchObject({ reason: 'AutomationCreateUnknown' })
    await vi.advanceTimersByTimeAsync(15001)
    await result
    resolve(new Response(JSON.stringify({ metadata: { name: 'daily' }, spec: { agentRef: 'scout', type: 'cron' } }), { status: 201 }))
    await Promise.resolve()
    await expect(request).rejects.toMatchObject({ reason: 'AutomationCreateUnknown' })
  })
  it('keeps a pending request in the original workspace after a context switch', async () => {
    let resolve!: (response: Response) => void
    const resources = new Resources()
    const context = { basePath: '/ui/providers/agents', tenant: 'tenant-a', fetch: () => new Promise<Response>(done => { resolve = done }) }
    resources.setContext(context as never)
    const result = resources.createTrigger({ name: 'events', agentRef: 'scout', webhookRef: 'hook' })
    await Promise.resolve(); await Promise.resolve()
    resources.setContext({ ...context, tenant: 'tenant-b' } as never)
    resolve(new Response(JSON.stringify({ metadata: { name: 'events' }, spec: { agentRef: 'scout', webhookRef: 'hook' } }), { status: 201 }))
    await expect(result).rejects.toMatchObject({ reason: 'AutomationCreateUnknown' })
  })
  it.each(['schedule', 'trigger'] as const)('classifies rejected, lost and timed out %s requests', async kind => {
    const resources = new Resources()
    const request = () => kind === 'schedule' ? resources.createSchedule({ name: 'daily', agentRef: 'scout', type: 'cron', schedule: '* * * * *' }) : resources.createTrigger({ name: 'daily', agentRef: 'scout', webhookRef: 'events' })
    const context = (fetch: (...args: any[]) => any) => ({ basePath: '/ui/providers/agents', tenant: 'tenant-a', orgUUID: 'org', workspaceUUID: 'ws', fetch }) as never
    resources.setContext(context(async () => new Response(JSON.stringify({ kind: 'Status', reason: 'Forbidden', code: 403 }), { status: 403 })))
    await expect(request()).rejects.toMatchObject({ status: 403 })
    resources.setContext(context(async () => new Response(JSON.stringify({ kind: 'Status', reason: 'AlreadyExists', code: 409 }), { status: 409 })))
    await expect(request()).rejects.toMatchObject({ status: 409 })
    resources.setContext(context(async () => { throw new TypeError('lost') }))
    await expect(request()).rejects.toMatchObject({ reason: 'AutomationCreateUnknown' })
    vi.useFakeTimers()
    resources.setContext(context(() => new Promise(() => {})))
    const result = expect(request()).rejects.toMatchObject({ reason: 'AutomationCreateUnknown' })
    await vi.advanceTimersByTimeAsync(15001)
    await result
  })
})
