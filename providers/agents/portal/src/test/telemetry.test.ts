import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api-error'
import { mutate } from '../mutate'
import type { AppStore } from '../store'
import { failureReason, setTelemetryContext, track } from '../telemetry'

function store(): AppStore {
  const target = new EventTarget() as EventTarget & { isRetired: () => boolean; load: () => Promise<void> }
  target.isRetired = () => false
  target.load = async () => {}
  return target as unknown as AppStore
}

describe('telemetry', () => {
  afterEach(() => setTelemetryContext(null))

  it('does nothing without a host track function and never throws', () => {
    expect(() => track('agent_created')).not.toThrow()
    setTelemetryContext({ track: () => { throw new Error('host failure') } })
    expect(() => track('agent_created')).not.toThrow()
  })

  it('reports a failure as a fixed reason, never the message', () => {
    expect(failureReason(new ApiError(403, 'cannot create agents "payroll-bot"'))).toBe('forbidden')
    expect(failureReason(new ApiError(403, 'x', 'admission webhook "m" denied the request: metering: agents limit exceeded for account acme (limit 1)'))).toBe('limit')
    expect(failureReason(new ApiError(409, 'agents "payroll-bot" already exists'))).toBe('conflict')
    expect(failureReason(new ApiError(422, 'invalid'))).toBe('validation')
    expect(failureReason(new ApiError(0, 'offline'))).toBe('network')
    expect(failureReason(new ApiError(503, 'down'))).toBe('server')
    expect(failureReason(new Error('payroll-bot exploded'))).toBe('error')
    expect(failureReason(undefined)).toBe('error')
  })

  it('records the outcome of a mutation with its fixed labels', async () => {
    const host = vi.fn()
    setTelemetryContext({ track: host })
    const telemetry = { ok: 'agent_created', failed: 'agent_create_failed', props: { backend: 'model' } }

    await expect(mutate(store(), { run: async () => 'made', failure: 'Create failed', telemetry })).resolves.toBe('made')
    expect(host).toHaveBeenLastCalledWith('agent_created', { backend: 'model' })

    await expect(mutate(store(), {
      run: async () => { throw new ApiError(409, 'agents "payroll-bot" already exists') },
      failure: 'Create failed',
      onFailure: () => true,
      telemetry,
    })).resolves.toBeUndefined()
    expect(host).toHaveBeenLastCalledWith('agent_create_failed', { backend: 'model', reason: 'conflict' })
    expect(JSON.stringify(host.mock.calls)).not.toContain('payroll-bot')
  })
})
