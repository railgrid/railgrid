import { afterEach, describe, expect, it } from 'vitest'
import ChatMessageView from '../views/ChatMessage.vue'
import type { ChatMessage } from '../types'
import { mountVue, type MountedVue } from './vue-helper'
const views: MountedVue[] = []
afterEach(() => { while (views.length) views.pop()?.unmount() })
describe('tool activity follows the run lifecycle', () => {
  it.each([
    ['waiting', 'Waiting for approval'], ['aborted', 'Cancelled'],
    ['failed', 'Interrupted'], ['interrupted', 'Interrupted'], ['completed', 'No result recorded'],
  ] as const)('does not keep unfinished tools spinning after %s', async (status, label) => {
    const tool = { id: 't1', name: 'describe_model', args: '{}', pending: true }
    const message: ChatMessage = { id: 'm1', role: 'assistant', content: '', tools: [tool], progress: { status, trace: [{ kind: 'tool', id: 't1', tool }] } }
    const view = await mountVue(ChatMessageView, { message }); views.push(view)
    // Terminal turns collapse their trace, but the activity projection is still
    // rendered and must never claim the unfinished call succeeded or is busy.
    const progress = view.element.querySelector<HTMLButtonElement>('.k-ai-turn-progress__toggle')
    if (status !== 'waiting') progress?.click()
    await view.setProps({ message: { ...message } })
    expect(view.element.textContent).toContain(label)
    expect(view.element.querySelector('.k-ai-activity-feed [aria-busy="true"]')).toBeNull()
    expect(view.element.textContent).not.toContain('1 running')
  })
})
