import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import ts from 'typescript'
import { ref } from 'vue'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const threadReasoningSource = await readFile(new URL('./threadReasoning.ts', import.meta.url), 'utf8')
const { outputText: threadReasoningOutput } = ts.transpileModule(threadReasoningSource, {
  compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
})
const threadReasoning = await import(`data:text/javascript;base64,${Buffer.from(threadReasoningOutput).toString('base64')}`)
const script = app.slice(app.indexOf('>', app.indexOf('<script')) + 1, app.indexOf('</script>'))
const appAST = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
const compile = nodes => ts.transpileModule(nodes.map(node => node.getText(appAST)).join('\n'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022 },
}).outputText

function appFunctions(...names) {
  const wanted = new Set(names)
  const nodes = appAST.statements.filter(node => ts.isFunctionDeclaration(node) && wanted.has(node.name?.text))
  assert.equal(nodes.length, names.length)
  return compile(nodes)
}

function deferred() {
  let resolve
  let reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

function persistenceHarness(initialPreferences = { 'model-b': 'low' }) {
  const selected = ref({ name: 'project-a', uid: 'uid-a' })
  const props = { ctx: { tenant: 'workspace-a' } }
  const assistantThreads = ref([{ id: 'thread-a', reasoningEffortByModel: initialPreferences }])
  const threadReasoningPreferenceDrafts = ref({})
  const threadReasoningPreferenceErrors = ref({})
  const threadReasoningPreferencePending = ref({})
  const threadReasoningPreferencePendingCounts = new Map()
  const threadReasoningPreferenceSaves = new Map()
  const assistantThreadEventSequences = new Map()
  const assistantThreadReasoningEventSequences = new Map()
  const pending = []
  const calls = []
  let remoteThreads = null
  let listPromise = null
  let listCalls = 0
  const api = {
    patchAssistantThread: (...args) => {
      calls.push(args)
      const wait = deferred()
      pending.push(wait)
      return wait.promise
    },
    listAssistantThreads: async () => {
      listCalls += 1
      if (listPromise) return listPromise
      return remoteThreads ?? assistantThreads.value
    },
  }
  const appContextFingerprint = ctx => JSON.stringify(ctx)
  const threadReasoningPreferenceKey = (threadID, modelID) => JSON.stringify([appContextFingerprint(props.ctx), selected.value?.name ?? '', threadID, modelID])
  const threadReasoningPreferenceScopeKey = (threadID, projectName = selected.value?.name ?? '') => JSON.stringify([appContextFingerprint(props.ctx), projectName, threadID])
  const replaceAssistantThread = thread => {
    const exists = assistantThreads.value.some(candidate => candidate.id === thread.id)
    assistantThreads.value = exists
      ? assistantThreads.value.map(candidate => candidate.id === thread.id ? thread : candidate)
      : [thread, ...assistantThreads.value]
  }
  const code = appFunctions('threadReasoningPreferenceKey', 'threadReasoningPreferenceScopeKey', 'persistThreadReasoningEffort', 'ensureAssistantThreadPreferencesHydrated', 'saveAssistantReasoningEffortBeforeSend')
  const runtime = new Function('ref', 'appContextFingerprint', 'overlayThreadReasoningPreferenceDrafts', 'threadReasoningEffort', `
    const selected = arguments[4].selected;
    const props = arguments[4].props;
    const assistantThreads = arguments[4].assistantThreads;
    const threadReasoningPreferenceDrafts = arguments[4].threadReasoningPreferenceDrafts;
    const threadReasoningPreferenceErrors = arguments[4].threadReasoningPreferenceErrors;
    const threadReasoningPreferencePending = arguments[4].threadReasoningPreferencePending;
    const threadReasoningPreferencePendingCounts = arguments[4].threadReasoningPreferencePendingCounts;
    const threadReasoningPreferenceSaves = arguments[4].threadReasoningPreferenceSaves;
    const assistantThreadEventSequences = arguments[4].assistantThreadEventSequences;
    const assistantThreadReasoningEventSequences = arguments[4].assistantThreadReasoningEventSequences;
    let assistantReasoningPreferenceScopeGeneration = 0;
    const api = arguments[4].api;
    const replaceAssistantThread = arguments[4].replaceAssistantThread;
    ${code}
    return {
      threadReasoningPreferenceKey,
      threadReasoningPreferenceScopeKey,
      persistThreadReasoningEffort,
      ensureAssistantThreadPreferencesHydrated,
      saveAssistantReasoningEffortBeforeSend,
      advanceScopeGeneration: () => { assistantReasoningPreferenceScopeGeneration += 1; },
    };
  `)(ref, appContextFingerprint, threadReasoning.overlayThreadReasoningPreferenceDrafts, (preferences, modelID) => preferences?.[modelID] ?? '', {
    selected,
    props,
    assistantThreads,
    threadReasoningPreferenceDrafts,
    threadReasoningPreferenceErrors,
    threadReasoningPreferencePending,
    threadReasoningPreferencePendingCounts,
    assistantThreadEventSequences,
    assistantThreadReasoningEventSequences,
    threadReasoningPreferenceSaves,
    api,
    replaceAssistantThread,
  })
  return { ...runtime, selected, props, assistantThreads, threadReasoningPreferenceDrafts, threadReasoningPreferenceErrors, threadReasoningPreferencePending, threadReasoningPreferencePendingCounts, threadReasoningPreferenceSaves, assistantThreadEventSequences, assistantThreadReasoningEventSequences, calls, pending, setRemoteThreads: threads => { remoteThreads = threads }, setListPromise: promise => { listPromise = promise }, get listCalls() { return listCalls } }
}

test('thread preference writes serialize and canonical responses preserve pending local model edits', async () => {
  const h = persistenceHarness()
  const keyA = h.threadReasoningPreferenceKey('thread-a', 'model-a')
  const keyB = h.threadReasoningPreferenceKey('thread-a', 'model-b')
  h.threadReasoningPreferenceDrafts.value = {
    [keyA]: 'high',
    [keyB]: 'xhigh',
  }
  const first = h.persistThreadReasoningEffort('thread-a', 'model-a', 'high')
  const second = h.persistThreadReasoningEffort('thread-a', 'model-b', 'xhigh')
  await Promise.resolve()
  assert.deepEqual(h.calls.map(([, , threadID, body]) => [threadID, body]), [
    ['thread-a', { reasoningEffort: { modelID: 'model-a', effort: 'high' } }],
  ])
  assert.deepEqual(h.threadReasoningPreferencePending.value, {
    [keyA]: true,
    [keyB]: true,
  })

  const waitingSend = h.saveAssistantReasoningEffortBeforeSend('project-a', 'thread-a', 'model-a', 'high')
  let sendResolved = false
  void waitingSend.then(() => { sendResolved = true })
  await Promise.resolve()
  assert.equal(sendResolved, false, 'send admission waits for the keyed write')

  h.pending[0].resolve({ id: 'thread-a', reasoningEffortByModel: { 'model-a': 'high', 'model-b': 'remote-value' } })
  assert.equal(await first, true)
  assert.deepEqual(h.assistantThreads.value[0].reasoningEffortByModel, { 'model-a': 'high', 'model-b': 'xhigh' }, 'the canonical response is merged while an unsaved local draft stays visible')
  assert.equal(h.calls.length, 2, 'the next model write starts only after the first thread patch completes')
  h.pending[1].resolve({ id: 'thread-a', reasoningEffortByModel: { 'model-a': 'high', 'model-b': 'xhigh' } })
  assert.equal(await second, true)
  assert.equal(await waitingSend, true)
  assert.deepEqual(h.assistantThreads.value[0].reasoningEffortByModel, { 'model-a': 'high', 'model-b': 'xhigh' })
  assert.deepEqual(h.threadReasoningPreferencePending.value, {})
  assert.deepEqual(h.threadReasoningPreferenceErrors.value, {})
})

test('a stale cached map adopts other-model preferences from the canonical patch response', async () => {
  const h = persistenceHarness({ 'model-a': 'old' })
  const save = h.persistThreadReasoningEffort('thread-a', 'model-a', 'high')
  await Promise.resolve()
  h.pending[0].resolve({ id: 'thread-a', reasoningEffortByModel: { 'model-a': 'high', 'model-b': 'updated-in-another-tab' } })
  assert.equal(await save, true)
  assert.deepEqual(h.assistantThreads.value[0].reasoningEffortByModel, {
    'model-a': 'high',
    'model-b': 'updated-in-another-tab',
  })
})

test('a preference PATCH without a local projection keeps the complete response map after an event advances', async () => {
  const h = persistenceHarness()
  h.assistantThreads.value = []
  const save = h.persistThreadReasoningEffort('thread-a', 'model-a', 'high')
  await Promise.resolve()
  const scope = h.threadReasoningPreferenceScopeKey('thread-a', 'project-a')
  h.assistantThreadEventSequences.set(scope, 1)
  h.assistantThreadReasoningEventSequences.set(scope, 1)
  h.pending[0].resolve({
    id: 'thread-a',
    reasoningEffortByModel: { 'model-a': 'high', 'model-b': 'remote-high' },
  })

  assert.equal(await save, true)
  assert.deepEqual(h.assistantThreads.value[0].reasoningEffortByModel, {
    'model-a': 'high',
    'model-b': 'remote-high',
  })
})

test('a newer thread projection event cannot be rolled back by an older preference PATCH response', async () => {
  const h = persistenceHarness({ 'model-a': 'old' })
  const scope = h.threadReasoningPreferenceScopeKey('thread-a')
  const save = h.persistThreadReasoningEffort('thread-a', 'model-a', 'high')
  await Promise.resolve()
  h.assistantThreads.value[0] = {
    ...h.assistantThreads.value[0],
    title: 'Updated in another tab',
    updatedAt: '2026-10-17T12:00:02Z',
    reasoningEffortByModel: { 'model-a': 'remote-choice', 'model-b': 'remote-high' },
  }
  h.assistantThreadEventSequences.set(scope, 12)
  h.assistantThreadReasoningEventSequences.set(scope, 12)
  h.pending[0].resolve({
    id: 'thread-a',
    title: 'Older title',
    updatedAt: '2026-10-17T12:00:01Z',
    reasoningEffortByModel: { 'model-a': 'high' },
  })
  assert.equal(await save, true)
  assert.equal(h.assistantThreads.value[0].title, 'Updated in another tab')
  assert.deepEqual(h.assistantThreads.value[0].reasoningEffortByModel, {
    'model-a': 'remote-choice',
    'model-b': 'remote-high',
  })
})

test('a newer canonical PATCH response wins over an event that arrived while it was pending', async () => {
  const h = persistenceHarness({ 'model-a': 'old' })
  const scope = h.threadReasoningPreferenceScopeKey('thread-a')
  const save = h.persistThreadReasoningEffort('thread-a', 'model-a', 'high')
  await Promise.resolve()
  h.assistantThreads.value[0] = {
    ...h.assistantThreads.value[0],
    title: 'Concurrent title update',
    updatedAt: '2026-10-17T12:00:02Z',
    reasoningEffortByModel: { 'model-a': 'old', 'model-b': 'remote-high' },
  }
  h.assistantThreadEventSequences.set(scope, 12)
  h.assistantThreadReasoningEventSequences.set(scope, 12)
  h.pending[0].resolve({
    id: 'thread-a',
    title: 'Canonical title update',
    updatedAt: '2026-10-17T12:00:03Z',
    reasoningEffortByModel: { 'model-a': 'high', 'model-b': 'remote-high' },
  })

  assert.equal(await save, true)
  assert.equal(h.assistantThreads.value[0].title, 'Canonical title update')
  assert.deepEqual(h.assistantThreads.value[0].reasoningEffortByModel, {
    'model-a': 'high',
    'model-b': 'remote-high',
  })
})

test('a preference save from an invalidated project scope cannot mutate the new scope state', async () => {
  const h = persistenceHarness({ 'model-a': 'old' })
  const key = h.threadReasoningPreferenceKey('thread-a', 'model-a')
  h.threadReasoningPreferenceDrafts.value = { [key]: 'high' }
  const save = h.persistThreadReasoningEffort('thread-a', 'model-a', 'high')
  await Promise.resolve()
  h.advanceScopeGeneration()
  h.threadReasoningPreferenceDrafts.value = {}
  h.threadReasoningPreferenceErrors.value = {}
  h.threadReasoningPreferencePending.value = {}
  h.threadReasoningPreferencePendingCounts.clear()
  h.pending[0].resolve({ id: 'thread-a', reasoningEffortByModel: { 'model-a': 'high' } })
  assert.equal(await save, true)
  assert.deepEqual(h.assistantThreads.value[0].reasoningEffortByModel, { 'model-a': 'old' })
  assert.deepEqual(h.threadReasoningPreferenceDrafts.value, {})
  assert.deepEqual(h.threadReasoningPreferenceErrors.value, {})
  assert.deepEqual(h.threadReasoningPreferencePending.value, {})
})

test('a send waiting on an old preference queue cannot patch a replacement project scope', async () => {
  const h = persistenceHarness({ 'model-a': 'old' })
  const queuedSave = h.persistThreadReasoningEffort('thread-a', 'model-a', 'high')
  await Promise.resolve()
  const waitingSend = h.saveAssistantReasoningEffortBeforeSend('project-a', 'thread-a', 'model-a', 'medium')
  await Promise.resolve()

  h.advanceScopeGeneration()
  h.selected.value = { name: 'project-b', uid: 'uid-b' }
  h.assistantThreads.value = [{ id: 'thread-a', reasoningEffortByModel: { 'model-a': 'project-b-choice' } }]
  h.pending[0].resolve({ id: 'thread-a', reasoningEffortByModel: { 'model-a': 'high' } })

  assert.equal(await queuedSave, true)
  assert.equal(await waitingSend, false)
  assert.equal(h.calls.length, 1, 'the old send does not enqueue a preference write in the replacement project')
  assert.deepEqual(h.assistantThreads.value[0].reasoningEffortByModel, { 'model-a': 'project-b-choice' })
})

test('a restored thread without a cached projection hydrates remote model preferences and keeps local drafts', async () => {
  const h = persistenceHarness()
  h.assistantThreads.value = []
  h.setRemoteThreads([{
    id: 'thread-a',
    title: 'Restored conversation',
    reasoningEffortByModel: { 'model-a': 'remote-choice', 'model-b': 'remote-high' },
  }])
  const localModelKey = h.threadReasoningPreferenceKey('thread-a', 'model-a')
  h.threadReasoningPreferenceDrafts.value = { [localModelKey]: 'local-draft' }

  assert.equal(await h.ensureAssistantThreadPreferencesHydrated('project-a', 'thread-a'), true)
  assert.equal(h.listCalls, 1)
  assert.equal(h.assistantThreads.value[0].title, 'Restored conversation')
  assert.deepEqual(h.assistantThreads.value[0].reasoningEffortByModel, {
    'model-a': 'local-draft',
    'model-b': 'remote-high',
  }, 'hydration restores remote sibling keys without replacing the displayed local draft')

  h.setRemoteThreads([{ id: 'thread-a', reasoningEffortByModel: { 'model-a': 'other-tab', 'model-b': 'newer-remote' } }])
  assert.equal(await h.ensureAssistantThreadPreferencesHydrated('project-a', 'thread-a'), true)
  assert.equal(h.listCalls, 1, 'a cached server projection does not force a stale-tab refresh')
})

test('an unknown restored thread fails hydration instead of sending with an empty preference map', async () => {
  const h = persistenceHarness()
  h.assistantThreads.value = []
  h.setRemoteThreads([])
  assert.equal(await h.ensureAssistantThreadPreferencesHydrated('project-a', 'missing-thread'), false)
  assert.equal(h.listCalls, 1)
  assert.deepEqual(h.assistantThreads.value, [])
})

test('a reasoning event during restored-thread hydration cannot discard the fetched map without a local projection', async () => {
  const h = persistenceHarness()
  h.assistantThreads.value = []
  const remoteThread = {
    id: 'thread-a',
    title: 'Fetched conversation',
    reasoningEffortByModel: { 'model-a': 'high', 'model-b': 'xhigh' },
  }
  const list = deferred()
  h.setListPromise(list.promise)
  const hydration = h.ensureAssistantThreadPreferencesHydrated('project-a', 'thread-a')
  await Promise.resolve()

  const scope = h.threadReasoningPreferenceScopeKey('thread-a', 'project-a')
  h.assistantThreadEventSequences.set(scope, 2)
  h.assistantThreadReasoningEventSequences.set(scope, 2)
  list.resolve([remoteThread])

  assert.equal(await hydration, true)
  assert.deepEqual(h.assistantThreads.value[0], remoteThread)
  assert.deepEqual(h.assistantThreads.value[0].reasoningEffortByModel, {
    'model-a': 'high',
    'model-b': 'xhigh',
  })
})

test('a failed keyed preference patch remains visible and blocks admission until retry succeeds', async () => {
  const h = persistenceHarness()
  const key = h.threadReasoningPreferenceKey('thread-a', 'model-a')
  h.threadReasoningPreferenceDrafts.value = { [key]: 'high' }
  const save = h.persistThreadReasoningEffort('thread-a', 'model-a', 'high')
  await Promise.resolve()
  assert.deepEqual(h.calls[0][3], { reasoningEffort: { modelID: 'model-a', effort: 'high' } })
  h.pending[0].reject(new Error('offline'))
  assert.equal(await save, false)
  assert.equal(h.threadReasoningPreferenceErrors.value[key], 'offline')
  assert.deepEqual(h.threadReasoningPreferencePending.value, {})
  assert.equal(h.threadReasoningPreferenceDrafts.value[key], 'high')
})

test('empty effort is serialized for turn and review, and the first send awaits persistence before POST', async () => {
  const apiSource = await readFile(new URL('./api.ts', import.meta.url), 'utf8')
  const apiAST = ts.createSourceFile('api.ts', apiSource, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const apiObject = apiAST.statements.find(node => ts.isVariableStatement(node) && node.declarationList.declarations.some(decl => decl.name.getText(apiAST) === 'api'))
  assert.ok(apiObject)
  const methods = apiObject.declarationList.declarations[0].initializer.properties.filter(node =>
    ts.isMethodDeclaration(node) && ['startAssistantTurn', 'startAssistantReview'].includes(node.name.getText(apiAST)),
  )
  assert.equal(methods.length, 2)
  const apiCode = ts.transpileModule(`const api = { ${methods.map(node => node.getText(apiAST)).join(',\n')} };`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText
  const calls = []
  const callsAPI = new Function('sessionRequest', `${apiCode}; return api;`)(async (...args) => {
    calls.push(args)
    return args[5]
  })
  const turnBody = { content: 'start', clientUserMessageID: 'request-a', modelID: 'model-a', reasoningEffort: '', collaborationMode: 'default' }
  const reviewBody = { target: { type: 'current_workspace', instructions: 'review' }, clientUserMessageID: 'request-b', modelID: 'model-a', reasoningEffort: '' }
  await callsAPI.startAssistantTurn(null, 'project-a', 'thread-a', turnBody)
  await callsAPI.startAssistantReview(null, 'project-a', 'thread-a', reviewBody)
  assert.equal(calls[0][5].reasoningEffort, '')
  assert.equal(calls[1][5].reasoningEffort, '')
  assert.equal(calls[0][3], 'turn')
  assert.equal(calls[1][3], 'review')

  const send = appAST.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'sendMessage')?.getText(appAST) ?? ''
  const preferenceSaveIndex = send.indexOf('await saveAssistantReasoningEffortBeforeSend(')
  assert.ok(preferenceSaveIndex >= 0 && preferenceSaveIndex < send.indexOf('prompt.value = \'\'', preferenceSaveIndex))
  assert.match(send, /reasoningEffort: payload\.reasoningEffort \?\? ''/)
  assert.match(send, /if \(!steeringActiveRun && assistantReasoningPreferenceError\.value\)/)
  assert.match(app, /function toggleAssistantProgress[\s\S]*loadAssistantTurnDetail\(message\)/)
  assert.match(app, /data-testid="assistant-run-effective-settings"/)
  assert.match(app, /Conversation preference or model default/)
  assert.match(app, /Resolved effort:/)
  assert.match(app, /actualReasoningLabel\(assistantTurnReasoning\(message\)\?\.effectiveEffort\)/)
  assert.doesNotMatch(app, /Saved default:/)
})

test('model edits persist reasoning in a new revision and archive the previous snapshot', async () => {
  const registry = await readFile(new URL('./llmRegistry.ts', import.meta.url), 'utf8')
  assert.match(registry, /reasoningEffort: model\.reasoningEffort \|\| undefined/)
  assert.match(registry, /revisionID: newRevisionID\(\)[\s\S]*reasoningEffort: body\.reasoningEffort === undefined \? current\.reasoningEffort : body\.reasoningEffort \|\| undefined/)
  assert.match(registry, /models\.push\(\{ \.\.\.current, archived: true \}\)/)
})
