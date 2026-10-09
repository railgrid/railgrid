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

// Mounts the real useNavigationDock composable under Vue's own renderer and
// swaps page instances the way <router-view> does, so the unmount ordering
// that hid the TerminalDock behind the sidebar is exercised by Vue itself
// rather than restated by the test. No DOM: the renderer gets no-op node ops.

import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { pathToFileURL } from 'node:url'
import test from 'node:test'
import * as ts from 'typescript'

const dir = path.dirname(new URL(import.meta.url).pathname)
const vueEntry = pathToFileURL(path.join(dir, '..', '..', 'node_modules', 'vue', 'dist', 'vue.runtime.esm-bundler.js')).href

function transpile(file, rewrites) {
  let js = ts.transpileModule(fs.readFileSync(path.join(dir, file), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
  }).outputText
  for (const [from, to] of rewrites) js = js.replace(from, to)
  return `data:text/javascript;charset=utf-8,${encodeURIComponent(js)}`
}

// Minimal browser surface for the dock's listeners, storage and clamp frame.
const listeners = new Set()
globalThis.window = {
  innerWidth: 1440,
  innerHeight: 900,
  localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  addEventListener: (type) => listeners.add(type),
  removeEventListener: (type) => listeners.delete(type),
  requestAnimationFrame: () => 1,
  cancelAnimationFrame() {},
}
globalThis.ResizeObserver = class { observe() {} disconnect() {} }

const insetsURL = transpile('useLayoutInsets.ts', [["from 'vue'", `from '${vueEntry}'`]])
const dockURL = transpile('useNavigationDock.ts', [
  ["from 'vue'", `from '${vueEntry}'`],
  // encodeURIComponent leaves single quotes alone, so the nested URL is
  // embedded in double quotes.
  ["from '@/composables/useLayoutInsets'", `from "${insetsURL}"`],
])
const insets = await import(insetsURL)
const { useNavigationDock } = await import(dockURL)
const { createRenderer, defineComponent, h, nextTick, ref, shallowRef } = await import(vueEntry)

const noop = () => {}
const { createApp } = createRenderer({
  createElement: () => ({}),
  createText: () => ({}),
  createComment: () => ({}),
  setText: noop,
  setElementText: noop,
  patchProp: noop,
  insert: noop,
  remove: noop,
  parentNode: () => null,
  nextSibling: () => null,
  querySelector: () => null,
})

// Stands in for AppLayout: every routed page renders its own instance.
const Shell = defineComponent({
  name: 'Shell',
  setup() {
    useNavigationDock(ref(true))
    return () => h('div')
  },
})
const PageA = defineComponent({ name: 'PageA', setup: () => () => h(Shell) })
const PageB = defineComponent({ name: 'PageB', setup: () => () => h(Shell) })
const Standalone = defineComponent({ name: 'Standalone', setup: () => () => h('div') })

function snapshot() {
  const s = insets.useLayoutInsets()
  return { left: s.left, right: s.right, bottom: s.bottom }
}

test('the sidebar inset survives a router-view swap between AppLayout pages', async () => {
  const page = shallowRef(PageA)
  const app = createApp({ setup: () => () => h(page.value) })
  app.mount({})
  await nextTick()
  assert.equal(snapshot().left, '13rem', 'the first page publishes the expanded rail')

  page.value = PageB
  await nextTick()
  await nextTick() // post-flush: the outgoing page's onUnmounted has now run
  assert.equal(snapshot().left, '13rem', 'the outgoing page must not wipe the incoming page\'s inset')

  page.value = Standalone
  await nextTick()
  await nextTick()
  assert.deepEqual(snapshot(), { left: '0px', right: '0px', bottom: '0px' }, 'a page without AppLayout clears the inset')

  app.unmount()
  assert.equal(listeners.size, 0, 'the dock removed every window listener it added')
})
