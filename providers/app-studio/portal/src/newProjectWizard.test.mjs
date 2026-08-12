import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/faros-vite-new-project-wizard',
  server: { middlewareMode: true, hmr: false },
})
const { default: NewProjectWizard } = await vite.ssrLoadModule('/src/NewProjectWizard.vue')
const { api } = await vite.ssrLoadModule('/src/api.ts')
const wizardSource = await readFile(new URL('./NewProjectWizard.vue', import.meta.url), 'utf8')
const appSource = await readFile(new URL('./App.vue', import.meta.url), 'utf8')

test.after(async () => vite.close())

function deferred() {
  let resolve
  let reject
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

test('submitted landing idea renders an explicit pending planning surface without duplicate intake', async () => {
  const request = deferred()
  const calls = []
  const originalPlanProject = api.planProject
  api.planProject = async (_ctx, body) => {
    calls.push(body)
    return request.promise
  }

  try {
    const idea = 'A shared pantry inventory with expiring-item alerts'
    const html = await renderToString(createSSRApp(NewProjectWizard, {
      ctx: null,
      initialPrompt: idea,
    }))

    assert.deepEqual(calls, [{ prompt: idea }])
    assert.match(html, /aria-labelledby="new-project-planning-title"/)
    assert.match(html, /Turning your idea into a project plan/)
    assert.match(html, /Submitted idea/)
    assert.match(html, new RegExp(idea.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
    assert.match(html, /Planning in progress/)
    assert.match(html, /No project has been created yet\./)
    assert.doesNotMatch(html, /new-project-intake-title/)
    assert.doesNotMatch(html, /<textarea/)
  } finally {
    api.planProject = originalPlanProject
    request.resolve({
      displayName: 'Pantry',
      repositoryName: 'pantry',
      availableTemplates: [],
    })
  }
})

test('review contract preserves the idea and exposes editable name/template and starter-code impact', () => {
  assert.match(wizardSource, /<div class="rounded-lg border border-border-subtle bg-surface p-4">\s*<div[^>]*>Submitted idea<\/div>/)
  assert.match(wizardSource, /<span[^>]*>Project name<\/span>[\s\S]*v-model="displayName"/)
  assert.match(wizardSource, /<span[^>]*>Template<\/span>[\s\S]*v-model="chosenTemplate"/)
  assert.match(wizardSource, /Starter-code impact/)
  assert.match(wizardSource, /Starter code will be attached[\s\S]*working placeholder/)
  assert.match(wizardSource, /No starter code will be attached[\s\S]*build from an empty project/)
  assert.match(wizardSource, /Create &amp; open thread/)
  assert.match(wizardSource, /aria-describedby="template-impact"/)
})

test('planning failures remain honest and retain retry/edit affordances for a submitted idea', async () => {
  const originalPlanProject = api.planProject
  api.planProject = () => {
    throw new Error('planner is temporarily unavailable')
  }
  try {
    const html = await renderToString(createSSRApp(NewProjectWizard, {
      ctx: null,
      initialPrompt: 'A team launch checklist with owners and due dates',
    }))
    assert.match(html, /Planning needs attention/)
    assert.match(html, /planner is temporarily unavailable/)
    assert.match(html, /Try again/)
    assert.match(html, /Edit prompt/)
  } finally {
    api.planProject = originalPlanProject
  }

  assert.match(wizardSource, /error\.value = e instanceof Error \? e\.message : 'Could not plan the project\. Try again\.'/)
  assert.match(wizardSource, /if \(!hasInitialPrompt\.value\) step\.value = 'intake'/)
  assert.match(wizardSource, /<p v-if="error" role="alert"[\s\S]*\{\{ error \}\}/)
  assert.match(wizardSource, /<button[\s\S]*v-if="error"[\s\S]*@click="runPlan"[\s\S]*Try again/)
  assert.match(wizardSource, /\{\{ hasInitialPrompt \? 'Edit prompt' : 'Back' \}\}/)
  assert.match(wizardSource, /The plan could not be loaded\. You can retry or edit the idea\./)
  assert.match(wizardSource, /function back\(\) \{[\s\S]*if \(hasInitialPrompt\.value\) \{[\s\S]*invalidatePlanRequest\(\)[\s\S]*planning\.value = false[\s\S]*emit\('cancel'\)/)
})

test('confirmed review emits the exact durable create payload and honors the disabled gate', () => {
  assert.match(wizardSource, /function confirmCreate\(\) \{[\s\S]*if \(props\.disabled\) return[\s\S]*emit\('create', \{[\s\S]*prompt: prompt\.value\.trim\(\),[\s\S]*templateName: chosenTemplate\.value \|\| undefined,[\s\S]*displayName: displayName\.value\.trim\(\) \|\| undefined,[\s\S]*\}\)[\s\S]*\}/)
  assert.match(wizardSource, /@click="confirmCreate"/)
  assert.match(wizardSource, /:disabled="disabled"/)
})

test('App replaces the landing composer with the wizard and wires cancel to restore the exact prompt focus', () => {
  const wizardBlock = appSource.match(/<template v-if="wizardOpen">[\s\S]*?<\/template>/)?.[0]
  assert.ok(wizardBlock, 'wizard must occupy the landing surface when open')
  assert.match(wizardBlock, /<NewProjectWizard/)
  assert.match(wizardBlock, /:initial-prompt="prompt"/)
  assert.match(wizardBlock, /@cancel="onWizardCancel"/)
  assert.match(appSource, /<template v-else>\s*<div[\s\S]*?<form[\s\S]*v-if="!wizardOpen"/)

  const cancelFunction = appSource.match(/async function onWizardCancel\(\) \{([\s\S]*?)\n\}/)?.[1]
  assert.ok(cancelFunction, 'cancel handler must remain explicit')
  assert.match(cancelFunction, /wizardOpen\.value = false/)
  assert.doesNotMatch(cancelFunction, /prompt\.value\s*=\s*['"]{2}/)
  assert.match(cancelFunction, /await nextTick\(\)/)
  assert.match(cancelFunction, /promptRef\.value\?\.focus\(\)/)
  assert.match(cancelFunction, /promptRef\.value\?\.setSelectionRange\(prompt\.value\.length, prompt\.value\.length\)/)
})

test('wizard handoff keeps the existing readiness and project/thread start path intact', () => {
  const handoff = appSource.match(/async function onWizardCreate\([\s\S]*?\n\}/)?.[0]
  assert.ok(handoff, 'wizard create handler must remain explicit')
  assert.match(handoff, /wizardOpen\.value = false/)
  assert.match(handoff, /prompt\.value = payload\.prompt/)
  assert.match(handoff, /await ensureCreateSetupReady\(\)/)
  assert.match(handoff, /createProjectAndStartConversation\(payload\.prompt, \{[\s\S]*templateName: payload\.templateName,[\s\S]*displayName: payload\.displayName/)

  const startPath = appSource.slice(appSource.indexOf('async function createProjectAndStartConversation('))
  assert.match(startPath, /api\.createProjectStream\(props\.ctx, \{/)
  assert.match(startPath, /api\.createAssistantThread\(props\.ctx, projectName\)/)
  assert.match(startPath, /api\.startAssistantTurn\(props\.ctx, projectName, thread\.id, \{/)
})
