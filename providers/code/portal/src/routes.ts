export type ConnectionCreateMethod = 'token' | 'github'

export interface CodeRoute {
  page: 'connections' | 'repositories' | 'packages'
  repo?: string
  connection?: string
  create?:
    | { resource: 'connection'; method: ConnectionCreateMethod }
    | { resource: 'repository' }
}

export interface CodeNavigationDetail {
  path: string
  replace?: boolean
}

/** Build the detail payload used by the shell's faros-navigate event. */
export function codeNavigationDetail(path: string, options: { replace?: boolean } = {}): CodeNavigationDetail {
  const detail: CodeNavigationDetail = { path: path.replace(/^\/+/, '') }
  if (options.replace) detail.replace = true
  return detail
}

function decodeSegment(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    // Keep an invalid escape visible instead of making an otherwise valid
    // provider route impossible to render.
    return value
  }
}

/**
 * Parse the provider-relative part of `/providers/code/<subPath>`.
 *
 * The shell owns the `/providers/code/` prefix and passes only its trailing
 * segments through FarosContext.subPath. Keeping this parser provider-relative
 * means links emitted by the provider work both from the bare provider landing
 * page and from a deep-linked browser refresh.
 */
export function parseCodeSubPath(subPath: string | null | undefined): CodeRoute {
  const normalized = (subPath ?? '').replace(/^\/+|\/+$/g, '')
  if (normalized === '' || normalized === 'connections') return { page: 'connections' }
  if (normalized === 'packages') return { page: 'packages' }
  if (normalized === 'repositories') return { page: 'repositories' }

  const parts = normalized.split('/')
  if (parts[0] === 'create') {
    if (parts[1] === 'connection' && (parts[2] === 'token' || parts[2] === 'github') && parts.length === 3) {
      return { page: 'connections', create: { resource: 'connection', method: parts[2] } }
    }
    if (parts[1] === 'repository' && parts.length === 2) {
      return { page: 'repositories', create: { resource: 'repository' } }
    }
    return { page: 'connections' }
  }

  if (parts[0] === 'connections' && parts.length > 1) {
    return { page: 'connections', connection: decodeSegment(parts.slice(1).join('/')) }
  }
  if (parts[0] === 'repositories' && parts.length > 1) {
    return { page: 'repositories', repo: decodeSegment(parts.slice(1).join('/')) }
  }
  return { page: 'connections' }
}
