// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it, vi } from 'vitest'
import NewProjectWizard from '../src/NewProjectWizard.vue'
import { api } from '../src/api'

it('confirms a reviewed draft with an explicit without-Git action', async () => {
  const plan = vi.spyOn(api, 'planProject').mockResolvedValue({ displayName: 'Tracker', availableTemplates: [], allowEmptyTemplate: true } as never)
  const create = vi.fn()
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(NewProjectWizard, {
    ctx: null, initialPrompt: 'Build a tracker', createLabel: 'Create project without Git', onCreate: create,
  }) })
  try {
    app.mount(host)
    for (let i = 0; i < 10; i++) { await Promise.resolve(); await nextTick() }
    const button = [...host.querySelectorAll('button')].find(item => item.textContent?.trim() === 'Create project without Git')!
    expect(button.disabled).toBe(false)
    button.click()
    expect(create).toHaveBeenCalledWith({ prompt: 'Build a tracker', displayName: 'Tracker', templateName: undefined })
  } finally {
    app.unmount(); host.remove(); plan.mockRestore()
  }
})

it('preserves review edits on same-authority context updates and resets them on workspace change', async () => {
  const plan = vi.spyOn(api, 'planProject').mockResolvedValue({ displayName: 'Tracker', availableTemplates: [], allowEmptyTemplate: true } as never)
  const state = reactive({ ctx: { tenant: 'one', theme: 'light' as 'light' | 'dark', fetch: vi.fn() } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(NewProjectWizard, { ctx: state.ctx, initialPrompt: 'Build a tracker' }) })
  const flush = async () => { for (let i = 0; i < 10; i++) { await Promise.resolve(); await nextTick() } }
  try {
    app.mount(host); await flush()
    const input = host.querySelector('input[autocomplete="off"]') as HTMLInputElement
    input.value = 'My edited name'; input.dispatchEvent(new Event('input'))
    state.ctx = { ...state.ctx, theme: 'dark' }; await flush()
    expect((host.querySelector('input[autocomplete="off"]') as HTMLInputElement).value).toBe('My edited name')
    expect(plan).toHaveBeenCalledTimes(1)
    state.ctx = { ...state.ctx, tenant: 'two' }; await flush()
    expect((host.querySelector('textarea') as HTMLTextAreaElement).value).toBe('')
    expect(plan).toHaveBeenCalledTimes(1)
  } finally { app.unmount(); host.remove(); plan.mockRestore() }
})

it('keeps manual drafts on same-authority refresh and clears them when the user changes', async () => {
  const state = reactive({ ctx: { tenant: 'one', user: { userId: 'alice' }, token: 'one' } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(NewProjectWizard, { ctx: state.ctx }) })
  const flush = async () => { for (let i = 0; i < 10; i++) { await Promise.resolve(); await nextTick() } }
  try {
    app.mount(host); await flush()
    const input = host.querySelector('textarea') as HTMLTextAreaElement
    input.value = 'Private draft'; input.dispatchEvent(new Event('input')); await flush()
    state.ctx = { ...state.ctx, token: 'renewed' }; await flush()
    expect(input.value).toBe('Private draft')
    state.ctx = { ...state.ctx, user: { userId: 'bob' } }; await flush()
    expect(input.value).toBe('')
  } finally { app.unmount(); host.remove() }
})

it('edits a new draft after an authority change instead of cancelling the old submitted prompt', async () => {
  const plan = vi.spyOn(api, 'planProject').mockResolvedValue({ displayName: 'Tracker', availableTemplates: [], allowEmptyTemplate: true } as never)
  const cancel = vi.fn()
  const state = reactive({ ctx: { tenant: 'one' } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(NewProjectWizard, { ctx: state.ctx, initialPrompt: 'Old idea', onCancel: cancel }) })
  const flush = async () => { for (let i = 0; i < 10; i++) { await Promise.resolve(); await nextTick() } }
  try {
    app.mount(host); await flush()
    state.ctx = { tenant: 'two' }; await flush()
    const input = host.querySelector('textarea') as HTMLTextAreaElement
    input.value = 'New idea'; input.dispatchEvent(new Event('input')); await flush()
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Prepare project'))!.click(); await flush()
    ;[...host.querySelectorAll('button')].find(button => button.textContent?.includes('Edit request'))!.click(); await flush()
    expect(cancel).not.toHaveBeenCalled()
    expect((host.querySelector('textarea') as HTMLTextAreaElement).value).toBe('New idea')
  } finally { app.unmount(); host.remove(); plan.mockRestore() }
})

it('ignores a stale plan rejection after token renewal', async () => {
  let rejectOld!: (reason: Error) => void
  const old = new Promise<never>((_, reject) => { rejectOld = reject })
  const plan = vi.spyOn(api, 'planProject').mockReturnValueOnce(old).mockResolvedValueOnce({ displayName: 'Current', availableTemplates: [], allowEmptyTemplate: true } as never)
  const state = reactive({ ctx: { tenant: 'one', token: 'old' } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(NewProjectWizard, { ctx: state.ctx, initialPrompt: 'Build a tracker' }) })
  const flush = async () => { for (let i = 0; i < 10; i++) { await Promise.resolve(); await nextTick() } }
  try {
    app.mount(host); await flush()
    state.ctx = { ...state.ctx, token: 'new' }; await flush()
    rejectOld(new Error('old failure')); await flush()
    expect(plan).toHaveBeenCalledTimes(2)
    expect(host.textContent).not.toContain('old failure')
    expect((host.querySelector('input[autocomplete="off"]') as HTMLInputElement).value).toBe('Current')
  } finally { app.unmount(); host.remove(); plan.mockRestore() }
})

it('restarts an in-flight plan when transport changes and ignores the old response', async () => {
  let resolveOld!: (value: never) => void
  const old = new Promise<never>(resolve => { resolveOld = resolve })
  const plan = vi.spyOn(api, 'planProject').mockReturnValueOnce(old).mockResolvedValueOnce({ displayName: 'Current', availableTemplates: [], allowEmptyTemplate: true } as never)
  const state = reactive({ ctx: { tenant: 'one', fetch: vi.fn() } })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(NewProjectWizard, { ctx: state.ctx, initialPrompt: 'Build a tracker' }) })
  const flush = async () => { for (let i = 0; i < 10; i++) { await Promise.resolve(); await nextTick() } }
  try {
    app.mount(host); await flush()
    state.ctx = { ...state.ctx, fetch: vi.fn() }; await flush()
    resolveOld({ displayName: 'Stale', availableTemplates: [], allowEmptyTemplate: true } as never); await flush()
    expect(plan).toHaveBeenCalledTimes(2)
    expect((host.querySelector('input[autocomplete="off"]') as HTMLInputElement).value).toBe('Current')
  } finally { app.unmount(); host.remove(); plan.mockRestore() }
})
