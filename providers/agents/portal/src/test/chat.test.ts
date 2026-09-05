// Chat streaming: the delta → tool card → approval card → done lifecycle, plus
// the Stop button's cancel call and scroll-pinning behaviour.

import { afterEach, describe, expect, it, vi } from 'vitest'
import AgentChat from '../views/AgentChat.vue'
import { resolveConfirm } from '../portalkit/confirm'
import { rebuildTranscript } from '../vue/chat'
import type { SSEEvent } from '../api'
import type { TranscriptMessage } from '../types'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text, type MountedVue } from './vue-helper'

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

async function mountChat(chatStream: unknown, extra: Record<string, unknown> = {}) {
  const api = stubApi({ chatStream, ...extra })
  const store = makeStore(api)
  store.agents.data = [agentFixture('scout')]
  store.agents.loaded = true
  const view = await mountVue(AgentChat, { store, api, name: 'scout' })
  mounted.push(view)
  await settle(6)
  return { el: view.element, api, store, view }
}

async function send(el: HTMLElement, message: string): Promise<void> {
  const ta = el.querySelector<HTMLTextAreaElement>('.agents-composer textarea')!
  ta.value = message
  ta.dispatchEvent(new Event('input'))
  await settle()
  el.querySelector<HTMLFormElement>('.agents-composer')!.dispatchEvent(new Event('submit', { cancelable: true }))
  await settle(6)
}

async function chooseSession(el: HTMLElement, label: string): Promise<void> {
  el.querySelector<HTMLButtonElement>('.agents-session-picker [role="combobox"]')!.click()
  await settle()
  const option = [...document.querySelectorAll<HTMLButtonElement>('.k-form-select__option')]
    .find(candidate => text(candidate) === label)
  expect(option).toBeDefined()
  option!.click()
  await settle()
}

describe('chat streaming', () => {
  it('uses the App Studio composer geometry and accessible compact action', async () => {
    const chatStream = vi.fn(scripted([]))
    const { el } = await mountChat(chatStream)
    const surface = el.querySelector('.agents-composer-surface')
    const textarea = el.querySelector<HTMLTextAreaElement>('.agents-composer-input')!
    const submit = el.querySelector<HTMLButtonElement>('button[type="submit"]')!

    expect(surface).not.toBeNull()
    expect(textarea.rows).toBe(3)
    expect(textarea.getAttribute('aria-label')).toBe('Message scout')
    expect(submit.classList.contains('agents-composer-primary')).toBe(true)
    expect(submit.getAttribute('aria-label')).toBe('Send')
    expect(submit.title).toBe('Send')
    expect(submit.textContent?.trim()).toBe('')
    expect(submit.disabled).toBe(true)

    textarea.value = 'hello'
    textarea.dispatchEvent(new Event('input'))
    await settle()
    expect(el.querySelector<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(false)

    const composing = new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, bubbles: true, cancelable: true })
    textarea.dispatchEvent(composing)
    await settle()
    expect(composing.defaultPrevented).toBe(false)
    expect(chatStream).not.toHaveBeenCalled()
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
    // Markdown is rendered, not escaped.
    expect(msgs[1].querySelector('strong')?.textContent).toBe('hi')
    // Per-turn usage footer.
    expect(text(msgs[1].querySelector('.agents-turn-usage'))).toContain('$0.0015')
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
    localStorage.setItem('faros:agents:session:org:ws:scout', 'seed-session')
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
          { event: 'tool_start', data: { id: 't1', name: 'github__list_issues', args: '{"repo":"faros"}' } },
        ],
        gate,
      ),
    )
    const done = send(el, 'find issues')
    await settle(6)

    const card = el.querySelector('.agents-toolcard')!
    expect(card.className).toContain('is-pending')
    expect(text(card)).toContain('github__list_issues')
    expect(text(card)).toContain('running…')

    release()
    await done

    // Expanding reveals the recorded args.
    el.querySelector<HTMLButtonElement>('.agents-toolcard-head')!.click()
    await settle()
    expect(text(el.querySelector('.agents-toolcard-body'))).toContain('"repo"')
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
    const card = el.querySelector('.agents-toolcard')!
    expect(card.className).toContain('is-err')
    expect(text(card)).toContain('250ms')
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
    expect(text(card)).toContain('edges__pods_delete')

    card.querySelector<HTMLButtonElement>('.agents-approval-actions button')!.click()
    await settle(6)
    expect(resolveInbox).toHaveBeenCalledWith('i1', 'approve')
    expect(text(el.querySelector('.agents-approval-done'))).toContain('resuming')
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

    const [approve, deny] = [...el.querySelectorAll<HTMLButtonElement>('.agents-approval-actions button')]
    approve.click()
    await settle(2)

    expect(approve.disabled).toBe(true)
    expect(deny.disabled).toBe(true)
    expect(approve.getAttribute('aria-busy')).toBe('true')
    expect(deny.getAttribute('aria-busy')).toBe('true')
    expect(text(approve)).toContain('Approving')
    deny.click()
    expect(resolveInbox).toHaveBeenCalledTimes(1)
    expect(resolveInbox).toHaveBeenCalledWith('i1', 'approve')

    resolution.resolve({ id: 'i1', state: 'approved' })
    await settle(4)
    expect(text(el.querySelector('.agents-approval-done'))).toContain('resuming')
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

    const [approve, deny] = [...el.querySelectorAll<HTMLButtonElement>('.agents-approval-actions button')]
    expect(approve.disabled).toBe(true)
    expect(deny.disabled).toBe(false)
    expect(text(el.querySelector('.agents-approval-disclosure-error'))).toContain('Approval details are unavailable or malformed')
    deny.click()
    await settle(4)
    expect(resolveInbox).toHaveBeenCalledWith('i1', 'deny')
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
    el.querySelector<HTMLButtonElement>('.agents-approval-actions button')!.click()
    await settle(2)
    el.querySelector<HTMLButtonElement>('button[aria-label="New chat"]')!.click()
    await settle(2)

    resolution.reject(new Error('old approval failure'))
    await settle(4)

    expect(text(document.querySelector('.k-toast--error'))).not.toContain('old approval failure')
  })

  it('stop aborts the stream and cancels the run server-side', async () => {
    const cancelRun = vi.fn().mockResolvedValue({ id: 'r1', cancelling: true })
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const { el } = await mountChat(scripted([{ event: 'start', data: { runID: 'r1', sessionID: 's1' } }], gate), { cancelRun })

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
    const listMessages = vi.fn().mockResolvedValue([])
    const cancelRun = vi.fn().mockResolvedValue({ id: 'r1', cancelling: true })
    const chatStream = vi.fn(async function* (_agent: string, _message: string, _session: string, signal: AbortSignal) {
      yield { event: 'start', data: { runID: 'r1', sessionID: 's1' } } as SSEEvent
      await new Promise<void>((resolve) => signal.addEventListener('abort', () => resolve(), { once: true }))
    })
    const { el, store } = await mountChat(chatStream, { cancelRun, listMessages })
    const readsBeforeSend = listMessages.mock.calls.length

    await send(el, 'long task')
    el.querySelector<HTMLButtonElement>('.agents-stop')!.click()
    await settle(6)
    expect(cancelRun).toHaveBeenCalledWith('r1')

    store.dispatchEvent(new CustomEvent('server', {
      detail: { type: 'run', data: { id: 'r1', phase: 'Aborted' } },
    }))
    await settle(6)

    expect(listMessages.mock.calls.length).toBeGreaterThan(readsBeforeSend)
    expect(listMessages).toHaveBeenLastCalledWith('scout', 's1')
  })

  it('defers a terminal transcript refresh until an in-flight cancellation settles', async () => {
    const cancellation = deferred<{ id: string; cancelling: boolean }>()
    const listMessages = vi.fn()
      .mockResolvedValueOnce([])
      .mockResolvedValue([{ id: 'final', role: 'assistant', content: 'Authoritative final reply' }])
    const cancelRun = vi.fn(() => cancellation.promise)
    const chatStream = vi.fn(async function* (_agent: string, _message: string, _session: string, signal: AbortSignal) {
      yield { event: 'start', data: { runID: 'r1', sessionID: 's1' } } as SSEEvent
      await new Promise<void>((resolve) => signal.addEventListener('abort', () => resolve(), { once: true }))
    })
    const { el, store } = await mountChat(chatStream, { cancelRun, listMessages })

    await send(el, 'long task')
    el.querySelector<HTMLButtonElement>('.agents-stop')!.click()
    await settle(2)
    expect(cancelRun).toHaveBeenCalledWith('r1')
    const readsBeforeTerminal = listMessages.mock.calls.length

    store.dispatchEvent(new CustomEvent('server', {
      detail: { type: 'run', data: { id: 'r1', phase: 'Aborted' } },
    }))
    await settle(3)
    expect(listMessages).toHaveBeenCalledTimes(readsBeforeTerminal)

    cancellation.resolve({ id: 'r1', cancelling: true })
    await settle(8)
    expect(listMessages.mock.calls.length).toBeGreaterThan(readsBeforeTerminal)
    expect(listMessages).toHaveBeenLastCalledWith('scout', 's1')
    expect(text(el)).toContain('Authoritative final reply')
  })

  it('waits for a delayed start frame before cancelling an immediate stop', async () => {
    const start = deferred<void>()
    const cancelRun = vi.fn().mockResolvedValue({ id: 'r-delayed', cancelling: true })
    const chatStream = vi.fn(async function* () {
      await start.promise
      yield { event: 'start', data: { runID: 'r-delayed', sessionID: 's-delayed' } } as SSEEvent
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
      await new Promise<void>((resolve) => signal.addEventListener('abort', () => resolve(), { once: true }))
    })
    const { el } = await mountChat(chatStream, { cancelRun })

    await send(el, 'long task')
    el.querySelector<HTMLButtonElement>('.agents-stop')!.click()
    await settle(2)
    cancellation.reject(new Error('provider unavailable'))
    await settle(6)

    const banner = el.querySelector('.agents-orphan-banner')
    expect(banner).not.toBeNull()
    expect(text(banner)).toContain('still working')
    expect(text(document.querySelector('.k-toast--error'))).toContain('provider unavailable')
    const retry = [...banner!.querySelectorAll<HTMLButtonElement>('button')]
      .find(button => text(button).includes('Stop it'))!
    retry.click()
    await settle(4)
    expect(cancelRun).toHaveBeenCalledTimes(2)
    expect(el.querySelector('.agents-orphan-banner')).toBeNull()
  })

  it('does not show a failed stream cancellation after the user leaves its session', async () => {
    const cancellation = deferred<{ id: string; cancelling: boolean }>()
    let release!: () => void
    const gate = new Promise<void>((resolve) => { release = resolve })
    const { el } = await mountChat(
      scripted([{ event: 'start', data: { runID: 'r1', sessionID: 's1' } }], gate),
      { cancelRun: () => cancellation.promise },
    )

    const done = send(el, 'long task')
    await settle(6)
    el.querySelector<HTMLButtonElement>('.agents-stop')!.click()
    await settle(2)
    release()
    await done
    await settle(6)
    expect(el.querySelector<HTMLButtonElement>('button[aria-label="New chat"]')!.disabled).toBe(false)
    el.querySelector<HTMLButtonElement>('button[aria-label="New chat"]')!.click()
    await settle(2)

    cancellation.reject(new Error('old cancellation failure'))
    await settle(4)

    expect(text(document.querySelector('.k-toast--error'))).not.toContain('old cancellation failure')
  })

  it('does not show a failed deletion after the user leaves its session', async () => {
    const deletion = deferred<void>()
    localStorage.setItem('faros:agents:session:org:ws:scout', 's1')
    const { el } = await mountChat(scripted([]), {
      listSessions: () => Promise.resolve([session('s1')]),
      deleteSession: () => deletion.promise,
    })

    el.querySelector<HTMLButtonElement>('button[aria-label="Delete this chat"]')!.click()
    await settle(2)
    resolveConfirm(true)
    await settle(2)
    el.querySelector<HTMLButtonElement>('button[aria-label="New chat"]')!.click()
    await settle(2)

    deletion.reject(new Error('old deletion failure'))
    await settle(4)

    expect(text(document.querySelector('.k-toast--error'))).not.toContain('old deletion failure')
  })

  it('keeps chat deletion single-flight through confirmation and deletion', async () => {
    const deletion = deferred<void>()
    const deleteSession = vi.fn(() => deletion.promise)
    localStorage.setItem('faros:agents:session:org:ws:scout', 's1')
    const { el } = await mountChat(scripted([]), {
      listSessions: () => Promise.resolve([session('s1')]),
      deleteSession,
    })

    const button = el.querySelector<HTMLButtonElement>('button[aria-label="Delete this chat"]')!
    button.click()
    button.click()
    await settle(2)
    expect(button.disabled).toBe(true)
    expect(button.getAttribute('aria-busy')).toBe('true')
    resolveConfirm(true)
    await settle(3)
    expect(deleteSession).toHaveBeenCalledTimes(1)
    button.click()
    expect(deleteSession).toHaveBeenCalledTimes(1)

    deletion.resolve()
    await settle(6)
    expect(el.querySelector<HTMLButtonElement>('button[aria-label="Delete this chat"]')?.disabled).toBe(false)
  })

  it('does not force-scroll when the user has scrolled up', async () => {
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
    Object.defineProperty(log, 'scrollHeight', { value: 1000, configurable: true })
    Object.defineProperty(log, 'clientHeight', { value: 200, configurable: true })
    log.scrollTop = 100
    log.dispatchEvent(new Event('scroll'))
    await settle(2)
    expect(log.scrollTop).toBe(100)

    release()
    await done
  })
})

describe('chat read ownership', () => {
  it('does not let initial session discovery replace a new chat the user opened', async () => {
    const sessions = deferred<ReturnType<typeof session>[]>()
    localStorage.setItem('faros:agents:session:org:ws:scout', 'remembered-session')
    const api = stubApi({ listSessions: () => sessions.promise })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)

    view.element.querySelector<HTMLButtonElement>('button[aria-label="New chat"]')!.click()
    await settle(2)
    const userSession = localStorage.getItem('faros:agents:session:org:ws:scout')
    expect(userSession).toBeTruthy()
    expect(userSession).not.toBe('remembered-session')

    sessions.resolve([session('remembered-session', 'Remembered chat')])
    await settle(6)

    expect(localStorage.getItem('faros:agents:session:org:ws:scout')).toBe(userSession)
    expect(text(view.element.querySelector('.agents-session-picker'))).toContain('New chat')
  })

  it('does not let initial session discovery replace a session that has started sending', async () => {
    const sessions = deferred<ReturnType<typeof session>[]>()
    const chatStream = vi.fn(async function* (_agent: string, _message: string, _sessionID: string) {
      yield { event: 'done', data: { runID: 'r1', content: 'current reply' } }
    })
    localStorage.setItem('faros:agents:session:org:ws:scout', 'remembered-session')
    const api = stubApi({ listSessions: () => sessions.promise, chatStream })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)

    await send(view.element, 'start now')
    const activeSession = chatStream.mock.calls[0][2]
    expect(activeSession).toBeTruthy()
    expect(activeSession).not.toBe('remembered-session')

    sessions.resolve([session('remembered-session', 'Remembered chat')])
    await settle(6)

    expect(localStorage.getItem('faros:agents:session:org:ws:scout')).toBe(activeSession)
    expect(text(view.element)).toContain('current reply')
  })

  it('does not let a late message read overwrite a newer session', async () => {
    const first = deferred<Array<{ id: string; role: string; content: string }>>()
    localStorage.setItem('faros:agents:session:org:ws:scout', 's1')
    const listMessages = vi.fn((_agent: string, id: string) => id === 's1'
      ? first.promise
      : Promise.resolve([{ id: 'new', role: 'user', content: 'new session' }]))
    const api = stubApi({ listSessions: () => Promise.resolve([session('s1'), session('s2')]), listMessages })
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
    localStorage.setItem('faros:agents:session:org:ws:scout', 's1')
    const chatStream = vi.fn(async function* () {
      yield { event: 'done', data: { runID: 'r1', content: 'fresh reply' } }
    })
    const api = stubApi({
      listSessions: () => Promise.resolve([session('s1')]),
      listMessages: () => history.promise,
      chatStream,
    })
    const store = makeStore(api)
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
    const listMessages = vi.fn((_name: string, id: string) => Promise.resolve([
      { id: `${id}-message`, role: 'user', content: id },
    ]))
    const api = stubApi({ listSessions, listMessages })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout'), agentFixture('ranger')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)

    await view.setProps({ name: 'ranger' })
    await settle(4)
    scoutSessions.resolve([session('scout-session', 'Scout chat')])
    await settle(4)

    expect(text(view.element.querySelector('.agents-session-picker'))).toContain('Ranger chat')
    expect(text(view.element.querySelector('.agents-session-picker'))).not.toContain('Scout chat')
    expect(text(view.element)).toContain('ranger-session')
  })

  it('does not surface an orphan result from a session the user left', async () => {
    const firstRuns = deferred<{ items: ReturnType<typeof runRowForRead>[] }>()
    localStorage.setItem('faros:agents:session:org:ws:scout', 's1')
    const listRuns = vi.fn((filter: { session?: string }) => filter.session === 's1'
      ? firstRuns.promise
      : Promise.resolve({ items: [] }))
    const api = stubApi({
      listSessions: () => Promise.resolve([session('s1'), session('s2')]),
      listMessages: () => Promise.resolve([]),
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

    expect(view.element.querySelector('.agents-orphan-banner')).toBeNull()
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
    const listMessages = vi.fn().mockResolvedValue([
      { id: '3', role: 'assistant', content: 'done', runID: 'r1' },
      { id: '2', role: 'tool', content: 'ok', runID: 'r1', metadata: { tool: 'web_search', args: '{"q":"faros"}', durationMS: 90 } },
      { id: '1', role: 'user', content: 'search', runID: 'r1' },
    ])
    const api = stubApi({ listMessages })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    const el = view.element
    await settle(6)

    const card = el.querySelector('.agents-toolcard')!
    expect(text(card)).toContain('web_search')
    expect(text(card)).toContain('90ms')
    // A generous limit is requested so a long session comes back whole.
    expect(listMessages.mock.calls[0][2] ?? 200).toBeGreaterThanOrEqual(200)
  })
})

// A run outlives the stream that started it, so reopening a chat can find work
// still in flight. Saying so is what stops the reply looking lost — and stops the
// user re-asking and paying for the same research twice.
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
    const api = stubApi({ listMessages: () => Promise.resolve([]), listRuns, ...extra })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    await settle(4)
    return { el: view.element, api, listRuns, store }
  }

  it('announces a Running run and offers the run view', async () => {
    const { el } = await mountWith('Running')
    const banner = el.querySelector('.agents-orphan-banner')
    expect(banner).toBeTruthy()
    expect(text(banner!)).toContain('still working')
    expect(text(banner!)).toContain('View progress')
  })

  it('counts a run waiting on approval as still working', async () => {
    const { el } = await mountWith('PendingApproval')
    expect(el.querySelector('.agents-orphan-banner')).toBeTruthy()
  })

  it('says nothing when the session has no live run', async () => {
    const { el } = await mountWith('Succeeded')
    expect(el.querySelector('.agents-orphan-banner')).toBeNull()
  })

  it('can stop the run, which clears the banner', async () => {
    const cancelRun = vi.fn().mockResolvedValue({ id: 'r-live', cancelling: true })
    const { el } = await mountWith('Running', { cancelRun })
    const stop = [...el.querySelectorAll('.agents-orphan-banner button')].find((b) => b.textContent?.includes('Stop it')) as HTMLButtonElement
    stop.click()
    await settle(4)
    expect(cancelRun).toHaveBeenCalledWith('r-live')
    expect(el.querySelector('.agents-orphan-banner')).toBeNull()
  })

  it('keeps orphan cancellation single-flight while the request is pending', async () => {
    const cancellation = deferred<{ id: string; cancelling: boolean }>()
    const cancelRun = vi.fn(() => cancellation.promise)
    const { el } = await mountWith('Running', { cancelRun })
    const stop = [...el.querySelectorAll<HTMLButtonElement>('.agents-orphan-banner button')]
      .find(button => text(button).includes('Stop it'))!

    stop.click()
    await settle(2)
    expect(stop.disabled).toBe(true)
    expect(stop.getAttribute('aria-busy')).toBe('true')
    expect(text(stop)).toContain('Stopping')
    stop.click()
    expect(cancelRun).toHaveBeenCalledTimes(1)

    cancellation.resolve({ id: 'r-live', cancelling: true })
    await settle(4)
    expect(el.querySelector('.agents-orphan-banner')).toBeNull()
  })

  it('does not show a failed stop from a run after the user leaves its session', async () => {
    const cancellation = deferred<{ id: string; cancelling: boolean }>()
    const { el } = await mountWith('Running', { cancelRun: () => cancellation.promise })
    const stop = [...el.querySelectorAll('.agents-orphan-banner button')].find((b) => b.textContent?.includes('Stop it')) as HTMLButtonElement
    stop.click()
    await settle(2)
    el.querySelector<HTMLButtonElement>('button[aria-label="New chat"]')!.click()
    await settle(2)

    cancellation.reject(new Error('old run failure'))
    await settle(4)

    expect(text(document.querySelector('.k-toast--error'))).not.toContain('old run failure')
  })

  it('clears the banner and reloads the transcript when the run finishes', async () => {
    const listMessages = vi.fn().mockResolvedValue([])
    // Running while the banner is up, terminal once it has finished — the
    // re-check after the reload has to agree, or the banner would reappear.
    let phase = 'Running'
    const listRuns = vi.fn().mockImplementation(() => Promise.resolve({ items: [runRow({ phase })] }))
    const api = stubApi({ listMessages, listRuns })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    const el = view.element
    await settle(4)
    expect(el.querySelector('.agents-orphan-banner')).toBeTruthy()
    const before = listMessages.mock.calls.length
    phase = 'Succeeded'

    store.dispatchEvent(
      new CustomEvent('server', { detail: { type: 'run', data: { id: 'r-live', phase: 'Succeeded' } } }),
    )
    await settle(4)

    expect(el.querySelector('.agents-orphan-banner')).toBeNull()
    expect(listMessages.mock.calls.length).toBeGreaterThan(before)
  })

  it('a lookup failure never breaks the transcript', async () => {
    const listRuns = vi.fn().mockRejectedValue(new Error('unavailable'))
    const api = stubApi({ listMessages: () => Promise.resolve([{ id: '1', role: 'user', content: 'hi' }]), listRuns })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    const el = view.element
    await settle(4)
    expect(el.querySelector('.agents-orphan-banner')).toBeNull()
    expect(text(el)).toContain('hi')
  })
})
