import assert from 'node:assert/strict'
import test from 'node:test'
import { createRenderer, defineComponent, h, nextTick, onMounted } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import vue from '@vitejs/plugin-vue'
import { createServer } from 'vite'
import { join } from 'node:path'
import { tmpdir } from 'node:os'

const vite = await createServer({
  appType: 'custom',
  configFile: false,
  root: new URL('../../', import.meta.url).pathname,
  plugins: [vue()],
  cacheDir: join(tmpdir(), 'railgrid-login-auth-session-test'),
  resolve: { alias: { '@': new URL('..', import.meta.url).pathname } },
  optimizeDeps: { noDiscovery: true },
  server: { middlewareMode: true, hmr: false, ws: false },
})

const { default: LoginPage } = await vite.ssrLoadModule('/src/pages/LoginPage.vue')
const { useAuthStore } = await vite.ssrLoadModule('/src/stores/auth.ts')
const { peekAppAccessNext } = await vite.ssrLoadModule('/src/auth/appAccessNext.ts')
LoginPage.render = () => null
test.after(() => vite.close())

function storage() {
  const values = new Map()
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  }
}

function createHostRenderer() {
  return createRenderer({
    patchProp(node, key, _previous, value) { node.props[key] = value },
    insert(node, parent, anchor = null) {
      node.parent = parent
      const index = anchor ? parent.children.indexOf(anchor) : -1
      if (index < 0) parent.children.push(node)
      else parent.children.splice(index, 0, node)
    },
    remove(node) {
      if (!node.parent) return
      const index = node.parent.children.indexOf(node)
      if (index >= 0) node.parent.children.splice(index, 1)
      node.parent = null
    },
    createElement(type) { return { type, props: {}, children: [], parent: null } },
    createText(text) { return { type: '#text', text, parent: null } },
    createComment(text) { return { type: '#comment', text, parent: null } },
    setText(node, text) { node.text = text },
    setElementText(node, text) { node.text = text },
    parentNode(node) { return node.parent },
    nextSibling(node) {
      if (!node.parent) return null
      return node.parent.children[node.parent.children.indexOf(node) + 1] ?? null
    },
  })
}

function mountLoginScenario({ expiresAt = 4_000_000_000, healthzStatus = 200 } = {}) {
  const previous = {
    localStorage: globalThis.localStorage,
    sessionStorage: globalThis.sessionStorage,
    window: globalThis.window,
    document: globalThis.document,
    fetch: globalThis.fetch,
  }
  const portalStorage = storage()
  const tabStorage = storage()
  const continuation = '/auth/apps/authorize?app=workshop'
  const redirects = []
  globalThis.localStorage = portalStorage
  globalThis.sessionStorage = tabStorage
  globalThis.window = {
    location: {
      search: `?next=${encodeURIComponent(continuation)}`,
      assign: (destination) => redirects.push(destination),
    },
    dispatchEvent() {},
  }
  globalThis.document = {
    documentElement: {
      classList: { toggle() {} },
      style: { colorScheme: 'light', setProperty() {}, removeProperty() {} },
    },
    querySelector: () => null,
  }
  portalStorage.setItem('railgrid-auth', JSON.stringify({
    idToken: 'synthetic-test-bearer',
    expiresAt,
    email: 'cached@example.test',
    userId: 'cached-user',
  }))

  let beginBootstrap
  const bootstrapStarted = new Promise((resolve) => { beginBootstrap = resolve })
  let resolveBootstrapResponse
  const bootstrapResponse = new Promise((resolve) => { resolveBootstrapResponse = resolve })
  const requests = []
  globalThis.fetch = async (input) => {
    const path = typeof input === 'string' ? input : input.url
    requests.push(path)
    if (path === '/healthz') {
      return new Response(JSON.stringify({ oidc: false }), { status: healthzStatus })
    }
    if (path === '/auth/session/bootstrap') {
      beginBootstrap()
      return bootstrapResponse
    }
    throw new Error(`Unexpected request path: ${path}`)
  }

  const pinia = createPinia()
  setActivePinia(pinia)
  const auth = useAuthStore()
  let appDetection
  const AppShell = defineComponent({
    setup() {
      onMounted(() => { appDetection = auth.detectAuthMode() })
      return () => h(LoginPage)
    },
  })
  const router = createRouter({ history: createMemoryHistory('/ui'), routes: [] })
  const app = createHostRenderer().createApp(AppShell)
  app.use(pinia)
  app.use(router)
  app.provide(Symbol.for('v-scx'), { modules: new Set() })
  const host = { type: 'root', children: [], props: {}, parent: null }
  app.mount(host)

  return {
    auth,
    continuation,
    redirects,
    requests,
    bootstrapStarted,
    completeBootstrap(status = 200) {
      resolveBootstrapResponse(new Response(JSON.stringify({}), { status }))
    },
    appDetection: () => appDetection,
    pendingContinuation: () => peekAppAccessNext(),
    async cleanup() {
      resolveBootstrapResponse(new Response(JSON.stringify({}), { status: 200 }))
      await appDetection
      app.unmount()
      setActivePinia(undefined)
      for (const [key, value] of Object.entries(previous)) {
        if (value === undefined) delete globalThis[key]
        else globalThis[key] = value
      }
    },
  }
}

test('cached login waits for browser session bootstrap before resuming app authorization', async () => {
  const scenario = mountLoginScenario()
  try {
    await scenario.bootstrapStarted
    assert.deepEqual(
      scenario.redirects,
      [],
      'a cached bearer must not resume app authorization before the cookie bootstrap settles',
    )

    scenario.completeBootstrap()
    await scenario.appDetection()
    await nextTick()
    assert.deepEqual(scenario.redirects, [scenario.continuation])
    assert.equal(scenario.auth.browserSessionReady, true)
  } finally {
    await scenario.cleanup()
  }
})

test('cached login resumes after bootstrap succeeds when healthz discovery fails', async () => {
  const scenario = mountLoginScenario({ healthzStatus: 503 })
  try {
    await scenario.bootstrapStarted
    assert.deepEqual(scenario.requests, ['/healthz', '/auth/session/bootstrap'])
    scenario.completeBootstrap()
    await scenario.appDetection()
    await nextTick()

    assert.deepEqual(scenario.redirects, [scenario.continuation])
    assert.equal(scenario.auth.authMode, 'token', 'failed discovery keeps the legacy token-login fallback')
    assert.equal(scenario.auth.browserSessionReady, true)
    assert.equal(scenario.pendingContinuation(), null, 'the successful continuation is consumed once')
    assert.deepEqual(scenario.requests, ['/healthz', '/auth/session/bootstrap'], 'recovery must not retry either request')
  } finally {
    await scenario.cleanup()
  }
})

test('cached login keeps the app continuation pending when browser session bootstrap fails', async () => {
  const scenario = mountLoginScenario()
  try {
    await scenario.bootstrapStarted
    scenario.completeBootstrap(503)
    await scenario.appDetection()
    await nextTick()

    assert.deepEqual(scenario.redirects, [], 'failed bootstrap must not bounce back through app authorization')
    assert.equal(scenario.auth.browserSessionReady, false)
    assert.equal(scenario.pendingContinuation(), scenario.continuation)
  } finally {
    await scenario.cleanup()
  }
})

test('cached login keeps the app continuation pending when the stored bearer is stale', async () => {
  const scenario = mountLoginScenario({ expiresAt: 1 })
  try {
    await scenario.appDetection()
    await nextTick()

    assert.deepEqual(scenario.redirects, [], 'a stale bearer must not resume app authorization')
    assert.equal(scenario.auth.browserSessionReady, false)
    assert.equal(scenario.pendingContinuation(), scenario.continuation)
    assert.deepEqual(scenario.requests, ['/healthz'], 'a stale bearer must not be sent to session bootstrap')
  } finally {
    await scenario.cleanup()
  }
})
