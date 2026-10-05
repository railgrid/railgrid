import { sanitizeTechnicalDiagnostic } from './failure-presentation'
import type { CredentialTestResult } from './types'

/** A provider diagnostic is supporting detail, never the headline or toast. */
export function presentModelProbeResult(result: CredentialTestResult): CredentialTestResult {
  if (result.ok) return result
  return {
    ...result,
    error: 'The model connection did not pass verification. Review the endpoint, credential, and selected model before testing again.',
    technicalDiagnostic: sanitizeTechnicalDiagnostic(result.technicalDiagnostic || result.error),
  }
}

export function errorTechnicalDiagnostic(error: unknown): string {
  return error && typeof error === 'object' && 'technicalDiagnostic' in error
    ? sanitizeTechnicalDiagnostic(String(error.technicalDiagnostic || '')) : ''
}
