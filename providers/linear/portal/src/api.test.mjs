import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import ts from 'typescript'
// Compile the actual adapter with a small host-fetch binding, without a DOM.
const source = (await readFile(new URL('./api.ts', import.meta.url), 'utf8')).replace(/import .*from '.\/portalkit\/tenant'/, 'const providerFetch = (context: any) => context.fetch')
const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } })
const { API } = await import('data:text/javascript;base64,' + Buffer.from(outputText).toString('base64'))
test('calls host fetch with tenant scope and preserves stable command identity on uncertainty', async () => {
 const calls = []; const api = new API({ tenant: 'one', fetch: async (path, init) => { calls.push({ path, init }); throw new Error('connection lost') } }, new AbortController().signal)
 await assert.rejects(api.operation('linear', { action: 'createIssue', title: 'Test' }), /op-.*submission outcome unknown/)
 assert.equal(calls.length, 1); assert.match(calls[0].path, /^\/clusters\/one\/apis\/linear.providers.faros.sh/)
 const body = JSON.parse(calls[0].init.body); assert.equal(body.spec.connection, 'linear'); assert.equal(body.spec.action, 'createIssue')
})
test('discard stale workspace responses even if the host transport ignores abort', async () => {
 const controller = new AbortController(); const api = new API({ tenant: 'one', fetch: async () => { controller.abort(); return new Response(JSON.stringify({ items: [{ metadata: { name: 'private' } }] })) } }, controller.signal)
 await assert.rejects(api.list('connections'), { name: 'AbortError' })
})
