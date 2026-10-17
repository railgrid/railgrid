export interface ReasoningCatalogEntry {
  id: string
  aliases?: string[]
  efforts: string[]
  responsesForReasoning?: boolean
}

export interface ReasoningModelConnection {
  provider?: string
  baseURL?: string
  model: string
}

export const OPENAI_API_BASE_URL = 'https://api.openai.com/v1'

function normalizedBaseURL(value: string | undefined): string {
  const raw = (value ?? '').trim() || OPENAI_API_BASE_URL
  if (/[?#]/.test(raw)) return ''
  const authorityMatch = raw.match(/^https:\/\/([^/?#]*)/i)
  const rawAuthority = authorityMatch?.[1] ?? ''
  if (!/^(?:api\.openai\.com|api\.openai\.com:443)$/i.test(rawAuthority)) return ''
  const rawPath = authorityMatch ? raw.slice(authorityMatch[0].length) || '/' : ''
  if (rawPath !== '/v1' && rawPath !== '/v1/') return ''
  let parsed: URL
  try {
    parsed = new URL(raw)
  } catch {
    return ''
  }
  if (
    parsed.protocol !== 'https:' ||
    parsed.hostname.toLowerCase() !== 'api.openai.com' ||
    (parsed.port !== '' && parsed.port !== '443') ||
    parsed.username !== '' || parsed.password !== '' ||
    parsed.search !== '' || parsed.hash !== '' ||
    (parsed.pathname !== '/v1' && parsed.pathname !== '/v1/')
  ) return ''
  return OPENAI_API_BASE_URL
}

export function normalizedReasoningModelID(value: string): string {
  return value.trim().toLowerCase()
}

export function lookupReasoningModel(
  model: string,
  catalog: readonly ReasoningCatalogEntry[],
): ReasoningCatalogEntry | undefined {
  const normalized = normalizedReasoningModelID(model)
  if (!normalized) return undefined
  return catalog.find((entry) => entry.id.trim().toLowerCase() === normalized ||
    (entry.aliases ?? []).some((alias) => alias.trim().toLowerCase() === normalized))
}

export function reasoningEffortsForConnection(
  connection: ReasoningModelConnection,
  catalog: readonly ReasoningCatalogEntry[],
): string[] {
  const provider = (connection.provider ?? '').trim().toLowerCase()
  const baseURL = normalizedBaseURL(connection.baseURL || OPENAI_API_BASE_URL)
  if ((provider && provider !== 'openai-compatible') || baseURL !== OPENAI_API_BASE_URL) return []
  const entry = lookupReasoningModel(connection.model, catalog)
  return entry?.efforts.filter((effort) => typeof effort === 'string' && effort.trim() !== '') ?? []
}

export function isReasoningEffortSupported(effort: string, options: readonly string[]): boolean {
  return effort === '' || options.includes(effort)
}

export function reasoningEffortError(effort: string, options: readonly string[]): string {
  if (!effort || isReasoningEffortSupported(effort, options)) return ''
  return 'This reasoning effort is no longer supported by the selected model. Reset it to Model default before saving or sending.'
}

export function reasoningSelectionError(
  effort: string,
  inheritedEffort: string,
  options: readonly string[],
): string {
  const explicitError = reasoningEffortError(effort, options)
  const inheritedError = Boolean(inheritedEffort) && !isReasoningEffortSupported(inheritedEffort, options)
  if (inheritedError && explicitError) {
    return `The thread preference and saved model default are no longer supported. Reset the thread preference and update the model settings before sending.`
  }
  if (inheritedError) {
    return `The saved model default “${inheritedEffort}” is no longer supported. Update the model settings before sending.`
  }
  return explicitError
}

export function reasoningEffortLabel(effort: string): string {
  if (effort === 'xhigh') return 'Extra high'
  return effort ? effort[0].toUpperCase() + effort.slice(1) : 'Model default'
}
