import { describe, expect, it } from 'vitest'
import { validateBudgetInputs } from '../budget-validation'

describe('budget validation', () => {
  it.each(['', '0', '000', '42'])('accepts whole token limits (%s)', value => {
    const result = validateBudgetInputs('', value)
    expect(result.tokenError).toBe('')
    expect(result.budgetTokens).toBe(value ? Number(value) : 0)
  })

  it.each(['-1', '1.5', 'NaN', 'Infinity', 'tokens'])('rejects malformed token limits (%s)', value => {
    expect(validateBudgetInputs('', value).tokenError).not.toBe('')
  })

  it.each(['', '0', '0.00'])('normalizes zero-dollar or blank budgets to unlimited (%s)', value => {
    const result = validateBudgetInputs(value, '')
    expect(result.usdError).toBe('')
    expect(result.budgetUSD).toBe('')
  })

  it.each(['-1', 'NaN', 'Infinity', '-Infinity', 'USD', '0x10'])('rejects invalid dollar budgets (%s)', value => {
    expect(validateBudgetInputs(value, '').usdError).not.toBe('')
  })
})
