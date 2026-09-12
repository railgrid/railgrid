import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, nextTick, shallowReactive, watch } from 'vue'
import { createServer } from 'vite'
import { createPinia, setActivePinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import { join } from 'node:path'
import { tmpdir } from 'node:os'

const vite = await createServer({
  appType: 'custom', configFile: false, root: new URL('../../', import.meta.url).pathname,
  cacheDir: join(tmpdir(), 'faros-scoped-navigation-test'),
  resolve: { alias: { '@': new URL('../', import.meta.url).pathname } },
  optimizeDeps: { noDiscovery: true }, server: { middlewareMode: true, hmr: false, ws: false },
})
const { routes } = await vite.ssrLoadModule('/src/router/routes.ts')
const { registerProviderRoutes } = await vite.ssrLoadModule('/src/router/providers.ts')
const { installContextGuard } = await vite.ssrLoadModule('/src/router/contextGuard.ts')
const { useTenantStore } = await vite.ssrLoadModule('/src/stores/tenant.ts')
const { useRouteContextStore } = await vite.ssrLoadModule('/src/stores/routeContext.ts')
const { useAuthStore } = await vite.ssrLoadModule('/src/stores/auth.ts')
const { scopedPath, parsePortalScope, portalHref, portalRoutePath } = await vite.ssrLoadModule('/src/portalkit/navigation.ts')
const { readTenant } = await vite.ssrLoadModule('/src/portalkit/tenant.ts')
const { validPortalNext, consumePortalNext } = await vite.ssrLoadModule('/src/auth/portalNext.ts')
const { consumeAppAccessNext, rememberAppAccessNext } = await vite.ssrLoadModule('/src/auth/appAccessNext.ts')
test.after(() => vite.close())
const O = '11111111-1111-4111-8111-111111111111'
const W = '22222222-2222-4222-8222-222222222222'
const B = '33333333-3333-4333-8333-333333333333'
const resource = `/${O}/${W}/providers/infrastructure/instances/shared?tab=logs#output`
const response = (data, status = 200) => new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
function storage() {
  const data = new Map()
  return { getItem: key => data.get(key) ?? null, setItem: (key, value) => data.set(key, value), removeItem: key => data.delete(key) }
}
function setup({ authenticated = true } = {}) {
  globalThis.localStorage = storage()
  globalThis.sessionStorage = storage()
  globalThis.window = { location: { pathname: '/ui' + resource.split('?')[0] }, dispatchEvent() {} }
  localStorage.setItem('faros:portal:tenant', JSON.stringify({ orgUUID: B, workspaceUUID: B }))
  if (authenticated) localStorage.setItem('faros-auth', JSON.stringify({ idToken: 'test-token', expiresAt: 9999999999, email: 'teammate@example.test', userId: 'teammate' }))
  setActivePinia(createPinia())
  const auth = useAuthStore()
  auth.initialized = true
  const router = createRouter({ history: createMemoryHistory('/ui/'), routes })
  registerProviderRoutes(router)
  // Exercise real route records and guards without rendering Vue components.
  for (const record of router.getRoutes()) if (record.components) record.components.default = { render: () => null }
  installContextGuard(router)
  const calls = []
  globalThis.fetch = async (path, init) => {
    calls.push({ path, headers: new Headers(init?.headers) })
    if (path === `/api/orgs/${O}`) return response({ uuid: O, displayName: 'Team', personal: false })
    if (path === '/api/orgs') return response({ items: [{ uuid: O, displayName: 'Team' }] })
    if (path === `/api/orgs/${O}/workspaces`) return response({ items: [{ uuid: W, orgUUID: O, clusterName: 'cluster-w' }] })
    if (path === `/api/orgs/${O}/workspaces/${W}` || path === `/api/orgs/${O}/workspaces/${B}`) {
      const id = path.endsWith(W) ? W : B
      return response({ uuid: id, orgUUID: O, displayName: 'Workspace', clusterName: `cluster-${id}` })
    }
    return response({}, 404)
  }
  return { router, calls, auth, tenant: useTenantStore(), context: useRouteContextStore() }
}

test('canonical routes resolve exact IDs and preserve provider suffix, query and fragment', async () => {
  const { router, tenant, calls, auth } = setup()
  await router.push(resource)
  assert.equal(router.currentRoute.value.name, 'provider-frame')
  assert.equal(router.currentRoute.value.fullPath, resource)
  assert.equal(router.resolve(resource).href, '/ui' + resource)
  assert.equal(tenant.orgUUID, O)
  assert.equal(tenant.workspaceUUID, W)
  assert.equal(auth.clusterName, `cluster-${W}`)
  assert.deepEqual(calls.map(call => call.path), [`/api/orgs/${O}`, `/api/orgs/${O}/workspaces/${W}`])
  assert.equal(calls[1].headers.get('X-Faros-Workspace'), W)
  assert.equal(calls[1].headers.get('X-Faros-Org'), O)
  assert.equal(calls[1].headers.get('Authorization'), 'Bearer test-token')
  assert.equal(portalRoutePath(router.currentRoute.value.path), '/providers/infrastructure/instances/shared')
  assert.equal(scopedPath('/settings/workspaces', tenant), `/${O}/settings/workspaces`)
})

test('all old scoped entry points are not-found; global routes cannot be parsed as IDs', () => {
  const { router } = setup()
  for (const path of ['/providers', '/providers/infrastructure/instances/shared', '/settings/workspaces', '/mcp', '/not-an-org/not-a-workspace/providers/code']) {
    assert.equal(router.resolve(path).name, 'not-found', path)
  }
  for (const path of ['/login', '/auth/callback', '/organizations', '/organizations/new', '/bonkers/providers']) assert.equal(parsePortalScope(path), null)
  assert.equal(router.resolve(`/${O}/settings/workspaces/${W}`).params.workspaceUUID, W)
})

test('unauthenticated deep links survive a per-tab single-use login continuation', async () => {
  const { router } = setup({ authenticated: false })
  await router.push(resource)
  assert.equal(router.currentRoute.value.name, 'login')
  assert.equal(router.currentRoute.value.query.returnTo, resource)
  assert.equal(consumePortalNext(), resource)
  assert.equal(consumePortalNext(), null)
  for (const unsafe of ['https://evil.test', '//evil.test', '/\\evil.test', '/login', '/auth/callback', '/%5cevil.test', '/a\nb']) assert.equal(validPortalNext(unsafe), null, unsafe)
  rememberAppAccessNext('/auth/apps/authorize?app=example')
  assert.equal(consumeAppAccessNext(), '/auth/apps/authorize?app=example')
})

test('cross-tab storage changes cannot retarget a hosted page or native link', async () => {
  const { router, tenant } = setup()
  await router.push(resource)
  localStorage.setItem('faros:portal:tenant', JSON.stringify({ orgUUID: B, workspaceUUID: B }))
  assert.deepEqual(readTenant(), { orgUUID: O, workspaceUUID: W })
  assert.equal(tenant.workspaceUUID, W)
  assert.equal(portalHref('/providers/code/repositories'), `/ui/${O}/${W}/providers/code/repositories`)
  window.location.pathname = `/ui/${O}/settings/workspaces`
  assert.deepEqual(readTenant(), { orgUUID: O, workspaceUUID: null })
})

test('denied, missing, provisioning and failed destinations never substitute a workspace', async () => {
  for (const status of [403, 404, 503, 200]) {
    const { router, tenant, context, auth } = setup()
    const real = globalThis.fetch
    globalThis.fetch = async (path, init) => path.includes(`/workspaces/${W}`)
      ? response({ uuid: W, orgUUID: O }, status) : real(path, init)
    await router.push(resource)
    assert.equal(router.currentRoute.value.fullPath, resource)
    assert.equal(context.state, status === 200 ? 'pending' : status === 503 ? 'error' : 'unavailable')
    assert.equal(auth.clusterName, null)
    assert.notEqual(tenant.workspaceUUID, status === 200 ? B : W)
  }
})

test('new navigation fences a late context response and back restores the original workspace', async () => {
  const { router, tenant } = setup()
  await router.push(resource)
  await router.push(`/${O}/${B}/providers/infrastructure/instances/shared`)
  assert.equal(tenant.workspaceUUID, B)
  const back = new Promise(resolve => { const remove = router.afterEach(() => { remove(); resolve() }) })
  router.back()
  await back
  assert.equal(tenant.workspaceUUID, W)
  assert.equal(router.currentRoute.value.fullPath, resource)
  let release
  const real = globalThis.fetch
  globalThis.fetch = (path, init) => path.endsWith(`/workspaces/${B}`)
    ? new Promise(resolve => { release = () => resolve(response({ uuid: B, orgUUID: O, clusterName: 'late-cluster' })) }) : real(path, init)
  const old = router.push(`/${O}/${B}`)
  while (!release) await new Promise(resolve => setImmediate(resolve))
  await router.push(resource)
  release()
  await old
  assert.equal(tenant.workspaceUUID, W)
  assert.equal(router.currentRoute.value.fullPath, resource)
})

test('org settings clear the operating workspace and list refresh cannot default it again', async () => {
  const { router, tenant, auth } = setup()
  await router.push(resource)
  await router.push(`/${O}/settings/workspaces/${B}`)
  assert.equal(tenant.orgUUID, O)
  assert.equal(tenant.workspaceUUID, null)
  assert.equal(auth.clusterName, null)
  await tenant.fetchWorkspaces(O)
  assert.equal(tenant.workspaceUUID, null)
})

test('returning to the current URL cancels a resolved context awaiting navigation commit', async () => {
  const { router, tenant, context } = setup()
  await router.push(resource)
  let release
  router.beforeResolve(to => to.params.workspaceID === B
    ? new Promise(resolve => { release = resolve }) : undefined)
  const pending = router.push(`/${O}/${B}`)
  while (!release) await new Promise(resolve => setImmediate(resolve))
  assert.equal(context.state, 'ready')
  assert.equal(tenant.workspaceUUID, B)
  await router.push(resource)
  release()
  await pending
  while (context.state === 'loading') await new Promise(resolve => setImmediate(resolve))
  assert.equal(router.currentRoute.value.fullPath, resource)
  assert.equal(tenant.workspaceUUID, W)
  assert.equal(context.blocksRoute(router.currentRoute.value.path, false), false)
})


test('a resolved incoming context cannot remount the outgoing login before route commit', async () => {
  const { context } = setup()
  await context.resolve({ orgUUID: O, workspaceUUID: W })
  assert.equal(context.state, 'ready')
  assert.equal(context.blocksRoute('/login', true), true)
  assert.equal(context.blocksRoute(`/${O}/${B}/mcp`, false), true)
  assert.equal(context.blocksRoute(resource.split('?')[0], false), false)
  context.invalidate()
  assert.equal(context.blocksRoute('/login', true), false)
})


test('deep-link detail metadata does not pretend the org and workspace lists were loaded', async () => {
  const { router, tenant } = setup()
  await router.push(resource)
  assert.equal(tenant.activeWorkspace.uuid, W)
  assert.equal(tenant.orgListLoaded, false)
  assert.equal(tenant.workspaceListLoadedByOrg[O], undefined)
  await tenant.fetchOrgs()
  await tenant.fetchWorkspaces(O, { selectDefault: false })
  assert.equal(tenant.orgListLoaded, true)
  assert.equal(tenant.workspaceListLoadedByOrg[O], true)
  assert.equal(tenant.workspaceUUID, W)
})


test('account changes discard cached metadata and fence old list responses', async () => {
  const { router, tenant, auth, context } = setup()
  await router.push(resource)
  const real = globalThis.fetch
  globalThis.fetch = async (path, init) => path === `/api/orgs/${O}/workspaces`
    ? response({ items: [{ uuid: W, orgUUID: O, clusterName: 'w' }, { uuid: B, orgUUID: O, displayName: 'Private', role: 'admin' }] })
    : real(path, init)
  await tenant.fetchOrgs()
  await tenant.fetchWorkspaces(O)
  // Ordinary bearer refresh must not discard the current identity's cache.
  auth.token = 'refreshed-token'
  assert.equal(tenant.workspaceListLoadedByOrg[O], true)
  const releases = []
  globalThis.fetch = (path, init) => path === '/api/orgs' || path === `/api/orgs/${O}/workspaces`
    ? new Promise(resolve => releases.push(() => resolve(response({ items: [{ uuid: B, orgUUID: O, displayName: 'Old account' }] }))))
    : real(path, init)
  const oldLists = Promise.all([tenant.fetchOrgs(), tenant.fetchWorkspaces(O)])
  while (releases.length !== 2) await new Promise(resolve => setImmediate(resolve))
  await router.push('/login')
  auth.logout()
  assert.deepEqual(tenant.orgs, [])
  assert.deepEqual(tenant.workspacesByOrg, {})
  assert.equal(context.state, 'idle')
  auth.loginFromOIDCResponse({ idToken: 'other-token', expiresAt: 9999999999, email: 'other@example.test', userId: 'other' })
  globalThis.fetch = real
  await router.push(resource)
  assert.equal(tenant.orgListLoaded, false)
  assert.equal(tenant.workspaceListLoadedByOrg[O], undefined)
  // Finishing old reads must neither restore private rows nor finish a new
  // account's in-flight loading indicator for the same organization.
  let finishNew
  globalThis.fetch = (path, init) => path === `/api/orgs/${O}/workspaces`
    ? new Promise(resolve => { finishNew = () => resolve(response({ items: [{ uuid: W, orgUUID: O, clusterName: 'w' }] })) })
    : real(path, init)
  const newList = tenant.fetchWorkspaces(O)
  while (!finishNew) await new Promise(resolve => setImmediate(resolve))
  releases.forEach(release => release())
  await oldLists
  assert.equal(tenant.workspacePendingByOrg[O], 1)
  assert.deepEqual(tenant.orgs.map(row => row.uuid), [O])
  assert.deepEqual(tenant.workspacesByOrg[O].map(row => row.uuid), [W])
  finishNew()
  await newList
  assert.equal(tenant.workspacePendingByOrg[O], undefined)
  assert.equal(tenant.workspaceListLoadedByOrg[O], true)
})

test('a failed page import restores the committed URL context and permits retry', async () => {
  const { router, context, tenant, auth } = setup()
  await router.push(resource)
  const dashboard = router.getRoutes().find(record => record.name === 'dashboard')
  const component = dashboard.components.default
  dashboard.components.default = async () => { throw new Error('Failed to fetch dynamically imported module') }
  await assert.rejects(router.push(`/${O}/${B}`), /Failed to fetch/)
  while (context.state === 'loading') await new Promise(resolve => setImmediate(resolve))
  assert.equal(router.currentRoute.value.fullPath, resource)
  assert.equal(context.destination, resource)
  assert.equal(tenant.workspaceUUID, W)
  assert.equal(auth.clusterName, `cluster-${W}`)
  assert.equal(context.blocksRoute(router.currentRoute.value.path, false), false)
  dashboard.components.default = component
  await router.push(`/${O}/${B}`)
  assert.equal(tenant.workspaceUUID, B)
  assert.equal(context.blocksRoute(router.currentRoute.value.path, false), false)
})

test('late import failures cannot undo a newer navigation', async () => {
  const { router, tenant, context } = setup()
  await router.push(resource)
  let rejectImport
  router.getRoutes().find(record => record.name === 'dashboard').components.default = () => new Promise((_resolve, reject) => { rejectImport = reject })
  const failed = assert.rejects(router.push(`/${O}/${B}`), /Import failed/)
  while (!rejectImport) await new Promise(resolve => setImmediate(resolve))
  const latest = `/${O}/${W}/mcp`
  await router.push(latest)
  const generation = context.generation
  rejectImport(new Error('Import failed'))
  await failed
  assert.equal(router.currentRoute.value.fullPath, latest)
  assert.equal(context.destination, latest)
  assert.equal(context.generation, generation)
  assert.equal(tenant.workspaceUUID, W)
})

test('a later guard rejection restores the committed context', async () => {
  const { router, tenant, context } = setup()
  await router.push(resource)
  router.beforeResolve(to => to.params.workspaceID === B ? false : undefined)
  await router.push(`/${O}/${B}`)
  while (context.state === 'loading') await new Promise(resolve => setImmediate(resolve))
  assert.equal(router.currentRoute.value.fullPath, resource)
  assert.equal(tenant.workspaceUUID, W)
  assert.equal(context.blocksRoute(router.currentRoute.value.path, false), false)
})

// Execute the shell's actual watcher so this test also catches regressions in
// its watch-source shape without mounting unrelated terminal/toast components.
function installShellProviderWatcher({ router, context, tenant }, loads) {
  const source = readFileSync(new URL('../App.vue', import.meta.url), 'utf8')
    .split('<script setup lang="ts">')[1].split('</script>')[0]
  const script = ts.createSourceFile('App.ts', source, ts.ScriptTarget.Latest, true)
  const statement = script.statements.find(node =>
    ts.isExpressionStatement(node) && ts.isCallExpression(node.expression) &&
    node.expression.expression.getText(script) === 'watch' &&
    node.expression.arguments[0].getText(script).includes('routeContext.state') &&
    node.expression.arguments[0].getText(script).includes('route.params.orgID'))
  assert.ok(statement, 'shell provider context watcher exists')
  // useRoute exposes reactive getters into the router's current route.
  const route = shallowReactive({
    get params() { return router.currentRoute.value.params },
  })
  return runInNewContext(statement.getText(script), {
    watch, route, routeContext: context, tenant,
    scopeBlocked: computed(() => context.blocksRoute(router.currentRoute.value.path, false)),
    providers: { load: org => { loads.push(org) } },
  })
}

test('shell provider loading ignores same-scope navigation but follows authority changes', async () => {
  const state = setup()
  const { router, context } = state
  const loads = []
  const stop = installShellProviderWatcher(state, loads)
  try {
    await router.push(resource)
    await nextTick()
    assert.ok(loads.length > 0, 'initial scope loads providers')
    loads.length = 0
    const authorityReads = state.calls.length
    for (const destination of [
      `/${O}/${W}/providers/infrastructure/instances/another`,
      `/${O}/${W}/providers/code/repositories`,
      `/${O}/${W}/providers/code/repositories?tab=activity`,
      `/${O}/${W}/providers/code/repositories?tab=activity#latest`,
    ]) {
      await router.push(destination)
      await nextTick()
      assert.deepEqual(loads, [], `no provider reload for ${destination}`)
    }
    for (const direction of [-1, 1]) {
      await new Promise(resolve => {
        const remove = router.afterEach(() => { remove(); resolve() })
        router.go(direction)
      })
      await nextTick()
      assert.deepEqual(loads, [], 'history navigation preserves provider state')
    }
    assert.equal(state.calls.length, authorityReads, 'same scope reuses verified authority')
    await router.push(`/${O}/${B}/providers/code/repositories`)
    await nextTick()
    assert.ok(loads.length > 0, 'workspace switch loads providers')
    loads.length = 0
    context.invalidate()
    await context.resolve({ orgUUID: O, workspaceUUID: B })
    await nextTick()
    assert.ok(loads.length > 0, 'revalidated authority loads providers')
  } finally {
    stop()
  }
})
