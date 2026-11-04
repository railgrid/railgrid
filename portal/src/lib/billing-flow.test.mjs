import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'

const code = ts.transpileModule(readFileSync(new URL('./billing-flow.ts', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 } }).outputText
function fixture() {
  const items = new Map()
  let now = 10000
  const exports = {}
  runInNewContext(code, { exports, Intl, Number, Date: { now: () => now }, sessionStorage: {
    setItem: (key, value) => items.set(key, value), getItem: key => items.get(key) || null, removeItem: key => items.delete(key),
  } })
  return { api: exports, advance: ms => { now += ms }, items }
}

test('drafts and checkout intents are scoped to the user, organization and tab storage, and expire', () => {
  const { api, advance } = fixture()
  assert.equal(api.saveWorkspaceBillingDraft('org-a', 'alice', '  Production  '), true)
  assert.equal(api.workspaceBillingDraft('org-a', 'alice').name, 'Production')
  assert.equal(api.workspaceBillingDraft('org-b', 'alice'), null)
  assert.equal(api.workspaceBillingDraft('org-a', 'bob'), null)
  assert.equal(api.savePendingCheckout('org-a', 'alice', 'team'), true)
  assert.equal(api.pendingCheckout('org-a', 'alice').plan, 'team')
  api.clearBillingState('checkout', 'org-a', 'alice')
  assert.equal(api.pendingCheckout('org-a', 'alice'), null)
  advance(3600001)
  assert.equal(api.workspaceBillingDraft('org-a', 'alice'), null)
})

test('unavailable and malformed pricing never becomes a free price', () => {
  const { api } = fixture()
  for (const price of [undefined, {}, { id: 'price_1', amount: '-1', currency: 'USD', interval: 'month', intervalCount: 1 }]) {
    assert.equal(api.priceAvailable(price), false)
    assert.equal(api.planPrice(price), 'Price unavailable')
  }
  const usd = { id: 'price_1', amount: '49', currency: 'USD', interval: 'month', intervalCount: 1 }
  assert.equal(api.priceAvailable(usd), true)
  assert.match(api.planPrice(usd), /49/)
  assert.match(api.planPrice({ ...usd, amount: '99999999999999.99' }), /\.99 USD$/)
  assert.equal(api.priceInterval({ ...usd, interval: 'year', intervalCount: 2 }), 'every 2 years')
  assert.equal(api.priceAvailable({ ...usd, amount: '0' }), true, 'a real zero-price offer is distinct from missing pricing')
})

test('a checkout redirect cannot confirm activation without the subscribed plan and applied allowances', () => {
  const { api } = fixture()
  const value = { plan: 'team', managedBy: 'billing', overrides: [], subscription: { entitledPlan: 'team', status: 'active' } }
  assert.equal(api.subscriptionActivated(value, 'team', false), true)
  assert.equal(api.subscriptionActivated(value, 'team', true), false)
  assert.equal(api.subscriptionActivated({ ...value, plan: 'free' }, 'team', false), false)
  assert.equal(api.subscriptionActivated({ ...value, managedBy: 'operator' }, 'team', false), false)
  assert.equal(api.subscriptionActivated({ ...value, overrides: undefined }, 'team', false), false)
  assert.equal(api.subscriptionActivated({ ...value, subscription: { entitledPlan: 'team', status: 'incomplete' } }, 'team', false), false)
  assert.equal(api.subscriptionActivated(value, '', false), false)
})

test('Workspace limits get an upgrade route; ordinary permission errors do not', () => {
  const { api } = fixture()
  assert.equal(api.workspaceLimitError('workspaces limit exceeded: used 1, max 1'), true)
  assert.equal(api.workspaceLimitError('Workspace limit reached: 1 of 1 Workspaces in use.'), true)
  assert.equal(api.workspaceLimitError('Forbidden: you cannot create workspaces'), false)
  assert.equal(api.billingSettingsLocation('org-a').query.panel, 'billing')
})
