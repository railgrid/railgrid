import { afterEach, expect, it, vi } from 'vitest'
import OverflowTitle from '../agentkit/AIOverflowTitle.vue'
import { mountVue } from './vue-helper'

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

it('scrolls only overflowing engaged titles, remeasures on resize, and cancels on exit', async () => {
  let resize = () => {}
  const disconnect = vi.fn()
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback: () => void) { resize = callback }
    observe() {}
    disconnect = disconnect
  })
  const view = await mountVue(OverflowTitle, { text: 'A long conversation title', active: false })
  const outer = view.element.querySelector<HTMLElement>('.k-ai-overflow-title')!
  const inner = view.element.querySelector<HTMLElement>('.k-ai-overflow-title__text')!
  let width = 100
  Object.defineProperty(outer, 'clientWidth', { get: () => width })
  Object.defineProperty(inner, 'scrollWidth', { get: () => 250 })
  const cancel = vi.fn()
  const animate = vi.fn(() => ({ cancel }))
  Object.defineProperty(inner, 'animate', { value: animate })
  resize()
  expect(animate).not.toHaveBeenCalled()
  await view.setProps({ active: true })
  expect(JSON.stringify(animate.mock.calls)).toContain('translateX(-150px)')
  width = 300
  resize()
  expect(cancel).toHaveBeenCalled()
  expect(animate).toHaveBeenCalledTimes(1)
  width = 100
  resize()
  expect(animate).toHaveBeenCalledTimes(2)
  await view.setProps({ active: false })
  expect(cancel).toHaveBeenCalledTimes(2)
  view.unmount()
  expect(disconnect).toHaveBeenCalled()
})

it('keeps the full title available without animation under reduced motion', async () => {
  vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
  const view = await mountVue(OverflowTitle, { text: 'The complete title remains accessible', active: false })
  const outer = view.element.querySelector<HTMLElement>('.k-ai-overflow-title')!
  const inner = view.element.querySelector<HTMLElement>('.k-ai-overflow-title__text')!
  Object.defineProperty(outer, 'clientWidth', { value: 80 })
  Object.defineProperty(inner, 'scrollWidth', { value: 300 })
  const animate = vi.fn()
  Object.defineProperty(inner, 'animate', { value: animate })
  await view.setProps({ active: true })
  expect(animate).not.toHaveBeenCalled()
  expect(outer.textContent).toBe('The complete title remains accessible')
  view.unmount()
})
