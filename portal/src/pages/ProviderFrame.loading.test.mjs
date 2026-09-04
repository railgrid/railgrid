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

const source = await readFile(new URL('./ProviderFrame.vue', import.meta.url), 'utf8')

test('provider bundle loading is delayed so cached navigation does not flash', () => {
  assert.match(source, /import \{ useDelayedLoading \} from '@\/portalkit\/useDelayedLoading'/)
  assert.match(source, /const providerLoadPending = computed\(\(\) => loadState\.value === 'loading'\)/)
  assert.match(source, /const showProviderLoading = useDelayedLoading\(providerLoadPending\)/)
  assert.match(source, /loadState === 'loading' && showProviderLoading/)
})

test('App Studio deep links receive a responsive three-region loading shell', () => {
  assert.match(source, /const isAppStudioWorkspaceRoute = computed/)
  assert.match(source, /aria-label="Loading App Studio workspace"/)
  assert.match(source, /md:grid-cols-\[minmax\(12rem,18rem\)_minmax\(0,2fr\)_minmax\(16rem,28rem\)\]/)
  assert.equal((source.match(/class="hidden content-start gap-3[^\n]+md:grid"/g) ?? []).length, 2)
  assert.match(source, /role="status"[\s\S]*aria-live="polite"[\s\S]*aria-busy="true"/)
})

test('readiness labels distinguish degraded and disconnected providers', () => {
  assert.match(source, /case 'BackendUnhealthy': return 'Degraded'/)
  assert.match(source, /case 'HeartbeatStale': return 'Disconnected'/)
  assert.match(source, /entry\.readinessMessage \|\| `Waiting for \$\{entry\.name\} to report ready\.`/)
})
