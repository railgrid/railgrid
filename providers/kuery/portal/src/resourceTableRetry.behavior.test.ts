// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { expect, it, vi } from 'vitest'
import ResourceTable from './portalkit/ResourceTable.vue'

async function flush() { for (let i = 0; i < 4; i++) await nextTick() }

it.each([false, true])('retains focus on failure and restores it on success, loaded=%s', async loaded => {
  const state = reactive({ loaded, loading: false, error: 'Offline' as string | null })
  const retryHandler = vi.fn(() => { state.loading = true })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ setup: () => () => h(ResourceTable, { columns: [{ key: 'name', label: 'Name' }], rows: [], ...state, retryable: true, focusScope: 'inventory', onRetry: retryHandler }) })
  try {
    app.mount(host); await flush()
    const retry = host.querySelector<HTMLButtonElement>('.k-table__retry')!
    retry.focus(); retry.click(); retry.click(); await flush()
    expect(retryHandler).toHaveBeenCalledTimes(1)
    expect(document.activeElement).toBe(retry)
    expect(retry.getAttribute('aria-disabled')).toBe('true')
    state.loading = false; await flush()
    expect(document.activeElement).toBe(retry)
    expect(retry.getAttribute('aria-disabled')).toBe('false')
    retry.click(); await flush()
    state.loaded = true; state.loading = false; state.error = null; await flush()
    expect(document.activeElement).toBe(host.querySelector('.k-table__scroll'))
  } finally { app.unmount(); host.remove() }
})

it('does not recover focus across a scope change or steal moved focus', async () => {
  const state = reactive({ scope: 'one', error: 'Offline' as string | null })
  const host = document.createElement('div'); const outside = document.createElement('button'); document.body.append(host, outside)
  const app = createApp({ setup: () => () => h(ResourceTable, { columns: [{ key: 'name', label: 'Name' }], rows: [], loaded: true, error: state.error, retryable: true, focusScope: state.scope }) })
  try {
    app.mount(host); await flush()
    host.querySelector<HTMLButtonElement>('.k-table__retry')!.focus()
    state.scope = 'two'; await flush(); state.error = null; await flush()
    expect(document.activeElement).not.toBe(host.querySelector('.k-table__scroll'))
    state.error = 'Offline'; await flush()
    host.querySelector<HTMLButtonElement>('.k-table__retry')!.focus(); state.error = null; outside.focus(); await flush()
    expect(document.activeElement).toBe(outside)
  } finally { app.unmount(); host.remove(); outside.remove() }
})
