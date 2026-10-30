import { sanitizeTechnicalDiagnostic } from './failure-presentation'

export interface RequestFailurePresentation {
  message: string
  technicalDiagnostic: string
}

/**
 * Present a remote transport failure without forwarding its response body as
 * the default user-facing message. The diagnostic remains available to an
 * explicit support-details surface after redaction and bounding.
 */
export function presentRequestFailure(status: number, diagnostic?: unknown, reason = ''): RequestFailurePresentation {
  const raw = typeof diagnostic === 'string' ? diagnostic : diagnostic == null ? '' : String(diagnostic)
  const denial = parseAdmissionDenial(raw)
  const message = denial ? admissionDenialSummary(denial) : statusSummary(status, reason)
  return { message, technicalDiagnostic: sanitizeTechnicalDiagnostic(raw) }
}

/**
 * An admission denial is a policy decision the platform made about this
 * write, not a missing RBAC grant, even though kcp reports both as 403
 * Forbidden. The webhook's own message is the actionable part, so it is
 * surfaced instead of the generic permission summary. kcp wraps it as
 * `admission webhook "<name>" denied the request: <message>`; anything that
 * does not match that exact shape stays an untrusted diagnostic.
 */
export interface AdmissionDenial {
  webhook: string
  message: string
}

const ADMISSION_DENIAL = /admission webhook "([^"]+)" denied the request: ([\s\S]+)$/
const MAX_DENIAL_MESSAGE_LENGTH = 300

export function parseAdmissionDenial(diagnostic: string): AdmissionDenial | null {
  const match = ADMISSION_DENIAL.exec(diagnostic)
  if (!match) return null
  const message = match[2].trim()
  if (!message) return null
  return { webhook: match[1], message }
}

// contrib-metering's enforcement webhook denies with
// `metering: <meter> limit exceeded for account <account> (limit <n>)`.
const METERING_LIMIT = /^metering: ([a-z][a-z0-9_]*) limit exceeded for account \S+ \(limit (\d+(?:\.\d+)?)\)/

export function admissionDenialSummary(denial: AdmissionDenial): string {
  const limit = METERING_LIMIT.exec(denial.message)
  if (limit) {
    const meter = limit[1].replace(/_/g, ' ')
    const max = Number(limit[2])
    const noun = max === 1 ? meter.replace(/s$/, '') : meter
    return `This workspace's plan allows up to ${max} ${noun}. Remove one, or ask a workspace administrator to raise the limit.`
  }
  const { text } = boundedDiagnostic(denial.message, MAX_DENIAL_MESSAGE_LENGTH)
  return `The workspace refused this change: ${text}`
}

function boundedDiagnostic(value: string, limit: number): { text: string } {
  const safe = sanitizeTechnicalDiagnostic(value)
  const chars = Array.from(safe)
  return { text: chars.length > limit ? `${chars.slice(0, limit).join('')}…` : safe }
}

function statusSummary(status: number, reason: string): string {
  switch (status) {
    case 0:
      return 'The workspace could not be reached. Check your connection and try again.'
    case 400:
      return 'The request was rejected. Review the setup and try again.'
    case 401:
      return 'Your session could not be verified. Sign in again, then retry.'
    case 403:
      return 'You do not have permission to do that in this workspace. Ask a workspace administrator if access is needed.'
    case 404:
      return 'The requested item was not found in this workspace. It may have been removed.'
    case 408:
    case 504:
      return 'The workspace took too long to answer. Try again.'
    case 409:
      if (reason === 'AlreadyExists') return 'An item with this name already exists. Choose a different name and try again.'
      return 'This item changed while you were editing it. Refresh and try again.'
    case 410:
      return 'This item is no longer available. Refresh and try again.'
    case 422:
      return 'The request could not be applied. Review the setup and try again.'
    case 429:
      return 'Too many requests were sent. Wait a moment and try again.'
    default:
      if (status >= 500) {
        return 'The server could not complete this request. Try again; if the problem continues, contact a workspace administrator.'
      }
      return 'The request could not be completed. Review workspace access and try again.'
  }
}
