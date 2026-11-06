import { describe, expect, it, vi } from 'vitest'
import AICommandPalette from '../agentkit/AICommandPalette.vue'
import AIConversationComposer from '../agentkit/AIConversationComposer.vue'
import { mountVue, settleVue } from './vue-helper'

const skill = { id: 'workspace:review', name: 'Review', description: 'Review a change', scope: 'workspace', enabled: true }
const paletteProps = { open: true, commandQuery: '', skills: [skill], selectedSkillIDs: [], selectedResources: [] }

describe('shared palette and chip accessibility', () => {
  it('gives simultaneously mounted palettes distinct listbox and option IDs', async () => {
    await mountVue(AICommandPalette, paletteProps)
    await mountVue(AICommandPalette, paletteProps)
    const elements = [...document.querySelectorAll('.k-ai-command-palette-panel [id]')]
    const ids = elements.map(el => el.id)
    expect(new Set(ids).size).toBe(ids.length)
    for (const panel of document.querySelectorAll('.k-ai-command-palette-panel')) {
      const list = panel.querySelector('[role=listbox]')!
      const active = list.getAttribute('aria-activedescendant')!
      expect(list.contains(document.getElementById(active))).toBe(true)
    }
  })

  it('starts keyboard selection on an available skill instead of an already added one', async () => {
    const select = vi.fn()
    await mountVue(AICommandPalette, { ...paletteProps, skills: [skill, { ...skill, id: 'workspace:ship', name: 'Ship' }], selectedSkillIDs: [skill.id], onSelectSkill: select })
    document.querySelector<HTMLButtonElement>('[role=option]')!.click()
    await settleVue(4)
    document.querySelector<HTMLButtonElement>('[role=option][disabled]')!.dispatchEvent(new MouseEvent('mouseenter'))
    await settleVue(4)
    document.querySelector<HTMLInputElement>('.k-ai-command-palette-panel input')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    await settleVue(4)
    expect(select).toHaveBeenCalledWith(expect.objectContaining({ name: 'Ship' }))
  })

  it('shows a useful empty state when no command matches', async () => {
    await mountVue(AICommandPalette, { ...paletteProps, commandQuery: 'no-such-command' })
    expect(document.querySelector('.k-ai-command-palette-panel [role=status]')?.textContent).toContain('No commands match')
  })

  it('names inline skill activation as removal', async () => {
    const view = await mountVue(AIConversationComposer, { modelValue: '', skills: [skill], selectedSkills: [skill], contentParts: [{ type: 'skill', skillID: skill.id }] })
    expect(view.element.querySelector('[data-assistant-chip]')?.getAttribute('aria-label')).toBe('Remove skill Review')
  })

  it('keeps image error descriptions within their owning composer across Vue roots', async () => {
    for (let index = 0; index < 2; index++) {
      const view = await mountVue(AIConversationComposer, { modelValue: '', skills: [], attachmentOwnerKey: 'agent', draftScopeKey: 'draft',
        attachmentTransport: { upload: async () => { throw new Error(`Failure ${index}`) }, delete: async () => {} } })
      const input = view.element.querySelector<HTMLInputElement>('input[type=file]')!
      Object.defineProperty(input, 'files', { configurable: true, value: [new File(['image'], 'image.png', { type: 'image/png' })] })
      input.dispatchEvent(new Event('change', { bubbles: true }))
      await settleVue(8)
    }
    const groups = document.querySelectorAll('.k-agentkit-attachment-draft[aria-describedby]')
    expect(groups).toHaveLength(2)
    for (const group of groups) expect(group.contains(document.getElementById(group.getAttribute('aria-describedby')!))).toBe(true)
  })
})
