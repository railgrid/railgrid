import { describe, expect, it } from 'vitest'
import { instanceReviewHref, legacyInfrastructurePath, parseInfrastructureSubPath } from './routes'

describe('Infrastructure provider routes', () => {
  it('builds review links only from a workspace-scoped infrastructure base', () => {
    const base = '/ui/11111111-1111-1111-1111-111111111111/22222222-2222-2222-2222-222222222222/providers/infrastructure'
    expect(instanceReviewHref(base + '/', 'name/with space')).toBe(base + '/instances/name%2Fwith%20space')
    for (const invalid of [undefined, '/ui/providers/infrastructure', '//example.com', base + '?x=1', base.replace('infrastructure', 'edges')]) {
      expect(instanceReviewHref(invalid, 'name')).toBeNull()
    }
  })
  it('preserves list, direct detail, and browser-history paths from the shell', () => {
    expect(parseInfrastructureSubPath('')).toEqual({ page: 'templates' })
    expect(parseInfrastructureSubPath('/templates/')).toEqual({ page: 'templates' })
    expect(parseInfrastructureSubPath('instances')).toEqual({ page: 'instances' })
    expect(parseInfrastructureSubPath('templates/postgres%2Fha')).toEqual({ page: 'templates', id: 'postgres/ha' })
    expect(parseInfrastructureSubPath('instances/demo%20instance')).toEqual({ page: 'instances', id: 'demo instance' })
    expect(parseInfrastructureSubPath('missing-credentials')).toEqual({ page: 'missing-credentials' })
  })

  it('keeps malformed and stale shell paths safe', () => {
    expect(parseInfrastructureSubPath('templates/bad%2')).toEqual({ page: 'templates', id: 'bad%2' })
    expect(parseInfrastructureSubPath('retired-section')).toEqual({ page: 'templates' })
  })

  it('translates legacy child events without taking navigation authority', () => {
    expect(legacyInfrastructurePath('catalog')).toBe('templates')
    expect(legacyInfrastructurePath('templates')).toBe('templates')
    expect(legacyInfrastructurePath('instances')).toBe('instances')
    expect(legacyInfrastructurePath('missing-credentials')).toBe('missing-credentials')
    expect(legacyInfrastructurePath('instances/demo')).toBe('instances/demo')
  })
})
