import { createApp, h } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import ModelIDSelector from '../agentkit/ModelIDSelector.vue'
import { mountVue, settleVue } from './vue-helper'

describe('Model ID selector keyboard interaction', () => {
  it('describes required selection without an unsupported button state', async () => {
    const view = await mountVue(ModelIDSelector, { modelValue: '', models: [], required: true, describedBy: 'model-hint' })
    const trigger = view.element.querySelector<HTMLButtonElement>('#model-id')!
    expect(trigger.hasAttribute('aria-required')).toBe(false)
    const descriptions = trigger.getAttribute('aria-describedby')!.split(' ')
    expect(descriptions).toContain('model-hint')
    expect(view.element.querySelector(`#${descriptions[1]}`)?.textContent).toBe('Required')
    await view.setProps({ required: false })
    expect(trigger.getAttribute('aria-describedby')).toBe('model-hint')
    expect(view.element.querySelector(`#${descriptions[1]}`)).toBeNull()
  })

  it('preserves text editing and composition before choosing a model', async () => {
    const update = vi.fn()
    const { element } = await mountVue(ModelIDSelector, {
      modelValue: '',
      models: [{ id: 'model-one', name: 'Model One', compatibility: 'recommended' }],
      'onUpdate:modelValue': update,
    })
    const trigger = element.querySelector<HTMLButtonElement>('#model-id')!
    trigger.click()
    await settleVue()
    const input = document.querySelector<HTMLInputElement>('[aria-label="Search model IDs"]')!
    for (const key of ['Home', 'End']) {
      const event = new KeyboardEvent('keydown', { key, shiftKey: true, bubbles: true, cancelable: true })
      input.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(false)
    }
    for (const key of ['Enter', 'Escape', 'ArrowDown']) {
      const event = new KeyboardEvent('keydown', { key, isComposing: true, bubbles: true, cancelable: true })
      input.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(false)
    }
    input.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
    const unflaggedEnter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    input.dispatchEvent(unflaggedEnter)
    expect(unflaggedEnter.defaultPrevented).toBe(false)
    input.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true }))
    await settleVue()
    expect(update).not.toHaveBeenCalled()
    expect(document.querySelector('[aria-label="Search model IDs"]')).not.toBeNull()

    const escape = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    input.dispatchEvent(escape)
    await settleVue()
    expect(escape.defaultPrevented).toBe(true)
    expect(document.querySelector('[aria-label="Search model IDs"]')).toBeNull()
    expect(document.activeElement).toBe(trigger)
  })
  it('hosts the picker inside an open native dialog and restores focus on Escape', async () => {
    const dialog = document.createElement('dialog')
    dialog.open = true
    document.body.append(dialog)
    const app = createApp({ render: () => h(ModelIDSelector, { modelValue: '', models: [{ id: 'model-one', name: 'Model One', compatibility: 'available' }] }) })
    app.mount(dialog)
    try {
      const trigger = dialog.querySelector<HTMLButtonElement>('#model-id')!
      trigger.click()
      await settleVue()
      const input = dialog.querySelector<HTMLInputElement>('[aria-label="Search model IDs"]')!
      expect(input).toBeTruthy()
      expect(document.activeElement).toBe(input)
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      await settleVue()
      expect(dialog.querySelector('[aria-label="Search model IDs"]')).toBeNull()
      expect(document.activeElement).toBe(trigger)
      expect(dialog.open).toBe(true)
    } finally { app.unmount(); dialog.remove() }
  })

})
