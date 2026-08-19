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

  assert.match(app, /<ModelsSettings[\s\S]*@save="saveLLMSettings"[\s\S]*@delete="deleteLLMModel"[\s\S]*@set-default="setDefaultLLMModel"/)
  assert.match(app, /function selectLLMProvider[\s\S]*llmBaseURL\.value = GEMINI_BASE_URL[\s\S]*llmBaseURL\.value = 'https:\/\/api\.openai\.com\/v1'/)
  assert.match(app, /async function saveLLMSettings[\s\S]*api\.patchLLMModel[\s\S]*api\.createLLMModel/)
  assert.match(app, /async function deleteLLMModel[\s\S]*api\.deleteLLMModel/)
  assert.match(app, /catch \(e\)[\s\S]*llmActionError\.value = e instanceof Error/)
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
