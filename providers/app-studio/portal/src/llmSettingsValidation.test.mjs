import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./llmSettingsValidation.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const { hasLLMModelFormErrors, validateLLMBaseURL, validateLLMModelForm } = await import(moduleURL)

test('accepts an OpenAI-compatible API base URL', () => {
  assert.equal(validateLLMBaseURL('openai-compatible', 'https://opencode.ai/zen/v1'), '')
  assert.equal(validateLLMBaseURL('openai-compatible', 'http://localhost:11434/v1'), '')
})

test('rejects a full chat completions endpoint with a corrected base URL', () => {
  assert.equal(
    validateLLMBaseURL('openai-compatible', 'https://opencode.ai/zen/v1/chat/completions/'),
    'Enter the API base URL, not the chat completions endpoint. Use https://opencode.ai/zen/v1; App Studio adds /chat/completions automatically.',
  )
})

test('explains that non-chat OpenAI endpoints are unsupported', () => {
  assert.equal(
    validateLLMBaseURL('openai-compatible', 'https://opencode.ai/zen/v1/responses'),
    'This endpoint uses /responses, but the OpenAI-compatible provider requires a /chat/completions model. Choose a compatible model and enter its base URL.',
  )
  assert.match(
    validateLLMBaseURL('openai-compatible', 'https://example.test/v1/messages'),
    /requires a \/chat\/completions model/,
  )
})

test('rejects malformed and non-HTTP base URLs', () => {
  assert.equal(validateLLMBaseURL('openai-compatible', 'not-a-url'), 'Enter an absolute HTTP(S) base URL.')
  assert.equal(validateLLMBaseURL('openai-compatible', 'file:///tmp/model'), 'Base URL must use HTTP or HTTPS.')
})

test('validates Google URL syntax while leaving its operation paths alone', () => {
  assert.equal(validateLLMBaseURL('google-ai-studio', 'https://example.test/v1/responses'), '')
  assert.equal(validateLLMBaseURL('google-ai-studio', ''), '')
  assert.equal(validateLLMBaseURL('google-ai-studio', 'not-a-url'), 'Enter an absolute HTTP(S) base URL.')
  assert.equal(validateLLMBaseURL('google-ai-studio', 'file:///tmp/model'), 'Base URL must use HTTP or HTTPS.')
})

test('requires a credential when connecting a model while preserving blank-on-edit semantics', () => {
  const input = {
    name: 'GPT High',
    provider: 'openai-compatible',
    credentialMode: 'api-key',
    baseURL: 'https://api.openai.com/v1',
    model: 'gpt-5.4',
    credential: '',
    existingModels: [],
  }
  const creating = validateLLMModelForm(input)
  assert.equal(creating.credential, 'Enter a credential to connect this model.')
  assert.equal(hasLLMModelFormErrors(creating), true)

  const editing = validateLLMModelForm({ ...input, editingModelID: 'gpt-high' })
  assert.equal(editing.credential, '')
  assert.equal(hasLLMModelFormErrors(editing), false)

  const repairingIncomplete = validateLLMModelForm({ ...input, editingModelID: 'gpt-high', credentialRequired: true })
  assert.equal(repairingIncomplete.credential, 'Enter a credential to connect this model.')
})

test('reports field-specific name and model errors before the API request', () => {
  const errors = validateLLMModelForm({
    name: 'GPT High!',
    provider: 'openai-compatible',
    credentialMode: 'api-key',
    baseURL: 'https://api.openai.com/v1',
    model: '',
    credential: 'secret',
    existingModels: [{ id: 'gpt-high', name: 'GPT High' }],
  })
  assert.equal(errors.name, 'A model with this display name already exists.')
  assert.equal(errors.model, 'Enter the provider’s exact model ID.')
})

test('validates Google credential shape near the credential field', () => {
  const base = {
    name: 'Gemini Fast',
    provider: 'google-ai-studio',
    baseURL: 'https://aiplatform.googleapis.com',
    model: 'google/gemini-3.5-flash',
    existingModels: [],
  }
  assert.equal(
    validateLLMModelForm({ ...base, credentialMode: 'service-account-json', credential: '{"type":"service_account"}' }).credential,
    'Paste a complete Google service-account key. Missing: project_id, client_email, private_key, token_uri.',
  )
  assert.equal(
    validateLLMModelForm({ ...base, credentialMode: 'api-key', credential: 'ya29.oauth-token' }).credential,
    'Enter a Gemini API key, not an OAuth or JWT token.',
  )
})
