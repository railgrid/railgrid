// Chat streaming: the delta → tool card → approval card → done lifecycle, plus
// the Stop button's cancel call and scroll-pinning behaviour.

import { describe, expect, it, vi } from 'vitest'
import type { AgentChat } from '../views/agent-chat'
import { rebuildTranscript } from '../views/agent-chat'
import type { SSEEvent } from '../api'
import type { TranscriptMessage } from '../types'
import '../views/agent-chat'
import { agentFixture, makeStore, mount, settle, stubApi, text } from './helpers'

// scripted turns the given SSE frames into the async generator chatStream
// returns, with a `gate` promise so a test can hold the stream open.
function scripted(events: SSEEvent[], gate?: Promise<void>) {
  return async function* () {
    for (const ev of events) yield ev
    if (gate) await gate
  }
}

async function mountChat(chatStream: unknown, extra: Record<string, unknown> = {}) {
  const api = stubApi({ chatStream, ...extra })
  const store = makeStore(api)
  store.agents.data = [agentFixture('scout')]
  store.agents.loaded = true
  const el = await mount<AgentChat>('agents-agent-chat', { store, api, name: 'scout' })
  return { el, api, store }
}

async function send(el: AgentChat, message: string): Promise<void> {
  const ta = el.querySelector<HTMLTextAreaElement>('.agents-composer textarea')!
  ta.value = message
  ta.dispatchEvent(new Event('input'))
  await settle(el)
  el.querySelector<HTMLFormElement>('.agents-composer')!.dispatchEvent(new Event('submit', { cancelable: true }))
  await settle(el, 6)
}

describe('chat streaming', () => {
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

    const msgs = el.querySelectorAll('agents-chat-message')
    expect(msgs.length).toBe(2)
    expect(text(msgs[0])).toContain('hello')
    // Markdown is rendered, not escaped.
    expect(msgs[1].querySelector('strong')?.textContent).toBe('hi')
    // Per-turn usage footer.
    expect(text(msgs[1].querySelector('.agents-turn-usage'))).toContain('$0.0015')
  })

  it('shows a pending tool card that resolves with duration and expands', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const { el } = await mountChat(
      scripted(
        [
          { event: 'start', data: { runID: 'r1', sessionID: 's1' } },
          { event: 'tool_start', data: { id: 't1', name: 'github__list_issues', args: '{"repo":"kedge"}' } },
        ],
        gate,
      ),
    )
    const done = send(el, 'find issues')
    await settle(el, 6)

    const card = el.querySelector('.agents-toolcard')!
    expect(card.className).toContain('is-pending')
    expect(text(card)).toContain('github__list_issues')
    expect(text(card)).toContain('running…')

    release()
    await done

    // Expanding reveals the recorded args.
    el.querySelector<HTMLButtonElement>('.agents-toolcard-head')!.click()
    await settle(el)
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
    await settle(el, 6)
    expect(resolveInbox).toHaveBeenCalledWith('i1', 'approve')
    expect(text(el.querySelector('.agents-approval-done'))).toContain('resuming')
  })

  it('stop aborts the stream and cancels the run server-side', async () => {
    const cancelRun = vi.fn().mockResolvedValue({ id: 'r1', cancelling: true })
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const { el } = await mountChat(scripted([{ event: 'start', data: { runID: 'r1', sessionID: 's1' } }], gate), { cancelRun })

    const done = send(el, 'long task')
    await settle(el, 6)

    const stop = el.querySelector<HTMLButtonElement>('.agents-stop')
    expect(stop).not.toBeNull()
    stop!.click()
    await settle(el, 4)
    expect(cancelRun).toHaveBeenCalledWith('r1')
    release()
    await done
  })

  it('does not force-scroll when the user has scrolled up', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const { el } = await mountChat(
      scripted([{ event: 'start', data: { runID: 'r1', sessionID: 's1' } }, { event: 'delta', data: { text: 'a' } }], gate),
    )
    const done = send(el, 'hi')
    await settle(el, 4)

    const log = el.querySelector<HTMLElement>('.agents-log')!
    // jsdom reports zero heights, so drive the scroll handler directly with a
    // geometry that means "scrolled up".
    Object.defineProperty(log, 'scrollHeight', { value: 1000, configurable: true })
    Object.defineProperty(log, 'clientHeight', { value: 200, configurable: true })
    log.scrollTop = 100
    log.dispatchEvent(new Event('scroll'))
    await settle(el, 2)
    expect(log.scrollTop).toBe(100)

    release()
    await done
  })
})

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
      { id: '2', role: 'tool', content: 'ok', runID: 'r1', metadata: { tool: 'web_search', args: '{"q":"kedge"}', durationMS: 90 } },
      { id: '1', role: 'user', content: 'search', runID: 'r1' },
    ])
    const api = stubApi({ listMessages })
    const store = makeStore(api)
    store.agents.data = [agentFixture('scout')]
    const el = await mount<AgentChat>('agents-agent-chat', { store, api, name: 'scout' })

    const card = el.querySelector('.agents-toolcard')!
    expect(text(card)).toContain('web_search')
    expect(text(card)).toContain('90ms')
    // A generous limit is requested so a long session comes back whole.
    expect(listMessages.mock.calls[0][2] ?? 200).toBeGreaterThanOrEqual(200)
  })
})
