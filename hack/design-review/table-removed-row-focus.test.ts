import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it } from 'vitest'
import ResourceTable from '../../provider-sdk/portalkit-vue/ResourceTable.vue'
import ResourceTableDeleteButton from '../../provider-sdk/portalkit-vue/ResourceTableDeleteButton.vue'

for (const count of [1, 2]) {
  it(`returns focus to the named table region when a focused row disappears (${count} rows)`, async () => {
    const root = document.createElement('div')
    document.body.append(root)
    const state = reactive({ rows: Array.from({ length: count }, (_, i) => ({ name: `service-${i}` })) })
    const app = createApp({ render: () => h(ResourceTable, {
      columns: [{ key: 'name', label: 'Service', primary: true }, { key: 'actions', label: '', ariaLabel: 'Actions' }],
      rows: state.rows, loaded: true, ariaLabel: 'Services', rowKey: 'name',
    }, { actions: ({ row }: { row: { name: string } }) => h(ResourceTableDeleteButton, {
      label: `Delete ${row.name}`, onClick: () => { state.rows = state.rows.filter(item => item.name !== row.name) },
    }) }) })
    try {
      app.mount(root)
      const action = root.querySelector<HTMLButtonElement>('[aria-label="Delete service-0"]')!
      action.focus()
      action.click()
      await nextTick()
      expect(document.activeElement).toBe(root.querySelector('[role="region"][aria-label="Services scroll area"]'))
      expect(root.textContent).toContain(count === 1 ? 'No data' : 'service-1')
    } finally { app.unmount(); root.remove() }
  })
}

it('does not move focus if the user has moved away before the row update', async () => {
  const root = document.createElement('div')
  const elsewhere = document.createElement('button')
  document.body.append(root, elsewhere)
  const state = reactive({ rows: [{ name: 'service' }] })
  const app = createApp({ render: () => h(ResourceTable, {
    columns: [{ key: 'name', label: 'Service', primary: true }, { key: 'actions', label: '', ariaLabel: 'Actions' }],
    rows: state.rows, loaded: true, ariaLabel: 'Services', rowKey: 'name',
  }, { actions: () => h(ResourceTableDeleteButton, { label: 'Delete service' }) }) })
  try {
    app.mount(root)
    root.querySelector<HTMLButtonElement>('[aria-label="Delete service"]')!.focus()
    state.rows = []
    elsewhere.focus()
    await nextTick()
    expect(document.activeElement).toBe(elsewhere)
  } finally { app.unmount(); root.remove(); elsewhere.remove() }
})

it('returns focus when a confirmation immediately removes its row', async () => {
  const { default: ActionMenu } = await import('../../provider-sdk/portalkit-vue/ActionMenu.vue')
  const { default: ConfirmDialog } = await import('../../provider-sdk/portalkit-vue/ConfirmDialog.vue')
  const { confirmDialog } = await import('../../provider-sdk/portalkit-vue/confirm')
  const root = document.createElement('div')
  document.body.append(root)
  const state = reactive({ rows: [{ name: 'service' }] })
  const app = createApp({ render: () => h('div', [
    h(ResourceTable, { columns: [{ key: 'name', label: 'Service', primary: true }, { key: 'actions', label: '', ariaLabel: 'Actions' }], rows: state.rows, loaded: true, ariaLabel: 'Services', rowKey: 'name' }, {
      actions: () => h(ActionMenu, { label: 'Service actions', items: [{ id: 'delete', label: 'Delete' }], onSelect: async () => { if (await confirmDialog({ title: 'Delete service', danger: true })) state.rows = [] } }),
    }), h(ConfirmDialog),
  ]) })
  try {
    app.mount(root)
    const trigger = root.querySelector<HTMLButtonElement>('[aria-haspopup="menu"]')!
    trigger.focus()
    trigger.click()
    await nextTick()
    document.querySelector<HTMLButtonElement>('[role="menuitem"]')!.click()
    await nextTick(); await nextTick()
    document.querySelector<HTMLButtonElement>('.k-modal-btn--confirm')!.click()
    for (let i = 0; i < 5; i++) await nextTick()
    expect(root.textContent).toContain('No data')
    expect(document.activeElement).toBe(root.querySelector('[role="region"][aria-label="Services scroll area"]'))
  } finally { app.unmount(); root.remove() }
})
