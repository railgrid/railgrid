import { describe, expect, it } from 'vitest'
import { clearProjectCreateUncertainty, createProjectOperationFence, markProjectCreateUncertain, readProjectCreateUncertainty } from '../src/projectCreateUncertainty'

function memoryStorage() {
  const values = new Map<string, string>()
  return { values, getItem: (k: string) => values.get(k) ?? null, setItem: (k: string, v: string) => { values.set(k, v) }, removeItem: (k: string) => { values.delete(k) } }
}
const identity = { tenant: 'tenant', orgUUID: 'org', workspaceUUID: 'workspace', user: 'alice' }

describe('project creation uncertainty', () => {
  it('retains only a scoped marker across reads until acknowledged', () => {
    const store = memoryStorage()
    expect(markProjectCreateUncertain(identity, 'attempt-a', store)).toBe(true)
    expect(readProjectCreateUncertainty(identity, store)).toBe('uncertain')
    expect(readProjectCreateUncertainty({ ...identity, user: 'bob' }, store)).toBe('clear')
    expect(readProjectCreateUncertainty({ ...identity, workspaceUUID: 'other' }, store)).toBe('clear')
    expect([...store.values.values()]).toEqual(['attempt-a'])
    expect(markProjectCreateUncertain(identity, 'attempt-b', store)).toBe(true)
    expect(clearProjectCreateUncertainty(identity, 'attempt-a', store)).toBe(false)
    expect(readProjectCreateUncertainty(identity, store)).toBe('uncertain')
    expect(clearProjectCreateUncertainty(identity, 'attempt-b', store)).toBe(true)
    expect(readProjectCreateUncertainty(identity, store)).toBe('clear')
  })
  it('does not treat malformed marker or inaccessible storage as a safe outcome', () => {
    const store = memoryStorage()
    markProjectCreateUncertain(identity, 'attempt-a', store)
    for (const name of store.values.keys()) store.values.set(name, 'invalid')
    expect(readProjectCreateUncertainty(identity, store)).toBe('uncertain')
    const blocked = { getItem() { throw Error() }, setItem() { throw Error() }, removeItem() { throw Error() } }
    expect(readProjectCreateUncertainty(identity, blocked)).toBe('unavailable')
    expect(markProjectCreateUncertain(identity, 'attempt-a', blocked)).toBe(false)
    expect(clearProjectCreateUncertainty(identity, 'attempt-a', blocked)).toBe(false)
    expect(markProjectCreateUncertain({ ...identity, user: '' }, 'attempt-a', store)).toBe(false)
  })
})

it('invalidates a detached import even after returning to the same scope', () => {
  const fence = createProjectOperationFence()
  const first = fence.begin()
  expect(fence.owns(first)).toBe(true)
  fence.invalidate()
  const second = fence.begin()
  expect(fence.owns(first)).toBe(false)
  expect(fence.owns(second)).toBe(true)
  fence.invalidate()
  expect(fence.owns(second)).toBe(false)
})
