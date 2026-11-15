// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it, vi } from 'vitest'
import ResourceBoard from './portalkit/ResourceBoard.vue'

it('keeps empty columns discoverable and follows focus when revealing and hiding them', async () => {
  const columns = reactive([{ id: 'running', label: 'Running', count: 1 }, { id: 'blocked', label: 'Blocked', count: 0 }])
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp({ render: () => h(ResourceBoard, { columns, ariaLabel: 'Tasks' }) })
  app.mount(host)
  const button = host.querySelector<HTMLButtonElement>('[data-hidden-column="blocked"]')!
  expect(button.closest('.k-resource-board')).toBeNull()
  expect(button.textContent).toContain('Blocked')
  const scrollIntoView = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
  button.click()
  await nextTick(); await nextTick()
  const lane = host.querySelector<HTMLElement>('[data-board-column="blocked"]')!
  expect(document.activeElement).toBe(lane)
  expect(scrollIntoView).toHaveBeenCalled()
  lane.querySelector<HTMLButtonElement>('button')!.click()
  await nextTick(); await nextTick()
  expect(document.activeElement).toBe(host.querySelector('[data-hidden-column="blocked"]'))
  columns[1].count = 1
  await nextTick(); await nextTick()
  expect(host.querySelector('[data-hidden-column="blocked"]')).toBeNull()
  expect(host.querySelector('[data-board-column="blocked"]')).not.toBeNull()
  scrollIntoView.mockRestore()
  app.unmount(); host.remove()
})
