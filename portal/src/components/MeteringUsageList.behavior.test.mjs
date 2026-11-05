import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

// Execute the production row calculation so missing samples cannot silently
// become zero and finite chart ranges cannot corrupt the original quantities.
const source = fs.readFileSync(new URL('./MeteringUsageList.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('MeteringUsageList.ts', script, ts.ScriptTarget.Latest, true)

const meterSource = fs.readFileSync(new URL('./metering-meters.ts', import.meta.url), 'utf8')
const meterCode = ts.transpileModule(meterSource, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText
const { meterLabel, meterDefinition } = await import('data:text/javascript;base64,' + Buffer.from(meterCode).toString('base64'))

function usageFixture(props) {
  const context = {
    props: { limits: [], usage: null, window: null, ...props },
    label: meterLabel,
    meterDefinition,
    integerFormat: new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 }),
    computed: (calculate) => ({ get value() { return calculate() } }),
  }
  for (const name of ['num', 'show', 'stateLabel', 'usedClass', 'barClass', 'barPercent', 'barText', 'rateUnit', 'headlineRank', 'pickHeadline', 'summarize']) {
    const declaration = parsed.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === name)
    assert.ok(declaration, `${name} exists in the production component`)
    const code = ts.transpileModule(declaration.getText(parsed), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
    context[name] = runInNewContext(`${code}\n${name}`, context)
  }
  const declaration = parsed.statements.flatMap((node) => ts.isVariableStatement(node) ? [...node.declarationList.declarations] : [])
    .find((node) => node.name.getText(parsed) === 'rows')
  assert.ok(declaration?.initializer, 'production rows calculation exists')
  const code = ts.transpileModule(`const result = ${declaration.initializer.getText(parsed)}`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText
  context.rows = runInNewContext(`${code}\nresult`, context).value
  return context
}

const limit = (max = '10', enforce = 'Hard') => ({ meter: 'workspaces', shape: 'Level', max, enforce })
// Objects built inside the sandbox have the sandbox's prototypes; compare them by value.
const plain = (value) => JSON.parse(JSON.stringify(value))

test('rate limits retain their units in compact rows and accessible progress text', () => {
  const fixture = usageFixture({ limits: [{ meter: 'request_cost_rpm', max: '600', enforce: 'Hard' }], usage: { request_cost_rpm: '25' } })
  assert.equal(fixture.rateUnit(fixture.rows[0]), 'cost units / min')
  assert.equal(fixture.barText(fixture.rows[0]), '25 of 600 cost units / min')
  assert.equal(fixture.rateUnit({ meter: 'workspaces' }), '')
})

test('missing, null and empty measurements remain unavailable while zero is measured', () => {
  for (const usage of [{}, { workspaces: null }, { workspaces: '' }]) {
    const fixture = usageFixture({ limits: [limit()], usage })
    assert.equal(fixture.rows[0].used, null)
    assert.equal(fixture.rows[0].ratio, null)
    assert.equal(fixture.stateLabel(fixture.rows[0]), 'Usage unavailable')
  }
  const fixture = usageFixture({ limits: [limit()], usage: { workspaces: '0' } })
  assert.equal(fixture.rows[0].used, '0')
  assert.equal(fixture.rows[0].ratio, 0)
  assert.equal(fixture.rows[0].state, 'ok')
})

test('a zero allowance has a valid chart range at zero and above zero', () => {
  for (const [used, state, percent] of [['0', 'at', 0], ['1', 'over', 100]]) {
    const fixture = usageFixture({ limits: [limit('0')], usage: { workspaces: used } })
    assert.equal(fixture.rows[0].state, state)
    assert.equal(fixture.barPercent(fixture.rows[0]), percent)
    assert.equal(fixture.barText(fixture.rows[0]), `${used} of 0${state === 'over' ? ', over the limit' : ''}`)
  }
})

test('fractional and unsafe integer quantities retain API precision in display', () => {
  const raw = '0.000000000000000001'
  const fixture = usageFixture({ limits: [limit('1')], usage: { workspaces: raw } })
  assert.equal(fixture.rows[0].used, raw)
  assert.equal(fixture.show('9007199254740993'), '9007199254740993')
  assert.equal(fixture.show('10000'), '10,000')
})

test('Meter mode does not present an exceeded numeric reference as enforcement', () => {
  const fixture = usageFixture({ limits: [limit('10', 'Meter')], usage: { workspaces: '12' } })
  const row = fixture.rows[0]
  assert.equal(fixture.stateLabel(row), 'Metered')
  assert.equal(fixture.barClass(row), 'k-progress__bar--accent')
  assert.equal(fixture.usedClass(row), 'text-text-primary')
})

test('an unlimited meter and a usage-only meter have no utilization percentage', () => {
  const fixture = usageFixture({ usage: { extra: '27' } })
  const unlimited = usageFixture({ limits: [{ meter: 'workspaces', enforce: 'Meter' }], usage: { workspaces: '15' } })
  assert.equal(unlimited.rows[0].ratio, null)
  assert.equal(unlimited.rows[0].max, '')
  assert.equal(fixture.rows.find((row) => row.meter === 'extra').ratio, null)
})

// The collapsed table row shows one line: the count and the meter that
// matters most. Over a limit outranks at it, which outranks merely close.
test('the summary leads with the meter closest to its limit, over before at', () => {
  const limits = [
    { meter: 'agents', shape: 'Level', max: '10', enforce: 'Hard' },
    { meter: 'edges', shape: 'Level', max: '4', enforce: 'Hard' },
    { meter: 'services', shape: 'Level', max: '4', enforce: 'Soft' },
  ]
  const near = usageFixture({ limits, usage: { agents: 5, edges: 4, services: 1 } })
  assert.equal(near.pickHeadline(near.rows).meter, 'edges', 'at the limit outranks halfway')
  const over = usageFixture({ limits, usage: { agents: 5, edges: 4, services: 6 } })
  assert.equal(over.pickHeadline(over.rows).meter, 'services', 'over a limit outranks at one')
  const summary = over.summarize(over.rows, true, true)
  assert.equal(summary.count, '3 limits')
  assert.equal(summary.headline.meter, 'services')
  assert.equal(summary.detail, '')
})

test('a plan summary counts enforcement modes and an account summary names its first meters', () => {
  const plan = usageFixture({ limits: [
    { meter: 'agents', max: '5', enforce: 'Hard' },
    { meter: 'edges', max: '5', enforce: 'Hard' },
    { meter: 'services', max: '', enforce: 'Meter' },
  ], usage: null })
  assert.deepEqual(plain(plan.summarize(plan.rows, false, true)), { count: '3 limits', headline: null, detail: '2 hard, 1 metered' })

  const account = usageFixture({ limits: [], usage: { services: 3, agents: 1, edges: 2 } })
  assert.deepEqual(plain(account.summarize(account.rows, true, false)), { count: '3 meters', headline: null, detail: 'agents 1, edges 2, +1 more' })
  const one = usageFixture({ limits: [], usage: { workspaces: 2 } })
  assert.deepEqual(plain(one.summarize(one.rows, true, false)), { count: '1 meter', headline: null, detail: 'Workspaces 2' })
})

test('an entitlement without a measurement says so instead of inventing a headline', () => {
  const fixture = usageFixture({ limits: [limit('10'), { meter: 'agents', max: '5', enforce: 'Hard' }], usage: {} })
  assert.deepEqual(plain(fixture.summarize(fixture.rows, true, true)), { count: '2 limits', headline: null, detail: 'usage unavailable' })
  const empty = usageFixture({ limits: [], usage: {} })
  assert.deepEqual(plain(empty.summarize(empty.rows, true, false)), { count: '0 meters', headline: null, detail: '' })
})
