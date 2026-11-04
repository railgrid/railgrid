// Valid scope IDs follow the real router's UUID contract. Display names match
// the existing onboarding visual fixture; plan caps match billing_test.go.
export const ORG = '11111111-1111-4111-8111-111111111111'
export const WS = '22222222-2222-4222-8222-222222222222'
export const routePath = `/ui/${ORG}/${WS}/settings/organizations`
export const org = { uuid: ORG, displayName: 'Demo', role: 'admin', personal: false, workspaceCreation: 'admins', createdAt: '2026-10-01T12:00:00Z' }
export const workspace = { uuid: WS, orgUUID: ORG, displayName: 'My Workspace', clusterName: 'abc123def456', role: 'admin', createdAt: '2026-10-01T12:00:00Z' }
const limits = (workspaces, capacity, rpm) => [
  { meter: 'workspaces', shape: 'Level', enforce: 'Hard', max: String(workspaces) },
  ...['agents', 'edges', 'projects', 'instances'].map(meter => ({ meter, shape: 'Level', enforce: 'Hard', max: String(capacity) })),
  ...['repositories', 'services'].map(meter => ({ meter, shape: 'Level', enforce: 'Hard', max: String(capacity * 2) })),
  { meter: 'request_cost_rpm', shape: 'Level', enforce: 'Hard', max: String(rpm) },
]
export const billing = {
  installed: true, enabled: true, livemode: false, defaultPlan: 'free',
  plan: 'free', planDisplayName: 'Free', managedBy: '', overrides: [],
  limits: limits(3, 5, 600),
  usage: { workspaces: '3', agents: '4', edges: '2', projects: '1', instances: '1', repositories: '3', services: '2', request_cost_rpm: '120' }, overHardLimit: false, overSoftLimit: false,
  plans: [
    { name: 'free', displayName: 'Free', current: true, sellable: false, limits: limits(3, 5, 600) },
    { name: 'team', displayName: 'Team', current: false, sellable: true, price: { id: 'price_fixture_team', amount: '50', currency: 'USD', interval: 'month', intervalCount: 1, taxBehavior: 'exclusive' }, limits: limits(25, 25, 6000) },
    { name: 'enterprise', displayName: 'Enterprise', current: false, sellable: false, limits: limits(100, 100, 60000).map(limit => ['workspaces', 'request_cost_rpm'].includes(limit.meter) ? limit : { meter: limit.meter, shape: 'Level', enforce: 'Meter' }) },
  ],
}
const providers = [
  { name: 'app-studio', displayName: 'App Studio', category: 'Development' },
  { name: 'code', displayName: 'Code', category: 'Development' },
  { name: 'edges', displayName: 'Edges', category: 'Infrastructure' },
  { name: 'agents', displayName: 'Agents', category: 'AI' },
].map(item => ({ ...item, description: `${item.displayName} fixture`, scope: 'global', ready: true, export: { name: `${item.name}.providers.railgrid.ai`, path: `root:providers:${item.name}` }, serving: { ui: {} } }))

const checkouts = new Map()
const attempts = new Map()
const created = new Map()
let sequence = 0

function summary(context) {
  const out = structuredClone(billing)
  const scenario = context.referer.searchParams.get('preview')
  const checkout = checkouts.get(context.referer.searchParams.get('fixtureCheckout'))
  if (checkout?.paid) checkout.reads++
  const active = scenario === 'active' || (checkout?.paid && checkout.reads >= 2 && scenario !== 'delayed')
  if (active) {
    out.plan = 'team'; out.planDisplayName = 'Team'; out.managedBy = 'billing'
    out.subscription = { entitledPlan: 'team', status: 'active', cancelAtPeriodEnd: false, syncedAt: new Date().toISOString() }
    out.limits = out.plans[1].limits
    out.plans.forEach(plan => { plan.current = plan.name === 'team' })
  }
  if (scenario === 'operator') out.managedBy = 'operator'
  if (scenario === 'override') out.overrides = [{ ...out.limits[0] }]
  if (scenario === 'different-price') { out.plans[1].price.amount = '73'; out.plans[1].price.currency = 'EUR'; out.plans[1].limits[0].max = '41' }
  if (scenario === 'disabled') { out.enabled = false; out.plans[1].sellable = false; delete out.plans[1].price }
  if (scenario === 'unavailable') delete out.plans[1].price
  return out
}

export function fixtureResponse(path, method = 'GET', context = { referer: new URL('http://fixture/'), cookie: '', body: '' }) {
  const scenario = context.referer.searchParams.get('preview')
  const selectedOrg = { ...org, role: scenario === 'member' ? 'member' : 'admin' }
  if (path === '/healthz') return { body: { status: 'ok', oidc: false, tokenLogin: true } }
  if (path === '/version') return { body: { version: 'design-preview', gitCommit: 'fixture' } }
  if (path === '/auth/session/bootstrap') return { body: { authenticated: true, userId: 'fixture-user', email: 'fixture@example.test', expiresAt: Math.floor(Date.now() / 1000) + 3600 } }
  if (path === '/api/users/me') return { body: { user: 'fixture-user', email: 'fixture@example.test', displayName: 'Dev User', rbacIdentity: 'railgrid:static:fixture-user' } }
  if (path === '/api/admin/access') return { status: 403, body: { message: 'Not a platform administrator' } }
  if (path === '/api/orgs') return { body: { items: [selectedOrg] } }
  if (path === `/api/orgs/${ORG}`) return { body: selectedOrg }
  if (path === `/api/orgs/${ORG}/billing`) return { body: summary(context) }
  if (path === `/api/orgs/${ORG}/billing/checkout`) {
    const input = JSON.parse(context.body || '{}')
    if (scenario === 'failed' && !attempts.has(context.cookie)) {
      attempts.set(context.cookie, 1)
      return { status: 502, body: { message: 'The billing provider could not open checkout. Try again.' } }
    }
    if (input.priceID !== billing.plans[1].price.id || input.plan !== 'team') return { status: 409, body: { message: 'The reviewed price changed. Reload pricing.' } }
    const id = `checkout-${++sequence}`
    checkouts.set(id, { paid: false, reads: 0 })
    return { body: { url: `/_billing-fixture/checkout?id=${id}` } }
  }
  if (path === `/api/orgs/${ORG}/billing/portal`) return { body: { url: '/_billing-fixture/portal' } }
  if (path === `/api/orgs/${ORG}/workspaces`) {
    if (method === 'GET') return { body: { items: [workspace, ...created.values()] } }
    const state = summary(context)
    if (state.plan !== 'team') return { status: 403, body: { message: 'Workspace limit reached: 3 of 3 Workspaces in use.' } }
    const row = { ...workspace, uuid: '33333333-3333-4333-8333-333333333333', clusterName: 'createdcluster', displayName: JSON.parse(context.body).displayName }
    created.set(row.uuid, row)
    return { status: 201, body: row }
  }
  if (path === `/api/orgs/${ORG}/workspaces/${WS}`) return { body: workspace }
  if (path.startsWith(`/api/orgs/${ORG}/workspaces/`) && created.has(path.split('/')[5])) return { body: created.get(path.split('/')[5]) }
  if (path.endsWith('/memberships/me')) return { body: { role: 'admin' } }
  if (path.endsWith('/memberships')) return { body: { items: [{ user: 'fixture-user', email: 'fixture@example.test', displayName: 'Dev User', role: 'admin', createdAt: '2026-10-01T12:00:00Z' }] } }
  if (path === '/api/providers') return { body: { items: providers, categories: [{ name: 'Development', icon: 'Layers', order: 1 }, { name: 'Infrastructure', icon: 'Server', order: 2 }, { name: 'AI', icon: 'Bot', order: 3 }] } }
  if (path.endsWith('/providers/enabled')) return { body: {
    bindingNamesByProvider: Object.fromEntries(providers.map(p => [p.name, `binding-${p.name}`])),
    bindingsByProvider: Object.fromEntries(providers.map(p => [p.name, {
      bindingName: `binding-${p.name}`, exportPath: p.export.path,
      exportName: p.export.name, boundProvider: p, selfHosted: false, ready: true,
    }])),
  } }
  // Unknown writes cannot mutate even this fixture. No upstream hub exists.
  if (method !== 'GET') return { status: 405, body: { message: 'This is a read-only design fixture.' } }
  return { body: { items: [] } }
}

export function hostedFixture(url) {
  if (!url.pathname.startsWith('/_billing-fixture/')) return null
  const id = url.searchParams.get('id') || ''
  const base = `/ui/${ORG}/settings/organizations?panel=billing`
  if (url.pathname.endsWith('/paid') && checkouts.has(id)) {
    checkouts.get(id).paid = true
    return `<!doctype html><script>location.replace(${JSON.stringify(`${base}&billing=success&billingPlan=team&fixtureCheckout=${encodeURIComponent(id)}`)})</script>`
  }
  if (url.pathname.endsWith('/portal')) return `<!doctype html><title>Billing portal fixture</title><h1>Hosted billing portal fixture</h1><a href="${base}&billing=portal">Return to Railgrid</a>`
  return `<!doctype html><html><head><title>Checkout fixture</title></head><body style="font:16px system-ui;padding:40px"><h1>Stripe checkout fixture</h1><p>Test-only handoff. No payment is collected.</p><p>Team · $50 USD/month · illustrative fixture price.</p><a href="/_billing-fixture/paid?id=${encodeURIComponent(id)}">Complete fixture checkout</a><p><a href="${base}&billing=canceled&billingPlan=team">Cancel fixture checkout</a></p></body></html>`
}
