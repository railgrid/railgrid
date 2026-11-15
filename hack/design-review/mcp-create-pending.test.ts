import { createApp, nextTick } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { expect, it, onTestFinished, vi } from 'vitest'
import MCPPage from '../../portal/src/pages/MCPPage.vue'
import { useTenantStore } from '../../portal/src/stores/tenant'

const request = vi.hoisted(() => vi.fn())
vi.mock('../../portal/src/auth/session', () => ({ authFetch: request }))
vi.mock('../../portal/src/components/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))

it.each(['click', 'submit'] as const)('keeps the submitted draft and focus during %s submission and restores editing on failure', async (method) => {
  let finish!: (response: Response) => void
  request.mockReset().mockImplementation((_path: string, options?: RequestInit) => options?.method === 'POST'
    ? new Promise<Response>(resolve => { finish = resolve })
    : Promise.resolve(Response.json({ items: [] })))
  const pinia = createPinia()
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/mcp', name: 'mcp', component: MCPPage },
    { path: '/create', name: 'mcp-create', component: MCPPage },
  ] })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp(MCPPage).use(pinia).use(router)
  const tenant = useTenantStore(pinia)
  tenant.orgUUID = 'org'; tenant.workspaceUUID = 'workspace'
  await router.push('/create'); await router.isReady(); app.mount(host)
  onTestFinished(() => { app.unmount(); host.remove() })
  const name = host.querySelector<HTMLInputElement>('input[placeholder="ops"]')!
  const checkbox = host.querySelector<HTMLInputElement>('input[type="checkbox"]')!
  const submit = [...host.querySelectorAll('button')].find(button => button.textContent?.includes('Create server'))!
  name.value = 'operations'; name.dispatchEvent(new Event('input', { bubbles: true }))
  await nextTick()
  const focused = method === 'click' ? submit : name
  focused.focus()
  if (method === 'click') submit.click()
  else name.form!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await nextTick()
  expect(document.activeElement).toBe(focused)
  expect(name.readOnly).toBe(true)
  expect(submit.getAttribute('aria-disabled')).toBe('true')
  expect(submit.getAttribute('aria-busy')).toBe('true')
  checkbox.click()
  expect(checkbox.checked).toBe(false)
  submit.click()
  expect(request.mock.calls.filter(([, options]) => options?.method === 'POST')).toHaveLength(1)
  finish(new Response(null, { status: 503 }))
  await vi.waitFor(() => expect(host.textContent).toContain('Create failed'))
  expect(name.readOnly).toBe(false)
  expect(name.value).toBe('operations')
  expect(document.activeElement).toBe(focused)
})
