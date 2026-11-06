import { describe, expect, it, vi } from 'vitest'
import AIConversationComposer from '../agentkit/AIConversationComposer.vue'
import { mountVue, settleVue } from './vue-helper'

async function setup() {
  const view = await mountVue(AIConversationComposer, { modelValue: '', skills: [] })
  const add = view.element.querySelector<HTMLButtonElement>('button[aria-haspopup=menu]')!
  const input = view.element.querySelector<HTMLInputElement>('input[type=file]')!
  async function files() {
    add.click()
    await settleVue()
    document.querySelector<HTMLButtonElement>('[role=menuitem]')!.click()
    await settleVue()
  }
  return { view, add, input, files }
}

describe('native file picker activation', () => {
  it('activates the file input directly even when showPicker is present', async () => {
    const { input, files } = await setup()
    const showPicker = vi.fn()
    Object.defineProperty(input, 'showPicker', { configurable: true, value: showPicker })
    const click = vi.spyOn(input, 'click')
    await files()
    expect(click).toHaveBeenCalledOnce()
    expect(showPicker).not.toHaveBeenCalled()
  })

  it('keeps menu pointer and focus interactions inside its body-mounted panel', async () => {
    const { view, add, input } = await setup()
    const click = vi.spyOn(input, 'click').mockImplementation(() => {})
    add.click()
    await settleVue()
    const menu = document.querySelector<HTMLElement>('[role=menu][aria-label=Add]')!
    expect(menu.parentElement).toBe(document.body)
    expect(view.element.contains(menu)).toBe(false)
    const files = menu.querySelector<HTMLButtonElement>('[role=menuitem]')!
    files.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    files.focus()
    await settleVue()
    expect(menu.isConnected).toBe(true)
    files.click()
    await settleVue()
    expect(click).toHaveBeenCalledOnce()
    expect(document.querySelector('[role=menu][aria-label=Add]')).toBeNull()
    expect(input.isConnected).toBe(true)
  })

  it('does not mistake element focus for a return from the native dialog', async () => {
    const { view, input, files } = await setup()
    vi.spyOn(input, 'click').mockImplementation(() => {})
    await files()
    const other = document.createElement('button')
    document.body.append(other)
    other.focus()
    await settleVue()
    expect(document.activeElement).toBe(other)
    input.dispatchEvent(new Event('cancel'))
    await settleVue()
    expect(document.activeElement).toBe(view.element.querySelector('.assistant-rich-composer'))
    other.remove()
  })
})
