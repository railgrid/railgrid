import { afterEach, describe, expect, it } from 'vitest'
import ChatMessageView from '../views/ChatMessage.vue'
import type { ChatMessage } from '../types'
import { mountVue, settleVue, type MountedVue } from './vue-helper'
const views: MountedVue[] = []
afterEach(() => { while (views.length) views.pop()?.unmount() })
describe('tool activity follows the run lifecycle', () => {
  it.each([false, true])('matches App Studio progress presentation with details=%s and preserves run metadata', async (withDetails) => {
    const message: ChatMessage = {
      id: 'turn', role: 'assistant', content: 'The answer.', tools: [], runID: 'run-1',
      usage: { inputTokens: 100, outputTokens: 25, usdMicros: 1000 },
      progress: { status: 'completed', durationMS: 4000, trace: withDetails
        ? [{ kind: 'commentary', id: 'step', content: 'Checking the result.' }] : [] },
    }
    const view = await mountVue(ChatMessageView, { message }); views.push(view)
    const progress = view.element.querySelector('.k-ai-turn-progress--divided')!
    expect(progress.textContent).toContain('Worked for 4s')
    expect(view.element.querySelector('.agents-body')?.textContent?.trim()).toBe(withDetails ? 'Checking the result.' : 'The answer.')
    const trigger = progress.querySelector<HTMLButtonElement>('.k-ai-turn-progress__trigger')
    if (withDetails) {
      expect(trigger!.getAttribute('aria-expanded')).toBe('false')
      trigger!.click()
      await settleVue()
      expect(trigger!.getAttribute('aria-expanded')).toBe('true')
      const trace = view.element.querySelector('.k-ai-turn-progress__details')!
      expect(trace.closest('.k-ai-turn-progress')).not.toBeNull()
      expect(trace.textContent).toContain('Checking the result.')
      await view.setProps({ message: { ...message } })
      expect(trigger!.getAttribute('aria-expanded')).toBe('true')
    } else expect(trigger).toBeNull()
    expect(view.element.querySelector('.agents-message-run-link')?.textContent).toBe('View run')
    expect(view.element.querySelector('.agents-turn-usage')?.textContent).toContain('100 in')
    expect(view.element.textContent).toContain('The answer.')
  })

  it.each([
    ['waiting', 'Waiting for approval'], ['aborted', 'Cancelled'],
    ['failed', 'Interrupted'], ['interrupted', 'Interrupted'], ['completed', 'No result recorded'],
  ] as const)('does not keep unfinished tools spinning after %s', async (status, label) => {
    const tool = { id: 't1', name: 'describe_model', args: '{}', pending: true }
    const message: ChatMessage = { id: 'm1', role: 'assistant', content: '', tools: [tool], progress: { status, trace: [{ kind: 'tool', id: 't1', tool }] } }
    const view = await mountVue(ChatMessageView, { message }); views.push(view)
    // Transcript activity stays visible and never turns an unfinished call
    // into a successful result or a running spinner after terminal settlement.
    await view.setProps({ message: { ...message } })
    expect(view.element.textContent).toContain(label)
    expect(view.element.querySelector('.k-ai-action-row--busy')).toBeNull()
    expect(view.element.textContent).not.toContain('1 running')
  })
})
