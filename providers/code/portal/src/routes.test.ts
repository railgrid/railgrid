import { describe, expect, it } from 'vitest'
import { codeNavigationDetail, parseCodeSubPath } from './routes'

describe('Code provider routes', () => {
  it('parses provider-relative connection creation routes', () => {
    expect(parseCodeSubPath('create/connection/token')).toEqual({
      page: 'connections',
      create: { resource: 'connection', method: 'token' },
    })
    expect(parseCodeSubPath('/create/connection/github/')).toEqual({
      page: 'connections',
      create: { resource: 'connection', method: 'github' },
    })
  })

  it('parses repository creation and preserves encoded detail names', () => {
    expect(parseCodeSubPath('create/repository')).toEqual({
      page: 'repositories',
      create: { resource: 'repository' },
    })
    expect(parseCodeSubPath('repositories/acme%2Fmonorepo')).toEqual({
      page: 'repositories',
      repo: 'acme/monorepo',
    })
  })

  it('falls back safely for unknown or malformed provider-relative paths', () => {
    expect(parseCodeSubPath('create/connection/password')).toEqual({ page: 'connections' })
    expect(parseCodeSubPath('repositories/%E0%A4%A')).toEqual({ page: 'repositories', repo: '%E0%A4%A' })
  })
})

describe('Code provider navigation detail', () => {
  it('strips a provider prefix and carries replace history semantics only when requested', () => {
    expect(codeNavigationDetail('/create/repository')).toEqual({ path: 'create/repository' })
    expect(codeNavigationDetail('/repositories/orders', { replace: true })).toEqual({
      path: 'repositories/orders',
      replace: true,
    })
  })
})

