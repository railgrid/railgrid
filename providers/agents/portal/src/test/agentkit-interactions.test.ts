import { describe, expect, it, vi } from 'vitest'
import AICommandPalette from '../agentkit/AICommandPalette.vue'
import AIConversationComposer from '../agentkit/AIConversationComposer.vue'
import { mountVue, settleVue } from './vue-helper'

const skill = { id: 'workspace:review', name: 'Review', description: 'Review a change', scope: 'workspace', enabled: true }
async function composer(extra: Record<string, unknown> = {}) {
  return mountVue(AIConversationComposer, { modelValue: '', skills: [skill], attachmentOwnerKey: 'agent', draftScopeKey: 'draft', ...extra })
}
function selectFiles(view: Awaited<ReturnType<typeof composer>>, files: File[]) {
  const input = view.element.querySelector<HTMLInputElement>('input[type=file]')!
  Object.defineProperty(input, 'files', { configurable: true, value: files })
  input.dispatchEvent(new Event('change', { bubbles: true }))
}

describe('shared composer interaction boundaries', () => {
  it('does not select a palette option while an IME commits text', async () => {
    const selectSkill = vi.fn()
    await mountVue(AICommandPalette, { open: true, commandQuery: '', skills: [skill], selectedSkillIDs: [], selectedResources: [], onSelectSkill: selectSkill })
    document.querySelector<HTMLButtonElement>('[role=option]')!.click()
    await settleVue(4)
    const filter = document.querySelector<HTMLInputElement>('.k-ai-command-palette-panel input')!
    const event = new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, bubbles: true, cancelable: true })
    filter.dispatchEvent(event)
    await settleVue(4)
    expect(selectSkill).not.toHaveBeenCalled()
    expect(event.defaultPrevented).toBe(false)
  })

  it.each(['draftScopeKey', 'attachmentOwnerKey'])('closes stale palettes and menus on %s changes', async key => {
    const view = await composer()
    const add = view.element.querySelector<HTMLButtonElement>('button[aria-haspopup=menu]')!
    add.click()
    await settleVue(4)
    document.querySelector<HTMLButtonElement>('[role=menuitem]:last-child')!.click()
    await settleVue(4)
    expect(document.querySelector('.k-ai-command-palette-panel')).not.toBeNull()
    await view.setProps({ [key]: 'other' })
    expect(document.querySelector('.k-ai-command-palette-panel')).toBeNull()
    add.click()
    await settleVue(4)
    expect(document.querySelector('[role=menu]')).not.toBeNull()
    await view.setProps({ [key]: 'third' })
    expect(document.querySelector('[role=menu]')).toBeNull()
  })

  it('does not attach a native chooser result to a different draft', async () => {
    const upload = vi.fn(async () => { throw new Error('unexpected upload') })
    const view = await composer({ attachmentTransport: { upload, delete: vi.fn() } })
    view.element.querySelector<HTMLButtonElement>('button[aria-haspopup=menu]')!.click()
    await settleVue(4)
    document.querySelector<HTMLButtonElement>('[role=menuitem]')!.click()
    await view.setProps({ draftScopeKey: 'other-draft' })
    selectFiles(view, [new File(['private'], 'private.txt', { type: 'text/plain' })])
    await settleVue(8)
    expect(upload).not.toHaveBeenCalled()
  })

  it('cancels queued native chooser focus restoration on a draft change', async () => {
    const view = await composer()
    view.element.querySelector<HTMLButtonElement>('button[aria-haspopup=menu]')!.click()
    await settleVue(4)
    document.querySelector<HTMLButtonElement>('[role=menuitem]')!.click()
    const other = document.createElement('button')
    document.body.append(other)
    window.dispatchEvent(new Event('focus'))
    other.focus()
    await view.setProps({ draftScopeKey: 'other-draft' })
    await settleVue(8)
    expect(document.activeElement).toBe(other)
    other.remove()
  })

  it('stops a multi-file selection when its draft changes during the first upload', async () => {
    let finish!: (value: unknown) => void
    const first = new Promise(resolve => { finish = resolve })
    const upload = vi.fn(() => first)
    const view = await composer({ attachmentTransport: { upload, delete: vi.fn(async () => {}) } })
    selectFiles(view, [new File(['one'], 'one.txt', { type: 'text/plain' }), new File(['two'], 'two.txt', { type: 'text/plain' })])
    await settleVue(4)
    expect(upload).toHaveBeenCalledTimes(1)
    await view.setProps({ draftScopeKey: 'other-draft' })
    finish({ id: 'one', filename: 'one.txt', contentType: 'text/plain', sizeBytes: 3, sha256: 'a'.repeat(64), createdAt: '2026-11-05T00:00:00Z' })
    await settleVue(12)
    expect(upload).toHaveBeenCalledTimes(1)
  })
})
