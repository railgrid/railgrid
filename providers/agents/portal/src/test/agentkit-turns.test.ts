import { createCommentVNode, defineComponent, h, ref } from 'vue'
import { describe, expect, it } from 'vitest'
import AIConversationTurn from '../agentkit/AIConversationTurn.vue'
import AIPlanDisclosure from '../agentkit/AIPlanDisclosure.vue'
import AITurnProgress from '../agentkit/AITurnProgress.vue'
import type { AIPlanView } from '../agentkit/conversation'
import { mountVue, settleVue, text } from './vue-helper'

const plan: AIPlanView = {
  steps: [
    { id: 'one', content: 'Inspect the request', status: 'completed' },
    { id: 'two', content: 'Apply the change', activeForm: 'Applying the change', status: 'in_progress' },
    { id: 'three', content: 'Run the checks', status: 'pending' },
  ],
}

describe('AgentKit conversation turn presentation', () => {
  it('renders caller-owned turn slots inside the neutral AIMessage frame', async () => {
    const view = await mountVue(defineComponent({
      setup: () => () => h(AIConversationTurn, {
        turnId: 'turn:one',
        role: 'assistant',
      }, {
        before: () => h('span', { class: 'before-slot' }, 'Before'),
        progress: () => h('span', { class: 'progress-slot' }, 'Progress'),
        trace: () => h('span', { class: 'trace-slot' }, 'Trace'),
        output: () => h('p', { class: 'output-slot' }, 'Answer'),
        interrupt: () => h('span', { class: 'interrupt-slot' }, 'Interrupt'),
        metadata: () => h('time', { class: 'metadata-slot' }, 'Now'),
        after: () => h('span', { class: 'after-slot' }, 'After'),
      }),
    }), {})

    const article = view.element.querySelector<HTMLElement>('article.k-ai-message')!
    expect(article.id).toBe('k-ai-conversation-turn-turn-one')
    expect(article.classList.contains('k-ai-conversation-turn')).toBe(true)
    expect(text(article.querySelector('.k-ai-message__before'))).toContain('BeforeProgressTrace')
    expect(text(article.querySelector('.k-ai-message__content'))).toBe('Answer')
    expect(text(article.querySelector('.k-ai-message__after'))).toContain('InterruptNowAfter')
  })

  it('omits empty frame regions when conditional slots have no content', async () => {
    const showExtras = ref(false)
    const view = await mountVue(defineComponent({
      setup: () => () => h(AIConversationTurn, {
        turnId: 'plain-user',
        role: 'user',
      }, {
        output: () => h('p', 'Message'),
        ...(showExtras.value ? {
          before: () => h('span', 'Before'),
          progress: () => h('span', 'Progress'),
          after: () => h('span', 'After'),
        } : {
          before: () => createCommentVNode('v-if'),
          progress: () => createCommentVNode('v-if'),
          after: () => createCommentVNode('v-if'),
        }),
      }),
    }), {})

    const article = view.element.querySelector<HTMLElement>('article.k-ai-message')!
    expect(article.querySelector('.k-ai-message__before')).toBeNull()
    expect(article.querySelector('.k-ai-message__after')).toBeNull()
    expect(article.querySelector('.k-ai-conversation-turn__progress')).toBeNull()
  })

  it('omits declared comment-only progress and implicit output slots', async () => {
    const view = await mountVue(defineComponent({
      setup: () => () => h(AIConversationTurn, {
        turnId: 'comment-only',
        role: 'user',
      }, {
        progress: () => createCommentVNode('v-if'),
        default: () => createCommentVNode('v-if'),
      }),
    }), {})

    const article = view.element.querySelector<HTMLElement>('article.k-ai-message')!
    expect(article.querySelector('.k-ai-message__before')).toBeNull()
    expect(article.querySelector('.k-ai-message__content')).toBeNull()
    expect(article.querySelector('.k-ai-message__after')).toBeNull()
  })

  it('keeps progress expansion controlled and exposes a live log only while running', async () => {
    const expanded = ref(true)
    const toggles: boolean[] = []
    const view = await mountVue(defineComponent({
      setup: () => () => h(AITurnProgress, {
        turnId: 'run/one',
        status: 'running',
        duration: null,
        expanded: expanded.value,
        onToggle: (next: boolean) => {
          toggles.push(next)
          expanded.value = next
        },
      }, {
        default: () => h('p', { class: 'trace-slot' }, 'Trace detail'),
      }),
    }), {})

    const trigger = view.element.querySelector<HTMLButtonElement>('button')!
    const details = view.element.querySelector<HTMLElement>('[role="log"]')!
    expect(text(trigger)).toBe('Working')
    expect(trigger.getAttribute('aria-expanded')).toBe('true')
    expect(trigger.getAttribute('aria-controls')).toBe(details.id)
    expect(details.getAttribute('aria-live')).toBe('polite')
    trigger.focus()
    expect(document.activeElement).toBe(trigger)

    trigger.click()
    await settleVue()
    expect(toggles).toEqual([false])
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
    expect(details.style.display).toBe('none')

    const terminal = await mountVue(AITurnProgress, {
      turnId: 'run/terminal',
      status: 'interrupted',
      duration: '2s',
      interrupted: true,
      expanded: true,
    })
    expect(text(terminal.element)).toContain('Worked for 2s')
    expect(text(terminal.element)).toContain('Interrupted')
    expect(terminal.element.querySelector('[role="log"]')).toBeNull()
    expect(terminal.element.querySelector('[aria-live]')).toBeNull()
    expect(terminal.element.querySelector('.k-ai-turn-progress__status')?.getAttribute('aria-label')).toBe('Worked for 2s. Interrupted')
  })

  it('omits the details affordance when a declared conditional slot is empty', async () => {
    const view = await mountVue(defineComponent({
      setup: () => () => h(AITurnProgress, {
        turnId: 'conditional-details',
        status: 'running',
        expanded: true,
      }, {
        details: () => createCommentVNode('v-if'),
      }),
    }), {})

    expect(view.element.querySelector('.k-ai-turn-progress__trigger')).toBeNull()
    expect(view.element.querySelector('.k-ai-turn-progress__details')).toBeNull()
    expect(view.element.querySelector('.k-ai-turn-progress__status')?.getAttribute('aria-label')).toBe('Working')
  })

  it('does not present waiting or pending work as active or timed', async () => {
    const pending = await mountVue(AITurnProgress, {
      turnId: 'run-pending',
      status: 'pending',
      duration: '1s',
    })
    expect(text(pending.element.querySelector('.k-ai-turn-progress'))).toBe('Pending')

    const waiting = await mountVue(AITurnProgress, {
      turnId: 'run-waiting',
      status: 'waiting',
      duration: '1s',
    })
    expect(text(waiting.element.querySelector('.k-ai-turn-progress'))).toBe('Waiting')
  })

  it('keeps the Studio plan count and supports keyboard-focusable disclosure on mobile', async () => {
    const view = await mountVue(AIPlanDisclosure, { plan, turnId: 'plan/one', mobile: true })
    const trigger = view.element.querySelector<HTMLButtonElement>('.k-ai-plan-disclosure__trigger')!
    const panel = view.element.querySelector<HTMLElement>('.k-ai-plan-disclosure__panel')!

    expect(text(trigger)).toContain('Plan')
    expect(text(trigger)).toContain('1 of 3 steps')
    expect(trigger.type).toBe('button')
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
    expect(trigger.getAttribute('aria-controls')).toBe(panel.id)
    trigger.focus()
    expect(document.activeElement).toBe(trigger)

    trigger.click()
    await settleVue()
    expect(trigger.getAttribute('aria-expanded')).toBe('true')
    expect(panel.style.display).not.toBe('none')
    expect(panel.querySelector('.k-ai-plan-steps--mobile')).not.toBeNull()
    expect(text(panel)).toContain('Completed: Inspect the request')
    expect(text(panel)).toContain('In progress: Apply the change')
    expect(text(panel)).toContain('Pending: Run the checks')
  })
})
