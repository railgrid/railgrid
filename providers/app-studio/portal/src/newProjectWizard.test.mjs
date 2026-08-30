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
const styleSource = await readFile(new URL('./style.css', import.meta.url), 'utf8')

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
    assert.match(html, /Prepare your project/)
    assert.match(html, /Your request/)
    assert.match(html, new RegExp(idea.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
    assert.match(html, /Preparing project plan…/)
    assert.equal((html.match(/Preparing project plan…/g) ?? []).length, 1)
    assert.match(html, /Describe[\s\S]*Prepare[\s\S]*Confirm/)
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
  assert.match(wizardSource, />Starter source<\/div>/)
  assert.match(wizardSource, /Attaches starter source[\s\S]*when it is available/)
  assert.match(wizardSource, /Starts with an empty project[\s\S]*choose a development template later/)
  assert.match(wizardSource, /Selected development template[\s\S]*activeTemplateDescription/)
  assert.match(wizardSource, /Full project name[\s\S]*displayName/)
  assert.match(wizardSource, /Development components[\s\S]*componentOutcomes/)
  assert.match(wizardSource, /Create project/)
  assert.match(wizardSource, /aria-describedby="template-impact template-selection-review"/)
  assert.match(wizardSource, /type Step = 'describe' \| 'prepare' \| 'confirm'/)
  assert.match(wizardSource, /aria-label="Project creation steps"/)
  assert.match(wizardSource, /wizardStep\.id === step \? 'step' : undefined/)
  assert.match(wizardSource, /Step \$\{activeStepIndex\.value \+ 1\} of \$\{wizardSteps\.length\}/)
  assert.match(wizardSource, /index < activeStepIndex[\s\S]*text-text-primary[\s\S]*text-text-muted/)
  assert.match(wizardSource, /onMounted\(async \(\) => \{[\s\S]*stepHeading\.value\?\.focus\(\{ preventScroll: true \}\)/)
  assert.doesNotMatch(wizardSource, /min-h-\[470px\]/)
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
  assert.match(wizardSource, /if \(!hasInitialPrompt\.value\) step\.value = 'describe'/)
  assert.match(wizardSource, /<p v-if="error" role="alert"[\s\S]*\{\{ error \}\}/)
  assert.match(wizardSource, /v-else-if="error"[\s\S]*@click="runPlan"[\s\S]*Try again/)
  assert.match(wizardSource, /Preparing project plan…/)
  assert.equal((wizardSource.match(/<Loader2/g) ?? []).length, 1)
  assert.match(wizardSource, /function back\(\) \{[\s\S]*if \(hasInitialPrompt\.value\) \{[\s\S]*invalidatePlanRequest\(\)[\s\S]*planning\.value = false[\s\S]*emit\('cancel'\)/)
})

test('confirmed details emit the exact durable create payload and honor the disabled gate', () => {
  assert.match(wizardSource, /function confirmCreate\(\) \{[\s\S]*if \(props\.disabled\) return[\s\S]*emit\('create', \{[\s\S]*prompt: prompt\.value\.trim\(\),[\s\S]*templateName: chosenTemplate\.value \|\| undefined,[\s\S]*displayName: displayName\.value\.trim\(\) \|\| undefined,[\s\S]*\}\)[\s\S]*\}/)
  assert.match(wizardSource, /@click="confirmCreate"/)
  assert.match(wizardSource, /:disabled="disabled"/)
})

test('wizard keeps responsive controls and long blueprint content usable', () => {
  assert.match(wizardSource, /text-\[16px\][\s\S]*md:text-\[13px\]/)
  assert.match(wizardSource, /break-words[\s\S]*\[overflow-wrap:anywhere\]/)
  assert.match(wizardSource, /footer v-if="step === 'confirm'" class="flex flex-col[\s\S]*sm:flex-row/)
  assert.match(wizardSource, /class="inline-flex h-9 w-full[\s\S]*sm:w-auto"[\s\S]*Create project/)
  assert.match(wizardSource, /<ol class="[^"\n]*m-0[^"\n]*list-none[^"\n]*p-0[^"\n]*">/)
  assert.match(wizardSource, /class="font-mono text-\[11px\][\s\S]*text-text-secondary">Your request/)
  assert.match(wizardSource, /class="mt-2 whitespace-pre-wrap break-words text-\[14px\]/)
  assert.doesNotMatch(wizardSource, /text-\[10px\]/)
  assert.match(wizardSource, /placeholder:text-text-secondary/)
  assert.match(wizardSource, /font-mono text-\[12px\] text-text-secondary \[overflow-wrap:anywhere\].*\{\{ starterRepository \}\}/)
  assert.match(wizardSource, /Full project name:[\s\S]*break-words[\s\S]*overflow-wrap:anywhere/)
  assert.match(wizardSource, /Selected development template:[\s\S]*break-words[\s\S]*overflow-wrap:anywhere/)
  assert.match(wizardSource, /Workspace path:[\s\S]*component\.workspacePath/)
  assert.match(wizardSource, /focus-visible:ring-2 focus-visible:ring-accent\/40/)
})

test('provider styles define their own spacing scale for standalone utility layout', () => {
  assert.match(styleSource, /:scope\s*\{\s*--spacing:\s*0\.25rem;\s*\}/)
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
  assert.match(handoff, /prompt\.value = payload\.prompt/)
  assert.match(handoff, /await ensureCreateSetupReady\(\)/)
  assert.ok(handoff.indexOf('await ensureCreateSetupReady()') < handoff.indexOf('wizardOpen.value = false'), 'readiness must pass before closing the confirmation surface')
  assert.match(handoff, /createProjectAndStartConversation\(payload\.prompt, \{[\s\S]*templateName: payload\.templateName,[\s\S]*displayName: payload\.displayName/)
  assert.match(appSource, /:setup-items="createSetupItemsForPrompt"/)
  assert.match(appSource, /:setup-error="createSetupErrorMessage"/)
  assert.match(appSource, /:setup-loading="createSetupLoading"/)
  assert.match(appSource, /:code-connections-url="CODE_CONNECTIONS_URL"/)
  assert.match(appSource, /@setup-action="onWizardSetupAction"/)
  assert.match(appSource, /@retry-setup="onWizardSetupRetry"/)
  assert.match(appSource, /await Promise\.all\(\[loadCreateReadiness\(\), loadLLMSettings\(\)\]\)/)
  assert.match(wizardSource, /target="_blank"[\s\S]*rel="noopener noreferrer"[\s\S]*Connect Git/)
  assert.match(wizardSource, /@click="emit\('retry-setup'\)"[\s\S]*Check again/)

  const startPath = appSource.slice(appSource.indexOf('async function createProjectAndStartConversation('))
  assert.match(startPath, /api\.createProjectStream\(props\.ctx, \{/)
  assert.match(startPath, /api\.createAssistantThread\(props\.ctx, projectName\)/)
  assert.match(startPath, /api\.startAssistantTurn\(props\.ctx, projectName, thread\.id, \{/)
})

test('landing intake uses a compact Faros composer with concrete prompts and a repository popover', () => {
  assert.match(appSource, /What are we building in Faros today\?/)
  assert.match(appSource, /Describe what you want to build\. Faros turns your idea into a blueprint[\s\S]*before anything is created/)
  assert.match(appSource, /<label for="landing-project-prompt" class="sr-only">\s*Describe what you want to build\s*<\/label>/)
  assert.match(appSource, /id="landing-project-prompt"[\s\S]*placeholder:text-text-secondary[\s\S]*placeholder="Describe what you want to build…"/)
  assert.match(appSource, /@keydown\.ctrl\.enter\.prevent="createProjectFromPrompt"/)
  assert.match(appSource, /@keydown\.meta\.enter\.prevent="createProjectFromPrompt"/)
  assert.doesNotMatch(appSource, /@keydown\.enter\.exact\.prevent="createProjectFromPrompt"/)
  assert.doesNotMatch(appSource, /Enter adds a line · Ctrl\/⌘ Enter prepares the project for review\./)
  assert.doesNotMatch(appSource, /landingPlaceholder|landingComposerPlaceholder|typeLandingPlaceholder|startLandingPlaceholder|clearLandingPlaceholder/)
  assert.doesNotMatch(appSource, /text-\[44px\]|text-\[56px\]/)

  assert.match(appSource, /<div class="ml-auto flex min-w-0 shrink items-center justify-end gap-1">\s*<ModelPicker[\s\S]*:models="configuredLLMModels"[\s\S]*:selected-i-d="selectedLLMModel\?\.id \|\| ''"[\s\S]*@select="selectedLLMModelID = \$event"[\s\S]*<button\s+type="submit"[\s\S]*rounded-md bg-accent text-on-accent[\s\S]*disabled:shadow-none/)
  assert.match(appSource, /<ArrowUp class="h-4 w-4" :stroke-width="1\.75" \/>/)
  assert.doesNotMatch(appSource, /Auto/)

  assert.match(appSource, /const landingStarterPrompts: LandingStarterPrompt\[\] = \[/)
  assert.match(appSource, /v-for="starter in landingStarterPrompts"/)
  assert.match(appSource, /Starting points/)
  for (const prompt of [
    'Create a feedback tracker that collects requests, tags themes, and surfaces top priorities',
    'Create a SaaS KPI dashboard with revenue trends, churn risk, and filters',
    'Build a purchase approval workflow with roles and audit history',
    'Create a partner API console with keys, usage charts, and request logs',
  ]) assert.match(appSource, new RegExp(prompt.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
  const landingStartingPoints = appSource.match(/<section class="mx-auto mt-5 w-full max-w-\[860px\]" aria-labelledby="landing-starting-points-title">[\s\S]*?<\/section>/)?.[0]
  const landingComposer = appSource.indexOf('<form class="mx-auto mt-5 max-w-[860px]"')
  assert.ok(landingStartingPoints, 'starting points must be rendered as one section')
  assert.ok(appSource.indexOf('<section class="mx-auto mt-5 w-full max-w-[860px]"') < landingComposer, 'starting points must precede the composer')
  assert.match(landingStartingPoints, /class="grid gap-1\.5"/)
  assert.doesNotMatch(landingStartingPoints, /grid-cols-1|grid-cols-2/)
  assert.match(landingStartingPoints, /<component :is="starter\.icon" class="h-4 w-4" :stroke-width="1\.75" \/>/)
  assert.doesNotMatch(appSource, /landingCategoryTiles|projectStarterTemplates|landingPromptChips/)
  assert.doesNotMatch(appSource, /Explore example prompts|<details/)
  const submitTitle = appSource.match(/const createPromptSubmitTitle = computed\(\(\) => \{[\s\S]*?\n\}\)/)?.[0]
  assert.ok(submitTitle, 'landing submit title must remain explicit')
  assert.match(submitTitle, /Prepare project for review/)
  assert.doesNotMatch(submitTitle, /Create project and send prompt/)

  assert.match(appSource, /ref="landingImportPopoverRef"/)
  assert.match(appSource, /ref="landingImportDialogRef"[\s\S]*role="dialog"[\s\S]*tabindex="-1"/)
  assert.match(appSource, /ref="landingImportTriggerRef"/)
  assert.match(appSource, /aria-haspopup="dialog"[\s\S]*aria-controls="landing-import-popover"[\s\S]*:aria-expanded="landingImportOpen"/)
  assert.match(appSource, /id="landing-import-popover"[\s\S]*role="dialog"[\s\S]*aria-label="Import an existing repository"/)
  assert.match(appSource, /Loading repositories…/)
  assert.match(appSource, /No unclaimed repositories available\./)
  assert.match(appSource, /importRepositoriesError[\s\S]*Retry/)
  assert.match(appSource, /importRepositoryProject/)
  assert.match(appSource, /Updating repositories…[\s\S]*text-text-secondary/)
  assert.match(appSource, /v-model="importSelectedRepository"[\s\S]*text-\[16px\][\s\S]*md:text-\[12px\]/)
  assert.match(appSource, /api\.createProject\(props\.ctx, \{ existingRepositoryRef: repositoryRef \}\)/)
  assert.match(appSource, /function handleLandingImportEscape\(event: KeyboardEvent\)[\s\S]*event\.key !== 'Escape'[\s\S]*closeLandingImportPopover\(true\)/)
  assert.match(appSource, /function handleLandingImportOutside\(event: PointerEvent\)[\s\S]*root\.contains\(target\)[\s\S]*closeLandingImportPopover\(\)/)
  assert.match(appSource, /function toggleLandingImport\(\)[\s\S]*landingImportDialogRef\.value[\s\S]*\.focus\(\{ preventScroll: true \}\)/)
  assert.match(appSource, /document\.addEventListener\('pointerdown', handleLandingImportOutside\)/)
  assert.match(appSource, /document\.addEventListener\('keydown', handleLandingImportEscape\)/)
})
