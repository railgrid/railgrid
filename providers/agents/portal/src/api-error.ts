/** HTTP failures surfaced by the provider API client. */
export class ApiError extends Error {
  readonly status: number
  readonly technicalDiagnostic: string
  readonly reason: string

  constructor(status: number, message: string, technicalDiagnostic = '', reason = '') {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.technicalDiagnostic = technicalDiagnostic
    this.reason = reason
  }
}
