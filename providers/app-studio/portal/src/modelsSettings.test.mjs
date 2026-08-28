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
  server: { middlewareMode: true },
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
  credentialMode: 'api-key',
  baseURL: 'https://api.openai.com/v1',
  model: 'gpt-5.4',
  apiKey: '',
  baseURLError: '',
  baseURLPlaceholder: 'Base URL',
  apiKeyPlaceholder: 'API key',
  apiKeyHint: '',
  googleProvider: false,
  googleServiceAccountMode: false,
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
  assert.match(html, /Configured/)
  assert.match(html, /Needs credential/)
  assert.match(html, /Make default/)
  assert.doesNotMatch(html, /aria-label="Model configuration form"/)
})

test('uses an explicit empty state before opening the model form', async () => {
  const html = await render()

  assert.match(html, /No models configured/)
  assert.match(html, /New model/)
  assert.match(html, /Add model/)
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

  assert.match(html, /aria-label="New model"/)
  assert.match(html, /aria-label="Model configuration form"/)
  assert.doesNotMatch(html, />New model</)
  assert.doesNotMatch(html, /aria-label="Model GPT High"/)
  assert.doesNotMatch(html, />New model<\/button>/)
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
    credentialMode: 'service-account-json',
    baseURL: 'https://aiplatform.googleapis.com',
    model: 'google/gemini-3.5-flash',
    apiKeyPlaceholder: 'Service account JSON',
    apiKeyHint: 'Paste the Google service-account JSON key.',
    googleProvider: true,
    googleServiceAccountMode: true,
  })

  assert.match(html, /aria-label="Model configuration form"/)
  assert.match(html, /Provider preset/)
  assert.match(html, /Display name/)
  assert.match(html, /Credential method/)
  assert.match(html, /Vertex AI service account/)
  assert.match(html, /Model endpoint/)
  assert.match(html, /Service account JSON/)
  assert.match(html, /Add model/)
})

test('App Studio owns save state while the extracted surface owns presentation', async () => {
  const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')

  assert.match(app, /const CREATE_MODEL_ROUTE = 'create\/model'/)
  assert.match(app, /const isCreateModelRoute = computed\(\(\) => routePath\.value === CREATE_MODEL_ROUTE\)/)
  assert.match(app, /const routePath = computed\(\(\) => \(props\.ctx\?\.subPath \?\? ''\)\.split\('\/'\)\.filter\(Boolean\)\.join\('\/'\)\)/)
  assert.match(app, /v-if="showSettings[\s\S]*\(\(isModelsRoute \|\| isCreateModelRoute\) && !\(initializing && !loading\)\)"/)
  assert.match(app, /<ModelsSettings[\s\S]*:creation-route="isCreateModelRoute"/)
  assert.match(app, /<ModelsSettings[\s\S]*@save="saveLLMSettings"[\s\S]*@delete="deleteLLMModel"[\s\S]*@set-default="setDefaultLLMModel"/)
  assert.match(app, /function selectLLMProvider[\s\S]*llmBaseURL\.value = GEMINI_BASE_URL[\s\S]*llmBaseURL\.value = 'https:\/\/api\.openai\.com\/v1'/)
  assert.match(app, /async function saveLLMSettings[\s\S]*api\.patchLLMModel[\s\S]*api\.createLLMModel/)
  assert.match(app, /async function deleteLLMModel[\s\S]*api\.deleteLLMModel/)
  assert.match(app, /catch \(e\)[\s\S]*llmActionError\.value = e instanceof Error/)
})

test('model creation replaces its route entry and nested navigation accepts replace metadata', async () => {
  const [app, element] = await Promise.all([
    readFile(new URL('./App.vue', import.meta.url), 'utf8'),
    readFile(new URL('./element.ts', import.meta.url), 'utf8'),
  ])

  assert.match(app, /function openNewLLMModelEditor\(\)[\s\S]*props\.navigate\(CREATE_MODEL_ROUTE\)/)
  assert.match(app, /function cancelLLMEditor\(\)[\s\S]*const returnRoute = routeOwnedCreation[\s\S]*props\.navigate\(returnRoute, \{ replace: true \}\)/)
  assert.match(app, /const routeOwnedCreation = isCreateModelRoute\.value && !llmEditingModelID\.value[\s\S]*const returnRoute = routeOwnedCreation[\s\S]*props\.navigate\(returnRoute, \{ replace: true \}\)/)
  assert.match(app, /const detail = \(e as CustomEvent<\{ path\?: unknown; replace\?: unknown \}>\)\.detail/)
  assert.match(app, /Nested provider tabs have one persisted descriptor rather than their own/)
  assert.match(element, /navigate: \(path: string, options\?: NavigationOptions\) => this\.navigate\(path, options\)/)
  assert.match(element, /detail: \{ path, \.\.\.\(options\.replace === true \? \{ replace: true \} : \{\}\) \}/)
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

  const cancelStart = app.indexOf('function cancelLLMEditor')
  const saveStart = app.indexOf('async function saveLLMSettings')
  const saveEnd = app.indexOf('\nasync function deleteLLMModel', saveStart)
  assert.match(app.slice(cancelStart, saveStart), /const returnRoute = routeOwnedCreation[\s\S]*modelsReturnRoute\.value = ''[\s\S]*props\.navigate\(returnRoute, \{ replace: true \}\)/)
  assert.match(app.slice(saveStart, saveEnd), /const returnRoute = routeOwnedCreation[\s\S]*modelsReturnRoute\.value = ''[\s\S]*props\.navigate\(returnRoute, \{ replace: true \}\)/)
})
