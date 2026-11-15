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
import test from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('./adminNavigationFocus.ts', import.meta.url), 'utf8')
const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext } }).outputText
const { rememberAdminNavigation, consumeAdminNavigation } = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(code)}`)
const event = overrides => ({ button: 0, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, ...overrides })

test('only a normal section transition can claim focus, once, at its destination', () => {
  for (const change of [{ button: 1 }, { metaKey: true }, { ctrlKey: true }, { shiftKey: true }, { altKey: true }]) {
    rememberAdminNavigation(event(change), '/bonkers/users', '/bonkers/providers')
    assert.equal(consumeAdminNavigation('/bonkers/users'), false)
  }
  rememberAdminNavigation(event({}), '/bonkers/providers', '/bonkers/providers')
  assert.equal(consumeAdminNavigation('/bonkers/providers'), false)
  rememberAdminNavigation(event({}), '/bonkers/users', '/bonkers/providers')
  assert.equal(consumeAdminNavigation('/bonkers/organizations'), false)
  assert.equal(consumeAdminNavigation('/bonkers/users'), false)
  rememberAdminNavigation(event({}), '/bonkers/users', '/bonkers/providers')
  assert.equal(consumeAdminNavigation('/bonkers/users'), true)
  assert.equal(consumeAdminNavigation('/bonkers/users'), false)
})
