export type GettingStartedReadState = 'unscoped' | 'checking' | 'error' | 'ready'

export interface GettingStartedReadInputs {
  hasScope: boolean
  catalogLoaded: boolean
  catalogLoading: boolean
  catalogMatchesOrganization: boolean
  catalogError: boolean
  bindingsMatchScope: boolean
  bindingsState: 'idle' | 'loading' | 'ready' | 'error'
  bindingsStale: boolean
}

export function gettingStartedReadState(input: GettingStartedReadInputs): GettingStartedReadState {
  if (!input.hasScope) return 'unscoped'
  if (input.catalogError || input.bindingsState === 'error') return 'error'
  if (
    !input.catalogLoaded ||
    input.catalogLoading ||
    !input.catalogMatchesOrganization ||
    !input.bindingsMatchScope ||
    input.bindingsState !== 'ready' ||
    input.bindingsStale
  ) return 'checking'
  return 'ready'
}

export function shouldAutoOpenGettingStarted(
  readState: GettingStartedReadState,
  enabledBindingCount: number,
  dismissed: boolean,
): boolean {
  return readState === 'ready' && enabledBindingCount === 0 && !dismissed
}

