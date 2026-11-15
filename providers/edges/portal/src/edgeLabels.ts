type LabelPairResult = { value: Record<string, string>; error: string | null }

/** Parse the key=value syntax used for Edge labels and workload selectors. */
export function parseEdgeLabelPairs(input: string, kind: 'label' | 'selector'): LabelPairResult {
  const out: Record<string, string> = {}
  if (!input.trim()) return { value: out, error: null }
  const namePattern = /^[A-Za-z0-9](?:[-A-Za-z0-9_.]*[A-Za-z0-9])?$/
  const prefixPattern = /^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?(?:\.[a-z0-9](?:[-a-z0-9]*[a-z0-9])?)*$/
  for (const pair of input.split(',')) {
    const parts = pair.split('=')
    if (parts.length !== 2) return { value: {}, error: 'Use key=value pairs separated by commas.' }
    const [key, value] = parts.map(part => part.trim())
    if (kind === 'selector' && (!key || !value)) return { value: {}, error: 'Selector keys and values cannot be empty.' }
    const segments = key.split('/')
    const labelName = segments[segments.length - 1]
    const prefix = segments.length === 2 ? segments[0] : ''
    if (segments.length > 2 || (segments.length === 2 && (!prefix || prefix.length > 253 || !prefixPattern.test(prefix)))) {
      return { value: {}, error: 'Use a lowercase DNS prefix followed by /, or omit the prefix.' }
    }
    if (!labelName || labelName.length > 63 || !namePattern.test(labelName)) {
      return { value: {}, error: 'Use a label name of up to 63 letters, numbers, hyphens, underscores, or dots; start and end with a letter or number.' }
    }
    if (value.length > 63 || (value && !namePattern.test(value))) {
      return { value: {}, error: kind === 'label'
        ? 'Use up to 63 letters, numbers, hyphens, underscores, or dots for the value; start and end with a letter or number, or leave it empty.'
        : 'Use up to 63 letters, numbers, hyphens, underscores, or dots for the value; start and end with a letter or number.' }
    }
    if (Object.hasOwn(out, key)) return { value: {}, error: kind === 'label' ? `Label “${key}” is listed more than once.` : `Selector key "${key}" is listed more than once.` }
    out[key] = value
  }
  return { value: out, error: null }
}
