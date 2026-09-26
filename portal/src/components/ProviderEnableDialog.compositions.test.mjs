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

const portalRoot = path.resolve(new URL('../..', import.meta.url).pathname)

// Same harness as the hub-access test: compile the dialog's real setup script
// against minimal Vue doubles and drive it directly.
async function loadDialog(props) {
  const source = await readFile(path.join(portalRoot, 'src/components/ProviderEnableDialog.vue'), 'utf8')
  const script = source.match(/<script setup[^>]*>([\s\S]*?)<\/script>/)?.[1]
  assert.ok(script, 'ProviderEnableDialog.vue should contain a setup script')
  const body = script.replace(/^import[\s\S]*?from ['"][^'"]+['"]\n/gm, '')
  const harness = `
const ref = (value) => ({ value })
const computed = (getter) => ({ get value() { return getter() } })
const watch = (source, cb) => { if (typeof source === 'function') cb(source()) }
const nextTick = (fn) => fn && fn()
const onBeforeUnmount = () => {}
const window = { addEventListener() {}, removeEventListener() {} }
const document = { activeElement: null }
class HTMLElement {}
const emitted = []
const __props = ${JSON.stringify(props)}
const defineProps = () => __props
const defineEmits = () => (...args) => emitted.push(args)
${body}
export { acceptedComposition, canAcceptComposition, claims, compositionLabel, compositions, compositionKey, onConfirm, toggleComposition, verbsLabel, emitted }
`
  const { outputText } = ts.transpileModule(harness, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
  })
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)
}

// App Studio's real declaration. One spec.requires[] list holds both kinds of
// requirement: the entries naming a provider are the compositions, the one
// naming none is an ordinary platform claim and must not appear among them.
const provider = {
  name: 'app-studio',
  displayName: 'App Studio',
  requires: [
    {
      provider: 'infrastructure',
      group: 'infrastructure.railgrid.ai',
      resources: [{ name: 'instances', verbs: ['get', 'list', 'watch', 'create', 'update', 'delete'] }],
    },
    {
      provider: 'code',
      group: 'code.railgrid.ai',
      resources: [
        { name: 'repositories', verbs: ['get', 'list', 'watch', 'create', 'update'] },
        { name: 'repositorycommits', verbs: ['get', 'list', 'watch'] },
      ],
    },
    { group: 'authorization.k8s.io', resources: [{ name: 'subjectaccessreviews', verbs: ['create'] }] },
  ],
}

test('a workspace admin accepts every declared composition by default', async () => {
  const d = await loadDialog({ provider, orgRole: 'member', workspaceRole: 'admin' })
  d.onConfirm()
  const [event, claims, hub, composed] = d.emitted.at(-1)
  assert.equal(event, 'confirm')
  // The entry naming no provider is a claim, not a composition.
  assert.deepEqual(claims, [{ group: 'authorization.k8s.io', resource: 'subjectaccessreviews' }])
  assert.deepEqual(hub, [])
  assert.deepEqual(composed, [
    { provider: 'infrastructure', group: 'infrastructure.railgrid.ai', resource: 'instances' },
    { provider: 'code', group: 'code.railgrid.ai', resource: 'repositories' },
    { provider: 'code', group: 'code.railgrid.ai', resource: 'repositorycommits' },
  ])
})

test('an org admin may decide compositions too', async () => {
  const d = await loadDialog({ provider, orgRole: 'admin', workspaceRole: 'member' })
  assert.equal(d.canAcceptComposition(), true)
})

test('unchecking one leaves it out', async () => {
  const d = await loadDialog({ provider, orgRole: 'admin', workspaceRole: 'admin' })
  d.toggleComposition(d.compositions.value[1])
  d.onConfirm()
  assert.deepEqual(d.emitted.at(-1)[3].map((c) => c.resource), ['instances', 'repositorycommits'])
})

test('a member accepts nothing but can still enable', async () => {
  const d = await loadDialog({ provider, orgRole: 'member', workspaceRole: 'member' })
  assert.equal(d.canAcceptComposition(), false)
  // Toggling is a no-op for them, and confirming sends nothing.
  d.toggleComposition(d.compositions.value[0])
  d.onConfirm()
  assert.deepEqual(d.emitted.at(-1)[3], [])
})

test('the consent text says what the provider will do, and read-only says read', async () => {
  const d = await loadDialog({ provider, orgRole: 'admin', workspaceRole: 'admin' })
  const [instances, repositories, commits] = d.compositions.value
  assert.match(d.compositionLabel(instances), /^Create and manage Instances \(infrastructure\) in this workspace$/)
  assert.match(d.compositionLabel(repositories), /^Create and manage Repositories \(code\) in this workspace$/)
  assert.match(d.compositionLabel(commits), /^Read Repositorycommits \(code\) in this workspace$/)
})

test('a provider that declares no composition offers none', async () => {
  const d = await loadDialog({ provider: { name: 'kuery', displayName: 'Kuery', requires: [] }, orgRole: 'admin', workspaceRole: 'admin' })
  assert.deepEqual(d.compositions.value, [])
  d.onConfirm()
  assert.deepEqual(d.emitted.at(-1)[3], [])
})

test('a verb coordinate is never described as read-only or verbless', async () => {
  // A "<resource>/<verb>" requirement carries no verbs — the call IS the
  // capability, and the generated claim spells every verb.
  const d = await loadDialog({
    provider: {
      name: 'app-studio',
      displayName: 'App Studio',
      requires: [{ provider: 'code', group: 'code.railgrid.ai', resources: [{ name: 'repositories/commit' }] }],
    },
    orgRole: 'admin',
    workspaceRole: 'admin',
  })
  const [commit] = d.compositions.value
  assert.match(d.compositionLabel(commit), /^Create and manage Repositories\/commit \(code\) in this workspace$/)
  assert.equal(d.verbsLabel(commit), 'the call itself')
  d.onConfirm()
  assert.deepEqual(d.emitted.at(-1)[3], [{ provider: 'code', group: 'code.railgrid.ai', resource: 'repositories/commit' }])
})

test('a core-group requirement reads as a claim on the core group', async () => {
  const d = await loadDialog({
    provider: {
      name: 'app-studio',
      displayName: 'App Studio',
      requires: [{ resources: [{ name: 'secrets', verbs: ['get', 'list'], selector: { matchLabels: { 'railgrid.ai/owner': 'app-studio' } } }] }],
    },
    orgRole: 'admin',
    workspaceRole: 'admin',
  })
  assert.deepEqual(d.compositions.value, [])
  assert.deepEqual(d.claims.value.map((c) => `${c.group}/${c.resource}`), ['/secrets'])
  d.onConfirm()
  assert.deepEqual(d.emitted.at(-1)[1], [{ group: '', resource: 'secrets' }])
})
