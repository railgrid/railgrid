import { afterEach, expect, it, vi } from 'vitest'
import { Play } from 'lucide-vue-next'
import ResourceTableActionButton from '../portalkit/ResourceTableActionButton.vue'
import { mountVue, unmountVueApps } from './vue-helper'

afterEach(() => unmountVueApps())

it('retains focus and blocks repeated activation while busy, then respects disabled state', async () => {
  const onClick = vi.fn()
  const mounted = await mountVue(ResourceTableActionButton, { icon: Play, label: 'Test connection', onClick })
  const button = mounted.element.querySelector('button')!
  button.focus()
  button.click()
  expect(onClick).toHaveBeenCalledTimes(1)

  await mounted.setProps({ busy: true, disabled: true })
  expect(document.activeElement).toBe(button)
  expect(button.disabled).toBe(false)
  expect(button.getAttribute('aria-disabled')).toBe('true')
  expect(button.getAttribute('aria-busy')).toBe('true')
  button.click()
  expect(onClick).toHaveBeenCalledTimes(1)

  await mounted.setProps({ busy: false, disabled: false })
  expect(document.activeElement).toBe(button)
  expect(button.hasAttribute('aria-disabled')).toBe(false)
  button.click()
  expect(onClick).toHaveBeenCalledTimes(2)

  await mounted.setProps({ disabled: true })
  expect(button.disabled).toBe(true)
  button.click()
  expect(onClick).toHaveBeenCalledTimes(2)
})
