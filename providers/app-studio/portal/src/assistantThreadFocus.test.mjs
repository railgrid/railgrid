import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./assistantThreadFocus.ts', import.meta.url), 'utf8')
const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const {
  assistantThreadFocusStorageKey,
  persistAssistantThreadFocus,
  readAssistantThreadFocus,
  restoreAssistantThreadFocus,
} = await import(moduleURL)

function memoryStorage(initial = {}) {
  const values = new Map(Object.entries(initial))
  return {
    values,
    getItem(key) { return values.has(key) ? values.get(key) : null },
    setItem(key, value) { values.set(key, value) },
    removeItem(key) { values.delete(key) },
  }
}

const scope = (project, extra = {}) => ({
  tenant: 'tenant-a',
  orgUUID: 'org-a',
  workspaceUUID: 'workspace-a',
  userSub: 'user-a',
  project,
  ...extra,
})

test('isolates saved thread focus by tenant, user, and project', () => {
  const storage = memoryStorage()
  persistAssistantThreadFocus(scope('project-a'), 'thread-a', storage)
  persistAssistantThreadFocus(scope('project-b'), 'thread-b', storage)
  persistAssistantThreadFocus(scope('project-a', { tenant: 'tenant-b' }), 'thread-c', storage)

  assert.equal(readAssistantThreadFocus(scope('project-a'), storage), 'thread-a')
  assert.equal(readAssistantThreadFocus(scope('project-b'), storage), 'thread-b')
  assert.equal(readAssistantThreadFocus(scope('project-a', { tenant: 'tenant-b' }), storage), 'thread-c')
  assert.notEqual(assistantThreadFocusStorageKey(scope('project-a')), assistantThreadFocusStorageKey(scope('project-b')))
  assert.notEqual(assistantThreadFocusStorageKey(scope('project-a')), assistantThreadFocusStorageKey(scope('project-a', { workspaceUUID: 'workspace-b' })))
  assert.notEqual(
    assistantThreadFocusStorageKey(scope('project-a', { userSub: `${'a'.repeat(300)}-one` })),
    assistantThreadFocusStorageKey(scope('project-a', { userSub: `${'a'.repeat(300)}-two` })),
  )
})

test('restores a saved thread when it is still present in the server list', () => {
  const storage = memoryStorage()
  const currentScope = scope('project-a')
  persistAssistantThreadFocus(currentScope, 'thread-b', storage)

  assert.equal(
    restoreAssistantThreadFocus(currentScope, [{ id: 'thread-a' }, { id: 'thread-b' }], storage),
    'thread-b',
  )
  assert.equal(readAssistantThreadFocus(currentScope, storage), 'thread-b')
})

test('falls back from a stale saved thread and reconciles storage', () => {
  const storage = memoryStorage()
  const currentScope = scope('project-a')
  persistAssistantThreadFocus(currentScope, 'deleted-thread', storage)

  assert.equal(restoreAssistantThreadFocus(currentScope, [{ id: 'newest' }, { id: 'older' }], storage), 'newest')
  assert.equal(readAssistantThreadFocus(currentScope, storage), 'newest')
  assert.equal(restoreAssistantThreadFocus(currentScope, [], storage), '')
  assert.equal(readAssistantThreadFocus(currentScope, storage), '')
})

test('ignores malformed values and storage failures without throwing', () => {
  const currentScope = scope('project-a')
  const malformed = memoryStorage()
  malformed.setItem(assistantThreadFocusStorageKey(currentScope), '{not-json')
  assert.equal(readAssistantThreadFocus(currentScope, malformed), '')
  assert.doesNotThrow(() => restoreAssistantThreadFocus(currentScope, [{ id: 'thread-a' }], malformed))

  const brokenStorage = {
    getItem() { throw new Error('blocked') },
    setItem() { throw new Error('full') },
    removeItem() { throw new Error('blocked') },
  }
  assert.equal(readAssistantThreadFocus(currentScope, brokenStorage), '')
  assert.doesNotThrow(() => persistAssistantThreadFocus(currentScope, 'thread-a', brokenStorage))
  assert.doesNotThrow(() => restoreAssistantThreadFocus(currentScope, [{ id: 'thread-a' }], brokenStorage))
})

test('App restores, persists, and guards thread focus through project/thread transitions', () => {
  assert.match(appSource, /from ['"]\.\/assistantThreadFocus['"]/) 
  assert.match(appSource, /userId \|\| props\.ctx\?\.user\?\.sub \|\| props\.ctx\?\.user\?\.email/)
  assert.match(appSource, /restoreAssistantThreadFocus\(assistantThreadFocusScope\(name\), threads\)/)
  assert.match(appSource, /persistAssistantThreadFocus\(assistantThreadFocusScope\(projectName\), threadID\)/)
  assert.ok((appSource.match(/persistAssistantThreadFocus\(assistantThreadFocusScope\(projectName\), thread\.id\)/g) ?? []).length >= 3)
  assert.match(appSource, /const assistantThreadLoadSerial = beginAssistantThreadRequest\(\)/)
  assert.match(appSource, /!projectRequestIsCurrent\(requestGuard, name\)/)
  assert.match(appSource, /assistantThreadLoadSerial !== assistantThreadRequestSerial/)
})
