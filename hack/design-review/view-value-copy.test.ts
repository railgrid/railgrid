import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import ViewValue from '../../providers/infrastructure/portal/src/components/ViewValue.vue'

const apps: ReturnType<typeof createApp>[] = []
afterEach(() => { for (const app of apps.splice(0)) app.unmount(); document.body.innerHTML = ''; vi.unstubAllGlobals(); vi.useRealTimers() })

it('shows local success and a recoverable failure beside the copied value', async () => {
  const writeText = vi.fn().mockResolvedValueOnce(undefined).mockRejectedValueOnce(new Error('denied'))
  vi.stubGlobal('navigator', { clipboard: { writeText } })
  const host = document.createElement('div')
  document.body.append(host)
  const value = ref({ type: 'code', text: 'edge.example.test', empty: false })
  const app = createApp(() => h(ViewValue, { value: value.value as any }))
  apps.push(app); app.mount(host)
  const button = () => host.querySelector<HTMLButtonElement>('button')!
  button().click()
  await vi.waitFor(() => expect(host.querySelector('[role="status"]')?.textContent).toBe('Copied'))
  expect(writeText).toHaveBeenCalledWith('edge.example.test')
  expect(button().getAttribute('aria-label')).toBe('Value copied')
  button().click()
  await vi.waitFor(() => expect(host.querySelector('[role="status"]')?.textContent).toContain('Could not copy'))
  expect(host.textContent).toContain('Select the value to copy it.')
  expect(host.textContent).toContain('edge.example.test')
})

it('ignores an old clipboard result when the displayed value changes', async () => {
  let resolveCopy!: () => void
  vi.stubGlobal('navigator', { clipboard: { writeText: () => new Promise<void>(resolve => { resolveCopy = resolve }) } })
  const host = document.createElement('div')
  document.body.append(host)
  const value = ref({ type: 'code', text: 'old-value', empty: false })
  const app = createApp(() => h(ViewValue, { value: value.value as any }))
  apps.push(app); app.mount(host)
  host.querySelector<HTMLButtonElement>('button')!.click()
  value.value = { type: 'code', text: 'new-value', empty: false }
  await nextTick()
  resolveCopy()
  await nextTick()
  expect(host.querySelector('[role="status"]')?.textContent).toBe('')
  expect(host.querySelector('button')?.getAttribute('aria-label')).toBe('Copy new-value')
})
