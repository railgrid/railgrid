// Chat streaming: the delta → tool card → approval card → done lifecycle, plus
// the Stop button's cancel call and scroll-pinning behaviour.

import { defineComponent, h, ref, shallowRef } from 'vue'
import { provideAgentsRuntime } from '../vue/runtime'
import { afterEach, describe, expect, it, vi } from 'vitest'
import AgentChat from '../views/AgentChat.vue'
import ChatMessageView from '../views/ChatMessage.vue'
import { formatAIMessageAuthor, optimisticAIMessageActor } from '../agentkit/conversation'
import AIConversationRail from '../agentkit/AIConversationRail.vue'
import { resolveConfirm } from '../portalkit/confirm'
import { rebuildTranscript } from '../vue/chat'
import { agentChatDraftStorageKey, readAgentChatDraft } from '../chatDraft'
import { ApiError, type SSEEvent } from '../api'
import type { SessionMeta, TranscriptMessage } from '../types'
import { agentFixture, composerEditor, composerText, makeStore, modelBackend, setComposerText, stubApi } from './helpers'
import { mountVue, settleVue, text, type MountedVue } from './vue-helper'
import { queueAgentConfigSave } from '../vue/config-save-queue'

const mounted: MountedVue[] = []
afterEach(() => {
  while (mounted.length) mounted.pop()?.unmount()
})

async function settle(passes = 4): Promise<void> {
  await settleVue(passes, 1)
}

// scripted turns the given SSE frames into the async generator chatStream
// returns, with a `gate` promise so a test can hold the stream open.
function scripted(events: SSEEvent[], gate?: Promise<void>) {
  return async function* () {
    for (const ev of events) yield ev
    if (gate) await gate
  }
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

const session = (id: string, preview = id) => ({ id, preview, messageCount: 1, createdAt: '', lastActivity: '' })

function domRect(left: number, top: number, width: number, height: number): DOMRect {
  return {
    x: left,
    y: top,
    left,
    top,
    right: left + width,
    bottom: top + height,
    width,
    height,
    toJSON: () => ({}),
  } as DOMRect
}

async function mountConversationRail(): Promise<MountedVue> {
  const overlay = document.createElement('div')
  overlay.id = 'app-studio-overlay-root'
  document.body.appendChild(overlay)
  const rail = await mountVue(AIConversationRail, {
    threads: [
      { id: 'thread-active', title: 'Current work', status: 'active' },
      { id: 'thread-older', title: 'Previous incident review', status: 'idle' },
    ],
    activeThreadID: 'thread-active',
    unreadThreadIDs: ['thread-older'],
    pinnedThreadIDs: [],
    capabilities: { create: true, pin: true, unread: true, archive: true },
    overlayTarget: '#app-studio-overlay-root',
    panelId: 'app-studio-thread-rail',
    storageScope: 'app-studio-fixture',
  })
  mounted.push(rail)
  await settle(4)
  return rail
}

async function mountChat(chatStream: unknown, extra: Record<string, unknown> = {}) {
  const api = stubApi({ chatStream, listRuns: () => Promise.resolve({ items: [] }), ...extra })
  const store = makeStore(api)
  store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'gpt-test' }]
  store.credentials.loaded = store.credentials.hasSnapshot = true
  store.agents.data = [agentFixture('scout')]
  store.agents.loaded = true
  const view = await mountVue(AgentChat, { store, api, name: 'scout' })
  mounted.push(view)
  await settle(6)
  return { el: view.element, api, store, view }
}

async function send(el: HTMLElement, message: string): Promise<void> {
  setComposerText(el, message)
  await settle()
  el.querySelector<HTMLFormElement>('.agents-composer')!.dispatchEvent(new Event('submit', { cancelable: true }))
  await settle(6)
}

async function chooseSession(el: HTMLElement, label: string): Promise<void> {
  const option = [...el.querySelectorAll<HTMLButtonElement>('.k-ai-conversation-rail__item-select')]
    .find(candidate => text(candidate).includes(label))
  expect(option).toBeDefined()
  option!.click()
  await settle()
}

const draftAttachment = {
  id: 'attachment-one',
  filename: 'notes.txt',
  contentType: 'text/plain',
  sizeBytes: 5,
  sha256: 'a'.repeat(64),
  createdAt: '2026-11-05T00:00:00Z',
}
const attachmentAuthority = { usable: true, tenantKey: 'org/ws', userKey: 'alice@example.test', token: 'fixture-token' }

async function attachTextFile(el: HTMLElement): Promise<void> {
  const input = el.querySelector<HTMLInputElement>('.agents-composer input[type="file"]')!
  Object.defineProperty(input, 'files', {
    configurable: true,
    value: [new File(['notes'], draftAttachment.filename, { type: draftAttachment.contentType })],
  })
  input.dispatchEvent(new Event('change', { bubbles: true }))
  await settle(8)
}

describe('chat streaming', () => {
  it('orders threads by creation time and keeps a new thread first through its first turn', async () => {
    const older = { ...session('older'), createdAt: '2026-11-05T10:00:00Z', lastActivity: '2026-11-07T10:00:00Z' }
    const newer = { ...session('newer'), createdAt: '2026-11-06T10:00:00Z', lastActivity: '2026-11-06T10:00:00Z' }
    const finish = deferred<void>()
    let saved: SessionMeta | undefined
    async function* chatStream(_agent: string, _message: string, sessionID: string) {
      yield { event: 'start', data: { runID: 'first-run', sessionID } } as SSEEvent
      yield { event: 'run_started', data: { runID: 'first-run', sessionID, status: 'Running' } } as SSEEvent
      await finish.promise
      saved = { ...session(sessionID), createdAt: '2026-11-07T11:00:00Z', lastActivity: '2026-11-07T11:00:01Z' }
      yield { event: 'done', data: { runID: 'first-run', content: 'Reply' } } as SSEEvent
    }
    const { el } = await mountChat(chatStream, { listSessions: async () => saved ? [saved, older, newer] : [older, newer] })
    const threadIDs = () => [...el.querySelectorAll('.k-ai-conversation-rail__item-select')]
      .map(row => row.getAttribute('data-thread-id'))

    expect(threadIDs()).toEqual(['newer', 'older'])
    el.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!.click()
    await settle()
    const newID = threadIDs()[0]
    expect(newID).toBeTruthy()
    expect(threadIDs()).toEqual([newID, 'newer', 'older'])

    await send(el, 'First message')
    try {
      expect(el.querySelector(`[data-thread-id="${newID}"]`)?.getAttribute('aria-busy')).toBe('true')
      expect(threadIDs()).toEqual([newID, 'newer', 'older'])
    } finally {
      finish.resolve()
    }
    await settle(8)
    expect(threadIDs()).toEqual([newID, 'newer', 'older'])
  })

  it('double-clicks a thread-list row to rename after selection finishes', async () => {
    const rename = vi.fn()
    const select = vi.fn()
    const rail = await mountConversationRail()
    await rail.setProps({ capabilities: { rename: true }, onRename: rename, onSelect: select })
    const row = rail.element.querySelector<HTMLButtonElement>('[data-thread-id="thread-older"]')!
    row.click()
    expect(select).toHaveBeenCalledExactlyOnceWith('thread-older')
    await rail.setProps({ selectingThreadID: 'thread-older' })
    expect(row.disabled).toBe(false)
    row.dispatchEvent(new MouseEvent('dblclick', { bubbles: true, cancelable: true, detail: 2 }))
    expect(rename).not.toHaveBeenCalled()
    await rail.setProps({ activeThreadID: 'thread-older', selectingThreadID: '' })
    expect(rename).toHaveBeenCalledExactlyOnceWith('thread-older')
  })

  it('shows a trailing spinner for active work without preventing thread selection', async () => {
    const rail = await mountConversationRail()
    await rail.setProps({ activeThreadID: '', unreadThreadIDs: ['thread-active', 'thread-older'] })
    const active = rail.element.querySelector<HTMLButtonElement>('[data-thread-id="thread-active"]')!
    const idle = rail.element.querySelector<HTMLButtonElement>('[data-thread-id="thread-older"]')!
    expect(active.getAttribute('aria-busy')).toBe('true')
    expect(active.closest('.k-ai-conversation-rail__item')?.classList.contains('is-running')).toBe(true)
    expect(active.querySelector('svg[aria-label="Running"]')).not.toBeNull()
    expect(active.querySelector('[aria-label="Unread thread"]')).toBeNull()
    expect(idle.querySelector('[aria-label="Unread thread"]')).not.toBeNull()
    expect(idle.querySelector('.k-ai-conversation-rail__item-spinner')).toBeNull()
    expect(active.disabled).toBe(false)
    await rail.setProps({ threads: [{ id: 'thread-active', title: 'Current work', status: 'waiting' }, { id: 'thread-older', title: 'Previous incident review' }] })
    expect(active.querySelector('.k-ai-conversation-rail__item-spinner')).toBeNull()
    expect(active.hasAttribute('aria-busy')).toBe(false)
    expect(active.closest('.k-ai-conversation-rail__item')?.classList.contains('is-running')).toBe(false)
    await rail.setProps({ threads: [{ id: 'thread-active', title: 'Current work', status: 'idle' }, { id: 'thread-older', title: 'Previous incident review' }] })
    expect(active.querySelector('[aria-label="Unread thread"]')).not.toBeNull()
  })

  it('marks background updates unread, persists them, and clears them when opened', async () => {
    const initial = '2026-11-07T10:00:00Z'
    let activity = initial
    const listSessions = async () => [
      { ...session('read-current', 'Current conversation'), lastActivity: initial },
      { ...session('read-background', 'Background conversation'), lastActivity: activity },
    ]
    const first = await mountChat(scripted([]), { listSessions })
    const dot = (el: HTMLElement) => el.querySelector('[data-thread-id="read-background"] .k-ai-conversation-rail__unread[aria-label="Unread thread"]')
    expect(dot(first.el)).toBeNull()
    activity = '2026-11-07T10:01:00Z'
    first.store.dispatchEvent(new CustomEvent('server', {
      detail: { type: 'run', data: { id: 'background-completed', agent: 'scout', sessionID: 'read-background', phase: 'Succeeded' } },
    }))
    await settle(6)
    expect(dot(first.el)).not.toBeNull()
    first.view.unmount()
    const second = await mountChat(scripted([]), { listSessions })
    expect(dot(second.el)).not.toBeNull()
    await chooseSession(second.el, 'Background conversation')
    expect(dot(second.el)).toBeNull()
    await chooseSession(second.el, 'Current conversation')
    expect(dot(second.el)).toBeNull()
  })

  it('keeps updates to the open conversation read and scopes unread state per agent', async () => {
    let activity = '2026-11-07T11:00:00Z'
    const listSessions = async () => [
      { ...session('read-open', 'Open conversation'), lastActivity: activity },
      { ...session('read-other', 'Other conversation'), lastActivity: activity },
    ]
    const { el, store, view } = await mountChat(scripted([]), { listSessions })
    activity = '2026-11-07T11:01:00Z'
    store.dispatchEvent(new CustomEvent('server', {
      detail: { type: 'run', data: { id: 'other-completed', agent: 'scout', sessionID: 'read-other', phase: 'Succeeded' } },
    }))
    await settle(6)
    expect(el.querySelector('[data-thread-id="read-open"] [aria-label="Unread thread"]')).toBeNull()
    expect(el.querySelector('[data-thread-id="read-other"] [aria-label="Unread thread"]')).not.toBeNull()
    await view.setProps({ name: 'nova' })
    await settle(6)
    expect(el.querySelector('[aria-label="Unread thread"]')).toBeNull()
  })

  it('persists thread pins per agent and keeps delete available beside pin', async () => {
    const listSessions = async () => [session('pinned', 'Pinned conversation')]
    const first = await mountChat(scripted([]), { listSessions })
    const row = first.el.querySelector('[data-thread-id="pinned"]')!.closest('.k-ai-conversation-rail__item')!
    expect(row.querySelector('button[aria-label="Delete chat"]')).not.toBeNull()
    row.querySelector<HTMLButtonElement>('button[aria-label="Pin thread"]')!.click()
    await settle()
    expect(first.el.querySelector('button[aria-label="Unpin thread"]')).not.toBeNull()
    first.view.unmount()
    const second = await mountChat(scripted([]), { listSessions })
    expect(second.el.querySelector('button[aria-label="Unpin thread"]')).not.toBeNull()
    await second.view.setProps({ name: 'another-agent' })
    await settle()
    expect(second.el.querySelector('button[aria-label="Unpin thread"]')).toBeNull()
  })

  it('opens the rename modal from the title and saves only on submission', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const renameSession = vi.fn(async (_agent: string, _id: string, title: string) => ({ title }))
    const { el } = await mountChat(scripted([]), {
      listSessions: async () => [session('s1', 'Original title')], renameSession,
    })
    const title = el.querySelector<HTMLButtonElement>('button[aria-label="Rename chat"]')!
    title.focus()
    title.click()
    await settle()
    const dialog = document.querySelector<HTMLFormElement>('[role="dialog"]')!
    const input = dialog.querySelector<HTMLInputElement>('input')!
    expect(document.activeElement).toBe(input)
    expect(input.value).toBe('Original title')
    expect(el.querySelector('.k-ai-conversation-title-input')).toBeNull()
    input.value = '  My custom title  '
    input.dispatchEvent(new Event('input'))
    input.blur()
    await settle()
    expect(renameSession).not.toHaveBeenCalled()
    dialog.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle()
    expect(renameSession).toHaveBeenCalledExactlyOnceWith('scout', 's1', 'My custom title')
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(document.activeElement).toBe(title)
    expect(text(el.querySelector('.agents-chat-title'))).toContain('My custom title')
    expect(text(el.querySelector('[data-thread-id="s1"]'))).toContain('My custom title')
  })

  it('keeps failed title edits in the modal for retry and blocks duplicate saves', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const pending = deferred<{ title: string }>()
    const renameSession = vi.fn().mockImplementationOnce(() => pending.promise).mockResolvedValueOnce({ title: 'New title' })
    const { el } = await mountChat(scripted([]), { listSessions: async () => [session('s1', 'Original')], renameSession })
    el.querySelector<HTMLButtonElement>('button[aria-label="Rename chat"]')!.click()
    await settle()
    const dialog = document.querySelector<HTMLFormElement>('[role="dialog"]')!
    const input = dialog.querySelector<HTMLInputElement>('input')!
    input.value = 'New title'
    input.dispatchEvent(new Event('input'))
    dialog.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle()
    dialog.dispatchEvent(new Event('submit', { cancelable: true }))
    expect(renameSession).toHaveBeenCalledTimes(1)
    pending.reject(new Error('Unable to save'))
    await settle()
    expect(text(dialog.querySelector('[role="alert"]'))).toContain('Unable to save')
    expect(input.value).toBe('New title')
    dialog.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle()
    expect(renameSession).toHaveBeenCalledTimes(2)
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  })

  it('replaces a prior local rename with the persisted title after reconnect', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    let persistedTitle = 'Original'
    const listSessions = vi.fn(async () => [{ ...session('s1'), title: persistedTitle }])
    const renameSession = vi.fn(async (_agent: string, _id: string, title: string) => { persistedTitle = title; return { title } })
    const { el, store } = await mountChat(scripted([]), { listSessions, renameSession })
    store.live = true
    store.dispatchEvent(new Event('change'))
    await settle()
    el.querySelector<HTMLButtonElement>('button[aria-label="Rename chat"]')!.click()
    await settle()
    const input = document.querySelector<HTMLInputElement>('[role="dialog"] input')!
    input.value = 'My first rename'
    input.dispatchEvent(new Event('input'))
    document.querySelector('[role="dialog"]')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle()
    expect(text(el.querySelector('.agents-chat-title'))).toContain('My first rename')

    store.live = false
    store.dispatchEvent(new Event('change'))
    await settle()
    persistedTitle = 'Renamed in another client'
    const callsBefore = listSessions.mock.calls.length
    store.live = true
    store.dispatchEvent(new Event('change'))
    await settle()
    expect(listSessions.mock.calls.length).toBeGreaterThan(callsBefore)
    expect(text(el.querySelector('.agents-chat-title'))).toContain(persistedTitle)
    expect(text(el.querySelector('[data-thread-id="s1"]'))).toContain(persistedTitle)
  })

  it('keeps a repeated title event newer than an in-flight session snapshot', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const listSessions = vi.fn(async (): Promise<SessionMeta[]> => [session('s1', 'Original')])
    const { el, store } = await mountChat(scripted([]), { listSessions })
    store.live = true
    store.dispatchEvent(new Event('change'))
    await settle()
    const titleEvent = { type: 'session_title', data: { agent: 'scout', sessionID: 's1', title: 'Latest title' } }
    store.applyServerEvent(titleEvent)
    await settle()
    store.live = false
    store.dispatchEvent(new Event('change'))
    await settle()
    const pending = deferred<SessionMeta[]>()
    listSessions.mockImplementationOnce(() => pending.promise)
    const callsBefore = listSessions.mock.calls.length
    store.live = true
    store.dispatchEvent(new Event('change'))
    await settle()
    expect(listSessions.mock.calls.length).toBe(callsBefore + 1)
    // Even an event with the same text must invalidate the older snapshot.
    store.applyServerEvent(titleEvent)
    await settle()
    pending.resolve([{ ...session('s1'), title: 'Older snapshot' }])
    await settle()
    expect(text(el.querySelector('.agents-chat-title'))).toContain('Latest title')
    expect(text(el.querySelector('[data-thread-id="s1"]'))).toContain('Latest title')
  })

  it('disables heading and menu renaming until a draft has a persisted message', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    let messageCount = 0
    const renameSession = vi.fn()
    const { el, store } = await mountChat(scripted([]), {
      listSessions: async () => [{ ...session('s1', ''), messageCount }], renameSession,
    })
    const title = el.querySelector<HTMLButtonElement>('button[aria-label="Rename chat"]')!
    expect(title.disabled).toBe(true)
    expect(title.title).toBe('Send a message before renaming this chat')
    title.click()
    await settle()
    expect(el.querySelector('.k-ai-conversation-title-input')).toBeNull()
    const row = el.querySelector('[data-thread-id="s1"]')!.closest('.k-ai-conversation-rail__item')!
    row.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true }))
    await settle()
    const menuItem = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(item => text(item) === 'Rename')!
    expect(menuItem.disabled).toBe(true)
    expect(menuItem.title).toBe('Send a message before renaming this chat')
    menuItem.click()
    await settle()
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(renameSession).not.toHaveBeenCalled()
    document.querySelector('[role="menu"]')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    store.live = true
    store.dispatchEvent(new Event('change'))
    await settle()
    store.live = false
    store.dispatchEvent(new Event('change'))
    await settle()
    messageCount = 1
    store.live = true
    store.dispatchEvent(new Event('change'))
    await settle()
    expect(title.disabled).toBe(false)
    expect(title.classList.contains('k-ai-conversation-title')).toBe(true)
    expect(title.classList.contains('k-btn--ghost')).toBe(false)
    title.click()
    await settle()
    expect(document.querySelector('[role="dialog"] input')).not.toBeNull()
  })

  it('renames a different thread through its context menu and retries a failed save', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const renameSession = vi.fn().mockRejectedValueOnce(new Error('Try again')).mockResolvedValueOnce({ title: 'Renamed thread' })
    const { el } = await mountChat(scripted([]), {
      listSessions: async () => [session('s1', 'Current chat'), session('s2', 'Other chat')], renameSession,
    })
    const row = el.querySelector('[data-thread-id="s2"]')!.closest('.k-ai-conversation-rail__item')!
    row.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 100, clientY: 100 }))
    await settle()
    const rename = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(item => text(item) === 'Rename')!
    expect(rename).toBeDefined()
    rename.click()
    await settle()
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!
    const input = dialog.querySelector<HTMLInputElement>('input')!
    expect(document.activeElement).toBe(input)
    expect(input.value).toBe('Other chat')
    input.value = 'Renamed thread'
    input.dispatchEvent(new Event('input'))
    dialog.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle()
    expect(text(dialog.querySelector('[role="alert"]'))).toContain('Try again')
    expect(input.value).toBe('Renamed thread')
    dialog.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle()
    expect(renameSession).toHaveBeenLastCalledWith('scout', 's2', 'Renamed thread')
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(text(el.querySelector('.agents-chat-title'))).toContain('Current chat')
    expect(text(el.querySelector('[data-thread-id="s2"]'))).toContain('Renamed thread')
  })

  it('cancels modal renaming with Escape without saving', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const renameSession = vi.fn()
    const { el } = await mountChat(scripted([]), { listSessions: async () => [session('s1', 'Original')], renameSession })
    el.querySelector<HTMLButtonElement>('button[aria-label="Rename chat"]')!.click()
    await settle()
    document.querySelector('[role="dialog"] input')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await settle()
    expect(el.querySelector('.k-ai-conversation-title-input')).toBeNull()
    expect(renameSession).not.toHaveBeenCalled()
    expect(text(el.querySelector('.agents-chat-title'))).toContain('Original')
  })

  it('prefers an async generated title in the header and rail after the reply finishes', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const preview = 'Investigate order 42'
    const getSessionModel = vi.fn().mockResolvedValue({ selectedCredential: 'openai', effortOptions: [], defaultEffort: '' })
    const saveSessionModel = vi.fn(async (_agent: string, _session: string, preferences: unknown) => preferences)
    const { el, store } = await mountChat(scripted([
      { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
      { event: 'done', data: { runID: 'r1', content: 'The order failed during validation.' } },
    ]), {
      listSessions: () => Promise.resolve([session('s1', preview)]),
      getSessionModel,
      saveSessionModel,
    })

    await send(el, 'Investigate order 42')
    const header = el.querySelector('.agents-chat-title')!
    const railTitle = () => text(el.querySelector('[data-thread-id="s1"] .k-ai-conversation-rail__item-title'))
    expect(text(header)).toContain('Investigate order 42')
    expect(railTitle()).toBe('Investigate order 42')

    const preferenceReadCount = getSessionModel.mock.calls.length
    store.applyServerEvent({
      type: 'session_title',
      data: { agent: 'scout', sessionID: 's1', title: '  Order 42 validation failure  ' },
    })
    await settle()

    expect(text(header)).toContain('Order 42 validation failure')
    expect(railTitle()).toBe('Order 42 validation failure')
    expect(getSessionModel).toHaveBeenCalledTimes(preferenceReadCount)
    expect(saveSessionModel).not.toHaveBeenCalled()
  })

  it('keeps the first-message preview when title generation produces no event', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const { el } = await mountChat(scripted([
      { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
      { event: 'done', data: { runID: 'r1', content: 'Reply completed.' } },
    ]), { listSessions: () => Promise.resolve([session('s1', 'Review the deployment')]) })

    await send(el, 'Review the deployment')
    await settle()

    expect(text(el.querySelector('.agents-chat-title'))).toContain('Review the deployment')
    expect(text(el.querySelector('[data-thread-id="s1"] .k-ai-conversation-rail__item-title'))).toBe('Review the deployment')
  })

  it('reloads persisted titles when the first event stream connection opens', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    let persistedTitle = ''
    const listSessions = vi.fn(async (_agent: string): Promise<SessionMeta[]> => [{
      ...session('s1', 'Review the deployment'),
      ...(persistedTitle ? { title: persistedTitle } : {}),
    }])
    const api = stubApi({ listSessions })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = store.agents.hasSnapshot = true
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    await settle(6)

    expect(listSessions).toHaveBeenCalledTimes(1)
    expect(text(view.element.querySelector('.agents-chat-title'))).toContain('Review the deployment')

    // The title event was published before the event stream had a subscriber.
    // The first successful connection must catch up from the durable list.
    persistedTitle = 'Recovered on first connection'
    store.live = true
    store.dispatchEvent(new Event('change'))
    await settle(6)

    expect(listSessions).toHaveBeenCalledTimes(2)
    expect(text(view.element.querySelector('.agents-chat-title'))).toContain('Recovered on first connection')
    expect(text(view.element.querySelector('[data-thread-id="s1"] .k-ai-conversation-rail__item-title'))).toBe('Recovered on first connection')
  })

  it('reloads persisted titles after reconnect when the title event was missed', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const initialSessions = deferred<SessionMeta[]>()
    let persistedTitle = ''
    const listSessions = vi.fn(async (_agent: string): Promise<SessionMeta[]> => [{
      ...session('s1', 'Review the deployment'),
      ...(persistedTitle ? { title: persistedTitle } : {}),
    }])
    listSessions.mockImplementationOnce(() => initialSessions.promise)
    const api = stubApi({ listSessions })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = store.agents.hasSnapshot = true
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    await settle(4)

    expect(listSessions).toHaveBeenCalledTimes(1)
    // The title is durable by the time the first connection opens, but the
    // initial request's snapshot predates it. Do not overlap that request;
    // catch up once its stale result settles.
    persistedTitle = 'Recovered deployment review'
    store.live = true
    store.dispatchEvent(new Event('change'))
    await settle()
    expect(listSessions).toHaveBeenCalledTimes(1)

    initialSessions.resolve([session('s1', 'Review the deployment')])
    await settle(8)
    expect(listSessions).toHaveBeenCalledTimes(2)
    expect(text(view.element.querySelector('.agents-chat-title'))).toContain('Recovered deployment review')

    // A same-state store change is not a reconnect and does not reload the list.
    store.applyServerEvent({ type: 'run', data: { id: 'unrelated', agent: 'scout', phase: 'Running' } })
    await settle()
    expect(listSessions).toHaveBeenCalledTimes(2)

    // Reopening the stream refreshes the durable session list again.
    store.live = false
    store.dispatchEvent(new Event('change'))
    await settle()
    store.live = true
    store.dispatchEvent(new Event('change'))
    await settle(6)

    expect(listSessions).toHaveBeenCalledTimes(3)
    expect(text(view.element.querySelector('.agents-chat-title'))).toContain('Recovered deployment review')
    expect(text(view.element.querySelector('[data-thread-id="s1"] .k-ai-conversation-rail__item-title'))).toBe('Recovered deployment review')
  })

  it('ignores title events from the previous workspace and another agent', async () => {
    localStorage.removeItem('railgrid:agents:session:org-a:workspace-a:scout')
    localStorage.removeItem('railgrid:agents:session:org-b:workspace-b:scout')
    localStorage.removeItem('railgrid:agents:session:org-b:workspace-b:nova')
    const workspaceAApi = stubApi({
      tenant: () => ({ orgUUID: 'org-a', workspaceUUID: 'workspace-a' }),
      listSessions: async (agent: string) => [session('shared-session', `${agent} workspace A preview`)],
    })
    const workspaceAStore = makeStore(workspaceAApi)
    workspaceAStore.agents.data = [agentFixture('scout')]
    workspaceAStore.agents.loaded = workspaceAStore.agents.hasSnapshot = true
    const view = await mountVue(AgentChat, { store: workspaceAStore, api: workspaceAApi, name: 'scout' })
    mounted.push(view)
    await settle(6)

    const workspaceBApi = stubApi({
      tenant: () => ({ orgUUID: 'org-b', workspaceUUID: 'workspace-b' }),
      listSessions: async (agent: string) => [session('shared-session', `${agent} workspace B preview`)],
    })
    const workspaceBStore = makeStore(workspaceBApi)
    workspaceBStore.agents.data = [agentFixture('scout'), agentFixture('nova')]
    workspaceBStore.agents.loaded = workspaceBStore.agents.hasSnapshot = true
    await view.setProps({ store: workspaceBStore, api: workspaceBApi })
    await settle(6)

    workspaceAStore.applyServerEvent({
      type: 'session_title',
      data: { agent: 'scout', sessionID: 'shared-session', title: 'Old workspace title' },
    })
    await settle()
    expect(text(view.element.querySelector('.agents-chat-title'))).toContain('scout workspace B preview')
    expect(text(view.element.querySelector('[data-thread-id="shared-session"] .k-ai-conversation-rail__item-title'))).toBe('scout workspace B preview')

    await view.setProps({ name: 'nova' })
    await settle(6)
    workspaceBStore.applyServerEvent({
      type: 'session_title',
      data: { agent: 'scout', sessionID: 'shared-session', title: 'Another agent title' },
    })
    await settle()

    expect(text(view.element.querySelector('.agents-chat-title'))).toContain('nova workspace B preview')
    expect(text(view.element.querySelector('[data-thread-id="shared-session"] .k-ai-conversation-rail__item-title'))).toBe('nova workspace B preview')
  })

  it('rechecks native run activity after the delete confirmation', async () => {
    const deleteSession = vi.fn()
    let targetReads = 0
    const listRuns = vi.fn(async ({ session: id }: { session: string }) => ({ items: id === 'target' && ++targetReads > 1
      ? [{ id: 'just-started', phase: 'Running', sessionID: 'target' }]
      : [] }))
    const { el } = await mountChat(scripted([]), {
      deleteSession, listRuns,
      listSessions: () => Promise.resolve([session('current', 'Current thread'), session('target', 'Delete target')]),
    })
    const thread = [...el.querySelectorAll<HTMLElement>('.k-ai-conversation-rail__item')]
      .find(item => text(item).includes('Delete target'))!
    thread.querySelector<HTMLButtonElement>('button[aria-label="Delete chat"]')!.click()
    await settle(4)
    resolveConfirm(true)
    await settle(6)
    expect(targetReads).toBe(2)
    expect(deleteSession).not.toHaveBeenCalled()
    expect(text(document.querySelector('.k-toast--info'))).toContain('Stop the run before deleting this thread')
  })

  it('keeps a detached running thread until its run is stopped before deletion', async () => {
    const deleteSession = vi.fn()
    const live = { id: 'background-run', phase: 'Running', sessionID: 'background', agent: 'scout' }
    const listRuns = vi.fn(async ({ session: id }: { session: string }) => ({ items: id === 'background' ? [live] : [] }))
    const { el } = await mountChat(scripted([]), {
      deleteSession, listRuns,
      listSessions: () => Promise.resolve([session('current', 'Current thread'), session('background', 'Background work')]),
    })
    const thread = [...el.querySelectorAll<HTMLElement>('.k-ai-conversation-rail__item')]
      .find(item => text(item).includes('Background work'))!
    thread.querySelector<HTMLButtonElement>('button[aria-label="Delete chat"]')!.click()
    await settle(6)
    expect(listRuns).toHaveBeenCalledWith({ agent: 'scout', session: 'background' })
    expect(deleteSession).not.toHaveBeenCalled()
    expect(document.querySelector('.k-confirm')).toBeNull()
    expect(text(document.querySelector('.k-toast--info'))).toContain('Stop the run before deleting this thread')
    expect(text(el.querySelector('.k-ai-conversation-rail'))).toContain('Background work')
  })

  it('preserves an unacknowledged start until admission when another thread opens', async () => {
    const admission = deferred<void>()
    let signal!: AbortSignal
    const cancelRun = vi.fn()
    const listSessions = vi.fn().mockResolvedValue([])
    const chatStream = async function* (_agent: string, _message: string, sessionID: string, controller: AbortSignal) {
      signal = controller
      await admission.promise
      yield { event: 'start', data: { runID: 'late-admitted', sessionID } } as SSEEvent
      yield { event: 'run_started', data: { runID: 'late-admitted', sessionID, status: 'running' } } as SSEEvent
      yield { event: 'delta', data: { text: 'Original thread only' } } as SSEEvent
    }
    const { el } = await mountChat(chatStream, { cancelRun, listSessions })
    await send(el, 'A request awaiting admission')
    el.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!.click()
    await settle(4)
    expect(signal.aborted).toBe(false)
    expect(text(document.querySelector('.k-toast--info'))).toContain('request is still starting')
    admission.resolve()
    await settle(6)
    expect(signal.aborted).toBe(true)
    expect(cancelRun).not.toHaveBeenCalled()
    expect(text(el.querySelector('.agents-log'))).not.toContain('Original thread only')
    expect(listSessions.mock.calls.length).toBeGreaterThan(1)
  })

  it('starts another thread without canceling a running turn or admitting late stream frames', async () => {
    const oldGate = deferred<void>()
    const secondGate = deferred<void>()
    const signals: AbortSignal[] = []
    const cancelRun = vi.fn()
    let call = 0
    const chatStream = vi.fn(async function* (_agent: string, _message: string, sessionID: string, signal: AbortSignal) {
      signals.push(signal)
      const first = call++ === 0
      yield { event: 'start', data: { runID: first ? 'run-old' : 'run-new', sessionID } } as SSEEvent
      yield { event: 'run_started', data: { runID: first ? 'run-old' : 'run-new', sessionID, status: 'running' } } as SSEEvent
      if (first) {
        await oldGate.promise
        yield { event: 'delta', data: { text: 'Old thread late delta' } } as SSEEvent
        yield { event: 'done', data: { runID: 'run-old', content: 'Old thread final', status: 'completed' } } as SSEEvent
      } else {
        yield { event: 'delta', data: { text: 'New thread reply' } } as SSEEvent
        await secondGate.promise
      }
    })
    const { el } = await mountChat(chatStream, { cancelRun })
    await send(el, 'First thread task')
    const runningThread = el.querySelector('.k-ai-conversation-rail__item-select[aria-busy="true"]')?.getAttribute('data-thread-id')
    expect(runningThread).toBeTruthy()
    const create = el.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!
    expect(create.disabled).toBe(false)
    create.click()
    await settle(6)
    expect(signals[0].aborted).toBe(true)
    expect(cancelRun).not.toHaveBeenCalled()
    expect(el.querySelector(`[data-thread-id="${runningThread}"] .k-ai-conversation-rail__item-spinner`)).not.toBeNull()
    expect(text(el.querySelector('.agents-log'))).not.toContain('First thread task')
    expect(text(document.querySelector('.k-toast--info'))).toContain('continues in its original thread')
    await send(el, 'Second thread task')
    expect(chatStream).toHaveBeenCalledTimes(2)
    expect(chatStream.mock.calls[0][2]).not.toBe(chatStream.mock.calls[1][2])
    oldGate.resolve()
    await settle(6)
    expect(text(el.querySelector('.agents-log'))).toContain('New thread reply')
    expect(text(el.querySelector('.agents-log'))).not.toContain('Old thread')
    expect(el.querySelector('.agents-stop')).not.toBeNull()
    expect(signals[1].aborted).toBe(false)
    secondGate.resolve()
  })

  it('selects another saved thread while a turn continues, then recovers the original run', async () => {
    const gate = deferred<void>()
    const cancelRun = vi.fn()
    const listMessagePage = vi.fn(async (_agent: string, sessionID: string) => ({ items: await (sessionID === 'other'
      ? [{ id: 'other-reply', role: 'assistant', content: 'Other thread history' }]
      : [{ id: 'original-reply', runID: 'running-original', role: 'assistant', content: 'Original progress' }]) }))
    let active = false
    const listRuns = vi.fn(async ({ session: sessionID }: { session: string }) => ({ items: active && sessionID === 'original'
      ? [{ id: 'running-original', sessionID: 'original', phase: 'Running', agent: 'scout' }]
      : [] }))
    let signal!: AbortSignal
    const chatStream = async function* (_agent: string, _message: string, _session: string, controller: AbortSignal) {
      active = true
      signal = controller
      yield { event: 'start', data: { runID: 'running-original', sessionID: 'original' } } as SSEEvent
      yield { event: 'run_started', data: { runID: 'running-original', sessionID: 'original', status: 'running' } } as SSEEvent
      await gate.promise
    }
    const { el } = await mountChat(chatStream, {
      cancelRun, listMessagePage, listRuns, getRun: vi.fn().mockResolvedValue({ id: 'running-original', phase: 'Running' }),
      listSessions: () => Promise.resolve([session('original', 'Original thread'), session('other', 'Other thread')]),
    })
    await send(el, 'Continue original work')
    await chooseSession(el, 'Other thread')
    expect(signal.aborted).toBe(true)
    expect(cancelRun).not.toHaveBeenCalled()
    expect(text(el.querySelector('.agents-log'))).toContain('Other thread history')
    await chooseSession(el, 'Original thread')
    await settle(6)
    expect(text(el.querySelector('.agents-log'))).toContain('Original progress')
    expect(text(el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)'))).toContain('Working')
    gate.resolve()
  })

  it('recovers a running turn when the chat stream ends without a terminal frame', async () => {
    const getRun = vi.fn().mockResolvedValue({
      id: 'r-disconnected', agent: 'scout', sessionID: 's-disconnected', phase: 'Running',
      trigger: 'chat', class: 'interactive', createdAt: '', inputTokens: 0, outputTokens: 0, usdMicros: 0,
    })
    const { el } = await mountChat(scripted([
      { event: 'start', data: { runID: 'r-disconnected', sessionID: 's-disconnected' } },
      { event: 'delta', data: { text: 'Partial reply' } },
    ]), { getRun })

    await send(el, 'hello')
    await settle(6)

    expect(getRun).toHaveBeenCalledWith('r-disconnected')
    expect(text(el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)'))).toContain('Working')
    expect(el.querySelector('.agents-composer .agents-stop')).not.toBeNull()
    expect(el.querySelector('.agents-run-stop')).toBeNull()
    expect(text(el.querySelector('.agents-log'))).toContain('Partial reply')
  })

  it('reloads the saved reply when a disconnected turn already completed', async () => {
    const listMessagePage = vi.fn().mockResolvedValue({ items: [{ id: 'saved-final', runID: 'r1', role: 'assistant', content: 'Complete saved reply' }] })
    const { el } = await mountChat(scripted([
      { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
      { event: 'delta', data: { text: 'Partial reply' } },
    ]), { getRun: vi.fn().mockResolvedValue({ id: 'r1', phase: 'Succeeded' }), listMessagePage })

    await send(el, 'hello')
    await settle(6)

    expect(listMessagePage).toHaveBeenCalledWith('scout', 's1')
    expect(text(el.querySelector('.agents-log'))).toContain('Complete saved reply')
    expect(text(el.querySelector('.agents-log'))).not.toContain('Partial reply')
    expect(el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)')).toBeNull()
  })

  it('preserves partial output and offers recovery when disconnected run status cannot be read', async () => {
    const { el } = await mountChat(scripted([
      { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
      { event: 'delta', data: { text: 'Partial reply' } },
    ]), { getRun: vi.fn().mockRejectedValue(new Error('network unavailable')) })

    await send(el, 'hello')
    await settle(6)

    expect(text(el)).toContain('Could not recover the disconnected run.')
    expect(text(el)).toContain('network unavailable')
    expect(text(el.querySelector('.agents-log'))).toContain('Partial reply')
    expect(el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)')).toBeNull()
  })

  it('keeps Stop available when a live Running event supersedes stream recovery', async () => {
    const recovery = deferred<unknown>()
    const cancelRun = vi.fn().mockResolvedValue({})
    const { el, store } = await mountChat(scripted([
      { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
      { event: 'delta', data: { text: 'Partial reply' } },
    ]), { getRun: vi.fn(() => recovery.promise), cancelRun })
    await send(el, 'hello')

    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'r1', phase: 'Running' } } }))
    recovery.resolve({ id: 'r1', phase: 'Failed' })
    await settle(6)

    expect(text(el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)'))).toContain('Working')
    const stop = el.querySelector<HTMLButtonElement>('.agents-composer .agents-stop')!
    stop.click()
    await settle(4)
    expect(cancelRun).toHaveBeenCalledWith('r1')
  })

  it('uses the App Studio composer geometry and accessible compact action', async () => {
    const chatStream = vi.fn(scripted([]))
    const { el } = await mountChat(chatStream)
    const surface = el.querySelector('.agents-composer-surface')
    const editor = composerEditor(el)!
    const submit = el.querySelector<HTMLButtonElement>('button[type="submit"]')!

    const layout = el.querySelector<HTMLElement>('.k-ai-conversation-layout')!
    const header = el.querySelector<HTMLElement>('.k-ai-conversation-header')!
    expect(layout).not.toBeNull()
    expect(header).not.toBeNull()
    expect(header.parentElement?.classList.contains('agents-chat-shell')).toBe(true)
    expect(header.parentElement).toBe(layout.parentElement)
    expect(header.nextElementSibling).toBe(layout)
    expect(el.querySelector('.k-ai-conversation-back')).toBeNull()
    expect(el.querySelector('.agents-chat-title')).not.toBeNull()
    expect(el.querySelector('.agents-log.k-ai-transcript-scroll .k-ai-transcript')).not.toBeNull()
    expect(surface).not.toBeNull()
    expect(editor.getAttribute('role')).toBe('textbox')
    expect(editor.getAttribute('aria-multiline')).toBe('true')
    expect(editor.getAttribute('aria-label')).toBe('Message scout…')
    expect(editor.getAttribute('data-placeholder')).toBe('Message scout…')
    expect(text(el.querySelector('#agents-composer-help'))).toContain('Enter to send')
    expect(el.querySelector('.k-ai-composer__help')).toBeNull()
    expect(submit.classList.contains('agents-composer-primary')).toBe(true)
    expect(submit.getAttribute('aria-label')).toBe('Send')
    expect(submit.title).toBe('Send')
    expect(submit.textContent?.trim()).toBe('')
    expect(submit.disabled).toBe(true)
    expect(el.querySelectorAll('.agents-composer .k-ai-composer__controls')).toHaveLength(1)
    const add = el.querySelector<HTMLButtonElement>('[aria-label="Add files or open slash commands"]')!
    add.click()
    await settle()
    expect(add.getAttribute('aria-expanded')).toBe('true')
    expect(document.querySelector('[role="menu"][aria-label="Add"]')).not.toBeNull()

    setComposerText(editor, 'hello')
    await settle()
    expect(el.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(false)

    const composing = new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, bubbles: true, cancelable: true })
    editor.dispatchEvent(composing)
    await settle()
    expect(composing.defaultPrevented).toBe(false)
    expect(chatStream).not.toHaveBeenCalled()
  })

  it('opens the conversation rail on narrow screens and restores trigger focus', async () => {
    const originalMatchMedia = window.matchMedia
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn(() => ({
        matches: true,
        media: '(max-width: 767px)',
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    })
    try {
      localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
      const { el } = await mountChat(scripted([]), {
        listSessions: () => Promise.resolve([session('s1', 'Active chat'), session('s2', 'Second chat')]),
      })
      const trigger = el.querySelector<HTMLButtonElement>('.agents-mobile-rail-toggle')!
      trigger.focus()
      expect(trigger.getAttribute('aria-expanded')).toBe('false')

      trigger.click()
      await settle(4)
      expect(trigger.getAttribute('aria-expanded')).toBe('true')
      expect(el.querySelector('.agents-conversation-rail-shell.is-open')).not.toBeNull()
      expect(el.querySelector('.k-ai-conversation-rail--mobile-open')).not.toBeNull()

      el.querySelector<HTMLButtonElement>('.agents-mobile-rail-backdrop')!.click()
      await settle(4)
      expect(trigger.getAttribute('aria-expanded')).toBe('false')
      expect(document.activeElement).toBe(trigger)

      trigger.click()
      await settle(3)
      const rail = el.querySelector<HTMLElement>('.k-ai-conversation-rail--mobile-open')!
      rail.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      await settle(4)
      expect(trigger.getAttribute('aria-expanded')).toBe('false')
      expect(el.querySelector('.k-ai-conversation-rail--mobile-open')).toBeNull()
      expect(document.activeElement).toBe(trigger)

      trigger.click()
      await settle(3)
      el.querySelector<HTMLButtonElement>('[data-thread-id="s2"]')!.click()
      await settle(5)
      expect(trigger.getAttribute('aria-expanded')).toBe('false')
      expect(el.querySelector('.agents-conversation-rail-shell.is-open')).toBeNull()
      expect(el.querySelector('.k-ai-conversation-rail--mobile-open')).toBeNull()
      expect(document.activeElement).toBe(trigger)
    } finally {
      Object.defineProperty(window, 'matchMedia', { configurable: true, value: originalMatchMedia })
    }
  })

  it('lets desktop users collapse and reopen the shared rail without swallowing Escape', async () => {
    const originalMatchMedia = window.matchMedia
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn(() => ({
        matches: false,
        media: '(max-width: 767px)',
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    })
    try {
      const { el } = await mountChat(scripted([]))
      const trigger = el.querySelector<HTMLButtonElement>('.agents-desktop-rail-toggle')!
      const rail = el.querySelector<HTMLElement>('.k-ai-conversation-rail')!
      trigger.focus()

      expect(trigger.getAttribute('aria-label')).toBe('Toggle conversation panel')
      expect(trigger.getAttribute('aria-controls')).toBe('agents-conversation-rail')
      expect(trigger.getAttribute('aria-expanded')).toBe('true')

      trigger.click()
      await settle(3)
      expect(trigger.getAttribute('aria-expanded')).toBe('false')
      expect(rail.classList.contains('k-ai-conversation-rail--collapsed')).toBe(true)
      expect(document.activeElement).toBe(trigger)

      const escape = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
      rail.dispatchEvent(escape)
      expect(escape.defaultPrevented).toBe(false)

      trigger.click()
      await settle(3)
      expect(trigger.getAttribute('aria-expanded')).toBe('true')
      expect(rail.classList.contains('k-ai-conversation-rail--anchored')).toBe(true)
      expect(document.activeElement).toBe(trigger)
    } finally {
      Object.defineProperty(window, 'matchMedia', { configurable: true, value: originalMatchMedia })
    }
  })

  it('keeps App Studio context menus inside the viewport for pointer and Shift+F10', async () => {
    const originalWidth = window.innerWidth
    const originalHeight = window.innerHeight
    const originalGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 1440 })
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 900 })
    const menuRect = domRect(0, 0, 192, 115)
    const menuRectSpy = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      if (this.classList.contains('k-ai-conversation-rail__context-menu')) return menuRect
      return originalGetBoundingClientRect.call(this)
    })

    const assertMenuBounds = (menu: HTMLElement): void => {
      const left = Number.parseFloat(menu.style.left)
      const top = Number.parseFloat(menu.style.top)
      expect(Number.isFinite(left)).toBe(true)
      expect(Number.isFinite(top)).toBe(true)
      expect(left).toBe(1240)
      expect(top).toBe(777)
      expect(left).toBeGreaterThanOrEqual(8)
      expect(top).toBeGreaterThanOrEqual(8)
      expect(left + menuRect.width).toBeLessThanOrEqual(window.innerWidth - 8)
      expect(top + menuRect.height).toBeLessThanOrEqual(window.innerHeight - 8)
    }

    try {
      const rail = await mountConversationRail()
      const select = rail.element.querySelector<HTMLButtonElement>('[data-thread-id="thread-older"]')!
      const item = select.closest<HTMLElement>('.k-ai-conversation-rail__item')!
      const targetRect = domRect(1320, 840, 110, 32)
      Object.defineProperty(item, 'getBoundingClientRect', { configurable: true, value: () => targetRect })
      Object.defineProperty(select, 'getBoundingClientRect', { configurable: true, value: () => targetRect })

      const pointerEvent = new MouseEvent('contextmenu', {
        bubbles: true,
        cancelable: true,
        clientX: 1428,
        clientY: 888,
      })
      item.dispatchEvent(pointerEvent)
      expect(pointerEvent.defaultPrevented).toBe(true)
      await settle(6)

      let menu = document.querySelector<HTMLElement>('#app-studio-overlay-root [role="menu"]')
      expect(menu).not.toBeNull()
      expect(menu?.getAttribute('aria-label')).toBe('Actions for Previous incident review')
      assertMenuBounds(menu!)
      expect(document.activeElement).toBe(menu!.querySelector('[role="menuitem"]'))

      window.dispatchEvent(new Event('blur'))
      await settle(3)
      expect(document.querySelector('#app-studio-overlay-root [role="menu"]')).toBeNull()

      select.focus()
      const keyboardEvent = new KeyboardEvent('keydown', {
        bubbles: true,
        cancelable: true,
        key: 'F10',
        shiftKey: true,
      })
      select.dispatchEvent(keyboardEvent)
      expect(keyboardEvent.defaultPrevented).toBe(true)
      await settle(6)

      menu = document.querySelector<HTMLElement>('#app-studio-overlay-root [role="menu"]')
      expect(menu).not.toBeNull()
      assertMenuBounds(menu!)
      expect(document.activeElement).toBe(menu!.querySelector('[role="menuitem"]'))
    } finally {
      menuRectSpy.mockRestore()
      Object.defineProperty(window, 'innerWidth', { configurable: true, value: originalWidth })
      Object.defineProperty(window, 'innerHeight', { configurable: true, value: originalHeight })
    }
  })

  it('renders the user turn and streams assistant deltas as markdown', async () => {
    const { el } = await mountChat(
      scripted([
        { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
        { event: 'delta', data: { text: '**hi** ' } },
        { event: 'delta', data: { text: 'there' } },
        { event: 'done', data: { runID: 'r1', content: '**hi** there', usage: { inputTokens: 12, outputTokens: 4, usdMicros: 1500 } } },
      ]),
    )
    await send(el, 'hello')

    const msgs = el.querySelectorAll('.agents-msg')
    expect(msgs.length).toBe(2)
    expect(text(msgs[0])).toContain('hello')
    expect(msgs[0].querySelector('.k-ai-message__content--bubble')).not.toBeNull()
    expect(msgs[0].querySelector('.k-ai-message__before')).toBeNull()
    expect(msgs[0].querySelector('.agents-body')?.classList.contains('k-ai-prose')).toBe(false)
    // Markdown is rendered, not escaped.
    expect(msgs[1].querySelector('strong')?.textContent).toBe('hi')
    expect(msgs[1].querySelector('.agents-body')?.classList.contains('k-ai-prose')).toBe(true)
    // Per-turn usage footer.
    expect(text(msgs[1].querySelector('.agents-turn-usage'))).toContain('$0.0015')
  })

  it('keeps the last loaded chat list when its background refresh fails', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const listSessions = vi.fn()
      .mockResolvedValueOnce([session('s1', 'First chat'), session('s2', 'Second chat')])
      .mockRejectedValueOnce(new Error('session refresh failed'))
    const { el } = await mountChat(scripted([
      { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
      { event: 'done', data: { runID: 'r1', content: 'done' } },
    ]), { listSessions })

    await send(el, 'hello')

    expect(text(el.querySelector('.agents-chat-sessions-notice strong'))).toBe('Could not refresh conversations')
    expect(text(el.querySelector('.agents-chat-sessions-notice'))).toContain('Showing the last loaded conversations.')
    expect(text(el)).toContain('session refresh failed')
    await chooseSession(el, 'Second chat')
    expect(text(el.querySelector('.k-ai-conversation-rail'))).toContain('Second chat')
  })

  it('announces only the actively streaming assistant message', async () => {
    let release!: () => void
    const gate = new Promise<void>((resolve) => { release = resolve })
    const { el } = await mountChat(scripted([
      { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
      { event: 'delta', data: { text: 'Working' } },
    ], gate))

    const done = send(el, 'hello')
    await settle(6)

    expect(el.querySelector('.agents-log')?.hasAttribute('aria-live')).toBe(false)
    expect(el.querySelector('.agents-msg.user')?.hasAttribute('aria-live')).toBe(false)
    expect(el.querySelector('.agents-msg.assistant')?.getAttribute('aria-live')).toBe('polite')

    release()
    await done
  })

  it('sanitizes model HTML and hardens rendered links', async () => {
    const content = '<img src="x" onerror="window.pwned=true"><script>window.pwned=true</script> [docs](https://example.com)'
    const { el } = await mountChat(scripted([{ event: 'done', data: { runID: 'r1', content } }]))
    await send(el, 'show me')

    expect(el.querySelector('.agents-msg.assistant script')).toBeNull()
    expect(el.querySelector('.agents-msg.assistant img')?.hasAttribute('onerror')).toBe(false)
    const link = el.querySelector<HTMLAnchorElement>('.agents-msg.assistant a')!
    expect(link.target).toBe('_blank')
    expect(link.rel).toBe('noopener noreferrer')
  })

  it('copies a rendered markdown code block without trusting model chrome', async () => {
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
    const { el } = await mountChat(scripted([{
      event: 'done',
      data: { runID: 'r1', content: '```ts\nconst answer = 42\n```' },
    }]))
    await send(el, 'show code')

    const copy = el.querySelector<HTMLButtonElement>('.agents-code-copy')!
    expect(copy).not.toBeNull()
    copy.click()
    await settle()

    expect(writeText).toHaveBeenCalledWith('const answer = 42\n')
  })

  it('keeps the server-selected session identity for the next turn', async () => {
    const seen: string[] = []
    const chatStream = vi.fn(async function* (_agent: string, _message: string, sessionID: string) {
      seen.push(sessionID)
      yield { event: 'start', data: { runID: `r${seen.length}`, sessionID: 'server-session' } }
      yield { event: 'done', data: { runID: `r${seen.length}`, content: 'done' } }
    })
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 'seed-session')
    const { el } = await mountChat(chatStream, { listSessions: () => Promise.resolve([session('seed-session')]) })

    await send(el, 'first')
    await send(el, 'second')

    expect(seen).toEqual(['seed-session', 'server-session'])
  })

  it('shows a pending tool card that resolves with duration and expands', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const { el } = await mountChat(
      scripted(
        [
          { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
          { event: 'tool_start', data: { id: 't1', name: 'github__list_issues', args: '{"repo":"railgrid"}' } },
        ],
        gate,
      ),
    )
    const done = send(el, 'find issues')
    await settle(6)

    const card = el.querySelector('.k-ai-action-row')!
    expect(card.className).toContain('k-ai-action-row--busy')
    expect(text(card)).toContain('Github list issues')
    expect(text(card)).toContain('Running')

    release()
    await done

    // Expanding reveals the recorded args.
    el.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')!.click()
    await settle()
    expect(text(el.querySelector('.k-ai-action-row__details'))).toContain('"repo"')
    expect(text(el.querySelector('.k-ai-action-row__details'))).toContain('github__list_issues')
  })

  it('marks a tool card failed when tool_end carries an error', async () => {
    const { el } = await mountChat(
      scripted([
        { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
        { event: 'tool_start', data: { id: 't1', name: 'web_search', args: '{}' } },
        { event: 'tool_end', data: { id: 't1', name: 'web_search', args: '{}', error: 'rate limited', durationMS: 250 } },
        { event: 'done', data: { runID: 'r1', content: 'sorry' } },
      ]),
    )
    await send(el, 'search')
    const card = el.querySelector('.k-ai-action-row')!
    expect(card.className).toContain('k-ai-action-row--error')
    expect(text(card)).toContain('Elapsed 250ms')
  })

  it('shows a nonzero exit and the recorded output in live chat', async () => {
    const { el } = await mountChat(scripted([
      { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
      { event: 'tool_start', data: { id: 't1', name: 'commandExecution', args: 'npm test' } },
      { event: 'tool_end', data: { id: 't1', name: 'commandExecution', result: 'Assertion failed', error: true, exitCode: 1 } },
      { event: 'done', data: { runID: 'r1', content: 'The test failed.' } },
    ]))
    await send(el, 'Run the tests')
    const card = el.querySelector('.k-ai-action-row')!
    expect(card.classList.contains('k-ai-action-row--error')).toBe(false)
    expect(card.classList.contains('k-ai-action-row--attention')).toBe(false)
    expect(text(card.querySelector('.k-ai-action-row__outcome'))).toBe('Completed · exit 1')
    card.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')!.click()
    await settle()
    expect(text(card.querySelector('.k-ai-execution-details__output'))).toBe('Assertion failed')
    expect(text(card.querySelector('.k-ai-execution-details__status'))).toBe('Completed · exit 1')
  })

  it('allows manual collapse of running activity and preserves it after success', async () => {
    const release = deferred<void>()
    const chatStream = vi.fn(async function* () {
      yield { event: 'start', data: { runID: 'r-activity', sessionID: 's-activity' } } as SSEEvent
      yield { event: 'tool_start', data: { id: 't-activity', name: 'inspect_queue', args: '{}' } } as SSEEvent
      await release.promise
      yield { event: 'tool_end', data: { id: 't-activity', name: 'inspect_queue', args: '{}', result: '{"ok":true}', durationMS: 90 } } as SSEEvent
      yield { event: 'done', data: { runID: 'r-activity', content: 'finished' } } as SSEEvent
    })
    const { el } = await mountChat(chatStream)
    const sending = send(el, 'inspect the queue')
    await settle(6)

    const trigger = el.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')!
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
    trigger.click()
    await settle(3)
    expect(trigger.getAttribute('aria-expanded')).toBe('true')
    trigger.click()
    await settle(3)
    expect(trigger.getAttribute('aria-expanded')).toBe('false')

    release.resolve(undefined)
    await sending
    await settle(4)
    expect(trigger.getAttribute('aria-expanded')).toBe('false')

    trigger.click()
    await settle(3)
    expect(trigger.getAttribute('aria-expanded')).toBe('true')
    trigger.click()
    await settle(3)
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
  })

  it('keeps failed status visible while allowing activity to collapse and reopen', async () => {
    const { el } = await mountChat(scripted([
      { event: 'start', data: { runID: 'r-error', sessionID: 's-error' } },
      { event: 'tool_start', data: { id: 't-error', name: 'inspect_queue', args: '{}' } },
      { event: 'tool_end', data: { id: 't-error', name: 'inspect_queue', args: '{}', error: 'permission denied', durationMS: 40 } },
      { event: 'done', data: { runID: 'r-error', content: 'failed' } },
    ]))
    await send(el, 'inspect the queue')

    const trigger = el.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')!
    const card = el.querySelector<HTMLElement>('.k-ai-action-row')!
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
    expect(card.classList.contains('k-ai-action-row--error')).toBe(true)
    trigger.click()
    await settle(3)
    expect(trigger.getAttribute('aria-expanded')).toBe('true')
    trigger.click()
    await settle(3)
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
    expect(card.classList.contains('k-ai-action-row--error')).toBe(true)
    trigger.click()
    await settle(3)
    expect(text(card.querySelector('.k-ai-action-row__details'))).toContain('permission denied')
  })

  it('renders an approval card and resolves it through the inbox endpoint', async () => {
    const resolveInbox = vi.fn().mockResolvedValue({ id: 'i1', state: 'approved' })
    const { el } = await mountChat(
      scripted([
        { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
        {
          event: 'approval_required',
          data: { runID: 'r1', inboxID: 'i1', tool: 'edges__pods_delete', args: '{"name":"api"}', content: 'I need approval.' },
        },
      ]),
      { resolveInbox },
    )
    await send(el, 'delete the pod')

    const card = el.querySelector('.agents-approval')!
    expect(el.querySelector('.agents-approval-dock')?.contains(card)).toBe(true)
    expect(el.querySelector('.agents-log .agents-approval')).toBeNull()
    expect(el.querySelectorAll('.agents-approval')).toHaveLength(1)
    expect(text(card)).toContain('edges__pods_delete')
    expect(card.querySelector('.agents-approval-head')).toBeNull()
    expect(text(card.querySelector('.agents-approval-tool'))).toBe('edges__pods_delete')

    card.querySelector<HTMLButtonElement>('.agents-approval-actions .k-btn--primary')!.click()
    await settle(6)
    expect(resolveInbox).toHaveBeenCalledWith('i1', 'approve')
    expect(el.querySelector('.agents-approval-dock')).toBeNull()
    expect(el.querySelector('.agents-log .agents-approval')).toBeNull()
    const receipt = el.querySelector<HTMLDetailsElement>('details.agents-approval-done')!
    expect(text(receipt.querySelector('summary'))).toBe('Permission approved')
    expect(receipt.open).toBe(false)
    receipt.querySelector('summary')!.click()
    expect(receipt.open).toBe(true)
    expect(text(receipt.querySelector('.agents-approval-args'))).toContain('"name": "api"')
  })

  // A harness that cannot proceed asks a question. It names no tool, so the
  // approval card rendered "Approval required … tool unavailable" over an
  // Approve button that could not mean anything — reported from a live run.
  it('streams the same run after a question reply and uses the composer Stop control', async () => {
    const admitted = deferred<void>()
    const close = deferred<void>()
    const answerStream = vi.fn(async function* () {
      await admitted.promise
      yield { event: 'start', data: { runID: 'r1', sessionID: 's1', response: 'the acme/app one' } }
      yield { event: 'delta', data: { text: 'Opening the pull request' } }
      await close.promise
    })
    const cancelRun = vi.fn().mockResolvedValue({})
    const { el, store } = await mountChat(
      scripted([
        { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
        {
          event: 'approval_required',
          data: {
            runID: 'r1', inboxID: 'i1', kind: 'question', tool: '', args: '',
            question: 'Which repository should I open the pull request against?',
          },
        },
      ]),
      { answerStream, cancelRun, uploadAgentAttachment: async () => draftAttachment },
    )
    await send(el, 'open a PR')

    const card = el.querySelector('.agents-question-card')!
    expect(el.querySelector('.agents-log .agents-question-card')).toBeNull()
    expect(el.querySelector('.agents-question-dock')?.nextElementSibling).toBe(el.querySelector('.agents-composer'))
    expect(text(card)).toContain('Which repository should I open the pull request against?')
    expect(text(card)).not.toContain('tool unavailable')
    expect(text(card)).not.toContain('Approval required')
    // No verdict buttons: a question is answered, not approved.
    expect(text(card)).not.toContain('Deny')

    await attachTextFile(el)
    setComposerText(el, 'Keep this draft for my next turn')
    await settle(2)
    const input = card.querySelector<HTMLInputElement>('input[type="text"]')!
    input.value = 'the acme/app one'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await settle(2)
    card.querySelector<HTMLButtonElement>('button[type="submit"]')!.click()
    await settle(2)
    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'r1', phase: 'Running' } } }))
    await settle(2)
    expect(el.querySelector('.agents-composer .agents-stop')).not.toBeNull()
    admitted.resolve()
    await settle(6)
    expect(answerStream).toHaveBeenCalledWith('scout', 'i1', 'the acme/app one', expect.any(AbortSignal))
    expect(composerText(el)).toBe('Keep this draft for my next turn')
    expect(text(el.querySelector('.agents-composer'))).toContain('notes.txt')
    expect(el.querySelector('.agents-question-dock')).toBeNull()
    expect(el.querySelector('.agents-log .agents-question-card')).toBeNull()
    const reply = el.querySelector('.agents-answer-context')?.parentElement
    expect(text(reply)).toContain('Which repository should I open the pull request against?')
    expect(text(reply)).toContain('the acme/app one')
    expect(el.querySelector('.agents-composer .agents-stop')).toBeTruthy()
    expect(text(el.querySelector('.agents-log'))).toContain('Working')
    expect(text(el.querySelector('.agents-log'))).toContain('Opening the pull request')
    el.querySelector<HTMLButtonElement>('.agents-composer button[aria-label="Stop run"]')!.click()
    await settle(4)
    expect(cancelRun).toHaveBeenCalledWith('r1')
    close.resolve()
    await settle(4)
  })

  it('rebuilds question replies as user messages before the continued reply', () => {
    const rows = rebuildTranscript([
      { id: 'answer', role: 'user', content: 'Line guide', runID: 'r1', metadata: { questionPrompt: 'Where should it go?' } },
      { id: 'continuation', role: 'assistant', content: 'I updated the guide.', runID: 'r1' },
    ])
    expect(rows.map(({ role, content, questionPrompt }) => ({ role, content, questionPrompt }))).toEqual([
      { role: 'user', content: 'Line guide', questionPrompt: 'Where should it go?' },
      { role: 'assistant', content: 'I updated the guide.' },
    ])
  })

  it('expands the persisted question group into its original prompt and reply pairs', async () => {
    const [message] = rebuildTranscript([{
      id: 'group-reply', role: 'user', content: 'formatted reply', metadata: {
        questionPrompt: 'Where? Who? When?',
        questions: [
          { id: 'where', prompt: 'Where?' },
          { id: 'who', prompt: 'Who?' },
          { id: 'when', prompt: 'When?' },
        ],
        answers: { where: 'README', who: null, when: 'Tomorrow' },
      },
    }])
    const view = await mountVue(ChatMessageView, { message })
    mounted.push(view)
    const details = view.element.querySelector<HTMLDetailsElement>('details.agents-question-history')!
    expect(details.open).toBe(false)
    expect(details.querySelector('summary')?.textContent).toBe('Asked 3 questions · 2 answered, 1 skipped')
    details.querySelector('summary')!.click()
    expect(details.open).toBe(true)
    expect(Array.from(details.querySelectorAll('dt, dd'), node => node.textContent)).toEqual([
      'Where?', 'README', 'Who?', 'Skipped', 'When?', 'Tomorrow',
    ])
  })

  // An older server sends no kind. A park with no tool is still a question, so
  // the absent field must not fall back to the approval card.
  it('treats a park with no tool as a question even without a kind', async () => {
    const { el } = await mountChat(
      scripted([
        { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
        { event: 'approval_required', data: { runID: 'r1', inboxID: 'i1', tool: '', args: '', question: 'Which one?' } },
      ]),
      {},
    )
    await send(el, 'go')
    const card = el.querySelector('.agents-question-card')!
    expect(text(card)).toContain('Which one?')
    expect(text(card)).not.toContain('tool unavailable')
  })

  it('keeps both approval decisions single-flight while one is pending', async () => {
    const resolution = deferred<{ id: string; state: string }>()
    const resolveInbox = vi.fn(() => resolution.promise)
    const { el } = await mountChat(
      scripted([
        { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
        {
          event: 'approval_required',
          data: { runID: 'r1', inboxID: 'i1', tool: 'edges__pods_delete', args: '{}', content: 'Approve?' },
        },
      ]),
      { resolveInbox },
    )
    await send(el, 'delete it')

    const [deny, approve] = [...el.querySelectorAll<HTMLButtonElement>('.agents-approval-actions button')]
    approve.click()
    await settle(2)

    expect(approve.disabled).toBe(true)
    expect(deny.disabled).toBe(true)
    expect(approve.getAttribute('aria-busy')).toBe('true')
    expect(deny.getAttribute('aria-busy')).toBeNull()
    expect(text(approve)).toContain('Approving')
    deny.click()
    expect(resolveInbox).toHaveBeenCalledTimes(1)
    expect(resolveInbox).toHaveBeenCalledWith('i1', 'approve')

    resolution.resolve({ id: 'i1', state: 'approved' })
    await settle(4)
    expect(text(el.querySelector('.agents-approval-done summary'))).toBe('Permission approved')
  })

  it('blocks malformed approval disclosure while leaving denial available', async () => {
    const resolveInbox = vi.fn().mockResolvedValue({ id: 'i1', state: 'denied' })
    const { el } = await mountChat(
      scripted([
        { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
        {
          event: 'approval_required',
          data: { runID: 'r1', inboxID: 'i1', tool: 'edges__pods_delete', args: 'not-json', content: 'Approve?' },
        },
      ]),
      { resolveInbox },
    )
    await send(el, 'delete it')

    const [deny, approve] = [...el.querySelectorAll<HTMLButtonElement>('.agents-approval-actions button')]
    expect(approve.disabled).toBe(true)
    expect(deny.disabled).toBe(false)
    expect(text(el.querySelector('.agents-approval-disclosure-error'))).toContain('Approval details are unavailable or malformed')
    deny.click()
    await settle(4)
    expect(resolveInbox).toHaveBeenCalledWith('i1', 'deny')
    expect(text(el.querySelector('.agents-approval-done summary'))).toBe('Permission denied')
  })

  it('does not show a failed approval after the user leaves its session', async () => {
    const resolution = deferred<{ id: string; state: string }>()
    const { el } = await mountChat(
      scripted([
        { event: 'start', data: { runID: 'r1', sessionID: 'approval-session' } },
        {
          event: 'approval_required',
          data: { runID: 'r1', inboxID: 'i1', tool: 'edges__pods_delete', args: '{}', content: 'Approve?' },
        },
      ]),
      { resolveInbox: () => resolution.promise },
    )
    await send(el, 'delete it')
    el.querySelector<HTMLButtonElement>('.agents-approval-actions .k-btn--primary')!.click()
    await settle(2)
    el.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!.click()
    await settle(2)

    resolution.reject(new Error('old approval failure'))
    await settle(4)

    expect(text(document.querySelector('.k-toast--error'))).not.toContain('old approval failure')
  })

  it('stop aborts the stream and cancels the run server-side', async () => {
    const cancelRun = vi.fn().mockResolvedValue({ id: 'r1', cancelling: true })
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const { el } = await mountChat(scripted([
      { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
      { event: 'run_started', data: { runID: 'r1', sessionID: 's1', status: 'running' } },
    ], gate), { cancelRun })

    const done = send(el, 'long task')
    await settle(6)

    const stop = el.querySelector<HTMLButtonElement>('.agents-stop')
    expect(stop).not.toBeNull()
    stop!.click()
    await settle(4)
    expect(cancelRun).toHaveBeenCalledWith('r1')
    release()
    await done
  })

  it('refreshes the transcript when a successfully cancelled run becomes terminal', async () => {
    const listMessagePage = vi.fn().mockResolvedValue({ items: [] })
    const cancelRun = vi.fn().mockResolvedValue({ id: 'r1', cancelling: true })
    const chatStream = vi.fn(async function* (_agent: string, _message: string, _session: string, signal: AbortSignal) {
      yield { event: 'start', data: { runID: 'r1', sessionID: 's1' } } as SSEEvent
      yield { event: 'run_started', data: { runID: 'r1', sessionID: 's1', status: 'running' } } as SSEEvent
      await new Promise<void>((resolve) => signal.addEventListener('abort', () => resolve(), { once: true }))
    })
    const { el, store } = await mountChat(chatStream, { cancelRun, listMessagePage })
    const readsBeforeSend = listMessagePage.mock.calls.length

    await send(el, 'long task')
    el.querySelector<HTMLButtonElement>('.agents-stop')!.click()
    await settle(6)
    expect(cancelRun).toHaveBeenCalledWith('r1')

    store.dispatchEvent(new CustomEvent('server', {
      detail: { type: 'run', data: { id: 'r1', phase: 'Aborted' } },
    }))
    await settle(6)

    expect(listMessagePage.mock.calls.length).toBeGreaterThan(readsBeforeSend)
    expect(listMessagePage).toHaveBeenLastCalledWith('scout', 's1')
  })

  it('defers a terminal transcript refresh until an in-flight cancellation settles', async () => {
    const cancellation = deferred<{ id: string; cancelling: boolean }>()
    const listMessagePage = vi.fn()
      .mockResolvedValueOnce({ items: [] })
      .mockResolvedValue({ items: [{ id: 'final', role: 'assistant', content: 'Authoritative final reply' }] })
    const cancelRun = vi.fn(() => cancellation.promise)
    const chatStream = vi.fn(async function* (_agent: string, _message: string, _session: string, signal: AbortSignal) {
      yield { event: 'start', data: { runID: 'r1', sessionID: 's1' } } as SSEEvent
      yield { event: 'run_started', data: { runID: 'r1', sessionID: 's1', status: 'running' } } as SSEEvent
      await new Promise<void>((resolve) => signal.addEventListener('abort', () => resolve(), { once: true }))
    })
    const { el, store } = await mountChat(chatStream, { cancelRun, listMessagePage })

    await send(el, 'long task')
    el.querySelector<HTMLButtonElement>('.agents-stop')!.click()
    await settle(2)
    expect(cancelRun).toHaveBeenCalledWith('r1')
    const readsBeforeTerminal = listMessagePage.mock.calls.length

    store.dispatchEvent(new CustomEvent('server', {
      detail: { type: 'run', data: { id: 'r1', phase: 'Aborted' } },
    }))
    await settle(3)
    expect(listMessagePage).toHaveBeenCalledTimes(readsBeforeTerminal)

    cancellation.resolve({ id: 'r1', cancelling: true })
    await settle(8)
    expect(listMessagePage.mock.calls.length).toBeGreaterThan(readsBeforeTerminal)
    expect(listMessagePage).toHaveBeenLastCalledWith('scout', 's1')
    expect(text(el)).toContain('Authoritative final reply')
  })

  it('waits for a delayed start frame before cancelling an immediate stop', async () => {
    const start = deferred<void>()
    const cancelRun = vi.fn().mockResolvedValue({ id: 'r-delayed', cancelling: true })
    const chatStream = vi.fn(async function* () {
      await start.promise
      yield { event: 'start', data: { runID: 'r-delayed', sessionID: 's-delayed' } } as SSEEvent
      yield { event: 'run_started', data: { runID: 'r-delayed', sessionID: 's-delayed', status: 'running' } } as SSEEvent
    })
    const { el } = await mountChat(chatStream, { cancelRun })

    await send(el, 'long task')
    const stop = el.querySelector<HTMLButtonElement>('.agents-stop')!
    stop.click()
    await settle(2)
    expect(stop.disabled).toBe(true)
    expect(stop.getAttribute('aria-busy')).toBe('true')
    expect(cancelRun).not.toHaveBeenCalled()

    start.resolve()
    await settle(6)
    expect(cancelRun).toHaveBeenCalledTimes(1)
    expect(cancelRun).toHaveBeenCalledWith('r-delayed')
  })

  it('keeps a run recoverable when stream cancellation fails', async () => {
    const cancellation = deferred<{ id: string; cancelling: boolean }>()
    const cancelRun = vi.fn()
      .mockImplementationOnce(() => cancellation.promise)
      .mockResolvedValueOnce({ id: 'r1', cancelling: true })
    const chatStream = vi.fn(async function* (_agent: string, _message: string, _session: string, signal: AbortSignal) {
      yield { event: 'start', data: { runID: 'r1', sessionID: 's1' } } as SSEEvent
      yield { event: 'run_started', data: { runID: 'r1', sessionID: 's1', status: 'running' } } as SSEEvent
      await new Promise<void>((resolve) => signal.addEventListener('abort', () => resolve(), { once: true }))
    })
    const { el } = await mountChat(chatStream, { cancelRun })

    await send(el, 'long task')
    el.querySelector<HTMLButtonElement>('.agents-stop')!.click()
    await settle(2)
    cancellation.reject(new Error('provider unavailable'))
    await settle(6)

    const banner = el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)')
    expect(banner).not.toBeNull()
    expect(text(banner)).toContain('Working')
    expect(text(document.querySelector('.k-toast--error'))).toContain('provider unavailable')
    const retry = el.querySelector<HTMLButtonElement>('.agents-composer .agents-stop')!
    retry.click()
    await settle(4)
    expect(cancelRun).toHaveBeenCalledTimes(2)
    expect(el.querySelector<HTMLButtonElement>('.agents-composer .agents-stop')?.disabled).toBe(true)
  })

  it('does not show a failed stream cancellation after the user leaves its session', async () => {
    const cancellation = deferred<{ id: string; cancelling: boolean }>()
    let release!: () => void
    const gate = new Promise<void>((resolve) => { release = resolve })
    const { el } = await mountChat(
      scripted([
        { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
        { event: 'run_started', data: { runID: 'r1', sessionID: 's1', status: 'running' } },
      ], gate),
      { cancelRun: () => cancellation.promise },
    )

    const done = send(el, 'long task')
    await settle(6)
    el.querySelector<HTMLButtonElement>('.agents-stop')!.click()
    await settle(2)
    release()
    await done
    await settle(6)
    expect(el.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!.disabled).toBe(false)
    el.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!.click()
    await settle(2)

    cancellation.reject(new Error('old cancellation failure'))
    await settle(4)

    expect(text(document.querySelector('.k-toast--error'))).not.toContain('old cancellation failure')
  })

  it('does not show a failed deletion after the user leaves its agent', async () => {
    const deletion = deferred<void>()
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const { el, view } = await mountChat(scripted([]), {
      listSessions: () => Promise.resolve([session('s1')]),
      deleteSession: () => deletion.promise,
    })

    el.querySelector<HTMLButtonElement>('button[aria-label="Delete chat"]')!.click()
    await settle(2)
    resolveConfirm(true)
    await settle(2)
    await view.setProps({ name: 'ranger' })

    deletion.reject(new Error('old deletion failure'))
    await settle(4)

    expect(text(document.querySelector('.k-toast--error'))).not.toContain('old deletion failure')
  })

  it('shows a current-agent error when deleting an inactive session fails', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const deleteSession = vi.fn().mockRejectedValue(new Error('inactive deletion failed'))
    const { el } = await mountChat(scripted([]), {
      listSessions: () => Promise.resolve([session('s1', 'Active chat'), session('s2', 'Old chat')]),
      deleteSession,
    })

    const oldChat = [...el.querySelectorAll<HTMLElement>('.k-ai-conversation-rail__item')]
      .find(item => item.querySelector('[data-thread-id="s2"]'))!
    oldChat.querySelector<HTMLButtonElement>('button[aria-label="Delete chat"]')!.click()
    await settle(2)
    resolveConfirm(true)
    await settle(4)

    expect(deleteSession).toHaveBeenCalledWith('scout', 's2')
    expect(text(document.querySelector('.k-toast--error'))).toContain('inactive deletion failed')
  })

  it('clears a pending session-list refresh when deleting an inactive session', async () => {
    const pendingRefresh = deferred<ReturnType<typeof session>[]>()
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const listSessions = vi.fn()
      .mockResolvedValueOnce([session('s1', 'Active chat'), session('s2', 'Old chat')])
      .mockRejectedValueOnce(new Error('session refresh failed'))
      .mockImplementationOnce(() => pendingRefresh.promise)
    const { el } = await mountChat(scripted([
      { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
      { event: 'done', data: { runID: 'r1', content: 'done' } },
    ]), { listSessions, deleteSession: vi.fn().mockResolvedValue(undefined) })

    await send(el, 'refresh the list')
    const stale = el.querySelector<HTMLElement>('.agents-chat-sessions-notice')!
    const retry = stale.querySelector<HTMLButtonElement>('button')!
    retry.click()
    await settle(2)
    expect(retry.disabled).toBe(true)
    expect(text(retry)).toContain('Retrying')

    const oldChat = [...el.querySelectorAll<HTMLElement>('.k-ai-conversation-rail__item')]
      .find(item => item.querySelector('[data-thread-id="s2"]'))!
    oldChat.querySelector<HTMLButtonElement>('button[aria-label="Delete chat"]')!.click()
    await settle(2)
    resolveConfirm(true)
    await settle(4)

    expect(text(retry)).toContain('Retry')
    expect(text(retry)).toBe('Retry')
    expect(retry.disabled).toBe(false)

    pendingRefresh.resolve([session('s1', 'Active chat'), session('s2', 'Old chat')])
    await settle(4)
  })

  it('keeps an active transcript read alive when deleting another session', async () => {
    const activeHistory = deferred<TranscriptMessage[]>()
    const listMessagePage = vi.fn(async (_agent: string, id: string) => ({ items: await (id === 's1'
      ? activeHistory.promise
      : Promise.resolve([] as TranscriptMessage[])) }))
    const deleteSession = vi.fn().mockResolvedValue(undefined)
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const { el } = await mountChat(scripted([]), {
      listSessions: () => Promise.resolve([session('s1', 'Active chat'), session('s2', 'Old chat')]),
      listMessagePage,
      deleteSession,
    })

    await settle(2)
    expect(listMessagePage).toHaveBeenCalledWith('scout', 's1')
    const oldChat = [...el.querySelectorAll<HTMLElement>('.k-ai-conversation-rail__item')]
      .find(item => item.querySelector('[data-thread-id="s2"]'))
    expect(oldChat).not.toBeUndefined()
    oldChat!.querySelector<HTMLButtonElement>('button[aria-label="Delete chat"]')!.click()
    await settle(2)
    resolveConfirm(true)
    await settle(4)

    expect(deleteSession).toHaveBeenCalledWith('scout', 's2')
    activeHistory.resolve([{ id: 'active-message', role: 'user', content: 'active transcript' }])
    await settle(6)
    expect(text(el)).toContain('active transcript')
  })

  it('keeps chat deletion single-flight through confirmation and deletion', async () => {
    const deletion = deferred<void>()
    const deleteSession = vi.fn(() => deletion.promise)
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const { el } = await mountChat(scripted([]), {
      listSessions: () => Promise.resolve([session('s1')]),
      deleteSession,
    })

    const button = el.querySelector<HTMLButtonElement>('button[aria-label="Delete chat"]')!
    button.click()
    button.click()
    await settle(2)
    expect(button.disabled).toBe(true)
    expect(el.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__item-select')?.getAttribute('aria-busy')).toBe('true')
    resolveConfirm(true)
    await settle(3)
    expect(deleteSession).toHaveBeenCalledTimes(1)
    button.click()
    expect(deleteSession).toHaveBeenCalledTimes(1)

    deletion.resolve()
    await settle(6)
    expect(el.querySelector<HTMLButtonElement>('button[aria-label="Delete chat"]')?.disabled).toBe(false)
  })

  it('honors user scrolling and keeps following when activity grows before a scroll event', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const { el } = await mountChat(
      scripted([{ event: 'start', data: { runID: 'r1', sessionID: 's1' } }, { event: 'delta', data: { text: 'a' } }], gate),
    )
    const done = send(el, 'hi')
    await settle(4)

    const log = el.querySelector<HTMLElement>('.agents-log')!
    // jsdom reports zero heights, so drive the scroll handler directly with a
    // geometry that means "scrolled up".
    let scrollHeight = 1000
    let scrollTop = 0
    Object.defineProperty(log, 'scrollHeight', { get: () => scrollHeight })
    Object.defineProperty(log, 'clientHeight', { value: 200, configurable: true })
    Object.defineProperty(log, 'scrollTop', {
      get: () => scrollTop,
      set: (value: number) => { scrollTop = Math.min(value, scrollHeight - log.clientHeight) },
    })
    log.scrollTop = 100
    log.dispatchEvent(new Event('scroll'))
    await settle(2)
    expect(log.scrollTop).toBe(100)

    el.querySelector<HTMLButtonElement>('.agents-return-latest')!.click()
    await settle(2)
    expect(log.scrollTop).toBe(800)

    scrollHeight = 1250
    log.dispatchEvent(new Event('scroll'))
    await settle(2)
    expect(log.scrollTop).toBe(1050)
    expect(el.querySelector('.agents-return-latest')).toBeNull()

    release()
    await done
  })
})

describe('chat read ownership', () => {
  it('announces a settled empty transcript as a polite status', async () => {
    const api = stubApi({ listSessions: () => Promise.resolve([]), listMessagePage: async () => ({ items: await (Promise.resolve([])) }) })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    await settle(4)

    const empty = [...view.element.querySelectorAll<HTMLElement>('[role="status"]')]
      .find(element => text(element).includes('Share a goal'))
    expect(empty).not.toBeUndefined()
    expect(empty?.getAttribute('aria-live')).toBe('polite')
  })

  it('does not present failed initial reads as an empty chat', async () => {
    const api = stubApi({ listSessions: () => Promise.reject(new Error('sessions unavailable')) })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const sessionFailure = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(sessionFailure)
    await settle(4)

    expect(text(sessionFailure.element.querySelector('[role="alert"]'))).toContain('Could not load conversations')
    expect(text(sessionFailure.element)).not.toContain('No messages yet')
    expect(text(sessionFailure.element.querySelector('.k-ai-conversation-rail'))).not.toContain('New chat')

    const pendingRetry = deferred<{ items: [] }>()
    const listMessagePage = vi.fn()
      .mockRejectedValueOnce(new Error('transcript unavailable'))
      .mockReturnValueOnce(pendingRetry.promise)
    const messageAPI = stubApi({
      listSessions: () => Promise.resolve([session('s1')]),
      listMessagePage,
    })
    const messageStore = makeStore(messageAPI)
    messageStore.agents.data = [agentFixture('scout')]
    const messageFailure = await mountVue(AgentChat, { store: messageStore, api: messageAPI, name: 'scout' })
    mounted.push(messageFailure)
    await settle(4)

    const notice = messageFailure.element.querySelector('.agents-chat-transcript-notice')!
    expect(notice.getAttribute('role')).toBe('alert')
    expect(text(notice.querySelector('strong'))).toBe('Could not load this conversation')
    expect(text(notice)).toContain('transcript unavailable')
    expect(text(messageFailure.element)).not.toContain('No messages yet')

    const retry = notice.querySelector<HTMLButtonElement>('button')!
    retry.click()
    await settle(4)
    expect(retry.disabled).toBe(true)
    expect(text(retry)).toBe('Retrying…')
    expect(text(messageFailure.element)).not.toContain('Loading conversation…')
    pendingRetry.resolve({ items: [] })
    await settle(4)
    expect(messageFailure.element.querySelector('.agents-chat-transcript-notice')).toBeNull()
    expect(text(messageFailure.element)).toContain('Start a conversation')
  })

  it('retains a loaded transcript when a background refresh fails', async () => {
    let failMessages = false
    const listMessagePage = vi.fn(async () => ({ items: await (failMessages
      ? Promise.reject(new Error('transcript refresh unavailable'))
      : Promise.resolve([{ id: 'm1', role: 'user', content: 'keep this message' }])) }))
    const listRuns = vi.fn().mockResolvedValue({ items: [runRowForRead()] })
    const api = stubApi({ listSessions: () => Promise.resolve([session('s1')]), listMessagePage, listRuns })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    await settle(4)
    expect(text(view.element)).toContain('keep this message')

    failMessages = true
    store.dispatchEvent(new CustomEvent('server', {
      detail: { type: 'run', data: { id: 'late-run', phase: 'Succeeded' } },
    }))
    await settle(4)

    expect(text(view.element)).toContain('keep this message')
    expect(text(view.element.querySelector('.agents-chat-transcript-notice'))).toContain('Showing the last loaded transcript')
    expect(text(view.element.querySelector('.agents-chat-transcript-notice'))).toContain('transcript refresh unavailable')
    expect(text(view.element)).not.toContain('No messages yet')
  })

  it.each(['switch', 'new'] as const)('clears the old transcript error on %s while a retry is pending', async destination => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const pendingRetry = deferred<{ items: TranscriptMessage[] }>()
    const pendingDestination = deferred<{ items: TranscriptMessage[] }>()
    const listMessagePage = vi.fn()
      .mockRejectedValueOnce(new Error('first conversation unavailable'))
      .mockReturnValueOnce(pendingRetry.promise)
      .mockReturnValueOnce(pendingDestination.promise)
    const { el } = await mountChat(scripted([]), {
      listSessions: async () => [session('s1', 'First chat'), session('s2', 'Second chat')],
      listMessagePage,
    })

    const retry = el.querySelector<HTMLButtonElement>('.agents-chat-transcript-notice button')!
    retry.click()
    await settle()
    expect(retry.disabled).toBe(true)

    if (destination === 'switch') {
      await chooseSession(el, 'Second chat')
      expect(text(el.querySelector('.agents-log'))).toContain('Loading conversation…')
    } else {
      el.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!.click()
      await settle()
      expect(text(el.querySelector('.agents-log'))).toContain('Start a conversation')
    }
    expect(el.querySelector('.agents-chat-transcript-notice')).toBeNull()

    pendingRetry.reject(new Error('late error from first conversation'))
    await settle()
    expect(el.querySelector('.agents-chat-transcript-notice')).toBeNull()
    if (destination === 'switch') {
      expect(text(el.querySelector('.agents-log'))).toContain('Loading conversation…')
      pendingDestination.resolve({ items: [] })
      await settle()
      expect(text(el.querySelector('.agents-log'))).toContain('Start a conversation')
    }
  })

  it('does not let initial session discovery replace a new chat the user opened', async () => {
    const sessions = deferred<ReturnType<typeof session>[]>()
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 'remembered-session')
    const api = stubApi({ listSessions: () => sessions.promise })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)

    view.element.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!.click()
    await settle(2)
    const userSession = localStorage.getItem('railgrid:agents:session:org:ws:scout')
    expect(userSession).toBeTruthy()
    expect(userSession).not.toBe('remembered-session')

    sessions.resolve([session('remembered-session', 'Remembered chat')])
    await settle(6)

    expect(localStorage.getItem('railgrid:agents:session:org:ws:scout')).toBe(userSession)
    expect(text(view.element.querySelector('.k-ai-conversation-rail'))).toContain('New chat')
  })

  it('does not let initial session discovery replace a new thread that has started sending', async () => {
    const sessions = deferred<ReturnType<typeof session>[]>()
    const chatStream = vi.fn(async function* (_agent: string, _message: string, _sessionID: string) {
      yield { event: 'done', data: { runID: 'r1', content: 'current reply' } }
    })
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 'remembered-session')
    const api = stubApi({ listSessions: () => sessions.promise, chatStream })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'test-model' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)

    view.element.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!.click()
    await settle(6)
    await send(view.element, 'start now')
    const activeSession = chatStream.mock.calls[0][2]
    expect(activeSession).toBeTruthy()
    expect(activeSession).not.toBe('remembered-session')

    sessions.resolve([session('remembered-session', 'Remembered chat')])
    await settle(6)

    expect(localStorage.getItem('railgrid:agents:session:org:ws:scout')).toBe(activeSession)
    expect(text(view.element)).toContain('current reply')
  })

  it('keeps a remembered preference-bearing empty chat when older chats exist', async () => {
    const remembered = 'unsent-model-chat'
    const key = 'railgrid:agents:session:org:ws:scout'
    localStorage.setItem(key, remembered)
    const getSessionModel = vi.fn(async (_name: string, id: string) => ({
      selectedCredential: id === remembered ? 'sol' : 'openai',
      modelCredential: id === remembered ? 'sol' : '',
      effortOptions: ['low', 'high'],
      defaultEffort: '',
      reasoningEffortByModel: id === remembered ? { sol: 'high' } : {},
    }))
    const listMessagePage = vi.fn(async () => ({ items: [] }))
    const api = stubApi({
      listSessions: () => Promise.resolve([session('older', 'Older chat')]),
      getSessionModel,
      listMessagePage,
    })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    await settle(8)

    expect(getSessionModel).toHaveBeenCalledWith('scout', remembered)
    expect(getSessionModel).toHaveBeenCalledTimes(1)
    expect(listMessagePage).toHaveBeenCalledWith('scout', remembered)
    expect(localStorage.getItem(key)).toBe(remembered)
    expect(text(view.element.querySelector('.k-ai-conversation-rail'))).toContain('New chat')
    expect(text(view.element.querySelector('.k-ai-conversation-rail'))).toContain('Older chat')
  })

  it('keeps the remembered empty chat when its preference probe transiently fails', async () => {
    const remembered = 'unsent-model-chat'
    const key = 'railgrid:agents:session:org:ws:scout'
    localStorage.setItem(key, remembered)
    const getSessionModel = vi.fn()
      .mockRejectedValueOnce(new Error('temporary read failure'))
      .mockResolvedValueOnce({
        selectedCredential: 'sol', modelCredential: 'sol', effortOptions: ['low', 'high'], defaultEffort: '',
        reasoningEffortByModel: { sol: 'high' },
      })
    const listMessagePage = vi.fn(async () => ({ items: [] }))
    const api = stubApi({
      listSessions: () => Promise.resolve([session('older', 'Older chat')]),
      getSessionModel,
      listMessagePage,
    })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    await settle(8)

    expect(getSessionModel).toHaveBeenCalledTimes(2)
    expect(getSessionModel.mock.calls.map(([, id]) => id)).toEqual([remembered, remembered])
    expect(listMessagePage).toHaveBeenCalledWith('scout', remembered)
    expect(localStorage.getItem(key)).toBe(remembered)
    expect(text(view.element.querySelector('.k-ai-conversation-rail'))).toContain('New chat')
  })

  it('does not let a late message read overwrite a newer session', async () => {
    const first = deferred<Array<{ id: string; role: string; content: string }>>()
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const listMessagePage = vi.fn(async (_agent: string, id: string) => ({ items: await (id === 's1'
      ? first.promise
      : Promise.resolve([{ id: 'new', role: 'user', content: 'new session' }])) }))
    const api = stubApi({ listSessions: () => Promise.resolve([session('s1'), session('s2')]), listMessagePage })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    await settle(4)

    await chooseSession(view.element, 's2')
    expect(text(view.element)).toContain('new session')

    first.resolve([{ id: 'old', role: 'user', content: 'stale session' }])
    await settle(4)
    expect(text(view.element)).toContain('new session')
    expect(text(view.element)).not.toContain('stale session')
  })

  it('does not let a pending history read overwrite a completed streamed turn', async () => {
    const history = deferred<Array<{ id: string; role: string; content: string }>>()
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const chatStream = vi.fn(async function* () {
      yield { event: 'done', data: { runID: 'r1', content: 'fresh reply' } }
    })
    const api = stubApi({
      listSessions: () => Promise.resolve([session('s1')]),
      listMessagePage: async () => ({ items: await (history.promise) }),
      chatStream,
    })
    const store = makeStore(api)
    store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'test-model' }]
    store.credentials.loaded = store.credentials.hasSnapshot = true
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    await settle(4)

    await send(view.element, 'fresh question')
    expect(text(view.element)).toContain('fresh question')
    expect(text(view.element)).toContain('fresh reply')

    history.resolve([{ id: 'old', role: 'user', content: 'stale history' }])
    await settle(4)

    expect(text(view.element)).toContain('fresh question')
    expect(text(view.element)).toContain('fresh reply')
    expect(text(view.element)).not.toContain('stale history')
  })

  it('does not adopt a late session list from the previous agent', async () => {
    const scoutSessions = deferred<ReturnType<typeof session>[]>()
    const listSessions = vi.fn((name: string) => name === 'scout'
      ? scoutSessions.promise
      : Promise.resolve([session('ranger-session', 'Ranger chat')]))
    const listMessagePage = vi.fn(async (_name: string, id: string) => ({ items: await (Promise.resolve([
      { id: `${id}-message`, role: 'user', content: id },
    ])) }))
    const api = stubApi({ listSessions, listMessagePage })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout'), agentFixture('ranger')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)

    await view.setProps({ name: 'ranger' })
    await settle(4)
    scoutSessions.resolve([session('scout-session', 'Scout chat')])
    await settle(4)

    expect(text(view.element.querySelector('.k-ai-conversation-rail'))).toContain('Ranger chat')
    expect(text(view.element.querySelector('.k-ai-conversation-rail'))).not.toContain('Scout chat')
    expect(text(view.element)).toContain('ranger-session')
  })

  it('does not surface an orphan result from a session the user left', async () => {
    const firstRuns = deferred<{ items: ReturnType<typeof runRowForRead>[] }>()
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const listRuns = vi.fn((filter: { session?: string }) => filter.session === 's1'
      ? firstRuns.promise
      : Promise.resolve({ items: [] }))
    const api = stubApi({
      listSessions: () => Promise.resolve([session('s1'), session('s2')]),
      listMessagePage: async () => ({ items: await (Promise.resolve([])) }),
      listRuns,
    })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    await settle(4)

    await chooseSession(view.element, 's2')
    firstRuns.resolve({ items: [runRowForRead()] })
    await settle(4)

    expect(view.element.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)')).toBeNull()
  })
})

function runRowForRead() {
  return {
    id: 'late-run',
    agent: 'scout',
    sessionID: 's1',
    trigger: 'chat',
    class: 'interactive',
    phase: 'Running',
    inputTokens: 0,
    outputTokens: 0,
    usdMicros: 0,
    createdAt: new Date().toISOString(),
  }
}

describe('transcript rehydration', () => {
  const msg = (over: Partial<TranscriptMessage> & { id: string; role: string }): TranscriptMessage => ({
    content: '',
    ...over,
  })

  it('attaches persisted tool turns to the assistant turn that follows them', () => {
    const out = rebuildTranscript([
      msg({ id: '1', role: 'system', content: 'you are…' }),
      msg({ id: '2', role: 'user', content: 'list pods', runID: 'r1' }),
      msg({
        id: '3',
        role: 'tool',
        content: '["api-1","api-2"]',
        runID: 'r1',
        metadata: { tool: 'edges__pods_list', args: '{"ns":"prod"}', durationMS: 140 },
      }),
      msg({ id: '4', role: 'assistant', content: 'Two pods.', runID: 'r1' }),
    ])

    expect(out.map((m) => m.role)).toEqual(['user', 'assistant'])
    // The system prompt is not shown.
    expect(out[0].content).toBe('list pods')
    expect(out[1].tools).toHaveLength(1)
    expect(out[1].tools[0]).toMatchObject({
      name: 'edges__pods_list',
      args: '{"ns":"prod"}',
      result: '["api-1","api-2"]',
      durationMS: 140,
      pending: false,
    })
    expect(out[1].runID).toBe('r1')
  })

  it('keeps trailing tool turns that have no assistant reply', () => {
    const out = rebuildTranscript([
      msg({ id: '1', role: 'user', content: 'delete it' }),
      msg({ id: '2', role: 'tool', content: '', metadata: { tool: 'edges__pods_delete', error: 'denied' } }),
    ])
    expect(out).toHaveLength(2)
    expect(out[1].role).toBe('assistant')
    expect(out[1].tools[0]).toMatchObject({ name: 'edges__pods_delete', error: 'denied' })
  })

  it('renders reloaded tool turns as the same cards the live stream produces', async () => {
    // The API returns newest-first; the component reverses it.
    const listMessagePage = vi.fn().mockResolvedValue({ items: [
      { id: '3', role: 'assistant', content: 'done', runID: 'r1' },
      { id: '2', role: 'tool', content: 'ok', runID: 'r1', metadata: { tool: 'web_search', args: '{"q":"railgrid"}', durationMS: 90 } },
      { id: '1', role: 'user', content: 'search', runID: 'r1' },
    ] })
    const api = stubApi({ listMessagePage })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    const el = view.element
    await settle(6)

    const card = el.querySelector('.k-ai-action-row')!
    expect(text(card)).toContain('Web search')
    card.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')!.click()
    await settle()
    expect(text(card.querySelector('.k-ai-execution-details'))).toContain('web_search')
    expect(text(card)).toContain('90ms')
    // A generous limit is requested so a long session comes back whole.
    expect(listMessagePage.mock.calls[0][2] ?? 200).toBeGreaterThanOrEqual(200)
  })

  it('shows one stable run link on the last assistant segment and falls back to an available message', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 's1')
    const listMessagePage = vi.fn().mockResolvedValue({ items: [
      { id: '4', role: 'user', content: 'fallback', runID: 'r2' },
      { id: '3', role: 'assistant', content: 'second segment', runID: 'r1' },
      { id: '2', role: 'assistant', content: 'first segment', runID: 'r1' },
      { id: '1', role: 'user', content: 'request', runID: 'r1' },
    ] })
    const { el } = await mountChat(scripted([]), {
      listSessions: () => Promise.resolve([session('s1')]),
      listMessagePage,
    })

    const links = [...el.querySelectorAll<HTMLButtonElement>('.agents-message-run-link')]
    expect(links).toHaveLength(2)
    expect(links.map(link => link.id).sort()).toEqual(['agents-view-run-m3', 'agents-view-run-m4'])
    expect(links.every(link => !link.classList.contains('k-dashboard-action'))).toBe(true)
    expect(el.querySelector('#agents-view-run-m2')).toBeNull()
  })
})

// Reopened chats show active work and run controls in the progress header.
describe('a run still working with nobody attached', () => {
  const runRow = (over: Record<string, unknown> = {}) => ({
    id: 'r-live',
    agent: 'scout',
    sessionID: 's1',
    trigger: 'chat',
    class: 'interactive',
    phase: 'Running',
    inputTokens: 0,
    outputTokens: 0,
    usdMicros: 0,
    createdAt: new Date().toISOString(),
    ...over,
  })

  async function mountWith(phase: string, extra: Record<string, unknown> = {}) {
    const listRuns = vi.fn().mockResolvedValue({ items: [runRow({ phase })] })
    const api = stubApi({ listMessagePage: async () => ({ items: await (Promise.resolve([])) }), listRuns, ...extra })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    await settle(4)
    return { el: view.element, api, listRuns, store }
  }

  it('announces a Running run and offers the run view', async () => {
    const { el } = await mountWith('Running')
    const banner = el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)')
    expect(banner).toBeTruthy()
    expect(text(banner!)).toContain('Working')
    expect(text(banner!)).toContain('View run')
    expect(el.querySelector('.agents-orphan-banner')).toBeNull()
  })

  it('keeps the composer Stop control when reopening a running question reply', async () => {
    const answer = { id: 'answer', role: 'user', content: 'README', runID: 'r-live', metadata: { questionPrompt: 'Where?' } }
    let finished = false
    const { el, store } = await mountWith('Running', {
      listRuns: async () => ({ items: [runRow({ phase: finished ? 'Succeeded' : 'Running' })] }),
      listMessagePage: async () => ({ items: finished
        ? [{ id: 'final', role: 'assistant', content: 'I updated the README.', runID: 'r-live' }, answer]
        : [answer] }),
    })
    expect(el.querySelector('.agents-composer button[aria-label="Stop run"]')).toBeTruthy()
    expect(el.querySelector('.agents-composer .agents-stop')).toBeTruthy()
    finished = true
    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'r-live', phase: 'Succeeded' } } }))
    await settle(6)
    expect(el.querySelector('.agents-composer button[aria-label="Stop run"]')).toBeNull()
    expect(text(el.querySelector('.agents-log'))).toContain('I updated the README.')
  })

  it('counts a run waiting on approval as still working', async () => {
    const { el } = await mountWith('PendingApproval')
    expect(el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)')).toBeTruthy()
  })

  it('says nothing when the session has no live run', async () => {
    const { el } = await mountWith('Succeeded')
    expect(el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)')).toBeNull()
  })

  it('can stop a recovered run from the composer', async () => {
    const cancelRun = vi.fn().mockResolvedValue({ id: 'r-live', cancelling: true })
    const { el } = await mountWith('Running', { cancelRun })
    const stop = el.querySelector<HTMLButtonElement>('.agents-composer .agents-stop')!
    stop.click()
    await settle(4)
    expect(cancelRun).toHaveBeenCalledWith('r-live')
    expect(el.querySelector<HTMLButtonElement>('.agents-composer .agents-stop')?.disabled).toBe(true)
  })

  it('keeps orphan cancellation single-flight while the request is pending', async () => {
    const cancellation = deferred<{ id: string; cancelling: boolean }>()
    const cancelRun = vi.fn(() => cancellation.promise)
    const { el } = await mountWith('Running', { cancelRun })
    const stop = el.querySelector<HTMLButtonElement>('.agents-composer .agents-stop')!

    stop.click()
    await settle(2)
    expect(stop.disabled).toBe(true)
    expect(stop.getAttribute('aria-busy')).toBe('true')
    expect(stop.getAttribute('aria-label')).toBe('Stop requested')
    stop.click()
    expect(cancelRun).toHaveBeenCalledTimes(1)

    cancellation.resolve({ id: 'r-live', cancelling: true })
    await settle(4)
    expect(el.querySelector<HTMLButtonElement>('.agents-composer .agents-stop')?.disabled).toBe(true)
  })

  it('does not show a failed stop from a run after the user leaves its session', async () => {
    const cancellation = deferred<{ id: string; cancelling: boolean }>()
    const { el } = await mountWith('Running', { cancelRun: () => cancellation.promise })
    const stop = el.querySelector<HTMLButtonElement>('.agents-composer .agents-stop')!
    stop.click()
    await settle(2)
    el.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!.click()
    await settle(2)

    cancellation.reject(new Error('old run failure'))
    await settle(4)

    expect(text(document.querySelector('.k-toast--error'))).not.toContain('old run failure')
  })

  it('finishes progress and reloads the transcript when the run finishes', async () => {
    const listMessagePage = vi.fn().mockResolvedValue({ items: [] })
    // The re-check after the terminal transcript reload must also be terminal.
    let phase = 'Running'
    const listRuns = vi.fn().mockImplementation(() => Promise.resolve({ items: [runRow({ phase })] }))
    const api = stubApi({ listMessagePage, listRuns })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    const el = view.element
    await settle(4)
    expect(el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)')).toBeTruthy()
    const before = listMessagePage.mock.calls.length
    phase = 'Succeeded'

    store.dispatchEvent(
      new CustomEvent('server', { detail: { type: 'run', data: { id: 'r-live', phase: 'Succeeded' } } }),
    )
    await settle(4)

    expect(el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)')).toBeNull()
    expect(listMessagePage.mock.calls.length).toBeGreaterThan(before)
  })

  it('a lookup failure never breaks the transcript', async () => {
    const listRuns = vi.fn().mockRejectedValue(new Error('unavailable'))
    const api = stubApi({ listMessagePage: async () => ({ items: await (Promise.resolve([{ id: '1', role: 'user', content: 'hi' }])) }), listRuns })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    const el = view.element
    await settle(4)
    expect(el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)')).toBeNull()
    expect(text(el)).toContain('hi')
    expect(text(el.querySelector('.agents-chat-run-notice strong'))).toBe('Could not check for an active run')
    expect(text(el.querySelector('.agents-chat-run-notice'))).toContain('unavailable')
  })

  it('keeps a live-run snapshot when a same-session refresh fails', async () => {
    let lookups = 0
    const listRuns = vi.fn(async (filter: { session?: string } = {}) => {
      if (!filter.session) return { items: [] }
      if (lookups++ === 0) return { items: [runRow()] }
      throw new Error('run refresh failed')
    })
    const exposed = ref<{ refreshOrphan: () => Promise<void> } | null>(null)
    const api = stubApi({ listMessagePage: async () => ({ items: await (Promise.resolve([])) }), listRuns })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { ref: exposed, store, api, name: 'scout' })
    mounted.push(view)
    await settle(4)

    expect(view.element.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)')).toBeTruthy()
    await exposed.value!.refreshOrphan()
    await settle(4)

    expect(view.element.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)')).toBeTruthy()
    expect(text(view.element.querySelector('.agents-chat-run-notice'))).toContain('Could not refresh run status. Showing the last loaded status.')
    expect(text(view.element)).toContain('run refresh failed')
  })
})

describe('approval handoff after the chat stream closes', () => {
  const frames: SSEEvent[] = [
    { event: 'start', data: { runID: 'r-approval', sessionID: 's-approval' } },
    { event: 'approval_required', data: { runID: 'r-approval', inboxID: 'first', tool: 'describe_model', args: '{"model":"first"}' } },
    { event: 'done', data: { status: 'waiting' } },
  ]
  const pending = (inboxID: string, runID = 'r-approval') => ({ id: runID, phase: 'Running', pending: { inboxID, tool: 'describe_model', args: JSON.stringify({ model: inboxID }) }, steps: [] })
  const event = (store: EventTarget, phase: string, runID = 'r-approval') => store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: runID, phase } } }))

  it('keeps an approval retryable after a rejected decision and refreshes its server state safely', async () => {
    const resolveInbox = vi.fn()
      .mockRejectedValueOnce(new ApiError(403, 'Authorization: Bearer sensitive-backend-detail'))
      .mockResolvedValueOnce({})
    const listInbox = vi.fn().mockResolvedValue([])
    const getRun = vi.fn().mockResolvedValue(pending('first'))
    const { el } = await mountChat(scripted(frames), { resolveInbox, listInbox, getRun })
    await send(el, 'describe models')
    listInbox.mockClear()

    const approve = () => el.querySelector<HTMLButtonElement>('.agents-approval-actions .k-btn--primary')!
    approve().click()
    await settle(6)

    expect(resolveInbox).toHaveBeenCalledWith('first', 'approve')
    expect(listInbox).toHaveBeenCalledTimes(1)
    expect(getRun).toHaveBeenCalledWith('r-approval')
    expect(el.querySelector('.agents-approval')).not.toBeNull()
    expect(approve().disabled).toBe(false)
    expect([...document.querySelectorAll('.k-toast--error')].map(item => text(item)).join(' ')).toContain('do not have permission')
    expect(text(el)).not.toContain('sensitive-backend-detail')

    approve().click()
    await settle(6)

    expect(resolveInbox).toHaveBeenCalledTimes(2)
    expect(text(el.querySelector('.agents-approval-done summary'))).toBe('Permission approved')
  })

  it('uses the refreshed inbox to reconcile a decision whose response was lost without resending it', async () => {
    const resolveInbox = vi.fn().mockRejectedValue(new ApiError(502, 'Authorization: Bearer sensitive-backend-detail'))
    const listInbox = vi.fn().mockResolvedValue([{
      id: 'first', agentName: 'scout', runID: 'r-approval', kind: 'approval', state: 'approved',
      prompt: 'describe_model', createdAt: '',
    }])
    const getRun = vi.fn().mockResolvedValue(pending('first'))
    const { el } = await mountChat(scripted(frames), { resolveInbox, listInbox, getRun })
    await send(el, 'describe models')
    listInbox.mockClear()

    el.querySelector<HTMLButtonElement>('.agents-approval-actions .k-btn--primary')!.click()
    await settle(6)

    expect(listInbox).toHaveBeenCalledTimes(1)
    expect(getRun).toHaveBeenCalledWith('r-approval')
    expect(resolveInbox).toHaveBeenCalledTimes(1)
    expect(text(el.querySelector('.agents-approval-done summary'))).toBe('Permission approved')
    expect(el.querySelector('.agents-approval-actions')).toBeNull()
    expect(text(el)).not.toContain('sensitive-backend-detail')
  })

  it('recovers a pending disclosure after the original chat stream fails', async () => {
    const chatStream = vi.fn(async function* () {
      yield { event: 'start', data: { runID: 'r-approval', sessionID: 's-approval' } } as SSEEvent
      throw new Error('connection closed')
    })
    const getRun = vi.fn().mockResolvedValue(pending('recovered'))
    const { el, store } = await mountChat(chatStream, { getRun })
    await send(el, 'describe models')
    expect(text(el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)'))).toContain('Waiting')

    event(store, 'PendingApproval')
    await settle(6)

    expect(getRun).toHaveBeenCalledWith('r-approval')
    expect(text(el.querySelector('.agents-approval'))).toContain('recovered')
    expect(text(el)).not.toContain('Chat failed: connection closed')
  })

  it('keeps approval recovery closed after a terminal chat frame', async () => {
    const chatStream = scripted([
      { event: 'start', data: { runID: 'r-approval', sessionID: 's-approval' } },
      { event: 'done', data: { runID: 'r-approval', status: 'completed', content: 'finished' } },
    ])
    const getRun = vi.fn().mockResolvedValue(pending('stale'))
    const { el, store } = await mountChat(chatStream, { getRun })
    await send(el, 'describe models')

    event(store, 'PendingApproval')
    await settle(6)

    expect(getRun).not.toHaveBeenCalled()
    expect(el.querySelector('.agents-approval')).toBeNull()
  })

  it('projects a terminal run detail when approval lifecycle events arrive late', async () => {
    const finalTranscript = deferred<TranscriptMessage[]>()
    const listMessagePage = vi.fn()
      .mockResolvedValueOnce({ items: [] })
      .mockImplementationOnce(async () => ({ items: await (finalTranscript.promise) }))
    const getRun = vi.fn().mockResolvedValue({ ...pending('stale'), phase: 'Succeeded' })
    const { el, store } = await mountChat(scripted(frames), { getRun, listMessagePage })
    await send(el, 'describe models')
    expect(text(el.querySelector('.agents-approval'))).toContain('first')

    event(store, 'PendingApproval')
    await settle(6)

    expect(getRun).toHaveBeenCalledWith('r-approval')
    expect(el.querySelector('.agents-approval')).toBeNull()
    expect(el.querySelector('.k-ai-turn-progress')).toBeNull()
    expect(text(el)).not.toContain('Waiting for approval')

    finalTranscript.resolve([{
      id: 'terminal', runID: 'r-approval', role: 'assistant', content: 'Authoritative final answer',
      metadata: { turnPhase: 'terminal', turnStatus: 'completed' },
    }])
    await settle(6)

    expect(text(el)).toContain('Authoritative final answer')
    expect(el.querySelector('.k-ai-turn-progress')).toBeNull()
  })

  it('keeps approval recovery closed after an accepted cancellation without a terminal event', async () => {
    const gate = deferred<void>()
    const cancelRun = vi.fn().mockResolvedValue({ id: 'r-approval', cancelling: true })
    const getRun = vi.fn().mockResolvedValue(pending('stale'))
    const { el, store } = await mountChat(scripted([
      { event: 'start', data: { runID: 'r-approval', sessionID: 's-approval' } },
      { event: 'run_started', data: { runID: 'r-approval', sessionID: 's-approval', status: 'running' } },
    ], gate.promise), { cancelRun, getRun })
    await send(el, 'describe models')
    el.querySelector<HTMLButtonElement>('.agents-stop')!.click()
    await settle(6)

    event(store, 'PendingApproval')
    await settle(6)

    expect(cancelRun).toHaveBeenCalledWith('r-approval')
    expect(getRun).not.toHaveBeenCalled()
    expect(el.querySelector('.agents-approval')).toBeNull()
    gate.resolve()
    await settle(6)
  })

  it('keeps each permission decision when a successive approval arrives', async () => {
    const getRun = vi.fn().mockResolvedValue(pending('first'))
    const resolveInbox = vi.fn().mockResolvedValue({})
    const { el, store } = await mountChat(scripted(frames), { getRun, resolveInbox })
    await send(el, 'describe models')
    el.querySelector<HTMLButtonElement>('.agents-approval-actions .k-btn--primary')!.click()
    await settle(6)
    expect(text(el.querySelector('.agents-approval-done summary'))).toBe('Permission approved')
    getRun.mockResolvedValue(pending('second'))
    event(store, 'PendingApproval')
    await settle(6)
    expect(text(el.querySelector('.agents-log .agents-approval-done summary'))).toBe('Permission approved')
    expect(text(el.querySelector('.agents-approval'))).toContain('second')
    el.querySelector<HTMLButtonElement>('.agents-approval-actions button')!.click()
    await settle(6)
    expect(resolveInbox).toHaveBeenLastCalledWith('second', 'deny')
    expect(Array.from(el.querySelectorAll('.agents-log .agents-approval-done summary'), text)).toEqual([
      'Permission approved', 'Permission denied',
    ])
  })

  it('restores the pending disclosure when reopening a session', async () => {
    const getRun = vi.fn().mockResolvedValue(pending('restored'))
    const { el } = await mountChat(scripted([]), {
      listSessions: () => Promise.resolve([session('s-approval')]),
      listMessagePage: async () => ({ items: await (Promise.resolve([{ id: 'm1', runID: 'r-approval', role: 'assistant', content: '', metadata: { turnPhase: 'terminal', turnStatus: 'waiting' } }])) }),
      listRuns: () => Promise.resolve({ items: [{ id: 'r-approval', phase: 'Running' }] }), getRun,
    })
    await settle(6)
    expect(getRun).toHaveBeenCalledWith('r-approval')
    expect(text(el.querySelector('.agents-approval'))).toContain('restored')
  })

  it('recovers independent run approvals concurrently', async () => {
    let nextRun = 0
    const chatStream = vi.fn(async function* () {
      const runID = 'r-' + String(++nextRun)
      yield { event: 'start', data: { runID, sessionID: 's-shared' } } as SSEEvent
      throw new Error('connection closed')
    })
    const first = deferred<ReturnType<typeof pending>>()
    const second = deferred<ReturnType<typeof pending>>()
    const getRun = vi.fn((runID: string) => runID === 'r-1' ? first.promise : second.promise)
    const { el, store } = await mountChat(chatStream, {
      getRun, listSessions: async () => [session('s-shared', 'Existing runs')],
      listMessagePage: async () => ({ items: ['r-1', 'r-2'].map(runID => ({ id: runID, runID, role: 'assistant', content: runID })) }),
    })

    getRun.mockClear() // Ignore the status reads started by stream recovery.
    event(store, 'PendingApproval', 'r-1')
    event(store, 'PendingApproval', 'r-2')
    expect(getRun).toHaveBeenCalledTimes(2)

    first.resolve(pending('inbox-r-1', 'r-1'))
    second.resolve(pending('inbox-r-2', 'r-2'))
    await settle(8)

    expect(el.querySelectorAll('.agents-approval')).toHaveLength(2)
    expect(text(el)).toContain('inbox-r-1')
    expect(text(el)).toContain('inbox-r-2')
  })

  it.each(['Running', 'Succeeded'] as const)('%s on another run does not invalidate a pending approval lookup', async phase => {
    let nextRun = 0
    const chatStream = vi.fn(async function* () {
      const runID = 'r-' + String(++nextRun)
      yield { event: 'start', data: { runID, sessionID: 's-shared' } } as SSEEvent
      throw new Error('connection closed')
    })
    const lookup = deferred<ReturnType<typeof pending>>()
    const getRun = vi.fn(() => lookup.promise)
    const { el, store } = await mountChat(chatStream, {
      getRun, listSessions: async () => [session('s-shared', 'Existing runs')],
      listMessagePage: async () => ({ items: ['r-1', 'r-2'].map(runID => ({ id: runID, runID, role: 'assistant', content: runID })) }),
    })

    event(store, 'PendingApproval', 'r-1')
    event(store, phase, 'r-2')
    await settle(6)
    lookup.resolve(pending('still-pending', 'r-1'))
    await settle(8)

    expect(text(el.querySelector('.agents-approval'))).toContain('still-pending')
  })

  it('still rejects a stale lookup after the same run returns to Running', async () => {
    const lookup = deferred<ReturnType<typeof pending>>()
    const getRun = vi.fn(() => lookup.promise)
    const chatStream = vi.fn(async function* () {
      yield { event: 'start', data: { runID: 'r-approval', sessionID: 's-approval' } } as SSEEvent
      throw new Error('connection closed')
    })
    const { el, store } = await mountChat(chatStream, { getRun })
    await send(el, 'describe models')

    getRun.mockClear() // Ignore the status read started by stream recovery.
    event(store, 'PendingApproval')
    event(store, 'Running')
    lookup.resolve(pending('stale'))
    await settle(8)

    expect(getRun).toHaveBeenCalledTimes(1)
    expect(el.querySelector('.agents-approval')).toBeNull()
    expect(text(el)).not.toContain('stale')
  })

  it('restores every live approval after a different run finishes and reloads the transcript', async () => {
    const runIDs = ['r-a', 'r-b', 'r-c']
    let nextRun = 0
    const chatStream = vi.fn(async function* () {
      const runID = runIDs[nextRun++]
      yield { event: 'start', data: { runID, sessionID: 's-shared' } } as SSEEvent
      yield { event: 'approval_required', data: { runID, inboxID: 'inbox-' + runID, tool: 'describe_model', args: '{}' } } as SSEEvent
      yield { event: 'done', data: { runID, status: 'waiting' } } as SSEEvent
    })
    const transcript: TranscriptMessage[] = [
      { id: 'user-a', runID: 'r-a', role: 'user', content: 'first run' },
      { id: 'assistant-a', runID: 'r-a', role: 'assistant', content: '', metadata: { turnPhase: 'terminal', turnStatus: 'waiting' } },
      { id: 'user-b', runID: 'r-b', role: 'user', content: 'second run' },
      { id: 'assistant-b', runID: 'r-b', role: 'assistant', content: 'finished', metadata: { turnPhase: 'terminal', turnStatus: 'completed' } },
      { id: 'user-c', runID: 'r-c', role: 'user', content: 'third run' },
      { id: 'assistant-c', runID: 'r-c', role: 'assistant', content: '', metadata: { turnPhase: 'terminal', turnStatus: 'waiting' } },
    ]
    let persistedRuns: Array<{ id: string; phase: string; sessionID: string }> = runIDs.map(id => ({ id, phase: 'PendingApproval', sessionID: 's-shared' }))
    const listRuns = vi.fn((query: { limit?: number }) => Promise.resolve({
      items: query.limit ? persistedRuns.slice(0, query.limit) : persistedRuns,
    }))
    const listMessagePage = vi.fn(async () => ({ items: transcript.slice().reverse() }))
    const getRun = vi.fn((runID: string) => Promise.resolve(pending('inbox-' + runID, runID)))
    const { el, store } = await mountChat(chatStream, { listMessagePage, listRuns, getRun, listSessions: async () => [session('s-shared', 'Existing runs')] })
    expect(el.querySelectorAll('.agents-approval')).toHaveLength(3)

    listMessagePage.mockResolvedValue({ items: transcript.slice().reverse() })
    persistedRuns = [
      ...Array.from({ length: 5 }, (_, index) => ({ id: 'r-history-' + index, phase: 'Succeeded', sessionID: 's-shared' })),
      { id: 'r-b', phase: 'Running', sessionID: 's-shared' },
      { id: 'r-c', phase: 'PendingApproval', sessionID: 's-shared' },
      { id: 'r-a', phase: 'PendingApproval', sessionID: 's-shared' },
    ]
    getRun.mockClear()
    event(store, 'Succeeded', 'r-b')
    await settle(12)

    expect(listRuns).toHaveBeenCalledWith({ agent: 'scout', session: 's-shared' })
    expect(getRun).toHaveBeenCalledWith('r-a')
    expect(getRun).toHaveBeenCalledWith('r-c')
    expect(getRun).not.toHaveBeenCalledWith('r-b')
    expect(el.querySelectorAll('.agents-approval')).toHaveLength(2)
    expect(text(el)).toContain('inbox-r-a')
    expect(text(el)).toContain('inbox-r-c')
    expect(text(el)).not.toContain('inbox-r-b')
    const terminalMessage = [...el.querySelectorAll<HTMLElement>('.agents-ai-message')]
      .find(message => message.id === 'k-ai-conversation-turn-massistant-b')
    expect(terminalMessage).toBeDefined()
    expect(terminalMessage?.querySelector('.k-ai-turn-progress')).toBeNull()
  })

  it.each(['cancel', 'navigate'] as const)('discards a pending disclosure fetched before %s', async reason => {
    const lookup = deferred<ReturnType<typeof pending>>()
    const getRun = vi.fn(() => lookup.promise)
    const { el, store, view } = await mountChat(scripted(frames), { getRun })
    await send(el, 'describe models')
    event(store, 'PendingApproval')
    if (reason === 'cancel') {
      event(store, 'Aborted')
      // A delayed nonterminal event after the terminal one must not start a
      // fresh lookup from stale run details.
      event(store, 'PendingApproval')
    } else await view.setProps({ name: 'another-agent' })
    await settle(6)
    lookup.resolve(pending('stale'))
    await settle(6)
    expect(getRun).toHaveBeenCalledTimes(1)
    expect(el.querySelector('.agents-approval')).toBeNull()
    expect(text(el)).not.toContain('stale')
  })
})

describe('rich composer admission boundary', () => {
  const authOverride = { contextAuthority: () => attachmentAuthority }

  it('keeps attachment receipts and text when a provisional start fails before admission', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 'source-session')
    const admission = deferred<void>()
    const upload = vi.fn(async () => draftAttachment)
    const remove = vi.fn(async () => undefined)
    const chatStream = vi.fn(async function* (_agent: string, _message: string, _sessionID: string, _signal: AbortSignal, _turn: unknown) {
      yield { event: 'start', data: { runID: 'provisional', sessionID: 'source-session' } } as SSEEvent
      await admission.promise
      throw new Error('admission failed')
    })
    const { el } = await mountChat(chatStream, {
      ...authOverride,
      uploadAgentAttachment: upload,
      deleteAgentAttachment: remove,
    })
    const draftKey = agentChatDraftStorageKey(attachmentAuthority, 'scout', 'source-session')

    await attachTextFile(el)
    expect(upload).toHaveBeenCalledOnce()
    expect(text(el.querySelector('.agents-composer'))).toContain('notes.txt')
    setComposerText(el, 'Retry the change')
    await settle()
    el.querySelector<HTMLFormElement>('.agents-composer')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(6)
    admission.reject(new Error('admission failed'))
    await settle(10)

    expect(chatStream.mock.calls[0][4]).toEqual(expect.objectContaining({
      contentParts: expect.arrayContaining([{ type: 'attachment', attachment: draftAttachment }]),
    }))
    expect(composerText(el)).toBe('Retry the change')
    expect(text(el.querySelector('.agents-composer'))).toContain('notes.txt')
    expect(readAgentChatDraft(draftKey)).toEqual(expect.objectContaining({
      content: 'Retry the change',
      contentParts: expect.arrayContaining([{ type: 'attachment', attachment: draftAttachment }]),
    }))
    expect(remove).not.toHaveBeenCalled()
  })

  it('preserves follow-up text authored while admission is pending and reports the failed send', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 'source-session')
    const admission = deferred<void>()
    const upload = vi.fn(async () => draftAttachment)
    const remove = vi.fn(async () => undefined)
    const chatStream = vi.fn(async function* (_agent: string, _message: string, _sessionID: string, _signal: AbortSignal) {
      yield { event: 'start', data: { runID: 'provisional', sessionID: 'source-session' } } as SSEEvent
      await admission.promise
      throw new Error('admission failed')
    })
    const { el } = await mountChat(chatStream, {
      ...authOverride,
      uploadAgentAttachment: upload,
      deleteAgentAttachment: remove,
    })
    const draftKey = agentChatDraftStorageKey(attachmentAuthority, 'scout', 'source-session')

    await attachTextFile(el)
    setComposerText(el, 'First request')
    await settle()
    el.querySelector<HTMLFormElement>('.agents-composer')!.dispatchEvent(new Event('submit', { cancelable: true }))
    await settle(6)
    setComposerText(el, 'Follow-up draft')
    await settle(2)
    admission.reject(new Error('503 unavailable'))
    await settle(10)

    expect(composerText(el)).toBe('Follow-up draft')
    expect(text(el.querySelector('.agents-composer'))).toContain('notes.txt')
    expect(readAgentChatDraft(draftKey)).toEqual(expect.objectContaining({
      content: 'Follow-up draft',
      contentParts: expect.arrayContaining([{ type: 'attachment', attachment: draftAttachment }]),
    }))
    expect(text(document.getElementById('k-toasts'))).toContain('The previous send was not accepted. Your newer draft is still here.')
    expect(remove).not.toHaveBeenCalled()
  })

  it('submits an attachment-only turn and commits its receipt after run admission', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 'source-session')
    const upload = vi.fn(async () => draftAttachment)
    const remove = vi.fn(async () => undefined)
    const chatStream = vi.fn(async function* (_agent: string, _message: string, _sessionID: string, _signal: AbortSignal, _turn: unknown) {
      yield { event: 'start', data: { runID: 'accepted', sessionID: 'source-session' } } as SSEEvent
      yield { event: 'run_started', data: { runID: 'accepted', sessionID: 'source-session', status: 'running' } } as SSEEvent
      yield { event: 'done', data: { runID: 'accepted', content: 'Read the note.', status: 'completed' } } as SSEEvent
    })
    const { el } = await mountChat(chatStream, {
      ...authOverride,
      uploadAgentAttachment: upload,
      deleteAgentAttachment: remove,
    })
    const draftKey = agentChatDraftStorageKey(attachmentAuthority, 'scout', 'source-session')

    await attachTextFile(el)
    await send(el, '')

    expect(chatStream.mock.calls[0][1]).toBe('')
    expect(chatStream.mock.calls[0][4]).toEqual(expect.objectContaining({
      contentParts: expect.arrayContaining([{ type: 'attachment', attachment: draftAttachment }]),
    }))
    expect(localStorage.getItem(draftKey)).toBeNull()
    expect(text(el.querySelector('.agents-composer'))).not.toContain('notes.txt')
    expect(remove).not.toHaveBeenCalled()
  })

  it('cleans up the captured draft on detached admission without clearing the new session', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 'source-session')
    const admission = deferred<void>()
    const upload = vi.fn(async () => draftAttachment)
    const remove = vi.fn(async () => undefined)
    let signal!: AbortSignal
    const chatStream = vi.fn(async function* (_agent: string, _message: string, sessionID: string, controller: AbortSignal) {
      signal = controller
      yield { event: 'start', data: { runID: 'detached', sessionID } } as SSEEvent
      await admission.promise
      yield { event: 'run_started', data: { runID: 'detached', sessionID, status: 'running' } } as SSEEvent
    })
    const { el } = await mountChat(chatStream, {
      ...authOverride,
      uploadAgentAttachment: upload,
      deleteAgentAttachment: remove,
    })
    const oldDraftKey = agentChatDraftStorageKey(attachmentAuthority, 'scout', 'source-session')
    await attachTextFile(el)
    await send(el, 'Original session request')
    await settle(4)
    expect(readAgentChatDraft(oldDraftKey).content).toBe('Original session request')

    el.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!.click()
    await settle(6)
    expect(signal.aborted).toBe(false)
    setComposerText(el, 'New session draft')
    await settle()
    const newSessionID = localStorage.getItem('railgrid:agents:session:org:ws:scout')!
    const newDraftKey = agentChatDraftStorageKey(attachmentAuthority, 'scout', newSessionID)

    admission.resolve()
    await settle(10)

    expect(signal.aborted).toBe(true)
    expect(localStorage.getItem(oldDraftKey)).toBeNull()
    expect(composerText(el)).toBe('New session draft')
    expect(readAgentChatDraft(newDraftKey).content).toBe('New session draft')
    expect(remove).not.toHaveBeenCalled()
  })
})

describe('conversation model picker', () => {
  async function configureModels(store: ReturnType<typeof makeStore>): Promise<void> {
    store.credentials.data = [
      { name: 'openai', provider: 'openai', model: 'gpt-4o' },
      { name: 'sol', provider: 'openai', model: 'gpt-6-sol' },
      { name: 'harness-login', provider: 'codex' },
    ]
    store.credentials.loaded = true
    store.dispatchEvent(new Event('change'))
    await settle()
  }

  function expectSingleModelError(message = 'Chat model could not be saved') {
    const alerts = [...document.querySelectorAll('[role="alert"]')].filter(alert => text(alert).includes(message))
    expect(alerts).toHaveLength(1)
  }

  const picker = (el: HTMLElement) => el.querySelector<HTMLButtonElement>('.k-ai-model-reasoning-picker__trigger')!
  async function chooseModel(el: HTMLElement, name: string) {
    picker(el).click()
    await settle()
    document.querySelector<HTMLButtonElement>('[aria-label^="Choose model:"]')?.click()
    await settle()
    const option = [...document.querySelectorAll<HTMLButtonElement>('[role="option"]')].find(item => text(item).includes(name))!
    option.click()
    await settle()
  }

  it('keeps the conversation model picker compact as the pane widens', async () => {
    const { el, store } = await mountChat(scripted([]))
    await configureModels(store)

    const modelPicker = el.querySelector('.k-ai-model-reasoning-picker')
    expect(modelPicker).not.toBeNull()
    expect(modelPicker?.classList.contains('k-ai-model-reasoning-picker--compact')).toBe(true)
  })

  it('saves only the current chat, blocks sending until saved, and keeps the draft', async () => {
    const pending = deferred<any>()
    const saveSessionModel = vi.fn(() => pending.promise)
    const patchAgent = vi.fn()
    const chatStream = vi.fn(scripted([]))
    const { el, store } = await mountChat(chatStream, { saveSessionModel, patchAgent })
    await configureModels(store)
    await chooseModel(el, 'sol')
    expect(saveSessionModel).toHaveBeenCalledWith('scout', expect.any(String), expect.objectContaining({ modelCredential: 'sol' }))
    expect(patchAgent).not.toHaveBeenCalled()
    expect(document.querySelector('.k-ai-model-reasoning-picker__panel')).not.toBeNull()
    await send(el, 'Keep this draft')
    expect(chatStream).not.toHaveBeenCalled()
    pending.resolve({ selectedCredential: 'sol', modelCredential: 'sol', effortOptions: ['low', 'high'], defaultEffort: '' })
    await settle(8)
    expect(composerText(el)).toBe('Keep this draft')
    expect(picker(el).getAttribute('aria-label')).toContain('sol')
    expect(store.agents.data[0].spec.backend!.model!.credentials!.chat).toBe('openai')
  })

  it('keeps a saved preference-only chat visible and deletable after starting a new thread', async () => {
    const pendingSave = deferred<void>()
    let savedSession: SessionMeta | null = null
    const storedPreferences = new Map<string, any>()
    const inherited = {
      selectedCredential: 'openai', modelCredential: '', effortOptions: ['low', 'high'], defaultEffort: 'high',
      reasoningEffortByModel: {},
    }
    const listSessions = vi.fn(async () => savedSession ? [savedSession] : [])
    const getSessionModel = vi.fn(async (_name: string, id: string) => storedPreferences.get(id) ?? inherited)
    const saveSessionModel = vi.fn((_name: string, id: string, preferences: any) => pendingSave.promise.then(() => {
      const saved = {
        ...preferences,
        selectedCredential: 'sol', modelCredential: 'sol', effortOptions: ['low', 'high'], defaultEffort: '',
      }
      storedPreferences.set(id, saved)
      savedSession = { id, preview: 'New chat', messageCount: 0, createdAt: '', lastActivity: '' }
      return saved
    }))
    const deleteSession = vi.fn(async (_name: string, id: string) => {
      if (savedSession?.id === id) savedSession = null
      storedPreferences.delete(id)
    })
    const { el, store } = await mountChat(scripted([]), {
      listSessions, getSessionModel, saveSessionModel, deleteSession,
    })
    await configureModels(store)
    await chooseModel(el, 'sol')

    const savedDraftID = saveSessionModel.mock.calls[0][1]
    expect(savedDraftID).toBeTruthy()
    el.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!.click()
    await settle(6)
    const newThreadID = localStorage.getItem('railgrid:agents:session:org:ws:scout')
    expect(newThreadID).toBeTruthy()
    expect(newThreadID).not.toBe(savedDraftID)

    // The preference write completes after New thread has changed ownership of
    // the editor. It must refresh only this Agent's list, without applying the
    // old model result to the new thread.
    pendingSave.resolve()
    await settle(10)
    expect(listSessions).toHaveBeenCalledTimes(2)
    const savedDraftButton = el.querySelector<HTMLButtonElement>(`[data-thread-id="${savedDraftID}"]`)
    expect(savedDraftButton).not.toBeNull()
    expect(savedDraftButton!.closest('.k-ai-conversation-rail__item')?.textContent).toContain('New chat')
    expect(picker(el).getAttribute('aria-label')).toContain('openai')
    expect(picker(el).getAttribute('aria-label')).not.toContain('sol')

    savedDraftButton!.click()
    await settle(8)
    expect(getSessionModel).toHaveBeenCalledWith('scout', savedDraftID)
    expect(picker(el).getAttribute('aria-label')).toContain('sol')

    // Keep the preference-only thread inactive so deletion exercises the row's
    // own ID rather than whichever empty placeholder happens to be current.
    el.querySelector<HTMLButtonElement>('.k-ai-conversation-rail__create')!.click()
    await settle(6)
    const row = [...el.querySelectorAll<HTMLElement>('.k-ai-conversation-rail__item')]
      .find(item => item.querySelector(`[data-thread-id="${savedDraftID}"]`))
    expect(row).toBeDefined()
    row!.querySelector<HTMLButtonElement>('button[aria-label="Delete chat"]')!.click()
    await settle(2)
    resolveConfirm(true)
    await settle(8)

    expect(deleteSession).toHaveBeenCalledWith('scout', savedDraftID)
    expect(el.querySelector(`[data-thread-id="${savedDraftID}"]`)).toBeNull()
    expect(localStorage.getItem('railgrid:agents:session:org:ws:scout')).not.toBe(savedDraftID)
  })

  it('refreshes empty chats and gives recovery guidance after a preference quota response', async () => {
    const savedSession: SessionMeta = { id: 'unused-empty-chat', preview: 'New chat', messageCount: 0, createdAt: '', lastActivity: '' }
    const listSessions = vi.fn()
      .mockResolvedValueOnce([])
      .mockImplementation(async () => [savedSession])
    const deleteSession = vi.fn().mockResolvedValue(undefined)
    const { el, store } = await mountChat(scripted([]), {
      listSessions,
      deleteSession,
      saveSessionModel: async () => { throw new ApiError(429, 'private backend detail', '', 'TooManyRequests') },
    })
    await configureModels(store)
    await chooseModel(el, 'sol')
    await settle(8)

    expect(listSessions).toHaveBeenCalledTimes(2)
    expectSingleModelError('Chat limit reached. Delete an unused chat, then retry.')
    expect(text(document.body)).not.toContain('private backend detail')
    const emptyChat = el.querySelector(`[data-thread-id="${savedSession.id}"]`)
    expect(emptyChat).not.toBeNull()

    const row = emptyChat!.closest('.k-ai-conversation-rail__item')!
    row.querySelector<HTMLButtonElement>('button[aria-label="Delete chat"]')!.click()
    await settle(2)
    resolveConfirm(true)
    await settle(8)
    expect(deleteSession).toHaveBeenCalledWith('scout', savedSession.id)
  })

  it('rolls back a rejected save and exposes a retry', async () => {
    const { el, store } = await mountChat(scripted([]), {
      saveSessionModel: async () => { throw new Error('denied') },
    })
    await configureModels(store)
    await chooseModel(el, 'sol')
    await settle(8)
    expectSingleModelError()
    expect(picker(el).getAttribute('aria-label')).toBe('Model: openai')
  })

  it('refreshes inherited state after a failed explicit choice when the Agent default changes', async () => {
    const getSessionModel = vi.fn()
      .mockResolvedValueOnce({
        selectedCredential: 'openai', modelCredential: '', effortOptions: ['low'], defaultEffort: '',
        reasoningEffortByModel: {},
      })
      .mockResolvedValueOnce({
        selectedCredential: 'sol', modelCredential: '', effortOptions: ['medium', 'high'], defaultEffort: 'high',
        reasoningEffortByModel: {},
      })
    const { el, store } = await mountChat(scripted([]), {
      getSessionModel,
      saveSessionModel: async () => { throw new Error('denied') },
    })
    await configureModels(store)
    await chooseModel(el, 'sol')
    await settle(8)
    expectSingleModelError()
    expect(getSessionModel).toHaveBeenCalledTimes(1)

    store.agents.data[0].spec.backend = modelBackend('sol')
    store.dispatchEvent(new Event('change'))
    await settle(10)

    expect(getSessionModel).toHaveBeenCalledTimes(2)
    expect(picker(el).getAttribute('aria-label')).toContain('sol')
    expect(picker(el).getAttribute('aria-label')).not.toContain('could not be loaded')
    expectSingleModelError()
  })

  it('refreshes inherited metadata after a pending explicit save fails and preserves its retry', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 'old')
    const explicitSave = deferred<any>()
    const staleInheritedRead = deferred<any>()
    const inherited = (selectedCredential: string, effortOptions: string[], defaultEffort = '') => ({
      selectedCredential, modelCredential: '', effortOptions, defaultEffort, reasoningEffortByModel: {},
    })
    let oldReads = 0
    const getSessionModel = vi.fn(async (_name: string, id: string) => {
      if (id !== 'old') return inherited('deepseek', ['low', 'high'], 'high')
      oldReads += 1
      if (oldReads === 1) return inherited('openai', ['low'], '')
      if (oldReads === 2) return inherited('claude', [], '')
      if (oldReads === 3) return staleInheritedRead.promise
      return inherited('deepseek', ['low', 'medium', 'high'], 'high')
    })
    const saveSessionModel = vi.fn()
      .mockImplementationOnce(() => explicitSave.promise)
      .mockImplementationOnce(async (_name: string, _session: string, preferences: any) => ({
        ...preferences, selectedCredential: 'sol', modelCredential: 'sol', effortOptions: ['low', 'high'], defaultEffort: '',
      }))
    const pendingAgentSave = deferred<unknown>()
    const patchAgent = vi.fn(() => pendingAgentSave.promise)
    const listAgents = vi.fn(async () => [agentFixture('scout', { backend: modelBackend('claude') })])
    const { el, api, store } = await mountChat(scripted([]), {
      listSessions: async () => [session('old', 'Old chat'), session('new', 'New chat')],
      getSessionModel, saveSessionModel, patchAgent, listAgents,
    })
    await configureModels(store)
    store.credentials.data.push(
      { name: 'claude', provider: 'anthropic', model: 'claude-model' },
      { name: 'gemini', provider: 'google', model: 'gemini-model' },
      { name: 'deepseek', provider: 'deepseek', model: 'deepseek-model' },
    )
    store.dispatchEvent(new Event('change'))
    await settle()

    await chooseModel(el, 'sol')
    expect(saveSessionModel).toHaveBeenCalledWith('scout', 'old', expect.objectContaining({ modelCredential: 'sol' }))
    const agentSave = queueAgentConfigSave({
      store,
      api,
      name: 'scout',
      patch: { modelCredential: 'claude' },
      apply: spec => { spec.backend = modelBackend('claude') },
      success: 'Saved model settings',
    })
    await settle(4)
    expect(patchAgent).toHaveBeenCalledTimes(1)
    expect(getSessionModel).toHaveBeenCalledTimes(1)

    explicitSave.reject(new Error('save failed'))
    await settle(6)
    expectSingleModelError()
    expect(getSessionModel).toHaveBeenCalledTimes(1)

    pendingAgentSave.resolve({ metadata: { name: 'scout' }, spec: {} })
    await agentSave
    await settle(8)
    expect(getSessionModel.mock.calls.filter(([, id]) => id === 'old')).toHaveLength(2)
    expect(picker(el).getAttribute('aria-label')).toContain('claude')
    picker(el).click()
    await settle()
    expect(document.querySelector('[data-testid="reasoning-effort-slider"]')).toBeNull()
    expectSingleModelError()

    store.agents.data[0].spec.backend = modelBackend('gemini')
    store.dispatchEvent(new Event('change'))
    await settle(6)
    expect(getSessionModel.mock.calls.filter(([, id]) => id === 'old')).toHaveLength(3)
    store.agents.data[0].spec.backend = modelBackend('deepseek')
    store.dispatchEvent(new Event('change'))
    await settle()
    staleInheritedRead.resolve(inherited('gemini', ['low'], ''))
    await settle(10)
    expect(getSessionModel.mock.calls.filter(([, id]) => id === 'old')).toHaveLength(4)
    expect(picker(el).getAttribute('aria-label')).toContain('deepseek')
    expectSingleModelError()
    expect(patchAgent).toHaveBeenCalledTimes(1)

    picker(el).click()
    await settle()
    const retry = [...document.querySelectorAll<HTMLButtonElement>('.k-ai-model-reasoning-picker__recovery-actions button')]
      .find(button => text(button) === 'Retry')
    expect(retry).toBeDefined()
    retry!.click()
    await settle(8)
    expect(saveSessionModel).toHaveBeenNthCalledWith(2, 'scout', 'old', expect.objectContaining({
      selectedCredential: 'sol', modelCredential: 'sol',
    }))
    expect(picker(el).getAttribute('aria-label')).toContain('sol')
    expect(text(document.body)).not.toContain('Chat model could not be saved')

    await chooseSession(el, 'New chat')
    await settle(8)
    expect(getSessionModel).toHaveBeenCalledWith('scout', 'new')
    expect(picker(el).getAttribute('aria-label')).toContain('deepseek')
    expect(saveSessionModel.mock.calls.map(([, id]) => id)).toEqual(['old', 'old'])
  })

  it('ignores a late failed model save after switching chats', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 'first')
    const pendingSave = deferred<any>()
    const inherited = (selectedCredential: string) => ({
      selectedCredential, modelCredential: '', effortOptions: [], defaultEffort: '', reasoningEffortByModel: {},
    })
    let defaultCredential = 'openai'
    const getSessionModel = vi.fn(async () => inherited(defaultCredential))
    const saveSessionModel = vi.fn(() => pendingSave.promise)
    const { el, store } = await mountChat(scripted([]), {
      listSessions: async () => [session('first', 'First chat'), session('second', 'Second chat')],
      getSessionModel, saveSessionModel,
    })
    await configureModels(store)
    store.credentials.data.push({ name: 'claude', provider: 'anthropic', model: 'claude-model' })
    store.dispatchEvent(new Event('change'))
    await settle()

    await chooseModel(el, 'sol')
    defaultCredential = 'claude'
    store.agents.data[0].spec.backend = modelBackend('claude')
    store.dispatchEvent(new Event('change'))
    await settle()
    expect(getSessionModel).toHaveBeenCalledTimes(1)

    await chooseSession(el, 'Second chat')
    expect(picker(el).getAttribute('aria-label')).toContain('claude')
    pendingSave.reject(new Error('old chat save failed'))
    await settle(8)
    expect(text(document.body)).not.toContain('Chat model could not be saved')
    expect(picker(el).getAttribute('aria-label')).toContain('claude')
    expect(saveSessionModel).toHaveBeenCalledTimes(1)

    await chooseSession(el, 'First chat')
    await settle(8)
    expect(getSessionModel).toHaveBeenCalledWith('scout', 'first')
    expect(picker(el).getAttribute('aria-label')).toContain('claude')
    expect(saveSessionModel).toHaveBeenCalledTimes(1)
  })

  it('keeps the failed explicit choice retryable when its inherited refresh also fails', async () => {
    localStorage.setItem('railgrid:agents:session:org:ws:scout', 'old')
    const explicitSave = deferred<any>()
    const inherited = (selectedCredential: string) => ({
      selectedCredential, modelCredential: '', effortOptions: [], defaultEffort: '', reasoningEffortByModel: {},
    })
    const getSessionModel = vi.fn()
      .mockResolvedValueOnce(inherited('openai'))
      .mockRejectedValueOnce(new Error('refresh failed'))
    const saveSessionModel = vi.fn()
      .mockImplementationOnce(() => explicitSave.promise)
      .mockImplementationOnce(async (_name: string, _session: string, preferences: any) => ({
        ...preferences, selectedCredential: 'sol', modelCredential: 'sol', effortOptions: [], defaultEffort: '',
      }))
    const { el, store } = await mountChat(scripted([]), {
      listSessions: async () => [session('old', 'Old chat')],
      getSessionModel, saveSessionModel,
    })
    await configureModels(store)
    store.credentials.data.push({ name: 'claude', provider: 'anthropic', model: 'claude-model' })
    store.dispatchEvent(new Event('change'))
    await settle()

    await chooseModel(el, 'sol')
    store.agents.data[0].spec.backend = modelBackend('claude')
    store.dispatchEvent(new Event('change'))
    explicitSave.reject(new Error('save failed'))
    await settle(8)

    expect(getSessionModel).toHaveBeenCalledTimes(2)
    expect(picker(el).getAttribute('aria-label')).toContain('claude')
    expectSingleModelError()
    expect(el.querySelector('.k-ai-composer__help')).toBeNull()
    const retries = [...document.querySelectorAll<HTMLButtonElement>('button')]
      .filter(button => text(button) === 'Retry')
    expect(retries).toHaveLength(1)
    retries[0].click()
    await settle(8)

    expect(saveSessionModel).toHaveBeenNthCalledWith(2, 'scout', 'old', expect.objectContaining({
      selectedCredential: 'sol', modelCredential: 'sol',
    }))
    expect(picker(el).getAttribute('aria-label')).toContain('sol')
    expect(text(document.body)).not.toContain('Chat model could not be saved')
  })

  it('lets the user reselect the displayed default after saved-model access is denied', async () => {
    const saveSessionModel = vi.fn(async (_name: string, _session: string, preferences: any) => ({
      ...preferences,
      selectedCredential: preferences.modelCredential,
      effortOptions: ['low', 'high'],
      defaultEffort: '',
    }))
    const { el, store } = await mountChat(scripted([]), {
      getSessionModel: async () => { throw new ApiError(403, 'Access denied', 'forbidden') },
      saveSessionModel,
    })
    store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'gpt-4o' }]
    store.credentials.loaded = true
    store.dispatchEvent(new Event('change'))
    await settle(8)

    expect(text(el)).toContain('Chat model could not be loaded')
    expect(picker(el).getAttribute('aria-label')).toBe('Model: openai')
    await chooseModel(el, 'openai')
    await settle(8)

    expect(saveSessionModel).toHaveBeenCalledWith('scout', expect.any(String), expect.objectContaining({
      modelCredential: 'openai',
    }))
    expect(text(el)).not.toContain('Chat model could not be loaded')
  })

  it('keeps the chosen effort visible during saving and restores it on failure', async () => {
    const pending = deferred<any>()
    const { el, store } = await mountChat(scripted([]), {
      getSessionModel: async () => ({ selectedCredential: 'sol', effortOptions: ['low', 'high'], defaultEffort: '' }),
      saveSessionModel: () => pending.promise,
    })
    await configureModels(store)
    picker(el).click()
    await settle()
    const slider = document.querySelector<HTMLInputElement>('[data-testid="reasoning-effort-slider"]')!
    slider.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true }))
    slider.value = '2'
    slider.dispatchEvent(new Event('input', { bubbles: true }))
    slider.dispatchEvent(new Event('change', { bubbles: true }))
    await settle()
    expect(picker(el).getAttribute('aria-label')).toContain('High')
    expect(document.querySelector('.k-ai-model-reasoning-picker__effort-heading')?.textContent).toBe('High')
    expect(el.querySelector('.k-ai-composer__help')).toBeNull()
    pending.reject(new Error('save failed'))
    await settle(8)
    expect(picker(el).getAttribute('aria-label')).toContain('Model default')
    expectSingleModelError()
    expect(el.querySelector('.k-ai-composer__help')).toBeNull()
  })

  it('shows all Haiku effort levels from the backend and saves an explicit choice for this chat', async () => {
    const capabilities = {
      selectedCredential: 'haiku', modelCredential: 'haiku',
      effortOptions: ['low', 'medium', 'high', 'xhigh', 'max'], defaultEffort: 'medium',
    }
    const saveSessionModel = vi.fn(async (_agent, _session, preferences) => ({ ...capabilities, ...preferences }))
    const { el, store } = await mountChat(scripted([]), {
      getSessionModel: async () => capabilities,
      saveSessionModel,
    })
    store.credentials.data = [{ name: 'haiku', provider: 'openai-compatible', baseURL: 'https://api.anthropic.com/v1', model: 'claude-haiku-5-5' }]
    store.credentials.loaded = true
    store.dispatchEvent(new Event('change'))
    await settle(8)
    picker(el).click()
    await settle()
    const slider = document.querySelector<HTMLInputElement>('[data-testid="reasoning-effort-slider"]')!
    expect(slider.max).toBe('5')
    expect(slider.getAttribute('aria-valuetext')).toContain('Medium')
    slider.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }))
    slider.value = '3'
    slider.dispatchEvent(new Event('input', { bubbles: true }))
    slider.dispatchEvent(new Event('change', { bubbles: true }))
    await settle(8)
    expect(saveSessionModel).toHaveBeenCalledWith('scout', expect.any(String), expect.objectContaining({
      modelCredential: 'haiku', reasoningEffortByModel: { haiku: 'high' },
    }))
    expect(picker(el).getAttribute('aria-label')).toContain('High')
  })

  it('waits for an Agent default save and authoritative reload before refreshing inherited capabilities', async () => {
    const pendingAgentSave = deferred<unknown>()
    const order: string[] = []
    const getSessionModel = vi.fn()
      .mockImplementationOnce(async () => {
        order.push('preferences')
        return { selectedCredential: 'openai', modelCredential: '', effortOptions: ['low'], defaultEffort: '' }
      })
      .mockImplementationOnce(async () => {
        order.push('preferences')
        return { selectedCredential: 'sol', modelCredential: '', effortOptions: ['low', 'high'], defaultEffort: 'high' }
      })
    const patchAgent = vi.fn(() => {
      order.push('patch')
      return pendingAgentSave.promise
    })
    const listAgents = vi.fn(async () => {
      order.push('agents')
      return [agentFixture('scout', { backend: modelBackend('sol') })]
    })
    const { el, api, store } = await mountChat(scripted([]), { getSessionModel, patchAgent, listAgents })
    await configureModels(store)
    expect(getSessionModel).toHaveBeenCalledTimes(1)

    const save = queueAgentConfigSave({
      store,
      api,
      name: 'scout',
      patch: { modelCredential: 'sol' },
      apply: spec => { spec.backend = modelBackend('sol') },
      success: 'Saved model settings',
    })
    await settle(4)
    expect(patchAgent).toHaveBeenCalledTimes(1)
    expect(getSessionModel).toHaveBeenCalledTimes(1)

    pendingAgentSave.resolve({ metadata: { name: 'scout' }, spec: {} })
    await save
    await settle(10)

    expect(listAgents).toHaveBeenCalledTimes(1)
    expect(order.indexOf('agents')).toBeLessThan(order.lastIndexOf('preferences'))
    expect(getSessionModel).toHaveBeenCalledTimes(2)
    expect(picker(el).getAttribute('aria-label')).toContain('sol')
    picker(el).click()
    await settle()
    expect(document.querySelector<HTMLInputElement>('[data-testid="reasoning-effort-slider"]')?.max).toBe('2')
    expect(text(document.querySelector('.k-ai-model-reasoning-picker__effort-heading'))).toBe('Model default')
    expect(text(document.querySelector('.k-ai-model-reasoning-picker__effort-heading')?.nextElementSibling)).toContain('Using High as the model default')
  })

  it('recovers an inherited conversation after a saved default replaces its deleted model', async () => {
    const sessionID = 'same-conversation'
    localStorage.setItem('railgrid:agents:session:org:ws:scout', sessionID)
    const pendingAgentSave = deferred<unknown>()
    const refreshedPreferences = deferred<any>()
    const newDefault = 'claude-haiku-5-5'
    const history = [{ id: 'existing-answer', role: 'assistant' as const, content: 'Earlier answer', createdAt: '' }]
    const getSessionModel = vi.fn()
      .mockResolvedValueOnce({
        selectedCredential: 'openai', modelCredential: '', effortOptions: ['low'], defaultEffort: 'low',
        reasoningEffortByModel: { openai: 'low' },
      })
      .mockReturnValueOnce(refreshedPreferences.promise)
    const patchAgent = vi.fn(() => pendingAgentSave.promise)
    const listAgents = vi.fn(async () => [agentFixture('scout', { backend: modelBackend(newDefault) })])
    const chatStream = vi.fn(scripted([]))
    const { el, api, store } = await mountChat(chatStream, {
      listSessions: async () => [session(sessionID, 'Existing conversation')],
      listMessagePage: async () => ({ items: history }),
      getSessionModel,
      patchAgent,
      listAgents,
    })
    await configureModels(store)
    setComposerText(el, 'Keep this unsent draft')
    await settle()
    expect(text(el)).toContain('Earlier answer')

    // The old configured credential disappears before the replacement Agent
    // default has finished saving. Chat must retain the same conversation and
    // wait for that save before reading the inherited preference again.
    store.credentials.data = [{ name: newDefault, provider: 'anthropic', model: 'claude-haiku-5-5' }]
    store.dispatchEvent(new Event('change'))
    await settle(8)
    expect(text(el)).toContain('Choose a model to continue')
    store.setPendingPrompt('scout', 'Do not send this queued prompt')

    const save = queueAgentConfigSave({
      store,
      api,
      name: 'scout',
      patch: { modelCredential: newDefault },
      apply: spec => { spec.backend = modelBackend(newDefault) },
      success: 'Saved model settings',
    })
    await settle(4)
    expect(patchAgent).toHaveBeenCalledOnce()
    expect(getSessionModel).toHaveBeenCalledTimes(1)
    expect(composerText(el)).toBe('Keep this unsent draft')

    pendingAgentSave.resolve({ metadata: { name: 'scout' }, spec: { backend: modelBackend(newDefault) } })
    await save
    await settle(8)
    expect(listAgents).toHaveBeenCalledOnce()
    expect(getSessionModel).toHaveBeenCalledTimes(2)
    expect(getSessionModel).toHaveBeenNthCalledWith(2, 'scout', sessionID)
    // While the fresh capability read is still outstanding, the effective
    // choice follows the saved Agent default instead of the deleted name.
    expect(picker(el).getAttribute('aria-label')).toContain(newDefault)
    expect(text(el)).not.toContain('Choose a model to continue')

    refreshedPreferences.resolve({
      selectedCredential: newDefault, modelCredential: '', effortOptions: ['low', 'medium'], defaultEffort: 'medium',
      reasoningEffortByModel: {},
    })
    await settle(10)
    expect(text(el)).toContain('Earlier answer')
    expect(composerText(el)).toBe('Keep this unsent draft')
    expect(localStorage.getItem('railgrid:agents:session:org:ws:scout')).toBe(sessionID)
    expect(store.takePendingPrompt('scout')).toBe('Do not send this queued prompt')
    expect(chatStream).not.toHaveBeenCalled()
  })

  it('refreshes the rolled-back inherited model after an Agent default save fails', async () => {
    const pendingAgentSave = deferred<unknown>()
    const getSessionModel = vi.fn()
      .mockResolvedValueOnce({ selectedCredential: 'openai', modelCredential: '', effortOptions: ['low'], defaultEffort: '' })
      .mockResolvedValueOnce({ selectedCredential: 'openai', modelCredential: '', effortOptions: ['low', 'high'], defaultEffort: 'high' })
    const patchAgent = vi.fn(() => pendingAgentSave.promise)
    const listAgents = vi.fn(async () => [agentFixture('scout')])
    const { el, api, store } = await mountChat(scripted([]), { getSessionModel, patchAgent, listAgents })
    await configureModels(store)
    expect(getSessionModel).toHaveBeenCalledTimes(1)

    const save = queueAgentConfigSave({
      store,
      api,
      name: 'scout',
      patch: { modelCredential: 'sol' },
      apply: spec => { spec.backend = modelBackend('sol') },
      success: 'Saved model settings',
    })
    await settle(4)
    expect(patchAgent).toHaveBeenCalledTimes(1)
    expect(getSessionModel).toHaveBeenCalledTimes(1)

    pendingAgentSave.reject(new Error('rejected'))
    await expect(save).resolves.toBe(false)
    await settle(10)

    expect(listAgents).toHaveBeenCalledTimes(1)
    expect(getSessionModel).toHaveBeenCalledTimes(2)
    expect(picker(el).getAttribute('aria-label')).toContain('openai')
    picker(el).click()
    await settle()
    expect(document.querySelector<HTMLInputElement>('[data-testid="reasoning-effort-slider"]')?.max).toBe('2')
    expect(text(document.querySelector('.k-ai-model-reasoning-picker__effort-heading')?.nextElementSibling)).toContain('Using High as the model default')
  })

  it('fences an inherited preference read when the Agent default changes in flight', async () => {
    const staleRead = deferred<any>()
    const getSessionModel = vi.fn()
      .mockReturnValueOnce(staleRead.promise)
      .mockResolvedValueOnce({
        selectedCredential: 'sol', modelCredential: '', effortOptions: ['medium', 'high'], defaultEffort: 'high',
        reasoningEffortByModel: {},
      })
    const { el, store } = await mountChat(scripted([]), { getSessionModel })
    await configureModels(store)
    await settle(6)
    expect(getSessionModel).toHaveBeenCalledTimes(1)

    store.agents.data[0].spec.backend = modelBackend('sol')
    store.dispatchEvent(new Event('change'))
    await settle(4)
    expect(getSessionModel).toHaveBeenCalledTimes(1)

    staleRead.resolve({
      selectedCredential: 'openai', modelCredential: '', effortOptions: ['low'], defaultEffort: 'low',
      reasoningEffortByModel: {},
    })
    await settle(10)

    expect(getSessionModel).toHaveBeenCalledTimes(2)
    expect(picker(el).getAttribute('aria-label')).toContain('sol')
    picker(el).click()
    await settle()
    expect(document.querySelector<HTMLInputElement>('[data-testid="reasoning-effort-slider"]')?.max).toBe('2')
    expect(text(document.querySelector('.k-ai-model-reasoning-picker__effort-heading')?.nextElementSibling)).toContain('Using High as the model default')
  })

  it('refreshes inherited capabilities after a pending effort save without closing the picker', async () => {
    const pendingEffortSave = deferred<any>()
    const getSessionModel = vi.fn()
      .mockResolvedValueOnce({
        selectedCredential: 'openai', modelCredential: '', effortOptions: ['low', 'high'], defaultEffort: '',
        reasoningEffortByModel: {},
      })
      .mockImplementationOnce(async (_name: string, _session: string) => ({
        selectedCredential: 'sol', modelCredential: '', effortOptions: ['low', 'medium', 'high'], defaultEffort: 'high',
        reasoningEffortByModel: {},
      }))
    const saveSessionModel = vi.fn((_name: string, _session: string, _preferences: any) => pendingEffortSave.promise)
    const { el, store } = await mountChat(scripted([]), { getSessionModel, saveSessionModel })
    await configureModels(store)
    expect(getSessionModel).toHaveBeenCalledTimes(1)

    picker(el).click()
    await settle()
    const slider = document.querySelector<HTMLInputElement>('[data-testid="reasoning-effort-slider"]')!
    slider.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true }))
    slider.value = '2'
    slider.dispatchEvent(new Event('input', { bubbles: true }))
    slider.dispatchEvent(new Event('change', { bubbles: true }))
    await settle()
    expect(saveSessionModel).toHaveBeenCalledWith('scout', expect.any(String), expect.objectContaining({
      modelCredential: '', reasoningEffortByModel: { openai: 'high' },
    }))
    expect(document.querySelector('.k-ai-model-reasoning-picker__panel')).not.toBeNull()
    expect(document.querySelector<HTMLButtonElement>('[data-testid="reasoning-model-trigger"]')?.disabled).toBe(true)
    document.querySelector<HTMLButtonElement>('[data-testid="reasoning-model-trigger"]')!.click()
    await settle()
    expect(document.querySelector('[data-testid="reasoning-model-list"]')).toBeNull()

    store.agents.data[0].spec.backend = modelBackend('sol')
    store.dispatchEvent(new Event('change'))
    await settle(4)
    expect(getSessionModel).toHaveBeenCalledTimes(1)

    const saved = saveSessionModel.mock.calls[0][2]
    pendingEffortSave.resolve({
      ...saved,
      selectedCredential: 'openai', modelCredential: '', effortOptions: ['low', 'high'], defaultEffort: '',
    })
    await settle(10)

    expect(getSessionModel).toHaveBeenCalledTimes(2)
    await expect(getSessionModel.mock.results[1].value).resolves.toMatchObject({
      selectedCredential: 'sol', reasoningEffortByModel: {},
    })
    expect(picker(el).getAttribute('aria-label')).toContain('sol')
    expect(document.querySelector('.k-ai-model-reasoning-picker__panel')).not.toBeNull()
    expect(document.querySelector<HTMLInputElement>('[data-testid="reasoning-effort-slider"]')?.max).toBe('3')
    expect(text(document.querySelector('.k-ai-model-reasoning-picker__effort-heading')?.nextElementSibling)).toContain('Using High as the model default')
  })

  it('preserves an explicit model and effort across Agent default changes without leaking them to a new chat', async () => {
    const getSessionModel = vi.fn(async (_name: string, id: string) => id === 'explicit'
      ? {
          selectedCredential: 'openai', modelCredential: 'openai', effortOptions: ['low', 'high', 'max'], defaultEffort: '',
          reasoningEffortByModel: { openai: 'max' },
        }
      : {
          selectedCredential: 'sol', modelCredential: '', effortOptions: ['low', 'high'], defaultEffort: 'high',
          reasoningEffortByModel: {},
        })
    const { el, store } = await mountChat(scripted([]), {
      listSessions: async () => [session('explicit', 'Explicit chat'), session('new', 'New chat')],
      getSessionModel,
    })
    await configureModels(store)
    expect(getSessionModel).toHaveBeenCalledWith('scout', 'explicit')
    expect(picker(el).getAttribute('aria-label')).toContain('openai')
    expect(picker(el).getAttribute('aria-label')).toContain('Max')

    store.agents.data[0].spec.backend = modelBackend('sol')
    store.dispatchEvent(new Event('change'))
    await settle(6)
    expect(getSessionModel).toHaveBeenCalledTimes(1)
    expect(picker(el).getAttribute('aria-label')).toContain('openai')
    expect(picker(el).getAttribute('aria-label')).toContain('Max')

    await chooseSession(el, 'New chat')
    await settle(8)
    expect(getSessionModel).toHaveBeenCalledWith('scout', 'new')
    expect(picker(el).getAttribute('aria-label')).toContain('sol')
    expect(picker(el).getAttribute('aria-label')).not.toContain('Max')
  })

  it('disables model switching during a running turn', async () => {
    const gate = deferred<void>()
    const { el, store } = await mountChat(scripted([], gate.promise))
    await configureModels(store)
    await send(el, 'Start a turn')
    expect(picker(el).disabled).toBe(true)
    gate.resolve()
    await settle(8)
    expect(picker(el).disabled).toBe(false)
  })

  it('restores reasoning per chat without patching the agent', async () => {
    const saveSessionModel = vi.fn(async (_name: string, _session: string, prefs: any) => prefs)
    const { el, store } = await mountChat(scripted([]), {
      listSessions: async () => [session('s1', 'First'), session('s2', 'Second')],
      getSessionModel: async (_name: string, id: string) => ({
        selectedCredential: 'sol', modelCredential: 'sol', effortOptions: ['low', 'high'], defaultEffort: '',
        reasoningEffortByModel: { sol: id === 's1' ? 'low' : 'high' },
      }),
      saveSessionModel,
    })
    await configureModels(store)
    expect(picker(el).getAttribute('aria-label')).toContain('Low')
    await chooseSession(el, 'Second')
    await settle(8)
    expect(picker(el).getAttribute('aria-label')).toContain('High')
    picker(el).click()
    await settle()
    document.querySelector<HTMLButtonElement>('[aria-label="Reset reasoning to model default"]')!.click()
    await settle(8)
    expect(saveSessionModel).toHaveBeenCalledWith('scout', 's2', expect.objectContaining({ reasoningEffortByModel: { sol: '' } }))
    expect(store.agents.data[0].spec.backend!.model!.credentials!.chat).toBe('openai')
  })

  it('waits for a pending save before reloading that same chat after switching away and back', async () => {
    const pendingSave = deferred<any>()
    const stored = new Map<string, any>([
      ['first', { selectedCredential: 'openai', modelCredential: '', effortOptions: ['low', 'high'], defaultEffort: '', reasoningEffortByModel: {} }],
      ['second', { selectedCredential: 'openai', modelCredential: '', effortOptions: ['low', 'high'], defaultEffort: '', reasoningEffortByModel: {} }],
    ])
    const getSessionModel = vi.fn(async (_name: string, id: string) => stored.get(id))
    const saveSessionModel = vi.fn((_name: string, id: string, preferences: any) => {
      if (id === 'first') return pendingSave.promise
      stored.set(id, preferences)
      return Promise.resolve(preferences)
    })
    const { el, store } = await mountChat(scripted([]), {
      listSessions: async () => [session('first', 'First chat'), session('second', 'Second chat')],
      getSessionModel,
      saveSessionModel,
    })
    const firstSessionID = 'first'
    await configureModels(store)
    await chooseModel(el, 'sol')
    expect(saveSessionModel).toHaveBeenCalledWith('scout', firstSessionID, expect.objectContaining({ modelCredential: 'sol' }))

    await chooseSession(el, 'Second chat')
    await settle(6)
    await chooseSession(el, 'First chat')
    await settle(6)
    expect(getSessionModel.mock.calls.filter(([, id]) => id === firstSessionID)).toHaveLength(1)

    const saved = { ...stored.get(firstSessionID), selectedCredential: 'sol', modelCredential: 'sol' }
    stored.set(firstSessionID, saved)
    pendingSave.resolve(saved)
    await settle(10)

    expect(getSessionModel.mock.calls.filter(([, id]) => id === firstSessionID)).toHaveLength(2)
    expect(picker(el).getAttribute('aria-label')).toContain('sol')
  })

  it('switches with selected-only responses without dropping hidden server-side efforts', async () => {
    // The server stores both preferences, but only returns the selected
    // credential's entry. Model the PATCH decoder merging an incoming JSON map
    // into its cloned, non-nil stored map instead of replacing that map.
    const storedEfforts: Record<string, string> = { openai: 'high', sol: 'max' }
    const responseFor = (selectedCredential: string, preferences: any) => ({
      ...preferences,
      selectedCredential,
      reasoningEffortByModel: storedEfforts[selectedCredential] === undefined
        ? {}
        : { [selectedCredential]: storedEfforts[selectedCredential] },
      effortOptions: ['low', 'high'],
      defaultEffort: '',
    })
    const saveSessionModel = vi.fn(async (_name: string, _session: string, preferences: any) => {
      Object.assign(storedEfforts, preferences.reasoningEffortByModel ?? {})
      const selectedCredential = preferences.modelCredential || preferences.selectedCredential
      // The stored sol preference is stale under the current model metadata;
      // the server clears that selected entry while retaining openai's entry.
      if (selectedCredential === 'sol') delete storedEfforts.sol
      return responseFor(selectedCredential, preferences)
    })
    const { el, store } = await mountChat(scripted([]), {
      getSessionModel: async () => responseFor('openai', { modelCredential: 'openai' }),
      saveSessionModel,
    })
    await configureModels(store)

    await chooseModel(el, 'sol')
    expect(saveSessionModel).toHaveBeenCalledWith('scout', expect.any(String), expect.objectContaining({
      reasoningEffortByModel: { openai: 'high' },
    }))
    expect(storedEfforts).toEqual({ openai: 'high' })
    expect(picker(el).getAttribute('aria-label')).toContain('Model default')

    document.querySelector<HTMLButtonElement>('[aria-label^="Choose model:"]')!.click()
    await settle()
    const openai = [...document.querySelectorAll<HTMLButtonElement>('[role="option"]')]
      .find(item => text(item).includes('openai'))
    expect(openai).toBeDefined()
    openai!.click()
    await settle(8)
    expect(saveSessionModel).toHaveBeenNthCalledWith(2, 'scout', expect.any(String), expect.objectContaining({
      reasoningEffortByModel: {},
    }))
    expect(storedEfforts).toEqual({ openai: 'high' })
    expect(picker(el).getAttribute('aria-label')).toContain('High')
  })

  it('omits the picker for harness execution', async () => {
    const { el, store } = await mountChat(scripted([]))
    store.agents.data[0].spec.backend = { type: 'harness' }
    store.dispatchEvent(new Event('change'))
    await settle()
    expect(el.querySelector('.k-ai-model-reasoning-picker__trigger')).toBeNull()
    expect(text(el)).not.toContain('default for future turns')
  })
})


describe('conversation history navigation', () => {
  const row = (id: string): TranscriptMessage => ({ id, role: 'user', content: id, createdAt: '' })
  const button = (el: HTMLElement, label: string) => [...el.querySelectorAll<HTMLButtonElement>('button')]
    .find(item => text(item).includes(label))!

  it('loads cursor pages in order, preserves the viewport, and returns to latest', async () => {
    const page = vi.fn()
      .mockResolvedValueOnce({ items: [row('recent')], nextCursor: 'older' })
      .mockResolvedValueOnce({ items: [row('historical message')], nextCursor: '' })
    const { el } = await mountChat(scripted([]), {
      listSessions: async () => [session('s1')], listMessagePage: page,
    })
    const log = el.querySelector<HTMLElement>('.agents-log')!
    Object.defineProperties(log, {
      clientHeight: { value: 200 },
      scrollHeight: { get: () => text(log).includes('historical message') ? 1200 : 800 },
    })
    log.scrollTop = 100
    button(el, 'Load earlier messages').click()
    await settle()
    expect(page).toHaveBeenLastCalledWith('scout', 's1', 'older')
    expect(log.scrollTop).toBe(500)
    expect(text(log).indexOf('historical message')).toBeLessThan(text(log).indexOf('recent'))
    expect(button(el, 'Load earlier messages')).toBeUndefined()
    const latest = el.querySelector<HTMLButtonElement>('button[aria-label="Return to latest"]')!
    expect(latest.title).toBe('Return to latest')
    expect(latest.querySelector('svg')).not.toBeNull()
    expect(text(latest)).toBe('')
    latest.click()
    await settle()
    expect(log.scrollTop).toBe(log.scrollHeight)
    expect(el.querySelector('button[aria-label="Return to latest"]')).toBeNull()
  })

  it.each([false, true])('preserves recovered approvals when a page changes the turn ID: %s', async splitRun => {
    const run = { id: 'r1', phase: 'PendingApproval', sessionID: 's1', agent: 'scout' }
    const assistant = (id: string, content: string): TranscriptMessage => ({
      id, role: 'assistant', content, runID: 'r1', createdAt: '',
      metadata: { turnPhase: 'commentary', turnStatus: 'running' },
    })
    const page = vi.fn()
      .mockResolvedValueOnce({ items: [assistant('a2', 'Latest progress')], nextCursor: 'older' })
      .mockResolvedValueOnce({ items: splitRun ? [assistant('a1', 'Earlier progress')] : [row('old')] })
    const { el } = await mountChat(scripted([]), {
      listSessions: async () => [session('s1')],
      listMessagePage: page,
      listRuns: async () => ({ items: [run] }),
      getRun: async () => ({ ...run, pending: { inboxID: 'i1', kind: 'approval', tool: 'deploy', args: '{}' } }),
    })
    expect(button(el, 'Approve')).toBeDefined()
    button(el, 'Load earlier messages').click()
    await settle()
    expect(button(el, 'Approve')).toBeDefined()
    expect(button(el, 'Deny')).toBeDefined()
    if (splitRun) expect(text(el)).toContain('Earlier progress')
    expect(el.querySelectorAll('.agents-msg.assistant')).toHaveLength(1)
  })

  it.each(['Succeeded', 'Aborted'])('retains older pages and reading position after a %s refresh', async phase => {
    const recent = { id: 'recent', runID: 'r1', role: 'assistant', content: 'Working' }
    const page = vi.fn()
      .mockResolvedValueOnce({ items: [recent], nextCursor: 'older' })
      .mockResolvedValueOnce({ items: [row('previous page')], nextCursor: 'oldest' })
      .mockResolvedValueOnce({ items: [{ ...recent, content: 'Final result' }], nextCursor: 'older' })
      .mockResolvedValueOnce({ items: [row('first page')] })
    const { el, store } = await mountChat(scripted([]), {
      listSessions: async () => [session('s1')], listMessagePage: page,
    })
    button(el, 'Load earlier messages').click()
    await settle()
    const log = el.querySelector<HTMLElement>('.agents-log')!
    Object.defineProperties(log, { clientHeight: { value: 200 }, scrollHeight: { value: 1200 } })
    log.scrollTop = 123
    log.dispatchEvent(new Event('scroll'))
    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'r1', phase } } }))
    await settle(8)
    expect(text(log)).toContain('previous page')
    expect(text(log)).toContain('Final result')
    expect(text(log)).not.toContain('Working')
    expect(log.scrollTop).toBe(123)
    button(el, 'Load earlier messages').click()
    await settle()
    expect(page).toHaveBeenLastCalledWith('scout', 's1', 'oldest')
    expect(text(log)).toContain('first page')
  })

  it('explains unavailable starters while retaining keyboard access and heading semantics', async () => {
    const { el } = await mountChat(scripted([]))
    const heading = el.querySelector('.agents-chat-welcome h2')!
    expect(heading.getAttribute('role')).toBeNull()
    const starter = button(el, 'What can you help me with')
    starter.click()
    await settle()
    expect(starter.disabled).toBe(false)
    expect(starter.getAttribute('aria-disabled')).toBe('true')
    expect(text(el)).toContain('Clear your draft to use a starter prompt.')
    const draft = composerText(el)
    button(el, 'Help me plan a task').click()
    await settle()
    expect(composerText(el)).toBe(draft)
  })

  it('retains new turns when loading history after sending', async () => {
    const page = vi.fn()
      .mockResolvedValueOnce({ items: [row('recent')], nextCursor: 'older' })
      .mockResolvedValueOnce({ items: [row('historical message')] })
    const { el } = await mountChat(scripted([{ event: 'done', data: { runID: 'r1', content: 'New answer' } }]), {
      listSessions: async () => [session('s1')], listMessagePage: page,
    })
    await send(el, 'New question')
    button(el, 'Load earlier messages').click()
    await settle()
    expect(text(el)).toContain('historical message')
    expect(text(el)).toContain('New question')
    expect(text(el)).toContain('New answer')
  })

  it('ignores older history after switching conversations', async () => {
    const older = deferred<{ items: TranscriptMessage[] }>()
    const page = vi.fn()
      .mockResolvedValueOnce({ items: [row('recent')], nextCursor: 'older' })
      .mockReturnValueOnce(older.promise)
      .mockResolvedValueOnce({ items: [row('other conversation')] })
    const { el } = await mountChat(scripted([]), {
      listSessions: async () => [session('s1'), session('s2')], listMessagePage: page,
    })
    button(el, 'Load earlier messages').click()
    await settle()
    await chooseSession(el, 's2')
    older.resolve({ items: [row('obsolete history')] })
    await settle()
    expect(text(el)).toContain('other conversation')
    expect(text(el)).not.toContain('obsolete history')
  })

  it('retains the transcript and cursor when an older page fails, then retries', async () => {
    const page = vi.fn()
      .mockResolvedValueOnce({ items: [row('recent')], nextCursor: 'older' })
      .mockRejectedValueOnce(new Error('Offline'))
      .mockResolvedValueOnce({ items: [row('earlier')] })
    const { el } = await mountChat(scripted([]), {
      listSessions: async () => [session('s1')], listMessagePage: page,
    })
    button(el, 'Load earlier messages').click()
    await settle()
    expect(text(el)).toContain('Could not load earlier messages.')
    expect(text(el)).toContain('recent')
    button(el, 'Load earlier messages').click()
    await settle()
    expect(page).toHaveBeenLastCalledWith('scout', 's1', 'older')
    expect(text(el)).toContain('earlier')
    expect(text(el)).not.toContain('Offline')
  })

  it('puts a starter in the composer without sending', async () => {
    const stream = vi.fn(scripted([]))
    const { el } = await mountChat(stream)
    button(el, 'What can you help me with').click()
    await settle()
    const composer = composerEditor(el)!
    expect(composer.textContent).toContain('which tools can you use')
    expect(document.activeElement).toBe(composer)
    expect(stream).not.toHaveBeenCalled()
  })
})

describe('dedicated chat model recovery', () => {
  it('replaces a missing selection with recovery and preserves an unsent draft', async () => {
    const { el, store } = await mountChat(scripted([]))
    setComposerText(el, 'Keep this conversation draft')
    store.credentials.data = []; store.dispatchEvent(new Event('change')); await settle(12)
    expect(text(el)).toContain('Connect a model to continue')
    expect(composerEditor(el)).toBeNull()
    expect(el.querySelector('.k-ai-model-reasoning-picker')).toBeNull()
    store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'restored' }]
    store.dispatchEvent(new Event('change')); await settle()
    expect(composerText(el)).toBe('Keep this conversation draft')
  })
  it('keeps a draft for setup navigation without assigning a replacement', async () => {
    const { el, store, view } = await mountChat(scripted([]))
    setComposerText(el, 'Retain through setup')
    store.credentials.data = []; store.dispatchEvent(new Event('change')); await settle(12)
    const connect = [...el.querySelectorAll<HTMLButtonElement>('button')].find(button => text(button) === 'Connect model')!
    connect.click(); await settle()
    expect(view.navigations.at(-1)).toEqual({ kind: 'create', resource: 'model', type: 'from-connection' })
    expect(store.takeChatSetupDraft('scout')?.draft).toBe('Retain through setup')
  })
})

describe('authoritative recovery state', () => {
  it('hides the loading notice during a background refresh when the selected model is usable', async () => {
    const { el, store } = await mountChat(scripted([]))
    store.credentials.loading = true
    store.dispatchEvent(new Event('change'))
    await settle()

    expect(text(el)).not.toContain('Loading models…')
    setComposerText(el, 'Ready to send')
    await settle()
    expect(el.querySelector<HTMLButtonElement>('.agents-composer button[type="submit"]')?.disabled).toBe(false)
  })

  it('keeps unknown credentials in loading and a failed first read in recovery', async () => {
    const { el, store } = await mountChat(scripted([]))
    store.credentials.hasSnapshot = false; store.credentials.loaded = false; store.credentials.loading = true
    store.dispatchEvent(new Event('change')); await settle()
    expect(text(el)).toContain('Loading models…')
    expect(composerEditor(el)).toBeNull()
    store.credentials.loading = false; store.credentials.error = 'offline'; store.credentials.loaded = true
    store.dispatchEvent(new Event('change')); await settle()
    expect(text(el)).toContain('Models could not be loaded')
    expect(composerEditor(el)).toBeNull()
  })
  it('blocks a known missing key without blocking discovery-only failures', async () => {
    const { el, store } = await mountChat(scripted([]))
    store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'manual-model', ready: false, secretResolved: true }]
    store.dispatchEvent(new Event('change')); await settle()
    expect(composerEditor(el)).not.toBeNull()
    store.credentials.data[0].secretResolved = false
    store.dispatchEvent(new Event('change')); await settle(12)
    expect(composerEditor(el)).toBeNull()
    expect(text(el)).toContain('Connect a model to continue')
  })
  it('does not replace the thread or send a queued assisted prompt after recovery', async () => {
    const stream = vi.fn(scripted([]))
    const { el, store } = await mountChat(stream)
    setComposerText(el, 'Retain my draft')
    store.credentials.data = []; store.dispatchEvent(new Event('change')); await settle(12)
    store.setPendingPrompt('scout', 'Queued assisted setup')
    store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'restored' }]
    store.dispatchEvent(new Event('change')); await settle(12)
    expect(stream).not.toHaveBeenCalled()
    expect(composerText(el)).toBe('Retain my draft')
    expect(store.takePendingPrompt('scout')).toBe('Queued assisted setup')
  })
})

it('keeps each stored sender beside its timestamp after rebuilding a shared transcript', async () => {
  const rows = rebuildTranscript([
    { id: 'alice', role: 'user', content: 'First', metadata: { actorID: 'railgrid:alice@example.com' }, createdAt: '2026-11-05T00:00:00Z' },
    { id: 'bob', role: 'user', content: 'Second', metadata: { actorID: 'railgrid:bob@example.com' }, createdAt: '2026-11-05T00:01:00Z' },
    { id: 'legacy', role: 'user', content: 'Older message' },
  ])
  expect(rows.map(row => formatAIMessageAuthor(row.actorID))).toEqual(['alice@example.com', 'bob@example.com', 'Unknown sender'])
  for (const message of rows) {
    const view = await mountVue(ChatMessageView, { message, showRunLink: false })
    mounted.push(view)
    await settle()
    const footer = view.element.querySelector('.k-ai-message__after')
    expect(footer?.textContent).toContain(formatAIMessageAuthor(message.actorID))
    expect(view.element.querySelector('article')?.getAttribute('aria-label')).toBe(`Message from ${formatAIMessageAuthor(message.actorID)}`)
  }
})

it('attributes optimistic messages for accounts without an email before the server responds', async () => {
  const user = { email: '', userId: 'static-user-47b9dce0e91570a1' }
  const message = { id: 'pending', role: 'user', content: 'Hello', tools: [], actorID: optimisticAIMessageActor(user) }
  const view = await mountVue(ChatMessageView, { message })
  mounted.push(view)
  expect(view.element.querySelector('.k-ai-message-author')?.textContent).toBe(user.userId)
  expect(view.element.textContent).not.toContain('Unknown sender')
  expect(optimisticAIMessageActor(null)).toBe('You')
  expect(formatAIMessageAuthor(undefined)).toBe('Unknown sender')
})

it('shows the Railgrid user immediately while a no-email account waits for a chat response', async () => {
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  const { el } = await mountChat(scripted([], gate), {
    context: () => ({ user: { email: '', userId: 'static-user-alice' } }),
  })
  try {
    await send(el, 'Please review this thread.')
    expect(el.querySelector('.k-ai-message-author')?.textContent).toBe('static-user-alice')
    expect(text(el)).not.toContain('Unknown sender')
  } finally {
    release()
    await settle()
  }
})

it('renders saved user IDs and new pending messages with the same account email', async () => {
  localStorage.setItem('railgrid:agents:session:org:ws:scout', 'sender-session')
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  const { el } = await mountChat(scripted([], gate), {
    context: () => ({ user: { email: 'alice@example.com', userId: 'user-alice' } }),
    listMessagePage: () => Promise.resolve({ items: [{ id: 'saved', role: 'user', content: 'Earlier message', metadata: { actorID: 'user-alice' } }] }),
  })
  try {
    expect(el.querySelector('.k-ai-message-author')?.textContent).toBe('alice@example.com')
    await send(el, 'New message')
    expect([...el.querySelectorAll('.k-ai-message-author')].map(node => node.textContent)).toEqual(['alice@example.com', 'alice@example.com'])
  } finally {
    release()
    await settle()
  }
})


it('navigates the overlay chat list, searches, and keeps drafts when starting another chat', async () => {
  const api = stubApi({ listSessions: async () => [session('saved', 'Saved conversation')], listRuns: async () => ({ items: [] }) })
  const store = makeStore(api)
  store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'gpt-test' }]
  store.credentials.loaded = store.credentials.hasSnapshot = true
  store.agents.data = [agentFixture('scout')]
  store.agents.loaded = true
  const host = document.createElement('div')
  document.body.appendChild(host)
  const views: Array<{ chats: boolean; title: string }> = []
  host.addEventListener('railgrid-overlay-view', event => views.push((event as CustomEvent).detail))
  const wrapper = defineComponent({ setup() {
    provideAgentsRuntime({ store: shallowRef(store), api: shallowRef(api), route: ref({ kind: 'agent', name: 'scout', tab: 'chat' }), authorityEpoch: ref(0), host: ref(host) })
    return () => h(AgentChat, { store, api, name: 'scout', overlay: true, compact: true })
  } })
  const view = await mountVue(wrapper, {})
  mounted.push(view)
  await settle(8)
  const input = composerEditor(view.element)!
  setComposerText(input, 'Keep this draft')
  host.dispatchEvent(new Event('railgrid-overlay-show-chats'))
  await settle()
  expect(views.at(-1)?.chats).toBe(true)
  expect(text(view.element.querySelector('.agents-overlay-chats'))).toContain('Saved conversation')
  const search = view.element.querySelector<HTMLInputElement>('input[type="search"]')!
  search.value = 'no match'
  search.dispatchEvent(new Event('input'))
  await settle()
  expect(text(view.element.querySelector('.agents-overlay-chats'))).toContain('No chats match')
  search.value = ''
  search.dispatchEvent(new Event('input'))
  host.dispatchEvent(new Event('railgrid-overlay-new-chat'))
  await settle()
  expect(views.at(-1)?.chats).toBe(false)
  expect(input.textContent).toBe('')
  host.dispatchEvent(new Event('railgrid-overlay-show-chats'))
  await settle()
  const saved = [...view.element.querySelectorAll<HTMLButtonElement>('.agents-overlay-chats__row')].find(row => text(row).includes('Saved conversation'))!
  saved.click()
  await settle(8)
  expect(input.textContent).toBe('Keep this draft')
  expect(views.at(-1)?.chats).toBe(false)
  host.remove()
})

async function mountOverlayDraftTest(extra: Record<string, unknown>) {
  const api = stubApi({ listRuns: async () => ({ items: [] }), ...extra })
  const store = makeStore(api)
  store.credentials.data = [{ name: 'openai', provider: 'openai', model: 'gpt-test' }]
  store.credentials.loaded = store.credentials.hasSnapshot = true
  store.agents.data = [agentFixture('scout')]
  store.agents.loaded = true
  const host = document.createElement('div')
  document.body.appendChild(host)
  const views: Array<{ chats: boolean; title: string }> = []
  host.addEventListener('railgrid-overlay-view', event => views.push((event as CustomEvent).detail))
  const wrapper = defineComponent({ setup() {
    provideAgentsRuntime({ store: shallowRef(store), api: shallowRef(api), route: ref({ kind: 'agent', name: 'scout', tab: 'chat' }), authorityEpoch: ref(0), host: ref(host) })
    return () => h(AgentChat, { store, api, name: 'scout', overlay: true, compact: true })
  } })
  const view = await mountVue(wrapper, {})
  mounted.push(view)
  await settle(8)
  const input = composerEditor(view.element)!
  return { view, host, input, api }
}

it('keeps unsent overlay chats accessible after starting another chat and refreshing history', async () => {
  const { view, host, input, api } = await mountOverlayDraftTest({ listSessions: async () => [] })
  try {
    setComposerText(input, 'Unsent original draft')
    host.dispatchEvent(new Event('railgrid-overlay-show-chats'))
    await settle()
    host.dispatchEvent(new Event('railgrid-overlay-new-chat'))
    await settle()
    setComposerText(input, 'Second unsent draft')
    host.dispatchEvent(new Event('railgrid-overlay-show-chats'))
    await settle()
    const original = [...view.element.querySelectorAll<HTMLButtonElement>('.agents-overlay-chats__row')].find(row => text(row).includes('Unsent original draft'))
    expect(original).toBeDefined()
    const reads = vi.spyOn(api, 'listMessagePage')
    original!.click()
    await settle()
    expect(input.textContent).toBe('Unsent original draft')
    expect(reads).not.toHaveBeenCalled()
    host.dispatchEvent(new Event('railgrid-overlay-show-chats'))
    await settle()
    const second = [...view.element.querySelectorAll<HTMLButtonElement>('.agents-overlay-chats__row')].find(row => text(row).includes('Second unsent draft'))
    expect(second).toBeDefined()
    second!.click()
    await settle()
    expect(input.textContent).toBe('Second unsent draft')
  } finally { host.remove() }
})

it('keeps text typed while the destination overlay chat history is loading', async () => {
  const history = deferred<{ items: TranscriptMessage[] }>()
  let delayHistory = false
  const { view, host, input } = await mountOverlayDraftTest({
    listSessions: async () => [session('draft-source'), session('draft-destination')],
    listMessagePage: async () => delayHistory ? history.promise : { items: [] },
  })
  try {
    setComposerText(input, 'Source draft')
    host.dispatchEvent(new Event('railgrid-overlay-show-chats'))
    await settle()
    delayHistory = true
    const destination = [...view.element.querySelectorAll<HTMLButtonElement>('.agents-overlay-chats__row')].find(row => text(row).includes('draft-destination'))!
    destination.click()
    await settle()
    expect(input.textContent).toBe('')
    setComposerText(input, 'Typed while loading')
    await settle()
    history.resolve({ items: [] })
    await settle(8)
    expect(input.textContent).toBe('Typed while loading')
    host.dispatchEvent(new Event('railgrid-overlay-show-chats'))
    await settle()
    const source = [...view.element.querySelectorAll<HTMLButtonElement>('.agents-overlay-chats__row')].find(row => text(row).includes('draft-source'))!
    source.click()
    await settle(8)
    expect(input.textContent).toBe('Source draft')
  } finally { history.resolve({ items: [] }); host.remove() }
})
