import { afterEach, describe, expect, it, vi } from 'vitest'
import RunFailureNotice from '../components/RunFailureNotice.vue'
import TechnicalDetails from '../components/TechnicalDetails.vue'
import ChatMessage from '../views/ChatMessage.vue'
import { sanitizeTechnicalDiagnostic } from '../failure-presentation'
import { mountVue, settleVue, text } from './vue-helper'

const runnerError = `starting the harness turn: runner HTTP 404: ${JSON.stringify({
  kind: 'Status', apiVersion: 'v1', metadata: {}, status: 'Failure',
  message: 'services.edges.railgrid.ai "book-codex" not found', reason: 'NotFound',
  details: { name: 'book-codex', group: 'edges.railgrid.ai', kind: 'services' }, code: 404,
})}`

afterEach(() => { vi.unstubAllGlobals() })

describe('run failure recovery and diagnostics', () => {
  it.each([
    ['duration omitted', undefined],
    ['zero duration', 0],
  ] as const)('routes the missing runner recovery from a real chat turn and preserves the run link (%s)', async (_label, durationMS) => {
    const recovery = vi.fn()
    const viewRun = vi.fn()
    const progress = {
      status: 'failed' as const,
      trace: [],
      ...(durationMS === undefined ? {} : { durationMS }),
    }
    const view = await mountVue(ChatMessage, {
      message: { id: 'failed-start', role: 'assistant', content: '', tools: [], runID: 'run-start', error: runnerError, progress },
      onRecovery: recovery,
      onViewRun: viewRun,
    })
    const notice = view.element.querySelector('.k-ai-turn-progress')!
    expect(text(view.element.querySelector('[role="alert"]'))).toBe('Connection failed')
    const toggle = notice.querySelector<HTMLButtonElement>('.k-ai-turn-progress__trigger')!
    expect(text(toggle)).toBe('Connection failed')
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    const panel = notice.querySelector<HTMLElement>('.k-ai-turn-progress__details')!
    expect(panel.style.display).toBe('none')
    toggle.click()
    await settleVue()
    expect(panel.style.display).not.toBe('none')
    expect(text(panel.querySelector('.agents-turn-failure p'))).toBe('Runner “book-codex” wasn’t found.')
    notice.querySelector<HTMLButtonElement>('.agents-run-state__recovery')!.click()
    expect(view.element.querySelectorAll('.agents-message-run-link')).toHaveLength(1)
    expect(notice.querySelector('.agents-message-run-link')).toBeNull()
    view.element.querySelector<HTMLButtonElement>('.agents-message-footer .agents-message-run-link')!.click()
    expect(recovery).toHaveBeenCalledWith('agent-config')
    expect(viewRun).toHaveBeenCalledWith('run-start')
  })

  it.each([
    { status: 'failed', durationMS: 1200, trace: [] },
    { status: 'failed', trace: [{ id: 'step-1', kind: 'commentary', content: 'Inspected the rollout.' }] },
  ])('discloses inspectable activity after a failure', async progress => {
    const view = await mountVue(ChatMessage, {
      message: { id: 'failed-with-work', role: 'assistant', content: 'Partial answer', tools: [], error: 'Harness stopped', progress },
    })
    expect(view.element.querySelectorAll('.k-ai-turn-progress')).toHaveLength(1)
    view.element.querySelector<HTMLButtonElement>('.k-ai-turn-progress__trigger')!.click()
    await settleVue()
    expect(view.element.querySelector('.k-ai-turn-progress__details .agents-turn-failure')).not.toBeNull()
    expect(text(view.element.querySelector('.k-ai-turn-progress__details .agents-trace-commentary'))).toBe(
      progress.trace.length ? 'Inspected the rollout.' : '',
    )
    expect(text(view.element.querySelector('.agents-body:not(.agents-trace-commentary)'))).toBe('Partial answer')
  })

  it('keeps short diagnostics optional in run inspection as well as chat', async () => {
    const view = await mountVue(RunFailureNotice, { phase: 'failed', diagnostic: 'Short runner error' })
    const details = view.element.querySelector<HTMLDetailsElement>('details')!
    expect(details.open).toBe(false)
    expect(view.element.querySelector('.agents-run-state__diagnostic')).toBeNull()
    details.querySelector('summary')!.click()
    await settleVue()
    expect(text(details.querySelector('pre'))).toBe('Short runner error')
  })

  it('keeps a pre-dispatch failure, nested diagnostics, and footer in the shared turn layout', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const diagnostic = 'connecting to the runner before dispatch: runner HTTP 502: upstream unavailable'
    const view = await mountVue(ChatMessage, {
      message: { id: 'unclear', role: 'assistant', content: '', tools: [], runID: 'run-502', error: diagnostic, createdAt: '2026-11-09T18:00:00Z' },
    })
    const notice = view.element.querySelector('.k-ai-turn-progress')!
    const toggle = notice.querySelector<HTMLButtonElement>('.k-ai-turn-progress__trigger')!
    expect(text(toggle)).toBe('Connection failed')
    const footer = view.element.querySelector('.k-ai-conversation-turn__after')!
    expect(footer.querySelector('.agents-message-run-link')).not.toBeNull()
    expect(footer.querySelector('time')).not.toBeNull()
    expect(notice.compareDocumentPosition(footer) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const body = document.getElementById(toggle.getAttribute('aria-controls')!)!
    expect(body.style.display).toBe('none')
    toggle.click()
    await settleVue()
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(body.style.display).not.toBe('none')
    expect(text(body.querySelector('.agents-turn-failure p'))).toContain('before dispatch')
    const technical = body.querySelector('details')!
    expect(technical.open).toBe(false)
    technical.querySelector('summary')!.click()
    expect(technical.open).toBe(true)
    expect(text(technical.querySelector('pre'))).toBe(diagnostic)
    body.querySelector<HTMLButtonElement>('[aria-label="Copy details"]')!.click()
    await settleVue()
    expect(writeText).toHaveBeenCalledWith(diagnostic)
    expect(text(body.querySelector('[role="status"]'))).toBe('Technical details copied.')
    toggle.click()
    await settleVue()
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    await vi.waitFor(() => expect(body.style.display).toBe('none'))
  })

  it('copies only the sanitized diagnostic and clears feedback when it changes', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const diagnostic = 'runner failed Authorization: Bearer private-value-123456789'
    const view = await mountVue(TechnicalDetails, { diagnostic, compact: true, copyable: true })
    view.element.querySelector('summary')!.click()
    const button = view.element.querySelector<HTMLButtonElement>('button')!
    button.click()
    await settleVue()
    expect(writeText).toHaveBeenCalledWith(sanitizeTechnicalDiagnostic(diagnostic))
    expect(writeText.mock.calls[0]![0]).not.toContain('private-value-123456789')
    expect(button.getAttribute('aria-label')).toBe('Copy details')
    expect(button.getAttribute('data-k-tip')).toBe('Copied')
    expect(text(view.element.querySelector('[role="status"]'))).toBe('Technical details copied.')
    await view.setProps({ diagnostic: 'Another failure' })
    expect(button.getAttribute('aria-label')).toBe('Copy details')
    expect(button.getAttribute('data-k-tip')).toBe('Copy details')
    expect(text(view.element.querySelector('[role="status"]'))).toBe('')
  })

  it.each([undefined, { writeText: vi.fn().mockRejectedValue(new Error('denied')) }])('keeps diagnostics selectable when clipboard access is unavailable', async clipboard => {
    vi.stubGlobal('navigator', { clipboard })
    const view = await mountVue(TechnicalDetails, { diagnostic: 'Runner diagnostic', copyable: true, compact: true })
    view.element.querySelector('summary')!.click()
    view.element.querySelector<HTMLButtonElement>('button')!.click()
    await settleVue()
    expect(text(view.element.querySelector('[role="status"]'))).toBe('Could not copy details. Select the text below to copy it.')
    expect(text(view.element.querySelector('pre'))).toBe('Runner diagnostic')
    expect(view.element.querySelector<HTMLDetailsElement>('details')!.open).toBe(true)
  })

  it('shows the summary without a diagnostic block when the diagnostic is empty', async () => {
    const view = await mountVue(ChatMessage, {
      message: { id: 'failed-empty', role: 'assistant', content: '', tools: [], error: '  ' },
    })
    view.element.querySelector<HTMLButtonElement>('.k-ai-turn-progress__trigger')!.click()
    await settleVue()
    expect(view.element.querySelector('pre')).toBeNull()
    expect(text(view.element)).not.toContain('partial output')
  })
})
