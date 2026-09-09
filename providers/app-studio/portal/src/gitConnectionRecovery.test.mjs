import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

const vite = await createServer({ appType: 'custom', cacheDir: '/tmp/faros-vite-git-recovery', configFile: false, plugins: [vue()], server: { middlewareMode: true, hmr: false } })
const { default: Settings } = await vite.ssrLoadModule('/src/GitConnectionSettings.vue')
test.after(() => vite.close())

for (const canRetryCreation of [false, true]) {
  test(`Git recovery action requires server eligibility: ${canRetryCreation}`, async () => {
    const html = await renderToString(createSSRApp(Settings, { ctx: null, project: { name: 'demo', uid: 'uid', repository: { ref: 'failed-repo', connectionRef: 'github', canRetryCreation, message: 'Repository ownership could not be confirmed.' } } }))
    assert.match(html, /Repository ownership could not be confirmed/)
    if (canRetryCreation) {
      assert.match(html, /Create a new repository<\/button>/)
      assert.match(html, /previous repository will be left untouched/)
      assert.doesNotMatch(html, /disabled/)
    } else assert.doesNotMatch(html, /Create a new repository/)
  })
}
