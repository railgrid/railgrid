import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import ts from 'typescript'
import { effectScope, ref, watch } from 'vue'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')

test('mounts the shared conversation rail against App Studio lifecycle handlers', () => {
  assert.match(app, /import AIConversationRail from '\.\/agentkit\/AIConversationRail\.vue'/)
  assert.match(app, /<AIConversationRail[\s\S]*:threads="assistantThreads"[\s\S]*:active-thread-i-d="activeAssistantThreadID"/)
  assert.match(app, /<AIConversationRail[\s\S]*:unread-thread-i-ds="unreadAssistantThreadIDs"/)
  assert.match(app, /<AIConversationRail[\s\S]*:pinned-thread-i-ds="pinnedAssistantThreadIDs"/)
  assert.match(app, /<AIConversationRail[\s\S]*:capabilities="\{ create: true, rename: !threadActionsDisabled, pin: true, unread: true, archive: !messageStreaming \}"/)
  assert.match(app, /<AIConversationRail[\s\S]*@select="selectAssistantThread"[\s\S]*@create="createAssistantThread"/)
  assert.match(app, /<AIConversationRail[\s\S]*@archive="archiveAssistantThread"[\s\S]*@rename="renameAssistantThreadFromRail"[\s\S]*@toggle-pin="toggleThreadPin"[\s\S]*@set-unread="setThreadUnread"/)
  assert.match(app, /<AIConversationRail[\s\S]*:storage-scope="assistantConversationRailStorageScope"/)
  assert.match(app, /<AIConversationRail[\s\S]*overlay-target="#app-studio-overlay-root"[\s\S]*panel-id="app-studio-thread-rail"/)
  assert.doesNotMatch(app, /Manage threads|manageAssistantThreads|@manage=/)
})

test('keeps pin and manual read state project scoped and archives through the canonical API', () => {
  assert.match(app, /from '\.\/assistantThreadPinState'/)
  assert.match(app, /readAssistantThreadPins\([\s\S]*assistantThreadFocusStorageKey\(assistantThreadFocusScope\(projectName\)\)/)
  assert.match(app, /toggleAssistantThreadPin\([\s\S]*pinnedAssistantThreadIDs\.value/)
  assert.match(app, /removeAssistantThreadPin\(threadStateScopeKey, threadID\)/)
  assert.match(app, /markAssistantThreadUnread\(scopeKey, thread\)/)
  assert.match(app, /markAssistantThreadRead\(scopeKey, thread\)/)
  assert.match(app, /removeAssistantThreadReadState\(threadStateScopeKey, threadID\)/)
  assert.match(app, /threadID === activeAssistantThreadID\.value[\s\S]*setThreadUnread\(threadID, false\)/)
  assert.match(app, /api\.patchAssistantThread\(props\.ctx, projectName, threadID, \{ archived: true \}\)/)
  assert.match(app, /const remaining = assistantThreads\.value\.filter\(\(thread\) => thread\.id !== threadID\)/)
  const archiveSource = app.slice(app.indexOf('async function archiveAssistantThread'), app.indexOf('\nfunction assistantRunForMessage'))
  assert.match(archiveSource, /await api\.patchAssistantThread\([\s\S]*if \(!requestIsCurrent\(\)\) return[\s\S]*removeAssistantThreadPin\(threadStateScopeKey, threadID\)[\s\S]*removeAssistantThreadReadState\(threadStateScopeKey, threadID\)/)
  assert.match(archiveSource, /const requestGuard = beginProjectRequest\(\)/)
  assert.match(archiveSource, /const archiveRequestSerial = beginAssistantThreadRequest\(\)/)
  assert.match(archiveSource, /projectRequestIsCurrent\(requestGuard, projectName\)[\s\S]*archiveRequestSerial === assistantThreadRequestSerial/)
  assert.match(archiveSource, /const threadMutationLatchOwner = beginThreadMutationLatch\(threadID\)/)
  assert.match(archiveSource, /finally \{[\s\S]*releaseThreadMutationLatch\(threadMutationLatchOwner\)/)
  assert.match(archiveSource, /let archiveFailed = false/)
  assert.match(archiveSource, /archiveFailed = true/)
  assert.match(archiveSource, /finally \{[\s\S]*releaseThreadMutationLatch\(threadMutationLatchOwner\)[\s\S]*if \(archiveFailed\) threadRailRef\.value\?\.focusThread\?\.\(threadID\)/)
  assert.match(archiveSource, /activeAssistantThreadID\.value = ''[\s\S]*messages\.value = \[\]/)
  assert.match(archiveSource, /assistantThreads\.value = remaining[\s\S]*toast\('ok', 'Conversation archived\.'\)[\s\S]*if \(!wasActive\)/)
  assert.equal((archiveSource.match(/Conversation archived\./g) ?? []).length, 1)
  assert.match(archiveSource, /if \(!wasActive\) \{[\s\S]*focusThread\?\.\(activeAssistantThreadID\.value\)/)
  assert.match(archiveSource, /if \(requestIsCurrent\(\)\) \{[\s\S]*threadError\.value = e instanceof Error \? e\.message : String\(e\)[\s\S]*archiveFailed = true[\s\S]*\}\n    return[\s\S]*finally/)
  assert.match(archiveSource, /if \(!requestIsCurrent\(\) \|\| !contextIsCurrent\(\)\) return/)
  const replacementStart = archiveSource.indexOf('if (nextThreadID)')
  const replacementEnd = archiveSource.indexOf('\n  } else {', replacementStart)
  const replacementSource = archiveSource.slice(replacementStart, replacementEnd)
  assert.match(replacementSource, /if \(nextThreadID\)[\s\S]*await selectAssistantThread\(nextThreadID\)[\s\S]*if \(!contextIsCurrent\(\)\) return[\s\S]*focusThread\?\.\(nextThreadID\)/)
  assert.doesNotMatch(replacementSource, /createAssistantThread\(\)/)
  assert.match(archiveSource, /else \{[\s\S]*await createAssistantThread\(\)/)
})

test('releases stale thread operation latches and restores the active row focus', () => {
  assert.match(app, /function beginProjectOpenLatch\(\): number/)
  assert.match(app, /function beginThreadHistoryLatch\(\): number/)
  assert.match(app, /function beginConversationRefreshLatch\(\): number/)
  assert.match(app, /function beginThreadMutationLatch\(threadID = ''\): number/)
  assert.match(app, /function resetProjectOpenLatch\(\)[\s\S]*function resetThreadHistoryLatch\(\)[\s\S]*function resetConversationRefreshLatch\(\)[\s\S]*function resetThreadMutationLatch\(\)/)

  const openSource = app.slice(app.indexOf('async function openProject'), app.indexOf('\nasync function selectApprovalMode'))
  assert.match(openSource, /const projectOpenLatchOwner = beginProjectOpenLatch\(\)/)
  assert.match(openSource, /const threadHistoryLatchOwner = beginThreadHistoryLatch\(\)/)
  assert.match(openSource, /finally \{[\s\S]*releaseProjectOpenLatch\(projectOpenLatchOwner\)[\s\S]*releaseThreadHistoryLatch\(threadHistoryLatchOwner\)/)

  const refreshSource = app.slice(app.indexOf('async function refreshSelectedProjectConversation'), app.indexOf('\nfunction selectAssistantResponseMode'))
  assert.match(refreshSource, /const conversationRefreshLatchOwner = beginConversationRefreshLatch\(\)/)
  assert.match(refreshSource, /finally \{[\s\S]*releaseConversationRefreshLatch\(conversationRefreshLatchOwner\)/)

  const selectSource = app.slice(app.indexOf('async function selectAssistantThread'), app.indexOf('\nasync function createAssistantThread'))
  assert.match(selectSource, /async function selectAssistantThread\(threadID: string\): Promise<boolean>/)
  assert.match(selectSource, /threadRailRef\.value\?\.focusThread\?\.\(previousThreadID\)/)
  assert.match(selectSource, /finally \{[\s\S]*releaseThreadHistoryLatch\(threadHistoryLatchOwner, threadID\)/)

  const renameSource = app.slice(app.indexOf('async function renameAssistantThread'), app.indexOf('\n\nasync function commitAssistantThreadTitleRename'))
  assert.match(renameSource, /const requestGuard = beginProjectRequest\(\)/)
  assert.match(renameSource, /const renameRequestSerial = beginAssistantThreadRequest\(\)/)
  assert.match(renameSource, /const renameIsCurrent = \(\) =>[\s\S]*projectRequestIsCurrent\(requestGuard, projectName\)[\s\S]*renameRequestSerial === assistantThreadRequestSerial/)
  assert.match(renameSource, /if \(!renameIsCurrent\(\)\) return/)
  assert.match(renameSource, /if \(renameIsCurrent\(\)\) threadError\.value = e instanceof Error \? e\.message : String\(e\)/)
})

test('keeps active threads read and invalidates project requests after unmount', () => {
  assert.match(app, /let appComponentMounted = true/)
  assert.match(app, /function projectRequestIsCurrent\(guard: ProjectRequestGuard, projectName = ''\): boolean \{[\s\S]*return appComponentMounted &&[\s\S]*guard\.serial === projectLoadSerial/)
  assert.match(app, /onMounted\(\(\) => \{\n  appComponentMounted = true/)
  assert.match(app, /onBeforeUnmount\(\(\) => \{\n  appComponentMounted = false/)

  const unreadSource = app.slice(app.indexOf('function setThreadUnread'), app.indexOf('\n\nasync function archiveAssistantThread'))
  assert.match(unreadSource, /if \(threadID === activeAssistantThreadID\.value\) \{[\s\S]*markAssistantThreadRead\(scopeKey, thread\)[\s\S]*unreadAssistantThreadIDs\.value = unreadAssistantThreadIDs\.value\.filter[\s\S]*return/)

  const archiveSource = app.slice(app.indexOf('async function archiveAssistantThread'), app.indexOf('\nfunction assistantRunForMessage'))
  const replacementStart = archiveSource.indexOf('if \(nextThreadID\)')
  assert.ok(replacementStart >= 0)
  assert.match(archiveSource, /if \(!requestIsCurrent\(\) \|\| !contextIsCurrent\(\)\) return/)
  assert.match(archiveSource.slice(replacementStart), /await selectAssistantThread\(nextThreadID\)/)
})

test('does not apply or present create-thread responses after unmount', () => {
  const createSource = app.slice(app.indexOf('async function createAssistantThread'), app.indexOf('\nfunction beginAssistantThreadTitleRename'))
  assert.match(createSource, /const createIsCurrent = \(\) =>[\s\S]*appComponentMounted &&[\s\S]*assistantThreadLoadSerial === assistantThreadRequestSerial[\s\S]*selected\.value\?\.name === projectName/)
  assert.match(createSource, /const thread = await api\.createAssistantThread\(props\.ctx, projectName\)[\s\S]*if \(!createIsCurrent\(\)\) return/)
  assert.match(createSource, /catch \(e\) \{[\s\S]*if \(createIsCurrent\(\)\) \{[\s\S]*threadError\.value = e instanceof Error \? e\.message : String\(e\)/)

  const firstProjectSource = app.slice(app.indexOf('async function createProjectAndStartConversation'), app.indexOf('\nasync function openSettings'))
  assert.match(firstProjectSource, /const current = \(\) => appComponentMounted && pendingFirstProjectSubmission === submission[\s\S]*firstProjectSubmissionIsCurrent\(/)

  const sendSource = app.slice(app.indexOf('async function sendMessage'), app.indexOf('function cancelMessageStream'))
  assert.match(sendSource, /const sendContextFingerprint = projectContextFingerprint\(sendContext\)/)
  assert.match(sendSource, /const submissionIsCurrent = \(\) => appComponentMounted &&[\s\S]*submissionOwner\.generation === assistantSubmissionGeneration[\s\S]*sendContextFingerprint === projectContextFingerprint\(props\.ctx\)[\s\S]*selected\.value\?\.name === submissionOwner\.projectName[\s\S]*selected\.value\?\.uid \?\? ''\) === submissionOwner\.projectUID[\s\S]*activeAssistantThreadID\.value === submissionOwner\.threadID/)
  assert.match(app, /watch\(\s*activeAssistantThreadID,[\s\S]*current !== owner\.threadID[\s\S]*invalidateAssistantMessageSubmission\(\)/)
  const firstThreadStart = sendSource.indexOf('let thread = assistantThreads.value.find')
  const firstThreadEnd = sendSource.indexOf('\n      const canonical', firstThreadStart)
  assert.ok(firstThreadStart >= 0 && firstThreadEnd > firstThreadStart)
  assert.match(sendSource, /initialThreadReasoningEffortByModel = supportedThreadReasoningEffortSnapshot\([\s\S]*initialReasoningEffortSnapshot\([\s\S]*reasoningEffortsForDraftScope\(unthreadedReasoningEfforts\.value/)
  assert.match(sendSource.slice(firstThreadStart, firstThreadEnd), /thread = await api\.createAssistantThread\(\s*props\.ctx,\s*projectName,[\s\S]*initialThreadReasoningEffortByModel[\s\S]*if \(!submissionIsCurrent\(\)\) return false[\s\S]*assistantThreads\.value = \[thread, \.\.\.assistantThreads\.value\]/)
})

test('derives unread dots from persisted project-scoped update markers', () => {
  assert.match(app, /from '\.\/assistantThreadReadState'/)
  assert.match(app, /reconcileAssistantThreadReadState\([\s\S]*assistantThreadFocusStorageKey\(assistantThreadFocusScope\(projectName\)\)/)
  assert.doesNotMatch(app, /ThreadsWorkbench|builtin:threads|activeWorkbenchTab\?\.kind === 'threads'/)
})

test('keeps identity and thread controls in one workspace-wide title bar', () => {
  assert.match(app, /const activeAssistantThreadTitle = computed/)
  assert.match(app, /import AIConversationIdentity from '\.\/agentkit\/AIConversationIdentity\.vue'/)
  assert.match(app, /import ResourceBackLink from '\.\/portalkit\/ResourceBackLink\.vue'/)
  assert.match(app, /<header data-app-studio-titlebar/)
  assert.match(app, /aria-label="Toggle thread side panel"[\s\S]*@click="toggleThreadPanel"/)
  assert.match(app, /aria-label="Toggle thread side panel"[\s\S]*@pointerenter="previewThreadPanel"[\s\S]*@pointerleave="closeThreadPanelPreview"/)
  assert.match(app, /aria-label="Toggle thread side panel"[\s\S]*@focusin="previewThreadPanel"[\s\S]*@focusout="closeThreadPanelPreview"/)
  assert.match(app, /const threadRailExpanded = computed\(\(\) => threadRailRef\.value\?\.expanded \?\? true\)/)
  assert.match(app, /aria-label="Toggle thread side panel"[\s\S]*:aria-expanded="threadRailExpanded"[\s\S]*aria-controls="app-studio-thread-rail"/)
  assert.match(app, /function toggleThreadPanel\(event\?: MouseEvent\)[\s\S]*returnFocus[\s\S]*threadRailRef\.value\?\.toggle\?\.\(returnFocus\)/)
  assert.match(app, /focusThread\?: \(threadID: string\) => void/)
  assert.match(app, /function previewThreadPanel\(\)[\s\S]*threadRailRef\.value\?\.previewEnter\?\.\(\)/)
  assert.match(app, /function closeThreadPanelPreview\(\)[\s\S]*threadRailRef\.value\?\.previewLeave\?\.\(\)/)
  assert.match(app, /import AIConversationRenameDialog from '\.\/agentkit\/AIConversationRenameDialog\.vue'/)
  assert.match(app, /function renameAssistantThread\(threadID: string, title: string\)[\s\S]*api\.patchAssistantThread\(props\.ctx, projectName, threadID, \{ title: normalizedTitle \}\)/)
  assert.match(app, /const editingAssistantThreadID = ref\(''\)/)
  assert.match(app, /function beginAssistantThreadTitleRename\(threadID = activeAssistantThreadID\.value\)[\s\S]*editingAssistantThreadID\.value = thread\.id/)
  assert.match(app, /function commitAssistantThreadTitleRename\(title: string\)[\s\S]*const threadID = editingAssistantThreadID\.value[\s\S]*assistantThreads\.value\.find\(\(candidate\) => candidate\.id === threadID\)[\s\S]*renameAssistantThread\(threadID, normalizedTitle\)/)
  assert.match(app, /aria-label="Rename thread"[\s\S]*@click="beginAssistantThreadTitleRename\(\)"/)
  assert.match(app, /<AIConversationRenameDialog[\s\S]*:title="assistantThreadTitleDraft"[\s\S]*:busy="threadMutationBusy"[\s\S]*:error="threadError \|\| ''"[\s\S]*@save="commitAssistantThreadTitleRename"[\s\S]*@close="cancelAssistantThreadTitleRename"/)
  assert.doesNotMatch(app, /assistantThreadTitleInput|@blur="commitAssistantThreadTitleRename"/)
  assert.match(app, /v-if="threadError"[\s\S]*role="alert"[\s\S]*aria-live="assertive"[\s\S]*\{\{ threadError \}\}/)
  assert.match(app, /<span class="truncate">\{\{ activeAssistantThreadTitle \}\}<\/span>/)
  assert.match(app, /selected\.displayName \|\| selected\.name \|\| 'Project'/)
  assert.match(app, /selected\.repository\.name \|\| selected\.repository\.ref/)
  const titleBarStart = app.indexOf('<header data-app-studio-titlebar')
  const titleBarEnd = app.indexOf('</header>', titleBarStart)
  const railStart = app.indexOf('<AIConversationRail', titleBarEnd)
  assert.ok(titleBarStart >= 0 && titleBarEnd > titleBarStart && railStart > titleBarEnd)
  const titleBar = app.slice(titleBarStart, titleBarEnd)
  assert.match(titleBar, /<ResourceBackLink[\s\S]*icon-only[\s\S]*aria-label="Back to projects"[\s\S]*@back="props\.navigate\(''\)"/)
  assert.match(titleBar, /<AIConversationIdentity[\s\S]*<template #icon>[\s\S]*<template #title>[\s\S]*<template #context>/)
  assert.match(app, /const APP_STUDIO_ICON_URL = '\/ui\/providers\/app-studio\/icon\.svg'/)
  assert.match(titleBar, /<img :src="APP_STUDIO_ICON_URL" alt="" class="h-4 w-4 object-contain"/)
  assert.doesNotMatch(titleBar, /<MessageSquare/)
  assert.doesNotMatch(titleBar, /aria-label="New thread"|@click="createAssistantThread"/)
})

test('uses the thread side panel as the only conversation-switching surface', () => {
  assert.doesNotMatch(app, /Open conversation threads|openConversationThreads|Switch thread:/)
  assert.doesNotMatch(app, /Switch conversations, rename threads|builtInTab: 'threads'/)
})


function renameHarness(saveResult = true) {
  const names = new Set(['beginAssistantThreadTitleRename', 'cancelAssistantThreadTitleRename', 'commitAssistantThreadTitleRename', 'renameAssistantThreadFromRail'])
  const ast = ts.createSourceFile('App.ts', app.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1], ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const source = ast.statements.filter(node => ts.isFunctionDeclaration(node) && names.has(node.name?.text)).map(node => node.getText(ast)).join('\n')
  const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const state = {
    activeAssistantThreadID: { value: 'current' },
    assistantThreads: { value: [{ id: 'current', title: 'Current title' }, { id: 'other', title: 'Other title' }] },
    editingAssistantThreadID: { value: '' }, editingAssistantThreadTitle: { value: false },
    assistantThreadTitleDraft: { value: '' }, threadActionsDisabled: { value: false },
    threadMutationBusy: { value: false }, threadError: { value: null },
  }
  const calls = []
  const renameAssistantThread = async (...args) => { calls.push(args); return saveResult }
  const actions = new Function(...Object.keys(state), 'renameAssistantThread', `${outputText}; return { beginAssistantThreadTitleRename, commitAssistantThreadTitleRename, cancelAssistantThreadTitleRename, renameAssistantThreadFromRail }`)(...Object.values(state), renameAssistantThread)
  return { ...state, ...actions, calls }
}

test('rail rename opens a modal for the clicked thread without navigating', async () => {
  const h = renameHarness()
  h.renameAssistantThreadFromRail('other')
  assert.equal(h.activeAssistantThreadID.value, 'current')
  assert.equal(h.editingAssistantThreadID.value, 'other')
  assert.equal(h.assistantThreadTitleDraft.value, 'Other title')
  assert.equal(h.editingAssistantThreadTitle.value, true)
  await h.commitAssistantThreadTitleRename('  New title  ')
  assert.deepEqual(h.calls, [['other', 'New title']])
  assert.equal(h.editingAssistantThreadTitle.value, false)
})

test('failed modal saves retain the editor and target for retry', async () => {
  const h = renameHarness(false)
  h.beginAssistantThreadTitleRename()
  await h.commitAssistantThreadTitleRename('Updated title')
  assert.equal(h.editingAssistantThreadTitle.value, true)
  assert.equal(h.editingAssistantThreadID.value, 'current')
  await h.commitAssistantThreadTitleRename('Updated title')
  assert.equal(h.calls.length, 2)
  h.cancelAssistantThreadTitleRename()
  assert.equal(h.editingAssistantThreadTitle.value, false)
})

test('modal ignores blank and duplicate saves and cancels unchanged titles', async () => {
  const h = renameHarness()
  h.beginAssistantThreadTitleRename()
  await h.commitAssistantThreadTitleRename('  ')
  assert.equal(h.editingAssistantThreadTitle.value, true)
  h.threadMutationBusy.value = true
  await h.commitAssistantThreadTitleRename('Updated title')
  assert.deepEqual(h.calls, [])
  h.threadMutationBusy.value = false
  await h.commitAssistantThreadTitleRename('Current title')
  assert.equal(h.editingAssistantThreadTitle.value, false)
  assert.deepEqual(h.calls, [])
})

test('route changes dismiss the old project rename modal and discard its target', () => {
  const ast = ts.createSourceFile('App.ts', app.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)[1], ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const cancel = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'cancelAssistantThreadTitleRename')
  const watcher = ast.statements.find(node => ts.isExpressionStatement(node) && ts.isCallExpression(node.expression) && node.expression.expression.getText(ast) === 'watch' && node.expression.arguments[1]?.getText(ast) === 'cancelAssistantThreadTitleRename')
  assert.ok(watcher)
  const { outputText } = ts.transpileModule(cancel.getText(ast) + '\n' + watcher.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const context = ref({ subPath: '/project-a' })
  const editing = ref(true), target = ref('thread-a'), draft = ref('Unsaved title')
  const scope = effectScope()
  try {
    scope.run(() => new Function('props', 'editingAssistantThreadTitle', 'editingAssistantThreadID', 'assistantThreadTitleDraft', 'watch', outputText)(
      { get ctx() { return context.value } }, editing, target, draft, watch,
    ))
    context.value = { subPath: '/' }
    assert.equal(editing.value, false)
    assert.equal(target.value, '')
    assert.equal(draft.value, '')
  } finally { scope.stop() }
})
