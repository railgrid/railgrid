import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/railgrid-vite-new-project-wizard',
  configFile: false,
  plugins: [vue()],
  server: { middlewareMode: true, hmr: false },
})
const { default: NewProjectWizard } = await vite.ssrLoadModule('/src/NewProjectWizard.vue')
const { api } = await vite.ssrLoadModule('/src/api.ts')
const wizardSource = await readFile(new URL('./NewProjectWizard.vue', import.meta.url), 'utf8')
const preProjectComposerSource = await readFile(new URL('./AssistantPreProjectComposer.vue', import.meta.url), 'utf8')
const attachmentPreviewSource = await readFile(new URL('./AssistantAttachmentPreview.vue', import.meta.url), 'utf8')
const sharedAttachmentPreviewSource = await readFile(new URL('./agentkit/AgentKitAttachmentPreview.vue', import.meta.url), 'utf8')
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
    assert.match(html, /Preparing review/)
    assert.match(html, /Preparing project plan…/)
    assert.equal((html.match(/Preparing project plan…/g) ?? []).length, 1)
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

test('auth-compatible creation opens as an editable draft and does not start planning automatically', async () => {
  const calls = []
  const originalPlanProject = api.planProject
  api.planProject = async (_ctx, body) => {
    calls.push(body)
    return { displayName: 'Protected app', repositoryName: 'protected-app', availableTemplates: [] }
  }

  try {
    const html = await renderToString(createSSRApp(NewProjectWizard, {
      ctx: null,
      authCompatibleOnly: true,
      initialDraft: 'Create a new app with protected user data.',
    }))

    assert.deepEqual(calls, [])
    assert.match(html, /Create a compatible project/)
    assert.match(html, /Your current project stays unchanged/)
    assert.match(html, /Checking compatible templates…/)
    assert.match(html, /Create a new app with protected user data\./)
    assert.doesNotMatch(html, /Prepare your project/)
  } finally {
    api.planProject = originalPlanProject
  }
})

test('auth-compatible planning and confirmation fail closed on unverified or changed template choices', () => {
  assert.match(wizardSource, /template\?\.browserPreview === true && template\.railgridAuthSupported === true && template\.railgridAuthReason === 'supported'/)
  assert.match(wizardSource, /templateName: requiredTemplate/)
  assert.match(wizardSource, /result\.template !== requiredTemplate/)
  assert.match(wizardSource, /supportedChoices\.some\(\(template\) => template\.name === requiredTemplate\)/)
  assert.match(wizardSource, /creationChecking\) return/)
  assert.match(appSource, /api\.listDevelopmentTemplates\(props\.ctx\)[\s\S]*selectedTemplate\?\.railgridAuthSupported !== true[\s\S]*selectedTemplate\.railgridAuthReason !== 'supported'/)
  assert.match(appSource, /@create-compatible-project="openAuthCompatibleProjectCreation"/)
  assert.match(appSource, /@setup-auth="setupAuthInCurrentProjectDraft"/)
})

test('confirmation shows the original request, attachments, and resources for the chosen template', () => {
  assert.match(wizardSource, />Your request<\/h3>[\s\S]*\{\{ initialPrompt \|\| prompt \}\}/)
  assert.match(wizardSource, /<span[^>]*>Project name<\/span>[\s\S]*v-model="displayName"/)
  assert.match(wizardSource, /v-if="attachments\?\.length \|\| attachmentError"[\s\S]*attachment-only/)
  assert.match(wizardSource, /id="review-attachments-heading"[\s\S]*<section[^>]*aria-labelledby="review-source-control-heading">\s*<h3[^>]*>Source control<\/h3>\s*<slot name="repository-options" \/>\s*<\/section>[\s\S]*id="review-development-template"/)
  assert.match(wizardSource, /<label for="review-development-template">Development template<\/label>[\s\S]*v-model="chosenTemplate"/)
  assert.match(wizardSource, /activeTemplate\.value\?\.resourceKinds/)
  assert.match(wizardSource, /Resources this template creates[\s\S]*v-for="resource in templateResources"/)
  assert.match(wizardSource, /selectableTemplates\.value\.find\(\(template\) => template\.name === chosenTemplate\.value\)/)
  assert.match(wizardSource, /The project starts without a development instance/)
  assert.match(wizardSource, /attaches the template’s starter source when it is available/)
  assert.match(wizardSource, /Create project/)
  assert.match(wizardSource, /aria-describedby="template-impact"/)
  assert.match(wizardSource, /type Step = 'describe' \| 'prepare' \| 'confirm'/)
  assert.match(wizardSource, /onMounted\(async \(\) => \{[\s\S]*stepHeading\.value\?\.focus\(\{ preventScroll: true \}\)/)
  assert.doesNotMatch(wizardSource, /min-h-\[470px\]/)
  assert.doesNotMatch(wizardSource, /Review your project|Plan ready|Planning in progress/)
})

test('wizard uses the shared route-owned creation skeleton with progress inside the review card', () => {
  assert.match(wizardSource, /class="k-create-surface k-create-surface--wide flex w-full flex-col"/)
  assert.match(wizardSource, /aria-labelledby="new-project-details-title"[\s\S]*<slot name="review-progress" \/>/)
  assert.match(wizardSource, /class="k-create-body flex w-full flex-col gap-5 p-5 sm:p-7"/)
  assert.match(wizardSource, /class="k-create-actions flex flex-col-reverse/)
  assert.match(wizardSource, /class="k-create-actions flex flex-col items-stretch/)
  assert.match(wizardSource, /type Step = 'describe' \| 'prepare' \| 'confirm'/)
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
  assert.equal((wizardSource.match(/<Loader2/g) ?? []).length, 2)
  assert.match(wizardSource, /function back\(\) \{[\s\S]*if \(hasInitialPrompt\.value\) \{[\s\S]*invalidatePlanRequest\(\)[\s\S]*planning\.value = false[\s\S]*emit\('cancel'\)/)
})

test('confirmed details emit the exact durable create payload and honor the disabled gate', () => {
  assert.match(wizardSource, /function confirmCreate\(\) \{[\s\S]*if \(props\.disabled \|\| props\.creationChecking\) return[\s\S]*emit\('create', \{[\s\S]*prompt: prompt\.value\.trim\(\),[\s\S]*templateName: chosenTemplate\.value \|\| undefined,[\s\S]*displayName: displayName\.value\.trim\(\) \|\| undefined,[\s\S]*\}\)[\s\S]*\}/)
  assert.match(wizardSource, /@click="confirmCreate"/)
  assert.match(wizardSource, /:disabled="disabled \|\| creationChecking \|\| !hasValidTemplateChoice"/)
  assert.match(wizardSource, /if \(!hasValidTemplateChoice\.value\) return\s+emit/)
  assert.match(wizardSource, /<option v-if="allowEmptyTemplate" value="">No template \(start empty\)<\/option>/)
})

test('wizard keeps responsive controls and long blueprint content usable', () => {
  assert.match(wizardSource, /text-\[16px\][\s\S]*md:text-\[13px\]/)
  assert.match(wizardSource, /break-words[\s\S]*\[overflow-wrap:anywhere\]/)
  assert.match(wizardSource, /footer v-if="step === 'confirm'" class="k-create-actions flex flex-col[\s\S]*sm:flex-row/)
  assert.match(wizardSource, /class="mt-2 whitespace-pre-wrap break-words text-\[13px\]/)
  assert.doesNotMatch(wizardSource, /text-\[10px\]/)
  assert.match(wizardSource, /developmentInstanceKind[\s\S]*overflow-wrap:anywhere/)
  assert.match(wizardSource, /focus-visible:ring-2 focus-visible:ring-accent\/40/)
})

test('provider styles define their own spacing scale for standalone utility layout', () => {
  assert.match(styleSource, /:scope\s*\{\s*--spacing:\s*0\.25rem;\s*\}/)
})

test('App replaces the landing composer with the wizard and wires cancel to restore the exact prompt focus', () => {
  const wizardBlock = appSource.match(/<div v-if="wizardOpen" v-show="[^"]+">[\s\S]*?<\/NewProjectWizard>/)?.[0]
  assert.ok(wizardBlock, 'wizard must occupy the landing surface when open')
  assert.match(wizardBlock, /<NewProjectWizard/)
  assert.match(wizardBlock, /:initial-prompt="authCompatibleCreation \? undefined : prompt"/)
  assert.match(wizardBlock, /@cancel="onWizardCancel"/)
  assert.match(appSource, /<form v-else-if="llmConfigured"[\s\S]*?<AssistantPreProjectComposer/)
  assert.match(appSource, /<section class="w-full" :class="llmConfigured && !wizardOpen && !createModelSetup \? \'max-w-\[860px\]\' : \'max-w-\[760px\]\'">/)
  assert.match(appSource, /const isBuilderVisible = computed\(\(\) =>[\s\S]*selected\.value !== null\)/)
  assert.match(appSource, /props\.requestFullBleed\?\.\(visible\)/)

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
  assert.match(handoff, /await projectCreationSubmit\.run\(async \(\) => \{[\s\S]*ensureCreateSetupReady\(\)/)
  assert.ok(handoff.indexOf('ensureCreateSetupReady()') < handoff.indexOf('const templates = await api.listDevelopmentTemplates(props.ctx)'), 'auth template support is rechecked after setup and before project creation')
  assert.ok(handoff.indexOf('projectCreationSubmit.run') < handoff.indexOf('wizardOpen.value = false'), 'guarded readiness must pass before closing the confirmation surface')
  assert.match(appSource, /:disabled="projectCreationPending \|\| busy \|\| !canStartProjectFromPrompt \|\| !llmConfigured"/)
  assert.match(appSource, /async function onWizardCancel\(\) \{\s+projectCreationSubmit\.invalidate\(\)/)
  assert.match(handoff, /createProjectAndStartConversation\(payload\.prompt, \{[\s\S]*templateName: payload\.templateName,[\s\S]*displayName: payload\.displayName/)
  assert.match(appSource, /:setup-items="createSetupItemsForPrompt"/)
  assert.match(appSource, /:setup-error="llmSettingsError \|\| \'\'"/)
  assert.match(appSource, /:setup-loading="createSetupLoading"/)
  assert.match(appSource, /:code-connections-url="CODE_CONNECTIONS_URL"/)
  assert.match(appSource, /@setup-action="onWizardSetupAction"/)
  assert.match(appSource, /@retry-setup="onWizardSetupRetry"/)
  const setupAuth = appSource.match(/async function setupAuthInCurrentProjectDraft\(\)[\s\S]*?\n\}/)?.[0]
  assert.ok(setupAuth, 'setup-auth must remain an explicit guarded draft action')
  assert.match(setupAuth, /selected\.value\?\.uid[\s\S]*activeAssistantThreadID\.value === threadID/)
  assert.match(setupAuth, /!selectingThreadID\.value[\s\S]*!threadHistoryLoading\.value/)
  assert.match(setupAuth, /assistantComposerParts\.value = nextParts[\s\S]*persistCurrentAssistantComposerDraft\(nextParts\)/)
  assert.doesNotMatch(setupAuth, /!threadID/)
  assert.match(appSource, /await Promise\.all\(\[loadCreateReadiness\(\), loadLLMSettings\(\)\]\)/)
  assert.match(wizardSource, /target="_blank"[\s\S]*rel="noopener noreferrer"[\s\S]*Connect Git/)
  assert.match(wizardSource, /@click="emit\('retry-setup'\)"[\s\S]*Check again/)

  const startPath = appSource.slice(appSource.indexOf('async function createProjectAndStartConversation('))
  assert.match(startPath, /api\.createProjectStream\(props\.ctx, \{/)
  assert.match(startPath, /api\.createAssistantThread\(\s*props\.ctx,\s*projectName,\s*undefined,\s*requestedThreadID,\s*supportedThreadReasoningEffortSnapshot\(startPlan\.reasoningEffortByModel\),/)
  assert.match(startPath, /startPreProjectAssistantTurn\(projectName, submission, thread\.id(?:, current)?\)/)
})

test('landing intake uses a compact Railgrid composer with concrete prompts and a repository popover', () => {
  assert.match(appSource, /Describe your project/)
  assert.match(appSource, /<label for="landing-project-prompt" class="sr-only">\s*Describe what you want to build\s*<\/label>/)
  assert.match(appSource, /input-id="landing-project-prompt"/)
  assert.match(preProjectComposerSource, /:id="inputId"[\s\S]*placeholder:text-text-secondary[\s\S]*:placeholder="placeholder"/)
  assert.match(preProjectComposerSource, /@keydown\.ctrl\.enter\.prevent="emit\('submit'\)"/)
  assert.match(preProjectComposerSource, /@keydown\.meta\.enter\.prevent="emit\('submit'\)"/)
  assert.doesNotMatch(appSource, /@keydown\.enter\.exact\.prevent="createProjectFromPrompt"/)
  assert.doesNotMatch(appSource, /Enter adds a line · Ctrl\/⌘ Enter prepares the project for review\./)
  assert.doesNotMatch(appSource, /landingPlaceholder|landingComposerPlaceholder|typeLandingPlaceholder|startLandingPlaceholder|clearLandingPlaceholder/)
  assert.doesNotMatch(appSource, /text-\[44px\]|text-\[56px\]/)

  assert.match(appSource, /<template #actions>[\s\S]*<AIModelReasoningPicker[\s\S]*:models="configuredLLMModels"[\s\S]*:selected-i-d="selectedLLMModel\?\.id \|\| ''"[\s\S]*:effort="landingReasoningEffort"[\s\S]*@select="selectedLLMModelID = \$event"[\s\S]*@update:effort="selectLandingReasoningEffort"[\s\S]*<button\s+type="submit"/)
  assert.match(preProjectComposerSource, /class="ml-auto flex min-w-0 flex-wrap items-center justify-end gap-1"/)
  assert.match(preProjectComposerSource, /role="menu"/)
  assert.match(appSource, /Review project <ArrowRight class="h-4 w-4"/)
  assert.doesNotMatch(appSource, />\s*Auto\s*</)

  assert.match(appSource, /const landingStarterPrompts: LandingStarterPrompt\[\] = \[/)
  assert.match(appSource, /v-for="starter in landingStarterPrompts"/)
  assert.match(appSource, /Starting points/)
  for (const prompt of [
    'Create a feedback tracker that collects requests, tags themes, and surfaces top priorities',
    'Create a SaaS KPI dashboard with revenue trends, churn risk, and filters',
    'Build a purchase approval workflow with roles and audit history',
    'Create a partner API console with keys, usage charts, and request logs',
  ]) assert.match(appSource, new RegExp(prompt.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
  const landingStartingPoints = appSource.match(/<section aria-labelledby="landing-starting-points-title">[\s\S]*?<\/section>/)?.[0]
  assert.ok(landingStartingPoints, 'starting points should be visible below the composer')
  assert.match(landingStartingPoints, /@click="applyLandingStarterPrompt\(starter\)"/)
  assert.match(landingStartingPoints, /\{\{ starter\.description \}\}/)
  assert.doesNotMatch(landingStartingPoints, /<details/)
  assert.doesNotMatch(appSource, /landingCategoryTiles|projectStarterTemplates|landingPromptChips/)
  const submitTitle = appSource.match(/const createPromptSubmitTitle = computed\(\(\) => \{[\s\S]*?\n\}\)/)?.[0]
  assert.ok(submitTitle, 'landing submit title must remain explicit')
  assert.match(submitTitle, /Review project/)
  assert.doesNotMatch(submitTitle, /Create project and send prompt/)

  assert.match(appSource, /ref="landingImportPopoverRef"/)
  assert.match(appSource, /ref="landingImportDialogRef"[\s\S]*role="dialog"[\s\S]*tabindex="-1"/)
  assert.match(appSource, /ref="landingImportTriggerRef"/)
  assert.match(appSource, /<header[^>]*>[\s\S]*ref="landingImportTriggerRef"[\s\S]*Import repository[\s\S]*id="landing-import-popover"[\s\S]*<\/header>/)
  const landingComposerSource = appSource.match(/<AssistantPreProjectComposer[\s\S]*?<\/AssistantPreProjectComposer>/)?.[0]
  assert.ok(landingComposerSource, 'landing composer remains present')
  assert.doesNotMatch(landingComposerSource, /<template #leading>/)
  assert.doesNotMatch(landingComposerSource, /<FirstRunGuide|#header>/)
  const landingForm = appSource.match(/<form v-else-if="llmConfigured"[^>]*>[\s\S]*?<\/form>/)?.[0]
  assert.ok(landingForm, 'landing prompt form remains present')
  assert.doesNotMatch(landingForm, /<FirstRunGuide|k-create-surface/)
  assert.match(appSource, /aria-haspopup="dialog"[\s\S]*aria-controls="landing-import-popover"[\s\S]*:aria-expanded="landingImportOpen"/)
  assert.match(appSource, /id="landing-import-popover"[\s\S]*role="dialog"[\s\S]*aria-label="Import an existing repository"/)
  assert.match(appSource, /Loading repositories…/)
  assert.match(appSource, /No unclaimed repositories available\./)
  assert.match(appSource, /importRepositoriesError[\s\S]*Retry/)
  assert.match(appSource, /importRepositoryProject/)
  assert.match(appSource, /@add-attachment="stagePreProjectAttachment"/)
  assert.match(appSource, /:attachments="preProjectAttachments"/)
  assert.match(preProjectComposerSource, /aria-label="Add"/)
  assert.match(preProjectComposerSource, /<Paperclip[\s\S]*Files/)
  assert.match(preProjectComposerSource, /:accept="ASSISTANT_ATTACHMENT_ACCEPT"/)
  assert.match(preProjectComposerSource, /:accept="ASSISTANT_ATTACHMENT_ACCEPT"[\s\S]*multiple/)
  assert.doesNotMatch(preProjectComposerSource, /Screenshot|Text file/)
  assert.match(preProjectComposerSource, /ASSISTANT_LARGE_PASTE_BYTES/)
  assert.match(preProjectComposerSource, /clipboardImageFile/)
  assert.match(appSource, /Updating repositories…[\s\S]*text-text-secondary/)
  assert.match(appSource, /v-model="importSelectedRepository"[\s\S]*class="app-studio-touch-target k-input h-9 min-w-0 text-\[16px\] md:text-\[12px\]"/)
  assert.match(appSource, /api\.createProject\(props\.ctx, \{ existingRepositoryRef: repositoryRef \}\)/)
  assert.match(appSource, /function handleLandingImportEscape\(event: KeyboardEvent\)[\s\S]*event\.key !== 'Escape'[\s\S]*closeLandingImportPopover\(true\)/)
  assert.match(appSource, /function handleLandingImportOutside\(event: PointerEvent\)[\s\S]*root\.contains\(target\)[\s\S]*closeLandingImportPopover\(\)/)
  assert.match(appSource, /function toggleLandingImport\(\)[\s\S]*landingImportDialogRef\.value[\s\S]*\.focus\(\{ preventScroll: true \}\)/)
  assert.equal((appSource.match(/landingImportOpen\.value = false/g) || []).length, 1, 'all landing import close paths must use the shared close helper')
  assert.match(preProjectComposerSource, /'close-menu': \[\]/)
  assert.match(preProjectComposerSource, /function closeAttachmentMenu\(\)[\s\S]*emit\('close-menu'\)/)
  assert.match(preProjectComposerSource, /if \(attachmentMenuOpen\.value\) \{[\s\S]*closeAttachmentMenu\(\)/)
  assert.match(preProjectComposerSource, /function openAttachmentPicker\(\)[\s\S]*closeAttachmentMenu\(\)/)
  assert.doesNotMatch(landingComposerSource, /landingImportTriggerRef/)
  assert.match(appSource, /document\.addEventListener\('pointerdown', handleLandingImportOutside\)/)
  assert.match(appSource, /document\.addEventListener\('keydown', handleLandingImportEscape\)/)
})

test('pre-project attachments stay parent-owned across both intake surfaces and hand off receipts with the prompt', () => {
  const describeStart = wizardSource.indexOf('<div class="grid gap-2">')
  const describeEnd = wizardSource.indexOf('\n        <p v-if="error"', describeStart)
  assert.ok(describeStart >= 0 && describeEnd > describeStart, 'project description controls should remain a bounded block')
  const describeBlock = wizardSource.slice(describeStart, describeEnd)
  assert.match(describeBlock, /<label for="new-project-prompt"[^>]*>Project description<\/label>\s*<AssistantPreProjectComposer/)
  assert.doesNotMatch(describeBlock, /<label for="new-project-prompt"[^>]*>[\s\S]*<AssistantPreProjectComposer[\s\S]*<\/label>/)
  assert.match(wizardSource, /AssistantPreProjectComposer/)
  assert.match(wizardSource, /:attachments="attachments"/)
  assert.match(wizardSource, /attachment-only/)
  assert.match(wizardSource, /@add-attachment="emit\('add-attachment', \$event\)"/)
  assert.match(wizardSource, /@remove-attachment="emit\('remove-attachment', \$event\)"/)
  assert.match(wizardSource, /@retry-attachment="emit\('retry-attachment', \$event\)"/)
  assert.match(preProjectComposerSource, /ASSISTANT_ATTACHMENT_ACCEPT/)
  assert.match(preProjectComposerSource, /ref="rootRef"[\s\S]*:tabindex="attachmentOnly \? 0 : undefined"/)
  assert.match(preProjectComposerSource, /:role="attachmentOnly \? 'group' : undefined"/)
  assert.match(preProjectComposerSource, /@paste\.self="handlePaste"/)
  assert.match(preProjectComposerSource, /<textarea[\s\S]*@paste="handlePaste"/)
  assert.match(preProjectComposerSource, /function focus\(\)[\s\S]*editorRef\.value \|\| rootRef\.value/)
  assert.match(preProjectComposerSource, /event\.preventDefault\(\)[\s\S]*stageFile\(image\)/)
  assert.match(preProjectComposerSource, /ASSISTANT_LARGE_PASTE_BYTES/)
  assert.match(preProjectComposerSource, /new File\(\[text\], 'pasted-text\.txt'/)
  assert.match(preProjectComposerSource, /AssistantAttachmentPreview/)
  assert.match(preProjectComposerSource, /assistantAttachmentIsImage\(attachment\.file\)/)
  assert.match(preProjectComposerSource, /`Remove attachment \$\{attachmentLabel\(attachment\)\}`/)
  assert.match(preProjectComposerSource, /`Retry attachment upload \$\{attachmentLabel\(attachment\)\}`/)
  assert.match(attachmentPreviewSource, /<AgentKitAttachmentPreview[\s\S]*v-bind="props"[\s\S]*@remove="emit\('remove'\)"[\s\S]*@retry="emit\('retry'\)"/)
  assert.match(sharedAttachmentPreviewSource, /URL\.createObjectURL\(file\)/)
  assert.match(sharedAttachmentPreviewSource, /URL\.revokeObjectURL\(previewURL\.value\)/)
  assert.match(sharedAttachmentPreviewSource, /watch\(\(\) => props\.file, syncPreview, \{ immediate: true \}\)/)
  assert.match(sharedAttachmentPreviewSource, /onBeforeUnmount\(revokePreview\)/)
  assert.match(sharedAttachmentPreviewSource, /<img v-if="previewURL"[\s\S]*:src="previewURL"/)
  assert.match(sharedAttachmentPreviewSource, /`Remove attachment \$\{label\}`/)
  assert.doesNotMatch(sharedAttachmentPreviewSource, /Ready to attach/)

  const startPath = appSource.slice(appSource.indexOf('async function createProjectAndStartConversation('))
  assert.match(startPath, /ensurePreProjectAttachmentsUploaded\(projectName(?:, current)?\)/)
  assert.match(appSource, /function preProjectStartContentParts\(projectName: string, content: string\)[\s\S]*type: 'text' as const, text: content/)
  assert.match(appSource, /function startPreProjectAssistantTurn\([\s\S]*preProjectStartContentParts\(projectName, startPlan\.content\)/)
  assert.match(startPath, /startPreProjectAssistantTurn\(projectName, submission, thread\.id(?:, current)?\)/)
  const startRequest = startPath.slice(startPath.indexOf('const canonical = await startPreProjectAssistantTurn'))
  assert.ok(startRequest.indexOf('clearPreProjectAttachments(true)') > 0, 'receipts clear only after turn acceptance')
  assert.match(startPath, /preProjectAttachmentProjectName = projectName/)
  assert.match(startPath, /firstProjectSubmissionWithProject\(submission, projectName\)/)
  assert.match(appSource, /recoverPreProjectAttachmentReceipts\(projectName, true(?:, isCurrent)?\)/)
  assert.match(startPath, /requestedThreadID/)
  assert.match(startPath, /firstProjectSubmissionWithThread\(submission, requestedThreadID\)/)
  assert.match(startPath, /canonicalThreadID/)
  assert.match(startPath, /listAssistantThreadItemPage\(props\.ctx, projectName, canonicalThreadID\)/)
  assert.match(appSource, /retryPreProjectAttachment/)
  assert.match(appSource, /retryAction: 'delete'/)
  assert.match(appSource, /api\.deleteAssistantAttachment\(props\.ctx, attachment\.projectName, attachment\.receipt\.id\)/)
})

test('keeps a project-bound first submission when returning to ~new for attachment retry', () => {
  assert.match(appSource, /function shouldKeepProjectBoundFirstSubmission\(\): boolean \{[\s\S]*isCreateRoute\.value[\s\S]*pendingFirstProjectSubmission\?\.content/)
  assert.match(appSource, /pendingFirstProjectSubmission[\s\S]*projectName !== pendingFirstProjectSubmission\.projectName[\s\S]*!shouldKeepProjectBoundFirstSubmission\(\)/)
  assert.match(appSource, /pendingFirstProjectSubmission &&[\s\S]*pathName !== pendingFirstProjectSubmission\.projectName[\s\S]*!shouldKeepProjectBoundFirstSubmission\(\)/)
  assert.match(appSource, /if \(!shouldKeepProjectBoundFirstSubmission\(\)\) clearPendingFirstProjectSubmission\(\)/)
})

test('attachment-only landing submission is admitted and uses an explicit derived planning input', () => {
  assert.match(appSource, /const createPromptContent = computed\(\(\) => projectCreationPrompt\(prompt\.value, preProjectAttachments\.value\.length\)\)/)
  assert.match(appSource, /canSubmitCreatePrompt\(createPromptContent\.value, createReadiness\.value\)/)
  assert.match(appSource, /const createPromptContent = computed\(\(\) => projectCreationPrompt\(prompt\.value, preProjectAttachments\.value\.length\)\)/)
  assert.match(appSource, /if \(!prompt\.value\.trim\(\)\) prompt\.value = content/)
  assert.match(appSource, /preProjectStartContentParts\(projectName, startPlan\.content\)/)
  assert.match(appSource, /type: 'text' as const, text: content/)
})

test('project-bound retry remains fenced by mount and pending-submission identity', () => {
  const guardStart = appSource.indexOf('const current = () => appComponentMounted')
  const guardEnd = appSource.indexOf('\n\n  try {', guardStart)
  assert.ok(guardStart >= 0 && guardEnd > guardStart)
  const guard = appSource.slice(guardStart, guardEnd)
  assert.match(guard, /appComponentMounted && pendingFirstProjectSubmission === submission && \(/)
  assert.match(guard, /firstProjectSubmissionIsCurrent\([\s\S]*\) \|\| firstProjectSubmissionCanRetryFromCreateRoute\(/)
  assert.equal((guard.match(/appComponentMounted/g) || []).length, 1)
  assert.equal((guard.match(/pendingFirstProjectSubmission === submission/g) || []).length, 1)
  assert.match(guard, /\n  \)$/)
})
