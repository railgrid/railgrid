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

function deferred() {
  let resolve
  let reject
  const promise = new Promise((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

async function loadFlow(enable) {
  const source = await readFile(path.join(portalRoot, 'src/composables/useProviderEnableFlow.ts'), 'utf8')
  const sourceWithoutImports = source.replace(/^import[\s\S]*?from ['"][^'"]+['"]\n/gm, '')
  const harness = `${sourceWithoutImports}\nmodule.exports = { useProviderEnableFlow }`
  const { outputText } = ts.transpileModule(harness, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
  })
  const module = { exports: {} }
  const watchCalls = []
  const toastCalls = []
  const ref = (value) => ({ value })
  const computed = (getter) => ({ get value() { return getter() } })
  const watch = (source, callback) => watchCalls.push({ source, callback })
  const toast = (...args) => toastCalls.push(args)
  new Function('module', 'exports', 'ref', 'computed', 'watch', 'toast', outputText)(module, module.exports, ref, computed, watch, toast)

  let currentScope = 'org-a/workspace-a/workspace'
  const flow = module.exports.useProviderEnableFlow({ scope: () => currentScope, enable })
  return {
    flow,
    toastCalls,
    setScope(value) {
      currentScope = value
      for (const call of watchCalls) call.callback()
    },
  }
}

test('a stale request cannot clear a newer enable operation after a scope change', async () => {
  const requests = []
  const h = await loadFlow(() => {
    const request = deferred()
    requests.push(request)
    return request.promise
  })
  const provider = { name: 'edges', displayName: 'Edges' }

  h.flow.openEnableDialog(provider)
  const staleRequest = h.flow.onDialogConfirm([])
  assert.equal(h.flow.busy.value.edges, true)

  h.setScope('org-b/workspace-b/workspace')
  assert.equal(h.flow.dialogProvider.value, null)
  h.flow.openEnableDialog(provider)
  const currentRequest = h.flow.onDialogConfirm([])
  assert.equal(requests.length, 2)

  requests[0].reject(new Error('old workspace failed'))
  await staleRequest
  assert.equal(h.flow.busy.value.edges, true)
  assert.equal(h.flow.actionError.value, null)
  assert.deepEqual(h.toastCalls, [])

  requests[1].reject(new Error('current workspace failed'))
  await currentRequest
  assert.equal(h.flow.busy.value.edges, undefined)
  assert.equal(h.flow.actionError.value, 'current workspace failed')
})

test('a dismissed consent request reports a same-scope failure as a toast', async () => {
  const request = deferred()
  const h = await loadFlow(() => request.promise)
  const provider = { name: 'code', displayName: 'Code' }

  h.flow.openEnableDialog(provider)
  const pending = h.flow.onDialogConfirm([])
  h.flow.closeEnableDialog()
  request.reject(new Error('gateway failure'))
  await pending

  assert.equal(h.flow.actionError.value, null)
  assert.equal(h.toastCalls.length, 1)
  assert.deepEqual(h.toastCalls[0], [
    'error',
    'Could not enable Code: gateway failure',
    {
      scope: 'org-a/workspace-a/workspace',
      source: 'provider-enable',
      dedupeKey: 'provider-enable:code',
    },
  ])
})

test('the consent selections reach the provider enable action unchanged', async () => {
  const selections = []
  const h = await loadFlow(async (...args) => selections.push(...args))
  const provider = { name: 'app-studio', displayName: 'App Studio' }
  const claims = [{ group: 'apps.railgrid.ai', resources: ['projects'] }]
  const hubAccess = [{ capability: 'provider.read' }]
  const compositions = [{ provider: 'code', accept: true }]

  h.flow.openEnableDialog(provider)
  await h.flow.onDialogConfirm(claims, hubAccess, compositions)

  assert.deepEqual(selections, [provider, claims, hubAccess, compositions])
  assert.equal(h.flow.dialogProvider.value, null)
})
