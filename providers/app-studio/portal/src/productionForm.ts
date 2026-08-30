export type JSONSchemaValue = string | number | boolean | null

export interface JSONSchema {
  type?: string
  title?: string
  description?: string
  properties?: Record<string, JSONSchema>
  required?: string[]
  enum?: JSONSchemaValue[]
  items?: JSONSchema
  default?: unknown
  minimum?: number
  maximum?: number
  minLength?: number
  maxLength?: number
  pattern?: string
  minItems?: number
  maxItems?: number
  additionalProperties?: boolean | JSONSchema
}

export type ProductionFormValues = Record<string, unknown>

export interface ProductionValidationError {
  path: string
  message: string
}

export type ProductionValidationErrors = Record<string, string[]>

const ALWAYS_PLATFORM_OWNED = new Set([
  'name',
  'farosMode',
  'farosRedeployRevision',
  'farosCluster',
  'credentialsSecretName',
])

// App Studio publishing owns the template-native access field. Keeping it out
// of the generic production form gives access one user-facing owner (Share)
// and prevents a redeploy from silently changing who can reach the app.
const PUBLISHING_OWNED = new Set(['access'])

function clone<T>(value: T): T {
  if (value === undefined || value === null) return value
  if (typeof structuredClone === 'function') return structuredClone(value)
  return JSON.parse(JSON.stringify(value)) as T
}

function hasOwn(values: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(values, key)
}

export function isPlatformOwnedField(name: string, schema: JSONSchema, imageInputs: string[] = []): boolean {
  if (ALWAYS_PLATFORM_OWNED.has(name) || PUBLISHING_OWNED.has(name) || imageInputs.includes(name)) return true
  return /^computed by the platform\b/i.test(schema.description?.trim() ?? '')
}

export function visibleProductionProperties(
  schema: JSONSchema | null | undefined,
  imageInputs: string[] = [],
): Array<[string, JSONSchema]> {
  if (!schema?.properties) return []
  return Object.entries(schema.properties).filter(([name, field]) => !isPlatformOwnedField(name, field, imageInputs))
}

function normalizeObject(
  schema: JSONSchema,
  existing: unknown,
  imageInputs: string[],
): unknown {
  if (schema.type === 'array') {
    const source = Array.isArray(existing)
      ? existing
      : Array.isArray(schema.default) ? schema.default : []
    return schema.items ? source.map((item) => normalizeObject(schema.items!, item, imageInputs)) : clone(source)
  }
  if (schema.type !== 'object') return clone(existing)
  if (schema.properties) {
    const source = existing && typeof existing === 'object' && !Array.isArray(existing)
      ? existing as Record<string, unknown>
      : {}
    const result: Record<string, unknown> = {}
    for (const [name, field] of visibleProductionProperties(schema, imageInputs)) {
      if (field.type === 'object' && field.properties) {
        const nested = normalizeObject(field, source[name], imageInputs)
        if (hasOwn(source, name) || field.default !== undefined || (nested && typeof nested === 'object' && Object.keys(nested as object).length > 0)) {
          result[name] = nested
        }
      } else if (hasOwn(source, name)) {
        result[name] = normalizeObject(field, source[name], imageInputs)
      } else if (field.default !== undefined) {
        result[name] = clone(field.default)
      }
    }
    return result
  }
  if (existing && typeof existing === 'object' && !Array.isArray(existing)) return clone(existing)
  if (schema.default !== undefined) return clone(schema.default)
  return {}
}

/**
 * Hydrates only tenant-editable fields from a Template schema. Existing values
 * win over defaults so a status refresh/redeploy never resets a saved input.
 */
export function productionFormValuesFromSchema(
  schema: JSONSchema | null | undefined,
  existing: Record<string, unknown> | null | undefined,
  imageInputs: string[] = [],
): ProductionFormValues {
  if (!schema) return {}
  const result = normalizeObject(schema, existing ?? {}, imageInputs)
  return result && typeof result === 'object' && !Array.isArray(result)
    ? result as ProductionFormValues
    : {}
}

export function fieldLabel(name: string): string {
  const spaced = name
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/[-_]+/g, ' ')
  return spaced.charAt(0).toUpperCase() + spaced.slice(1)
}

export function fieldID(path: string): string {
  return `production-input-${path.replace(/[^a-zA-Z0-9_-]/g, '-')}`
}

/**
 * Builds the DOM id for a field rendered by a recursive ProductionForm.
 * The prefix is part of the identity: two sibling objects may both expose a
 * field named `size`, but their labels and descriptions must target different
 * controls.
 */
export function productionFieldID(pathPrefix: string, path: string | string[]): string {
  const suffix = Array.isArray(path) ? path.join('.') : path
  return fieldID([pathPrefix, suffix].filter(Boolean).join('.'))
}

export function renameMapKey(values: Record<string, unknown>, oldKey: string, newKey: string): Record<string, unknown> {
  newKey = newKey.trim()
  if (!newKey || oldKey === newKey || (hasOwn(values, newKey) && newKey !== oldKey)) return { ...values }
  const next: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(values)) {
    next[key === oldKey ? newKey : key] = value
  }
  return next
}

export function arrayInputValue(value: unknown): string {
  if (!Array.isArray(value)) return ''
  return value.map((item) => typeof item === 'string' ? item : JSON.stringify(item)).join('\n')
}

export function arrayInputValues(raw: string, itemSchema?: JSONSchema, imageInputs: string[] = []): unknown[] {
  const lines = raw.split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
  return lines.map((line) => {
    let value: unknown
    switch (itemSchema?.type) {
      case 'integer':
        value = Number(line)
        break
      case 'number':
        value = Number(line)
        break
      case 'boolean':
        if (line.toLowerCase() === 'true') return true
        if (line.toLowerCase() === 'false') return false
        return line
      case 'object':
        try {
          value = JSON.parse(line) as unknown
        } catch {
          return line
        }
        break
      default:
        return line
    }
    if (itemSchema && value && typeof value === 'object') return normalizeObject(itemSchema, value, imageInputs)
    return value
  })
}

function missing(value: unknown): boolean {
  return value === undefined || value === null || (typeof value === 'string' && value.trim() === '')
}

function pathLabel(path: string[]): string {
  return path.length ? path.join('.') : 'production settings'
}

function addValidationError(errors: ProductionValidationError[], path: string[], message: string): void {
  errors.push({ path: path.join('.'), message })
}

function enumContains(values: JSONSchemaValue[] | undefined, value: unknown): boolean {
  return !!values?.some((candidate) => Object.is(candidate, value))
}

function validateProductionNode(
  schema: JSONSchema,
  value: unknown,
  path: string[],
  required: boolean,
  errors: ProductionValidationError[],
): void {
  if (missing(value)) {
    if (required) addValidationError(errors, path, `${pathLabel(path)} is required.`)
    return
  }

  if (schema.enum?.length && !enumContains(schema.enum, value)) {
    addValidationError(errors, path, `${pathLabel(path)} must be one of: ${schema.enum.map(String).join(', ')}.`)
    return
  }

  switch (schema.type) {
    case 'object': {
      if (typeof value !== 'object' || Array.isArray(value)) {
        addValidationError(errors, path, `${pathLabel(path)} must be an object.`)
        return
      }
      const objectValue = value as Record<string, unknown>
      if (schema.properties) {
        const requiredFields = new Set(schema.required ?? [])
        for (const [name, field] of visibleProductionProperties(schema)) {
          validateProductionNode(field, objectValue[name], [...path, name], requiredFields.has(name), errors)
        }
      } else if (schema.additionalProperties && typeof schema.additionalProperties === 'object') {
        for (const [name, item] of Object.entries(objectValue)) {
          if (!name.trim()) addValidationError(errors, path, `${pathLabel(path)} keys cannot be empty.`)
          validateProductionNode(schema.additionalProperties, item, [...path, name], true, errors)
        }
      }
      return
    }
    case 'array': {
      if (!Array.isArray(value)) {
        addValidationError(errors, path, `${pathLabel(path)} must be a list.`)
        return
      }
      if (schema.minItems !== undefined && value.length < schema.minItems) {
        addValidationError(errors, path, `${pathLabel(path)} must contain at least ${schema.minItems} value${schema.minItems === 1 ? '' : 's'}.`)
      }
      if (schema.maxItems !== undefined && value.length > schema.maxItems) {
        addValidationError(errors, path, `${pathLabel(path)} must contain no more than ${schema.maxItems} values.`)
      }
      if (schema.items) {
        value.forEach((item, index) => validateProductionNode(schema.items as JSONSchema, item, [...path, String(index)], true, errors))
      }
      return
    }
    case 'string': {
      if (typeof value !== 'string') {
        addValidationError(errors, path, `${pathLabel(path)} must be text.`)
        return
      }
      if (schema.minLength !== undefined && value.length < schema.minLength) {
        addValidationError(errors, path, `${pathLabel(path)} must be at least ${schema.minLength} characters.`)
      }
      if (schema.maxLength !== undefined && value.length > schema.maxLength) {
        addValidationError(errors, path, `${pathLabel(path)} must be no more than ${schema.maxLength} characters.`)
      }
      if (schema.pattern) {
        try {
          if (!new RegExp(schema.pattern).test(value)) addValidationError(errors, path, `${pathLabel(path)} has an invalid format.`)
        } catch {
          // A malformed provider pattern is a schema defect; do not make the
          // user's form impossible to submit because of an invalid regex.
        }
      }
      return
    }
    case 'number':
    case 'integer': {
      if (typeof value !== 'number' || !Number.isFinite(value) || (schema.type === 'integer' && !Number.isInteger(value))) {
        addValidationError(errors, path, `${pathLabel(path)} must be a ${schema.type}.`)
        return
      }
      if (schema.minimum !== undefined && value < schema.minimum) addValidationError(errors, path, `${pathLabel(path)} must be at least ${schema.minimum}.`)
      if (schema.maximum !== undefined && value > schema.maximum) addValidationError(errors, path, `${pathLabel(path)} must be no more than ${schema.maximum}.`)
      return
    }
    case 'boolean':
      if (typeof value !== 'boolean') addValidationError(errors, path, `${pathLabel(path)} must be true or false.`)
      return
    default:
      return
  }
}

/** Validates only tenant-editable values against the selected Template schema. */
export function validateProductionValues(
  schema: JSONSchema | null | undefined,
  values: Record<string, unknown> | null | undefined,
  imageInputs: string[] = [],
): ProductionValidationError[] {
  if (!schema) return []
  const filteredSchema: JSONSchema = schema.properties
    ? { ...schema, properties: Object.fromEntries(visibleProductionProperties(schema, imageInputs)) }
    : schema
  const errors: ProductionValidationError[] = []
  validateProductionNode(filteredSchema, values ?? {}, [], false, errors)
  return errors
}

export function productionValidationErrorsByPath(errors: ProductionValidationError[]): ProductionValidationErrors {
  return errors.reduce<ProductionValidationErrors>((grouped, error) => {
    const path = error.path
    grouped[path] = [...(grouped[path] ?? []), error.message]
    return grouped
  }, {})
}
