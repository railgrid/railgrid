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

// These source-contract tests execute extracted functions; they do not mount
// Vue, exercise DOM event bubbling, or verify the rendered shell. Supplemental
// mock-browser checks cover that shell; live provider/backend acceptance stays separate.
const source = await readFile(new URL('./ProviderFrame.vue', import.meta.url), 'utf8')

function functionBody(name) {
  const body = source.match(new RegExp(`function ${name}\\([^)]*\\)(?:\\s*:\\s*[^\\{]+)?\\s*\\{([\\s\\S]*?)\\n\\}`))?.[1]
  assert.ok(body, `expected ${name} in ProviderFrame.vue`)
  return body
}

function executableBody(name) {
  return functionBody(name)
    .replace('(e as CustomEvent<{ fullBleed?: unknown }>)', 'e')
    .replaceAll('import.meta.env.DEV', 'isDev')
}

function layoutHandler() {
  return new Function(
    'elementRef', 'providerFullBleedOverride', 'e', 'isDev', 'console',
    executableBody('onLayoutChange'),
  )
}

test('any mounted provider can request or release full bleed with a boolean', () => {
  const handler = layoutHandler()
  const element = {}
  const state = { value: null }

  handler({ value: element }, state, { target: element, detail: { fullBleed: true } }, false, { warn() {} })
  assert.equal(state.value, true)
  handler({ value: element }, state, { target: element, detail: { fullBleed: false } }, false, { warn() {} })
  assert.equal(state.value, false)
})

test('nested, stale, unmounted, and nonboolean layout requests are ignored', () => {
  const handler = layoutHandler()
  const element = {}
  const state = { value: null }

  for (const target of [{}, { stale: true }]) {
    handler({ value: element }, state, { target, detail: { fullBleed: true } }, false, { warn() {} })
  }
  handler({ value: null }, state, { target: element, detail: { fullBleed: true } }, false, { warn() {} })
  for (const fullBleed of ['false', 0, null, undefined]) {
    handler({ value: element }, state, { target: element, detail: { fullBleed } }, false, { warn() {} })
  }
  assert.equal(state.value, null)
})

test('development warns on layout events outside the mounted provider root', () => {
  const handler = layoutHandler()
  const element = {}
  const state = { value: null }
  const warnings = []
  const devConsole = { warn: message => warnings.push(message) }

  handler({ value: element }, state, { target: {}, detail: { fullBleed: true } }, true, devConsole)
  handler({ value: null }, state, { target: element, detail: { fullBleed: true } }, true, devConsole)

  const instruction = '[railgrid] ignored railgrid-layout-change; dispatch it from the provider custom element root.'
  assert.deepEqual(warnings, [instruction, instruction])
  assert.equal(state.value, null)
})

test('production stays quiet for layout events outside the mounted provider root', () => {
  const handler = layoutHandler()
  const element = {}
  const state = { value: null }
  const warnings = []
  const prodConsole = { warn: message => warnings.push(message) }

  handler({ value: element }, state, { target: {}, detail: { fullBleed: true } }, false, prodConsole)
  handler({ value: null }, state, { target: element, detail: { fullBleed: true } }, false, prodConsole)

  assert.deepEqual(warnings, [])
  assert.equal(state.value, null)
})

test('App Studio keeps its workspace default and dynamic overrides require host access', () => {
  const expression = source.match(/const isFullBleedProvider = computed\(\(\) =>([\s\S]*?)\n\)/)?.[1]
  const accessExpression = source.match(/const accessAllowed = computed\(\(\) =>([\s\S]*?)\n\)/)?.[1]
  assert.ok(expression)
  assert.ok(accessExpression)
  const createValue = new Function('computed', 'isAppStudioWorkspaceRoute', 'providerFullBleedOverride', 'accessAllowed',
    `return computed(() => ${expression})`)
  const createAccess = new Function(
    'computed', 'catalogSettled', 'entry', 'requiresBinding', 'bindingsAuthoritative', 'providerBound',
    `return computed(() => ${accessExpression})`,
  )
  const computed = getter => ({ get value() { return getter() } })
  const access = (ready, bound) => createAccess(
    computed,
    { value: true },
    { value: { ready, serving: { ui: '/main.js' } } },
    { value: true },
    { value: true },
    { value: bound },
  ).value
  const fullBleed = (workspaceRoute, override, allowed = true) => createValue(
    computed,
    { value: workspaceRoute },
    { value: override },
    { value: allowed },
  ).value

  assert.equal(fullBleed(false, null), false)
  assert.equal(fullBleed(true, null, false), true)
  assert.equal(fullBleed(false, true), true)
  assert.equal(fullBleed(false, false), false)
  assert.equal(fullBleed(false, true, access(false, true)), false)
  assert.equal(fullBleed(false, true, access(true, false)), false)
  assert.equal(fullBleed(false, true, access(true, true)), true)
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
  const onLayoutChange = layoutHandler()
  const elementRef = { value: { stale: true } }
  const state = { value: true }
  const order = []
  let emitDuringAppend = false
  let listenersBound = false
  let bindCalls = 0
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
      if (emitDuringAppend) {
        onLayoutChange(elementRef, state, { target: element, detail: { fullBleed: true } }, false, { warn() {} })
      }
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
    () => {
      if (listenersBound) {
        order.push('already-bound')
        return
      }
      listenersBound = true
      bindCalls++
      order.push('bind')
    },
    () => order.push('context'),
  )

  assert.equal(mountElement('external-editor'), true)
  assert.equal(state.value, null)
  assert.ok(order.indexOf('bind') < order.indexOf('append'))
  assert.ok(order.indexOf('append') < order.indexOf('focus'))
  assert.ok(order.indexOf('focus') < order.indexOf('context'))

  emitDuringAppend = true
  assert.equal(mountElement('external-editor'), true)
  assert.equal(bindCalls, 1)
  assert.ok(order.indexOf('already-bound') < order.lastIndexOf('append'))
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
