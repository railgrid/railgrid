import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/faros-vite-release-selection',
  configFile: false,
  server: { middlewareMode: true },
})
const {
  newestDeployableRelease,
  releaseHasPromotionEvidence,
  orderReleases,
} = await vite.ssrLoadModule('/src/releaseSelection.ts')
test.after(async () => vite.close())

const release = (commitSHA, overrides = {}) => ({
  commitSHA,
  releaseID: `release-${commitSHA}`,
  deployable: true,
  live: false,
  createdAt: '2026-08-18T10:00:00Z',
  ...overrides,
})

test('orders releases newest first and defaults to the newest deployable release', () => {
  const releases = [
    release('old', { createdAt: '2026-08-17T10:00:00Z' }),
    release('incomplete-newest', { createdAt: '2026-08-19T10:00:00Z', deployable: false, missing: ['api'] }),
    release('newest-complete', { createdAt: '2026-08-18T12:00:00Z' }),
  ]

  assert.deepEqual(orderReleases(releases).map(({ commitSHA }) => commitSHA), ['incomplete-newest', 'newest-complete', 'old'])
  assert.equal(newestDeployableRelease(releases).commitSHA, 'newest-complete')
})

test('requires the server-derived release identity before enabling promotion', () => {
  assert.equal(releaseHasPromotionEvidence(release('complete')), true)
  assert.equal(releaseHasPromotionEvidence(release('missing-id', { releaseID: '' })), false)
  assert.equal(releaseHasPromotionEvidence(release('missing-sha', { commitSHA: '' })), false)
  assert.equal(newestDeployableRelease([
    release('newest-without-id', { releaseID: '', createdAt: '2026-08-19T10:00:00Z' }),
    release('older-complete'),
  ]).commitSHA, 'older-complete')
})
