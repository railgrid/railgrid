import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

for (const [file, key, noun] of [['ProvidersSection.vue', 'provider', 'Provider'], ['OrgsSection.vue', 'organization', 'Organization']]) {
  const source = fs.readFileSync(new URL(file, import.meta.url), 'utf8')
  const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
  const parsed = ts.createSourceFile(file + '.ts', script, ts.ScriptTarget.Latest, true)
  function fixture(query) {
    const context = {
      route: { path: `/bonkers/${key}s`, query }, document: { activeElement: null }, HTMLElement: class {}, listTrigger: null,
      computed: fn => ({ get value() { return fn() } }),
      router: { push(location) { context.route.query = location.query; return Promise.resolve() } },
    }
    const list = parsed.statements.flatMap(node => ts.isVariableStatement(node) ? [...node.declarationList.declarations] : [])
      .find(node => node.name.getText(parsed) === 'listLocation')
    context.listLocation = runInNewContext(ts.transpileModule(`const ${list.getText(parsed)};\nlistLocation`, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context)
    context.backLocation = context.listLocation
    for (const name of [`${key}Location`, `open${noun}`, `close${noun}`]) {
      const fn = parsed.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name)
      context[name] = runInNewContext(ts.transpileModule(`${fn.getText(parsed)}\n${name}`, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context)
    }
    return context
  }
  test(`${noun} drill-down and cold-link return preserve unrelated query context`, () => {
    const s = fixture({ review: 'kept' })
    s[`open${noun}`]({ name: 'resource-a' })
    assert.equal(s.route.query[key], 'resource-a')
    assert.equal(s.route.query.review, 'kept')
    const reloaded = fixture({ ...s.route.query })
    reloaded[`close${noun}`]()
    assert.equal(reloaded.route.query[key], undefined)
    assert.equal(reloaded.route.query.review, 'kept')
  })
}
