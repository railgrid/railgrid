export type InspectAssertion =
  | {
      kind: 'text_present'
      text: string
      exact?: boolean
    }
  | {
      kind: 'role_present'
      role: string
      name?: string
      exact?: boolean
    }
  | {
      kind: 'role_count'
      role: string
      name?: string
      exact?: boolean
      min?: number
      max?: number
    }

export interface InspectRequest {
  url: string
  assertions?: InspectAssertion[]
  includeScreenshot?: boolean
}

export type AssertionResult = InspectAssertion & {
  passed: boolean
  actualCount?: number
  message?: string
}

export interface ConsoleEvidence {
  level: string
  message: string
}

export interface NetworkEvidence {
  url: string
  method: string
  failure: string
}

export interface ScreenshotEvidence {
  mimeType: 'image/png'
  base64: string
  width: number
  height: number
  sha256: string
}

export interface InspectResponse {
  status: 'succeeded' | 'failed'
  failureKind?: 'navigation' | 'application' | 'assertion'
  finalURL: string
  title: string
  snapshot: string
  assertions: AssertionResult[]
  console: ConsoleEvidence[]
  network: NetworkEvidence[]
  screenshot?: ScreenshotEvidence
}
