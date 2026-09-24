import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed, createSSRApp, nextTick, shallowReactive, watch } from 'vue'
import { renderToString } from '@vue/server-renderer'
import vue from '@vitejs/plugin-vue'
import { createServer } from 'vite'
import { createPinia, getActivePinia, setActivePinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import { join } from 'node:path'
import { tmpdir } from 'node:os'

const vite = await createServer({
  appType: 'custom', configFile: false, root: new URL('../../', import.meta.url).pathname,
  plugins: [vue()],
  cacheDir: join(tmpdir(), 'railgrid-scoped-navigation-test'),
  resolve: { alias: { '@': new URL('../', import.meta.url).pathname } },
  optimizeDeps: { noDiscovery: true }, server: { middlewareMode: true, hmr: false, ws: false },
})
const { routes } = await vite.ssrLoadModule('/src/router/routes.ts')
const { registerProviderRoutes } = await vite.ssrLoadModule('/src/router/providers.ts')
const { installContextGuard } = await vite.ssrLoadModule('/src/router/contextGuard.ts')
const { readLandingScope, readOrganizationWorkspace, rememberLandingScope } = await vite.ssrLoadModule('/src/router/landingPreference.ts')
const { useTenantStore } = await vite.ssrLoadModule('/src/stores/tenant.ts')
const { useRouteContextStore } = await vite.ssrLoadModule('/src/stores/routeContext.ts')
const { useAuthStore } = await vite.ssrLoadModule('/src/stores/auth.ts')
const { useProvidersStore } = await vite.ssrLoadModule('/src/stores/providers.ts')
const { default: RouteContextState } = await vite.ssrLoadModule('/src/components/RouteContextState.vue')
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
  localStorage.setItem('railgrid:portal:tenant', JSON.stringify({ orgUUID: B, workspaceUUID: B }))
  if (authenticated) localStorage.setItem('railgrid-auth', JSON.stringify({ idToken: 'test-token', expiresAt: 9999999999, email: 'teammate@example.test', userId: 'teammate' }))
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
  assert.equal(calls[1].headers.get('X-Railgrid-Workspace'), W)
  assert.equal(calls[1].headers.get('X-Railgrid-Org'), O)
  assert.equal(calls[1].headers.get('Authorization'), 'Bearer test-token')
  assert.equal(portalRoutePath(router.currentRoute.value.path), '/providers/infrastructure/instances/shared')
  assert.equal(scopedPath('/settings/workspaces', tenant), `/${O}/${W}/settings/workspaces`)
})

test('unscoped entry asks multi-org users when the remembered organization is missing or deleting', async () => {
  for (const remembered of [null, '44444444-4444-4444-8444-444444444444', B]) {
    const { router, tenant, calls, auth } = setup()
    tenant.orgUUID = remembered
    const real = globalThis.fetch
    globalThis.fetch = (path, init) => path === '/api/orgs'
      ? Promise.resolve(response({ items: [{ uuid: O, personal: true }, { uuid: B, deletionRequestedAt: '2026-09-21' }, { uuid: W }] }))
      : real(path, init)
    await router.push('/')
    assert.equal(router.currentRoute.value.name, 'organizations')
    assert.equal(auth.clusterName, null)
    assert.equal(calls.some(call => call.path.includes('/workspaces')), false)
  }
})

test('unscoped entry resumes the remembered organization and workspace before personal defaults', async () => {
  for (const workspace of [W, null]) {
    const { router, tenant } = setup()
    tenant.orgUUID = O
    tenant.workspaceUUID = workspace
    const real = globalThis.fetch
    globalThis.fetch = (path, init) => path === '/api/orgs'
      ? Promise.resolve(response({ items: [{ uuid: B, personal: true }, { uuid: O }] }))
      : real(path, init)
    await router.push('/')
    assert.equal(router.currentRoute.value.path, `/${O}/${W}`)
  }
})

test('last visited scope survives sign-out for the same account without leaking to a different account', async () => {
  const { router, auth, tenant } = setup()
  const real = globalThis.fetch
  globalThis.fetch = (path, init) => path === '/api/orgs'
    ? Promise.resolve(response({ items: [{ uuid: B, personal: true }, { uuid: O }] }))
    : real(path, init)
  await router.push(resource)
  auth.logout()
  await nextTick()
  assert.equal(tenant.orgUUID, null)
  await router.push('/login')
  const login = (userId) => auth.loginFromOIDCResponse({
    idToken: 'test-token', expiresAt: 9999999999, email: `${userId}@example.test`, userId, clusterName: '',
  })
  login('different-account')
  await router.replace('/')
  assert.equal(router.currentRoute.value.name, 'organizations')
  auth.logout()
  await nextTick()
  await router.push('/login')
  login('teammate')
  await router.replace('/')
  assert.equal(router.currentRoute.value.path, `/${O}/${W}`)
})

test('an unavailable remembered workspace falls back to the sole available workspace', async () => {
  const { router, tenant } = setup()
  tenant.orgUUID = O
  tenant.workspaceUUID = B
  await router.push('/')
  assert.equal(router.currentRoute.value.path, `/${O}/${W}`)
  assert.equal(tenant.workspaceUUID, W)
})

test('failed destinations do not replace the last successfully visited scope', async () => {
  const { router, auth } = setup()
  await router.push(resource)
  const real = globalThis.fetch
  globalThis.fetch = (path, init) => path === `/api/orgs/${O}/workspaces/${B}`
    ? Promise.resolve(response({}, 403)) : real(path, init)
  await router.push(`/${O}/${B}`)
  assert.deepEqual(readLandingScope(auth.user), { orgUUID: O, workspaceUUID: W })
})

test('single-org entry remains direct and excludes deleting organizations from the choice', async () => {
  const { router } = setup()
  const real = globalThis.fetch
  globalThis.fetch = (path, init) => path === '/api/orgs'
    ? Promise.resolve(response({ items: [{ uuid: O }, { uuid: B, deletionRequestedAt: '2026-09-21' }] }))
    : real(path, init)
  await router.push('/')
  assert.equal(router.currentRoute.value.path, `/${O}/${W}`)
})

test('empty or failed organization reads reach the chooser without selecting stale context', async () => {
  for (const status of [200, 503]) {
    const { router } = setup()
    globalThis.fetch = async () => response({ items: [] }, status)
    await router.push('/')
    assert.equal(router.currentRoute.value.name, 'organizations')
  }
})

test('multi-org sign-in resumes an explicit workspace destination without the chooser', async () => {
  const { router, auth, calls } = setup({ authenticated: false })
  await router.push(resource)
  const destination = consumePortalNext()
  auth.token = 'test-token'
  const real = globalThis.fetch
  globalThis.fetch = (path, init) => path === '/api/orgs'
    ? Promise.resolve(response({ items: [{ uuid: O }, { uuid: B }] }))
    : real(path, init)
  await router.replace(destination)
  assert.equal(router.currentRoute.value.fullPath, resource)
  assert.equal(calls.some(call => call.path === '/api/orgs'), false)
})

test('login stays behind the loading gate until the organization chooser commits', async () => {
  const { router, auth, context, tenant } = setup({ authenticated: false })
  tenant.orgUUID = null
  await router.push('/login')
  auth.token = 'test-token'
  globalThis.fetch = async () => response({ items: [{ uuid: O }, { uuid: B }] })
  const remountedLogin = []
  const stop = watch(() => context.state, (state) => {
    if (state === 'idle' && router.currentRoute.value.name === 'login') remountedLogin.push(state)
  })
  await router.replace('/')
  await nextTick()
  stop()
  assert.deepEqual(remountedLogin, [])
  assert.equal(router.currentRoute.value.name, 'organizations')
  assert.equal(context.state, 'idle')
})

test('choosing a remembered org after sign-in exits the chooser; explicit returns still work', async () => {
  const source = readFileSync(new URL('../pages/OrganizationsPage.vue', import.meta.url), 'utf8')
  const start = source.indexOf('async function chooseOrganization(')
  const end = source.indexOf('\nasync function retryFailedSwitch', start)
  const code = ts.transpileModule(source.slice(start, end), {
    compilerOptions: { target: ts.ScriptTarget.ES2020 },
  }).outputText
  for (const back of ['/', resource]) {
    const { router, tenant } = setup()
    tenant.orgUUID = O
    await router.push('/organizations')
    const choose = runInNewContext(`${code}\nchooseOrganization`, {
      router, tenant, backPath: { value: back }, switchingOrg: { value: null },
      failedSwitchOrg: { value: null }, localError: { value: null },
    })
    await choose({ uuid: O })
    assert.equal(router.currentRoute.value.fullPath, back === '/' ? `/${O}/workspaces` : resource)
  }
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
  localStorage.setItem('railgrid:portal:tenant', JSON.stringify({ orgUUID: B, workspaceUUID: B }))
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
    let reads = 0
    globalThis.fetch = async (path, init) => {
      if (path.includes(`/workspaces/${W}`)) {
        reads++
        return response({ uuid: W, orgUUID: O }, status)
      }
      return real(path, init)
    }
    await router.push(resource)
    assert.equal(router.currentRoute.value.fullPath, resource)
    assert.equal(context.state, status === 200 ? 'pending' : status === 503 ? 'error' : 'unavailable')
    assert.equal(auth.clusterName, null)
    assert.notEqual(tenant.workspaceUUID, status === 200 ? B : W)
    assert.equal(reads, status === 503 ? 3 : 1, 'only transient failures receive bounded retries')
  }
})

test('destination gate renders recovery only for settled failures, including the route-commit gap', async () => {
  const { context, router } = setup()
  const app = createSSRApp(RouteContextState).use(getActivePinia()).use(router)
  for (const state of ['idle', 'loading', 'ready', 'unavailable', 'pending', 'error']) {
    context.state = state
    context.message = 'Previous failure'
    const html = await renderToString(app)
    if (['idle', 'loading', 'ready'].includes(state)) {
      assert.match(html, /Opening destination/)
      assert.doesNotMatch(html, /Destination unavailable|Previous failure|Switch account|Retry/)
      assert.match(html, /aria-busy="true"/)
    } else {
      assert.match(html, /Retry/)
      assert.match(html, new RegExp(state === 'pending' ? 'Workspace is provisioning' : state === 'error' ? 'Unable to verify destination' : 'Destination unavailable'))
    }
  }
  context.invalidate()
  assert.equal(context.message, '')
})

test('cold navigation waits for session bootstrap and delayed access reads without publishing failure', async () => {
  const { router, auth, context, calls } = setup()
  auth.initialized = false
  const states = []
  const stop = watch(() => context.state, value => states.push(value), { flush: 'sync' })
  const real = globalThis.fetch
  let releaseSession, releaseWorkspace
  globalThis.fetch = (path, init) => {
    if (path === '/healthz') return Promise.resolve(response({}))
    if (path === '/auth/session/bootstrap') return new Promise(resolve => { releaseSession = () => resolve(response({})) })
    if (path === `/api/orgs/${O}/workspaces/${W}`) return new Promise(resolve => { releaseWorkspace = () => resolve(response({ uuid: W, orgUUID: O, clusterName: 'ready' })) })
    return real(path, init)
  }
  const navigation = router.push(resource)
  while (!releaseSession) await new Promise(resolve => setImmediate(resolve))
  assert.equal(context.state, 'loading')
  assert.equal(calls.length, 0, 'access checks must wait for browser session initialization')
  releaseSession()
  while (!releaseWorkspace) await new Promise(resolve => setImmediate(resolve))
  assert.equal(context.state, 'loading')
  releaseWorkspace()
  await navigation
  stop()
  assert.deepEqual(states, ['loading', 'ready'])
  assert.equal(auth.clusterName, 'ready')
})

test('temporary destination failures recover while loading, and retries stop after navigation changes', async () => {
  for (const failure of ['network', 503]) {
    const { router, context } = setup()
    const real = globalThis.fetch
    let attempts = 0
    const states = []
    const stop = watch(() => context.state, value => states.push(value), { flush: 'sync' })
    globalThis.fetch = async (path, init) => {
      if (path.endsWith(`/workspaces/${W}`) && ++attempts === 1) {
        if (failure === 'network') throw new TypeError('Network unavailable')
        return response({}, failure)
      }
      return real(path, init)
    }
    await router.push(resource)
    stop()
    assert.equal(attempts, 2)
    assert.deepEqual(states, ['loading', 'ready'])
    assert.equal(context.state, 'ready')
  }
  const { router, tenant } = setup()
  const real = globalThis.fetch
  let attempts = 0
  globalThis.fetch = async (path, init) => {
    if (path.endsWith(`/workspaces/${W}`)) { attempts++; return response({}, 503) }
    return real(path, init)
  }
  const oldNavigation = router.push(resource)
  while (!attempts) await new Promise(resolve => setImmediate(resolve))
  await router.push(`/${O}/${B}`)
  await oldNavigation
  assert.equal(attempts, 1, 'superseded destinations must not retry')
  assert.equal(tenant.workspaceUUID, B)
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

test('workspace and organization settings retain the operating workspace without a context reload', async () => {
  const { router, tenant, auth, calls, context } = setup()
  await router.push(resource)
  const states = []
  const stop = watch(() => [tenant.workspaceUUID, auth.clusterName, context.state], value => states.push(value), { flush: 'sync' })
  const initialCalls = calls.length
  for (const path of ['/settings/workspaces', '/settings/organizations', '/settings/workspaces']) {
    await router.push(scopedPath(path, tenant))
    assert.equal(tenant.workspaceUUID, W)
    assert.equal(auth.clusterName, `cluster-${W}`)
  }
  stop()
  assert.deepEqual(states, [], 'settings tabs must not clear workspace authority or flash a loading state')
  assert.equal(calls.length, initialCalls)
  assert.equal(portalHref('/providers/code', tenant), `/ui/${O}/${W}/providers/code`)
})

test('legacy workspace details select that operating workspace and preserve URL state', async () => {
  const { router, tenant, auth } = setup()
  await router.push(resource)
  await router.push(`/${O}/settings/workspaces/${B}?tab=access#members`)
  assert.equal(router.currentRoute.value.fullPath, `/${O}/${B}/settings/workspaces?tab=access#members`)
  assert.equal(tenant.orgUUID, O)
  assert.equal(tenant.workspaceUUID, B)
  assert.equal(auth.clusterName, `cluster-${B}`)
})

test('legacy settings entry retains current workspace; cold entry opens organization settings', async () => {
  let fixture = setup()
  await fixture.router.push(resource)
  await fixture.router.push(`/${O}/settings/workspaces`)
  assert.equal(fixture.router.currentRoute.value.path, `/${O}/${W}/settings/workspaces`)
  assert.equal(fixture.tenant.workspaceUUID, W)
  fixture = setup()
  await fixture.router.push(`/${O}/settings/workspaces`)
  assert.equal(fixture.router.currentRoute.value.path, `/${O}/settings/organizations`)
  assert.equal(fixture.tenant.workspaceUUID, null)
})

test('settings deep links verify their exact workspace and never substitute a denied destination', async () => {
  const { router, tenant, context, auth } = setup()
  await router.push(`/${O}/${W}/settings/organizations`)
  assert.equal(tenant.workspaceUUID, W)
  const real = globalThis.fetch
  globalThis.fetch = (path, init) => path === `/api/orgs/${O}/workspaces/${B}`
    ? Promise.resolve(response({}, 403)) : real(path, init)
  await router.push(`/${O}/settings/workspaces/${B}`)
  assert.equal(router.currentRoute.value.path, `/${O}/${B}/settings/workspaces`)
  assert.equal(context.state, 'unavailable')
  assert.equal(auth.clusterName, null)
  assert.notEqual(tenant.workspaceUUID, B)
})

test('organization-only settings work without a workspace or misleading provider links', async () => {
  const { router, tenant, auth } = setup()
  await router.push(`/${O}/settings/organizations`)
  assert.equal(tenant.orgUUID, O)
  assert.equal(tenant.workspaceUUID, null)
  assert.equal(auth.clusterName, null)
  const providers = useProvidersStore()
  providers.items = [{ name: 'edges', displayName: 'Edges', ready: true, hasUI: true, builtin: true }]
  assert.deepEqual(providers.enabledNavItems, [])
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

const { preferredWorkspace } = await vite.ssrLoadModule('/src/router/workspaceEntry.ts')
test('workspace entry resumes valid preferences, never guesses among multiple workspaces, and waits for provisioning', () => {
  const ready = { uuid: W, orgUUID: O, clusterName: 'cluster' }
  const other = { uuid: B, orgUUID: O, clusterName: 'other' }
  assert.equal(preferredWorkspace([ready], null), ready)
  assert.equal(preferredWorkspace([ready, other], null), null)
  assert.equal(preferredWorkspace([ready, other], B), other)
  assert.equal(preferredWorkspace([ready, { ...other, clusterName: undefined }], B), null)
  assert.equal(preferredWorkspace([{ ...ready, deletionRequestedAt: 'today' }], W), null)
  assert.equal(preferredWorkspace([{ ...ready, clusterName: undefined }], W), null)
  assert.equal(preferredWorkspace([], W), null)
})
test('last workspace is remembered per organization and account, including visits to org settings', () => {
  const { auth } = setup()
  rememberLandingScope(auth.user, { orgUUID: O, workspaceUUID: W })
  rememberLandingScope(auth.user, { orgUUID: B, workspaceUUID: B })
  rememberLandingScope(auth.user, { orgUUID: O, workspaceUUID: null })
  assert.equal(readOrganizationWorkspace(auth.user, O), W)
  assert.equal(readOrganizationWorkspace(auth.user, B), B)
  assert.equal(readOrganizationWorkspace({ userId: 'someone-else' }, O), null)
})
