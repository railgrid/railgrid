import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./assistantThreadPinState.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
})
const pins = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)

function memoryStorage() {
  const values = new Map()
  return {
    values,
    getItem(key) { return values.get(key) ?? null },
    setItem(key, value) { values.set(key, value) },
  }
}

test('pins are project scoped, ordered by latest pin, and toggle off', () => {
  const storage = memoryStorage()
  let state = pins.toggleAssistantThreadPin('scope-a', 'one', [], storage)
  state = pins.toggleAssistantThreadPin('scope-a', 'two', state, storage)
  assert.deepEqual(state, ['two', 'one'])
  assert.deepEqual(pins.readAssistantThreadPins('scope-a', ['one', 'two'], storage), ['two', 'one'])
  assert.deepEqual(pins.readAssistantThreadPins('scope-b', ['one', 'two'], storage), [])
  assert.deepEqual(pins.toggleAssistantThreadPin('scope-a', 'two', state, storage), ['one'])
})

test('missing threads remain preserved while stored pins are sanitized', () => {
  const storage = memoryStorage()
  pins.toggleAssistantThreadPin('scope', 'one', [], storage)
  assert.deepEqual(pins.readAssistantThreadPins('scope', ['two'], storage), ['one'])

  storage.setItem(pins.assistantThreadPinStorageKey('scope'), JSON.stringify([
    ' one ',
    'one',
    42,
    '',
    null,
  ]))
  assert.deepEqual(pins.readAssistantThreadPins('scope', ['two'], storage), ['one'])
  assert.deepEqual(
    JSON.parse(storage.values.get(pins.assistantThreadPinStorageKey('scope'))),
    ['one'],
  )

  pins.removeAssistantThreadPin('scope', 'one', storage)
  assert.deepEqual(pins.readAssistantThreadPins('scope', ['two'], storage), [])
})

test('storage failures remain nonblocking', () => {
  const broken = {
    getItem() { throw new Error('blocked') },
    setItem() { throw new Error('full') },
  }
  assert.deepEqual(pins.readAssistantThreadPins('scope', ['one'], broken), [])
  assert.deepEqual(pins.toggleAssistantThreadPin('scope', 'one', [], broken), ['one'])
})
