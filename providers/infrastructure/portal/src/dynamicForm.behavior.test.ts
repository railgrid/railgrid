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
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(defineComponent({
    setup: () => () => h(DynamicForm, {
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
  return { host, values }
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
})
