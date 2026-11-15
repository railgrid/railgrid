import { describe, expect, it } from 'vitest'
import { needsOIDCSecret, validSecretName } from './oidcSecret'
import type { Template } from './types'

const template = (exposure: Template['exposure'], mode: string): Template => ({
  name: 'app', displayName: 'App', description: '', kind: 'App', exposure,
  inputsSchema: { properties: {
    oidc: { type: 'object', default: {}, properties: { mode: { default: mode } } },
    expose: { type: 'object', default: {}, properties: { enabled: { default: false } } },
  } },
})

describe('OIDC Secret reference', () => {
  it('follows mode and optional exposure defaults', () => {
    expect(needsOIDCSecret(template('public', 'none'), {})).toBe(false)
    expect(needsOIDCSecret(template('public', 'none'), { oidc: { mode: 'byo' } })).toBe(true)
    expect(needsOIDCSecret(template('optional', 'byo'), {})).toBe(false)
    expect(needsOIDCSecret(template('optional', 'byo'), { expose: { enabled: true } })).toBe(true)
    expect(needsOIDCSecret(template('optional', 'byo'), { expose: { enabled: false }, oidc: { mode: 'byo' } })).toBe(false)
  })
  it('validates the API SecretReference name contract', () => {
    expect(validSecretName('my.oidc-secret')).toBe(true)
    expect(validSecretName('')).toBe(false)
    expect(validSecretName('UPPER')).toBe(false)
    expect(validSecretName('a..b')).toBe(false)
    expect(validSecretName('a.-b')).toBe(false)
    expect(validSecretName('a-.b')).toBe(false)
    expect(validSecretName('a'.repeat(254))).toBe(false)
  })
})
