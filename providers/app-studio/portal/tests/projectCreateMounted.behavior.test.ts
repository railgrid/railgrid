// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it, vi } from 'vitest'
import App from '../src/App.vue'

import { markProjectCreateUncertain, readProjectCreateUncertainty } from '../src/projectCreateUncertainty'

it('submits a compatible project review from its own draft when the landing draft is empty', async () => {
  const { api } = await import('../src/api')
  sessionStorage.clear()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200 }))
  vi.stubGlobal('fetch', fetchStub)
  vi.spyOn(api, 'getLLMSettings').mockResolvedValue({ models: [{ id: 'model', name: 'Model', model: 'gpt-6-sol', provider: 'openai-compatible', baseURL: 'https://example.test', configured: true }] } as never)
  vi.spyOn(api, 'getProjectCreateReadiness').mockResolvedValue({ gitConnection: { ready: false } } as never)
  const template = { name: 'auth', displayName: 'Auth', browserPreview: true, railgridAuthSupported: true, railgridAuthReason: 'supported' }
  const templates = vi.spyOn(api, 'listDevelopmentTemplates').mockResolvedValue([template] as never)
  vi.spyOn(api, 'planProject').mockResolvedValue({ displayName: 'Protected app', template: 'auth', availableTemplates: [template] } as never)
  const create = vi.spyOn(api, 'createProjectStream')
  const state = reactive({ ctx: { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '~new', token: 'token', fetch: fetchStub } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx: state.ctx, navigate: vi.fn() }) })
  const flush = async () => { for (let i = 0; i < 30; i++) { await Promise.resolve(); await nextTick() } }
  try {
    const root = app.mount(host) as any; await flush()
    const studio = root.$.subTree.component.setupState
    studio.authCompatibleCreation = true
    studio.authCompatibleCreationPrompt = 'Build a protected app'
    studio.wizardOpen = true
    await flush()
    const input = host.querySelector('#new-project-prompt') as HTMLTextAreaElement
    input.value = ' '; input.dispatchEvent(new Event('input')); await flush()
    const review = [...host.querySelectorAll('button')].find(button => button.textContent?.includes('Review compatible project'))!
    expect(review.disabled).toBe(true)
    input.value = 'Build a protected app'; input.dispatchEvent(new Event('input')); await flush()
    review.click(); await flush()
    const confirm = [...host.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Create project without Git')!
    expect(confirm.disabled).toBe(false)
    templates.mockClear().mockResolvedValueOnce([{ ...template, browserPreview: false }] as never)
    confirm.click(); await flush()
    expect(templates).toHaveBeenCalledTimes(1)
    expect(create).not.toHaveBeenCalled()
    expect(host.textContent).toContain('This template is no longer available for Railgrid Auth')
    expect(host.textContent).toContain('Build a protected app')
    templates.mockClear().mockReturnValue(new Promise(() => {}))
    confirm.click(); await flush()
    expect(templates).toHaveBeenCalledTimes(1)
    expect(host.textContent).toContain('Rechecking template support')
  } finally {
    app.unmount(); host.remove(); sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals()
  }
})

it('keeps the creation warning scoped across navigation and requires acknowledgement', async () => {
  const identity = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: 'alice' }
  sessionStorage.clear()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  expect(markProjectCreateUncertain(identity, 'attempt')).toBe(true)
  const state = reactive({ ctx: { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '~new', token: 'token', fetch: fetchStub } })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx: state.ctx, navigate: vi.fn() }) })
  const flush = async () => { for (let i = 0; i < 20; i++) { await Promise.resolve(); await nextTick() } }
  try {
    app.mount(host)
    await flush()
    expect(host.textContent).toContain('Project creation may have completed')
    state.ctx = { ...state.ctx, subPath: '' }; await flush()
    state.ctx = { ...state.ctx, subPath: '~new' }; await flush()
    expect(host.textContent).toContain('Project creation may have completed')
    state.ctx = { ...state.ctx, workspaceUUID: 'other' }; await flush()
    expect(host.textContent).not.toContain('Project creation may have completed')
    state.ctx = { ...state.ctx, workspaceUUID: 'workspace' }; await flush()
    expect(host.textContent).toContain('Project creation may have completed')
    state.ctx = { ...state.ctx, user: { userId: 'bob' } }; await flush()
    expect(host.textContent).not.toContain('Project creation may have completed')
    state.ctx = { ...state.ctx, user: { userId: 'alice' } }; await flush()
    expect(host.textContent).toContain('Project creation may have completed')
    const acknowledge = [...host.querySelectorAll('button')].find(button => button.textContent?.includes('I understand, continue'))!
    acknowledge.click(); await flush()
    expect(host.textContent).not.toContain('Project creation may have completed')
    expect(readProjectCreateUncertainty(identity)).toBe('clear')
  } finally {
    app.unmount(); host.remove(); sessionStorage.clear(); vi.unstubAllGlobals()
  }
})

it('ignores an import response after leaving and returning to the creation route', async () => {
  const { api } = await import('../src/api')
  sessionStorage.clear()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  vi.spyOn(api, 'getLLMSettings').mockResolvedValue({ models: [{ id: 'model', name: 'Model', model: 'gpt-6-sol', provider: 'openai-compatible', baseURL: 'https://example.test', configured: true }] } as never)
  vi.spyOn(api, 'listImportRepositories').mockResolvedValue([{ ref: 'repo', name: 'Repository' }] as never)
  let resolveImport!: (value: never) => void
  const create = vi.spyOn(api, 'createProject').mockReturnValue(new Promise(resolve => { resolveImport = resolve }))
  const navigate = vi.fn()
  const state = reactive({ ctx: { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '~new', token: 'token', fetch: fetchStub } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx: state.ctx, navigate }) })
  const flush = async () => { for (let i = 0; i < 30; i++) { await Promise.resolve(); await nextTick() } }
  try {
    app.mount(host); await flush()
    const trigger = host.querySelector('button[aria-label="Import an existing repository"]') as HTMLButtonElement
    expect(trigger).toBeTruthy()
    const composer = host.querySelector('textarea') as HTMLTextAreaElement
    expect(composer).toBeTruthy()
    composer.value = 'private project idea'; composer.dispatchEvent(new Event('input')); await flush()
    trigger.click(); await flush()
    const select = host.querySelector('#landing-import-repository') as HTMLSelectElement
    select.value = 'repo'; select.dispatchEvent(new Event('change')); await flush()
    const button = [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === 'Import project')!
    button.click(); await flush()
    expect(create).toHaveBeenCalledTimes(1)
    expect(host.textContent).toContain('Project creation may have completed')
    expect(trigger.disabled).toBe(false)
    expect((host.querySelector('button[type=submit]') as HTMLButtonElement).disabled).toBe(true)
    button.click(); await flush()
    expect(create).toHaveBeenCalledTimes(1)
    const markerEntries = Object.entries(sessionStorage).filter(([key]) => key.includes('project-create-uncertain'))
    expect(markerEntries).toHaveLength(1)
    expect(JSON.stringify(markerEntries)).not.toContain('token')
    expect(JSON.stringify(markerEntries)).not.toContain('Repository')
    expect(JSON.stringify(markerEntries)).not.toContain('private project idea')
    state.ctx = { ...state.ctx, subPath: '' }; await flush()
    state.ctx = { ...state.ctx, subPath: '~new' }; await flush()
    expect((host.querySelector('button[aria-label="Import an existing repository"]') as HTMLButtonElement).disabled).toBe(false)
    navigate.mockClear()
    resolveImport({ name: 'late-project' } as never); await flush()
    expect(navigate).not.toHaveBeenCalledWith('late-project')
    expect(host.textContent).toContain('Project creation may have completed')
    expect(readProjectCreateUncertainty({ tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: 'alice' })).toBe('uncertain')
  } finally {
    app.unmount(); host.remove(); sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals()
  }
})

it('disables import after an uncertain failure until the warning is acknowledged', async () => {
  const { api } = await import('../src/api')
  sessionStorage.clear()
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  vi.spyOn(api, 'getLLMSettings').mockResolvedValue({ models: [{ id: 'model', name: 'Model', model: 'gpt-6-sol', provider: 'openai-compatible', baseURL: 'https://example.test', configured: true }] } as never)
  vi.spyOn(api, 'listImportRepositories').mockResolvedValue([{ ref: 'repo', name: 'Repository' }] as never)
  const create = vi.spyOn(api, 'createProject').mockRejectedValueOnce(new Error('Connection lost')).mockResolvedValueOnce({ name: 'imported-project', displayName: 'Imported project', createdAt: '2026-01-01T00:00:00Z' } as never)
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '~new', token: 'token', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const navigate = vi.fn()
  const app = createApp({ render: () => h(App, { ctx, navigate }) })
  const flush = async () => { for (let i = 0; i < 30; i++) { await Promise.resolve(); await nextTick() } }
  try {
    app.mount(host); await flush()
    ;(host.querySelector('button[aria-label="Import an existing repository"]') as HTMLButtonElement).click(); await flush()
    const select = host.querySelector('#landing-import-repository') as HTMLSelectElement
    select.value = 'repo'; select.dispatchEvent(new Event('change')); await flush()
    const button = [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === 'Import project')!
    expect(button.disabled).toBe(false)
    button.click(); await flush()
    expect(create).toHaveBeenCalledTimes(1)
    expect(host.textContent).toContain('Connection lost')
    expect(host.textContent).toContain('Project creation may have completed')
    expect(button.disabled).toBe(true)
    button.click(); await flush()
    expect(create).toHaveBeenCalledTimes(1)
    let dialog = host.querySelector('#landing-import-popover') as HTMLElement
    expect(dialog.textContent).toContain('Project creation may have completed')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); await flush()
    expect(host.querySelector('#landing-import-popover')).toBeNull()
    const trigger = host.querySelector('button[aria-label="Import an existing repository"]') as HTMLButtonElement
    expect(document.activeElement).toBe(trigger)
    trigger.click(); await flush()
    dialog = host.querySelector('#landing-import-popover') as HTMLElement
    expect(dialog.textContent).toContain('Project creation may have completed')
    ;[...dialog.querySelectorAll('button')].find(item => item.textContent?.trim() === 'Check Projects')!.click(); await flush()
    expect(navigate).toHaveBeenCalledWith('')
    expect(host.querySelector('#landing-import-popover')).toBeNull()
    ;(host.querySelector('button[aria-label="Import an existing repository"]') as HTMLButtonElement).click(); await flush()
    dialog = host.querySelector('#landing-import-popover') as HTMLElement
    const acknowledge = [...dialog.querySelectorAll('button')].find(item => item.textContent?.includes('I understand, continue'))!
    acknowledge.click(); await flush()
    const retry = [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === 'Import project')!
    expect(retry.disabled).toBe(false)
    expect((host.querySelector('#landing-import-repository') as HTMLSelectElement).value).toBe('repo')
    expect(document.activeElement).toBe(host.querySelector('#landing-import-repository'))
    expect(readProjectCreateUncertainty({ tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: 'alice' })).toBe('clear')
  } finally {
    app.unmount(); host.remove(); sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals()
  }
})

it.each(['empty', 'loading', 'error', 'refreshing'])('keeps focus in the import dialog after acknowledgement when repositories are %s', async (state) => {
  const { api } = await import('../src/api')
  sessionStorage.clear()
  markProjectCreateUncertain({ tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: 'alice' }, 'attempt')
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200 }))
  vi.stubGlobal('fetch', fetchStub)
  vi.spyOn(api, 'getLLMSettings').mockResolvedValue({ models: [{ id: 'model', name: 'Model', model: 'gpt-6-sol', provider: 'openai-compatible', baseURL: 'https://example.test', configured: true }] } as never)
  const repositories = vi.spyOn(api, 'listImportRepositories')
  if (state === 'empty') repositories.mockResolvedValue([])
  else if (state === 'error') repositories.mockRejectedValue(new Error('Unavailable'))
  else if (state === 'refreshing') repositories.mockResolvedValueOnce([{ ref: 'repo', name: 'Repository' }] as never).mockReturnValue(new Promise(() => {}))
  else repositories.mockReturnValue(new Promise(() => {}))
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '~new', token: 'token', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate: vi.fn() }) })
  const flush = async () => { for (let i = 0; i < 30; i++) { await Promise.resolve(); await nextTick() } }
  try {
    const root = app.mount(host) as any; await flush()
    ;(host.querySelector('button[aria-label="Import an existing repository"]') as HTMLButtonElement).click(); await flush()
    if (state === 'refreshing') { void root.$.subTree.component.setupState.loadImportRepositories(); await flush() }
    const dialog = host.querySelector('#landing-import-popover') as HTMLElement
    expect(dialog.querySelector('#landing-import-repository:not(:disabled)')).toBeNull()
    ;[...dialog.querySelectorAll('button')].find(button => button.textContent?.includes('I understand, continue'))!.click(); await flush()
    expect(document.activeElement).toBe(dialog)
  } finally {
    app.unmount(); host.remove(); sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals()
  }
})

it('offers a Code import only for an empty list and refreshes without losing the draft', async () => {
  const { api } = await import('../src/api')
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  vi.spyOn(api, 'getLLMSettings').mockResolvedValue({ models: [{ id: 'model', name: 'Model', model: 'gpt-6-sol', provider: 'openai-compatible', baseURL: 'https://example.test', configured: true }] } as never)
  let resolveList!: (value: never) => void
  const list = vi.spyOn(api, 'listImportRepositories').mockReturnValue(new Promise(resolve => { resolveList = resolve }))
  const templates = vi.spyOn(api, 'listDevelopmentTemplates').mockResolvedValue([] as never)
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '~new', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate: vi.fn() }) })
  const flush = async () => { for (let i = 0; i < 30; i++) { await Promise.resolve(); await nextTick() } }
  try {
    app.mount(host); await flush()
    expect(list).toHaveBeenCalledWith(ctx)
    expect(templates).toHaveBeenCalledWith(ctx)
    const composer = host.querySelector('textarea') as HTMLTextAreaElement
    composer.value = 'My project idea'; composer.dispatchEvent(new Event('input')); await flush()
    ;(host.querySelector('button[aria-label="Import an existing repository"]') as HTMLButtonElement).click(); await flush()
    expect(host.textContent).toContain('Loading repositories')
    expect(host.textContent).not.toContain('Import repository in Code')
    resolveList([] as never); await flush()
    const link = [...host.querySelectorAll('a')].find(item => item.textContent?.includes('Import repository in Code'))!
    expect(link.getAttribute('href')).toBe('/ui/org/workspace/providers/code/import/repository')
    expect(link.target).toBe('_blank')
    expect(link.rel).toContain('noopener')
    list.mockRejectedValueOnce(new Error('Could not load repositories'))
    ;[...host.querySelectorAll('button')].find(item => item.textContent?.trim() === 'Refresh')!.click(); await flush()
    expect(host.textContent).toContain('Could not load repositories')
    expect(host.textContent).not.toContain('Import repository in Code')
    list.mockResolvedValueOnce([{ ref: 'repo', name: 'Repository' }] as never)
    ;[...host.querySelectorAll('button')].find(item => item.textContent?.trim() === 'Retry')!.click(); await flush()
    expect(host.querySelector('#landing-import-repository')?.textContent).toContain('Repository')
    expect(composer.value).toBe('My project idea')
  } finally {
    app.unmount(); host.remove(); sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals()
  }
})

it('waits for workspace transport and loads both catalogs when it arrives', async () => {
  const { api } = await import('../src/api')
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchStub)
  vi.spyOn(api, 'getLLMSettings').mockResolvedValue({ models: [{ id: 'model', name: 'Model', model: 'gpt-6-sol', provider: 'openai-compatible', baseURL: 'https://example.test', configured: true }] } as never)
  const list = vi.spyOn(api, 'listImportRepositories').mockResolvedValue([] as never)
  const templates = vi.spyOn(api, 'listDevelopmentTemplates').mockResolvedValue([] as never)
  const state = reactive<{ ctx: any }>({ ctx: { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '~new' } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx: state.ctx, navigate: vi.fn() }) })
  const flush = async () => { for (let i = 0; i < 30; i++) { await Promise.resolve(); await nextTick() } }
  try {
    app.mount(host); await flush()
    expect(list).not.toHaveBeenCalled()
    expect(templates).not.toHaveBeenCalled()
    expect(host.textContent).not.toContain('No unclaimed repositories available')
    state.ctx = { ...state.ctx, fetch: fetchStub }; await flush()
    expect(list).toHaveBeenCalledTimes(1)
    expect(templates).toHaveBeenCalledTimes(1)
    ;(host.querySelector('button[aria-label="Import an existing repository"]') as HTMLButtonElement).click(); await flush()
    expect(host.textContent).toContain('No unclaimed repositories available')
    const composer = host.querySelector('textarea') as HTMLTextAreaElement
    composer.value = 'Keep this idea'; composer.dispatchEvent(new Event('input')); await flush()
    let resolveLate!: (value: never) => void
    list.mockReturnValueOnce(new Promise(resolve => { resolveLate = resolve }))
    ;[...host.querySelectorAll('button')].find(item => item.textContent?.trim() === 'Refresh')!.click(); await flush()
    state.ctx = { ...state.ctx, fetch: undefined }; await flush()
    resolveLate([{ ref: 'stale', name: 'Stale repository' }] as never); await flush()
    expect(host.textContent).not.toContain('Stale repository')
    list.mockResolvedValueOnce([{ ref: 'fresh', name: 'Fresh repository' }] as never)
    state.ctx = { ...state.ctx, fetch: fetchStub }; await flush()
    expect((host.querySelector('textarea') as HTMLTextAreaElement).value).toBe('Keep this idea')
    if (!host.querySelector('#landing-import-popover')) {
      ;(host.querySelector('button[aria-label="Import an existing repository"]') as HTMLButtonElement).click(); await flush()
    }
    expect(host.textContent).toContain('Fresh repository')
    expect(host.textContent).not.toContain('Stale repository')
  } finally {
    app.unmount(); host.remove(); sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals()
  }
})



it('stops the owning wait, hands focus to its warning, and fences scope changes', async () => {
  const { useProjectCreationWait } = await import('../src/useProjectCreationWait')
  let wait!: ReturnType<typeof useProjectCreationWait>
  const showWarning = reactive({ value: false })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup() {
    wait = useProjectCreationWait()
    return () => h('div', [
      wait.waiting.value ? h('button', { ref: wait.stopButton, onClick: wait.stop }, 'Stop waiting') : null,
      showWarning.value ? h('section', { ref: wait.warning, tabindex: -1 }, 'Creation may have completed') : null,
      h('button', { id: 'other-focus' }, 'Other'),
    ])
  } })
  try {
    app.mount(host)
    const first = wait.begin(); await nextTick()
    const stop = host.querySelector('button')!; stop.focus(); stop.click()
    expect(first.signal.aborted).toBe(true)
    wait.finish(first); showWarning.value = true; await nextTick()
    expect(document.activeElement).toBe(host.querySelector('section'))
    showWarning.value = false
    const second = wait.begin(); await nextTick()
    const newer = wait.begin(); wait.finish(second)
    expect(wait.waiting.value).toBe(true)
    wait.invalidate(); expect(newer.signal.aborted).toBe(true)
    wait.finish(newer); expect(wait.waiting.value).toBe(false)
    const third = wait.begin(); await nextTick()
    host.querySelector('button')!.focus(); wait.stop(); wait.finish(third)
    host.querySelector<HTMLButtonElement>('#other-focus')!.focus()
    showWarning.value = true; await nextTick()
    expect(document.activeElement).toBe(host.querySelector('#other-focus'))
  } finally { app.unmount(); host.remove() }
})

it('closes the composer without restoring focus when streaming begins, including while its async ref is loading', async () => {
  const fetchStub = vi.fn(async () => new Response('{}', { status: 200 }))
  vi.stubGlobal('fetch', fetchStub)
  const errors = vi.spyOn(console, 'error').mockImplementation(() => {})
  const ctx = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: { userId: 'alice' }, subPath: '~new', fetch: fetchStub }
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(App, { ctx, navigate: vi.fn() }) })
  const flush = async () => { for (let i = 0; i < 20; i++) { await Promise.resolve(); await nextTick() } }
  try {
    const root = app.mount(host) as any; await flush()
    const studio = root.$.subTree.component.setupState
    const closePalette = vi.fn()
    studio.assistantComposerRef = { closePalette }
    studio.messageStreaming = true; await flush()
    expect(closePalette).toHaveBeenCalledWith({ restoreFocus: false })
    studio.messageStreaming = false; await flush()
    studio.assistantComposerRef = {}
    studio.messageStreaming = true; await flush()
    expect(errors).not.toHaveBeenCalled()
  } finally {
    app.unmount(); host.remove(); sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals()
  }
})
