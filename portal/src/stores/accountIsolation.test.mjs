import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer, transformWithEsbuild } from 'vite'
import { parse, compileScript } from '@vue/compiler-sfc'
import { createRenderer, nextTick } from 'vue'
import { createPinia, setActivePinia, disposePinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import { join } from 'node:path'
import { tmpdir } from 'node:os'

// Render the real component with an in-memory Vue host. Only the browser's
// terminal and socket transports are doubles; auth, stores and routing are real.
const terminals = []
const sockets = []
class FakeTerminal {
  cols = 80; rows = 24; disposed = false; data = null; resize = null
  constructor() { terminals.push(this) }
  loadAddon() {} open() {} write() {} focus() {} clear() {}
  onData(callback) { this.data = callback; return { dispose: () => { this.data = null } } }
  onResize(callback) { this.resize = callback; return { dispose: () => { this.resize = null } } }
  dispose() { this.disposed = true }
}
class FakeSocket {
  static CONNECTING = 0; static OPEN = 1; static CLOSED = 3
  readyState = 0; sent = []; closed = false
  constructor(url) { this.url = url; sockets.push(this) }
  send(data) { this.sent.push(data) }
  close() { this.closed = true; this.readyState = FakeSocket.CLOSED }
}
globalThis.__accountIsolationTerminal = FakeTerminal
const vite = await createServer({
  appType: 'custom', configFile: false, root: new URL('../../', import.meta.url).pathname,
  cacheDir: join(tmpdir(), 'faros-account-isolation-test'),
  resolve: { alias: { '@': new URL('../', import.meta.url).pathname } },
  ssr: { noExternal: [/^@xterm\//] },
  optimizeDeps: { noDiscovery: true }, server: { middlewareMode: true, hmr: false, ws: false },
  plugins: [{
    name: 'terminal-test-transports', enforce: 'pre',
    resolveId(id) { if (id.startsWith('@xterm/')) return '\0' + id },
    load(id) {
      if (id === '\0@xterm/xterm') return 'export const Terminal = globalThis.__accountIsolationTerminal'
      if (id === '\0@xterm/addon-fit') return 'export class FitAddon { fit() {} }'
      if (id === '\0@xterm/xterm/css/xterm.css') return ''
    },
    async transform(source, id) {
      if (!id.endsWith('/TerminalInstance.vue')) return
      const { descriptor } = parse(source)
      const compiled = compileScript(descriptor, { id: 'terminal-test', inlineTemplate: true })
      return transformWithEsbuild(compiled.content, id + '.ts', { loader: 'ts' })
    },
  }],
})
const { useAuthStore } = await vite.ssrLoadModule('/src/stores/auth.ts')
const { useAdminStore } = await vite.ssrLoadModule('/src/stores/admin.ts')
const { useTerminalSessionsStore } = await vite.ssrLoadModule('/src/stores/terminalSessions.ts')
const { default: TerminalInstance } = await vite.ssrLoadModule('/src/components/TerminalInstance.vue')
const { routes } = await vite.ssrLoadModule('/src/router/routes.ts')
const { installContextGuard } = await vite.ssrLoadModule('/src/router/contextGuard.ts')
test.after(() => { delete globalThis.__accountIsolationTerminal; return vite.close() })
const node = (type, text = '') => ({ type, text, children: [], parent: null })
const renderer = createRenderer({
  createElement: node, createText: text => node('text', text), createComment: text => node('comment', text),
  setText: (el, text) => { el.text = text }, setElementText: (el, text) => { el.text = text },
  parentNode: el => el.parent, nextSibling: el => el.parent?.children[el.parent.children.indexOf(el) + 1],
  patchProp() {},
  insert(el, parent, anchor) {
    if (el.parent) el.parent.children.splice(el.parent.children.indexOf(el), 1)
    const index = anchor ? parent.children.indexOf(anchor) : -1
    parent.children.splice(index < 0 ? parent.children.length : index, 0, el)
    el.parent = parent
  },
  remove(el) { if (el.parent) el.parent.children.splice(el.parent.children.indexOf(el), 1); el.parent = null },
})
const response = (body, status = 200) => new Response(JSON.stringify(body), { status })
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r }); return { promise, resolve } }
const flush = async () => { await nextTick(); await new Promise(resolve => setImmediate(resolve)) }
function setup(t) {
  const data = new Map()
  globalThis.localStorage = { getItem: key => data.get(key) ?? null, setItem: (key, value) => data.set(key, value), removeItem: key => data.delete(key) }
  globalThis.sessionStorage = globalThis.localStorage
  globalThis.location = { protocol: 'https:', host: 'faros.test', pathname: '/ui/bonkers/users' }
  globalThis.window = { location: globalThis.location, dispatchEvent() {} }
  globalThis.WebSocket = FakeSocket
  globalThis.fetch = async () => response({})
  sockets.length = terminals.length = 0
  const pinia = createPinia()
  setActivePinia(pinia)
  const auth = useAuthStore()
  const login = (userId = 'admin') => auth.loginFromOIDCResponse({ idToken: userId + '-token', expiresAt: 9999999999, email: userId + '@example.test', userId, clusterName: 'cluster-a' })
  login()
  auth.initialized = true
  t.after(() => disposePinia(pinia))
  return { auth, login, pinia, admin: useAdminStore(), sessions: useTerminalSessionsStore() }
}
function mountTerminal(t, pinia) {
  const app = renderer.createApp(TerminalInstance, { edgeName: 'private-server', cluster: 'cluster-a', isActive: true })
  app.use(pinia)
  const component = app.mount(node('root'))
  t.after(() => app.unmount())
  return { app, component }
}

test('logout closes live SSH immediately and removes all sessions; navigation and token rotation preserve them', async t => {
  const { auth, login, pinia, sessions } = setup(t)
  sessions.openSession({ edgeName: 'private-server', cluster: 'cluster-a' })
  sessions.openSession({ edgeName: 'another-server', cluster: 'cluster-b' })
  const { component } = mountTerminal(t, pinia)
  await flush()
  assert.equal(sockets.length, 1)
  const socket = sockets[0], terminal = terminals[0]
  socket.readyState = FakeSocket.OPEN
  socket.onopen()
  terminal.data('whoami\n')
  assert.ok(socket.sent.some(message => JSON.parse(message).type === 'cmd'))
  auth.token = 'rotated-token'
  auth.setClusterName('cluster-b')
  await flush()
  assert.equal(socket.closed, false)
  assert.equal(sessions.sessions.length, 2)
  auth.logout()
  // Assert before Vue's next render: an open transport must close synchronously.
  assert.equal(socket.closed, true)
  assert.equal(terminal.disposed, true)
  assert.equal(terminal.data, null)
  assert.equal(terminal.resize, null)
  assert.equal(socket.onmessage, null)
  assert.deepEqual(sessions.sessions, [])
  assert.equal(sessions.activeSessionId, null)
  assert.equal(sessions.isVisible, false)
  login('member')
  await component.reconnect()
  assert.equal(sockets.length, 1, 'old component cannot reconnect as the next account')
})

test('direct identity replacement closes a connecting socket', async t => {
  const { login, pinia, sessions } = setup(t)
  sessions.openSession({ edgeName: 'private-server', cluster: 'cluster-a' })
  mountTerminal(t, pinia)
  await flush()
  assert.equal(sockets[0].readyState, FakeSocket.CONNECTING)
  login('member')
  assert.equal(sockets[0].closed, true)
  assert.deepEqual(sessions.sessions, [])
})

test('a pending token cannot create a socket after logout, identity replacement or unmount', async t => {
  for (const action of ['logout', 'replace', 'unmount']) {
    await t.test(action, async t => {
      const { auth, login, pinia } = setup(t)
      const token = deferred()
      auth.getValidToken = () => token.promise
      const { app } = mountTerminal(t, pinia)
      await flush()
      assert.equal(terminals.length, 1)
      if (action === 'logout') auth.logout()
      else if (action === 'replace') login('member')
      else app.unmount()
      token.resolve('old-admin-token')
      await flush()
      assert.equal(sockets.length, 0)
      assert.equal(terminals[0].disposed, true)
    })
  }
})

test('reconnect fences the previous token attempt and keeps only the replacement transport', async t => {
  const { auth, pinia } = setup(t)
  const old = deferred()
  auth.getValidToken = () => old.promise
  const { component } = mountTerminal(t, pinia)
  await flush()
  auth.getValidToken = async () => 'new-token'
  await component.reconnect()
  old.resolve('old-token')
  await flush()
  assert.equal(sockets.length, 1)
  assert.ok(sockets[0].url.endsWith('token=new-token'))
  assert.equal(terminals[0].disposed, true)
  assert.equal(terminals[1].disposed, false)
})

async function seedAdmin(admin) {
  globalThis.fetch = async path => response(path === '/api/admin/access'
    ? { kubeconfigServers: ['internal', 'external'] } : { items: [{ name: 'private-' + path }] })
  assert.equal(await admin.checkAccess(), true)
  await admin.refresh()
  assert.equal(admin.loaded, true)
}
function assertEmptyAdmin(admin) {
  for (const key of ['users', 'orgs', 'providers', 'identities', 'kubeconfigServers']) assert.deepEqual(admin[key], [], key)
  assert.equal(admin.isAdmin, null)
  assert.equal(admin.loaded, false)
  assert.equal(admin.loading, false)
  assert.equal(admin.forbidden, false)
  assert.equal(admin.error, null)
}

test('logout resets admin data and access; the next account must pass a fresh route check', async t => {
  const { auth, login, admin } = setup(t)
  await seedAdmin(admin)
  auth.token = 'rotated-token'
  auth.setClusterName('cluster-b')
  assert.equal(admin.isAdmin, true)
  assert.equal(admin.users.length, 1)
  auth.logout()
  assertEmptyAdmin(admin)
  login('member')
  const calls = []
  globalThis.fetch = async path => { calls.push(path); return response({}, 403) }
  const router = createRouter({ history: createMemoryHistory('/ui/'), routes })
  for (const record of router.getRoutes()) if (record.components) record.components.default = { render: () => null }
  installContextGuard(router)
  await router.push('/bonkers/users')
  assert.ok(calls.includes('/api/admin/access'))
  assert.equal(admin.isAdmin, false)
  assert.notEqual(router.currentRoute.value.path, '/bonkers/users')
  assert.deepEqual(admin.users, [])
})

test('late admin access and collection bodies cannot restore prior-account data or end a new refresh', async t => {
  const { login, admin } = setup(t)
  const accessBody = deferred(), collectionBody = deferred(), newBody = deferred()
  globalThis.fetch = async path => ({ ok: true, status: 200, json: () => path === '/api/admin/access' ? accessBody.promise : collectionBody.promise })
  const oldAccess = admin.checkAccess(), oldRefresh = admin.refresh()
  await flush()
  login('member')
  assertEmptyAdmin(admin)
  globalThis.fetch = async () => ({ ok: true, status: 200, json: () => newBody.promise })
  const newRefresh = admin.refresh()
  await flush()
  accessBody.resolve({ kubeconfigServers: ['internal'] })
  collectionBody.resolve({ items: [{ name: 'private-old-account' }] })
  assert.equal(await oldAccess, false)
  await oldRefresh
  assert.equal(admin.isAdmin, null)
  assert.deepEqual(admin.kubeconfigServers, [])
  assert.deepEqual(admin.users, [])
  assert.equal(admin.loading, true)
  newBody.resolve({ items: [{ name: 'new-account' }] })
  await newRefresh
  assert.equal(admin.loading, false)
  assert.equal(admin.users[0].name, 'new-account')
})

test('stale denied responses cannot overwrite the new account admin state', async t => {
  const { login, admin } = setup(t)
  const pending = deferred()
  globalThis.fetch = () => pending.promise
  const access = admin.checkAccess(), refresh = admin.refresh()
  await flush()
  login('other-admin')
  await seedAdmin(admin)
  pending.resolve(response({}, 403))
  await Promise.all([access, refresh])
  assert.equal(admin.isAdmin, true)
  assert.equal(admin.forbidden, false)
  assert.equal(admin.loaded, true)
  assert.equal(admin.users.length, 1)
})

test('an in-flight kubeconfig download cannot deliver old-account credentials', async t => {
  const { login, admin } = setup(t)
  const body = deferred()
  let downloads = 0
  globalThis.document = { createElement() { downloads++; return { click() {} } } }
  t.after(() => { delete globalThis.document })
  globalThis.fetch = async () => ({ ok: true, status: 200, text: () => body.promise })
  const download = admin.downloadProviderKubeconfig('private-provider', 'internal')
  const rejected = assert.rejects(download, { name: 'AbortError' })
  await flush()
  login('member')
  body.resolve('private-kubeconfig')
  await rejected
  assert.equal(downloads, 0)
})

test('delayed OIDC refresh cannot resurrect credentials or dispatch an admin request after the session changes', async t => {
  const { authFetch } = await vite.ssrLoadModule('/src/auth/session.ts')
  const { loadAuth } = await vite.ssrLoadModule('/src/auth/token.ts')
  for (const action of ['logout', 'replace', 'same-account-relogin']) {
    await t.test(action, async t => {
      const { auth, login } = setup(t)
      auth.loginFromOIDCResponse({ idToken: 'expired-admin-token', expiresAt: 1,
        email: 'admin@example.test', userId: 'admin', refreshToken: 'old-refresh',
        issuerUrl: 'https://issuer.test', clientId: 'portal' })
      const tokenBody = deferred()
      let exchanges = 0
      const requests = []
      globalThis.fetch = async path => {
        requests.push(path)
        if (path.endsWith('/.well-known/openid-configuration')) return response({ token_endpoint: 'https://issuer.test/token' })
        if (path === 'https://issuer.test/token') {
          exchanges++
          return { ok: true, json: () => tokenBody.promise }
        }
        return response({})
      }
      const terminalToken = assert.rejects(auth.getValidToken(), { name: 'AbortError' })
      const adminRequest = assert.rejects(authFetch('/api/admin/users'), { name: 'AbortError' })
      await flush()
      assert.equal(exchanges, 2)
      auth.logout()
      if (action !== 'logout') login(action === 'replace' ? 'member' : 'admin')
      tokenBody.resolve({ id_token: 'refreshed-old-admin-token', expires_in: 3600 })
      await Promise.all([terminalToken, adminRequest])
      assert.equal(requests.includes('/api/admin/users'), false)
      assert.equal(auth.token, action === 'logout' ? null : action === 'replace' ? 'member-token' : 'admin-token')
      assert.equal(loadAuth()?.idToken ?? null, auth.token)
    })
  }
})

test('an old request returning 401 cannot expire the replacement account', async t => {
  const { login, auth } = setup(t)
  const { authFetch } = await vite.ssrLoadModule('/src/auth/session.ts')
  const pending = deferred()
  let expired = 0
  window.dispatchEvent = () => { expired++ }
  globalThis.fetch = () => pending.promise
  const rejected = assert.rejects(authFetch('/api/admin/users'), { name: 'AbortError' })
  await flush()
  login('member')
  pending.resolve(response({}, 401))
  await rejected
  assert.equal(expired, 0)
  assert.equal(auth.user.userId, 'member')
})

test('normal OIDC refresh preserves the same account admin cache and returns a usable bearer', async t => {
  const { auth, admin } = setup(t)
  await seedAdmin(admin)
  auth.loginFromOIDCResponse({ idToken: 'expired-token', expiresAt: 1,
    email: 'admin@example.test', userId: 'admin', refreshToken: 'refresh',
    issuerUrl: 'https://issuer.test', clientId: 'portal' })
  globalThis.fetch = async path => path.endsWith('/.well-known/openid-configuration')
    ? response({ token_endpoint: 'https://issuer.test/token' })
    : response({ id_token: 'refreshed-token', expires_in: 3600 })
  assert.equal(await auth.getValidToken(), 'refreshed-token')
  assert.equal(auth.token, 'refreshed-token')
  assert.equal(admin.isAdmin, true)
  assert.equal(admin.users.length, 1)
})
