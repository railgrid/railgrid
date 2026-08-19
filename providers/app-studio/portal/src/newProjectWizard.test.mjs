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

test('submitted landing idea renders one stable preparation surface without duplicate intake or duplicate spinners', async () => {
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
    assert.match(html, /aria-labelledby="new-project-details-title"/)
    assert.match(html, /Set up your project/)
    assert.match(html, /Your request/)
    assert.match(html, new RegExp(idea.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
    assert.match(html, /Preparing details…/)
    assert.equal((html.match(/Preparing details…/g) ?? []).length, 1)
    assert.doesNotMatch(html, /Review your project|Plan ready|Planning in progress/)
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

test('confirmation contract preserves the request and exposes editable name, template, and starter-code details', () => {
  assert.match(wizardSource, />Your request<\/div>[\s\S]*\{\{ prompt \}\}/)
  assert.match(wizardSource, /<span[^>]*>Project name<\/span>[\s\S]*v-model="displayName"/)
  assert.match(wizardSource, /<span[^>]*>Template<\/span>[\s\S]*v-model="chosenTemplate"/)
  assert.match(wizardSource, />Starter code<\/div>/)
  assert.match(wizardSource, /Includes starter code[\s\S]*working foundation/)
  assert.match(wizardSource, /Starts with an empty project[\s\S]*assistant to build from/)
  assert.match(wizardSource, /Create project/)
  assert.match(wizardSource, /aria-describedby="template-impact"/)
  assert.match(wizardSource, /class="flex min-h-\[470px\][^"]*"/)
  assert.doesNotMatch(wizardSource, /Review your project|Plan ready|Planning in progress/)
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
    assert.match(html, /Project details could not be prepared/)
    assert.match(html, /planner is temporarily unavailable/)
    assert.match(html, /Try again/)
    assert.match(html, /Edit request/)
  } finally {
    api.planProject = originalPlanProject
  }

  assert.match(wizardSource, /error\.value = e instanceof Error \? e\.message : 'Could not plan the project\. Try again\.'/)
  assert.match(wizardSource, /if \(!hasInitialPrompt\.value\) step\.value = 'intake'/)
  assert.match(wizardSource, /<p v-if="error" role="alert"[\s\S]*\{\{ error \}\}/)
  assert.match(wizardSource, /v-else-if="error"[\s\S]*@click="runPlan"[\s\S]*Try again/)
  assert.match(wizardSource, /Preparing details…/)
  assert.equal((wizardSource.match(/<Loader2/g) ?? []).length, 1)
  assert.match(wizardSource, /function back\(\) \{[\s\S]*if \(hasInitialPrompt\.value\) \{[\s\S]*invalidatePlanRequest\(\)[\s\S]*planning\.value = false[\s\S]*emit\('cancel'\)/)
})

test('confirmed details emit the exact durable create payload and honor the disabled gate', () => {
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
  assert.match(appSource, /:class="wizardOpen \? 'items-start' : 'items-center'"/)

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
