import { afterEach, describe, expect, it, vi } from 'vitest'
import AgentChat from '../views/AgentChat.vue'
import { agentChatDraftStorageKey, writeAgentChatDraft } from '../chatDraft'
import type { SessionMeta } from '../types'
import { agentFixture, composerEditor, composerText, makeStore, setComposerText, stubApi } from './helpers'
import { mountVue, settleVue, type MountedVue } from './vue-helper'

const mounted: MountedVue[] = []
const authority = { usable: true, tenantKey: 'org/ws', userKey: 'alice@example.test', token: 'fixture-token' }
const rememberedSessionKey = 'railgrid:agents:session:org:ws:scout'

afterEach(() => {
  while (mounted.length) mounted.pop()?.unmount()
})

async function settle(passes = 6): Promise<void> {
  await settleVue(passes, 1)
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function session(id: string): SessionMeta {
  return { id, preview: id, messageCount: 1, createdAt: '', lastActivity: '' }
}

async function mountChat(overrides: Record<string, unknown> = {}) {
  const api = stubApi({ listRuns: async () => ({ items: [] }), contextAuthority: () => authority, ...overrides })
  const store = makeStore(api)
  store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'gpt-test' }]
  store.credentials.loaded = store.credentials.hasSnapshot = true
  store.agents.data = [agentFixture('scout')]
  store.agents.loaded = true
  const view = await mountVue(AgentChat, { store, api, name: 'scout' })
  mounted.push(view)
  await settle(3)
  return { api, store, view, el: view.element }
}

function dispatchFile(el: HTMLElement): File {
  const input = el.querySelector<HTMLInputElement>('.agents-composer input[type="file"]')!
  const file = new File(['notes'], 'notes.txt', { type: 'text/plain' })
  Object.defineProperty(input, 'files', { configurable: true, value: [file] })
  input.dispatchEvent(new Event('change', { bubbles: true }))
  return file
}

describe('chat bootstrap input readiness', () => {
  it('holds text and file input until the remembered session draft is restored', async () => {
    const bootstrap = deferred<SessionMeta[]>()
    const chatStream = vi.fn(async function* () {})
    const uploadAgentAttachment = vi.fn()
    const savedSession = 'remembered-session'
    localStorage.setItem(rememberedSessionKey, savedSession)
    writeAgentChatDraft(agentChatDraftStorageKey(authority, 'scout', savedSession), {
      content: 'saved draft', contentParts: [], skills: [], resources: [],
    })

    const { el } = await mountChat({
      listSessions: () => bootstrap.promise,
      chatStream,
      uploadAgentAttachment,
    })

    const editor = composerEditor(el)!
    expect(editor.getAttribute('contenteditable')).toBe('false')
    expect(el.querySelector<HTMLButtonElement>('.k-ai-composer__add-control button')?.disabled).toBe(true)
    expect(el.querySelector<HTMLButtonElement>('.agents-composer-primary')?.disabled).toBe(true)
    expect(el.querySelector('.agents-composer-readiness')?.textContent).toContain('Loading conversations')

    // Exercise the event handlers as well as the disabled UI: neither a forced
    // submit nor a synthetic file change may target the empty bootstrap scope.
    setComposerText(el, 'too early')
    el.querySelector<HTMLFormElement>('.agents-composer')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    dispatchFile(el)
    await settle(3)
    expect(chatStream).not.toHaveBeenCalled()
    expect(uploadAgentAttachment).not.toHaveBeenCalled()
    expect(localStorage.getItem(rememberedSessionKey)).toBe(savedSession)

    bootstrap.resolve([session(savedSession)])
    await settle(10)

    expect(editor.getAttribute('contenteditable')).toBe('true')
    expect(composerText(el)).toBe('saved draft')
    expect(uploadAgentAttachment).not.toHaveBeenCalled()
  })

  it('lets New thread take ownership while the original bootstrap read is pending', async () => {
    const bootstrap = deferred<SessionMeta[]>()
    const uploadAgentAttachment = vi.fn(async (_agent: string, _session: string, file: File) => ({
      id: 'draft-receipt',
      filename: file.name,
      contentType: file.type,
      sizeBytes: file.size,
      sha256: 'b'.repeat(64),
      createdAt: '2026-11-06T00:00:00Z',
    }))
    const { el } = await mountChat({
      listSessions: () => bootstrap.promise,
      uploadAgentAttachment,
    })

    const create = el.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!
    expect(create.disabled).toBe(false)
    create.click()
    await settle(5)

    const newSession = localStorage.getItem(rememberedSessionKey)
    expect(newSession).toBeTruthy()
    expect(newSession).not.toBe('')
    expect(composerEditor(el)?.getAttribute('contenteditable')).toBe('true')
    setComposerText(el, 'my new thread draft')
    await settle()

    bootstrap.resolve([session('stale-remembered-session')])
    await settle(8)

    expect(localStorage.getItem(rememberedSessionKey)).toBe(newSession)
    expect(composerText(el)).toBe('my new thread draft')

    const file = dispatchFile(el)
    await settle(8)
    expect(uploadAgentAttachment).toHaveBeenCalledTimes(1)
    expect(uploadAgentAttachment.mock.calls[0][1]).toBe(newSession)
    expect(uploadAgentAttachment.mock.calls[0][2]).toBe(file)
  })

  it('reruns bootstrap after an initial session-list failure and enables input on recovery', async () => {
    const listSessions = vi.fn()
      .mockRejectedValueOnce(new Error('sessions unavailable'))
      .mockResolvedValueOnce([session('recovered-session')])
    const { el } = await mountChat({ listSessions })

    expect(el.querySelector('[role="alert"]')?.textContent).toContain('Could not load conversations')
    expect(composerEditor(el)?.getAttribute('contenteditable')).toBe('false')

    el.querySelector<HTMLButtonElement>('.agents-state-error button')!.click()
    await settle(10)

    expect(listSessions).toHaveBeenCalledTimes(2)
    expect(localStorage.getItem(rememberedSessionKey)).toBe('recovered-session')
    expect(composerEditor(el)?.getAttribute('contenteditable')).toBe('true')
    expect(el.querySelector('.agents-state-error')).toBeNull()
  })
})
