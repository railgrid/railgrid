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

test('renders preview secondary actions behind an accessible overflow button', async () => {
  const { default: PreviewActionsMenu } = await vite.ssrLoadModule('/src/PreviewActionsMenu.vue')
  const html = await renderToString(createSSRApp(PreviewActionsMenu, {
    templates: [{ name: 'sandbox-runner', displayName: 'Sandbox Runner' }],
    currentTemplate: 'sandbox-runner',
  }))
  assert.match(html, /aria-label="More preview actions"/)
  assert.match(html, /aria-haspopup="dialog"/)
  assert.match(html, /aria-expanded="false"/)
  assert.doesNotMatch(html, /Load from git/)
})

test('keeps template switching and git hydration inside the overflow menu', async () => {
  const source = await readFile(new URL('./PreviewActionsMenu.vue', import.meta.url), 'utf8')
  assert.match(source, /role="dialog"/)
  assert.match(source, /aria-modal="false"/)
  assert.match(source, />Switch template</)
  assert.match(source, /Development templates/)
  assert.match(source, /Load from git/)
  assert.match(source, /emit\('selectTemplate', template\)/)
  assert.match(source, /emit\('loadFromGit'\)/)
  assert.match(source, /event\.key !== 'Escape'/)
})

test('leaves only primary preview actions visible in the toolbar', async () => {
  const source = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const toolbar = source.slice(source.indexOf('<PreviewActionsMenu'), source.indexOf('</div>', source.indexOf('{{ developmentPreviewOpenButtonLabel }}')))
  assert.match(toolbar, /<PreviewActionsMenu/)
  assert.match(toolbar, /title="Sync"/)
  assert.match(toolbar, /developmentPreviewOpenButtonLabel/)
  assert.doesNotMatch(toolbar, /<select/)
})

test('disables external workspace and target changes while an assistant run is active', async () => {
  const menu = await readFile(new URL('./PreviewActionsMenu.vue', import.meta.url), 'utf8')
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(menu, /disabled\?: boolean/)
  assert.match(menu, /props\.disabled \|\| props\.templateBusy/)
  assert.match(menu, /props\.disabled \|\| props\.hydrateBusy/)
  assert.match(app, /:disabled="messageStreaming"/)
  assert.match(app, /messageStreaming \|\| developmentSyncBusy/)
})
