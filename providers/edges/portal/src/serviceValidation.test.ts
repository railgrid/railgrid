import { describe, expect, it } from 'vitest'
import { servicePortError } from './serviceValidation'

describe('service port validation', () => {
  it('accepts the full valid integer range', () => {
    for (const port of [1, 80, '443', 65535]) expect(servicePortError(port)).toBeNull()
  })

  it('rejects blank, zero, fractional, non-finite and out-of-range ports without substituting a default', () => {
    for (const port of ['', undefined, 0, -1, 1.5, 65536, Infinity, NaN, 'invalid']) {
      expect(servicePortError(port)).toBe('Enter a whole-number port from 1 to 65535.')
    }
  })
})
