import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import ts from 'typescript'

async function loadTS(path) {
  const source = await readFile(new URL(path, import.meta.url), 'utf8')
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
  })
  const moduleURL = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`
  return import(moduleURL)
}

const reasoning = await loadTS('./reasoning.ts')
const threadReasoning = await loadTS('./threadReasoning.ts')
const catalog = [
  { id: 'gpt-6-luna', aliases: ['gpt-6-luna-2026-10-01'], efforts: ['low', 'medium', 'high', 'xhigh'] },
  { id: 'o4-mini', efforts: ['low', 'medium', 'high'] },
]
const endpointFixtures = JSON.parse(await readFile(
  new URL('../../../../provider-sdk/modelcatalog/testdata/endpoints.json', import.meta.url),
  'utf8',
))

test('reasoning lookup accepts exact canonical IDs and declared aliases only', () => {
  assert.equal(reasoning.lookupReasoningModel(' GPT-6-LUNA ', catalog)?.id, 'gpt-6-luna')
  assert.equal(reasoning.lookupReasoningModel('gpt-6-luna-2026-10-01', catalog)?.id, 'gpt-6-luna')
  assert.equal(reasoning.lookupReasoningModel('gpt-6-luna-latest', catalog), undefined)
  assert.equal(reasoning.lookupReasoningModel('openai/gpt-6-luna', catalog), undefined)
  assert.equal(reasoning.lookupReasoningModel('anthropic/gpt-6-luna', catalog), undefined)
})

test('reasoning endpoint gate matches the shared Go fixture', () => {
  for (const fixture of endpointFixtures.cases) {
    const connection = {
      provider: fixture.provider,
      baseURL: fixture.baseURL,
      model: 'gpt-6-luna',
    }
    assert.deepEqual(
      reasoning.reasoningEffortsForConnection(connection, catalog),
      fixture.official ? catalog[0].efforts : [],
      fixture.name,
    )
  }
})

test('reasoning levels still require a known model profile', () => {
  assert.deepEqual(reasoning.reasoningEffortsForConnection({ model: 'vendor/unknown' }, catalog), [])
})

test('empty effort inherits the saved model default and stale values require an explicit reset', () => {
  const options = ['low', 'medium', 'high']
  assert.equal(reasoning.isReasoningEffortSupported('', options), true)
  assert.equal(reasoning.isReasoningEffortSupported('medium', options), true)
  assert.equal(reasoning.isReasoningEffortSupported('xhigh', options), false)
  assert.equal(reasoning.reasoningEffortError('', options), '')
  assert.match(reasoning.reasoningEffortError('xhigh', options), /Reset it to Model default/)
  assert.equal(reasoning.reasoningEffortLabel('xhigh'), 'Extra high')
})

test('a stale saved model default blocks admission even when the thread has an explicit override', () => {
  const options = ['low', 'medium', 'high']
  assert.equal(reasoning.reasoningSelectionError('', '', options), '')
  assert.equal(reasoning.reasoningSelectionError('', 'medium', options), '')
  assert.match(reasoning.reasoningSelectionError('', 'xhigh', options), /saved model default.*Update the model settings before sending/)
  assert.match(reasoning.reasoningSelectionError('xhigh', 'medium', options), /Reset it to Model default/)
  assert.match(reasoning.reasoningSelectionError('low', 'xhigh', options), /saved model default.*Update the model settings before sending/)
  assert.match(reasoning.reasoningSelectionError('xhigh', 'xhigh', options), /thread preference and saved model default.*Reset the thread preference and update the model settings/)
})

test('thread reasoning preferences are sparse, keyed by saved model, and empty removes only that key', () => {
  const original = { 'model-a': 'high', 'model-b': 'low' }
  assert.equal(threadReasoning.threadReasoningEffort(original, 'model-a'), 'high')
  assert.equal(threadReasoning.threadReasoningEffort(original, 'model-b'), 'low')
  assert.deepEqual(threadReasoning.updateThreadReasoningEffort(original, 'model-a', ''), { 'model-b': 'low' })
  assert.deepEqual(threadReasoning.updateThreadReasoningEffort(original, 'model-b', 'xhigh'), { 'model-a': 'high', 'model-b': 'xhigh' })
  assert.deepEqual(threadReasoning.initialReasoningEffortByModel('model-a', ''), undefined)
  assert.deepEqual(threadReasoning.initialReasoningEffortByModel('model-a', 'medium'), { 'model-a': 'medium' })
  assert.deepEqual(original, { 'model-a': 'high', 'model-b': 'low' }, 'updates preserve the API snapshot')
})

test('new thread snapshots preserve only the current scoped per-model choices and can clear to automatic', () => {
  const contextA = JSON.stringify(['workspace-a', 'user-a'])
  const contextB = JSON.stringify(['workspace-b', 'user-a'])
  const projectA = JSON.stringify([contextA, 'project-a', 'model-a'])
  const projectB = JSON.stringify([contextA, 'project-b', 'model-a'])
  const projectOtherWorkspace = JSON.stringify([contextB, 'project-a', 'model-c'])
  const projectDrafts = {
    [projectA]: 'high',
    [JSON.stringify([contextA, 'project-a', 'model-b'])]: 'max',
    [projectB]: 'low',
    [projectOtherWorkspace]: 'xhigh',
    malformed: 'medium',
    [JSON.stringify([contextA, 'project-a', 'model-empty'])]: '',
  }

  const scoped = threadReasoning.reasoningEffortsForDraftScope(projectDrafts, [contextA, 'project-a'])
  assert.deepEqual(scoped, { 'model-a': 'high', 'model-b': 'max' })
  assert.deepEqual(
    threadReasoning.initialReasoningEffortSnapshot(scoped, 'model-b', ''),
    { 'model-a': 'high' },
    'selecting Model default removes only that model while preserving sibling choices',
  )
  assert.deepEqual(
    threadReasoning.initialReasoningEffortSnapshot(scoped, 'model-c', 'low'),
    { 'model-a': 'high', 'model-b': 'max', 'model-c': 'low' },
  )
})

test('thread creation drops removed models and stale sibling efforts before server validation', () => {
  const preferences = {
    'model-a': 'high',
    'model-b': 'max',
    'model-removed': 'medium',
    'model-stale': 'xhigh',
  }
  const supported = threadReasoning.supportedReasoningEffortSnapshot(preferences, {
    'model-a': ['low', 'high'],
    'model-b': ['low', 'max'],
  })

  assert.deepEqual(supported, { 'model-a': 'high', 'model-b': 'max' })
  assert.deepEqual(preferences, {
    'model-a': 'high',
    'model-b': 'max',
    'model-removed': 'medium',
    'model-stale': 'xhigh',
  }, 'sanitizing the request does not mutate saved drafts')
  assert.deepEqual(
    threadReasoning.supportedReasoningEffortSnapshot({ 'model-removed': 'medium' }, {}),
    undefined,
    'an empty sanitized snapshot stays sparse',
  )
})

test('pending per-model edits overlay a canonical thread response without crossing thread scope', () => {
  const result = threadReasoning.overlayThreadReasoningPreferenceDrafts(
    { 'model-a': 'low', 'model-b': 'remote' },
    {
      [JSON.stringify(['ctx-a', 'project-a', 'thread-a', 'model-a'])]: '',
      [JSON.stringify(['ctx-a', 'project-a', 'thread-a', 'model-b'])]: 'xhigh',
      [JSON.stringify(['ctx-a', 'project-a', 'thread-b', 'model-c'])]: 'medium',
    },
    'ctx-a',
    'project-a',
    'thread-a',
  )
  assert.deepEqual(result, { 'model-b': 'xhigh' })
})

test('reasoning control delegates to the accessible PortalKit combobox and exposes browser selectors', async () => {
  const component = await readFile(new URL('./ReasoningEffortControl.vue', import.meta.url), 'utf8')
  assert.match(component, /import FormSelect, \{ type FormSelectOption \} from '\.\/portalkit\/FormSelect\.vue'/)
  assert.doesNotMatch(component, /<select\b/)
  assert.match(component, /:labelledby="labelID"/)
  assert.match(component, /:describedby="selectionError \? errorID/)
  assert.match(component, /data-testid="reasoning-effort-control"/)
  assert.match(component, /data-testid="reasoning-effort-error"/)
  assert.match(component, /data-testid="reasoning-effort-reset"/)
  assert.match(component, /v-if="explicitStaleError"/)
  assert.match(component, /data-testid="reasoning-effort-edit-model"/)
})
