// A resource pointer, not page contents, instructions, or an authorization grant.
// Publishers must exclude credentials, arbitrary spec/status, and form values.
export interface PageResourceHint {
  apiVersion: string
  kind: string
  resource: string
  name: string
  namespace?: string
  uid?: string
  // Provider-local read tool name; availability depends on the agent's toolsets.
  readTool?: string
}
export type PublishPageResource = (resource: PageResourceHint | null) => void

const FIELD_SHAPES: Record<string, RegExp> = {
  apiVersion: /^(?:[a-z0-9](?:[-a-z0-9.]*[a-z0-9])?\/)?v[0-9]+(?:(?:alpha|beta)[0-9]+)?$/,
  kind: /^[A-Z][A-Za-z0-9]{0,62}$/,
  resource: /^[a-z][a-z0-9-]{0,62}$/,
  name: /^[a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?$/,
  namespace: /^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$/,
  uid: /^[A-Za-z0-9-]{1,64}$/,
  readTool: /^[a-z][a-z0-9_]{0,63}$/,
}

// Bound and copy the wire shape at the host boundary. Never retain a provider's
// reactive object or forward unknown properties into the model transcript.
export function normalizePageResource(value: unknown): PageResourceHint | null {
  if (!value || typeof value !== 'object') return null
  const input = value as Record<string, unknown>
  const result: Record<string, string> = {}
  for (const key of ['apiVersion', 'kind', 'resource', 'name', 'namespace', 'uid', 'readTool']) {
    const field = input[key]
    if (field === undefined && ['namespace', 'uid', 'readTool'].includes(key)) continue
    if (typeof field !== 'string' || field.length > 253 || /[\s\u0000-\u001f\u007f]/u.test(field) || !FIELD_SHAPES[key]!.test(field)) return null
    result[key] = field
  }
  return result as unknown as PageResourceHint
}
