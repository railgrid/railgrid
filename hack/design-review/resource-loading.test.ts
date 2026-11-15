import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it, vi } from 'vitest'
import ResourcePage from '../../provider-sdk/portalkit-vue/ResourcePage.vue'
import ResourceTable from '../../provider-sdk/portalkit-vue/ResourceTable.vue'
import { DEFAULT_LOADING_INDICATOR_DELAY_MS } from '../../provider-sdk/portalkit-vue/useDelayedLoading'

for (const surface of ['page', 'table']) {
  it(`delays ${surface} loading feedback and retains data on refresh`, async () => {
    const dialog = document.createElement('dialog')
    const root = document.createElement('div')
    dialog.append(root)
    document.body.append(dialog)
    dialog.showModal()
    const state = reactive({ loaded: false, loading: true })
    const app = createApp({ render: () => surface === 'page'
      ? h(ResourcePage, { ...state, title: 'Deployments', refreshMode: 'background' }, { default: () => 'api-production' })
      : h(ResourceTable, {
        ...state, ariaLabel: 'deployments', refreshMode: 'background',
        rows: [{ name: 'api-production' }], columns: [{ key: 'name', label: 'Deployment' }],
      }),
    })
    const liveClass = surface === 'page' ? '.k-resource-page__live' : '.k-table__live'
    vi.useFakeTimers()
    try {
      app.mount(root)
      await nextTick()
      const live = dialog.querySelector(liveClass)!
      expect(live.closest('[aria-busy]')).toBeNull()
      expect(live.textContent).not.toMatch(/loading/i)
      expect(root.querySelector('[data-pending-hidden="true"]')?.getAttribute('aria-hidden')).toBe('true')

      await vi.advanceTimersByTimeAsync(DEFAULT_LOADING_INDICATOR_DELAY_MS - 1)
      expect(live.textContent).not.toMatch(/loading/i)
      await vi.advanceTimersByTimeAsync(1)
      expect(live.textContent).toMatch(/Loading deployments/i)
      expect(root.querySelector('[data-pending-hidden="true"]')).toBeNull()

      state.loaded = true
      state.loading = false
      await nextTick()
      expect(live.textContent).not.toMatch(/loading/i)
      expect(root.textContent).toContain('api-production')

      state.loading = true
      await nextTick()
      expect(live.textContent).toMatch(/Updating deployments/i)
      expect(root.textContent).toContain('api-production')
      expect(root.querySelector('[data-loading-visual]')).toBeNull()
    } finally {
      app.unmount()
      dialog.close()
      dialog.remove()
      vi.useRealTimers()
    }
  })
}
