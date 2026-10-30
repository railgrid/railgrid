/**
 * Provider-published page context for the shared Agents overlay.
 *
 * This is bounded, copied JSON for model context only. It grants no authority
 * and must not contain credentials, arbitrary resource contents, or commands.
 */
export type ProviderAgentJSONValue =
  | string
  | number
  | boolean
  | null
  | ProviderAgentJSONValue[]
  | { [key: string]: ProviderAgentJSONValue }

export interface ProviderAgentResource {
  apiVersion: string
  kind: string
  name: string
  resource?: string
  namespace?: string
  uid?: string
  resourceVersion?: string
}

export interface ProviderAgentContext {
  version: 1
  title: string
  summary?: string
  resources?: ProviderAgentResource[]
  data?: { [key: string]: ProviderAgentJSONValue }
}

export type PublishAgentContext = (value: ProviderAgentContext | null) => void

const MAX_UTF8_BYTES = 8192
const MAX_DEPTH = 8
const MAX_NODES = 512
const MAX_OBJECT_KEYS = 64
const MAX_ARRAY_ITEMS = 32
const MAX_STRING_BYTES = 2048
const MAX_KEY_BYTES = 128
const MAX_RESOURCES = 32
const utf8 = new TextEncoder()

const API_VERSION = /^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?(?:\/[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?)?$/
const KIND = /^[A-Z][A-Za-z0-9]{0,62}$/
const RESOURCE_NAME = /^[a-z0-9](?:[-a-z0-9.]*[a-z0-9])?$/
const RESOURCE_PLURAL = /^[a-z][a-z0-9-]{0,62}$/
const NAMESPACE = /^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$/
const OPAQUE_ID = /^[^\s\u0000-\u001f\u007f-\u009f]{1,128}$/u

const INVALID = Symbol('invalid JSON value')

type ObjectProperties = Map<string, unknown>

function plainObject(value: unknown): value is Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return false
  const prototype = Object.getPrototypeOf(value)
  return prototype === Object.prototype || prototype === null
}

/** Read only enumerable own data properties; never invoke a provider getter. */
function ownDataProperties(value: unknown, maxKeys: number): ObjectProperties | null {
  if (!plainObject(value)) return null
  const keys = Reflect.ownKeys(value)
  if (keys.length > maxKeys) return null
  const properties = new Map<string, unknown>()
  for (const key of keys) {
    if (typeof key !== 'string') return null
    const descriptor = Object.getOwnPropertyDescriptor(value, key)
    if (!descriptor || !descriptor.enumerable || !('value' in descriptor)) return null
    // UTF-8 is never shorter than the UTF-16 code-unit count. Reject huge
    // keys before TextEncoder allocates a buffer proportional to their size.
    if (key.length > MAX_KEY_BYTES || utf8.encode(key).byteLength > MAX_KEY_BYTES) return null
    properties.set(key, descriptor.value)
  }
  return properties
}

function validResource(value: unknown): ProviderAgentResource | null {
  const properties = ownDataProperties(value, 7)
  if (!properties) return null
  const allowed = new Set(['apiVersion', 'kind', 'name', 'resource', 'namespace', 'uid', 'resourceVersion'])
  if ([...properties.keys()].some(key => !allowed.has(key))) return null

  const apiVersion = properties.get('apiVersion')
  const kind = properties.get('kind')
  const name = properties.get('name')
  if (typeof apiVersion !== 'string' || apiVersion.length > 253 || !API_VERSION.test(apiVersion)) return null
  if (typeof kind !== 'string' || !KIND.test(kind)) return null
  if (typeof name !== 'string' || name.length > 253 || !RESOURCE_NAME.test(name)) return null

  const result: ProviderAgentResource = { apiVersion, kind, name }
  const optional: Array<[string, RegExp, number]> = [
    ['resource', RESOURCE_PLURAL, 63],
    ['namespace', NAMESPACE, 63],
    ['uid', OPAQUE_ID, 128],
    ['resourceVersion', OPAQUE_ID, 128],
  ]
  for (const [key, shape, maxLength] of optional) {
    if (!properties.has(key)) continue
    const field = properties.get(key)
    if (typeof field !== 'string' || field.length > maxLength || !shape.test(field)) return null
    Object.defineProperty(result, key, { value: field, enumerable: true, writable: true, configurable: true })
  }
  return result
}

function freezeJSON(value: ProviderAgentJSONValue): ProviderAgentJSONValue {
  if (value && typeof value === 'object') {
    for (const child of Array.isArray(value) ? value : Object.values(value)) freezeJSON(child)
    Object.freeze(value)
  }
  return value
}

/**
 * Normalize untrusted provider input into an immutable, bounded wire payload.
 * Invalid values return null so hosts can revoke a previously published value.
 */
export function normalizeProviderAgentContext(value: unknown): ProviderAgentContext | null {
  try {
    const properties = ownDataProperties(value, 5)
    if (!properties) return null
    const allowed = new Set(['version', 'title', 'summary', 'resources', 'data'])
    if ([...properties.keys()].some(key => !allowed.has(key))) return null
    if (properties.get('version') !== 1) return null

    const title = properties.get('title')
    if (typeof title !== 'string' || title.length > 160 || !title.trim() || /[\u0000-\u001f\u007f-\u009f]/u.test(title)) return null
    const result: ProviderAgentContext = { version: 1, title: title.trim() }

    if (properties.has('summary')) {
      const summary = properties.get('summary')
      if (typeof summary !== 'string' || summary.length > 1024 || /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f]/u.test(summary)) return null
      result.summary = summary.trim()
    }

    if (properties.has('resources')) {
      const resources = properties.get('resources')
      if (!Array.isArray(resources) || Object.getPrototypeOf(resources) !== Array.prototype || resources.length > MAX_RESOURCES) return null
      const keys = Reflect.ownKeys(resources)
      if (keys.length !== resources.length + 1 || keys.some(key => typeof key !== 'string' || (key !== 'length' && !/^(?:0|[1-9][0-9]*)$/.test(key)))) return null
      const copy: ProviderAgentResource[] = []
      for (let index = 0; index < resources.length; index++) {
        const descriptor = Object.getOwnPropertyDescriptor(resources, String(index))
        if (!descriptor || !descriptor.enumerable || !('value' in descriptor)) return null
        const resource = validResource(descriptor.value)
        if (!resource) return null
        copy.push(resource)
      }
      result.resources = copy
    }

    let nodes = 0
    const active = new WeakSet<object>()
    const copyValue = (input: unknown, depth: number): ProviderAgentJSONValue | typeof INVALID => {
      nodes++
      if (nodes > MAX_NODES || depth > MAX_DEPTH) return INVALID
      if (input === null || typeof input === 'boolean') return input
      if (typeof input === 'string') {
        // UTF-8 is never shorter than the code-unit count, so this keeps
        // untrusted oversized values from allocating an unbounded byte buffer.
        if (input.length > MAX_STRING_BYTES) return INVALID
        return utf8.encode(input).byteLength <= MAX_STRING_BYTES ? input : INVALID
      }
      if (typeof input === 'number') return Number.isFinite(input) ? input : INVALID
      if (typeof input !== 'object') return INVALID
      if (active.has(input)) return INVALID
      active.add(input)
      try {
        if (Array.isArray(input)) {
          if (Object.getPrototypeOf(input) !== Array.prototype || input.length > MAX_ARRAY_ITEMS) return INVALID
          const keys = Reflect.ownKeys(input)
          if (keys.length !== input.length + 1 || keys.some(key => typeof key !== 'string' || (key !== 'length' && !/^(?:0|[1-9][0-9]*)$/.test(key)))) return INVALID
          const copy: ProviderAgentJSONValue[] = []
          for (let index = 0; index < input.length; index++) {
            const descriptor = Object.getOwnPropertyDescriptor(input, String(index))
            if (!descriptor || !descriptor.enumerable || !('value' in descriptor)) return INVALID
            const child = copyValue(descriptor.value, depth + 1)
            if (child === INVALID) return INVALID
            copy.push(child)
          }
          return copy
        }
        const entries = ownDataProperties(input, MAX_OBJECT_KEYS)
        if (!entries) return INVALID
        const copy: { [key: string]: ProviderAgentJSONValue } = {}
        for (const [key, childValue] of entries) {
          const child = copyValue(childValue, depth + 1)
          if (child === INVALID) return INVALID
          Object.defineProperty(copy, key, { value: child, enumerable: true, writable: true, configurable: true })
        }
        return copy
      } finally {
        active.delete(input)
      }
    }

    if (properties.has('data')) {
      const data = properties.get('data')
      const copied = copyValue(data, 0)
      if (copied === INVALID || !copied || Array.isArray(copied) || typeof copied !== 'object') return null
      result.data = copied as { [key: string]: ProviderAgentJSONValue }
    }

    const serialized = JSON.stringify(result)
    if (utf8.encode(serialized).byteLength > MAX_UTF8_BYTES) return null
    if (result.resources) for (const resource of result.resources) Object.freeze(resource)
    if (result.resources) Object.freeze(result.resources)
    if (result.data) freezeJSON(result.data)
    return Object.freeze(result)
  } catch {
    // Proxies and hostile property traps are malformed input, not host errors.
    return null
  }
}
