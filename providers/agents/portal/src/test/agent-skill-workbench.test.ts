import { describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, ref } from 'vue'
import SkillsLibrary from '../views/SkillsLibrary.vue'
import { confirmState, resolveConfirm } from '../portalkit/confirm'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

describe('Workspace skills library', () => {
  it('offers creation and import only for a confirmed empty library', async () => {
    const pending = deferred<{ skills: never[]; warnings: string[] }>()
    const api = stubApi({ getSkillLibrary: () => pending.promise })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api, returnAgent: 'scout' })
    expect(view.element.querySelector('.k-first-run')).toBeNull()
    pending.resolve({ skills: [], warnings: [] })
    await settleVue(8)
    const guide = view.element.querySelector<HTMLElement>('.k-first-run')!
    expect(text(guide)).toContain('Enable for an agent')
    expect([...view.element.querySelectorAll('button')].filter(button => text(button) === 'New skill')).toHaveLength(1)
    expect([...view.element.querySelectorAll('button')].filter(button => text(button) === 'Import')).toHaveLength(0)
    const buttons = [...guide.querySelectorAll<HTMLButtonElement>('button')]
    const file = view.element.querySelector<HTMLInputElement>('input[type=file]')!
    const click = vi.spyOn(file, 'click').mockImplementation(() => {})
    buttons[1].click()
    expect(click).toHaveBeenCalledOnce()
    buttons[0].click()
    expect(view.navigations.at(-1)).toEqual({ kind: 'create', resource: 'skill', returnAgent: 'scout' })
    view.unmount()
  })

  it.each(['New skill', 'Import skill'])('moves focus from guide %s to its header action when refresh starts', async (label) => {
    const pending = deferred<{ skills: never[]; warnings: string[] }>()
    const getSkillLibrary = vi.fn().mockResolvedValueOnce({ skills: [], warnings: [] }).mockReturnValueOnce(pending.promise)
    const api = stubApi({ getSkillLibrary })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api })
    const button = [...view.element.querySelectorAll<HTMLButtonElement>('.k-first-run button')].find(item => text(item) === label)!
    button.focus()
    ;[...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => text(item) === 'Refresh')!.click()
    await settleVue()
    expect(view.element.querySelector('.k-first-run')).toBeNull()
    expect(text(document.activeElement)).toBe(label === 'Import skill' ? 'Import' : 'New skill')
    pending.resolve({ skills: [], warnings: [] })
    await settleVue(8)
    view.unmount()
  })

  it('does not move outside focus when the guide changes during refresh', async () => {
    const pending = deferred<{ skills: never[]; warnings: string[] }>()
    const getSkillLibrary = vi.fn().mockResolvedValueOnce({ skills: [], warnings: [] }).mockReturnValueOnce(pending.promise)
    const api = stubApi({ getSkillLibrary })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api })
    const outside = document.createElement('button')
    document.body.append(outside)
    outside.focus()
    ;[...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => text(item) === 'Refresh')!.click()
    await settleVue()
    expect(document.activeElement).toBe(outside)
    pending.resolve({ skills: [], warnings: [] })
    await settleVue(8)
    expect(document.activeElement).toBe(outside)
    outside.remove()
    view.unmount()
  })

  it('keeps first-use guidance hidden on partial or stale empty results', async () => {
    const refresh = deferred<{ skills: never[]; warnings: string[] }>()
    const getSkillLibrary = vi.fn().mockResolvedValueOnce({ skills: [], warnings: ['Provider catalog unavailable'] }).mockReturnValueOnce(refresh.promise)
    const api = stubApi({ getSkillLibrary })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api })
    expect(view.element.querySelector('.k-first-run')).toBeNull()
    ;[...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Refresh')!.click()
    await settleVue()
    expect(view.element.querySelector('.k-first-run')).toBeNull()
    refresh.reject(new Error('Offline'))
    await settleVue(8)
    expect(view.element.querySelector('.k-first-run')).toBeNull()
    expect(text(view.element)).toContain('Showing the last loaded skills. Offline')
    view.unmount()
  })

  it('retains the library and search after a populated catalog becomes empty', async () => {
    const skill = { id: 'workspace:review', name: 'Review', description: 'Check a change', scope: 'workspace', enabled: true, editable: true, resources: [] }
    const getSkillLibrary = vi.fn().mockResolvedValueOnce({ skills: [skill], warnings: [] }).mockResolvedValueOnce({ skills: [], warnings: [] })
    const api = stubApi({ getSkillLibrary })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api })
    const search = view.element.querySelector<HTMLInputElement>('input[type=search]')!
    search.value = 'review'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    ;[...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Refresh')!.click()
    await settleVue(8)
    expect(view.element.querySelector('.k-first-run')).toBeNull()
    expect(search.value).toBe('review')
    expect(search.closest<HTMLElement>('.agents-skill-workbench__columns')?.style.display).not.toBe('none')
    view.unmount()
  })

  it('leaves creation after a successful write even while the catalog is loading', async () => {
    const catalog = deferred<{ skills: never[]; warnings: string[] }>()
    const api = stubApi({ context: () => ({ tenant: 'workspace-cluster', fetch: vi.fn(async () => new Response('{}', { status: 201 })) }), getSkillLibrary: () => catalog.promise })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api, createRoute: true })
    const button = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => text(item) === 'Create skill')!
    button.click()
    await settleVue(8)
    expect(view.navigations.at(-1)).toEqual({ kind: 'menu', menu: 'skills' })
    await view.setProps({ createRoute: false })
    expect(view.element.querySelector('.agents-skill-workbench__document-editor')).toBeNull()
    catalog.resolve({ skills: [], warnings: [] })
    await settleVue(8)
    view.unmount()
  })

  it.each([false, true])('restores focus after deferred create while respecting outside focus: %s', async (moveFocus) => {
    const response = deferred<Response>()
    const api = stubApi({ context: () => ({ tenant: 'workspace-cluster', fetch: () => response.promise }), getSkillLibrary: async () => ({ skills: [], warnings: [] }) })
    const store = makeStore(api)
    const root = document.createElement('div')
    const outside = document.createElement('button')
    document.body.append(root, outside)
    const app = createApp(defineComponent({
      setup() {
        const creating = ref(true)
        return () => h(SkillsLibrary, { store, api, createRoute: creating.value, onNavigate: () => { creating.value = false } })
      },
    }))
    app.mount(root)
    await settleVue()
    const submit = [...root.querySelectorAll('button')].find(button => text(button) === 'Create skill')!
    submit.focus()
    submit.click()
    await settleVue()
    if (moveFocus) outside.focus()
    response.resolve(new Response('{}', { status: 201 }))
    await settleVue(12)
    const create = [...root.querySelectorAll('button')].find(button => text(button) === 'New skill')!
    expect(create.disabled).toBe(false)
    expect(document.activeElement).toBe(moveFocus ? outside : create)
    app.unmount()
    root.remove()
    outside.remove()
  })

  it('rejects an overlong package name on its field before writing', async () => {
    const fetch = vi.fn()
    const api = stubApi({ context: () => ({ tenant: 'workspace-cluster', fetch }), getSkillLibrary: async () => ({ skills: [], warnings: [] }) })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api, createRoute: true })
    const name = view.element.querySelector<HTMLInputElement>('input[placeholder="my-skill"]')!
    name.value = 'a'.repeat(254)
    name.dispatchEvent(new Event('input', { bubbles: true }))
    ;[...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Create skill')!.click()
    await settleVue()
    expect(name.getAttribute('aria-invalid')).toBe('true')
    expect(name.getAttribute('aria-describedby')).toBe('skill-validation-error')
    expect(text(view.element.querySelector('#skill-validation-error'))).toContain('253 characters')
    expect(fetch).not.toHaveBeenCalled()
    view.unmount()
  })

  it('associates validation errors with the relevant field or resource group', async () => {
    const api = stubApi({ getSkillLibrary: async () => ({ skills: [], warnings: [] }) })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api, createRoute: true })
    const button = (label: string) => [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => text(item) === label)!
    const name = view.element.querySelector<HTMLInputElement>('input[placeholder="my-skill"]')!
    const document = view.element.querySelector<HTMLTextAreaElement>('.agents-skill-workbench__document-editor')!
    name.value = 'Invalid Name'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    button('Create skill').click()
    await settleVue()
    expect(name.getAttribute('aria-describedby')).toBe('skill-validation-error')
    expect(document.hasAttribute('aria-invalid')).toBe(false)
    name.value = ''
    name.dispatchEvent(new Event('input', { bubbles: true }))
    button('Add resource').click()
    await settleVue()
    const resource = view.element.querySelector<HTMLInputElement>('input[placeholder="references/guide.md"]')!
    resource.value = '../invalid'
    resource.dispatchEvent(new Event('input', { bubbles: true }))
    button('Create skill').click()
    await settleVue()
    expect(view.element.querySelector('fieldset')?.getAttribute('aria-describedby')).toBe('skill-validation-error')
    expect(name.hasAttribute('aria-invalid')).toBe(false)
    view.unmount()
  })
  it('keeps the library and draft across route history and returns with context', async () => {
    const api = stubApi({ getSkillLibrary: async () => ({ skills: [], warnings: [] }) })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api, returnAgent: 'scout', createRoute: false })
    const button = (label: string) => [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => text(item) === label)!
    button('New skill').click()
    expect(view.navigations.at(-1)).toEqual({ kind: 'create', resource: 'skill', returnAgent: 'scout' })
    await view.setProps({ createRoute: true })
    expect(document.activeElement?.textContent).toBe('Create a workspace skill')
    expect(view.element.querySelector<HTMLElement>('[aria-label="Skill catalog"]')?.style.display).toBe('none')
    const editor = view.element.querySelector<HTMLTextAreaElement>('.agents-skill-workbench__document-editor')!
    editor.value = 'draft kept'
    editor.dispatchEvent(new Event('input', { bubbles: true }))
    button('Add resource').click()
    await settleVue()
    expect(view.element.querySelector('input[placeholder="references/guide.md"]')).not.toBeNull()
    await view.setProps({ createRoute: false })
    expect(document.activeElement).toBe(button('New skill'))
    await view.setProps({ createRoute: true })
    expect(view.element.querySelector<HTMLTextAreaElement>('.agents-skill-workbench__document-editor')?.value).toBe('draft kept')
    button('Cancel').click()
    expect(view.navigations.at(-1)).toEqual({ kind: 'menu', menu: 'skills', returnAgent: 'scout' })
    await view.setProps({ createRoute: false })
    await view.setProps({ createRoute: true })
    expect(view.element.querySelector<HTMLTextAreaElement>('.agents-skill-workbench__document-editor')?.value).not.toBe('draft kept')
    view.unmount()
  })

  it('does not delete a same-named skill after the workspace changes during confirmation', async () => {
    const skill = { id: 'workspace:review', name: 'Review', description: 'Review', scope: 'workspace', packageName: 'review', enabled: true, editable: true, version: '7', resources: [] }
    const fetch = vi.fn(async () => new Response('{}'))
    const api = stubApi({ context: () => ({ tenant: 'workspace-cluster', fetch }), getSkillLibrary: async () => ({ skills: [skill], warnings: [] }), getSkillLibraryDetail: async () => ({ ...skill, document: '---\nname: review\ndescription: Review\n---\nOriginal', instructions: 'Original' }) })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api, authorityEpoch: 1 })
    const select = () => view.element.querySelector<HTMLButtonElement>('button[aria-label^="Review."]')!.click()
    select()
    await settleVue(8)
    view.element.querySelector<HTMLButtonElement>('button[aria-label="Delete workspace skill"]')!.click()
    expect(confirmState.open).toBe(true)
    await view.setProps({ authorityEpoch: 2 })
    await settleVue(8)
    select()
    await settleVue(8)
    resolveConfirm(true)
    await settleVue(8)
    expect(fetch).not.toHaveBeenCalled()
    view.unmount()
  })
  it.each([
    { name: 'unchanged', initial: '7', latest: '7', conflict: false },
    { name: 'changed', initial: '7', latest: '8', conflict: true },
    { name: 'missing baseline version', initial: undefined, latest: '7', conflict: true },
    { name: 'missing refreshed version', initial: '7', latest: undefined, conflict: true },
  ])('preserves a draft after a $name workspace refresh', async ({ initial, latest, conflict }) => {
    const skill = { id: 'workspace:review', name: 'Review', description: 'Review', scope: 'workspace', packageName: 'review', enabled: true, editable: true, version: initial, resources: [] }
    const document = '---\nname: review\ndescription: Review\n---\nOriginal'
    const pending = deferred<{ skills: typeof skill[]; warnings: string[] }>()
    const getSkillLibrary = vi.fn().mockResolvedValueOnce({ skills: [skill], warnings: [] }).mockReturnValueOnce(pending.promise)
    const api = stubApi({ getSkillLibrary, getSkillLibraryDetail: async () => ({ ...skill, document, instructions: 'Original' }) })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api })
    const button = (label: string) => [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => text(item) === label)!
    view.element.querySelector<HTMLButtonElement>('button[aria-label^="Review."]')!.click()
    await settleVue(8)
    button('Edit').click()
    await settleVue()
    const editor = view.element.querySelector<HTMLTextAreaElement>('textarea')!
    editor.value += ' Draft'
    editor.dispatchEvent(new Event('input', { bubbles: true }))
    button('Refresh').click()
    await settleVue()
    pending.resolve({ skills: [{ ...skill, version: latest }], warnings: [] })
    await settleVue(8)
    expect(editor.value).toContain('Draft')
    expect(button('Save skill').disabled).toBe(conflict)
    expect(text(view.element).includes('This skill changed or was removed')).toBe(conflict)
    view.unmount()
  })
  it('submits the read resource version and preserves the draft when the update conflicts', async () => {
    const skill = { id: 'workspace:review', name: 'Review', description: 'Review', scope: 'workspace', packageName: 'review', enabled: true, editable: true, version: '7', resources: [] }
    const original = '---\nname: review\ndescription: Review\n---\nOriginal'
    let submitted: { metadata: { resourceVersion: string }; spec: { document: string } } | undefined
    const fetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'GET') return new Response(JSON.stringify({ apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'Skill', metadata: { name: 'review', resourceVersion: '7' }, spec: { document: original, resources: [] } }))
      submitted = JSON.parse(String(init?.body))
      return new Response(JSON.stringify({ message: 'Resource version conflict' }), { status: 409 })
    })
    const api = stubApi({ context: () => ({ tenant: 'workspace-cluster', fetch }), getSkillLibrary: async () => ({ skills: [skill], warnings: [] }), getSkillLibraryDetail: async () => ({ ...skill, document: original, instructions: 'Original' }) })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api })
    view.element.querySelector<HTMLButtonElement>('button[aria-label^="Review."]')!.click()
    await settleVue(8)
    ;[...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Edit')!.click()
    await settleVue()
    const editor = view.element.querySelector<HTMLTextAreaElement>('textarea')!
    editor.value += ' Draft'
    editor.dispatchEvent(new Event('input', { bubbles: true }))
    ;[...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Save skill')!.click()
    await settleVue(12)
    expect(submitted?.metadata.resourceVersion).toBe('7')
    expect(submitted?.spec.document).toContain('Draft')
    expect(editor.value).toContain('Draft')
    expect(text(view.element)).toContain('Could not save skill:')
    expect(text(view.element)).toContain('Resource version conflict')
    view.unmount()
  })
  it.each(['server changed before save', 'refresh detects change during save'])('blocks a stale edit when %s', async scenario => {
    const skill = { id: 'workspace:review', name: 'Review', description: 'Review', scope: 'workspace', packageName: 'review', enabled: true, editable: true, version: '7', resources: [] }
    const original = '---\nname: review\ndescription: Review\n---\nOriginal'
    const pending = deferred<Response>()
    const fetch = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'GET') return pending.promise
      return Promise.resolve(new Response('{}'))
    })
    const getSkillLibrary = vi.fn().mockResolvedValueOnce({ skills: [skill], warnings: [] }).mockResolvedValue({ skills: [{ ...skill, version: '8' }], warnings: [] })
    const api = stubApi({ context: () => ({ tenant: 'workspace-cluster', fetch }), getSkillLibrary, getSkillLibraryDetail: async () => ({ ...skill, document: original, instructions: 'Original' }) })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api })
    const button = (label: string) => [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => text(item) === label)!
    view.element.querySelector<HTMLButtonElement>('button[aria-label^="Review."]')!.click()
    await settleVue(8)
    button('Edit').click()
    await settleVue()
    const editor = view.element.querySelector<HTMLTextAreaElement>('textarea')!
    editor.value += ' Draft'
    editor.dispatchEvent(new Event('input', { bubbles: true }))
    button('Save skill').click()
    await settleVue()
    if (scenario === 'refresh detects change during save') {
      button('Refresh').click()
      await settleVue(8)
    }
    pending.resolve(new Response(JSON.stringify({ apiVersion: 'agents.railgrid.ai/v1alpha1', kind: 'Skill', metadata: { name: 'review', resourceVersion: '7' }, spec: { document: scenario === 'server changed before save' ? original + ' external' : original, resources: [] } })))
    await settleVue(12)
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(editor.value).toContain('Draft')
    expect(text(view.element)).toContain('This skill changed or was removed')
    view.unmount()
  })
  it('keeps a new scope import locked when an old scope import finishes', async () => {
    const first = deferred<string>()
    const second = deferred<string>()
    const api = stubApi({ getSkillLibrary: async () => ({ skills: [], warnings: [] }) })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api, authorityEpoch: 1 })
    const input = view.element.querySelector<HTMLInputElement>('input[type=file]')!
    const choose = (promise: Promise<string>) => {
      const file = new File([''], 'skill.md', { type: 'text/markdown' })
      Object.defineProperty(file, 'text', { value: () => promise })
      Object.defineProperty(input, 'files', { configurable: true, value: [file] })
      input.dispatchEvent(new Event('change', { bubbles: true }))
    }
    choose(first.promise)
    await settleVue()
    await view.setProps({ authorityEpoch: 2 })
    choose(second.promise)
    await settleVue()
    first.resolve('---\nname: old\ndescription: Old\n---\nOld')
    await settleVue(8)
    expect(text(view.element)).toContain('Importing…')
    expect(input.disabled).toBe(true)
    second.resolve('---\nname: new\ndescription: New\n---\nNew')
    await settleVue(8)
    expect(view.navigations.at(-1)).toEqual({ kind: 'create', resource: 'skill' })
    await view.setProps({ createRoute: true })
    expect(view.element.querySelector<HTMLTextAreaElement>('textarea')!.value).toContain('New')
    view.unmount()
  })
  it('reloads selected detail after a catalog refresh', async () => {
    const skill = { id: 'workspace:review', name: 'Review', description: 'Review', scope: 'workspace', packageName: 'review', enabled: true, editable: true, digest: 'a', resources: [] }
    const getSkillLibrary = vi.fn().mockResolvedValueOnce({ skills: [skill], warnings: [] }).mockResolvedValue({ skills: [{ ...skill, digest: 'b' }], warnings: [] })
    const getSkillLibraryDetail = vi.fn().mockResolvedValueOnce({ ...skill, instructions: 'Original' }).mockResolvedValue({ ...skill, digest: 'b', instructions: 'Updated' })
    const api = stubApi({ getSkillLibrary, getSkillLibraryDetail })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api })
    view.element.querySelector<HTMLButtonElement>('button[aria-label^="Review."]')!.click()
    await settleVue(8)
    expect(text(view.element)).toContain('Original')
    ;[...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Refresh')!.click()
    await settleVue(8)
    expect(text(view.element)).toContain('Updated')
    expect(text(view.element)).not.toContain('Original')
    view.unmount()
  })

  it('preserves an edit draft and prevents saving after an external change', async () => {
    const skill = { id: 'workspace:review', name: 'Review', description: 'Review', scope: 'workspace', packageName: 'review', enabled: true, editable: true, version: '7', resources: [] }
    const getSkillLibrary = vi.fn().mockResolvedValueOnce({ skills: [skill], warnings: [] }).mockResolvedValue({ skills: [{ ...skill, version: '8' }], warnings: [] })
    const api = stubApi({ getSkillLibrary, getSkillLibraryDetail: async () => ({ ...skill, document: '---\nname: review\ndescription: Review\n---\nOriginal', instructions: 'Original' }) })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api })
    view.element.querySelector<HTMLButtonElement>('button[aria-label^="Review."]')!.click()
    await settleVue(8)
    ;[...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Edit')!.click()
    await settleVue()
    const draft = view.element.querySelector<HTMLTextAreaElement>('textarea')!
    draft.value += ' My draft'
    draft.dispatchEvent(new Event('input', { bubbles: true }))
    ;[...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Refresh')!.click()
    await settleVue(8)
    expect(draft.value).toContain('My draft')
    expect(text(view.element)).toContain('This skill changed or was removed')
    expect([...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Save skill')!.disabled).toBe(true)
    view.unmount()
  })
  it('distinguishes a failed initial read from an empty library and retries', async () => {
    const getSkillLibrary = vi.fn().mockRejectedValueOnce(new Error('Offline')).mockResolvedValue({ skills: [], catalogDigest: '', warnings: [] })
    const api = stubApi({ getSkillLibrary })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api })
    expect(text(view.element)).toContain('Could not load skill library. Offline')
    expect(text(view.element)).not.toContain('No skills have been published')
    const retry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Retry')!
    retry.click()
    await settleVue(8)
    expect(text(view.element)).toContain('No skills have been published')
    expect(getSkillLibrary).toHaveBeenCalledTimes(2)
    view.unmount()
  })

  it('keeps the search, results and focused refresh control during a failed refresh', async () => {
    const skill = { id: 'workspace:review', name: 'Review', description: 'Check a change', scope: 'workspace', enabled: true, editable: true, resources: [] }
    const pending = deferred<{ skills: typeof skill[]; catalogDigest: string; warnings: string[] }>()
    const getSkillLibrary = vi.fn().mockResolvedValueOnce({ skills: [skill], catalogDigest: '', warnings: [] }).mockReturnValueOnce(pending.promise)
    const api = stubApi({ getSkillLibrary })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api })
    const input = view.element.querySelector<HTMLInputElement>('input[type=search]')!
    input.value = 'review'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    const refresh = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Refresh')!
    refresh.focus()
    refresh.click()
    refresh.click()
    await settleVue()
    expect(getSkillLibrary).toHaveBeenCalledTimes(2)
    expect(document.activeElement).toBe(refresh)
    expect(input.value).toBe('review')
    expect(text(view.element)).toContain('Review')
    pending.reject(new Error('Connection lost'))
    await settleVue(8)
    expect(document.activeElement).toBe(refresh)
    expect(input.value).toBe('review')
    expect(text(view.element)).toContain('Showing the last loaded skills. Connection lost')
    expect(text(view.element)).toContain('Review')
    view.unmount()
  })

  it('ignores an old scope response after authority changes', async () => {
    const old = deferred<{ skills: Array<{ id: string; name: string; description: string; scope: string; enabled: boolean; editable: boolean; resources: never[] }>; catalogDigest: string; warnings: string[] }>()
    const getSkillLibrary = vi.fn().mockReturnValueOnce(old.promise).mockResolvedValueOnce({ skills: [], catalogDigest: '', warnings: [] })
    const api = stubApi({ getSkillLibrary })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api, authorityEpoch: 1 })
    await view.setProps({ authorityEpoch: 2 })
    await settleVue(8)
    old.resolve({ skills: [{ id: 'old', name: 'Old scope skill', description: '', scope: 'workspace', enabled: true, editable: true, resources: [] }], catalogDigest: '', warnings: [] })
    await settleVue(8)
    expect(text(view.element)).not.toContain('Old scope skill')
    expect(text(view.element)).toContain('No skills have been published')
    view.unmount()
  })
  it('uses the shared multi-term search across skill names and scopes', async () => {
    const skill = { id: 'workspace:review', name: 'Review', description: 'Check a change', scope: 'workspace', enabled: true, editable: true, resources: [] }
    const api = stubApi({ getSkillLibrary: async () => ({ skills: [skill], catalogDigest: '', warnings: [] }) })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(SkillsLibrary, { store, api, name: 'scout' })
    const input = view.element.querySelector<HTMLInputElement>('input[type=search]')!
    input.value = 'workspace review'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await settleVue(6)
    expect(view.element.querySelector('button[aria-label^="Review."]')).not.toBeNull()
  })

  it('retries a failed skill detail read without leaving the selected skill', async () => {
    const skill = {
      id: 'workspace:release-review',
      name: 'Release review',
      description: 'Review release evidence.',
      scope: 'workspace',
      packageName: 'release-review',
      enabled: true,
      editable: true,
      digest: 'a'.repeat(64),
      resources: [],
    }
    let attempts = 0
    const getSkillLibraryDetail = vi.fn(async () => {
      attempts += 1
      if (attempts === 1) throw new Error('Temporary detail read failure')
      return { ...skill, instructions: 'Check the changelog.', resources: [] }
    })
    const api = stubApi({
      getSkillLibrary: async () => ({ skills: [skill], catalogDigest: 'catalog', warnings: [] }),
      getSkillLibraryDetail,
    })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(SkillsLibrary, { store, api, name: 'scout' })

    const skillButton = view.element.querySelector<HTMLButtonElement>('button[aria-label*="Release review"]')
    expect(skillButton).toBeTruthy()
    expect(skillButton!.hasAttribute('aria-haspopup')).toBe(false)
    skillButton!.click()
    await settleVue(8)
    expect(text(view.element)).toContain('Temporary detail read failure')

    const retry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Retry')
    expect(retry).toBeTruthy()
    retry!.click()
    await settleVue(8)

    expect(getSkillLibraryDetail).toHaveBeenCalledTimes(2)
    expect(text(view.element)).toContain('Check the changelog.')
    view.unmount()
  })

  it('preserves the original YAML document when exporting and saving an unchanged edit', async () => {
    const sourceDocument = [
      '---',
      '"name": release-review',
      '"description": >-',
      '  Check the current changes',
      '  and preserve evidence.',
      'license: Apache-2.0',
      'metadata:',
      '  maintained-by: platform',
      '---',
      '',
      '# Review steps',
      '',
    ].join('\r\n')
    const skill = {
      id: 'workspace:release-review',
      name: 'Release review',
      description: 'Check the current changes and preserve evidence.',
      scope: 'workspace',
      packageName: 'release-review',
      enabled: true,
      editable: true,
      resources: [],
    }
    const detail = {
      ...skill,
      instructions: 'Fallback text must not replace the source document.',
      document: sourceDocument,
      resources: [],
    }
    let savedDocument = ''
    const fetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'GET') {
        return new Response(JSON.stringify({
          apiVersion: 'agents.railgrid.ai/v1alpha1',
          kind: 'Skill',
          metadata: { name: 'release-review', resourceVersion: '7' },
          spec: { document: sourceDocument, resources: [] },
        }))
      }
      const saved = JSON.parse(String(init?.body)) as { spec: { document: string } }
      savedDocument = saved.spec.document
      return new Response(JSON.stringify(saved))
    })
    const api = stubApi({
      context: () => ({ tenant: 'workspace-cluster', fetch }),
      getSkillLibrary: async () => ({ skills: [skill], catalogDigest: '', warnings: [] }),
      getSkillLibraryDetail: async () => detail,
    })
    const store = makeStore(api)
    const originalCreateObjectURL = URL.createObjectURL
    const originalRevokeObjectURL = URL.revokeObjectURL
    let exportedBlob: Blob | undefined
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      value: vi.fn((blob: Blob) => { exportedBlob = blob; return 'blob:skill-export' }),
    })
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: vi.fn() })

    const view = await mountVue(SkillsLibrary, { store, api })
    try {
      const findButton = (label: string) => [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === label)
      view.element.querySelector<HTMLButtonElement>('button[aria-label*="Release review"]')!.click()
      await settleVue(8)

      findButton('Export')!.click()
      expect(exportedBlob).toBeTruthy()
      const exportedText = await new Promise<string>((resolve, reject) => {
        const reader = new FileReader()
        reader.onload = () => resolve(String(reader.result))
        reader.onerror = () => reject(reader.error)
        reader.readAsText(exportedBlob!)
      })
      expect(JSON.parse(exportedText).spec.document).toBe(sourceDocument)

      findButton('Edit')!.click()
      await settleVue()
      const editor = view.element.querySelector<HTMLTextAreaElement>('.agents-skill-workbench__document-editor')!
      expect(editor.value).toBe(sourceDocument.replace(/\r\n/gu, '\n'))

      findButton('Save skill')!.click()
      await settleVue(12)
      expect(savedDocument).toBe(sourceDocument)
    } finally {
      view.unmount()
      if (originalCreateObjectURL) Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: originalCreateObjectURL })
      else Reflect.deleteProperty(URL, 'createObjectURL')
      if (originalRevokeObjectURL) Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: originalRevokeObjectURL })
      else Reflect.deleteProperty(URL, 'revokeObjectURL')
    }
  })

  it('locks the draft while a slow create is pending so a second draft cannot be discarded', async () => {
    const createResponse = deferred<Response>()
    const requestStarted = deferred<void>()
    let submittedBody: Record<string, unknown> | undefined
    const fetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      submittedBody = JSON.parse(String(init?.body)) as Record<string, unknown>
      requestStarted.resolve()
      return createResponse.promise
    })
    const api = stubApi({
      context: () => ({ tenant: 'workspace-cluster', fetch }),
      getSkillLibrary: async () => ({ skills: [], catalogDigest: '', warnings: [] }),
    })
    const store = makeStore(api)
    const view = await mountVue(SkillsLibrary, { store, api })
    const findButton = (label: string) => [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === label)

    findButton('New skill')!.click()
    await view.setProps({ createRoute: true })
    await settleVue()
    const authoredDocument = '---\nname: authored-skill\ndescription: Preserve this draft.\n---\nKeep the author text intact.\n'
    const editor = view.element.querySelector<HTMLTextAreaElement>('.agents-skill-workbench__document-editor')!
    editor.value = authoredDocument
    editor.dispatchEvent(new Event('input', { bubbles: true }))
    await settleVue()
    findButton('Create skill')!.click()
    await requestStarted.promise
    await settleVue()

    expect(submittedBody).toMatchObject({
      metadata: { name: 'authored-skill' },
      spec: { document: authoredDocument },
    })
    expect(findButton('Cancel')?.disabled).toBe(true)
    expect(editor.disabled).toBe(true)
    expect(view.element.querySelector<HTMLInputElement>('input[placeholder="my-skill"]')?.disabled).toBe(true)

    findButton('Cancel')!.click()
    await settleVue()
    expect(view.element.querySelector('.agents-skill-workbench__document-editor')).toBe(editor)
    expect(editor.value).toBe(authoredDocument)

    createResponse.resolve(new Response(JSON.stringify({ metadata: { name: 'authored-skill' } }), { status: 201 }))
    await settleVue(12)

    await view.setProps({ createRoute: false })
    expect(view.element.querySelector('.agents-skill-workbench__document-editor')).toBeNull()
    expect(text(view.element)).toContain('Choose a skill to review')
    view.unmount()
  })

  it('shows a confirmed create after browser history changes without navigating away', async () => {
    const response = deferred<Response>()
    const fetch = vi.fn(() => response.promise)
    const api = stubApi({ context: () => ({ tenant: 'workspace-cluster', fetch }), getSkillLibrary: async () => ({ skills: [], warnings: [] }) })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api, createRoute: true })
    const button = (label: string) => [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => text(item) === label)!
    button('Create skill').click()
    await settleVue()
    await view.setProps({ createRoute: false })
    await view.setProps({ createRoute: true })
    response.resolve(new Response('{}', { status: 201 }))
    await settleVue(12)
    expect(view.navigations).toEqual([])
    expect(text(view.element)).toContain('was created. Return to the library')
    expect(button('Create skill').disabled).toBe(true)
    view.unmount()
  })

  it('reports a late create failure without attaching it to a revisited draft', async () => {
    const response = deferred<Response>()
    const api = stubApi({ context: () => ({ tenant: 'workspace-cluster', fetch: () => response.promise }), getSkillLibrary: async () => ({ skills: [], warnings: [] }) })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api, createRoute: true })
    const submit = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Create skill')!
    submit.click()
    await settleVue()
    await view.setProps({ createRoute: false })
    await view.setProps({ createRoute: true })
    response.reject(new Error('connection lost'))
    await settleVue(12)
    expect(text(view.element)).not.toContain('Could not save skill:')
    expect(text(document.querySelector('#k-toasts'))).toContain('Could not confirm skill creation: connection lost')
    expect([...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Create skill')!.disabled).toBe(false)
    view.unmount()
  })

  it('does not replace a new create draft when a prior result catalog reload finishes', async () => {
    const catalog = deferred<{ skills: never[]; warnings: string[] }>()
    const getSkillLibrary = vi.fn().mockResolvedValueOnce({ skills: [], warnings: [] }).mockReturnValueOnce(catalog.promise)
    const api = stubApi({ context: () => ({ tenant: 'workspace-cluster', fetch: vi.fn(async () => new Response('{}', { status: 201 })) }), getSkillLibrary })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api, createRoute: true })
    const button = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(item => text(item) === 'Create skill')!
    button.click()
    await settleVue(8)
    expect(view.navigations).toEqual([{ kind: 'menu', menu: 'skills' }])
    await view.setProps({ createRoute: false })
    await view.setProps({ createRoute: true })
    catalog.resolve({ skills: [], warnings: [] })
    await settleVue(12)
    expect(view.element.querySelector('.agents-skill-workbench__document-editor')).not.toBeNull()
    expect(view.navigations).toEqual([{ kind: 'menu', menu: 'skills' }])
    view.unmount()
  })

  it('does not show an import error after the creation route changes', async () => {
    const fileText = deferred<string>()
    const api = stubApi({ getSkillLibrary: async () => ({ skills: [], warnings: [] }) })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api, createRoute: false })
    const input = view.element.querySelector<HTMLInputElement>('input[type="file"]')!
    const file = new File([''], 'skill.md', { type: 'text/markdown' })
    Object.defineProperty(file, 'text', { value: () => fileText.promise })
    Object.defineProperty(input, 'files', { configurable: true, value: [file] })
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await settleVue()
    await view.setProps({ createRoute: true })
    fileText.reject(new Error('late import failure'))
    await settleVue(8)
    expect(document.body.textContent).not.toContain('late import failure')
    expect(view.navigations).toEqual([])
    expect(view.element.querySelector('.agents-skill-workbench__document-editor')).not.toBeNull()
    view.unmount()
  })

  it('keeps a slow import from overwriting the current draft after authority changes', async () => {
    const fileText = deferred<string>()
    const api = stubApi()
    const store = makeStore(api)
    const view = await mountVue(SkillsLibrary, { store, api })
    const previousHash = location.hash
    try {
      const input = view.element.querySelector<HTMLInputElement>('input[type="file"]')!
      const file = new File([''], 'late-import.md', { type: 'text/markdown' })
      Object.defineProperty(file, 'text', { configurable: true, value: () => fileText.promise })
      Object.defineProperty(input, 'files', { configurable: true, value: [file] })
      input.dispatchEvent(new Event('change', { bubbles: true }))
      await settleVue()

      expect(view.element.querySelector<HTMLButtonElement>('button[aria-busy="true"]')?.disabled).toBe(true)
      location.hash = '#/agents/another-route'
      fileText.resolve('---\nname: stale-import\ndescription: Must not replace the current draft.\n---\n')
      await settleVue(8)

      expect(view.element.querySelector('.agents-skill-workbench__document-editor')).toBeNull()
      expect(text(view.element)).toContain('Choose a skill to review')
    } finally {
      location.hash = previousHash
      view.unmount()
    }
  })
})
