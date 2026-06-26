export interface DevelopmentPreviewSyncState {
  hasPreviewRouteBinding: boolean
  previewURL: string
  readinessMessage: string
  authorizationError: string
}

export interface DevelopmentPreviewDisplayPhaseState {
  previewURL: string
  authorizationError: string
}

export function developmentPreviewDisplayPhase(state: DevelopmentPreviewDisplayPhaseState): string {
  if (state.authorizationError) return 'Error'
  if (state.previewURL) return 'Ready'
  return 'Pending'
}

export function developmentPreviewSyncStatus(state: DevelopmentPreviewSyncState, refreshedStatus: string): string {
  if (state.previewURL && !state.authorizationError) return refreshedStatus
  if (!state.hasPreviewRouteBinding) return 'Synced project files. Preview routing is not configured yet.'
  if (state.authorizationError) return 'Synced project files. Preview authorization failed.'
  if (state.readinessMessage) return `Synced project files. ${state.readinessMessage}`
  return 'Synced project files. Preview is getting ready.'
}
