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
  const message = statusSummary(status, reason)
  const raw = typeof diagnostic === 'string' ? diagnostic : diagnostic == null ? '' : String(diagnostic)
  return { message, technicalDiagnostic: sanitizeTechnicalDiagnostic(raw) }
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
