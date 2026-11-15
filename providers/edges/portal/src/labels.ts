export function parseLabels(input: string): Record<string, string> | null {
  const out: Record<string, string> = Object.create(null)
  const part = /^[A-Za-z0-9](?:[-A-Za-z0-9_.]*[A-Za-z0-9])?$/
  const dnsPart = /^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/
  if (input.trim()) {
    for (const pair of input.split(',')) {
      const pieces = pair.split('=')
      if (pieces.length !== 2) return null
      const [k, v] = pieces.map((s) => s.trim())
      const keyParts = k.split('/')
      const name = keyParts[keyParts.length - 1]
      const prefix = keyParts.length === 2 ? keyParts[0] : null
      if (keyParts.length > 2 || !name || name.length > 63 || !part.test(name) ||
          (prefix !== null && (!prefix || prefix.length > 253 || prefix.split('.').some(p => !dnsPart.test(p)))) ||
          v.length > 63 || (v !== '' && !part.test(v)) || Object.prototype.hasOwnProperty.call(out, k)) return null
      out[k] = v
    }
  }
  return out
}
