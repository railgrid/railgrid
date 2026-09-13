import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import ts from 'typescript'
// Compile the actual adapter with a small host-fetch binding, without a DOM.
const source = (await readFile(new URL('./api.ts', import.meta.url), 'utf8')).replace(/import .*from '.\/portalkit\/tenant'/, 'const providerFetch = (context: any) => context.fetch')
const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } })
const { API } = await import('data:text/javascript;base64,' + Buffer.from(outputText).toString('base64'))
test('calls host fetch with tenant scope and preserves stable command identity on uncertainty', async () => {
 const calls = []; const writes = {};
 const api = new API({ tenant: 'one', fetch: async (path, init) => {
  calls.push({ path, init });
  if (path.includes('/teams?')) return Response.json({ items: [{ metadata: { name: 'engineering' }, spec: { connection: 'linear', teamID: 'team' } }] });
  throw new Error('connection lost');
 } }, new AbortController().signal, writes)
 await assert.rejects(api.action('linear', { action: 'createIssue', teamID: 'team', title: 'Test' }), /write outcome needs confirmation/)
 assert.equal(calls.length, 2); assert.equal(calls[1].path, '/services/providers/linear/actions/clusters/one/teams/engineering/create_issue/v1')
 const body = JSON.parse(calls[1].init.body); assert.deepEqual(body.input, { title: 'Test' }); assert.equal(body.requestId, writes.createIssue.name)
 await assert.rejects(api.action('linear', { action: 'createIssue', teamID: 'team', title: 'Again' }), /previous write needs inspection/)
 assert.equal(calls.length, 2)

})
test('discard stale workspace responses even if the host transport ignores abort', async () => {
 const controller = new AbortController(); const api = new API({ tenant: 'one', fetch: async () => { controller.abort(); return new Response(JSON.stringify({ items: [{ metadata: { name: 'private' } }] })) } }, controller.signal)
 await assert.rejects(api.list('connections'), { name: 'AbortError' })
})
