import type { JSONSchema } from './types'

const COMPUTED_PREFIX = 'Computed by the platform'

function omittedFromCreate(schema: JSONSchema | undefined): boolean {
  return Boolean(schema?.readOnly || schema?.description?.startsWith(COMPUTED_PREFIX))
}

export function createWritableValue(schema: JSONSchema | undefined, value: unknown): unknown {
  if (omittedFromCreate(schema)) return undefined
  if (Array.isArray(value)) {
    if (!schema?.items) return [...value]
    return value
      .map(item => createWritableValue(schema.items, item))
      .filter(item => item !== undefined)
  }
  if (value && typeof value === 'object') {
    const source = value as Record<string, unknown>
    const result: Record<string, unknown> = {}
    const additionalSchema = typeof schema?.additionalProperties === 'object'
      ? schema.additionalProperties
      : undefined
    for (const [name, child] of Object.entries(source)) {
      const childSchema = schema?.properties?.[name] ?? additionalSchema
      const writable = createWritableValue(childSchema, child)
      if (writable !== undefined) result[name] = writable
    }
    return result
  }
  return value
}

export function createWritableValues(schema: JSONSchema | undefined, values: Record<string, unknown>): Record<string, unknown> {
  return createWritableValue(schema, values) as Record<string, unknown>
}
