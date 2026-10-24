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
import test from 'node:test'

const source = await readFile(new URL('./ProviderFrame.vue', import.meta.url), 'utf8')

function functionBody(name) {
  const body = source.match(new RegExp(`function ${name}\\([^)]*\\)(?:\\s*:\\s*[^\\{]+)?\\s*\\{([\\s\\S]*?)\\n\\}`))?.[1]
  assert.ok(body, `expected ${name} in ProviderFrame.vue`)
  return body
}

test('any mounted provider can request or release full bleed with a boolean', () => {
  const body = functionBody('onLayoutChange')
  const handler = new Function('elementRef', 'providerFullBleedOverride', 'e',
    body.replace('(e as CustomEvent<{ fullBleed?: unknown }>)', 'e'))
  const element = {}
  const state = { value: null }

  handler({ value: element }, state, { target: element, detail: { fullBleed: true } })
  assert.equal(state.value, true)
  handler({ value: element }, state, { target: element, detail: { fullBleed: false } })
  assert.equal(state.value, false)
})

test('nested, stale, unmounted, and nonboolean layout requests are ignored', () => {
  const body = functionBody('onLayoutChange')
  const handler = new Function('elementRef', 'providerFullBleedOverride', 'e',
    body.replace('(e as CustomEvent<{ fullBleed?: unknown }>)', 'e'))
  const element = {}
  const state = { value: null }

  for (const target of [{}, { stale: true }]) {
    handler({ value: element }, state, { target, detail: { fullBleed: true } })
  }
  handler({ value: null }, state, { target: element, detail: { fullBleed: true } })
  for (const fullBleed of ['false', 0, null, undefined]) {
    handler({ value: element }, state, { target: element, detail: { fullBleed } })
  }
  assert.equal(state.value, null)
})

test('full-bleed defaults to App Studio workspace routes and accepts provider overrides', () => {
  const expression = source.match(/const isFullBleedProvider = computed\(\(\) =>([\s\S]*?)\n\)/)?.[1]
  assert.ok(expression)
  const createValue = new Function('computed', 'isAppStudioWorkspaceRoute', 'providerFullBleedOverride',
    `return computed(() => ${expression})`)
  const computed = getter => ({ get value() { return getter() } })
  const fullBleed = (workspaceRoute, override) => createValue(
    computed,
    { value: workspaceRoute },
    { value: override },
  ).value

  assert.equal(fullBleed(false, null), false)
  assert.equal(fullBleed(true, null), true)
  assert.equal(fullBleed(false, true), true)
  assert.equal(fullBleed(false, false), false)
})

test('route reset watcher tracks provider and subroute and runs synchronously', () => {
  const declaration = source.match(/watch\(\n  \(\) => \[props\.providerName, providerRoutePath\.value\] as const,\n  \(\) => \{[\s\S]*?\},\n  \{ flush: 'sync' \},\n\)/)?.[0]
  assert.ok(declaration)
  let getRouteIdentity
  let clearOverride
  let options
  const watch = (getter, callback, watchOptions) => {
    getRouteIdentity = getter
    clearOverride = callback
    options = watchOptions
  }
  const props = { providerName: 'external-editor' }
  const providerRoutePath = { value: 'library' }
  const state = { value: true }
  new Function('watch', 'props', 'providerRoutePath', 'providerFullBleedOverride',
    declaration.replace('] as const,', '],'))(watch, props, providerRoutePath, state)

  assert.deepEqual(getRouteIdentity(), ['external-editor', 'library'])
  props.providerName = 'external-editor-next'
  providerRoutePath.value = 'detail'
  assert.deepEqual(getRouteIdentity(), ['external-editor-next', 'detail'])
  assert.equal(options.flush, 'sync')
  clearOverride()
  assert.equal(state.value, null)
})

test('mounted-element cleanup clears the override and removes the mount listeners', () => {
  const body = functionBody('clearMountedElement')
  const makeCleanup = new Function(
    'pageResource', 'resourceSource', 'routeFocus', 'boundMount',
    'onRouteReady', 'onNavigate', 'onLayoutChange', 'onProviderBootstrapRetry',
    'elementRef', 'mountRef', 'loadState', 'loadError', 'providerFullBleedOverride',
    `let mountGeneration = 0; return function () { ${body} }`,
  )
  const removedListeners = []
  const removedElements = []
  const resourceSource = {}
  const element = { parentNode: { removeChild: child => removedElements.push(child) } }
  const state = { value: true }
  const loadState = { value: 'ready' }
  const loadError = { value: 'old error' }
  const handlers = [() => {}, () => {}, () => {}, () => {}]
  const clear = makeCleanup(
    { clear: sourceKey => assert.equal(sourceKey, resourceSource) },
    resourceSource,
    { clear() {} },
    { removeEventListener: name => removedListeners.push(name) },
    ...handlers,
    { value: element },
    { value: { replaceChildren() {} } },
    loadState,
    loadError,
    state,
  )

  clear()

  assert.equal(state.value, null)
  assert.equal(removedElements[0], element)
  assert.deepEqual(removedListeners, [
    'railgrid-route-ready',
    'railgrid-navigate',
    'railgrid-layout-change',
    'railgrid-provider-bootstrap-retry',
  ])
  assert.equal(loadState.value, 'idle')
  assert.equal(loadError.value, null)
})

test('mount resets old layout state and accepts a synchronous request from the new element', () => {
  const body = functionBody('mountElement').replace(' as HTMLElement', '')
  const onLayoutChange = new Function('elementRef', 'providerFullBleedOverride', 'e',
    functionBody('onLayoutChange').replace('(e as CustomEvent<{ fullBleed?: unknown }>)', 'e'))
  const elementRef = { value: { stale: true } }
  const state = { value: true }
  const order = []
  let emitDuringAppend = false
  let listenersBound = false
  const mount = {
    replaceChildren() {
      assert.equal(elementRef.value, null)
      assert.equal(state.value, null)
      order.push('replace')
    },
    appendChild(element) {
      assert.equal(elementRef.value, element)
      assert.equal(listenersBound, true)
      order.push('append')
      if (emitDuringAppend) onLayoutChange(elementRef, state, { target: element, detail: { fullBleed: true } })
    },
  }
  const makeMount = new Function(
    'mountRef', 'isCurrentMount', 'document', 'tagFor', 'elementRef',
    'providerFullBleedOverride', 'props', 'routeFocus', 'bindMountEvents', 'pushContext',
    `let mountGeneration = 0; return function (name, generation) { ${body} }`,
  )
  const mountElement = makeMount(
    { value: mount },
    () => true,
    { createElement: name => { order.push(`create:${name}`); return {} } },
    name => `railgrid-provider-${name}`,
    elementRef,
    state,
    { subPath: 'library' },
    { before: () => order.push('focus') },
    () => { listenersBound = true; order.push('bind') },
    () => order.push('context'),
  )

  assert.equal(mountElement('external-editor'), true)
  assert.equal(state.value, null)
  assert.ok(order.indexOf('bind') < order.indexOf('append'))
  assert.ok(order.indexOf('append') < order.indexOf('focus'))
  assert.ok(order.indexOf('focus') < order.indexOf('context'))

  emitDuringAppend = true
  assert.equal(mountElement('external-editor'), true)
  assert.equal(state.value, true)
})

test('provider bundle loading is delayed so cached navigation does not flash', () => {
  assert.match(source, /import \{ useDelayedLoading \} from '@\/portalkit\/useDelayedLoading'/)
  assert.match(source, /const providerLoadPending = computed\(\(\) => loadState\.value === 'loading'\)/)
  assert.match(source, /const showProviderLoading = useDelayedLoading\(providerLoadPending\)/)
  assert.match(source, /loadState === 'loading' && showProviderLoading/)
})

test('App Studio deep links receive a responsive three-region loading shell', () => {
  assert.match(source, /const isAppStudioWorkspaceRoute = computed/)
  assert.match(source, /aria-label="Loading App Studio workspace"/)
  assert.match(source, /md:grid-cols-\[minmax\(12rem,18rem\)_minmax\(0,2fr\)_minmax\(16rem,28rem\)\]/)
  assert.equal((source.match(/class="hidden content-start gap-3[^\n]+md:grid"/g) ?? []).length, 2)
  assert.match(source, /role="status"[\s\S]*aria-live="polite"[\s\S]*aria-busy="true"/)
})

test('readiness labels distinguish degraded and disconnected providers', () => {
  assert.match(source, /case 'BackendUnhealthy': return 'Degraded'/)
  assert.match(source, /case 'HeartbeatStale': return 'Disconnected'/)
  assert.match(source, /entry\.readinessMessage \|\| `Waiting for \$\{entry\.name\} to report ready\.`/)
})
