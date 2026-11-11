import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp, onServerPrefetch } from 'vue'
import { renderToString } from 'vue/server-renderer'

const vite = await createServer({ appType: 'custom', cacheDir: '/tmp/railgrid-vite-git-recovery', configFile: false, plugins: [vue()], server: { middlewareMode: true, hmr: false } })
const { default: Settings } = await vite.ssrLoadModule('/src/GitConnectionSettings.vue')
const { api } = await vite.ssrLoadModule('/src/api.ts')
test.after(() => vite.close())

test('opening project settings with Git collapsed does not check for a connection', async (t) => {
  const check = t.mock.method(api, 'getProjectCreateReadiness', async () => {
    throw new Error('list Code connections: access denied')
  })
  const html = await renderToString(createSSRApp(Settings, { ctx: null, project: { name: 'demo' } }))

  assert.equal(check.mock.callCount(), 0)
  assert.match(html, /Not connected/)
  assert.doesNotMatch(html, /role="alert"/)
})

for (const retry of [false, true]) {
  test(`Git readiness access denial keeps setup actionable (retry: ${retry})`, async (t) => {
    let requests = 0
    const check = t.mock.method(api, 'getProjectCreateReadiness', async () => {
      if (++requests === 1) throw new Error('list Code connections: access denied')
      return { gitConnection: { ready: false, status: 'provider-missing' } }
    })
    const settings = {
      ...Settings,
      setup(props, context) {
        const state = Settings.setup(props, context)
        onServerPrefetch(async () => {
          // Let the initial rejection settle before rendering or retrying.
          await Promise.resolve()
          if (retry) await state.check()
        })
        return state
      },
    }
    const html = await renderToString(createSSRApp(settings, { open: true, ctx: null, project: { name: 'demo' } }))

    assert.equal(check.mock.callCount(), retry ? 2 : 1)
    assert.doesNotMatch(html, /access denied|role="alert"/)
    if (retry) {
      assert.match(html, /Enable Code provider/)
      assert.doesNotMatch(html, /Couldn’t check the Git connection/)
    } else {
      assert.match(html, /Couldn’t check the Git connection/)
      assert.match(html, /View providers/)
      assert.match(html, /Check again/)
    }
  })
}

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

test('keeps repository errors visible when the Git disclosure is closed', async () => {
  const source = await readFile(new URL('./GitConnectionSettings.vue', import.meta.url), 'utf8')
  assert.match(source, /<\/AIWorkbenchSection>\s*<p v-if="error" role="alert"/)
})
