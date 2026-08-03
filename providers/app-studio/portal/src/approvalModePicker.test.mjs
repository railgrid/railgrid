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

test('renders the current approval mode as an accessible composer control', async () => {
  const { default: ApprovalModePicker } = await vite.ssrLoadModule('/src/ApprovalModePicker.vue')
  const html = await renderToString(createSSRApp(ApprovalModePicker, {
    mode: 'on_request',
  }))
  assert.match(html, /Approval mode: Ask when needed/)
  assert.match(html, /aria-haspopup="dialog"/)
  assert.match(html, /aria-expanded="false"/)
  assert.match(html, />Ask when needed</)
})

test('provides the Codex-style choices and responsive popover placement', async () => {
  const source = await readFile(new URL('./ApprovalModePicker.vue', import.meta.url), 'utf8')
  assert.match(source, /How should App Studio actions be approved\?/)
  assert.match(source, /choose\('on_request'\)/)
  assert.match(source, /choose\('always_ask'\)/)
  assert.match(source, /choose\('never'\)/)
  assert.match(source, /role="dialog"/)
  assert.match(source, /:aria-pressed="mode === 'always_ask'"/)
  assert.match(source, /if \(!open\.value \|\| event\.key !== 'Escape'\) return/)
  assert.match(source, /fixed inset-x-3 bottom-3/)
  assert.match(source, /md:absolute/)
})

test('defaults the App Studio composer to ask when needed', async () => {
  const source = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(source, /ref<ProjectAssistantApprovalMode>\('on_request'\)/)
  assert.match(source, /preference\?\.mode \?\? 'on_request'/)
})
