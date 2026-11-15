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
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import test from 'node:test'
import * as ts from 'typescript'

const dir = dirname(fileURLToPath(import.meta.url))
const vueURL = pathToFileURL(join(dir, '../../node_modules/vue/dist/vue.runtime.esm-bundler.js')).href
const { createRenderer, h } = await import(vueURL)
const script = ts.transpileModule(readFileSync(join(dir, 'useSidebarExpansion.ts'), 'utf8'), { compilerOptions: { module: ts.ModuleKind.ESNext } }).outputText.replace("from 'vue'", `from '${vueURL}'`)
const { useSidebarExpansion } = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(script)}`)
const noop = () => {}
const { createApp } = createRenderer({ createElement: () => ({}), createText: () => ({}), createComment: () => ({}), setText: noop, setElementText: noop, patchProp: noop, insert: noop, remove: noop, parentNode: () => null, nextSibling: () => null })

test('narrow navigation does not overwrite the desktop preference', () => {
  let preference = '1'
  let listener
  const media = { matches: false, addEventListener: (_type, fn) => { listener = fn }, removeEventListener: (_type, fn) => { if (listener === fn) listener = undefined } }
  globalThis.window = { matchMedia: () => media, localStorage: { getItem: () => preference, setItem: (_key, value) => { preference = value } } }
  let state
  const app = createApp({ setup() { state = useSidebarExpansion(); return () => h('div') } })
  app.mount({})
  assert.equal(state.sidebarExpanded.value, true)
  listener({ matches: true })
  assert.equal(state.sidebarExpanded.value, false)
  state.toggleSidebar()
  assert.equal(state.sidebarExpanded.value, true)
  assert.equal(preference, '1')
  state.closeNarrowSidebar()
  assert.equal(state.sidebarExpanded.value, false)
  listener({ matches: false })
  assert.equal(state.sidebarExpanded.value, true)
  assert.equal(preference, '1')
  app.unmount()
  assert.equal(listener, undefined)
  delete globalThis.window
})
