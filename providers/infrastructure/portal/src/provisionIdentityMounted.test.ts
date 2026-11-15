// @vitest-environment happy-dom
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import App from './App.vue'
import { api } from './api'
import type { RailgridContext } from './types'

async function flush() {
  for (let i = 0; i < 4; i++) { await Promise.resolve(); await nextTick() }
}
afterEach(() => { window.sessionStorage.clear(); vi.restoreAllMocks() })
it('remounts and restores only the current caller recovery after identity switches', async () => {
  vi.spyOn(api, 'getTemplate').mockResolvedValue({ template: {
    name: 'demo', uid: 'uid', generation: 1, displayName: 'Demo', description: '', kind: 'Demo',
    inputsSchema: { type: 'object', properties: {} }, sampleValues: {},
  } } as never)
  vi.spyOn(api, 'createInstance').mockRejectedValue({ reason: 'CreateOutcomeUnknown' })
  const ctx = ref<RailgridContext>({ tenant: 'tenant', basePath: '/ui/providers/infrastructure', subPath: 'templates/demo', user: { userId: '', email: 'a@example.test' } })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(defineComponent(() => () => h(App, { ctx: ctx.value })))
  try {
    app.mount(host)
    await flush()
    const name = host.querySelector<HTMLInputElement>('#infrastructure-instance-name')!
    name.value = 'first-instance'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flush()
    expect(host.textContent).toContain('request for “first-instance” may have succeeded')
    ctx.value = { ...ctx.value, user: { userId: '', email: '  a@example.test  ' } }
    await flush()
    expect(host.textContent).not.toContain('may have succeeded')
    expect(window.sessionStorage.length).toBe(1)
    ctx.value = { ...ctx.value, user: { userId: '', email: 'b@example.test' } }
    await flush()
    expect(host.textContent).not.toContain('may have succeeded')
    expect(host.querySelector<HTMLInputElement>('#infrastructure-instance-name')!.value).toBe('')
    ctx.value = { ...ctx.value, user: { userId: '', email: '  a@example.test  ' } }
    await flush()
    expect(host.textContent).not.toContain('may have succeeded')
    ctx.value = { ...ctx.value, user: { userId: '', email: 'a@example.test' } }
    await flush()
    expect(host.textContent).toContain('request for “first-instance” may have succeeded')
    expect(api.createInstance).toHaveBeenCalledTimes(1)
  } finally { app.unmount(); host.remove() }
})
