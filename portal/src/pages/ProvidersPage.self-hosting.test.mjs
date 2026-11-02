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

const source = await readFile(new URL('./ProvidersPage.vue', import.meta.url), 'utf8')

test('self-hosting Edges prerequisite uses catalog-owned availability states', () => {
  assert.match(source, /const edgesProvider = computed\(\(\) => providers\.byName\('edges'\)\)/)
  assert.match(source, /if \(!edges\) return 'absent'/)
  assert.match(source, /return edges\.ready && !!edges\.serving\?\.ui \? 'ready' : 'unready'/)
})

test('only a ready Edges portal receives the provider deep link', () => {
  assert.match(
    source,
    /<router-link\s+v-else-if="edgesSelfHostState === 'ready'"\s+:to="scopePath\('\/providers\/edges'\)"/,
  )
  assert.match(source, /v-else-if="edgesSelfHostState === 'unready'"/)
  assert.match(source, /v-else-if="edgesSelfHostState === 'absent'"/)
  assert.match(source, /View Edges in catalog/)
  assert.match(source, /Edges is not installed in this catalog\./)
})

test('a failed install-target check is visible and retryable', () => {
  assert.match(source, /v-if="orgProviders\.installTargetsError"/)
  assert.match(source, /@click="orgProviders\.loadInstallTargets"/)
  assert.match(source, /Retry check/)
})

test('unready Edges returns to a filtered catalog instead of navigating to a dead route', () => {
  assert.match(
    source,
    /function showEdgesCatalogEntry\(\) \{\s+tab\.value = 'catalog'\s+selectedCategory\.value = null\s+search\.value = 'edges'\s+\}/,
  )
  assert.match(source, /@click="showEdgesCatalogEntry"/)
})

test('one-time credentials are cleared and pending self-host actions fenced on principal or admin-role changes', () => {
  assert.match(source, /useAuthStore\(\)/)
  assert.match(source, /auth\.user\?\.userId \|\| auth\.user\?\.email/)
  assert.match(source, /canManageProviders\.value/)
  assert.match(source, /activeRegistration\.value = null/)
  assert.match(source, /selfHostRequestGeneration \+= 1/)
  assert.match(source, /if \(requestGeneration === selfHostRequestGeneration\) \{\s+selfHostBusy\.value = \{ \.\.\.selfHostBusy\.value, \[p\.name\]: false \}/)
})

test('hosted enrollment status fails closed without mixing it with Edge eligibility', () => {
  assert.match(source, /const enrollmentAvailable = computed\(\(\) => orgProviders\.enrollment\?\.available === true\)/)
  assert.match(source, /canManageProviders\.value && enrollmentAvailable\.value && orgProviders\.installTargetsEligible/)
  assert.match(source, /v-if="orgProviders\.supported && !enrollmentAvailable && \(tab === 'self-hosting' \|\| orgProviders\.items\.length > 0\)"[^>]*role="status"/)
  assert.match(source, /Hosted provider enrollment availability could not be verified by this hub\./)
  assert.match(source, /Existing providers remain listed; Disable and Remove stay available\./)
  assert.match(source, /if \(p\.scope === 'org' && !enrollmentAvailable\.value\) return/)
  assert.match(source, /if \(p\.scope === 'org' && !enrollmentAvailable\.value\) return Promise\.reject\(new Error\(enrollmentMessage\.value\)\)/)
  assert.match(source, /dialogProvider\.value\?\.scope === 'org'/)
})

test('closed enrollment disables creation, credential refetch, Upgrade and Enable while preserving cleanup', () => {
  assert.match(source, /<router-link v-if="canManageProviders && enrollmentAvailable"/)
  assert.match(source, /:disabled="!!selfHostBusy\[p\.name\] \|\| !enrollmentAvailable"/)
  assert.match(source, /:disabled="!!busy\[p\.name\] \|\| providers\.hasMissingDependencies\(p\) \|\| \(p\.scope === 'org' && !enrollmentAvailable\)"/)
  assert.match(source, /@click="requestEnableDialog\(p\)"/)
  assert.match(source, /@click="removeSelfHosted\(p\.name\)"/)
  assert.match(source, /@click="onDisable\(p\)"/)
})
