import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { describe, expect, it } from 'vitest'

import DynamicForm from './components/DynamicForm.vue'

describe('DynamicForm field identity', () => {
  it('keeps recursive paths and sanitized names unique and associated', async () => {
    const html = await renderToString(createSSRApp(DynamicForm, {
      schema: {
        type: 'object',
        properties: {
          'a-b': { type: 'string', description: 'hyphen description' },
          'a b': { type: 'string', description: 'space description' },
          database: {
            type: 'object',
            properties: {
              size: { type: 'string', description: 'database size description' },
            },
          },
          cache: {
            type: 'object',
            properties: {
              size: { type: 'string', description: 'cache size description' },
            },
          },
        },
      },
      values: {},
    }))

    const ids = [...html.matchAll(/<(?:input|select)[^>]*\sid="([^"]+)"/g)].map(match => match[1])
    expect(ids).toHaveLength(4)
    expect(new Set(ids).size).toBe(ids.length)
    expect(ids.filter(id => id.includes('database')).length).toBe(1)
    expect(ids.filter(id => id.includes('cache')).length).toBe(1)

    for (const id of ids) {
      expect(html).toContain(`for="${id}"`)
      expect(html).toContain(`aria-describedby="${id}-description"`)
      expect(html).toContain(`id="${id}-description"`)
    }
  })

  it('uses the shared native checkbox recipe for boolean inputs', async () => {
    const html = await renderToString(createSSRApp(DynamicForm, {
      schema: {
        type: 'object',
        properties: {
          enabled: { type: 'boolean', description: 'Enable this template' },
        },
      },
      values: { enabled: true },
    }))

    expect(html).toMatch(/<input[^>]*class="k-checkbox"[^>]*type="checkbox"/)
    expect(html).toContain('checked')
  })

  it('renders line arrays, map rows, and complex array fallback while omitting read-only fields', async () => {
    const html = await renderToString(createSSRApp(DynamicForm, {
      schema: {
        type: 'object',
        required: ['domains', 'settings'],
        properties: {
          domains: {
            type: 'array',
            items: { type: 'string' },
            minItems: 1,
            description: 'Allowed domains',
          },
          settings: {
            type: 'object',
            additionalProperties: { type: 'string' },
            description: 'Environment settings',
          },
          rules: {
            type: 'array',
            items: { type: 'object', properties: { path: { type: 'string' } } },
          },
          database: {
            type: 'object',
            properties: {
              version: { type: 'string', pattern: '^[0-9]+$', minLength: 2, maxLength: 2 },
            },
          },
          generated: { type: 'string', readOnly: true },
        },
      },
      values: { domains: ['example.com'], settings: { MODE: 'prod' }, generated: 'server-owned' },
    }))

    expect(html.match(/<textarea/g)).toHaveLength(2)
    expect(html).toContain('required')
    expect(html).toContain('Enter one item per line.')
    expect(html).toContain('Add entry')
    expect(html).toContain('Key for settings')
    expect(html).toContain('Value for settings key MODE')
    expect(html).toContain('Remove settings key MODE')
    expect(html).not.toContain('server-owned')
    expect(html).not.toContain('generated')
    expect(html).not.toContain('readonly')
    expect(html).toContain('pattern="^[0-9]+$"')
    expect(html).toContain('minlength="2"')
    expect(html).toContain('maxlength="2"')
    expect(html).toContain('example.com')
  })
})
