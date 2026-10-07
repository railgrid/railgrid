import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

const source = fs.readFileSync(new URL('./MCPPage.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('component.ts', script, ts.ScriptTarget.Latest, true)
function load(name, context = {}) {
  const node = parsed.statements.find(n => ts.isFunctionDeclaration(n) && n.name?.text === name)
  const code = ts.transpileModule(node.getText(parsed), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  return runInNewContext(`${code}\n${name}`, context)
}
function deleteContext(uid = 'observed-uid', status = 204) {
  const server = { name: 'ops', uid }
  const requests = []
  const context = {
    base: () => '/mcpservers', tenantIdentity: () => 'scope', selected: { value: 'ops' },
    servers: { value: [server] }, pendingDeletion: { value: null }, mutationError: { value: null },
    confirmDialog: async () => true, connect: { value: {} }, connectRequestID: 0,
    router: { replace: async () => {} }, load: async () => {}, encodeURIComponent,
    authFetch: async (url, options) => { requests.push({ url, options }); return { ok: status < 400, status } },
  }
  return { context, requests, server }
}

test('delete uses the identity shown in the confirmation', async () => {
  const { context, requests } = deleteContext('observed/uid')
  await load('remove', context)('ops')
  assert.equal(requests[0].url, '/mcpservers/ops?uid=observed%2Fuid')
  assert.equal(context.servers.value.length, 0)
})
test('delete conflict retains the server and explains recovery', async () => {
  const { context, requests, server } = deleteContext('old-uid', 409)
  await load('remove', context)('ops')
  assert.equal(requests.length, 1)
  assert.equal(context.servers.value[0], server)
  assert.equal(context.pendingDeletion.value, null)
  assert.match(context.mutationError.value, /changed.*Refresh and review/)
})
test('delete refuses a snapshot without an object identity', async () => {
  const { context, requests } = deleteContext('')
  await load('remove', context)('ops')
  assert.equal(requests.length, 0)
  assert.match(context.mutationError.value, /Refresh and review/)
})
test('delayed delete success retains an already loaded replacement', async () => {
  const { context } = deleteContext('old-uid')
  const replacement = { name: 'ops', uid: 'replacement-uid' }
  const connection = { endpointURL: '/replacement' }
  let navigations = 0
  context.router.replace = async () => { navigations += 1 }
  context.authFetch = async () => {
    context.servers.value = [replacement]
    context.connect.value = { ops: connection }
    return { ok: true, status: 204 }
  }
  await load('remove', context)('ops')
  assert.equal(context.servers.value[0], replacement)
  assert.equal(context.connect.value.ops, connection)
  assert.equal(context.pendingDeletion.value, null)
  assert.equal(navigations, 0)
})
