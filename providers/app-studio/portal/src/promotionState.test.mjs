import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'vite'

const vite = await createServer({
  appType: 'custom',
  cacheDir: '/tmp/kedge-vite-promotion-state',
  configFile: false,
  server: { middlewareMode: true },
})
const {
  advancePromotionPoll,
  beginPromotionPoll,
  promotionAcceptedFeedback,
  promotionObservationMatches,
  promotionPollExhaustedFeedback,
  promotionReadyFeedback,
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
