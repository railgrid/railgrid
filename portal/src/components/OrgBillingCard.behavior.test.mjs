import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

// The card asks the hub for a hosted page (Checkout, the Customer Portal) and
// navigates to the URL that comes back. These tests run the production
// functions in a sandbox to check the one thing that matters about that
// wait: an answer that arrives after the card unmounted, the route or the
// organization changed, the session was replaced, or the user left and came
// back to the same URL, is dropped instead of navigated to.
const source = fs.readFileSync(new URL('./OrgBillingCard.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1]
const parsed = ts.createSourceFile('OrgBillingCard.ts', script, ts.ScriptTarget.Latest, true)

function loadFunction(name, context) {
  const declaration = parsed.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === name)
  assert.ok(declaration, `${name} exists in the production component`)
  const code = ts.transpileModule(declaration.getText(parsed), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
  }).outputText
  return runInNewContext(`${code}\n${name}`, context)
}

function deferred() {
  let resolve
  let reject
  const promise = new Promise((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

function cardFixture() {
  const checkout = deferred()
  const portal = deferred()
  const calls = { assign: [], checkout: [], portal: [] }
  let authRevision = 1
  const context = {
    props: { orgUuid: 'org-a' },
    route: { fullPath: '/org-a/settings/organizations' },
    tenant: {
      orgUUID: 'org-a',
      startOrgCheckout: async (org, plan) => { calls.checkout.push([org, plan]); return checkout.promise },
      openOrgBillingPortal: async (org) => { calls.portal.push([org]); return portal.promise },
    },
    authSessionRevision: () => authRevision,
    bumpAuth: () => { authRevision += 1 },
    disposed: false,
    hostedPageGeneration: 0,
    busy: { value: null },
    error: { value: null },
    window: { location: { assign: (url) => calls.assign.push(url) } },
    // The sandboxed functions check `err as Error`; share this realm's Error.
    Error,
  }
  for (const name of ['hostedPageRequest', 'hostedPageStillWanted', 'upgrade', 'manage']) context[name] = loadFunction(name, context)
  return { context, calls, checkout, portal }
}

test('a checkout answer that arrives with nothing changed is navigated to', async () => {
  const fixture = cardFixture()
  const pending = fixture.context.upgrade('team')
  assert.equal(fixture.context.busy.value, 'team')
  assert.deepEqual(fixture.calls.checkout, [['org-a', 'team']])
  fixture.checkout.resolve('https://checkout.example/cs_1')
  await pending
  assert.deepEqual(fixture.calls.assign, ['https://checkout.example/cs_1'])
  // The page is leaving; the button stays busy until it does.
  assert.equal(fixture.context.busy.value, 'team')
})

for (const [name, change] of [
  // Left and came back to the same URL: everything reads the same, except the
  // generation the scope watcher moved on the way out and on the way back.
  ['the scope generation moved on', (context) => { context.hostedPageGeneration += 1 }],
  ['the card unmounted', (context) => { context.disposed = true; context.hostedPageGeneration += 1 }],
  ['the route changed', (context) => { context.route.fullPath = '/org-a/settings/workspaces' }],
  ['the card was handed another organization', (context) => { context.props.orgUuid = 'org-b' }],
  ['the operating organization changed', (context) => { context.tenant.orgUUID = 'org-b' }],
  ['the session was replaced', (context) => { context.bumpAuth() }],
]) {
  test(`a checkout answer is dropped after ${name}`, async () => {
    const fixture = cardFixture()
    const pending = fixture.context.upgrade('team')
    change(fixture.context)
    fixture.checkout.resolve('https://checkout.example/cs_1')
    await pending
    assert.deepEqual(fixture.calls.assign, [])
    assert.equal(fixture.context.busy.value, null)
    assert.equal(fixture.context.error.value, null)
  })
}

test('a checkout failure is shown only where it was requested', async () => {
  const inPlace = cardFixture()
  const pending = inPlace.context.upgrade('team')
  inPlace.checkout.reject(new Error('the organization\'s plan is assigned by a platform administrator'))
  await pending
  assert.equal(inPlace.context.error.value, 'the organization\'s plan is assigned by a platform administrator')
  assert.equal(inPlace.context.busy.value, null)

  const moved = cardFixture()
  const pendingMoved = moved.context.upgrade('team')
  moved.context.route.fullPath = '/org-a/settings/workspaces'
  moved.checkout.reject(new Error('metering unreachable'))
  await pendingMoved
  assert.equal(moved.context.error.value, null)
  assert.equal(moved.context.busy.value, null)
})

test('the customer portal answer is fenced the same way', async () => {
  const fixture = cardFixture()
  const pending = fixture.context.manage()
  assert.equal(fixture.context.busy.value, 'portal')
  fixture.context.hostedPageGeneration += 1
  fixture.portal.resolve('https://billing.example/p/session')
  await pending
  assert.deepEqual(fixture.calls.assign, [])
  assert.equal(fixture.context.busy.value, null)

  const settled = cardFixture()
  const pendingSettled = settled.context.manage()
  settled.portal.resolve('https://billing.example/p/session')
  await pendingSettled
  assert.deepEqual(settled.calls.assign, ['https://billing.example/p/session'])
})

// The fence itself is wiring the sandbox cannot run: the generation must move
// synchronously on every route, card-organization and operating-organization
// change (a batched watcher could miss a change that is undone before the
// next flush) and on unmount, which also marks the card disposed.
test('the generation moves synchronously on scope changes and on unmount', () => {
  const watcher = script.match(/watch\(\[([\s\S]*?)\],\s*\(\)\s*=>\s*\{\s*hostedPageGeneration\+\+\s*\},\s*\{\s*flush:\s*'sync'\s*\}\)/)
  assert.ok(watcher, 'the scope watcher bumps hostedPageGeneration with flush: sync')
  for (const sourceExpr of ['route.fullPath', 'props.orgUuid', 'tenant.orgUUID']) {
    assert.ok(watcher[1].includes(sourceExpr), `the scope watcher watches ${sourceExpr}`)
  }
  assert.match(script, /onBeforeUnmount\(\(\) => \{\s*disposed = true\s*hostedPageGeneration\+\+\s*\}\)/)
})
