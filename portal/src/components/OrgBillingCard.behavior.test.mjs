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

const billingFlow = ts.createSourceFile('billing-flow.ts', fs.readFileSync(new URL('../lib/billing-flow.ts', import.meta.url), 'utf8'), ts.ScriptTarget.Latest, true)

function loadFunction(name, context, functionSource = ['proposedLimits', 'allowancesPending'].includes(name) ? billingFlow : parsed) {
  const declaration = functionSource.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === name)
  assert.ok(declaration, `${name} exists in the production component`)
  const code = ts.transpileModule(declaration.getText(functionSource).replace(/^export /, ''), {
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

test('a stored checkout intent only starts activation after a successful Stripe return', () => {
  for (const outcome of [undefined, 'portal', 'canceled', 'success']) {
    const cleared = []
    const context = {
      props: { orgUuid: 'org-a' }, auth: { user: { userId: 'alice' } },
      route: { query: { panel: 'billing', review: '1', billing: outcome, billingPlan: 'team' } },
      router: { replace: () => {} },
      savedDraft: { value: null }, workspaceBillingDraft: () => null,
      pendingCheckout: () => ({ plan: 'team' }),
      checkoutPlan: { value: '' }, activation: { value: 'idle' },
      reviewOpen: { value: false }, focusedMeter: { value: null }, returnNotice: { value: '' },
      clearBillingState: (...args) => cleared.push(args),
    }
    loadFunction('handleReturn', context)()
    assert.equal(context.activation.value, outcome === 'success' ? 'checking' : 'idle')
    assert.equal(context.checkoutPlan.value, outcome === 'success' ? 'team' : '')
    assert.equal(context.reviewOpen.value, true)
    assert.deepEqual(cleared, outcome === 'success' ? [] : [['checkout', 'org-a', 'alice']])
  }
})

function cardFixture() {
  const checkout = deferred()
  const portal = deferred()
  const calls = { assign: [], checkout: [], portal: [] }
  let authRevision = 1
  const context = {
    props: { orgUuid: 'org-a', canManage: true },
    auth: { user: { userId: 'alice' } },
    billing: { value: { plans: [{ name: 'team', sellable: true, price: { id: 'price_team' } }] } },
    priceAvailable: price => !!price?.id,
    awaitingActivation: { value: false }, managedByOperator: { value: false }, hasSubscription: { value: false },
    failedAction: { value: null }, checkoutPlan: { value: '' }, savePendingCheckout: () => true,
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
  for (const name of ['hostedPageRequest', 'hostedPageStillWanted', 'upgrade', 'manage', 'retryHostedAction']) context[name] = loadFunction(name, context)
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
  assert.match(script, /onBeforeUnmount\(\(\) => \{\s*disposed = true\s*hostedPageGeneration\+\+\s*clearTimeout\(pollTimer\)\s*loadAbort\?\.abort\(\)\s*\}\)/)
})

test('Retry repeats the failed hosted action without reloading the summary', () => {
  const { context } = cardFixture()
  const attempts = []
  context.upgrade = plan => attempts.push(['checkout', plan])
  context.manage = () => attempts.push(['portal'])
  context.failedAction.value = { kind: 'checkout', plan: 'team' }
  context.retryHostedAction()
  context.failedAction.value = { kind: 'portal' }
  context.retryHostedAction()
  assert.deepEqual(attempts, [['checkout', 'team'], ['portal']])
})

test('missing prices and duplicate submits cannot open checkout', async () => {
  const missing = cardFixture()
  delete missing.context.billing.value.plans[0].price
  await missing.context.upgrade('team')
  assert.equal(missing.calls.checkout.length, 0)
  const duplicate = cardFixture()
  const first = duplicate.context.upgrade('team')
  await duplicate.context.upgrade('team')
  assert.equal(duplicate.calls.checkout.length, 1)
  duplicate.checkout.resolve('https://checkout.example/one')
  await first
})

test('activation polling stops at its deadline and fences disposed contexts', async () => {
  for (const leave of [false, true]) {
    let now = 0
    let queued
    let reads = 0
    const context = {
      pollTimer: undefined, pollStarted: 0, disposed: false, props: { orgUuid: 'org-a' },
      activation: { value: 'idle' }, checkoutPlan: { value: 'team' },
      Date: { now: () => now }, Math, authSessionRevision: () => 1,
      clearTimeout: () => { queued = undefined },
      setTimeout: (callback, delay) => { assert.ok(delay <= 3000); queued = callback; return 1 },
      load: async () => { reads++ },
    }
    loadFunction('startActivationCheck', context)()
    for (let turn = 0; !queued && turn < 10; turn++) await Promise.resolve()
    assert.equal(reads, 1)
    assert.equal(typeof queued, 'function')
    if (leave) context.disposed = true
    else now = 60000
    queued()
    await Promise.resolve()
    assert.equal(reads, 1, 'no fetch starts past the deadline or after disposal')
    if (!leave) assert.equal(context.activation.value, 'delayed')
  }
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

test('an archived current plan remains readable but is not offered as a plan change', () => {
  const currentPlan = { name: 'legacy', displayName: 'Legacy', current: true, sellable: false, limits: [planLimit('10')] }
  const availablePlan = { name: 'team', displayName: 'Team', current: false, sellable: true, limits: [planLimit('100')] }
  const billing = { value: { plans: [currentPlan, availablePlan] } }
  const suggested = { value: null }
  const evaluate = name => {
    const declaration = parsed.statements.flatMap(node => ts.isVariableStatement(node) ? [...node.declarationList.declarations] : [])
      .find(node => node.name.getText(parsed) === name)
    assert.ok(declaration?.initializer, `${name} computed exists`)
    const code = ts.transpileModule(`const result = ${declaration.initializer.getText(parsed)}`, {
      compilerOptions: { target: ts.ScriptTarget.ES2022 },
    }).outputText
    return runInNewContext(`${code}\nresult`, { computed: fn => ({ get value() { return fn() } }), billing, suggested })
  }

  assert.equal(evaluate('current').value.name, 'legacy', 'the existing current plan remains in the billing view')
  assert.deepEqual(evaluate('offers').value.map(plan => plan.name), ['team'], 'only noncurrent plans are eligible choices')
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

test('the comparison checkout respects quoted prices, permissions, subscriptions, activation and focused capacity', () => {
  const plan = planOffer('team', '25', { sellable: true, price: { id: 'price_live', amount: '73', currency: 'EUR' } })
  const context = {
    billing: { value: { enabled: true } }, props: { canManage: true },
    managedByOperator: { value: false }, hasSubscription: { value: false },
    awaitingActivation: { value: false }, focusedMeter: { value: null },
    priceAvailable: price => !!price?.id, suggestionForPlan: () => false,
  }
  const canCheckout = loadFunction('canCompareCheckout', context)
  assert.equal(canCheckout(plan), true)
  for (const flag of ['managedByOperator', 'hasSubscription', 'awaitingActivation']) {
    context[flag].value = true
    assert.equal(canCheckout(plan), false, flag)
    context[flag].value = false
  }
  assert.equal(canCheckout({ ...plan, price: undefined }), false)
  assert.equal(canCheckout({ ...plan, current: true }), false)
  assert.equal(canCheckout({ ...plan, sellable: false }), false)
  context.props.canManage = false
  assert.equal(canCheckout(plan), false)
  context.props.canManage = true
  context.focusedMeter.value = 'workspaces'
  assert.equal(canCheckout(plan), false, 'the proposed allowance must resolve the blocked action')
  context.suggestionForPlan = () => true
  assert.equal(canCheckout(plan), true)
  context.billing.value.enabled = false
  assert.equal(canCheckout(plan), false)
})


test('first-workspace creation clears only the successful creator’s saved billing draft', async () => {
  const wizard = fs.readFileSync(new URL('./FirstWorkspaceWizard.vue', import.meta.url), 'utf8')
  const wizardScript = wizard.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
  const wizardParsed = ts.createSourceFile('FirstWorkspaceWizard.ts', wizardScript, ts.ScriptTarget.Latest, true)
  for (const success of [true, false]) {
    const creation = deferred()
    const cleared = []
    const context = {
      canSubmit: { value: true }, busy: { value: false }, error: { value: null },
      trimmed: { value: 'Production' }, auth: { user: { userId: 'alice' } },
      tenant: { orgUUID: 'org-a', error: 'Workspace limit reached', createWorkspace: () => creation.promise },
      clearBillingState: (...args) => cleared.push(args), router: { push: async () => {} }, Error,
    }
    const pending = loadFunction('handleCreate', context, wizardParsed)()
    context.auth.user.userId = 'bob'
    creation.resolve(success ? { orgUUID: 'org-a', uuid: 'ws-new' } : null)
    await pending
    assert.deepEqual(cleared, success ? [['draft', 'org-a', 'alice']] : [])
  }
})

test('paid subscribers downgrade through the existing billing portal, without checkout or request drafts', () => {
  const source = fs.readFileSync(new URL('./BillingPlanComparison.vue', import.meta.url), 'utf8')
  const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
  const comparison = ts.createSourceFile('BillingPlanComparison.ts', script, ts.ScriptTarget.Latest, true)
  const events = []
  const context = {
    props: { canManage: true, billing: { enabled: true, defaultPlan: 'free' }, busy: null, awaitingActivation: false, canCheckout: () => true },
    hasSubscription: { value: true }, managed: { value: false },
    emit: (...args) => events.push(args), priceAvailable: () => false,
  }
  for (const name of ['checkoutPlan', 'action', 'choose', 'disabled']) context[name] = loadFunction(name, context, comparison)
  const free = { name: 'free', current: false, sellable: false }
  assert.equal(context.action(free), 'Downgrade to Free')
  assert.equal(context.disabled(free), false)
  context.choose(free)
  assert.deepEqual(events, [['manage']])
  // A manual plan assignment cannot prevent cancellation of an existing bill.
  context.managed.value = true
  assert.equal(context.action(free), 'Manage subscription')
  assert.equal(context.disabled(free), false)
  context.choose(free)
  assert.deepEqual(events, [['manage'], ['manage']])
  context.props.canManage = false
  assert.equal(context.disabled(free), true)
  context.choose(free)
  assert.equal(events.length, 2, 'members cannot invoke subscription changes')
  context.props.canManage = true
  context.hasSubscription.value = false
  assert.equal(context.disabled(free), true)
  context.choose(free)
  assert.equal(events.length, 2, 'an assigned plan is not a self-service subscription')
})
