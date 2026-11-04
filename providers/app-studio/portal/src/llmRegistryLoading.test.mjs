import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import ts from 'typescript'
import { createServer } from 'vite'

const apiSource = await readFile(new URL('./api.ts', import.meta.url), 'utf8')
const apiAST = ts.createSourceFile('api.ts', apiSource, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
const apiDeclaration = apiAST.statements
  .flatMap(node => ts.isVariableStatement(node) ? [...node.declarationList.declarations] : [])
  .find(node => node.name.getText(apiAST) === 'api')
const apiProperties = apiDeclaration?.initializer && ts.isObjectLiteralExpression(apiDeclaration.initializer)
  ? apiDeclaration.initializer.properties
  : []
const apiMethod = name => apiProperties.find(node => ts.isMethodDeclaration(node) && node.name.getText(apiAST) === name)
const portalRoot = fileURLToPath(new URL('..', import.meta.url))
const vite = await createServer({
  root: portalRoot,
  appType: 'custom',
  cacheDir: `/tmp/railgrid-vite-llm-registry-loading-${process.pid}`,
  configFile: false,
  server: { middlewareMode: true, hmr: false },
})
const { api } = await vite.ssrLoadModule('/src/api.ts')
test.after(async () => vite.close())

function jsonResponse(body) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

function contextWithFetch(respond) {
  const calls = []
  const ctx = {
    tenant: 'cluster-1',
    token: 'deprecated-token-must-not-be-used-when-host-fetch-exists',
    fetch: async (input, init = {}) => {
      calls.push({ url: String(input), init })
      return respond(String(input), init)
    },
  }
  return { ctx, calls }
}

test('saved connection discovery sends the JSON object required by the backend', async () => {
  const { ctx, calls } = contextWithFetch((url, init) => {
    assert.equal(url, '/clusters/cluster-1/apis/ai.railgrid.ai/v1alpha1/modelproviderconnections/haiku/discover')
    assert.equal(init.method, 'POST')
    assert.equal(new Headers(init.headers).get('Content-Type'), 'application/json')
    assert.deepEqual(JSON.parse(init.body), {})
    return jsonResponse({ models: [{ id: 'claude-haiku', name: 'Haiku' }] })
  })
  const result = await api.discoverModelProviderConnection({ ...ctx, orgUUID: 'org-1', workspaceUUID: 'workspace-1' }, 'haiku')
  assert.equal(result.models[0].id, 'claude-haiku')
  assert.equal(calls.length, 1)
})

test('discovery preserves the backend reason for the manual-verification fallback', async () => {
  const { ctx } = contextWithFetch(() => new Response(JSON.stringify({ reason: 'DiscoveryUnsupported', message: 'Discovery is unsupported.' }), {
    status: 502, headers: { 'Content-Type': 'application/json' },
  }))
  await assert.rejects(api.discoverModelProviderConnection({ ...ctx, orgUUID: 'org-1', workspaceUUID: 'workspace-1' }, 'haiku'), {
    status: 502, reason: 'DiscoveryUnsupported',
  })
})

test('model registry stays outside the static API graph and mutations use their own lazy module', () => {
  const staticImports = apiAST.statements
    .filter(ts.isImportDeclaration)
    .map(node => node.moduleSpecifier)
    .filter(ts.isStringLiteral)
    .map(node => node.text)
  assert.equal(staticImports.includes('./llmRegistry'), false)
  assert.equal(staticImports.includes('./llmRegistryRead'), false)

  const loaders = new Map(apiAST.statements
    .filter(node => ts.isFunctionDeclaration(node) && node.name)
    .map(node => [node.name.text, node]))
  assert.match(loaders.get('loadLLMRegistryRead')?.getText(apiAST) ?? '', /import\('\.\/llmRegistryRead'\)/)
  assert.match(loaders.get('loadLLMRegistry')?.getText(apiAST) ?? '', /import\('\.\/llmRegistry'\)/)

  for (const name of ['getLLMSettings', 'listModelProviderConnections', 'getModelProviderConnection']) {
    assert.match(apiMethod(name)?.getText(apiAST) ?? '', /loadLLMRegistryRead\(\)/, `${name} uses the read core`)
  }
  for (const name of ['saveModelProviderConnection', 'deleteModelProviderConnection', 'createLLMModel', 'patchLLMModel', 'deleteLLMModel', 'setDefaultLLMModel']) {
    assert.match(apiMethod(name)?.getText(apiAST) ?? '', /loadLLMRegistry\(\)/, `${name} loads mutations on demand`)
  }
})

test('settings reads keep the host-owned kcp transport and do not fetch credential Secrets', async () => {
  const { ctx, calls } = contextWithFetch((url) => {
    assert.equal(url, '/clusters/cluster-1/apis/ai.railgrid.ai/v1alpha1/studios/studio')
    return jsonResponse({
      apiVersion: 'ai.railgrid.ai/v1alpha1',
      kind: 'Studio',
      metadata: { name: 'studio' },
      spec: {
        llm: {
          defaultModel: 'primary',
          models: [{
            id: 'primary',
            revisionID: 'revision-1',
            name: 'Primary model',
            provider: 'openai',
            baseURL: 'https://api.openai.com/v1',
            model: 'gpt-6.1-sol',
          }],
        },
      },
      status: { models: [{ id: 'primary', configured: true }] },
    })
  })

  const settings = await api.getLLMSettings(ctx)

  assert.equal(settings.defaultModelID, 'primary')
  assert.equal(settings.models[0].name, 'Primary model')
  assert.equal(settings.models[0].configured, true)
  assert.equal(calls.length, 1)
  assert.equal(calls[0].init.method, 'GET')
  assert.equal(calls[0].init.credentials, 'same-origin')
  assert.equal(calls[0].init.headers.Authorization, undefined)
  assert.equal(calls.some(call => call.url.includes('/secrets')), false)
})

test('provider-connection status reads stay on the tenant cluster path', async () => {
  const { ctx, calls } = contextWithFetch((url) => {
    assert.equal(url, '/clusters/cluster-1/apis/ai.railgrid.ai/v1alpha1/modelproviderconnections?limit=500')
    return jsonResponse({
      apiVersion: 'ai.railgrid.ai/v1alpha1',
      kind: 'ModelProviderConnectionList',
      items: [{
        metadata: { name: 'shared', uid: 'connection-uid', annotations: { 'ai.railgrid.ai/display-name': 'Shared provider' } },
        spec: { provider: 'openai', baseURL: 'https://api.openai.com/v1' },
        status: {
          conditions: [{ type: 'Ready', status: 'True' }],
          models: [{ id: 'gpt-6.1-sol', compatibility: 'recommended' }],
        },
      }],
    })
  })

  const connections = await api.listModelProviderConnections(ctx)

  assert.equal(connections.length, 1)
  assert.equal(connections[0].displayName, 'Shared provider')
  assert.equal(connections[0].ready, true)
  assert.deepEqual(connections[0].models[0], {
    id: 'gpt-6.1-sol',
    name: 'gpt-6.1-sol',
    compatibility: 'recommended',
    capabilities: undefined,
  })
  assert.equal(calls.length, 1)
  assert.equal(calls[0].init.method, 'GET')
})


test('snapshot fallback reports unexpected metadata failures but tolerates missing or forbidden connections', async t => {
  const warning = t.mock.method(console, 'warn', () => {})
  for (const failure of [403, 404, 500, 'network']) {
    warning.mock.resetCalls()
    const { ctx } = contextWithFetch(url => {
      if (url.endsWith('/studios/studio')) return jsonResponse({
        metadata: { name: 'studio' },
        spec: { llm: { models: [
          { id: 'fallback', name: 'Fallback', model: 'fixture-model', connectionRef: 'unreadable', connectionSnapshot: { provider: 'openai', baseURL: 'https://snapshot.example/v1' } },
          { id: 'healthy', name: 'Healthy', model: 'fixture-model', connectionRef: 'readable' },
        ] } },
      })
      if (url.endsWith('/modelproviderconnections/readable')) return jsonResponse({
        metadata: { name: 'readable', annotations: { 'ai.railgrid.ai/display-name': 'Readable provider' } },
        spec: { provider: 'openai', baseURL: 'https://api.openai.com/v1' },
      })
      assert.ok(url.endsWith('/modelproviderconnections/unreadable'))
      if (failure === 'network') throw new TypeError('Network unavailable')
      return new Response(JSON.stringify({ kind: 'Status', message: 'Metadata unavailable' }), {
        status: failure, headers: { 'Content-Type': 'application/json' },
      })
    })
    const settings = await api.getLLMSettings(ctx)
    assert.equal(settings.models.length, 2)
    assert.equal(settings.models.find(model => model.id === 'fallback').baseURL, 'https://snapshot.example/v1')
    assert.equal(settings.models.find(model => model.id === 'healthy').connectionName, 'Readable provider')
    assert.equal(warning.mock.callCount(), failure === 500 || failure === 'network' ? 1 : 0)
    if (warning.mock.callCount()) {
      assert.equal(warning.mock.calls[0].arguments[0], 'Model provider connection metadata unavailable')
      assert.equal(warning.mock.calls[0].arguments[1], 'unreadable')
    }
  }
})
