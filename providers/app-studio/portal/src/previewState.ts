export interface DevelopmentPreviewSyncState {
  hasPreviewRouteBinding: boolean
  previewURL: string
  readinessMessage: string
  authorizationError: string
	documentState: DevelopmentPreviewDocumentState
}

export type DevelopmentPreviewDocumentState = 'disabled' | 'connecting' | 'connected' | 'unavailable'

export interface DevelopmentPreviewDisplayPhaseState {
  previewURL: string
  authorizationError: string
	documentState: DevelopmentPreviewDocumentState
	frameLoaded?: boolean
	recoveryExhausted?: boolean
	starting?: boolean
}

export interface DevelopmentPreviewWakeState extends DevelopmentPreviewDisplayPhaseState {
  needsAuthorization: boolean
  authorizing: boolean
}

export type DevelopmentPreviewRecoveryAction =
	| { kind: 'reconnect'; delayMS: number }
	| { kind: 'reload'; delayMS: 0 }
	| { kind: 'background'; delayMS: number }

const developmentPreviewReconnectDelays = [1_000, 2_000, 4_000] as const
const developmentPreviewBackgroundRetryMS = 30_000

export function developmentPreviewDisplayPhase(state: DevelopmentPreviewDisplayPhaseState): string {
	if (!state.previewURL) {
		if (state.authorizationError) return 'Error'
		return state.starting ? 'Starting' : 'Pending'
	}
	if (state.documentState === 'connected') return 'Loaded'
	// Once the iframe has loaded, a console/annotation bridge failure must not
	// misrepresent the application itself as unavailable. The bridge is
	// advisory evidence; the rendered document remains useful without it.
	if (state.frameLoaded) return 'Loaded unverified'
	if (state.authorizationError || state.recoveryExhausted) return 'Error'
	if (state.documentState === 'disabled') return 'Loaded unverified'
	return 'Loading'
}

export function developmentPreviewSyncStatus(state: DevelopmentPreviewSyncState, refreshedStatus: string): string {
	if (state.previewURL && !state.authorizationError && state.documentState === 'connected') return refreshedStatus
	if (state.previewURL && !state.authorizationError && state.documentState === 'disabled') return 'Synced project files. Preview loaded; document verification is unavailable.'
  if (!state.hasPreviewRouteBinding) return 'Synced project files. Preview routing is not configured yet.'
  if (state.authorizationError) return 'Synced project files. Preview authorization failed.'
  if (state.readinessMessage) return `Synced project files. ${state.readinessMessage}`
  return 'Synced project files. Preview is getting ready.'
}

export function developmentPreviewShouldRefreshOnWake(
  state: DevelopmentPreviewWakeState,
): boolean {
  if (!state.needsAuthorization || state.authorizing) return false
	return !state.previewURL || !!state.authorizationError || state.documentState === 'connecting' || state.documentState === 'unavailable' || !!state.recoveryExhausted
}

/**
 * Escalates recovery without turning normal console-capability renewal into an
 * iframe reload. A failed document gets three cheap bridge probes, one fresh
 * authorization + iframe reload, then slow background document recovery. The
 * final phase must replace an error document because a failed iframe navigation
 * does not retry itself when the preview edge becomes reachable later.
 */
export function developmentPreviewRecoveryAction(
	attempt: number,
	documentReloadAttempted: boolean,
): DevelopmentPreviewRecoveryAction {
	const boundedAttempt = Number.isSafeInteger(attempt) && attempt > 0 ? attempt : 0
	const reconnectDelay = developmentPreviewReconnectDelays[boundedAttempt]
	if (reconnectDelay !== undefined) return { kind: 'reconnect', delayMS: reconnectDelay }
	if (!documentReloadAttempted) return { kind: 'reload', delayMS: 0 }
	return { kind: 'background', delayMS: developmentPreviewBackgroundRetryMS }
}
