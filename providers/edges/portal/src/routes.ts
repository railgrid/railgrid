import type { EdgeType } from './types'

export type EdgeCollectionPage = 'edges' | 'services' | 'workloads'
export type ServiceCreateRoute = {
  resource: 'service'
  edgeType?: EdgeType
  edgeName?: string
}
export type WorkloadDeployRoute = {
  resource: 'workload'
  mode: 'manual' | 'marketplace'
  app?: string
}

export interface EdgeRoute {
  page: EdgeCollectionPage
  edge?: { type: EdgeType; name: string }
  service?: string
  connect?: { resource: 'edge'; successPath?: string; cancelPath?: string; requiredType?: EdgeType }
  create?: ServiceCreateRoute
  deploy?: WorkloadDeployRoute
}

export interface EdgeNavigationDetail {
  path: string
  replace?: boolean
}

function decodeSegment(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    // Keep malformed escapes visible so a bad deep link remains addressable and
    // the eventual resource read can report a useful not-found/error state.
    return value
  }
}

function normalizeSubPath(subPath: string | null | undefined): string {
  let normalized = (subPath ?? '').replace(/^\/+|\/+$/g, '')
  // FarosContext is provider-relative, but accepting the shell prefix here is
  // harmless and makes standalone/debug harness context easier to diagnose.
  if (normalized === 'providers/edges') return ''
  if (normalized.startsWith('providers/edges/')) normalized = normalized.slice('providers/edges/'.length)
  return normalized
}

function hasEncodedDotSegment(path: string): boolean {
  let candidate = path
  for (let depth = 0; depth < 5; depth += 1) {
    if (candidate.includes('\\')) return true
    if (candidate.split('/').some(segment => segment === '.' || segment === '..')) return true
    let decoded: string
    try {
      decoded = decodeURIComponent(candidate)
    } catch {
      // A malformed escape makes the callback ambiguous to downstream URL
      // parsers. Fail closed instead of letting it hide encoded dot segments.
      return true
    }
    if (decoded === candidate) return false
    candidate = decoded
  }
  // Excessive nested encoding is not produced by provider navigation helpers;
  // reject it instead of passing an ambiguous callback to the shell router.
  return true
}

/** Parse the provider-relative path after `/providers/edges/`. */
export function parseSubPath(subPath: string | null | undefined): EdgeRoute {
  const normalized = normalizeSubPath(subPath)
  const parts = normalized ? normalized.split('/') : []

  if (normalized === '' || normalized === 'edges') return { page: 'edges' }
  if (normalized === 'services') return { page: 'services' }
  if (normalized === 'workloads') return { page: 'workloads' }

  if (parts[0] === 'connect' && parts[1] === 'edge') {
    if (parts.length === 2) return { page: 'edges', connect: { resource: 'edge' } }
    const hasRequiredType = parts[2] === 'kubernetes' || parts[2] === 'server'
    const actionIndex = hasRequiredType ? 3 : 2
    const requestedType = hasRequiredType ? parts[2] as EdgeType : undefined
    let successPath: string | undefined
    let cancelPath: string | undefined
    // Keep legacy `return` links refresh-safe. New links persist separate
    // success and cancel destinations so prerequisite onboarding can resume a
    // form on success without sending Cancel back into that form.
    if (parts[actionIndex] === 'return' && parts.length === actionIndex + 2 && parts[actionIndex + 1] !== '') {
      successPath = decodeSegment(parts[actionIndex + 1])
      cancelPath = successPath
    } else if (
      parts[actionIndex] === 'success' && parts[actionIndex + 1] !== '' &&
      parts[actionIndex + 2] === 'cancel' && parts[actionIndex + 3] !== '' &&
      parts.length === actionIndex + 4
    ) {
      successPath = decodeSegment(parts[actionIndex + 1])
      cancelPath = decodeSegment(parts[actionIndex + 3])
    }
    if (successPath && cancelPath) {
      if (hasEncodedDotSegment(successPath) || hasEncodedDotSegment(cancelPath)) {
        return { page: 'edges', connect: { resource: 'edge' } }
      }
      const successRoute = parseSubPath(successPath)
      const normalizedSuccessPath = normalizeSubPath(successPath)
      const normalizedCancelPath = normalizeSubPath(cancelPath)
      const validCancelPath = normalizedCancelPath === normalizedSuccessPath ||
        (successRoute.create?.resource === 'service' && normalizedCancelPath === 'services') ||
        (successRoute.deploy?.resource === 'workload' && normalizedCancelPath === 'workloads')
      if (validCancelPath && (successRoute.create?.resource === 'service' || successRoute.deploy?.resource === 'workload')) {
        if (successRoute.deploy?.resource === 'workload' && requestedType && requestedType !== 'kubernetes') {
          return { page: 'edges', connect: { resource: 'edge' } }
        }
        const requiredType = successRoute.deploy?.resource === 'workload' ? 'kubernetes' : requestedType
        return {
          page: 'edges',
          connect: {
            resource: 'edge',
            successPath: normalizedSuccessPath,
            cancelPath: normalizedCancelPath,
            ...(requiredType ? { requiredType } : {}),
          },
        }
      }
    }
    return { page: 'edges', connect: { resource: 'edge' } }
  }

  if (parts[0] === 'create' && parts[1] === 'service') {
    if (parts.length === 2) return { page: 'services', create: { resource: 'service' } }
    if (
      parts.length >= 4 &&
      (parts[2] === 'kubernetes' || parts[2] === 'server') &&
      parts[3] !== ''
    ) {
      return {
        page: 'services',
        create: {
          resource: 'service',
          edgeType: parts[2],
          edgeName: decodeSegment(parts.slice(3).join('/')),
        },
      }
    }
    return { page: 'services' }
  }

  if (parts[0] === 'deploy' && parts[1] === 'workload') {
    if (parts.length === 3 && parts[2] === 'manual') {
      return { page: 'workloads', deploy: { resource: 'workload', mode: 'manual' } }
    }
    if (parts.length === 4 && parts[2] === 'marketplace' && parts[3] !== '') {
      return {
        page: 'workloads',
        deploy: { resource: 'workload', mode: 'marketplace', app: decodeSegment(parts.slice(3).join('/')) },
      }
    }
    return { page: 'workloads' }
  }

  if (parts[0] === 'edges' && parts.length >= 3 && (parts[1] === 'kubernetes' || parts[1] === 'server')) {
    return {
      page: 'edges',
      edge: { type: parts[1], name: decodeSegment(parts.slice(2).join('/')) },
    }
  }
  if (parts[0] === 'services' && parts.length > 1) {
    return { page: 'services', service: decodeSegment(parts.slice(1).join('/')) }
  }

  // Unknown provider paths land on the default collection rather than leaving
  // a stale action surface mounted.
  return { page: 'edges' }
}

// The explicit alias keeps the provider name visible at call sites and gives
// route tests a stable, discoverable parser name.
export const parseEdgesSubPath = parseSubPath

export function navigationDetail(path: string, replace = false): EdgeNavigationDetail {
  const detail: EdgeNavigationDetail = { path: path.replace(/^\/+/, '') }
  if (replace) detail.replace = true
  return detail
}

export function edgeDetailPath(type: EdgeType, name: string): string {
  return `edges/${type}/${encodeURIComponent(name)}`
}

export interface EdgeConnectOptions {
  requiredType?: EdgeType
  cancelPath?: string
}

export function edgeConnectPath(successPath?: string, options: EdgeConnectOptions = {}): string {
  if (!successPath) return 'connect/edge'
  const typeSegment = options.requiredType ? `/${options.requiredType}` : ''
  const cancelPath = options.cancelPath ?? successPath
  return `connect/edge${typeSegment}/success/${encodeURIComponent(successPath)}/cancel/${encodeURIComponent(cancelPath)}`
}

export function edgeConnectionCancelPath(cancelPath?: string): string {
  return cancelPath ?? ''
}

export function edgeConnectionSuccessPath(successPath: string | undefined, type: EdgeType, name: string): string {
  return successPath ?? edgeDetailPath(type, name)
}

export function serviceDetailPath(name: string): string {
  return `services/${encodeURIComponent(name)}`
}

export function serviceCreatePath(edgeType?: EdgeType, edgeName?: string): string {
  if (edgeType && edgeName) return `create/service/${edgeType}/${encodeURIComponent(edgeName)}`
  return 'create/service'
}

export function workloadDeployPath(mode: 'manual' | 'marketplace', app?: string): string {
  return mode === 'marketplace' && app
    ? `deploy/workload/marketplace/${encodeURIComponent(app)}`
    : 'deploy/workload/manual'
}
