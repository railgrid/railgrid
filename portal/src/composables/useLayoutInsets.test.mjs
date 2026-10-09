/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { pathToFileURL } from 'node:url'
import test from 'node:test'
import * as ts from 'typescript'

const dir = path.dirname(new URL(import.meta.url).pathname)
const source = fs.readFileSync(path.join(dir, 'useLayoutInsets.ts'), 'utf8')
const vueEntry = pathToFileURL(path.join(dir, '..', '..', 'node_modules', 'vue', 'dist', 'vue.runtime.esm-bundler.js')).href
const js = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText.replace("from 'vue'", `from '${vueEntry}'`)
const mod = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(js)}`)

const RAIL = { left: '13rem', right: '0px', bottom: '0px' }
const ZERO = { left: '0px', right: '0px', bottom: '0px' }

function snapshot() {
  const s = mod.useLayoutInsets()
  return { left: s.left, right: s.right, bottom: s.bottom }
}

test('a claim publishes and releases its insets', () => {
  const claim = mod.claimLayoutInsets()
  claim.set(RAIL)
  assert.deepEqual(snapshot(), RAIL)
  claim.release()
  assert.deepEqual(snapshot(), ZERO)
})

test('a stale release cannot wipe the insets of the page that took over', () => {
  // Vue runs the outgoing AppLayout's onUnmounted post-flush, i.e. after the
  // incoming AppLayout's setup already claimed and published. The terminal
  // dock used to lose the sidebar inset on every page navigation this way.
  const outgoing = mod.claimLayoutInsets()
  outgoing.set(RAIL)
  const incoming = mod.claimLayoutInsets()
  incoming.set(RAIL)
  outgoing.release()
  assert.deepEqual(snapshot(), RAIL)
  outgoing.set({ left: '3.5rem', right: '0px', bottom: '0px' })
  assert.deepEqual(snapshot(), RAIL, 'a stale writer must not publish either')
  incoming.release()
  assert.deepEqual(snapshot(), ZERO)
})

test('the live claim still resets the insets for standalone shells', () => {
  // Platform admin and login render no AppLayout, so the last page's release
  // must clear the insets when nothing newer has claimed them.
  const page = mod.claimLayoutInsets()
  page.set({ left: '0px', right: '0px', bottom: '44px' })
  page.release()
  assert.deepEqual(snapshot(), ZERO)
  page.release()
  assert.deepEqual(snapshot(), ZERO, 'release is idempotent')
})
