import { describe, expect, it } from 'vitest'
import { runFailurePresentation, sanitizeTechnicalDiagnostic } from '../failure-presentation'

const RUNNER_404_PREFIX = 'starting the harness turn: runner HTTP 404:'
const RUNNER_502 = 'starting the harness turn: runner HTTP 502: upstream returned an empty response'

function runnerStatus(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    kind: 'Status',
    apiVersion: 'v1',
    metadata: {},
    status: 'Failure',
    message: 'services.edges.railgrid.ai "book-codex" not found',
    reason: 'NotFound',
    details: { name: 'book-codex', group: 'edges.railgrid.ai', kind: 'services' },
    code: 404,
    ...overrides,
  }
}

function missingRunnerDiagnostic(status: Record<string, unknown> = runnerStatus()): string {
  return `${RUNNER_404_PREFIX} ${JSON.stringify(status)}`
}

const genericFailure = {
  title: 'Run failed',
  summary: 'Review the run details for more information.',
}

describe('run failure presentation', () => {
  it('recognizes the missing edge runner Service and offers agent settings', () => {
    expect(runFailurePresentation('failed', missingRunnerDiagnostic())).toEqual({
      title: 'Couldn’t start this run',
      summary: 'Runner “book-codex” wasn’t found.',
      recovery: { label: 'Review agent settings', target: 'agent-config' },
    })
  })

  it.each([
    ['malformed JSON', `${RUNNER_404_PREFIX} {"kind":"Status"`],
    ['truncated JSON', `${RUNNER_404_PREFIX} {"kind":"Status","apiVersion":"v1"`],
    ['a generic 404', 'HTTP 404: resource not found'],
    ['a tool 404', 'Tool call failed: HTTP 404: services.edges.railgrid.ai "book-codex" not found'],
    ['a different response group', missingRunnerDiagnostic({
      ...runnerStatus(),
      details: { name: 'book-codex', group: 'apps', kind: 'services' },
    })],
    ['a different resource kind', missingRunnerDiagnostic({
      ...runnerStatus(),
      details: { name: 'book-codex', group: 'edges.railgrid.ai', kind: 'deployments' },
    })],
    ['a different Status kind', missingRunnerDiagnostic({ ...runnerStatus(), kind: 'Error' })],
    ['a non-v1 Status', missingRunnerDiagnostic({ ...runnerStatus(), apiVersion: 'v2' })],
    ['a non-NotFound reason', missingRunnerDiagnostic({ ...runnerStatus(), reason: 'Forbidden' })],
    ['a different HTTP status code', missingRunnerDiagnostic({ ...runnerStatus(), code: 502 })],
    ['a missing service name', missingRunnerDiagnostic({
      ...runnerStatus(),
      details: { group: 'edges.railgrid.ai', kind: 'services' },
    })],
    ['a name that does not match the Status message', missingRunnerDiagnostic({
      ...runnerStatus(),
      details: { name: 'other-runner', group: 'edges.railgrid.ai', kind: 'services' },
    })],
    ['an invalid Kubernetes service name', missingRunnerDiagnostic({
      ...runnerStatus(),
      message: 'services.edges.railgrid.ai "book-codex<script>" not found',
      details: { name: 'book-codex<script>', group: 'edges.railgrid.ai', kind: 'services' },
    })],
  ])('uses generic failure copy for %s', (_label, diagnostic) => {
    expect(runFailurePresentation('failed', diagnostic)).toEqual(genericFailure)
  })

  it('treats a runner 502 during harness startup as an unknown outcome', () => {
    expect(runFailurePresentation('failed', RUNNER_502)).toEqual({
      title: 'Run status is unclear',
      summary: 'Check the run’s latest status before trying again.',
    })
  })

  it.each([
    ['a tool 502', 'Tool call failed: HTTP 502: upstream returned an empty response'],
    ['an unrelated gateway error', 'Gateway returned HTTP 502 while loading agent configuration'],
  ])('does not treat %s as a runner start outcome', (_label, diagnostic) => {
    expect(runFailurePresentation('failed', diagnostic)).toEqual(genericFailure)
  })

  it('does not classify startup transport errors when the run was canceled or interrupted', () => {
    expect(runFailurePresentation('aborted', RUNNER_502).title).toBe('Run canceled')
    expect(runFailurePresentation('interrupted', missingRunnerDiagnostic()).title).toBe('Chat connection interrupted')
  })

  it('keeps generic failed copy independent of optional steps and partial output', () => {
    expect(runFailurePresentation('failed', '')).toEqual(genericFailure)
    expect(runFailurePresentation('failed', 'tool execution failed')).toEqual(genericFailure)
  })

  it('redacts credential-shaped values before the runner error is classified', () => {
    const secret = 'Bearer abcdefghijklmnopqrstuvwxyz123456'
    const diagnostic = missingRunnerDiagnostic(runnerStatus({
      metadata: { authorization: secret },
    }))
    const safeDiagnostic = sanitizeTechnicalDiagnostic(diagnostic)

    expect(safeDiagnostic).not.toContain(secret)
    expect(safeDiagnostic).toContain('Bearer [redacted]')
    expect(runFailurePresentation('failed', safeDiagnostic)).toEqual({
      title: 'Couldn’t start this run',
      summary: 'Runner “book-codex” wasn’t found.',
      recovery: { label: 'Review agent settings', target: 'agent-config' },
    })
  })

  it('preserves the existing model-specific recovery classifications', () => {
    expect(runFailurePresentation('failed', 'The model rejected reasoning_effort because it is unsupported').recovery?.target)
      .toBe('agent-config')
    expect(runFailurePresentation('failed', 'OpenAI API key is invalid').recovery?.target)
      .toBe('model-connections')
  })
})
