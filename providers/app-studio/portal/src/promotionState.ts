import type { ProjectPromotionReadiness } from './types'

export const PROMOTION_ACTION_MIN_POLLS = 2
export const PROMOTION_ACTION_MAX_POLLS = 5
export const PROMOTION_POLL_BASE_DELAY_MS = 4000
export const PROMOTION_POLL_MAX_DELAY_MS = 15000

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

export type ReleasePipelineState =
  | 'needs_commit'
  | 'waiting'
  | 'queued'
  | 'running'
  | 'finalizing'
  | 'unavailable'
  | 'failed'
  | 'ready'
  | 'deploying'
  | 'production_ready'

export type ReleasePipelineTone = 'muted' | 'warning' | 'danger' | 'success'
export type ReleasePipelineStepState = 'done' | 'current' | 'pending' | 'error'

export interface ReleasePipelineStep {
  key: 'commit' | 'build' | 'deploy' | 'access'
  label: string
  state: ReleasePipelineStepState
  detail?: string
}

export interface ReleasePipelineView {
  state: ReleasePipelineState
  tone: ReleasePipelineTone
  message: string
  detail: string
  transitional: boolean
  commitSHA: string
  requestedRevision: string
  observedRevision: string
  buildURL: string
  builtCount: number
  totalCount: number
  /** Some component images are visible, but the exact release is incomplete. */
  partial: boolean
  /** CI completed successfully while registry Package observations lag. */
  artifactLag: boolean
  missing: string[]
  steps: ReleasePipelineStep[]
}

export interface ReleaseAccessObservation {
  published?: boolean
  ready?: boolean
  /** The access URL is the semantic boundary for the Live label. */
  url?: string | null
}

function clean(value: string | null | undefined): string {
  return typeof value === 'string' ? value.trim() : ''
}

function shortSHA(value: string): string {
  return value.length > 8 ? value.slice(0, 8) : value
}

function failedConclusion(value: string): boolean {
  return !!value && value !== 'success' && value !== 'neutral' && value !== 'skipped'
}

function selectedReleaseMatchesProduction(readiness: ProjectPromotionReadiness, components: NonNullable<ProjectPromotionReadiness['build']['components']>): boolean {
  if (readiness.build.status !== 'built') return false
  if (components.length === 0) return true
  const values = readiness.productionValues ?? {}
  return components.every((component) => {
    const builtImage = clean(component.image)
    return !!builtImage && clean(typeof values[component.imageInput] === 'string' ? values[component.imageInput] as string : '') === builtImage
  })
}

export function promotionPollDelay(attempts: number): number {
  const exponent = Math.max(0, Math.floor(attempts))
  return Math.min(PROMOTION_POLL_MAX_DELAY_MS, PROMOTION_POLL_BASE_DELAY_MS * (2 ** exponent))
}

export function releasePipelineView(
  readiness: ProjectPromotionReadiness | null | undefined,
  access: ReleaseAccessObservation = {},
): ReleasePipelineView {
  const build = readiness?.build
  const commitSHA = clean(build?.commitSHA)
  const components = build?.components ?? []
  const totalCount = components.length
  const builtCount = components.filter((component) => component.built).length
  const missing = (build?.missing ?? components.filter((component) => !component.built).map((component) => component.name))
    .filter(Boolean)
  const run = build?.run
  const runStatus = clean(run?.status).toLowerCase()
  const conclusion = clean(run?.conclusion).toLowerCase()
  const productionPhase = clean(readiness?.production?.phase).toLowerCase()
  const requestedRevision = clean(readiness?.requestedRolloutRevision)
  const observedRevision = clean(readiness?.observedRolloutRevision)
  const rolloutConverged = !requestedRevision || observedRevision === requestedRevision
  const productionFailed = ['failed', 'error', 'degraded'].includes(productionPhase)
  const currentProductionReady = productionPhase === 'ready' && rolloutConverged
  const selectedReleaseDeployed = !!readiness && currentProductionReady && selectedReleaseMatchesProduction(readiness, components)
  const deploying = !!readiness?.production && !currentProductionReady
  // A workflow lookup is explanatory evidence, not the promotion gate. It is
  // still only allowed to explain a release when the provider echoed the
  // exact reviewed SHA requested through code__build_status. A missing SHA is
  // deliberately inconclusive so a stale/ambiguous run cannot claim failure.
  const runHeadSHA = clean(run?.headSHA)
  const runMatchesCommit = !!commitSHA && !!runHeadSHA && runHeadSHA === commitSHA
  const partial = builtCount > 0 && builtCount < totalCount
  const artifactLag = !!run?.found && runMatchesCommit && runStatus === 'completed' && conclusion === 'success' && build?.status !== 'built'
  const accessLive = !!access.published && !!access.ready && !!clean(access.url)
  // runError is an observability failure, not evidence that CI failed. When
  // there is no usable exact-commit run and artifacts are still incomplete,
  // stop presenting the build as an active transition so polling can settle.
  const runObservationUnavailable = !!build?.runError && build.status !== 'built' && !runMatchesCommit

  let state: ReleasePipelineState
  let tone: ReleasePipelineTone
  let message: string
  let detail: string

  if (productionFailed) {
    state = 'failed'
    tone = 'danger'
    message = 'The production rollout failed.'
    detail = requestedRevision
      ? `Requested rollout ${shortSHA(requestedRevision)}; observed ${shortSHA(observedRevision) || 'no revision'}.`
      : 'The production provider reported a terminal failure.'
  } else if (selectedReleaseDeployed) {
    state = 'production_ready'
    tone = 'success'
    message = accessLive
      ? 'Production is running with external access enabled.'
      : access.published
        ? 'Production is running. Resolving external access…'
        : 'Production is running. Choose who can access it.'
    detail = requestedRevision
      ? `Requested rollout ${shortSHA(requestedRevision)} is observed in production${observedRevision ? ` at ${shortSHA(observedRevision)}` : ''}. Redeploying a newer release does not change the current access policy.`
      : 'Redeploying a newer release does not change the current access policy.'
  } else if (deploying) {
    state = 'deploying'
    tone = 'warning'
    message = `Deploying release ${shortSHA(commitSHA) || 'to production'}…`
    detail = productionPhase
      ? `Requested rollout ${shortSHA(requestedRevision) || shortSHA(commitSHA) || 'unknown'}; current production is ${shortSHA(observedRevision) || 'not observed'} (${readiness?.production?.phase}).`
      : 'The production provider is applying the release.'
  } else if (!commitSHA) {
    state = 'needs_commit'
    tone = 'muted'
    message = 'Commit your latest changes to create a release.'
    detail = 'Deployment stays disabled until a successful commit has exact-commit images.'
  } else if (build?.status === 'built') {
    state = 'ready'
    tone = 'success'
    message = currentProductionReady ? 'A new release is ready for production.' : 'Release ready for production.'
    detail = `${currentProductionReady ? `Current production remains online at ${shortSHA(observedRevision || requestedRevision) || 'its observed revision'}. ` : ''}All ${totalCount} component image${totalCount === 1 ? '' : 's'} are available for ${shortSHA(commitSHA)}.`
  } else if (runObservationUnavailable) {
    state = 'unavailable'
    tone = 'warning'
    message = 'Build status is temporarily unavailable.'
    detail = `${build?.runError} Exact-commit release artifacts remain the promotion authority; refresh to retry the status lookup.`
  } else if (run?.found && runMatchesCommit && runStatus === 'completed' && failedConclusion(conclusion)) {
    state = 'failed'
    tone = 'danger'
    message = conclusion === 'cancelled' ? 'The release build was cancelled.' : 'The release build failed.'
    detail = `Artifacts remain incomplete for ${shortSHA(commitSHA)}. Missing: ${missing.join(', ') || 'release images'}.`
  } else if (run?.found && !runMatchesCommit) {
    state = 'waiting'
    tone = 'warning'
    message = `Committed ${shortSHA(commitSHA)}. Waiting for its build to start…`
    detail = runHeadSHA
      ? `The observed workflow run belongs to ${shortSHA(runHeadSHA)}; it cannot explain or unlock this release.`
      : 'The workflow did not report its commit; it cannot explain or unlock this release yet.'
  } else if (run?.found && runStatus === 'queued') {
    state = 'queued'
    tone = 'warning'
    message = `Release build queued — ${builtCount} of ${totalCount} ready.`
    detail = missing.length ? `Waiting for ${missing.join(', ')}.` : 'Waiting for the build runner.'
  } else if (run?.found && runStatus === 'in_progress') {
    state = 'running'
    tone = 'warning'
    message = `Building release images — ${builtCount} of ${totalCount} ready.`
    detail = missing.length ? `Still building: ${missing.join(', ')}.` : 'The workflow is still reporting in progress.'
  } else if (run?.found && runStatus === 'completed' && conclusion === 'success') {
    state = 'finalizing'
    tone = 'warning'
    message = 'Build succeeded. Finalizing release images…'
    detail = missing.length ? `The registry is still indexing ${missing.join(', ')}.` : 'Waiting for registry package observations.'
  } else if (build?.status === 'incomplete') {
    state = 'waiting'
    tone = 'warning'
    message = `Partial release artifacts — ${builtCount} of ${totalCount} ready.`
    detail = missing.length
      ? `Waiting for ${missing.join(', ')}. The exact-commit build remains the promotion authority.`
      : 'Waiting for the remaining exact-commit release images.'
  } else {
    state = 'waiting'
    tone = 'warning'
    message = `Committed ${shortSHA(commitSHA)}. Waiting for the build to start…`
    detail = build?.runError || 'The exact-commit release images have not appeared yet.'
  }

  if (currentProductionReady && !selectedReleaseDeployed && build?.status !== 'built') {
    detail = `Current production remains online at ${shortSHA(observedRevision || requestedRevision) || 'its observed revision'}. ${detail}`
  }

  // A provider rollout failure is independent from image production. Keep
  // that failure on Deploy so an already-valid build is not relabeled as a
  // build error while the production provider reports its own terminal state.
  const buildFailed = state === 'failed' && !productionFailed && build?.status !== 'built'
  const buildDone = build?.status === 'built'
  const buildCurrent = !buildDone && !['needs_commit', 'unavailable'].includes(state)
  const steps: ReleasePipelineStep[] = [
    { key: 'commit', label: 'Commit', state: commitSHA ? 'done' : 'current', detail: commitSHA ? shortSHA(commitSHA) : undefined },
    { key: 'build', label: 'Build images', state: buildFailed ? 'error' : buildDone ? 'done' : buildCurrent ? 'current' : 'pending', detail: totalCount ? `${builtCount} of ${totalCount}` : undefined },
    { key: 'deploy', label: 'Deploy', state: selectedReleaseDeployed ? 'done' : productionFailed ? 'error' : deploying || state === 'ready' ? 'current' : 'pending', detail: requestedRevision ? `requested ${shortSHA(requestedRevision)} / observed ${shortSHA(observedRevision) || '—'}` : undefined },
    { key: 'access', label: 'Enable access', state: accessLive ? 'done' : selectedReleaseDeployed ? 'current' : 'pending' },
  ]

  return {
    state, tone, message, detail,
    transitional: ['waiting', 'queued', 'running', 'finalizing', 'deploying'].includes(state),
    commitSHA, requestedRevision, observedRevision, buildURL: clean(run?.url), builtCount, totalCount, missing, steps,
    partial, artifactLag,
  }
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
