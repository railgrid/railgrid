import { describe, expect, it, vi } from 'vitest'
import AgentCreate from '../views/AgentCreate.vue'
import { Resources } from '../resources'
import { confirmState, resolveConfirm } from '../portalkit/confirm'
import { makeStore, stubApi } from './helpers'
import { mountVue, settleVue } from './vue-helper'

describe('agent creation outcome', () => {
  it('reviews legacy and concurrent records independently without crossing identities', async () => {
    const base = `railgrid:agents:create-attempt:${JSON.stringify(['org', 'ws', 'alice'])}`
    const other = `railgrid:agents:create-attempt:${JSON.stringify(['org', 'ws', 'bob'])}:other`
    localStorage.setItem(base, '{invalid')
    localStorage.setItem(`${base}:a`, JSON.stringify({ name: 'first-agent', template: true, id: 'a' }))
    localStorage.setItem(`${base}:b`, JSON.stringify({ name: 'second-agent', template: false, id: 'b' }))
    localStorage.setItem(other, JSON.stringify({ name: 'other-agent', template: false, id: 'other' }))
    const api = stubApi({ listAgents: async () => [], contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }) })
    const store = makeStore(api)
    for (const resource of ['agents', 'credentials', 'connections', 'edges'] as const) { store[resource].loaded = true; store[resource].hasSnapshot = true }
    const view = await mountVue(AgentCreate, { store, api })
    const click = (label: string) => [...view.element.querySelectorAll('button')].find(button => button.textContent?.includes(label))!.click()
    expect(view.element.textContent).toContain('recovery record could not be read')
    click('Clear unreadable record')
    await settleVue()
    expect(view.element.textContent).toContain('first-agent” may have been created')
    expect(view.element.textContent).toContain('Template schedules may have been submitted')
    click('Check agent')
    await settleVue(5)
    click('Allow another request')
    await settleVue()
    expect(view.element.textContent).toContain('second-agent” may have been created')
    expect(view.element.querySelector<HTMLButtonElement>('button[type=submit]')!.disabled).toBe(true)
    click('Check agent')
    await settleVue(5)
    click('Allow another request')
    await settleVue()
    expect(localStorage.getItem(other)).not.toBeNull()
    expect(localStorage.getItem(`${base}:a`)).toBeNull()
    expect(localStorage.getItem(`${base}:b`)).toBeNull()
    view.unmount()
    localStorage.removeItem(other)
  })

  it('preserves a concurrent attempt written just before this attempt', async () => {
    const base = `railgrid:agents:create-attempt:${JSON.stringify(['org', 'ws', 'alice'])}`
    const methods = Object.hasOwn(localStorage, 'setItem') ? localStorage : Object.getPrototypeOf(localStorage) as Storage
    const original = methods.setItem
    const set = vi.spyOn(methods, 'setItem').mockImplementation(function (this: Storage, key: string, value: string) {
      if (key.startsWith(`${base}:`)) original.call(this, `${base}:concurrent`, JSON.stringify({ name: 'other-agent', template: true, id: 'concurrent' }))
      original.call(this, key, value)
    })
    const api = stubApi({ createAgent: async () => { throw { reason: 'AgentCreateUnknown' } }, contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }) })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'test', ready: true }]
    for (const resource of ['agents', 'credentials', 'connections', 'edges'] as const) { store[resource].loaded = true; store[resource].hasSnapshot = true }
    const view = await mountVue(AgentCreate, { store, api })
    await settleVue(5)
    const name = view.element.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'draft-agent'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue(5)
    set.mockRestore()
    const keys = Array.from({ length: localStorage.length }, (_, index) => localStorage.key(index)).filter(key => key?.startsWith(`${base}:`))
    expect(keys).toHaveLength(2)
    expect(keys.map(key => JSON.parse(localStorage.getItem(key!)!).name).sort()).toEqual(['draft-agent', 'other-agent'])
    view.unmount()
    for (const key of keys) localStorage.removeItem(key!)
  })

  it('retries an unavailable storage read and preserves the newly visible record', async () => {
    const key = `railgrid:agents:create-attempt:${JSON.stringify(['org', 'ws', 'alice'])}`
    const replacement = JSON.stringify({ name: 'other-agent', template: false, id: 'other' })
    localStorage.setItem(key, replacement)
    const storageMethods = Object.hasOwn(localStorage, 'getItem') ? localStorage : Object.getPrototypeOf(localStorage) as Storage
    const originalGet = storageMethods.getItem
    const get = vi.spyOn(storageMethods, 'getItem').mockImplementation(function (this: Storage, item: string) {
      if (item === key) throw new Error('unavailable')
      return originalGet.call(this, item)
    })
    const api = stubApi({ contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }) })
    const store = makeStore(api)
    for (const resource of ['agents', 'credentials', 'connections', 'edges'] as const) { store[resource].loaded = true; store[resource].hasSnapshot = true }
    const view = await mountVue(AgentCreate, { store, api })
    await settleVue()
    expect(view.element.textContent).toContain('Browser storage is unavailable')
    get.mockRestore()
    const retry = [...view.element.querySelectorAll('button')].find(button => button.textContent?.includes('Retry reading recovery state'))!
    retry.focus()
    retry.click()
    await settleVue()
    expect(view.element.textContent).toContain('other-agent” may have been created')
    expect(document.activeElement).toBe(view.element.querySelector('#agent-create-name'))
    expect(localStorage.getItem(key)).toBe(replacement)
    view.unmount()
    localStorage.removeItem(key)
  })

  it.each([false, true])('restores retry focus for an empty record without stealing moved focus (moved: %s)', async moved => {
    const key = `railgrid:agents:create-attempt:${JSON.stringify(['org', 'ws', 'alice'])}`
    localStorage.removeItem(key)
    const storageMethods = Object.hasOwn(localStorage, 'getItem') ? localStorage : Object.getPrototypeOf(localStorage) as Storage
    const originalGet = storageMethods.getItem
    const get = vi.spyOn(storageMethods, 'getItem').mockImplementation(function (this: Storage, item: string) {
      if (item === key) throw new Error('unavailable')
      return originalGet.call(this, item)
    })
    const api = stubApi({ contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }) })
    const store = makeStore(api)
    for (const resource of ['agents', 'credentials', 'connections', 'edges'] as const) { store[resource].loaded = true; store[resource].hasSnapshot = true }
    const view = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 1 })
    await settleVue()
    get.mockRestore()
    const retry = [...view.element.querySelectorAll('button')].find(button => button.textContent?.includes('Retry reading recovery state'))!
    const other = document.createElement('button')
    document.body.append(other)
    retry.focus()
    retry.click()
    if (moved) other.focus()
    await settleVue()
    expect(document.activeElement).toBe(moved ? other : view.element.querySelector('#agent-create-name'))
    other.remove()
    view.unmount()
  })

  it('shows an attempt inserted after the form mounted', async () => {
    const key = `railgrid:agents:create-attempt:${JSON.stringify(['org', 'ws', 'alice'])}`
    const createAgent = vi.fn()
    const api = stubApi({ createAgent, contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }) })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'test', ready: true }]
    for (const resource of ['agents', 'credentials', 'connections', 'edges'] as const) { store[resource].loaded = true; store[resource].hasSnapshot = true }
    const view = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 1 })
    await settleVue(5)
    const name = view.element.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'draft-agent'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    const replacement = JSON.stringify({ name: 'other-agent', template: false, id: 'other' })
    localStorage.setItem(key, replacement)
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue(5)
    expect(view.element.textContent).toContain('other-agent” may have been created')
    expect(localStorage.getItem(key)).toBe(replacement)
    expect(createAgent).not.toHaveBeenCalled()
    view.unmount()
    localStorage.removeItem(key)
  })
  it.each(['', '{invalid', 'null'])('blocks unreadable recovery state until explicitly cleared: %s', async raw => {
    const key = `railgrid:agents:create-attempt:${JSON.stringify(['org', 'ws', 'alice'])}`
    localStorage.setItem(key, raw)
    const createAgent = vi.fn()
    const api = stubApi({ createAgent, contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }) })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'test', ready: true }]
    for (const key of ['agents', 'credentials', 'connections', 'edges'] as const) { store[key].loaded = true; store[key].hasSnapshot = true }
    const view = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 1 })
    await settleVue()
    expect(view.element.textContent).toContain('recovery record could not be read')
    expect(view.element.querySelector<HTMLButtonElement>('button[type=submit]')!.disabled).toBe(true)
    expect(localStorage.getItem(key)).toBe(raw)
    expect(createAgent).not.toHaveBeenCalled()
    const clear = [...view.element.querySelectorAll('button')].find(button => button.textContent?.includes('Clear unreadable record'))!
    clear.focus()
    clear.click()
    await settleVue()
    expect(localStorage.getItem(key)).toBeNull()
    expect(document.activeElement).toBe(view.element.querySelector('#agent-create-name'))
    view.unmount()
  })

  it('preserves a replacement recovery record when clearing an unreadable record', async () => {
    const key = `railgrid:agents:create-attempt:${JSON.stringify(['org', 'ws', 'alice'])}`
    localStorage.setItem(key, '{invalid')
    const api = stubApi({ contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }) })
    const store = makeStore(api)
    for (const resource of ['agents', 'credentials', 'connections', 'edges'] as const) { store[resource].loaded = true; store[resource].hasSnapshot = true }
    const view = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 1 })
    await settleVue()
    const replacement = JSON.stringify({ name: 'new-agent', template: false, id: 'new-attempt' })
    localStorage.setItem(key, replacement)
    ;[...view.element.querySelectorAll('button')].find(button => button.textContent?.includes('Clear unreadable record'))!.click()
    await settleVue()
    expect(localStorage.getItem(key)).toBe(replacement)
    expect(view.element.textContent).toContain('new-agent” may have been created')
    expect(view.element.querySelector<HTMLButtonElement>('button[type=submit]')!.disabled).toBe(true)
    view.unmount()
    localStorage.removeItem(key)
  })

  it.each([['removed', false], ['replaced', false], ['replaced', true]])('refreshes a %s record after inspection without losing the draft (focus moved: %s)', async (change, moved) => {
    const key = `railgrid:agents:create-attempt:${JSON.stringify(['org', 'ws', 'alice'])}:one`
    localStorage.setItem(key, JSON.stringify({ name: 'old-agent', template: false, id: 'one' }))
    const api = stubApi({ listAgents: async () => [], contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }) })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'test', ready: true }]
    for (const resource of ['agents', 'credentials', 'connections', 'edges'] as const) { store[resource].loaded = true; store[resource].hasSnapshot = true }
    const view = await mountVue(AgentCreate, { store, api })
    await settleVue(5)
    const name = view.element.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'my-draft'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    const click = (label: string) => [...view.element.querySelectorAll('button')].find(button => button.textContent?.includes(label))!.click()
    click('Check agent')
    await settleVue(5)
    if (change === 'removed') localStorage.removeItem(key)
    else localStorage.setItem(key, JSON.stringify({ name: 'new-agent', template: true, id: 'new' }))
    const allow = [...view.element.querySelectorAll('button')].find(button => button.textContent?.includes('Allow another request'))!
    const other = document.createElement('button')
    document.body.append(other)
    allow.focus()
    allow.click()
    if (moved) other.focus()
    await settleVue()
    expect(document.activeElement).toBe(moved ? other : name)
    other.remove()
    expect(name.value).toBe('my-draft')
    expect(view.element.textContent).not.toContain('Allow another request')
    if (change === 'replaced') {
      expect(view.element.textContent).toContain('new-agent” may have been created')
      expect(localStorage.getItem(key)).not.toBeNull()
    } else expect(view.element.textContent).not.toContain('may have been created')
    view.unmount()
    localStorage.removeItem(key)
  })

  it('refreshes an unreadable record removed in another tab', async () => {
    const key = `railgrid:agents:create-attempt:${JSON.stringify(['org', 'ws', 'alice'])}`
    localStorage.setItem(key, '{invalid')
    const api = stubApi({ contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }) })
    const store = makeStore(api)
    for (const resource of ['agents', 'credentials', 'connections', 'edges'] as const) { store[resource].loaded = true; store[resource].hasSnapshot = true }
    const view = await mountVue(AgentCreate, { store, api })
    localStorage.removeItem(key)
    ;[...view.element.querySelectorAll('button')].find(button => button.textContent?.includes('Clear unreadable record'))!.click()
    await settleVue()
    expect(view.element.textContent).not.toContain('recovery record could not be read')
    view.unmount()
  })

  it('retains an in-flight attempt across remounts for its user and workspace', async () => {
    let finish!: (value: unknown) => void
    const createAgent = vi.fn(() => new Promise(resolve => { finish = resolve }))
    const authority = { usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }
    const api = stubApi({ createAgent, contextAuthority: () => authority })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'test', ready: true }]
    for (const key of ['agents', 'credentials', 'connections', 'edges'] as const) {
      store[key].loaded = true
      store[key].hasSnapshot = true
    }
    const view = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 1 })
    await settleVue(5)
    const name = view.element.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'draft-agent'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue(5)
    expect(createAgent).toHaveBeenCalledTimes(1)
    view.unmount()
    const otherApi = stubApi({ contextAuthority: () => ({ ...authority, userKey: 'bob' }) })
    const other = await mountVue(AgentCreate, { store: makeStore(otherApi), api: otherApi })
    expect(other.element.textContent).not.toContain('may have been created')
    other.unmount()
    const restored = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 2 })
    expect(restored.element.textContent).toContain('draft-agent” may have been created')
    expect(restored.element.querySelector<HTMLButtonElement>('button[type=submit]')!.disabled).toBe(true)
    finish({ metadata: { name: 'draft-agent' }, spec: {} })
    await settleVue(5)
    restored.unmount()
    const after = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 3 })
    expect(after.element.textContent).not.toContain('may have been created')
    after.unmount()
  })

  it('requires a successful inspection and acknowledgement before retrying the retained draft', async () => {
    const createAgent = vi.fn(async () => { throw { reason: 'AgentCreateUnknown' } })
    const listAgents = vi.fn().mockResolvedValue([])
    const api = stubApi({ createAgent, listAgents, contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }) })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'test', ready: true }]
    for (const key of ['agents', 'credentials', 'connections', 'edges'] as const) {
      store[key].loaded = true
      store[key].hasSnapshot = true
    }
    const view = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 1 })
    await settleVue(5)
    const name = view.element.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'draft-agent'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue(5)
    listAgents.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce([])
    const click = (label: string) => [...view.element.querySelectorAll('button')].find(button => button.textContent?.includes(label))!.click()
    click('Check agent')
    await settleVue(5)
    expect(view.element.textContent).toContain('Could not check Agents')
    expect(view.element.textContent).not.toContain('Allow another request')
    click('Check agent')
    await settleVue(5)
    expect(view.element.textContent).toContain('original request may still be processing')
    expect(view.element.querySelector<HTMLButtonElement>('button[type=submit]')!.disabled).toBe(true)
    const storageMethods = Object.hasOwn(localStorage, 'removeItem') ? localStorage : Object.getPrototypeOf(localStorage) as Storage
    const remove = vi.spyOn(storageMethods, 'removeItem').mockImplementationOnce(() => { throw new Error('storage unavailable') })
    click('Allow another request')
    await settleVue()
    expect(view.element.textContent).toContain('Could not clear the recovery record')
    expect(view.element.querySelector<HTMLButtonElement>('button[type=submit]')!.disabled).toBe(true)
    remove.mockRestore()
    const allow = [...view.element.querySelectorAll('button')].find(button => button.textContent?.includes('Allow another request'))!
    allow.focus()
    click('Allow another request')
    await settleVue()
    expect(name.value).toBe('draft-agent')
    expect(document.activeElement).toBe(name)
    expect(view.element.querySelector<HTMLButtonElement>('button[type=submit]')!.disabled).toBe(false)
    expect(createAgent).toHaveBeenCalledTimes(1)
    view.unmount()
  })

  it('keeps a confirmed create visible when its recovery record cannot be removed', async () => {
    const api = stubApi({
      createAgent: async () => ({ metadata: { name: 'draft-agent' }, spec: {} }),
      contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }),
    })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'test', ready: true }]
    for (const key of ['agents', 'credentials', 'connections', 'edges'] as const) {
      store[key].loaded = true
      store[key].hasSnapshot = true
    }
    const view = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 1 })
    await settleVue(5)
    const name = view.element.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'draft-agent'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    const storageMethods = Object.hasOwn(localStorage, 'removeItem') ? localStorage : Object.getPrototypeOf(localStorage) as Storage
    const remove = vi.spyOn(storageMethods, 'removeItem').mockImplementationOnce(() => { throw new Error('storage unavailable') })
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue(5)
    expect(view.element.textContent).toContain('was created, but its recovery record could not be cleared')
    expect(view.events['create-success']).toBeUndefined()
    expect(view.element.querySelector<HTMLButtonElement>('button[type=submit]')!.disabled).toBe(true)
    remove.mockRestore()
    view.unmount()
    localStorage.clear()
  })

  it('exposes a stale recovery record after a rejected request', async () => {
    const api = stubApi({
      createAgent: async () => { throw { reason: 'Forbidden' } },
      contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }),
    })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'test', ready: true }]
    for (const key of ['agents', 'credentials', 'connections', 'edges'] as const) {
      store[key].loaded = true
      store[key].hasSnapshot = true
    }
    const view = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 1 })
    await settleVue(5)
    const name = view.element.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'draft-agent'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    const storageMethods = Object.hasOwn(localStorage, 'removeItem') ? localStorage : Object.getPrototypeOf(localStorage) as Storage
    const remove = vi.spyOn(storageMethods, 'removeItem').mockImplementationOnce(() => { throw new Error('storage unavailable') })
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue(5)
    expect(view.element.textContent).toContain('was rejected, but its recovery record could not be cleared')
    expect(view.element.querySelector<HTMLButtonElement>('button[type=submit]')!.disabled).toBe(true)
    remove.mockRestore()
    const allow = [...view.element.querySelectorAll('button')].find(button => button.textContent?.includes('Allow another request'))!
    allow.focus()
    allow.click()
    await settleVue()
    expect(document.activeElement).toBe(name)
    expect(view.element.querySelector<HTMLButtonElement>('button[type=submit]')!.disabled).toBe(false)
    view.unmount()
  })

  it('does not adopt a same-name agent or apply an inspection after the session changes', async () => {
    const listAgents = vi.fn().mockResolvedValue([])
    const api = stubApi({ createAgent: async () => { throw { reason: 'AgentCreateUnknown' } }, listAgents })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'test', ready: true }]
    for (const key of ['agents', 'credentials', 'connections', 'edges'] as const) {
      store[key].loaded = true
      store[key].hasSnapshot = true
    }
    const view = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 1 })
    await settleVue(5)
    const name = view.element.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'draft-agent'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue(5)
    listAgents.mockResolvedValueOnce([{ metadata: { name: 'draft-agent' } }])
    const check = () => [...view.element.querySelectorAll('button')].find(button => button.textContent?.includes('Check agent'))!.click()
    check()
    await settleVue(5)
    expect(view.element.textContent).toContain('name alone does not establish which request created it')
    expect(view.events['create-success']).toBeUndefined()
    let resolveRead!: (value: unknown[]) => void
    listAgents.mockReturnValueOnce(new Promise(resolve => { resolveRead = resolve }))
    check()
    await settleVue()
    const signal = listAgents.mock.calls.at(-1)?.[0] as AbortSignal
    expect(signal.aborted).toBe(false)
    await view.setProps({ createSession: 2 })
    expect(signal.aborted).toBe(true)
    resolveRead([])
    await settleVue(5)
    expect(view.element.textContent).not.toContain('No agent named')
    expect(view.element.textContent).not.toContain('Allow another request')
    view.unmount()
  })

  it('does not adopt a successful create from an earlier session', async () => {
    let resolveCreate!: (value: any) => void
    const createAgent = vi.fn(() => new Promise<any>(resolve => { resolveCreate = resolve }))
    const api = stubApi({ createAgent })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'test', ready: true }]
    for (const key of ['agents', 'credentials', 'connections', 'edges'] as const) {
      store[key].loaded = true
      store[key].hasSnapshot = true
    }
    const view = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 1 })
    await settleVue(5)
    const name = view.element.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'draft-agent'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue()
    await view.setProps({ createSession: 2 })
    resolveCreate({ metadata: { name: 'draft-agent' }, spec: {} })
    await settleVue(5)
    expect(view.events['create-success']).toBeUndefined()
    view.unmount()
  })

  it('retains recovery when the workspace changes during a template schedule write', async () => {
    let finishSchedule!: (response: Response) => void
    const transport = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).includes('/schedules')) return new Promise<Response>(resolve => { finishSchedule = resolve })
      return new Response(JSON.stringify({ metadata: { name: 'draft-agent' }, spec: {} }), { status: 201 })
    })
    const context = { basePath: '/ui/providers/agents', tenant: 'tenant-a', orgUUID: 'org', workspaceUUID: 'ws', fetch: transport }
    const resources = new Resources()
    resources.setContext(context as never)
    const catalog = [{ apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'AgentTemplate', metadata: { name: 'starter' }, spec: {
      displayName: 'Starter', category: 'general', suggestedName: 'starter', agent: {},
      schedules: [{ name: 'daily', spec: { schedule: '0 9 * * *' } }],
    } }]
    const api = stubApi({ createAgentFromTemplate: resources.createAgentFromTemplate, agentTemplates: async () => catalog, contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }) })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'test', ready: true }]
    for (const key of ['agents', 'credentials', 'connections', 'edges'] as const) {
      store[key].loaded = true
      store[key].hasSnapshot = true
    }
    const view = await mountVue(AgentCreate, { store, api, initialTemplate: 'starter', authorityEpoch: 1, createSession: 1 })
    await settleVue(5)
    const name = view.element.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'draft-agent'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue(5)
    resolveConfirm(true)
    await vi.waitFor(() => expect(finishSchedule).toBeDefined())
    view.unmount()
    const restored = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 2 })
    expect(restored.element.textContent).toContain('schedules may have been submitted')
    resources.setContext({ ...context, tenant: 'tenant-b' } as never)
    finishSchedule(new Response(JSON.stringify({ metadata: { name: 'daily' }, spec: {} }), { status: 201 }))
    await settleVue(5)
    restored.unmount()
    const after = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 3 })
    expect(after.element.textContent).toContain('schedules may have been submitted')
    after.unmount()
  })

  it.each(['unknown', 'rejected'] as const)('retains only an unknown schedule outcome after the create view unmounts: %s', async outcome => {
    let finish!: (value: any) => void
    const createAgentFromTemplate = vi.fn(() => new Promise(resolve => { finish = resolve }))
    const catalog = [{ apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'AgentTemplate', metadata: { name: 'starter' }, spec: {
      displayName: 'Starter', category: 'general', suggestedName: 'starter', agent: {},
      schedules: [{ name: 'daily', spec: { schedule: '0 9 * * *' } }],
    } }]
    const api = stubApi({ createAgentFromTemplate, agentTemplates: async () => catalog, contextAuthority: () => ({ usable: true, tenantKey: 'org/ws', userKey: 'alice', token: null }) })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'test', ready: true }]
    for (const key of ['agents', 'credentials', 'connections', 'edges'] as const) {
      store[key].loaded = true
      store[key].hasSnapshot = true
    }
    const view = await mountVue(AgentCreate, { store, api, initialTemplate: 'starter', authorityEpoch: 1, createSession: 1 })
    await settleVue(5)
    const name = view.element.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'draft-agent'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue(5)
    resolveConfirm(true)
    await vi.waitFor(() => expect(createAgentFromTemplate).toHaveBeenCalledTimes(1))
    view.unmount()
    finish({ agent: { metadata: { name: 'draft-agent' }, spec: {} }, createdSchedules: [], warnings: [{ schedule: 'daily', outcome }] })
    await settleVue(5)
    const restored = await mountVue(AgentCreate, { store, api, authorityEpoch: 1, createSession: 2 })
    if (outcome === 'unknown') expect(restored.element.textContent).toContain('schedules may have been submitted')
    else expect(restored.element.textContent).not.toContain('may have been created')
    restored.unmount()
  })

  it('bounds an inspection even if the transport ignores abort', async () => {
    vi.useFakeTimers()
    try {
      const resources = new Resources()
      resources.setContext({ basePath: '/ui/providers/agents', tenant: 'tenant-a', orgUUID: 'org', workspaceUUID: 'ws', fetch: () => new Promise(() => {}) } as never)
      const read = resources.listAgents(new AbortController().signal)
      const result = expect(read).rejects.toThrow()
      await vi.advanceTimersByTimeAsync(15_001)
      await result
    } finally {
      vi.useRealTimers()
    }
  })

  it.each([
    { status: 403, template: false },
    { status: 409, template: false },
    { status: 500, template: false },
    { status: 0, template: false },
    { status: 0, template: true },
  ])('handles status $status with template $template', async ({ status, template }) => {
    const transport = vi.fn(async () => {
      if (!status) throw new TypeError('response lost')
      return new Response(JSON.stringify({ kind: 'Status', status: 'Failure', reason: 'Forbidden', message: 'forbidden', code: status }), { status })
    })
    const resources = new Resources()
    resources.setContext({ basePath: '/ui/providers/agents', tenant: 'tenant-a', orgUUID: 'org', workspaceUUID: 'ws', fetch: transport } as never)
    const catalog = [{ apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'AgentTemplate', metadata: { name: 'starter' }, spec: {
      displayName: 'Starter', category: 'general', suggestedName: 'starter', agent: {},
      schedules: [{ name: 'daily', spec: { schedule: '0 9 * * *' } }],
    } }]
    const api = stubApi({ createAgent: resources.createAgent, createAgentFromTemplate: resources.createAgentFromTemplate, agentTemplates: async () => catalog })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'main', model: 'test', ready: true }]
    for (const key of ['agents', 'credentials', 'connections', 'edges'] as const) {
      store[key].loaded = true
      store[key].hasSnapshot = true
    }
    const view = await mountVue(AgentCreate, { store, api, initialTemplate: template ? 'starter' : '', authorityEpoch: 1, createSession: 1 })
    await settleVue(5)
    const name = view.element.querySelector<HTMLInputElement>('#agent-create-name')!
    name.value = 'draft-agent'
    name.dispatchEvent(new InputEvent('input', { bubbles: true }))
    view.element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settleVue(5)
    if (template) {
      expect(confirmState.open).toBe(true)
      resolveConfirm(true)
      await settleVue(5)
    }
    await vi.waitFor(() => expect(view.element.querySelector('form')?.getAttribute('aria-busy')).toBe('false'))
    expect(transport).toHaveBeenCalledTimes(1)
    expect(view.events['create-success']).toBeUndefined()
    expect(name.value).toBe('draft-agent')
    const submit = view.element.querySelector<HTMLButtonElement>('button[type=submit]')!
    if (status === 403 || status === 409) {
      expect(view.element.textContent).not.toContain('may have been created')
      expect(submit.disabled).toBe(false)
    } else {
      expect(view.element.textContent).toContain('may have been created')
      expect(submit.disabled).toBe(true)
      expect(view.element.querySelector('a[target="_blank"]')?.getAttribute('href')).toContain('#/agents')
      if (template) expect(view.element.textContent).toContain('schedules may have been submitted')
      view.element.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }))
      await settleVue()
      expect(transport).toHaveBeenCalledTimes(1)
    }
    view.unmount()
  })
})
