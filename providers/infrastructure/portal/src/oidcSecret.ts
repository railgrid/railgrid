import type { JSONSchema, Template } from './types'

function object(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function field(values: Record<string, unknown>, schema: JSONSchema | undefined, key: string): unknown {
  return values[key] ?? schema?.properties?.[key]?.default
}

export function needsOIDCSecret(template: Template, values: Record<string, unknown>): boolean {
  const schema = template.inputsSchema
  const oidcSchema = schema.properties?.oidc
  if (!oidcSchema || (template.exposure === 'optional' && field(object(field(values, schema, 'expose')), schema.properties?.expose, 'enabled') !== true)) return false
  return field(object(field(values, schema, 'oidc')), oidcSchema, 'mode') === 'byo'
}

export function validSecretName(name: string): boolean {
  return name.length > 0 && name.length <= 253 && /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/.test(name)
}
