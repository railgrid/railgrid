import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'
import ts from 'typescript'

const source = await readFile(new URL('./projectIntegrations.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const {
  buildProjectIntegrationCreatePayload,
  buildProjectIntegrationRevokePayload,
  projectIntegrationsAuthorityKey,
  projectIntegrationsRequestIsCurrent,
  readyProviderActions,
} = await import(moduleURL)

const digest = 'sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'
// The coordinate an action is bound to now lives on its parent export resource:
// the entry declares apiVersion and kind once, next to the plural name.
const boundResource = { apiVersion: 'example.railgrid.ai/v1', kind: 'Table', resource: 'tables' }

function action(id, overrides = {}) {
  const [name, version] = id.split('/')
  return {
    id,
    name,
    version,
    displayName: id,
    schemaDigest: digest,
    readOnly: true,
    risk: 'low',
    consent: { required: false },
    ...overrides,
  }
}

// One export section carrying the given actions on example.railgrid.ai/v1 Table.
function exportWith(...actions) {
  return { name: 'example.providers.railgrid.ai', resources: [{ name: 'tables', apiVersion: 'example.railgrid.ai/v1', kind: 'Table', actions }] }
}

function bound(action) {
  return { ...boundResource, action }
}

test('selects only Ready providers with current, digest-backed actions', () => {
  const ready = { name: 'ready', displayName: 'Ready', ready: true, export: exportWith(action('query/v1')) }
  const unready = { name: 'unready', displayName: 'Unready', ready: false, export: exportWith(action('query/v1')) }
  const deprecated = { name: 'deprecated', displayName: 'Deprecated', ready: true, export: exportWith(action('query/v1', { deprecation: { deprecated: true } })) }
  const invalidDigest = { name: 'invalid', displayName: 'Invalid', ready: true, export: exportWith(action('query/v1', { schemaDigest: 'sha256:old' })) }
  const noExport = { name: 'no-export', displayName: 'No export', ready: true }

  const selected = readyProviderActions([unready, invalidDigest, deprecated, noExport, ready])
  assert.deepEqual(selected.map(({ provider, action }) => `${provider.name}:${action.id}`), ['ready:query/v1'])
  // Each selection carries its parent resource's coordinate, which is the only
  // place the catalog publishes it.
  assert.deepEqual(
    selected.map(({ apiVersion, kind, resource }) => ({ apiVersion, kind, resource })),
    [boundResource],
  )
})

test('an export resource missing part of its coordinate publishes no grantable action', () => {
  const provider = {
    name: 'partial', displayName: 'Partial', ready: true,
    export: { name: 'partial.providers.railgrid.ai', resources: [
      { name: 'tables', apiVersion: 'example.railgrid.ai/v1', actions: [action('query/v1')] },
      { name: '', apiVersion: 'example.railgrid.ai/v1', kind: 'Table', actions: [action('describe/v1')] },
      { name: 'views', apiVersion: 'example.railgrid.ai/v1', kind: 'View', verbs: [{ name: 'render' }] },
    ] },
  }
  assert.deepEqual(readyProviderActions([provider]), [])
})

test('builds an exact resource grant from immutable catalog metadata', () => {
  const provider = { name: 'databricks', displayName: 'Databricks', ready: true }
  const payload = buildProjectIntegrationCreatePayload(provider, bound(action('query_table/v1')), ' sales ', ' orders ', false)

  assert.deepEqual(payload, {
    alias: 'sales',
    provider: 'databricks',
    kind: 'providerReference',
    resourceRef: { name: 'orders', ...boundResource },
    allowedActions: [{ name: 'query_table', version: 'v1', schemaDigest: digest }],
    consentAccepted: false,
  })
  assert.equal('credentials' in payload, false)
  assert.equal('providerURL' in payload, false)
})

test('revoke payload preserves each grant digest and targets only the selected action', () => {
  const integration = {
    environment: 'development',
    alias: 'sales',
    provider: 'databricks',
    kind: 'providerReference',
    resourceRef: { name: 'orders', ...boundResource },
    allowedActions: [
      { name: 'query_table', version: 'v1', schemaDigest: digest, grantedBy: 'alice@example.com' },
      { name: 'describe_table', version: 'v1', schemaDigest: digest, grantedBy: 'alice@example.com' },
    ],
  }
  assert.deepEqual(buildProjectIntegrationRevokePayload(integration, 'query_table', 'v1'), {
    allowedActions: [
      { name: 'query_table', version: 'v1', schemaDigest: digest, revoked: true },
      { name: 'describe_table', version: 'v1', schemaDigest: digest },
    ],
    consentAccepted: true,
  })
  assert.equal(buildProjectIntegrationRevokePayload(integration, 'missing', 'v1'), null)
})

test('drops late integration reads after project, workspace, or user authority changes', async () => {
  const originalContext = { tenant: 'tenant-a', orgUUID: 'org-a', workspaceUUID: 'workspace-a', user: { sub: 'alice' }, token: 'old-token' }
  const nextContext = { tenant: 'tenant-b', orgUUID: 'org-b', workspaceUUID: 'workspace-b', user: { userId: 'bob' }, token: 'new-token' }
  const originalAuthority = projectIntegrationsAuthorityKey(originalContext, 'first-project')
  const nextAuthority = projectIntegrationsAuthorityKey(nextContext, 'second-project')
  let resolveOriginal
  const originalResponse = new Promise((resolve) => { resolveOriginal = resolve })
  let currentSerial = 1
  let currentAuthority = originalAuthority
  const committed = originalResponse.then((value) => projectIntegrationsRequestIsCurrent(
    1,
    originalAuthority,
    currentSerial,
    currentAuthority,
  ) ? value : null)

  currentSerial = 2
  currentAuthority = nextAuthority
  resolveOriginal([{ alias: 'stale-first-project' }])
  assert.equal(await committed, null)
  assert.equal(projectIntegrationsRequestIsCurrent(2, nextAuthority, currentSerial, currentAuthority), true)

  // A rotated bearer token keeps the same resource authority and therefore
  // does not blank an otherwise valid same-scope snapshot.
  assert.equal(projectIntegrationsAuthorityKey({ ...originalContext, token: 'rotated-token' }, 'first-project'), originalAuthority)
  assert.notEqual(projectIntegrationsAuthorityKey({ ...originalContext, user: { sub: 'bob' } }, 'first-project'), originalAuthority)
})
