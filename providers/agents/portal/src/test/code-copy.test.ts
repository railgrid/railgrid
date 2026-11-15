import { afterEach, describe, expect, it, vi } from 'vitest'
import { attachCodeCopy } from '../vue/chat'

afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers(); document.body.innerHTML = '' })

function codeBlock() {
  const root = document.createElement('div')
  root.innerHTML = '<div class="agents-body"><pre><code>echo ready\n</code></pre></div>'
  document.body.appendChild(root)
  attachCodeCopy(root)
  attachCodeCopy(root)
  return { root, button: root.querySelector('button')!, feedback: root.querySelector('[role=status]')! }
}

describe('markdown code copying', () => {
  it('copies only code and clears its local success feedback', async () => {
    vi.useFakeTimers()
    const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
    const { root, button, feedback } = codeBlock()
    expect(root.querySelectorAll('button')).toHaveLength(1)
    expect(button.parentElement).toBe(root.querySelector('.agents-code-frame'))
    expect(button.closest('pre')).toBeNull()
    button.click()
    await Promise.resolve()
    expect(write).toHaveBeenCalledWith('echo ready\n')
    expect(feedback.textContent).toBe('Copied')
    vi.advanceTimersByTime(2000)
    expect(feedback.textContent).toBe('')
  })

  it('keeps actionable feedback when clipboard access fails and permits retry', async () => {
    const write = vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValueOnce(new Error('denied')).mockResolvedValue(undefined)
    const { button, feedback } = codeBlock()
    button.click()
    await Promise.resolve()
    expect(feedback.textContent).toContain('Select the code')
    button.click()
    await Promise.resolve()
    expect(write).toHaveBeenCalledTimes(2)
    expect(feedback.textContent).toBe('Copied')
  })

  it('reports an unavailable clipboard', async () => {
    vi.stubGlobal('navigator', {})
    const { button, feedback } = codeBlock()
    button.click()
    expect(feedback.textContent).toContain('Select the code')
    vi.unstubAllGlobals()
  })
})
