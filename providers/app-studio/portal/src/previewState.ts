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
	recoveryExhausted?: boolean
	starting?: boolean
}

export interface DevelopmentPreviewWakeState extends DevelopmentPreviewDisplayPhaseState {
  needsAuthorization: boolean
  authorizing: boolean
}

export function developmentPreviewDisplayPhase(state: DevelopmentPreviewDisplayPhaseState): string {
	if (state.authorizationError || state.recoveryExhausted) return 'Error'
	if (!state.previewURL) return state.starting ? 'Starting' : 'Pending'
	if (state.documentState === 'connected') return 'Loaded'
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
