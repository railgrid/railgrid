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
})
