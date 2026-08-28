import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const root = new URL('../', import.meta.url)

async function source(path) {
  return readFile(new URL(path, root), 'utf8')
}

function expectSkeleton(name, text, { wide = false } = {}) {
  for (const className of [
    'k-create-page',
    'k-back-action',
    'k-create-header',
    'k-create-title',
    'k-create-description',
    'k-create-surface',
    'k-create-actions',
  ]) {
    assert.match(text, new RegExp(className), `${name} is missing ${className}`)
  }
  if (wide) assert.match(text, /k-create-surface--wide/, `${name} should use the wide provisioning surface`)
  assert.match(text, /Cancel[\s\S]*k-btn--primary/, `${name} must order Cancel before its primary action`)
}

test('the canonical create-page vocabulary defines one shared hierarchy', async () => {
  const css = await source('provider-sdk/portalkit/faros-ui.css')
  for (const selector of [
    '.k-create-page',
    '.k-create-header',
    '.k-create-title',
    '.k-create-description',
    '.k-create-surface',
    '.k-create-surface--wide',
    '.k-create-body',
    '.k-create-actions',
  ]) {
    assert.match(css, new RegExp(selector.replace('.', '\\.')))
  }
  assert.match(css, /\.k-create-surface\s*\{[\s\S]*max-width: 42rem/)
  assert.match(css, /\.k-create-surface\s*\{[\s\S]*overflow: clip/)
  assert.match(css, /\.k-create-actions\s*\{[\s\S]*justify-content: flex-end/)
  assert.match(css, /@media \(max-width: 620px\)[\s\S]*\.k-create-actions\s*\{[\s\S]*position: sticky/)
})

test('Vue route-owned create flows use the shared skeleton', async () => {
  const cases = [
    ['MCP', ['portal/src/pages/MCPPage.vue'], false],
    ['Code connection', ['providers/code/portal/src/views/ConnectionCreateView.vue'], false],
    ['Code repository', ['providers/code/portal/src/views/RepositoryCreateView.vue'], false],
    ['Databricks connection', ['providers/databricks/portal/src/views/CreateConnectionView.vue'], false],
    ['Databricks warehouse', ['providers/databricks/portal/src/views/CreateWarehouseView.vue'], false],
    ['Databricks table', ['providers/databricks/portal/src/views/CreateTableView.vue'], true],
    ['Databricks import', ['providers/databricks/portal/src/ResourceImportWizard.vue'], true],
    ['Edges service', ['providers/edges/portal/src/ServiceCreate.vue'], true],
    ['Edges workload', ['providers/edges/portal/src/WorkloadCreate.vue'], true],
    ['Infrastructure provision', ['providers/infrastructure/portal/src/views/ProvisionPage.vue'], true],
    ['App Studio model', ['providers/app-studio/portal/src/App.vue', 'providers/app-studio/portal/src/ModelsSettings.vue'], true],
  ]
  for (const [name, paths, wide] of cases) {
    expectSkeleton(name, (await Promise.all(paths.map(source))).join('\n'), { wide })
  }
})

test('Agents route-owned create flows use the shared skeleton while dialogs remain contextual', async () => {
  const cases = [
    ['agent', 'providers/agents/portal/src/views/agent-create.ts'],
    ['connection', 'providers/agents/portal/src/views/connections.ts'],
    ['assisted search', 'providers/agents/portal/src/views/assisted-search.ts'],
    ['model', 'providers/agents/portal/src/views/models.ts'],
    ['toolset', 'providers/agents/portal/src/views/toolsets.ts'],
  ]
  for (const [name, path] of cases) {
    const text = await source(path)
    expectSkeleton(`Agents ${name}`, text)
  }
  const [agent, assistedSearch] = await Promise.all([
    source('providers/agents/portal/src/views/agent-create.ts'),
    source('providers/agents/portal/src/views/assisted-search.ts'),
  ])
  assert.match(agent, /agents-dialog/)
  assert.match(assistedSearch, /agents-dialog/)
})

test('App Studio removes collection tabs and nested settings chrome from model creation', async () => {
  const [app, settings] = await Promise.all([
    source('providers/app-studio/portal/src/App.vue'),
    source('providers/app-studio/portal/src/ModelsSettings.vue'),
  ])
  assert.match(app, /<Tabs[\s\S]*v-if="!isCreateModelRoute"/)
  assert.match(app, /v-if="!publishingInWorkbench && !historyInWorkbench && !isCreateModelRoute"/)
  assert.match(settings, /v-if="!creationRoute" class="flex flex-wrap items-start/)
})

test('route-owned Databricks import removes modal close chrome', async () => {
  const wizard = await source('providers/databricks/portal/src/ResourceImportWizard.vue')
  assert.match(wizard, /v-if="!props\.routeOwned" class="import-head"/)
  assert.match(wizard, /v-if="props\.routeOwned" class="k-btn k-btn--ghost k-back-action"/)
})
