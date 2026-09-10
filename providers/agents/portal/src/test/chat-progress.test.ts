import { afterEach, describe, expect, it, vi } from 'vitest'
import AgentChat from '../views/AgentChat.vue'
import { rebuildTranscript } from '../vue/chat'
import type { SSEEvent } from '../api'
import type { TranscriptMessage } from '../types'
import { agentFixture, makeStore, stubApi } from './helpers'
import { mountVue, settleVue, text, type MountedVue } from './vue-helper'

const mounted: MountedVue[] = []

afterEach(() => {
  while (mounted.length) mounted.pop()?.unmount()
})

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(res => { resolve = res })
  return { promise, resolve }
}

function session(id: string) {
  return { id, preview: id, messageCount: 0, createdAt: '', lastActivity: '' }
}

describe('typed chat progress projection', () => {
  it('keeps server-ordered commentary, tools, final output, and timestamps together', () => {
    const messages = rebuildTranscript([
      { id: 'u1', role: 'user', content: 'Find the issue', runID: 'r1', createdAt: '2026-09-10T10:00:00Z' },
      {
        id: 'a1', role: 'assistant', content: 'I will inspect the repository.', runID: 'r1', createdAt: '2026-09-10T10:00:01Z',
        metadata: { turnPhase: 'commentary', turnStatus: 'running', startedAt: '2026-09-10T10:00:00Z', segmentDurationMS: 120 },
      },
      {
        id: 't1', role: 'tool', content: 'one result', runID: 'r1', createdAt: '2026-09-10T10:00:02Z',
        metadata: { tool: 'repo_search', args: '{"q":"issue"}', durationMS: 40 },
      },
      {
        id: 'a2', role: 'assistant', content: '', runID: 'r1', createdAt: '2026-09-10T10:00:03Z',
        metadata: { turnPhase: 'terminal', turnStatus: 'waiting', startedAt: '2026-09-10T10:00:00Z', durationMS: 160 },
      },
      {
        id: 'a3', role: 'assistant', content: 'The issue is fixed.', runID: 'r1', createdAt: '2026-09-10T10:00:04Z',
        metadata: { turnPhase: 'final', turnStatus: 'completed', startedAt: '2026-09-10T10:00:00Z', durationMS: 260 },
      },
    ])

    expect(messages).toHaveLength(2)
    expect(messages[0]).toMatchObject({ role: 'user', createdAt: '2026-09-10T10:00:00Z' })
    expect(messages[1]).toMatchObject({
      role: 'assistant', content: 'The issue is fixed.', runID: 'r1', createdAt: '2026-09-10T10:00:04Z',
      progress: { status: 'completed', startedAt: '2026-09-10T10:00:00Z', durationMS: 260 },
    })
    expect(messages[1].progress?.trace.map(block => block.kind)).toEqual(['commentary', 'tool'])
    expect(messages[1].progress?.trace[0]).toMatchObject({ content: 'I will inspect the repository.' })
    expect(messages[1].progress?.trace[1]).toMatchObject({ kind: 'tool', tool: { name: 'repo_search', durationMS: 40 } })
  })

  it('leaves unknown status content visible without claiming success', () => {
    const [message] = rebuildTranscript([{
      id: 'a1', role: 'assistant', content: 'Future state output', runID: 'r1',
      metadata: { turnPhase: 'final', turnStatus: 'future_status' },
    }])
    expect(message.content).toBe('Future state output')
    expect(message.progress).toBeUndefined()
  })

  it('preserves partial output and active timing for a failed terminal marker', () => {
    const [message] = rebuildTranscript([{
      id: 'a1', role: 'assistant', content: 'partial output', runID: 'r1', createdAt: '2026-09-10T10:00:01Z',
      metadata: { turnPhase: 'terminal', turnStatus: 'failed', startedAt: '2026-09-10T10:00:00Z', durationMS: 900, turnError: 'upstream disconnected' },
    }])
    expect(message).toMatchObject({ content: 'partial output', error: 'upstream disconnected' })
    expect(message.progress).toMatchObject({ status: 'failed', durationMS: 900 })
  })

  it('folds repeated approval resumes into one ordered turn', () => {
    const messages = rebuildTranscript([
      { id: 'u1', role: 'user', content: 'Make the changes', runID: 'r1' },
      {
        id: 'a1', role: 'assistant', content: 'I found the first change.', runID: 'r1',
        metadata: { turnPhase: 'commentary', turnStatus: 'running', startedAt: '2026-09-10T10:00:00Z', segmentDurationMS: 80 },
      },
      {
        id: 'a2', role: 'assistant', content: '', runID: 'r1',
        metadata: { turnPhase: 'terminal', turnStatus: 'waiting', startedAt: '2026-09-10T10:00:00Z', durationMS: 100 },
      },
      {
        id: 't1', role: 'tool', content: 'first result', runID: 'r1',
        metadata: { tool: 'repo_edit', args: '{"file":"one"}', durationMS: 20 },
      },
      {
        id: 'a3', role: 'assistant', content: 'I found the second change.', runID: 'r1',
        metadata: { turnPhase: 'commentary', turnStatus: 'running', startedAt: '2026-09-10T10:00:00Z', segmentDurationMS: 60 },
      },
      {
        id: 'a4', role: 'assistant', content: '', runID: 'r1',
        metadata: { turnPhase: 'terminal', turnStatus: 'waiting', startedAt: '2026-09-10T10:00:00Z', durationMS: 180 },
      },
      {
        id: 't2', role: 'tool', content: 'second result', runID: 'r1',
        metadata: { tool: 'repo_edit', args: '{"file":"two"}', durationMS: 30 },
      },
      {
        id: 'a5', role: 'assistant', content: 'All changes are complete.', runID: 'r1',
        metadata: { turnPhase: 'final', turnStatus: 'completed', startedAt: '2026-09-10T10:00:00Z', durationMS: 270 },
      },
    ])

    expect(messages).toHaveLength(2)
    expect(messages[1]).toMatchObject({
      role: 'assistant', content: 'All changes are complete.', runID: 'r1',
      progress: { status: 'completed', startedAt: '2026-09-10T10:00:00Z', durationMS: 270 },
    })
    expect(messages[1].progress?.trace.map(block => block.kind)).toEqual([
      'commentary', 'tool', 'commentary', 'tool',
    ])
    expect(messages[1].progress?.trace.map(block => block.kind === 'commentary' ? block.content : block.tool.name)).toEqual([
      'I found the first change.', 'repo_edit', 'I found the second change.', 'repo_edit',
    ])
    expect(messages[1].tools.map(tool => tool.result)).toEqual(['first result', 'second result'])
  })

  it('keeps measured work duration when a live run summary carries wall time', async () => {
    const startedAt = '2026-09-10T10:00:00Z'
    const api = stubApi({
      listSessions: () => Promise.resolve([session('s1')]),
      listMessages: () => Promise.resolve([{
        id: 'a1', role: 'assistant', content: 'Still working', runID: 'r1',
        metadata: { turnPhase: 'terminal', turnStatus: 'running', startedAt, durationMS: 1250 },
      }]),
      listRuns: vi.fn().mockResolvedValue({
        items: [{
          id: 'r1', agent: 'scout', sessionID: 's1', trigger: 'chat', class: 'interactive', phase: 'Running',
          inputTokens: 0, outputTokens: 0, usdMicros: 0, createdAt: startedAt, startedAt,
          // This represents wall time observed by the run list, including a
          // long approval pause. The turn metadata's 1.25s is active work.
          durationMS: 60_000,
        }],
        nextCursor: '',
      }),
    })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    await settleVue(8, 1)

    const progress = view.element.querySelector('.k-ai-turn-progress')
    expect(progress?.getAttribute('data-status')).toBe('running')
    expect(text(progress?.querySelector('.k-ai-turn-progress__label'))).toBe('Working for 1s')
  })
})

describe('AgentChat loading and draft behavior', () => {
  async function mountChat(chatStream: unknown, overrides: Record<string, unknown> = {}) {
    const api = stubApi({ chatStream, listRuns: () => Promise.resolve({ items: [] }), ...overrides })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    store.agents.loaded = true
    const view = await mountVue(AgentChat, { store, api, name: 'scout' })
    mounted.push(view)
    return { api, store, view }
  }

  it('shows a loading state before the first transcript snapshot', async () => {
    const history = deferred<TranscriptMessage[]>()
    const { view } = await mountChat(async function* () {}, {
      listSessions: () => Promise.resolve([session('s1')]),
      listMessages: () => history.promise,
    })
    await settleVue(3, 1)

    expect(text(view.element)).toContain('Loading conversation')
    expect(text(view.element)).not.toContain('No messages yet')

    history.resolve([{ id: 'u1', role: 'user', content: 'Loaded' }])
    await settleVue(5, 1)
    expect(text(view.element)).toContain('Loaded')
    expect(text(view.element)).not.toContain('Loading conversation')
  })

  it('keeps Enter editable while a run streams', async () => {
    const gate = deferred<void>()
    const chatStream = vi.fn(async function* (_agent: string, _message: string, _session: string) {
      yield { event: 'start', data: { runID: 'r1', sessionID: 's1' } } as SSEEvent
      await gate.promise
      yield { event: 'done', data: { runID: 'r1', content: 'done' } } as SSEEvent
    })
    const { view } = await mountChat(chatStream)
    await settleVue(3, 1)

    const textarea = view.element.querySelector<HTMLTextAreaElement>('.agents-composer-input')!
    textarea.value = 'first question'
    textarea.dispatchEvent(new Event('input', { bubbles: true }))
    view.element.querySelector<HTMLFormElement>('.agents-composer')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue(4, 1)
    expect(chatStream).toHaveBeenCalledTimes(1)

    textarea.value = 'draft while working'
    textarea.dispatchEvent(new Event('input', { bubbles: true }))
    await settleVue(2, 1)
    const enter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    textarea.dispatchEvent(enter)
    await settleVue(2, 1)
    expect(enter.defaultPrevented).toBe(false)
    expect(textarea.value).toBe('draft while working')
    expect(text(view.element.querySelector('#agents-composer-help'))).toContain('Draft your next message while the agent works.')

    gate.resolve()
    await settleVue(5, 1)
  })

  it('uses server-owned timestamps for live user and assistant rows', async () => {
    const startedAt = '2026-09-10T10:00:00.123Z'
    const finishedAt = '2026-09-10T10:00:04.567Z'
    const chatStream = vi.fn(async function* (_agent: string, _message: string, _session: string) {
      yield { event: 'start', data: { runID: 'r1', sessionID: 's1' } } as SSEEvent
      yield { event: 'run_started', data: { runID: 'r1', sessionID: 's1', status: 'running', startedAt } } as SSEEvent
      yield { event: 'delta', data: { text: 'answer' } } as SSEEvent
      yield {
        event: 'done',
        data: { runID: 'r1', content: 'answer', status: 'completed', startedAt, finishedAt, durationMS: 1250 },
      } as SSEEvent
    })
    const { view } = await mountChat(chatStream)
    await settleVue(3, 1)

    const textarea = view.element.querySelector<HTMLTextAreaElement>('.agents-composer-input')!
    textarea.value = 'question'
    textarea.dispatchEvent(new Event('input', { bubbles: true }))
    view.element.querySelector<HTMLFormElement>('.agents-composer')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settleVue(8, 1)

    const messages = view.element.querySelectorAll('.agents-msg')
    expect(messages).toHaveLength(2)
    expect(messages[0].querySelector('time')?.getAttribute('datetime')).toBe(startedAt)
    expect(messages[1].querySelector('time')?.getAttribute('datetime')).toBe(finishedAt)
    expect(text(messages[1])).toContain('answer')
    expect(text(messages[1].querySelector('.k-ai-turn-progress__label'))).toBe('Worked for 1s')
  })
})
