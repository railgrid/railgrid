export type ProviderBindingAction = 'enable' | 'disable' | null

export interface ProviderBindingState {
  hasAPIExport: boolean
  ready: boolean
  enabled: boolean
  disabling: boolean
}

// Binding removal remains available during provider outages, but a provider
// that was never enabled must not gain a misleading Disable action merely
// because readiness prevents Enable.
export function providerBindingAction(state: ProviderBindingState): ProviderBindingAction {
  if (!state.hasAPIExport || state.disabling) return null
  if (state.enabled) return 'disable'
  return state.ready ? 'enable' : null
}
