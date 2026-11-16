import { expect, it, vi } from 'vitest'
import ResourceTable from '../portalkit/ResourceTable.vue'
import { mountVue } from './vue-helper'

it('excludes unavailable rows from row navigation without blocking other rows', async () => {
  const onRowClick = vi.fn()
  const rows = [{ name: 'deleting', deleting: true }, { name: 'ready', deleting: false }]
  const mounted = await mountVue(ResourceTable, {
    columns: [{ key: 'name', label: 'Name' }], rows, loaded: true,
    rowInteractive: (row: Record<string, unknown>) => !row.deleting,
    onRowClick,
  })
  const [deleting, ready] = [...mounted.element.querySelectorAll<HTMLElement>('.k-table__row')]
  expect(deleting.hasAttribute('tabindex')).toBe(false)
  expect(deleting.classList.contains('k-table__row--interactive')).toBe(false)
  expect(ready.tabIndex).toBe(0)
  deleting.click()
  deleting.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
  expect(onRowClick).not.toHaveBeenCalled()
  ready.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
  expect(onRowClick).toHaveBeenCalledOnce()
  expect(onRowClick).toHaveBeenCalledWith(rows[1])
})
