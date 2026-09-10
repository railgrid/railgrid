import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

let vite
test.before(async () => {
  vite = await createServer({ appType: 'custom', server: { middlewareMode: true, hmr: false } })
})
test.after(async () => vite?.close())

test('renders the current approval mode as an accessible composer control', async () => {
  const { default: ApprovalModePicker } = await vite.ssrLoadModule('/src/ApprovalModePicker.vue')
  const html = await renderToString(createSSRApp(ApprovalModePicker, {
    mode: 'on_request',
  }))
  assert.match(html, /Approval mode: Ask when needed/)
  assert.match(html, /aria-haspopup="menu"/)
  assert.match(html, /aria-expanded="false"/)
  assert.match(html, />Ask when needed</)
})

test('provides the Codex-style choices and responsive popover placement', async () => {
  const [source, menuSource] = await Promise.all([
    readFile(new URL('./ApprovalModePicker.vue', import.meta.url), 'utf8'),
    readFile(new URL('./useModePickerMenu.ts', import.meta.url), 'utf8'),
  ])
  assert.match(source, /How should App Studio actions be approved\?/)
  assert.match(source, /choose\('on_request'\)/)
  assert.match(source, /choose\('always_ask'\)/)
  assert.match(source, /choose\('never'\)/)
  assert.match(source, /Run routine workspace, build, test, and lint actions automatically/)
  assert.match(source, /role="menu"/)
  assert.match(source, /role="menuitemradio"/)
  assert.match(source, /:aria-checked="mode === 'always_ask'"/)
  assert.match(menuSource, /useAnchoredPopover\(/)
  assert.match(menuSource, /closeMenuAfterTab\(\)/)
  assert.match(menuSource, /event\.key === 'Home'/)
  assert.match(menuSource, /event\.key === 'End'/)
  assert.match(menuSource, /event\.key === 'ArrowDown'/)
  assert.match(menuSource, /event\.key === 'ArrowUp'/)
  assert.match(source, /overflow-y-auto/)
  assert.match(source, /max-h-\[calc\(100dvh-1rem\)\]/)
})

test('defaults the App Studio composer to ask when needed', async () => {
  const source = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(source, /ref<ProjectAssistantApprovalMode>\('on_request'\)/)
  assert.match(source, /preference\?\.mode \?\? 'on_request'/)
})
