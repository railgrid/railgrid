// @vitest-environment happy-dom

import { createApp, nextTick, type App } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ProvisionPage from './views/ProvisionPage.vue'
import { api } from './api'

const toastMock = vi.hoisted(() => vi.fn())

vi.mock('./api', () => ({
  api: {
    getTemplate: vi.fn(),
    createInstance: vi.fn(),
  },
  isContextChangedError: vi.fn(() => false),
}))
vi.mock('./portalkit/toast', () => ({ toast: toastMock }))

async function flush(): Promise<void> {
  await Promise.resolve()
  await nextTick()
  await Promise.resolve()
  await nextTick()
}

describe('mounted Infrastructure provisioning workflow', () => {
  let app: App<Element> | null = null
  let host: HTMLDivElement

  beforeEach(() => {
    vi.mocked(api.getTemplate).mockResolvedValue({
      template: {
        name: 'demo-template',
        version: 'v2',
        displayName: 'Demo template',
        description: 'A test template',
        kind: 'Demo',
        inputsSchema: {
          type: 'object',
          properties: {
            enabled: { type: 'boolean', description: 'Enable the demo' },
            database: {
              type: 'object',
              description: 'Database settings',
              properties: {
                size: { type: 'string', description: 'Database size' },
              },
            },
          },
        },
        sampleValues: { enabled: false, database: { size: 'small' } },
      },
    })
    vi.mocked(api.createInstance).mockResolvedValue({ name: 'demo-instance' } as never)
    host = document.createElement('div')
    document.body.appendChild(host)
  })

  afterEach(() => {
    app?.unmount()
    app = null
    host.remove()
    vi.clearAllMocks()
  })

  it('keeps nested and boolean edits in the create-instance payload', async () => {
    const provisioned = vi.fn()
    app = createApp(ProvisionPage, {
      templateName: 'demo-template',
      onProvisioned: provisioned,
    })
    app.mount(host)
    await flush()

    const name = host.querySelector<HTMLInputElement>('#infrastructure-instance-name')
    const enabled = host.querySelector<HTMLInputElement>('input.k-checkbox')
    const size = [...host.querySelectorAll<HTMLInputElement>('input.k-input')]
      .find(input => input.id !== 'infrastructure-instance-name')
    expect(name).not.toBeNull()
    expect(enabled).not.toBeNull()
    expect(size).not.toBeNull()

    name!.value = 'demo-instance'
    name!.dispatchEvent(new Event('input', { bubbles: true }))
    enabled!.click()
    await flush()
    size!.value = 'large'
    size!.dispatchEvent(new Event('input', { bubbles: true }))
    await flush()

    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flush()

    expect(api.createInstance).toHaveBeenCalledWith({
      templateName: 'demo-template',
      templateVersion: 'v2',
      name: 'demo-instance',
      values: { enabled: true, database: { size: 'large' } },
    })
    expect(provisioned).toHaveBeenCalledWith('demo-instance')
    expect(toastMock).toHaveBeenCalledTimes(1)
    expect(toastMock).toHaveBeenCalledWith('info', 'Provisioning started for demo-instance.')
  })

  it('keeps a failed provision contextual and does not toast', async () => {
    vi.mocked(api.createInstance).mockRejectedValueOnce({ message: 'quota exceeded' })
    app = createApp(ProvisionPage, { templateName: 'demo-template' })
    app.mount(host)
    await flush()

    const name = host.querySelector<HTMLInputElement>('#infrastructure-instance-name')!
    name.value = 'demo-instance'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flush()

    expect(host.querySelector('[role="alert"]')?.textContent).toContain('quota exceeded')
    expect(toastMock).not.toHaveBeenCalled()
  })
})
