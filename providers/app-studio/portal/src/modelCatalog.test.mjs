import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  configFile: false,
  appType: 'custom',
  root: new URL('../', import.meta.url).pathname,
  server: { middlewareMode: true, hmr: false, ws: false },
  optimizeDeps: { noDiscovery: true },
})
test.after(() => vite.close())
const { lookupCatalog } = await vite.ssrLoadModule('/src/llmRegistry.ts')

test('current model prices survive generation and provider-prefixed lookup', () => {
  const sol = lookupCatalog('openai/gpt-6.1-sol')
  assert.equal(sol.inputPer1M, 2)
  assert.equal(sol.outputPer1M, 10)
  const opus = lookupCatalog('claude-opus-5-5')
  assert.equal(opus.inputPer1M, 4)
  assert.equal(opus.outputPer1M, 20)
  assert.equal(lookupCatalog('gpt-6.1-sol-2026-10-01').id, sol.id)
})

test('unknown variants do not inherit historical prices', () => {
  for (const id of ['gpt-5.99', 'gpt-6.1-sol-custom', 'claude-opus-5-50']) {
    assert.equal(lookupCatalog(id), undefined, id)
  }
  assert.equal(lookupCatalog('gpt-4o').inputPer1M, 2.5)
})
