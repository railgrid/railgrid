import { describe, expect, it } from 'vitest'
import { createWritableValues } from './createValues'
import type { JSONSchema } from './types'

describe('createWritableValues', () => {
  it('omits forbidden unknown fields recursively and preserves allowed additional fields', () => {
    const schema: JSONSchema = {
      type: 'object', additionalProperties: false,
      properties: {
        nested: { type: 'object', additionalProperties: false, properties: { keep: { type: 'string' } } },
        rows: { type: 'array', items: { type: 'object', additionalProperties: false, properties: { keep: { type: 'string' } } } },
        labels: { type: 'object', additionalProperties: { type: 'string' } },
      },
    }
    expect(createWritableValues(schema, {
      removed: 'drop', nested: { keep: 'yes', removed: 'drop' },
      rows: [{ keep: 'yes', removed: 'drop' }], labels: { custom: 'yes' },
    })).toEqual({ nested: { keep: 'yes' }, rows: [{ keep: 'yes' }], labels: { custom: 'yes' } })
  })

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
