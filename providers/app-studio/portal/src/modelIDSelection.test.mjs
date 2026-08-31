import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

const source = await readFile(new URL('./modelIDSelection.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
})
const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
const { filterDiscoveredModels, modelSelectorOptions } = await import(moduleURL)

const catalog = [
  { id: 'gpt-5.4', name: 'GPT 5.4', compatibility: 'recommended' },
  { id: 'gpt-5.4-nano', name: 'GPT 5.4 Nano', compatibility: 'available' },
  { id: 'gpt-5.6', name: 'GPT 5.6', compatibility: 'recommended' },
  { id: 'o4-mini', name: 'o4 mini', compatibility: 'available' },
  { id: 'text-embedding-3-large', name: 'Text Embedding 3 Large', compatibility: 'unsuitable' },
]

test('an empty selector query exposes the complete discovered catalog', () => {
  const options = modelSelectorOptions(catalog, '')
  assert.deepEqual(options.map(option => option.value), catalog.map(model => model.id))
  assert.equal(options.at(-1).disabled, true)
})

test('search filters by model ID or display name without changing the catalog', () => {
  assert.deepEqual(filterDiscoveredModels(catalog, '5.4').map(model => model.id), ['gpt-5.4', 'gpt-5.4-nano'])
  assert.deepEqual(filterDiscoveredModels(catalog, 'O4 MINI').map(model => model.id), ['o4-mini'])
  assert.equal(catalog.length, 5)
})

test('manual IDs are explicit options and exact discovered IDs are not duplicated', () => {
  const custom = modelSelectorOptions(catalog, 'vendor/new-chat-model')
  assert.deepEqual(custom[0], {
    key: 'manual:vendor/new-chat-model',
    value: 'vendor/new-chat-model',
    label: 'vendor/new-chat-model',
    manual: true,
    disabled: false,
  })
  const exact = modelSelectorOptions(catalog, 'gpt-5.6')
  assert.equal(exact.filter(option => option.value === 'gpt-5.6').length, 1)
  assert.equal(exact[0].manual, false)

  const partial = modelSelectorOptions(catalog, 'gpt-5.4-n')
  assert.equal(partial[0].value, 'gpt-5.4-nano')
  assert.equal(partial.at(-1).value, 'gpt-5.4-n')
  assert.equal(partial.at(-1).manual, true)
})
