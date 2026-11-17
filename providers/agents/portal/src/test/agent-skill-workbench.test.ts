import { describe, expect, it, vi } from 'vitest'
import SkillsLibrary from '../views/SkillsLibrary.vue'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text } from './vue-helper'

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}

describe('Workspace skills library', () => {
  it('keeps returned rows usable and replaces a warning-bearing result after retry', async () => {
    const skill = { id: 'workspace:review', name: 'Review', description: 'Review changes.', scope: 'workspace', enabled: false, editable: true, resources: [] }
    const retryResult = deferred<{ skills: typeof skill[]; warnings: string[] }>()
    const getSkillLibrary = vi.fn()
      .mockResolvedValueOnce({ skills: [skill], warnings: ['Provider skills could not be loaded. Retry to refresh the library.'] })
      .mockImplementationOnce(() => retryResult.promise)
    const api = stubApi({ getSkillLibrary })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(SkillsLibrary, { store, api })

    try {
      expect(text(view.element)).toContain('Catalog needs attention.')
      expect(text(view.element)).toContain('Returned skills remain usable')
      expect(text(view.element)).toContain('1 listed')
      expect(view.element.querySelector('button[aria-label*="Review"]')).not.toBeNull()

      const retry = view.element.querySelector<HTMLButtonElement>('[aria-label="Retry"]')!
      retry.focus()
      retry.click()
      await settleVue(3)
      expect(retry.getAttribute('aria-busy')).toBe('true')
      expect(document.activeElement).toBe(retry)
      retry.click()
      await settleVue(2)
      expect(getSkillLibrary).toHaveBeenCalledTimes(2)

      retryResult.resolve({ skills: [skill], warnings: [] })
      await settleVue(8)
      expect(text(view.element)).not.toContain('Catalog needs attention.')
      expect(text(view.element)).toContain('1 skill')
      expect(document.activeElement).toBe(view.element.querySelector('[aria-label="Skill catalog"]'))
    } finally {
      view.unmount()
    }
  })

  it('retains selected details but pauses actions if a warning-bearing refresh omits the skill', async () => {
    const skill = {
      id: 'workspace:review', name: 'Review', description: 'Review changes.', scope: 'workspace',
      packageName: 'review', enabled: false, editable: true, resources: [], instructions: 'Review the rollout.',
    }
    const retryResult = deferred<{ skills: never[]; warnings: string[] }>()
    const getSkillLibrary = vi.fn()
      .mockResolvedValueOnce({ skills: [skill], warnings: ['Published skill could not be read.'] })
      .mockImplementationOnce(() => retryResult.promise)
    const api = stubApi({
      getSkillLibrary,
      getSkillLibraryDetail: async () => skill,
    })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(SkillsLibrary, { store, api })

    try {
      view.element.querySelector<HTMLButtonElement>('button[aria-label*="Review"]')!.click()
      await settleVue(8)
      expect(text(view.element)).toContain('Review the rollout.')

      const retry = view.element.querySelector<HTMLButtonElement>('[aria-label="Retry"]')!
      retry.click()
      await settleVue(3)
      retryResult.resolve({ skills: [], warnings: ['Provider skills could not be loaded.'] })
      await settleVue(8)

      expect(view.element.querySelector('[aria-label="Skill details"]')?.textContent).toContain('Skill not listed in the latest catalog response.')
      expect(view.element.querySelector('[aria-label="Skill details"]')?.textContent).toContain('Review the rollout.')
      expect(view.element.querySelector('button[aria-label="Delete workspace skill"]')?.hasAttribute('disabled')).toBe(true)
      expect(view.element.querySelector('button')?.textContent).not.toContain('Review')
      expect(view.element.querySelector('[aria-label="Skill catalog"] .skill-catalog-empty-text')?.textContent).toContain('No skills were listed in this response.')
    } finally {
      view.unmount()
    }
  })

  it('keeps warning details and one focused retry when a catalog retry fails', async () => {
    const getSkillLibrary = vi.fn()
      .mockResolvedValueOnce({ skills: [], warnings: ['Workspace skills could not be loaded. Retry to refresh the library.'] })
      .mockRejectedValueOnce(new Error('Retry failed'))
    const api = stubApi({ getSkillLibrary })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(SkillsLibrary, { store, api })

    try {
      const warningRetry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.getAttribute('aria-label') === 'Retry')!
      warningRetry.focus()
      warningRetry.click()
      await settleVue(8)

      expect(text(view.element)).toContain('Retry failed')
      expect(text(view.element)).toContain('Workspace skills could not be loaded.')
      const retries = [...view.element.querySelectorAll<HTMLButtonElement>('button')].filter(button => button.getAttribute('aria-label') === 'Retry')
      expect(retries).toHaveLength(1)
      expect(document.activeElement).toBe(retries[0])
      expect(getSkillLibrary).toHaveBeenCalledTimes(2)
    } finally {
      view.unmount()
    }
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

  it('does not present first-load catalog failure as an authoritative empty result', async () => {
    const skill = { id: 'workspace:review', name: 'Review', description: 'Review changes.', scope: 'workspace', enabled: true, editable: true, resources: [] }
    const retryResult = deferred<{ skills: typeof skill[]; warnings: string[] }>()
    const getSkillLibrary = vi.fn()
      .mockRejectedValueOnce(new Error('Fixture catalog failure'))
      .mockImplementationOnce(() => retryResult.promise)
    const api = stubApi({ getSkillLibrary })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(SkillsLibrary, { store, api })

    try {
      const notice = view.element.querySelector('[role="alert"]')!
      expect(text(notice)).toContain('Could not load the skill library. Fixture catalog failure')
      expect(text(view.element)).not.toContain('No skills have been published in this workspace.')
      expect(view.element.querySelector('[aria-label="Skill catalog"] h3')).toBeNull()

      const retry = notice.querySelector<HTMLButtonElement>('button')!
      retry.focus()
      retry.click()
      await settleVue(3)

      expect(notice.isConnected).toBe(true)
      expect(text(notice)).toContain('Fixture catalog failure')
      expect(retry.getAttribute('aria-label')).toBe('Retrying catalog…')
      expect(retry.getAttribute('aria-busy')).toBe('true')
      expect(retry.getAttribute('aria-disabled')).toBe('true')
      expect(document.activeElement).toBe(retry)
      retry.click()
      await settleVue(2)
      expect(getSkillLibrary).toHaveBeenCalledTimes(2)

      retryResult.resolve({ skills: [skill], warnings: [] })
      await settleVue(8)

      expect(view.element.querySelector('[role="alert"]')).toBeNull()
      expect(view.element.querySelector('button[aria-label^="Review."]')).not.toBeNull()
      expect(document.activeElement).toBe(view.element.querySelector('[aria-label="Skill catalog"]'))
    } finally {
      view.unmount()
    }
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
    const retryDetail = deferred<typeof skill & { instructions: string; resources: never[] }>()
    const getSkillLibraryDetail = vi.fn(async () => {
      attempts += 1
      if (attempts === 1) throw new Error('Temporary detail read failure')
      return retryDetail.promise
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

    const notice = view.element.querySelector('[role="alert"]')!
    const retry = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Retry')
    expect(retry).toBeTruthy()
    retry!.focus()
    retry!.click()
    await settleVue(3)

    expect(notice.isConnected).toBe(true)
    expect(text(notice)).toContain('Temporary detail read failure')
    expect(retry!.getAttribute('aria-label')).toBe('Retrying detail…')
    expect(retry!.getAttribute('aria-busy')).toBe('true')
    expect(retry!.getAttribute('aria-disabled')).toBe('true')
    expect(document.activeElement).toBe(retry)
    retry!.click()
    await settleVue(2)
    expect(getSkillLibraryDetail).toHaveBeenCalledTimes(2)

    retryDetail.resolve({ ...skill, instructions: 'Check the changelog.', resources: [] })
    await settleVue(8)

    expect(getSkillLibraryDetail).toHaveBeenCalledTimes(2)
    expect(text(view.element)).toContain('Check the changelog.')
    expect(view.element.querySelector('[aria-label="Skill details"] [role="alert"]')).toBeNull()
    expect(document.activeElement).toBe(view.element.querySelector('[aria-label="Skill details"]'))
    view.unmount()
  })

  it('keeps the last detail visible but prevents editing after a same-skill refresh fails', async () => {
    const skill = { id: 'workspace:review', name: 'Review', description: '', scope: 'workspace', packageName: 'review', enabled: true, editable: true }
    const getSkillLibraryDetail = vi.fn()
      .mockResolvedValueOnce({ ...skill, instructions: 'Previous instructions.', version: '7', sourceUID: 'review-uid' })
      .mockRejectedValueOnce(new Error('Read failed'))
      .mockResolvedValueOnce({ ...skill, instructions: 'New instructions.', version: '8', sourceUID: 'review-uid' })
    const api = stubApi({ getSkillLibrary: async () => ({ skills: [skill] }), getSkillLibraryDetail })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api })
    try {
      const row = view.element.querySelector<HTMLButtonElement>('button[aria-label*="Review"]')!
      const edit = () => [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(el => text(el) === 'Edit')!
      row.click()
      await settleVue(8)
      expect(edit().disabled).toBe(false)
      row.click()
      await settleVue(8)
      expect(text(view.element)).toContain('Previous instructions.')
      expect(text(view.element)).toContain('Showing the last loaded details')
      expect(edit().disabled).toBe(true)
      view.element.querySelector<HTMLButtonElement>('[aria-label="Retry"]')!.click()
      await settleVue(8)
      expect(text(view.element)).toContain('New instructions.')
      expect(edit().disabled).toBe(false)
    } finally {
      view.unmount()
    }
  })

  it.each([
    { uid: 'review-uid', resourceVersion: '8', changesDuringSave: false },
    { uid: 'replacement-uid', resourceVersion: '7', changesDuringSave: false },
    { uid: 'review-uid', resourceVersion: '7', changesDuringSave: true },
  ])('keeps the draft when the source changes before or during save (%j)', async ({ changesDuringSave, ...metadata }) => {
    const document = '---\nname: Review\ndescription: Review evidence.\n---\nOriginal instructions.'
    const draft = document.replace('Original', 'Draft')
    const skill = { id: 'workspace:review', name: 'Review', description: 'Review evidence.', scope: 'workspace', packageName: 'review', enabled: true, editable: true }
    const fetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT' && changesDuringSave) {
        return Response.json({ kind: 'Status', status: 'Failure', reason: 'Conflict', code: 409 }, { status: 409 })
      }
      return Response.json({
        apiVersion: 'agents.railgrid.ai/v1alpha1',
        kind: 'Skill',
        metadata: { name: 'review', ...metadata },
        spec: { document: document.replace('Original', 'Other editor’s') },
      })
    })
    const api = stubApi({
      context: () => ({ tenant: 'workspace-cluster', fetch }),
      getSkillLibrary: async () => ({ skills: [skill] }),
      getSkillLibraryDetail: async () => ({ ...skill, document, instructions: 'Original instructions.', version: '7', sourceUID: 'review-uid' }),
    })
    const view = await mountVue(SkillsLibrary, { store: makeStore(api), api })
    try {
      const button = (label: string) => [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(el => text(el) === label)!
      view.element.querySelector<HTMLButtonElement>('button[aria-label*="Review"]')!.click()
      await settleVue(8)
      button('Edit').click()
      await settleVue()
      const editor = view.element.querySelector<HTMLTextAreaElement>('.agents-skill-workbench__document-editor')!
      editor.value = draft
      editor.dispatchEvent(new Event('input', { bubbles: true }))
      button('Save skill').click()
      await vi.waitFor(() => {
        expect(text(view.element)).toContain('This skill changed since you opened it.')
      })
      expect(fetch.mock.calls.map(([, init]) => init?.method)).toEqual(changesDuringSave ? ['GET', 'PUT'] : ['GET'])
      if (changesDuringSave) {
        expect(JSON.parse(String(fetch.mock.calls[1][1]?.body))).toMatchObject({ metadata: { uid: 'review-uid', resourceVersion: '7' } })
      }
      expect(editor.value).toBe(draft)
    } finally {
      view.unmount()
    }
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
      version: '7',
      sourceUID: 'review-uid',
      resources: [],
    }
    let savedDocument = ''
    const fetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'GET') {
        return new Response(JSON.stringify({
          apiVersion: 'agents.railgrid.ai/v1alpha1',
          kind: 'Skill',
          metadata: { name: 'release-review', uid: 'review-uid', resourceVersion: '7' },
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
    expect(findButton('New skill')?.disabled).toBe(true)
    expect(findButton('Import')?.disabled).toBe(true)
    expect(editor.disabled).toBe(true)
    expect(view.element.querySelector<HTMLInputElement>('input[placeholder="my-skill"]')?.disabled).toBe(true)

    findButton('New skill')!.click()
    await settleVue()
    expect(view.element.querySelector('.agents-skill-workbench__document-editor')).toBe(editor)
    expect(editor.value).toBe(authoredDocument)

    createResponse.resolve(new Response(JSON.stringify({ metadata: { name: 'authored-skill' } }), { status: 201 }))
    await settleVue(12)

    expect(view.element.querySelector('.agents-skill-workbench__document-editor')).toBeNull()
    expect(text(view.element)).toContain('Choose a skill to review')
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
