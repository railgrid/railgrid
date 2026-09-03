import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'

const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/faros-vite-app-studio-models',
  configFile: false,
  plugins: [vue()],
  server: { middlewareMode: true, hmr: false },
})
const { default: ModelsSettings } = await vite.ssrLoadModule('/src/ModelsSettings.vue')
test.after(async () => vite.close())

const baseProps = {
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
  provider: 'openai-compatible',
  providerPreset: 'openai',
  credentialMode: 'api-key',
  baseURL: 'https://api.openai.com/v1',
  model: 'gpt-5.4',
  apiKey: '',
  nameError: '',
  baseURLError: '',
  modelError: '',
  credentialError: '',
  credentialRequired: true,
  baseURLPlaceholder: 'Base URL',
  apiKeyPlaceholder: 'API key',
  apiKeyHint: '',
  providerGuidance: 'Use an OpenAI-compatible provider.',
  modelHint: 'Use the provider model ID.',
  googleProvider: false,
  googleServiceAccountMode: false,
  customProvider: false,
  discoveredModels: [],
  discoveryLoading: false,
  discoveryError: null,
  discoveryStatus: null,
  canDiscover: false,
  testing: false,
  testStatus: null,
  testError: null,
  requireConnectionTest: false,
  connectionTested: false,
}

async function render(props = {}) {
  return renderToString(createSSRApp(ModelsSettings, { ...baseProps, ...props }))
}

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

  assert.match(html, /aria-label="Model GPT High"/)
  assert.match(html, /aria-label="Model Gemini Fast"/)
  assert.match(html, /gpt-5\.4/)
  assert.match(html, /Default/)
  assert.match(html, /Credential saved/)
  assert.match(html, /Needs credential/)
  assert.match(html, /Make default/)
  assert.doesNotMatch(html, /aria-label="Model configuration form"/)
})

test('uses an explicit empty state before opening the model form', async () => {
  const html = await render()

  assert.match(html, /No models configured/)
  assert.match(html, /Connect model/)
  assert.doesNotMatch(html, /aria-label="Model configuration form"/)
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

  assert.match(html, /aria-label="Connect model"/)
  assert.match(html, /aria-label="Model configuration form"/)
  assert.doesNotMatch(html, /aria-label="Model GPT High"/)
  assert.doesNotMatch(html, />Connect model<\/button>/)
})

test('route-owned model creation keeps the form actionable when settings load fails', async () => {
  const html = await render({
    creationRoute: true,
    loadError: 'Could not load model settings.',
    settings: null,
  })

  assert.match(html, /Could not load model settings\./)
  assert.match(html, />Retry</)
  assert.match(html, /aria-label="Model configuration form"/)
  assert.match(html, /Display name/)
  assert.doesNotMatch(html, />New model</)
  assert.doesNotMatch(html, /No models configured/)
})

test('renders a guided provider, endpoint, and credential form', async () => {
  const html = await render({
    editorOpen: true,
    name: 'Gemini Fast',
    provider: 'google-ai-studio',
    providerPreset: 'google',
    credentialMode: 'service-account-json',
    baseURL: 'https://aiplatform.googleapis.com',
    model: 'google/gemini-3.5-flash',
    apiKeyPlaceholder: 'Service account JSON',
    apiKeyHint: 'Paste the Google service-account JSON key.',
    providerGuidance: 'Use a Gemini API key for Google AI Studio, or a service-account key for Vertex AI.',
    modelHint: 'Use the exact Vertex AI model identifier, including its publisher prefix.',
    googleProvider: true,
    googleServiceAccountMode: true,
  })

  assert.match(html, /aria-label="Model configuration form"/)
  assert.match(html, /id="model-provider"/)
  assert.match(html, /Use a Gemini API key for Google AI Studio/)
  assert.match(html, /Display name/)
  assert.match(html, /Credential method/)
  assert.match(html, /Vertex AI service account/)
  assert.match(html, /Find models/)
  assert.match(html, /Service account JSON/)
  assert.match(html, /Connect model/)
  assert.match(html, /Test connection/)
})

test('offers known provider endpoints and keeps custom endpoints editable', async () => {
  const openAI = await render({ editorOpen: true, providerPreset: 'openai', canDiscover: true })
  assert.match(openAI, />OpenAI</)
  assert.match(openAI, /https:\/\/api\.openai\.com\/v1/)
  assert.doesNotMatch(openAI, /id="model-base-url"/)

  const custom = await render({
    editorOpen: true,
    providerPreset: 'custom',
    customProvider: true,
    baseURL: 'https://gateway.example/v1',
  })
  assert.match(custom, /Custom OpenAI-compatible/)
  assert.match(custom, /id="model-base-url"/)
  assert.match(custom, /queries \/models/)
})

test('renders discovered suggestions while preserving manual model entry', async () => {
  const html = await render({
    editorOpen: true,
    canDiscover: true,
    discoveryStatus: '3 models found; 1 marked unavailable for chat.',
    discoveredModels: [
      { id: 'gpt-5.6', name: 'gpt-5.6', compatibility: 'recommended' },
      { id: 'custom-chat', name: 'custom-chat', compatibility: 'available' },
      { id: 'text-embedding-3-large', name: 'text-embedding-3-large', compatibility: 'unsuitable' },
    ],
  })

  assert.match(html, /Recommended for App Studio/)
  assert.match(html, /id="model-id"/)
  assert.match(html, /aria-haspopup="listbox"/)
  assert.doesNotMatch(html, /<datalist/)
  assert.match(html, /search or enter an ID/)
  assert.match(html, /3 models found; 1 marked unavailable for chat/)
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
    name: 'GPT High',
    settings: {
      provider: 'openai-compatible',
      baseURL: 'https://api.openai.com/v1',
      model: 'gpt-5.4',
      configured: true,
      defaultModelID: 'gpt-high',
      models: [{ id: 'gpt-high', name: 'GPT High', provider: 'openai-compatible', baseURL: 'https://api.openai.com/v1', model: 'gpt-5.4', configured: true, default: true }],
    },
  })

  assert.match(html, /Edit model/)
  assert.match(html, /aria-label="Model configuration form"/)
  assert.match(html, /class="k-create-surface"/)
  assert.match(html, /Save changes/)
  assert.doesNotMatch(html, /aria-label="Model GPT High"/)
})

test('associates field guidance and validation errors with their controls', async () => {
  const html = await render({
    editorOpen: true,
    nameError: 'Enter a display name.',
    modelError: 'Enter the provider’s exact model ID.',
    credentialError: 'Enter a credential to connect this model.',
  })

  assert.match(html, /id="model-display-name"[^>]*aria-invalid="true"[^>]*aria-describedby="model-display-name-error"/)
  assert.match(html, /id="model-id"[^>]*aria-invalid="true"[^>]*aria-describedby="model-id-error"/)
  assert.match(html, /id="model-credential"[^>]*aria-invalid="true"[^>]*aria-describedby="model-credential-help"/)
  assert.match(html, /id="model-display-name"[^>]*border-danger/)
  assert.match(html, /id="model-id"[^>]*border-danger/)
  assert.match(html, /Enter a credential to connect this model\./)
})

test('announces the active model mutation and disables competing card actions', async () => {
  const editing = await render({
    editorOpen: true,
    editingModelID: 'gpt-high',
    name: 'GPT High',
    saving: true,
  })
  assert.match(editing, /aria-busy="true"/)
  assert.match(editing, /Saving changes…/)

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
  assert.match(collection, /<button type="button"[^>]*disabled[^>]*>\s*<svg[^>]*>[\s\S]*?<\/svg> Edit/)
})

test('first-time setup requires a verified model response before save and finish', async () => {
  const html = await render({ creationRoute: true, name: 'OpenAI', apiKey: 'test-key', requireConnectionTest: true })
  assert.match(html, /Test connection/)
  assert.match(html, /Save and finish/)
  assert.match(html, /<button class="k-btn k-btn--primary" disabled>[\s\S]*Save and finish/)

  const verified = await render({
    creationRoute: true,
    name: 'OpenAI',
    apiKey: 'test-key',
    requireConnectionTest: true,
    connectionTested: true,
    testStatus: 'Connection verified. The model responded successfully.',
  })
  assert.match(verified, /Connection verified\. The model responded successfully\./)
})

test('App Studio owns save state while the extracted surface owns presentation', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')

  assert.match(app, /const CREATE_MODEL_ROUTE = 'create\/model'/)
  assert.match(app, /const isCreateModelRoute = computed\(\(\) => routePath\.value === CREATE_MODEL_ROUTE\)/)
  assert.match(app, /const routePath = computed\(\(\) => \(props\.ctx\?\.subPath \?\? ''\)\.split\('\/'\)\.filter\(Boolean\)\.join\('\/'\)\)/)
  assert.match(app, /v-if="showSettings[\s\S]*\(\(isModelsRoute \|\| isCreateModelRoute\) && !\(initializing && !loading\)\)"/)
  assert.match(app, /<ModelsSettings[\s\S]*:creation-route="isCreateModelRoute"/)
  assert.match(app, /<ModelsSettings[\s\S]*@save="saveLLMSettings"[\s\S]*@delete="deleteLLMModel"[\s\S]*@set-default="setDefaultLLMModel"/)
  assert.match(app, /function selectLLMProvider[\s\S]*llmProviderSelection[\s\S]*llmProviderPreset\.value = preset/)
  assert.match(app, /async function discoverLLMModels[\s\S]*api\.discoverLLMModels[\s\S]*existingModelID/)
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
  assert.match(app, /Nested provider tabs have one persisted descriptor rather than their own/)
  assert.match(pageElement, /const navigate = \(path: string, options: NavigationOptions = \{\}\)/)
  assert.match(pageElement, /detail: \{ path, \.\.\.\(options\.replace === true \? \{ replace: true \} : \{\}\) \}/)
})

test('composer exposes the configured model picker and sends its stable ID', async () => {
  const [app, picker] = await Promise.all([
    readFile(new URL('./App.vue', import.meta.url), 'utf8'),
    readFile(new URL('./ModelPicker.vue', import.meta.url), 'utf8'),
  ])
  assert.match(app, /<template #actions>[\s\S]*<ModelPicker[\s\S]*:models="configuredLLMModels"[\s\S]*@select="selectedLLMModelID = \$event"/)
  assert.match(app, /const startOperation = \{[\s\S]*modelID: selectedLLMModelID\.value/)
  assert.match(app, /startAssistantTurn[\s\S]*modelID: payload\.modelID/)
  assert.match(app, /startAssistantReview[\s\S]*modelID: payload\.modelID/)
  assert.match(picker, /aria-label="Choose model"/)
  assert.match(picker, /aria-haspopup="listbox"/)
  assert.match(picker, /role="combobox"/)
  assert.match(picker, /:aria-activedescendant="open \? activeOptionID : undefined"/)
  assert.match(picker, /@keydown="handleTriggerKeydown"/)
  assert.match(picker, /event\.key === 'ArrowDown' \|\| event\.key === 'ArrowUp'/)
  assert.match(picker, /event\.key === 'Home' \|\| event\.key === 'End'/)
  assert.match(picker, /event\.key === 'Escape'/)
  assert.match(picker, /event\.key === 'Tab' && open\.value[\s\S]*closePicker\(false\)/)
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
  assert.match(app, /modelsReturnRoute\.value === CREATE_PROJECT_ROUTE \? CREATE_PROJECT_ROUTE : MODELS_ROUTE/)
  assert.match(app, /function openNewLLMModelEditor\(\)[\s\S]*if \(!modelsReturnRoute\.value && isCreateRoute\.value\) modelsReturnRoute\.value = CREATE_PROJECT_ROUTE/)

  const cancelStart = app.indexOf('async function cancelLLMEditor')
  const saveStart = app.indexOf('async function saveLLMSettings')
  const saveEnd = app.indexOf('\nasync function deleteLLMModel', saveStart)
  assert.match(app.slice(cancelStart, saveStart), /const returnRoute = routeOwnedCreation[\s\S]*modelsReturnRoute\.value = ''[\s\S]*props\.navigate\(returnRoute, \{ replace: true \}\)/)
  assert.match(app.slice(saveStart, saveEnd), /const returnRoute = routeOwnedCreation[\s\S]*modelsReturnRoute\.value = ''[\s\S]*props\.navigate\(returnRoute, \{ replace: true \}\)/)
})
