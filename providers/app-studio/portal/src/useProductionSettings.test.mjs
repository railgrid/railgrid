import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'
import { ref } from 'vue'

const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/faros-vite-production-settings',
  configFile: false,
  server: { middlewareMode: true },
})
const { useProductionSettings } = await vite.ssrLoadModule('/src/useProductionSettings.ts')
test.after(async () => vite.close())

function readiness(overrides = {}) {
  return {
    promotable: true,
    build: { status: 'built', note: '', components: [] },
    ...overrides,
  }
}

function setup() {
  const input = {
    promotion: ref(null),
    publishing: ref(null),
    promotionLoading: ref(false),
    promotionBusy: ref(false),
    promotionError: ref(null),
    productionFormValid: ref(true),
    selectedProjectName: ref('example'),
  }
  return { input, state: useProductionSettings(input) }
}

test('keeps stale publication state from claiming an undeployed app is ready', () => {
  const { input, state } = setup()
  input.publishing.value = { published: true, publication: { ready: true, url: 'https://example.test' } }
  input.promotion.value = readiness()

  assert.equal(state.productionPublicationReady.value, false)
  assert.deepEqual(state.productionOverview.value, { label: 'Not deployed', tone: 'muted' })
  assert.equal(state.productionAccess.value.label, 'Offline')
})

test('separates ready publication evidence from URL availability', () => {
  const { input, state } = setup()
  input.promotion.value = readiness({ production: { phase: 'Ready' } })
  input.publishing.value = { published: true, publication: { ready: true, mode: 'restricted' }, grants: [] }

  assert.equal(state.productionPublicationReady.value, true)
  assert.deepEqual(state.productionPublicationStatus.value, { label: 'Ready', tone: 'success' })
  assert.match(state.productionDescription.value, /link is still being resolved/)

  input.publishing.value = { ...input.publishing.value, publication: { ...input.publishing.value.publication, url: 'https://example.test' } }
  assert.deepEqual(state.productionPublicationStatus.value, { label: 'Live', tone: 'success' })
  assert.equal(state.productionURL.value, 'https://example.test')
})

test('centralizes promotion validity and busy-state presentation', () => {
  const { input, state } = setup()
  input.promotion.value = readiness()
  assert.equal(state.canPromote.value, true)

  input.productionFormValid.value = false
  assert.equal(state.canPromote.value, false)
  assert.equal(state.promotionDisabledReason.value, 'Fix the highlighted production settings before deploying.')

  input.productionFormValid.value = true
  input.promotionBusy.value = true
  assert.equal(state.promoteButtonLabel.value, 'Deploying…')
  assert.equal(state.promotionDisabledReason.value, 'Promotion is in progress.')
})

test('marks cached production evidence stale and disables deployment after a status error', () => {
  const { input, state } = setup()
  input.promotion.value = readiness({
    build: { status: 'built', commitSHA: 'release123', note: '', components: [] },
    production: { phase: 'Ready' },
  })
  input.promotionError.value = 'Registry status request failed.'

  assert.deepEqual(state.productionOverview.value, { label: 'Status unavailable', tone: 'warning' })
  assert.match(state.productionOverviewDescription.value, /Previously loaded details may be stale/)
  assert.equal(state.releasePipeline.value.state, 'unavailable')
  assert.equal(state.canPromote.value, false)
  assert.match(state.promotionDisabledReason.value, /Check again before deploying/)
})
