import { describe, expect, it } from 'vitest'
import { readRepositoryAttempt, repositoryAttemptKey, repositoryCreateRejected } from './repositoryCreateRecovery'

describe('repository create recovery', () => {
  it.each([400, 401, 403, 404, 409, 422])('recognizes HTTP %s as a rejection', status => {
    expect(repositoryCreateRejected({ reason: 'HTTPError', message: `${status}: rejected` })).toBe(true)
  })
  it.each([408, 429, 500, 502, 503, 504])('treats HTTP %s as uncertain', status => {
    expect(repositoryCreateRejected({ reason: 'HTTPError', message: `${status}: failed` })).toBe(false)
  })
  it('treats transport and protocol failures as uncertain', () => {
    expect(repositoryCreateRejected(new Error('network'))).toBe(false)
    expect(repositoryCreateRejected({ reason: 'ProtocolError', message: 'invalid response' })).toBe(false)
  })
  it('isolates and restores a saved attempt by scope', () => {
    const data = new Map<string, string>()
    const storage = { getItem: (key: string) => data.get(key) ?? null } as Storage
    const attempt = { mode: 'import', resourceName: 'repo', remote: 'owner/repo', draft: { name: 'repo', repo: 'owner/repo', connectionRef: 'connection', visibility: 'private', description: '', autoInit: true } }
    data.set(repositoryAttemptKey('a'), JSON.stringify(attempt))
    expect(readRepositoryAttempt(storage, 'a')).toEqual(attempt)
    expect(readRepositoryAttempt(storage, 'b')).toBeNull()
    data.set(repositoryAttemptKey('a'), '{invalid')
    expect(() => readRepositoryAttempt(storage, 'a')).toThrow()
  })
})
