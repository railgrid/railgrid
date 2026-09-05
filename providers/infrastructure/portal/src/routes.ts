export interface InfrastructureRoute {
  page: 'templates' | 'instances' | 'missing-credentials'
  id?: string
}

function decodeSegment(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}

export function parseInfrastructureSubPath(sub: string | null | undefined): InfrastructureRoute {
  const path = (sub ?? '').replace(/^\/+|\/+$/g, '')
  if (path === '' || path === 'templates') return { page: 'templates' }
  if (path === 'instances') return { page: 'instances' }
  if (path === 'missing-credentials') return { page: 'missing-credentials' }
  const [head, ...rest] = path.split('/')
  if (head === 'templates' && rest.length) return { page: 'templates', id: decodeSegment(rest.join('/')) }
  if (head === 'instances' && rest.length) return { page: 'instances', id: decodeSegment(rest.join('/')) }
  return { page: 'templates' }
}

export function legacyInfrastructurePath(view: string): string {
  switch (view) {
    case 'catalog':
    case 'templates':
      return 'templates'
    case 'instances':
      return 'instances'
    case 'missing-credentials':
      return 'missing-credentials'
    default:
      return view
  }
}
