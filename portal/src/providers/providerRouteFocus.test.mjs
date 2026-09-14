import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'
const vite = await createServer({ configFile: false, appType: 'custom',
  optimizeDeps: { noDiscovery: true },
  root: new URL('../../', import.meta.url).pathname,
  server: { middlewareMode: true, hmr: false } })
const { createProviderRouteFocus } = await vite.ssrLoadModule('/src/providers/providerRouteFocus.ts')
test.after(() => vite.close())
test('host restores source focus and fences remounts', () => {
  const previousDocument = globalThis.document
  const previousElement = globalThis.HTMLElement
  class Element {
    constructor(tagName) { this.tagName = tagName; this.isConnected = true }
    hasAttribute() { return false }
    focus() { document.activeElement = this }
  }
  globalThis.HTMLElement = Element
  globalThis.document = { body: {}, activeElement: null }
  try {
    const button = new Element('BUTTON'); const heading = new Element('H1')
    let children = [button]
    const root = { contains: el => children.includes(el), querySelector: () => heading }
    const focus = createProviderRouteFocus()
    focus.before(root, 'issues'); button.focus(); focus.before(root, 'detail')
    button.isConnected = false; children = [heading]; document.activeElement = document.body
    focus.ready(root); assert.equal(document.activeElement, heading)
    focus.before(root, 'issues'); children = [button]; button.isConnected = true
    document.activeElement = document.body; focus.ready(root)
    assert.equal(document.activeElement, button)
    focus.clear(); document.activeElement = document.body; focus.ready(root)
    assert.equal(document.activeElement, document.body)
  } finally { globalThis.document = previousDocument; globalThis.HTMLElement = previousElement }
})
