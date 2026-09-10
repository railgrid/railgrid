// @vitest-environment happy-dom

import { createApp, defineComponent, h, nextTick, ref, type App } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ProvisionPage from './views/ProvisionPage.vue'
import { api } from './api'

const state = vi.hoisted(() => ({
  validateCalls: 0,
  validation: Promise.resolve({ valid: true, values: {} as Record<string, unknown> }),
  resolveValidation: null as ((value: { valid: boolean, values: Record<string, unknown> }) => void) | null,
}))

const toastMock = vi.hoisted(() => vi.fn())

vi.mock('./api', () => ({
  api: {
    getTemplate: vi.fn(),
    createInstance: vi.fn(),
  },
  isContextChangedError: vi.fn(() => false),
}))
vi.mock('./portalkit/toast', () => ({ toast: toastMock }))
vi.mock('./components/DynamicForm.vue', () => ({
  default: defineComponent({
    name: 'DeferredDynamicForm',
    setup(_, { expose }) {
      expose({
        validate: () => {
          state.validateCalls += 1
          return state.validation
        },
      })
      return () => h('div', { class: 'deferred-dynamic-form' })
    },
  }),
}))

const template = {
  name: 'demo-template',
  version: 'v2',
  displayName: 'Demo template',
  description: 'A test template',
  kind: 'Demo',
  inputsSchema: { type: 'object', properties: { enabled: { type: 'boolean' } } },
  sampleValues: { enabled: false },
}

async function flush(): Promise<void> {
  await Promise.resolve()
  await nextTick()
  await Promise.resolve()
  await nextTick()
}

function beginValidation(): void {
  state.validateCalls = 0
  state.resolveValidation = null
  state.validation = new Promise(resolve => { state.resolveValidation = resolve })
}

describe('mounted Infrastructure provisioning validation boundary', () => {
  let app: App<Element> | null = null
  let host: HTMLDivElement

  beforeEach(() => {
    beginValidation()
    vi.mocked(api.getTemplate).mockResolvedValue({ template } as never)
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

  function enterName(): void {
    const input = host.querySelector<HTMLInputElement>('#infrastructure-instance-name')!
    input.value = 'demo-instance'
    input.dispatchEvent(new Event('input', { bubbles: true }))
  }

  it('latches before deferred validation and ignores duplicate submits', async () => {
    app = createApp(ProvisionPage, { templateName: template.name })
    app.mount(host)
    await flush()

    enterName()
    const form = host.querySelector('form')!
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flush()

    expect(state.validateCalls).toBe(1)
    expect(host.querySelector<HTMLButtonElement>('button[type="submit"]')?.disabled).toBe(true)
    expect(api.createInstance).not.toHaveBeenCalled()

    state.resolveValidation?.({ valid: true, values: { enabled: false } })
    await flush()

    expect(api.createInstance).toHaveBeenCalledTimes(1)
    expect(toastMock).toHaveBeenCalledWith('info', 'Provisioning started for demo-instance.')
  })

  it('drops a validation result after the template route changes', async () => {
    const templateName = ref(template.name)
    const provisioned = vi.fn()
    app = createApp(defineComponent({
      setup: () => () => h(ProvisionPage, { templateName: templateName.value, onProvisioned: provisioned }),
    }))
    app.mount(host)
    await flush()

    enterName()
    host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flush()
    expect(state.validateCalls).toBe(1)

    templateName.value = 'other-template'
    await flush()
    state.resolveValidation?.({ valid: true, values: { enabled: false } })
    await flush()

    expect(api.createInstance).not.toHaveBeenCalled()
    expect(provisioned).not.toHaveBeenCalled()
    expect(toastMock).not.toHaveBeenCalled()
  })
})
