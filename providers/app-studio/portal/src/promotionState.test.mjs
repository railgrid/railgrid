import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/faros-vite-promotion-state',
  configFile: false,
  server: { middlewareMode: true },
})
const {
  advancePromotionPoll,
  beginPromotionPoll,
  promotionAcceptedFeedback,
  promotionObservationMatches,
  promotionPollExhaustedFeedback,
  promotionPollDelay,
  promotionReadyFeedback,
  releaseArtifactPollDelay,
  releaseArtifactWaitPhase,
  releasePipelineView,
} = await vite.ssrLoadModule('/src/promotionState.ts')
test.after(async () => vite.close())

test('requires a follow-up poll instead of trusting the first stale Ready response', () => {
  const initial = beginPromotionPoll({ instance: 'demo-prod', rolloutRevision: '7' })
  const first = advancePromotionPoll(initial, {
    instance: 'demo-prod',
    phase: 'Ready',
    rolloutRevision: '6',
  })

  assert.equal(first.state.attempts, 1)
  assert.equal(first.matched, false)
  assert.equal(first.done, false)

  const second = advancePromotionPoll(first.state, {
    instance: 'demo-prod',
    phase: 'Ready',
    rolloutRevision: '7',
  })
  assert.equal(second.matched, true)
  assert.equal(second.done, true)
  assert.match(promotionReadyFeedback(second.state, {
    instance: 'demo-prod',
    phase: 'Ready',
    rolloutRevision: '7',
  }).message, /demo-prod.*7/)
})

test('bounds post-action polling when production remains in a pending phase', () => {
  let progress = advancePromotionPoll(
    beginPromotionPoll({ instance: 'demo-prod', rolloutRevision: '9' }, 3),
    { instance: 'demo-prod', phase: 'Provisioning' },
  )
  progress = advancePromotionPoll(progress.state, {
    instance: 'demo-prod',
    phase: 'Provisioning',
  })
  assert.equal(progress.done, false)
  progress = advancePromotionPoll(progress.state, {
    instance: 'demo-prod',
    phase: 'Provisioning',
  })
  assert.equal(progress.done, true)
  assert.equal(progress.matched, false)
  const feedback = promotionPollExhaustedFeedback(progress.state, { phase: 'Provisioning' })
  assert.equal(feedback.tone, 'warning')
  assert.match(feedback.message, /currently reports Provisioning/)
})

test('uses bounded backoff delays for slow production convergence', () => {
  assert.equal(promotionPollDelay(0), 4000)
  assert.equal(promotionPollDelay(1), 8000)
  assert.equal(promotionPollDelay(8), 15000)
})

test('bounds exact-commit image verification before settling into background polling', () => {
  assert.equal(releaseArtifactWaitPhase(89_999), 'waiting')
  assert.equal(releaseArtifactWaitPhase(90_000), 'delayed')
  assert.equal(releaseArtifactWaitPhase(299_999), 'delayed')
  assert.equal(releaseArtifactWaitPhase(300_000), 'attention')
  assert.equal(releaseArtifactPollDelay('waiting'), 4000)
  assert.equal(releaseArtifactPollDelay('delayed'), 15000)
  assert.equal(releaseArtifactPollDelay('attention'), 30000)
})

test('keeps promotion disabled before a reviewed commit exists', () => {
  const pipeline = releasePipelineView(null)
  assert.equal(pipeline.state, 'needs_commit')
  assert.equal(pipeline.transitional, false)
  assert.match(pipeline.message, /Commit your latest changes/)
  assert.equal(pipeline.steps.find((step) => step.key === 'commit').state, 'current')
  assert.equal(pipeline.steps.find((step) => step.key === 'build').state, 'pending')
})

test('feedback identifies the returned instance and rollout revision', () => {
  const feedback = promotionAcceptedFeedback({ instance: 'demo-prod', rolloutRevision: '12' })
  assert.equal(feedback.tone, 'success')
  assert.match(feedback.message, /demo-prod/)
  assert.match(feedback.message, /12/)
})

test('readiness only matches the expected instance and revision', () => {
  const state = beginPromotionPoll({ instance: 'demo-prod', rolloutRevision: '4' })
  assert.equal(promotionObservationMatches(state, {
    instance: 'other-prod',
    phase: 'Ready',
    rolloutRevision: '4',
  }), false)
  assert.equal(promotionObservationMatches(state, {
    instance: 'demo-prod',
    phase: 'Provisioning',
    rolloutRevision: '4',
  }), false)
  assert.equal(promotionObservationMatches(state, {
    instance: 'demo-prod',
    phase: 'Ready',
    rolloutRevision: '3',
  }), false)
  assert.equal(promotionObservationMatches(state, {
    instance: 'demo-prod',
    phase: 'Ready',
    rolloutRevision: '4',
  }), true)
})

test('does not claim revision readiness when status omits the observed revision', () => {
  const state = beginPromotionPoll({ instance: 'demo-prod', rolloutRevision: '4' })
  assert.equal(promotionObservationMatches(state, { instance: 'demo-prod', phase: 'Ready' }), false)
  const feedback = promotionReadyFeedback(state, { instance: 'demo-prod', phase: 'Ready' })
  assert.match(feedback.message, /currently reports Ready/)
  assert.doesNotMatch(feedback.message, /Ready at rollout revision/)
})

test('distinguishes queued, partial running, finalizing, and failed exact-commit builds', () => {
  const readiness = (run, built = 0) => ({
    promotable: false,
    build: {
      status: built ? 'incomplete' : 'none',
      commitSHA: '70aed526ffff',
      note: '',
      components: [
        { name: 'web', imageInput: 'webImage', built: built > 0 },
        { name: 'api', imageInput: 'apiImage', built: built > 1 },
      ],
      missing: built ? ['api'] : ['api', 'web'],
      run: { found: true, headSHA: '70aed526ffff', ...run },
    },
  })

  assert.equal(releasePipelineView(readiness({ status: 'queued' })).state, 'queued')
  const running = releasePipelineView(readiness({ status: 'in_progress' }, 1))
  assert.equal(running.state, 'running')
  assert.match(running.message, /1 of 2/)
  assert.deepEqual(running.missing, ['api'])
  assert.equal(running.partial, true)
  assert.equal(running.artifactLag, false)
  const finalizing = releasePipelineView(readiness({ status: 'completed', conclusion: 'success' }))
  assert.equal(finalizing.state, 'finalizing')
  assert.equal(finalizing.artifactLag, true)
  assert.equal(finalizing.steps.find((step) => step.key === 'build').state, 'done')
  assert.equal(finalizing.steps.find((step) => step.key === 'verify').state, 'current')
  const failed = releasePipelineView(readiness({ status: 'completed', conclusion: 'failure' }))
  assert.equal(failed.state, 'failed')
  assert.equal(failed.transitional, false)
})

test('settles a successful build with missing exact-commit images into an actionable state', () => {
  const pipeline = releasePipelineView({
    promotable: false,
    build: {
      status: 'none', commitSHA: '70aed526ffff', note: '', missing: ['web'],
      components: [{ name: 'web', imageInput: 'webImage', built: false }],
      run: { found: true, headSHA: '70aed526ffff', status: 'completed', conclusion: 'success' },
    },
  }, {}, { artifactNeedsAttention: true })

  assert.equal(pipeline.state, 'artifact_attention')
  assert.equal(pipeline.transitional, false)
  assert.match(pipeline.message, /needs attention/)
  assert.equal(pipeline.steps.find((step) => step.key === 'build').state, 'done')
  assert.equal(pipeline.steps.find((step) => step.key === 'verify').state, 'attention')
  assert.deepEqual(pipeline.artifacts[0], {
    component: 'web',
    packageMatcher: 'web or */web',
    expectedTag: 'sha-70aed526ffff',
    observedTag: '',
    digest: '',
    verified: false,
  })
})

test('replaces stale transitional evidence with status unavailable after a refresh error', () => {
  const pipeline = releasePipelineView({
    promotable: false,
    build: {
      status: 'none', commitSHA: '70aed526ffff', note: '', missing: ['web'],
      components: [{ name: 'web', imageInput: 'webImage', built: false }],
      run: { found: true, headSHA: '70aed526ffff', status: 'completed', conclusion: 'success' },
    },
  }, {}, { statusError: 'Registry status request failed.' })

  assert.equal(pipeline.state, 'unavailable')
  assert.equal(pipeline.transitional, false)
  assert.match(pipeline.message, /temporarily unavailable/)
  assert.match(pipeline.detail, /may be stale/)
})

test('labels a partial artifact view when the run observation is unavailable', () => {
  const pipeline = releasePipelineView({
    promotable: false,
    build: {
      status: 'incomplete', commitSHA: 'release123', note: '', missing: ['api'],
      components: [
        { name: 'web', imageInput: 'webImage', built: true },
        { name: 'api', imageInput: 'apiImage', built: false },
      ],
    },
  })
  assert.equal(pipeline.state, 'waiting')
  assert.equal(pipeline.partial, true)
  assert.match(pipeline.message, /Partial release artifacts.*1 of 2/)
  assert.match(pipeline.detail, /api/)
})

test('stops transitional polling when CI status is unavailable without a usable run', () => {
  const pipeline = releasePipelineView({
    promotable: false,
    build: {
      status: 'incomplete', commitSHA: 'release123', note: '', missing: ['api'],
      components: [
        { name: 'web', imageInput: 'webImage', built: true },
        { name: 'api', imageInput: 'apiImage', built: false },
      ],
      runError: 'Build status temporarily unavailable.',
    },
  })
  assert.equal(pipeline.state, 'unavailable')
  assert.equal(pipeline.tone, 'warning')
  assert.equal(pipeline.transitional, false)
  assert.match(pipeline.message, /Build status is temporarily unavailable/)
  assert.match(pipeline.detail, /Build status temporarily unavailable/)
  assert.equal(pipeline.steps.find((step) => step.key === 'build').state, 'pending')
})

test('does not claim an unpinned run failed when the host omits its head SHA', () => {
  const pipeline = releasePipelineView({
    promotable: false,
    build: {
      status: 'none', commitSHA: 'release123', note: '', missing: ['web'],
      components: [{ name: 'web', imageInput: 'webImage', built: false }],
      run: { found: true, status: 'completed', conclusion: 'failure' },
    },
  })
  assert.equal(pipeline.state, 'waiting')
  assert.doesNotMatch(pipeline.message, /failed/i)
  assert.match(pipeline.detail, /did not report its commit/)
})

test('only marks external access done when a ready publication has a URL', () => {
  const readiness = {
    promotable: true,
    build: { status: 'built', commitSHA: 'release123', note: '', components: [] },
    production: { name: 'demo-prod', phase: 'Ready' },
  }
  const resolving = releasePipelineView(readiness, { published: true, ready: true })
  assert.equal(resolving.steps.find((step) => step.key === 'access').state, 'current')
  assert.match(resolving.message, /Resolving external access/)
  const live = releasePipelineView(readiness, { published: true, ready: true, url: 'https://demo.example.test' })
  assert.equal(live.steps.find((step) => step.key === 'access').state, 'done')
  assert.match(live.message, /external access enabled/)
})

test('does not use a stale workflow run to explain the selected commit', () => {
  const pipeline = releasePipelineView({
    promotable: false,
    build: {
      status: 'none', commitSHA: 'current123', note: '', missing: ['web'],
      components: [{ name: 'web', imageInput: 'webImage', built: false }],
      run: { found: true, headSHA: 'older456', status: 'completed', conclusion: 'failure' },
    },
  })
  assert.equal(pipeline.state, 'waiting')
  assert.match(pipeline.detail, /another commit|older456/)
  assert.doesNotMatch(pipeline.message, /failed/i)
})

test('artifact availability remains authoritative after CI success or failure', () => {
  for (const conclusion of ['success', 'failure']) {
    const pipeline = releasePipelineView({
      promotable: true,
      build: {
        status: 'built', commitSHA: 'release123', note: '',
        components: [{ name: 'web', imageInput: 'webImage', built: true }],
        run: { found: true, headSHA: 'release123', status: 'completed', conclusion },
      },
    })
    assert.equal(pipeline.state, 'ready')
    assert.equal(pipeline.steps.find((step) => step.key === 'build').state, 'done')
  }
})

test('production readiness advances access without claiming it is already enabled', () => {
  const pipeline = releasePipelineView({
    promotable: true,
    build: { status: 'built', commitSHA: 'release123', note: '', components: [] },
    production: { name: 'demo-prod', phase: 'Ready' },
  }, { published: false, ready: false })
  assert.equal(pipeline.state, 'production_ready')
  assert.match(pipeline.message, /Choose who can access it/)
  assert.equal(pipeline.steps.find((step) => step.key === 'access').state, 'current')
})

test('keeps old production online without claiming a newer selected release is deployed', () => {
  const base = {
    requestedRolloutRevision: 'live-7', observedRolloutRevision: 'live-7',
    production: { name: 'demo-prod', phase: 'Ready' },
    productionValues: { webImage: 'registry/web@sha256:old' },
  }
  const building = releasePipelineView({
    ...base, promotable: false,
    build: {
      status: 'incomplete', commitSHA: 'newcommit123', note: '', missing: ['web'],
      components: [{ name: 'web', imageInput: 'webImage', built: false }],
      run: { found: true, headSHA: 'newcommit123', status: 'in_progress' },
    },
  })
  assert.equal(building.state, 'running')
  assert.match(building.detail, /Current production remains online/)
  assert.equal(building.steps.find((step) => step.key === 'deploy').state, 'pending')

  const ready = releasePipelineView({
    ...base, promotable: true,
    build: {
      status: 'built', commitSHA: 'newcommit123', note: '',
      components: [{ name: 'web', imageInput: 'webImage', built: true, image: 'registry/web@sha256:new' }],
    },
  })
  assert.equal(ready.state, 'ready')
  assert.match(ready.message, /new release/i)
  assert.doesNotMatch(ready.message, /Production is running/)
})

test('claims production readiness only when deployed image values match the selected release', () => {
  const pipeline = releasePipelineView({
    promotable: true,
    build: {
      status: 'built', commitSHA: 'release123', note: '',
      components: [{ name: 'web', imageInput: 'webImage', built: true, image: 'registry/web@sha256:same' }],
    },
    productionValues: { webImage: 'registry/web@sha256:same' },
    requestedRolloutRevision: '8', observedRolloutRevision: '8',
    production: { name: 'demo-prod', phase: 'Ready' },
  })
  assert.equal(pipeline.state, 'production_ready')
})

test('keeps a stale Ready production revision in deployment until the requested revision is observed', () => {
  const pipeline = releasePipelineView({
    promotable: true,
    build: { status: 'built', commitSHA: 'release123456', note: '', components: [] },
    requestedRolloutRevision: 'requested-42',
    observedRolloutRevision: 'older-41',
    production: { name: 'demo-prod', phase: 'Ready' },
  })
  assert.equal(pipeline.state, 'deploying')
  assert.equal(pipeline.transitional, true)
  assert.match(pipeline.detail, /Requested rollout requeste.*older-41/)
  assert.equal(pipeline.steps.find((step) => step.key === 'deploy').state, 'current')
})

test('stops the transitional release state when the provider reports a terminal rollout failure', () => {
  const pipeline = releasePipelineView({
    promotable: true,
    build: { status: 'built', commitSHA: 'release123456', note: '', components: [] },
    requestedRolloutRevision: 'requested-42',
    observedRolloutRevision: 'older-41',
    production: { name: 'demo-prod', phase: 'Failed' },
  })
  assert.equal(pipeline.state, 'failed')
  assert.equal(pipeline.transitional, false)
  assert.match(pipeline.message, /failed/i)
  assert.equal(pipeline.steps.find((step) => step.key === 'build').state, 'done')
  assert.equal(pipeline.steps.find((step) => step.key === 'deploy').state, 'error')
})
