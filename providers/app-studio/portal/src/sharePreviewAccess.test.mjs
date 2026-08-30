import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./sharePreviewAccess.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const { sharePreviewAccessDraftState } = await import(moduleURL)

test('keeps Preview dirty through the v-model echo and pending through async convergence', () => {
  assert.deepEqual(
    sharePreviewAccessDraftState('restricted', 'restricted', true, true),
    { dirty: false, pending: false },
  )

  // Selecting Public changes the parent-backed draft immediately, but the
  // saved mode remains Restricted until the POST response acknowledges it.
  assert.deepEqual(
    sharePreviewAccessDraftState('public', 'restricted', true, true),
    { dirty: true, pending: false },
  )

  // The response acknowledges the desired mode before the reconciler applies
  // it, so the edit is saved while the live URL remains explicitly pending.
  assert.deepEqual(
    sharePreviewAccessDraftState('public', 'public', true, false),
    { dirty: false, pending: true },
  )

  assert.deepEqual(
    sharePreviewAccessDraftState('public', 'public', true, true),
    { dirty: false, pending: false },
  )
})

test('does not expose draft or pending state when Preview access is unsupported', () => {
  assert.deepEqual(
    sharePreviewAccessDraftState('public', 'restricted', false, false),
    { dirty: false, pending: false },
  )
})
