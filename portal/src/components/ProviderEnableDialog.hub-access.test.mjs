// Copyright 2026 The Faros Authors.
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

// Compile the dialog's real setup script against minimal doubles for Vue:
// props are a plain object, watch runs its callback once (immediate), and the
// emitted events are recorded.
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
export { acceptedHub, canAcceptHub, hubLabel, onConfirm, toggleHub, emitted }
`
  const { outputText } = ts.transpileModule(harness, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
  })
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)
}

const provider = {
  name: 'app-studio',
  displayName: 'App Studio',
  permissionClaims: [],
  hubAccess: [
    { capability: 'memberships.read', scope: 'org', reason: 'r1' },
    { capability: 'memberships.read', scope: 'workspace', reason: 'r2' },
    { capability: 'memberships.invite', scope: 'org', maxRole: 'member', allowInvite: true, reason: 'r3' },
  ],
}

test('an org admin accepts every requested capability by default', async () => {
  const d = await loadDialog({ provider, orgRole: 'admin', workspaceRole: 'member' })
  d.onConfirm()
  const [event, claims, hub] = d.emitted.at(-1)
  assert.equal(event, 'confirm')
  assert.deepEqual(claims, [])
  assert.deepEqual(hub, [
    { capability: 'memberships.read', scope: 'org' },
    { capability: 'memberships.read', scope: 'workspace' },
    { capability: 'memberships.invite', scope: 'org' },
  ])
})

test('a workspace admin can only accept workspace-scoped capabilities', async () => {
  const d = await loadDialog({ provider, orgRole: 'member', workspaceRole: 'admin' })
  assert.equal(d.canAcceptHub(provider.hubAccess[0]), false)
  assert.equal(d.canAcceptHub(provider.hubAccess[1]), true)
  // Toggling an org-scoped capability is a no-op for them.
  d.toggleHub(provider.hubAccess[2])
  d.onConfirm()
  assert.deepEqual(d.emitted.at(-1)[2], [{ capability: 'memberships.read', scope: 'workspace' }])
})

test('unchecking a capability leaves it out', async () => {
  const d = await loadDialog({ provider, orgRole: 'admin', workspaceRole: 'admin' })
  d.toggleHub(provider.hubAccess[2])
  d.onConfirm()
  assert.deepEqual(d.emitted.at(-1)[2].map((h) => h.capability + '/' + h.scope), ['memberships.read/org', 'memberships.read/workspace'])
})

test('a member accepts nothing but can still enable', async () => {
  const d = await loadDialog({ provider, orgRole: 'member', workspaceRole: 'member' })
  d.onConfirm()
  assert.deepEqual(d.emitted.at(-1)[2], [])
})

test('capabilities read as what the provider may do', async () => {
  const d = await loadDialog({ provider, orgRole: 'admin' })
  assert.match(d.hubLabel(provider.hubAccess[2]), /inviting them by email/)
  assert.match(d.hubLabel({ ...provider.hubAccess[2], allowInvite: false }), /existing users/)
})
