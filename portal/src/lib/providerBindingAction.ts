export type ProviderBindingAction = 'enable' | 'disable' | null

export interface ProviderBindingState {
  hasAPIExport: boolean
  enabled: boolean
  disabling: boolean
}

// Binding creation must work before runtime readiness: KCP publishes virtual
// workspace endpoints only after the first consumer binds to an export.
// Readiness gates opening a provider, not creating or removing its binding.
export function providerBindingAction(state: ProviderBindingState): ProviderBindingAction {
  if (!state.hasAPIExport || state.disabling) return null
  if (state.enabled) return 'disable'
  return 'enable'
}
