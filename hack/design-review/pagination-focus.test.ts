import { createApp, h, nextTick } from 'vue'
import { expect, it } from 'vitest'
import ResourceTable from '../../provider-sdk/portalkit-vue/ResourceTable.vue'

it('keeps focus at pagination boundaries and ignores unavailable navigation', async () => {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({ render: () => h(ResourceTable, {
    variant: 'queryable', columns: [{ key: 'name', label: 'Name' }],
    rows: [{ name: 'one' }, { name: 'two' }], loaded: true,
    ariaLabel: 'Services', paginated: true, pageSize: 1, pageSizeOptions: [1],
  }) })
  try {
    app.mount(root)
    const next = root.querySelector<HTMLButtonElement>('[aria-label="Next page"]')!
    const previous = root.querySelector<HTMLButtonElement>('[aria-label="Previous page"]')!
    next.focus(); next.click(); await nextTick()
    expect(document.activeElement).toBe(next)
    expect(next.getAttribute('aria-disabled')).toBe('true')
    expect(root.textContent).toContain('2 / 2')
    next.click(); await nextTick()
    expect(root.textContent).toContain('2 / 2')
    previous.focus(); previous.click(); await nextTick()
    expect(document.activeElement).toBe(previous)
    expect(previous.getAttribute('aria-disabled')).toBe('true')
    previous.click(); await nextTick()
    expect(root.textContent).toContain('1 / 2')
  } finally { app.unmount(); root.remove() }
})
