import { describe, expect, it, vi } from 'vitest'
import AIExecutionDetails from '../agentkit/AIExecutionDetails.vue'
import AgentToolDetails from '../components/AgentToolDetails.vue'
import { toolCommand, toolCommandTitle } from '../toolExecution'
import { mountVue, settleVue, text } from './vue-helper'

describe('command receipts', () => {
  it('recognizes harness and structured shell commands without reclassifying foreign tools', () => {
    expect(toolCommandTitle({ id: '1', name: 'commandExecution', args: "/bin/zsh -lc 'go test ./...'", pending: false })).toBe('Ran go test ./...')
    expect(toolCommand({ id: '1', name: 'Bash', pending: false, args: '{"command":"pwd"}' })).toBe('pwd')
    expect(toolCommand({ id: '1', name: 'exec_command', pending: false, args: '{"cmd":"pwd"}' })).toBe('pwd')
    expect(toolCommand({ id: '1', name: 'memory_search', pending: false, args: '{"command":"pwd"}' })).toBeUndefined()
    expect(toolCommand({ id: '1', name: 'shell', pending: false, args: '{"command":[]}' })).toBeUndefined()
  })

  it('shows compact shell output, retains the exact invocation, and copies the complete receipt', async () => {
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
    expect(text(el.querySelector('details'))).toContain("/bin/zsh -lc 'pwd'")
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
