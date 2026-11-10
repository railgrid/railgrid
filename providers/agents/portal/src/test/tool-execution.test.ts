import { describe, expect, it, vi } from 'vitest'
import AIExecutionDetails from '../agentkit/AIExecutionDetails.vue'
import AgentToolDetails from '../components/AgentToolDetails.vue'
import { toolCommand, toolCommandTitle } from '../toolExecution'
import ChatMessage from '../views/ChatMessage.vue'
import { rebuildTranscript } from '../vue/chat'
import { mountVue, settleVue, text } from './vue-helper'

describe('command receipts', () => {
  it('recognizes harness and structured shell commands without reclassifying foreign tools', () => {
    expect(toolCommandTitle({ id: '1', name: 'commandExecution', args: "/bin/zsh -lc 'go test ./...'", pending: false })).toBe('Ran go test ./...')
    expect(toolCommand({ id: '1', name: 'Bash', pending: false, args: '{"command":"pwd"}' })).toBe('pwd')
    expect(toolCommand({ id: '1', name: 'exec_command', pending: false, args: '{"cmd":"pwd"}' })).toBe('pwd')
    expect(toolCommand({ id: '1', name: 'memory_search', pending: false, args: '{"command":"pwd"}' })).toBeUndefined()
    expect(toolCommand({ id: '1', name: 'shell', pending: false, args: '{"command":[]}' })).toBeUndefined()
  })

  it('restores exit receipts and unknown outcomes from transcript metadata', async () => {
    const [message] = rebuildTranscript([
      { id: 'a', role: 'assistant', content: 'Checked the setup.', runID: 'run' },
      { id: 'b', role: 'tool', content: 'command not found', runID: 'run', metadata: { tool: 'commandExecution', args: 'vitest', error: true, exitCode: 127 } },
      { id: 'c', role: 'tool', content: '', runID: 'run', metadata: { tool: 'commandExecution', args: 'deploy', error: true, outcomeUnknown: true } },
    ])
    const view = await mountVue(ChatMessage, { message })
    expect(text(view.element.querySelector('.k-ai-transcript-activity__summary'))).toContain('1 nonzero exit · 1 to review')
    const rows = view.element.querySelectorAll('.k-ai-action-row')
    expect(text(rows[0].querySelector('.k-ai-action-row__outcome'))).toBe('Completed · exit 127')
    expect(rows[0].classList.contains('k-ai-action-row--attention')).toBe(false)
    expect(rows[1].classList.contains('k-ai-action-row--attention')).toBe(true)
    expect(text(rows[1].querySelector('.k-ai-action-row__outcome'))).toBe('Outcome unknown')
    rows[1].querySelector<HTMLButtonElement>('.k-ai-action-row__toggle')!.click()
    await settleVue()
    expect(text(rows[1])).toContain('Check what the tool did before retrying.')
    view.unmount()
  })

  it('keeps the diagnostic from a legacy boolean-error result', async () => {
    const tool = { id: '1', name: 'lookup', result: 'upstream timed out', error: true, pending: false }
    const view = await mountVue(AgentToolDetails, { tool })
    expect(text(view.element.querySelector('.k-ai-execution-details__output'))).toBe('upstream timed out')
    expect(text(view.element.querySelector('.k-ai-execution-details__status'))).toBe('Failed')
    view.unmount()
  })

  it('shows a silent nonzero exit without inventing error output', async () => {
    const tool = { id: '1', name: 'commandExecution', args: 'false', exitCode: 1, error: 'Tool failed', pending: false }
    const view = await mountVue(AgentToolDetails, { tool })
    expect(text(view.element.querySelector('.k-ai-execution-details__status--muted'))).toBe('Completed · exit 1')
    expect(text(view.element)).toContain('No output')
    expect(text(view.element)).not.toContain('Tool failed')
    view.unmount()
  })

  it('redacts structured shell commands and keeps the recorded formatting of successful output', async () => {
    const command = 'API_KEY=fake-command-key example'
    const output = '{ "id": 9007199254740993, "note": "Bearer fake-output-key" }\n'
    const tool = { id: '1', name: 'exec_command', args: JSON.stringify({ cmd: command }), result: output, pending: false }
    expect(toolCommandTitle(tool)).toBe('Ran API_KEY=[redacted] example')
    const view = await mountVue(AgentToolDetails, { tool })
    expect(text(view.element.querySelector('.k-ai-execution-details__command'))).toBe('$ API_KEY=[redacted] example')
    expect(view.element.querySelector('.k-ai-execution-details__output')?.textContent).toBe(
      '{ "id": 9007199254740993, "note": "Bearer [redacted]" }\n',
    )
    view.unmount()
  })

  it('shows compact shell output without redundant metadata and copies the complete receipt', async () => {
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
    const output = Array.from({ length: 24 }, (_, i) => `line ${i}  <script>literal</script>`).join('\n')
    const view = await mountVue(AgentToolDetails, { tool: { id: '1', name: 'commandExecution', args: "/bin/zsh -lc 'pwd'", result: output, pending: false, durationMS: 3900 } })
    const el = view.element
    expect(text(el)).toContain('Shell')
    expect(text(el)).not.toContain('Arguments')
    expect(text(el.querySelector('.k-ai-execution-details__command'))).toBe('$ pwd')
    expect(el.querySelector('pre')?.textContent).not.toContain('line 23')
    expect(el.querySelector('script')).toBeNull()
    const expand = [...el.querySelectorAll('button')].find(b => b.textContent === 'Show full output')!
    expand.click()
    await settleVue()
    expect(el.querySelector('pre')?.textContent).toContain('line 23')
    expect(expand.getAttribute('aria-expanded')).toBe('true')
    el.querySelector<HTMLButtonElement>('[aria-label="Copy command and output"]')!.click()
    await settleVue()
    expect(writeText).toHaveBeenCalledWith(`pwd\n\n${output}`)
    const wrap = el.querySelector<HTMLButtonElement>('[aria-pressed]')!
    wrap.click()
    await settleVue()
    expect(el.querySelector('pre')?.classList.contains('is-wrapped')).toBe(true)
    expect(el.querySelector('details')).toBeNull()
    expect(text(el)).not.toContain('Execution details')
    view.unmount()
  })

  it.each(['command', 'input'] as const)('preserves expansion across equivalent %s execution refreshes and resets for a different execution', async source => {
    vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
    const output = Array.from({ length: 13 }, (_, index) => `line ${index + 1}`)
    const execution = { [source]: 'node verify', output, status: 'succeeded' }
    const view = await mountVue(AIExecutionDetails, { execution })
    const el = view.element
    const button = (label: string) => [...el.querySelectorAll('button')].find(item => item.textContent === label)!
    expect(el.querySelector('pre.k-ai-execution-details__output')?.textContent).not.toContain('line 13')
    button('Show full output').click()
    el.querySelector<HTMLButtonElement>('[aria-label="Copy command and output"]')!.click()
    await settleVue()

    await view.setProps({ execution: { ...execution, output: [...output] } })
    expect(el.querySelector('pre.k-ai-execution-details__output')?.textContent).toContain('line 13')
    expect(button('Show less output').getAttribute('aria-expanded')).toBe('true')
    expect(text(el.querySelector('[role="status"]'))).toBe('Copied')

    await view.setProps({ execution: { ...execution, output: [...output, 'line 14'], status: 'running', durationMs: 100 } })
    expect(el.querySelector('pre.k-ai-execution-details__output')?.textContent).toContain('line 14')
    expect(button('Show less output').getAttribute('aria-expanded')).toBe('true')

    await view.setProps({ execution: { ...execution, [source]: 'node other' } })
    expect(el.querySelector('pre.k-ai-execution-details__output')?.textContent).not.toContain('line 13')
    expect(button('Show full output').getAttribute('aria-expanded')).toBe('false')
    expect(text(el.querySelector('[role="status"]'))).toBe('')
    view.unmount()
  })

  it('preserves unknown and failed outcomes, server truncation and copy failure', async () => {
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'))
    const view = await mountVue(AIExecutionDetails, { execution: { command: 'false', status: 'future-status', exitCode: 0, output: ['bounded'], outputTruncated: true } })
    expect(text(view.element)).toContain('Status unavailable')
    expect(text(view.element)).toContain('Output truncated')
    expect(text(view.element)).not.toContain('Success')
    view.element.querySelector<HTMLButtonElement>('[aria-label="Copy command and output"]')!.click()
    await settleVue()
    expect(text(view.element.querySelector('[role="status"]'))).toBe('Could not copy')
    await view.setProps({ execution: { command: 'false', status: 'succeeded', exitCode: 1, output: ['failed'] } })
    expect(text(view.element)).toContain('Failed · exit 1')
    expect(text(view.element)).not.toContain('Success')
    view.unmount()
  })
})
