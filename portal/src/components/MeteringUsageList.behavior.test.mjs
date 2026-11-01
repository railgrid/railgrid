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

function usageFixture(props) {
  const context = {
    props: { limits: [], usage: null, window: null, ...props },
    integerFormat: new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 }),
    computed: (calculate) => ({ get value() { return calculate() } }),
  }
  for (const name of ['num', 'show', 'stateLabel', 'usedClass', 'barClass', 'barPercent', 'barText']) {
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
