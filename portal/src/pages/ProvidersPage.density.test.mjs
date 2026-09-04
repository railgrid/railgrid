// Copyright 2026 The Faros Authors.
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

const source = await readFile(new URL('./ProvidersPage.vue', import.meta.url), 'utf8')

test('provider cards use bounded leading-edge tracks on wide screens', () => {
  assert.equal(
    (source.match(/grid-cols-\[repeat\(auto-fill,minmax\(240px,320px\)\)\] justify-start/g) ?? []).length,
    2,
  )
  assert.doesNotMatch(source, /sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5/)
})

test('catalog search and category filters remain in one wrapping control rail', () => {
  assert.match(source, /class="mb-4 flex flex-wrap items-center gap-3"/)
  assert.match(source, /class="relative w-full sm:w-80 sm:max-w-full"/)
  assert.doesNotMatch(source, /sm:justify-between/)
})
