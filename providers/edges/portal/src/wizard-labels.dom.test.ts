// @vitest-environment happy-dom
import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'

const api = vi.hoisted(() => ({ createEdge: vi.fn(), probeEdge: vi.fn() }))
vi.mock('./api', () => api)
import Wizard from './Wizard.vue'

afterEach(() => { document.body.innerHTML = ''; vi.clearAllMocks() })

it.each(['env', '=prod', 'env=prod,env=dev', 'Bad_Prefix/name=x', 'env=-bad', 'env=a=b', 'env=x,'])(
  'rejects invalid labels %s without creating an edge', async labels => {
    const host = document.createElement('div')
    document.body.append(host)
    const app = createApp(Wizard, { cluster: null })
    app.mount(host)
    try {
      const name = host.querySelector<HTMLInputElement>('#edge-name')!
      const input = host.querySelector<HTMLInputElement>('#edge-labels')!
      name.value = 'edge-a'; name.dispatchEvent(new Event('input'))
      input.value = labels; input.dispatchEvent(new Event('input'))
      host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      await nextTick(); await nextTick()
      expect(api.createEdge).not.toHaveBeenCalled()
      expect(input.value).toBe(labels)
      expect(document.activeElement).toBe(input)
      expect(input.getAttribute('aria-invalid')).toBe('true')
      expect(host.querySelector('#edge-labels-error')?.textContent).toContain('key=value')
    } finally { app.unmount() }
  },
)

it('creates an edge with valid qualified labels and an explicit empty value', async () => {
  api.createEdge.mockResolvedValue(undefined)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(Wizard, { cluster: null })
  app.mount(host)
  try {
    const name = host.querySelector<HTMLInputElement>('#edge-name')!
    const input = host.querySelector<HTMLInputElement>('#edge-labels')!
    name.value = 'edge-a'; name.dispatchEvent(new Event('input'))
    const longPrefix = 'a'.repeat(64)
    input.value = `example.com/Zone=us_east.1, empty=, ${longPrefix}/name=value`; input.dispatchEvent(new Event('input'))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await nextTick()
    expect(api.createEdge).toHaveBeenCalledWith('edge-a', 'kubernetes', { 'example.com/Zone': 'us_east.1', empty: '', [`${longPrefix}/name`]: 'value' }, expect.any(AbortSignal))
  } finally { app.unmount() }
})
