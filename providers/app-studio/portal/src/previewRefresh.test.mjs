import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./previewRefresh.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const { DevelopmentPreviewRefreshController } = await import(moduleURL)

function deferred() {
  let resolve
  let reject
  const promise = new Promise((nextResolve, nextReject) => {
    resolve = nextResolve
    reject = nextReject
  })
  return { promise, resolve, reject }
}

function createController(overrides = {}) {
  const state = {
    mounted: true,
    selected: { name: 'demo', binding: undefined },
  }
  const setSelectedProjects = []
  const options = {
    isMounted: () => state.mounted,
    selectedProjectName: () => state.selected?.name,
    getProject: async (name) => ({ name, binding: { provider: 'app-studio' } }),
    setSelectedProject: (project) => {
      state.selected = project
      setSelectedProjects.push(project)
    },
    ...overrides,
  }
  return {
    state,
    setSelectedProjects,
    controller: new DevelopmentPreviewRefreshController(options),
  }
}

test('hydrates an unbound selected project before preview authorization', async () => {
  const { state, setSelectedProjects, controller } = createController()
  const hydrated = await controller.hydrateProject('demo')

  assert.equal(hydrated?.binding?.provider, 'app-studio')
  assert.equal(state.selected.binding.provider, 'app-studio')
  assert.equal(setSelectedProjects.length, 1)

  let authorizationCalls = 0
  await controller.authorize('demo', 'demo-key', async () => {
    authorizationCalls += 1
    return 'authorized'
  })
  assert.equal(authorizationCalls, 1)
})

test('ignores project hydration that arrives after a project switch or unmount', async () => {
  const pendingSwitch = deferred()
  const switched = createController({ getProject: () => pendingSwitch.promise })
  const switchingHydration = switched.controller.hydrateProject('demo')
  switched.state.selected = { name: 'other' }
  switched.controller.invalidate()
  pendingSwitch.resolve({ name: 'demo', binding: { provider: 'app-studio' } })
  assert.equal(await switchingHydration, undefined)
  assert.equal(switched.setSelectedProjects.length, 0)

  const pendingUnmount = deferred()
  const unmounted = createController({ getProject: () => pendingUnmount.promise })
  const unmountHydration = unmounted.controller.hydrateProject('demo')
  unmounted.state.mounted = false
  unmounted.controller.dispose()
  pendingUnmount.resolve({ name: 'demo', binding: { provider: 'app-studio' } })
  assert.equal(await unmountHydration, undefined)
  assert.equal(unmounted.setSelectedProjects.length, 0)
})

test('coalesces same-key authorization while the first request is active', async () => {
  const { controller } = createController()
  const pending = deferred()
  let authorizationCalls = 0
  const request = () => {
    authorizationCalls += 1
    return pending.promise
  }

  const first = controller.authorize('demo', 'same-key', request)
  const second = controller.authorize('demo', 'same-key', request)
  assert.equal(authorizationCalls, 1)

  pending.resolve('ready')
  assert.equal(await first, 'ready')
  assert.equal(await second, 'ready')
})

test('clears a rejected authorization so the same key can retry to ready', async () => {
  const { controller } = createController()
  let authorizationCalls = 0

  await assert.rejects(
    controller.authorize('demo', 'retry-key', async () => {
      authorizationCalls += 1
      throw new Error('preview is still starting')
    }),
    /preview is still starting/,
  )

  const ready = await controller.authorize('demo', 'retry-key', async () => {
    authorizationCalls += 1
    return 'ready'
  })

  assert.equal(ready, 'ready')
  assert.equal(authorizationCalls, 2)
})

test('does not let an invalidated request clear a replacement authorization', async () => {
  const oldRequest = deferred()
  const replacementRequest = deferred()
  const { controller } = createController()

  const oldAuthorization = controller.authorize('demo', 'same-key', () => oldRequest.promise)
  controller.invalidate()
  const replacementAuthorization = controller.authorize('demo', 'same-key', () => replacementRequest.promise)

  oldRequest.resolve('stale')
  replacementRequest.resolve('ready')

  assert.equal(await oldAuthorization, 'stale')
  assert.equal(await replacementAuthorization, 'ready')

  const followUp = await controller.authorize('demo', 'same-key', async () => 'follow-up')
  assert.equal(followUp, 'follow-up')
})
