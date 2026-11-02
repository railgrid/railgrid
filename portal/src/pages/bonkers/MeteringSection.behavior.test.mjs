import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

// The admin tables keep which rows are expanded by collection and row name,
// so the state follows the resource through search, paging and refresh. These
// tests run the production functions against a stub of the section's state.
const source = fs.readFileSync(new URL('./MeteringSection.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('MeteringSection.ts', script, ts.ScriptTarget.Latest, true)

function sectionFixture(plans) {
  const context = {
    items: { value: { plans: plans.map((name) => ({ metadata: { name } })) } },
    expanded: { value: {} },
  }
  for (const name of ['isExpanded', 'toggleExpanded', 'allExpanded', 'setAllExpanded', 'detailId']) {
    const declaration = parsed.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === name)
    assert.ok(declaration, `${name} exists in the production component`)
    const code = ts.transpileModule(declaration.getText(parsed), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
    context[name] = runInNewContext(`${code}\n${name}`, context)
  }
  return context
}

test('a row toggles independently and expand all follows the loaded items', () => {
  const s = sectionFixture(['free', 'team', 'enterprise'])
  assert.equal(s.isExpanded('plans', 'free'), false)
  s.toggleExpanded('plans', 'free')
  assert.equal(s.isExpanded('plans', 'free'), true)
  assert.equal(s.isExpanded('plans', 'team'), false)
  assert.equal(s.allExpanded('plans'), false)
  s.setAllExpanded('plans', true)
  assert.equal(s.allExpanded('plans'), true)
  assert.ok(['free', 'team', 'enterprise'].every((name) => s.isExpanded('plans', name)))
  s.toggleExpanded('plans', 'team')
  assert.equal(s.allExpanded('plans'), false, 'collapsing one row leaves the rest open and clears "all"')
  s.setAllExpanded('plans', false)
  assert.ok(['free', 'team', 'enterprise'].every((name) => !s.isExpanded('plans', name)))
})

test('collections keep separate expanded sets and an empty collection is never "all expanded"', () => {
  const s = sectionFixture([])
  s.toggleExpanded('entitlements', 'org-a')
  assert.equal(s.isExpanded('entitlements', 'org-a'), true)
  assert.equal(s.isExpanded('accounts', 'org-a'), false)
  assert.equal(s.allExpanded('plans'), false)
})

test('detail row ids are stable, unique per collection and safe as element ids', () => {
  const s = sectionFixture([])
  assert.equal(s.detailId('plans', 'free'), 'metering-plans-free-detail')
  assert.notEqual(s.detailId('plans', 'free'), s.detailId('entitlements', 'free'))
  assert.equal(s.detailId('accounts', 'railgrid-hub.1fde3890-2219'), 'metering-accounts-railgrid-hub.1fde3890-2219-detail')
  assert.match(s.detailId('plans', 'odd name/with:chars'), /^[A-Za-z0-9_.-]+$/)
})
