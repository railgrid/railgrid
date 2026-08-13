import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/faros-vite-clipboard',
  configFile: false,
  server: { middlewareMode: true },
})
const { copyTextWithFallback } = await vite.ssrLoadModule('/src/clipboard.ts')
test.after(async () => vite.close())

test('uses the Clipboard API when the embedded surface is permitted', async () => {
  const writes = []
  assert.equal(await copyTextWithFallback(' https://app.example.test ', {
    async writeText(value) { writes.push(value) },
  }, null), true)
  assert.deepEqual(writes, ['https://app.example.test'])
})

test('falls back to a selected textarea when Clipboard API permission is denied', async () => {
  let selected = false
  let removed = false
  const textarea = {
    value: '', readOnly: false, style: {},
    setAttribute() {}, focus() {},
    select() { selected = true },
    setSelectionRange() {},
    remove() { removed = true },
  }
  const doc = {
    body: { appendChild() {} },
    activeElement: null,
    createElement() { return textarea },
    execCommand(command) { return command === 'copy' && selected },
  }
  const copied = await copyTextWithFallback('https://app.example.test', {
    async writeText() { throw new Error('permission denied') },
  }, doc)
  assert.equal(copied, true)
  assert.equal(removed, true)
})

test('reports false when neither copy mechanism is available', async () => {
  assert.equal(await copyTextWithFallback('https://app.example.test', null, null), false)
  assert.equal(await copyTextWithFallback('   ', null, null), false)
})
