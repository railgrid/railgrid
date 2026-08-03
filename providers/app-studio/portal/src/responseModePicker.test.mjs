import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true } })
})
test.after(async () => vite?.close())

test('renders the current response mode as an accessible composer control', async () => {
  const { default: ResponseModePicker } = await vite.ssrLoadModule('/src/ResponseModePicker.vue')
  const html = await renderToString(createSSRApp(ResponseModePicker, {
    mode: 'default',
  }))
  assert.match(html, /Response mode: Default/)
  assert.match(html, /aria-haspopup="dialog"/)
  assert.match(html, /aria-expanded="false"/)
  assert.match(html, />Default</)
})

test('provides explicit default, plan, and review choices in one responsive popover', async () => {
  const source = await readFile(new URL('./ResponseModePicker.vue', import.meta.url), 'utf8')
  assert.match(source, /How should App Studio respond\?/)
  assert.match(source, /chooseMode\('default'\)/)
  assert.match(source, /chooseMode\('plan'\)/)
	assert.match(source, /chooseMode\('review'\)/)
  assert.match(source, /produce a plan without changing the project/)
	assert.match(source, /report prioritized findings without changing it/)
  assert.doesNotMatch(source, /chooseMode\('build'\)/)
  assert.doesNotMatch(source, /chooseMode\('auto'\)/)
  assert.match(source, /if \(!open\.value \|\| event\.key !== 'Escape'\) return/)
  assert.match(source, /fixed inset-x-3 bottom-3/)
  assert.match(source, /overflow-y-auto/)
  assert.match(source, /md:absolute/)
})

test('composer mounts both current settings', async () => {
  const source = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(source, /<ResponseModePicker/)
  assert.match(source, /<ApprovalModePicker/)
  assert.match(source, /right-12 flex min-w-0/)
})
