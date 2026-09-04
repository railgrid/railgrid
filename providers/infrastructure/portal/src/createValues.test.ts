import { describe, expect, it } from 'vitest'
import { createWritableValues } from './createValues'
import type { JSONSchema } from './types'

describe('createWritableValues', () => {
  it('recursively omits read-only and platform-computed values', () => {
    const schema: JSONSchema = {
      type: 'object',
      properties: {
        name: { type: 'string' },
        generatedID: { type: 'string', readOnly: true },
        nested: {
          type: 'object',
          properties: {
            userValue: { type: 'string' },
            serverValue: { type: 'string', readOnly: true },
          },
        },
        rows: {
          type: 'array',
          items: {
            type: 'object',
            properties: {
              label: { type: 'string' },
              computed: { type: 'string', description: 'Computed by the platform after provisioning.' },
            },
          },
        },
      },
    }

    expect(createWritableValues(schema, {
      name: 'demo',
      generatedID: 'server-owned',
      nested: { userValue: 'keep', serverValue: 'drop' },
      rows: [{ label: 'keep', computed: 'drop' }],
    })).toEqual({
      name: 'demo',
      nested: { userValue: 'keep' },
      rows: [{ label: 'keep' }],
    })
  })
})
