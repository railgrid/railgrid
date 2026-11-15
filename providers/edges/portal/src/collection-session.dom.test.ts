// @vitest-environment happy-dom
import { expect, it } from 'vitest'
import { createApp, h, nextTick, ref } from 'vue'
import EdgeCollection from './EdgeCollection.vue'

it('keeps focused search when the final edge disappears and resets for a new scope', async () => {
  const rows = ref<any[]>([{ name: 'edge-a', type: 'kubernetes', connected: true }])
  const scope = ref(1)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(EdgeCollection, {
    key: scope.value, edges: rows.value, loaded: true, loading: false,
    refreshMode: 'foreground', error: null, foregroundLoading: false,
  }) })
  app.mount(host)
  try {
    await nextTick()
    const search = host.querySelector<HTMLInputElement>('input[type="search"]')!
    search.focus()
    rows.value = []
    await nextTick()
    expect(document.activeElement).toBe(search)
    expect(host.querySelector('input[type="search"]')).toBe(search)
    expect(host.textContent).not.toContain('Connect your first edge')
    scope.value++
    await nextTick()
    expect(host.textContent).toContain('Connect your first edge')
  } finally { app.unmount(); host.remove() }
})

it('keeps refresh available and focused on the first-use guide and after an edge appears', async () => {
  const rows = ref<any[]>([])
  const loading = ref(false)
  let refreshes = 0
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(EdgeCollection, {
    edges: rows.value, loaded: true, loading: loading.value,
    refreshMode: 'foreground', error: null, foregroundLoading: loading.value,
    onRefresh: () => { refreshes++ },
  }) })
  app.mount(host)
  try {
    await nextTick()
    expect(host.textContent).toContain('Connect your first edge')
    expect([...host.querySelectorAll('button')].filter(button => button.textContent?.includes('Connect edge'))).toHaveLength(1)
    const refresh = [...host.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Refresh')!
    refresh.focus()
    refresh.click()
    expect(refreshes).toBe(1)
    loading.value = true
    await nextTick()
    expect(document.activeElement).toBe(refresh)
    expect(refresh.getAttribute('aria-disabled')).toBe('true')
    refresh.click()
    expect(refreshes).toBe(1)
    rows.value = [{ name: 'edge-a', type: 'kubernetes', connected: true }]
    loading.value = false
    await nextTick()
    expect(document.activeElement).toBe(refresh)
    expect(host.textContent).not.toContain('Connect your first edge')
    expect(host.textContent).toContain('edge-a')
  } finally { app.unmount(); host.remove() }
})
