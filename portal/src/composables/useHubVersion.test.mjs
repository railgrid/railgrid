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
import fs from 'node:fs'
import path from 'node:path'
import test from 'node:test'
import * as ts from 'typescript'

const dir = path.dirname(new URL(import.meta.url).pathname)
const source = fs.readFileSync(path.join(dir, 'useHubVersion.ts'), 'utf8')
const helpers = source.match(/export function hubVersionLabel[\s\S]*?\n}\n[\s\S]*?export function hubVersionDetails[\s\S]*?\n}\n/)?.[0]
assert.ok(helpers, 'version label helpers should stay directly testable')
const mod = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(ts.transpileModule(helpers, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2020 },
}).outputText)}`)

const adminPage = fs.readFileSync(path.join(dir, '..', 'pages', 'BonkersPage.vue'), 'utf8')

test('platform version label matches the provider version style', () => {
  assert.equal(mod.hubVersionLabel(null), '')
  assert.equal(mod.hubVersionLabel({ version: '', gitCommit: '', buildDate: '' }), '')
  assert.equal(mod.hubVersionLabel({ version: '0.4.1', gitCommit: '', buildDate: '' }), 'v0.4.1')
  assert.equal(mod.hubVersionLabel({ version: 'v0.4.1', gitCommit: '', buildDate: '' }), 'v0.4.1')
  assert.equal(mod.hubVersionLabel({ version: 'dev', gitCommit: '', buildDate: '' }), 'dev')
  assert.equal(mod.hubVersionLabel({ version: '0fbcba7', gitCommit: '', buildDate: '' }), '0fbcba7')
})

test('platform version tooltip carries commit and build date when known', () => {
  assert.equal(
    mod.hubVersionDetails({ version: 'v0.4.1', gitCommit: 'abc1234', buildDate: '2026-09-16T10:00:00Z' }),
    'Platform v0.4.1 · commit abc1234 · built 2026-09-16T10:00:00Z',
  )
  assert.equal(mod.hubVersionDetails({ version: 'dev', gitCommit: 'unknown', buildDate: 'unknown' }), 'Platform dev')
})

test('platform admin sidebar shows the hub version', () => {
  assert.match(adminPage, /useHubVersion\(\)/)
  assert.match(adminPage, /data-testid="platform-version"/)
  assert.match(adminPage, /:title="platformVersionDetails"/)
})
