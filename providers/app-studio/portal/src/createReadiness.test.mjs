import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./createReadiness.ts', import.meta.url), 'utf8')
const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const gitReviewSource = await readFile(new URL('./ProjectGitReview.vue', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const {
  canSubmitCreatePrompt,
  createSetupItems,
  createPromptBlockedMessage,
  reconcileGitReview,
  waitForCurrentReadiness,
} = await import(moduleURL)

test('submission waits for a newer readiness request when its own request is superseded', async () => {
  let resolveFirst, resolveSecond
  const first = new Promise(resolve => { resolveFirst = resolve })
  const second = new Promise(resolve => { resolveSecond = resolve })
  let current = first
  let completed = false
  const waiting = (async () => {
    await first
    await waitForCurrentReadiness(() => current)
    completed = true
  })()
  current = second
  resolveFirst()
  await Promise.resolve()
  await Promise.resolve()
  assert.equal(completed, false)
  current = null
  resolveSecond()
  await waiting
  assert.equal(completed, true)
  assert.match(appSource, /await loadCreateReadiness\(\)\s+await waitForCurrentReadiness\(\(\) => currentCreateReadinessLoad\)/)
  assert.match(appSource, /await waitForCurrentReadiness\(\(\) => currentCreateReadinessLoad\)\s+if \(!isCurrent\(\)\) return false[\s\S]*?createGitError\.value =/)
})

test('rechecking an unchecked Git choice clears a stale error only after a ready connection', () => {
  const ready = { gitConnection: { ready: true, connectionRef: 'replacement' } }
  assert.deepEqual(reconcileGitReview(false, 'old', 'unavailable', ready), { reviewed: 'replacement', error: '' })
  assert.deepEqual(reconcileGitReview(false, 'old', 'unavailable', null), { reviewed: '', error: 'unavailable' })
  assert.deepEqual(reconcileGitReview(true, 'old', 'unavailable', ready), { reviewed: 'old', error: 'unavailable' })
  assert.deepEqual(reconcileGitReview(true, 'replacement', 'unavailable', ready), { reviewed: 'replacement', error: '' })
  assert.match(appSource, /const review = reconcileGitReview\(createWithGit\.value, reviewedGitConnection\.value, createGitError\.value, createReadiness\.value\)/)
})

test('Git health never blocks a nonempty project prompt or model-only readiness', () => {
  for (const status of ['ready', 'provider-missing', 'connection-missing', 'validating', 'failed']) {
    const readiness = { gitConnection: { ready: status === 'ready', status } }
    assert.equal(canSubmitCreatePrompt('build a dashboard', readiness), true)
    assert.equal(canSubmitCreatePrompt('  ', readiness), false)
    assert.equal(createPromptBlockedMessage(readiness), '')
    assert.deepEqual(createSetupItems({ readiness, llmConfigured: true, checkingGit: true }), [])
    assert.deepEqual(createSetupItems({ readiness, llmConfigured: false, checkingGit: false }), [{
      id: 'llm', label: 'LLM credentials', status: 'missing', actionLabel: 'Set up LLM', action: 'setup-llm',
    }])
  }
  assert.equal(canSubmitCreatePrompt('build a dashboard', null), true)
})

test('new-project route has one setup surface and a stable wizard entry label', () => {
  assert.equal(appSource.includes('workspaceSetupLabel'), false)
  assert.equal(appSource.includes('createPromptSubmitLabel'), false)
  assert.equal(appSource.includes('to create a durable project.'), false)
  assert.match(appSource, /<Tabs[\s\S]*aria-label="App Studio sections"/)
  assert.match(appSource, /function openSettings\(\)[\s\S]*openModelsSection\(\)/)
  assert.equal(appSource.includes('error.value = gitConnectionCreateReady.value ? null : createReadinessError.value || createPromptBlockedMessage(createReadiness.value)'), false)
  assert.match(appSource, /if \(llmConfigured\.value\) return true\s+error\.value = null\s+return false/)
  assert.match(gitReviewSource, />\s*Continue without Git\s*</)
})

test('new-project stream uses the reviewed template decision', () => {
  const stream = appSource.match(/api\.createProjectStream\(creationContext,\s*\{[\s\S]*?\}, \(message\) =>/)?.[0] ?? ''
  assert.match(stream, /templateName: createOverrides\?\.templateName/)
  assert.doesNotMatch(stream, /inferDevelopmentTemplate/)
})

test('optional Code reads cannot put the entire App Studio into initialization', () => {
  for (const name of ['loadCreateReadiness', 'loadImportRepositories']) {
    const start = appSource.indexOf(`async function ${name}()`)
    const end = appSource.indexOf('\nasync function ', start + 1)
    assert.doesNotMatch(appSource.slice(start, end), /handleProjectAPIInitializing/)
  }
})

test('workspace revalidation keeps the reviewed wizard mounted', () => {
  assert.match(appSource, /<div v-if="wizardOpen" v-show="[^"]+">\s*<NewProjectWizard/)
})
