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
  assert.match(html, /Set up App Studio/)
  assert.match(html, /Git keeps every project durable/)
  assert.match(html, /Connect GitHub/)
  assert.doesNotMatch(html, /What are we building|Describe what you want to build|<textarea/)
})

test('advances from Git to model setup using real readiness', async () => {
  const html = await render({ readiness: { gitConnection: { ready: true, status: 'ready', connectionRef: 'github-workspace' } } })
  assert.match(html, /GitHub connected/)
  assert.match(html, /Connect an AI model/)
  assert.match(html, /tests the provider connection before saving/)
})

test('surfaces terminal Git validation failures with a recovery action', async () => {
  const html = await render({
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
  assert.match(html, /Check failed/)
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
