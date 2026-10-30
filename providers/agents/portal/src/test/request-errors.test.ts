import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiClient, ApiError } from '../api'
import { admissionDenialSummary, parseAdmissionDenial, presentRequestFailure } from '../request-errors'
import { Resources, ResourceError } from '../resources'
import type { RailgridContext } from '../types'

const CLUSTER = 'tenant-a'
const SECRET = 'sk-live-1234567890abcdef0123456789'
const realFetch = globalThis.fetch

afterEach(() => {
  globalThis.fetch = realFetch
})

function contextWith(fetch: RailgridContext['fetch']): RailgridContext {
  return {
    basePath: '/ui/providers/agents',
    tenant: CLUSTER,
    orgUUID: 'org-1',
    workspaceUUID: 'workspace-1',
    fetch,
  } as RailgridContext
}

type RequestError = Error & { status: number; reason?: string; technicalDiagnostic: string }

async function rejected<T>(promise: Promise<T>): Promise<RequestError> {
  try {
    await promise
  } catch (error) {
    return error as RequestError
  }
  throw new Error('expected the request to fail')
}

describe('remote request errors', () => {
  it('keeps an HTTP status and safe summary while redacting and bounding the diagnostic', async () => {
    const raw = `gateway response Authorization: Bearer ${SECRET} ${'x'.repeat(8_000)}`
    const transport = vi.fn(async () => new Response(JSON.stringify({ message: raw }), { status: 403, statusText: 'Forbidden' }))
    const api = new ApiClient()
    api.setContext(contextWith(transport))

    const error = await rejected(api.listSessions('scout'))

    expect(error).toBeInstanceOf(ApiError)
    expect(error.status).toBe(403)
    expect(error.message).toContain('permission')
    expect(error.message).not.toContain('gateway response')
    expect(error.message).not.toContain(SECRET)
    expect(error.technicalDiagnostic).toContain('[redacted]')
    expect(error.technicalDiagnostic).not.toContain(SECRET)
    expect(error.technicalDiagnostic.length).toBeLessThan(4_100)
  })

  it('keeps Kube Status details separate from the message and retains the status/reason', async () => {
    const raw = `admission response apiKey: "${SECRET}" ${'y'.repeat(8_000)}`
    const resources = new Resources()
    resources.setContext(contextWith(async () => new Response(JSON.stringify({
      apiVersion: 'v1',
      kind: 'Status',
      status: 'Failure',
      reason: 'Forbidden',
      message: raw,
      code: 403,
    }), { status: 403 })))

    const error = await rejected(resources.listAgents())

    expect(error).toBeInstanceOf(ResourceError)
    expect(error.status).toBe(403)
    expect(error.reason).toBe('Forbidden')
    expect(error.message).toContain('permission')
    expect(error.message).not.toContain('admission response')
    expect(error.message).not.toContain(SECRET)
    expect(error.technicalDiagnostic).toContain('[redacted]')
    expect(error.technicalDiagnostic).not.toContain(SECRET)
    expect(error.technicalDiagnostic.length).toBeLessThan(4_100)
  })

  it('surfaces an admission webhook denial instead of the generic permission summary', async () => {
    // Enforcement by contrib-metering: kcp reports the denial as 403 Forbidden,
    // the same status as a missing RBAC grant, but the message is the point.
    const denied = 'admission webhook "enforce.metering.railgrid.ai" denied the request: metering: agents limit exceeded for account b6dc5a8a-5cd2-4c9d-a2b6-c236d6c90c33 (limit 5)'
    const resources = new Resources()
    resources.setContext(contextWith(async () => new Response(JSON.stringify({
      apiVersion: 'v1',
      kind: 'Status',
      status: 'Failure',
      reason: 'Forbidden',
      message: denied,
      code: 403,
    }), { status: 403 })))

    const error = await rejected(resources.createAgent({ name: 'scout' }))

    expect(error).toBeInstanceOf(ResourceError)
    expect(error.status).toBe(403)
    expect(error.reason).toBe('Forbidden')
    expect(error.message).toBe("This workspace's plan allows up to 5 agents. Remove one, or ask a workspace administrator to raise the limit.")
    expect(error.message).not.toContain('permission')
    expect(error.technicalDiagnostic).toBe(denied)

    // Any other webhook's reason is shown too, redacted and bounded, with the
    // same framing; a 403 whose message merely mentions admission is not.
    const other = presentRequestFailure(403, `admission webhook "policy.example" denied the request: images must come from the internal registry, token=${SECRET}`, 'Forbidden')
    expect(other.message).toBe('The workspace refused this change: images must come from the internal registry, token=[redacted]')
    expect(other.message).not.toContain(SECRET)
    const long = presentRequestFailure(403, `admission webhook "policy.example" denied the request: ${'x'.repeat(1_000)}`, 'Forbidden')
    expect(long.message.length).toBeLessThan(400)
    expect(long.message.endsWith('…')).toBe(true)
    expect(presentRequestFailure(403, 'admission response rejected', 'Forbidden').message).toContain('permission')
    expect(parseAdmissionDenial('admission webhook "x" denied the request: ')).toBeNull()

    // Meter names read as plural nouns; a limit of one is singular.
    expect(admissionDenialSummary({ webhook: 'w', message: 'metering: edges limit exceeded for account org (limit 1)' })).toContain('up to 1 edge.')
    expect(admissionDenialSummary({ webhook: 'w', message: 'metering: request_cost_rpm limit exceeded for account org (limit 600)' })).toContain('up to 600 request cost rpm.')
  })

  it('distinguishes an existing name from an edit conflict when presenting 409 responses', async () => {
    const resources = new Resources()
    resources.setContext(contextWith(async () => new Response(JSON.stringify({
      apiVersion: 'v1',
      kind: 'Status',
      status: 'Failure',
      reason: 'AlreadyExists',
      message: 'agents "scout" already exists',
      code: 409,
    }), { status: 409 })))

    const duplicate = await rejected(resources.createAgent({ name: 'scout' }))
    expect(duplicate.status).toBe(409)
    expect(duplicate.reason).toBe('AlreadyExists')
    expect(duplicate.message).toContain('already exists')
    expect(duplicate.message).toContain('Choose a different name')
    expect(duplicate.message).not.toContain('Refresh')

    const api = new ApiClient()
    api.setContext(contextWith(async () => new Response(JSON.stringify({
      reason: 'Conflict',
      message: 'the object was changed by another writer',
    }), { status: 409 })))
    const conflict = await rejected(api.listSessions('scout'))
    expect(conflict.status).toBe(409)
    expect(conflict.reason).toBe('Conflict')
    expect(conflict.message).toContain('changed while you were editing')
    expect(conflict.message).toContain('Refresh')
  })

  it('uses a safe network summary and sanitized diagnostic for Kube transport failures', async () => {
    const resources = new Resources()
    resources.setContext(contextWith(async () => {
      throw new TypeError(`fetch failed: apiKey=${SECRET} ${'z'.repeat(8_000)}`)
    }))

    const error = await rejected(resources.listAgents())

    expect(error).toBeInstanceOf(ResourceError)
    expect(error.status).toBe(0)
    expect(error.reason).toBe('NetworkError')
    expect(error.message).toContain('could not be reached')
    expect(error.message).not.toContain(SECRET)
    expect(error.technicalDiagnostic).toContain('[redacted]')
    expect(error.technicalDiagnostic).not.toContain(SECRET)
    expect(error.technicalDiagnostic.length).toBeLessThan(4_100)
  })

  it('preserves locally authored validation and context messages', async () => {
    const resources = new Resources()
    resources.setContext(contextWith(vi.fn(async () => new Response('{}', { status: 200 }))))

    const validation = await rejected(resources.createAgent({ name: '' } as never))
    expect(validation).toMatchObject({ status: 400, reason: 'BadRequest', message: 'name is required', technicalDiagnostic: '' })

    const noWorkspace = new Resources()
    noWorkspace.setContext({ basePath: '/ui/providers/agents', tenant: null } as RailgridContext)
    const missingContext = await rejected(noWorkspace.listAgents())
    expect(missingContext).toMatchObject({ status: 400, reason: 'TenantMissing', message: 'no workspace selected', technicalDiagnostic: '' })
  })
})
