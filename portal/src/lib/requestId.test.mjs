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
import path from 'node:path'
import test from 'node:test'
import ts from 'typescript'

const source = await readFile(path.join(path.resolve(new URL('.', import.meta.url).pathname), 'requestId.ts'), 'utf8')
const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } })
const mod = { exports: {} }
new Function('module', 'exports', outputText)(mod, mod.exports)
const { requestIdV7, requestIdMintedAt, reusableRequestId, REQUEST_ID_REUSE_MS } = mod.exports

test('request IDs are version 7 UUIDs carrying their mint time', () => {
  const now = Date.UTC(2026, 10, 11, 12)
  const id = requestIdV7(now, new Uint8Array(16).fill(0xff))
  assert.match(id, /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
  assert.equal(requestIdMintedAt(id), now)
  assert.equal(requestIdMintedAt('df093ac1-ea4b-4386-a7ce-9417b184855b'), undefined)
})

test('a draft reuses its key only while it is recent', () => {
  const now = Date.UTC(2026, 10, 11, 12)
  const fresh = requestIdV7(now - 1000)
  assert.equal(reusableRequestId(fresh, now), fresh)
  assert.equal(reusableRequestId(requestIdV7(now - REQUEST_ID_REUSE_MS - 1), now), undefined)
  assert.equal(reusableRequestId('df093ac1-ea4b-4386-a7ce-9417b184855b', now), undefined)
  assert.equal(reusableRequestId(undefined, now), undefined)
})
