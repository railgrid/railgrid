import { defineComponent, h } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import AITimestamp from '../agentkit/AITimestamp.vue'
import ChatMessage from '../views/ChatMessage.vue'
import { formatAIWorkedDuration } from '../agentkit/conversation'
import { formatFullTime, formatRelativeTime } from '../agentkit/timestamp'
import type { ChatMessage as ChatMessageView } from '../types'
import { runFailurePresentation, sanitizeTechnicalDiagnostic, type FailureRecoveryTarget } from '../failure-presentation'
import { mountVue, settleVue, text } from './vue-helper'

type TimestampMessage = ChatMessageView & { createdAt?: string }

function message(overrides: Partial<TimestampMessage> = {}): TimestampMessage {
  return {
    id: 'message-1',
    role: 'assistant',
    content: '',
    tools: [],
    ...overrides,
  }
}

describe('AgentKit timestamp and chat presentation', () => {
  it('does not blame the model for an unidentified tool authentication or request failure', () => {
    for (const diagnostic of ['Tool credential expired', 'HTTP connector: 400 Bad Request']) {
      expect(runFailurePresentation('failed', diagnostic).recovery).toBeUndefined()
    }
    expect(runFailurePresentation('failed', 'OpenAI API key is invalid').recovery?.target).toBe('model-connections')
  })
  it('omits missing or invalid timestamps', async () => {
    const missing = await mountVue(AITimestamp, { value: '' })
    expect(missing.element.querySelector('.k-ai-timestamp')).toBeNull()

    const nullValue = await mountVue(AITimestamp, { value: null })
    expect(nullValue.element.querySelector('.k-ai-timestamp')).toBeNull()

    const whitespace = await mountVue(AITimestamp, { value: '   ' })
    expect(whitespace.element.querySelector('.k-ai-timestamp')).toBeNull()

    const invalid = await mountVue(AITimestamp, { value: 'not-a-date' })
    expect(invalid.element.querySelector('.k-ai-timestamp')).toBeNull()
  })

  it('keeps the semantic date accessible and expands relative time on click', async () => {
    const value = new Date(Date.now() - 10_000).toISOString()
    const view = await mountVue(AITimestamp, { value })
    const time = view.element.querySelector<HTMLTimeElement>('time')!
    const button = view.element.querySelector<HTMLButtonElement>('button')!

    expect(time.getAttribute('datetime')).toBe(value)
    expect(button.type).toBe('button')
    expect(text(time)).toBe(formatRelativeTime(value, 'always'))
    expect(button.getAttribute('aria-label')).toBe(formatFullTime(value))
    expect(button.getAttribute('title')).toBe(formatFullTime(value))
    expect(view.element.querySelector('[role="tooltip"]')).not.toBeNull()

    button.focus()
    expect(document.activeElement).toBe(button)
    button.click()
    await settleVue()
    expect(text(time)).toBe(formatFullTime(value))
    expect(view.element.querySelector('[role="tooltip"]')).toBeNull()

    button.click()
    await settleVue()
    expect(text(time)).toBe(formatRelativeTime(value, 'always'))
  })

  it('keeps relative-time boundaries deterministic around the just-now window', () => {
    const now = Date.parse('2026-09-10T00:00:00.000Z')
    const timestamp = (seconds: number) => new Date(now + seconds * 1000).toISOString()

    expect(formatRelativeTime(timestamp(-44), 'always', now)).toBe('just now')
    expect(formatRelativeTime(timestamp(-45), 'always', now)).toBe('45 seconds ago')
    expect(formatRelativeTime(timestamp(44), 'always', now)).toBe('just now')
    expect(formatRelativeTime(timestamp(45), 'always', now)).toBe('in 45 seconds')
  })

  it('updates the relative label from the shared wall clock while idle', async () => {
    vi.useFakeTimers()
    try {
      const now = Date.parse('2026-09-10T00:00:00.000Z')
      vi.setSystemTime(now)
      const value = new Date(now - 44_000).toISOString()
      const view = await mountVue(AITimestamp, { value })
      const time = view.element.querySelector<HTMLTimeElement>('time')!

      expect(text(time)).toBe('just now')

      await vi.advanceTimersByTimeAsync(2_000)
      await settleVue()
      expect(text(time)).toBe('46 seconds ago')

      view.unmount()
    } finally {
      vi.useRealTimers()
    }
  })

  it('shares one interval, stops after the last unmount, and refreshes on remount', async () => {
    vi.useFakeTimers()
    try {
      const now = Date.parse('2026-09-10T00:00:00.000Z')
      vi.setSystemTime(now)
      const value = new Date(now - 40_000).toISOString()
      const first = await mountVue(AITimestamp, { value })
      const second = await mountVue(AITimestamp, { value })

      expect(vi.getTimerCount()).toBe(1)
      first.unmount()
      expect(vi.getTimerCount()).toBe(1)
      second.unmount()
      expect(vi.getTimerCount()).toBe(0)

      vi.setSystemTime(now + 10_000)
      const remounted = await mountVue(AITimestamp, { value })
      expect(text(remounted.element.querySelector('time'))).toBe('50 seconds ago')
      expect(vi.getTimerCount()).toBe(1)
      remounted.unmount()
      expect(vi.getTimerCount()).toBe(0)
    } finally {
      vi.useRealTimers()
    }
  })

  it('uses truthful progress states without an empty output frame', async () => {
    const running = await mountVue(ChatMessage, {
      message: message({ progress: { status: 'running', trace: [] } }),
    })
    expect(running.element.querySelector('.k-ai-turn-progress')?.getAttribute('data-status')).toBe('running')
    expect(text(running.element.querySelector('.k-ai-turn-progress'))).toBe('Working')
    expect(running.element.querySelector('.agents-thinking')).toBeNull()
    expect(running.element.querySelector('.agents-caret')).toBeNull()
    expect(running.element.querySelector('.k-ai-message__content')).toBeNull()

    const waiting = await mountVue(ChatMessage, {
      message: message({
        progress: { status: 'waiting', trace: [] },
        approval: { runID: 'run-1', inboxID: 'inbox-1', tool: 'search', args: '{}' },
      }),
    })
    expect(waiting.element.querySelector('.k-ai-turn-progress')?.getAttribute('data-status')).toBe('waiting')
    expect(text(waiting.element.querySelector('.k-ai-turn-progress'))).toBe('Waiting')

    const tool = { id: 'tool-1', name: 'search', result: 'ok', pending: false }
    const completed = await mountVue(ChatMessage, {
      message: message({
        content: 'Answer',
        tools: [tool],
        progress: {
          status: 'completed',
          durationMS: 3400,
          trace: [
            { id: 'commentary-1', kind: 'commentary', content: 'Checking the request.' },
            { id: 'tool-1', kind: 'tool', tool },
            { id: 'commentary-2', kind: 'commentary', content: 'The answer is ready.' },
          ],
        },
      }),
    })
    expect(completed.element.querySelector('.k-ai-turn-progress')?.getAttribute('data-status')).toBe('completed')
    expect(formatAIWorkedDuration(3400)).toBe('3s')
    expect(text(completed.element.querySelector('.k-ai-turn-progress'))).toContain('Worked for 3s')
    expect(text(completed.element.querySelector('.k-ai-message__content'))).toContain('Answer')

    const progressTrigger = completed.element.querySelector<HTMLButtonElement>('.k-ai-turn-progress__trigger')!
    expect(progressTrigger.getAttribute('aria-expanded')).toBe('false')
    progressTrigger.click()
    await settleVue()
    expect(progressTrigger.getAttribute('aria-expanded')).toBe('true')
    const details = completed.element.querySelector<HTMLElement>('.k-ai-turn-progress__details')!
    const detailsText = text(details)
    expect(detailsText.indexOf('Checking the request.')).toBeLessThan(detailsText.indexOf('Search'))
    expect(detailsText.indexOf('Search')).toBeLessThan(detailsText.indexOf('The answer is ready.'))

    const progressActivity = details.querySelector<HTMLElement>('.k-ai-activity')!
    const activityTrigger = progressActivity.querySelector<HTMLButtonElement>('.k-ai-activity__trigger')!
    activityTrigger.click()
    await settleVue()
    const progressRow = progressActivity.querySelector<HTMLElement>('.k-ai-action-row')!
    expect(progressRow.querySelector('.k-ai-activity-feed__row-icon')).not.toBeNull()
    progressRow.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')!.click()
    await settleVue()
    const progressToolDetails = progressRow.querySelector<HTMLElement>('.k-ai-execution-details')!
    expect(text(progressToolDetails)).toContain('Tool')
    expect(text(progressToolDetails)).toContain('search')
    expect(text(progressToolDetails)).toContain('ok')

    const fallbackTool = {
      id: 'memory-1',
      name: 'memory_list',
      args: '{"query":"<unsafe>"}',
      result: '<raw>&output',
      pending: false,
    }
    const inferred = await mountVue(ChatMessage, { message: message({ tools: [fallbackTool] }) })
    expect(inferred.element.querySelector('.k-ai-turn-progress')).toBeNull()
    const unclassifiedActivity = inferred.element.querySelector('.k-ai-activity')
    expect(unclassifiedActivity).not.toBeNull()
    expect(text(unclassifiedActivity?.querySelector('.k-ai-activity__trigger'))).toContain('1 action')
    expect(text(inferred.element)).not.toContain('Worked')

    const fallbackRow = inferred.element.querySelector<HTMLElement>('.k-ai-action-row')!
    expect(text(fallbackRow.querySelector('.k-ai-action-row__title'))).toBe('Memory list')
    expect(fallbackRow.querySelector('.k-ai-activity-feed__row-icon')).not.toBeNull()
    unclassifiedActivity!.querySelector<HTMLButtonElement>('.k-ai-activity__trigger')!.click()
    await settleVue()
    fallbackRow.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')!.click()
    await settleVue()
    const fallbackDetails = fallbackRow.querySelector<HTMLElement>('.k-ai-execution-details')!
    expect(text(fallbackDetails)).toContain('Tool')
    expect(text(fallbackDetails)).toContain('memory_list')
    expect(text(fallbackDetails)).toContain('Arguments')
    expect(text(fallbackDetails)).toContain('<unsafe>')
    expect(text(fallbackDetails)).toContain('<raw>&output')
    expect(fallbackDetails.querySelector('unsafe')).toBeNull()
    expect(fallbackDetails.querySelector('raw')).toBeNull()
    expect(text(fallbackDetails)).not.toContain('Shell')

    const errorTool = {
      id: 'memory-error',
      name: 'memory_lookup',
      error: '<failure> raw error',
      pending: false,
    }
    const errored = await mountVue(ChatMessage, { message: message({ tools: [errorTool] }) })
    const errorRow = errored.element.querySelector<HTMLElement>('.k-ai-action-row')!
    expect(errorRow.classList.contains('k-ai-action-row--error')).toBe(true)
    errorRow.querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')!.click()
    await settleVue()
    const errorDetails = errorRow.querySelector<HTMLElement>('.k-ai-execution-details')!
    expect(text(errorDetails)).toContain('Error')
    expect(text(errorDetails)).toContain('<failure> raw error')
    expect(errorDetails.querySelector('failure')).toBeNull()
  })

  it('retains sanitized markdown and code-copy attachment while streaming chrome stays shared', async () => {
    const view = await mountVue(ChatMessage, {
      message: message({ content: '**Answer**\n\n```ts\nconst value = 1\n```' }),
    })
    await settleVue()

    expect(view.element.querySelector('.agents-body strong')?.textContent).toBe('Answer')
    expect(view.element.querySelector('.agents-body pre code')?.textContent).toBe('const value = 1\n')
    expect(view.element.querySelector('.agents-code-copy')).not.toBeNull()
  })

  it.each([
    "{'api_key': 'opaque-private-value'}",
    String.raw`{"password":"prefix\"opaque-private-value"}`,
    String.raw`{'client_secret': 'prefix\'opaque-private-value'}`,
  ])('redacts quoted credential fields completely: %s', diagnostic => {
    expect(sanitizeTechnicalDiagnostic(diagnostic)).not.toContain('opaque-private-value')
    expect(sanitizeTechnicalDiagnostic(diagnostic)).toContain('[redacted]')
  })

  it('keeps a failed chat turn actionable while preserving secret-safe diagnostics and the run link', async () => {
    const bearer = 'super-secret-bearer-value'
    const apiKey = 'sk-proj-abcdefghijklmnopqrstuvwxyz0123456789'
    const urlPassword = 'private-url-password'
    const urlToken = 'query-secret-value'
    const diagnostic = [
      'HTTP 400 Bad Request: reasoning_effort is not supported by this model.',
      `Authorization: Bearer ${bearer}`,
      `{"api_key":"${apiKey}"}`,
      `Endpoint https://operator:${urlPassword}@model.example/v1?access_token=${urlToken}`,
    ].join('\n')
    const safe = sanitizeTechnicalDiagnostic(diagnostic)
    const recovery = runFailurePresentation('failed', safe)
    expect(recovery.recovery).toEqual({ label: 'Review model selection', target: 'agent-config' })
    expect(safe).toContain('reasoning_effort')
    expect(safe).toContain('model.example')
    expect(safe).not.toContain(bearer)
    expect(safe).not.toContain(apiKey)
    expect(safe).not.toContain(urlPassword)
    expect(safe).not.toContain(urlToken)

    const recoveredTo: FailureRecoveryTarget[] = []
    const viewedRuns: string[] = []
    const host = defineComponent({
      setup: () => () => h(ChatMessage, {
        message: message({
          id: 'failed-turn',
          runID: 'run-9',
          content: 'Partial answer',
          error: diagnostic,
          progress: { status: 'failed', trace: [] },
        }),
        onRecovery: (target: FailureRecoveryTarget) => recoveredTo.push(target),
        onViewRun: (runID: string) => viewedRuns.push(runID),
      }),
    })
    const view = await mountVue(host, {})
    const state = view.element.querySelector<HTMLElement>('.agents-run-state')!
    const progress = view.element.querySelector<HTMLElement>('.k-ai-turn-progress')!
    const partialOutput = view.element.querySelector<HTMLElement>('.agents-body')!
    const details = state.querySelector<HTMLDetailsElement>('details')!

    expect(state.getAttribute('role')).toBe('alert')
    expect(text(state.querySelector('h3'))).toBe('The model rejected a reasoning setting')
    expect(details.open).toBe(false)
    expect(Boolean(state.compareDocumentPosition(progress) & Node.DOCUMENT_POSITION_FOLLOWING)).toBe(true)
    expect(Boolean(state.compareDocumentPosition(partialOutput) & Node.DOCUMENT_POSITION_FOLLOWING)).toBe(true)

    details.querySelector('summary')!.click()
    await settleVue()
    const diagnosticText = text(details.querySelector('pre'))
    expect(diagnosticText).toContain('reasoning_effort')
    expect(diagnosticText).toContain('model.example')
    expect(diagnosticText).not.toContain(bearer)
    expect(diagnosticText).not.toContain(apiKey)
    expect(diagnosticText).not.toContain(urlPassword)
    expect(diagnosticText).not.toContain(urlToken)
    expect(text(partialOutput)).toBe('Partial answer')

    state.querySelector<HTMLButtonElement>('.agents-run-state__recovery')!.click()
    view.element.querySelector<HTMLButtonElement>('.agents-message-run-link')!.click()
    expect(recoveredTo).toEqual(['agent-config'])
    expect(viewedRuns).toEqual(['run-9'])
  })

  it('distinguishes canceled and recovered successful turns from failed runs', async () => {
    const canceled = await mountVue(ChatMessage, {
      message: message({ error: 'operator requested stop', progress: { status: 'aborted', trace: [] } }),
    })
    expect(canceled.element.querySelector('.agents-run-state')?.getAttribute('role')).toBe('status')
    expect(text(canceled.element.querySelector('.agents-run-state h3'))).toBe('Run canceled')
    expect(canceled.element.querySelector('.agents-run-state__recovery')).toBeNull()

    const completed = await mountVue(ChatMessage, {
      message: message({
        error: 'Chat failed: connection closed before transcript reload',
        progress: { status: 'completed', trace: [] },
      }),
    })
    expect(completed.element.querySelector('.agents-run-state')).toBeNull()

    const interrupted = await mountVue(ChatMessage, {
      message: message({ error: 'Chat failed: connection closed', progress: { status: 'interrupted', trace: [] } }),
    })
    expect(interrupted.element.querySelector('.agents-run-state')?.getAttribute('role')).toBe('status')
    expect(text(interrupted.element.querySelector('.agents-run-state h3'))).toBe('Chat connection interrupted')
  })
})
