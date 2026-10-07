import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({ appType: 'custom', logLevel: 'silent', server: { middlewareMode: true, hmr: false } })
const {
  buildProjectIntegrationConsentRequest,
  projectIntegrationCardEntries,
  projectIntegrationConsentState,
  projectIntegrationErrorMessage,
  projectIntegrationItemsAfterResponse,
} = await vite.ssrLoadModule('/src/ProjectIntegrations.vue')
test.after(async () => vite.close())

const resourceRef = {
  name: 'warehouse',
  apiVersion: 'data.railgrid.ai/v1alpha1',
  kind: 'DatabricksCatalog',
  resource: 'databrickscatalogs',
}
const boundDigest = `sha256:${'b'.repeat(64)}`
const catalogDigest = `sha256:${'c'.repeat(64)}`
const otherDigest = `sha256:${'d'.repeat(64)}`

const persistedIntegration = {
  environment: 'development',
  alias: 'auto-warehouse',
  provider: 'databricks',
  kind: 'providerReference',
  resourceRef,
  allowedActions: [{ name: 'query', version: 'v1', schemaDigest: boundDigest }],
  phase: 'Ready',
}

const candidate = (overrides = {}) => ({
  environment: 'development',
  alias: 'auto-warehouse',
  provider: 'databricks',
  kind: 'providerReference',
  resourceRef,
  actions: [{
    id: 'query/v1',
    name: 'query',
    version: 'v1',
    displayName: 'Query warehouse',
    schemaDigest: catalogDigest,
    readOnly: true,
    risk: 'low',
    consent: { required: true, prompt: 'Confirm access before use.' },
  }],
  ...overrides,
})

test('shows discovered candidates as Available and persisted bindings as Connected', () => {
  const connected = { ...persistedIntegration, alias: 'saved-warehouse' }
  const cards = projectIntegrationCardEntries(
    [connected],
    [candidate({ resourceRef: { ...resourceRef, name: 'another-warehouse' }, alias: 'auto-another-warehouse' })],
  )

  assert.deepEqual(cards.map(({ state, alias }) => [state, alias]), [
    ['connected', 'saved-warehouse'],
    ['available', 'auto-another-warehouse'],
  ])
  assert.equal(cards[0].allowedActions[0].schemaDigest, boundDigest)
  assert.equal(cards[1].allowedActions.length, 0)
  assert.equal(cards[1].catalogActions[0].schemaDigest, catalogDigest)
  assert.equal(cards[1].catalogActions[0].consent.required, true)
})

test('does not duplicate a discovered candidate already represented by a project binding', () => {
  const cards = projectIntegrationCardEntries([persistedIntegration], [candidate()])

  assert.equal(cards.length, 1)
  assert.equal(cards[0].state, 'connected')
  assert.equal(cards[0].integration, persistedIntegration)
  assert.equal(cards[0].catalogActions[0].id, 'query/v1')
})

test('keeps discovered catalog metadata attached to the matching alias when a resource has duplicate bindings', () => {
  const first = { ...persistedIntegration, alias: 'warehouse-primary', allowedActions: [] }
  const second = { ...persistedIntegration, alias: 'warehouse-readonly', allowedActions: [] }
  const firstAction = candidate({ alias: first.alias, actions: [{ ...candidate().actions[0], id: 'read/v1', name: 'read' }] })
  const secondAction = candidate({ alias: second.alias, actions: [{ ...candidate().actions[0], id: 'write/v1', name: 'write' }] })
  const cards = projectIntegrationCardEntries([first, second], [firstAction, secondAction])

  assert.deepEqual(cards.map((entry) => [entry.alias, entry.catalogActions[0].id]), [
    ['warehouse-primary', 'read/v1'],
    ['warehouse-readonly', 'write/v1'],
  ])
})

test('creates only the explicitly approved action for the exact discovered resource', () => {
  const entry = projectIntegrationCardEntries([], [candidate()])[0]
  const action = entry.catalogActions[0]

  assert.equal(projectIntegrationConsentState(entry, action), 'pending')
  assert.equal(buildProjectIntegrationConsentRequest(entry, action, false), null)
  assert.deepEqual(buildProjectIntegrationConsentRequest(entry, action, true), {
    method: 'create',
    body: {
      alias: entry.alias,
      provider: entry.provider,
      kind: 'providerReference',
      resourceRef,
      allowedActions: [{ name: 'query', version: 'v1', schemaDigest: catalogDigest }],
      consentAccepted: true,
    },
  })
})

test('adds explicit consent to an existing binding without dropping other or revoked actions', () => {
  const integration = {
    ...persistedIntegration,
    alias: 'shared-warehouse',
    allowedActions: [
      { name: 'list', version: 'v1', schemaDigest: boundDigest, grantedBy: 'alex@example.com' },
      { name: 'write', version: 'v1', schemaDigest: otherDigest, revoked: true, revokedBy: 'sam@example.com' },
      { name: 'query', version: 'v1', schemaDigest: boundDigest, revoked: true, revokedBy: 'alex@example.com' },
    ],
  }
  const entry = projectIntegrationCardEntries([integration], [candidate({ alias: integration.alias })])[0]
  const action = entry.catalogActions[0]
  const request = buildProjectIntegrationConsentRequest(entry, action, true)

  assert.equal(projectIntegrationConsentState(entry, action), 'revoked')
  assert.deepEqual(request, {
    method: 'patch',
    alias: 'shared-warehouse',
    body: {
      allowedActions: [
        { name: 'list', version: 'v1', schemaDigest: boundDigest },
        { name: 'write', version: 'v1', schemaDigest: otherDigest, revoked: true },
        { name: 'query', version: 'v1', schemaDigest: catalogDigest },
      ],
      consentAccepted: true,
    },
  })
})

test('requires renewed review after a catalog digest changes and rejects unusable consent metadata', () => {
  const entry = projectIntegrationCardEntries([persistedIntegration], [candidate({ alias: persistedIntegration.alias })])[0]
  const action = entry.catalogActions[0]

  assert.equal(projectIntegrationConsentState(entry, action), 'changed')
  assert.ok(buildProjectIntegrationConsentRequest(entry, action, true))
  assert.equal(projectIntegrationConsentState(entry, { ...action, deprecation: { deprecated: true } }), 'unavailable')
  assert.equal(buildProjectIntegrationConsentRequest(entry, { ...action, deprecation: { deprecated: true } }, true), null)
  assert.equal(projectIntegrationConsentState(entry, { ...action, schemaDigest: 'sha256:malformed' }), 'unavailable')
})

test('keeps bindings in different environments distinct for the same provider resource', () => {
  const productionBinding = { ...persistedIntegration, environment: 'production' }
  const cards = projectIntegrationCardEntries([productionBinding], [candidate()])

  assert.deepEqual(cards.map(({ state, environment }) => [state, environment]), [
    ['connected', 'production'],
    ['available', 'development'],
  ])
})

test('uses the authoritative persisted binding list regardless of discovery state', () => {
  const previous = [persistedIntegration]

  assert.deepEqual(projectIntegrationItemsAfterResponse(previous, {
    items: [],
    available: [],
    discovery: { state: 'unavailable', issues: [{ code: 'catalog-unavailable', message: 'Catalog could not be checked.' }] },
  }), [])
  assert.deepEqual(projectIntegrationItemsAfterResponse([], {
    items: [persistedIntegration],
    available: [],
    discovery: { state: 'unavailable', issues: [{ code: 'catalog-unavailable', message: 'Catalog could not be checked.' }] },
  }), [persistedIntegration])
  assert.deepEqual(projectIntegrationItemsAfterResponse(previous, {
    items: [],
    available: [],
    discovery: { state: 'available', issues: [] },
  }), [])
})

test('shows a useful fallback when a failed request has an empty error message', () => {
  const fallback = 'The provider access request failed. Retry to check again.'

  assert.equal(projectIntegrationErrorMessage(new Error(''), fallback), fallback)
  assert.equal(projectIntegrationErrorMessage('  ', fallback), fallback)
  assert.equal(projectIntegrationErrorMessage(new Error('HTTP 502 Bad Gateway'), fallback), 'HTTP 502 Bad Gateway')
})
