import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/faros-vite-production-pane',
  configFile: false,
  server: { middlewareMode: true },
})
const { productionConfigurationSummary, shortReleaseSHA } = await vite.ssrLoadModule('/src/productionPane.ts')
test.after(async () => vite.close())

test('presents an exact release with a compact SHA', () => {
  assert.equal(shortReleaseSHA(' 75ff40e31b345e10efcb5d0398aad3c7bacf626d '), '75ff40e3')
  assert.equal(shortReleaseSHA('abc123'), 'abc123')
})

test('summarizes the runtime without exposing publishing policy as deployment configuration', () => {
  assert.equal(productionConfigurationSummary({
    access: 'public',
    port: 8080,
    replicas: 1,
    expose: { hostnamePrefix: '' },
    env: {},
  }), '1 replica · port 8080 · default hostname · no environment overrides')
  assert.equal(productionConfigurationSummary({
    webPort: '3000',
    replicaCount: 2,
    expose: { hostnamePrefix: 'docs' },
    webEnv: { API_URL: 'https://api.example.test' },
    apiEnv: { LOG_LEVEL: 'info' },
  }), '2 replicas · port 3000 · custom hostname · 2 environment overrides')
})
