import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./llmDiscovery.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const { inferLLMProviderPreset, llmProviderSelection } = await import(moduleURL)

test('infers known providers without changing persisted provider identifiers', () => {
  assert.equal(inferLLMProviderPreset('openai-compatible', 'https://api.openai.com/v1/'), 'openai')
  assert.equal(inferLLMProviderPreset('google-ai-studio', 'https://generativelanguage.googleapis.com'), 'google')
  assert.equal(inferLLMProviderPreset('openai-compatible', 'https://gateway.example/v1'), 'custom')
})

test('maps provider presets to known endpoints and leaves custom endpoints editable', () => {
  assert.deepEqual(llmProviderSelection('openai'), {
    provider: 'openai-compatible',
    baseURL: 'https://api.openai.com/v1',
  })
  assert.deepEqual(llmProviderSelection('google'), {
    provider: 'google-ai-studio',
    baseURL: 'https://generativelanguage.googleapis.com',
  })
  assert.deepEqual(llmProviderSelection('custom', 'https://gateway.example/v1'), {
    provider: 'openai-compatible',
    baseURL: 'https://gateway.example/v1',
  })
  assert.equal(llmProviderSelection('custom', 'https://api.openai.com/v1').baseURL, '')
})
