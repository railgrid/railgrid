import { describe, expect, it } from 'vitest'
import { legacyInfrastructurePath, parseInfrastructureSubPath } from './routes'

describe('Infrastructure provider routes', () => {
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
