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
