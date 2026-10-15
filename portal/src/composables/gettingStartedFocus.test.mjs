import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('./gettingStartedFocus.ts', import.meta.url), 'utf8')
const js = ts.transpileModule(source.replace(/^import .*$/m, ''), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
}).outputText
const module = await import(`data:text/javascript;base64,${Buffer.from(`
const cleanups = [];
const watchers = [];
const ref = value => ({ value });
const readonly = value => value;
const onUnmounted = callback => cleanups.push(callback);
const watch = (source, callback) => { watchers.push(callback); callback(source.value); };
${js}
export { cleanups, watchers };
`).toString('base64')}`)

test('focus follows the wizard and stale route cleanup cannot clear a newer owner', () => {
  module.useGettingStartedFocus({ value: true })
  assert.equal(module.gettingStartedFocused.value, true)
  module.watchers[0](false)
  assert.equal(module.gettingStartedFocused.value, false)
  module.watchers[0](true)
  assert.equal(module.gettingStartedFocused.value, true)
  module.useGettingStartedFocus({ value: true })
  module.cleanups[0]()
  module.watchers[0](false)
  assert.equal(module.gettingStartedFocused.value, true)
  module.cleanups[1]()
  assert.equal(module.gettingStartedFocused.value, false)
})
