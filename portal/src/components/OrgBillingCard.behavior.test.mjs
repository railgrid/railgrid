import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

// The card asks the hub for a hosted page (Checkout, the Customer Portal) and
// navigates to the URL that comes back. These tests run the production
// functions in a sandbox to check the one thing that matters about that
// wait: an answer that arrives after the card unmounted, the route or the
// organization changed, the session was replaced, or the user left and came
// back to the same URL, is dropped instead of navigated to.
const source = fs.readFileSync(new URL('./OrgBillingCard.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('OrgBillingCard.ts', script, ts.ScriptTarget.Latest, true)

function loadFunction(name, context) {
  const declaration = parsed.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === name)
  assert.ok(declaration, `${name} exists in the production component`)
  const code = ts.transpileModule(declaration.getText(parsed), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
  }).outputText
  return runInNewContext(`${code}\n${name}`, context)
}

function deferred() {
  let resolve
  let reject
  const promise = new Promise((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

function cardFixture() {
  const checkout = deferred()
  const portal = deferred()
  const calls = { assign: [], checkout: [], portal: [] }
  let authRevision = 1
  const context = {
    props: { orgUuid: 'org-a' },
    route: { fullPath: '/org-a/settings/organizations' },
    tenant: {
      orgUUID: 'org-a',
      startOrgCheckout: async (org, plan) => { calls.checkout.push([org, plan]); return checkout.promise },
      openOrgBillingPortal: async (org) => { calls.portal.push([org]); return portal.promise },
    },
    authSessionRevision: () => authRevision,
    bumpAuth: () => { authRevision += 1 },
    disposed: false,
    hostedPageGeneration: 0,
    busy: { value: null },
    error: { value: null },
    window: { location: { assign: (url) => calls.assign.push(url) } },
    // The sandboxed functions check `err as Error`; share this realm's Error.
    Error,
  }
  for (const name of ['hostedPageRequest', 'hostedPageStillWanted', 'upgrade', 'manage']) context[name] = loadFunction(name, context)
  return { context, calls, checkout, portal }
}

test('a checkout answer that arrives with nothing changed is navigated to', async () => {
  const fixture = cardFixture()
  const pending = fixture.context.upgrade('team')
  assert.equal(fixture.context.busy.value, 'team')
  assert.deepEqual(fixture.calls.checkout, [['org-a', 'team']])
  fixture.checkout.resolve('https://checkout.example/cs_1')
  await pending
  assert.deepEqual(fixture.calls.assign, ['https://checkout.example/cs_1'])
  // The page is leaving; the button stays busy until it does.
  assert.equal(fixture.context.busy.value, 'team')
})

for (const [name, change] of [
  // Left and came back to the same URL: everything reads the same, except the
  // generation the scope watcher moved on the way out and on the way back.
  ['the scope generation moved on', (context) => { context.hostedPageGeneration += 1 }],
  ['the card unmounted', (context) => { context.disposed = true; context.hostedPageGeneration += 1 }],
  ['the route changed', (context) => { context.route.fullPath = '/org-a/settings/workspaces' }],
  ['the card was handed another organization', (context) => { context.props.orgUuid = 'org-b' }],
  ['the operating organization changed', (context) => { context.tenant.orgUUID = 'org-b' }],
  ['the session was replaced', (context) => { context.bumpAuth() }],
]) {
  test(`a checkout answer is dropped after ${name}`, async () => {
    const fixture = cardFixture()
    const pending = fixture.context.upgrade('team')
    change(fixture.context)
    fixture.checkout.resolve('https://checkout.example/cs_1')
    await pending
    assert.deepEqual(fixture.calls.assign, [])
    assert.equal(fixture.context.busy.value, null)
    assert.equal(fixture.context.error.value, null)
  })
}

test('a checkout failure is shown only where it was requested', async () => {
  const inPlace = cardFixture()
  const pending = inPlace.context.upgrade('team')
  inPlace.checkout.reject(new Error('the organization\'s plan is assigned by a platform administrator'))
  await pending
  assert.equal(inPlace.context.error.value, 'the organization\'s plan is assigned by a platform administrator')
  assert.equal(inPlace.context.busy.value, null)

  const moved = cardFixture()
  const pendingMoved = moved.context.upgrade('team')
  moved.context.route.fullPath = '/org-a/settings/workspaces'
  moved.checkout.reject(new Error('metering unreachable'))
  await pendingMoved
  assert.equal(moved.context.error.value, null)
  assert.equal(moved.context.busy.value, null)
})

test('the customer portal answer is fenced the same way', async () => {
  const fixture = cardFixture()
  const pending = fixture.context.manage()
  assert.equal(fixture.context.busy.value, 'portal')
  fixture.context.hostedPageGeneration += 1
  fixture.portal.resolve('https://billing.example/p/session')
  await pending
  assert.deepEqual(fixture.calls.assign, [])
  assert.equal(fixture.context.busy.value, null)

  const settled = cardFixture()
  const pendingSettled = settled.context.manage()
  settled.portal.resolve('https://billing.example/p/session')
  await pendingSettled
  assert.deepEqual(settled.calls.assign, ['https://billing.example/p/session'])
})

// The fence itself is wiring the sandbox cannot run: the generation must move
// synchronously on every route, card-organization and operating-organization
// change (a batched watcher could miss a change that is undone before the
// next flush) and on unmount, which also marks the card disposed.
test('the generation moves synchronously on scope changes and on unmount', () => {
  const watcher = script.match(/watch\(\[([\s\S]*?)\],\s*\(\)\s*=>\s*\{\s*hostedPageGeneration\+\+\s*\},\s*\{\s*flush:\s*'sync'\s*\}\)/)
  assert.ok(watcher, 'the scope watcher bumps hostedPageGeneration with flush: sync')
  for (const sourceExpr of ['route.fullPath', 'props.orgUuid', 'tenant.orgUUID']) {
    assert.ok(watcher[1].includes(sourceExpr), `the scope watcher watches ${sourceExpr}`)
  }
  assert.match(script, /onBeforeUnmount\(\(\) => \{\s*disposed = true\s*hostedPageGeneration\+\+\s*\}\)/)
})

const recommendedPlan = loadFunction('recommendedPlan', { Number, Math })
const recommendWithoutOverrides = (...args) => recommendedPlan(...args, [])
const planLimit = (max, extra = {}) => ({ meter: 'workspaces', shape: 'Level', enforce: 'Hard', max, ...extra })
const planOffer = (name, max, extra = {}) => ({ name, current: false, sellable: false, limits: [planLimit(max)], ...extra })

test('recommendations provide actual headroom and choose the smallest sufficient known allowance', () => {
  const small = planOffer('small', '10')
  const large = planOffer('large', '100')
  assert.equal(recommendWithoutOverrides('workspaces', [large, small], [planLimit('1')], { workspaces: '1' }), small)
  // At 10/10, an allowance of 10 does not restore room for another workspace.
  assert.equal(recommendWithoutOverrides('workspaces', [small, large], [planLimit('1')], { workspaces: '10' }), large)
  assert.equal(recommendWithoutOverrides('workspaces', [small], [planLimit('1')], { workspaces: '12' }), null)
})

test('recommendations use effective limits rather than the catalog current-plan allowance', () => {
  const current = planOffer('free', '1', { current: true })
  const small = planOffer('small', '10')
  const large = planOffer('large', '100')
  assert.equal(recommendWithoutOverrides('workspaces', [current, small, large], [planLimit('20')], { workspaces: '20' }), large)
  assert.equal(recommendWithoutOverrides('workspaces', [small], [planLimit('20')], { workspaces: '2' }), null)
})

test('unknown usage never yields an upgrade recommendation', () => {
  for (const usage of [{}, { workspaces: '' }, { workspaces: null }, { workspaces: ' ' }, { workspaces: 'not measured' }]) {
    assert.equal(recommendWithoutOverrides('workspaces', [planOffer('small', '10')], [planLimit('0')], usage), null)
  }
  assert.equal(recommendWithoutOverrides('workspaces', [planOffer('small', '10')], [planLimit('0')], { workspaces: '0' }).name, 'small')
})

test('recommendations exclude unknown allowances, different shapes and current plans', () => {
  const offers = [
    planOffer('not-specified', '10', { limits: [] }),
    planOffer('unknown-max', undefined),
    planOffer('different-shape', '10', { limits: [planLimit('10', { shape: 'Consumption' })] }),
    planOffer('already-current', '100', { current: true }),
  ]
  assert.equal(recommendWithoutOverrides('workspaces', offers, [planLimit('1')], { workspaces: '1' }), null)
})

test('a metered numeric reference is not treated as an enforced limit', () => {
  assert.equal(recommendWithoutOverrides('workspaces', [planOffer('small', '10')], [planLimit('1', { enforce: 'Meter' })], { workspaces: '2' }), null)
})

test('omitted shape is compared as Consumption, while fractional display retains precision', () => {
  const candidate = planOffer('small', '10', { limits: [planLimit('10', { shape: 'Consumption' })] })
  assert.equal(recommendWithoutOverrides('workspaces', [candidate], [planLimit('1', { shape: undefined })], { workspaces: '1' }), candidate)
  const quantity = loadFunction('quantity', { Number, Intl })
  assert.equal(quantity('0.000000000000000001'), '0.000000000000000001')
  assert.equal(quantity('9007199254740993'), '9007199254740993')
})

test('persistent overrides and unknown override metadata cannot yield upgrade promises', () => {
  const plans = [planOffer('large', '100')]
  const effective = [planLimit('20')]
  assert.equal(recommendedPlan('workspaces', plans, effective, { workspaces: '20' }, [planLimit('20')]), null)
  // Even an override equal to the catalog allowance will survive an upgrade.
  assert.equal(recommendedPlan('workspaces', plans, [planLimit('1')], { workspaces: '1' }, [planLimit('1')]), null)
  assert.equal(recommendedPlan('workspaces', plans, effective, { workspaces: '20' }), null)
  assert.equal(recommendedPlan('workspaces', plans, effective, { workspaces: '20' }, []), plans[0])
})

test('proposed allowances preserve overrides, including meters absent from the target plan', () => {
  const proposedLimits = loadFunction('proposedLimits', { Map })
  const plan = planOffer('large', '100')
  const override = planLimit('20')
  const extra = { meter: 'agents', shape: 'Level', enforce: 'Soft', max: '5' }
  const actual = proposedLimits(plan, [override, extra])
  assert.equal(actual.find(limit => limit.meter === 'workspaces'), override)
  assert.equal(actual.find(limit => limit.meter === 'agents'), extra)
  assert.equal(plan.limits[0].max, '100', 'catalog limits are not mutated')
})

test('the current comparison column uses effective allowances and candidates retain persistent overrides', () => {
  const catalog = planOffer('free', '1', { current: true })
  const effective = [planLimit('20')]
  const proposedLimits = loadFunction('proposedLimits', { Map })
  const declaration = parsed.statements.flatMap((node) => ts.isVariableStatement(node) ? [...node.declarationList.declarations] : [])
    .find((node) => node.name.getText(parsed) === 'comparisonPlans')
  assert.ok(declaration?.initializer)
  const code = ts.transpileModule(`const result = ${declaration.initializer.getText(parsed)}`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText
  const actual = runInNewContext(`${code}\nresult`, {
    computed: (fn) => fn(), current: { value: catalog },
    billing: { value: { limits: effective, overrides: effective } },
    offers: { value: [planOffer('large', '100')] }, proposedLimits,
  })
  assert.equal(actual[0].limits, effective)
  assert.equal(actual[1].limits[0].max, '20')
  assert.equal(catalog.limits[0].max, '1')
})

test('admin request drafts distinguish empty, populated, and unknown overrides', () => {
  for (const overrides of [[], [planLimit('1')], undefined]) {
    const context = {
      billing: { value: { limits: [planLimit('1')], usage: { workspaces: '1' }, overrides } },
      reviewPlan: { value: planOffer('small', '10') }, reviewLimits: { value: [planLimit('10')] },
      reviewMeters: { value: ['workspaces'] }, focusedMeter: { value: null },
      planLabel: { value: 'Free' }, props: { orgUuid: 'org-a' }, requestDraft: { value: '' },
      overridesKnown: { value: overrides !== undefined }, limitsPending: { value: false },
      quantity: (value) => value, meterLabel: (meter) => meter,
      allowance: (limits, meter) => limits.find(limit => limit.meter === meter)?.max,
    }
    loadFunction('prepareRequest', context)()
    assert.equal(context.requestDraft.value.includes('Existing organization overrides remain in effect.'), !!overrides?.length)
    assert.equal(context.requestDraft.value.includes('This hub does not report organization overrides.'), overrides === undefined)
  }
})

test('pending allowance changes suppress upgrade hints until the applied snapshot catches up', () => {
  const proposedLimits = loadFunction('proposedLimits', { Map })
  const allowancesPending = loadFunction('allowancesPending', { proposedLimits, JSON })
  const value = { plans: [planOffer('free', '1', { current: true }), planOffer('small', '10')],
    limits: [planLimit('0')], overrides: [], usage: { workspaces: '1' } }
  // An override was removed, but effective limits still reflect that override.
  assert.equal(allowancesPending(value), true)
  const suggestion = loadFunction('suggestion', { billing: { value }, allowancesPending, recommendedPlan })
  assert.equal(suggestion('workspaces'), null)
  value.limits = [planLimit('1')]
  assert.equal(allowancesPending(value), false)
  assert.equal(suggestion('workspaces').name, 'small')
  // An override was just added; the catalog-sized snapshot is now stale.
  value.overrides = [planLimit('20')]
  assert.equal(allowancesPending(value), true)
  value.limits = [planLimit('20')]
  assert.equal(allowancesPending(value), false)
})
