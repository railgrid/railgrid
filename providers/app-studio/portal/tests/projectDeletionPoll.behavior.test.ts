// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it, vi } from 'vitest'
import App from '../src/App.vue'
import { api } from '../src/api'
import { resolveConfirm } from '../src/portalkit/confirm'
const flush = async () => { for (let i = 0; i < 30; i++) { await Promise.resolve(); await nextTick() } }
const project = (name: string, deleting = false) => ({ name, uid: name, displayName: name, description: '', phase: 'Ready', deleting })
it('keeps deletion and collection failures independently visible and recoverable', async () => {
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  const projects = [project('Target'), project('Remaining')]
  const list = vi.spyOn(api, 'listProjects').mockResolvedValueOnce(projects as never)
    .mockRejectedValueOnce(new Error('offline one')).mockResolvedValueOnce(projects as never)
    .mockRejectedValueOnce(new Error('offline two'))
  const deletion = vi.spyOn(api, 'deleteProject').mockRejectedValueOnce(new Error('delete failed')).mockResolvedValueOnce({} as never)
  vi.spyOn(api, 'awaitProjectDeleted').mockResolvedValue(false)
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate: vi.fn() }) })
  const button = (label: string) => [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === label)!
  try {
    app.mount(host); await flush()
    ;(host.querySelector('button[aria-label="Delete project Target"]') as HTMLButtonElement).click()
    await flush(); resolveConfirm(true); await flush()
    expect(host.textContent).toContain('Could not delete Target')
    button('Refresh').click(); await flush()
    expect(host.textContent).toContain('offline one')
    expect(host.textContent).toContain('Could not delete Target')
    expect([...host.querySelectorAll('button')].filter(item => item.textContent?.trim() === 'Retry deletion')).toHaveLength(1)
    button('Retry').click(); await flush()
    expect(host.textContent).not.toContain('offline one')
    expect(host.textContent).toContain('Could not delete Target')
    button('Refresh').click(); await flush()
    expect(host.textContent).toContain('offline two')
    button('Retry deletion').click(); await flush(); resolveConfirm(true); await flush()
    expect(deletion).toHaveBeenCalledTimes(2)
    expect(host.textContent).not.toContain('Could not delete Target')
    expect(host.textContent).toContain('offline two')
    expect(list).toHaveBeenCalledTimes(4)
  } finally { app.unmount(); host.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals() }
})
it('clears a failed deletion retry when refresh finds a same-name replacement', async () => {
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  vi.spyOn(api, 'listProjects').mockResolvedValueOnce([project('Target')] as never)
    .mockResolvedValueOnce([{ ...project('Target'), uid: 'replacement' }] as never)
  const deletion = vi.spyOn(api, 'deleteProject').mockRejectedValue(new Error('delete failed'))
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate: vi.fn() }) })
  try {
    app.mount(host); await flush()
    ;(host.querySelector('button[aria-label="Delete project Target"]') as HTMLButtonElement).click()
    await flush(); resolveConfirm(true); await flush()
    expect(host.textContent).toContain('Could not delete Target')
    ;[...host.querySelectorAll('button')].find(item => item.textContent?.trim() === 'Refresh')!.click(); await flush()
    expect(host.textContent).not.toContain('Could not delete Target')
    expect(host.textContent).not.toContain('Retry deletion')
    expect(host.textContent).toContain('Target')
    expect(deletion).toHaveBeenCalledTimes(1)
  } finally { app.unmount(); host.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals() }
})
it('retains the collection through a failed deletion poll and recovers', async () => {
  vi.useFakeTimers()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  let rejectPoll!: (error: Error) => void
  const list = vi.spyOn(api, 'listProjects').mockResolvedValueOnce([project('Deleting', true), project('Remaining')] as never)
    .mockReturnValueOnce(new Promise((_, reject) => { rejectPoll = reject })).mockResolvedValueOnce([project('Remaining')] as never)
  const navigate = vi.fn()
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate }) })
  try {
    app.mount(host); await flush()
    const search = host.querySelector('input[aria-label="Search projects"]') as HTMLInputElement
    search.value = 'absent'; search.dispatchEvent(new Event('input')); await flush()
    await vi.advanceTimersByTimeAsync(2000); await flush()
    expect(list).toHaveBeenCalledTimes(2)
    expect(search.disabled).toBe(false)
    expect(host.textContent).toContain('No projects match this search.')
    rejectPoll(new Error('Refresh failed')); await flush()
    expect(host.textContent).toContain('Refresh failed')
    expect(host.textContent).toContain('No projects match this search.')
    await vi.advanceTimersByTimeAsync(2000); await flush()
    expect(host.textContent).not.toContain('Refresh failed')
    expect(search.value).toBe('absent')
    expect(navigate).not.toHaveBeenCalled()
  } finally { app.unmount(); host.remove(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() }
})
it('continues to creation when the last terminating project disappears', async () => {
  vi.useFakeTimers()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  vi.spyOn(api, 'listProjects').mockResolvedValueOnce([project('Deleting', true)] as never).mockResolvedValueOnce([] as never)
  const navigate = vi.fn()
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '', token: 'token', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate }) })
  try {
    app.mount(host); await flush()
    await vi.advanceTimersByTimeAsync(2000); await flush()
    expect(navigate).toHaveBeenCalledWith('~new', { replace: true })
  } finally { app.unmount(); host.remove(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() }
})

it('fences a late deletion poll after a workspace change', async () => {
  vi.useFakeTimers()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  let resolvePoll!: (value: never) => void
  vi.spyOn(api, 'listProjects').mockResolvedValueOnce([project('Deleting', true)] as never)
    .mockReturnValueOnce(new Promise(resolve => { resolvePoll = resolve }))
    .mockResolvedValue([project('New workspace')] as never)
  const navigate = vi.fn()
  const state = reactive({ ctx: { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '', token: 'token', fetch: fetchStub } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx: state.ctx, navigate }) })
  try {
    app.mount(host); await flush()
    await vi.advanceTimersByTimeAsync(2000); await flush()
    state.ctx = { ...state.ctx, workspaceUUID: 'other' }; await flush()
    resolvePoll([] as never); await flush()
    expect(host.textContent).toContain('New workspace')
    expect(navigate).not.toHaveBeenCalled()
  } finally { app.unmount(); host.remove(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() }
})

it('does not let an older gallery list overwrite a completed deletion poll', async () => {
  vi.useFakeTimers()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  let resolveGallery!: (value: never) => void
  vi.spyOn(api, 'getProject').mockResolvedValue({ thumbnail: { refreshing: true } } as never)
  const initial = [project('Deleting', true), project('Remaining')]
  const list = vi.spyOn(api, 'listProjects').mockResolvedValueOnce(initial as never).mockResolvedValueOnce(initial as never)
    .mockReturnValueOnce(new Promise(resolve => { resolveGallery = resolve }))
    .mockResolvedValueOnce([project('Fresh')] as never)
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate: vi.fn() }) })
  try {
    app.mount(host); await flush()
    await vi.advanceTimersByTimeAsync(2000); await flush()
    await vi.advanceTimersByTimeAsync(1000); await flush()
    expect(list).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(1000); await flush()
    expect(list).toHaveBeenCalledTimes(4)
    expect(host.textContent).toContain('Fresh')
    resolveGallery(initial as never); await flush()
    expect(host.textContent).toContain('Fresh')
    expect(host.textContent).not.toContain('Remaining')
  } finally { app.unmount(); host.remove(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() }
})

it('defers gallery reads while a deletion poll is in flight', async () => {
  vi.useFakeTimers()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  vi.spyOn(api, 'getProject').mockResolvedValue({ thumbnail: { refreshing: true } } as never)
  let resolvePoll!: (value: never) => void
  const list = vi.spyOn(api, 'listProjects').mockResolvedValueOnce([project('Deleting', true), project('Remaining')] as never)
    .mockReturnValueOnce(new Promise(resolve => { resolvePoll = resolve }))
    .mockResolvedValue([project('Fresh')] as never)
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate: vi.fn() }) })
  try {
    app.mount(host); await flush()
    await vi.advanceTimersByTimeAsync(3000); await flush()
    expect(list).toHaveBeenCalledTimes(2)
    resolvePoll([project('Fresh')] as never); await flush()
    expect(host.textContent).toContain('Fresh')
    await vi.advanceTimersByTimeAsync(3000); await flush()
    expect(host.textContent).toContain('Fresh')
  } finally { app.unmount(); host.remove(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() }
})

it('continues to creation when a gallery refresh observes an empty list', async () => {
  vi.useFakeTimers()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  vi.spyOn(api, 'getProject').mockResolvedValue({ thumbnail: { refreshing: true } } as never)
  vi.spyOn(api, 'listProjects').mockResolvedValueOnce([project('Remaining')] as never).mockResolvedValueOnce([] as never)
  const navigate = vi.fn()
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate }) })
  try {
    app.mount(host); await flush()
    await vi.advanceTimersByTimeAsync(3000); await flush()
    expect(navigate).toHaveBeenCalledWith('~new', { replace: true })
  } finally { app.unmount(); host.remove(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() }
})

it('resumes deletion polling when host transport returns in the same workspace', async () => {
  vi.useFakeTimers()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  const list = vi.spyOn(api, 'listProjects').mockResolvedValue([project('Deleting', true)] as never)
  const state = reactive<{ctx: any}>({ ctx: { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '', fetch: fetchStub } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx: state.ctx, navigate: vi.fn() }) })
  try {
    app.mount(host); await flush()
    state.ctx = { ...state.ctx, fetch: undefined }; await flush()
    await vi.advanceTimersByTimeAsync(2000); await flush()
    expect(list).toHaveBeenCalledTimes(1)
    state.ctx = { ...state.ctx, fetch: fetchStub }; await flush()
    await vi.advanceTimersByTimeAsync(2000); await flush()
    expect(list).toHaveBeenCalledTimes(2)
  } finally { app.unmount(); host.remove(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() }
})

it('refreshes externally changed projects while preserving the search and stale snapshot on failure', async () => {
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  const list = vi.spyOn(api, 'listProjects').mockResolvedValueOnce([project('Old'), project('Keep')] as never)
    .mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce([project('New'), project('Keep')] as never)
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate: vi.fn() }) })
  try {
    app.mount(host); await flush()
    const search = host.querySelector('input[aria-label="Search projects"]') as HTMLInputElement
    search.value = 'Keep'; search.dispatchEvent(new Event('input')); await flush()
    const refresh = () => Array.from(host.querySelectorAll('button')).find(button => button.textContent?.trim() === 'Refresh')!.click()
    refresh(); await flush()
    expect(host.textContent).toContain('Showing the last loaded projects')
    expect(host.textContent).toContain('Keep')
    expect(search.value).toBe('Keep')
    refresh(); await flush()
    expect(host.textContent).not.toContain('offline')
    expect(search.value).toBe('Keep')
    search.value = ''; search.dispatchEvent(new Event('input')); await flush()
    expect(host.textContent).toContain('New')
    expect(host.textContent).not.toContain('Old')
    expect(list).toHaveBeenCalledTimes(3)
  } finally { app.unmount(); host.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals() }
})

it('queues manual refresh behind an active deletion poll', async () => {
  vi.useFakeTimers()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  let resolvePoll!: (value: never) => void
  const list = vi.spyOn(api, 'listProjects').mockResolvedValueOnce([project('Deleting', true), project('Old')] as never)
    .mockReturnValueOnce(new Promise(resolve => { resolvePoll = resolve }))
    .mockResolvedValueOnce([project('Fresh')] as never)
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate: vi.fn() }) })
  try {
    app.mount(host); await flush()
    await vi.advanceTimersByTimeAsync(2000); await flush()
    const refresh = Array.from(host.querySelectorAll('button')).find(button => button.textContent?.trim() === 'Refresh')!
    refresh.focus(); refresh.click(); await flush()
    expect(document.activeElement).toBe(refresh)
    expect(refresh.getAttribute('aria-disabled')).toBe('true')
    expect(host.textContent).toContain('Refreshing…')
    expect(list).toHaveBeenCalledTimes(2)
    resolvePoll([project('Old')] as never); await flush()
    expect(list).toHaveBeenCalledTimes(3)
    expect(host.textContent).toContain('Fresh')
    expect(host.textContent).not.toContain('Old')
  } finally { app.unmount(); host.remove(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() }
})

it('queues manual refresh behind an active gallery read', async () => {
  vi.useFakeTimers()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  vi.spyOn(api, 'getProject').mockResolvedValue({ thumbnail: { refreshing: true } } as never)
  let resolveGallery!: (value: never) => void
  const list = vi.spyOn(api, 'listProjects').mockResolvedValueOnce([project('Old')] as never)
    .mockReturnValueOnce(new Promise(resolve => { resolveGallery = resolve }))
    .mockResolvedValueOnce([project('Fresh')] as never)
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate: vi.fn() }) })
  try {
    app.mount(host); await flush()
    await vi.advanceTimersByTimeAsync(3000); await flush()
    Array.from(host.querySelectorAll('button')).find(button => button.textContent?.trim() === 'Refresh')!.click(); await flush()
    expect(list).toHaveBeenCalledTimes(2)
    resolveGallery([project('Old')] as never); await flush()
    expect(list).toHaveBeenCalledTimes(3)
    expect(host.textContent).toContain('Fresh')
    expect(host.textContent).not.toContain('Old')
  } finally { app.unmount(); host.remove(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() }
})

it('does not wait for slow gallery thumbnail hydration before refreshing the list', async () => {
  vi.useFakeTimers()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  let rejectThumbnail!: (reason: Error) => void
  vi.spyOn(api, 'getProject').mockResolvedValueOnce({ thumbnail: { refreshing: true } } as never)
    .mockReturnValueOnce(new Promise((_, reject) => { rejectThumbnail = reject }))
    .mockResolvedValue({} as never)
  const list = vi.spyOn(api, 'listProjects').mockResolvedValueOnce([project('Old')] as never)
    .mockResolvedValueOnce([project('Old')] as never).mockResolvedValueOnce([project('Fresh')] as never)
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate: vi.fn() }) })
  try {
    app.mount(host); await flush()
    await vi.advanceTimersByTimeAsync(3000); await flush()
    expect(list).toHaveBeenCalledTimes(2)
    Array.from(host.querySelectorAll('button')).find(button => button.textContent?.trim() === 'Refresh')!.click(); await flush()
    expect(list).toHaveBeenCalledTimes(3)
    expect(host.textContent).toContain('Fresh')
    rejectThumbnail(new Error('thumbnail failed')); await flush()
    expect(host.textContent).toContain('Fresh')
    expect(host.textContent).not.toContain('thumbnail failed')
  } finally { app.unmount(); host.remove(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() }
})
