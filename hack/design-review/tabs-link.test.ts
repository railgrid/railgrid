import { expect, it } from 'vitest'
import { createApp, h } from 'vue'
import Tabs from '../../provider-sdk/portalkit-vue/Tabs.vue'
import { edgeCollectionHref } from '../../providers/edges/portal/src/routes'

it('preserves host routing and native modified links', () => {
  const host = document.createElement('div')
  document.body.append(host)
  const selected: string[] = []
  const app = createApp({ render: () => h(Tabs, {
    active: 'services', tabs: [
      { id: 'services', label: 'Services', href: '/ui/org/workspace/providers/edges/services' },
      { id: 'disabled', label: 'Unavailable', href: '/unavailable', disabled: true },
      { id: 'local', label: 'Local' },
    ], onSelect: (id: string) => selected.push(id),
  }) })
  app.mount(host)
  try {
    const link = host.querySelector<HTMLAnchorElement>('a')!
    expect(link.getAttribute('href')).toBe('/ui/org/workspace/providers/edges/services')
    expect(link.getAttribute('aria-current')).toBe('page')
    const ordinary = new MouseEvent('click', { bubbles: true, cancelable: true })
    link.dispatchEvent(ordinary)
    expect(ordinary.defaultPrevented).toBe(true)
    expect(selected).toEqual(['services'])
    for (const init of [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { altKey: true }, { button: 1 }]) {
      const event = new MouseEvent('click', { bubbles: true, cancelable: true, ...init })
      link.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(false)
    }
    expect(selected).toEqual(['services'])
    const disabled = host.querySelector<HTMLButtonElement>('[data-k-tab-id="disabled"]')!
    expect(disabled.tagName).toBe('BUTTON')
    expect(disabled.disabled).toBe(true)
    disabled.click()
    expect(selected).toEqual(['services'])
    const local = host.querySelector<HTMLButtonElement>('[data-k-tab-id="local"]')!
    expect(local.tagName).toBe('BUTTON')
    local.click()
    expect(selected).toEqual(['services', 'local'])
  } finally { app.unmount(); host.remove() }
})

it('builds scoped Edges destinations and leaves standalone selection as buttons', () => {
  const base = '/ui/org/workspace/providers/edges/'
  expect(edgeCollectionHref(base, 'edges')).toBe('/ui/org/workspace/providers/edges')
  expect(edgeCollectionHref(base, 'services')).toBe('/ui/org/workspace/providers/edges/services')
  expect(edgeCollectionHref(base, 'workloads')).toBe('/ui/org/workspace/providers/edges/workloads')
  expect(edgeCollectionHref(undefined, 'services')).toBeUndefined()
})
