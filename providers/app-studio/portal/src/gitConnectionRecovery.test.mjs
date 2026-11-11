import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp, onServerPrefetch } from 'vue'
import { renderToString } from 'vue/server-renderer'

const vite = await createServer({ appType: 'custom', cacheDir: '/tmp/railgrid-vite-git-recovery', configFile: false, plugins: [vue()], server: { middlewareMode: true, hmr: false } })
const { default: Settings } = await vite.ssrLoadModule('/src/GitConnectionSettings.vue')
const { api, ProjectAPIRequestError } = await vite.ssrLoadModule('/src/api.ts')
test.after(() => vite.close())

function renderOpenSettings(afterOpening) {
  const settings = {
    ...Settings,
    setup(props, context) {
      const state = Settings.setup(props, context)
      onServerPrefetch(() => afterOpening(state.check))
      return state
    },
  }
  return renderToString(createSSRApp(settings, { open: true, ctx: null, project: { name: 'demo' } }))
}

test('opening project settings with Git collapsed does not check for a connection', async (t) => {
  const check = t.mock.method(api, 'getProjectCreateReadiness', async () => {
    throw new Error('list Code connections: access denied')
  })
  const html = await renderToString(createSSRApp(Settings, { ctx: null, project: { name: 'demo' } }))

  assert.equal(check.mock.callCount(), 0)
  assert.match(html, /Not connected/)
  assert.doesNotMatch(html, /role="alert"/)
})

for (const { status, retry } of [{ status: 403, retry: false }, { status: 403, retry: true }, { status: 503, retry: false }]) {
  test(`Git readiness failure keeps setup actionable (status: ${status}, retry: ${retry})`, async (t) => {
    let requests = 0
    const failure = new ProjectAPIRequestError('list Code connections: request failed', status)
    const warn = t.mock.method(console, 'warn', () => {})
    const check = t.mock.method(api, 'getProjectCreateReadiness', async () => {
      if (++requests === 1) throw failure
      return { gitConnection: { ready: false, status: 'provider-missing' } }
    })
    const html = await renderOpenSettings(async (refresh) => {
      // Let the initial rejection settle before rendering or retrying.
      await Promise.resolve()
      if (retry) await refresh()
    })

    assert.equal(check.mock.callCount(), retry ? 2 : 1)
    assert.equal(warn.mock.callCount(), 1)
    assert.equal(warn.mock.calls[0].arguments[1], failure)
    assert.doesNotMatch(html, /list Code connections|role="alert"/)
    if (retry) {
      assert.match(html, /Enable Code provider/)
      assert.doesNotMatch(html, /Couldn’t check the Git connection/)
    } else {
      assert.match(html, /Couldn’t check the Git connection/)
      if (status === 403) assert.match(html, /View providers/)
      else assert.doesNotMatch(html, /View providers/)
      assert.match(html, /Check again/)
    }
  })
}

test('a newer Git check takes precedence over an in-flight check', async (t) => {
  let finishFirstCheck
  let requests = 0
  t.mock.method(api, 'getProjectCreateReadiness', () => {
    if (++requests === 1) return new Promise(resolve => { finishFirstCheck = resolve })
    return Promise.resolve({ gitConnection: { ready: true, connectionRef: 'github' } })
  })

  const html = await renderOpenSettings(async (refresh) => {
    await refresh()
    finishFirstCheck({ gitConnection: { ready: false, status: 'connection-missing' } })
    await Promise.resolve()
  })

  assert.equal(requests, 2)
  assert.match(html, /Create repository and save project files/)
  assert.doesNotMatch(html, /Connect GitHub/)
})

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
