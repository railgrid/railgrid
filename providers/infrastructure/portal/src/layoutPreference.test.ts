import { describe, expect, it } from 'vitest'
import {
  DEFAULT_LAYOUT_MODE,
  isLayoutMode,
  nextLayoutMenuIndex,
  readLayoutPreference,
  writeLayoutPreference,
  type LayoutPreferenceStorage,
} from './portalkit/layoutPreference'

describe('shared layout preference', () => {
  it('defaults to grid and accepts only the sanctioned modes', () => {
    expect(DEFAULT_LAYOUT_MODE).toBe('grid')
    expect(isLayoutMode('grid')).toBe(true)
    expect(isLayoutMode('list')).toBe(true)
    expect(isLayoutMode('table')).toBe(false)
  })

  it('persists a validated preference and rejects malformed stored values', () => {
    const values = new Map<string, string>()
    const storage: LayoutPreferenceStorage = {
      getItem: key => values.get(key) ?? null,
      setItem: (key, value) => { values.set(key, value) },
    }

    expect(readLayoutPreference('layout', storage)).toBe('grid')
    values.set('layout', 'table')
    expect(readLayoutPreference('layout', storage)).toBe('grid')
    writeLayoutPreference('layout', 'list', storage)
    expect(readLayoutPreference('layout', storage)).toBe('list')
  })

  it('treats unavailable or denied storage as a non-fatal preference miss', () => {
    const deniedStorage: LayoutPreferenceStorage = {
      getItem: () => { throw new Error('denied') },
      setItem: () => { throw new Error('denied') },
    }

    expect(readLayoutPreference('layout', deniedStorage)).toBe('grid')
    expect(() => writeLayoutPreference('layout', 'list', deniedStorage)).not.toThrow()
    expect(readLayoutPreference('layout', null)).toBe('grid')
  })

  it('wraps arrow navigation and supports Home and End', () => {
    expect(nextLayoutMenuIndex('ArrowDown', 0)).toBe(1)
    expect(nextLayoutMenuIndex('ArrowDown', 1)).toBe(0)
    expect(nextLayoutMenuIndex('ArrowUp', 0)).toBe(1)
    expect(nextLayoutMenuIndex('ArrowUp', 1)).toBe(0)
    expect(nextLayoutMenuIndex('Home', 1)).toBe(0)
    expect(nextLayoutMenuIndex('End', 0)).toBe(1)
    expect(nextLayoutMenuIndex('PageDown', 0)).toBeNull()
  })
})
