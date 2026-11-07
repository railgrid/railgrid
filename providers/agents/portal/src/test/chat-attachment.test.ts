import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, ref, shallowRef } from 'vue'
import { provideAgentsRuntime } from '../vue/runtime'
import type { Route } from '../router'
import AgentChat from '../views/AgentChat.vue'
import { ApiClient, ApiError, type SSEEvent } from '../api'
import { ResourceError } from '../resources'
import { rebuildTranscript, transcriptWithProgress } from '../vue/chat'
import type { RunDetail, TranscriptMessage } from '../types'
import { agentFixture, makeStore, setComposerText, stubApi } from './helpers'
import { mountVue, settleVue, text, type MountedVue } from './vue-helper'

const mounted: MountedVue[] = []
afterEach(() => {
  while (mounted.length) mounted.pop()?.unmount()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  localStorage.clear()
})

const run = (phase: RunDetail['phase'] = 'Running'): RunDetail => ({
  id: 'run', agent: 'coder', sessionID: 'session', phase, trigger: 'chat', class: 'interactive',
  createdAt: '2026-11-06T10:00:00Z', inputTokens: 0, outputTokens: 0, usdMicros: 0, steps: [],
})
const user: TranscriptMessage = { id: 'user', role: 'user', content: 'Fix it', runID: 'run', createdAt: '2026-11-06T10:00:00Z' }
const partial: TranscriptMessage = {
  id: 'partial', role: 'assistant', content: 'Inspecting the code after reconnect', runID: 'run',
  createdAt: '2026-11-06T10:00:01Z', metadata: { turnPhase: 'streaming', turnStatus: 'running' },
}
const command: TranscriptMessage = {
  id: 'command', role: 'tool', content: '', runID: 'run', createdAt: '2026-11-06T10:00:02Z',
  metadata: { tool: 'shell', toolCallID: 'call', args: '{"command":"go test"}', pending: true },
}

async function mountChat(overrides: Record<string, unknown> = {}) {
  const api = stubApi({ listRuns: async () => ({ items: overrides.chatStream ? [] : [run()] }), ...overrides })
  const store = makeStore(api)
  const agent = agentFixture('coder')
  agent.spec.backend = { type: 'harness' }
  store.agents.data = [agent]
  store.agents.loaded = store.agents.hasSnapshot = true
  const view = await mountVue(AgentChat, { store, api, name: 'coder' })
  mounted.push(view)
  await settleVue(24)
  return { view, api, store, el: view.element }
}

async function send(el: HTMLElement) {
  setComposerText(el, 'Fix it')
  await settleVue(6)
  el.querySelector<HTMLButtonElement>('.agents-composer button[type="submit"]')!.click()
  await settleVue(30)
}

describe('chat attaches to active runs', () => {
  it.each([false, true])('retries failed discovery after lost admission (terminal=%s)', async terminal => {
    vi.useFakeTimers()
    let admitted: RunDetail | undefined
    let failed = false
    const chatStream = vi.fn(async function* (_agent: string, _text: string, session: string) {
      admitted = { ...run(terminal ? 'Succeeded' : 'Running'), sessionID: session, progress: [partial] }
      throw new Error('Lost acknowledgement')
    })
    const { el } = await mountChat({
      chatStream,
      listRuns: async () => {
        if (admitted && !failed) { failed = true; throw new ApiError(503, 'Unavailable') }
        return { items: admitted ? [admitted] : [] }
      },
      getRun: async () => admitted,
      listMessagePage: async () => ({ items: admitted ? terminal ? [{ ...partial, content: 'Recovered final', metadata: { turnPhase: 'final' } }, user] : [user] : [] }),
    })
    await send(el)
    await vi.advanceTimersByTimeAsync(6000)
    expect(text(el)).toContain(terminal ? 'Recovered final' : partial.content)
    expect(chatStream).toHaveBeenCalledTimes(1)
  })

  it.each([false, true])('recovers an acknowledged half-open stream (terminal event=%s)', async terminalEvent => {
    vi.useFakeTimers()
    let admitted = false
    let finished = false
    let observerAborted = false
    const chatStream = vi.fn(async function* (_agent: string, _text: string, session: string, signal: AbortSignal) {
      admitted = true
      yield { event: 'start', data: { runID: 'run', sessionID: session } }
      yield { event: 'run_started', data: { runID: 'run', status: 'Running' } }
      yield { event: 'delta', data: { text: 'First chunk' } }
      await new Promise<void>(resolve => signal.addEventListener('abort', () => { observerAborted = true; resolve() }, { once: true }))
      yield { event: 'delta', data: { text: 'STALE FRAME' } }
    })
    const { el, store } = await mountChat({
      chatStream, listRuns: async () => ({ items: [] }),
      listMessagePage: async () => ({ items: finished ? [{ ...partial, content: 'Canonical final', metadata: { turnPhase: 'final', turnStatus: 'completed' } }, user] : admitted ? [user] : [] }),
      getRun: async () => ({ ...run(finished ? 'Succeeded' : 'Running'), progress: [partial] }),
    })
    await send(el)
    if (terminalEvent) {
      finished = true
      store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'run', phase: 'Succeeded' } } }))
      await settleVue(30)
    } else {
      await vi.advanceTimersByTimeAsync(17000)
      expect(text(el)).toContain(partial.content)
      expect(el.querySelector('.agents-composer .agents-stop')).not.toBeNull()
      finished = true
      await vi.advanceTimersByTimeAsync(2000)
    }
    expect(observerAborted).toBe(true)
    expect(text(el)).toContain('Canonical final')
    expect(text(el)).not.toContain('STALE FRAME')
    expect(chatStream).toHaveBeenCalledTimes(1)
  })

  it('does not redisclose a stale approval while waiting for cancellation acknowledgement', async () => {
    vi.useFakeTimers()
    let detail: RunDetail = { ...run('PendingApproval'), pending: { inboxID: 'old-approval', tool: 'shell', args: '{}' } }
    const { el } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listRuns: async () => ({ items: [detail] }),
      listMessagePage: async () => ({ items: [partial, user] }),
      getRun: async () => detail,
      cancelRun: async () => ({ id: 'run', cancelling: true }),
    })
    expect(el.querySelector('.agents-approval')).not.toBeNull()
    el.querySelector<HTMLButtonElement>('.agents-composer .agents-stop')!.click()
    await settleVue(24)
    await vi.advanceTimersByTimeAsync(2000)
    expect(el.querySelector('.agents-approval')).toBeNull()
    expect(text(el.querySelector('.agents-log'))).toContain('Stopping')
    detail = run('Aborted')
    await vi.advanceTimersByTimeAsync(1000)
    expect(text(el.querySelector('.agents-log'))).not.toContain('Stopping')
  })

  it('keeps a completed run terminal when its cancellation response arrives later', async () => {
    vi.useFakeTimers()
    let release!: () => void
    const cancelRun = vi.fn(() => new Promise<void>(resolve => { release = resolve }))
    let detail: RunDetail = run()
    let history = [user, partial]
    const { el } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listRuns: async () => ({ items: detail.phase === 'Succeeded' ? [] : [detail] }),
      listMessagePage: async () => ({ items: [...history].reverse() }),
      getRun: async () => detail, cancelRun,
    })
    el.querySelector<HTMLButtonElement>('.agents-composer .agents-stop')!.click()
    await settleVue()
    detail = run('Succeeded')
    history = [user, { ...partial, content: 'Finished before cancellation', metadata: { turnPhase: 'final', turnStatus: 'completed' } }]
    await vi.advanceTimersByTimeAsync(1000)
    await settleVue(24)
    release()
    await settleVue(24)
    expect(text(el.querySelector('.agents-log'))).toContain('Finished before cancellation')
    expect(text(el.querySelector('.agents-log'))).not.toContain('Stopping')
    expect(el.querySelector('.agents-composer .agents-stop')).toBeNull()
  })

  it.each([false, true])('observes cancellation to completion without lifecycle events (streaming=%s)', async attached => {
    vi.useFakeTimers()
    let detail: RunDetail = run()
    let history: TranscriptMessage[] = attached ? [] : [user, partial]
    const cancelRun = vi.fn(async () => ({ id: 'run', cancelling: true }))
    const getRun = vi.fn(async () => detail)
    const { el } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listRuns: async () => ({ items: attached ? [] : [detail] }),
      listMessagePage: async () => ({ items: [...history].reverse() }), getRun, cancelRun,
      chatStream: async function* (_agent: string, _text: string, _session: string, signal: AbortSignal) {
        yield { event: 'start', data: { runID: 'run', sessionID: 'session' } } as SSEEvent
        yield { event: 'run_started', data: { runID: 'run', sessionID: 'session' } } as SSEEvent
        yield { event: 'delta', data: { text: 'Work before stopping' } } as SSEEvent
        await new Promise<void>(resolve => signal.addEventListener('abort', () => resolve(), { once: true }))
      },
    })
    if (attached) await send(el)
    el.querySelector<HTMLButtonElement>('.agents-composer .agents-stop')!.click()
    await settleVue(24)
    expect(cancelRun).toHaveBeenCalledTimes(1)
    // The cancellation was accepted, but the runner has not acknowledged it.
    await vi.advanceTimersByTimeAsync(1000)
    expect(text(el.querySelector('.agents-log'))).toContain('Stopping')
    expect(el.querySelector<HTMLButtonElement>('.agents-composer .agents-stop')?.disabled).toBe(true)
    detail = run('Aborted')
    history = [user, { ...partial, content: 'Cancelled work', metadata: { turnPhase: 'terminal', turnStatus: 'aborted' } }]
    await vi.advanceTimersByTimeAsync(2000)
    await settleVue(24)
    expect(text(el.querySelector('.agents-log'))).not.toContain('Stopping')
    expect(text(el.querySelector('.agents-log'))).toContain('Cancelled work')
    expect(el.querySelector('.agents-run-state--aborted')).not.toBeNull()
    const reads = getRun.mock.calls.length
    await vi.advanceTimersByTimeAsync(3000)
    expect(getRun).toHaveBeenCalledTimes(reads)
  })

  it('keeps following a retained chat across run inspection and browser back', async () => {
    const getRun = vi.fn(async () => ({ ...run(), progress: [partial] }))
    const api = stubApi({
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listRuns: async () => ({ items: [run()] }),
      listMessagePage: async () => ({ items: [user] }),
      getRun,
    })
    const store = makeStore(api)
    const agent = agentFixture('coder')
    agent.spec.backend = { type: 'harness' }
    store.agents.data = [agent]
    store.agents.loaded = store.agents.hasSnapshot = true
    const route = ref<Route>({ kind: 'agent', name: 'coder', tab: 'chat' })
    const host = document.createElement('div')
    document.body.appendChild(host)
    const wrapper = defineComponent({ setup() {
      provideAgentsRuntime({ store: shallowRef(store), api: shallowRef(api), route, authorityEpoch: ref(0), host: ref(host) })
      return () => h(AgentChat, { store, api, name: 'coder' })
    } })
    const view = await mountVue(wrapper, {})
    mounted.push(view)
    await settleVue(24)
    vi.useFakeTimers()
    await vi.advanceTimersByTimeAsync(1000)
    const before = getRun.mock.calls.length
    route.value = { kind: 'agent', name: 'coder', tab: 'runs', runID: 'run' }
    await settleVue()
    await vi.advanceTimersByTimeAsync(2000)
    expect(getRun.mock.calls.length).toBeGreaterThan(before)
    route.value = { kind: 'agent', name: 'coder', tab: 'chat' }
    await settleVue()
    const returning = getRun.mock.calls.length
    getRun.mockImplementation(async () => ({ ...run(), progress: [{ ...partial, content: 'Still working after browser back' }] }))
    await vi.advanceTimersByTimeAsync(2000)
    expect(getRun.mock.calls.length).toBeGreaterThan(returning)
    expect(text(view.element.querySelector('.agents-log'))).toContain('Still working after browser back')
    host.remove()
  })

  it.each([false, true])('restores harness text and command progress after stream loss (error=%s)', async broken => {
    let detail = { ...run(), progress: [partial, command] }
    let admitted = false
    let history: TranscriptMessage[] = []
    const getRun = vi.fn(async () => detail)
    const { el } = await mountChat({
      getRun,
      listMessagePage: async () => ({ items: [...history].reverse() }),
      listRuns: async () => ({ items: admitted && detail.phase !== 'Succeeded' ? [detail] : [] }),
      chatStream: async function* () {
        admitted = true
        yield { event: 'start', data: { runID: 'run', sessionID: 'session' } }
        yield { event: 'run_started', data: { runID: 'run', sessionID: 'session' } }
        yield { event: 'delta', data: { text: 'Before disconnect' } }
        if (broken) throw new Error('connection lost')
      },
    })
    vi.useFakeTimers()
    await send(el)
    history = [user]
    await vi.advanceTimersByTimeAsync(1000)
    expect(text(el.querySelector('.agents-log'))).toContain(partial.content)
    expect(text(el.querySelector('.agents-log'))).not.toContain('Chat failed:')
    expect(el.querySelectorAll('.k-ai-action-row')).toHaveLength(1)
    expect(text(el.querySelector('.k-ai-action-row'))).toContain('Shell')

    const completed = { ...command, content: 'PASS', metadata: { ...command.metadata, pending: false } }
    detail = { ...detail, progress: [partial, completed] }
    history = [user, completed]
    await vi.advanceTimersByTimeAsync(2000)
    expect(el.querySelectorAll('.k-ai-action-row')).toHaveLength(1)
    expect(text(el.querySelector('.agents-log'))).toContain(partial.content)

    detail = { ...run('Succeeded'), progress: [] }
    history = [user, completed, { ...partial, id: 'final', content: 'Fixed and verified.', metadata: { turnPhase: 'final', turnStatus: 'completed' } }]
    await vi.advanceTimersByTimeAsync(1000)
    await settleVue(20)
    expect(text(el.querySelector('.agents-log'))).toContain('Fixed and verified.')
    expect(el.querySelector('.k-ai-turn-progress__header:has(.agents-progress-actions)')).toBeNull()
    const calls = getRun.mock.calls.length
    await vi.advanceTimersByTimeAsync(3000)
    expect(getRun).toHaveBeenCalledTimes(calls)
  })

  it('accepts a terminal-only response without starting uncertain-admission discovery', async () => {
    const listRuns = vi.fn(async () => ({ items: [] }))
    const { el } = await mountChat({ listRuns, chatStream: async function* () {
      yield { event: 'done', data: { runID: 'run', content: 'Complete response without progress frames', status: 'Succeeded' } }
    } })
    const reads = listRuns.mock.calls.length
    await send(el)
    expect(text(el.querySelector('.agents-log'))).toContain('Complete response without progress frames')
    expect(text(el)).not.toContain('Run failed')
    expect(listRuns).toHaveBeenCalledTimes(reads)
    expect(el.querySelector('.agents-composer [contenteditable="true"]')?.textContent).toBe('')
  })

  it('preserves a terminal-only backend error without treating it as a lost acknowledgement', async () => {
    const listRuns = vi.fn(async () => ({ items: [] }))
    const { el } = await mountChat({ listRuns, chatStream: async function* () {
      yield { event: 'error', data: { runID: 'run', message: 'Backend failure details', status: 'Failed' } }
    } })
    const reads = listRuns.mock.calls.length
    await send(el)
    expect(text(el)).toContain('Backend failure details')
    expect(text(el)).not.toContain('stream closed before')
    expect(listRuns).toHaveBeenCalledTimes(reads)
  })

  it.each([400, 401, 403, 404, 409])('does not recover a definitively refused admission (status=%s)', async status => {
    const listRuns = vi.fn(async () => ({ items: [] }))
    const { el } = await mountChat({ listRuns, chatStream: async function* () { throw new ApiError(status, 'Admission refused') } })
    const reads = listRuns.mock.calls.length
    await send(el)
    expect(listRuns).toHaveBeenCalledTimes(reads)
    expect(el.querySelector('.agents-composer .agents-stop')).toBeNull()
  })

  it.each([{ cleanEOF: false, terminal: false }, { cleanEOF: true, terminal: false }, { cleanEOF: false, terminal: true }, { cleanEOF: true, terminal: true }])('discovers a run after its first acknowledgement is lost (%j)', async ({ cleanEOF, terminal }) => {
    vi.useFakeTimers()
    let admitted: RunDetail | undefined
    const chatStream = vi.fn(async function* (_agent: string, _text: string, session: string) {
      admitted = { ...run(terminal ? 'Succeeded' : 'Running'), sessionID: session, progress: [partial, command] }
      if (!cleanEOF) throw new Error('Connection dropped before first chat frame')
    })
    const { el } = await mountChat({
      listRuns: async () => ({ items: admitted ? [admitted] : [] }),
      listMessagePage: async () => ({ items: admitted ? terminal ? [{ ...partial, content: 'Finished before acknowledgement', metadata: { turnPhase: 'final', turnStatus: 'completed' } }, user] : [user] : [] }),
      getRun: async () => admitted, chatStream,
    })
    await send(el)
    await vi.advanceTimersByTimeAsync(5000)
    expect(text(el.querySelector('.agents-log'))).toContain(terminal ? 'Finished before acknowledgement' : partial.content)
    if (terminal) expect(el.querySelector('.agents-composer .agents-stop')).toBeNull()
    else expect(el.querySelector('.agents-composer .agents-stop')).not.toBeNull()
    expect(chatStream).toHaveBeenCalledTimes(1)
    expect(el.querySelector('.agents-composer [contenteditable="true"]')?.textContent).toContain('Fix it')
  })

  it.each([false, true])('reattaches when a question reply disconnects before start (clean EOF=%s)', async cleanEOF => {
    vi.useFakeTimers()
    let detail: RunDetail = { ...run('PendingApproval'), pending: { inboxID: 'question', kind: 'question', tool: '', args: '', question: 'Continue?' } }
    let failRead = false
    const getRun = vi.fn(async () => {
      if (failRead) { failRead = false; throw new ApiError(503, 'temporary status failure') }
      return detail
    })
    const answerStream = vi.fn(async function* () {
      detail = { ...run(), progress: [partial, command] }
      failRead = cleanEOF
      if (!cleanEOF) throw new Error('Reply transport disconnected before acknowledgement')
    })
    const chatStream = vi.fn()
    const { el } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listMessagePage: async () => ({ items: [user] }),
      listRuns: async () => ({ items: [detail] }), getRun, answerStream, chatStream,
      listInbox: async () => { throw new ApiError(503, 'Inbox temporarily unavailable') },
    })
    expect(text(el.querySelector('.k-ai-conversation-status'))).toBe('Waiting for your answer')
    const card = el.querySelector('.agents-question-card')!
    const input = card.querySelector<HTMLInputElement>('input[type="text"]')!
    input.value = 'Continue'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await settleVue()
    card.querySelector<HTMLButtonElement>('button[type="submit"]')!.click()
    await settleVue(30)
    await vi.advanceTimersByTimeAsync(5000)
    expect(text(el.querySelector('.agents-log'))).toContain(partial.content)
    expect(el.querySelectorAll('.k-ai-action-row')).toHaveLength(1)
    expect(el.querySelector('.agents-composer .agents-stop')).not.toBeNull()
    expect(answerStream).toHaveBeenCalledTimes(1)
    expect(chatStream).not.toHaveBeenCalled()
    expect(text(el)).not.toContain('Could not')
  })

  it('follows an approval continuation without a replacement chat stream or lifecycle events', async () => {
    let detail: RunDetail = { ...run('PendingApproval'), pending: { inboxID: 'approve', kind: 'approval', tool: 'shell', args: '{}' } }
    let admitted = false
    let history: TranscriptMessage[] = []
    const resolveInbox = vi.fn(async () => {
      detail = { ...run(), progress: [partial, command] }
      return {}
    })
    const chatStream = vi.fn(async function* () {
      admitted = true
      yield { event: 'start', data: { runID: 'run', sessionID: 'session' } } as SSEEvent
      yield { event: 'approval_required', data: { runID: 'run', inboxID: 'approve', kind: 'approval', tool: 'shell', args: '{}' } } as SSEEvent
    })
    const { el } = await mountChat({
      chatStream, resolveInbox, getRun: async () => detail,
      listMessagePage: async () => ({ items: [...history].reverse() }),
      listRuns: async () => ({ items: admitted ? [detail] : [] }),
    })
    vi.useFakeTimers()
    await send(el)
    history = [user]
    expect(text(el.querySelector('.k-ai-conversation-status'))).toBe('Waiting for approval')
    el.querySelector<HTMLButtonElement>('.agents-approval-actions .k-btn--primary')!.click()
    await settleVue(24)
    await vi.advanceTimersByTimeAsync(1000)
    expect(text(el.querySelector('.k-ai-conversation-status'))).toBe('Running')
    expect(resolveInbox).toHaveBeenCalledWith('approve', 'approve')
    expect(chatStream).toHaveBeenCalledTimes(1)
    expect(text(el.querySelector('.agents-log'))).toContain(partial.content)
    expect(el.querySelectorAll('.k-ai-action-row')).toHaveLength(1)
    expect(el.querySelector('.agents-composer .agents-stop')).not.toBeNull()
    expect(el.querySelector('.agents-run-stop')).toBeNull()

    // A later interrupt is a new inbox identity even when the previous
    // approval's accepted receipt is still visible on this assistant turn.
    detail = { ...run('PendingApproval'), pending: { inboxID: 'second', kind: 'approval', tool: 'deploy', args: '{}' } }
    await vi.advanceTimersByTimeAsync(1000)
    expect(text(el.querySelector('.agents-approval'))).toContain('deploy')
    expect(el.querySelector('.agents-approval-actions .k-btn--primary')).not.toBeNull()
  })

  it('retries automatically when even the first disconnected-run status read fails', async () => {
    const getRun = vi.fn()
      .mockRejectedValueOnce(new ApiError(503, 'unavailable'))
      .mockResolvedValue({ ...run(), progress: [partial, command] })
    const { el } = await mountChat({
      listRuns: async () => ({ items: [] }),
      listMessagePage: async () => ({ items: [user] }), getRun,
      chatStream: async function* () {
        yield { event: 'start', data: { runID: 'run', sessionID: 'session' } }
        yield { event: 'run_started', data: { runID: 'run', sessionID: 'session' } }
        throw new Error('connection lost')
      },
    })
    vi.useFakeTimers()
    await send(el)
    await vi.advanceTimersByTimeAsync(1000)
    expect(text(el.querySelector('.agents-log'))).toContain(partial.content)
    expect(el.querySelectorAll('.k-ai-action-row')).toHaveLength(1)
  })

  it('discards a progress read after navigating to another conversation', async () => {
    vi.useFakeTimers()
    let release!: (value: RunDetail) => void
    let reads = 0
    const getRun = vi.fn(() => ++reads === 1 ? Promise.resolve(run()) : new Promise<RunDetail>(resolve => { release = resolve }))
    const { el, view } = await mountChat({
      listSessions: async () => [{ id: 'session', preview: 'Current', messageCount: 1, createdAt: '', lastActivity: '' }],
      listMessagePage: async () => ({ items: [user] }), getRun,
    })
    await vi.advanceTimersByTimeAsync(1000)
    expect(release).toBeTypeOf('function')
    // Changing the agent invalidates the old conversation and its observer.
    await view.setProps({ name: 'other' })
    release({ ...run(), progress: [partial, command] })
    await settleVue(24)
    expect(text(el)).not.toContain(partial.content)
  })

  it('recovers an externally resumed approval after a trace body stalls without disconnecting', async () => {
    vi.useFakeTimers()
    const transport = new ApiClient()
    transport.setContext({ basePath: '/ui/providers/agents', tenant: 'cluster', orgUUID: 'org', workspaceUUID: 'ws', token: 'test' } as never)
    let phase = 'PendingApproval'
    let stall = false
    let stalled = false
    vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
      if (String(input).endsWith('/trace')) {
        if (stall) {
          stall = false
          stalled = true
          return { ok: true, status: 200, json: () => new Promise(() => {}) } as unknown as Response
        }
        return new Response(JSON.stringify(phase === 'PendingApproval'
          ? { pending: { inboxID: 'external', tool: 'shell', args: '{}' } }
          : { progress: [partial, command] }))
      }
      return new Response(JSON.stringify({ metadata: { name: 'run' }, spec: { agentRef: 'coder', sessionID: 'session', trigger: 'chat' }, status: { phase } }))
    })
    const { el } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listMessagePage: async () => ({ items: [user] }), getRun: transport.getRun,
      listRuns: async () => ({ items: [run('PendingApproval')] }),
    })
    expect(el.querySelector('.agents-approval')).not.toBeNull()
    phase = 'Running' // another observer accepts the approval; no lifecycle event
    stall = true
    await vi.advanceTimersByTimeAsync(1000)
    expect(stalled).toBe(true)
    await vi.advanceTimersByTimeAsync(15000)
    expect(text(el)).toContain('Reconnecting')
    await vi.advanceTimersByTimeAsync(2000)
    expect(text(el.querySelector('.agents-log'))).toContain(partial.content)
    expect(el.querySelectorAll('.k-ai-action-row')).toHaveLength(1)
    expect(el.querySelector('.agents-approval-dock')).toBeNull()
    expect(text(el)).not.toContain('Reconnecting')
  })

  it('backs off after a disconnected progress read and recovers automatically', async () => {
    vi.useFakeTimers()
    const getRun = vi.fn()
      .mockResolvedValueOnce(run()) // initial orphan discovery
      .mockRejectedValueOnce(new ApiError(503, 'temporarily unavailable'))
      .mockResolvedValue({ ...run(), progress: [partial] })
    const { el } = await mountChat({
      listSessions: async () => [{ id: 'session', preview: 'Current', messageCount: 1, createdAt: '', lastActivity: '' }],
      listMessagePage: async () => ({ items: [user] }), getRun,
    })
    await vi.advanceTimersByTimeAsync(1000)
    expect(text(el)).toContain('Reconnecting')
    expect(text(el.querySelector('.k-ai-conversation-status'))).toBe('Reconnecting')
    expect(el.querySelectorAll('.k-ai-conversation-status .k-ai-conversation-status__dot')).toHaveLength(0)
    expect(el.querySelector('.k-stale')?.getAttribute('role')).toBe('status')
    expect(el.querySelector('.agents-composer .agents-stop')).not.toBeNull()
    await vi.advanceTimersByTimeAsync(2000)
    expect(text(el.querySelector('.agents-log'))).toContain(partial.content)
    expect(text(el)).not.toContain('Reconnecting')
    expect(el.querySelector('.agents-orphan-banner')).toBeNull()
    expect(text(el.querySelector('.k-ai-conversation-status'))).toBe('Running')
  })

  it.each([401, 403, 404].flatMap(status => [false, true].map(resource => ({ status, resource }))))('stops observing unavailable progress and explains why (%j)', async ({ status, resource }) => {
    vi.useFakeTimers()
    let unavailable = true
    const getRun = vi.fn(async (_id: string, options?: { requireTrace?: boolean }) => {
      if (options?.requireTrace && unavailable) throw resource ? new ResourceError(status, 'Forbidden', 'Run access unavailable') : new ApiError(status, 'Trace unavailable')
      return { ...run(), progress: [partial] }
    })
    const { el } = await mountChat({
      listSessions: async () => [{ id: 'session', preview: 'Current', messageCount: 1, createdAt: '', lastActivity: '' }],
      listMessagePage: async () => ({ items: [user] }), getRun,
    })
    await vi.advanceTimersByTimeAsync(1000)
    expect(getRun).toHaveBeenCalledWith('run', { requireTrace: true })
    expect(text(el.querySelector('.k-stale'))).toContain('Access to run progress is no longer available.')
    expect(text(el.querySelector('.agents-log'))).toContain(user.content)
    const calls = getRun.mock.calls.length
    await vi.advanceTimersByTimeAsync(5000)
    expect(getRun).toHaveBeenCalledTimes(calls)
    unavailable = false
    el.querySelector<HTMLButtonElement>('.k-stale button')!.click()
    await settleVue(24)
    await vi.advanceTimersByTimeAsync(1000)
    expect(text(el.querySelector('.agents-log'))).toContain(partial.content)
    expect(text(el)).not.toContain('Access to run progress is no longer available.')
    const resumed = getRun.mock.calls.length
    await vi.advanceTimersByTimeAsync(1000)
    expect(getRun.mock.calls.length).toBeGreaterThan(resumed)
  })
})

it('reconciles a completed command with an older pending snapshot without duplication', () => {
  const completed = { ...command, content: 'PASS', metadata: { ...command.metadata, pending: false } }
  const rows = transcriptWithProgress([user, completed], [partial, command])
  const projected = rebuildTranscript(rows)
  expect(projected[1].tools).toHaveLength(1)
  expect(projected[1].tools[0]).toMatchObject({ pending: false, result: 'PASS' })
})

it('shows new harness text after an approval without losing the earlier segment', () => {
  const waiting: TranscriptMessage = {
    ...partial, id: 'waiting', content: 'May I run the tests?',
    createdAt: '2026-11-06T10:00:00Z', metadata: { turnPhase: 'terminal', turnStatus: 'waiting' },
  }
  const projected = rebuildTranscript(transcriptWithProgress([user, waiting], [partial]))
  expect(projected[1].content).toBe(partial.content)
  expect(projected[1].progress?.trace).toMatchObject([{ kind: 'commentary', content: waiting.content }])
})

describe('terminal recovery and rejected prompts', () => {
  it.each([false, true])('hydrates final history before delayed cancellation settles (reject=%s)', async reject => {
    vi.useFakeTimers()
    let final = false
    let finishCancel!: () => void
    const cancelRun = vi.fn(() => new Promise<void>((resolve, fail) => { finishCancel = () => reject ? fail(new ApiError(409, 'Run already finished')) : resolve() }))
    const { el, store } = await mountChat({
      listRuns: async () => ({ items: [] }), cancelRun,
      listMessagePage: async () => ({ items: final ? [{ ...partial, content: 'Final after delayed cancellation', metadata: { turnPhase: 'final', turnStatus: 'completed' } }, user] : [] }),
      chatStream: async function* (_name: string, _text: string, session: string, signal: AbortSignal) {
        yield { event: 'start', data: { runID: 'run', sessionID: session } }
        yield { event: 'run_started', data: { runID: 'run', sessionID: session, status: 'Running' } }
        yield { event: 'delta', data: { text: 'Initial content' } }
        await new Promise<void>(resolve => signal.addEventListener('abort', () => resolve(), { once: true }))
      },
    })
    await send(el)
    el.querySelector<HTMLButtonElement>('.agents-stop')!.click()
    await settleVue(20)
    final = true
    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'run', phase: 'Succeeded' } } }))
    await settleVue(20)
    await vi.advanceTimersByTimeAsync(20000)
    expect(text(el)).toContain('Final after delayed cancellation')
    finishCancel()
    await settleVue(40)
    await vi.advanceTimersByTimeAsync(5000)
    expect(text(el)).toContain('Final after delayed cancellation')
  })

  it.each([1, 3])('retries final transcript hydration after transient history failures (failures=%s)', async failures => {
    vi.useFakeTimers()
    let terminal = false
    let failFinalRead = 0
    let finalFailureObserved = false
    const listMessagePage = vi.fn(async () => {
      if (failFinalRead) {
        failFinalRead -= 1
        finalFailureObserved = true
        throw new ApiError(503, 'Transient final history failure')
      }
      return { items: terminal && finalFailureObserved ? [{ ...partial, content: 'Canonical final recovered', metadata: { turnPhase: 'final', turnStatus: 'completed' } }, user] : [user] }
    })
    const { el } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Selected chat' }],
      listMessagePage,
      getRun: async (_id: string, options?: { requireTrace?: boolean }) => {
        if (terminal && options?.requireTrace) failFinalRead = failures
        return { ...run(terminal ? 'Succeeded' : 'Running'), progress: [partial] }
      },
    })
    await vi.advanceTimersByTimeAsync(1000)
    expect(text(el)).toContain(partial.content)
    terminal = true
    await vi.advanceTimersByTimeAsync(1000)
    expect(finalFailureObserved).toBe(true)
    await vi.advanceTimersByTimeAsync(15000)
    expect(text(el)).toContain('Canonical final recovered')
  })

  it('keeps the draft and active run when Enter is pressed during recovered progress', async () => {
    vi.useFakeTimers()
    let latest = partial.content
    const chatStream = vi.fn()
    const { el } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Selected chat' }],
      listRuns: async () => ({ items: [run()] }),
      listMessagePage: async () => ({ items: [user] }),
      getRun: async () => ({ ...run(), progress: [{ ...partial, content: latest }] }), chatStream,
    })
    await vi.advanceTimersByTimeAsync(1000)
    setComposerText(el, 'My next message')
    await settleVue()
    el.querySelector<HTMLElement>('[contenteditable="true"]')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    await settleVue()
    latest = 'Old run is still making progress'
    await vi.advanceTimersByTimeAsync(2000)
    expect(chatStream).not.toHaveBeenCalled()
    expect(text(el)).toContain(latest)
    expect(text(el.querySelector('[contenteditable="true"]'))).toContain('My next message')
    expect(el.querySelector('.agents-composer .agents-stop')).not.toBeNull()
    expect(el.querySelector('.agents-run-stop')).toBeNull()
  })
})


it.each(['navigation', 'access'])('ends terminal history retries on %s', async stop => {
  vi.useFakeTimers()
  let terminal = false
  let failFinalRead = false
  const listMessagePage = vi.fn(async () => {
    if (failFinalRead) throw new ApiError(stop === 'access' ? 403 : 503, 'Final history unavailable')
    return { items: [user] }
  })
  const { el } = await mountChat({
    listSessions: async () => [{ id: 'session', title: 'Selected chat' }], listMessagePage,
    getRun: async (_id: string, options?: { requireTrace?: boolean }) => {
      if (terminal && options?.requireTrace) failFinalRead = true
      return { ...run(terminal ? 'Succeeded' : 'Running'), progress: [partial] }
    },
  })
  await vi.advanceTimersByTimeAsync(1000)
  terminal = true
  await vi.advanceTimersByTimeAsync(1000)
  expect(text(el)).toContain('Final history unavailable')
  if (stop === 'navigation') {
    const button = [...el.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'New thread')!
    button.click()
    await settleVue(24)
  }
  const reads = listMessagePage.mock.calls.length
  await vi.advanceTimersByTimeAsync(10000)
  expect(listMessagePage).toHaveBeenCalledTimes(reads)
})


it('does not rebind a queued terminal retry after authority changes', async () => {
  vi.useFakeTimers()
  let terminal = false
  let failed = false
  const listMessagePage = vi.fn(async () => {
    if (failed) throw new ApiError(503, 'Final history unavailable')
    return { items: [user] }
  })
  const api = stubApi({
    listRuns: async () => ({ items: [run()] }),
    listSessions: async () => [{ id: 'session', title: 'Selected chat' }], listMessagePage,
    getRun: async (_id: string, options?: { requireTrace?: boolean }) => {
      if (terminal && options?.requireTrace) failed = true
      return { ...run(terminal ? 'Succeeded' : 'Running'), progress: [partial] }
    },
  })
  const store = makeStore(api)
  store.agents.data = [agentFixture('coder')]
  store.agents.loaded = store.agents.hasSnapshot = true
  const authorityEpoch = ref(0)
  const route = ref<Route>({ kind: 'agent', name: 'coder', tab: 'chat' })
  const host = document.createElement('div')
  document.body.appendChild(host)
  const wrapper = defineComponent({ setup() {
    provideAgentsRuntime({ store: shallowRef(store), api: shallowRef(api), route, authorityEpoch, host: ref(host) })
    return () => h(AgentChat, { store, api, name: 'coder' })
  } })
  const view = await mountVue(wrapper, {})
  mounted.push(view)
  await settleVue(24)
  await vi.advanceTimersByTimeAsync(1000)
  terminal = true
  await vi.advanceTimersByTimeAsync(1000)
  expect(text(view.element)).toContain('Final history unavailable')
  const reads = listMessagePage.mock.calls.length
  authorityEpoch.value++
  await settleVue(24)
  await vi.advanceTimersByTimeAsync(10000)
  expect(listMessagePage).toHaveBeenCalledTimes(reads)
  host.remove()
})

describe('approval and terminal retry races', () => {
  it('does not redisclose a consumed approval from an older progress read', async () => {
    vi.useFakeTimers()
    const parked: RunDetail = { ...run('PendingApproval'), pending: { inboxID: 'approval', tool: 'shell', args: '{}' } }
    let finishRead!: (detail: RunDetail) => void
    let firstStrict = true
    const { el, store } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listRuns: async () => ({ items: [parked] }),
      listMessagePage: async () => ({ items: [partial, user] }),
      getRun: async (_id: string, options?: { requireTrace?: boolean }) => {
        if (!options?.requireTrace) return parked
        if (firstStrict) { firstStrict = false; return new Promise<RunDetail>(resolve => { finishRead = resolve }) }
        throw new ApiError(503, 'Connection lost after resume')
      },
    })
    expect(el.querySelector('.agents-permission-card')).not.toBeNull()
    await vi.advanceTimersByTimeAsync(1000)
    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'run', phase: 'Running' } } }))
    await settleVue(24)
    expect(el.querySelector('.agents-permission-card')).toBeNull()
    finishRead(parked)
    await settleVue(30)
    await vi.advanceTimersByTimeAsync(8000)
    expect(el.querySelector('.agents-permission-card')).toBeNull()
  })

  it('continues final history recovery after View run and browser Back', async () => {
    vi.useFakeTimers()
    let terminal = false
    let failNext = false
    let failureObserved = false
    const api = stubApi({
      listRuns: async () => ({ items: [run()] }),
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listMessagePage: async () => {
        if (failNext) { failNext = false; failureObserved = true; throw new ApiError(503, 'Final history unavailable') }
        return { items: terminal && failureObserved ? [{ ...partial, content: 'Final after browser Back', metadata: { turnPhase: 'final', turnStatus: 'completed' } }, user] : [user] }
      },
      getRun: async (_id: string, options?: { requireTrace?: boolean }) => {
        if (terminal && options?.requireTrace) failNext = true
        return { ...run(terminal ? 'Succeeded' : 'Running'), progress: [partial] }
      },
    })
    const store = makeStore(api)
    store.agents.data = [agentFixture('coder')]
    store.agents.loaded = store.agents.hasSnapshot = true
    const route = ref<Route>({ kind: 'agent', name: 'coder', tab: 'chat' })
    const host = document.createElement('div')
    document.body.appendChild(host)
    const wrapper = defineComponent({ setup() {
      provideAgentsRuntime({ store: shallowRef(store), api: shallowRef(api), route, authorityEpoch: ref(0), host: ref(host) })
      return () => h(AgentChat, { store, api, name: 'coder' })
    } })
    const view = await mountVue(wrapper, {})
    mounted.push(view)
    await settleVue(24)
    await vi.advanceTimersByTimeAsync(1000)
    terminal = true
    await vi.advanceTimersByTimeAsync(1000)
    expect(failureObserved).toBe(true)
    route.value = { kind: 'agent', name: 'coder', tab: 'runs', runID: 'run' }
    await settleVue(24)
    await vi.advanceTimersByTimeAsync(3000)
    route.value = { kind: 'agent', name: 'coder', tab: 'chat' }
    await settleVue(24)
    await vi.advanceTimersByTimeAsync(10000)
    expect(text(view.element)).toContain('Final after browser Back')
    host.remove()
  })

  it.each([409, 429].flatMap(status => [false, true].map(provisional => ({ status, provisional }))))('keeps queued terminal recovery when a later prompt is refused (%j)', async ({ status, provisional }) => {
    vi.useFakeTimers()
    let terminal = false
    let failNext = false
    let failureObserved = false
    const { el } = await mountChat({
      listRuns: async () => ({ items: [run()] }),
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listMessagePage: async () => {
        if (failNext) { failNext = false; failureObserved = true; throw new ApiError(503, 'Final history unavailable') }
        return { items: terminal && failureObserved ? [{ ...partial, content: 'Final after refused new prompt', metadata: { turnPhase: 'final', turnStatus: 'completed' } }, user] : [user] }
      },
      getRun: async (_id: string, options?: { requireTrace?: boolean }) => {
        if (terminal && options?.requireTrace) failNext = true
        return { ...run(terminal ? 'Succeeded' : 'Running'), progress: [partial] }
      },
      chatStream: async function* (_agent: string, _text: string, session: string) {
        if (!provisional) throw new ApiError(status, 'New prompt refused')
        yield { event: 'start', data: { runID: 'refused', sessionID: session } }
        yield { event: 'error', data: { runID: 'refused', status: 'Failed', message: 'New prompt refused' } }
      },
    })
    await vi.advanceTimersByTimeAsync(1000)
    terminal = true
    await vi.advanceTimersByTimeAsync(1000)
    expect(failureObserved).toBe(true)
    await send(el)
    await vi.advanceTimersByTimeAsync(10000)
    expect(text(el)).toContain('Final after refused new prompt')
  })
})


it('keeps a newly arrived approval when an older running snapshot settles', async () => {
  vi.useFakeTimers()
  let detail: RunDetail = run()
  let firstStrict = true
  let finishRead!: (detail: RunDetail) => void
  const { el, store } = await mountChat({
    listSessions: async () => [{ id: 'session', title: 'Current chat' }],
    listRuns: async () => ({ items: [detail] }),
    listMessagePage: async () => ({ items: [partial, user] }),
    getRun: async (_id: string, options?: { requireTrace?: boolean }) => {
      if (!options?.requireTrace) return detail
      if (firstStrict) { firstStrict = false; return new Promise<RunDetail>(resolve => { finishRead = resolve }) }
      throw new ApiError(503, 'Connection lost before approval')
    },
  })
  await vi.advanceTimersByTimeAsync(1000)
  detail = { ...run('PendingApproval'), pending: { inboxID: 'new-approval', tool: 'shell', args: '{}' } }
  store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'run', phase: 'PendingApproval' } } }))
  await settleVue(24)
  expect(el.querySelector('.agents-permission-card')).not.toBeNull()
  finishRead(run())
  await settleVue(30)
  await vi.advanceTimersByTimeAsync(8000)
  expect(el.querySelector('.agents-permission-card')).not.toBeNull()
})

describe('disconnected approval and discovery recovery', () => {
  it.each([401, 403, 404].flatMap(status => [false, true].map(resource => ({ status, resource }))))('ignores superseded access errors after a newer successful approval read (%j)', async ({ status, resource }) => {
    vi.useFakeTimers()
    let detail: RunDetail = run()
    let firstStrict = true
    let failOldRead!: (error: Error) => void
    const getRun = vi.fn(async (_id: string, options?: { requireTrace?: boolean }) => {
      if (options?.requireTrace && firstStrict) {
        firstStrict = false
        return new Promise<RunDetail>((_resolve, reject) => { failOldRead = reject })
      }
      return detail
    })
    const { el, store } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listRuns: async () => ({ items: [detail] }),
      listMessagePage: async () => ({ items: [partial, user] }), getRun,
    })
    await vi.advanceTimersByTimeAsync(1000)
    detail = { ...run('PendingApproval'), pending: { inboxID: 'approval', tool: 'shell', args: '{}' } }
    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'run', phase: 'PendingApproval' } } }))
    await settleVue(24)
    // A newer successful detail read has already confirmed current access.
    expect(el.querySelector('.agents-permission-card')).not.toBeNull()
    failOldRead(resource ? new ResourceError(status, 'Unavailable', 'Old request was unavailable') : new ApiError(status, 'Old request was unavailable'))
    await settleVue(24)
    detail = { ...run(), progress: [{ ...partial, id: 'resumed-after-approval', content: 'Resumed output with access restored' }] }
    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'run', phase: 'Running' } } }))
    await settleVue(24)
    await vi.advanceTimersByTimeAsync(10000)
    expect(text(el)).toContain('Resumed output with access restored')
    expect(text(el)).not.toContain('Access to run progress is no longer available.')
    expect(getRun.mock.calls.filter(([, options]) => options?.requireTrace).length).toBeGreaterThan(1)
  })

  it.each(['Running', 'PendingApproval'] as const)('preserves fresh approval details without events (projected phase=%s)', async phase => {
    vi.useFakeTimers()
    let finishApproval!: (detail: RunDetail) => void
    let finishProgress!: (detail: RunDetail) => void
    let firstStrict = true
    const { el } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listMessagePage: async () => ({ items: [partial, user] }),
      getRun: async (_id: string, options?: { requireTrace?: boolean }) => {
        if (!options?.requireTrace) return new Promise<RunDetail>(resolve => { finishApproval = resolve })
        if (firstStrict) { firstStrict = false; return new Promise<RunDetail>(resolve => { finishProgress = resolve }) }
        throw new ApiError(503, 'Progress disconnected')
      },
    })
    await vi.advanceTimersByTimeAsync(1000)
    finishApproval({ ...run(phase), pending: { inboxID: 'fresh-approval', tool: 'shell', args: '{}' } })
    await settleVue(24)
    expect(el.querySelector('.agents-permission-card')).not.toBeNull()
    finishProgress({ ...run(), progress: [partial] })
    await settleVue(24)
    await vi.advanceTimersByTimeAsync(8000)
    expect(el.querySelector('.agents-permission-card')).not.toBeNull()
  })

  it.each(['workbench', 'authority', 'thread'])('scopes unknown-run discovery retries after lost acknowledgement (%s)', async destination => {
    vi.useFakeTimers()
    let admitted: RunDetail | undefined
    let discoveryFailed = false
    let discoveryReads = 0
    const chatStream = vi.fn(async function* (_agent: string, _text: string, session: string) {
      admitted = { ...run(), sessionID: session, progress: [partial] }
      throw new Error('Lost initial acknowledgement')
    })
    const api = stubApi({
      chatStream,
      listRuns: async () => {
        discoveryReads += 1
        if (admitted && !discoveryFailed) { discoveryFailed = true; throw new ApiError(503, 'Run discovery unavailable') }
        return { items: admitted ? [admitted] : [] }
      },
      listMessagePage: async () => ({ items: admitted ? [user] : [] }),
      getRun: async () => admitted,
    })
    const store = makeStore(api)
    const agent = agentFixture('coder')
    agent.spec.backend = { type: 'harness' }
    store.agents.data = [agent]
    store.agents.loaded = store.agents.hasSnapshot = true
    const route = ref<Route>({ kind: 'agent', name: 'coder', tab: 'chat' })
    const host = document.createElement('div')
    document.body.appendChild(host)
    const authorityEpoch = ref(0)
    const wrapper = defineComponent({ setup() {
      provideAgentsRuntime({ store: shallowRef(store), api: shallowRef(api), route, authorityEpoch, host: ref(host) })
      return () => h(AgentChat, { store, api, name: 'coder' })
    } })
    const view = await mountVue(wrapper, {})
    mounted.push(view)
    await settleVue(30)
    await send(view.element)
    expect(discoveryFailed).toBe(true)
    const reads = discoveryReads
    if (destination === 'workbench') {
      route.value = { kind: 'agent', name: 'coder', tab: 'runs' }
      await settleVue(24)
      await vi.advanceTimersByTimeAsync(3000)
      route.value = { kind: 'agent', name: 'coder', tab: 'chat' }
    } else if (destination === 'authority') {
      authorityEpoch.value++
    } else {
      const button = [...view.element.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent?.trim() === 'New thread')!
      button.click()
    }
    await settleVue(24)
    await vi.advanceTimersByTimeAsync(10000)
    if (destination === 'workbench') expect(text(view.element)).toContain(partial.content)
    else expect(discoveryReads).toBe(reads)
    expect(chatStream).toHaveBeenCalledTimes(1)
    host.remove()
  })
})

describe('shared run read arbitration and restored access', () => {
  it.each(['permission', 'question'])('does not reopen a consumed disclosure from an older approval read (%s)', async kind => {
    vi.useFakeTimers()
    let finishOldApproval!: (detail: RunDetail) => void
    let firstProgress = true
    const parked: RunDetail = { ...run('PendingApproval'), pending: kind === 'question'
      ? { inboxID: 'already-consumed', kind: 'question', question: 'Continue?', tool: '', args: '' }
      : { inboxID: 'already-consumed', tool: 'shell', args: '{}' } }
    const { el } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listRuns: async () => ({ items: [parked] }),
      listMessagePage: async () => ({ items: [user] }),
      getRun: async (_id: string, options?: { requireTrace?: boolean }) => {
        if (!options?.requireTrace) return new Promise<RunDetail>(resolve => { finishOldApproval = resolve })
        if (firstProgress) { firstProgress = false; return { ...run(), progress: [{ ...partial, content: 'Authoritative progress after external approval' }] } }
        throw new ApiError(503, 'Disconnected after resume')
      },
    })
    await vi.advanceTimersByTimeAsync(1000)
    expect(text(el)).toContain('Authoritative progress after external approval')
    expect(el.querySelector('.agents-permission-card,.agents-question-card')).toBeNull()
    finishOldApproval(parked)
    await settleVue(24)
    await vi.advanceTimersByTimeAsync(8000)
    expect(el.querySelector('.agents-permission-card,.agents-question-card')).toBeNull()
  })

  it.each([{ status: 401, resource: false, events: false }, { status: 403, resource: false, events: false }, { status: 404, resource: false, events: false }, { status: 403, resource: true, events: false }, { status: 403, resource: false, events: true }])('restarts progress after a fresh disclosure restores access (%j)', async ({ status, resource, events }) => {
    vi.useFakeTimers()
    let finishAuthorizedRead!: (detail: RunDetail) => void
    let firstProgress = true
    let resumed = false
    const parked: RunDetail = { ...run('PendingApproval'), pending: { inboxID: 'approval', tool: 'shell', args: '{}' } }
    const getRun = vi.fn(async (_id: string, options?: { requireTrace?: boolean }) => {
      if (!options?.requireTrace) return new Promise<RunDetail>(resolve => { finishAuthorizedRead = resolve })
      if (firstProgress) { firstProgress = false; throw resource ? new ResourceError(status, 'Unavailable', 'Access was previously unavailable') : new ApiError(status, 'Access was previously unavailable') }
      return { ...run(resumed ? 'Running' : 'PendingApproval'), ...(resumed ? { progress: [{ ...partial, content: 'Progress after access was restored' }] } : { pending: parked.pending }) }
    })
    const { el, store } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listRuns: async () => ({ items: [parked] }),
      listMessagePage: async () => ({ items: [user] }), getRun,
    })
    await vi.advanceTimersByTimeAsync(1000)
    expect(text(el)).toContain('Access to run progress is no longer available.')
    finishAuthorizedRead(parked)
    await settleVue(24)
    expect(el.querySelector('.agents-permission-card')).not.toBeNull()
    resumed = true
    if (events) store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'run', phase: 'Running' } } }))
    await settleVue(24)
    await vi.advanceTimersByTimeAsync(10000)
    expect(text(el)).toContain('Progress after access was restored')
    expect(text(el)).not.toContain('Access to run progress is no longer available.')
  })
})


it('keeps progress stopped when a metadata-only approval read cannot confirm trace access', async () => {
  vi.useFakeTimers()
  let finishMetadata!: (detail: RunDetail) => void
  const getRun = vi.fn(async (_id: string, options?: { requireTrace?: boolean }) => {
    if (!options?.requireTrace) return new Promise<RunDetail>(resolve => { finishMetadata = resolve })
    throw new ApiError(403, 'Trace access denied')
  })
  const { el } = await mountChat({
    listSessions: async () => [{ id: 'session', title: 'Current chat' }],
    listMessagePage: async () => ({ items: [user] }), getRun,
  })
  await vi.advanceTimersByTimeAsync(1000)
  finishMetadata(run('PendingApproval'))
  await settleVue(24)
  await vi.advanceTimersByTimeAsync(10000)
  expect(text(el)).toContain('Access to run progress is no longer available.')
  expect(getRun.mock.calls.filter(([, options]) => options?.requireTrace)).toHaveLength(1)
})


describe('composer owns the active run control', () => {
  it.each(['Pending', 'Running', 'PendingApproval'] as const)('restores one composer Stop for a selected active run (%s)', async phase => {
    let finished = false
    const chatStream = vi.fn()
    const { el, store } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listRuns: async () => ({ items: [run(finished ? 'Succeeded' : phase)] }),
      listMessagePage: async () => ({ items: finished ? [{ ...partial, content: 'Finished run', metadata: { turnPhase: 'final', turnStatus: 'completed' } }, user] : [user] }),
      getRun: async () => run(finished ? 'Succeeded' : phase), chatStream,
    })
    expect(text(el.querySelector('.k-ai-conversation-status'))).toBe(phase === 'PendingApproval' ? 'Waiting for approval' : 'Running')
    expect(el.querySelectorAll('.agents-stop')).toHaveLength(1)
    expect(el.querySelector('.agents-composer .agents-stop')?.getAttribute('aria-label')).toBe('Stop run')
    expect(el.querySelector('.agents-run-stop')).toBeNull()
    expect(el.querySelector('.agents-progress-actions .agents-message-run-link')).not.toBeNull()
    expect(el.querySelector('.agents-composer button[type="submit"]')).toBeNull()
    setComposerText(el, 'Keep this draft')
    await settleVue(12)
    el.querySelector<HTMLElement>('[contenteditable="true"]')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    await settleVue(12)
    expect(chatStream).not.toHaveBeenCalled()
    finished = true
    store.dispatchEvent(new CustomEvent('server', { detail: { type: 'run', data: { id: 'run', phase: 'Succeeded' } } }))
    await settleVue(30)
    expect(el.querySelector('.agents-stop')).toBeNull()
    expect(el.querySelector('.k-ai-conversation-status')).toBeNull()
    expect(el.querySelector('.agents-composer button[type="submit"]')).not.toBeNull()
    expect(text(el.querySelector('[contenteditable="true"]'))).toContain('Keep this draft')
  })

  it('stops the known reconnect target before a progress snapshot is available', async () => {
    vi.useFakeTimers()
    let cancelled = false
    const cancelRun = vi.fn(async () => { cancelled = true })
    const { el } = await mountChat({
      listRuns: async () => ({ items: [] }), cancelRun,
      getRun: async () => {
        if (!cancelled) throw new ApiError(503, 'Progress temporarily unavailable')
        return run('Aborted')
      },
      listMessagePage: async () => ({ items: cancelled ? [{ ...partial, metadata: { turnPhase: 'terminal', turnStatus: 'aborted' } }, user] : [] }),
      chatStream: async function* () {
        yield { event: 'start', data: { runID: 'run', sessionID: 'session' } }
        yield { event: 'run_started', data: { runID: 'run', sessionID: 'session', status: 'Running' } }
        throw new Error('Stream disconnected')
      },
    })
    await send(el)
    expect(el.querySelector('.agents-composer .agents-stop')).not.toBeNull()
    el.querySelector<HTMLButtonElement>('.agents-composer .agents-stop')!.click()
    await settleVue(24)
    expect(cancelRun).toHaveBeenCalledExactlyOnceWith('run')
    expect(el.querySelector<HTMLButtonElement>('.agents-stop')?.disabled).toBe(true)
    await vi.advanceTimersByTimeAsync(3000)
    expect(el.querySelector('.agents-stop')).toBeNull()
    expect(el.querySelector('.agents-composer button[type="submit"]')).not.toBeNull()
  })

  it('keeps the draft and allows an explicit retry when recovered cancellation fails', async () => {
    vi.useFakeTimers()
    let ended = false
    const cancelRun = vi.fn().mockRejectedValueOnce(new ApiError(503, 'Cancel unavailable')).mockResolvedValueOnce({ cancelling: true })
    const { el } = await mountChat({
      listSessions: async () => [{ id: 'session', title: 'Current chat' }],
      listRuns: async () => ({ items: ended ? [] : [run()] }), cancelRun,
      getRun: async () => run(ended ? 'Aborted' : 'Running'),
      listMessagePage: async () => ({ items: [user] }),
    })
    setComposerText(el, 'Draft survives stopping')
    await settleVue(12)
    el.querySelector<HTMLButtonElement>('.agents-stop')!.click()
    await settleVue(24)
    expect(el.querySelector<HTMLButtonElement>('.agents-stop')?.disabled).toBe(false)
    expect(cancelRun).toHaveBeenCalledTimes(1)
    el.querySelector<HTMLButtonElement>('.agents-stop')!.click()
    await settleVue(24)
    expect(el.querySelector<HTMLButtonElement>('.agents-stop')?.disabled).toBe(true)
    expect(cancelRun).toHaveBeenCalledTimes(2)
    ended = true
    await vi.advanceTimersByTimeAsync(2000)
    expect(el.querySelector('.agents-stop')).toBeNull()
    expect(text(el.querySelector('[contenteditable="true"]'))).toContain('Draft survives stopping')
  })
})
