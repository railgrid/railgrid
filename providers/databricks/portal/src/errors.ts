import type { ErrorResponse } from './types.js'

type ErrorCategory = 'not-found' | 'forbidden' | 'authentication' | 'service' | 'protocol' | 'tenant' | 'domain' | 'unknown'
type RegistrationMessageState = 'created' | 'existing' | 'conflict' | 'failed'

const SAFE_REASON_PREFIX = /^(?:[A-Za-z]+Error|RequestFailed|ServiceUnavailable|Unauthorized|Forbidden|NotFound|ProtocolError)\s*:\s*/i
const ERROR_LABEL = /\b(?:HTTPError|GraphQLError)\b/i
const HTML_BODY = /<\/?(?:!doctype|html|head|body|script|style|title|div|pre)\b|<[a-z][^>]*>/i
const JSON_BODY = /(?:^|[\s:(])(?:\{\s*["']?[-\w]+["']?\s*:|\[\s*(?:\{|["']))/i
const SENSITIVE_PAYLOAD = /\b(?:authorization|bearer|token|password|passwd|secret|api[-_ ]?key|access[-_ ]?key|client[-_ ]?secret|credential|cookie|set-cookie)\b\s*(?::|=)\s*(?:bearer\s+)?(?:"[^"]*"|'[^']*'|[^\s,;)}]+)/i
const URL_CREDENTIAL = /[?&](?:token|access[_-]?token|api[_-]?key|password|secret)=/i
const NOT_FOUND = /\bnot\s*found\b/i
const SAFE_RESOURCE_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/
const GENERIC_MESSAGE = 'Databricks request failed. Retry the request.'
const SERVICE_MESSAGE = 'Databricks service is unavailable. Retry the request.'

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value)
}

function stringField(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const message = stripTransportPrefix(value.trim()).replace(/\s+/g, ' ')
  if (!message || message.length > 500 || HTML_BODY.test(message) || JSON_BODY.test(message)) return undefined
  if (ERROR_LABEL.test(message) || SENSITIVE_PAYLOAD.test(message) || URL_CREDENTIAL.test(message)) return undefined
  return message
}

function reasonField(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const reason = stripTransportPrefix(value.trim()).replace(/\s+/g, ' ')
  if (!reason || reason.length > 120 || HTML_BODY.test(reason) || JSON_BODY.test(reason) || SENSITIVE_PAYLOAD.test(reason) || URL_CREDENTIAL.test(reason)) return undefined
  return reason
}

interface ErrorDetails {
  reason?: string
  message?: string
  status?: number
  /** Native exceptions are implementation details, not domain copy. */
  runtime: boolean
}

function errorDetails(error: unknown): ErrorDetails {
  if (error instanceof Error) {
    const details = error as Error & { reason?: unknown; status?: unknown }
    return {
      reason: reasonField(details.reason),
      message: stringField(details.message),
      status: typeof details.status === 'number' && Number.isInteger(details.status) ? details.status : undefined,
      runtime: !reasonField(details.reason),
    }
  }
  if (!isRecord(error)) return { runtime: false }
  const reason = reasonField(error.reason)
  const message = stringField(error.message)
  const status = typeof error.status === 'number' && Number.isInteger(error.status) ? error.status : undefined
  return { reason, message, status, runtime: false }
}

function reasonText(reason: string | undefined): string {
  return stripTransportPrefix(reason ?? '').replace(SAFE_REASON_PREFIX, '').trim().toLowerCase()
}

function stripTransportPrefix(value: string): string {
  let message = value
  let previous = ''
  while (message !== previous) {
    previous = message
    message = message.replace(SAFE_REASON_PREFIX, '')
  }
  return message
}

function categoryFor(details: ReturnType<typeof errorDetails>): ErrorCategory {
  const reason = reasonText(details.reason)
  const message = details.message?.toLowerCase() ?? ''

  // Native exceptions are runtime implementation details. Only recognize the
  // narrow service/network signals needed for useful recovery copy; all other
  // native messages stay behind the generic boundary. Structured provider
  // records below retain their domain-specific reason and message.
  if (details.runtime) {
    if ((details.status !== undefined && (details.status === 429 || details.status >= 500)) || /failed to fetch|network error|service unavailable|temporarily unavailable|upstream failure|too many requests|gateway timeout/.test(message)) return 'service'
    return 'unknown'
  }

  if (details.status === 404 || reason === 'notfound' || reason.endsWith('notfound') || NOT_FOUND.test(message)) return 'not-found'
  if (details.status === 403 || reason === 'forbidden' || /forbidden|permission denied|not authorized/.test(message)) return 'forbidden'
  if (details.status === 401 || /unauthori[sz]ed|authentication|invalid token|credential/.test(reason) || /unauthori[sz]ed|authentication failed|invalid token/.test(message)) return 'authentication'
  if ((details.status !== undefined && (details.status === 429 || details.status >= 500)) || reason === 'serviceunavailable' || /\b(?:429|5\d\d)\b|failed to fetch|network error|service unavailable|temporarily unavailable|upstream failure|too many requests|gateway timeout/.test(message)) return 'service'
  if (reason === 'protocolerror') return 'protocol'
  if (reason === 'tenantmissing') return 'tenant'
  if (reason === 'requestfailed') return 'unknown'
  if (!details.runtime && (details.reason || details.message)) return 'domain'
  return 'unknown'
}

function notFoundMessage(message: string | undefined): string {
  if (!message) return 'Databricks resource not found.'
  const cleaned = message.replace(SAFE_REASON_PREFIX, '').trim()
  const match = cleaned.match(/\b(connections?|warehouses?|tables?)\b[^"']*["']([^"']+)["'][^\n]*\bnot\s*found\b/i)
  if (match) {
    const kind = match[1].toLowerCase().startsWith('connection')
      ? 'Connection'
      : match[1].toLowerCase().startsWith('warehouse') ? 'Warehouse' : 'Table'
    return SAFE_RESOURCE_NAME.test(match[2]) ? `${kind} "${match[2]}" not found.` : `${kind} not found.`
  }
  const kind = cleaned.match(/\b(connection|warehouse|table)\b/i)?.[1]
  return kind ? `${kind[0].toUpperCase()}${kind.slice(1)} not found.` : 'Databricks resource not found.'
}

/**
 * Convert any provider/API failure into concise copy suitable for a visible
 * Databricks portal error state. Transport reason labels and response bodies
 * are deliberately kept out of the returned string.
 */
export function formatDatabricksError(error: unknown): string {
  const details = errorDetails(error)
  switch (categoryFor(details)) {
    case 'not-found':
      return notFoundMessage(details.message)
    case 'forbidden':
      return 'You do not have permission to access Databricks resources in this workspace.'
    case 'authentication':
      return 'Databricks authentication failed. Check the connection token and try again.'
    case 'service':
      return SERVICE_MESSAGE
    case 'tenant':
      return 'Select a workspace before accessing Databricks resources.'
    case 'protocol':
    case 'domain':
      return details.message || GENERIC_MESSAGE
    case 'unknown':
      return GENERIC_MESSAGE
  }
}

export function isTenantMissingError(error: unknown): boolean {
  return reasonText(errorDetails(error).reason) === 'tenantmissing'
}

/** Keep per-item registration details useful without trusting provider text. */
export function formatDatabricksRegistrationMessage(message: unknown, state: RegistrationMessageState): string | undefined {
  const safeMessage = stringField(message)
  if (safeMessage) return safeMessage
  if (state === 'existing') return 'The resource is already registered.'
  if (state === 'conflict') return 'Registration conflicted with an existing resource.'
  if (state === 'failed') return 'Registration failed. Retry this item.'
  return undefined
}

function statusReason(status: number): string | undefined {
  if (status === 401) return 'Unauthorized'
  if (status === 403) return 'Forbidden'
  if (status === 404) return 'NotFound'
  if (status >= 500) return 'ServiceUnavailable'
  return undefined
}

function bodyError(body: unknown): { reason?: string; message?: string } {
  if (!isRecord(body)) return {}
  return {
    reason: stringField(body.reason),
    message: stringField(body.message),
  }
}

/** Map a non-2xx provider response without retaining a raw JSON/HTML body. */
export function providerRequestError(status: number, body: unknown, fallback = 'Databricks provider request failed'): ErrorResponse {
  const bodyFailure = bodyError(body)
  const reason = statusReason(status) || bodyFailure.reason || 'RequestFailed'
  const message = statusReason(status)
    ? stringField(fallback) || GENERIC_MESSAGE
    : bodyFailure.message || stringField(fallback) || GENERIC_MESSAGE
  return { reason, message, retryable: status >= 500, status }
}

/** Map GraphQL's structured errors to the same internal error contract. */
export function graphqlResponseError(message: string): ErrorResponse {
  const safeMessage = stringField(message) || 'Databricks GraphQL request failed.'
  const details: ErrorDetails = { message: safeMessage, runtime: false }
  const category = categoryFor(details)
  const reason = category === 'not-found'
    ? 'NotFound'
    : category === 'forbidden'
      ? 'Forbidden'
      : category === 'authentication'
        ? 'Unauthorized'
        : category === 'service'
          ? 'ServiceUnavailable'
          : 'DomainError'
  return { reason, message: safeMessage, retryable: category === 'service' }
}
