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

test('historical variants and latest aliases remain priced', () => {
 for (const [id, input, output] of [
  ['gpt-5.2', 1.75, 14], ['gpt-5-chat-latest', 1.25, 10],
  ['claude-sonnet-4-5-latest', 3, 15], ['claude-3-5-haiku-latest', .8, 4],
  ['gpt-4o-mini-search-preview', .15, .6], ['o3-pro', 20, 80],
 ]) {
  assert.equal(lookupCatalog(id)?.inputPer1M, input, id)
  assert.equal(lookupCatalog(id)?.outputPer1M, output, id)
 }
})
