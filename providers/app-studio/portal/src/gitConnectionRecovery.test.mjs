import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

const vite = await createServer({ appType: 'custom', cacheDir: '/tmp/railgrid-vite-git-recovery', configFile: false, plugins: [vue()], server: { middlewareMode: true, hmr: false } })
const { default: Settings } = await vite.ssrLoadModule('/src/GitConnectionSettings.vue')
test.after(() => vite.close())

for (const canRetryCreation of [false, true]) {
  test(`Git recovery action requires server eligibility: ${canRetryCreation}`, async () => {
    const html = await renderToString(createSSRApp(Settings, { ctx: null, project: { name: 'demo', uid: 'uid', repository: { ref: 'failed-repo', connectionRef: 'github', canRetryCreation, message: 'Repository ownership could not be confirmed.' } } }))
    assert.match(html, /Repository ownership could not be confirmed/)
    if (canRetryCreation) {
      assert.match(html, /Create a new repository<\/span><\/button>/)
      assert.match(html, /previous repository will be left untouched/)
      assert.doesNotMatch(html, /disabled/)
    } else assert.doesNotMatch(html, /Create a new repository/)
  })
}

test('Git status follows the newest commit request and keeps newer workspace changes uncertain', async () => {
  const html = await renderToString(createSSRApp(Settings, { ctx: null, project: {
    name: 'demo',
    repository: {
      ref: 'demo-repo',
      ready: true,
      commits: [
        { name: 'newest', phase: 'Failed', createdAt: '2026-10-12T12:00:00Z' },
        { name: 'older', phase: 'Succeeded', createdAt: '2026-10-12T11:00:00Z' },
      ],
    },
  } }))

  assert.match(html, /Latest Git save failed/)
  assert.doesNotMatch(html, /Latest Git save succeeded/)
})

test('successful Git status refers to the latest commit snapshot only', async () => {
  const html = await renderToString(createSSRApp(Settings, { ctx: null, project: {
    name: 'demo',
    repository: {
      ref: 'demo-repo',
      ready: true,
      commits: [{ name: 'latest', phase: 'Succeeded', createdAt: '2026-10-12T12:00:00Z' }],
    },
  } }))

  assert.match(html, /Latest Git save succeeded\. Newer App Studio changes may not be in Git\./)
})
