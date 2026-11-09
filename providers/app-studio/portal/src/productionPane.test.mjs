import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { createServer } from 'vite'
import { computed, ref, shallowRef } from 'vue'

const app = await readFile(new URL('./App.vue', import.meta.url), 'utf8')
const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/railgrid-vite-production-pane',
  configFile: false,
  server: { middlewareMode: true },
})
const { productionConfigurationSummary, shortReleaseSHA } = await vite.ssrLoadModule('/src/productionPane.ts')
const { productionFormValuesFromSchema } = await vite.ssrLoadModule('/src/productionForm.ts')
const { releasePipelineView } = await vite.ssrLoadModule('/src/promotionState.ts')
test.after(async () => vite.close())

function sourceBetween(start, end) {
  const first = app.indexOf(start)
  const last = app.indexOf(end, first)
  assert.ok(first >= 0 && last > first)
  return app.slice(first, last)
}

test('presents an exact release with a compact SHA', () => {
  assert.equal(shortReleaseSHA(' 75ff40e31b345e10efcb5d0398aad3c7bacf626d '), '75ff40e3')
  assert.equal(shortReleaseSHA('abc123'), 'abc123')
})

test('summarizes the runtime without exposing publishing policy as deployment configuration', () => {
  assert.equal(productionConfigurationSummary({
    access: 'public',
    port: 8080,
    replicas: 1,
    expose: { hostnamePrefix: '' },
    env: {},
  }), '1 replica · port 8080 · default hostname · no environment overrides')
  assert.equal(productionConfigurationSummary({
    webPort: '3000',
    replicaCount: 2,
    expose: { hostnamePrefix: 'docs' },
    webEnv: { API_URL: 'https://api.example.test' },
    apiEnv: { LOG_LEVEL: 'info' },
  }), '2 replicas · port 3000 · custom hostname · 2 environment overrides')
})

test('summarizes template defaults and saved environment maps through rollout updates', () => {
  const createFixture = new Function('ref', 'shallowRef', 'computed', 'productionConfigurationSummary', 'productionFormValuesFromSchema', [
    sourceBetween('const promotion = ', '\nconst promotionLoading').replace('<ProjectPromotionReadiness | null>', ''),
    sourceBetween('function productionValuesFromReadiness(', '\n\nfunction syncProductionForm')
      .replace('readiness: ProjectPromotionReadiness): ProductionFormValues', 'readiness)'),
    sourceBetween('const savedProductionSettingsSummary = ', '\n// A release update'),
    'return { promotion, savedProductionSettingsSummary }',
  ].join('\n'))
  const { promotion, savedProductionSettingsSummary } = createFixture(
    ref, shallowRef, computed, productionConfigurationSummary, productionFormValuesFromSchema,
  )
  promotion.value = {
    build: { components: [] },
    productionSchema: {
      type: 'object',
      properties: { env: { type: 'object', additionalProperties: { type: 'string' }, default: {} } },
    },
  }
  assert.match(savedProductionSettingsSummary.value, /no environment overrides/)
  promotion.value = { ...promotion.value, productionValues: { env: { LOG_LEVEL: 'info' } } }
  assert.match(savedProductionSettingsSummary.value, /1 environment override/)
  promotion.value = { ...promotion.value, requestedRolloutRevision: 'next' }
  assert.match(savedProductionSettingsSummary.value, /1 environment override/)
})

test('distinguishes pending rollout observation, terminal failure, and unavailable status', () => {
  const createFixture = new Function('computed', 'promotionError', 'releasePipeline', [
    sourceBetween('const productionRolloutFailed = ', '\nfunction canPromoteRelease'),
    'return productionRolloutSummary',
  ].join('\n'))
  const promotionError = ref(null)
  const readiness = ref({
    build: { status: 'built', commitSHA: 'release123', components: [] },
    requestedRolloutRevision: 'next',
    observedRolloutRevision: 'prior',
    production: { name: 'demo-prod', phase: 'Ready' },
  })
  const pipeline = computed(() => releasePipelineView(readiness.value, {}, { statusError: promotionError.value }))
  const summary = createFixture(computed, promotionError, pipeline)
  assert.equal(summary.value, 'Rollout requested · awaiting observation')
  readiness.value.production.phase = 'Failed'
  assert.equal(summary.value, 'Rollout requested · awaiting observation')
  readiness.value.observedRolloutRevision = 'next'
  assert.equal(summary.value, 'Production rollout failed')
  promotionError.value = 'Request timed out'
  assert.equal(summary.value, 'Rollout status unavailable')
  promotionError.value = null
  readiness.value.production.phase = 'Ready'
  assert.equal(summary.value, 'Observed rollout next')
  readiness.value.build = {
    status: 'incomplete',
    commitSHA: 'release456',
    components: [{ name: 'web', imageInput: 'webImage', built: false }],
    run: { found: true, headSHA: 'release456', status: 'completed', conclusion: 'failure' },
  }
  assert.equal(pipeline.value.state, 'failed')
  assert.equal(summary.value, 'Observed rollout next')
})
