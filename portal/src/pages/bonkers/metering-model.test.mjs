import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

const source = fs.readFileSync(new URL('./metering-model.ts', import.meta.url), 'utf8')
const code = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText
const context = { exports: {}, Map, Set }
runInNewContext(code, context)
const { effectiveLimits, assignmentApplied, assignmentNeedsWrite, assignmentSelection, assignmentSource, limitsError, meteringDisplayNames, indexEntitlements } = context.exports
const limit = (meter, max) => ({ meter, max, shape: 'Level', enforce: 'Hard' })
const plan = { metadata: { name: 'small' }, spec: { limits: [limit('workspaces', '10'), limit('request_cost_rpm', '6000')] } }
function entitlement() {
  return { metadata: { name: 'org', generation: 3 }, spec: { plan: 'small', overrides: [limit('workspaces', '20')] }, status: { observedGeneration: 3, effectiveLimits: [limit('request_cost_rpm', '6000'), limit('workspaces', '20')] } }
}

test('preview retains overrides and includes meters defined only by an override', () => {
  const merged = effectiveLimits(plan, [limit('workspaces', '20'), limit('agents', '3')])
  assert.equal(merged.find(l => l.meter === 'workspaces').max, '20')
  assert.equal(merged.find(l => l.meter === 'request_cost_rpm').max, '6000')
  assert.equal(merged.find(l => l.meter === 'agents').max, '3')
  assert.equal(plan.spec.limits[0].max, '10', 'preview must not mutate the plan')
})

test('assignment success requires matching org assignment, entitlement generation and effective limits', () => {
  const ent = entitlement()
  assert.equal(assignmentApplied('small', 'small', plan, ent), true)
  assert.equal(assignmentApplied('free', 'small', plan, ent), false, 'accepted HTTP request is not application')
  ent.status.observedGeneration = 2
  assert.equal(assignmentApplied('small', 'small', plan, ent), false)
  ent.status.observedGeneration = 3
  ent.status.effectiveLimits[0].max = '600'
  assert.equal(assignmentApplied('small', 'small', plan, ent), false, 'stale effective limits are not success')
})

test('reset to default must actually move a directly edited entitlement', () => {
  const ent = entitlement()
  assert.equal(assignmentApplied(undefined, '', plan, ent), true)
  ent.spec.plan = 'large'
  assert.equal(assignmentApplied(undefined, '', plan, ent), false)
  assert.equal(assignmentApplied(undefined, '', plan, undefined), false)
})

test('a subscription is not described as the platform default', () => {
  const ent = entitlement()
  ent.metadata.labels = { 'metering.railgrid.ai/plan-source': 'billing' }
  assert.equal(assignmentSource(undefined, ent, 'small'), 'Subscription')
  assert.equal(assignmentSource('small', ent, 'free'), 'Assigned by platform admin')
})

test('invalid quotas are blocked before saving; meter-only caps can be empty', () => {
  assert.equal(limitsError([limit('workspaces', '0')]), null)
  assert.equal(limitsError([limit('request.cost-rpm', '0.001')]), null)
  assert.equal(limitsError([{ meter: 'agents', shape: 'Level', enforce: 'Meter', max: '' }]), null)
  assert.match(limitsError([limit('agents', '-1')]), /non-negative/)
  assert.match(limitsError([limit('agents', '2'), limit('agents', '3')]), /twice/)
})

test('direct entitlement assignments can be reset even when the organization plan is empty', () => {
  const ent = entitlement()
  assert.equal(assignmentSelection(undefined, ent, 'free'), 'small')
  assert.equal(assignmentNeedsWrite(undefined, ent, 'free', ''), true)
  assert.equal(assignmentNeedsWrite(undefined, ent, 'free', 'small'), true, 'admin may pin the current entitlement plan')
  ent.spec.plan = 'free'
  assert.equal(assignmentSelection(undefined, ent, 'free'), '')
  assert.equal(assignmentNeedsWrite(undefined, ent, 'free', ''), false)
  ent.metadata.labels = { 'metering.railgrid.ai/plan-source': 'billing' }
  assert.equal(assignmentNeedsWrite(undefined, ent, 'free', ''), true, 'reset can release an assignment source without changing the plan name')
})

test('diagnostic names prefer renamed organizations and retain independent account names', () => {
  const names = meteringDisplayNames(
    [{ name: 'org-a', displayName: 'Platform Engineering' }, { name: 'org-b', displayName: '' }],
    [
      { metadata: { name: 'org-a' }, spec: { displayName: 'Old organization name' } },
      { metadata: { name: 'org-b' }, spec: { displayName: 'Independent billing account' } },
      { metadata: { name: 'orphan' }, spec: { displayName: '' } },
    ],
  )
  assert.equal(names.get('org-a'), 'Platform Engineering')
  assert.equal(names.get('org-b'), 'Independent billing account')
  assert.equal(names.get('orphan'), undefined, 'unnamed records retain their ID fallback')
  assert.equal(names.get('missing'), undefined)
})

test('organization identity cannot be replaced by another entitlement for the same account', () => {
  const primary = { metadata: { name: 'org-a' }, spec: { account: 'org-a', plan: 'free' } }
  const addon = { metadata: { name: 'addon-a' }, spec: { account: 'org-a', plan: 'small' } }
  for (const items of [[primary, addon], [addon, primary]]) {
    const index = indexEntitlements(items)
    assert.equal(index.get('org-a'), primary)
    assert.equal(index.get('addon-a'), addon)
  }
  assert.equal(indexEntitlements([addon]).get('org-a'), undefined, 'an addon cannot impersonate a missing primary entitlement')
})
