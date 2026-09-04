import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from '../../portal/node_modules/typescript/lib/typescript.js'

// The standalone PortalKit package has no test-runner dependency of its own.
// Transpile the real canonical module with the TypeScript package already
// present in the root portal, then exercise its public runtime contract in a
// tiny document/event harness. This keeps the tests behavioral without adding
// another dependency or copying implementation into the fixture.
const source = readFileSync(new URL('./toast.ts', import.meta.url), 'utf8')
const output = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
}).outputText

if (typeof globalThis.HTMLElement === 'undefined') globalThis.HTMLElement = class HTMLElement {}
class TestDocument extends EventTarget {
  activeElement = null
  hidden = false
}
globalThis.document = new TestDocument()

const kit = await import(`data:text/javascript;base64,${Buffer.from(output).toString('base64')}`)

let testScope = ''
test.afterEach(() => {
  if (testScope) kit.clearToasts(testScope)
  testScope = ''
})

function scope(name) {
  testScope = `toast-behavior-${name}-${Date.now()}-${Math.random().toString(36).slice(2)}`
  return testScope
}

test('normalizes defaults, recovery persistence, and legacy calls', () => {
  const currentScope = scope('defaults')
  const snapshots = []
  const off = kit.subscribeToasts(commands => snapshots.push(commands.filter(command => command.scope === currentScope)))
  const infoID = kit.toast({ message: '  read complete  ', scope: currentScope })
  const okID = kit.toast('ok', 'saved', { scope: currentScope })
  const clampedID = kit.toast({ message: 'short-lived', scope: currentScope, duration: 1 })
  const warningID = kit.toast({ message: 'needs attention', kind: 'warning', scope: currentScope })
  const actionID = kit.toast({
    message: 'retry available',
    kind: 'info',
    scope: currentScope,
    action: { label: 'Retry', run: () => {} },
  })

  const current = snapshots.at(-1)
  assert.deepEqual(current.map(command => command.id), [warningID, infoID, clampedID, actionID, okID])
  assert.ok(infoID < okID && okID < clampedID && clampedID < warningID && warningID < actionID)
  assert.equal(current.find(command => command.id === infoID).duration, 6000)
  assert.equal(current.find(command => command.id === okID).duration, 5000)
  assert.equal(current.find(command => command.id === clampedID).duration, 5000)
  assert.equal(current.find(command => command.id === warningID).persistent, true)
  assert.equal(current.find(command => command.id === actionID).persistent, true)
  assert.equal(current.find(command => command.id === infoID).message, 'read complete')
  assert.equal(current.find(command => command.id === actionID).actionLabel, 'Retry')
  off()
})

test('allows finite warning and error durations while clamping short values', () => {
  const currentScope = scope('numeric-duration')
  const snapshots = []
  const off = kit.subscribeToasts(commands => snapshots.push(commands.filter(command => command.scope === currentScope)))
  const warning = kit.toast({ kind: 'warning', message: 'warning window', scope: currentScope, duration: 7000 })
  const error = kit.toast({ kind: 'error', message: 'error window', scope: currentScope, duration: 1 })
  const defaultWarning = kit.toast({ kind: 'warning', message: 'persistent warning', scope: currentScope })

  const current = snapshots.at(-1)
  assert.equal(current.find(command => command.id === warning).persistent, false)
  assert.equal(current.find(command => command.id === warning).duration, 7000)
  assert.equal(current.find(command => command.id === error).persistent, false)
  assert.equal(current.find(command => command.id === error).duration, 5000)
  assert.equal(current.find(command => command.id === defaultWarning).persistent, true)
  assert.equal(current.find(command => command.id === defaultWarning).duration, null)
  off()
})

test('runtime updates remain addressable by ID after queue updates', () => {
  const currentScope = scope('runtime')
  const snapshots = []
  const off = kit.subscribeToasts(commands => snapshots.push(commands.filter(command => command.scope === currentScope)))
  const id = kit.toast({
    message: 'retry',
    scope: currentScope,
    action: { label: 'Retry', run: () => {} },
  })
  kit.toast({ kind: 'error', message: 'interrupt', scope: currentScope })
  kit.updateToastRuntime(id, { actionBusy: true })
  kit.toast({ kind: 'info', message: 'queue update', scope: currentScope })
  kit.updateToastRuntime(id, {
    actionBusy: false,
    actionError: 'Action failed. Try again.',
    persistent: true,
    duration: null,
  })

  const command = snapshots.at(-1).find(command => command.id === id)
  assert.equal(command.runtime.actionBusy, false)
  assert.equal(command.runtime.actionError, 'Action failed. Try again.')
  assert.equal(command.runtime.persistent, true)
  assert.equal(command.runtime.duration, null)
  off()
})

test('primary hosts own the queue while fallback hosts become active only after takeover', () => {
  const currentScope = scope('hosts')
  const fallbackEvents = []
  const primaryEvents = []
  const offFallback = kit.registerToastHost('fallback', (commands, active) => {
    fallbackEvents.push({ ids: commands.filter(command => command.scope === currentScope).map(command => command.id), active })
  })
  const offPrimary = kit.registerToastHost('primary', (commands, active) => {
    primaryEvents.push({ ids: commands.filter(command => command.scope === currentScope).map(command => command.id), active })
  })
  const id = kit.toast({ message: 'owned by shell', scope: currentScope })

  assert.deepEqual(primaryEvents.at(-1), { ids: [id], active: true })
  assert.deepEqual(fallbackEvents.at(-1), { ids: [], active: false })

  offPrimary()
  assert.deepEqual(fallbackEvents.at(-1), { ids: [id], active: true })
  offFallback()
})

test('dedupe replacement, scoped dismiss, and clear invoke observers exactly once', () => {
  const currentScope = scope('lifecycle')
  const otherScope = `toast-behavior-other-${Math.random().toString(36).slice(2)}`
  let replaced = 0
  let cleared = 0
  let dismissed = 0
  const first = kit.toast({
    message: 'first',
    scope: currentScope,
    dedupeKey: 'same',
    source: 'source-a',
    onDismiss: reason => { if (reason === 'replaced') replaced += 1 },
  })
  const replacement = kit.toast({
    message: 'replacement',
    scope: currentScope,
    dedupeKey: 'same',
    source: 'source-b',
  })
  const scoped = kit.toast({
    message: 'scoped',
    scope: currentScope,
    onDismiss: reason => { if (reason === 'clear') cleared += 1 },
  })
  const other = kit.toast({
    message: 'other scope',
    scope: otherScope,
    onDismiss: reason => { if (reason === 'dismiss') dismissed += 1 },
  })

  assert.notEqual(first, replacement)
  assert.equal(replaced, 1)
  kit.dismissToast(other, currentScope)
  assert.equal(dismissed, 0)
  kit.clearToasts(currentScope)
  kit.clearToasts(currentScope)
  assert.equal(replaced, 1)
  assert.equal(cleared, 1)
  assert.equal(dismissed, 0)
  kit.dismissToast(other)
  kit.dismissToast(other)
  assert.equal(dismissed, 1)
})

test('useToast applies source/scope defaults, clears its default scope, and leaves no-scope clear inert', () => {
  const currentScope = scope('service')
  const otherScope = `toast-behavior-other-service-${Math.random().toString(36).slice(2)}`
  const service = kit.useToast({ scope: currentScope, source: 'test-service' })
  const ownID = service.toast({ message: 'own' })
  const otherID = kit.toast({ message: 'other', scope: otherScope })

  let latest = []
  const off = service.subscribeToasts(commands => {
    latest = commands.filter(command => command.scope === currentScope)
  })
  assert.equal(latest[0].id, ownID)
  service.dismissToast(otherID)
  assert.equal(latest[0].id, ownID)
  service.clearToasts()
  assert.equal(latest.length, 0)

  const unscopedService = kit.useToast()
  const unscopedID = kit.toast({ message: 'unscoped', scope: otherScope })
  unscopedService.clearToasts()
  const afterNoScopeClear = []
  const offNoScope = unscopedService.subscribeToasts(commands => {
    afterNoScopeClear.splice(0, afterNoScopeClear.length, ...commands.filter(command => command.scope === otherScope))
  })
  assert.deepEqual(afterNoScopeClear.map(command => command.id), [otherID, unscopedID])
  kit.clearToasts(otherScope)
  offNoScope()
  off()
})
