import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiClient, ApiError } from '../api'
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
