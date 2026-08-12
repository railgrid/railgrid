export const PROMOTION_ACTION_MIN_POLLS = 2
export const PROMOTION_ACTION_MAX_POLLS = 5

export type PromotionFeedbackTone = 'success' | 'warning'

export interface PromotionFeedback {
  tone: PromotionFeedbackTone
  message: string
}

export interface PromotionActionResult {
  instance?: string | null
  rolloutRevision?: string | null
}

export interface PromotionPollObservation {
  instance?: string | null
  phase?: string | null
  rolloutRevision?: string | null
}

export interface PromotionPollState {
  expectedInstance: string
  expectedRolloutRevision: string
  attempts: number
  minAttempts: number
  maxAttempts: number
}

export interface PromotionPollProgress {
  state: PromotionPollState
  matched: boolean
  done: boolean
}

function clean(value: string | null | undefined): string {
  return typeof value === 'string' ? value.trim() : ''
}

export function beginPromotionPoll(
  result: PromotionActionResult,
  maxAttempts = PROMOTION_ACTION_MAX_POLLS,
  minAttempts = PROMOTION_ACTION_MIN_POLLS,
): PromotionPollState {
  const boundedMin = Math.max(1, Math.floor(minAttempts))
  const boundedMax = Math.max(boundedMin, Math.floor(maxAttempts))
  return {
    expectedInstance: clean(result.instance),
    expectedRolloutRevision: clean(result.rolloutRevision),
    attempts: 0,
    minAttempts: boundedMin,
    maxAttempts: boundedMax,
  }
}

export function observedPromotionRevision(observation: PromotionPollObservation | null | undefined): string {
  if (!observation) return ''
  return clean(observation.rolloutRevision)
}

export function promotionObservationMatches(
  state: PromotionPollState,
  observation: PromotionPollObservation | null | undefined,
): boolean {
  if (!observation || clean(observation.phase).toLowerCase() !== 'ready') return false

  const observedInstance = clean(observation.instance)
  if (state.expectedInstance && observedInstance && observedInstance !== state.expectedInstance) return false

  const observedRevision = observedPromotionRevision(observation)
  // A server that returned a rollout revision must later observe that exact
  // revision on the provider instance. Missing observation is not evidence of
  // convergence; older servers remain compatible because they return no
  // expected revision in the first place.
  return !state.expectedRolloutRevision || observedRevision === state.expectedRolloutRevision
}

export function advancePromotionPoll(
  state: PromotionPollState,
  observation: PromotionPollObservation | null | undefined,
): PromotionPollProgress {
  const nextState = { ...state, attempts: state.attempts + 1 }
  const matched = nextState.attempts >= nextState.minAttempts && promotionObservationMatches(nextState, observation)
  return {
    state: nextState,
    matched,
    done: matched || nextState.attempts >= nextState.maxAttempts,
  }
}

export function promotionAcceptedFeedback(result: PromotionActionResult): PromotionFeedback {
  const instance = clean(result.instance) || 'the production instance'
  const revision = clean(result.rolloutRevision)
  return {
    tone: 'success',
    message: revision
      ? `Promotion accepted for ${instance} at rollout revision ${revision}. Waiting for that production rollout to report Ready.`
      : `Promotion accepted for ${instance}. Waiting for the production rollout to report Ready.`,
  }
}

export function promotionReadyFeedback(
  state: PromotionPollState,
  observation: PromotionPollObservation | null | undefined,
): PromotionFeedback {
  const instance = clean(observation?.instance) || state.expectedInstance || 'the production instance'
  const observedRevision = observedPromotionRevision(observation)
  const requestedRevision = state.expectedRolloutRevision
  return {
    tone: 'success',
    message: observedRevision && (!requestedRevision || observedRevision === requestedRevision)
      ? `Production ${instance} is Ready at rollout revision ${observedRevision}.`
      : requestedRevision
        ? `Promotion accepted for ${instance} at rollout revision ${requestedRevision}. Production currently reports Ready; refresh to verify the latest rollout.`
        : `Promotion accepted for ${instance}. Production currently reports Ready.`,
  }
}

export function promotionPollExhaustedFeedback(
  state: PromotionPollState,
  observation?: PromotionPollObservation | null,
): PromotionFeedback {
  const instance = state.expectedInstance || 'the production instance'
  const revision = state.expectedRolloutRevision
  const phase = clean(observation?.phase) || 'an unavailable phase'
  return {
    tone: 'warning',
    message: revision
      ? `Promotion accepted for ${instance} at rollout revision ${revision}. Production currently reports ${phase}; refresh to check the latest rollout.`
      : `Promotion accepted for ${instance}. Production currently reports ${phase}; refresh to check the latest rollout.`,
  }
}
