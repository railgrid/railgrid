import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  activeMenu,
  hashFor,
  parseHash,
  syncHash,
  writeHash,
  type Route,
} from '../router'

beforeEach(() => {
  history.replaceState(null, '', '#/agents')
})

describe('create routes', () => {
  it.each<[string, Route]>([
    ['#/create/agent', { kind: 'create', resource: 'agent' }],
    ['#/create/connection', { kind: 'create', resource: 'connection' }],
    ['#/create/connection/github', { kind: 'create', resource: 'connection', type: 'github' }],
    ['#/create/toolset', { kind: 'create', resource: 'toolset' }],
    ['#/create/model', { kind: 'create', resource: 'model' }],
  ])('parses and formats %s', (hash, route) => {
    expect(parseHash(hash)).toEqual(route)
    expect(hashFor(route)).toBe(hash)
  })

  it('keeps create routes highlighted under their owning menu', () => {
    expect(activeMenu({ kind: 'create', resource: 'agent' })).toBe('agents')
    expect(activeMenu({ kind: 'create', resource: 'connection' })).toBe('connections')
    expect(activeMenu({ kind: 'create', resource: 'toolset' })).toBe('connections')
    expect(activeMenu({ kind: 'create', resource: 'model' })).toBe('models')
  })
})

describe('hash history writes', () => {
  it('pushes ordinary navigation and replaces only an explicit terminal transition', () => {
    const push = vi.spyOn(history, 'pushState')
    const replace = vi.spyOn(history, 'replaceState')

    writeHash({ kind: 'create', resource: 'agent' }, 'push')
    expect(location.hash).toBe('#/create/agent')
    expect(push).toHaveBeenCalledWith(null, '', '#/create/agent')

    writeHash({ kind: 'menu', menu: 'agents' }, 'replace')
    expect(location.hash).toBe('#/agents')
    expect(replace).toHaveBeenCalledWith(null, '', '#/agents')
    expect(push).toHaveBeenCalledTimes(1)
  })

  it('does not add a second entry when canonicalizing an unchanged hash', () => {
    const replace = vi.spyOn(history, 'replaceState')
    replace.mockClear()
    syncHash({ kind: 'menu', menu: 'agents' })
    expect(replace).not.toHaveBeenCalled()
  })

  it('does not throw on malformed externally supplied encoded segments', () => {
    expect(parseHash('#/agents/%E0%A4%A')).toEqual({ kind: 'agent', name: '%E0%A4%A', tab: 'config' })
  })
})
