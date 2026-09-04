import { describe, expect, it } from 'vitest'
import {
  CODE_JOURNEY_STEPS,
  clearCodeReturnIntent,
  codeFirstRunModel,
  codeJourneyStorage,
  codeJourneyTenantKey,
  readCodeReturnIntent,
  writeCodeReturnIntent,
  type CodeJourneyStorage,
} from './journey'

describe('Code first-run journey', () => {
  it('starts connection setup with the available authentication method', () => {
    expect(codeFirstRunModel('connection', false, false).primary).toEqual({
      label: 'Add token manually',
      action: 'create-connection',
    })
    expect(codeFirstRunModel('connection', false, true).primary.label).toBe('Connect with GitHub')
    expect(codeFirstRunModel('connection', false, true).secondary?.label).toBe('Add token manually')
  })

  it('keeps repository onboarding on the connection prerequisite until one exists', () => {
    const blocked = codeFirstRunModel('repository', false)
    expect(blocked.currentStep).toBe(0)
    expect(blocked.primary.action).toBe('create-connection')

    const ready = codeFirstRunModel('repository', true)
    expect(ready.currentStep).toBe(1)
    expect(ready.primary.action).toBe('create-repository')
    expect(CODE_JOURNEY_STEPS.map(step => step.label)).toEqual(['Connection', 'Repository', 'Manage'])
  })

  it('restores only a validated, tenant-scoped repository return intent', () => {
    const values = new Map<string, string>()
    const storage: CodeJourneyStorage = {
      getItem: key => values.get(key) ?? null,
      setItem: (key, value) => { values.set(key, value) },
      removeItem: key => { values.delete(key) },
    }
    const tenantA = codeJourneyTenantKey('root:tenant-a', 'user-a')
    const tenantAUserB = codeJourneyTenantKey('root:tenant-a', 'user-b')
    const tenantB = codeJourneyTenantKey('root:tenant-b', 'user-a')

    writeCodeReturnIntent(storage, tenantA, 'create/repository', 'create/connection/token')
    expect(readCodeReturnIntent(storage, tenantAUserB, 'create/connection/token')).toBeNull()
    expect(readCodeReturnIntent(storage, tenantB, 'create/connection/token')).toBeNull()
    expect(readCodeReturnIntent(storage, tenantA, 'create/connection/token')).toBe('create/repository')

    writeCodeReturnIntent(storage, tenantA, 'create/repository', 'create/connection/token')
    expect(readCodeReturnIntent(storage, tenantA, 'connections')).toBeNull()
    expect(readCodeReturnIntent(storage, tenantA, 'create/connection/token')).toBeNull()

    values.set('faros:code:return-intent', JSON.stringify({
      [tenantA]: { returnPath: 'https://example.invalid', expectedPath: 'create/connection/token' },
    }))
    expect(readCodeReturnIntent(storage, tenantA, 'create/connection/token')).toBeNull()

    writeCodeReturnIntent(storage, tenantA, 'create/repository', 'create/connection/token')
    clearCodeReturnIntent(storage, tenantA)
    expect(readCodeReturnIntent(storage, tenantA, 'create/connection/token')).toBeNull()
  })

  it('degrades to in-memory routing when browser storage throws', () => {
    const windowDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'window')
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: {
        get sessionStorage() {
          throw new Error('storage denied')
        },
      },
    })
    try {
      expect(codeJourneyStorage()).toBeNull()
    } finally {
      if (windowDescriptor) Object.defineProperty(globalThis, 'window', windowDescriptor)
      else Reflect.deleteProperty(globalThis, 'window')
    }
  })
})
