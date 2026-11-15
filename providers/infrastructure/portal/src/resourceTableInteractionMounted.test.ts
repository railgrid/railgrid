// @vitest-environment happy-dom

import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it, vi } from 'vitest'
import ResourceTable from './portalkit/ResourceTable.vue'

it('updates row navigation as a deletion starts while keeping nested controls independent', async () => {
  const rows = reactive([{ name: 'one', deleting: false }, { name: 'two', deleting: false }])
  const open = vi.fn()
  const nested = vi.fn()
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({
    setup: () => () => h(ResourceTable, {
      columns: [{ key: 'name', label: 'Name' }], rows, loaded: true,
      rowKey: 'name', rowInteractive: (row: Record<string, unknown>) => !row.deleting,
      onRowClick: open,
    }, { name: ({ value }: { value: string }) => h('button', { onClick: nested }, value) }),
  })
  try {
    app.mount(host)
    await nextTick()
    const tableRows = () => [...host.querySelectorAll<HTMLTableRowElement>('tbody tr')]
    expect(tableRows().map(row => row.tabIndex)).toEqual([0, 0])
    rows[0]!.deleting = true
    await nextTick()
    const [deleting, ready] = tableRows()
    expect(deleting!.hasAttribute('tabindex')).toBe(false)
    expect(ready!.tabIndex).toBe(0)
    deleting!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    deleting!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(open).not.toHaveBeenCalled()
    ready!.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true }))
    expect(open).toHaveBeenCalledTimes(1)
    deleting!.querySelector('button')!.click()
    expect(nested).toHaveBeenCalledTimes(1)
    expect(open).toHaveBeenCalledTimes(1)
  } finally {
    app.unmount()
    host.remove()
  }
})
