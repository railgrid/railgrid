// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import test from 'node:test'
import ts from 'typescript'

const here = path.resolve(new URL('.', import.meta.url).pathname)
const source = await readFile(path.join(here, 'telemetry.ts'), 'utf8')
const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } })
const mod = { exports: {} }
new Function('module', 'exports', outputText)(mod, mod.exports)
const { routePattern, pageEvent, repeatsPage, allowedBeforeSignIn, providerOfTag, providerEvent, trackProps, externalOrigin, createTracker, MAX_BATCH } = mod.exports

const UUID = '([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})'
const WORKSPACE = `/:orgID${UUID}/:workspaceID${UUID}`

test('a page is reported by its route pattern, never by the IDs in the address bar', () => {
  const route = {
    name: 'mcp-detail',
    params: { orgID: '0b9c6a1e-1111-4222-8333-444455556666', workspaceID: '7d1f0c2a-aaaa-4bbb-8ccc-ddddeeeeffff', name: 'customer-secret-server' },
    matched: [{ path: `${WORKSPACE}/mcp/:name` }],
  }
  assert.deepEqual(pageEvent(route), { type: 'page', name: 'mcp-detail', route: '/:orgID/:workspaceID/mcp/:name' })
  assert.doesNotMatch(JSON.stringify(pageEvent(route)), /0b9c6a1e|7d1f0c2a|customer-secret-server/)
})

test('a platform provider page names the provider and drops the path inside it', () => {
  const route = {
    name: 'provider-frame',
    params: { name: 'agents', rest: ['agents', 'billing-bot', 'runs'] },
    matched: [{ path: `${WORKSPACE}/providers/:name/:rest(.*)*` }],
  }
  assert.equal(routePattern(route, () => 'platform'), '/:orgID/:workspaceID/providers/agents')
  assert.doesNotMatch(routePattern(route, () => 'platform'), /billing-bot/)
})

test("an organization's own provider is not named, and neither is one the catalog does not know", () => {
  const route = { name: 'provider-frame', params: { name: 'acme-internal-billing' }, matched: [{ path: `${WORKSPACE}/providers/:name/:rest(.*)*` }] }
  assert.equal(routePattern(route, () => 'org'), '/:orgID/:workspaceID/providers/org-provider')
  assert.equal(routePattern(route), '/:orgID/:workspaceID/providers/:name')
})

test('a provider name that is not a plain name stays a placeholder', () => {
  const route = { name: 'provider-frame', params: { name: 'Not A Name?x=1' }, matched: [{ path: `${WORKSPACE}/providers/:name/:rest(.*)*` }] }
  assert.equal(routePattern(route, () => 'platform'), '/:orgID/:workspaceID/providers/:name')
})

test('moving around inside a provider is one page view, moving between pages is not', () => {
  const edges = { type: 'page', name: 'provider-frame', route: '/:orgID/:workspaceID/providers/edges' }
  const agents = { type: 'page', name: 'provider-frame', route: '/:orgID/:workspaceID/providers/agents' }
  const detail = { type: 'page', name: 'mcp-detail', route: '/:orgID/:workspaceID/mcp/:name' }
  assert.equal(repeatsPage(undefined, edges), false)
  assert.equal(repeatsPage(edges, { ...edges }), true)
  assert.equal(repeatsPage(edges, agents), false)
  // Two resources of one kind share a pattern but are two visits.
  assert.equal(repeatsPage(detail, { ...detail }), false)
})

test('the catch-all and unnamed routes still produce a valid event', () => {
  assert.deepEqual(pageEvent({ name: 'not-found', matched: [{ path: '/:pathMatch(.*)*' }] }), { type: 'page', name: 'not-found', route: '/:pathMatch' })
  assert.deepEqual(pageEvent({ name: Symbol('x'), matched: [] }), { type: 'page', name: 'unnamed', route: '/' })
})

test('before sign-in only the sign-in page reports, and only as itself', () => {
  assert.equal(allowedBeforeSignIn({ type: 'page', name: 'login', route: '/login' }), true)
  assert.equal(allowedBeforeSignIn({ type: 'page', name: 'auth-callback', route: '/auth/callback' }), true)
  assert.equal(allowedBeforeSignIn({ type: 'event', name: 'sign_in', route: '/login' }), true)
  assert.equal(allowedBeforeSignIn({ type: 'page', name: 'dashboard', route: '/:orgID/:workspaceID' }), false)
  assert.equal(allowedBeforeSignIn({ type: 'page', name: 'login', route: '/:orgID/:workspaceID' }), false)
  assert.equal(allowedBeforeSignIn({ type: 'event', name: 'create_workspace', route: '/login' }), false)
})

test("a platform provider's events carry its name; an organization's own provider reports nothing", () => {
  assert.equal(providerOfTag('RAILGRID-PROVIDER-APP-STUDIO'), 'app-studio')
  assert.equal(providerOfTag('DIV'), '')
  assert.equal(providerOfTag('railgrid-provider-'), '')
  assert.deepEqual(providerEvent('edges', 'platform', { placement: 'header' }), { placement: 'header', provider: 'edges' })
  // The provider label is the host's, not the bundle's.
  assert.deepEqual(providerEvent('edges', 'platform', { provider: 'spoofed' }), { provider: 'edges' })
  assert.equal(providerEvent('acme-internal-billing', 'org', { customer: 'Initech' }), null)
  assert.equal(providerEvent('not-in-catalog', 'unknown'), null)
})

test('data-track-* attributes become event properties', () => {
  assert.deepEqual(trackProps({ track: 'enable_provider', trackPlacement: 'card', trackProviderKind: 'platform', other: 'x', trackEmpty: '' }),
    { placement: 'card', providerKind: 'platform' })
})

test('only another site counts as a referrer, and only its origin is kept', () => {
  const own = 'https://console.example.com'
  assert.equal(externalOrigin('https://railgrid.ai/pricing/?utm_source=x', own), 'https://railgrid.ai')
  assert.equal(externalOrigin('https://console.example.com/ui/0b9c6a1e/settings', own), '')
  assert.equal(externalOrigin('', own), '')
  assert.equal(externalOrigin('not a url', own), '')
})

test('nothing is recorded or sent while telemetry is off', () => {
  const sent = []
  const scheduled = []
  const tracker = createTracker({ enabled: () => false, send: batch => sent.push(batch), schedule: flush => scheduled.push(flush) })
  tracker.record({ type: 'page', name: 'dashboard', route: '/' })
  tracker.flush()
  assert.deepEqual(sent, [])
  assert.deepEqual(scheduled, [])
})

test('a burst leaves as one request, split at the relay limit', () => {
  const sent = []
  const scheduled = []
  const tracker = createTracker({ enabled: () => true, send: batch => sent.push(batch), schedule: flush => scheduled.push(flush) })
  for (let i = 0; i < MAX_BATCH + 3; i++) tracker.record({ type: 'event', name: `e${i}`, route: '/' })
  assert.equal(scheduled.length, 1)
  scheduled[0]()
  assert.deepEqual(sent.map(batch => batch.length), [MAX_BATCH, 3])
  tracker.record({ type: 'event', name: 'later', route: '/' })
  assert.equal(scheduled.length, 2)
})

test('a failing or unreachable relay never throws into the app and never grows a backlog', () => {
  const scheduled = []
  const tracker = createTracker({ enabled: () => true, send: () => { throw new Error('offline') }, schedule: flush => scheduled.push(flush) })
  tracker.record({ type: 'page', name: 'dashboard', route: '/' })
  assert.doesNotThrow(() => scheduled[0]())

  let delivered = 0
  const stuck = createTracker({ enabled: () => true, send: batch => { delivered += batch.length }, schedule: () => {} })
  for (let i = 0; i < MAX_BATCH * 10; i++) stuck.record({ type: 'event', name: 'e', route: '/' })
  stuck.flush()
  assert.equal(delivered, MAX_BATCH * 5)
})
