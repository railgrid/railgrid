// @vitest-environment happy-dom
import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'

import DynamicForm from './components/DynamicForm.vue'
import type { JSONSchema } from './types'
import { validateSchemaValue } from './schemaValidation'

const mounted: Array<() => void> = []

afterEach(() => {
  while (mounted.length) mounted.pop()?.()
})

function mountForm(schema: JSONSchema, initial: Record<string, unknown>) {
  const currentSchema = ref(schema)
  const values = ref({ ...initial })
  const form = ref<{ validate: () => Promise<{ valid: boolean, values: Record<string, unknown>, error?: string }> } | null>(null)
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(defineComponent({
    setup: () => () => h(DynamicForm, {
      ref: form,
      schema: currentSchema.value,
      values: values.value,
      'onUpdate:values': (next: Record<string, unknown>) => { values.value = next },
    }),
  }))
  app.mount(host)
  mounted.push(() => {
    app.unmount()
    host.remove()
  })
  return { host, values, form, currentSchema }
}

describe('DynamicForm collection editors', () => {
  it('asks for an enum selection and preserves the selected value type', async () => {
    const { host, form } = mountForm({ type: 'object', required: ['size'], properties: {
      size: { type: 'integer', enum: [1, 2] },
    } }, {})
    const select = host.querySelector<HTMLSelectElement>('select')!
    expect(select.value).toBe('')
    expect(select.selectedOptions[0].textContent).toBe('Select an option')
    expect((await form.value!.validate()).valid).toBe(false)
    expect(document.activeElement).toBe(select)
    select.value = '1'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    expect(await form.value!.validate()).toEqual({ valid: true, values: { size: 2 } })
  })

  it('shows an enum default and updates it', async () => {
    const { host, form } = mountForm({ type: 'object', properties: {
      mode: { type: 'string', enum: ['safe', 'strict'], default: 'safe' },
    } }, {})
    const select = host.querySelector<HTMLSelectElement>('select')!
    expect(select.selectedOptions[0].textContent).toBe('safe')
    select.value = '1'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    expect(await form.value!.validate()).toEqual({ valid: true, values: { mode: 'strict' } })
  })

  it('prompts for a new map enum value and validates its typed selection', async () => {
    const { host, form } = mountForm({ type: 'object', properties: {
      limits: { type: 'object', additionalProperties: { type: 'integer', enum: [1, 2] } },
    } }, {})
    host.querySelector<HTMLButtonElement>('.dynform-map-add')!.click()
    await nextTick()
    const key = host.querySelector<HTMLInputElement>('.dynform-map-row input')!
    key.value = 'cpu'
    key.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    const select = host.querySelector<HTMLSelectElement>('.dynform-map-row select')!
    expect(select.selectedOptions[0].textContent).toBe('Select an option')
    expect((await form.value!.validate()).valid).toBe(false)
    expect(document.activeElement).toBe(select)
    select.value = '2'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    expect(await form.value!.validate()).toEqual({ valid: true, values: { limits: { cpu: 2 } } })
  })

  it('keeps labels concise and places described help after scalar and map controls', () => {
    const { host } = mountForm({ type: 'object', properties: {
      image: { type: 'string', description: 'Choose a container image.' },
      env: { type: 'object', description: 'Add public environment values.', additionalProperties: { type: 'string' } },
    } }, { image: 'nginx:alpine', env: {} })
    const input = host.querySelector<HTMLInputElement>('input')!
    const help = host.querySelector<HTMLElement>(`#${input.getAttribute('aria-describedby')}`)!
    expect(input.labels?.[0]?.textContent).toBe('image')
    expect(help.textContent).toBe('Choose a container image.')
    expect(input.compareDocumentPosition(help) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const group = host.querySelector<HTMLElement>('[role="group"]')!
    const mapHelp = host.querySelector<HTMLElement>(`#${group.getAttribute('aria-describedby')}`)!
    expect(group.compareDocumentPosition(mapHelp) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
  it('preserves fractional integers for validation and accepts a corrected whole number', async () => {
    const { host, values, form } = mountForm({ type: 'object', properties: { replicas: { type: 'integer', minimum: 1 } } }, { replicas: 1 })
    const input = host.querySelector<HTMLInputElement>('input')!
    input.value = '1.5'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(input.value).toBe('1.5')
    expect(values.value.replicas).toBe(1.5)
    expect((await form.value!.validate()).valid).toBe(false)
    expect(input.getAttribute('aria-invalid')).toBe('true')
    expect(document.activeElement).toBe(input)
    input.value = '2'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(await form.value!.validate()).toEqual({ valid: true, values: { replicas: 2 } })
  })

  it('preserves exponent notation meaning for numeric scalars', async () => {
    const { host, values, form } = mountForm({ type: 'object', properties: { count: { type: 'integer' }, limit: { type: 'number' } } }, {})
    for (const input of host.querySelectorAll<HTMLInputElement>('input')) {
      input.value = '1e2'
      input.dispatchEvent(new Event('input', { bubbles: true }))
      await nextTick()
    }
    expect(values.value).toEqual({ count: 100, limit: 100 })
    expect((await form.value!.validate()).valid).toBe(true)
  })
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

  it('associates map errors with controls and focuses the offending value', async () => {
    const { host, form } = mountForm({ type: 'object', properties: {
      limits: { type: 'object', additionalProperties: { type: 'number' } },
    } }, { limits: { cpu: 1, memory: 2 } })
    const rows = host.querySelectorAll<HTMLElement>('.dynform-map-row')
    const value = rows[1].querySelectorAll<HTMLInputElement>('input')[1]
    value.value = 'invalid'
    value.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect((await form.value!.validate()).valid).toBe(false)
    expect(document.activeElement).toBe(value)
    expect(value.getAttribute('aria-invalid')).toBe('true')
    const error = host.querySelector<HTMLElement>(`#${value.getAttribute('aria-describedby')}`)!
    expect(error.textContent).toContain('limits.memory must be a number')
    for (const control of host.querySelectorAll<HTMLInputElement>('.dynform-map-row input')) {
      if (control === value) continue
      expect(control.getAttribute('aria-invalid')).toBeNull()
      expect(control.getAttribute('aria-describedby')).toBeNull()
      expect(control.validationMessage).toBe('')
    }
    value.value = '4'
    value.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(value.getAttribute('aria-describedby')).toBeNull()
    expect((await form.value!.validate()).valid).toBe(true)
  })

  it('identifies the duplicate key rather than its valid neighbor', async () => {
    const { host, form } = mountForm({ type: 'object', properties: {
      limits: { type: 'object', additionalProperties: { type: 'number' } },
    } }, { limits: { cpu: 1, memory: 2 } })
    const rows = host.querySelectorAll<HTMLElement>('.dynform-map-row')
    const firstKey = rows[0].querySelector<HTMLInputElement>('input')!
    const secondKey = rows[1].querySelector<HTMLInputElement>('input')!
    secondKey.value = 'cpu'
    secondKey.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect((await form.value!.validate()).valid).toBe(false)
    expect(document.activeElement).toBe(secondKey)
    expect(secondKey.getAttribute('aria-invalid')).toBe('true')
    expect(firstKey.getAttribute('aria-invalid')).toBeNull()
    expect(firstKey.validationMessage).toBe('')
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
    expect(mode.selectedOptions[0].textContent).toBe('safe')
    expect(notes.value).toBe('')
    expect(values.value).toEqual({ enabled: false, retries: 0 })

    const result = await form.value!.validate()
    expect(result.valid).toBe(true)
    expect(result.values).toEqual({ enabled: false, retries: 0, mode: 'safe' })
    expect(values.value).toEqual({ enabled: false, retries: 0 })
  })

  it('submits typed scalar and map enum choices', async () => {
    const { host, form } = mountForm({ type: 'object', required: ['replicas', 'enabled'], properties: {
      replicas: { type: 'integer', enum: [1, 2] },
      enabled: { type: 'boolean', enum: [true, false] },
      limits: { type: 'object', additionalProperties: { type: 'number', enum: [1, 2] } },
    } }, { limits: { cpu: 1 } })
    const selects = [...host.querySelectorAll<HTMLSelectElement>('select')]
    for (const select of selects) {
      select.value = '1'
      select.dispatchEvent(new Event('change', { bubbles: true }))
      await nextTick()
    }
    expect(await form.value!.validate()).toEqual({ valid: true, values: { replicas: 2, enabled: false, limits: { cpu: 2 } } })
  })

  it('requires an explicit answer for a required boolean without a default', async () => {
    const { host, form } = mountForm({ type: 'object', required: ['enabled'], properties: {
      enabled: { type: 'boolean' },
    } }, {})
    const select = host.querySelector<HTMLSelectElement>('select')!
    expect(select.value).toBe('')
    expect((await form.value!.validate()).valid).toBe(false)
    select.value = 'false'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    expect(await form.value!.validate()).toEqual({ valid: true, values: { enabled: false } })
  })

  it('allows an optional enum choice to be cleared', async () => {
    const { host, form } = mountForm({ type: 'object', properties: { mode: { type: 'string', enum: ['on', 'off'] } } }, {})
    const select = host.querySelector<HTMLSelectElement>('select')!
    select.value = '0'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    select.value = ''
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    expect(await form.value!.validate()).toEqual({ valid: true, values: {} })
  })

  it('keeps a cleared optional enum unset even when it has a default', async () => {
    const { host, form } = mountForm({ type: 'object', properties: { mode: { type: 'string', enum: ['on', 'off'], default: 'on' } } }, {})
    const select = host.querySelector<HTMLSelectElement>('select')!
    expect(select.value).toBe('0')
    select.value = ''
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    expect(select.value).toBe('')
    expect(await form.value!.validate()).toEqual({ valid: true, values: {} })
    await nextTick()
    expect(select.value).toBe('')
  })

  it('accepts a selected empty enum value unless its schema disallows it', async () => {
    const { host, form } = mountForm({ type: 'object', required: ['mode'], properties: { mode: { type: 'string', enum: ['', 'on'] } } }, {})
    const select = host.querySelector<HTMLSelectElement>('select')!
    expect((await form.value!.validate()).valid).toBe(false)
    select.value = '0'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    expect(await form.value!.validate()).toEqual({ valid: true, values: { mode: '' } })

    const constrained = mountForm({ type: 'object', required: ['mode'], properties: { mode: { type: 'string', enum: ['', 'on'], minLength: 1 } } }, {})
    const constrainedSelect = constrained.host.querySelector<HTMLSelectElement>('select')!
    constrainedSelect.value = '0'
    constrainedSelect.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    expect((await constrained.form.value!.validate()).valid).toBe(false)
  })

  it('validates empty enum values in required properties of complex array items', async () => {
    const schema: JSONSchema = { type: 'object', properties: {
      entries: { type: 'array', items: { type: 'object', required: ['mode'], properties: { mode: { type: 'string', enum: ['', 'on'] } } } },
    } }
    const allowed = mountForm(schema, { entries: [{ mode: '' }] })
    expect(await allowed.form.value!.validate()).toEqual({ valid: true, values: { entries: [{ mode: '' }] } })
    const constrained = mountForm({ type: 'object', properties: {
      entries: { type: 'array', items: { type: 'object', required: ['mode'], properties: { mode: { type: 'string', enum: ['', 'on'], minLength: 1 } } } },
    } }, { entries: [{ mode: '' }] })
    expect((await constrained.form.value!.validate()).valid).toBe(false)
    const missing = mountForm(schema, { entries: [{}] })
    expect((await missing.form.value!.validate()).valid).toBe(false)
  })

  it('distinguishes unset, empty and mixed-type map enum choices', async () => {
    const { host, form } = mountForm({ type: 'object', properties: {
      options: { type: 'object', additionalProperties: { enum: [1, '1', ''] } },
    } }, { options: {} })
    host.querySelector<HTMLButtonElement>('.dynform-map-add')!.click()
    await nextTick()
    const key = host.querySelector<HTMLInputElement>('input')!
    const select = host.querySelector<HTMLSelectElement>('select')!
    key.value = 'choice'
    key.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(select.value).toBe('')
    expect((await form.value!.validate()).valid).toBe(false)
    for (const [index, expected] of [['0', 1], ['1', '1'], ['2', '']] as const) {
      select.value = index
      select.dispatchEvent(new Event('change', { bubbles: true }))
      await nextTick()
      expect(await form.value!.validate()).toEqual({ valid: true, values: { options: { choice: expected } } })
    }
  })

  it('preserves typed map selections across schema changes and rejects removed choices', async () => {
    const mapSchema = (options: unknown[]): JSONSchema => ({ type: 'object', properties: {
      options: { type: 'object', additionalProperties: { enum: options } },
    } })
    const { host, form, currentSchema } = mountForm(mapSchema([1, '1', 'other']), { options: { size: 1 } })
    currentSchema.value = mapSchema(['1', 'other', 1])
    await nextTick()
    expect(host.querySelector<HTMLSelectElement>('select')!.value).toBe('2')
    expect(await form.value!.validate()).toEqual({ valid: true, values: { options: { size: 1 } } })
    currentSchema.value = mapSchema(['1', 'other'])
    await nextTick()
    expect(host.querySelector<HTMLSelectElement>('select')!.value).toBe('')
    expect((await form.value!.validate()).valid).toBe(false)
    const select = host.querySelector<HTMLSelectElement>('select')!
    select.value = '0'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    expect(await form.value!.validate()).toEqual({ valid: true, values: { options: { size: '1' } } })
    currentSchema.value = { type: 'object', properties: { options: { type: 'object', additionalProperties: { type: 'string' } } } }
    await nextTick()
    expect(await form.value!.validate()).toEqual({ valid: true, values: { options: { size: '1' } } })
  })

  it('validates enums for object and array schemas with structural JSON equality', () => {
    expect(validateSchemaValue({ type: 'object', enum: [{ a: 1, b: [2] }] }, { b: [2], a: 1 })).toBeNull()
    expect(validateSchemaValue({ type: 'object', enum: [{ a: 1 }] }, { a: 2 })).toContain('allowed values')
    expect(validateSchemaValue({ type: 'array', enum: [[1, '1']] }, [1, '1'])).toBeNull()
    expect(validateSchemaValue({ type: 'array', enum: [[1, '1']] }, ['1', 1])).toContain('allowed values')
  })

  it('matches structured map enum values as JSON across refreshed schemas', async () => {
    const mapSchema = (options: unknown[]): JSONSchema => ({ type: 'object', properties: {
      options: { type: 'object', additionalProperties: { enum: options } },
    } })
    const { host, form, currentSchema } = mountForm(mapSchema([{ a: 1, b: [2] }, ['other']]), { options: { item: { b: [2], a: 1 } } })
    expect(host.querySelector<HTMLSelectElement>('select')!.value).toBe('0')
    currentSchema.value = mapSchema([['other'], { b: [2], a: 1 }])
    await nextTick()
    expect(host.querySelector<HTMLSelectElement>('select')!.value).toBe('1')
    expect(await form.value!.validate()).toEqual({ valid: true, values: { options: { item: { a: 1, b: [2] } } } })
    currentSchema.value = mapSchema([['other'], { b: [3], a: 1 }])
    await nextTick()
    expect((await form.value!.validate()).valid).toBe(false)
  })

  it('requires an explicit value for a new boolean map entry', async () => {
    const { host, form } = mountForm({ type: 'object', properties: {
      flags: { type: 'object', additionalProperties: { type: 'boolean' } },
    } }, { flags: {} })
    host.querySelector<HTMLButtonElement>('.dynform-map-add')!.click()
    await nextTick()
    const key = host.querySelector<HTMLInputElement>('input')!
    const select = host.querySelector<HTMLSelectElement>('select')!
    key.value = 'enabled'
    key.dispatchEvent(new Event('input', { bubbles: true }))
    await nextTick()
    expect(select.value).toBe('')
    expect((await form.value!.validate()).valid).toBe(false)
    select.value = 'false'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    expect(await form.value!.validate()).toEqual({ valid: true, values: { flags: { enabled: false } } })
  })
})
