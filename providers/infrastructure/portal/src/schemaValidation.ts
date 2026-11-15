import type { JSONSchema } from './types'

function valueType(value: unknown): string {
  if (Array.isArray(value)) return 'array'
  if (value === null) return 'null'
  return typeof value
}

// JSON Schema enum compares JSON values structurally, regardless of object key order.
export function equalJSONValue(a: unknown, b: unknown): boolean {
  if (a === b) return true
  if (Array.isArray(a) || Array.isArray(b)) {
    return Array.isArray(a) && Array.isArray(b) && a.length === b.length
      && a.every((value, index) => equalJSONValue(value, b[index]))
  }
  if (!a || !b || typeof a !== 'object' || typeof b !== 'object') return false
  const left = a as Record<string, unknown>
  const right = b as Record<string, unknown>
  const keys = Object.keys(left)
  return keys.length === Object.keys(right).length
    && keys.every(key => Object.prototype.hasOwnProperty.call(right, key) && equalJSONValue(left[key], right[key]))
}

export function validateSchemaValue(schema: JSONSchema | undefined, value: unknown, path = 'value'): string | null {
  if (!schema) return null
  const type = schema.type
  if (schema.enum && !schema.enum.some(option => equalJSONValue(option, value))) return `${path} must be one of the allowed values.`
  if (type === 'array') {
    if (!Array.isArray(value)) return `${path} must be an array.`
    if (schema.minItems !== undefined && value.length < schema.minItems) return `${path} needs at least ${schema.minItems} item(s).`
    if (schema.maxItems !== undefined && value.length > schema.maxItems) return `${path} allows at most ${schema.maxItems} item(s).`
    for (let index = 0; index < value.length; index += 1) {
      const error = validateSchemaValue(schema.items, value[index], `${path} line ${index + 1}`)
      if (error) return error
    }
    return null
  }
  if (type === 'object') {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return `${path} must be an object.`
    const record = value as Record<string, unknown>
    for (const required of schema.required ?? []) {
      const child = schema.properties?.[required]
      if (record[required] === undefined || (record[required] === '' && !child?.enum?.includes(''))) return `${path}.${required} is required.`
    }
    for (const [name, child] of Object.entries(schema.properties ?? {})) {
      if (record[name] === undefined) continue
      const error = validateSchemaValue(child, record[name], `${path}.${name}`)
      if (error) return error
    }
    if (typeof schema.additionalProperties === 'object') {
      for (const [name, childValue] of Object.entries(record)) {
        if (schema.properties?.[name]) continue
        const error = validateSchemaValue(schema.additionalProperties, childValue, `${path}.${name}`)
        if (error) return error
      }
    }
    return null
  }
  if (type === 'integer' && !Number.isInteger(value)) return `${path} must be an integer.`
  if (type === 'number' && typeof value !== 'number') return `${path} must be a number.`
  if (type === 'boolean' && typeof value !== 'boolean') return `${path} must be true or false.`
  if (type === 'string' && typeof value !== 'string') return `${path} must be a string.`
  if (typeof value === 'number') {
    if (schema.minimum !== undefined && value < schema.minimum) return `${path} must be at least ${schema.minimum}.`
    if (schema.maximum !== undefined && value > schema.maximum) return `${path} must be at most ${schema.maximum}.`
  }
  if (typeof value === 'string') {
    if (schema.minLength !== undefined && value.length < schema.minLength) return `${path} needs at least ${schema.minLength} character(s).`
    if (schema.maxLength !== undefined && value.length > schema.maxLength) return `${path} allows at most ${schema.maxLength} character(s).`
    if (schema.pattern) {
      try {
        if (!new RegExp(schema.pattern).test(value)) return `${path} does not match the required format.`
      } catch {
        return `${path} has an invalid schema pattern.`
      }
    }
  }
  if (type && type !== valueType(value) && !(type === 'integer' && typeof value === 'number')) return `${path} must be ${type}.`
  return null
}
