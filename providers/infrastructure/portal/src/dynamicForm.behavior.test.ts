// @vitest-environment happy-dom
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'

import DynamicForm from './components/DynamicForm.vue'
import type { JSONSchema } from './types'

const mounted: Array<() => void> = []

afterEach(() => {
  while (mounted.length) mounted.pop()?.()
})

function mountForm(schema: JSONSchema, initial: Record<string, unknown>) {
  const values = ref({ ...initial })
  const form = ref<{ validate: () => Promise<{ valid: boolean, values: Record<string, unknown>, error?: string }> } | null>(null)
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(defineComponent({
    setup: () => () => h(DynamicForm, {
      ref: form,
      schema,
      values: values.value,
      'onUpdate:values': (next: Record<string, unknown>) => { values.value = next },
    }),
  }))
  app.mount(host)
  mounted.push(() => {
    app.unmount()
    host.remove()
  })
  return { host, values, form }
}

describe('DynamicForm collection editors', () => {
  it('validates line-based scalar arrays before updating the model', async () => {
    const { host, values } = mountForm({
      type: 'object',
      properties: {
        ports: { type: 'array', items: { type: 'integer' }, minItems: 1 },
        limits: { type: 'object', additionalProperties: { type: 'number' } },
      },
    }, { ports: [8080], limits: { cpu: 1 } })

    const ports = host.querySelector<HTMLTextAreaElement>('.dynform-lines')!
    ports.value = '8080\nlarge'
    ports.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(ports.validationMessage).toContain('ports line 2 must be an integer')
    expect(ports.getAttribute('aria-invalid')).toBe('true')
    expect(values.value.ports).toEqual([8080])

    ports.value = '8080\n9090'
    ports.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(ports.validationMessage).toBe('')
    expect(values.value.ports).toEqual([8080, 9090])
  })

  it('edits additionalProperties maps through accessible key/value rows', async () => {
    const { host, values } = mountForm({
      type: 'object',
      properties: { limits: { type: 'object', additionalProperties: { type: 'number' } } },
    }, { limits: { cpu: 1 } })

    const group = host.querySelector<HTMLElement>('[role="group"]')!
    const [key, value] = Array.from(group.querySelectorAll<HTMLInputElement>('input'))
    expect(key.labels?.[0]?.textContent).toContain('Key for limits')
    expect(value.labels?.[0]?.textContent).toContain('Value for limits key cpu')

    value.value = 'large'
    value.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(value.validationMessage).toContain('limits.cpu must be a number')
    expect(values.value.limits).toEqual({ cpu: 1 })

    value.value = '2'
    value.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(value.validationMessage).toBe('')
    expect(values.value.limits).toEqual({ cpu: 2 })

    const add = Array.from(group.querySelectorAll('button')).find(button => button.textContent?.includes('Add entry'))!
    add.click()
    await nextTick()
    const rows = group.querySelectorAll('.dynform-map-row')
    expect(rows).toHaveLength(2)
    const newInputs = rows[1].querySelectorAll<HTMLInputElement>('input')
    expect(document.activeElement).toBe(newInputs[0])
    newInputs[0].value = 'memory'
    newInputs[0].dispatchEvent(new Event('input', { bubbles: true }))
    newInputs[1].value = '4'
    newInputs[1].dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(values.value.limits).toEqual({ cpu: 2, memory: 4 })

    const remove = rows[1].querySelector<HTMLButtonElement>('button')!
    expect(remove.getAttribute('aria-label')).toContain('Remove limits key memory')
	expect(remove.classList.contains('k-icon-action')).toBe(true)
	expect(remove.getAttribute('data-k-tip')).toContain('Remove limits key memory')
	remove.click()
	await nextTick()
	expect(values.value.limits).toEqual({ cpu: 2 })
	expect(document.activeElement).toBe(group.querySelector<HTMLInputElement>('.dynform-map-row input'))

	const remainingRemove = group.querySelector<HTMLButtonElement>('.dynform-map-remove')!
	remainingRemove.click()
	await nextTick()
	expect(document.activeElement).toBe(add)
  })

  it('omits read-only values from the create form', () => {
    const { host } = mountForm({
      type: 'object',
      properties: { generated: { type: 'string', readOnly: true } },
    }, { generated: 'server-owned' })

    expect(host.querySelector('input')).toBeNull()
    expect(host.textContent).not.toContain('generated')
    expect(host.textContent).not.toContain('server-owned')
  })

  it('validates untouched nested required children and keeps valid empty maps', async () => {
    const { host, values, form } = mountForm({
      type: 'object',
      required: ['settings', 'labels'],
      properties: {
        settings: {
          type: 'object',
          description: 'Runtime settings',
          required: ['region'],
          properties: { region: { type: 'string', description: 'Deployment region' } },
        },
        labels: { type: 'object', additionalProperties: { type: 'string' } },
      },
    }, { settings: {}, labels: {} })

    const invalid = await form.value!.validate()
    expect(invalid.valid).toBe(false)
    expect(invalid.error).toContain('region is required')
    expect(host.querySelector('.dynform-group input')?.getAttribute('aria-invalid')).toBe('true')
    expect(document.activeElement).toBe(host.querySelector('.dynform-group input'))
    expect(values.value).toEqual({ settings: {}, labels: {} })

    const region = host.querySelector<HTMLInputElement>('.dynform-group input')!
    region.value = 'us-east-1'
    region.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()

    const valid = await form.value!.validate()
    expect(valid.valid).toBe(true)
    expect(valid.values).toEqual({ settings: { region: 'us-east-1' }, labels: {} })
    expect(values.value).toEqual({ settings: { region: 'us-east-1' }, labels: {} })
  })

  it('blocks untouched required arrays when their schema cardinality is unmet', async () => {
    const { host, form } = mountForm({
      type: 'object',
      required: ['ports'],
      properties: { ports: { type: 'array', items: { type: 'integer' }, minItems: 1 } },
    }, { ports: [] })

    const invalid = await form.value!.validate()
    expect(invalid.valid).toBe(false)
    expect(invalid.error).toContain('ports needs at least 1 item')
    expect(host.querySelector<HTMLTextAreaElement>('.dynform-lines')?.getAttribute('aria-invalid')).toBe('true')

    const ports = host.querySelector<HTMLTextAreaElement>('.dynform-lines')!
    ports.value = '8080'
    ports.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    const valid = await form.value!.validate()
    expect(valid.valid).toBe(true)
    expect(valid.values).toEqual({ ports: [8080] })
  })

  it('does not impose child requirements on an omitted optional object', async () => {
    const { form } = mountForm({
      type: 'object',
      properties: {
        settings: {
          type: 'object',
          required: ['region'],
          properties: { region: { type: 'string' } },
        },
      },
    }, {})

    const result = await form.value!.validate()
    expect(result.valid).toBe(true)
    expect(result.values).toEqual({})
  })

  it('renders scalar defaults while preserving explicit false and zero values', async () => {
    const { host, values, form } = mountForm({
      type: 'object',
      properties: {
        enabled: { type: 'boolean', default: true },
        retries: { type: 'integer', default: 3 },
        mode: { type: 'string', enum: ['safe', 'strict'], default: 'safe' },
        notes: { type: 'string' },
      },
    }, { enabled: false, retries: 0 })

    const enabled = host.querySelector<HTMLInputElement>('input.k-checkbox')!
    const retries = host.querySelector<HTMLInputElement>('input[type="number"]')!
    const mode = host.querySelector<HTMLSelectElement>('select')!
    const notes = [...host.querySelectorAll<HTMLInputElement>('input.k-input')].find(input => input.type === 'text')!
    expect(enabled.checked).toBe(false)
    expect(retries.value).toBe('0')
    expect(mode.value).toBe('safe')
    expect(notes.value).toBe('')
    expect(values.value).toEqual({ enabled: false, retries: 0 })

    const result = await form.value!.validate()
    expect(result.valid).toBe(true)
    expect(result.values).toEqual({ enabled: false, retries: 0, mode: 'safe' })
    expect(values.value).toEqual({ enabled: false, retries: 0, mode: 'safe' })
  })
})
