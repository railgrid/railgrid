import { defineComponent, h, ref } from 'vue'
import { describe, expect, it } from 'vitest'
import FormSelect from '../portalkit/FormSelect.vue'
import { mountVue, settleVue } from './vue-helper'

async function mountSelect() {
  const label = document.createElement('span')
  label.id = 'model-label'
  label.textContent = 'Model credential'
  const options = [
    { value: '', label: '— no model —' },
    { value: 'main', label: 'main (gpt-5)' },
    { value: 'disabled', label: 'disabled', disabled: true },
    { value: 'backup', label: 'backup (claude)' },
  ]
  const value = ref('main')
  const Harness = defineComponent({ setup: () => () => h(FormSelect, {
    modelValue: value.value, options, labelledby: label.id,
    'onUpdate:modelValue': (next: string) => { value.value = next },
  }) })
  document.body.append(label)
  return { ...(await mountVue(Harness, {})), value }
}

describe('PortalKit Vue form select', () => {
  it('matches the canonical combobox/listbox and portalled menu contract', async () => {
    const { element: select } = await mountSelect()
    const trigger = select.querySelector<HTMLButtonElement>('[role="combobox"]')!
    expect(trigger.classList.contains('k-input')).toBe(true)
    expect(trigger.classList.contains('k-form-select__trigger')).toBe(true)
    expect(trigger.getAttribute('aria-labelledby')).toContain('model-label')
    expect(trigger.getAttribute('aria-expanded')).toBe('false')

    trigger.click()
    await settleVue()
    const panel = document.body.querySelector<HTMLElement>('.k-form-select__portal')!
    expect(panel).not.toBeNull()
    expect(select.contains(panel)).toBe(false)
    expect(panel.getAttribute('role')).toBe('listbox')
    expect(trigger.getAttribute('aria-expanded')).toBe('true')
    expect(trigger.getAttribute('aria-activedescendant')).toContain('option-1')
  })

  it('wraps keyboard navigation, skips disabled options, and updates the value', async () => {
    const { element: select, value } = await mountSelect()
    const trigger = select.querySelector<HTMLButtonElement>('[role="combobox"]')!

    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    await settleVue()
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    await settleVue()
    expect(trigger.getAttribute('aria-activedescendant')).toContain('option-3')
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    await settleVue()

    expect(value.value).toBe('backup')
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
  })

  it('closes on Escape and restores focus to the trigger', async () => {
    const { element: select } = await mountSelect()
    const trigger = select.querySelector<HTMLButtonElement>('[role="combobox"]')!
    trigger.click()
    await settleVue()
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await settleVue()

    expect(document.body.querySelector('.k-form-select__portal')).toBeNull()
    expect(document.activeElement).toBe(trigger)
  })
})
