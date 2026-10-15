import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('./firstWelcome.ts', import.meta.url), 'utf8')
let revision = 0
async function load() {
  const { outputText } = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext } })
  return import(`data:text/javascript;base64,${Buffer.from(outputText + `\n// ${revision++}`).toString('base64')}`)
}

test('first welcome is user-scoped, survives reload, and ignores unowned legacy dismissals', async () => {
  const original = globalThis.localStorage
  const values = new Map()
  globalThis.localStorage = { getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key) }
  try {
    let api = await load()
    assert.equal(api.claimFirstWelcome(''), false)
    assert.equal(api.claimFirstWelcome('alice'), true)
    assert.equal(api.claimFirstWelcome('alice'), false, 'another Workspace must not prompt again')
    api = await load()
    assert.equal(api.claimFirstWelcome('alice'), false, 'reload must not prompt again')
    assert.equal(api.claimFirstWelcome('bob'), true, 'another account has its own first visit')
    values.set('railgrid:portal:welcome-dismissed', JSON.stringify(['old-workspace']))
    assert.equal(api.claimFirstWelcome('existing-user'), true, 'unowned history cannot identify this account')
    assert.equal(api.claimFirstWelcome('new-user'), true, 'unowned history must not suppress a new account')
    values.set('railgrid:portal:welcome-dismissed', 'broken json')
    assert.equal(api.claimFirstWelcome('newer-user'), true)
    globalThis.localStorage = { getItem() { throw new Error('disabled') } }
    assert.equal(api.claimFirstWelcome('private-user'), true)
    assert.equal(api.claimFirstWelcome('private-user'), false, 'session memory works without storage')
  } finally { globalThis.localStorage = original }
})
