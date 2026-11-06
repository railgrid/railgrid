import { describe, expect, it, vi } from 'vitest'
import AICommandPalette from '../agentkit/AICommandPalette.vue'
import AIConversationComposer from '../agentkit/AIConversationComposer.vue'
import { mountVue, settleVue } from './vue-helper'

const skill = { id: 'workspace:review', name: 'Review', description: '', scope: 'workspace' }
const props = { open: true, commandQuery: '', skills: [skill], selectedSkillIDs: [], selectedResources: [] }

describe('palette keyboard dismissal and resource recovery', () => {
  it('dismisses without stealing focus when the user tabs from the filter', async () => {
    const close = vi.fn()
    await mountVue(AICommandPalette, { ...props, onClose: close })
    document.querySelector<HTMLButtonElement>('[role=option]')!.click()
    await settleVue(4)
    const other = document.createElement('button')
    document.body.append(other)
    other.focus()
    await settleVue(4)
    expect(close).toHaveBeenCalledWith(false)
    expect(document.activeElement).toBe(other)
    other.remove()
  })

  it('dismisses a slash popup when focus leaves its external editor', async () => {
    const view = await mountVue(AIConversationComposer, { modelValue: '', skills: [skill] })
    const editor = view.element.querySelector<HTMLElement>('.assistant-rich-composer')!
    editor.focus()
    editor.textContent = '/skill'
    const range = document.createRange()
    range.selectNodeContents(editor)
    range.collapse(false)
    window.getSelection()!.removeAllRanges()
    window.getSelection()!.addRange(range)
    editor.dispatchEvent(new Event('input', { bubbles: true }))
    await settleVue(4)
    expect(document.querySelector('.k-ai-command-palette-panel')).not.toBeNull()
    const other = document.createElement('button')
    document.body.append(other)
    other.focus()
    await settleVue(6)
    expect(document.querySelector('.k-ai-command-palette-panel')).toBeNull()
    expect(document.activeElement).toBe(other)
    other.remove()
  })

  it('keeps the panel open during the browser focus gap before Back receives focus', async () => {
    const close = vi.fn()
    await mountVue(AICommandPalette, { ...props, onClose: close })
    document.querySelector<HTMLButtonElement>('[role=option]')!.click()
    await settleVue(4)
    const filter = document.querySelector<HTMLInputElement>('.k-ai-command-palette-panel input')!
    const back = document.querySelector<HTMLButtonElement>('button[aria-label=Back]')!
    const active = vi.spyOn(document, 'activeElement', 'get').mockReturnValue(document.body)
    try {
      filter.dispatchEvent(new FocusEvent('focusout', { bubbles: true, relatedTarget: back }))
      await settleVue(4)
      expect(close).not.toHaveBeenCalled()
    } finally { active.mockRestore() }
  })

  it('handles Escape from Back without dismissing during its internal focus transition', async () => {
    const close = vi.fn()
    await mountVue(AICommandPalette, { ...props, onClose: close })
    document.querySelector<HTMLButtonElement>('[role=option]')!.click()
    await settleVue(4)
    const back = document.querySelector<HTMLButtonElement>('button[aria-label=Back]')!
    back.focus()
    back.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    back.dispatchEvent(new FocusEvent('focusout', { bubbles: true, relatedTarget: null }))
    await settleVue(4)
    expect(close).not.toHaveBeenCalled()
    expect(document.querySelector('[role=listbox]')?.getAttribute('aria-label')).toBe('Assistant commands')
  })
  it('does not bind or focus its external editor after unmount during a queued open', async () => {
    const editor = document.createElement('textarea')
    document.body.append(editor)
    editor.focus()
    const focus = vi.spyOn(editor, 'focus')
    const view = await mountVue(AICommandPalette, { ...props, open: false, preserveComposerFocus: true })
    view.props.open = true
    queueMicrotask(() => view.unmount())
    await settleVue(8)
    expect(focus).not.toHaveBeenCalled()
    expect(editor.getAttribute('role')).toBeNull()
    editor.remove()
  })

  it('commits selection before dismissing when the consumer focuses its editor', async () => {
    const editor = document.createElement('textarea')
    document.body.append(editor)
    const close = vi.fn()
    await mountVue(AICommandPalette, { ...props, onClose: close, onSelectSkill: () => editor.focus() })
    document.querySelector<HTMLButtonElement>('[role=option]')!.click()
    await settleVue(4)
    const filter = document.querySelector<HTMLInputElement>('.k-ai-command-palette-panel input')!
    filter.focus()
    filter.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    await settleVue(4)
    expect(close).toHaveBeenCalledOnce()
    expect(close.mock.calls[0]).toEqual([])
    editor.remove()
  })

  it('recovers from a failed resource lookup and allows retry through Back', async () => {
    const discover = vi.fn().mockRejectedValueOnce(new Error('Network failure')).mockResolvedValue({ groups: [], warnings: [] })
    const view = await mountVue(AICommandPalette, { ...props, resourceProviders: [{ name: 'demo', displayName: 'Demo', resourceTypes: [] }], discoverResources: discover })
    const error = vi.fn()
    view.app.config.errorHandler = error
    const option = (text: string) => [...document.querySelectorAll<HTMLButtonElement>('[role=option]')].find(el => el.textContent?.includes(text))!
    option('/resource').click()
    await settleVue(4)
    option('Demo').click()
    await settleVue(8)
    expect(document.querySelector('.k-ai-command-palette-panel')?.textContent).toContain('Resources unavailable. Go back to retry.')
    expect(document.querySelector('.k-ai-command-palette-panel')?.textContent).not.toContain('Loading resources')
    document.querySelector<HTMLButtonElement>('button[aria-label=Back]')!.click()
    await settleVue(4)
    option('Demo').click()
    await settleVue(8)
    expect(discover).toHaveBeenCalledTimes(2)
    expect(error).not.toHaveBeenCalled()
    expect(document.querySelector('.k-ai-command-palette-panel')?.textContent).not.toContain('Resources unavailable')
  })

  it('ignores a late lookup failure after returning to the provider list', async () => {
    let reject!: (reason: Error) => void
    const pending = new Promise<never>((_, fail) => { reject = fail })
    const view = await mountVue(AICommandPalette, { ...props, resourceProviders: [{ name: 'demo', displayName: 'Demo', resourceTypes: [] }], discoverResources: () => pending })
    const error = vi.fn()
    view.app.config.errorHandler = error
    const option = (text: string) => [...document.querySelectorAll<HTMLButtonElement>('[role=option]')].find(el => el.textContent?.includes(text))!
    option('/resource').click()
    await settleVue(4)
    option('Demo').click()
    await settleVue(4)
    document.querySelector<HTMLButtonElement>('button[aria-label=Back]')!.click()
    await settleVue(4)
    reject(new Error('Late failure'))
    await settleVue(8)
    expect(document.querySelector('[role=listbox]')?.getAttribute('aria-label')).toBe('Providers')
    expect(document.querySelector('.k-ai-command-palette-panel')?.textContent).not.toContain('Resources unavailable')
    expect(error).not.toHaveBeenCalled()
  })

})
