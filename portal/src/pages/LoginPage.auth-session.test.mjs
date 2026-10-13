import assert from 'node:assert/strict'
import test from 'node:test'
import { createRenderer, defineComponent, h, nextTick, onMounted } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import vue from '@vitejs/plugin-vue'
import { createServer } from 'vite'
import { readFileSync } from 'node:fs'
import { parse, compileScript, compileTemplate } from '@vue/compiler-sfc'

const { descriptor } = parse(readFileSync(new URL('./LoginPage.vue', import.meta.url), 'utf8'))
const { bindings } = compileScript(descriptor, { id: 'login-session-test' })
const template = compileTemplate({ source: descriptor.template.content, filename: 'LoginPage.vue',
  id: 'login-session-test', compilerOptions: { bindingMetadata: bindings } })
assert.deepEqual(template.errors, [])

const vite = await createServer({
  appType: 'custom',
  configFile: false,
  root: new URL('../../', import.meta.url).pathname,
  plugins: [vue(), { name: 'login-client-render',
    resolveId(id) { if (id === 'virtual:login-render') return id },
    load(id) { if (id === 'virtual:login-render') return template.code },
  }],
  cacheDir: new URL('../../node_modules/.vite-login-auth-session-test', import.meta.url).pathname,
  resolve: { alias: { '@': new URL('..', import.meta.url).pathname } },
  optimizeDeps: { noDiscovery: true },
  server: { middlewareMode: true, hmr: false, ws: false },
})

const { default: LoginPage } = await vite.ssrLoadModule('/src/pages/LoginPage.vue')
const { useAuthStore } = await vite.ssrLoadModule('/src/stores/auth.ts')
const { peekAppAccessNext } = await vite.ssrLoadModule('/src/auth/appAccessNext.ts')
// SSR loading supplies setup; mount the real compiled client template.
LoginPage.render = (await vite.ssrLoadModule('virtual:login-render')).render
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
    createElement(type) { return { type, props: {}, children: [], parent: null, addEventListener() {}, removeEventListener() {} } },
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

function mountLoginScenario({ expiresAt = 4_000_000_000, healthzStatus = 200, oidcOnly = false, hangHealthz = false } = {}) {
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
  globalThis.fetch = async (input, options = {}) => {
    const path = typeof input === 'string' ? input : input.url
    requests.push(path)
    if (path === '/healthz') {
      if (hangHealthz) return untilAborted(options.signal)
      return new Response(JSON.stringify({ oidc: oidcOnly, tokenLogin: !oidcOnly }), { status: healthzStatus })
    }
    if (path === '/auth/session/bootstrap') {
      beginBootstrap()
      return Promise.race([bootstrapResponse, untilAborted(options.signal)])
    }
    if (path === '/auth/logout') return new Response('{}')
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
    host,
    storedAuth: () => portalStorage.getItem('railgrid-auth'),
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
    assertLoginAvailable(scenario)
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
    assertLoginAvailable(scenario)
    assert.deepEqual(scenario.requests, ['/healthz', '/auth/logout'], 'a stale bearer must not be sent to session bootstrap')
  } finally {
    await scenario.cleanup()
  }
})

function untilAborted(signal) {
  assert.ok(signal, 'initialization fetches must have an abort signal')
  return new Promise((_, reject) => {
    if (signal.aborted) reject(signal.reason)
    else signal.addEventListener('abort', () => reject(signal.reason), { once: true })
  })
}

function elements(node) {
  return [node, ...(node.children ?? []).flatMap(elements)]
}

function textContent(node) {
  return (node.type === '#comment' ? '' : node.text ?? '') + (node.children ?? []).map(textContent).join('')
}

function assertLoginAvailable(scenario, oidcOnly = false) {
  const nodes = elements(scenario.host)
  const alert = nodes.find((node) => node.props?.role === 'alert')
  assert.ok(alert, 'restoration error must be rendered')
  assert.match(textContent(alert), /Could not restore the browser session/)
  assert.equal(scenario.auth.token, null)
  assert.equal(scenario.storedAuth(), null)
  const buttons = nodes.filter((node) => node.type === 'button')
  assert.ok(buttons.some((node) => textContent(node).includes(oidcOnly ? 'Sign in with SSO' : 'Sign in with token')))
  assert.equal(nodes.some((node) => node.type === 'input' && node.props.id === 'token'), !oidcOnly)
}

for (const oidcOnly of [false, true]) {
  test(`rejected cached login clears storage and renders ${oidcOnly ? 'SSO' : 'token'} sign-in`, async () => {
    const scenario = mountLoginScenario({ oidcOnly })
    try {
      await scenario.bootstrapStarted
      scenario.completeBootstrap(401)
      await scenario.appDetection()
      await nextTick()
      assert.deepEqual(scenario.redirects, [])
      assert.equal(scenario.pendingContinuation(), scenario.continuation)
      assertLoginAvailable(scenario, oidcOnly)
    } finally {
      await scenario.cleanup()
    }
  })
}

for (const hangHealthz of [false, true]) {
  test(`hung ${hangHealthz ? 'health discovery and bootstrap' : 'bootstrap'} reaches a usable sign-in page`, async (t) => {
    // Exercise the real abort signals without waiting eight seconds per request.
    t.mock.method(AbortSignal, 'timeout', (milliseconds) => {
      assert.equal(milliseconds, 8000)
      const controller = new AbortController()
      setTimeout(() => controller.abort(new DOMException('Timed out', 'TimeoutError')), 10)
      return controller.signal
    })
    const scenario = mountLoginScenario({ hangHealthz })
    try {
      await scenario.appDetection()
      await nextTick()
      assert.equal(scenario.auth.initialized, true)
      assert.deepEqual(scenario.redirects, [])
      assert.equal(scenario.pendingContinuation(), scenario.continuation)
      assertLoginAvailable(scenario)
    } finally {
      await scenario.cleanup()
    }
  })
}

test('logout during bootstrap cannot restore session readiness', async () => {
  const scenario = mountLoginScenario()
  try {
    await scenario.bootstrapStarted
    scenario.auth.logout()
    scenario.completeBootstrap()
    await scenario.appDetection()
    await nextTick()
    assert.equal(scenario.auth.token, null)
    assert.equal(scenario.auth.browserSessionReady, false)
    assert.equal(scenario.pendingContinuation(), scenario.continuation)
    assert.deepEqual(scenario.redirects, [])
  } finally {
    await scenario.cleanup()
  }
})

for (const oldStatus of [200, 503]) {
  test(`old bootstrap (${oldStatus}) cannot overwrite a newer login`, async () => {
    const scenario = mountLoginScenario()
    try {
      await scenario.bootstrapStarted
      scenario.auth.loginFromOIDCResponse({
        idToken: 'new-synthetic-bearer', expiresAt: 4_000_000_000,
        email: 'new@example.test', userId: 'new-user', clusterName: 'new-cluster',
      })
      scenario.completeBootstrap(oldStatus)
      await scenario.appDetection()
      await nextTick()
      assert.equal(scenario.auth.token, 'new-synthetic-bearer')
      assert.equal(scenario.auth.browserSessionReady, true)
      assert.deepEqual(scenario.redirects, [scenario.continuation])
    } finally {
      await scenario.cleanup()
    }
  })
}
