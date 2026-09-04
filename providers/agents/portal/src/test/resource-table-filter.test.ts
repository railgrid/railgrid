import { defineComponent, h, ref } from 'vue'
import { describe, expect, it } from 'vitest'
import ResourceTableFilter from '../portalkit/ResourceTableFilter.vue'
import { mountVue, settleVue } from './vue-helper'

async function mountFilter() {
  const options = [
    { value: 'connection', label: 'Connection' },
    { value: 'channel', label: 'Channel' },
  ]
  const value = ref('')
  const Harness = defineComponent({ setup: () => () => h(ResourceTableFilter, {
    definition: { key: 'kind', label: 'Kind', allLabel: 'All kinds' }, options, modelValue: value.value,
    'onUpdate:modelValue': (next: string) => { value.value = next },
  }) })
  return { ...(await mountVue(Harness, {})), value }
}

describe('PortalKit Vue resource table filter', () => {
  it('uses the standard visible-label combobox and portalled listbox', async () => {
    const { element: filter } = await mountFilter()
    const root = filter.querySelector('.k-table__filter')!
    const trigger = filter.querySelector<HTMLButtonElement>('.k-table__filter-trigger')!
    expect(root.textContent).toContain('Kind')
    expect(trigger.type).toBe('button')
    expect(trigger.getAttribute('role')).toBe('combobox')
    expect(trigger.getAttribute('aria-labelledby')).toContain('label')
    expect(trigger.textContent).toContain('All')

    trigger.click()
    await settleVue()
    const panel = document.body.querySelector('.k-table__filter-panel')!
    expect(panel).not.toBeNull()
    expect(filter.contains(panel)).toBe(false)
    expect(panel.querySelector('[role="listbox"]')).not.toBeNull()
    expect(trigger.getAttribute('aria-expanded')).toBe('true')
  })

  it('commits one keyboard selection and closes on unmount', async () => {
    const { element: filter, value, unmount } = await mountFilter()
    const trigger = filter.querySelector<HTMLButtonElement>('.k-table__filter-trigger')!
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    await settleVue()
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    await settleVue()

    expect(value.value).toBe('connection')
    expect(document.body.querySelector('.k-table__filter-panel')).toBeNull()

    trigger.click()
    unmount()
    expect(document.body.querySelector('.k-table__filter-panel')).toBeNull()
  })
})
