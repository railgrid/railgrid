import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

const vite = await createServer({ appType: 'custom', cacheDir: '/tmp/faros-vite-first-time-setup', configFile: false, plugins: [vue()], server: { middlewareMode: true, hmr: false } })
const { default: FirstTimeSetup } = await vite.ssrLoadModule('/src/FirstTimeSetup.vue')
test.after(async () => vite.close())

const base = {
  readiness: { gitConnection: { ready: false, status: 'connection-missing' } },
  llmConfigured: false,
  llmModel: '',
  loading: false,
  gitError: '',
  llmError: '',
  completion: false,
  codeConnectionsUrl: '/ui/providers/code/connections',
  codeCatalogUrl: '/providers',
}
const render = (props = {}) => renderToString(createSSRApp(FirstTimeSetup, { ...base, ...props }))

test('keeps first-time setup separate from the project prompt', async () => {
  const html = await render()
  assert.match(html, /aria-label="App Studio workspace setup"/)
  assert.doesNotMatch(html, /aria-labelledby="app-studio-setup-title"/)
  assert.doesNotMatch(html, /Connect an AI model/)
  assert.match(html, /aria-current="step"/)
  assert.match(html, /Skip for now/)
  assert.match(html, /Git backs up your source and tracks changes/)
  assert.doesNotMatch(html, /What are we building|Describe what you want to build|<textarea/)
})

test('requires the model even when Git is ready', async () => {
  const html = await render({ readiness: { gitConnection: { ready: true, status: 'ready', connectionRef: 'github-workspace' } } })
  assert.match(html, /Connected/)
  assert.doesNotMatch(html, /Skip for now/)
  assert.match(html, /Connect an AI model/)
  assert.match(html, /tested before saving/)
})

test('surfaces terminal Git validation failures with a recovery action', async () => {
  const html = await render({
    llmConfigured: true,
    readiness: {
      gitConnection: {
        ready: false,
        status: 'failed',
        connectionRef: 'github-workspace',
        message: 'The git host rejected the credential.',
      },
    },
  })
  assert.match(html, /The git host rejected the credential\./)
  assert.match(html, /Fix Git connection/)
  assert.match(html, /role="alert"/)
})

test('completion hands off to normal project creation', async () => {
  const html = await render({
    readiness: { gitConnection: { ready: true, status: 'ready', connectionRef: 'github-workspace' } },
    llmConfigured: true,
    llmModel: 'gpt-5.4',
    completion: true,
  })
  assert.match(html, /App Studio is ready/)
  assert.match(html, /Create your first project/)
  assert.match(html, /gpt-5\.4/)
})

test('App gates the new-project composer behind setup', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(app, /<template v-if="firstTimeSetupVisible">[\s\S]*<FirstTimeSetup[\s\S]*<template v-else-if="wizardOpen">/)
  assert.match(app, /@connect-model="openSettings"/)
  assert.match(app, /@finish="finishFirstTimeSetup"/)
})

test('optional Git step remains skippable while Git is missing, checking, or failed', async () => {
  for (const status of ['provider-missing', 'connection-missing', 'validating', 'failed']) {
    const html = await render({ llmConfigured: true, readiness: { gitConnection: { ready: false, status } }, gitLoading: status === 'validating' })
    assert.match(html, /Skip for now/)
    assert.match(html, /Connect Git/)
    assert.doesNotMatch(html, /GitHub connected/)
  }
})
test('completion without Git does not claim Git was connected', async () => {
  const html = await render({ llmConfigured: true, completion: true, gitSkipped: true })
  assert.match(html, /App Studio is ready/)
  assert.doesNotMatch(html, /Git and an AI model are connected|GitHub<\/dt>/)
})

test('Git skip storage is scoped to a signed-in user and workspace', async () => {
  const { gitOnboardingStorageKey } = await vite.ssrLoadModule('/src/useGitOnboarding.ts')
  const context = { orgUUID: 'org-a', workspaceUUID: 'ws-a', user: { sub: 'alice' } }
  const key = gitOnboardingStorageKey(context)
  assert.ok(key)
  assert.notEqual(key, gitOnboardingStorageKey({ ...context, workspaceUUID: 'ws-b' }))
  assert.notEqual(key, gitOnboardingStorageKey({ ...context, user: { sub: 'bob' } }))
  assert.equal(gitOnboardingStorageKey({ ...context, user: null }), null)
})

test('skipping Git before model setup keeps the required model action and acknowledges the choice', async () => {
  const html = await render({ gitSkipped: true })
  assert.match(html, /Connect AI model/)
  assert.match(html, /Skipped for now/)
  assert.doesNotMatch(html, /Skip for now|Create your first project/)
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(app, /:git-skipped="gitSetupSkipped"/)
  assert.match(app, /@skip-git="skipGitSetup"/)
})

test('Git choice precedes model setup even while model settings load', async () => {
  const html = await render({ loading: true })
  assert.match(html, /Skip for now/)
  assert.doesNotMatch(html, /Checking AI model setup|Connect AI model/)
  const model = await render({ gitSkipped: true })
  assert.match(model, /Connect AI model/)
  assert.match(model, /Required/)
  assert.doesNotMatch(model, /Git backs up your source and tracks changes/)
})

test('revisiting Git clears only this user and workspace skip choice', async () => {
  const { effectScope } = await import('vue')
  const { useGitOnboarding, gitOnboardingStorageKey } = await vite.ssrLoadModule('/src/useGitOnboarding.ts')
  const context = { orgUUID: 'org-a', workspaceUUID: 'ws-a', user: { sub: 'alice' } }
  const key = gitOnboardingStorageKey(context)
  const otherKey = gitOnboardingStorageKey({ ...context, workspaceUUID: 'ws-b' })
  const data = new Map([[key, '1'], [otherKey, '1']])
  const original = globalThis.localStorage
  globalThis.localStorage = { getItem: key => data.get(key), setItem: (key, value) => data.set(key, value), removeItem: key => data.delete(key) }
  const scope = effectScope()
  try {
    const onboarding = scope.run(() => useGitOnboarding(() => context))
    assert.equal(onboarding.skipped.value, true)
    onboarding.reset()
    assert.equal(onboarding.skipped.value, false)
    assert.equal(data.has(key), false)
    assert.equal(data.get(otherKey), '1')
    assert.equal(scope.run(() => useGitOnboarding(() => context)).skipped.value, false)
    onboarding.skip()
    assert.equal(data.get(key), '1')
  } finally {
    scope.stop()
    if (original === undefined) delete globalThis.localStorage
    else globalThis.localStorage = original
  }
  assert.match(await render({ gitSkipped: true }), /Back to Git/)
  assert.doesNotMatch(await render(), /Back to Git/)
})

test('explains development without Git and production prerequisites before skipping', async () => {
  for (const gitError of ['', 'Connection unavailable']) {
    const html = await render({ gitError })
    assert.match(html, /Development environments work without Git; publishing to production requires it/)
    assert.match(html, /Skip for now/)
  }
  assert.match(await render(), /skip for now and connect it later/)
})
