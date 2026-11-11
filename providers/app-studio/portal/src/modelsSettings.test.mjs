import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import ts from 'typescript'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { computed, createSSRApp, nextTick, ref, watch } from 'vue'
import { renderToString } from 'vue/server-renderer'

const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/railgrid-vite-app-studio-models',
  configFile: false,
  plugins: [vue()],
  server: { middlewareMode: true, hmr: { server: new EventEmitter() } },
})
const { default: ModelsSettings } = await vite.ssrLoadModule('/src/ModelsSettings.vue')
test.after(async () => vite.close())

const baseProps = {
  ctx: null,
  connectionRef: '',
  connections: [],
  settings: null,
  loading: false,
  loadError: null,
  saving: false,
  status: null,
  actionError: null,
  editorOpen: false,
  creationRoute: false,
  editingModelID: null,
  name: '',
  model: 'gpt-5.4',
  nameError: '',
  modelError: '',
  modelHint: 'Use the provider model ID.',
  discoveredModels: [],
  discoveryLoading: false,
  discoveryError: null,
  testing: false,
  testStatus: null,
  testError: null,
  requireConnectionTest: false,
  nameOnlyEdit: false,
}

async function render(props = {}) {
  return renderToString(createSSRApp(ModelsSettings, { ...baseProps, ...props }))
}

const savedConnection = {
  name: 'openai-main',
  displayName: 'OpenAI main',
  uid: 'connection-uid',
  resourceVersion: '12',
  generation: 1,
  observedGeneration: 1,
  provider: 'openai',
  baseURL: 'https://api.openai.com/v1',
  connectionFingerprint: 'fingerprint',
  credentialMode: 'api-key',
  secretName: 'key-version',
  ready: true,
  secretResolved: true,
  reachable: true,
  secretRef: { name: 'key-version' },
  secretKey: 'apiKey',
  secretUID: 'secret-uid',
  secretResourceVersion: '4',
  models: ['gpt-5.4'],
}

test('retains initial and stale model errors with one disabled busy retry action', async () => {
  for (const settings of [null, {
    provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', model: 'gpt-5.4',
    configured: true, defaultModelID: 'main',
    models: [{ id: 'main', name: 'Main model', provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', model: 'gpt-5.4', configured: true, default: true }],
  }]) {
    const html = await render({ settings, loading: true, loadError: 'Could not load model settings.' })
    assert.match(html, /Could not load model settings\./)
    assert.match(html, /<button[^>]*class="k-inline-notification__action"[^>]*disabled[^>]*aria-label="Retrying models…"[^>]*aria-busy="true"[^>]*>Retrying models…<\/button>/)
    assert.doesNotMatch(html, />Loading models…</)
    if (settings) assert.match(html, /aria-label="Edit Main model"/)
  }
})

async function modelSettingsLoader() {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const script = app.slice(app.indexOf('>', app.indexOf('<script')) + 1, app.indexOf('</script>'))
  const ast = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const statements = ast.statements.filter(node =>
    (ts.isFunctionDeclaration(node) && ['loadLLMSettings', 'invalidateLLMSettingsLoad'].includes(node.name?.text)) ||
    node.getText(ast).startsWith('watch(selectedNameFromPath,'),
  )
  assert.equal(statements.length, 3)
  const { outputText } = ts.transpileModule(statements.map(node => node.getText(ast)).join('\n'), {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  })
  const createState = new Function('ref', 'watch', 'computed', 'projectName', `
    const props = { ctx: { workspace: 'first' } };
    const selectedNameFromPath = ref(projectName);
    const pendingFirstProjectSubmission = null;
    const hasProjectRequestTransport = () => true;
    let llmSettingsLoadSerial = 0, llmSettingsLoadProject = '';
    const llmSettings = ref(null), llmSettingsLoading = ref(false), llmActionError = ref(null);
    const llmSettingsError = ref('Previous read failed');
    const llmConfigured = computed(() => llmSettings.value?.models.some(model => model.configured));
    const reads = [], timers = [];
    let initializationRetries = 0;
    const api = { getLLMSettings: ctx => new Promise((resolve, reject) => {
      reads.push({ workspace: ctx.workspace, resolve, reject });
    }) };
    const setTimeout = resolve => timers.push(resolve);
    const applyLLMSettings = settings => { llmSettings.value = settings; };
    const handleProjectAPIInitializing = () => { initializationRetries += 1; return true; };
    ${outputText}
    return {
      loadLLMSettings, invalidateLLMSettingsLoad, llmSettings, llmSettingsLoading, llmSettingsError,
      reads, timers, selectedNameFromPath, props,
      get initializationRetries() { return initializationRetries; },
    };
  `)
  return { app, createState: (projectName = '') => createState(ref, watch, computed, projectName) }
}

test('a pending model retry keeps its previous error until the read succeeds', async () => {
  const { createState } = await modelSettingsLoader()
  const state = createState()
  const pending = state.loadLLMSettings()
  assert.equal(state.llmSettingsLoading.value, true)
  assert.equal(state.llmSettingsError.value, 'Previous read failed')
  state.reads.shift().resolve({ models: [] })
  await pending
  assert.equal(state.llmSettingsLoading.value, false)
  assert.equal(state.llmSettingsError.value, null)
  assert.deepEqual(state.llmSettings.value, { models: [] })
})

const pendingModel = { id: 'new-model', configured: false }
const readyModel = { id: 'ready-model', configured: true }

for (const existingModels of [[], [readyModel]]) {
  test(`project model settings retry a saved binding with ${existingModels.length} other ready models`, async () => {
    const { createState } = await modelSettingsLoader()
    const state = createState('existing-project')
    state.llmSettings.value = { models: existingModels }
    const pending = state.loadLLMSettings()
    assert.equal(state.llmSettingsLoading.value, existingModels.length === 0)
    state.reads.shift().resolve({ models: [...existingModels, pendingModel] })
    await nextTick()
    assert.equal(state.llmSettingsLoading.value, existingModels.length === 0)
    assert.equal(state.timers.length, 1)
    state.timers.shift()()
    await nextTick()
    state.reads.shift().resolve({ models: [...existingModels, { ...pendingModel, configured: true }] })
    await pending
    assert.equal(state.llmSettingsLoading.value, false)
    assert.deepEqual(state.llmSettings.value.models.filter(model => model.configured).map(model => model.id),
      [...existingModels.map(model => model.id), pendingModel.id])
  })
}

async function startInFlightModelRetry(state) {
  const completion = state.loadLLMSettings()
  state.reads.shift().resolve({ models: [pendingModel] })
  await nextTick()
  state.timers.shift()()
  await nextTick()
  return { completion, retry: state.reads.shift() }
}

test('switching projects discards a late retry without releasing the new project load', async () => {
  const { createState } = await modelSettingsLoader()
  const state = createState('project-a')
  const { completion, retry } = await startInFlightModelRetry(state)
  state.selectedNameFromPath.value = 'project-b'
  await nextTick()
  assert.equal(state.reads.length, 1)
  retry.resolve({ models: [readyModel] })
  await completion
  assert.equal(state.llmSettingsLoading.value, true)
  assert.deepEqual(state.llmSettings.value.models, [pendingModel])
  state.reads.shift().resolve({ models: [] })
  await nextTick()
  assert.equal(state.llmSettingsLoading.value, false)
  assert.deepEqual(state.llmSettings.value.models, [])
})

test('leaving a project releases loading and a late retry cannot overwrite a model mutation', async () => {
  const { createState } = await modelSettingsLoader()
  const state = createState('project-a')
  const { completion, retry } = await startInFlightModelRetry(state)
  state.selectedNameFromPath.value = ''
  await nextTick()
  assert.equal(state.llmSettingsLoading.value, false)
  state.llmSettings.value = { models: [] }
  retry.resolve({ models: [readyModel] })
  await completion
  assert.deepEqual(state.llmSettings.value.models, [])
  assert.equal(state.timers.length, 0)
})

test('workspace invalidation fences a retry when the destination project has the same name', async () => {
  const { app, createState } = await modelSettingsLoader()
  assert.match(app, /function invalidateProjectContextState\(\) \{[\s\S]*?invalidateLLMSettingsLoad\(\)/)
  assert.match(app, /invalidateProjectContextState\(\)\s+if \(isCreateModelRoute.value\)[\s\S]*?void loadLLMSettings\(\)/)
  const state = createState('same-project')
  const { completion, retry } = await startInFlightModelRetry(state)
  assert.equal(retry.workspace, 'first')
  state.props.ctx = { workspace: 'second' }
  state.invalidateLLMSettingsLoad()
  state.llmSettings.value = null
  const replacement = state.loadLLMSettings()
  retry.resolve({ models: [readyModel] })
  await completion
  assert.equal(state.llmSettings.value, null)
  const second = state.reads.shift()
  assert.equal(second.workspace, 'second')
  second.resolve({ models: [pendingModel] })
  await nextTick()
  state.timers.shift()()
  await nextTick()
  state.reads.shift().resolve({ models: [{ ...pendingModel, configured: true }] })
  await replacement
  assert.equal(state.llmSettings.value.models[0].configured, true)
})

test('workspace remount prevents an old retry error from restarting initialization', async () => {
  const { app, createState } = await modelSettingsLoader()
  assert.match(app, /onBeforeUnmount\(\(\) => \{\s*appComponentMounted = false\s*invalidateLLMSettingsLoad\(\)/)
  const state = createState('project-a')
  const { completion, retry } = await startInFlightModelRetry(state)
  state.invalidateLLMSettingsLoad()
  retry.reject(new Error('Workspace still initializing'))
  await completion
  assert.equal(state.initializationRetries, 0)
  assert.equal(state.timers.length, 0)
})

test('selecting a saved provider clears stale verification and preserves the model draft', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const script = app.slice(app.indexOf('>', app.indexOf('<script')) + 1, app.indexOf('</script>'))
  const ast = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const names = new Set(['selectLLMModelConnection', 'clearLLMDiscovery'])
  const functions = ast.statements.filter(node => ts.isFunctionDeclaration(node) && names.has(node.name?.text))
  assert.equal(functions.length, names.size)
  const { outputText } = ts.transpileModule(functions.map(node => node.getText(ast)).join('\n'), { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const state = new Function('ref', `
    const llmConnections = ref([${JSON.stringify(savedConnection)}]);
    const llmConnectionStatusPollRequested = ref(false);
    const llmBindingConnectionRef = ref('');
    const llmBindingConnectionInfo = ref(null);
    const llmProvider = ref('openai-compatible');
    const llmCredentialMode = ref('api-key');
    const llmBaseURL = ref('https://old-provider.example/v1');
    const llmEditingModelID = ref('legacy-model');
    const llmName = ref('Saved model');
    const llmModel = ref('custom-model');
    const llmReasoningEffort = ref('high');
    const llmTestError = ref('old test error');
    const llmTestStatus = ref('old test success');
    const llmTestedFingerprint = ref('old fingerprint');
    let llmDiscoverySerial = 0;
    const llmDiscoveryLoading = ref(true);
    const llmDiscoveredModels = ref([{ id: 'original-model' }]);
    const llmDiscoveryComplete = ref(true);
    const llmDiscoveryError = ref(null);
    const isGoogleCloudBaseURL = () => false;
    const normalizeLLMBaseURLInput = (_provider, baseURL) => baseURL;
    ${outputText}
    return { selectLLMModelConnection, llmBindingConnectionRef, llmBindingConnectionInfo, llmProvider,
      llmBaseURL, llmName, llmModel, llmReasoningEffort, llmTestError, llmTestStatus,
      llmTestedFingerprint, llmDiscoveredModels, llmDiscoveryComplete, llmDiscoveryLoading, llmConnectionStatusPollRequested };
  `)(ref)

  state.selectLLMModelConnection(savedConnection.name)
  assert.equal(state.llmBindingConnectionRef.value, savedConnection.name)
  assert.deepEqual(state.llmBindingConnectionInfo.value, savedConnection)
  assert.equal(state.llmProvider.value, savedConnection.provider)
  assert.equal(state.llmBaseURL.value, savedConnection.baseURL)
  assert.equal(state.llmConnectionStatusPollRequested.value, true)
  assert.equal(state.llmTestStatus.value, null)
  assert.equal(state.llmTestedFingerprint.value, '')
  assert.equal(state.llmTestError.value, null)
  assert.deepEqual(state.llmDiscoveredModels.value, [])
  assert.equal(state.llmDiscoveryComplete.value, false)
  assert.equal(state.llmDiscoveryLoading.value, false)
  assert.equal(state.llmName.value, 'Saved model')
  assert.equal(state.llmModel.value, 'custom-model')
  assert.equal(state.llmReasoningEffort.value, 'high')
})

test('model binding snapshots wait for an authoritative provider status and reject stale key identities', async () => {
  const registry = await readFile(new URL('./llmRegistry.ts', import.meta.url), 'utf8')
  const ast = ts.createSourceFile('llmRegistry.ts', registry, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const declaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'connectionSnapshotFor')
  assert.ok(declaration)
  const { outputText } = ts.transpileModule(declaration.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const snapshotFor = new Function('LLMRegistryError', 'MODEL_PROVIDER_CONNECTION_RESOURCE', 'CREDENTIAL_KEY', 'GENERATED_SECRET_ANNOTATION', `${outputText}; return connectionSnapshotFor`)(
    class LLMRegistryError extends Error {},
    { group: 'ai.railgrid.ai', version: 'v1alpha1', resource: 'modelproviderconnections' },
    'apiKey',
    'ai.railgrid.ai/generated-secret-id',
  )
  const candidate = {
    name: 'openai-main', uid: 'connection-uid', generation: 2, provider: 'openai',
    baseURL: 'https://api.openai.com/v1', secretName: 'key-v2', secretKey: 'apiKey',
    secretUID: 'secret-v2-uid', secretResourceVersion: '9', connectionFingerprint: 'current-fingerprint', generatedSecretID: 'ab'.repeat(16),
  }
  const object = status => ({
    metadata: { name: candidate.name, uid: candidate.uid, generation: 2, annotations: { 'ai.railgrid.ai/generated-secret-id': candidate.generatedSecretID } },
    spec: { provider: candidate.provider, baseURL: candidate.baseURL, secretRef: { name: candidate.secretName }, secretKey: candidate.secretKey },
    status,
  })
  let current = object({ observedGeneration: 1, connectionFingerprint: 'stale-fingerprint', secretUID: 'old-secret', secretResourceVersion: '8' })
  let reads = 0
  const client = { get: async () => { reads += 1; return current } }
  const pending = snapshotFor(client, candidate.name, candidate)
  await new Promise(resolve => setTimeout(resolve, 300))
  current = object({ observedGeneration: 2 })
  await new Promise(resolve => setTimeout(resolve, 300))
  const admitted = {
    connectionRef: candidate.name, connectionUID: candidate.uid, provider: 'openai-compatible',
    baseURL: 'https://api.openai.com/v1', secretRef: { name: candidate.secretName }, secretKey: 'apiKey',
    secretUID: candidate.secretUID, secretResourceVersion: candidate.secretResourceVersion,
    connectionFingerprint: candidate.connectionFingerprint, generatedSecretID: candidate.generatedSecretID,
  }
  current = object({ observedGeneration: 2, connectionFingerprint: 'current-fingerprint', activeSnapshot: admitted, secretUID: candidate.secretUID, secretResourceVersion: candidate.secretResourceVersion })
  const snapshot = await pending
  assert.ok(reads >= 3, 'waits for the controller to observe generation and key identity')
  assert.equal(snapshot.connectionFingerprint, 'current-fingerprint')
  assert.equal(snapshot.secretUID, 'secret-v2-uid')
  assert.equal(snapshot.secretResourceVersion, '9')
  assert.equal(snapshot.generatedSecretID, candidate.generatedSecretID)
  assert.equal('apiKey' in snapshot, false, 'the immutable model snapshot contains no credential bytes')
  assert.deepEqual(snapshot, admitted, 'captures the exact controller-admitted transport')
  assert.notEqual(snapshot, admitted, 'keeps an independent revision snapshot')
  current.spec.baseURL += '/'
  const normalized = await snapshotFor({ get: async () => current }, candidate.name)
  assert.deepEqual(normalized, admitted, 'trailing slash in spec does not change admitted transport')


  current = object({ observedGeneration: 2, connectionFingerprint: 'different-key-fingerprint', secretUID: 'other-secret', secretResourceVersion: '10' })
  await assert.rejects(snapshotFor({ get: async () => current }, candidate.name, candidate), /saved provider key changed/)

  const deleting = object({ observedGeneration: 2 })
  deleting.metadata.deletionTimestamp = '2026-11-03T12:00:00Z'
  let deletingReads = 0
  await assert.rejects(snapshotFor({ get: async () => { deletingReads += 1; return deleting } }, candidate.name, candidate), /provider connection is being deleted/)
  assert.equal(deletingReads, 1, 'does not poll a connection after Kubernetes has started deleting it')
})

test('connection edits tolerate status-only resourceVersion changes but reject changed provider authority', async () => {
  const registry = await readFile(new URL('./llmRegistry.ts', import.meta.url), 'utf8')
  const ast = ts.createSourceFile('llmRegistry.ts', registry, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const declaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'expectedConnectionMatchesLatest')
  assert.ok(declaration)
  const { outputText } = ts.transpileModule(declaration.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const matches = new Function('CONNECTION_DISPLAY_NAME_ANNOTATION', 'CREDENTIAL_KEY', `${outputText}; return expectedConnectionMatchesLatest`)('ai.railgrid.ai/display-name', 'apiKey')
  const expected = {
    name: 'shared', uid: 'connection-uid', resourceVersion: '12', generation: 4, displayName: 'Shared provider',
    provider: 'openai', baseURL: 'https://api.openai.com/v1', secretName: 'key-v2', secretKey: 'apiKey',
    connectionFingerprint: 'fingerprint-a', secretUID: 'secret-uid-a', secretResourceVersion: '9',
  }
  const latest = {
    metadata: { name: 'shared', uid: 'connection-uid', resourceVersion: '19', generation: 4, annotations: { 'ai.railgrid.ai/display-name': 'Shared provider' } },
    spec: { provider: 'openai', baseURL: 'https://api.openai.com/v1', secretRef: { name: 'key-v2' }, secretKey: 'apiKey' },
    status: { connectionFingerprint: 'fingerprint-a', secretUID: 'secret-uid-a', secretResourceVersion: '9' },
  }
  assert.equal(matches(expected, latest), true, 'a status-only probe update advances resourceVersion but leaves the captured connection authority intact')
  assert.equal(matches(expected, { ...latest, metadata: { ...latest.metadata, generation: 5 }, spec: { ...latest.spec, baseURL: 'https://other.example/v1' } }), false)
  assert.equal(matches(expected, { ...latest, status: { ...latest.status, connectionFingerprint: 'fingerprint-b', secretUID: 'secret-uid-b' } }), false)
  assert.match(registry, /const resourceVersion = existing\.metadata\.resourceVersion/)
})

test('display-name-only model edits save without an inference request', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const script = app.slice(app.indexOf('>', app.indexOf('<script')) + 1, app.indexOf('</script>'))
  const ast = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const nameOnlyStatement = ast.statements.find(node => ts.isVariableStatement(node)
    && node.declarationList.declarations.some(declaration => declaration.name.getText(ast) === 'llmNameOnlyEdit'))
  const verifyDeclaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'verifyAndSaveLLMSettings')
  assert.ok(nameOnlyStatement)
  assert.ok(verifyDeclaration)
  const { outputText: nameOnlySource } = ts.transpileModule(nameOnlyStatement.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const saved = {
    id: 'main', name: 'Main model', connectionRef: savedConnection.name,
    connectionFingerprint: savedConnection.connectionFingerprint,
    provider: 'openai', baseURL: savedConnection.baseURL, model: 'gpt-5.4', reasoningEffort: 'medium',
  }
  const values = {
    llmSettings: { value: { models: [saved] } },
    llmEditingModelID: { value: 'main' },
    llmName: { value: 'Main model renamed' },
    llmBindingConnectionRef: { value: savedConnection.name },
    llmBindingConnectionInfo: { value: savedConnection },
    llmModel: { value: 'gpt-5.4' },
    llmReasoningEffort: { value: 'medium' },
    llmProvider: { value: 'openai' },
    llmBaseURL: { value: savedConnection.baseURL },
    llmCredentialMode: { value: 'api-key' },
  }
  const getNameOnly = new Function('computed', ...Object.keys(values), `${nameOnlySource}; return () => llmNameOnlyEdit.value`)(
    getter => ({ get value() { return getter() } }), ...Object.values(values),
  )
  assert.equal(getNameOnly(), true)
  values.llmBindingConnectionInfo.value = null
  assert.equal(getNameOnly(), true, 'a bound-model label change does not depend on connection metadata access')
  values.llmBindingConnectionInfo.value = savedConnection
  values.llmModel.value = 'gpt-5.5'
  assert.equal(getNameOnly(), false, 'a model change still requires verification')
  values.llmModel.value = 'gpt-5.4'
  values.llmBindingConnectionRef.value = 'different-provider'
  assert.equal(getNameOnly(), false, 'a provider migration still requires verification')

  const { outputText: verifySource } = ts.transpileModule(verifyDeclaration.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  let inferenceRequests = 0
  let saves = 0
  const verifyAndSave = new Function('llmConnectionTestRequired', 'llmConnectionTested', 'testLLMConnection', 'saveLLMSettings', `${verifySource}; return verifyAndSaveLLMSettings`)(
    { value: false }, { value: false }, async () => { inferenceRequests += 1; return true }, async () => { saves += 1 },
  )
  await verifyAndSave()
  assert.equal(inferenceRequests, 0)
  assert.equal(saves, 1)
})

test('a bound-model rename patches only the Studio label without connection or Secret reads', async () => {
  const registry = await readFile(new URL('./llmRegistry.ts', import.meta.url), 'utf8')
  const ast = ts.createSourceFile('llmRegistry.ts', registry, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const names = new Set(['writeRegistry', 'patchLLMModel'])
  const declarations = ast.statements.filter(node => ts.isFunctionDeclaration(node) && names.has(node.name?.text))
  assert.equal(declarations.length, names.size)
  const source = declarations.map(node => node.getText(ast).replace(/^export\s+/, '')).join('\n')
  const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const paths = []
  const writes = []
  const snapshotCalls = []
  const credentialWrites = []
  const model = {
    id: 'main', revisionID: 'revision-a', name: 'Main model', connectionRef: 'shared', model: 'gpt-5.4', reasoningEffort: 'medium',
    connectionSnapshot: {
      connectionRef: 'shared', connectionUID: 'connection-uid', provider: 'openai', baseURL: 'https://api.openai.com/v1',
      secretRef: { name: 'key-version' }, secretKey: 'apiKey', secretUID: 'secret-uid', secretResourceVersion: '4',
      connectionFingerprint: 'fingerprint-a',
    },
  }
  const studio = {
    metadata: { name: 'studio', uid: 'studio-uid', resourceVersion: '17' },
    spec: { llm: { defaultModel: 'main', models: [model] } },
    status: { models: [{ id: 'main', configured: true }] },
  }
  const resource = { group: 'ai.railgrid.ai', version: 'v1alpha1', resource: 'studios' }
  const client = {
    get: async (ref, name) => { paths.push(`${ref.resource}/${name}`); return studio },
    patch: async (ref, name, body, options) => { writes.push({ path: `${ref.resource}/${name}`, body, options }); return {} },
  }
  const patchModel = new Function(
    'STUDIO_RESOURCE', 'STUDIO_NAME', 'MAX_ACTIVE_MODELS', 'MAX_REVISIONS', 'LLMRegistryError', 'isKubeConflict',
    'kube', 'readStudio', 'toSettings', 'connectionSnapshotFor', 'newRevisionID', 'writeCredential',
    `${outputText}; return patchLLMModel`,
  )(
    resource, 'studio', 20, 200, class LLMRegistryError extends Error {}, () => false,
    () => client, async currentClient => currentClient.get(resource, 'studio'),
    current => ({ models: current.spec.llm.models.filter(entry => !entry.archived) }),
    async () => { snapshotCalls.push('connection'); throw new Error('connection read must not happen') },
    () => { throw new Error('name-only edits must keep the revision identity') },
    async () => { credentialWrites.push('secret'); throw new Error('Secret write must not happen') },
  )

  const result = await patchModel(null, 'main', {
    name: 'Main model renamed', expectedName: 'Main model', expectedRevisionID: 'revision-a',
    connectionRef: 'shared', model: 'gpt-5.4', reasoningEffort: 'medium',
  })
  assert.deepEqual(paths, ['studios/studio'])
  assert.equal(writes.length, 1)
  assert.equal(writes[0].path, 'studios/studio')
  assert.equal(writes[0].body.metadata.uid, 'studio-uid')
  assert.equal(writes[0].body.metadata.resourceVersion, '17')
  assert.equal(result.models[0].name, 'Main model renamed')
  assert.equal(result.models[0].revisionID, 'revision-a')
  assert.equal(result.models[0].connectionSnapshot, model.connectionSnapshot)
  assert.deepEqual(snapshotCalls, [])
  assert.deepEqual(credentialWrites, [])

  studio.spec.llm.models = [{ ...model, revisionID: 'revision-b', model: 'gpt-5.5' }]
  await assert.rejects(patchModel(null, 'main', {
    name: 'Main model renamed again', expectedName: 'Main model', expectedRevisionID: 'revision-a',
    connectionRef: 'shared', model: 'gpt-5.4', reasoningEffort: 'medium',
  }), /model changed while you were editing it/)
  assert.equal(writes.length, 1, 'a stale editor cannot overwrite a controller or sibling model revision')
  assert.deepEqual(snapshotCalls, [])
  assert.deepEqual(credentialWrites, [])
})

test('a verified provider fingerprint replaces the create acknowledgement before model testing', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const script = app.slice(app.indexOf('>', app.indexOf('<script')) + 1, app.indexOf('</script>'))
  const ast = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const names = ['isLLMConnectionSnapshotReady', 'syncSelectedLLMProviderConnection']
  const declarations = names.map(name => {
    const declaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name)
    assert.ok(declaration, `${name} exists`)
    return declaration.getText(ast)
  }).join('\n')
  const { outputText } = ts.transpileModule(declarations, { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const sync = new Function('llmConnections', 'llmBindingConnectionRef', 'llmBindingConnectionInfo', `${outputText}; return syncSelectedLLMProviderConnection`)
  const acknowledgement = { ...savedConnection, connectionFingerprint: undefined }
  const connections = ref([acknowledgement])
  const bindingRef = ref(savedConnection.name)
  const bindingInfo = ref(acknowledgement)
  const syncSelected = sync(connections, bindingRef, bindingInfo)
  syncSelected()
  assert.equal(bindingInfo.value.connectionFingerprint, undefined, 'an unobserved create acknowledgement is not used for verification')

  const verifiedA = { ...savedConnection, connectionFingerprint: 'fingerprint-a' }
  connections.value = [verifiedA]
  syncSelected()
  const testedFingerprint = bindingInfo.value.connectionFingerprint
  assert.equal(testedFingerprint, 'fingerprint-a')

  const rotatedB = { ...savedConnection, connectionFingerprint: 'fingerprint-b', secretUID: 'secret-b', secretResourceVersion: '10' }
  connections.value = [rotatedB]
  syncSelected()
  assert.equal(bindingInfo.value.connectionFingerprint, 'fingerprint-b')
  assert.notEqual(bindingInfo.value.connectionFingerprint, testedFingerprint, 'a concurrent key rotation invalidates the model test fingerprint')
})

test('presents multiple workspace models with explicit default and readiness state', async () => {
  const html = await render({
    settings: {
      provider: 'openai-compatible',
      baseURL: 'https://api.openai.com/v1',
      model: 'gpt-5.4',
      configured: true,
      defaultModelID: 'gpt-high',
      models: [
        { id: 'gpt-high', name: 'GPT High', provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', model: 'gpt-5.4', configured: true, default: true },
        { id: 'gemini-fast', name: 'Gemini Fast', provider: 'google-ai-studio', baseURL: 'https://generativelanguage.googleapis.com', model: 'gemini-2.5-flash', configured: false },
      ],
    },
  })

  assert.match(html, /aria-label="Edit GPT High"/)
  assert.match(html, /aria-label="Edit Gemini Fast"/)
  assert.match(html, /<table/)
  assert.match(html, /gpt-5\.4/)
  assert.match(html, /Default/)
  assert.match(html, /Not tested/)
  assert.match(html, /Needs credential/)
  assert.doesNotMatch(html, /aria-label="Model configuration form"/)
})

test('uses an explicit empty state before opening the model form', async () => {
  const html = await render()

  assert.match(html, /Connect a provider to add models/)
  assert.match(html, />Connect provider/)
  assert.doesNotMatch(html, /aria-label="Model binding form"/)
})

test('keeps the usage tab separate from the Models collection', async () => {
  const html = await render({
    routePage: true,
    settings: {
      provider: 'openai-compatible',
      baseURL: 'https://api.openai.com/v1',
      model: 'gpt-5.4',
      configured: true,
      defaultModelID: 'gpt-high',
      models: [{ id: 'gpt-high', name: 'GPT High', provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', model: 'gpt-5.4', configured: true, default: true }],
    },
  })

  assert.match(html, /<h2[^>]*>Setup<\/h2>/)
  assert.match(html, /data-k-tab-id="models"[^>]*aria-current="page"/)
  assert.match(html, /Usage &amp; cost/)
  assert.doesNotMatch(html, /aria-label="Usage and cost"/)
  assert.match(html, /aria-label="Edit GPT High"/)
})

test('usage route renders its own section even without configured models', async () => {
  for (const settings of [null, {
    provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', model: 'gpt-5.4', configured: true,
    models: [{ id: 'main', name: 'Main model', model: 'gpt-5.4', configured: true }],
  }]) {
    const html = await render({ routePage: true, usageRoute: true, settings })
    assert.match(html, /data-k-tab-id="usage"[^>]*aria-current="page"/)
    assert.match(html, /aria-label="Usage and cost"/)
    assert.match(html, /Usage reporting isn’t available yet/)
    assert.doesNotMatch(html, /No models configured|aria-label="Edit Main model"|>Add model<|>Connect provider</)
  }

})

test('route-owned model creation keeps the collection out of the form surface', async () => {
  const html = await render({
    creationRoute: true,
    settings: {
      provider: 'openai-compatible',
      baseURL: 'https://api.openai.com/v1',
      model: 'gpt-5.4',
      configured: true,
      defaultModelID: 'gpt-high',
      models: [{ id: 'gpt-high', name: 'GPT High', provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', model: 'gpt-5.4', configured: true, default: true }],
    },
  })

  assert.match(html, /aria-label="Model binding form"/)
  assert.match(html, /Saved provider connection/)
  assert.doesNotMatch(html, /aria-label="Edit GPT High"/)
  assert.doesNotMatch(html, />Add model<\/button>/)
})

test('route-owned model creation keeps the form actionable when settings load fails', async () => {
  const html = await render({
    creationRoute: true,
    loadError: 'Could not load model settings.',
    settings: null,
  })

  assert.match(html, /Could not load model settings\./)
  assert.match(html, />Retry</)
  assert.match(html, /aria-label="Model binding form"/)
  assert.match(html, /Saved provider connection/)
  assert.doesNotMatch(html, />New model</)
  assert.doesNotMatch(html, /No models configured/)
})

test('legacy model editing uses the shared binding flow and requires connection migration', async () => {
  const html = await render({
    editorOpen: true,
    editingModelID: 'legacy-haiku',
    connectionRef: '',
    name: 'Legacy Claude',
    model: 'claude-haiku-4-5',
    settings: {
      provider: 'openai-compatible', baseURL: 'https://api.anthropic.com/v1', model: 'claude-haiku-4-5',
      configured: true, defaultModelID: 'legacy-haiku',
      models: [{ id: 'legacy-haiku', name: 'Legacy Claude', provider: 'openai-compatible', baseURL: 'https://api.anthropic.com/v1', model: 'claude-haiku-4-5', configured: true, default: true }],
    },
  })
  assert.match(html, /aria-label="Model binding form"/)
  assert.match(html, /This model uses a legacy direct credential\. Select a saved provider connection to migrate it/)
  assert.match(html, /Saved provider connection/)
  assert.match(html, /No provider connections are saved yet\.[\s\S]*Connect a provider/)
  assert.match(html, />Verify &amp; migrate<\/button>/)
  assert.match(html, /claude-haiku-4-5/)
  assert.doesNotMatch(html, /ModelConnectionForm|legacy-model-connection|model-credential|API endpoint/)

  const source = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /ModelConnectionForm/)
  assert.match(source, /const legacyEditing = computed\(\(\) => Boolean\(props\.editingModelID && !props\.connectionRef\)\)/)
  assert.match(source, /modelCanSave = computed\(\(\) => Boolean\(props\.nameOnlyEdit \|\| \(props\.connectionRef && selectedConnectionSnapshotReady\.value\)\)/)
})

test('provider detours preserve the original Models or project route and the model draft', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const script = app.slice(app.indexOf('>', app.indexOf('<script')) + 1, app.indexOf('</script>'))
  const ast = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const names = ['beginLLMProviderDetour', 'finishLLMProviderDetour', 'continueModelWithProvider']
  const declarations = names.map(name => {
    const declaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name)
    assert.ok(declaration, `${name} exists`)
    return declaration.getText(ast)
  }).join('\n')
  const { outputText } = ts.transpileModule(declarations, { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const buildState = ({ createRoute = false, createModelRoute = false, returnRoute = '' } = {}) => {
    const navigation = []
    const values = {
      CREATE_PROJECT_ROUTE: '~new',
      MODELS_ROUTE: '~models',
      MODEL_PROVIDER_CONNECTIONS_ROUTE: '~models/provider-connections',
      CREATE_MODEL_ROUTE: 'create/model',
      modelsReturnRoute: ref(returnRoute),
      llmProviderDetourActive: ref(false),
      llmProviderDetourReturnRoute: ref(''),
      isCreateRoute: ref(createRoute),
      isCreateModelRoute: ref(createModelRoute),
      isProviderConnectionsRoute: ref(false),
      props: { navigate: (route, options) => navigation.push({ route, options }) },
      llmBindingConnectionRef: ref('old-provider'),
      llmBindingConnectionInfo: ref(null),
      llmProvider: ref('openai'),
      llmProviderPreset: ref('openai'),
      llmCredentialMode: ref('api-key'),
      llmBaseURL: ref('https://old.example/v1'),
      llmConnectionStatusPollRequested: ref(false),
      llmModel: ref('legacy-model'),
      llmName: ref('Existing label'),
      llmReasoningEffort: ref('high'),
      inferLLMProviderPreset: () => 'anthropic',
      isGoogleCloudBaseURL: () => false,
      normalizeLLMBaseURLInput: (_provider, baseURL) => baseURL,
      invalidateLLMConnectionTest: () => {},
      clearLLMDiscovery: () => {},
      handleModelConnectionsChanged: () => {},
      navigation,
    }
    const functions = new Function(...Object.keys(values), `${outputText}; return { beginLLMProviderDetour, continueModelWithProvider }`)(...Object.values(values))
    return { ...values, ...functions }
  }

  const editor = buildState({ createModelRoute: true, returnRoute: '~new' })
  editor.beginLLMProviderDetour()
  assert.equal(editor.llmProviderDetourReturnRoute.value, 'create/model')
  assert.deepEqual(editor.navigation.at(-1), { route: '~models/provider-connections', options: undefined })
  const modelDraft = [editor.llmModel.value, editor.llmName.value, editor.llmReasoningEffort.value]
  editor.continueModelWithProvider({
    name: 'new-provider', provider: 'anthropic', baseURL: 'https://api.anthropic.com/v1', credentialMode: 'api-key',
  })
  assert.equal(editor.llmBindingConnectionRef.value, 'new-provider')
  assert.deepEqual([editor.llmModel.value, editor.llmName.value, editor.llmReasoningEffort.value], modelDraft)
  assert.equal(editor.llmProviderDetourActive.value, false)
  assert.deepEqual(editor.navigation.at(-1), { route: 'create/model', options: { replace: true } })

  const settings = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  const settingsScript = settings.slice(settings.indexOf('>', settings.indexOf('<script')) + 1, settings.indexOf('</script>'))
  const settingsAST = ts.createSourceFile('ModelsSettings.ts', settingsScript, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const accept = settingsAST.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'acceptSavedConnection')
  assert.ok(accept)
  const compiledAccept = ts.transpileModule(accept.getText(settingsAST), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  const acceptedConnection = { name: 'new-provider', provider: 'anthropic', baseURL: 'https://api.anthropic.com/v1' }
  const buildAcceptance = (providerDetourActive, original = null) => {
    const events = []
    const state = new Function('ref', 'props', 'emit', 'toast', 'original', `
      const pendingConnection = ref(null)
      const connectionAPIKey = ref('typed-secret')
      const connectionEditorOpen = ref(true)
      const connectionOriginal = ref(original)
      const connectionNotice = ref('saved')
      const connectionError = ref('stale error')
      const connectionVerifiedFingerprint = ref('')
      ${compiledAccept}
      return { acceptSavedConnection, pendingConnection, connectionAPIKey,
        connectionEditorOpen, connectionOriginal, connectionNotice, connectionError,
        connectionVerifiedFingerprint }
    `)(ref, { providerDetourActive }, (...args) => events.push(args), (...args) => events.push(['toast', ...args]), original)
    return { ...state, events }
  }
  const detour = buildAcceptance(true)
  detour.acceptSavedConnection(acceptedConnection, 'new-fingerprint')
  assert.deepEqual(detour.events, [
    ['connectionsChanged', acceptedConnection],
    ['toast', 'ok', 'Provider connected.'],
    ['continueModelWithProvider', acceptedConnection],
  ])
  assert.equal(detour.connectionAPIKey.value, '')
  assert.equal(detour.connectionVerifiedFingerprint.value, 'new-fingerprint')
  const standalone = buildAcceptance(false)
  standalone.acceptSavedConnection(acceptedConnection, 'new-fingerprint')
  assert.deepEqual(standalone.events, [
    ['connectionsChanged', acceptedConnection],
    ['toast', 'ok', 'Provider connected.'],
  ])
  assert.equal(standalone.pendingConnection.value, null)

  const editing = buildAcceptance(false, acceptedConnection)
  editing.acceptSavedConnection(acceptedConnection, 'new-fingerprint')
  assert.deepEqual(editing.events.at(-1), ['toast', 'ok', 'Provider connection updated.'])

  const pending = buildAcceptance(false)
  const pendingConnection = { ...acceptedConnection, activationPending: true }
  pending.acceptSavedConnection(pendingConnection, 'new-fingerprint')
  assert.deepEqual(pending.events, [['connectionsChanged', pendingConnection]])
  assert.deepEqual(pending.pendingConnection.value, pendingConnection)

  assert.match(app, /if \(!modelsReturnRoute\.value && isProviderConnectionsRoute\.value\) modelsReturnRoute\.value = MODEL_PROVIDER_CONNECTIONS_ROUTE/)
  assert.match(settings, /Need another provider\? <button[^>]*@click="startProviderDetour"/)
  assert.match(settings, /No provider connections are saved yet\.[\s\S]*@click="startProviderDetour"/)
  assert.match(settings, /This model uses a legacy direct credential\. Select a saved provider connection to migrate it/)
  assert.match(settings, /No provider connections are saved yet\.[\s\S]*@click="startProviderDetour"/)
  assert.match(settings, /@click="addModelFor\(connectionFromTableRow\(row\)\)"/)
  assert.doesNotMatch(settings, /Dismiss provider connected message/)
  assert.match(app, /@continue-model-with-provider="continueModelWithProvider"/)
})

test('keeps the provider detour link visible beneath an existing model binding selection', async () => {
  const html = await render({
    creationRoute: true,
    editorOpen: true,
    connectionRef: savedConnection.name,
    connections: [savedConnection],
  })
  assert.match(html, /Saved provider connection/)
  assert.match(html, /Need another provider\?/)
  assert.match(html, />Connect provider<\/button>/)
})

test('combines models and provider connections and keeps a focused provider-first form', async () => {
  const html = await render({
    routePage: true,
    providerConnectionsRoute: true,
    connections: [savedConnection],
    settings: {
      provider: 'openai-compatible', baseURL: '', model: 'gpt-5.4', configured: true,
      models: [{ id: 'main', name: 'Main model', model: 'gpt-5.4', configured: true, connectionRef: savedConnection.name }],
    },
  })

  assert.match(html, /data-k-tab-id="models"[^>]*aria-current="page"/)
  assert.doesNotMatch(html, /data-k-tab-id="connections"/)
  assert.match(html, /aria-label="Models"[\s\S]*OpenAI main[\s\S]*aria-label="Provider connections"/)
  assert.match(html, /aria-label="Create"/)
  assert.match(html, /aria-label="Delete provider connection OpenAI main"[^>]*disabled/)
  const source = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  const start = source.indexOf('<template v-else-if="connectionEditorOpen">')
  const end = source.indexOf('<template v-else-if="!editorOpen && !creationRoute">', start)
  assert.ok(start >= 0 && end > start)
  const form = source.slice(start, end)
  assert.match(form, /<h1[^>]*tabindex="-1"/)
  assert.match(form, /id="model-provider"/)
  assert.match(form, /id="model-credential"/)
  assert.doesNotMatch(form.slice(0, form.indexOf('connectionStage === \'verify\'')), /id="model-id"/)
  assert.match(form.slice(0, form.indexOf('connectionStage === \'verify\'')), /Connection name \(optional\)/)
  assert.match(form, /Discovery checks provider access without sending a model request or adding a model binding/)
  assert.match(form, /No model request will be sent/)
  assert.match(form, /connectionVerificationWillCallModel \? 'Verify sends the selected model request/)
  assert.match(form, /<ArrowLeft class="h-3\.5 w-3\.5"/)
})

test('labels connections awaiting explicit discovery as not verified, not failed', async () => {
  const html = await render({
    providerConnectionsRoute: true,
    connections: [{
      ...savedConnection,
      ready: false,
      reachable: undefined,
      lastProbeError: 'run authorized model discovery to verify this endpoint',
      conditions: [{ type: 'Reachable', status: 'Unknown', reason: 'ExplicitDiscoveryRequired' }],
    }],
  })

  assert.match(html, /Not verified/)
  assert.match(html, /No provider check has been run yet\. Discover models or test a model to verify this connection\./)
  assert.doesNotMatch(html, />Checking</)
  assert.doesNotMatch(html, /Needs attention/)
  assert.doesNotMatch(html, /run authorized model discovery/)
})

test('shows manual model guidance for ready providers that cannot list models', async () => {
  const guidance = 'Automatic model discovery is unavailable for Vertex service-account credentials; enter a model ID and test it.'
  const html = await render({
    routePage: true,
    connections: [{
      ...savedConnection,
      provider: 'google-ai-studio',
      baseURL: 'https://aiplatform.googleapis.com',
      credentialMode: 'service-account-json',
      reachable: undefined,
      lastProbeError: guidance,
      conditions: [{ type: 'Reachable', status: 'Unknown', reason: 'DiscoveryUnavailable' }],
    }],
  })

  assert.match(html, /data-full-value="Ready"/)
  assert.ok(html.includes(guidance))
})

test('shows recovery guidance when a saved provider probe fails', async () => {
  const guidance = 'Provider model discovery failed; check the endpoint and credential, or enter a model ID and test it.'
  const html = await render({
    routePage: true,
    connections: [{
      ...savedConnection,
      ready: false,
      reachable: false,
      lastProbeError: guidance,
      conditions: [{ type: 'Reachable', status: 'False', reason: 'ProbeFailed' }],
    }],
  })

  assert.match(html, /data-full-value="Unreachable"/)
  assert.ok(html.includes(guidance))
})

test('offers known provider presets and marks custom endpoints required', async () => {
  const source = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  assert.match(source, /label: 'OpenAI'/)
  assert.match(source, /label: 'Custom OpenAI-compatible'/)
  assert.match(source, /label: 'Anthropic'/)
  assert.match(source, /Google AI Studio \/ Vertex AI/)
  assert.match(source, /:required="connectionPreset === 'custom'"/)
  assert.match(source, /:aria-required="connectionPreset === 'custom'"/)
})

test('new provider connections get friendly preset labels while edits can clear their label', async () => {
  const source = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  const script = source.slice(source.indexOf('>', source.indexOf('<script')) + 1, source.indexOf('</script>'))
  const ast = ts.createSourceFile('ModelsSettings.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const providerOptions = ast.statements.find(node => ts.isVariableStatement(node)
    && node.declarationList.declarations.some(declaration => ts.isIdentifier(declaration.name) && declaration.name.text === 'providerOptions'))
  const providerPresetFor = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'providerPresetFor')
  const displayNameForSave = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'connectionDisplayNameForSave')
  assert.ok(providerOptions)
  assert.ok(providerPresetFor)
  assert.ok(displayNameForSave)
  const declarations = [providerOptions.getText(ast), providerPresetFor.getText(ast), displayNameForSave.getText(ast)].join('\n')
  const { outputText } = ts.transpileModule(declarations, { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const displayName = new Function(`${outputText}; return connectionDisplayNameForSave`)()
  const anthropic = { name: 'anthropic', provider: 'openai-compatible', baseURL: 'https://api.anthropic.com/v1' }

  assert.equal(displayName('', false, 'anthropic', []), 'Anthropic')
  assert.equal(displayName('', false, 'anthropic', [{ ...anthropic, displayName: 'Anthropic' }]), 'Anthropic 2')
  assert.equal(displayName('', false, 'openai', [savedConnection]), 'OpenAI')
  assert.equal(displayName('', false, 'openai', [{ ...savedConnection, displayName: 'OpenAI 2' }]), 'OpenAI')
  assert.equal(displayName('', false, 'openai', [
    { ...savedConnection, displayName: 'OPENAI' },
    { ...savedConnection, displayName: 'openai 2' },
    { ...anthropic, displayName: 'OpenAI 3' },
  ]), 'OpenAI 4')
  assert.equal(displayName('', false, 'custom', [{ ...savedConnection, displayName: 'Custom OpenAI-compatible', baseURL: 'https://api.example.test/v1' }]), 'Custom OpenAI-compatible 2')
  assert.equal(displayName('  Team key  ', false, 'openai', []), 'Team key')
  assert.equal(displayName('', true, 'anthropic', []), '')

  assert.equal((source.match(/displayName: connectionDisplayNameForSave\(/g) || []).length, 2,
    'automatic discovery-save and manual verify-save both apply the fallback')
  assert.match(source, /\.\.\.\(saved \? \{ name: saved\.name \} : \{\}\)/,
    'new connections continue to use an opaque backend-generated resource ID')
})

test('saved provider connections open on cached models with the existing label visible', async () => {
  const source = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  const script = source.slice(source.indexOf('>', source.indexOf('<script')) + 1, source.indexOf('</script>'))
  const ast = ts.createSourceFile('ModelsSettings.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const names = ['providerPresetFor', 'savedConnectionCredentialMode', 'openConnectionEditor']
  const declarations = names.map(name => {
    const declaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name)
    assert.ok(declaration, `${name} exists`)
    if (name === 'openConnectionEditor') assert.doesNotMatch(declaration.getText(ast), /api\.(?:test|discover)/)
    return declaration.getText(ast)
  }).join('\n')
  const { outputText } = ts.transpileModule(declarations, { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const state = {
    connectionOriginal: ref(null), pendingConnection: ref(null), connectionPreset: ref('openai'),
    connectionProvider: ref(''), connectionBaseURL: ref(''), connectionCredentialMode: ref('api-key'),
    connectionWorkspaceID: ref(''), connectionAPIKey: ref(''), connectionDisplayName: ref(''),
    connectionVerificationModel: ref(''), connectionDiscoveredModels: ref([]),
    connectionDiscoverySucceeded: ref(false), connectionStage: ref('details'), connectionEditorOpen: ref(false),
  }
  const openConnectionEditor = new Function('ref', 'invalidateConnectionProbe', ...Object.keys(state),
    `${outputText}; return openConnectionEditor`)(ref, () => {}, ...Object.values(state))
  openConnectionEditor(savedConnection)

  assert.equal(state.connectionStage.value, 'verify')
  assert.equal(state.connectionDisplayName.value, savedConnection.displayName)
  assert.deepEqual(state.connectionDiscoveredModels.value, savedConnection.models)
  assert.equal(state.connectionDiscoverySucceeded.value, false,
    'cached models populate the selector without being treated as a fresh discovery check')
  assert.match(source, /connectionStage\.value = saved \? 'verify' : 'details'/)
  assert.match(source, /<label for="provider-connection-name"[^>]*>Connection name \(optional\)/)
})

test('saved connection label-only edits use a guarded metadata save without provider probes', async () => {
  const source = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  const script = source.slice(source.indexOf('>', source.indexOf('<script')) + 1, source.indexOf('</script>'))
  const ast = ts.createSourceFile('ModelsSettings.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const nameOnlyDeclaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'isConnectionNameOnlyEdit')
  const saveDeclaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'saveProviderConnection')
  assert.ok(nameOnlyDeclaration)
  assert.ok(saveDeclaration)
  const helperSource = [
    ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'savedConnectionCredentialMode')?.getText(ast),
    nameOnlyDeclaration.getText(ast),
  ].join('\n')
  const { outputText: helperOutput } = ts.transpileModule(helperSource, { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const isNameOnlyEdit = new Function(`${helperOutput}; return isConnectionNameOnlyEdit`)()
  const vertexConnection = {
    ...savedConnection,
    name: 'vertex-main',
    displayName: 'Vertex main',
    provider: 'google-ai-studio',
    baseURL: 'not-a-valid-url',
    credentialMode: 'service-account-json',
    anthropicWorkspaceID: '',
  }
  assert.equal(isNameOnlyEdit(vertexConnection, vertexConnection.provider, vertexConnection.baseURL, '', 'service-account-json', '', 'Vertex renamed', ''), true)
  assert.equal(isNameOnlyEdit(vertexConnection, vertexConnection.provider, vertexConnection.baseURL, '', 'service-account-json', '', 'Vertex main', ''), false)
  assert.equal(isNameOnlyEdit(vertexConnection, vertexConnection.provider, 'https://changed.example/v1', '', 'service-account-json', '', 'Vertex renamed', ''), false)
  assert.equal(isNameOnlyEdit(vertexConnection, vertexConnection.provider, vertexConnection.baseURL, '', 'service-account-json', 'new-key', 'Vertex renamed', ''), false)
  assert.equal(isNameOnlyEdit(vertexConnection, vertexConnection.provider, vertexConnection.baseURL, '', 'service-account-json', '', 'Vertex renamed', 'gemini-2.5-pro'), false)

  const { outputText: saveOutput } = ts.transpileModule(saveDeclaration.getText(ast), {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  })
  const events = []
  const saveState = new Function('ref', 'saved', 'events', `
    let connectionRequestSerial = 0
    const props = { ctx: { token: 'test-token' } }
    const connectionError = ref('')
    const connectionNotice = ref('')
    const connectionOriginal = ref(saved)
    const activeConnectionBindings = { value: () => [] }
    const connectionAPIKey = ref('')
    const connectionNameOnlyEdit = ref(true)
    const connectionHasConfigChanges = ref(false)
    const connectionStage = ref('verify')
    const connectionBaseURLError = ref('The saved endpoint is currently invalid.')
    const connectionHasReusableKey = ref(false)
    const connectionCredentialMode = ref('service-account-json')
    const connectionVerificationModel = ref('')
    const connectionDiscoverySucceeded = ref(false)
    const connectionDraftFingerprint = ref('draft-1')
    const connectionBusy = ref(false)
    const connectionVerified = ref(false)
    const connectionVerifiedFingerprint = ref('')
    const connectionDisplayName = ref('Vertex renamed')
    const connectionPreset = ref('google')
    const connectionRows = ref([saved])
    const connectionProvider = ref(saved.provider)
    const connectionEndpoint = ref(saved.baseURL)
    const connectionWorkspaceID = ref('')
    const verifyConnectionDraft = async () => { events.push(['test']) }
    const connectionDisplayNameForSave = value => value.trim()
    const verifyConnectionModels = async (...args) => {
      const [bindings, optionalModel, verifyOptional, verify] = args
      events.push(['verify-models', bindings, optionalModel, verifyOptional])
      const checked = []
      for (const binding of bindings) { await verify(binding.model, binding.reasoningEffort); checked.push(binding.model) }
      if (optionalModel && verifyOptional) { await verify(optionalModel); checked.push(optionalModel) }
      return checked.length
    }
    const api = {
      discoverLLMModels: async () => { events.push(['discover']); return { models: [] } },
      testLLMConnection: async () => { events.push(['test']); return { ok: true } },
      saveModelProviderConnection: async (_ctx, body, expected) => {
        events.push(['save', body, expected])
        return { ...saved, displayName: body.displayName }
      },
    }
    const acceptSavedConnection = (connection, fingerprint) => events.push(['accepted', connection, fingerprint])
    ${saveOutput}
    return { saveProviderConnection, connectionError, connectionBusy }
  `)(ref, vertexConnection, events)

  await saveState.saveProviderConnection()
  assert.deepEqual(events.map(event => event[0]), ['verify-models', 'save', 'accepted'])
  assert.deepEqual(events[0].slice(1), [[], '', true], 'no bound or optional model is tested for a label-only edit')
  const saveEvent = events[1]
  assert.equal(saveEvent[1].name, vertexConnection.name)
  assert.equal(saveEvent[1].displayName, 'Vertex renamed')
  assert.equal(saveEvent[1].baseURL, vertexConnection.baseURL, 'metadata-only save preserves the exact saved endpoint')
  assert.equal('apiKey' in saveEvent[1], false)
  assert.equal(saveEvent[2].uid, vertexConnection.uid, 'the metadata write retains the captured connection UID')
  assert.equal(saveEvent[2].generation, vertexConnection.generation, 'the metadata write retains the captured spec generation')
  assert.equal(saveState.connectionError.value, '')

  assert.match(source, /connectionNameOnlyEdit\.value\s*\|\|\s*\(!connectionBaseURLError\.value/)
  assert.match(source, /if \(connectionBaseURLError\.value && !nameOnlyEdit\)/)
})

test('fresh discovery replaces cached connection models, including an empty result', async () => {
  const source = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  const script = source.slice(source.indexOf('>', source.indexOf('<script')) + 1, source.indexOf('</script>'))
  const ast = ts.createSourceFile('ModelsSettings.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const declaration = ast.statements.find(node => ts.isVariableStatement(node)
    && node.declarationList.declarations.some(item => item.name.getText(ast) === 'selectedModelRows'))
  assert.ok(declaration)
  const props = { discoveryComplete: false, discoveredModels: [] }
  const selectedConnection = { value: { models: [{ id: 'cached' }] } }
  const rows = new Function('props', 'selectedConnection', 'computed', `${declaration.getText(ast)}; return selectedModelRows`)(props, selectedConnection, fn => ({ get value() { return fn() } }))
  assert.deepEqual(rows.value, [{ id: 'cached' }])
  props.discoveredModels = [{ id: 'fresh' }]
  props.discoveryComplete = true
  assert.deepEqual(rows.value, [{ id: 'fresh' }])
  props.discoveredModels = []
  assert.deepEqual(rows.value, [])
  props.discoveryComplete = false
  assert.deepEqual(rows.value, [{ id: 'cached' }])
})

test('renders discovered suggestions while preserving manual model entry', async () => {
  const html = await render({
    editorOpen: true,
    canDiscover: true,
    discoveredModels: [
      { id: 'gpt-5.6', name: 'gpt-5.6', compatibility: 'recommended' },
      { id: 'custom-chat', name: 'custom-chat', compatibility: 'available' },
      { id: 'text-embedding-3-large', name: 'text-embedding-3-large', compatibility: 'unsuitable' },
    ],
  })

  assert.match(html, /id="model-id"/)
  assert.match(html, /aria-haspopup="listbox"/)
  assert.doesNotMatch(html, /<datalist/)
  const source = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  assert.match(source, /:models="selectedModelRows"/)
  assert.match(source, /Choose a discovered model or enter its exact ID/)
  assert.match(await render({ editorOpen: true, discoveryLoading: true }), /Loading models…/)
  assert.match(await render({ editorOpen: true, discoveryComplete: true, modelHint: '' }), /Choose a discovered model or enter its exact ID./)
})

test('keeps discovery failure separate from manual model validation', async () => {
  const html = await render({
    editorOpen: true,
    discoveryError: 'Provider model discovery returned 401 Unauthorized.',
  })

  assert.match(html, /Provider model discovery returned 401 Unauthorized/)
  assert.match(html, /You can still enter a model ID manually/)
  assert.match(html, /id="model-id"/)
})

test('editing replaces the collection with one focused form', async () => {
  const html = await render({
    editorOpen: true,
    editingModelID: 'gpt-high',
    connectionRef: savedConnection.name,
    connections: [savedConnection],
    name: 'GPT High',
    requireConnectionTest: true,
    settings: {
      provider: 'openai-compatible',
      baseURL: 'https://api.openai.com/v1',
      model: 'gpt-5.4',
      configured: true,
      defaultModelID: 'gpt-high',
      models: [{ id: 'gpt-high', name: 'GPT High', provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', model: 'gpt-5.4', connectionRef: savedConnection.name, configured: true, default: true }],
    },
  })

  assert.match(html, /aria-label="Model binding form"/)
  assert.match(html, /class="k-create-surface k-create-surface--wide k-model-form agents-provider-connect"/)
  assert.match(html, /Verify &amp; save/)
  assert.doesNotMatch(html, /aria-label="Edit GPT High"/)
})

test('Models-route editing uses the wide full-page form without an inner editor heading', async () => {
  const html = await render({
    routePage: true,
    editorOpen: true,
    editingModelID: 'gpt-high',
    connectionRef: savedConnection.name,
    connections: [savedConnection],
    name: 'GPT High',
  })

  assert.match(html, /class="k-create-surface k-create-surface--wide k-model-form agents-provider-connect"/)
  assert.match(html, /aria-label="Model binding form"/)
  assert.doesNotMatch(html, /<h4[^>]*>Edit model<\/h4>/)
})

test('associates field guidance and validation errors with their controls', async () => {
  const html = await render({
    editorOpen: true,
    connectionRef: savedConnection.name,
    connections: [savedConnection],
    editingModelID: 'gpt-high',
    model: 'gpt-5.4',
    nameError: 'Enter a display name.',
    modelError: 'Enter the provider’s exact model ID.',
  })

  assert.match(html, /id="model-display-name"[^>]*aria-invalid="true"[^>]*aria-describedby="model-display-name-error"/)
  assert.match(html, /id="model-id"[^>]*aria-invalid="true"[^>]*aria-describedby="[^"]*model-id-error[^"]*binding-model-help"/)
  assert.match(html, /id="model-display-name"[^>]*aria-describedby="model-display-name-error"/)
  assert.match(html, /id="model-id"[^>]*aria-invalid="true"/)
  assert.match(html, /Enter the provider’s exact model ID\./)
})

test('announces the active model mutation and disables competing table actions', async () => {
  const editing = await render({
    editorOpen: true,
    editingModelID: 'gpt-high',
    connectionRef: savedConnection.name,
    connections: [savedConnection],
    name: 'GPT High',
    saving: true,
  })
  assert.match(editing, /aria-busy="true"/)
  assert.match(editing, /Saving…/)

  const collection = await render({
    saving: true,
    settings: {
      provider: 'openai-compatible',
      baseURL: 'https://api.openai.com/v1',
      model: 'gpt-5.4',
      configured: true,
      defaultModelID: 'gpt-high',
      models: [{ id: 'gpt-high', name: 'GPT High', provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', model: 'gpt-5.4', configured: true, default: true }],
    },
  })
  assert.match(collection, /aria-label="Edit GPT High"[^>]*disabled/)
})

test('model binding combines verification and save in one primary action', async () => {
  const html = await render({ creationRoute: true, name: '', model: 'gpt-5.4', connectionRef: savedConnection.name, connections: [savedConnection] })
  assert.match(html, /Verify &amp; connect/)
  assert.doesNotMatch(html, />Test connection<\/button>/)
  assert.match(html, /Display name \(optional\)/)
})

test('model binding verification reads the selected connection fingerprint and uses the guarded draft probe', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const script = app.slice(app.indexOf('>', app.indexOf('<script')) + 1, app.indexOf('</script>'))
  const ast = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const declaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'testLLMConnection')
  assert.ok(declaration)
  const source = declaration.getText(ast)
  assert.match(source, /api\.getModelProviderConnection\(props\.ctx, llmBindingConnectionRef\.value\)/)
  assert.match(source, /api\.testLLMConnection\(props\.ctx,[\s\S]*existingConnectionID: connectionInfo\?\.name[\s\S]*connectionFingerprint: connectionInfo\?\.connectionFingerprint/)
  assert.doesNotMatch(source, /api\.testModelProviderConnection/)
})

test('a display-name-only edit stays saveable without provider connection metadata', async () => {
  const stale = { ...savedConnection, observedGeneration: 0, connectionFingerprint: undefined, secretUID: undefined, secretResourceVersion: undefined }
  const html = await render({
    creationRoute: true,
    editingModelID: 'main',
    name: 'Main model renamed',
    model: 'gpt-5.4',
    connectionRef: stale.name,
    connections: [stale],
    nameOnlyEdit: true,
    requireConnectionTest: false,
  })
  assert.match(html, /<button type="submit" class="k-btn k-btn--primary"[^>]*>Save changes<\/button>/)
  assert.doesNotMatch(html, /Confirming the saved provider key version/)
})

test('connection edits verify every dependent model and the optional model before save', async () => {
  const source = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  const script = source.slice(source.indexOf('>', source.indexOf('<script')) + 1, source.indexOf('</script>'))
  const ast = ts.createSourceFile('ModelsSettings.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const declaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'verifyConnectionModels')
  const saveDeclaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'saveProviderConnection')
  assert.ok(declaration)
  assert.ok(saveDeclaration)
  const { outputText } = ts.transpileModule(declaration.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const verifyModels = new Function(`${outputText}; return verifyConnectionModels`)()
  const calls = []
  const count = await verifyModels(
    [{ model: 'claude-sonnet-4-5', reasoningEffort: 'medium' }, { model: 'gpt-5.4' }],
    'gpt-5.4',
    true,
    async (model, effort) => { calls.push([model, effort]) },
    () => true,
  )
  assert.deepEqual(calls, [['claude-sonnet-4-5', 'medium'], ['gpt-5.4', undefined]])
  assert.equal(count, 2, 'an identical optional model is not tested twice')

  const attempted = []
  await assert.rejects(verifyModels(
    [{ model: 'claude-sonnet-4-5' }],
    'rejected-optional-model',
    true,
    async model => {
      attempted.push(model)
      if (model === 'rejected-optional-model') throw new Error('optional model rejected')
    },
    () => true,
  ), /optional model rejected/)
  assert.deepEqual(attempted, ['claude-sonnet-4-5', 'rejected-optional-model'])
  assert.ok(saveDeclaration.getText(ast).indexOf('await verifyConnectionModels(') < saveDeclaration.getText(ast).indexOf('api.saveModelProviderConnection('))
})

test('provider connection edits explicitly send an empty Anthropic workspace when clearing it', async () => {
  const source = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  const script = source.slice(source.indexOf('>', source.indexOf('<script')) + 1, source.indexOf('</script>'))
  const ast = ts.createSourceFile('ModelsSettings.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const declaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'draftProbeBody')
  assert.ok(declaration)
  const { outputText } = ts.transpileModule(declaration.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const buildBody = new Function('connectionOriginal', 'connectionHasReusableKey', 'connectionProvider', 'connectionEndpoint', 'connectionCredentialMode', 'connectionWorkspaceID', 'connectionAPIKey', `${outputText}; return draftProbeBody`)(
    { value: null }, { value: false }, { value: 'anthropic' }, { value: 'https://api.anthropic.com/v1' },
    { value: 'api-key' }, { value: '' }, { value: 'sk-test' },
  )
  assert.equal(buildBody().anthropicWorkspaceID, '')
  assert.match(source, /anthropicWorkspaceID: nameOnlyEdit && saved \? saved\.anthropicWorkspaceID \|\| '' : connectionWorkspaceID\.value\.trim\(\)/)
})

test('waits for the saved key version and lets the binding action enable when status catches up', async () => {
  const stale = { ...savedConnection, observedGeneration: 0, connectionFingerprint: undefined, secretUID: undefined, secretResourceVersion: undefined }
  const waiting = await render({ creationRoute: true, name: '', model: 'gpt-5.4', connectionRef: stale.name, connections: [stale], connectionStatusChecking: true })
  assert.match(waiting, /Confirming the saved provider key version/)
  assert.match(waiting, /<button type="submit" class="k-btn k-btn--primary" disabled[^>]*>Verify &amp; connect/)

  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const script = app.slice(app.indexOf('>', app.indexOf('<script')) + 1, app.indexOf('</script>'))
  const ast = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const names = ['isLLMConnectionSnapshotReady', 'llmConnectionStatusPollIsCurrent', 'pollLLMConnectionStatus']
  const declarations = names.map(name => {
    const declaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name)
    assert.ok(declaration, `${name} exists`)
    return declaration.getText(ast)
  }).join('\n')
  const { outputText: pollSource } = ts.transpileModule(declarations, { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const poll = new Function('ref', 'props', 'routePath', 'appContextFingerprint', 'llmBindingConnectionRef', 'llmEditorOpen', 'isCreateModelRoute', 'llmConnections', 'llmConnectionStatusPollSerial', 'llmConnectionStatusChecking', 'llmConnectionStatusTimedOutFor', 'loadModelProviderConnections', `${pollSource}; return pollLLMConnectionStatus`)
  const ready = { ...savedConnection, observedGeneration: savedConnection.generation }
  const staleStatus = { ...savedConnection, observedGeneration: 0, connectionFingerprint: undefined, secretUID: undefined, secretResourceVersion: undefined }
  const llmConnections = ref([staleStatus])
  let reads = 0
  const pollStatus = poll(
    ref,
    { ctx: { token: 'token' } },
    { value: 'create/model' },
    () => 'context-fingerprint',
    { value: savedConnection.name },
    { value: true },
    { value: false },
    llmConnections,
    7,
    ref(false),
    ref(''),
    async () => { reads += 1; llmConnections.value = [reads < 2 ? staleStatus : ready] },
  )
  await pollStatus(savedConnection.name, 'create/model', 'context-fingerprint', 7)
  assert.equal(reads, 2, 'retries after the first stale list read')
  assert.equal(llmConnections.value[0].connectionFingerprint, 'fingerprint')
})

test('App Studio owns save state while the extracted surface owns presentation', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')

  assert.match(app, /const CREATE_MODEL_ROUTE = 'create\/model'/)
  assert.match(app, /const isCreateModelRoute = computed\(\(\) => routePath\.value === CREATE_MODEL_ROUTE\)/)
  assert.match(app, /const isModelEditorPage = computed\(\(\) => isCreateModelRoute\.value \|\| \(isModelsRoute\.value && !isModelUsageRoute\.value && !llmProviderDetourActive\.value && llmEditorOpen\.value\)\)/)
  assert.match(app, /const routePath = computed\(\(\) => \(props\.ctx\?\.subPath \?\? ''\)\.split\('\/'\)\.filter\(Boolean\)\.join\('\/'\)\)/)
  assert.match(app, /v-if="showSettings[\s\S]*\(\(isModelsRoute \|\| isCreateModelRoute\) && !\(initializing && !loading\)\)"/)
  assert.match(app, /<ModelsSettings[\s\S]*:creation-route="isCreateModelRoute"/)
  assert.match(app, /<ModelsSettings[\s\S]*@save="verifyAndSaveLLMSettings"[\s\S]*@delete="deleteLLMModel"[\s\S]*@set-default="setDefaultLLMModel"/)
  assert.doesNotMatch(app, /llmApiKey|selectLLMProvider|updateLLMBaseURL|updateLLMAPIKey/)
  assert.match(app, /async function discoverLLMModels[\s\S]*api\.discoverModelProviderConnection/)
  assert.doesNotMatch(app, /api\.discoverLLMModels\(props\.ctx/)
  assert.match(app, /async function saveLLMSettings[\s\S]*api\.patchLLMModel[\s\S]*api\.createLLMModel/)
  assert.match(app, /async function deleteLLMModel[\s\S]*api\.deleteLLMModel/)
  assert.match(app, /catch \(e\)[\s\S]*llmActionError\.value = e instanceof Error/)
})

test('model creation replaces its route entry and nested navigation accepts replace metadata', async () => {
  const [app, pageElement] = await Promise.all([
    readFile(new URL('./App.vue', import.meta.url), 'utf8'),
    readFile(new URL('./page-element.ts', import.meta.url), 'utf8'),
  ])

  assert.match(app, /function openNewLLMModelEditor\(\)[\s\S]*props\.navigate\(CREATE_MODEL_ROUTE\)/)
  assert.match(app, /async function cancelLLMEditor\(\)[\s\S]*Discard model changes\?[\s\S]*const returnRoute = routeOwnedCreation[\s\S]*props\.navigate\(returnRoute, \{ replace: true \}\)/)
  assert.match(app, /const routeOwnedCreation = isCreateModelRoute\.value && !llmEditingModelID\.value[\s\S]*const returnRoute = routeOwnedCreation[\s\S]*props\.navigate\(returnRoute, \{ replace: true \}\)/)
  assert.match(app, /const detail = \(e as CustomEvent<\{ path\?: unknown; replace\?: unknown \}>\)\.detail/)
  assert.match(app, /if \(!tab \|\| tab\.kind !== 'provider'\) return\s+\/\/ A cancelable nested-provider event[\s\S]*e\.preventDefault\(\)/)
  assert.match(app, /Nested provider tabs have one persisted descriptor rather than their own/)
  assert.match(pageElement, /const navigate = \(path: string, options: NavigationOptions = \{\}\)/)
  assert.match(pageElement, /detail: \{ path, \.\.\.\(options\.replace === true \? \{ replace: true \} : \{\}\) \}/)
})

test('composer exposes the configured model and reasoning picker and sends stable selections', async () => {
  const [app, picker] = await Promise.all([
    readFile(new URL('./App.vue', import.meta.url), 'utf8'),
    readFile(new URL('./agentkit/AIModelReasoningPicker.vue', import.meta.url), 'utf8'),
  ])
  assert.match(app, /<template #actions>[\s\S]*<AIModelReasoningPicker[\s\S]*:models="configuredLLMModels"[\s\S]*:selected-i-d="selectedLLMModel\?\.id \|\| ''"[\s\S]*:effort="assistantReasoningEffort"[\s\S]*@select="selectedLLMModelID = \$event"[\s\S]*@update:effort="selectAssistantReasoningEffort"/)
  assert.match(app, /const startOperation = \{[\s\S]*modelID: requestedModelID[\s\S]*reasoningEffort: requestedReasoningEffort/)
  assert.match(app, /startAssistantTurn[\s\S]*modelID: payload\.modelID/)
  assert.match(app, /startAssistantReview[\s\S]*modelID: payload\.modelID/)
  assert.match(picker, /data-testid="model-reasoning-trigger"/)
  assert.match(picker, /data-testid="reasoning-model-list"/)
  assert.match(picker, /@keydown="handleTriggerKeydown"/)
  assert.match(picker, /event\.key === 'ArrowDown' \|\| event\.key === 'ArrowUp'/)
  assert.match(picker, /event\.key === 'Home' \|\| event\.key === 'End'/)
  assert.match(picker, /event\.key === 'Escape'/)
  assert.match(picker, /event\.key === 'Tab'[\s\S]*closePicker\(false\)/)
  assert.match(picker, /role="option"[\s\S]*tabindex="-1"/)
  assert.match(picker, /triggerRef\.value\?\.focus\(\{ preventScroll: true \}\)/)
  assert.doesNotMatch(picker, /document\.addEventListener\('keydown'/)
})

test('guards delayed model mutations against context, route, and newer mutation generations', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const mutationStart = app.indexOf('async function saveLLMSettings')
  const mutationEnd = app.indexOf('async function createProjectFromPrompt', mutationStart)
  assert.ok(mutationStart >= 0 && mutationEnd > mutationStart)
  const mutations = app.slice(mutationStart, mutationEnd)

  assert.match(app, /interface LLMModelMutationGuard[\s\S]*generation: number[\s\S]*contextFingerprint: string[\s\S]*routePath: string/)
  assert.match(app, /function invalidateLLMModelMutationState\(\)[\s\S]*llmModelMutationGeneration \+= 1/)
  assert.match(app, /function beginLLMModelMutation\(\): LLMModelMutationGuard[\s\S]*generation: \+\+llmModelMutationGeneration/)
  assert.match(app, /function llmModelMutationIsCurrent\(guard: LLMModelMutationGuard\): boolean[\s\S]*guard\.contextFingerprint === appContextFingerprint\(props\.ctx\)[\s\S]*guard\.routePath === routePath\.value/)
  assert.match(app, /watch\(\s*\(\) => props\.ctx\?\.subPath \?\? ''[\s\S]*invalidateLLMModelMutationState\(\)/)
  assert.match(app, /function invalidateProjectContextState\(\)[\s\S]*invalidateLLMModelMutationState\(\)/)

  for (const operation of ['saveLLMSettings', 'deleteLLMModel', 'setDefaultLLMModel']) {
    const start = mutations.indexOf(`async function ${operation}`)
    assert.ok(start >= 0, `${operation} should remain App Studio-owned`)
    const end = mutations.indexOf('\nasync function ', start + 1)
    const block = mutations.slice(start, end < 0 ? mutations.length : end)
    assert.match(block, /const guard = beginLLMModelMutation\(\)/)
    assert.match(block, /if \(!llmModelMutationIsCurrent\(guard\)\) return/)
    assert.match(block, /catch \(e\) \{[\s\S]*if \(!llmModelMutationIsCurrent\(guard\)\) return/)
    assert.match(block, /finally \{[\s\S]*if \(llmModelMutationIsCurrent\(guard\)\) llmSaving\.value = false/)
  }
})

test('uses the stored project-creation destination and Models fallback for route-owned creation', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  assert.match(app, /modelsReturnRoute\.value === CREATE_PROJECT_ROUTE \? CREATE_PROJECT_ROUTE : modelsReturnRoute\.value === MODEL_PROVIDER_CONNECTIONS_ROUTE \? MODEL_PROVIDER_CONNECTIONS_ROUTE : MODELS_ROUTE/)
  assert.match(app, /function openNewLLMModelEditor\(\)[\s\S]*if \(!modelsReturnRoute\.value && isCreateRoute\.value\) modelsReturnRoute\.value = CREATE_PROJECT_ROUTE/)

  const cancelStart = app.indexOf('async function cancelLLMEditor')
  const saveStart = app.indexOf('async function saveLLMSettings')
  const saveEnd = app.indexOf('\nasync function deleteLLMModel', saveStart)
  assert.match(app.slice(cancelStart, saveStart), /const returnRoute = routeOwnedCreation[\s\S]*modelsReturnRoute\.value = ''[\s\S]*props\.navigate\(returnRoute, \{ replace: true \}\)/)
  assert.match(app.slice(saveStart, saveEnd), /const returnRoute = routeOwnedCreation[\s\S]*modelsReturnRoute\.value = ''[\s\S]*props\.navigate\(returnRoute, \{ replace: true \}\)/)
})


test('refreshing settings allows retry and ignores old saved-model completions', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const script = app.slice(app.indexOf('>') + 1, app.indexOf('</script>'))
  const parsed = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const names = ['invalidateLLMModelMutationState', 'llmModelMutationIsCurrent', 'testSavedLLMModel']
  const functions = names.map(name => {
    const declaration = parsed.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name)
    assert.ok(declaration, `${name} exists`)
    return declaration.getText(parsed)
  }).join('\n')
  for (const name of ['openLLMEditor', 'cancelLLMEditor']) {
    const declaration = parsed.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name)
    assert.match(declaration.getText(parsed), /invalidateLLMModelMutationState\(\)/)
  }
  const { outputText } = ts.transpileModule(`
    export function harness(api, dependencies) {
      let llmModelMutationGeneration = 0;
      const appComponentMounted = true;
      const props = { ctx: 'workspace' };
      const appContextFingerprint = ctx => ctx;
      const routePath = { value: '/models' };
      const llmSaving = { value: false };
      const llmModelTests = dependencies.llmModelTests;
      const llmSettings = dependencies.llmSettings;
      let llmModelTestRequestSerial = 0;
      ${functions}
      return { states: llmModelTests, test: testSavedLLMModel, invalidate: invalidateLLMModelMutationState };
    }
  `, { compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 } })
  const { harness } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)
  for (const reset of ['refresh', 'invalidate']) {
    for (const oldFail of [false, true]) {
      for (const retryFail of [false, true]) {
        const requests = []
        const states = ref({})
        const settings = ref({ models: [{ id: 'main', configured: true, provider: 'openai-compatible', baseURL: 'https://example.test/v1', model: 'chat' }] })
        const stopWatchingSettings = watch(settings, () => { states.value = {} })
        const state = harness(
          { testLLMConnection: () => new Promise((resolve, reject) => requests.push({ resolve, reject })) },
          { llmModelTests: states, llmSettings: settings },
        )
        const oldTest = state.test('main')
        assert.equal(state.states.value.main.state, 'Testing…')
        if (reset === 'refresh') {
          settings.value = { models: [{ id: 'main', configured: true, provider: 'openai-compatible', baseURL: 'https://example.test/v1', model: 'chat' }] }
          await nextTick()
        } else {
          state.invalidate() // Opening and cancelling the editor invalidate the same generation.
          state.invalidate()
        }
        assert.equal(state.states.value.main, undefined)
        const retry = state.test('main')
        assert.equal(requests.length, 2)
        if (oldFail) requests[0].reject(new Error('Old request failed'))
        else requests[0].resolve({ ok: true })
        await oldTest
        assert.equal(state.states.value.main.state, 'Testing…', 'old completion cannot overwrite the retry')
        if (retryFail) requests[1].reject(new Error('Retry failed'))
        else requests[1].resolve({ ok: true })
        await retry
        if (retryFail) {
          assert.equal(state.states.value.main.state, 'Test failed')
          assert.equal(state.states.value.main.error, 'Retry failed')
        } else {
          assert.equal(state.states.value.main.state, 'Test passed')
        }
        stopWatchingSettings()
      }
    }
  }
})

test('shows model actions and states in a table even with a saved card preference', async () => {
  const previousWindow = globalThis.window
  globalThis.window = { localStorage: { getItem: key => key === 'railgrid:app-studio:models:layout' ? 'grid' : null } }
  try {
    const html = await render({
      settings: {
        provider: 'openai-compatible', baseURL: '', model: 'gpt-6-sol', configured: true,
        models: [{ id: 'sol', name: 'Sol', model: 'gpt-6-sol', baseURL: 'https://api.openai.com/v1', configured: true, default: true }],
      },
    })
    assert.match(html, /<table/)
    assert.doesNotMatch(html, /k-model-grid/)
    assert.doesNotMatch(html, /Model layout:/)
    assert.match(html, /aria-label="Edit Sol"/)
    assert.match(html, /aria-label="Test connection for Sol"/)
    assert.match(html, /aria-label="Delete Sol"/)
    assert.match(html, /Not tested/)
    assert.match(html, /Default/)
  } finally {
    if (previousWindow === undefined) delete globalThis.window
    else globalThis.window = previousWindow
  }
})

test('offers all Agents chat provider paths and keeps Google AI Studio', async () => {
  const html = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  for (const label of ['OpenAI', 'Anthropic', 'OpenRouter', 'Custom OpenAI-compatible', 'Google AI Studio / Vertex AI']) {
    assert.ok(html.includes(label), `missing provider option: ${label}`)
  }
})

test('provider key activation rejects insufficient revision capacity before changing the active spec', async () => {
  const registry = await readFile(new URL('./llmRegistry.ts', import.meta.url), 'utf8')
  const ast = ts.createSourceFile('llmRegistry.ts', registry, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const compile = (name, dependencies) => {
    const declaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name)
    assert.ok(declaration, name)
    const { outputText } = ts.transpileModule(declaration.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
    return new Function(...Object.keys(dependencies), `${outputText.replace(/^export /gm, '')}; return ${name}`)(...Object.values(dependencies))
  }
  let models = []
  const capacity = compile('assertConnectionRolloverCapacity', {
    readStudio: async () => ({ spec: { llm: { models } } }), MAX_REVISIONS: 200, LLMRegistryError: Error,
  })
  const history = count => Array.from({ length: count }, (_, index) => ({ id: `old-${index}`, archived: true }))
  models = [...history(199), { id: 'active', connectionRef: 'primary' }]
  await assert.rejects(capacity({}, 'primary'), /no room/)
  models = [...history(198), { id: 'active', connectionRef: 'primary' }]
  await capacity({}, 'primary')
  models = [...history(197), { id: 'a', connectionRef: 'primary' }, { id: 'b', connectionRef: 'primary' }]
  await assert.rejects(capacity({}, 'primary'), /no room/)
  models = history(200)
  await capacity({}, 'unused')

  const pending = 'ai.railgrid.ai/pending-key-secret'
  const spec = { provider: 'openai-compatible', baseURL: 'https://provider.example/v1', secretRef: { name: 'old-key' } }
  const existing = { metadata: { name: 'primary', uid: 'uid', resourceVersion: '1', annotations: { [pending]: 'new-key' } }, spec }
  let patches = 0
  const client = { get: async () => existing, patch: async () => { patches++; throw new Error('must not activate') } }
  const activate = compile('activateConnectionSecret', {
    MODEL_PROVIDER_CONNECTION_RESOURCE: {}, PENDING_SECRET_ANNOTATION: pending, LLMRegistryError: Error,
    sameJSONValue: (a, b) => JSON.stringify(a) === JSON.stringify(b), assertConnectionRolloverCapacity: capacity,
  })
  models = [...history(199), { id: 'active', connectionRef: 'primary' }]
  await assert.rejects(activate(client, 'primary', 'uid', spec, { ...spec, secretRef: { name: 'new-key' } }, { id: 'new', name: 'new-key' }), /no room/)
  assert.equal(patches, 0, 'a registry that fills during key staging must not activate the replacement')

  let reservations = 0
  let createdKeys = 0
  const save = compile('saveModelProviderConnection', {
    kube: () => client, MODEL_PROVIDER_CONNECTION_RESOURCE: {}, LLMRegistryError: Error,
    expectedConnectionMatchesLatest: () => true, metadataDisplayNamePatch: () => undefined,
    assertConnectionRolloverCapacity: capacity,
    newProviderConnectionSecret: () => { createdKeys++; throw new Error('must not create') },
    reserveConnectionKey: () => { reservations++; throw new Error('must not reserve') },
  })
  await assert.rejects(save(null, { name: 'primary', provider: spec.provider, baseURL: spec.baseURL, apiKey: 'replacement-key' }, {}), /no room/)
  assert.equal(createdKeys, 0)
  assert.equal(reservations, 0, 'the initial capacity check must leave the existing connection untouched')
})

test('key activation retries a status-only race and stops on a concurrent edit', async () => {
  const { KubeError } = await vite.ssrLoadModule('/src/portalkit/kube.ts')
  const source = await readFile(new URL('./llmRegistry.ts', import.meta.url), 'utf8')
  const ast = ts.createSourceFile('registry.ts', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const fn = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'activateConnectionSecret')
  const { outputText } = ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const pending = 'ai.railgrid.ai/pending-key-secret', generated = 'ai.railgrid.ai/generated-secret-id'
  const activate = new Function('KubeError', 'MODEL_PROVIDER_CONNECTION_RESOURCE', 'PENDING_SECRET_ANNOTATION',
    'GENERATED_SECRET_ANNOTATION', 'CONNECTION_DISPLAY_NAME_ANNOTATION', 'LLMRegistryError', 'sameJSONValue',
    'assertConnectionRolloverCapacity', `${outputText}; return activateConnectionSecret`)(
    KubeError, {}, pending, generated, 'display-name', Error,
    (a, b) => JSON.stringify(a) === JSON.stringify(b), async () => {},
  )
  const spec = { provider: 'openai-compatible', baseURL: 'https://provider.example/v1', secretRef: { name: 'old-key' } }
  const desired = { ...spec, secretRef: { name: 'new-key' } }
  const secret = { id: 'new-id', name: 'new-key' }
  for (const race of ['status', 'spec', 'label', 'same-version']) {
    let current = { metadata: { name: 'primary', uid: 'uid', resourceVersion: '1', generation: 1, annotations: { [pending]: secret.name } }, spec }
    const versions = []
    const rejected = new KubeError('PATCH', '/connection', 422, null, 'the server rejected our request due to an error in our request')
    const client = { get: async () => current, patch: async (_resource, _name, ops) => {
      versions.push(ops.find(op => op.path === '/metadata/resourceVersion').value)
      if (versions.length === 1) {
        current = { ...current, metadata: { ...current.metadata, resourceVersion: race === 'same-version' ? '1' : '2' } }
        if (race === 'status') current.status = { observedGeneration: 1 }
        if (race === 'spec') current.spec = { ...spec, baseURL: 'https://other.example/v1' }
        if (race === 'label') current.metadata.annotations = { ...current.metadata.annotations, 'display-name': 'Concurrent label' }
        throw rejected
      }
      current = { metadata: { ...current.metadata, resourceVersion: '3', generation: 2, annotations: { [generated]: secret.id } }, spec: desired,
        status: { observedGeneration: 2, connectionFingerprint: 'fp', activeSnapshot: { secretRef: desired.secretRef, generatedSecretID: secret.id, connectionFingerprint: 'fp' } } }
      return current
    } }
    const result = activate(client, 'primary', 'uid', spec, desired, secret)
    if (race === 'status') {
      assert.deepEqual((await result).spec, desired)
      assert.deepEqual(versions, ['1', '2'])
    } else {
      await assert.rejects(result, race === 'same-version' ? error => error === rejected : /connection changed/)
      assert.deepEqual(versions, ['1'])
    }
  }
})

test('pending activation uses warning copy and does not offer an unadmitted binding', async () => {
  const html = await render({ routePage: true, providerConnectionsRoute: true, connections: [{ ...savedConnection, activationPending: true, ready: false }] })
  assert.match(html, /Changes pending/)
  const source = await readFile(new URL('./llmRegistry.ts', import.meta.url), 'utf8')
  assert.match(source, /active\?\.secretRef\?\.name === secret\.name/)
  assert.match(source, /condition\.reason === 'RevisionHistoryFull'/)
})

test('a capacity race saves pending desired state without admitting its key', async () => {
  const source = await readFile(new URL('./llmRegistry.ts', import.meta.url), 'utf8')
  const ast = ts.createSourceFile('registry.ts', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const compile = (name, deps) => {
    const fn = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === name)
    const { outputText } = ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
    return new Function(...Object.keys(deps), `${outputText}; return ${name}`)(...Object.values(deps))
  }
  const pending = 'ai.railgrid.ai/pending-key-secret', generated = 'ai.railgrid.ai/generated-secret-id'
  const active = { connectionRef: 'primary', connectionUID: 'uid', connectionFingerprint: 'old-fp', provider: 'openai-compatible', baseURL: 'https://old.example/v1', secretRef: { name: 'old-key' } }
  const oldSpec = { provider: active.provider, baseURL: active.baseURL, secretRef: active.secretRef }
  const desired = { ...oldSpec, baseURL: 'https://new.example/v1', secretRef: { name: 'new-key' } }
  const models = [...Array.from({ length: 198 }, () => ({ archived: true })), { id: 'fast', connectionRef: 'primary' }]
  let current = { metadata: { name: 'primary', uid: 'uid', resourceVersion: '1', generation: 1, annotations: { [pending]: 'new-key' } }, spec: oldSpec }
  const capacity = compile('assertConnectionRolloverCapacity', { readStudio: async () => ({ spec: { llm: { models } } }), MAX_REVISIONS: 200, LLMRegistryError: Error })
  const client = { get: async () => current, patch: async () => {
    models.push({ archived: true }) // a different Studio edit fills history
    current = { metadata: { name: 'primary', uid: 'uid', resourceVersion: '2', generation: 2, annotations: { [generated]: 'new-id' } }, spec: desired,
      status: { observedGeneration: 2, activeSnapshot: active, connectionFingerprint: 'new-fp', conditions: [{ type: 'BindingsReady', status: 'False', reason: 'RevisionHistoryFull' }] } }
    return current
  } }
  const activate = compile('activateConnectionSecret', { MODEL_PROVIDER_CONNECTION_RESOURCE: {}, PENDING_SECRET_ANNOTATION: pending, GENERATED_SECRET_ANNOTATION: generated, CONNECTION_DISPLAY_NAME_ANNOTATION: 'display-name', LLMRegistryError: Error, sameJSONValue: (a, b) => JSON.stringify(a) === JSON.stringify(b), assertConnectionRolloverCapacity: capacity })
  const saved = await activate(client, 'primary', 'uid', oldSpec, desired, { id: 'new-id', name: 'new-key' })
  assert.equal(models.length, 200)
  assert.equal(saved.spec.secretRef.name, 'new-key')
  assert.equal(saved.status.activeSnapshot.secretRef.name, 'old-key')
  const readSource = await readFile(new URL('./llmRegistryRead.ts', import.meta.url), 'utf8')
  const readAST = ts.createSourceFile('read.ts', readSource, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const fn = readAST.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'connectionView')
  const js = ts.transpileModule(fn.getText(readAST), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText.replace(/^export /gm, '')
  const view = new Function('GENERATED_SECRET_ANNOTATION', 'CONNECTION_DISPLAY_NAME_ANNOTATION', `${js}; return connectionView`)(generated, 'display-name')
  assert.equal(view(saved).activationPending, true)
  const normalized = view({ ...saved, spec: { ...oldSpec, secretRef: { name: ' old-key ' } }, status: { ...saved.status, activeSnapshot: active, connectionFingerprint: active.connectionFingerprint } })
  assert.equal(normalized.activationPending, false, 'Secret references use the same normalization as backend admission')
  assert.equal(normalized.secretName, 'old-key')

  const settingsFunction = readAST.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'toSettings')
  const settingsJS = ts.transpileModule(settingsFunction.getText(readAST), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText.replace(/^export /gm, '')
  const toSettings = new Function('lookupCatalog', `${settingsJS}; return toSettings`)(() => undefined)
  const settings = toSettings({ metadata: { name: 'studio' }, spec: { llm: { models: [{ id: 'fast', revisionID: 'old-revision', name: 'Fast', model: 'fixture-fast', provider: desired.provider, baseURL: desired.baseURL, connectionRef: 'primary', connectionSnapshot: active }] } } }, [view(saved)])
  assert.equal(settings.models[0].baseURL, active.baseURL, 'model cards describe the admitted snapshot, including in-place migrated rows')

})


test('discovery failures keep credentials in place unless the endpoint is unsupported', async () => {
  const source = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  const script = source.slice(source.indexOf('>', source.indexOf('<script')) + 1, source.indexOf('</script>'))
  const ast = ts.createSourceFile('ModelsSettings.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const declaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'continueConnection')
  const { outputText } = ts.transpileModule(declaration.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  const { ProjectAPIRequestError } = await vite.ssrLoadModule('/src/api.ts')
  for (const reason of ['BadGateway', 'Forbidden', 'DiscoveryUnsupported']) {
    let focused = false
    const state = {
      connectionError: ref(''), connectionNotice: ref(''), connectionCanContinue: ref(true),
      connectionCredentialMode: ref('api-key'), connectionStage: ref('details'),
      connectionRequestSerial: 0, connectionDraftFingerprint: ref('draft'), connectionBusy: ref(false),
      connectionDiscoverySucceeded: ref(false), connectionDiscoveredModels: ref([]),
      connectionAPIKey: ref('typed-key'), props: { ctx: null }, draftProbeBody: () => ({}),
      connectionCredentialRef: ref({ focus: () => { focused = true } }),
    }
    const api = { discoverLLMModels: async () => { throw new ProjectAPIRequestError('Discovery failed', 502, reason) } }
    const run = new Function('state', 'api', 'ProjectAPIRequestError', 'nextTick', `with (state) { ${outputText}; return continueConnection }`)(state, api, ProjectAPIRequestError, callback => callback())
    await run()
    assert.equal(state.connectionStage.value, reason === 'DiscoveryUnsupported' ? 'verify' : 'details')
    assert.equal(focused, reason !== 'DiscoveryUnsupported')
    assert.equal(state.connectionAPIKey.value, 'typed-key')
    assert.equal(state.connectionBusy.value, false)
    assert.equal(state.connectionDiscoverySucceeded.value, false)
    assert.equal(state.connectionError.value, reason === 'DiscoveryUnsupported' ? '' : 'Discovery failed')
  }
})

async function connectionWatchSource(marker, declarations = ['isLLMConnectionSnapshotReady', 'llmConnectionStatusPollIsCurrent', 'pollLLMConnectionStatus']) {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
  const script = app.slice(app.indexOf('>', app.indexOf('<script')) + 1, app.indexOf('</script>'))
  const ast = ts.createSourceFile('App.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const watcher = ast.statements.find(n => ts.isExpressionStatement(n) && n.getText(ast).startsWith('watch(') && n.getText(ast).includes(marker))
  const functions = ast.statements.filter(n =>
    (ts.isFunctionDeclaration(n) && declarations.includes(n.name?.text))
    || (ts.isVariableStatement(n) && n.declarationList.declarations.some(d => declarations.includes(d.name.getText(ast)))))
  const compile = text => ts.transpileModule(text, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  return { watcher: compile(watcher.getText(ast)).trim().replace(/;$/, ''), functions: compile(functions.map(n => n.getText(ast)).join('\n')) }
}

test('identical pending responses reach the poll timeout without restarting', async () => {
  const source = await connectionWatchSource('llmConnectionStatusPollSerial += 1')
  const pending = { ...savedConnection, observedGeneration: 0 }
  let reads = 0
  const state = {
    llmEditorOpen: ref(true), isCreateModelRoute: ref(false), llmEditingModelID: ref(null), llmConnectionStatusPollRequested: ref(true), llmNameOnlyEdit: ref(false),
    llmBindingConnectionRef: ref(pending.name), llmConnections: ref([pending]), routePath: ref('create/model'), props: { ctx: {} }, appContextFingerprint: () => 'workspace',
    llmConnectionStatusPollSerial: 0, llmConnectionStatusChecking: ref(false), llmConnectionStatusTimedOutFor: ref(''),
    loadModelProviderConnections: async () => { reads += 1; assert.ok(reads <= 15); state.llmConnections.value = [{ ...pending }]; await nextTick() },
  }
  const { stop, tasks } = new Function('state', 'watch', 'setTimeout', `with (state) {
    ${source.functions}
    const tasks = []; const runPoll = pollLLMConnectionStatus;
    pollLLMConnectionStatus = (...args) => { const task = runPoll(...args); tasks.push(task); return task; };
    const stop = ${source.watcher}; return { stop, tasks };
  }`)(state, watch, callback => queueMicrotask(callback))
  try {
    await tasks[0]
    assert.equal(tasks.length, 1)
    assert.equal(reads, 15)
    assert.equal(state.llmConnectionStatusChecking.value, false)
    assert.equal(state.llmConnectionStatusTimedOutFor.value, pending.name)
  } finally { stop() }
})

test('discovers missing models when the selected saved key is confirmed', async () => {
  const { functions, watcher } = await connectionWatchSource('!connection.models?.length', [
    'llmDiscoveryConnection', 'clearLLMDiscovery', 'discoverLLMModels',
  ])
  const pending = { ...savedConnection, observedGeneration: 0, models: [] }
  const cached = { ...savedConnection, name: 'cached', uid: 'cached-uid', connectionFingerprint: 'cached-fingerprint' }
  const vertex = { ...savedConnection, name: 'vertex', uid: 'vertex-uid', credentialMode: 'service-account-json', models: [] }
  const requests = []
  const state = {
    props: { ctx: {} }, hasProjectRequestTransport: () => true,
    isModelEditorPage: ref(true), routePath: ref('create/model'), llmEditorBaseline: ref({}),
    llmConnections: ref([pending, cached, vertex]), llmBindingConnectionRef: ref(pending.name),
    llmDiscoverySerial: 0, llmDiscoveryLoading: ref(false), llmDiscoveredModels: ref([]),
    llmDiscoveryComplete: ref(false), llmDiscoveryError: ref(null),
    api: {
      discoverModelProviderConnection: (_ctx, name) => new Promise((resolve, reject) => requests.push({ name, resolve, reject })),
    },
  }
  const { stop, discoverLLMModels } = new Function('state', 'computed', 'watch', `with (state) {
    ${functions}
    const stop = ${watcher};
    return { stop, discoverLLMModels };
  }`)(state, computed, watch)
  try {
    state.llmConnections.value = [{ ...pending, resourceVersion: '13' }, cached, vertex]
    await nextTick()
    assert.equal(requests.length, 0)

    const confirmed = { ...pending, observedGeneration: pending.generation, activationPending: true }
    state.llmConnections.value = [confirmed, cached, vertex]
    await nextTick()
    assert.deepEqual(requests.map(request => request.name), [confirmed.name])
    assert.equal(state.llmDiscoveryLoading.value, true)
    await discoverLLMModels()
    state.llmConnections.value = [{ ...confirmed, resourceVersion: '14' }, cached, vertex]
    await nextTick()
    assert.equal(requests.length, 1)

    state.llmBindingConnectionRef.value = cached.name
    await nextTick()
    requests[0].resolve({ models: [{ id: 'stale' }] })
    await nextTick()
    assert.equal(requests.length, 1)
    assert.deepEqual(state.llmDiscoveredModels.value, [])
    assert.equal(state.llmDiscoveryLoading.value, false)

    state.llmBindingConnectionRef.value = vertex.name
    await nextTick()
    assert.equal(requests.length, 1)

    state.llmBindingConnectionRef.value = confirmed.name
    await nextTick()
    const model = { id: 'discovered-model' }
    requests[1].resolve({ models: [model] })
    await nextTick()
    assert.deepEqual(state.llmDiscoveredModels.value, [model])
    assert.equal(state.llmDiscoveryComplete.value, true)

    const refresh = discoverLLMModels()
    requests[2].reject(new Error('Provider unavailable'))
    await refresh
    assert.equal(state.llmDiscoveryError.value, 'Provider unavailable')
    assert.deepEqual(state.llmDiscoveredModels.value, [model])

    state.llmConnections.value = [{ ...confirmed, connectionFingerprint: 'rotated-key' }, cached, vertex]
    await nextTick()
    assert.deepEqual(state.llmDiscoveredModels.value, [])
    assert.equal(requests.length, 4)
    requests[3].resolve({ models: [] })
    await nextTick()
    assert.equal(state.llmDiscoveryComplete.value, true)

    state.isModelEditorPage.value = false
    state.llmConnections.value = [{ ...confirmed, connectionFingerprint: 'another-key' }, cached, vertex]
    await nextTick()
    assert.equal(requests.length, 4)
  } finally { stop() }
})

test('deleting a pending connection removes its row and activation warning', async () => {
  const app = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  const script = app.slice(app.indexOf('>', app.indexOf('<script')) + 1, app.indexOf('</script>'))
  const ast = ts.createSourceFile('ModelsSettings.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const rows = ast.statements.find(n => ts.isVariableStatement(n) && n.declarationList.declarations.some(d => d.name.getText(ast) === 'connectionRows'))
  const watcher = ast.statements.find(n => ts.isExpressionStatement(n) && n.getText(ast).startsWith('watch(() => props.connections,'))
  const declarations = ts.transpileModule(`${rows.getText(ast)}\nconst stop = ${watcher.getText(ast)}`, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText
  const props = { connections: ref([{ ...savedConnection }]) }
  const reactiveProps = { get connections() { return props.connections.value } }
  const pending = ref({ ...savedConnection, activationPending: true })
  const computed = fn => ({ get value() { return fn() } })
  const state = new Function('props', 'pendingConnection', 'computed', 'watch', `${declarations}; return { rows: connectionRows, stop }`)(reactiveProps, pending, computed, watch)
  try {
    assert.equal(state.rows.value.length, 1)
    props.connections.value = [{ ...savedConnection }]
    await nextTick()
    assert.ok(pending.value, 'collection refresh retains the pending warning')
    props.connections.value = []
    await nextTick()
    assert.deepEqual(state.rows.value, [])
    assert.equal(pending.value, null)
    props.connections.value = []
    await nextTick()
    assert.deepEqual(state.rows.value, [])
  } finally { state.stop() }
})


test('discovery guidance only appears in the existing-connection editor', async () => {
  const source = await readFile(new URL('./ModelsSettings.vue', import.meta.url), 'utf8')
  const script = source.slice(source.indexOf('>', source.indexOf('<script')) + 1, source.indexOf('</script>'))
  const ast = ts.createSourceFile('ModelsSettings.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  const declaration = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'continueConnection')
  const { outputText } = ts.transpileModule(declaration.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } })
  for (const editing of [false, true]) {
    const state = {
      connectionError: ref(''), connectionNotice: ref(''), connectionCanContinue: ref(true),
      connectionCredentialMode: ref('api-key'), connectionStage: ref('details'),
      connectionRequestSerial: 0, connectionDraftFingerprint: ref('draft'), connectionBusy: ref(false),
      connectionDiscoverySucceeded: ref(false), connectionDiscoveredModels: ref([]),
      connectionOriginal: ref(editing ? savedConnection : null), connectionProvider: ref('openai'),
      connectionEndpoint: ref('https://api.openai.com/v1'), connectionWorkspaceID: ref(''),
      connectionDisplayName: ref(''), connectionPreset: ref('openai'), connectionRows: ref([]),
      connectionAPIKey: ref('typed-key'), props: { ctx: null }, draftProbeBody: () => ({}),
      connectionDisplayNameForSave: () => 'OpenAI',
    }
    let saves = 0
    const api = {
      discoverLLMModels: async () => ({ models: [{ id: 'model' }] }),
      saveModelProviderConnection: async () => { saves += 1; return savedConnection },
    }
    const acceptSavedConnection = () => {
      assert.equal(state.connectionNotice.value, '', 'no inaccessible choice is set before closing the new-connection form')
    }
    const run = new Function('state', 'api', 'acceptSavedConnection', `with (state) { ${outputText}; return continueConnection }`)(state, api, acceptSavedConnection)
    await run()
    assert.equal(state.connectionError.value, '')
    assert.equal(saves, editing ? 0 : 1)
    assert.equal(state.connectionNotice.value, editing ? '1 model found. Choose one to verify it, or save the changes.' : '')
    assert.equal(state.connectionStage.value, editing ? 'verify' : 'details')
  }
})
