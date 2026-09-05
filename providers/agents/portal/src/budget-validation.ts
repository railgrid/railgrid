export interface BudgetValidationResult {
  budgetUSD: string
  budgetTokens: number
  usdError: string
  tokenError: string
}

// The portal mirrors the provider's write contract so invalid drafts never
// become requests. The server remains authoritative and applies the same rules
// to REST and MCP callers.
export function validateBudgetInputs(usdInput: string, tokenInput: string): BudgetValidationResult {
  const usd = usdInput.trim()
  const tokens = tokenInput.trim()
  let usdError = ''
  let tokenError = ''
  let budgetUSD = ''
  let budgetTokens = 0

  if (usd) {
    // Match strconv.ParseFloat's decimal/exponent forms without accepting
    // JavaScript-only numeric syntax such as hexadecimal literals.
    const decimal = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/
    const parsed = Number(usd)
    if (!decimal.test(usd) || !Number.isFinite(parsed) || parsed < 0) {
      usdError = 'Enter a finite amount of zero or more, or leave this blank for unlimited.'
    } else if (parsed > 0) {
      budgetUSD = usd
    }
  }

  if (tokens) {
    if (!/^\d+$/.test(tokens)) {
      tokenError = 'Enter a whole number of zero or more, or leave this blank for unlimited.'
    } else {
      const parsed = Number(tokens)
      if (!Number.isSafeInteger(parsed)) {
        tokenError = 'Enter a whole number within the supported range.'
      } else {
        budgetTokens = parsed
      }
    }
  }

  return { budgetUSD, budgetTokens, usdError, tokenError }
}
