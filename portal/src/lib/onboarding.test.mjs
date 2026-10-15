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

const libDir = path.resolve(new URL('.', import.meta.url).pathname)

async function compileModule(name, dependencies = {}) {
  const source = await readFile(path.join(libDir, `${name}.ts`), 'utf8')
  const withoutImports = source.replace(/^import[\s\S]*?from ['"][^'"]+['"]\n/gm, '')
  const { outputText } = ts.transpileModule(withoutImports, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
  })
  const module = { exports: {} }
  new Function('module', 'exports', ...Object.keys(dependencies), outputText)(
    module,
    module.exports,
    ...Object.values(dependencies),
  )
  return module.exports
}

const copy = await compileModule('onboardingCopy')
const plan = await compileModule('onboardingPlan')
const state = await compileModule('onboardingState', { audienceCopy: copy.audienceCopy, defaultGoalForAudience: copy.defaultGoalForAudience, goalCopy: copy.goalCopy })

test('onboarding copy follows the audience defaults and provider purpose rules', () => {
  assert.equal(copy.defaultGoalForAudience('team'), 'edges')
  assert.equal(copy.defaultGoalForAudience('explore'), 'agents')
  assert.equal(copy.defaultGoalForAudience('personal'), 'app-studio')
  assert.equal(copy.goalTitleForAudience('agents', true), 'Run AI agents')
  assert.equal(copy.goalBodyForAudience('agents', false), 'Create an AI agent with tools and memory.')
  assert.equal(copy.purposeForProvider('app-studio', 'code', 'original description'), 'Manage source repositories for your apps.')
  assert.equal(copy.purposeForProvider('agents', 'custom-provider', 'Custom provider copy.'), 'Custom provider copy.')
})

test('onboarding planning is dependency-first and handles missing or cyclic catalog entries', () => {
  const items = [
    { name: 'app-studio', requires: [{ provider: 'code' }, { provider: 'infrastructure' }] },
    { name: 'code', requires: [{ provider: 'edges' }] },
    { name: 'edges', requires: [{ provider: 'code' }] },
  ]
  const result = plan.planFor('app-studio', name => items.find(item => item.name === name))
  assert.deepEqual(result.map(item => item.name), ['edges', 'code', 'infrastructure', 'app-studio'])
  assert.equal(result[2].provider, undefined)
  assert.deepEqual(plan.planFor('explore', () => undefined), [])
})

test('setup readiness requires current data, a launchable target, and ready enabled dependencies', () => {
  const entries = [
    { name: 'app-studio', provider: { ready: true } },
    { name: 'code', provider: { ready: true } },
  ]
  const enabled = name => name !== 'code' ? true : true
  assert.equal(plan.setupReadyFor(true, true, entries, enabled), true)
  assert.equal(plan.setupReadyFor(false, true, entries, enabled), false)
  assert.equal(plan.setupReadyFor(true, false, entries, enabled), false)
  assert.equal(plan.setupReadyFor(true, true, [{ name: 'code' }], enabled), false)
  assert.equal(plan.setupReadyFor(true, true, entries, name => name === 'app-studio'), false)
  entries[1].provider.ready = false
  assert.equal(plan.setupReadyFor(true, true, entries, enabled), false)
})

test('restore validates preferences and manual explore re-entry returns to the checked audience default', () => {
  const savedExplore = JSON.stringify({ audience: 'personal', goal: 'explore', step: 2 })
  assert.deepEqual(state.restoreOnboardingState(savedExplore), {
    step: 2, audience: 'personal', goal: 'explore', goalExplicit: false,
  })
  assert.deepEqual(state.restoreOnboardingState(savedExplore, true), {
    step: 1, audience: 'personal', goal: 'app-studio', goalExplicit: false,
  })
  assert.deepEqual(state.restoreOnboardingState(JSON.stringify({ audience: 'team', goal: 'agents', goalExplicit: true, step: 2 }), true), {
    step: 1, audience: 'team', goal: 'agents', goalExplicit: true,
  })
  assert.equal(state.restoreOnboardingState('{broken').step, 0)
  assert.equal(state.restoreOnboardingState(JSON.stringify({ audience: 'unknown', goal: 'agents', step: 2 })).step, 0)
  assert.equal(state.restoreOnboardingState(JSON.stringify({ audience: 'team', goal: 'explore', step: 1 })).step, 0)
})

test('onboarding persistence is scoped to a stable identity and role checks fail closed', () => {
  const preference = { step: 2, audience: 'personal', goal: 'agents', goalExplicit: true }
  assert.equal(state.persistOnboardingState(preference), JSON.stringify(preference))
  assert.equal(state.onboardingStorageKey('', 'org-a/workspace-a/workspace'), '')
  assert.equal(state.onboardingStorageKey('a b', 'org-a/workspace-a/workspace'), 'railgrid:portal:onboarding:user:a%20b:org-a/workspace-a/workspace')
  assert.equal(state.isConfirmedAdmin('admin', undefined), true)
  assert.equal(state.isConfirmedAdmin(undefined, 'admin'), true)
  assert.equal(state.isConfirmedAdmin('member', 'member'), false)
  assert.equal(state.isConfirmedAdmin(undefined, undefined), false)
})
