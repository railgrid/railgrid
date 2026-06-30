import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./createReadiness.ts', import.meta.url), 'utf8')
const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
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
} = await import(moduleURL)

test('blocks project creation when no validated Git connection is ready', () => {
  const readiness = {
    gitConnection: {
      ready: false,
      message: 'You need to connect to a Git account before you can continue',
    },
  }

  assert.equal(canSubmitCreatePrompt('build a dashboard', readiness), false)
  assert.equal(
    createPromptBlockedMessage(readiness),
    'You need to connect to a Git account before you can continue',
  )
})

test('allows the a-ha prompt only after Git durability is ready', () => {
  const readiness = {
    gitConnection: {
      ready: true,
      connectionRef: 'github',
    },
  }

  assert.equal(canSubmitCreatePrompt('build a dashboard', readiness), true)
  assert.equal(createPromptBlockedMessage(readiness), '')
})

test('still requires the user to type a prompt before submitting', () => {
  const readiness = {
    gitConnection: {
      ready: true,
      connectionRef: 'github',
    },
  }

  assert.equal(canSubmitCreatePrompt('   ', readiness), false)
})

test('summarizes Git and LLM setup as one checklist', () => {
  const items = createSetupItems({
    readiness: {
      gitConnection: {
        ready: false,
        message: 'You need to connect to a Git account before you can continue',
      },
    },
    llmConfigured: false,
    checkingGit: false,
  })

  assert.deepEqual(items, [
    {
      id: 'git',
      label: 'Git connection',
      status: 'missing',
      actionLabel: 'Connect Git',
      action: 'connect-git',
    },
    {
      id: 'llm',
      label: 'LLM credentials',
      status: 'missing',
      actionLabel: 'Set up LLM',
      action: 'setup-llm',
    },
  ])
})

test('collapses the setup checklist once all prerequisites are ready', () => {
  const items = createSetupItems({
    readiness: {
      gitConnection: {
        ready: true,
        connectionRef: 'github',
      },
    },
    llmConfigured: true,
    checkingGit: false,
  })

  assert.deepEqual(items, [])
})

test('keeps ready setup rows visible while another prerequisite is missing', () => {
  const items = createSetupItems({
    readiness: {
      gitConnection: {
        ready: true,
        connectionRef: 'github',
      },
    },
    llmConfigured: false,
    checkingGit: false,
  })

  assert.deepEqual(items, [
    {
      id: 'git',
      label: 'Git connection',
      status: 'ready',
    },
    {
      id: 'llm',
      label: 'LLM credentials',
      status: 'missing',
      actionLabel: 'Set up LLM',
      action: 'setup-llm',
    },
  ])
})

test('new-project route has one setup surface and a stable create button label', () => {
  assert.equal(appSource.includes('workspaceSetupLabel'), false)
  assert.equal(appSource.includes('createPromptSubmitLabel'), false)
  assert.equal(appSource.includes('to create a durable project.'), false)
  assert.match(appSource, /<button\s+v-if="!showNewProjectComposer"[\s\S]*title="LLM settings"/)
  assert.equal(appSource.includes('error.value = gitConnectionCreateReady.value ? null : createReadinessError.value || createPromptBlockedMessage(createReadiness.value)'), false)
  assert.match(appSource, /if \(gitConnectionCreateReady\.value && llmConfigured\.value\) return true\s+error\.value = null\s+return false/)
  assert.match(appSource, />\s*Create and send\s*</)
})
