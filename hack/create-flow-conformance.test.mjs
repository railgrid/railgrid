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
  if (wide) assert.match(text, /k-create-surface--(?:wide|guided)/, `${name} should use a wide provisioning surface`)
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

test('PortalKit defines shared first-run and guided-create widgets', async () => {
  const [css, firstRun, guidance, sync] = await Promise.all([
    source('provider-sdk/portalkit/faros-ui.css'),
    source('provider-sdk/portalkit-vue/FirstRunGuide.vue'),
    source('provider-sdk/portalkit-vue/CreateGuidance.vue'),
    source('hack/sync-portalkit.sh'),
  ])

  for (const selector of [
    '.k-first-run',
    '.k-first-run__journey',
    '.k-create-surface--guided',
    '.k-create-body--guided',
    '.k-create-fields',
    '.k-create-guidance',
  ]) {
    assert.match(css, new RegExp(selector.replace('.', '\\.')), `shared CSS is missing ${selector}`)
  }
  assert.match(css, /@container k-create-surface \(max-width: 960px\)[\s\S]*\.k-create-guidance[\s\S]*grid-row: 2/)
  assert.match(css, /@media \(max-width: 620px\)[\s\S]*\.k-first-run__journey/)

  assert.match(firstRun, /<section class="k-first-run" :aria-labelledby="titleID">/)
  assert.match(firstRun, /ensureFarosUIStyles\(\)/)
  assert.match(firstRun, /<ol v-if="steps\.length" class="k-first-run__journey"/)
  assert.match(firstRun, /:aria-current="index === boundedCurrentStep \? 'step' : undefined"/)
  assert.match(firstRun, /class="k-first-run__step-status"/)
  assert.match(firstRun, /index < boundedCurrentStep \? 'Completed step'/)
  assert.match(css, /\.k-first-run__step-status\s*\{[\s\S]*clip-path: inset\(50%\)[\s\S]*position: absolute/)
  assert.match(guidance, /<aside class="k-create-guidance" :aria-labelledby="titleID">/)
  assert.match(guidance, /ensureFarosUIStyles\(\)/)
  assert.match(guidance, /<dl class="k-create-guidance__values">/)
  assert.match(guidance, /<ol>[\s\S]*v-for="step in nextSteps"/)
  assert.match(sync, /CreateGuidance\.vue FirstRunGuide\.vue/)
})

test('provider create journeys adopt the shared first-run and guidance vocabulary', async () => {
  const vueProviders = [
    ['Databricks', 'providers/databricks/portal/src/components/DatabricksEmptyState.vue', 'providers/databricks/portal/src/components/ManualCreateGuidance.vue'],
    ['Code', 'providers/code/portal/src/views/ConnectionsView.vue', 'providers/code/portal/src/views/ConnectionCreateView.vue'],
    ['Edges', 'providers/edges/portal/src/EdgeCollection.vue', 'providers/edges/portal/src/ServiceCreate.vue'],
  ]
  for (const [name, emptyPath, createPath] of vueProviders) {
    assert.match(await source(emptyPath), /FirstRunGuide/, `${name} must use the shared first-run component`)
    assert.match(await source(createPath), /CreateGuidance/, `${name} must use the shared create-guidance component`)
  }

  const agents = await Promise.all([
    source('providers/agents/portal/src/views/AgentsList.vue'),
    source('providers/agents/portal/src/views/AgentCreate.vue'),
  ])
  assert.match(agents[0], /FirstRunGuide/, 'Agents must use the shared first-run component')
  assert.match(agents[1], /CreateGuidance/, 'Agents must use the shared create-guidance component')
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

test('Agents route-owned create flows use the shared skeleton without obsolete modal fallbacks', async () => {
  const cases = [
    ['agent', 'providers/agents/portal/src/views/AgentCreate.vue'],
    ['connection', 'providers/agents/portal/src/views/Connections.vue'],
    ['assisted search', 'providers/agents/portal/src/views/AssistedSearch.vue'],
    ['model', 'providers/agents/portal/src/views/Models.vue'],
    ['toolset', 'providers/agents/portal/src/views/Toolsets.vue'],
  ]
  for (const [name, path] of cases) {
    const text = await source(path)
    expectSkeleton(`Agents ${name}`, text)
  }
  const [agent, assistedSearch] = await Promise.all([
    source('providers/agents/portal/src/views/AgentCreate.vue'),
    source('providers/agents/portal/src/views/AssistedSearch.vue'),
  ])
  assert.doesNotMatch(agent, /agents-dialog/)
  assert.doesNotMatch(assistedSearch, /agents-dialog/)
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
