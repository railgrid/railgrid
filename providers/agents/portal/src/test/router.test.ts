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

describe('edit routes', () => {
  it('parses, formats, and highlights an encoded connection edit route', () => {
    const route: Route = { kind: 'edit', resource: 'connection', name: 'team/github' }
    expect(hashFor(route)).toBe('#/connections/team%2Fgithub/edit')
    expect(parseHash('#/connections/team%2Fgithub/edit')).toEqual(route)
    expect(activeMenu(route)).toBe('connections')
  })

  it('parses, formats, and highlights a toolset edit route', () => {
    const route: Route = { kind: 'edit', resource: 'toolset', name: 'research tools' }
    expect(hashFor(route)).toBe('#/toolsets/research%20tools/edit')
    expect(parseHash('#/toolsets/research%20tools/edit')).toEqual(route)
    expect(activeMenu(route)).toBe('connections')
  })

  it('rejects extra path segments after a connection edit route', () => {
    expect(parseHash('#/connections/test/edit/extra')).toEqual({ kind: 'menu', menu: 'connections' })
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

  it('preserves ambient history state in the standalone fallback', () => {
    const hostState = {
      back: '/dashboard',
      current: '/providers/agents',
      forward: null,
      position: 7,
      replaced: false,
      scroll: null,
    }
    history.replaceState(hostState, '', '#/agents')
    const push = vi.spyOn(history, 'pushState')

    writeHash({ kind: 'menu', menu: 'activity' })

    expect(push).toHaveBeenCalledWith(hostState, '', '#/activity')
    expect(history.state).toEqual(hostState)
  })

  it('updates the current host route without changing its traversal position on replace', () => {
    const hostState = {
      back: '/providers/agents#/agents',
      current: '/providers/agents#/create/agent',
      forward: null,
      position: 8,
      replaced: false,
      scroll: null,
    }
    history.replaceState(hostState, '', '#/create/agent')
    const replace = vi.spyOn(history, 'replaceState')

    writeHash({ kind: 'agent', name: 'scout', tab: 'config' }, 'replace')

    expect(replace).toHaveBeenCalledWith(hostState, '', '#/agents/scout/config')
  })

  it('does not throw on malformed externally supplied encoded segments', () => {
    expect(parseHash('#/agents/%E0%A4%A')).toEqual({ kind: 'agent', name: '%E0%A4%A', tab: 'config' })
  })
})
